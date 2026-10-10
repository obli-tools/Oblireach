//go:build windows

package main

import (
	"encoding/json"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The client area keeps the standard left / right / bottom frame and loses
// the title bar; maximized, the top overhang goes too, and an auto-hide
// taskbar keeps its 2 px.
func TestFrameClientRect(t *testing.T) {
	proposed := winRect{100, 50, 1400, 900}
	standard := winRect{108, 81, 1392, 892} // DefWindowProc: 8 px frame, 31 px caption
	none := [4]bool{}
	if got := frameClientRect(proposed, standard, false, 8, none); got != (winRect{108, 50, 1392, 892}) {
		t.Fatalf("normal: %+v", got)
	}
	// An auto-hide taskbar only matters maximized.
	if got := frameClientRect(proposed, standard, false, 8, [4]bool{true, true, true, true}); got != (winRect{108, 50, 1392, 892}) {
		t.Fatalf("normal + autohide: %+v", got)
	}
	// Maximized on a 1920x1040 work area: the window is (-8,-8)-(1928,1048).
	proposed = winRect{-8, -8, 1928, 1048}
	standard = winRect{0, 23, 1920, 1040}
	if got := frameClientRect(proposed, standard, true, 8, none); got != (winRect{0, 0, 1920, 1040}) {
		t.Fatalf("maximized: %+v (want the work area)", got)
	}
	for edge, want := range map[int]winRect{
		abeLeft:   {2, 0, 1920, 1040},
		abeTop:    {0, 2, 1920, 1040},
		abeRight:  {0, 0, 1918, 1040},
		abeBottom: {0, 0, 1920, 1038},
	} {
		var hide [4]bool
		hide[edge] = true
		if got := frameClientRect(proposed, standard, true, 8, hide); got != want {
			t.Fatalf("maximized, auto-hide taskbar on edge %d: %+v, want %+v", edge, got, want)
		}
	}
	// Degenerate (minimized-like) rectangles never come out inverted.
	if got := frameClientRect(winRect{0, 0, 0, 0}, winRect{8, 31, 0, 0}, true, 8, none); got.Bottom < got.Top || got.Right < got.Left {
		t.Fatalf("inverted: %+v", got)
	}
}

func TestFrameHit(t *testing.T) {
	const w, band, corner, strip, btn = 1000, 8, 16, 44, 46
	cases := []struct {
		x, y      int32
		maximized bool
		strip     int32
		want      uintptr
	}{
		{500, 0, false, 0, htTop},
		{500, 7, false, 0, htTop},
		{5, 3, false, 0, htTopLeft},
		{15, 3, false, 0, htTopLeft},
		{16, 3, false, 0, htTop},
		{983, 3, false, 0, htTop},
		{984, 3, false, 0, htTopRight},
		{999, 0, false, 0, htTopRight},
		{500, 8, false, 0, htClient}, // below the band: the page (no strip)
		{500, 3, true, 0, htClient},  // maximized: no top border
		{500, 3, true, strip, htCaption},
		{500, 20, false, strip, htCaption}, // native strip: the title
		{0, 43, false, strip, htCaption},
		{500, 44, false, strip, htClient},       // below it
		{w - 3*btn, 20, false, strip, htClient}, // minimize button
		{w - 1, 20, false, strip, htClient},     // close button
		{w - 3*btn - 1, 20, false, strip, htCaption},
		{w - 1, 3, false, strip, htTopRight}, // the band wins over the buttons
		{500, -1, false, strip, htClient},
	}
	for _, c := range cases {
		if got := frameHit(c.x, c.y, w, c.maximized, band, corner, c.strip, btn); got != c.want {
			t.Errorf("frameHit(%d,%d max=%v strip=%d) = %d, want %d", c.x, c.y, c.maximized, c.strip, got, c.want)
		}
	}
}

func TestStripButtons(t *testing.T) {
	const w, strip, btn = 900, 34, 46
	for b := stripMin; b <= stripClose; b++ {
		r := stripButtonRect(b, w, strip, btn)
		if r.Right-r.Left != btn || r.Top != 0 || r.Bottom != strip {
			t.Fatalf("button %d rect %+v", b, r)
		}
		for _, x := range []int32{r.Left, (r.Left + r.Right) / 2, r.Right - 1} {
			if got := stripButtonAt(x, strip/2, w, strip, btn); got != b {
				t.Fatalf("x=%d: button %d, want %d", x, got, b)
			}
		}
	}
	if stripButtonRect(stripClose, w, strip, btn).Right != w || stripButtonRect(stripMin, w, strip, btn).Left != w-3*btn {
		t.Fatal("buttons are not at the right end, minimize / maximize / close")
	}
	for _, p := range [][2]int32{{w - 3*btn - 1, 10}, {w, 10}, {w - 1, strip}, {w - 1, -1}} {
		if got := stripButtonAt(p[0], p[1], w, strip, btn); got != stripNone {
			t.Fatalf("%v: button %d, want none", p, got)
		}
	}
	if stripButtonAt(w-1, 10, w, 0, btn) != stripNone {
		t.Fatal("no strip, no button")
	}
}

func TestScaleDpi(t *testing.T) {
	for _, c := range [][3]int32{{44, 96, 44}, {44, 120, 55}, {44, 144, 66}, {46, 144, 69}, {8, 192, 16}, {5, 0, 5}, {34, 168, 60}} {
		if got := scaleDpi(c[0], c[1]); got != c[2] {
			t.Fatalf("scaleDpi(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

func TestDragHitAndSysCommand(t *testing.T) {
	for edge, want := range map[string]uintptr{"": htCaption, "top": htTop, "topleft": htTopLeft, "topright": htTopRight} {
		if got, ok := dragHitFor(edge); !ok || got != want {
			t.Fatalf("dragHitFor(%q) = %d %v", edge, got, ok)
		}
	}
	for _, edge := range []string{"left", "bottom", "move ", "TOP", "caption", "2"} {
		if _, ok := dragHitFor(edge); ok {
			t.Fatalf("dragHitFor(%q) accepted", edge)
		}
	}
	for _, c := range []struct {
		action string
		max    bool
		want   uintptr
	}{{"minimize", false, scMinimize}, {"minimize", true, scMinimize}, {"maximize", false, scMaximize}, {"maximize", true, scRestore}, {"close", false, scClose}, {"close", true, scClose}} {
		if got, ok := sysCommandFor(c.action, c.max); !ok || got != c.want {
			t.Fatalf("sysCommandFor(%q, %v) = %#x %v", c.action, c.max, got, ok)
		}
	}
	for _, a := range []string{"", "restore", "quit", "Close", "move"} {
		if _, ok := sysCommandFor(a, false); ok {
			t.Fatalf("sysCommandFor(%q) accepted", a)
		}
	}
	for _, p := range []winPoint{{0, 0}, {1919, 1079}, {-1920, 5}, {-5, -1200}, {32767, -32768}} {
		if got := lParamPoint(pointsLParam(p)); got != p {
			t.Fatalf("point %v -> lParam -> %v", p, got)
		}
	}
}

func TestIsLocalUIURL(t *testing.T) {
	const o = "http://127.0.0.1:4321"
	for raw, want := range map[string]bool{
		"http://127.0.0.1:4321/":                    true,
		"http://127.0.0.1:4321":                     true,
		"http://127.0.0.1:4321/?detached=12&vo=1":   true,
		"http://127.0.0.1:4321/#x":                  true,
		"http://127.0.0.1:4321/sso/callback?code=x": false, // the proxy's error pages
		"http://127.0.0.1:4321/local/enter?k=abc":   false,
		"http://127.0.0.1:4322/":                    false,
		"https://127.0.0.1:4321/":                   false,
		"https://obligate.example/authorize?x=1":    false,
		"http://user@127.0.0.1:4321/":               false,
		"about:blank":                               false,
		"":                                          false,
		"%zz":                                       false,
	} {
		if got := isLocalUIURL(raw, o); got != want {
			t.Errorf("isLocalUIURL(%q) = %v, want %v", raw, got, want)
		}
	}
	if isLocalUIURL("http://127.0.0.1:4321/", "") {
		t.Fatal("no origin: nothing is local")
	}
}

// fakeFrame records what the title bar bindings do to their window.
type fakeFrame struct {
	drags, cmds []uintptr
	st          windowStateInfo
}

func (f *fakeFrame) startDrag(hit uintptr)  { f.drags = append(f.drags, hit) }
func (f *fakeFrame) sysCommand(sc uintptr)  { f.cmds = append(f.cmds, sc) }
func (f *fakeFrame) state() windowStateInfo { return f.st }

func TestWindowBindings(t *testing.T) {
	host := &fakeHost{}
	b, _ := newNativeBridge(host, "http://127.0.0.1:4321")
	tok := b.token

	// No window behind (never the case in the app): nothing happens.
	if ok, err := b.windowDrag(tok, ""); ok || err != nil {
		t.Fatalf("drag without window: %v %v", ok, err)
	}
	if ok, err := b.windowControl(tok, "close"); ok || err != nil {
		t.Fatalf("control without window: %v %v", ok, err)
	}
	if s, err := b.windowState(tok); err != nil || !s.Active || s.Maximized {
		t.Fatalf("state without window: %+v %v", s, err)
	}

	f := &fakeFrame{st: windowStateInfo{Active: true}}
	b.win = f
	// Every call needs the token.
	if _, err := b.windowDrag("nope", ""); err != errNotAllowed {
		t.Fatal("drag without token")
	}
	if _, err := b.windowControl("", "close"); err != errNotAllowed {
		t.Fatal("control without token")
	}
	if _, err := b.windowState(tok[:31] + "x"); err != errNotAllowed {
		t.Fatal("state with a wrong token")
	}
	if len(f.drags)+len(f.cmds) != 0 {
		t.Fatal("a refused call reached the window")
	}

	// Move, resize from the top band; bad edges refused.
	for _, e := range []string{"", "top", "topleft", "topright"} {
		if ok, err := b.windowDrag(tok, e); !ok || err != nil {
			t.Fatalf("drag %q: %v %v", e, ok, err)
		}
	}
	if len(f.drags) != 4 || f.drags[0] != htCaption || f.drags[1] != htTop || f.drags[2] != htTopLeft || f.drags[3] != htTopRight {
		t.Fatalf("drags: %v", f.drags)
	}
	if _, err := b.windowDrag(tok, "bottom"); err == nil || len(f.drags) != 4 {
		t.Fatal("unknown edge accepted")
	}
	// Maximized: moving is allowed (Windows restores the window under the
	// mouse), resizing is not.
	f.st.Maximized = true
	if ok, _ := b.windowDrag(tok, "top"); ok || len(f.drags) != 4 {
		t.Fatal("a maximized window was resized from its top band")
	}
	if ok, _ := b.windowDrag(tok, ""); !ok || f.drags[4] != htCaption {
		t.Fatal("a maximized window cannot be dragged")
	}

	// Buttons: maximize toggles with the state.
	b.windowControl(tok, "maximize") // maximized: restore
	f.st.Maximized = false
	b.windowControl(tok, "maximize")
	b.windowControl(tok, "minimize")
	b.windowControl(tok, "close")
	if len(f.cmds) != 4 || f.cmds[0] != scRestore || f.cmds[1] != scMaximize || f.cmds[2] != scMinimize || f.cmds[3] != scClose {
		t.Fatalf("commands: %#v", f.cmds)
	}
	if _, err := b.windowControl(tok, "quit"); err == nil || len(f.cmds) != 4 {
		t.Fatal("unknown action accepted")
	}
	f.st = windowStateInfo{Maximized: true, Active: false}
	if s, _ := b.windowState(tok); s != f.st {
		t.Fatalf("state: %+v", s)
	}

	// State pushes reach the local UI only.
	host.evals = nil
	b.pushWindowState(windowStateInfo{Maximized: true, Active: true})
	if len(host.evals) != 1 || !strings.Contains(host.evals[0], `"__reachOnWindowState"`) ||
		!strings.Contains(host.evals[0], `{"maximized":true,"active":true}`) ||
		!strings.Contains(host.evals[0], `location.origin !== "http://127.0.0.1:4321"`) {
		t.Fatalf("push: %v", host.evals)
	}
	b.shutdown()
	b.pushWindowState(windowStateInfo{})
	if len(host.evals) != 1 {
		t.Fatal("a closed window's page was told")
	}
}

// The page is told once per change of state, not at every WM_SIZE.
func TestPushStateOnChange(t *testing.T) {
	var got []windowStateInfo
	h := &webHost{} // hwnd 0: never maximized
	h.frame.onState = func(s windowStateInfo) { got = append(got, s) }
	h.pushState()
	h.pushState()
	h.frame.active = true
	h.pushState()
	h.pushState()
	h.frame.active = false
	h.pushState()
	if len(got) != 3 || got[0].Active || !got[1].Active || got[2].Active {
		t.Fatalf("pushes: %+v", got)
	}
	h.finished = true
	h.frame.active = true
	h.pushState()
	if len(got) != 3 {
		t.Fatal("a closed window's page was told")
	}
	if b, _ := json.Marshal(windowStateInfo{Maximized: true}); string(b) != `{"maximized":true,"active":false}` {
		t.Fatalf("json: %s", b)
	}
}

// ── A real (never shown) window with the frame's procedure ────────────────

var testFrameHost *webHost

func testFrameProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	if h := testFrameHost; h != nil && h.hwnd == hwnd {
		if r, done := h.frameProc(msg, wParam, lParam); done {
			return r
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

var (
	procGetWindowRect    = modUser32.NewProc("GetWindowRect")
	procClientToScreen   = modUser32.NewProc("ClientToScreen")
	procSendMessageW     = modUser32.NewProc("SendMessageW")
	procGetWindowLongPtr = modUser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr = modUser32.NewProc("SetWindowLongPtrW")
	procIsWindowVisible  = modUser32.NewProc("IsWindowVisible")
)

// With the real DefWindowProc: the client area starts at the top of the
// window and keeps the standard frame elsewhere; hit-testing gives the
// frame's borders, the top band, and the native strip (the WebView2 is not
// there); maximized, the overhang is off the top too. The window is never
// shown.
func TestFramelessWindow(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cls, err := registerWindowClass("OblireachFrameTest", testFrameProc)
	if err != nil {
		t.Skipf("no window class: %v", err)
	}
	h := &webHost{}
	h.frame = frameState{minW: 480, minH: 320, captionH: 34}
	testFrameHost = h
	defer func() { testFrameHost = nil }()
	title, _ := syscall.UTF16PtrFromString("frame test")
	hinst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, 200, 150, 900, 600, 0, 0, hinst, 0)
	if hwnd == 0 {
		t.Skipf("no window: %v", e)
	}
	defer procDestroyWindow.Call(hwnd)
	h.hwnd = hwnd
	frameReady(hwnd)
	if v, _, _ := procIsWindowVisible.Call(hwnd); v != 0 {
		t.Fatal("the window was shown")
	}

	geometry := func() (win, client winRect) {
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&win)))
		var rc winRect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		tl, br := winPoint{0, 0}, winPoint{rc.Right, rc.Bottom}
		procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&tl)))
		procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&br)))
		return win, winRect{tl.X, tl.Y, br.X, br.Y}
	}
	band := resizeBand(hwnd)
	win, cl := geometry()
	if cl.Top != win.Top {
		t.Fatalf("client top %d, window top %d: a title bar is left", cl.Top, win.Top)
	}
	side, bottom := cl.Left-win.Left, win.Bottom-cl.Bottom
	if side <= 0 || side != win.Right-cl.Right || bottom <= 0 {
		t.Fatalf("frame kept: left %d right %d bottom %d (window %+v client %+v)", side, win.Right-cl.Right, bottom, win, cl)
	}
	t.Logf("window %+v client %+v band %d side %d bottom %d", win, cl, band, side, bottom)

	hit := func(x, y int32) uintptr {
		r, _, _ := procSendMessageW.Call(hwnd, wmNCHitTest, 0, pointsLParam(winPoint{x, y}))
		return r
	}
	const htLeft, htRight, htBottom, htBottomRight = 10, 11, 15, 17
	cw := cl.Right - cl.Left
	for _, c := range []struct {
		name string
		x, y int32
		want uintptr
	}{
		{"top band", cl.Left + cw/2, cl.Top + 1, htTop},
		{"top-left corner", cl.Left + 2, cl.Top + 1, htTopLeft},
		{"top-right corner", cl.Right - 2, cl.Top + 1, htTopRight},
		{"native strip (title)", cl.Left + cw/2, cl.Top + band + 5, htCaption},
		{"native strip (close button)", cl.Right - 10, cl.Top + band + 5, htClient},
		{"below the strip", cl.Left + cw/2, cl.Top + 34 + 5, htClient},
		{"left frame", win.Left + side/2, cl.Top + 200, htLeft},
		{"right frame", win.Right - side/2 - 1, cl.Top + 200, htRight},
		{"bottom frame", cl.Left + cw/2, win.Bottom - bottom/2 - 1, htBottom},
		{"bottom-right corner", win.Right - 1, win.Bottom - 1, htBottomRight},
	} {
		if got := hit(c.x, c.y); got != c.want {
			t.Errorf("%s (%d,%d): hit %d, want %d", c.name, c.x, c.y, got, c.want)
		}
	}

	// Smallest size, scaled.
	var mmi minMaxInfo
	procSendMessageW.Call(hwnd, wmGetMinMaxInfo, 0, uintptr(unsafe.Pointer(&mmi)))
	if dpi := dpiOf(hwnd); mmi.PtMinTrackSize != (winPoint{scaleDpi(480, dpi), scaleDpi(320, dpi)}) {
		t.Fatalf("min track size %+v", mmi.PtMinTrackSize)
	}

	// "Maximized" (the style bit IsZoomed reads, so that the window stays
	// hidden): no top band, and the overhang is off the top like the others.
	const gwlStyle = ^uintptr(15) // -16
	const wsMaximize = 0x01000000
	style, _, _ := procGetWindowLongPtr.Call(hwnd, gwlStyle)
	procSetWindowLongPtr.Call(hwnd, gwlStyle, style|wsMaximize)
	if !isZoomed(hwnd) {
		t.Skip("cannot fake a maximized window here")
	}
	frameReady(hwnd)
	win, cl = geometry()
	// (an auto-hide taskbar may keep 2 px of one edge)
	if over, side := cl.Top-win.Top, cl.Left-win.Left; over <= 0 || over-side > autohideTaskbarPx || side-over > autohideTaskbarPx {
		t.Fatalf("maximized: top overhang %d, side %d (window %+v client %+v)", over, side, win, cl)
	}
	if got := hit(cl.Left+cw/2, cl.Top+1); got != htCaption {
		t.Fatalf("maximized, top of the strip: hit %d, want the title (no resize band)", got)
	}
	procSetWindowLongPtr.Call(hwnd, gwlStyle, style)
}
