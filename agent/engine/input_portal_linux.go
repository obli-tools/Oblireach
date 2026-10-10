//go:build linux

package engine

// Input through the RemoteDesktop portal. The viewer's frame pixels are
// mapped to the shared screen's logical coordinates (the portal's absolute
// pointer motion is per stream, in compositor units: mixed scales need no
// special case); keys go as evdev key codes from the physical key the
// viewer reports (keymap_evdev_linux.go), so the remote layout applies as
// for a local keyboard.
//
// The portal cannot read the keyboard state: what this backend pressed is
// tracked here, which is what the modifier reconciliation (stuck Ctrl) and
// the release at the end of a stream use.

import (
	"math"
	"sync"
)

// portalInjector is what the input backend sends (portalSession implements
// it; the tests record the calls).
type portalInjector interface {
	motionAbs(node uint32, x, y float64)
	button(code int32, pressed bool)
	axisDiscrete(axis uint32, steps int32)
	keycode(code int32, pressed bool)
	keysym(sym int32, pressed bool)
}

// portalTarget gives the current stream geometry (portalCapture.target).
type portalTarget func() (node uint32, fw, fh, lw, lh int, ok bool)

type portalInput struct {
	inj    portalInjector
	target portalTarget

	mu      sync.Mutex
	keys    map[int32]bool  // key codes held down by us
	buttons map[int32]bool  // buttons held down by us
	typed   map[string]bool // keys sent as a keysym tap: their release is swallowed
}

func newPortalInput(ps *portalSession, pc *portalCapture) linuxInput {
	return newPortalInputWith(ps, pc.target)
}

func newPortalInputWith(inj portalInjector, target portalTarget) *portalInput {
	return &portalInput{inj: inj, target: target,
		keys: map[int32]bool{}, buttons: map[int32]bool{}, typed: map[string]bool{}}
}

func (in *portalInput) setOffset(x, y int) {} // coordinates are per stream

// toStream maps a frame pixel to the stream's logical coordinates (pixel
// centre, clamped inside the screen).
func (in *portalInput) toStream(x, y int) (node uint32, sx, sy float64, ok bool) {
	node, fw, fh, lw, lh, ok := in.target()
	if !ok {
		return 0, 0, 0, false
	}
	cx := math.Min(math.Max(float64(x), 0), float64(fw-1))
	cy := math.Min(math.Max(float64(y), 0), float64(fh-1))
	sx = (cx + 0.5) * float64(lw) / float64(fw)
	sy = (cy + 0.5) * float64(lh) / float64(fh)
	return node, sx, sy, true
}

func (in *portalInput) mouseMove(x, y int) {
	if node, sx, sy, ok := in.toStream(x, y); ok {
		in.inj.motionAbs(node, sx, sy)
	}
}

func (in *portalInput) mouseButton(button int, down bool, x, y int) {
	in.mouseMove(x, y)
	code := evdevButton(button)
	in.mu.Lock()
	if in.buttons[code] == down {
		in.mu.Unlock()
		return // never a release of a button we did not press, nor a double press
	}
	if down {
		in.buttons[code] = true
	} else {
		delete(in.buttons, code)
	}
	in.mu.Unlock()
	in.inj.button(code, down)
}

// mouseScroll: delta in wheel notches, positive = up (the viewers' and the
// Windows agent's convention); the portal's discrete steps are positive
// downwards.
func (in *portalInput) mouseScroll(delta int) {
	if delta != 0 {
		in.inj.axisDiscrete(0, int32(-delta))
	}
}

// key: a Windows VK from the generic path (the engine's own injections).
func (in *portalInput) key(vk int, down bool) {
	if code := evdevFromVK(vk); code != 0 {
		in.sendKey(code, down)
	}
}

// vkFromKey: unused (keyEvent takes every key message).
func (in *portalInput) vkFromKey(key string) (int, int) { return 0, 0 }

func (in *portalInput) sendKey(code int32, down bool) {
	in.mu.Lock()
	if in.keys[code] == down {
		// A release of a key we did not press, or the viewer's auto-repeat
		// of a key already down: the compositor repeats a held key itself
		// (a second press without release is an error for libei / mutter).
		in.mu.Unlock()
		if g := evdevModGroup(code); g >= 0 && down {
			trackInjectedModifier(evdevModVK[g], true)
		}
		return
	}
	if down {
		in.keys[code] = true
	} else {
		delete(in.keys, code)
	}
	in.mu.Unlock()
	in.inj.keycode(code, down)
	if g := evdevModGroup(code); g >= 0 {
		trackInjectedModifier(evdevModVK[g], down)
	}
}

// keyEvent maps a whole viewer key message (always handled: the VK path is
// for X11 / Windows).
func (in *portalInput) keyEvent(m macKeyIn) bool {
	if code, ok := evdevByCode[m.Code]; ok && code != 0 {
		in.sendKey(code, m.Down)
		return true
	}
	// No physical key (IME, virtual keyboard, a code we do not know): the
	// character itself, typed on the press.
	r := []rune(m.Key)
	if len(r) != 1 {
		return true
	}
	id := m.Code + "\x00" + m.Key
	if !m.Down {
		in.mu.Lock()
		delete(in.typed, id)
		in.mu.Unlock()
		return true
	}
	sym := keysymForRune(r[0])
	if sym == 0 {
		return true
	}
	in.mu.Lock()
	in.typed[id] = true
	in.mu.Unlock()
	in.inj.keysym(sym, true)
	in.inj.keysym(sym, false)
	return true
}

// releaseStale releases the modifiers we hold that the viewer reports as
// up, except those pressed a moment ago (protect, see modProtectMask).
func (in *portalInput) releaseStale(ctrl, shift, alt, meta bool, protect int) {
	held := [4]bool{ctrl, shift, alt, meta}
	var up []int32
	in.mu.Lock()
	for g, codes := range evdevModGroups {
		if held[g] || protect&(1<<uint(g)) != 0 {
			continue
		}
		for _, c := range codes {
			if in.keys[c] {
				up = append(up, c)
			}
		}
	}
	in.mu.Unlock()
	for _, c := range up {
		in.sendKey(c, false)
	}
}

// releaseAll releases every key and button we hold (end of stream).
func (in *portalInput) releaseAll() {
	in.mu.Lock()
	keys := make([]int32, 0, len(in.keys))
	for c := range in.keys {
		keys = append(keys, c)
	}
	buttons := make([]int32, 0, len(in.buttons))
	for b := range in.buttons {
		buttons = append(buttons, b)
	}
	in.buttons = map[int32]bool{}
	in.typed = map[string]bool{}
	in.mu.Unlock()
	for _, c := range keys {
		in.sendKey(c, false)
	}
	for _, b := range buttons {
		in.inj.button(b, false)
	}
}
