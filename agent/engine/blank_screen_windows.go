//go:build windows

package engine

// Blank screen, Windows side (blank_screen.go has the protocol and texts).
//
// One full-screen, topmost, click-through window per monitor, in the capture
// helper's process (it runs in the streamed session, on its Default desktop):
//   - WS_EX_LAYERED + WS_EX_TRANSPARENT: never takes a click (the operator's
//     injected input reaches the real windows underneath — so would the local
//     user's, which is why the viewers block local input with it);
//   - WS_EX_NOACTIVATE / WS_EX_TOOLWINDOW: no focus, no taskbar button;
//   - SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE) set and READ BACK on
//     every window BEFORE any is shown: the capture (DXGI duplication, GDI)
//     then sees the desktop underneath, the physical screen the page. On a
//     Windows older than 10 2004 (build 19041) the flag does not exist (an
//     older build would treat it as WDA_MONITOR — a black rectangle in the
//     capture: the operator blinded too), so the feature is not offered at all
//     there (os_version) and a show that cannot confirm the flag shows nothing
//     ("failed").
//
// Every user32 / gdi32 / advapi32 function is resolved at run time (lazy):
// nothing here adds a static import, the nt6 build (Windows 7 / 2008 R2 –
// 2012 R2) links and starts as before (TestBuiltExeImports) and simply never
// offers the feature.
//
// Lifetime: the windows belong to the helper — they go with it whatever ends
// it (stream stopped, viewer gone, service pipe broken, crash). The window
// thread re-asserts topmost on every relevant window event (blank_guard.go)
// and on a slow fallback tick, and removes the page when the capture loop
// stops beating (blankWatchdogMs) or when a window lost its capture exclusion. Ctrl+Alt+Del, the lock screen and UAC prompts are other
// desktops (Winlogon / secure desktop): they show normally above it, and the
// local-input block's escape hatch (Ctrl+Alt+Del) lifts the page too (the
// capture loop, on inputBlockReleased).

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
	"log"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Win32 structures of the blank-screen window (same layouts as the chat
// window's in the agent: WNDCLASSEXW, MSG, RECT, POINT, MONITORINFO).
type bsRect struct{ Left, Top, Right, Bottom int32 }

type bsPoint struct{ X, Y int32 }

type bsWndClassEx struct {
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

type bsMsg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       bsPoint
	lPrivate uint32
}

type bsMonitorInfo struct {
	cbSize    uint32
	rcMonitor bsRect
	rcWork    bsRect
	dwFlags   uint32
}

//go:embed blank_logo.png
var blankLogoPNG []byte

var (
	bsUser32   = newSystemLazyDLL("user32.dll")
	bsGdi32    = newSystemLazyDLL("gdi32.dll")
	bsKernel32 = newSystemLazyDLL("kernel32.dll")
	bsAdvapi32 = newSystemLazyDLL("advapi32.dll")

	bsRegisterClassExW         = bsUser32.NewProc("RegisterClassExW")
	bsCreateWindowExW          = bsUser32.NewProc("CreateWindowExW")
	bsDefWindowProcW           = bsUser32.NewProc("DefWindowProcW")
	bsDestroyWindow            = bsUser32.NewProc("DestroyWindow")
	bsShowWindow               = bsUser32.NewProc("ShowWindow")
	bsSetWindowPos             = bsUser32.NewProc("SetWindowPos")
	bsSetLayeredAttr           = bsUser32.NewProc("SetLayeredWindowAttributes")
	bsSetWindowDisplayAffinity = bsUser32.NewProc("SetWindowDisplayAffinity")
	bsGetWindowDisplayAffinity = bsUser32.NewProc("GetWindowDisplayAffinity")
	bsEnumDisplayMonitors      = bsUser32.NewProc("EnumDisplayMonitors")
	bsGetMonitorInfoW          = bsUser32.NewProc("GetMonitorInfoW")
	bsBeginPaint               = bsUser32.NewProc("BeginPaint")
	bsEndPaint                 = bsUser32.NewProc("EndPaint")
	bsFillRect                 = bsUser32.NewProc("FillRect")
	bsDrawTextW                = bsUser32.NewProc("DrawTextW")
	bsGetMessageW              = bsUser32.NewProc("GetMessageW")
	bsPeekMessageW             = bsUser32.NewProc("PeekMessageW")
	bsTranslateMessage         = bsUser32.NewProc("TranslateMessage")
	bsDispatchMessageW         = bsUser32.NewProc("DispatchMessageW")
	bsPostThreadMessageW       = bsUser32.NewProc("PostThreadMessageW")
	bsSetTimer                 = bsUser32.NewProc("SetTimer")
	bsOpenDesktopW             = bsUser32.NewProc("OpenDesktopW")
	bsSetThreadDesktop         = bsUser32.NewProc("SetThreadDesktop")
	bsGetThreadDesktop         = bsUser32.NewProc("GetThreadDesktop")
	bsGetUserObjectInformation = bsUser32.NewProc("GetUserObjectInformationW")
	bsLoadCursorW              = bsUser32.NewProc("LoadCursorW")

	bsCreateSolidBrush  = bsGdi32.NewProc("CreateSolidBrush")
	bsDeleteObject      = bsGdi32.NewProc("DeleteObject")
	bsCreateFontW       = bsGdi32.NewProc("CreateFontW")
	bsSelectObject      = bsGdi32.NewProc("SelectObject")
	bsSetTextColor      = bsGdi32.NewProc("SetTextColor")
	bsSetBkMode         = bsGdi32.NewProc("SetBkMode")
	bsStretchDIBits     = bsGdi32.NewProc("StretchDIBits")
	bsSetStretchBltMode = bsGdi32.NewProc("SetStretchBltMode")
	bsSetBrushOrgEx     = bsGdi32.NewProc("SetBrushOrgEx")

	bsGetModuleHandleW         = bsKernel32.NewProc("GetModuleHandleW")
	bsGetCurrentThreadID       = bsKernel32.NewProc("GetCurrentThreadId")
	bsGetUserDefaultUILanguage = bsKernel32.NewProc("GetUserDefaultUILanguage")
	bsRegOpenCurrentUser       = bsAdvapi32.NewProc("RegOpenCurrentUser")
	bsRegOpenKeyExW            = bsAdvapi32.NewProc("RegOpenKeyExW")
	bsRegQueryValueExW         = bsAdvapi32.NewProc("RegQueryValueExW")
	bsRegCloseKey              = bsAdvapi32.NewProc("RegCloseKey")
)

const (
	bsWdaExcludeFromCapture = 0x11
	bsWsPopup               = 0x80000000
	bsWsExTopmost           = 0x00000008
	bsWsExTransparent       = 0x00000020
	bsWsExToolWindow        = 0x00000080
	bsWsExLayered           = 0x00080000
	bsWsExNoActivate        = 0x08000000
	bsLwaAlpha              = 0x2
	bsSwShowNoActivate      = 4
	bsSwpNoSize             = 0x0001
	bsSwpNoMove             = 0x0002
	bsSwpNoActivate         = 0x0010
	bsSwpNoOwnerZOrder      = 0x0200
	bsHwndTopmost           = ^uintptr(0)
	bsWmPaint               = 0x000F
	bsWmEraseBkgnd          = 0x0014
	bsWmNcHitTest           = 0x0084
	bsWmTimer               = 0x0113
	bsWmDisplayChange       = 0x007E
	bsWmApp                 = 0x8000
	bsWmRequest             = bsWmApp + 0x51
	bsWmRebuild             = bsWmApp + 0x52
	bsWmReassert            = bsWmApp + 0x53
	bsHtTransparent         = ^uintptr(0) // -1
	bsDtCenter              = 0x0001
	bsDtWordBreak           = 0x0010
	bsDtNoPrefix            = 0x0800
	bsDtCalcRect            = 0x0400
	bsDtRtlReading          = 0x00020000
	bsTransparent           = 1
	bsHalftone              = 4
	bsSrcCopy               = 0x00CC0020
	bsKeyRead               = 0x20019
	bsUoiName               = 2
	bsGenericAll            = 0x10000000

	// #0d1117 page, texts like the agent's chat window.
	bsColorBg    = 0x0017110D
	bsColorTitle = 0x00F3EDE6 // #e6edf3
	bsColorBody  = 0x00D9D1C9 // #c9d1d9
	bsColorHint  = 0x009E948B // #8b949e
	bsColorLine  = 0x001B00C2 // #c2001b (Oblireach red)

	bsMinBuild = 19041 // Windows 10 2004: WDA_EXCLUDEFROMCAPTURE
)

type bsPaintStruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     bsRect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

type bsBitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// platformBlankScreenAvailable: Windows 10 2004+ with the API present.
func platformBlankScreenAvailable() (bool, string) {
	v := WindowsVersion()
	if !v.known() || v.Major < 10 || (v.Major == 10 && v.Build < bsMinBuild) {
		return false, "os_version"
	}
	if bsSetWindowDisplayAffinity.Find() != nil || bsGetWindowDisplayAffinity.Find() != nil {
		return false, "os_version"
	}
	return true, ""
}

// ── Language of the session's user ──────────────────────────────────────────

// platformBlankScreenLang: the display language of the session's Windows
// user (HKCU\Control Panel\Desktop PreferredUILanguages, read as that user),
// else this process's UI language, else the user's regional format, else
// English.
func platformBlankScreenLang() string {
	lang := ""
	_ = asSessionUser(func() error {
		if s := bsReadUserString(`Control Panel\Desktop`, "PreferredUILanguages"); s != "" {
			lang = blankLangFromTag(s)
		}
		return nil
	})
	if lang != "" {
		return lang
	}
	if bsGetUserDefaultUILanguage.Find() == nil {
		id, _, _ := bsGetUserDefaultUILanguage.Call()
		if l, ok := blankLangByPrimaryID[uint16(id)&0x3FF]; ok {
			return l
		}
	}
	_ = asSessionUser(func() error {
		if s := bsReadUserString(`Control Panel\International`, "LocaleName"); s != "" {
			lang = blankLangFromTag(s)
		}
		return nil
	})
	if lang != "" {
		return lang
	}
	return "en"
}

// bsReadUserString: a REG_SZ / REG_MULTI_SZ (its first string) of the current
// (impersonated) user's hive, "" when absent.
func bsReadUserString(subkey, value string) string {
	if bsRegOpenCurrentUser.Find() != nil {
		return ""
	}
	var hcu syscall.Handle
	if r, _, _ := bsRegOpenCurrentUser.Call(bsKeyRead, uintptr(unsafe.Pointer(&hcu))); r != 0 {
		return ""
	}
	defer bsRegCloseKey.Call(uintptr(hcu))
	sk, _ := syscall.UTF16PtrFromString(subkey)
	var hk syscall.Handle
	if r, _, _ := bsRegOpenKeyExW.Call(uintptr(hcu), uintptr(unsafe.Pointer(sk)), 0, bsKeyRead, uintptr(unsafe.Pointer(&hk))); r != 0 {
		return ""
	}
	defer bsRegCloseKey.Call(uintptr(hk))
	vn, _ := syscall.UTF16PtrFromString(value)
	buf := make([]uint16, 512)
	size := uint32(len(buf) * 2)
	var typ uint32
	if r, _, _ := bsRegQueryValueExW.Call(uintptr(hk), uintptr(unsafe.Pointer(vn)), 0, uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r != 0 {
		return ""
	}
	n := int(size / 2)
	if n > len(buf) {
		n = len(buf)
	}
	// REG_SZ / REG_MULTI_SZ: the first NUL-terminated string.
	return strings.TrimSpace(syscall.UTF16ToString(buf[:n]))
}

// ── Logo (decoded once, composed over the page colour) ──────────────────────

var (
	bsLogoOnce sync.Once
	bsLogoBGRA []byte
	bsLogoW    int
	bsLogoH    int
)

// bsLogo: the embedded logo as top-down BGRA, alpha already blended over the
// page colour (StretchDIBits then needs no alpha support).
func bsLogo() ([]byte, int, int) {
	bsLogoOnce.Do(func() {
		img, err := png.Decode(bytes.NewReader(blankLogoPNG))
		if err != nil {
			log.Printf("blank screen: logo decode failed: %v", err)
			return
		}
		bsLogoBGRA, bsLogoW, bsLogoH = composeOver(img, 0x0D, 0x11, 0x17)
	})
	return bsLogoBGRA, bsLogoW, bsLogoH
}

// composeOver: img blended over the colour (r,g,b) as top-down BGRA bytes.
func composeOver(img image.Image, br, bg, bb uint8) ([]byte, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA() // premultiplied, 16-bit
			inv := 0xffff - a
			o := (y*w + x) * 4
			out[o+0] = uint8((bl + uint32(bb)*0x101*inv/0xffff) >> 8)
			out[o+1] = uint8((g + uint32(bg)*0x101*inv/0xffff) >> 8)
			out[o+2] = uint8((r + uint32(br)*0x101*inv/0xffff) >> 8)
			out[o+3] = 0xff
		}
	}
	return out, w, h
}

