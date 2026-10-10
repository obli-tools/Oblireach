/* Oblireach RDP host helper (rdphost) — protocol 1.
 *
 * Connects to an RDP server with FreeRDP 3, renders the session into the GDI
 * framebuffer, converts BGRA->I420 (FreeRDP primitives) and encodes H.264
 * Annex-B (libx264). The Oblireach Desktop shows the stream in its existing
 * ObliReach viewer (tabs, mosaic, detach), exactly like a remote session.
 * Derived from the Obliance VM console helper (agent/vmconsole/vmconsole.c):
 * same framing, same input messages, same status machine, plus what a real
 * RDP connection needs (target, credentials, certificate trust, dynamic
 * resolution, clipboard, cursor shapes).
 *
 * Portable C: WinPR provides the threads / events / locks on every platform;
 * only stdin reading and the stdout duplication are per-OS. Built on Linux
 * today (build-linux.sh); a Windows (MSYS2 mingw64) build needs no code change.
 *
 * ── stdout: framed, [u8 type][u32 LE length][payload] ──────────────────────
 *   'I' init    : u32 w, u32 h (re-sent on every size change / refresh)
 *   'F' frame   : one H.264 Annex-B access unit
 *   'S' status  : JSON object (UTF-8):
 *       {"state":"waiting_config"}                  (before the connect line)
 *       {"state":"connecting","host":"..","port":N}
 *       {"state":"cert","host","port","commonName","subject","issuer",
 *        "fingerprint","hostMismatch":b,"gateway":b,"changed":b,"oldFingerprint"}
 *         -> the helper WAITS for {"type":"cert_reply","accept":b}
 *       {"state":"credentials","reason":"nla|gateway|tls|rdp|...","username","domain"}
 *         -> the helper WAITS for {"type":"credentials",...} (or "cancel":true)
 *       {"state":"connected","width":W,"height":H,"unicode":b,"displayControl":b,
 *        "clipboard":b,"gfx":b}
 *       {"state":"display_control","ready":true}     (DISP caps received: resize works)
 *       {"state":"resize_unsupported"}               (no DISP channel: the page scales)
 *       {"state":"reconnecting","attempt":N,"reason":"network|resize"}
 *       {"state":"typing","done":n,"total":m[,"skipped":k]} / {"state":"typing_cancelled",...}
 *       {"state":"ended","code":"<code>","detail":"<FreeRDP name>","message":"..",
 *        "error":"0x%08X","errinfo":"0x%08X"}
 *         code: config_invalid | dns_failed | unreachable | security_failed |
 *               nla_required | auth_failed | auth_cancelled | password_expired |
 *               account_restricted | kerberos_failed | cert_rejected |
 *               connect_failed | other_connection | server_disconnected |
 *               logged_off | idle_timeout | logon_timeout | denied |
 *               license_error | reconnect_failed | encoder_failed |
 *               user_disconnect | stdin_closed
 *   'P' pointer : u8 kind (0 hidden, 1 system default, 2 shape)
 *                 kind 2: u16 hotX, u16 hotY, u16 w, u16 h, w*h*4 BGRA (straight alpha)
 *   'C' clip    : remote clipboard text, UTF-8 (sent when the server announces text)
 *
 * ── stdin: one JSON object per line (<= 4 MiB) ─────────────────────────────
 *   FIRST line (anything else is ignored until it arrives):
 *     {"type":"connect","host":"..","port":3389,"username":"..","domain":"..",
 *      "password":"..",                  (from the vault; never argv / env / file)
 *      "security":"auto|nla|tls|rdp","width":W,"height":H,
 *      "desktopScale":100,"deviceScale":100,"fps":30,"maxKbps":0 (0 = by size),
 *      "profile":"high|baseline","keyboardLayout":"0000040C",
 *      "knownFingerprints":["sha256 fingerprint",..] (TOFU: accepted silently),
 *      "certTimeoutMs":120000,"clipboard":true,"audio":"off|remote|local",
 *      "resizeReconnect":false (no DISP channel: reconnect at the new size),
 *      "console":false,"restrictedAdmin":false,"clientHostname":"..",
 *      "kerberosRealm":"..","kdcUrl":"..","configDir":"..",
 *      "gateway":{"host","port":443,"username","domain","password",
 *                 "sameCredentials":b,"transport":"auto|http|rpc","bypassLocal":b},
 *      "autoReconnect":true}
 *   then:
 *     {"type":"mouse","action":"move|down|up|scroll|hscroll","x","y","button","delta"}
 *     {"type":"key","action":"down|up","code":"KeyA","key":"a"}
 *     {"type":"type_text","text":"..."} {"type":"type_cancel"}
 *     {"type":"sas"}  (Ctrl+Alt+End: the RDP secure attention sequence)
 *     {"type":"release_all"} {"type":"refresh"} {"type":"sync","caps":b,"num":b,"scroll":b}
 *     {"type":"resize","width":W,"height":H[,"desktopScale":N,"deviceScale":N]}
 *     {"type":"clipboard_set","text":"..."}  (local text offered to the server)
 *     {"type":"bitrate","kbps":N} {"type":"cert_reply","accept":b}
 *     {"type":"credentials","username","domain","password"[,"cancel":true]}
 *     {"type":"disconnect"}
 *
 * Exit codes: 0 normal end (disconnect / stdin EOF), 2 usage or no valid
 * connect line, 3 init, 4 first connection failed, 6 output broken,
 * 7 connection lost for good.
 *
 * Run:        rdphost              (stream mode; the connect line on stdin)
 * Self-test:  rdphost --selftest
 * Version:    rdphost --version
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stdarg.h>
#include <ctype.h>

#ifdef _WIN32
#include <io.h>
#include <fcntl.h>
#include <winsock2.h>
#include <windows.h>
#else
#include <unistd.h>
#include <fcntl.h>
#include <signal.h>
#include <errno.h>
#ifdef __linux__
#include <sys/prctl.h>
#endif
#endif

#include <winpr/crt.h>
#include <winpr/synch.h>
#include <winpr/thread.h>
#include <winpr/sysinfo.h>
#include <winpr/interlocked.h>
#include <winpr/string.h>
#include <winpr/json.h>
#include <winpr/wlog.h>
#include <winpr/collections.h>
#include <freerdp/freerdp.h>
#include <freerdp/client.h>
#include <freerdp/gdi/gdi.h>
#include <freerdp/settings.h>
#include <freerdp/codec/color.h>
#include <freerdp/primitives.h>
#include <freerdp/input.h>
#include <freerdp/error.h>
#include <freerdp/event.h>
#include <freerdp/graphics.h>
#include <freerdp/update.h>
#include <freerdp/version.h>
#include <freerdp/client/cliprdr.h>
#include <freerdp/client/disp.h>
#include <freerdp/channels/cliprdr.h>
#include <freerdp/channels/disp.h>
#include <x264.h>

#define RH_PROTOCOL 1
#define MAX_LINE (4 * 1024 * 1024)
#define MAX_CLIP_TEXT (1024 * 1024)
#define MAX_TYPE_CHARS 20000
#define RECONNECT_EVERY_MS 3000
#define RECONNECT_WINDOW_MS 60000
#define CONFIG_WAIT_MS 30000
#define RESIZE_DEBOUNCE_MS 250
#define MIN_KBPS 300
#define CF_UNICODETEXT_ID 13
#define CF_TEXT_ID 1

#ifndef KBD_SYNC_SCROLL_LOCK
#define KBD_SYNC_SCROLL_LOCK 0x01
#define KBD_SYNC_NUM_LOCK 0x02
#define KBD_SYNC_CAPS_LOCK 0x04
#endif

/* ── configuration (the connect line) ───────────────────────────────────── */

typedef struct
{
	char* host;
	int port;
	char* username;
	char* domain;
	char* password;
	char security[8]; /* auto | nla | tls | rdp */
	int width, height;
	int desktopScale, deviceScale;
	int fps;
	int maxKbps;
	char profile[12];
	UINT32 keyboardLayout;
	char** knownFp;
	int nKnownFp;
	int certTimeoutMs;
	BOOL clipboard;
	BOOL audioRemote; /* sound stays on the server */
	BOOL audioLocal;  /* RDPSND played here (FreeRDP's PulseAudio / ALSA backend) */
	BOOL resizeReconnect; /* no DISP channel: a resize reconnects at the new size */
	BOOL console;
	BOOL restrictedAdmin;
	BOOL autoReconnect;
	char* clientHostname;
	char* kerberosRealm;
	char* kdcUrl;
	char* configDir;
	/* gateway */
	BOOL gwEnabled;
	char* gwHost;
	int gwPort;
	char* gwUser;
	char* gwDomain;
	char* gwPassword;
	BOOL gwSame;
	BOOL gwBypassLocal;
	char gwTransport[8]; /* auto | http | rpc */
} RhConfig;

typedef struct
{
	rdpClientContext common;
	DispClientContext* disp;
	CliprdrClientContext* cliprdr;
} rhContext;

typedef struct
{
	rdpPointer pointer;
	BYTE* bgra; /* w*h*4, straight alpha */
} rhPointer;

/* ── global state ───────────────────────────────────────────────────────── */

static FILE* g_out = NULL;
static volatile LONG g_out_broken = 0;
static volatile LONG g_stdin_closed = 0;
static volatile LONG g_quit_req = 0;
static CRITICAL_SECTION g_outLock;
static CRITICAL_SECTION g_inLock;
static CRITICAL_SECTION g_clipLock;
static HANDLE g_wake = NULL;       /* main loop: something to do */
static HANDLE g_cfg_event = NULL;  /* the connect line arrived */
static HANDLE g_reply_event = NULL;
static RhConfig g_cfg;
static volatile LONG g_cfg_ok = 0;

static x264_t* g_enc = NULL;
static x264_picture_t g_picin;
static int g_encw = 0, g_ench = 0;
static primitives_t* g_prims = NULL;
static int g_force_idr = 0;
static int g_init_sent = 0;
static volatile LONG g_refresh_req = 0;
static volatile LONG g_dirty = 0;
static ULONGLONG g_last_encode = 0;
static long long g_pts = 0;
static int g_frames = 0;
static int g_fps = 30;
static int g_frame_interval = 33;
static char g_profile[12] = "high";
static int g_kbps = 0, g_kbps_cap = 0;
static volatile LONG g_kbps_req = 0;
static volatile LONG g_slow_writes = 0;

static freerdp* g_instance = NULL;
static rdpInput* g_input = NULL;
static volatile LONG g_desk_w = 0, g_desk_h = 0;
static BOOL g_unicode_ok = FALSE;
static int g_connected = 0;

static volatile LONG g_resize_pending = 0;
static volatile LONG g_resize_w = 0, g_resize_h = 0, g_resize_ds = 0, g_resize_dvs = 0;
static volatile LONG g_disp_ready = 0;
static int g_resize_unsupported_sent = 0;
static ULONGLONG g_connected_at = 0;
static ULONGLONG g_last_resize = 0;

static int g_cert_rejected = 0;
static int g_cred_cancelled = 0;

/* reply mailbox (certificate / credentials prompts) */
static volatile LONG g_reply_kind = 0; /* 0 none, 1 cert, 2 credentials */
static int g_reply_accept = 0;
static int g_reply_cancel = 0;
static char* g_reply_user = NULL;
static char* g_reply_domain = NULL;
static char* g_reply_pass = NULL;

/* clipboard */
static char* g_clip_local = NULL; /* UTF-8 text offered to the server */
static int g_clip_ready = 0;

/* last pointer message (re-sent on refresh) */
static BYTE* g_ptr_msg = NULL;
static size_t g_ptr_len = 0;

/* ── test recorder (selftest: events recorded instead of sent) ───────────── */

typedef struct
{
	char kind; /* 'k' key, 'u' unicode, 'm' mouse, 'x' extended mouse, 'p' pause, 's' sync */
	UINT16 flags;
	UINT16 a, b;
} TestEv;
static int g_testmode = 0;
static TestEv g_tev[4096];
static int g_ntev = 0;

static void rec_ev(char kind, UINT16 flags, UINT16 a, UINT16 b)
{
	if (g_ntev < (int)(sizeof(g_tev) / sizeof(g_tev[0])))
	{
		g_tev[g_ntev].kind = kind;
		g_tev[g_ntev].flags = flags;
		g_tev[g_ntev].a = a;
		g_tev[g_ntev].b = b;
		g_ntev++;
	}
}

/* ── small helpers ──────────────────────────────────────────────────────── */

static void secure_wipe(void* p, size_t n)
{
	volatile unsigned char* v = (volatile unsigned char*)p;
	while (n--)
		*v++ = 0;
}

static void free_secret(char** s)
{
	if (*s)
	{
		secure_wipe(*s, strlen(*s));
		free(*s);
		*s = NULL;
	}
}

static char* dup_or_null(const char* s)
{
	return (s && *s) ? _strdup(s) : NULL;
}

static void wake_main(void)
{
	if (g_wake)
		SetEvent(g_wake);
}

/* ── stream framing ─────────────────────────────────────────────────────── */

static void mark_out_broken(void)
{
	if (InterlockedExchange(&g_out_broken, 1) == 0 && g_instance && g_instance->context)
		freerdp_abort_connect_context(g_instance->context);
}

/* Writes one framed message (thread-safe). Returns the time spent writing. */
static ULONGLONG emit_msg(char type, const void* a, size_t alen, const void* b, size_t blen)
{
	if (!g_out || g_out_broken)
		return 0;
	const uint32_t len = (uint32_t)(alen + blen);
	unsigned char hdr[5] = { (unsigned char)type, (unsigned char)(len), (unsigned char)(len >> 8),
		                     (unsigned char)(len >> 16), (unsigned char)(len >> 24) };
	EnterCriticalSection(&g_outLock);
	const ULONGLONG t0 = GetTickCount64();
	BOOL ok = fwrite(hdr, 1, 5, g_out) == 5;
	if (ok && alen)
		ok = fwrite(a, 1, alen, g_out) == alen;
	if (ok && blen)
		ok = fwrite(b, 1, blen, g_out) == blen;
	if (ok)
		ok = fflush(g_out) == 0;
	const ULONGLONG spent = GetTickCount64() - t0;
	LeaveCriticalSection(&g_outLock);
	if (!ok)
		mark_out_broken();
	return spent;
}

/* Status objects are written by hand: integers must stay integers ("2", not
 * json-c's "2.0", which a Go int field refuses). */
typedef struct
{
	char buf[8192];
	size_t n;
	int first;
} JB;

static void jb_put(JB* j, const char* s, size_t len)
{
	if (j->n + len + 2 >= sizeof(j->buf)) /* keep room for the closing brace */
		len = sizeof(j->buf) - j->n - 2;
	memcpy(j->buf + j->n, s, len);
	j->n += len;
	j->buf[j->n] = '\0';
}

static void jb_key(JB* j, const char* k)
{
	if (!j->first)
		jb_put(j, ",", 1);
	j->first = 0;
	jb_put(j, "\"", 1);
	jb_put(j, k, strlen(k));
	jb_put(j, "\":", 2);
}

/* JSON string escape (UTF-8 passes through, control characters escaped). */
static void jb_escaped(JB* j, const char* v)
{
	jb_put(j, "\"", 1);
	for (const unsigned char* p = (const unsigned char*)(v ? v : ""); *p; p++)
	{
		if (j->n + 8 >= sizeof(j->buf))
			break;
		char tmp[8];
		if (*p == '"' || *p == '\\')
		{
			tmp[0] = '\\';
			tmp[1] = (char)*p;
			jb_put(j, tmp, 2);
		}
		else if (*p < 0x20 || *p == 0x7F)
		{
			const int m = snprintf(tmp, sizeof(tmp), "\\u%04x", *p);
			jb_put(j, tmp, (size_t)m);
		}
		else
			jb_put(j, (const char*)p, 1);
	}
	jb_put(j, "\"", 1);
}

