package engine

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Pure tests of the macOS key mapping (keymap_mac.go): no Mac needed.

func testMacMapper() (*macKeyMapper, *time.Time) {
	m := newMacKeyMapper()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	return m, &now
}

func kdown(key, code string, mode int) macKeyIn {
	return macKeyIn{Down: true, Key: key, Code: code, Mode: mode}
}

func kup(key, code string, mode int) macKeyIn {
	return macKeyIn{Down: false, Key: key, Code: code, Mode: mode}
}

func opKey(kc int, down bool, flags uint64) macKeyOp {
	return macKeyOp{Kind: macOpKey, Keycode: kc, Down: down, Flags: flags}
}

func opChar(kc int, s string) macKeyOp {
	return macKeyOp{Kind: macOpChar, Keycode: kc, Char: s}
}

func expectOps(t *testing.T, name string, got []macKeyOp, want ...macKeyOp) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %+v\n want %+v", name, got, want)
	}
}

func TestMacKeyFeatureOnlyOnDarwin(t *testing.T) {
	has := false
	for _, f := range directStreamFeatures() {
		if f == featKeyMapMac {
			has = true
		}
	}
	if has != macKeyMapSupported() {
		t.Fatalf("directStreamFeatures lists key_map_mac = %v, supported = %v", has, macKeyMapSupported())
	}
	if featKeyMapMac != "key_map_mac" {
		t.Fatalf("featKeyMapMac = %q: the viewers look for \"key_map_mac\"", featKeyMapMac)
	}
}

// Mode 0 (old viewer, operator on a Mac, setting Off): by position, the
// modifiers as they are. Lower-case letters reach the Mac (they were dropped
// before: the Windows VK path read 'a' as VK 0x61).
func TestMacKeyMode0Positional(t *testing.T) {
	m, _ := testMacMapper()
	expectOps(t, "a", m.Plan(kdown("a", "KeyA", 0)), opKey(0, true, 0))
	expectOps(t, "a up", m.Plan(kup("a", "KeyA", 0)), opKey(0, false, 0))
	// Ctrl stays Control, Win stays Cmd.
	in := kdown("c", "KeyC", 0)
	in.Ctrl = true
	expectOps(t, "ctrl+c", m.Plan(in), opKey(8, true, macFlagControl))
	m.Plan(kup("c", "KeyC", 0))
	in = kdown("Meta", "MetaRight", 0)
	in.Meta = true
	expectOps(t, "win right", m.Plan(in), opKey(mkCmdR, true, macFlagCommand))
	// Typed text without a code: the character.
	m2, _ := testMacMapper()
	expectOps(t, "typed é", m2.Plan(kdown("é", "", 0)), opChar(0, "é"))
	expectOps(t, "typed é up", m2.Plan(kup("é", "", 0)))
	// Caps Lock: never pressed on the Mac; the flag follows the operator.
	caps := true
	in = kdown("CapsLock", "CapsLock", 0)
	in.Caps = &caps
	expectOps(t, "capslock", m2.Plan(in))
	in = kdown("A", "KeyA", 0)
	in.Caps = &caps
	expectOps(t, "A caps", m2.Plan(in), opKey(0, true, macFlagAlphaShift))
}

func TestMacKeyMode0RemoteCapsWhenUnknown(t *testing.T) {
	m, _ := testMacMapper()
	m.remoteCaps = func() bool { return true }
	expectOps(t, "a", m.Plan(kdown("a", "KeyA", 0)), opKey(0, true, macFlagAlphaShift))
	// A shortcut never carries Caps Lock.
	in := kdown("c", "KeyC", 0)
	in.Meta = true
	m.Plan(kup("a", "KeyA", 0))
	expectOps(t, "cmd+c", m.Plan(in), opKey(8, true, macFlagCommand))
}

