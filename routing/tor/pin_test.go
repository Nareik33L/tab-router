package tor

import (
	"testing"

	"github.com/Nareik33L/tab-router/controller/browser"
)

func TestPinCoversProductPlatforms(t *testing.T) {
	pin := Pin()
	if pin == nil || pin.Version == "" {
		t.Fatal("pin.json missing")
	}
	for _, key := range []string{"win64", "mac-arm64", "mac-x64", "linux64"} {
		p, ok := pin.Platforms[key]
		if !ok || p.URL == "" || p.SHA256 == "" || p.Binary == "" {
			t.Errorf("platform %s incomplete: %+v", key, p)
		}
	}
	if _, ok := pin.Platforms[browser.PlatformKey()]; !ok && browser.PlatformKey() != "linux-arm64" {
		t.Fatalf("running platform %s not pinned", browser.PlatformKey())
	}
}
