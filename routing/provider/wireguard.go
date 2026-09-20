package provider

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/routing/wireguard"
)

// wgRoute is a Route whose Dial goes through a userspace WireGuard tunnel.
// Chromium never sees WireGuard: it still talks only to its local Gate.
type wgRoute struct {
	def RouteDef

	mu      sync.Mutex
	tun     *wireguard.Tunnel
	status  RouteStatus
	lastErr error
}

func (r *wgRoute) ID() string    { return r.def.ID }
func (r *wgRoute) Def() RouteDef { return r.def }

func (r *wgRoute) Status() RouteStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *wgRoute) SetStatus(s RouteStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *wgRoute) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *wgRoute) setErr(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
}

func (r *wgRoute) Start(ctx context.Context) error {
	r.SetStatus(StatusStarting)
	cfg := wireguard.Config{
		PrivateKey:    r.def.PrivateKey,
		PeerPublicKey: r.def.PeerPublicKey,
		Endpoint:      r.def.Address,
		LocalAddress:  r.def.LocalAddress,
		LocalAddress6: r.def.LocalAddress6,
		DNS:           r.def.DNS,
		MTU:           r.def.MTU,
	}
	tun, err := wireguard.Open(cfg)
	if err != nil {
		r.setErr(err)
		r.SetStatus(StatusDown)
		return fmt.Errorf("route %s: %w", r.def.ID, err)
	}
	hctx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		hctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	if err := tun.WaitHandshake(hctx); err != nil {
		_ = tun.Close()
		r.setErr(err)
		r.SetStatus(StatusDown)
		return fmt.Errorf("route %s: %w", r.def.ID, err)
	}
	r.mu.Lock()
	if r.tun != nil {
		_ = r.tun.Close()
	}
	r.tun = tun
	r.lastErr = nil
	r.status = StatusVerifying
	r.mu.Unlock()
	return nil
}

func (r *wgRoute) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = StatusStopped
	if r.tun != nil {
		err := r.tun.Close()
		r.tun = nil
		return err
	}
	return nil
}

func (r *wgRoute) Dial(ctx context.Context, network, hostport string) (net.Conn, error) {
	switch r.Status() {
	case StatusReady, StatusVerifying:
	default:
		return nil, fmt.Errorf("route %s is %s", r.def.ID, r.Status())
	}
	r.mu.Lock()
	tun := r.tun
	r.mu.Unlock()
	if tun == nil {
		return nil, fmt.Errorf("route %s: tunnel not started", r.def.ID)
	}
	return tun.DialContext(ctx, network, hostport)
}

func (r *wgRoute) Reachable(ctx context.Context) error {
	r.mu.Lock()
	tun := r.tun
	r.mu.Unlock()
	if tun == nil {
		return fmt.Errorf("tunnel not started")
	}
	if tun.Alive() {
		return nil
	}
	return fmt.Errorf("no recent handshake")
}

// WireGuardProvider creates userspace WireGuard routes.
type WireGuardProvider struct{}

func (WireGuardProvider) Type() string { return "wireguard" }

func (WireGuardProvider) Create(def RouteDef) (Route, error) {
	return &wgRoute{def: def, status: StatusCreated}, nil
}

func init() {
	Register(WireGuardProvider{})
}
