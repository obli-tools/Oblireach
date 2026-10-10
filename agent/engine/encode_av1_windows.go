//go:build windows

package engine

/*
// SVT-AV1's own headers (svtav1-prebuilt, version 2.3.0 like the shipped
// libSvtAv1Enc-2.dll): the configuration, buffer header and picture layouts
// must match the DLL exactly. The functions are called through pointers from
// GetProcAddress (never linked; EB_DLL is not defined, so EB_API is empty)
// and their pointer types are taken from the header prototypes.
//
// Until 2026-10 the encoder used hand-written structures and configured
// itself through svt_av1_enc_parse_parameter with names the library does not
// know ("source-width", "fps-num"...): the width stayed 0, the target bit
// rate was 1000 times too high ("tbr" is in kbps), the picture claimed a
// 0-bit depth, and svt_av1_enc_set_parameter refused the configuration.
#cgo CFLAGS: -I${SRCDIR}/../../svtav1-prebuilt/mingw64/include/svt-av1
#include <windows.h>
#include <stdint.h>

// Verified loader of the bundled codec DLLs (codecdll_windows.go).
extern uintptr_t obliLoadCodecDLL(char *names);
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "EbSvtAv1Enc.h"

#if SVT_AV1_VERSION_MAJOR != 2
#error "the headers must be those of the shipped SVT-AV1 2.x DLL (libSvtAv1Enc-2.dll)"
#endif

// ── Function pointer types (from the header prototypes) ────────────────────

typedef const char *(*PFN_svt_av1_get_version)(void);
typedef __typeof__(svt_av1_enc_init_handle)        *PFN_svt_av1_enc_init_handle;
typedef __typeof__(svt_av1_enc_set_parameter)      *PFN_svt_av1_enc_set_parameter;
typedef __typeof__(svt_av1_enc_init)               *PFN_svt_av1_enc_init;
typedef __typeof__(svt_av1_enc_send_picture)       *PFN_svt_av1_enc_send_picture;
typedef __typeof__(svt_av1_enc_get_packet)         *PFN_svt_av1_enc_get_packet;
typedef __typeof__(svt_av1_enc_release_out_buffer) *PFN_svt_av1_enc_release_out_buffer;
typedef __typeof__(svt_av1_enc_deinit)             *PFN_svt_av1_enc_deinit;
typedef __typeof__(svt_av1_enc_deinit_handle)      *PFN_svt_av1_enc_deinit_handle;

// ── Global state ────────────────────────────────────────────────────────────

static HMODULE g_av1_dll = NULL;
static EbComponentType *g_av1_enc = NULL;
static int g_av1_init = 0;
static int g_av1_w = 0, g_av1_h = 0;
static int64_t g_av1_pts = 0;

static PFN_svt_av1_enc_init_handle        pfn_av1_init_handle = NULL;
static PFN_svt_av1_enc_set_parameter      pfn_av1_set_param = NULL;
static PFN_svt_av1_enc_init               pfn_av1_init = NULL;
static PFN_svt_av1_enc_send_picture       pfn_av1_send = NULL;
static PFN_svt_av1_enc_get_packet         pfn_av1_get_pkt = NULL;
static PFN_svt_av1_enc_release_out_buffer pfn_av1_release = NULL;
static PFN_svt_av1_enc_deinit             pfn_av1_deinit = NULL;
static PFN_svt_av1_enc_deinit_handle      pfn_av1_deinit_handle = NULL;

static int av1_load_dll(void) {
    if (g_av1_dll) return 0;
    // From the executable's folder only, verified against the pinned builds
    // (codecdll_windows.go); never searched by bare name.
    g_av1_dll = (HMODULE)obliLoadCodecDLL((char *)"libSvtAv1Enc-2.dll;SvtAv1Enc.dll;libSvtAv1Enc.dll");
    if (!g_av1_dll) return -1;

    PFN_svt_av1_get_version get_version = (PFN_svt_av1_get_version)GetProcAddress(g_av1_dll, "svt_av1_get_version");
    pfn_av1_init_handle   = (PFN_svt_av1_enc_init_handle)GetProcAddress(g_av1_dll, "svt_av1_enc_init_handle");
    pfn_av1_set_param     = (PFN_svt_av1_enc_set_parameter)GetProcAddress(g_av1_dll, "svt_av1_enc_set_parameter");
    pfn_av1_init          = (PFN_svt_av1_enc_init)GetProcAddress(g_av1_dll, "svt_av1_enc_init");
    pfn_av1_send          = (PFN_svt_av1_enc_send_picture)GetProcAddress(g_av1_dll, "svt_av1_enc_send_picture");
    pfn_av1_get_pkt       = (PFN_svt_av1_enc_get_packet)GetProcAddress(g_av1_dll, "svt_av1_enc_get_packet");
    pfn_av1_release       = (PFN_svt_av1_enc_release_out_buffer)GetProcAddress(g_av1_dll, "svt_av1_enc_release_out_buffer");
    pfn_av1_deinit        = (PFN_svt_av1_enc_deinit)GetProcAddress(g_av1_dll, "svt_av1_enc_deinit");
    pfn_av1_deinit_handle = (PFN_svt_av1_enc_deinit_handle)GetProcAddress(g_av1_dll, "svt_av1_enc_deinit_handle");

    if (!get_version || !pfn_av1_init_handle || !pfn_av1_set_param || !pfn_av1_init ||
        !pfn_av1_send || !pfn_av1_get_pkt || !pfn_av1_release ||
        !pfn_av1_deinit || !pfn_av1_deinit_handle) {
        g_av1_dll = NULL; // the loader keeps the module (it owns the handle)
        return -2;
    }
    // The structures above are those of the 2.3 headers: another major or
    // minor version of the library may lay them out differently.
    char want[32];
    snprintf(want, sizeof(want), "v%d.%d.", SVT_AV1_VERSION_MAJOR, SVT_AV1_VERSION_MINOR);
    const char *ver = get_version();
    if (!ver || strncmp(ver, want, strlen(want)) != 0) {
        g_av1_dll = NULL;
        return -3;
    }
    return 0;
}

static int av1_available(void) { return av1_load_dll() == 0 ? 1 : 0; }

static void av1_teardown(int initialized) {
    if (!g_av1_enc) return;
    if (initialized) pfn_av1_deinit(g_av1_enc);
    pfn_av1_deinit_handle(g_av1_enc);
    g_av1_enc = NULL;
}

static int av1_encoder_init(int w, int h, int fps, int bitrate_kbps) {
    if (g_av1_init) return 0;
    if (av1_load_dll() != 0) return -1;
    if (fps <= 0) fps = 30;
    if (bitrate_kbps <= 0) bitrate_kbps = 2000;

    // init_handle loads the library defaults into cfg.
    EbSvtAv1EncConfiguration cfg;
    memset(&cfg, 0, sizeof(cfg));
    if (pfn_av1_init_handle(&g_av1_enc, NULL, &cfg) != EB_ErrorNone) { g_av1_enc = NULL; return -2; }

    // Picture: 8-bit 4:2:0, profile 0 (Main) — the viewers decode
    // "av01.0.04M.08".
    cfg.source_width = (uint32_t)w;
    cfg.source_height = (uint32_t)h;
    cfg.frame_rate_numerator = (uint32_t)fps;
    cfg.frame_rate_denominator = 1;
    cfg.encoder_bit_depth = 8;
    cfg.encoder_color_format = EB_YUV420;
    cfg.profile = MAIN_PROFILE;

    // Real-time remote desktop: low delay (one packet out per picture in,
    // no look-ahead, no reordering), constant bit rate, fastest preset.
    cfg.enc_mode = ENC_M12;
    cfg.pred_structure = SVT_AV1_PRED_LOW_DELAY_B;
    cfg.rate_control_mode = SVT_AV1_RC_MODE_CBR;
    cfg.target_bit_rate = (uint32_t)bitrate_kbps * 1000; // bits per second
    cfg.look_ahead_distance = 0;
    cfg.enable_overlays = FALSE;
    cfg.scene_change_detection = 0;

    // A key frame (with its sequence header) every second, like the H.264
    // and VP9 encoders: a viewer that joins or lost a frame resynchronizes
    // within a second. intra_period_length counts the frames between two
    // key frames; KF_REFRESH makes them real key frames (closed GOP).
    cfg.intra_period_length = fps - 1;
    cfg.intra_refresh_type = SVT_AV1_KF_REFRESH;

    // Two threads (logical_processors is deprecated in 2.x and mapped to
    // this field); two tile columns (log2).
    cfg.level_of_parallelism = 2;
    cfg.tile_columns = 1;
    cfg.tile_rows = 0;

    EbErrorType err = pfn_av1_set_param(g_av1_enc, &cfg);
    if (err != EB_ErrorNone) { av1_teardown(0); return -3; }

    err = pfn_av1_init(g_av1_enc);
    if (err != EB_ErrorNone) { av1_teardown(0); return -4; }

    g_av1_w = w; g_av1_h = h; g_av1_pts = 0; g_av1_init = 1;
    return 0;
}

// av1_encode_frame sends one I420 picture and copies the temporal unit it
// produced (low-overhead OBU stream: temporal delimiter, sequence header on
// key frames, frame OBUs) into out. Returns its size, 0 when there is no
// packet, or a negative error.
static int av1_encode_frame(const unsigned char *i420, int w, int h,
                            unsigned char *out, int out_cap) {
    if (!g_av1_init || !g_av1_enc) return -1;
    if (w != g_av1_w || h != g_av1_h) return -5;

    int y_sz = w * h, uv_sz = (w/2) * (h/2);

    EbSvtIOFormat io;
    memset(&io, 0, sizeof(io));
    io.luma      = (uint8_t*)i420;
    io.cb        = (uint8_t*)(i420 + y_sz);
    io.cr        = (uint8_t*)(i420 + y_sz + uv_sz);
    io.y_stride  = (uint32_t)w;
    io.cb_stride = (uint32_t)(w / 2);
    io.cr_stride = (uint32_t)(w / 2);
    io.width     = (uint32_t)w;
    io.height    = (uint32_t)h;
    io.color_fmt = EB_YUV420;
    io.bit_depth = EB_EIGHT_BIT;

    // The library copies the picture during send_picture.
    EbBufferHeaderType in;
    memset(&in, 0, sizeof(in));
    in.size         = sizeof(EbBufferHeaderType);
    in.p_buffer     = (uint8_t*)&io;
    in.n_filled_len = (uint32_t)(y_sz + 2 * uv_sz);
    in.n_alloc_len  = in.n_filled_len;
    in.pts          = g_av1_pts++;
    in.pic_type     = EB_AV1_INVALID_PICTURE; // the encoder decides
    if (pfn_av1_send(g_av1_enc, &in) != EB_ErrorNone) return -2;

    // Low delay: get_packet waits for the packet of this picture.
    EbBufferHeaderType *pkt = NULL;
    EbErrorType err = pfn_av1_get_pkt(g_av1_enc, &pkt, 0);
    if (err == EB_NoErrorEmptyQueue || (err == EB_ErrorNone && !pkt)) return 0;
    if (err != EB_ErrorNone) return -3;
    int rc;
    if (pkt->flags & EB_BUFFERFLAG_ERROR_MASK) {
        rc = -3;
    } else if ((int)pkt->n_filled_len > out_cap) {
        rc = -4; // never truncate: a cut OBU stream would not decode
    } else {
        rc = (int)pkt->n_filled_len;
        if (rc > 0) memcpy(out, pkt->p_buffer, (size_t)rc);
    }
    pfn_av1_release(&pkt);
    return rc;
}

static void av1_encoder_close(void) {
    if (!g_av1_init || !g_av1_enc) return;
    // End of stream, then drain to the EOS packet so the library's threads
    // are idle before deinit.
    EbBufferHeaderType eos;
    memset(&eos, 0, sizeof(eos));
    eos.size = sizeof(EbBufferHeaderType);
    eos.flags = EB_BUFFERFLAG_EOS;
    eos.pic_type = EB_AV1_INVALID_PICTURE;
    if (pfn_av1_send(g_av1_enc, &eos) == EB_ErrorNone) {
        for (int i = 0; i < 256; i++) {
            EbBufferHeaderType *pkt = NULL;
            EbErrorType err = pfn_av1_get_pkt(g_av1_enc, &pkt, 1);
            if (err != EB_ErrorNone || !pkt) break;
            int done = (pkt->flags & EB_BUFFERFLAG_EOS) != 0;
            pfn_av1_release(&pkt);
            if (done) break;
        }
    }
    av1_teardown(1);
    g_av1_init = 0;
}

// av1_forget_dll drops the module handle without unloading it (tests: the
// loader that verified it frees it).
static void av1_forget_dll(void) {
    av1_encoder_close();
    g_av1_dll = NULL;
}
*/
import "C"
import (
	"fmt"
	"log"
	"unsafe"
)

