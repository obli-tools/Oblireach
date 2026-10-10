package main

// explorer_viewer.go — the file explorer of a device, in a tool tab (the
// tabs themselves: terminal_viewer.go; the gates that open it, privacy
// included: agent_tools.go toolOpen). The main window shows its explorers in
// its Files page (files_page.go), a session window as tabs of its own.
//
// The web's path (FileExplorerTab), nothing else:
//   - POST /api/files/:id/files/open-explorer once per tab (the audit
//     file_explorer.open), pinned like every device action (multi-server
//     mode: the device's tenant); a 403 ("File access not permitted for
//     your team") stops it there;
//   - every operation is FILE_EXPLORER_CMD {requestId, deviceId,
//     commandType, payload} on the socket.io socket of the DEVICE's server
//     (chatSocketFor), answered by FILE_EXPLORER_RESULT {id, commandType,
//     status, result} on that socket only. The server authorizes each
//     command per device (its tenant, the team's 'files' capability, the
//     tenant's restriction command.<type>), audits the mutations and adds
//     the privacy unlock itself. A refusal comes back as a failure
//     {error, code[, action]}:
//       step_up_required -> POST /api/files/:id/files/step-up {commandType}
//         (pinned; the app's usual 2FA prompt answers its 401, like every
//         sensitive action), then the command is sent again ONCE;
//       restricted / forbidden / not_found / session / invalid -> said;
//     the agent's own failures carry its text ({error}).
//   - The agent refused for privacy (its unlock gone: a restart, the time
//     over): the code is asked again (privacyRetryGate) and the command sent
//     again ONCE.
//   - Commands (Obliance agent files.go): list_directory {path} ('' = the
//     roots: the drives on Windows, '/' elsewhere), create_directory {path},
//     rename_file {oldPath, newPath}, delete_file {path, recursive} (a folder
//     with its contents only after a confirmation that says so),
//     download_file {path} -> {data: base64}, upload_file {path, data,
//     overwrite} ("already exists" -> asked, then overwrite: true). The
//     agent refuses any path holding "..".
//   - Files travel whole, as base64, through socket.io and the agent's
//     channel (150 MB each): 100 MB at most here, checked before reading or
//     asking (a larger download would close the agent's channel).
//   - A socket renewal (a tenant switch) waits while a command is on its
//     way (ui.go chatRenewSocketWhenIdle): its answer goes to the socket that
//     asked; a disconnection fails what was pending on that socket.
//
// Both consts are injected verbatim after the terminals (buildUI, not
// through Sprintf: '%' is safe): no backtick here.

const explorerCSS = `
/* ── File explorer (explorer_viewer.go) ── */
.tool-host.exp-host{display:flex;flex-direction:column;background:var(--bg);color:var(--text)}
.exp-bar{display:flex;align-items:center;gap:6px;padding:8px 12px;flex-shrink:0;flex-wrap:wrap;row-gap:6px;background:var(--bg2)}
.exp-bar .btn-sm{display:inline-flex;align-items:center;gap:5px;padding:6px 9px;flex-shrink:0}
.exp-bar .btn-sm:disabled{opacity:.35;cursor:default}
.exp-bar .btn-sm:disabled:hover{background:rgb(var(--ink) / .06);color:var(--muted)}
.exp-bar .btn-sm:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.exp-grp{display:inline-flex;align-items:center;gap:6px;flex-shrink:0}
.exp-pipe{width:1px;height:18px;margin:0 3px;flex-shrink:0;border-radius:1px;background:rgb(var(--ink) / .16)}
.exp-crumbs{flex:1;min-width:140px;height:30px;display:flex;align-items:center;gap:2px;overflow:hidden;padding:0 6px;border-radius:10px;background:var(--input-bg);cursor:text}
.exp-crumb{flex-shrink:0;max-width:220px;padding:3px 6px;border:none;border-radius:7px;background:none;color:var(--muted);font-size:12px;cursor:pointer;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.exp-crumb:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.exp-crumb.cur{color:var(--text);font-weight:600}
.exp-crumb-sep{flex-shrink:0;color:var(--muted2);font-size:11px}
.exp-path,.exp-filter{height:30px;padding:0 11px;border:none;border-radius:10px;background:var(--input-bg);color:var(--text);font-size:12px;outline:none}
.exp-path{flex:1;min-width:140px;display:none}
.exp-host.editing>.exp-bar .exp-path{display:block}
.exp-host.editing>.exp-bar .exp-crumbs{display:none}
.exp-filter{width:150px}
.exp-path:focus,.exp-filter:focus{box-shadow:0 0 0 2px var(--accent)}
.exp-list{flex:1;min-height:0;overflow:auto;position:relative;outline:none}
.exp-list:focus-visible{box-shadow:inset 0 0 0 2px rgb(var(--accent-rgb) / .45)}
.exp-table{table-layout:fixed}
.exp-table th{position:sticky;top:0;z-index:1;cursor:pointer;user-select:none}
.exp-table th:hover{color:var(--text)}
.exp-table th.c-size{width:110px;text-align:right}
.exp-table th.c-mod{width:190px}
.exp-table td{padding:6px 14px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.exp-table td.c-size{text-align:right;color:var(--muted);font-variant-numeric:tabular-nums}
.exp-table td.c-mod{color:var(--muted)}
.exp-list.loading .exp-table tbody{opacity:.55}
.exp-table tbody tr.sel td{background:rgb(var(--accent-rgb) / .14)}
.exp-table tbody tr.sel:hover td{background:rgb(var(--accent-rgb) / .2)}
.exp-name{display:flex;align-items:center;gap:8px;min-width:0}
.exp-name .nm{overflow:hidden;text-overflow:ellipsis}
.exp-ico{flex-shrink:0;display:inline-flex;color:var(--muted)}
.exp-ico.dir{color:var(--accent-l)}
.exp-note{padding:28px 16px;text-align:center;color:var(--muted2);font-size:12px}
.exp-note.err{color:var(--danger)}
.exp-drop{position:absolute;top:8px;right:8px;bottom:8px;left:8px;z-index:3;display:none;align-items:center;justify-content:center;padding:20px;text-align:center;border-radius:14px;background:rgb(var(--accent-rgb) / .1);color:var(--accent-l);font-size:13px;font-weight:600;pointer-events:none}
.exp-drop.visible{display:flex}
.exp-tr{flex-shrink:0;max-height:150px;overflow:auto;padding:6px 12px;background:var(--bg2)}
.exp-tr-head{display:flex;align-items:center;gap:8px;padding:2px 0 4px;font-size:11px;font-weight:600;color:var(--muted)}
.exp-tr-head .sp{flex:1}
.exp-tr-x{width:22px;height:22px;border:none;border-radius:6px;background:none;color:var(--muted2);cursor:pointer}
.exp-tr-x:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.exp-tr-row{display:flex;align-items:center;gap:8px;padding:3px 0;font-size:11.5px;color:var(--text)}
.exp-tr-dir{flex-shrink:0;width:12px;color:var(--muted)}
.exp-tr-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.exp-tr-state{flex-shrink:0;max-width:55%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted)}
.exp-tr-row.done .exp-tr-state{color:var(--success)}
.exp-tr-row.error .exp-tr-state{color:var(--danger)}
.exp-tr-bar{position:relative;flex-shrink:0;width:70px;height:4px;overflow:hidden;border-radius:2px;background:rgb(var(--ink) / .08)}
.exp-tr-bar::after{content:'';position:absolute;top:0;bottom:0;left:-40%;width:40%;border-radius:2px;background:var(--accent);animation:exp-ind 1.1s ease-in-out infinite}
@keyframes exp-ind{0%{left:-40%}100%{left:100%}}
.exp-menu{position:fixed;z-index:400;min-width:180px;padding:5px;display:flex;flex-direction:column;border-radius:12px;background:var(--bg2);box-shadow:0 10px 30px var(--shadow)}
.exp-menu button{display:flex;align-items:center;gap:8px;padding:7px 10px;border:none;border-radius:8px;background:none;color:var(--text);font-size:12px;text-align:left;cursor:pointer}
.exp-menu button:hover,.exp-menu button:focus-visible{background:rgb(var(--ink) / .06);outline:none}
.exp-menu button:disabled{opacity:.4;cursor:default}
.exp-menu button.danger{color:var(--danger)}
.modal-overlay.exp-dlg{z-index:340}
.exp-dlg .modal-box p.desc{word-break:break-word}
.exp-dlg-input{width:100%;padding:11px 15px;border:none;border-radius:12px;background:var(--input-bg);color:var(--text);font-size:13px;outline:none}
.exp-dlg-input:focus{box-shadow:0 0 0 2px var(--accent)}
.exp-btn-danger{padding:8px 18px;border:none;border-radius:10px;background:rgb(var(--danger-rgb) / .16);color:var(--danger);font-size:12px;font-weight:600;cursor:pointer}
.exp-btn-danger:hover{background:rgb(var(--danger-rgb) / .24)}
` + explorerTabsCSS // (its sub-tabs and their mosaic: explorer_tabs.go)

