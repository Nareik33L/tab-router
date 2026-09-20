// Package startup runs the ordered startup sequence, verifies isolation,
// reports to the terminal and owns the running session afterwards.
package startup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/controller/config"
	"github.com/Nareik33L/tab-router/controller/health"
	"github.com/Nareik33L/tab-router/controller/identity"
	"github.com/Nareik33L/tab-router/controller/verify"
	"github.com/Nareik33L/tab-router/routing/manager"
	"github.com/Nareik33L/tab-router/routing/provider"
)

// Version is the product version string. Release builds overwrite it via
// -ldflags "-X github.com/Nareik33L/tab-router/controller/startup.Version=vX.Y.Z".
var Version = "v0.1.0"

// Exit codes, matching docs/SCOPE.md §18.
var (
	ErrVerificationFailed = errors.New("verification failed")
	ErrRouteFailed        = errors.New("route establishment failed")
	ErrBrowserFailed      = errors.New("browser launch failed")
)

// Options are runtime inputs beyond the config file.
type Options struct {
	Pin *browser.PinManifest
	// Unicode enables ✓/→ glyphs in output.
	Unicode bool
	// ChromiumStderr receives Chromium's stderr (nil discards).
	ChromiumStderr *os.File
	// RouteDefs overrides loading routes.toml (tests / --routes).
	RouteDefs []provider.RouteDef
	// Provisioner, if set, is used instead of resolving one from disk.
	Provisioner manager.Provisioner
	// ChromiumExtraArgs are appended to every launch (tests only).
	ChromiumExtraArgs []string
}

// Session is a running set of identities.
type Session struct {
	Cfg       config.Config
	Report    *Reporter
	Started   time.Time
	Chromium  browser.Found
	ChromeVer string

	set      *identity.Set
	subjects []*verify.Subject
	main     map[*verify.Subject]*browser.Page
	monitor  *health.Monitor
	prov     manager.Provisioner
	hostIPv4 string
	hostIPv6 string

	mu       sync.Mutex
	finalURL map[*verify.Subject]string
	closed   bool
	done     chan struct{}
}

