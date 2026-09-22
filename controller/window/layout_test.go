package window

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeWin struct {
	index  int
	pid    int
	alive  bool
	scale  float64
	bounds Bounds
	sets   []Bounds
	focus  int
}

func (f *fakeWin) Index() int           { return f.index }
func (f *fakeWin) PID() int             { return f.pid }
func (f *fakeWin) Alive() bool          { return f.alive }
func (f *fakeWin) DeviceScale() float64 { return f.scale }
func (f *fakeWin) Bounds(context.Context) (Bounds, error) {
	return f.bounds, nil
}
func (f *fakeWin) SetBounds(_ context.Context, b Bounds) error {
	f.sets = append(f.sets, b)
	f.bounds = b
	return nil
}
func (f *fakeWin) Focus(context.Context) error {
	f.focus++
	return nil
}

func TestTileRestoreAndManual(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "window-layout.json")
	displays := []Display{{
		ID: "HDMI-1", Name: "HDMI-1", Primary: true, Scale: 1,
		Bounds: Rect{Width: 1920, Height: 1080},
		Work:   Rect{Y: 25, Width: 1920, Height: 1000},
	}}
	m := managerFor(t, path, displays)
	a := &fakeWin{index: 1, pid: 10, alive: true, scale: 1, bounds: Bounds{X: 40, Y: 40, Width: 800, Height: 600, State: "normal"}}
	b := &fakeWin{index: 2, pid: 11, alive: true, scale: 1, bounds: Bounds{X: 100, Y: 100, Width: 800, Height: 600, State: "normal"}}
	wins := []Controlled{a, b}

	res, err := m.TileAll(context.Background(), wins)
	if err != nil {
		t.Fatal(err)
	}
	if res.Columns != 2 || res.Rows != 1 || res.Count != 2 || res.Display.ID != "HDMI-1" {
		t.Fatalf("result %+v", res)
	}
	if len(a.sets) != 1 || len(b.sets) != 1 {
		t.Fatalf("sets a=%d b=%d", len(a.sets), len(b.sets))
	}
	assertInsideNoOverlap(t, 2, []Rect{a.bounds.Rect(), b.bounds.Rect()}, displays[0].Work)
	if a.focus+b.focus != 1 {
		t.Fatalf("tile should refocus one window, focus counts %d %d", a.focus, b.focus)
	}
	focused := a
	if b.focus == 1 {
		focused = b
	}

	// A second tile, with nobody dragged, keeps the pre-tile snapshot.
	if _, err := m.TileAll(context.Background(), wins); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(context.Background(), wins); err != nil {
		t.Fatal(err)
	}
	if a.bounds.X != 40 || a.bounds.Y != 40 || b.bounds.X != 100 || b.bounds.Y != 100 {
		t.Fatalf("restore = a %+v b %+v", a.bounds, b.bounds)
	}
	setsAfter := len(a.sets) + len(b.sets)
	m.Manual()
	b.alive = false
	m.OnCountChanged(context.Background(), wins)
	if len(a.sets)+len(b.sets) != setsAfter {
		t.Fatal("manual mode moved a window when one closed")
	}
	if focused.focus == 0 {
		t.Fatal("focused window was lost")
	}
}

func TestAutoArrangeRetilesAndFocusDoesNotMove(t *testing.T) {
	displays := []Display{{
		ID: "primary", Name: "primary", Primary: true,
		Work: Rect{Width: 1000, Height: 800},
	}}
	m := managerFor(t, "", displays)
	m.Auto()
	a := &fakeWin{index: 1, pid: 1, alive: true, scale: 1, bounds: Bounds{X: 1, Y: 1, Width: 10, Height: 10}}
	b := &fakeWin{index: 2, pid: 2, alive: true, scale: 1, bounds: Bounds{X: 2, Y: 2, Width: 10, Height: 10}}
	if _, err := m.TileAll(context.Background(), []Controlled{a, b}); err != nil {
		t.Fatal(err)
	}
	sets := len(a.sets)
	focus := a.focus + b.focus
	b.alive = false
	m.OnCountChanged(context.Background(), []Controlled{a, b})
	if len(a.sets) != sets+1 {
		t.Fatalf("auto-arrange did not retile, sets %d -> %d", sets, len(a.sets))
	}
	if a.bounds.Width != 1000 || a.bounds.Height != 800 {
		t.Fatalf("single window should fill the work area, got %+v", a.bounds)
	}
	focus = a.focus + b.focus
	if _, err := m.FocusNext(context.Background(), []Controlled{a, b}); err != nil {
		t.Fatal(err)
	}
	if len(a.sets) != sets+1 {
		t.Fatal("focus changed window positions")
	}
	if a.focus+b.focus != focus+1 {
		t.Fatal("focus was not applied")
	}
}

