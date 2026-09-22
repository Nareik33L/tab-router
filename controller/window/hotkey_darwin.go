//go:build darwin

package window

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const hotkeyScript = `
function run() {
  ObjC.import('Cocoa');
  const env = $.NSProcessInfo.processInfo.environment;
  function num(key) {
    const v = env.objectForKey($(key));
    if (!v) return -1;
    return parseInt(ObjC.unwrap(v), 10);
  }
  const nextCode = num('TR_HK_NEXT_CODE');
  const nextMask = num('TR_HK_NEXT_MASK');
  const prevCode = num('TR_HK_PREV_CODE');
  const prevMask = num('TR_HK_PREV_MASK');
  const interesting = (1 << 17) | (1 << 18) | (1 << 19) | (1 << 20);
  $.NSEvent.addGlobalMonitorForEventsMatchingMaskHandler($.NSEventMaskKeyDown, function(event) {
    const code = event.keyCode;
    const mods = event.modifierFlags & interesting;
    if (code === nextCode && mods === nextMask) console.log('next');
    if (code === prevCode && mods === prevMask) console.log('prev');
  });
  $.NSRunLoop.currentRunLoop.run;
}
`

func startShortcuts(next, prev Shortcut, onNext, onPrev func()) (func(), error) {
	env := os.Environ()
	if !next.Empty() {
		code, mask, err := darwinBinding(next)
		if err != nil {
			return nil, err
		}
		env = append(env, "TR_HK_NEXT_CODE="+strconv.Itoa(code), "TR_HK_NEXT_MASK="+strconv.Itoa(mask))
	}
	if !prev.Empty() {
		code, mask, err := darwinBinding(prev)
		if err != nil {
			return nil, err
		}
		env = append(env, "TR_HK_PREV_CODE="+strconv.Itoa(code), "TR_HK_PREV_MASK="+strconv.Itoa(mask))
	}
	cmd := exec.Command("osascript", "-l", "JavaScript", "-")
	cmd.Stdin = strings.NewReader(hotkeyScript)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			switch strings.TrimSpace(sc.Text()) {
			case "next":
				if onNext != nil {
					onNext()
				}
			case "prev":
				if onPrev != nil {
					onPrev()
				}
			}
		}
	}()
	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	}
	return stop, nil
}

func darwinBinding(s Shortcut) (code, mask int, err error) {
	code, ok := darwinKeyCode(s.Key)
	if !ok {
		return 0, 0, fmt.Errorf("no macOS key code for %q", s.Key)
	}
	if s.Shift {
		mask |= 1 << 17
	}
	if s.Ctrl {
		mask |= 1 << 18
	}
	if s.Alt {
		mask |= 1 << 19
	}
	if s.Super {
		mask |= 1 << 20
	}
	return code, mask, nil
}

func darwinKeyCode(key string) (int, bool) {
	keys := map[string]int{
		"a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
		"z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0b, "q": 0x0c,
		"w": 0x0d, "e": 0x0e, "r": 0x0f, "y": 0x10, "t": 0x11,
		"1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17,
		"equal": 0x18, "9": 0x19, "7": 0x1a, "minus": 0x1b, "8": 0x1c, "0": 0x1d,
		"bracketright": 0x1e, "o": 0x1f, "u": 0x20, "bracketleft": 0x21, "i": 0x22, "p": 0x23,
		"enter": 0x24, "l": 0x25, "j": 0x26, "quote": 0x27, "k": 0x28, "semicolon": 0x29,
		"backslash": 0x2a, "comma": 0x2b, "slash": 0x2c, "n": 0x2d, "m": 0x2e, "period": 0x2f,
		"tab": 0x30, "space": 0x31, "escape": 0x35,
		"left": 0x7b, "right": 0x7c, "down": 0x7d, "up": 0x7e,
		"f1": 0x7a, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60, "f6": 0x61,
		"f7": 0x62, "f8": 0x64, "f9": 0x65, "f10": 0x6d, "f11": 0x67, "f12": 0x6f,
	}
	c, ok := keys[key]
	return c, ok
}
