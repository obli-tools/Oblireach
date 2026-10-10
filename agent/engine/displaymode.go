package engine

// display_modes — the viewer's resolution selector (Oblireach Desktop session
// toolbar, Obliance web viewer). The operator raises (or lowers) the remote
// screen's resolution among the modes Windows offers for the monitor being
// streamed, without opening the remote Display settings.
//
// This file is PURE Go (no CGo, no build tag): the mode-list shaping, the
// request parsing and the original-mode restore bookkeeping are unit-tested
// (displaymode_test.go) with a fake display. The Win32 side (EnumDisplay-
// SettingsEx / ChangeDisplaySettingsEx, RDP detection, which monitor) lives in
// displaymode_windows.go; elsewhere the feature is not advertised.
//
// Wire shape (viewer → agent), only sent when init.features has
// "display_modes":
//
//	{"type":"display_modes_get","id":"<=32 [A-Za-z0-9_-]>"}
//	{"type":"display_mode_set","id":"...","w":1920,"h":1080,"hz":144,"keep":false}
//
// Answers (agent → viewer):
//
//	{"type":"display_modes","id":"...","monitor":0,"changeable":true,
//	 "current":{"w":1280,"h":720,"hz":60},
//	 "modes":[{"w":1920,"h":1080,"hz":144},{"w":1280,"h":720,"hz":60,"current":true},…]}
//	{"type":"display_modes","id":"...","changeable":false,"reason":"rdp"|"unavailable",…}
//	{"type":"display_mode_result","id":"...","ok":true}
//	{"type":"display_mode_result","id":"...","ok":false,"code":"rdp|invalid|
//	  not_listed|unavailable|session_changed|badmode|restart|failed","cds":<DISP_CHANGE_*>}
//
// The monitor changed is the one the stream shows (single-monitor view); in a
// mosaic (or the Magnification fallback, which captures the whole virtual
// screen) it is the PRIMARY monitor. A successful change re-sizes the capture,
// which already re-initialises on a display mode change (new init → the
// viewer asks for the list again and marks the new current mode).
//
// Restore at the end ("keep" = false, the default):
//   - the FIRST change this stream makes on a monitor remembers the mode it
//     had before (the original), per monitor;
//   - "keep" false applies the mode dynamically (no registry write); "keep"
//     true applies it with CDS_UPDATEREGISTRY. The LAST request's keep wins;
//   - when the stream ends (stop, viewer gone, service pipe broken → the
//     helper exits), the original is put back unless keep was asked — and only
//     if the monitor still shows the mode THIS stream applied.
//
// Several viewers: each viewer has its own capture helper (its own process,
// its own bookkeeping). "Still shows the mode this stream applied" is the whole
// multi-viewer rule — the last change wins, conservatively: a viewer that
// changed the mode and leaves restores it only when nobody (another viewer, or
// the local user) changed it after; otherwise the newer choice stays. Viewers
// stack naturally: B's "original" is the mode A applied, so B leaving puts A's
// mode back, then A leaving puts the true original back.
//
// RDP: an RDP session's resolution is the client window's — Windows refuses a
// mode change there. The list says changeable:false, reason "rdp", and a set
// is refused with code "rdp" (the viewers disable the control with a tooltip).

import (
	"bytes"
	"encoding/json"
	"sort"
	"sync"
)

// featDisplayModes is advertised in init.features where the selector works
// end to end (Windows capture helper, displayModesSupported).
const featDisplayModes = "display_modes"

// DISP_CHANGE_* (winuser.h) — the code a ChangeDisplaySettingsEx returns.
const (
	dispChangeSuccessful = 0
	dispChangeRestart    = 1
	dispChangeFailed     = -1
	dispChangeBadMode    = -2
)

// Modes below this are never offered (VGA-era modes a modern desktop cannot
// lay out its taskbar in).
const (
	displayMinW = 640
	displayMinH = 480
)

var displayModeMark = []byte(`display_mode`)

// IsDisplayModeMsg reports whether a viewer text frame is a display_modes_get
// or display_mode_set request (byte pre-check first: the relay sees every
// mouse move).
func IsDisplayModeMsg(payload []byte) bool {
	if !bytes.Contains(payload, displayModeMark) {
		return false
	}
	var peek struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &peek) != nil {
		return false
	}
	return peek.Type == "display_modes_get" || peek.Type == "display_mode_set"
}

// rawDisplayMode is one mode as Windows enumerates it.
type rawDisplayMode struct {
	W, H, Hz, Bpp int
	Orientation   int // DMDO_DEFAULT 0, DMDO_90 1, DMDO_180 2, DMDO_270 3
	Interlaced    bool
}

// displayMode is one entry of the list the viewer shows.
type displayMode struct {
	W       int  `json:"w"`
	H       int  `json:"h"`
	Hz      int  `json:"hz,omitempty"`
	Current bool `json:"current,omitempty"`
}

