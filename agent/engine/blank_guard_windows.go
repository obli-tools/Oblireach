//go:build windows

package engine

// Blank screen guard, Windows side (blank_guard.go has the decisions).
//
// Everything here runs for the capture helper, in the streamed session, on its
// Default desktop — the same process and thread that owns the page windows
// (blank_screen_windows.go). The WinEvent hooks are installed on that window
// thread so their out-of-context callbacks are delivered while it pumps
// messages; each relevant event wakes the thread (one coalesced message) to
// re-assert the page on top. Full-window drag is forced on while the page is
// up and put back when it goes down.
//
// Every user32 call is resolved lazily (no static import): the nt6 build links
// and starts unchanged and simply never shows the page, so none of this runs.

import (
	"log"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	bgSetWinEventHook      = bsUser32.NewProc("SetWinEventHook")
	bgUnhookWinEvent       = bsUser32.NewProc("UnhookWinEvent")
	bgSystemParametersInfo = bsUser32.NewProc("SystemParametersInfoW")
)

const (
	bgWineventOutOfContext   = 0x0000
	bgWineventSkipOwnProcess = 0x0002

	bgSpiGetDragFullWindows = 0x0026
	bgSpiSetDragFullWindows = 0x0025
)

// bgCoal coalesces the re-assert requests coming from the WinEvent callbacks.
var bgCoal bgCoalescer

// Owned by the window thread.
var (
	bgHooks      []uintptr
	bgProc       uintptr // the WinEvent callback (created once, never freed)
	bgDragForced bool
	bgDragOrig   bool
)

// bgWinEventCallback: a relevant window event may have put a window above the
// page. Drop the noise (child objects, cursors, carets) cheaply, then post one
// coalesced wake to the window thread; the heavy work (SetWindowPos per
// monitor) happens there, not in the callback, and a burst collapses to a
// single re-assert.
func bgWinEventCallback(hook, event, hwnd, idObject, idChild, idEventThread, dwmsEventTime uintptr) uintptr {
	if !bgEventRelevant(uint32(event), int32(idObject), int32(idChild)) {
		return 0
	}
	if bgCoal.request() {
		bsPostThreadMessageW.Call(uintptr(bsTid), bsWmReassert, 0, 0)
	}
	return 0
}

// bgInstallHooks subscribes to the window events while the page is up. Window
// thread only; idempotent (installed once, removed by bgRemoveHooks).
func bgInstallHooks() {
	if len(bgHooks) > 0 {
		return
	}
	if bgSetWinEventHook.Find() != nil {
		return // too old for SetWinEventHook: the one-second tick still covers it
	}
	if bgProc == 0 {
		bgProc = syscall.NewCallback(bgWinEventCallback)
	}
	for _, e := range bgHookedEvents {
		h, _, _ := bgSetWinEventHook.Call(uintptr(e), uintptr(e), 0, bgProc, 0, 0,
			bgWineventOutOfContext|bgWineventSkipOwnProcess)
		if h != 0 {
			bgHooks = append(bgHooks, h)
		}
	}
	if len(bgHooks) == 0 {
		log.Printf("blank screen: no window-event hook installed (the tick still re-asserts)")
	}
}

// bgRemoveHooks drops the subscriptions when the page goes down. Window thread
// only.
func bgRemoveHooks() {
	for _, h := range bgHooks {
		bgUnhookWinEvent.Call(h)
	}
	bgHooks = nil
	bgCoal.take() // clear any pending request so a stale wake is a no-op
}

// ── Full-window drag ─────────────────────────────────────────────────────────

func bgDragFullWindows() (on bool, ok bool) {
	if bgSystemParametersInfo.Find() != nil {
		return false, false
	}
	var v int32
	r, _, _ := bgSystemParametersInfo.Call(bgSpiGetDragFullWindows, 0, uintptr(unsafe.Pointer(&v)), 0)
	if r == 0 {
		return false, false
	}
	return v != 0, true
}

func bgSetDragFullWindows(on bool) {
	if bgSystemParametersInfo.Find() != nil {
		return
	}
	var v uintptr
	if on {
		v = 1
	}
	// uiParam carries the value; pvParam is NULL; fWinIni 0 => session only,
	// never persisted to the user's profile.
	bgSystemParametersInfo.Call(bgSpiSetDragFullWindows, v, 0, 0)
}

// bgForceDragFull turns full-window drag on for the duration of the page, after
// recording the value to put back. A marker left by a crashed helper (same
// session) wins over the live value: the live value would be the one that
// crash left on. Window thread only; idempotent.
func bgForceDragFull(sessionID int) {
	if bgDragForced {
		return
	}
	cur, ok := bgDragFullWindows()
	if !ok {
		return // cannot read it: leave the system untouched
	}
	orig := cur
	if m, have := bgReadDragMarker(sessionID); have {
		orig = m // a prior helper recorded the true original before it died
	} else {
		bgWriteDragMarker(sessionID, orig)
	}
	bgDragOrig = orig
	bgDragForced = true
	if bgNeedForceDrag(cur) {
		bgSetDragFullWindows(true)
		log.Printf("blank screen: full-window drag forced on (was off) to hide the drag outline")
	}
}

// bgRestoreDragFull puts the value back when the page goes down. Window thread
// only; idempotent.
func bgRestoreDragFull(sessionID int) {
	if !bgDragForced {
		return
	}
	bgSetDragFullWindows(bgDragOrig)
	bgDeleteDragMarker(sessionID)
	bgDragForced = false
}

// bgRecoverStaleDragMarker runs once at the start of a helper's stream: if a
// previous helper of this session died with full-window drag forced on, its
// marker still holds the real original — put it back and clear the marker.
// If this helper then shows the page it re-records from the (now correct)
// live value. Safe to call when disk writes are off (no marker was written).
func bgRecoverStaleDragMarker(sessionID int) {
	m, have := bgReadDragMarker(sessionID)
	if !have {
		return
	}
	bgSetDragFullWindows(m)
	bgDeleteDragMarker(sessionID)
	log.Printf("blank screen: recovered full-window drag from a previous session helper")
}

// ── Marker file (C:\ProgramData\OblireachAgent) ─────────────────────────────
// The service runs as the agent, under ProgramData: writing there is fine. The
// disk-write switch (diag.go) is honoured — the portable client leaves nothing
// behind, so it never writes a marker and so never recovers one.

func bgDragMarkerPath(sessionID int) string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "OblireachAgent", bgDragMarkerName(sessionID))
}

func bgReadDragMarker(sessionID int) (on bool, ok bool) {
	b, err := os.ReadFile(bgDragMarkerPath(sessionID))
	if err != nil {
		return false, false
	}
	return bgDecodeDragOriginal(b)
}

func bgWriteDragMarker(sessionID int, orig bool) {
	if !DiskWritesEnabled() {
		return
	}
	path := bgDragMarkerPath(sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, bgEncodeDragOriginal(orig), 0o644); err != nil {
		log.Printf("blank screen: could not write the drag-recovery marker: %v", err)
	}
}

func bgDeleteDragMarker(sessionID int) {
	_ = os.Remove(bgDragMarkerPath(sessionID))
}
