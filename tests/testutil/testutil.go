// Package testutil holds helpers shared by the test suites.
package testutil

import (
	"os"
	"testing"

	"github.com/Nareik33L/tab-router/controller/browser"
)

// ChromiumOrSkip returns a Chromium binary path or skips the test when none
// is available. Set TAB_ROUTER_CHROMIUM to point at a specific build.
func ChromiumOrSkip(t *testing.T) string {
	t.Helper()
	if os.Getenv("TAB_ROUTER_REQUIRE_CHROMIUM") == "" {
		f, err := browser.Find("", nil)
		if err != nil {
			t.Skipf("no Chromium available: %v (set TAB_ROUTER_CHROMIUM or TAB_ROUTER_REQUIRE_CHROMIUM=1)", err)
		}
		return f.Path
	}
	f, err := browser.Find("", nil)
	if err != nil {
		t.Fatal(err)
	}
	return f.Path
}
