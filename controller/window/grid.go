// Package window arranges the Chromium windows Tab Router already owns.
// Each identity stays its own process. Tiling only moves and resizes those
// windows; it never merges them into one browser.
//
// Launch positions stay staggered, so windows may overlap. Tile All is an
// optional grid. Restore puts the previous positions back. Manual mode
// leaves whatever the user arranged alone, including when a window closes.
// None of these operations move the pointer.
package window

import "math"

// Rect is a screen rectangle in the same pixels Chromium uses for
// Browser.setWindowBounds (physical pixels on Windows, points on macOS).
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Grid chooses a column and row count for n windows on a work area.
// The grid's shape follows the work area: a wide display gets more columns,
// a tall one more rows. Empty cells are kept only when a tighter fit would
// make the grid a much worse match for that aspect.
func Grid(n, workW, workH int) (cols, rows int) {
	if n < 1 {
		return 0, 0
	}
	if workW < 1 {
		workW = 1
	}
	if workH < 1 {
		workH = 1
	}
	aspect := float64(workW) / float64(workH)
	best := math.Inf(1)
	bestC, bestR := 1, n
	logAspect := math.Log(aspect)
	for c := 1; c <= n; c++ {
		r := (n + c - 1) / c
		waste := c*r - n
		mismatch := math.Abs(math.Log(float64(c)/float64(r)) - logAspect)
		score := mismatch + 0.35*float64(waste)
		better := score < best-1e-9
		tie := math.Abs(score-best) <= 1e-9
		if better || (tie && (c*r < bestC*bestR || (c*r == bestC*bestR && c > bestC))) {
			best = score
			bestC, bestR = c, r
		}
	}
	return bestC, bestR
}

// Tile places n windows on work with no overlap and no cell outside work.
// There is no minimum cell size: a crowded display yields very small
// windows, including zero when the work area has fewer pixels than cells.
// A partial last row leaves the unused cells empty.
func Tile(n int, work Rect) []Rect {
	if n < 1 || work.Width < 1 || work.Height < 1 {
		return nil
	}
	cols, rows := Grid(n, work.Width, work.Height)
	colW := split(work.Width, cols)
	rowH := split(work.Height, rows)
	out := make([]Rect, 0, n)
	y := work.Y
	for r := 0; r < rows && len(out) < n; r++ {
		x := work.X
		for c := 0; c < cols && len(out) < n; c++ {
			out = append(out, Rect{X: x, Y: y, Width: colW[c], Height: rowH[r]})
			x += colW[c]
		}
		y += rowH[r]
	}
	return out
}

// split divides total pixels across n buckets. Earlier buckets receive the
// remainder so the buckets sum to total and never exceed it.
func split(total, n int) []int {
	out := make([]int, n)
	if n < 1 {
		return out
	}
	base := total / n
	extra := total % n
	if base < 0 {
		base = 0
		extra = 0
	}
	for i := range out {
		out[i] = base
		if i < extra {
			out[i]++
		}
	}
	return out
}

// Clamp moves r so it lies entirely inside work. A rectangle larger than
// the work area is shrunk to fit. The pointer is not involved.
func Clamp(r, work Rect) Rect {
	if work.Width < 1 || work.Height < 1 {
		return r
	}
	if r.Width > work.Width {
		r.Width = work.Width
	}
	if r.Height > work.Height {
		r.Height = work.Height
	}
	if r.Width < 1 {
		r.Width = 1
	}
	if r.Height < 1 {
		r.Height = 1
	}
	if r.X < work.X {
		r.X = work.X
	}
	if r.Y < work.Y {
		r.Y = work.Y
	}
	if r.X+r.Width > work.X+work.Width {
		r.X = work.X + work.Width - r.Width
	}
	if r.Y+r.Height > work.Y+work.Height {
		r.Y = work.Y + work.Height - r.Height
	}
	return r
}

// Fitted reports whether r is fully inside work.
func Fitted(r, work Rect) bool {
	return r.Width >= 0 && r.Height >= 0 &&
		r.X >= work.X && r.Y >= work.Y &&
		r.X+r.Width <= work.X+work.Width &&
		r.Y+r.Height <= work.Y+work.Height
}
