//go:build windows

package main

// tray_windows.go — the notification-area icon of the main window
// (Shell_NotifyIconW; what it does: tray.go). Everything here runs on the UI
// thread: the main window's procedure (its callback messages, the menu) and
// the bindings (reachTrayUnread / reachTrayNotify, the setting's
// startupCtl.set), which WebView2 runs there too.

import (
	"syscall"
	"unsafe"
)

var (
	procShellNotifyIconW       = modShell32.NewProc("Shell_NotifyIconW")
	procCreatePopupMenu        = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW            = modUser32.NewProc("AppendMenuW")
	procSetMenuDefaultItem     = modUser32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu         = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu            = modUser32.NewProc("DestroyMenu")
	procRegisterWindowMessageW = modUser32.NewProc("RegisterWindowMessageW")
	procDestroyIcon            = modUser32.NewProc("DestroyIcon")
)

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10

	niifUser             = 0x04 // the icon's own image in the notification
	niifRespectQuietTime = 0x80 // (Focus assist: the system decides)

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfSeparator = 0x0800

	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	wmNull          = 0x0000
	wmContextMenu   = 0x007B
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	ninBalloonClick = 0x0405 // NIN_BALLOONUSERCLICK (WM_USER + 5)

	sizeMinimized = 1
	swShow        = 5

	// The icon's callback message, and the deferred hide after a minimize
	// (hidden once Windows is done minimizing, never inside WM_SIZE).
	wmTrayCallback = 0x8000 + 0x31
	wmTrayHide     = 0x8000 + 0x32

	trayIconID = 1
)

// NOTIFYICONDATAW (the Vista+ layout; 976 bytes on 64-bit, 956 on 32-bit).
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

// appTray: the main window's icon.
type appTray struct {
	hwnd   uintptr
	icon   uintptr
	added  bool // the icon is in the notification area
	hidden bool // the main window is hidden there
	unread int  // unread chat messages (the page's count)

	taskbarCreated uintptr // "TaskbarCreated": Explorer restarted, the icon goes back

	onQuit func() // Quit (the clean way: quitApp)
	onChat func() // a notification clicked, or its menu line: the Chat page
	focus  func() // the keyboard to the page once shown again

	// Test seams (nil: the real calls).
	notifyHook func(op uint32, d *notifyIconData) bool
	showHook   func(cmd uintptr)
}

var mainTray *appTray // UI thread only

func newAppTray(hwnd uintptr) *appTray {
	t := &appTray{hwnd: hwnd}
	if exe := exePath(); exe != nil {
		var small uintptr // the app's own icon (its resource), the small one
		procExtractIconExW.Call(uintptr(unsafe.Pointer(exe)), 0, 0, uintptr(unsafe.Pointer(&small)), 1)
		t.icon = small
	}
	if name, err := syscall.UTF16PtrFromString("TaskbarCreated"); err == nil {
		t.taskbarCreated, _, _ = procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	}
	return t
}

func (t *appTray) data(flags uint32) *notifyIconData {
	d := &notifyIconData{HWnd: t.hwnd, UID: trayIconID, UFlags: flags, UCallbackMessage: wmTrayCallback, HIcon: t.icon}
	d.CbSize = uint32(unsafe.Sizeof(*d))
	copyUTF16(d.SzTip[:], trayTip(t.unread))
	return d
}

func (t *appTray) notify(op uint32, d *notifyIconData) bool {
	if t.notifyHook != nil {
		return t.notifyHook(op, d)
	}
	r, _, _ := procShellNotifyIconW.Call(uintptr(op), uintptr(unsafe.Pointer(d)))
	return r != 0
}

func (t *appTray) show(cmd uintptr) {
	if t.showHook != nil {
		t.showHook(cmd)
		return
	}
	procShowWindow.Call(t.hwnd, cmd)
}

// active: the icon is there (the setting on): minimize and close hide.
func (t *appTray) active() bool { return t != nil && t.added }

// enable adds or removes the icon (the setting). Removed while the window
// is hidden: the window comes back first (never an app without a way back).
func (t *appTray) enable(on bool) {
	if on == t.added {
		return
	}
	if on {
		t.added = t.notify(nimAdd, t.data(nifMessage|nifIcon|nifTip))
		return
	}
	if t.hidden {
		t.restore()
	}
	t.notify(nimDelete, t.data(0))
	t.added = false
}

// hide: the main window into the notification area.
func (t *appTray) hide() {
	if !t.active() {
		return
	}
	t.show(swHide)
	t.hidden = true
}

