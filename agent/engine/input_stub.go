//go:build !windows && !darwin && !linux

package engine

func setInputMonitorOffset(x, y int)                  {}
func inputMouseMove(x, y int)                          {}
func inputMouseButton(button int, down bool, x, y int) {}
func inputMouseScroll(delta int)                        {}
func inputKey(vk int, down bool)                        {}
func InputSAS()                                          {}
func inputVKFromKey(key string) (int, int)               { return 0, 0 }
func clipboardGet() string                               { return "" }
func clipboardSet(text string)                           {}
// Local-input block: no mechanism on this platform, so the block is a no-op
// that always reports "not blocked". input_block_local is not advertised.
func platformInputBlockSupported() bool      { return false }
func platformInputBlockApply(want bool) bool { return false }
func inputSwitchActiveDesktop()                         {}
func inputAttachToDefaultDesktop() string               { return "" }
func logInputEventImpl(msg string)          {}

// Modifier reconciliation (stuck Ctrl): implemented on Windows only for now.
func inputReleaseStaleModifiers(ctrl, shift, alt, meta bool, protect int) {}
func inputReleaseAllModifiers()                               {}

// EnablePerMonitorDPIAwareness is a Windows concept — no-op elsewhere.
func EnablePerMonitorDPIAwareness() {}

// inputMoveDesktop / inputButtonDesktop map PHYSICAL desktop coords. The view
// path that calls them is Windows-only today (curView is set by the Windows
// capture); these keep the package compiling and stay best-effort elsewhere.
func inputMoveDesktop(vx, vy int)                        { inputMouseMove(vx, vy) }
func inputButtonDesktop(button int, down bool, vx, vy int) { inputMouseButton(button, down, vx, vy) }
