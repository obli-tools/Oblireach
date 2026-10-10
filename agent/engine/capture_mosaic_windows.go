//go:build windows

package engine

// Mosaic capture: every monitor composited into one frame at its real relative
// position. It has its own DXGI
// duplications and globals; set_view mode:"mosaic" starts it, set_view
// mode:"monitor" stops it and returns to single capture. If it cannot start it
// reports view_error and the viewer stays single-monitor.
//
// Geometry (tile rects, scale, coordinate mapping) is computed in pure Go
// (protocol.go layoutMosaic, unit-tested). The per-tile policy (when to open,
// retry, fall back to GDI, log) is pure Go too (mosaic_tiles.go, unit-tested).
// This file only provides the primitives: duplicate an output, pull its newest
// desktop image, copy a monitor through GDI, and nearest-neighbour-downscale
// each tile into the composite. Nearest-neighbour is deliberate: a mosaic is an
// overview, and it keeps the per-frame cost of several 4K outputs bounded.
//
// DXGI allows ONE duplication of an output per process: the output the
// single-monitor capture (capture_windows.go) is showing can only be
// duplicated here after that capture released it — mosaicTiles.start does it
// in that order (otherwise the primary tile stays black).

/*
#cgo CFLAGS: -DCOBJMACROS -DINITGUID
// NO -ld3d11 / -ldxgi: loaded at run time (mos_dxgi_fns), as in
// capture_windows.go — the exe must start where they are missing.
#cgo LDFLAGS: -lgdi32 -luser32

#include <windows.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <stdlib.h>
#include <string.h>

#ifndef OR_MAX_MOS
#define OR_MAX_MOS 16
#endif

typedef struct {
    int outputIndex;              // global DXGI enumerate index
    IDXGIOutputDuplication *dup;
    ID3D11Device *dev;
    ID3D11DeviceContext *ctx;
    ID3D11Texture2D *staging;
    int srcW, srcH;               // duplicated texture size (unrotated)
    int rotation;                 // 1 identity, 2=90, 3=180, 4=270
    int rotHint;                  // rotation enumerated with the monitor (0 = unknown)
    int ddRot;                    // rotation DXGI reported at the last open
    int opened;                   // the output has been duplicated at least once
    int tileX, tileY, tileW, tileH;
    int deskX, deskY, deskW, deskH; // monitor rect, physical desktop pixels (GDI fallback)
    unsigned char *last;          // last desktop image of this output (lastW*lastH*4)
    int lastW, lastH;             // allocated size of last
    HDC gdiDC;                    // GDI copy of the monitor, already tile-sized
    HBITMAP gdiBmp, gdiOld;
    unsigned char *gdiBits;       // tileW*tileH*4, top-down BGRX
    int src;                      // what the tile is painted from: 0 nothing, 1 last (DXGI), 2 GDI copy
    unsigned int hr;              // HRESULT of the last failure (open / grab)
} MosOut;

static MosOut g_mo[OR_MAX_MOS];

// ── d3d11.dll / dxgi.dll, loaded at run time ─────────────────────────────────
typedef HRESULT (WINAPI *PFN_MosCreateDXGIFactory1)(REFIID, void **);
typedef HRESULT (WINAPI *PFN_MosD3D11CreateDevice)(IDXGIAdapter *, D3D_DRIVER_TYPE, HMODULE, UINT,
    const D3D_FEATURE_LEVEL *, UINT, UINT, ID3D11Device **, D3D_FEATURE_LEVEL *, ID3D11DeviceContext **);
static PFN_MosCreateDXGIFactory1 p_mos_CreateDXGIFactory1 = NULL;
static PFN_MosD3D11CreateDevice  p_mos_D3D11CreateDevice  = NULL;
static volatile LONG g_mos_dll_state = 0;    // 0 not tried, 1 loaded, -1 unusable
// Windows < 8 (set from Go, os_version.go): no Desktop Duplication, every
// open fails at once with -9 (no device created).
static volatile LONG g_mos_dxgi_disabled = 0;
static void mos_set_dxgi_disabled(int v) { InterlockedExchange(&g_mos_dxgi_disabled, v ? 1 : 0); }

static HMODULE mos_load_system_dll(const wchar_t *name) {
    HMODULE h = LoadLibraryExW(name, NULL, 0x00000800); // LOAD_LIBRARY_SEARCH_SYSTEM32 (KB2533623 on 7 / 2008 R2)
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

// mos_dxgi_fns: 1 when both functions are available (DLLs kept loaded).
static int mos_dxgi_fns(void) {
    LONG st = InterlockedCompareExchange(&g_mos_dll_state, 0, 0);
    if (st != 0) return st > 0;
    HMODULE hd = mos_load_system_dll(L"dxgi.dll");
    HMODULE h3 = mos_load_system_dll(L"d3d11.dll");
    PFN_MosCreateDXGIFactory1 f1 = hd ? (PFN_MosCreateDXGIFactory1)GetProcAddress(hd, "CreateDXGIFactory1") : NULL;
    PFN_MosD3D11CreateDevice  f2 = h3 ? (PFN_MosD3D11CreateDevice)GetProcAddress(h3, "D3D11CreateDevice") : NULL;
    if (!f1 || !f2) {
        if (hd) FreeLibrary(hd);
        if (h3) FreeLibrary(h3);
        InterlockedExchange(&g_mos_dll_state, -1);
        return 0;
    }
    p_mos_CreateDXGIFactory1 = f1;
    p_mos_D3D11CreateDevice = f2;
    InterlockedExchange(&g_mos_dll_state, 1);
    return 1;
}
static int g_mo_n = 0;
static int g_mo_w = 0, g_mo_h = 0;
static int g_mo_originX = 0, g_mo_originY = 0, g_mo_scaleMilli = 1000;
// Set when an output lost its duplication (desktop switch, display change)
// or came back with another size / rotation: the Go side then checks
// whether the monitor layout changed (re-layout) — see mos_take_events.
static int g_mo_lost_event = 0;
static int g_mo_geom_event = 0;
// Cursor rectangle drawn in the previous composite (cleared before the next
// one: in a black gap between screens nothing else repaints it).
static RECT g_mo_lastcur = {0, 0, 0, 0};

// mos_release_dup frees the duplication of one output and keeps its images
// (the tile goes on showing its last one).
static void mos_release_dup(MosOut *m) {
    if (m->staging) { IUnknown_Release(m->staging); m->staging = NULL; }
    if (m->dup) { IUnknown_Release(m->dup); m->dup = NULL; }
    if (m->ctx) { IUnknown_Release(m->ctx); m->ctx = NULL; }
    if (m->dev) { IUnknown_Release(m->dev); m->dev = NULL; }
}

static void mos_free_gdi(MosOut *m) {
    if (m->gdiDC && m->gdiOld) SelectObject(m->gdiDC, m->gdiOld);
    if (m->gdiBmp) DeleteObject(m->gdiBmp);
    if (m->gdiDC) DeleteDC(m->gdiDC);
    m->gdiDC = NULL; m->gdiBmp = NULL; m->gdiOld = NULL; m->gdiBits = NULL;
}

// mos_open_output: create a D3D11 device (on the adapter that owns the output)
// + output duplication for the DXGI output at global enumerate index `idx`
// (same order as enumerate_monitors). On success fills dev/ctx/dup and returns
// 0 with *w/*h/*rot = the duplicated mode. Otherwise returns the failing step
// (-1 factory, -2 output not found, -3 device, -4 IDXGIOutput1, -5
// DuplicateOutput) with m->hr = its HRESULT, and leaves nothing allocated.
static int mos_open_output(int idx, MosOut *m, int *w, int *h, int *rot) {
    if (InterlockedCompareExchange(&g_mos_dxgi_disabled, 0, 0) || !mos_dxgi_fns()) { m->hr = 0; return -9; }
    IDXGIFactory1 *factory = NULL;
    HRESULT hr = p_mos_CreateDXGIFactory1(&IID_IDXGIFactory1, (void**)&factory);
    if (FAILED(hr) || !factory) { m->hr = (unsigned int)hr; return -1; }
    int cur = 0, rc = -2;
    m->hr = 0;
    IDXGIAdapter *adp = NULL;
    for (UINT ai = 0; IDXGIFactory1_EnumAdapters(factory, ai, &adp) == S_OK; ai++) {
        IDXGIOutput *out = NULL;
        int matched = 0;
        for (UINT oi = 0; IDXGIAdapter_EnumOutputs(adp, oi, &out) == S_OK; oi++) {
            if (cur++ != idx) { IUnknown_Release(out); continue; }
            matched = 1;
            D3D_FEATURE_LEVEL fl;
            hr = p_mos_D3D11CreateDevice(adp, D3D_DRIVER_TYPE_UNKNOWN, NULL, 0, NULL, 0,
                D3D11_SDK_VERSION, &m->dev, &fl, &m->ctx);
            if (FAILED(hr)) {
                rc = -3; m->hr = (unsigned int)hr;
            } else {
                IDXGIOutput1 *o1 = NULL;
                hr = IDXGIOutput_QueryInterface(out, &IID_IDXGIOutput1, (void**)&o1);
                if (FAILED(hr)) {
                    rc = -4; m->hr = (unsigned int)hr;
                } else {
                    hr = IDXGIOutput1_DuplicateOutput(o1, (IUnknown*)m->dev, &m->dup);
                    IUnknown_Release(o1);
                    if (FAILED(hr)) {
                        rc = -5; m->hr = (unsigned int)hr; m->dup = NULL;
                    } else {
                        DXGI_OUTDUPL_DESC dd;
                        IDXGIOutputDuplication_GetDesc(m->dup, &dd);
                        *w = (int)dd.ModeDesc.Width;
                        *h = (int)dd.ModeDesc.Height;
                        *rot = (int)dd.Rotation;
                        rc = 0;
                    }
                }
            }
            if (rc != 0) mos_release_dup(m);
            IUnknown_Release(out);
            break;
        }
        IUnknown_Release(adp);
        if (matched) break;
    }
    IUnknown_Release(factory);
    return rc;
}

static void mos_reset(void) {
    for (int i = 0; i < g_mo_n; i++) {
        MosOut *m = &g_mo[i];
        mos_release_dup(m);
        mos_free_gdi(m);
        if (m->last) { free(m->last); m->last = NULL; }
    }
    memset(g_mo, 0, sizeof(g_mo));
    g_mo_n = 0;
    g_mo_lost_event = 0;
    g_mo_geom_event = 0;
    SetRectEmpty(&g_mo_lastcur);
}

static void mos_begin(int compositeW, int compositeH, int originX, int originY, int scaleMilli) {
    mos_reset();
    g_mo_w = compositeW; g_mo_h = compositeH;
    g_mo_originX = originX; g_mo_originY = originY;
    g_mo_scaleMilli = scaleMilli > 0 ? scaleMilli : 1000;
}

// mos_add registers output `idx` with its tile (composite pixels) and its
// monitor rect (physical desktop pixels). It does NOT duplicate the output
// (mos_open does, driven by mosaicTiles). Returns the slot, or -1 when full.
static int mos_add(int idx, int tileX, int tileY, int tileW, int tileH,
                   int deskX, int deskY, int deskW, int deskH, int rotationHint) {
    if (g_mo_n >= OR_MAX_MOS) return -1;
    MosOut *m = &g_mo[g_mo_n];
    memset(m, 0, sizeof(*m));
    m->outputIndex = idx;
    m->tileX = tileX; m->tileY = tileY; m->tileW = tileW; m->tileH = tileH;
    m->deskX = deskX; m->deskY = deskY; m->deskW = deskW; m->deskH = deskH;
    m->rotHint = rotationHint;
    m->rotation = (rotationHint >= 1 && rotationHint <= 4) ? rotationHint : 1;
    return g_mo_n++;
}

// mos_open (re)creates the duplication of a slot. Returns 0, or the failing
// step (see mos_open_output; -6 out of memory, -7 bad slot, -8 empty mode)
// with the HRESULT in mos_last_hr. A new mode (resolution / rotation change)
// reallocates the image buffer and raises the geometry event.
static int mos_open(int slot) {
    if (slot < 0 || slot >= g_mo_n) return -7;
    MosOut *m = &g_mo[slot];
    mos_release_dup(m);
    int w = 0, h = 0, rot = 0;
    int rc = mos_open_output(m->outputIndex, m, &w, &h, &rot);
    if (rc != 0) return rc;
    if (w <= 0 || h <= 0) { mos_release_dup(m); m->hr = 0; return -8; }
    if (!m->last || m->lastW != w || m->lastH != h) {
        unsigned char *nb = (unsigned char*)calloc(1, (size_t)w * h * 4);
        if (!nb) { mos_release_dup(m); m->hr = 0; return -6; }
        if (m->last) g_mo_geom_event = 1; // came back with another size
        free(m->last);
        m->last = nb; m->lastW = w; m->lastH = h;
        if (m->src == 1) m->src = 0;      // the old image is gone until the next one
    }
    if (m->opened && rot != m->ddRot) g_mo_geom_event = 1; // came back rotated
    m->ddRot = rot;
    m->opened = 1;
    m->srcW = w; m->srcH = h;
    // The enumerated rotation wins (DXGI_OUTDUPL_DESC can say UNSPECIFIED).
    if (m->rotHint >= 1 && m->rotHint <= 4) m->rotation = m->rotHint;
    else m->rotation = (rot >= 1 && rot <= 4) ? rot : 1;
    return 0;
}

static unsigned int mos_last_hr(int slot) {
    if (slot < 0 || slot >= g_mo_n) return 0;
    return g_mo[slot].hr;
}

static void mos_tile_info(int slot, int *w, int *h, int *rot, int *src) {
    *w = 0; *h = 0; *rot = 0; *src = 0;
    if (slot < 0 || slot >= g_mo_n) return;
    *w = g_mo[slot].srcW; *h = g_mo[slot].srcH; *rot = g_mo[slot].rotation; *src = g_mo[slot].src;
}

static int mos_count(void) { return g_mo_n; }
static void mos_dims(int *w, int *h) { *w = g_mo_w; *h = g_mo_h; }

// scale_into_tile: nearest-neighbour copy of the last desktop image (srcW x
// srcH, unrotated) into out at the tile rect, applying `rotation` so a portrait
// monitor lands upright. out is g_mo_w x g_mo_h.
static void scale_into_tile(unsigned char *out, MosOut *m) {
    int rot = m->rotation;
    int rw = (rot == 2 || rot == 4) ? m->srcH : m->srcW; // displayed dims
    int rh = (rot == 2 || rot == 4) ? m->srcW : m->srcH;
    if (rw <= 0 || rh <= 0 || m->tileW <= 0 || m->tileH <= 0) return;
    if (m->srcW != m->lastW || m->srcH != m->lastH) return;
    for (int dy = 0; dy < m->tileH; dy++) {
        int oy = m->tileY + dy;
        if (oy < 0 || oy >= g_mo_h) continue;
        int py = dy * rh / m->tileH;              // point in displayed space
        for (int dx = 0; dx < m->tileW; dx++) {
            int ox = m->tileX + dx;
            if (ox < 0 || ox >= g_mo_w) continue;
            int px = dx * rw / m->tileW;
            int tx, ty;                           // map displayed → texture
            switch (rot) {
                case 3: tx = m->srcW - 1 - px; ty = m->srcH - 1 - py; break; // 180
                case 2: tx = py;               ty = m->srcH - 1 - px; break; // 90
                case 4: tx = m->srcW - 1 - py; ty = px;               break; // 270
                default: tx = px; ty = py; break;                            // identity
            }
            if (tx < 0 || tx >= m->srcW || ty < 0 || ty >= m->srcH) continue;
            unsigned char *s = m->last + ((size_t)ty * m->srcW + tx) * 4;
            unsigned char *d = out + ((size_t)oy * g_mo_w + ox) * 4;
            d[0] = s[0]; d[1] = s[1]; d[2] = s[2]; d[3] = 255;
        }
    }
}

// gdi_into_tile copies the GDI copy (already tile-sized) into out.
static void gdi_into_tile(unsigned char *out, MosOut *m) {
    if (!m->gdiBits || m->tileW <= 0 || m->tileH <= 0) return;
    int x0 = m->tileX < 0 ? 0 : m->tileX;
    int x1 = m->tileX + m->tileW > g_mo_w ? g_mo_w : m->tileX + m->tileW;
    if (x1 <= x0) return;
    for (int dy = 0; dy < m->tileH; dy++) {
        int oy = m->tileY + dy;
        if (oy < 0 || oy >= g_mo_h) continue;
        unsigned char *s = m->gdiBits + ((size_t)dy * m->tileW + (x0 - m->tileX)) * 4;
        unsigned char *d = out + ((size_t)oy * g_mo_w + x0) * 4;
        memcpy(d, s, (size_t)(x1 - x0) * 4);
        for (int x = 0; x < x1 - x0; x++) d[x * 4 + 3] = 255;
    }
}

// mos_grab pulls the newest desktop image of a slot into m->last. Returns 1 if
// a new image was copied, 0 when there is none (nothing changed, or only the
// pointer moved: AccumulatedFrames == 0, the desktop image did not change and
// may not even be valid before the first real update — webrtc copies only then
// too), -1 when the duplication died (it is released: mos_open recreates it;
// the HRESULT is in mos_last_hr). It does NOT change what the tile is painted
// from: the Go policy calls mos_show_dxgi (it first checks that the first
// images after a (re)open are not the black ones DXGI can hand out).
//
// The timeout is 0 (like OBS): the frame ticker already paces the composite,
// and waiting on every idle output in turn (8 ms each before) only delayed the
// frame; an image that is not ready now is taken on the next tick.
static int mos_grab(int slot) {
    if (slot < 0 || slot >= g_mo_n) return -1;
    MosOut *m = &g_mo[slot];
    if (!m->dup) return -1;
    DXGI_OUTDUPL_FRAME_INFO info;
    memset(&info, 0, sizeof(info));
    IDXGIResource *res = NULL;
    HRESULT hr = IDXGIOutputDuplication_AcquireNextFrame(m->dup, 0, &info, &res);
    if (hr == DXGI_ERROR_WAIT_TIMEOUT) return 0;
    if (FAILED(hr)) {
        m->hr = (unsigned int)hr;
        mos_release_dup(m);
        g_mo_lost_event = 1;
        return -1;
    }
    int copied = 0;
    if (info.AccumulatedFrames > 0 && res) {
        ID3D11Texture2D *tex = NULL;
        if (SUCCEEDED(IDXGIResource_QueryInterface(res, &IID_ID3D11Texture2D, (void**)&tex))) {
            D3D11_TEXTURE2D_DESC td;
            ID3D11Texture2D_GetDesc(tex, &td);
            int ok = 1;
            if ((int)td.Width != m->srcW || (int)td.Height != m->srcH) {
                // The desktop image is not the size DXGI_OUTDUPL_DESC gave:
                // the texture is authoritative (a buffer of the wrong size
                // would be read out of bounds). Follow it and let the Go side
                // re-check the layout.
                unsigned char *nb = (td.Width > 0 && td.Height > 0)
                    ? (unsigned char*)calloc(1, (size_t)td.Width * td.Height * 4) : NULL;
                if (!nb) {
                    ok = 0;
                } else {
                    if (m->staging) { IUnknown_Release(m->staging); m->staging = NULL; }
                    free(m->last);
                    m->last = nb; m->lastW = (int)td.Width; m->lastH = (int)td.Height;
                    m->srcW = (int)td.Width; m->srcH = (int)td.Height;
                    if (m->src == 1) m->src = 0;
                    g_mo_geom_event = 1;
                }
            }
            if (ok && !m->staging) {
                td.Usage = D3D11_USAGE_STAGING; td.BindFlags = 0;
                td.CPUAccessFlags = D3D11_CPU_ACCESS_READ; td.MiscFlags = 0;
                td.MipLevels = 1; td.ArraySize = 1;
                ID3D11Device_CreateTexture2D(m->dev, &td, NULL, &m->staging);
            }
            if (ok && m->staging) {
                ID3D11DeviceContext_CopyResource(m->ctx, (ID3D11Resource*)m->staging, (ID3D11Resource*)tex);
                copied = 1;
            }
            IUnknown_Release(tex);
        }
    }
    if (res) IUnknown_Release(res);
    IDXGIOutputDuplication_ReleaseFrame(m->dup);
    if (!copied) return 0;

    D3D11_MAPPED_SUBRESOURCE mapped;
    hr = ID3D11DeviceContext_Map(m->ctx, (ID3D11Resource*)m->staging, 0, D3D11_MAP_READ, 0, &mapped);
    if (FAILED(hr)) return 0;
    size_t row = (size_t)m->srcW * 4;
    if (row > mapped.RowPitch) row = mapped.RowPitch;
    unsigned char *src = (unsigned char*)mapped.pData;
    for (int y = 0; y < m->srcH; y++)
        memcpy(m->last + (size_t)y * m->srcW * 4, src + (size_t)y * mapped.RowPitch, row);
    ID3D11DeviceContext_Unmap(m->ctx, (ID3D11Resource*)m->staging, 0);
    return 1;
}

// mos_show_dxgi paints the slot from its DXGI buffer (m->last) from now on.
static void mos_show_dxgi(int slot) {
    if (slot < 0 || slot >= g_mo_n) return;
    MosOut *m = &g_mo[slot];
    if (m->last && m->lastW == m->srcW && m->lastH == m->srcH) m->src = 1;
}

// mos_blank: 1 when the slot's DXGI buffer looks entirely black (a 32x18 grid
// of samples, colour channels only), 0 otherwise.
static int mos_blank(int slot) {
    if (slot < 0 || slot >= g_mo_n) return 0;
    MosOut *m = &g_mo[slot];
    if (!m->last || m->lastW <= 0 || m->lastH <= 0) return 1;
    for (int gy = 0; gy < 18; gy++) {
        int y = (2 * gy + 1) * m->lastH / 36;
        for (int gx = 0; gx < 32; gx++) {
            int x = (2 * gx + 1) * m->lastW / 64;
            unsigned char *p = m->last + ((size_t)y * m->lastW + x) * 4;
            if (p[0] | p[1] | p[2]) return 0;
        }
    }
    return 1;
}

// mos_gdi copies the slot's monitor (physical desktop rect; the process is
// per-monitor DPI aware) into a tile-sized DIB with StretchBlt — for a monitor
// DXGI cannot duplicate (another duplication, DisplayLink, some virtual
// displays) or that has not delivered an image yet. Returns 1 on success.
static int mos_gdi(int slot) {
    if (slot < 0 || slot >= g_mo_n) return 0;
    MosOut *m = &g_mo[slot];
    if (m->tileW <= 0 || m->tileH <= 0 || m->deskW <= 0 || m->deskH <= 0) return 0;
    if (!m->gdiDC) {
        BITMAPINFO bi; ZeroMemory(&bi, sizeof(bi));
        bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
        bi.bmiHeader.biWidth = m->tileW; bi.bmiHeader.biHeight = -m->tileH; // top-down
        bi.bmiHeader.biPlanes = 1; bi.bmiHeader.biBitCount = 32; bi.bmiHeader.biCompression = BI_RGB;
        m->gdiDC = CreateCompatibleDC(NULL);
        if (!m->gdiDC) return 0;
        void *bits = NULL;
        m->gdiBmp = CreateDIBSection(m->gdiDC, &bi, DIB_RGB_COLORS, &bits, NULL, 0);
        if (!m->gdiBmp || !bits) { mos_free_gdi(m); return 0; }
        m->gdiBits = (unsigned char*)bits;
        m->gdiOld = (HBITMAP)SelectObject(m->gdiDC, m->gdiBmp);
        SetStretchBltMode(m->gdiDC, COLORONCOLOR);
    }
    HDC screen = GetDC(NULL);
    if (!screen) return 0;
    BOOL ok = StretchBlt(m->gdiDC, 0, 0, m->tileW, m->tileH,
                         screen, m->deskX, m->deskY, m->deskW, m->deskH, SRCCOPY | CAPTUREBLT);
    ReleaseDC(NULL, screen);
    if (!ok) return 0;
    GdiFlush();
    m->src = 2;
    return 1;
}

// mos_paint_all paints every tile from its current source (a tile with none
// stays black). Called after mosaicTiles.tick refreshed the sources.
static void mos_paint_all(unsigned char *out) {
    for (int i = 0; i < g_mo_n; i++) {
        MosOut *m = &g_mo[i];
        if (m->src == 1 && m->last) scale_into_tile(out, m);
        else if (m->src == 2) gdi_into_tile(out, m);
    }
}

// mos_take_events returns (and clears) bit0 = an output lost its duplication
// since the last call, bit1 = an output came back with another size or
// rotation.
static int mos_take_events(void) {
    int e = (g_mo_lost_event ? 1 : 0) | (g_mo_geom_event ? 2 : 0);
    g_mo_lost_event = 0;
    g_mo_geom_event = 0;
    return e;
}

// mos_erase_cursor clears the cursor drawn in the previous composite: the
// tiles repaint their own area, a gap between screens would otherwise keep a
// trail of cursors.
static void mos_erase_cursor(unsigned char *out) {
    if (IsRectEmpty(&g_mo_lastcur)) return;
    for (int y = g_mo_lastcur.top; y < g_mo_lastcur.bottom && y < g_mo_h; y++)
        memset(out + ((size_t)y * g_mo_w + g_mo_lastcur.left) * 4, 0, (size_t)(g_mo_lastcur.right - g_mo_lastcur.left) * 4);
    SetRectEmpty(&g_mo_lastcur);
}

// mos_overlay_cursor draws the real cursor into the composite at its mapped
// position (DXGI does not capture the cursor). Unscaled icon. Only the
// cursor's own rectangle goes through GDI (a small DIB section): round-tripping
// the whole composite (up to 4096×2304, ~38 MB each way plus a full-size
// bitmap allocation) on every frame would cost more than the capture itself.
static void mos_overlay_cursor(unsigned char *out) {
    CURSORINFO ci; ci.cbSize = sizeof(ci);
    if (!GetCursorInfo(&ci) || !(ci.flags & CURSOR_SHOWING)) return;
    long fx = ((long)ci.ptScreenPos.x - g_mo_originX) * g_mo_scaleMilli / 1000;
    long fy = ((long)ci.ptScreenPos.y - g_mo_originY) * g_mo_scaleMilli / 1000;
    if (fx < -256 || fy < -256 || fx > g_mo_w + 256 || fy > g_mo_h + 256) return;
    ICONINFO ii;
    if (!GetIconInfo(ci.hCursor, &ii)) return;
    int cw = 32, ch = 32;
    BITMAP bm;
    if (ii.hbmColor && GetObject(ii.hbmColor, sizeof(bm), &bm)) { cw = bm.bmWidth; ch = bm.bmHeight; }
    else if (ii.hbmMask && GetObject(ii.hbmMask, sizeof(bm), &bm)) { cw = bm.bmWidth; ch = bm.bmHeight / 2; } // AND+XOR masks stacked
    if (cw <= 0 || ch <= 0 || cw > 256 || ch > 256) { cw = 64; ch = 64; }
    int drawX = (int)fx - (int)ii.xHotspot;
    int drawY = (int)fy - (int)ii.yHotspot;
    // Clip the cursor rectangle to the composite.
    int x0 = drawX < 0 ? 0 : drawX, y0 = drawY < 0 ? 0 : drawY;
    int x1 = drawX + cw > g_mo_w ? g_mo_w : drawX + cw;
    int y1 = drawY + ch > g_mo_h ? g_mo_h : drawY + ch;
    if (x1 > x0 && y1 > y0) {
        int rw = x1 - x0, rh = y1 - y0;
        BITMAPINFO bi; ZeroMemory(&bi, sizeof(bi));
        bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
        bi.bmiHeader.biWidth = rw; bi.bmiHeader.biHeight = -rh;
        bi.bmiHeader.biPlanes = 1; bi.bmiHeader.biBitCount = 32; bi.bmiHeader.biCompression = BI_RGB;
        HDC mem = CreateCompatibleDC(NULL);
        void *bits = NULL;
        HBITMAP dib = mem ? CreateDIBSection(mem, &bi, DIB_RGB_COLORS, &bits, NULL, 0) : NULL;
        if (dib && bits) {
            HBITMAP oldb = (HBITMAP)SelectObject(mem, dib);
            unsigned char *b = (unsigned char*)bits;
            for (int y = 0; y < rh; y++)
                memcpy(b + (size_t)y * rw * 4, out + ((size_t)(y0 + y) * g_mo_w + x0) * 4, (size_t)rw * 4);
            DrawIconEx(mem, drawX - x0, drawY - y0, ci.hCursor, 0, 0, 0, NULL, DI_NORMAL);
            GdiFlush();
            for (int y = 0; y < rh; y++) {
                unsigned char *d = out + ((size_t)(y0 + y) * g_mo_w + x0) * 4;
                memcpy(d, b + (size_t)y * rw * 4, (size_t)rw * 4);
                for (int x = 0; x < rw; x++) d[x * 4 + 3] = 255;
            }
            SelectObject(mem, oldb);
            SetRect(&g_mo_lastcur, x0, y0, x1, y1);
        }
        if (dib) DeleteObject(dib);
        if (mem) DeleteDC(mem);
    }
    if (ii.hbmMask) DeleteObject(ii.hbmMask);
    if (ii.hbmColor) DeleteObject(ii.hbmColor);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	"time"
	"unsafe"
)

// captureMosaicActive is true while the composite (all-monitors) capture is the
// active source. captureWidth/Height/Frame route to it, like captureMagActive.
var captureMosaicActive bool

// mosaicView is the view published while mosaic is active (so publishSingleView
// and the init message carry the right tiles).
var mosaicView reachView

// mosaicState drives the tiles of the running mosaic (nil when none).
var mosaicState *mosaicTiles

// Windows < 8 (os_version.go): no Desktop Duplication — mosaic tiles are
// painted from GDI, their DXGI opens fail at once (-9) without a device.
func init() {
	if !WindowsVersion().DXGIDuplicationPossible() {
		C.mos_set_dxgi_disabled(1)
	}
}

// dxgiMosaicBackend implements mosaicTileBackend on the native MosOut slots.
type dxgiMosaicBackend struct{}

func (dxgiMosaicBackend) open(slot int) (bool, string) {
	rc := int(C.mos_open(C.int(slot)))
	if rc == 0 {
		var w, h, rot, src C.int
		C.mos_tile_info(C.int(slot), &w, &h, &rot, &src)
		return true, fmt.Sprintf("mode %dx%d, rotation %d", int(w), int(h), int(rot))
	}
	hr := uint32(C.mos_last_hr(C.int(slot)))
	return false, fmt.Sprintf("%s hr=0x%08X %s", mosaicStepName(rc), hr, dxgiHRText(hr))
}

func (dxgiMosaicBackend) grab(slot int) (mosaicGrab, string) {
	switch int(C.mos_grab(C.int(slot))) {
	case 1:
		return mosaicGrabNew, ""
	case 0:
		return mosaicGrabNone, ""
	}
	hr := uint32(C.mos_last_hr(C.int(slot)))
	return mosaicGrabLost, fmt.Sprintf("AcquireNextFrame hr=0x%08X %s", hr, dxgiHRText(hr))
}

func (dxgiMosaicBackend) blank(slot int) bool { return C.mos_blank(C.int(slot)) != 0 }

func (dxgiMosaicBackend) showDXGI(slot int) { C.mos_show_dxgi(C.int(slot)) }

func (dxgiMosaicBackend) gdi(slot int) bool { return C.mos_gdi(C.int(slot)) != 0 }

func (dxgiMosaicBackend) source(slot int) int {
	var w, h, rot, src C.int
	C.mos_tile_info(C.int(slot), &w, &h, &rot, &src)
	return int(src)
}

// mosaicCaptureInit lays out every monitor under the viewer's budget and
// duplicates them. The output the single-monitor capture holds is duplicated
// after that capture was released (captureStopSingle): the caller must then
// restore a single-monitor capture if the mosaic fails (captureActive false).
// Returns a view_error code on failure (mosaic_unavailable / too_large /
// capture_failed) so the viewer can fall back to single-monitor.
func mosaicCaptureInit(maxW, maxH, maxPixels int) (reachView, string, error) {
	mons := mosaicMonitors(enumerateMonitors())
	if len(mons) < 2 {
		return reachView{}, "mosaic_unavailable", fmt.Errorf("mosaic needs at least 2 monitors (have %d)", len(mons))
	}
	budget := agentClampBudget(maxW, maxH, maxPixels)
	view := layoutMosaic(mons, budget)
	if view.Width <= 0 || view.Height <= 0 || len(view.Tiles) == 0 {
		return reachView{}, "too_large", fmt.Errorf("mosaic layout produced an empty frame")
	}

	scaleMilli := int(view.Scale * 1000)
	if scaleMilli <= 0 {
		scaleMilli = 1
	}
	// A previous mosaic (re-layout) is released here, before any new duplication.
	mosaicState = nil
	C.mos_begin(C.int(view.Width), C.int(view.Height), C.int(view.originX), C.int(view.originY), C.int(scaleMilli))

	ts := newMosaicTiles(dxgiMosaicBackend{}, log.Printf)
	for _, t := range view.Tiles {
		slot := int(C.mos_add(C.int(t.Index),
			C.int(t.Frame.X), C.int(t.Frame.Y), C.int(t.Frame.W), C.int(t.Frame.H),
			C.int(t.Desktop.X), C.int(t.Desktop.Y), C.int(t.Desktop.W), C.int(t.Desktop.H),
			C.int(t.Rotation)))
		if slot < 0 {
			log.Printf("mosaic: output %d not registered (more than %d monitors) — its tile stays black", t.Index, int(C.OR_MAX_MOS))
			continue
		}
		ts.add(slot, t)
	}
	held := captureSingleDuplicatedOutput()
	heldTxt := "does not hold a DXGI duplication"
	if held >= 0 {
		heldTxt = fmt.Sprintf("duplicates output %d", held)
	}
	log.Printf("mosaic: starting, %d monitor(s) into %dx%d (scale %.3f); the single-monitor capture %s",
		len(ts.tiles), view.Width, view.Height, view.Scale, heldTxt)
	live := ts.start(time.Now(), held, captureStopSingle)
	if live == 0 {
		C.mos_reset()
		return reachView{}, "capture_failed", fmt.Errorf("no monitor could be duplicated for the mosaic")
	}

	captureMosaicActive = true
	mosaicView = view
	mosaicState = ts
	mosaicAllLostSince = time.Time{}
	log.Printf("mosaic: capturing %d/%d monitors through DXGI into %dx%d (scale %.3f); any other is painted from GDI and retried",
		live, len(ts.tiles), view.Width, view.Height, view.Scale)
	return view, "", nil
}

func mosaicCaptureClose() {
	if captureMosaicActive {
		C.mos_reset()
		captureMosaicActive = false
		mosaicView = reachView{}
		mosaicState = nil
	}
}

func mosaicDims() (int, int) {
	var w, h C.int
	C.mos_dims(&w, &h)
	return int(w), int(h)
}

func mosaicWidth() int  { w, _ := mosaicDims(); return w }
func mosaicHeight() int { _, h := mosaicDims(); return h }

// errMosaicLayout: the monitor layout changed under the mosaic (display
// settings, a monitor added / removed / rotated): the helper lays it out
// again and sends a new init (the tiles and the input mapping changed).
var errMosaicLayout = errors.New("mosaic: the monitor layout changed")

// errMosaicLost: no output could be duplicated for mosaicLostGrace — secure
// desktop (lock screen, UAC, Ctrl+Alt+Del), which only the single-monitor
// capture follows. The helper falls back to it (view_error + init).
var errMosaicLost = errors.New("mosaic: the capture is lost (secure desktop?)")

const mosaicLostGrace = 3 * time.Second

var mosaicAllLostSince time.Time

// mosaicLayoutChanged compares the monitors Windows reports now with the
// tiles of the running mosaic (index, desktop rectangle, rotation).
func mosaicLayoutChanged() bool {
	mons := mosaicMonitors(enumerateMonitors())
	if len(mons) != len(mosaicView.Tiles) {
		return true
	}
	for _, m := range mons {
		found := false
		for _, t := range mosaicView.Tiles {
			if t.Index == m.Index {
				found = t.Desktop == (reachRect{m.X, m.Y, m.Width, m.Height}) && t.Rotation == m.Rotation
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

// mosaicCaptureFrame composites the current monitors into buf.
func mosaicCaptureFrame(buf []byte) (int, int, error) {
	w, h := mosaicDims()
	if w <= 0 || h <= 0 || mosaicState == nil || int(C.mos_count()) == 0 {
		return 0, 0, fmt.Errorf("mosaic not initialised")
	}
	if len(buf) < w*h*4 {
		return 0, 0, fmt.Errorf("mosaic buffer too small (%d < %d)", len(buf), w*h*4)
	}
	out := (*C.uchar)(unsafe.Pointer(&buf[0]))
	now := time.Now()
	C.mos_erase_cursor(out)
	mosaicState.tick(now) // new images, retries, GDI fallback — per tile
	C.mos_paint_all(out)
	C.mos_overlay_cursor(out)
	mosaicState.maybeSummary(now, buf, w, h)
	// A duplication was lost or came back different: re-enumerate (only then —
	// it creates a DXGI factory) and re-layout if the monitors changed.
	if ev := int(C.mos_take_events()); ev != 0 && mosaicLayoutChanged() {
		return 0, 0, errMosaicLayout
	}
	if len(mosaicState.tiles) > 0 && mosaicState.live() == 0 {
		if mosaicAllLostSince.IsZero() {
			mosaicAllLostSince = now
		} else if now.Sub(mosaicAllLostSince) > mosaicLostGrace {
			mosaicAllLostSince = time.Time{}
			return 0, 0, errMosaicLost
		}
	} else {
		mosaicAllLostSince = time.Time{}
	}
	return w, h, nil
}
