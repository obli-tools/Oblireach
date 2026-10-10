package engine

// Per-tile policy of the mosaic capture (every monitor in one frame). Pure Go
// so it is unit-tested without a GPU: capture_mosaic_windows.go implements
// mosaicTileBackend with DXGI Desktop Duplication + GDI, mosaic_tiles_test.go
// with a fake.
//
// Symptom: in mosaic view the PRIMARY monitor's tile stayed black
// (cursor drawn over it) while the other monitors displayed fine. Root cause:
// DXGI allows ONE Desktop Duplication of an output per process
// (IDXGIOutput1::DuplicateOutput returns E_INVALIDARG when "the calling
// application is already duplicating this desktop output"). enterMosaic
// duplicated every monitor while the single-monitor capture (native capture
// thread, same process) still held its duplication of the monitor it was
// showing — the primary, where a session starts. That DuplicateOutput failed,
// mos_add skipped the tile for good (never retried, never painted) and the
// composite kept zeros there; only the cursor overlay was drawn over it. Now:
//   - the output held by the single-monitor capture is duplicated only after
//     that capture released it (start);
//   - a tile whose duplication fails stays in the mosaic, is painted from a GDI
//     copy of its monitor and its duplication is retried with a backoff;
//   - a live duplication that has not shown a desktop image yet (idle output,
//     pointer-only updates, or the black images DXGI can hand out right after
//     DuplicateOutput) is seeded from GDI: from the first composite on no tile
//     is black;
//   - a lost duplication keeps its last image for a moment, then falls back to
//     GDI while other monitors still capture (when every duplication is lost it
//     is the secure desktop, which GDI cannot read either: the helper returns
//     to the single-monitor capture, which follows it).
// Every step is logged per tile so a test on a real machine says what happened.

import (
	"fmt"
	"strings"
	"time"
)

// mosaicGrab is the result of pulling the newest desktop image of one tile.
type mosaicGrab int

const (
	mosaicGrabNone mosaicGrab = iota // no new desktop image (nothing changed, pointer-only update)
	mosaicGrabNew                    // a new desktop image was copied into the tile's DXGI buffer (shown by showDXGI)
	mosaicGrabLost                   // the duplication died and was released: recreate it
)

// Pixel source a tile is painted from (mirrors MosOut.src on Windows).
const (
	mosaicSrcNone = 0 // nothing yet: the tile would be black
	mosaicSrcDXGI = 1 // last desktop image from the duplication
	mosaicSrcGDI  = 2 // GDI copy of the monitor
)

// mosaicTileBackend is the set of per-tile capture primitives. slot is the
// tile's slot in the native state (MosOut index).
type mosaicTileBackend interface {
	// open (re)creates the tile's duplication; detail describes the result
	// (mode on success, failing step + HRESULT otherwise).
	open(slot int) (ok bool, detail string)
	// grab copies the newest desktop image into the tile's DXGI buffer; it
	// does not change what the tile is painted from. detail explains a loss.
	grab(slot int) (res mosaicGrab, detail string)
	// blank reports whether the DXGI buffer looks entirely black (sampled).
	blank(slot int) bool
	// showDXGI paints the tile from its DXGI buffer from now on.
	showDXGI(slot int)
	// gdi copies the tile's monitor through GDI and paints the tile from that
	// copy from now on; false when it failed (the source is unchanged).
	gdi(slot int) bool
	// source is the mosaicSrc* the next composite paints for the tile.
	source(slot int) int
}

const (
	mosaicRetryMin     = 250 * time.Millisecond  // first retry of a failed / lost duplication
	mosaicRetryMax     = 2 * time.Second         // backoff ceiling (a D3D11 device per try)
	mosaicGDIEvery     = 100 * time.Millisecond  // GDI refresh of a tile without a duplication (~10 fps)
	mosaicSeedEvery    = 2 * time.Second         // GDI refresh while a live duplication has not shown an image
	mosaicKeepLastFor  = 1500 * time.Millisecond // a lost duplication shows its last image this long before GDI
	mosaicSummaryAfter = 3 * time.Second         // second status line (the first one is on the first composite)
	// DXGI can hand out a few black desktop images right after DuplicateOutput
	// (webrtc skips up to 5 before trusting a duplication). A black image is
	// not shown over the GDI seed until this many arrived since the (re)open:
	// after that the screen is believed to be really black.
	mosaicWarmupImages = 5
)

