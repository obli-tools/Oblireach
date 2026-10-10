//go:build windows

package main

// A small painted UI toolkit on raw Win32 (the native-window technique of the
// agent's chat, without common controls): each window ("surface") rebuilds a
// flat list of widgets from the current state with a flow layout measured in
// physical pixels (per-monitor DPI aware), paints them double-buffered with
// GDI, and handles hover, click, Tab / Enter / Space / Escape itself.
// Look: dark surfaces, Segoe UI (or the system UI font of the language for
// Japanese, Korean and Chinese), the Oblireach red as accent, no borders
// (states are color variants). Right-to-left languages get a mirrored
// layout: widgets are laid out left to right, then flipped.

import (
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Palette (0xRRGGBB).
const (
	colBg         = 0x16181D
	colSurface    = 0x20232A
	colSurfaceHi  = 0x2A2E37
	colSurfaceAct = 0x343945
	colText       = 0xECEEF1
	colMuted      = 0x9AA1AC
	colFaint      = 0x6B7280
	colAccent     = 0xE03A3A // brand red
	colButton     = 0xC83232 // filled buttons
	colButtonHi   = 0xD63B3B
	colButtonAct  = 0xA92828
	colDisabled   = 0x3A3F4A
	colOK         = 0x3BB273
	colWarn       = 0xE0A030
	colTrackOff   = 0x3A3F4A
	colKnob       = 0xFFFFFF
)

type fontKind int

const (
	fontSmall fontKind = iota
	fontBody
	fontBold
	fontTitle
	fontBig
	fontCount
)

// Logical sizes (px at 96 DPI) and weights of the fonts.
var fontSpecs = [fontCount]struct {
	px     int32
	weight int32
}{
	fontSmall: {12, fwNormal},
	fontBody:  {14, fwNormal},
	fontBold:  {14, fwSemiBold},
	fontTitle: {20, fwSemiBold},
	fontBig:   {36, fwSemiBold},
}

type widgetKind int

const (
	wText   widgetKind = iota // text (wrapped or single line)
	wFill                     // rounded rectangle (cards, dots)
	wButton                   // filled button
	wToggle                   // switch + label
)

type btnStyle int

const (
	btnPrimary btnStyle = iota
	btnSecondary
	btnSmall
)

type widget struct {
	kind     widgetKind
	id       int // action id, 0 = not interactive
	r        rect
	text     string
	font     fontKind
	fg, bg   uint32
	flags    uintptr // DrawText flags (wText)
	radius   int32
	style    btnStyle
	on       bool // wToggle
	disabled bool
	face     string // wText: font face override ("" = the surface's)
	clip     rect   // painting is clipped to it when not empty
	badge    int    // wButton: count drawn on its corner (0 = none)
	ltr      bool   // wText: always read left to right (digit groups)
}

// surface is one window of the app.
type surface struct {
	hwnd    uintptr
	style   uintptr
	exStyle uintptr
	dpi     int32
	fonts   [fontCount]uintptr
	// face is the font family of the text ("" = Segoe UI); faceFonts holds
	// fonts of other families (widget.face), by family and kind.
	face      string
	faceFonts map[string]uintptr
	// rtl mirrors the layout and reads text right to left.
	rtl bool

	// logical client width; the height follows the content
	width int32
	// build lays out the current state.
	build func(b *builder)
	// action runs when an interactive widget is activated.
	action func(id int)
	// key handles a key no widget took (Escape...); true = handled.
	key func(vk uintptr) bool
	// closed runs on WM_CLOSE (the window is not destroyed by the toolkit).
	closed func()
	// hotkey runs on WM_HOTKEY.
	hotkey func(id uintptr)
	// destroyed runs on WM_DESTROY.
	destroyed func()
	// timer runs every second while the window exists.
	timer func()
	// dragAnywhere: a click on empty space moves the window (banner).
	dragAnywhere bool
	// noActivate: never take the focus on click (banner).
	noActivate bool
	// autoHeight resizes the window to fit the content; otherwise the
	// client area is width x height (logical) when height is set.
	autoHeight bool
	height     int32
	// wheel handles the mouse wheel (notches, positive = away from the user).
	wheel func(notches int32)
	// activated runs when the window becomes (true) or stops being the
	// active window.
	activated func(active bool)
	// ctlColor colours child edit controls: text, background.
	ctlColor  func() (fg, bg uint32)
	ctlBrush  uintptr
	ctlBrushC uint32
	// laidOut runs after each layout (child controls follow it).
	laidOut func()

	widgets  []widget
	hover    int
	pressed  int
	focus    int
	tracked  bool
	contentH int32
}

var (
	surfacesMu sync.Mutex
	surfaces   = map[uintptr]*surface{}
	creating   *surface
	classOnce  sync.Once
	className  = utf16("OblireachSupportWindow")
	wndProcCB  uintptr
)

func registerClass() {
	classOnce.Do(func() {
		wndProcCB = syscall.NewCallback(wndProc)
		cur, _, _ := pLoadCursorW.Call(0, idcArrow)
		wc := wndClassEx{
			lpfnWndProc:   wndProcCB,
			hInstance:     moduleHandle(),
			hCursor:       cur,
			lpszClassName: className,
		}
		wc.hIcon = appIcon(false, 96)
		wc.hIconSm = appIcon(true, 96)
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	})
}

// create makes the window (hidden) at x, y in physical pixels; the size
// comes from the first layout.
func (s *surface) create(title string, x, y int32) bool {
	registerClass()
	surfacesMu.Lock()
	creating = s
	surfacesMu.Unlock()
	h, _, _ := pCreateWindowExW.Call(s.exStyle, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16(title))), s.style,
		uintptr(x), uintptr(y), 100, 100, 0, 0, moduleHandle(), 0)
	surfacesMu.Lock()
	creating = nil
	if h != 0 {
		surfaces[h] = s
	}
	surfacesMu.Unlock()
	if h == 0 {
		return false
	}
	s.hwnd = h
	s.dpi = windowDPI(h)
	s.makeFonts()
	if s.style&wsCaption != 0 {
		darkFrame(h)
	} else {
		corner := int32(dwmwcpRound)
		if pDwmSetWindowAttribute.Find() == nil {
			pDwmSetWindowAttribute.Call(h, dwmwaCornerPref, uintptr(unsafe.Pointer(&corner)), 4)
		}
	}
	if s.timer != nil {
		pSetTimer.Call(h, 1, 1000, 0)
	}
	s.relayout()
	return true
}

