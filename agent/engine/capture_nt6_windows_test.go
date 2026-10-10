//go:build windows

package engine

// Windows 7 / 2008 R2 capture rules (capture_windows.go): which DXGI
// failures are permanent / hopeless (no endless D3D11 device re-creation),
// and the pacing of the GDI capture.

import "testing"

func TestDXGIFailureClassification(t *testing.T) {
	const (
		unsupported  = 0x887A0004 // DXGI_ERROR_UNSUPPORTED
		accessDenied = 0x80070005 // E_ACCESSDENIED (secure desktop: heals)
		notAvailable = 0x887A0022 // DXGI_ERROR_NOT_CURRENTLY_AVAILABLE (heals)
	)
	for _, c := range []struct {
		name                string
		step                int
		hr                  uint32
		permanent, hopeless bool
	}{
		{"IDXGIOutput1 missing (Windows 7 / 2008 R2, no Platform Update)", -5, 0x80004002, true, true},
		{"no Desktop Duplication on this Windows / no d3d11.dll", -7, 0, true, true},
		{"adapter without Desktop Duplication", -6, unsupported, true, true},
		{"no DXGI output for the index (RDP / basic video)", -4, 0, false, true},
		{"secure desktop", -6, accessDenied, false, false},
		{"too many duplications", -6, notAvailable, false, false},
		{"factory", -1, 0x80004005, false, false},
		{"none", 0, 0, false, false},
	} {
		if p := capDXGIPermanent(c.step, c.hr); p != c.permanent {
			t.Errorf("%s: permanent=%v want %v", c.name, p, c.permanent)
		}
		if h := capDXGIHopeless(c.step, c.hr); h != c.hopeless {
			t.Errorf("%s: hopeless=%v want %v", c.name, h, c.hopeless)
		}
		// A hopeless failure backs the GDI -> DXGI retries off to 30 s.
		if c.hopeless {
			if d := capGDINextUpgradeDelay(250, capDXGIHopeless(c.step, c.hr), false); d != 30000 {
				t.Errorf("%s: retry in %d ms", c.name, d)
			}
		}
	}
}

func TestGDICapturePacing(t *testing.T) {
	for _, c := range []struct{ cost, idle int }{
		{0, 33},     // instant copy: ~30 fps at most, never a busy loop
		{10, 23},    // 33 ms per frame
		{22, 11},    // cost + half of it
		{40, 20},    // a slow BitBlt (Aero on Windows 7): idle half of it
		{300, 150},  //
		{1000, 250}, // capped
	} {
		if got := capGDIIdleMs(c.cost); got != c.idle {
			t.Errorf("cost %d ms: idle %d ms, want %d", c.cost, got, c.idle)
		}
	}
}

// On this (test) machine: Windows 8+ with d3d11 / dxgi present, Desktop
// Duplication is possible and the loaders found both DLLs.
func TestDXGIPossibleOnThisWindows(t *testing.T) {
	v := WindowsVersion()
	if !v.DXGIDuplicationPossible() {
		t.Skipf("Windows %s: Desktop Duplication does not exist here", v)
	}
	if !captureDXGIPossible() {
		t.Fatalf("Windows %s: d3d11.dll / dxgi.dll not loadable at run time", v)
	}
	if len(enumerateMonitors()) == 0 {
		t.Log("no monitor visible to this process (service / CI session)")
	}
}
