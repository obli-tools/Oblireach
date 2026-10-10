//go:build windows

package main

// The native chat window on a real (hidden) Win32 window: the RichEdit
// history receives the operator's text as plain text, the Allow / Deny
// buttons appear for a remote-control request and answer once, Send sends
// the input, typing and closing emit the same events as the WebView2 page.
// The window is never shown (show() is not called).

import (
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func ncTestText(h uintptr) string {
	n, _, _ := ncGetWindowTextLengthW.Call(h)
	buf := make([]uint16, n+1)
	ncGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

// ncTestDrain removes what the destroyed window left in this thread's queue
// (its WM_QUIT) so later tests on the same OS thread are not affected.
func ncTestDrain() {
	var m chatMsg
	peek := ncUser32.NewProc("PeekMessageW")
	for i := 0; i < 64; i++ {
		if r, _, _ := peek.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1 /* PM_REMOVE */); r == 0 {
			return
		}
	}
}

func TestNativeChatWindowHidden(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer ncTestDrain()

	ev := &sentEvents{}
	i18n := chatStrings(false)
	c, err := newNativeChat("Op", "user", i18n, ev.send)
	if err != nil {
		t.Skipf("no window station / desktop for a window here: %v", err)
	}
	if !c.richEdit {
		t.Log("RichEdit not available: plain EDIT history")
	}

	c.operatorMessage("Bonjour\x00 {\\rtf1 \\b gras}\nligne 2")
	if h := ncTestText(c.history); !strings.Contains(h, "Op: ") || !strings.Contains(h, `Bonjour {\rtf1 \b gras}`) || !strings.Contains(h, "ligne 2") {
		t.Fatalf("history (plain text, RTF never interpreted): %q", h)
	}

	// A message that IS an RTF stream (with an embedded object) stays text.
	evil := `{\rtf1\ansi{\object\objemb{\*\objdata 0102}}HACK}`
	c.operatorMessage(evil) // body after the "[hh:mm] Op: " head
	c.appendRaw(evil+"\r\n", false)
	if h := ncTestText(c.history); strings.Count(h, `{\rtf1\ansi{\object`) != 2 {
		t.Fatalf("RTF interpreted: %q", h)
	}

	c.remoteRequest("")
	if !c.remoteShown || ncTestText(c.remoteText) != i18n["remoteRequested"] {
		t.Fatalf("remote request panel: shown=%v text=%q", c.remoteShown, ncTestText(c.remoteText))
	}
	c.answer(true)
	c.answer(false) // second click: ignored
	if c.remoteShown || !strings.Contains(ncTestText(c.history), i18n["remoteGranted"]) {
		t.Fatalf("after Allow: shown=%v history=%q", c.remoteShown, ncTestText(c.history))
	}

	// Typing in the input (EN_CHANGE -> typing event), then Send.
	ncSetWindowTextW.Call(c.input, uintptr(unsafe.Pointer(ncUTF16("  salut  "))))
	c.submit()
	if got := ncTestText(c.input); got != "" {
		t.Fatalf("input not cleared: %q", got)
	}
	if h := ncTestText(c.history); !strings.Contains(h, i18n["you"]+": ") || !strings.Contains(h, "salut") {
		t.Fatalf("own message not in the history: %q", h)
	}

	// Closed by the user (X / Alt+F4): user_closed, window destroyed.
	ncSendMessageW.Call(c.hwnd, ncWmClose, 0, 0)
	if isWin, _, _ := ncUser32.NewProc("IsWindow").Call(c.hwnd); isWin != 0 {
		t.Fatal("window still alive after WM_CLOSE")
	}
	want := "allow_remote|typing|user_message:  salut|user_closed"
	if ev.list() != want {
		t.Fatalf("events:\n got %q\nwant %q", ev.list(), want)
	}
}

// Closed by the service: no user_closed.
func TestNativeChatWindowServiceClose(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer ncTestDrain()

	ev := &sentEvents{}
	c, err := newNativeChat("Op", "user", chatStrings(true), ev.send)
	if err != nil {
		t.Skipf("no window: %v", err)
	}
	if !chatHelperDispatch(c, chatPipeStop, nil) {
		t.Fatal("stop must end the chat")
	}
	// The close is posted: run the loop until the window is gone.
	c.run()
	if ev.list() != "" {
		t.Fatalf("events after a service close: %q", ev.list())
	}
}
