//go:build linux

package engine

import (
	"encoding/hex"
	"testing"
)

// Bytes produced by libspa's own builder (spa_pod_builder_add_object with
// the SPA_POD_* macros of PipeWire 1.0.5) for the same parameters, padded to
// 8: the Go builder must match them exactly.
var spaPodGolden = map[string]string{
	"enum":       "f00000000f0000000300040003000000010000000000000004000000030000000200000000000000020000000000000004000000030000000100000000000000010002000000000034000000130000000300000000000000040000000300000008000000080000000c000000070000000b000000090000000d0000000a0000000e00000000000000030002000000000028000000130000000100000000000000080000000a000000800700003804000001000000010000000020000000200000040002000000000028000000130000000100000000000000080000000b0000001e0000000100000000000000010000006801000001000000",
	"buffers":    "300000000f000000040004000500000006000000000000001400000013000000040000000000000004000000040000000600000000000000",
	"metaheader": "380000000f0000000500040006000000010000000000000004000000030000000100000000000000020000000000000004000000040000002000000000000000",
	"metacursor": "500000000f000000050004000600000001000000000000000400000003000000050000000000000002000000000000001c000000130000000100000000000000040000000400000030400000340000003000040000000000",
}

// A fixated Format as a compositor sends it (BGRA, a DRM modifier,
// 2560x1440), built by libspa.
const spaFormatWithModifier = "980000000f0000000300040004000000010000000000000004000000030000000200000000000000020000000000000004000000030000000100000000000000010002000000000004000000030000000c000000000000000200020000000000080000000500000014606000000000030300020000000000080000000a000000000a0000a00500000400020000000000080000000b0000000000000001000000"

func TestSpaPodsMatchLibspa(t *testing.T) {
	got := map[string]spaPod{
		"enum": pwEnumFormatPod(), "buffers": pwBuffersPod(),
		"metaheader": pwMetaHeaderPod(), "metacursor": pwMetaCursorPod(),
	}
	for name, want := range spaPodGolden {
		if h := hex.EncodeToString(got[name]); h != want {
			t.Errorf("%s:\n got %s\nwant %s", name, h, want)
		}
		if len(got[name])%8 != 0 {
			t.Errorf("%s: not padded to 8", name)
		}
	}
}

func TestParseSpaFormat(t *testing.T) {
	b, _ := hex.DecodeString(spaFormatWithModifier)
	fi, ok := parseSpaFormat(b)
	if !ok || fi != (spaFormatInfo{Format: spaVideoFormatBGRA, Width: 2560, Height: 1440, HasModifier: true}) {
		t.Errorf("parse = %+v %v", fi, ok)
	}
	// The EnumFormat (choices) reads its defaults.
	fi, ok = parseSpaFormat(pwEnumFormatPod())
	if !ok || fi.Format != spaVideoFormatBGRx || fi.Width != 1920 || fi.Height != 1080 || fi.HasModifier {
		t.Errorf("enum defaults = %+v %v", fi, ok)
	}
	// Truncated / garbage input never panics and is refused.
	for n := 0; n < len(b); n += 7 {
		if _, ok := parseSpaFormat(b[:n]); ok {
			t.Errorf("truncated to %d accepted", n)
		}
	}
	if _, ok := parseSpaFormat(pwBuffersPod()); ok {
		t.Error("a Buffers object is not a format")
	}
}
