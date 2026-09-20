package tor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWithLibPathPrepends(t *testing.T) {
	env := withLibPath([]string{"PATH=/bin", "LD_LIBRARY_PATH=/old"}, "/opt/tor")
	var ld, dy string
	for _, e := range env {
		if strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			ld = e
		}
		if strings.HasPrefix(e, "DYLD_LIBRARY_PATH=") {
			dy = e
		}
	}
	if !strings.HasPrefix(ld, "LD_LIBRARY_PATH=/opt/tor") || !strings.Contains(ld, "/old") {
		t.Fatalf("LD_LIBRARY_PATH: %s", ld)
	}
	if dy != "DYLD_LIBRARY_PATH=/opt/tor" {
		t.Fatalf("DYLD_LIBRARY_PATH: %s", dy)
	}
}

func TestParseSocksListener(t *testing.T) {
	addr, err := parseSocksListener(`net/listeners/socks="127.0.0.1:9050"`)
	if err != nil || addr != "127.0.0.1:9050" {
		t.Fatalf("got %q %v", addr, err)
	}
	if _, err := parseSocksListener("nothing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseCookieFile(t *testing.T) {
	info := "PROTOCOLINFO 1\nAUTH METHODS=COOKIE,SAFECOOKIE COOKIEFILE=\"/Users/me/Library/Application Support/tab-router/tor/session/control_auth_cookie\"\nVERSION Tor=\"0.4.9.11\"\n"
	got := parseCookieFile(info)
	want := "/Users/me/Library/Application Support/tab-router/tor/session/control_auth_cookie"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if parseCookieFile("AUTH METHODS=NULL") != "" {
		t.Fatal("expected empty path")
	}
}

func TestWaitAuthCookieStable32(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control_auth_cookie")
	want := bytes.Repeat([]byte{0xab}, cookieLen)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := waitAuthCookie(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x", got)
	}
}

func TestWaitAuthCookieIgnoresPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control_auth_cookie")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte{0xcd}, cookieLen)
	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = os.WriteFile(path, want, 0o600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := waitAuthCookie(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x", got)
	}
}

func TestResetSessionDropsStaleCookie(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "session")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(session, "control_auth_cookie")
	if err := os.WriteFile(stale, bytes.Repeat([]byte{1}, cookieLen), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resetSession(session); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale cookie still present: %v", err)
	}
}

func TestAuthCookieMismatch(t *testing.T) {
	if !authCookieMismatch(errString("515 Authentication failed: Authentication cookie did not match expected value.")) {
		t.Fatal("515 cookie mismatch")
	}
	if authCookieMismatch(errString("551 Invalid command")) {
		t.Fatal("other errors are not cookie mismatches")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
