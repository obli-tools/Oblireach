//go:build windows

package main

// The NATIVE chat window: plain Win32 controls (a RichEdit history from
// msftedit.dll, an edit box, Send, and Allow / Deny for a remote-control
// request), for the machines where the WebView2 page cannot serve the chat:
// Windows 7 SP1 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 (WebView2 Runtime
// frozen at 109, no more security fixes), a runtime missing, blocked by
// policy, or one that does not start. Chosen by the service (--chat-ui=native,
// chat.go) or by the helper itself when WebView2 fails to start
// (runChatHelperMode).
//
// Same rules as the WebView2 window:
//   - the helper runs AS THE SIGNED-IN USER (chat_spawn_windows.go), never
//     SYSTEM: the window gives the user nothing the user does not already
//     have;
//   - same pipe protocol and events (goChatSend: user_message, typing,
//     allow_remote / deny_remote, user_closed, ready; chatHelperUnavailable
//     before it exits when the window cannot be created);
//   - operator text is inserted as PLAIN text (EM_REPLACESEL, never RTF:
//     no OLE object, no link, no embedded content), control characters
//     removed; no URL detection, no drag and drop, no file opening;
//   - texts from chatStrings (English / French, like the page);
//   - shown without taking the focus (bottom-right, topmost), like the page.

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

var (
	ncUser32 = syscall.NewLazyDLL("user32.dll")
	ncGdi32  = syscall.NewLazyDLL("gdi32.dll")

	ncCreateWindowExW       = ncUser32.NewProc("CreateWindowExW")
	ncRegisterClassExW      = ncUser32.NewProc("RegisterClassExW")
	ncDefWindowProcW        = ncUser32.NewProc("DefWindowProcW")
	ncDestroyWindow         = ncUser32.NewProc("DestroyWindow")
	ncShowWindow            = ncUser32.NewProc("ShowWindow")
	ncSetWindowPos          = ncUser32.NewProc("SetWindowPos")
	ncMoveWindow            = ncUser32.NewProc("MoveWindow")
	ncSendMessageW          = ncUser32.NewProc("SendMessageW")
	ncPostMessageW          = ncUser32.NewProc("PostMessageW")
	ncPostQuitMessage       = ncUser32.NewProc("PostQuitMessage")
	ncGetMessageW           = ncUser32.NewProc("GetMessageW")
	ncIsDialogMessageW      = ncUser32.NewProc("IsDialogMessageW")
	ncTranslateMessage      = ncUser32.NewProc("TranslateMessage")
	ncDispatchMessageW      = ncUser32.NewProc("DispatchMessageW")
	ncGetClientRect         = ncUser32.NewProc("GetClientRect")
	ncSetWindowTextW        = ncUser32.NewProc("SetWindowTextW")
	ncGetWindowTextW        = ncUser32.NewProc("GetWindowTextW")
	ncGetWindowTextLengthW  = ncUser32.NewProc("GetWindowTextLengthW")
	ncSetFocus              = ncUser32.NewProc("SetFocus")
	ncIsIconic              = ncUser32.NewProc("IsIconic")
	ncIsWindowVisible       = ncUser32.NewProc("IsWindowVisible")
	ncFlashWindowEx         = ncUser32.NewProc("FlashWindowEx")
	ncMessageBeep           = ncUser32.NewProc("MessageBeep")
	ncSetTimer              = ncUser32.NewProc("SetTimer")
	ncKillTimer             = ncUser32.NewProc("KillTimer")
	ncLoadCursorW           = ncUser32.NewProc("LoadCursorW")
	ncGetSysColorBrush      = ncUser32.NewProc("GetSysColorBrush")
	ncSystemParametersInfoW = ncUser32.NewProc("SystemParametersInfoW")
	ncEnableWindow          = ncUser32.NewProc("EnableWindow")
	ncCreateFontIndirectW   = ncGdi32.NewProc("CreateFontIndirectW")
	ncDeleteObject          = ncGdi32.NewProc("DeleteObject")
	ncGetModuleHandleW      = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	ncRtlMoveMemory         = syscall.NewLazyDLL("kernel32.dll").NewProc("RtlMoveMemory")
)

