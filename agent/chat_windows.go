//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
)

// ── End-user chat window ─────────────────────────────────────────────────────
//
// The helper owns its host window. It is created with CreateWindowExW already
// in its final form (WS_POPUP, tool window, topmost, dark class brush,
// rounded region, final size, bottom-right of the primary monitor's work
// area) and WITHOUT WS_VISIBLE. WebView2 is then embedded into it (pkg/edge,
// the same engine go-webview2's WebView uses, but with our window: the
// WebView helper always creates and SHOWS its own framed window first, which
// is what made the chat appear framed / at the left of the screen before
// jumping to the bottom-right). The window is shown exactly once, in place,
// when the page has loaded, or by a fallback timer: it is never restyled or
// moved while visible before that, and never stays hidden forever.

var (
	chatUser32   = syscall.NewLazyDLL("user32.dll")
	chatGdi32    = syscall.NewLazyDLL("gdi32.dll")
	chatKernel32 = syscall.NewLazyDLL("kernel32.dll")

	chatProcRegisterClassExW  = chatUser32.NewProc("RegisterClassExW")
	chatProcCreateWindowExW   = chatUser32.NewProc("CreateWindowExW")
	chatProcDefWindowProcW    = chatUser32.NewProc("DefWindowProcW")
	chatProcDestroyWindow     = chatUser32.NewProc("DestroyWindow")
	chatProcShowWindow        = chatUser32.NewProc("ShowWindow")
	chatProcIsWindowVisible   = chatUser32.NewProc("IsWindowVisible")
	chatProcSetWindowPos      = chatUser32.NewProc("SetWindowPos")
	chatProcSetWindowRgn      = chatUser32.NewProc("SetWindowRgn")
	chatProcGetWindowLongPtrW = chatUser32.NewProc("GetWindowLongPtrW")
	chatProcSetWindowLongPtrW = chatUser32.NewProc("SetWindowLongPtrW")
	chatProcSetLayeredAttr    = chatUser32.NewProc("SetLayeredWindowAttributes")
	chatProcSetTimer          = chatUser32.NewProc("SetTimer")
	chatProcKillTimer         = chatUser32.NewProc("KillTimer")
	chatProcPostMessageW      = chatUser32.NewProc("PostMessageW")
	chatProcPostQuitMessage   = chatUser32.NewProc("PostQuitMessage")
	chatProcGetMessageW       = chatUser32.NewProc("GetMessageW")
	chatProcTranslateMessage  = chatUser32.NewProc("TranslateMessage")
	chatProcDispatchMessageW  = chatUser32.NewProc("DispatchMessageW")
	chatProcLoadCursorW       = chatUser32.NewProc("LoadCursorW")
	chatProcReleaseCapture    = chatUser32.NewProc("ReleaseCapture")
	chatProcGetCursorPos      = chatUser32.NewProc("GetCursorPos")
	chatProcGetAsyncKeyState  = chatUser32.NewProc("GetAsyncKeyState")
	chatProcGetSystemMetrics  = chatUser32.NewProc("GetSystemMetrics")
	chatProcSysParamsInfo     = chatUser32.NewProc("SystemParametersInfoW")
	chatProcMonitorFromPoint  = chatUser32.NewProc("MonitorFromPoint")
	chatProcMonitorFromWindow = chatUser32.NewProc("MonitorFromWindow")
	chatProcGetMonitorInfoW   = chatUser32.NewProc("GetMonitorInfoW")

	chatProcCreateRoundRgn = chatGdi32.NewProc("CreateRoundRectRgn")
	chatProcCreateBrush    = chatGdi32.NewProc("CreateSolidBrush")
	chatProcDeleteObject   = chatGdi32.NewProc("DeleteObject")

	chatProcGetModuleHandleW = chatKernel32.NewProc("GetModuleHandleW")
	chatProcGetUILang        = chatKernel32.NewProc("GetUserDefaultUILanguage")
)

const (
	chatWsPopup        = 0x80000000
	chatWsClipChildren = 0x02000000
	chatWsExTopmost    = 0x00000008
	chatWsExToolWindow = 0x00000080
	chatWsExLayered    = 0x00080000
	chatLwaAlpha       = 0x2

	chatWmDestroy       = 0x0002
	chatWmMove          = 0x0003
	chatWmSize          = 0x0005
	chatWmActivate      = 0x0006
	chatWmClose         = 0x0010
	chatWmNcLButtonDown = 0x00A1
	chatWmTimer         = 0x0113
	chatWmMoving        = 0x0216
	chatWmDispatch      = 0x8000 + 0x43 // WM_APP+n: run the queued UI-thread work

	chatWaInactive       = 0
	chatHtCaption        = 2
	chatSwShowNoActivate = 4
	chatSwShow           = 5
	chatSwpNoActivate    = 0x0010
	chatSwpShowWindow    = 0x0040

	chatGwlExStyle  = ^uintptr(19) // GWL_EXSTYLE = -20
	chatHwndTopmost = ^uintptr(0)  // HWND_TOPMOST = (HWND)-1

	chatMonitorDefaultToPrimary = 1
	chatMonitorDefaultToNearest = 2
	chatSpiGetWorkArea          = 0x0030
	chatSmCxScreen              = 0
	chatSmCyScreen              = 1
	chatSmSwapButton            = 23
	chatIdcArrow                = 32512

	chatVkLButton        = 0x01
	chatVkRButton        = 0x02
	chatVkControl        = 0x11
	chatVkF5             = 0x74
	chatVkBrowserRefresh = 0xA8

	chatDarkColorRef = 0x0017110D // #0d1117 as COLORREF 0x00BBGGRR

	chatTimerReveal        = 1
	chatTimerEmbedDeadline = 2
)

type chatWndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type chatPoint struct{ X, Y int32 }

type chatMsg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       chatPoint
	lPrivate uint32
}

type chatMonitorInfo struct {
	cbSize    uint32
	rcMonitor chatRect
	rcWork    chatRect
	dwFlags   uint32
}

func chatIsFrench() bool {
	ret, _, _ := chatProcGetUILang.Call()
	return (uint16(ret) & 0x3FF) == 0x0C
}

