package main

// reachCodecCSS / reachCodecJS: the session toolbar's video codec selector,
// right after the "1920×1080 @ 30fps · H.264" readout.
//
// Injected into the page like reachViewerJS (strings.Replace, not Sprintf:
// '%' needs no escaping; no backticks, no closing script tag).
//
// Protocol (agent: engine/capture_loop_windows.go, engine/stream.go):
//   agent -> viewer  init {codec:'h264', codecs:['h264','jpeg','vp9','av1'
//                    (+ 'h265' only with its DLL)], ...}
//   viewer -> agent  {type:'set_codec', codec}
//   agent -> viewer  {type:'codec_switch', codec}, then frames of that codec
// The agent answers codec_switch with the codec ASKED even when its encoder
// could not start (it then encodes JPEG): a choice is applied only when a
// frame of that codec arrives; a frame of another codec after the switch, a
// codec_switch naming another codec or no frame in time is a fallback, said
// in a toast. Every init restarts the stream on H.264; the agent's helper
// applies the chosen codec again by itself (a codec_switch follows), the
// direct path does not: after a re-init within the same connection the
// viewer asks again only when nothing came.
//
// The list: the codecs the agent announced (init.codecs) AND that this
// WebView2 decodes (VideoDecoder.isConfigSupported with the codec strings
// reachCodecString configures, probed once). An agent that announces none
// (older) gets no selector; nor does a session with a single usable codec.
//
// The choice is remembered per device (this computer, every window:
// localStorage 'oblireach_codec.<server:device>') and applied again after
// each new connection when the agent offers it. One setting per session: in
// a mosaic of tabs (tab_groups.go) the toolbar is the member in focus's, so
// the selector applies to that tile's session only; the other tiles keep
// their own codec. A choice that falls back is forgotten for that device.

const reachCodecCSS = `
.reach-codec{display:inline-flex;align-items:center;flex-shrink:0}
.remote-toolbar .odd-trigger.reach-codec-sel{padding:5px 9px;border-radius:9px;font-size:11px;background-color:rgb(var(--ink) / .06);color:var(--muted)}
.remote-toolbar .odd-trigger.reach-codec-sel:hover,.remote-toolbar .odd-trigger.reach-codec-sel.open{color:var(--text)}
.remote-toolbar .odd-trigger.reach-codec-sel.pending{opacity:.75}
.remote-toolbar .odd-trigger.reach-codec-sel.pending .odd-label::after{content:'\2026'}
`

