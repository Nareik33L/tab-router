//go:build darwin

package tor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var prepareMu sync.Mutex

// prepareExecutable clears Gatekeeper quarantine and ad-hoc signs tor plus
// its bundled dylibs. Apple silicon kills unsigned binaries with SIGKILL.
//
// Two identities share one Expert Bundle. Starting both daemons at once
// used to xattr/codesign the same libevent dylib in parallel, which fails
// with "replacing existing signature" / "No such file or directory".
func prepareExecutable(bin string) error {
	prepareMu.Lock()
	defer prepareMu.Unlock()
	if bundleSigned(bin) {
		return nil
	}
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
	return adhocSign(bin)
}

func bundleSigned(bin string) bool {
	if !codeSigned(bin) {
		return false
	}
	dir := filepath.Dir(bin)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".dylib") {
			continue
		}
		if !codeSigned(filepath.Join(dir, e.Name())) {
			return false
		}
	}
	return true
}

func adhocSign(path string) error {
	if codeSigned(path) {
		return nil
	}
	cmd := exec.Command("codesign", "--force", "--sign", "-", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign %s: %v: %s", filepath.Base(path), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func codeSigned(path string) bool {
	return exec.Command("codesign", "--verify", "--quiet", path).Run() == nil
}
