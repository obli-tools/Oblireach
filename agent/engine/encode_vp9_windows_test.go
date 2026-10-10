//go:build windows

package engine

import (
	"testing"
)

// TestVP9EncodeWithShippedDLLs: the shipped libvpx (agent/dist, with its
// runtime dependency) loads through the verified loader, the encoder starts
// with the configuration layout and ABI version of libvpx's own headers, and
// the first frame is a VP9 key frame. (Until 2026-10 the encoder used
// hand-written structures and ABI 14: vpx_codec_enc_config_default wrote past
// the configuration and the process crashed.)
func TestVP9EncodeWithShippedDLLs(t *testing.T) {
	needAMD64(t)
	dir := copyDistDLLs(t, "libvpx-1.dll", "libwinpthread-1.dll")
	cdlForget(t, dir)
	saved := bundledDLLDir
	bundledDLLDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		vp9ForgetDLL()
		bundledDLLDir = saved
	})
	if !vp9Available() {
		t.Fatalf("libvpx not available: %s", refusal(dir, "libvpx-1.dll"))
	}
	const w, h = 640, 360
	if err := vp9EncoderInit(w, h, 30, 1000); err != nil {
		t.Fatal(err)
	}
	bgra := make([]byte, w*h*4)
	total := 0
	for i := 0; i < 10; i++ {
		for j := range bgra {
			bgra[j] = byte(i*7 + j/4)
		}
		out, err := vp9EncodeFrame(bgra, w, h)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if i == 0 {
			// Uncompressed header: frame_marker 0b10, profile 0, show_existing 0,
			// frame_type 0 (key frame), then the sync code 49 83 42.
			if len(out) < 4 || out[0]>>6 != 2 || out[0]&0x04 != 0 || out[1] != 0x49 || out[2] != 0x83 || out[3] != 0x42 {
				t.Fatalf("first frame is not a VP9 key frame: % x", out[:min(len(out), 8)])
			}
		}
		total += len(out)
	}
	t.Logf("VP9: 10 frames %dx%d, %d bytes", w, h, total)
}
