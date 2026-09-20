package tor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	bootstrapTimeout  = 3 * time.Minute
	cookieLen         = 32
	authAttempts      = 8
	cookieWaitTimeout = 10 * time.Second
)

var socksListenerRe = regexp.MustCompile(`(?:127\.0\.0\.1|\[::1\]):(\d+)`)

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
	exited  chan error
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
	// Drop the previous control socket and cookie. A leftover 32-byte
	// cookie from the last run authenticates against this process and
	// fails with "Got mismatched authentication cookie".
	if err := resetSession(session); err != nil {
		return err
	}

	torrc := filepath.Join(session, "torrc")
	if err := os.WriteFile(torrc, []byte(d.torrc(session)), 0o600); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(session, "tor.stderr"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := prepareExecutable(d.Binary); err != nil {
		logf.Close()
		return fmt.Errorf("prepare tor: %w", err)
	}
	libDir := filepath.Dir(d.Binary)
	cmd := exec.Command(d.Binary, "-f", torrc)
	hideWindow(cmd)
	cmd.Dir = libDir
	cmd.Env = withLibPath(os.Environ(), libDir)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return fmt.Errorf("start tor: %w", err)
	}
	d.cmd = cmd
	d.exited = make(chan error, 1)
	go func() {
		err := cmd.Wait()
		logf.Close()
		d.exited <- err
	}()

	waitCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()
	if err := d.waitBootstrap(waitCtx, session); err != nil {
		_ = cmd.Process.Kill()
		d.cmd = nil
		return err
	}
	fmt.Fprintf(d.Log, "started local tor (socks %s)\n", d.SocksAddr)
	return nil
}

func (d *Daemon) torrc(session string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "DataDirectory %s\n", quotePath(session))
	fmt.Fprintf(&b, "SocksPort auto IsolateSOCKSAuth IsolateDestAddr\n")
	fmt.Fprintf(&b, "CookieAuthentication 1\n")
	fmt.Fprintf(&b, "CookieAuthFile %s\n", quotePath(filepath.Join(session, "control_auth_cookie")))
	fmt.Fprintf(&b, "AvoidDiskWrites 1\n")
	fmt.Fprintf(&b, "SafeLogging 1\n")
	fmt.Fprintf(&b, "Log notice stdout\n")
	fmt.Fprintf(&b, "__OwningControllerProcess %d\n", os.Getpid())
	if runtime.GOOS == "windows" {
		fmt.Fprintf(&b, "ControlPort auto\n")
		fmt.Fprintf(&b, "ControlPortWriteToFile %s\n", quotePath(filepath.Join(session, "control.port")))
	} else {
		fmt.Fprintf(&b, "ControlPort unix:%s\n", quotePath(filepath.Join(session, "control")))
	}
	if geo := filepath.Join(filepath.Dir(d.Binary), "..", "data", "geoip"); fileExists(geo) {
		fmt.Fprintf(&b, "GeoIPFile %s\n", quotePath(geo))
	}
	if geo6 := filepath.Join(filepath.Dir(d.Binary), "..", "data", "geoip6"); fileExists(geo6) {
		fmt.Fprintf(&b, "GeoIPv6File %s\n", quotePath(geo6))
	}
	return b.String()
}