// Run executes the startup sequence. On any verification failure every
// browser is terminated and ErrVerificationFailed is returned: no identity
// is ever left running unverified.
func Run(ctx context.Context, cfg config.Config, rep *Reporter, opts Options) (*Session, error) {
	s := &Session{Cfg: cfg, Report: rep, Started: time.Now(), main: map[*verify.Subject]*browser.Page{}, finalURL: map[*verify.Subject]string{}, done: make(chan struct{})}
	rep.Header(Version, cfg.Identities, cfg.StartupURL)

	// Identities first (persistent); routes are provisioned fresh each run.
	mgr := identity.New(cfg.DataDir)
	set, err := mgr.Open(cfg.Fresh)
	if err != nil {
		return nil, err
	}
	if err := set.Lock(); err != nil {
		return nil, err
	}
	s.set = set
	ids, err := set.Ensure(cfg.Identities, func(i int) identity.Environment {
		env, err := cfg.EnvironmentFor(i)
		if err != nil {
			panic(err) // validated in config.Load
		}
		return env
	})
	if err != nil {
		set.Unlock()
		return nil, err
	}
	if cfg.Fresh {
		rep.Line("Created fresh identity set %s", set.Name)
	} else {
		rep.Line("Using identity set %s", set.Name)
	}
	for _, id := range ids {
		if id.JustCreated {
			rep.Line("Creating %s...", id.Label())
		}
	}

	prov := opts.Provisioner
	if prov == nil {
		p, _, err := manager.Resolve(ctx, cfg.DataDir, cfg.RoutesPath, opts.RouteDefs, cfg.Identities)
		switch {
		case err == nil:
			prov = p
		case errors.Is(err, manager.ErrTryRoutes):
			defs, err := config.LoadRoutes(cfg.RoutesPath)
			if err != nil {
				set.Unlock()
				return nil, err
			}
			prov = manager.Static{Defs: defs}
		default:
			set.Unlock()
			return nil, err
		}
	}
	s.prov = prov

	// Chromium.
	found, err := browser.Find(cfg.DataDir, opts.Pin)
	if err != nil {
		set.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrBrowserFailed, err)
	}
	s.Chromium = found
	if v, err := browser.Version(found.Path); err == nil {
		s.ChromeVer = v
	}
	rep.Line("Browser: %s (%s)", s.ChromeVer, found.Source)

	rep.Line("Network provider: %s", prov.Name())
	rep.Line("Provisioning network routes...")
	routes, err := prov.Provision(ctx, cfg.Identities)
	if err != nil {
		s.abort(ctx)
		return nil, fmt.Errorf("%w: %v", ErrRouteFailed, err)
	}
	rep.Line("Starting %d identities...", cfg.Identities)
	for _, id := range ids {
		if id.RouteSlot-1 >= len(routes) {
			s.abort(ctx)
			return nil, fmt.Errorf("%w: provisioner returned %d routes, need slot %d", ErrRouteFailed, len(routes), id.RouteSlot)
		}
		route := routes[id.RouteSlot-1]
		gate := provider.NewGate(route)
		if _, err := gate.Listen(); err != nil {
			s.abort(ctx)
			return nil, fmt.Errorf("%w: %v", ErrRouteFailed, err)
		}
		s.subjects = append(s.subjects, &verify.Subject{Identity: id, Route: route, Gate: gate})
	}

	vopts := verify.Options{
		EchoURL:       cfg.Verify.IPEchoURL,
		IPv6EchoURL:   cfg.Verify.IPv6EchoURL,
		StorageOrigin: cfg.Verify.StorageOrigin,
		CompareHostIP: cfg.Verify.CompareHostIP,
		Timeout:       time.Duration(cfg.Verify.TimeoutSeconds) * time.Second,
	}
	if !cfg.IPv6 {
		vopts.IPv6EchoURL = ""
	}
	if cfg.Verify.CompareHostIP {
		v6URL := cfg.Verify.HostEchoURLv6
		if !cfg.IPv6 {
			v6URL = ""
		}
		v4, v6 := verify.HostIPs(ctx, cfg.Verify.HostEchoURL, v6URL, vopts.Timeout)
		if v4 != nil {
			s.hostIPv4 = v4.String()
		}
		if v6 != nil {
			s.hostIPv6 = v6.String()
		}
		vopts.HostIPv4, vopts.HostIPv6 = v4, v6
	}

	routeErr := s.parallel(func(sub *verify.Subject) error {
		if err := sub.Route.Start(ctx); err != nil {
			rep.Result(sub.Label(), routeLabel(sub), "FAILED ("+err.Error()+")", false)
			return err
		}
		c := verify.V1RouteUp(ctx, sub, vopts)
		if !c.Passed {
			rep.Result(sub.Label(), routeLabel(sub), "FAILED ("+c.Detail+")", false)
			return errors.New(c.Detail)
		}
		sub.Route.SetStatus(provider.StatusReady)
		rep.Result(sub.Label(), routeLabel(sub), "CONNECTED", true)
		return nil
	})
	if routeErr != nil {
		rep.Line("ERROR: a route could not be established. No identity was started over the host network.")
		s.abort(ctx)
		return nil, fmt.Errorf("%w: %v", ErrRouteFailed, routeErr)
	}

	// 8: browsers.
	launchErr := s.parallel(func(sub *verify.Subject) error {
		sub.Gate.Open()
		b, err := browser.Launch(ctx, browser.LaunchOptions{
			Binary:      found.Path,
			ProfileDir:  sub.Identity.Profile,
			GateAddr:    sub.Gate.Addr(),
			Headless:    cfg.Headless,
			Environment: sub.Identity.Environment,
			DownloadDir: sub.Identity.DownloadPath(),
			Stderr:      opts.ChromiumStderr,
			ExtraArgs:   opts.ChromiumExtraArgs,
		})
		if err != nil {
			sub.Gate.Close()
			return err
		}
		sub.Browser = b
		page, err := b.FirstPage(ctx)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.main[sub] = page
		s.mu.Unlock()
		return verify.Begin(ctx, sub)
	})
	if launchErr != nil {
		rep.Line("ERROR: browser launch failed: %v", launchErr)
		s.abort(ctx)
		return nil, fmt.Errorf("%w: %v", ErrBrowserFailed, launchErr)
	}

	// 9: verification.
	passed := s.verifyAll(ctx, vopts)
	for _, sub := range s.subjects {
		verify.Finish(ctx, sub)
	}
	if !passed {
		s.failAndAbort(ctx)
		return nil, ErrVerificationFailed
	}

	// 11: startup URL (V9).
	if cfg.StartupURL != "" {
		rep.Line("Opening startup URL...")
		urlOK := true
		var detailMu sync.Mutex
		_ = s.parallel(func(sub *verify.Subject) error {
			c, res := verify.V9StartupURL(ctx, sub, s.main[sub], cfg.StartupURL, vopts)
			detailMu.Lock()
			if res.FinalURL != "" {
				s.finalURL[sub] = res.FinalURL
			}
			if !c.Passed {
				urlOK = false
			}
			detailMu.Unlock()
			if c.Passed {
				rep.Result(sub.Label(), cfg.StartupURL, "", true)
			} else {
				rep.Result(sub.Label(), cfg.StartupURL, "FAILED ("+c.Detail+")", false)
			}
			return nil
		})
		if !urlOK {
			s.failAndAbort(ctx)
			return nil, ErrVerificationFailed
		}
	}

	// 12: ready.
	rep.Banner("TAB ROUTER READY")
	for _, sub := range s.subjects {
		rep.Line("%s %s %s %s READY   %s", sub.Label(), rep.Arrow(), routeLabel(sub), rep.Arrow(), sub.BrowserIP)
	}
	n := uniqueChecks(s.subjects[0].Results)
	rep.Line("Isolation verification: PASSED (%d/%d checks per identity)", n, n)

	s.monitor = health.New(s.subjects, health.Options{
		ProbeInterval:   time.Duration(cfg.Health.ProbeIntervalSeconds) * time.Second,
		IPCheckInterval: time.Duration(cfg.Health.IPCheckIntervalSeconds) * time.Second,
		EchoURL:         vopts.EchoURL,
		Timeout:         vopts.Timeout,
		OnEvent:         s.onHealthEvent,
		Reestablish:     s.reestablish,
	})
	s.monitor.Start(context.Background())
	return s, nil
}

