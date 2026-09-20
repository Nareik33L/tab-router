package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"http://example.com", "https://example.com/a?b=c", "http://127.0.0.1:8080/"} {
		if err := ValidateURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"example.com", "ftp://x", "javascript:alert(1)", "file:///etc/passwd", "http://"} {
		if err := ValidateURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestIdentityCap(t *testing.T) {
	c := Defaults()
	c.Identities = MaxIdentities + 1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("expected cap error, got %v", err)
	}
	c.Identities = 0
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for 0 identities")
	}
	c.Identities = 2
	c.FailClosed = false
	if err := c.Validate(); err == nil {
		t.Fatal("fail_closed=false must be rejected")
	}
}

func TestLoadMergesAndOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(`identities = 1
startup_url = "https://a.example"
[environment]
locale = "de-DE"
timezone = "Europe/Berlin"
window = "1000x700"
[[identity]]
index = 2
locale = "fr-FR"
`), 0o600)
	cfg, err := Load(Overrides{DataDir: dir, Identities: 2, URL: "https://b.example", URLSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Identities != 2 || cfg.StartupURL != "https://b.example" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	e1, err := cfg.EnvironmentFor(1)
	if err != nil {
		t.Fatal(err)
	}
	if e1.Locale != "de-DE" || e1.Timezone != "Europe/Berlin" || e1.WindowWidth != 1000 || e1.AcceptLanguages != "de-DE,de" {
		t.Errorf("env1 = %+v", e1)
	}
	e2, _ := cfg.EnvironmentFor(2)
	if e2.Locale != "fr-FR" || e2.Timezone != "Europe/Berlin" || e2.WindowX == e1.WindowX {
		t.Errorf("env2 = %+v", e2)
	}
	if cfg.Verify.HostEchoURL != cfg.Verify.IPEchoURL {
		t.Errorf("host_echo_url should default to ip_echo_url")
	}
}

func TestRoutesFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "routes.toml")
	body := "[[route]]\nid=\"r1\"\ntype=\"socks5\"\naddress=\"h:1\"\n"
	os.WriteFile(p, []byte(body), 0o644)
	if _, err := LoadRoutes(p); err == nil || !strings.Contains(err.Error(), "other users") {
		t.Fatalf("world-readable routes file accepted: %v", err)
	}
	os.Chmod(p, 0o600)
	defs, err := LoadRoutes(p)
	if err != nil || len(defs) != 1 {
		t.Fatalf("%v %v", defs, err)
	}
	os.WriteFile(p, []byte(body+body), 0o600)
	if _, err := LoadRoutes(p); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate ids accepted: %v", err)
	}
	if _, err := LoadRoutes(filepath.Join(dir, "missing.toml")); err == nil {
		t.Fatal("missing file accepted")
	}
}
