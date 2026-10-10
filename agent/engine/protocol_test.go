package engine

import (
	"testing"
	"time"
)

func mon(idx, x, y, w, h int, primary bool) MonitorInfo {
	return MonitorInfo{Index: idx, X: x, Y: y, Width: w, Height: h, Primary: primary}
}

// Every frame pixel inside a tile must map into that tile's monitor rectangle
// (this is what a click needs), and the tile grid must stay inside the budget.
func assertRoundTrip(t *testing.T, v reachView, b mosaicBudget) {
	t.Helper()
	for ti, tile := range v.Tiles {
		f := tile.Frame
		stepX := max(1, f.W/23)
		stepY := max(1, f.H/17)
		for fy := f.Y; fy < f.Y+f.H; fy += stepY {
			for fx := f.X; fx < f.X+f.W; fx += stepX {
				dx, dy, ok := v.frameToDesktop(fx, fy)
				d := tile.Desktop
				if !ok || dx < d.X || dx >= d.X+d.W || dy < d.Y || dy >= d.Y+d.H {
					t.Fatalf("frame (%d,%d) in tile %d -> desktop (%d,%d) ok=%v outside %+v", fx, fy, ti, dx, dy, ok, d)
				}
			}
		}
		// Bottom-right corner stays inside its monitor.
		dx, dy, _ := v.frameToDesktop(f.X+f.W-1, f.Y+f.H-1)
		if dx >= tile.Desktop.X+tile.Desktop.W || dy >= tile.Desktop.Y+tile.Desktop.H {
			t.Fatalf("tile %d bottom-right escapes its monitor: (%d,%d)", ti, dx, dy)
		}
		// Cursor overlay: the monitor centre must land inside its own tile.
		cx, cy := v.desktopToFrame(tile.Desktop.X+tile.Desktop.W/2, tile.Desktop.Y+tile.Desktop.H/2)
		if v.hitTile(cx, cy) != ti {
			t.Fatalf("centre of monitor %d drawn outside its tile (%d,%d)", ti, cx, cy)
		}
	}
	if v.Width%2 != 0 || v.Height%2 != 0 {
		t.Fatalf("odd frame %dx%d", v.Width, v.Height)
	}
	if v.Width > b.maxW || v.Height > b.maxH {
		t.Fatalf("budget side exceeded %dx%d (max %dx%d)", v.Width, v.Height, b.maxW, b.maxH)
	}
}

func TestMosaicSideBySideMixedSizes(t *testing.T) {
	b := agentClampBudget(0, 0, 0)
	// 2560x1440 primary + 1920x1080 to the right, top-aligned (mixed-DPI setup).
	v := layoutMosaic([]MonitorInfo{
		mon(0, 0, 0, 2560, 1440, true),
		mon(1, 2560, 0, 1920, 1080, false),
	}, b)
	if v.Mode != "mosaic" || v.Scale >= 1 || v.Width > b.maxW {
		t.Fatalf("expected downscaled mosaic, got %+v", v)
	}
	// A click in the gap under the smaller monitor is clamped + flagged.
	fx, fy := v.desktopToFrame(3000, 1300)
	if v.hitTile(fx, fy) != -1 {
		t.Fatal("gap below the smaller monitor must not hit a tile")
	}
	if _, _, ok := v.frameToDesktop(fx, fy); ok {
		t.Fatal("gap click must be flagged ok=false")
	}
	assertRoundTrip(t, v, b)
}

func TestMosaicNegativeOrigin(t *testing.T) {
	b := agentClampBudget(0, 0, 0)
	v := layoutMosaic([]MonitorInfo{
		mon(0, 0, 0, 1920, 1080, true),
		mon(1, -1680, 300, 1680, 1050, false),
	}, b)
	if v.originX != -1680 || v.originY != 0 {
		t.Fatalf("origin (%d,%d)", v.originX, v.originY)
	}
	if v.Scale != 1 {
		t.Fatalf("3600x1350 fits, scale should be 1, got %f", v.Scale)
	}
	// The primary's (0,0) sits at frame x=1680.
	if fx, fy := v.desktopToFrame(0, 0); fx != 1680 || fy != 0 {
		t.Fatalf("primary origin at frame (%d,%d)", fx, fy)
	}
	assertRoundTrip(t, v, b)
}

