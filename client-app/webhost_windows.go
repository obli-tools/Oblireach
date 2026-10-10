//go:build windows

package main

// The app's windows: the main window (mainwindow_windows.go) and the detached
// session windows (detached_windows.go) are plain Win32 windows, each hosting
// its own WebView2 through the edge package of go-webview2, all on the app's
// UI thread (the main goroutine, locked to its OS thread by the edge
// package's init). One message loop, runMessageLoop, serves them all.
//
// go-webview2's own window is not used any more: its standard frame showed
// before anything could replace it (the white Windows title bar flashed at
// start), its dispatch queue was woken by thread messages that any modal loop
// drops (a window drag, a nested WebView2 creation), and its Destroy quits the
// app. Its page-side binding glue and message format are kept (rpcGlue).
//
// Every window is frameless (frame_windows.go): the page draws the title bar.

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

var (
	modOle32 = syscall.NewLazyDLL("ole32.dll")

	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage  = modUser32.NewProc("PostQuitMessage")
	procCoTaskMemFree    = modOle32.NewProc("CoTaskMemFree")
)

// Posted to one of our windows: run its Dispatch queue. A window message, not
// a thread one: a modal loop (a window being moved, a WebView2 being created)
// dispatches it too, where a thread message would be lost.
const wmHostDispatch = 0x8000 + 0x21

// webHost is one of our top-level windows and its WebView2; it implements
// bridgeHost (native_bridge.go) and appWindow (platform.go). Everything runs on the UI thread, except
// Dispatch, which any goroutine may call.
type webHost struct {
	hwnd     uintptr
	origin   string // local UI origin: a page elsewhere gets the native strip (frame_windows.go)
	chromium *edge.Chromium
	rpc      rpcBindings

	ready    bool // the WebView2 exists (Embed returned): Init / Eval can run
	finished bool // controller closed, window destroyed (or being destroyed)

	frame frameState // frame_windows.go

	qmu   sync.Mutex
	queue []func()
	gone  bool
}

// embed creates the window's WebView2 (Embed runs a nested message loop
// meanwhile: other windows' calls and dispatched functions run in it).
func (h *webHost) embed() error {
	c := edge.NewChromium()
	c.MessageCallback = h.onMessage
	c.NavigationCompletedCallback = h.onNavigated
	c.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)
	h.chromium = c
	if !c.Embed(h.hwnd) {
		return errors.New(tr("win.webviewFailed")) // (a session window's page shows it)
	}
	hardenWebView(c) // DevTools off, no password autosave / autofill (vault secrets)
	h.ready = true
	h.resize()
	// The theme's background until the page paints (no white flash on the
	// dark themes; theme_windows.go follows the theme's changes).
	putWebViewBackground(c, currentPalette().Bg)
	h.invalidate() // the native strip goes (the page now covers the window)
	return nil
}

// resize fits the WebView2 to the client area, below the native strip when
// the page on screen is not the local UI (frame_windows.go).
func (h *webHost) resize() {
	if !h.ready || h.finished || h.chromium == nil {
		return
	}
	ctrl := h.chromium.GetController()
	if ctrl == nil {
		return
	}
	var rc winRect
	procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&rc)))
	if top := h.webTop(); top > 0 {
		rc.Top = min32(rc.Top+top, rc.Bottom)
	}
	putControllerBounds(ctrl, rc)
}

// onNavigated (NavigationCompleted): the local UI draws its own title bar;
// any other page (the SSO sign-in, an error page) gets the native strip.
func (h *webHost) onNavigated(sender *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
	if h.origin == "" || h.finished {
		return
	}
	foreign := !isLocalUIURL(webviewSource(sender), h.origin)
	if foreign == h.frame.foreign {
		return
	}
	h.frame.foreign = foreign
	h.frame.hover, h.frame.pressed = stripNone, stripNone
	h.resize()
	h.invalidate()
}

func (h *webHost) invalidate() {
	if h.hwnd != 0 {
		procInvalidateRect.Call(h.hwnd, 0, 1)
	}
}

// (isLocalUIURL, rpcBindings, rpcGlue: webhost_rpc.go, platform-neutral.)

// ── bridgeHost ─────────────────────────────────────────────────────────────

func (h *webHost) Window() unsafe.Pointer { return globalPtr(h.hwnd) }

func (h *webHost) Init(js string) {
	if h.ready && !h.finished {
		h.chromium.Init(js)
	}
}

func (h *webHost) Eval(js string) {
	if h.ready && !h.finished {
		h.chromium.Eval(js)
	}
}

func (h *webHost) Navigate(u string) {
	if h.ready && !h.finished {
		h.chromium.Navigate(u)
	}
}

// Dispatch runs f on the UI thread (any goroutine may call it). Dropped once
// the window is gone.
func (h *webHost) Dispatch(f func()) {
	h.qmu.Lock()
	defer h.qmu.Unlock()
	if h.gone {
		return
	}
	h.queue = append(h.queue, f)
	// Under the lock (PostMessage never blocks): WM_DESTROY takes it too, so
	// nothing is ever posted to the handle of a destroyed window.
	procPostMessageW.Call(h.hwnd, wmHostDispatch, 0, 0)
}

