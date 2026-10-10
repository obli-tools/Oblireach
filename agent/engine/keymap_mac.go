package engine

// macOS keyboard mapping (feature "key_map_mac").
//
// An operator on Windows / Linux driving a Mac: the Windows key must act as
// Cmd, Ctrl+C / V / X / A / Z… must do what they do on Windows (Cmd+… on a
// Mac), and what is typed must be the operator's characters whatever the
// Mac's own keyboard layout (an AZERTY PC driving a QWERTY Mac).
//
// The viewer decides (setting "Mac keyboard mapping": Automatic / Off, with
// the sub-option "Ctrl → Cmd for shortcuts") and stamps every key message with
// `macMap`:
//
//	absent / 0  no mapping: keys go by position (browser `code`), modifiers
//	            as they are (Ctrl = Control, Alt = Option, Win/Meta = Cmd).
//	            Old viewers, an operator on a Mac, or the setting Off.
//	1           mapping: printable characters are typed as Unicode (the
//	            operator's layout wins, like the Windows text path), Win/Meta
//	            = Cmd, Alt = Option, AltGr types its character (no modifier
//	            reaches the Mac), Caps Lock is irrelevant (characters).
//	2           mapping + "Ctrl → Cmd": the Windows shortcuts are translated
//	            (Ctrl+C → Cmd+C, Ctrl+Y → Cmd+Shift+Z, Ctrl+Arrows → Option+
//	            Arrows, Ctrl+Home/End → Cmd+Up/Down, Home/End → Cmd+Left/Right,
//	            Ctrl+Backspace/Delete → Option+…). Ctrl with any other key
//	            stays Control (terminal Ctrl+D, Ctrl+L…).
//
// Only the darwin agent acts on it (input_darwin.go sets inputKeyEventHook);
// Windows / Linux agents ignore the field: no change for those targets. This
// file is PURE Go (no build tag, no CGo) so the whole decision table is unit
// tested on any OS (keymap_mac_test.go); the darwin glue only posts the
// planned events (CGEvent) with exactly the flags computed here — flags are
// set on every event, never inferred from what the Mac thinks is held.

