package startup

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/controller/config"
	"github.com/Nareik33L/tab-router/controller/identity"
	"github.com/Nareik33L/tab-router/controller/verify"
	"github.com/Nareik33L/tab-router/routing/manager"
	"github.com/Nareik33L/tab-router/routing/provider"
)

func TestResolveRequiresDecodoCredentials(t *testing.T) {
	t.Setenv("DECODO_USERNAME", "")
	t.Setenv("DECODO_PASSWORD", "")
	dir := t.TempDir()
	_, name, err := manager.Resolve(context.Background(), dir, filepath.Join(dir, "routes.toml"), nil, 2)
	if err == nil || name == "tor" || !strings.Contains(err.Error(), "decodo") {
		t.Fatalf("want decodo credential error, got %v %s", err, name)
	}
}

func TestRunWithBrokenLocalProvisionerStopsSafely(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.RoutesPath = filepath.Join(dir, "routes.toml")
	cfg.Headless = true
	cfg.Identities = 2
	var out bytes.Buffer
	_, err := Run(context.Background(), cfg, NewReporter(&out, true), Options{
		Provisioner: &manager.Local{DataDir: dir, Binary: filepath.Join(dir, "no-such-tor")},
	})
	if err == nil {
		t.Fatal("expected startup to fail without a working local exit")
	}
	if bytes.Contains(out.Bytes(), []byte("TAB ROUTER READY")) {
		t.Fatal("READY printed without working exits")
	}
	if strings.Contains(out.String(), "provider login") {
		t.Fatalf("must not ask for provider login:\n%s", out.String())
	}
}

func TestDecodoSelectionFailsClosed(t *testing.T) {
	t.Setenv("DECODO_USERNAME", "")
	t.Setenv("DECODO_PASSWORD", "")
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.RoutesPath = filepath.Join(dir, "routes.toml")
	cfg.Headless = true
	cfg.Identities = 2
	cfg.Routing.Provider = "decodo"
	var out bytes.Buffer
	_, err := Run(context.Background(), cfg, NewReporter(&out, true), Options{})
	if err == nil {
		t.Fatal("expected decodo startup to fail without credentials")
	}
	if bytes.Contains(out.Bytes(), []byte("TAB ROUTER READY")) {
		t.Fatal("READY without a decodo route")
	}
	if !strings.Contains(err.Error(), "decodo") {
		t.Fatalf("error should name decodo, not fall through: %v", err)
	}
}

func TestDeadResidentialSessionIsReplacedOnce(t *testing.T) {
	bad := errors.New("socks5: host unreachable")
	next := &dialRoute{err: bad}
	prov := &onceReplacer{next: next}
	var out bytes.Buffer
	s := &Session{prov: prov, Report: NewReporter(&out, true)}
	sub := &verify.Subject{
		Identity: identity.Identity{Index: 1, RouteSlot: 1},
		Route:    &dialRoute{err: bad},
	}
	err := s.bringUpRoute(context.Background(), sub, verify.Options{
		EchoURL: "https://api.ipify.org?format=json",
		Timeout: time.Second,
	})
	if err == nil || prov.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, prov.calls)
	}
	if !strings.Contains(out.String(), "requesting another") {
		t.Fatalf("output:\n%s", out.String())
	}
	if sub.Route != next {
		t.Fatal("failed session was kept")
	}
}

type dialRoute struct {
	err    error
	status provider.RouteStatus
}

func (r *dialRoute) ID() string { return "route-001" }
func (r *dialRoute) Def() provider.RouteDef {
	return provider.RouteDef{ID: "route-001", Type: "socks5", Address: "127.0.0.1:1"}
}
func (r *dialRoute) Start(context.Context) error      { return nil }
func (r *dialRoute) Stop() error                      { return nil }
func (r *dialRoute) Status() provider.RouteStatus     { return r.status }
func (r *dialRoute) SetStatus(s provider.RouteStatus) { r.status = s }
func (r *dialRoute) LastError() error                 { return r.err }
func (r *dialRoute) Reachable(context.Context) error  { return r.err }
func (r *dialRoute) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, r.err
}

type onceReplacer struct {
	calls int
	next  provider.Route
}

func (r *onceReplacer) Name() string { return "decodo" }
func (r *onceReplacer) Provision(context.Context, int) ([]provider.Route, error) {
	return nil, nil
}
func (r *onceReplacer) Reestablish(context.Context, int, provider.Route) (provider.Route, error) {
	return nil, errors.New("no")
}
func (r *onceReplacer) Close() error { return nil }
func (r *onceReplacer) ReplaceSession(context.Context, int) (provider.Route, error) {
	r.calls++
	return r.next, nil
}
