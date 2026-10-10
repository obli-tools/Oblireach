//go:build windows

package main

import "unsafe"

var procFlashWindowEx = modUser32.NewProc("FlashWindowEx")

const (
	flashwAll       = 0x3 // caption + taskbar button
	flashwTimerNoFG = 0xC // keep flashing until the window comes to the foreground
)

// FLASHWINFO
type flashWInfo struct {
	CbSize    uint32
	Hwnd      uintptr
	DwFlags   uint32
	UCount    uint32
	DwTimeout uint32
}

// attention (JS binding reachAttention): flashes the taskbar button until the
// window comes to the front, for a chat event (message, chat closed by the
// user) while the app is in the background.
func (b *nativeBridge) attention(tok string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if b.hwnd == 0 {
		return false, nil
	}
	fi := flashWInfo{Hwnd: b.hwnd, DwFlags: flashwAll | flashwTimerNoFG}
	fi.CbSize = uint32(unsafe.Sizeof(fi))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	return true, nil
}