static JB* status_new(const char* state)
{
	JB* j = (JB*)calloc(1, sizeof(JB));
	if (!j)
		return NULL;
	j->first = 1;
	jb_put(j, "{", 1);
	jb_key(j, "state");
	jb_escaped(j, state);
	return j;
}

static void jadd_str(JB* o, const char* k, const char* v)
{
	if (!o)
		return;
	jb_key(o, k);
	jb_escaped(o, v);
}
static void jadd_int(JB* o, const char* k, long long v)
{
	if (!o)
		return;
	char tmp[32];
	const int m = snprintf(tmp, sizeof(tmp), "%lld", v);
	jb_key(o, k);
	jb_put(o, tmp, (size_t)m);
}
static void jadd_bool(JB* o, const char* k, BOOL v)
{
	if (!o)
		return;
	jb_key(o, k);
	jb_put(o, v ? "true" : "false", v ? 4 : 5);
}

/* Emits a status object; the object is consumed. */
static void emit_status_obj(JB* o)
{
	if (!o)
		return;
	jb_put(o, "}", 1);
	fprintf(stderr, "status %s\n", o->buf);
	emit_msg('S', o->buf, o->n, NULL, 0);
	free(o);
}

static void emit_simple(const char* state)
{
	emit_status_obj(status_new(state));
}

static void emit_ended(const char* code, const char* detail, const char* message, UINT32 err, UINT32 info)
{
	char e1[16], e2[16];
	snprintf(e1, sizeof(e1), "0x%08X", (unsigned)err);
	snprintf(e2, sizeof(e2), "0x%08X", (unsigned)info);
	JB* o = status_new("ended");
	jadd_str(o, "code", code);
	jadd_str(o, "detail", detail);
	jadd_str(o, "message", message);
	jadd_str(o, "error", e1);
	jadd_str(o, "errinfo", e2);
	emit_status_obj(o);
}

static void emit_init(int w, int h)
{
	if (g_init_sent)
		return;
	unsigned char p[8] = { (unsigned char)w, (unsigned char)(w >> 8), (unsigned char)(w >> 16),
		                   (unsigned char)(w >> 24), (unsigned char)h, (unsigned char)(h >> 8),
		                   (unsigned char)(h >> 16), (unsigned char)(h >> 24) };
	emit_msg('I', p, 8, NULL, 0);
	g_init_sent = 1;
	fprintf(stderr, "init %dx%d\n", w, h);
}

static void emit_frame(const unsigned char* data, int len)
{
	const ULONGLONG spent = emit_msg('F', data, (size_t)len, NULL, 0);
	/* A slow write = the reader (Desktop -> WebSocket -> viewer) lags. */
	if (spent > (ULONGLONG)(g_frame_interval * 2))
		InterlockedIncrement(&g_slow_writes);
}

/* ── encoder ────────────────────────────────────────────────────────────── */

/* Default ceiling: about 3 kbit per 1000 pixels at 30 fps (1080p ~ 6 Mbit/s). */
static int default_kbps(int w, int h, int fps)
{
	long long k = (long long)w * h * 3 / 1000;
	k = k * fps / 30;
	if (k < 1500)
		k = 1500;
	if (k > 20000)
		k = 20000;
	return (int)k;
}

static BOOL setup_encoder(int w, int h)
{
	x264_param_t p;
	if (x264_param_default_preset(&p, "ultrafast", "zerolatency") < 0)
		return FALSE;
	p.i_csp = X264_CSP_I420;
	p.i_width = w;
	p.i_height = h;
	p.i_fps_num = (uint32_t)g_fps;
	p.i_fps_den = 1;
	p.i_keyint_max = g_fps; /* a keyframe about every second */
	p.b_annexb = 1;
	p.b_repeat_headers = 1;
	p.i_log_level = X264_LOG_NONE;
	if (g_kbps_cap <= 0)
		g_kbps_cap = default_kbps(w, h, g_fps);
	if (g_kbps <= 0 || g_kbps > g_kbps_cap)
		g_kbps = g_kbps_cap;
	p.rc.i_rc_method = X264_RC_CRF;
	p.rc.f_rf_constant = 23;
	p.rc.i_vbv_max_bitrate = g_kbps;
	p.rc.i_vbv_buffer_size = g_kbps; /* ~1 s of buffer */
	if (x264_param_apply_profile(&p, g_profile) < 0)
		return FALSE;
	g_enc = x264_encoder_open(&p);
	if (!g_enc)
		return FALSE;
	if (x264_picture_alloc(&g_picin, X264_CSP_I420, w, h) < 0)
	{
		x264_encoder_close(g_enc);
		g_enc = NULL;
		return FALSE;
	}
	if (!g_prims)
		g_prims = primitives_get();
	g_encw = w;
	g_ench = h;
	fprintf(stderr, "encoder %dx%d %d fps, max %d kbit/s, profile %s\n", w, h, g_fps, g_kbps, g_profile);
	return TRUE;
}

static void close_encoder(void)
{
	if (g_enc)
	{
		x264_picture_clean(&g_picin);
		x264_encoder_close(g_enc);
		g_enc = NULL;
	}
	g_encw = g_ench = 0;
}

static void apply_kbps(int kbps)
{
	if (kbps < MIN_KBPS)
		kbps = MIN_KBPS;
	if (g_kbps_cap > 0 && kbps > g_kbps_cap)
		kbps = g_kbps_cap;
	if (kbps == g_kbps)
		return;
	g_kbps = kbps;
	if (g_enc)
	{
		x264_param_t p;
		x264_encoder_parameters(g_enc, &p);
		p.rc.i_vbv_max_bitrate = kbps;
		p.rc.i_vbv_buffer_size = kbps;
		if (x264_encoder_reconfig(g_enc, &p) < 0)
			fprintf(stderr, "bitrate reconfig refused (%d kbit/s)\n", kbps);
	}
	fprintf(stderr, "bitrate %d kbit/s\n", kbps);
}

/* Encodes the current framebuffer (main thread only). */
static BOOL encode_now(rdpContext* ctx)
{
	rdpGdi* gdi = ctx->gdi;
	InterlockedExchange(&g_dirty, 0);
	g_last_encode = GetTickCount64();
	if (!gdi || !gdi->primary_buffer)
		return TRUE;
	rdp_update_lock(ctx->update);
	InterlockedExchange(&g_desk_w, (LONG)gdi->width);
	InterlockedExchange(&g_desk_h, (LONG)gdi->height);
	/* x264 I420 needs even dimensions: drop the last odd row / column. */
	const int w = (int)gdi->width & ~1;
	const int h = (int)gdi->height & ~1;
	if (w < 2 || h < 2)
	{
		rdp_update_unlock(ctx->update);
		return TRUE;
	}
	if (InterlockedExchange(&g_refresh_req, 0))
	{
		g_force_idr = 1;
		g_init_sent = 0;
	}
	if (!g_enc || g_encw != w || g_ench != h)
	{
		close_encoder();
		if (!setup_encoder(w, h))
		{
			rdp_update_unlock(ctx->update);
			fprintf(stderr, "encoder setup failed (%dx%d)\n", w, h);
			emit_ended("encoder_failed", "", "H.264 encoder setup failed", 0, 0);
			return FALSE;
		}
		g_init_sent = 0;
		g_force_idr = 1;
	}
	BYTE* dst[3] = { g_picin.img.plane[0], g_picin.img.plane[1], g_picin.img.plane[2] };
	const UINT32 dstStep[3] = { (UINT32)g_picin.img.i_stride[0], (UINT32)g_picin.img.i_stride[1],
		                        (UINT32)g_picin.img.i_stride[2] };
	const prim_size_t roi = { (UINT32)w, (UINT32)h };
	g_prims->RGBToYUV420_8u_P3AC4R(gdi->primary_buffer, gdi->dstFormat, gdi->stride, dst, dstStep, &roi);
	rdp_update_unlock(ctx->update);

	emit_init(w, h);
	g_picin.i_pts = g_pts++;
	g_picin.i_type = g_force_idr ? X264_TYPE_IDR : X264_TYPE_AUTO;
	g_force_idr = 0;
	x264_nal_t* nals = NULL;
	int i_nals = 0;
	x264_picture_t pic_out;
	const int sz = x264_encoder_encode(g_enc, &nals, &i_nals, &g_picin, &pic_out);
	if (sz > 0)
	{
		emit_frame(nals[0].p_payload, sz);
		g_frames++;
	}
	return TRUE;
}

/* Called by the GDI after each update (any thread): the main loop encodes. */
static BOOL rh_end_paint(rdpContext* ctx)
{
	(void)ctx;
	InterlockedExchange(&g_dirty, 1);
	wake_main();
	return TRUE;
}

static BOOL rh_desktop_resize(rdpContext* ctx)
{
	rdpSettings* s = ctx->settings;
	const UINT32 w = freerdp_settings_get_uint32(s, FreeRDP_DesktopWidth);
	const UINT32 h = freerdp_settings_get_uint32(s, FreeRDP_DesktopHeight);
	fprintf(stderr, "desktop resize %ux%u\n", (unsigned)w, (unsigned)h);
	if (ctx->gdi && !gdi_resize(ctx->gdi, w, h))
		return FALSE;
	InterlockedExchange(&g_desk_w, (LONG)w);
	InterlockedExchange(&g_desk_h, (LONG)h);
	InterlockedExchange(&g_dirty, 1); /* the encoder is re-created at the new size */
	wake_main();
	return TRUE;
}

/* ── pointer (cursor shapes) ────────────────────────────────────────────── */

static void emit_pointer_msg(const BYTE* msg, size_t len)
{
	EnterCriticalSection(&g_clipLock);
	BYTE* copy = (BYTE*)malloc(len);
	if (copy)
	{
		memcpy(copy, msg, len);
		free(g_ptr_msg);
		g_ptr_msg = copy;
		g_ptr_len = len;
	}
	LeaveCriticalSection(&g_clipLock);
	emit_msg('P', msg, len, NULL, 0);
}

static void emit_pointer_kind(BYTE kind)
{
	emit_pointer_msg(&kind, 1);
}

static BOOL rh_pointer_new(rdpContext* context, rdpPointer* pointer)
{
	rhPointer* p = (rhPointer*)pointer;
	const UINT32 w = pointer->width, h = pointer->height;
	p->bgra = NULL;
	if (w == 0 || h == 0 || w > 384 || h > 384)
		return TRUE; /* nothing drawable: Set() sends the default cursor */
	p->bgra = (BYTE*)calloc((size_t)w * h, 4);
	if (!p->bgra)
		return FALSE;
	if (!freerdp_image_copy_from_pointer_data(p->bgra, PIXEL_FORMAT_BGRA32, w * 4, 0, 0, w, h,
	                                          pointer->xorMaskData, pointer->lengthXorMask,
	                                          pointer->andMaskData, pointer->lengthAndMask,
	                                          pointer->xorBpp, context->gdi ? &context->gdi->palette : NULL))
	{
		free(p->bgra);
		p->bgra = NULL;
	}
	return TRUE;
}

static void rh_pointer_free(rdpContext* context, rdpPointer* pointer)
{
	(void)context;
	rhPointer* p = (rhPointer*)pointer;
	free(p->bgra);
	p->bgra = NULL;
}

static BOOL rh_pointer_set(rdpContext* context, rdpPointer* pointer)
{
	(void)context;
	rhPointer* p = (rhPointer*)pointer;
	if (!p->bgra)
	{
		emit_pointer_kind(1);
		return TRUE;
	}
	const UINT32 w = pointer->width, h = pointer->height;
	const size_t px = (size_t)w * h * 4;
	BYTE* msg = (BYTE*)malloc(9 + px);
	if (!msg)
		return TRUE;
	msg[0] = 2;
	msg[1] = (BYTE)(pointer->xPos & 0xFF);
	msg[2] = (BYTE)(pointer->xPos >> 8);
	msg[3] = (BYTE)(pointer->yPos & 0xFF);
	msg[4] = (BYTE)(pointer->yPos >> 8);
	msg[5] = (BYTE)(w & 0xFF);
	msg[6] = (BYTE)(w >> 8);
	msg[7] = (BYTE)(h & 0xFF);
	msg[8] = (BYTE)(h >> 8);
	memcpy(msg + 9, p->bgra, px);
	emit_pointer_msg(msg, 9 + px);
	free(msg);
	return TRUE;
}

static BOOL rh_pointer_set_null(rdpContext* context)
{
	(void)context;
	emit_pointer_kind(0);
	return TRUE;
}

static BOOL rh_pointer_set_default(rdpContext* context)
{
	(void)context;
	emit_pointer_kind(1);
	return TRUE;
}

static BOOL rh_pointer_set_position(rdpContext* context, UINT32 x, UINT32 y)
{
	(void)context;
	(void)x;
	(void)y;
	return TRUE; /* the viewer owns the local pointer position */
}

/* ── clipboard (CLIPRDR, text only) ─────────────────────────────────────── */

/* UTF-8 -> CF_UNICODETEXT bytes (UTF-16LE, NUL-terminated, CRLF line ends). */
static BYTE* utf8_to_cf_unicode(const char* utf8, UINT32* outLen)
{
	*outLen = 0;
	if (!utf8)
		return NULL;
	/* LF -> CRLF first (Windows clipboard convention). */
	size_t n = strlen(utf8), extra = 0;
	for (size_t i = 0; i < n; i++)
		if (utf8[i] == '\n' && (i == 0 || utf8[i - 1] != '\r'))
			extra++;
	char* crlf = (char*)malloc(n + extra + 1);
	if (!crlf)
		return NULL;
	size_t o = 0;
	for (size_t i = 0; i < n; i++)
	{
		if (utf8[i] == '\n' && (i == 0 || utf8[i - 1] != '\r'))
			crlf[o++] = '\r';
		crlf[o++] = utf8[i];
	}
	crlf[o] = '\0';
	size_t wlen = 0;
	WCHAR* w = ConvertUtf8ToWCharAlloc(crlf, &wlen);
	free(crlf);
	if (!w)
		return NULL;
	*outLen = (UINT32)((wlen + 1) * sizeof(WCHAR));
	return (BYTE*)w;
}

/* CF_UNICODETEXT bytes -> UTF-8 (stops at the first NUL, CRLF -> LF). */
static char* cf_unicode_to_utf8(const BYTE* data, UINT32 len)
{
	if (!data || len < 2)
		return _strdup("");
	size_t wlen = len / 2;
	const WCHAR* w = (const WCHAR*)data;
	size_t real = 0;
	while (real < wlen && w[real] != 0)
		real++;
	if (real == 0)
		return _strdup("");
	size_t ulen = 0;
	char* u = ConvertWCharNToUtf8Alloc(w, real, &ulen);
	if (!u)
		return NULL;
	size_t o = 0;
	for (size_t i = 0; u[i]; i++)
		if (!(u[i] == '\r' && u[i + 1] == '\n'))
			u[o++] = u[i];
	u[o] = '\0';
	return u;
}

static UINT clip_send_format_list(CliprdrClientContext* c)
{
	CLIPRDR_FORMAT fmt = { 0 };
	CLIPRDR_FORMAT_LIST list = { 0 };
	EnterCriticalSection(&g_clipLock);
	const BOOL have = g_clip_local != NULL;
	LeaveCriticalSection(&g_clipLock);
	fmt.formatId = CF_UNICODETEXT_ID;
	list.common.msgType = CB_FORMAT_LIST;
	list.numFormats = have ? 1 : 0;
	list.formats = have ? &fmt : NULL;
	return c->ClientFormatList(c, &list);
}

static UINT clip_monitor_ready(CliprdrClientContext* c, const CLIPRDR_MONITOR_READY* r)
{
	(void)r;
	CLIPRDR_GENERAL_CAPABILITY_SET gen = { 0 };
	CLIPRDR_CAPABILITIES caps = { 0 };
	gen.capabilitySetType = CB_CAPSTYPE_GENERAL;
	gen.capabilitySetLength = CB_CAPSTYPE_GENERAL_LEN;
	gen.version = CB_CAPS_VERSION_2;
	gen.generalFlags = CB_USE_LONG_FORMAT_NAMES;
	caps.cCapabilitiesSets = 1;
	caps.capabilitySets = (CLIPRDR_CAPABILITY_SET*)&gen;
	UINT rc = c->ClientCapabilities(c, &caps);
	if (rc != CHANNEL_RC_OK)
		return rc;
	g_clip_ready = 1;
	return clip_send_format_list(c);
}

