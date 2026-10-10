package main

// usage_viewer.go — the disk usage analyzer of the file explorer
// (explorer_viewer.go), TreeSize-like: which folders hold the space of a
// drive, drilled down to the file, while the device's agent walks it. Plus
// the space of each drive (Windows) or file system (Unix) on the roots
// listing.
//
// The Obliance agent (usage_scan.go, 4.5.93+) walks the tree in the
// background; the commands travel like every explorer command
// (explorerCmd: FILE_EXPLORER_CMD on the device's server socket, its tenant,
// the step-up of browsing, the privacy code):
//   start_usage_scan {path, restart}  -> {scanId, root, reused} (a running
//       scan of the same folder is joined; restart: replaced);
//   get_usage_children {scanId, paths[], limit[, files, filesLimit]} ->
//       {state, progress, nodes: [{path, found, node, children[],
//       childrenTotal, filesGroup, files?, filesTotal?}]} (folders sorted by
//       size; files: the folder's loose files, read again from the disk);
//   get_usage_top_files {scanId} -> {files[]} (the 100 largest);
//   cancel_usage_scan {scanId}.
// While the scan runs, the folders on screen (the root and the expanded
// ones not complete yet: a complete folder never changes) are read again
// about every second, one command for them all, the next one only once the
// previous answer came (slower when it took long, or when the tab is not on
// screen); nothing is read any more once the scan is over. A deletion goes
// through the explorer's delete_file (its confirmation); the agent takes it
// out of its scans, the folders concerned are read again.
//
// The agent's data is untrusted text: never markup. Both consts are
// injected verbatim after the explorer's (buildUI, not through Sprintf: '%'
// is safe): no backtick here.

const usageCSS = `
/* ── Disk usage analyzer (usage_viewer.go) ── */
.us-panel{display:none;flex:1;min-height:0;flex-direction:column;background:var(--bg);color:var(--text);container-type:inline-size;container-name:usage}
.us-on>.exp-bar,.us-on>.exp-list,.us-on>.exp-vols{display:none}
.us-on>.us-panel{display:flex}
.us-bar{display:flex;align-items:center;gap:6px;padding:8px 12px;flex-shrink:0;flex-wrap:wrap;row-gap:6px;background:var(--bg2)}
.us-bar .btn-sm{display:inline-flex;align-items:center;gap:5px;padding:6px 9px;flex-shrink:0}
.us-bar .btn-sm:disabled{opacity:.35;cursor:default}
.us-bar .btn-sm:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.us-root{flex:1;min-width:120px;height:30px;display:flex;align-items:center;gap:8px;padding:0 11px;overflow:hidden;border-radius:10px;background:var(--input-bg);font-size:12px}
.us-root .t{flex-shrink:0;color:var(--muted);font-weight:600}
.us-root .p{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.us-prog{display:flex;align-items:center;gap:10px;min-width:0;padding:4px 14px 7px;flex-shrink:0;font-size:11.5px;color:var(--muted);background:var(--bg2)}
.us-state{flex-shrink:0;font-weight:600;color:var(--text)}
.us-state.running{color:var(--accent-l)}
.us-state.done{color:var(--success)}
.us-state.cancelled,.us-state.expired{color:var(--warn)}
.us-state.error{color:var(--danger)}
.us-count{flex-shrink:0;font-variant-numeric:tabular-nums}
.us-errs{flex-shrink:0;padding:1px 7px;border-radius:7px;background:rgb(var(--warn-rgb) / .14);color:var(--warn)}
.us-cur{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted2)}
.us-ind{position:relative;height:2px;flex-shrink:0;overflow:hidden;background:var(--bg2)}
.us-ind.on::after{content:'';position:absolute;top:0;bottom:0;left:-40%;width:40%;background:var(--accent);animation:exp-ind 1.1s ease-in-out infinite}
.us-tabs{display:flex;gap:2px;padding:0 10px;flex-shrink:0;background:var(--bg2)}
.us-tab{padding:8px 12px;border:none;background:none;color:var(--muted);font-size:12px;font-weight:600;cursor:pointer}
.us-tab:hover{color:var(--text)}
.us-tab.active{color:var(--accent-l);box-shadow:inset 0 -2px 0 var(--accent)}
.us-tab:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.us-list{flex:1;min-height:0;overflow:auto;position:relative;outline:none}
.us-list:focus-visible{box-shadow:inset 0 0 0 2px rgb(var(--accent-rgb) / .45)}
.us-table{table-layout:fixed}
.us-table th{position:sticky;top:0;z-index:1;cursor:pointer;user-select:none}
.us-table th:hover{color:var(--text)}
.us-table td{padding:5px 12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.us-table th.n,.us-table td.n{text-align:right;font-variant-numeric:tabular-nums}
.us-table td.n{color:var(--muted)}
.us-table td.n.sz{color:var(--text)}
.us-table .c-size,.us-table .c-alloc{width:96px}
.us-table .c-files,.us-table .c-folders{width:84px}
.us-table .c-pct{width:132px}
.us-table .c-mod{width:150px}
.us-table .c-dir{width:38%}
.us-table tbody tr{cursor:default}
.us-table tbody tr.sel td{background:rgb(var(--accent-rgb) / .14)}
.us-table tbody tr.sel:hover td{background:rgb(var(--accent-rgb) / .2)}
.us-table tbody tr.grp td{color:var(--muted)}
.us-table tbody tr.more td{color:var(--muted2);font-style:italic}
.us-name{display:flex;align-items:center;gap:6px;min-width:0}
.us-caret{flex-shrink:0;width:18px;height:18px;padding:0;display:inline-flex;align-items:center;justify-content:center;border:none;border-radius:5px;background:none;color:var(--muted);cursor:pointer}
.us-caret:hover{background:rgb(var(--ink) / .08);color:var(--text)}
.us-caret.leaf{visibility:hidden}
.us-caret svg{transition:transform .12s}
.us-caret.open svg{transform:rotate(90deg)}
.us-mini{flex-shrink:0;width:62px;font-size:10.5px;color:var(--muted);text-align:right;font-variant-numeric:tabular-nums}
.us-minibar{flex-shrink:0;width:38px;height:6px;overflow:hidden;border-radius:3px;background:rgb(var(--ink) / .08)}
.us-minibar i,.us-pct i,.exp-fill i{display:block;height:100%;border-radius:inherit;background:var(--accent)}
.us-ico{flex-shrink:0;display:inline-flex;color:var(--muted)}
.us-ico.dir{color:var(--accent-l)}
.us-nm{min-width:0;overflow:hidden;text-overflow:ellipsis}
.us-tag{flex-shrink:0;padding:0 6px;border-radius:6px;font-size:10.5px;background:rgb(var(--warn-rgb) / .14);color:var(--warn)}
.us-busy{flex-shrink:0;width:6px;height:6px;border-radius:50%;background:var(--accent);animation:us-pulse 1.2s ease-in-out infinite}
@keyframes us-pulse{0%,100%{opacity:.25}50%{opacity:.9}}
.us-pctc{display:flex;align-items:center;justify-content:flex-end;gap:6px}
.us-pct{flex:1;max-width:70px;height:6px;overflow:hidden;border-radius:3px;background:rgb(var(--ink) / .08)}
.us-pctc span{width:44px;text-align:right}
@container usage (max-width: 820px){.us-table .c-alloc,.us-table .c-folders{display:none}}
@container usage (max-width: 620px){.us-table .c-mod,.us-table .c-files,.us-mini{display:none}.us-table .c-pct{width:64px}.us-pct{display:none}}
@container usage (max-width: 440px){.us-table .c-pct,.us-cur{display:none}}
.exp-name .exp-dl{flex-shrink:1;min-width:0;color:var(--muted);overflow:hidden;text-overflow:ellipsis}
.exp-name .exp-dfs{flex-shrink:0;color:var(--muted2);font-size:11px}
.exp-fill{flex:1 1 80px;min-width:40px;max-width:360px;height:6px;overflow:hidden;border-radius:3px;background:rgb(var(--ink) / .08)}
.exp-fill.warn i{background:var(--warn)}
.exp-fill.danger i{background:var(--danger)}
.exp-dsp{flex-shrink:0;font-size:11.5px;color:var(--muted);font-variant-numeric:tabular-nums}
.exp-table td.c-size.exp-dpct{color:var(--text);font-weight:600}
.exp-table td.c-size.exp-dpct.warn{color:var(--warn)}
.exp-table td.c-size.exp-dpct.danger{color:var(--danger)}
.exp-go{display:inline-flex;align-items:center;gap:5px;padding:4px 10px;border:none;border-radius:9px;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);font-size:11.5px;font-weight:600;cursor:pointer}
.exp-go:hover{background:rgb(var(--accent-rgb) / .2)}
.exp-go:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.exp-go:disabled{opacity:.4;cursor:default}
.exp-vols{flex-shrink:0;display:flex;flex-wrap:wrap;gap:8px;padding:8px 12px;background:var(--bg)}
.exp-vol{flex:1 1 260px;min-width:0;max-width:540px;display:flex;flex-direction:column;gap:6px;padding:9px 12px;border-radius:12px;background:var(--bg2);box-shadow:var(--surface-shadow)}
.exp-vol-l1{display:flex;align-items:center;gap:8px;min-width:0;font-size:12px}
.exp-vol-l1 .p{min-width:0;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.exp-vol-l1 .f{flex-shrink:0;color:var(--muted2);font-size:11px}
.exp-vol-l1 .sp{flex:1}
.exp-vol .exp-fill{max-width:none;flex:0 0 6px}
.xp-pane .us-panel,.xp-pane .us-bar,.xp-pane .us-prog,.xp-pane .us-tabs,.xp-pane .us-ind,.xp-pane .exp-vols{background:transparent}
.xp-pane .us-bar{padding:6px 10px}
.xp-pane .us-table th{background:var(--bg2)}
.xp-pane .us-list{border-radius:0 0 14px 14px}
.xp-pane .exp-vol{background:var(--bg);box-shadow:none}
`

