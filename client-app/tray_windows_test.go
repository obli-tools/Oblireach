//go:build windows

package main

import (
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// NOTIFYICONDATAW as Windows lays it out.
func TestNotifyIconDataLayout(t *testing.T) {
	want := uintptr(976)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 956
	}
	var d notifyIconData
	if unsafe.Sizeof(d) != want {
		t.Fatalf("NOTIFYICONDATAW: %d bytes, want %d", unsafe.Sizeof(d), want)
	}
	if unsafe.Sizeof(uintptr(0)) == 8 && (unsafe.Offsetof(d.HIcon) != 32 || unsafe.Offsetof(d.SzInfo) != 304 || unsafe.Offsetof(d.HBalloonIcon) != 968) {
		t.Fatalf("offsets: hIcon %d szInfo %d hBalloonIcon %d", unsafe.Offsetof(d.HIcon), unsafe.Offsetof(d.SzInfo), unsafe.Offsetof(d.HBalloonIcon))
	}
}

type trayCall struct {
	op    uint32
	flags uint32
	tip   string
	title string
	text  string
}

func testTray() (*appTray, *[]trayCall, *[]uintptr) {
	calls := &[]trayCall{}
	shows := &[]uintptr{}
	tr := &appTray{hwnd: 0x42}
	tr.notifyHook = func(op uint32, d *notifyIconData) bool {
		*calls = append(*calls, trayCall{op, d.UFlags, syscall.UTF16ToString(d.SzTip[:]), syscall.UTF16ToString(d.SzInfoTitle[:]), syscall.UTF16ToString(d.SzInfo[:])})
		return true
	}
	tr.showHook = func(cmd uintptr) { *shows = append(*shows, cmd) }
	return tr, calls, shows
}

// The icon: added and removed with the setting; minimize / close hide the
// window only while it is there; removed while hidden: the window comes back.
func TestAppTrayLifecycle(t *testing.T) {
	tr, calls, shows := testTray()
	focused := 0
	tr.focus = func() { focused++ }
	tr.hide()
	if tr.hidden || len(*shows) != 0 {
		t.Fatal("hidden without an icon (no way back)")
	}
	tr.enable(true)
	if !tr.active() || len(*calls) != 1 || (*calls)[0].op != nimAdd || (*calls)[0].flags != nifMessage|nifIcon|nifTip || (*calls)[0].tip != "Oblireach" {
		t.Fatalf("add: %+v", *calls)
	}
	tr.enable(true)
	if len(*calls) != 1 {
		t.Fatal("added twice")
	}
	tr.hide()
	if !tr.hidden || (*shows)[len(*shows)-1] != 0 {
		t.Fatal("not hidden")
	}
	// A double-click brings it back (and the keyboard to the page).
	if _, done := tr.handle(wmTrayCallback, trayIconID, wmLButtonDblClk); !done || tr.hidden || (*shows)[len(*shows)-1] != swShow || focused != 1 {
		t.Fatal("double-click did not restore")
	}
	// Hidden again, then the setting off: shown first, the icon goes.
	tr.hide()
	tr.enable(false)
	if tr.hidden || tr.active() || (*calls)[len(*calls)-1].op != nimDelete {
		t.Fatalf("removed while hidden: hidden=%v calls=%+v", tr.hidden, *calls)
	}
	// Explorer restarted: the icon again (only when it was there).
	tr.taskbarCreated = 0xC123
	n := len(*calls)
	tr.handle(0xC123, 0, 0)
	if len(*calls) != n {
		t.Fatal("re-added an icon that was off")
	}
	tr.enable(true)
	tr.handle(0xC123, 0, 0)
	if (*calls)[len(*calls)-1].op != nimAdd {
		t.Fatal("not re-added after TaskbarCreated")
	}
	// The deferred hide after a minimize.
	if _, done := tr.handle(wmTrayHide, 0, 0); !done || !tr.hidden {
		t.Fatal("wmTrayHide")
	}
	tr.remove()
	if tr.active() || (*calls)[len(*calls)-1].op != nimDelete {
		t.Fatal("remove")
	}
	var none *appTray
	if none.active() {
		t.Fatal("a nil tray is active")
	}
	none.remove() // (no icon: nothing)
}

