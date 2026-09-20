package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/controller/verify"
	"github.com/Nareik33L/tab-router/routing/provider"
)

type stubRoute struct {
	addr      string
	reachable error
	status    provider.RouteStatus
}

func (r *stubRoute) ID() string { return "route-001" }
func (r *stubRoute) Def() provider.RouteDef {
	return provider.RouteDef{ID: "route-001", Type: "socks5", Address: r.addr}
}
func (r *stubRoute) Start(context.Context) error      { return nil }
func (r *stubRoute) Stop() error                      { return nil }
func (r *stubRoute) Status() provider.RouteStatus     { return r.status }
func (r *stubRoute) SetStatus(s provider.RouteStatus) { r.status = s }
func (r *stubRoute) LastError() error                 { return r.reachable }
func (r *stubRoute) Reachable(context.Context) error  { return r.reachable }
func (r *stubRoute) Dial(ctx context.Context, _, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", r.addr)
}

func TestProbeEgressChangeEmitsOnce(t *testing.T) {
	var current atomic.Value
	current.Store("198.51.100.10")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, current.Load().(string))
	}))
	t.Cleanup(srv.Close)

	rt := &stubRoute{addr: srv.Listener.Addr().String()}
	sub := &verify.Subject{
		Route:   rt,
		Gate:    provider.NewGate(rt),
		RouteIP: net.ParseIP("198.51.100.10"),
	}
	var events []Event
	m := New([]*verify.Subject{sub}, Options{
		ProbeInterval:   time.Hour,
		IPCheckInterval: time.Nanosecond,
		EchoURL:         srv.URL,
		Timeout:         2 * time.Second,
		OnEvent:         func(ev Event) { events = append(events, ev) },
		Reestablish: func(context.Context, *verify.Subject) error {
			t.Fatal("reestablish must not run when the session IP changes")
			return nil
		},
		ReestablishAfter: time.Millisecond,
	})

	m.probe(context.Background(), sub)
	if sub.Route.Status() != provider.StatusReady {
		t.Fatalf("same IP should stay ready, got %s", sub.Route.Status())
	}

	current.Store("203.0.113.9")
	m.probe(context.Background(), sub)
	m.probe(context.Background(), sub)
	m.probe(context.Background(), sub)

	changed := 0
	for _, ev := range events {
		if ev.Kind == EgressChanged {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("EGRESS CHANGED emitted %d times, want 1: %+v", changed, events)
	}
	if sub.Gate.IsOpen() {
		t.Fatal("gate must stay closed after the session IP moves")
	}
	if !sub.RouteIP.Equal(net.ParseIP("198.51.100.10")) {
		t.Fatalf("RouteIP must stay pinned, got %s", sub.RouteIP)
	}
}

func TestProbeRestoresOnlyOriginalIP(t *testing.T) {
	var current atomic.Value
	current.Store("198.51.100.10")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, current.Load().(string))
	}))
	t.Cleanup(srv.Close)

	rt := &stubRoute{addr: srv.Listener.Addr().String(), reachable: fmt.Errorf("down")}
	sub := &verify.Subject{
		Route:   rt,
		Gate:    provider.NewGate(rt),
		RouteIP: net.ParseIP("198.51.100.10"),
	}
	m := New([]*verify.Subject{sub}, Options{
		ProbeInterval:    time.Hour,
		IPCheckInterval:  time.Nanosecond,
		EchoURL:          srv.URL,
		Timeout:          2 * time.Second,
		ReestablishAfter: time.Hour,
	})
	m.probe(context.Background(), sub)
	if !m.StateOf(sub).Down {
		t.Fatal("expected DOWN while unreachable")
	}

	rt.reachable = nil
	m.probe(context.Background(), sub)
	if m.StateOf(sub).Down || !sub.Gate.IsOpen() {
		t.Fatal("original IP should restore the route")
	}
}