// rotatedPortrait: the current mode is turned a quarter (portrait). The
// enumerated modes are then expressed in the same orientation as the current
// one (width < height) so the list and the apply match what the screen shows.
func rotatedPortrait(cur rawDisplayMode) bool {
	return cur.Orientation == 1 || cur.Orientation == 3
}

// shapeDisplayModes turns what Windows enumerates into the viewer's list:
// the current bit depth only, no interlaced mode, nothing under 640×480, one
// entry per W×H with its highest refresh rate — except the current W×H, which
// keeps the current refresh rate (choosing it again changes nothing) and is
// marked current. Always contains the current mode. Largest first (width,
// then height), like the Windows Display settings list.
func shapeDisplayModes(raw []rawDisplayMode, cur rawDisplayMode) []displayMode {
	type key struct{ w, h int }
	best := map[key]int{}
	portrait := rotatedPortrait(cur)
	for _, m := range raw {
		if m.Interlaced {
			continue
		}
		if cur.Bpp > 0 && m.Bpp > 0 && m.Bpp != cur.Bpp {
			continue
		}
		w, h := m.W, m.H
		if portrait && w > h {
			w, h = h, w
		}
		if min(w, h) < displayMinH || max(w, h) < displayMinW {
			continue
		}
		k := key{w, h}
		if hz, ok := best[k]; !ok || m.Hz > hz {
			best[k] = m.Hz
		}
	}
	ck := key{cur.W, cur.H}
	if cur.W > 0 && cur.H > 0 {
		best[ck] = cur.Hz
	}
	out := make([]displayMode, 0, len(best))
	for k, hz := range best {
		out = append(out, displayMode{W: k.w, H: k.h, Hz: hz, Current: k == ck})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].W != out[j].W {
			return out[i].W > out[j].W
		}
		return out[i].H > out[j].H
	})
	return out
}

// displayModeSetReq is a parsed display_mode_set.
type displayModeSetReq struct {
	ID   string
	W, H int
	Hz   int // 0 = the listed refresh rate for W×H
	Keep bool
}

// parseDisplayModeSet validates a display_mode_set. ok is false for a
// malformed request (answered with code "invalid").
func parseDisplayModeSet(payload []byte) (displayModeSetReq, bool) {
	var raw struct {
		ID   string `json:"id"`
		W    int    `json:"w"`
		H    int    `json:"h"`
		Hz   int    `json:"hz"`
		Keep bool   `json:"keep"`
	}
	if json.Unmarshal(payload, &raw) != nil || !validTypeTextID(raw.ID) {
		return displayModeSetReq{}, false
	}
	if raw.W <= 0 || raw.H <= 0 || raw.W > 32768 || raw.H > 32768 || raw.Hz < 0 || raw.Hz > 1000 {
		return displayModeSetReq{}, false
	}
	return displayModeSetReq{ID: raw.ID, W: raw.W, H: raw.H, Hz: raw.Hz, Keep: raw.Keep}, true
}

// peekDisplayModeID reads only the id ("" when absent or malformed), so an
// answer can name a request that is otherwise invalid.
func peekDisplayModeID(payload []byte) string { return PeekTypeTextID(payload) }

// resolveDisplayTarget picks the mode to apply for a request: the W×H must be
// in the list the viewer was shown (shapeDisplayModes of the same raw modes);
// the refresh rate is the requested one when Windows offers it for that W×H
// at the current depth, else the listed one. The result carries the current
// bit depth and orientation (a resolution change never changes those).
func resolveDisplayTarget(raw []rawDisplayMode, cur rawDisplayMode, req displayModeSetReq) (rawDisplayMode, bool) {
	listed := shapeDisplayModes(raw, cur)
	var entry *displayMode
	for i := range listed {
		if listed[i].W == req.W && listed[i].H == req.H {
			entry = &listed[i]
			break
		}
	}
	if entry == nil {
		return rawDisplayMode{}, false
	}
	hz := entry.Hz
	if req.Hz > 0 && req.Hz != hz {
		portrait := rotatedPortrait(cur)
		for _, m := range raw {
			w, h := m.W, m.H
			if portrait && w > h {
				w, h = h, w
			}
			if w == req.W && h == req.H && m.Hz == req.Hz && !m.Interlaced &&
				(cur.Bpp <= 0 || m.Bpp <= 0 || m.Bpp == cur.Bpp) {
				hz = req.Hz
				break
			}
		}
	}
	return rawDisplayMode{W: req.W, H: req.H, Hz: hz, Bpp: cur.Bpp, Orientation: cur.Orientation}, true
}

