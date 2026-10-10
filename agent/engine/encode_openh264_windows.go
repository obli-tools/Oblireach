//go:build windows

package engine

/*
#cgo LDFLAGS: -lole32

#include <windows.h>
#include <stdint.h>

// Verified loader of the bundled codec DLLs (codecdll_windows.go).
extern uintptr_t obliLoadCodecDLL(char *names);
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

// ── OpenH264 type definitions (from codec_api.h, C-compatible) ──────────────

typedef enum {
    OH_CAMERA_VIDEO_REAL_TIME = 0,
    OH_SCREEN_CONTENT_REAL_TIME = 1
} OH_EUsageType;

typedef enum {
    OH_RC_QUALITY_MODE = 0,
    OH_RC_BITRATE_MODE = 1,
    OH_RC_TIMESTAMP_MODE = 3,
    OH_RC_OFF_MODE = -1
} OH_RC_MODES;

typedef enum {
    OH_LOW_COMPLEXITY = 0,
    OH_MEDIUM_COMPLEXITY = 1,
    OH_HIGH_COMPLEXITY = 2
} OH_ECOMPLEXITY_MODE;

typedef enum {
    OH_SM_SINGLE_SLICE = 0,
    OH_SM_FIXEDSLCNUM_SLICE = 1
} OH_SliceModeEnum;

typedef enum {
    OH_PRO_UNKNOWN = 0,
    OH_PRO_BASELINE = 66,
    OH_PRO_MAIN = 77,
    OH_PRO_HIGH = 100
} OH_EProfileIdc;

typedef enum {
    OH_videoFormatI420 = 23
} OH_EVideoFormatType;

typedef enum {
    OH_videoFrameTypeInvalid = 0,
    OH_videoFrameTypeIDR = 1,
    OH_videoFrameTypeI = 2,
    OH_videoFrameTypeP = 3,
    OH_videoFrameTypeSkip = 4
} OH_EVideoFrameType;

typedef enum {
    OH_ENCODER_OPTION_DATAFORMAT = 0,
    OH_ENCODER_OPTION_IDR_INTERVAL = 1,
    OH_ENCODER_OPTION_SVC_ENCODE_PARAM_BASE = 2,
    OH_ENCODER_OPTION_BITRATE = 7,
    OH_ENCODER_OPTION_MAX_BITRATE = 8,
    OH_ENCODER_OPTION_TRACE_LEVEL = 27
} OH_ENCODER_OPTION;

typedef struct {
    int iUsageType;
    int iPicWidth;
    int iPicHeight;
    int iTargetBitrate;
    int iRCMode;
    float fMaxFrameRate;
} OH_SEncParamBase;

typedef enum {
    OH_CONSTANT_ID = 0,
    OH_INCREASING_ID = 1,
    OH_SPS_LISTING = 2,
    OH_SPS_LISTING_AND_PPS_INCREASING = 3,
    OH_SPS_PPS_LISTING = 6
} OH_EParameterSetStrategy;

typedef struct {
    OH_SliceModeEnum uiSliceMode;
    unsigned int uiSliceNum;
    // MAX_SLICES_NUM_TMP of codec_app_def.h: (128 - (4*4 + 1 + 4)) / 3.
    // Until 2026-10 this array had 256 entries: every field after it (in
    // OH_SEncParamExt: the intra period, frame skip, SPS/PPS strategy, max
    // bit rate...) sat at another offset than the DLL reads, so the encoder
    // ran on its defaults (no periodic IDR). TestOpenH264ParamLayout checks
    // the size against what GetDefaultParams writes.
    unsigned int uiSliceMbNum[35];
    unsigned int uiSliceSizeConstraint;
} OH_SSliceArgument;

typedef struct {
    int         iVideoWidth;
    int         iVideoHeight;
    float       fFrameRate;
    int         iSpatialBitrate;
    int         iMaxSpatialBitrate;
    OH_EProfileIdc uiProfileIdc;
    int         uiLevelIdc;
    int         iDLayerQp;
    OH_SSliceArgument sSliceArgument;
    unsigned char bVideoSignalTypePresent;
    unsigned char uiVideoFormat;
    unsigned char bFullRange;
    unsigned char bColorDescriptionPresent;
    unsigned char uiColorPrimaries;
    unsigned char uiTransferCharacteristics;
    unsigned char uiColorMatrix;
    unsigned char bAspectRatioPresent;
    int         eAspectRatio;
    unsigned short sAspectRatioExtWidth;
    unsigned short sAspectRatioExtHeight;
} OH_SSpatialLayerConfig;

typedef struct {
    OH_EUsageType  iUsageType;
    int         iPicWidth;
    int         iPicHeight;
    int         iTargetBitrate;
    OH_RC_MODES iRCMode;
    float       fMaxFrameRate;
    int         iTemporalLayerNum;
    int         iSpatialLayerNum;
    OH_SSpatialLayerConfig sSpatialLayers[4];
    OH_ECOMPLEXITY_MODE iComplexityMode;
    unsigned int uiIntraPeriod;
    int         iNumRefFrame;
    OH_EParameterSetStrategy eSpsPpsIdStrategy;
    unsigned char bPrefixNalAddingCtrl;
    unsigned char bEnableSSEI;
    unsigned char bSimulcastAVC;
    int         iPaddingFlag;
    int         iEntropyCodingModeFlag;
    unsigned char bEnableFrameSkip;
    int         iMaxBitrate;
    int         iMaxQp;
    int         iMinQp;
    unsigned int uiMaxNalSize;
    unsigned char bEnableLongTermReference;
    int         iLTRRefNum;
    unsigned int iLtrMarkPeriod;
    unsigned short iMultipleThreadIdc;
    unsigned char bUseLoadBalancing;
    int         iLoopFilterDisableIdc;
    int         iLoopFilterAlphaC0Offset;
    int         iLoopFilterBetaOffset;
    unsigned char bEnableDenoise;
    unsigned char bEnableBackgroundDetection;
    unsigned char bEnableAdaptiveQuant;
    unsigned char bEnableFrameCroppingFlag;
    unsigned char bEnableSceneChangeDetect;
    unsigned char bIsLosslessLink;
    unsigned char bFixRCOverShoot;
    int         iIdrBitrateRatio;
} OH_SEncParamExt;

typedef struct {
    int         iColorFormat;
    int         iStride[4];
    unsigned char* pData[4];
    int         iPicWidth;
    int         iPicHeight;
    long long   uiTimeStamp;
} OH_SSourcePicture;

typedef struct {
    unsigned char uiTemporalId;
    unsigned char uiSpatialId;
    unsigned char uiQualityId;
    OH_EVideoFrameType eFrameType;
    unsigned char uiLayerType;
    int         iSubSeqId;
    int         iNalCount;
    int*        pNalLengthInByte;
    unsigned char* pBsBuf;
} OH_SLayerBSInfo;

typedef struct {
    int         iLayerNum;
    OH_SLayerBSInfo sLayerInfo[128];
    OH_EVideoFrameType eFrameType;
    int         iFrameSizeInBytes;
    long long   uiTimeStamp;
} OH_SFrameBSInfo;

// ── ISVCEncoder vtable (C-compatible) ──────────────────────────────────────

// The ISVCEncoder is a C++ object with a vtable pointer as its first member.
// In C, we access it through the double-pointer pattern: (*encoder)->Method(encoder, ...)
// The vtable layout matches the order of virtual methods in codec_api.h.

typedef int  (*FnInitialize)(void*, void*);
typedef int  (*FnInitializeExt)(void*, OH_SEncParamExt*);
typedef int  (*FnGetDefaultParams)(void*, OH_SEncParamExt*);
typedef int  (*FnUninitialize)(void*);
typedef int  (*FnEncodeFrame)(void*, const OH_SSourcePicture*, OH_SFrameBSInfo*);
typedef int  (*FnEncodeParameterSets)(void*, OH_SFrameBSInfo*);
typedef int  (*FnForceIntraFrame)(void*, int);
typedef int  (*FnSetOption)(void*, int, void*);
typedef int  (*FnGetOption)(void*, int, void*);

typedef struct {
    FnInitialize          Initialize;
    FnInitializeExt       InitializeExt;
    FnGetDefaultParams    GetDefaultParams;
    FnUninitialize        Uninitialize;
    FnEncodeFrame         EncodeFrame;
    FnEncodeParameterSets EncodeParameterSets;
    FnForceIntraFrame     ForceIntraFrame;
    FnSetOption           SetOption;
    FnGetOption           GetOption;
} OH_ISVCEncoderVtbl;

typedef struct {
    const OH_ISVCEncoderVtbl* lpVtbl;
} OH_ISVCEncoder;

// ── Global state ──────────────────────────────────────────────────────────

static OH_ISVCEncoder* g_oh_encoder = NULL;
static HMODULE         g_oh_dll     = NULL;
static int             g_oh_w       = 0;
static int             g_oh_h       = 0;

typedef int  (*PFN_WelsCreateSVCEncoder)(OH_ISVCEncoder** ppEncoder);
typedef void (*PFN_WelsDestroySVCEncoder)(OH_ISVCEncoder* pEncoder);

static PFN_WelsCreateSVCEncoder  g_pfnCreate  = NULL;
static PFN_WelsDestroySVCEncoder g_pfnDestroy = NULL;

// ── Load DLL ──────────────────────────────────────────────────────────────

static int oh_load_dll(void) {
    if (g_oh_dll) return 0;
    // From the executable's folder only, verified against the pinned builds
    // (codecdll_windows.go); never searched by bare name.
    g_oh_dll = (HMODULE)obliLoadCodecDLL((char *)"openh264-2.4.1-win64.dll;openh264.dll");
    if (!g_oh_dll) return -1;

    g_pfnCreate  = (PFN_WelsCreateSVCEncoder)GetProcAddress(g_oh_dll, "WelsCreateSVCEncoder");
    g_pfnDestroy = (PFN_WelsDestroySVCEncoder)GetProcAddress(g_oh_dll, "WelsDestroySVCEncoder");
    if (!g_pfnCreate || !g_pfnDestroy) {
        FreeLibrary(g_oh_dll);
        g_oh_dll = NULL;
        return -2;
    }
    return 0;
}

// ── Init ──────────────────────────────────────────────────────────────────

static int oh_encoder_init(int w, int h, int fps, int bitrate) {
    if (oh_load_dll() != 0) return -1;
    if (g_oh_encoder) return 0; // already init

    int rv = g_pfnCreate(&g_oh_encoder);
    if (rv != 0 || !g_oh_encoder) return -2;

    OH_SEncParamExt param;
    memset(&param, 0, sizeof(param));
    g_oh_encoder->lpVtbl->GetDefaultParams(g_oh_encoder, &param);

    param.iUsageType     = OH_SCREEN_CONTENT_REAL_TIME;
    param.iPicWidth      = w;
    param.iPicHeight     = h;
    param.iTargetBitrate = bitrate;
    param.iMaxBitrate    = bitrate * 2;
    param.fMaxFrameRate  = (float)fps;
    param.iRCMode        = OH_RC_BITRATE_MODE;
    param.uiIntraPeriod  = (unsigned int)fps; // IDR every ~1 second

    param.iSpatialLayerNum  = 1;
    param.iTemporalLayerNum = 1;
    param.iComplexityMode   = OH_LOW_COMPLEXITY;
    // Frame skip and scene change detection stay on (the library defaults,
    // which the encoder actually ran with until the structure layout was
    // fixed in 2026-10): OpenH264 cannot hold the bit rate without frame
    // skip, and wants scene change detection for screen content.
    param.bEnableFrameSkip  = 1;
    param.bEnableDenoise    = 0;
    param.bEnableBackgroundDetection = 0;
    param.bEnableAdaptiveQuant = 1; // turned off by OpenH264 for screen content
    param.bEnableSceneChangeDetect = 1;
    param.iEntropyCodingModeFlag = 0; // CAVLC (faster than CABAC)
    param.eSpsPpsIdStrategy = OH_CONSTANT_ID;
    param.iMultipleThreadIdc = 1;

    param.sSpatialLayers[0].iVideoWidth  = w;
    param.sSpatialLayers[0].iVideoHeight = h;
    param.sSpatialLayers[0].fFrameRate   = (float)fps;
    param.sSpatialLayers[0].iSpatialBitrate    = bitrate;
    param.sSpatialLayers[0].iMaxSpatialBitrate = bitrate * 2;
    param.sSpatialLayers[0].uiProfileIdc = OH_PRO_BASELINE;
    param.sSpatialLayers[0].sSliceArgument.uiSliceMode = OH_SM_SINGLE_SLICE;

    rv = g_oh_encoder->lpVtbl->InitializeExt(g_oh_encoder, &param);
    if (rv != 0) {
        g_pfnDestroy(g_oh_encoder);
        g_oh_encoder = NULL;
        return -3;
    }

    int fmt = OH_videoFormatI420;
    g_oh_encoder->lpVtbl->SetOption(g_oh_encoder, OH_ENCODER_OPTION_DATAFORMAT, &fmt);

    g_oh_w = w;
    g_oh_h = h;
    return 0;
}

// ── Encode one frame ──────────────────────────────────────────────────────
// i420: Y(w*h) + U(w/2*h/2) + V(w/2*h/2)
// out_buf: receives Annex B H.264 data
// Returns bytes written, 0 = skip, negative = error.

static int oh_encode_frame(
    const unsigned char *i420, int w, int h,
    long long timestamp_ms,
    unsigned char *out_buf, int out_cap)
{
    if (!g_oh_encoder) return -1;

    OH_SSourcePicture pic;
    memset(&pic, 0, sizeof(pic));
    pic.iColorFormat = OH_videoFormatI420;
    pic.iPicWidth    = w;
    pic.iPicHeight   = h;
    pic.uiTimeStamp  = timestamp_ms;
    pic.iStride[0]   = w;
    pic.iStride[1]   = w / 2;
    pic.iStride[2]   = w / 2;
    pic.pData[0]     = (unsigned char*)i420;
    pic.pData[1]     = (unsigned char*)i420 + w * h;
    pic.pData[2]     = (unsigned char*)i420 + w * h + (w/2) * (h/2);

    OH_SFrameBSInfo info;
    memset(&info, 0, sizeof(info));

    int rv = g_oh_encoder->lpVtbl->EncodeFrame(g_oh_encoder, &pic, &info);
    if (rv != 0) return -2;

    if (info.eFrameType == OH_videoFrameTypeSkip || info.iFrameSizeInBytes == 0) {
        return 0;
    }

    // Collect all NAL units from all layers into out_buf
    int total = 0;
    int layer;
    for (layer = 0; layer < info.iLayerNum; layer++) {
        OH_SLayerBSInfo *pLayer = &info.sLayerInfo[layer];
        unsigned char *pBs = pLayer->pBsBuf;
        int nal;
        for (nal = 0; nal < pLayer->iNalCount; nal++) {
            int nalLen = pLayer->pNalLengthInByte[nal];
            if (total + nalLen > out_cap) break;
            memcpy(out_buf + total, pBs, nalLen);
            total += nalLen;
            pBs += nalLen;
        }
    }
    return total;
}

// ── Close ─────────────────────────────────────────────────────────────────

static void oh_encoder_close(void) {
    if (g_oh_encoder) {
        g_oh_encoder->lpVtbl->Uninitialize(g_oh_encoder);
        g_pfnDestroy(g_oh_encoder);
        g_oh_encoder = NULL;
    }
}

// ── Set bitrate at runtime ────────────────────────────────────────────────

static int oh_set_bitrate(int bitrate) {
    if (!g_oh_encoder) return -1;
    OH_SEncParamBase param;
    memset(&param, 0, sizeof(param));
    param.iUsageType = OH_SCREEN_CONTENT_REAL_TIME;
    param.iPicWidth = g_oh_w;
    param.iPicHeight = g_oh_h;
    param.iTargetBitrate = bitrate;
    param.iRCMode = OH_RC_BITRATE_MODE;
    param.fMaxFrameRate = 30.0f;
    return g_oh_encoder->lpVtbl->SetOption(g_oh_encoder,
        OH_ENCODER_OPTION_SVC_ENCODE_PARAM_BASE, &param);
}

// ── Check if DLL is available ─────────────────────────────────────────────

static int oh_available(void) {
    return (oh_load_dll() == 0) ? 1 : 0;
}

// ── Tests ─────────────────────────────────────────────────────────────────

// oh_param_extent returns how many bytes GetDefaultParams writes into the
// parameter structure (the DLL's sizeof(SEncParamExt), as far as the last
// field is concerned) and stores sizeof(OH_SEncParamExt) in *ours.
static int oh_param_extent(int *ours) {
    *ours = (int)sizeof(OH_SEncParamExt);
    if (oh_load_dll() != 0) return -1;
    OH_ISVCEncoder *enc = NULL;
    if (g_pfnCreate(&enc) != 0 || !enc) return -2;
    static unsigned char buf[sizeof(OH_SEncParamExt) + 4096];
    memset(buf, 0xAB, sizeof(buf));
    enc->lpVtbl->GetDefaultParams(enc, (OH_SEncParamExt*)buf);
    g_pfnDestroy(enc);
    int last = -1;
    for (int i = 0; i < (int)sizeof(buf); i++) if (buf[i] != 0xAB) last = i;
    return last + 1;
}

// oh_forget_dll drops the module handle without unloading it (tests: the
// loader that verified it frees it).
static void oh_forget_dll(void) {
    oh_encoder_close();
    g_oh_dll = NULL;
}
*/
import "C"
import (
	"fmt"
	"log"
	"unsafe"
)

