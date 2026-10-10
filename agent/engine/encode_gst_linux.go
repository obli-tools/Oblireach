//go:build linux && cgo

package engine

// Hardware H.264 through GStreamer (NVENC via nvcodec, VA-API via va /
// gstreamer-vaapi), loaded with dlopen: libgstreamer-1.0 and libgstapp-1.0
// are part of the Flatpak runtimes, and their absence only removes these
// backends. Why GStreamer rather than libva directly: a raw VA-API H.264
// encoder needs the whole bitstream header packing (SPS / PPS / slice
// headers) and per-driver quirks; the va plugin has them, and the same code
// reaches NVENC (NVIDIA has no VA-API encoder) through nvcodec.

/*
#cgo LDFLAGS: -ldl

#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

typedef struct { uint32_t domain; int code; char *message; } OGError;

static void *g_gst, *g_gstapp;
static int  (*q_gst_init_check)(int *, char ***, OGError **);
static void *(*q_gst_element_factory_find)(const char *);
static void (*q_gst_object_unref)(void *);
static void *(*q_gst_parse_launch)(const char *, OGError **);
static void *(*q_gst_bin_get_by_name)(void *, const char *);
static int  (*q_gst_element_set_state)(void *, int);
static void *(*q_gst_element_get_bus)(void *);
static void *(*q_gst_bus_pop_filtered)(void *, int);
static void (*q_gst_message_parse_error)(void *, OGError **, char **);
static void (*q_gst_mini_object_unref)(void *);
static void *(*q_gst_buffer_new_allocate)(void *, size_t, void *);
static size_t (*q_gst_buffer_fill)(void *, size_t, const void *, size_t);
static size_t (*q_gst_buffer_get_size)(void *);
static size_t (*q_gst_buffer_extract)(void *, size_t, void *, size_t);
static int  (*q_gst_app_src_push_buffer)(void *, void *);
static void *(*q_gst_app_sink_try_pull_sample)(void *, uint64_t);
static void *(*q_gst_sample_get_buffer)(void *);
static void (*q_gst_util_set_object_arg)(void *, const char *, const char *);
static void (*q_g_error_free)(OGError *);
static void (*q_g_free)(void *);

static char g_gst_err[512];
static int g_gst_state; // 0 not tried, 1 ok, -1 failed

#define GSTSYM(h, name) do { *(void **)(&q_##name) = dlsym(h, #name); if (!q_##name) { \
	snprintf(g_gst_err, sizeof g_gst_err, "missing symbol %s", #name); return -2; } } while (0)

static int gst_load_syms(void) {
	GSTSYM(g_gst, gst_init_check);
	GSTSYM(g_gst, gst_element_factory_find);
	GSTSYM(g_gst, gst_object_unref);
	GSTSYM(g_gst, gst_parse_launch);
	GSTSYM(g_gst, gst_bin_get_by_name);
	GSTSYM(g_gst, gst_element_set_state);
	GSTSYM(g_gst, gst_element_get_bus);
	GSTSYM(g_gst, gst_bus_pop_filtered);
	GSTSYM(g_gst, gst_message_parse_error);
	GSTSYM(g_gst, gst_mini_object_unref);
	GSTSYM(g_gst, gst_buffer_new_allocate);
	GSTSYM(g_gst, gst_buffer_fill);
	GSTSYM(g_gst, gst_buffer_get_size);
	GSTSYM(g_gst, gst_buffer_extract);
	GSTSYM(g_gst, gst_util_set_object_arg);
	GSTSYM(g_gst, gst_sample_get_buffer);
	GSTSYM(g_gst, g_error_free);
	GSTSYM(g_gst, g_free);
	GSTSYM(g_gstapp, gst_app_src_push_buffer);
	GSTSYM(g_gstapp, gst_app_sink_try_pull_sample);
	return 0;
}

static int ogst_load(void) {
	if (g_gst_state) return g_gst_state > 0 ? 0 : -1;
	g_gst_state = -1;
	g_gst = dlopen("libgstreamer-1.0.so.0", RTLD_NOW | RTLD_GLOBAL);
	if (!g_gst) { const char *e = dlerror(); snprintf(g_gst_err, sizeof g_gst_err, "%s", e ? e : "dlopen failed"); return -1; }
	g_gstapp = dlopen("libgstapp-1.0.so.0", RTLD_NOW | RTLD_GLOBAL);
	if (!g_gstapp) { const char *e = dlerror(); snprintf(g_gst_err, sizeof g_gst_err, "%s", e ? e : "dlopen failed"); return -1; }
	if (gst_load_syms() != 0) return -1;
	OGError *err = NULL;
	if (!q_gst_init_check(NULL, NULL, &err)) {
		snprintf(g_gst_err, sizeof g_gst_err, "gst_init: %s", err && err->message ? err->message : "failed");
		if (err) q_g_error_free(err);
		return -1;
	}
	g_gst_state = 1;
	return 0;
}

static const char *ogst_error(void) { return g_gst_err; }

static int ogst_has(const char *factory) {
	if (ogst_load() != 0) return 0;
	void *f = q_gst_element_factory_find(factory);
	if (!f) return 0;
	q_gst_object_unref(f);
	return 1;
}

typedef struct { void *pipe, *src, *sink, *enc, *bus; } ogst_enc;

// ogst_bus_error: the first error message on the bus ("" none).
static void ogst_bus_error(ogst_enc *e, char *out, int n) {
	out[0] = 0;
	if (!e->bus) return;
	void *msg = q_gst_bus_pop_filtered(e->bus, 1 << 1); // GST_MESSAGE_ERROR
	if (!msg) return;
	OGError *err = NULL; char *dbg = NULL;
	q_gst_message_parse_error(msg, &err, &dbg);
	snprintf(out, n, "%s%s%s", err && err->message ? err->message : "error", dbg ? " / " : "", dbg ? dbg : "");
	if (err) q_g_error_free(err);
	if (dbg) q_g_free(dbg);
	q_gst_mini_object_unref(msg);
}

static void ogst_close(ogst_enc *e) {
	if (e->pipe) q_gst_element_set_state(e->pipe, 1); // GST_STATE_NULL
	if (e->bus) q_gst_object_unref(e->bus);
	if (e->enc) q_gst_object_unref(e->enc);
	if (e->src) q_gst_object_unref(e->src);
	if (e->sink) q_gst_object_unref(e->sink);
	if (e->pipe) q_gst_object_unref(e->pipe);
	memset(e, 0, sizeof *e);
}

static int ogst_open(ogst_enc *e, const char *desc, char *errout, int n) {
	memset(e, 0, sizeof *e);
	errout[0] = 0;
	if (ogst_load() != 0) { snprintf(errout, n, "%s", g_gst_err); return -1; }
	OGError *err = NULL;
	e->pipe = q_gst_parse_launch(desc, &err);
	if (err) { // a warning when the pipeline exists (unknown property), fatal otherwise
		snprintf(errout, n, "%s", err->message ? err->message : "parse error");
		q_g_error_free(err);
	}
	if (!e->pipe) return -2;
	e->src = q_gst_bin_get_by_name(e->pipe, "src");
	e->sink = q_gst_bin_get_by_name(e->pipe, "sink");
	e->enc = q_gst_bin_get_by_name(e->pipe, "enc");
	e->bus = q_gst_element_get_bus(e->pipe);
	if (!e->src || !e->sink || !e->enc) { snprintf(errout, n, "pipeline elements missing"); ogst_close(e); return -3; }
	if (q_gst_element_set_state(e->pipe, 4) == 0) { // PLAYING; 0 = GST_STATE_CHANGE_FAILURE
		ogst_bus_error(e, errout, n);
		ogst_close(e);
		return -4;
	}
	return 0;
}

// ogst_push: one BGRx frame in. 0 ok.
static int ogst_push(ogst_enc *e, const uint8_t *data, size_t size) {
	void *buf = q_gst_buffer_new_allocate(NULL, size, NULL);
	if (!buf) return -1;
	q_gst_buffer_fill(buf, 0, data, size);
	return q_gst_app_src_push_buffer(e->src, buf); // takes the buffer; GST_FLOW_OK = 0
}

// ogst_pull: one access unit out, waiting up to timeout_ns. Bytes, 0 none,
// -1 too large for out.
static int ogst_pull(ogst_enc *e, uint8_t *out, int cap, uint64_t timeout_ns) {
	void *s = q_gst_app_sink_try_pull_sample(e->sink, timeout_ns);
	if (!s) return 0;
	void *b = q_gst_sample_get_buffer(s);
	int r = 0;
	if (b) {
		size_t sz = q_gst_buffer_get_size(b);
		if (sz > (size_t)cap) r = -1;
		else r = (int)q_gst_buffer_extract(b, 0, out, sz);
	}
	q_gst_mini_object_unref(s);
	return r;
}

static void ogst_set(ogst_enc *e, const char *prop, const char *val) {
	if (e->enc) q_gst_util_set_object_arg(e->enc, prop, val);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"unsafe"
)

const (
	gstMaxAU       = 4 * 1024 * 1024
	gstPullTimeout = 30_000_000 // ns: the hardware answers within a few ms
)

var gstMu sync.Mutex

// gstProbe: whether a family can run (GStreamer, the plumbing elements and
// one of the family's encoders exist).
func gstProbe(f gstFamily) error {
	_, err := gstPickElement(f)
	return err
}

func gstPickElement(f gstFamily) (string, error) {
	gstMu.Lock()
	defer gstMu.Unlock()
	if C.ogst_load() != 0 {
		return "", fmt.Errorf("GStreamer is not available: %s", C.GoString(C.ogst_error()))
	}
	for _, name := range gstRequiredElements {
		if !gstHasLocked(name) {
			return "", fmt.Errorf("GStreamer element %s missing", name)
		}
	}
	for _, name := range gstFamilyElements[f] {
		if gstHasLocked(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no %s H.264 encoder element", f)
}

func gstHasLocked(name string) bool {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	return C.ogst_has(cs) == 1
}

type gstEncoder struct {
	family  gstFamily
	element string
	e       C.ogst_enc
	open    bool
	out     []byte
	w, h    int
}

func newGstEncoder(f gstFamily) h264Encoder { return &gstEncoder{family: f} }

func (g *gstEncoder) init(w, h, fps, bitrate int) error {
	el, err := gstPickElement(g.family)
	if err != nil {
		return err
	}
	desc := gstH264Pipeline(el, w, h, fps, bitrate/1000)
	cs := C.CString(desc)
	defer C.free(unsafe.Pointer(cs))
	var msg [512]C.char
	gstMu.Lock()
	rc := C.ogst_open(&g.e, cs, &msg[0], C.int(len(msg)))
	gstMu.Unlock()
	text := C.GoString(&msg[0])
	if rc != 0 {
		return fmt.Errorf("%s pipeline failed (code %d): %s", el, int(rc), text)
	}
	if text != "" {
		log.Printf("encoder: %s pipeline: %s", el, text)
	}
	g.element, g.open, g.w, g.h = el, true, w, h
	g.out = make([]byte, gstMaxAU)
	return nil
}

func (g *gstEncoder) encode(bgra []byte, w, h int, tsMs int64) ([]byte, error) {
	if !g.open {
		return nil, errors.New("encoder not open")
	}
	if w != g.w || h != g.h || len(bgra) < w*h*4 {
		return nil, errors.New("frame size changed")
	}
	if rc := C.ogst_push(&g.e, (*C.uint8_t)(unsafe.Pointer(&bgra[0])), C.size_t(w*h*4)); rc != 0 {
		return nil, fmt.Errorf("%s: push failed (flow %d): %s", g.element, int(rc), g.busError())
	}
	n := C.ogst_pull(&g.e, (*C.uint8_t)(unsafe.Pointer(&g.out[0])), C.int(len(g.out)), C.uint64_t(gstPullTimeout))
	if n < 0 {
		return nil, fmt.Errorf("%s: access unit too large", g.element)
	}
	if n == 0 {
		if e := g.busError(); e != "" {
			return nil, fmt.Errorf("%s: %s", g.element, e)
		}
		return nil, nil
	}
	return append([]byte(nil), g.out[:n]...), nil
}

func (g *gstEncoder) busError() string {
	var msg [512]C.char
	C.ogst_bus_error(&g.e, &msg[0], C.int(len(msg)))
	return C.GoString(&msg[0])
}

func (g *gstEncoder) setBitrate(bps int) {
	if !g.open {
		return
	}
	p := C.CString("bitrate")
	v := C.CString(strconv.Itoa(bps / 1000))
	C.ogst_set(&g.e, p, v)
	C.free(unsafe.Pointer(p))
	C.free(unsafe.Pointer(v))
}

func (g *gstEncoder) close() {
	if g.open {
		C.ogst_close(&g.e)
		g.open = false
	}
}
