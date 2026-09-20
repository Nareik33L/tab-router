// Package verify implements the startup isolation checks V1–V9. Starting
// Chromium is not success; these checks are what allow an identity to be
// reported READY.
package verify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/controller/identity"
	"github.com/Nareik33L/tab-router/routing/platform"
	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/routing/socks5"
)

// Check is the outcome of one verification step.
type Check struct {
	ID       string // "V1".."V9"
	Name     string
	Passed   bool
	Detail   string
	Duration time.Duration
}

func (c Check) String() string {
	mark := "FAILED"
	if c.Passed {
		mark = "OK"
	}
	if c.Detail != "" {
		return fmt.Sprintf("%s %s: %s (%s)", c.ID, mark, c.Name, c.Detail)
	}
	return fmt.Sprintf("%s %s: %s", c.ID, mark, c.Name)
}

// Subject is one identity under verification.
type Subject struct {
	Identity identity.Identity
	Route    provider.Route
	Gate     *provider.Gate
	Browser  *browser.Browser

	// Filled in by the checks.
	RouteIP   net.IP
	BrowserIP net.IP
	Page      *browser.Page // verification tab; closed by Finish
	Results   []Check

	eventCursor int
	sampler     *sampler
}

// Label is "Identity 001".
func (s *Subject) Label() string { return s.Identity.Label() }

// Failed reports whether any check failed.
func (s *Subject) Failed() bool {
	for _, c := range s.Results {
		if !c.Passed {
			return true
		}
	}
	return false
}

func (s *Subject) add(c Check) Check {
	s.Results = append(s.Results, c)
	return c
}

// Options configures the checks.
type Options struct {
	EchoURL       string
	IPv6EchoURL   string
	StorageOrigin string
	CompareHostIP bool
	Timeout       time.Duration
	// HostIPv4/HostIPv6 are the host's own addresses (nil if unknown).
	HostIPv4 net.IP
	HostIPv6 net.IP
	// OnCheck is invoked as each check completes (for live terminal output).
	OnCheck func(s *Subject, c Check)
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 20 * time.Second
	}
	return o.Timeout
}

func (o Options) storageOrigin() string {
	if o.StorageOrigin != "" {
		return o.StorageOrigin
	}
	u, err := url.Parse(o.EchoURL)
	if err != nil {
		return o.EchoURL
	}
	return u.Scheme + "://" + u.Host + "/"
}

func (o Options) report(s *Subject, c Check) Check {
	s.add(c)
	if o.OnCheck != nil {
		o.OnCheck(s, c)
	}
	return c
}

// HostIPs fetches the host's own IPv4 and IPv6 as seen by the echo services,
// over the host's default connection. This is the only direct request the
// controller ever makes and exists solely for the "≠ host" comparison.
func HostIPs(ctx context.Context, echoURL, ipv6EchoURL string, timeout time.Duration) (v4, v6 net.IP) {
	dial4 := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return provider.DirectDial(ctx, "tcp4", addr)
	}
	if ip, err := provider.PublicIP(ctx, dial4, echoURL, timeout); err == nil {
		v4 = ip
	}
	if ipv6EchoURL != "" {
		dial6 := func(ctx context.Context, network, addr string) (net.Conn, error) {
			return provider.DirectDial(ctx, "tcp6", addr)
		}
		if ip, err := provider.PublicIP(ctx, dial6, ipv6EchoURL, timeout); err == nil {
			v6 = ip
		}
	}
	return v4, v6
}

// V1RouteUp fetches the public IP through the route from the controller.
func V1RouteUp(ctx context.Context, s *Subject, o Options) Check {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	ip, err := provider.PublicIP(ctx, s.Route.Dial, o.EchoURL, o.timeout())
	c := Check{ID: "V1", Name: "route up", Duration: time.Since(start)}
	if err != nil {
		c.Detail = err.Error()
		return o.report(s, c)
	}
	s.RouteIP = ip
	c.Passed, c.Detail = true, ip.String()
	return o.report(s, c)
}

