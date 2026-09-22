// Package isolation holds the network-isolation suite (T-A … T-I). Every
// test drives the real startup sequence headlessly against tests/infra.
package isolation_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/controller/config"
	"github.com/Nareik33L/tab-router/controller/identity"
	"github.com/Nareik33L/tab-router/controller/startup"
	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/tests/infra"
	"github.com/Nareik33L/tab-router/tests/testutil"
)

type harness struct {
	t     *testing.T
	in    *infra.Infra
	data  string
	defs  []provider.RouteDef
	out   *bytes.Buffer
	chrom string
}

func newHarness(t *testing.T, proxies ...string) *harness {
	t.Helper()
	chrom := testutil.ChromiumOrSkip(t)
	t.Setenv("TAB_ROUTER_CHROMIUM", chrom)
	if len(proxies) == 0 {
		proxies = []string{"socks5", "socks5"}
	}
	in, err := infra.Start(infra.Options{Proxies: proxies})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Close)
	h := &harness{t: t, in: in, data: t.TempDir(), out: &bytes.Buffer{}, chrom: chrom}
	for i, p := range in.Proxies {
		h.defs = append(h.defs, provider.RouteDef{ID: "route-" + pad(i+1), Type: p.Type, Address: p.Addr})
	}
	return h
}

func pad(i int) string { return fmt.Sprintf("%03d", i) }

func (h *harness) config(n int, url string, fresh bool) config.Config {
	cfg := config.Defaults()
	cfg.DataDir = h.data
	cfg.RoutesPath = filepath.Join(h.data, "routes.toml")
	cfg.Identities = n
	cfg.StartupURL = url
	cfg.Headless = true
	cfg.Fresh = fresh
	cfg.Verify.IPEchoURL = h.in.EchoURL()
	cfg.Verify.IPv6EchoURL = h.in.IPv6EchoURL()
	cfg.Verify.HostEchoURL = h.in.EchoURLDirect()
	cfg.Verify.TimeoutSeconds = 15
	cfg.Health.ProbeIntervalSeconds = 1
	cfg.Health.IPCheckIntervalSeconds = 2
	if err := cfg.Validate(); err != nil {
		h.t.Fatal(err)
	}
	return cfg
}

