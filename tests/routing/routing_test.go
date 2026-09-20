// Package routing_test holds unit tests for the routing layer that need no
// browser: SOCKS5/HTTP dialers and servers, the gate, and the echo parser.
package routing_test

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/routing/httpproxy"
	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/routing/socks5"
	"github.com/Nareik33L/tab-router/tests/infra"
)

func echoTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, dial provider.DialFunc, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dial(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo mismatch: %q %v", buf, err)
	}
}

func TestSOCKS5ClientServerRoundTrip(t *testing.T) {
	target := echoTCP(t)
	var seen []socks5.Request
	srv := &socks5.Server{
		Dial:         func(ctx context.Context, hp string) (net.Conn, error) { return net.Dial("tcp", hp) },
		OnRequest:    func(r socks5.Request) { seen = append(seen, r) },
		Authenticate: func(u, p string) bool { return u == "u" && p == "p" },
	}
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	d := &socks5.Dialer{ProxyAddr: addr.String(), Username: "u", Password: "p"}
	roundTrip(t, d.DialContext, target)
	if len(seen) != 1 || seen[0].Target.Type != socks5.AddrType(socks5.ATypIPv4) {
		t.Fatalf("unexpected requests: %+v", seen)
	}

	// Hostnames must be sent as DOMAINNAME, unresolved.
	host, port, _ := net.SplitHostPort(target)
	_ = host
	srv2 := &socks5.Server{Dial: func(ctx context.Context, hp string) (net.Conn, error) {
		h, p, _ := net.SplitHostPort(hp)
		if h != "echo.test" {
			t.Errorf("server got %q, want hostname", h)
		}
		return net.Dial("tcp", net.JoinHostPort("127.0.0.1", p))
	}}
	addr2, _ := srv2.Listen("127.0.0.1:0")
	defer srv2.Close()
	d2 := &socks5.Dialer{ProxyAddr: addr2.String()}
	roundTrip(t, d2.DialContext, "echo.test:"+port)

	// Wrong credentials are rejected.
	bad := &socks5.Dialer{ProxyAddr: addr.String(), Username: "u", Password: "x"}
	if _, err := bad.DialContext(context.Background(), "tcp", target); err == nil {
		t.Fatal("expected auth failure")
	}
	// UDP is refused outright.
	if _, err := d.DialContext(context.Background(), "udp", target); err == nil {
		t.Fatal("expected UDP to be refused")
	}
}

func TestHTTPConnectRoundTrip(t *testing.T) {
	target := echoTCP(t)
	srv := &httpproxy.Server{
		Dial:         func(ctx context.Context, hp string) (net.Conn, error) { return net.Dial("tcp", hp) },
		Authenticate: func(u, p string) bool { return u == "a" && p == "b" },
	}
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	d := &httpproxy.Dialer{ProxyAddr: addr.String(), Username: "a", Password: "b"}
	roundTrip(t, d.DialContext, target)
	bad := &httpproxy.Dialer{ProxyAddr: addr.String()}
	if _, err := bad.DialContext(context.Background(), "tcp", target); err == nil {
		t.Fatal("expected 407")
	}
}

func TestGateRefusesWhenClosedAndKillsTunnels(t *testing.T) {
	target := echoTCP(t)
	up := &socks5.Server{Dial: func(ctx context.Context, hp string) (net.Conn, error) { return net.Dial("tcp", hp) }}
	upAddr, _ := up.Listen("127.0.0.1:0")
	defer up.Close()

	route, err := provider.New(provider.RouteDef{ID: "r", Type: "socks5", Address: upAddr.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := route.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	gate := provider.NewGate(route)
	gateAddr, err := gate.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Shutdown()

	client := &socks5.Dialer{ProxyAddr: gateAddr}
	// CLOSED by default: refused.
	if _, err := client.DialContext(context.Background(), "tcp", target); err == nil {
		t.Fatal("gate forwarded while CLOSED")
	}
	ev := gate.Events()
	if len(ev) != 1 || ev[0].Reply != socks5.RepGeneralFailure {
		t.Fatalf("expected one refused event, got %+v", ev)
	}

	gate.Open()
	c, err := client.DialContext(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if gate.ActiveConnections() != 1 {
		t.Fatalf("expected 1 active tunnel, got %d", gate.ActiveConnections())
	}
	// Closing the gate must terminate the live tunnel.
	gate.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(buf); err == nil {
		t.Fatal("tunnel survived gate close")
	}
	if !strings.Contains(route.Def().Redacted(), upAddr.String()) {
		t.Fatalf("redacted def should keep address: %s", route.Def().Redacted())
	}
}

func TestRouteDefRedaction(t *testing.T) {
	d := provider.RouteDef{ID: "x", Type: "socks5", Address: "h:1", Username: "alice", Password: "s3cret"}
	if r := d.Redacted(); strings.Contains(r, "s3cret") || !strings.Contains(r, "***") {
		t.Fatalf("redaction failed: %s", r)
	}
}

func TestParseEchoBody(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4\n":                            "1.2.3.4",
		`{"ip":"203.0.113.9"}`:                 "203.0.113.9",
		`{"origin": "198.51.100.1, 10.0.0.1"}`: "198.51.100.1",
		"2001:db8::1":                          "2001:db8::1",
	}
	for in, want := range cases {
		ip, err := provider.ParseEchoBody([]byte(in))
		if err != nil || ip.String() != want {
			t.Errorf("%q -> %v %v, want %s", in, ip, err, want)
		}
	}
	if _, err := provider.ParseEchoBody([]byte("<html>nope</html>")); err == nil {
		t.Error("expected error for HTML body")
	}
}

// TestInfraDistinctEgress is T-A at the controller level (M2 exit
// criterion): two routes, two source IPs, simultaneously.
func TestInfraDistinctEgress(t *testing.T) {
	in, err := infra.Start(infra.Options{Proxies: []string{"socks5", "http"}})
	if err != nil {
		t.Skipf("infra unavailable: %v", err)
	}
	defer in.Close()
	ctx := context.Background()
	var ips []string
	for i, p := range in.Proxies {
		r, err := provider.New(provider.RouteDef{ID: "r", Type: p.Type, Address: p.Addr})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Start(ctx); err != nil {
			t.Fatal(err)
		}
		ip, err := provider.PublicIP(ctx, r.Dial, in.EchoURL(), 5*time.Second)
		if err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
		ips = append(ips, ip.String())
	}
	if ips[0] == ips[1] || ips[0] != "127.0.0.2" || ips[1] != "127.0.0.3" {
		t.Fatalf("expected distinct egress 127.0.0.2/127.0.0.3, got %v", ips)
	}
	host, err := provider.PublicIP(ctx, provider.DirectDial, in.EchoURLDirect(), 5*time.Second)
	if err != nil || host.String() != "127.0.0.1" {
		t.Fatalf("host probe: %v %v", host, err)
	}
}