// Begin opens the verification tab and starts endpoint sampling. Call after
// the browser is launched and before browser-side checks.
func Begin(ctx context.Context, s *Subject) error {
	s.eventCursor = s.Gate.EventCount()
	p, err := s.Browser.NewPage(ctx)
	if err != nil {
		return err
	}
	s.Page = p
	s.sampler = startSampler(s.Browser, s.Gate.Addr())
	return nil
}

// Finish stops sampling and closes the verification tab.
func Finish(ctx context.Context, s *Subject) {
	if s.sampler != nil {
		s.sampler.stop()
	}
	if s.Page != nil {
		_ = s.Page.Close(ctx)
		s.Page = nil
	}
}

func cacheBust(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("_tr", fmt.Sprintf("%d", time.Now().UnixNano()))
	u.RawQuery = q.Encode()
	return u.String()
}

// V2BrowserEgress navigates the verification tab to the echo URL and
// compares the reported IP with V1.
func V2BrowserEgress(ctx context.Context, s *Subject, o Options) Check {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	c := Check{ID: "V2", Name: "browser egress"}
	defer func() { c.Duration = time.Since(start) }()
	res, err := s.Page.Navigate(ctx, cacheBust(o.EchoURL))
	if err != nil {
		c.Detail = err.Error()
		return o.report(s, c)
	}
	if res.Blocked() {
		c.Detail = "navigation failed: " + res.ErrorText
		return o.report(s, c)
	}
	body, err := s.Page.BodyText(ctx)
	if err != nil {
		c.Detail = err.Error()
		return o.report(s, c)
	}
	ip, err := provider.ParseEchoBody([]byte(body))
	if err != nil {
		c.Detail = fmt.Sprintf("%v (body %q)", err, truncate(body, 80))
		return o.report(s, c)
	}
	s.BrowserIP = ip
	if s.RouteIP != nil && !ip.Equal(s.RouteIP) {
		c.Detail = fmt.Sprintf("browser egress %s != route %s", ip, s.RouteIP)
		return o.report(s, c)
	}
	c.Passed, c.Detail = true, ip.String()
	return o.report(s, c)
}

// V3Separation checks all browser IPs are pairwise distinct and differ from
// the host. It records the check on every subject.
func V3Separation(subjects []*Subject, o Options) bool {
	start := time.Now()
	ok := true
	var problems []string
	for i, a := range subjects {
		if a.BrowserIP == nil {
			problems = append(problems, a.Label()+" has no verified IP")
			ok = false
			continue
		}
		if o.CompareHostIP && o.HostIPv4 != nil && a.BrowserIP.Equal(o.HostIPv4) {
			problems = append(problems, fmt.Sprintf("%s egress %s equals host IP", a.Label(), a.BrowserIP))
			ok = false
		}
		for _, b := range subjects[i+1:] {
			if b.BrowserIP != nil && a.BrowserIP.Equal(b.BrowserIP) {
				problems = append(problems, fmt.Sprintf("%s and %s share egress %s", a.Label(), b.Label(), a.BrowserIP))
				ok = false
			}
		}
	}
	detail := strings.Join(problems, "; ")
	if ok {
		parts := make([]string, 0, len(subjects))
		for _, s := range subjects {
			parts = append(parts, s.BrowserIP.String())
		}
		detail = strings.Join(parts, " ≠ ")
		if o.CompareHostIP && o.HostIPv4 != nil {
			detail += " ≠ host " + o.HostIPv4.String()
		}
	}
	for _, s := range subjects {
		o.report(s, Check{ID: "V3", Name: "separation", Passed: ok, Detail: detail, Duration: time.Since(start)})
	}
	return ok
}

