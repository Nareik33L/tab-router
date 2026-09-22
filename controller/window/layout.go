package window

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	// ModeManual leaves windows where they are. Closing one does not
	// move the others unless auto-arrange is on.
	ModeManual = "manual"
	// ModeTile is the state after Tile All. A later Tile All keeps the
	// pre-tile snapshot unless the user has dragged a window since.
	ModeTile = "tile"
)

// Bounds is a window's position, size, and show-state.
type Bounds struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	State  string `json:"state,omitempty"`
}

// Rect returns the position and size.
func (b Bounds) Rect() Rect { return Rect{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height} }

// Controlled is one managed Chromium window. Implementations must not move
// the pointer in Bounds, SetBounds, or Focus.
type Controlled interface {
	Index() int
	PID() int
	Alive() bool
	DeviceScale() float64
	Bounds(ctx context.Context) (Bounds, error)
	SetBounds(ctx context.Context, b Bounds) error
	Focus(ctx context.Context) error
}

// ArrangeResult describes a grid that was applied.
type ArrangeResult struct {
	Columns  int     `json:"columns"`
	Rows     int     `json:"rows"`
	Count    int     `json:"count"`
	Focused  int     `json:"focused"`
	Display  Display `json:"display"`
	Fallback bool    `json:"fallback"`
}

// Info is the layout state reported by status.
type Info struct {
	Mode        string `json:"mode"`
	AutoArrange bool   `json:"auto_arrange"`
	Display     string `json:"display,omitempty"`
	Focused     int    `json:"focused,omitempty"`
}

// Options configures a manager. Displays and Foreground may be nil in tests;
// the real process uses Detect and ForegroundPID.
type Options struct {
	Path          string
	Mode          string
	AutoArrange   bool
	Display       string
	FocusNext     string
	FocusPrevious string
	Displays      func() ([]Display, error)
	Foreground    func() (int, bool)
}

// Manager tracks manual versus tiled layout for the live session.
type Manager struct {
	op       sync.Mutex // one arrange or focus at a time
	mu       sync.Mutex
	path     string
	mode     string
	auto     bool
	display  string
	snapshot map[int]Bounds
	applied  map[int]Rect
	focused  int
	next     Shortcut
	prev     Shortcut
	stopKeys func()
	displays func() ([]Display, error)
	front    func() (int, bool)
}

// Open loads a saved layout, if one exists, over the supplied defaults.
// Shortcuts always come from opts so a config edit takes effect on the
// next start. Mode, auto-arrange, display, and the restore snapshot come
// from the state file when it exists, so Manual mode survives a restart.
func Open(opts Options) (*Manager, error) {
	next, err := ParseShortcut(opts.FocusNext)
	if err != nil {
		return nil, err
	}
	prev, err := ParseShortcut(opts.FocusPrevious)
	if err != nil {
		return nil, err
	}
	mode := opts.Mode
	if mode == "" {
		mode = ModeManual
	}
	if mode != ModeManual && mode != ModeTile {
		return nil, fmt.Errorf("window mode %q must be manual or tile", mode)
	}
	m := &Manager{
		path:     opts.Path,
		mode:     mode,
		auto:     opts.AutoArrange,
		display:  opts.Display,
		snapshot: map[int]Bounds{},
		applied:  map[int]Rect{},
		next:     next,
		prev:     prev,
		displays: opts.Displays,
		front:    opts.Foreground,
		stopKeys: func() {},
	}
	if opts.Displays == nil {
		m.displays = Detect
	}
	if opts.Foreground == nil {
		m.front = ForegroundPID
	}
	if opts.Path != "" {
		if st, err := loadState(opts.Path); err == nil {
			if st.Mode == ModeManual || st.Mode == ModeTile {
				m.mode = st.Mode
			}
			m.auto = st.AutoArrange
			if st.Display != "" {
				m.display = st.Display
			}
			m.focused = st.Focused
			m.snapshot = st.Snapshot
			if m.snapshot == nil {
				m.snapshot = map[int]Bounds{}
			}
		}
	}
	return m, nil
}

// Info reports the current mode.
func (m *Manager) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Info{Mode: m.mode, AutoArrange: m.auto, Display: m.display, Focused: m.focused}
}

