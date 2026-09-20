package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nareik33L/tab-router/routing/platform"
)

// LaunchOptions configures one identity's Chromium process.
type LaunchOptions struct {
	Binary     string
	ProfileDir string
	// GateAddr is the identity's gate, "127.0.0.1:port".
	GateAddr string
	// Headless runs without a window (tests and CI).
	Headless bool
	// ExtraArgs are appended verbatim (dev builds only).
	ExtraArgs []string
	// Stderr receives Chromium's stderr; nil discards it.
	Stderr *os.File
}

// Browser is a running Chromium process and its CDP connection.
type Browser struct {
	proc   *os.Process
	pid    int
	cdp    *Client
	plat   platform.Provider
	opts   LaunchOptions
	exited chan struct{}
}

// Launch starts Chromium with the hardening flag set and connects over the
// inherited DevTools pipe. The initial tab is about:blank; nothing is
// navigated until the caller says so.
func Launch(ctx context.Context, opts LaunchOptions) (*Browser, error) {
	if opts.Binary == "" || opts.ProfileDir == "" || opts.GateAddr == "" {
		return nil, errors.New("browser: Binary, ProfileDir and GateAddr are required")
	}
	if err := os.MkdirAll(opts.ProfileDir, 0o700); err != nil {
		return nil, err
	}
	if err := SeedPreferences(opts.ProfileDir); err != nil {
		return nil, fmt.Errorf("browser: seed preferences: %w", err)
	}
	args := append([]string{"--user-data-dir=" + opts.ProfileDir}, HardeningFlags(opts.GateAddr)...)
	if opts.Headless {
		args = append(args, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--mute-audio")
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, "about:blank")

	proc, tr, err := spawnWithPipe(opts.Binary, args, scrubEnv(os.Environ()), opts.ProfileDir, opts.Stderr)
	if err != nil {
		return nil, fmt.Errorf("browser: start %s: %w", filepath.Base(opts.Binary), err)
	}
	b := &Browser{proc: proc, pid: proc.Pid, plat: platform.Current(), opts: opts, exited: make(chan struct{})}
	go func() {
		_, _ = proc.Wait()
		close(b.exited)
	}()
	b.cdp = NewClient(tr)

	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var ver struct {
		Product string `json:"product"`
	}
	if err := b.cdp.Call(hctx, "", "Browser.getVersion", nil, &ver); err != nil {
		_ = b.Kill()
		return nil, fmt.Errorf("browser: devtools handshake: %w", err)
	}
	return b, nil
}

// scrubEnv removes proxy-related variables so nothing but our flags can
// influence Chromium's network configuration.
func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToLower(k) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy", "socks_proxy", "ftp_proxy":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// PID is the root Chromium process id.
func (b *Browser) PID() int { return b.pid }

// CDP exposes the protocol client.
func (b *Browser) CDP() *Client { return b.cdp }

// Exited is closed when the root process has exited.
func (b *Browser) Exited() <-chan struct{} { return b.exited }

// ProcessTree lists the root PID and all descendants.
func (b *Browser) ProcessTree() ([]int, error) { return b.plat.ProcessTree(b.pid) }

// Endpoints enumerates network endpoints owned by the process tree.
func (b *Browser) Endpoints() ([]platform.Endpoint, error) {
	pids, err := b.ProcessTree()
	if err != nil {
		return nil, err
	}
	return b.plat.RemoteEndpoints(pids)
}

// Close asks Chromium to exit gracefully, then kills the tree if needed.
func (b *Browser) Close(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = b.cdp.Call(cctx, "", "Browser.close", nil, nil)
	select {
	case <-b.exited:
	case <-time.After(5 * time.Second):
	}
	return b.Kill()
}

// Kill terminates the whole process tree and closes the CDP transport.
func (b *Browser) Kill() error {
	_ = b.plat.KillTree(b.pid)
	_ = b.proc.Kill()
	select {
	case <-b.exited:
	case <-time.After(5 * time.Second):
	}
	if b.cdp != nil {
		_ = b.cdp.Close()
	}
	return nil
}

// Alive reports whether the root process is still running.
func (b *Browser) Alive() bool {
	select {
	case <-b.exited:
		return false
	default:
		return true
	}
}
