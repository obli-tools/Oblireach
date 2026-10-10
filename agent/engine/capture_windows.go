//go:build windows

package engine

/*
#cgo CFLAGS: -DCOBJMACROS -DINITGUID
// NO -ld3d11 / -ldxgi: d3d11.dll and dxgi.dll are loaded at run time
// (cap_dxgi_dll / cap_d3d11_dll below), so the exe starts on any Windows even
// when one of them is missing or unusable; the capture then uses GDI.
#cgo LDFLAGS: -lgdi32 -luser32

#include <windows.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <stdlib.h>
#include <string.h>

// ── d3d11.dll / dxgi.dll, loaded at run time ─────────────────────────────────
typedef HRESULT (WINAPI *PFN_CreateDXGIFactory1)(REFIID, void **);
typedef HRESULT (WINAPI *PFN_D3D11CreateDevice)(IDXGIAdapter *, D3D_DRIVER_TYPE, HMODULE, UINT,
    const D3D_FEATURE_LEVEL *, UINT, UINT, ID3D11Device **, D3D_FEATURE_LEVEL *, ID3D11DeviceContext **);

static PFN_CreateDXGIFactory1 p_CreateDXGIFactory1 = NULL;
static PFN_D3D11CreateDevice  p_D3D11CreateDevice  = NULL;
static volatile LONG          g_dxgi_dll_state     = 0; // 0 not tried, 1 loaded, -1 dxgi.dll unusable
static volatile LONG          g_d3d11_dll_state    = 0; // same for d3d11.dll

// Desktop Duplication cannot exist on this Windows (< 8 / Server 2012): set
// from Go at start (os_version.go). dxgi_init then fails at once with -7
// (permanent): no D3D11 device is ever created for nothing.
static volatile LONG          g_dxgi_disabled      = 0;
static void cap_set_dxgi_disabled(int v) { InterlockedExchange(&g_dxgi_disabled, v ? 1 : 0); }
static int  cap_dxgi_is_disabled(void)    { return InterlockedCompareExchange(&g_dxgi_disabled, 0, 0) != 0; }

// cap_load_system_dll: a DLL from System32 only (never the install folder,
// which is on the DLL search path for the codec DLLs). LOAD_LIBRARY_SEARCH_
// SYSTEM32 needs KB2533623 on Windows 7 / 2008 R2 (ERROR_INVALID_PARAMETER
// without it): the full System32 path is used then.
static HMODULE cap_load_system_dll(const wchar_t *name) {
    HMODULE h = LoadLibraryExW(name, NULL, 0x00000800); // LOAD_LIBRARY_SEARCH_SYSTEM32
    if (!h) {
        wchar_t path[MAX_PATH + 64];
        UINT n = GetSystemDirectoryW(path, MAX_PATH);
        if (n > 0 && n < MAX_PATH) {
            path[n] = 0;
            wcscat(path, L"\\");
            wcscat(path, name);
            h = LoadLibraryExW(path, NULL, 0x00000008); // LOAD_WITH_ALTERED_SEARCH_PATH
        }
    }
    return h;
}

static int cap_dxgi_dll(void) {
    LONG st = InterlockedCompareExchange(&g_dxgi_dll_state, 0, 0);
    if (st != 0) return st > 0;
    HMODULE h = cap_load_system_dll(L"dxgi.dll");
    PFN_CreateDXGIFactory1 f = h ? (PFN_CreateDXGIFactory1)GetProcAddress(h, "CreateDXGIFactory1") : NULL;
    if (!f) {
        if (h) FreeLibrary(h);
        InterlockedExchange(&g_dxgi_dll_state, -1);
        return 0;
    }
    p_CreateDXGIFactory1 = f; // the DLL stays loaded for the process lifetime
    InterlockedExchange(&g_dxgi_dll_state, 1);
    return 1;
}

static int cap_d3d11_dll(void) {
    LONG st = InterlockedCompareExchange(&g_d3d11_dll_state, 0, 0);
    if (st != 0) return st > 0;
    HMODULE h = cap_load_system_dll(L"d3d11.dll");
    PFN_D3D11CreateDevice f = h ? (PFN_D3D11CreateDevice)GetProcAddress(h, "D3D11CreateDevice") : NULL;
    if (!f) {
        if (h) FreeLibrary(h);
        InterlockedExchange(&g_d3d11_dll_state, -1);
        return 0;
    }
    p_D3D11CreateDevice = f;
    InterlockedExchange(&g_d3d11_dll_state, 1);
    return 1;
}

// Same signatures as the SDK functions; E_NOINTERFACE when the DLL is missing.
static HRESULT cap_CreateDXGIFactory1(REFIID riid, void **pp) {
    if (pp) *pp = NULL;
    if (!cap_dxgi_dll()) return E_NOINTERFACE;
    return p_CreateDXGIFactory1(riid, pp);
}

static HRESULT cap_D3D11CreateDevice(IDXGIAdapter *adp, D3D_DRIVER_TYPE dt, HMODULE sw, UINT flags,
    const D3D_FEATURE_LEVEL *fls, UINT nfl, UINT sdk, ID3D11Device **dev, D3D_FEATURE_LEVEL *fl,
    ID3D11DeviceContext **ctx) {
    if (dev) *dev = NULL;
    if (ctx) *ctx = NULL;
    if (!cap_d3d11_dll()) return E_NOINTERFACE;
    return p_D3D11CreateDevice(adp, dt, sw, flags, fls, nfl, sdk, dev, fl, ctx);
}

static int cap_dxgi_dll_ok(void)  { return cap_dxgi_dll(); }
static int cap_d3d11_dll_ok(void) { return cap_d3d11_dll(); }

// ── DXGI globals ──────────────────────────────────────────────────────────────
static ID3D11Device           *g_device  = NULL;
static ID3D11DeviceContext    *g_ctx     = NULL;
static IDXGIOutputDuplication *g_dup     = NULL;
static ID3D11Texture2D        *g_staging = NULL;

// ── GDI globals (fallback for RDP / remote sessions) ─────────────────────────
static int     g_use_gdi   = 0;
static HDC     g_hdcScreen = NULL;
static HDC     g_hdcMem    = NULL;
static HBITMAP g_hBitmap   = NULL;
static HBITMAP g_hOldBmp   = NULL;

// ── Common ────────────────────────────────────────────────────────────────────
static int g_width  = 0;
static int g_height = 0;
static int g_mon_x  = 0; // monitor origin (for multi-monitor coordinate mapping)
static int g_mon_y  = 0;
static int g_target_monitor = 0; // which monitor to capture
static int g_gdi_virtual = 0;    // GDI copies the whole virtual screen (not one monitor)
// Display mode changes (resolution, orientation, scaling, a monitor added or
// removed) land in the capture while it runs: DXGI reports ACCESS_LOST and the
// native thread re-initialises at the new geometry (see cap_recover).
static int g_rotation = 1;      // DXGI_MODE_ROTATION of the duplicated output (1 identity, 2=90, 3=180, 4=270)
static int g_staging_w = 0;     // size of g_staging = the duplicated texture (unrotated)
static int g_staging_h = 0;
static int g_tex_mismatch = 0;  // consecutive frames whose texture was not the size the output reports
static int g_tex_mismatch_reinits = 0; // re-inits it caused since the last frame of the right size

// DXGI_ERROR_UNSUPPORTED: the adapter does not do Desktop Duplication at all
// (basic display, some RDP / virtual adapters). Not a transient failure.
#define OR_DXGI_ERROR_UNSUPPORTED 0x887A0004u

// ── Monitor enumeration ──────────────────────────────────────────────────────

#define OR_MAX_MONITORS 16

typedef struct {
    int index;
    wchar_t name[32];
    int x, y, w, h;
    int primary;   // 1 if MONITORINFOF_PRIMARY
    int rotation;  // DXGI_MODE_ROTATION (1 identity, 2=90, 3=180, 4=270)
} MonitorInfoC;

// GDI enumeration, used only when DXGI cannot even create a factory
// (dxgi.dll missing or broken): same fields, EnumDisplayMonitors order.
typedef struct { MonitorInfoC *out; int max; int count; } EnumMonCtx;

static BOOL CALLBACK enum_mon_gdi_cb(HMONITOR hm, HDC hdc, LPRECT rc, LPARAM lp) {
    (void)hdc; (void)rc;
    EnumMonCtx *c = (EnumMonCtx *)lp;
    if (c->count >= c->max) return FALSE;
    MONITORINFOEXW mi;
    ZeroMemory(&mi, sizeof(mi));
    mi.cbSize = sizeof(mi);
    if (!GetMonitorInfoW(hm, (LPMONITORINFO)&mi)) return TRUE;
    MonitorInfoC *m = &c->out[c->count];
    ZeroMemory(m, sizeof(*m));
    m->index = c->count;
    wcsncpy(m->name, mi.szDevice, 31);
    m->x = mi.rcMonitor.left;
    m->y = mi.rcMonitor.top;
    m->w = mi.rcMonitor.right - mi.rcMonitor.left;
    m->h = mi.rcMonitor.bottom - mi.rcMonitor.top;
    m->primary = (mi.dwFlags & MONITORINFOF_PRIMARY) ? 1 : 0;
    m->rotation = 1;
    c->count++;
    return TRUE;
}

static int enumerate_monitors_gdi(MonitorInfoC *out, int maxCount) {
    EnumMonCtx c;
    c.out = out; c.max = maxCount; c.count = 0;
    if (!out || maxCount <= 0) return 0;
    EnumDisplayMonitors(NULL, NULL, enum_mon_gdi_cb, (LPARAM)&c);
    return c.count;
}

static int enumerate_monitors(MonitorInfoC *out, int maxCount) {
    IDXGIFactory1 *factory = NULL;
    HRESULT hr;
    int count = 0;
    UINT ai, oi;
    IDXGIAdapter *adapter = NULL;
    IDXGIOutput *output = NULL;

    // dxgi.dll is loaded at run time; no factory at all -> GDI enumeration.
    hr = cap_CreateDXGIFactory1(&IID_IDXGIFactory1, (void**)&factory);
    if (FAILED(hr) || !factory) return enumerate_monitors_gdi(out, maxCount);

    for (ai = 0; IDXGIFactory1_EnumAdapters(factory, ai, &adapter) == S_OK; ai++) {
        for (oi = 0; IDXGIAdapter_EnumOutputs(adapter, oi, &output) == S_OK; oi++) {
            if (count >= maxCount) { IUnknown_Release(output); break; }
            DXGI_OUTPUT_DESC desc;
            IDXGIOutput_GetDesc(output, &desc);
            out[count].index = count;
            memcpy(out[count].name, desc.DeviceName, sizeof(desc.DeviceName));
            out[count].x = desc.DesktopCoordinates.left;
            out[count].y = desc.DesktopCoordinates.top;
            out[count].w = desc.DesktopCoordinates.right - desc.DesktopCoordinates.left;
            out[count].h = desc.DesktopCoordinates.bottom - desc.DesktopCoordinates.top;
            out[count].rotation = (int)desc.Rotation;
            out[count].primary = 0;
            // Primary = the monitor GetMonitorInfo flags as MONITORINFOF_PRIMARY.
            // Falls back to the DXGI origin (0,0) when the HMONITOR query fails.
            if (desc.Monitor) {
                MONITORINFO mi; mi.cbSize = sizeof(mi);
                if (GetMonitorInfoW(desc.Monitor, &mi))
                    out[count].primary = (mi.dwFlags & MONITORINFOF_PRIMARY) ? 1 : 0;
            }
            if (!out[count].primary && desc.DesktopCoordinates.left == 0 && desc.DesktopCoordinates.top == 0)
                out[count].primary = 1;
            count++;
            IUnknown_Release(output);
        }
        IUnknown_Release(adapter);
    }
    IUnknown_Release(factory);
    return count;
}

static void capture_get_monitor_offset(int *ox, int *oy) {
    *ox = g_mon_x;
    *oy = g_mon_y;
}

// ── Cursor overlay ───────────────────────────────────────────────────────────

static void overlay_cursor_gdi(unsigned char *bgra, int w, int h, int mon_x, int mon_y) {
    CURSORINFO ci;
    ci.cbSize = sizeof(ci);
    if (!GetCursorInfo(&ci)) return;
    if (!(ci.flags & CURSOR_SHOWING)) return;

    int cx = ci.ptScreenPos.x - mon_x;
    int cy = ci.ptScreenPos.y - mon_y;
    if (cx < -32 || cx > w + 32 || cy < -32 || cy > h + 32) return;

    ICONINFO ii;
    if (!GetIconInfo(ci.hCursor, &ii)) return;

    int hotX = (int)ii.xHotspot;
    int hotY = (int)ii.yHotspot;
    int drawX = cx - hotX;
    int drawY = cy - hotY;

    // Draw cursor onto a temporary DC, then alpha-blend into the BGRA buffer
    HDC hdcScreen = GetDC(NULL);
    HDC hdcMem = CreateCompatibleDC(hdcScreen);
    HBITMAP hbm = CreateCompatibleBitmap(hdcScreen, w, h);
    HBITMAP old = (HBITMAP)SelectObject(hdcMem, hbm);

    // Copy existing frame into the DC
    BITMAPINFO bmi;
    ZeroMemory(&bmi, sizeof(bmi));
    bmi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    bmi.bmiHeader.biWidth = w;
    bmi.bmiHeader.biHeight = -h;
    bmi.bmiHeader.biPlanes = 1;
    bmi.bmiHeader.biBitCount = 32;
    bmi.bmiHeader.biCompression = BI_RGB;
    SetDIBitsToDevice(hdcMem, 0, 0, w, h, 0, 0, 0, h, bgra, &bmi, DIB_RGB_COLORS);

    // Draw the cursor
    DrawIconEx(hdcMem, drawX, drawY, ci.hCursor, 0, 0, 0, NULL, DI_NORMAL);

    // Read back
    GetDIBits(hdcMem, hbm, 0, h, bgra, &bmi, DIB_RGB_COLORS);

    SelectObject(hdcMem, old);
    DeleteObject(hbm);
    DeleteDC(hdcMem);
    ReleaseDC(NULL, hdcScreen);

    if (ii.hbmMask) DeleteObject(ii.hbmMask);
    if (ii.hbmColor) DeleteObject(ii.hbmColor);
}

// ── DXGI helpers ──────────────────────────────────────────────────────────────

static void dxgi_close(void) {
    if (g_staging) { IUnknown_Release(g_staging); g_staging = NULL; }
    if (g_dup)     { IUnknown_Release(g_dup);     g_dup     = NULL; }
    if (g_ctx)     { IUnknown_Release(g_ctx);     g_ctx     = NULL; }
    if (g_device)  { IUnknown_Release(g_device);  g_device  = NULL; }
    g_staging_w = g_staging_h = 0;
    g_tex_mismatch = 0;
}

// g_dxgi_last_fail: exit code of the most recent dxgi_init failure, for diag.
// g_dxgi_last_hr: raw HRESULT from DuplicateOutput when it was the failing step.
static int g_dxgi_last_fail = 0;
static unsigned int g_dxgi_last_hr = 0;
static int dxgi_last_fail(void) { return g_dxgi_last_fail; }
static unsigned int dxgi_last_hr(void) { return g_dxgi_last_hr; }

// Returns 0 on success, negative on any failure (resources cleaned up).
// monitor_idx: which output to capture (0 = primary, enumerate order).
//
// IMPORTANT: DXGI requires the D3D11 device to be created on the SAME
// adapter that owns the target output. Using a device from a different
// adapter makes DuplicateOutput return E_INVALIDARG (0x80070057). That
// matters for systems with multiple adapters — e.g. a Hyper-V VM where
// the primary is "Microsoft Hyper-V Video" and the Virtual Display
// Driver is a separate IDD adapter: we must enumerate first, find the
// adapter that owns monitor_idx, then create D3D11 on it.
static int dxgi_init(int monitor_idx) {
    // Windows < 8: no Desktop Duplication at all (-7, permanent). d3d11.dll
    // missing: same (nothing to duplicate with).
    if (cap_dxgi_is_disabled() || !cap_d3d11_dll()) {
        g_dxgi_last_fail = -7; g_dxgi_last_hr = 0; return -7;
    }
    IDXGIFactory1 *factory = NULL;
    HRESULT hr = cap_CreateDXGIFactory1(&IID_IDXGIFactory1, (void**)&factory);
    if (FAILED(hr) || !factory) { g_dxgi_last_fail = -1; g_dxgi_last_hr = (unsigned int)hr; return -1; }

    // First pass: locate the adapter that owns monitor_idx.
    IDXGIAdapter *targetAdapter = NULL;
    IDXGIOutput *targetOutput = NULL;
    int cur = 0;
    UINT ai, oi;
    IDXGIAdapter *adp = NULL;
    for (ai = 0; !targetOutput && IDXGIFactory1_EnumAdapters(factory, ai, &adp) == S_OK; ai++) {
        IDXGIOutput *out = NULL;
        for (oi = 0; IDXGIAdapter_EnumOutputs(adp, oi, &out) == S_OK; oi++) {
            if (cur == monitor_idx) {
                targetOutput = out;              // keep ref, released later
                targetAdapter = adp;             // keep ref (will be released after device creation)
                IDXGIAdapter_AddRef(targetAdapter);
                break;
            }
            cur++;
            IUnknown_Release(out);
        }
        IUnknown_Release(adp);
    }
    IUnknown_Release(factory);

    if (!targetOutput || !targetAdapter) {
        g_dxgi_last_fail = -4;
        if (targetOutput) IUnknown_Release(targetOutput);
        if (targetAdapter) IUnknown_Release(targetAdapter);
        return -4;
    }

    // Create D3D11 device on the adapter that owns this output. With an
    // explicit adapter the driver type MUST be D3D_DRIVER_TYPE_UNKNOWN.
    D3D_FEATURE_LEVEL fl;
    hr = cap_D3D11CreateDevice(
        (IDXGIAdapter*)targetAdapter, D3D_DRIVER_TYPE_UNKNOWN, NULL,
        0, NULL, 0, D3D11_SDK_VERSION,
        &g_device, &fl, &g_ctx
    );
    IUnknown_Release(targetAdapter);
    if (FAILED(hr)) {
        IUnknown_Release(targetOutput);
        g_dxgi_last_fail = -1;
        g_dxgi_last_hr = (unsigned int)hr;
        return -1;
    }

    DXGI_OUTPUT_DESC desc;
    IDXGIOutput_GetDesc(targetOutput, &desc);
    g_width  = desc.DesktopCoordinates.right  - desc.DesktopCoordinates.left;
    g_height = desc.DesktopCoordinates.bottom - desc.DesktopCoordinates.top;
    g_mon_x  = desc.DesktopCoordinates.left;
    g_mon_y  = desc.DesktopCoordinates.top;

    IDXGIOutput1 *output1 = NULL;
    hr = IDXGIOutput_QueryInterface(targetOutput, &IID_IDXGIOutput1, (void**)&output1);
    IUnknown_Release(targetOutput);
    if (FAILED(hr)) { g_dxgi_last_fail = -5; dxgi_close(); return -5; }

    hr = IDXGIOutput1_DuplicateOutput(output1, (IUnknown*)g_device, &g_dup);
    IUnknown_Release(output1);
    if (FAILED(hr)) {
        // HRESULT encodes the reason: E_ACCESSDENIED = no rights (secure
        // desktop / cross-session), DXGI_ERROR_UNSUPPORTED = adapter does
        // not implement desktop duplication, E_INVALIDARG = device/output
        // adapter mismatch (should no longer happen after the fix above).
        g_dxgi_last_fail = -6;
        g_dxgi_last_hr = (unsigned int)hr;
        dxgi_close(); g_width = 0; g_height = 0; return -6;
    }

    // Orientation: DXGI hands the desktop image in the panel's native
    // orientation, so a portrait output (90/270) gives a texture of
    // g_height x g_width. The enumerated rotation wins (the duplication
    // description can say UNSPECIFIED), as in the mosaic.
    g_rotation = (desc.Rotation >= 1 && desc.Rotation <= 4) ? (int)desc.Rotation : 0;
    if (!g_rotation) {
        DXGI_OUTDUPL_DESC dd;
        IDXGIOutputDuplication_GetDesc(g_dup, &dd);
        g_rotation = (dd.Rotation >= 1 && dd.Rotation <= 4) ? (int)dd.Rotation : 1;
    }
    g_tex_mismatch = 0;
    return 0;
}

// ── GDI helpers ───────────────────────────────────────────────────────────────

static void gdi_close(void) {
    if (g_hOldBmp && g_hdcMem) SelectObject(g_hdcMem, g_hOldBmp);
    if (g_hBitmap)   { DeleteObject(g_hBitmap);        g_hBitmap   = NULL; }
    if (g_hdcMem)    { DeleteDC(g_hdcMem);             g_hdcMem    = NULL; }
    if (g_hdcScreen) { ReleaseDC(NULL, g_hdcScreen);   g_hdcScreen = NULL; }
    g_hOldBmp = NULL;
    g_use_gdi = 0;
}

// gdi_init: rx/ry/rw/rh = region to capture. If rw <= 0, captures virtual screen.
static int gdi_init(int rx, int ry, int rw, int rh) {
    g_hdcScreen = GetDC(NULL);
    if (!g_hdcScreen) return -10;

    g_gdi_virtual = (rw > 0 && rh > 0) ? 0 : 1;
    if (rw > 0 && rh > 0) {
        g_width  = rw;
        g_height = rh;
        g_mon_x  = rx;
        g_mon_y  = ry;
    } else {
        g_width  = GetSystemMetrics(SM_CXVIRTUALSCREEN);
        g_height = GetSystemMetrics(SM_CYVIRTUALSCREEN);
        g_mon_x  = GetSystemMetrics(SM_XVIRTUALSCREEN);
        g_mon_y  = GetSystemMetrics(SM_YVIRTUALSCREEN);
    }
    if (g_width <= 0 || g_height <= 0) {
        ReleaseDC(NULL, g_hdcScreen);
        g_hdcScreen = NULL;
        return -11;
    }

    g_hdcMem = CreateCompatibleDC(g_hdcScreen);
    if (!g_hdcMem) { gdi_close(); return -12; }

    g_hBitmap = CreateCompatibleBitmap(g_hdcScreen, g_width, g_height);
    if (!g_hBitmap) { gdi_close(); return -13; }

    g_hOldBmp = (HBITMAP)SelectObject(g_hdcMem, g_hBitmap);
    g_use_gdi = 1;
    return 0;
}

// ── Public capture API ────────────────────────────────────────────────────────

// capture_init_monitor: tries DXGI first; falls back to GDI.
static int capture_init_monitor(int monitor_idx) {
    g_target_monitor = monitor_idx;

    if (dxgi_init(monitor_idx) == 0) return 0;

    // DXGI unavailable — use GDI. Find monitor region via enumeration.
    MonitorInfoC mons[OR_MAX_MONITORS];
    int n = enumerate_monitors(mons, OR_MAX_MONITORS);
    if (monitor_idx >= 0 && monitor_idx < n) {
        return gdi_init(mons[monitor_idx].x, mons[monitor_idx].y,
                        mons[monitor_idx].w, mons[monitor_idx].h);
    }
    return gdi_init(0, 0, 0, 0); // fallback: entire virtual screen
}

// capture_init: backward compat — captures primary monitor.
static int capture_init(void) {
    return capture_init_monitor(0);
}

// capture_init_dxgi_only: attempts DXGI desktop duplication on monitor_idx
// without the GDI fallback. Used by capture_init_best to iterate monitors
// and pick the first one that actually supports duplication — the Virtual
// Display Driver does, while Hyper-V "basic" video and disconnected RDP
// outputs do not.
static int capture_init_dxgi_only(int monitor_idx) {
    g_target_monitor = monitor_idx;
    return dxgi_init(monitor_idx);
}

// capture_init_best: iterates every enumerated monitor and picks the first
// one on which DXGI Desktop Duplication succeeds. Falls back to GDI on
// monitor 0 if no DXGI output works at all (e.g. pure session-0 context).
// Returns 0 on success, negative on failure.
//
// g_diag_init records the per-output attempt results so the Go caller can
// surface the full picture to the log (adapter descriptions + HRESULTs).
#define OR_MAX_DIAG 8
typedef struct {
    wchar_t adapter[64];
    wchar_t device[32];
    int     w, h;
    int     step;       // where dxgi_init failed for this output (0 if ok)
    unsigned int hr;    // HRESULT of the last failing step
} DxgiDiagEntry;
static DxgiDiagEntry g_diag[OR_MAX_DIAG];
static int g_diag_count = 0;

// dxgi_diag_dump populates g_diag with one entry per output, trying each
// in isolation so we can see which adapter+output succeeded and why the
// others failed. Called by capture_init_best.
static void dxgi_diag_collect(void) {
    g_diag_count = 0;
    IDXGIFactory1 *factory = NULL;
    if (FAILED(cap_CreateDXGIFactory1(&IID_IDXGIFactory1, (void**)&factory)) || !factory) return;
    int idx = 0;
    IDXGIAdapter *adp = NULL;
    for (UINT ai = 0; IDXGIFactory1_EnumAdapters(factory, ai, &adp) == S_OK; ai++) {
        DXGI_ADAPTER_DESC adesc;
        IDXGIAdapter_GetDesc(adp, &adesc);
        IDXGIOutput *out = NULL;
        for (UINT oi = 0; IDXGIAdapter_EnumOutputs(adp, oi, &out) == S_OK; oi++) {
            if (idx < OR_MAX_DIAG) {
                DXGI_OUTPUT_DESC odesc;
                IDXGIOutput_GetDesc(out, &odesc);
                wcsncpy(g_diag[idx].adapter, adesc.Description, 63);
                wcsncpy(g_diag[idx].device, odesc.DeviceName, 31);
                g_diag[idx].w = odesc.DesktopCoordinates.right - odesc.DesktopCoordinates.left;
                g_diag[idx].h = odesc.DesktopCoordinates.bottom - odesc.DesktopCoordinates.top;
                g_diag[idx].step = 0;
                g_diag[idx].hr = 0;
                g_diag_count = idx + 1;
                // Try dxgi_init(idx) to capture failure code for this output.
                if (dxgi_init(idx) == 0) {
                    dxgi_close();
                } else {
                    g_diag[idx].step = g_dxgi_last_fail;
                    g_diag[idx].hr = g_dxgi_last_hr;
                }
            }
            idx++;
            IUnknown_Release(out);
        }
        IUnknown_Release(adp);
    }
    IUnknown_Release(factory);
}

static int diag_count(void) { return g_diag_count; }
static DxgiDiagEntry *diag_entry(int i) { return &g_diag[i]; }

// capture_init_best_dxgi: the first output on which DXGI Desktop Duplication
// succeeds, without the GDI fallback. Returns 0 on success, -1 otherwise.
static int capture_init_best_dxgi(void) {
    MonitorInfoC mons[OR_MAX_MONITORS];
    int n = enumerate_monitors(mons, OR_MAX_MONITORS);
    for (int i = 0; i < n; i++) {
        if (dxgi_init(i) == 0) {
            g_target_monitor = i;
            return 0;
        }
    }
    return -1;
}

static int capture_init_best(void) {
    if (capture_init_best_dxgi() == 0) return 0;
    // No DXGI output usable — last-resort GDI on the virtual screen.
    return gdi_init(0, 0, 0, 0);
}

// (The recovery after DXGI_ERROR_ACCESS_LOST — UAC prompt, workstation lock,
// login screen transition, display mode change — is cap_recover, on the
// native capture thread below.)

static int capture_get_target_monitor(void) { return g_target_monitor; }

static void capture_get_size(int *w, int *h) {
    *w = g_width;
    *h = g_height;
}

// cap_copy_upright copies the duplicated texture (srcW x srcH, unrotated, rows
// `pitch` bytes apart) into dst (dstW x dstH BGRA, the desktop orientation the
// input mapping uses), turning a rotated output upright — the same mapping as
// the mosaic's scale_into_tile at scale 1. Neither buffer is ever read or
// written out of bounds: a texture that is not the size the output reports
// (mode changing under the duplication) is clamped, and the part of dst it
// does not cover is cleared.
static void cap_copy_upright(unsigned char *dst, int dstW, int dstH,
                             unsigned char *src, int srcW, int srcH, int pitch, int rot) {
    if (!dst || !src || dstW <= 0 || dstH <= 0 || srcW <= 0 || srcH <= 0 || pitch < 4) return;
    if (rot != 2 && rot != 3 && rot != 4) {
        int cw = dstW < srcW ? dstW : srcW;
        int ch = dstH < srcH ? dstH : srcH;
        size_t row = (size_t)cw * 4;
        if (row > (size_t)pitch) row = (size_t)pitch;
        for (int y = 0; y < ch; y++) {
            unsigned char *d = dst + (size_t)y * dstW * 4;
            memcpy(d, src + (size_t)y * pitch, row);
            if (row < (size_t)dstW * 4) memset(d + row, 0, (size_t)dstW * 4 - row);
        }
        for (int y = ch; y < dstH; y++) memset(dst + (size_t)y * dstW * 4, 0, (size_t)dstW * 4);
        return;
    }
    int dispW = (rot == 3) ? srcW : srcH; // upright size of the texture
    int dispH = (rot == 3) ? srcH : srcW;
    if (dispW != dstW || dispH != dstH) memset(dst, 0, (size_t)dstW * dstH * 4);
    int cols = srcW;
    if ((size_t)cols * 4 > (size_t)pitch) cols = pitch / 4;
    // Walk the texture rows (sequential reads), scatter into the upright frame.
    for (int ty = 0; ty < srcH; ty++) {
        const unsigned char *s = src + (size_t)ty * pitch;
        for (int tx = 0; tx < cols; tx++) {
            int px, py;
            if (rot == 2)      { px = srcH - 1 - ty; py = tx; }               // 90
            else if (rot == 3) { px = srcW - 1 - tx; py = srcH - 1 - ty; }    // 180
            else               { px = ty;            py = srcW - 1 - tx; }    // 270
            if (px < 0 || px >= dstW || py < 0 || py >= dstH) continue;
            memcpy(dst + ((size_t)py * dstW + px) * 4, s + (size_t)tx * 4, 4);
        }
    }
}

// GDI pacing. The GDI path has no "wait for a new frame" (DXGI's
// AcquireNextFrame paces the DXGI path): without a pause the capture thread
// would BitBlt the whole screen back to back, one CPU core at 100 % - on
// Windows 7 / 2008 R2 (no Desktop Duplication: GDI is the capture) a
// full-screen BitBlt with Aero on takes tens of ms. A frame then takes at
// least OR_GDI_MIN_FRAME_MS (~30 fps, the stream's own rate), and a slow
// copy is followed by an idle half of its cost (the thread is busy at most
// ~2/3 of the time), never more than OR_GDI_MAX_IDLE_MS.
#define OR_GDI_MIN_FRAME_MS 33
#define OR_GDI_MAX_IDLE_MS  250
static DWORD cap_gdi_idle_ms(DWORD cost_ms) {
    DWORD idle = cost_ms / 2;
    if (cost_ms + idle < OR_GDI_MIN_FRAME_MS) idle = OR_GDI_MIN_FRAME_MS - cost_ms;
    if (idle > OR_GDI_MAX_IDLE_MS) idle = OR_GDI_MAX_IDLE_MS;
    return idle;
}

// Consecutive frames whose texture is not the size the output reports before
// the capture is re-initialised (a mode change DXGI did not signal with
// ACCESS_LOST). Frames in between are copied clamped, never out of bounds.
#define OR_TEX_MISMATCH_REINIT 15

// capture_frame: fills out_bgra (pre-allocated w*h*4 bytes) with BGRA pixel data.
// Returns:  0 = frame captured,  1 = no new frame (DXGI timeout),  -1 = fatal error
// (re-initialise), -2 = the texture keeps another size than the output reports
// (re-initialise at the current geometry).
// GDI path always returns 0 (no "wait for new frame" concept).
//
// g_gdi_fail_count / g_gdi_last_err: GDI failures (BitBlt / GetDIBits) since
// the process started and the last GetLastError, read by the helper's
// "no picture" diagnostic (cap_gdi_failures): an RDP session whose client
// window is minimised, or a disconnected one, draws nothing and the GDI copy
// fails there.
static volatile LONG g_gdi_fail_count = 0;
static volatile LONG g_gdi_last_err   = 0;
static void cap_note_gdi_failure(void) {
    InterlockedExchange(&g_gdi_last_err, (LONG)GetLastError());
    InterlockedIncrement(&g_gdi_fail_count);
}
static void cap_gdi_failures(long *count, unsigned long *last_err) {
    *count = (long)InterlockedCompareExchange(&g_gdi_fail_count, 0, 0);
    *last_err = (unsigned long)InterlockedCompareExchange(&g_gdi_last_err, 0, 0);
}
static int capture_frame(unsigned char *out_bgra) {
    if (g_use_gdi) {
        if (!g_hdcMem || !g_hBitmap || g_width <= 0 || g_height <= 0) return -1;
        // GDI BitBlt — works in RDP sessions (CPU capture, no hardware acceleration)
        if (!BitBlt(g_hdcMem, 0, 0, g_width, g_height, g_hdcScreen, g_mon_x, g_mon_y, SRCCOPY | CAPTUREBLT)) {
            cap_note_gdi_failure();
            return -1;
        }

        BITMAPINFO bmi;
        ZeroMemory(&bmi, sizeof(bmi));
        bmi.bmiHeader.biSize        = sizeof(BITMAPINFOHEADER);
        bmi.bmiHeader.biWidth       = g_width;
        bmi.bmiHeader.biHeight      = -g_height; // negative = top-down (matches DXGI convention)
        bmi.bmiHeader.biPlanes      = 1;
        bmi.bmiHeader.biBitCount    = 32;        // BGRX — alpha ignored by encoder
        bmi.bmiHeader.biCompression = BI_RGB;

        int lines = GetDIBits(g_hdcMem, g_hBitmap, 0, (UINT)g_height, out_bgra, &bmi, DIB_RGB_COLORS);
        if (lines <= 0) {
            cap_note_gdi_failure();
            return -1;
        }
        overlay_cursor_gdi(out_bgra, g_width, g_height, g_mon_x, g_mon_y);
        return 0;
    }

    // ── DXGI path ────────────────────────────────────────────────────────────
    if (!g_dup || !g_ctx || !g_device || g_width <= 0 || g_height <= 0) return -1;
    DXGI_OUTDUPL_FRAME_INFO info;
    IDXGIResource *res = NULL;

    HRESULT hr = IDXGIOutputDuplication_AcquireNextFrame(g_dup, 33, &info, &res); // 33ms timeout
    if (hr == DXGI_ERROR_WAIT_TIMEOUT) return 1;
    if (hr == DXGI_ERROR_ACCESS_LOST) return -1;
    if (FAILED(hr)) return -1;

    ID3D11Texture2D *tex = NULL;
    hr = IDXGIResource_QueryInterface(res, &IID_ID3D11Texture2D, (void**)&tex);
    IUnknown_Release(res);
    if (FAILED(hr)) {
        IDXGIOutputDuplication_ReleaseFrame(g_dup);
        return -1;
    }

    // The texture is authoritative for the copy: the staging copy follows its
    // size (a staging texture of another size would make CopyResource a no-op
    // and the copy below read garbage or out of bounds).
    D3D11_TEXTURE2D_DESC td;
    ID3D11Texture2D_GetDesc(tex, &td);
    int texW = (int)td.Width, texH = (int)td.Height;
    if (g_staging && (g_staging_w != texW || g_staging_h != texH)) {
        IUnknown_Release(g_staging);
        g_staging = NULL;
    }
    if (!g_staging) {
        td.Usage          = D3D11_USAGE_STAGING;
        td.BindFlags      = 0;
        td.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
        td.MiscFlags      = 0;
        td.MipLevels      = 1;
        td.ArraySize      = 1;
        if (FAILED(ID3D11Device_CreateTexture2D(g_device, &td, NULL, &g_staging))) g_staging = NULL;
        if (g_staging) { g_staging_w = texW; g_staging_h = texH; }
    }
    if (!g_staging) {
        IUnknown_Release(tex);
        IDXGIOutputDuplication_ReleaseFrame(g_dup);
        return -1;
    }

    ID3D11DeviceContext_CopyResource(g_ctx, (ID3D11Resource*)g_staging, (ID3D11Resource*)tex);
    IUnknown_Release(tex);
    IDXGIOutputDuplication_ReleaseFrame(g_dup);

    D3D11_MAPPED_SUBRESOURCE mapped;
    hr = ID3D11DeviceContext_Map(g_ctx, (ID3D11Resource*)g_staging, 0, D3D11_MAP_READ, 0, &mapped);
    if (FAILED(hr)) return -1;

    // g_width x g_height is the output's desktop rectangle (rotated for a
    // portrait output); the texture is that rectangle unrotated.
    int rot = g_rotation;
    int expW = (rot == 2 || rot == 4) ? g_height : g_width;
    int expH = (rot == 2 || rot == 4) ? g_width : g_height;
    if (texW != expW || texH != expH) g_tex_mismatch++;
    else { g_tex_mismatch = 0; g_tex_mismatch_reinits = 0; }
    cap_copy_upright(out_bgra, g_width, g_height, (unsigned char*)mapped.pData,
                     texW, texH, (int)mapped.RowPitch, rot);

    ID3D11DeviceContext_Unmap(g_ctx, (ID3D11Resource*)g_staging, 0);

    // Overlay the mouse cursor onto the captured frame
    overlay_cursor_gdi(out_bgra, g_width, g_height, g_mon_x, g_mon_y);

    // A few re-inits at most: a driver whose texture never matches its
    // desktop rectangle keeps streaming (clamped) instead of stuttering.
    if (g_tex_mismatch >= OR_TEX_MISMATCH_REINIT && g_tex_mismatch_reinits < 3) {
        g_tex_mismatch = 0;
        g_tex_mismatch_reinits++;
        return -2;
    }
    return 0;
}

// capture_close: releases all resources (DXGI or GDI).
static void capture_close(void) {
    if (g_use_gdi) {
        gdi_close();
    } else {
        dxgi_close();
    }
    g_width = 0;
    g_height = 0;
}
static int capture_is_gdi(void) { return g_use_gdi; }

// ── Native capture thread ────────────────────────────────────────────────────
//
// Historically capture_frame ran on the Go goroutine that called captureFrame
// via CGo. That goroutine's OS thread is polluted with COM apartments, D3D11
// device notification windows, Go scheduler signal state and the watermark /
// toast HWNDs — any one of which makes SetThreadDesktop(Winlogon) return
// ERROR_BUSY (170). Without the desktop switch, DuplicateOutput returns
// E_ACCESSDENIED from a non-visible desktop, so DXGI stays dead while the
// Secure Desktop (UAC prompt, CAD security menu) is up — exactly the bug
// reported in 1.0.182.
//
// RustDesk solves this by keeping capture in a dedicated std::thread in their
// --server process; those threads don't own any of that state. We replicate
// that by spawning a real native Win32 thread via CreateThread. The Go side
// becomes a consumer that reads the latest frame from a mutex-protected
// shared buffer.

static volatile LONG   g_cap_running     = 0;  // 0=stopped, 1=run requested
static volatile LONG   g_cap_restart     = 0;  // request re-init (monitor change)
static volatile int    g_cap_want_mon    = 0;  // monitor index for restart
static volatile int    g_cap_init_done   = 0;  // init complete (ok or fail)
static volatile int    g_cap_init_ok     = 0;  // last init succeeded
static HANDLE          g_cap_thread_h    = NULL;
static CRITICAL_SECTION g_cap_frame_cs;
static int             g_cap_cs_ready    = 0;
// Double buffer: the native capture thread writes into g_cap_write_buf (no
// lock — nobody else touches it), then on successful frame swaps pointers
// under g_cap_frame_cs so Go's cap_fetch reads a coherent buffer. Lock is
// held ONLY for the pointer swap (nanoseconds) in the thread, and for the
// memcpy during fetch (5-10ms for a 14MB 2552×1348 BGRA frame). The
// previous design in 1.0.184 held the lock during the entire capture_frame
// (30ms+) which starved Go's fetch and produced the 10-second latency
// build-up the user saw.
static unsigned char  *g_cap_buf_a       = NULL;
static unsigned char  *g_cap_buf_b       = NULL;
static unsigned char  *g_cap_write_buf   = NULL; // back buffer — thread writes here
static unsigned char  *g_cap_read_buf    = NULL; // front buffer — latest complete frame
static int             g_cap_shared_w    = 0;
static int             g_cap_shared_h    = 0;
static int             g_cap_shared_ox   = 0;
static int             g_cap_shared_oy   = 0;
static volatile LONG   g_cap_frame_seq   = 0;
static volatile LONG   g_cap_last_read   = 0;
// Geometry of the shared frame (size AND desktop origin): bumped whenever it
// changes, so the Go side re-publishes the view (new init to the viewer, input
// mapping) — see captureSingleStale.
static volatile LONG   g_cap_geom_seq    = 0;
static volatile LONG   g_cap_reinit_req  = 0;  // Go's geometry probe asks for a re-init
static volatile LONG   g_cap_recovering  = 0;  // the thread is re-initialising the capture
// GDI mode (DXGI failed): the thread keeps trying to get DXGI back — quickly
// first (a mode change settles within a second or two), then every 2 s, like
// the mosaic's per-tile retries. GDI is never sticky.
#define OR_UPGRADE_MIN_MS 250
#define OR_UPGRADE_MAX_MS 2000
// ...unless DXGI cannot work here for now — the adapter does not implement
// Desktop Duplication (DXGI_ERROR_UNSUPPORTED), or DXGI sees no output for the
// target (an RDP / Hyper-V basic-video session) — or one try was slow: each try
// creates a D3D device and blocks this thread (no frame meanwhile), so it is
// then only retried every OR_UPGRADE_IDLE_MS (a later display change resets the
// schedule to OR_UPGRADE_MIN_MS).
#define OR_UPGRADE_IDLE_MS 30000
#define OR_UPGRADE_SLOW_MS 1500
// cap_ms_reached: GetTickCount() deadline test that survives the 49.7-day wrap
// (GetTickCount64 is not declared for the toolchain's default _WIN32_WINNT).
static int cap_ms_reached(DWORD now, DWORD deadline) { return (LONG)(now - deadline) >= 0; }
static DWORD           g_gdi_next_upgrade  = 0; // GetTickCount() deadline (wrap-safe: cap_ms_reached)
static DWORD           g_gdi_upgrade_delay = 0;
// After ACCESS_LOST (display mode change, desktop switch) DuplicateOutput can
// fail for a moment while the driver settles: DXGI is retried this long on the
// same output before falling back to GDI.
#define OR_DXGI_GRACE_MS 1000
#define OR_DXGI_RETRY_MS 150
// Desktop this thread was last attached to by cap_thread_follow_input_desktop
// (the handle was opened there; the previous one is closed once the thread
// left it, so repeated recoveries do not leak desktop handles).
static HDESK           g_cap_desk        = NULL;

// Helper: try to attach this thread to whichever desktop is currently
// receiving input. Called from capture_thread_proc on DXGI_ERROR_ACCESS_LOST
// so the duplication can be recreated on the Secure Desktop / Winlogon
// during CAD / UAC transitions. Must be called from the native capture
// thread — THAT thread has no windows so SetThreadDesktop succeeds (unlike
// Go runtime threads where it fails with ERROR_BUSY 170).
static void cap_thread_follow_input_desktop(void) {
    HDESK d = OpenInputDesktop(0, FALSE, GENERIC_ALL);
    if (!d) {
        d = OpenInputDesktop(0, FALSE,
            DESKTOP_READOBJECTS | DESKTOP_WRITEOBJECTS | DESKTOP_SWITCHDESKTOP |
            DESKTOP_CREATEWINDOW | DESKTOP_CREATEMENU | DESKTOP_HOOKCONTROL |
            DESKTOP_JOURNALRECORD | DESKTOP_JOURNALPLAYBACK | DESKTOP_ENUMERATE);
    }
    if (!d) return;
    if (SetThreadDesktop(d)) {
        // The previous handle is no longer this thread's desktop: closing it
        // is safe (CloseDesktop refuses, harmlessly, a desktop still in use).
        if (g_cap_desk && g_cap_desk != d) CloseDesktop(g_cap_desk);
        g_cap_desk = d;
    } else {
        CloseDesktop(d); // never attached: nothing uses this handle
    }
}

// Resize the double-buffer pair to match current capture dimensions. Called
// after every (re)init. Must be called with g_cap_frame_cs held because it
// reassigns the read/write pointer pair atomically. The origin is ALWAYS
// updated (a monitor that moved in the layout without changing size — its
// neighbour changed resolution — moves the input mapping too) and the
// geometry sequence bumps on any change of size or origin.
static void cap_resize_shared_locked(void) {
    int need = g_width * g_height * 4;
    if (need <= 0) return;
    int sizeChanged = !(g_cap_shared_w == g_width && g_cap_shared_h == g_height && g_cap_buf_a);
    int originChanged = (g_cap_shared_ox != g_mon_x || g_cap_shared_oy != g_mon_y);
    if (sizeChanged) {
        if (g_cap_buf_a) { free(g_cap_buf_a); g_cap_buf_a = NULL; }
        if (g_cap_buf_b) { free(g_cap_buf_b); g_cap_buf_b = NULL; }
        g_cap_buf_a = (unsigned char*)calloc(1, need);
        g_cap_buf_b = (unsigned char*)calloc(1, need);
        if (!g_cap_buf_a || !g_cap_buf_b) {
            if (g_cap_buf_a) { free(g_cap_buf_a); g_cap_buf_a = NULL; }
            if (g_cap_buf_b) { free(g_cap_buf_b); g_cap_buf_b = NULL; }
            g_cap_write_buf = g_cap_read_buf = NULL;
            g_cap_shared_w = g_cap_shared_h = 0;
            InterlockedIncrement(&g_cap_geom_seq);
            return;
        }
        g_cap_write_buf = g_cap_buf_a;
        g_cap_read_buf  = g_cap_buf_b;
        g_cap_shared_w  = g_width;
        g_cap_shared_h  = g_height;
    }
    g_cap_shared_ox = g_mon_x;
    g_cap_shared_oy = g_mon_y;
    if (sizeChanged || originChanged) InterlockedIncrement(&g_cap_geom_seq);
}

static void cap_publish_geometry(void) {
    EnterCriticalSection(&g_cap_frame_cs);
    cap_resize_shared_locked();
    LeaveCriticalSection(&g_cap_frame_cs);
}

// cap_input_desktop_moved: 1 when the desktop receiving input is not the one
// this thread is attached to (GDI mode: a lock / UAC / sign-in transition GDI
// did not notice — BitBlt keeps copying the old desktop, black or frozen).
static int cap_input_desktop_moved(void) {
    wchar_t a[64], b[64];
    DWORD n = 0;
    ZeroMemory(a, sizeof(a)); ZeroMemory(b, sizeof(b));
    HDESK in = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS);
    if (!in) return 0;
    BOOL okA = GetUserObjectInformationW(in, UOI_NAME, a, sizeof(a) - sizeof(wchar_t), &n);
    CloseDesktop(in);
    HDESK cur = GetThreadDesktop(GetCurrentThreadId()); // not to be closed
    BOOL okB = cur ? GetUserObjectInformationW(cur, UOI_NAME, b, sizeof(b) - sizeof(wchar_t), &n) : FALSE;
    if (!okA || !okB) return 0;
    return _wcsicmp(a, b) != 0;
}

// cap_gdi_next_upgrade_delay: the wait before the next GDI → DXGI try, after a
// failed one that waited `prev`: doubling from OR_UPGRADE_MIN_MS up to
// OR_UPGRADE_MAX_MS, or OR_UPGRADE_IDLE_MS when DXGI cannot work here for now
// (hopeless) or the try was slow.
static DWORD cap_gdi_next_upgrade_delay(DWORD prev, int hopeless, int slow) {
    if (hopeless || slow) return OR_UPGRADE_IDLE_MS;
    DWORD next = prev * 2;
    if (next < OR_UPGRADE_MIN_MS) next = OR_UPGRADE_MIN_MS;
    if (next > OR_UPGRADE_MAX_MS) next = OR_UPGRADE_MAX_MS;
    return next;
}

// cap_gdi_schedule_upgrade: (re)start the DXGI retries of GDI mode.
static void cap_gdi_schedule_upgrade(void) {
    g_gdi_upgrade_delay = OR_UPGRADE_MIN_MS;
    g_gdi_next_upgrade = GetTickCount() + g_gdi_upgrade_delay;
}

// cap_gdi_try_upgrade: GDI mode — try DXGI again on the same output (any
// output when GDI copies the whole virtual screen). On success the GDI copy is
// released and the DXGI geometry published (a new init only if it differs);
// otherwise the GDI state is left exactly as it was and the next try is
// scheduled with a backoff.
// cap_dxgi_fail_hopeless: the last dxgi_init failure will not heal by itself
// soon (no Desktop Duplication on the adapter, or no DXGI output for the index).
//
// cap_dxgi_fail_permanent: the failure can never heal on this machine:
// IDXGIOutput1 missing (-5: Windows 7 / 2008 R2, DXGI 1.1 without the
// Platform Update), Desktop Duplication impossible on this Windows or
// d3d11.dll missing (-7), or the adapter does not implement it (-6 with
// DXGI_ERROR_UNSUPPORTED). Hopeless also covers -4 (no DXGI output for the
// index: an RDP / Hyper-V basic-video session), which may change later.
static int cap_dxgi_fail_permanent_for(int step, unsigned int hr) {
    return step == -5 || step == -7 ||
        (step == -6 && hr == OR_DXGI_ERROR_UNSUPPORTED);
}
static int cap_dxgi_fail_permanent(void) {
    return cap_dxgi_fail_permanent_for(g_dxgi_last_fail, g_dxgi_last_hr);
}
static int cap_dxgi_fail_hopeless_for(int step, unsigned int hr) {
    return step == -4 || cap_dxgi_fail_permanent_for(step, hr);
}
static int cap_dxgi_fail_hopeless(void) {
    return cap_dxgi_fail_hopeless_for(g_dxgi_last_fail, g_dxgi_last_hr);
}

static void cap_gdi_try_upgrade(void) {
    if (cap_dxgi_is_disabled()) {
        // Windows < 8: DXGI never comes back, nothing to try.
        g_gdi_upgrade_delay = OR_UPGRADE_IDLE_MS;
        g_gdi_next_upgrade = GetTickCount() + OR_UPGRADE_IDLE_MS;
        return;
    }
    int sw = g_width, sh = g_height, sx = g_mon_x, sy = g_mon_y, st = g_target_monitor;
    DWORD t0 = GetTickCount();
    int ok = 0, hopeless = 1;
    if (g_gdi_virtual) {
        MonitorInfoC mons[OR_MAX_MONITORS];
        int n = enumerate_monitors(mons, OR_MAX_MONITORS);
        for (int i = 0; i < n && !ok; i++) {
            if (dxgi_init(i) == 0) { g_target_monitor = i; ok = 1; }
            else if (!cap_dxgi_fail_hopeless()) hopeless = 0;
        }
    } else {
        ok = (dxgi_init(g_target_monitor) == 0);
        if (!ok && !cap_dxgi_fail_hopeless()) hopeless = 0;
    }
    if (ok) {
        int dw = g_width, dh = g_height, dx = g_mon_x, dy = g_mon_y;
        gdi_close(); // clears g_use_gdi: capture_frame takes the DXGI path from now on
        g_width = dw; g_height = dh; g_mon_x = dx; g_mon_y = dy;
        cap_publish_geometry();
        return;
    }
    // dxgi_init may have overwritten the GDI geometry before failing.
    g_width = sw; g_height = sh; g_mon_x = sx; g_mon_y = sy; g_target_monitor = st;
    DWORD next = cap_gdi_next_upgrade_delay(g_gdi_upgrade_delay, hopeless,
        GetTickCount() - t0 > OR_UPGRADE_SLOW_MS);
    g_gdi_upgrade_delay = next;
    g_gdi_next_upgrade = GetTickCount() + next;
}

// cap_dxgi_grace: DXGI on output idx, retried for up to OR_DXGI_GRACE_MS.
// Returns 0 on success, -1 otherwise (at once when the adapter does not do
// Desktop Duplication at all, or when the Go side restarts / stops the thread).
static int cap_dxgi_grace(int idx) {
    DWORD deadline = GetTickCount() + OR_DXGI_GRACE_MS;
    for (;;) {
        if (dxgi_init(idx) == 0) { g_target_monitor = idx; return 0; }
        if (cap_dxgi_fail_permanent()) return -1;
        if (!g_cap_running || g_cap_restart) return -1;
        if (cap_ms_reached(GetTickCount(), deadline)) return -1;
        Sleep(OR_DXGI_RETRY_MS);
    }
}

// cap_recover: the capture died (ACCESS_LOST: display mode change, desktop
// switch — or a geometry probe asked for it). Follow the input desktop, then:
//   1. DXGI on the same output, with a short grace;
//   2. GDI on the same output while it still exists;
//   3. the best output (DXGI on any output, else GDI on the virtual screen).
// The new geometry is published (the Go side sends the viewer a new init when
// it changed). Returns 0 on success, negative when nothing could be opened.
static int cap_recover(void) {
    InterlockedExchange(&g_cap_recovering, 1);
    capture_close();
    // This thread has no windows so SetThreadDesktop actually works.
    cap_thread_follow_input_desktop();
    int rc = cap_dxgi_grace(g_target_monitor);
    if (rc != 0 && g_cap_running && !g_cap_restart) {
        MonitorInfoC mons[OR_MAX_MONITORS];
        int n = enumerate_monitors(mons, OR_MAX_MONITORS);
        int t = g_target_monitor;
        if (t >= 0 && t < n && mons[t].w > 0 && mons[t].h > 0)
            rc = gdi_init(mons[t].x, mons[t].y, mons[t].w, mons[t].h);
        if (rc != 0) rc = capture_init_best();
    }
    if (rc == 0) {
        if (g_use_gdi) cap_gdi_schedule_upgrade();
        cap_publish_geometry();
        InterlockedExchange(&g_cap_reinit_req, 0);
    }
    InterlockedExchange(&g_cap_recovering, 0);
    return rc;
}

static DWORD WINAPI capture_thread_proc(LPVOID arg) {
    (void)arg;
    while (g_cap_running) {
        if (g_cap_restart) {
            g_cap_restart = 0;
            capture_close();
            // g_cap_want_mon == -1 means "pick best available output" (initial
            // startup behaviour matching pre-1.0.184 captureInit). Any value
            // >=0 is an explicit monitor index requested by the operator via
            // the monitor-switch UI — previously we ignored that and always
            // called capture_init_best(), which on a multi-monitor target
            // silently reverted to the primary output whenever the viewer
            // picked a secondary. Matches the old capture_init_monitor() path
            // for explicit switches.
            int rc;
            if (g_cap_want_mon < 0) {
                rc = capture_init_best();
            } else {
                rc = capture_init_monitor(g_cap_want_mon);
            }
            if (rc < 0) {
                g_cap_init_ok = 0;
            } else {
                if (g_use_gdi) cap_gdi_schedule_upgrade();
                cap_publish_geometry();
                InterlockedExchange(&g_cap_reinit_req, 0);
                g_cap_init_ok = 1;
            }
            g_cap_init_done = 1;
            continue;
        }

        if (!g_cap_init_ok) { Sleep(20); continue; }

        // The Go side's geometry probe saw the display change without the
        // capture noticing (GDI never gets ACCESS_LOST): re-init now.
        if (g_cap_reinit_req) {
            if (cap_recover() < 0 && !g_cap_restart) Sleep(250);
            continue;
        }
        // GDI mode: try to get DXGI back (backoff 250 ms → 2 s) — first
        // following the input desktop if it moved (GDI gets no ACCESS_LOST).
        if (g_use_gdi && cap_ms_reached(GetTickCount(), g_gdi_next_upgrade)) {
            if (cap_input_desktop_moved()) {
                if (cap_recover() < 0 && !g_cap_restart) Sleep(250);
                continue;
            }
            cap_gdi_try_upgrade();
        }

        // Capture into the BACK buffer — no lock needed, this thread is the
        // only writer to g_cap_write_buf. capture_frame's internal 33ms DXGI
        // timeout paces this naturally at ~30 Hz.
        unsigned char *write_target = g_cap_write_buf;
        int was_gdi = g_use_gdi;
        DWORD t_frame = GetTickCount();
        int ret = (write_target != NULL) ? capture_frame(write_target) : -1;

        if (ret == 1) { continue; }   // DXGI_ERROR_WAIT_TIMEOUT, no new frame
        if (ret < 0) {
            // DXGI_ERROR_ACCESS_LOST — display mode change (resolution,
            // orientation, a monitor added / removed) or input desktop
            // transition (UAC, Secure Desktop / CAD menu, workstation lock).
            // Follow the input desktop, re-init there at the current
            // geometry, continue capturing.
            if (cap_recover() < 0 && !g_cap_restart) {
                // Can't re-init right now (no rights, or adapter busy).
                // Brief back-off; next tick will retry.
                Sleep(250);
            } else if (was_gdi && !g_cap_restart) {
                // The GDI copy failed and was re-opened: one that keeps
                // failing (an RDP session whose client window is minimised
                // draws nothing) must not spin this thread re-opening it.
                Sleep(100);
            }
            continue;
        }

        // Frame captured. Flip the read/write buffers — lock held only for
        // the pointer swap so fetchers aren't blocked by the capture cost.
        EnterCriticalSection(&g_cap_frame_cs);
        unsigned char *tmp  = g_cap_read_buf;
        g_cap_read_buf      = g_cap_write_buf;
        g_cap_write_buf     = tmp;
        LeaveCriticalSection(&g_cap_frame_cs);
        InterlockedIncrement(&g_cap_frame_seq);

        // GDI: paced (cap_gdi_idle_ms). A restart / stop request is
        // honoured at the next loop turn.
        if (was_gdi) Sleep(cap_gdi_idle_ms(GetTickCount() - t_frame));
    }
    return 0;
}

// Start the native capture thread. Blocks up to wait_ms for the initial DXGI
// init to complete so captureInit() semantics match the prior synchronous
// path. Returns 0 on success.
static int cap_start_wait(int monitor_idx, int wait_ms) {
    if (g_cap_thread_h) return 0;
    if (!g_cap_cs_ready) {
        InitializeCriticalSection(&g_cap_frame_cs);
        g_cap_cs_ready = 1;
    }
    g_cap_want_mon  = monitor_idx;
    g_cap_restart   = 1;
    g_cap_init_done = 0;
    g_cap_init_ok   = 0;
    InterlockedExchange(&g_cap_reinit_req, 0);
    InterlockedExchange(&g_cap_running, 1);
    g_cap_thread_h = CreateThread(NULL, 0, capture_thread_proc, NULL, 0, NULL);
    if (!g_cap_thread_h) { InterlockedExchange(&g_cap_running, 0); return -1; }
    for (int i = 0; i < wait_ms / 10 && !g_cap_init_done; i++) Sleep(10);
    return g_cap_init_ok ? 0 : -2;
}

static int cap_start(int monitor_idx) { return cap_start_wait(monitor_idx, 5000); }

static int cap_request_monitor(int monitor_idx) {
    g_cap_want_mon  = monitor_idx;
    g_cap_init_done = 0;
    g_cap_restart   = 1;
    for (int i = 0; i < 500 && !g_cap_init_done; i++) Sleep(10);
    return g_cap_init_ok ? 0 : -2;
}

static void cap_stop(void) {
    InterlockedExchange(&g_cap_running, 0);
    if (g_cap_thread_h) {
        WaitForSingleObject(g_cap_thread_h, 2000);
        CloseHandle(g_cap_thread_h);
        g_cap_thread_h = NULL;
    }
    capture_close();
    if (g_cap_cs_ready) {
        EnterCriticalSection(&g_cap_frame_cs);
        if (g_cap_buf_a) { free(g_cap_buf_a); g_cap_buf_a = NULL; }
        if (g_cap_buf_b) { free(g_cap_buf_b); g_cap_buf_b = NULL; }
        g_cap_write_buf = NULL;
        g_cap_read_buf  = NULL;
        g_cap_shared_w = g_cap_shared_h = 0;
        LeaveCriticalSection(&g_cap_frame_cs);
    }
}

// Read the latest frame from the shared read buffer. Non-blocking: if no
// new frame since the last call, returns 1 immediately so the Go main loop
// can skip encoding this tick and stay responsive. The frame-ticker in
// runHelperMode already provides the 30 Hz cadence — a WaitForSingleObject
// in here (1.0.184) just added up-to-50ms of extra latency per tick with
// nothing gained.
//
// Returns 0 (frame copied, *out_w/*out_h = its size), 1 (no new frame), -1
// (no frame buffer), -3 (the frame is larger than out_bgra: the display mode
// changed; *out_w/*out_h = the new size, the frame stays unread).
static int cap_fetch(unsigned char *out_bgra, int out_size, int *out_w, int *out_h) {
    if (!g_cap_cs_ready) return -1;
    LONG cur = InterlockedCompareExchange(&g_cap_frame_seq, 0, 0);
    if (cur == g_cap_last_read) return 1;
    EnterCriticalSection(&g_cap_frame_cs);
    int need = g_cap_shared_w * g_cap_shared_h * 4;
    if (!g_cap_read_buf || need <= 0) {
        LeaveCriticalSection(&g_cap_frame_cs);
        return -1;
    }
    if (out_size < need) {
        *out_w = g_cap_shared_w;
        *out_h = g_cap_shared_h;
        LeaveCriticalSection(&g_cap_frame_cs);
        return -3;
    }
    memcpy(out_bgra, g_cap_read_buf, need);
    *out_w = g_cap_shared_w;
    *out_h = g_cap_shared_h;
    g_cap_last_read = cur;
    LeaveCriticalSection(&g_cap_frame_cs);
    return 0;
}

static void cap_get_shared_dims(int *w, int *h, int *ox, int *oy) {
    if (!g_cap_cs_ready) { *w=0; *h=0; *ox=0; *oy=0; return; }
    EnterCriticalSection(&g_cap_frame_cs);
    *w = g_cap_shared_w; *h = g_cap_shared_h;
    *ox = g_cap_shared_ox; *oy = g_cap_shared_oy;
    LeaveCriticalSection(&g_cap_frame_cs);
}

// cap_get_shared_geom: size, origin and geometry sequence read together.
static void cap_get_shared_geom(int *w, int *h, int *ox, int *oy, long *seq) {
    if (!g_cap_cs_ready) { *w=0; *h=0; *ox=0; *oy=0; *seq=0; return; }
    EnterCriticalSection(&g_cap_frame_cs);
    *w = g_cap_shared_w; *h = g_cap_shared_h;
    *ox = g_cap_shared_ox; *oy = g_cap_shared_oy;
    *seq = (long)g_cap_geom_seq;
    LeaveCriticalSection(&g_cap_frame_cs);
}

static void cap_request_reinit(void) { InterlockedExchange(&g_cap_reinit_req, 1); }
// cap_force_gdi: DXGI is switched off for the rest of this process (as on
// Windows 7) and the native thread re-initialises — on GDI. Used once by the
// helper when a remote session's DXGI duplication gives no first frame.
static void cap_force_gdi(void) {
    cap_set_dxgi_disabled(1);
    InterlockedExchange(&g_cap_reinit_req, 1);
}
static int cap_is_recovering(void) {
    return (InterlockedCompareExchange(&g_cap_recovering, 0, 0) || InterlockedCompareExchange(&g_cap_reinit_req, 0, 0)) ? 1 : 0;
}
static int capture_is_gdi_virtual(void) { return g_use_gdi && g_gdi_virtual; }

static void cap_virtual_screen(int *x, int *y, int *w, int *h) {
    *x = GetSystemMetrics(SM_XVIRTUALSCREEN);
    *y = GetSystemMetrics(SM_YVIRTUALSCREEN);
    *w = GetSystemMetrics(SM_CXVIRTUALSCREEN);
    *h = GetSystemMetrics(SM_CYVIRTUALSCREEN);
}

// Test hooks (capture_windows_test.go) for the DXGI failure classification.
static int cap_test_hopeless(int step, unsigned int hr)  { return cap_dxgi_fail_hopeless_for(step, hr); }
static int cap_test_permanent(int step, unsigned int hr) { return cap_dxgi_fail_permanent_for(step, hr); }
static int cap_dxgi_permanent_now(void) { return cap_dxgi_fail_permanent(); }

// ── Test hooks (capture_windows_test.go): drive the shared-frame bookkeeping
// without a capture thread (no GPU needed). Never called by the agent.
static long cap_test_set_geometry(int w, int h, int ox, int oy) {
    if (!g_cap_cs_ready) {
        InitializeCriticalSection(&g_cap_frame_cs);
        g_cap_cs_ready = 1;
    }
    g_width = w; g_height = h; g_mon_x = ox; g_mon_y = oy;
    cap_publish_geometry();
    InterlockedIncrement(&g_cap_frame_seq); // as if a frame had been captured
    return (long)g_cap_geom_seq;
}
*/
import "C"
import (
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"
)

