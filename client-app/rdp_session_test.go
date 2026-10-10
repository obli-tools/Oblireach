//go:build windows

package main

// rdp_session_test.go — exercises the risky Win32 plumbing rdp_session.go's
// embed() relies on (reparenting a real, independently created top-level
// window as a child, stripping its top-level frame, raising it to the top
// of the z-order) without needing a real RDP server: notepad.exe stands in
// for mstsc.exe. This is the one part of the RDP embedding that IS verifiable
// without an interactive session; actually driving an RDP connection
// through the embedded window is not, and needs a manual check on a real
// machine (see rdp_session.go's header).

import (
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var procRdpTestGetParent = modUser32.NewProc("GetParent")

func TestRdpAvailable(t *testing.T) {
	if !rdpAvailable() {
		t.Skip("mstsc.exe not on PATH in this environment")
	}
}

func TestRdpFindWindowAndEmbed(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	notepad, err := exec.LookPath("notepad.exe")
	if err != nil {
		t.Skip("notepad.exe not available to stand in for mstsc.exe")
	}

	// A throwaway top-level window to act as the app's own window (b.hwnd
	// in rdp_bridge_windows.go), never shown — same pattern as
	// frame_windows_test.go's TestFramelessWindow.
	cls, err := registerWindowClass("OblireachRdpTestParent", rdpContainerWndProc)
	if err != nil {
		t.Skipf("no window class: %v", err)
	}
	title, _ := syscall.UTF16PtrFromString("rdp test parent")
	hinst, _, _ := procGetModuleHandleW.Call(0)
	parent, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, 100, 100, 900, 600, 0, 0, hinst, 0)
	if parent == 0 {
		t.Skipf("no parent window: %v", e)
	}
	defer procDestroyWindow.Call(parent)

	container, err := rdpCreateContainer(parent, 10, 20, 400, 300)
	if err != nil {
		t.Fatalf("rdpCreateContainer: %v", err)
	}
	defer procDestroyWindow.Call(container)

	cmd := exec.Command(notepad)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start notepad: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	hwnd, err := rdpFindWindow(uint32(cmd.Process.Pid), 10*time.Second)
	if err != nil {
		t.Fatalf("rdpFindWindow: %v", err)
	}

	// Before embedding: a real top-level window (its own ancestor root),
	// not a child of our container.
	if p, _, _ := procRdpTestGetParent.Call(hwnd); p == container {
		t.Fatal("notepad window already parented to the container before embed()")
	}

	s := &rdpSession{hwnd: hwnd, parent: container}
	s.embed(0, 0, 400, 300)

	if p, _, _ := procRdpTestGetParent.Call(hwnd); p != container {
		t.Fatalf("GetParent after embed = %#x, want container %#x", p, container)
	}
	style, _, _ := procRdpGetWindowLongPtr.Call(hwnd, rdpGwlStyle)
	if style&rdpWsChild == 0 {
		t.Error("WS_CHILD not set after embed")
	}
	if style&rdpWsPopup != 0 {
		t.Error("WS_POPUP still set after embed")
	}
	if style&rdpWsCaption != 0 {
		t.Error("WS_CAPTION still set after embed")
	}

	// Resize/reposition still works once embedded (the ResizeObserver path).
	s.Resize(5, 5, 200, 150)
	var rc winRect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	if got := rc.Right - rc.Left; got != 200 {
		t.Errorf("width after Resize = %d, want 200", got)
	}
}

func TestRdpFindWindowTimesOutForUnknownPid(t *testing.T) {
	if _, err := rdpFindWindow(0xFFFFFFF0, 300*time.Millisecond); err != errRdpNoWindow {
		t.Fatalf("err = %v, want errRdpNoWindow", err)
	}
}

func TestRdpConnRegistry(t *testing.T) {
	r := &rdpConnRegistry{conns: map[string]*rdpEntry{}}
	id1, id2 := r.newID(), r.newID()
	if id1 == id2 {
		t.Fatal("newID returned the same id twice")
	}
	r.put(id1, &rdpSession{}, 42, nil)
	e, ok := r.get(id1)
	if !ok || e.container != 42 {
		t.Fatalf("get(%q) = %+v, %v", id1, e, ok)
	}
	if _, ok := r.get(id2); ok {
		t.Fatal("get found an id that was never put")
	}
	r.remove(id1)
	if _, ok := r.get(id1); ok {
		t.Fatal("get found a removed id")
	}
}
