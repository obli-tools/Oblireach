//go:build windows

package main

// Per-monitor DPI awareness (V2).
//
// Desktop is Per-Monitor V2 DPI aware: its manifest says so
// (oblireach.manifest, embedded in resource.syso by goversioninfo:
// versioninfo.json, build.bat), and main calls enableDpiAwareness before any
// window exists, for a build without that resource (a plain go build, the
// tests). What it changes:
//   - every coordinate of the process is a physical pixel: the "Receive help"
//     host (the engine in-process: its capture, SendInput, the virtual
//     screen) maps the operator's pointer right on every screen, whatever its
//     scale. The engine's EnablePerMonitorDPIAwareness asks for the same mode
//     (Desktop does not call it; were it called, it would find the mode set
//     and change nothing);
//   - the windows are sharp (no bitmap stretching by DWM), and each WebView2
//     follows the scale of the screen its window is on (its rasterization
//     scale; the controller's bounds are physical pixels: webHost.resize);
//   - sizes given at 96 DPI are scaled with the window's DPI (scaleDpi): the
//     first size of a window (initialWindowRect), its frame
//     (frame_windows.go), a torn-off tab's window landing on a screen of
//     another scale (tearOffSize);
//   - a window moved onto a screen of another scale gets WM_DPICHANGED and
//     takes the rectangle Windows suggests (frameProc), except while the app
//     places it itself (frameState.placing: that rectangle is already meant
//     for the screen it goes to);
//   - a native rectangle the page gives in CSS pixels is converted by the
//     page (devicePixelRatio: rdp_view.go rdpRectOf).
// The window rectangles kept (sessionWindows.places, GetWindowPlacement) are
// physical pixels of the screen they were on, and go back there as they are.

import (
	"syscall"
	"unsafe"
)

var (
	procSetProcessDpiAwarenessContext = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procGetThreadDpiAwarenessContext  = modUser32.NewProc("GetThreadDpiAwarenessContext")
	procAreDpiAwarenessContextsEqual  = modUser32.NewProc("AreDpiAwarenessContextsEqual")
	procGetDpiForMonitor              = modShcore.NewProc("GetDpiForMonitor")
	procSetProcessDpiAwareness        = modShcore.NewProc("SetProcessDpiAwareness")
)

// (shcore.dll is one of the KnownDLLs, like user32 or ole32: always the
// system's copy.)
var modShcore = syscall.NewLazyDLL("shcore.dll")

const (
	wmDpiChanged = 0x02E0

	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 / _PER_MONITOR_AWARE ((HANDLE)-4, -3).
	dpiContextPerMonitorV2 = ^uintptr(3)
	dpiContextPerMonitor   = ^uintptr(2)
	processPerMonitorAware = 2 // PROCESS_PER_MONITOR_DPI_AWARE (shcore)
	mdtEffectiveDpi        = 0 // MDT_EFFECTIVE_DPI
)

// enableDpiAwareness makes the process Per-Monitor V2 DPI aware (else
// Per-Monitor, on a Windows 10 older than 1703), before any window exists.
// When the manifest set it already, the calls fail (the mode is set once per
// process) and nothing changes. True: the process is Per-Monitor V2 aware.
func enableDpiAwareness() bool {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		if r, _, _ := procSetProcessDpiAwarenessContext.Call(dpiContextPerMonitorV2); r == 0 && !perMonitorV2() {
			procSetProcessDpiAwarenessContext.Call(dpiContextPerMonitor)
		}
	} else if procSetProcessDpiAwareness.Find() == nil {
		procSetProcessDpiAwareness.Call(processPerMonitorAware)
	}
	return perMonitorV2()
}

// perMonitorV2: the UI thread (and so the process, whose default it is) is
// Per-Monitor V2 DPI aware.
func perMonitorV2() bool {
	if procGetThreadDpiAwarenessContext.Find() != nil || procAreDpiAwarenessContextsEqual.Find() != nil {
		return false
	}
	ctx, _, _ := procGetThreadDpiAwarenessContext.Call()
	r, _, _ := procAreDpiAwarenessContextsEqual.Call(ctx, dpiContextPerMonitorV2)
	return r != 0
}

