//go:build windows

package main

// Raw Win32 bindings used by the native UI (no WebView2, no common
// controls: every screen is painted with GDI, see ui_windows.go). DLLs are
// loaded from System32 only.

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll") // Windows 8.1+: looked up at run time only

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pShowWindow               = user32.NewProc("ShowWindow")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pInvalidateRect           = user32.NewProc("InvalidateRect")
	pBeginPaint               = user32.NewProc("BeginPaint")
	pEndPaint                 = user32.NewProc("EndPaint")
	pGetDC                    = user32.NewProc("GetDC")
	pReleaseDC                = user32.NewProc("ReleaseDC")
	pFillRect                 = user32.NewProc("FillRect")
	pDrawTextW                = user32.NewProc("DrawTextW")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pSetCursor                = user32.NewProc("SetCursor")
	pLoadImageW               = user32.NewProc("LoadImageW")
	pSetTimer                 = user32.NewProc("SetTimer")
	pKillTimer                = user32.NewProc("KillTimer")
	pSetCapture               = user32.NewProc("SetCapture")
	pReleaseCapture           = user32.NewProc("ReleaseCapture")
	pTrackMouseEvent          = user32.NewProc("TrackMouseEvent")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pFlashWindowEx            = user32.NewProc("FlashWindowEx")
	pGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	pAdjustWindowRectExForDpi = user32.NewProc("AdjustWindowRectExForDpi")
	pMonitorFromWindow        = user32.NewProc("MonitorFromWindow")
	pMonitorFromPoint         = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfoW          = user32.NewProc("GetMonitorInfoW")
	pOpenClipboard            = user32.NewProc("OpenClipboard")
	pCloseClipboard           = user32.NewProc("CloseClipboard")
	pEmptyClipboard           = user32.NewProc("EmptyClipboard")
	pSetClipboardData         = user32.NewProc("SetClipboardData")
	pRegisterHotKey           = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey         = user32.NewProc("UnregisterHotKey")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW          = user32.NewProc("CallWindowProcW")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pSetFocus                 = user32.NewProc("SetFocus")
	pGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	pIsIconic                 = user32.NewProc("IsIconic")
	pMoveWindow               = user32.NewProc("MoveWindow")
	pGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	pGetWindowTextW           = user32.NewProc("GetWindowTextW")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pGetStockObject         = gdi32.NewProc("GetStockObject")
	pRoundRect              = gdi32.NewProc("RoundRect")
	pEllipse                = gdi32.NewProc("Ellipse")
	pSetTextColor           = gdi32.NewProc("SetTextColor")
	pSetBkMode              = gdi32.NewProc("SetBkMode")
	pCreateFontW            = gdi32.NewProc("CreateFontW")
	pSaveDC                 = gdi32.NewProc("SaveDC")
	pRestoreDC              = gdi32.NewProc("RestoreDC")
	pIntersectClipRect      = gdi32.NewProc("IntersectClipRect")
	pSetBkColor             = gdi32.NewProc("SetBkColor")
	pGetTextFaceW           = gdi32.NewProc("GetTextFaceW")

	pGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	pGlobalAlloc              = kernel32.NewProc("GlobalAlloc")
	pGlobalLock               = kernel32.NewProc("GlobalLock")
	pGlobalUnlock             = kernel32.NewProc("GlobalUnlock")
	pGlobalFree               = kernel32.NewProc("GlobalFree")
	pRtlMoveMemory            = kernel32.NewProc("RtlMoveMemory")
	pGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")
	pLCIDToLocaleName         = kernel32.NewProc("LCIDToLocaleName")
	pSetDefaultDllDirectories = kernel32.NewProc("SetDefaultDllDirectories")
	pSetDllDirectoryW         = kernel32.NewProc("SetDllDirectoryW")

	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")

	// Fallbacks of the per-monitor DPI functions of Windows 10 1607+
	// (GetDpiForWindow, AdjustWindowRectExForDpi, GetSystemMetricsForDpi)
	// for Windows 7 / 8.1 / 2008 R2 / 2012 R2 (the nt6 build).
	pGetDpiForMonitor   = shcore.NewProc("GetDpiForMonitor")
	pGetDeviceCaps      = gdi32.NewProc("GetDeviceCaps")
	pAdjustWindowRectEx = user32.NewProc("AdjustWindowRectEx")
	pGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
)