static UINT clip_server_caps(CliprdrClientContext* c, const CLIPRDR_CAPABILITIES* caps)
{
	(void)c;
	(void)caps;
	return CHANNEL_RC_OK;
}

static UINT clip_server_format_list(CliprdrClientContext* c, const CLIPRDR_FORMAT_LIST* list)
{
	BOOL unicode = FALSE, text = FALSE;
	for (UINT32 i = 0; i < list->numFormats; i++)
	{
		if (list->formats[i].formatId == CF_UNICODETEXT_ID)
			unicode = TRUE;
		else if (list->formats[i].formatId == CF_TEXT_ID)
			text = TRUE;
	}
	CLIPRDR_FORMAT_LIST_RESPONSE resp = { 0 };
	resp.common.msgType = CB_FORMAT_LIST_RESPONSE;
	resp.common.msgFlags = CB_RESPONSE_OK;
	UINT rc = c->ClientFormatListResponse(c, &resp);
	if (rc != CHANNEL_RC_OK)
		return rc;
	if (unicode || text)
	{
		CLIPRDR_FORMAT_DATA_REQUEST req = { 0 };
		req.common.msgType = CB_FORMAT_DATA_REQUEST;
		req.requestedFormatId = unicode ? CF_UNICODETEXT_ID : CF_TEXT_ID;
		c->lastRequestedFormatId = req.requestedFormatId;
		rc = c->ClientFormatDataRequest(c, &req);
	}
	return rc;
}

static UINT clip_server_format_list_response(CliprdrClientContext* c, const CLIPRDR_FORMAT_LIST_RESPONSE* r)
{
	(void)c;
	(void)r;
	return CHANNEL_RC_OK;
}

static UINT clip_server_data_response(CliprdrClientContext* c, const CLIPRDR_FORMAT_DATA_RESPONSE* r)
{
	if (r->common.msgFlags & CB_RESPONSE_FAIL)
		return CHANNEL_RC_OK;
	const UINT32 len = r->common.dataLen;
	char* text = NULL;
	if (c->lastRequestedFormatId == CF_UNICODETEXT_ID)
		text = cf_unicode_to_utf8(r->requestedFormatData, len);
	else
	{
		text = (char*)calloc(1, (size_t)len + 1);
		if (text && len)
			memcpy(text, r->requestedFormatData, len);
	}
	if (text)
	{
		size_t n = strlen(text);
		if (n > MAX_CLIP_TEXT)
			n = MAX_CLIP_TEXT;
		emit_msg('C', text, n, NULL, 0);
		free(text);
	}
	return CHANNEL_RC_OK;
}

static UINT clip_server_data_request(CliprdrClientContext* c, const CLIPRDR_FORMAT_DATA_REQUEST* r)
{
	CLIPRDR_FORMAT_DATA_RESPONSE resp = { 0 };
	BYTE* data = NULL;
	UINT32 len = 0;
	resp.common.msgType = CB_FORMAT_DATA_RESPONSE;
	EnterCriticalSection(&g_clipLock);
	if (g_clip_local && r->requestedFormatId == CF_UNICODETEXT_ID)
		data = utf8_to_cf_unicode(g_clip_local, &len);
	else if (g_clip_local && r->requestedFormatId == CF_TEXT_ID)
	{
		len = (UINT32)strlen(g_clip_local) + 1;
		data = (BYTE*)_strdup(g_clip_local);
	}
	LeaveCriticalSection(&g_clipLock);
	resp.common.msgFlags = data ? CB_RESPONSE_OK : CB_RESPONSE_FAIL;
	resp.common.dataLen = data ? len : 0;
	resp.requestedFormatData = data;
	const UINT rc = c->ClientFormatDataResponse(c, &resp);
	free(data);
	return rc;
}

static void clip_attach(rhContext* rh, CliprdrClientContext* c)
{
	rh->cliprdr = c;
	c->custom = rh;
	c->MonitorReady = clip_monitor_ready;
	c->ServerCapabilities = clip_server_caps;
	c->ServerFormatList = clip_server_format_list;
	c->ServerFormatListResponse = clip_server_format_list_response;
	c->ServerFormatDataResponse = clip_server_data_response;
	c->ServerFormatDataRequest = clip_server_data_request;
}

/* Local text for the server: stored, then announced (stdin thread). */
static void clip_set_local(const char* text)
{
	EnterCriticalSection(&g_clipLock);
	free(g_clip_local);
	g_clip_local = NULL;
	if (text)
	{
		size_t n = strlen(text);
		if (n > MAX_CLIP_TEXT)
			n = MAX_CLIP_TEXT;
		g_clip_local = (char*)malloc(n + 1);
		if (g_clip_local)
		{
			memcpy(g_clip_local, text, n);
			g_clip_local[n] = '\0';
		}
	}
	LeaveCriticalSection(&g_clipLock);
	if (g_instance && g_instance->context && g_clip_ready)
	{
		rhContext* rh = (rhContext*)g_instance->context;
		if (rh->cliprdr)
			(void)clip_send_format_list(rh->cliprdr);
	}
}

/* ── display control (RDPEDISP) ─────────────────────────────────────────── */

static UINT disp_caps(DispClientContext* d, UINT32 maxMon, UINT32 fa, UINT32 fb)
{
	(void)d;
	fprintf(stderr, "display control caps: %u monitors, area %ux%u\n", (unsigned)maxMon, (unsigned)fa,
	        (unsigned)fb);
	InterlockedExchange(&g_disp_ready, 1);
	JB* o = status_new("display_control");
	jadd_bool(o, "ready", TRUE);
	emit_status_obj(o);
	wake_main();
	return CHANNEL_RC_OK;
}

/* Clamps a requested size to what RDPEDISP accepts (200..8192, even width). */
static void clamp_layout(int* w, int* h)
{
	if (*w < 200)
		*w = 200;
	if (*h < 200)
		*h = 200;
	if (*w > 8192)
		*w = 8192;
	if (*h > 8192)
		*h = 8192;
	*w &= ~1;
}

static int clamp_scale(int v, int lo, int hi, int def)
{
	if (v <= 0)
		return def;
	return v < lo ? lo : (v > hi ? hi : v);
}

static BOOL send_layout(rhContext* rh, int w, int h, int ds, int dvs)
{
	DISPLAY_CONTROL_MONITOR_LAYOUT l = { 0 };
	clamp_layout(&w, &h);
	l.Flags = DISPLAY_CONTROL_MONITOR_PRIMARY;
	l.Left = 0;
	l.Top = 0;
	l.Width = (UINT32)w;
	l.Height = (UINT32)h;
	l.Orientation = 0;
	l.DesktopScaleFactor = (UINT32)clamp_scale(ds, 100, 500, 100);
	l.DeviceScaleFactor = (UINT32)(dvs >= 180 ? 180 : (dvs >= 140 ? 140 : 100));
	l.PhysicalWidth = 0;
	l.PhysicalHeight = 0;
	fprintf(stderr, "display layout -> %dx%d (scale %u/%u)\n", w, h, (unsigned)l.DesktopScaleFactor,
	        (unsigned)l.DeviceScaleFactor);
	return rh->disp->SendMonitorLayout(rh->disp, 1, &l) == CHANNEL_RC_OK;
}

/* ── channels ───────────────────────────────────────────────────────────── */

static void rh_on_channel_connected(void* context, const ChannelConnectedEventArgs* e)
{
	rhContext* rh = (rhContext*)context;
	freerdp_client_OnChannelConnectedEventHandler(context, e);
	if (strcmp(e->name, DISP_DVC_CHANNEL_NAME) == 0)
	{
		rh->disp = (DispClientContext*)e->pInterface;
		rh->disp->custom = rh;
		rh->disp->DisplayControlCaps = disp_caps;
		fprintf(stderr, "channel %s connected\n", e->name);
	}
	else if (strcmp(e->name, CLIPRDR_SVC_CHANNEL_NAME) == 0)
	{
		clip_attach(rh, (CliprdrClientContext*)e->pInterface);
		fprintf(stderr, "channel %s connected\n", e->name);
	}
}

static void rh_on_channel_disconnected(void* context, const ChannelDisconnectedEventArgs* e)
{
	rhContext* rh = (rhContext*)context;
	if (strcmp(e->name, DISP_DVC_CHANNEL_NAME) == 0)
	{
		rh->disp = NULL;
		InterlockedExchange(&g_disp_ready, 0);
	}
	else if (strcmp(e->name, CLIPRDR_SVC_CHANNEL_NAME) == 0)
	{
		rh->cliprdr = NULL;
		g_clip_ready = 0;
	}
	freerdp_client_OnChannelDisconnectedEventHandler(context, e);
}

/* ── prompts (certificate / credentials): wait for the Desktop's answer ─── */

/* Waits for the reply of `kind` (set by the stdin thread). FALSE on timeout
 * or when stdin closes. */
static BOOL wait_reply(int kind, int timeoutMs)
{
	const ULONGLONG start = GetTickCount64();
	for (;;)
	{
		if (g_stdin_closed || g_out_broken || g_quit_req)
			return FALSE;
		if (WaitForSingleObject(g_reply_event, 200) == WAIT_OBJECT_0)
		{
			ResetEvent(g_reply_event);
			if (InterlockedCompareExchange(&g_reply_kind, 0, -kind) == -kind)
				return TRUE;
		}
		if (timeoutMs > 0 && GetTickCount64() - start >= (ULONGLONG)timeoutMs)
			return FALSE;
	}
}

/* "AB:cd:01" and "abcd01" compare equal; any "sha256 " style prefix is kept
 * out of the comparison. */
static void norm_fp(const char* in, char* out, size_t outsz)
{
	size_t o = 0;
	const char* p = in ? in : "";
	const char* sp = strrchr(p, ' ');
	if (sp)
		p = sp + 1;
	for (; *p && o + 1 < outsz; p++)
		if (isxdigit((unsigned char)*p))
			out[o++] = (char)tolower((unsigned char)*p);
	out[o] = '\0';
}

static BOOL fp_known(const char* fp)
{
	char a[256], b[256];
	norm_fp(fp, a, sizeof(a));
	if (!a[0])
		return FALSE;
	for (int i = 0; i < g_cfg.nKnownFp; i++)
	{
		norm_fp(g_cfg.knownFp[i], b, sizeof(b));
		if (strcmp(a, b) == 0)
			return TRUE;
	}
	return FALSE;
}

/* 2 = accept for this session only: the Desktop keeps the trust store
 * (rdp_known_hosts), FreeRDP never writes its own. */
static DWORD cert_decide(const char* host, UINT16 port, const char* cn, const char* subject, const char* issuer,
                         const char* fp, DWORD flags, BOOL changed, const char* oldFp)
{
	if (fp_known(fp))
	{
		fprintf(stderr, "certificate of %s:%u: known fingerprint\n", host, (unsigned)port);
		return 2;
	}
	g_reply_accept = 0;
	ResetEvent(g_reply_event);
	InterlockedExchange(&g_reply_kind, 1);
	JB* o = status_new("cert");
	jadd_str(o, "host", host);
	jadd_int(o, "port", port);
	jadd_str(o, "commonName", cn);
	jadd_str(o, "subject", subject);
	jadd_str(o, "issuer", issuer);
	jadd_str(o, "fingerprint", fp);
	jadd_bool(o, "hostMismatch", (flags & VERIFY_CERT_FLAG_MISMATCH) != 0);
	jadd_bool(o, "gateway", (flags & VERIFY_CERT_FLAG_GATEWAY) != 0);
	jadd_bool(o, "redirect", (flags & VERIFY_CERT_FLAG_REDIRECT) != 0);
	jadd_bool(o, "changed", changed);
	if (changed)
		jadd_str(o, "oldFingerprint", oldFp);
	emit_status_obj(o);
	const BOOL got = wait_reply(1, g_cfg.certTimeoutMs);
	InterlockedExchange(&g_reply_kind, 0);
	if (got && g_reply_accept)
		return 2;
	g_cert_rejected = 1;
	fprintf(stderr, "certificate of %s:%u rejected (%s)\n", host, (unsigned)port, got ? "by the user" : "no answer");
	return 0;
}

static DWORD rh_verify_cert_ex(freerdp* instance, const char* host, UINT16 port, const char* cn,
                               const char* subject, const char* issuer, const char* fp, DWORD flags)
{
	(void)instance;
	return cert_decide(host, port, cn, subject, issuer, fp, flags, FALSE, NULL);
}

static DWORD rh_verify_changed_cert_ex(freerdp* instance, const char* host, UINT16 port, const char* cn,
                                       const char* subject, const char* issuer, const char* fp,
                                       const char* oldSubject, const char* oldIssuer, const char* oldFp,
                                       DWORD flags)
{
	(void)instance;
	(void)oldSubject;
	(void)oldIssuer;
	return cert_decide(host, port, cn, subject, issuer, fp, flags, TRUE, oldFp);
}

static const char* auth_reason_name(rdp_auth_reason r)
{
	switch (r)
	{
		case AUTH_NLA:
			return "nla";
		case AUTH_TLS:
			return "tls";
		case AUTH_RDP:
			return "rdp";
		case GW_AUTH_HTTP:
		case GW_AUTH_RDG:
		case GW_AUTH_RPC:
			return "gateway";
		case AUTH_SMARTCARD_PIN:
			return "smartcard_pin";
		case AUTH_RDSTLS:
			return "rdstls";
		default:
			return "other";
	}
}

static void replace_str(char** dst, const char* v)
{
	if (*dst)
	{
		secure_wipe(*dst, strlen(*dst));
		free(*dst);
	}
	*dst = v ? _strdup(v) : NULL;
}

static BOOL rh_authenticate_ex(freerdp* instance, char** username, char** password, char** domain,
                               rdp_auth_reason reason)
{
	(void)instance;
	const BOOL gw = (reason == GW_AUTH_HTTP || reason == GW_AUTH_RDG || reason == GW_AUTH_RPC);
	const BOOL haveUser = *username && **username;
	const BOOL havePass = *password && **password;
	if (haveUser && havePass)
		return TRUE;
	/* TLS / RDP security: the server shows its own logon screen. */
	if (!gw && (reason == AUTH_TLS || reason == AUTH_RDP))
		return TRUE;
	if (reason == AUTH_SMARTCARD_PIN)
		return FALSE; /* smart cards: not in v1 */
	ResetEvent(g_reply_event);
	InterlockedExchange(&g_reply_kind, 2);
	g_reply_cancel = 0;
	JB* o = status_new("credentials");
	jadd_str(o, "reason", auth_reason_name(reason));
	jadd_str(o, "username", *username);
	jadd_str(o, "domain", *domain);
	emit_status_obj(o);
	const BOOL got = wait_reply(2, 300000);
	InterlockedExchange(&g_reply_kind, 0);
	if (!got || g_reply_cancel)
	{
		g_cred_cancelled = 1;
		free_secret(&g_reply_pass);
		return FALSE;
	}
	if (g_reply_user)
		replace_str(username, g_reply_user);
	if (g_reply_domain)
		replace_str(domain, g_reply_domain);
	replace_str(password, g_reply_pass ? g_reply_pass : "");
	free_secret(&g_reply_user);
	free_secret(&g_reply_domain);
	free_secret(&g_reply_pass);
	return TRUE;
}

static int rh_logon_error_info(freerdp* instance, UINT32 data, UINT32 type)
{
	(void)instance;
	fprintf(stderr, "logon info: type 0x%08x data 0x%08x\n", (unsigned)type, (unsigned)data);
	return 1;
}