func (s *surface) destroy() {
	if s.hwnd != 0 {
		pDestroyWindow.Call(s.hwnd)
	}
}

// px converts logical pixels to physical ones at the window's DPI.
func (s *surface) px(v int32) int32 { return (v*s.dpi + 48) / 96 }

func (s *surface) makeFonts() {
	s.freeFonts()
	for i := range s.fonts {
		s.fonts[i] = s.newFont(s.face, fontKind(i))
	}
}

func (s *surface) newFont(face string, k fontKind) uintptr {
	if face == "" {
		face = "Segoe UI"
	}
	spec := fontSpecs[k]
	h := -s.px(spec.px)
	f, _, _ := pCreateFontW.Call(uintptr(h), 0, 0, 0, uintptr(spec.weight), 0, 0, 0,
		1 /*DEFAULT_CHARSET*/, 0, 0, 5 /*CLEARTYPE_QUALITY*/, 0, uintptr(unsafe.Pointer(utf16(face))))
	return f
}

// font returns the font of kind k in family face ("" = the surface's).
func (s *surface) font(face string, k fontKind) uintptr {
	if face == "" || strings.EqualFold(face, s.face) || (s.face == "" && face == "Segoe UI") {
		return s.fonts[k]
	}
	key := face + "/" + strconv.Itoa(int(k))
	if f, ok := s.faceFonts[key]; ok {
		return f
	}
	if s.faceFonts == nil {
		s.faceFonts = map[string]uintptr{}
	}
	f := s.newFont(face, k)
	s.faceFonts[key] = f
	return f
}

// setLook changes the font family and the direction, and lays out again.
func (s *surface) setLook(face string, rtl bool) {
	s.face, s.rtl = face, rtl
	if s.hwnd != 0 {
		s.makeFonts()
		s.relayout()
	}
}