// ── Window thread ───────────────────────────────────────────────────────────

type bsRequest struct {
	show  bool
	text  blankScreenText
	reply chan string // "" ok, else a reason
}

type bsWindow struct {
	hwnd uintptr
	w, h int32
}

var (
	bsMu      sync.Mutex
	bsTid     uint32
	bsReqs    = make(chan bsRequest, 4)
	bsStarted bool

	// Owned by the window thread.
	bsWindows []bsWindow
	bsText    blankScreenText
	bsShown   bool
	bsClass   *uint16
)

// platformBlankScreenShow shows the page on every monitor; "" when it is up
// with the capture exclusion confirmed, else the reason nothing is shown.
func platformBlankScreenShow(text blankScreenText) string {
	if ok, reason := platformBlankScreenAvailable(); !ok {
		return reason
	}
	if currentSessionInfo("").User == "" {
		return "no_user"
	}
	return bsDo(bsRequest{show: true, text: text})
}

// platformBlankScreenHide removes the page (no-op when not shown).
func platformBlankScreenHide() {
	bsMu.Lock()
	started := bsStarted
	bsMu.Unlock()
	if started {
		bsDo(bsRequest{show: false})
	}
}

func bsDo(req bsRequest) string {
	tid, err := bsEnsureThread()
	if err != "" {
		return err
	}
	req.reply = make(chan string, 1)
	bsReqs <- req
	bsPostThreadMessageW.Call(uintptr(tid), bsWmRequest, 0, 0)
	select {
	case r := <-req.reply:
		return r
	case <-time.After(5 * time.Second):
		log.Printf("blank screen: the window thread did not answer")
		return "failed"
	}
}

