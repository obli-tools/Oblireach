//go:build windows

package main

// Frameless windows: the app draws its own title bar instead of the white
// Windows one, for the main window and the
// detached session windows (webHost, webhost_windows.go).
//
// How (the technique of Windows Terminal):
//   - The windows keep WS_OVERLAPPEDWINDOW: resize borders, Aero Snap, the
//     minimize / maximize animations, the system menu, and a maximized window
//     fills the work area (never the taskbar).
//   - WM_NCCALCSIZE keeps the standard left, right and bottom frame (the
//     invisible resize borders outside the visible edge, the thin border and
//     the shadow, all drawn by DWM) and takes the title bar away: the client
//     area starts at the top of the window. Maximized, the window overhangs
//     the screen by its frame on every side: the top overhang is taken off
//     too, and an auto-hide taskbar keeps 2 px so that it can still pop up.
//   - The WebView2 covers the whole client area, so the PAGE draws the title
//     bar: its empty parts move the window (reachWindowDrag: Windows does the
//     move, with Snap and drag-to-restore), a double click maximizes, the
//     buttons on the right minimize / maximize / close (reachWindowControl),
//     and a thin band along the top edge resizes it (reachWindowDrag with an
//     edge); the other edges are the frame's own.
//   - A page that is not the local UI has no title bar of its own (the SSO
//     sign-in, an error page): the window then shows a native strip above
//     it (the window title, drag, double click, system menu, the same three
//     buttons), as it does while the WebView2 starts.
//   - The window class is ours from the start and the frame is recomputed
//     before the window is first shown: it never appears with a standard
//     frame.
//
// Sizes are given at 96 DPI and scaled with the window's DPI (the process is
// Per-Monitor V2 DPI aware: dpi_windows.go; a window moved onto a screen of
// another scale follows it, WM_DPICHANGED).

import (
	"encoding/json"
	"errors"
	"syscall"
	"unsafe"
)

var (
	procGetClientRect          = modUser32.NewProc("GetClientRect")
	procScreenToClient         = modUser32.NewProc("ScreenToClient")
	procGetCursorPos           = modUser32.NewProc("GetCursorPos")
	procSetCapture             = modUser32.NewProc("SetCapture")
	procReleaseCapture         = modUser32.NewProc("ReleaseCapture")
	procTrackMouseEvent        = modUser32.NewProc("TrackMouseEvent")
	procInvalidateRect         = modUser32.NewProc("InvalidateRect")
	procIsZoomed               = modUser32.NewProc("IsZoomed")
	procSetWindowPos           = modUser32.NewProc("SetWindowPos")
	procMonitorFromRect        = modUser32.NewProc("MonitorFromRect")
	procGetMonitorInfoW        = modUser32.NewProc("GetMonitorInfoW")
	procGetDpiForWindow        = modUser32.NewProc("GetDpiForWindow")
	procGetSystemMetrics       = modUser32.NewProc("GetSystemMetrics")
	procGetSystemMetricsForDpi = modUser32.NewProc("GetSystemMetricsForDpi")
	procGetWindowTextW         = modUser32.NewProc("GetWindowTextW")
	procBeginPaint             = modUser32.NewProc("BeginPaint")
	procEndPaint               = modUser32.NewProc("EndPaint")
	procFillRect               = modUser32.NewProc("FillRect")
	procDrawTextW              = modUser32.NewProc("DrawTextW")
	procSHAppBarMessage        = modShell32.NewProc("SHAppBarMessage")
	procCreateFontW            = modGdi32.NewProc("CreateFontW")
	procSelectObject           = modGdi32.NewProc("SelectObject")
	procDeleteObject           = modGdi32.NewProc("DeleteObject")
	procSetBkMode              = modGdi32.NewProc("SetBkMode")
	procSetTextColor           = modGdi32.NewProc("SetTextColor")
	procGetAsyncKeyState       = modUser32.NewProc("GetAsyncKeyState")
)