// V4DNSDelegation inspects gate events since Begin: the echo hostname must
// have arrived as DOMAINNAME and no hostname navigation may have arrived as
// an IP literal.
func V4DNSDelegation(s *Subject, o Options) Check {
	start := time.Now()
	c := Check{ID: "V4", Name: "DNS delegation", Duration: 0}
	echoHost := hostOf(o.EchoURL)
	events := s.Gate.EventsSince(s.eventCursor)
	sawEcho := false
	var literal []string
	for _, ev := range events {
		if ev.AddrType == socks5.AddrType(socks5.ATypDomain) {
			if strings.EqualFold(ev.Host, echoHost) {
				sawEcho = true
			}
			continue
		}
		literal = append(literal, fmt.Sprintf("%s:%d", ev.Host, ev.Port))
	}
	reused := false
	if !sawEcho {
		// A keep-alive connection from earlier in the session may have been
		// reused; the original CONNECT must then be on record as DOMAINNAME.
		for _, ev := range s.Gate.Events() {
			if strings.EqualFold(ev.Host, echoHost) && ev.AddrType == socks5.AddrType(socks5.ATypDomain) && ev.Reply == socks5.RepSuccess {
				reused = true
				break
			}
		}
	}
	c.Duration = time.Since(start)
	switch {
	case !sawEcho && !reused:
		c.Detail = fmt.Sprintf("gate never saw a CONNECT for %s (%d events)", echoHost, len(events))
	case len(literal) > 0:
		c.Detail = "IP-literal CONNECTs indicate local resolution: " + strings.Join(dedupe(literal), ", ")
	case reused:
		c.Passed = true
		c.Detail = "hostname delegated to route (connection reused), 0 local lookups"
	default:
		c.Passed = true
		c.Detail = fmt.Sprintf("%d hostnames delegated to route, 0 local lookups", countDomain(events))
	}
	return o.report(s, c)
}

