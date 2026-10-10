//go:build linux && cgo

package engine

import (
	"bytes"
	"encoding/hex"
	"os"
	"syscall"
	"testing"
)

// The C format parser of the PipeWire callback agrees with the Go one.
func TestPipeWireCParserMatchesGo(t *testing.T) {
	b, _ := hex.DecodeString(spaFormatWithModifier)
	for _, p := range [][]byte{b, pwEnumFormatPod(), pwBuffersPod(), b[:40], {}} {
		goFi, goOK := parseSpaFormat(p)
		cFi, cOK := pwTestParseFormat(p)
		if goOK != cOK || (goOK && goFi != cFi) {
			t.Errorf("C %+v %v, Go %+v %v", cFi, cOK, goFi, goOK)
		}
	}
}

func TestPipeWireRowConversion(t *testing.T) {
	px := []byte{1, 2, 3, 4} // one pixel, bytes in memory order
	want := map[uint32][]byte{
		spaVideoFormatBGRx: {1, 2, 3, 4}, spaVideoFormatBGRA: {1, 2, 3, 4},
		spaVideoFormatRGBx: {3, 2, 1, 255}, spaVideoFormatRGBA: {3, 2, 1, 255},
		spaVideoFormatxRGB: {4, 3, 2, 255}, spaVideoFormatARGB: {4, 3, 2, 255},
		spaVideoFormatxBGR: {2, 3, 4, 255}, spaVideoFormatABGR: {2, 3, 4, 255},
	}
	for f, w := range want {
		if got := pwTestConvert(px, 1, f); !bytes.Equal(got, w) {
			t.Errorf("format %d: %v, want %v", f, got, w)
		}
	}
}

// Without PipeWire the consumer fails cleanly (the build host has none).
func TestPipeWireMissingIsAnError(t *testing.T) {
	if pwAvailable() == nil {
		t.Skip("libpipewire-0.3 is installed here")
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := syscall.Dup(int(f.Fd()))
	f.Close()
	if _, err := pwOpen(fd, []uint32{42}); err == nil {
		t.Fatal("pwOpen without PipeWire must fail")
	}
}

// Real OpenH264 (OBLIREACH_TEST_OPENH264_DIR = a folder holding Cisco's
// pinned build): structure layout and an actual encode.
func TestOpenH264LinuxReal(t *testing.T) {
	dir := os.Getenv("OBLIREACH_TEST_OPENH264_DIR")
	if dir == "" {
		t.Skip("OBLIREACH_TEST_OPENH264_DIR not set")
	}
	SetOpenH264Dir(dir)
	defer SetOpenH264Dir("")
	if err := openH264LinuxLoad(); err != nil {
		t.Fatal(err)
	}
	lib, ours := openH264LinuxParamExtent()
	if lib <= 0 || lib > ours {
		t.Fatalf("GetDefaultParams writes %d bytes, OL_SEncParamExt is %d: layout mismatch", lib, ours)
	}
	enc := &openH264Linux{}
	const w, h = 320, 240
	if err := enc.init(w, h, 30, 1_000_000); err != nil {
		t.Fatal(err)
	}
	defer enc.close()
	frame := make([]byte, w*h*4)
	for i := range frame {
		frame[i] = byte(i * 7)
	}
	out, err := enc.encode(frame, w, h, 0)
	if err != nil || len(out) < 16 {
		t.Fatalf("encode = %d bytes, %v", len(out), err)
	}
	// First access unit: Annex B, starts with an SPS (nal_unit_type 7).
	if !bytes.HasPrefix(out, []byte{0, 0, 0, 1}) || out[4]&0x1f != 7 {
		t.Errorf("not an SPS-led IDR: % x", out[:8])
	}
	enc.setBitrate(500_000)
	for i := 0; i < 5; i++ {
		frame[i*101] ^= 0xff
		if _, err := enc.encode(frame, w, h, int64(33*(i+1))); err != nil {
			t.Fatal(err)
		}
	}
}
