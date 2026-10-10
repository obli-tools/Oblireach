package main

// local_pane.go — a Files tab's split view (the main window's Files page, a
// session window's Files tabs): the operator's own PC on the left, the
// device on the right (explorer_viewer.go), as WinSCP shows them, in the
// Desktop's look (no borders: two surfaces, the theme's tokens only).
//
// The local disk is Go's (localfs*.go): the page only calls the narrow
// bindings reachFs* with the per-run token (every window: a session window
// shares the main window's file bindings, localfs_windows.go
// attachSharedLocalFS; localFsAvailable; the editing round trip stays the
// main window's: localEditAvailable), and gets their answers through
// window.__reachLocalFs(reqId, ok, json). A refusal is a code, said here in
// the operator's language (localFsErrText). Everything the disk names (a
// file of this PC, a drive label) is text on screen, never markup.
//
// The pane: "This PC" (the known folders, then the drives), a folder's
// listing (breadcrumb, a typed path with Ctrl+L, a filter, sorting, hidden
// files when Settings > Files says so), a selection (click, Ctrl, Shift,
// Ctrl+A, the arrows), new folder, rename (F2), the Recycle Bin (Del, after
// a confirmation; fixed drives only), Show in Explorer (the element selected,
// never opened), copy the path, and sending the selection to the device
// (Ctrl+U, the rail's arrow, a drag to the device's pane). A double click on
// a file starts nothing: no binding can start a local file.
//
// Both consts are injected verbatim after the Files page (buildUI, not
// through Sprintf: '%' is safe): no backtick here.

const localPaneCSS = `
/* ── Split view (local_pane.go): this PC | the device ── */
.tool-host.exp-host.xp-host{padding:10px;gap:10px;background:var(--bg)}
.xp-split{flex:1;min-height:0;display:flex;align-items:stretch}
.xp-pane{flex:1 1 0;min-width:0;min-height:0;display:flex;flex-direction:column;overflow:hidden;border-radius:14px;background:var(--bg2);box-shadow:var(--surface-shadow);transition:box-shadow .15s}
.xp-pane.focus{box-shadow:var(--surface-shadow),inset 0 2px 0 rgb(var(--accent-rgb) / .6)}
.xp-remote .exp-bar .toolbar-sep{flex-basis:100%;width:auto;height:0}
.xp-pane .exp-bar{background:transparent;padding:6px 10px}
.xp-pane .exp-bar.xp-acts{padding-top:0}
.xp-pane .exp-list{border-radius:0 0 14px 14px}
.xp-pane .exp-table th{background:var(--bg2)}
.xp-pane{container-type:inline-size}
.xp-pane .exp-table th.c-size{width:88px}
.xp-pane .exp-table th.c-mod{width:158px}
@container (max-width: 560px){.exp-table th.c-mod,.exp-table td.c-mod{display:none}}
@container (max-width: 330px){.exp-table th.c-size,.exp-table td.c-size{display:none}}
.xp-head{display:flex;align-items:center;gap:9px;padding:10px 12px 2px;min-width:0}
.xp-head-ico{width:28px;height:28px;flex-shrink:0;border-radius:9px;display:inline-flex;align-items:center;justify-content:center;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.xp-head-txt{min-width:0;display:flex;flex-direction:column}
.xp-head-t{font-size:13px;font-weight:650;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.xp-head-s{font-size:11px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.xp-pane:not(.focus) .xp-head-t{color:var(--muted)}
.xp-head .sp{flex:1}
.xp-head .dot{flex-shrink:0}
.xp-srv-dot{width:8px;height:8px;flex-shrink:0;border-radius:50%}
.xp-places{display:flex;flex-wrap:wrap;gap:5px;padding:7px 12px 1px}
.xp-chip{display:inline-flex;align-items:center;gap:5px;padding:4px 9px;border:none;border-radius:8px;background:rgb(var(--ink) / .06);color:var(--muted);font-size:11px;cursor:pointer;max-width:180px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.xp-chip:hover{background:rgb(var(--ink) / .11);color:var(--text)}
.xp-chip:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.xp-chip.cur{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.xp-chip svg{flex-shrink:0}
.xp-rail{flex:0 0 40px;position:relative;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:10px;cursor:col-resize;touch-action:none;user-select:none}
.xp-rail::before{content:'';position:absolute;top:16px;bottom:16px;left:19px;width:2px;border-radius:1px;background:rgb(var(--ink) / .12);transition:background .15s}
.xp-rail:hover::before,.xp-rail.drag::before{background:var(--accent)}
.xp-rail-btn{position:relative;z-index:1;width:30px;height:30px;border:none;border-radius:50%;display:inline-flex;align-items:center;justify-content:center;background:var(--bg2);color:var(--muted);box-shadow:var(--surface-shadow);cursor:pointer;transition:background .15s,color .15s}
.xp-rail-btn:hover:not(:disabled){background:var(--accent-fill);color:var(--on-accent)}
.xp-rail-btn:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.xp-rail-btn:disabled{opacity:.35;cursor:default}
.xp-host.no-local .xp-local,.xp-host.no-local .xp-rail{display:none}
/* Alone, the device pane takes the whole width (its inline flex is the split ratio, 0.5 by default: a single item with flex-grow < 1 only fills that share). */
.xp-host.no-local .xp-remote{flex:1 1 0 !important}
.xp-host.stacked .xp-split{flex-direction:column}
.xp-host.stacked .xp-rail{flex:0 0 36px;flex-direction:row;cursor:row-resize}
.xp-host.stacked .xp-rail::before{top:17px;bottom:auto;left:16px;right:16px;width:auto;height:2px}
.xp-host.stacked .xp-rail-btn svg{transform:rotate(90deg)}
.exp-table tbody tr.drop-row td{background:rgb(var(--accent-rgb) / .18)}
.exp-table tbody tr.hid td{opacity:.62}
.exp-name .lnk{flex-shrink:0;color:var(--muted2);font-size:11px}
.xp-note{flex-shrink:0;padding:5px 14px 8px;font-size:11px;color:var(--muted2);min-height:0}
.xp-note:empty{display:none}
.xp-toggle{display:inline-flex;align-items:center;gap:6px}
.xp-toggle.on{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
`