// V5IPv6 navigates to the IPv6 echo. Acceptable outcomes: blocked, or an
// IPv6 address that is not the host's. The host's IPv6 is never acceptable.
func V5IPv6(ctx context.Context, s *Subject, o Options) Check {
	start := time.Now()
	c := Check{ID: "V5", Name: "IPv6 behaviour"}
	defer func() { c.Duration = time.Since(start) }()
	if o.IPv6EchoURL == "" {
		c.Passed, c.Detail = true, "no IPv6 echo configured; skipped"
		return o.report(s, c)
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	res, err := s.Page.Navigate(ctx, cacheBust(o.IPv6EchoURL))
	if err != nil {
		// Timeouts count as blocked: nothing reached the echo.
		c.Passed, c.Detail = true, "blocked (no response)"
		return o.report(s, c)
	}
	if res.Blocked() {
		c.Passed, c.Detail = true, "blocked ("+res.ErrorText+")"
		return o.report(s, c)
	}
	body, _ := s.Page.BodyText(ctx)
	ip, err := provider.ParseEchoBody([]byte(body))
	if err != nil {
		c.Passed, c.Detail = true, "blocked (no address returned)"
		return o.report(s, c)
	}
	if o.HostIPv6 != nil && ip.Equal(o.HostIPv6) {
		c.Detail = "browser used the host's IPv6 address " + ip.String()
		return o.report(s, c)
	}
	if ip.To4() != nil {
		c.Passed, c.Detail = true, "route has no IPv6 egress (echo answered over IPv4 "+ip.String()+")"
		return o.report(s, c)
	}
	c.Passed, c.Detail = true, "route IPv6 "+ip.String()
	return o.report(s, c)
}

// V6StorageIsolation writes a cookie and a localStorage value in a and
// asserts b cannot see them, then cleans up. Profiles must be distinct paths.
func V6StorageIsolation(ctx context.Context, a, b *Subject, o Options) (Check, Check) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	origin := o.storageOrigin()
	name := fmt.Sprintf("tr_probe_%d", time.Now().UnixNano())
	fail := func(detail string) (Check, Check) {
		c := Check{ID: "V6", Name: "storage isolation", Detail: detail, Duration: time.Since(start)}
		return o.report(a, c), o.report(b, c)
	}
	ra, _ := filepath.EvalSymlinks(a.Identity.Profile)
	rb, _ := filepath.EvalSymlinks(b.Identity.Profile)
	if ra == "" || ra == rb {
		return fail("profile directories are not distinct")
	}
	// Both pages must be on the storage origin (a previous check may have
	// left them on an error page, where storage APIs are denied).
	for _, s := range []*Subject{a, b} {
		res, err := s.Page.Navigate(ctx, cacheBust(origin))
		if err != nil {
			return fail("navigate " + s.Label() + " to storage origin: " + err.Error())
		}
		if res.Blocked() {
			return fail("navigate " + s.Label() + " to storage origin: " + res.ErrorText)
		}
	}
	if err := a.Page.SetCookie(ctx, origin, name, "1"); err != nil {
		return fail("set cookie in " + a.Label() + ": " + err.Error())
	}
	defer a.Page.DeleteCookie(context.Background(), origin, name)
	// localStorage requires the page to be on the origin; V2 left it there.
	if err := a.Page.Evaluate(ctx, fmt.Sprintf(`localStorage.setItem(%q, "1")`, name), nil); err != nil {
		return fail("localStorage in " + a.Label() + ": " + err.Error())
	}
	defer a.Page.Evaluate(context.Background(), fmt.Sprintf(`localStorage.removeItem(%q)`, name), nil)

	cookies, err := b.Page.GetCookies(ctx, origin)
	if err != nil {
		return fail("read cookies in " + b.Label() + ": " + err.Error())
	}
	for _, ck := range cookies {
		if ck.Name == name {
			return fail(fmt.Sprintf("cookie written in %s is visible in %s", a.Label(), b.Label()))
		}
	}
	var seen *string
	if err := b.Page.Evaluate(ctx, fmt.Sprintf(`localStorage.getItem(%q)`, name), &seen); err != nil {
		return fail("localStorage in " + b.Label() + ": " + err.Error())
	}
	if seen != nil {
		return fail(fmt.Sprintf("localStorage written in %s is visible in %s", a.Label(), b.Label()))
	}
	// Sanity: a itself must see its own values, or the test proved nothing.
	var own *string
	_ = a.Page.Evaluate(ctx, fmt.Sprintf(`localStorage.getItem(%q)`, name), &own)
	if own == nil {
		return fail(a.Label() + " could not read back its own storage")
	}
	c := Check{ID: "V6", Name: "storage isolation", Passed: true,
		Detail: fmt.Sprintf("%s state invisible to %s; distinct profiles", a.Label(), b.Label()), Duration: time.Since(start)}
	return o.report(a, c), o.report(b, c)
}

// V7FailClosed closes the gate, proves navigation fails, reopens it and
// proves navigation resumes through the same gate. Tor exits may rotate
// after streams are torn down; a new non-host IP is accepted.
func V7FailClosed(ctx context.Context, s *Subject, o Options) Check {
	start := time.Now()
	c := Check{ID: "V7", Name: "fail-closed"}
	defer func() { c.Duration = time.Since(start) }()

	s.Gate.Close()
	blockCtx, blockCancel := context.WithTimeout(ctx, o.timeout())
	res, err := s.Page.Navigate(blockCtx, cacheBust(o.EchoURL))
	blockCancel()
	if err == nil && !res.Blocked() {
		s.Gate.Open()
		c.Detail = fmt.Sprintf("navigation succeeded (HTTP %d) while the gate was closed", res.Status)
		return o.report(s, c)
	}
	blockedWith := "timeout"
	if err == nil && res.ErrorText != "" {
		blockedWith = res.ErrorText
	}
	if s.sampler != nil {
		if leaks := s.sampler.snapshotNow(); len(leaks) > 0 {
			s.Gate.Open()
			c.Detail = "non-gate endpoints while gate closed: " + joinEndpoints(leaks)
			return o.report(s, c)
		}
	}
	cursor := s.Gate.EventCount()
	s.Gate.Open()
	res, err = resumeNavigate(ctx, s, o)
	if err != nil || res.Blocked() {
		detail := "timeout"
		if err == nil {
			detail = res.ErrorText
		} else {
			detail = err.Error()
		}
		c.Detail = "traffic did not resume after reopening the gate: " + detail
		return o.report(s, c)
	}
	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	body, _ := s.Page.BodyText(readCtx)
	readCancel()
	ip, err := provider.ParseEchoBody([]byte(body))
	if err != nil {
		c.Detail = "no IP after resume"
		return o.report(s, c)
	}
	if o.CompareHostIP && o.HostIPv4 != nil && ip.Equal(o.HostIPv4) {
		c.Detail = fmt.Sprintf("egress became the host IP after resume: %s", ip)
		return o.report(s, c)
	}
	if !gateSucceededSince(s.Gate, cursor) {
		c.Detail = "resume did not produce a successful gate CONNECT"
		return o.report(s, c)
	}
	rotated := s.BrowserIP != nil && !ip.Equal(s.BrowserIP)
	prev := s.BrowserIP
	s.BrowserIP = ip
	c.Passed = true
	if rotated {
		c.Detail = fmt.Sprintf("blocked while down (%s), resumed via %s (exit rotated from %s)", blockedWith, ip, prev)
	} else {
		c.Detail = fmt.Sprintf("blocked while down (%s), resumed via %s", blockedWith, ip)
	}
	return o.report(s, c)
}