const reachCodecJS = `
// ── Video codec (reach_codec.go) ───────────────────────────────────────────
var REACH_CODEC_ORDER = ['h264', 'vp9', 'av1', 'h265', 'jpeg'];
var REACH_CODEC_TIMEOUT_MS = 10000;
var REACH_CODEC_REINIT_MS = 2500;
var REACH_CODEC_PREF = 'oblireach_codec.';
// The short note after each codec in the list (catalog keys).
var REACH_CODEC_HINT = {
  h264: 'viewer.codec.hint.h264',
  vp9: 'viewer.codec.hint.vp9',
  av1: 'viewer.codec.hint.av1',
  h265: 'viewer.codec.hint.h265',
  jpeg: 'viewer.codec.hint.jpeg',
};
var reachCodecDecodable = null; // codec -> decodable by this WebView2, once probed
var reachCodecProbing = null;   // the probe running

function reachCodecKnown(c) { return typeof c === 'string' && REACH_CODEC_ORDER.indexOf(c) >= 0; }
function reachCodecLabel(c) { return REACH_CODEC_LABEL[c] || String(c || ''); }
function reachCodecLive(tab) { return !!(tab && tab.ws && tab.gotInit); }
// init.codecs, known names only, in the menu's order; null: none announced.
function reachCodecList(info) {
  if (!info || !Array.isArray(info.codecs)) return null;
  var seen = {};
  info.codecs.forEach(function (c) { if (reachCodecKnown(c)) seen[c] = true; });
  return REACH_CODEC_ORDER.filter(function (c) { return !!seen[c]; });
}
// What this WebView2 decodes: asked once (the answer does not change while
// the app runs). JPEG is drawn by createImageBitmap, always there.
function reachCodecProbe() {
  if (reachCodecDecodable) return Promise.resolve(reachCodecDecodable);
  if (reachCodecProbing) return reachCodecProbing;
  var res = { jpeg: true };
  var video = ['h264', 'vp9', 'av1', 'h265'];
  if (typeof VideoDecoder === 'undefined' || !VideoDecoder || typeof VideoDecoder.isConfigSupported !== 'function') {
    video.forEach(function (c) { res[c] = false; });
    reachCodecDecodable = res;
    return Promise.resolve(res);
  }
  reachCodecProbing = Promise.all(video.map(function (c) {
    var p;
    try { p = VideoDecoder.isConfigSupported({ codec: reachCodecString(c), codedWidth: 1920, codedHeight: 1080, optimizeForLatency: true }); } catch (e) { p = null; }
    return Promise.resolve(p).then(function (r) { res[c] = !!(r && r.supported); }, function () { res[c] = false; });
  })).then(function () { reachCodecDecodable = res; reachCodecProbing = null; return res; });
  return reachCodecProbing;
}
// The codecs this session can switch to.
function reachCodecChoices(tab) {
  if (!tab || !tab.codecs || !reachCodecDecodable) return [];
  return tab.codecs.filter(function (c) { return !!reachCodecDecodable[c]; });
}

// ── The device's remembered choice (this computer) ──
function reachCodecPrefKey(tab) { return REACH_CODEC_PREF + devKey(tab.device); }
function reachCodecSaved(tab) { var v = reachPref(reachCodecPrefKey(tab), ''); return reachCodecKnown(v) ? v : ''; }
function reachCodecRemember(tab, c) { reachSetPref(reachCodecPrefKey(tab), c); }
function reachCodecForget(tab) { try { localStorage.removeItem(reachCodecPrefKey(tab)); } catch (e) {} }
// The codec wanted for this session: its own choice, else the device's.
function reachCodecWanted(tab) { return reachCodecKnown(tab.codecWant) ? tab.codecWant : reachCodecSaved(tab); }

function reachCodecClear(tab) {
  var p = tab && tab.codecPending;
  if (p && p.timer) clearTimeout(p.timer);
  if (tab) tab.codecPending = null;
  return p;
}
// Ask the agent for codec c. quiet: the remembered choice applied again
// (no "switching" / "applied" toast; a fallback is still said).
function reachCodecSet(tab, c, quiet) {
  if (!reachCodecLive(tab) || tab.codecPending || reachCodecChoices(tab).indexOf(c) < 0) return false;
  if (!quiet) { tab.codecWant = c; reachCodecRemember(tab, c); }
  if (c === tab.codec) { reachRefreshCodec(tab); return false; }
  if (!reachSend(tab, { type: 'set_codec', codec: c })) return false;
  var p = { codec: c, switched: false, quiet: !!quiet, timer: null };
  tab.codecPending = p;
  p.timer = setTimeout(function () { if (tab.codecPending === p) reachCodecFail(tab, 'timeout', tab.codec); }, REACH_CODEC_TIMEOUT_MS);
  if (!quiet && reachUI(tab)) reachToast(T('viewer.codec.switching', { codec: reachCodecLabel(c) }), '');
  reachRefreshCodec(tab);
  return true;
}
function reachCodecDone(tab) {
  var p = reachCodecClear(tab);
  if (p && !p.quiet && reachUI(tab)) reachToast(T('viewer.codec.applied', { codec: reachCodecLabel(p.codec) }), 'ok');
  reachRefreshCodec(tab);
}
// The agent did not switch (why: 'fallback' | 'timeout'); got: what streams.
function reachCodecFail(tab, why, got) {
  var p = reachCodecClear(tab);
  if (!p) return;
  // Forgotten for this device: it would fall back again at each connection.
  if (reachCodecSaved(tab) === p.codec) reachCodecForget(tab);
  if (tab.codecWant === p.codec) tab.codecWant = '';
  if (reachUI(tab)) reachToast(T(why === 'timeout' ? 'viewer.codec.timeout' : 'viewer.codec.fallback', { codec: reachCodecLabel(p.codec), got: reachCodecLabel(got || tab.codec) }), 'warn');
  reachRefreshCodec(tab);
}
// codec_switch from the agent (after reachOnCodecSwitch took it).
function reachCodecOnSwitch(tab, c) {
  var p = tab.codecPending;
  if (p) {
    if (c === p.codec) p.switched = true;
    else reachCodecFail(tab, 'fallback', c);
  }
  reachRefreshCodec(tab);
}
// A frame of codec c decoded / drawn while a switch is pending.
function reachCodecOnFrame(tab, c) {
  var p = tab.codecPending;
  if (!p) return;
  if (c === p.codec) reachCodecDone(tab);
  else if (p.switched) reachCodecFail(tab, 'fallback', c);
}
// After every init: the agent's list, and the wanted codec applied again
// (a new connection: asked at once; a re-init: only when the agent's helper
// did not switch back by itself).
function reachCodecOnInit(tab, info) {
  tab.codecs = reachCodecList(info);
  if (tab.codecReinitTimer) { clearTimeout(tab.codecReinitTimer); tab.codecReinitTimer = null; }
  if (tab.codecPending) tab.codecPending.switched = false; // (the stream restarted on H.264: its codec_switch comes again)
  var conn = tab.ws, n = tab.initCount;
  var go = function () {
    if (tab.ws !== conn || tab.initCount !== n || !reachCodecLive(tab)) return;
    reachRefreshCodec(tab);
    var want = reachCodecWanted(tab);
    if (!want || want === 'h264' || tab.codecPending || reachCodecChoices(tab).indexOf(want) < 0) return;
    if (n === 1) { reachCodecSet(tab, want, true); return; }
    tab.codecReinitTimer = setTimeout(function () {
      tab.codecReinitTimer = null;
      if (tab.ws !== conn || tab.initCount !== n || !reachCodecLive(tab) || tab.codecPending) return;
      if (tab.codec === 'h264') reachCodecSet(tab, want, true);
    }, REACH_CODEC_REINIT_MS);
  };
  if (reachCodecDecodable) go(); else reachCodecProbe().then(go);
}
function reachCodecReset(tab) {
  reachCodecClear(tab);
  if (tab.codecReinitTimer) { clearTimeout(tab.codecReinitTimer); tab.codecReinitTimer = null; }
  tab.codecs = null;
}

// ── The selector (the session ON SCREEN; called by reachRefreshToolbar) ──
function reachRefreshCodec(tab) {
  if (!tab || !reachUI(tab)) return;
  var wrap = document.getElementById('reach-codec');
  var sel = document.getElementById('reach-codec-sel');
  if (!wrap || !sel) return;
  var choices = reachCodecLive(tab) ? reachCodecChoices(tab) : [];
  var show = choices.length > 1;
  wrap.style.display = show ? '' : 'none';
  if (!show) return;
  var p = tab.codecPending;
  var cur = p ? p.codec : tab.codec;
  var list = choices.slice();
  if (list.indexOf(cur) < 0) list.push(cur); // (a fallback not offered: the trigger still says what streams)
  var key = list.join(',') + '|' + choices.join(',') + '|' + i18nLang();
  if (sel._codecKey !== key) {
    sel._codecKey = key;
    sel.innerHTML = '';
    list.forEach(function (c) {
      var o = document.createElement('option');
      o.value = c;
      o.textContent = reachCodecLabel(c);
      var hint = T(REACH_CODEC_HINT[c]);
      o.setAttribute('data-hint', hint);
      o.title = hint;
      if (choices.indexOf(c) < 0) o.disabled = true;
      sel.appendChild(o);
    });
  }
  sel.value = cur;
  sel.disabled = !!p;
  sel.classList.toggle('pending', !!p);
  var title = p ? T('viewer.codec.switching', { codec: reachCodecLabel(p.codec) }) : T('viewer.codec.titleCurrent', { codec: reachCodecLabel(tab.codec) });
  sel.title = title;
  sel.setAttribute('aria-label', title);
}
function reachBuildCodecTool(toolbar) {
  var wrap = document.createElement('span'); wrap.className = 'reach-codec'; wrap.id = 'reach-codec';
  wrap.style.display = 'none';
  var sel = document.createElement('select'); sel.className = 'reach-codec-sel'; sel.id = 'reach-codec-sel';
  sel.title = T('viewer.codec.title');
  sel.setAttribute('aria-label', T('viewer.codec.title'));
  sel.addEventListener('change', function () {
    var tab = reachDisplayedTab();
    if (!tab) return;
    if (!reachCodecSet(tab, sel.value)) reachRefreshCodec(tab);
  });
  wrap.appendChild(sel);
  toolbar.appendChild(wrap);
  return wrap;
}
`