const localPaneJS = `
// ═════════════════════════════════════════════════════════════════════════
// The operator's own files (local_pane.go; Go: localfs*.go)
// ═════════════════════════════════════════════════════════════════════════
var LOCALFS_TIMEOUT_MS = 600000; // (a listing of a slow network drive; a walk)
var LOCALFS_CODES = ['invalid_path', 'invalid_name', 'invalid_request', 'not_found', 'not_dir', 'is_dir', 'is_link', 'denied', 'protected', 'exists',
  'too_large', 'limit', 'changed', 'in_use', 'io', 'stale_handle', 'stale', 'no_recycle', 'dangerous_type', 'dangerous_editor', 'no_editor', 'unsynced',
  'busy', 'cancelled', 'timeout', 'unavailable', 'server_changed'];
var localFsWaiting = new Map(); // reqId -> { resolve, reject, timer }

// The split view exists with the file bindings (every window of the app).
function localFsAvailable() {
  return typeof window.reachFsList === 'function' && !!reachNativeToken();
}
// The editing round trip (edit_page.go): the main window's only (its working
// copies are watched there); a session window downloads instead.
function localEditAvailable() {
  return localFsAvailable() && !reachDetachedMode;
}
function localFsErr(r) {
  var code = (r && typeof r.code === 'string' && LOCALFS_CODES.indexOf(r.code) >= 0) ? r.code : 'io';
  var e = new Error(code);
  e.code = code; e.info = (r && typeof r === 'object') ? r : {};
  e.local = true;
  return e;
}
// Go's answer to an asynchronous call (native_windows.go evalLocalFs).
window.__reachLocalFs = function (reqId, ok, res) {
  var id = String(reqId), w = localFsWaiting.get(id);
  if (!w) return;
  localFsWaiting.delete(id);
  clearTimeout(w.timer);
  if (ok === true) w.resolve(res); else w.reject(localFsErr(res));
};
// An asynchronous binding: its request id, then its answer (a promise).
function localFs(name) {
  var args = Array.prototype.slice.call(arguments, 1);
  var id = explorerReqId();
  return new Promise(function (resolve, reject) {
    var w = { resolve: resolve, reject: reject, timer: null };
    w.timer = setTimeout(function () { if (localFsWaiting.get(id) === w) { localFsWaiting.delete(id); reject(localFsErr({ code: 'timeout' })); } }, LOCALFS_TIMEOUT_MS);
    localFsWaiting.set(id, w);
    reachNative.apply(null, [name, id].concat(args)).then(function (r) {
      if (r === true || localFsWaiting.get(id) !== w) return;
      localFsWaiting.delete(id); clearTimeout(w.timer); reject(localFsErr({ code: 'io' }));
    }, function (e) {
      if (localFsWaiting.get(id) !== w) return;
      localFsWaiting.delete(id); clearTimeout(w.timer);
      var s = String((e && e.message) || e || '');
      reject(localFsErr({ code: /busy/.test(s) ? 'busy' : s === 'unavailable' ? 'unavailable' : 'io' }));
    });
  });
}
// A refusal of the local disk, in words.
function localFsErrText(e, name) {
  var c = (e && e.code) || 'io';
  switch (c) {
    case 'invalid_path': case 'invalid_request': return T('local.err.invalidPath');
    case 'invalid_name': return T('explorer.invalidName');
    case 'not_found': case 'stale_handle': case 'stale': return T('local.err.notFound');
    case 'not_dir': return T('local.err.notDir');
    case 'is_dir': return T('local.err.isDir');
    case 'is_link': return T('local.err.isLink');
    case 'denied': return T('local.err.denied');
    case 'protected': return T('local.err.protected');
    case 'exists': return T('local.err.exists', { name: name || '' });
    case 'too_large': return T('local.err.tooLarge', { max: reachFmtBytes(EXPLORER_MAX_BYTES) });
    case 'limit': return T('xfer.limit');
    case 'changed': return T('local.err.changed');
    case 'in_use': return T('local.err.inUse');
    case 'no_recycle': return T('local.err.noRecycle');
    case 'dangerous_type': case 'dangerous_editor': return T('local.err.dangerousEditor');
    case 'no_editor': return T('local.err.noEditor');
    case 'unsynced': return T('edit.unsynced', { name: name || '' });
    case 'busy': return T('local.err.busy');
    case 'cancelled': return T('viewer.tr.cancelled');
    case 'timeout': return T('local.err.timeout');
    case 'unavailable': return T('local.err.unavailable');
    case 'server_changed': return T('local.err.serverChanged');
  }
  return T('local.err.io');
}
function localFsSay(e, name) { reachNotify(localFsErrText(e, name), 'warn'); }

// ── Local paths ──
// The operator's own OS: 'windows', or 'unix' (Linux: set by the platform's
// block, ui_platform.go). Windows: drive paths and backslashes; unix:
// absolute POSIX paths.
var LOCAL_OS = 'windows';
function localIsWin() { return LOCAL_OS !== 'unix'; }
// A typed local path: a drive path only on Windows (never \\server: the Go
// side refuses it too), an absolute path elsewhere; no "..". null: refused.
function localNormPath(v) {
  if (!localIsWin()) { var u = String(v === undefined || v === null ? '' : v).trim(); return u ? explorerNormPath(u, 'unix') : ''; }
  var p = explorerNormPath(v, 'windows');
  if (p === null || /^\\\\/.test(String(p))) return null;
  return p;
}
// A local path's separator.
function localSep() { return localIsWin() ? '\\' : '/'; }
// A name for this PC (a new folder, a rename): Windows' rules, or POSIX's.
function localValidName(n) {
  n = String(n === undefined || n === null ? '' : n);
  if (!localIsWin()) return explorerValidName(n, 'unix') && n === n.trim();
  if (!explorerValidName(n, 'windows') || n !== n.trim() || /[. ]$/.test(n)) return false;
  var base = n.split('.')[0].replace(/ +$/, '').toUpperCase();
  return !/^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|COM[0-9¹²³]|LPT[0-9¹²³])$/.test(base);
}
function localNameCheck(v) { return localValidName(v) ? '' : T('explorer.invalidName'); }

var LP_ICON = {
  pc: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>',
  home: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 9l9-7 9 7v11a2 2 0 01-2 2H5a2 2 0 01-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/></svg>',
  desktop: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/></svg>',
  documents: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="8" y1="13" x2="16" y2="13"/><line x1="8" y1="17" x2="13" y2="17"/></svg>',
  downloads: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/></svg>',
  drive: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="13" width="20" height="8" rx="2"/><path d="M5.5 13L8 4h8l2.5 9"/></svg>',
  network: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="7" rx="2"/><line x1="12" y1="10" x2="12" y2="16"/><line x1="4" y1="20" x2="20" y2="20"/><circle cx="12" cy="18" r="2"/></svg>',
  usb: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="7" y="9" width="10" height="13" rx="2"/><path d="M9 9V3h6v6"/></svg>',
  disc: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="2"/></svg>',
  toRemote: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><line x1="5" y1="12" x2="19" y2="12"/><polyline points="12 5 19 12 12 19"/></svg>',
  toLocal: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/></svg>',
  reveal: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>',
  split: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><line x1="12" y1="4" x2="12" y2="20"/></svg>',
};
var LP_PLACE_KEY = { home: 'local.place.home', desktop: 'local.place.desktop', documents: 'local.place.documents', downloads: 'local.place.downloads' };
function lpPlaceName(id) { return T(LP_PLACE_KEY[id] || 'local.place.home'); }
function lpPlaceIcon(id) { return LP_ICON[id] || LP_ICON.home; }
function lpDriveIcon(kind) { return kind === 'network' ? LP_ICON.network : kind === 'removable' ? LP_ICON.usb : kind === 'cdrom' ? LP_ICON.disc : LP_ICON.drive; }
// A drive's path as shown ('C:\' -> 'C:'; a mount point as is).
function lpDrivePath(p) { p = String(p || ''); return localIsWin() ? p.replace(/\\$/, '') : p; }
// A drive the Go side listed: 'C:\' (Windows), an absolute mount point (unix).
function lpDriveOk(p) { return localIsWin() ? /^[A-Za-z]:\\$/.test(p) : (p.charAt(0) === '/' && p.indexOf('..') < 0 && p.length <= 4096); }
function lpDriveLabel(d) {
  var p = lpDrivePath(d.path);
  return d.label ? p + ' · ' + String(d.label).slice(0, 60) : p;
}

// ── Preferences (this computer; localStorage never trusted as a path) ──
function lpPref(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
function lpSetPref(k, v) { try { localStorage.setItem(k, v); } catch (e) {} }
// The split's ratio: the local pane's share, 25..75 %.
function lpRatio() { var n = Number(lpPref('oblireach_split_ratio')); return (isFinite(n) && n >= 0.25 && n <= 0.75) ? n : 0.5; }
// The local pane shown (on by default).
function lpShownPref() { return lpPref('oblireach_files_split') !== '0'; }

// ── The drag between the panes (one at a time, this window) ──
var xpDrag = null; // { tabId, side: 'local' | 'remote', paths: [], view (the device's view it left: explorer_tabs.go) }
var XP_DRAG_TYPE = 'application/x-oblireach-items';
function xpDragStart(tab, side, paths, e) {
  xpDrag = { tabId: tab.id, side: side, paths: paths.slice() };
  try {
    if (e && e.dataTransfer) {
      e.dataTransfer.setData(XP_DRAG_TYPE, JSON.stringify(xpDrag));
      xpDrag.view = tab; // (never in the data: an object of the page)
      e.dataTransfer.effectAllowed = 'copy';
    }
  } catch (x) {}
}
function xpDragEnd() { xpDrag = null; }
function xpDragIs(e) { var t = e && e.dataTransfer && e.dataTransfer.types; return !!t && Array.prototype.indexOf.call(t, XP_DRAG_TYPE) >= 0; }
// The drag comes from the other pane of this explorer.
function xpDragFrom(tab, side) { return !!xpDrag && xpDrag.tabId === tab.id && xpDrag.side === side && xpDrag.paths.length > 0; }

// ── The pane ──
// tab.lp: { path ('' = This PC), items, view, sel, anchor, filter, sortKey,
// sortDir, listed, loading, listSeq, roots, dropPath, ui }
function lpCreate(tab, el) {
  var lp = tab.lp = { path: '', items: [], view: [], sel: new Set(), anchor: '', filter: '', sortKey: 'name', sortDir: 1, listed: false, loading: false,
    listSeq: 0, roots: null, dropPath: null, rowEls: new Map(), scrollTop: 0, ui: {} };
  var ui = lp.ui;
  ui.pane = el;
  el.addEventListener('focusin', function () { xpFocusPane(tab, 'local'); });
  // Its title: This PC, the computer's name.
  var head = ui.head = explorerEl('div', 'xp-head', el, 'lp-head');
  var ic = explorerEl('span', 'xp-head-ico', head); ic.innerHTML = LP_ICON.pc;
  var ht = explorerEl('div', 'xp-head-txt', head);
  var t1 = explorerEl('span', 'xp-head-t', ht); t1.textContent = T('local.thisPc');
  ui.computer = explorerEl('span', 'xp-head-s', ht, 'lp-computer');
  // Quick places and drives.
  ui.places = explorerEl('div', 'xp-places', el, 'lp-places');
  // Bar: up, refresh, the path, the filter.
  var bar = explorerEl('div', 'exp-bar', el);
  ui.up = explorerBtn(bar, 'lp-up', EXPLORER_ICON.up, 'explorer.up', function () { lpUp(tab); });
  ui.refresh = explorerBtn(bar, 'lp-refresh', EXPLORER_ICON.refresh, 'common.refresh', function () { lpRefresh(tab); });
  var crumbs = ui.crumbs = explorerEl('div', 'exp-crumbs', bar, 'lp-crumbs');
  crumbs.title = T('explorer.path');
  crumbs.addEventListener('click', function (e) { if (!e || e.target === crumbs) lpEditPath(tab, true); });
  var pathIn = ui.path = explorerEl('input', 'exp-path', bar, 'lp-path');
  pathIn.type = 'text'; pathIn.setAttribute('autocomplete', 'off'); pathIn.setAttribute('spellcheck', 'false');
  pathIn.setAttribute('aria-label', T('explorer.path')); pathIn.placeholder = T('explorer.path');
  pathIn.addEventListener('keydown', function (e) {
    if (!e) return;
    if (e.key === 'Enter') { if (e.preventDefault) e.preventDefault(); lpGoTyped(tab, pathIn.value); }
    else if (e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); lpEditPath(tab, false); lpFocus(tab); }
  });
  pathIn.addEventListener('blur', function () { lpEditPath(tab, false); });
  var filt = ui.filter = explorerEl('input', 'exp-filter', bar, 'lp-filter');
  filt.type = 'search'; filt.setAttribute('autocomplete', 'off'); filt.setAttribute('spellcheck', 'false');
  filt.placeholder = T('explorer.filter'); filt.setAttribute('aria-label', T('explorer.filter'));
  filt.addEventListener('input', function () { lp.filter = String(filt.value || ''); lpRenderList(tab); });
  // (Escape: cleared, the keyboard back to the list: explorer_viewer.go explorerFilterEscape)
  filt.addEventListener('keydown', function (e) { explorerFilterEscape(tab, 'local', e); });
  // Actions: new folder, rename, the Recycle Bin, Explorer; send to the device.
  var acts = explorerEl('div', 'exp-bar xp-acts', el);
  ui.mkdir = explorerBtn(acts, 'lp-mkdir', EXPLORER_ICON.newFolder, 'explorer.newFolder', function () { lpMkdir(tab); });
  ui.rename = explorerBtn(acts, 'lp-rename', EXPLORER_ICON.rename, 'explorer.rename', function () { lpRenameSel(tab); });
  ui.trash = explorerBtn(acts, 'lp-trash', EXPLORER_ICON.trash, 'local.recycle', function () { lpTrashSel(tab); });
  ui.reveal = explorerBtn(acts, 'lp-reveal', LP_ICON.reveal, 'local.reveal', function () { lpRevealSel(tab); });
  explorerEl('div', 'toolbar-sep', acts);
  ui.send = explorerEl('button', 'btn-sm', acts, 'lp-upload');
  ui.send.innerHTML = EXPLORER_ICON.upload + '<span>' + TH('local.sendToDevice') + '</span>';
  ui.send.title = T('local.sendToDeviceKey');
  ui.send.addEventListener('click', function () { lpSendSel(tab); });
  // The list.
  var list = ui.list = explorerEl('div', 'exp-list', el, 'lp-list');
  list.tabIndex = 0; list.setAttribute('role', 'grid'); list.setAttribute('aria-label', T('local.thisPc'));
  var table = explorerEl('table', 'dt exp-table', list);
  var thead = explorerEl('thead', '', table), hr = explorerEl('tr', '', thead);
  ui.th = {};
  [['name', 'explorer.col.name', 'c-name'], ['size', 'explorer.col.size', 'c-size'], ['modified', 'explorer.col.modified', 'c-mod']].forEach(function (c) {
    var th = explorerEl('th', c[2], hr); th.dataset.sort = c[0]; th.textContent = T(c[1]);
    th.addEventListener('click', function () { lpSortBy(tab, c[0]); });
    ui.th[c[0]] = th;
  });
  ui.tbody = explorerEl('tbody', '', table);
  ui.note = explorerEl('div', 'exp-note', list, 'lp-note');
  ui.drop = explorerEl('div', 'exp-drop', list, 'lp-drop');
  ui.hint = explorerEl('div', 'xp-note', el, 'lp-hint');
  list.addEventListener('keydown', function (e) { lpListKey(tab, e); });
  list.addEventListener('scroll', function () { if (list.isConnected !== false) lp.scrollTop = list.scrollTop || 0; });
  list.addEventListener('contextmenu', function (e) { if (e && e.preventDefault) e.preventDefault(); lpMenuOpen(tab, e, null); });
  list.addEventListener('mousedown', function (e) {
    if (!e || (e.target !== list && e.target !== ui.note) || e.button !== 0 || !lp.sel.size) return;
    if (typeof e.offsetX === 'number' && typeof list.clientWidth === 'number' && e.offsetX >= list.clientWidth) return;
    lp.sel.clear(); lp.anchor = ''; lpPaintSel(tab);
  });
  // The device's files dragged here: downloaded to the folder on screen (or
  // to the folder row under the pointer).
  el.addEventListener('dragover', function (e) {
    if (!xpDragIs(e)) return;
    var can = xpDragFrom(tab, 'remote') && lpCanWrite(tab);
    if (e.preventDefault) e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = can ? 'copy' : 'none';
    lpDropTarget(tab, can ? lpRowPathAt(tab, e) : null, can);
  });
  el.addEventListener('dragleave', function (e) {
    if (!e || !e.relatedTarget || typeof el.contains !== 'function' || !el.contains(e.relatedTarget)) lpDropTarget(tab, null, false);
  });
  el.addEventListener('drop', function (e) {
    var target = lp.dropPath;
    lpDropTarget(tab, null, false);
    if (!xpDragIs(e)) return;
    if (e.preventDefault) e.preventDefault();
    if (!xpDragFrom(tab, 'remote') || !lpCanWrite(tab)) return;
    var paths = xpDrag.paths.slice(), from = (xpDrag.view && xpDrag.view.ex && !xpDrag.view.ex.disposed) ? xpDrag.view : tab;
    xpDragEnd();
    xferDownloadPaths(from, paths, target || lp.path); // (from the view it was dragged from)
  });
  // Keys of the whole pane: F5 / Ctrl+R refresh, Ctrl+F the filter, Ctrl+L
  // the path (never the device's pane's: stopped here).
  el.addEventListener('keydown', function (e) {
    if (!e) return;
    var k = String(e.key || ''), code = String(e.code || ''), ctrl = !!(e.ctrlKey || e.metaKey);
    var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
    if ((k === 'F5' || code === 'F5') && !e.altKey) { stop(); lpRefresh(tab); return; }
    if (ctrl && !e.altKey && (code === 'KeyR' || k.toLowerCase() === 'r')) { stop(); lpRefresh(tab); return; }
    if (ctrl && !e.altKey && (code === 'KeyF' || k.toLowerCase() === 'f')) { stop(); try { filt.focus(); } catch (x) {} return; }
    if (ctrl && !e.altKey && (code === 'KeyL' || k.toLowerCase() === 'l')) { stop(); lpEditPath(tab, true); return; }
    if (k === 'F5' || (ctrl && /^[rfl]$/i.test(k))) { stop(); return; }
    // A character typed in this pane (not in a field): its filter
    // (explorer_viewer.go explorerPaneKey; never the device's pane's).
    explorerPaneKey(tab, 'local', e, false);
  });
  lpRender(tab);
}
// The first listing: the folder of last time, else Downloads, else This PC.
async function lpStart(tab) {
  var lp = tab.lp;
  if (!lp || lp.started) return;
  lp.started = true;
  var roots = await lpRootsLoad(tab);
  if (tab.ex.disposed) return;
  var last = localNormPath(lpPref('oblireach_local_last') || '');
  var dl = null;
  (roots && roots.places || []).forEach(function (p) { if (p.id === 'downloads') dl = p.path; });
  if (last && await lpList(tab, last, { quiet: true })) return;
  if (tab.ex.disposed) return;
  if (dl && await lpList(tab, dl, { quiet: true })) return;
  await lpList(tab, '');
}
async function lpRootsLoad(tab) {
  var lp = tab.lp;
  try {
    var r = await localFs('reachFsRoots');
    lp.roots = {
      computer: (r && typeof r.computer === 'string') ? r.computer.slice(0, 120) : '',
      places: (r && Array.isArray(r.places) ? r.places : []).filter(function (p) { return p && typeof p.path === 'string' && typeof p.id === 'string'; }).slice(0, 8),
      drives: (r && Array.isArray(r.drives) ? r.drives : []).filter(function (d) { return d && typeof d.path === 'string' && lpDriveOk(d.path); }).slice(0, 26),
    };
  } catch (e) {
    lp.roots = lp.roots || { computer: '', places: [], drives: [] };
  }
  lpRenderPlaces(tab);
  return lp.roots;
}
// This PC: the known folders, then the drives.
function lpRootItems(tab) {
  var r = tab.lp.roots || { places: [], drives: [] }, out = [];
  r.places.forEach(function (p) { out.push({ name: lpPlaceName(p.id), path: p.path, isDir: true, size: 0, modified: '', place: p.id }); });
  r.drives.forEach(function (d) { out.push({ name: lpDriveLabel(d), path: d.path, isDir: true, size: 0, modified: '', drive: d.kind || 'fixed' }); });
  return out;
}
function lpItems(res) {
  var list = res && Array.isArray(res.entries) ? res.entries : [], dir = String((res && res.path) || ''), out = [];
  for (var i = 0; i < list.length && out.length < EXPLORER_MAX_ITEMS; i++) {
    var f = list[i];
    if (!f || typeof f.name !== 'string' || !f.name) continue;
    var size = Number(f.size);
    out.push({ name: f.name.slice(0, 1024), path: explorerJoin(dir, f.name, LOCAL_OS), isDir: f.isDir === true, size: isFinite(size) && size > 0 ? size : 0,
      modified: typeof f.modified === 'string' ? f.modified : '', hidden: f.hidden === true, link: f.link === true, readonly: f.readonly === true });
  }
  return out;
}
// A folder listed ('' = This PC). opts: keepSel, select, quiet.
async function lpList(tab, path, opts) {
  var lp = tab.lp;
  opts = opts || {};
  path = String(path || '');
  var seq = ++lp.listSeq;
  lp.loading = true;
  lpRenderNote(tab); lpUpdateBar(tab);
  var items, dir, truncated = false;
  try {
    if (!path) {
      if (!lp.roots || opts.fresh) await lpRootsLoad(tab);
      items = lpRootItems(tab); dir = '';
    } else {
      var set = editSettingsNow();
      var res = await localFs('reachFsList', path, { hidden: !!set.showHidden });
      items = lpItems(res); dir = String((res && res.path) || path); truncated = !!(res && res.truncated);
    }
  } catch (e) {
    if (seq !== lp.listSeq || tab.ex.disposed) return false;
    lp.loading = false;
    if (!opts.quiet) localFsSay(e);
    lpRender(tab);
    return false;
  }
  if (seq !== lp.listSeq || tab.ex.disposed) return false;
  lp.loading = false;
  var same = dir === lp.path;
  var prev = opts.keepSel && same ? lp.sel : null;
  if (!same) {
    lp.filter = ''; lp.ui.filter.value = '';
    lp.scrollTop = 0;
    try { lp.ui.list.scrollTop = 0; } catch (e) {}
  }
  lp.path = dir; lp.items = items; lp.truncated = truncated; lp.listed = true;
  lp.sel = new Set(); lp.anchor = '';
  if (prev) items.forEach(function (it) { if (prev.has(it.path)) lp.sel.add(it.path); });
  if (opts.select) items.forEach(function (it) { if (it.path === opts.select || it.name === opts.select) { lp.sel.add(it.path); lp.anchor = it.path; } });
  if (dir) lpSetPref('oblireach_local_last', dir);
  lpRender(tab);
  return true;
}
function lpRefresh(tab) { return lpList(tab, tab.lp.path, { keepSel: true, fresh: !tab.lp.path }); }
function lpUp(tab) {
  var lp = tab.lp;
  if (!lp.path) return;
  var from = lp.path;
  lpList(tab, explorerParent(lp.path, LOCAL_OS), { select: from });
}
function lpGoTyped(tab, v) {
  var p = localNormPath(v);
  if (p === null) { reachNotify(T('local.err.invalidPath'), 'warn'); return; }
  lpEditPath(tab, false);
  lpList(tab, p);
  lpFocus(tab);
}
function lpEditPath(tab, on) {
  var ui = tab.lp.ui;
  if (on) {
    ui.path.value = tab.lp.path;
    ui.pane.classList.add('editing');
    try { ui.path.focus(); if (typeof ui.path.select === 'function') ui.path.select(); } catch (e) {}
  } else ui.pane.classList.remove('editing');
}
function lpSortBy(tab, key) {
  var lp = tab.lp;
  if (lp.sortKey === key) lp.sortDir = -lp.sortDir; else { lp.sortKey = key; lp.sortDir = 1; }
  lpRenderList(tab);
}
function lpFocus(tab) {
  var l = tab && tab.lp && tab.lp.ui.list;
  if (l && toolShown(xvOwnerOf(tab)) && !tab.lp.ui.pane.classList.contains('editing')) { try { l.focus(); } catch (e) {} }
}
// The pane with the keyboard wears the accent ring; Tab switches.
function xpFocusPane(tab, side) {
  tab = xvOwnerOf(tab); // (the tab's, whichever view: explorer_tabs.go)
  var ex = tab.ex;
  if (!ex || !ex.split) return;
  ex.focusSide = side;
  if (tab.lp) tab.lp.ui.pane.classList.toggle('focus', side === 'local');
  if (ex.ui.rpane) ex.ui.rpane.classList.toggle('focus', side === 'remote');
}

// ── Drawing ──
function lpRender(tab) {
  if (!tab.lp) return;
  lpRenderPlaces(tab);
  lpRenderCrumbs(tab);
  lpRenderList(tab);
}
function lpRenderPlaces(tab) {
  var lp = tab.lp, ui = lp.ui, r = lp.roots;
  ui.computer.textContent = r && r.computer ? r.computer : '';
  ui.places.innerHTML = '';
  if (!r) return;
  // The chip of where the pane is: the closest place or drive holding it
  // (Downloads, not also its drive; the drive for elsewhere on it).
  var cur = String(lp.path || '').toLowerCase(), best = '', bestLen = -1;
  var win = localIsWin(), sep = localSep();
  if (!win) cur = String(lp.path || ''); // (POSIX paths are case-sensitive)
  var under = function (p) {
    p = win ? String(p).toLowerCase().replace(/\\$/, '') : String(p).replace(/\/+$/, '');
    return !!cur && (cur === p || (p === '' ? cur.charAt(0) === '/' : cur.indexOf(p + sep) === 0));
  };
  r.places.concat(r.drives).forEach(function (x) { if (under(x.path) && x.path.length > bestLen) { best = x.path; bestLen = x.path.length; } });
  var chip = function (label, title, icon, path) {
    var b = explorerEl('button', 'xp-chip' + (path === best ? ' cur' : ''), ui.places);
    b.innerHTML = icon;
    var s = explorerEl('span', '', b); s.textContent = label;
    b.title = title;
    b.dataset.path = path;
    b.addEventListener('click', function () { lpList(tab, path); lpFocus(tab); });
  };
  ['home', 'desktop', 'documents', 'downloads'].forEach(function (id) {
    r.places.forEach(function (p) { if (p.id === id) chip(lpPlaceName(id), p.path, lpPlaceIcon(id), p.path); });
  });
  r.drives.forEach(function (d) { chip(win ? lpDrivePath(d.path) : (d.label ? String(d.label).slice(0, 60) : d.path), lpDriveLabel(d), lpDriveIcon(d.kind), d.path); });
}
function lpRenderCrumbs(tab) {
  var lp = tab.lp, el = lp.ui.crumbs;
  el.innerHTML = '';
  var cs = explorerCrumbs(lp.path, LOCAL_OS);
  // (unix: This PC first, then the root '/' when a folder is listed)
  if (!localIsWin()) cs = [{ label: '', path: '' }].concat(lp.path ? cs : []);
  cs[0].label = T('local.thisPc');
  cs.forEach(function (c, i) {
    if (i > 0) { var s = explorerEl('span', 'exp-crumb-sep', el); s.textContent = '›'; }
    var b = explorerEl('button', 'exp-crumb' + (i === cs.length - 1 ? ' cur' : ''), el);
    b.textContent = c.label; b.title = c.path || c.label; b.dataset.path = c.path;
    if (i === cs.length - 1) b.setAttribute('aria-current', 'location');
    b.addEventListener('click', function () { lpList(tab, c.path, c.path === lp.path ? { keepSel: true } : null); });
  });
}
function lpVisible(tab) {
  var lp = tab.lp, f = lp.filter.trim().toLowerCase();
  var items = f ? lp.items.filter(function (it) { return it.name.toLowerCase().indexOf(f) >= 0; }) : lp.items;
  return lp.path ? explorerSort(items, lp.sortKey, lp.sortDir) : items; // (This PC: in its own order)
}
function lpRenderList(tab) {
  var lp = tab.lp, ui = lp.ui;
  lp.view = lpVisible(tab);
  ui.tbody.innerHTML = '';
  lp.rowEls = new Map();
  lp.view.forEach(function (it) {
    var tr = explorerEl('tr', (lp.sel.has(it.path) ? 'sel' : '') + (it.hidden ? ' hid' : ''), ui.tbody);
    tr.dataset.path = it.path;
    tr.setAttribute('aria-selected', lp.sel.has(it.path) ? 'true' : 'false');
    if (lp.path) tr.draggable = true;
    var td = explorerEl('td', 'c-name', tr), nm = explorerEl('div', 'exp-name', td);
    var ic = explorerEl('span', 'exp-ico' + (it.isDir ? ' dir' : ''), nm);
    ic.innerHTML = it.place ? lpPlaceIcon(it.place).replace(/width="12" height="12"/, 'width="16" height="16"') : it.drive ? lpDriveIcon(it.drive).replace(/width="12" height="12"/, 'width="16" height="16"')
      : it.isDir ? EXPLORER_ICON.folder : EXPLORER_ICON.file;
    var sp = explorerEl('span', 'nm', nm); sp.textContent = it.name;
    if (it.link) { var lk = explorerEl('span', 'lnk', nm); lk.textContent = '↗'; lk.title = T('local.link'); }
    td.title = it.path;
    var sz = explorerEl('td', 'c-size', tr); sz.textContent = it.isDir ? '' : reachFmtBytes(it.size);
    var md = explorerEl('td', 'c-mod', tr); md.textContent = explorerFmtDate(it.modified);
    tr.addEventListener('click', function (e) { lpClickRow(tab, it, e || {}); });
    tr.addEventListener('dblclick', function () { lpOpenItem(tab, it); });
    tr.addEventListener('contextmenu', function (e) {
      if (e && e.preventDefault) e.preventDefault();
      if (e && e.stopPropagation) e.stopPropagation();
      if (!lp.sel.has(it.path)) { lp.sel = new Set([it.path]); lp.anchor = it.path; lpPaintSel(tab); }
      lpMenuOpen(tab, e, it);
    });
    tr.addEventListener('dragstart', function (e) {
      if (!lp.path) return;
      if (!lp.sel.has(it.path)) { lp.sel = new Set([it.path]); lp.anchor = it.path; lpPaintSel(tab); }
      xpDragStart(tab, 'local', lpSelItems(tab).map(function (x) { return x.path; }), e);
    });
    tr.addEventListener('dragend', xpDragEnd);
    lp.rowEls.set(it.path, tr);
  });
  ['name', 'size', 'modified'].forEach(function (k) {
    var th = ui.th[k];
    th.setAttribute('aria-sort', lp.sortKey === k ? (lp.sortDir === 1 ? 'ascending' : 'descending') : 'none');
    th.textContent = T(k === 'name' ? 'explorer.col.name' : k === 'size' ? 'explorer.col.size' : 'explorer.col.modified') +
      (lp.path && lp.sortKey === k ? (lp.sortDir === 1 ? ' ↑' : ' ↓') : '');
  });
  lpRenderNote(tab);
  lpUpdateBar(tab);
}
function lpRenderNote(tab) {
  var lp = tab.lp, n = lp.ui.note, text = '';
  if (lp.loading && !lp.view.length) text = T('common.loading');
  else if (lp.listed && !lp.view.length) text = lp.items.length ? T('explorer.noMatch') : T('explorer.empty');
  n.textContent = text;
  n.style.display = text ? '' : 'none';
  lp.ui.list.classList.toggle('loading', !!lp.loading);
  lp.ui.hint.textContent = lp.truncated ? T('local.truncated', { max: EXPLORER_MAX_ITEMS }) : '';
}
function lpPaintSel(tab) {
  var lp = tab.lp;
  lp.rowEls.forEach(function (tr, p) {
    var on = lp.sel.has(p);
    tr.classList.toggle('sel', on);
    tr.setAttribute('aria-selected', on ? 'true' : 'false');
  });
  lpUpdateBar(tab);
}
function lpSelItems(tab) { var lp = tab.lp; return lp.items.filter(function (it) { return lp.sel.has(it.path); }); }
// A folder of a drive can receive (not This PC).
function lpCanWrite(tab) { var lp = tab.lp; return !!lp && lp.listed && !!lp.path; }
function lpUpdateBar(tab) {
  var lp = tab.lp, ui = lp.ui, sel = lpSelItems(tab), inDir = !!lp.path;
  ui.up.disabled = !lp.path;
  ui.mkdir.disabled = !inDir;
  ui.rename.disabled = !inDir || sel.length !== 1;
  ui.trash.disabled = !inDir || !sel.length;
  ui.reveal.disabled = !sel.length && !inDir;
  ui.send.disabled = !inDir || !sel.length || !explorerCanWrite(tab);
  xpUpdateRail(tab);
}

// ── Mouse, keys ──
// The selection's rules, the device's pane's too (explorer_viewer.go): st
// holds view, sel, anchor; items are known by their path.
function paneSelClick(st, it, e) {
  var ctrl = !!(e.ctrlKey || e.metaKey);
  if (e.shiftKey && st.anchor) {
    var a = -1, b = -1;
    st.view.forEach(function (x, i) { if (x.path === st.anchor) a = i; if (x.path === it.path) b = i; });
    if (a >= 0 && b >= 0) {
      if (!ctrl) st.sel = new Set();
      for (var i = Math.min(a, b); i <= Math.max(a, b); i++) st.sel.add(st.view[i].path);
      return;
    }
  }
  if (ctrl) { if (st.sel.has(it.path)) st.sel.delete(it.path); else st.sel.add(it.path); }
  else st.sel = new Set([it.path]);
  st.anchor = it.path;
}
// Arrows, Home, End: the row it moves to (null: none).
function paneSelMove(st, k, shift) {
  if (!st.view.length) return null;
  var cur = -1;
  st.view.forEach(function (x, i) { if (x.path === st.anchor) cur = i; });
  var n = k === 'Home' ? 0 : k === 'End' ? st.view.length - 1 : k === 'ArrowDown' ? Math.min(st.view.length - 1, cur + 1) : Math.max(0, cur < 0 ? 0 : cur - 1);
  var it = st.view[n];
  if (shift && st.anchor) paneSelClick(st, it, { shiftKey: true });
  else { st.sel = new Set([it.path]); st.anchor = it.path; }
  return it;
}
function lpClickRow(tab, it, e) { paneSelClick(tab.lp, it, e); lpPaintSel(tab); }
// Open: a folder is listed. A file: nothing is started (the hint says how
// to send it).
function lpOpenItem(tab, it) {
  if (!it) return;
  if (it.isDir) { lpList(tab, it.path); return; }
  tab.lp.ui.hint.textContent = T('local.fileHint');
}
function lpListKey(tab, e) {
  if (!e) return;
  var lp = tab.lp, k = String(e.key || ''), ctrl = !!(e.ctrlKey || e.metaKey);
  var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  // (a character: the filter; Backspace: the filter's last one while it has one)
  if (explorerPaneKey(tab, 'local', e, true)) return;
  var sel = lpSelItems(tab);
  if (k === 'Tab' && !ctrl && !e.altKey && !e.shiftKey && tab.ex.split) { stop(); explorerFocus(tab); return; }
  if (k === 'Enter') {
    stop();
    if (sel.length === 1 && sel[0].isDir) lpOpenItem(tab, sel[0]);
    else if (sel.length && lp.path) lpSendSel(tab);
    return;
  }
  if (k === 'Backspace' || (k === 'ArrowUp' && e.altKey)) { stop(); lpUp(tab); return; }
  if (k === 'Delete') { stop(); lpTrashSel(tab); return; }
  if (k === 'F2') { stop(); lpRenameSel(tab); return; }
  if (k === 'Escape') { if (lp.sel.size) { stop(); lp.sel = new Set(); lp.anchor = ''; lpPaintSel(tab); } return; }
  if (ctrl && (e.code === 'KeyA' || k.toLowerCase() === 'a')) { stop(); lp.sel = new Set(lp.view.map(function (x) { return x.path; })); lpPaintSel(tab); return; }
  if (ctrl && (e.code === 'KeyU' || k.toLowerCase() === 'u')) { stop(); lpSendSel(tab); return; }
  if ((k === 'ArrowDown' || k === 'ArrowUp' || k === 'Home' || k === 'End') && lp.view.length) {
    stop();
    var it = paneSelMove(lp, k, !!e.shiftKey);
    lpPaintSel(tab);
    var tr = it && lp.rowEls.get(it.path);
    if (tr && typeof tr.scrollIntoView === 'function') { try { tr.scrollIntoView({ block: 'nearest' }); } catch (x) {} }
  }
}
// The folder row under the pointer of a drag (a drop there goes in it).
function lpRowPathAt(tab, e) {
  var n = e && e.target, lp = tab.lp;
  for (var i = 0; n && i < 8; i++, n = n.parentElement) {
    if (n.tagName === 'TR' && n.dataset && n.dataset.path) {
      var it = null;
      lp.view.forEach(function (x) { if (x.path === n.dataset.path) it = x; });
      return it && it.isDir && !it.link ? it.path : null;
    }
  }
  return null;
}
function lpDropTarget(tab, rowPath, on) {
  var lp = tab.lp;
  if (lp.dropPath && lp.rowEls.get(lp.dropPath)) lp.rowEls.get(lp.dropPath).classList.remove('drop-row');
  lp.dropPath = rowPath || null;
  if (lp.dropPath && lp.rowEls.get(lp.dropPath)) lp.rowEls.get(lp.dropPath).classList.add('drop-row');
  lp.ui.drop.textContent = on ? T('local.dropHint', { path: rowPath || lp.path }) : '';
  lp.ui.drop.classList.toggle('visible', !!on);
}

// ── Operations ──
async function lpMkdir(tab) {
  var lp = tab.lp;
  if (!lp.path) return;
  var dir = lp.path;
  var name = await explorerDialog({ tab: tab, title: T('explorer.newFolder'), text: T('explorer.newFolderPrompt'), value: '', ok: 'explorer.create',
    icon: EXPLORER_ICON.newFolder, validate: localNameCheck });
  if (!name || tab.ex.disposed) return;
  var r;
  try { r = await localFs('reachFsMkdir', dir, name); } catch (e) { localFsSay(e, name); return; }
  if (tab.ex.disposed) return;
  reachNotify(T(r && r.existed ? 'local.folderExists' : 'explorer.created', { name: (r && r.name) || name }), 'ok');
  if (lp.path === dir) lpList(tab, dir, { select: (r && r.path) || name });
}
async function lpRenameSel(tab) {
  var lp = tab.lp, sel = lpSelItems(tab);
  if (!lp.path || sel.length !== 1) return;
  var it = sel[0], dir = lp.path, dot = it.isDir ? -1 : it.name.lastIndexOf('.');
  var name = await explorerDialog({ tab: tab, title: T('explorer.rename'), text: T('explorer.renamePrompt', { name: it.name }), value: it.name,
    selectTo: dot > 0 ? dot : it.name.length, ok: 'explorer.rename', icon: EXPLORER_ICON.rename, validate: localNameCheck });
  if (!name || name === it.name || tab.ex.disposed) return;
  var r;
  try { r = await localFs('reachFsRename', it.path, name); } catch (e) { localFsSay(e, name); return; }
  if (tab.ex.disposed) return;
  reachNotify(T('explorer.renamed', { name: name }), 'ok');
  if (lp.path === dir) lpList(tab, dir, { select: (r && r.path) || name });
}
// The Recycle Bin, after the confirmation (never a permanent delete).
async function lpTrashSel(tab) {
  var lp = tab.lp, sel = lpSelItems(tab);
  if (!lp.path || !sel.length) return;
  var text = sel.length > 1 ? T('local.recycleMany', { count: sel.length }) : T('local.recycleOne', { name: sel[0].name });
  var ok = await explorerDialog({ tab: tab, title: T('local.recycle'), text: text, ok: 'local.recycle', danger: true, icon: EXPLORER_ICON.trash });
  if (ok !== true || tab.ex.disposed) return;
  var dir = lp.path;
  try { await localFs('reachFsTrash', sel.map(function (x) { return x.path; })); }
  catch (e) { localFsSay(e); return; }
  if (tab.ex.disposed) return;
  reachNotify(sel.length > 1 ? T('local.recycledMany', { count: sel.length }) : T('local.recycled', { name: sel[0].name }), 'ok');
  if (lp.path === dir) lpList(tab, dir, { keepSel: true });
}
function lpRevealSel(tab) {
  var lp = tab.lp, sel = lpSelItems(tab);
  var p = sel.length ? sel[0].path : lp.path;
  if (!p) return;
  reachNative('reachFsReveal', p).catch(function (e) { localFsSay(localFsErr({ code: String((e && e.message) || e || 'io') })); });
}
async function lpCopyPath(p) { return explorerCopyPath(p); }
// The selection sent to the folder on screen of the device.
function lpSendSel(tab) {
  var lp = tab.lp, sel = lpSelItems(tab);
  if (!lp.path || !sel.length) return;
  if (!explorerCanWrite(tab)) { reachNotify(T('local.pickRemoteFolder'), 'warn'); return; }
  xferUploadLocal(tab, lp.path, sel.map(function (x) { return x.name; }), tab.ex.path);
}

// ── Context menu ──
function lpMenuOpen(tab, e, it) {
  explorerMenuClose();
  var lp = tab.lp, sel = lpSelItems(tab), inDir = !!lp.path, items = [];
  var rdir = tab.ex.path ? tab.ex.path : T('explorer.roots');
  if (it) {
    if (it.isDir) items.push({ icon: EXPLORER_ICON.open, key: 'explorer.open', run: function () { lpOpenItem(tab, it); } });
    if (inDir) items.push({ icon: EXPLORER_ICON.upload, key: 'local.sendTo', label: T('local.sendTo', { path: rdir }), off: !explorerCanWrite(tab), run: function () { lpSendSel(tab); } });
    if (inDir) items.push({ icon: EXPLORER_ICON.rename, key: 'explorer.rename', off: sel.length !== 1, run: function () { lpRenameSel(tab); } });
    items.push({ icon: EXPLORER_ICON.copy, key: 'explorer.copyPath', run: function () { lpCopyPath(it.path); } });
    items.push({ icon: LP_ICON.reveal, key: 'local.reveal', run: function () { lpRevealSel(tab); } });
    if (inDir) items.push({ icon: EXPLORER_ICON.trash, key: 'local.recycle', danger: true, run: function () { lpTrashSel(tab); } });
  } else {
    if (!lp.listed) return;
    if (inDir) items.push({ icon: EXPLORER_ICON.newFolder, key: 'explorer.newFolder', run: function () { lpMkdir(tab); } });
    items.push({ icon: EXPLORER_ICON.refresh, key: 'common.refresh', run: function () { lpRefresh(tab); } });
    if (inDir) items.push({ icon: EXPLORER_ICON.copy, key: 'explorer.copyPath', run: function () { lpCopyPath(lp.path); } });
    if (inDir) items.push({ icon: LP_ICON.reveal, key: 'local.reveal', run: function () { lpRevealSel(tab); } });
  }
  explorerMenuShow(tab, e, items, function () { lpFocus(tab); });
}

// ── The rail between the panes: the arrows, the resize ──
function xpRailBuild(tab, rail) {
  var ui = tab.ex.ui;
  ui.toRemote = explorerEl('button', 'xp-rail-btn', rail, 'xp-to-remote');
  ui.toRemote.innerHTML = LP_ICON.toRemote; ui.toRemote.title = T('local.sendToDeviceKey'); ui.toRemote.setAttribute('aria-label', T('local.sendToDeviceKey'));
  ui.toRemote.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); lpSendSel(tab); });
  ui.toLocal = explorerEl('button', 'xp-rail-btn', rail, 'xp-to-local');
  ui.toLocal.innerHTML = LP_ICON.toLocal; ui.toLocal.title = T('local.downloadHereKey'); ui.toLocal.setAttribute('aria-label', T('local.downloadHereKey'));
  ui.toLocal.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); explorerDownloadSel(tab); });
  [ui.toRemote, ui.toLocal].forEach(function (b) { b.addEventListener('pointerdown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); }); });
  rail.addEventListener('pointerdown', function (e) { xpResizeStart(tab, rail, e); });
  xpApplyRatio(tab, lpRatio());
}
// The rail's arrows, and the buttons of each pane that depend on the other
// one (Send: the device's folder; Download: this PC's), whichever pane just
// changed (both bars end here).
function xpUpdateRail(tab) {
  var ui = tab.ex && tab.ex.ui;
  if (!ui || !ui.toRemote) return;
  var lp = tab.lp;
  ui.toRemote.disabled = !lp || !lp.path || !lp.sel.size || !explorerCanWrite(tab);
  ui.toLocal.disabled = !lpCanWrite(tab) || !tab.ex.sel.size || !tab.ex.listed || (tab.ex.os === 'windows' && tab.ex.path === '');
  if (lp && lp.ui && lp.ui.send) lp.ui.send.disabled = !lp.path || !lpSelItems(tab).length || !explorerCanWrite(tab);
  if (ui.download) ui.download.disabled = explorerDownloadOff(tab);
}
// F6 (Shift+F6 back): this PC's list, the device's list, the dock when it
// shows, as Explorer's F6 goes round its areas (Tab only switches the lists).
function xpFocusCycle(tab, dir, target) {
  var ex = tab.ex, ui = ex.ui, areas = [];
  if (xpLocalShown(tab)) areas.push('local');
  areas.push('remote');
  var dockBtn = xpDockFirstButton(tab);
  if (dockBtn) areas.push('dock');
  var inEl = function (el) { try { return !!(el && target && typeof el.contains === 'function' && el.contains(target)); } catch (e) { return false; } };
  var from = inEl(ui.dock) ? 'dock' : (tab.lp && inEl(tab.lp.ui.pane)) ? 'local' : inEl(ui.rpane) ? 'remote' : (ex.focusSide || 'remote');
  var i = areas.indexOf(from);
  if (i < 0) i = areas.indexOf('remote');
  var to = areas[(i + (dir < 0 ? -1 : 1) + areas.length) % areas.length];
  if (to === 'local') lpFocus(tab);
  else if (to === 'remote') explorerFocus(tab);
  else { try { dockBtn.focus(); } catch (e) {} }
  return to;
}
// The dock's first button (its "Transfers" tab), when the dock shows.
function xpDockFirstButton(tab) {
  var d = tab.ex.ui.dock;
  if (!d || d.classList.contains('empty')) return null;
  var head = d.children && d.children[0];
  var b = head && head.children && head.children[0];
  return (b && String(b.tagName).toUpperCase() === 'BUTTON') ? b : null;
}
function xpApplyRatio(tab, r) {
  var lp = tab.lp, ui = tab.ex.ui;
  if (!lp || !ui.rpane) return;
  lp.ui.pane.style.flex = r + ' 1 0';
  ui.rpane.style.flex = (1 - r) + ' 1 0';
}
function xpResizeStart(tab, rail, e) {
  if (!e || (typeof e.button === 'number' && e.button !== 0)) return;
  var split = tab.ex.ui.split, stacked = tab.host.classList.contains('stacked');
  var box = split && typeof split.getBoundingClientRect === 'function' ? split.getBoundingClientRect() : null;
  if (!box || !(stacked ? box.height : box.width)) return;
  if (e.preventDefault) e.preventDefault();
  rail.classList.add('drag');
  var ratio = lpRatio();
  var move = function (ev) {
    var pos = stacked ? (ev.clientY - box.top) / box.height : (ev.clientX - box.left) / box.width;
    if (!isFinite(pos)) return;
    ratio = Math.max(0.25, Math.min(0.75, pos));
    xpApplyRatio(tab, ratio);
  };
  var up = function () {
    rail.classList.remove('drag');
    if (typeof document.removeEventListener === 'function') {
      document.removeEventListener('pointermove', move);
      document.removeEventListener('pointerup', up);
    }
    lpSetPref('oblireach_split_ratio', String(Math.round(ratio * 1000) / 1000));
  };
  document.addEventListener('pointermove', move);
  document.addEventListener('pointerup', up);
}
// Narrow (under 760 px): the panes one above the other.
function xpLayout(tab) {
  tab = xvOwnerOf(tab);
  var host = tab.host;
  if (!tab.ex || !tab.ex.split) return;
  var w = Number(host.clientWidth) || 0;
  host.classList.toggle('stacked', w > 0 && w < 760);
}
// The tool bar's "Local pane" switch (kept on this computer).
function xpSetLocalShown(tab, on) {
  tab = xvOwnerOf(tab);
  lpSetPref('oblireach_files_split', on ? '1' : '0');
  tab.host.classList.toggle('no-local', !on);
  if (tab.ex.ui.splitToggle) {
    tab.ex.ui.splitToggle.classList.toggle('on', on);
    tab.ex.ui.splitToggle.setAttribute('aria-pressed', on ? 'true' : 'false');
  }
  if (on && tab.lp && !tab.lp.started) lpStart(tab);
  explorerUpdateBar(tab);
}
function xpLocalShown(tab) { tab = xvOwnerOf(tab); return !!(tab && tab.ex && tab.ex.split && tab.lp && !tab.host.classList.contains('no-local')); }
`
