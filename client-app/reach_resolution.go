package main

// reachResolutionCSS / reachResolutionJS: the session toolbar's screen
// resolution selector (raise the resolution of a machine quickly, among
// those Windows offers) and Settings > Remote sessions' "Keep
// the chosen resolution at the end of the session" (off by default: the
// operator knows when to keep it, typically on a server).
//
// Injected into the page like reachViewerJS (strings.Replace, not Sprintf:
// '%' needs no escaping; no backticks, no closing script tag).
//
// Protocol (agent: agent/displaymode.go), gated on init.features
// "display_modes" (REACH.FEATURE.DISPLAY_MODES):
//   viewer -> agent {type:'display_modes_get', id}
//   agent -> viewer {type:'display_modes', id, monitor, changeable, reason?,
//                    current:{w,h,hz}, modes:[{w,h,hz,current?}]}
//   viewer -> agent {type:'display_mode_set', id, w, h, hz, keep}
//   agent -> viewer {type:'display_mode_result', id, ok, code?}
// The list is asked after every init (a resolution change re-inits the
// stream, so the current mode is marked again) and when the menu opens.
// An RDP session's resolution is its client window's: changeable:false,
// reason 'rdp' -> the button is disabled and its tooltip says why; so is it
// for an agent without the capability (older, or not Windows).

const reachResolutionCSS = `
.toolbar-dropdown-menu.reach-res-menu{min-width:190px;max-height:62vh;overflow-y:auto}
.toolbar-dropdown-menu .reach-res-keep{display:flex;align-items:center;gap:8px;padding:6px 10px;font-size:11px;color:var(--text);cursor:pointer;white-space:nowrap;border-radius:6px}
.toolbar-dropdown-menu .reach-res-keep:hover{background:rgb(var(--ink) / .06)}
.toolbar-dropdown-menu .reach-res-keep input{accent-color:var(--accent);width:14px;height:14px;margin:0;cursor:pointer}
.toolbar-dropdown-menu button.reach-res-cur{color:var(--accent-l);font-weight:600}
`