const (
	wmPaint          = 0x000F
	wmEraseBkgnd     = 0x0014
	wmNCCalcSize     = 0x0083
	wmNCHitTest      = 0x0084
	wmNCLButtonDown  = 0x00A1
	wmSysCommand     = 0x0112
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmCaptureChanged = 0x0215
	wmMouseLeave     = 0x02A3
	// Posted by reachWindowDrag (wParam: the hit-test code to drag with).
	wmHostDrag = 0x8000 + 0x22

	htClient   = 1
	htCaption  = 2
	htTop      = 12
	htTopLeft  = 13
	htTopRight = 14

	scMinimize = 0xF020
	scMaximize = 0xF030
	scClose    = 0xF060
	scRestore  = 0xF120

	waInactive       = 0
	smSwapButton     = 23
	smCySizeFrame    = 33
	smCxPaddedBorder = 92

	swpNoSize        = 0x0001
	swpNoMove        = 0x0002
	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swpFrameChanged  = 0x0020
	monitorNearest   = 2
	abmGetState      = 0x04
	abmGetAutohideEx = 0x0B
	absAutohide      = 0x01
	abeLeft          = 0
	abeTop           = 1
	abeRight         = 2
	abeBottom        = 3
	tmeLeave         = 0x02
	vkLButton        = 0x01
	vkRButton        = 0x02

	// An auto-hide taskbar keeps this much of a maximized window's edge, so
	// that the mouse still makes it pop up (Windows Terminal: 2).
	autohideTaskbarPx = 2

	// The native strip (96 DPI): caption buttons as wide as Windows 11's, and
	// how far from the left / right end the top band resizes diagonally.
	stripBtnW   = 46
	frameCorner = 16

	bkTransparent = 1
	dtLeft        = 0x0000
	dtCenter      = 0x0001
	dtVCenter     = 0x0004
	dtSingleLine  = 0x0020
	dtNoPrefix    = 0x0800
	dtEndEllipsis = 0x8000

	// Colours (COLORREF 0x00BBGGRR). The others are the theme's (theme.go
	// stripColors, currentPalette).
	colorClose     = 0x001c2bc4 // #c42b1c (Windows 11 close button)
	colorClosePush = 0x001a27b3
	colorWhite     = 0x00ffffff
)

// The native strip's buttons (0: none).
const (
	stripNone = iota
	stripMin
	stripMax
	stripClose
)

type winRect struct{ Left, Top, Right, Bottom int32 }

// MONITORINFO
type monitorInfo struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

// APPBARDATA
type appBarData struct {
	CbSize           uint32
	HWnd             uintptr
	UCallbackMessage uint32
	UEdge            uint32
	Rc               winRect
	LParam           uintptr
}

// PAINTSTRUCT
type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     winRect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

// TRACKMOUSEEVENT
type trackMouseEvent struct {
	CbSize      uint32
	DwFlags     uint32
	HwndTrack   uintptr
	DwHoverTime uint32
}

// frameState: the frame of one window (webHost.frame).
type frameState struct {
	minW, minH int32 // smallest window, at 96 DPI
	captionH   int32 // the page's title bar height at 96 DPI: the native strip's
	foreign    bool  // the page on screen is not the local UI: native strip above it

	hover, pressed int  // native strip buttons (stripNone...)
	tracking       bool // WM_MOUSELEAVE asked for

	active  bool                  // the window is the active one (WM_ACTIVATE)
	last    windowStateInfo       // last state told to the page
	told    bool                  // ... once at least
	onState func(windowStateInfo) // tells the page (its bridge)

	// The move under way was started by dragging a window's ONLY tab
	// (reachWindowDrag "tab"): when Windows' move loop ends, onTabDragEnd
	// looks where it was dropped (onto another window's tab strip: the tab
	// joins it, detached_windows.go tabDragEnded).
	tabDrag      bool
	onTabDragEnd func()

	// The app positions the window itself (webHost.place, dpi_windows.go):
	// its rectangle is meant for the screen it goes to, a WM_DPICHANGED
	// meanwhile keeps it.
	placing bool
}

// ── Pure geometry (frame_windows_test.go) ──────────────────────────────────

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