func (h *harness) run(ctx context.Context, cfg config.Config) (*startup.Session, error) {
	h.out.Reset()
	rep := startup.NewReporter(h.out, true)
	return startup.Run(ctx, cfg, rep, startup.Options{RouteDefs: h.defs, Unicode: true})
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// TestStartupPassesAllChecks covers T-A (IP separation), T-D (storage),
// T-F (startup URL), T-H (no direct connections) and the READY contract.
func TestStartupPassesAllChecks(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	sess, err := h.run(ctx, h.config(2, h.in.EchoURL()+"text", false))
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)
	out := h.out.String()
	for _, want := range []string{
		"Identity 001 → Public IP: 127.0.0.2 ✓",
		"Identity 002 → Public IP: 127.0.0.3 ✓",
		"Identity 001 ≠ Identity 002 ≠ host ✓",
		"Verifying DNS routing... ✓",
		"Verifying browser storage isolation... ✓",
		"Verifying fail-closed behaviour... ✓",
		"Verifying no direct connections... ✓",
		"TAB ROUTER READY",
		"Isolation verification: PASSED (9/9 checks per identity)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	st := sess.Status()
	if len(st.Identities) != 2 || st.Identities[0].EgressIP == st.Identities[1].EgressIP {
		t.Fatalf("expected two identities with distinct egress, got %+v", st.Identities)
	}
	if st.HostIPv4 == "" || st.HostIPv4 == st.Identities[0].EgressIP {
		t.Errorf("host IP comparison missing or equal: %q", st.HostIPv4)
	}
	// T-F: both identities fetched the startup URL, each from its own source.
	seen := map[string]bool{}
	for _, hit := range h.in.Hits() {
		if hit.Path == "/text" {
			seen[hit.ClientIP] = true
		}
	}
	if !seen["127.0.0.2"] || !seen["127.0.0.3"] {
		t.Errorf("startup URL not fetched from both routes: %v", seen)
	}
	// T-B (infra variant): the hostname was resolved by each upstream and
	// never by the host: every gate CONNECT was DOMAINNAME (V4 passed).
	for i, p := range h.in.Proxies {
		found := false
		for _, l := range p.Lookups() {
			if l == infra.EchoHost {
				found = true
			}
		}
		if !found {
			t.Errorf("proxy %d never resolved %s", i+1, infra.EchoHost)
		}
	}
}

// TestVerificationFailureAbortsSafely is T-I: a misconfigured route must
// produce the FAILED contract, ErrVerificationFailed/ErrRouteFailed and no
// surviving Chromium processes.
func TestVerificationFailureAbortsSafely(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	// Point route 002 at a closed port: route establishment fails.
	h.defs[1].Address = "127.0.0.1:1"
	_, err := h.run(ctx, h.config(2, "", false))
	if !errors.Is(err, startup.ErrRouteFailed) {
		t.Fatalf("expected ErrRouteFailed, got %v\n%s", err, h.out)
	}
	if !strings.Contains(h.out.String(), "No identity was started over the host network") {
		t.Errorf("missing safety message:\n%s", h.out)
	}

	// Now a route that works at the TCP level but shares egress with
	// route 001: V3 separation must fail and everything must be torn down.
	h.defs[1].Address = h.in.Proxies[0].Addr
	_, err = h.run(ctx, h.config(2, "", false))
	if !errors.Is(err, startup.ErrVerificationFailed) {
		t.Fatalf("expected ErrVerificationFailed, got %v\n%s", err, h.out)
	}
	out := h.out.String()
	for _, want := range []string{"share egress", "could not be verified as isolated", "TAB ROUTER FAILED TO START SAFELY"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "TAB ROUTER READY") {
		t.Errorf("READY must not be printed on failure")
	}
	assertNoChromium(t, h.data)
}

// TestRouteFailureBlocksAndRecovers is T-C at runtime: the health monitor
// must close the gate when the upstream dies and reopen it only after the
// egress IP is re-verified.
func TestRouteFailureBlocksAndRecovers(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	sess, err := h.run(ctx, h.config(2, "", false))
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)

	// Kill upstream 1 entirely (listener gone), keep upstream 2 alive.
	h.in.Proxies[0].Close()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Subjects()[0].Route.Status() == provider.StatusDown && !sess.Subjects()[0].Gate.IsOpen() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	sub := sess.Subjects()[0]
	if sub.Route.Status() != provider.StatusDown || sub.Gate.IsOpen() {
		t.Fatalf("route 001 not marked DOWN/gate closed after upstream loss: %s open=%v\n%s", sub.Route.Status(), sub.Gate.IsOpen(), h.out)
	}
	if err := sess.OpenURL(ctx, 1, h.in.EchoURL()+"text"); err == nil {
		t.Fatal("navigation succeeded while route DOWN")
	}
	if sess.Subjects()[1].Route.Status() != provider.StatusReady {
		t.Errorf("route 002 should be unaffected, is %s", sess.Subjects()[1].Route.Status())
	}
	if err := sess.OpenURL(ctx, 2, h.in.EchoURL()+"text"); err != nil {
		t.Errorf("identity 002 should still browse: %v", err)
	}
	if !strings.Contains(h.out.String(), "DOWN (traffic blocked)") {
		t.Errorf("terminal did not report DOWN:\n%s", h.out)
	}
}

// TestRouteRecoveryViaRefuse uses a refusing (but listening) upstream so the
// same proxy can come back, exercising the READY transition.
func TestRouteRecoveryViaRefuse(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	sess, err := h.run(ctx, h.config(2, "", false))
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)
	sub := sess.Subjects()[0]

	h.in.Proxies[0].SetRefuse(true)
	waitFor(t, 15*time.Second, func() bool { return sub.Route.Status() == provider.StatusDown })
	if sub.Gate.IsOpen() {
		t.Fatal("gate open while route DOWN")
	}
	h.in.Proxies[0].SetRefuse(false)
	waitFor(t, 15*time.Second, func() bool { return sub.Route.Status() == provider.StatusReady && sub.Gate.IsOpen() })
	if err := sess.OpenURL(ctx, 1, h.in.EchoURL()+"text"); err != nil {
		t.Fatalf("navigation after recovery failed: %v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, "READY: egress 127.0.0.2 verified") {
		t.Errorf("terminal did not report recovery:\n%s", out)
	}
}

