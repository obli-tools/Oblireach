//go:build darwin

package engine

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation

#include <CoreGraphics/CoreGraphics.h>

// Every event carries explicit flags (keymap_mac.go computes them): what the
// Mac thinks is held never decides a shortcut or a case.
static void mac_mouse_move(int x, int y, unsigned long long flags) {
    CGEventRef ev = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved,
        CGPointMake((CGFloat)x, (CGFloat)y), kCGMouseButtonLeft);
    if (ev) { CGEventSetFlags(ev, (CGEventFlags)flags); CGEventPost(kCGHIDEventTap, ev); CFRelease(ev); }
}

static void mac_mouse_button(int button, int down, int x, int y, unsigned long long flags) {
    CGEventType type;
    CGMouseButton btn = kCGMouseButtonLeft;
    if (button == 0) {
        type = down ? kCGEventLeftMouseDown : kCGEventLeftMouseUp;
        btn = kCGMouseButtonLeft;
    } else if (button == 1) {
        type = down ? kCGEventRightMouseDown : kCGEventRightMouseUp;
        btn = kCGMouseButtonRight;
    } else {
        type = down ? kCGEventOtherMouseDown : kCGEventOtherMouseUp;
        btn = kCGMouseButtonCenter;
    }
    CGEventRef ev = CGEventCreateMouseEvent(NULL, type,
        CGPointMake((CGFloat)x, (CGFloat)y), btn);
    if (ev) { CGEventSetFlags(ev, (CGEventFlags)flags); CGEventPost(kCGHIDEventTap, ev); CFRelease(ev); }
}

static void mac_mouse_scroll(int delta, unsigned long long flags) {
    CGEventRef ev = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitLine, 1, delta);
    if (ev) { CGEventSetFlags(ev, (CGEventFlags)flags); CGEventPost(kCGHIDEventTap, ev); CFRelease(ev); }
}

static void mac_key(int keycode, int down) {
    CGEventRef ev = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keycode, down ? true : false);
    if (ev) { CGEventPost(kCGHIDEventTap, ev); CFRelease(ev); }
}

// A key with exactly these flags.
static void mac_post_key(int keycode, int down, unsigned long long flags) {
    CGEventRef ev = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keycode, down ? true : false);
    if (!ev) return;
    CGEventSetFlags(ev, (CGEventFlags)flags);
    CGEventPost(kCGHIDEventTap, ev);
    CFRelease(ev);
}

// Types a character (UTF-16, 1 or 2 units) whatever the Mac's layout: the
// Unicode string replaces what the key code would type. Down then up.
static void mac_post_chars(int keycode, const UniChar *chars, int n, unsigned long long flags) {
    for (int down = 1; down >= 0; down--) {
        CGEventRef ev = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keycode, down ? true : false);
        if (!ev) return;
        CGEventSetFlags(ev, (CGEventFlags)flags);
        CGEventKeyboardSetUnicodeString(ev, (UniCharCount)n, chars);
        CGEventPost(kCGHIDEventTap, ev);
        CFRelease(ev);
    }
}

// What a key code types (no modifier) on the Mac's current layout: 0 when
// none / not a single UTF-16 unit. Read from a created (never posted) event.
static unsigned int mac_keycode_char(int keycode) {
    CGEventRef ev = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keycode, true);
    if (!ev) return 0;
    CGEventSetFlags(ev, (CGEventFlags)0);
    UniChar buf[4];
    UniCharCount n = 0;
    CGEventKeyboardGetUnicodeString(ev, 4, &n, buf);
    CFRelease(ev);
    return n == 1 ? (unsigned int)buf[0] : 0;
}

static int mac_caps_on(void) {
    return (CGEventSourceFlagsState(kCGEventSourceStateHIDSystemState) & kCGEventFlagMaskAlphaShift) ? 1 : 0;
}