// MonitorInfo describes a connected display for the viewer's monitor selector
// and the mosaic layout. Primary/Rotation are filled on Windows; DPI/Scale are
// reserved (viewer treats them as optional).
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
	var cmons [16]C.MonitorInfoC
	n := int(C.enumerate_monitors(&cmons[0], C.int(16)))
	out := make([]MonitorInfo, n)
	for i := 0; i < n; i++ {
		out[i] = MonitorInfo{
			Index:    int(cmons[i].index),
			Name:     syscall.UTF16ToString((*[32]uint16)(unsafe.Pointer(&cmons[i].name[0]))[:]),
			X:        int(cmons[i].x),
			Y:        int(cmons[i].y),
			Width:    int(cmons[i].w),
			Height:   int(cmons[i].h),
			Primary:  cmons[i].primary != 0,
			Rotation: int(cmons[i].rotation),
		}
	}
	return out
}

func captureInitMonitor(idx int) error {
	// Explicit monitor switch. If the native thread isn't running yet, start
	// it targeting this specific monitor; otherwise tell the existing thread
	// to reinit on the new monitor. Both paths funnel into the thread's
	// restart handler which calls capture_init_monitor(idx) — never
	// capture_init_best(), which would silently bounce back to the primary
	// output whenever the operator picked a secondary.
	if int(C.cap_start(C.int(idx))) == 0 {
		captureActive = true
		if C.capture_is_gdi() != 0 {
			log.Printf("capture: monitor %d via GDI (native thread)", idx)
		} else {
			log.Printf("capture: monitor %d via DXGI (native thread)", idx)
		}
		return nil
	}
	if int(C.cap_request_monitor(C.int(idx))) == 0 {
		captureActive = true
		log.Printf("capture: switched to monitor %d (native thread reinit)", idx)
		return nil
	}
	return fmt.Errorf("capture init monitor %d failed", idx)
}