func (s *surface) freeFonts() {
	for i, f := range s.fonts {
		if f != 0 {
			pDeleteObject.Call(f)
			s.fonts[i] = 0
		}
	}
	for k, f := range s.faceFonts {
		pDeleteObject.Call(f)
		delete(s.faceFonts, k)
	}
}

// relayout rebuilds the widgets, resizes the window to the content when
// asked, and repaints.
func (s *surface) relayout() {
	if s.hwnd == 0 {
		return
	}
	hdc, _, _ := pGetDC.Call(s.hwnd)
	b := &builder{s: s, hdc: hdc, x: s.px(24), y: s.px(24), w: s.px(s.width) - 2*s.px(24)}
	if s.build != nil {
		s.build(b)
	}
	pReleaseDC.Call(s.hwnd, hdc)
	s.widgets = b.out
	s.contentH = b.y + b.bottomPad()
	if s.rtl {
		s.mirror(s.px(s.width))
	}
	s.fixFocus()
	if s.autoHeight {
		s.fitClient(s.px(s.width), s.contentH)
	} else if s.height > 0 {
		s.fitClient(s.px(s.width), s.px(s.height))
	}
	if s.laidOut != nil {
		s.laidOut()
	}
	pInvalidateRect.Call(s.hwnd, 0, 0)
}

// mirror flips the layout for a right-to-left language: rectangles are
// reflected in the client width, left-aligned text becomes right-aligned
// (and is read right to left, see drawTextFace).
func (s *surface) mirror(cw int32) {
	flip := func(r rect) rect {
		if r == (rect{}) {
			return r
		}
		return rect{cw - r.R, r.T, cw - r.L, r.B}
	}
	for i := range s.widgets {
		w := &s.widgets[i]
		w.r, w.clip = flip(w.r), flip(w.clip)
		if w.kind == wText {
			switch {
			case w.flags&dtCenter != 0:
			case w.flags&dtRight != 0:
				w.flags &^= dtRight
			default:
				w.flags |= dtRight
			}
		}
	}
}

// fitClient resizes the window so its client area is cw x ch (physical).
func (s *surface) fitClient(cw, ch int32) {
	var cr rect
	pGetClientRect.Call(s.hwnd, uintptr(unsafe.Pointer(&cr)))
	if cr.w() == cw && cr.h() == ch {
		return
	}
	ow, oh := outerSize(s.style, s.exStyle, s.dpi, cw, ch)
	pSetWindowPos.Call(s.hwnd, 0, 0, 0, uintptr(ow), uintptr(oh), swpNoMove|swpNoZOrder|swpNoActivate)
}

// placeCenter centres the window on its monitor's work area.
func (s *surface) placeCenter() {
	var wr rect
	pGetWindowRect.Call(s.hwnd, uintptr(unsafe.Pointer(&wr)))
	wa := workArea(s.hwnd)
	x := wa.L + (wa.w()-wr.w())/2
	y := wa.T + (wa.h()-wr.h())/2
	pSetWindowPos.Call(s.hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

func (s *surface) interactive() []int {
	var ids []int
	for _, w := range s.widgets {
		if w.id != 0 && !w.disabled {
			ids = append(ids, w.id)
		}
	}
	return ids
}

func (s *surface) fixFocus() {
	if s.focus == 0 {
		return
	}
	for _, id := range s.interactive() {
		if id == s.focus {
			return
		}
	}
	s.focus = 0
}

func (s *surface) hit(x, y int32) *widget {
	for i := len(s.widgets) - 1; i >= 0; i-- {
		w := &s.widgets[i]
		if w.id != 0 && w.r.has(x, y) {
			return w
		}
	}
	return nil
}

func (s *surface) activate(id int) {
	for _, w := range s.widgets {
		if w.id == id && !w.disabled && s.action != nil {
			s.action(id)
			return
		}
	}
}

// ── painting ────────────────────────────────────────────────────────────────

func (s *surface) paint() {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(s.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(s.hwnd, uintptr(unsafe.Pointer(&ps)))
	s.render(hdc)
}

// render paints the client area into hdc (double-buffered).
func (s *surface) render(hdc uintptr) {
	var cr rect
	pGetClientRect.Call(s.hwnd, uintptr(unsafe.Pointer(&cr)))
	if cr.w() <= 0 || cr.h() <= 0 {
		return
	}
	mem, _, _ := pCreateCompatibleDC.Call(hdc)
	bmp, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(cr.w()), uintptr(cr.h()))
	oldBmp, _, _ := pSelectObject.Call(mem, bmp)
	fillRect(mem, cr, colBg)
	pSetBkMode.Call(mem, transparent)
	for i := range s.widgets {
		s.drawWidget(mem, &s.widgets[i])
	}
	pBitBlt.Call(hdc, 0, 0, uintptr(cr.w()), uintptr(cr.h()), mem, 0, 0, srcCopy)
	pSelectObject.Call(mem, oldBmp)
	pDeleteObject.Call(bmp)
	pDeleteDC.Call(mem)
}

func fillRect(hdc uintptr, r rect, color uint32) {
	br, _, _ := pCreateSolidBrush.Call(uintptr(rgb(color)))
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), br)
	pDeleteObject.Call(br)
}