static void mac_type_char(unsigned int utf32) {
    CGEventRef ev = CGEventCreateKeyboardEvent(NULL, 0, true);
    if (!ev) return;
    UniChar uc = (UniChar)utf32;
    CGEventKeyboardSetUnicodeString(ev, 1, &uc);
    CGEventPost(kCGHIDEventTap, ev);
    CFRelease(ev);
    // Key up
    ev = CGEventCreateKeyboardEvent(NULL, 0, false);
    if (ev) { CGEventPost(kCGHIDEventTap, ev); CFRelease(ev); }
}
*/
import "C"

import (
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unsafe"
)

var g_monOffX, g_monOffY int

// macKeys maps the viewers' key messages (keymap_mac.go).
var macKeys = newMacKeyMapper()

func init() {
	macKeys.layoutKeycode = macLayoutKeycode
	macKeys.remoteCaps = func() bool { return C.mac_caps_on() != 0 }
	inputKeyEventHook = func(in macKeyIn) bool {
		macPostOps(macKeys.Plan(in))
		return true
	}
}

func macPostOps(ops []macKeyOp) {
	for _, op := range ops {
		switch op.Kind {
		case macOpKey:
			d := C.int(0)
			if op.Down {
				d = 1
			}
			C.mac_post_key(C.int(op.Keycode), d, C.ulonglong(op.Flags))
		case macOpChar:
			u := utf16.Encode([]rune(op.Char))
			if len(u) == 0 {
				continue
			}
			C.mac_post_chars(C.int(op.Keycode), (*C.UniChar)(unsafe.Pointer(&u[0])), C.int(len(u)), C.ulonglong(op.Flags))
		}
	}
}

// The Mac's layout, read back key by key (character → key code), refreshed
// every few seconds (the user may switch layouts). A layout that yields no
// Latin letters (Cyrillic, Greek…) is not used: shortcuts then go by US
// position, which is what such Macs use for Cmd+letter.
var macLayout struct {
	mu   sync.Mutex
	at   time.Time
	ok   bool
	byCh map[rune]int
}

func macLayoutKeycode(r rune) (int, bool) {
	macLayout.mu.Lock()
	defer macLayout.mu.Unlock()
	if macLayout.byCh == nil || time.Since(macLayout.at) > 3*time.Second {
		m := map[rune]int{}
		letters := 0
		for kc := 0; kc <= 50; kc++ {
			switch kc {
			case mkReturn, mkTab, mkSpace:
				continue
			}
			ch := rune(C.mac_keycode_char(C.int(kc)))
			if ch == 0 || !unicode.IsPrint(ch) {
				continue
			}
			ch = unicode.ToLower(ch)
			if _, dup := m[ch]; !dup {
				m[ch] = kc
				if ch >= 'a' && ch <= 'z' {
					letters++
				}
			}
		}
		macLayout.byCh, macLayout.at, macLayout.ok = m, time.Now(), letters >= 20
	}
	if !macLayout.ok {
		return 0, false
	}
	kc, ok := macLayout.byCh[r]
	return kc, ok
}

func setInputMonitorOffset(x, y int) {
	g_monOffX = x
	g_monOffY = y
}

func inputMouseMove(x, y int) {
	C.mac_mouse_move(C.int(g_monOffX+x), C.int(g_monOffY+y), C.ulonglong(macKeys.MouseFlags()))
}

func inputMouseButton(button int, down bool, x, y int) {
	f := C.ulonglong(macKeys.MouseFlags())
	C.mac_mouse_move(C.int(g_monOffX+x), C.int(g_monOffY+y), f)
	d := C.int(0)
	if down {
		d = 1
	}
	C.mac_mouse_button(C.int(button), d, C.int(g_monOffX+x), C.int(g_monOffY+y), f)
}

func inputMouseScroll(delta int) {
	C.mac_mouse_scroll(C.int(delta), C.ulonglong(macKeys.MouseFlags()))
}

func inputKey(vk int, down bool) {
	// Map Windows VK codes to macOS keycodes
	kc := vkToMacKeycode(vk)
	if kc < 0 {
		return
	}
	d := C.int(0)
	if down {
		d = 1
	}
	C.mac_key(C.int(kc), d)
}

func InputSAS() {} // not applicable on macOS

