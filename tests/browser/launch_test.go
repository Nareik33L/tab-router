package browser_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/tests/infra"
	"github.com/Nareik33L/tab-router/tests/testutil"
)

// TestBrowserEgressThroughGate proves the M1+M2 stack: Chromium, pinned to a
// gate, reaches the echo server via the route's upstream and reports the
// upstream's source address rather than the host's.
func TestBrowserEgressThroughGate(t *testing.T) {
	bin := testutil.ChromiumOrSkip(t)
	in, err := infra.Start(infra.Options{Proxies: []string{"socks5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	route, err := provider.New(provider.RouteDef{ID: "route-001", Type: "socks5", Address: in.Proxies[0].Addr})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := route.Start(ctx); err != nil {
		t.Fatal(err)
	}
	gate := provider.NewGate(route)
	addr, err := gate.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Shutdown()
	gate.Open()

	b, err := browser.Launch(ctx, browser.LaunchOptions{Binary: bin, ProfileDir: t.TempDir(), GateAddr: addr, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Kill()

	page, err := b.FirstPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := page.Navigate(ctx, in.EchoURL())
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() {
		t.Fatalf("navigation blocked: %s", res.ErrorText)
	}
	body, err := page.BodyText(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"127.0.0.2"`) {
		t.Fatalf("expected echo of route source 127.0.0.2, got %q", body)
	}
	evs := gate.Events()
	if len(evs) == 0 {
		t.Fatal("gate recorded no CONNECT events")
	}
	for _, ev := range evs {
		if ev.Host == infra.EchoHost && ev.AddrType.String() != "DOMAINNAME" {
			t.Fatalf("browser resolved %s locally (ATYP=%s)", ev.Host, ev.AddrType)
		}
	}
	lk := in.Proxies[0].Lookups()
	resolved := false
	for _, h := range lk {
		if h == infra.EchoHost {
			resolved = true
		}
	}
	if !resolved {
		t.Fatalf("upstream did not resolve %s; lookups=%v", infra.EchoHost, lk)
	}
	t.Logf("upstream lookups (background traffic is routed, not leaked): %v", lk)

	// Fail-closed: close the gate, navigation must error at the network layer.
	gate.Close()
	res, err = page.Navigate(ctx, in.EchoURL()+"text")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked() {
		t.Fatalf("expected navigation to be blocked while gate closed, got status %d", res.Status)
	}
	gate.Open()
	res, err = page.Navigate(ctx, in.EchoURL()+"text")
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() {
		t.Fatalf("expected navigation to resume, got %s", res.ErrorText)
	}

	eps, err := b.Endpoints()
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if strings.HasPrefix(ep.Proto, "tcp") && ep.Remote.String() != addr {
			t.Errorf("non-gate TCP endpoint: %s", ep)
		}
		if strings.HasPrefix(ep.Proto, "udp") {
			t.Errorf("UDP socket present: %s", ep)
		}
	}
}