type mosaicTile struct {
	slot int
	tile reachTile

	live       bool      // the duplication is alive
	everLive   bool      // it has been alive at least once
	fails      int       // consecutive failed opens
	failsTotal int       // failed opens since the mosaic started
	lastFail   string    // detail of the last failed open (logged again only when it changes)
	nextOpen   time.Time // no open before this (backoff)
	downSince  time.Time // when the duplication went away (zero while live)
	openedAt   time.Time // last successful open

	fresh      bool // the duplication has shown a desktop image since its last open
	blanks     int  // black images not shown since the last open
	blankTotal int  // black images not shown since the mosaic started
	dxgiFrames int  // desktop images copied from the duplication
	gdiFrames  int  // GDI copies
	nextGDI    time.Time
	gdiReason  string // why the last GDI copy was made (logged when it changes)
	gdiFailed  bool   // a GDI failure was logged already
}

// mosaicTiles drives every tile of a running mosaic, once per frame (tick).
type mosaicTiles struct {
	be        mosaicTileBackend
	logf      func(format string, args ...interface{})
	tiles     []*mosaicTile
	started   time.Time
	summaries int
}

func newMosaicTiles(be mosaicTileBackend, logf func(string, ...interface{})) *mosaicTiles {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &mosaicTiles{be: be, logf: logf}
}

func (ts *mosaicTiles) add(slot int, t reachTile) {
	ts.tiles = append(ts.tiles, &mosaicTile{slot: slot, tile: t})
}

// name labels a tile like the viewer does ("screen 1 ★" = output 0, primary).
func (ts *mosaicTiles) name(t *mosaicTile) string {
	star := ""
	if t.tile.Primary {
		star = " ★"
	}
	d := t.tile.Desktop
	return fmt.Sprintf("screen %d%s (output %d, %dx%d at %d,%d)", t.tile.Index+1, star, t.tile.Index, d.W, d.H, d.X, d.Y)
}

// live is the number of tiles whose duplication is alive.
func (ts *mosaicTiles) live() int {
	n := 0
	for _, t := range ts.tiles {
		if t.live {
			n++
		}
	}
	return n
}

// start opens every duplication. held is the DXGI output the single-monitor
// capture holds a duplication of (-1 if none): DXGI refuses a second one in
// the same process, so that output is duplicated last, after release() freed
// it. Returns the number of live duplications (0 = the mosaic cannot start).
func (ts *mosaicTiles) start(now time.Time, held int, release func()) int {
	ts.started = now
	var heldTile *mosaicTile
	for _, t := range ts.tiles {
		if held >= 0 && t.tile.Index == held && heldTile == nil {
			heldTile = t
			continue
		}
		ts.open(t, now)
	}
	if heldTile != nil {
		ts.logf("mosaic: %s is held by the single-monitor capture (DXGI allows one duplication per output per process): releasing it first", ts.name(heldTile))
		if release != nil {
			release()
		}
		ts.open(heldTile, now)
	}
	return ts.live()
}

func (ts *mosaicTiles) open(t *mosaicTile, now time.Time) bool {
	ok, detail := ts.be.open(t.slot)
	if ok {
		switch {
		case t.fails > 0 && t.everLive:
			ts.logf("mosaic: %s: duplication re-created after %d failed attempt(s) (%s)", ts.name(t), t.fails, detail)
		case t.fails > 0:
			ts.logf("mosaic: %s: duplication created after %d failed attempt(s) (%s)", ts.name(t), t.fails, detail)
		case t.everLive:
			ts.logf("mosaic: %s: duplication re-created (%s)", ts.name(t), detail)
		default:
			ts.logf("mosaic: %s: DXGI duplication ok (%s)", ts.name(t), detail)
		}
		t.live, t.everLive = true, true
		t.fails, t.lastFail = 0, ""
		t.downSince = time.Time{}
		t.openedAt = now
		t.fresh, t.blanks = false, 0
		t.nextGDI = time.Time{} // seeded from GDI until its first image (fillGDI)
		return true
	}
	t.fails++
	t.failsTotal++
	if t.downSince.IsZero() {
		t.downSince = now
	}
	backoff := mosaicRetryMin << uint(min(t.fails-1, 8))
	if backoff > mosaicRetryMax {
		backoff = mosaicRetryMax
	}
	t.nextOpen = now.Add(backoff)
	if t.fails == 1 || detail != t.lastFail || t.fails%30 == 0 {
		ts.logf("mosaic: %s: DXGI duplication failed (%s), attempt %d — painted from GDI while another screen captures, next try in %v",
			ts.name(t), detail, t.fails, backoff)
	}
	t.lastFail = detail
	return false
}

