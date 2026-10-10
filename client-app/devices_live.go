package main

// devices_live.go — the device lists kept up to date: devices going online,
// offline, or newly added are refreshed regularly.
//
// Two sources, both bounded to what the server already lets this user see:
//
//   - A light periodic read of GET /api/reach/overview, per server (the same
//     request as a full load: ?tenantId=all in multi-server mode, ?agents=1),
//     never more than one in flight per server, each server on its own (a
//     slow or unreachable one never holds the others). It carries the last
//     answer's ETag (If-None-Match: Express answers 304 when nothing changed,
//     no body sent) and the answer is compared with the last one (a hash of
//     its text): nothing changed, nothing is drawn again. Something changed:
//     that server's data are replaced (the lists' own rules: srvStamp,
//     srvStampTools, explorerCtxPruneGone) and the lists drawn again ONCE,
//     keeping the search, the filters, the folding, the ticked devices (what
//     the data still hold: dsPrune / dpPrune), the scroll and the focus.
//     How often (Settings > Application > "Device list refresh",
//     DL_KEY): Automatic (default) = every DL_AUTO_MS while the window is in
//     front, every DL_BG_MS behind other windows, minimized or hidden, read
//     at once when it comes back to the front (unless read within
//     DL_FRONT_GAP_MS); 15 / 30 / 60 s = that while visible (DL_BG_MS when
//     hidden); Manual = never (the Refresh buttons, the socket events).
//     The main window reads; a session window only while its "+" list is
//     open (its tabs are never touched: their device records stay theirs).
//     Two reads failing in a row: the server's section says so (its usual
//     message, "Try again"), the status bar in single mode; no toast.
//
//   - The socket.io events the server already sends each server's socket
//     (ui.go chatSocketFor, one per server signed in): DEVICE_UPDATED (status,
//     name, agent version, privacy), DEVICE_OFFLINE, OBLIREACH_AGENT_UPDATED
//     (ObliReach connected: online, version), applied ONLY to a device the
//     lists already hold (the overview's filters decided it: an event about
//     any other device is ignored, never added: these events go to the whole
//     tenant room); DEVICE_DELETED removes that device; DEVICE_DELETED and
//     DEVICE_APPROVED ask that server's list again soon (a new device only
//     ever comes from the overview). A device patched is a new record (copy
//     on write), never the one a tab holds.
//
// A device that was not in a server's previous list gets a discreet "New"
// chip for DL_NEW_MS (not at the first read, not after a tenant switch).
//
// Injected verbatim after vms_page.go's (ui.go buildUI, not through Sprintf);
// no backtick here.

const devLiveCSS = `
/* ── Device lists kept up to date (devices_live.go) ── */
.dt .tag-chip.dev-new{margin-inline-start:6px;background:rgb(var(--accent-rgb) / .14);color:var(--accent);font-weight:600}
`

