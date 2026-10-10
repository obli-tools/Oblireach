//go:build linux

package engine

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

type recInjector struct {
	mu  sync.Mutex
	log []string
}

func (r *recInjector) add(s string) { r.mu.Lock(); r.log = append(r.log, s); r.mu.Unlock() }

func (r *recInjector) motionAbs(node uint32, x, y float64) {
	r.add(fmt.Sprintf("move %d %.2f %.2f", node, x, y))
}
func (r *recInjector) button(code int32, pressed bool) {
	r.add(fmt.Sprintf("button %#x %v", code, pressed))
}
func (r *recInjector) axisDiscrete(axis uint32, steps int32) {
	r.add(fmt.Sprintf("axis %d %d", axis, steps))
}
func (r *recInjector) keycode(code int32, pressed bool) {
	r.add(fmt.Sprintf("key %d %v", code, pressed))
}
func (r *recInjector) keysym(sym int32, pressed bool) {
	r.add(fmt.Sprintf("sym %#x %v", sym, pressed))
}
func (r *recInjector) take() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := strings.Join(r.log, "; ")
	r.log = nil
	return s
}
func (r *recInjector) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

// A 2560x1440 frame of a screen the compositor lays out as 1280x720
// (scale 200 %).
func scaledTarget() (uint32, int, int, int, int, bool) { return 58, 2560, 1440, 1280, 720, true }

func newTestPortalInput() (*portalInput, *recInjector) {
	rec := &recInjector{}
	inputResetModifierTracking()
	return newPortalInputWith(rec, scaledTarget), rec
}

func TestPortalPointerMapping(t *testing.T) {
	in, rec := newTestPortalInput()
	in.mouseMove(0, 0)
	in.mouseMove(2559, 1439)
	in.mouseMove(1280, 720)
	in.mouseMove(-50, 99999) // clamped inside the screen
	if got, want := rec.take(), "move 58 0.25 0.25; move 58 1279.75 719.75; move 58 640.25 360.25; move 58 0.25 719.75"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	noTarget := newPortalInputWith(rec, func() (uint32, int, int, int, int, bool) { return 0, 0, 0, 0, 0, false })
	noTarget.mouseMove(10, 10)
	if rec.take() != "" {
		t.Error("no stream: nothing must be sent")
	}
}

