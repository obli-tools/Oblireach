//go:build linux

package engine

// Linux input: the engine's input functions go to the backend that matches
// the capture (capture_linux.go): the RemoteDesktop portal when the capture
// runs through the portal (input_portal_linux.go), XTest otherwise
// (input_x11_linux.go).

import "sync"

// linuxInput is one input backend. Mouse positions are frame pixels of the
// captured monitor; key() takes a Windows VK code (dispatchInputJSON's
// generic path), keyEvent() a whole viewer key message (true = handled, the
// VK path is skipped).
type linuxInput interface {
	setOffset(x, y int)
	mouseMove(x, y int)
	mouseButton(button int, down bool, x, y int)
	mouseScroll(delta int)
	key(vk int, down bool)
	vkFromKey(key string) (int, int)
	keyEvent(in macKeyIn) bool
	releaseStale(ctrl, shift, alt, meta bool, protect int)
	releaseAll()
}

var linuxIn struct {
	mu  sync.Mutex
	cur linuxInput // nil: X11
	x11 linuxInput
}

// setLinuxInput selects the input backend (nil: XTest). The previous one
// releases what it still holds.
func setLinuxInput(in linuxInput) {
	linuxIn.mu.Lock()
	prev := linuxIn.cur
	linuxIn.cur = in
	linuxIn.mu.Unlock()
	if prev != nil && prev != in {
		prev.releaseAll()
	}
}

func curLinuxInput() linuxInput {
	linuxIn.mu.Lock()
	defer linuxIn.mu.Unlock()
	if linuxIn.cur != nil {
		return linuxIn.cur
	}
	if linuxIn.x11 == nil {
		linuxIn.x11 = newX11Input()
	}
	return linuxIn.x11
}

func init() {
	// The whole key message goes to the backend first: the portal maps the
	// viewer's physical key code to an evdev key code (keymap_evdev_linux.go);
	// X11 declines and keeps the VK path.
	inputKeyEventHook = func(in macKeyIn) bool { return curLinuxInput().keyEvent(in) }
}

func setInputMonitorOffset(x, y int) { curLinuxInput().setOffset(x, y) }
func inputMouseMove(x, y int)        { curLinuxInput().mouseMove(x, y) }
func inputMouseButton(button int, down bool, x, y int) {
	curLinuxInput().mouseButton(button, down, x, y)
}
func inputMouseScroll(delta int)           { curLinuxInput().mouseScroll(delta) }
func inputKey(vk int, down bool)           { curLinuxInput().key(vk, down) }
func inputVKFromKey(key string) (int, int) { return curLinuxInput().vkFromKey(key) }

func InputSAS() {} // no secure attention sequence on Linux

// Clipboard: not offered on Linux yet (the direct path does not advertise
// clipboard_text). Under Wayland it needs the portal's Clipboard interface
// (RequestClipboard before Start, SelectionRead / SetSelection +
// SelectionWrite), available on GNOME, to be checked on KDE.
func clipboardGet() string     { return "" }
func clipboardSet(text string) {}

// Local-input block: Wayland has no way to freeze the local user without
// freezing the injected input too (and XTest neither): never advertised,
// always reports "not blocked".
func platformInputBlockSupported() bool      { return false }
func platformInputBlockApply(want bool) bool { return false }

func inputSwitchActiveDesktop()           {} // Windows-only concept
func inputAttachToDefaultDesktop() string { return "" }
func logInputEventImpl(msg string)        {}

// Modifier reconciliation (stuck Ctrl): the portal backend releases the
// modifiers it injected that the viewer reports as up.
func inputReleaseStaleModifiers(ctrl, shift, alt, meta bool, protect int) {
	curLinuxInput().releaseStale(ctrl, shift, alt, meta, protect)
}
func inputReleaseAllModifiers() { curLinuxInput().releaseAll() }

// EnablePerMonitorDPIAwareness is a Windows concept: no-op.
func EnablePerMonitorDPIAwareness() {}

// inputMoveDesktop / inputButtonDesktop: the view path (Windows capture only)
// never runs on Linux (no view is published); best effort.
func inputMoveDesktop(vx, vy int)                          { inputMouseMove(vx, vy) }
func inputButtonDesktop(button int, down bool, vx, vy int) { inputMouseButton(button, down, vx, vy) }