func captureMonitorOffset() (x, y int) {
	// The origin published with the shared frame (the native thread may be
	// re-initialising the capture globals right now).
	if captureActive && !captureMagActive && !captureMosaicActive {
		var w, h, ox, oy C.int
		C.cap_get_shared_dims(&w, &h, &ox, &oy)
		if w > 0 && h > 0 {
			return int(ox), int(oy)
		}
	}
	var cx, cy C.int
	C.capture_get_monitor_offset(&cx, &cy)
	return int(cx), int(cy)
}

// singleGeom is the geometry of the single-monitor capture: its desktop
// rectangle, the native thread's geometry sequence (0 for Magnification) and
// whether Magnification captures it.
type singleGeom struct {
	rect reachRect
	seq  int64
	mag  bool
}

// publishedSingle is the geometry the current single view (and the viewer's
// last init) was built from.
var publishedSingle singleGeom

func currentSingleGeom() singleGeom {
	if captureMagActive {
		x, y := magCaptureOrigin()
		return singleGeom{rect: reachRect{x, y, magCaptureWidth(), magCaptureHeight()}, mag: true}
	}
	var w, h, ox, oy C.int
	var seq C.long
	C.cap_get_shared_geom(&w, &h, &ox, &oy, &seq)
	return singleGeom{rect: reachRect{int(ox), int(oy), int(w), int(h)}, seq: int64(seq)}
}