func resumeNavigate(ctx context.Context, s *Subject, o Options) (browser.NavResult, error) {
	try := func() (browser.NavResult, error) {
		resumeCtx, cancel := context.WithTimeout(ctx, o.timeout())
		defer cancel()
		return s.Page.Navigate(resumeCtx, cacheBust(o.EchoURL))
	}
	res, err := try()
	if err == nil && !res.Blocked() {
		return res, nil
	}
	// Closing the gate kills every stream. Tor often needs a beat to
	// attach a replacement circuit before ipify works again.
	select {
	case <-ctx.Done():
		return res, err
	case <-time.After(500 * time.Millisecond):
	}
	return try()
}

func gateSucceededSince(g *provider.Gate, cursor int) bool {
	for _, ev := range g.EventsSince(cursor) {
		if ev.Reply == socks5.RepSuccess {
			return true
		}
	}
	return false
}

// V8NoDirect evaluates the endpoint samples taken since Begin.
func V8NoDirect(s *Subject, o Options) Check {
	start := time.Now()
	c := Check{ID: "V8", Name: "no direct connections"}
	if s.sampler == nil {
		c.Detail = "sampler not started"
		return o.report(s, c)
	}
	leaks, samples, err := s.sampler.results()
	c.Duration = time.Since(start)
	if err != nil {
		c.Detail = "endpoint enumeration failed: " + err.Error()
		return o.report(s, c)
	}
	if len(leaks) > 0 {
		c.Detail = "non-gate endpoints observed: " + joinEndpoints(leaks)
		return o.report(s, c)
	}
	c.Passed = true
	c.Detail = fmt.Sprintf("%d samples, only %s observed", samples, s.Gate.Addr())
	return o.report(s, c)
}

