//go:build linux

package browser

import "os"

// PlatformFlags are Linux-only extras. Linux is a CI/dev host: GitHub
// Actions (and any root process) cannot use Chromium's sandbox, and the
// runner's /dev/shm is too small for Chromium's default shared-memory usage.
func PlatformFlags() []string {
	flags := []string{"--disable-dev-shm-usage"}
	if os.Geteuid() == 0 || os.Getenv("CI") != "" || os.Getenv("TAB_ROUTER_NO_SANDBOX") != "" {
		flags = append(flags, "--no-sandbox", "--disable-setuid-sandbox")
	}
	return flags
}
