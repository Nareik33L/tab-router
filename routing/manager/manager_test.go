package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/routing/wireguard"
)

func TestStaticProvisionsIndependentSlots(t *testing.T) {
	defs := []provider.RouteDef{
		{ID: "route-001", Type: "socks5", Address: "127.0.0.1:1"},
		{ID: "route-002", Type: "socks5", Address: "127.0.0.1:2"},
	}
	p := Static{Defs: defs}
	ctx := context.Background()
	routes, err := p.Provision(ctx, 2)
	if err != nil || len(routes) != 2 {
		t.Fatalf("Provision: %v %d", err, len(routes))
	}
	if routes[0].ID() == routes[1].ID() {
		t.Fatal("routes shared an id")
	}
	if _, err := p.Provision(ctx, 3); err == nil {
		t.Fatal("provisioner must not share a route across identities")
	}
	r, err := p.Reestablish(ctx, 2, routes[1])
	if err != nil || r.ID() != "route-002" {
		t.Fatalf("Reestablish slot 2: %v %v", err, r)
	}
}

func TestResolveOrder(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	explicit := []provider.RouteDef{{ID: "r", Type: "socks5", Address: "h:1"}}
	p, name, err := Resolve(ctx, dir, filepath.Join(dir, "routes.toml"), explicit, 1)
	if err != nil || !strings.HasPrefix(name, "static") {
		t.Fatalf("explicit defs: %v %s", err, name)
	}
	if _, ok := p.(Static); !ok {
		t.Fatalf("got %T", p)
	}

	p, name, err = Resolve(ctx, dir, filepath.Join(dir, "missing.toml"), nil, 2)
	if err != nil || name != "tor" {
		t.Fatalf("want tor, got %v %s", err, name)
	}
	if _, ok := p.(*Local); !ok {
		t.Fatalf("got %T", p)
	}

	routes := filepath.Join(dir, "routes.toml")
	body := "[[route]]\nid=\"r1\"\ntype=\"socks5\"\naddress=\"h:1\"\n"
	if err := os.WriteFile(routes, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = Resolve(ctx, dir, routes, nil, 2)
	if !errors.Is(err, ErrTryRoutes) {
		t.Fatalf("want ErrTryRoutes, got %v", err)
	}

	priv, _ := wireguard.GeneratePrivateKey()
	f := File{
		Type:    "mullvad",
		Account: "1234567890123456",
		Device: []Device{{
			Slot: 1, ID: "d1", Name: "one", PrivateKey: priv.Base64(), IPv4: "10.64.0.1",
		}, {
			Slot: 2, ID: "d2", Name: "two", PrivateKey: priv.Base64(), IPv4: "10.64.0.2",
		}},
	}
	if err := SaveFile(Path(dir), f); err != nil {
		t.Fatal(err)
	}
	p, name, err = Resolve(ctx, dir, routes, nil, 2)
	if err != nil || name != "mullvad" {
		t.Fatalf("provider.toml should win over routes.toml: %v %s", err, name)
	}
	if p.Name() != "mullvad" {
		t.Fatalf("name %s", p.Name())
	}
}

func TestProviderFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics")
	}
	dir := t.TempDir()
	p := Path(dir)
	f := File{Type: "mullvad", Account: "1234567890123456"}
	if err := SaveFile(p, f); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("provider.toml not owner-only: %v", st)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(p); err == nil || !strings.Contains(err.Error(), "other users") && !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("world-readable provider.toml accepted: %v", err)
	}
}

func TestLogoutRemovesFileOnly(t *testing.T) {
	dir := t.TempDir()
	p := Path(dir)
	if err := SaveFile(p, File{Type: "mullvad", Account: "1234567890123456"}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "identities-keep-me")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Logout(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("provider.toml still present")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("logout must not touch unrelated files")
	}
}

func TestRedactAccount(t *testing.T) {
	if got := RedactAccount("1234567890123456"); got != "************3456" {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(MissingProviderMessage("/tmp/tr"), "tab-router --identities 2") {
		t.Fatal("missing default start hint")
	}
	if strings.Contains(MissingProviderMessage("/tmp/tr"), "provider login") {
		t.Fatal("login must not be presented as required")
	}
}
