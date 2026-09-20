package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetsPersistAndFresh(t *testing.T) {
	m := New(t.TempDir())
	s1, err := m.Open(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Lock(); err != nil {
		t.Fatal(err)
	}
	ids, err := s1.Ensure(2, func(i int) Environment {
		return NewEnvironment(i, EnvironmentDefaults{Locale: "en-GB", Timezone: "Europe/London"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids[0].RouteSlot != 1 || ids[1].RouteSlot != 2 || ids[0].Environment.WindowX == ids[1].Environment.WindowX {
		t.Fatalf("bad identities: %+v", ids)
	}
	// Second lock must fail while held.
	s1b, _ := m.Current()
	if err := s1b.Lock(); err == nil {
		t.Fatal("double lock succeeded")
	}
	s1.Unlock()

	// Reopen: same set, same environment even with different defaults.
	s2, _ := m.Open(false)
	if s2.Name != s1.Name {
		t.Fatalf("set changed: %s -> %s", s1.Name, s2.Name)
	}
	ids2, _ := s2.Ensure(2, func(i int) Environment {
		return NewEnvironment(i, EnvironmentDefaults{Locale: "de-DE", Timezone: "Europe/Berlin"})
	})
	if ids2[0].Environment != ids[0].Environment {
		t.Fatalf("environment was not pinned: %+v vs %+v", ids2[0].Environment, ids[0].Environment)
	}

	// Fresh: new set, old retained.
	s3, _ := m.Open(true)
	if s3.Name == s1.Name {
		t.Fatal("fresh reused set")
	}
	if _, err := os.Stat(filepath.Join(s1.Dir, "identity-001", "identity.json")); err != nil {
		t.Fatal("old set removed")
	}
	sets, _ := m.ListSets()
	if len(sets) != 2 {
		t.Fatalf("expected 2 sets, got %v", sets)
	}
}

func TestEnvironmentDefaultsAndValidation(t *testing.T) {
	e := NewEnvironment(1, EnvironmentDefaults{})
	if err := e.Validate(); err != nil {
		t.Fatalf("default environment invalid: %v (%+v)", err, e)
	}
	bad := e
	bad.Timezone = "Mars/Olympus"
	if err := bad.Validate(); err == nil {
		t.Fatal("bad timezone accepted")
	}
	bad = e
	bad.Locale = "english"
	if err := bad.Validate(); err == nil {
		t.Fatal("bad locale accepted")
	}
	bad = e
	bad.DownloadDir = "../elsewhere"
	if err := bad.Validate(); err == nil {
		t.Fatal("escaping download dir accepted")
	}
	if acceptLanguagesFor("pt-BR") != "pt-BR,pt" || acceptLanguagesFor("ja") != "ja" {
		t.Fatal("accept-language derivation wrong")
	}
}