func bsEnsureThread() (uint32, string) {
	bsMu.Lock()
	defer bsMu.Unlock()
	if bsStarted {
		return bsTid, ""
	}
	ready := make(chan uint32, 1)
	go bsThread(ready)
	select {
	case tid := <-ready:
		if tid == 0 {
			return 0, "failed"
		}
		bsTid = tid
		bsStarted = true
		return tid, ""
	case <-time.After(5 * time.Second):
		return 0, "failed"
	}
}

// bsThread owns the windows: created, painted and destroyed here only.
func bsThread(ready chan<- uint32) {
	runtime.LockOSThread() // never unlocked: the thread dies with its windows
	bsAttachDefaultDesktop()
	if err := bsRegisterClass(); err != nil {
		log.Printf("blank screen: %v", err)
		ready <- 0
		return
	}
	var m bsMsg
	// Create the thread's message queue before anyone posts to it.
	bsPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, bsWmApp, bsWmApp, 0)
	tid, _, _ := bsGetCurrentThreadID.Call()
	// A slow (2 s) fallback tick: the capture-loop watchdog and the exclusion
	// check live here, plus a cheap re-assert. The topmost band is kept by the
	// event hook (blank_guard.go), so this stays deliberately infrequent — the
	// remote machine's CPU matters more than a pixel-perfect page.
	bsSetTimer.Call(0, 0, 2000, 0)
	ready <- uint32(tid)
	for {
		r, _, _ := bsGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			bsTeardown()
			return
		}
		if m.hwnd == 0 {
			switch m.message {
			case bsWmRequest:
				bsDrain()
				continue
			case bsWmRebuild:
				if bsShown {
					if reason := bsBuild(); reason != "" {
						log.Printf("blank screen: rebuild after a display change failed (%s): page removed", reason)
						bsTeardown()
						bsShown = false
						notifyBlankAutoOff("failed")
					}
				}
				continue
			case bsWmReassert:
				// A window event (menu, tooltip, foreground change, drag...)
				// may have come above the page: take the coalesced request and
				// put the page back on top. Event-driven, no polling.
				bgCoal.take()
				bsReassertTopmost()
				continue
			case bsWmTimer:
				bsTick()
				continue
			}
		}
		bsTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		bsDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// bsAttachDefaultDesktop puts the window thread on the session's Default
