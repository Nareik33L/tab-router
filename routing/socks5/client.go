package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"
)

// Dialer connects through an upstream SOCKS5 proxy. Hostnames are passed to
// the proxy unresolved (SOCKS5h semantics), so DNS happens at the upstream.
type Dialer struct {
	// ProxyAddr is the upstream proxy host:port.
	ProxyAddr string
	Username  string
	Password  string
	// Dial is used to reach the proxy itself. Defaults to net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout bounds the handshake. Zero means 30s.
	Timeout time.Duration
}

// DialContext opens a TCP connection to hostport via the proxy.
func (d *Dialer) DialContext(ctx context.Context, network, hostport string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("socks5: network %q not supported (TCP only)", network)
	}
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	dial := d.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", d.ProxyAddr)
	if err != nil {
		return nil, fmt.Errorf("socks5: connect to proxy: %w", err)
	}
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if err := d.handshake(conn, host, port); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *Dialer) handshake(conn net.Conn, host string, port int) error {
	methods := []byte{MethodNoAuth}
	if d.Username != "" || d.Password != "" {
		// Only UserPass: offering NoAuth as well lets some proxies (Tor)
		// pick NoAuth and drop stream-isolation credentials.
		methods = []byte{MethodUserPass}
	}
	greet := append([]byte{Version5, byte(len(methods))}, methods...)
	if _, err := conn.Write(greet); err != nil {
		return fmt.Errorf("socks5: greeting: %w", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("socks5: greeting reply: %w", err)
	}
	if resp[0] != Version5 {
		return fmt.Errorf("socks5: proxy spoke version %d", resp[0])
	}
	switch resp[1] {
	case MethodNoAuth:
	case MethodUserPass:
		if err := d.userPass(conn); err != nil {
			return err
		}
	default:
		return fmt.Errorf("socks5: proxy offered no acceptable auth method (0x%02x)", resp[1])
	}

	req := []byte{Version5, CmdConnect, 0x00}
	req, err := appendTarget(req, host, port)
	if err != nil {
		return err
	}
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5: connect request: %w", err)
	}
	var head [3]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("socks5: connect reply: %w", err)
	}
	if head[0] != Version5 {
		return fmt.Errorf("socks5: bad reply version %d", head[0])
	}
	if head[1] != RepSuccess {
		return &ReplyError{Code: head[1]}
	}
	if _, err := readTarget(conn); err != nil {
		return fmt.Errorf("socks5: bound address: %w", err)
	}
	return nil
}

func (d *Dialer) userPass(conn net.Conn) error {
	if len(d.Username) > 255 || len(d.Password) > 255 {
		return fmt.Errorf("socks5: credentials too long")
	}
	msg := []byte{0x01, byte(len(d.Username))}
	msg = append(msg, d.Username...)
	msg = append(msg, byte(len(d.Password)))
	msg = append(msg, d.Password...)
	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("socks5: auth: %w", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("socks5: auth reply: %w", err)
	}
	if resp[1] != 0x00 {
		return fmt.Errorf("socks5: authentication rejected by upstream")
	}
	return nil
}

// ReplyError is a non-success SOCKS5 reply code from the proxy.
type ReplyError struct{ Code byte }

func (e *ReplyError) Error() string {
	var s string
	switch e.Code {
	case RepGeneralFailure:
		s = "general failure"
	case RepNotAllowed:
		s = "connection not allowed by ruleset"
	case RepNetworkUnreachable:
		s = "network unreachable"
	case RepHostUnreachable:
		s = "host unreachable"
	case RepConnectionRefused:
		s = "connection refused"
	case RepTTLExpired:
		s = "TTL expired"
	case RepCommandNotSupported:
		s = "command not supported"
	case RepAddrTypeNotSupported:
		s = "address type not supported"
	default:
		s = fmt.Sprintf("reply code 0x%02x", e.Code)
	}
	return "socks5: " + s
}
