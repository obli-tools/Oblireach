//go:build windows

package main

// Windows key capture for the remote viewer in windowed mode.
//
// The webview never sees the Windows key: the shell consumes it first (Start
// menu, Win+R, Win+E...). While a session canvas has the focus (the page says
// so through reachKeyCapture) and its window is in the foreground, a
// low-level keyboard hook swallows LWIN / RWIN and hands them to that page
// (window.__reachNativeKey), which forwards them to the session.
//
// There is one hook for the whole app: with detached session windows
// (detached_windows.go) the key goes to whichever of our windows is in the
// foreground and captures it (window_registry.go), and its release goes to
// the window that got the press, even if the foreground changed meanwhile.
//
// Only these two keys are looked at: no other keystroke is read, kept or
// logged, and injected keys are left alone. A key whose press was swallowed
// also has its release swallowed, so the local shell never sees half a
// keystroke. The hook is installed on first use only, on its own thread
// (with its own message loop), so a busy UI thread never delays typing.

import (
	"log"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procSetWindowsHookExW   = modUser32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = modUser32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = modUser32.NewProc("UnhookWindowsHookEx")
	procGetMessageW         = modUser32.NewProc("GetMessageW")
	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
	procGetAncestor         = modUser32.NewProc("GetAncestor")
	procGetModuleHandleW    = modKernel32.NewProc("GetModuleHandleW")
)

const (
	whKeyboardLL  = 13
	wmKeyDown     = 0x0100
	wmSysKeyDown  = 0x0104
	vkLWin        = 0x5B
	vkRWin        = 0x5C
	llkhfInjected = 0x10
	gaRoot        = 2

	// A swallowed press whose release has not been seen for this long (ms)
	// is over: auto-repeat arrives every 30-1000 ms while the key is held.
	winKeyRepeatGap = 1500
)

// KBDLLHOOKSTRUCT
type kbdllHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// MSG
type winMsg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       [2]int32
	LPrivate uint32
}

type winKeyHook struct {
	// target: the window (bridge) the Windows key goes to right now, nil
	// when none of ours is in the foreground with its capture on.
	target      func() keyTarget
	swallowedBy [2]keyTarget // LWIN, RWIN: press swallowed for that window, release pending (hook thread only)
	lastSeen    [2]uint32    // time (ms) of the last swallowed event of that key (hook thread only)
}

var (
	keyHookOnce sync.Once
	keyHook     *winKeyHook
)

// decide says whether the hook swallows this event, and hands the swallowed
// Windows keys to their window (once per press: auto-repeat is swallowed
// silently). t is the event time (KBDLLHOOKSTRUCT.Time, ms).
func (h *winKeyHook) decide(vk, flags, t uint32, wParam uintptr) bool {
	if (vk != vkLWin && vk != vkRWin) || flags&llkhfInjected != 0 {
		return false
	}
	idx, code := 0, "MetaLeft"
	if vk == vkRWin {
		idx, code = 1, "MetaRight"
	}
	if wParam == wmKeyDown || wParam == wmSysKeyDown {
		if prev := h.swallowedBy[idx]; prev != nil {
			if t-h.lastSeen[idx] <= winKeyRepeatGap {
				h.lastSeen[idx] = t
				return true // auto-repeat of a press we already forwarded
			}
			// The release was never seen: it happened where a low-level hook
			// sees nothing (UAC secure desktop, Ctrl+Alt+Del, lock screen).
			// Close that press for its page; this one is a new press, judged
			// like any other (never swallowed outside a session).
			h.swallowedBy[idx] = nil
			prev.emitKey(false, code)
		}
		tgt := h.target()
		if tgt == nil {
			return false
		}
		h.swallowedBy[idx] = tgt
		h.lastSeen[idx] = t
		tgt.emitKey(true, code)
		return true
	}
	prev := h.swallowedBy[idx]
	if prev == nil {
		return false
	}
	// The release goes where the press went (the foreground may have changed).
	h.swallowedBy[idx] = nil
	prev.emitKey(false, code)
	return true
}

func (h *winKeyHook) proc(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) == 0 && lParam != 0 {
		kb := (*kbdllHookStruct)(globalPtr(lParam))
		if h.decide(kb.VkCode, kb.Flags, kb.Time, wParam) {
			return 1
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return r
}

func (h *winKeyHook) run() {
	runtime.LockOSThread() // the hook belongs to this thread and its message loop
	cb := syscall.NewCallback(h.proc)
	mod, _, _ := procGetModuleHandleW.Call(0)
	hk, _, err := procSetWindowsHookExW.Call(whKeyboardLL, cb, mod, 0)
	if hk == 0 {
		log.Printf("windows key capture disabled: %v", err)
		return
	}
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
	}
	procUnhookWindowsHookEx.Call(hk)
}

// foregroundRoot: the top-level window in the foreground (0 if none).
func foregroundRoot() uintptr {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return 0
	}
	root, _, _ := procGetAncestor.Call(fg, gaRoot)
	if root == 0 {
		return fg
	}
	return root
}

// keyCapture (JS binding reachKeyCapture): the page turns the capture on
// while a session canvas has the focus, off otherwise. Per window: the hook
// looks at the capture of the window in the foreground only.
func (b *nativeBridge) keyCapture(tok string, on bool) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	keyHookOnce.Do(func() {
		keyHook = &winKeyHook{target: bridges.keyTarget}
		go keyHook.run()
	})
	b.capture.Store(on)
	return on, nil
}

// (keyCaptureOn, emitKey, evalKey: native_events.go.)