const (
	ncWsOverlapped   = 0x00000000
	ncWsCaption      = 0x00C00000
	ncWsSysMenu      = 0x00080000
	ncWsThickFrame   = 0x00040000
	ncWsMinimizeBox  = 0x00020000
	ncWsChild        = 0x40000000
	ncWsVisible      = 0x10000000
	ncWsVScroll      = 0x00200000
	ncWsTabStop      = 0x00010000
	ncWsClipChildren = 0x02000000
	ncWsExTopmost    = 0x00000008
	ncWsExClientEdge = 0x00000200
	ncWsExAppWindow  = 0x00040000

	ncEsMultiline   = 0x0004
	ncEsAutoVScroll = 0x0040
	ncEsAutoHScroll = 0x0080
	ncEsReadOnly    = 0x0800
	ncEsNoHideSel   = 0x0100
	ncBsPushButton  = 0x0000
	ncBsDefPushBtn  = 0x0001
	ncSsLeft        = 0x0000
	ncSsNoPrefix    = 0x0080

	ncWmDestroy         = 0x0002
	ncWmSize            = 0x0005
	ncWmSetFocus        = 0x0007
	ncWmClose           = 0x0010
	ncWmSetFont         = 0x0030
	ncWmGetMinMaxInfo   = 0x0024
	ncWmCommand         = 0x0111
	ncWmTimer           = 0x0113
	ncWmVScroll         = 0x0115
	ncWmApp             = 0x8000
	ncWmDispatch        = ncWmApp + 0x51 // run the queued UI-thread work
	ncWmServiceClose    = ncWmApp + 0x52 // the service ended the chat: close without user_closed
	ncDmGetDefID        = 0x0400         // WM_USER + 0
	ncDcHasDefID        = 0x534B
	ncEnChange          = 0x0300
	ncBnClicked         = 0
	ncSbBottom          = 7
	ncEmSetSel          = 0x00B1
	ncEmReplaceSel      = 0x00C2
	ncEmScrollCaret     = 0x00B7
	ncEmLimitText       = 0x00C5
	ncEmSetReadOnly     = 0x00CF
	ncEmExLimitText     = 0x0400 + 53
	ncEmExSetSel        = 0x0400 + 55
	ncEmSetCharFormat   = 0x0400 + 68
	ncEmSetBkgndColor   = 0x0400 + 67
	ncEmGetTextLengthEx = 0x0400 + 95
	ncEmAutoURLDetect   = 0x0400 + 91
	ncEmSetEventMask    = 0x0400 + 69
	ncScfSelection      = 0x0001
	ncCfmBold           = 0x00000001
	ncCfmColor          = 0x40000000
	ncCfeBold           = 0x00000001
	ncCfeAutoColor      = 0x40000000
	ncGtlPrecise        = 2
	ncGtlNumChars       = 8

	ncSwHide              = 0
	ncSwShowNoActivate    = 4
	ncSwpNoActivate       = 0x0010
	ncSwpNoSize           = 0x0001
	ncSwpNoMove           = 0x0002
	ncSwpShowWindow       = 0x0040
	ncHwndTopmost         = ^uintptr(0)
	ncColorBtnFace        = 15
	ncIdcArrow            = 32512
	ncSpiNonClientMetrics = 0x0029
	ncFlashwTray          = 0x00000002
	ncFlashwTimerNoFG     = 0x0000000C
	ncMbOK                = 0x00000000

	ncIDHistory = 101
	ncIDInput   = 102
	ncIDSend    = 103
	ncIDRemote  = 104
	ncIDAllow   = 105
	ncIDDeny    = 106
	ncIDTyping  = 107
	ncIDOK      = 1
	ncIDCancel  = 2

	ncTimerTyping = 1

	ncWinW = 380
	ncWinH = 480
)

type ncLogFont struct {
	Height, Width, Escapement, Orientation, Weight       int32
	Italic, Underline, StrikeOut, CharSet                byte
	OutPrecision, ClipPrecision, Quality, PitchAndFamily byte
	FaceName                                             [32]uint16
}

type ncNonClientMetrics struct {
	CbSize                          uint32
	BorderWidth                     int32
	ScrollWidth, ScrollHeight       int32
	CaptionWidth, CaptionHeight     int32
	CaptionFont                     ncLogFont
	SmCaptionWidth, SmCaptionHeight int32
	SmCaptionFont                   ncLogFont
	MenuWidth, MenuHeight           int32
	MenuFont, StatusFont            ncLogFont
	MessageFont                     ncLogFont
	PaddedBorderWidth               int32 // Vista+
}

