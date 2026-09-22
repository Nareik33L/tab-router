package window

import (
	"fmt"
	"strings"
)

// Shortcut is an optional global focus binding. A zero Key means the
// binding is disabled. Shortcuts never move the pointer; they only ask a
// managed window to become active.
type Shortcut struct {
	Ctrl  bool
	Alt   bool
	Shift bool
	Super bool
	Key   string
}

// Empty reports whether the shortcut is disabled.
func (s Shortcut) Empty() bool { return s.Key == "" }

func (s Shortcut) String() string {
	if s.Empty() {
		return ""
	}
	var parts []string
	if s.Ctrl {
		parts = append(parts, "ctrl")
	}
	if s.Alt {
		parts = append(parts, "alt")
	}
	if s.Shift {
		parts = append(parts, "shift")
	}
	if s.Super {
		parts = append(parts, "super")
	}
	parts = append(parts, s.Key)
	return strings.Join(parts, "+")
}

// ParseShortcut reads values such as "ctrl+alt+right" or "super+]".
// An empty string is a disabled shortcut.
func ParseShortcut(s string) (Shortcut, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return Shortcut{}, nil
	}
	parts := strings.Split(s, "+")
	var sc Shortcut
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return Shortcut{}, fmt.Errorf("window shortcut %q is not valid", s)
		}
		if i == len(parts)-1 {
			key, ok := normalizeKey(p)
			if !ok {
				return Shortcut{}, fmt.Errorf("window shortcut %q has unknown key %q", s, p)
			}
			sc.Key = key
			continue
		}
		switch p {
		case "ctrl", "control":
			sc.Ctrl = true
		case "alt", "option", "opt":
			sc.Alt = true
		case "shift":
			sc.Shift = true
		case "super", "cmd", "command", "win", "meta":
			sc.Super = true
		default:
			return Shortcut{}, fmt.Errorf("window shortcut %q has unknown modifier %q", s, p)
		}
	}
	if sc.Key == "" {
		return Shortcut{}, fmt.Errorf("window shortcut %q has no key", s)
	}
	return sc, nil
}

func normalizeKey(p string) (string, bool) {
	switch p {
	case "left", "arrowleft":
		return "left", true
	case "right", "arrowright":
		return "right", true
	case "up", "arrowup":
		return "up", true
	case "down", "arrowdown":
		return "down", true
	case "tab", "space":
		return p, true
	case "enter", "return":
		return "enter", true
	case "esc", "escape":
		return "escape", true
	case "[":
		return "bracketleft", true
	case "]":
		return "bracketright", true
	case "bracketleft", "bracketright", "minus", "equal", "comma", "period", "slash", "backslash", "grave", "semicolon", "quote":
		return p, true
	}
	if len(p) == 1 && p[0] >= 'a' && p[0] <= 'z' {
		return p, true
	}
	if len(p) == 1 && p[0] >= '0' && p[0] <= '9' {
		return p, true
	}
	if len(p) >= 2 && (p[0] == 'f') {
		n := 0
		for _, c := range p[1:] {
			if c < '0' || c > '9' {
				return "", false
			}
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 12 {
			return p, true
		}
	}
	return "", false
}
