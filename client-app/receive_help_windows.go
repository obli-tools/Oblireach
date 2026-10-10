//go:build windows

package main

// "Receive help" on Windows: the backend is receive_help_link.go
// (supportlink + the engine in this process); this file is the Windows
// side: the vault held, the main window's attention.
//
// Limits of a host inside Desktop, said on the page: Desktop
// runs as the signed-in user, not as a service. The operator sees and drives
// the user's desktop, but not the secure desktop (UAC prompts, Ctrl+Alt+Del,
// the lock screen), nor windows of elevated programs (UIPI) unless Desktop
// itself runs elevated. Desktop is Per-Monitor V2 DPI aware (dpi_windows.go:
// its manifest, and main before any window), the mode the engine's
// EnablePerMonitorDPIAwareness asks for: the virtual screen, the capture and
// SendInput all speak physical pixels, so the pointer lands right on screens
// of any scale.

import (
	"os"
	"syscall"
	"unsafe"
)

// newAppHelpHost: the app's "Receive help" (support.go appSupportHub).
func newAppHelpHost(settings *supportSettings) *helpHost {
	h := newHelpHost(settings, &helpLinkBackend{version: appVersion})
	h.onSession = helpOnSession
	h.onAttention = func(string) { helpAttention(false) }
	return h
}

// helpOnSession: while a session runs the password vault is locked and held
// locked (no code, no Windows Hello: the operator drives this computer).
// The window flashes once when it starts.
func helpOnSession(on bool) {
	if v, err := getAppVault(); err == nil {
		v.SetHeld(on)
	}
	if on {
		helpAttention(true)
	}
}

// helpAttention: an acceptance request brings the main window back on
// screen and in front (out of the notification area too: the singleinstance
// activation message, handled on the UI thread), and flashes its taskbar
// button; a session start only flashes it (a few times).
func helpAttention(flashOnly bool) {
	hwnd := helpOwnMainWindow()
	if hwnd == 0 {
		return
	}
	if !flashOnly {
		if msg := singleInstanceMsg(); msg != 0 {
			procPostMessageW.Call(hwnd, msg, 0, 0)
		}
	}
	fi := flashWInfo{Hwnd: hwnd, DwFlags: flashwAll, UCount: 3}
	if !flashOnly {
		fi.DwFlags, fi.UCount = flashwAll|flashwTimerNoFG, 0
	}
	fi.CbSize = uint32(unsafe.Sizeof(fi))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}

var procFindWindowExW = modUser32.NewProc("FindWindowExW")

// helpOwnMainWindow: this process's main window (never another Desktop's:
// a test process has none), 0 if none.
func helpOwnMainWindow() uintptr {
	cls, err := syscall.UTF16PtrFromString(mainWindowClass)
	if err != nil {
		return 0
	}
	self := uint32(os.Getpid())
	var hwnd uintptr
	for i := 0; i < 64; i++ {
		hwnd, _, _ = procFindWindowExW.Call(0, hwnd, uintptr(unsafe.Pointer(cls)), 0)
		if hwnd == 0 {
			return 0
		}
		var pid uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == self {
			return hwnd
		}
	}
	return 0
}

// startIncomingSupport: at the app's start, "Receive help" runs in the
// background while "Allow incoming support" is on (single instance: only
// the one Desktop of this Windows session hosts).
func startIncomingSupport() {
	go appSupportHub().help.autoStart()
}

// stopIncomingSupport: the app quits: the registration closes (the
// preference is kept).
func stopIncomingSupport() {
	if h := appSupportHub().help; h != nil {
		h.stop()
	}
}
