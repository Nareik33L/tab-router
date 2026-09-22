// Command tab-router starts isolated browser identities, each bound to its
// own network route, verifies the isolation and reports the result.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nareik33L/tab-router/browser/chromium"
	"github.com/Nareik33L/tab-router/controller/activation"
	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/controller/config"
	"github.com/Nareik33L/tab-router/controller/startup"
	"github.com/Nareik33L/tab-router/ipc"
	"github.com/Nareik33L/tab-router/routing/manager"
	"github.com/Nareik33L/tab-router/routing/provider"
)

const (
	exitOK           = 0
	exitUsage        = 1
	exitVerification = 2
	exitRoute        = 3
	exitBrowser      = 4
	exitRunning      = 5
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "provider" {
		return runProvider(args[1:])
	}
	fs := flag.NewFlagSet("tab-router", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		identities  = fs.Int("identities", 0, "number of identities to start (default 2)")
		urlFlag     = fs.String("url", "", "URL to open in every identity after verification")
		fresh       = fs.Bool("fresh", false, "create a new identity set instead of reusing the current one")
		configPath  = fs.String("config", "", "path to config.toml")
		routesPath  = fs.String("routes", "", "path to routes.toml")
		dataDir     = fs.String("data-dir", "", "data directory (default: per-user application data)")
		headless    = fs.Bool("headless", false, "run browsers without windows (testing)")
		status      = fs.Bool("status", false, "show the running session's status")
		stop        = fs.Bool("stop", false, "stop the running session")
		diagnostics = fs.Bool("diagnostics", false, "run the isolation checks and print a report")
		asJSON      = fs.Bool("json", false, "machine-readable output for --status/--diagnostics")
		version     = fs.Bool("version", false, "print version")
		ascii       = fs.Bool("ascii", runtime.GOOS == "windows", "use ASCII instead of ✓/→ in output")
		noPrompt    = fs.Bool("no-interactive", false, "never prompt, even on a terminal")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  tab-router [--identities N] [--url URL] [--fresh]")
		fmt.Fprintln(os.Stderr, "  tab-router --status | --stop | --diagnostics [--json]")
		fmt.Fprintln(os.Stderr, "  tab-router deactivate")
		fmt.Fprintln(os.Stderr, "  tab-router route-check            (dev: print the public IP of each route)")
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
	}
	// Allow the single dev subcommand before flags.
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *version {
		fmt.Println("tab-router", startup.Version)
		return exitOK
	}
	if *identities > config.MaxIdentities {
		fmt.Fprintf(os.Stderr, "identities=%d exceeds the current cap of %d; the 2-identity milestone must pass first (docs/ENGINEERING_PLAN.md M9)\n", *identities, config.MaxIdentities)
		return exitUsage
	}

	ov := config.Overrides{Identities: *identities, URL: *urlFlag, URLSet: *urlFlag != "", Fresh: *fresh, Headless: *headless,
		DataDir: *dataDir, ConfigPath: *configPath, RoutesPath: *routesPath}

	switch {
	case sub == "route-check":
		return routeCheck(ov)
	case sub == "deactivate":
		return deactivate(ov)
	case sub != "":
		fmt.Fprintf(os.Stderr, "unknown command %q\n", sub)
		return exitUsage
	case *status:
		return showStatus(ov, *asJSON)
	case *stop:
		return stopSession(ov)
	case *diagnostics:
		return runDiagnostics(ov, *asJSON, !*ascii)
	}

	interactive := !*noPrompt && isTerminal(os.Stdin)
	if interactive && fs.NFlag() == 0 && sub == "" {
		n, u, ok := prompt()
		if !ok {
			return exitUsage
		}
		ov.Identities, ov.URL, ov.URLSet = n, u, true
	}
	return start(ov, !*ascii, interactive)
}

func loadConfig(ov config.Overrides) (config.Config, bool) {
	cfg, err := config.Load(ov)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return cfg, false
	}
	return cfg, true
}