// Mode 1: characters typed as Unicode (the operator's layout wins), Win = Cmd,
// Alt = Option, the key-up of a typed character is swallowed.
func TestMacKeyMode1Text(t *testing.T) {
	m, _ := testMacMapper()
	// AZERTY operator: the key at the US "Q" position types "a".
	expectOps(t, "azerty a", m.Plan(kdown("a", "KeyQ", 1)), opChar(12, "a"))
	expectOps(t, "azerty a up", m.Plan(kup("a", "KeyQ", 1)))
	in := kdown("A", "KeyQ", 1)
	in.Shift = true
	expectOps(t, "shift A", m.Plan(in), opChar(12, "A"))
	expectOps(t, "space", m.Plan(kdown(" ", "Space", 1)), opChar(mkSpace, " "))
	expectOps(t, "dead key", m.Plan(kdown("Dead", "BracketLeft", 1)))
	expectOps(t, "composed", m.Plan(kdown("ê", "KeyE", 1)), opChar(14, "ê"))
	// Non printable keys: by key code.
	expectOps(t, "enter", m.Plan(kdown("Enter", "Enter", 1)), opKey(mkReturn, true, 0))
	expectOps(t, "enter up", m.Plan(kup("Enter", "Enter", 1)), opKey(mkReturn, false, 0))
	in = kdown("ArrowLeft", "ArrowLeft", 1)
	in.Shift = true
	expectOps(t, "shift+left", m.Plan(in), opKey(mkLeft, true, macFlagShift|macFlagNumPad|macFlagFn))
}

func TestMacKeyMode1WinIsCmd(t *testing.T) {
	m, _ := testMacMapper()
	in := kdown("Meta", "MetaLeft", 1)
	in.Meta = true
	expectOps(t, "win", m.Plan(in), opKey(mkCmdL, true, macFlagCommand))
	// Win+C on an AZERTY operator, US Mac: Cmd + the Mac's "c" key.
	in = kdown("c", "KeyC", 1)
	in.Meta = true
	expectOps(t, "win+c", m.Plan(in), opKey(8, true, macFlagCommand))
	in = kup("c", "KeyC", 1)
	in.Meta = true
	expectOps(t, "win+c up", m.Plan(in), opKey(8, false, macFlagCommand))
	// AZERTY Win+Z (code KeyW): undo, the Mac's "z" key.
	in = kdown("z", "KeyW", 1)
	in.Meta = true
	expectOps(t, "win+z azerty", m.Plan(in), opKey(6, true, macFlagCommand))
	// AZERTY digit row (key "&" on Digit1): Cmd+1.
	in = kdown("&", "Digit1", 1)
	in.Meta = true
	expectOps(t, "win+1 azerty", m.Plan(in), opKey(18, true, macFlagCommand))
	expectOps(t, "win up", m.Plan(kup("Meta", "MetaLeft", 1)), opKey(mkCmdL, false, 0))
}

// The Mac's own layout decides which key types a shortcut's letter.
func TestMacKeyShortcutUsesMacLayout(t *testing.T) {
	m, _ := testMacMapper()
	// French AZERTY Mac: "a" is at the US Q position (12), "q" at A (0).
	m.layoutKeycode = func(r rune) (int, bool) {
		switch r {
		case 'a':
			return 12, true
		case 'q':
			return 0, true
		case 'z':
			return 13, true
		}
		return 0, false
	}
	in := kdown("a", "KeyA", 1) // QWERTY operator, Win+A
	in.Meta = true
	expectOps(t, "cmd+a on azerty mac", m.Plan(in), opKey(12, true, macFlagCommand))
	in = kdown("z", "KeyZ", 2)
	in.Ctrl = true
	expectOps(t, "ctrl+z → cmd+z on azerty mac", m.Plan(in), opKey(13, true, macFlagCommand))
}

func TestMacKeyAltIsOption(t *testing.T) {
	m, _ := testMacMapper()
	in := kdown("Alt", "AltLeft", 1)
	in.Alt = true
	expectOps(t, "alt", m.Plan(in), opKey(mkOptionL, true, macFlagOption))
	in = kdown("e", "KeyE", 1)
	in.Alt = true
	expectOps(t, "alt+e", m.Plan(in), opKey(14, true, macFlagOption))
}