// Displays lists connected displays and which one tiling will use.
func (m *Manager) Displays() ([]Display, string, error) {
	list, err := m.displays()
	if err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	want := m.display
	m.mu.Unlock()
	return list, want, nil
}

// SetDisplay remembers which monitor receives tiled windows. Positions are
// left alone in manual mode. In tiled auto-arrange mode the grid is rebuilt
// on that display.
func (m *Manager) SetDisplay(ctx context.Context, id string, wins []Controlled) (ArrangeResult, error) {
	list, err := m.displays()
	if err != nil {
		return ArrangeResult{}, err
	}
	chosen, _, err := Choose(list, id)
	if err != nil {
		return ArrangeResult{}, err
	}
	// An explicit unknown id is an error here; Choose falls back for tiling
	// of a saved id, but a user command must name a real display.
	if _, exact, _ := chooseExact(list, id); !exact {
		return ArrangeResult{}, fmt.Errorf("display %q not found", id)
	}
	m.mu.Lock()
	m.display = chosen.ID
	if chosen.ID == "" {
		m.display = chosen.Name
	}
	auto := m.auto
	mode := m.mode
	m.mu.Unlock()
	m.persist()
	if auto && mode == ModeTile {
		return m.TileAll(ctx, wins)
	}
	return ArrangeResult{Display: chosen}, nil
}

func chooseExact(list []Display, want string) (Display, bool, error) {
	d, fallback, err := Choose(list, want)
	if err != nil {
		return Display{}, false, err
	}
	if fallback {
		return Display{}, false, nil
	}
	return d, true, nil
}

// ApplyStartup puts windows back where the user left them, or tiles when
// that is the saved mode. A failure leaves the launch positions in place.
func (m *Manager) ApplyStartup(ctx context.Context, wins []Controlled) (ArrangeResult, error) {
	m.mu.Lock()
	mode := m.mode
	has := len(m.snapshot) > 0
	m.mu.Unlock()
	if mode == ModeTile {
		return m.TileAll(ctx, wins)
	}
	if has {
		return ArrangeResult{}, m.restore(ctx, wins)
	}
	return ArrangeResult{}, nil
}

// TileAll packs every live window onto the selected display's work area.
// The pointer stays where it is. The window that was focused is focused
// again afterwards, so a tiny cell can still be told apart. Other windows
// are not raised.
func (m *Manager) TileAll(ctx context.Context, wins []Controlled) (ArrangeResult, error) {
	m.op.Lock()
	defer m.op.Unlock()
	return m.tile(ctx, wins)
}

func (m *Manager) tile(ctx context.Context, wins []Controlled) (ArrangeResult, error) {
	live := aliveSorted(wins)
	if len(live) == 0 {
		return ArrangeResult{}, errors.New("no browser windows to arrange")
	}
	list, err := m.displays()
	if err != nil {
		return ArrangeResult{}, err
	}
	m.mu.Lock()
	want := m.display
	m.mu.Unlock()
	disp, fallback, err := Choose(list, want)
	if err != nil {
		return ArrangeResult{}, err
	}
	work := scaleWork(disp.Work, commonScale(live))
	cells := Tile(len(live), work)
	if len(cells) != len(live) {
		return ArrangeResult{}, fmt.Errorf("display %s has no usable area", disp.label())
	}
	current, err := readBounds(ctx, live)
	if err != nil {
		return ArrangeResult{}, err
	}
	focused := m.currentFocus(live)

	m.mu.Lock()
	capture := m.mode != ModeTile || len(m.snapshot) == 0 || userMoved(current, m.applied)
	if capture {
		m.snapshot = current
	}
	m.mode = ModeTile
	m.applied = map[int]Rect{}
	for i, w := range live {
		m.applied[w.Index()] = cells[i]
	}
	m.focused = focused.Index()
	m.mu.Unlock()

	var first error
	for i, w := range live {
		b := Bounds{X: cells[i].X, Y: cells[i].Y, Width: cells[i].Width, Height: cells[i].Height, State: "normal"}
		if err := w.SetBounds(ctx, b); err != nil && first == nil {
			first = err
		}
	}
	if err := focused.Focus(ctx); err != nil && first == nil {
		first = err
	}
	m.persist()
	cols, rows := Grid(len(live), work.Width, work.Height)
	return ArrangeResult{
		Columns: cols, Rows: rows, Count: len(live), Focused: focused.Index(),
		Display: disp, Fallback: fallback,
	}, first
}