const (
	wsOverlapped  = 0x00000000
	wsPopup       = 0x80000000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsClipChild   = 0x02000000

	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExAppWindow  = 0x00040000
	wsExNoActivate = 0x08000000

	wmCreate       = 0x0001
	wmDestroy      = 0x0002
	wmSize         = 0x0005
	wmActivate     = 0x0006
	wmPaint        = 0x000F
	wmClose        = 0x0010
	wmEraseBkgnd   = 0x0014
	wmSetCursor    = 0x0020
	wmNCHitTest    = 0x0084
	wmKeyDown      = 0x0100
	wmSysKeyDown   = 0x0104
	wmTimer        = 0x0113
	wmMouseMove    = 0x0200
	wmLButtonDown  = 0x0201
	wmLButtonUp    = 0x0202
	wmMouseLeave   = 0x02A3
	wmHotKey       = 0x0312
	wmDpiChanged   = 0x02E0
	wmApp          = 0x8000
	wmAppDispatch  = wmApp + 1
	wmMouseActive  = 0x0021
	wmSetFont      = 0x0030
	wmChar         = 0x0102
	wmMouseWheel   = 0x020A
	wmCtlColorEdit = 0x0133
	wmPrintClient  = 0x0318
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsTabStop      = 0x00010000
	esAutoHScroll  = 0x0080
	esRight        = 0x0002
	wsExRTLReading = 0x00002000
	emLimitText    = 0x00C5
	emSetMargins   = 0x00D3
	emSetCueBanner = 0x1501
	ecLeftMargin   = 0x0001
	ecRightMargin  = 0x0002
	flashwTray     = 0x00000002
	swMinimize     = 6
	gwlpWndProc    = ^uintptr(3) // GWLP_WNDPROC (-4)
	maNoActivate   = 3
	htClient       = 1
	htCaption      = 2
	swHide         = 0
	swShow         = 5
	swShowNA       = 8
	swShowNormal   = 1
	swRestore      = 9
	swpNoSize      = 0x0001
	swpNoMove      = 0x0002
	swpNoZOrder    = 0x0004
	swpNoActivate  = 0x0010
	swpShowWindow  = 0x0040
	hwndTopmost    = ^uintptr(0) // (HWND)-1
	idcArrow       = 32512
	idcHand        = 32649
	imageIcon      = 1
	lrDefaultColor = 0
	lrShared       = 0x00008000
	tmeLeave       = 0x00000002
	cfUnicodeText  = 13
	gmemMoveable   = 0x0002
	srcCopy        = 0x00CC0020
	nullPen        = 8
	transparent    = 1
	monitorPrimary = 1
	monitorNearest = 2
	smCxIcon       = 11
	smCxSmIcon     = 49
	flashwAll      = 0x00000003
	flashwTimerNF  = 0x0000000C
	mbIconError    = 0x00000010
	modAlt         = 0x0001
	modControl     = 0x0002
	modNoRepeat    = 0x4000
	vkF12          = 0x7B
	vkTab          = 0x09
	vkReturn       = 0x0D
	vkEscape       = 0x1B
	vkSpace        = 0x20
	vkShift        = 0x10

	dtLeft       = 0x0000
	dtCenter     = 0x0001
	dtRight      = 0x0002
	dtVCenter    = 0x0004
	dtWordBreak  = 0x0010
	dtSingleLine = 0x0020
	dtCalcRect   = 0x0400
	dtNoPrefix   = 0x0800
	dtEndEllip   = 0x8000
	dtEditCtrl   = 0x2000
	dtRTLReading = 0x20000

	fwNormal   = 400
	fwSemiBold = 600

	dwmwaDarkMode      = 20
	dwmwaDarkModeOld   = 19
	dwmwaCornerPref    = 33
	dwmwcpRound        = 2
	dwmwaCaptionColor  = 35
	dwmwaBorderColor   = 34
	dwmwaColorNone     = 0xFFFFFFFE
	loadLibSystem32    = 0x00000800
	loadLibDefaultDirs = 0x00001000
)