// AltGr (Windows: Ctrl+Alt): the character is typed, no modifier reaches the Mac.
func TestMacKeyAltGr(t *testing.T) {
	for _, mode := range []int{1, 2} {
		m, _ := testMacMapper()
		in := kdown("Control", "ControlLeft", mode)
		in.Ctrl = true
		ctrlOps := m.Plan(in)
		if mode == 1 {
			expectOps(t, "fake ctrl", ctrlOps, opKey(mkControlL, true, macFlagControl))
		} else {
			expectOps(t, "fake ctrl swallowed", ctrlOps)
		}
		in = kdown("AltGraph", "AltRight", mode)
		in.Ctrl, in.Alt = true, true
		expectOps(t, "altgr", m.Plan(in))
		in = kdown("@", "Digit0", mode)
		in.Ctrl, in.Alt = true, true
		expectOps(t, "altgr @", m.Plan(in), opChar(29, "@"))
		in = kdown("€", "KeyE", mode)
		in.Ctrl, in.Alt = true, true
		expectOps(t, "altgr €", m.Plan(in), opChar(14, "€"))
		in = kup("@", "Digit0", mode)
		in.Ctrl, in.Alt = true, true
		expectOps(t, "altgr @ up", m.Plan(in))
		in = kup("AltGraph", "AltRight", mode)
		in.Ctrl = true
		expectOps(t, "altgr up", m.Plan(in))
	}
	// Mode 0: AltRight is the right Option key.
	m, _ := testMacMapper()
	in := kdown("AltGraph", "AltRight", 0)
	in.Ctrl, in.Alt = true, true
	expectOps(t, "altgr mode0", m.Plan(in), opKey(mkOptionR, true, macFlagControl|macFlagOption))
}

// Mode 2: Ctrl → Cmd for the common shortcuts.
func TestMacKeyCtrlAsCmd(t *testing.T) {
	m, _ := testMacMapper()
	in := kdown("Control", "ControlLeft", 2)
	in.Ctrl = true
	expectOps(t, "ctrl swallowed", m.Plan(in))
	for _, c := range []struct {
		key, code string
		kc        int
	}{
		{"c", "KeyC", 8}, {"v", "KeyV", 9}, {"x", "KeyX", 7}, {"a", "KeyA", 0},
		{"z", "KeyZ", 6}, {"s", "KeyS", 1}, {"f", "KeyF", 3}, {"t", "KeyT", 17},
		{"w", "KeyW", 13}, {"n", "KeyN", 45}, {"q", "KeyQ", 12}, {"r", "KeyR", 15},
		{"p", "KeyP", 35},
	} {
		in = kdown(c.key, c.code, 2)
		in.Ctrl = true
		expectOps(t, "ctrl+"+c.key, m.Plan(in), opKey(c.kc, true, macFlagCommand))
		in = kup(c.key, c.code, 2)
		in.Ctrl = true
		expectOps(t, "ctrl+"+c.key+" up", m.Plan(in), opKey(c.kc, false, macFlagCommand))
	}
	// Ctrl+Y: redo = Cmd+Shift+Z.
	in = kdown("y", "KeyY", 2)
	in.Ctrl = true
	expectOps(t, "ctrl+y", m.Plan(in), opKey(6, true, macFlagCommand|macFlagShift))
	// AZERTY Ctrl+Z (code KeyW).
	in = kdown("z", "KeyW", 2)
	in.Ctrl = true
	expectOps(t, "ctrl+z azerty", m.Plan(in), opKey(6, true, macFlagCommand))
	// Russian layout: Ctrl+"с" on KeyC is a copy.
	in = kdown("с", "KeyC", 2)
	in.Ctrl = true
	expectOps(t, "ctrl+c cyrillic", m.Plan(in), opKey(8, true, macFlagCommand))
	// Ctrl + another letter stays Control (terminal Ctrl+D).
	in = kdown("d", "KeyD", 2)
	in.Ctrl = true
	expectOps(t, "ctrl+d", m.Plan(in), opKey(2, true, macFlagControl))
	// Ctrl+Shift+T keeps Shift.
	in = kdown("T", "KeyT", 2)
	in.Ctrl, in.Shift = true, true
	expectOps(t, "ctrl+shift+t", m.Plan(in), opKey(17, true, macFlagCommand|macFlagShift))
	// Ctrl up: nothing was pressed on the Mac.
	expectOps(t, "ctrl up", m.Plan(kup("Control", "ControlLeft", 2)))
}

