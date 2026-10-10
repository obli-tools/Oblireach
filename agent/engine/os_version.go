package engine

import (
	"fmt"
	"strings"
)

// WinVersion is the real Windows version (RtlGetVersion: not subject to the
// manifest-based version lie of GetVersionEx). Zero off Windows or when the
// query failed: every decision below then keeps the modern behaviour.
type WinVersion struct {
	Major, Minor, Build uint32
	SPMajor             uint16
	Server              bool // ProductType != VER_NT_WORKSTATION
}

func (v WinVersion) known() bool { return v.Major > 0 }

// String: "6.1.7601" ("" when unknown).
func (v WinVersion) String() string {
	if !v.known() {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Build)
}

// needsNT6: Windows 7 SP1 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 (NT 6.x)
// run the nt6 build line (oblireach-agent-nt6.msi).
func (v WinVersion) NeedsNT6() bool { return v.known() && v.Major < 10 }

// dxgiDuplicationPossible: DXGI Desktop Duplication (IDXGIOutput1::
// DuplicateOutput) exists from Windows 8 / Server 2012 (6.2, build 9200).
// On Windows 7 / 2008 R2 it never works (IDXGIOutput1 missing without the
// Platform Update, DXGI_ERROR_UNSUPPORTED with it): the capture must not
// even try (each try creates a D3D11 device). Unknown version: try.
func (v WinVersion) DXGIDuplicationPossible() bool {
	if !v.known() {
		return true
	}
	if v.Major != 6 {
		return v.Major > 6
	}
	return v.Minor >= 2 && (v.Build == 0 || v.Build >= 9200)
}

// canDisableComposition: Windows 7 / Server 2008 R2 (6.1) is the last
// Windows where DWM desktop composition (Aero) can be switched off by a
// program (DwmEnableComposition); from Windows 8 it is always on.
func (v WinVersion) CanDisableComposition() bool {
	return v.known() && v.Major == 6 && v.Minor == 1
}

// sha2SigningMayBeMissing: Windows Vista / 7 / Server 2008 / 2008 R2 (6.0,
// 6.1) verify SHA-256 Authenticode signatures only with KB3033929 or
// KB4474419 installed. Later Windows always can.
func (v WinVersion) SHA2SigningMayBeMissing() bool {
	return v.known() && v.Major == 6 && v.Minor <= 1
}

// webview2Unsupported: WebView2 needs Windows 10 / Server 2016 or later
// (the Runtime stopped at 109 on Windows 7 / 8.1 / 2008 R2 - 2012 R2).
func (v WinVersion) WebView2Unsupported() bool { return v.known() && v.Major < 10 }

// SHA2SignatureUnsupported: a refused update signature is due to the missing
// SHA-2 code signing support of Windows 7 / 2008 R2 (kbInstalled is only
// asked there).
func SHA2SignatureUnsupported(v WinVersion, kbInstalled func() bool) bool {
	return v.SHA2SigningMayBeMissing() && !kbInstalled()
}

// SHA2KBPackageName: a Component Based Servicing package of KB4474419 or
// KB3033929 ("Package_for_KB4474419~31bf3856ad364e35~amd64~~6.1.3.2").
func SHA2KBPackageName(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"package_for_kb4474419", "package_for_kb3033929"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// CompositionOffForSession: the capture helper switches DWM composition
// (Aero) off for the session — Windows 7 / 2008 R2 only (GDI capture), unless
// the machine opted out (KeepAeroDuringSession = 1, dwm_windows.go).
func CompositionOffForSession(v WinVersion, keepAero bool) bool {
	return v.CanDisableComposition() && !keepAero
}
