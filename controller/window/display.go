package window

import (
	"fmt"
	"strconv"
	"strings"
)

// Display is one connected monitor. Bounds is the full panel. Work is the
// usable area inside the taskbar, dock, and menu bar. Both are in Chromium
// window pixels. Scale is the operating system's display scale (1 = 100%)
// and is informational: Work is already expressed in Chromium's pixels, so
// a 200% desktop does not double the grid.
type Display struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Primary bool    `json:"primary"`
	Scale   float64 `json:"scale"`
	Bounds  Rect    `json:"bounds"`
	Work    Rect    `json:"work"`
}

func (d Display) label() string {
	if d.Name != "" {
		return d.Name
	}
	if d.ID != "" {
		return d.ID
	}
	return "display"
}

// Choose selects the display the user asked for. want may be an id, a name,
// a 1-based index, "primary", or empty (primary). When want names a display
// that is not connected, the primary is returned and fallback is true. The
// caller's saved choice is left unchanged so it applies again when that
// display returns.
func Choose(displays []Display, want string) (Display, bool, error) {
	if len(displays) == 0 {
		return Display{}, false, fmt.Errorf("no connected displays")
	}
	primary := displays[0]
	for _, d := range displays {
		if d.Primary {
			primary = d
			break
		}
	}
	want = strings.TrimSpace(want)
	if want == "" || strings.EqualFold(want, "primary") {
		return primary, false, nil
	}
	for _, d := range displays {
		if d.ID == want || strings.EqualFold(d.Name, want) || strings.EqualFold(d.ID, want) {
			return d, false, nil
		}
	}
	if n, err := strconv.Atoi(want); err == nil && n >= 1 && n <= len(displays) {
		return displays[n-1], false, nil
	}
	return primary, true, nil
}

// FitVisible leaves r unchanged when it already sits inside some work area.
// Otherwise it is clamped into prefer, or the primary work area when prefer
// is zero.
func FitVisible(r Rect, displays []Display, prefer Rect) Rect {
	for _, d := range displays {
		if d.Work.Width > 0 && d.Work.Height > 0 && Fitted(r, d.Work) {
			return r
		}
	}
	work := prefer
	if work.Width < 1 || work.Height < 1 {
		for _, d := range displays {
			if d.Primary && d.Work.Width > 0 && d.Work.Height > 0 {
				work = d.Work
				break
			}
		}
	}
	if work.Width < 1 || work.Height < 1 {
		for _, d := range displays {
			if d.Work.Width > 0 && d.Work.Height > 0 {
				work = d.Work
				break
			}
		}
	}
	if work.Width < 1 || work.Height < 1 {
		return r
	}
	return Clamp(r, work)
}

// intersect clips a against b. ok is false when the intersection is empty.
func intersect(a, b Rect) (Rect, bool) {
	x0 := a.X
	if b.X > x0 {
		x0 = b.X
	}
	y0 := a.Y
	if b.Y > y0 {
		y0 = b.Y
	}
	x1 := a.X + a.Width
	if b.X+b.Width < x1 {
		x1 = b.X + b.Width
	}
	y1 := a.Y + a.Height
	if b.Y+b.Height < y1 {
		y1 = b.Y + b.Height
	}
	if x1 <= x0 || y1 <= y0 {
		return Rect{}, false
	}
	return Rect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}, true
}
