package window

import (
	"bufio"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var xrandrConnected = regexp.MustCompile(`^(\S+) connected (primary )?([0-9]+)x([0-9]+)\+(-?[0-9]+)\+(-?[0-9]+)`)

// ParseXrandr reads `xrandr --query` output. work, when non-nil, is the
// _NET_WORKAREA rectangle; each output's usable area is its intersection
// with that rectangle (the taskbar or panel). An output that misses the
// work area keeps its full bounds so it is still a place windows can sit.
func ParseXrandr(out string, work *Rect) ([]Display, error) {
	var list []Display
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		m := xrandrConnected.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		w := atoi(m[3])
		h := atoi(m[4])
		x := atoi(m[5])
		y := atoi(m[6])
		bounds := Rect{X: x, Y: y, Width: w, Height: h}
		usable := bounds
		if work != nil {
			if clipped, ok := intersect(bounds, *work); ok {
				usable = clipped
			}
		}
		list = append(list, Display{
			ID:      m[1],
			Name:    m[1],
			Primary: strings.TrimSpace(m[2]) == "primary",
			Scale:   1,
			Bounds:  bounds,
			Work:    usable,
		})
	}
	if len(list) == 0 {
		return nil, errNoDisplay("xrandr")
	}
	if !anyPrimary(list) {
		list[0].Primary = true
	}
	return list, nil
}

// ParseWorkArea reads an xprop _NET_WORKAREA value. The first four integers
// are the current desktop. ok is false when the line does not contain them.
func ParseWorkArea(line string) (Rect, bool) {
	_, rest, ok := strings.Cut(line, "=")
	if !ok {
		rest = line
	}
	fields := strings.FieldsFunc(rest, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var nums []int
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		nums = append(nums, n)
		if len(nums) == 4 {
			break
		}
	}
	if len(nums) < 4 || nums[2] <= 0 || nums[3] <= 0 {
		return Rect{}, false
	}
	return Rect{X: nums[0], Y: nums[1], Width: nums[2], Height: nums[3]}, true
}

// cocoaScreen is one NSScreen after the bottom-left origin has been
// converted to Chromium's top-left origin. Values may be fractional points.
type cocoaScreen struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Primary bool    `json:"primary"`
	Scale   float64 `json:"scale"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	WX      float64 `json:"wx"`
	WY      float64 `json:"wy"`
	WWidth  float64 `json:"wwidth"`
	WHeight float64 `json:"wheight"`
}

// CocoaScreens converts already-flipped NSScreen records into displays.
// Points are Chromium's coordinate space on macOS, so the backing scale is
// recorded and not applied a second time.
func CocoaScreens(screens []cocoaScreen) []Display {
	out := make([]Display, 0, len(screens))
	for i, s := range screens {
		name := s.Name
		if name == "" {
			name = s.ID
		}
		if name == "" {
			name = "display"
		}
		id := s.ID
		if id == "" {
			id = name
		}
		scale := s.Scale
		if scale <= 0 {
			scale = 1
		}
		out = append(out, Display{
			ID:      id,
			Name:    name,
			Primary: s.Primary,
			Scale:   scale,
			Bounds:  rectFrom(s.X, s.Y, s.Width, s.Height),
			Work:    rectFrom(s.WX, s.WY, s.WWidth, s.WHeight),
		})
		if out[i].Work.Width < 1 || out[i].Work.Height < 1 {
			out[i].Work = out[i].Bounds
		}
	}
	if len(out) > 0 && !anyPrimary(out) {
		out[0].Primary = true
	}
	return out
}

// FlipCocoa converts an NSScreen frame from Cocoa's bottom-left origin to
// a top-left origin using the primary display's height in points.
func FlipCocoa(primaryHeight, x, y, w, h float64) (topX, topY, width, height float64) {
	return x, primaryHeight - (y + h), w, h
}

func rectFrom(x, y, w, h float64) Rect {
	return Rect{
		X:      int(math.Round(x)),
		Y:      int(math.Round(y)),
		Width:  int(math.Round(w)),
		Height: int(math.Round(h)),
	}
}

func anyPrimary(list []Display) bool {
	for _, d := range list {
		if d.Primary {
			return true
		}
	}
	return false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

type displayError string

func (e displayError) Error() string { return string(e) }

func errNoDisplay(source string) error {
	return displayError("no displays reported by " + source)
}
