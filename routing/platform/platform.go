// Package platform isolates every OS-specific operation Tab Router needs:
// enumerating a process tree's network endpoints (the observational
// fail-closed layer), authenticated local IPC endpoints, owner-only file
// permissions, process-tree termination and default data directories.
//
// Linux is a development/CI convenience target; Windows and macOS are the
// supported product platforms.
package platform

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
)

// Endpoint is one socket owned by a process in the tree.
type Endpoint struct {
	PID    int
	Proto  string // "tcp", "tcp6", "udp", "udp6"
	Local  netip.AddrPort
	Remote netip.AddrPort // zero for unconnected UDP / listening TCP
	State  string         // TCP state name; "" for UDP
}

func (e Endpoint) String() string {
	if e.Remote.IsValid() && e.Remote.Port() != 0 {
		return fmt.Sprintf("%s pid=%d %s -> %s %s", e.Proto, e.PID, e.Local, e.Remote, e.State)
	}
	return fmt.Sprintf("%s pid=%d %s (unconnected) %s", e.Proto, e.PID, e.Local, e.State)
}

// Provider is the OS abstraction. Implementations live in platform_<os>.go.
type Provider interface {
	Name() string
	// ProcessTree returns root and all of its descendants.
	ProcessTree(root int) ([]int, error)
	// RemoteEndpoints returns every TCP socket (excluding LISTEN) and every UDP
	// socket owned by the given PIDs.
	RemoteEndpoints(pids []int) ([]Endpoint, error)
	// ListenIPC creates the controller's local IPC endpoint inside dir,
	// accessible only to the current user. It returns the listener and the
	// endpoint string clients pass to DialIPC.
	ListenIPC(dir, name string) (net.Listener, string, error)
	DialIPC(endpoint string) (net.Conn, error)
	// SecureFile restricts path to the owning user (0600 / owner-only DACL).
	SecureFile(path string) error
	// CheckFilePrivate returns an error if path is readable by others.
	CheckFilePrivate(path string) error
	// KillTree terminates root and all descendants.
	KillTree(root int) error
	// DefaultDataDir is the per-user application data directory.
	DefaultDataDir() (string, error)
}

// ErrUnsupported marks functionality not implemented for the current OS.
var ErrUnsupported = errors.New("platform: not supported on " + runtime.GOOS)

// Current returns the provider for the running OS.
func Current() Provider { return current }

func userDataDir(appName string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, appName), nil
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", appName), nil
	default:
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return filepath.Join(v, appName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, appName), nil
}

// TCP state names shared by the /proc and iphlpapi decoders.
var tcpStates = map[int]string{
	1: "ESTABLISHED", 2: "SYN_SENT", 3: "SYN_RECV", 4: "FIN_WAIT1", 5: "FIN_WAIT2",
	6: "TIME_WAIT", 7: "CLOSE", 8: "CLOSE_WAIT", 9: "LAST_ACK", 10: "LISTEN", 11: "CLOSING",
}