// desktop (a pooled OS thread may have been moved to another one).
func bsAttachDefaultDesktop() {
	if bsDesktopName() == "default" {
		return
	}
	name, _ := syscall.UTF16PtrFromString("Default")
	h, _, _ := bsOpenDesktopW.Call(uintptr(unsafe.Pointer(name)), 0, 0, bsGenericAll)
	if h == 0 {
		return
	}
	if r, _, _ := bsSetThreadDesktop.Call(h); r == 0 {
		log.Printf("blank screen: SetThreadDesktop(Default) failed")
	}
}

func bsDesktopName() string {
	tid, _, _ := bsGetCurrentThreadID.Call()
	h, _, _ := bsGetThreadDesktop.Call(tid)
	if h == 0 {
		return ""
	}
	buf := make([]uint16, 64)
	var need uint32
	if r, _, _ := bsGetUserObjectInformation.Call(h, bsUoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&need))); r == 0 {
		return ""
	}
	return strings.ToLower(syscall.UTF16ToString(buf))
}

func bsRegisterClass() error {
	name, _ := syscall.UTF16PtrFromString("OblireachBlankScreen")
	hinst, _, _ := bsGetModuleHandleW.Call(0)
	cursor, _, _ := bsLoadCursorW.Call(0, 32512) // IDC_ARROW
	wc := bsWndClassEx{
		lpfnWndProc:   syscall.NewCallback(bsWndProc),
		hInstance:     hinst,
		hCursor:       cursor,
		lpszClassName: name,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if atom, _, err := bsRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return err
	}
	bsClass = name
	return nil
}

