//go:build windows

package engine

/*
// libvpx's own headers (libvpx-prebuilt, the build the agent ships): the
// encoder configuration, image and packet layouts and the ABI version must
// match the DLL exactly. The functions are called through pointers from
// GetProcAddress (never linked); VPX_DISABLE_CTRL_TYPECHECKS drops the
// header's static wrappers that would reference vpx_codec_control_.
#cgo CFLAGS: -I${SRCDIR}/../../libvpx-prebuilt/mingw64/include -DVPX_DISABLE_CTRL_TYPECHECKS=1
#include <windows.h>
#include <stdint.h>

// Verified loader of the bundled codec DLLs (codecdll_windows.go).
extern uintptr_t obliLoadCodecDLL(char *names);
#include <stdlib.h>
#include <string.h>
#include "vpx/vpx_encoder.h"
#include "vpx/vp8cx.h"

// ── Function pointer types (signatures of vpx_encoder.h / vpx_image.h) ─────

typedef vpx_codec_iface_t* (*PFN_vpx_codec_vp9_cx)(void);
typedef vpx_codec_err_t (*PFN_vpx_codec_enc_config_default)(vpx_codec_iface_t*, vpx_codec_enc_cfg_t*, unsigned int);
typedef vpx_codec_err_t (*PFN_vpx_codec_enc_init_ver)(vpx_codec_ctx_t*, vpx_codec_iface_t*, const vpx_codec_enc_cfg_t*, vpx_codec_flags_t, int);
typedef vpx_codec_err_t (*PFN_vpx_codec_encode)(vpx_codec_ctx_t*, const vpx_image_t*, vpx_codec_pts_t, unsigned long, vpx_enc_frame_flags_t, vpx_enc_deadline_t);
typedef const vpx_codec_cx_pkt_t* (*PFN_vpx_codec_get_cx_data)(vpx_codec_ctx_t*, vpx_codec_iter_t*);
typedef vpx_codec_err_t (*PFN_vpx_codec_destroy)(vpx_codec_ctx_t*);
typedef vpx_image_t* (*PFN_vpx_img_alloc)(vpx_image_t*, vpx_img_fmt_t, unsigned int, unsigned int, unsigned int);
typedef void (*PFN_vpx_img_free)(vpx_image_t*);
typedef vpx_codec_err_t (*PFN_vpx_codec_control_)(vpx_codec_ctx_t*, int, ...);

// ── Global state ────────────────────────────────────────────────────────────

static HMODULE g_vpx_dll = NULL;
static vpx_codec_ctx_t g_vpx_codec;
static vpx_image_t *g_vpx_img = NULL;
static int g_vpx_init = 0;
static int g_vpx_pts = 0;

static PFN_vpx_codec_vp9_cx             pfn_vp9_cx = NULL;
static PFN_vpx_codec_enc_config_default pfn_cfg_default = NULL;
static PFN_vpx_codec_enc_init_ver       pfn_enc_init = NULL;
static PFN_vpx_codec_encode             pfn_encode = NULL;
static PFN_vpx_codec_get_cx_data        pfn_get_data = NULL;
static PFN_vpx_codec_destroy            pfn_destroy = NULL;
static PFN_vpx_img_alloc                pfn_img_alloc = NULL;
static PFN_vpx_img_free                 pfn_img_free = NULL;
static PFN_vpx_codec_control_           pfn_control = NULL;

static int vpx_load_dll(void) {
    if (g_vpx_dll) return 0;
    // From the executable's folder only, verified against the pinned builds
    // (codecdll_windows.go); never searched by bare name.
    g_vpx_dll = (HMODULE)obliLoadCodecDLL((char *)"libvpx-1.dll;libvpx.dll");
    if (!g_vpx_dll) return -1;

    pfn_vp9_cx      = (PFN_vpx_codec_vp9_cx)GetProcAddress(g_vpx_dll, "vpx_codec_vp9_cx");
    pfn_cfg_default = (PFN_vpx_codec_enc_config_default)GetProcAddress(g_vpx_dll, "vpx_codec_enc_config_default");
    pfn_enc_init    = (PFN_vpx_codec_enc_init_ver)GetProcAddress(g_vpx_dll, "vpx_codec_enc_init_ver");
    pfn_encode      = (PFN_vpx_codec_encode)GetProcAddress(g_vpx_dll, "vpx_codec_encode");
    pfn_get_data    = (PFN_vpx_codec_get_cx_data)GetProcAddress(g_vpx_dll, "vpx_codec_get_cx_data");
    pfn_destroy     = (PFN_vpx_codec_destroy)GetProcAddress(g_vpx_dll, "vpx_codec_destroy");
    pfn_img_alloc   = (PFN_vpx_img_alloc)GetProcAddress(g_vpx_dll, "vpx_img_alloc");
    pfn_img_free    = (PFN_vpx_img_free)GetProcAddress(g_vpx_dll, "vpx_img_free");
    pfn_control     = (PFN_vpx_codec_control_)GetProcAddress(g_vpx_dll, "vpx_codec_control_");

    if (!pfn_vp9_cx || !pfn_cfg_default || !pfn_enc_init || !pfn_encode ||
        !pfn_get_data || !pfn_destroy || !pfn_img_alloc || !pfn_img_free || !pfn_control) {
        FreeLibrary(g_vpx_dll); g_vpx_dll = NULL;
        return -2;
    }
    return 0;
}

static int vpx_available(void) { return vpx_load_dll() == 0 ? 1 : 0; }

static int vpx_encoder_init(int w, int h, int fps, int bitrate_kbps) {
    if (g_vpx_init) return 0;
    if (vpx_load_dll() != 0) return -1;

    vpx_codec_enc_cfg_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    if (pfn_cfg_default(pfn_vp9_cx(), &cfg, 0) != VPX_CODEC_OK) return -2;

    cfg.g_w = (unsigned int)w;
    cfg.g_h = (unsigned int)h;
    cfg.g_timebase.num = 1;
    cfg.g_timebase.den = fps;
    cfg.rc_target_bitrate = (unsigned int)bitrate_kbps;
    cfg.g_error_resilient = VPX_ERROR_RESILIENT_DEFAULT;
    cfg.g_lag_in_frames = 0;
    cfg.rc_end_usage = VPX_CBR;
    cfg.g_threads = 2;
    cfg.kf_max_dist = (unsigned int)fps;
    cfg.kf_min_dist = 0;
    cfg.rc_min_quantizer = 4;
    cfg.rc_max_quantizer = 48;

    if (pfn_enc_init(&g_vpx_codec, pfn_vp9_cx(), &cfg, 0, VPX_ENCODER_ABI_VERSION) != VPX_CODEC_OK) return -3;

    pfn_control(&g_vpx_codec, VP8E_SET_CPUUSED, 8);
    pfn_control(&g_vpx_codec, VP9E_SET_ROW_MT, 1);
    pfn_control(&g_vpx_codec, VP9E_SET_TILE_COLUMNS, 2);

    g_vpx_img = pfn_img_alloc(NULL, VPX_IMG_FMT_I420, (unsigned int)w, (unsigned int)h, 1);
    if (!g_vpx_img) { pfn_destroy(&g_vpx_codec); return -4; }

    g_vpx_pts = 0;
    g_vpx_init = 1;
    return 0;
}

static int vpx_encode_frame(const unsigned char *i420, int w, int h,
                             unsigned char *out, int out_cap) {
    if (!g_vpx_init || !g_vpx_img) return -1;

    int y_sz = w*h, uv_sz = (w/2)*(h/2);
    memcpy(g_vpx_img->planes[0], i420, y_sz);
    memcpy(g_vpx_img->planes[1], i420 + y_sz, uv_sz);
    memcpy(g_vpx_img->planes[2], i420 + y_sz + uv_sz, uv_sz);

    if (pfn_encode(&g_vpx_codec, g_vpx_img, g_vpx_pts, 1, 0, VPX_DL_REALTIME) != VPX_CODEC_OK)
        return -2;
    g_vpx_pts++;

    int total = 0;
    vpx_codec_iter_t iter = NULL;
    const vpx_codec_cx_pkt_t *pkt;
    while ((pkt = pfn_get_data(&g_vpx_codec, &iter)) != NULL) {
        if (pkt->kind == VPX_CODEC_CX_FRAME_PKT) {
            int sz = (int)pkt->data.frame.sz;
            if (total + sz <= out_cap) {
                memcpy(out + total, pkt->data.frame.buf, sz);
                total += sz;
            }
        }
    }
    return total;
}

static void vpx_encoder_close(void) {
    if (g_vpx_init) {
        pfn_destroy(&g_vpx_codec);
        if (g_vpx_img) { pfn_img_free(g_vpx_img); g_vpx_img = NULL; }
        g_vpx_init = 0;
    }
}

// vpx_forget_dll drops the module handle without unloading it (tests: the
// loader that verified it frees it).
static void vpx_forget_dll(void) {
    vpx_encoder_close();
    g_vpx_dll = NULL;
}
*/
import "C"
import (
	"fmt"
	"log"
	"unsafe"
)