/* ── connection lifecycle callbacks ─────────────────────────────────────── */

static BOOL rh_pre_connect(freerdp* instance)
{
	rdpContext* ctx = instance->context;
	PubSub_SubscribeChannelConnected(ctx->pubSub, rh_on_channel_connected);
	PubSub_SubscribeChannelDisconnected(ctx->pubSub, rh_on_channel_disconnected);
	return TRUE;
}

static BOOL rh_post_connect(freerdp* instance)
{
	rdpContext* ctx = instance->context;
	if (ctx->gdi)
	{
		/* Reconnect on the same instance: keep the GDI, follow the size. */
		if (!gdi_resize(ctx->gdi, freerdp_settings_get_uint32(ctx->settings, FreeRDP_DesktopWidth),
		                freerdp_settings_get_uint32(ctx->settings, FreeRDP_DesktopHeight)))
			return FALSE;
	}
	else
	{
		if (!gdi_init(instance, PIXEL_FORMAT_BGRA32))
			return FALSE;
		rdpPointer ptr = { 0 };
		ptr.size = sizeof(rhPointer);
		ptr.New = rh_pointer_new;
		ptr.Free = rh_pointer_free;
		ptr.Set = rh_pointer_set;
		ptr.SetNull = rh_pointer_set_null;
		ptr.SetDefault = rh_pointer_set_default;
		ptr.SetPosition = rh_pointer_set_position;
		graphics_register_pointer(ctx->graphics, &ptr);
	}
	ctx->update->EndPaint = rh_end_paint;
	ctx->update->DesktopResize = rh_desktop_resize;
	return TRUE;
}

static BOOL rh_client_new(freerdp* instance, rdpContext* context)
{
	(void)context;
	instance->PreConnect = rh_pre_connect;
	instance->PostConnect = rh_post_connect;
	instance->AuthenticateEx = rh_authenticate_ex;
	instance->VerifyCertificateEx = rh_verify_cert_ex;
	instance->VerifyChangedCertificateEx = rh_verify_changed_cert_ex;
	instance->LogonErrorInfo = rh_logon_error_info;
	instance->LoadChannels = freerdp_client_load_channels;
	return TRUE;
}

/* ── low-level input (callers hold g_inLock) ─────────────────────────────── */

static BOOL g_keydown[2][256];
static UINT32 g_buttons = 0; /* bit i = DOM button i pressed */

static void ev_key(UINT8 sc, BOOL ext, BOOL down)
{
	const UINT16 flags = (UINT16)((ext ? KBD_FLAGS_EXTENDED : 0) | (down ? 0 : KBD_FLAGS_RELEASE));
	g_keydown[ext ? 1 : 0][sc] = down;
	if (g_testmode)
		rec_ev('k', flags, sc, 0);
	else if (g_input)
		freerdp_input_send_keyboard_event(g_input, flags, sc);
}

static void ev_unicode(UINT16 ch, BOOL down)
{
	const UINT16 flags = down ? 0 : KBD_FLAGS_RELEASE;
	if (g_testmode)
		rec_ev('u', flags, ch, 0);
	else if (g_input)
		freerdp_input_send_unicode_keyboard_event(g_input, flags, ch);
}

static void ev_pause(void)
{
	if (g_testmode)
		rec_ev('p', 0, 0, 0);
	else if (g_input)
		freerdp_input_send_keyboard_pause_event(g_input);
}

static void ev_mouse(UINT16 flags, UINT16 x, UINT16 y)
{
	if (g_testmode)
		rec_ev('m', flags, x, y);
	else if (g_input)
		freerdp_input_send_mouse_event(g_input, flags, x, y);
}

static void ev_xmouse(UINT16 flags, UINT16 x, UINT16 y)
{
	if (g_testmode)
		rec_ev('x', flags, x, y);
	else if (g_input)
		freerdp_input_send_extended_mouse_event(g_input, flags, x, y);
}

static void ev_sync(UINT32 flags)
{
	if (g_testmode)
		rec_ev('s', (UINT16)flags, 0, 0);
	else if (g_input)
		freerdp_input_send_synchronize_event(g_input, flags);
}

/* Wheel flags: delta > 0 = wheel up (away from the user), < 0 = down. */
static UINT16 wheel_flags(int delta, BOOL horizontal)
{
	UINT16 f = horizontal ? PTR_FLAGS_HWHEEL : PTR_FLAGS_WHEEL;
	if (delta < 0)
		f |= PTR_FLAGS_WHEEL_NEGATIVE | 0x0088; /* -120 in 9-bit two's complement */
	else
		f |= 0x0078; /* +120 */
	return f;
}

/* ── DOM KeyboardEvent.code -> PS/2 set-1 scancode (+ extended flag) ─────── */

struct kmap
{
	const char* code;
	UINT8 sc;
	BOOL ext;
};
#define SC_PAUSE 0xFF /* special: Pause/Break sequence */
static const struct kmap KMAP[] = {
	{ "Escape", 0x01, 0 }, { "Digit1", 0x02, 0 }, { "Digit2", 0x03, 0 }, { "Digit3", 0x04, 0 },
	{ "Digit4", 0x05, 0 }, { "Digit5", 0x06, 0 }, { "Digit6", 0x07, 0 }, { "Digit7", 0x08, 0 },
	{ "Digit8", 0x09, 0 }, { "Digit9", 0x0A, 0 }, { "Digit0", 0x0B, 0 }, { "Minus", 0x0C, 0 },
	{ "Equal", 0x0D, 0 }, { "Backspace", 0x0E, 0 }, { "Tab", 0x0F, 0 }, { "KeyQ", 0x10, 0 },
	{ "KeyW", 0x11, 0 }, { "KeyE", 0x12, 0 }, { "KeyR", 0x13, 0 }, { "KeyT", 0x14, 0 },
	{ "KeyY", 0x15, 0 }, { "KeyU", 0x16, 0 }, { "KeyI", 0x17, 0 }, { "KeyO", 0x18, 0 },
	{ "KeyP", 0x19, 0 }, { "BracketLeft", 0x1A, 0 }, { "BracketRight", 0x1B, 0 }, { "Enter", 0x1C, 0 },
	{ "ControlLeft", 0x1D, 0 }, { "KeyA", 0x1E, 0 }, { "KeyS", 0x1F, 0 }, { "KeyD", 0x20, 0 },
	{ "KeyF", 0x21, 0 }, { "KeyG", 0x22, 0 }, { "KeyH", 0x23, 0 }, { "KeyJ", 0x24, 0 },
	{ "KeyK", 0x25, 0 }, { "KeyL", 0x26, 0 }, { "Semicolon", 0x27, 0 }, { "Quote", 0x28, 0 },
	{ "Backquote", 0x29, 0 }, { "ShiftLeft", 0x2A, 0 }, { "Backslash", 0x2B, 0 }, { "KeyZ", 0x2C, 0 },
	{ "KeyX", 0x2D, 0 }, { "KeyC", 0x2E, 0 }, { "KeyV", 0x2F, 0 }, { "KeyB", 0x30, 0 },
	{ "KeyN", 0x31, 0 }, { "KeyM", 0x32, 0 }, { "Comma", 0x33, 0 }, { "Period", 0x34, 0 },
	{ "Slash", 0x35, 0 }, { "ShiftRight", 0x36, 0 }, { "NumpadMultiply", 0x37, 0 }, { "AltLeft", 0x38, 0 },
	{ "Space", 0x39, 0 }, { "CapsLock", 0x3A, 0 }, { "F1", 0x3B, 0 }, { "F2", 0x3C, 0 }, { "F3", 0x3D, 0 },
	{ "F4", 0x3E, 0 }, { "F5", 0x3F, 0 }, { "F6", 0x40, 0 }, { "F7", 0x41, 0 }, { "F8", 0x42, 0 },
	{ "F9", 0x43, 0 }, { "F10", 0x44, 0 }, { "NumLock", 0x45, 0 }, { "ScrollLock", 0x46, 0 },
	{ "Numpad7", 0x47, 0 }, { "Numpad8", 0x48, 0 }, { "Numpad9", 0x49, 0 }, { "NumpadSubtract", 0x4A, 0 },
	{ "Numpad4", 0x4B, 0 }, { "Numpad5", 0x4C, 0 }, { "Numpad6", 0x4D, 0 }, { "NumpadAdd", 0x4E, 0 },
	{ "Numpad1", 0x4F, 0 }, { "Numpad2", 0x50, 0 }, { "Numpad3", 0x51, 0 }, { "Numpad0", 0x52, 0 },
	{ "NumpadDecimal", 0x53, 0 }, { "IntlBackslash", 0x56, 0 }, { "F11", 0x57, 0 }, { "F12", 0x58, 0 },
	{ "NumpadEqual", 0x59, 0 }, { "F13", 0x64, 0 }, { "F14", 0x65, 0 }, { "F15", 0x66, 0 },
	{ "F16", 0x67, 0 }, { "F17", 0x68, 0 }, { "F18", 0x69, 0 }, { "F19", 0x6A, 0 }, { "F20", 0x6B, 0 },
	{ "F21", 0x6C, 0 }, { "F22", 0x6D, 0 }, { "F23", 0x6E, 0 }, { "KanaMode", 0x70, 0 },
	{ "IntlRo", 0x73, 0 }, { "F24", 0x76, 0 }, { "Convert", 0x79, 0 }, { "NonConvert", 0x7B, 0 },
	{ "IntlYen", 0x7D, 0 }, { "NumpadComma", 0x7E, 0 },
	/* extended (0xE0-prefixed) */
	{ "ControlRight", 0x1D, 1 }, { "AltRight", 0x38, 1 }, { "NumpadEnter", 0x1C, 1 },
	{ "NumpadDivide", 0x35, 1 }, { "PrintScreen", 0x37, 1 }, { "Insert", 0x52, 1 }, { "Delete", 0x53, 1 },
	{ "Home", 0x47, 1 }, { "End", 0x4F, 1 }, { "PageUp", 0x49, 1 }, { "PageDown", 0x51, 1 },
	{ "ArrowUp", 0x48, 1 }, { "ArrowLeft", 0x4B, 1 }, { "ArrowRight", 0x4D, 1 }, { "ArrowDown", 0x50, 1 },
	{ "MetaLeft", 0x5B, 1 }, { "MetaRight", 0x5C, 1 }, { "OSLeft", 0x5B, 1 }, { "OSRight", 0x5C, 1 },
	{ "ContextMenu", 0x5D, 1 }, { "AudioVolumeMute", 0x20, 1 }, { "AudioVolumeDown", 0x2E, 1 },
	{ "AudioVolumeUp", 0x30, 1 }, { "MediaPlayPause", 0x22, 1 }, { "MediaStop", 0x24, 1 },
	{ "MediaTrackNext", 0x19, 1 }, { "MediaTrackPrevious", 0x10, 1 },
	/* special */
	{ "Pause", SC_PAUSE, 0 }, { NULL, 0, 0 },
};

static BOOL code_to_sc(const char* code, UINT8* sc, BOOL* ext)
{
	if (!code)
		return FALSE;
	for (int i = 0; KMAP[i].code; i++)
		if (strcmp(KMAP[i].code, code) == 0)
		{
			*sc = KMAP[i].sc;
			*ext = KMAP[i].ext;
			return TRUE;
		}
	return FALSE;
}

/* US-layout fallback for servers without unicode input: ASCII -> scancode
 * + shift. Only used when the server refuses unicode keyboard events. */
static BOOL ascii_us(unsigned char c, UINT8* sc, BOOL* shift)
{
	static const char* rowQ = "qwertyuiop";
	static const char* rowA = "asdfghjkl";
	static const char* rowZ = "zxcvbnm";
	static const char* digits = "1234567890";
	static const char* shDigits = "!@#$%^&*()";
	static const struct
	{
		char c, sh;
		UINT8 sc;
	} punct[] = { { '`', '~', 0x29 }, { '-', '_', 0x0C }, { '=', '+', 0x0D }, { '[', '{', 0x1A },
		          { ']', '}', 0x1B }, { '\\', '|', 0x2B }, { ';', ':', 0x27 }, { '\'', '"', 0x28 },
		          { ',', '<', 0x33 }, { '.', '>', 0x34 }, { '/', '?', 0x35 } };
	*shift = FALSE;
	if (c == ' ')
	{
		*sc = 0x39;
		return TRUE;
	}
	const unsigned char lc = (unsigned char)tolower(c);
	const char* p;
	if (c >= 'A' && c <= 'Z')
		*shift = TRUE;
	if ((p = strchr(rowQ, lc)) && lc >= 'a' && lc <= 'z')
	{
		*sc = (UINT8)(0x10 + (p - rowQ));
		return TRUE;
	}
	if ((p = strchr(rowA, lc)) && lc >= 'a' && lc <= 'z')
	{
		*sc = (UINT8)(0x1E + (p - rowA));
		return TRUE;
	}
	if ((p = strchr(rowZ, lc)) && lc >= 'a' && lc <= 'z')
	{
		*sc = (UINT8)(0x2C + (p - rowZ));
		return TRUE;
	}
	*shift = FALSE;
	if (c && (p = strchr(digits, c)))
	{
		*sc = (UINT8)(0x02 + (p - digits));
		return TRUE;
	}
	if (c && (p = strchr(shDigits, c)))
	{
		*sc = (UINT8)(0x02 + (p - shDigits));
		*shift = TRUE;
		return TRUE;
	}
	for (size_t i = 0; i < sizeof(punct) / sizeof(punct[0]); i++)
	{
		if (c == (unsigned char)punct[i].c)
		{
			*sc = punct[i].sc;
			return TRUE;
		}
		if (c == (unsigned char)punct[i].sh)
		{
			*sc = punct[i].sc;
			*shift = TRUE;
			return TRUE;
		}
	}
	return FALSE;
}

/* ── text typing ────────────────────────────────────────────────────────── */

static DWORD g_type_delay = 4;
static volatile LONG g_type_cancel = 0;

static void type_pause(void)
{
	if (!g_testmode && g_type_delay)
		Sleep(g_type_delay);
}

static void tkey(UINT8 sc, BOOL ext, BOOL down)
{
	EnterCriticalSection(&g_inLock);
	ev_key(sc, ext, down);
	LeaveCriticalSection(&g_inLock);
	type_pause();
}

/* Types one UTF-16 unit. Returns FALSE when it could not be typed. */
static BOOL type_char(WCHAR ch)
{
	if (ch == L'\r')
		return TRUE;
	if (ch == L'\n')
	{
		tkey(0x1C, FALSE, TRUE);
		tkey(0x1C, FALSE, FALSE);
		return TRUE;
	}
	if (ch == L'\t')
	{
		tkey(0x0F, FALSE, TRUE);
		tkey(0x0F, FALSE, FALSE);
		return TRUE;
	}
	if (g_unicode_ok)
	{
		EnterCriticalSection(&g_inLock);
		ev_unicode(ch, TRUE);
		ev_unicode(ch, FALSE);
		LeaveCriticalSection(&g_inLock);
		type_pause();
		return TRUE;
	}
	UINT8 sc;
	BOOL shift;
	if (ch < 0x80 && ascii_us((unsigned char)ch, &sc, &shift))
	{
		if (shift)
			tkey(0x2A, FALSE, TRUE);
		tkey(sc, FALSE, TRUE);
		tkey(sc, FALSE, FALSE);
		if (shift)
			tkey(0x2A, FALSE, FALSE);
		return TRUE;
	}
	return FALSE;
}

static void release_all_locked(void)
{
	for (int e = 0; e < 2; e++)
		for (int sc = 0; sc < 256; sc++)
			if (g_keydown[e][sc])
				ev_key((UINT8)sc, e == 1, FALSE);
	const UINT16 x = 0, y = 0;
	if (g_buttons & 1)
		ev_mouse(PTR_FLAGS_BUTTON1, x, y);
	if (g_buttons & 2)
		ev_mouse(PTR_FLAGS_BUTTON3, x, y);
	if (g_buttons & 4)
		ev_mouse(PTR_FLAGS_BUTTON2, x, y);
	if (g_buttons & 8)
		ev_xmouse(PTR_XFLAGS_BUTTON1, x, y);
	if (g_buttons & 16)
		ev_xmouse(PTR_XFLAGS_BUTTON2, x, y);
	g_buttons = 0;
}

