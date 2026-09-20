// Command fetch-chromium downloads the pinned Chromium build for this
// platform (or --platform), verifies its SHA-256 against
// browser/chromium/pin.json and extracts it under <data-dir>/chromium/<version>/
// where tab-router looks for it.
//
//	go run ./scripts/fetch-chromium                 # into the default data dir
//	go run ./scripts/fetch-chromium --dest dist/chromium --platform win64
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nareik33L/tab-router/browser/chromium"
	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/routing/platform"
)

func main() {
	dest := flag.String("dest", "", "destination directory (default: <data-dir>/chromium)")
	plat := flag.String("platform", browser.PlatformKey(), "pin.json platform key")
	flag.Parse()

	pin := chromium.Pin()
	if pin == nil {
		fail("embedded pin.json is invalid")
	}
	p, ok := pin.Platforms[*plat]
	if !ok {
		fail("no pinned build for platform %q", *plat)
	}
	if *dest == "" {
		d, err := platform.Current().DefaultDataDir()
		if err != nil {
			fail("%v", err)
		}
		*dest = filepath.Join(d, "chromium")
	}
	verDir := filepath.Join(*dest, pin.Version)
	bin := filepath.Join(verDir, filepath.FromSlash(p.Binary))
	if _, err := os.Stat(bin); err == nil {
		fmt.Printf("already present: %s\n", bin)
		return
	}
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		fail("%v", err)
	}

	zipPath := filepath.Join(verDir, filepath.Base(p.URL))
	fmt.Printf("downloading %s\n", p.URL)
	if err := download(p.URL, zipPath); err != nil {
		fail("download: %v", err)
	}
	sum, err := sha256File(zipPath)
	if err != nil {
		fail("%v", err)
	}
	if !strings.EqualFold(sum, p.SHA256) {
		_ = os.Remove(zipPath)
		fail("checksum mismatch for %s\n  expected %s\n  got      %s", filepath.Base(p.URL), p.SHA256, sum)
	}
	fmt.Printf("sha256 verified: %s\n", sum)
	if err := unzip(zipPath, verDir); err != nil {
		fail("extract: %v", err)
	}
	_ = os.Remove(zipPath)
	if _, err := os.Stat(bin); err != nil {
		fail("expected binary missing after extract: %s", bin)
	}
	fmt.Printf("installed %s %s\n  %s\n", pin.Version, *plat, bin)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fetch-chromium: "+format+"\n", args...)
	os.Exit(1)
}

func download(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	pr := &progress{total: resp.ContentLength}
	_, err = io.Copy(f, io.TeeReader(resp.Body, pr))
	fmt.Println()
	return err
}

type progress struct {
	total, done int64
	last        int64
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.done-p.last > 8<<20 || p.done == p.total {
		p.last = p.done
		if p.total > 0 {
			fmt.Printf("\r  %3d%% (%d MB)", p.done*100/p.total, p.done>>20)
		} else {
			fmt.Printf("\r  %d MB", p.done>>20)
		}
	}
	return len(b), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unzip extracts preserving permissions and symlinks (macOS app bundles
// contain framework symlinks), refusing paths that escape dest.
func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	destAbs, _ := filepath.Abs(dest)
	for _, f := range r.File {
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		if abs, _ := filepath.Abs(target); !strings.HasPrefix(abs, destAbs+string(os.PathSeparator)) && abs != destAbs {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			link, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(string(link), target); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			perm := mode.Perm()
			if perm == 0 {
				perm = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, rc)
			rc.Close()
			out.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