// ── Work areas ───────────────────────────────────────────────────────────────

func chatMonitorWork(mon uintptr) chatRect {
	if mon == 0 {
		return chatRect{}
	}
	mi := chatMonitorInfo{}
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	if ok, _, _ := chatProcGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return chatRect{}
	}
	return mi.rcWork
}

// chatPrimaryWorkArea: work area (taskbar excluded) of the primary monitor,
// where the chat first appears (as the agent's toasts). Falls back to the
// SPI work area, then to the screen size.
func chatPrimaryWorkArea() chatRect {
	mon, _, _ := chatProcMonitorFromPoint.Call(0, chatMonitorDefaultToPrimary) // POINT{0,0}
	var spi chatRect
	chatProcSysParamsInfo.Call(chatSpiGetWorkArea, 0, uintptr(unsafe.Pointer(&spi)), 0)
	cx, _, _ := chatProcGetSystemMetrics.Call(chatSmCxScreen)
	cy, _, _ := chatProcGetSystemMetrics.Call(chatSmCyScreen)
	return chatFirstUsable(chatMonitorWork(mon), spi, chatRect{0, 0, int32(cx), int32(cy)})
}

// ── Host window ──────────────────────────────────────────────────────────────

type chatHost struct {
	hwnd uintptr
	web  *edge.Chromium

	embedding bool // inside edge.Embed (it runs its own message loop)
	embedded  bool // WebView2 controller created
	pageReady bool // the chat page has loaded
	revealed  bool
	pendingJS []string // scripts waiting for the page

	workArea      func() chatRect // where the window first appears
	revealAfter   time.Duration
	embedDeadline time.Duration
	quitOnDestroy bool
	onCall        func(chatBridgeCall) // page -> Go calls (UI thread)

	mu    sync.Mutex
	queue []func() // work posted from other goroutines
}

func newChatHost() *chatHost {
	return &chatHost{
		workArea:      chatPrimaryWorkArea,
		revealAfter:   chatRevealFallback,
		embedDeadline: chatEmbedDeadline,
		quitOnDestroy: true,
	}
}

var (
	chatHostsMu sync.Mutex
	chatHosts   = map[uintptr]*chatHost{}

	chatClassOnce sync.Once
	chatClassName *uint16
	chatClassErr  error
)

func chatRegisterClass() error {
	chatClassOnce.Do(func() {
		name, _ := syscall.UTF16PtrFromString("OblianceChatHost")
		hinst, _, _ := chatProcGetModuleHandleW.Call(0)
		// Dark background brush: whatever shows before WebView2 paints is
		// the page colour, never white.
		brush, _, _ := chatProcCreateBrush.Call(chatDarkColorRef)
		cursor, _, _ := chatProcLoadCursorW.Call(0, chatIdcArrow)
		wc := chatWndClassEx{
			lpfnWndProc:   syscall.NewCallback(chatWndProc),
			hInstance:     hinst,
			hCursor:       cursor,
			hbrBackground: brush,
			lpszClassName: name,
		}
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		if atom, _, err := chatProcRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
			chatClassErr = fmt.Errorf("RegisterClassExW: %v", err)
			return
		}
		chatClassName = name
	})
	return chatClassErr
}

func chatWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	chatHostsMu.Lock()
	h := chatHosts[hwnd]
	chatHostsMu.Unlock()
	if h != nil {
		if r, handled := h.handle(uint32(msg), wp, lp); handled {
			return r
		}
	}
	r, _, _ := chatProcDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// create makes the host window: final style, size, place and shape, hidden.
func (h *chatHost) create(title string) error {
	if err := chatRegisterClass(); err != nil {
		return err
	}
	r := chatBottomRight(h.workArea(), chatWinW, chatWinH, chatWinMargin)
	titleW, _ := syscall.UTF16PtrFromString(title)
	hinst, _, _ := chatProcGetModuleHandleW.Call(0)
	hwnd, _, err := chatProcCreateWindowExW.Call(
		chatWsExToolWindow|chatWsExTopmost,
		uintptr(unsafe.Pointer(chatClassName)),
		uintptr(unsafe.Pointer(titleW)),
		chatWsPopup|chatWsClipChildren, // no WS_VISIBLE: shown by reveal() only
		uintptr(r.Left), uintptr(r.Top), uintptr(r.width()), uintptr(r.height()),
		0, 0, hinst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %v", err)
	}
	h.hwnd = hwnd
	chatHostsMu.Lock()
	chatHosts[hwnd] = h
	chatHostsMu.Unlock()
	h.setShape(chatWinW, chatWinH, chatWinRadius, false)
	return nil
}

// armTimers starts the reveal fallback (the window is never hidden forever)
// and the WebView2 start deadline. Call before embed(): edge.Embed pumps
// messages, so both timers also fire while WebView2 is starting.
func (h *chatHost) armTimers() {
	if ok, _, err := chatProcSetTimer.Call(h.hwnd, chatTimerReveal, uintptr(h.revealAfter/time.Millisecond), 0); ok == 0 {
		log.Printf("chat-helper: SetTimer failed (%v), showing the window now", err)
		h.reveal("no timer")
	}
	if h.embedDeadline > 0 {
		chatProcSetTimer.Call(h.hwnd, chatTimerEmbedDeadline, uintptr(h.embedDeadline/time.Millisecond), 0)
	}
}

// setShape clips the window to a rounded rectangle of w x hh.
func (h *chatHost) setShape(w, hh, radius int32, redraw bool) {
	rgn, _, _ := chatProcCreateRoundRgn.Call(0, 0, uintptr(w), uintptr(hh), uintptr(radius), uintptr(radius))
	if rgn == 0 {
		return
	}
	var redrawArg uintptr
	if redraw {
		redrawArg = 1
	}
	if ok, _, _ := chatProcSetWindowRgn.Call(h.hwnd, rgn, redrawArg); ok == 0 {
		chatProcDeleteObject.Call(rgn) // only owned by the system on success
	}
}