func fillRound(hdc uintptr, r rect, radius int32, color uint32) {
	if radius <= 0 {
		fillRect(hdc, r, color)
		return
	}
	br, _, _ := pCreateSolidBrush.Call(uintptr(rgb(color)))
	pen, _, _ := pGetStockObject.Call(nullPen)
	ob, _, _ := pSelectObject.Call(hdc, br)
	op, _, _ := pSelectObject.Call(hdc, pen)
	// NULL_PEN: the shape is one pixel smaller, hence +1.
	pRoundRect.Call(hdc, uintptr(r.L), uintptr(r.T), uintptr(r.R+1), uintptr(r.B+1), uintptr(2*radius), uintptr(2*radius))
	pSelectObject.Call(hdc, ob)
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(br)
}

func fillEllipse(hdc uintptr, r rect, color uint32) {
	br, _, _ := pCreateSolidBrush.Call(uintptr(rgb(color)))
	pen, _, _ := pGetStockObject.Call(nullPen)
	ob, _, _ := pSelectObject.Call(hdc, br)
	op, _, _ := pSelectObject.Call(hdc, pen)
	pEllipse.Call(hdc, uintptr(r.L), uintptr(r.T), uintptr(r.R+1), uintptr(r.B+1))
	pSelectObject.Call(hdc, ob)
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(br)
}

func (s *surface) drawText(hdc uintptr, text string, r rect, f fontKind, color uint32, flags uintptr) {
	s.drawTextFace(hdc, text, r, "", f, color, flags)
}

func (s *surface) drawTextFace(hdc uintptr, text string, r rect, face string, f fontKind, color uint32, flags uintptr) {
	if s.rtl {
		flags |= dtRTLReading
	}
	of, _, _ := pSelectObject.Call(hdc, s.font(face, f))
	pSetTextColor.Call(hdc, uintptr(rgb(color)))
	u, _ := syscall.UTF16FromString(text)
	if len(u) > 1 {
		pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), flags|dtNoPrefix)
	}
	pSelectObject.Call(hdc, of)
}

func (s *surface) drawWidget(hdc uintptr, w *widget) {
	if w.clip != (rect{}) {
		if w.clip.B <= w.r.T || w.clip.T >= w.r.B {
			return
		}
		saved, _, _ := pSaveDC.Call(hdc)
		defer pRestoreDC.Call(hdc, saved)
		pIntersectClipRect.Call(hdc, uintptr(w.clip.L), uintptr(w.clip.T), uintptr(w.clip.R), uintptr(w.clip.B))
	}
	switch w.kind {
	case wText:
		if w.ltr && s.rtl {
			// Digit groups ("123 456 789") would be reordered by the
			// bidirectional algorithm in a right-to-left paragraph.
			s.rtl = false
			s.drawTextFace(hdc, w.text, w.r, w.face, w.font, w.fg, w.flags)
			s.rtl = true
			break
		}
		s.drawTextFace(hdc, w.text, w.r, w.face, w.font, w.fg, w.flags)
	case wFill:
		if w.radius >= w.r.h()/2 && w.r.w() == w.r.h() {
			fillEllipse(hdc, w.r, w.bg)
		} else {
			fillRound(hdc, w.r, w.radius, w.bg)
		}
	case wButton:
		bg, fg := s.buttonColors(w)
		fillRound(hdc, w.r, w.radius, bg)
		f := fontBold
		if w.style == btnSmall {
			f = fontSmall
		}
		s.drawTextFace(hdc, w.text, w.r, w.face, f, fg, dtCenter|dtVCenter|dtSingleLine|dtEndEllip)
		if w.badge > 0 {
			d := s.px(18)
			o := s.px(6)
			br := rect{w.r.R - d + o, w.r.T - o, w.r.R + o, w.r.T - o + d}
			if s.rtl {
				br.L, br.R = w.r.L-o, w.r.L-o+d
			}
			fillEllipse(hdc, br, colAccent)
			s.drawText(hdc, badgeText(w.badge), br, fontSmall, colKnob, dtCenter|dtVCenter|dtSingleLine)
		}
	case wToggle:
		s.drawToggle(hdc, w)
	}
}

