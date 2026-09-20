package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Transport carries CDP messages. The pipe transport frames messages with a
// trailing NUL byte, as Chromium's --remote-debugging-pipe expects.
type Transport interface {
	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error
	Close() error
}

type pipeTransport struct {
	r  *bufio.Reader
	w  io.WriteCloser
	rc io.Closer
	mu sync.Mutex
}

func newPipeTransport(r io.ReadCloser, w io.WriteCloser) *pipeTransport {
	return &pipeTransport{r: bufio.NewReaderSize(r, 1<<20), w: w, rc: r}
}

func (p *pipeTransport) ReadMessage() ([]byte, error) {
	b, err := p.r.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	return b[:len(b)-1], nil
}

func (p *pipeTransport) WriteMessage(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.w.Write(b); err != nil {
		return err
	}
	_, err := p.w.Write([]byte{0})
	return err
}

func (p *pipeTransport) Close() error {
	_ = p.w.Close()
	return p.rc.Close()
}

// Event is a CDP notification.
type Event struct {
	SessionID string
	Method    string
	Params    json.RawMessage
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *CDPError       `json:"error,omitempty"`
}

// CDPError is a protocol-level error reply.
type CDPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *CDPError) Error() string { return fmt.Sprintf("cdp: %s (%d)", e.Message, e.Code) }

type subscriber struct {
	sessionID string
	method    string // "" matches all methods
	ch        chan Event
}

// Client is a minimal, dependency-free CDP client supporting flattened
// sessions. It is safe for concurrent use.
type Client struct {
	tr     Transport
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan cdpMessage
	subs    map[*subscriber]struct{}
	closed  bool
	readErr error
	done    chan struct{}
}

// NewClient starts the read loop on tr.
func NewClient(tr Transport) *Client {
	c := &Client{tr: tr, pending: map[int64]chan cdpMessage{}, subs: map[*subscriber]struct{}{}, done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		raw, err := c.tr.ReadMessage()
		if err != nil {
			c.fail(err)
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch, ok := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ok {
				ch <- msg
			}
			continue
		}
		if msg.Method == "" {
			continue
		}
		ev := Event{SessionID: msg.SessionID, Method: msg.Method, Params: msg.Params}
		c.mu.Lock()
		for s := range c.subs {
			if s.sessionID == ev.SessionID && (s.method == "" || s.method == ev.Method) {
				select {
				case s.ch <- ev:
				default: // slow subscriber: drop rather than stall the protocol
				}
			}
		}
		c.mu.Unlock()
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.readErr = err
	for id, ch := range c.pending {
		ch <- cdpMessage{ID: id, Error: &CDPError{Code: -1, Message: "connection closed: " + err.Error()}}
		delete(c.pending, id)
	}
	for s := range c.subs {
		close(s.ch)
		delete(c.subs, s)
	}
}

// ErrClosed is returned once the transport has been closed.
var ErrClosed = errors.New("cdp: client closed")

// Close shuts the transport down.
func (c *Client) Close() error {
	err := c.tr.Close()
	<-c.done
	return err
}

// Done is closed when the read loop has exited (browser gone).
func (c *Client) Done() <-chan struct{} { return c.done }

// Call invokes method with params on the given session ("" = browser) and
// unmarshals the result into out (which may be nil).
func (c *Client) Call(ctx context.Context, sessionID, method string, params any, out any) error {
	id := c.nextID.Add(1)
	msg := cdpMessage{ID: id, SessionID: sessionID, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = b
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ch := make(chan cdpMessage, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.tr.WriteMessage(raw); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("cdp: write %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("cdp: %s: %w", method, ctx.Err())
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("cdp: %s: %w", method, resp.Error)
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

// Subscribe returns a channel of events for sessionID (and method, or all
// methods if method == ""). Call cancel to release it.
func (c *Client) Subscribe(sessionID, method string) (<-chan Event, func()) {
	s := &subscriber{sessionID: sessionID, method: method, ch: make(chan Event, 256)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	c.subs[s] = struct{}{}
	c.mu.Unlock()
	return s.ch, func() {
		c.mu.Lock()
		if _, ok := c.subs[s]; ok {
			delete(c.subs, s)
			close(s.ch)
		}
		c.mu.Unlock()
	}
}