type wndClassEx struct {
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

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type rect struct{ L, T, R, B int32 }

func (r rect) w() int32 { return r.R - r.L }
func (r rect) h() int32 { return r.B - r.T }
func (r rect) has(x, y int32) bool {
	return x >= r.L && x < r.R && y >= r.T && y < r.B
}

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type trackMouseEvent struct {
	CbSize      uint32
	DwFlags     uint32
	HwndTrack   uintptr
	DwHoverTime uint32
}

type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	DwFlags   uint32
}

type flashInfo struct {
	CbSize    uint32
	Hwnd      uintptr
	DwFlags   uint32
	UCount    uint32
	DwTimeout uint32
}

func utf16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

func moduleHandle() uintptr {
	h, _, _ := pGetModuleHandleW.Call(0)
	return h
}

// hardenDLLSearch keeps the current directory and the executable's folder
// (typically Downloads, where a planted DLL may wait) out of the search
// path of every later LoadLibrary without a full path.
func hardenDLLSearch() {
	if pSetDefaultDllDirectories.Find() == nil {
		pSetDefaultDllDirectories.Call(loadLibSystem32)
	}
	if pSetDllDirectoryW.Find() == nil {
		pSetDllDirectoryW.Call(uintptr(unsafe.Pointer(utf16(""))))
	}
}

// windowsUILanguage returns the display language of this account ("fr-FR"),
// "" when Windows does not say.
func windowsUILanguage() string {
	if pGetUserDefaultUILanguage.Find() != nil || pLCIDToLocaleName.Find() != nil {
		return ""
	}
	id, _, _ := pGetUserDefaultUILanguage.Call()
	if id == 0 {
		return ""
	}
	var buf [85]uint16 // LOCALE_NAME_MAX_LENGTH
	n, _, _ := pLCIDToLocaleName.Call(id&0xFFFF, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

// rgb builds a COLORREF from 0xRRGGBB.
func rgb(c uint32) uint32 {
	return (c>>16)&0xFF | (c & 0xFF00) | (c&0xFF)<<16
}

// copyToClipboard puts text on the clipboard (CF_UNICODETEXT).
func copyToClipboard(owner uintptr, text string) bool {
	u, err := syscall.UTF16FromString(text)
	if err != nil {
		return false
	}
	// Another process may hold the clipboard for a moment: retry briefly.
	opened := false
	for i := 0; i < 10 && !opened; i++ {
		if r, _, _ := pOpenClipboard.Call(owner); r != 0 {
			opened = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !opened {
		return false
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	size := uintptr(len(u) * 2)
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return false
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return false
	}
	pRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	pGlobalUnlock.Call(h)
	if r, _, _ := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		pGlobalFree.Call(h)
		return false
	}
	return true
}

// messageBox shows a blocking error box (start-up failures only).
func messageBox(title, text string) {
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16(title))), mbIconError)
}

