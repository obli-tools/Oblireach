package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeMosaicOutput emulates one DXGI output as the mosaic sees it.
type fakeMosaicOutput struct {
	heldBySingle bool   // the single-monitor capture holds its duplication (DXGI: one per output per process)
	openErr      string // non-empty: DuplicateOutput keeps failing with it
	idle         bool   // the duplication delivers no desktop image
	lose         bool   // the next grab reports DXGI_ERROR_ACCESS_LOST
	blackImages  int    // the next N desktop images are black (DXGI right after DuplicateOutput)
	reallyBlack  bool   // the screen is black: every desktop image is
	gdiFails     bool

	dup      bool
	buf      bool // the DXGI buffer holds an image
	bufBlack bool // ... a black one
	src      int
	opens    int
	grabs    int
	gdis     int
}

type fakeMosaicBackend struct {
	outs  []*fakeMosaicOutput
	calls []string // "open N" / "release", in order
}

func (f *fakeMosaicBackend) open(slot int) (bool, string) {
	o := f.outs[slot]
	o.opens++
	f.calls = append(f.calls, fmt.Sprintf("open %d", slot))
	if o.heldBySingle {
		return false, "DuplicateOutput hr=0x80070057 " + dxgiHRText(0x80070057)
	}
	if o.openErr != "" {
		return false, o.openErr
	}
	o.dup = true
	return true, "mode 1920x1080, rotation 1"
}

// grab fills the DXGI buffer; like mos_grab it never changes the source.
func (f *fakeMosaicBackend) grab(slot int) (mosaicGrab, string) {
	o := f.outs[slot]
	o.grabs++
	if !o.dup {
		return mosaicGrabLost, "no duplication"
	}
	if o.lose {
		o.lose, o.dup = false, false
		return mosaicGrabLost, "hr=0x887A0026 " + dxgiHRText(0x887A0026)
	}
	if o.idle {
		return mosaicGrabNone, ""
	}
	o.buf = true
	o.bufBlack = o.reallyBlack || o.blackImages > 0
	if o.blackImages > 0 {
		o.blackImages--
	}
	return mosaicGrabNew, ""
}

func (f *fakeMosaicBackend) blank(slot int) bool { return !f.outs[slot].buf || f.outs[slot].bufBlack }

func (f *fakeMosaicBackend) showDXGI(slot int) {
	if f.outs[slot].buf {
		f.outs[slot].src = mosaicSrcDXGI
	}
}

func (f *fakeMosaicBackend) gdi(slot int) bool {
	o := f.outs[slot]
	o.gdis++
	if o.gdiFails {
		return false
	}
	o.src = mosaicSrcGDI
	return true
}

// paintsBlack: what the next composite paints for the tile is black.
func (f *fakeMosaicBackend) paintsBlack(slot int) bool {
	o := f.outs[slot]
	switch o.src {
	case mosaicSrcDXGI:
		return o.bufBlack && !o.reallyBlack
	case mosaicSrcGDI:
		return false
	}
	return true
}

func (f *fakeMosaicBackend) source(slot int) int { return f.outs[slot].src }

// A typical three-monitor layout: 3 | 1★ | 2 — output 0 is the primary in the middle, and
// taller than the side screens, which are vertically offset.
func threeMonitorMosaic(t *testing.T) (*mosaicTiles, *fakeMosaicBackend, *[]string) {
	t.Helper()
	view := layoutMosaic([]MonitorInfo{
		mon(0, 0, 0, 2560, 1440, true),
		mon(1, 2560, 180, 1920, 1080, false),
		mon(2, -1920, 180, 1920, 1080, false),
	}, agentClampBudget(0, 0, 0))
	if len(view.Tiles) != 3 {
		t.Fatalf("layout: %+v", view)
	}
	be := &fakeMosaicBackend{}
	logs := &[]string{}
	ts := newMosaicTiles(be, func(f string, a ...interface{}) { *logs = append(*logs, fmt.Sprintf(f, a...)) })
	for i, tile := range view.Tiles {
		be.outs = append(be.outs, &fakeMosaicOutput{})
		ts.add(i, tile)
	}
	return ts, be, logs
}