typedef struct
{
	WCHAR* text;
	int len;
} TypeJob;

static HANDLE g_type_thread = NULL;

static DWORD WINAPI type_thread_proc(LPVOID arg)
{
	TypeJob* job = (TypeJob*)arg;
	int done = 0, skipped = 0;
	for (int i = 0; i < job->len; i++)
	{
		if (g_type_cancel || g_stdin_closed || g_out_broken)
			break;
		if (!type_char(job->text[i]))
			skipped++;
		done++;
		if (done % 200 == 0)
		{
			JB* o = status_new("typing");
			jadd_int(o, "done", done);
			jadd_int(o, "total", job->len);
			emit_status_obj(o);
		}
	}
	JB* o;
	if (done < job->len)
	{
		EnterCriticalSection(&g_inLock);
		release_all_locked();
		LeaveCriticalSection(&g_inLock);
		o = status_new("typing_cancelled");
	}
	else
	{
		o = status_new("typing");
		jadd_int(o, "skipped", skipped);
	}
	jadd_int(o, "done", done);
	jadd_int(o, "total", job->len);
	emit_status_obj(o);
	secure_wipe(job->text, (size_t)job->len * sizeof(WCHAR));
	free(job->text);
	free(job);
	return 0;
}

static void stop_typing(void)
{
	if (g_type_thread)
	{
		InterlockedExchange(&g_type_cancel, 1);
		WaitForSingleObject(g_type_thread, INFINITE);
		CloseHandle(g_type_thread);
		g_type_thread = NULL;
	}
	InterlockedExchange(&g_type_cancel, 0);
}

static void start_typing(const char* utf8)
{
	stop_typing();
	size_t wlen = 0;
	WCHAR* w = ConvertUtf8ToWCharAlloc(utf8, &wlen);
	if (!w || wlen == 0)
	{
		free(w);
		return;
	}
	TypeJob* job = (TypeJob*)calloc(1, sizeof(TypeJob));
	if (!job)
	{
		free(w);
		return;
	}
	job->text = w;
	job->len = (int)(wlen > MAX_TYPE_CHARS ? MAX_TYPE_CHARS : wlen);
	if (g_testmode)
	{
		type_thread_proc(job); /* synchronous in the self-test */
		return;
	}
	g_type_thread = CreateThread(NULL, 0, type_thread_proc, job, 0, NULL);
	if (!g_type_thread)
	{
		free(job->text);
		free(job);
	}
}

/* ── JSON input dispatch ────────────────────────────────────────────────── */

static int json_int(WINPR_JSON* o, const char* k, int def)
{
	WINPR_JSON* it = WINPR_JSON_GetObjectItemCaseSensitive(o, k);
	return (it && WINPR_JSON_IsNumber(it)) ? (int)WINPR_JSON_GetNumberValue(it) : def;
}
static const char* json_str(WINPR_JSON* o, const char* k)
{
	WINPR_JSON* it = WINPR_JSON_GetObjectItemCaseSensitive(o, k);
	return (it && WINPR_JSON_IsString(it)) ? WINPR_JSON_GetStringValue(it) : NULL;
}
static BOOL json_bool(WINPR_JSON* o, const char* k, BOOL def)
{
	WINPR_JSON* it = WINPR_JSON_GetObjectItemCaseSensitive(o, k);
	if (!it || !WINPR_JSON_IsBool(it))
		return def;
	return WINPR_JSON_IsTrue(it);
}
/* Wipes a secret string value inside the parsed JSON before it is freed. */
static void json_wipe(WINPR_JSON* o, const char* k)
{
	WINPR_JSON* it = WINPR_JSON_GetObjectItemCaseSensitive(o, k);
	if (it && WINPR_JSON_IsString(it))
	{
		char* v = (char*)WINPR_JSON_GetStringValue(it);
		if (v)
			secure_wipe(v, strlen(v));
	}
}

static UINT16 clamp_coord(int v, LONG size)
{
	if (v < 0)
		v = 0;
	if (size > 0 && v > size - 1)
		v = (int)size - 1;
	if (v > 0xFFFF)
		v = 0xFFFF;
	return (UINT16)v;
}

static void inject_mouse(WINPR_JSON* o, const char* action)
{
	const UINT16 x = clamp_coord(json_int(o, "x", 0), g_desk_w);
	const UINT16 y = clamp_coord(json_int(o, "y", 0), g_desk_h);
	EnterCriticalSection(&g_inLock);
	if (strcmp(action, "move") == 0)
		ev_mouse(PTR_FLAGS_MOVE, x, y);
	else if (strcmp(action, "scroll") == 0 || strcmp(action, "hscroll") == 0)
	{
		const int d = json_int(o, "delta", 0);
		if (d != 0)
			ev_mouse(wheel_flags(d, action[0] == 'h'), x, y);
	}
	else if (strcmp(action, "down") == 0 || strcmp(action, "up") == 0)
	{
		const BOOL down = action[0] == 'd';
		const int b = json_int(o, "button", 0);
		if (b >= 0 && b <= 4)
		{
			if (down)
				g_buttons |= (1u << b);
			else
				g_buttons &= ~(1u << b);
		}
		if (b == 3 || b == 4)
			ev_xmouse((UINT16)((b == 3 ? PTR_XFLAGS_BUTTON1 : PTR_XFLAGS_BUTTON2) | (down ? PTR_XFLAGS_DOWN : 0)),
			          x, y);
		else
		{
			const UINT16 bf = (b == 2) ? PTR_FLAGS_BUTTON2 : (b == 1) ? PTR_FLAGS_BUTTON3 : PTR_FLAGS_BUTTON1;
			ev_mouse((UINT16)(bf | (down ? PTR_FLAGS_DOWN : 0)), x, y);
		}
	}
	LeaveCriticalSection(&g_inLock);
}

static void inject_key(WINPR_JSON* o, const char* action)
{
	const BOOL down = (strcmp(action, "down") == 0);
	if (!down && strcmp(action, "up") != 0)
		return;
	UINT8 sc;
	BOOL ext;
	if (code_to_sc(json_str(o, "code"), &sc, &ext))
	{
		EnterCriticalSection(&g_inLock);
		if (sc == SC_PAUSE)
		{
			if (down)
				ev_pause();
		}
		else
			ev_key(sc, ext, down);
		LeaveCriticalSection(&g_inLock);
		return;
	}
	/* Unknown code (soft keyboards, IME): one character typed on the press. */
	const char* key = json_str(o, "key");
	if (!down || !key || !key[0])
		return;
	WCHAR w[4] = { 0 };
	const SSIZE_T n = ConvertUtf8ToWChar(key, w, 4);
	if (n != 1) /* exactly one UTF-16 unit */
		return;
	const DWORD saved = g_type_delay;
	g_type_delay = 0;
	type_char(w[0]);
	g_type_delay = saved;
}

/* RDP's secure attention sequence: Ctrl+Alt+End (a real Ctrl+Alt+Del is
 * caught by the local machine and cannot be injected). */
static void inject_sas(void)
{
	EnterCriticalSection(&g_inLock);
	ev_key(0x1D, FALSE, TRUE);
	ev_key(0x38, FALSE, TRUE);
	ev_key(0x4F, TRUE, TRUE);
	LeaveCriticalSection(&g_inLock);
	if (!g_testmode)
		Sleep(50);
	EnterCriticalSection(&g_inLock);
	ev_key(0x4F, TRUE, FALSE);
	ev_key(0x38, FALSE, FALSE);
	ev_key(0x1D, FALSE, FALSE);
	LeaveCriticalSection(&g_inLock);
}

static void request_resize(WINPR_JSON* o)
{
	int w = json_int(o, "width", 0), h = json_int(o, "height", 0);
	if (w <= 0 || h <= 0)
		return;
	clamp_layout(&w, &h);
	InterlockedExchange(&g_resize_w, w);
	InterlockedExchange(&g_resize_h, h);
	InterlockedExchange(&g_resize_ds, json_int(o, "desktopScale", 0));
	InterlockedExchange(&g_resize_dvs, json_int(o, "deviceScale", 0));
	InterlockedExchange(&g_resize_pending, 1);
	wake_main();
}

/* ── the connect line ───────────────────────────────────────────────────── */

static void free_config(RhConfig* c)
{
	free(c->host);
	free_secret(&c->username);
	free_secret(&c->domain);
	free_secret(&c->password);
	for (int i = 0; i < c->nKnownFp; i++)
		free(c->knownFp[i]);
	free(c->knownFp);
	free(c->clientHostname);
	free(c->kerberosRealm);
	free(c->kdcUrl);
	free(c->configDir);
	free(c->gwHost);
	free_secret(&c->gwUser);
	free_secret(&c->gwDomain);
	free_secret(&c->gwPassword);
	memset(c, 0, sizeof(*c));
}

static void copy_word(char* dst, size_t n, const char* v, const char* def, const char* const* allowed)
{
	snprintf(dst, n, "%s", def);
	if (!v)
		return;
	for (int i = 0; allowed[i]; i++)
		if (strcmp(v, allowed[i]) == 0)
		{
			snprintf(dst, n, "%s", v);
			return;
		}
}

/* Parses the connect object into c. Returns NULL on success, else why not. */
static const char* parse_config(WINPR_JSON* o, RhConfig* c)
{
	static const char* const SEC[] = { "auto", "nla", "tls", "rdp", NULL };
	static const char* const PROF[] = { "high", "main", "baseline", NULL };
	static const char* const GWT[] = { "auto", "http", "rpc", NULL };
	memset(c, 0, sizeof(*c));
	const char* host = json_str(o, "host");
	if (!host || !*host || strlen(host) > 255)
		return "host missing or invalid";
	for (const char* p = host; *p; p++)
		if ((unsigned char)*p <= 0x20 || *p == '/' || *p == '\\')
			return "host contains invalid characters";
	c->host = _strdup(host);
	c->port = json_int(o, "port", 3389);
	if (c->port <= 0 || c->port > 65535)
		return "port out of range";
	c->username = dup_or_null(json_str(o, "username"));
	c->domain = dup_or_null(json_str(o, "domain"));
	c->password = dup_or_null(json_str(o, "password"));
	copy_word(c->security, sizeof(c->security), json_str(o, "security"), "auto", SEC);
	c->width = json_int(o, "width", 1280);
	c->height = json_int(o, "height", 800);
	clamp_layout(&c->width, &c->height);
	c->desktopScale = clamp_scale(json_int(o, "desktopScale", 100), 100, 500, 100);
	c->deviceScale = json_int(o, "deviceScale", 100);
	c->fps = json_int(o, "fps", 30);
	if (c->fps < 5)
		c->fps = 5;
	if (c->fps > 60)
		c->fps = 60;
	c->maxKbps = json_int(o, "maxKbps", 0);
	if (c->maxKbps < 0)
		c->maxKbps = 0;
	if (c->maxKbps > 0 && c->maxKbps < MIN_KBPS)
		c->maxKbps = MIN_KBPS;
	copy_word(c->profile, sizeof(c->profile), json_str(o, "profile"), "high", PROF);
	{
		const char* kl = json_str(o, "keyboardLayout");
		c->keyboardLayout = 0x409;
		if (kl && strlen(kl) <= 8 && *kl)
		{
			char* end = NULL;
			unsigned long v = strtoul(kl, &end, 16);
			if (end && *end == '\0' && v > 0)
				c->keyboardLayout = (UINT32)v;
		}
	}
	WINPR_JSON* fps = WINPR_JSON_GetObjectItemCaseSensitive(o, "knownFingerprints");
	if (fps && WINPR_JSON_IsArray(fps))
	{
		size_t n = WINPR_JSON_GetArraySize(fps);
		if (n > 32)
			n = 32;
		c->knownFp = (char**)calloc(n ? n : 1, sizeof(char*));
		for (size_t i = 0; c->knownFp && i < n; i++)
		{
			WINPR_JSON* it = WINPR_JSON_GetArrayItem(fps, i);
			if (it && WINPR_JSON_IsString(it) && WINPR_JSON_GetStringValue(it))
				c->knownFp[c->nKnownFp++] = _strdup(WINPR_JSON_GetStringValue(it));
		}
	}
	c->certTimeoutMs = json_int(o, "certTimeoutMs", 120000);
	if (c->certTimeoutMs < 1000)
		c->certTimeoutMs = 1000;
	c->clipboard = json_bool(o, "clipboard", TRUE);
	{
		const char* a = json_str(o, "audio");
		c->audioRemote = a && strcmp(a, "remote") == 0;
		c->audioLocal = a && strcmp(a, "local") == 0;
	}
	c->resizeReconnect = json_bool(o, "resizeReconnect", FALSE);
	c->console = json_bool(o, "console", FALSE);
	c->restrictedAdmin = json_bool(o, "restrictedAdmin", FALSE);
	c->autoReconnect = json_bool(o, "autoReconnect", TRUE);
	c->clientHostname = dup_or_null(json_str(o, "clientHostname"));
	c->kerberosRealm = dup_or_null(json_str(o, "kerberosRealm"));
	c->kdcUrl = dup_or_null(json_str(o, "kdcUrl"));
	c->configDir = dup_or_null(json_str(o, "configDir"));
	WINPR_JSON* gw = WINPR_JSON_GetObjectItemCaseSensitive(o, "gateway");
	if (gw && WINPR_JSON_IsObject(gw))
	{
		const char* gh = json_str(gw, "host");
		if (gh && *gh)
		{
			c->gwEnabled = TRUE;
			c->gwHost = _strdup(gh);
			c->gwPort = json_int(gw, "port", 443);
			if (c->gwPort <= 0 || c->gwPort > 65535)
				return "gateway port out of range";
			c->gwUser = dup_or_null(json_str(gw, "username"));
			c->gwDomain = dup_or_null(json_str(gw, "domain"));
			c->gwPassword = dup_or_null(json_str(gw, "password"));
			c->gwSame = json_bool(gw, "sameCredentials", c->gwUser == NULL);
			c->gwBypassLocal = json_bool(gw, "bypassLocal", FALSE);
			copy_word(c->gwTransport, sizeof(c->gwTransport), json_str(gw, "transport"), "auto", GWT);
		}
		json_wipe(gw, "password");
	}
	return NULL;
}

