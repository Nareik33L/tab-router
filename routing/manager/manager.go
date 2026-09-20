// Package manager provisions one independent network route per identity.
// Chromium never talks to a provisioner: it only sees its local Gate.
package manager

import (
	"context"
	"fmt"
	"os"

	"github.com/Nareik33L/tab-router/routing/provider"
)

// Provisioner creates and replaces routes. Implementations must not share a
// single network path across identities.
type Provisioner interface {
	Name() string
	// Provision returns n independent routes, Identity i → routes[i].
	// Routes are created, not yet Started.
	Provision(ctx context.Context, n int) ([]provider.Route, error)
	// Reestablish replaces a failed route with a new independent path for
	// the same slot (same identity). The old route should be Stopped by
	// the caller after the swap.
	Reestablish(ctx context.Context, slot int, failed provider.Route) (provider.Route, error)
	Close() error
}

// Static turns already-known RouteDefs (tests, leftover routes.toml) into
// a Provisioner. It does not invent egress; it only wraps supplied upstreams.
type Static struct {
	Defs []provider.RouteDef
}

func (s Static) Name() string {
	if len(s.Defs) == 0 {
		return "static"
	}
	return "static/" + s.Defs[0].Type
}

func (s Static) Provision(_ context.Context, n int) ([]provider.Route, error) {
	if n < 1 {
		return nil, fmt.Errorf("provision: need at least 1 route")
	}
	if len(s.Defs) < n {
		return nil, fmt.Errorf("provision: %d identities requested but only %d routes available (routes are never shared)", n, len(s.Defs))
	}
	out := make([]provider.Route, 0, n)
	for i := 0; i < n; i++ {
		r, err := provider.New(s.Defs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s Static) Reestablish(_ context.Context, slot int, failed provider.Route) (provider.Route, error) {
	if slot < 1 || slot > len(s.Defs) {
		return nil, fmt.Errorf("provision: no route for slot %d", slot)
	}
	return provider.New(s.Defs[slot-1])
}

func (s Static) Close() error { return nil }

// Resolve picks a provisioner for this session.
//
//  1. explicitDefs (tests / --routes) win.
//  2. Else a configured provider.toml (optional Mullvad leftover).
//  3. Else an existing routes.toml (power-user override).
//  4. Else the default local Tor provisioner (no account, no login).
func Resolve(ctx context.Context, dataDir, routesPath string, explicitDefs []provider.RouteDef, n int) (Provisioner, string, error) {
	if len(explicitDefs) > 0 {
		return Static{Defs: explicitDefs}, "static", nil
	}
	pp := Path(dataDir)
	if _, err := os.Stat(pp); err == nil {
		m, err := OpenMullvad(ctx, pp, n)
		if err != nil {
			return nil, "", err
		}
		return m, m.Name(), nil
	}
	if routesPath != "" {
		if _, err := os.Stat(routesPath); err == nil {
			return nil, "", ErrTryRoutes
		}
	}
	return &Local{DataDir: dataDir}, "tor", nil
}

// ErrNoProvider is retained for callers that still special-case a missing
// optional override; normal startup never returns it.
var ErrNoProvider = fmt.Errorf("no network provider configured")

// ErrTryRoutes tells the caller to fall back to routes.toml.
var ErrTryRoutes = fmt.Errorf("no provider.toml; try routes.toml")
