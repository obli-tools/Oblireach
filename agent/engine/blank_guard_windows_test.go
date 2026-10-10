//go:build windows

package engine

import (
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Opt-in (touches a live system setting and the real screen; needs Windows 10
// 2004+ on an interactive desktop):
//
//	set OBLIREACH_BLANK_LIVE=1
//	go test -run TestBlankGuardLive -count=1 .
//
// It checks that, while the page is up:
//   - full-window drag is forced on and put back to its previous value after;
//   - the page is re-asserted above a topmost window created after it.
func TestBlankGuardLive(t *testing.T) {
	if os.Getenv("OBLIREACH_BLANK_LIVE") != "1" {
		t.Skip("set OBLIREACH_BLANK_LIVE=1 to run the guard against this desktop")
	}
	if ok, reason := platformBlankScreenAvailable(); !ok {
		t.Skipf("not available here: %s", reason)
	}

	before, okBefore := bgDragFullWindows()
	if !okBefore {
		t.Skip("cannot read SPI_GETDRAGFULLWINDOWS here")
	}

	blankScreenBeat()
	if reason := bsDo(bsRequest{show: true, text: blankScreenTextFor("fr", "Test Oblireach")}); reason != "" {
		t.Fatalf("show failed: %s", reason)
	}
	defer bsDo(bsRequest{show: false})
	time.Sleep(300 * time.Millisecond)

	// Full-window drag must be on while the page is up.
	if on, ok := bgDragFullWindows(); !ok || !on {
		t.Errorf("full-window drag = (%v,%v) while the page is up, want on", on, ok)
	}

	// A topmost window created AFTER the page lands above it in the band; the
	// guard must put the page back on top. Create one, let the hook fire, then
	// check the z-order.
	intruder := createTopmostTestWindow(t)
	defer destroyTestWindow(intruder)
	time.Sleep(300 * time.Millisecond) // the WinEvent hook coalesces + re-asserts

	if !pageAboveAll(t, intruder) {
		// Give the one-second tick a chance as well, then fail if still below.
		time.Sleep(1200 * time.Millisecond)
		if !pageAboveAll(t, intruder) {
			t.Errorf("a topmost window created after the page is above it: re-assert did not win")
		}
	}

	bsDo(bsRequest{show: false})
	time.Sleep(200 * time.Millisecond)

	// The previous value must be restored.
	if after, ok := bgDragFullWindows(); !ok || after != before {
		t.Errorf("full-window drag = (%v,%v) after the page, want %v (its value before)", after, ok, before)
	}
}

// pageAboveAll: every curtain window is enumerated (top of the Z order first)
// before the intruder window.
func pageAboveAll(t *testing.T, intruder uintptr) bool {
	t.Helper()
	order := enumTopLevelZOrder(t)
	posOf := func(h uintptr) int {
		for i, w := range order {
			if w == h {
				return i
			}
		}
		return 1 << 30
	}
	intruderPos := posOf(intruder)
	for _, w := range bsWindows {
		if posOf(w.hwnd) > intruderPos {
			return false
		}
	}
	return true
}

// enumTopLevelZOrder returns the visible top-level windows in Z order, the
// front-most first (EnumWindows order).
func enumTopLevelZOrder(t *testing.T) []uintptr {
	t.Helper()
	u32 := syscall.NewLazyDLL("user32.dll")
	enumWindows := u32.NewProc("EnumWindows")
	isVisible := u32.NewProc("IsWindowVisible")
	var out []uintptr
	cb := syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		if r, _, _ := isVisible.Call(hwnd); r != 0 {
			out = append(out, hwnd)
		}
		return 1
	})
	enumWindows.Call(cb, 0)
	return out
}

func createTopmostTestWindow(t *testing.T) uintptr {
	t.Helper()
	u32 := syscall.NewLazyDLL("user32.dll")
	createWindowEx := u32.NewProc("CreateWindowExW")
	showWindow := u32.NewProc("ShowWindow")
	cls, _ := syscall.UTF16PtrFromString("Static") // a ready-made system class
	name, _ := syscall.UTF16PtrFromString("oblireach-guard-test")
	const wsPopup = 0x80000000
	const wsExTopmost = 0x00000008
	const wsExToolWindow = 0x00000080
	hwnd, _, _ := createWindowEx.Call(wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)), wsPopup,
		100, 100, 300, 200, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatal("could not create the intruder window")
	}
	showWindow.Call(hwnd, 5 /*SW_SHOW*/)
	return hwnd
}

func destroyTestWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	syscall.NewLazyDLL("user32.dll").NewProc("DestroyWindow").Call(hwnd)
}
