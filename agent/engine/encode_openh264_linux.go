//go:build linux && cgo

package engine

// Cisco OpenH264 on Linux, loaded with dlopen (never linked): the same
// encoder settings as encode_openh264_windows.go (screen content, CAVLC,
// constrained baseline, IDR every second). Where the library comes from:
//   1. the copy FetchOpenH264 downloaded from Cisco (openh264_fetch_linux.go),
//      loaded only when its SHA-256 matches the pin;
//   2. libopenh264.so.7 from the system / the Flatpak extension
//      org.freedesktop.Platform.openh264. The Flatpak runtimes and Fedora
//      ship "noopenh264" under that name when the real one is missing: a
//      stub whose encoder cannot be created. The probe creates one, so the
//      stub reads as "unavailable".

/*
#cgo LDFLAGS: -ldl

#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

// ── OpenH264 2.x ABI (codec_api.h / codec_app_def.h), as on Windows ────────
typedef struct {
    int iUsageType; int iPicWidth; int iPicHeight; int iTargetBitrate; int iRCMode; float fMaxFrameRate;
} OL_SEncParamBase;

typedef struct {
    int uiSliceMode;
    unsigned int uiSliceNum;
    unsigned int uiSliceMbNum[35];
    unsigned int uiSliceSizeConstraint;
} OL_SSliceArgument;

typedef struct {
    int iVideoWidth; int iVideoHeight; float fFrameRate; int iSpatialBitrate; int iMaxSpatialBitrate;
    int uiProfileIdc; int uiLevelIdc; int iDLayerQp;
    OL_SSliceArgument sSliceArgument;
    unsigned char bVideoSignalTypePresent; unsigned char uiVideoFormat; unsigned char bFullRange;
    unsigned char bColorDescriptionPresent; unsigned char uiColorPrimaries;
    unsigned char uiTransferCharacteristics; unsigned char uiColorMatrix; unsigned char bAspectRatioPresent;
    int eAspectRatio; unsigned short sAspectRatioExtWidth; unsigned short sAspectRatioExtHeight;
} OL_SSpatialLayerConfig;

typedef struct {
    int iUsageType; int iPicWidth; int iPicHeight; int iTargetBitrate; int iRCMode; float fMaxFrameRate;
    int iTemporalLayerNum; int iSpatialLayerNum;
    OL_SSpatialLayerConfig sSpatialLayers[4];
    int iComplexityMode; unsigned int uiIntraPeriod; int iNumRefFrame; int eSpsPpsIdStrategy;
    unsigned char bPrefixNalAddingCtrl; unsigned char bEnableSSEI; unsigned char bSimulcastAVC;
    int iPaddingFlag; int iEntropyCodingModeFlag;
    unsigned char bEnableFrameSkip; int iMaxBitrate; int iMaxQp; int iMinQp; unsigned int uiMaxNalSize;
    unsigned char bEnableLongTermReference; int iLTRRefNum; unsigned int iLtrMarkPeriod;
    unsigned short iMultipleThreadIdc; unsigned char bUseLoadBalancing;
    int iLoopFilterDisableIdc; int iLoopFilterAlphaC0Offset; int iLoopFilterBetaOffset;
    unsigned char bEnableDenoise; unsigned char bEnableBackgroundDetection; unsigned char bEnableAdaptiveQuant;
    unsigned char bEnableFrameCroppingFlag; unsigned char bEnableSceneChangeDetect; unsigned char bIsLosslessLink;
    unsigned char bFixRCOverShoot; int iIdrBitrateRatio;
} OL_SEncParamExt;

typedef struct {
    int iColorFormat; int iStride[4]; unsigned char *pData[4]; int iPicWidth; int iPicHeight; long long uiTimeStamp;
} OL_SSourcePicture;

typedef struct {
    unsigned char uiTemporalId; unsigned char uiSpatialId; unsigned char uiQualityId; int eFrameType;
    unsigned char uiLayerType; int iSubSeqId; int iNalCount; int *pNalLengthInByte; unsigned char *pBsBuf;
} OL_SLayerBSInfo;

typedef struct {
    int iLayerNum; OL_SLayerBSInfo sLayerInfo[128]; int eFrameType; int iFrameSizeInBytes; long long uiTimeStamp;
} OL_SFrameBSInfo;

typedef struct {
    int  (*Initialize)(void *, void *);
    int  (*InitializeExt)(void *, OL_SEncParamExt *);
    int  (*GetDefaultParams)(void *, OL_SEncParamExt *);
    int  (*Uninitialize)(void *);
    int  (*EncodeFrame)(void *, const OL_SSourcePicture *, OL_SFrameBSInfo *);
    int  (*EncodeParameterSets)(void *, OL_SFrameBSInfo *);
    int  (*ForceIntraFrame)(void *, int);
    int  (*SetOption)(void *, int, void *);
    int  (*GetOption)(void *, int, void *);
} OL_ISVCEncoderVtbl;

typedef struct { const OL_ISVCEncoderVtbl *lpVtbl; } OL_ISVCEncoder;

typedef struct { unsigned int uMajor, uMinor, uRevision, uReserved; } OL_Version;

#define OL_SCREEN_CONTENT_REAL_TIME 1
#define OL_RC_BITRATE_MODE 1
#define OL_LOW_COMPLEXITY 0
#define OL_PRO_BASELINE 66
#define OL_SM_SINGLE_SLICE 0
#define OL_CONSTANT_ID 0
#define OL_videoFormatI420 23
#define OL_videoFrameTypeSkip 4
#define OL_ENCODER_OPTION_DATAFORMAT 0
#define OL_ENCODER_OPTION_SVC_ENCODE_PARAM_BASE 2

static void *g_ol_lib;
static int  (*g_ol_create)(OL_ISVCEncoder **);
static void (*g_ol_destroy)(OL_ISVCEncoder *);
static void (*g_ol_version_ex)(OL_Version *);
static OL_ISVCEncoder *g_ol_enc;
static int g_ol_w, g_ol_h, g_ol_fps;
static char g_ol_err[256];

// ol_load opens path (a file, or a soname when it has no '/'). 0 = ok.
static int ol_load(const char *path) {
	if (g_ol_lib) return 0;
	void *h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (!h) { const char *e = dlerror(); snprintf(g_ol_err, sizeof g_ol_err, "%s", e ? e : "dlopen failed"); return -1; }
	*(void **)(&g_ol_create) = dlsym(h, "WelsCreateSVCEncoder");
	*(void **)(&g_ol_destroy) = dlsym(h, "WelsDestroySVCEncoder");
	*(void **)(&g_ol_version_ex) = dlsym(h, "WelsGetCodecVersionEx");
	if (!g_ol_create || !g_ol_destroy) {
		snprintf(g_ol_err, sizeof g_ol_err, "%s: not an OpenH264 library", path);
		dlclose(h); g_ol_create = NULL; g_ol_destroy = NULL; return -2;
	}
	// The noopenh264 stub cannot create an encoder.
	OL_ISVCEncoder *e = NULL;
	if (g_ol_create(&e) != 0 || !e) {
		snprintf(g_ol_err, sizeof g_ol_err, "%s: no working encoder (the noopenh264 placeholder?)", path);
		dlclose(h); g_ol_create = NULL; g_ol_destroy = NULL; return -3;
	}
	g_ol_destroy(e);
	g_ol_lib = h;
	return 0;
}

static const char *ol_error(void) { return g_ol_err; }

static void ol_version(unsigned int *maj, unsigned int *min, unsigned int *rev) {
	*maj = *min = *rev = 0;
	if (g_ol_version_ex) { OL_Version v; memset(&v, 0, sizeof v); g_ol_version_ex(&v); *maj = v.uMajor; *min = v.uMinor; *rev = v.uRevision; }
}

static int ol_init(int w, int h, int fps, int bitrate) {
	if (!g_ol_lib) return -1;
	if (g_ol_enc) return 0;
	if (g_ol_create(&g_ol_enc) != 0 || !g_ol_enc) { g_ol_enc = NULL; return -2; }
	OL_SEncParamExt p;
	memset(&p, 0, sizeof p);
	g_ol_enc->lpVtbl->GetDefaultParams(g_ol_enc, &p);
	p.iUsageType = OL_SCREEN_CONTENT_REAL_TIME;
	p.iPicWidth = w; p.iPicHeight = h;
	p.iTargetBitrate = bitrate; p.iMaxBitrate = bitrate * 2;
	p.fMaxFrameRate = (float)fps;
	p.iRCMode = OL_RC_BITRATE_MODE;
	p.uiIntraPeriod = (unsigned int)fps;
	p.iSpatialLayerNum = 1; p.iTemporalLayerNum = 1;
	p.iComplexityMode = OL_LOW_COMPLEXITY;
	p.bEnableFrameSkip = 1;
	p.bEnableDenoise = 0;
	p.bEnableBackgroundDetection = 0;
	p.bEnableAdaptiveQuant = 1;
	p.bEnableSceneChangeDetect = 1;
	p.iEntropyCodingModeFlag = 0;
	p.eSpsPpsIdStrategy = OL_CONSTANT_ID;
	p.iMultipleThreadIdc = 1;
	p.sSpatialLayers[0].iVideoWidth = w;
	p.sSpatialLayers[0].iVideoHeight = h;
	p.sSpatialLayers[0].fFrameRate = (float)fps;
	p.sSpatialLayers[0].iSpatialBitrate = bitrate;
	p.sSpatialLayers[0].iMaxSpatialBitrate = bitrate * 2;
	p.sSpatialLayers[0].uiProfileIdc = OL_PRO_BASELINE;
	p.sSpatialLayers[0].sSliceArgument.uiSliceMode = OL_SM_SINGLE_SLICE;
	if (g_ol_enc->lpVtbl->InitializeExt(g_ol_enc, &p) != 0) {
		g_ol_destroy(g_ol_enc); g_ol_enc = NULL; return -3;
	}
	int fmt = OL_videoFormatI420;
	g_ol_enc->lpVtbl->SetOption(g_ol_enc, OL_ENCODER_OPTION_DATAFORMAT, &fmt);
	g_ol_w = w; g_ol_h = h; g_ol_fps = fps;
	return 0;
}

// bgra_to_i420: BT.601 limited range, the same integer formula as the
// Windows bgraToI420 (Y from each pixel, U/V from the 2x2 average).
static void bgra_to_i420(const uint8_t *bgra, int w, int h, uint8_t *dst) {
	uint8_t *Y = dst, *U = dst + w * h, *V = U + (w / 2) * (h / 2);
	int row, col;
	for (row = 0; row < h; row++) {
		const uint8_t *s = bgra + (size_t)row * w * 4;
		uint8_t *d = Y + (size_t)row * w;
		for (col = 0; col < w; col++) {
			int b = s[4*col], g = s[4*col+1], r = s[4*col+2];
			int y = ((77 * r + 150 * g + 29 * b + 128) >> 8) + 16;
			d[col] = (uint8_t)(y > 235 ? 235 : y);
		}
	}
	for (row = 0; row < h / 2; row++) {
		const uint8_t *s0 = bgra + (size_t)(row * 2) * w * 4;
		const uint8_t *s1 = s0 + (size_t)w * 4;
		for (col = 0; col < w / 2; col++) {
			const uint8_t *a = s0 + col * 8, *c = s1 + col * 8;
			int b = (a[0] + a[4] + c[0] + c[4]) >> 2;
			int g = (a[1] + a[5] + c[1] + c[5]) >> 2;
			int r = (a[2] + a[6] + c[2] + c[6]) >> 2;
			int u = ((-43 * r - 85 * g + 128 * b + 128) >> 8) + 128;
			int v = ((128 * r - 107 * g - 21 * b + 128) >> 8) + 128;
			U[row * (w / 2) + col] = (uint8_t)(u < 0 ? 0 : u > 255 ? 255 : u);
			V[row * (w / 2) + col] = (uint8_t)(v < 0 ? 0 : v > 255 ? 255 : v);
		}
	}
}

// ol_encode: Annex B of one frame into out; bytes written, 0 skipped, <0 error.
static int ol_encode(const uint8_t *bgra, int w, int h, long long ts, uint8_t *i420, uint8_t *out, int cap) {
	if (!g_ol_enc) return -1;
	bgra_to_i420(bgra, w, h, i420);
	OL_SSourcePicture pic;
	memset(&pic, 0, sizeof pic);
	pic.iColorFormat = OL_videoFormatI420;
	pic.iPicWidth = w; pic.iPicHeight = h; pic.uiTimeStamp = ts;
	pic.iStride[0] = w; pic.iStride[1] = w / 2; pic.iStride[2] = w / 2;
	pic.pData[0] = i420; pic.pData[1] = i420 + w * h; pic.pData[2] = i420 + w * h + (w / 2) * (h / 2);
	static OL_SFrameBSInfo info;
	memset(&info, 0, sizeof info);
	if (g_ol_enc->lpVtbl->EncodeFrame(g_ol_enc, &pic, &info) != 0) return -2;
	if (info.eFrameType == OL_videoFrameTypeSkip || info.iFrameSizeInBytes == 0) return 0;
	int total = 0, layer, nal;
	for (layer = 0; layer < info.iLayerNum; layer++) {
		OL_SLayerBSInfo *L = &info.sLayerInfo[layer];
		unsigned char *bs = L->pBsBuf;
		for (nal = 0; nal < L->iNalCount; nal++) {
			int n = L->pNalLengthInByte[nal];
			if (total + n > cap) return -3;
			memcpy(out + total, bs, n);
			total += n; bs += n;
		}
	}
	return total;
}

static int ol_set_bitrate(int bitrate) {
	if (!g_ol_enc) return -1;
	OL_SEncParamBase p;
	memset(&p, 0, sizeof p);
	p.iUsageType = OL_SCREEN_CONTENT_REAL_TIME;
	p.iPicWidth = g_ol_w; p.iPicHeight = g_ol_h;
	p.iTargetBitrate = bitrate; p.iRCMode = OL_RC_BITRATE_MODE;
	p.fMaxFrameRate = (float)(g_ol_fps > 0 ? g_ol_fps : 30);
	return g_ol_enc->lpVtbl->SetOption(g_ol_enc, OL_ENCODER_OPTION_SVC_ENCODE_PARAM_BASE, &p);
}

static void ol_close(void) {
	if (g_ol_enc) { g_ol_enc->lpVtbl->Uninitialize(g_ol_enc); g_ol_destroy(g_ol_enc); g_ol_enc = NULL; }
}

// ol_param_extent (tests): bytes GetDefaultParams writes vs sizeof ours.
static int ol_param_extent(int *ours) {
	*ours = (int)sizeof(OL_SEncParamExt);
	if (!g_ol_lib) return -1;
	OL_ISVCEncoder *e = NULL;
	if (g_ol_create(&e) != 0 || !e) return -2;
	static unsigned char buf[sizeof(OL_SEncParamExt) + 4096];
	memset(buf, 0xAB, sizeof buf);
	e->lpVtbl->GetDefaultParams(e, (OL_SEncParamExt *)buf);
	g_ol_destroy(e);
	int last = -1, i;
	for (i = 0; i < (int)sizeof buf; i++) if (buf[i] != 0xAB) last = i;
	return last + 1;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"unsafe"
)

const olMaxOut = 4 * 1024 * 1024

var olLoad struct {
	mu   sync.Mutex
	done bool
	err  error
	from string
}

// openH264LinuxLoad loads the library once (pinned download first, then the
// system soname).
func openH264LinuxLoad() error {
	olLoad.mu.Lock()
	defer olLoad.mu.Unlock()
	if olLoad.done && olLoad.err == nil {
		return nil
	}
	var errs []string
	for _, cand := range openH264Candidates() {
		if cand.verify != nil {
			if err := cand.verify(); err != nil {
				errs = append(errs, err.Error())
				continue
			}
		}
		cs := C.CString(cand.path)
		rc := C.ol_load(cs)
		C.free(unsafe.Pointer(cs))
		if rc == 0 {
			var maj, min, rev C.uint
			C.ol_version(&maj, &min, &rev)
			log.Printf("encoder: OpenH264 %d.%d.%d loaded from %s", maj, min, rev, cand.path)
			olLoad.done, olLoad.err, olLoad.from = true, nil, cand.path
			return nil
		}
		errs = append(errs, C.GoString(C.ol_error()))
	}
	olLoad.done = true
	if len(errs) == 0 {
		olLoad.err = errors.New("no OpenH264 library found")
	} else {
		olLoad.err = fmt.Errorf("OpenH264 not loaded: %v", errs)
	}
	return olLoad.err
}

// openH264LinuxProbe: the encoder backend's probe (encode_linux.go). A
// failed load is retried at the next probe (a download may have happened).
func openH264LinuxProbe() error { return openH264LinuxLoad() }

type openH264Linux struct {
	i420 []byte
	out  []byte
	on   bool
}

func (e *openH264Linux) init(w, h, fps, bitrate int) error {
	if err := openH264LinuxLoad(); err != nil {
		return err
	}
	if rc := C.ol_init(C.int(w), C.int(h), C.int(fps), C.int(bitrate)); rc != 0 {
		return fmt.Errorf("OpenH264 init failed (code %d)", int(rc))
	}
	e.i420 = make([]byte, w*h+2*(w/2)*(h/2))
	e.out = make([]byte, olMaxOut)
	e.on = true
	return nil
}

func (e *openH264Linux) encode(bgra []byte, w, h int, ts int64) ([]byte, error) {
	if !e.on {
		return nil, errors.New("OpenH264 not initialised")
	}
	if len(bgra) < w*h*4 || len(e.i420) < w*h+2*(w/2)*(h/2) {
		return nil, errors.New("OpenH264: frame size changed")
	}
	n := C.ol_encode((*C.uint8_t)(unsafe.Pointer(&bgra[0])), C.int(w), C.int(h), C.longlong(ts),
		(*C.uint8_t)(unsafe.Pointer(&e.i420[0])), (*C.uint8_t)(unsafe.Pointer(&e.out[0])), C.int(len(e.out)))
	if n < 0 {
		return nil, fmt.Errorf("OpenH264 encode failed (code %d)", int(n))
	}
	if n == 0 {
		return nil, nil
	}
	return append([]byte(nil), e.out[:n]...), nil
}

func (e *openH264Linux) setBitrate(bps int) {
	if e.on {
		C.ol_set_bitrate(C.int(bps))
	}
}

func (e *openH264Linux) close() {
	if e.on {
		C.ol_close()
		e.on = false
	}
}

// openH264LinuxParamExtent (tests): see TestOpenH264ParamLayout on Windows.
func openH264LinuxParamExtent() (lib, ours int) {
	var o C.int
	lib = int(C.ol_param_extent(&o))
	return lib, int(o)
}
