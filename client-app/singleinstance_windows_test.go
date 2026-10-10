//go:build windows

package main

import (
	"syscall"
	"testing"
	"unsafe"
)

// The mutex is per name: a first create holds it, a second sees it taken,
// and once the holder's handle is closed the name is free again (what the
// update relaunch relies on: the old process gone, its handle with it).
func TestTryInstanceMutexRoundTrip(t *testing.T) {
	saved := instanceMutex
	defer func() { instanceMutex = saved }()
	name, _ := syscall.UTF16PtrFromString(singleInstanceMutex)
	// Another holder (as the running app would be).
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		t.Fatalf("CreateMutex: %v", err)
	}
	if en, ok := err.(syscall.Errno); ok && en == errorAlreadyExists {
		procCloseHandleSI.Call(h)
		t.Skip("an Oblireach runs in this session")
	}
	instanceMutex = 0
	if tryInstanceMutex() {
		t.Fatal("taken mutex reported free")
	}
	if instanceMutex != 0 {
		t.Fatal("a launch that lost kept a handle")
	}
	procCloseHandleSI.Call(h)
	if !tryInstanceMutex() {
		t.Fatal("free mutex reported taken")
	}
	procCloseHandleSI.Call(instanceMutex)
}

func TestSingleInstanceMsgRegistered(t *testing.T) {
	if m := singleInstanceMsg(); m < 0xC000 {
		t.Fatalf("registered message out of range: %#x", m)
	}
}
