//go:build darwin

package window

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const cocoaScript = `
function run() {
  ObjC.import('AppKit');
  const screens = $.NSScreen.screens;
  const n = screens.count;
  let primaryH = 0;
  for (let i = 0; i < n; i++) {
    const f = screens.objectAtIndex(i).frame;
    if (Number(f.origin.x) === 0 && Number(f.origin.y) === 0) {
      primaryH = Number(f.size.height);
    }
  }
  const out = [];
  for (let i = 0; i < n; i++) {
    const s = screens.objectAtIndex(i);
    const f = s.frame;
    const v = s.visibleFrame;
    let name = 'Display ' + (i + 1);
    try {
      const ln = s.localizedName;
      if (ln) name = ObjC.unwrap(ln);
    } catch (e) {}
    const fx = Number(f.origin.x), fy = Number(f.origin.y);
    const fw = Number(f.size.width), fh = Number(f.size.height);
    const vx = Number(v.origin.x), vy = Number(v.origin.y);
    const vw = Number(v.size.width), vh = Number(v.size.height);
    out.push({
      id: name, name: name, primary: fx === 0 && fy === 0,
      scale: Number(s.backingScaleFactor),
      x: fx, y: primaryH - (fy + fh), width: fw, height: fh,
      wx: vx, wy: primaryH - (vy + vh), wwidth: vw, wheight: vh
    });
  }
  return JSON.stringify(out);
}
`

// Detect lists NSScreen frames in Chromium's top-left point coordinates.
// visibleFrame already excludes the menu bar and dock.
func Detect() ([]Display, error) {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-")
	cmd.Stdin = strings.NewReader(cocoaScript)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list displays: %w", err)
	}
	var screens []cocoaScreen
	if err := json.Unmarshal(bytes.TrimSpace(out), &screens); err != nil {
		return nil, fmt.Errorf("list displays: %w", err)
	}
	list := CocoaScreens(screens)
	if len(list) == 0 {
		return nil, fmt.Errorf("no displays reported")
	}
	return list, nil
}

// ForegroundPID is the PID of the frontmost application. It needs the same
// Automation permission System Events already asks for; failure just leaves
// focus tracking to the last window Tab Router activated.
func ForegroundPID() (int, bool) {
	out, err := exec.Command("osascript", "-e", `tell application "System Events" to unix id of first application process whose frontmost is true`).Output()
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