type ncCharFormat2 struct {
	CbSize          uint32
	DwMask          uint32
	DwEffects       uint32
	YHeight         int32
	YOffset         int32
	CrTextColor     uint32
	BCharSet        byte
	BPitchAndFamily byte
	SzFaceName      [32]uint16
	WWeight         uint16
	SSpacing        int16
	CrBackColor     uint32
	Lcid            uint32
	DwReserved      uint32
	SStyle          int16
	WKerning        uint16
	BUnderlineType  byte
	BAnimation      byte
	BRevAuthor      byte
	BReserved1      byte
}

type ncGetTextLengthEx struct {
	Flags    uint32
	Codepage uint32
}

type ncCharRange struct{ Min, Max int32 }

type ncFlashInfo struct {
	CbSize  uint32
	Hwnd    uintptr
	Flags   uint32
	Count   uint32
	Timeout uint32
}

type ncMinMaxInfo struct {
	Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize chatPoint
}

// nativeChat is one native chat window (one per helper process).
type nativeChat struct {
	hwnd                                                  uintptr
	history, input, send, remoteText, allow, deny, typing uintptr
	richEdit                                              bool
	font                                                  uintptr

	operatorName, userName string
	i18n                   map[string]string
	actions                *chatUserActions
	remoteShown            bool
	typingShown            bool
	closing                bool
	clearing               bool // the input is being emptied by submit (not typing)

	mu    sync.Mutex
	queue []func()
}

var (
	ncClassOnce sync.Once
	ncClassName *uint16
	ncClassErr  error
	ncWindowsMu sync.Mutex
	ncWindows   = map[uintptr]*nativeChat{}
	ncCreating  *nativeChat // WM_* sent during CreateWindowExW, before the map has the handle
)

func ncRegisterClass() error {
	ncClassOnce.Do(func() {
		name, _ := syscall.UTF16PtrFromString("OblianceNativeChat")
		hinst, _, _ := ncGetModuleHandleW.Call(0)
		cursor, _, _ := ncLoadCursorW.Call(0, ncIdcArrow)
		brush, _, _ := ncGetSysColorBrush.Call(ncColorBtnFace)
		wc := chatWndClassEx{
			lpfnWndProc:   syscall.NewCallback(ncWndProc),
			hInstance:     hinst,
			hCursor:       cursor,
			hbrBackground: brush,
			lpszClassName: name,
		}
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		if atom, _, err := ncRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
			ncClassErr = fmt.Errorf("RegisterClassExW: %v", err)
			return
		}
		ncClassName = name
	})
	return ncClassErr
}