const ohMaxBuf = 4 * 1024 * 1024

var (
	ohInitDone bool
	ohNalBuf   = make([]byte, ohMaxBuf)
	ohI420Buf  []byte
)

func openH264Available() bool {
	return int(C.oh_available()) == 1
}

func openH264Init(width, height, fps, bitrate int) error {
	rc := int(C.oh_encoder_init(C.int(width), C.int(height), C.int(fps), C.int(bitrate)))
	if rc != 0 {
		return fmt.Errorf("OpenH264 init failed (code %d)", rc)
	}
	ohInitDone = true
	// Pre-allocate I420 buffer
	ohI420Buf = make([]byte, width*height+2*(width/2)*(height/2))
	log.Printf("OpenH264 encoder initialized: %dx%d@%dfps %dkbps", width, height, fps, bitrate/1000)
	return nil
}

// openH264EncodeFrame encodes a BGRA frame to H.264 Annex B via OpenH264.
func openH264EncodeFrame(bgra []byte, width, height int, timestampMs int64) ([]byte, error) {
	if !ohInitDone {
		return nil, fmt.Errorf("OpenH264 not initialized")
	}

	// BGRA → I420
	bgraToI420(bgra, width, height, ohI420Buf)

	n := int(C.oh_encode_frame(
		(*C.uchar)(unsafe.Pointer(&ohI420Buf[0])),
		C.int(width), C.int(height),
		C.longlong(timestampMs),
		(*C.uchar)(unsafe.Pointer(&ohNalBuf[0])),
		C.int(ohMaxBuf),
	))
	if n < 0 {
		return nil, fmt.Errorf("OpenH264 encode failed (code %d)", n)
	}
	if n == 0 {
		return nil, nil // skipped
	}

	out := make([]byte, n)
	copy(out, ohNalBuf[:n])
	return out, nil
}