// Restore puts windows back to the positions recorded before tiling.
func (m *Manager) Restore(ctx context.Context, wins []Controlled) error {
	return m.restore(ctx, wins)
}

func (m *Manager) restore(ctx context.Context, wins []Controlled) error {
	m.op.Lock()
	defer m.op.Unlock()
	return m.restoreLocked(ctx, wins)
}

func (m *Manager) restoreLocked(ctx context.Context, wins []Controlled) error {
	m.mu.Lock()
	if len(m.snapshot) == 0 {
		m.mu.Unlock()
		return errors.New("no saved layout to restore")
	}
	snap := copyBounds(m.snapshot)
	m.mu.Unlock()

	list, _ := m.displays()
	var prefer Rect
	if len(list) > 0 {
		m.mu.Lock()
		want := m.display
		m.mu.Unlock()
		if d, _, err := Choose(list, want); err == nil {
			prefer = d.Work
		}
	}
	live := aliveSorted(wins)
	var first error
	var focused Controlled
	m.mu.Lock()
	focusIndex := m.focused
	m.mu.Unlock()
	for _, w := range live {
		b, ok := snap[w.Index()]
		if !ok {
			continue
		}
		if len(list) > 0 {
			r := FitVisible(b.Rect(), list, prefer)
			b.X, b.Y, b.Width, b.Height = r.X, r.Y, r.Width, r.Height
		}
		if b.State == "" {
			b.State = "normal"
		}
		if err := w.SetBounds(ctx, b); err != nil && first == nil {
			first = err
		}
		if w.Index() == focusIndex {
			focused = w
		}
	}
	m.mu.Lock()
	m.mode = ModeManual
	m.applied = map[int]Rect{}
	m.mu.Unlock()
	if focused == nil && len(live) > 0 {
		focused = m.currentFocus(live)
	}
	if focused != nil {
		if err := focused.Focus(ctx); err != nil && first == nil {
			first = err
		}
		m.mu.Lock()
		m.focused = focused.Index()
		m.mu.Unlock()
	}
	m.persist()
	return first
}

// Manual stops automatic rearrangement and leaves every window where it is.
func (m *Manager) Manual() {
	m.mu.Lock()
	m.mode = ModeManual
	m.auto = false
	m.applied = map[int]Rect{}
	m.mu.Unlock()
	m.persist()
}

// Auto enables rearrangement when a window is added or removed. It does not
// move anything by itself.
func (m *Manager) Auto() {
	m.mu.Lock()
	m.auto = true
	m.mu.Unlock()
	m.persist()
}

// OnCountChanged rebuilds the grid only when auto-arrange is enabled.
func (m *Manager) OnCountChanged(ctx context.Context, wins []Controlled) {
	m.mu.Lock()
	auto := m.auto
	m.mu.Unlock()
	if !auto {
		return
	}
	_, _ = m.TileAll(ctx, wins)
}

// FocusNext makes the next live window active and does not move any window.
func (m *Manager) FocusNext(ctx context.Context, wins []Controlled) (int, error) {
	return m.focusStep(ctx, wins, 1)
}

// FocusPrevious makes the previous live window active and does not move any window.
func (m *Manager) FocusPrevious(ctx context.Context, wins []Controlled) (int, error) {
	return m.focusStep(ctx, wins, -1)
}

func (m *Manager) focusStep(ctx context.Context, wins []Controlled, dir int) (int, error) {
	m.op.Lock()
	defer m.op.Unlock()
	live := aliveSorted(wins)
	if len(live) == 0 {
		return 0, errors.New("no browser windows to focus")
	}
	cur := m.currentFocus(live)
	i := 0
	for n, w := range live {
		if w.Index() == cur.Index() {
			i = n
			break
		}
	}
	next := live[(i+dir+len(live))%len(live)]
	if err := next.Focus(ctx); err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.focused = next.Index()
	m.mu.Unlock()
	m.persist()
	return next.Index(), nil
}

