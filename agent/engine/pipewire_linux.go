//go:build linux && cgo

package engine

// PipeWire consumer of the portal's screen streams.
//
// libpipewire-0.3 is loaded with dlopen at run time, never linked: the
// engine (and the Oblireach agent built from it) keeps starting on machines
// without PipeWire (servers, old distributions), and the build needs no
// PipeWire headers (the build host only has the X11 ones). The few types
// used are declared below from PipeWire's stable ABI (spa_buffer,
// spa_data, spa_chunk, spa_meta*, pw_buffer, pw_stream_events version 0);
// pipewire_abi_linux_test.go checks them against the real headers on a
// machine that has them. The Flatpak runtimes (org.freedesktop.Platform,
// org.kde.Platform) ship libpipewire-0.3.so.0.
//
// One pw_thread_loop runs every stream of the session. Its process callback
// copies the newest frame (converted to BGRA) and the pointer (metadata) into
// a per-stream store under a mutex; Go reads that store on its own clock
// (captureFrame) and draws the pointer over the copy. Shared-memory buffers
// (MemFd / MemPtr) are what we ask for (spa_pod_linux.go); a DMA-BUF that
// arrives anyway is read through mmap when it is linear (DMA_BUF_IOCTL_SYNC
// around the copy) and skipped otherwise (a tiled NVIDIA buffer needs an EGL
// import: TODO).