func start(ov config.Overrides, unicode bool, interactive bool) int {
	cfg, ok := loadConfig(ov)
	if !ok {
		return exitUsage
	}
	if _, err := ipc.Dial(cfg.DataDir); err == nil {
		fmt.Fprintln(os.Stderr, "tab-router is already running; use --status or --stop")
		return exitRunning
	}
	prov, err := prepareProvisioner(cfg, interactive)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitRoute
	}
	if err := ensureChromium(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitBrowser
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := startup.NewReporter(os.Stdout, unicode)
	sess, err := startup.Run(ctx, cfg, rep, startup.Options{Pin: chromium.Pin(), Unicode: unicode, Provisioner: prov})
	if err != nil {
		return exitFor(err)
	}

	stopCh := make(chan struct{}, 1)
	requestStop := func() {
		select {
		case stopCh <- struct{}{}:
		default:
		}
	}
	srv, err := ipc.Listen(cfg.DataDir, sess.IPCHandlers(requestStop))
	if err != nil {
		rep.Line("ERROR: control channel: %v", err)
		sess.Shutdown(ctx)
		return exitRunning
	}
	defer srv.Close()
	rep.Line("Control channel: %s", srv.Endpoint())
	rep.Line("Press Ctrl-C or run `tab-router --stop` to shut down.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			rep.Line("Shutting down...")
			sess.Shutdown(ctx)
			return exitOK
		case <-stopCh:
			rep.Line("Stop requested; shutting down...")
			sess.Shutdown(ctx)
			return exitOK
		case <-tick.C:
			if sess.AllBrowsersExited() {
				rep.Line("All browser windows closed; shutting down.")
				sess.Shutdown(ctx)
				return exitOK
			}
		}
	}
}

func exitFor(err error) int {
	switch {
	case errors.Is(err, startup.ErrVerificationFailed):
		return exitVerification
	case errors.Is(err, startup.ErrRouteFailed):
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitRoute
	case errors.Is(err, startup.ErrBrowserFailed):
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitBrowser
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
}

func showStatus(ov config.Overrides, asJSON bool) int {
	cfg, ok := loadConfig(ov)
	if !ok {
		return exitUsage
	}
	c, err := ipc.Dial(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tab-router is not running")
		return exitUsage
	}
	var st startup.Status
	if err := c.Call("status", nil, &st); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return exitOK
	}
	printStatus(st)
	return exitOK
}

func printStatus(st startup.Status) {
	fmt.Printf("TAB ROUTER %s   pid %d   up %s\n", st.Version, st.PID, time.Since(st.StartedAt).Round(time.Second))
	fmt.Printf("Browser: %s (%s)\n", st.Chromium, st.Source)
	if st.HostIPv4 != "" {
		fmt.Printf("Host IP: %s (never used by identities)\n", st.HostIPv4)
	}
	for _, id := range st.Identities {
		state := id.RouteStatus
		if !id.BrowserAlive {
			state += ", browser closed"
		}
		fmt.Printf("Identity %03d -> %s (%s) -> %s   egress %s\n", id.Index, id.Route, id.RouteType, state, id.EgressIP)
		if id.CurrentURL != "" {
			fmt.Printf("  URL: %s\n", id.CurrentURL)
		}
		fmt.Printf("  Environment: %s, %s, %dx%d\n", id.Environment.Locale, id.Environment.Timezone, id.Environment.WindowWidth, id.Environment.WindowHeight)
		if id.Health.Down {
			fmt.Printf("  DOWN since %s: %s\n", id.Health.DownSince.Format(time.RFC3339), id.Health.LastError)
		}
		for _, l := range id.Health.Leaks {
			fmt.Printf("  ALARM non-gate endpoint: %s\n", l)
		}
	}
	for _, e := range st.Events {
		fmt.Println(" ", e)
	}
}

func stopSession(ov config.Overrides) int {
	cfg, ok := loadConfig(ov)
	if !ok {
		return exitUsage
	}
	c, err := ipc.Dial(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tab-router is not running")
		return exitUsage
	}
	if err := c.Call("shutdown", nil, nil); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := ipc.Dial(cfg.DataDir); err != nil {
			fmt.Println("stopped")
			return exitOK
		}
		time.Sleep(250 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "shutdown requested but the session is still running")
	return exitUsage
}

