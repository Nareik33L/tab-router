package isolation_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/controller/startup"
	"github.com/Nareik33L/tab-router/controller/verify"
	"github.com/Nareik33L/tab-router/tests/infra"
)

// TestProtocolCoverage is T-J: redirects, WebSockets, downloads and service
// workers all traverse the identity's own route, and never the other one.
func TestProtocolCoverage(t *testing.T) {
	h := newHarness(t)
	ctx := ctxT(t)
	cfg := h.config(2, "", false)
	sess, err := startup.Run(ctx, cfg, startup.NewReporter(h.out, true), startup.Options{
		RouteDefs: h.defs,
		// Service workers need a secure context; the test origin is plain
		// HTTP on a fake hostname.
		ChromiumExtraArgs: []string{"--unsafely-treat-insecure-origin-as-secure=" + strings.TrimSuffix(h.in.EchoURL(), "/")},
	})
	if err != nil {
		t.Fatalf("startup failed: %v\n%s", err, h.out)
	}
	defer sess.Shutdown(ctx)

	want := map[int]string{0: "127.0.0.2", 1: "127.0.0.3"}
	for i, sub := range sess.Subjects() {
		t.Run(fmt.Sprintf("identity-%03d", i+1), func(t *testing.T) {
			testProtocols(t, h, sub, want[i])
		})
	}

	// Cross-check: every hit the echo server saw for these paths came from
	// exactly the two route IPs, never the host.
	for _, hit := range h.in.Hits() {
		if hit.ClientIP != "127.0.0.2" && hit.ClientIP != "127.0.0.3" && hit.ClientIP != "127.0.0.1" {
			t.Errorf("unexpected client %s for %s", hit.ClientIP, hit.Path)
		}
		if hit.ClientIP == "127.0.0.1" && hit.Path != "/" {
			// The controller's own host-IP probe hits "/" directly; nothing
			// else may.
			t.Errorf("host connection observed for %s", hit.Path)
		}
	}
}

func testProtocols(t *testing.T, h *harness, sub *verify.Subject, wantIP string) {
	ctx := ctxT(t)
	page, err := sub.Browser.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(ctx)
	base := h.in.EchoURL()

	// Redirect chain: 3 hops, final body must carry the route IP.
	res, err := page.Navigate(ctx, base+"redirect/3")
	if err != nil || res.Blocked() {
		t.Fatalf("redirect navigate: %v %+v", err, res)
	}
	body, _ := page.BodyText(ctx)
	if !strings.Contains(body, wantIP) {
		t.Errorf("redirect landed with body %q, want %s", body, wantIP)
	}

	// WebSocket: server replies "<client-ip>:<payload>".
	var wsReply string
	js := fmt.Sprintf(`new Promise((ok, bad) => {
		const ws = new WebSocket(%q);
		ws.onopen = () => ws.send("hello");
		ws.onmessage = e => { ok(e.data); ws.close(); };
		ws.onerror = e => bad("ws error");
		setTimeout(() => bad("ws timeout"), 8000);
	})`, "ws://"+infra.EchoHost+":"+fmt.Sprint(h.in.EchoPort())+"/ws")
	if err := page.Evaluate(ctx, js, &wsReply); err != nil {
		t.Fatalf("websocket: %v", err)
	}
	if wsReply != wantIP+":hello" {
		t.Errorf("websocket reply %q, want %q", wsReply, wantIP+":hello")
	}

	// Download: lands in the identity's own download dir, fetched via route.
	before := listDir(sub.Identity.DownloadPath())
	if err := page.Evaluate(ctx, fmt.Sprintf(`(() => { const a = document.createElement("a"); a.href = %q; a.download = "probe.txt"; document.body.appendChild(a); a.click(); return true; })()`, base+"download"), nil); err != nil {
		t.Fatalf("download click: %v", err)
	}
	var got string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && got == "" {
		for f := range listDir(sub.Identity.DownloadPath()) {
			if before[f] || strings.HasSuffix(f, ".crdownload") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(sub.Identity.DownloadPath(), f))
			if err == nil && len(b) > 0 {
				got = string(b)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(got, "client="+wantIP) {
		t.Errorf("download content %q, want client=%s in %s", got, wantIP, sub.Identity.DownloadPath())
	}

	// Service worker: install-time fetch of /sw-fetch must come from the
	// route IP.
	hitsBefore := len(h.in.Hits())
	res, err = page.Navigate(ctx, base+"sw")
	if err != nil || res.Blocked() {
		t.Fatalf("sw navigate: %v %+v", err, res)
	}
	var state string
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_ = page.Evaluate(ctx, `window.swState`, &state)
		if state == "activated" || strings.HasPrefix(state, "error") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if state != "activated" {
		t.Fatalf("service worker state %q", state)
	}
	found := false
	for _, hit := range h.in.Hits()[hitsBefore:] {
		if hit.Path == "/sw-fetch" {
			found = true
			if hit.ClientIP != wantIP {
				t.Errorf("service worker fetch from %s, want %s", hit.ClientIP, wantIP)
			}
		}
	}
	if !found {
		t.Errorf("service worker fetch never reached the echo server")
	}
}

func listDir(dir string) map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}