import (
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// featKeyMapMac: the agent applies `macMap` on key messages (darwin only).
const featKeyMapMac = "key_map_mac"

// macKeyMapSupported: advertised by the darwin agent only.
func macKeyMapSupported() bool { return runtime.GOOS == "darwin" }

// CGEventFlags.
const (
	macFlagAlphaShift = 0x00010000
	macFlagShift      = 0x00020000
	macFlagControl    = 0x00040000
	macFlagOption     = 0x00080000
	macFlagCommand    = 0x00100000
	macFlagNumPad     = 0x00200000
	macFlagFn         = 0x00800000
)

// macOS virtual key codes (HIToolbox kVK_*) used by name below.
const (
	mkReturn        = 36
	mkTab           = 48
	mkSpace         = 49
	mkBackspace     = 51
	mkEscape        = 53
	mkCmdR          = 54
	mkCmdL          = 55
	mkShiftL        = 56
	mkCapsLock      = 57
	mkOptionL       = 58
	mkControlL      = 59
	mkShiftR        = 60
	mkOptionR       = 61
	mkControlR      = 62
	mkHelp          = 114
	mkHome          = 115
	mkPageUp        = 116
	mkForwardDelete = 117
	mkEnd           = 119
	mkPageDown      = 121
	mkLeft          = 123
	mkRight         = 124
	mkDown          = 125
	mkUp            = 126
)

// macCodeKeycodes: browser KeyboardEvent.code → macOS key code (position on
// the keyboard, same table as Chromium's dom_code_data).
var macCodeKeycodes = map[string]int{
	"KeyA": 0, "KeyS": 1, "KeyD": 2, "KeyF": 3, "KeyH": 4, "KeyG": 5, "KeyZ": 6,
	"KeyX": 7, "KeyC": 8, "KeyV": 9, "IntlBackslash": 10, "KeyB": 11, "KeyQ": 12,
	"KeyW": 13, "KeyE": 14, "KeyR": 15, "KeyY": 16, "KeyT": 17,
	"Digit1": 18, "Digit2": 19, "Digit3": 20, "Digit4": 21, "Digit6": 22,
	"Digit5": 23, "Equal": 24, "Digit9": 25, "Digit7": 26, "Minus": 27,
	"Digit8": 28, "Digit0": 29, "BracketRight": 30, "KeyO": 31, "KeyU": 32,
	"BracketLeft": 33, "KeyI": 34, "KeyP": 35, "Enter": mkReturn, "KeyL": 37,
	"KeyJ": 38, "Quote": 39, "KeyK": 40, "Semicolon": 41, "Backslash": 42,
	"Comma": 43, "Slash": 44, "KeyN": 45, "KeyM": 46, "Period": 47,
	"Tab": mkTab, "Space": mkSpace, "Backquote": 50, "Backspace": mkBackspace,
	"Escape":    mkEscape,
	"MetaRight": mkCmdR, "MetaLeft": mkCmdL, "OSRight": mkCmdR, "OSLeft": mkCmdL,
	"ShiftLeft": mkShiftL, "CapsLock": mkCapsLock, "AltLeft": mkOptionL,
	"ControlLeft": mkControlL, "ShiftRight": mkShiftR, "AltRight": mkOptionR,
	"ControlRight":  mkControlR,
	"NumpadDecimal": 65, "NumpadMultiply": 67, "NumpadAdd": 69, "NumLock": 71,
	"NumpadDivide": 75, "NumpadEnter": 76, "NumpadSubtract": 78, "NumpadEqual": 81,
	"Numpad0": 82, "Numpad1": 83, "Numpad2": 84, "Numpad3": 85, "Numpad4": 86,
	"Numpad5": 87, "Numpad6": 88, "Numpad7": 89, "Numpad8": 91, "Numpad9": 92,
	"F1": 122, "F2": 120, "F3": 99, "F4": 118, "F5": 96, "F6": 97, "F7": 98,
	"F8": 100, "F9": 101, "F10": 109, "F11": 103, "F12": 111,
	"F13": 105, "F14": 107, "F15": 113, "F16": 106, "F17": 64, "F18": 79,
	"F19": 80, "F20": 90,
	"Insert": mkHelp, "Home": mkHome, "PageUp": mkPageUp, "Delete": mkForwardDelete,
	"End": mkEnd, "PageDown": mkPageDown,
	"ArrowLeft": mkLeft, "ArrowRight": mkRight, "ArrowDown": mkDown, "ArrowUp": mkUp,
	"ContextMenu":   110,
	"AudioVolumeUp": 72, "AudioVolumeDown": 73, "AudioVolumeMute": 74,
}

// macKeyNameCodes: KeyboardEvent.key names → code, for messages without a
// code (soft keyboards, panels that only send `key`).
var macKeyNameCodes = map[string]string{
	"Enter": "Enter", "Tab": "Tab", "Backspace": "Backspace", "Escape": "Escape",
	"Esc": "Escape", "Delete": "Delete", "Del": "Delete", "Insert": "Insert",
	"Home": "Home", "End": "End", "PageUp": "PageUp", "PageDown": "PageDown",
	"ArrowLeft": "ArrowLeft", "ArrowRight": "ArrowRight", "ArrowUp": "ArrowUp",
	"ArrowDown": "ArrowDown", "Left": "ArrowLeft", "Right": "ArrowRight",
	"Up": "ArrowUp", "Down": "ArrowDown",
	"Shift": "ShiftLeft", "Control": "ControlLeft", "Alt": "AltLeft",
	"Meta": "MetaLeft", "OS": "MetaLeft", "AltGraph": "AltRight", "CapsLock": "CapsLock",
	"F1": "F1", "F2": "F2", "F3": "F3", "F4": "F4", "F5": "F5", "F6": "F6",
	"F7": "F7", "F8": "F8", "F9": "F9", "F10": "F10", "F11": "F11", "F12": "F12",
	"ContextMenu": "ContextMenu",
}

// macUSKeycodes: character → key code on a US (ANSI QWERTY) Mac, the fallback
// for a shortcut when the Mac's own layout could not be read.
var macUSKeycodes = map[rune]int{
	'a': 0, 's': 1, 'd': 2, 'f': 3, 'h': 4, 'g': 5, 'z': 6, 'x': 7, 'c': 8, 'v': 9,
	'b': 11, 'q': 12, 'w': 13, 'e': 14, 'r': 15, 'y': 16, 't': 17,
	'1': 18, '2': 19, '3': 20, '4': 21, '6': 22, '5': 23, '=': 24, '9': 25, '7': 26,
	'-': 27, '8': 28, '0': 29, ']': 30, 'o': 31, 'u': 32, '[': 33, 'i': 34, 'p': 35,
	'l': 37, 'j': 38, '\'': 39, 'k': 40, ';': 41, '\\': 42, ',': 43, '/': 44,
	'n': 45, 'm': 46, '.': 47, '`': 50,
}

// macCtrlShortcuts: with "Ctrl → Cmd", Ctrl + these characters become Cmd +
// the same character (copy, paste, cut, select all, undo, redo, save, find,
// new tab, close, new, quit, reload, print).
var macCtrlShortcuts = map[rune]bool{
	'c': true, 'v': true, 'x': true, 'a': true, 'z': true, 'y': true, 's': true,
	'f': true, 't': true, 'w': true, 'n': true, 'q': true, 'r': true, 'p': true,
}

// macKeyIn is one viewer key message.
type macKeyIn struct {
	Down                   bool
	Key, Code              string
	Ctrl, Shift, Alt, Meta bool
	Caps                   *bool // operator's Caps Lock (key-down of newer viewers), nil = unknown
	Mode                   int   // macMap: 0 none, 1 map, 2 map + Ctrl → Cmd
}

// Planned operations.
const (
	macOpKey  = iota // Keycode Down/up with Flags
	macOpChar        // type Char (down + up) with Keycode and Flags
)

type macKeyOp struct {
	Kind    int
	Keycode int
	Down    bool
	Char    string
	Flags   uint64
}

type macHeld struct {
	swallow bool // the press was swallowed or typed as a character: the release is too
	keycode int
	flags   uint64 // flags of the press (a non-modifier key goes up with them)
}

// macKeyMapper turns key messages into macOS events. One per process (the
// darwin agent serves one console); safe for concurrent use.
type macKeyMapper struct {
	mu                     sync.Mutex
	held                   map[string]macHeld // key id → what its press sent
	mods                   map[int]time.Time  // modifier key codes injected down → when
	mode                   int                // macMap of the last key message
	ctrl, shift, alt, meta bool               // operator's modifiers (last key message)
	caps                   *bool              // operator's Caps Lock, last known

	// layoutKeycode: the key code producing r (unmodified) on the Mac's
	// current layout (darwin glue); nil or !ok → US position.
	layoutKeycode func(r rune) (int, bool)
	// remoteCaps: the Mac's Caps Lock state (darwin glue), for a viewer that
	// never said what the operator's is.
	remoteCaps func() bool
	now        func() time.Time
}

func newMacKeyMapper() *macKeyMapper {
	return &macKeyMapper{held: map[string]macHeld{}, mods: map[int]time.Time{}, now: time.Now}
}

// macModGroup: which operator flag keeps a modifier key code down.
func macModGroup(kc int) int {
	switch kc {
	case mkControlL, mkControlR:
		return 0
	case mkShiftL, mkShiftR:
		return 1
	case mkOptionL, mkOptionR:
		return 2
	case mkCmdL, mkCmdR:
		return 3
	}
	return -1
}

// macFlags builds CGEventFlags.
func macFlags(cmd, opt, ctl, shift bool) uint64 {
	var f uint64
	if cmd {
		f |= macFlagCommand
	}
	if opt {
		f |= macFlagOption
	}
	if ctl {
		f |= macFlagControl
	}
	if shift {
		f |= macFlagShift
	}
	return f
}

// macKeyExtraFlags: the flags a real Mac keyboard sets on these keys (arrows
// are "numeric pad" + "fn" keys, F-keys and the navigation block "fn"). Key
// bindings of text views expect them.
func macKeyExtraFlags(kc int) uint64 {
	switch kc {
	case mkLeft, mkRight, mkUp, mkDown:
		return macFlagNumPad | macFlagFn
	case mkHelp, mkHome, mkPageUp, mkForwardDelete, mkEnd, mkPageDown,
		122, 120, 99, 118, 96, 97, 98, 100, 101, 109, 103, 111, 105, 107, 113, 106, 64, 79, 80, 90:
		return macFlagFn
	case 65, 67, 69, 71, 75, 76, 78, 81, 82, 83, 84, 85, 86, 87, 88, 89, 91, 92:
		return macFlagNumPad
	}
	return 0
}

func macIsModifierCode(code string) bool {
	switch code {
	case "ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight",
		"MetaLeft", "MetaRight", "OSLeft", "OSRight", "CapsLock":
		return true
	}
	return false
}

// macPrintable: the message's key is one printable character.
func macPrintable(key string) (rune, bool) {
	if utf8.RuneCountInString(key) != 1 {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(key)
	if r == utf8.RuneError || !unicode.IsPrint(r) {
		return 0, false
	}
	return r, true
}

func macIsASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// macShortcutChar: the character a shortcut is about. The operator's layout
// wins for letters (AZERTY Ctrl+Z is code KeyW, key "z": undo). A digit row
// key on AZERTY reports a symbol ("&" for 1): the digit is used. A non-Latin
// layout (Cyrillic "с" on KeyC) falls back to the key's Latin position.
func macShortcutChar(key, code string) rune {
	r, ok := macPrintable(key)
	if ok && r < 0x80 && unicode.IsLetter(r) {
		return unicode.ToLower(r)
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		return rune(code[5])
	}
	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		return unicode.ToLower(rune(code[3]))
	}
	if ok {
		return unicode.ToLower(r)
	}
	return 0
}

// macCodeKeycode resolves a message's position (code, else key name).
func macCodeKeycode(code, key string) int {
	if kc, ok := macCodeKeycodes[code]; ok {
		return kc
	}
	if code == "" {
		if c, ok := macKeyNameCodes[key]; ok {
			return macCodeKeycodes[c]
		}
	}
	return -1
}

// keycodeForChar: the Mac key code that types r on the Mac's own layout,
// else its US position, else -1.
func (m *macKeyMapper) keycodeForChar(r rune) int {
	if m.layoutKeycode != nil {
		if kc, ok := m.layoutKeycode(r); ok {
			return kc
		}
	}
	if kc, ok := macUSKeycodes[r]; ok {
		return kc
	}
	return -1
}

func macKeyID(in macKeyIn) string {
	if in.Code != "" {
		return in.Code
	}
	return "key:" + in.Key
}

// MouseFlags: the modifiers a mouse event carries (Shift+click, Cmd+click…).
// With "Ctrl → Cmd", Ctrl+click (add to selection on Windows) is Cmd+click.
func (m *macKeyMapper) MouseFlags() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd, ctl := m.meta, m.ctrl
	if m.mode == 2 && ctl {
		ctl, cmd = false, true
	}
	return macFlags(cmd, m.alt, ctl, m.shift)
}

