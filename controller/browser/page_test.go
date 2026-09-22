package browser

import (
	"strings"
	"testing"
)

func TestIsErrorPageURL(t *testing.T) {
	yes := []string{
		"chrome-error://chromewebdata/",
		"chrome://network-error/-105",
		"https://example.com/chromewebdata",
	}
	no := []string{
		"https://api.ipify.org/",
		"https://example.com/",
		"",
	}
	for _, u := range yes {
		if !isErrorPageURL(u) {
			t.Errorf("isErrorPageURL(%q) = false", u)
		}
	}
	for _, u := range no {
		if isErrorPageURL(u) {
			t.Errorf("isErrorPageURL(%q) = true", u)
		}
	}
}

func TestTransientNavError(t *testing.T) {
	if !transientNavError("net::ERR_ABORTED") {
		t.Fatal("ERR_ABORTED should retry")
	}
	if !transientNavError("net::ERR_FAILED") {
		t.Fatal("ERR_FAILED should retry once (stale connection after gate close)")
	}
	if !transientNavError("net::ERR_SOCKS_CONNECTION_FAILED") {
		t.Fatal("ERR_SOCKS_CONNECTION_FAILED should retry (upstream blip)")
	}
	if transientNavError("net::ERR_PROXY_CONNECTION_FAILED") {
		t.Fatal("proxy refuse must not be treated as a transient retry")
	}
}

func TestScrubEnvDropsDecodoCredentials(t *testing.T) {
	out := scrubEnv([]string{
		"HOME=/tmp",
		"DECODO_USERNAME=account",
		"DECODO_PASSWORD=secret",
		"http_proxy=http://127.0.0.1:1",
		"PATH=/usr/bin",
	}, "Europe/London")
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "DECODO_") || strings.Contains(joined, "http_proxy") {
		t.Fatalf("credential or proxy leaked into browser env: %q", joined)
	}
	if !strings.Contains(joined, "HOME=/tmp") || !strings.Contains(joined, "TZ=Europe/London") {
		t.Fatalf("kept env: %q", joined)
	}
}

func TestHardeningFlagsAvoidKeychain(t *testing.T) {
	joined := strings.Join(HardeningFlags("127.0.0.1:1"), " ")
	for _, want := range []string{"--use-mock-keychain", "--password-store=basic", "OsCryptAsync", "AppBoundEncryption"} {
		if !strings.Contains(joined, want) {
			t.Errorf("hardening flags missing %s", want)
		}
	}
}