/*
#cgo LDFLAGS: -ldl -lpthread

#include <dlfcn.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <unistd.h>

// ── PipeWire / SPA ABI (stable; see the file comment) ──────────────────────
struct ospa_pod { uint32_t size; uint32_t type; };
struct ospa_chunk { uint32_t offset; uint32_t size; int32_t stride; int32_t flags; };
struct ospa_data {
	uint32_t type; uint32_t flags; int64_t fd; uint32_t mapoffset; uint32_t maxsize;
	void *data; struct ospa_chunk *chunk;
};
struct ospa_meta { uint32_t type; uint32_t size; void *data; };
struct ospa_buffer { uint32_t n_metas; uint32_t n_datas; struct ospa_meta *metas; struct ospa_data *datas; };
struct opw_buffer { struct ospa_buffer *buffer; void *user_data; uint64_t size; uint64_t requested; };
struct ospa_point { int32_t x; int32_t y; };
struct ospa_rectangle { uint32_t width; uint32_t height; };
struct ospa_meta_header { uint32_t flags; uint32_t offset; int64_t pts; int64_t dts_offset; uint64_t seq; };
struct ospa_meta_bitmap { uint32_t format; struct ospa_rectangle size; int32_t stride; uint32_t offset; };
struct ospa_meta_cursor {
	uint32_t id; uint32_t flags; struct ospa_point position; struct ospa_point hotspot; uint32_t bitmap_offset;
};
// struct spa_hook is 6 pointers; the caller owns its storage.
struct ospa_hook_space { void *p[8]; };
struct opw_stream_events {
	uint32_t version; // 0: the callbacks up to drained
	void (*destroy)(void *data);
	void (*state_changed)(void *data, int old, int state, const char *error);
	void (*control_info)(void *data, uint32_t id, const void *control);
	void (*io_changed)(void *data, uint32_t id, void *area, uint32_t size);
	void (*param_changed)(void *data, uint32_t id, const struct ospa_pod *param);
	void (*add_buffer)(void *data, struct opw_buffer *buffer);
	void (*remove_buffer)(void *data, struct opw_buffer *buffer);
	void (*process)(void *data);
	void (*drained)(void *data);
};

#define OSPA_TYPE_Id 3
#define OSPA_TYPE_Rectangle 10
#define OSPA_TYPE_Object 15
#define OSPA_TYPE_Choice 19
#define OSPA_PARAM_Format 4
#define OSPA_FORMAT_VIDEO_format 0x20001
#define OSPA_FORMAT_VIDEO_modifier 0x20002
#define OSPA_FORMAT_VIDEO_size 0x20003
#define OSPA_DATA_MemPtr 1
#define OSPA_DATA_MemFd 2
#define OSPA_DATA_DmaBuf 3
#define OSPA_META_Header 1
#define OSPA_META_Cursor 5
#define OSPA_META_HEADER_FLAG_CORRUPTED (1u<<1)
#define OSPA_CHUNK_FLAG_CORRUPTED (1u<<0)
#define OPW_DIRECTION_INPUT 0
#define OPW_STREAM_FLAG_AUTOCONNECT (1u<<0)
#define OPW_STREAM_FLAG_MAP_BUFFERS (1u<<2)
#define OPW_STREAM_STATE_ERROR (-1)
#define OPW_STREAM_STATE_STREAMING 3

// video formats (enum spa_video_format)
#define OVF_RGBx 7
#define OVF_BGRx 8
#define OVF_xRGB 9
#define OVF_xBGR 10
#define OVF_RGBA 11
#define OVF_BGRA 12
#define OVF_ARGB 13
#define OVF_ABGR 14

// DMA-BUF sync (linux/dma-buf.h)
struct obli_dma_buf_sync { uint64_t flags; };
#define OBLI_DMA_BUF_SYNC_READ (1ULL << 0)
#define OBLI_DMA_BUF_SYNC_START (0ULL << 2)
#define OBLI_DMA_BUF_SYNC_END (1ULL << 2)
#define OBLI_DMA_BUF_IOCTL_SYNC _IOW('b', 0, struct obli_dma_buf_sync)

// ── libpipewire entry points ───────────────────────────────────────────────
static void *g_pw_lib;
static void (*p_pw_init)(int *, char ***);
static void *(*p_pw_thread_loop_new)(const char *, const void *);
static void *(*p_pw_thread_loop_get_loop)(void *);
static int (*p_pw_thread_loop_start)(void *);
static void (*p_pw_thread_loop_stop)(void *);
static void (*p_pw_thread_loop_destroy)(void *);
static void (*p_pw_thread_loop_lock)(void *);
static void (*p_pw_thread_loop_unlock)(void *);
static void *(*p_pw_context_new)(void *, void *, size_t);
static void (*p_pw_context_destroy)(void *);
static void *(*p_pw_context_connect_fd)(void *, int, void *, size_t);
static int (*p_pw_core_disconnect)(void *);
static void *(*p_pw_properties_new)(const char *, ...);
static void *(*p_pw_stream_new)(void *, const char *, void *);
static void (*p_pw_stream_add_listener)(void *, void *, const struct opw_stream_events *, void *);
static int (*p_pw_stream_connect)(void *, int, uint32_t, int, const struct ospa_pod **, uint32_t);
static int (*p_pw_stream_disconnect)(void *);
static void (*p_pw_stream_destroy)(void *);
static struct opw_buffer *(*p_pw_stream_dequeue_buffer)(void *);
static int (*p_pw_stream_queue_buffer)(void *, struct opw_buffer *);
static int (*p_pw_stream_update_params)(void *, const struct ospa_pod **, uint32_t);

static char g_pw_load_err[256];

static int obli_pw_load(void) {
	if (g_pw_lib) return 0;
	void *h = dlopen("libpipewire-0.3.so.0", RTLD_NOW | RTLD_LOCAL);
	if (!h) {
		const char *e = dlerror();
		snprintf(g_pw_load_err, sizeof g_pw_load_err, "%s", e ? e : "dlopen failed");
		return -1;
	}
#define PWSYM(name) do { *(void **)(&p_##name) = dlsym(h, #name); if (!p_##name) { \
	snprintf(g_pw_load_err, sizeof g_pw_load_err, "missing symbol %s", #name); dlclose(h); return -2; } } while (0)
	PWSYM(pw_init);
	PWSYM(pw_thread_loop_new);
	PWSYM(pw_thread_loop_get_loop);
	PWSYM(pw_thread_loop_start);
	PWSYM(pw_thread_loop_stop);
	PWSYM(pw_thread_loop_destroy);
	PWSYM(pw_thread_loop_lock);
	PWSYM(pw_thread_loop_unlock);
	PWSYM(pw_context_new);
	PWSYM(pw_context_destroy);
	PWSYM(pw_context_connect_fd);
	PWSYM(pw_core_disconnect);
	PWSYM(pw_properties_new);
	PWSYM(pw_stream_new);
	PWSYM(pw_stream_add_listener);
	PWSYM(pw_stream_connect);
	PWSYM(pw_stream_disconnect);
	PWSYM(pw_stream_destroy);
	PWSYM(pw_stream_dequeue_buffer);
	PWSYM(pw_stream_queue_buffer);
	PWSYM(pw_stream_update_params);
#undef PWSYM
	g_pw_lib = h;
	p_pw_init(NULL, NULL);
	return 0;
}

static const char *obli_pw_load_error(void) { return g_pw_load_err; }

// ── Format parsing ─────────────────────────────────────────────────────────
static uint32_t rd32(const uint8_t *p) { uint32_t v; memcpy(&v, p, 4); return v; }

// obli_parse_format reads video format, size and the presence of a modifier
// from a Format object. Returns 0 when format and size were found.
static int obli_parse_format(const uint8_t *p, uint32_t len, uint32_t *fmt, uint32_t *w, uint32_t *h, int *has_mod) {
	*fmt = 0; *w = 0; *h = 0; *has_mod = 0;
	if (len < 16) return -1;
	uint32_t size = rd32(p);
	if (rd32(p + 4) != OSPA_TYPE_Object || size < 8 || 8 + (uint64_t)size > len) return -1;
	uint32_t end = 8 + size, off = 16;
	while (off + 16 <= end) {
		uint32_t key = rd32(p + off);
		uint32_t vsize = rd32(p + off + 8), vtype = rd32(p + off + 12);
		uint32_t body = off + 16;
		if ((uint64_t)body + vsize > end) return -1;
		uint32_t val = body, vt = vtype, vs = vsize;
		if (vtype == OSPA_TYPE_Choice && vsize >= 16) {
			vs = rd32(p + body + 8);
			vt = rd32(p + body + 12);
			val = body + 16;
			if ((uint64_t)val + vs > (uint64_t)body + vsize) vs = 0;
		}
		if (key == OSPA_FORMAT_VIDEO_format && vt == OSPA_TYPE_Id && vs >= 4) *fmt = rd32(p + val);
		else if (key == OSPA_FORMAT_VIDEO_size && vt == OSPA_TYPE_Rectangle && vs >= 8) { *w = rd32(p + val); *h = rd32(p + val + 4); }
		else if (key == OSPA_FORMAT_VIDEO_modifier) *has_mod = 1;
		off = body + ((vsize + 7) & ~7u);
	}
	return (*fmt && *w && *h) ? 0 : -1;
}

// ── Per-stream store ───────────────────────────────────────────────────────
#define OBLI_PW_MAX 16

typedef struct {
	int idx;
	void *stream;
	struct ospa_hook_space hook;
	pthread_mutex_t mu;
	int state;
	char error[160];
	uint32_t fmt, w, h; int has_mod;
	uint8_t *frame; size_t cap; uint32_t fw, fh; uint64_t seq;
	int cur_visible; int32_t cur_x, cur_y, cur_hx, cur_hy;
	uint8_t *cur_bmp; size_t cur_cap; uint32_t cur_w, cur_h;
	uint64_t n_shm, n_dmabuf, n_skip;
} obli_pw_stream;

static void *g_loop, *g_ctx, *g_core;
static obli_pw_stream g_st[OBLI_PW_MAX];
static int g_nst;
static uint8_t *g_post_mem;
static const struct ospa_pod *g_post[8];
static uint32_t g_npost;

// convert_row copies one row of width px from src (format fmt) to BGRA dst.
static void convert_row(uint8_t *dst, const uint8_t *src, uint32_t width, uint32_t fmt) {
	uint32_t x;
	switch (fmt) {
	case OVF_BGRx: case OVF_BGRA:
		memcpy(dst, src, (size_t)width * 4);
		break;
	case OVF_RGBx: case OVF_RGBA:
		for (x = 0; x < width; x++) { dst[4*x] = src[4*x+2]; dst[4*x+1] = src[4*x+1]; dst[4*x+2] = src[4*x]; dst[4*x+3] = 255; }
		break;
	case OVF_xRGB: case OVF_ARGB:
		for (x = 0; x < width; x++) { dst[4*x] = src[4*x+3]; dst[4*x+1] = src[4*x+2]; dst[4*x+2] = src[4*x+1]; dst[4*x+3] = 255; }
		break;
	case OVF_xBGR: case OVF_ABGR:
		for (x = 0; x < width; x++) { dst[4*x] = src[4*x+1]; dst[4*x+1] = src[4*x+2]; dst[4*x+2] = src[4*x+3]; dst[4*x+3] = 255; }
		break;
	default:
		memset(dst, 0, (size_t)width * 4);
	}
}

static void store_cursor(obli_pw_stream *s, const struct ospa_meta *m) {
	if (m->size < sizeof(struct ospa_meta_cursor) || !m->data) return;
	const struct ospa_meta_cursor *c = (const struct ospa_meta_cursor *)m->data;
	if (c->id == 0) { s->cur_visible = 0; return; }
	s->cur_visible = 1;
	s->cur_x = c->position.x;
	s->cur_y = c->position.y;
	if (c->bitmap_offset < sizeof(struct ospa_meta_cursor) ||
	    (uint64_t)c->bitmap_offset + sizeof(struct ospa_meta_bitmap) > m->size) return; // no new bitmap
	const struct ospa_meta_bitmap *b = (const struct ospa_meta_bitmap *)((const uint8_t *)c + c->bitmap_offset);
	if (b->format == 0) return; // no new bitmap information
	if (b->offset < sizeof(struct ospa_meta_bitmap) || b->size.width == 0 || b->size.height == 0) {
		s->cur_w = s->cur_h = 0; // invisible pointer
		return;
	}
	uint32_t bw = b->size.width, bh = b->size.height;
	int32_t stride = b->stride ? b->stride : (int32_t)(bw * 4);
	if (bw > 1024 || bh > 1024 || stride < (int32_t)(bw * 4)) return;
	if ((uint64_t)c->bitmap_offset + b->offset + (uint64_t)stride * (bh - 1) + bw * 4 > m->size) return;
	size_t need = (size_t)bw * bh * 4;
	if (s->cur_cap < need) {
		uint8_t *nb = (uint8_t *)realloc(s->cur_bmp, need);
		if (!nb) return;
		s->cur_bmp = nb; s->cur_cap = need;
	}
	const uint8_t *src = (const uint8_t *)b + b->offset;
	uint32_t y;
	for (y = 0; y < bh; y++) {
		const uint8_t *sr = src + (size_t)y * stride;
		uint8_t *dr = s->cur_bmp + (size_t)y * bw * 4;
		uint32_t x;
		for (x = 0; x < bw; x++) {
			const uint8_t *sp = sr + 4 * x; uint8_t *dp = dr + 4 * x;
			switch (b->format) {
			case OVF_RGBA: case OVF_RGBx: dp[0] = sp[2]; dp[1] = sp[1]; dp[2] = sp[0]; dp[3] = sp[3]; break;
			case OVF_ARGB: case OVF_xRGB: dp[0] = sp[3]; dp[1] = sp[2]; dp[2] = sp[1]; dp[3] = sp[0]; break;
			case OVF_ABGR: case OVF_xBGR: dp[0] = sp[1]; dp[1] = sp[2]; dp[2] = sp[3]; dp[3] = sp[0]; break;
			default: dp[0] = sp[0]; dp[1] = sp[1]; dp[2] = sp[2]; dp[3] = sp[3]; break; // BGRA
			}
			if (b->format == OVF_RGBx || b->format == OVF_BGRx || b->format == OVF_xRGB || b->format == OVF_xBGR) dp[3] = 255;
		}
	}
	s->cur_w = bw; s->cur_h = bh;
	s->cur_hx = c->hotspot.x; s->cur_hy = c->hotspot.y;
}

static void store_frame(obli_pw_stream *s, struct ospa_data *d) {
	struct ospa_chunk *ch = d->chunk;
	if (!ch || ch->size == 0 || (ch->flags & OSPA_CHUNK_FLAG_CORRUPTED)) { s->n_skip++; return; }
	uint32_t w = s->w, h = s->h;
	if (!w || !h) { s->n_skip++; return; }
	int32_t stride = ch->stride ? ch->stride : (int32_t)(w * 4);
	if (stride < (int32_t)(w * 4)) { s->n_skip++; return; }
	const uint8_t *base = (const uint8_t *)d->data;
	void *mapped = NULL; size_t maplen = 0; int dmabuf = 0;
	if (d->type == OSPA_DATA_DmaBuf) {
		if (s->has_mod || d->fd < 0) { s->n_skip++; return; } // tiled: needs an EGL import
		dmabuf = 1;
		if (!base) {
			maplen = (size_t)d->maxsize + d->mapoffset;
			mapped = mmap(NULL, maplen, PROT_READ, MAP_SHARED, (int)d->fd, 0);
			if (mapped == MAP_FAILED) { s->n_skip++; return; }
			base = (const uint8_t *)mapped + d->mapoffset;
		}
		struct obli_dma_buf_sync sy = { OBLI_DMA_BUF_SYNC_START | OBLI_DMA_BUF_SYNC_READ };
		ioctl((int)d->fd, OBLI_DMA_BUF_IOCTL_SYNC, &sy);
	}
	if (!base) { s->n_skip++; return; }
	uint32_t off = d->maxsize ? (ch->offset % d->maxsize) : ch->offset;
	if (d->maxsize && (uint64_t)off + (uint64_t)stride * (h - 1) + (uint64_t)w * 4 > d->maxsize) {
		s->n_skip++;
	} else {
		size_t need = (size_t)w * h * 4;
		if (s->cap < need) {
			uint8_t *nf = (uint8_t *)realloc(s->frame, need);
			if (nf) { s->frame = nf; s->cap = need; }
		}
		if (s->cap >= need) {
			const uint8_t *src = base + off;
			uint32_t y;
			for (y = 0; y < h; y++) convert_row(s->frame + (size_t)y * w * 4, src + (size_t)y * stride, w, s->fmt);
			s->fw = w; s->fh = h; s->seq++;
			if (dmabuf) s->n_dmabuf++; else s->n_shm++;
		}
	}
	if (dmabuf) {
		struct obli_dma_buf_sync sy = { OBLI_DMA_BUF_SYNC_END | OBLI_DMA_BUF_SYNC_READ };
		ioctl((int)d->fd, OBLI_DMA_BUF_IOCTL_SYNC, &sy);
		if (mapped) munmap(mapped, maplen);
	}
}

static void on_process(void *data) {
	obli_pw_stream *s = (obli_pw_stream *)data;
	struct opw_buffer *b = p_pw_stream_dequeue_buffer(s->stream), *nb;
	if (!b) return;
	while ((nb = p_pw_stream_dequeue_buffer(s->stream)) != NULL) { // keep the newest
		p_pw_stream_queue_buffer(s->stream, b);
		b = nb;
	}
	struct ospa_buffer *sb = b->buffer;
	pthread_mutex_lock(&s->mu);
	int corrupted = 0;
	uint32_t i;
	for (i = 0; sb && i < sb->n_metas; i++) {
		struct ospa_meta *m = &sb->metas[i];
		if (m->type == OSPA_META_Cursor) store_cursor(s, m);
		else if (m->type == OSPA_META_Header && m->data && m->size >= sizeof(struct ospa_meta_header) &&
		         (((struct ospa_meta_header *)m->data)->flags & OSPA_META_HEADER_FLAG_CORRUPTED)) corrupted = 1;
	}
	if (sb && sb->n_datas > 0 && !corrupted) store_frame(s, &sb->datas[0]);
	else s->n_skip++;
	pthread_mutex_unlock(&s->mu);
	p_pw_stream_queue_buffer(s->stream, b);
}

static void on_param_changed(void *data, uint32_t id, const struct ospa_pod *param) {
	obli_pw_stream *s = (obli_pw_stream *)data;
	if (id != OSPA_PARAM_Format || !param) return;
	uint32_t fmt, w, h; int has_mod;
	if (obli_parse_format((const uint8_t *)param, 8 + param->size, &fmt, &w, &h, &has_mod) != 0) return;
	pthread_mutex_lock(&s->mu);
	s->fmt = fmt; s->w = w; s->h = h; s->has_mod = has_mod;
	pthread_mutex_unlock(&s->mu);
	p_pw_stream_update_params(s->stream, g_post, g_npost);
}

static void on_state_changed(void *data, int old, int state, const char *error) {
	obli_pw_stream *s = (obli_pw_stream *)data;
	pthread_mutex_lock(&s->mu);
	s->state = state;
	if (error) snprintf(s->error, sizeof s->error, "%s", error);
	pthread_mutex_unlock(&s->mu);
}

static const struct opw_stream_events g_events = {
	0, NULL, on_state_changed, NULL, NULL, on_param_changed, NULL, NULL, on_process, NULL,
};

static void obli_pw_free_streams(void) {
	int i;
	for (i = 0; i < OBLI_PW_MAX; i++) {
		free(g_st[i].frame);
		free(g_st[i].cur_bmp);
		pthread_mutex_destroy(&g_st[i].mu);
	}
	memset(g_st, 0, sizeof g_st);
	g_nst = 0;
	free(g_post_mem); g_post_mem = NULL; g_npost = 0;
}

static void obli_pw_close(void) {
	int i;
	if (!g_loop) return;
	p_pw_thread_loop_lock(g_loop);
	for (i = 0; i < g_nst; i++) {
		if (g_st[i].stream) {
			p_pw_stream_disconnect(g_st[i].stream);
			p_pw_stream_destroy(g_st[i].stream);
			g_st[i].stream = NULL;
		}
	}
	if (g_core) { p_pw_core_disconnect(g_core); g_core = NULL; }
	p_pw_thread_loop_unlock(g_loop);
	p_pw_thread_loop_stop(g_loop);
	if (g_ctx) { p_pw_context_destroy(g_ctx); g_ctx = NULL; }
	p_pw_thread_loop_destroy(g_loop);
	g_loop = NULL;
	obli_pw_free_streams();
}

// obli_pw_open connects to the portal's PipeWire remote (fd, owned from now
// on) and starts one stream per node. post: the Buffers + Meta parameters,
// concatenated (lens = their lengths), answered on every Format.
static int obli_pw_open(int fd, const uint32_t *nodes, int n, const uint8_t *enum_pod, uint32_t enum_len,
                        const uint8_t *post, const uint32_t *post_lens, int npost) {
	int i;
	if (obli_pw_load() != 0) { close(fd); return -1; }
	if (g_loop) obli_pw_close();
	if (n > OBLI_PW_MAX) n = OBLI_PW_MAX;
	if (npost > 8) npost = 8;
	size_t total = 0;
	for (i = 0; i < npost; i++) total += post_lens[i];
	g_post_mem = (uint8_t *)malloc(total + enum_len + 8);
	if (!g_post_mem) { close(fd); return -2; }
	memcpy(g_post_mem, post, total);
	size_t off = 0;
	for (i = 0; i < npost; i++) { g_post[i] = (const struct ospa_pod *)(g_post_mem + off); off += post_lens[i]; }
	g_npost = (uint32_t)npost;
	uint8_t *enum_copy = g_post_mem + ((total + 7) & ~(size_t)7);
	memcpy(enum_copy, enum_pod, enum_len);
	const struct ospa_pod *params[1] = { (const struct ospa_pod *)enum_copy };

	g_loop = p_pw_thread_loop_new("obli-pw", NULL);
	if (!g_loop) { close(fd); obli_pw_free_streams(); return -3; }
	g_ctx = p_pw_context_new(p_pw_thread_loop_get_loop(g_loop), NULL, 0);
	if (!g_ctx || p_pw_thread_loop_start(g_loop) < 0) { close(fd); obli_pw_close(); return -4; }
	p_pw_thread_loop_lock(g_loop);
	g_core = p_pw_context_connect_fd(g_ctx, fd, NULL, 0);
	if (!g_core) { p_pw_thread_loop_unlock(g_loop); obli_pw_close(); return -5; }
	for (i = 0; i < n; i++) {
		obli_pw_stream *s = &g_st[i];
		memset(s, 0, sizeof *s);
		pthread_mutex_init(&s->mu, NULL);
		s->idx = i;
		void *props = p_pw_properties_new("media.type", "Video", "media.category", "Capture",
		                                  "media.role", "Screen", (const char *)NULL);
		s->stream = p_pw_stream_new(g_core, "oblireach-capture", props);
		if (!s->stream) { p_pw_thread_loop_unlock(g_loop); g_nst = i; obli_pw_close(); return -6; }
		g_nst = i + 1;
		p_pw_stream_add_listener(s->stream, &s->hook, &g_events, s);
		if (p_pw_stream_connect(s->stream, OPW_DIRECTION_INPUT, nodes[i],
		                        OPW_STREAM_FLAG_AUTOCONNECT | OPW_STREAM_FLAG_MAP_BUFFERS, params, 1) < 0) {
			p_pw_thread_loop_unlock(g_loop); obli_pw_close(); return -7;
		}
	}
	for (; i < OBLI_PW_MAX; i++) pthread_mutex_init(&g_st[i].mu, NULL);
	p_pw_thread_loop_unlock(g_loop);
	return 0;
}

// blend_cursor draws the pointer over a BGRA frame.
static void blend_cursor(uint8_t *out, uint32_t fw, uint32_t fh, obli_pw_stream *s) {
	if (!s->cur_visible || !s->cur_bmp || !s->cur_w || !s->cur_h) return;
	int32_t x0 = s->cur_x - s->cur_hx, y0 = s->cur_y - s->cur_hy;
	uint32_t y;
	for (y = 0; y < s->cur_h; y++) {
		int32_t fy = y0 + (int32_t)y;
		if (fy < 0 || fy >= (int32_t)fh) continue;
		uint32_t x;
		for (x = 0; x < s->cur_w; x++) {
			int32_t fx = x0 + (int32_t)x;
			if (fx < 0 || fx >= (int32_t)fw) continue;
			const uint8_t *c = s->cur_bmp + ((size_t)y * s->cur_w + x) * 4;
			uint32_t a = c[3];
			if (!a) continue;
			uint8_t *d = out + ((size_t)fy * fw + (uint32_t)fx) * 4;
			d[0] = (uint8_t)((c[0] * a + d[0] * (255 - a)) / 255);
			d[1] = (uint8_t)((c[1] * a + d[1] * (255 - a)) / 255);
			d[2] = (uint8_t)((c[2] * a + d[2] * (255 - a)) / 255);
		}
	}
}

// obli_pw_read copies the newest frame of stream i (pointer drawn) into out.
// 0 ok, -1 no frame yet, -2 out too small (*w/*h say the size), -3 bad index.
static int obli_pw_read(int i, uint8_t *out, size_t cap, uint32_t *w, uint32_t *h, uint64_t *seq) {
	if (i < 0 || i >= g_nst) return -3;
	obli_pw_stream *s = &g_st[i];
	pthread_mutex_lock(&s->mu);
	*w = s->fw; *h = s->fh; *seq = s->seq;
	if (!s->frame || !s->fw || !s->fh) { pthread_mutex_unlock(&s->mu); return -1; }
	size_t need = (size_t)s->fw * s->fh * 4;
	if (cap < need) { pthread_mutex_unlock(&s->mu); return -2; }
	memcpy(out, s->frame, need);
	blend_cursor(out, s->fw, s->fh, s);
	pthread_mutex_unlock(&s->mu);
	return 0;
}

// obli_pw_info: frame size (negotiated size before the first frame), state.
static void obli_pw_info(int i, uint32_t *w, uint32_t *h, int *state, uint64_t *seq,
                         uint64_t *n_shm, uint64_t *n_dmabuf, uint64_t *n_skip, char *err, int errlen) {
	*w = *h = 0; *state = 0; *seq = 0; *n_shm = *n_dmabuf = *n_skip = 0; if (errlen > 0) err[0] = 0;
	if (i < 0 || i >= g_nst) return;
	obli_pw_stream *s = &g_st[i];
	pthread_mutex_lock(&s->mu);
	*w = s->fw ? s->fw : s->w; *h = s->fh ? s->fh : s->h;
	*state = s->state; *seq = s->seq;
	*n_shm = s->n_shm; *n_dmabuf = s->n_dmabuf; *n_skip = s->n_skip;
	if (errlen > 0) snprintf(err, errlen, "%s", s->error);
	pthread_mutex_unlock(&s->mu);
}

// Test hooks (pipewire_linux_test.go).
static int obli_pw_test_parse(const uint8_t *p, uint32_t len, uint32_t *fmt, uint32_t *w, uint32_t *h, int *has_mod) {
	return obli_parse_format(p, len, fmt, w, h, has_mod);
}
static void obli_pw_test_convert(uint8_t *dst, const uint8_t *src, uint32_t width, uint32_t fmt) {
	convert_row(dst, src, width, fmt);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

// pwSession is the running PipeWire consumer (one per process: the engine
// runs one session at a time).
type pwSession struct {
	n      int
	closed bool
}

var pwMu sync.Mutex

// pwAvailable reports whether libpipewire-0.3 can be loaded.
func pwAvailable() error {
	pwMu.Lock()
	defer pwMu.Unlock()
	if C.obli_pw_load() != 0 {
		return fmt.Errorf("PipeWire is not available: %s", C.GoString(C.obli_pw_load_error()))
	}
	return nil
}

// pwOpen connects to the portal's PipeWire remote and starts one stream per
// node. fd is handed over: the caller never closes it after this call.
func pwOpen(fd int, nodes []uint32) (*pwSession, error) {
	if len(nodes) == 0 {
		closeFD(fd)
		return nil, errors.New("no stream to open")
	}
	pwMu.Lock()
	defer pwMu.Unlock()
	enum := pwEnumFormatPod()
	posts := []spaPod{pwBuffersPod(), pwMetaHeaderPod(), pwMetaCursorPod()}
	var post []byte
	lens := make([]C.uint32_t, len(posts))
	for i, p := range posts {
		post = append(post, p...)
		lens[i] = C.uint32_t(len(p))
	}
	cEnum := C.CBytes(enum)
	defer C.free(cEnum)
	cPost := C.CBytes(post)
	defer C.free(cPost)
	cNodes := C.malloc(C.size_t(4 * len(nodes)))
	defer C.free(cNodes)
	for i, n := range nodes {
		*(*C.uint32_t)(unsafe.Pointer(uintptr(cNodes) + uintptr(4*i))) = C.uint32_t(n)
	}
	cLens := C.malloc(C.size_t(4 * len(lens)))
	defer C.free(cLens)
	for i, l := range lens {
		*(*C.uint32_t)(unsafe.Pointer(uintptr(cLens) + uintptr(4*i))) = l
	}
	rc := C.obli_pw_open(C.int(fd), (*C.uint32_t)(cNodes), C.int(len(nodes)),
		(*C.uint8_t)(cEnum), C.uint32_t(len(enum)),
		(*C.uint8_t)(cPost), (*C.uint32_t)(cLens), C.int(len(posts)))
	if rc != 0 {
		if rc == -1 {
			return nil, fmt.Errorf("PipeWire is not available: %s", C.GoString(C.obli_pw_load_error()))
		}
		return nil, fmt.Errorf("PipeWire connection failed (code %d)", int(rc))
	}
	n := len(nodes)
	if n > 16 {
		n = 16
	}
	return &pwSession{n: n}, nil
}

func (p *pwSession) info(i int) pwStreamInfo {
	var w, h C.uint32_t
	var st C.int
	var seq, nshm, ndma, nskip C.uint64_t
	var buf [160]C.char
	pwMu.Lock()
	if !p.closed {
		C.obli_pw_info(C.int(i), &w, &h, &st, &seq, &nshm, &ndma, &nskip, &buf[0], C.int(len(buf)))
	}
	pwMu.Unlock()
	return pwStreamInfo{W: int(w), H: int(h), State: int(st), Seq: uint64(seq),
		SHM: uint64(nshm), DMABuf: uint64(ndma), Skip: uint64(nskip), Err: C.GoString(&buf[0])}
}

// read copies the newest frame of stream i into buf (BGRA, pointer drawn).
func (p *pwSession) read(i int, buf []byte) (w, h int, seq uint64, err error) {
	var cw, ch C.uint32_t
	var cseq C.uint64_t
	pwMu.Lock()
	defer pwMu.Unlock()
	if p.closed {
		return 0, 0, 0, errors.New("PipeWire session closed")
	}
	var ptr *C.uint8_t
	if len(buf) > 0 {
		ptr = (*C.uint8_t)(unsafe.Pointer(&buf[0]))
	}
	rc := C.obli_pw_read(C.int(i), ptr, C.size_t(len(buf)), &cw, &ch, &cseq)
	w, h, seq = int(cw), int(ch), uint64(cseq)
	switch rc {
	case 0:
		return w, h, seq, nil
	case -1:
		return w, h, seq, errPWNoFrame
	case -2:
		return w, h, seq, errCaptureResized
	}
	return w, h, seq, fmt.Errorf("PipeWire read failed (code %d)", int(rc))
}

// waitFirstFrame waits until stream i delivered a frame (or failed).
func (p *pwSession) waitFirstFrame(i int, timeout time.Duration) pwStreamInfo {
	deadline := time.Now().Add(timeout)
	for {
		inf := p.info(i)
		if inf.Seq > 0 || inf.State == pwStateError || time.Now().After(deadline) {
			return inf
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *pwSession) close() {
	pwMu.Lock()
	defer pwMu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	C.obli_pw_close()
}

// pwTestParseFormat / pwTestConvert: the C parser and converter, for tests.
func pwTestParseFormat(p []byte) (spaFormatInfo, bool) {
	if len(p) == 0 {
		return spaFormatInfo{}, false
	}
	var f, w, h C.uint32_t
	var m C.int
	cp := C.CBytes(p)
	defer C.free(cp)
	rc := C.obli_pw_test_parse((*C.uint8_t)(cp), C.uint32_t(len(p)), &f, &w, &h, &m)
	return spaFormatInfo{Format: uint32(f), Width: uint32(w), Height: uint32(h), HasModifier: m != 0}, rc == 0
}

func pwTestConvert(src []byte, width int, format uint32) []byte {
	dst := make([]byte, width*4)
	C.obli_pw_test_convert((*C.uint8_t)(unsafe.Pointer(&dst[0])), (*C.uint8_t)(unsafe.Pointer(&src[0])),
		C.uint32_t(width), C.uint32_t(format))
	return dst
}