const explorerJS = `
// ═════════════════════════════════════════════════════════════════════════
// File explorer (explorer_viewer.go): TOOL_IMPL.files
// ═════════════════════════════════════════════════════════════════════════
var EXPLORER_MAX_BYTES = 100 * 1024 * 1024; // uploads and downloads (base64 through socket.io and the agent: 150 MB)
var EXPLORER_SHORT_MS = 30000;   // list, create, rename, delete
var EXPLORER_LONG_MS = 180000;   // download, upload
var EXPLORER_CONNECT_MS = 10000; // the device's server socket, connected
var EXPLORER_MAX_ITEMS = 20000;  // rows drawn at most
var EXPLORER_MAX_TRANSFERS = 30;
// Refusals that hold for every file of a batch: said once, the batch stops.
var EXPLORER_BATCH_STOP = ['restricted', 'forbidden', 'not_found', 'session', 'invalid', 'step_up_required', 'step_up_cancelled',
  'step_up_failed', 'not_connected', 'connection', 'timeout', 'privacy', 'cancelled'];
var EXPLORER_CMD_PREFIX_RE = /^(list_directory|create_directory|rename_file|delete_file|download_file|upload_file|start_usage_scan|get_usage_children|get_usage_top_files|cancel_usage_scan):\s*/;

var EXPLORER_ICON = {
  up: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/></svg>',
  refresh: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 11-2.12-9.36L23 10"/></svg>',
  newFolder: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/><line x1="12" y1="11" x2="12" y2="17"/><line x1="9" y1="14" x2="15" y2="14"/></svg>',
  upload: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>',
  download: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>',
  rename: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 013 3L7 19l-4 1 1-4z"/></svg>',
  trash: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 01-2 2H8a2 2 0 01-2-2L5 6"/><path d="M10 11v6M14 11v6"/><path d="M9 6V4a1 1 0 011-1h4a1 1 0 011 1v2"/></svg>',
  copy: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1"/></svg>',
  open: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/></svg>',
  folder: '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" stroke="none"><path d="M10 4H4a2 2 0 00-2 2v12a2 2 0 002 2h16a2 2 0 002-2V8a2 2 0 00-2-2h-8l-2-2z"/></svg>',
  file: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>',
  drive: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="13" width="20" height="8" rx="2"/><path d="M5.5 13L8 4h8l2.5 9"/><line x1="6" y1="17" x2="6.01" y2="17"/></svg>',
  paste: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 4h2a2 2 0 012 2v14a2 2 0 01-2 2H6a2 2 0 01-2-2V6a2 2 0 012-2h2"/><rect x="8" y="2" width="8" height="4" rx="1"/></svg>',
};

// ── Paths (the device's OS: 'windows' or not) ──
function explorerOs(dev) { return devOsKind(dev) === 'windows' ? 'windows' : 'unix'; }
function explorerSep(os) { return os === 'windows' ? '\\' : '/'; }
// The top of the tree: the drives (Windows), '/' (the others: the agent lists '/' for '').
function explorerIsTop(path, os) { path = String(path || ''); return os === 'windows' ? path === '' : (path === '' || path === '/'); }
function explorerJoin(dir, name, os) {
  var sep = explorerSep(os);
  dir = String(dir || '');
  if (!dir) return os === 'windows' ? String(name) : '/' + name;
  return dir.slice(-1) === sep ? dir + name : dir + sep + name;
}
// One level up: 'C:\a\b' -> 'C:\a' -> 'C:\' -> '' (the drives); '/a/b' -> '/a' -> '/' -> ''.
function explorerParent(path, os) {
  path = String(path || '');
  if (os === 'windows') {
    if (!path || /^[A-Za-z]:\\?$/.test(path)) return '';
    var p = path.replace(/\\+$/, '');
    var i = p.lastIndexOf('\\');
    if (i < 0) return '';
    var up = p.slice(0, i);
    if (/^[A-Za-z]:$/.test(up)) return up + '\\';
    if (/^\\*$/.test(up)) return '';
    return up;
  }
  if (!path || path === '/') return '';
  var q = path.replace(/\/+$/, '');
  var j = q.lastIndexOf('/');
  return j <= 0 ? '/' : q.slice(0, j);
}
// The breadcrumb: [{ label, path }], the top first.
function explorerCrumbs(path, os) {
  path = String(path || '');
  var out = [];
  if (os === 'windows') {
    out.push({ label: T('explorer.roots'), path: '' });
    if (!path) return out;
    var m = /^([A-Za-z]:)(\\(.*))?$/.exec(path);
    if (!m) { out.push({ label: path, path: path }); return out; }
    var acc = m[1] + '\\';
    out.push({ label: m[1], path: acc });
    String(m[3] || '').split('\\').filter(Boolean).forEach(function (seg) {
      acc = acc.slice(-1) === '\\' ? acc + seg : acc + '\\' + seg;
      out.push({ label: seg, path: acc });
    });
    return out;
  }
  out.push({ label: '/', path: '/' });
  var acc2 = '';
  path.split('/').filter(Boolean).forEach(function (seg) { acc2 += '/' + seg; out.push({ label: seg, path: acc2 }); });
  return out;
}
// A path typed in the path field: absolute, no "..", the OS's separators.
// null: refused.
function explorerNormPath(v, os) {
  var s = String(v === undefined || v === null ? '' : v).trim();
  if (!s) return os === 'windows' ? '' : '/';
  if (s.length > 4096 || /[\u0000-\u001f]/.test(s) || s.indexOf('..') >= 0) return null;
  if (os === 'windows') {
    s = s.replace(/\//g, '\\');
    if (/^[A-Za-z]:$/.test(s)) s += '\\';
    if (!/^[A-Za-z]:\\/.test(s) && !/^\\\\[^\\]/.test(s)) return null;
    if (s.length > 3) s = s.replace(/\\+$/, '');
    return s;
  }
  if (s.charAt(0) !== '/') return null;
  s = s.replace(/\/{2,}/g, '/');
  if (s.length > 1) s = s.replace(/\/+$/, '');
  return s;
}
// A name typed (new folder, rename) or of a file sent: one path element.
function explorerValidName(n, os) {
  n = String(n === undefined || n === null ? '' : n).trim();
  if (!n || n.length > 255 || n === '.' || n.indexOf('..') >= 0) return false;
  if (/[\/\\\u0000-\u001f]/.test(n)) return false;
  if (os === 'windows' && /[:*?"<>|]/.test(n)) return false;
  return true;
}
// A listing's entries (the agent's; untrusted text: never markup).
function explorerItems(res, dir, os) {
  var list = Array.isArray(res) ? res : (res && Array.isArray(res.files) ? res.files : []);
  var out = [];
  for (var i = 0; i < list.length && out.length < EXPLORER_MAX_ITEMS; i++) {
    var f = list[i];
    if (!f || typeof f !== 'object' || typeof f.name !== 'string' || !f.name) continue;
    var size = Number(f.size);
    out.push({
      name: f.name.slice(0, 1024),
      path: (typeof f.path === 'string' && f.path) ? f.path.slice(0, 4096) : explorerJoin(dir, f.name, os),
      isDir: f.isDir === true,
      size: isFinite(size) && size > 0 ? size : 0,
      modified: typeof f.modified === 'string' ? f.modified : '',
      mode: typeof f.mode === 'string' ? f.mode : '',
    });
    // A drive of the roots (agents 4.5.93+): its space, label, file system.
    var tb = Number(f.totalBytes), fb = Number(f.freeBytes), last = out[out.length - 1];
    if (isFinite(tb) && tb > 0 && isFinite(fb) && fb >= 0) { last.totalBytes = tb; last.freeBytes = Math.min(fb, tb); }
    if (typeof f.label === 'string' && f.label) last.label = f.label.slice(0, 128);
    if (typeof f.fsType === 'string' && f.fsType) last.fsType = f.fsType.slice(0, 32);
  }
  return out;
}
// Folders first; then by name (natural order), size or date.
function explorerSort(items, key, dir) {
  var coll = null;
  try { coll = new Intl.Collator(typeof i18nLang === 'function' ? i18nLang() : undefined, { numeric: true, sensitivity: 'base' }); } catch (e) { coll = null; }
  var byName = function (a, b) {
    if (coll) return coll.compare(a.name, b.name);
    var x = a.name.toLowerCase(), y = b.name.toLowerCase();
    return x < y ? -1 : x > y ? 1 : 0;
  };
  dir = dir === -1 ? -1 : 1;
  return items.slice().sort(function (a, b) {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    var c = 0;
    if (key === 'size') c = a.size - b.size;
    else if (key === 'modified') c = (Date.parse(a.modified) || 0) - (Date.parse(b.modified) || 0);
    if (!c) c = byName(a, b);
    return c * dir;
  });
}
function explorerFmtDate(v) {
  if (!v) return '';
  var t = Date.parse(v);
  if (!isFinite(t)) return '';
  try { return typeof fmtDateTime === 'function' ? fmtDateTime(t) : new Date(t).toLocaleString(); } catch (e) { return ''; }
}
// Base64 of a local file (a File of the file picker or a drop).
function explorerBytesToB64(u8) {
  var s = '', CH = 0x8000;
  for (var i = 0; i < u8.length; i += CH) s += String.fromCharCode.apply(null, u8.subarray(i, i + CH));
  return btoa(s);
}
function explorerReadB64(file) {
  if (file && typeof file.readB64 === 'function') return explorerClipB64(file); // (a pasted file: explorerClipFile)
  if (typeof FileReader === 'function') {
    return new Promise(function (resolve, reject) {
      var r = new FileReader();
      r.onload = function () { var s = String(r.result || ''); var i = s.indexOf(','); resolve(i >= 0 ? s.slice(i + 1) : ''); };
      r.onerror = function () { reject(new Error('read')); };
      r.readAsDataURL(file);
    });
  }
  return Promise.resolve(file.arrayBuffer()).then(function (b) { return explorerBytesToB64(new Uint8Array(b)); });
}
function explorerReqId() {
  try { if (typeof crypto !== 'undefined' && crypto && typeof crypto.randomUUID === 'function') return crypto.randomUUID(); } catch (e) {}
  var s = '';
  for (var i = 0; i < 32; i++) s += Math.floor(Math.random() * 16).toString(16);
  return s;
}

// ── Errors ──
// An explorer error: its text (a server or agent sentence, or a catalog key
// { k, p }), its code (the server's refusal, or ours).
function explorerErr(x, code, action) {
  var e = new Error(typeof x === 'string' ? x : toolTxt(x));
  if (x && typeof x === 'object') e.msgKey = x;
  e.code = code || '';
  if (action) e.action = action;
  return e;
}
// In words. ctx 'list': a listing (the agent's words quoted as such).
function explorerErrText(e, ctx) {
  if (e && e.msgKey) return toolTxt(e.msgKey);
  switch (e && e.code) {
    case 'restricted': return T('explorer.err.restricted');
    case 'forbidden': return T('explorer.err.notPermitted');
    case 'not_found': return T('explorer.err.notFound');
    case 'session': return T('explorer.err.session');
    case 'step_up_required': return T('explorer.err.stepUp');
    case 'invalid': return T('explorer.err.invalid');
  }
  var msg = String((e && e.message) || '');
  if (/^agent is not connected/i.test(msg)) return T('explorer.err.agentNotConnected');
  if (privacyIsDenial(msg)) return T('privacy.denied');
  msg = msg.replace(EXPLORER_CMD_PREFIX_RE, '').slice(0, 400) || T('common.error');
  return T(ctx === 'list' ? 'explorer.err.listFailed' : 'explorer.err.failed', { error: msg });
}
function explorerSay(text, kind) { reachNotify(text, kind || 'warn'); }
// Refused for privacy mode (the agent's words, or its code asked in vain).
function explorerIsPrivacy(e) { return !!e && (e.code === 'privacy' || (!e.code && privacyIsDenial(e.message))); }

// ── Transport: FILE_EXPLORER_CMD on the device's server socket ──
var explorerPending = new Map(); // requestId -> { tab, pid, sock, type, resolve, reject, timer }
// A command of this server is on its way (its socket must not be renewed).
function explorerBusy(pid) {
  pid = pid || '';
  var busy = false;
  explorerPending.forEach(function (p) { if (p.pid === pid) busy = true; });
  return busy;
}
// Nothing pending any more: a renewal that waited for it goes now.
function explorerSocketIdle(pid) {
  pid = pid || '';
  if (explorerBusy(pid) || typeof chatSock !== 'function') return;
  var s = chatSock(pid);
  if (s && s._renew && typeof chatRenewSocketWhenIdle === 'function') chatRenewSocketWhenIdle(pid);
}
// Each server socket (terminal_viewer.go toolsWireSocket): the results, and
// its disconnection (what it was waiting for is lost: the server answers
// the socket that asked, a new one is a new socket).
toolSocketHooks.push(function (sock, pid) {
  if (!sock || typeof sock.on !== 'function') return;
  sock.on('FILE_EXPLORER_RESULT', function (msg) { explorerOnResult(sock, pid || '', msg); });
  sock.on('disconnect', function () { explorerOnDisconnect(sock, pid || ''); });
});
function explorerOnResult(sock, pid, msg) {
  if (!msg || typeof msg !== 'object' || typeof msg.id !== 'string') return;
  var p = explorerPending.get(msg.id);
  if (!p || p.sock !== sock) return;
  explorerPending.delete(msg.id);
  clearTimeout(p.timer);
  var r = msg.result;
  if (msg.status === 'success') p.resolve(r);
  else {
    var text = (r && typeof r === 'object' && typeof r.error === 'string') ? r.error.slice(0, 600) : '';
    var code = (r && typeof r === 'object' && typeof r.code === 'string' && /^[a-z_]{1,40}$/.test(r.code)) ? r.code : '';
    var action = (r && typeof r === 'object' && typeof r.action === 'string') ? r.action.slice(0, 80) : '';
    p.reject(text ? explorerErr(text, code, action) : explorerErr({ k: 'explorer.err.failed', p: { error: T('common.error') } }, code, action));
  }
  explorerSocketIdle(pid);
}
function explorerOnDisconnect(sock, pid) {
  explorerPending.forEach(function (p, id) {
    if (p.sock !== sock) return;
    explorerPending.delete(id);
    clearTimeout(p.timer);
    p.reject(explorerErr({ k: 'explorer.err.connectionLost' }, 'connection'));
  });
}
// The device's server socket, connected (10 s at most: it may be loading,
// connecting, or being renewed).
function explorerSocket(pid) {
  pid = pid || '';
  return new Promise(function (resolve, reject) {
    var over = false, done = false, made = 0;
    var deadline = setTimeout(function () { over = true; }, EXPLORER_CONNECT_MS);
    var check = function () {
      if (done) return;
      var s = typeof chatSock === 'function' ? chatSock(pid) : null;
      // (a socket asked for twice at most: one the server refuses is not
      // asked for again every moment)
      if ((!s || !s.active) && made < 2 && typeof chatSocketFor === 'function') { var n = chatSocketFor(pid); if (n) { made++; s = n; } }
      if (s && s.connected) { done = true; clearTimeout(deadline); resolve(s); return; }
      if (over) { done = true; reject(explorerErr({ k: 'explorer.err.notConnected' }, 'not_connected')); return; }
      setTimeout(check, 250);
    };
    check();
  });
}
// Multi-server mode, on a server listing every tenant (scoped): the server
// session is put on the device's tenant first, as the pinned requests do
// (tenantpin.go /local/tenant-align; nothing is sent when it is there
// already). The tenant's restrictions on an explorer command are the
// device's (the master's too when the session is on the master tenant): the
// level must not depend on where an earlier request left the session.
// 'ok', 'denied' (no access to that tenant any more) or 'failed' (sent
// anyway: the server answers what it can).
async function explorerAlign(dev) {
  var pid = devPid(dev), st = srv.get(pid), t = devTenant(dev);
  if (serverMode !== 'multi' || !st || !st.scoped || !t) return 'ok';
  try {
    var r = await fetch('/local/tenant-align', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile: pid || '', tenantId: t }) });
    if (r && r.status === 403) return 'denied';
    return r && r.ok ? 'ok' : 'failed';
  } catch (e) { return 'failed'; }
}
// One command, one answer: resolves to the agent's result, rejects with an
// explorerErr (the server's refusal code, the agent's text, or ours).
async function explorerSend(tab, type, payload, ms) {
  var dev = tab.device, pid = devPid(dev);
  // A local connection (localconn_tools.go): the same commands to the app's
  // SFTP client, never a server socket.
  if (devIsLocal(dev)) return cxnSftpSend(tab, type, payload, ms);
  var sock = await explorerSocket(pid);
  if (tab.ex && tab.ex.disposed) throw explorerErr('', 'cancelled');
  if ((await explorerAlign(dev)) === 'denied') throw explorerErr({ k: 'devices.server.tenantDenied' }, 'forbidden');
  if (tab.ex && tab.ex.disposed) throw explorerErr('', 'cancelled');
  var id = explorerReqId();
  return new Promise(function (resolve, reject) {
    var p = { tab: tab, pid: pid, sock: sock, type: type, resolve: resolve, reject: reject, timer: null };
    p.timer = setTimeout(function () {
      if (explorerPending.get(id) !== p) return;
      explorerPending.delete(id);
      reject(explorerErr({ k: 'explorer.err.timeout' }, 'timeout'));
      explorerSocketIdle(pid);
    }, ms || EXPLORER_SHORT_MS);
    explorerPending.set(id, p);
    try {
      sock.emit('FILE_EXPLORER_CMD', { requestId: id, deviceId: dev.id, commandType: type, payload: payload || {} });
    } catch (e) {
      explorerPending.delete(id);
      clearTimeout(p.timer);
      reject(explorerErr({ k: 'explorer.err.notConnected' }, 'not_connected'));
    }
  });
}
// The tenant marks this action sensitive (step_up_required): the step-up
// route asks the app's usual 2FA prompt (its 401 answered by _apiWithTfa),
// then this session holds a grant for the device and the action: browsing
// for a while; any other action for ONE operation, or count for a
// multi-file operation (the files left of it), within 2 min. One prompt per
// device and action at a time (a listing and a download may ask together).
// Throws an explorerErr when cancelled or refused.
var explorerStepUps = new Map();
function explorerStepUpKey(dev, type) { return devKey(dev) + '|' + type; }
function explorerStepUp(dev, type, count) {
  var key = explorerStepUpKey(dev, type);
  var p = explorerStepUps.get(key);
  if (p) return p;
  p = explorerStepUpRun(dev, type, count);
  explorerStepUps.set(key, p);
  var drop = function () { if (explorerStepUps.get(key) === p) explorerStepUps.delete(key); };
  p.then(drop, drop);
  return p;
}
function explorerApiPath(dev, what) { return '/api/files/' + encodeURIComponent(String(dev.id)) + '/files/' + what; }
async function explorerStepUpRun(dev, type, count) {
  var r = null, d = null;
  var n = Math.floor(Number(count));
  var body = (isFinite(n) && n > 1) ? { commandType: type, count: Math.min(n, 100) } : { commandType: type };
  try { r = await devApi(dev, 'POST', explorerApiPath(dev, 'step-up'), body, true); }
  catch (e) { throw explorerErr({ k: 'explorer.err.stepUpFailed', p: { error: reachErrText(e) } }, 'step_up_failed'); }
  try { d = await r.json(); } catch (e) { d = null; }
  if (r.ok) return true;
  var err = (d && typeof d.error === 'string') ? d.error.slice(0, 300) : '';
  if (r.status === 401 && d && d.twoFactorRequired) throw explorerErr({ k: 'explorer.err.stepUpCancelled' }, 'step_up_cancelled');
  if (r.status === 403 && d && d.code === 'restricted') throw explorerErr({ k: 'explorer.err.restricted' }, 'restricted');
  if (r.status === 403 && d && (d.code === 'forbidden' || /not permitted/i.test(err))) throw explorerErr({ k: 'explorer.err.notPermitted' }, 'forbidden');
  if (r.status === 404) throw explorerErr({ k: 'explorer.err.notFound' }, 'not_found');
  throw explorerErr({ k: 'explorer.err.stepUpFailed', p: { error: err || ('HTTP ' + r.status) } }, 'step_up_failed');
}
// The agent refused for privacy: the code asked again, one retry (one
// prompt per tab at a time).
function explorerPrivacyGate(tab) {
  if (tab.ex.privacyGate) return tab.ex.privacyGate;
  var p = privacyRetryGate(tab.device, 'files').then(function (fresh) {
    if (fresh && tab.device !== fresh) {
      if (typeof selectedDevice !== 'undefined' && selectedDevice === tab.device) selectedDevice = fresh;
      tab.device = fresh;
    }
    return !!fresh;
  }, function () { return false; });
  tab.ex.privacyGate = p;
  var drop = function () { if (tab.ex.privacyGate === p) tab.ex.privacyGate = null; };
  p.then(drop, drop);
  return p;
}
// A command with its recoveries: the step-up once (once more when the
// confirmation it waited for was another command's: one confirmation, one
// operation), the privacy code once. batch: the operations of a multi-file
// operation left, this one included (one confirmation for them all).
// opts.onPrompt(kind): a confirmation is being asked ('stepUp', 'privacy').
async function explorerCmd(tab, type, payload, ms, batch, opts) {
  var stepUps = 0, joined = false, privacyAsked = false;
  var prompt = function (kind) { if (opts && typeof opts.onPrompt === 'function') { try { opts.onPrompt(kind); } catch (x) {} } };
  // (a local connection: no server, so no step-up nor privacy code to recover with)
  if (devIsLocal(tab.device)) return explorerSend(tab, type, payload, ms);
  for (;;) {
    try { return await explorerSend(tab, type, payload, ms); }
    catch (e) {
      if (tab.ex.disposed) throw explorerErr('', 'cancelled');
      if (e && e.code === 'step_up_required' && (stepUps === 0 || (stepUps === 1 && joined))) {
        stepUps++;
        joined = explorerStepUps.has(explorerStepUpKey(tab.device, type));
        prompt('stepUp');
        await explorerStepUp(tab.device, type, batch);
        if (tab.ex.disposed) throw explorerErr('', 'cancelled');
        continue;
      }
      if (e && !e.code && explorerIsPrivacy(e) && !privacyAsked) {
        privacyAsked = true;
        prompt('privacy');
        if (!(await explorerPrivacyGate(tab))) throw explorerErr({ k: 'privacy.denied' }, 'privacy');
        if (tab.ex.disposed) throw explorerErr('', 'cancelled');
        continue;
      }
      throw e;
    }
  }
}

// ── A device's context without an explorer tab ──
// The transfer queue (transfers.go) and the editing round trip
// (edit_page.go) send their commands through it: the same path as a tab
// (explorerCmd: the device's server socket, its tenant, the step-up, the
// privacy code), the audit POST open-explorer once (a 403 stops it there).
// Keyed by the device's key; ended with the device's tabs (sign-out, a
// server or tenant left: explorerCtxDisposeWhere).
var explorerCtxs = new Map();
function explorerCtxFor(dev) {
  var k = devKey(dev), c = explorerCtxs.get(k);
  if (c && !c.ex.disposed) return c;
  c = { id: 'ctx:' + k, kind: 'files', ctx: true, key: k, device: dev, ex: { disposed: false, privacyGate: null, audited: false, auditing: null } };
  explorerCtxs.set(k, c);
  return c;
}
async function explorerCtxOpen(ctx) {
  if (ctx.ex.audited || devIsLocal(ctx.device)) return; // (a local connection: no server to audit it)
  if (!ctx.ex.auditing) {
    ctx.ex.auditing = (async function () {
      var r = null;
      try { r = await devApi(ctx.device, 'POST', explorerApiPath(ctx.device, 'open-explorer'), {}, true); } catch (e) { r = null; }
      if (ctx.ex.disposed) throw explorerErr('', 'cancelled');
      if (r && r.status === 403) throw explorerErr({ k: 'explorer.err.notPermitted' }, 'forbidden');
      if (r && r.status === 401) {
        var d = null; try { d = await r.json(); } catch (e) { d = null; }
        if (d && d.error === 'Authentication required') throw explorerErr({ k: 'explorer.err.session' }, 'session');
      }
      if (r && r.ok) ctx.ex.audited = true;
    })();
    var drop = function () { ctx.ex.auditing = null; };
    ctx.ex.auditing.then(drop, drop);
  }
  await ctx.ex.auditing;
}
// explorerCmd through a context (its audit first) or a tab.
async function explorerCtxCmd(ctx, type, payload, ms, batch, opts) {
  if (ctx && ctx.ctx) await explorerCtxOpen(ctx);
  if (ctx.ex.disposed) throw explorerErr('', 'cancelled');
  return explorerCmd(ctx, type, payload, ms, batch, opts);
}
// Sign-out, a server or tenant left (terminal_viewer.go toolDisposeWhere):
// the devices' contexts end, their transfers stop, their files being edited
// stop (pred sees { device }). editKey: the editing notice (a device gone).
function explorerCtxDisposeWhere(pred, editKey) {
  var hit = function (dev) { try { return !!pred({ device: dev }); } catch (e) { return false; } };
  explorerCtxs.forEach(function (c, k) {
    if (!hit(c.device)) return;
    explorerCtxs.delete(k);
    explorerDispose(c);
  });
  if (typeof xferQueues !== 'undefined') xferQueues.forEach(function (q) { if (hit(q.dev)) xferAbortQueue(q); });
  if (typeof editDisposeWhere === 'function') editDisposeWhere(function (x) { return hit(x.device); }, editKey);
}
// A server's devices read again (ui.go: loadOverview, srvLoadRun), its list
// whole and fresh: the devices it no longer lists for this operator (a right
// taken back, a device removed) lose their context, their transfers and
// their files being edited (Oblireach shows and reaches only what the
// operator may; the server would refuse them anyway). Never on a failed read.
function explorerCtxPruneGone(pid, groups) {
  if (!Array.isArray(groups)) return 0;
  pid = pid || '';
  var ids = new Set();
  groups.forEach(function (g) { (g && Array.isArray(g.devices) ? g.devices : []).forEach(function (d) { if (d && d.id !== undefined && d.id !== null) ids.add(String(d.id)); }); });
  var n = 0;
  var gone = function (dev) { return !!dev && devPid(dev) === pid && !ids.has(String(dev.id)); };
  explorerCtxs.forEach(function (c) { if (gone(c.device)) n++; });
  if (typeof xferQueues !== 'undefined') xferQueues.forEach(function (q) { if (gone(q.dev)) n++; });
  if (typeof editSessions !== 'undefined') editSessions.forEach(function (e) { if (gone(e.dev)) n++; });
  if (n) explorerCtxDisposeWhere(function (t) { return gone(t && t.device); }, 'edit.stoppedGone');
  return n;
}

// ── The tab ──
var explorerImpl = {
  create: function (tab) { explorerCreate(tab); },
  start: function (tab) { return explorerStart(tab); },
  render: function (tab, tb) { explorerRenderPane(tab, tb); },
  dispose: function (tab) { explorerDispose(tab); },
  focus: function (tab) { explorerFocus(tab); },
};
TOOL_IMPL.files = explorerImpl;

// (its id before it joins its parent: an element is known by its id from then on)
function explorerEl(tag, cls, parent, id) {
  var e = document.createElement(tag);
  if (cls) e.className = cls;
  if (id) e.id = id;
  if (parent) parent.appendChild(e);
  return e;
}
function explorerBtn(parent, id, icon, labelKey, fn) {
  var b = explorerEl('button', 'btn-sm', parent, id);
  b.innerHTML = icon; b.title = T(labelKey);
  b.setAttribute('aria-label', T(labelKey));
  b.addEventListener('click', fn);
  return b;
}
function explorerCreate(tab) {
  var dev = tab.device, os = explorerOs(dev);
  // The tab's state, shared by its views (its sub-tabs: explorer_tabs.go);
  // each view's explorer state is its own (xvMake), and the tab's fields of
  // a view's (XV_KEYS: its folder, its selection...) read the view in focus.
  var ex = tab.ex = { os: os, split: false, disposed: false, audited: false, privacyGate: null, transfers: [], trSeq: 0, focusSide: 'remote', dockTab: '', ui: {} };
  xvRootDefine(tab);
  var host = tab.host, ui = ex.ui;
  host.className = 'tool-host exp-host';
  // The main window with the local file bindings: the split view (this PC
  // on the left, local_pane.go; the device on the right; the dock of the
  // transfers and files being edited below). Elsewhere, the device alone
  // (a session's side panel too: files_dock.go, no room for two panes).
  var split = ex.split = !tab.docked && localFsAvailable();
  var side = host, lpEl = null, rail = null;
  if (split) {
    host.className = 'tool-host exp-host xp-host' + (lpShownPref() ? '' : ' no-local');
    var sp = ui.split = explorerEl('div', 'xp-split', host);
    lpEl = explorerEl('div', 'xp-pane xp-local', sp, 'xp-local');
    rail = ui.rail = explorerEl('div', 'xp-rail', sp, 'xp-rail');
    side = ui.rpane = explorerEl('div', 'xp-pane xp-remote', sp, 'xp-remote');
    ui.rhead = explorerEl('div', 'xp-head', side, 'xp-rhead');
    side.addEventListener('focusin', function () { xpFocusPane(tab, 'remote'); });
  }
  // The device's side: the sub-tabs (their "+" above the breadcrumb), then
  // the views (one, or a mosaic of them), each with its own bar and list.
  xvBuild(tab, side);
  explorerWireDrops(tab, side);
  // Keys of the whole explorer (the view they are typed in: explorerHostKey).
  host.addEventListener('keydown', function (e) { explorerHostKey(tab, e); });
  // The transfers of this tab.
  ui.tr = explorerEl('div', 'exp-tr', host, 'exp-tr'); ui.tr.style.display = 'none';
  if (split) ui.dock = explorerEl('div', 'xp-dock empty', host, 'xp-dock');
  var first = xvNew(tab, '', { quiet: true });
  if (split) {
    lpCreate(tab, lpEl);
    xpRailBuild(tab, rail);
  }
  xvApply(tab, false);
  if (first) explorerRender(first);
}
// A view's explorer (explorer_tabs.go xvNew): its bar, its list, its
// analyzer, in its pane (v.host); every handler works on the view.
function explorerCreateView(v) {
  var tab = v, ex = v.ex, ui = ex.ui, split = !!ex.split, pane = v.host, sfx = v.xvSfx || '';
  // Toolbar: up, refresh, the path (breadcrumb, or typed), the filter, the actions.
  var bar = ui.bar = explorerEl('div', 'exp-bar', pane);
  ui.up = explorerBtn(bar, 'exp-up' + sfx, EXPLORER_ICON.up, 'explorer.up', function () { explorerUp(tab); });
  ui.refresh = explorerBtn(bar, 'exp-refresh' + sfx, EXPLORER_ICON.refresh, 'common.refresh', function () { explorerRefresh(tab); });
  var crumbs = ui.crumbs = explorerEl('div', 'exp-crumbs', bar, 'exp-crumbs' + sfx);
  crumbs.title = T('explorer.path');
  crumbs.addEventListener('click', function (e) { if (!e || e.target === crumbs) explorerEditPath(tab, true); });
  var pathIn = ui.path = explorerEl('input', 'exp-path', bar, 'exp-path' + sfx);
  pathIn.type = 'text'; pathIn.setAttribute('autocomplete', 'off'); pathIn.setAttribute('spellcheck', 'false');
  pathIn.setAttribute('aria-label', T('explorer.path')); pathIn.placeholder = T('explorer.path');
  pathIn.addEventListener('keydown', function (e) {
    if (!e) return;
    if (e.key === 'Enter') { if (e.preventDefault) e.preventDefault(); explorerGoTyped(tab, pathIn.value); }
    else if (e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); explorerEditPath(tab, false); explorerFocus(tab); }
  });
  pathIn.addEventListener('blur', function () { explorerEditPath(tab, false); });
  var filt = ui.filter = explorerEl('input', 'exp-filter', bar, 'exp-filter' + sfx);
  filt.type = 'search'; filt.setAttribute('autocomplete', 'off'); filt.setAttribute('spellcheck', 'false');
  filt.placeholder = T('explorer.filter'); filt.setAttribute('aria-label', T('explorer.filter'));
  filt.addEventListener('input', function () { ex.filter = String(filt.value || ''); explorerRenderList(tab); });
  // (Escape: cleared, the keyboard back to the list: explorerFilterEscape)
  filt.addEventListener('keydown', function (e) { explorerFilterEscape(tab, 'remote', e); });
  explorerEl('div', 'toolbar-sep', bar);
  ui.mkdir = explorerBtn(bar, 'exp-mkdir' + sfx, EXPLORER_ICON.newFolder, 'explorer.newFolder', function () { explorerMkdir(tab); });
  // Upload: the local pane's selection (split view), else the file picker.
  ui.upload = explorerBtn(bar, 'exp-upload' + sfx, EXPLORER_ICON.upload, 'explorer.upload', function () {
    if (split && xpLocalShown(tab) && tab.lp.path && tab.lp.sel.size) { lpSendSel(tab); return; }
    if (ui.file && typeof ui.file.click === 'function') ui.file.click();
  });
  var fileIn = ui.file = explorerEl('input', '', bar, 'exp-file' + sfx);
  fileIn.type = 'file'; fileIn.multiple = true; fileIn.style.display = 'none';
  fileIn.addEventListener('change', function () {
    var files = fileIn.files ? Array.prototype.slice.call(fileIn.files) : [];
    fileIn.value = '';
    if (!files.length) return;
    if (split) xferUploadDropped(tab, files, ex.path); else explorerUpload(tab, files);
  });
  ui.download = explorerBtn(bar, 'exp-download' + sfx, EXPLORER_ICON.download, 'explorer.download', function () { explorerDownloadSel(tab); });
  ui.rename = explorerBtn(bar, 'exp-rename' + sfx, EXPLORER_ICON.rename, 'explorer.rename', function () { explorerRenameSel(tab); });
  // Delete, a thin divider, then the disk usage analyzer (usage_viewer.go:
  // the folder on screen, or the drive selected on the drives list). The
  // three wrap together: the divider never starts nor ends a line.
  var grp = ui.delGrp = explorerEl('span', 'exp-grp', bar);
  ui.del = explorerBtn(grp, 'exp-delete' + sfx, EXPLORER_ICON.trash, 'explorer.delete', function () { explorerDeleteSel(tab); });
  ui.usagePipe = explorerEl('span', 'exp-pipe', grp, 'exp-usage-pipe' + sfx);
  ui.usagePipe.setAttribute('aria-hidden', 'true');
  ui.usageBtn = explorerBtn(grp, 'exp-usage' + sfx, USAGE_ICON.analyze, 'usage.analyze', function () { var p = usageTarget(tab); if (p) usageOpen(tab, p, false); });
  // A Unix device's file systems, above the listing of '/'.
  ui.vols = explorerEl('div', 'exp-vols', pane, 'exp-vols' + sfx); ui.vols.style.display = 'none';
  // The list.
  var list = ui.list = explorerEl('div', 'exp-list', pane, 'exp-list' + sfx);
  list.tabIndex = 0; list.setAttribute('role', 'grid'); list.setAttribute('aria-label', T('tools.files'));
  var table = explorerEl('table', 'dt exp-table', list);
  var thead = explorerEl('thead', '', table), hr = explorerEl('tr', '', thead);
  ui.th = {};
  [['name', 'explorer.col.name', 'c-name'], ['size', 'explorer.col.size', 'c-size'], ['modified', 'explorer.col.modified', 'c-mod']].forEach(function (c) {
    var th = explorerEl('th', c[2], hr); th.dataset.sort = c[0]; th.textContent = T(c[1]);
    th.addEventListener('click', function () { explorerSortBy(tab, c[0]); });
    ui.th[c[0]] = th;
  });
  ui.tbody = explorerEl('tbody', '', table);
  ui.note = explorerEl('div', 'exp-note', list, 'exp-note' + sfx);
  ui.drop = explorerEl('div', 'exp-drop', list); ui.drop.textContent = T('explorer.dropHint');
  // The analyzer's view takes the place of the bar and the list (usage_viewer.go).
  ui.usPane = pane;
  usageBuild(tab);
  list.addEventListener('keydown', function (e) { explorerListKey(tab, e); });
  list.addEventListener('scroll', function () { if (list.isConnected !== false) ex.scrollTop = list.scrollTop || 0; });
  list.addEventListener('contextmenu', function (e) {
    if (e && e.preventDefault) e.preventDefault();
    explorerMenuOpen(tab, e, null);
  });
  list.addEventListener('mousedown', function (e) {
    // A click on the empty part of the list (not its scrollbar): nothing selected.
    if (!e || (e.target !== list && e.target !== ui.note) || e.button !== 0 || !ex.sel.size) return;
    if (typeof e.offsetX === 'number' && typeof list.clientWidth === 'number' && e.offsetX >= list.clientWidth) return;
    ex.sel.clear(); ex.anchor = ''; explorerPaintSel(tab);
  });
  // (its "could not open" notice: over this view only)
  pane.appendChild(v.ovEl);
}
// Files dropped on the explorer: sent to the folder on screen of the view
// under the pointer (explorer_tabs.go). The split view: to the folder row
// under the pointer, folders included (the transfer queue), and the local
// pane's selection dragged here too. side: the device's side (the tab's
// host, or the split view's right pane); no drop from one view to another
// (the device has no copy of its own: refused).
function explorerWireDrops(root, side) {
  var split = !!root.ex.split, pane = side;
  var hasFiles = function (e) { var t = e && e.dataTransfer && e.dataTransfer.types; return !!t && Array.prototype.indexOf.call(t, 'Files') >= 0; };
  pane.addEventListener('dragover', function (e) {
    var tab = xvAt(root, e && e.target), ex = tab.ex, ui = ex.ui;
    explorerDropOthers(root, tab);
    if (split && xpDragIs(e)) {
      var can2 = xpDragFrom(tab, 'local') && explorerCanWrite(tab);
      if (e.preventDefault) e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = can2 ? 'copy' : 'none';
      explorerDropTarget(tab, can2 ? explorerRowPathAt(tab, e) : null, can2, xpDrag ? xpDrag.paths.length : 0);
      return;
    }
    if (!hasFiles(e)) return;
    e.preventDefault();
    var can = explorerCanWrite(tab);
    e.dataTransfer.dropEffect = can ? 'copy' : 'none';
    if (split) explorerDropTarget(tab, can ? explorerRowPathAt(tab, e) : null, can, -1);
    else ui.drop.classList.toggle('visible', can);
  });
  pane.addEventListener('dragleave', function (e) {
    if (!e || !e.relatedTarget || typeof pane.contains !== 'function' || !pane.contains(e.relatedTarget)) explorerDropOthers(root, null);
  });
  pane.addEventListener('drop', function (e) {
    var tab = xvAt(root, e && e.target), ex = tab.ex;
    var target = ex.dropPath;
    explorerDropOthers(root, null);
    if (split && xpDragIs(e)) {
      if (e.preventDefault) e.preventDefault();
      if (!xpDragFrom(tab, 'local') || !explorerCanWrite(tab) || !lpCanWrite(tab)) return;
      var names = xpDrag.paths.map(function (p) { return String(p).slice(String(p).lastIndexOf(localSep()) + 1); });
      xpDragEnd();
      xferUploadLocal(tab, tab.lp.path, names, target || ex.path);
      return;
    }
    if (!hasFiles(e)) return;
    e.preventDefault();
    if (split) { if (explorerCanWrite(tab)) xferUploadDropped(tab, e.dataTransfer, target || ex.path); return; }
    explorerDropped(tab, e.dataTransfer);
  });
}
// The drop hints of the views other than keep (null: all of them) hidden.
function explorerDropOthers(root, keep) {
  explorerViews(root).forEach(function (v) {
    if (v === keep || !v.ex || !v.ex.ui.drop) return;
    if (v.ex.split) { if (v.ex.dropPath || v.ex.ui.drop.classList.contains('visible')) explorerDropTarget(v, null, false); }
    else v.ex.ui.drop.classList.remove('visible');
  });
}
// Keys of the whole explorer, for the view they are typed in (else the one
// in focus): F5 / Ctrl+R refresh (never the page's reload), Ctrl+F the
// filter, Ctrl+L the path, Ctrl+T a new view on this folder, Ctrl+W this
// view closed (never the last one: explorer_tabs.go).
function explorerHostKey(root, e) {
  if (!e) return;
  var tab = xvAt(root, e.target), ex = tab.ex, split = !!root.ex.split;
  var k = String(e.key || ''), code = String(e.code || ''), ctrl = !!(e.ctrlKey || e.metaKey);
  var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  var side = explorerKeySide(tab, e.target);
  if (ctrl && !e.altKey && !e.shiftKey && side === 'remote' && !explorerKeyBusy()) {
    if (code === 'KeyT' || k.toLowerCase() === 't') { stop(); xvNewHere(root, false); return; }
    if ((code === 'KeyW' || k.toLowerCase() === 'w') && xvList(root).length > 1) { stop(); xvClose(root, tab); return; }
  }
  if (ex.usage && ex.usage.on) {
    // (the analyzer's view: F5 / Ctrl+R read its folders again)
    if (((k === 'F5' || code === 'F5') && !e.altKey) || (ctrl && !e.altKey && (code === 'KeyR' || k.toLowerCase() === 'r'))) {
      stop(); ex.usage.nodes.forEach(function (r) { r.stale = true; }); ex.usage.files = new Map(); usageSchedule(tab, 0);
    }
    return;
  }
  if ((k === 'F5' || code === 'F5') && !e.altKey) { stop(); explorerRefresh(tab); return; }
  if (split && (k === 'F6' || code === 'F6') && !ctrl && !e.altKey) { stop(); xpFocusCycle(root, e.shiftKey ? -1 : 1, e.target); return; }
  if (ctrl && !e.altKey && (code === 'KeyR' || k.toLowerCase() === 'r')) { stop(); explorerRefresh(tab); return; }
  if (ctrl && !e.altKey && (code === 'KeyF' || k.toLowerCase() === 'f')) { stop(); try { ex.ui.filter.focus(); } catch (x) {} return; }
  if (ctrl && !e.altKey && (code === 'KeyL' || k.toLowerCase() === 'l')) { stop(); explorerEditPath(tab, true); return; }
  // Ctrl+V: the clipboard's files (or image) into the device's folder;
  // a character typed: the filter of the pane it was typed in.
  if (explorerIsPasteKey(e)) {
    if (side !== 'remote' || explorerKeyInField(e.target) || explorerKeyBusy() || !explorerPasteAvailable()) return;
    stop(); explorerPaste(tab);
    return;
  }
  explorerPaneKey(tab, side, e, false);
}
function explorerStart(tab) {
  if (tab.ex && tab.ex.split) {
    editSettingsLoad();
    editRecover();
    if (xpLocalShown(tab)) lpStart(tab);
  }
  // A tab moved here with its views (files_dock.go): those first.
  var init = tab.xvInit;
  tab.xvInit = null;
  if (init) xvRestore(tab, init);
  // The view in focus opens (the audit, its folder); then the others.
  var f = xvView(tab), rest = xvList(tab).filter(function (v) { return v !== f; });
  var p = explorerOpen(f);
  if (rest.length) {
    p = Promise.resolve(p).then(function () {
      rest.forEach(function (v) { if (!v.ex.disposed && !v.ex.closed) { var q = explorerOpen(v); if (q && typeof q.catch === 'function') q.catch(function () {}); } });
    });
  }
  return p;
}
// Opening (and Retry): the audit once, then the folder on screen (the roots at first).
async function explorerOpen(tab) {
  tab = xvView(tab);
  var ex = tab.ex;
  if (!ex || ex.disposed || ex.starting) return;
  ex.starting = true;
  try {
    ex.fatal = null;
    explorerSetStatus(tab, 'starting', { k: 'common.loading' });
    toolSetOverlay(tab, { kind: 'busy', title: { k: 'common.loading' }, text: '' });
    if (!ex.audited && !devIsLocal(tab.device)) { // (a local connection: no server to audit it)
      var r = null;
      try { r = await devApi(tab.device, 'POST', explorerApiPath(tab.device, 'open-explorer'), {}, true); } catch (e) { r = null; }
      if (ex.disposed) return;
      if (r && r.status === 403) { explorerFatal(tab, explorerErr({ k: 'explorer.err.notPermitted' }, 'forbidden')); return; }
      if (r && r.status === 401) {
        var d = null; try { d = await r.json(); } catch (e) { d = null; }
        if (d && d.error === 'Authentication required') { explorerFatal(tab, explorerErr({ k: 'explorer.err.session' }, 'session')); return; }
      }
      if (r && r.ok) ex.audited = true;
    }
    await explorerList(tab, ex.path || '', { keepSel: true });
  } finally { ex.starting = false; }
}
// It could not open (its first listing, or the audit refused): said over it.
function explorerFatal(tab, e) {
  var ex = tab.ex;
  ex.fatal = e; ex.loading = false;
  explorerSetStatus(tab, 'error', { k: 'explorer.err.openFailed' });
  toolSetOverlay(tab, { kind: 'error', title: { k: 'explorer.err.openFailed' }, text: explorerErrText(e, 'list'), acts: [
    { label: { k: explorerIsPrivacy(e) ? 'privacy.retry' : 'common.retry' }, primary: true, id: 'exp-ov-retry', run: function () { explorerOpen(tab); } },
    { label: { k: 'common.close' }, id: 'exp-ov-close', run: function () { explorerCloseView(tab); } },
  ] });
  explorerRender(tab);
}
function explorerStatusText(tab) { return tab.ex.path ? tab.ex.path : { k: 'explorer.roots' }; }
// A folder listed. opts: keepSel (a refresh: the selection kept where it
// still exists), select (a path to select: the folder just created...).
async function explorerList(tab, path, opts) {
  tab = xvView(tab);
  var ex = tab.ex;
  opts = opts || {};
  var seq = ++ex.listSeq;
  ex.loading = true;
  explorerRenderNote(tab); explorerUpdateBar(tab);
  var res;
  try { res = await explorerCmd(tab, 'list_directory', { path: String(path || '') }, EXPLORER_SHORT_MS); }
  catch (e) {
    if (seq !== ex.listSeq || ex.disposed || (e && e.code === 'cancelled')) return false;
    ex.loading = false;
    if (!ex.listed) { explorerFatal(tab, e); return false; }
    // (quiet: a link tried as a folder first; the caller goes on)
    if (!opts.quiet) explorerSay(explorerErrText(e, 'list'));
    explorerRender(tab);
    return false;
  }
  if (seq !== ex.listSeq || ex.disposed) return false;
  ex.loading = false;
  var dir = (res && !Array.isArray(res) && typeof res.path === 'string') ? res.path : String(path || '');
  var same = dir === ex.path;
  var prevSel = opts.keepSel && same ? ex.sel : null;
  if (!same) {
    // Another folder: from its top, unfiltered.
    ex.filter = ''; ex.ui.filter.value = '';
    ex.scrollTop = 0;
    try { ex.ui.list.scrollTop = 0; } catch (e) {}
  }
  ex.path = dir;
  ex.items = explorerItems(res, dir, ex.os);
  // A Unix agent sends "volumes" with the roots listing ("" path) only, not
  // when "/" is listed by path (Up, address bar): keep the last ones seen.
  if (dir === '/') {
    var vols = usageVolumesOf(res);
    if (vols.length) ex.rootVolumes = vols;
    ex.volumes = vols.length ? vols : (ex.rootVolumes || []);
  } else {
    ex.volumes = [];
  }
  ex.sel = new Set();
  ex.anchor = '';
  if (prevSel) ex.items.forEach(function (it) { if (prevSel.has(it.path)) ex.sel.add(it.path); });
  if (opts.select) ex.items.forEach(function (it) { if (it.path === opts.select || it.name === opts.select) { ex.sel.add(it.path); ex.anchor = it.path; } });
  ex.listed = true; ex.fatal = null;
  toolSetOverlay(tab, null);
  explorerSetStatus(tab, 'live', explorerStatusText(tab));
  explorerRender(tab);
  return true;
}
function explorerRefresh(tab) { tab = xvView(tab); if (tab.ex.fatal) return explorerOpen(tab); return explorerList(tab, tab.ex.path, { keepSel: true }); }
function explorerUp(tab) {
  var ex = tab.ex;
  if (explorerIsTop(ex.path, ex.os)) return;
  var from = ex.path;
  explorerList(tab, explorerParent(ex.path, ex.os), { select: from });
}
function explorerGoTyped(tab, v) {
  var p = explorerNormPath(v, tab.ex.os);
  if (p === null) { explorerSay(T('explorer.invalidPath')); return; }
  explorerEditPath(tab, false);
  explorerList(tab, p);
  explorerFocus(tab);
}
function explorerEditPath(tab, on) {
  tab = xvView(tab);
  // (its view's pane; the tab's host says it too: the view in focus is being typed in)
  var ui = tab.ex.ui, o = xvOwnerOf(tab);
  if (on) {
    ui.path.value = tab.ex.path;
    tab.host.classList.add('editing');
    if (o !== tab) o.host.classList.add('editing');
    try { ui.path.focus(); if (typeof ui.path.select === 'function') ui.path.select(); } catch (e) {}
  } else {
    tab.host.classList.remove('editing');
    if (o !== tab) o.host.classList.remove('editing');
  }
}
function explorerSortBy(tab, key) {
  var ex = tab.ex;
  if (ex.sortKey === key) ex.sortDir = -ex.sortDir; else { ex.sortKey = key; ex.sortDir = 1; }
  explorerRenderList(tab);
}

// ── Drawing ──
function explorerRender(tab) {
  tab = xvView(tab);
  if (!tab.ex || !tab.ex.ui.bar) return;
  explorerRenderCrumbs(tab);
  explorerRenderList(tab);
  explorerRenderTransfers(tab);
  if (tab.ex.split) { explorerRenderHead(tab); xpDockRender(tab); }
  if (tab.xvOwner) xvRenderStrip(tab.xvOwner); // (its sub-tab: its folder's name)
}
// The split view: the device's pane's title (its server when several show,
// its name, its OS, its state: the tab's, its view in focus's).
function explorerRenderHead(tab) {
  tab = xvOwnerOf(tab);
  var h = tab.ex.ui.rhead;
  if (!h) return;
  h.innerHTML = '';
  var pid = devPid(tab.device), multi = typeof serverMode !== 'undefined' && serverMode === 'multi';
  var ic = explorerEl('span', 'xp-head-ico', h); ic.innerHTML = EXPLORER_ICON.drive;
  var txt = explorerEl('div', 'xp-head-txt', h);
  var t1 = explorerEl('span', 'xp-head-t', txt, 'xp-rhead-name'); t1.textContent = tab.label;
  var k = devOsKind(tab.device);
  var t2 = explorerEl('span', 'xp-head-s', txt);
  t2.textContent = [multi && typeof srvName === 'function' ? srvName(pid) : '', k === 'windows' ? 'Windows' : k === 'macos' ? 'macOS' : k === 'unix' ? 'Linux' : ''].filter(Boolean).join(' · ');
  explorerEl('span', 'sp', h);
  if (multi && typeof srvColor === 'function') { var sd = explorerEl('span', 'xp-srv-dot', h); sd.style.background = srvColor(pid); sd.title = srvName(pid); }
  var dot = explorerEl('span', 'dot ' + toolTabDot(tab), h); dot.title = toolTxt(tab.statusText);
}
function explorerRenderCrumbs(tab) {
  var ex = tab.ex, el = ex.ui.crumbs;
  el.innerHTML = '';
  var cs = explorerCrumbs(ex.path, ex.os);
  cs.forEach(function (c, i) {
    if (i > 0 && !(ex.os !== 'windows' && i === 1)) { var s = explorerEl('span', 'exp-crumb-sep', el); s.textContent = '\u203A'; }
    var b = explorerEl('button', 'exp-crumb' + (i === cs.length - 1 ? ' cur' : ''), el);
    b.textContent = c.label; b.title = c.path || c.label; b.dataset.path = c.path;
    if (i === cs.length - 1) b.setAttribute('aria-current', 'location');
    b.addEventListener('click', function () { explorerList(tab, c.path, c.path === ex.path ? { keepSel: true } : null); });
  });
}
function explorerVisible(tab) {
  var ex = tab.ex, f = ex.filter.trim().toLowerCase();
  var items = f ? ex.items.filter(function (it) { return it.name.toLowerCase().indexOf(f) >= 0; }) : ex.items;
  return explorerSort(items, ex.sortKey, ex.sortDir);
}
function explorerRenderList(tab) {
  tab = xvView(tab);
  var ex = tab.ex, ui = ex.ui;
  ex.view = explorerVisible(tab);
  ui.tbody.innerHTML = '';
  ex.rowEls = new Map();
  var roots = ex.os === 'windows' && ex.path === '';
  ex.view.forEach(function (it) {
    var tr = explorerEl('tr', ex.sel.has(it.path) ? 'sel' : '', ui.tbody);
    tr.dataset.path = it.path;
    tr.setAttribute('aria-selected', ex.sel.has(it.path) ? 'true' : 'false');
    var td = explorerEl('td', 'c-name', tr), nm = explorerEl('div', 'exp-name', td);
    var ic = explorerEl('span', 'exp-ico' + (it.isDir ? ' dir' : ''), nm);
    ic.innerHTML = roots ? EXPLORER_ICON.drive : it.isDir ? EXPLORER_ICON.folder : EXPLORER_ICON.file;
    var sp = explorerEl('span', 'nm', nm); sp.textContent = it.name;
    td.title = it.path;
    var sz = explorerEl('td', 'c-size', tr); sz.textContent = it.isDir ? '' : reachFmtBytes(it.size);
    var md = explorerEl('td', 'c-mod', tr); md.textContent = roots ? '' : explorerFmtDate(it.modified);
    if (roots) usageDriveRow(tab, it, tr, nm, sz, md);
    tr.addEventListener('click', function (e) { explorerClickRow(tab, it, e || {}); });
    tr.addEventListener('dblclick', function () { explorerOpenItem(tab, it); });
    tr.addEventListener('contextmenu', function (e) {
      if (e && e.preventDefault) e.preventDefault();
      if (e && e.stopPropagation) e.stopPropagation();
      if (!ex.sel.has(it.path)) { ex.sel = new Set([it.path]); ex.anchor = it.path; explorerPaintSel(tab); }
      explorerMenuOpen(tab, e, it);
    });
    // The split view: a drag to the local pane downloads (never the drives).
    if (ex.split && !roots) {
      tr.draggable = true;
      tr.addEventListener('dragstart', function (e) {
        if (!ex.sel.has(it.path)) { ex.sel = new Set([it.path]); ex.anchor = it.path; explorerPaintSel(tab); }
        xpDragStart(tab, 'remote', explorerSelItems(tab).map(function (x) { return x.path; }), e);
      });
      tr.addEventListener('dragend', xpDragEnd);
    }
    ex.rowEls.set(it.path, tr);
  });
  ['name', 'size', 'modified'].forEach(function (k) {
    var th = ui.th[k];
    th.setAttribute('aria-sort', ex.sortKey === k ? (ex.sortDir === 1 ? 'ascending' : 'descending') : 'none');
    th.textContent = T(k === 'name' ? 'explorer.col.name' : k === 'size' ? 'explorer.col.size' : 'explorer.col.modified') +
      (ex.sortKey === k ? (ex.sortDir === 1 ? ' \u2191' : ' \u2193') : '');
  });
  usageRenderVolumes(tab);
  explorerRenderNote(tab);
  explorerUpdateBar(tab);
}
function explorerRenderNote(tab) {
  var ex = tab.ex, n = ex.ui.note;
  var text = '';
  if (ex.loading && !ex.view.length) text = T('common.loading');
  else if (ex.fatal && !ex.listed) text = '';
  else if (ex.listed && !ex.view.length) text = ex.items.length ? T('explorer.noMatch') : T('explorer.empty');
  n.textContent = text;
  n.style.display = text ? '' : 'none';
  ex.ui.list.classList.toggle('loading', !!ex.loading);
}
function explorerPaintSel(tab) {
  tab = xvView(tab);
  var ex = tab.ex;
  ex.rowEls.forEach(function (tr, p) {
    var on = ex.sel.has(p);
    tr.classList.toggle('sel', on);
    tr.setAttribute('aria-selected', on ? 'true' : 'false');
  });
  explorerUpdateBar(tab);
}
function explorerSelItems(tab) {
  tab = xvView(tab);
  var ex = tab.ex;
  return ex.items.filter(function (it) { return ex.sel.has(it.path); });
}
// A folder here can receive (not the drives list of Windows).
function explorerCanWrite(tab) { tab = xvView(tab); var ex = tab.ex; return ex.listed && !ex.fatal && !(ex.os === 'windows' && ex.path === ''); }
// Download off: nothing to take; the split view: files and folders, to the
// local pane's folder (it depends on that pane too: xpUpdateRail).
function explorerDownloadOff(tab, sel) {
  tab = xvView(tab);
  var ex = tab.ex, ok = ex.listed && !ex.fatal;
  sel = sel || explorerSelItems(tab);
  return xpLocalShown(tab) ? (!ok || !sel.length || !lpCanWrite(tab) || (ex.os === 'windows' && ex.path === ''))
    : (!ok || !sel.some(function (it) { return !it.isDir; }));
}
function explorerUpdateBar(tab) {
  tab = xvView(tab);
  var ex = tab.ex, ui = ex.ui, sel = explorerSelItems(tab), ok = ex.listed && !ex.fatal, write = explorerCanWrite(tab);
  ui.up.disabled = !ok || explorerIsTop(ex.path, ex.os);
  ui.mkdir.disabled = !write;
  ui.upload.disabled = !write;
  ui.download.disabled = explorerDownloadOff(tab, sel);
  ui.rename.disabled = !write || sel.length !== 1;
  ui.del.disabled = !write || !sel.length;
  usageUpdateButton(tab);
  if (ex.split) xpUpdateRail(tab);
}

// ── Mouse, keys ──
// (the selection's rules: local_pane.go paneSelClick, both panes)
function explorerClickRow(tab, it, e) { paneSelClick(tab.ex, it, e); explorerPaintSel(tab); }
// Open: a folder is listed, a file downloaded. A symbolic link (a Windows
// junction too) is listed first: the agent's isDir does not follow links,
// and most of them are folders (/bin, /lib on a recent Linux); a link to a
// file is then downloaded.
function explorerIsLink(it) { return !!it && typeof it.mode === 'string' && it.mode.charAt(0) === 'L'; }
async function explorerOpenItem(tab, it) {
  if (!it) return;
  if (it.isDir) { explorerList(tab, it.path); return; }
  if (explorerIsLink(it)) {
    if (await explorerList(tab, it.path, { quiet: true })) return;
    if (tab.ex.disposed) return;
  }
  // A file: what the operator chose for the double click (Settings ›
  // Sessions), whatever its format — opened in the editor (edit_page.go,
  // which itself downloads when local editing is unavailable) or downloaded.
  if (explorerDblAction() === 'open' && typeof editOpen === 'function') { editOpen(tab, it); return; }
  explorerDownload(tab, [it]);
}
// Double click on a file: 'open' (the editor, default) or 'download'.
var EXPLORER_DBL_KEY = 'oblireach_files_dblclick';
function explorerDblAction() {
  try { return localStorage.getItem(EXPLORER_DBL_KEY) === 'download' ? 'download' : 'open'; } catch (e) { return 'open'; }
}
function explorerSetDblAction(v) {
  try { localStorage.setItem(EXPLORER_DBL_KEY, v === 'download' ? 'download' : 'open'); } catch (e) {}
}
function explorerListKey(tab, e) {
  if (!e) return;
  var ex = tab.ex, k = String(e.key || ''), ctrl = !!(e.ctrlKey || e.metaKey);
  var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  // (a character: the filter; Backspace: the filter's last one while it has one)
  if (explorerPaneKey(tab, 'remote', e, true)) return;
  var sel = explorerSelItems(tab);
  // (Tab: the other list, as WinSCP; Shift+Tab leaves the list as anywhere
  // else; F6 goes round the lists and the dock: the explorer's keydown)
  if (k === 'Tab' && !ctrl && !e.altKey && !e.shiftKey && ex.split && xpLocalShown(tab)) { stop(); lpFocus(tab); return; }
  if (ctrl && ex.split && (e.code === 'KeyD' || k.toLowerCase() === 'd')) { stop(); explorerDownloadSel(tab); return; }
  if (k === 'Enter') { stop(); if (sel.length === 1) explorerOpenItem(tab, sel[0]); else if (sel.length) explorerDownloadSel(tab); return; }
  if (k === 'Backspace' || (k === 'ArrowUp' && e.altKey)) { stop(); explorerUp(tab); return; }
  if (k === 'Delete') { stop(); explorerDeleteSel(tab); return; }
  if (k === 'F2') { stop(); explorerRenameSel(tab); return; }
  if (k === 'Escape') { if (ex.sel.size) { stop(); ex.sel = new Set(); ex.anchor = ''; explorerPaintSel(tab); } return; }
  if (ctrl && (e.code === 'KeyA' || k.toLowerCase() === 'a')) { stop(); ex.sel = new Set(ex.view.map(function (x) { return x.path; })); explorerPaintSel(tab); return; }
  if ((k === 'ArrowDown' || k === 'ArrowUp' || k === 'Home' || k === 'End') && ex.view.length) {
    stop();
    var it = paneSelMove(ex, k, !!e.shiftKey);
    explorerPaintSel(tab);
    var tr = ex.rowEls.get(it.path);
    if (tr && typeof tr.scrollIntoView === 'function') { try { tr.scrollIntoView({ block: 'nearest' }); } catch (x) {} }
  }
}
function explorerFocus(tab) {
  tab = xvView(tab);
  if (tab && tab.ex && tab.ex.usage && tab.ex.usage.on) { usageFocus(tab); return; }
  var l = tab && tab.ex && tab.ex.ui.list;
  if (l && explorerShown(tab) && !tab.host.classList.contains('editing')) { try { l.focus(); } catch (e) {} }
}
function explorerRenderPane(tab, tb) {
  // The split view: its switch "Local pane" in the tool bar, its layout
  // (one above the other when narrow), the local pane started.
  var ex0 = tab.ex;
  if (ex0 && ex0.split) {
    if (tb && tb.actions) {
      var t = ex0.ui.splitToggle = document.createElement('button');
      t.className = 'btn-sm xp-toggle' + (xpLocalShown(tab) ? ' on' : ''); t.id = 'xp-toggle';
      t.innerHTML = LP_ICON.split + '<span>' + TH('local.pane') + '</span>';
      t.title = T('local.paneTitle'); t.setAttribute('aria-pressed', xpLocalShown(tab) ? 'true' : 'false');
      t.addEventListener('click', function () { xpSetLocalShown(tab, !xpLocalShown(tab)); });
      tb.actions.appendChild(t);
    }
    xpLayout(tab);
    xpWatchSize(tab);
    if (xpLocalShown(tab) && tab.lp && !tab.lp.started) lpStart(tab);
  }
  // Its own toolbar lives in its host (kept across tab switches; the tool
  // bar says where it is). Moved into the new page, its list starts at the
  // top again: back where it was (each view on screen: explorer_tabs.go).
  var go = function () {
    xvLayout(tab);
    explorerViews(tab).forEach(function (v) {
      var ex = v.ex, l = ex && ex.ui.list;
      if (l && xvIsShown(v) && ex.scrollTop && l.scrollTop !== ex.scrollTop) { try { l.scrollTop = ex.scrollTop; } catch (e) {} }
    });
    explorerFocus(tab);
  };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(go); else go();
}

// ── Context menu ──
var explorerMenuEl = null, explorerMenuWired = false;
function explorerMenuClose() {
  var m = explorerMenuEl;
  explorerMenuEl = null;
  if (m && m.parentElement && typeof m.parentElement.removeChild === 'function') { try { m.parentElement.removeChild(m); } catch (e) {} }
}
function explorerMenuOpen(tab, e, it) {
  explorerMenuClose();
  var ex = tab.ex, sel = explorerSelItems(tab), write = explorerCanWrite(tab), items = [];
  if (it && ex.split) {
    // The split view: a file is edited, the selection downloaded to the
    // local pane's folder.
    if (it.isDir || explorerIsLink(it)) items.push({ icon: EXPLORER_ICON.open, key: 'explorer.open', run: function () { explorerOpenItem(tab, it); } });
    // (editing: the main window's; a session window's split view has none)
    if (!it.isDir && localEditAvailable()) items.push({ icon: EDIT_ICON, key: 'edit.edit', label: T('edit.editWith', { editor: editEditorName() }), run: function () { editOpen(tab, it); } });
    if (xpLocalShown(tab)) items.push({ icon: EXPLORER_ICON.download, key: 'local.downloadTo', label: T('local.downloadTo', { path: tab.lp.path || T('local.thisPc') }),
      off: !sel.length || !lpCanWrite(tab) || (ex.os === 'windows' && ex.path === ''), run: function () { explorerDownloadSel(tab); } });
    else items.push({ icon: EXPLORER_ICON.download, key: 'explorer.download', off: !sel.some(function (x) { return !x.isDir; }), run: function () { explorerDownloadSel(tab); } });
    items.push({ icon: EXPLORER_ICON.rename, key: 'explorer.rename', off: !write || sel.length !== 1, run: function () { explorerRenameSel(tab); } });
    items.push({ icon: EXPLORER_ICON.copy, key: 'explorer.copyPath', run: function () { explorerCopyPath(it.path); } });
    if (it.isDir && explorerPasteAvailable()) items.push({ icon: EXPLORER_ICON.paste, key: 'explorer.paste', off: true, paste: it.path, run: function () { explorerPaste(tab, it.path); } });
    if (it.isDir && usageOffered(tab.device)) items.push({ icon: USAGE_ICON.analyze, key: 'usage.analyzeFolder', off: !usageAgentOk(tab.device), run: function () { usageOpen(tab, it.path, false); } });
    items.push({ icon: EXPLORER_ICON.trash, key: 'explorer.delete', off: !write || !sel.length, danger: true, run: function () { explorerDeleteSel(tab); } });
  } else if (it) {
    items.push({ icon: EXPLORER_ICON.open, key: 'explorer.open', run: function () { explorerOpenItem(tab, it); } });
    items.push({ icon: EXPLORER_ICON.download, key: 'explorer.download', off: !sel.some(function (x) { return !x.isDir; }), run: function () { explorerDownloadSel(tab); } });
    items.push({ icon: EXPLORER_ICON.rename, key: 'explorer.rename', off: !write || sel.length !== 1, run: function () { explorerRenameSel(tab); } });
    items.push({ icon: EXPLORER_ICON.copy, key: 'explorer.copyPath', run: function () { explorerCopyPath(it.path); } });
    if (it.isDir && explorerPasteAvailable()) items.push({ icon: EXPLORER_ICON.paste, key: 'explorer.paste', off: true, paste: it.path, run: function () { explorerPaste(tab, it.path); } });
    if (it.isDir && usageOffered(tab.device)) items.push({ icon: USAGE_ICON.analyze, key: 'usage.analyzeFolder', off: !usageAgentOk(tab.device), run: function () { usageOpen(tab, it.path, false); } });
    items.push({ icon: EXPLORER_ICON.trash, key: 'explorer.delete', off: !write || !sel.length, danger: true, run: function () { explorerDeleteSel(tab); } });
  } else {
    if (!ex.listed || ex.fatal) return;
    items.push({ icon: EXPLORER_ICON.newFolder, key: 'explorer.newFolder', off: !write, run: function () { explorerMkdir(tab); } });
    items.push({ icon: EXPLORER_ICON.upload, key: 'explorer.upload', off: !write, run: function () { if (ex.ui.file && typeof ex.ui.file.click === 'function') ex.ui.file.click(); } });
    if (explorerPasteAvailable()) items.push({ icon: EXPLORER_ICON.paste, key: 'explorer.paste', off: true, paste: write ? ex.path : null, run: function () { explorerPaste(tab); } });
    items.push({ icon: EXPLORER_ICON.refresh, key: 'common.refresh', run: function () { explorerRefresh(tab); } });
    if (ex.path) items.push({ icon: EXPLORER_ICON.copy, key: 'explorer.copyPath', run: function () { explorerCopyPath(ex.path); } });
    if (ex.path && usageOffered(tab.device)) items.push({ icon: USAGE_ICON.analyze, key: 'usage.analyzeFolder', off: !usageAgentOk(tab.device), run: function () { usageOpen(tab, ex.path, false); } });
  }
  explorerMenuShow(tab, e, items, function () { explorerFocus(tab); });
  // Paste: on once the clipboard is known to hold files or an image (asked
  // of Go; the menu is drawn at once).
  var pi = null;
  items.forEach(function (x) { if (x.key === 'explorer.paste') pi = x; });
  var m = explorerMenuEl;
  if (pi && pi.paste && m) explorerPasteKinds().then(function (k) {
    if (explorerMenuEl !== m || !(k.files || k.image)) return;
    Array.prototype.forEach.call(m.children, function (b) { if (b.dataset && b.dataset.key === 'explorer.paste') b.disabled = false; });
  });
}
// A context menu (the device's pane, the local pane): items { icon, key,
// label (a text of its own), off, danger, run }.
function explorerMenuShow(tab, e, items, onEscape) {
  explorerMenuClose();
  if (!items.length) return;
  var m = explorerEl('div', 'exp-menu', null, 'exp-menu'); m.setAttribute('role', 'menu');
  var first = null;
  items.forEach(function (x) {
    var b = explorerEl('button', x.danger ? 'danger' : '', m);
    b.setAttribute('role', 'menuitem');
    b.dataset.key = x.key;
    b.innerHTML = x.icon + '<span>' + (x.label ? i18nEsc(x.label) : TH(x.key)) + '</span>';
    if (x.off) b.disabled = true; else if (!first) first = b;
    b.addEventListener('click', function () { if (b.disabled) return; explorerMenuClose(); x.run(); });
  });
  m.addEventListener('keydown', function (ev) {
    if (!ev) return;
    if (ev.key === 'Escape') { if (ev.preventDefault) ev.preventDefault(); explorerMenuClose(); if (onEscape) onEscape(); }
  });
  var vw = (typeof window !== 'undefined' && window.innerWidth) || 1200, vh = (typeof window !== 'undefined' && window.innerHeight) || 800;
  var x0 = (e && typeof e.clientX === 'number') ? e.clientX : 40, y0 = (e && typeof e.clientY === 'number') ? e.clientY : 40;
  m.style.left = Math.max(4, Math.min(x0, vw - 200)) + 'px';
  m.style.top = Math.max(4, Math.min(y0, vh - 34 * items.length - 16)) + 'px';
  document.body.appendChild(m);
  explorerMenuEl = m;
  if (!explorerMenuWired) {
    explorerMenuWired = true;
    // (once for the page: a press anywhere else, the window's size, closes it)
    document.addEventListener('mousedown', function (ev) {
      var mm = explorerMenuEl;
      if (mm && !(ev && ev.target && typeof mm.contains === 'function' && mm.contains(ev.target))) explorerMenuClose();
    }, true);
    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      window.addEventListener('resize', explorerMenuClose);
      window.addEventListener('blur', explorerMenuClose);
    }
  }
  if (first) { try { first.focus(); } catch (x2) {} }
}
async function explorerCopyPath(p) {
  var ok = await reachNativeClipboardWrite(String(p || ''));
  reachNotify(T(ok ? 'term.copied' : 'term.clipboardFailed'), ok ? 'ok' : 'warn');
}

// ── Dialogs (the app's modals; never window.prompt / confirm) ──
// o: { title, text, value (an input), selectTo (its selection's end),
// ok (label key), danger, validate(v) -> '' or the error }. Resolves to the
// text typed (trimmed), true (a confirmation), or null (cancelled).
var explorerDlgDone = null, explorerDlgTab = null;
function explorerDialog(o) {
  if (explorerDlgDone) explorerDlgDone(null);
  explorerMenuClose();
  explorerDlgTab = o.tab || null;
  return new Promise(function (resolve) {
    var hasInput = typeof o.value === 'string';
    var ov = explorerEl('div', 'modal-overlay visible exp-dlg', null, 'exp-dlg');
    var box = explorerEl('div', 'modal-box', ov);
    box.setAttribute('role', o.danger ? 'alertdialog' : 'dialog'); box.setAttribute('aria-label', o.title);
    var head = explorerEl('div', 'modal-head', box);
    var ico = explorerEl('div', 'icon', head); ico.innerHTML = o.icon || EXPLORER_ICON.open;
    var h = explorerEl('h3', '', head); h.textContent = o.title;
    var x = explorerEl('button', 'close', head, 'exp-dlg-close'); x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    if (o.text) { var p = explorerEl('p', 'desc', box, 'exp-dlg-text'); p.textContent = o.text; }
    var input = null;
    if (hasInput) {
      input = explorerEl('input', 'exp-dlg-input', box, 'exp-dlg-input');
      input.type = 'text'; input.value = o.value; input.setAttribute('autocomplete', 'off'); input.setAttribute('spellcheck', 'false');
      input.setAttribute('maxlength', String(o.maxLength > 255 ? Math.min(o.maxLength, 32767) : 255)); input.setAttribute('aria-label', o.title);
    }
    var err = explorerEl('div', 'modal-err', box, 'exp-dlg-err'); err.setAttribute('role', 'alert');
    var acts = explorerEl('div', 'modal-actions', box);
    var cancel = explorerEl('button', 'btn-ghost', acts, 'exp-dlg-cancel'); cancel.textContent = T('common.cancel');
    var ok = explorerEl('button', o.danger ? 'exp-btn-danger' : 'btn-primary', acts, 'exp-dlg-ok'); ok.textContent = T(o.ok || 'common.ok');
    if (!o.danger) ok.style.cssText = 'padding:8px 18px;font-size:12px;border-radius:10px';
    var done = false;
    var finish = function (v) {
      if (done) return;
      done = true;
      if (explorerDlgDone === finish) { explorerDlgDone = null; explorerDlgTab = null; }
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    var submit = function () {
      if (!hasInput) { finish(true); return; }
      var v = String(input.value || '').trim();
      var bad = o.validate ? o.validate(v) : '';
      if (bad) { err.textContent = bad; try { input.focus(); } catch (e) {} return; }
      finish(v);
    };
    ok.addEventListener('click', submit);
    cancel.addEventListener('click', function () { finish(null); });
    x.addEventListener('click', function () { finish(null); });
    ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) finish(null); }); // the backdrop
    ov.addEventListener('keydown', function (e) {
      if (!e) return;
      // (never the page's reload behind a dialog: it would end this window's sessions)
      if (e.key === 'F5' || ((e.ctrlKey || e.metaKey) && String(e.key || '').toLowerCase() === 'r')) { if (e.preventDefault) e.preventDefault(); return; }
      if (e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); finish(null); }
      else if (e.key === 'Enter' && e.target !== cancel && e.target !== x) { if (e.preventDefault) e.preventDefault(); submit(); }
    });
    if (input) input.addEventListener('input', function () { err.textContent = ''; });
    explorerDlgDone = finish;
    document.body.appendChild(ov);
    try {
      if (input) {
        input.focus();
        var end = typeof o.selectTo === 'number' && o.selectTo > 0 ? o.selectTo : input.value.length;
        if (typeof input.setSelectionRange === 'function') input.setSelectionRange(0, end);
      } else (o.danger ? cancel : ok).focus();
    } catch (e) {}
  });
}
function explorerNameCheck(os) { return function (v) { return explorerValidName(v, os) ? '' : T('explorer.invalidName'); }; }

// ── Operations ──
async function explorerMkdir(tab) {
  var ex = tab.ex;
  if (!explorerCanWrite(tab)) return;
  var dir = ex.path;
  var name = await explorerDialog({ tab: tab, title: T('explorer.newFolder'), text: T('explorer.newFolderPrompt'), value: '', ok: 'explorer.create',
    icon: EXPLORER_ICON.newFolder, validate: explorerNameCheck(ex.os) });
  if (!name || ex.disposed) return;
  var path = explorerJoin(dir, name, ex.os);
  try { await explorerCmd(tab, 'create_directory', { path: path }, EXPLORER_SHORT_MS); }
  catch (e) { if (!ex.disposed && e.code !== 'cancelled') explorerSay(explorerErrText(e)); return; }
  if (ex.disposed) return;
  reachNotify(T('explorer.created', { name: name }), 'ok');
  if (ex.path === dir) explorerList(tab, dir, { select: path });
}
async function explorerRenameSel(tab) {
  var ex = tab.ex, sel = explorerSelItems(tab);
  if (!explorerCanWrite(tab) || sel.length !== 1) return;
  var it = sel[0], dir = ex.path, dot = it.isDir ? -1 : it.name.lastIndexOf('.');
  var name = await explorerDialog({ tab: tab, title: T('explorer.rename'), text: T('explorer.renamePrompt', { name: it.name }), value: it.name,
    selectTo: dot > 0 ? dot : it.name.length, ok: 'explorer.rename', icon: EXPLORER_ICON.rename, validate: explorerNameCheck(ex.os) });
  if (!name || name === it.name || ex.disposed) return;
  var to = explorerJoin(dir, name, ex.os);
  try { await explorerCmd(tab, 'rename_file', { oldPath: it.path, newPath: to }, EXPLORER_SHORT_MS); }
  catch (e) { if (!ex.disposed && e.code !== 'cancelled') explorerSay(explorerErrText(e)); return; }
  if (ex.disposed) return;
  reachNotify(T('explorer.renamed', { name: name }), 'ok');
  if (ex.path === dir) explorerList(tab, dir, { select: to });
}
// Delete: a folder goes with its contents (recursive) only after the
// confirmation that says so; files alone never carry it.
async function explorerDeleteSel(tab) {
  var ex = tab.ex, sel = explorerSelItems(tab);
  if (!explorerCanWrite(tab) || !sel.length) return;
  var text = sel.length > 1 ? T('explorer.deleteMany', { count: sel.length })
    : sel[0].isDir ? T('explorer.deleteFolderConfirm', { name: sel[0].name }) : T('explorer.deleteConfirm', { name: sel[0].name });
  var ok = await explorerDialog({ tab: tab, title: T('explorer.delete'), text: text, ok: 'explorer.delete', danger: true, icon: EXPLORER_ICON.trash });
  if (ok !== true || ex.disposed) return;
  var dir = ex.path, gone = 0;
  for (var i = 0; i < sel.length; i++) {
    var it = sel[i];
    try {
      await explorerCmd(tab, 'delete_file', it.isDir ? { path: it.path, recursive: true } : { path: it.path, recursive: false }, EXPLORER_SHORT_MS, sel.length - i);
      gone++;
    } catch (e) {
      if (ex.disposed || e.code === 'cancelled') return;
      explorerSay(sel.length > 1 ? T('viewer.tr.fileError', { name: it.name, error: explorerErrText(e) }) : explorerErrText(e));
      if (EXPLORER_BATCH_STOP.indexOf(e.code) >= 0) break;
    }
  }
  if (ex.disposed) return;
  if (gone === 1 && sel.length === 1) reachNotify(T('explorer.deleted', { name: sel[0].name }), 'ok');
  if (ex.path === dir) explorerList(tab, dir, { keepSel: true });
}

// ── Transfers ──
function explorerTrAdd(tab, dir, name, size) {
  var ex = tab.ex;
  var t = { id: ++ex.trSeq, dir: dir, name: String(name || ''), size: size || 0, state: 'active', error: '' };
  ex.transfers.push(t);
  // (the oldest finished ones go first)
  while (ex.transfers.length > EXPLORER_MAX_TRANSFERS) {
    var i = -1;
    for (var j = 0; j < ex.transfers.length; j++) if (ex.transfers[j].state !== 'active') { i = j; break; }
    if (i < 0) break;
    ex.transfers.splice(i, 1);
  }
  explorerRenderTransfers(tab);
  return t;
}
function explorerTrSet(tab, t, state, error) { t.state = state; t.error = error || ''; explorerRenderTransfers(tab); }
function explorerRenderTransfers(tab) {
  var ex = tab.ex, el = ex.ui.tr;
  if (!el) return;
  el.innerHTML = '';
  if (!ex.transfers.length) { el.style.display = 'none'; return; }
  el.style.display = '';
  var head = explorerEl('div', 'exp-tr-head', el);
  var ht = explorerEl('span', '', head); ht.textContent = T('viewer.tr.title');
  explorerEl('span', 'sp', head);
  var clr = explorerEl('button', 'exp-tr-x', head, 'exp-tr-clear'); clr.title = T('viewer.tr.clearFinished'); clr.textContent = '\u2715';
  clr.addEventListener('click', function () { ex.transfers = ex.transfers.filter(function (t) { return t.state === 'active'; }); explorerRenderTransfers(tab); });
  ex.transfers.forEach(function (t) {
    var row = explorerEl('div', 'exp-tr-row ' + t.state, el);
    var d = explorerEl('span', 'exp-tr-dir', row); d.textContent = t.dir === 'up' ? '\u2191' : '\u2193';
    var nm = explorerEl('span', 'exp-tr-name', row); nm.textContent = t.name; nm.title = t.name;
    if (t.state === 'active') explorerEl('span', 'exp-tr-bar', row);
    var st = explorerEl('span', 'exp-tr-state', row);
    st.textContent = t.state === 'active' ? T(t.dir === 'up' ? 'explorer.tr.uploading' : 'explorer.tr.downloading') + (t.size ? ' \u00B7 ' + reachFmtBytes(t.size) : '') + (t.pct >= 0 ? ' \u00B7 ' + T('info.percent', { n: t.pct }) : '')
      : t.state === 'done' ? T('viewer.tr.done') : t.state === 'cancelled' ? T('viewer.tr.cancelled') : (t.error || T('viewer.tr.failed'));
    if (t.error) st.title = t.error;
    // (a file the app saves itself: stopped from here, sftp_save.go)
    if (t.state === 'active' && typeof t.cancel === 'function') {
      var cx = explorerEl('button', 'exp-tr-x', row); cx.title = T('common.cancel'); cx.setAttribute('aria-label', cx.title); cx.textContent = '\u2715';
      cx.addEventListener('click', function () { t.cancel(); });
    }
  });
}
function explorerDownloadSel(tab) {
  tab = xvView(tab);
  // The split view: files and folders, to the local pane's folder (the queue).
  if (xpLocalShown(tab)) {
    var sel = explorerSelItems(tab);
    if (!sel.length || (tab.ex.os === 'windows' && tab.ex.path === '')) return;
    if (!lpCanWrite(tab)) { explorerSay(T('local.pickLocalFolder')); return; }
    xferDownloadItems(tab, sel, tab.lp.path);
    return;
  }
  var files = explorerSelItems(tab).filter(function (it) { return !it.isDir; });
  if (!files.length) { if (explorerSelItems(tab).length) explorerSay(T('explorer.noFolderDownload')); return; }
  explorerDownload(tab, files);
}
// A local SSH connection's file saved by the app itself in Downloads
// (sftp_save.go): streamed to the disk there, any size, never through this
// page; its progress polled (t: its row). Resolves to the name it was saved
// under (an existing file is never replaced: "name (2).ext").
var EXPLORER_SAVE_POLL_MS = 400;
async function explorerStreamSave(tab, it, t) {
  var o = await explorerCmd(tab, 'save_open', { path: it.path, name: it.name }, EXPLORER_SHORT_MS);
  var job = (o && typeof o.job === 'string') ? o.job : '', size = Number(o && o.size) || 0, stop = false;
  if (!job) throw explorerErr({ k: 'explorer.err.badData' }, '');
  t.pct = 0;
  t.cancel = function () { stop = true; explorerCmd(tab, 'save_cancel', { job: job }, EXPLORER_SHORT_MS).catch(function () {}); };
  explorerRenderTransfers(tab);
  try {
    for (;;) {
      if (tab.ex.disposed && !stop) t.cancel();
      await new Promise(function (r) { setTimeout(r, EXPLORER_SAVE_POLL_MS); });
      var s = await explorerCmd(tab, 'save_status', { job: job }, EXPLORER_SHORT_MS);
      var state = s && s.state;
      if (state === 'done') return String(s.name || it.name);
      if (state === 'cancelled') throw explorerErr('', 'cancelled');
      if (state === 'error') throw explorerErr(String(s.error || '').slice(0, 300) || { k: 'viewer.tr.failed' }, '');
      var got = Number(s && s.received) || 0;
      var pct = size > 0 ? Math.min(99, Math.floor(got / size * 100)) : -1;
      if (pct !== t.pct) { t.pct = pct; explorerRenderTransfers(tab); }
    }
  } finally { t.cancel = null; t.pct = -1; }
}
// A file to a local SSH connection (sftp_stream.go), in chunks; stopped,
// its partial file goes.
async function explorerStreamUp(tab, f, path, overwrite) {
  // (its size first: a server without SFTP takes it by SCP, scp.go)
  var o = await explorerCmd(tab, 'upload_open', { path: path, overwrite: !!overwrite, size: Number(f.size) || 0 }, EXPLORER_SHORT_MS);
  var h = (o && typeof o.handle === 'string') ? o.handle : '', size = Number(f.size) || 0, done = false;
  if (!h) throw explorerErr({ k: 'explorer.err.badData' }, '');
  try {
    for (var off = 0; off < size; off += XFER_CHUNK) {
      if (tab.ex.disposed) throw explorerErr('', 'cancelled');
      var data;
      try {
        if (typeof f.readB64 === 'function') data = await f.readB64(off, Math.min(XFER_CHUNK, size - off)); // (a pasted file)
        else data = explorerBytesToB64(new Uint8Array(await f.slice(off, Math.min(size, off + XFER_CHUNK)).arrayBuffer()));
      } catch (e) { throw explorerErr({ k: 'viewer.tr.readFailed' }, ''); }
      await explorerCmd(tab, 'upload_write', { handle: h, offset: off, data: data }, EXPLORER_LONG_MS);
    }
    await explorerCmd(tab, 'upload_close', { handle: h }, EXPLORER_LONG_MS);
    done = true;
  } finally { if (!done) explorerCmd(tab, 'upload_abort', { handle: h }, EXPLORER_SHORT_MS).catch(function () {}); }
}
// Each file whole (base64 in the agent's answer), saved where the app saves
// (reachSaveBlob): 100 MB at most, checked on its listed size first (a
// local SSH connection: saved to Downloads by the app itself, streamed, no
// such limit: explorerStreamSave).
async function explorerDownload(tab, files) {
  var ex = tab.ex, max = reachFmtBytes(EXPLORER_MAX_BYTES), streams = devIsLocal(tab.device);
  for (var i = 0; i < files.length; i++) {
    var it = files[i];
    if (it.isDir) { explorerSay(T('explorer.noFolderDownload')); continue; }
    if (!streams && it.size > EXPLORER_MAX_BYTES) { explorerSay(T('viewer.file.tooLargeMax', { name: it.name, max: max })); continue; }
    var t = explorerTrAdd(tab, 'down', it.name, it.size);
    var res;
    if (streams) {
      var saved = '';
      try { saved = await explorerStreamSave(tab, it, t); }
      catch (e) {
        if (ex.disposed) return;
        if (e.code === 'cancelled') { explorerTrSet(tab, t, 'cancelled'); continue; }
        var smsg = explorerErrText(e);
        explorerTrSet(tab, t, 'error', smsg);
        explorerSay(files.length > 1 ? T('viewer.tr.fileError', { name: it.name, error: smsg }) : smsg);
        continue;
      }
      explorerTrSet(tab, t, 'done');
      reachNotify(T('explorer.tr.savedDownloads', { name: saved }), 'ok');
      continue;
    }
    try { res = await explorerCmd(tab, 'download_file', { path: it.path }, EXPLORER_LONG_MS, files.length - i); }
    catch (e) {
      if (ex.disposed || e.code === 'cancelled') return;
      var msg = explorerErrText(e);
      explorerTrSet(tab, t, 'error', msg);
      explorerSay(files.length > 1 ? T('viewer.tr.fileError', { name: it.name, error: msg }) : msg);
      if (EXPLORER_BATCH_STOP.indexOf(e.code) >= 0) return;
      continue;
    }
    if (ex.disposed) return;
    var b64 = (res && typeof res.data === 'string') ? res.data : '';
    var bytes;
    try { bytes = reachB64ToBytes(b64); } catch (e) { explorerTrSet(tab, t, 'error', T('explorer.err.badData')); explorerSay(T('explorer.err.badData')); continue; }
    reachSaveBlob(new Blob([bytes], { type: 'application/octet-stream' }), reachSafeFileName(it.name));
    explorerTrSet(tab, t, 'done');
    reachNotify(T('viewer.tr.downloaded', { name: it.name }), 'ok');
  }
}
// Files dropped (folders left out: said).
function explorerDropped(tab, dt) {
  if (!explorerCanWrite(tab)) return;
  var files = [], skipped = 0, items = dt && dt.items;
  if (items && items.length) {
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      if (!it || it.kind !== 'file') continue;
      var entry = typeof it.webkitGetAsEntry === 'function' ? it.webkitGetAsEntry() : null;
      if (entry && entry.isDirectory) { skipped++; continue; }
      var f = it.getAsFile();
      if (f) files.push(f);
    }
  } else if (dt && dt.files) files = Array.prototype.slice.call(dt.files);
  if (skipped) explorerSay(T('viewer.file.foldersSkipped'));
  if (files.length) explorerUpload(tab, files);
}
// Files sent to the folder on screen, one after the other: 100 MB at most
// (checked before reading), an existing name asked first (then replaced).
async function explorerUpload(tab, files, into) {
  var ex = tab.ex;
  if (!into && !explorerCanWrite(tab)) return;
  var dir = into || ex.path, max = reachFmtBytes(EXPLORER_MAX_BYTES), sent = 0, stop = false, streams = devIsLocal(tab.device);
  for (var i = 0; i < files.length && !stop; i++) {
    var f = files[i], name = String((f && f.name) || '');
    if (!f) continue;
    if (!explorerValidName(name, ex.os)) { explorerSay(T('viewer.tr.fileError', { name: name, error: T('explorer.invalidName') })); continue; }
    if (!streams && Number(f.size) > EXPLORER_MAX_BYTES) { explorerSay(T('viewer.file.tooLargeMax', { name: name, max: max })); continue; }
    var t = explorerTrAdd(tab, 'up', name, Number(f.size) || 0);
    var b64 = '';
    // (a local SSH connection: read and sent in chunks, explorerStreamUp)
    if (!streams) {
      try { b64 = await explorerReadB64(f); }
      catch (e) { explorerTrSet(tab, t, 'error', T('viewer.tr.readFailed')); explorerSay(T('viewer.tr.fileError', { name: name, error: T('viewer.tr.readFailed') })); continue; }
    }
    if (ex.disposed) return;
    var path = explorerJoin(dir, name, ex.os), overwrite = false;
    for (;;) {
      try {
        if (streams) await explorerStreamUp(tab, f, path, overwrite);
        else await explorerCmd(tab, 'upload_file', { path: path, data: b64, overwrite: overwrite }, EXPLORER_LONG_MS, files.length - i);
        explorerTrSet(tab, t, 'done');
        sent++;
        if (files.length === 1) reachNotify(T('viewer.tr.sent', { name: name }), 'ok');
      } catch (e) {
        if (ex.disposed || e.code === 'cancelled') return;
        if (!overwrite && !e.code && /already exists/i.test(e.message)) {
          var yes = await explorerDialog({ tab: tab, title: T('explorer.upload'), text: T('explorer.exists', { name: name }), ok: 'explorer.replace', danger: true, icon: EXPLORER_ICON.upload });
          if (ex.disposed) return;
          if (yes === true) { overwrite = true; continue; }
          explorerTrSet(tab, t, 'cancelled');
          break;
        }
        var msg = explorerErrText(e);
        explorerTrSet(tab, t, 'error', msg);
        explorerSay(files.length > 1 ? T('viewer.tr.fileError', { name: name, error: msg }) : msg);
        if (EXPLORER_BATCH_STOP.indexOf(e.code) >= 0) stop = true;
      }
      break;
    }
    b64 = '';
  }
  if (ex.disposed) return;
  if (sent && ex.path === dir) explorerList(tab, dir, { keepSel: true });
}

// ── The split view's drops on the device's pane ──
// The folder row under the pointer of a drag (a drop there goes in it).
function explorerRowPathAt(tab, e) {
  var n = e && e.target, ex = tab.ex;
  for (var i = 0; n && i < 8; i++, n = n.parentElement) {
    if (n.tagName === 'TR' && n.dataset && n.dataset.path) {
      var it = null;
      ex.view.forEach(function (x) { if (x.path === n.dataset.path) it = x; });
      return it && it.isDir ? it.path : null;
    }
  }
  return null;
}
// count: the local pane's elements dragged (-1: the operator's files).
function explorerDropTarget(tab, rowPath, on, count) {
  var ex = tab.ex;
  if (ex.dropPath && ex.rowEls.get(ex.dropPath)) ex.rowEls.get(ex.dropPath).classList.remove('drop-row');
  ex.dropPath = rowPath || null;
  if (ex.dropPath && ex.rowEls.get(ex.dropPath)) ex.rowEls.get(ex.dropPath).classList.add('drop-row');
  var to = rowPath || ex.path;
  ex.ui.drop.textContent = !on ? T('explorer.dropHint') : count >= 0 ? T('local.uploadDropHint', { path: to }) : T('local.dropFilesHint', { path: to });
  ex.ui.drop.classList.toggle('visible', !!on);
}
// The split view's size: its layout follows (one above the other when narrow).
var xpObserved = new WeakSet(), xpResizeWired = false;
function xpWatchSize(tab) {
  if (xpObserved.has(tab.host)) return;
  xpObserved.add(tab.host);
  if (typeof ResizeObserver === 'function') {
    try { var ro = new ResizeObserver(function () { xpLayout(tab); }); ro.observe(tab.host); tab.ex.ui.ro = ro; return; } catch (e) {}
  }
  if (!xpResizeWired && typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
    xpResizeWired = true;
    window.addEventListener('resize', function () { toolTabs.forEach(function (t) { if (t && t.ex && t.ex.split) xpLayout(t); }); });
  }
}

// ── Closing ──
function explorerDispose(tab) {
  var ex = tab.ex;
  if (!ex) return;
  // A tab: its views first (those closed whose commands went on too:
  // explorer_tabs.go), then what they share.
  var view = !!tab.xvOwner;
  if (!view && tab.xv) {
    xvList(tab).concat(tab.xv.gone).forEach(function (v) { explorerDispose(v); });
    tab.xv.gone = [];
    if (ex.ui && ex.ui.xvRo) { try { ex.ui.xvRo.disconnect(); } catch (e) {} ex.ui.xvRo = null; }
  }
  ex.disposed = true;
  usageDispose(tab);
  if (xpDrag && (view ? xpDrag.view === tab : xpDrag.tabId === tab.id)) xpDragEnd();
  if (!view && ex.ui && ex.ui.ro) { try { ex.ui.ro.disconnect(); } catch (e) {} ex.ui.ro = null; }
  var pids = {};
  explorerPending.forEach(function (p, id) {
    if (p.tab !== tab) return;
    explorerPending.delete(id);
    clearTimeout(p.timer);
    pids[p.pid] = true;
    p.reject(explorerErr('', 'cancelled'));
  });
  explorerMenuClose();
  if (explorerDlgDone && explorerDlgTab === tab) explorerDlgDone(null);
  Object.keys(pids).forEach(explorerSocketIdle);
  if (!tab.ctx && !view && devIsLocal(tab.device)) cxnSftpRelease(tab); // (its SFTP session: localconn_tools.go)
}
// ── Typing in the void: the filter ──
// A key typed while the keyboard is in an explorer's list (or on one of its
// buttons, or nowhere: explorerDocKey) and not in a field: the character
// goes at the end of that pane's filter ("Filter this folder"), which takes
// the keyboard; Backspace in the list takes the last one back (up a level
// once the filter is empty); Escape in the filter clears it and goes back to
// the list. Never a shortcut (Ctrl / Alt / Meta, F-keys, Enter, arrows,
// Delete, Tab), nor a composition (IME, dead keys), nor while a dialog or a
// menu is open. The split view: the pane the key is in, else the one used
// last (ex.focusSide).
// The character a key types ('' : none). AltGr (Ctrl+Alt on Windows) types
// characters on many layouts: allowed.
function explorerTypedChar(e) {
  if (!e || e.isComposing || e.keyCode === 229) return '';
  var k = typeof e.key === 'string' ? e.key : '';
  if (!k || Array.from(k).length !== 1 || /[\u0000-\u001f\u007f-\u009f]/.test(k)) return '';
  var altGr = false;
  try { altGr = typeof e.getModifierState === 'function' && !!e.getModifierState('AltGraph'); } catch (x) { altGr = false; }
  if (e.metaKey || ((e.ctrlKey || e.altKey) && !altGr)) return '';
  return k;
}
// The keyboard is in a field (or a terminal, a picture, a menu, a dialog):
// its keys are its own.
function explorerKeyInField(t) {
  if (!t) return false;
  var tag = String(t.tagName || '').toUpperCase();
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || tag === 'CANVAS' || tag === 'IFRAME' || tag === 'VIDEO') return true;
  if (t.isContentEditable === true) return true;
  var role = typeof t.getAttribute === 'function' ? String(t.getAttribute('role') || '') : '';
  if (/^(textbox|searchbox|combobox|listbox|option|menu|menuitem|slider|spinbutton)$/.test(role)) return true;
  try { if (typeof t.closest === 'function' && t.closest('.xterm,.exp-menu,.modal-overlay,[contenteditable="true"]')) return true; } catch (x) {}
  return false;
}
// A control a Space presses.
function explorerKeyOnControl(t) {
  var tag = String((t && t.tagName) || '').toUpperCase();
  var role = (t && typeof t.getAttribute === 'function') ? String(t.getAttribute('role') || '') : '';
  return tag === 'BUTTON' || tag === 'A' || role === 'button' || role === 'tab' || role === 'checkbox' || role === 'switch';
}
// A dialog or a menu of the page is open.
function explorerKeyBusy() {
  if (explorerMenuEl || explorerDlgDone) return true;
  if (typeof xferDlgDone !== 'undefined' && xferDlgDone) return true;
  if (typeof oddOpenSt !== 'undefined' && oddOpenSt) return true;
  try { if (document.querySelector('.modal-overlay.visible')) return true; } catch (x) {}
  return false;
}
// A pane's filter: { input, apply(value) } (null: none to type in now).
function explorerFilterOf(tab, side) {
  tab = xvView(tab);
  var ex = tab && tab.ex;
  if (!ex || ex.disposed) return null;
  if (side === 'local') {
    var lp = tab.lp;
    if (!lp || !lp.ui.filter || !xpLocalShown(tab) || lp.ui.pane.classList.contains('editing')) return null;
    return { input: lp.ui.filter, apply: function (v) { lp.filter = v; lpRenderList(tab); } };
  }
  if (!ex.ui.filter || !ex.listed || ex.fatal || (ex.usage && ex.usage.on) || tab.host.classList.contains('editing')) return null;
  return { input: ex.ui.filter, apply: function (v) { ex.filter = v; explorerRenderList(tab); } };
}
function explorerFilterFocusEnd(inp) {
  try { inp.focus(); } catch (x) {}
  var n = String(inp.value || '').length;
  try { if (typeof inp.setSelectionRange === 'function') inp.setSelectionRange(n, n); } catch (x) {}
}
// A key of a pane (its list: inList; one of its buttons; the page itself).
// true: taken (into the filter).
function explorerPaneKey(tab, side, e, inList) {
  if (!e || e.defaultPrevented || explorerKeyInField(e.target) || explorerKeyBusy()) return false;
  var f = explorerFilterOf(tab, side);
  if (!f) return false;
  var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  var cur = String(f.input.value || ''), ch = explorerTypedChar(e);
  if (ch) {
    // (Space presses a button; a filter does not start with it)
    if (ch === ' ' && (!cur || explorerKeyOnControl(e.target))) return false;
    stop();
    f.input.value = cur + ch;
    f.apply(f.input.value);
    explorerFilterFocusEnd(f.input);
    return true;
  }
  if (inList && cur && e.key === 'Backspace' && !e.ctrlKey && !e.altKey && !e.metaKey && !e.shiftKey) {
    stop();
    var cs = Array.from(cur); cs.pop();
    f.input.value = cs.join('');
    f.apply(f.input.value);
    explorerFilterFocusEnd(f.input);
    return true;
  }
  return false;
}
// Escape in a pane's filter: cleared, the keyboard back to the list (empty:
// as before, nothing).
function explorerFilterEscape(tab, side, e) {
  var inp = side === 'local' ? (tab.lp && tab.lp.ui.filter) : tab.ex.ui.filter;
  if (!e || e.key !== 'Escape' || !inp || !inp.value) return;
  if (e.preventDefault) e.preventDefault();
  if (e.stopPropagation) e.stopPropagation();
  inp.value = '';
  if (side === 'local') { tab.lp.filter = ''; lpRenderList(tab); lpFocus(tab); }
  else { tab.ex.filter = ''; explorerRenderList(tab); explorerFocus(tab); }
}
// The pane a key of the explorer's host belongs to (the split view).
function explorerKeySide(tab, t) {
  var ex = tab.ex;
  if (!ex.split || !xpLocalShown(tab)) return 'remote';
  var inside = function (el) { try { return !!el && !!t && typeof el.contains === 'function' && el.contains(t); } catch (x) { return false; } };
  if (inside(tab.lp.ui.pane)) return 'local';
  if (inside(ex.ui.rpane)) return 'remote';
  return ex.focusSide === 'local' ? 'local' : 'remote';
}
function explorerIsPasteKey(e) {
  if (!e || !(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey) return false;
  return e.code === 'KeyV' || String(e.key || '').toLowerCase() === 'v';
}
// The explorer whose page has the keyboard when no element does (the Files
// page's explorer in front; a session window's tab in front, the focused
// cell of a mosaic).
function explorerKeyTab() {
  var t = (typeof filesPageShown === 'function' && filesPageShown()) ? filesActiveTab() : toolActiveTab();
  return (t && t.kind === 'files' && t.ex && !t.ex.disposed && toolShown(t)) ? t : null;
}
// The page's keys with the keyboard nowhere (a click on an empty part of it).
function explorerDocKey(e) {
  if (!e || e.defaultPrevented) return;
  var t = e.target, d = (typeof document !== 'undefined') ? document : null;
  if (t && d && t !== d.body && t !== d.documentElement && t !== d) return;
  var tab = xvView(explorerKeyTab()); // (its view in focus: explorer_tabs.go)
  if (!tab) return;
  var side = explorerKeySide(tab, null);
  if (explorerIsPasteKey(e)) {
    if (side !== 'remote' || explorerKeyBusy()) return;
    if (e.preventDefault) e.preventDefault();
    explorerPaste(tab);
    return;
  }
  explorerPaneKey(tab, side, e, true);
}
if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') document.addEventListener('keydown', explorerDocKey);

// ── Ctrl+V: the operator's clipboard into the device's folder ──
// Read by Go (clipboard_paste*.go: files and folders copied in Windows
// Explorer, else an image alone), then sent like a drop of them: the
// transfer queue of the split view (folders included, the conflicts asked,
// a confirmation above 500 files or 1 GiB), the upload of a session
// window's explorer (files; folders left out, said). Files "cut" in
// Explorer are copied all the same (nothing local is ever deleted).
function explorerPasteAvailable() { return typeof window.reachClipboardPaste === 'function' && !!reachNativeToken(); }
// What the clipboard would paste now: { files, image }.
async function explorerPasteKinds() {
  if (typeof window.reachClipboardPasteKinds !== 'function') return { files: false, image: false };
  try { var k = await reachNative('reachClipboardPasteKinds'); return { files: !!(k && k.files), image: !!(k && k.image) }; }
  catch (e) { return { files: false, image: false }; }
}
// A pasted file, as the transfers read a File (its name, size, date, slices;
// readB64: its base64 at once). Its bytes through reachReadFileChunk.
function explorerClipFile(handle, name, size, modified) {
  var read = function (off, n) {
    if (!(n > 0)) return Promise.resolve('');
    return reachNative('reachReadFileChunk', handle, off, n).then(function (b64) { return String(b64 || ''); });
  };
  var t = Date.parse(modified || '');
  return { name: String(name || ''), size: Number(size) || 0, lastModified: isFinite(t) ? t : Date.now(), clipHandle: handle, readB64: read,
    slice: function (a, b) {
      return { arrayBuffer: function () {
        return read(a, Math.max(0, b - a)).then(function (s) { var u = reachB64ToBytes(s); return u.buffer.slice(u.byteOffset, u.byteOffset + u.byteLength); });
      } };
    } };
}
// A file whole, base64 (the non-split upload): its chunks joined.
async function explorerClipB64(f) {
  var parts = [];
  for (var off = 0; off < f.size; off += XFER_CHUNK) parts.push(await f.readB64(off, Math.min(XFER_CHUNK, f.size - off)));
  return parts.join('');
}
// Ctrl+V (or the menu's Paste): to dir (the folder on screen by default).
async function explorerPaste(tab, dir) {
  tab = xvView(tab);
  var ex = tab && tab.ex;
  if (!ex || ex.disposed || !explorerPasteAvailable() || ex.pasting) return null;
  var into = typeof dir === 'string' && dir ? dir : ex.path;
  if (!(typeof dir === 'string' && dir ? (ex.listed && !ex.fatal) : explorerCanWrite(tab))) {
    if (ex.listed && !ex.fatal) explorerSay(T('explorer.pasteNoFolder'));
    return null;
  }
  var dev = tab.device, q = ex.split ? xferQueueFor(dev) : null, res = null;
  ex.pasting = true;
  if (q) { q.planning++; xferChanged(q); }
  try { res = await localFs('reachClipboardPaste', xferStreams(dev)); }
  catch (e) { res = null; if (!ex.disposed) explorerSay(T('explorer.pasteFailed')); }
  finally { ex.pasting = false; if (q) { q.planning = Math.max(0, q.planning - 1); xferChanged(q); } }
  if (!res || ex.disposed || !Array.isArray(res.items)) return null;
  var sk = res.skipped || {};
  if (res.kind === 'image') return explorerPasteImage(tab, res, into);
  if (res.kind !== 'files') return null; // (text, nothing: the explorer lets it be)
  if (res.limit) { reachNotify(T('xfer.limit'), 'warn'); return null; }
  var items = res.items.map(function (w) {
    return { rel: String(w.rel || ''), isDir: !!w.isDir, size: Number(w.size) || 0, modified: typeof w.modified === 'string' ? w.modified : '',
      file: w.isDir ? null : explorerClipFile(String(w.handle || ''), String(w.rel || '').split('/').pop(), w.size, w.modified) };
  });
  var links = Number(sk.links) || 0, tooLarge = Number(sk.tooLarge) || 0, denied = Number(sk.denied) || 0;
  if (ex.split) {
    var plan = xferPlanUpload(items, into, ex.os, function (w) { return { kind: 'file', file: w.file }; });
    xferSaySkipped(links, tooLarge, plan.invalid, denied);
    if (!plan.items.length) return null;
    if (!(await xferConfirmBig(plan.files, plan.bytes, into))) return null;
    if (ex.disposed) return null;
    return xferStart(dev, 'up', { origin: 'explorer', dstDir: into, os: ex.os, skipped: tooLarge + plan.invalid }, plan.items);
  }
  // A session window's explorer: the files (as a drop: folders left out).
  var files = items.filter(function (w) { return !w.isDir && w.rel.indexOf('/') < 0; }).map(function (w) { return w.file; });
  if (items.some(function (w) { return w.isDir; })) explorerSay(T('viewer.file.foldersSkipped'));
  else xferSaySkipped(links, tooLarge, 0, denied);
  if (!files.length) return null;
  var bytes = files.reduce(function (s, f) { return s + f.size; }, 0);
  if (!(await xferConfirmBig(files.length, bytes, into))) return null;
  if (ex.disposed) return null;
  return explorerUpload(tab, files, into);
}
// An image alone (a screenshot): offered as "Clipboard <date> <time>.png",
// its name editable.
async function explorerPasteImage(tab, res, into) {
  var ex = tab.ex, w = res.items[0];
  if (!w || !w.handle) return null;
  var def = String(w.rel || 'Clipboard.png'), dot = def.lastIndexOf('.');
  var name = await explorerDialog({ tab: tab, title: T('explorer.pasteImageTitle'), text: T('explorer.pasteImageText', { size: reachFmtBytes(Number(w.size) || 0), path: into }),
    value: def, selectTo: dot > 0 ? dot : def.length, ok: 'explorer.paste', icon: EXPLORER_ICON.paste, validate: explorerNameCheck(ex.os) });
  if (typeof name !== 'string' || !name || ex.disposed) return null;
  var f = explorerClipFile(String(w.handle), name, w.size, w.modified);
  if (ex.split) {
    var plan = xferPlanUpload([{ rel: name, isDir: false, size: f.size, modified: w.modified || '', file: f }], into, ex.os, function (x) { return { kind: 'file', file: x.file }; });
    if (!plan.items.length) return null;
    return xferStart(tab.device, 'up', { origin: 'explorer', dstDir: into, os: ex.os, skipped: plan.invalid }, plan.items);
  }
  return explorerUpload(tab, [f], into);
}
` + explorerTabsJS // (its sub-tabs and their mosaic: explorer_tabs.go)