static BOOL apply_settings(rdpSettings* s, const RhConfig* c)
{
	BOOL ok = TRUE;
	ok &= freerdp_settings_set_string(s, FreeRDP_ServerHostname, c->host);
	ok &= freerdp_settings_set_uint32(s, FreeRDP_ServerPort, (UINT32)c->port);
	if (c->username)
		ok &= freerdp_settings_set_string(s, FreeRDP_Username, c->username);
	if (c->domain)
		ok &= freerdp_settings_set_string(s, FreeRDP_Domain, c->domain);
	if (c->password)
		ok &= freerdp_settings_set_string(s, FreeRDP_Password, c->password);
	ok &= freerdp_settings_set_uint32(s, FreeRDP_DesktopWidth, (UINT32)c->width);
	ok &= freerdp_settings_set_uint32(s, FreeRDP_DesktopHeight, (UINT32)c->height);
	ok &= freerdp_settings_set_uint32(s, FreeRDP_DesktopScaleFactor, (UINT32)c->desktopScale);
	ok &= freerdp_settings_set_uint32(s, FreeRDP_DeviceScaleFactor,
	                                  (UINT32)(c->deviceScale >= 180 ? 180 : (c->deviceScale >= 140 ? 140 : 100)));
	ok &= freerdp_settings_set_uint32(s, FreeRDP_ColorDepth, 32);
	ok &= freerdp_settings_set_uint32(s, FreeRDP_KeyboardLayout, c->keyboardLayout);
	ok &= freerdp_settings_set_bool(s, FreeRDP_UnicodeInput, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_HasHorizontalWheel, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_HasExtendedMouseEvent, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_EnableWindowsKey, TRUE);

	/* security */
	const BOOL a = strcmp(c->security, "auto") == 0;
	ok &= freerdp_settings_set_bool(s, FreeRDP_NegotiateSecurityLayer, a);
	ok &= freerdp_settings_set_bool(s, FreeRDP_NlaSecurity, a || strcmp(c->security, "nla") == 0);
	ok &= freerdp_settings_set_bool(s, FreeRDP_TlsSecurity, a || strcmp(c->security, "tls") == 0);
	ok &= freerdp_settings_set_bool(s, FreeRDP_RdpSecurity, a || strcmp(c->security, "rdp") == 0);
	ok &= freerdp_settings_set_bool(s, FreeRDP_ExtSecurity, FALSE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_IgnoreCertificate, FALSE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_AutoAcceptCertificate, FALSE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_AutoDenyCertificate, FALSE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_CertificateCallbackPreferPEM, FALSE);
	if (c->restrictedAdmin)
		ok &= freerdp_settings_set_bool(s, FreeRDP_RestrictedAdminModeRequired, TRUE);
	if (c->console)
		ok &= freerdp_settings_set_bool(s, FreeRDP_ConsoleSession, TRUE);
	if (c->kerberosRealm)
		ok &= freerdp_settings_set_string(s, FreeRDP_KerberosRealm, c->kerberosRealm);
	if (c->kdcUrl)
		ok &= freerdp_settings_set_string(s, FreeRDP_KerberosKdcUrl, c->kdcUrl);
	if (c->clientHostname)
		ok &= freerdp_settings_set_string(s, FreeRDP_ClientHostname, c->clientHostname);
	if (c->configDir)
		ok &= freerdp_settings_set_string(s, FreeRDP_ConfigPath, c->configDir);

	/* graphics: GFX pipeline (the server's best codec FreeRDP decodes) */
	ok &= freerdp_settings_set_bool(s, FreeRDP_SupportGraphicsPipeline, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_SupportDynamicChannels, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_SupportDisplayControl, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_DynamicResolutionUpdate, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_RedirectClipboard, c->clipboard);
	/* No device redirection (drives, printers, smart cards) in v1. FreeRDP
	 * still registers an empty rdpdr when RDP8 features or rdpsnd need it
	 * (freerdp_client_load_addins): no device is ever announced on it. */
	ok &= freerdp_settings_set_bool(s, FreeRDP_DeviceRedirection, FALSE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_RemoteConsoleAudio, c->audioRemote);
	ok &= freerdp_settings_set_bool(s, FreeRDP_AudioPlayback, c->audioLocal);
	ok &= freerdp_settings_set_bool(s, FreeRDP_AllowFontSmoothing, TRUE);
	ok &= freerdp_settings_set_bool(s, FreeRDP_AutoReconnectionEnabled, c->autoReconnect);
	ok &= freerdp_settings_set_bool(s, FreeRDP_TcpKeepAlive, TRUE);

	/* gateway (RD Gateway) */
	if (c->gwEnabled)
	{
		ok &= freerdp_settings_set_bool(s, FreeRDP_GatewayEnabled, TRUE);
		ok &= freerdp_settings_set_string(s, FreeRDP_GatewayHostname, c->gwHost);
		ok &= freerdp_settings_set_uint32(s, FreeRDP_GatewayPort, (UINT32)c->gwPort);
		ok &= freerdp_settings_set_bool(s, FreeRDP_GatewayUseSameCredentials, c->gwSame);
		ok &= freerdp_settings_set_bool(s, FreeRDP_GatewayBypassLocal, c->gwBypassLocal);
		if (!c->gwSame)
		{
			if (c->gwUser)
				ok &= freerdp_settings_set_string(s, FreeRDP_GatewayUsername, c->gwUser);
			if (c->gwDomain)
				ok &= freerdp_settings_set_string(s, FreeRDP_GatewayDomain, c->gwDomain);
			if (c->gwPassword)
				ok &= freerdp_settings_set_string(s, FreeRDP_GatewayPassword, c->gwPassword);
		}
		const BOOL http = strcmp(c->gwTransport, "rpc") != 0;
		const BOOL rpc = strcmp(c->gwTransport, "http") != 0;
		ok &= freerdp_settings_set_bool(s, FreeRDP_GatewayHttpTransport, http);
		ok &= freerdp_settings_set_bool(s, FreeRDP_GatewayRpcTransport, rpc);
		ok &= freerdp_set_gateway_usage_method(s, TSC_PROXY_MODE_DIRECT);
	}
	return ok;
}

static const char* g_cfg_error = NULL;

static void handle_connect(WINPR_JSON* o)
{
	if (g_cfg_ok != 0)
		return; /* one connect line per process */
	const char* why = parse_config(o, &g_cfg);
	json_wipe(o, "password");
	if (why)
	{
		g_cfg_error = why;
		free_config(&g_cfg);
		InterlockedExchange(&g_cfg_ok, -1);
	}
	else
		InterlockedExchange(&g_cfg_ok, 1);
	if (g_cfg_event)
		SetEvent(g_cfg_event);
}

static void handle_cert_reply(WINPR_JSON* o)
{
	if (g_reply_kind != 1)
		return;
	g_reply_accept = json_bool(o, "accept", FALSE) ? 1 : 0;
	InterlockedExchange(&g_reply_kind, -1);
	SetEvent(g_reply_event);
}

static void handle_credentials(WINPR_JSON* o)
{
	if (g_reply_kind != 2)
	{
		json_wipe(o, "password");
		return;
	}
	g_reply_cancel = json_bool(o, "cancel", FALSE) ? 1 : 0;
	free_secret(&g_reply_user);
	free_secret(&g_reply_domain);
	free_secret(&g_reply_pass);
	g_reply_user = dup_or_null(json_str(o, "username"));
	g_reply_domain = dup_or_null(json_str(o, "domain"));
	g_reply_pass = dup_or_null(json_str(o, "password"));
	json_wipe(o, "password");
	InterlockedExchange(&g_reply_kind, -2);
	SetEvent(g_reply_event);
}

/* Handles one input line. Returns FALSE when the line is not valid JSON. */
static BOOL handle_line(const char* line)
{
	WINPR_JSON* o = WINPR_JSON_Parse(line);
	if (!o)
		return FALSE;
	const char* type = json_str(o, "type");
	const char* action = json_str(o, "action");
	if (!type)
	{
		/* not an input message: ignored */
	}
	else if (strcmp(type, "connect") == 0)
		handle_connect(o);
	else if (strcmp(type, "cert_reply") == 0)
		handle_cert_reply(o);
	else if (strcmp(type, "credentials") == 0)
		handle_credentials(o);
	else if (strcmp(type, "disconnect") == 0)
	{
		InterlockedExchange(&g_quit_req, 1);
		InterlockedExchange(&g_type_cancel, 1);
		if (g_instance && g_instance->context)
			freerdp_abort_connect_context(g_instance->context);
		wake_main();
	}
	else if (g_cfg_ok != 1 && !g_testmode)
	{
		/* nothing but the prompts before the session exists */
	}
	else if (strcmp(type, "mouse") == 0 && action)
		inject_mouse(o, action);
	else if (strcmp(type, "key") == 0 && action)
		inject_key(o, action);
	else if (strcmp(type, "type_text") == 0)
	{
		const char* text = json_str(o, "text");
		if (text)
			start_typing(text);
		json_wipe(o, "text");
	}
	else if (strcmp(type, "type_cancel") == 0)
		stop_typing();
	else if (strcmp(type, "sas") == 0)
		inject_sas();
	else if (strcmp(type, "release_all") == 0)
	{
		EnterCriticalSection(&g_inLock);
		release_all_locked();
		LeaveCriticalSection(&g_inLock);
	}
	else if (strcmp(type, "sync") == 0)
	{
		UINT32 f = 0;
		if (json_bool(o, "caps", FALSE))
			f |= KBD_SYNC_CAPS_LOCK;
		if (json_bool(o, "num", FALSE))
			f |= KBD_SYNC_NUM_LOCK;
		if (json_bool(o, "scroll", FALSE))
			f |= KBD_SYNC_SCROLL_LOCK;
		EnterCriticalSection(&g_inLock);
		ev_sync(f);
		LeaveCriticalSection(&g_inLock);
	}
	else if (strcmp(type, "refresh") == 0)
	{
		InterlockedExchange(&g_refresh_req, 1);
		wake_main();
	}
	else if (strcmp(type, "resize") == 0)
		request_resize(o);
	else if (strcmp(type, "clipboard_set") == 0)
		clip_set_local(json_str(o, "text"));
	else if (strcmp(type, "bitrate") == 0)
	{
		const int k = json_int(o, "kbps", 0);
		if (k > 0)
		{
			InterlockedExchange(&g_kbps_req, k);
			wake_main();
		}
	}
	/* unknown types: ignored */
	WINPR_JSON_Delete(o);
	return TRUE;
}

/* Line splitter: lines longer than MAX_LINE are dropped whole. Every line is
 * wiped from the buffer once handled (it may hold a password). */
typedef struct
{
	char* buf;
	size_t len, cap;
	BOOL overflow;
} LineBuf;

static void feed_bytes(LineBuf* lb, const char* data, size_t n, void (*on_line)(const char*))
{
	for (size_t i = 0; i < n; i++)
	{
		const char c = data[i];
		if (c == '\n')
		{
			if (!lb->overflow && lb->len > 0)
			{
				if (lb->buf[lb->len - 1] == '\r')
					lb->len--;
				lb->buf[lb->len] = '\0';
				on_line(lb->buf);
				secure_wipe(lb->buf, lb->len);
			}
			lb->len = 0;
			lb->overflow = FALSE;
			continue;
		}
		if (lb->overflow)
			continue;
		if (lb->len + 2 > lb->cap)
		{
			size_t nc = lb->cap ? lb->cap * 2 : 4096;
			if (nc > MAX_LINE + 2)
				nc = MAX_LINE + 2;
			if (lb->len + 2 > nc)
			{
				lb->overflow = TRUE;
				secure_wipe(lb->buf, lb->len);
				lb->len = 0;
				continue;
			}
			char* nb = (char*)malloc(nc);
			if (!nb)
			{
				lb->overflow = TRUE;
				lb->len = 0;
				continue;
			}
			if (lb->buf)
			{
				memcpy(nb, lb->buf, lb->len);
				secure_wipe(lb->buf, lb->cap);
				free(lb->buf);
			}
			lb->buf = nb;
			lb->cap = nc;
		}
		lb->buf[lb->len++] = c;
	}
}

static void on_input_line(const char* line)
{
	(void)handle_line(line);
}

static DWORD WINAPI stdin_thread(LPVOID arg)
{
	(void)arg;
	LineBuf lb = { 0 };
	char chunk[8192];
	for (;;)
	{
#ifdef _WIN32
		DWORD got = 0;
		if (!ReadFile(GetStdHandle(STD_INPUT_HANDLE), chunk, sizeof(chunk), &got, NULL) || got == 0)
			break;
#else
		ssize_t got = read(STDIN_FILENO, chunk, sizeof(chunk));
		if (got < 0 && errno == EINTR)
			continue;
		if (got <= 0)
			break;
#endif
		feed_bytes(&lb, chunk, (size_t)got, on_input_line);
		secure_wipe(chunk, sizeof(chunk));
	}
	if (lb.buf)
	{
		secure_wipe(lb.buf, lb.cap);
		free(lb.buf);
	}
	fprintf(stderr, "stdin closed\n");
	InterlockedExchange(&g_stdin_closed, 1);
	InterlockedExchange(&g_type_cancel, 1);
	if (g_cfg_event)
		SetEvent(g_cfg_event);
	if (g_reply_event)
		SetEvent(g_reply_event);
	if (g_instance && g_instance->context)
		freerdp_abort_connect_context(g_instance->context);
	wake_main();
	return 0;
}

/* ── error classification ───────────────────────────────────────────────── */

typedef struct
{
	const char* code;
	BOOL retry; /* a network loss: worth a reconnection */
} EndReason;

static BOOL is_auth_errinfo(UINT32 info)
{
	return info == ERRINFO_SERVER_INSUFFICIENT_PRIVILEGES || info == ERRINFO_SERVER_FRESH_CREDENTIALS_REQUIRED;
}

/* Why a connection attempt or a session ended. `connecting`: the first
 * connection (no session yet). */
static EndReason classify_end(UINT32 err, UINT32 info, BOOL connecting)
{
	EndReason r = { "connect_failed", FALSE };
	if (g_quit_req)
	{
		r.code = "user_disconnect";
		return r;
	}
	if (g_stdin_closed)
	{
		r.code = "stdin_closed";
		return r;
	}
	if (g_cert_rejected)
	{
		r.code = "cert_rejected";
		return r;
	}
	if (g_cred_cancelled)
	{
		r.code = "auth_cancelled";
		return r;
	}
	switch (info)
	{
		case ERRINFO_DISCONNECTED_BY_OTHER_CONNECTION:
			r.code = "other_connection";
			return r;
		case ERRINFO_RPC_INITIATED_DISCONNECT:
		case ERRINFO_RPC_INITIATED_DISCONNECT_BY_USER:
			r.code = "server_disconnected";
			return r;
		case ERRINFO_RPC_INITIATED_LOGOFF:
		case ERRINFO_LOGOFF_BY_USER:
			r.code = "logged_off";
			return r;
		case ERRINFO_IDLE_TIMEOUT:
			r.code = "idle_timeout";
			return r;
		case ERRINFO_LOGON_TIMEOUT:
			r.code = "logon_timeout";
			return r;
		case ERRINFO_SERVER_DENIED_CONNECTION:
			r.code = "denied";
			return r;
		default:
			break;
	}
	if (is_auth_errinfo(info))
	{
		r.code = "auth_failed";
		return r;
	}
	if (info >= 0x100 && info <= 0x10A)
	{
		r.code = "license_error";
		return r;
	}
	if (err == FREERDP_ERROR_DISCONNECTED_BY_OTHER_CONNECTION)
	{
		r.code = "other_connection";
		return r;
	}
	if ((err >> 16) == FREERDP_ERROR_CONNECT_CLASS)
	{
		switch (err & 0xFFFF)
		{
			case ERRCONNECT_DNS_ERROR:
			case ERRCONNECT_DNS_NAME_NOT_FOUND:
				r.code = "dns_failed";
				return r;
			case ERRCONNECT_CONNECT_FAILED:
			case ERRCONNECT_CONNECT_TRANSPORT_FAILED:
				r.code = connecting ? "unreachable" : "reconnect_failed";
				r.retry = !connecting;
				return r;
			case ERRCONNECT_TLS_CONNECT_FAILED:
			case ERRCONNECT_SECURITY_NEGO_CONNECT_FAILED:
				r.code = "security_failed";
				return r;
			case 0x1E: /* ERRCONNECT_HYBRID_REQUIRED_BY_SERVER (FreeRDP >= 3.31) */
				r.code = "nla_required";
				return r;
			case ERRCONNECT_PASSWORD_EXPIRED:
			case ERRCONNECT_PASSWORD_CERTAINLY_EXPIRED:
			case ERRCONNECT_PASSWORD_MUST_CHANGE:
				r.code = "password_expired";
				return r;
			case ERRCONNECT_ACCOUNT_DISABLED:
			case ERRCONNECT_ACCOUNT_RESTRICTION:
			case ERRCONNECT_ACCOUNT_LOCKED_OUT:
			case ERRCONNECT_ACCOUNT_EXPIRED:
			case ERRCONNECT_LOGON_TYPE_NOT_GRANTED:
			case ERRCONNECT_CLIENT_REVOKED:
				r.code = "account_restricted";
				return r;
			case ERRCONNECT_KDC_UNREACHABLE:
				r.code = "kerberos_failed";
				return r;
			case ERRCONNECT_AUTHENTICATION_FAILED:
			case ERRCONNECT_INSUFFICIENT_PRIVILEGES:
			case ERRCONNECT_LOGON_FAILURE:
			case ERRCONNECT_WRONG_PASSWORD:
			case ERRCONNECT_ACCESS_DENIED:
			case ERRCONNECT_NO_OR_MISSING_CREDENTIALS:
				r.code = "auth_failed";
				return r;
			case ERRCONNECT_CONNECT_CANCELLED:
				r.code = "user_disconnect";
				return r;
			default:
				break;
		}
	}
	if (!connecting)
	{
		/* No reason from the server: the network went away. */
		r.code = "reconnect_failed";
		r.retry = TRUE;
	}
	return r;
}

