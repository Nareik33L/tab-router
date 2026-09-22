package startup

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nareik33L/tab-router/controller/config"
	"github.com/Nareik33L/tab-router/routing/manager"
)

func TestResolveDefaultsToLocalExits(t *testing.T) {
	dir := t.TempDir()
	p, name, err := manager.Resolve(context.Background(), dir, filepath.Join(dir, "routes.toml"), nil, 2)
	if err != nil || name != "tor" {
		t.Fatalf("want tor provisioner, got %v %s", err, name)
	}
	if _, ok := p.(*manager.Local); !ok {
		t.Fatalf("got %T", p)
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
