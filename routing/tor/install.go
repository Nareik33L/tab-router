package tor

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Install downloads the pinned expert bundle for this machine into dest
// (the tor root, typically <data-dir>/tor), verifies the SHA-256, and
// extracts it. If the binary is already present, Install is a no-op.
func Install(dest string, log io.Writer) (string, error) {
	if log == nil {
		log = io.Discard
	}
	if p := Find(dest); p != "" {
		return p, nil
	}
	pin := Pin()
	if pin == nil {
		return "", fmt.Errorf("embedded tor pin.json is invalid")
	}
	plat, ok := pin.Platforms[platformKey()]
	if !ok {
		return "", fmt.Errorf("no pinned Tor expert bundle for this platform")
	}
	verDir := filepath.Join(dest, pin.Version)
	bin := filepath.Join(verDir, filepath.FromSlash(plat.Binary))
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		return "", err
	}
	archive := filepath.Join(verDir, filepath.Base(plat.URL))
	fmt.Fprintf(log, "downloading Tor %s\n", pin.Version)
	if err := download(plat.URL, archive, log); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	sum, err := sha256File(archive)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(sum, plat.SHA256) {
		_ = os.Remove(archive)
		return "", fmt.Errorf("checksum mismatch for %s\n  expected %s\n  got      %s", filepath.Base(plat.URL), plat.SHA256, sum)
	}
	fmt.Fprintf(log, "sha256 verified: %s\n", sum)
	if err := untar(archive, verDir); err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	_ = os.Remove(archive)
	if _, err := os.Stat(bin); err != nil {
		if found := findNamed(verDir, exeName()); found != "" {
			bin = found
		} else {
			return "", fmt.Errorf("expected tor binary missing after extract: %s", bin)
		}
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return "", err
	}
	fmt.Fprintf(log, "installed Tor %s\n  %s\n", pin.Version, bin)
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
	if p.done-p.last > 8<<20 || (p.total > 0 && p.done == p.total) {
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

func untar(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			continue
		}
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dest, name)
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) && target != dest {
			return fmt.Errorf("refusing to extract %s outside %s", hdr.Name, dest)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)|0o200)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func findNamed(root, name string) string {
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == name {
			found = path
			return io.EOF
		}
		return nil
	})
	return found
}
