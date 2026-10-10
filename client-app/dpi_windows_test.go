//go:build windows

package main

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// testDpiAware: TestMain made the test process Per-Monitor V2 DPI aware.
var testDpiAware bool

func TestDpiAwareness(t *testing.T) {
	if !testDpiAware || !perMonitorV2() {
		t.Fatal("the process is not Per-Monitor V2 DPI aware")
	}
	// Asked again (the manifest set it first, or the engine asks): no change.
	if !enableDpiAwareness() || !perMonitorV2() {
		t.Fatal("a second call changed the mode")
	}
}

func TestScaleBetween(t *testing.T) {
	for _, c := range [][4]int32{
		{1280, 96, 144, 1920}, {1920, 144, 96, 1280}, {1280, 96, 96, 1280}, {1280, 120, 168, 1792},
		{1001, 96, 144, 1502}, {1000, 144, 120, 833}, {820, 0, 144, 820}, {820, 96, 0, 820}, {0, 96, 192, 0},
	} {
		if got := scaleBetween(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("scaleBetween(%d, %d, %d) = %d, want %d", c[0], c[1], c[2], got, c[3])
		}
	}
	// A window of 1280 x 820 px at 100 % torn onto a 150 % screen: the same
	// size in DIPs; and back.
	if got := tearOffSize(winPoint{1280, 820}, 96, 144); got != (winPoint{1920, 1230}) {
		t.Fatalf("to 150 %%: %+v", got)
	}
	if got := tearOffSize(winPoint{1920, 1230}, 144, 96); got != (winPoint{1280, 820}) {
		t.Fatalf("back to 100 %%: %+v", got)
	}
}

func TestInitialWindowRect(t *testing.T) {
	work := winRect{0, 0, 1920, 1040}
	for _, c := range []struct {
		name string
		cur  winRect
		size winPoint
		work winRect
		want winRect
	}{
		{"fits", winRect{26, 26, 0, 0}, winPoint{1294, 808}, work, winRect{26, 26, 1320, 834}},
		{"too wide and tall: the work area", winRect{26, 26, 0, 0}, winPoint{2894, 1608}, work, winRect{0, 0, 1920, 1040}},
		{"overflows right: moved left", winRect{1000, 26, 0, 0}, winPoint{1294, 808}, work, winRect{626, 26, 1920, 834}},
		{"overflows bottom: moved up", winRect{26, 500, 0, 0}, winPoint{1294, 808}, work, winRect{26, 232, 1320, 1040}},
		{"a second screen on the left", winRect{-2500, 100, 0, 0}, winPoint{1940, 1235}, winRect{-2560, 0, 0, 1400}, winRect{-2500, 100, -560, 1335}},
		{"work area unknown: as asked", winRect{26, 26, 0, 0}, winPoint{2894, 1608}, winRect{}, winRect{26, 26, 2920, 1634}},
	} {
		if got := initialWindowRect(c.cur, c.size, c.work); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A torn-off tab's window (tearOffFrom): its size converted to the DPI of the
// screen it is dropped on, the grab and the caption at that DPI.
func TestTearOffAcrossDpi(t *testing.T) {
	_, host, m, _, _ := testMainBridge(t)
	w1, d1, _ := testSessionWindow(t, m, 0x100, "41")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"41", "42"}})
	m.cursorHook = func() winPoint { return winPoint{3000, 400} }
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return nil, false }
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: 0x900}, mgr: m, deviceID: r.deviceID}, nil
	}
	// The source at 100 % (1280 x 820), the drop on a 150 % screen at x >= 2560.
	m.placeHook = func(w *detachedWindow, pt winPoint) (winPoint, winRect, int32) {
		return tearOffSize(winPoint{1280, 820}, 96, 144), winRect{2560, 0, 2560 + 2880, 1600}, 144
	}
	if r, err := d1.tearOff(d1.token, "42", "x", detachOpts{}); err != nil || r != "opening" {
		t.Fatalf("tear off: %q %v", r, err)
	}
	for _, f := range host.dispatched {
		f()
	}
	if len(created) != 1 || created[0].rect == nil {
		t.Fatalf("created: %+v", created)
	}
	rc := *created[0].rect
	if rc.Right-rc.Left != 1920 || rc.Bottom-rc.Top != 1230 {
		t.Fatalf("size %dx%d, want 1920x1230", rc.Right-rc.Left, rc.Bottom-rc.Top)
	}
	if grabX, grabY := 3000-rc.Left, 400-rc.Top; grabX != scaleDpi(tearGrabX, 144) || grabY != scaleDpi(sessionCaptionH, 144)/2 {
		t.Fatalf("grab %d,%d at 150 %%", grabX, grabY)
	}
	_ = w1
}

