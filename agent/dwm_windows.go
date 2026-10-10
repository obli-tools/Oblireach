//go:build windows

package main

// Windows 7 / Server 2008 R2 performance: the capture there is GDI (no DXGI
// Desktop Duplication before Windows 8), and a GDI BitBlt of a composed
// (Aero) desktop is several times slower than of an uncomposed one. The
// capture helper switches desktop composition off for the time of the
// session, as remote-control tools did on Windows 7, and back on when it
// ends. DwmEnableComposition is honoured for the calling process only:
// Windows itself turns composition back on when the helper exits, however it
// exits (crash, kill, session end). From Windows 8 composition cannot be
// switched off (the call is a no-op): nothing is done there.
//
// Opt-out per machine: HKLM\SOFTWARE\OblireachAgent, DWORD
// KeepAeroDuringSession = 1.

import (
	"log"
	"syscall"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

const (
	dwmECDisableComposition = 0
	dwmECEnableComposition  = 1
)

// compositionKeepAeroValue: the opt-out registry value (DWORD, 1 = keep Aero).
const compositionKeepAeroValue = "KeepAeroDuringSession"

// win7CompositionOffForSession switches DWM composition off when this is
// Windows 7 / 2008 R2, composition is on and the machine did not opt out.
// Returns the function that restores it (always non-nil; idempotent).
func win7CompositionOffForSession() func() {
	noop := func() {}
	keep, _ := regReadDWORD(syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\OblireachAgent`, compositionKeepAeroValue)
	if !engine.CompositionOffForSession(engine.WindowsVersion(), keep == 1) {
		return noop
	}
	dwm := syscall.NewLazyDLL(systemDir() + `\dwmapi.dll`)
	isOn := dwm.NewProc("DwmIsCompositionEnabled")
	enable := dwm.NewProc("DwmEnableComposition")
	if isOn.Find() != nil || enable.Find() != nil {
		return noop
	}
	var on int32
	if hr, _, _ := isOn.Call(uintptr(unsafe.Pointer(&on))); hr != 0 || on == 0 {
		return noop // already off (basic theme, RDP session, policy): nothing to restore
	}
	if hr, _, _ := enable.Call(dwmECDisableComposition); hr != 0 {
		log.Printf("helper: Windows 7: DwmEnableComposition(off) failed (0x%08X), Aero left on", uint32(hr))
		return noop
	}
	log.Printf("helper: Windows 7: desktop composition (Aero) switched off for the session (GDI capture is faster); opt-out: HKLM\\SOFTWARE\\OblireachAgent %s=1", compositionKeepAeroValue)
	done := false
	return func() {
		if done {
			return
		}
		done = true
		if hr, _, _ := enable.Call(dwmECEnableComposition); hr != 0 {
			log.Printf("helper: Windows 7: DwmEnableComposition(on) failed (0x%08X); Windows restores it when this helper exits", uint32(hr))
			return
		}
		log.Printf("helper: Windows 7: desktop composition (Aero) restored")
	}
}
