package engine

// Blank screen guard: keeps the page above everything the operator opens and
// hides the move/size outline Windows draws on the screen itself.
//
// Two leaks were seen in the field while the page was up:
//   - context menus (#32768), tooltips, jump lists, toasts... are topmost
//     windows created AFTER the page: they come above it in the topmost band.
//     The window thread re-asserts the page on top on every relevant window
//     event (an out-of-context WinEvent hook: menu popup start, window shown,
//     z-order change, foreground change, move/size start), coalesced into at
//     most one re-assert per message-loop turn. The hook is purely
//     event-driven: no fast polling (the remote machine's CPU budget matters
//     more than a perfect page — the owner would rather the local user glimpse
//     something than have the agent weigh the machine down). The existing
//     one-second window tick already re-asserts as a cheap fallback.
//   - with "show window contents while dragging" off (servers, RDP), the
//     move/size outline is drawn straight on the screen, above every window.
//     While the page is up, full-window drag is forced on (in memory only,
//     never written to the user's profile) and put back afterwards. A helper
//     that dies with the page up leaves a marker (agent only, disk writes on)
//     that the next helper of the session reads to put the real value back.
//
// This file is PURE Go: the decisions, unit-tested everywhere
// (blank_guard_test.go); blank_guard_windows.go does the Win32 side.

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// WinEvent constants (winuser.h).
const (
	bgEventSystemForeground     = 0x0003
	bgEventSystemMenuPopupStart = 0x0006
	bgEventSystemMoveSizeStart  = 0x000A
	bgEventSystemMoveSizeEnd    = 0x000B
	bgEventSystemDragDropStart  = 0x000E
	bgEventSystemSwitchStart    = 0x0014
	bgEventObjectShow           = 0x8002
	bgEventObjectReorder        = 0x8004

	bgObjidWindow = 0 // OBJID_WINDOW
	bgChildidSelf = 0 // CHILDID_SELF
)

// bgHookedEvents: the events the window thread subscribes to (one hook per
// entry: the ranges are not contiguous, and a single [min,max] range would
// pull in far more than these).
var bgHookedEvents = []uint32{
	bgEventSystemForeground,
	bgEventSystemMenuPopupStart,
	bgEventSystemMoveSizeStart,
	bgEventSystemMoveSizeEnd,
	bgEventSystemDragDropStart,
	bgEventSystemSwitchStart,
	bgEventObjectShow,
	bgEventObjectReorder,
}

// bgEventRelevant: whether a WinEvent may have put a window above the page.
// EVENT_OBJECT_SHOW / EVENT_OBJECT_REORDER fire for carets, cursors, scrollbars
// and other child objects too (idObject OBJID_CURSOR = -9, OBJID_CARET = -8,
// a positive idChild for a control): only a top-level window (OBJID_WINDOW +
// CHILDID_SELF) can change the topmost band, so everything else is dropped
// before it costs a re-assert. The system events (foreground, menu popup,
// move/size, drag, task switch) are always about a top-level window.
func bgEventRelevant(event uint32, idObject, idChild int32) bool {
	switch event {
	case bgEventObjectShow, bgEventObjectReorder:
		return idObject == bgObjidWindow && idChild == bgChildidSelf
	case bgEventSystemForeground, bgEventSystemMenuPopupStart,
		bgEventSystemMoveSizeStart, bgEventSystemMoveSizeEnd,
		bgEventSystemDragDropStart, bgEventSystemSwitchStart:
		return true
	default:
		return false
	}
}

// bgCoalescer collapses a burst of z-order events into at most one pending
// re-assert. request() returns true only for the event that flips the flag
// from clear to set — the caller then posts a single wake message; the
// following events in the burst return false and post nothing. take() is
// called once the wake message is handled, just before the re-assert, so a
// new burst arriving during the re-assert schedules the next one.
//
// Pure, lock-free, unit-tested; the Windows WinEvent callback and the window
// thread's message loop drive it (both run on the one window thread, so the
// flag never actually contends, but atomics keep it honest).
type bgCoalescer struct {
	pending atomic.Bool
}

func (c *bgCoalescer) request() bool { return c.pending.CompareAndSwap(false, true) }

func (c *bgCoalescer) take() { c.pending.Store(false) }

// ── Full-window-drag recovery marker ────────────────────────────────────────

// bgDragMarkerName: the per-session marker file name (not the full path — the
// platform side prepends the data directory). Session-scoped so two sessions
// showing the page at once never fight over one file.
func bgDragMarkerName(sessionID int) string {
	return "blankguard-dragfull-" + strconv.Itoa(sessionID) + ".marker"
}

// bgEncodeDragOriginal / bgDecodeDragOriginal: the ORIGINAL full-window-drag
// value (what to put back), "1" on / "0" off.
func bgEncodeDragOriginal(on bool) []byte {
	if on {
		return []byte("1")
	}
	return []byte("0")
}

func bgDecodeDragOriginal(b []byte) (on bool, ok bool) {
	switch strings.TrimSpace(string(b)) {
	case "1":
		return true, true
	case "0":
		return false, true
	default:
		return false, false
	}
}

// bgNeedForceDrag: given the current full-window-drag value, whether the guard
// must turn it on (the outline is drawn on screen only when it is off).
func bgNeedForceDrag(current bool) bool { return !current }