func routeLabel(sub *verify.Subject) string {
	return fmt.Sprintf("Route %03d", sub.Identity.RouteSlot)
}

// parallel runs fn for every subject and returns the first error.
func (s *Session) parallel(fn func(*verify.Subject) error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(s.subjects))
	for i, sub := range s.subjects {
		wg.Add(1)
		go func(i int, sub *verify.Subject) {
			defer wg.Done()
			errs[i] = fn(sub)
		}(i, sub)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) verifyAll(ctx context.Context, o verify.Options) bool {
	rep := s.Report
	allOK := true
	phase := func(name string, run func(sub *verify.Subject) verify.Check) {
		results := make([]verify.Check, len(s.subjects))
		var wg sync.WaitGroup
		for i, sub := range s.subjects {
			wg.Add(1)
			go func(i int, sub *verify.Subject) {
				defer wg.Done()
				results[i] = run(sub)
			}(i, sub)
		}
		wg.Wait()
		ok := true
		var details []string
		for i, c := range results {
			if !c.Passed {
				ok = false
				details = append(details, fmt.Sprintf("%s: %s", s.subjects[i].Label(), c.Detail))
			} else if c.Detail != "" {
				details = append(details, fmt.Sprintf("%03d: %s", s.subjects[i].Identity.Index, c.Detail))
			}
		}
		if !ok {
			allOK = false
		}
		rep.Phase(name, ok, details)
	}

	rep.Line("Verifying network isolation...")
	{
		results := make([]verify.Check, len(s.subjects))
		var wg sync.WaitGroup
		for i, sub := range s.subjects {
			wg.Add(1)
			go func(i int, sub *verify.Subject) {
				defer wg.Done()
				results[i] = verify.V2BrowserEgress(ctx, sub, o)
			}(i, sub)
		}
		wg.Wait()
		for i, c := range results {
			if c.Passed {
				rep.Result(s.subjects[i].Label(), "Public IP", c.Detail, true)
			} else {
				allOK = false
				rep.Result(s.subjects[i].Label(), "Public IP", "FAILED (V2: "+c.Detail+")", false)
			}
		}
		sep := verify.V3Separation(s.subjects, o)
		if !sep {
			allOK = false
		}
		labels := make([]string, 0, len(s.subjects))
		for _, sub := range s.subjects {
			labels = append(labels, sub.Label())
		}
		what := strings.Join(labels, " ≠ ")
		if !s.Report.uni {
			what = strings.Join(labels, " != ")
		}
		if o.CompareHostIP && o.HostIPv4 != nil {
			what += " ≠ host"
			if !s.Report.uni {
				what = strings.TrimSuffix(what, " ≠ host") + " != host"
			}
		}
		last := s.subjects[0].Results[len(s.subjects[0].Results)-1]
		if sep {
			rep.Line("%s %s", what, rep.ok())
		} else {
			rep.Line("%s %s (%s)", what, rep.bad(), last.Detail)
		}
	}
	phase("Verifying DNS routing...", func(sub *verify.Subject) verify.Check { return verify.V4DNSDelegation(sub, o) })
	phase("Verifying IPv6 behaviour...", func(sub *verify.Subject) verify.Check { return verify.V5IPv6(ctx, sub, o) })
	{
		// Storage: ring i -> i+1 so every identity is both writer and reader.
		ok := true
		var details []string
		if len(s.subjects) == 1 {
			details = append(details, "single identity: cross-identity check not applicable")
		}
		for i := range s.subjects {
			if len(s.subjects) == 1 {
				break
			}
			a := s.subjects[i]
			b := s.subjects[(i+1)%len(s.subjects)]
			ca, _ := verify.V6StorageIsolation(ctx, a, b, o)
			if !ca.Passed {
				ok = false
				details = append(details, ca.Detail)
			}
		}
		if !ok {
			allOK = false
		}
		rep.Phase("Verifying browser storage isolation...", ok, details)
	}
	phase("Verifying fail-closed behaviour...", func(sub *verify.Subject) verify.Check { return verify.V7FailClosed(ctx, sub, o) })
	phase("Verifying no direct connections...", func(sub *verify.Subject) verify.Check { return verify.V8NoDirect(sub, o) })
	return allOK
}