func TestDragThenTileUpdatesRestorePoint(t *testing.T) {
	displays := []Display{{ID: "d", Name: "d", Primary: true, Work: Rect{Width: 800, Height: 600}}}
	m := managerFor(t, "", displays)
	a := &fakeWin{index: 1, pid: 1, alive: true, scale: 1, bounds: Bounds{X: 10, Y: 10, Width: 100, Height: 100}}
	b := &fakeWin{index: 2, pid: 2, alive: true, scale: 1, bounds: Bounds{X: 20, Y: 20, Width: 100, Height: 100}}
	if _, err := m.TileAll(context.Background(), []Controlled{a, b}); err != nil {
		t.Fatal(err)
	}
	// User drags the first window off the grid.
	a.bounds.X += 80
	if _, err := m.TileAll(context.Background(), []Controlled{a, b}); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(context.Background(), []Controlled{a, b}); err != nil {
		t.Fatal(err)
	}
	if a.bounds.Width == 100 && a.bounds.X == 10 {
		t.Fatal("restore returned to the original launch position instead of the drag")
	}
}

func TestDeviceScaleShrinksTheGrid(t *testing.T) {
	displays := []Display{{ID: "d", Name: "d", Primary: true, Work: Rect{Width: 2000, Height: 1000}}}
	m := managerFor(t, "", displays)
	a := &fakeWin{index: 1, pid: 1, alive: true, scale: 2, bounds: Bounds{Width: 10, Height: 10}}
	b := &fakeWin{index: 2, pid: 2, alive: true, scale: 2, bounds: Bounds{Width: 10, Height: 10}}
	if _, err := m.TileAll(context.Background(), []Controlled{a, b}); err != nil {
		t.Fatal(err)
	}
	if a.bounds.Width+b.bounds.Width != 1000 {
		t.Fatalf("scale 2 should tile a 1000-wide area, widths %d %d", a.bounds.Width, b.bounds.Width)
	}
}

func TestStartupRestoresManualLayout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "window-layout.json")
	displays := []Display{{ID: "d", Name: "d", Primary: true, Work: Rect{Width: 900, Height: 700}}}
	m := managerFor(t, path, displays)
	a := &fakeWin{index: 1, pid: 5, alive: true, scale: 1, bounds: Bounds{X: 15, Y: 25, Width: 300, Height: 200}}
	m.Manual()
	m.SaveCurrent(context.Background(), []Controlled{a})

	m2 := managerFor(t, path, displays)
	a.bounds = Bounds{X: 40, Y: 40, Width: 1280, Height: 860}
	if _, err := m2.ApplyStartup(context.Background(), []Controlled{a}); err != nil {
		t.Fatal(err)
	}
	if a.bounds.X != 15 || a.bounds.Y != 25 || a.bounds.Width != 300 {
		t.Fatalf("startup did not restore saved layout: %+v", a.bounds)
	}
}

func TestOffscreenRestoreIsClamped(t *testing.T) {
	displays := []Display{{ID: "d", Name: "d", Primary: true, Work: Rect{X: 0, Y: 0, Width: 800, Height: 600}}}
	m := managerFor(t, "", displays)
	a := &fakeWin{index: 1, pid: 1, alive: true, scale: 1, bounds: Bounds{X: 10, Y: 10, Width: 100, Height: 100}}
	if _, err := m.TileAll(context.Background(), []Controlled{a}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.snapshot[1] = Bounds{X: 5000, Y: 5000, Width: 100, Height: 80, State: "normal"}
	m.mu.Unlock()
	if err := m.Restore(context.Background(), []Controlled{a}); err != nil {
		t.Fatal(err)
	}
	if !Fitted(a.bounds.Rect(), displays[0].Work) {
		t.Fatalf("restored window left the desktop: %+v", a.bounds)
	}
}

func managerFor(t *testing.T, path string, displays []Display) *Manager {
	t.Helper()
	m, err := Open(Options{
		Path: path,
		Mode: ModeManual,
		Displays: func() ([]Display, error) {
			return displays, nil
		},
		Foreground: func() (int, bool) { return 0, false },
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
