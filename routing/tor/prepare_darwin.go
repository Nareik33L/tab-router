//go:build darwin

package tor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// prepareExecutable clears Gatekeeper quarantine and ad-hoc signs tor plus
// its bundled dylibs. Apple silicon kills unsigned binaries with SIGKILL.
func prepareExecutable(bin string) error {
	dir := filepath.Dir(bin)
	_ = exec.Command("xattr", "-cr", dir).Run()
	if parent := filepath.Dir(dir); parent != "" {
		_ = exec.Command("xattr", "-cr", parent).Run()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".dylib") {
			if err := adhocSign(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := adhocSign(bin); err != nil {
		return err
	}
	return nil
}

func adhocSign(path string) error {
	if exec.Command("codesign", "--verify", "--quiet", path).Run() == nil {
		return nil
	}
	cmd := exec.Command("codesign", "--force", "--sign", "-", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign %s: %v: %s", filepath.Base(path), err, strings.TrimSpace(string(out)))
	}
	return nil
}