// failAndAbort prints the failure contract and tears everything down.
func (s *Session) failAndAbort(ctx context.Context) {
	rep := s.Report
	for _, sub := range s.subjects {
		var failed []string
		seen := map[string]bool{}
		for _, c := range sub.Results {
			if !c.Passed && !seen[c.ID] {
				seen[c.ID] = true
				failed = append(failed, c.ID+" "+c.Name)
			}
		}
		if len(failed) > 0 {
			rep.Result(sub.Label(), "Network verification", "FAILED ("+strings.Join(failed, ", ")+")", false)
			rep.Line("ERROR: %s could not be verified as isolated.", sub.Label())
		}
	}
	rep.Line("The identities will NOT use the host/default network.")
	rep.Line("Shutting down all identities...")
	s.abort(ctx)
	rep.Banner("TAB ROUTER FAILED TO START SAFELY")
}

// abort tears down everything created so far without ceremony.
func (s *Session) abort(ctx context.Context) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	if s.monitor != nil {
		s.monitor.Stop()
	}
	var wg sync.WaitGroup
	for _, sub := range s.subjects {
		sub.Gate.Close()
		if sub.Browser != nil {
			wg.Add(1)
			go func(b *browser.Browser) {
				defer wg.Done()
				_ = b.Kill()
			}(sub.Browser)
		}
	}
	wg.Wait()
	for _, sub := range s.subjects {
		_ = sub.Gate.Shutdown()
		_ = sub.Route.Stop()
	}
	if s.prov != nil {
		_ = s.prov.Close()
	}
	if s.set != nil {
		s.set.Unlock()
	}
	close(s.done)
}

// Shutdown closes browsers gracefully, then everything else.
func (s *Session) Shutdown(ctx context.Context) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if s.monitor != nil {
		s.monitor.Stop()
	}
	var wg sync.WaitGroup
	for _, sub := range s.subjects {
		if sub.Browser != nil {
			wg.Add(1)
			go func(b *browser.Browser) {
				defer wg.Done()
				_ = b.Close(ctx)
			}(sub.Browser)
		}
	}
	wg.Wait()
	s.abort(ctx)
}

// Done is closed once the session has been torn down.
func (s *Session) Done() <-chan struct{} { return s.done }

// Subjects exposes the verified identities.
func (s *Session) Subjects() []*verify.Subject { return s.subjects }

// AllBrowsersExited reports whether the user has closed every window.
func (s *Session) AllBrowsersExited() bool {
	for _, sub := range s.subjects {
		if sub.Browser != nil && sub.Browser.Alive() {
			return false
		}
	}
	return true
}

