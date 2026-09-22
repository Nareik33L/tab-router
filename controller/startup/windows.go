package startup

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/Nareik33L/tab-router/controller/browser"
	"github.com/Nareik33L/tab-router/controller/verify"
	"github.com/Nareik33L/tab-router/controller/window"
)

// browserWindow is one identity's Chromium window. Bounds and focus go
// through DevTools, which does not move the pointer.
type browserWindow struct {
	sub  *verify.Subject
	page *browser.Page
}

func (w browserWindow) Index() int { return w.sub.Identity.Index }
func (w browserWindow) PID() int {
	if w.sub.Browser == nil {
		return 0
	}
	return w.sub.Browser.PID()
}
func (w browserWindow) Alive() bool {
	return w.sub.Browser != nil && w.sub.Browser.Alive() && w.page != nil
}
func (w browserWindow) DeviceScale() float64 {
	s := w.sub.Identity.Environment.DeviceScaleFactor
	if s <= 0 {
		return 1
	}
	return s
}
func (w browserWindow) Bounds(ctx context.Context) (window.Bounds, error) {
	b, err := w.sub.Browser.WindowBounds(ctx, w.page.TargetID)
	if err != nil {
		return window.Bounds{}, err
	}
	return window.Bounds{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height, State: b.State}, nil
}
func (w browserWindow) SetBounds(ctx context.Context, b window.Bounds) error {
	return w.sub.Browser.SetWindowBounds(ctx, w.page.TargetID, browser.WindowBounds{
		X: b.X, Y: b.Y, Width: b.Width, Height: b.Height, State: b.State,
	})
}
func (w browserWindow) Focus(ctx context.Context) error {
	return w.sub.Browser.FocusWindow(ctx, w.page.TargetID, w.page.SessionID)
}

func windowLayoutPath(dataDir string) string {
	return filepath.Join(dataDir, "window-layout.json")
}

func (s *Session) startWindowManager() {
	if s.Cfg.Headless {
		return
	}
	m, err := window.Open(window.Options{
		Path:          windowLayoutPath(s.Cfg.DataDir),
		Mode:          s.Cfg.Windows.Mode,
		AutoArrange:   s.Cfg.Windows.AutoArrange,
		Display:       s.Cfg.Windows.Display,
		FocusNext:     s.Cfg.Windows.FocusNext,
		FocusPrevious: s.Cfg.Windows.FocusPrevious,
	})
	if err != nil {
		s.Report.Line("Window layout: %v", err)
		return
	}
	s.windows = m
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := m.ApplyStartup(ctx, s.controlled())
	if err != nil {
		s.Report.Line("Window layout: %v", err)
	} else if res.Count > 0 {
		name := res.Display.Name
		if name == "" {
			name = res.Display.ID
		}
		s.Report.Line("Tiled %d windows in a %dx%d grid on %s", res.Count, res.Columns, res.Rows, name)
		if res.Fallback {
			s.Report.Line("Saved display is not connected; using %s", name)
		}
	}
	if err := m.StartShortcuts(
		func() { _, _ = s.windows.FocusNext(context.Background(), s.controlled()) },
		func() { _, _ = s.windows.FocusPrevious(context.Background(), s.controlled()) },
	); err != nil {
		s.Report.Line("Focus shortcuts: %v", err)
	}
}

func (s *Session) controlled() []window.Controlled {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]window.Controlled, 0, len(s.subjects))
	for _, sub := range s.subjects {
		page := s.main[sub]
		if sub.Browser == nil || page == nil {
			continue
		}
		out = append(out, browserWindow{sub: sub, page: page})
	}
	return out
}

func (s *Session) saveWindows(ctx context.Context) {
	if s.windows == nil {
		return
	}
	s.windows.SaveCurrent(ctx, s.controlled())
}

func (s *Session) stopWindows() {
	if s.windows == nil {
		return
	}
	s.windows.Stop()
}

func (s *Session) tileWindows(ctx context.Context) (window.ArrangeResult, error) {
	if err := s.needWindows(); err != nil {
		return window.ArrangeResult{}, err
	}
	return s.windows.TileAll(ctx, s.controlled())
}

func (s *Session) restoreWindows(ctx context.Context) error {
	if err := s.needWindows(); err != nil {
		return err
	}
	return s.windows.Restore(ctx, s.controlled())
}

func (s *Session) windowsMode(mode string) (window.Info, error) {
	if err := s.needWindows(); err != nil {
		return window.Info{}, err
	}
	if mode == "auto" {
		s.windows.Auto()
	} else {
		s.windows.Manual()
	}
	return s.windows.Info(), nil
}

func (s *Session) focusWindows(ctx context.Context, dir int) (int, error) {
	if err := s.needWindows(); err != nil {
		return 0, err
	}
	if dir < 0 {
		return s.windows.FocusPrevious(ctx, s.controlled())
	}
	return s.windows.FocusNext(ctx, s.controlled())
}

func (s *Session) listDisplays() (struct {
	Displays []window.Display `json:"displays"`
	Selected string           `json:"selected"`
}, error) {
	var out struct {
		Displays []window.Display `json:"displays"`
		Selected string           `json:"selected"`
	}
	if err := s.needWindows(); err != nil {
		return out, err
	}
	list, selected, err := s.windows.Displays()
	if err != nil {
		return out, err
	}
	out.Displays = list
	out.Selected = selected
	return out, nil
}

func (s *Session) setDisplay(ctx context.Context, id string) (window.ArrangeResult, error) {
	if err := s.needWindows(); err != nil {
		return window.ArrangeResult{}, err
	}
	return s.windows.SetDisplay(ctx, id, s.controlled())
}

func (s *Session) needWindows() error {
	if s.windows == nil {
		return errNoWindows
	}
	return nil
}

var errNoWindows = errors.New("window management is not available")