func TestMosaicPortrait(t *testing.T) {
	b := agentClampBudget(0, 0, 0)
	v := layoutMosaic([]MonitorInfo{
		mon(0, 0, 0, 2560, 1440, true),
		mon(1, 2560, -240, 1080, 1920, false),
	}, b)
	if v.originY != -240 || len(v.Tiles) != 2 {
		t.Fatalf("bbox/tiles %+v", v)
	}
	if v.Tiles[1].Frame.H <= v.Tiles[1].Frame.W {
		t.Fatalf("portrait tile must stay portrait: %+v", v.Tiles[1].Frame)
	}
	assertRoundTrip(t, v, b)
}

func TestMosaicThree4K(t *testing.T) {
	b := agentClampBudget(0, 0, 0)
	v := layoutMosaic([]MonitorInfo{
		mon(0, 0, 0, 3840, 2160, true),
		mon(1, 3840, 0, 3840, 2160, false),
		mon(2, 7680, 0, 3840, 2160, false),
	}, b)
	if v.Width > b.maxW {
		t.Fatalf("width %d exceeds %d", v.Width, b.maxW)
	}
	mbs := ((v.Width + 15) / 16) * ((v.Height + 15) / 16)
	if mbs > 36864 { // H.264 level 5.2 macroblock ceiling
		t.Fatalf("frame %dx%d = %d MBs exceeds level 5.2", v.Width, v.Height, mbs)
	}
	assertRoundTrip(t, v, b)
}

func TestSingleTileViewIsIdentity(t *testing.T) {
	v := singleTileView(1, 1920, 0, 1280, 1024, 0, false)
	if v.Scale != 1 || v.Width != 1280 || v.Height != 1024 {
		t.Fatalf("%+v", v)
	}
	// Frame (10,20) maps to the monitor's physical origin + offset.
	dx, dy, ok := v.frameToDesktop(10, 20)
	if !ok || dx != 1930 || dy != 20 {
		t.Fatalf("single-monitor mapping (%d,%d) ok=%v", dx, dy, ok)
	}
}

// A viewer budget smaller than the hard ceiling must be honoured, and a viewer
// value larger than the ceiling must be clamped down (anti-DoS).
func TestClampBudget(t *testing.T) {
	b := agentClampBudget(2560, 1440, 2560*1440)
	if b.maxW != 2560 || b.maxH != 1440 || b.maxPixels != 2560*1440 {
		t.Fatalf("viewer budget not honoured: %+v", b)
	}
	b = agentClampBudget(99999, 99999, 1<<40)
	if b.maxW != mosaicMaxW || b.maxH != mosaicMaxH || b.maxPixels != mosaicMaxPixels {
		t.Fatalf("oversized viewer budget not clamped: %+v", b)
	}
}

func TestAudioV2FrameRoundTrip(t *testing.T) {
	pcm := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	f := buildAudioV2Frame(2, 48000, 7, 123456789, pcm)
	if len(f) != audioV2HeaderLen+len(pcm) {
		t.Fatalf("len %d", len(f))
	}
	if f[0] != frameTypeAudioV2 || f[1] != 1 || f[2] != 0 || f[3] != 2 {
		t.Fatalf("header %v", f[:4])
	}
	if u32LE(f[4:]) != 48000 || u32LE(f[8:]) != 7 {
		t.Fatalf("rate/seq %d %d", u32LE(f[4:]), u32LE(f[8:]))
	}
	var pos uint64
	for i := 0; i < 8; i++ {
		pos |= uint64(f[12+i]) << (8 * uint(i))
	}
	if pos != 123456789 {
		t.Fatalf("pos %d", pos)
	}
}

// Reproduces the stuck-modifier chord: {Meta down} then {r down, meta:false}. The
// protect mask must keep Win while the client-app's per-key 'r' says meta:up.
func TestModifierProtectChord(t *testing.T) {
	inputResetModifierTracking()
	// {Meta down}: before injecting, nothing is protected.
	if modProtectMask() != 0 {
		t.Fatalf("initial mask %d", modProtectMask())
	}
	trackInjectedModifier(0x5B, true) // LWIN down
	if modProtectMask() != 1<<3 {
		t.Fatalf("after Win down mask=%d, want %d", modProtectMask(), 1<<3)
	}
	// {r down, meta:false} arrives: reconcile would release Win, but the mask
	// protects it (bit 3 set) — the C reconciler skips it.
	if modProtectMask()&(1<<3) == 0 {
		t.Fatal("Win must be protected while injected")
	}
	// {Meta up}: Win released, mask clears.
	trackInjectedModifier(0x5B, false)
	if modProtectMask() != 0 {
		t.Fatalf("after Win up mask=%d", modProtectMask())
	}
	// Ctrl+Shift group mapping (Task Manager chord).
	trackInjectedModifier(0xA2, true) // LCONTROL
	trackInjectedModifier(0xA0, true) // LSHIFT
	if modProtectMask() != (1<<0 | 1<<1) {
		t.Fatalf("ctrl+shift mask=%d", modProtectMask())
	}
	inputResetModifierTracking()
	if modProtectMask() != 0 {
		t.Fatal("reset must clear the tracker")
	}
	if modBitForVK(0x41) != -1 {
		t.Fatal("a letter is not a modifier")
	}
}