func TestMacKeyCtrlNavigation(t *testing.T) {
	m, _ := testMacMapper()
	nav := uint64(macFlagNumPad | macFlagFn)
	cases := []struct {
		code  string
		ctrl  bool
		shift bool
		kc    int
		flags uint64
	}{
		{"ArrowLeft", true, false, mkLeft, macFlagOption | nav},
		{"ArrowRight", true, true, mkRight, macFlagOption | macFlagShift | nav},
		{"ArrowUp", true, false, mkUp, macFlagOption | nav},
		{"Backspace", true, false, mkBackspace, macFlagOption},
		{"Delete", true, false, mkForwardDelete, macFlagOption | macFlagFn},
		{"Home", true, false, mkUp, macFlagCommand | nav},
		{"End", true, true, mkDown, macFlagCommand | macFlagShift | nav},
		{"Home", false, false, mkLeft, macFlagCommand | nav},
		{"End", false, true, mkRight, macFlagCommand | macFlagShift | nav},
	}
	for _, c := range cases {
		in := kdown(c.code, c.code, 2)
		in.Ctrl, in.Shift = c.ctrl, c.shift
		expectOps(t, c.code, m.Plan(in), opKey(c.kc, true, c.flags))
		m.Plan(kup(c.code, c.code, 2))
	}
	// Mode 1: no translation (Ctrl+Left is Control+Left, Home is Home).
	m1, _ := testMacMapper()
	in := kdown("ArrowLeft", "ArrowLeft", 1)
	in.Ctrl = true
	expectOps(t, "mode1 ctrl+left", m1.Plan(in), opKey(mkLeft, true, macFlagControl|nav))
	expectOps(t, "mode1 home", m1.Plan(kdown("Home", "Home", 1)), opKey(mkHome, true, macFlagFn))
}

func TestMacKeyMouseFlags(t *testing.T) {
	m, _ := testMacMapper()
	in := kdown("Control", "ControlLeft", 2)
	in.Ctrl = true
	m.Plan(in)
	if f := m.MouseFlags(); f != macFlagCommand {
		t.Fatalf("mode 2 ctrl+click flags = %#x, want Cmd", f)
	}
	m1, _ := testMacMapper()
	in.Mode = 1
	m1.Plan(in)
	if f := m1.MouseFlags(); f != macFlagControl {
		t.Fatalf("mode 1 ctrl+click flags = %#x, want Control", f)
	}
	m1.Plan(kup("Control", "ControlLeft", 1))
	if f := m1.MouseFlags(); f != 0 {
		t.Fatalf("after release flags = %#x", f)
	}
}

// A modifier whose key-up was lost is released before the next key, unless
// it was pressed a moment ago (per-key flags of a chord).
func TestMacKeyStaleModifier(t *testing.T) {
	m, now := testMacMapper()
	in := kdown("Meta", "MetaLeft", 1)
	in.Meta = true
	m.Plan(in)
	expectOps(t, "fresh chord keeps Cmd", m.Plan(kdown("Enter", "Enter", 1)), opKey(mkReturn, true, 0))
	m.Plan(kup("Enter", "Enter", 1))
	*now = now.Add(2 * time.Second)
	expectOps(t, "stale Cmd released", m.Plan(kdown("Enter", "Enter", 1)),
		opKey(mkCmdL, false, 0), opKey(mkReturn, true, 0))
	expectOps(t, "its late up is ignored", m.Plan(kup("Meta", "MetaLeft", 1)))
}