// capsFlag: Caps Lock as the operator has it (positional typing only).
func (m *macKeyMapper) capsFlag() uint64 {
	on := false
	if m.caps != nil {
		on = *m.caps
	} else if m.remoteCaps != nil {
		on = m.remoteCaps()
	}
	if on {
		return macFlagAlphaShift
	}
	return 0
}

// releaseStale: a modifier injected down whose operator flag is now up lost
// its key-up (tab switch with Ctrl held…): released before this key. A
// modifier pressed less than modProtectWindow ago is kept (a viewer that
// sends per-key flags, see protocol.go).
func (m *macKeyMapper) releaseStale(in macKeyIn, ops []macKeyOp) []macKeyOp {
	flag := [4]bool{in.Ctrl, in.Shift, in.Alt, in.Meta}
	now := m.now()
	for kc, at := range m.mods {
		g := macModGroup(kc)
		if g < 0 || flag[g] || now.Sub(at) < modProtectWindow {
			continue
		}
		delete(m.mods, kc)
		for id, h := range m.held {
			if !h.swallow && h.keycode == kc {
				delete(m.held, id)
			}
		}
		ops = append(ops, macKeyOp{Kind: macOpKey, Keycode: kc, Down: false,
			Flags: macFlags(in.Meta, in.Alt, in.Ctrl, in.Shift)})
	}
	return ops
}

