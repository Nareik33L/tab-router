//go:build windows

package window

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	shcore                     = windows.NewLazySystemDLL("shcore.dll")
	procEnumDisplayMonitors    = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procSetProcessDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForMonitor       = shcore.NewProc("GetDpiForMonitor")
	dpiOnce                    sync.Once
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

type monitorInfoEx struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
	Device    [32]uint16
}

const monitorInfoPrimary = 1

// Detect lists monitors in physical pixels. The process is made per-monitor
// DPI aware first so the work area matches Chromium when
// --force-device-scale-factor is 1. rcWork already excludes the taskbar.
func Detect() ([]Display, error) {
	enablePerMonitorDPI()
	var list []Display
	cb := windows.NewCallback(func(hmon, hdc, prect, data uintptr) uintptr {
		var mi monitorInfoEx
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi)))
		if r == 0 {
			return 1
		}
		name := windows.UTF16ToString(mi.Device[:])
		scale := monitorScale(hmon)
		list = append(list, Display{
			ID:      name,
			Name:    name,
			Primary: mi.DwFlags&monitorInfoPrimary != 0,
			Scale:   scale,
			Bounds:  rectFromWin(mi.RcMonitor),
			Work:    rectFromWin(mi.RcWork),
		})
		return 1
	})
	r, _, err := procEnumDisplayMonitors.Call(0, 0, cb, 0)
	if r == 0 {
		return nil, fmt.Errorf("list displays: %v", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no displays reported")
	}
	if !anyPrimary(list) {
		list[0].Primary = true
	}
	return list, nil
}

func enablePerMonitorDPI() {
	dpiOnce.Do(func() {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is (HANDLE)-4.
		_, _, _ = procSetProcessDpiAwareness.Call(^uintptr(3))
	})
}

func monitorScale(hmon uintptr) float64 {
	var x, y uint32
	hr, _, _ := procGetDpiForMonitor.Call(hmon, 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y)))
	if hr != 0 || x == 0 {
		return 1
	}
	return float64(x) / 96
}

func rectFromWin(r winRect) Rect {
	return Rect{X: int(r.Left), Y: int(r.Top), Width: int(r.Right - r.Left), Height: int(r.Bottom - r.Top)}
}

// ForegroundPID is the process that owns the foreground window.
func ForegroundPID() (int, bool) {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return 0, false
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid == 0 {
		return 0, false
	}
	return int(pid), true
}