func (s *surface) buttonColors(w *widget) (bg, fg uint32) {
	hot := w.id == s.hover || w.id == s.focus
	down := w.id == s.pressed && w.id == s.hover
	switch {
	case w.disabled:
		return colDisabled, colFaint
	case w.style == btnPrimary:
		switch {
		case down:
			return colButtonAct, colText
		case hot:
			return colButtonHi, colText
		}
		return colButton, colText
	default:
		switch {
		case down:
			return colSurface, colText
		case hot:
			return colSurfaceAct, colText
		}
		return colSurfaceHi, colText
	}
}

// drawToggle: the label on the left, the switch on the right.
func (s *surface) drawToggle(hdc uintptr, w *widget) {
	hot := w.id == s.hover || w.id == s.focus
	if hot {
		fillRound(hdc, w.r, s.px(8), colSurfaceHi)
	}
	tw, th := s.px(40), s.px(22)
	pad := s.px(10)
	track := rect{w.r.R - pad - tw, w.r.T + (w.r.h()-th)/2, 0, 0}
	if s.rtl {
		track.L = w.r.L + pad
	}
	track.R, track.B = track.L+tw, track.T+th
	col := uint32(colTrackOff)
	if w.on {
		col = colButton
		if hot {
			col = colButtonHi
		}
	}
	fillRound(hdc, track, th/2, col)
	k := th - s.px(6)
	kx := track.L + s.px(3)
	if w.on != s.rtl { // the knob moves towards the reading end when on
		kx = track.R - s.px(3) - k
	}
	fillEllipse(hdc, rect{kx, track.T + s.px(3), kx + k, track.T + s.px(3) + k}, colKnob)
	lr := rect{w.r.L + pad, w.r.T, track.L - pad, w.r.B}
	align := uintptr(dtLeft)
	if s.rtl {
		lr = rect{track.R + pad, w.r.T, w.r.R - pad, w.r.B}
		align = dtRight
	}
	s.drawText(hdc, w.text, lr, fontBody, colText, align|dtVCenter|dtSingleLine|dtEndEllip)
}

// badgeText: a count, capped for its small circle.
func badgeText(n int) string {
	if n > 9 {
		return "9+"
	}
	return strconv.Itoa(n)
}

// ── input ───────────────────────────────────────────────────────────────────

func lparamXY(lp uintptr) (int32, int32) {
	return int32(int16(lp & 0xFFFF)), int32(int16((lp >> 16) & 0xFFFF))
}

func (s *surface) setHover(id int) {
	if id != s.hover {
		s.hover = id
		pInvalidateRect.Call(s.hwnd, 0, 0)
	}
}

