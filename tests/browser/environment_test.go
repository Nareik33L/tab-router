package browser_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/controller/identity"
	"github.com/Nareik33L/tab-router/routing/provider"
	"github.com/Nareik33L/tab-router/tests/infra"
	"github.com/Nareik33L/tab-router/tests/testutil"
)

// TestEnvironmentApplied checks that the pinned environment (locale,
// timezone, window geometry) is what the page observes, and that a second
// launch of the same profile observes the same values.
func TestEnvironmentApplied(t *testing.T) {
	bin := testutil.ChromiumOrSkip(t)
	in, err := infra.Start(infra.Options{Proxies: []string{"socks5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	route, _ := provider.New(provider.RouteDef{ID: "r", Type: "socks5", Address: in.Proxies[0].Addr})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := route.Start(ctx); err != nil {
		t.Fatal(err)
	}
	gate := provider.NewGate(route)
	addr, _ := gate.Listen()
	defer gate.Shutdown()
	gate.Open()

	env := identity.NewEnvironment(2, identity.EnvironmentDefaults{Locale: "de-DE", Timezone: "Asia/Tokyo", WindowWidth: 1024, WindowHeight: 700})
	profile := t.TempDir()

	observe := func() (lang, tz string, w, h int) {
		b, err := browser.Launch(ctx, browser.LaunchOptions{Binary: bin, ProfileDir: profile, GateAddr: addr, Headless: true, Environment: env, DownloadDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer b.Kill()
		page, err := b.FirstPage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := page.Navigate(ctx, in.EchoURL()+"store"); err != nil || res.Blocked() {
			t.Fatalf("navigate: %v %+v", err, res)
		}
		var out struct {
			Lang string `json:"lang"`
			TZ   string `json:"tz"`
			W    int    `json:"w"`
			H    int    `json:"h"`
		}
		expr := `({lang: navigator.language, tz: Intl.DateTimeFormat().resolvedOptions().timeZone, w: window.outerWidth, h: window.outerHeight})`
		if err := page.Evaluate(ctx, expr, &out); err != nil {
			t.Fatal(err)
		}
		return out.Lang, out.TZ, out.W, out.H
	}

	lang1, tz1, w1, h1 := observe()
	if lang1 != "de-DE" {
		t.Errorf("navigator.language = %q, want de-DE", lang1)
	}
	if tz1 != "Asia/Tokyo" {
		t.Errorf("timezone = %q, want Asia/Tokyo", tz1)
	}
	if w1 != 1024 || h1 != 700 {
		t.Errorf("window = %dx%d, want 1024x700", w1, h1)
	}
	lang2, tz2, w2, h2 := observe()
	if lang2 != lang1 || tz2 != tz1 || w2 != w1 || h2 != h1 {
		t.Errorf("environment changed across restart: %v/%v/%dx%d vs %v/%v/%dx%d", lang1, tz1, w1, h1, lang2, tz2, w2, h2)
	}
}
