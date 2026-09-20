// Package tor downloads a pinned Tor Expert Bundle and runs a local tor
// daemon so Tab Router can provision isolated network exits with no
// user account and no Tab Router login.
package tor

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Nareik33L/tab-router/controller/browser"
)

// EnvBinary overrides discovery (development and CI).
const EnvBinary = "TAB_ROUTER_TOR"

//go:embed pin.json
var pinJSON []byte

// PinManifest is the embedded expert-bundle pin.
type PinManifest struct {
	Version   string                 `json:"version"`
	Source    string                 `json:"source"`
	Platforms map[string]PinPlatform `json:"platforms"`
}

// PinPlatform is one downloadable expert bundle.
type PinPlatform struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Binary string `json:"binary"`
}

// Pin returns the embedded manifest.
func Pin() *PinManifest {
	var m PinManifest
	if err := json.Unmarshal(pinJSON, &m); err != nil {
		return nil
	}
	return &m
}

func platformKey() string { return browser.PlatformKey() }

func exeName() string {
	if runtime.GOOS == "windows" {
		return "tor.exe"
	}
	return "tor"
}

// Find returns a usable tor binary: TAB_ROUTER_TOR, a previously installed
// pin under dest, or empty path.
func Find(dest string) string {
	if p := os.Getenv(EnvBinary); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	pin := Pin()
	if pin == nil {
		return ""
	}
	p, ok := pin.Platforms[platformKey()]
	if !ok {
		return ""
	}
	bin := filepath.Join(dest, pin.Version, filepath.FromSlash(p.Binary))
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	return ""
}