const reachResolutionJS = `
// ── Screen resolution (reach_resolution.go) ────────────────────────────────
var REACH_RES_PREF = 'oblireach_keep_resolution';
var REACH_RES_TIMEOUT_MS = 20000;
var reachResSeq = 0;
var REACH_RES_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>';
// Agent codes (display_mode_result / display_modes reason) -> catalog keys.
var REACH_RES_ERROR_TEXT = {
  rdp: 'viewer.res.rdp',
  badmode: 'viewer.res.err.badmode',
  restart: 'viewer.res.err.restart',
  not_listed: 'viewer.res.err.notListed',
  session_changed: 'viewer.res.err.sessionChanged',
  unavailable: 'viewer.res.unavailable',
  timeout: 'viewer.res.err.timeout',
};
// "Keep the chosen resolution at the end of the session": OFF unless the
// operator turned it on in the Settings (this computer).
function reachKeepResDefault() { return reachPref(REACH_RES_PREF, '0') === '1'; }
function reachSetKeepResDefault(on) { reachSetPref(REACH_RES_PREF, on ? '1' : '0'); }
// The menu's checkbox: the tab's own choice once ticked / unticked there,
// else the Settings default.
function reachResKeep(tab) { return (tab && typeof tab.resKeep === 'boolean') ? tab.resKeep : reachKeepResDefault(); }
function reachCanResolution(tab) { return reachHas(tab, REACH.FEATURE.DISPLAY_MODES); }
function reachResLive(tab) { return !!(tab && tab.ws && tab.gotInit); }
// '@ 144 Hz' only when the offered modes do not all share one refresh rate.
function reachResShowHz(modes) {
  var seen = {};
  var n = 0;
  (modes || []).forEach(function (m) { if (m.hz > 1 && !seen[m.hz]) { seen[m.hz] = true; n++; } });
  return n > 1;
}
function reachResModeText(m, showHz) {
  return (showHz && m.hz > 1) ? T('viewer.res.modeHz', { w: m.w, h: m.h, hz: m.hz }) : T('viewer.res.mode', { w: m.w, h: m.h });
}
function reachResCodeText(code) { return reachCodeText(REACH_RES_ERROR_TEXT, code) || T('viewer.res.err.failed'); }
// Normalised display_modes answer (agent input: only well-formed modes kept).
function reachResNormalize(msg) {
  var mode = function (m) { return (m && m.w > 0 && m.h > 0) ? { w: m.w | 0, h: m.h | 0, hz: (m.hz | 0) > 0 ? (m.hz | 0) : 0, current: !!m.current } : null; };
  var modes = Array.isArray(msg.modes) ? msg.modes.map(mode).filter(function (m) { return !!m; }).slice(0, 200) : [];
  var cur = mode(msg.current);
  if (!cur) { var c = modes.filter(function (m) { return m.current; })[0]; if (c) cur = c; }
  return {
    changeable: msg.changeable === true,
    reason: typeof msg.reason === 'string' ? msg.reason : '',
    monitor: (typeof msg.monitor === 'number') ? msg.monitor : -1,
    current: cur, modes: modes,
  };
}
// What the button shows: {enabled, title, label}.
function reachResState(tab) {
  if (!reachResLive(tab)) return { enabled: false, title: T('viewer.res.title'), label: '' };
  if (!reachCanResolution(tab)) return { enabled: false, title: T('viewer.res.unsupported'), label: '' };
  var r = tab.res;
  var label = (r && r.current) ? T('viewer.res.mode', { w: r.current.w, h: r.current.h }) : '';
  if (r && !r.changeable) return { enabled: false, title: reachResCodeText(r.reason || 'unavailable'), label: label };
  return { enabled: true, title: label ? T('viewer.res.titleCurrent', { w: r.current.w, h: r.current.h }) : T('viewer.res.title'), label: label };
}
function reachResRequest(tab) {
  if (!reachResLive(tab) || !reachCanResolution(tab)) return false;
  var id = 'res' + (++reachResSeq);
  tab.resListId = id;
  return reachSend(tab, { type: 'display_modes_get', id: id });
}
// After every init: the capability may have changed (another session, an
// updated agent) and a resolution change re-inits the stream.
function reachResOnInit(tab) {
  if (!reachCanResolution(tab)) { tab.res = null; return; }
  reachResRequest(tab);
}
function reachResOnModes(tab, msg) {
  if (!reachCanResolution(tab)) return;
  tab.res = reachResNormalize(msg);
  if (reachUI(tab)) {
    reachRefreshResolution(tab);
    var menu = document.getElementById('reach-res-menu');
    if (menu && menu.classList.contains('open')) reachRenderResMenu(tab);
  }
}
function reachResClearPending(tab) {
  if (tab && tab.resPending) { if (tab.resPending.timer) clearTimeout(tab.resPending.timer); tab.resPending = null; }
}
// Apply a mode of the list. keep: the menu's checkbox.
function reachResSet(tab, m) {
  if (!reachResLive(tab) || !reachCanResolution(tab) || !tab.res || !tab.res.changeable || tab.resPending) return false;
  if (m.current) return false;
  var id = 'res' + (++reachResSeq);
  var keep = reachResKeep(tab);
  if (!reachSend(tab, { type: 'display_mode_set', id: id, w: m.w, h: m.h, hz: m.hz || 0, keep: keep })) return false;
  tab.resPending = { id: id, w: m.w, h: m.h, keep: keep, timer: null };
  tab.resPending.timer = setTimeout(function () {
    if (!tab.resPending || tab.resPending.id !== id) return;
    tab.resPending = null;
    if (reachUI(tab)) { reachToast(reachResCodeText('timeout'), 'warn'); reachRefreshResolution(tab); }
  }, REACH_RES_TIMEOUT_MS);
  if (reachUI(tab)) { reachToast(T('viewer.res.applying', { w: m.w, h: m.h }), ''); reachRefreshResolution(tab); }
  return true;
}
function reachResOnResult(tab, msg) {
  var p = tab.resPending;
  if (!p || (msg.id && msg.id !== p.id)) return;
  reachResClearPending(tab);
  if (msg.ok === true) {
    if (reachUI(tab)) reachToast(T(p.keep ? 'viewer.res.appliedKeep' : 'viewer.res.appliedRestore', { w: p.w, h: p.h }), 'ok');
    reachResRequest(tab); // (a refresh-rate-only change brings no new init)
  } else {
    if (reachUI(tab)) reachToast(reachResCodeText(msg.code), 'warn');
    if (msg.code === 'rdp' && tab.res) { tab.res.changeable = false; tab.res.reason = 'rdp'; }
  }
  if (reachUI(tab)) reachRefreshResolution(tab);
}
// Button state of the session ON SCREEN (called by reachRefreshToolbar).
function reachRefreshResolution(tab) {
  if (!reachUI(tab)) return;
  var b = document.getElementById('reach-res-btn');
  if (!b) return;
  var s = reachResState(tab);
  b.disabled = !s.enabled || !!tab.resPending;
  b.title = s.title;
  b.setAttribute('aria-label', s.title);
  b.innerHTML = REACH_RES_ICON + (s.label ? '<span>' + esc(s.label) + '</span>' : '');
  if (!s.enabled) { var menu = document.getElementById('reach-res-menu'); if (menu) menu.classList.remove('open'); }
}
function reachRenderResMenu(tab) {
  var menu = document.getElementById('reach-res-menu');
  if (!menu) return;
  menu.innerHTML = '';
  if (!tab) return;
  var r = tab.res;
  if (!r) {
    var ld = document.createElement('div'); ld.className = 'reach-menu-note'; ld.textContent = T('viewer.res.loading');
    menu.appendChild(ld);
    return;
  }
  if (tab.monitors && tab.monitors.length > 1 && r.monitor >= 0) {
    var mn = document.createElement('div'); mn.className = 'reach-menu-note'; mn.textContent = T('viewer.res.forScreen', { n: r.monitor + 1 });
    menu.appendChild(mn);
  }
  var showHz = reachResShowHz(r.modes);
  r.modes.forEach(function (m) {
    var b = document.createElement('button');
    b.textContent = (m.current ? '\u2713 ' : '') + reachResModeText(m, showHz);
    if (m.current) b.className = 'reach-res-cur';
    b.disabled = !r.changeable || !!tab.resPending;
    b.addEventListener('click', function (ev) {
      if (ev && ev.stopPropagation) ev.stopPropagation();
      menu.classList.remove('open');
      reachResSet(tab, m);
    });
    menu.appendChild(b);
  });
  var sep = document.createElement('div'); sep.className = 'reach-menu-sep'; menu.appendChild(sep);
  var lbl = document.createElement('label'); lbl.className = 'reach-res-keep';
  var cb = document.createElement('input'); cb.type = 'checkbox'; cb.id = 'reach-res-keep';
  cb.checked = reachResKeep(tab);
  cb.disabled = !r.changeable;
  cb.addEventListener('change', function () { tab.resKeep = !!cb.checked; });
  var tx = document.createElement('span'); tx.textContent = T('viewer.res.keep');
  lbl.appendChild(cb); lbl.appendChild(tx);
  lbl.title = T('viewer.res.keepTitle');
  menu.appendChild(lbl);
}
function reachBuildResolutionTool(toolbar) {
  // Opening asks for a fresh list (the local user may have changed it); the
  // answer re-renders the open menu (reachResOnModes), which asks nothing.
  var onOpen = function (tab) { if (tab && !tab.resPending) reachResRequest(tab); reachRenderResMenu(tab); };
  var wrap = reachDropdown(toolbar, 'reach-res', REACH_RES_ICON, T('viewer.res.title'), onOpen, 'reach-res-menu');
  var b = document.getElementById('reach-res-btn');
  if (b) b.disabled = true;
  return wrap;
}
// Settings > Remote sessions: the default of the menu's checkbox.
function reachRenderKeepResSetting() {
  var row = document.createElement('div'); row.className = 'setting-row';
  var lbl = document.createElement('div'); lbl.className = 'setting-label';
  var title = document.createElement('div'); title.className = 'title'; title.textContent = T('settings.sessions.keepRes');
  var sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('settings.sessions.keepResSub');
  lbl.appendChild(title); lbl.appendChild(sub);
  var ctl = document.createElement('div'); ctl.className = 'setting-control';
  var cb = document.createElement('input');
  cb.type = 'checkbox'; cb.id = 'set-keep-resolution';
  cb.style.cssText = 'accent-color:var(--accent);width:16px;height:16px;cursor:pointer';
  cb.checked = reachKeepResDefault();
  cb.title = T('settings.sessions.keepRes');
  cb.addEventListener('change', function () {
    reachSetKeepResDefault(cb.checked);
    if (typeof setStatus === 'function') setStatus(cb.checked ? T('settings.sessions.keepResOn') : T('settings.sessions.keepResOff'));
  });
  ctl.appendChild(cb);
  row.appendChild(lbl); row.appendChild(ctl);
  return row;
}
`