// scaleDpi: a 96 DPI size at dpi, rounded.
func scaleDpi(v, dpi int32) int32 {
	if dpi <= 0 {
		dpi = 96
	}
	return (v*dpi + 48) / 96
}

// frameClientRect: the client area of a frameless window, from the window
// rectangle Windows proposes and the standard client area DefWindowProc made
// of it: the standard left, right and bottom frame, no title bar. Maximized,
// the window overhangs the screen by overhang at the top as on the other
// sides (that part is not on screen), and an auto-hide taskbar on an edge
// (indexed abeLeft...abeBottom) keeps autohideTaskbarPx of it.
func frameClientRect(proposed, standard winRect, maximized bool, overhang int32, autohide [4]bool) winRect {
	r := standard
	r.Top = proposed.Top
	if maximized {
		r.Top += overhang
		if autohide[abeLeft] {
			r.Left += autohideTaskbarPx
		}
		if autohide[abeTop] {
			r.Top += autohideTaskbarPx
		}
		if autohide[abeRight] {
			r.Right -= autohideTaskbarPx
		}
		if autohide[abeBottom] {
			r.Bottom -= autohideTaskbarPx
		}
	}
	if r.Bottom < r.Top {
		r.Bottom = r.Top
	}
	if r.Right < r.Left {
		r.Right = r.Left
	}
	return r
}

// frameHit: what a point of the client area is (x, y: client coordinates, w:
// client width), where the default procedure only saw client area: the top
// resize band (band high, diagonal within corner of either end; none when
// maximized), the native strip (strip high: the title, or one of its three
// btnW-wide buttons at the right end, handled by the window itself) or the
// page.
func frameHit(x, y, w int32, maximized bool, band, corner, strip, btnW int32) uintptr {
	if !maximized && y >= 0 && y < band {
		switch {
		case x < corner:
			return htTopLeft
		case x >= w-corner:
			return htTopRight
		}
		return htTop
	}
	if y >= 0 && y < strip {
		if stripButtonAt(x, y, w, strip, btnW) != stripNone {
			return htClient
		}
		return htCaption
	}
	return htClient
}

// stripButtonAt: the native strip button at (x, y), from the right end:
// close, maximize / restore, minimize.
func stripButtonAt(x, y, w, strip, btnW int32) int {
	if btnW <= 0 || y < 0 || y >= strip || x >= w || x < w-3*btnW {
		return stripNone
	}
	return stripMin + int((x-(w-3*btnW))/btnW)
}

// stripButtonRect: where button b is drawn.
func stripButtonRect(b int, w, strip, btnW int32) winRect {
	left := w - int32(stripClose-b+1)*btnW
	return winRect{left, 0, left + btnW, strip}
}

// dragHitFor: what a press on the page's title bar does (reachWindowDrag):
// "" moves the window; "top", "topleft", "topright" resize it from the band
// along its top edge (the other edges are the standard frame's).
func dragHitFor(edge string) (uintptr, bool) {
	switch edge {
	case "", "tab": // "tab": the window's only tab dragged (it may join another window)
		return htCaption, true
	case "top":
		return htTop, true
	case "topleft":
		return htTopLeft, true
	case "topright":
		return htTopRight, true
	}
	return 0, false
}

// sysCommandFor: the system command of a title bar button
// (reachWindowControl); "maximize" toggles.
func sysCommandFor(action string, maximized bool) (uintptr, bool) {
	switch action {
	case "minimize":
		return scMinimize, true
	case "maximize":
		if maximized {
			return scRestore, true
		}
		return scMaximize, true
	case "close":
		return scClose, true
	}
	return 0, false
}

// pointsLParam: a screen point as the lParam of a WM_NC* mouse message.
func pointsLParam(p winPoint) uintptr {
	return uintptr(uint16(int16(p.X))) | uintptr(uint16(int16(p.Y)))<<16
}

// lParamPoint: the (signed) point of a mouse message.
func lParamPoint(lp uintptr) winPoint {
	return winPoint{int32(int16(uint16(lp))), int32(int16(uint16(lp >> 16)))}
}

// ── Win32 helpers ──────────────────────────────────────────────────────────

