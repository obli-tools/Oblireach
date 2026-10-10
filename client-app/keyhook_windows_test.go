//go:build windows

package main

import "testing"

type keyEvt struct {
	down bool
	code string
}

// fakeWin is a window of the registry: the capture flag of its page and the
// keys it got.
type fakeWin struct {
	capture bool
	got     []keyEvt
}

func (f *fakeWin) keyCaptureOn() bool             { return f.capture }
func (f *fakeWin) emitKey(down bool, code string) { f.got = append(f.got, keyEvt{down, code}) }

// newTestHook: a hook whose only window is w; ours tells whether it is in
// the foreground.
func newTestHook(ours *bool) (*winKeyHook, *fakeWin) {
	w := &fakeWin{}
	h := &winKeyHook{target: func() keyTarget {
		if !*ours || !w.capture {
			return nil
		}
		return w
	}}
	return h, w
}

func TestWinKeyHookCapture(t *testing.T) {
	ours := true
	h, w := newTestHook(&ours)

	// Capture off: the Windows key goes to the local shell.
	if h.decide(vkLWin, 0, 100, wmKeyDown) || h.decide(vkLWin, 0, 150, 0x0101) {
		t.Fatal("captured while off")
	}
	w.capture = true

	// Other keys are never touched.
	if h.decide(0x52 /* R */, 0, 200, wmKeyDown) {
		t.Fatal("captured a non-Windows key")
	}
	// Injected keys (ours or another tool's) pass.
	if h.decide(vkLWin, llkhfInjected, 250, wmKeyDown) {
		t.Fatal("captured an injected key")
	}

	// Press, auto-repeat, release: one down, one up, all swallowed.
	for i, m := range []uintptr{wmKeyDown, wmKeyDown, wmKeyDown} {
		if !h.decide(vkLWin, 0, uint32(300+500*i), m) { // first repeat after 500 ms, then 500 ms apart
			t.Fatalf("press %d not swallowed", i)
		}
	}
	// The page turns the capture off before the release (blur): the
	// release must still be swallowed (the shell saw no press).
	w.capture = false
	if !h.decide(vkLWin, 0, 1400, 0x0101) {
		t.Fatal("release of a swallowed press reached the shell")
	}
	want := []keyEvt{{true, "MetaLeft"}, {false, "MetaLeft"}}
	if len(w.got) != len(want) || w.got[0] != want[0] || w.got[1] != want[1] {
		t.Fatalf("events = %v, want %v", w.got, want)
	}

	// Another window in the foreground: not captured even when on.
	w.capture = true
	ours = false
	if h.decide(vkRWin, 0, 2000, wmSysKeyDown) || h.decide(vkRWin, 0, 2050, 0x0105) {
		t.Fatal("captured while another window is in the foreground")
	}
	ours = true
	if !h.decide(vkRWin, 0, 2100, wmSysKeyDown) || !h.decide(vkRWin, 0, 2150, 0x0105) {
		t.Fatal("right Windows key not captured")
	}
	if n := len(w.got); n != 4 || w.got[2] != (keyEvt{true, "MetaRight"}) || w.got[3] != (keyEvt{false, "MetaRight"}) {
		t.Fatalf("events = %v", w.got)
	}
}

// A release the hook never saw (let go on the UAC secure desktop, the lock
// screen...) must not make the next press, in any window, disappear.
func TestWinKeyHookLostRelease(t *testing.T) {
	ours := true
	h, w := newTestHook(&ours)
	w.capture = true
	if !h.decide(vkLWin, 0, 1000, wmKeyDown) || !h.decide(vkLWin, 0, 1400, wmKeyDown) {
		t.Fatal("press / auto-repeat not swallowed")
	}
	// Released elsewhere: no key-up. Minutes later, another application is
	// in the foreground and the operator presses Win: it reaches the shell,
	// and the page gets the release it missed.
	ours = false
	if h.decide(vkLWin, 0, 300000, wmKeyDown) {
		t.Fatal("new press swallowed after a lost release")
	}
	if h.decide(vkLWin, 0, 300100, 0x0101) {
		t.Fatal("release of a press the shell saw was swallowed")
	}
	want := []keyEvt{{true, "MetaLeft"}, {false, "MetaLeft"}}
	if len(w.got) != len(want) || w.got[0] != want[0] || w.got[1] != want[1] {
		t.Fatalf("events = %v, want %v", w.got, want)
	}
	// Same, back in the session: a fresh press is captured again.
	ours = true
	if !h.decide(vkLWin, 0, 400000, wmKeyDown) {
		t.Fatal("press not captured")
	}
	if !h.decide(vkLWin, 0, 900000, wmKeyDown) || len(w.got) != 5 || w.got[3] != (keyEvt{false, "MetaLeft"}) || w.got[4] != (keyEvt{true, "MetaLeft"}) {
		t.Fatalf("stale press not closed then reopened: %v", w.got)
	}
	// The tick counter wraps (49.7 days): repeats across the wrap stay repeats.
	h2, w2 := newTestHook(&ours)
	w2.capture = true
	if !h2.decide(vkRWin, 0, 0xFFFFFF00, wmKeyDown) || !h2.decide(vkRWin, 0, 0x100, wmKeyDown) || len(w2.got) != 1 {
		t.Fatalf("auto-repeat across the tick wrap: %v", w2.got)
	}
}