// captureSingleStale reports whether the single-monitor capture moved (size,
// origin, or DXGI / GDI / Magnification) since its view was published: the
// helper then re-publishes the view and sends a new init. False while the
// capture is re-initialising (no geometry yet) and in mosaic view.
func captureSingleStale() bool {
	if captureMosaicActive || !captureActive {
		return false
	}
	cur := currentSingleGeom()
	if cur.rect.W <= 0 || cur.rect.H <= 0 {
		return false
	}
	return cur != publishedSingle
}

// Actions of the geometry watchdog for the helper loop.
const (
	watchNone      = iota
	watchRepublish // the capture source changed (Magnification re-initialised, or DXGI back): re-publish the view
	watchRestart   // the capture is gone: restart the single view from scratch (best monitor)
)

// captureWatchdog probes the display geometry for what no DXGI event reports:
// GDI and Magnification captures, the virtual screen SendInput normalises over
// (any monitor's mode change moves it), and a DXGI capture that missed a change.
// It also takes the Magnification fallback back to DXGI when that works again.
// Runs on the helper's capture thread (the Magnification windows live there).
type captureWatchdog struct {
	next    time.Time
	confirm geomConfirm
	lastAsk time.Time
	magUp   upgradeBackoff
}

func (cw *captureWatchdog) tick(now time.Time) int {
	if now.Before(cw.next) {
		return watchNone
	}
	fallback := captureMagActive || C.capture_is_gdi() != 0
	cw.next = now.Add(geomProbeInterval(fallback))

	// Input: SendInput normalises absolute positions over the virtual screen.
	if virtualScreenCacheStale() {
		refreshVirtualScreenCache()
		log.Printf("helper: virtual screen changed — input mapping refreshed")
	}
	if captureMosaicActive || !captureActive {
		cw.confirm.reset()
		return watchNone
	}

	if captureMagActive {
		// Magnification captures the whole virtual screen (magCaptureInit).
		var x, y, w, h C.int
		C.cap_virtual_screen(&x, &y, &w, &h)
		mx, my := magCaptureOrigin()
		cur := reachRect{mx, my, magCaptureWidth(), magCaptureHeight()}
		exp := reachRect{int(x), int(y), int(w), int(h)}
		if cw.confirm.observe(cur, exp) {
			log.Printf("helper: display changed under the Magnification capture (%dx%d at %d,%d → %dx%d at %d,%d) — re-initialising it",
				cur.W, cur.H, cur.X, cur.Y, exp.W, exp.H, exp.X, exp.Y)
			magCaptureClose()
			if err := magCaptureInit(); err != nil {
				captureMagActive = false
				log.Printf("helper: Magnification re-init failed: %v — restarting the capture", err)
				return watchRestart
			}
			cw.magUp.reset(now)
			return watchRepublish
		}
		if cw.magUp.due(now) {
			t0 := time.Now()
			if captureTryDXGIFromMag() {
				log.Printf("helper: DXGI works again — leaving the Magnification fallback (monitor %d)", captureTargetMonitor())
				return watchRepublish
			}
			cw.magUp.failed(now, time.Since(t0))
			if cw.magUp.disabled {
				log.Printf("helper: DXGI retry took %v — no further retries, staying on Magnification", time.Since(t0).Round(time.Millisecond))
			}
		}
		return watchNone
	}

	if C.cap_is_recovering() != 0 {
		cw.confirm.reset()
		return watchNone
	}
	cur, exp := nativeGeomRects()
	if cw.confirm.observe(cur, exp) && (cw.lastAsk.IsZero() || now.Sub(cw.lastAsk) >= geomReinitCooldown) {
		cw.lastAsk = now
		if exp.W <= 0 {
			log.Printf("helper: captured output %d is gone — re-initialising the capture", captureTargetMonitor())
		} else {
			log.Printf("helper: capture %dx%d at %d,%d but Windows reports %dx%d at %d,%d — re-initialising the capture",
				cur.W, cur.H, cur.X, cur.Y, exp.W, exp.H, exp.X, exp.Y)
		}
		C.cap_request_reinit()
	}
	return watchNone
}