func (s *surface) handle(hwnd uintptr, m uint32, wp, lp uintptr) (uintptr, bool) {
	switch m {
	case wmEraseBkgnd:
		return 1, true
	case wmPaint:
		s.paint()
		return 0, true
	case wmMouseActive:
		if s.noActivate {
			return maNoActivate, true
		}
	case wmNCHitTest:
		if s.dragAnywhere {
			r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(m), wp, lp)
			if r == htClient {
				var wr rect
				pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
				x, y := lparamXY(lp)
				if s.hit(x-wr.L, y-wr.T) == nil {
					return htCaption, true
				}
			}
			return r, true
		}
	case wmSetCursor:
		if uint32(lp&0xFFFF) == htClient && s.hover != 0 {
			c, _, _ := pLoadCursorW.Call(0, idcHand)
			pSetCursor.Call(c)
			return 1, true
		}
	case wmMouseMove:
		if !s.tracked {
			tme := trackMouseEvent{DwFlags: tmeLeave, HwndTrack: hwnd}
			tme.CbSize = uint32(unsafe.Sizeof(tme))
			pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			s.tracked = true
		}
		x, y := lparamXY(lp)
		id := 0
		if w := s.hit(x, y); w != nil && !w.disabled {
			id = w.id
		}
		s.setHover(id)
		return 0, true
	case wmMouseLeave:
		s.tracked = false
		s.setHover(0)
		return 0, true
	case wmLButtonDown:
		x, y := lparamXY(lp)
		if w := s.hit(x, y); w != nil && !w.disabled {
			s.pressed = w.id
			pSetCapture.Call(hwnd)
			pInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0, true
	case wmLButtonUp:
		x, y := lparamXY(lp)
		p := s.pressed
		s.pressed = 0
		pReleaseCapture.Call()
		pInvalidateRect.Call(hwnd, 0, 0)
		if w := s.hit(x, y); p != 0 && w != nil && w.id == p {
			s.activate(p)
		}
		return 0, true
	case wmKeyDown:
		return 0, s.keyDown(wp)
	case wmPrintClient:
		s.render(wp)
		return 0, true
	case wmMouseWheel:
		if s.wheel != nil {
			s.wheel(int32(int16(wp>>16)) / 120)
			return 0, true
		}
	case wmActivate:
		if s.activated != nil {
			s.activated(wp&0xFFFF != 0)
		}
	case wmCtlColorEdit:
		if s.ctlColor != nil {
			fg, bg := s.ctlColor()
			pSetTextColor.Call(wp, uintptr(rgb(fg)))
			pSetBkColor.Call(wp, uintptr(rgb(bg)))
			if s.ctlBrush == 0 || s.ctlBrushC != bg {
				if s.ctlBrush != 0 {
					pDeleteObject.Call(s.ctlBrush)
				}
				s.ctlBrush, _, _ = pCreateSolidBrush.Call(uintptr(rgb(bg)))
				s.ctlBrushC = bg
			}
			return s.ctlBrush, true
		}
	case wmHotKey:
		if s.hotkey != nil {
			s.hotkey(wp)
		}
		return 0, true
	case wmTimer:
		if s.timer != nil {
			s.timer()
		}
		return 0, true
	case wmDpiChanged:
		s.dpi = int32(wp & 0xFFFF)
		var r rect
		pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&r)), lp, unsafe.Sizeof(r))
		pSetWindowPos.Call(hwnd, 0, uintptr(r.L), uintptr(r.T), uintptr(r.w()), uintptr(r.h()), swpNoZOrder|swpNoActivate)
		s.makeFonts()
		s.relayout()
		return 0, true
	case wmClose:
		if s.closed != nil {
			s.closed()
		}
		return 0, true
	case wmDestroy:
		surfacesMu.Lock()
		delete(surfaces, hwnd)
		surfacesMu.Unlock()
		pKillTimer.Call(hwnd, 1)
		s.freeFonts()
		if s.ctlBrush != 0 {
			pDeleteObject.Call(s.ctlBrush)
			s.ctlBrush = 0
		}
		s.hwnd = 0
		if s.destroyed != nil {
			s.destroyed()
		}
		return 0, true
	}
	return 0, false
}

// keyDown: Tab / Shift+Tab move the focus, Enter / Space activate it,
// anything else goes to the window's key handler.
func (s *surface) keyDown(vk uintptr) bool {
	switch vk {
	case vkTab:
		ids := s.interactive()
		if len(ids) == 0 {
			return true
		}
		back := keyDownNow(vkShift)
		i := -1
		for k, id := range ids {
			if id == s.focus {
				i = k
			}
		}
		switch {
		case i < 0 && back:
			i = len(ids) - 1
		case i < 0:
			i = 0
		case back:
			i = (i - 1 + len(ids)) % len(ids)
		default:
			i = (i + 1) % len(ids)
		}
		s.focus = ids[i]
		pInvalidateRect.Call(s.hwnd, 0, 0)
		return true
	case vkReturn, vkSpace:
		if s.focus != 0 {
			s.activate(s.focus)
			return true
		}
	}
	if s.key != nil {
		return s.key(vk)
	}
	return false
}