// With detached session windows, the single hook hands the key to the window
// in the foreground (fake HWNDs), and a release to the window of its press.
func TestKeyHookRoutesToForegroundWindow(t *testing.T) {
	fg := uintptr(0)
	reg := newBridgeRegistry(func() uintptr { return fg })
	main := &fakeWin{}
	det := &fakeWin{}
	reg.register(100, main)
	reg.register(200, det)
	h := &winKeyHook{target: reg.keyTarget}

	// Main window in front, its picture focused: main gets Win.
	fg, main.capture = 100, true
	if !h.decide(vkLWin, 0, 10, wmKeyDown) {
		t.Fatal("not captured for the main window")
	}
	// Alt+Tab to the detached window while Win is held: the release still
	// goes to the main window's page (it got the press).
	fg, det.capture = 200, true
	if !h.decide(vkLWin, 0, 20, 0x0101) {
		t.Fatal("release not swallowed")
	}
	if len(main.got) != 2 || len(det.got) != 0 || main.got[1] != (keyEvt{false, "MetaLeft"}) {
		t.Fatalf("main=%v detached=%v", main.got, det.got)
	}
	// Now the detached window gets it; the main window, not in front, never.
	if !h.decide(vkRWin, 0, 30, wmKeyDown) || !h.decide(vkRWin, 0, 40, 0x0101) {
		t.Fatal("not captured for the detached window")
	}
	if len(det.got) != 2 || det.got[0] != (keyEvt{true, "MetaRight"}) || len(main.got) != 2 {
		t.Fatalf("main=%v detached=%v", main.got, det.got)
	}
	// The window in front does not capture (its chat box is focused): the
	// shell gets the key, even though the other window captures.
	det.capture = false
	if h.decide(vkLWin, 0, 50, wmKeyDown) || h.decide(vkLWin, 0, 60, 0x0101) {
		t.Fatal("captured for a window whose page does not capture")
	}
	// Another application in front: nothing captured.
	fg = 999
	main.capture, det.capture = true, true
	if h.decide(vkLWin, 0, 70, wmKeyDown) || h.decide(vkLWin, 0, 80, 0x0101) {
		t.Fatal("captured while another application is in front")
	}
	// The detached window closes while it holds a press: its release is
	// still swallowed (the shell saw no press), and handed to that bridge
	// (the real one ignores it once closed).
	fg = 200
	if !h.decide(vkLWin, 0, 90, wmKeyDown) {
		t.Fatal("press not captured")
	}
	reg.unregister(200)
	fg = 100
	if !h.decide(vkLWin, 0, 100, 0x0101) {
		t.Fatal("release of a closed window's press reached the shell")
	}
	if n := len(det.got); n != 4 || det.got[3] != (keyEvt{false, "MetaLeft"}) {
		t.Fatalf("detached=%v", det.got)
	}
	// Unregistered: a new press while that HWND is in front is not ours.
	fg = 200
	if h.decide(vkLWin, 0, 110, wmKeyDown) {
		t.Fatal("captured for an unregistered window")
	}
	if reg.count() != 1 {
		t.Fatalf("registry holds %d windows", reg.count())
	}
}

// Local clipboard copies go to the window the operator worked in last.
func TestBridgeRegistryClipboardTarget(t *testing.T) {
	fg := uintptr(0)
	reg := newBridgeRegistry(func() uintptr { return fg })
	reg.register(100, &fakeWin{})
	// One window: every copy goes to it (the behaviour before detached windows).
	if !reg.clipboardTarget(100) {
		t.Fatal("single window: not the target")
	}
	reg.register(200, &fakeWin{})
	// Two windows, none of ours seen in front yet: both, as before.
	if !reg.clipboardTarget(100) || !reg.clipboardTarget(200) {
		t.Fatal("no window seen in front yet: both are targets")
	}
	fg = 200
	if reg.clipboardTarget(100) || !reg.clipboardTarget(200) {
		t.Fatal("detached window in front: only it gets the copy")
	}
	// The operator copies in another application: the last of our windows
	// in front keeps it.
	fg = 555
	if reg.clipboardTarget(100) || !reg.clipboardTarget(200) {
		t.Fatal("another application in front: the last window of ours keeps the copy")
	}
	fg = 100
	if !reg.clipboardTarget(100) || reg.clipboardTarget(200) {
		t.Fatal("main window in front: only it")
	}
	// The last active window closes: back to one window.
	reg.unregister(100)
	fg = 0
	if !reg.clipboardTarget(200) {
		t.Fatal("the remaining window gets the copies")
	}
	reg.register(0, &fakeWin{})
	reg.register(300, nil)
	if reg.count() != 1 {
		t.Fatal("a null HWND or bridge was registered")
	}
}
