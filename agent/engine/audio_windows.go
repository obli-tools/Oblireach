//go:build windows

package engine

/*
#cgo LDFLAGS: -lole32 -loleaut32

#include <windows.h>
#include <mmdeviceapi.h>
#include <audioclient.h>
#include <mmreg.h>
#include <math.h>
#include <stdlib.h>
#include <string.h>

// MinGW may not have these — define manually
static const GUID g_CLSID_MMDeviceEnumerator = {0xBCDE0395, 0xE52F, 0x467C, {0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}};
static const GUID g_IID_IMMDeviceEnumerator  = {0xA95664D2, 0x9614, 0x4F35, {0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}};
static const GUID g_IID_IAudioClient         = {0x1CB9AD4C, 0xDBFA, 0x4C32, {0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}};
static const GUID g_IID_IAudioCaptureClient  = {0xC8ADBD64, 0xE71E, 0x48A0, {0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}};

static IAudioClient *g_audioClient = NULL;
static IAudioCaptureClient *g_captureClient = NULL;
static WAVEFORMATEX *g_audioFmt = NULL;
static int g_audioInit = 0;

static int audio_init(void) {
    if (g_audioInit) return 0;
    HRESULT hr;

    CoInitializeEx(NULL, COINIT_MULTITHREADED);

    IMMDeviceEnumerator *enumerator = NULL;
    hr = CoCreateInstance(&g_CLSID_MMDeviceEnumerator, NULL, CLSCTX_ALL,
                          &g_IID_IMMDeviceEnumerator, (void**)&enumerator);
    if (FAILED(hr)) return -1;

    IMMDevice *device = NULL;
    hr = IMMDeviceEnumerator_GetDefaultAudioEndpoint(enumerator, eRender, eConsole, &device);
    IUnknown_Release(enumerator);
    if (FAILED(hr)) return -2;

    hr = IMMDevice_Activate(device, &g_IID_IAudioClient, CLSCTX_ALL, NULL, (void**)&g_audioClient);
    IUnknown_Release(device);
    if (FAILED(hr)) return -3;

    hr = IAudioClient_GetMixFormat(g_audioClient, &g_audioFmt);
    if (FAILED(hr)) return -4;

    // Initialize in loopback mode (capture system audio output)
    REFERENCE_TIME bufDuration = 1000000; // 100ms buffer (10 * 100ns units)
    hr = IAudioClient_Initialize(g_audioClient, AUDCLNT_SHAREMODE_SHARED,
        AUDCLNT_STREAMFLAGS_LOOPBACK, bufDuration, 0, g_audioFmt, NULL);
    if (FAILED(hr)) return -5;

    hr = IAudioClient_GetService(g_audioClient, &g_IID_IAudioCaptureClient, (void**)&g_captureClient);
    if (FAILED(hr)) return -6;

    hr = IAudioClient_Start(g_audioClient);
    if (FAILED(hr)) return -7;

    g_audioInit = 1;
    return 0;
}

// audio_capture: reads available audio data into out_buf as 16-bit PCM mono.
// Returns number of bytes written, 0 = no data, negative = error.
static int audio_capture(unsigned char *out_buf, int out_cap) {
    if (!g_audioInit || !g_captureClient) return -1;

    UINT32 packetLen = 0;
    IAudioCaptureClient_GetNextPacketSize(g_captureClient, &packetLen);
    if (packetLen == 0) return 0;

    int total = 0;
    while (packetLen > 0) {
        BYTE *data = NULL;
        UINT32 frames = 0;
        DWORD flags = 0;
        HRESULT hr = IAudioCaptureClient_GetBuffer(g_captureClient, &data, &frames, &flags, NULL, NULL);
        if (FAILED(hr)) break;

        // Convert to int16 mono
        int channels = g_audioFmt->nChannels;
        int isFloat = (g_audioFmt->wFormatTag == 3) ||
                      (g_audioFmt->wFormatTag == 0xFFFE && g_audioFmt->cbSize >= 22);
        // WAVE_FORMAT_EXTENSIBLE: SubFormat starts at cbSize offset
        // For simplicity, WASAPI shared mode almost always returns float32
        int bytesPerSample = g_audioFmt->wBitsPerSample / 8;
        UINT32 i;
        for (i = 0; i < frames && total + 2 <= out_cap; i++) {
            float sample = 0;
            if (flags & AUDCLNT_BUFFERFLAGS_SILENT) {
                sample = 0;
            } else if (isFloat || bytesPerSample == 4) {
                // Float32 format (most common WASAPI shared mode)
                float sum = 0;
                int ch;
                for (ch = 0; ch < channels; ch++) {
                    sum += ((float*)data)[i * channels + ch];
                }
                sample = sum / channels;
            } else if (bytesPerSample == 2) {
                // Int16 format
                int sum = 0;
                int ch;
                for (ch = 0; ch < channels; ch++) {
                    sum += ((short*)data)[i * channels + ch];
                }
                short avg = (short)(sum / channels);
                out_buf[total] = (unsigned char)(avg & 0xFF);
                out_buf[total+1] = (unsigned char)((avg >> 8) & 0xFF);
                total += 2;
                continue;
            }
            // Soft clamp with tanh-like curve to reduce harsh clipping
            if (sample > 1.0f) sample = 1.0f;
            if (sample < -1.0f) sample = -1.0f;
            short s16 = (short)(sample * 32767.0f);
            out_buf[total] = (unsigned char)(s16 & 0xFF);
            out_buf[total+1] = (unsigned char)((s16 >> 8) & 0xFF);
            total += 2;
        }

        IAudioCaptureClient_ReleaseBuffer(g_captureClient, frames);
        IAudioCaptureClient_GetNextPacketSize(g_captureClient, &packetLen);
    }
    return total;
}

static void audio_close(void) {
    if (g_audioClient) {
        IAudioClient_Stop(g_audioClient);
        IUnknown_Release(g_audioClient);
        g_audioClient = NULL;
    }
    if (g_captureClient) {
        IUnknown_Release(g_captureClient);
        g_captureClient = NULL;
    }
    if (g_audioFmt) {
        CoTaskMemFree(g_audioFmt);
        g_audioFmt = NULL;
    }
    g_audioInit = 0;
}

static int audio_sample_rate(void) {
    return g_audioFmt ? (int)g_audioFmt->nSamplesPerSec : 48000;
}

// ── Audio v2 conversion (fixes the "crunched" sound) ────────────────────────
// The legacy audio_capture() above (frame 0x06) has three defects heard
// as crackle: (1) it treats WAVE_FORMAT_EXTENSIBLE as float whenever
// cbSize>=22 — a PCM-16/24/32 endpoint is then read as float and mangled;
// (2) it reads 4 bytes/sample regardless of the real sample size (over-read on
// 16/24-bit); (3) it truncates instead of rounding. audio_capture_v2 detects
// the real SubFormat, reads the correct width, rounds, and keeps stereo (up to
// 2 channels) so the viewer's 0x07 playhead scheduler gets clean samples.
static const GUID g_SUBTYPE_IEEE_FLOAT = {0x00000003,0x0000,0x0010,{0x80,0x00,0x00,0xaa,0x00,0x38,0x9b,0x71}};

static int fmt_is_float(const WAVEFORMATEX *f) {
    if (f->wFormatTag == WAVE_FORMAT_IEEE_FLOAT) return 1;
    if (f->wFormatTag == WAVE_FORMAT_EXTENSIBLE && f->cbSize >= 22) {
        const WAVEFORMATEXTENSIBLE *x = (const WAVEFORMATEXTENSIBLE*)f;
        return memcmp(&x->SubFormat, &g_SUBTYPE_IEEE_FLOAT, sizeof(GUID)) == 0;
    }
    return 0;
}

static float read_one_sample(const WAVEFORMATEX *f, int isF, const BYTE *p) {
    int bps = f->wBitsPerSample / 8;
    if (isF && bps == 4)  { float v; memcpy(&v, p, 4); return v; }
    if (!isF && bps == 2) { short v; memcpy(&v, p, 2); return v / 32768.0f; }
    if (!isF && bps == 3) { int v = (p[0] << 8) | (p[1] << 16) | (p[2] << 24); return (v >> 8) / 8388608.0f; }
    if (!isF && bps == 4) { int v; memcpy(&v, p, 4); return (float)(v / 2147483648.0); }
    return 0.0f;
}

// audio_capture_v2: drains the loopback buffer into interleaved s16le. Output
// channel count = min(source channels, 2), stored in *out_channels. Returns
// bytes written, 0 = no data, negative = error.
static int audio_capture_v2(unsigned char *out, int cap, int *out_channels) {
    if (!g_audioInit || !g_captureClient || !g_audioFmt) { if (out_channels) *out_channels = 2; return -1; }
    int srcCh = g_audioFmt->nChannels;
    int outCh = srcCh >= 2 ? 2 : 1;
    if (out_channels) *out_channels = outCh;
    int isF = fmt_is_float(g_audioFmt);
    int bps = g_audioFmt->wBitsPerSample / 8;
    int blk = g_audioFmt->nBlockAlign;
    if (blk <= 0) blk = srcCh * bps;

    UINT32 packetLen = 0;
    IAudioCaptureClient_GetNextPacketSize(g_captureClient, &packetLen);
    int total = 0;
    while (packetLen > 0) {
        BYTE *data = NULL; UINT32 frames = 0; DWORD flags = 0;
        if (FAILED(IAudioCaptureClient_GetBuffer(g_captureClient, &data, &frames, &flags, NULL, NULL))) break;
        for (UINT32 i = 0; i < frames && total + outCh * 2 <= cap; i++) {
            for (int c = 0; c < outCh; c++) {
                float s = 0.0f;
                if (!(flags & AUDCLNT_BUFFERFLAGS_SILENT))
                    s = read_one_sample(g_audioFmt, isF, data + (size_t)i * blk + (size_t)c * bps);
                if (s != s) s = 0.0f;                        // NaN guard
                if (s > 1.0f) s = 1.0f; else if (s < -1.0f) s = -1.0f;
                short s16 = (short)lrintf(s * 32767.0f);      // round, not truncate
                out[total]     = (unsigned char)(s16 & 0xFF);
                out[total + 1] = (unsigned char)((s16 >> 8) & 0xFF);
                total += 2;
            }
        }
        IAudioCaptureClient_ReleaseBuffer(g_captureClient, frames);
        IAudioCaptureClient_GetNextPacketSize(g_captureClient, &packetLen);
    }
    return total;
}
*/
import "C"
import (
	"log"
	"unsafe"
)