func runDiagnostics(ov config.Overrides, asJSON, unicode bool) int {
	cfg, ok := loadConfig(ov)
	if !ok {
		return exitUsage
	}
	var st startup.Status
	if c, err := ipc.Dial(cfg.DataDir); err == nil {
		if err := c.Call("diagnostics.run", nil, &st); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitUsage
		}
	} else {
		// No live session: start a temporary headless one, which runs the
		// full verification, then tear it down.
		cfg.Headless = true
		cfg.StartupURL = ""
		if err := ensureChromium(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitBrowser
		}
		ctx := context.Background()
		rep := startup.NewReporter(os.Stderr, unicode)
		sess, err := startup.Run(ctx, cfg, rep, startup.Options{Pin: chromium.Pin(), Unicode: unicode})
		if err != nil {
			return exitFor(err)
		}
		st = sess.Status()
		sess.Shutdown(ctx)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
	} else {
		printDiagnostics(st)
	}
	for _, id := range st.Identities {
		if !id.Verified {
			return exitVerification
		}
	}
	return exitOK
}

func printDiagnostics(st startup.Status) {
	okStr := func(b bool) string {
		if b {
			return "OK  "
		}
		return "FAIL"
	}
	fmt.Printf("Browser        OK   %s (%s)\n", st.Chromium, st.Source)
	fmt.Printf("Controller     OK   pid %d\n", st.PID)
	for _, id := range st.Identities {
		fmt.Printf("Identity %03d -> %s\n", id.Index, id.Route)
		for _, c := range id.Checks {
			fmt.Printf("  %-22s %s %s\n", c.Name, okStr(c.Passed), c.Detail)
		}
		if id.CurrentURL != "" {
			fmt.Printf("  %-22s %s %s\n", "startup URL", "OK  ", id.CurrentURL)
		}
	}
}