func dpiOf(hwnd uintptr) int32 {
	if hwnd != 0 && procGetDpiForWindow.Find() == nil {
		if d, _, _ := procGetDpiForWindow.Call(hwnd); d > 0 {
			return int32(d)
		}
	}
	return 96
}

func systemMetric(index int, dpi int32) int32 {
	if procGetSystemMetricsForDpi.Find() == nil {
		r, _, _ := procGetSystemMetricsForDpi.Call(uintptr(index), uintptr(dpi))
		return int32(r)
	}
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int32(r)
}

// resizeBand: the height of the top resize band, i.e. the frame thickness
// (the invisible resize border plus its padding) at the window's DPI.
func resizeBand(hwnd uintptr) int32 {
	dpi := dpiOf(hwnd)
	return systemMetric(smCySizeFrame, dpi) + systemMetric(smCxPaddedBorder, dpi)
}

// primaryButtonDown: the (logical) left mouse button is down right now.
func primaryButtonDown() bool {
	vk := uintptr(vkLButton)
	if systemMetric(smSwapButton, 96) != 0 {
		vk = vkRButton // GetAsyncKeyState reads the physical buttons
	}
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}

func isZoomed(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

// frameReady recomputes the frame of a window just created: the first
// WM_NCCALCSIZE arrives inside CreateWindowEx, before the window is known to
// its procedure. Before the window is first shown.
func frameReady(hwnd uintptr) {
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFrameChanged|swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate)
}

// autohideTaskbars: the edges of the monitor of wr that have an auto-hide
// taskbar.
func autohideTaskbars(wr winRect) (out [4]bool) {
	st := appBarData{}
	st.CbSize = uint32(unsafe.Sizeof(st))
	if r, _, _ := procSHAppBarMessage.Call(abmGetState, uintptr(unsafe.Pointer(&st))); r&absAutohide == 0 {
		return
	}
	mon, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&wr)), monitorNearest)
	if mon == 0 {
		return
	}
	mi := monitorInfo{}
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return
	}
	for edge := abeLeft; edge <= abeBottom; edge++ {
		d := appBarData{UEdge: uint32(edge), Rc: mi.RcMonitor}
		d.CbSize = uint32(unsafe.Sizeof(d))
		if bar, _, _ := procSHAppBarMessage.Call(abmGetAutohideEx, uintptr(unsafe.Pointer(&d))); bar != 0 {
			out[edge] = true
		}
	}
	return
}

// ── The frame of a webHost ─────────────────────────────────────────────────

// stripShown: the native strip is on screen (the WebView2 is not there yet,
// or shows a page without a title bar of its own).
func (h *webHost) stripShown() bool { return !h.ready || h.frame.foreign }

func (h *webHost) stripHeight(dpi int32) int32 {
	if !h.stripShown() {
		return 0
	}
	return scaleDpi(h.frame.captionH, dpi)
}

// webTop: how far down the page starts (below the native strip).
func (h *webHost) webTop() int32 {
	if !h.frame.foreign {
		return 0
	}
	return scaleDpi(h.frame.captionH, dpiOf(h.hwnd))
}

