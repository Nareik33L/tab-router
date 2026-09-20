package startup

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nareik33L/tab-router/controller/config"
	"github.com/Nareik33L/tab-router/routing/manager"
)

func TestRunWithoutProviderStopsSafely(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.RoutesPath = filepath.Join(dir, "routes.toml")
	cfg.Headless = true
	cfg.Identities = 2
	var out bytes.Buffer
	_, err := Run(context.Background(), cfg, NewReporter(&out, true), Options{})
	if !errors.Is(err, manager.ErrNoProvider) {
		t.Fatalf("want ErrNoProvider, got %v\n%s", err, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("TAB ROUTER READY")) {
		t.Fatal("READY printed without a provider")
	}
}
