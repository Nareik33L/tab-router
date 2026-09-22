package window

import "testing"

func TestGridMatchesLandscapeExamples(t *testing.T) {
	// 1920×1080 is the landscape case the product examples describe.
	const w, h = 1920, 1080
	want := []struct{ n, cols, rows int }{
		{1, 1, 1},
		{2, 2, 1},
		{4, 2, 2},
		{6, 3, 2},
		{9, 3, 3},
		{16, 4, 4},
		{25, 5, 5},
		{50, 10, 5},
	}
	for _, tc := range want {
		c, r := Grid(tc.n, w, h)
		if c != tc.cols || r != tc.rows {
			t.Errorf("n=%d: got %dx%d, want %dx%d", tc.n, c, r, tc.cols, tc.rows)
		}
	}
}

func TestGridFollowsPortrait(t *testing.T) {
	c, r := Grid(2, 1080, 1920)
	if c != 1 || r != 2 {
		t.Fatalf("portrait 2: got %dx%d, want 1x2", c, r)
	}
	c, r = Grid(6, 1080, 1920)
	if c != 2 || r != 3 {
		t.Fatalf("portrait 6: got %dx%d, want 2x3", c, r)
	}
}

func TestTileCoversWorkAreaWithoutOverlap(t *testing.T) {
	work := Rect{X: 100, Y: 40, Width: 1920, Height: 1040}
	for _, n := range []int{2, 4, 6, 9, 16, 25, 50} {
		cells := Tile(n, work)
		if len(cells) != n {
			t.Fatalf("n=%d: %d cells", n, len(cells))
		}
		assertInsideNoOverlap(t, n, cells, work)
		if n == 4 || n == 9 || n == 50 {
			if !covers(cells, work) {
				t.Fatalf("n=%d: grid does not cover the work area", n)
			}
		}
	}
}

func TestTileMayBeVerySmall(t *testing.T) {
	work := Rect{Width: 10, Height: 8}
	const n = 100
	cells := Tile(n, work)
	if len(cells) != n {
		t.Fatalf("got %d cells", len(cells))
	}
	assertInsideNoOverlap(t, n, cells, work)
	small := false
	for _, c := range cells {
		if c.Width <= 2 || c.Height <= 2 {
			small = true
		}
	}
	if !small {
		t.Fatal("expected very small cells on a tiny work area")
	}
}

func TestClampKeepsWindowsOnScreen(t *testing.T) {
	work := Rect{X: 1920, Y: 0, Width: 1280, Height: 800}
	got := Clamp(Rect{X: 3000, Y: -20, Width: 4000, Height: 100}, work)
	if !Fitted(got, work) {
		t.Fatalf("clamped rect %+v is outside %+v", got, work)
	}
	if got.Width != work.Width || got.Height != 100 {
		t.Fatalf("clamp = %+v", got)
	}
}

func TestParseXrandrWorkArea(t *testing.T) {
	const sample = "" +
		"Screen 0: minimum 8 x 8, current 3840 x 1080, maximum 32767 x 32767\n" +
		"HDMI-1 connected primary 1920x1080+0+0 (normal) 510mm x 287mm\n" +
		"   1920x1080     60.00*+\n" +
		"DP-1 connected 1920x1080+1920+0 (normal) 510mm x 287mm\n"
	work, ok := ParseWorkArea("_NET_WORKAREA(CARDINAL) = 0, 0, 3840, 1040, 0, 0, 3840, 1040")
	if !ok {
		t.Fatal("work area")
	}
	list, err := ParseXrandr(sample, &work)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || !list[0].Primary || list[0].Name != "HDMI-1" {
		t.Fatalf("displays: %+v", list)
	}
	if list[0].Work.Height != 1040 || list[1].Work.X != 1920 || list[1].Work.Height != 1040 {
		t.Fatalf("work areas: %+v %+v", list[0].Work, list[1].Work)
	}
}

func TestFlipCocoaAccountsForMenuBar(t *testing.T) {
	// Primary 1920×1080 points, menu bar 25, dock 80 at the bottom.
	// Cocoa visible frame: origin (0, 80), size 1920×975.
	x, y, w, h := FlipCocoa(1080, 0, 80, 1920, 975)
	if x != 0 || y != 25 || w != 1920 || h != 975 {
		t.Fatalf("flipped visible frame = %v %v %v %v", x, y, w, h)
	}
	screens := CocoaScreens([]cocoaScreen{{
		ID: "Built-in", Name: "Built-in Retina", Primary: true, Scale: 2,
		X: x, Y: y, Width: w, Height: h,
		WX: x, WY: y, WWidth: w, WHeight: h,
	}})
	if len(screens) != 1 || screens[0].Scale != 2 || screens[0].Work.Y != 25 || screens[0].Work.Height != 975 {
		t.Fatalf("screen = %+v", screens)
	}
}

func TestParseShortcut(t *testing.T) {
	s, err := ParseShortcut("Ctrl+Alt+Right")
	if err != nil || s.String() != "ctrl+alt+right" {
		t.Fatalf("got %v %q", err, s)
	}
	s, err = ParseShortcut("super+]")
	if err != nil || s.Key != "bracketright" || !s.Super {
		t.Fatalf("got %+v %v", s, err)
	}
	if _, err := ParseShortcut("ctrl+notakey"); err == nil {
		t.Fatal("accepted unknown key")
	}
	s, err = ParseShortcut("  ")
	if err != nil || !s.Empty() {
		t.Fatalf("empty: %+v %v", s, err)
	}
}

func TestSourcesDoNotWarpThePointer(t *testing.T) {
	// Window movement goes through bounds and focus calls only.
	banned := []string{"SetCursorPos", "CGWarpMouseCursorPosition", "XWarpPointer", "CGDisplayMoveCursor"}
	for _, name := range []string{"grid.go", "layout.go", "display.go", "geom.go", "hotkey.go"} {
		b := mustRead(t, name)
		for _, bad := range banned {
			if contains(b, bad) {
				t.Errorf("%s contains %s", name, bad)
			}
		}
	}
}

func assertInsideNoOverlap(t *testing.T, n int, cells []Rect, work Rect) {
	t.Helper()
	for i, c := range cells {
		if !Fitted(c, work) {
			t.Fatalf("n=%d cell %d %+v outside %+v", n, i, c, work)
		}
		for j := i + 1; j < len(cells); j++ {
			if overlaps(c, cells[j]) {
				t.Fatalf("n=%d cells %d and %d overlap: %+v %+v", n, i, j, c, cells[j])
			}
		}
	}
}

func overlaps(a, b Rect) bool {
	if a.Width <= 0 || a.Height <= 0 || b.Width <= 0 || b.Height <= 0 {
		return false
	}
	return a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height
}

func covers(cells []Rect, work Rect) bool {
	// Every pixel of work belongs to some cell. Check the corners and that
	// the cells' bounding box is the work area, which holds because split
	// sums to the work size and cells tile a complete grid.
	minX, minY := cells[0].X, cells[0].Y
	maxX, maxY := minX, minY
	for _, c := range cells {
		if c.X < minX {
			minX = c.X
		}
		if c.Y < minY {
			minY = c.Y
		}
		if c.X+c.Width > maxX {
			maxX = c.X + c.Width
		}
		if c.Y+c.Height > maxY {
			maxY = c.Y + c.Height
		}
	}
	return minX == work.X && minY == work.Y && maxX == work.X+work.Width && maxY == work.Y+work.Height
}

func contains(b []byte, s string) bool {
	return len(b) >= len(s) && (string(b) == s || len(s) == 0 || indexOf(string(b), s) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