const av1MaxBuf = 4 * 1024 * 1024

var (
	av1InitDone bool
	av1NalBuf   = make([]byte, av1MaxBuf)
	av1I420Buf  []byte
)

func av1Available() bool { return int(C.av1_available()) == 1 }

func av1EncoderInit(width, height, fps, bitrateKbps int) error {
	// bgraToI420 and the I420 layout handed to the library assume even
	// dimensions (chroma planes of width/2 x height/2).
	if width < 64 || height < 64 || width%2 != 0 || height%2 != 0 {
		return fmt.Errorf("SVT-AV1: unsupported size %dx%d", width, height)
	}
	rc := int(C.av1_encoder_init(C.int(width), C.int(height), C.int(fps), C.int(bitrateKbps)))
	if rc != 0 {
		return fmt.Errorf("SVT-AV1 init failed (code %d)", rc)
	}
	av1InitDone = true
	av1I420Buf = make([]byte, width*height+2*(width/2)*(height/2))
	log.Printf("AV1 encoder initialized: %dx%d@%dfps %dkbps", width, height, fps, bitrateKbps)
	return nil
}

func av1EncodeFrame(bgra []byte, width, height int) ([]byte, error) {
	if !av1InitDone {
		return nil, fmt.Errorf("AV1 not initialized")
	}
	if len(av1I420Buf) != width*height+2*(width/2)*(height/2) || len(bgra) < width*height*4 {
		return nil, fmt.Errorf("AV1: frame %dx%d does not match the encoder", width, height)
	}
	bgraToI420(bgra, width, height, av1I420Buf)
	n := int(C.av1_encode_frame(
		(*C.uchar)(unsafe.Pointer(&av1I420Buf[0])),
		C.int(width), C.int(height),
		(*C.uchar)(unsafe.Pointer(&av1NalBuf[0])), C.int(av1MaxBuf)))
	if n < 0 {
		return nil, fmt.Errorf("AV1 encode failed (code %d)", n)
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]byte, n)
	copy(out, av1NalBuf[:n])
	return out, nil
}

func av1EncoderClose() {
	if av1InitDone {
		C.av1_encoder_close()
		av1InitDone = false
	}
}

// av1ForgetDLL closes the encoder and forgets the loaded SVT-AV1 (tests that
// load it from a temporary folder, then unload it through the loader).
func av1ForgetDLL() {
	C.av1_forget_dll()
	av1InitDone = false
}
