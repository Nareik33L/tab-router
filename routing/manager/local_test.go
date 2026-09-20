package manager

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/routing/tor"
)

func TestLocalProvisionMissingBinary(t *testing.T) {
	dir := t.TempDir()
	l := &Local{DataDir: dir, Binary: filepath.Join(dir, "no-such-tor")}
	_, err := l.Provision(context.Background(), 2)
	if err == nil {
		t.Fatal("expected missing-binary error")
	}
	if strings.Contains(err.Error(), "provider login") {
		t.Fatalf("must not mention login: %v", err)
	}
}

func TestLocalName(t *testing.T) {
	if (&Local{}).Name() != "tor" {
		t.Fatal((&Local{}).Name())
	}
}

func TestLocalReestablishKeepsCredentials(t *testing.T) {
	def := provider.RouteDef{
		ID:       "route-001",
		Type:     "socks5",
		Address:  "127.0.0.1:9050",
		Username: "identity-001-aaaaaa",
		Password: "bbbbbbbbbbbbbbbb",
	}
	l := &Local{slots: map[int]*localSlot{
		1: {daemon: &tor.Daemon{SocksAddr: "127.0.0.1:9050"}, def: def},
	}}
	r, err := l.Reestablish(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Def()
	if got.Username != def.Username || got.Password != def.Password || got.Address != def.Address {
		t.Fatalf("reestablish rotated session credentials: %+v", got)
	}
}
