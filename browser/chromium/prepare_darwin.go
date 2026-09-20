//go:build darwin

package chromium

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// prepareExecutable clears Gatekeeper quarantine and ad-hoc signs the
// Chromium.app bundle (or the binary if no bundle is found). Apple silicon
// kills unsigned snapshot builds with SIGKILL.
func prepareExecutable(bin string) error {
	target := appBundle(bin)
	if target == "" {
		target = bin
	}
	_ = exec.Command("xattr", "-cr", target).Run()
	// Re-signing changes the ad-hoc identity. macOS then prompts for the
	// login password so the new signature can use "Chromium Safe Storage".
	if exec.Command("codesign", "--verify", "--quiet", target).Run() == nil {
		return nil
	}
	cmd := exec.Command("codesign", "--force", "--deep", "--sign", "-", "--identifier", "org.chromium.Chromium", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign %s: %v: %s", filepath.Base(target), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func appBundle(bin string) string {
	p := bin
	for p != "" && p != string(filepath.Separator) {
		if strings.HasSuffix(p, ".app") {
			return p
		}
		next := filepath.Dir(p)
		if next == p {
			return ""
		}
		p = next
	}
	return ""
}
