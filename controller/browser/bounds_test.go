package browser

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
)

type scriptTransport struct {
	mu      sync.Mutex
	methods []string
	resp    chan []byte
}

func newScriptTransport() *scriptTransport {
	return &scriptTransport{resp: make(chan []byte, 8)}
}

func (s *scriptTransport) ReadMessage() ([]byte, error) {
	b, ok := <-s.resp
	if !ok {
		return nil, io.EOF
	}
	return b, nil
}

func (s *scriptTransport) WriteMessage(b []byte) error {
	var msg struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		return err
	}
	s.mu.Lock()
	s.methods = append(s.methods, msg.Method)
	s.mu.Unlock()
	var result any
	switch msg.Method {
	case "Browser.getWindowForTarget":
		result = map[string]any{"windowId": 7}
	case "Browser.getWindowBounds":
		result = map[string]any{"bounds": map[string]any{
			"left": 10, "top": 20, "width": 800, "height": 600, "windowState": "normal",
		}}
	default:
		result = map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{"id": msg.ID, "result": result})
	if err != nil {
		return err
	}
	s.resp <- raw
	return nil
}

func (s *scriptTransport) Close() error {
	close(s.resp)
	return nil
}

func TestWindowBoundsDoNotUseThePointer(t *testing.T) {
	tr := newScriptTransport()
	c := NewClient(tr)
	b := &Browser{cdp: c}
	ctx := context.Background()
	got, err := b.WindowBounds(ctx, "target")
	if err != nil {
		t.Fatal(err)
	}
	if got.X != 10 || got.Y != 20 || got.Width != 800 || got.State != "normal" {
		t.Fatalf("bounds %+v", got)
	}
	if err := b.SetWindowBounds(ctx, "target", WindowBounds{X: 1, Y: 2, Width: 3, Height: 4, State: "normal"}); err != nil {
		t.Fatal(err)
	}
	if err := b.FocusWindow(ctx, "target", "session"); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	methods := append([]string(nil), tr.methods...)
	tr.mu.Unlock()
	want := []string{
		"Browser.getWindowForTarget", "Browser.getWindowBounds",
		"Browser.getWindowForTarget", "Browser.setWindowBounds",
		"Target.activateTarget", "Page.bringToFront",
	}
	if len(methods) != len(want) {
		t.Fatalf("methods %v", methods)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("methods %v", methods)
		}
	}
	_ = c.Close()
}
