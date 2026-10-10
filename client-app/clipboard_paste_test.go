package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"
)

// dibHeader: a BITMAPINFOHEADER (40 bytes) or a V5 one (124 bytes) with
// its masks.
func dibHeader(size, w, h int, bpp uint16, comp uint32, clrUsed int, masks [4]uint32) []byte {
	b := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint32(b[0:], uint32(size))
	le.PutUint32(b[4:], uint32(int32(w)))
	le.PutUint32(b[8:], uint32(int32(h)))
	le.PutUint16(b[12:], 1)
	le.PutUint16(b[14:], bpp)
	le.PutUint32(b[16:], comp)
	le.PutUint32(b[32:], uint32(clrUsed))
	if size >= 56 {
		le.PutUint32(b[40:], masks[0])
		le.PutUint32(b[44:], masks[1])
		le.PutUint32(b[48:], masks[2])
		le.PutUint32(b[52:], masks[3])
	}
	return b
}

func nrgbaAt(t *testing.T, img image.Image, x, y int) [4]uint8 {
	t.Helper()
	n, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("image %T, want *image.NRGBA", img)
	}
	c := n.NRGBAAt(x, y)
	return [4]uint8{c.R, c.G, c.B, c.A}
}

// A 24-bit bottom-up DIB (the most common screenshot form): rows padded to 4
// bytes, the last row first.
func TestDIBDecode24BottomUp(t *testing.T) {
	h := dibHeader(40, 2, 2, 24, biRGB, 0, [4]uint32{})
	// bottom row: blue, green; top row: red, white (stride 8: 6 + 2 padding)
	px := []byte{255, 0, 0, 0, 255, 0, 0, 0, 0, 0, 255, 255, 255, 255, 0, 0}
	img, err := dibDecode(append(h, px...))
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]int][4]uint8{{0, 0}: {255, 0, 0, 255}, {1, 0}: {255, 255, 255, 255}, {0, 1}: {0, 0, 255, 255}, {1, 1}: {0, 255, 0, 255}}
	for p, c := range want {
		if got := nrgbaAt(t, img, p[0], p[1]); got != c {
			t.Errorf("pixel %v = %v, want %v", p, got, c)
		}
	}
}

