// Package ipc is the authenticated local control channel between the
// running controller and CLI invocations such as --status and --stop. It
// uses a Unix domain socket (macOS) or a named pipe (Windows), both created
// by the platform provider with owner-only access, plus a per-session random
// token that every request must carry.
package ipc

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nareik33L/tab-router/routing/platform"
)

// Request is one newline-delimited JSON message from a client.
type Request struct {
	Token  string          `json:"token"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the reply.
type Response struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Handler serves one method.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server accepts authenticated requests.
type Server struct {
	ln       net.Listener
	endpoint string
	token    string
	runDir   string
	handlers map[string]Handler
	mu       sync.Mutex
	wg       sync.WaitGroup
	closed   bool
}

// RunDir is where the endpoint, token and pid files live.
func RunDir(dataDir string) string { return filepath.Join(dataDir, "run") }

// ErrNotRunning is returned by clients when no session is active.
var ErrNotRunning = errors.New("no running tab-router session")

// Listen creates the endpoint and writes run/{endpoint,token,pid}.
func Listen(dataDir string, handlers map[string]Handler) (*Server, error) {
	plat := platform.Current()
	runDir := RunDir(dataDir)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, err
	}
	if pid, ok := runningPID(runDir); ok {
		return nil, fmt.Errorf("tab-router is already running (pid %d); use --stop first", pid)
	}
	ln, endpoint, err := plat.ListenIPC(runDir, "control")
	if err != nil {
		return nil, err
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{ln: ln, endpoint: endpoint, token: hex.EncodeToString(tok), runDir: runDir, handlers: handlers}
	for name, content := range map[string]string{
		"endpoint": endpoint,
		"token":    s.token,
		"pid":      strconv.Itoa(os.Getpid()),
	} {
		p := filepath.Join(runDir, name)
		if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
			s.Close()
			return nil, err
		}
		if err := plat.SecureFile(p); err != nil {
			s.Close()
			return nil, err
		}
	}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Endpoint returns the endpoint string.
func (s *Server) Endpoint() string { return s.endpoint }

func runningPID(runDir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(runDir, "pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return 0, false
	}
	// Probe: can we reach the endpoint? A stale pid file with a dead
	// endpoint is reclaimed.
	ep, err := os.ReadFile(filepath.Join(runDir, "endpoint"))
	if err != nil {
		return 0, false
	}
	c, err := platform.Current().DialIPC(strings.TrimSpace(string(ep)))
	if err != nil {
		return 0, false
	}
	c.Close()
	return pid, true
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(c)
		}()
	}
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp(c, Response{Error: "bad request"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.token)) != 1 {
		writeResp(c, Response{Error: "unauthorized"})
		return
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		writeResp(c, Response{Error: "unknown method " + req.Method})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := h(ctx, req.Params)
	if err != nil {
		writeResp(c, Response{Error: err.Error()})
		return
	}
	b, err := json.Marshal(res)
	if err != nil {
		writeResp(c, Response{Error: err.Error()})
		return
	}
	writeResp(c, Response{OK: true, Result: b})
}

func writeResp(c net.Conn, r Response) {
	b, _ := json.Marshal(r)
	_, _ = c.Write(append(b, '\n'))
}

// Close stops the server and removes the run files.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	err := s.ln.Close()
	s.wg.Wait()
	for _, n := range []string{"endpoint", "token", "pid", "control.sock"} {
		_ = os.Remove(filepath.Join(s.runDir, n))
	}
	return err
}

// Client talks to a running session.
type Client struct {
	endpoint string
	token    string
}

// Dial reads the run files and returns a client, or ErrNotRunning.
func Dial(dataDir string) (*Client, error) {
	runDir := RunDir(dataDir)
	ep, err := os.ReadFile(filepath.Join(runDir, "endpoint"))
	if err != nil {
		return nil, ErrNotRunning
	}
	tok, err := os.ReadFile(filepath.Join(runDir, "token"))
	if err != nil {
		return nil, ErrNotRunning
	}
	c := &Client{endpoint: strings.TrimSpace(string(ep)), token: strings.TrimSpace(string(tok))}
	conn, err := platform.Current().DialIPC(c.endpoint)
	if err != nil {
		return nil, ErrNotRunning
	}
	conn.Close()
	return c, nil
}

// Call invokes method and unmarshals the result into out.
func (c *Client) Call(method string, params any, out any) error {
	conn, err := platform.Current().DialIPC(c.endpoint)
	if err != nil {
		return ErrNotRunning
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	req := Request{Token: c.token, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}
