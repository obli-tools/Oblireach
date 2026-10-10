//go:build windows

package main

// The Win32 side of singleinstance.go.

import (
	"log"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	procCreateMutexW              = modKernel32.NewProc("CreateMutexW")
	procFindWindowW               = modUser32.NewProc("FindWindowW")
	procGetWindowThreadProcessId  = modUser32.NewProc("GetWindowThreadProcessId")
	procAllowSetForegroundWindow  = modUser32.NewProc("AllowSetForegroundWindow")
	procSendMessageTimeoutW       = modUser32.NewProc("SendMessageTimeoutW")
	procChangeWindowMessageFilter = modUser32.NewProc("ChangeWindowMessageFilterEx")
	procCloseHandleSI             = modKernel32.NewProc("CloseHandle")
)

const (
	errorAlreadyExists = 183
	smtoAbortIfHung    = 0x0002
	smtoBlock          = 0x0001
	msgfltAllow        = 1
	// The main window's class (mainwindow_windows.go newMainWindow): not the
	// session windows' (OblireachSessionWindow).
	mainWindowClass = "OblireachMainWindow"
)

// instanceMutex: this process's handle, kept until it ends (the named object
// lives as long as one handle is open: no thread ownership involved).
var instanceMutex uintptr

var (
	activateMsgOnce sync.Once
	activateMsg     uintptr
)

// singleInstanceMsg: the registered activation message (0: unavailable).
func singleInstanceMsg() uintptr {
	activateMsgOnce.Do(func() {
		if name, err := syscall.UTF16PtrFromString(singleInstanceMsgName); err == nil {
			activateMsg, _, _ = procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
		}
	})
	return activateMsg
}

// tryInstanceMutex creates the session's mutex. true: none existed, this
// process holds it now. false: another instance has it (the handle is closed
// again, so that its end frees the name).
func tryInstanceMutex() bool {
	name, _ := syscall.UTF16PtrFromString(singleInstanceMutex)
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		// No mutex at all (should not happen): never block the app on it.
		log.Printf("single instance: CreateMutex: %v", err)
		return true
	}
	if en, ok := err.(syscall.Errno); ok && en == errorAlreadyExists {
		procCloseHandleSI.Call(h)
		return false
	}
	instanceMutex = h
	return true
}

// activateExisting finds the running app's main window and asks it to show
// itself in front (unless quiet: then only whether it is there). Returns
// whether a window was found and its answer.
func activateExisting(quiet bool) (bool, uintptr) {
	cls, _ := syscall.UTF16PtrFromString(mainWindowClass)
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if hwnd == 0 {
		return false, 0
	}
	if quiet {
		return true, 0
	}
	msg := singleInstanceMsg()
	if msg == 0 {
		return true, 0
	}
	// The running app may take the foreground (this launch has it: the
	// user just started it); without this, Windows only flashes its button.
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		procAllowSetForegroundWindow.Call(uintptr(pid))
	}
	var res uintptr
	r, _, _ := procSendMessageTimeoutW.Call(hwnd, msg, 0, 0, smtoAbortIfHung|smtoBlock, 3000, uintptr(unsafe.Pointer(&res)))
	if r == 0 {
		return true, 0 // hung or timed out
	}
	return true, res
}

// winInstance: the single instance of the app in this Windows session
// (instanceGuard, platform.go): a named mutex, the running one's main window
// brought back by a registered message.
type winInstance struct{}

var platformInstance instanceGuard = winInstance{}

func (winInstance) claim(args []string) bool { return ensureSingleInstance(args) }

// ensureSingleInstance: true, this launch is the app; false, it must end
// (the running one is in front now, or nothing could be shown).
func ensureSingleInstance(args []string) bool {
	quiet := quietLaunch(args)
	start := time.Now()
	for {
		o := instanceObs{Acquired: tryInstanceMutex(), Elapsed: time.Since(start), Quiet: quiet}
		if !o.Acquired {
			o.Window, o.Ack = activateExisting(quiet)
		}
		switch decideInstance(o) {
		case instanceRun:
			return true
		case instanceExit:
			if !o.Window || (o.Ack != singleInstanceAckShown && !quiet) {
				log.Printf("single instance: another Oblireach runs in this session; ending")
			}
			return false
		}
		time.Sleep(singleInstancePoll)
	}
}

// allowActivateMessage: the activation message may come from a launch at a
// lower integrity level (UIPI), to the main window only.
func allowActivateMessage(hwnd uintptr) {
	if msg := singleInstanceMsg(); msg != 0 {
		procChangeWindowMessageFilter.Call(hwnd, msg, msgfltAllow, 0)
	}
}

// handleActivate: the main window's answer to a second launch (mainWndProc,
// UI thread): back on screen and in front, out of the taskbar or the
// notification area. While the app quits: "quitting" (the launch waits).
func (w *mainWindow) handleActivate() uintptr {
	if w.quitting || w.finished {
		return singleInstanceAckQuitting
	}
	if mainTray != nil && mainTray.active() {
		mainTray.restore() // (hidden there or not: shown, restored, in front, focused)
		return singleInstanceAckShown
	}
	if r, _, _ := procIsIconic.Call(w.hwnd); r != 0 {
		procShowWindow.Call(w.hwnd, swRestore)
	} else {
		procShowWindow.Call(w.hwnd, swShow)
	}
	procSetForegroundWindow.Call(w.hwnd)
	if w.ready && !w.finished {
		w.chromium.Focus()
	}
	return singleInstanceAckShown
}
