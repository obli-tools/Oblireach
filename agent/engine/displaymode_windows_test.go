//go:build windows

package engine

import (
	"testing"
	"unsafe"
)

func TestDevModeWSize(t *testing.T) {
	if s := unsafe.Sizeof(devModeW{}); s != 220 {
		t.Fatalf("DEVMODEW is 220 bytes, got %d", s)
	}
	var dm devModeW
	if o := unsafe.Offsetof(dm.BitsPerPel); o != 168 {
		t.Fatalf("dmBitsPerPel offset %d, want 168", o)
	}
	if o := unsafe.Offsetof(dm.DisplayFrequency); o != 184 {
		t.Fatalf("dmDisplayFrequency offset %d, want 184", o)
	}
}

// TestWinDisplayOpsReadOnly reads the current mode and the list of the first
// monitor this process sees (no change is ever applied by the tests).
func TestWinDisplayOpsReadOnly(t *testing.T) {
	_, dev := displayTarget()
	if dev == "" {
		t.Skip("no monitor visible to this process")
	}
	ops := winDisplayOps{}
	cur, ok := ops.current(dev)
	if !ok {
		t.Skipf("EnumDisplaySettingsEx(%s, current) failed (no display driver?)", dev)
	}
	if cur.W <= 0 || cur.H <= 0 {
		t.Fatalf("current %+v", cur)
	}
	modes := shapeDisplayModes(ops.list(dev), cur)
	n := 0
	for _, m := range modes {
		if m.Current {
			n++
			if m.W != cur.W || m.H != cur.H {
				t.Fatalf("current entry %+v vs %+v", m, cur)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d current entries in %+v", n, modes)
	}
	_ = displaySessionIsRDP() // must not crash
}
