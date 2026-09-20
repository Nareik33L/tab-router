package chromium

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nareik33L/tab-router/controller/browser"
)

// Install downloads the pinned Chromium for platformKey (empty = this
// machine), verifies the SHA-256, and extracts it under dest. dest is the
// chromium root (…/chromium); the binary lands at dest/<version>/<binary>.
// If the binary is already present, Install is a no-op. log may be nil.
func Install(dest, platformKey string, log io.Writer) (string, error) {
	if log == nil {
		log = io.Discard
	}
	pin := Pin()
	if pin == nil {
		return "", fmt.Errorf("embedded pin.json is invalid")
	}
	if platformKey == "" {
		platformKey = browser.PlatformKey()
	}
	p, ok := pin.Platforms[platformKey]
	if !ok {
		return "", fmt.Errorf("no pinned Chromium for platform %q", platformKey)
	}
	verDir := filepath.Join(dest, pin.Version)
	bin := filepath.Join(verDir, filepath.FromSlash(p.Binary))
	if _, err := os.Stat(bin); err == nil {
		if err := prepareExecutable(bin); err != nil {
			return "", err
		}
		fmt.Fprintf(log, "already present: %s\n", bin)
		return bin, nil
	}
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(verDir, filepath.Base(p.URL))
	fmt.Fprintf(log, "downloading %s\n", p.URL)
	if err := download(p.URL, zipPath, log); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	sum, err := sha256File(zipPath)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(sum, p.SHA256) {
		_ = os.Remove(zipPath)
		return "", fmt.Errorf("checksum mismatch for %s\n  expected %s\n  got      %s", filepath.Base(p.URL), p.SHA256, sum)
	}
	fmt.Fprintf(log, "sha256 verified: %s\n", sum)
	if err := unzip(zipPath, verDir); err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	_ = os.Remove(zipPath)
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("expected binary missing after extract: %s", bin)
	}
	if err := prepareExecutable(bin); err != nil {
		return "", err
	}
	fmt.Fprintf(log, "installed Chromium snapshot %s %s\n  %s\n", pin.Version, platformKey, bin)
	return bin, nil
}

func download(url, dest string, log io.Writer) error {
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
	pr := &progress{total: resp.ContentLength, w: log}
	_, err = io.Copy(f, io.TeeReader(resp.Body, pr))
	fmt.Fprintln(log)
	return err
}

type progress struct {
	total, done, last int64
	w                 io.Writer
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.done-p.last > 8<<20 || p.done == p.total {
		p.last = p.done
		if p.total > 0 {
			fmt.Fprintf(p.w, "\r  %3d%% (%d MB)", p.done*100/p.total, p.done>>20)
		} else {
			fmt.Fprintf(p.w, "\r  %d MB", p.done>>20)
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
