//go:build linux

package engine

// Viewer key → Linux evdev key code (linux/input-event-codes.h), for the
// RemoteDesktop portal's NotifyKeyboardKeycode. The viewer sends the
// physical key (KeyboardEvent.code, a position on a US layout); the
// compositor applies the remote user's own layout to it, like a local
// keyboard. Characters without a physical key (an IME, a virtual keyboard:
// empty code) go as keysyms instead (keysymForRune).

// evdevByCode maps KeyboardEvent.code to the evdev key code.
var evdevByCode = map[string]int32{
	"Escape": 1,
	"Digit1": 2, "Digit2": 3, "Digit3": 4, "Digit4": 5, "Digit5": 6,
	"Digit6": 7, "Digit7": 8, "Digit8": 9, "Digit9": 10, "Digit0": 11,
	"Minus": 12, "Equal": 13, "Backspace": 14, "Tab": 15,
	"KeyQ": 16, "KeyW": 17, "KeyE": 18, "KeyR": 19, "KeyT": 20,
	"KeyY": 21, "KeyU": 22, "KeyI": 23, "KeyO": 24, "KeyP": 25,
	"BracketLeft": 26, "BracketRight": 27, "Enter": 28, "ControlLeft": 29,
	"KeyA": 30, "KeyS": 31, "KeyD": 32, "KeyF": 33, "KeyG": 34,
	"KeyH": 35, "KeyJ": 36, "KeyK": 37, "KeyL": 38,
	"Semicolon": 39, "Quote": 40, "Backquote": 41, "ShiftLeft": 42, "Backslash": 43,
	"KeyZ": 44, "KeyX": 45, "KeyC": 46, "KeyV": 47, "KeyB": 48, "KeyN": 49, "KeyM": 50,
	"Comma": 51, "Period": 52, "Slash": 53, "ShiftRight": 54,
	"NumpadMultiply": 55, "AltLeft": 56, "Space": 57, "CapsLock": 58,
	"F1": 59, "F2": 60, "F3": 61, "F4": 62, "F5": 63, "F6": 64, "F7": 65, "F8": 66, "F9": 67, "F10": 68,
	"NumLock": 69, "ScrollLock": 70,
	"Numpad7": 71, "Numpad8": 72, "Numpad9": 73, "NumpadSubtract": 74,
	"Numpad4": 75, "Numpad5": 76, "Numpad6": 77, "NumpadAdd": 78,
	"Numpad1": 79, "Numpad2": 80, "Numpad3": 81, "Numpad0": 82, "NumpadDecimal": 83,
	"Lang5": 85, "IntlBackslash": 86, "F11": 87, "F12": 88, "IntlRo": 89,
	"Lang3": 90, "Lang4": 91, "Convert": 92, "KanaMode": 93, "NonConvert": 94,
	"NumpadEnter": 96, "ControlRight": 97, "NumpadDivide": 98, "PrintScreen": 99, "AltRight": 100,
	"Home": 102, "ArrowUp": 103, "PageUp": 104, "ArrowLeft": 105, "ArrowRight": 106,
	"End": 107, "ArrowDown": 108, "PageDown": 109, "Insert": 110, "Delete": 111,
	"AudioVolumeMute": 113, "VolumeMute": 113, "AudioVolumeDown": 114, "VolumeDown": 114,
	"AudioVolumeUp": 115, "VolumeUp": 115, "Power": 116, "NumpadEqual": 117, "Pause": 119,
	"NumpadComma": 121, "Lang1": 122, "Lang2": 123, "IntlYen": 124,
	"MetaLeft": 125, "OSLeft": 125, "MetaRight": 126, "OSRight": 126, "ContextMenu": 127,
	"BrowserStop": 128, "Again": 129, "Props": 130, "Undo": 131, "Copy": 133, "Open": 134,
	"Paste": 135, "Find": 136, "Cut": 137, "Help": 138, "LaunchApp2": 140, "Sleep": 142,
	"WakeUp": 143, "LaunchApp1": 157, "LaunchMail": 155, "BrowserFavorites": 156,
	"BrowserBack": 158, "BrowserForward": 159, "Eject": 161, "MediaTrackNext": 163,
	"MediaPlayPause": 164, "MediaTrackPrevious": 165, "MediaStop": 166, "BrowserRefresh": 173,
	"NumpadParenLeft": 179, "NumpadParenRight": 180,
	"F13": 183, "F14": 184, "F15": 185, "F16": 186, "F17": 187, "F18": 188,
	"F19": 189, "F20": 190, "F21": 191, "F22": 192, "F23": 193, "F24": 194,
	"BrowserSearch": 217, "MediaSelect": 226, "BrowserHome": 172,
}