// tick runs once per composite frame, before the tiles are painted: retries
// due duplications, pulls new desktop images and makes sure every tile has
// something to paint.
func (ts *mosaicTiles) tick(now time.Time) {
	anyLive := ts.live() > 0
	for _, t := range ts.tiles {
		if !t.live && !now.Before(t.nextOpen) {
			ts.open(t, now)
		}
		if t.live {
			ts.pull(t, now)
		}
		ts.fillGDI(t, now, anyLive)
	}
}

// pull takes the newest desktop image of a live duplication and decides
// whether the tile shows it.
func (ts *mosaicTiles) pull(t *mosaicTile, now time.Time) {
	res, detail := ts.be.grab(t.slot)
	switch res {
	case mosaicGrabNew:
		t.dxgiFrames++
		if !t.fresh {
			if t.blanks < mosaicWarmupImages && ts.be.blank(t.slot) {
				t.blanks++
				t.blankTotal++
				if t.blanks == 1 {
					ts.logf("mosaic: %s: the duplication handed out a black desktop image %v after it was created — not shown, the tile keeps its GDI copy until a real image (black is believed after %d)",
						ts.name(t), now.Sub(t.openedAt).Round(time.Millisecond), mosaicWarmupImages)
				}
				return
			}
			t.fresh = true
			note := ""
			switch {
			case t.blanks >= mosaicWarmupImages:
				note = fmt.Sprintf(" — still black after %d images: shown (the screen is probably really black)", t.blanks)
			case t.blanks > 0:
				note = fmt.Sprintf(" (after %d black one(s))", t.blanks)
			}
			ts.logf("mosaic: %s: DXGI image shown, %v after the duplication was created, %v after the mosaic started%s",
				ts.name(t), now.Sub(t.openedAt).Round(time.Millisecond), now.Sub(ts.started).Round(time.Millisecond), note)
		}
		ts.be.showDXGI(t.slot)
	case mosaicGrabLost:
		t.live, t.fresh = false, false
		t.downSince = now
		t.fails = 0
		t.nextOpen = now.Add(mosaicRetryMin)
		t.nextGDI = time.Time{}
		ts.logf("mosaic: %s: duplication lost (%s) — keeping its last image, retrying", ts.name(t), detail)
	}
}

// fillGDI paints a tile from GDI when DXGI gives it nothing to show.
func (ts *mosaicTiles) fillGDI(t *mosaicTile, now time.Time, anyLive bool) {
	// No duplication alive anywhere = secure desktop (lock, UAC, Ctrl+Alt+Del)
	// or a display reset: GDI reads black there. Keep the last images; the
	// helper returns to the single-monitor capture after mosaicLostGrace.
	if !anyLive {
		return
	}
	src := ts.be.source(t.slot)
	var every time.Duration
	var why string
	switch {
	case t.live && t.fresh:
		return // the duplication paints it
	case t.live:
		// Re-created duplication still painting its DXGI buffer: that image
		// is stale, or was just overwritten by a black image that is not
		// shown. Replace it now.
		if src == mosaicSrcDXGI {
			t.nextGDI = time.Time{}
		}
		every, why = mosaicSeedEvery, "no DXGI image shown yet"
	case src == mosaicSrcDXGI && now.Sub(t.downSince) < mosaicKeepLastFor:
		return // short desktop switch: the last image is better than a GDI copy
	default:
		every, why = mosaicGDIEvery, "no DXGI duplication"
	}
	if now.Before(t.nextGDI) {
		return
	}
	t.nextGDI = mosaicNextDue(t.nextGDI, now, every)
	if !ts.be.gdi(t.slot) {
		if !t.gdiFailed {
			t.gdiFailed = true
			ts.logf("mosaic: %s: the GDI copy failed too (%s) — the tile keeps what it shows", ts.name(t), why)
		}
		return
	}
	t.gdiFrames++
	if why != t.gdiReason {
		t.gdiReason = why
		ts.logf("mosaic: %s: painted from a GDI copy (%s)", ts.name(t), why)
	}
}

// mosaicNextDue schedules the next periodic copy after one due at prev (zero
// = none yet) ran at now. It keeps the period (a 100 ms refresh stays ~10 per
// second on a 33 ms frame tick) unless it fell a whole period behind.
func mosaicNextDue(prev, now time.Time, every time.Duration) time.Time {
	next := prev.Add(every)
	if prev.IsZero() || !next.After(now) {
		return now.Add(every)
	}
	return next
}