var pGetKeyState = user32.NewProc("GetKeyState")

func keyDownNow(vk uintptr) bool {
	r, _, _ := pGetKeyState.Call(vk)
	return r&0x8000 != 0
}

func wndProc(hwnd, m, wp, lp uintptr) uintptr {
	surfacesMu.Lock()
	s := surfaces[hwnd]
	if s == nil && creating != nil {
		s = creating
	}
	surfacesMu.Unlock()
	if s != nil {
		if s.hwnd == 0 {
			s.hwnd = hwnd
		}
		if r, ok := s.handle(hwnd, uint32(m), wp, lp); ok {
			return r
		}
	}
	if uint32(m) == wmAppDispatch {
		runDispatch()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// ── layout ──────────────────────────────────────────────────────────────────

// builder lays widgets out top to bottom in physical pixels.
type builder struct {
	s       *surface
	hdc     uintptr
	x, y, w int32
	out     []widget
	cards   []int // open card indexes
	cardY   []int32
	pad     int32 // bottom padding override (0 = default)
}

func (b *builder) px(v int32) int32 { return b.s.px(v) }

func (b *builder) bottomPad() int32 {
	if b.pad > 0 {
		return b.pad
	}
	return b.px(24)
}

func (b *builder) space(v int32) { b.y += b.px(v) }

// measure returns the height of text wrapped at width w.
func (b *builder) measure(text string, f fontKind, w int32, flags uintptr) int32 {
	return b.measureFace(text, "", f, w, flags)
}

func (b *builder) measureFace(text, face string, f fontKind, w int32, flags uintptr) int32 {
	if b.s.rtl {
		flags |= dtRTLReading
	}
	of, _, _ := pSelectObject.Call(b.hdc, b.s.font(face, f))
	defer pSelectObject.Call(b.hdc, of)
	r := rect{0, 0, w, 0}
	u, _ := syscall.UTF16FromString(text)
	if len(u) <= 1 {
		u, _ = syscall.UTF16FromString(" ")
	}
	pDrawTextW.Call(b.hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), flags|dtCalcRect|dtNoPrefix)
	return r.h()
}

// measureW returns the single-line width of text.
func (b *builder) measureW(text string, f fontKind) int32 {
	return b.measureWFace(text, "", f)
}

func (b *builder) measureWFace(text, face string, f fontKind) int32 {
	of, _, _ := pSelectObject.Call(b.hdc, b.s.font(face, f))
	defer pSelectObject.Call(b.hdc, of)
	r := rect{0, 0, 10000, 0}
	u, _ := syscall.UTF16FromString(text)
	if len(u) <= 1 {
		return 0
	}
	pDrawTextW.Call(b.hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), dtSingleLine|dtCalcRect|dtNoPrefix)
	return r.w()
}

// text adds a wrapped paragraph and moves down.
func (b *builder) text(s string, f fontKind, color uint32) {
	b.textAligned(s, f, color, dtLeft)
}

func (b *builder) textAligned(s string, f fontKind, color uint32, align uintptr) {
	flags := align | dtWordBreak
	h := b.measure(s, f, b.w, flags)
	b.out = append(b.out, widget{kind: wText, r: rect{b.x, b.y, b.x + b.w, b.y + h}, text: s, font: f, fg: color, flags: flags})
	b.y += h
}

// textAt adds a single line in a given rectangle (no flow).
func (b *builder) textAt(s string, r rect, f fontKind, color uint32, flags uintptr) {
	b.out = append(b.out, widget{kind: wText, r: r, text: s, font: f, fg: color, flags: flags | dtSingleLine | dtEndEllip})
}

// ltr marks the last widget as always left to right (digits).
func (b *builder) ltr() { b.out[len(b.out)-1].ltr = true }

// dot adds a small round status dot at x, centred on a line of height h.
func (b *builder) dot(x, y, h int32, color uint32) {
	d := b.px(8)
	t := y + (h-d)/2
	b.out = append(b.out, widget{kind: wFill, r: rect{x, t, x + d, t + d}, bg: color, radius: d})
}

