//go:build windows

package engine

import (
	"testing"
)

// TestAV1EncodeWithShippedDLL: the shipped SVT-AV1 (agent/dist) loads through
// the verified loader, the encoder starts with the configuration layout of
// SVT-AV1's own headers, and its output is what the viewers decode with
// WebCodecs "av01.0.04M.08": a low-overhead OBU stream (every OBU carries its
// size), one temporal unit per frame, the first one a key frame led by a
// sequence header (Main profile, 8-bit 4:2:0, the stream's size), a key frame
// with its sequence header again one second later. (Until 2026-10 the
// encoder used hand-written structures and unknown parameter names:
// svt_av1_enc_set_parameter refused the configuration, code -3.)
func TestAV1EncodeWithShippedDLL(t *testing.T) {
	needAMD64(t)
	dir := copyDistDLLs(t, "libSvtAv1Enc-2.dll")
	cdlForget(t, dir)
	saved := bundledDLLDir
	bundledDLLDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		av1ForgetDLL()
		bundledDLLDir = saved
	})
	if !av1Available() {
		t.Fatalf("SVT-AV1 not available: %s", refusal(dir, "libSvtAv1Enc-2.dll"))
	}
	const w, h, fps = 640, 360, 10
	if err := av1EncoderInit(w, h, fps, 1000); err != nil {
		t.Fatal(err)
	}
	bgra := make([]byte, w*h*4)
	total, keys := 0, []int{}
	for i := 0; i < 2*fps+1; i++ {
		for j := range bgra {
			bgra[j] = byte(i*7 + j/4 + (j/(w*4))*3)
		}
		out, err := av1EncodeFrame(bgra, w, h)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(out) == 0 {
			t.Fatalf("frame %d: no packet (low delay: one temporal unit per picture)", i)
		}
		tu, err := parseAV1TemporalUnit(out)
		if err != nil {
			t.Fatalf("frame %d: %v (% x)", i, err, out[:min(len(out), 16)])
		}
		if !tu.hasFrame {
			t.Fatalf("frame %d: no frame OBU (types %v)", i, tu.types)
		}
		if tu.key != (tu.seq != nil) {
			t.Fatalf("frame %d: key frame %v but sequence header %v (types %v)", i, tu.key, tu.seq != nil, tu.types)
		}
		if tu.seq != nil {
			s := tu.seq
			if s.profile != 0 || s.bitDepth != 8 || s.mono || s.width != w || s.height != h {
				t.Fatalf("frame %d: sequence header %+v, want Main 8-bit 4:2:0 %dx%d", i, *s, w, h)
			}
			keys = append(keys, i)
		}
		total += len(out)
	}
	if len(keys) != 3 || keys[0] != 0 || keys[1] != fps || keys[2] != 2*fps {
		t.Fatalf("key frames at %v, want 0, %d, %d", keys, fps, 2*fps)
	}
	t.Logf("AV1: %d frames %dx%d, %d bytes, key frames %v", 2*fps+1, w, h, total, keys)

	// Close and start again at another size (view change).
	av1EncoderClose()
	const w2, h2 = 320, 240
	if err := av1EncoderInit(w2, h2, fps, 500); err != nil {
		t.Fatal(err)
	}
	out, err := av1EncodeFrame(make([]byte, w2*h2*4), w2, h2)
	if err != nil {
		t.Fatal(err)
	}
	tu, err := parseAV1TemporalUnit(out)
	if err != nil || tu.seq == nil || !tu.key || tu.seq.width != w2 || tu.seq.height != h2 {
		t.Fatalf("after restart: %v %+v", err, tu)
	}
	if _, err := av1EncodeFrame(bgra, w, h); err == nil {
		t.Fatal("a frame of another size than the encoder's was accepted")
	}
}

func TestAV1RejectsUnsupportedSizes(t *testing.T) {
	for _, s := range [][2]int{{641, 360}, {640, 361}, {32, 32}} {
		if err := av1EncoderInit(s[0], s[1], 30, 1000); err == nil {
			av1EncoderClose()
			t.Fatalf("%dx%d accepted", s[0], s[1])
		}
	}
}

func TestParseAV1TemporalUnitRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{0x12},             // temporal delimiter without its size byte
		{0x10, 0x00},       // temporal delimiter without obu_has_size_field
		{0x32, 0x05, 0x00}, // frame header OBU shorter than its size
		{0x92, 0x00},       // forbidden bit set
	} {
		if _, err := parseAV1TemporalUnit(b); err == nil {
			t.Errorf("% x accepted", b)
		}
	}
}