// nativeGeomRects returns the rectangle the native capture publishes and the
// one Windows reports for it now (the captured output, or the virtual screen
// for GDI on the whole virtual screen). A zero expected rectangle: the output
// is gone.
func nativeGeomRects() (cur, exp reachRect) {
	var w, h, ox, oy C.int
	C.cap_get_shared_dims(&w, &h, &ox, &oy)
	cur = reachRect{int(ox), int(oy), int(w), int(h)}
	if C.capture_is_gdi_virtual() != 0 {
		var x, y, vw, vh C.int
		C.cap_virtual_screen(&x, &y, &vw, &vh)
		return cur, reachRect{int(x), int(y), int(vw), int(vh)}
	}
	idx := captureTargetMonitor()
	for _, m := range enumerateMonitors() {
		if m.Index == idx && m.Width > 0 && m.Height > 0 {
			return cur, reachRect{m.X, m.Y, m.Width, m.Height}
		}
	}
	return cur, reachRect{}
}

// captureTryDXGIFromMag: from the Magnification fallback, start the native
// DXGI capture on the best output (as captureInit does). On success the
// Magnification capture is closed; otherwise nothing changed.
func captureTryDXGIFromMag() bool {
	if !captureMagActive || !captureDXGIPossible() {
		return false
	}
	if int(C.cap_start_wait(C.int(-1), C.int(magUpgradeSlow/time.Millisecond))) == 0 && C.capture_is_gdi() == 0 {
		magCaptureClose()
		captureMagActive = false
		captureActive = true
		return true
	}
	C.cap_stop()
	return false
}