func TestPortalButtons(t *testing.T) {
	in, rec := newTestPortalInput()
	in.mouseButton(0, true, 2, 2)
	in.mouseButton(0, true, 2, 2) // double press: dropped
	in.mouseButton(0, false, 2, 2)
	in.mouseButton(0, false, 2, 2) // release of a released button: dropped
	in.mouseButton(2, true, 2, 2)
	in.mouseButton(1, true, 2, 2)
	in.mouseButton(3, true, 2, 2)
	in.mouseButton(4, true, 2, 2)
	got := rec.take()
	for _, want := range []string{"button 0x110 true", "button 0x110 false", "button 0x111 true", "button 0x112 true", "button 0x113 true", "button 0x114 true"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Count(got, "button 0x110 true") != 1 || strings.Count(got, "button 0x110 false") != 1 {
		t.Errorf("double press / release not filtered: %s", got)
	}
	in.releaseAll()
	rel := rec.take()
	for _, want := range []string{"button 0x111 false", "button 0x112 false", "button 0x113 false", "button 0x114 false"} {
		if !strings.Contains(rel, want) {
			t.Errorf("releaseAll: missing %q in %s", want, rel)
		}
	}
}

func TestPortalScroll(t *testing.T) {
	in, rec := newTestPortalInput()
	in.mouseScroll(3)  // wheel up
	in.mouseScroll(-1) // down
	in.mouseScroll(0)
	if got := rec.take(); got != "axis 0 -3; axis 0 1" {
		t.Errorf("scroll = %s", got)
	}
}

func key(code, k string, down bool, mods ...string) macKeyIn {
	m := macKeyIn{Down: down, Code: code, Key: k}
	for _, x := range mods {
		switch x {
		case "ctrl":
			m.Ctrl = true
		case "shift":
			m.Shift = true
		case "alt":
			m.Alt = true
		case "meta":
			m.Meta = true
		}
	}
	return m
}

func TestPortalKeys(t *testing.T) {
	in, rec := newTestPortalInput()
	// AZERTY operator types "a" on the KeyQ position: positional, the
	// remote layout decides the character.
	in.keyEvent(key("KeyQ", "a", true))
	in.keyEvent(key("KeyQ", "a", true)) // auto-repeat: the compositor repeats itself
	in.keyEvent(key("KeyQ", "a", false))
	in.keyEvent(key("KeyQ", "a", false))
	if got := rec.take(); got != "key 16 true; key 16 false" {
		t.Errorf("letter = %s", got)
	}
	// Ctrl+C
	in.keyEvent(key("ControlLeft", "Control", true, "ctrl"))
	in.keyEvent(key("KeyC", "c", true, "ctrl"))
	in.keyEvent(key("KeyC", "c", false, "ctrl"))
	in.keyEvent(key("ControlLeft", "Control", false))
	if got := rec.take(); got != "key 29 true; key 46 true; key 46 false; key 29 false" {
		t.Errorf("ctrl+c = %s", got)
	}
	// No physical key: the character as a keysym tap, its release swallowed.
	in.keyEvent(key("", "€", true))
	in.keyEvent(key("", "€", false))
	in.keyEvent(key("Unidentified", "é", true))
	if got := rec.take(); got != "sym 0x10020ac true; sym 0x10020ac false; sym 0xe9 true; sym 0xe9 false" {
		t.Errorf("keysym = %s", got)
	}
	// Unknown, not a character: dropped, but handled (no VK path).
	if !in.keyEvent(key("", "Dead", true)) || rec.take() != "" {
		t.Error("an unknown key must be swallowed")
	}
	// The generic VK path (Shift around a character, Caps Lock alignment).
	in.key(0x10, true)
	in.key(0x41, true)
	in.key(0x41, false)
	in.key(0x10, false)
	in.key(0x7B, true)
	in.key(0x7B, false)
	if got := rec.take(); got != "key 42 true; key 30 true; key 30 false; key 42 false; key 88 true; key 88 false" {
		t.Errorf("vk = %s", got)
	}
}

func TestPortalStaleModifiers(t *testing.T) {
	in, rec := newTestPortalInput()
	in.keyEvent(key("ControlLeft", "Control", true, "ctrl"))
	in.keyEvent(key("AltRight", "AltGraph", true, "ctrl", "alt"))
	in.keyEvent(key("ShiftLeft", "Shift", true, "shift"))
	rec.take()
	// Inside the protection window: nothing released.
	in.releaseStale(false, false, false, false, modProtectMask())
	if got := rec.take(); got != "" {
		t.Errorf("protected modifiers released: %s", got)
	}
	// The viewer still holds Shift, lost Ctrl and AltGr (no protection).
	in.releaseStale(false, true, false, false, 0)
	got := rec.take()
	if !strings.Contains(got, "key 29 false") || !strings.Contains(got, "key 100 false") || strings.Contains(got, "key 42") {
		t.Errorf("stale release = %s", got)
	}
	in.releaseAll()
	if got := rec.take(); got != "key 42 false" {
		t.Errorf("releaseAll = %s", got)
	}
	in.releaseAll()
	if got := rec.take(); got != "" {
		t.Errorf("second releaseAll = %s", got)
	}
}

func TestEvdevTableCoversViewerCodes(t *testing.T) {
	// Every code the generic path knows (codeToVK) has an evdev key.
	for _, code := range []string{
		"KeyA", "KeyZ", "Digit0", "Digit9", "Space", "Enter", "Backspace", "Tab", "Escape", "Delete", "Insert",
		"Home", "End", "PageUp", "PageDown", "ArrowLeft", "ArrowUp", "ArrowRight", "ArrowDown",
		"ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight", "MetaLeft", "MetaRight",
		"F1", "F12", "Minus", "Equal", "BracketLeft", "BracketRight", "Backslash", "Semicolon", "Quote",
		"Comma", "Period", "Slash", "Backquote", "CapsLock", "NumLock", "ScrollLock", "PrintScreen",
		"ContextMenu", "Pause", "IntlBackslash", "NumpadEnter", "Numpad0", "NumpadDecimal",
	} {
		if evdevByCode[code] == 0 {
			t.Errorf("no evdev code for %s", code)
		}
		if vk := codeToVK(code); vk != 0 && code != "MetaRight" && code != "ShiftRight" && code != "ControlRight" && code != "AltRight" {
			if got, want := evdevFromVK(vk), evdevByCode[code]; got != want {
				t.Errorf("evdevFromVK(%#x) = %d, %s = %d", vk, got, code, want)
			}
		}
	}
	// Spot values from linux/input-event-codes.h.
	for code, want := range map[string]int32{
		"Escape": 1, "KeyA": 30, "KeyQ": 16, "KeyM": 50, "Digit0": 11, "Enter": 28, "Space": 57,
		"F10": 68, "F11": 87, "F13": 183, "F24": 194, "Delete": 111, "ArrowUp": 103, "MetaLeft": 125,
		"ContextMenu": 127, "IntlBackslash": 86, "NumpadEnter": 96, "AltRight": 100, "PrintScreen": 99,
	} {
		if got := evdevByCode[code]; got != want {
			t.Errorf("%s = %d, want %d", code, got, want)
		}
	}
	seen := map[int32]string{}
	for code, ev := range evdevByCode {
		if prev, dup := seen[ev]; dup {
			ok := map[string]bool{"MetaLeft": true, "OSLeft": true, "MetaRight": true, "OSRight": true,
				"AudioVolumeMute": true, "VolumeMute": true, "AudioVolumeDown": true, "VolumeDown": true,
				"AudioVolumeUp": true, "VolumeUp": true}
			if !ok[code] || !ok[prev] {
				t.Errorf("%s and %s share evdev %d", code, prev, ev)
			}
		}
		seen[ev] = code
	}
}

func TestKeysymForRune(t *testing.T) {
	for r, want := range map[rune]int32{'a': 0x61, 'A': 0x41, ' ': 0x20, 'é': 0xe9, '€': 0x10020ac, '\n': 0, 0x7f: 0, 0x85: 0, 'ß': 0xdf, '中': 0x1004e2d} {
		if got := keysymForRune(r); got != want {
			t.Errorf("keysymForRune(%q) = %#x, want %#x", r, got, want)
		}
	}
}

func TestEvdevButtonOrder(t *testing.T) {
	// MouseEvent.button order (0 left, 1 middle, 2 right), as the viewers and
	// the Windows agent use it.
	if evdevButton(0) != evdevBtnLeft || evdevButton(1) != evdevBtnMiddle || evdevButton(2) != evdevBtnRight || evdevButton(9) != evdevBtnLeft {
		t.Error("button mapping")
	}
}

// The whole viewer path: dispatchInputJSON → the Linux input backend.
func TestDispatchInputJSONToPortal(t *testing.T) {
	in, rec := newTestPortalInput()
	setLinuxInput(in)
	defer setLinuxInput(nil)
	clearCurrentView()
	for _, msg := range []string{
		`{"type":"mouse","action":"move","x":1280,"y":720}`,
		`{"type":"mouse","action":"down","button":0,"x":1280,"y":720}`,
		`{"type":"mouse","action":"up","button":0,"x":1280,"y":720}`,
		`{"type":"mouse","action":"scroll","delta":-2}`,
		`{"type":"key","action":"down","code":"KeyW","key":"z"}`,
		`{"type":"key","action":"up","code":"KeyW","key":"z"}`,
		`{"type":"key","action":"down","code":"Delete","key":"Delete","ctrl":true,"alt":true}`,
	} {
		dispatchInputJSON([]byte(msg), 2560, 1440)
	}
	got := rec.take()
	want := "move 58 640.25 360.25; move 58 640.25 360.25; button 0x110 true; move 58 640.25 360.25; button 0x110 false; axis 0 2; key 17 true; key 17 false"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
