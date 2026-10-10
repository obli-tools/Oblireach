//go:build windows

package engine

import (
	"errors"
	"testing"
)

// testPx builds a distinct BGRA pixel for texture position (x, y).
func testPx(x, y int) [4]byte { return [4]byte{byte(x), byte(y), byte(x ^ y), 0xAB} }

// testTexture is a srcW x srcH texture with `pad` bytes of row padding filled
// with 0xEE (must never reach the frame).
func testTexture(srcW, srcH, pad int) (buf []byte, pitch int) {
	pitch = srcW*4 + pad
	buf = make([]byte, pitch*srcH)
	for i := range buf {
		buf[i] = 0xEE
	}
	for y := 0; y < srcH; y++ {
		for x := 0; x < srcW; x++ {
			p := testPx(x, y)
			copy(buf[y*pitch+x*4:], p[:])
		}
	}
	return buf, pitch
}

// The DXGI copy turns a rotated output's texture upright with the same
// mapping as the mosaic (scale_into_tile at scale 1: displayed (px,py) ←
// texture (tx,ty)), so the frame matches the desktop rectangle the input
// mapping uses. Before, a portrait output read past the mapped texture.
func TestCapCopyUprightRotations(t *testing.T) {
	const srcW, srcH = 7, 4
	src, pitch := testTexture(srcW, srcH, 12)
	for _, rot := range []int{1, 2, 3, 4} {
		dstW, dstH := srcW, srcH
		if rot == 2 || rot == 4 {
			dstW, dstH = srcH, srcW
		}
		const canary = 32
		mem := make([]byte, dstW*dstH*4+canary)
		for i := range mem {
			mem[i] = 0x5A
		}
		capCopyUpright(mem[:dstW*dstH*4], dstW, dstH, src, srcW, srcH, pitch, rot)
		for py := 0; py < dstH; py++ {
			for pxx := 0; pxx < dstW; pxx++ {
				var tx, ty int
				switch rot {
				case 3:
					tx, ty = srcW-1-pxx, srcH-1-py
				case 2:
					tx, ty = py, srcH-1-pxx
				case 4:
					tx, ty = srcW-1-py, pxx
				default:
					tx, ty = pxx, py
				}
				want := testPx(tx, ty)
				o := (py*dstW + pxx) * 4
				if got := [4]byte{mem[o], mem[o+1], mem[o+2], mem[o+3]}; got != want {
					t.Fatalf("rot %d: frame (%d,%d) = %v, want texture (%d,%d) = %v", rot, pxx, py, got, tx, ty, want)
				}
			}
		}
		for i := dstW * dstH * 4; i < len(mem); i++ {
			if mem[i] != 0x5A {
				t.Fatalf("rot %d: wrote past the frame at byte %d", rot, i)
			}
		}
	}
}

// A texture that is not the size the output reports (a mode change DXGI did
// not signal) is clamped: nothing is written past the frame, and the part of
// the frame it does not cover is cleared instead of keeping a stale picture.
func TestCapCopyUprightClamps(t *testing.T) {
	src, pitch := testTexture(6, 5, 8)
	cases := []struct {
		name       string
		dstW, dstH int
		rot        int
	}{
		{"identity, frame smaller than texture", 4, 3, 1},
		{"identity, frame larger than texture", 9, 8, 1},
		{"90, frame of the unrotated size", 6, 5, 2},
		{"270, frame larger", 8, 9, 4},
		{"180, frame smaller", 3, 2, 3},
	}
	for _, c := range cases {
		const canary = 64
		mem := make([]byte, c.dstW*c.dstH*4+canary)
		for i := range mem {
			mem[i] = 0x5A
		}
		capCopyUpright(mem[:c.dstW*c.dstH*4], c.dstW, c.dstH, src, 6, 5, pitch, c.rot)
		for i := c.dstW * c.dstH * 4; i < len(mem); i++ {
			if mem[i] != 0x5A {
				t.Fatalf("%s: wrote past the frame at byte %d", c.name, i)
			}
		}
		for i := 0; i < c.dstW*c.dstH*4; i++ {
			if mem[i] == 0x5A || mem[i] == 0xEE {
				t.Fatalf("%s: byte %d is stale (0x%02X) or row padding", c.name, i, mem[i])
			}
		}
	}
}