// captureTargetMonitor is the DXGI output index the single-monitor capture is
// currently on (enumerate order, matching MonitorInfo.Index).
func captureTargetMonitor() int { return int(C.capture_get_target_monitor()) }

// publishSingleView builds the one-tile view for the current single-monitor
// capture, registers it for input mapping (with a fresh gen), and returns it so
// the init message can carry it. When mosaic capture is active it publishes the
// mosaic view instead. Rotation/primary come from the monitor list.
func publishSingleView() (reachView, int) {
	if captureMosaicActive {
		gen := nextViewGen()
		setCurrentView(mosaicView, gen)
		return mosaicView, gen
	}
	// Size and origin read together (the native thread publishes them with
	// its geometry sequence). The Magnification fallback captures its own
	// rectangle (the whole virtual screen), not a DXGI output.
	g := currentSingleGeom()
	offX, offY, w, h := g.rect.X, g.rect.Y, g.rect.W, g.rect.H
	publishedSingle = g
	idx := captureTargetMonitor()
	rotation, primary := 0, false
	for _, m := range enumerateMonitors() {
		if m.Index == idx {
			rotation, primary = m.Rotation, m.Primary
			break
		}
	}
	v := singleTileView(idx, offX, offY, w, h, rotation, primary)
	gen := nextViewGen()
	setCurrentView(v, gen)
	return v, gen
}

