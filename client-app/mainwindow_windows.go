//go:build windows

package main

// The main window: a frameless host window (webhost_windows.go,
// frame_windows.go) like the detached session windows. Its page's top bar is
// its title bar. Closing it quits the app (the detached windows go with it),
// unless it has its notification-area icon (tray_windows.go): minimizing or
// closing it then hides it there, and the icon's Quit ends the app.

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	mainMinW          = 820 // smallest main window, at 96 DPI
	mainMinH          = 520
	mainCaptionH      = 44 // the page's top bar (ui.go .topbar), and so the native strip
	swShowDefault     = 10
	swShowMinNoActive = 7

	// The app quits (an update installs): the pages end their sessions,
	// then the main window goes after this long (the session windows take
	// closeGraceMs), and the process at the latest after quitWatchdog.
	quitTimerID  = 2
	quitGraceMs  = 900
	quitWatchdog = 15 * time.Second
)

type mainWindow struct {
	webHost
	closeLate bool // WM_CLOSE while the WebView2 was being created: closed once it is ready
	quitting  bool // quitApp ran: the window goes when its timer fires
}

var mainWin *mainWindow // UI thread only

// newMainWindow creates the main window (clientW x clientH of page, at 96
// DPI), shows it (minimized: in the taskbar, not activated) and creates its
// WebView2. origin: the local UI's.
func newMainWindow(title, origin string, clientW, clientH int32, minimized bool) (*mainWindow, error) {
	cls, err := registerWindowClass("OblireachMainWindow", mainWndProc)
	if err != nil {
		return nil, err
	}
	w := &mainWindow{}
	w.origin = origin
	w.frame = frameState{minW: mainMinW, minH: mainMinH, captionH: mainCaptionH}
	titleW, _ := syscall.UTF16PtrFromString(title)
	hinst, _, _ := procGetModuleHandleW.Call(0)
	band := resizeBand(0) // the frame kept on the left, right and bottom (at 96 DPI: resized below)
	mainWin = w
	hwnd, _, e := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(titleW)),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, uintptr(clientW+2*band), uintptr(clientH+band),
		0, 0, hinst, 0)
	if hwnd == 0 {
		mainWin = nil
		return nil, fmt.Errorf("CreateWindowEx: %v", e)
	}
	w.hwnd = hwnd
	frameReady(hwnd) // our frame, before the window is ever shown
	// Its size at the DPI of the screen it is on (dpi_windows.go): the page's
	// clientW x clientH DIPs, the frame kept on the left, right and bottom.
	dpi, b := dpiOf(hwnd), resizeBand(hwnd)
	w.setInitialSize(scaleDpi(clientW, dpi)+2*b, scaleDpi(clientH, dpi)+b)
	// A second launch brings it back (singleinstance_windows.go).
	allowActivateMessage(hwnd)
	if minimized {
		procShowWindow.Call(hwnd, swShowMinNoActive)
	} else {
		procShowWindow.Call(hwnd, swShowDefault)
	}
	procUpdateWindow.Call(hwnd)
	if err := w.embed(); err != nil {
		procDestroyWindow.Call(hwnd)
		return nil, err
	}
	if !minimized {
		w.chromium.Focus() // the keyboard goes to the page (the sign-in fields)
	}
	if w.closeLate {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
	return w, nil
}

// Run pumps the app's messages until the main window is closed.
func (w *mainWindow) Run() { runMessageLoop() }

func mainWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	w := mainWin
	if w == nil || w.hwnd != hwnd {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	if mainTray != nil {
		if r, done := mainTray.handle(msg, wParam, lParam); done {
			return r
		}
		// Minimized with the icon: hidden there, once Windows is done.
		if msg == wmSize && wParam == sizeMinimized && mainTray.active() && !w.quitting {
			procPostMessageW.Call(hwnd, wmTrayHide, 0, 0)
		}
	}
	if r, done := w.frameProc(msg, wParam, lParam); done {
		return r
	}
	// A second launch of the app (singleinstance_windows.go): this window
	// back on screen, in front.
	if am := singleInstanceMsg(); am != 0 && msg == am {
		return w.handleActivate()
	}
	switch msg {
	case wmHostDispatch:
		w.drain()
		return 0
	case wmActivate:
		// Back to the window (Alt+Tab, taskbar): the keyboard goes to the
		// page, not to the host window.
		if wParam&0xFFFF != waInactive && w.ready && !w.finished {
			w.chromium.Focus()
		}
	case wmClose:
		if !w.ready && !w.finished {
			// Still inside Embed (its nested loop runs this): destroying the
			// parent now would make the pending WebView2 creation fail, which
			// the edge package treats as fatal.
			w.closeLate = true
			return 0
		}
		// With the icon: into the notification area (its menu quits).
		if mainTray.active() && !w.quitting {
			mainTray.hide()
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmTimer:
		if wParam == quitTimerID {
			procKillTimer.Call(hwnd, quitTimerID)
			procDestroyWindow.Call(hwnd)
			return 0
		}
	case wmDestroy:
		w.markGone()
		mainTray.remove()
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// quitApp closes the app the clean way, for the installer of an update
// (updater_windows.go), on the UI thread: the main page leaves its sessions
// and chats as on sign-out (window.__reachAppClosing), every session window
// closes its own way (its page ends its sessions: detached_windows.go
// beginClose), then the main window goes, and the process with it. The
// installer waits for this process to end; a watchdog makes sure it does.
func quitApp(w *mainWindow, b *nativeBridge) {
	if w.quitting || w.finished {
		return
	}
	w.quitting = true
	time.AfterFunc(quitWatchdog, func() { os.Exit(0) })
	b.evalMain("__reachAppClosing", "")
	if b.windows != nil {
		b.windows.closeAll()
	}
	if r, _, _ := procSetTimer.Call(w.hwnd, quitTimerID, quitGraceMs, 0); r == 0 {
		procDestroyWindow.Call(w.hwnd)
	}
}