// statusLine: a dot and a wrapped text.
func (b *builder) statusLine(s string, color uint32) {
	lineH := b.measure("Ag", fontBody, b.w, dtSingleLine)
	b.dot(b.x, b.y, lineH, color)
	ind := b.px(16)
	h := b.measure(s, fontBody, b.w-ind, dtWordBreak)
	b.out = append(b.out, widget{kind: wText, r: rect{b.x + ind, b.y, b.x + b.w, b.y + h}, text: s, font: fontBody, fg: colText, flags: dtWordBreak})
	b.y += h
}

// beginCard opens a filled card; the content is indented.
func (b *builder) beginCard() {
	b.cards = append(b.cards, len(b.out))
	b.cardY = append(b.cardY, b.y)
	b.out = append(b.out, widget{}) // placeholder, filled by endCard
	b.x += b.px(16)
	b.w -= 2 * b.px(16)
	b.y += b.px(14)
}

func (b *builder) endCard(color uint32) {
	n := len(b.cards) - 1
	i, y0 := b.cards[n], b.cardY[n]
	b.cards, b.cardY = b.cards[:n], b.cardY[:n]
	b.x -= b.px(16)
	b.w += 2 * b.px(16)
	b.y += b.px(14)
	b.out[i] = widget{kind: wFill, r: rect{b.x, y0, b.x + b.w, b.y}, bg: color, radius: b.px(12)}
}

// button adds a button in r.
func (b *builder) button(id int, label string, r rect, style btnStyle, disabled bool) {
	b.out = append(b.out, widget{kind: wButton, id: id, r: r, text: label, style: style, disabled: disabled, radius: b.px(8)})
}

// buttonRow lays buttons out right-aligned on one row (the first one is the
// leftmost); widths follow their labels.
func (b *builder) buttonRow(btns ...rowButton) {
	h := b.px(40)
	gap := b.px(10)
	widths := make([]int32, len(btns))
	total := int32(0)
	for i, bt := range btns {
		w := b.measureW(bt.label, fontBold) + 2*b.px(22)
		if w < b.px(110) {
			w = b.px(110)
		}
		widths[i] = w
		total += w
	}
	total += gap * int32(len(btns)-1)
	x := b.x + b.w - total
	if x < b.x {
		// Too wide for one row: share the width.
		x = b.x
		each := (b.w - gap*int32(len(btns)-1)) / int32(len(btns))
		for i := range widths {
			widths[i] = each
		}
	}
	for i, bt := range btns {
		b.button(bt.id, bt.label, rect{x, b.y, x + widths[i], b.y + h}, bt.style, bt.disabled)
		b.out[len(b.out)-1].badge = bt.badge
		x += widths[i] + gap
	}
	b.y += h
}

type rowButton struct {
	id       int
	label    string
	style    btnStyle
	disabled bool
	badge    int
}

// toggle adds a full-width switch row.
func (b *builder) toggle(id int, label string, on bool) {
	h := b.px(40)
	b.out = append(b.out, widget{kind: wToggle, id: id, r: rect{b.x - b.px(10), b.y, b.x + b.w + b.px(10), b.y + h}, text: label, on: on})
	b.y += h
}

// ── UI-thread dispatch ──────────────────────────────────────────────────────

var (
	dispatchMu    sync.Mutex
	dispatchQueue []func()
	dispatchHwnd  uintptr // the main window (receives wmAppDispatch)
)

// post runs f on the UI thread. Before the main window exists the work is
// kept and runs once it does.
func post(f func()) {
	dispatchMu.Lock()
	dispatchQueue = append(dispatchQueue, f)
	h := dispatchHwnd
	dispatchMu.Unlock()
	if h != 0 {
		pPostMessageW.Call(h, wmAppDispatch, 0, 0)
	}
}

func setDispatchWindow(h uintptr) {
	dispatchMu.Lock()
	dispatchHwnd = h
	pending := len(dispatchQueue) > 0
	dispatchMu.Unlock()
	if h != 0 && pending {
		pPostMessageW.Call(h, wmAppDispatch, 0, 0)
	}
}

func runDispatch() {
	dispatchMu.Lock()
	q := dispatchQueue
	dispatchQueue = nil
	dispatchMu.Unlock()
	for _, f := range q {
		f()
	}
}

// messageLoop runs until WM_QUIT.
func messageLoop() {
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
