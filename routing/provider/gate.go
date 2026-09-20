package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nareik33L/tab-router/routing/socks5"
)

// ConnectEvent records one CONNECT request that reached a gate. Only the
// hostname is retained, never URL paths.
type ConnectEvent struct {
	At       time.Time
	Host     string
	Port     int
	AddrType socks5.AddrType
	Reply    byte
	Err      string
}

// Gate is the only proxy a Chromium identity is configured to use. It is a
// SOCKS5 listener on loopback that forwards to exactly one Route and refuses
// everything while CLOSED. Closing a gate also terminates live tunnels.
type Gate struct {
	route Route
	srv   *socks5.Server
	open  atomic.Bool

	mu     sync.Mutex
	events []ConnectEvent
	max    int
}

// NewGate creates a gate bound to route. Call Listen to start it (CLOSED).
func NewGate(route Route) *Gate {
	g := &Gate{route: route, max: 4096}
	g.srv = &socks5.Server{
		Dial: func(ctx context.Context, hostport string) (net.Conn, error) {
			return route.Dial(ctx, "tcp", hostport)
		},
		Refuse:   func() bool { return !g.open.Load() },
		OnResult: g.record,
	}
	return g
}

// Listen binds the gate to a random loopback port.
func (g *Gate) Listen() (string, error) {
	addr, err := g.srv.Listen("127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("gate for %s: %w", g.route.ID(), err)
	}
	return addr.String(), nil
}

// Addr returns "127.0.0.1:port" or "" before Listen.
func (g *Gate) Addr() string {
	a := g.srv.Addr()
	if a == nil {
		return ""
	}
	return a.String()
}

// Port returns the bound port or 0.
func (g *Gate) Port() int {
	a, ok := g.srv.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return a.Port
}

// Route returns the bound route.
func (g *Gate) Route() Route { return g.route }

// Open allows forwarding.
func (g *Gate) Open() { g.open.Store(true) }

// Close refuses new requests and terminates all live tunnels.
func (g *Gate) Close() {
	g.open.Store(false)
	g.srv.CloseConnections()
}

// IsOpen reports whether the gate forwards traffic.
func (g *Gate) IsOpen() bool { return g.open.Load() }

// ActiveConnections reports live client connections.
func (g *Gate) ActiveConnections() int { return g.srv.ActiveConnections() }

// Shutdown stops the listener permanently.
func (g *Gate) Shutdown() error {
	g.open.Store(false)
	return g.srv.Close()
}

func (g *Gate) record(req socks5.Request, code byte, err error) {
	ev := ConnectEvent{At: req.At, Host: req.Target.Host, Port: req.Target.Port, AddrType: req.Target.Type, Reply: code}
	if err != nil {
		ev.Err = err.Error()
	}
	g.mu.Lock()
	g.events = append(g.events, ev)
	if len(g.events) > g.max {
		g.events = g.events[len(g.events)-g.max:]
	}
	g.mu.Unlock()
}

// Events returns a snapshot of recorded CONNECT events.
func (g *Gate) Events() []ConnectEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]ConnectEvent, len(g.events))
	copy(out, g.events)
	return out
}

// EventCount returns the number of events recorded so far; useful as a
// cursor for "events since".
func (g *Gate) EventCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.events)
}

// EventsSince returns events recorded after cursor (an earlier EventCount).
func (g *Gate) EventsSince(cursor int) []ConnectEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cursor < 0 || cursor > len(g.events) {
		cursor = 0
	}
	out := make([]ConnectEvent, len(g.events)-cursor)
	copy(out, g.events[cursor:])
	return out
}

// ErrGateClosed is returned by helpers when the gate is not forwarding.
var ErrGateClosed = errors.New("gate is closed")
