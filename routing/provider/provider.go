// Package provider defines the RoutingProvider abstraction the controller
// uses to obtain independent network egress for each identity, plus the Gate
// that pins a Chromium process to exactly one route.
package provider

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// RouteStatus is the lifecycle state of a Route.
type RouteStatus int

const (
	StatusCreated RouteStatus = iota
	StatusStarting
	StatusVerifying
	StatusReady
	StatusDown
	StatusStopped
)

func (s RouteStatus) String() string {
	switch s {
	case StatusCreated:
		return "CREATED"
	case StatusStarting:
		return "STARTING"
	case StatusVerifying:
		return "VERIFYING"
	case StatusReady:
		return "READY"
	case StatusDown:
		return "DOWN"
	case StatusStopped:
		return "STOPPED"
	}
	return fmt.Sprintf("RouteStatus(%d)", int(s))
}

// RouteDef is one entry of routes.toml.
type RouteDef struct {
	ID       string `toml:"id"`
	Type     string `toml:"type"`
	Address  string `toml:"address"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	// PasswordEnv names an environment variable holding the password.
	PasswordEnv string `toml:"password_env"`

	// WireGuard fields (type = "wireguard"). Keys are hex or base64.
	PrivateKey    string `toml:"private_key"`
	PeerPublicKey string `toml:"peer_public_key"`
	LocalAddress  string `toml:"local_address"`
	LocalAddress6 string `toml:"local_address6"`
	DNS           string `toml:"dns"`
	MTU           int    `toml:"mtu"`
}

// Redacted returns a log-safe description of the route with credentials
// removed. Nothing else may ever print a RouteDef.
func (d RouteDef) Redacted() string {
	if d.Type == "wireguard" {
		return fmt.Sprintf("wireguard://%s (userspace)", d.Address)
	}
	if d.Username != "" || d.Password != "" || d.PasswordEnv != "" {
		return fmt.Sprintf("%s://%s:***@%s", d.Type, d.Username, d.Address)
	}
	return fmt.Sprintf("%s://%s", d.Type, d.Address)
}

// Validate checks the static fields of a definition.
func (d RouteDef) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return fmt.Errorf("route: id is required")
	}
	if d.Type == "" {
		return fmt.Errorf("route %s: type is required", d.ID)
	}
	if _, _, err := net.SplitHostPort(d.Address); err != nil {
		return fmt.Errorf("route %s: address must be host:port: %v", d.ID, err)
	}
	if d.Type == "wireguard" {
		if d.PrivateKey == "" || d.PeerPublicKey == "" || d.LocalAddress == "" {
			return fmt.Errorf("route %s: wireguard requires private_key, peer_public_key and local_address", d.ID)
		}
		return nil
	}
	if d.Password != "" && d.PasswordEnv != "" {
		return fmt.Errorf("route %s: set either password or password_env, not both", d.ID)
	}
	return nil
}

// Route is a network path providing independent egress. Implementations
// must be safe for concurrent use.
type Route interface {
	ID() string
	Def() RouteDef
	Start(ctx context.Context) error
	Stop() error
	Status() RouteStatus
	// SetStatus is used by the controller's health monitor to mark a route
	// DOWN or READY based on observed probes.
	SetStatus(RouteStatus)
	// Dial opens a TCP connection to hostport through the route. Hostnames
	// must be passed to the far end unresolved.
	Dial(ctx context.Context, network, hostport string) (net.Conn, error)
	// Reachable is a cheap liveness probe of the transport itself (TCP to a
	// proxy upstream, handshake for a tunnel). It does not fetch a public IP.
	Reachable(ctx context.Context) error
	// LastError returns the most recent start/probe error, if any.
	LastError() error
}

// RoutingProvider creates Routes of one type.
type RoutingProvider interface {
	Type() string
	Create(def RouteDef) (Route, error)
}

var (
	regMu     sync.RWMutex
	providers = map[string]RoutingProvider{}
)

// Register makes a provider available under its Type().
func Register(p RoutingProvider) {
	regMu.Lock()
	defer regMu.Unlock()
	providers[p.Type()] = p
}

// Lookup returns the provider for a route type.
func Lookup(typ string) (RoutingProvider, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := providers[typ]
	return p, ok
}

// Types lists registered provider types, sorted.
func Types() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(providers))
	for k := range providers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// New creates a Route for def using the registered provider.
func New(def RouteDef) (Route, error) {
	if err := def.Validate(); err != nil {
		return nil, err
	}
	p, ok := Lookup(def.Type)
	if !ok {
		return nil, fmt.Errorf("route %s: unknown type %q (available: %s)", def.ID, def.Type, strings.Join(Types(), ", "))
	}
	return p.Create(def)
}