// frameProc handles the frame's messages. done: answered (ret); otherwise
// the window's own procedure goes on (and then DefWindowProc).
func (h *webHost) frameProc(msg, wParam, lParam uintptr) (ret uintptr, done bool) {
	switch msg {
	case wmNCCalcSize:
		return h.onNCCalcSize(wParam, lParam), true
	case wmNCHitTest:
		return h.onNCHitTest(wParam, lParam), true
	case wmHostDrag:
		// The press the page saw is handed to Windows as a press on a title
		// bar (or on the top border), whose move / size loop runs here, from
		// the message loop. Only while the button is still down: a click
		// already released would leave the window stuck to the mouse.
		tabDrag := h.frame.tabDrag
		h.frame.tabDrag = false
		if primaryButtonDown() && (wParam == htCaption || !isZoomed(h.hwnd)) {
			var pt winPoint
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			// (Windows' move loop: returns once the button is released)
			procDefWindowProcW.Call(h.hwnd, wmNCLButtonDown, wParam, pointsLParam(pt))
			if tabDrag && h.frame.onTabDragEnd != nil && !h.finished {
				h.frame.onTabDragEnd()
			}
		}
		return 0, true
	case wmGetMinMaxInfo:
		dpi := dpiOf(h.hwnd)
		mmi := (*minMaxInfo)(globalPtr(lParam))
		mmi.PtMinTrackSize = winPoint{scaleDpi(h.frame.minW, dpi), scaleDpi(h.frame.minH, dpi)}
		return 0, true
	case wmDpiChanged:
		h.onDpiChanged(lParam) // (dpi_windows.go)
		return 0, true
	case wmSize:
		h.resize()
		h.pushState()
	case wmMove, wmMoving:
		if h.ready && !h.finished {
			_ = h.chromium.NotifyParentWindowPositionChanged()
		}
	case wmActivate:
		h.frame.active = wParam&0xFFFF != waInactive
		h.pushState()
	case wmEraseBkgnd:
		if h.stripShown() {
			return 1, true // paintStrip paints it all (no flicker on hover)
		}
		// Below the WebView2 (seen while it resizes): the theme's background
		// now, not the class brush of the theme the window class was made in.
		var rc winRect
		procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&rc)))
		fillRect(wParam, rc, uintptr(colorRef(currentPalette().Bg)))
		return 1, true
	case wmPaint:
		if h.stripShown() {
			h.paintStrip()
			return 0, true
		}
	case wmMouseMove:
		if !h.frame.tracking {
			tme := trackMouseEvent{DwFlags: tmeLeave, HwndTrack: h.hwnd}
			tme.CbSize = uint32(unsafe.Sizeof(tme))
			if r, _, _ := procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme))); r != 0 {
				h.frame.tracking = true
			}
		}
		h.setStripHover(h.stripButtonUnder(lParam))
	case wmMouseLeave:
		h.frame.tracking = false
		h.setStripHover(stripNone)
	case wmLButtonDown:
		if b := h.stripButtonUnder(lParam); b != stripNone {
			h.frame.pressed = b
			procSetCapture.Call(h.hwnd)
			h.invalidate()
			return 0, true
		}
	case wmLButtonUp:
		if p := h.frame.pressed; p != stripNone {
			h.frame.pressed = stripNone
			procReleaseCapture.Call()
			h.invalidate()
			if h.stripButtonUnder(lParam) == p {
				h.stripCommand(p)
			}
			return 0, true
		}
	case wmCaptureChanged:
		if h.frame.pressed != stripNone {
			h.frame.pressed = stripNone
			h.invalidate()
		}
	}
	return 0, false
}

func (h *webHost) onNCCalcSize(wParam, lParam uintptr) uintptr {
	// rgrc[0] of NCCALCSIZE_PARAMS (wParam TRUE) or the RECT itself (FALSE):
	// the proposed window rectangle in, the client area out.
	rp := (*winRect)(globalPtr(lParam))
	proposed := *rp
	if r, _, _ := procDefWindowProcW.Call(h.hwnd, wmNCCalcSize, wParam, lParam); r != 0 {
		return r
	}
	maximized := isZoomed(h.hwnd)
	var overhang int32
	var hide [4]bool
	if maximized {
		// The overhang is the frame DefWindowProc just took off the bottom.
		if overhang = proposed.Bottom - rp.Bottom; overhang <= 0 {
			overhang = resizeBand(h.hwnd)
		}
		hide = autohideTaskbars(proposed)
	}
	*rp = frameClientRect(proposed, *rp, maximized, overhang, hide)
	return 0
}

func (h *webHost) onNCHitTest(wParam, lParam uintptr) uintptr {
	// The standard frame (left, right, bottom and their corners) first.
	if r, _, _ := procDefWindowProcW.Call(h.hwnd, wmNCHitTest, wParam, lParam); r != htClient {
		return r
	}
	pt := lParamPoint(lParam)
	procScreenToClient.Call(h.hwnd, uintptr(unsafe.Pointer(&pt)))
	var rc winRect
	procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&rc)))
	dpi := dpiOf(h.hwnd)
	return frameHit(pt.X, pt.Y, rc.Right, isZoomed(h.hwnd), resizeBand(h.hwnd), scaleDpi(frameCorner, dpi),
		h.stripHeight(dpi), scaleDpi(stripBtnW, dpi))
}

