//go:build windows

package main

/*
#cgo LDFLAGS: -lgdi32 -luser32

#include <windows.h>
#include <stdlib.h>
#include <string.h>

// ── Toast notification: dark, borderless, bottom-right, auto-close ──────────

#define TOAST_W        360
#define TOAST_H         90
#define TOAST_MARGIN    16
#define ACCENT_W         4
#define ID_CLOSE_TIMER   1
#define ID_FADE_TIMER    2

static BYTE g_toast_alpha = 240;
static const wchar_t *g_toast_title = NULL;
static const wchar_t *g_toast_msg   = NULL;

static void paintToast(HWND hwnd) {
    PAINTSTRUCT ps;
    HDC hdc = BeginPaint(hwnd, &ps);

    // Background: dark slate (#1e293b)
    HBRUSH bgBrush = CreateSolidBrush(RGB(30, 41, 59));
    RECT rc = {0, 0, TOAST_W, TOAST_H};
    FillRect(hdc, &rc, bgBrush);
    DeleteObject(bgBrush);

    // Left accent bar: Oblireach red (#c2001b). Was indigo (#6366f1) — the
    // purple came from the Obligate SSO branding; for an INCOMING Reach
    // connection the toast should use the Oblireach accent.
    HBRUSH acBrush = CreateSolidBrush(RGB(194, 0, 27));
    RECT acRc = {0, 0, ACCENT_W, TOAST_H};
    FillRect(hdc, &acRc, acBrush);
    DeleteObject(acBrush);

    SetBkMode(hdc, TRANSPARENT);

    // Title (bold)
    HFONT titleFont = CreateFontW(
        -15, 0, 0, 0, FW_SEMIBOLD, FALSE, FALSE, FALSE,
        DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS,
        CLEARTYPE_QUALITY, DEFAULT_PITCH | FF_SWISS, L"Segoe UI");
    HFONT oldFont = (HFONT)SelectObject(hdc, titleFont);
    SetTextColor(hdc, RGB(241, 245, 249));
    RECT titleRc = {ACCENT_W + 14, 14, TOAST_W - 14, 40};
    if (g_toast_title) DrawTextW(hdc, g_toast_title, -1, &titleRc, DT_LEFT | DT_SINGLELINE | DT_END_ELLIPSIS);
    SelectObject(hdc, oldFont);
    DeleteObject(titleFont);

    // Message (regular)
    HFONT msgFont = CreateFontW(
        -14, 0, 0, 0, FW_NORMAL, FALSE, FALSE, FALSE,
        DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS,
        CLEARTYPE_QUALITY, DEFAULT_PITCH | FF_SWISS, L"Segoe UI");
    oldFont = (HFONT)SelectObject(hdc, msgFont);
    SetTextColor(hdc, RGB(148, 163, 184));
    RECT msgRc = {ACCENT_W + 14, 42, TOAST_W - 14, TOAST_H - 10};
    if (g_toast_msg) DrawTextW(hdc, g_toast_msg, -1, &msgRc, DT_LEFT | DT_WORDBREAK | DT_END_ELLIPSIS);
    SelectObject(hdc, oldFont);
    DeleteObject(msgFont);

    EndPaint(hwnd, &ps);
}

static LRESULT CALLBACK toastWndProc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp) {
    switch (msg) {
    case WM_CREATE: {
        CREATESTRUCTW *cs = (CREATESTRUCTW*)lp;
        DWORD timeout = cs->lpCreateParams ? *(DWORD*)cs->lpCreateParams : 8;
        SetTimer(hwnd, ID_CLOSE_TIMER, timeout * 1000, NULL);
        return 0;
    }
    case WM_PAINT:
        paintToast(hwnd);
        return 0;
    case WM_TIMER:
        if (wp == ID_CLOSE_TIMER) {
            KillTimer(hwnd, ID_CLOSE_TIMER);
            SetTimer(hwnd, ID_FADE_TIMER, 30, NULL);
        } else if (wp == ID_FADE_TIMER) {
            if (g_toast_alpha <= 15) {
                KillTimer(hwnd, ID_FADE_TIMER);
                DestroyWindow(hwnd);
            } else {
                g_toast_alpha -= 15;
                SetLayeredWindowAttributes(hwnd, 0, g_toast_alpha, LWA_ALPHA);
            }
        }
        return 0;
    case WM_LBUTTONUP:
        DestroyWindow(hwnd);
        return 0;
    case WM_DESTROY:
        PostQuitMessage(0);
        return 0;
    }
    return DefWindowProcW(hwnd, msg, wp, lp);
}

static void showToast(const wchar_t *title, const wchar_t *message, DWORD timeoutSec) {
    g_toast_title = title;
    g_toast_msg   = message;

    WNDCLASSEXW wc;
    ZeroMemory(&wc, sizeof(wc));
    wc.cbSize        = sizeof(wc);
    wc.lpfnWndProc   = toastWndProc;
    wc.hInstance      = GetModuleHandleW(NULL);
    wc.hCursor       = LoadCursor(NULL, IDC_HAND);
    wc.hbrBackground = CreateSolidBrush(RGB(30, 41, 59));
    wc.lpszClassName  = L"OblireachToast";
    RegisterClassExW(&wc);

    // Position: bottom-right of primary monitor work area
    RECT wa;
    SystemParametersInfoW(SPI_GETWORKAREA, 0, &wa, 0);
    int finalX = wa.right  - TOAST_W - TOAST_MARGIN;
    int y      = wa.bottom - TOAST_H - TOAST_MARGIN;

    // Start off-screen to the right for slide-in
    int startX = wa.right + 10;

    HWND hwnd = CreateWindowExW(
        WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_LAYERED | WS_EX_NOACTIVATE,
        L"OblireachToast", NULL,
        WS_POPUP | WS_VISIBLE,
        startX, y, TOAST_W, TOAST_H,
        NULL, NULL, wc.hInstance, &timeoutSec);

    if (!hwnd) return;

    g_toast_alpha = 240;
    SetLayeredWindowAttributes(hwnd, 0, g_toast_alpha, LWA_ALPHA);

    // Slide-in animation (right → left, ~200ms)
    int steps = 8;
    int i;
    for (i = 0; i <= steps; i++) {
        int cx = startX + (finalX - startX) * i / steps;
        SetWindowPos(hwnd, HWND_TOPMOST, cx, y, 0, 0,
                     SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE);
        UpdateWindow(hwnd);
        Sleep(25);
    }

    // Message loop
    MSG m;
    while (GetMessageW(&m, NULL, 0, 0) > 0) {
        TranslateMessage(&m);
        DispatchMessageW(&m);
    }
}
*/
import "C"
import (
	"syscall"
	"unsafe"
)

// runToastNotification displays a dark toast popup in the bottom-right
// corner of the primary monitor.  Blocks until the toast auto-closes or
// the user clicks it.
func runToastNotification(title, message string, timeoutSec int) {
	titleW, _ := syscall.UTF16FromString(title)
	msgW, _ := syscall.UTF16FromString(message)
	C.showToast(
		(*C.wchar_t)(unsafe.Pointer(&titleW[0])),
		(*C.wchar_t)(unsafe.Pointer(&msgW[0])),
		C.DWORD(timeoutSec),
	)
}