// Evdev codes of the modifiers, by group (bit order of modProtectMask: 0
// Ctrl, 1 Shift, 2 Alt, 3 Meta) and the Windows VK the shared modifier
// tracking uses.
var evdevModGroups = [4][2]int32{{29, 97}, {42, 54}, {56, 100}, {125, 126}}
var evdevModVK = [4]int{0x11, 0x10, 0x12, 0x5B}

// evdevModGroup is the group of a modifier key code, -1 otherwise.
func evdevModGroup(code int32) int {
	for g, codes := range evdevModGroups {
		if code == codes[0] || code == codes[1] {
			return g
		}
	}
	return -1
}

// evdevByVK maps the Windows VK codes the engine itself may inject (Caps
// Lock alignment, Shift around a character: the generic path) to evdev.
var evdevByVK = map[int]int32{
	0x08: 14, 0x09: 15, 0x0D: 28, 0x10: 42, 0x11: 29, 0x12: 56, 0x13: 119, 0x14: 58,
	0x1B: 1, 0x20: 57, 0x21: 104, 0x22: 109, 0x23: 107, 0x24: 102,
	0x25: 105, 0x26: 103, 0x27: 106, 0x28: 108, 0x2C: 99, 0x2D: 110, 0x2E: 111,
	0x5B: 125, 0x5C: 126, 0x5D: 127, 0x90: 69, 0x91: 70,
	0xBA: 39, 0xBB: 13, 0xBC: 51, 0xBD: 12, 0xBE: 52, 0xBF: 53, 0xC0: 41,
	0xDB: 26, 0xDC: 43, 0xDD: 27, 0xDE: 40,
}

func evdevFromVK(vk int) int32 {
	if c, ok := evdevByVK[vk]; ok {
		return c
	}
	switch {
	case vk >= 0x41 && vk <= 0x5A: // A..Z
		return evdevByCode["Key"+string(rune(vk))]
	case vk >= 0x30 && vk <= 0x39: // 0..9
		return evdevByCode["Digit"+string(rune(vk))]
	case vk >= 0x70 && vk <= 0x7B: // F1..F12
		return evdevByCode["F"+itoaSmall(vk-0x70+1)]
	}
	return 0
}

func itoaSmall(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// Mouse buttons (linux/input-event-codes.h) by MouseEvent.button.
const (
	evdevBtnLeft   = 0x110
	evdevBtnRight  = 0x111
	evdevBtnMiddle = 0x112
	evdevBtnSide   = 0x113 // back
	evdevBtnExtra  = 0x114 // forward
)

func evdevButton(button int) int32 {
	switch button {
	case 1:
		return evdevBtnMiddle
	case 2:
		return evdevBtnRight
	case 3:
		return evdevBtnSide
	case 4:
		return evdevBtnExtra
	}
	return evdevBtnLeft
}

// keysymForRune is the X keysym of a character (what NotifyKeyboardKeysym
// takes): Latin-1 keysyms are the code point, the rest 0x01000000 + code
// point; 0 for control characters.
func keysymForRune(r rune) int32 {
	switch {
	case r < 0x20 || r == 0x7F || (r >= 0x80 && r < 0xA0) || r > 0x10FFFF:
		return 0
	case r < 0x100:
		return int32(r)
	}
	return 0x01000000 | int32(r)
}
