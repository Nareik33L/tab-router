//go:build linux

package window

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Detect lists X11 outputs. Wayland sessions that still expose xrandr work
// the same way. A session with neither reports an error; tiling commands
// fail and the staggered launch positions stay where they are.
func Detect() ([]Display, error) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") != "" {
		if list, err := detectSway(); err == nil {
			return list, nil
		}
	}
	out, err := exec.Command("xrandr", "--query").Output()
	if err != nil {
		return nil, fmt.Errorf("list displays: %w", err)
	}
	var work *Rect
	if prop, err := exec.Command("xprop", "-root", "_NET_WORKAREA").Output(); err == nil {
		if r, ok := ParseWorkArea(string(prop)); ok {
			work = &r
		}
	}
	list, err := ParseXrandr(string(out), work)
	if err != nil {
		return nil, err
	}
	if dpi := xftDPI(); dpi > 0 {
		scale := float64(dpi) / 96
		for i := range list {
			list[i].Scale = scale
		}
	}
	return list, nil
}

func xftDPI() int {
	out, err := exec.Command("xrdb", "-query").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.ToLower(line), "xft.dpi:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				n, _ := strconv.Atoi(fields[len(fields)-1])
				return n
			}
		}
	}
	return 0
}

// detectSway reads swaymsg JSON when xrandr is not the right tool.
func detectSway() ([]Display, error) {
	out, err := exec.Command("swaymsg", "-t", "get_outputs", "--raw").Output()
	if err != nil {
		return nil, err
	}
	return parseSway(out)
}

// ForegroundPID is the PID of the window that currently has keyboard focus.
func ForegroundPID() (int, bool) {
	out, err := exec.Command("xprop", "-root", "_NET_ACTIVE_WINDOW").Output()
	if err != nil {
		return 0, false
	}
	id := ""
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "0x") {
			id = strings.Trim(f, ",")
		}
	}
	if id == "" || id == "0x0" {
		return 0, false
	}
	pidOut, err := exec.Command("xprop", "-id", id, "_NET_WM_PID").Output()
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(pidOut))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
