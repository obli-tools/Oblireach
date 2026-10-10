package main

// The app's top-level windows (the main window and the detached session
// windows, detached_windows.go), by native handle (HWND on Windows), with
// their native bridge; platform-neutral (the foreground window:
// foregroundRoot, per platform). Two
// things are global to the machine but must reach ONE of them:
//
//   - the Windows key (keyhook_windows.go): the low-level hook is a single
//     one; the key goes to the window in the foreground, if its page captures
//     it (a session canvas has the focus there);
//   - local clipboard copies (native_bridge.go watch): pushed to the page of
//     the window the operator worked in last, never to every open session at
//     once (the text may have been copied for another customer).

import "sync"

// regBridge is what the registry needs from a bridge (fakes in the tests).
type regBridge interface {
	keyCaptureOn() bool
	emitKey(down bool, code string)
}

type bridgeRegistry struct {
	mu         sync.Mutex
	byHwnd     map[uintptr]regBridge
	lastActive uintptr        // last of our windows seen in the foreground (0: none yet)
	foreground func() uintptr // root HWND of the foreground window, 0 if none
}

func newBridgeRegistry(foreground func() uintptr) *bridgeRegistry {
	return &bridgeRegistry{byHwnd: map[uintptr]regBridge{}, foreground: foreground}
}

// bridges is the process registry (foreground: keyhook_windows.go).
var bridges = newBridgeRegistry(foregroundRoot)

func (r *bridgeRegistry) register(hwnd uintptr, b regBridge) {
	if hwnd == 0 || b == nil {
		return
	}
	r.mu.Lock()
	r.byHwnd[hwnd] = b
	r.mu.Unlock()
}

func (r *bridgeRegistry) unregister(hwnd uintptr) {
	r.mu.Lock()
	delete(r.byHwnd, hwnd)
	if r.lastActive == hwnd {
		r.lastActive = 0
	}
	r.mu.Unlock()
}

func (r *bridgeRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byHwnd)
}

// noteForegroundLocked records the foreground window when it is one of ours,
// and returns it (0 when the foreground belongs to another application).
func (r *bridgeRegistry) noteForegroundLocked() uintptr {
	fg := r.foreground()
	if fg == 0 {
		return 0
	}
	if _, ok := r.byHwnd[fg]; !ok {
		return 0
	}
	r.lastActive = fg
	return fg
}

// keyTarget: the bridge of our window in the foreground, if its page captures
// the Windows key; nil otherwise (the key is left to the local shell).
// Called on the hook thread: a map lookup and two cheap Win32 calls.
func (r *bridgeRegistry) keyTarget() keyTarget {
	r.mu.Lock()
	fg := r.noteForegroundLocked()
	b := r.byHwnd[fg]
	r.mu.Unlock()
	if fg == 0 || b == nil || !b.keyCaptureOn() {
		return nil
	}
	return b
}

// clipboardTarget says whether a local clipboard change goes to the page of
// window hwnd: the window in the foreground, else the one of ours that was
// in the foreground last. With a single window (or before any of ours was
// seen in the foreground) every change goes to it, as before.
func (r *bridgeRegistry) clipboardTarget(hwnd uintptr) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noteForegroundLocked()
	if len(r.byHwnd) <= 1 || r.lastActive == 0 {
		return true
	}
	return r.lastActive == hwnd
}

// langBridge: a bridge whose page can be told the app's language changed
// (i18n.go syncLang; nativeBridge.emitLang).
type langBridge interface {
	emitLang(code string)
}

// broadcastLang tells every window of the app the language changed: each
// page redraws itself in it (the main window, the session windows).
func (r *bridgeRegistry) broadcastLang(code string) {
	for _, b := range r.list() {
		if lb, ok := b.(langBridge); ok {
			lb.emitLang(code)
		}
	}
}

// themeBridge: a bridge whose window can be told the app's theme changed
// (theme.go syncTheme; nativeBridge.emitTheme).
type themeBridge interface {
	emitTheme(theme string)
}

// broadcastTheme tells every window of the app the theme changed: each page
// wears it (the main window, the session windows, their viewer and dialogs)
// and so do the window's native colours.
func (r *bridgeRegistry) broadcastTheme(theme string) {
	for _, b := range r.list() {
		if tb, ok := b.(themeBridge); ok {
			tb.emitTheme(theme)
		}
	}
}

// serversBridge: a bridge whose page can be told the servers changed (the
// active one, the mode, the list: servers.go notifyServers).
type serversBridge interface {
	emitServers()
}

// broadcastServers tells every window the servers changed: each page reads
// them again (the session windows follow the mode and the active server).
func (r *bridgeRegistry) broadcastServers() {
	for _, b := range r.list() {
		if sb, ok := b.(serversBridge); ok {
			sb.emitServers()
		}
	}
}

// list: the registered bridges now (told outside the lock).
func (r *bridgeRegistry) list() []regBridge {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]regBridge, 0, len(r.byHwnd))
	for _, b := range r.byHwnd {
		list = append(list, b)
	}
	return list
}

// keyTarget is what the Windows key hook hands the key to.
type keyTarget interface {
	emitKey(down bool, code string)
}
