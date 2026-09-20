// Package browser launches one hardened Chromium process per identity and
// drives it over the DevTools protocol.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// EnvBinary overrides Chromium discovery (development and CI).
const EnvBinary = "TAB_ROUTER_CHROMIUM"

// PinManifest mirrors browser/chromium/pin.json.
type PinManifest struct {
	Version   string                 `json:"version"`
	Source    string                 `json:"source"`
	Platforms map[string]PinPlatform `json:"platforms"`
}

// PinPlatform is one downloadable build.
type PinPlatform struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Binary is the executable path inside the extracted archive.
	Binary string `json:"binary"`
}

// PlatformKey returns the pin.json key for the running OS/arch.
func PlatformKey() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "win64"
	case "darwin/arm64":
		return "mac-arm64"
	case "darwin/amd64":
		return "mac-x64"
	case "linux/amd64":
		return "linux64"
	case "linux/arm64":
		return "linux-arm64"
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

// Found describes the Chromium binary selected for launch.
type Found struct {
	Path   string
	Source string // "env", "pinned", "bundled", "system"
}

// Find locates a Chromium binary. Order: TAB_ROUTER_CHROMIUM, the pinned
// build under dataDir, a bundle beside the executable, then well-known
// system installs (development convenience; the version is always printed).
func Find(dataDir string, pin *PinManifest) (Found, error) {
	if v := os.Getenv(EnvBinary); v != "" {
		if _, err := os.Stat(v); err != nil {
			return Found{}, fmt.Errorf("%s=%q: %w", EnvBinary, v, err)
		}
		return Found{Path: v, Source: "env"}, nil
	}
	if pin != nil {
		if p, ok := pin.Platforms[PlatformKey()]; ok && dataDir != "" {
			cand := filepath.Join(dataDir, "chromium", pin.Version, filepath.FromSlash(p.Binary))
			if _, err := os.Stat(cand); err == nil {
				return Found{Path: cand, Source: "pinned"}, nil
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		for _, rel := range bundledCandidates() {
			cand := filepath.Join(filepath.Dir(exe), rel)
			if _, err := os.Stat(cand); err == nil {
				return Found{Path: cand, Source: "bundled"}, nil
			}
		}
	}
	for _, c := range systemCandidates() {
		if strings.ContainsRune(c, os.PathSeparator) {
			if _, err := os.Stat(c); err == nil {
				return Found{Path: c, Source: "system"}, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return Found{Path: p, Source: "system"}, nil
		}
	}
	return Found{}, errors.New("no Chromium found: run scripts/fetch-chromium or set " + EnvBinary)
}

func bundledCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{`chromium\chrome.exe`}
	case "darwin":
		return []string{
			"../Frameworks/Chromium.app/Contents/MacOS/Chromium",
			"chromium/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		}
	default:
		return []string{"chromium/chrome"}
	}
}

func systemCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		pf := os.Getenv("ProgramFiles")
		pf86 := os.Getenv("ProgramFiles(x86)")
		local := os.Getenv("LOCALAPPDATA")
		return []string{
			filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(local, `Chromium\Application\chrome.exe`),
		}
	case "darwin":
		return []string{
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		}
	default:
		return []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"}
	}
}

// LoadPin reads a pin.json manifest.
func LoadPin(path string) (*PinManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m PinManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("pin manifest %s: %w", path, err)
	}
	return &m, nil
}

// Version runs the binary with --product-version (then --version) and
// returns the trimmed output. Chrome for Testing on Windows can hang
// forever on --version, so the call is bounded and the window is hidden.
func Version(binary string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	try := func(flag string) (string, error) {
		cmd := exec.CommandContext(ctx, binary, flag)
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	if s, err := try("--product-version"); err == nil && s != "" {
		return s, nil
	}
	return try("--version")
}

// HardeningFlags is the flag set every identity runs with. Each flag is
// documented in docs/chromium-flags.md; change both together.
func HardeningFlags(gateAddr string) []string {
	return []string{
		// The gate is the only proxy, with no DIRECT alternative, and loopback
		// is not implicitly bypassed.
		"--proxy-server=socks5://" + gateAddr,
		"--proxy-bypass-list=<-loopback>",
		// Never resolve names on the host; send them to the gate unresolved.
		"--host-resolver-rules=MAP * ~NOTFOUND , EXCLUDE 127.0.0.1",
		// No UDP paths out of the browser.
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-features=DnsOverHttps,AsyncDns,OptimizationHints,MediaRouter,Translate,InterestFeedContentSuggestions,CalculateNativeWinOcclusion,SafeBrowsingEnhancedProtection,ChromeWhatsNewUI,PrivacySandboxSettings4,SegmentationPlatform,AutofillServerCommunication",
		// Reduce non-user traffic that would correlate identities.
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--disable-client-side-phishing-detection",
		"--disable-search-engine-choice-screen",
		"--disable-breakpad",
		"--metrics-recording-only",
		"--no-pings",
		"--no-first-run",
		"--no-default-browser-check",
		"--no-service-autorun",
		"--password-store=basic",
		"--use-mock-keychain",
		// Control channel: inherited pipe, never a TCP port.
		"--remote-debugging-pipe",
	}
}
