//go:build windows

package engine

/*
#cgo LDFLAGS: -lgdi32 -luser32

#include <windows.h>
#include <stdlib.h>
#include <string.h>

// Previous 1.0.188 attempt used SetWindowCompositionAttribute with
// ACCENT_ENABLE_ACRYLICBLURBEHIND. On Server 2025 either the undocumented
// API returned FALSE or the acrylic didn't compose because WS_EX_LAYERED +
// SetWindowRgn don't cooperate with the DWM blur pipeline. User screenshot
// showed: opaque dark pill, crenellated/aliased edges, wasted right
// padding. Real frosted-glass would need UpdateLayeredWindow with per-
// pixel alpha from a GDI+ rendered bitmap — bigger refactor, deferred.
// This version gives a clean tight capsule with tuned colors.

#define WM_PILL_W 58
#define WM_PILL_H 24
#define WM_PILL_RADIUS 12 // = height/2 ⇒ perfect capsule ends
#define WM_USER_SET_MODE (WM_USER + 1)

static HWND g_watermark = NULL;
static HFONT g_wm_font = NULL;
static int g_wm_recording = 0; // 0 = LIVE, 1 = REC

// wm_place puts the pill at the top-right of the primary work area. Called at
// creation and again when the display or the work area changes: after a lower
// resolution the old position could be off-screen, and the local user must
// keep seeing that a remote session is live / recorded.
static void wm_place(HWND hwnd) {
    RECT wa;
    if (!hwnd || !SystemParametersInfoW(SPI_GETWORKAREA, 0, &wa, 0)) return;
    SetWindowPos(hwnd, HWND_TOPMOST, wa.right - WM_PILL_W - 12, wa.top + 8, 0, 0,
        SWP_NOSIZE | SWP_NOACTIVATE);
}

static LRESULT CALLBACK wmWndProc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp) {
    switch (msg) {
    case WM_USER_SET_MODE:
        g_wm_recording = (int)wp;
        InvalidateRect(hwnd, NULL, TRUE);
        return 0;
    case WM_DISPLAYCHANGE:
        wm_place(hwnd);
        return 0;
    case WM_SETTINGCHANGE:
        if (wp == SPI_SETWORKAREA) wm_place(hwnd);
        break;
    case WM_PAINT: {
        PAINTSTRUCT ps;
        HDC hdc = BeginPaint(hwnd, &ps);

        // Capsule fill + 1px border. NeonUI chat-aligned palette:
        //   fill    = #161b22 (chat "elevated surface" bg, input bar, header)
        //   border  = ~ rgba(255,255,255,0.08) over #161b22 = #252c35
        // Combined with WS_EX_LAYERED LWA_ALPHA below, the whole pill shows
        // at ~75% opacity for a see-through feel matching the chat's
        // rgba-on-dark aesthetic.
        HBRUSH bgBr = CreateSolidBrush(RGB(22, 27, 34));
        HPEN bgPen = CreatePen(PS_SOLID, 1, RGB(37, 44, 53));
        HBRUSH oldBr = (HBRUSH)SelectObject(hdc, bgBr);
        HPEN   oldPen = (HPEN)SelectObject(hdc, bgPen);
        RoundRect(hdc, 0, 0, WM_PILL_W, WM_PILL_H, WM_PILL_RADIUS*2, WM_PILL_RADIUS*2);
        SelectObject(hdc, oldBr);
        SelectObject(hdc, oldPen);
        DeleteObject(bgBr);
        DeleteObject(bgPen);

        // Accent dot — green #22c55e for LIVE (chat "allow" button), red
        // #c2001b for REC (Oblireach accent, same as chat operator avatar).
        COLORREF accent = g_wm_recording ? RGB(194, 0, 27) : RGB(34, 197, 94);
        HBRUSH dotBr = CreateSolidBrush(accent);
        HPEN dotPen = CreatePen(PS_SOLID, 1, accent);
        SelectObject(hdc, dotBr);
        SelectObject(hdc, dotPen);
        // 6×6 dot, vertically centered at y=(H-6)/2 = 9, x=9 (from left edge).
        Ellipse(hdc, 9, 9, 15, 15);
        DeleteObject(dotBr);
        DeleteObject(dotPen);

        // Text — body fg #e6edf3 (chat body text), slightly brighter than
        // the accent so it's readable without echoing the dot colour twice.
        SetBkMode(hdc, TRANSPARENT);
        if (!g_wm_font)
            g_wm_font = CreateFontW(-11, 0, 0, 0, FW_SEMIBOLD, 0, 0, 0,
                DEFAULT_CHARSET, 0, 0, CLEARTYPE_QUALITY, DEFAULT_PITCH, L"Segoe UI");
        SelectObject(hdc, g_wm_font);
        SetTextColor(hdc, RGB(230, 237, 243));
        // Text region starts after the dot+gap. DT_CENTER in that region
        // keeps "LIVE" / "REC" balanced — avoids the big right gap that the
        // previous DT_LEFT version produced.
        RECT rc = {18, 3, WM_PILL_W - 4, WM_PILL_H - 3};
        DrawTextW(hdc, g_wm_recording ? L"REC" : L"LIVE", -1, &rc,
            DT_CENTER | DT_SINGLELINE | DT_VCENTER);

        EndPaint(hwnd, &ps);
        return 0;
    }
    case WM_NCHITTEST:
        return HTTRANSPARENT;
    }
    return DefWindowProcW(hwnd, msg, wp, lp);
}

static void show_watermark_rec(void) {
    if (g_watermark) return;

    WNDCLASSEXW wc;
    ZeroMemory(&wc, sizeof(wc));
    wc.cbSize = sizeof(wc);
    wc.lpfnWndProc = wmWndProc;
    wc.hInstance = GetModuleHandleW(NULL);
    wc.hbrBackground = NULL;
    wc.lpszClassName = L"OblireachRec";
    RegisterClassExW(&wc);

    RECT wa;
    SystemParametersInfoW(SPI_GETWORKAREA, 0, &wa, 0);
    int x = wa.right - WM_PILL_W - 12;
    int y = wa.top + 8;

    g_watermark = CreateWindowExW(
        WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_NOACTIVATE,
        L"OblireachRec", NULL,
        WS_POPUP | WS_VISIBLE,
        x, y, WM_PILL_W, WM_PILL_H,
        NULL, NULL, wc.hInstance, NULL);

    if (g_watermark) {
        // Physically shape the window as a capsule via SetWindowRgn — pixels
        // outside the region don't exist (no alpha artefacts on the corners).
        HRGN rgn = CreateRoundRectRgn(0, 0, WM_PILL_W + 1, WM_PILL_H + 1,
            WM_PILL_RADIUS * 2, WM_PILL_RADIUS * 2);
        SetWindowRgn(g_watermark, rgn, TRUE);
        // Uniform ~73% opacity so the pill is clearly present but the
        // background content bleeds through — as close to "transparent" as
        // GDI without per-pixel alpha gets. Full acrylic would need
        // UpdateLayeredWindow + a GDI+ rendered premultiplied-alpha bitmap.
        SetLayeredWindowAttributes(g_watermark, 0, 185, LWA_ALPHA);
    }
}

// set_watermark_mode: switch between LIVE (0) and REC (1).
// Safe to call from any thread.
static void set_watermark_mode(int recording) {
    if (g_watermark) {
        PostMessageW(g_watermark, WM_USER_SET_MODE, (WPARAM)recording, 0);
    }
}

static void hide_watermark_rec(void) {
    if (g_watermark) {
        DestroyWindow(g_watermark);
        g_watermark = NULL;
    }
    if (g_wm_font) {
        DeleteObject(g_wm_font);
        g_wm_font = NULL;
    }
}
// g_watermark_tid: thread ID of the watermark message pump thread.
static DWORD g_watermark_tid = 0;

// watermark_run: creates the watermark and runs a message pump.
// Blocks until WM_QUIT is posted to this thread.
static void watermark_run(void) {
    g_watermark_tid = GetCurrentThreadId();
    show_watermark_rec();
    MSG msg;
    while (GetMessage(&msg, NULL, 0, 0) > 0) {
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
    }
    hide_watermark_rec();
    g_watermark_tid = 0;
}

// stop_watermark_pump: posts WM_QUIT to the watermark thread.
// Safe to call from any thread.
static void stop_watermark_pump(void) {
    DWORD tid = g_watermark_tid;
    if (tid != 0) {
        PostThreadMessageW(tid, WM_QUIT, 0, 0);
    }
}
*/
import "C"

import "runtime"

var watermarkRunning bool

func ShowWatermark(operatorName string) {
	if watermarkRunning {
		return
	}
	watermarkRunning = true
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		C.watermark_run() // blocks until stop_watermark_pump is called
		watermarkRunning = false
	}()
}

func HideWatermark() {
	if watermarkRunning {
		C.stop_watermark_pump()
	}
}

func SetWatermarkRecording(recording bool) {
	if watermarkRunning {
		v := C.int(0)
		if recording {
			v = 1
		}
		C.set_watermark_mode(v)
	}
}