var captureActive bool

// captureDXGIUnsupported: the last DXGI attempt failed because the adapter does
// not implement Desktop Duplication at all (no point retrying).
func captureDXGIUnsupported() bool {
	return C.cap_dxgi_permanent_now() != 0
}

// captureDXGIPossible: Desktop Duplication can exist on this Windows (8 /
// Server 2012 or later) and d3d11.dll / dxgi.dll load. False on Windows 7 /
// 2008 R2: the capture is GDI there (no Magnification detour, no DXGI retry).
func captureDXGIPossible() bool {
	return C.cap_dxgi_is_disabled() == 0 && C.cap_d3d11_dll_ok() != 0 && C.cap_dxgi_dll_ok() != 0
}

// Windows < 8 (os_version.go): DXGI Desktop Duplication is switched off in
// the native capture (dxgi_init fails at once, permanently) before any use.
func init() {
	if !WindowsVersion().DXGIDuplicationPossible() {
		C.cap_set_dxgi_disabled(1)
	}
}

func captureInit() error {
	// Up-front diagnostic dump: try each DXGI output independently and log
	// adapter + HRESULT for each. Makes it obvious which adapter succeeds
	// and which Windows is refusing (E_ACCESSDENIED, UNSUPPORTED, etc.).
	C.dxgi_diag_collect()
	n := int(C.diag_count())
	log.Printf("helper: DXGI dump — %d outputs visible to this process", n)
	for i := 0; i < n; i++ {
		e := C.diag_entry(C.int(i))
		adapter := syscall.UTF16ToString((*[64]uint16)(unsafe.Pointer(&e.adapter[0]))[:])
		device := syscall.UTF16ToString((*[32]uint16)(unsafe.Pointer(&e.device[0]))[:])
		if e.step == 0 {
			log.Printf("  [%d] %s / %s  %dx%d  DXGI ok", i, adapter, device, int(e.w), int(e.h))
		} else {
			log.Printf("  [%d] %s / %s  %dx%d  DXGI fail step=%d hr=0x%08x",
				i, adapter, device, int(e.w), int(e.h), int(e.step), uint32(e.hr))
		}
	}

	// Step 1: start the NATIVE capture thread in "best monitor" mode (-1
	// sentinel → the thread calls capture_init_best() which iterates every
	// DXGI output and picks the first that supports DuplicateOutput).
	// Explicit index >=0 is reserved for operator-initiated monitor switches
	// via captureInitMonitor(). Must not run DXGI init on a Go-runtime
	// thread: goroutine threads carry windows/hooks (watermark HWND, COM
	// apartments, scheduler signal state) that make SetThreadDesktop(Winlogon)
	// fail with ERROR_BUSY during CAD / UAC transitions. The native thread
	// is clean so it can actually follow the Secure Desktop when the input
	// desktop transitions.
	disp := ownSessionDisplay()
	captureSessionDisplay = disp
	log.Printf("helper: session %d display: %s", CurrentSessionID(), disp)

	startRC := int(C.cap_start(C.int(-1)))
	if startRC == 0 && C.capture_is_gdi() != 0 && skipMagnification(disp.remote, captureDXGIPossible()) {
		// Windows 7 / 2008 R2 (or d3d11 / dxgi unusable): no Desktop
		// Duplication can ever start here. GDI is the capture — it follows
		// the input desktop (sign-in, UAC) on the native thread — and the
		// Magnification detour (whose callback is not reliable there) is
		// skipped.
		// Remote (RDP) session where DXGI failed: same — GDI BitBlt is the
		// path that works there, the Magnification callback may never fire
		// (every frame then failed silently: no picture at all), and the
		// native thread keeps retrying DXGI from GDI.
		captureActive = true
		if !captureDXGIPossible() {
			log.Printf("helper: capture path = GDI (native thread): no DXGI Desktop Duplication on this Windows (%s)", WindowsVersion())
		} else {
			log.Printf("helper: capture path = GDI (native thread): remote session (%s), DXGI unavailable (step=%d hr=0x%08x) — Magnification skipped",
				disp, int(C.dxgi_last_fail()), uint32(C.dxgi_last_hr()))
		}
		return nil
	}
	if !(startRC == 0 && C.capture_is_gdi() == 0) && HelperDXGIWait > 0 {
		// A helper spawned by a session switch (--dxgi-wait): right after a
		// sign-in the Welcome screen can still own the input desktop and DXGI
		// refuses for a moment. Keep trying before settling for Magnification
		// / GDI, the weaker paths — unless the adapter does not
		// do Desktop Duplication at all.
		t0 := time.Now()
		for time.Since(t0) < HelperDXGIWait && !captureDXGIUnsupported() {
			C.cap_stop()
			time.Sleep(400 * time.Millisecond)
			startRC = int(C.cap_start(C.int(-1)))
			if startRC == 0 && C.capture_is_gdi() == 0 {
				log.Printf("helper: DXGI came up after %v (session switch)", time.Since(t0).Round(time.Millisecond))
				break
			}
		}
	}
	mons := enumerateMonitors()
	if startRC == 0 && C.capture_is_gdi() == 0 {
		captureActive = true
		picked := int(C.capture_get_target_monitor())
		name := ""
		if picked >= 0 && picked < len(mons) {
			name = fmt.Sprintf(" [%s %dx%d@%d,%d]",
				mons[picked].Name, mons[picked].Width, mons[picked].Height,
				mons[picked].X, mons[picked].Y)
		}
		log.Printf("helper: capture path = DXGI Desktop Duplication (monitor %d of %d, native thread)%s",
			picked, len(mons), name)
		return nil
	}

	// Step 2: DXGI failed to start (or fell through to GDI). Stop the native
	// thread, try Magnification API fallback.
	C.cap_stop()
	// Magnification API fallback — covers edge cases where DXGI fails on
	// every output (pathological driver state, some RDS configurations).
	if err := magCaptureInit(); err == nil {
		captureActive = true
		captureMagActive = true
		log.Printf("helper: capture path = Magnification API (%dx%d)",
			magCaptureWidth(), magCaptureHeight())
		return nil
	} else {
		log.Printf("helper: Magnification init failed: %v", err)
	}

	// Step 3: last-resort GDI. Rarely produces useful frames on no-user
	// sessions but kept as a final safety net.
	if rc := int(C.capture_init_monitor(0)); rc < 0 {
		return fmt.Errorf("screen capture init failed at every layer (dxgi step=%d hr=0x%08x, mag, gdi rc=%d) in a session with %s",
			int(C.dxgi_last_fail()), uint32(C.dxgi_last_hr()), rc, disp)
	}
	captureActive = true
	failStep := int(C.dxgi_last_fail())
	hr := uint32(C.dxgi_last_hr())
	log.Printf("helper: capture path = GDI fallback (dxgi step=%d hr=0x%08x among %d outputs)",
		failStep, hr, len(mons))
	return nil
}