// restore: the main window back on screen, in front.
func (t *appTray) restore() {
	t.hidden = false
	t.show(swShow)
	if t.showHook == nil {
		if r, _, _ := procIsIconic.Call(t.hwnd); r != 0 {
			procShowWindow.Call(t.hwnd, swRestore)
		}
		procSetForegroundWindow.Call(t.hwnd)
	}
	if t.focus != nil {
		t.focus()
	}
}

// setUnread: the page's unread chat messages (tooltip, menu).
func (t *appTray) setUnread(n int) {
	if n < 0 {
		n = 0
	}
	if n == t.unread {
		return
	}
	t.unread = n
	if t.added {
		t.notify(nimModify, t.data(nifTip))
	}
}

// balloon: a notification of the icon (a chat message while the window is
// hidden). False when none was shown (no icon, or the window is on screen:
// its taskbar button flashes instead).
func (t *appTray) balloon(title, text string) bool {
	if !t.active() || !t.hidden {
		return false
	}
	title, text = trayNoteText(title, text)
	d := t.data(nifInfo)
	copyUTF16(d.SzInfoTitle[:], title)
	copyUTF16(d.SzInfo[:], text)
	d.DwInfoFlags = niifUser | niifRespectQuietTime
	return t.notify(nimModify, d)
}

// handle: the main window's messages that concern the icon.
func (t *appTray) handle(msg, wParam, lParam uintptr) (uintptr, bool) {
	switch {
	case msg == wmTrayCallback:
		switch lParam & 0xFFFF {
		case wmLButtonDblClk:
			t.restore()
		case wmRButtonUp, wmContextMenu:
			t.menu()
		case ninBalloonClick:
			t.restore()
			if t.onChat != nil {
				t.onChat()
			}
		}
		return 0, true
	case msg == wmTrayHide:
		t.hide()
		return 0, true
	case t.taskbarCreated != 0 && msg == t.taskbarCreated:
		if t.added { // Explorer restarted: the icon again
			t.added = t.notify(nimAdd, t.data(nifMessage|nifIcon|nifTip))
		}
		return 0, false
	}
	return 0, false
}

// menu: the icon's menu at the mouse, then its command.
func (t *appTray) menu() {
	h, _, _ := procCreatePopupMenu.Call()
	if h == 0 {
		return
	}
	defer procDestroyMenu.Call(h)
	for _, it := range trayMenu(t.unread) {
		if it.sep {
			procAppendMenuW.Call(h, mfSeparator, 0, 0)
			continue
		}
		s, err := syscall.UTF16PtrFromString(it.text)
		if err != nil {
			continue
		}
		flags := uintptr(mfString)
		if it.grey {
			flags |= mfGrayed
		}
		procAppendMenuW.Call(h, flags, uintptr(it.id), uintptr(unsafe.Pointer(s)))
		if it.bold {
			procSetMenuDefaultItem.Call(h, uintptr(it.id), 0)
		}
	}
	var pt winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// (the documented dance: the menu closes when clicking elsewhere)
	procSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(h, tpmRightButton|tpmReturnCmd|tpmNoNotify, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	t.command(int(cmd))
}

func (t *appTray) command(cmd int) {
	switch cmd {
	case trayCmdOpen:
		t.restore()
	case trayCmdChat:
		t.restore()
		if t.onChat != nil {
			t.onChat()
		}
	case trayCmdQuit:
		if t.onQuit != nil {
			t.onQuit()
		}
	}
}

// remove: the icon goes (the app ends).
func (t *appTray) remove() {
	if t == nil || !t.added {
		return
	}
	t.notify(nimDelete, t.data(0))
	t.added = false
}

// copyUTF16 writes s into a fixed NUL-terminated UTF-16 buffer (cut to fit).
func copyUTF16(dst []uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		u = []uint16{0}
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// ── Bindings (main window) ─────────────────────────────────────────────────

// trayUnread (JS reachTrayUnread): the page's unread chat messages.
func (b *nativeBridge) trayUnread(tok string, n int) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if b.tray == nil {
		return false, nil
	}
	b.tray.setUnread(n)
	return true, nil
}

// trayNotify (JS reachTrayNotify): a chat event while the window may be
// hidden in the notification area: shown there (true), or not (false: the
// page's other alerts are enough, the window is on screen).
func (b *nativeBridge) trayNotify(tok, title, text string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if b.tray == nil {
		return false, nil
	}
	return b.tray.balloon(title, text), nil
}