// 32 bits BI_RGB, top-down, the fourth byte 0 everywhere: opaque (a
// screenshot), never transparent.
func TestDIBDecode32ZeroAlphaIsOpaque(t *testing.T) {
	h := dibHeader(40, 2, -1, 32, biRGB, 0, [4]uint32{})
	img, err := dibDecode(append(h, 10, 20, 30, 0, 40, 50, 60, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := nrgbaAt(t, img, 0, 0); got != [4]uint8{30, 20, 10, 255} {
		t.Errorf("pixel 0 = %v", got)
	}
	if got := nrgbaAt(t, img, 1, 0); got != [4]uint8{60, 50, 40, 255} {
		t.Errorf("pixel 1 = %v", got)
	}
}

// A V5 header with BI_BITFIELDS and an alpha mask (a browser's "copy image"):
// its alpha kept; the three masks written again after the header (some
// programs do): skipped.
func TestDIBDecodeV5Alpha(t *testing.T) {
	masks := [4]uint32{0x00ff0000, 0x0000ff00, 0x000000ff, 0xff000000}
	px := []byte{1, 2, 3, 128, 4, 5, 6, 255}
	for _, extra := range []bool{false, true} {
		b := dibHeader(124, 2, -1, 32, biBitfields, 0, masks)
		if extra {
			m := make([]byte, 12)
			binary.LittleEndian.PutUint32(m[0:], masks[0])
			binary.LittleEndian.PutUint32(m[4:], masks[1])
			binary.LittleEndian.PutUint32(m[8:], masks[2])
			b = append(b, m...)
		}
		img, err := dibDecode(append(b, px...))
		if err != nil {
			t.Fatalf("extra=%v: %v", extra, err)
		}
		if got := nrgbaAt(t, img, 0, 0); got != [4]uint8{3, 2, 1, 128} {
			t.Errorf("extra=%v: pixel 0 = %v", extra, got)
		}
		if got := nrgbaAt(t, img, 1, 0); got != [4]uint8{6, 5, 4, 255} {
			t.Errorf("extra=%v: pixel 1 = %v", extra, got)
		}
	}
	// BI_BITFIELDS after a 40-byte header: the masks follow it (here 5-6-5 on 16 bits).
	b := dibHeader(40, 1, 1, 16, biBitfields, 0, [4]uint32{})
	m := make([]byte, 12)
	binary.LittleEndian.PutUint32(m[0:], 0xf800)
	binary.LittleEndian.PutUint32(m[4:], 0x07e0)
	binary.LittleEndian.PutUint32(m[8:], 0x001f)
	img, err := dibDecode(append(append(b, m...), 0x00, 0xf8, 0, 0)) // pure red
	if err != nil {
		t.Fatal(err)
	}
	if got := nrgbaAt(t, img, 0, 0); got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("565 red = %v", got)
	}
}

// Palettes (8 and 1 bit) and 16 bits 5-5-5.
func TestDIBDecodePaletteAnd16(t *testing.T) {
	h := dibHeader(40, 2, 1, 8, biRGB, 2, [4]uint32{})
	pal := []byte{0, 0, 255, 0 /* red */, 0, 255, 0, 0 /* green */}
	img, err := dibDecode(append(append(h, pal...), 1, 0, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if nrgbaAt(t, img, 0, 0) != [4]uint8{0, 255, 0, 255} || nrgbaAt(t, img, 1, 0) != [4]uint8{255, 0, 0, 255} {
		t.Errorf("8 bits: %v %v", nrgbaAt(t, img, 0, 0), nrgbaAt(t, img, 1, 0))
	}
	h1 := dibHeader(40, 3, 1, 1, biRGB, 0, [4]uint32{})
	pal1 := []byte{0, 0, 0, 0, 255, 255, 255, 0}
	img, err = dibDecode(append(append(h1, pal1...), 0xa0, 0, 0, 0)) // 1 0 1
	if err != nil {
		t.Fatal(err)
	}
	if nrgbaAt(t, img, 0, 0)[0] != 255 || nrgbaAt(t, img, 1, 0)[0] != 0 || nrgbaAt(t, img, 2, 0)[0] != 255 {
		t.Error("1 bit: wrong pixels")
	}
	h16 := dibHeader(40, 1, 1, 16, biRGB, 0, [4]uint32{})
	img, err = dibDecode(append(h16, 0xe0, 0x03, 0, 0)) // green 0x03e0
	if err != nil {
		t.Fatal(err)
	}
	if got := nrgbaAt(t, img, 0, 0); got != [4]uint8{0, 255, 0, 255} {
		t.Errorf("555 green = %v", got)
	}
}

func TestDIBDecodeRefusals(t *testing.T) {
	ok := dibHeader(40, 1, 1, 24, biRGB, 0, [4]uint32{})
	cases := map[string][]byte{
		"short":       ok[:20],
		"no pixels":   ok,
		"RLE":         append(dibHeader(40, 1, 1, 8, 1, 0, [4]uint32{}), make([]byte, 64)...),
		"JPEG inside": append(dibHeader(40, 1, 1, 24, 4, 0, [4]uint32{}), make([]byte, 64)...),
		"zero width":  append(dibHeader(40, 0, 1, 24, biRGB, 0, [4]uint32{}), make([]byte, 8)...),
		"huge":        append(dibHeader(40, 1<<16, 1<<16, 24, biRGB, 0, [4]uint32{}), make([]byte, 8)...),
		"bad bpp":     append(dibHeader(40, 1, 1, 7, biRGB, 0, [4]uint32{}), make([]byte, 8)...),
		"core header": append([]byte{12, 0, 0, 0}, make([]byte, 60)...),
	}
	for name, b := range cases {
		if _, err := dibDecode(b); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
}

// The PNG made from a DIB decodes to the same pixels.
func TestDIBToPNG(t *testing.T) {
	h := dibHeader(40, 2, 2, 24, biRGB, 0, [4]uint32{})
	px := []byte{255, 0, 0, 0, 255, 0, 0, 0, 0, 0, 255, 255, 255, 255, 0, 0}
	out, err := dibToPNG(append(h, px...))
	if err != nil {
		t.Fatal(err)
	}
	if !pngChecked(out) {
		t.Fatal("not a PNG")
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := img.At(0, 1).RGBA()
	if r != 0 || g != 0 || b != 0xffff || a != 0xffff {
		t.Errorf("bottom-left = %x %x %x %x, want blue", r, g, b, a)
	}
	if pngChecked([]byte("not a png")) || pngChecked(nil) {
		t.Error("pngChecked accepts garbage")
	}
}

func TestPasteImageName(t *testing.T) {
	at := time.Date(2026, 10, 4, 9, 5, 7, 0, time.Local)
	if got := pasteImageName(at); got != "Clipboard 2026-10-04 09-05-07.png" {
		t.Errorf("name = %q", got)
	}
	res, err := pasteImage([]byte("PNGDATA"), at)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "image" || len(res.Items) != 1 || res.Items[0].Rel != "Clipboard 2026-10-04 09-05-07.png" || res.Items[0].Size != 7 || res.Items[0].Handle == "" {
		t.Fatalf("result = %+v", res)
	}
	got, err := clipPastes.readB64(res.Items[0].Handle, 3, 100)
	if raw, _ := base64.StdEncoding.DecodeString(got); err != nil || string(raw) != "DATA" {
		t.Errorf("read = %q, %v", raw, err)
	}
}

// The handles of the pastes: opaque, readable while kept, the oldest pastes
// forgotten beyond 16, and after 12 hours.
func TestPasteStore(t *testing.T) {
	s := newPasteStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	hs, err := s.add([]pasteFile{{data: []byte("hello"), size: 5}, {data: []byte("x"), size: 1}})
	if err != nil || len(hs) != 2 || !strings.HasPrefix(hs[0], "p") || len(hs[0]) != 35 || hs[0][:34] != hs[1][:34] {
		t.Fatalf("handles = %v, %v", hs, err)
	}
	if b, err := s.readB64(hs[0], 1, 3); err != nil || b != base64.StdEncoding.EncodeToString([]byte("ell")) {
		t.Errorf("read = %q, %v", b, err)
	}
	for _, bad := range []struct {
		h   string
		off int64
		n   int
	}{{"nope", 0, 1}, {hs[0][:34] + "9", 0, 1}, {hs[0], -1, 1}, {hs[0], 0, 0}, {hs[0], 6, 1}, {hs[0], 0, 1<<20 + 1}, {"p" + strings.Repeat("0", 32) + "-0", 0, 1}} {
		if _, err := s.readB64(bad.h, bad.off, bad.n); err == nil {
			t.Errorf("readB64(%q, %d, %d) should fail", bad.h, bad.off, bad.n)
		}
	}
	for i := 0; i < pasteMaxSets; i++ {
		if _, err := s.add([]pasteFile{{data: []byte("y"), size: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.readB64(hs[0], 0, 1); err == nil {
		t.Error("the oldest paste kept beyond the limit")
	}
	last, _ := s.add([]pasteFile{{data: []byte("z"), size: 1}})
	if _, err := s.readB64(last[0], 0, 1); err != nil {
		t.Error("a new paste unreadable")
	}
	now = now.Add(pasteTTL + time.Minute)
	if _, err := s.readB64(last[0], 0, 1); err == nil {
		t.Error("an expired paste still readable")
	}
}