// A modifier whose key-up the viewer lost (tab switch with Ctrl held) must
// still be reconciled on the operator's next key, seconds later: the chord
// protection only covers the burst right after the press.
func TestModifierProtectExpires(t *testing.T) {
	defer func() { modNow = time.Now }()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	now := base
	modNow = func() time.Time { return now }
	inputResetModifierTracking()

	trackInjectedModifier(0x11, true) // Ctrl down, key-up then lost
	now = base.Add(20 * time.Millisecond)
	if modProtectMask()&1 == 0 {
		t.Fatal("Ctrl must be protected inside the chord burst")
	}
	now = base.Add(3 * time.Second) // operator comes back, types a letter (ctrl:false)
	if modProtectMask()&1 != 0 {
		t.Fatal("a Ctrl pressed 3 s ago with a lost key-up must be released by the reconciler")
	}
	// A new press restarts the window.
	trackInjectedModifier(0x11, true)
	if modProtectMask()&1 == 0 {
		t.Fatal("a new press must be protected again")
	}
	inputResetModifierTracking()
}

func TestAudioV2SenderSeqAndPos(t *testing.T) {
	var s audioV2Sender
	// 4 stereo frames of s16le = 4*2*2 = 16 bytes → 4 samples/channel.
	pcm := make([]byte, 16)
	f1 := s.frame(pcm, 2, 48000)
	if f1 == nil || u32LE(f1[8:]) != 0 {
		t.Fatalf("first frame seq should be 0")
	}
	f2 := s.frame(pcm, 2, 48000)
	if u32LE(f2[8:]) != 1 {
		t.Fatalf("second frame seq should be 1, got %d", u32LE(f2[8:]))
	}
	// pos advances by samples-per-channel (16 bytes / (2*2) = 4) each frame.
	var pos uint64
	for i := 0; i < 8; i++ {
		pos |= uint64(f2[12+i]) << (8 * uint(i))
	}
	if pos != 4 {
		t.Fatalf("third-frame pos should be 4, got %d", pos)
	}
	// A chunk too small for one full frame is dropped.
	if s.frame([]byte{1}, 2, 48000) != nil {
		t.Fatal("undersized chunk must return nil")
	}
}

func TestFileFrameRoundTrip(t *testing.T) {
	data := []byte("hello reach")
	f := buildFileFrame(FrameTypeFileUp, 0x11223344, 0x55667788, data)
	typ, tid, seq, got, ok := parseFileFrame(f)
	if !ok || typ != FrameTypeFileUp || tid != 0x11223344 || seq != 0x55667788 || string(got) != "hello reach" {
		t.Fatalf("roundtrip typ=%x tid=%x seq=%x data=%q ok=%v", typ, tid, seq, got, ok)
	}
	if _, _, _, _, ok := parseFileFrame([]byte{1, 2, 3}); ok {
		t.Fatal("short frame must not parse")
	}
}

// Legacy 0x06 audio for viewers that never negotiate: stereo s16le averaged
// into mono s16le (sign kept, no overflow at full scale).
func TestDownmixS16ToMono(t *testing.T) {
	le := func(v int16) []byte { return []byte{byte(uint16(v)), byte(uint16(v) >> 8)} }
	var pcm []byte
	for _, s := range []int16{1000, 3000, -32768, -32768, 32767, 32767, -100, 100} {
		pcm = append(pcm, le(s)...)
	}
	out := downmixS16ToMono(pcm, 2)
	want := []int16{2000, -32768, 32767, 0}
	if len(out) != len(want)*2 {
		t.Fatalf("len %d, want %d", len(out), len(want)*2)
	}
	for i, w := range want {
		got := int16(uint16(out[i*2]) | uint16(out[i*2+1])<<8)
		if got != w {
			t.Fatalf("frame %d: %d, want %d", i, got, w)
		}
	}
	mono := downmixS16ToMono(pcm[:4], 1)
	if len(mono) != 4 || mono[0] != pcm[0] {
		t.Fatal("mono input must be copied as is")
	}
}