// applyOpacity makes the window 94 % opaque. A layered window is not drawn
// at all until its attributes are set, so if that fails the layered style
// is removed again: the window may lose its transparency, never its display.
func (h *chatHost) applyOpacity() {
	ex, _, _ := chatProcGetWindowLongPtrW.Call(h.hwnd, chatGwlExStyle)
	if ex&chatWsExLayered != 0 {
		return
	}
	chatProcSetWindowLongPtrW.Call(h.hwnd, chatGwlExStyle, ex|chatWsExLayered)
	if ok, _, err := chatProcSetLayeredAttr.Call(h.hwnd, 0, chatWinAlpha, chatLwaAlpha); ok == 0 {
		chatProcSetWindowLongPtrW.Call(h.hwnd, chatGwlExStyle, ex&^chatWsExLayered)
		log.Printf("chat-helper: SetLayeredWindowAttributes failed (%v), window left opaque", err)
	}
}

// reveal shows the window, once, at its final place, without taking the
// focus away from what the user is typing (they click it to answer).
func (h *chatHost) reveal(reason string) {
	if h.revealed || h.hwnd == 0 {
		return
	}
	h.revealed = true
	chatProcKillTimer.Call(h.hwnd, chatTimerReveal)
	h.applyOpacity()
	// Recomputed while still hidden (the taskbar may have moved meanwhile).
	r := chatBottomRight(h.workArea(), chatWinW, chatWinH, chatWinMargin)
	chatProcSetWindowPos.Call(h.hwnd, chatHwndTopmost,
		uintptr(r.Left), uintptr(r.Top), uintptr(r.width()), uintptr(r.height()),
		chatSwpNoActivate|chatSwpShowWindow)
	chatProcShowWindow.Call(h.hwnd, chatSwShowNoActivate)
	if vis, _, _ := chatProcIsWindowVisible.Call(h.hwnd); vis == 0 {
		chatProcShowWindow.Call(h.hwnd, chatSwShow)
	}
	if h.embedded {
		// Hidden by embed() while the window was: WebView2 only starts
		// drawing on this false -> true change (checked on a real desktop:
		// without it the window stays an empty dark box).
		_ = h.web.Show()
	}
	log.Printf("chat-helper: window shown (%s) at %d,%d", reason, r.Left, r.Top)
}

// currentWorkArea: work area of the monitor the window is on now (the user
// may have dragged it to another screen).
func (h *chatHost) currentWorkArea() chatRect {
	mon, _, _ := chatProcMonitorFromWindow.Call(h.hwnd, chatMonitorDefaultToNearest)
	return chatFirstUsable(chatMonitorWork(mon), h.workArea())
}

func (h *chatHost) place(r chatRect) {
	chatProcSetWindowPos.Call(h.hwnd, chatHwndTopmost,
		uintptr(r.Left), uintptr(r.Top), uintptr(r.width()), uintptr(r.height()),
		chatSwpNoActivate)
}

// toBubble shrinks the window to the round 56x56 bubble (bottom-right).
func (h *chatHost) toBubble() {
	h.setShape(chatBubbleSize, chatBubbleSize, chatBubbleSize, true)
	h.place(chatBottomRight(h.currentWorkArea(), chatBubbleSize, chatBubbleSize, chatBubbleMargin))
}

// toFull restores the full chat window (bottom-right).
func (h *chatHost) toFull() {
	h.setShape(chatWinW, chatWinH, chatWinRadius, true)
	h.place(chatBottomRight(h.currentWorkArea(), chatWinW, chatWinH, chatWinMargin))
}

// startDrag moves the window with the mouse (called on mousedown in the
// header): the system move loop, as for a title bar.
func (h *chatHost) startDrag() {
	vk := uintptr(chatVkLButton)
	if swapped, _, _ := chatProcGetSystemMetrics.Call(chatSmSwapButton); swapped != 0 {
		vk = chatVkRButton // GetAsyncKeyState reads the physical button
	}
	if st, _, _ := chatProcGetAsyncKeyState.Call(vk); st&0x8000 == 0 {
		return // already released: a click, not a drag
	}
	var pt chatPoint
	chatProcGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	chatProcReleaseCapture.Call()
	lp := uintptr(uint16(pt.X)) | uintptr(uint16(pt.Y))<<16
	chatProcPostMessageW.Call(h.hwnd, chatWmNcLButtonDown, chatHtCaption, lp)
}

// dispatch runs f on the UI thread (from any goroutine). Posted to the
// window, not the thread, so it also runs during the modal move loop.
func (h *chatHost) dispatch(f func()) {
	h.mu.Lock()
	h.queue = append(h.queue, f)
	h.mu.Unlock()
	chatProcPostMessageW.Call(h.hwnd, chatWmDispatch, 0, 0)
}

func (h *chatHost) runQueue() {
	h.mu.Lock()
	q := h.queue
	h.queue = nil
	h.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// close closes the window (from any goroutine); the helper then exits.
func (h *chatHost) close() {
	chatProcPostMessageW.Call(h.hwnd, chatWmClose, 0, 0)
}

// destroyQuietly destroys the window without ending the thread's message
// loop (used when WebView2 cannot start, before the fallback toast).
func (h *chatHost) destroyQuietly() {
	h.quitOnDestroy = false
	chatProcDestroyWindow.Call(h.hwnd)
}

func (h *chatHost) handle(msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case chatWmSize:
		if h.web != nil {
			h.web.Resize() // WebView2 fills the client area
		}
	case chatWmMove, chatWmMoving:
		if h.web != nil {
			_ = h.web.NotifyParentWindowPositionChanged()
		}
	case chatWmActivate:
		if wp&0xFFFF != chatWaInactive && h.embedded {
			h.web.Focus()
			return 0, true
		}
	case chatWmTimer:
		switch wp {
		case chatTimerReveal:
			h.reveal("fallback timer")
		case chatTimerEmbedDeadline:
			chatProcKillTimer.Call(h.hwnd, chatTimerEmbedDeadline)
			if !h.embedded {
				log.Printf("chat-helper: WebView2 still not ready after %v, giving up", h.embedDeadline)
				chatHelperUnavailable(chatUnavailWebView2Timeout,
					fmt.Sprintf("WebView2 did not start within %v", h.embedDeadline))
				os.Exit(1)
			}
		}
		return 0, true
	case chatWmDispatch:
		h.runQueue()
		return 0, true
	case chatWmClose:
		chatProcDestroyWindow.Call(h.hwnd)
		return 0, true
	case chatWmDestroy:
		chatHostsMu.Lock()
		delete(chatHosts, h.hwnd)
		chatHostsMu.Unlock()
		if h.embedding {
			// A WM_QUIT inside edge.Embed's own loop would make it crash.
			log.Printf("chat-helper: window closed while WebView2 was starting")
			os.Exit(0)
		}
		if h.quitOnDestroy {
			chatProcPostQuitMessage.Call(0)
		}
		return 0, true
	}
	return 0, false
}

