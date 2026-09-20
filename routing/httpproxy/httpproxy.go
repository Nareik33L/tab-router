// Package httpproxy implements an HTTP CONNECT client dialer (for upstream
// HTTP proxies) and a minimal CONNECT-only server used by the test
// infrastructure. Hostnames are sent to the proxy unresolved.
package httpproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Dialer tunnels TCP connections through an HTTP proxy using CONNECT.
type Dialer struct {
	ProxyAddr string
	Username  string
	Password  string
	Dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	Timeout   time.Duration
}

// DialContext opens a tunnel to hostport.
func (d *Dialer) DialContext(ctx context.Context, network, hostport string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("httpproxy: network %q not supported (TCP only)", network)
	}
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		return nil, err
	}
	dial := d.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", d.ProxyAddr)
	if err != nil {
		return nil, fmt.Errorf("httpproxy: connect to proxy: %w", err)
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

	hdr := make(http.Header)
	if d.Username != "" || d.Password != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(d.Username + ":" + d.Password))
		hdr.Set("Proxy-Authorization", "Basic "+cred)
	}
	if err := writeConnect(conn, hostport, hdr); err != nil {
		conn.Close()
		return nil, fmt.Errorf("httpproxy: send CONNECT: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("httpproxy: read CONNECT response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, &StatusError{Code: resp.StatusCode}
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// StatusError is a non-200 CONNECT response.
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("httpproxy: CONNECT rejected with status %d", e.Code)
}

func writeConnect(w io.Writer, hostport string, h http.Header) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", hostport, hostport)
	for k, vs := range h {
		for _, v := range vs {
			fmt.Fprintf(bw, "%s: %s\r\n", k, v)
		}
	}
	bw.WriteString("\r\n")
	return bw.Flush()
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// Server is a CONNECT-only HTTP proxy used by tests to emulate an upstream.
type Server struct {
	Dial         func(ctx context.Context, hostport string) (net.Conn, error)
	Authenticate func(user, pass string) bool
	OnRequest    func(hostport string)

	mu    sync.Mutex
	ln    net.Listener
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

// Listen starts the server on addr and returns its address.
func (s *Server) Listen(addr string) (net.Addr, error) {
	if s.Dial == nil {
		return nil, errors.New("httpproxy: Server.Dial is required")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.ln = ln
	s.conns = map[net.Conn]struct{}{}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[c] = struct{}{}
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handle(c)
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
				c.Close()
			}()
		}
	}()
	return ln.Addr(), nil
}

// Close stops the listener and all tunnels.
func (s *Server) Close() error {
	s.mu.Lock()
	ln := s.ln
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	for _, c := range conns {
		c.Close()
	}
	s.wg.Wait()
	return err
}

func (s *Server) handle(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		writeStatus(c, http.StatusMethodNotAllowed)
		return
	}
	if s.Authenticate != nil {
		user, pass, ok := parseBasic(req.Header.Get("Proxy-Authorization"))
		if !ok || !s.Authenticate(user, pass) {
			_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"proxy\"\r\nContent-Length: 0\r\n\r\n")
			return
		}
	}
	hostport := req.Host
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		writeStatus(c, http.StatusBadRequest)
		return
	}
	if s.OnRequest != nil {
		s.OnRequest(hostport)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	up, err := s.Dial(ctx, hostport)
	cancel()
	if err != nil {
		writeStatus(c, http.StatusBadGateway)
		return
	}
	defer up.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(up, br); up.Close() }()
	go func() { defer wg.Done(); _, _ = io.Copy(c, up); c.Close() }()
	wg.Wait()
}

func writeStatus(w io.Writer, code int) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", code, http.StatusText(code))
}

func parseBasic(h string) (string, string, bool) {
	const prefix = "Basic "
	if len(h) < len(prefix) || h[:len(prefix)] != prefix {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(h[len(prefix):])
	if err != nil {
		return "", "", false
	}
	for i, b := range raw {
		if b == ':' {
			return string(raw[:i]), string(raw[i+1:]), true
		}
	}
	return "", "", false
}