func bsDrain() {
	for {
		select {
		case req := <-bsReqs:
			if !req.show {
				bsTeardown()
				bsShown = false
				req.reply <- ""
				continue
			}
			bsText = req.text
			reason := bsBuild()
			bsShown = reason == ""
			req.reply <- reason
		default:
			return
		}
	}
}

// bsBuild (re)creates one window per monitor: all with the capture exclusion
// confirmed, then all shown — or none.
func bsBuild() string {
	bsDestroyAll()
	mons := bsMonitors()
	if len(mons) == 0 {
		return "failed"
	}
	hinst, _, _ := bsGetModuleHandleW.Call(0)
	title, _ := syscall.UTF16PtrFromString("Oblireach")
	for _, r := range mons {
		w, h := r.Right-r.Left, r.Bottom-r.Top
		if w <= 0 || h <= 0 {
			continue
		}
		hwnd, _, err := bsCreateWindowExW.Call(
			bsWsExTopmost|bsWsExToolWindow|bsWsExLayered|bsWsExTransparent|bsWsExNoActivate,
			uintptr(unsafe.Pointer(bsClass)), uintptr(unsafe.Pointer(title)),
			bsWsPopup, uintptr(int(r.Left)), uintptr(int(r.Top)), uintptr(int(w)), uintptr(int(h)),
			0, 0, hinst, 0)
		if hwnd == 0 {
			log.Printf("blank screen: CreateWindowExW failed: %v", err)
			bsDestroyAll()
			return "failed"
		}
		bsWindows = append(bsWindows, bsWindow{hwnd: hwnd, w: w, h: h})
		bsSetLayeredAttr.Call(hwnd, 0, 255, bsLwaAlpha)
		if r, _, err := bsSetWindowDisplayAffinity.Call(hwnd, bsWdaExcludeFromCapture); r == 0 {
			log.Printf("blank screen: SetWindowDisplayAffinity failed: %v — nothing shown", err)
			bsDestroyAll()
			return "failed"
		}
		if !bsExcluded(hwnd) {
			log.Printf("blank screen: capture exclusion not confirmed — nothing shown")
			bsDestroyAll()
			return "failed"
		}
	}
	if len(bsWindows) == 0 {
		return "failed"
	}
	for _, w := range bsWindows {
		bsShowWindow.Call(w.hwnd, bsSwShowNoActivate)
		bsSetWindowPos.Call(w.hwnd, bsHwndTopmost, 0, 0, 0, 0, bsSwpNoMove|bsSwpNoSize|bsSwpNoActivate|bsSwpNoOwnerZOrder)
	}
	// Keep the page above whatever the operator opens, and hide the drag
	// outline. Installed once; a rebuild (display change) keeps them.
	bgInstallHooks()
	bgForceDragFull(CurrentSessionID())
	log.Printf("blank screen: shown on %d monitor(s)", len(bsWindows))
	return ""
}

