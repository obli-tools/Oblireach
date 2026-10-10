package engine

// Windows version decisions of the capture (nt6 build line: Windows 7 SP1 /
// 8.1 / Server 2008 R2 / 2012 / 2012 R2). Platform-neutral: every decision is
// a pure function of a WinVersion.

import "testing"

var (
	win7SP1   = WinVersion{Major: 6, Minor: 1, Build: 7601, SPMajor: 1}
	win2008R2 = WinVersion{Major: 6, Minor: 1, Build: 7601, SPMajor: 1, Server: true}
	win2012   = WinVersion{Major: 6, Minor: 2, Build: 9200, Server: true}
	win81     = WinVersion{Major: 6, Minor: 3, Build: 9600}
	win2012R2 = WinVersion{Major: 6, Minor: 3, Build: 9600, Server: true}
	win10     = WinVersion{Major: 10, Minor: 0, Build: 19045}
	win2025   = WinVersion{Major: 10, Minor: 0, Build: 26100, Server: true}
	winVista  = WinVersion{Major: 6, Minor: 0, Build: 6002}
	unknownOS = WinVersion{}
)

func TestWinVersionDecisions(t *testing.T) {
	for _, c := range []struct {
		name                                     string
		v                                        WinVersion
		nt6, dxgi, aero, sha2Maybe, wv2Unsupport bool
		str                                      string
	}{
		{"7 SP1", win7SP1, true, false, true, true, true, "6.1.7601"},
		{"2008 R2", win2008R2, true, false, true, true, true, "6.1.7601"},
		{"Vista", winVista, true, false, false, true, true, "6.0.6002"},
		{"2012", win2012, true, true, false, false, true, "6.2.9200"},
		{"8.1", win81, true, true, false, false, true, "6.3.9600"},
		{"2012 R2", win2012R2, true, true, false, false, true, "6.3.9600"},
		{"10", win10, false, true, false, false, false, "10.0.19045"},
		{"2025", win2025, false, true, false, false, false, "10.0.26100"},
		{"unknown", unknownOS, false, true, false, false, false, ""},
	} {
		v := c.v
		if v.NeedsNT6() != c.nt6 || v.DXGIDuplicationPossible() != c.dxgi || v.CanDisableComposition() != c.aero ||
			v.SHA2SigningMayBeMissing() != c.sha2Maybe || v.WebView2Unsupported() != c.wv2Unsupport || v.String() != c.str {
			t.Errorf("%s: nt6=%v dxgi=%v aero=%v sha2=%v wv2unsupported=%v str=%q", c.name,
				v.NeedsNT6(), v.DXGIDuplicationPossible(), v.CanDisableComposition(), v.SHA2SigningMayBeMissing(), v.WebView2Unsupported(), v.String())
		}
	}
}

func TestCompositionOffForSession(t *testing.T) {
	if !CompositionOffForSession(win7SP1, false) || !CompositionOffForSession(win2008R2, false) {
		t.Fatal("Windows 7 / 2008 R2 switch Aero off for the session")
	}
	if CompositionOffForSession(win7SP1, true) {
		t.Fatal("KeepAeroDuringSession=1 keeps Aero")
	}
	for _, v := range []WinVersion{win2012, win81, win10, unknownOS} {
		if CompositionOffForSession(v, false) {
			t.Fatalf("%v: composition cannot / must not be switched off", v)
		}
	}
}

func TestSHA2SignatureUnsupported(t *testing.T) {
	asked := 0
	kb := func(installed bool) func() bool { return func() bool { asked++; return installed } }
	if !SHA2SignatureUnsupported(win7SP1, kb(false)) {
		t.Fatal("Windows 7 without KB4474419 / KB3033929: sha2_unsupported")
	}
	if SHA2SignatureUnsupported(win2008R2, kb(true)) {
		t.Fatal("KB installed: a real signature failure")
	}
	asked = 0
	for _, v := range []WinVersion{win2012, win81, win10, unknownOS} {
		if SHA2SignatureUnsupported(v, kb(false)) {
			t.Fatalf("%v always verifies SHA-2", v)
		}
	}
	if asked != 0 {
		t.Fatal("the servicing packages are only read on Windows 7 / 2008 R2")
	}
	for name, want := range map[string]bool{
		"Package_for_KB4474419~31bf3856ad364e35~amd64~~6.1.3.2": true,
		"package_for_kb3033929~31bf3856ad364e35~x86~~6.1.1.0":   true,
		"Package_for_KB4474419_SP1~31bf3856ad364e35~amd64~~6.1": true,
		"Package_for_KB4490628~31bf3856ad364e35~amd64~~6.1.1.0": false,
		"Package_for_KB447441~31bf3856ad364e35":                 false,
	} {
		if SHA2KBPackageName(name) != want {
			t.Errorf("sha2KBPackageName(%q) != %v", name, want)
		}
	}
}
