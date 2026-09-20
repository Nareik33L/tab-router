package socks5

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

// Request describes one CONNECT request as seen by the server.
type Request struct {
	Client net.Addr
	Target Target
	At     time.Time
}

// Server is a CONNECT-only SOCKS5 server. Every accepted connection is
// tracked so the owner can tear them all down (the gate does this when a
// route goes DOWN).
type Server struct {
	// Dial establishes the outbound leg. Required.
	Dial func(ctx context.Context, hostport string) (net.Conn, error)
	// Refuse, if set and returning true, causes every request to be rejected
	// with a general-failure reply before any outbound dial is attempted.
	Refuse func() bool
	// Authenticate, if set, enables username/password auth and is called to
	// validate the pair. If nil, only "no authentication" is offered.
	Authenticate func(user, pass string) bool
	// OnRequest is called for every parsed CONNECT request, before dialing.
	OnRequest func(Request)
	// OnResult is called after the dial with the reply code sent to the client.
	OnResult func(Request, byte, error)
	// HandshakeTimeout bounds the client handshake. Zero means 15s.
	HandshakeTimeout time.Duration

	mu       sync.Mutex
	ln       net.Listener
	conns    map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
	listenWG sync.WaitGroup
}

// Listen starts serving on addr (e.g. "127.0.0.1:0") and returns the bound
// address.
func (s *Server) Listen(addr string) (net.Addr, error) {
	if s.Dial == nil {
		return nil, errors.New("socks5: Server.Dial is required")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.ln = ln
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()
	s.listenWG.Add(1)
	go s.acceptLoop(ln)
	return ln.Addr(), nil
}

// Addr returns the listening address, or nil before Listen.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

func (s *Server) acceptLoop(ln net.Listener) {
	defer s.listenWG.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if !s.track(c) {
			c.Close()
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(c)
			s.handle(c)
		}()
	}
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	c.Close()
}

// CloseConnections terminates every live client connection (and its
// outbound leg) without stopping the listener.
func (s *Server) CloseConnections() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// ActiveConnections reports the number of live client connections.
func (s *Server) ActiveConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Close stops the listener and closes all connections.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	s.CloseConnections()
	s.listenWG.Wait()
	s.wg.Wait()
	return err
}

func (s *Server) handle(c net.Conn) {
	timeout := s.HandshakeTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	_ = c.SetDeadline(time.Now().Add(timeout))

	if err := s.negotiate(c); err != nil {
		return
	}
	var head [3]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return
	}
	if head[0] != Version5 {
		return
	}
	target, err := readTarget(c)
	if err != nil {
		_ = s.reply(c, RepAddrTypeNotSupported)
		return
	}
	req := Request{Client: c.RemoteAddr(), Target: target, At: time.Now()}
	if s.OnRequest != nil {
		s.OnRequest(req)
	}
	if head[1] != CmdConnect {
		_ = s.reply(c, RepCommandNotSupported)
		s.result(req, RepCommandNotSupported, fmt.Errorf("socks5: command %d refused", head[1]))
		return
	}
	if s.Refuse != nil && s.Refuse() {
		_ = s.reply(c, RepGeneralFailure)
		s.result(req, RepGeneralFailure, ErrRefused)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	upstream, err := s.Dial(ctx, target.HostPort())
	cancel()
	if err != nil {
		code := replyCodeFor(err)
		_ = s.reply(c, code)
		s.result(req, code, err)
		return
	}
	defer upstream.Close()
	if err := s.reply(c, RepSuccess); err != nil {
		s.result(req, RepGeneralFailure, err)
		return
	}
	s.result(req, RepSuccess, nil)
	_ = c.SetDeadline(time.Time{})
	pipe(c, upstream)
}

// ErrRefused is reported when the server is in refuse mode (gate CLOSED).
var ErrRefused = errors.New("socks5: refused (route not ready)")

func (s *Server) result(req Request, code byte, err error) {
	if s.OnResult != nil {
		s.OnResult(req, code, err)
	}
}

func (s *Server) negotiate(c net.Conn) error {
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return err
	}
	if head[0] != Version5 {
		return fmt.Errorf("socks5: client version %d", head[0])
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	want := byte(MethodNoAuth)
	if s.Authenticate != nil {
		want = MethodUserPass
	}
	offered := false
	for _, m := range methods {
		if m == want {
			offered = true
			break
		}
	}
	if !offered {
		_, _ = c.Write([]byte{Version5, MethodNoAcceptable})
		return errors.New("socks5: no acceptable method")
	}
	if _, err := c.Write([]byte{Version5, want}); err != nil {
		return err
	}
	if want == MethodUserPass {
		return s.readUserPass(c)
	}
	return nil
}

func (s *Server) readUserPass(c net.Conn) error {
	var ver [2]byte
	if _, err := io.ReadFull(c, ver[:]); err != nil {
		return err
	}
	user := make([]byte, int(ver[1]))
	if _, err := io.ReadFull(c, user); err != nil {
		return err
	}
	var pl [1]byte
	if _, err := io.ReadFull(c, pl[:]); err != nil {
		return err
	}
	pass := make([]byte, int(pl[0]))
	if _, err := io.ReadFull(c, pass); err != nil {
		return err
	}
	if !s.Authenticate(string(user), string(pass)) {
		_, _ = c.Write([]byte{0x01, 0x01})
		return errors.New("socks5: auth failed")
	}
	_, err := c.Write([]byte{0x01, 0x00})
	return err
}

func (s *Server) reply(c net.Conn, code byte) error {
	// BND.ADDR/BND.PORT are zeroed; clients ignore them for CONNECT.
	_, err := c.Write([]byte{Version5, code, 0x00, ATypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

func replyCodeFor(err error) byte {
	var re *ReplyError
	if errors.As(err, &re) {
		return re.Code
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return RepConnectionRefused
	}
	if errors.Is(err, syscall.ENETUNREACH) {
		return RepNetworkUnreachable
	}
	if errors.Is(err, syscall.EHOSTUNREACH) {
		return RepHostUnreachable
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return RepHostUnreachable
	}
	return RepGeneralFailure
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