// ── WebView2 ─────────────────────────────────────────────────────────────────

// embed creates the WebView2 controller inside the (still hidden) window.
func (h *chatHost) embed() bool {
	web := edge.NewChromium()
	// The WebView2 profile in the signed-in user's LOCAL profile (the helper
	// runs as that user: chat_spawn_windows.go), not go-webview2's default
	// under the roaming %APPDATA%.
	if dp := chatWebViewDataPath(); dp != "" {
		if err := os.MkdirAll(dp, 0o700); err == nil {
			web.DataPath = dp
		}
	}
	web.MessageCallback = h.onWebMessage
	web.NavigationCompletedCallback = func(*edge.ICoreWebView2, *edge.ICoreWebView2NavigationCompletedEventArgs) {
		h.onPageLoaded()
	}
	web.AcceleratorKeyCallback = chatSwallowReloadKey
	web.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)
	h.web = web
	h.embedding = true
	ok := web.Embed(h.hwnd)
	h.embedding = false
	if !ok {
		return false
	}
	h.embedded = true
	chatProcKillTimer.Call(h.hwnd, chatTimerEmbedDeadline)
	web.Resize()
	if !h.revealed {
		// A WebView2 created in a hidden window does not draw once the
		// window is shown; mark it hidden now, reveal() shows it again.
		// (Navigation and scripts still run meanwhile.)
		_ = web.Hide()
	}
	if c := web.GetController(); c != nil {
		if c2 := c.GetICoreWebView2Controller2(); c2 != nil {
			_ = c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: 0x0d, G: 0x11, B: 0x17})
		}
	}
	if s, err := web.GetSettings(); err == nil {
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutIsZoomControlEnabled(false)
		// No browser feature from the keyboard (print / Save as / find /
		// reload / zoom…): a chat window, not a browser. Text editing keys
		// (copy, paste, undo, select all) keep working.
		if !chatDisableBrowserKeys(s) {
			log.Printf("chat-helper: WebView2 browser shortcut keys left on (runtime without ICoreWebView2Settings3)")
		}
	} else {
		log.Printf("chat-helper: WebView2 settings: %v", err)
	}
	return true
}

// loadPage installs the Go bridge and loads the chat page.
func (h *chatHost) loadPage(page string) {
	h.web.Init(chatBridgeScript())
	h.web.NavigateToString(page)
}

func (h *chatHost) onWebMessage(raw string) {
	if c, ok := chatParseBridgeCall(raw); ok && h.onCall != nil {
		h.onCall(c)
	}
}

func (h *chatHost) onPageLoaded() {
	if !h.pageReady {
		h.pageReady = true
		goChatSend("ready", "")
		for _, js := range h.pendingJS {
			h.web.Eval(js)
		}
		h.pendingJS = nil
	}
	h.reveal("page loaded")
}

// evalJS runs js in the page (UI thread). Scripts sent before the page has
// loaded (e.g. the message that respawned the window) wait for it.
func (h *chatHost) evalJS(js string) {
	if !h.pageReady || h.web == nil {
		h.pendingJS = append(h.pendingJS, js)
		return
	}
	h.web.Eval(js)
}

// chatWebViewDataPath: the chat window's WebView2 profile folder,
// %LOCALAPPDATA%\Oblireach\ChatWebView2 of the user the helper runs as ("":
// go-webview2's default).
func chatWebViewDataPath() string {
	la := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if la == "" || !filepath.IsAbs(la) {
		return ""
	}
	return filepath.Join(la, "Oblireach", "ChatWebView2")
}

// IID_ICoreWebView2Settings3 (AreBrowserAcceleratorKeysEnabled).
var chatIIDSettings3 = edge.NewGUID("{fdb5ab74-af33-4854-84f0-0a631deb5eba}")

// chatDisableBrowserKeys turns the WebView2 browser shortcut keys off, when
// the runtime implements ICoreWebView2Settings3 (asked with QueryInterface:
// an older runtime's settings object has no such method slot).
func chatDisableBrowserKeys(s *edge.ICoreWebViewSettings) bool {
	s3 := chatQueryInterface(unsafe.Pointer(s), chatIIDSettings3)
	if s3 == nil {
		return false
	}
	defer chatComRelease(s3)
	settings3 := (*edge.ICoreWebViewSettings)(s3)
	if err := settings3.PutAreBrowserAcceleratorKeysEnabled(false); err != nil {
		return false
	}
	on, err := settings3.GetAreBrowserAcceleratorKeysEnabled()
	return err == nil && !on
}