// maybeSummary logs one status line on the first composite and another one
// mosaicSummaryAfter later: per tile, the source painted, the counters and how
// many sampled pixels of the tile are not black (0/16 = a black tile).
func (ts *mosaicTiles) maybeSummary(now time.Time, frame []byte, w, h int) {
	switch {
	case ts.summaries == 0:
	case ts.summaries == 1 && now.Sub(ts.started) >= mosaicSummaryAfter:
	default:
		return
	}
	ts.summaries++
	parts := make([]string, 0, len(ts.tiles))
	for _, t := range ts.tiles {
		lit, n := mosaicLitSamples(frame, w, h, t.tile.Frame)
		state := "live"
		if !t.live {
			state = "no duplication"
		}
		parts = append(parts, fmt.Sprintf("%s: %s, painted from %s, %d DXGI image(s) (%d black not shown), %d GDI cop(ies), %d failed open(s), %d/%d sampled pixels not black",
			ts.name(t), state, mosaicSrcName(ts.be.source(t.slot)), t.dxgiFrames, t.blankTotal, t.gdiFrames, t.failsTotal, lit, n))
	}
	ts.logf("mosaic: status %v after start — %s", now.Sub(ts.started).Round(time.Millisecond), strings.Join(parts, " | "))
}

func mosaicSrcName(src int) string {
	switch src {
	case mosaicSrcDXGI:
		return "DXGI"
	case mosaicSrcGDI:
		return "GDI"
	}
	return "NOTHING (black)"
}

// mosaicLitSamples samples a 4x4 grid inside rect r of a BGRA frame (w x h)
// and returns how many samples are not black, and how many were taken.
func mosaicLitSamples(frame []byte, w, h int, r reachRect) (lit, n int) {
	for gy := 0; gy < 4; gy++ {
		for gx := 0; gx < 4; gx++ {
			x := r.X + (2*gx+1)*r.W/8
			y := r.Y + (2*gy+1)*r.H/8
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			o := (y*w + x) * 4
			if o+3 >= len(frame) {
				continue
			}
			n++
			if frame[o]|frame[o+1]|frame[o+2] != 0 {
				lit++
			}
		}
	}
	return lit, n
}

// mosaicStepName names the failing step of the native mos_open.
func mosaicStepName(step int) string {
	switch step {
	case -1:
		return "CreateDXGIFactory1"
	case -2:
		return "output not found"
	case -3:
		return "D3D11CreateDevice"
	case -4:
		return "IDXGIOutput1"
	case -5:
		return "DuplicateOutput"
	case -6:
		return "out of memory"
	case -7:
		return "bad slot"
	case -8:
		return "empty display mode"
	case -9:
		return "no DXGI Desktop Duplication on this Windows"
	}
	return fmt.Sprintf("step %d", step)
}

// dxgiHRText explains the HRESULTs DuplicateOutput / AcquireNextFrame return.
func dxgiHRText(hr uint32) string {
	switch hr {
	case 0:
		return "no HRESULT"
	case 0x80070057:
		return "E_INVALIDARG: this process already duplicates the output (or device/adapter mismatch)"
	case 0x80070005:
		return "E_ACCESSDENIED: no access to the desktop (secure desktop, locked or disconnected session)"
	case 0x887A002B:
		return "DXGI_ERROR_ACCESS_DENIED"
	case 0x887A0004:
		return "DXGI_ERROR_UNSUPPORTED: the output does not support desktop duplication (DisplayLink, some virtual displays)"
	case 0x887A0022:
		return "DXGI_ERROR_NOT_CURRENTLY_AVAILABLE: too many duplications, or a full-screen application"
	case 0x887A0026:
		return "DXGI_ERROR_ACCESS_LOST: desktop switch or display mode change"
	case 0x887A0027:
		return "DXGI_ERROR_WAIT_TIMEOUT"
	case 0x887A0028:
		return "DXGI_ERROR_SESSION_DISCONNECTED"
	case 0x887A0005:
		return "DXGI_ERROR_DEVICE_REMOVED"
	case 0x887A0006:
		return "DXGI_ERROR_DEVICE_HUNG"
	case 0x887A0007:
		return "DXGI_ERROR_DEVICE_RESET"
	case 0x887A0001:
		return "DXGI_ERROR_INVALID_CALL"
	case 0x887A0002:
		return "DXGI_ERROR_NOT_FOUND"
	case 0x887A0020:
		return "DXGI_ERROR_DRIVER_INTERNAL_ERROR"
	case 0x80004002:
		return "E_NOINTERFACE"
	case 0x8007000E:
		return "E_OUTOFMEMORY"
	}
	return "unknown HRESULT"
}