// bsReassertTopmost puts the page back at the top of the topmost band on every
// monitor — cheap, and the only work done per window event (coalesced). No
// move / size / activate / owner-z reorder.
func bsReassertTopmost() {
	if !bsShown {
		return
	}
	for _, w := range bsWindows {
		bsSetWindowPos.Call(w.hwnd, bsHwndTopmost, 0, 0, 0, 0, bsSwpNoMove|bsSwpNoSize|bsSwpNoActivate|bsSwpNoOwnerZOrder)
	}
}

// bsTeardown removes the page and everything the guard put in place (hooks,
// forced full-window drag). Window thread only; use it on every path that
// takes the page down.
func bsTeardown() {
	bsDestroyAll()
	bgRemoveHooks()
	bgRestoreDragFull(CurrentSessionID())
}

func bsExcluded(hwnd uintptr) bool {
	var aff uint32
	r, _, _ := bsGetWindowDisplayAffinity.Call(hwnd, uintptr(unsafe.Pointer(&aff)))
	return r != 0 && aff == bsWdaExcludeFromCapture
}

func bsDestroyAll() {
	for _, w := range bsWindows {
		bsDestroyWindow.Call(w.hwnd)
	}
	if len(bsWindows) > 0 {
		log.Printf("blank screen: removed")
	}
	bsWindows = nil
}

// bsTick: topmost again (another topmost window may have come above), the
// capture exclusion still in place, the capture loop still beating.
func bsTick() {
	if !bsShown || len(bsWindows) == 0 {
		return
	}
	if blankBeatStale(time.Now()) {
		log.Printf("blank screen: the capture loop stopped beating — page removed (watchdog)")
		bsTeardown()
		bsShown = false
		blankActive.Store(false)
		notifyBlankAutoOff("watchdog")
		return
	}
	for _, w := range bsWindows {
		if !bsExcluded(w.hwnd) {
			log.Printf("blank screen: a window lost its capture exclusion — page removed")
			bsTeardown()
			bsShown = false
			blankActive.Store(false)
			notifyBlankAutoOff("failed")
			return
		}
	}
	// Cheap one-second fallback to the event hook: put the page back on top.
	bsReassertTopmost()
}

// bsMonitors: the physical rectangles of the monitors (the helper is
// per-monitor DPI aware). Window thread only (bsMonitorAcc).
func bsMonitors() []bsRect {
	bsMonitorAcc = nil
	bsMonitorCbOnce.Do(func() { bsMonitorCb = syscall.NewCallback(bsMonitorEnum) })
	bsEnumDisplayMonitors.Call(0, 0, bsMonitorCb, 0)
	out := bsMonitorAcc
	bsMonitorAcc = nil
	return out
}

// One callback for the process (Go callbacks are never freed: a new one per
// display change would leak towards the runtime's limit).
var (
	bsMonitorCbOnce sync.Once
	bsMonitorCb     uintptr
	bsMonitorAcc    []bsRect
)

func bsMonitorEnum(hmon, hdc, lprc, lparam uintptr) uintptr {
	mi := bsMonitorInfo{}
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := bsGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 {
		bsMonitorAcc = append(bsMonitorAcc, mi.rcMonitor)
	}
	return 1
}

func bsWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case bsWmNcHitTest:
		return bsHtTransparent
	case bsWmEraseBkgnd:
		return 1
	case bsWmDisplayChange:
		bsPostThreadMessageW.Call(uintptr(bsTid), bsWmRebuild, 0, 0)
		return 0
	case bsWmPaint:
		bsPaint(hwnd)
		return 0
	}
	r, _, _ := bsDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func bsFont(px int32, weight int) uintptr {
	face, _ := syscall.UTF16PtrFromString("Segoe UI")
	f, _, _ := bsCreateFontW.Call(uintptr(-px), 0, 0, 0, uintptr(weight), 0, 0, 0,
		1 /*DEFAULT_CHARSET*/, 0, 0, 5 /*CLEARTYPE_QUALITY*/, 0, uintptr(unsafe.Pointer(face)))
	return f
}