func openH264SetBitrate(bitrate int) {
	if ohInitDone {
		C.oh_set_bitrate(C.int(bitrate))
	}
}

func openH264Close() {
	if ohInitDone {
		C.oh_encoder_close()
		ohInitDone = false
	}
}

// openH264ParamExtent (tests): the bytes the DLL's GetDefaultParams writes
// into the encoder parameters, and sizeof(OH_SEncParamExt).
func openH264ParamExtent() (dll, ours int) {
	var o C.int
	dll = int(C.oh_param_extent(&o))
	return dll, int(o)
}

// openH264ForgetDLL closes the encoder and forgets the loaded OpenH264
// (tests that load it from a temporary folder, then unload it through the
// loader).
func openH264ForgetDLL() {
	C.oh_forget_dll()
	ohInitDone = false
}

// bgraToI420 converts BGRA pixels to I420 (YUV420 planar).
func bgraToI420(bgra []byte, w, h int, dst []byte) {
	ySize := w * h
	uOff := ySize
	vOff := ySize + (w/2)*(h/2)

	// Y plane
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			off := (row*w + col) * 4
			b, g, r := int(bgra[off]), int(bgra[off+1]), int(bgra[off+2])
			y := ((77*r + 150*g + 29*b + 128) >> 8) + 16
			if y > 235 {
				y = 235
			}
			dst[row*w+col] = byte(y)
		}
	}
	// U and V planes (4:2:0 subsampling)
	for row := 0; row < h/2; row++ {
		for col := 0; col < w/2; col++ {
			s0 := (row*2)*w*4 + col*2*4
			s1 := (row*2+1)*w*4 + col*2*4
			b := (int(bgra[s0]) + int(bgra[s0+4]) + int(bgra[s1]) + int(bgra[s1+4])) >> 2
			g := (int(bgra[s0+1]) + int(bgra[s0+5]) + int(bgra[s1+1]) + int(bgra[s1+5])) >> 2
			r := (int(bgra[s0+2]) + int(bgra[s0+6]) + int(bgra[s1+2]) + int(bgra[s1+6])) >> 2
			u := ((-43*r - 85*g + 128*b + 128) >> 8) + 128
			v := ((128*r - 107*g - 21*b + 128) >> 8) + 128
			if u < 0 {
				u = 0
			} else if u > 255 {
				u = 255
			}
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			dst[uOff+row*(w/2)+col] = byte(u)
			dst[vOff+row*(w/2)+col] = byte(v)
		}
	}
}