func TestMacKeyReleaseAll(t *testing.T) {
	m, _ := testMacMapper()
	in := kdown("Shift", "ShiftLeft", 1)
	in.Shift = true
	m.Plan(in)
	in = kdown("ArrowDown", "ArrowDown", 1)
	in.Shift = true
	m.Plan(in)
	m.Plan(kdown("a", "KeyA", 1)) // typed: nothing held on the Mac
	got := m.ReleaseAll()
	want := []macKeyOp{opKey(mkDown, false, 0), opKey(mkShiftL, false, 0)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReleaseAll = %+v, want %+v", got, want)
	}
	if again := m.ReleaseAll(); len(again) != 0 {
		t.Fatalf("second ReleaseAll = %+v", again)
	}
}

// A press planned in one mode is released the same way after a mode change.
func TestMacKeyUpMatchesDown(t *testing.T) {
	m, _ := testMacMapper()
	m.Plan(kdown("a", "KeyA", 0))
	expectOps(t, "up after mode change", m.Plan(kup("a", "KeyA", 2)), opKey(0, false, 0))
	m.Plan(kdown("a", "KeyA", 2))
	expectOps(t, "typed up swallowed", m.Plan(kup("a", "KeyA", 0)))
	expectOps(t, "unknown up", m.Plan(kup("b", "KeyB", 1)))
}

func TestMacKeyShortcutChar(t *testing.T) {
	cases := []struct {
		key, code string
		want      rune
	}{
		{"z", "KeyW", 'z'}, {"Z", "KeyW", 'z'}, {"&", "Digit1", '1'}, {"с", "KeyC", 'c'},
		{"+", "Equal", '+'}, {"", "KeyK", 'k'}, {"Enter", "Enter", 0},
	}
	for _, c := range cases {
		if got := macShortcutChar(c.key, c.code); got != c.want {
			t.Errorf("macShortcutChar(%q,%q) = %q, want %q", c.key, c.code, got, c.want)
		}
	}
}

// The key message field: absent (old viewer) = 0, no mapping.
func TestMacMapFieldDecoding(t *testing.T) {
	var msg struct {
		MacMap int `json:"macMap"`
	}
	if err := json.Unmarshal([]byte(`{"type":"key","key":"a"}`), &msg); err != nil || msg.MacMap != 0 {
		t.Fatalf("absent macMap = %d, %v", msg.MacMap, err)
	}
	if err := json.Unmarshal([]byte(`{"type":"key","key":"a","macMap":2}`), &msg); err != nil || msg.MacMap != 2 {
		t.Fatalf("macMap 2 = %d, %v", msg.MacMap, err)
	}
	m, _ := testMacMapper()
	expectOps(t, "out of range = 0", m.Plan(macKeyIn{Down: true, Key: "a", Code: "KeyA", Mode: 7}), opKey(0, true, 0))
}

// dispatchInputJSON hands key messages to the hook (darwin) with macMap.
func TestDispatchKeyHook(t *testing.T) {
	prev := inputKeyEventHook
	defer func() { inputKeyEventHook = prev }()
	var got []macKeyIn
	inputKeyEventHook = func(in macKeyIn) bool { got = append(got, in); return true }
	dispatchInputJSON([]byte(`{"type":"key","action":"down","key":"c","code":"KeyC","ctrl":true,"macMap":2}`), 100, 100)
	dispatchInputJSON([]byte(`{"type":"key","action":"up","key":"c","code":"KeyC"}`), 100, 100)
	if len(got) != 2 || !got[0].Down || got[0].Mode != 2 || !got[0].Ctrl || got[0].Key != "c" || got[1].Down || got[1].Mode != 0 {
		t.Fatalf("hook got %+v", got)
	}
}