// darkFrame asks DWM for a dark title bar and, on Windows 11, rounded
// corners and no window border (the accent stays in the content).
func darkFrame(hwnd uintptr) {
	if pDwmSetWindowAttribute.Find() != nil {
		return
	}
	on := int32(1)
	if r, _, _ := pDwmSetWindowAttribute.Call(hwnd, dwmwaDarkMode, uintptr(unsafe.Pointer(&on)), 4); r != 0 {
		pDwmSetWindowAttribute.Call(hwnd, dwmwaDarkModeOld, uintptr(unsafe.Pointer(&on)), 4)
	}
	corner := int32(dwmwcpRound)
	pDwmSetWindowAttribute.Call(hwnd, dwmwaCornerPref, uintptr(unsafe.Pointer(&corner)), 4)
	caption := rgb(colBg)
	pDwmSetWindowAttribute.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&caption)), 4)
	border := uint32(dwmwaColorNone)
	pDwmSetWindowAttribute.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&border)), 4)
}

// windowDPI returns the DPI of the monitor the window is on. Before
// Windows 10 1607 (no GetDpiForWindow): the monitor's DPI from shcore on
// Windows 8.1 (as the process's awareness sees it: the system DPI for a
// system-aware process), else the system DPI (Windows 7, where the process
// is system-aware), else 96.
func windowDPI(hwnd uintptr) int32 {
	if pGetDpiForWindow.Find() == nil {
		if d, _, _ := pGetDpiForWindow.Call(hwnd); d != 0 {
			return int32(d)
		}
	}
	if pGetDpiForMonitor.Find() == nil {
		mon, _, _ := pMonitorFromWindow.Call(hwnd, monitorNearest)
		var x, y uint32
		if r, _, _ := pGetDpiForMonitor.Call(mon, 0 /* MDT_EFFECTIVE_DPI */, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y))); r == 0 && x != 0 {
			return int32(x)
		}
	}
	if hdc, _, _ := pGetDC.Call(0); hdc != 0 {
		d, _, _ := pGetDeviceCaps.Call(hdc, 88 /* LOGPIXELSX */)
		pReleaseDC.Call(0, hdc)
		if d != 0 {
			return int32(d)
		}
	}
	return 96
}

// workArea returns the work area of the monitor holding hwnd (the primary
// monitor when hwnd is 0).
func workArea(hwnd uintptr) rect {
	var mon uintptr
	if hwnd != 0 {
		mon, _, _ = pMonitorFromWindow.Call(hwnd, monitorNearest)
	} else {
		mon, _, _ = pMonitorFromPoint.Call(0, monitorPrimary)
	}
	mi := monitorInfo{}
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return rect{0, 0, 1280, 720}
	}
	return mi.RcWork
}

// outerSize converts a client size to a window size for a style at a DPI.
func outerSize(style, exStyle uintptr, dpi int32, cw, ch int32) (int32, int32) {
	r := rect{0, 0, cw, ch}
	if pAdjustWindowRectExForDpi.Find() == nil {
		pAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), style, 0, exStyle, uintptr(dpi))
	} else {
		// Before Windows 10 1607: the frame at the system DPI.
		pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), style, 0, exStyle)
	}
	return r.w(), r.h()
}

// appIcon loads the executable's icon at the size the system
// wants for a DPI, 0 when there is none.
func appIcon(small bool, dpi int32) uintptr {
	metric := uintptr(smCxIcon)
	if small {
		metric = smCxSmIcon
	}
	size := uintptr(32)
	if small {
		size = 16
	}
	if pGetSystemMetricsForDpi.Find() == nil {
		if s, _, _ := pGetSystemMetricsForDpi.Call(metric, uintptr(dpi)); s != 0 {
			size = s
		}
	} else if s, _, _ := pGetSystemMetrics.Call(metric); s != 0 {
		size = s // before Windows 10 1607: at the system DPI
	}
	// goversioninfo stores the icon group as resource 2 (1 holds the
	// version information); 1 is tried for other resource compilers.
	for _, id := range []uintptr{2, 1} {
		if h, _, _ := pLoadImageW.Call(moduleHandle(), id, imageIcon, size, size, lrDefaultColor); h != 0 {
			return h
		}
	}
	return 0
}

// uintptrOf passes a Go structure to a system call.
func uintptrOf[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

var sizeofFlash = unsafe.Sizeof(flashInfo{})
