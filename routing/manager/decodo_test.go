package manager

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestDecodoUsername(t *testing.T) {
	const password = "sentinel-password-should-not-appear"
	user, err := Username("account", "GB", "abc123", 1440)
	if err != nil {
		t.Fatal(err)
	}
	want := "user-account-country-gb-session_iplock-abc123-sessionduration-1440"
	if user != want {
		t.Fatalf("got %s", user)
	}
	if strings.Contains(user, password) {
		t.Fatal("username contained a password")
	}
	plain, err := Username("user-account", "", "abc123", 10)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "user-account-session_iplock-abc123-sessionduration-10" {
		t.Fatalf("got %s", plain)
	}
	if _, err := Username("ac-count", "gb", "abc", 10); err == nil {
		t.Fatal("hyphenated account must be rejected")
	}
	if _, err := Username("account", "gbr", "abc", 10); err == nil {
		t.Fatal("3-letter country must be rejected")
	}
	if _, err := Username("account", "gb", "abc", 1441); err == nil {
		t.Fatal("duration above 1440 must be rejected")
	}
}

func TestDecodoProvisionAnyCount(t *testing.T) {
	const password = "sentinel-password-should-not-appear"
	dir := t.TempDir()
	var log bytes.Buffer
	d, err := NewDecodo(DecodoConfig{
		Account:        "account",
		Password:       password,
		Country:        "gb",
		SessionMinutes: 1440,
		StatePath:      decodoStatePath(dir),
		Log:            &log,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	routes, err := d.Provision(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 {
		t.Fatalf("got %d routes", len(routes))
	}
	seen := map[string]bool{}
	for i, r := range routes {
		def := r.Def()
		if def.Type != "socks5" || def.Address != "gate.decodo.com:7000" {
			t.Fatalf("route %d: %+v", i, def)
		}
		if def.Password != password {
			t.Fatalf("route %d lost its password", i)
		}
		if strings.Contains(def.Username, password) || strings.Contains(log.String(), password) {
			t.Fatalf("credential leaked: user=%s log=%s", def.Username, log.String())
		}
		if !strings.Contains(def.Username, "-country-gb-session_iplock-") || !strings.HasSuffix(def.Username, "-sessionduration-1440") {
			t.Fatalf("username %s", def.Username)
		}
		id := d.SessionID(i + 1)
		if id == "" || seen[id] {
			t.Fatalf("session %d id %q", i+1, id)
		}
		seen[id] = true
		redacted := def.Redacted()
		if strings.Contains(redacted, password) {
			t.Fatalf("redacted route contains password: %s", redacted)
		}
	}
	if strings.Count(log.String(), "decodo route ") != 4 {
		t.Fatalf("log %q", log.String())
	}

	again, err := d.Provision(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := range again {
		if again[i].Def().Username != routes[i].Def().Username {
			t.Fatalf("slot %d minted a new session on reprovision", i+1)
		}
	}
}

func TestDecodoReestablishKeepsSession(t *testing.T) {
	d, err := NewDecodo(DecodoConfig{Account: "account", Password: "pw", Country: "us", SessionMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	routes, err := d.Provision(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	before := map[int]string{}
	for slot := 1; slot <= 3; slot++ {
		before[slot] = d.SessionID(slot)
	}
	got, err := d.Reestablish(ctx, 2, routes[1])
	if err != nil {
		t.Fatal(err)
	}
	if d.SessionID(2) != before[2] || got.Def().Username != routes[1].Def().Username {
		t.Fatalf("reestablish rotated slot 2: %s -> %s", before[2], d.SessionID(2))
	}
	if d.SessionID(1) != before[1] || d.SessionID(3) != before[3] {
		t.Fatal("reestablish touched another slot")
	}
	replaced, err := d.ReplaceSession(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d.SessionID(2) == before[2] || replaced.Def().Username == routes[1].Def().Username {
		t.Fatal("ReplaceSession kept the old session")
	}
	if d.SessionID(1) != before[1] {
		t.Fatal("ReplaceSession touched another slot")
	}
}

func TestDecodoSessionExpires(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDecodo(DecodoConfig{
		Account: "account", Password: "pw", SessionMinutes: 10,
		StatePath: decodoStatePath(dir),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := d.Provision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	first := d.SessionID(1)
	d.mu.Lock()
	st := d.slots[1]
	st.Created = time.Now().Add(-11 * time.Minute)
	d.slots[1] = st
	if err := d.save(); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	d.slots = map[int]decodoSlot{}
	d.mu.Unlock()

	if _, err := d.Provision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if d.SessionID(1) == "" || d.SessionID(1) == first {
		t.Fatalf("expired session was reused: %s", d.SessionID(1))
	}
}

func TestResolveDecodoDoesNotFallBack(t *testing.T) {
	t.Setenv("DECODO_USERNAME", "")
	t.Setenv("DECODO_PASSWORD", "")
	dir := t.TempDir()
	_, name, err := Open(context.Background(), Input{
		DataDir:  dir,
		Provider: "decodo",
		N:        4,
	})
	if err == nil {
		t.Fatal("expected missing-credential error")
	}
	if name == "tor" || name == "decodo" {
		t.Fatalf("fell through to %s: %v", name, err)
	}
	if !strings.Contains(err.Error(), "decodo") {
		t.Fatalf("error should name decodo: %v", err)
	}
}
