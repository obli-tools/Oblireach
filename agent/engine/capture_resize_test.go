package engine

import (
	"testing"
	"time"
)

func TestResizeGateThrottles(t *testing.T) {
	var g resizeGate
	t0 := time.Unix(1_700_000_000, 0)
	if !g.allow(t0) {
		t.Fatal("first re-init must be allowed")
	}
	if g.allow(t0.Add(100 * time.Millisecond)) {
		t.Fatal("second re-init within resizeMinInterval must wait")
	}
	if g.allow(t0.Add(resizeMinInterval - time.Millisecond)) {
		t.Fatal("still inside the interval")
	}
	if !g.allow(t0.Add(resizeMinInterval)) {
		t.Fatal("re-init allowed again after the interval")
	}
	// A clock that went backwards never blocks the re-init for good.
	if !g.allow(t0) {
		t.Fatal("a backwards clock must not block the re-init")
	}
}

func TestGeomConfirmNeedsTwoAgreeingProbes(t *testing.T) {
	var c geomConfirm
	cur := reachRect{0, 0, 1920, 1080}
	newR := reachRect{0, 0, 1280, 720}
	other := reachRect{0, 0, 1600, 900}

	if c.observe(cur, cur) {
		t.Fatal("no difference, nothing to do")
	}
	if c.observe(cur, newR) {
		t.Fatal("one probe is not enough (the layout may be read mid-change)")
	}
	if !c.observe(cur, newR) {
		t.Fatal("two probes agreeing on the new rectangle confirm the change")
	}
	// The counter starts again after a confirmation.
	if c.observe(cur, newR) {
		t.Fatal("a confirmation restarts the count")
	}
	// The expected rectangle moved between probes: start again from it.
	c.reset()
	if c.observe(cur, newR) || c.observe(cur, other) {
		t.Fatal("probes that disagree on the new rectangle do not confirm")
	}
	if !c.observe(cur, other) {
		t.Fatal("two probes on the same new rectangle confirm")
	}
	// Back in agreement in between: the count is dropped.
	if c.observe(cur, newR) || c.observe(cur, cur) || c.observe(cur, newR) {
		t.Fatal("an agreeing probe in between resets the count")
	}
}

func TestGeomConfirmGoneOutput(t *testing.T) {
	var c geomConfirm
	cur := reachRect{1920, 0, 1920, 1080}
	gone := reachRect{}
	if c.observe(cur, gone) {
		t.Fatal("one probe is not enough")
	}
	if !c.observe(cur, gone) {
		t.Fatal("an output missing on two probes is a confirmed change")
	}
}

func TestGeomProbeInterval(t *testing.T) {
	if geomProbeInterval(true) != geomProbeFast || geomProbeInterval(false) != geomProbeSlow {
		t.Fatal("fallback captures are probed fast, DXGI slowly")
	}
	if geomProbeFast > time.Second {
		t.Fatal("GDI / Magnification must follow a mode change well within the 15 s \"Keep changes?\" countdown")
	}
}

func TestUpgradeBackoffSchedule(t *testing.T) {
	var b upgradeBackoff
	t0 := time.Unix(1_700_000_000, 0)
	if b.due(t0) {
		t.Fatal("the first call only arms the schedule")
	}
	if b.due(t0.Add(magUpgradeMin - time.Millisecond)) {
		t.Fatal("not before the first delay")
	}
	now := t0.Add(magUpgradeMin)
	if !b.due(now) {
		t.Fatal("first try after magUpgradeMin")
	}
	want := magUpgradeMin
	for i := 0; i < 10; i++ {
		b.failed(now, 50*time.Millisecond)
		want *= 2
		if want > magUpgradeMax {
			want = magUpgradeMax
		}
		if b.due(now.Add(want - time.Millisecond)) {
			t.Fatalf("try %d came before its %v delay", i, want)
		}
		now = now.Add(want)
		if !b.due(now) {
			t.Fatalf("try %d not due after %v", i, want)
		}
	}
	if b.delay != magUpgradeMax {
		t.Fatalf("delay capped at %v, got %v", magUpgradeMax, b.delay)
	}
	// The display changed: fast again.
	b.reset(now)
	if !b.due(now.Add(magUpgradeMin)) || b.due(now.Add(magUpgradeMin-time.Millisecond)) {
		t.Fatal("reset starts again from magUpgradeMin")
	}
	// A try that stalls the stream disables the retries for good.
	b.failed(now, magUpgradeSlow)
	if !b.disabled || b.due(now.Add(time.Hour)) {
		t.Fatal("a slow try disables further tries")
	}
	b.reset(now)
	if b.due(now.Add(time.Hour)) {
		t.Fatal("reset does not re-enable a disabled schedule")
	}
}

func TestCodecToReapply(t *testing.T) {
	cases := map[string]string{
		"":      "",
		"h264":  "",
		"bogus": "",
		"vp9":   "vp9",
		"h265":  "h265",
		"av1":   "av1",
		"jpeg":  "jpeg",
	}
	for in, want := range cases {
		if got := codecToReapply(in); got != want {
			t.Errorf("codecToReapply(%q) = %q, want %q", in, got, want)
		}
	}
}

// After a resolution change the re-published single view maps the viewer's
// frame pixels onto the NEW desktop rectangle: a click on Windows' "Keep
// changes?" button lands where the dialog is.
func TestSingleViewFollowsResize(t *testing.T) {
	before := singleTileView(0, 0, 0, 1920, 1080, 1, true)
	after := singleTileView(0, 0, 0, 1280, 720, 1, true)
	// "Keep changes" button of the 1280x720 dialog, bottom-right area.
	if x, y, ok := after.frameToDesktop(700, 460); !ok || x != 700 || y != 460 {
		t.Fatalf("click after the resize mapped to (%d,%d) ok=%v", x, y, ok)
	}
	if x, y, _ := after.frameToDesktop(1279, 719); x != 1279 || y != 719 {
		t.Fatalf("bottom-right pixel of the new mode mapped to (%d,%d)", x, y)
	}
	// With the stale view the same frame pixel would be sent elsewhere once the
	// viewer scales to the new picture: the view must change.
	if before.Tiles[0].Desktop == after.Tiles[0].Desktop {
		t.Fatal("the view must carry the new desktop rectangle")
	}

	// Secondary monitor: the primary on its left went 1920 → 1280 wide, so
	// the captured monitor moved in the layout without changing size.
	moved := singleTileView(1, 1280, 0, 1920, 1080, 1, false)
	if x, y, _ := moved.frameToDesktop(0, 0); x != 1280 || y != 0 {
		t.Fatalf("origin-only change: frame (0,0) → (%d,%d), want (1280,0)", x, y)
	}

	// Portrait after a rotation: the view is the rotated desktop rectangle
	// (the capture copies the texture upright).
	portrait := singleTileView(0, 0, 0, 1080, 1920, 2, true)
	if x, y, _ := portrait.frameToDesktop(1079, 1919); x != 1079 || y != 1919 {
		t.Fatalf("portrait bottom-right mapped to (%d,%d)", x, y)
	}
}