func (d *Daemon) waitBootstrap(ctx context.Context, session string) error {
	fallbackCookie := filepath.Join(session, "control_auth_cookie")
	var last error
	for attempt := 0; attempt < authAttempts; attempt++ {
		if err := d.checkExited(); err != nil {
			return d.fail(session, "tor exited before control cookie appeared: %v", err)
		}
		conn, err := d.dialControl(ctx, session)
		if err != nil {
			return d.fail(session, "tor control port did not open: %v", err)
		}
		r := bufio.NewReader(conn)
		cookiePath := fallbackCookie
		if info, err := controlCmd(conn, r, "PROTOCOLINFO 1"); err == nil {
			if p := parseCookieFile(info); p != "" {
				cookiePath = p
			}
		}
		cookie, err := waitAuthCookie(ctx, cookiePath)
		if err != nil {
			_ = conn.Close()
			last = err
			if err := d.checkExited(); err != nil {
				return d.fail(session, "tor exited before control cookie appeared: %v", err)
			}
			time.Sleep(150 * time.Millisecond)
			continue
		}
		if _, err := controlCmd(conn, r, "AUTHENTICATE "+hex.EncodeToString(cookie)); err != nil {
			_ = conn.Close()
			last = err
			if !authCookieMismatch(err) {
				return d.fail(session, "tor authenticate: %v", err)
			}
			time.Sleep(150 * time.Millisecond)
			continue
		}
		d.ctrl = conn
		d.ctrlR = r
		last = nil
		break
	}
	if d.ctrl == nil {
		if last == nil {
			last = fmt.Errorf("no control connection")
		}
		return d.fail(session, "tor authenticate: %v", last)
	}
	socks, err := controlCmd(d.ctrl, d.ctrlR, "GETINFO net/listeners/socks")
	if err != nil {
		return d.fail(session, "tor socks listener: %v", err)
	}
	addr, err := parseSocksListener(socks)
	if err != nil {
		return d.fail(session, "tor socks listener: %v (got %q)", err, socks)
	}
	d.SocksAddr = addr
	fmt.Fprintf(d.Log, "waiting for tor circuits...\n")
	for {
		if ctx.Err() != nil {
			return d.fail(session, "tor bootstrap: %v", ctx.Err())
		}
		if err := d.checkExited(); err != nil {
			return d.fail(session, "tor exited during bootstrap: %v", err)
		}
		out, err := controlCmd(d.ctrl, d.ctrlR, "GETINFO status/bootstrap-phase")
		if err != nil {
			return d.fail(session, "tor bootstrap: %v", err)
		}
		if strings.Contains(out, "PROGRESS=100") {
			fmt.Fprintf(d.Log, "tor bootstrap complete\n")
			return nil
		}
		select {
		case <-ctx.Done():
			return d.fail(session, "tor bootstrap: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (d *Daemon) dialControl(ctx context.Context, session string) (net.Conn, error) {
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := d.checkExited(); err != nil {
			return nil, err
		}
		var conn net.Conn
		var err error
		if runtime.GOOS == "windows" {
			port, perr := readControlPortFile(filepath.Join(session, "control.port"))
			if perr == nil {
				conn, err = net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
			} else {
				err = perr
			}
		} else {
			conn, err = net.DialTimeout("unix", filepath.Join(session, "control"), time.Second)
		}
		if err == nil {
			return conn, nil
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return nil, last
}

func (d *Daemon) checkExited() error {
	if d.exited == nil {
		return nil
	}
	select {
	case err := <-d.exited:
		if err == nil {
			return fmt.Errorf("process exited")
		}
		return err
	default:
		return nil
	}
}

func (d *Daemon) fail(session, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if tail := tailFile(filepath.Join(session, "tor.stderr"), 2048); tail != "" {
		msg += "\n" + tail
	}
	return fmt.Errorf("%s", msg)
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

func resetSession(session string) error {
	_ = os.RemoveAll(session)
	return os.MkdirAll(session, 0o700)
}

var cookieFileRe = regexp.MustCompile(`COOKIEFILE="([^"]+)"`)

func parseCookieFile(protocolInfo string) string {
	m := cookieFileRe.FindStringSubmatch(protocolInfo)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func waitAuthCookie(ctx context.Context, path string) ([]byte, error) {
	deadline := time.Now().Add(cookieWaitTimeout)
	var last error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		b, err := os.ReadFile(path)
		switch {
		case err != nil:
			last = err
		case len(b) != cookieLen:
			last = fmt.Errorf("cookie length %d, want %d", len(b), cookieLen)
		default:
			time.Sleep(50 * time.Millisecond)
			b2, err := os.ReadFile(path)
			if err == nil && len(b2) == cookieLen && bytes.Equal(b, b2) {
				return b2, nil
			}
			last = fmt.Errorf("control cookie still being written")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return nil, last
}

func authCookieMismatch(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "515") || strings.Contains(s, "cookie")
}

func parseSocksListener(info string) (string, error) {
	m := socksListenerRe.FindString(info)
	if m == "" {
		return "", fmt.Errorf("no 127.0.0.1 listener")
	}
	return m, nil
}

func readControlPortFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	line = strings.TrimPrefix(line, "PORT=")
	host, port, err := net.SplitHostPort(strings.TrimSpace(line))
	if err != nil {
		return "", err
	}
	if host != "127.0.0.1" && host != "::1" {
		return "", fmt.Errorf("unexpected control host %s", host)
	}
	return port, nil
}

func withLibPath(env []string, dir string) []string {
	out := make([]string, 0, len(env)+2)
	var hasLD, hasDY bool
	sep := string(os.PathListSeparator)
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "LD_LIBRARY_PATH="):
			out = append(out, "LD_LIBRARY_PATH="+dir+sep+strings.TrimPrefix(e, "LD_LIBRARY_PATH="))
			hasLD = true
		case strings.HasPrefix(e, "DYLD_LIBRARY_PATH="):
			out = append(out, "DYLD_LIBRARY_PATH="+dir+sep+strings.TrimPrefix(e, "DYLD_LIBRARY_PATH="))
			hasDY = true
		default:
			out = append(out, e)
		}
	}
	if !hasLD {
		out = append(out, "LD_LIBRARY_PATH="+dir)
	}
	if !hasDY {
		out = append(out, "DYLD_LIBRARY_PATH="+dir)
	}
	out = append(out, "DYLD_FALLBACK_LIBRARY_PATH="+dir)
	return out
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

func slash(p string) string { return filepath.ToSlash(p) }

func quotePath(p string) string { return `"` + slash(p) + `"` }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
