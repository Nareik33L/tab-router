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
	bootstrapTimeout   = 3 * time.Minute
	cookieLen          = 32
	authAttempts       = 8
	cookieWaitTimeout  = 10 * time.Second
	sessionCircuitLife = 30 * 24 * 60 * 60 // seconds; keep the first circuit for the run
	pinAttempts        = 8
	pinRetry           = 250 * time.Millisecond
)

var socksListenerRe = regexp.MustCompile(`(?:127\.0\.0\.1|\[::1\]):(\d+)`)

// Daemon is one local tor process with a SOCKS port and a control port.
// One process is started per identity so ExitNodes can be pinned independently
// and the session public IP cannot rotate.
type Daemon struct {
	Binary    string
	DataDir   string
	Log       io.Writer
	SocksAddr string

	mu         sync.Mutex
	cmd        *exec.Cmd
	ctrl       net.Conn
	ctrlR      *bufio.Reader
	exited     chan error
	stopped    bool
	pinnedExit string
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
	// IsolateSOCKSAuth keeps this identity on its own circuit. IsolateDestAddr
	// is intentionally omitted: a health check to ipify must reuse the same
	// exit as browsing, or the session IP looks like it rotated.
	fmt.Fprintf(&b, "SocksPort auto IsolateSOCKSAuth KeepAliveIsolateSOCKSAuth\n")
	fmt.Fprintf(&b, "MaxCircuitDirtiness %d\n", sessionCircuitLife)
	fmt.Fprintf(&b, "CircuitIdleTimeout %d\n", sessionCircuitLife)
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

// Newnym asks tor to use fresh circuits. Session IPs are pinned; callers
// must not use this to recover a route or the public IP will rotate.
func (d *Daemon) Newnym(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctrl == nil {
		return fmt.Errorf("tor control connection is down")
	}
	_, err := controlCmd(d.ctrl, d.ctrlR, "SIGNAL NEWNYM")
	return err
}

// PinnedExit is the hex fingerprint set by PinSOCKSUser, or empty.
func (d *Daemon) PinnedExit() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pinnedExit
}

// PinSOCKSUser looks up the BUILT circuit for socksUser and SETCONFs
// ExitNodes to that fingerprint so later circuits keep the same egress IP.
func (d *Daemon) PinSOCKSUser(ctx context.Context, socksUser string) error {
	if socksUser == "" {
		return fmt.Errorf("pin exit: empty SOCKS username")
	}
	var last error
	for attempt := 0; attempt < pinAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = d.pinSOCKSUserOnce(socksUser)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pinRetry):
		}
	}
	return last
}

func (d *Daemon) pinSOCKSUserOnce(socksUser string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctrl == nil {
		return fmt.Errorf("tor control connection is down")
	}
	streams, err := controlCmd(d.ctrl, d.ctrlR, "GETINFO stream-status")
	if err != nil {
		return err
	}
	circs, err := controlCmd(d.ctrl, d.ctrlR, "GETINFO circuit-status")
	if err != nil {
		return err
	}
	fp, err := exitForSOCKSUser(streams, circs, socksUser)
	if err != nil {
		return err
	}
	if _, err := controlCmd(d.ctrl, d.ctrlR, "SETCONF ExitNodes=$"+fp+" StrictNodes=1"); err != nil {
		return err
	}
	d.pinnedExit = fp
	// Drop leftover GENERAL circuits so a new stream cannot attach to a
	// different exit that happened to share this SOCKS username.
	for _, id := range parseGeneralCircuitIDs(circs) {
		_, _ = controlCmd(d.ctrl, d.ctrlR, "CLOSECIRCUIT "+id)
	}
	fmt.Fprintf(d.Log, "pinned SOCKS user to exit $%s\n", fp)
	return nil
}

// ReapplyPin restores a previously recorded ExitNodes pin (after a restart).
func (d *Daemon) ReapplyPin() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setPinnedLocked(d.pinnedExit)
}

// SetPinnedExit records and applies an exit fingerprint.
func (d *Daemon) SetPinnedExit(fp string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setPinnedLocked(fp)
}

func (d *Daemon) setPinnedLocked(fp string) error {
	fp = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(fp)), "$")
	d.pinnedExit = fp
	if fp == "" {
		return nil
	}
	if d.ctrl == nil {
		return fmt.Errorf("tor control connection is down")
	}
	_, err := controlCmd(d.ctrl, d.ctrlR, "SETCONF ExitNodes=$"+fp+" StrictNodes=1")
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
		if strings.HasPrefix(line, "250+") {
			// Data reply: 250+key=\nlines\n.\n250 OK
			rest := strings.TrimPrefix(line, "250+")
			if i := strings.IndexByte(rest, '='); i >= 0 {
				if val := rest[i+1:]; val != "" {
					b.WriteString(val)
					b.WriteByte('\n')
				}
			}
			for {
				body, err := r.ReadString('\n')
				if err != nil {
					return "", err
				}
				body = strings.TrimRight(body, "\r\n")
				if body == "." {
					break
				}
				b.WriteString(body)
				b.WriteByte('\n')
			}
			continue
		}
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