// The shared-frame bookkeeping of the native capture thread: the geometry
// sequence moves on every change of size OR origin (the old code skipped an
// origin-only change), and a frame larger than the stream's buffer is reported
// as errCaptureResized with its size (it used to fail forever: frozen picture).
func TestCaptureGeometryAndResizedFrame(t *testing.T) {
	if captureActive || captureMagActive || captureMosaicActive {
		t.Skip("a capture is running in this process")
	}
	defer capTestReset()

	s1 := capTestSetGeometry(64, 32, 0, 0)
	buf := make([]byte, 64*32*4)
	w, h, err := captureFrame(buf)
	if err != nil || w != 64 || h != 32 {
		t.Fatalf("frame at the stream size: %dx%d, %v", w, h, err)
	}
	if _, _, err := captureFrame(buf); err == nil || errors.Is(err, errCaptureResized) {
		t.Fatalf("no new frame expected, got %v", err)
	}

	// Same geometry again: no new sequence (no needless init to the viewer).
	if s := capTestSetGeometry(64, 32, 0, 0); s != s1 {
		t.Fatalf("unchanged geometry bumped the sequence %d → %d", s1, s)
	}

	// Resolution up: the old buffer is too small.
	s2 := capTestSetGeometry(128, 64, 0, 0)
	if s2 == s1 {
		t.Fatal("a size change must bump the geometry sequence")
	}
	w, h, err = captureFrame(buf)
	if !errors.Is(err, errCaptureResized) || w != 128 || h != 64 {
		t.Fatalf("larger frame: %dx%d, %v (want errCaptureResized 128x64)", w, h, err)
	}

	// Origin only (a neighbour monitor changed): still a new geometry.
	s3 := capTestSetGeometry(128, 64, 1280, 0)
	if s3 == s2 {
		t.Fatal("an origin-only change must bump the geometry sequence")
	}

	// Stale detection as the helper loop sees it.
	captureActive = true
	defer func() { captureActive = false }()
	publishedSingle = currentSingleGeom()
	if captureSingleStale() {
		t.Fatal("just published: not stale")
	}
	if g := publishedSingle.rect; g != (reachRect{1280, 0, 128, 64}) {
		t.Fatalf("published geometry %+v", g)
	}
	if x, y := captureMonitorOffset(); x != 1280 || y != 0 {
		t.Fatalf("input offset follows the published origin: (%d,%d)", x, y)
	}

	// Resolution down: the frame comes back with its own, smaller size (the
	// helper then re-publishes: fw != w), and the view is stale.
	capTestSetGeometry(32, 16, 1280, 0)
	if !captureSingleStale() {
		t.Fatal("a smaller geometry must make the published view stale")
	}
	big := make([]byte, 128*64*4)
	w, h, err = captureFrame(big)
	if err != nil || w != 32 || h != 16 {
		t.Fatalf("smaller frame: %dx%d, %v", w, h, err)
	}
	publishedSingle = currentSingleGeom()
	if captureSingleStale() {
		t.Fatal("re-published: not stale any more")
	}
}

// GDI mode keeps retrying DXGI quickly (250 ms doubling to 2 s) only while it
// can work: an adapter without Desktop Duplication, no DXGI output for the
// target (RDP / Hyper-V basic video) or a slow try → one try every 30 s, so a
// capture that can never use DXGI is not paused every 2 s forever.
func TestCapGDIUpgradeBackoff(t *testing.T) {
	d := 0
	var got []int
	for i := 0; i < 6; i++ {
		d = capGDINextUpgradeDelay(d, false, false)
		got = append(got, d)
	}
	want := []int{250, 500, 1000, 2000, 2000, 2000}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fast schedule got %v want %v", got, want)
		}
	}
	if d := capGDINextUpgradeDelay(250, true, false); d != 30000 {
		t.Fatalf("hopeless DXGI must back off to 30 s, got %d", d)
	}
	if d := capGDINextUpgradeDelay(250, false, true); d != 30000 {
		t.Fatalf("a slow try must back off to 30 s, got %d", d)
	}
	// Once DXGI looks possible again, the fast schedule resumes (capped).
	if d := capGDINextUpgradeDelay(30000, false, false); d != 2000 {
		t.Fatalf("after an idle wait, a hopeful failure retries within 2 s, got %d", d)
	}
}