func logged(logs []string, sub string) bool {
	for _, l := range logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func assertNoBlackTile(t *testing.T, ts *mosaicTiles, when string) {
	t.Helper()
	for _, tile := range ts.tiles {
		if ts.be.source(tile.slot) == mosaicSrcNone {
			t.Fatalf("%s: %s has nothing to paint (black tile)", when, ts.name(tile))
		}
	}
}

// Regression (primary tile black): the single-monitor capture holds the
// primary's duplication when the mosaic starts. The primary must be
// duplicated only after that capture was released, and must not stay black.
func TestMosaicPrimaryHeldBySingleCapture(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	be.outs[0].heldBySingle = true
	release := func() {
		be.calls = append(be.calls, "release")
		be.outs[0].heldBySingle = false
	}
	t0 := time.Unix(1000, 0)
	if live := ts.start(t0, 0, release); live != 3 {
		t.Fatalf("live duplications = %d, want 3 (calls %v)", live, be.calls)
	}
	want := []string{"open 1", "open 2", "release", "open 0"}
	if strings.Join(be.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v (the held output after the release)", be.calls, want)
	}
	if be.outs[0].opens != 1 {
		t.Fatalf("the primary was opened %d times, want exactly once (no doomed attempt)", be.outs[0].opens)
	}
	ts.tick(t0.Add(33 * time.Millisecond))
	assertNoBlackTile(t, ts, "first frame")
	for _, o := range be.outs {
		if o.src != mosaicSrcDXGI || o.gdis != 0 {
			t.Fatalf("every tile should come from DXGI without GDI: %+v", *o)
		}
	}
	if !logged(*logs, "screen 1 ★ (output 0") || !logged(*logs, "held by the single-monitor capture") || !logged(*logs, "DXGI image shown") {
		t.Fatalf("missing per-tile logs: %q", *logs)
	}
}

// Before the fix a tile whose DuplicateOutput failed was skipped for good. If
// the held output is not detected (or it fails for another reason), the tile
// must be painted from GDI at once and recover when the duplication works.
func TestMosaicFailedTileIsPaintedThenRecovers(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	be.outs[0].heldBySingle = true
	t0 := time.Unix(1000, 0)
	if live := ts.start(t0, -1, nil); live != 2 {
		t.Fatalf("live = %d, want 2", live)
	}
	now := t0.Add(33 * time.Millisecond)
	ts.tick(now)
	assertNoBlackTile(t, ts, "first frame")
	if be.outs[0].src != mosaicSrcGDI {
		t.Fatalf("the failed primary must be painted from GDI, src=%d", be.outs[0].src)
	}
	if !logged(*logs, "E_INVALIDARG") {
		t.Fatalf("the failure HRESULT must be logged: %q", *logs)
	}
	// The single capture goes away (enterMosaic's captureStopSingle).
	be.outs[0].heldBySingle = false
	opensBefore := be.outs[0].opens
	for i := 0; i < 3; i++ { // 33..133 ms: still inside the 250 ms backoff
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if be.outs[0].opens != opensBefore {
		t.Fatalf("retried inside the backoff (%d opens)", be.outs[0].opens)
	}
	for i := 0; i < 6; i++ {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if !ts.tiles[0].live || be.outs[0].src != mosaicSrcDXGI {
		t.Fatalf("the primary did not come back on DXGI: live=%v src=%d", ts.tiles[0].live, be.outs[0].src)
	}
	if ts.live() != 3 {
		t.Fatalf("live = %d", ts.live())
	}
	if !logged(*logs, "duplication created after 1 failed attempt(s)") {
		t.Fatalf("recovery not logged: %q", *logs)
	}
}

// An idle output (no desktop image after DuplicateOutput, only pointer moves)
// is seeded from GDI instead of staying black, then DXGI takes over.
func TestMosaicIdleOutputSeededFromGDI(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	be.outs[0].idle = true
	t0 := time.Unix(1000, 0)
	ts.start(t0, -1, nil)
	now := t0
	ts.tick(now)
	assertNoBlackTile(t, ts, "first frame")
	if be.outs[0].src != mosaicSrcGDI || be.outs[0].gdis != 1 {
		t.Fatalf("idle primary: src=%d gdis=%d, want a GDI seed", be.outs[0].src, be.outs[0].gdis)
	}
	for now.Sub(t0) < mosaicSeedEvery-50*time.Millisecond {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if be.outs[0].gdis != 1 {
		t.Fatalf("seed refreshed too often: %d GDI copies", be.outs[0].gdis)
	}
	now = t0.Add(mosaicSeedEvery)
	ts.tick(now)
	if be.outs[0].gdis != 2 {
		t.Fatalf("seed not refreshed after %v: %d GDI copies", mosaicSeedEvery, be.outs[0].gdis)
	}
	be.outs[0].idle = false
	now = now.Add(33 * time.Millisecond)
	ts.tick(now)
	if be.outs[0].src != mosaicSrcDXGI {
		t.Fatal("DXGI must take over at its first image")
	}
	for i := 0; i < 200; i++ {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if be.outs[0].gdis != 2 {
		t.Fatalf("GDI kept running once DXGI delivered: %d copies", be.outs[0].gdis)
	}
	if !logged(*logs, "no DXGI image shown yet") {
		t.Fatalf("seed reason not logged: %q", *logs)
	}
}

// An output DXGI can never duplicate (DisplayLink, some virtual displays):
// painted from GDI at ~10 fps, retried with a capped backoff (a D3D11 device
// per try), not on every frame.
func TestMosaicUnsupportedOutputBackoffAndGDI(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	be.outs[1].openErr = "DuplicateOutput hr=0x887A0004 " + dxgiHRText(0x887A0004)
	t0 := time.Unix(1000, 0)
	if live := ts.start(t0, -1, nil); live != 2 {
		t.Fatalf("live = %d", live)
	}
	now := t0
	for now.Sub(t0) < 10*time.Second {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	o := be.outs[1]
	if o.opens < 5 || o.opens > 9 {
		t.Fatalf("%d opens in 10 s, want a 250 ms..2 s backoff (5..9)", o.opens)
	}
	if o.gdis < 90 || o.gdis > 101 {
		t.Fatalf("%d GDI copies in 10 s, want ~100 (every %v)", o.gdis, mosaicGDIEvery)
	}
	if o.src != mosaicSrcGDI {
		t.Fatalf("src=%d", o.src)
	}
	n := 0
	for _, l := range *logs {
		if strings.Contains(l, "DXGI_ERROR_UNSUPPORTED") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the same failure must be logged once, got %d lines", n)
	}
}

// A duplication lost while the others still capture (mode change on that
// screen): its last image stays for mosaicKeepLastFor, then GDI, then DXGI
// again once it can be re-created.
func TestMosaicLostKeepsLastImageThenGDI(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	t0 := time.Unix(1000, 0)
	ts.start(t0, -1, nil)
	now := t0
	ts.tick(now)
	be.outs[2].lose = true
	be.outs[2].openErr = "DuplicateOutput hr=0x80070005 " + dxgiHRText(0x80070005)
	now = now.Add(33 * time.Millisecond)
	ts.tick(now)
	lostAt := now
	if ts.tiles[2].live || !logged(*logs, "duplication lost") {
		t.Fatalf("loss not handled: live=%v logs=%q", ts.tiles[2].live, *logs)
	}
	for now.Sub(lostAt) < mosaicKeepLastFor-50*time.Millisecond {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if be.outs[2].gdis != 0 || be.outs[2].src != mosaicSrcDXGI {
		t.Fatalf("the last image must stay first: gdis=%d src=%d", be.outs[2].gdis, be.outs[2].src)
	}
	for now.Sub(lostAt) < mosaicKeepLastFor+100*time.Millisecond {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if be.outs[2].src != mosaicSrcGDI {
		t.Fatalf("GDI must take over after %v, src=%d", mosaicKeepLastFor, be.outs[2].src)
	}
	be.outs[2].openErr = ""
	for i := 0; i < 90 && !ts.tiles[2].live; i++ { // backoff ≤ 2 s
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if !ts.tiles[2].live || be.outs[2].src != mosaicSrcDXGI {
		t.Fatalf("not re-created: live=%v src=%d", ts.tiles[2].live, be.outs[2].src)
	}
	if !logged(*logs, "re-created after") {
		t.Fatalf("re-creation not logged: %q", *logs)
	}
}

// Every duplication lost = secure desktop: GDI would read black, so the tiles
// keep their last images and live() reports 0 (the helper then goes back to
// the single-monitor capture after mosaicLostGrace).
func TestMosaicAllLostKeepsImagesWithoutGDI(t *testing.T) {
	ts, be, _ := threeMonitorMosaic(t)
	t0 := time.Unix(1000, 0)
	ts.start(t0, -1, nil)
	now := t0
	ts.tick(now)
	for _, o := range be.outs {
		o.lose = true
		o.openErr = "DuplicateOutput hr=0x80070005 " + dxgiHRText(0x80070005)
	}
	for now.Sub(t0) < 4*time.Second {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if ts.live() != 0 {
		t.Fatalf("live = %d", ts.live())
	}
	for i, o := range be.outs {
		if o.gdis != 0 || o.src != mosaicSrcDXGI {
			t.Fatalf("tile %d: gdis=%d src=%d, want the last DXGI image and no GDI", i, o.gdis, o.src)
		}
	}
}

func TestMosaicStartFailsWhenNothingDuplicates(t *testing.T) {
	ts, be, _ := threeMonitorMosaic(t)
	for _, o := range be.outs {
		o.openErr = "DuplicateOutput hr=0x887A0004 " + dxgiHRText(0x887A0004)
	}
	released := false
	if live := ts.start(time.Unix(1000, 0), 0, func() { released = true }); live != 0 {
		t.Fatalf("live = %d", live)
	}
	if !released {
		t.Fatal("the held output must still be tried after releasing the single capture")
	}
}

// The status lines: one on the first composite, one after mosaicSummaryAfter,
// then nothing — with the sampled black / not-black pixels per tile.
func TestMosaicSummaryAndLitSamples(t *testing.T) {
	ts, _, logs := threeMonitorMosaic(t)
	t0 := time.Unix(1000, 0)
	ts.start(t0, -1, nil)
	ts.tick(t0)
	w, h := 400, 100
	frame := make([]byte, w*h*4)
	// Paint only the first tile's rect of a small fake frame.
	ts.tiles[0].tile.Frame = reachRect{0, 0, 100, 100}
	ts.tiles[1].tile.Frame = reachRect{100, 0, 100, 100}
	ts.tiles[2].tile.Frame = reachRect{350, 50, 100, 100} // half outside
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			frame[(y*w+x)*4+1] = 200
		}
	}
	if lit, n := mosaicLitSamples(frame, w, h, ts.tiles[0].tile.Frame); lit != 16 || n != 16 {
		t.Fatalf("painted tile %d/%d", lit, n)
	}
	if lit, n := mosaicLitSamples(frame, w, h, ts.tiles[1].tile.Frame); lit != 0 || n != 16 {
		t.Fatalf("black tile %d/%d", lit, n)
	}
	if _, n := mosaicLitSamples(frame, w, h, ts.tiles[2].tile.Frame); n >= 16 || n == 0 {
		t.Fatalf("clipped tile sampled %d", n)
	}
	if _, n := mosaicLitSamples(nil, w, h, ts.tiles[0].tile.Frame); n != 0 {
		t.Fatal("an empty frame must not be sampled")
	}
	before := len(*logs)
	ts.maybeSummary(t0, frame, w, h)
	ts.maybeSummary(t0.Add(time.Second), frame, w, h)
	ts.maybeSummary(t0.Add(mosaicSummaryAfter), frame, w, h)
	ts.maybeSummary(t0.Add(10*time.Second), frame, w, h)
	status := (*logs)[before:]
	if len(status) != 2 {
		t.Fatalf("want 2 status lines, got %q", status)
	}
	if !strings.Contains(status[0], "16/16 sampled pixels not black") || !strings.Contains(status[0], "0/16 sampled pixels not black") ||
		!strings.Contains(status[0], "painted from DXGI") {
		t.Fatalf("status line: %q", status[0])
	}
}

func TestDXGIHRText(t *testing.T) {
	if s := dxgiHRText(0x80070057); !strings.Contains(s, "already duplicates") {
		t.Fatalf("E_INVALIDARG: %q", s)
	}
	if s := dxgiHRText(0x887A0026); !strings.Contains(s, "ACCESS_LOST") {
		t.Fatalf("access lost: %q", s)
	}
	if dxgiHRText(0x12345678) != "unknown HRESULT" {
		t.Fatal("unknown code")
	}
	if mosaicStepName(-5) != "DuplicateOutput" {
		t.Fatal("step name")
	}
}

func assertNothingPaintsBlack(t *testing.T, ts *mosaicTiles, be *fakeMosaicBackend, when string) {
	t.Helper()
	for _, tile := range ts.tiles {
		if be.paintsBlack(tile.slot) {
			t.Fatalf("%s: %s paints black (src=%d, %+v)", when, ts.name(tile), be.source(tile.slot), *be.outs[tile.slot])
		}
	}
}

// DXGI can hand out black desktop images right after DuplicateOutput: they
// are not shown over the GDI seed, the first real image is.
func TestMosaicBlackFirstImagesKeepGDISeed(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	be.outs[0].blackImages = 2
	t0 := time.Unix(1000, 0)
	ts.start(t0, -1, nil)
	now := t0
	for i := 0; i < 2; i++ {
		ts.tick(now)
		assertNothingPaintsBlack(t, ts, be, fmt.Sprintf("tick %d", i))
		if be.outs[0].src != mosaicSrcGDI {
			t.Fatalf("tick %d: a black DXGI image must not replace the GDI seed (src=%d)", i, be.outs[0].src)
		}
		now = now.Add(33 * time.Millisecond)
	}
	ts.tick(now)
	if be.outs[0].src != mosaicSrcDXGI || !ts.tiles[0].fresh {
		t.Fatalf("the first real image must be shown: src=%d fresh=%v", be.outs[0].src, ts.tiles[0].fresh)
	}
	if be.outs[0].gdis != 1 {
		t.Fatalf("%d GDI copies, want the one seed", be.outs[0].gdis)
	}
	if !logged(*logs, "black desktop image") || !logged(*logs, "(after 2 black one(s))") {
		t.Fatalf("black images not logged: %q", *logs)
	}
	// Once shown, a later black image is shown too (the screen went black).
	be.outs[0].blackImages = 1
	now = now.Add(33 * time.Millisecond)
	ts.tick(now)
	if be.outs[0].src != mosaicSrcDXGI || !be.outs[0].bufBlack {
		t.Fatalf("after the warm-up DXGI is trusted: src=%d", be.outs[0].src)
	}
}

// A screen that really is black: believed after mosaicWarmupImages images.
func TestMosaicReallyBlackScreenBelievedAfterWarmup(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	be.outs[1].reallyBlack = true
	t0 := time.Unix(1000, 0)
	ts.start(t0, -1, nil)
	now := t0
	for i := 0; i < mosaicWarmupImages; i++ {
		ts.tick(now)
		if be.outs[1].src != mosaicSrcGDI {
			t.Fatalf("image %d: src=%d, want the GDI seed", i, be.outs[1].src)
		}
		now = now.Add(33 * time.Millisecond)
	}
	ts.tick(now)
	if be.outs[1].src != mosaicSrcDXGI {
		t.Fatalf("a black screen must be shown from DXGI after %d images, src=%d", mosaicWarmupImages, be.outs[1].src)
	}
	for i := 0; i < 300; i++ {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
	}
	if be.outs[1].gdis != 1 {
		t.Fatalf("%d GDI copies for a black screen DXGI shows, want the one seed", be.outs[1].gdis)
	}
	if !logged(*logs, "still black after 5 images") {
		t.Fatalf("not logged: %q", *logs)
	}
}

// A duplication re-created with the same mode keeps its DXGI buffer; the black
// image DXGI may hand out first overwrites it and is not shown: the tile must
// be re-seeded from GDI in the same composite, not paint that black image.
func TestMosaicRecreatedDuplicationReseeds(t *testing.T) {
	ts, be, logs := threeMonitorMosaic(t)
	t0 := time.Unix(1000, 0)
	ts.start(t0, -1, nil)
	now := t0
	ts.tick(now)
	be.outs[0].lose = true
	now = now.Add(33 * time.Millisecond)
	ts.tick(now)
	if ts.tiles[0].live {
		t.Fatal("not lost")
	}
	be.outs[0].blackImages = 1
	for i := 0; i < 20 && !ts.tiles[0].live; i++ {
		now = now.Add(33 * time.Millisecond)
		ts.tick(now)
		assertNothingPaintsBlack(t, ts, be, fmt.Sprintf("while re-created, tick %d", i))
	}
	if !ts.tiles[0].live {
		t.Fatal("not re-created")
	}
	if be.outs[0].src != mosaicSrcGDI || be.outs[0].gdis != 1 {
		t.Fatalf("re-created tile: src=%d gdis=%d, want an immediate GDI re-seed", be.outs[0].src, be.outs[0].gdis)
	}
	now = now.Add(33 * time.Millisecond)
	ts.tick(now)
	if be.outs[0].src != mosaicSrcDXGI {
		t.Fatalf("src=%d after a real image", be.outs[0].src)
	}
	if !logged(*logs, "duplication re-created") {
		t.Fatalf("not logged: %q", *logs)
	}
}

// Every tile of the three-monitor layout is painted from the first composite on in
// all the start situations, and the summary names the source of each.
func TestMosaicFirstCompositeNeverBlack(t *testing.T) {
	cases := map[string]func(be *fakeMosaicBackend){
		"all fine":        func(be *fakeMosaicBackend) {},
		"idle primary":    func(be *fakeMosaicBackend) { be.outs[0].idle = true },
		"black first":     func(be *fakeMosaicBackend) { be.outs[0].blackImages = 3 },
		"primary refused": func(be *fakeMosaicBackend) { be.outs[0].heldBySingle = true },
		"side unsupported": func(be *fakeMosaicBackend) {
			be.outs[2].openErr = "DuplicateOutput hr=0x887A0004 " + dxgiHRText(0x887A0004)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			ts, be, logs := threeMonitorMosaic(t)
			setup(be)
			t0 := time.Unix(1000, 0)
			if ts.start(t0, -1, nil) == 0 {
				t.Fatal("nothing live")
			}
			ts.tick(t0)
			assertNothingPaintsBlack(t, ts, be, "first composite")
			ts.maybeSummary(t0, nil, 0, 0)
			if !logged(*logs, "mosaic: status") || logged(*logs, "NOTHING (black)") {
				t.Fatalf("summary: %q", *logs)
			}
		})
	}
}

func TestMosaicNextDue(t *testing.T) {
	t0 := time.Unix(1000, 0)
	every := 100 * time.Millisecond
	if got := mosaicNextDue(time.Time{}, t0, every); !got.Equal(t0.Add(every)) {
		t.Fatalf("first: %v", got.Sub(t0))
	}
	// Due at t0, run 32 ms late: the period is kept.
	if got := mosaicNextDue(t0, t0.Add(32*time.Millisecond), every); !got.Equal(t0.Add(every)) {
		t.Fatalf("late: %v", got.Sub(t0))
	}
	// More than a period behind: restart from now.
	if got := mosaicNextDue(t0, t0.Add(250*time.Millisecond), every); !got.Equal(t0.Add(350 * time.Millisecond)) {
		t.Fatalf("behind: %v", got.Sub(t0))
	}
}