// SaveCurrent records manual positions so the next start can put windows
// back. A tiled session keeps the pre-tile snapshot instead.
func (m *Manager) SaveCurrent(ctx context.Context, wins []Controlled) {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	manual := m.mode == ModeManual
	m.mu.Unlock()
	if manual {
		if cur, err := readBounds(ctx, aliveSorted(wins)); err == nil && len(cur) > 0 {
			m.mu.Lock()
			m.snapshot = cur
			m.mu.Unlock()
		}
	}
	m.persist()
}

// StartShortcuts registers the configured focus bindings. Empty bindings
// do nothing. stop is idempotent.
func (m *Manager) StartShortcuts(onNext, onPrev func()) error {
	m.mu.Lock()
	next, prev := m.next, m.prev
	m.mu.Unlock()
	if next.Empty() && prev.Empty() {
		return nil
	}
	stop, err := startShortcuts(next, prev, onNext, onPrev)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.stopKeys = stop
	m.mu.Unlock()
	return nil
}

// Stop releases global shortcuts.
func (m *Manager) Stop() {
	m.mu.Lock()
	stop := m.stopKeys
	m.stopKeys = func() {}
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (m *Manager) currentFocus(live []Controlled) Controlled {
	if m.front != nil {
		if pid, ok := m.front(); ok {
			for _, w := range live {
				if w.PID() == pid {
					return w
				}
			}
		}
	}
	m.mu.Lock()
	focused := m.focused
	m.mu.Unlock()
	for _, w := range live {
		if w.Index() == focused {
			return w
		}
	}
	return live[0]
}

func (m *Manager) persist() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.path == "" {
		return
	}
	st := persisted{
		Mode:        m.mode,
		AutoArrange: m.auto,
		Display:     m.display,
		Focused:     m.focused,
		Snapshot:    copyBounds(m.snapshot),
	}
	_ = saveState(m.path, st)
}

type persisted struct {
	Mode        string         `json:"mode"`
	AutoArrange bool           `json:"auto_arrange"`
	Display     string         `json:"display,omitempty"`
	Focused     int            `json:"focused,omitempty"`
	Snapshot    map[int]Bounds `json:"snapshot,omitempty"`
}

func loadState(path string) (persisted, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return persisted{}, err
	}
	var st persisted
	if err := json.Unmarshal(b, &st); err != nil {
		return persisted{}, err
	}
	return st, nil
}

func saveState(path string, st persisted) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func aliveSorted(wins []Controlled) []Controlled {
	out := make([]Controlled, 0, len(wins))
	for _, w := range wins {
		if w != nil && w.Alive() {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index() < out[j].Index() })
	return out
}

func readBounds(ctx context.Context, live []Controlled) (map[int]Bounds, error) {
	out := make(map[int]Bounds, len(live))
	for _, w := range live {
		b, err := w.Bounds(ctx)
		if err != nil {
			return nil, fmt.Errorf("identity %03d: %w", w.Index(), err)
		}
		out[w.Index()] = b
	}
	return out, nil
}

func copyBounds(in map[int]Bounds) map[int]Bounds {
	out := make(map[int]Bounds, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func userMoved(live map[int]Bounds, applied map[int]Rect) bool {
	if len(applied) == 0 {
		return false
	}
	for id, b := range live {
		a, ok := applied[id]
		if !ok {
			continue
		}
		if !near(b.Rect(), a) {
			return true
		}
	}
	return false
}

func near(a, b Rect) bool {
	return abs(a.X-b.X) <= 2 && abs(a.Y-b.Y) <= 2 && abs(a.Width-b.Width) <= 2 && abs(a.Height-b.Height) <= 2
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func commonScale(live []Controlled) float64 {
	if len(live) == 0 {
		return 1
	}
	s := live[0].DeviceScale()
	if s <= 0 {
		return 1
	}
	return s
}

func scaleWork(work Rect, scale float64) Rect {
	if scale <= 0 || math.Abs(scale-1) < 1e-6 {
		return work
	}
	f := func(n int) int { return int(math.Round(float64(n) / scale)) }
	out := Rect{X: f(work.X), Y: f(work.Y), Width: f(work.Width), Height: f(work.Height)}
	if out.Width < 1 {
		out.Width = 1
	}
	if out.Height < 1 {
		out.Height = 1
	}
	return out
}
