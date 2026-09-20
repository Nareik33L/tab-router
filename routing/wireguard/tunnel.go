package wireguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Config describes one userspace tunnel. Keys are hex or base64.
type Config struct {
	PrivateKey    string
	PeerPublicKey string
	Endpoint      string // host:port of the peer
	LocalAddress  string // tunnel IPv4, with or without /32
	LocalAddress6 string // optional tunnel IPv6, with or without /128
	DNS           string // comma-separated IPs; first is used for hostname Dial
	MTU           int
	ListenPort    int // 0 = ephemeral; tests pin this so two stacks can peer
}

// Tunnel is a live userspace WireGuard device.
type Tunnel struct {
	cfg  Config
	dev  *device.Device
	tnet *netstack.Net

	mu     sync.Mutex
	closed bool
}

// Dialer-like: *Tunnel implements DialContext.
func (c Config) uapi() (string, netip.Addr, []netip.Addr, int, error) {
	priv, err := ParseKey(c.PrivateKey)
	if err != nil {
		return "", netip.Addr{}, nil, 0, fmt.Errorf("private key: %w", err)
	}
	pub, err := ParseKey(c.PeerPublicKey)
	if err != nil {
		return "", netip.Addr{}, nil, 0, fmt.Errorf("peer key: %w", err)
	}
	if _, _, err := net.SplitHostPort(c.Endpoint); err != nil {
		return "", netip.Addr{}, nil, 0, fmt.Errorf("endpoint: %w", err)
	}
	local := strings.TrimSpace(c.LocalAddress)
	if pfx, err := netip.ParsePrefix(local); err == nil {
		local = pfx.Addr().String()
	}
	laddr, err := parseAddr(local)
	if err != nil {
		return "", netip.Addr{}, nil, 0, fmt.Errorf("local address: %w", err)
	}
	var dns []netip.Addr
	for _, p := range strings.Split(c.DNS, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		a, err := netip.ParseAddr(p)
		if err != nil {
			return "", netip.Addr{}, nil, 0, fmt.Errorf("dns: %w", err)
		}
		dns = append(dns, a)
	}
	if len(dns) == 0 {
		dns = []netip.Addr{netip.MustParseAddr("10.64.0.1")}
	}
	mtu := c.MTU
	if mtu <= 0 {
		mtu = 1420
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", priv.Hex())
	if c.ListenPort > 0 {
		fmt.Fprintf(&b, "listen_port=%d\n", c.ListenPort)
	}
	fmt.Fprintf(&b, "public_key=%s\n", pub.Hex())
	fmt.Fprintf(&b, "endpoint=%s\n", c.Endpoint)
	b.WriteString("allowed_ip=0.0.0.0/0\n")
	b.WriteString("allowed_ip=::/0\n")
	b.WriteString("persistent_keepalive_interval=25\n")
	return b.String(), laddr, dns, mtu, nil
}

func parseAddr(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	if pfx, err := netip.ParsePrefix(s); err == nil {
		return pfx.Addr(), nil
	}
	return netip.ParseAddr(s)
}

// Open brings up a userspace tunnel. The caller must Close it.
func Open(cfg Config) (*Tunnel, error) {
	uapi, laddr, dns, mtu, err := cfg.uapi()
	if err != nil {
		return nil, err
	}
	locals := []netip.Addr{laddr}
	if cfg.LocalAddress6 != "" {
		a6, err := parseAddr(cfg.LocalAddress6)
		if err != nil {
			return nil, fmt.Errorf("local address6: %w", err)
		}
		locals = append(locals, a6)
	}
	tun, tnet, err := netstack.CreateNetTUN(locals, dns, mtu)
	if err != nil {
		return nil, fmt.Errorf("netstack: %w", err)
	}
	// StdNetBind on every OS: Windows DefaultBind is WinRingBind, which
	// cannot loop two devices in one process (CI) and is unnecessary for a
	// netstack client that is not using wintun.
	dev := device.NewDevice(tun, conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard configure: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard up: %w", err)
	}
	return &Tunnel{cfg: cfg, dev: dev, tnet: tnet}, nil
}

// DialContext opens a TCP connection through the tunnel. Hostnames are
// resolved by the tunnel's DNS, never by the host resolver.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("wireguard: %s not supported (CONNECT-only)", network)
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("wireguard: tunnel closed")
	}
	return t.tnet.DialContext(ctx, network, address)
}

// WaitHandshake blocks until a handshake completes or ctx is done.
func (t *Tunnel) WaitHandshake(ctx context.Context) error {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if sec, _ := t.lastHandshake(); sec > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wireguard: handshake timed out")
		case <-tick.C:
		}
	}
}

// LastHandshake is the unix seconds of the last successful handshake, or 0.
func (t *Tunnel) lastHandshake() (int64, error) {
	s, err := t.dev.IpcGet()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && k == "last_handshake_time_sec" {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n, nil
		}
	}
	return 0, nil
}

// Alive reports a handshake in the last three minutes.
func (t *Tunnel) Alive() bool {
	sec, err := t.lastHandshake()
	if err != nil || sec == 0 {
		return false
	}
	return time.Since(time.Unix(sec, 0)) < 3*time.Minute
}

// ListenTCP binds a TCP listener on the tunnel's netstack (tests).
func (t *Tunnel) ListenTCP(addr *net.TCPAddr) (net.Listener, error) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("wireguard: tunnel closed")
	}
	return t.tnet.ListenTCP(addr)
}

// SetPeerEndpoint updates the configured peer's UDP endpoint (used by tests
// after both devices have bound an ephemeral listen port).
func (t *Tunnel) SetPeerEndpoint(hostport string) error {
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}
	pub, err := ParseKey(t.cfg.PeerPublicKey)
	if err != nil {
		return err
	}
	return t.dev.IpcSet(fmt.Sprintf("public_key=%s\nendpoint=%s\n", pub.Hex(), hostport))
}

// ListenPort is the UDP port the device bound, or 0.
func (t *Tunnel) ListenPort() (int, error) {
	s, err := t.dev.IpcGet()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && k == "listen_port" {
			n, _ := strconv.Atoi(v)
			return n, nil
		}
	}
	return 0, nil
}

// Close tears the device down.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	t.dev.Close()
	return nil
}
