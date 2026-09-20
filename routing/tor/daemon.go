package tor

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const bootstrapTimeout = 2 * time.Minute

// Daemon is one local tor process with a SOCKS port and a control port.
type Daemon struct {
	Binary    string
	DataDir   string
	Log       io.Writer
	SocksAddr string

	mu      sync.Mutex
	cmd     *exec.Cmd
	ctrl    net.Conn
	ctrlR   *bufio.Reader
	stopped bool
}

// Start launches tor and waits until circuits can be built.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd != nil && d.cmd.Process != nil {
		return nil
	}
	if d.Log == nil {
		d.Log = io.Discard
	}
	session := filepath.Join(d.DataDir, "session")
	if err := os.MkdirAll(session, 0o700); err != nil {
		return err
	}
	socksPort, err := freePort()
	if err != nil {
		return err
	}
	ctrlPort, err := freePort()
	if err != nil {
		return err
	}
	torrc := filepath.Join(session, "torrc")
	body := fmt.Sprintf("DataDirectory %s\nSocksPort 127.0.0.1:%d IsolateSOCKSAuth IsolateDestAddr\nControlPort 127.0.0.1:%d\nCookieAuthentication 1\nAvoidDiskWrites 1\nSafeLogging 1\nLog notice file %s\n",
		quotePath(session), socksPort, ctrlPort, quotePath(filepath.Join(session, "notice.log")))
	if geo := filepath.Join(filepath.Dir(d.Binary), "geoip"); fileExists(geo) {
		body += fmt.Sprintf("GeoIPFile %s\n", quotePath(geo))
	}
	if geo6 := filepath.Join(filepath.Dir(d.Binary), "geoip6"); fileExists(geo6) {
		body += fmt.Sprintf("GeoIPv6File %s\n", quotePath(geo6))
	}
	if err := os.WriteFile(torrc, []byte(body), 0o600); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(session, "tor.stderr"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(d.Binary, "-f", torrc)
	hideWindow(cmd)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return fmt.Errorf("start tor: %w", err)
	}
	d.cmd = cmd
	d.SocksAddr = fmt.Sprintf("127.0.0.1:%d", socksPort)
	fmt.Fprintf(d.Log, "started local tor (socks %s)\n", d.SocksAddr)

	waitCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()
	if err := d.waitBootstrap(waitCtx, ctrlPort, session); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		d.cmd = nil
		return err
	}
	go func() {
		_ = cmd.Wait()
		logf.Close()
	}()
	return nil
}

func (d *Daemon) waitBootstrap(ctx context.Context, ctrlPort int, session string) error {
	cookiePath := filepath.Join(session, "control_auth_cookie")
	var conn net.Conn
	var err error
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ctrlPort), time.Second)
		if err == nil {
			break
		}
		if d.cmd.ProcessState != nil && d.cmd.ProcessState.Exited() {
			return fmt.Errorf("tor exited before control port came up")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if conn == nil {
		return fmt.Errorf("tor control port did not open: %v", err)
	}
	// Cookie appears shortly after the control port.
	var cookie []byte
	for i := 0; i < 50; i++ {
		cookie, err = os.ReadFile(cookiePath)
		if err == nil && len(cookie) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(cookie) == 0 {
		conn.Close()
		return fmt.Errorf("tor control cookie missing")
	}
	r := bufio.NewReader(conn)
	if _, err := controlCmd(conn, r, "AUTHENTICATE "+hex.EncodeToString(cookie)); err != nil {
		conn.Close()
		return fmt.Errorf("tor authenticate: %w", err)
	}
	d.ctrl = conn
	d.ctrlR = r
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("tor bootstrap: %w", ctx.Err())
		}
		out, err := controlCmd(d.ctrl, d.ctrlR, "GETINFO status/bootstrap-phase")
		if err != nil {
			return fmt.Errorf("tor bootstrap: %w", err)
		}
		if strings.Contains(out, "PROGRESS=100") {
			fmt.Fprintf(d.Log, "tor bootstrap complete\n")
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tor bootstrap: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Newnym asks tor to use fresh circuits. Stream isolation still comes from
// distinct SOCKS credentials per identity.
func (d *Daemon) Newnym(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctrl == nil {
		return fmt.Errorf("tor control connection is down")
	}
	_, err := controlCmd(d.ctrl, d.ctrlR, "SIGNAL NEWNYM")
	return err
}

// Stop terminates the daemon.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return nil
	}
	d.stopped = true
	if d.ctrl != nil {
		_, _ = controlCmd(d.ctrl, d.ctrlR, "SIGNAL SHUTDOWN")
		_ = d.ctrl.Close()
		d.ctrl = nil
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
	return nil
}

func controlCmd(conn net.Conn, r *bufio.Reader, cmd string) (string, error) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	if _, err := io.WriteString(conn, cmd+"\r\n"); err != nil {
		return "", err
	}
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "250-") {
			b.WriteString(strings.TrimPrefix(line, "250-"))
			b.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(line, "250 ") || line == "250 OK" {
			if rest := strings.TrimPrefix(line, "250 "); rest != "OK" && rest != line {
				b.WriteString(rest)
			}
			return b.String(), nil
		}
		if len(line) >= 3 && line[0] >= '4' && line[0] <= '6' {
			return "", fmt.Errorf("%s", line)
		}
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func slash(p string) string { return filepath.ToSlash(p) }

func quotePath(p string) string { return `"` + slash(p) + `"` }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