func parseSocksListeners(info string) []string {
	return socksListenerRe.FindAllString(info, -1)
}

func parseSocksListener(info string) (string, error) {
	addrs := parseSocksListeners(info)
	if len(addrs) == 0 {
		return "", fmt.Errorf("no 127.0.0.1 listener")
	}
	return addrs[0], nil
}

var (
	hopFingerprintRe = regexp.MustCompile(`\$([0-9A-Fa-f]{40})`)
	socksUserQuoted  = regexp.MustCompile(`SOCKS_USERNAME="([^"]*)"`)
	socksUserBare    = regexp.MustCompile(`SOCKS_USERNAME=(\S+)`)
)

func stripInfoKey(line, key string) string {
	line = strings.TrimSpace(line)
	prefix := key + "="
	if strings.HasPrefix(line, prefix) {
		return line[len(prefix):]
	}
	return line
}

func lastHopFingerprint(line string) string {
	hops := hopFingerprintRe.FindAllStringSubmatch(line, -1)
	if len(hops) == 0 {
		return ""
	}
	return strings.ToUpper(hops[len(hops)-1][1])
}

// exitForSOCKSUser prefers the circuit that actually carried a stream for
// socksUser (GETINFO stream-status). Falling back to circuit-status alone
// can pin a predicted/extra circuit and then the browser egress != V1 IP.
func exitForSOCKSUser(streams, circs, socksUser string) (string, error) {
	if id := parseStreamCircuit(streams, socksUser); id != "" {
		if fp, err := parseCircuitExitByID(circs, id); err == nil {
			return fp, nil
		}
	}
	return parseCircuitExit(circs, socksUser)
}

// parseStreamCircuit returns the most recent non-zero circuit id used by
// socksUser (SUCCEEDED/CLOSED/FAILED snapshots).
func parseStreamCircuit(status, socksUser string) string {
	if socksUser == "" {
		return ""
	}
	var circ string
	for _, line := range strings.Split(status, "\n") {
		line = stripInfoKey(line, "stream-status")
		if line == "" {
			continue
		}
		if circuitSOCKSUser(line) != socksUser && !strings.Contains(line, socksUser) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		st, id := fields[1], fields[2]
		if id == "0" {
			continue
		}
		switch st {
		case "SUCCEEDED", "CLOSED", "FAILED", "DETACHED", "SENTCONNECT":
			circ = id
		}
	}
	return circ
}

func parseCircuitExitByID(status, circID string) (string, error) {
	if circID == "" {
		return "", fmt.Errorf("no circuit id")
	}
	for _, line := range strings.Split(status, "\n") {
		line = stripInfoKey(line, "circuit-status")
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != circID || fields[1] != "BUILT" {
			continue
		}
		if fp := lastHopFingerprint(line); fp != "" {
			return fp, nil
		}
	}
	return "", fmt.Errorf("no BUILT circuit %s", circID)
}

func parseGeneralCircuitIDs(status string) []string {
	var ids []string
	for _, line := range strings.Split(status, "\n") {
		line = stripInfoKey(line, "circuit-status")
		if !strings.Contains(line, "BUILT") || strings.Contains(line, "PURPOSE=HS_") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "BUILT" {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

// parseCircuitExit returns the exit fingerprint (40 hex chars, no $) of a
// BUILT circuit that carried socksUser. Prefers PURPOSE=GENERAL and the
// last matching circuit when several exist.
func parseCircuitExit(status, socksUser string) (string, error) {
	if socksUser == "" {
		return "", fmt.Errorf("no SOCKS username")
	}
	var general, any string
	for _, line := range strings.Split(status, "\n") {
		line = stripInfoKey(line, "circuit-status")
		if line == "" || !strings.Contains(line, "BUILT") {
			continue
		}
		if circuitSOCKSUser(line) != socksUser {
			continue
		}
		fp := lastHopFingerprint(line)
		if fp == "" {
			continue
		}
		any = fp
		if strings.Contains(line, "PURPOSE=GENERAL") || !strings.Contains(line, "PURPOSE=") {
			general = fp
		}
	}
	if general != "" {
		return general, nil
	}
	if any != "" {
		return any, nil
	}
	return "", fmt.Errorf("no BUILT circuit for SOCKS user")
}

func circuitSOCKSUser(line string) string {
	if m := socksUserQuoted.FindStringSubmatch(line); len(m) == 2 {
		return m[1]
	}
	if m := socksUserBare.FindStringSubmatch(line); len(m) == 2 {
		return strings.Trim(m[1], `"`)
	}
	return ""
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