// monitorDpi: the DPI of a screen (its effective DPI: its scale), 96 when
// unknown.
func monitorDpi(mon uintptr) int32 {
	if mon == 0 || procGetDpiForMonitor.Find() != nil {
		return 96
	}
	var x, y uint32
	if hr, _, _ := procGetDpiForMonitor.Call(mon, mdtEffectiveDpi, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y))); int32(hr) < 0 || x == 0 {
		return 96
	}
	return int32(x)
}

// workAreaOf: the work area of the screen nearest to rc (zero when unknown).
func workAreaOf(rc winRect) winRect {
	mon, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&rc)), monitorNearest)
	if mon == 0 {
		return winRect{}
	}
	mi := monitorInfo{}
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return winRect{}
	}
	return mi.RcWork
}

// ── Pure geometry (dpi_windows_test.go) ────────────────────────────────────

// scaleBetween: a size in pixels at DPI from, in pixels at DPI to, rounded.
func scaleBetween(v, from, to int32) int32 {
	if from <= 0 || to <= 0 || from == to {
		return v
	}
	return int32((int64(v)*int64(to) + int64(from)/2) / int64(from))
}

// tearOffSize: the size of a torn-off tab's window, size being its old
// window's (pixels at DPI from) and to the DPI of the screen it lands on:
// the same size in DIPs (a window of 1280 px at 100 % is 1920 px at 150 %).
func tearOffSize(size winPoint, from, to int32) winPoint {
	return winPoint{scaleBetween(size.X, from, to), scaleBetween(size.Y, from, to)}
}

// initialWindowRect: the first rectangle of a new window: where Windows put
// it (cur: its top-left corner), size big (pixels at the screen's DPI), kept
// inside the work area of its screen (work; zero: unknown): never larger,
// moved back in when it would overflow its right or bottom edge.
func initialWindowRect(cur winRect, size winPoint, work winRect) winRect {
	left, top := cur.Left, cur.Top
	if work.Right > work.Left && work.Bottom > work.Top {
		if ww := work.Right - work.Left; size.X > ww {
			size.X = ww
		}
		if wh := work.Bottom - work.Top; size.Y > wh {
			size.Y = wh
		}
		if left+size.X > work.Right {
			left = work.Right - size.X
		}
		if top+size.Y > work.Bottom {
			top = work.Bottom - size.Y
		}
		if left < work.Left {
			left = work.Left
		}
		if top < work.Top {
			top = work.Top
		}
	}
	return winRect{left, top, left + size.X, top + size.Y}
}

// ── Windows ────────────────────────────────────────────────────────────────

// place runs f, which positions the window itself (its rectangle meant for
// the screen it goes to): a WM_DPICHANGED meanwhile does not resize it again.
func (h *webHost) place(f func()) {
	was := h.frame.placing
	h.frame.placing = true
	defer func() { h.frame.placing = was }()
	f()
}

// setInitialSize: a window just created (not shown yet) gets its first size,
// w x hh pixels at the DPI of the screen Windows put it on, inside its work
// area.
func (h *webHost) setInitialSize(w, hh int32) {
	var rc winRect
	procGetWindowRectDW.Call(h.hwnd, uintptr(unsafe.Pointer(&rc)))
	r := initialWindowRect(rc, winPoint{w, hh}, workAreaOf(rc))
	h.place(func() {
		procSetWindowPos.Call(h.hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top),
			swpNoZOrder|swpNoActivate|swpFrameChanged)
	})
}

// onDpiChanged (WM_DPICHANGED): the window is now on a screen of another
// scale: the rectangle Windows suggests (lParam: the same size in DIPs, under
// the mouse when the operator drags it), unless the app is placing it; then
// the WebView2's bounds and the native strip (its sizes follow dpiOf).
func (h *webHost) onDpiChanged(lParam uintptr) {
	if !h.frame.placing && lParam != 0 {
		r := *(*winRect)(globalPtr(lParam))
		if r.Right > r.Left && r.Bottom > r.Top {
			procSetWindowPos.Call(h.hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top),
				swpNoZOrder|swpNoActivate|swpFrameChanged)
		}
	}
	h.resize()
	h.invalidate()
}