func routeCheck(ov config.Overrides) int {
	cfg, ok := loadConfig(ov)
	if !ok {
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var explicit []provider.RouteDef
	if ov.RoutesPath != "" {
		defs, err := config.LoadRoutes(cfg.RoutesPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitUsage
		}
		explicit = defs
	}
	var prov manager.Provisioner
	var name string
	var err error
	if len(explicit) == 0 {
		prov, err = prepareProvisioner(cfg, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitRoute
		}
		if prov != nil {
			name = prov.Name()
		}
	}
	if prov == nil {
		prov, name, err = manager.Resolve(ctx, cfg.DataDir, cfg.RoutesPath, explicit, cfg.Identities)
	}
	switch {
	case err == nil:
	case errors.Is(err, manager.ErrTryRoutes):
		defs, err := config.LoadRoutes(cfg.RoutesPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitUsage
		}
		prov, name = manager.Static{Defs: defs}, "static"
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	fmt.Printf("Network provider: %s\n", name)
	routes, err := prov.Provision(ctx, cfg.Identities)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitRoute
	}
	defer prov.Close()
	rc := exitOK
	for i, r := range routes {
		if err := r.Start(ctx); err != nil {
			fmt.Printf("Route %03d  %s  UNREACHABLE %v\n", i+1, r.Def().Redacted(), err)
			rc = exitRoute
			_ = r.Stop()
			continue
		}
		ip, err := provider.PublicIP(ctx, r.Dial, cfg.Verify.IPEchoURL, 20*time.Second)
		if err != nil {
			fmt.Printf("Route %03d  %s  NO EGRESS %v\n", i+1, r.Def().Redacted(), err)
			rc = exitRoute
			_ = r.Stop()
			continue
		}
		fmt.Printf("Route %03d  %s  %s\n", i+1, r.Def().Redacted(), ip)
		_ = r.Stop()
	}
	return rc
}

func prepareProvisioner(cfg config.Config, interactive bool) (manager.Provisioner, error) {
	if !cfg.DecodoSelected() {
		return nil, nil
	}
	base := manager.DecodoConfig{
		Host:           cfg.Routing.Host,
		Port:           cfg.Routing.Port,
		Country:        cfg.Routing.Country,
		SessionMinutes: cfg.Routing.SessionMinutes,
		StatePath:      filepath.Join(cfg.DataDir, "decodo-sessions.json"),
	}
	if os.Getenv("DECODO_USERNAME") != "" && os.Getenv("DECODO_PASSWORD") != "" {
		return manager.DecodoFromEnv(cfg.DataDir, base)
	}
	if strings.TrimSpace(cfg.Activation.Server) == "" {
		return nil, fmt.Errorf("decodo: set [activation] server, or DECODO_USERNAME and DECODO_PASSWORD for local development")
	}
	act, err := ensureActivation(cfg, interactive)
	if err != nil {
		return nil, err
	}
	base.Account = act.Proxy.Username
	base.Password = act.Proxy.Password
	if act.Proxy.Host != "" {
		base.Host = act.Proxy.Host
	}
	if act.Proxy.Port != 0 {
		base.Port = act.Proxy.Port
	}
	if base.Country == "" {
		base.Country = act.Proxy.Country
	}
	if base.SessionMinutes == 0 {
		base.SessionMinutes = act.Proxy.SessionMinutes
	}
	return manager.NewDecodo(base)
}

func ensureActivation(cfg config.Config, interactive bool) (*activation.Activation, error) {
	client := activation.Client{Server: cfg.Activation.Server}
	store := activation.Open(cfg.DataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if rec, err := store.Load(); err == nil {
		fmt.Println("Checking authorization")
		act, err := client.Validate(ctx, rec.InstallationID, rec.Token)
		if err == nil {
			if err := store.Save(activation.Record{
				Server: cfg.Activation.Server, InstallationID: act.InstallationID, Token: act.Token, ExpiresAt: act.ExpiresAt,
			}); err != nil {
				return nil, err
			}
			return act, nil
		}
		if errors.Is(err, activation.ErrUnavailable) {
			return nil, err
		}
	}
	if !interactive {
		return nil, fmt.Errorf("activation required")
	}
	fmt.Println("Activation required")
	fmt.Print("Enter your activation key: ")
	in := bufio.NewReader(os.Stdin)
	key, _ := in.ReadString('\n')
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("activation required")
	}
	fmt.Println("Activating...")
	fmt.Println("Checking authorization")
	act, err := client.Activate(ctx, key, "")
	if err != nil {
		return nil, err
	}
	if err := store.Save(activation.Record{
		Server: cfg.Activation.Server, InstallationID: act.InstallationID, Token: act.Token, ExpiresAt: act.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	return act, nil
}

func deactivate(ov config.Overrides) int {
	cfg, ok := loadConfig(ov)
	if !ok {
		return exitUsage
	}
	if strings.TrimSpace(cfg.Activation.Server) == "" {
		fmt.Fprintln(os.Stderr, "error: no activation server configured")
		return exitUsage
	}
	store := activation.Open(cfg.DataDir)
	rec, err := store.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := (activation.Client{Server: cfg.Activation.Server}).Deactivate(ctx, rec.InstallationID, rec.Token); err != nil && !errors.Is(err, activation.ErrInvalidKey) && !errors.Is(err, activation.ErrRevoked) {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitRoute
	}
	if err := store.Clear(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitRoute
	}
	fmt.Println("Activation removed from this installation.")
	return exitOK
}

func prompt() (int, string, bool) {
	in := bufio.NewReader(os.Stdin)
	fmt.Printf("TAB ROUTER %s\n", startup.Version)
	fmt.Print("Number of identities [2]: ")
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	n := 2
	if line != "" {
		v, err := strconv.Atoi(line)
		if err != nil || v < 1 {
			fmt.Fprintln(os.Stderr, "please enter a positive number")
			return 0, "", false
		}
		n = v
	}
	fmt.Print("Startup URL (blank for none): ")
	u, _ := in.ReadString('\n')
	u = strings.TrimSpace(u)
	if u != "" {
		if err := config.ValidateURL(u); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 0, "", false
		}
	}
	return n, u, true
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// ensureChromium downloads the pinned build into the data dir when no
// Chromium is already configured. Release binaries use this so the user
// does not need a source checkout.
func ensureChromium(cfg config.Config) error {
	pin := chromium.Pin()
	if found, err := browser.Find(cfg.DataDir, pin); err == nil && found.Source != "system" {
		return nil
	}
	fmt.Fprintf(os.Stderr, "Chromium not found; downloading official Chromium snapshot %s…\n", pin.Version)
	_, err := chromium.Install(filepath.Join(cfg.DataDir, "chromium"), "", os.Stderr)
	return err
}
