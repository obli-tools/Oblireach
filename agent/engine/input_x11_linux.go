//go:build linux && cgo

package engine

/*
#cgo LDFLAGS: -lX11 -lXtst

#include <X11/Xlib.h>
#include <X11/extensions/XTest.h>
#include <X11/keysym.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>

static Display *g_inp_dpy = NULL;

static void lx_input_init(void) {
    if (!g_inp_dpy) g_inp_dpy = XOpenDisplay(NULL);
}

static void lx_mouse_move(int x, int y) {
    lx_input_init();
    if (!g_inp_dpy) return;
    XTestFakeMotionEvent(g_inp_dpy, -1, x, y, 0);
    XFlush(g_inp_dpy);
}

static void lx_mouse_button(int button, int down) {
    lx_input_init();
    if (!g_inp_dpy) return;
    // Viewer buttons follow MouseEvent.button (0 left, 1 middle, 2 right);
    // X11 buttons: 1=left, 2=middle, 3=right.
    unsigned int xbtn = 1;
    if (button == 1) xbtn = 2;      // middle
    else if (button == 2) xbtn = 3; // right
    XTestFakeButtonEvent(g_inp_dpy, xbtn, down ? True : False, 0);
    XFlush(g_inp_dpy);
}

static void lx_mouse_scroll(int delta) {
    lx_input_init();
    if (!g_inp_dpy) return;
    // Scroll: button 4=up, 5=down
    unsigned int btn = delta > 0 ? 4 : 5;
    int i, n = abs(delta);
    for (i = 0; i < n; i++) {
        XTestFakeButtonEvent(g_inp_dpy, btn, True, 0);
        XTestFakeButtonEvent(g_inp_dpy, btn, False, 0);
    }
    XFlush(g_inp_dpy);
}

static void lx_key(unsigned int keysym, int down) {
    lx_input_init();
    if (!g_inp_dpy) return;
    KeyCode kc = XKeysymToKeycode(g_inp_dpy, keysym);
    if (kc == 0) return;
    XTestFakeKeyEvent(g_inp_dpy, kc, down ? True : False, 0);
    XFlush(g_inp_dpy);
}

// lx_keysym_from_char: convert a Unicode character to an X11 keysym.
static unsigned long lx_keysym_from_char(unsigned int ch) {
    if (ch >= 0x20 && ch <= 0x7E) return (unsigned long)ch; // ASCII
    if (ch >= 0x100) return 0x01000000 | ch; // Unicode keysym
    return 0;
}

static void lx_input_close(void) {
    if (g_inp_dpy) { XCloseDisplay(g_inp_dpy); g_inp_dpy = NULL; }
}
*/
import "C"
import "C"

// x11Input is the XTest backend of the Linux input (input_linux.go). Keys
// arrive as Windows VK codes (stream.go maps the viewer's key first), mouse
// positions as frame pixels of the captured monitor.
type x11Input struct {
	offX, offY int
}

func newX11Input() linuxInput { return &x11Input{} }

func (in *x11Input) setOffset(x, y int) { in.offX, in.offY = x, y }

func (in *x11Input) mouseMove(x, y int) {
	C.lx_mouse_move(C.int(in.offX+x), C.int(in.offY+y))
}

// mouseButton: button as MouseEvent.button (0 left, 1 middle, 2 right), the
// order the viewers send and the Windows agent uses.
func (in *x11Input) mouseButton(button int, down bool, x, y int) {
	C.lx_mouse_move(C.int(in.offX+x), C.int(in.offY+y))
	d := C.int(0)
	if down {
		d = 1
	}
	C.lx_mouse_button(C.int(button), d)
}

func (in *x11Input) mouseScroll(delta int) {
	C.lx_mouse_scroll(C.int(delta))
}

// x11KeysymFlag marks a "VK" that is really a keysym (vkFromKey: the
// character the operator typed, layout-aware); without it the keysym went
// through vkToKeysym and printable characters were dropped.
const x11KeysymFlag = 0x40000000

func (in *x11Input) key(vk int, down bool) {
	var ks uint
	if vk&x11KeysymFlag != 0 {
		ks = uint(vk &^ x11KeysymFlag)
	} else {
		ks = vkToKeysym(vk)
	}
	if ks == 0 {
		return
	}
	d := C.int(0)
	if down {
		d = 1
	}
	C.lx_key(C.uint(ks), d)
}

func (in *x11Input) vkFromKey(key string) (int, int) {
	runes := []rune(key)
	if len(runes) != 1 {
		return 0, 0
	}
	ks := uint(C.lx_keysym_from_char(C.uint(runes[0])))
	if ks == 0 {
		return 0, 0
	}
	return int(ks) | x11KeysymFlag, 0
}

// keyEvent: the X11 backend takes the VK path of dispatchInputJSON.
func (in *x11Input) keyEvent(macKeyIn) bool { return false }

// Modifier reconciliation (stuck Ctrl): not implemented for XTest.
func (in *x11Input) releaseStale(ctrl, shift, alt, meta bool, protect int) {}
func (in *x11Input) releaseAll()                                           {}

// vkToKeysym maps Windows VK codes to X11 keysyms for common keys.
func vkToKeysym(vk int) uint {
	m := map[int]uint{
		0x08: 0xFF08, // Backspace
		0x09: 0xFF09, // Tab
		0x0D: 0xFF0D, // Enter
		0x10: 0xFFE1, // Shift
		0x11: 0xFFE3, // Ctrl
		0x12: 0xFFE9, // Alt
		0x14: 0xFFE5, // CapsLock
		0x1B: 0xFF1B, // Escape
		0x20: 0x0020, // Space
		0x21: 0xFF55, // PageUp
		0x22: 0xFF56, // PageDown
		0x23: 0xFF57, // End
		0x24: 0xFF50, // Home
		0x25: 0xFF51, // Left
		0x26: 0xFF52, // Up
		0x27: 0xFF53, // Right
		0x28: 0xFF54, // Down
		0x2D: 0xFF63, // Insert
		0x2E: 0xFFFF, // Delete
		0x5B: 0xFFEB, // Win/Super
	}
	if ks, ok := m[vk]; ok {
		return ks
	}
	// A-Z
	if vk >= 0x41 && vk <= 0x5A {
		return uint(vk + 0x20) // lowercase ascii
	}
	// 0-9
	if vk >= 0x30 && vk <= 0x39 {
		return uint(vk)
	}
	// F1-F12
	if vk >= 0x70 && vk <= 0x7B {
		return uint(0xFFBE + (vk - 0x70))
	}
	return 0
}

// Modifier reconciliation (stuck Ctrl): implemented on Windows only for now.
