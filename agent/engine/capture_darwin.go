//go:build darwin

package engine

/*
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo -framework CoreGraphics -framework CoreFoundation -framework Foundation

#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

// Globals — shared with screencapture_darwin.m
int      g_mac_width   = 0;
int      g_mac_height  = 0;
int      g_mac_mon_x   = 0;
int      g_mac_mon_y   = 0;
uint32_t g_mac_display = 0;

// Functions implemented in screencapture_darwin.m (Objective-C)
extern int  mac_sck_init(void);
extern int  mac_sck_capture_frame(unsigned char *out_bgra);
extern void mac_sck_close(void);

#define MAC_MAX_MONITORS 16

typedef struct {
    int index;
    char name[64];
    int x, y, w, h;
    uint32_t displayID;
} MacMonitorInfo;

static int mac_enumerate_monitors(MacMonitorInfo *out, int maxCount) {
    uint32_t displayCount = 0;
    CGDirectDisplayID displays[MAC_MAX_MONITORS];
    CGGetActiveDisplayList(MAC_MAX_MONITORS, displays, &displayCount);

    int count = 0;
    uint32_t i;
    for (i = 0; i < displayCount && count < maxCount; i++) {
        CGRect bounds = CGDisplayBounds(displays[i]);
        out[count].index = count;
        out[count].displayID = displays[i];
        if (CGDisplayIsMain(displays[i])) {
            snprintf(out[count].name, 64, "Main Display");
        } else {
            snprintf(out[count].name, 64, "Display %d", i + 1);
        }
        out[count].x = (int)bounds.origin.x;
        out[count].y = (int)bounds.origin.y;
        out[count].w = (int)bounds.size.width;
        out[count].h = (int)bounds.size.height;
        count++;
    }
    return count;
}

static int mac_capture_init(int monitor_idx) {
    MacMonitorInfo mons[MAC_MAX_MONITORS];
    int n = mac_enumerate_monitors(mons, MAC_MAX_MONITORS);

    if (monitor_idx >= 0 && monitor_idx < n) {
        g_mac_display = mons[monitor_idx].displayID;
        g_mac_width   = mons[monitor_idx].w;
        g_mac_height  = mons[monitor_idx].h;
        g_mac_mon_x   = mons[monitor_idx].x;
        g_mac_mon_y   = mons[monitor_idx].y;
    } else if (n > 0) {
        g_mac_display = mons[0].displayID;
        g_mac_width   = mons[0].w;
        g_mac_height  = mons[0].h;
        g_mac_mon_x   = 0;
        g_mac_mon_y   = 0;
    } else {
        g_mac_display = CGMainDisplayID();
        g_mac_width   = (int)CGDisplayPixelsWide(g_mac_display);
        g_mac_height  = (int)CGDisplayPixelsHigh(g_mac_display);
        g_mac_mon_x   = 0;
        g_mac_mon_y   = 0;
    }

    if (g_mac_width <= 0 || g_mac_height <= 0) return -1;

    return mac_sck_init();
}

static void mac_capture_close(void) {
    mac_sck_close();
    g_mac_width = 0;
    g_mac_height = 0;
}

static void mac_get_size(int *w, int *h) {
    *w = g_mac_width;
    *h = g_mac_height;
}

static void mac_get_offset(int *ox, int *oy) {
    *ox = g_mac_mon_x;
    *oy = g_mac_mon_y;
}

static int mac_capture_frame(unsigned char *out_bgra) {
    return mac_sck_capture_frame(out_bgra);
}
*/
import "C"
import (
	"fmt"
	"log"
	"unsafe"
)

type MonitorInfo struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Primary  bool   `json:"primary,omitempty"`
	DPI      int    `json:"dpi,omitempty"`
	Scale    int    `json:"scale,omitempty"`
	Rotation int    `json:"rotation,omitempty"`
}

func enumerateMonitors() []MonitorInfo {
	var cmons [16]C.MacMonitorInfo
	n := int(C.mac_enumerate_monitors(&cmons[0], C.int(16)))
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

var captureActive bool

func captureInitMonitor(idx int) error {
	ret := int(C.mac_capture_init(C.int(idx)))
	if ret < 0 {
		return fmt.Errorf("macOS capture init failed (code %d)", ret)
	}
	captureActive = true
	log.Printf("capture: monitor %d via ScreenCaptureKit", idx)
	return nil
}

func captureMonitorOffset() (x, y int) {
	var cx, cy C.int
	C.mac_get_offset(&cx, &cy)
	return int(cx), int(cy)
}

func captureInit() error {
	return captureInitMonitor(0)
}

func captureClose() {
	if captureActive {
		C.mac_capture_close()
		captureActive = false
	}
}

func captureWidth() int {
	var w, h C.int
	C.mac_get_size(&w, &h)
	return int(w)
}

func captureHeight() int {
	var w, h C.int
	C.mac_get_size(&w, &h)
	return int(h)
}

func captureFrame(buf []byte) (width, height int, err error) {
	var w, h C.int
	C.mac_get_size(&w, &h)
	width = int(w)
	height = int(h)

	expected := width * height * 4
	if len(buf) < expected {
		return 0, 0, fmt.Errorf("buffer too small (%d < %d)", len(buf), expected)
	}

	ret := int(C.mac_capture_frame((*C.uchar)(unsafe.Pointer(&buf[0]))))
	if ret == 1 {
		return width, height, fmt.Errorf("no new frame")
	}
	if ret != 0 {
		return width, height, fmt.Errorf("macOS capture failed (code %d)", ret)
	}
	return width, height, nil
}
