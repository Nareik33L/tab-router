package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrNoIP is returned when an echo response contains no parsable address.
var ErrNoIP = errors.New("echo response contained no IP address")

// ParseEchoBody extracts an IP from common echo formats: bare text, or JSON
// with an "ip" (or "origin"/"address") field.
func ParseEchoBody(body []byte) (net.IP, error) {
	s := strings.TrimSpace(string(body))
	if ip := net.ParseIP(s); ip != nil {
		return ip, nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err == nil {
		for _, k := range []string{"ip", "origin", "address", "query"} {
			if v, ok := m[k].(string); ok {
				v = strings.TrimSpace(strings.Split(v, ",")[0])
				if ip := net.ParseIP(v); ip != nil {
					return ip, nil
				}
			}
		}
	}
	return nil, ErrNoIP
}

// HTTPClientFor builds an HTTP client whose every connection is opened with
// dial. Redirects are followed; proxies from the environment are ignored.
func HTTPClientFor(dial DialFunc, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:       nil,
		DialContext: dial,
		// Handshake ourselves so ServerName is the echo hostname, not the
		// local SOCKS address (127.0.0.1). On macOS that mismatch makes
		// Security.framework fail with SecPolicyCreateSSL error: 0.
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialTLS(ctx, dial, network, addr)
		},
		ForceAttemptHTTP2:     false,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func dialTLS(ctx context.Context, dial DialFunc, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	raw, err := dial(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	c := tls.Client(&remoteAddrConn{Conn: raw, remote: addr}, cfg)
	if err := c.HandshakeContext(ctx); err != nil {
		raw.Close()
		if roots := extraRootCAs(); roots != nil && isSystemTLSVerifyBug(err) {
			raw, err = dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			cfg2 := cfg.Clone()
			cfg2.RootCAs = roots
			c = tls.Client(&remoteAddrConn{Conn: raw, remote: addr}, cfg2)
			if err := c.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			return c, nil
		}
		return nil, err
	}
	return c, nil
}

type remoteAddrConn struct {
	net.Conn
	remote string
}

func (c *remoteAddrConn) RemoteAddr() net.Addr { return hostAddr(c.remote) }

type hostAddr string

func (a hostAddr) Network() string { return "tcp" }
func (a hostAddr) String() string  { return string(a) }

func isSystemTLSVerifyBug(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SecPolicyCreateSSL")
}

// PublicIP fetches echoURL through dial and returns the reported address.
func PublicIP(ctx context.Context, dial DialFunc, echoURL string, timeout time.Duration) (net.IP, error) {
	client := HTTPClientFor(dial, timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, echoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tab-router-probe/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("echo returned HTTP %d", resp.StatusCode)
	}
	return ParseEchoBody(body)
}

// DirectDial is the host's own network path. It exists solely so the
// controller can compare the host IP against identity IPs; no browser
// traffic ever uses it.
func DirectDial(ctx context.Context, network, hostport string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, hostport)
}