// TestPersistenceAndFresh is T-E: identity state and environment survive a
// restart; --fresh creates a new set and leaves the old one intact.
func TestPersistenceAndFresh(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	sess, err := h.run(ctx, h.config(2, "", false))
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	sub := sess.Subjects()[0]
	env1 := sub.Identity.Environment
	set1 := identity.New(h.data)
	cur1, _ := set1.Current()
	// Write state in identity 001 (cookie survives restart).
	page, err := sub.Browser.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := page.Navigate(ctx, h.in.EchoURL()+"store"); err != nil || res.Blocked() {
		t.Fatalf("navigate: %v %+v", err, res)
	}
	if err := page.SetPersistentCookie(ctx, h.in.EchoURL(), "persist", "yes", 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := page.Evaluate(ctx, `localStorage.setItem("persist","yes")`, nil); err != nil {
		t.Fatal(err)
	}
	_ = page.Close(ctx)
	sess.Shutdown(ctx)

	sess2, err := h.run(ctx, h.config(2, "", false))
	if err != nil {
		t.Fatalf("restart failed: %v\n%s", err, h.out)
	}
	sub2 := sess2.Subjects()[0]
	if sub2.Identity.Environment != env1 {
		t.Errorf("environment changed across restart:\n%+v\n%+v", env1, sub2.Identity.Environment)
	}
	page2, _ := sub2.Browser.NewPage(ctx)
	if res, err := page2.Navigate(ctx, h.in.EchoURL()+"store"); err != nil || res.Blocked() {
		t.Fatalf("navigate: %v %+v", err, res)
	}
	cookies, _ := page2.GetCookies(ctx, h.in.EchoURL())
	found := false
	for _, c := range cookies {
		if c.Name == "persist" && c.Value == "yes" {
			found = true
		}
	}
	if !found {
		t.Errorf("cookie did not persist across restart: %+v", cookies)
	}
	var ls *string
	_ = page2.Evaluate(ctx, `localStorage.getItem("persist")`, &ls)
	if ls == nil || *ls != "yes" {
		t.Errorf("localStorage did not persist across restart")
	}
	// Identity 002 must not see identity 001's persisted cookie.
	other, _ := sess2.Subjects()[1].Browser.NewPage(ctx)
	oc, _ := other.GetCookies(ctx, h.in.EchoURL())
	for _, c := range oc {
		if c.Name == "persist" {
			t.Errorf("identity 002 can see identity 001's cookie")
		}
	}
	sess2.Shutdown(ctx)

	// --fresh
	sess3, err := h.run(ctx, h.config(2, "", true))
	if err != nil {
		t.Fatalf("fresh start failed: %v\n%s", err, h.out)
	}
	cur3, _ := set1.Current()
	if cur1.Name == cur3.Name {
		t.Errorf("--fresh did not create a new set")
	}
	if _, err := os.Stat(filepath.Join(cur1.Dir, "identity-001", "identity.json")); err != nil {
		t.Errorf("old set was removed: %v", err)
	}
	p3, _ := sess3.Subjects()[0].Browser.NewPage(ctx)
	c3, _ := p3.GetCookies(ctx, h.in.EchoURL())
	for _, c := range c3 {
		if c.Name == "persist" {
			t.Errorf("fresh identity inherited old cookie")
		}
	}
	sess3.Shutdown(ctx)
	sets, _ := set1.ListSets()
	if len(sets) != 2 {
		t.Errorf("expected 2 sets retained, got %v", sets)
	}
}

// TestHTTPProxyRoute proves the HTTP CONNECT provider end to end and mixed
// route types side by side.
func TestHTTPProxyRoute(t *testing.T) {
	h := newHarness(t, "socks5", "http")
	ctx := ctxT(t)
	sess, err := h.run(ctx, h.config(2, h.in.EchoURL(), false))
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)
	st := sess.Status()
	if st.Identities[1].RouteType != "http" || st.Identities[1].EgressIP != "127.0.0.3" {
		t.Errorf("http route not used: %+v", st.Identities[1])
	}
}