// Plan returns the macOS events for one key message.
func (m *macKeyMapper) Plan(in macKeyIn) []macKeyOp {
	m.mu.Lock()
	defer m.mu.Unlock()
	mode := in.Mode
	if mode < 0 || mode > 2 {
		mode = 0
	}
	in.Mode = mode
	m.mode = mode
	m.ctrl, m.shift, m.alt, m.meta = in.Ctrl, in.Shift, in.Alt, in.Meta
	if in.Down && in.Caps != nil {
		c := *in.Caps
		m.caps = &c
	}
	var ops []macKeyOp
	ops = m.releaseStale(in, ops)
	id := macKeyID(in)

	if !in.Down {
		h, ok := m.held[id]
		if !ok {
			return ops // press never sent (or already released)
		}
		delete(m.held, id)
		if h.swallow {
			return ops
		}
		f := h.flags // same chord as the press (Ctrl+C sent as Cmd+C goes up as Cmd+C)
		if macModGroup(h.keycode) >= 0 {
			delete(m.mods, h.keycode)
			f = macFlags(in.Meta, in.Alt, in.Ctrl, in.Shift) // the modifier itself is up
		}
		return append(ops, macKeyOp{Kind: macOpKey, Keycode: h.keycode, Down: false, Flags: f})
	}

	code := in.Code
	if code == "" {
		if c, ok := macKeyNameCodes[in.Key]; ok && macIsModifierCode(c) {
			code = c
		}
	}

	// ── Modifier keys ────────────────────────────────────────────────────────
	if macIsModifierCode(code) {
		kc := macCodeKeycodes[code]
		swallow := false
		switch {
		case code == "CapsLock":
			// Never toggled on the Mac: the flags of every event carry the
			// operator's state (positional typing) or characters are typed.
			swallow = true
		case code == "AltRight" && mode >= 1 && in.Key == "AltGraph":
			swallow = true // AltGr types its character, no Option reaches the Mac
		case (code == "ControlLeft" || code == "ControlRight") && mode == 2:
			swallow = true // Ctrl is decided per key (shortcut → Cmd, else Control flag)
		}
		if swallow {
			m.held[id] = macHeld{swallow: true}
			return ops
		}
		if _, dup := m.mods[kc]; !dup {
			m.mods[kc] = m.now()
		}
		m.held[id] = macHeld{keycode: kc}
		return append(ops, macKeyOp{Kind: macOpKey, Keycode: kc, Down: true,
			Flags: macFlags(in.Meta, in.Alt, in.Ctrl, in.Shift)})
	}

	r, printable := macPrintable(in.Key)
	cmd, opt, ctl, shift := in.Meta, in.Alt, in.Ctrl, in.Shift

	// ── No mapping: by position, modifiers as they are ─────────────────────
	if mode == 0 {
		kc := macCodeKeycode(in.Code, in.Key)
		if kc < 0 {
			if printable { // typed text (no code): the character itself
				m.held[id] = macHeld{swallow: true}
				return append(ops, macKeyOp{Kind: macOpChar, Keycode: 0, Char: string(r)})
			}
			return ops
		}
		f := macFlags(cmd, opt, ctl, shift) | macKeyExtraFlags(kc)
		if !cmd && !ctl {
			f |= m.capsFlag()
		}
		m.held[id] = macHeld{keycode: kc, flags: f}
		return append(ops, macKeyOp{Kind: macOpKey, Keycode: kc, Down: true, Flags: f})
	}

	// ── Mapping ──────────────────────────────────────────────────────────────
	if in.Key == "Dead" { // the operator's dead key: the composed character follows
		m.held[id] = macHeld{swallow: true}
		return ops
	}
	// AltGr (Windows reports Ctrl+Alt): a symbol / accented letter, typed as
	// is — a Ctrl+Alt+letter/digit shortcut keeps its modifiers.
	if printable && in.Ctrl && in.Alt && !in.Meta && !macIsASCIIAlnum(r) {
		m.held[id] = macHeld{swallow: true}
		return append(ops, macKeyOp{Kind: macOpChar, Keycode: macPosKeycode(in.Code), Char: string(r)})
	}

	kc := -1
	if mode == 2 {
		if ctl {
			if s := macShortcutChar(in.Key, in.Code); macCtrlShortcuts[s] {
				ctl, cmd = false, true
				if s == 'y' && !shift { // redo: Cmd+Shift+Z on a Mac
					shift = true
					if k := m.keycodeForChar('z'); k >= 0 {
						kc = k
					}
				}
			} else {
				switch in.Code {
				case "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Backspace", "Delete":
					ctl, opt = false, true // word / paragraph moves and deletes
				case "Home":
					ctl, cmd, kc = false, true, mkUp // start of document
				case "End":
					ctl, cmd, kc = false, true, mkDown // end of document
				}
			}
		} else if !cmd && !opt {
			switch in.Code {
			case "Home":
				cmd, kc = true, mkLeft // start of line
			case "End":
				cmd, kc = true, mkRight // end of line
			}
		}
	}

	if printable && kc < 0 {
		if !cmd && !ctl && !opt {
			// Text: the operator's character, whatever the Mac's layout.
			m.held[id] = macHeld{swallow: true}
			return append(ops, macKeyOp{Kind: macOpChar, Keycode: macPosKeycode(in.Code), Char: string(r)})
		}
		// A shortcut: the key that types this character on the Mac.
		if s := macShortcutChar(in.Key, in.Code); s != 0 {
			kc = m.keycodeForChar(s)
		}
		if kc < 0 {
			kc = macCodeKeycode(in.Code, in.Key)
		}
	}
	if kc < 0 {
		kc = macCodeKeycode(in.Code, in.Key)
	}
	if kc < 0 {
		return ops
	}
	f := macFlags(cmd, opt, ctl, shift) | macKeyExtraFlags(kc)
	m.held[id] = macHeld{keycode: kc, flags: f}
	return append(ops, macKeyOp{Kind: macOpKey, Keycode: kc, Down: true, Flags: f})
}

// macPosKeycode: the key's position for a typed character (informative: the
// Unicode string decides what is typed), 0 when unknown.
func macPosKeycode(code string) int {
	if kc, ok := macCodeKeycodes[code]; ok && !macIsModifierCode(code) {
		return kc
	}
	return 0
}

// ReleaseAll: every key and modifier still down on the Mac goes up (end of
// stream, view only).
func (m *macKeyMapper) ReleaseAll() []macKeyOp {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ops []macKeyOp
	for id, h := range m.held {
		if !h.swallow && macModGroup(h.keycode) < 0 {
			ops = append(ops, macKeyOp{Kind: macOpKey, Keycode: h.keycode, Down: false})
		}
		delete(m.held, id)
	}
	for kc := range m.mods {
		ops = append(ops, macKeyOp{Kind: macOpKey, Keycode: kc, Down: false})
		delete(m.mods, kc)
	}
	m.ctrl, m.shift, m.alt, m.meta = false, false, false, false
	return ops
}

// inputKeyEventHook: set by the darwin agent; when it returns true the key
// message was handled (dispatchInputJSON skips the Windows-VK path).
var inputKeyEventHook func(in macKeyIn) bool
