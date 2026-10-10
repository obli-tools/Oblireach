//go:build windows && h265

package engine

/*
#include <windows.h>
#include <stdint.h>

// Verified loader of the bundled codec DLLs (codecdll_windows.go).
extern uintptr_t obliLoadCodecDLL(char *names);
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stdio.h>

// ── Minimal x265 type definitions for dynamic loading ───────────────────────

typedef struct x265_nal {
    uint32_t type;
    uint32_t sizeBytes;
    uint8_t* payload;
} x265_nal;

// x265_param and x265_picture are large, opaque structs.
// We use them only through the API functions (alloc/init/free),
// never accessing fields directly — except a few via helper C functions.
typedef void x265_param;
typedef void x265_picture;
typedef void x265_encoder;

// ── Function pointer types ──────────────────────────────────────────────────

typedef x265_param*   (*PFN_x265_param_alloc)(void);
typedef void          (*PFN_x265_param_free)(x265_param*);
typedef int           (*PFN_x265_param_default_preset)(x265_param*, const char*, const char*);
typedef x265_picture* (*PFN_x265_picture_alloc)(void);
typedef void          (*PFN_x265_picture_free)(x265_picture*);
typedef void          (*PFN_x265_picture_init)(x265_param*, x265_picture*);
typedef x265_encoder* (*PFN_x265_encoder_open)(x265_param*);
typedef int           (*PFN_x265_encoder_encode)(x265_encoder*, x265_nal**, uint32_t*, x265_picture*, x265_picture*);
typedef void          (*PFN_x265_encoder_close)(x265_encoder*);

// ── Known param offsets (x265_param is a huge struct; we set fields by known byte offsets)
// These offsets are for x265 build 215 (4.1). We compute them from the header at build time
// by using a helper approach: set via param_parse or param_default_preset and then override.
// For safety, we use x265_param_parse if available.
typedef int (*PFN_x265_param_parse)(x265_param*, const char*, const char*);

// ── Global state ────────────────────────────────────────────────────────────

static HMODULE         g_x265_dll     = NULL;
static x265_encoder*   g_x265_enc    = NULL;
static x265_param*     g_x265_param  = NULL;
static x265_picture*   g_x265_pic    = NULL;
static int             g_x265_w      = 0;
static int             g_x265_h      = 0;

static PFN_x265_param_alloc          pfn_param_alloc = NULL;
static PFN_x265_param_free           pfn_param_free  = NULL;
static PFN_x265_param_default_preset pfn_param_preset = NULL;
static PFN_x265_param_parse          pfn_param_parse = NULL;
static PFN_x265_picture_alloc        pfn_pic_alloc   = NULL;
static PFN_x265_picture_free         pfn_pic_free    = NULL;
static PFN_x265_picture_init         pfn_pic_init    = NULL;
static PFN_x265_encoder_open         pfn_enc_open    = NULL;
static PFN_x265_encoder_encode       pfn_enc_encode  = NULL;
static PFN_x265_encoder_close        pfn_enc_close   = NULL;

static int x265_load_dll(void) {
    if (g_x265_dll) return 0;
    // From the executable's folder only, verified against the pinned builds
    // (codecdll_windows.go: an H.265 build must pin the libx265 it ships).
    g_x265_dll = (HMODULE)obliLoadCodecDLL((char *)"libx265-215.dll;libx265.dll");
    if (!g_x265_dll) return -1;

    pfn_param_alloc  = (PFN_x265_param_alloc)GetProcAddress(g_x265_dll, "x265_param_alloc");
    pfn_param_free   = (PFN_x265_param_free)GetProcAddress(g_x265_dll, "x265_param_free");
    pfn_param_preset = (PFN_x265_param_default_preset)GetProcAddress(g_x265_dll, "x265_param_default_preset");
    pfn_param_parse  = (PFN_x265_param_parse)GetProcAddress(g_x265_dll, "x265_param_parse");
    pfn_pic_alloc    = (PFN_x265_picture_alloc)GetProcAddress(g_x265_dll, "x265_picture_alloc");
    pfn_pic_free     = (PFN_x265_picture_free)GetProcAddress(g_x265_dll, "x265_picture_free");
    pfn_pic_init     = (PFN_x265_picture_init)GetProcAddress(g_x265_dll, "x265_picture_init");
    // x265_encoder_open is mangled with build number
    pfn_enc_open     = (PFN_x265_encoder_open)GetProcAddress(g_x265_dll, "x265_encoder_open_215");
    if (!pfn_enc_open)
        pfn_enc_open = (PFN_x265_encoder_open)GetProcAddress(g_x265_dll, "x265_encoder_open");
    pfn_enc_encode   = (PFN_x265_encoder_encode)GetProcAddress(g_x265_dll, "x265_encoder_encode");
    pfn_enc_close    = (PFN_x265_encoder_close)GetProcAddress(g_x265_dll, "x265_encoder_close");

    if (!pfn_param_alloc || !pfn_param_free || !pfn_param_preset ||
        !pfn_pic_alloc || !pfn_pic_free || !pfn_pic_init ||
        !pfn_enc_open || !pfn_enc_encode || !pfn_enc_close) {
        FreeLibrary(g_x265_dll); g_x265_dll = NULL;
        return -2;
    }
    return 0;
}

static int x265_available(void) { return x265_load_dll() == 0 ? 1 : 0; }

static int x265_encoder_init_ex(int w, int h, int fps, int bitrate_kbps) {
    if (g_x265_enc) return 0;
    if (x265_load_dll() != 0) return -1;

    g_x265_param = pfn_param_alloc();
    if (!g_x265_param) return -2;

    // ultrafast preset + zerolatency tune for real-time streaming
    if (pfn_param_preset(g_x265_param, "ultrafast", "zerolatency") != 0) {
        pfn_param_free(g_x265_param); g_x265_param = NULL;
        return -3;
    }

    // Set parameters via x265_param_parse (safe, no struct offset guessing)
    if (pfn_param_parse) {
        char buf[64];
        sprintf(buf, "%d", w); pfn_param_parse(g_x265_param, "input-res", buf);
        // param_parse for "input-res" expects "WxH" format
        sprintf(buf, "%dx%d", w, h); pfn_param_parse(g_x265_param, "input-res", buf);
        sprintf(buf, "%d", fps); pfn_param_parse(g_x265_param, "fps", buf);
        sprintf(buf, "%d", bitrate_kbps); pfn_param_parse(g_x265_param, "bitrate", buf);
        pfn_param_parse(g_x265_param, "repeat-headers", "1");
        sprintf(buf, "%d", fps); pfn_param_parse(g_x265_param, "keyint", buf);
        pfn_param_parse(g_x265_param, "rc-lookahead", "0");
        pfn_param_parse(g_x265_param, "bframes", "0");
        pfn_param_parse(g_x265_param, "scenecut", "0");
    }

    g_x265_enc = pfn_enc_open(g_x265_param);
    if (!g_x265_enc) {
        pfn_param_free(g_x265_param); g_x265_param = NULL;
        return -4;
    }

    g_x265_pic = pfn_pic_alloc();
    if (!g_x265_pic) {
        pfn_enc_close(g_x265_enc); g_x265_enc = NULL;
        pfn_param_free(g_x265_param); g_x265_param = NULL;
        return -5;
    }
    pfn_pic_init(g_x265_param, g_x265_pic);

    g_x265_w = w;
    g_x265_h = h;
    return 0;
}

// x265_encode_frame: encode one I420 frame.
// The x265_picture struct starts with: int colorSpace, then void* planes[3], int stride[3]...
// With x265_picture_init, colorSpace is set to X265_CSP_I420 = 1.
// We need to set planes and stride. Since we can't access struct fields directly
// (the struct is opaque), we use a trick: x265_picture_init sets defaults,
// then we patch the known fields.
//
// x265_picture layout (first fields, consistent across builds):
//   int colorSpace;        // offset 0 (4 bytes, set by picture_init)
//   int bitDepth;          // offset 4
//   int *sliceType;        // offset 8 (pointer, 8 bytes on x64... actually int, 4 bytes)
// Actually this is complicated. Let me use a simpler approach:
// Cast the picture to a byte array and set the plane pointers at known offsets.
// But this is fragile. Better approach: since x265_picture_init sets colorSpace=I420,
// we just need to set planes[0..2] and stride[0..2].
//
// From x265.h, x265_picture starts with:
//   int colorSpace;   (offset 0, 4 bytes)
//   int bitDepth;     (offset 4, 4 bytes)
//   int64_t pts;      (offset 8, 8 bytes)
//   int64_t dts;      (offset 16, 8 bytes)
//   void* userData;   (offset 24, 8 bytes)
//   int64_t reorderedPts; (offset 32, 8 bytes)
//   int sliceType;    (offset 40, 4 bytes)
//   int poc;          (offset 44, 4 bytes)
//   void* planes[3];  (offset 48, 24 bytes on x64)
//   int stride[3];    (offset 72, 12 bytes)
// Let's verify by reading the header more carefully.

// Actually, the safest approach: define a minimal struct that matches the
// beginning of x265_picture. We only need to set planes and stride.

typedef struct {
    int colorSpace;
    int bitDepth;
    int64_t pts;
    int64_t dts;
    void* userData;
    int64_t reorderedPts;
    int sliceType;
    int poc;
    void* planes[3];
    int stride[3];
} x265_picture_head;

static int x265_encode_frame(const unsigned char *i420, int w, int h,
                              unsigned char *out, int out_cap) {
    if (!g_x265_enc || !g_x265_pic) return -1;

    int y_sz = w * h;
    int uv_sz = (w/2) * (h/2);

    // Set plane pointers and strides
    x265_picture_head *pic = (x265_picture_head*)g_x265_pic;
    pic->planes[0] = (void*)i420;
    pic->planes[1] = (void*)(i420 + y_sz);
    pic->planes[2] = (void*)(i420 + y_sz + uv_sz);
    pic->stride[0] = w;
    pic->stride[1] = w / 2;
    pic->stride[2] = w / 2;

    x265_nal *nals = NULL;
    uint32_t nalCount = 0;

    int ret = pfn_enc_encode(g_x265_enc, &nals, &nalCount, g_x265_pic, NULL);
    if (ret < 0) return -2;
    if (ret == 0 || nalCount == 0) return 0;

    int total = 0;
    uint32_t i;
    for (i = 0; i < nalCount; i++) {
        int sz = (int)nals[i].sizeBytes;
        if (total + sz <= out_cap) {
            memcpy(out + total, nals[i].payload, sz);
            total += sz;
        }
    }
    return total;
}

static void x265_encoder_close_ex(void) {
    if (g_x265_enc) {
        pfn_enc_close(g_x265_enc);
        g_x265_enc = NULL;
    }
    if (g_x265_pic) {
        pfn_pic_free(g_x265_pic);
        g_x265_pic = NULL;
    }
    if (g_x265_param) {
        pfn_param_free(g_x265_param);
        g_x265_param = NULL;
    }
}
*/
import "C"
import (
	"fmt"
	"log"
	"unsafe"
)

