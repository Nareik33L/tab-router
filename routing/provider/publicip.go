package provider

import (
	"context"
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
		Proxy:                 nil,
		DialContext:           dial,
		ForceAttemptHTTP2:     false,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
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