const usageJS = `
// ═════════════════════════════════════════════════════════════════════════
// Disk usage analyzer (usage_viewer.go), in the file explorer's tab
// ═════════════════════════════════════════════════════════════════════════
var USAGE_MIN_AGENT = '4.5.93';
var USAGE_POLL_MS = 1000;        // while the scan runs
var USAGE_POLL_MAX_MS = 5000;    // a slow answer: the next one later
var USAGE_POLL_HIDDEN_MS = 4000; // the tab not on screen
var USAGE_MAX_PATHS = 64;        // folders read in one command
var USAGE_CHILD_LIMIT = 300;     // subfolders of one folder
var USAGE_FILES_LIMIT = 500;     // files of one folder
var USAGE_MAX_ROWS = 5000;       // rows drawn at most
var USAGE_MAX_ERRORS = 5;        // reads failed in a row: the polling stops
var USAGE_ICON = {
  analyze: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.21 15.89A10 10 0 118 2.83"/><path d="M22 12A10 10 0 0012 2v10z"/></svg>',
  back: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/></svg>',
  stop: '<svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="none"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>',
  caret: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 6 15 12 9 18"/></svg>',
  files: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M15 2H8a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7z"/><path d="M4 7v13a2 2 0 002 2h10"/></svg>',
};
var USAGE_COLS = [
  ['name', 'usage.col.name', 'c-name', ''], ['size', 'usage.col.size', 'c-size', 'n'], ['allocated', 'usage.col.allocated', 'c-alloc', 'n'],
  ['files', 'usage.col.files', 'c-files', 'n'], ['folders', 'usage.col.folders', 'c-folders', 'n'], ['pct', 'usage.col.percent', 'c-pct', 'n'],
  ['modified', 'usage.col.modified', 'c-mod', ''],
];
var USAGE_TOP_COLS = [
  ['name', 'usage.col.name', 'c-name', ''], ['size', 'usage.col.size', 'c-size', 'n'], ['allocated', 'usage.col.allocated', 'c-alloc', 'n'],
  ['modified', 'usage.col.modified', 'c-mod', ''], ['dir', 'usage.col.folder', 'c-dir', ''],
];

// ── Pure pieces ──
// a vs b, dotted numbers ('4.5.93'): -1, 0, 1.
function usageVerCmp(a, b) {
  var x = String(a || '').split(/[.\-+ ]/), y = String(b || '').split(/[.\-+ ]/);
  for (var i = 0; i < 3; i++) {
    var p = parseInt(x[i], 10) || 0, q = parseInt(y[i], 10) || 0;
    if (p !== q) return p < q ? -1 : 1;
  }
  return 0;
}
// The analyzer on this device: its Obliance agent (never a local connection
// nor the legacy agent), recent enough when its version is known.
function usageOffered(dev) { return !!dev && !devIsLocal(dev) && !devLegacy(dev); }
function usageAgentOk(dev) {
  if (!usageOffered(dev)) return false;
  var v = dev.agent && typeof dev.agent.version === 'string' ? dev.agent.version : '';
  return !v || usageVerCmp(v, USAGE_MIN_AGENT) >= 0;
}
function usageNum(v) { var n = Number(v); return isFinite(n) && n > 0 ? n : 0; }
function usageStr(v, max) { return typeof v === 'string' ? v.slice(0, max || 4096) : ''; }
// One folder of the agent's answer (null: unusable).
function usageNodeOf(o) {
  if (!o || typeof o !== 'object' || typeof o.path !== 'string' || !o.path) return null;
  return { name: usageStr(o.name, 1024) || usageStr(o.path), path: usageStr(o.path), size: usageNum(o.size), allocated: usageNum(o.allocated),
    files: usageNum(o.files), folders: usageNum(o.folders), modified: usageStr(o.modified, 40), done: o.done === true, denied: o.denied === true,
    errors: usageNum(o.errors), truncated: o.truncated === true, childCount: usageNum(o.childCount) };
}
function usageFileOf(o) {
  if (!o || typeof o !== 'object' || typeof o.path !== 'string' || !o.path) return null;
  return { name: usageStr(o.name, 1024) || usageStr(o.path), path: usageStr(o.path), size: usageNum(o.size), allocated: usageNum(o.allocated),
    modified: usageStr(o.modified, 40), isDir: false };
}
function usageProgressOf(p) {
  p = p && typeof p === 'object' ? p : {};
  return { files: usageNum(p.files), folders: usageNum(p.folders), bytes: usageNum(p.bytes), allocated: usageNum(p.allocated),
    errors: usageNum(p.errors), skipped: usageNum(p.skipped), current: usageStr(p.current), elapsedMs: usageNum(p.elapsedMs) };
}
// 83000 ms -> '1:23', 3723000 -> '1:02:03'.
function usageFmtElapsed(ms) {
  var s = Math.floor(usageNum(ms) / 1000), h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, r = s % 60;
  var two = function (v) { return (v < 10 ? '0' : '') + v; };
  return h ? h + ':' + two(m) + ':' + two(r) : m + ':' + two(r);
}
function usagePctText(f) { return T('info.percent', { n: i18nNum(Math.round(Math.max(0, Math.min(1, f)) * 1000) / 10, f >= 0.1 || f === 0 ? 0 : 1) }); }
// The used share of a drive, its level ('', 'warn' from 85 %, 'danger' from 95 %).
function usageFillLevel(f) { return f >= 0.95 ? 'danger' : f >= 0.85 ? 'warn' : ''; }
// The folder of a path ('C:\a\b.txt' -> 'C:\a'; 'C:\b.txt' -> 'C:\'; '/a/b' -> '/a'; '/b' -> '/').
function usageDirOf(p, os) {
  p = String(p || '');
  var sep = os === 'windows' ? '\\' : '/';
  var i = p.lastIndexOf(sep);
  if (i < 0) return '';
  var d = p.slice(0, i);
  if (os === 'windows' && /^[A-Za-z]:$/.test(d)) return d + '\\';
  return d || '/';
}
// Siblings in the order asked: a key and a direction (1 / -1); ties by name.
function usageSortRows(items, key, dir) {
  var coll = null;
  try { coll = new Intl.Collator(typeof i18nLang === 'function' ? i18nLang() : undefined, { numeric: true, sensitivity: 'base' }); } catch (e) { coll = null; }
  var byText = function (a, b, k) { var x = String(a[k] || ''), y = String(b[k] || ''); return coll ? coll.compare(x, y) : (x.toLowerCase() < y.toLowerCase() ? -1 : x.toLowerCase() > y.toLowerCase() ? 1 : 0); };
  var byName = function (a, b) { return byText(a, b, 'name'); };
  var val = function (r) {
    if (key === 'modified') return Date.parse(r.modified) || 0;
    if (key === 'pct') return r.size;
    return usageNum(r[key]);
  };
  return items.slice().sort(function (a, b) {
    var c = key === 'name' || key === 'dir' ? byText(a, b, key) : (val(a) - val(b));
    if (!c) c = byName(a, b);
    return c * (dir === 1 ? 1 : -1);
  });
}

// ── State ──
function usageState(tab) {
  var ex = tab.ex;
  if (!ex.usage) {
    ex.usage = { on: false, root: '', scanId: '', state: '', error: '', fail: null, progress: usageProgressOf(null), sum: new Map(), nodes: new Map(),
      expanded: new Set(), groups: new Set(), files: new Map(), sortKey: 'size', sortDir: -1, view: 'tree', top: [], topWanted: false,
      sel: '', rows: [], rowEls: new Map(), timer: null, busy: false, again: false, seq: 0, gen: 0, errs: 0, rtt: 0, starting: false, notified: '' };
  }
  return ex.usage;
}
function usageClear(u) {
  u.scanId = ''; u.state = ''; u.error = ''; u.fail = null; u.progress = usageProgressOf(null);
  u.sum = new Map(); u.nodes = new Map(); u.expanded = new Set(); u.groups = new Set(); u.files = new Map(); u.top = []; u.topWanted = false;
  u.sel = ''; u.errs = 0; u.again = false; u.gen++;
  if (u.timer) { clearTimeout(u.timer); u.timer = null; }
}
// The folder the toolbar button analyzes: the one on screen, or the drive
// selected on the drives list ('' : none).
function usageTarget(tab) {
  var ex = tab.ex;
  if (!ex.listed || ex.fatal) return '';
  if (ex.os === 'windows' && ex.path === '') {
    var sel = explorerSelItems(tab);
    return sel.length === 1 ? sel[0].path : '';
  }
  return ex.path || (ex.os === 'windows' ? '' : '/');
}
function usageUpdateButton(tab) {
  var ex = tab.ex, b = ex.ui.usageBtn;
  if (!b) return;
  var dev = tab.device;
  b.style.display = usageOffered(dev) ? '' : 'none';
  if (ex.ui.usagePipe) ex.ui.usagePipe.style.display = b.style.display; // (its divider after Delete)
  var ok = usageAgentOk(dev), target = usageTarget(tab);
  b.disabled = !ok || !target;
  b.title = !ok ? T('usage.err.agentTooOld', { version: USAGE_MIN_AGENT }) : !target ? T('usage.pickDrive') : T('usage.analyze');
}

// ── Opening, closing ──
// The analyzer on path (restart: a new scan even when one runs there).
async function usageOpen(tab, path, restart) {
  var ex = tab.ex;
  if (!ex || ex.disposed || !path || !usageOffered(tab.device)) return;
  if (!usageAgentOk(tab.device)) { explorerSay(T('usage.err.agentTooOld', { version: USAGE_MIN_AGENT })); return; }
  explorerMenuClose();
  var u = usageState(tab);
  usageBuild(tab);
  var same = !restart && u.scanId && u.root === path && !u.fail;
  u.on = true;
  usageShow(tab, true);
  if (same) { usageRender(tab); usageSchedule(tab, 0); usageFocus(tab); return; }
  // Another folder: the scan running for the previous one stops (a Rescan
  // replaces it on the device by itself).
  if (u.scanId && u.state === 'running' && u.root !== path) explorerCmd(tab, 'cancel_usage_scan', { scanId: u.scanId }, EXPLORER_SHORT_MS).catch(function () {});
  usageClear(u);
  u.root = path; u.starting = true; u.state = 'starting';
  var gen = u.gen;
  usageRender(tab);
  usageFocus(tab);
  var r;
  try { r = await explorerCmd(tab, 'start_usage_scan', { path: path, restart: !!restart }, EXPLORER_SHORT_MS); }
  catch (e) {
    if (ex.disposed || gen !== u.gen || (e && e.code === 'cancelled')) return;
    u.starting = false; u.state = 'error'; u.fail = usageErrText(e);
    usageRender(tab);
    return;
  }
  if (ex.disposed || gen !== u.gen) return;
  u.starting = false;
  u.scanId = usageStr(r && r.scanId, 64);
  if (!u.scanId) { u.state = 'error'; u.fail = T('common.error'); usageRender(tab); return; }
  if (r && typeof r.root === 'string' && r.root) u.root = usageStr(r.root);
  u.state = 'running';
  u.expanded = new Set([u.root]);
  u.sel = u.root;
  if (r && r.reused === true) reachNotify(T('usage.joined'), 'ok');
  usageRender(tab);
  usageSchedule(tab, 0);
}
// Back to the files (the scan goes on on the device; opened again on the
// same folder, it is shown again).
function usageClose(tab) {
  var u = tab.ex && tab.ex.usage;
  if (!u || !u.on) return;
  u.on = false;
  if (u.timer) { clearTimeout(u.timer); u.timer = null; }
  usageShow(tab, false);
  explorerUpdateBar(tab);
  explorerFocus(tab);
}
function usageShow(tab, on) {
  var pane = tab.ex.ui.usPane;
  if (pane) pane.classList.toggle('us-on', !!on);
}
function usageFocus(tab) {
  var l = tab.ex.ui.us && tab.ex.ui.us.list;
  if (l && explorerShown(tab)) { try { l.focus(); } catch (e) {} }
}
function usageDispose(tab) {
  var u = tab.ex && tab.ex.usage;
  if (!u) return;
  u.on = false; u.gen++;
  if (u.timer) { clearTimeout(u.timer); u.timer = null; }
}
// In words: an explorer error, or the analyzer's own cases.
function usageErrText(e) {
  var msg = String((e && e.message) || '');
  if (/unknown command type/i.test(msg)) return T('usage.err.agentTooOld', { version: USAGE_MIN_AGENT });
  if (e && e.code === 'invalid' && /not allowed on the file explorer channel/i.test(msg)) return T('usage.err.serverTooOld');
  if (/usage scan not found/i.test(msg)) return T('usage.err.expired');
  if (e && (e.code || e.msgKey)) return explorerErrText(e);
  if (/^agent is not connected/i.test(msg) || privacyIsDenial(msg)) return explorerErrText(e);
  msg = msg.replace(EXPLORER_CMD_PREFIX_RE, '').slice(0, 400) || T('common.error');
  return T('usage.err.failed', { error: msg });
}

// ── Reading the scan ──
function usageSchedule(tab, ms) {
  var u = tab.ex.usage;
  if (!u || !u.on || tab.ex.disposed) return;
  if (u.timer) { clearTimeout(u.timer); u.timer = null; }
  if (u.busy) { u.again = true; return; }
  u.timer = setTimeout(function () { u.timer = null; usageTick(tab); }, Math.max(0, ms || 0));
}
// The folders to read now: the root, and the expanded ones on screen not
// read yet or not complete yet (a complete folder never changes).
function usageWanted(u) {
  var out = [];
  var want = function (p) {
    if (out.length >= USAGE_MAX_PATHS || out.indexOf(p) >= 0) return;
    var rec = u.nodes.get(p);
    if (!rec || !rec.node.done || rec.stale) out.push(p);
  };
  want(u.root);
  u.expanded.forEach(function (p) { if (usageVisible(u, p)) want(p); });
  return out;
}
// An expanded folder is on screen when all its ancestors are expanded.
function usageVisible(u, p) {
  if (p === u.root) return true;
  var x = u.sum.get(p);
  for (var i = 0; x && i < 512; i++) {
    if (!x.parent) return false;
    if (x.parent === u.root) return u.expanded.has(u.root);
    if (!u.expanded.has(x.parent)) return false;
    x = u.sum.get(x.parent);
  }
  return false;
}
async function usageTick(tab) {
  var ex = tab.ex, u = ex.usage;
  if (!u || !u.on || ex.disposed || !u.scanId || u.busy) return;
  var paths = usageWanted(u);
  var top = u.view === 'top' && (u.state === 'running' || !u.topWanted);
  var groups = [];
  u.groups.forEach(function (p) { if (!u.files.has(p) && u.nodes.has(p) && usageVisible(u, p)) groups.push(p); });
  if (!paths.length && !top && !groups.length) return;
  u.busy = true;
  var gen = u.gen, t0 = Date.now(), failed = null;
  try {
    if (paths.length) {
      var r = await explorerCmd(tab, 'get_usage_children', { scanId: u.scanId, paths: paths, limit: USAGE_CHILD_LIMIT }, EXPLORER_SHORT_MS);
      if (gen !== u.gen || ex.disposed) return;
      usageApply(u, r);
    }
    for (var i = 0; i < groups.length && gen === u.gen; i++) {
      var g = await explorerCmd(tab, 'get_usage_children', { scanId: u.scanId, path: groups[i], limit: 1, files: true, filesLimit: USAGE_FILES_LIMIT }, EXPLORER_SHORT_MS);
      if (gen !== u.gen || ex.disposed) return;
      usageApplyFiles(u, groups[i], g);
    }
    if (top && gen === u.gen) {
      var tf = await explorerCmd(tab, 'get_usage_top_files', { scanId: u.scanId }, EXPLORER_SHORT_MS);
      if (gen !== u.gen || ex.disposed) return;
      u.top = (tf && Array.isArray(tf.files) ? tf.files : []).map(usageFileOf).filter(Boolean).slice(0, 100);
      u.topWanted = true;
      if (tf && typeof tf.state === 'string') usageSetState(u, tf.state, tf.error, tf.progress);
    }
    u.errs = 0;
  } catch (e) {
    if (gen !== u.gen || ex.disposed || (e && e.code === 'cancelled')) return;
    failed = e;
  } finally {
    if (gen === u.gen) {
      u.busy = false;
      u.rtt = Date.now() - t0;
    }
  }
  if (failed) {
    var text = usageErrText(failed);
    if (/usage scan not found/i.test(String(failed.message || ''))) { u.state = 'expired'; u.fail = text; usageRender(tab); return; }
    u.errs++;
    if (u.notified !== text) { u.notified = text; explorerSay(text); }
    if (u.errs >= USAGE_MAX_ERRORS || EXPLORER_BATCH_STOP.indexOf(failed.code) >= 0) { u.fail = text; usageRender(tab); return; }
    usageSchedule(tab, Math.min(USAGE_POLL_MAX_MS, USAGE_POLL_MS * (1 + u.errs)));
    return;
  }
  u.notified = '';
  usageRender(tab);
  if (u.again) { u.again = false; usageSchedule(tab, 0); return; }
  if (u.state === 'running') usageSchedule(tab, usageNextDelay(tab));
  else if (usageWanted(u).length) usageSchedule(tab, 0); // (expanded meanwhile)
}
// The next read: about every second, later when the answer was slow or the
// tab is not on screen.
function usageNextDelay(tab) {
  var u = tab.ex.usage;
  var d = Math.min(USAGE_POLL_MAX_MS, Math.max(USAGE_POLL_MS, (u.rtt || 0) * 2));
  return explorerShown(tab) ? d : Math.max(d, USAGE_POLL_HIDDEN_MS); // (a view out of sight too: explorer_tabs.go)
}
function usageSetState(u, state, err, progress) {
  if (['running', 'done', 'cancelled', 'error'].indexOf(state) >= 0) u.state = state;
  u.error = usageStr(err, 400);
  if (progress) u.progress = usageProgressOf(progress);
}
// One get_usage_children answer: the folders read, their subfolders.
function usageApply(u, r) {
  if (!r || typeof r !== 'object') return;
  u.seq++;
  usageSetState(u, r.state, r.error, r.progress);
  (Array.isArray(r.nodes) ? r.nodes : []).forEach(function (e) {
    if (!e || typeof e !== 'object') return;
    var n = usageNodeOf(e.node);
    if (!n) {
      // Gone (deleted, or never a folder of this scan): out of the view.
      var p = usageStr(e.path);
      if (p && p !== u.root) { u.nodes.delete(p); u.expanded.delete(p); }
      return;
    }
    var prev = u.sum.get(n.path);
    u.sum.set(n.path, { node: n, seq: u.seq, parent: prev ? prev.parent : '' });
    var kids = [];
    (Array.isArray(e.children) ? e.children : []).slice(0, USAGE_CHILD_LIMIT).forEach(function (c) {
      var k = usageNodeOf(c);
      if (!k) return;
      u.sum.set(k.path, { node: k, seq: u.seq, parent: n.path });
      kids.push(k.path);
    });
    var g = e.filesGroup && typeof e.filesGroup === 'object' ? e.filesGroup : {};
    u.nodes.set(n.path, { node: n, kids: kids, total: Math.max(usageNum(e.childrenTotal), kids.length),
      group: { count: usageNum(g.count), size: usageNum(g.size), allocated: usageNum(g.allocated), modified: usageStr(g.modified, 40) }, stale: false });
    // Files listed while the scan goes on: read again with the folder.
    if (u.files.has(n.path) && !n.done) u.files.delete(n.path);
  });
}
function usageApplyFiles(u, path, r) {
  var e = r && Array.isArray(r.nodes) ? r.nodes[0] : null;
  if (r && typeof r === 'object') usageSetState(u, r.state, r.error, r.progress);
  if (!e || !e.node) { u.files.set(path, { list: [], total: 0, err: T('usage.err.expired') }); return; }
  var list = (Array.isArray(e.files) ? e.files : []).map(usageFileOf).filter(Boolean).slice(0, USAGE_FILES_LIMIT);
  u.files.set(path, { list: list, total: Math.max(usageNum(e.filesTotal), list.length), err: usageStr(e.filesError, 300) });
}

// ── Drawing ──
function usageBuild(tab) {
  var ex = tab.ex, ui = ex.ui;
  if (ui.us) return;
  var pane = ui.usPane, us = ui.us = {};
  var panel = us.panel = explorerEl('div', 'us-panel', pane, 'us-panel');
  var bar = explorerEl('div', 'us-bar', panel);
  us.back = explorerBtn(bar, 'us-back', USAGE_ICON.back, 'usage.back', function () { usageClose(tab); });
  us.back.innerHTML = USAGE_ICON.back + '<span>' + TH('usage.backShort') + '</span>';
  var root = us.root = explorerEl('div', 'us-root', bar, 'us-root');
  var rt = explorerEl('span', 't', root); rt.textContent = T('usage.title');
  us.rootPath = explorerEl('span', 'p', root, 'us-root-path');
  us.stop = explorerBtn(bar, 'us-stop', USAGE_ICON.stop, 'usage.stop', function () { usageStop(tab); });
  us.stop.innerHTML = USAGE_ICON.stop + '<span>' + TH('usage.stop') + '</span>';
  us.rescan = explorerBtn(bar, 'us-rescan', EXPLORER_ICON.refresh, 'usage.rescan', function () { var u = usageState(tab); if (u.root) usageOpen(tab, u.root, true); });
  us.rescan.innerHTML = EXPLORER_ICON.refresh + '<span>' + TH('usage.rescan') + '</span>';
  var prog = us.prog = explorerEl('div', 'us-prog', panel, 'us-prog');
  us.state = explorerEl('span', 'us-state', prog, 'us-state');
  us.count = explorerEl('span', 'us-count', prog, 'us-count');
  us.errs = explorerEl('span', 'us-errs', prog, 'us-errs');
  us.errs.title = T('usage.errorsHint');
  us.cur = explorerEl('span', 'us-cur', prog, 'us-cur');
  us.ind = explorerEl('div', 'us-ind', panel, 'us-ind');
  var tabs = explorerEl('div', 'us-tabs', panel);
  tabs.setAttribute('role', 'tablist');
  us.tabTree = explorerEl('button', 'us-tab', tabs, 'us-tab-tree'); us.tabTree.textContent = T('usage.tabTree');
  us.tabTop = explorerEl('button', 'us-tab', tabs, 'us-tab-top'); us.tabTop.textContent = T('usage.tabTop');
  [us.tabTree, us.tabTop].forEach(function (b) { b.setAttribute('role', 'tab'); });
  us.tabTree.addEventListener('click', function () { usageSetView(tab, 'tree'); });
  us.tabTop.addEventListener('click', function () { usageSetView(tab, 'top'); });
  var list = us.list = explorerEl('div', 'us-list', panel, 'us-list');
  list.tabIndex = 0; list.setAttribute('role', 'treegrid'); list.setAttribute('aria-label', T('usage.title'));
  var table = explorerEl('table', 'dt us-table', list);
  us.thead = explorerEl('thead', '', table);
  us.tbody = explorerEl('tbody', '', table);
  us.note = explorerEl('div', 'exp-note', list, 'us-note');
  list.addEventListener('keydown', function (e) { usageKey(tab, e); });
  list.addEventListener('contextmenu', function (e) { if (e && e.preventDefault) e.preventDefault(); });
}
function usageSetView(tab, v) {
  var u = usageState(tab);
  if (u.view === v) return;
  u.view = v; u.sel = '';
  if (v === 'top') { u.sortKey = 'size'; u.sortDir = -1; }
  usageRender(tab);
  if (v === 'top' && (!u.topWanted || u.state === 'running')) usageSchedule(tab, 0);
}
async function usageStop(tab) {
  var u = usageState(tab);
  if (!u.scanId || u.state !== 'running') return;
  var gen = u.gen;
  try { await explorerCmd(tab, 'cancel_usage_scan', { scanId: u.scanId }, EXPLORER_SHORT_MS); }
  catch (e) { if (!tab.ex.disposed && e.code !== 'cancelled') explorerSay(usageErrText(e)); return; }
  if (gen !== u.gen || tab.ex.disposed) return;
  u.state = 'cancelled';
  usageRender(tab);
  // (one last read: the sizes as they stand)
  u.nodes.forEach(function (rec) { rec.stale = true; });
  usageSchedule(tab, 0);
}
function usageStateText(u) {
  if (u.state === 'starting') return T('common.loading');
  if (u.state === 'running') return T('usage.state.running');
  if (u.state === 'done') return T('usage.state.done');
  if (u.state === 'cancelled') return T('usage.state.cancelled');
  if (u.state === 'expired') return T('usage.err.expired');
  if (u.state === 'error') return T('usage.state.error', { error: u.fail || u.error || T('common.error') });
  return '';
}
function usageRender(tab) {
  var ex = tab.ex, u = ex.usage, us = ex.ui.us;
  if (!u || !us) return;
  us.rootPath.textContent = u.root; us.root.title = u.root;
  var running = u.state === 'running' || u.state === 'starting';
  us.stop.style.display = running ? '' : 'none';
  us.stop.disabled = u.state !== 'running';
  us.rescan.style.display = running ? 'none' : '';
  us.state.className = 'us-state ' + (u.fail && u.state !== 'expired' ? 'error' : u.state);
  us.state.textContent = u.fail && u.state !== 'expired' && u.state !== 'error' ? u.fail : usageStateText(u);
  var p = u.progress;
  us.count.textContent = u.scanId ? T('usage.progress', { files: i18nNum(p.files, 0), folders: i18nNum(p.folders, 0), size: reachFmtBytes(p.bytes), time: usageFmtElapsed(p.elapsedMs) }) : '';
  us.errs.textContent = p.errors ? T('usage.errors', { n: i18nNum(p.errors, 0) }) : '';
  us.errs.style.display = p.errors ? '' : 'none';
  us.cur.textContent = u.state === 'running' && p.current ? p.current : '';
  us.cur.title = us.cur.textContent;
  us.ind.classList.toggle('on', running);
  us.tabTree.classList.toggle('active', u.view === 'tree'); us.tabTree.setAttribute('aria-selected', u.view === 'tree' ? 'true' : 'false');
  us.tabTop.classList.toggle('active', u.view === 'top'); us.tabTop.setAttribute('aria-selected', u.view === 'top' ? 'true' : 'false');
  usageRenderHead(tab);
  usageRenderRows(tab);
}
// Drawn again only when its columns or the sort change (not at every read
// of a running scan: hover and focus stay).
function usageRenderHead(tab) {
  var u = tab.ex.usage, us = tab.ex.ui.us;
  var sig = [u.view, u.sortKey, u.sortDir, T('usage.col.name')].join('|');
  if (us.thead._usageSig === sig && us.thead.children && us.thead.children.length) return;
  us.thead._usageSig = sig;
  us.thead.innerHTML = '';
  var tr = explorerEl('tr', '', us.thead);
  (u.view === 'top' ? USAGE_TOP_COLS : USAGE_COLS).forEach(function (c) {
    var th = explorerEl('th', c[2] + (c[3] ? ' ' + c[3] : ''), tr);
    th.dataset.sort = c[0];
    var on = u.sortKey === c[0];
    th.textContent = T(c[1]) + (on ? (u.sortDir === 1 ? ' \u2191' : ' \u2193') : '');
    th.setAttribute('aria-sort', on ? (u.sortDir === 1 ? 'ascending' : 'descending') : 'none');
    th.addEventListener('click', function () { usageSortBy(tab, c[0]); });
  });
}
function usageSortBy(tab, key) {
  var u = usageState(tab);
  if (u.sortKey === key) u.sortDir = -u.sortDir;
  else { u.sortKey = key; u.sortDir = key === 'name' || key === 'dir' ? 1 : -1; }
  usageRender(tab);
}
// The rows of the tree view, flattened: { kind: dir | group | file | more |
// loading, key, depth, ... }.
function usageTreeRows(u) {
  var rows = [];
  var top = u.sum.get(u.root);
  if (!top) return rows;
  var push = function (n, depth, parentSize) {
    if (rows.length >= USAGE_MAX_ROWS) return;
    rows.push({ kind: 'dir', key: n.path, path: n.path, n: n, depth: depth, pct: parentSize > 0 ? n.size / parentSize : (depth ? 0 : 1) });
    if (!u.expanded.has(n.path)) return;
    var rec = u.nodes.get(n.path);
    if (!rec) { rows.push({ kind: 'loading', key: 'l:' + n.path, depth: depth + 1 }); return; }
    var items = rec.kids.map(function (p) { var s = u.sum.get(p); return s ? s.node : null; }).filter(Boolean)
      .map(function (k) { return { kind: 'dir', n: k, name: k.name, size: k.size, allocated: k.allocated, files: k.files, folders: k.folders, modified: k.modified }; });
    if (rec.group.count > 0) {
      items.push({ kind: 'group', name: T('usage.filesGroup', { n: i18nNum(rec.group.count, 0) }), size: rec.group.size, allocated: rec.group.allocated,
        files: rec.group.count, folders: 0, modified: rec.group.modified, g: rec.group, dir: n.path });
    }
    usageSortRows(items, u.sortKey, u.sortDir).forEach(function (it) {
      if (rows.length >= USAGE_MAX_ROWS) return;
      if (it.kind === 'dir') { push(it.n, depth + 1, n.size); return; }
      var gk = 'g:' + it.dir;
      rows.push({ kind: 'group', key: gk, path: it.dir, g: it.g, name: it.name, depth: depth + 1, pct: n.size > 0 ? it.size / n.size : 0 });
      if (!u.groups.has(it.dir)) return;
      var fl = u.files.get(it.dir);
      if (!fl) { rows.push({ kind: 'loading', key: 'lf:' + it.dir, depth: depth + 2 }); return; }
      if (fl.err && !fl.list.length) { rows.push({ kind: 'more', key: 'fe:' + it.dir, depth: depth + 2, text: T('usage.err.failed', { error: fl.err }) }); return; }
      usageSortRows(fl.list.map(function (f) { return Object.assign({ files: 1, folders: 0 }, f); }), u.sortKey, u.sortDir).forEach(function (f) {
        if (rows.length < USAGE_MAX_ROWS) rows.push({ kind: 'file', key: f.path, path: f.path, f: f, depth: depth + 2, pct: n.size > 0 ? f.size / n.size : 0 });
      });
      if (fl.total > fl.list.length) rows.push({ kind: 'more', key: 'fm:' + it.dir, depth: depth + 2, text: T('usage.moreFiles', { n: i18nNum(fl.total - fl.list.length, 0) }) });
    });
    if (rec.total > rec.kids.length) rows.push({ kind: 'more', key: 'dm:' + n.path, depth: depth + 1, text: T('usage.moreFolders', { n: i18nNum(rec.total - rec.kids.length, 0) }) });
  };
  push(top.node, 0, 0);
  return rows;
}
function usageTopRows(u, os) {
  var list = u.top.map(function (f) { return Object.assign({ dir: usageDirOf(f.path, os) }, f); });
  return usageSortRows(list, u.sortKey, u.sortDir).map(function (f) {
    return { kind: 'file', key: f.path, path: f.path, f: f, depth: 0, top: true };
  });
}
function usageCaret(parent, open, leaf, fn) {
  var b = explorerEl('button', 'us-caret' + (open ? ' open' : '') + (leaf ? ' leaf' : ''), parent);
  b.innerHTML = USAGE_ICON.caret; b.tabIndex = -1;
  b.setAttribute('aria-label', T(open ? 'usage.collapse' : 'usage.expand'));
  if (!leaf) b.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); fn(); });
  return b;
}
function usageBar(parent, cls, f) {
  var b = explorerEl('span', cls, parent), i = explorerEl('i', '', b);
  i.style.width = (Math.max(0, Math.min(1, f)) * 100).toFixed(1) + '%';
  return b;
}
// What a row shows, as a string: a row whose signature did not change is
// kept as it is between two reads of a running scan (dom_patch.go domKeyed).
function usageRowSig(u, r, ncols) {
  var s = [u.view, ncols, r.kind, r.depth, r.top ? 1 : 0, T('common.loading')];
  if (r.kind === 'more' || r.kind === 'loading') { s.push(r.text || ''); return s.join('\u0001'); }
  var it = r.kind === 'dir' ? r.n : r.kind === 'group' ? r.g : r.f;
  s.push(it.size, it.allocated, it.files, it.folders, it.count, it.modified, (Number(r.pct) || 0).toFixed(4), r.path);
  if (r.kind === 'dir') {
    var rec = u.nodes.get(r.n.path);
    s.push(r.n.name, r.n.path, u.expanded.has(r.n.path), r.n.childCount, !!(rec && rec.group.count > 0), r.n.done, r.n.denied, r.n.truncated, u.state === 'running', T('usage.denied'));
  } else if (r.kind === 'group') s.push(r.name, u.groups.has(r.path));
  else s.push(r.f.name, r.f.dir);
  return s.join('\u0001');
}
function usageRenderRows(tab) {
  var ex = tab.ex, u = ex.usage, us = ex.ui.us;
  var rows = u.rows = u.view === 'top' ? usageTopRows(u, ex.os) : usageTreeRows(u);
  var ncols = (u.view === 'top' ? USAGE_TOP_COLS : USAGE_COLS).length;
  domKeyed(us.tbody, rows.map(function (r) {
    return { key: r.kind + ':' + r.key, sig: usageRowSig(u, r, ncols), data: r, build: function () { return usageRowEl(tab, r, ncols); } };
  }));
  // The selection: a class on the rows kept or built.
  u.rowEls = new Map();
  Array.prototype.forEach.call(us.tbody.children || [], function (tr) {
    var r = tr._domData;
    if (!r) return;
    u.rowEls.set(r.key, tr);
    domClass(tr, 'sel', u.sel === r.key);
  });
  var note = '';
  if (u.fail && !rows.length) note = '';
  else if (u.state === 'starting' || (u.scanId && !rows.length && u.view === 'tree')) note = T('common.loading');
  else if (u.view === 'top' && !rows.length) note = u.topWanted ? T('usage.noTop') : T('common.loading');
  domText(us.note, note);
  if (us.note.style.display !== (note ? '' : 'none')) us.note.style.display = note ? '' : 'none';
}
// One row (its listeners read the row it shows now: tr._domData).
function usageRowEl(tab, r, ncols) {
  var u = tab.ex.usage;
  var tr = explorerEl('tr', r.kind === 'group' ? 'grp' : r.kind === 'more' || r.kind === 'loading' ? 'more' : '', null);
  tr.dataset.key = r.key;
  tr._domData = r;
  var cur = function () { return tr._domData || r; };
  (function () {
    var td = explorerEl('td', 'c-name', tr);
    td.style.paddingLeft = (12 + r.depth * 16) + 'px';
    var nm = explorerEl('div', 'us-name', td);
    if (r.kind === 'more' || r.kind === 'loading') {
      td.colSpan = ncols;
      var t0 = explorerEl('span', 'us-nm', nm); t0.textContent = r.kind === 'loading' ? T('common.loading') : r.text;
      return;
    }
    var it = r.kind === 'dir' ? r.n : r.kind === 'group' ? { size: r.g.size, allocated: r.g.allocated, files: r.g.count, folders: 0, modified: r.g.modified } : r.f;
    if (!r.top) {
      if (r.kind === 'dir') {
        var rec = u.nodes.get(r.n.path);
        var leaf = r.n.childCount === 0 && !(rec && rec.group.count > 0) && !(r.n.files > 0) && r.n.done;
        usageCaret(nm, u.expanded.has(r.n.path), leaf, function () { usageToggle(tab, r.n.path); });
      } else if (r.kind === 'group') usageCaret(nm, u.groups.has(r.path), !(r.g.count > 0), function () { usageToggleGroup(tab, r.path); });
      else usageCaret(nm, false, true, null);
      var mini = explorerEl('span', 'us-mini', nm); mini.textContent = reachFmtBytes(it.size);
      usageBar(nm, 'us-minibar', r.pct);
    }
    var ic = explorerEl('span', 'us-ico' + (r.kind === 'dir' ? ' dir' : ''), nm);
    ic.innerHTML = r.kind === 'dir' ? (r.depth === 0 ? EXPLORER_ICON.drive : EXPLORER_ICON.folder) : r.kind === 'group' ? USAGE_ICON.files : EXPLORER_ICON.file;
    var sp = explorerEl('span', 'us-nm', nm);
    sp.textContent = r.kind === 'dir' ? (r.depth === 0 ? r.n.path : r.n.name) : r.kind === 'group' ? r.name : r.f.name;
    td.title = r.path;
    if (r.kind === 'dir') {
      if (r.n.denied) { var dn = explorerEl('span', 'us-tag', nm); dn.textContent = T('usage.denied'); }
      if (r.n.truncated) { var tc = explorerEl('span', 'us-tag', nm); tc.textContent = '+'; tc.title = T('usage.truncated'); }
      if (!r.n.done && u.state === 'running') { var bz = explorerEl('span', 'us-busy', nm); bz.title = T('usage.partial'); }
    }
    var num = function (cls, text) { var c = explorerEl('td', cls + ' n', tr); c.textContent = text; return c; };
    num('c-size sz', reachFmtBytes(it.size));
    num('c-alloc', reachFmtBytes(it.allocated));
    if (u.view === 'top') {
      var md0 = explorerEl('td', 'c-mod', tr); md0.textContent = explorerFmtDate(it.modified);
      var dd = explorerEl('td', 'c-dir', tr); dd.textContent = r.f.dir; dd.title = r.f.dir;
    } else {
      num('c-files', r.kind === 'file' ? '' : i18nNum(it.files, 0));
      num('c-folders', r.kind === 'dir' ? i18nNum(it.folders, 0) : '');
      var pc = explorerEl('td', 'c-pct n', tr), pw = explorerEl('div', 'us-pctc', pc);
      usageBar(pw, 'us-pct', r.pct);
      var pt = explorerEl('span', '', pw); pt.textContent = usagePctText(r.pct);
      var md = explorerEl('td', 'c-mod', tr); md.textContent = explorerFmtDate(it.modified);
    }
  })();
  if (r.kind === 'more' || r.kind === 'loading') return tr;
  tr.addEventListener('click', function () { usageSelect(tab, cur().key); });
  tr.addEventListener('dblclick', function () {
    var x = cur();
    if (x.kind === 'dir' && !x.top) usageToggle(tab, x.n.path);
    else if (x.kind === 'group') usageToggleGroup(tab, x.path);
    else if (x.kind === 'file') usageReveal(tab, x);
  });
  tr.addEventListener('contextmenu', function (e) {
    if (e && e.preventDefault) e.preventDefault();
    if (e && e.stopPropagation) e.stopPropagation();
    usageSelect(tab, cur().key);
    usageMenu(tab, e, cur());
  });
  return tr;
}
function usageSelect(tab, key) {
  var u = tab.ex.usage;
  u.sel = key;
  u.rowEls.forEach(function (tr, k) { tr.classList.toggle('sel', k === key); });
}
function usageToggle(tab, path) {
  var u = tab.ex.usage;
  if (u.expanded.has(path)) u.expanded.delete(path);
  else {
    u.expanded.add(path);
    var rec = u.nodes.get(path);
    if (!rec || !rec.node.done) usageSchedule(tab, 0);
  }
  usageRender(tab);
}
function usageToggleGroup(tab, dir) {
  var u = tab.ex.usage;
  if (u.groups.has(dir)) u.groups.delete(dir);
  else { u.groups.add(dir); if (!u.files.has(dir)) usageSchedule(tab, 0); }
  usageRender(tab);
}
// Keys of the list: arrows move, Right / Left open and close, Enter opens,
// Delete deletes, Escape goes back to the files.
function usageKey(tab, e) {
  if (!e) return;
  var u = tab.ex.usage, k = String(e.key || '');
  var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  var rows = u.rows.filter(function (r) { return r.kind === 'dir' || r.kind === 'group' || r.kind === 'file'; });
  var i = -1;
  rows.forEach(function (r, j) { if (r.key === u.sel) i = j; });
  var cur = i >= 0 ? rows[i] : null;
  if (k === 'Escape') { stop(); usageClose(tab); return; }
  if ((k === 'ArrowDown' || k === 'ArrowUp' || k === 'Home' || k === 'End') && rows.length) {
    stop();
    var j2 = k === 'Home' ? 0 : k === 'End' ? rows.length - 1 : k === 'ArrowDown' ? Math.min(rows.length - 1, i + 1) : Math.max(0, i - 1);
    usageSelect(tab, rows[j2].key);
    var tr = u.rowEls.get(rows[j2].key);
    if (tr && typeof tr.scrollIntoView === 'function') { try { tr.scrollIntoView({ block: 'nearest' }); } catch (x) {} }
    return;
  }
  if (!cur) return;
  if (k === 'ArrowRight' || k === 'ArrowLeft') {
    stop();
    var open = k === 'ArrowRight';
    if (cur.kind === 'dir' && !cur.top && u.expanded.has(cur.path) !== open) usageToggle(tab, cur.path);
    else if (cur.kind === 'group' && u.groups.has(cur.path) !== open) usageToggleGroup(tab, cur.path);
    return;
  }
  if (k === 'Enter') {
    stop();
    if (cur.kind === 'dir' && !cur.top) usageToggle(tab, cur.path);
    else if (cur.kind === 'group') usageToggleGroup(tab, cur.path);
    else usageReveal(tab, cur);
    return;
  }
  if (k === 'Delete' && (cur.kind === 'dir' || cur.kind === 'file')) { stop(); usageDelete(tab, cur); }
}

// ── Row actions ──
function usageItemOf(r) {
  if (r.kind === 'dir') return { name: r.n.name, path: r.n.path, isDir: true, size: r.n.size, modified: r.n.modified, mode: 'd' };
  return { name: r.f.name, path: r.f.path, isDir: false, size: r.f.size, modified: r.f.modified, mode: '-' };
}
function usageMenu(tab, e, r) {
  var ex = tab.ex, u = ex.usage, items = [];
  if (r.kind === 'group') {
    items.push({ icon: EXPLORER_ICON.open, key: 'usage.openInExplorer', run: function () { usageReveal(tab, { kind: 'dir', path: r.path, n: { path: r.path } }); } });
    items.push({ icon: EXPLORER_ICON.copy, key: 'explorer.copyPath', run: function () { explorerCopyPath(r.path); } });
  } else if (r.kind === 'dir' || r.kind === 'file') {
    var it = usageItemOf(r), isRoot = it.isDir && it.path === u.root;
    items.push({ icon: EXPLORER_ICON.open, key: 'usage.openInExplorer', run: function () { usageReveal(tab, r); } });
    if (it.isDir && !isRoot) items.push({ icon: USAGE_ICON.analyze, key: 'usage.analyzeFolder', run: function () { usageOpen(tab, it.path, false); } });
    var local = xpLocalShown(tab);
    items.push({ icon: EXPLORER_ICON.download, key: local ? 'local.downloadTo' : 'explorer.download',
      label: local ? T('local.downloadTo', { path: tab.lp.path || T('local.thisPc') }) : '',
      off: local ? !lpCanWrite(tab) : it.isDir, run: function () { usageDownload(tab, it); } });
    items.push({ icon: EXPLORER_ICON.copy, key: 'explorer.copyPath', run: function () { explorerCopyPath(it.path); } });
    items.push({ icon: EXPLORER_ICON.trash, key: 'explorer.delete', off: isRoot || !!ex.fatal, danger: true, run: function () { usageDelete(tab, r); } });
  }
  explorerMenuShow(tab, e, items, function () { usageFocus(tab); });
}
// "Open in the explorer": a folder listed, a file's folder with the file selected.
function usageReveal(tab, r) {
  var dir = r.kind === 'dir' ? (r.n ? r.n.path : r.path) : usageDirOf(r.path, tab.ex.os);
  var sel = r.kind === 'file' ? r.path : null;
  usageClose(tab);
  explorerList(tab, dir, sel ? { select: sel } : null);
}
function usageDownload(tab, it) {
  if (xpLocalShown(tab)) {
    if (!lpCanWrite(tab)) { explorerSay(T('local.pickLocalFolder')); return; }
    xferDownloadItems(tab, [it], tab.lp.path);
    return;
  }
  if (it.isDir) { explorerSay(T('explorer.noFolderDownload')); return; }
  explorerDownload(tab, [it]);
}
// Delete: the explorer's confirmation and command; the folders concerned
// are read again (the agent took it out of its scans).
async function usageDelete(tab, r) {
  var ex = tab.ex, u = ex.usage, it = usageItemOf(r);
  if (ex.fatal || (it.isDir && it.path === u.root)) return;
  var ok = await explorerDialog({ tab: tab, title: T('explorer.delete'), ok: 'explorer.delete', danger: true, icon: EXPLORER_ICON.trash,
    text: it.isDir ? T('explorer.deleteFolderConfirm', { name: it.name }) : T('explorer.deleteConfirm', { name: it.name }) });
  if (ok !== true || ex.disposed) return;
  try { await explorerCmd(tab, 'delete_file', { path: it.path, recursive: !!it.isDir }, EXPLORER_SHORT_MS); }
  catch (e) { if (!ex.disposed && e.code !== 'cancelled') explorerSay(explorerErrText(e)); return; }
  if (ex.disposed) return;
  reachNotify(T('explorer.deleted', { name: it.name }), 'ok');
  // Its folder and every folder above: read again; the item: out of the view.
  var dir = it.isDir ? ((u.sum.get(it.path) || {}).parent || usageDirOf(it.path, ex.os)) : usageDirOf(it.path, ex.os);
  for (var p = dir, i = 0; p && i < 512; i++) {
    var rec = u.nodes.get(p);
    if (rec) rec.stale = true;
    var s = u.sum.get(p);
    p = s ? s.parent : '';
  }
  if (u.nodes.get(u.root)) u.nodes.get(u.root).stale = true;
  if (it.isDir) {
    u.nodes.delete(it.path); u.expanded.delete(it.path); u.sum.delete(it.path);
    var rp = u.nodes.get(dir); if (rp) rp.kids = rp.kids.filter(function (k) { return k !== it.path; });
  } else u.files.delete(dir);
  u.top = u.top.filter(function (f) { return f.path !== it.path && !(it.isDir && f.path.indexOf(it.path + (ex.os === 'windows' ? '\\' : '/')) === 0); });
  if (u.view === 'top') u.topWanted = false;
  if (u.sel === it.path) u.sel = '';
  usageRender(tab);
  usageSchedule(tab, 0);
}

// ── The roots: each drive's space (Windows), the file systems (Unix) ──
// A drive row of the roots listing: its label, its fill, its space, the
// analyzer's button (explorerRenderList).
function usageDriveRow(tab, it, tr, nm, sizeTd, modTd) {
  tr.classList.add('drive');
  if (it.label) { var lb = explorerEl('span', 'exp-dl', nm); lb.textContent = it.label; }
  if (it.fsType) { var fs = explorerEl('span', 'exp-dfs', nm); fs.textContent = it.fsType; }
  if (it.totalBytes > 0) {
    var used = Math.max(0, Math.min(1, 1 - it.freeBytes / it.totalBytes)), lv = usageFillLevel(used);
    usageBar(nm, 'exp-fill' + (lv ? ' ' + lv : ''), used);
    var sp = explorerEl('span', 'exp-dsp', nm);
    sp.textContent = T('usage.drive.free', { free: reachFmtBytes(it.freeBytes), total: reachFmtBytes(it.totalBytes) });
    sizeTd.textContent = usagePctText(used);
    sizeTd.className = 'c-size exp-dpct' + (lv ? ' ' + lv : '');
    sizeTd.title = T('usage.drive.used', { pct: usagePctText(used) });
  }
  if (usageOffered(tab.device)) usageGoBtn(tab, modTd, it.path);
}
function usageGoBtn(tab, parent, path) {
  var b = explorerEl('button', 'exp-go', parent);
  b.innerHTML = USAGE_ICON.analyze + '<span>' + TH('usage.analyzeShort') + '</span>';
  var ok = usageAgentOk(tab.device);
  b.disabled = !ok;
  b.title = ok ? T('usage.analyzeOn', { path: path }) : T('usage.err.agentTooOld', { version: USAGE_MIN_AGENT });
  b.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); usageOpen(tab, path, false); });
  b.addEventListener('dblclick', function (e) { if (e && e.stopPropagation) e.stopPropagation(); });
  return b;
}
// A Unix device's file systems (listRoots 'volumes'), above the listing of '/'.
function usageVolumesOf(res) {
  var list = res && !Array.isArray(res) && Array.isArray(res.volumes) ? res.volumes : [];
  var out = [];
  for (var i = 0; i < list.length && out.length < 64; i++) {
    var v = list[i];
    if (!v || typeof v !== 'object' || typeof v.path !== 'string' || v.path.charAt(0) !== '/') continue;
    var t = usageNum(v.totalBytes);
    if (!t) continue;
    out.push({ path: v.path.slice(0, 4096), totalBytes: t, freeBytes: Math.min(t, usageNum(v.freeBytes)), fsType: usageStr(v.fsType, 40) });
  }
  return out;
}
function usageRenderVolumes(tab) {
  var ex = tab.ex, el = ex.ui.vols;
  if (!el) return;
  el.innerHTML = '';
  var vols = ex.path === '/' && Array.isArray(ex.volumes) ? ex.volumes : [];
  el.style.display = vols.length ? '' : 'none';
  vols.forEach(function (v) {
    var box = explorerEl('div', 'exp-vol', el);
    box.dataset.path = v.path;
    var l1 = explorerEl('div', 'exp-vol-l1', box);
    var p = explorerEl('span', 'p', l1); p.textContent = v.path; p.title = v.path;
    if (v.fsType) { var f = explorerEl('span', 'f', l1); f.textContent = v.fsType; }
    explorerEl('span', 'sp', l1);
    var used = 1 - v.freeBytes / v.totalBytes, lv = usageFillLevel(used);
    var pc = explorerEl('span', 'exp-dsp', l1); pc.textContent = usagePctText(used);
    if (usageOffered(tab.device)) usageGoBtn(tab, l1, v.path);
    usageBar(box, 'exp-fill' + (lv ? ' ' + lv : ''), used);
    var sp = explorerEl('span', 'exp-dsp', box);
    sp.textContent = T('usage.drive.free', { free: reachFmtBytes(v.freeBytes), total: reachFmtBytes(v.totalBytes) });
  });
}
`