// With a real (never shown) window: WM_DPICHANGED moves it to the rectangle
// Windows suggests, except while the app places it; its first size stays in
// its work area; a torn-off tab's placement reads its size and screen.
func TestDpiChangedWindow(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cls, err := registerWindowClass("OblireachDpiTest", testFrameProc)
	if err != nil {
		t.Skipf("no window class: %v", err)
	}
	h := &webHost{}
	h.frame = frameState{minW: 480, minH: 320, captionH: 34}
	testFrameHost = h
	defer func() { testFrameHost = nil }()
	title, _ := syscall.UTF16PtrFromString("dpi test")
	hinst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, 200, 150, 900, 600, 0, 0, hinst, 0)
	if hwnd == 0 {
		t.Skipf("no window: %v", e)
	}
	defer procDestroyWindow.Call(hwnd)
	h.hwnd = hwnd
	frameReady(hwnd)
	rect := func() (r winRect) {
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		return
	}
	dpiMsg := func(r winRect) {
		procSendMessageW.Call(hwnd, wmDpiChanged, 144|144<<16, uintptr(unsafe.Pointer(&r)))
	}

	want := winRect{300, 200, 300 + 1350, 200 + 900}
	dpiMsg(want)
	if got := rect(); got != want {
		t.Fatalf("after WM_DPICHANGED: %+v, want the suggested %+v", got, want)
	}
	h.place(func() { dpiMsg(winRect{0, 0, 600, 400}) })
	if got := rect(); got != want || h.frame.placing {
		t.Fatalf("placing: %+v (placing %v), want it kept at %+v", got, h.frame.placing, want)
	}
	dpiMsg(winRect{}) // (an empty rectangle: ignored)
	if got := rect(); got != want {
		t.Fatalf("empty suggestion: %+v", got)
	}

	// The first size: as asked when it fits, never beyond the work area.
	h.setInitialSize(1000, 700)
	if got := rect(); got.Right-got.Left != 1000 || got.Bottom-got.Top != 700 {
		t.Fatalf("initial size: %+v", got)
	}
	work := workAreaOf(rect())
	if work.Right <= work.Left {
		t.Skip("no work area here")
	}
	h.setInitialSize(100000, 100000)
	if got := rect(); got.Left < work.Left || got.Top < work.Top || got.Right > work.Right || got.Bottom > work.Bottom {
		t.Fatalf("initial size %+v outside the work area %+v", got, work)
	}

	// placement (a torn-off tab of this window, dropped on its own screen):
	// its size, its screen's work area and DPI.
	h.place(func() { procSetWindowPos.Call(hwnd, 0, 100, 100, 1100, 750, swpNoZOrder|swpNoActivate) })
	m := &sessionWindows{}
	size, wa, dpi := m.placement(&detachedWindow{webHost: webHost{hwnd: hwnd}}, winPoint{150, 150})
	if size != (winPoint{1100, 750}) || wa != work || dpi != dpiOf(hwnd) {
		t.Fatalf("placement: %+v %+v %d (work %+v, dpi %d)", size, wa, dpi, work, dpiOf(hwnd))
	}
	if d := monitorDpi(0); d != 96 {
		t.Fatalf("monitorDpi(0) = %d", d)
	}
}
