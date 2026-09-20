package tor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
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

func TestParseSocksListeners(t *testing.T) {
	got := parseSocksListeners(`net/listeners/socks="127.0.0.1:9050" "127.0.0.1:9051"`)
	if len(got) != 2 || got[0] != "127.0.0.1:9050" || got[1] != "127.0.0.1:9051" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseCircuitExit(t *testing.T) {
	const (
		a = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		b = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
		c = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
		d = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
		e = "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
		f = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
	)
	status := strings.Join([]string{
		"1 EXTENDED $" + a + " SOCKS_USERNAME=\"identity-001-abc\"",
		"2 BUILT $" + a + "~g1,$" + b + "~g2,$" + c + "~exit BUILD_FLAGS=NEED_CAPACITY PURPOSE=GENERAL SOCKS_USERNAME=\"identity-001-abc\" SOCKS_PASSWORD=\"x\"",
		"3 BUILT $" + d + "~g1,$" + e + "~g2,$" + f + "~exit PURPOSE=GENERAL SOCKS_USERNAME=\"identity-002-def\" SOCKS_PASSWORD=\"y\"",
		"4 BUILT $" + a + " PURPOSE=HS_CLIENT_HSDIR",
	}, "\n")
	fp, err := parseCircuitExit(status, "identity-001-abc")
	if err != nil || fp != c {
		t.Fatalf("id 001: got %q %v", fp, err)
	}
	fp, err = parseCircuitExit(status, "identity-002-def")
	if err != nil || fp != f {
		t.Fatalf("id 002: got %q %v", fp, err)
	}
	if _, err := parseCircuitExit(status, "identity-009-zzz"); err == nil {
		t.Fatal("expected missing-user error")
	}
}

func TestControlCmdDataReply(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		_, _ = io.WriteString(server, "250+circuit-status=\r\n")
		_, _ = io.WriteString(server, "2 BUILT $AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA,$BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB SOCKS_USERNAME=\"identity-001-abc\"\r\n")
		_, _ = io.WriteString(server, ".\r\n")
		_, _ = io.WriteString(server, "250 OK\r\n")
	}()
	r := bufio.NewReader(client)
	out, err := controlCmd(client, r, "GETINFO circuit-status")
	if err != nil {
		t.Fatal(err)
	}
	fp, err := parseCircuitExit(out, "identity-001-abc")
	if err != nil || fp != "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Fatalf("got %q %v from %q", fp, err, out)
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