// displayModeCode maps a DISP_CHANGE_* to the answer's code.
func displayModeCode(rc int) string {
	switch rc {
	case dispChangeSuccessful:
		return ""
	case dispChangeRestart:
		return "restart"
	case dispChangeBadMode:
		return "badmode"
	}
	return "failed"
}

// ── Answers ─────────────────────────────────────────────────────────────────

// displayModesInfo is the state the list answer reports.
type displayModesInfo struct {
	Monitor    int
	Changeable bool
	Reason     string // "rdp" | "unavailable" when !Changeable
	Current    *displayMode
	Modes      []displayMode
}

func displayModesMsg(id string, info displayModesInfo) []byte {
	m := map[string]interface{}{
		"type":       "display_modes",
		"monitor":    info.Monitor,
		"changeable": info.Changeable,
	}
	if id != "" {
		m["id"] = id
	}
	if !info.Changeable && info.Reason != "" {
		m["reason"] = info.Reason
	}
	if info.Current != nil {
		m["current"] = info.Current
	}
	if info.Modes == nil {
		info.Modes = []displayMode{}
	}
	m["modes"] = info.Modes
	b, _ := json.Marshal(m)
	return b
}

func displayModeResult(id string, ok bool, code string, cds int) []byte {
	m := map[string]interface{}{"type": "display_mode_result", "ok": ok}
	if id != "" {
		m["id"] = id
	}
	if !ok {
		if code == "" {
			code = "failed"
		}
		m["code"] = code
		if cds != 0 {
			m["cds"] = cds
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// DisplayRefusal answers a display_modes_get / display_mode_set that cannot
// reach a capture helper (no leg: a session switch in progress) with code.
func DisplayRefusal(payload []byte, code string) []byte {
	var peek struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &peek)
	id := peekDisplayModeID(payload)
	if peek.Type == "display_modes_get" {
		return displayModesMsg(id, displayModesInfo{Monitor: -1, Reason: code})
	}
	return displayModeResult(id, false, code, 0)
}

// ── Original-mode bookkeeping (restore at the end of the stream) ────────────

// displayOps is what the keeper needs from the display (Win32 in production,
// a fake in tests). apply returns a DISP_CHANGE_* code; persist = write the
// registry too (CDS_UPDATEREGISTRY), else the change is dynamic.
type displayOps interface {
	current(dev string) (rawDisplayMode, bool)
	apply(dev string, m rawDisplayMode, persist bool) int
}

type keptDisplay struct {
	original  rawDisplayMode // before this stream's first change
	applied   rawDisplayMode // the last mode this stream applied
	keep      bool           // the last request's keep
	persisted bool           // some change of this stream wrote the registry
}

// displayKeeper remembers, per monitor device, the mode to put back.
type displayKeeper struct {
	mu   sync.Mutex
	mons map[string]*keptDisplay
}

func sameResolution(a, b rawDisplayMode) bool { return a.W == b.W && a.H == b.H }

// set applies target on dev and records the original on the first success.
// Returns the DISP_CHANGE_* code of the apply.
func (k *displayKeeper) set(ops displayOps, dev string, target rawDisplayMode, keep bool) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	st := k.mons[dev]
	var before rawDisplayMode
	if st == nil {
		cur, ok := ops.current(dev)
		if !ok {
			return dispChangeFailed
		}
		before = cur
	}
	rc := ops.apply(dev, target, keep)
	if rc != dispChangeSuccessful {
		return rc
	}
	if st == nil {
		if k.mons == nil {
			k.mons = map[string]*keptDisplay{}
		}
		st = &keptDisplay{original: before}
		k.mons[dev] = st
	}
	st.applied = target
	st.keep = keep
	st.persisted = st.persisted || keep
	return rc
}

// restoreAll puts each changed monitor back to its original mode, unless the
// last request asked to keep it, or the monitor no longer shows the mode this
// stream applied (someone changed it after: the newer choice stays). Returns
// the devices restored. The bookkeeping is cleared either way.
func (k *displayKeeper) restoreAll(ops displayOps) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var restored []string
	devs := make([]string, 0, len(k.mons))
	for dev := range k.mons {
		devs = append(devs, dev)
	}
	sort.Strings(devs)
	for _, dev := range devs {
		st := k.mons[dev]
		if st.keep {
			continue
		}
		cur, ok := ops.current(dev)
		if !ok || !sameResolution(cur, st.applied) {
			continue
		}
		if sameResolution(cur, st.original) && cur.Hz == st.original.Hz && !st.persisted {
			continue // already the original (the operator went back to it)
		}
		if ops.apply(dev, st.original, st.persisted) == dispChangeSuccessful {
			restored = append(restored, dev)
		}
	}
	k.mons = nil
	return restored
}

// changed reports whether this stream changed a mode it may put back.
func (k *displayKeeper) changed() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.mons) > 0
}