// V9StartupURL opens the startup URL in page and confirms the response was
// fetched through this identity's gate.
func V9StartupURL(ctx context.Context, s *Subject, page *browser.Page, startupURL string, o Options) (Check, browser.NavResult) {
	start := time.Now()
	c := Check{ID: "V9", Name: "startup URL"}
	defer func() { c.Duration = time.Since(start) }()
	cursor := s.Gate.EventCount()
	ctx, cancel := context.WithTimeout(ctx, 2*o.timeout())
	defer cancel()
	// Wait for the main-frame document, not a full loadEvent. Sites like
	// whatismyipaddress.com hang on ads/trackers over Tor and never fire it.
	res, err := page.NavigateDocument(ctx, startupURL)
	host := hostOf(startupURL)
	reused := gateSawHost(s.Gate.Events(), host)
	fresh := gateSawHost(s.Gate.EventsSince(cursor), host)
	saw := fresh || reused
	if res.Blocked() && !saw {
		c.Detail = "navigation failed: " + res.ErrorText
		return o.report(s, c), res
	}
	if saw && res.Status > 0 && !res.Blocked() {
		c.Passed = true
		c.Detail = fmt.Sprintf("HTTP %d via %s", res.Status, s.Route.ID())
		if !fresh {
			c.Detail += ", connection reused"
		}
		if res.FinalURL != "" && res.FinalURL != startupURL {
			c.Detail += " (redirected to " + truncate(res.FinalURL, 60) + ")"
		}
		return o.report(s, c), res
	}
	// Isolation of the URL path is proven by a successful CONNECT even if
	// the site never finishes (Tor-blocked, challenge page, hung assets).
	if saw && (res.Status > 0 || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		c.Passed = true
		if res.Status > 0 {
			c.Detail = fmt.Sprintf("HTTP %d via %s", res.Status, s.Route.ID())
		} else {
			c.Detail = fmt.Sprintf("CONNECT to %s via %s (page still loading)", host, s.Route.ID())
		}
		return o.report(s, c), res
	}
	if err != nil {
		c.Detail = err.Error()
		return o.report(s, c), res
	}
	c.Detail = fmt.Sprintf("no CONNECT to %s observed at gate", host)
	return o.report(s, c), res
}

func gateSawHost(events []provider.ConnectEvent, host string) bool {
	for _, ev := range events {
		if strings.EqualFold(ev.Host, host) && ev.Reply == socks5.RepSuccess {
			return true
		}
	}
	return false
}

// ---- endpoint sampler -------------------------------------------------------

type sampler struct {
	b       *browser.Browser
	gate    string
	stopCh  chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	leaks   map[string]platform.Endpoint
	samples int
	err     error
}

func startSampler(b *browser.Browser, gateAddr string) *sampler {
	s := &sampler{b: b, gate: gateAddr, stopCh: make(chan struct{}), done: make(chan struct{}), leaks: map[string]platform.Endpoint{}}
	go s.loop()
	return s
}

func (s *sampler) loop() {
	defer close(s.done)
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		s.sample()
		select {
		case <-s.stopCh:
			return
		case <-t.C:
		}
	}
}

func (s *sampler) sample() []platform.Endpoint {
	eps, err := s.b.Endpoints()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.err = err
		return nil
	}
	s.samples++
	var found []platform.Endpoint
	for _, ep := range eps {
		if isLeak(ep, s.gate) {
			s.leaks[ep.String()] = ep
			found = append(found, ep)
		}
	}
	return found
}

// isLeak decides whether an endpoint is anything other than a TCP connection
// to this identity's gate. Any UDP socket counts.
func isLeak(ep platform.Endpoint, gate string) bool {
	if strings.HasPrefix(ep.Proto, "udp") {
		return true
	}
	if !ep.Remote.IsValid() || ep.Remote.Port() == 0 {
		return false // unconnected TCP socket
	}
	return ep.Remote.String() != gate
}

func (s *sampler) snapshotNow() []platform.Endpoint { return s.sample() }

func (s *sampler) stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	<-s.done
}

func (s *sampler) results() ([]platform.Endpoint, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]platform.Endpoint, 0, len(s.leaks))
	for _, ep := range s.leaks {
		out = append(out, ep)
	}
	return out, s.samples, s.err
}

// ---- helpers ----------------------------------------------------------------

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func countDomain(evs []provider.ConnectEvent) int {
	seen := map[string]bool{}
	for _, ev := range evs {
		if ev.AddrType == socks5.AddrType(socks5.ATypDomain) {
			seen[strings.ToLower(ev.Host)] = true
		}
	}
	return len(seen)
}

func joinEndpoints(eps []platform.Endpoint) string {
	parts := make([]string, 0, len(eps))
	for i, ep := range eps {
		if i >= 5 {
			parts = append(parts, fmt.Sprintf("… (%d total)", len(eps)))
			break
		}
		parts = append(parts, ep.String())
	}
	return strings.Join(parts, "; ")
}
