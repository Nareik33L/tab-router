package browser

import "testing"

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
	if transientNavError("net::ERR_PROXY_CONNECTION_FAILED") {
		t.Fatal("proxy refuse must not be treated as a transient retry")
	}
}