func inputVKFromKey(key string) (int, int) {
	runes := []rune(key)
	if len(runes) != 1 {
		return 0, 0
	}
	// On macOS, we type Unicode characters directly
	return int(runes[0]), 0
}

func clipboardGet() string  { return "" } // TODO: NSPasteboard
func clipboardSet(text string) {}         // TODO: NSPasteboard

// Local-input block: no mechanism on macOS that freezes the local user without
// also freezing our injected input, so the block is a no-op that always reports
// "not blocked" (F3 — never a false "blocked"). input_block_local is therefore
// not advertised here.
func platformInputBlockSupported() bool     { return false }
func platformInputBlockApply(want bool) bool { return false }

func inputSwitchActiveDesktop() {} // Windows-only concept
func inputAttachToDefaultDesktop() string { return "" }
func logInputEventImpl(msg string)          {}

// vkToMacKeycode maps Windows VK codes to macOS key codes.
func vkToMacKeycode(vk int) int {
	m := map[int]int{
		0x08: 51,  // Backspace
		0x09: 48,  // Tab
		0x0D: 36,  // Return
		0x1B: 53,  // Escape
		0x20: 49,  // Space
		0x25: 123, // Left
		0x26: 126, // Up
		0x27: 124, // Right
		0x28: 125, // Down
		0x2E: 117, // Delete (forward)
		0x24: 115, // Home
		0x23: 119, // End
		0x21: 116, // PageUp
		0x22: 121, // PageDown
		0x70: 122, // F1
		0x71: 120, // F2
		0x72: 99,  // F3
		0x73: 118, // F4
		0x74: 96,  // F5
		0x75: 97,  // F6
		0x76: 98,  // F7
		0x77: 100, // F8
		0x78: 101, // F9
		0x79: 109, // F10
		0x7A: 103, // F11
		0x7B: 111, // F12
		0x10: 56,  // Shift
		0x11: 59,  // Control
		0x12: 58,  // Option/Alt
		0x5B: 55,  // Command
	}
	if kc, ok := m[vk]; ok {
		return kc
	}
	// A-Z → macOS keycodes 0-11, 13-46 (QWERTY layout)
	qwerty := map[int]int{
		0x41: 0, 0x42: 11, 0x43: 8, 0x44: 2, 0x45: 14, 0x46: 3,
		0x47: 5, 0x48: 4, 0x49: 34, 0x4A: 38, 0x4B: 40, 0x4C: 37,
		0x4D: 46, 0x4E: 45, 0x4F: 31, 0x50: 35, 0x51: 12, 0x52: 15,
		0x53: 1, 0x54: 17, 0x55: 32, 0x56: 9, 0x57: 13, 0x58: 7,
		0x59: 16, 0x5A: 6,
	}
	if kc, ok := qwerty[vk]; ok {
		return kc
	}
	// 0-9
	digits := map[int]int{
		0x30: 29, 0x31: 18, 0x32: 19, 0x33: 20, 0x34: 21,
		0x35: 23, 0x36: 22, 0x37: 26, 0x38: 28, 0x39: 25,
	}
	if kc, ok := digits[vk]; ok {
		return kc
	}
	return -1
}

// Modifier reconciliation: keymap_mac.go releases a stale modifier itself
// (every key message carries the operator's flags). The end of a stream
// lets go of everything the mapper still holds down on the Mac.
func inputReleaseStaleModifiers(ctrl, shift, alt, meta bool, protect int) {}
func inputReleaseAllModifiers()                                           { macPostOps(macKeys.ReleaseAll()) }

// EnablePerMonitorDPIAwareness is a Windows concept — no-op elsewhere.
func EnablePerMonitorDPIAwareness() {}

// inputMoveDesktop / inputButtonDesktop map PHYSICAL desktop coords. The view
// path that calls them is Windows-only today (curView is set by the Windows
// capture); these keep the package compiling and stay best-effort elsewhere.
func inputMoveDesktop(vx, vy int)                        { inputMouseMove(vx, vy) }
func inputButtonDesktop(button int, down bool, vx, vy int) { inputMouseButton(button, down, vx, vy) }