static const char* err_name(UINT32 err)
{
	const char* s = freerdp_get_last_error_name(err);
	return s ? s : "";
}

static const char* err_text(UINT32 err, UINT32 info)
{
	if (info != 0 && info != ERRINFO_NONE)
	{
		const char* s = freerdp_get_error_info_string(info);
		if (s && *s)
			return s;
	}
	const char* s = freerdp_get_last_error_string(err);
	return s ? s : "unknown error";
}

/* ── self-test ──────────────────────────────────────────────────────────── */

static int g_fail = 0;
#define CHECK(cond, ...)       \
	do                         \
	{                          \
		if (cond)              \
			printf("ok   - "); \
		else                   \
		{                      \
			printf("FAIL - "); \
			g_fail++;          \
		}                      \
		printf(__VA_ARGS__);   \
		printf("\n");          \
	} while (0)

static int run_selftest(void)
{
	g_testmode = 1;
	g_type_delay = 0;
	InitializeCriticalSection(&g_outLock);
	InitializeCriticalSection(&g_inLock);
	InitializeCriticalSection(&g_clipLock);
	g_reply_event = CreateEvent(NULL, TRUE, FALSE, NULL);
	g_cfg_event = CreateEvent(NULL, TRUE, FALSE, NULL);

	/* scancode table */
	UINT8 sc;
	BOOL ext, sh;
	CHECK(code_to_sc("IntlBackslash", &sc, &ext) && sc == 0x56 && !ext, "IntlBackslash -> 0x56");
	CHECK(code_to_sc("AltRight", &sc, &ext) && sc == 0x38 && ext, "AltRight -> 0x38 ext");
	CHECK(code_to_sc("MetaLeft", &sc, &ext) && sc == 0x5B && ext, "MetaLeft -> 0x5B ext");
	CHECK(code_to_sc("Pause", &sc, &ext) && sc == SC_PAUSE, "Pause special");
	CHECK(!code_to_sc("Bogus", &sc, &ext), "unknown code refused");
	{
		int dup = 0;
		for (int i = 0; KMAP[i].code; i++)
			for (int j = i + 1; KMAP[j].code; j++)
				if (strcmp(KMAP[i].code, KMAP[j].code) == 0)
					dup++;
		CHECK(dup == 0, "no duplicate code in the table (%d)", dup);
	}
	/* US fallback */
	CHECK(ascii_us('a', &sc, &sh) && sc == 0x1E && !sh, "ascii a -> 0x1E");
	CHECK(ascii_us('Q', &sc, &sh) && sc == 0x10 && sh, "ascii Q -> 0x10 + shift");
	CHECK(ascii_us('@', &sc, &sh) && sc == 0x03 && sh, "ascii @ -> 0x03 + shift");
	CHECK(ascii_us('0', &sc, &sh) && sc == 0x0B && !sh, "ascii 0 -> 0x0B");
	CHECK(ascii_us('?', &sc, &sh) && sc == 0x35 && sh, "ascii ? -> 0x35 + shift");
	CHECK(ascii_us('m', &sc, &sh) && sc == 0x32, "ascii m -> 0x32");
	CHECK(!ascii_us(0x01, &sc, &sh), "control char refused");

	/* wheel flags */
	CHECK(wheel_flags(1, FALSE) == 0x0278, "wheel up = 0x0278 (got 0x%04X)", wheel_flags(1, FALSE));
	CHECK(wheel_flags(-1, FALSE) == 0x0388, "wheel down = 0x0388 (got 0x%04X)", wheel_flags(-1, FALSE));
	CHECK(wheel_flags(-1, TRUE) == 0x0588, "hwheel left = 0x0588 (got 0x%04X)", wheel_flags(-1, TRUE));

	/* connect line */
	{
		WINPR_JSON* o = WINPR_JSON_Parse(
		    "{\"type\":\"connect\",\"host\":\"srv.example\",\"port\":3390,\"username\":\"u\","
		    "\"password\":\"p@ss\",\"security\":\"nla\",\"width\":1921,\"height\":100,\"fps\":120,"
		    "\"keyboardLayout\":\"0000040C\",\"knownFingerprints\":[\"AB:CD:01\"],"
		    "\"gateway\":{\"host\":\"gw.example\",\"password\":\"g\"}}");
		RhConfig c;
		const char* why = parse_config(o, &c);
		CHECK(why == NULL, "connect line parsed (%s)", why ? why : "ok");
		CHECK(c.port == 3390 && strcmp(c.security, "nla") == 0, "port + security");
		CHECK(c.width == 1920 && c.height == 200, "size clamped to 1920x200 (got %dx%d)", c.width, c.height);
		CHECK(c.fps == 60, "fps clamped to 60");
		CHECK(c.keyboardLayout == 0x40C, "keyboard layout 0x40C");
		CHECK(c.gwEnabled && c.gwPort == 443 && c.gwSame, "gateway defaults (443, same credentials)");
		CHECK(c.password && strcmp(c.password, "p@ss") == 0, "password copied");
		g_cfg = c;
		CHECK(fp_known("sha256 ab:cd:01") && fp_known("ABCD01") && !fp_known("ab:cd:02"), "fingerprint compare");
		free_config(&g_cfg);
		WINPR_JSON_Delete(o);
		RhConfig c2;
		o = WINPR_JSON_Parse("{\"type\":\"connect\",\"host\":\"a b\"}");
		CHECK(parse_config(o, &c2) != NULL, "host with a space refused");
		free_config(&c2);
		WINPR_JSON_Delete(o);
		o = WINPR_JSON_Parse("{\"type\":\"connect\",\"port\":1}");
		CHECK(parse_config(o, &c2) != NULL, "host missing refused");
		free_config(&c2);
		WINPR_JSON_Delete(o);
	}

	/* clipboard conversions */
	{
		UINT32 n = 0;
		BYTE* w = utf8_to_cf_unicode("a\nb\xC3\xA9", &n);
		CHECK(w && n == 6 * 2 && w[2] == '\r' && w[4] == '\n', "clipboard: LF -> CRLF, UTF-16LE + NUL (%u bytes)",
		      (unsigned)n);
		char* back = cf_unicode_to_utf8(w, n);
		CHECK(back && strcmp(back, "a\nb\xC3\xA9") == 0, "clipboard: round trip");
		free(back);
		free(w);
	}

	/* status JSON: integers stay integers, strings escaped */
	{
		JB* j = status_new("cert");
		jadd_int(j, "port", 3389);
		jadd_str(j, "subject", "CN=\"a\\b\"\n");
		jadd_bool(j, "changed", TRUE);
		jb_put(j, "}", 1);
		CHECK(strcmp(j->buf, "{\"state\":\"cert\",\"port\":3389,\"subject\":\"CN=\\\"a\\\\b\\\"\\u000a\",\"changed\":true}") == 0,
		      "status JSON: %s", j->buf);
		WINPR_JSON* back = WINPR_JSON_Parse(j->buf);
		CHECK(back && json_int(back, "port", 0) == 3389, "status JSON parses back");
		WINPR_JSON_Delete(back);
		free(j);
	}

	/* layout clamp */
	{
		int w = 3, h = 9000;
		clamp_layout(&w, &h);
		CHECK(w == 200 && h == 8192, "layout clamp 200..8192");
		w = 1001;
		h = 700;
		clamp_layout(&w, &h);
		CHECK(w == 1000, "layout width even");
	}

	/* error classification */
	{
		EndReason r = classify_end(MAKE_FREERDP_ERROR(CONNECT, ERRCONNECT_LOGON_FAILURE), 0, TRUE);
		CHECK(strcmp(r.code, "auth_failed") == 0, "logon failure -> auth_failed");
		r = classify_end(MAKE_FREERDP_ERROR(CONNECT, ERRCONNECT_DNS_NAME_NOT_FOUND), 0, TRUE);
		CHECK(strcmp(r.code, "dns_failed") == 0, "dns -> dns_failed");
		r = classify_end(0, ERRINFO_LOGOFF_BY_USER, FALSE);
		CHECK(strcmp(r.code, "logged_off") == 0 && !r.retry, "logoff -> logged_off, no retry");
		r = classify_end(0, 0, FALSE);
		CHECK(r.retry, "no reason during a session -> reconnect");
		r = classify_end(MAKE_FREERDP_ERROR(CONNECT, ERRCONNECT_CONNECT_FAILED), 0, TRUE);
		CHECK(strcmp(r.code, "unreachable") == 0 && !r.retry, "first connect refused -> unreachable");
	}

	/* JSON lines -> events (desktop 800x600) */
	g_desk_w = 800;
	g_desk_h = 600;
	g_ntev = 0;
	CHECK(handle_line("{\"type\":\"key\",\"action\":\"down\",\"code\":\"IntlBackslash\",\"key\":\"<\"}"), "key line parsed");
	CHECK(g_ntev == 1 && g_tev[0].kind == 'k' && g_tev[0].a == 0x56 && g_tev[0].flags == 0, "key down 0x56 press flags 0");
	handle_line("{\"type\":\"key\",\"action\":\"up\",\"code\":\"ControlRight\"}");
	CHECK(g_ntev == 2 && g_tev[1].flags == (KBD_FLAGS_EXTENDED | KBD_FLAGS_RELEASE), "ControlRight up = ext|release");
	g_ntev = 0;
	handle_line("{\"type\":\"mouse\",\"action\":\"move\",\"x\":5000,\"y\":-3}");
	CHECK(g_ntev == 1 && g_tev[0].a == 799 && g_tev[0].b == 0, "mouse clamped to 799,0 (got %u,%u)", g_tev[0].a,
	      g_tev[0].b);
	handle_line("{\"type\":\"mouse\",\"action\":\"down\",\"button\":2,\"x\":10,\"y\":10}");
	CHECK(g_ntev == 2 && g_tev[1].flags == (PTR_FLAGS_BUTTON2 | PTR_FLAGS_DOWN), "right button down");
	g_ntev = 0;
	handle_line("{\"type\":\"release_all\"}");
	CHECK(g_ntev == 2, "release_all releases the held key + the button (%d events)", g_ntev);
	g_ntev = 0;
	handle_line("{\"type\":\"sas\"}");
	CHECK(g_ntev == 6 && g_tev[2].a == 0x4F && (g_tev[2].flags & KBD_FLAGS_EXTENDED), "sas = Ctrl+Alt+End (6 events)");
	g_ntev = 0;
	handle_line("{\"type\":\"sync\",\"caps\":true,\"num\":true}");
	CHECK(g_ntev == 1 && g_tev[0].kind == 's' && g_tev[0].flags == (KBD_SYNC_CAPS_LOCK | KBD_SYNC_NUM_LOCK), "sync flags");
	CHECK(!handle_line("not json"), "invalid JSON refused");

	/* typing: unicode server vs US fallback */
	g_ntev = 0;
	g_unicode_ok = TRUE;
	start_typing("\xE2\x82\xAC\n");
	CHECK(g_ntev == 4 && g_tev[0].kind == 'u' && g_tev[0].a == 0x20AC && g_tev[2].a == 0x1C,
	      "type '\\u20ac\\n' (unicode): unicode + Enter (%d events)", g_ntev);
	g_ntev = 0;
	g_unicode_ok = FALSE;
	start_typing("A\xE2\x82\xAC");
	CHECK(g_ntev == 4 && g_tev[0].a == 0x2A && g_tev[1].a == 0x1E, "type 'A\\u20ac' (no unicode): shift+a, euro skipped");

	/* resize request */
	g_resize_pending = 0;
	handle_line("{\"type\":\"resize\",\"width\":1367,\"height\":768}");
	CHECK(g_resize_pending == 1 && g_resize_w == 1366 && g_resize_h == 768, "resize queued at 1366x768");

	/* prompts */
	g_reply_kind = 1;
	handle_line("{\"type\":\"cert_reply\",\"accept\":true}");
	CHECK(g_reply_kind == -1 && g_reply_accept == 1, "cert_reply accepted");
	g_reply_kind = 0;
	handle_line("{\"type\":\"cert_reply\",\"accept\":true}");
	CHECK(g_reply_kind == 0, "cert_reply without a prompt ignored");
	g_reply_kind = 2;
	handle_line("{\"type\":\"credentials\",\"username\":\"x\",\"password\":\"y\"}");
	CHECK(g_reply_kind == -2 && g_reply_user && strcmp(g_reply_user, "x") == 0 && g_reply_pass &&
	          strcmp(g_reply_pass, "y") == 0,
	      "credentials stored");
	free_secret(&g_reply_user);
	free_secret(&g_reply_pass);
	g_reply_kind = 0;

	/* line splitter */
	{
		LineBuf lb = { 0 };
		const char* data = "{\"type\":\"refresh\"}\r\n\n{\"type\":\"refresh\"}\n";
		g_refresh_req = 0;
		feed_bytes(&lb, data, strlen(data), on_input_line);
		CHECK(g_refresh_req == 1, "line splitter: CRLF + empty lines handled");
		char* big = (char*)malloc(MAX_LINE + 100);
		memset(big, 'a', MAX_LINE + 50);
		big[MAX_LINE + 50] = '\n';
		g_refresh_req = 0;
		feed_bytes(&lb, big, MAX_LINE + 51, on_input_line);
		feed_bytes(&lb, data, strlen(data), on_input_line);
		CHECK(g_refresh_req == 1 && lb.len == 0, "oversize line dropped, next lines still parsed");
		free(big);
		free(lb.buf);
	}

	/* encoder: a 64x48 grey picture -> one IDR access unit */
	{
		g_fps = 30;
		CHECK(setup_encoder(64, 48), "x264 encoder opens (64x48)");
		memset(g_picin.img.plane[0], 128, (size_t)g_picin.img.i_stride[0] * 48);
		memset(g_picin.img.plane[1], 128, (size_t)g_picin.img.i_stride[1] * 24);
		memset(g_picin.img.plane[2], 128, (size_t)g_picin.img.i_stride[2] * 24);
		g_picin.i_type = X264_TYPE_IDR;
		g_picin.i_pts = 0;
		x264_nal_t* nals = NULL;
		int nn = 0;
		x264_picture_t po;
		const int sz = x264_encoder_encode(g_enc, &nals, &nn, &g_picin, &po);
		CHECK(sz > 0 && po.b_keyframe && nals[0].p_payload[0] == 0 && nals[0].p_payload[2] <= 1,
		      "IDR access unit, Annex-B start code (%d bytes)", sz);
		apply_kbps(100);
		CHECK(g_kbps == MIN_KBPS, "bitrate floor %d", MIN_KBPS);
		close_encoder();
	}

	printf("%s: %d failure(s)\n", g_fail ? "SELFTEST FAILED" : "SELFTEST OK", g_fail);
	return g_fail ? 1 : 0;
}

/* ── main ───────────────────────────────────────────────────────────────── */

/* Adaptive bitrate, once a second: slow writes (the reader lags) lower the
 * ceiling by a quarter, five calm seconds raise it by a tenth. */
