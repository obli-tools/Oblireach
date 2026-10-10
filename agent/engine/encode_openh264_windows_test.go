//go:build windows

package engine

import (
	"testing"
)

func withShippedOpenH264(t *testing.T) {
	t.Helper()
	needAMD64(t)
	const name = "openh264-2.4.1-win64.dll"
	dir := copyDistDLLs(t, name)
	cdlForget(t, dir)
	saved := bundledDLLDir
	bundledDLLDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		openH264ForgetDLL()
		bundledDLLDir = saved
	})
	if !openH264Available() {
		t.Fatalf("OpenH264 not available: %s", refusal(dir, name))
	}
}

// TestOpenH264ParamLayout: the hand-written SEncParamExt has the size of the
// shipped OpenH264's (GetDefaultParams fills it to its last field). Until
// 2026-10 SSliceArgument held 256 slice entries instead of 35 and every
// later field was ignored by the encoder.
func TestOpenH264ParamLayout(t *testing.T) {
	withShippedOpenH264(t)
	dll, ours := openH264ParamExtent()
	if dll != ours {
		t.Fatalf("GetDefaultParams writes %d bytes, OH_SEncParamExt is %d", dll, ours)
	}
}

// TestOpenH264EncodeIntraPeriod: the configuration reaches the encoder: an
// IDR (with SPS/PPS) every fps frames, no skipped frame.
func TestOpenH264EncodeIntraPeriod(t *testing.T) {
	withShippedOpenH264(t)
	const w, h, fps = 640, 360, 10
	if err := openH264Init(w, h, fps, 1000000); err != nil {
		t.Fatal(err)
	}
	bgra := make([]byte, w*h*4)
	var idr []int
	for i := 0; i < 2*fps+1; i++ {
		for j := range bgra {
			bgra[j] = byte(i*7 + j/4 + (j/(w*4))*3)
		}
		out, err := openH264EncodeFrame(bgra, w, h, int64(i*1000/fps))
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(out) == 0 {
			t.Fatalf("frame %d skipped", i)
		}
		sps, isIDR := false, false
		for k := 0; k+3 < len(out); k++ {
			if out[k] == 0 && out[k+1] == 0 && out[k+2] == 1 {
				switch out[k+3] & 0x1f {
				case 5:
					isIDR = true
				case 7:
					sps = true
				}
			}
		}
		if isIDR {
			if !sps {
				t.Fatalf("frame %d: IDR without SPS", i)
			}
			idr = append(idr, i)
		}
	}
	if len(idr) != 3 || idr[0] != 0 || idr[1] != fps || idr[2] != 2*fps {
		t.Fatalf("IDR frames at %v, want 0, %d, %d", idr, fps, 2*fps)
	}
}