// stripButtonUnder: the native strip button under a mouse message's point.
func (h *webHost) stripButtonUnder(lParam uintptr) int {
	if !h.stripShown() {
		return stripNone
	}
	p := lParamPoint(lParam)
	var rc winRect
	procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&rc)))
	dpi := dpiOf(h.hwnd)
	return stripButtonAt(p.X, p.Y, rc.Right, h.stripHeight(dpi), scaleDpi(stripBtnW, dpi))
}

func (h *webHost) setStripHover(b int) {
	if b != h.frame.hover {
		h.frame.hover = b
		h.invalidate()
	}
}

// stripCommand: a native strip button was clicked.
func (h *webHost) stripCommand(b int) {
	action := map[int]string{stripMin: "minimize", stripMax: "maximize", stripClose: "close"}[b]
	if sc, ok := sysCommandFor(action, isZoomed(h.hwnd)); ok {
		procPostMessageW.Call(h.hwnd, wmSysCommand, sc, 0)
	}
}

// paintStrip draws the native strip (and the background below it until the
// WebView2 covers it): the window title and three caption buttons in the
// theme's colours (theme.go), glyphs from Segoe MDL2 Assets (Windows 10 and 11).
func (h *webHost) paintStrip() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(h.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(h.hwnd, uintptr(unsafe.Pointer(&ps)))
	var rc winRect
	procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&rc)))
	dpi := dpiOf(h.hwnd)
	sh, bw := min32(h.stripHeight(dpi), rc.Bottom), scaleDpi(stripBtnW, dpi)
	pal := currentPalette()
	s, t, g, hv, pr := stripColors(pal)
	colorStrip, colorStripText, colorGlyph := uintptr(colorRef(s)), uintptr(colorRef(t)), uintptr(colorRef(g))
	colorHover, colorPressed := uintptr(colorRef(hv)), uintptr(colorRef(pr))
	fillRect(hdc, winRect{0, 0, rc.Right, sh}, colorStrip)
	if sh < rc.Bottom {
		fillRect(hdc, winRect{0, sh, rc.Right, rc.Bottom}, uintptr(colorRef(pal.Bg)))
	}
	procSetBkMode.Call(hdc, bkTransparent)

	title := createFont("Segoe UI", scaleDpi(12, dpi), 600)
	old, _, _ := procSelectObject.Call(hdc, title)
	procSetTextColor.Call(hdc, colorStripText)
	tr := winRect{scaleDpi(14, dpi), 0, rc.Right - 3*bw - scaleDpi(8, dpi), sh}
	drawText(hdc, windowText(h.hwnd), tr, dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis|dtNoPrefix)

	glyphs := createFont("Segoe MDL2 Assets", scaleDpi(10, dpi), 400)
	procSelectObject.Call(hdc, glyphs)
	maximized := isZoomed(h.hwnd)
	for b := stripMin; b <= stripClose; b++ {
		br := stripButtonRect(b, rc.Right, sh, bw)
		bg, fg := colorStrip, colorGlyph
		switch {
		case b == stripClose && h.frame.pressed == b:
			bg, fg = colorClosePush, colorWhite
		case b == stripClose && h.frame.hover == b:
			bg, fg = colorClose, colorWhite
		case h.frame.pressed == b:
			bg = colorPressed
		case h.frame.hover == b:
			bg = colorHover
		}
		if bg != colorStrip {
			fillRect(hdc, br, bg)
		}
		glyph := map[int]string{stripMin: "", stripMax: "", stripClose: ""}[b]
		if b == stripMax && maximized {
			glyph = ""
		}
		procSetTextColor.Call(hdc, fg)
		drawText(hdc, glyph, br, dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	}
	procSelectObject.Call(hdc, old)
	procDeleteObject.Call(title)
	procDeleteObject.Call(glyphs)
}