static void adapt_bitrate(void)
{
	static ULONGLONG last = 0;
	static int calm = 0;
	const ULONGLONG now = GetTickCount64();
	const LONG req = InterlockedExchange(&g_kbps_req, 0);
	if (req > 0)
	{
		g_kbps_cap = req;
		apply_kbps(req);
	}
	if (now - last < 1000)
		return;
	last = now;
	const LONG slow = InterlockedExchange(&g_slow_writes, 0);
	if (slow >= 3)
	{
		calm = 0;
		apply_kbps(g_kbps * 3 / 4);
	}
	else if (slow == 0 && ++calm >= 5)
	{
		calm = 0;
		if (g_kbps < g_kbps_cap)
			apply_kbps(g_kbps + (g_kbps / 10 > 100 ? g_kbps / 10 : 100));
	}
}

/* Returns 1 when the resize needs a reconnection at the new size (no DISP
 * channel and resizeReconnect), else 0. */
static int handle_resize(rhContext* rh)
{
	if (!g_resize_pending)
		return 0;
	const ULONGLONG now = GetTickCount64();
	if (!rh->disp || !g_disp_ready)
	{
		/* Give the DISP channel a few seconds after the connection. */
		if (!g_connected || now - g_connected_at < 5000)
			return 0;
		if ((int)g_resize_w == (int)g_desk_w && (int)g_resize_h == (int)g_desk_h)
		{
			InterlockedExchange(&g_resize_pending, 0);
			return 0;
		}
		if (g_cfg.resizeReconnect)
		{
			if (now - g_last_resize < 1000) /* a window drag: wait until it settles */
				return 0;
			InterlockedExchange(&g_resize_pending, 0);
			g_last_resize = now;
			return 1;
		}
		InterlockedExchange(&g_resize_pending, 0);
		if (!g_resize_unsupported_sent)
		{
			g_resize_unsupported_sent = 1;
			emit_simple("resize_unsupported");
		}
		return 0;
	}
	if (now - g_last_resize < RESIZE_DEBOUNCE_MS)
		return 0;
	InterlockedExchange(&g_resize_pending, 0);
	g_last_resize = now;
	const int w = (int)g_resize_w, h = (int)g_resize_h;
	if (w == (int)g_desk_w && h == (int)g_desk_h && g_resize_ds == 0)
		return 0;
	if (!send_layout(rh, w, h, g_resize_ds ? (int)g_resize_ds : g_cfg.desktopScale,
	                 g_resize_dvs ? (int)g_resize_dvs : g_cfg.deviceScale))
		fprintf(stderr, "display layout refused by the channel\n");
	return 0;
}

/* Returns 0 when the session loop ends normally, 1 for a resize by
 * reconnection, -1 on an encoder failure. */
static int run_session_loop(freerdp* instance)
{
	rdpContext* ctx = instance->context;
	rhContext* rh = (rhContext*)ctx;
	while (!freerdp_shall_disconnect_context(ctx))
	{
		HANDLE handles[MAXIMUM_WAIT_OBJECTS];
		DWORD n = freerdp_get_event_handles(ctx, handles, MAXIMUM_WAIT_OBJECTS - 1);
		if (n == 0)
			return 0;
		handles[n++] = g_wake;
		DWORD timeout = 200;
		if (g_dirty || g_refresh_req)
		{
			const ULONGLONG el = GetTickCount64() - g_last_encode;
			timeout = el >= (ULONGLONG)g_frame_interval ? 0 : (DWORD)(g_frame_interval - el);
		}
		if (g_resize_pending && timeout > RESIZE_DEBOUNCE_MS)
			timeout = RESIZE_DEBOUNCE_MS;
		if (WaitForMultipleObjects(n, handles, FALSE, timeout) == WAIT_FAILED)
			return 0;
		ResetEvent(g_wake); /* the flags below are atomics: nothing is lost */
		if (!freerdp_check_event_handles(ctx))
			return 0;
		if (g_quit_req || g_stdin_closed || g_out_broken)
			return 0;
		adapt_bitrate();
		if (handle_resize(rh) == 1)
			return 1;
		if ((g_dirty || g_refresh_req) && GetTickCount64() - g_last_encode >= (ULONGLONG)g_frame_interval)
		{
			if (g_refresh_req)
			{
				InterlockedExchange(&g_dirty, 1);
				EnterCriticalSection(&g_clipLock);
				BYTE* pm = NULL;
				size_t pl = g_ptr_len;
				if (g_ptr_msg && pl)
				{
					pm = (BYTE*)malloc(pl);
					if (pm)
						memcpy(pm, g_ptr_msg, pl);
				}
				LeaveCriticalSection(&g_clipLock);
				if (pm)
				{
					emit_msg('P', pm, pl, NULL, 0);
					free(pm);
				}
			}
			if (!encode_now(ctx))
				return -1;
		}
	}
	return 0;
}

static void emit_connected(freerdp* instance)
{
	rdpContext* ctx = instance->context;
	rhContext* rh = (rhContext*)ctx;
	g_unicode_ok = freerdp_settings_get_bool(ctx->settings, FreeRDP_UnicodeInput);
	const int w = ctx->gdi ? (int)ctx->gdi->width : 0;
	const int h = ctx->gdi ? (int)ctx->gdi->height : 0;
	InterlockedExchange(&g_desk_w, w);
	InterlockedExchange(&g_desk_h, h);
	g_connected = 1;
	g_connected_at = GetTickCount64();
	JB* o = status_new("connected");
	jadd_int(o, "width", w);
	jadd_int(o, "height", h);
	jadd_bool(o, "unicode", g_unicode_ok);
	jadd_bool(o, "displayControl", rh->disp != NULL);
	jadd_bool(o, "clipboard", rh->cliprdr != NULL);
	jadd_bool(o, "gfx", freerdp_settings_get_bool(ctx->settings, FreeRDP_SupportGraphicsPipeline));
	emit_status_obj(o);
}

static void setup_output(void)
{
	/* The framed byte stream gets a PRIVATE duplicate of the stdout pipe,
	 * then fd 1 is pointed at stderr: anything else that writes to "stdout"
	 * (FreeRDP's WLog console appender, a stray printf in a library) lands in
	 * the diagnostics, never inside the frames (vmconsole lesson). */
#ifdef _WIN32
	int fdFrames = _dup(_fileno(stdout));
	if (fdFrames < 0 || !(g_out = _fdopen(fdFrames, "wb")))
		return;
	_setmode(fdFrames, _O_BINARY);
	setvbuf(g_out, NULL, _IOFBF, 1 << 16);
	fflush(stdout);
	_dup2(_fileno(stderr), _fileno(stdout));
	SetStdHandle(STD_OUTPUT_HANDLE, GetStdHandle(STD_ERROR_HANDLE));
	_setmode(_fileno(stdin), _O_BINARY);
#else
	int fdFrames = dup(STDOUT_FILENO);
	if (fdFrames < 0)
		return;
	(void)fcntl(fdFrames, F_SETFD, FD_CLOEXEC);
	if (!(g_out = fdopen(fdFrames, "wb")))
		return;
	setvbuf(g_out, NULL, _IOFBF, 1 << 16);
	fflush(stdout);
	dup2(STDERR_FILENO, STDOUT_FILENO);
#endif
	wLog* root = WLog_GetRoot();
	if (root && WLog_SetLogAppenderType(root, WLOG_APPENDER_CONSOLE))
	{
		wLogAppender* app = WLog_GetLogAppender(root);
		if (app)
			WLog_ConfigureAppender(app, "outputstream", (void*)"stderr");
	}
}

static void harden_process(void)
{
#if !defined(_WIN32)
	signal(SIGPIPE, SIG_IGN); /* a closed pipe is an error code, not a death */
#ifdef __linux__
	/* No core dumps, no ptrace by other same-user processes: the password
	 * lives in this process' memory while the session runs. */
	(void)prctl(PR_SET_DUMPABLE, 0, 0, 0, 0);
#endif
#endif
}

int main(int argc, char** argv)
{
	if (argc >= 2 && strcmp(argv[1], "--selftest") == 0)
		return run_selftest();
	if (argc >= 2 && strcmp(argv[1], "--version") == 0)
	{
		printf("rdphost protocol=%d freerdp=%s x264=%d\n", RH_PROTOCOL, freerdp_get_version_string(), X264_BUILD);
		return 0;
	}
	if (argc >= 2)
	{
		fprintf(stderr, "usage: rdphost (connection parameters as a JSON line on stdin) | --selftest | --version\n");
		return 2;
	}
	harden_process();
	{
		const char* d = getenv("RDPHOST_TYPE_DELAY_MS");
		if (d && *d)
		{
			const int v = atoi(d);
			if (v >= 0 && v <= 1000)
				g_type_delay = (DWORD)v;
		}
	}
	InitializeCriticalSection(&g_outLock);
	InitializeCriticalSection(&g_inLock);
	InitializeCriticalSection(&g_clipLock);
	/* Manual-reset events only: WinPR has no auto-reset events on POSIX. */
	g_wake = CreateEvent(NULL, TRUE, FALSE, NULL);
	g_cfg_event = CreateEvent(NULL, TRUE, FALSE, NULL);
	g_reply_event = CreateEvent(NULL, TRUE, FALSE, NULL);
	if (!g_wake || !g_cfg_event || !g_reply_event)
		return 3;
#ifdef _WIN32
	WSADATA wsaData;
	if (WSAStartup(MAKEWORD(2, 2), &wsaData) != 0)
		return 3;
#endif
	setup_output();
	if (!g_out)
	{
		fprintf(stderr, "cannot duplicate stdout for the frame stream\n");
		return 6;
	}

	/* The input reader starts first: a closed stdin aborts even a connect in progress. */
	HANDLE th = CreateThread(NULL, 0, stdin_thread, NULL, 0, NULL);
	if (!th)
		return 3;
	CloseHandle(th);

	emit_simple("waiting_config");
	if (WaitForSingleObject(g_cfg_event, CONFIG_WAIT_MS) != WAIT_OBJECT_0 || g_cfg_ok != 1)
	{
		if (g_cfg_ok == -1)
			emit_ended("config_invalid", "", g_cfg_error ? g_cfg_error : "invalid connect line", 0, 0);
		else if (g_stdin_closed)
			emit_ended("stdin_closed", "", "Input closed before the connect line", 0, 0);
		else
			emit_ended("config_invalid", "", "No connect line received", 0, 0);
		return 2;
	}
	g_fps = g_cfg.fps;
	g_frame_interval = 1000 / g_fps;
	g_kbps_cap = g_cfg.maxKbps;
	snprintf(g_profile, sizeof(g_profile), "%s", g_cfg.profile);

	RDP_CLIENT_ENTRY_POINTS ep = { 0 };
	ep.Size = sizeof(ep);
	ep.Version = RDP_CLIENT_INTERFACE_VERSION;
	ep.ContextSize = sizeof(rhContext);
	ep.ClientNew = rh_client_new;
	rdpContext* context = freerdp_client_context_new(&ep);
	if (!context)
	{
		emit_ended("connect_failed", "", "FreeRDP context creation failed", 0, 0);
		return 3;
	}
	freerdp* instance = context->instance;
	g_instance = instance;
	g_input = context->input;
	if (!apply_settings(context->settings, &g_cfg))
	{
		emit_ended("config_invalid", "", "FreeRDP refused a setting", 0, 0);
		freerdp_client_context_free(context);
		return 2;
	}
	/* Our copies of the secrets are no longer needed (FreeRDP keeps its own
	 * for NLA re-authentication on reconnect). */
	free_secret(&g_cfg.password);
	free_secret(&g_cfg.gwPassword);

	JB* st = status_new("connecting");
	jadd_str(st, "host", g_cfg.host);
	jadd_int(st, "port", g_cfg.port);
	emit_status_obj(st);

	int rc = 0;
	if (!freerdp_connect(instance))
	{
		const UINT32 err = freerdp_get_last_error(context);
		const UINT32 info = freerdp_error_info(instance);
		fprintf(stderr, "CONNECT FAILED: 0x%08x (errinfo 0x%08x) %s\n", (unsigned)err, (unsigned)info,
		        err_text(err, info));
		const EndReason r = classify_end(err, info, TRUE);
		emit_ended(r.code, err_name(err), err_text(err, info), err, info);
		rc = (g_stdin_closed || g_quit_req) ? 0 : 4;
		goto done;
	}
	emit_connected(instance);

	for (;;)
	{
		const int lr = run_session_loop(instance);
		if (lr < 0)
		{
			rc = 7;
			break;
		}
		if (g_out_broken)
		{
			rc = 6;
			break;
		}
		const char* why = "network";
		if (lr == 1)
		{
			/* Resize without DISP: reconnect at the new size (the server
			 * resumes the same session, like mstsc on old servers). */
			why = "resize";
			fprintf(stderr, "resize by reconnection -> %dx%d\n", (int)g_resize_w, (int)g_resize_h);
			(void)freerdp_settings_set_uint32(context->settings, FreeRDP_DesktopWidth, (UINT32)g_resize_w);
			(void)freerdp_settings_set_uint32(context->settings, FreeRDP_DesktopHeight, (UINT32)g_resize_h);
		}
		else
		{
			const UINT32 err = freerdp_get_last_error(context);
			const UINT32 info = freerdp_error_info(instance);
			EndReason r = classify_end(err, info, FALSE);
			fprintf(stderr, "session ended: 0x%08x (errinfo 0x%08x) %s -> %s\n", (unsigned)err, (unsigned)info,
			        err_text(err, info), r.code);
			if (!r.retry || !g_cfg.autoReconnect)
			{
				emit_ended(r.code, err_name(err), err_text(err, info), err, info);
				rc = (strcmp(r.code, "user_disconnect") == 0 || strcmp(r.code, "stdin_closed") == 0) ? 0 : 7;
				break;
			}
		}
		/* Unexpected loss: try again on the same instance (auto-reconnect cookie). */
		EnterCriticalSection(&g_inLock);
		memset(g_keydown, 0, sizeof(g_keydown));
		g_buttons = 0;
		LeaveCriticalSection(&g_inLock);
		const ULONGLONG start = GetTickCount64();
		int attempt = 0;
		BOOL back = FALSE;
		while (!g_stdin_closed && !g_out_broken && !g_quit_req && GetTickCount64() - start < RECONNECT_WINDOW_MS)
		{
			attempt++;
			JB* o = status_new("reconnecting");
			jadd_int(o, "attempt", attempt);
			jadd_str(o, "reason", why);
			emit_status_obj(o);
			if (freerdp_reconnect(instance))
			{
				back = TRUE;
				break;
			}
			const EndReason r2 = classify_end(freerdp_get_last_error(context), freerdp_error_info(instance), FALSE);
			if (!r2.retry)
				break;
			for (int i = 0; i < RECONNECT_EVERY_MS / 100 && !g_stdin_closed && !g_quit_req; i++)
				Sleep(100);
		}
		if (!back)
		{
			const UINT32 e3 = freerdp_get_last_error(context);
			const UINT32 i3 = freerdp_error_info(instance);
			const EndReason r3 = classify_end(e3, i3, FALSE);
			emit_ended(r3.retry ? "reconnect_failed" : r3.code, err_name(e3), err_text(e3, i3), e3, i3);
			rc = (g_stdin_closed || g_quit_req) ? 0 : 7;
			break;
		}
		/* Same instance, maybe a new size: new keyframe + init. */
		g_init_sent = 0;
		g_force_idr = 1;
		InterlockedExchange(&g_dirty, 1);
		InterlockedExchange(&g_refresh_req, 1);
		emit_connected(instance);
	}
	freerdp_disconnect(instance);
done:
	stop_typing();
	close_encoder();
	if (g_out)
		fflush(g_out);
	g_instance = NULL;
	g_input = NULL;
	if (context->gdi)
		gdi_free(instance);
	freerdp_client_context_free(context);
	free_config(&g_cfg);
	EnterCriticalSection(&g_clipLock);
	free(g_clip_local);
	g_clip_local = NULL;
	free(g_ptr_msg);
	g_ptr_msg = NULL;
	LeaveCriticalSection(&g_clipLock);
	if (g_out)
		fclose(g_out);
	return rc;
}
