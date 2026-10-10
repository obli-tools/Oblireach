//go:build linux && cgo

package engine

/*
#cgo LDFLAGS: -lX11 -lXrandr

#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/Xrandr.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

static Display *g_dpy      = NULL;
static Window   g_root     = 0;
static int      g_width    = 0;
static int      g_height   = 0;
static int      g_mon_x    = 0;
static int      g_mon_y    = 0;

#define LX_MAX_MONITORS 16

typedef struct {
    int index;
    char name[64];
    int x, y, w, h;
} LxMonitorInfo;

static int lx_enumerate_monitors(LxMonitorInfo *out, int maxCount) {
    Display *dpy = XOpenDisplay(NULL);
    if (!dpy) return 0;

    int count = 0;
    int nmon = 0;
    XRRMonitorInfo *mons = XRRGetMonitors(dpy, DefaultRootWindow(dpy), True, &nmon);
    if (mons) {
        int i;
        for (i = 0; i < nmon && count < maxCount; i++) {
            out[count].index = count;
            char *name = XGetAtomName(dpy, mons[i].name);
            if (name) {
                strncpy(out[count].name, name, 63);
                out[count].name[63] = 0;
                XFree(name);
            } else {
                snprintf(out[count].name, 64, "Monitor %d", i);
            }
            out[count].x = mons[i].x;
            out[count].y = mons[i].y;
            out[count].w = mons[i].width;
            out[count].h = mons[i].height;
            count++;
        }
        XRRFreeMonitors(mons);
    }
    XCloseDisplay(dpy);
    return count;
}

static int lx_capture_init(int monitor_idx) {
    g_dpy = XOpenDisplay(NULL);
    if (!g_dpy) return -1;

    g_root = DefaultRootWindow(g_dpy);

    // Find monitor by index
    int nmon = 0;
    XRRMonitorInfo *mons = XRRGetMonitors(g_dpy, g_root, True, &nmon);
    if (mons && monitor_idx >= 0 && monitor_idx < nmon) {
        g_mon_x  = mons[monitor_idx].x;
        g_mon_y  = mons[monitor_idx].y;
        g_width  = mons[monitor_idx].width;
        g_height = mons[monitor_idx].height;
        XRRFreeMonitors(mons);
    } else {
        if (mons) XRRFreeMonitors(mons);
        // Fallback: full screen
        g_width  = DisplayWidth(g_dpy, DefaultScreen(g_dpy));
        g_height = DisplayHeight(g_dpy, DefaultScreen(g_dpy));
        g_mon_x  = 0;
        g_mon_y  = 0;
    }

    return 0;
}

static void lx_capture_close(void) {
    if (g_dpy) {
        XCloseDisplay(g_dpy);
        g_dpy = NULL;
    }
    g_width = 0;
    g_height = 0;
}

static void lx_get_size(int *w, int *h) {
    *w = g_width;
    *h = g_height;
}

static void lx_get_offset(int *ox, int *oy) {
    *ox = g_mon_x;
    *oy = g_mon_y;
}

// lx_capture_frame: captures the screen region into BGRA buffer.
// Returns 0 on success, negative on failure.
static int lx_capture_frame(unsigned char *out_bgra) {
    if (!g_dpy || g_width <= 0 || g_height <= 0) return -1;

    XImage *img = XGetImage(g_dpy, g_root, g_mon_x, g_mon_y, g_width, g_height, AllPlanes, ZPixmap);
    if (!img) return -2;

    // XImage data is typically BGRA (32-bit depth) or BGR (24-bit).
    // For 32-bit, we can memcpy directly (it's already BGRA).
    if (img->bits_per_pixel == 32) {
        int y;
        for (y = 0; y < g_height; y++) {
            memcpy(out_bgra + y * g_width * 4,
                   img->data + y * img->bytes_per_line,
                   g_width * 4);
        }
    } else if (img->bits_per_pixel == 24) {
        // Convert BGR 24-bit to BGRA 32-bit
        int y, x;
        for (y = 0; y < g_height; y++) {
            unsigned char *src = (unsigned char*)img->data + y * img->bytes_per_line;
            unsigned char *dst = out_bgra + y * g_width * 4;
            for (x = 0; x < g_width; x++) {
                dst[x*4+0] = src[x*3+0]; // B
                dst[x*4+1] = src[x*3+1]; // G
                dst[x*4+2] = src[x*3+2]; // R
                dst[x*4+3] = 255;        // A
            }
        }
    } else {
        XDestroyImage(img);
        return -3; // unsupported depth
    }

    XDestroyImage(img);
    return 0;
}
*/
import "C"
import (
	"fmt"
	"log"
	"unsafe"
)

// x11Capture is the X11 backend of the Linux capture (capture_linux.go):
// XGetImage of one XRandR monitor. Under Wayland it only sees XWayland
// clients, which is why the portal backend exists.
type x11Capture struct {
	active bool
}

func newX11Capture() linuxCapture { return &x11Capture{} }

func (c *x11Capture) name() string { return "x11" }

func (c *x11Capture) monitors() []MonitorInfo {
	var cmons [16]C.LxMonitorInfo
	n := int(C.lx_enumerate_monitors(&cmons[0], C.int(16)))
	out := make([]MonitorInfo, n)
	for i := 0; i < n; i++ {
		out[i] = MonitorInfo{
			Index:  int(cmons[i].index),
			Name:   C.GoString(&cmons[i].name[0]),
			X:      int(cmons[i].x),
			Y:      int(cmons[i].y),
			Width:  int(cmons[i].w),
			Height: int(cmons[i].h),
		}
	}
	return out
}

func (c *x11Capture) initMonitor(idx int) error {
	ret := int(C.lx_capture_init(C.int(idx)))
	if ret < 0 {
		return fmt.Errorf("X11 capture init failed (code %d)", ret)
	}
	c.active = true
	log.Printf("capture: monitor %d via X11", idx)
	return nil
}

func (c *x11Capture) offset() (x, y int) {
	var cx, cy C.int
	C.lx_get_offset(&cx, &cy)
	return int(cx), int(cy)
}

func (c *x11Capture) close() {
	if c.active {
		C.lx_capture_close()
		c.active = false
	}
}

func (c *x11Capture) size() (w, h int) {
	var cw, ch C.int
	C.lx_get_size(&cw, &ch)
	return int(cw), int(ch)
}

func (c *x11Capture) frame(buf []byte) (width, height int, err error) {
	width, height = c.size()
	expected := width * height * 4
	if expected <= 0 {
		return 0, 0, fmt.Errorf("X11 capture not initialised")
	}
	if len(buf) < expected {
		return 0, 0, fmt.Errorf("buffer too small (%d < %d)", len(buf), expected)
	}
	ret := int(C.lx_capture_frame((*C.uchar)(unsafe.Pointer(&buf[0]))))
	if ret != 0 {
		return width, height, fmt.Errorf("X11 capture failed (code %d)", ret)
	}
	return width, height, nil
}