// bsDrawText draws s centred in [x0,x1] from y; returns the height used.
func bsDrawText(hdc uintptr, s string, font uintptr, color uint32, x0, x1, y int32, rtl bool) int32 {
	if s == "" {
		return 0
	}
	u, _ := syscall.UTF16FromString(s)
	flags := uintptr(bsDtCenter | bsDtWordBreak | bsDtNoPrefix)
	if rtl {
		flags |= bsDtRtlReading
	}
	old, _, _ := bsSelectObject.Call(hdc, font)
	bsSetTextColor.Call(hdc, uintptr(color))
	calc := bsRect{Left: x0, Top: y, Right: x1, Bottom: y}
	bsDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&calc)), flags|bsDtCalcRect)
	h := calc.Bottom - calc.Top
	rc := bsRect{Left: x0, Top: y, Right: x1, Bottom: y + h}
	bsDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&rc)), flags)
	bsSelectObject.Call(hdc, old)
	return h
}

func bsPaint(hwnd uintptr) {
	var ps bsPaintStruct
	hdc, _, _ := bsBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer bsEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var w, h int32
	for _, x := range bsWindows {
		if x.hwnd == hwnd {
			w, h = x.w, x.h
		}
	}
	if w <= 0 || h <= 0 {
		return
	}
	bsRender(hdc, w, h, bsText)
}

// bsRender draws the page (w×h) into hdc.
func bsRender(hdc uintptr, w, h int32, t blankScreenText) {
	bg, _, _ := bsCreateSolidBrush.Call(bsColorBg)
	full := bsRect{Right: w, Bottom: h}
	bsFillRect.Call(hdc, uintptr(unsafe.Pointer(&full)), bg)
	bsDeleteObject.Call(bg)

	unit := h
	if w < unit {
		unit = w
	}
	// Layout: logo, a short red line, the title, the message, the hint —
	// centred as a block slightly above the middle.
	logo, lw0, lh0 := bsLogo()
	lw := w * 2 / 5
	if lim := unit * 9 / 10; lw > lim {
		lw = lim
	}
	lh := int32(0)
	if logo != nil && lw0 > 0 {
		lh = lw * int32(lh0) / int32(lw0)
	}
	titlePx, bodyPx, hintPx := unit/18, unit/32, unit/48
	if titlePx < 20 {
		titlePx = 20
	}
	if bodyPx < 14 {
		bodyPx = 14
	}
	if hintPx < 12 {
		hintPx = 12
	}
	gap := unit / 28
	blockH := lh + gap + 4 + gap + titlePx*3/2 + gap/2 + bodyPx*3 + gap + hintPx*2
	y := (h-blockH)/2 - unit/20
	if y < 0 {
		y = 0
	}
	if logo != nil && lh > 0 {
		bi := bsBitmapInfoHeader{biWidth: int32(lw0), biHeight: -int32(lh0), biPlanes: 1, biBitCount: 32}
		bi.biSize = uint32(unsafe.Sizeof(bi))
		bsSetStretchBltMode.Call(hdc, bsHalftone)
		bsSetBrushOrgEx.Call(hdc, 0, 0, 0)
		bsStretchDIBits.Call(hdc, uintptr((w-lw)/2), uintptr(y), uintptr(lw), uintptr(lh),
			0, 0, uintptr(lw0), uintptr(lh0), uintptr(unsafe.Pointer(&logo[0])), uintptr(unsafe.Pointer(&bi)), 0, bsSrcCopy)
		y += lh + gap
	}
	line, _, _ := bsCreateSolidBrush.Call(bsColorLine)
	lr := bsRect{Left: w/2 - unit/20, Top: y, Right: w/2 + unit/20, Bottom: y + 4}
	bsFillRect.Call(hdc, uintptr(unsafe.Pointer(&lr)), line)
	bsDeleteObject.Call(line)
	y += 4 + gap

	bsSetBkMode.Call(hdc, bsTransparent)
	x0, x1 := w/8, w-w/8
	ft := bsFont(titlePx, 600)
	y += bsDrawText(hdc, t.Title, ft, bsColorTitle, x0, x1, y, t.RTL) + gap/2
	bsDeleteObject.Call(ft)
	fb := bsFont(bodyPx, 400)
	y += bsDrawText(hdc, t.Body, fb, bsColorBody, x0, x1, y, t.RTL) + gap
	bsDeleteObject.Call(fb)
	fh := bsFont(hintPx, 400)
	bsDrawText(hdc, t.Hint, fh, bsColorHint, x0, x1, y, t.RTL)
	bsDeleteObject.Call(fh)
}