// captureMagActive is true when captureInit picked the Magnification API
// fallback (used for no-user / Secure-Desktop / login-screen contexts that
// DXGI refuses).
var captureMagActive bool

// captureSessionDisplay: how the helper's session is displayed (console /
// RDP, connect state), read by captureInit — the no-picture watchdog uses it.
var captureSessionDisplay = sessionDisplayInfo{protocol: -1}

// capturePath names the single-monitor capture path in use (logs and
// capture_status.path).
func capturePath() string {
	return capturePathName(captureMosaicActive, captureMagActive, captureActive && C.capture_is_gdi() != 0)
}

// captureForceGDI switches the native single-monitor capture from DXGI to GDI
// for the rest of the helper (no-picture fallback of a remote session).
func captureForceGDI() { C.cap_force_gdi() }

// captureDiag is a one-line state of the capture for the no-picture
// diagnostic: path, DXGI's last failure, GDI failures.
func captureDiag() string {
	var n C.long
	var e C.ulong
	C.cap_gdi_failures(&n, &e)
	return fmt.Sprintf("path=%s dxgi_last_fail=%d hr=0x%08x gdi_failures=%d gdi_last_error=%d dxgi_possible=%v",
		capturePath(), int(C.dxgi_last_fail()), uint32(C.dxgi_last_hr()), int64(n), uint32(e), captureDXGIPossible())
}

func captureClose() {
	if captureMosaicActive {
		mosaicCaptureClose()
	}
	if captureMagActive {
		magCaptureClose()
		captureMagActive = false
	}
	if captureActive {
		C.cap_stop()
		captureActive = false
	}
}

// captureStopSingle stops only the single-monitor capture (native DXGI thread +
// Magnification) and leaves mosaic capture untouched. Used when switching to
// mosaic: the mosaic duplications are opened first, then the single output is
// freed, so a failed mosaic can keep the current stream.
func captureStopSingle() {
	if captureMagActive {
		magCaptureClose()
		captureMagActive = false
	}
	if captureActive {
		C.cap_stop()
		captureActive = false
	}
}

// captureSingleDuplicatedOutput is the DXGI output the single-monitor capture
// holds a Desktop Duplication of, or -1 (not running, GDI, Magnification).
// DXGI allows one duplication of an output per process: the mosaic duplicates
// that output only after captureStopSingle released it.
func captureSingleDuplicatedOutput() int {
	if !captureActive || captureMagActive || C.capture_is_gdi() != 0 {
		return -1
	}
	return int(C.capture_get_target_monitor())
}

func captureWidth() int {
	if captureMosaicActive {
		return mosaicWidth()
	}
	if captureMagActive {
		return magCaptureWidth()
	}
	var w, h, ox, oy C.int
	C.cap_get_shared_dims(&w, &h, &ox, &oy)
	return int(w)
}

func captureHeight() int {
	if captureMosaicActive {
		return mosaicHeight()
	}
	if captureMagActive {
		return magCaptureHeight()
	}
	var w, h, ox, oy C.int
	C.cap_get_shared_dims(&w, &h, &ox, &oy)
	return int(h)
}

// captureFrame fills buf (must be width*height*4 bytes) with BGRA pixel data.
// Reads the latest frame produced by the native capture thread (which owns
// DXGI and can follow Secure Desktop transitions unlike Go-runtime threads).
// Returns "no new frame" error when the thread hasn't produced a new frame
// since the last call — caller should skip encoding that tick.
func captureFrame(buf []byte) (width, height int, err error) {
	if captureMosaicActive {
		return mosaicCaptureFrame(buf)
	}
	if captureMagActive {
		width = magCaptureWidth()
		height = magCaptureHeight()
		expected := width * height * 4
		if expected <= 0 {
			return 0, 0, fmt.Errorf("captureFrame: Magnification capture not initialised")
		}
		if len(buf) < expected {
			// The Magnification rectangle grew (display change): the stream
			// must restart at the new size.
			return width, height, errCaptureResized
		}
		if err := magCaptureFrame(buf); err != nil {
			return width, height, fmt.Errorf("mag frame: %w", err)
		}
		return width, height, nil
	}

	if len(buf) == 0 {
		return 0, 0, errCaptureResized
	}
	var w, h C.int
	ret := int(C.cap_fetch((*C.uchar)(unsafe.Pointer(&buf[0])), C.int(len(buf)), &w, &h))
	width = int(w)
	height = int(h)
	if ret == 1 {
		return width, height, fmt.Errorf("no new frame")
	}
	if ret == -3 {
		// The display mode changed and the new frame is larger than the
		// stream's buffer (a smaller one is returned with its own size).
		return width, height, errCaptureResized
	}
	if ret < 0 {
		return 0, 0, fmt.Errorf("cap_fetch failed (code %d)", ret)
	}
	return width, height, nil
}

// capCopyUpright runs the native frame copy of the DXGI path (rotation to the
// desktop orientation, bounds): used by capture_windows_test.go.
func capCopyUpright(dst []byte, dstW, dstH int, src []byte, srcW, srcH, pitch, rot int) {
	if len(dst) < dstW*dstH*4 || len(src) < srcH*pitch || len(dst) == 0 || len(src) == 0 {
		return
	}
	C.cap_copy_upright((*C.uchar)(unsafe.Pointer(&dst[0])), C.int(dstW), C.int(dstH),
		(*C.uchar)(unsafe.Pointer(&src[0])), C.int(srcW), C.int(srcH), C.int(pitch), C.int(rot))
}

// capTestSetGeometry publishes a shared-frame geometry as the native thread
// does after a (re)init, plus one "captured" frame (capture_windows_test.go;
// no capture thread may be running). Returns the geometry sequence.
func capTestSetGeometry(w, h, ox, oy int) int64 {
	return int64(C.cap_test_set_geometry(C.int(w), C.int(h), C.int(ox), C.int(oy)))
}

// capTestReset frees what capTestSetGeometry allocated.
func capTestReset() { C.cap_stop() }

// capDXGIHopeless / capDXGIPermanent run the native DXGI failure
// classification (capture_windows_test.go).
func capDXGIHopeless(step int, hr uint32) bool {
	return C.cap_test_hopeless(C.int(step), C.uint(hr)) != 0
}

func capDXGIPermanent(step int, hr uint32) bool {
	return C.cap_test_permanent(C.int(step), C.uint(hr)) != 0
}

// capGDIIdleMs runs the native GDI pacing (capture_windows_test.go).
func capGDIIdleMs(costMs int) int { return int(C.cap_gdi_idle_ms(C.DWORD(costMs))) }

// capGDINextUpgradeDelay runs the native GDI → DXGI retry schedule
// (cap_gdi_next_upgrade_delay): used by capture_windows_test.go.
func capGDINextUpgradeDelay(prevMs int, hopeless, slow bool) int {
	h, sl := 0, 0
	if hopeless {
		h = 1
	}
	if slow {
		sl = 1
	}
	return int(C.cap_gdi_next_upgrade_delay(C.DWORD(prevMs), C.int(h), C.int(sl)))
}