// reestablish replaces a failed route with a new independent path for the
// same identity. The gate stays closed until the health monitor re-verifies
// egress; the replacement must not share another identity's public IP.
func (s *Session) reestablish(ctx context.Context, sub *verify.Subject) error {
	if s.prov == nil {
		return fmt.Errorf("no provisioner")
	}
	sub.Gate.Close()
	old := sub.Route
	next, err := s.prov.Reestablish(ctx, sub.Identity.RouteSlot, old)
	if err != nil {
		return err
	}
	if err := next.Start(ctx); err != nil {
		_ = next.Stop()
		return err
	}
	timeout := time.Duration(s.Cfg.Verify.TimeoutSeconds) * time.Second
	ip, err := provider.PublicIP(ctx, next.Dial, s.Cfg.Verify.IPEchoURL, timeout)
	if err != nil {
		_ = next.Stop()
		return err
	}
	for _, other := range s.subjects {
		if other == sub {
			continue
		}
		if other.RouteIP != nil && ip.Equal(other.RouteIP) {
			_ = next.Stop()
			return fmt.Errorf("replacement shares egress %s with %s", ip, other.Label())
		}
	}
	sub.Gate.SetRoute(next)
	sub.Route = next
	sub.RouteIP = ip
	sub.BrowserIP = ip
	next.SetStatus(provider.StatusVerifying)
	_ = old.Stop()
	return nil
}

func (s *Session) onHealthEvent(ev health.Event) {
	rep := s.Report
	label := ev.Subject.Label()
	switch ev.Kind {
	case health.RouteDown:
		rep.Line("%s %s %s %s DOWN (traffic blocked): %s", label, rep.Arrow(), routeLabel(ev.Subject), rep.Arrow(), ev.Detail)
	case health.RouteRestored:
		rep.Line("%s %s %s %s READY: %s", label, rep.Arrow(), routeLabel(ev.Subject), rep.Arrow(), ev.Detail)
	case health.EgressChanged:
		rep.Line("%s %s %s: EGRESS CHANGED %s (blocked until re-verified)", label, rep.Arrow(), routeLabel(ev.Subject), ev.Detail)
	case health.Leak:
		rep.Line("ALARM %s: non-gate network endpoint observed: %s", label, ev.Detail)
	case health.BrowserExited:
		rep.Line("%s: browser closed; %s", label, ev.Detail)
	}
}

// OpenURL navigates an identity's main tab (IPC identity.open_url).
func (s *Session) OpenURL(ctx context.Context, index int, rawURL string) error {
	if err := config.ValidateURL(rawURL); err != nil {
		return err
	}
	for _, sub := range s.subjects {
		if sub.Identity.Index != index {
			continue
		}
		if sub.Route.Status() != provider.StatusReady {
			return fmt.Errorf("%s route is %s; refusing to navigate", sub.Label(), sub.Route.Status())
		}
		page := s.main[sub]
		res, err := page.Navigate(ctx, rawURL)
		if err != nil {
			return err
		}
		if res.Blocked() {
			return errors.New(res.ErrorText)
		}
		s.mu.Lock()
		s.finalURL[sub] = res.FinalURL
		s.mu.Unlock()
		return nil
	}
	return fmt.Errorf("no identity %03d", index)
}

// SetRouteEnabled stops or restarts forwarding for one identity (IPC
// route.stop / route.start). Starting re-verifies the egress first.
func (s *Session) SetRouteEnabled(ctx context.Context, index int, enabled bool) error {
	for _, sub := range s.subjects {
		if sub.Identity.RouteSlot != index {
			continue
		}
		if !enabled {
			sub.Route.SetStatus(provider.StatusDown)
			sub.Gate.Close()
			return nil
		}
		sub.Route.SetStatus(provider.StatusVerifying)
		ip, err := provider.PublicIP(ctx, sub.Route.Dial, s.Cfg.Verify.IPEchoURL, time.Duration(s.Cfg.Verify.TimeoutSeconds)*time.Second)
		if err != nil {
			sub.Route.SetStatus(provider.StatusDown)
			return err
		}
		if sub.RouteIP != nil && !ip.Equal(sub.RouteIP) {
			sub.Route.SetStatus(provider.StatusDown)
			return fmt.Errorf("egress changed %s -> %s; not reopening", sub.RouteIP, ip)
		}
		sub.Route.SetStatus(provider.StatusReady)
		sub.Gate.Open()
		return nil
	}
	return fmt.Errorf("no route %03d", index)
}

// ProfileRelPath returns the profile path relative to the data dir, for
// status output that should not dump absolute home paths.
func (s *Session) ProfileRelPath(sub *verify.Subject) string {
	rel, err := filepath.Rel(s.Cfg.DataDir, sub.Identity.Profile)
	if err != nil {
		return sub.Identity.Profile
	}
	return rel
}