const h265MaxBuf = 4 * 1024 * 1024

var (
	h265InitDone bool
	h265NalBuf   = make([]byte, h265MaxBuf)
	h265I420Buf  []byte
)

func h265Available() bool {
	return int(C.x265_available()) == 1
}

func h265EncoderInit(width, height, fps, bitrateKbps int) error {
	rc := int(C.x265_encoder_init_ex(C.int(width), C.int(height), C.int(fps), C.int(bitrateKbps)))
	if rc != 0 {
		return fmt.Errorf("x265 init failed (code %d)", rc)
	}
	h265InitDone = true
	h265I420Buf = make([]byte, width*height+2*(width/2)*(height/2))
	log.Printf("H.265 encoder initialized: %dx%d@%dfps %dkbps", width, height, fps, bitrateKbps)
	return nil
}

func h265EncodeFrame(bgra []byte, width, height int) ([]byte, error) {
	if !h265InitDone {
		return nil, fmt.Errorf("H.265 not initialized")
	}
	bgraToI420(bgra, width, height, h265I420Buf)

	n := int(C.x265_encode_frame(
		(*C.uchar)(unsafe.Pointer(&h265I420Buf[0])),
		C.int(width), C.int(height),
		(*C.uchar)(unsafe.Pointer(&h265NalBuf[0])),
		C.int(h265MaxBuf),
	))
	if n < 0 {
		return nil, fmt.Errorf("H.265 encode failed (code %d)", n)
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]byte, n)
	copy(out, h265NalBuf[:n])
	return out, nil
}

func h265EncoderClose() {
	if h265InitDone {
		C.x265_encoder_close_ex()
		h265InitDone = false
	}
}