// chatQueryInterface: IUnknown::QueryInterface (vtable slot 0) of a COM
// object; nil when it does not implement iid.
func chatQueryInterface(obj unsafe.Pointer, iid *edge.GUID) unsafe.Pointer {
	if obj == nil {
		return nil
	}
	vtbl := *(*unsafe.Pointer)(obj)
	qi := *(*uintptr)(vtbl)
	var out unsafe.Pointer
	hr, _, _ := syscall.SyscallN(qi, uintptr(obj), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if hr != 0 {
		return nil
	}
	return out
}

// chatComRelease: IUnknown::Release (vtable slot 2).
func chatComRelease(obj unsafe.Pointer) {
	if obj == nil {
		return
	}
	vtbl := *(*unsafe.Pointer)(obj)
	release := *(*uintptr)(unsafe.Add(vtbl, 2*unsafe.Sizeof(uintptr(0))))
	syscall.SyscallN(release, uintptr(obj))
}

// chatSwallowReloadKey keeps F5 / Ctrl+R from reloading the page (which
// would wipe the conversation).
func chatSwallowReloadKey(vk uint) bool {
	switch vk {
	case chatVkF5, chatVkBrowserRefresh:
		return true
	case 'R':
		st, _, _ := chatProcGetAsyncKeyState.Call(chatVkControl)
		return st&0x8000 != 0
	}
	return false
}

func chatRunMessageLoop() {
	var m chatMsg
	for {
		r, _, _ := chatProcGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			return
		}
		chatProcTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		chatProcDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// ── Helper process ───────────────────────────────────────────────────────────

var chatConn net.Conn
var chatConnMu sync.Mutex

//export goChatSend
func goChatSend(action, text string) {
	chatConnMu.Lock()
	conn := chatConn
	chatConnMu.Unlock()
	if conn == nil {
		return
	}

	var msg []byte
	switch action {
	case "user_message":
		msg, _ = json.Marshal(map[string]interface{}{
			"action": "user_message",
			"text":   text,
			"from":   getUserDisplayName(),
		})
		chatPipeSend(conn, chatPipeMsg, msg)
	case "user_closed":
		msg, _ = json.Marshal(map[string]string{"action": "user_closed"})
		chatPipeSend(conn, chatPipeEvent, msg)
	case "allow_remote":
		msg, _ = json.Marshal(map[string]interface{}{"action": "allow_remote", "allowed": true})
		chatPipeSend(conn, chatPipeEvent, msg)
	case "deny_remote":
		msg, _ = json.Marshal(map[string]interface{}{"action": "deny_remote", "allowed": false})
		chatPipeSend(conn, chatPipeEvent, msg)
	case "typing":
		msg, _ = json.Marshal(map[string]string{"action": "typing"})
		chatPipeSend(conn, chatPipeEvent, msg)
	case "ready":
		// Handshake: the chat page has loaded (the service reports a helper
		// that goes away before this as "chat could not open").
		msg, _ = json.Marshal(map[string]string{"action": "ready"})
		chatPipeSend(conn, chatPipeEvent, msg)
	}
}

// chatHelperUnavailable tells the service the chat cannot be shown (sent
// before the helper exits, or before its toast): reason
// webview2_missing | webview2_failed | webview2_timeout.
func chatHelperUnavailable(reason, detail string) {
	chatConnMu.Lock()
	conn := chatConn
	chatConnMu.Unlock()
	if conn == nil {
		return
	}
	msg, _ := json.Marshal(map[string]string{"action": "unavailable", "reason": reason, "detail": detail})
	chatPipeSend(conn, chatPipeEvent, msg)
}

// chatEmbedFailure: why WebView2 did not start in this helper.
func chatEmbedFailure() (reason, detail string) {
	v, err := webviewloader.GetInstalledVersion()
	switch {
	case err != nil:
		return chatUnavailWebView2Failed, "WebView2 could not start (" + err.Error() + ")"
	case strings.TrimSpace(v) == "":
		return chatUnavailWebView2Missing, "the Microsoft Edge WebView2 Runtime is not visible to the chat window program"
	}
	return chatUnavailWebView2Failed, "WebView2 " + strings.TrimSpace(v) + " is installed but did not start"
}

func getUserDisplayName() string {
	u, err := os.UserHomeDir()
	if err == nil {
		return filepath.Base(u)
	}
	h, _ := os.Hostname()
	return h
}

// chatStrings: texts of the chat window (English, or French for a French UI).
func chatStrings(fr bool) map[string]string {
	s := map[string]string{
		"chattingWith":    "Chatting with",
		"online":          "Online",
		"yourMessage":     "Your message...",
		"remoteRequested": "Remote control access requested.",
		"allow":           "Allow",
		"deny":            "Deny",
		"remoteGranted":   "Remote control access granted.",
		"remoteDenied":    "Remote control access denied.",
		"minimize":        "Minimize",
		"close":           "Close",
		"fileReceived":    "\U0001F4CE File received:",
		"send":            "Send",
		"you":             "You",
		"operatorTyping":  "{name} is typing...",
		"cannotOpenTitle": "Chat request from {name}",
		"cannotOpen":      "The chat window could not open on this computer (Microsoft Edge WebView2 Runtime missing or not working). The technician has been informed.",
	}
	if fr {
		s["chattingWith"] = "Discussion avec"
		s["online"] = "En ligne"
		s["yourMessage"] = "Votre message..."
		s["remoteRequested"] = "Demande de prise de contrôle à distance reçue."
		s["allow"] = "Accepter"
		s["deny"] = "Refuser"
		s["remoteGranted"] = "Contrôle à distance autorisé."
		s["remoteDenied"] = "Contrôle à distance refusé."
		s["minimize"] = "Réduire"
		s["close"] = "Fermer"
		s["fileReceived"] = "\U0001F4CE Fichier reçu :"
		s["send"] = "Envoyer"
		s["you"] = "Vous"
		s["operatorTyping"] = "{name} est en train d'écrire..."
		s["cannotOpenTitle"] = "Demande de discussion de {name}"
		s["cannotOpen"] = "La fenêtre de discussion n'a pas pu s'ouvrir sur cet ordinateur (Microsoft Edge WebView2 Runtime absent ou défaillant). Le technicien en a été informé."
	}
	return s
}

// webChatUI applies the service's messages to the WebView2 page.
type webChatUI struct {
	h    *chatHost
	i18n map[string]string
}

func (w webChatUI) eval(js string) { w.h.dispatch(func() { w.h.evalJS(js) }) }

func (w webChatUI) operatorMessage(text string) {
	w.eval("addMessage(" + chatJSString(text) + ",'',true);playSound()")
}
func (w webChatUI) operatorTyping() { w.eval("showTyping()") }
func (w webChatUI) remoteRequest(message string) {
	w.eval("showRemoteRequest(" + chatJSString(message) + ")")
}
func (w webChatUI) fileReceived(name string) {
	w.eval("addMessage(" + chatJSString(w.i18n["fileReceived"]+" "+name) + ",'',true)")
}
func (w webChatUI) close() { w.h.close() }

// runChatHelperMode: the chat helper (runs as the session's user). ui is the
// --chat-ui value chosen by the service: "native" opens the native Win32
// window (chat_native_windows.go) at once; otherwise the WebView2 page, and
// the native window when WebView2 does not start.
func runChatHelperMode(addr, chatID, operatorName, ui string) {
	if tmpDir := os.TempDir(); tmpDir != "" {
		if f, err := os.OpenFile(
			filepath.Join(tmpDir, "oblireach-chat.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			log.SetOutput(f)
		}
	}
	log.Printf("chat-helper: connecting to %s (chatID=%s operator=%s)", addr, chatID, operatorName)

	// The window, WebView2 and the message loop all live on this thread.
	runtime.LockOSThread()

	var conn net.Conn
	var err error
	for i := 0; i < 20; i++ {
		conn, err = net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		log.Fatalf("chat-helper: connect failed: %v", err)
	}

	chatConnMu.Lock()
	chatConn = conn
	chatConnMu.Unlock()
	defer conn.Close()

	// Wait for init message with avatar data
	operatorAvatar := ""
	initType, initPayload, err := chatPipeRecv(conn)
	if err != nil || initType != chatPipeInit {
		log.Printf("chat-helper: expected init, got type=%d err=%v", initType, err)
	} else {
		var initData struct {
			OperatorName   string `json:"operatorName"`
			OperatorAvatar string `json:"operatorAvatar"`
		}
		if json.Unmarshal(initPayload, &initData) == nil {
			if initData.OperatorName != "" {
				operatorName = initData.OperatorName
			}
			operatorAvatar = initData.OperatorAvatar
			log.Printf("chat-helper: init received, operator=%s avatar=%d bytes",
				operatorName, len(operatorAvatar))
		}
	}

	operatorName = chatCleanText(operatorName)
	if operatorName == "" {
		operatorName = "Obliance"
	}
	userName := chatCleanText(getUserDisplayName())
	userInitials := chatInitials(userName)
	i18n := chatStrings(chatIsFrench())

	if chatUIFromFlag(ui) == chatUINative {
		log.Printf("chat-helper: native chat window (asked by the service)")
		runNativeChat(conn, operatorName, userName, i18n)
		return
	}

	h := newChatHost()
	if err := h.create("Obliance Chat"); err != nil {
		log.Printf("chat-helper: cannot create the chat window: %v", err)
		return
	}
	h.armTimers()
	if !h.embed() {
		// WebView2 did not start (policy, broken runtime, a runtime too old
		// for this Windows): the native window serves the chat instead —
		// same protocol, the service sees "ready" as usual (and the
		// operator a chat, not "could not open"). Only when that window
		// cannot be created either is the service told (chatHelperUnavailable
		// in runNativeChat).
		reason, detail := chatEmbedFailure()
		h.destroyQuietly()
		log.Printf("chat-helper: WebView2 could not start (%s: %s) — native chat window instead", reason, detail)
		runNativeChat(conn, operatorName, userName, i18n)
		return
	}

	h.onCall = func(c chatBridgeCall) {
		switch c.Method {
		case "goSendMessage":
			text := c.arg(0)
			if strings.TrimSpace(text) == "" {
				return
			}
			goChatSend("user_message", text)
			js := "addMessage(" + chatJSString(text) + "," + chatJSString(userInitials) + ",false)"
			h.dispatch(func() { h.evalJS(js) })
		case "goMinimizeChat":
			h.dispatch(h.toBubble)
		case "goRestoreChat":
			h.dispatch(h.toFull)
		case "goCloseChat":
			goChatSend("user_closed", "")
			h.close()
		case "goAllowRemote":
			goChatSend("allow_remote", "")
		case "goDenyRemote":
			goChatSend("deny_remote", "")
		case "goTyping":
			// Typing event to service -> server -> operator
			goChatSend("typing", "")
		case "goStartDrag":
			h.startDrag()
		}
	}
	h.loadPage(buildChatHTML(operatorName, operatorAvatar, userName, userInitials, i18n))

	// Read pipe messages in background (chatHelperDispatch, chat_native.go:
	// the same mapping as the native window).
	web := webChatUI{h: h, i18n: i18n}
	go func() {
		for {
			msgType, payload, err := chatPipeRecv(conn)
			if err != nil {
				if err != io.EOF && !errors.Is(err, net.ErrClosed) {
					log.Printf("chat-helper: pipe read error: %v", err)
				}
				h.close()
				return
			}
			if chatHelperDispatch(web, msgType, payload) {
				return
			}
		}
	}()

	// Run the event loop (returns when the window is destroyed)
	chatRunMessageLoop()
	log.Printf("chat-helper: exiting")
}

// chatAvatarHTML: the operator's picture (validated data URI) or initial.
func chatAvatarHTML(operatorName, operatorAvatar string, size int) string {
	px := fmt.Sprint(size)
	if src := chatAvatarSrc(operatorAvatar); src != "" {
		return `<img src="` + src + `" style="width:` + px + `px;height:` + px + `px;border-radius:50%;object-fit:cover;flex-shrink:0" />`
	}
	font := "11"
	if size >= 32 {
		font = "12"
	}
	return `<div style="width:` + px + `px;height:` + px + `px;border-radius:50%;background:#c2001b;display:flex;align-items:center;justify-content:center;flex-shrink:0"><span style="font-size:` + font + `px;font-weight:600;color:rgba(255,255,255,0.95)">` +
		html.EscapeString(chatInitial(operatorName)) + `</span></div>`
}

// buildChatHTML returns the chat page. Every dynamic value is a Sprintf
// argument, escaped for where it goes (a "%" in a name cannot break it).
func buildChatHTML(operatorName, operatorAvatar, userName, userInitials string, i18n map[string]string) string {
	avatarHTML := chatAvatarHTML(operatorName, operatorAvatar, 32)
	smallAvatarHTML := chatAvatarHTML(operatorName, operatorAvatar, 28)
	pageStrings, _ := json.Marshal(map[string]string{
		"remoteRequested": i18n["remoteRequested"],
		"remoteGranted":   i18n["remoteGranted"],
		"remoteDenied":    i18n["remoteDenied"],
	})
	esc := html.EscapeString

	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8">
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#0d1117;overflow:hidden;height:100vh;display:flex;flex-direction:column;margin:0}
::-webkit-scrollbar{width:4px}
::-webkit-scrollbar-thumb{background:rgba(255,255,255,0.08);border-radius:4px}
::-webkit-scrollbar-track{background:transparent}

/* ── Full chat view ── */
.chat-container{display:flex;flex-direction:column;height:100vh;overflow:hidden;background:#0d1117}
.header{background:#161b22;padding:14px 16px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid rgba(255,255,255,0.06);cursor:default;user-select:none;-webkit-user-select:none}
.header button{background:transparent;border:none;cursor:pointer;padding:4px;display:flex;align-items:center;border-radius:6px}
.header button:hover{background:rgba(255,255,255,0.08)}
.messages{flex:1;overflow-y:auto;padding:16px;display:flex;flex-direction:column;gap:12px}
.msg-row{display:flex;gap:8px;align-items:flex-end}
.msg-row.user{justify-content:flex-end}
.bubble{border-radius:14px 14px 14px 2px;padding:10px 14px;max-width:75%%;font-size:13px;line-height:1.5}
.bubble.op{background:rgba(194,0,27,0.12);border:1px solid rgba(194,0,27,0.2);color:#e6edf3}
.bubble.user{background:#c2001b;border-radius:14px 14px 2px 14px;color:rgba(255,255,255,0.95)}
.bubble.system{background:rgba(250,204,21,0.08);border:1px solid rgba(250,204,21,0.15);color:rgba(250,204,21,0.8);font-size:11px;text-align:center;border-radius:10px;align-self:center;max-width:90%%}
.avatar{width:28px;height:28px;border-radius:50%%;flex-shrink:0;display:flex;align-items:center;justify-content:center}
.avatar.op{background:#c2001b}
.avatar.user{background:#21262d}
.avatar img{width:28px;height:28px;border-radius:50%%;object-fit:cover}
.avatar span{font-size:11px;font-weight:500;color:#8b949e}
.timestamp{text-align:center;margin:4px 0}
.timestamp span{font-size:11px;color:#484f58;background:rgba(255,255,255,0.04);padding:3px 10px;border-radius:20px}
.input-area{background:#161b22;padding:12px;border-top:1px solid rgba(255,255,255,0.06)}
.input-row{display:flex;align-items:center;gap:8px;background:#0d1117;border:1px solid rgba(255,255,255,0.1);border-radius:10px;padding:8px 12px}
.input-row input{flex:1;background:transparent;border:none;outline:none;font-size:13px;color:#e6edf3}
.input-row input::placeholder{color:#484f58}
.input-row input:focus{outline:none}
.send-btn{background:#c2001b;border:none;cursor:pointer;width:30px;height:30px;border-radius:8px;display:flex;align-items:center;justify-content:center;transition:background .15s}
.send-btn:hover{background:#a80018}
.remote-panel{background:rgba(194,0,27,0.08);border:1px solid rgba(194,0,27,0.2);border-radius:12px;padding:12px;margin:8px 0;text-align:center}
.remote-panel p{font-size:12px;color:#e6edf3;margin-bottom:8px}
.remote-panel button{padding:6px 16px;border-radius:8px;border:none;cursor:pointer;font-size:12px;font-weight:500;margin:0 4px}
.remote-panel .allow{background:#22c55e;color:white}
.remote-panel .deny{background:#ef4444;color:white}

/* ── Bubble mode ── */
.bubble-btn{display:none;width:56px;height:56px;border-radius:50%%;background:#c2001b;cursor:pointer;align-items:center;justify-content:center;position:relative;box-shadow:0 4px 16px rgba(0,0,0,0.5)}
.bubble-btn:hover{background:#a80018}
.bubble-btn svg{width:24px;height:24px}
.unread-badge{position:absolute;top:-2px;right:-2px;min-width:18px;height:18px;border-radius:9px;background:#ef4444;color:white;font-size:10px;font-weight:700;display:none;align-items:center;justify-content:center;padding:0 4px}
/* ── Typing indicator ── */
.typing-row{display:flex;gap:8px;align-items:flex-end;display:none}
.typing-row.visible{display:flex}
.typing-dots{display:flex;gap:3px;padding:10px 14px;background:rgba(194,0,27,0.12);border:1px solid rgba(194,0,27,0.2);border-radius:14px 14px 14px 2px}
.typing-dots span{width:6px;height:6px;border-radius:50%%;background:#8b949e;animation:typeBounce 1.2s infinite}
.typing-dots span:nth-child(2){animation-delay:0.2s}
.typing-dots span:nth-child(3){animation-delay:0.4s}
@keyframes typeBounce{0%%,60%%,100%%{transform:translateY(0);opacity:0.4}30%%{transform:translateY(-4px);opacity:1}}

body.minimized .chat-container{display:none}
body.minimized .bubble-btn{display:flex}
</style>
</head><body>

<!-- Bubble button (shown when minimized) -->
<div class="bubble-btn" id="bubble-btn" onclick="restoreChat()">
  <svg viewBox="0 0 24 24" fill="none"><path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z" fill="white"/></svg>
  <span class="unread-badge" id="unread-badge">0</span>
</div>

<!-- Full chat container -->
<div class="chat-container">
  <div class="header" id="header">
    <div style="display:flex;align-items:center;gap:10px">
      %s
      <div>
        <p style="font-size:13px;font-weight:500;color:#e6edf3">%s</p>
        <div style="display:flex;align-items:center;gap:5px">
          <div style="width:6px;height:6px;border-radius:50%%;background:#4ade80"></div>
          <span style="font-size:11px;color:#8b949e">%s</span>
        </div>
      </div>
    </div>
    <div style="display:flex;gap:4px">
      <button onclick="minimizeChat()" title="%s">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none"><path d="M4 12h16" stroke="#8b949e" stroke-width="1.5" stroke-linecap="round"/></svg>
      </button>
      <button onclick="goCloseChat()" title="%s">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none"><path d="M18 6L6 18M6 6l12 12" stroke="#8b949e" stroke-width="1.5" stroke-linecap="round"/></svg>
      </button>
    </div>
  </div>
  <div class="messages" id="messages">
    <div class="typing-row" id="typing-row">
      <div class="avatar op" style="width:28px;height:28px;border-radius:50%%;display:flex;align-items:center;justify-content:center">%s</div>
      <div class="typing-dots"><span></span><span></span><span></span></div>
    </div>
  </div>
  <div id="remote-panel" style="display:none;padding:0 12px">
    <div class="remote-panel">
      <p id="remote-msg">%s</p>
      <button class="allow" onclick="answerRemote(true)">%s</button>
      <button class="deny" onclick="answerRemote(false)">%s</button>
    </div>
  </div>
  <div class="input-area">
    <div class="input-row">
      <input id="input" type="text" placeholder="%s" onkeydown="if(event.key==='Enter')sendMsg()" oninput="emitTyping()" />
      <button class="send-btn" onclick="sendMsg()">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M22 2L11 13M22 2L15 22l-4-9-9-4 20-7z" stroke="white" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </button>
    </div>
  </div>
</div>
<script>
const opAvatarSmall = %s;
const userInitials = %s;
const T = %s;
let isMinimized = false;
let unreadCount = 0;
let typingTimer = null;
let lastTypingEmit = 0;

// A file dropped on the window never replaces the chat page (the browser's
// default for a drop is to open the file).
window.addEventListener('dragover', function(e) { e.preventDefault(); if (e.dataTransfer) e.dataTransfer.dropEffect = 'none'; });
window.addEventListener('drop', function(e) { e.preventDefault(); });

// Drag the window by its header (the system move loop, like a title bar).
document.getElementById('header').addEventListener('mousedown', function(e) {
  if (e.button !== 0 || (e.target.closest && e.target.closest('button'))) return;
  e.preventDefault();
  goStartDrag();
});

function sendMsg() {
  const inp = document.getElementById('input');
  const text = inp.value.trim();
  if (!text) return;
  inp.value = '';
  goSendMessage(text);
}

function answerRemote(allowed) {
  if (allowed) goAllowRemote(); else goDenyRemote();
  document.getElementById('remote-panel').style.display = 'none';
  addSystemMessage(allowed ? T.remoteGranted : T.remoteDenied);
}

// Typing indicator: show operator typing animation (3s timeout)
function showTyping() {
  const el = document.getElementById('typing-row');
  el.classList.add('visible');
  const msgs = document.getElementById('messages');
  msgs.scrollTop = msgs.scrollHeight;
  clearTimeout(typingTimer);
  typingTimer = setTimeout(function() { el.classList.remove('visible'); }, 3000);
}

function hideTyping() {
  clearTimeout(typingTimer);
  document.getElementById('typing-row').classList.remove('visible');
}

// Emit user typing to operator (debounced: max 1 per 2s)
function emitTyping() {
  const now = Date.now();
  if (now - lastTypingEmit < 2000) return;
  lastTypingEmit = now;
  goTyping();
}

function addMessage(text, initials, isOp) {
  // Hide typing indicator when a real message arrives from operator
  if (isOp) hideTyping();

  const msgs = document.getElementById('messages');
  const typingEl = document.getElementById('typing-row');
  const row = document.createElement('div');
  row.className = 'msg-row ' + (isOp ? '' : 'user');
  if (isOp) {
    row.innerHTML = '<div class="avatar op">' + opAvatarSmall + '</div>' +
      '<div class="bubble op">' + escHtml(text) + '</div>';
  } else {
    row.innerHTML = '<div class="bubble user">' + escHtml(text) + '</div>' +
      '<div class="avatar user"><span>' + escHtml(initials || userInitials) + '</span></div>';
  }
  // Insert before the typing row so it stays at the bottom
  msgs.insertBefore(row, typingEl);
  msgs.scrollTop = msgs.scrollHeight;

  // Auto-restore chat when operator sends a message while minimized
  if (isMinimized && isOp) {
    restoreChat();
  }
}

function addSystemMessage(text) {
  const msgs = document.getElementById('messages');
  const row = document.createElement('div');
  row.style.cssText = 'display:flex;justify-content:center';
  row.innerHTML = '<div class="bubble system">' + escHtml(text) + '</div>';
  msgs.appendChild(row);
  msgs.scrollTop = msgs.scrollHeight;
}

function showRemoteRequest(msg) {
  // Auto-restore from bubble if minimized
  if (isMinimized) restoreChat();
  const panel = document.getElementById('remote-panel');
  const rmsg = document.getElementById('remote-msg');
  rmsg.textContent = msg || T.remoteRequested;
  panel.style.display = 'block';
  playSound();
}

function playSound() {
  try {
    const ctx = new AudioContext();
    const osc = ctx.createOscillator();
    const g = ctx.createGain();
    osc.connect(g); g.connect(ctx.destination);
    osc.frequency.value = 800; g.gain.value = 0.15;
    osc.start(); g.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.3);
    osc.stop(ctx.currentTime + 0.3);
  } catch(e) {}
}

function minimizeChat() {
  isMinimized = true;
  document.body.classList.add('minimized');
  goMinimizeChat();
}

function restoreChat() {
  isMinimized = false;
  unreadCount = 0;
  document.body.classList.remove('minimized');
  document.getElementById('unread-badge').style.display = 'none';
  goRestoreChat();
  // Scroll to bottom and focus input
  setTimeout(function() {
    var msgs = document.getElementById('messages');
    if (msgs) msgs.scrollTop = msgs.scrollHeight;
    var inp = document.getElementById('input');
    if (inp) inp.focus();
  }, 100);
}

function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}
</script>
</body></html>`,
		avatarHTML,
		esc(i18n["chattingWith"]+" "+operatorName),
		esc(i18n["online"]),
		esc(i18n["minimize"]),
		esc(i18n["close"]),
		smallAvatarHTML,
		esc(i18n["remoteRequested"]),
		esc(i18n["allow"]),
		esc(i18n["deny"]),
		esc(i18n["yourMessage"]),
		chatJSString(smallAvatarHTML),
		chatJSString(userInitials),
		string(pageStrings))
}