func fillRect(hdc uintptr, r winRect, color uintptr) {
	br, _, _ := procCreateSolidBrush.Call(color)
	if br == 0 {
		return
	}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), br)
	procDeleteObject.Call(br)
}

func createFont(face string, px int32, weight int32) uintptr {
	f, _ := syscall.UTF16PtrFromString(face)
	// height < 0: character height; DEFAULT_CHARSET, CLEARTYPE_QUALITY.
	h, _, _ := procCreateFontW.Call(uintptr(-px), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(f)))
	return h
}

func drawText(hdc uintptr, s string, r winRect, format uintptr) {
	u, err := syscall.UTF16FromString(s)
	if err != nil || len(u) < 2 {
		return
	}
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), format)
}

func windowText(hwnd uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

// ── Window state, told to the page ─────────────────────────────────────────

// (windowStateInfo, what the page's title bar shows: platform.go.)

func (h *webHost) windowState() windowStateInfo {
	return windowStateInfo{Maximized: isZoomed(h.hwnd), Active: h.frame.active}
}

// pushState tells the page when the state changed (WM_SIZE, WM_ACTIVATE).
func (h *webHost) pushState() {
	if h.frame.onState == nil || h.finished {
		return
	}
	s := h.windowState()
	if h.frame.told && s == h.frame.last {
		return
	}
	h.frame.told, h.frame.last = true, s
	h.frame.onState(s)
}

// ── Bindings (per window, like the others: native_bridge.go) ───────────────

// (frameOps, what the title bar bindings do to their window: platform.go.)

// hostFrame: the frameOps of a real window.
type hostFrame struct{ h *webHost }

// startDrag hands the press to Windows as if it were on a title bar (or on
// the top border): the capture the page's press took is released (the page
// drops the press), and wmHostDrag is posted, not handled here, so that
// Windows' move loop runs from the message loop, outside the WebView2 event
// that called the binding.
func (f hostFrame) startDrag(hit uintptr) {
	procReleaseCapture.Call()
	procPostMessageW.Call(f.h.hwnd, wmHostDrag, hit, 0)
}

func (f hostFrame) sysCommand(sc uintptr) { procPostMessageW.Call(f.h.hwnd, wmSysCommand, sc, 0) }

func (f hostFrame) state() windowStateInfo { return f.h.windowState() }

var errBadArgument = errors.New("invalid argument")

// windowDrag (JS reachWindowDrag): the page's title bar was pressed.
func (b *nativeBridge) windowDrag(tok, edge string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	hit, ok := dragHitFor(edge)
	if !ok {
		return false, errBadArgument
	}
	if b.win == nil {
		return false, nil
	}
	if hit != htCaption && b.win.state().Maximized {
		return false, nil // a maximized window has no border to resize
	}
	if hf, ok := b.win.(hostFrame); ok && hf.h != nil {
		hf.h.frame.tabDrag = edge == "tab" && b.detached != nil
	}
	b.win.startDrag(hit)
	return true, nil
}

// windowControl (JS reachWindowControl): "minimize", "maximize" (toggles) or
// "close" (a detached window: its session ends; the main window: the app).
func (b *nativeBridge) windowControl(tok, action string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	maximized := b.win != nil && b.win.state().Maximized
	sc, ok := sysCommandFor(action, maximized)
	if !ok {
		return false, errBadArgument
	}
	if b.win == nil {
		return false, nil
	}
	b.win.sysCommand(sc)
	return true, nil
}

// windowState (JS reachWindowState): the state now (the page asks when it
// loads; later changes come by __reachOnWindowState).
func (b *nativeBridge) windowState(tok string) (windowStateInfo, error) {
	if !b.allowed(tok) {
		return windowStateInfo{}, errNotAllowed
	}
	if b.win == nil {
		return windowStateInfo{Active: true}, nil
	}
	return b.win.state(), nil
}

// pushWindowState (webHost.frame.onState): the page's title bar follows.
func (b *nativeBridge) pushWindowState(s windowStateInfo) {
	j, _ := json.Marshal(s)
	b.evalMain("__reachOnWindowState", string(j))
}
