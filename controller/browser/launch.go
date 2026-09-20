package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nareik33L/tab-router/controller/identity"
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
	// Environment is the identity's pinned device configuration.
	Environment identity.Environment
	// DownloadDir is the absolute download directory for the identity.
	DownloadDir string
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
	env    identity.Environment
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
	env := opts.Environment
	if env.Locale == "" {
		env = identity.NewEnvironment(1, identity.EnvironmentDefaults{})
	}
	if err := SeedPreferences(opts.ProfileDir, env, opts.DownloadDir); err != nil {
		return nil, fmt.Errorf("browser: seed preferences: %w", err)
	}
	args := append([]string{"--user-data-dir=" + opts.ProfileDir}, HardeningFlags(opts.GateAddr)...)
	args = append(args, EnvironmentFlags(env)...)
	if opts.Headless {
		args = append(args, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--mute-audio")
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, "about:blank")

	proc, tr, err := spawnWithPipe(opts.Binary, args, scrubEnv(os.Environ(), env.Timezone), opts.ProfileDir, opts.Stderr)
	if err != nil {
		return nil, fmt.Errorf("browser: start %s: %w", filepath.Base(opts.Binary), err)
	}
	b := &Browser{proc: proc, pid: proc.Pid, plat: platform.Current(), opts: opts, env: env, exited: make(chan struct{})}
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
	if err := b.enforceEnvironment(hctx); err != nil {
		_ = b.Kill()
		return nil, fmt.Errorf("browser: apply environment: %w", err)
	}
	if opts.DownloadDir != "" {
		if err := b.SetDownloadDir(hctx, opts.DownloadDir); err != nil {
			_ = b.Kill()
			return nil, fmt.Errorf("browser: download dir: %w", err)
		}
	}
	return b, nil
}

// SetDownloadDir forces every download in this browser into dir, without
// prompting, regardless of profile settings.
func (b *Browser) SetDownloadDir(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return b.cdp.Call(ctx, "", "Browser.setDownloadBehavior", map[string]any{
		"behavior": "allow", "downloadPath": dir, "eventsEnabled": true,
	}, nil)
}

// EnvironmentFlags translates the pinned environment into Chromium flags.
func EnvironmentFlags(env identity.Environment) []string {
	flags := []string{
		"--lang=" + env.Locale,
		fmt.Sprintf("--window-size=%d,%d", env.WindowWidth, env.WindowHeight),
		fmt.Sprintf("--window-position=%d,%d", env.WindowX, env.WindowY),
		fmt.Sprintf("--force-device-scale-factor=%g", env.DeviceScaleFactor),
		// A fixed colour profile keeps rendering identical across displays.
		"--force-color-profile=srgb",
	}
	if env.ColorScheme == "dark" {
		flags = append(flags, "--force-dark-mode")
	}
	return flags
}

// scrubEnv removes proxy-related variables so nothing but our flags can
// influence Chromium's network configuration, and pins TZ to the identity's
// timezone (honoured natively on macOS; enforced via DevTools everywhere).
func scrubEnv(env []string, timezone string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToLower(k) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy", "socks_proxy", "ftp_proxy", "tz":
			continue
		}
		out = append(out, kv)
	}
	if timezone != "" {
		out = append(out, "TZ="+timezone)
	}
	return out
}

// enforceEnvironment turns on auto-attach so that every page target the
// browser creates (including tabs the user opens later) receives the
// identity's timezone override before any script runs.
func (b *Browser) enforceEnvironment(ctx context.Context) error {
	events, cancel := b.cdp.Subscribe("", "Target.attachedToTarget")
	go b.autoAttachLoop(events, cancel)
	return b.cdp.Call(ctx, "", "Target.setAutoAttach", map[string]any{
		"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true,
	}, nil)
}

func (b *Browser) autoAttachLoop(events <-chan Event, cancel func()) {
	defer cancel()
	for ev := range events {
		var p struct {
			SessionID  string `json:"sessionId"`
			TargetInfo struct {
				Type string `json:"type"`
			} `json:"targetInfo"`
			WaitingForDebugger bool `json:"waitingForDebugger"`
		}
		if err := json.Unmarshal(ev.Params, &p); err != nil || p.SessionID == "" {
			continue
		}
		ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
		if p.TargetInfo.Type == "page" || p.TargetInfo.Type == "iframe" {
			_ = b.cdp.Call(ctx, p.SessionID, "Emulation.setTimezoneOverride", map[string]any{"timezoneId": b.env.Timezone}, nil)
		}
		if p.WaitingForDebugger {
			_ = b.cdp.Call(ctx, p.SessionID, "Runtime.runIfWaitingForDebugger", nil, nil)
		}
		done()
	}
}

// Environment returns the environment the browser was launched with.
func (b *Browser) Environment() identity.Environment { return b.env }

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
