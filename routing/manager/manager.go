// Package manager provisions one independent network route per identity.
// Chromium never talks to a provisioner: it only sees its local Gate.
package manager

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Nareik33L/tab-router/routing/provider"
)

// Provisioner creates and replaces routes. Implementations must not share a
// single network path across identities.
type Provisioner interface {
	Name() string
	// Provision returns n independent routes, Identity i → routes[i].
	// Routes are created, not yet Started.
	Provision(ctx context.Context, n int) ([]provider.Route, error)
	// Reestablish restores the same session path for the slot. It must not
	// mint a new public IP; the caller rejects any replacement whose egress
	// differs from the IP verified at startup. The old route should be
	// Stopped by the caller after the swap.
	Reestablish(ctx context.Context, slot int, failed provider.Route) (provider.Route, error)
	Close() error
}

// SessionReplacer mints a new upstream session for one slot. Startup uses
// it when two identities observe the same public IP. Reestablish must not
// call it: a new session is a new public IP.
type SessionReplacer interface {
	ReplaceSession(ctx context.Context, slot int) (provider.Route, error)
}

// SessionExits pins an identity to the exit observed at V1 so the public
// IP cannot rotate for the rest of the session. Optional: Static and
// leftover Mullvad provisioners do not implement it.
type SessionExits interface {
	PinExit(ctx context.Context, slot int, socksUser string) error
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

// Input selects a provisioner. N is how many identities the caller intends
// to start; provisioners receive that count again at Provision time.
type Input struct {
	DataDir    string
	RoutesPath string
	Explicit   []provider.RouteDef
	N          int
	// Provider is an explicit selection such as "decodo". Empty keeps the
	// historical fallback order. An explicit provider that cannot be built
	// is a fatal error: Open does not continue to Tor.
	Provider string
	// Decodo, when set, is used as-is for Provider "decodo".
	Decodo *Decodo
}

// Resolve picks a provisioner for this session.
//
//  1. explicitDefs (tests / --routes) win.
//  2. Else a configured provider.toml (optional Mullvad leftover).
//  3. Else an existing routes.toml (power-user override).
//  4. Else the default local Tor provisioner (no account, no login).
func Resolve(ctx context.Context, dataDir, routesPath string, explicitDefs []provider.RouteDef, n int) (Provisioner, string, error) {
	return Open(ctx, Input{DataDir: dataDir, RoutesPath: routesPath, Explicit: explicitDefs, N: n})
}

// Open picks a provisioner.
//
//  1. Explicit route definitions (tests / --routes) win.
//  2. Else an explicit provider. "decodo" never falls through to Tor.
//  3. Else a configured provider.toml (optional Mullvad leftover).
//  4. Else an existing routes.toml (power-user override).
//  5. Else the default local Tor provisioner (no account, no login).
func Open(ctx context.Context, in Input) (Provisioner, string, error) {
	if len(in.Explicit) > 0 {
		return Static{Defs: in.Explicit}, "static", nil
	}
	if p := strings.ToLower(strings.TrimSpace(in.Provider)); p != "" {
		switch p {
		case "decodo":
			d := in.Decodo
			if d == nil {
				built, err := DecodoFromEnv(in.DataDir, DecodoConfig{})
				if err != nil {
					return nil, "", fmt.Errorf("decodo: %w", err)
				}
				d = built
			}
			return d, d.Name(), nil
		default:
			return nil, "", fmt.Errorf("unknown network provider %q", in.Provider)
		}
	}
	pp := Path(in.DataDir)
	if _, err := os.Stat(pp); err == nil {
		m, err := OpenMullvad(ctx, pp, in.N)
		if err != nil {
			return nil, "", err
		}
		return m, m.Name(), nil
	}
	if in.RoutesPath != "" {
		if _, err := os.Stat(in.RoutesPath); err == nil {
			return nil, "", ErrTryRoutes
		}
	}
	return &Local{DataDir: in.DataDir}, "tor", nil
}

// ErrNoProvider is retained for callers that still special-case a missing
// optional override; normal startup never returns it.
var ErrNoProvider = fmt.Errorf("no network provider configured")

// ErrTryRoutes tells the caller to fall back to routes.toml.
var ErrTryRoutes = fmt.Errorf("no provider.toml; try routes.toml")