const vp9MaxBuf = 4 * 1024 * 1024

var (
	vp9InitDone bool
	vp9NalBuf   = make([]byte, vp9MaxBuf)
	vp9I420Buf  []byte
)

func vp9Available() bool {
	return int(C.vpx_available()) == 1
}

func vp9EncoderInit(width, height, fps, bitrateKbps int) error {
	rc := int(C.vpx_encoder_init(C.int(width), C.int(height), C.int(fps), C.int(bitrateKbps)))
	if rc != 0 {
		return fmt.Errorf("VP9 init failed (code %d)", rc)
	}
	vp9InitDone = true
	vp9I420Buf = make([]byte, width*height+2*(width/2)*(height/2))
	log.Printf("VP9 encoder initialized: %dx%d@%dfps %dkbps", width, height, fps, bitrateKbps)
	return nil
}

func vp9EncodeFrame(bgra []byte, width, height int) ([]byte, error) {
	if !vp9InitDone {
		return nil, fmt.Errorf("VP9 not initialized")
	}
	bgraToI420(bgra, width, height, vp9I420Buf)

	n := int(C.vpx_encode_frame(
		(*C.uchar)(unsafe.Pointer(&vp9I420Buf[0])),
		C.int(width), C.int(height),
		(*C.uchar)(unsafe.Pointer(&vp9NalBuf[0])),
		C.int(vp9MaxBuf),
	))
	if n < 0 {
		return nil, fmt.Errorf("VP9 encode failed (code %d)", n)
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]byte, n)
	copy(out, vp9NalBuf[:n])
	return out, nil
}

func vp9EncoderClose() {
	if vp9InitDone {
		C.vpx_encoder_close()
		vp9InitDone = false
	}
}

// vp9ForgetDLL closes the encoder and forgets the loaded libvpx (tests that
// load it from a temporary folder, then unload it through the loader).
func vp9ForgetDLL() {
	C.vpx_forget_dll()
	vp9InitDone = false
}
