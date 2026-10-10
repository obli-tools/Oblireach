//go:build windows

package engine

// Helper process priority for the duration of a remote session.
//
// Capture + encode compete with the user's own apps for CPU. Raising the
// helper to ABOVE_NORMAL keeps the stream fluid under load without the
// starvation HIGH / REALTIME would cause to the user's foreground apps and to
// the input / desktop threads (which would make the machine feel slower, the
// opposite of what we want). The change is restored when the stream ends; the
// helper also exits at session end, so nothing long-lived keeps a raised
// priority.
//
// Lazily resolved (no static import): the nt6 build is unaffected.

var (
	hpKernel32          = newSystemLazyDLL("kernel32.dll")
	hpGetCurrentProcess = hpKernel32.NewProc("GetCurrentProcess")
	hpGetPriorityClass  = hpKernel32.NewProc("GetPriorityClass")
	hpSetPriorityClass  = hpKernel32.NewProc("SetPriorityClass")
)

const (
	hpNormalPriorityClass      = 0x00000020
	hpAboveNormalPriorityClass = 0x00008000
)

// raiseHelperPriority bumps this process to ABOVE_NORMAL and returns a function
// that restores the exact class it had (NORMAL if it could not be read). Safe
// to call more than once; the returned closure is safe to call once.
func raiseHelperPriority() func() {
	if hpSetPriorityClass.Find() != nil {
		return func() {}
	}
	h, _, _ := hpGetCurrentProcess.Call()
	prev := uintptr(hpNormalPriorityClass)
	if hpGetPriorityClass.Find() == nil {
		if r, _, _ := hpGetPriorityClass.Call(h); r != 0 {
			prev = r
		}
	}
	if prev == hpAboveNormalPriorityClass {
		return func() {} // already there (nested session): leave it, restore nothing
	}
	hpSetPriorityClass.Call(h, hpAboveNormalPriorityClass)
	restored := false
	return func() {
		if restored {
			return
		}
		restored = true
		hpSetPriorityClass.Call(h, prev)
	}
}
