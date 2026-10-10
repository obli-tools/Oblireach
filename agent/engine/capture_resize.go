package engine

// Display mode changes under a running capture (symptom: "the resolution
// changed: the picture froze, or the stream ended, and I could not click
// 'Keep changes'"). Pure Go, no build tag, so the policy is unit-tested without
// a GPU (capture_resize_test.go); the Windows capture (capture_windows.go) and
// the capture helper loop (remote_session_windows.go) apply it.
//
// What happens on Windows when the resolution, orientation or layout of the
// remote screens changes while a viewer watches one monitor:
//   - DXGI: AcquireNextFrame returns ACCESS_LOST; the native capture thread
//     re-initialises at the new geometry (cap_recover: DXGI retried for about a
//     second on the same output, then GDI, which keeps trying to get DXGI back —
//     GDI is never sticky any more) and bumps its geometry sequence;
//   - GDI / Magnification: nothing signals the change, so a probe compares the
//     capture rectangle with what Windows reports (geomConfirm: two probes that
//     agree), and asks for a re-init;
//   - the helper then re-publishes the single-monitor view instead of ending the
//     stream (shrink) or freezing it (grow): new encoder at the new size, a new
//     init (size, view, gen) that both viewers already follow, input mapping and
//     virtual-screen cache refreshed, and the codec the operator picked applied
//     again (every init restarts on H.264: codecToReapply).
// The "Keep these display settings?" countdown (15 s, SystemSettings on the
// Default desktop) is therefore visible and clickable about a second after the
// change. Nothing is ever confirmed automatically.
//
// The mosaic view already re-lays itself out on a layout change
// (errMosaicLayout); only the single-monitor view needed this.

import (
	"errors"
	"time"
)

// HelperDXGIWait is set from the helper's --dxgi-wait flag (milliseconds). A
// helper spawned by a session SWITCH keeps retrying DXGI Desktop Duplication
// this long before captureInit falls back to Magnification / GDI: right after a
// sign-in the Welcome screen still owns the input desktop and DXGI can refuse
// for a moment, and the Magnification fallback is the weakest capture path.
// 0 (the first leg, a stray helper) keeps the historical start.
var HelperDXGIWait time.Duration

// errCaptureResized: the frame the capture holds no longer has the size the
// stream was started with (the display mode changed). The helper re-publishes
// the view at the new size; the direct capture path (stream.go) reconnects.
var errCaptureResized = errors.New("capture: the display geometry changed")

const (
	// At most one re-init (a new init to the viewer, a new encoder) per
	// interval: a mode change can move the capture twice in a row (GDI while
	// the driver settles, then DXGI again).
	resizeMinInterval = 250 * time.Millisecond
	// Geometry probe period. GDI and Magnification get no signal at all when
	// the display changes; DXGI gets ACCESS_LOST, so there the probe is only a
	// safety net (it opens a DXGI factory each time).
	geomProbeFast = 500 * time.Millisecond
	geomProbeSlow = 2 * time.Second
	// After asking the capture thread for a re-init, the probe waits this long
	// before asking again (a re-init that cannot change the geometry must not
	// loop).
	geomReinitCooldown = 2 * time.Second
	// Magnification fallback → DXGI: retries with a backoff (each try opens
	// every output); a try slower than magUpgradeSlow stalls the stream, so
	// there is no further try in that session.
	magUpgradeMin  = 2 * time.Second
	magUpgradeMax  = 30 * time.Second
	magUpgradeSlow = 1500 * time.Millisecond
)

// geomProbeInterval is the geometry probe period: fast when the capture runs on
// a fallback (GDI, Magnification) that no event tells about a mode change.
func geomProbeInterval(fallback bool) time.Duration {
	if fallback {
		return geomProbeFast
	}
	return geomProbeSlow
}

// resizeGate throttles the re-inits of the view (resizeMinInterval).
type resizeGate struct {
	last time.Time
}

// allow reports whether a re-init may run now, and records it when it may.
func (g *resizeGate) allow(now time.Time) bool {
	if !g.last.IsZero() {
		if d := now.Sub(g.last); d >= 0 && d < resizeMinInterval {
			return false
		}
	}
	g.last = now
	return true
}

// geomConfirm debounces the geometry probe: the capture rectangle and the one
// Windows reports must differ on two consecutive probes, with the same expected
// rectangle, before anything is done. The layout can be read in the middle of a
// change, and the capture thread may already be re-initialising. A zero
// expected rectangle means "the output is gone" and counts as a difference.
type geomConfirm struct {
	want reachRect
	hits int
}

// observe feeds one probe; true when the difference is confirmed (the counter
// then starts again).
func (c *geomConfirm) observe(current, expected reachRect) bool {
	if current == expected {
		c.hits = 0
		return false
	}
	if c.hits > 0 && c.want == expected {
		c.hits++
	} else {
		c.want, c.hits = expected, 1
	}
	if c.hits >= 2 {
		c.hits = 0
		return true
	}
	return false
}

func (c *geomConfirm) reset() { c.hits = 0 }

// upgradeBackoff schedules the tries to leave the Magnification fallback for
// DXGI: magUpgradeMin, doubling up to magUpgradeMax; reset (display changed)
// starts again from the minimum; one try slower than magUpgradeSlow disables
// it for good (the helper would freeze the stream on every try).
type upgradeBackoff struct {
	next     time.Time
	delay    time.Duration
	disabled bool
}

// reset restarts the schedule from the shortest delay (kept disabled if it is).
func (b *upgradeBackoff) reset(now time.Time) {
	b.delay = magUpgradeMin
	b.next = now.Add(b.delay)
}

// due reports whether a try should run now. The first call only arms the
// schedule (no try the instant the fallback starts: captureInit just failed).
func (b *upgradeBackoff) due(now time.Time) bool {
	if b.disabled {
		return false
	}
	if b.next.IsZero() {
		b.reset(now)
		return false
	}
	return !now.Before(b.next)
}

// failed records a try that did not get DXGI back and how long it took.
func (b *upgradeBackoff) failed(now time.Time, took time.Duration) {
	if took >= magUpgradeSlow {
		b.disabled = true
		return
	}
	b.delay *= 2
	if b.delay < magUpgradeMin {
		b.delay = magUpgradeMin
	}
	if b.delay > magUpgradeMax {
		b.delay = magUpgradeMax
	}
	b.next = now.Add(b.delay)
}

// codecToReapply: every init restarts the encoder on H.264 — the viewers reset
// their decoder to H.264 on each init and expect the codec the operator picked
// to come back as a codec_switch. Returns that codec, or "" when there is
// nothing to switch back to (H.264, nothing picked, or an unknown name).
func codecToReapply(sticky string) string {
	switch sticky {
	case "vp9", "h265", "av1", "jpeg":
		return sticky
	}
	return ""
}