func (h *webHost) drain() {
	h.qmu.Lock()
	q := h.queue
	h.queue = nil
	h.qmu.Unlock()
	for _, f := range q {
		f()
	}
}

// markGone (WM_DESTROY): nothing is queued nor posted any more.
func (h *webHost) markGone() {
	h.qmu.Lock()
	h.gone = true
	h.queue = nil
	h.qmu.Unlock()
	h.finished = true
}

// Bind exposes f to the page as a global function returning a promise, like
// go-webview2's Bind (same page-side glue, same message format).
func (h *webHost) Bind(name string, f interface{}) error {
	if err := h.rpc.add(name, f); err != nil {
		return err
	}
	h.Init(rpcGlue(name))
	return nil
}

// onMessage: a call from the page (UI thread); the answer is evaluated
// afterwards, outside the WebView2 event.
func (h *webHost) onMessage(msg string) {
	id, res, err := h.rpc.call(msg)
	if id < 0 {
		return
	}
	sid := strconv.Itoa(id)
	var js string
	if err != nil {
		js = "window._rpc[" + sid + "].reject(" + jsonString(err.Error()) + "); window._rpc[" + sid + "] = undefined"
	} else {
		js = "window._rpc[" + sid + "].resolve(" + res + "); window._rpc[" + sid + "] = undefined"
	}
	h.Dispatch(func() { h.Eval(js) })
}

// ── Window class, message loop ─────────────────────────────────────────────

var (
	classMu    sync.Mutex
	classNames = map[string]*uint16{}
)

// registerWindowClass registers one of our window classes (once per name):
// the app's icon, and the theme's background while the page loads (the
// theme then in force; frame_windows.go paints the current one after that).
func registerWindowClass(name string, proc func(hwnd, msg, wParam, lParam uintptr) uintptr) (*uint16, error) {
	classMu.Lock()
	defer classMu.Unlock()
	if cn := classNames[name]; cn != nil {
		return cn, nil
	}
	cn, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	hinst, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	var large, small uintptr // the first icon of the exe
	if exe := exePath(); exe != nil {
		procExtractIconExW.Call(uintptr(unsafe.Pointer(exe)), 0, uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1)
	}
	brush, _, _ := procCreateSolidBrush.Call(uintptr(colorRef(currentPalette().Bg)))
	wc := wndClassExW{
		Style:         csHRedraw | csVRedraw,
		LpfnWndProc:   syscall.NewCallback(proc),
		HInstance:     hinst,
		HIcon:         large,
		HCursor:       cursor,
		HbrBackground: brush,
		LpszClassName: cn,
		HIconSm:       small,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return nil, fmt.Errorf("RegisterClassEx: %v", err)
	}
	classNames[name] = cn
	return cn, nil
}

func exePath() *uint16 {
	buf := make([]uint16, 32768)
	n, _, _ := procGetModuleFileNameW.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		return nil
	}
	return &buf[0]
}

// runMessageLoop pumps the messages of every window of the UI thread until
// WM_QUIT (the main window is destroyed).
func runMessageLoop() {
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// ── WebView2 COM calls the edge package does not expose ────────────────────

// putControllerBounds: ICoreWebView2Controller::put_Bounds (vtable slot 6:
// IUnknown's 3 methods, IsVisible get/put, Bounds get, Bounds put), with the
// RECT passed by value as the edge package does for each architecture.
func putControllerBounds(ctrl *edge.ICoreWebView2Controller, r winRect) {
	if ctrl == nil {
		return
	}
	vtbl := *(**[26]uintptr)(unsafe.Pointer(ctrl))
	this := uintptr(unsafe.Pointer(ctrl))
	switch runtime.GOARCH {
	case "386":
		syscall.SyscallN(vtbl[6], this, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	case "arm64":
		words := (*[2]uintptr)(unsafe.Pointer(&r))
		syscall.SyscallN(vtbl[6], this, words[0], words[1])
	default: // amd64: a 16-byte struct goes by reference
		syscall.SyscallN(vtbl[6], this, uintptr(unsafe.Pointer(&r)))
	}
}

// webviewSource: ICoreWebView2::get_Source (vtable slot 4: IUnknown's 3
// methods, Settings get, Source get), the URL of the document on screen.
func webviewSource(wv *edge.ICoreWebView2) string {
	if wv == nil {
		return ""
	}
	vtbl := *(**[5]uintptr)(unsafe.Pointer(wv))
	var p *uint16
	if hr, _, _ := syscall.SyscallN(vtbl[4], uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(&p))); int32(hr) < 0 || p == nil {
		return ""
	}
	s := utf16PtrString(p, 8192)
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}

// utf16PtrString reads a NUL-terminated UTF-16 string of at most max units.
func utf16PtrString(p *uint16, max int) string {
	if p == nil {
		return ""
	}
	var u []uint16
	for i := 0; i < max; i++ {
		c := *(*uint16)(unsafe.Add(unsafe.Pointer(p), 2*i))
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return syscall.UTF16ToString(u)
}