// Chat: the unread count in the tooltip and the menu; a notification only
// while the window is hidden there; clicked, the Chat page.
func TestAppTrayChat(t *testing.T) {
	tr, calls, _ := testTray()
	chat := 0
	tr.onChat = func() { chat++ }
	tr.setUnread(3) // (no icon yet: kept, nothing sent)
	if len(*calls) != 0 || tr.unread != 3 {
		t.Fatal("setUnread without an icon")
	}
	tr.enable(true)
	if (*calls)[0].tip != trayTip(3) || !strings.Contains((*calls)[0].tip, "3") {
		t.Fatalf("tip: %q", (*calls)[0].tip)
	}
	tr.setUnread(3)
	if len(*calls) != 1 {
		t.Fatal("an unchanged count was sent again")
	}
	tr.setUnread(0)
	if c := (*calls)[len(*calls)-1]; c.op != nimModify || c.flags != nifTip || c.tip != "Oblireach" {
		t.Fatalf("modify: %+v", c)
	}
	if tr.balloon("PC-1", "hello") {
		t.Fatal("a notification while the window is on screen")
	}
	tr.hide()
	if !tr.balloon("PC-1\n", "hel\x07lo\nworld") {
		t.Fatal("no notification while hidden")
	}
	if c := (*calls)[len(*calls)-1]; c.flags != nifInfo || c.title != "PC-1" || c.text != "hello world" {
		t.Fatalf("notification: %+v", c)
	}
	tr.handle(wmTrayCallback, trayIconID, ninBalloonClick)
	if tr.hidden || chat != 1 {
		t.Fatal("a click on the notification: the window and its Chat page")
	}
	// The menu's commands.
	quit := 0
	tr.onQuit = func() { quit++ }
	tr.hide()
	tr.command(trayCmdChat)
	tr.command(trayCmdQuit)
	if tr.hidden || chat != 2 || quit != 1 {
		t.Fatalf("commands: hidden=%v chat=%d quit=%d", tr.hidden, chat, quit)
	}
}

// The bindings: the page's token, and nothing without an icon.
func TestTrayBindings(t *testing.T) {
	b := &nativeBridge{token: "tok"}
	if _, err := b.trayUnread("x", 1); err != errNotAllowed {
		t.Fatal("trayUnread without the token")
	}
	if _, err := b.trayNotify("x", "a", "b"); err != errNotAllowed {
		t.Fatal("trayNotify without the token")
	}
	if ok, err := b.trayUnread("tok", 2); ok || err != nil {
		t.Fatal("trayUnread without an icon")
	}
	tr, _, _ := testTray()
	b.tray = tr
	if ok, _ := b.trayUnread("tok", 5); !ok || tr.unread != 5 {
		t.Fatal("trayUnread")
	}
	tr.enable(true)
	tr.hide()
	if ok, _ := b.trayNotify("tok", "PC", "msg"); !ok {
		t.Fatal("trayNotify while hidden")
	}
}

// Settings > Application: "tray" saved, and the icon follows.
func TestStartupTraySetting(t *testing.T) {
	c, _ := newTestStartup(&fakeRunKey{})
	on := false
	var followed []bool
	c.tray = func() bool { return on }
	c.setTray = func(v bool) { on = v }
	c.onTray = func(v bool) { followed = append(followed, v) }
	st, err := c.set("tray", true)
	if err != nil || !st.Tray || !on || len(followed) != 1 || !followed[0] {
		t.Fatalf("tray on: %+v %v %v", st, err, followed)
	}
	st, _ = c.set("tray", false)
	if st.Tray || on || followed[1] {
		t.Fatal("tray off")
	}
	c.setTray = nil
	if _, err := c.set("tray", true); err != errBadArgument {
		t.Fatal("tray without its setter")
	}
}
