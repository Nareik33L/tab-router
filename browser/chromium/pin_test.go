package chromium

import (
	"strings"
	"testing"

	"github.com/Nareik33L/tab-router/controller/browser"
)

func TestPinIsOfficialChromium(t *testing.T) {
	pin := Pin()
	if pin == nil || pin.Version == "" {
		t.Fatal("pin.json missing")
	}
	if strings.Contains(strings.ToLower(pin.Source), "chrome for testing") {
		t.Fatalf("pin must be official Chromium snapshots, got source %q", pin.Source)
	}
	wantBinary := map[string]string{
		"win64":     "chrome-win/chrome.exe",
		"mac-arm64": "chrome-mac/Chromium.app/Contents/MacOS/Chromium",
		"mac-x64":   "chrome-mac/Chromium.app/Contents/MacOS/Chromium",
		"linux64":   "chrome-linux/chrome",
	}
	for key, binary := range wantBinary {
		p, ok := pin.Platforms[key]
		if !ok || p.URL == "" || p.SHA256 == "" || p.Binary == "" {
			t.Errorf("platform %s incomplete: %+v", key, p)
			continue
		}
		if p.Binary != binary {
			t.Errorf("platform %s binary %q, want %q", key, p.Binary, binary)
		}
		if !strings.Contains(p.URL, "chromium-browser-snapshots") {
			t.Errorf("platform %s URL is not a Chromium snapshot: %s", key, p.URL)
		}
		if strings.Contains(p.URL, "chrome-for-testing") {
			t.Errorf("platform %s still points at Chrome for Testing: %s", key, p.URL)
		}
	}
	if _, ok := pin.Platforms[browser.PlatformKey()]; !ok && browser.PlatformKey() != "linux-arm64" {
		t.Fatalf("running platform %s not pinned", browser.PlatformKey())
	}
}