// TestAuthenticatedUpstreams checks credentials are handled by the gate and
// never appear in output.
func TestAuthenticatedUpstreams(t *testing.T) {
	chrom := testutil.ChromiumOrSkip(t)
	t.Setenv("TAB_ROUTER_CHROMIUM", chrom)
	in, err := infra.Start(infra.Options{Proxies: []string{"socks5", "http"}, Auth: true})
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	h := &harness{t: t, in: in, data: t.TempDir(), out: &bytes.Buffer{}}
	for i, p := range in.Proxies {
		h.defs = append(h.defs, provider.RouteDef{ID: "route-" + pad(i+1), Type: p.Type, Address: p.Addr, Username: p.Username, Password: p.Password})
	}
	ctx := ctxT(t)
	sess, err := h.run(ctx, h.config(2, "", false))
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)
	if strings.Contains(h.out.String(), "pass-1") || strings.Contains(h.out.String(), "pass-2") {
		t.Errorf("credential leaked into terminal output")
	}
	// Wrong password must fail closed at route establishment.
	h.defs[0].Password = "wrong"
	sess.Shutdown(ctx)
	_, err = h.run(ctx, h.config(2, "", false))
	if err == nil {
		t.Fatal("expected failure with wrong credentials")
	}
	if strings.Contains(h.out.String(), "wrong") {
		t.Errorf("credential leaked into terminal output:\n%s", h.out)
	}
}

// TestAutomaticProvisionWithoutRoutesFile is the product UX: identities + URL
// with no routes.toml. Tests inject RouteDefs (in-process infra) so CI does
// not need a live Decodo account. Production startup asks for Decodo credentials.
func TestAutomaticProvisionWithoutRoutesFile(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	cfg := h.config(2, "", false)
	if _, err := os.Stat(cfg.RoutesPath); !os.IsNotExist(err) {
		t.Fatalf("routes.toml should not exist: %v", err)
	}
	sess, err := h.run(ctx, cfg)
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)
	out := h.out.String()
	for _, want := range []string{
		"Network provider:",
		"Provisioning network routes...",
		"TAB ROUTER READY",
		"Identity 001 ≠ Identity 002",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

// TestRoutesTomlFallbackStillWorks keeps the power-user override: an existing
// routes.toml is used when no provider.toml is present and no RouteDefs are
// injected.
func TestRoutesTomlFallbackStillWorks(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	cfg := h.config(2, "", false)
	writeRoutesTOML(t, cfg.RoutesPath, h.defs)
	h.defs = nil
	sess, err := h.run(ctx, cfg)
	if err != nil {
		t.Fatalf("startup from routes.toml failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)
	if !strings.Contains(h.out.String(), "TAB ROUTER READY") {
		t.Errorf("not READY:\n%s", h.out)
	}
}

// TestFreshLeavesProviderConfig: --fresh rotates browser identities only.
func TestFreshLeavesProviderConfig(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	path := filepath.Join(h.data, "provider.toml")
	body := "type = \"mullvad\"\naccount = \"1234567890123456\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := h.run(ctx, h.config(2, "", true))
	if err != nil {
		t.Fatalf("fresh start failed: %v\n%s", err, h.out)
	}
	sess.Shutdown(ctx)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("--fresh removed provider.toml: %v", err)
	}
	if !strings.Contains(string(b), "1234567890123456") {
		t.Fatalf("provider.toml rewritten unexpectedly: %s", b)
	}
}

func writeRoutesTOML(t *testing.T, path string, defs []provider.RouteDef) {
	t.Helper()
	var b strings.Builder
	for _, d := range defs {
		fmt.Fprintf(&b, "[[route]]\nid = %q\ntype = %q\naddress = %q\n", d.ID, d.Type, d.Address)
		if d.Username != "" {
			fmt.Fprintf(&b, "username = %q\npassword = %q\n", d.Username, d.Password)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

// assertNoChromium fails if a browser that held a profile under dataDir is
// still alive, using Chromium's SingletonLock symlink ("host-pid").
func assertNoChromium(t *testing.T, dataDir string) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	matches, _ := filepath.Glob(filepath.Join(dataDir, "identities", "set-*", "identity-*", "profile", "SingletonLock"))
	for _, m := range matches {
		target, err := os.Readlink(m)
		if err != nil {
			continue
		}
		// SingletonLock is a symlink "host-pid"; if that pid is alive, a
		// browser survived.
		if i := strings.LastIndex(target, "-"); i >= 0 {
			var pid int
			for _, ch := range target[i+1:] {
				pid = pid*10 + int(ch-'0')
			}
			if pid > 0 && testutil.PIDAlive(pid) {
				t.Errorf("Chromium pid %d survived a failed start", pid)
			}
		}
	}
}