const audioMaxBuf = 128 * 1024 // 128KB audio buffer

var (
	audioInitDone bool
	audioBuf      = make([]byte, audioMaxBuf)
)

func audioInit() error {
	rc := int(C.audio_init())
	if rc != 0 {
		log.Printf("Audio capture init failed (code %d) — audio streaming disabled", rc)
		return nil // audio not available — not fatal
	}
	audioInitDone = true
	log.Printf("Audio capture initialized (sample rate: %d Hz)", int(C.audio_sample_rate()))
	return nil
}

func audioCapture() []byte {
	if !audioInitDone {
		return nil
	}
	n := int(C.audio_capture(
		(*C.uchar)(unsafe.Pointer(&audioBuf[0])),
		C.int(audioMaxBuf),
	))
	if n <= 0 {
		return nil
	}
	out := make([]byte, n)
	copy(out, audioBuf[:n])
	return out
}

func audioClose() {
	if audioInitDone {
		C.audio_close()
		audioInitDone = false
	}
}

func audioSampleRate() int {
	if !audioInitDone {
		return 48000
	}
	return int(C.audio_sample_rate())
}

// audioCaptureV2 drains the loopback buffer as interleaved s16le and returns
// the PCM plus its channel count (1 or 2). Used to build 0x07 audio frames.
func audioCaptureV2() ([]byte, int) {
	if !audioInitDone {
		return nil, 0
	}
	var ch C.int
	n := int(C.audio_capture_v2(
		(*C.uchar)(unsafe.Pointer(&audioBuf[0])),
		C.int(audioMaxBuf),
		&ch,
	))
	if n <= 0 {
		return nil, int(ch)
	}
	out := make([]byte, n)
	copy(out, audioBuf[:n])
	return out, int(ch)
}
