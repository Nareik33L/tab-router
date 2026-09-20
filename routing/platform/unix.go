//go:build !windows

package platform

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// unixIPC implements Unix-domain-socket IPC with owner-only permissions.
type unixIPC struct{}

func (unixIPC) ListenIPC(dir, name string) (net.Listener, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, name+".sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, "", err
	}
	return ln, "unix:" + path, nil
}

func (unixIPC) DialIPC(endpoint string) (net.Conn, error) {
	path := strings.TrimPrefix(endpoint, "unix:")
	return net.Dial("unix", path)
}

func (unixIPC) SecureFile(path string) error { return os.Chmod(path, 0o600) }

func (unixIPC) CheckFilePrivate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by other users (mode %04o); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	return nil
}

// killTree kills the process group if root leads one, then every PID in
// the tree individually as a fallback.
func killTree(p Provider, root int) error {
	pids, _ := p.ProcessTree(root)
	if pgid, err := syscall.Getpgid(root); err == nil && pgid == root {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	for i := len(pids) - 1; i >= 0; i-- {
		_ = syscall.Kill(pids[i], syscall.SIGKILL)
	}
	if len(pids) == 0 {
		_ = syscall.Kill(root, syscall.SIGKILL)
	}
	return nil
}
