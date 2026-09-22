//go:build windows

package window

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procRegisterHotKey     = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = user32.NewProc("UnregisterHotKey")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procPostThreadMessageW = user32.NewProc("PostThreadMessageW")
)

const (
	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNoRepeat = 0x4000
	wmHotkey    = 0x0312
	wmQuit      = 0x0012
)

type winPoint struct{ X, Y int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
	Private uint32
}

func startShortcuts(next, prev Shortcut, onNext, onPrev func()) (func(), error) {
	type binding struct {
		id   uintptr
		mods uintptr
		vk   uintptr
		fire func()
	}
	var bindings []binding
	if !next.Empty() {
		vk, err := virtualKey(next.Key)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding{id: 1, mods: winMods(next), vk: vk, fire: onNext})
	}
	if !prev.Empty() {
		vk, err := virtualKey(prev.Key)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding{id: 2, mods: winMods(prev), vk: vk, fire: onPrev})
	}
	ready := make(chan error, 1)
	var tid uint32
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if tid != 0 {
				_, _, _ = procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
			}
		})
	}
	go func() {
		tid = windows.GetCurrentThreadId()
		for _, b := range bindings {
			r, _, err := procRegisterHotKey.Call(0, b.id, b.mods|modNoRepeat, b.vk)
			if r == 0 {
				for _, prev := range bindings {
					if prev.id == b.id {
						break
					}
					_, _, _ = procUnregisterHotKey.Call(0, prev.id)
				}
				ready <- fmt.Errorf("register %s: %v", describe(b.id), err)
				return
			}
		}
		ready <- nil
		var msg winMsg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			if msg.Message == wmHotkey {
				for _, b := range bindings {
					if msg.WParam == b.id && b.fire != nil {
						b.fire()
					}
				}
			}
		}
		for _, b := range bindings {
			_, _, _ = procUnregisterHotKey.Call(0, b.id)
		}
	}()
	if err := <-ready; err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

func describe(id uintptr) string {
	if id == 1 {
		return "focus next"
	}
	return "focus previous"
}

func winMods(s Shortcut) uintptr {
	var m uintptr
	if s.Alt {
		m |= modAlt
	}
	if s.Ctrl {
		m |= modControl
	}
	if s.Shift {
		m |= modShift
	}
	if s.Super {
		m |= modWin
	}
	return m
}

func virtualKey(key string) (uintptr, error) {
	if len(key) == 1 && key[0] >= 'a' && key[0] <= 'z' {
		return uintptr(key[0] - 'a' + 0x41), nil
	}
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		return uintptr(key[0]), nil
	}
	keys := map[string]uintptr{
		"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28,
		"tab": 0x09, "space": 0x20, "enter": 0x0d, "escape": 0x1b,
		"minus": 0xbd, "equal": 0xbb, "bracketleft": 0xdb, "bracketright": 0xdd,
		"semicolon": 0xba, "quote": 0xde, "comma": 0xbc, "period": 0xbe,
		"slash": 0xbf, "backslash": 0xdc, "grave": 0xc0,
	}
	if v, ok := keys[key]; ok {
		return v, nil
	}
	if len(key) >= 2 && key[0] == 'f' {
		n := 0
		for _, c := range key[1:] {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("no virtual key for %q", key)
			}
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 12 {
			return uintptr(0x70 + n - 1), nil
		}
	}
	return 0, fmt.Errorf("no virtual key for %q", key)
}
