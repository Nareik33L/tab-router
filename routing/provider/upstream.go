package provider

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/routing/httpproxy"
	"github.com/Nareik33L/tab-router/routing/socks5"
)

// DialFunc opens a connection to hostport through some upstream.
type DialFunc func(ctx context.Context, network, hostport string) (net.Conn, error)

// upstreamRoute is a Route backed by a proxy-style upstream (SOCKS5 or HTTP
// CONNECT). Start only checks TCP reachability of the upstream; READY is
// granted by the controller after a successful public-IP probe.
type upstreamRoute struct {
	def  RouteDef
	dial DialFunc

	mu      sync.Mutex
	status  RouteStatus
	lastErr error
}

func newUpstreamRoute(def RouteDef, dial DialFunc) *upstreamRoute {
	return &upstreamRoute{def: def, dial: dial, status: StatusCreated}
}

func (r *upstreamRoute) ID() string    { return r.def.ID }
func (r *upstreamRoute) Def() RouteDef { return r.def }

func (r *upstreamRoute) Status() RouteStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *upstreamRoute) SetStatus(s RouteStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *upstreamRoute) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *upstreamRoute) setErr(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
}

// Start verifies the upstream accepts TCP connections. It does not mark the
// route READY; that requires a verified public IP (see controller/verify).
func (r *upstreamRoute) Start(ctx context.Context) error {
	r.SetStatus(StatusStarting)
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", r.def.Address)
	if err != nil {
		r.setErr(err)
		r.SetStatus(StatusDown)
		return fmt.Errorf("route %s: upstream unreachable: %w", r.def.ID, redactErr(err))
	}
	c.Close()
	r.setErr(nil)
	r.SetStatus(StatusVerifying)
	return nil
}

func (r *upstreamRoute) Stop() error {
	r.SetStatus(StatusStopped)
	return nil
}

func (r *upstreamRoute) Reachable(ctx context.Context) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", r.def.Address)
	if err != nil {
		return err
	}
	c.Close()
	return nil
}

func (r *upstreamRoute) Dial(ctx context.Context, network, hostport string) (net.Conn, error) {
	switch r.Status() {
	case StatusReady, StatusVerifying:
	default:
		return nil, fmt.Errorf("route %s is %s", r.def.ID, r.Status())
	}
	c, err := r.dial(ctx, network, hostport)
	if err != nil {
		return nil, redactErr(err)
	}
	return c, nil
}

// redactErr strips nothing today but is the single choke point should an
// upstream library ever include credentials in an error string.
func redactErr(err error) error { return err }

// SOCKS5Provider creates routes backed by upstream SOCKS5 proxies.
type SOCKS5Provider struct{}

func (SOCKS5Provider) Type() string { return "socks5" }

func (SOCKS5Provider) Create(def RouteDef) (Route, error) {
	pass, err := resolvePassword(def)
	if err != nil {
		return nil, err
	}
	d := &socks5.Dialer{ProxyAddr: def.Address, Username: def.Username, Password: pass}
	return newUpstreamRoute(def, d.DialContext), nil
}

// HTTPProvider creates routes backed by upstream HTTP CONNECT proxies.
type HTTPProvider struct{}

func (HTTPProvider) Type() string { return "http" }

func (HTTPProvider) Create(def RouteDef) (Route, error) {
	pass, err := resolvePassword(def)
	if err != nil {
		return nil, err
	}
	d := &httpproxy.Dialer{ProxyAddr: def.Address, Username: def.Username, Password: pass}
	return newUpstreamRoute(def, d.DialContext), nil
}

func init() {
	Register(SOCKS5Provider{})
	Register(HTTPProvider{})
}