// ncLoadRichEdit loads the RichEdit 4.1 control (msftedit.dll, Windows XP
// SP1 and later) from System32: "RICHEDIT50W". Falls back to RichEdit 2.0
// (riched20.dll), then to a plain multi-line EDIT.
func ncLoadRichEdit() (class string, rich bool) {
	for _, c := range []struct{ dll, class string }{{"msftedit.dll", "RICHEDIT50W"}, {"riched20.dll", "RichEdit20W"}} {
		if d := syscall.NewLazyDLL(systemDir() + `\` + c.dll); d.Load() == nil {
			return c.class, true
		}
	}
	return "EDIT", false
}

func ncWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	ncWindowsMu.Lock()
	c := ncWindows[hwnd]
	if c == nil {
		c = ncCreating
	}
	ncWindowsMu.Unlock()
	if c != nil {
		if r, handled := c.handle(hwnd, uint32(msg), wp, lp); handled {
			return r
		}
	}
	r, _, _ := ncDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func ncUTF16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString(strings.ReplaceAll(s, "\x00", ""))
	}
	return p
}

func (c *nativeChat) child(class, text string, style, exStyle uintptr, id int) uintptr {
	hinst, _, _ := ncGetModuleHandleW.Call(0)
	h, _, _ := ncCreateWindowExW.Call(exStyle,
		uintptr(unsafe.Pointer(ncUTF16(class))), uintptr(unsafe.Pointer(ncUTF16(text))),
		ncWsChild|ncWsVisible|style, 0, 0, 10, 10, c.hwnd, uintptr(id), hinst, 0)
	if h != 0 && c.font != 0 {
		ncSendMessageW.Call(h, ncWmSetFont, c.font, 0)
	}
	return h
}

// ncMessageFont: the system message font (as dialogs use; 0 = the
// controls' default font).
func ncMessageFont() uintptr {
	var m ncNonClientMetrics
	m.CbSize = uint32(unsafe.Sizeof(m))
	if r, _, _ := ncSystemParametersInfoW.Call(ncSpiNonClientMetrics, uintptr(m.CbSize), uintptr(unsafe.Pointer(&m)), 0); r == 0 {
		return 0
	}
	lf := m.MessageFont
	f, _, _ := ncCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&lf)))
	return f
}

// newNativeChat creates the window (hidden). The caller then calls show().
func newNativeChat(operatorName, userName string, i18n map[string]string, send func(action, text string)) (*nativeChat, error) {
	if err := ncRegisterClass(); err != nil {
		return nil, err
	}
	c := &nativeChat{operatorName: operatorName, userName: userName, i18n: i18n, actions: newChatUserActions(send)}
	c.font = ncMessageFont()
	r := chatBottomRight(chatPrimaryWorkArea(), ncWinW, ncWinH, chatWinMargin)
	title := strings.TrimSpace(i18n["chattingWith"] + " " + operatorName)
	hinst, _, _ := ncGetModuleHandleW.Call(0)
	ncWindowsMu.Lock()
	ncCreating = c
	ncWindowsMu.Unlock()
	hwnd, _, err := ncCreateWindowExW.Call(ncWsExTopmost|ncWsExAppWindow,
		uintptr(unsafe.Pointer(ncClassName)), uintptr(unsafe.Pointer(ncUTF16(title))),
		ncWsOverlapped|ncWsCaption|ncWsSysMenu|ncWsThickFrame|ncWsMinimizeBox|ncWsClipChildren,
		uintptr(r.Left), uintptr(r.Top), uintptr(r.width()), uintptr(r.height()),
		0, 0, hinst, 0)
	ncWindowsMu.Lock()
	ncCreating = nil
	if hwnd != 0 {
		ncWindows[hwnd] = c
	}
	ncWindowsMu.Unlock()
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowExW: %v", err)
	}
	c.hwnd = hwnd

	reClass, rich := ncLoadRichEdit()
	c.richEdit = rich
	c.history = c.child(reClass, "", ncWsVScroll|ncWsTabStop|ncEsMultiline|ncEsReadOnly|ncEsAutoVScroll|ncEsNoHideSel, ncWsExClientEdge, ncIDHistory)
	if c.history == 0 {
		ncDestroyWindow.Call(hwnd)
		return nil, fmt.Errorf("history control (%s) could not be created", reClass)
	}
	if rich {
		ncSendMessageW.Call(c.history, ncEmAutoURLDetect, 0, 0) // no clickable links
		ncSendMessageW.Call(c.history, ncEmSetEventMask, 0, 0)
		ncSendMessageW.Call(c.history, ncEmExLimitText, 0, 4<<20)
	}
	c.typing = c.child("STATIC", "", ncSsLeft|ncSsNoPrefix, 0, ncIDTyping)
	ncShowWindow.Call(c.typing, ncSwHide)
	c.remoteText = c.child("STATIC", i18n["remoteRequested"], ncSsLeft|ncSsNoPrefix, 0, ncIDRemote)
	c.allow = c.child("BUTTON", i18n["allow"], ncBsPushButton|ncWsTabStop, 0, ncIDAllow)
	c.deny = c.child("BUTTON", i18n["deny"], ncBsPushButton|ncWsTabStop, 0, ncIDDeny)
	for _, h := range []uintptr{c.remoteText, c.allow, c.deny} {
		ncShowWindow.Call(h, ncSwHide)
	}
	c.input = c.child("EDIT", "", ncWsTabStop|ncEsAutoHScroll, ncWsExClientEdge, ncIDInput)
	ncSendMessageW.Call(c.input, ncEmLimitText, chatNativeMaxInput, 0)
	c.send = c.child("BUTTON", i18n["send"], ncBsDefPushBtn|ncWsTabStop, 0, ncIDSend)
	if c.input == 0 || c.send == 0 {
		ncDestroyWindow.Call(hwnd)
		return nil, fmt.Errorf("input controls could not be created")
	}
	c.layout()
	return c, nil
}

// layout places the controls in the client area (history on top, then the
// typing line, the remote-control request when shown, the input row).
func (c *nativeChat) layout() {
	var rc chatRect
	ncGetClientRect.Call(c.hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := rc.width(), rc.height()
	const m, rowH, btnW, gap = 8, 26, 84, 6
	y := h - m - rowH
	move := func(hw uintptr, x, y, cw, ch int32) {
		if cw < 1 {
			cw = 1
		}
		if ch < 1 {
			ch = 1
		}
		ncMoveWindow.Call(hw, uintptr(x), uintptr(y), uintptr(cw), uintptr(ch), 1)
	}
	move(c.input, m, y, w-3*m/2-btnW-gap/2, rowH)
	move(c.send, w-m-btnW, y, btnW, rowH)
	if c.remoteShown {
		y -= rowH + gap
		move(c.allow, w-m-2*btnW-gap, y, btnW, rowH)
		move(c.deny, w-m-btnW, y, btnW, rowH)
		y -= 2*rowH - 8
		move(c.remoteText, m, y, w-2*m, 2*rowH-12)
	}
	if c.typingShown {
		y -= 20
		move(c.typing, m, y, w-2*m, 18)
	}
	move(c.history, m, m, w-2*m, y-gap-m)
}

// show reveals the window in place without taking the focus from what the
// user is doing, then tells the service the chat is up.
func (c *nativeChat) show() {
	r := chatBottomRight(chatPrimaryWorkArea(), ncWinW, ncWinH, chatWinMargin)
	ncSetWindowPos.Call(c.hwnd, ncHwndTopmost, uintptr(r.Left), uintptr(r.Top), uintptr(r.width()), uintptr(r.height()),
		ncSwpNoActivate|ncSwpShowWindow)
	ncShowWindow.Call(c.hwnd, ncSwShowNoActivate)
	goChatSend("ready", "")
	log.Printf("chat-helper: native chat window shown at %d,%d (RichEdit=%v)", r.Left, r.Top, c.richEdit)
}

// append adds "[15:04] who: " (bold) + text to the history, at the end.
func (c *nativeChat) append(who, text string) {
	head, body := chatNativeHistoryLine(time.Now(), who, text)
	c.appendRaw(head, true)
	c.appendRaw(body+"\r\n", false)
	ncSendMessageW.Call(c.history, ncWmVScroll, ncSbBottom, 0)
}

func (c *nativeChat) appendRaw(s string, bold bool) {
	if c.richEdit {
		gtl := ncGetTextLengthEx{Flags: ncGtlPrecise | ncGtlNumChars, Codepage: 1200}
		n, _, _ := ncSendMessageW.Call(c.history, ncEmGetTextLengthEx, uintptr(unsafe.Pointer(&gtl)), 0)
		cr := ncCharRange{int32(n), int32(n)}
		ncSendMessageW.Call(c.history, ncEmExSetSel, 0, uintptr(unsafe.Pointer(&cr)))
		cf := ncCharFormat2{DwMask: ncCfmBold}
		cf.CbSize = uint32(unsafe.Sizeof(cf))
		if bold {
			cf.DwEffects = ncCfeBold
		}
		ncSendMessageW.Call(c.history, ncEmSetCharFormat, ncScfSelection, uintptr(unsafe.Pointer(&cf)))
	} else {
		n, _, _ := ncGetWindowTextLengthW.Call(c.history)
		ncSendMessageW.Call(c.history, ncEmSetSel, n, n)
	}
	// Read-only for the user only: lifted around the insertion (some
	// RichEdit versions refuse EM_REPLACESEL on a read-only control).
	ncSendMessageW.Call(c.history, ncEmSetReadOnly, 0, 0)
	// Plain text only: never a string RichEdit would parse as RTF.
	ncSendMessageW.Call(c.history, ncEmReplaceSel, 0, uintptr(unsafe.Pointer(ncUTF16(chatNativeNoRTF(s)))))
	ncSendMessageW.Call(c.history, ncEmSetReadOnly, 1, 0)
	if !c.richEdit {
		ncSendMessageW.Call(c.history, ncEmScrollCaret, 0, 0)
	}
}

// attention: restore a minimized window (without activation) and flash its
// taskbar button, with a beep — what the page does with its sound.
func (c *nativeChat) attention() {
	if vis, _, _ := ncIsWindowVisible.Call(c.hwnd); vis == 0 {
		return // not shown yet (or never: tests)
	}
	if iconic, _, _ := ncIsIconic.Call(c.hwnd); iconic != 0 {
		ncShowWindow.Call(c.hwnd, ncSwShowNoActivate)
	}
	fi := ncFlashInfo{Hwnd: c.hwnd, Flags: ncFlashwTray | ncFlashwTimerNoFG, Count: 3}
	fi.CbSize = uint32(unsafe.Sizeof(fi))
	ncFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	ncMessageBeep.Call(ncMbOK)
}

func (c *nativeChat) setTyping(on bool) {
	if on == c.typingShown {
		return
	}
	c.typingShown = on
	if on {
		ncSetWindowTextW.Call(c.typing, uintptr(unsafe.Pointer(ncUTF16(strings.ReplaceAll(c.i18n["operatorTyping"], "{name}", c.operatorName)))))
		ncShowWindow.Call(c.typing, ncSwShowNoActivate)
	} else {
		ncShowWindow.Call(c.typing, ncSwHide)
	}
	c.layout()
}

func (c *nativeChat) setRemote(on bool, text string) {
	c.remoteShown = on
	if on {
		ncSetWindowTextW.Call(c.remoteText, uintptr(unsafe.Pointer(ncUTF16(text))))
	}
	for _, h := range []uintptr{c.remoteText, c.allow, c.deny} {
		if on {
			ncShowWindow.Call(h, ncSwShowNoActivate)
			ncEnableWindow.Call(h, 1)
		} else {
			ncShowWindow.Call(h, ncSwHide)
		}
	}
	c.layout()
}

// ── chatHelperUI (service -> window), called on the UI thread ────────────────

func (c *nativeChat) operatorMessage(text string) {
	c.setTyping(false)
	c.append(c.operatorName, text)
	c.attention()
}

func (c *nativeChat) operatorTyping() {
	c.setTyping(true)
	ncSetTimer.Call(c.hwnd, ncTimerTyping, uintptr(chatNativeTypingShow/time.Millisecond), 0)
}

func (c *nativeChat) remoteRequest(message string) {
	text := chatNativeCleanMessage(message)
	if strings.TrimSpace(text) == "" {
		text = c.i18n["remoteRequested"]
	}
	c.actions.remoteRequested()
	c.setRemote(true, text)
	c.attention()
}

func (c *nativeChat) fileReceived(name string) {
	c.append(c.operatorName, chatNativeStripEmoji(c.i18n["fileReceived"])+" "+chatCleanText(name))
}

func (c *nativeChat) close() {
	c.actions.serviceClosed()
	ncPostMessageW.Call(c.hwnd, ncWmServiceClose, 0, 0)
}

// ── UI thread plumbing ───────────────────────────────────────────────────────

// dispatch runs f on the UI thread (from the pipe reader goroutine).
func (c *nativeChat) dispatch(f func()) {
	c.mu.Lock()
	c.queue = append(c.queue, f)
	c.mu.Unlock()
	ncPostMessageW.Call(c.hwnd, ncWmDispatch, 0, 0)
}

func (c *nativeChat) runQueue() {
	c.mu.Lock()
	q := c.queue
	c.queue = nil
	c.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// uiProxy forwards the service's messages to the UI thread.
type ncUIProxy struct{ c *nativeChat }

func (p ncUIProxy) operatorMessage(text string) { p.c.dispatch(func() { p.c.operatorMessage(text) }) }
func (p ncUIProxy) operatorTyping()             { p.c.dispatch(p.c.operatorTyping) }
func (p ncUIProxy) remoteRequest(m string)      { p.c.dispatch(func() { p.c.remoteRequest(m) }) }
func (p ncUIProxy) fileReceived(n string)       { p.c.dispatch(func() { p.c.fileReceived(n) }) }
func (p ncUIProxy) close()                      { p.c.close() }

func (c *nativeChat) inputText() string {
	n, _, _ := ncGetWindowTextLengthW.Call(c.input)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	ncGetWindowTextW.Call(c.input, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func (c *nativeChat) submit() {
	text, ok := c.actions.submit(c.inputText())
	if !ok {
		return
	}
	c.clearing = true
	ncSetWindowTextW.Call(c.input, uintptr(unsafe.Pointer(ncUTF16(""))))
	c.clearing = false
	c.append(c.i18n["you"], text)
	ncSetFocus.Call(c.input)
}

func (c *nativeChat) answer(allow bool) {
	if !c.actions.answerRemote(allow) {
		return
	}
	c.setRemote(false, "")
	key := "remoteDenied"
	if allow {
		key = "remoteGranted"
	}
	c.appendRaw(c.i18n[key]+"\r\n", true)
	ncSendMessageW.Call(c.history, ncWmVScroll, ncSbBottom, 0)
}

func (c *nativeChat) handle(hwnd uintptr, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case ncWmSize:
		if c.hwnd != 0 && c.input != 0 {
			c.layout()
		}
		return 0, true
	case ncWmGetMinMaxInfo:
		// MINMAXINFO.ptMinTrackSize (copied into the system's structure at
		// lp: no Go pointer made from a uintptr).
		minTrack := chatPoint{300, 320}
		ncRtlMoveMemory.Call(lp+unsafe.Offsetof(ncMinMaxInfo{}.MinTrackSize), uintptr(unsafe.Pointer(&minTrack)), unsafe.Sizeof(minTrack))
		return 0, true
	case ncWmSetFocus:
		if c.input != 0 {
			ncSetFocus.Call(c.input)
		}
		return 0, true
	case ncDmGetDefID:
		// IsDialogMessage asks the default button: Enter in the input sends.
		return uintptr(ncIDSend) | uintptr(ncDcHasDefID)<<16, true
	case ncWmCommand:
		id, code := int(wp&0xFFFF), int((wp>>16)&0xFFFF)
		switch {
		case (id == ncIDSend || id == ncIDOK) && code == ncBnClicked:
			c.submit()
		case id == ncIDInput && code == ncEnChange:
			if !c.clearing {
				c.actions.typed()
			}
		case id == ncIDAllow && code == ncBnClicked:
			c.answer(true)
		case id == ncIDDeny && code == ncBnClicked:
			c.answer(false)
		case id == ncIDCancel:
			// Escape: never closes the chat by accident.
		}
		return 0, true
	case ncWmTimer:
		if wp == ncTimerTyping {
			ncKillTimer.Call(hwnd, ncTimerTyping)
			c.setTyping(false)
		}
		return 0, true
	case ncWmDispatch:
		c.runQueue()
		return 0, true
	case ncWmClose:
		// The user closed the window (X, Alt+F4, taskbar).
		c.closing = true
		c.actions.userClosed()
		ncDestroyWindow.Call(hwnd)
		return 0, true
	case ncWmServiceClose:
		c.closing = true
		ncDestroyWindow.Call(hwnd)
		return 0, true
	case ncWmDestroy:
		ncWindowsMu.Lock()
		delete(ncWindows, hwnd)
		ncWindowsMu.Unlock()
		if c.font != 0 {
			ncDeleteObject.Call(c.font)
		}
		ncPostQuitMessage.Call(0)
		return 0, true
	}
	return 0, false
}

// run is the window's message loop (IsDialogMessage: Tab between controls,
// Enter = Send). Returns when the window is destroyed.
func (c *nativeChat) run() {
	var m chatMsg
	for {
		r, _, _ := ncGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if d, _, _ := ncIsDialogMessageW.Call(c.hwnd, uintptr(unsafe.Pointer(&m))); d != 0 {
			continue
		}
		ncTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		ncDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// runNativeChat shows the native window for this helper (the pipe is already
// connected and the init message read) until the user or the service closes
// it. Called on the helper's locked UI thread.
func runNativeChat(conn io.Reader, operatorName, userName string, i18n map[string]string) {
	c, err := newNativeChat(operatorName, userName, i18n, goChatSend)
	if err != nil {
		log.Printf("chat-helper: native chat window could not be created: %v", err)
		chatHelperUnavailable("native_failed", "the native chat window could not be created: "+err.Error())
		return
	}
	ui := ncUIProxy{c}
	go func() {
		for {
			msgType, payload, err := chatPipeRecvMax(conn, chatHelperMaxPayload)
			if err != nil {
				log.Printf("chat-helper: pipe closed: %v", err)
				ui.close()
				return
			}
			if chatHelperDispatch(ui, msgType, payload) {
				return
			}
		}
	}()
	c.show()
	c.run()
	log.Printf("chat-helper: native chat window closed")
}

// platformChatNativeAvailable: the native window exists on every Windows.
func platformChatNativeAvailable() bool { return true }

// platformChatPreferNative: Windows 7 SP1 / 8.1 / Server 2008 R2 / 2012 /
// 2012 R2 always get the native window: the WebView2 Runtime stopped at 109
// there and receives no security fixes any more.
func platformChatPreferNative() bool { return engine.WindowsVersion().WebView2Unsupported() }