const devLiveJS = domPatchJS + `
// ── Device lists kept up to date (devices_live.go) ──────────────────────────
var DL_KEY = 'oblireach_dev_refresh';
var DL_MODES = ['auto', '15', '30', '60', 'manual'];
var DL_AUTO_MS = 30000;       // Automatic, the window in front
var DL_BG_MS = 180000;        // behind other windows, minimized, hidden
var DL_FRONT_GAP_MS = 10000;  // back in front: read at once unless read this recently
var DL_NEW_MS = 5 * 60000;    // the "New" chip
var DL_REDRAW_MS = 1000;      // the redraws of a burst of changes, as one (rows updated element-wise)
var DL_SOON_MS = 2000;        // a device deleted / approved: its server read again this soon
var DL_FAILS_SHOWN = 2;       // reads failing in a row before the server says so
var DL_TIMEOUT_MS = 45000;
var dlStarted = false;
var dlTimer = null, dlRedrawTimer = null, dlNewTimer = null;
var dlLastAt = 0;             // the last read of the lists (full or light)
var dlOvSeq = 0;              // single mode: a full read began (loadOverview): a light one in flight is dropped
var dlPrevReady = null;       // the status bar's "Ready" text before the change being drawn
var dlFailText = '';          // single mode: the status bar's text of a failing read
var dlSrv = new Map();        // profile id -> { etag, sig, ids, tk, at, fails, inflight, patchSeq, soon }
var dlNew = new Map();        // device key -> when it first appeared

function dlSt(pid) {
  pid = pid || '';
  var s = dlSrv.get(pid);
  if (!s) { s = { etag: '', sig: '', ids: null, tk: '', at: 0, fails: 0, inflight: false, patchSeq: 0, soon: null }; dlSrv.set(pid, s); }
  return s;
}

// ── The setting ──
function devRefreshMode() {
  var v = null;
  try { v = localStorage.getItem(DL_KEY); } catch (e) { v = null; }
  return DL_MODES.indexOf(v) >= 0 ? v : 'auto';
}
function devRefreshSetMode(m) {
  m = DL_MODES.indexOf(m) >= 0 ? m : 'auto';
  try { if (m === 'auto') localStorage.removeItem(DL_KEY); else localStorage.setItem(DL_KEY, m); } catch (e) {}
  dlSchedule();
}
// The window in front: visible, and active (the native frame's state when
// there is one: the focus may sit in a native child; else the document's).
function dlVisible() {
  var d = typeof document !== 'undefined' ? document : null;
  return !(d && (d.hidden === true || d.visibilityState === 'hidden'));
}
function dlForeground() {
  if (!dlVisible()) return false;
  try {
    if (typeof reachWinAvailable === 'function' && reachWinAvailable() && typeof reachWinState === 'object' && reachWinState) return reachWinState.active !== false;
  } catch (e) {}
  try { if (typeof document.hasFocus === 'function') return !!document.hasFocus(); } catch (e) {}
  return true;
}
// How long between two reads now (0: never, Manual).
function dlIntervalMs() {
  var m = devRefreshMode();
  if (m === 'manual') return 0;
  if (m === 'auto') return dlForeground() ? DL_AUTO_MS : DL_BG_MS;
  return dlVisible() ? Number(m) * 1000 : DL_BG_MS;
}
// What reads: the main window; a session window while its "+" list is open.
function dlActive() {
  if (!dlStarted || typeof appReady === 'undefined' || !appReady) return false;
  if (typeof reachDetachedMode !== 'undefined' && reachDetachedMode) return typeof devPickerIsOpen === 'function' && devPickerIsOpen();
  return true;
}
// The servers read: multi-server mode every server signed in (the tenant
// menu narrows what is shown, not what is kept); single mode this window's.
function dlTargets() {
  if (serverMode === 'multi') return obServers().filter(function (p) { return srvState(p.id).signedIn === true; }).map(function (p) { return p.id; });
  if (typeof activeServerIsLocal === 'function' && activeServerIsLocal() && !(typeof reachDetachedMode !== 'undefined' && reachDetachedMode && winPid())) return [];
  return [homePid()];
}

// ── The timer ──
function dlStart() {
  if (!dlStarted) {
    dlStarted = true;
    try { if (typeof document !== 'undefined' && document && typeof document.addEventListener === 'function') document.addEventListener('visibilitychange', function () { dlWake(); }); } catch (e) {}
    try {
      if (typeof window !== 'undefined' && window && typeof window.addEventListener === 'function') {
        window.addEventListener('focus', function () { dlWake(); });
        window.addEventListener('blur', function () { dlSchedule(); });
        // (the setting changed in another window of the app)
        window.addEventListener('storage', function (e) { if (e && e.key === DL_KEY) dlSchedule(); });
      }
    } catch (e) {}
  }
  if (!dlLastAt) dlLastAt = Date.now(); // (the lists were just read: enterApp)
  dlSchedule();
}
function dlSchedule() {
  if (dlTimer !== null) { clearTimeout(dlTimer); dlTimer = null; }
  if (!dlActive()) return;
  var ms = dlIntervalMs();
  if (!ms) return;
  var wait = Math.max(1000, ms - Math.max(0, Date.now() - dlLastAt));
  dlTimer = setTimeout(dlTick, wait);
}
function dlTick() {
  dlTimer = null;
  if (!dlActive() || !dlIntervalMs()) return;
  dlPollAll().then(dlSchedule, dlSchedule);
}
// Back in front (focus, visible, the frame active): read at once when the
// last read is not recent, then the timer of the new state.
function dlWake() {
  if (dlRedrawHidden && dlVisible()) dlRedrawSoon(50);
  if (!dlActive()) { dlSchedule(); return; }
  if (dlForeground() && dlIntervalMs() && Date.now() - dlLastAt >= DL_FRONT_GAP_MS) {
    if (dlTimer !== null) { clearTimeout(dlTimer); dlTimer = null; }
    dlPollAll().then(dlSchedule, dlSchedule);
    return;
  }
  dlSchedule();
}
// Every server, each on its own: one slow server never holds the others
// (each applies its answer as it comes).
function dlPollAll() {
  dlLastAt = Date.now();
  return Promise.all(dlTargets().map(function (pid) { return dlPollOne(pid).catch(function () {}); }));
}
// A server read again soon (a device deleted or approved there), Manual aside.
function dlPollSoon(pid) {
  if (!dlActive() || devRefreshMode() === 'manual') return;
  var s = dlSt(pid);
  if (s.soon !== null) return;
  s.soon = setTimeout(function () { s.soon = null; if (dlActive() && dlTargets().indexOf(pid || '') >= 0) dlPollOne(pid || '').catch(function () {}); }, DL_SOON_MS);
}

// ── A full read (ui.go loadOverview / srvLoadRun) tells what it read ──
// FNV-1a of the answer's text, with its length: "the same answer".
function dlHash(text) {
  if (typeof text !== 'string') return '';
  var h = 0x811c9dc5;
  for (var i = 0; i < text.length; i++) { h ^= text.charCodeAt(i); h = Math.imul(h, 0x01000193) >>> 0; }
  return text.length + ':' + h.toString(16);
}
// An answer's body, its signature and its ETag.
async function dlRead(r) {
  var body = null, text = null;
  if (r && typeof r.text === 'function') {
    try { text = await r.text(); } catch (e) { text = null; }
    if (typeof text === 'string') { try { body = JSON.parse(text); } catch (e) { body = null; } }
  } else if (r && typeof r.json === 'function') {
    try { body = await r.json(); } catch (e) { body = null; }
    try { text = JSON.stringify(body); } catch (e) { text = null; }
  }
  var etag = '';
  try { etag = (r && r.headers && typeof r.headers.get === 'function' && r.headers.get('ETag')) || ''; } catch (e) { etag = ''; }
  return { body: body, sig: dlHash(text), etag: String(etag || '') };
}
// What the list of a server is about: its tenant (single mode: a tenant
// switch is a new list, nothing "new" in it), every tenant (multi).
function dlTenantKey(pid) {
  if (serverMode === 'multi') { var st = srvState(pid); return st.scoped ? 'all' : 'session:' + String(st.me && st.me.tenantId); }
  return 'single:' + String(currentTenantId);
}
function dlNoteLoaded(pid, rd, groups) {
  var s = dlSt(pid);
  s.etag = (rd && rd.etag) || '';
  s.sig = (rd && rd.sig) || '';
  s.at = dlLastAt = Date.now();
  s.fails = 0;
  dlDiff(pid, s, groups);
}
// The devices new since the server's previous list (from its second one).
function dlDiff(pid, s, groups) {
  var ids = new Set(), now = Date.now(), tk = dlTenantKey(pid);
  (Array.isArray(groups) ? groups : []).forEach(function (g) {
    (g && Array.isArray(g.devices) ? g.devices : []).forEach(function (d) { if (d && d.id !== undefined && d.id !== null) ids.add(String(d.id)); });
  });
  if (s.ids && s.tk === tk) {
    ids.forEach(function (id) { if (!s.ids.has(id)) dlNew.set(devKeyOf(pid, id), now); });
    s.ids.forEach(function (id) { if (!ids.has(id)) dlNew.delete(devKeyOf(pid, id)); });
  }
  s.ids = ids; s.tk = tk;
  dlNewArm();
}
function dlIsNew(dev) {
  if (!dev || dev._vm || (typeof devIsLocal === 'function' && devIsLocal(dev))) return false;
  var t = dlNew.get(devKey(dev));
  return !!t && Date.now() - t < DL_NEW_MS;
}
// The chip next to a new device's name (the device rows, ui.go).
function devNewHtml(dev) {
  return dlIsNew(dev) ? '<span class="tag-chip dev-new" title="' + TH('devices.newTip') + '">' + TH('devices.newChip') + '</span>' : '';
}
// The chips go when their time is over (the lists drawn again then).
function dlNewArm() {
  if (dlNewTimer !== null) { clearTimeout(dlNewTimer); dlNewTimer = null; }
  var now = Date.now(), next = 0;
  dlNew.forEach(function (t, k) {
    if (now - t >= DL_NEW_MS) { dlNew.delete(k); return; }
    var at = t + DL_NEW_MS;
    if (!next || at < next) next = at;
  });
  if (next) dlNewTimer = setTimeout(function () { dlNewTimer = null; dlNewArm(); dlRedrawSoon(); }, Math.max(1000, next - now + 50));
}

// ── A light read ──
function dlFetch(url, etag, ms) {
  var ac = null;
  try { if (typeof AbortController === 'function') ac = new AbortController(); } catch (e) { ac = null; }
  var headers = { 'Content-Type': 'application/json' };
  if (etag) headers['If-None-Match'] = etag;
  var opts = { method: 'GET', headers: headers, cache: 'no-store' };
  if (ac) opts.signal = ac.signal;
  var timer = null, timedOut = false;
  var late = new Promise(function (_, reject) {
    timer = setTimeout(function () { timedOut = true; reject(srvTimeoutError(ms)); try { if (ac) ac.abort(); } catch (e) {} }, ms);
  });
  var req = fetch(url, opts).catch(function (e) { throw (timedOut || (e && e.name === 'AbortError')) ? srvTimeoutError(ms) : e; });
  return Promise.race([req, late]).finally(function () { clearTimeout(timer); });
}
async function dlPollOne(pid) {
  pid = pid || '';
  var multi = serverMode === 'multi';
  var st = multi ? srvState(pid) : null;
  if (multi && (st.signedIn !== true || st.loading)) return;
  var s = dlSt(pid);
  if (s.inflight) return;
  s.inflight = true;
  var seq0 = multi ? st.seq : dlOvSeq, patch0 = s.patchSeq;
  // (a newer full read, a mode or server switch meanwhile: this answer is dropped)
  var stale = function () { return multi ? (serverMode !== 'multi' || st.seq !== seq0) : (serverMode === 'multi' || dlOvSeq !== seq0 || homePid() !== pid); };
  var base = multi ? srvBase(pid) : (winPid() ? '/sp/' + winPid() : '/proxy');
  var path = '/api/reach/overview?' + (multi ? 'tenantId=all&' : '') + 'agents=1';
  try {
    var r = await dlFetch(base + path, s.etag, DL_TIMEOUT_MS);
    if (stale()) return;
    s.at = Date.now();
    if (r.status === 304) { dlOk(pid, s, st); return; }
    if (multi && (r.status === 401 || r.status === 403)) {
      // (its session may be over: the full read says so, its section follows)
      srvLoad(pid).then(function () { dlRedrawSoon(); });
      return;
    }
    var rd = await dlRead(r);
    if (stale()) return;
    var body = rd.body;
    if (!r.ok) throw new Error((body && typeof body.error === 'string' && body.error) || ('HTTP ' + r.status));
    var data = body && body.data;
    if (!data || typeof data !== 'object' || !Array.isArray(data.groups)) throw new Error('HTTP ' + r.status);
    if (rd.sig && rd.sig === s.sig) { if (rd.etag) s.etag = rd.etag; dlOk(pid, s, st); return; } // (the same list)
    if (s.patchSeq !== patch0) return; // (a socket event changed the list meanwhile: the next read)
    dlNotePrevReady();
    if (multi) {
      st.scoped = !!data.scope;
      var groups = srvStampTools(srvStamp(pid, data.groups), toolsSupported(data));
      if (typeof explorerCtxPruneGone === 'function') explorerCtxPruneGone(pid, groups);
      st.groups = groups;
      st.error = ''; st.errorTimeout = false;
    } else {
      overview = data;
      srvStamp(pid, overview.groups);
      srvStampTools(overview.groups, toolsSupported(overview));
      if (typeof explorerCtxPruneGone === 'function') explorerCtxPruneGone(pid, overview.groups);
    }
    s.etag = rd.etag || ''; s.sig = rd.sig || ''; s.fails = 0;
    dlDiff(pid, s, multi ? st.groups : overview.groups);
    if (!multi) dlFailClear();
    dlRedrawSoon();
  } catch (e) {
    if (!stale()) dlFail(pid, s, st, e);
  } finally {
    s.inflight = false;
  }
}
// The read answered (nothing changed): a failure said before goes.
function dlOk(pid, s, st) {
  s.fails = 0;
  if (st && st.error) { st.error = ''; st.errorTimeout = false; dlRedrawSoon(); }
  if (!st) dlFailClear();
}
// A read failed: said once DL_FAILS_SHOWN failed in a row (the server's
// section in multi-server mode, its "Try again"; the status bar in single
// mode), what the lists hold left as it is meanwhile.
function dlFail(pid, s, st, e) {
  s.fails++;
  if (s.fails < DL_FAILS_SHOWN) return;
  if (st) {
    var was = st.error;
    st.errorTimeout = !!(e && (e.timeout || e.name === 'AbortError'));
    st.error = (e && e.timeout) ? e.message : st.errorTimeout ? srvTimeoutError(DL_TIMEOUT_MS).message : ((e && e.message) || String(e));
    if (was !== st.error) dlRedrawSoon();
    return;
  }
  var text = T('status.loadFailed', { error: (e && e.message) || String(e) });
  var bar = document.getElementById('status-bar');
  if (bar && bar.textContent !== text) { setStatus(text); dlFailText = text; }
}
function dlFailClear() {
  if (!dlFailText) return;
  var bar = document.getElementById('status-bar');
  if (bar && bar.textContent === dlFailText) setStatus(T('status.readyDevices', { count: countDevices() }));
  dlFailText = '';
}
function dlNotePrevReady() {
  if (dlPrevReady === null) { try { dlPrevReady = T('status.readyDevices', { count: countDevices() }); } catch (e) { dlPrevReady = ''; } }
}

// ── Drawing again ──
function dlRedrawSoon(ms) {
  if (dlRedrawTimer !== null) return;
  dlRedrawTimer = setTimeout(function () { dlRedrawTimer = null; dlRedraw(); }, ms || DL_REDRAW_MS);
}
// The lists drawn again once (overviewRedraw: Devices, Dashboard, Files,
// VMs, the Chat page's and the "+" list), what the user was doing kept: the
// search, filters and folding are the views' own; the scroll of the page
// and the focused row are put back; the ticked devices keep what the data
// still hold (dsPrune: a device gone or no longer visible leaves the
// selection, on any page). Typing in a list's own field: a bit later.
var dlRedrawHidden = false;   // a redraw put off while the window is hidden (drawn when it shows)
function dlRedraw() {
  if (typeof appReady === 'undefined' || !appReady) { dlPrevReady = null; return; }
  // Hidden (minimized, another virtual desktop): nothing is drawn; the data
  // are kept up to date and the lists drawn once when the window shows.
  if (!dlVisible()) { dlRedrawHidden = true; return; }
  dlRedrawHidden = false;
  var ae = document.activeElement;
  if (ae && ae.tagName === 'INPUT' && typeof ae.closest === 'function' && ae.closest('tbody')) { dlRedrawSoon(2000); return; }
  if (serverMode === 'multi') overviewRebuild();
  dlKeepView(function () { overviewRedraw(); });
  if (serverMode === 'multi' && !(typeof reachDetachedMode !== 'undefined' && reachDetachedMode)) { try { renderTenantSwitcher(); } catch (e) {} }
  if (typeof dsOn === 'function' && dsOn() && typeof dsPrune === 'function') { dsPrune(); if (typeof dsBar === 'function') dsBar(); }
  var bar = document.getElementById('status-bar');
  if (bar && dlPrevReady !== null && bar.textContent === dlPrevReady) setStatus(T('status.readyDevices', { count: countDevices() }));
  dlPrevReady = null;
}
function dlKeepView(fn) {
  var pageBody = function () {
    var pc = document.getElementById('page-content');
    return pc && typeof pc.querySelector === 'function' ? pc.querySelector('.page-body') : null;
  };
  var pb = pageBody(), top = pb ? pb.scrollTop : 0, left = pb ? pb.scrollLeft : 0;
  // The focused row (a device's or a header's), or its box.
  var ae = document.activeElement, tr = ae && typeof ae.closest === 'function' ? ae.closest('tr') : null;
  var tb = tr && tr.parentElement, tbId = tb && tb.id;
  var attr = tr && tr.dataset ? (tr.dataset.devKey ? 'devKey' : tr.dataset.treeKey ? 'treeKey' : '') : '';
  var key = attr ? tr.dataset[attr] : '', onBox = !!(ae && ae.classList && ae.classList.contains('dp-check'));
  fn();
  var pb2 = pageBody();
  if (pb2 && (pb2.scrollTop !== top || pb2.scrollLeft !== left)) { pb2.scrollTop = top; pb2.scrollLeft = left; }
  if (!attr || !tbId || document.activeElement === ae) return;
  var t2 = document.getElementById(tbId);
  var rows = (t2 && t2.children) || [];
  for (var i = 0; i < rows.length; i++) {
    var r = rows[i];
    if (!r || !r.dataset || r.dataset[attr] !== key) continue;
    var el = onBox && typeof r.querySelector === 'function' ? (r.querySelector('.dp-check') || r) : r;
    try { el.focus(); } catch (e) {}
    return;
  }
}

// ── The servers' socket events ──
// The records of a device the lists hold (pid, id): [{ g, i }].
function dlGroupsOf(pid) {
  if (serverMode === 'multi') { var st = srv.get(pid || ''); return (st && st.signedIn === true && Array.isArray(st.groups)) ? st.groups : []; }
  return (pid || '') === homePid() && overview && Array.isArray(overview.groups) ? overview.groups : [];
}
function dlFind(pid, test) {
  var out = [];
  dlGroupsOf(pid).forEach(function (g) {
    (g && Array.isArray(g.devices) ? g.devices : []).forEach(function (d, i) { if (d && test(d)) out.push({ g: g, i: i }); });
  });
  return out;
}
// A device's new record: the old one copied, its parts that change too.
function dlCopy(d) {
  var n = Object.assign({}, d);
  ['oblireach', 'agent', 'privacy'].forEach(function (k) { if (d[k] && typeof d[k] === 'object') n[k] = Object.assign({}, d[k]); });
  return n;
}
var DL_STATUS_RE = /^[a-z_]{1,32}$/;
function dlText(v) { return typeof v === 'string' ? v.replace(/[\u0000-\u001f\u007f]/g, '').trim().slice(0, 255) : ''; }
// What an event says of a device, on its record (null: nothing changes).
function dlPatched(d, p) {
  var n = null;
  var put = function () { if (!n) n = dlCopy(d); return n; };
  if (typeof p.status === 'string' && DL_STATUS_RE.test(p.status) && p.status !== d.status) put().status = p.status;
  // (a whole device: its name as the overview gives it, display name first)
  if (typeof p.hostname === 'string') {
    var name = dlText(p.displayName) || dlText(p.hostname);
    if (name && name !== d.hostname) put().hostname = name;
  }
  if (typeof p.agentVersion === 'string' && p.agentVersion && d.agent && typeof d.agent === 'object' && d.agent.version !== dlText(p.agentVersion)) put().agent.version = dlText(p.agentVersion);
  if (typeof p.privacyModeEnabled === 'boolean' && d.privacy && typeof d.privacy === 'object' && d.privacy.enabled !== p.privacyModeEnabled) put().privacy.enabled = p.privacyModeEnabled;
  return n;
}
function dlApply(pid, hits, fn) {
  var changed = false;
  hits.forEach(function (h) {
    var n = fn(h.g.devices[h.i]);
    if (n) { h.g.devices[h.i] = n; changed = true; }
  });
  if (changed) { dlSt(pid).patchSeq++; dlNotePrevReady(); dlRedrawSoon(); }
  return changed;
}
function dlEventId(p) {
  var id = p && (p.deviceId !== undefined && p.deviceId !== null ? p.deviceId : p.id);
  var n = Number(id);
  return Number.isInteger(n) && n > 0 ? n : null;
}
// DEVICE_UPDATED / DEVICE_OFFLINE / DEVICE_DELETED / DEVICE_APPROVED.
function dlOnDeviceEvent(pid, p, kind) {
  pid = pid || '';
  if (!p || typeof p !== 'object') return;
  if (kind === 'approved') { dlPollSoon(pid); return; } // (a new device: only from the overview)
  var id = dlEventId(p);
  if (id === null) return;
  var hits = dlFind(pid, function (d) { return Number(d.id) === id && !d._vm; });
  if (!hits.length) return; // (not a device this user's lists hold: ignored)
  if (kind === 'deleted') {
    // (from the last one, so the indexes stay right)
    hits.reverse().forEach(function (h) { var a = h.g.devices.slice(); a.splice(h.i, 1); h.g.devices = a; });
    var s = dlSt(pid);
    s.patchSeq++;
    if (s.ids) s.ids.delete(String(id));
    dlNew.delete(devKeyOf(pid, id));
    dlNotePrevReady();
    dlRedrawSoon();
    dlPollSoon(pid);
    return;
  }
  var patch = kind === 'offline' ? { status: 'offline' } : p;
  dlApply(pid, hits, function (d) { return dlPatched(d, patch); });
}
// OBLIREACH_AGENT_UPDATED { deviceUuid, version, online }: the machine's
// ObliReach agent connected (its online state and version). Its going away
// is left to the overview (online stays true there for a short while after
// a disconnect: the server's rule), never said here first.
function dlOnReachEvent(pid, p) {
  pid = pid || '';
  if (!p || typeof p !== 'object' || typeof p.deviceUuid !== 'string' || !p.deviceUuid) return;
  var uuid = p.deviceUuid;
  var hits = dlFind(pid, function (d) { return d.uuid === uuid && !d._vm; });
  if (!hits.length) return;
  var notInstalled = false;
  dlApply(pid, hits, function (d) {
    if (!d.oblireach || typeof d.oblireach !== 'object' || !d.oblireach.installed) { notInstalled = true; return null; }
    var n = null;
    if (p.online === true && d.oblireach.online !== true) { n = n || dlCopy(d); n.oblireach.online = true; }
    var v = dlText(p.version);
    if (v && v !== d.oblireach.version) { n = n || dlCopy(d); n.oblireach.version = v; }
    return n;
  });
  if (notInstalled && p.online === true) dlPollSoon(pid); // (ObliReach just installed there: the overview says it all)
}
if (typeof toolSocketHooks !== 'undefined' && Array.isArray(toolSocketHooks)) {
  toolSocketHooks.push(function (sock, pid) {
    if (!sock || typeof sock.on !== 'function') return;
    sock.on('DEVICE_UPDATED', function (p) { dlOnDeviceEvent(pid || '', p, 'updated'); });
    sock.on('DEVICE_OFFLINE', function (p) { dlOnDeviceEvent(pid || '', p, 'offline'); });
    sock.on('DEVICE_DELETED', function (p) { dlOnDeviceEvent(pid || '', p, 'deleted'); });
    sock.on('DEVICE_APPROVED', function (p) { dlOnDeviceEvent(pid || '', p, 'approved'); });
    sock.on('OBLIREACH_AGENT_UPDATED', function (p) { dlOnReachEvent(pid || '', p); });
  });
}

// ── Settings > Application: "Device list refresh" ──
function dlRenderSetting() {
  var row = document.createElement('div'); row.className = 'setting-row';
  var lbl = document.createElement('div'); lbl.className = 'setting-label';
  var title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.app.devRefresh');
  var sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('settings.app.devRefreshSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  var ctl = document.createElement('div'); ctl.className = 'setting-control';
  var sel = document.createElement('select');
  sel.id = 'set-dev-refresh';
  sel.title = T('settings.app.devRefresh');
  sel.style.cssText = 'background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer';
  [['auto', T('settings.app.devRefreshAuto')], ['15', T('settings.app.devRefreshEvery', { seconds: 15 })], ['30', T('settings.app.devRefreshEvery', { seconds: 30 })],
    ['60', T('settings.app.devRefreshEvery', { seconds: 60 })], ['manual', T('settings.app.devRefreshManual')]].forEach(function (o) {
    var opt = document.createElement('option'); opt.value = o[0]; opt.textContent = o[1]; sel.appendChild(opt);
  });
  sel.value = devRefreshMode();
  sel.addEventListener('change', function () {
    devRefreshSetMode(sel.value);
    var m = devRefreshMode();
    setStatus(m === 'auto' ? T('settings.app.devRefreshStatusAuto') : m === 'manual' ? T('settings.app.devRefreshStatusManual')
      : T('settings.app.devRefreshStatusEvery', { seconds: Number(m) }));
  });
  ctl.appendChild(sel);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}
`
