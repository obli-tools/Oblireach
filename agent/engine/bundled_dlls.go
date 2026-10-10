package engine

import "sync/atomic"

// bundledDLLsOff is the inverse of SetBundledDLLs (zero value: allowed).
var bundledDLLsOff atomic.Bool

// SetBundledDLLs allows (default) or forbids, for the whole process, loading
// the third-party codec DLLs shipped next to the executable (OpenH264,
// libvpx, SVT-AV1 and their MinGW runtime, codecdll_windows.go). Forbidden:
// no DLL is ever looked for in that folder and the encoders fall back to
// the system ones (Media Foundation H.264, then JPEG). A portable host run
// from a user-writable folder (Downloads) forbids them. Call it before
// RunSession.
func SetBundledDLLs(enabled bool) { bundledDLLsOff.Store(!enabled) }

// BundledDLLsEnabled reports the SetBundledDLLs setting.
func BundledDLLsEnabled() bool { return !bundledDLLsOff.Load() }
