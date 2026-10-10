package main

// global_search.go — the main window's global search: a field in the top bar,
// next to the server and tenant menus, on every page (Dashboard, Devices, VMs,
// a device's sessions...). A machine's name typed there lists the matching
// devices and Hyper-V VMs in a drop-down under it.
//
// What it searches (the owner's rule: never a machine the lists do not
// already show this user):
//   - the devices: the main window's overview (ui.go overview.groups), exactly
//     what the Devices page and the Dashboard list: single mode, the active
//     server's session tenant (the god view's every tenant on the master
//     tenant, as the server sends it); multi-server mode, the servers and
//     tenants the tenant menu leaves (overviewRebuild). Never a server's raw
//     list (srv groups), never a request of its own;
//   - the VMs: the VMs page's list (vms_page.go vmsAll: its servers, tenants,
//     hosts and 'hyperv:view' gates), read again when stale by vmsKick (its
//     own TTL) as the drop-down opens. No new server endpoint, no other read;
//   - the local base's connections (the vault's SSH / RDP entries, imported
//     ones from WinSCP / 1Remote included: ui.go localDevGroups, the very
//     devices the Devices page lists for the base, folders as groups). They
//     belong to no server nor tenant: searched in every scope (single mode
//     on a server or on the base, "All servers", any tenant), as soon as a
//     local base exists. Only while the base is read and unlocked
//     (localConns.state 'ok', the state the Devices page lists them in):
//     locked, nothing of it, a "Local base locked" row instead whose Unlock
//     is the page's own (connections_page.go cxnUnlockThen: the vault's
//     form, Windows Hello included); absent / unreadable, nothing. What is
//     matched and shown is what the page shows (label, host, port, user,
//     folder path, type): never a secret (cxnDevice keeps none). Read again
//     as the drop-down opens when older than GS_LOCAL_TTL_MS (the lists
//     follow: overviewRedraw).
// Oblireach Support sessions and session windows are left out: the field is
// not drawn in a session window (no top bar there).
//
// A row opens what the same machine's row opens in its own list: a device,
// its row in the Devices page (ui.go devicesOpen(dev): its page and Remote
// session, or a session window with "Detach by default"); its buttons are the
// Devices page row's ▶ (devicesOpen 'remote'), CMD / PS / SSH and Files
// (agent_tools.go toolState / toolOpen through devicesOpen: the "Files opens
// as" setting, privacy, rights, greyed with their reason); a VM, its console
// (vms_page.go vmsOpenOne), greyed with its reason when it cannot open; a
// local connection, what its row in the Devices page opens (ui.go
// devicesOpen -> localDevOpen: SSH its terminal, RDP its view), its buttons
// that row's (localRowButtons: ▶, SSH and Files for an SSH connection, ▶
// alone for an RDP one; ▶ greyed when its host was found unreachable).
//
// Keyboard: Ctrl+K (Cmd+K on a Mac) puts the focus in the field, unless the
// key belongs to a remote picture or a terminal (they cancel it: the key goes
// to the machine); ↑ / ↓ move, Enter opens, Tab / Shift+Tab pick a row's
// button (Enter then runs it), Escape closes (then clears the field).
//
// Both consts are injected verbatim after vms_page.go's (ui.go buildUI, not
// through Sprintf: '%' is safe); no backtick here.

const globalSearchCSS = `
/* ── Global search (global_search.go): the top bar's field and its drop-down,
   in the tenant menu's visual language (surface, radius, shadow, rows) ── */
.gs{position:relative;flex:0 1 260px;min-width:150px;display:flex;align-items:center}
.gs.hide{display:none}
.gs-field{display:flex;align-items:center;gap:7px;width:100%;height:28px;padding:0 5px 0 10px;border-radius:10px;background:rgb(var(--ink) / .06);color:var(--muted);cursor:text;transition:background .15s,box-shadow .15s,color .15s}
.gs-field:hover{background:rgb(var(--ink) / .1)}
.gs-field:focus-within{background:var(--input-bg);color:var(--text);box-shadow:0 0 0 2px var(--accent)}
.gs-ico{display:inline-flex;flex-shrink:0;opacity:.8}
.gs-input{flex:1;min-width:0;height:100%;padding:0;border:none;outline:none;background:none;color:var(--text);font:inherit;font-size:12px}
.gs-input::placeholder{color:var(--muted);opacity:1}
.gs .gs-input,.gs .gs-input:focus-visible{background:none;border-radius:0;box-shadow:none} /* (the field draws the background and ring: controls.go's input look off) */
.gs-kbd{flex-shrink:0;padding:3px 6px;border-radius:6px;background:rgb(var(--ink) / .07);color:var(--muted2);font-family:inherit;font-size:10px;font-weight:600;line-height:1;letter-spacing:.3px;white-space:nowrap;pointer-events:none}
.gs-clear{display:none;flex-shrink:0;width:20px;height:20px;padding:0;border:none;border-radius:6px;background:transparent;color:var(--muted);cursor:pointer;align-items:center;justify-content:center;transition:background .1s,color .1s}
.gs-clear:hover{background:rgb(var(--ink) / .08);color:var(--text)}
.gs.has-q .gs-clear{display:inline-flex}
.gs.has-q .gs-kbd{display:none}
.gs-pop{position:fixed;top:0;left:0;z-index:65;display:flex;flex-direction:column;max-height:min(520px,calc(100vh - 64px));padding:4px;border-radius:12px;background:var(--bg2);box-shadow:0 12px 30px var(--shadow);opacity:0;visibility:hidden;pointer-events:none;transform:translateY(-4px);transform-origin:top center;transition:opacity .12s ease,transform .12s ease,visibility 0s linear .12s}
.gs.open .gs-pop{opacity:1;visibility:visible;pointer-events:auto;transform:none;transition:opacity .12s ease,transform .14s cubic-bezier(.2,.8,.2,1),visibility 0s}
.gs-list{flex:0 1 auto;min-height:0;overflow-y:auto;display:flex;flex-direction:column;gap:1px}
.gs-row{display:flex;align-items:center;gap:10px;min-height:44px;padding:6px 8px;border-radius:8px;color:var(--text);cursor:pointer;transition:background .08s}
.gs-row.sel{background:rgb(var(--accent-rgb) / .1)}
.gs-row.off .gs-tile,.gs-row.off .gs-main{opacity:.55}
.gs-tile{position:relative;flex-shrink:0;width:28px;height:28px;border-radius:8px;display:inline-flex;align-items:center;justify-content:center;background:rgb(var(--ink) / .06);color:var(--muted)}
.gs-tile.vm{background:rgb(var(--info-rgb) / .12);color:var(--info)}
.gs-tile .dot{position:absolute;right:-1px;bottom:-1px;width:8px;height:8px;box-shadow:0 0 0 2px var(--bg2)}
.gs-main{flex:1;min-width:0;display:flex;flex-direction:column;gap:2px}
.gs-name{display:flex;align-items:center;gap:6px;min-width:0;font-size:12.5px;font-weight:500;line-height:1.3}
.gs-nm{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.gs-kind{flex-shrink:0;padding:1px 5px;border-radius:5px;background:rgb(var(--ink) / .07);color:var(--muted);font-size:9px;font-weight:700;letter-spacing:.5px;line-height:1.4;text-transform:uppercase}
.gs-kind.vm{background:rgb(var(--info-rgb) / .12);color:var(--info)}
/* A local connection: "Local" in the accent, its type in the Devices page's
   colours (ui.go .tag-chip.linux / .win: SSH / RDP) */
.gs-tile.local{background:rgb(var(--accent-rgb) / .1);color:var(--accent-l)}
.gs-kind.local{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.gs-kind.ssh{background:rgb(var(--os-linux-rgb) / .12);color:var(--os-linux)}
.gs-kind.rdp{background:rgb(var(--os-win-rgb) / .12);color:var(--os-win)}
/* The local base locked: one quiet row at the end of the list */
.gs-row.gs-lock{min-height:40px;margin-top:2px}
.gs-row.gs-lock .gs-name{font-weight:500;color:var(--muted)}
.gs-row.gs-lock .gs-acts{opacity:1}
.gs-sub{display:flex;align-items:center;gap:5px;min-width:0;font-size:11px;line-height:1.3;color:var(--muted2)}
.gs-sub .gs-where{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
mark.gs-hl{background:rgb(var(--accent-rgb) / .18);color:inherit;border-radius:3px;padding:0 1px;margin:0 -1px}
.gs-acts{display:flex;align-items:center;gap:4px;flex-shrink:0;opacity:0;transition:opacity .1s}
.gs-row.sel .gs-acts{opacity:1}
.gs-act{height:26px;min-width:26px;padding:0 5px;border:none;border-radius:7px;background:rgb(var(--ink) / .06);color:var(--muted);display:inline-flex;align-items:center;justify-content:center;gap:3px;font:inherit;font-size:10px;font-weight:700;letter-spacing:.3px;cursor:pointer;transition:background .1s,color .1s,box-shadow .1s}
.gs-act:hover{background:rgb(var(--accent-rgb) / .14);color:var(--accent-l)}
.gs-act.txt{padding:0 7px}
.gs-act.on{background:rgb(var(--accent-rgb) / .14);color:var(--accent-l);box-shadow:0 0 0 2px var(--accent)}
.gs-act:disabled{opacity:.3;cursor:default;background:rgb(var(--ink) / .06);color:var(--muted);box-shadow:none}
.gs-msg{display:flex;flex-direction:column;align-items:center;gap:6px;padding:22px 16px 18px;text-align:center}
.gs-msg-ico{width:34px;height:34px;border-radius:10px;display:inline-flex;align-items:center;justify-content:center;background:rgb(var(--ink) / .06);color:var(--muted)}
.gs-msg-title{font-size:12.5px;font-weight:600;color:var(--text)}
.gs-msg-sub{max-width:320px;font-size:11px;line-height:1.45;color:var(--muted2)}
.gs-note{display:flex;align-items:center;gap:7px;padding:7px 10px 3px;font-size:11px;color:var(--muted2)}
.gs-spin{display:inline-block;flex-shrink:0;width:14px;height:14px;border-radius:50%;background:conic-gradient(var(--accent) 0 25%,rgb(var(--ink) / .12) 0);-webkit-mask:radial-gradient(farthest-side,transparent calc(100% - 2px),black 0);mask:radial-gradient(farthest-side,transparent calc(100% - 2px),black 0);animation:reachSpin .7s linear infinite}
.gs-msg .gs-spin{width:22px;height:22px}
.gs-foot{display:flex;align-items:center;gap:10px;flex-shrink:0;margin-top:4px;padding:6px 8px;border-radius:8px;background:rgb(var(--ink) / .03);font-size:10.5px;color:var(--muted2)}
.gs-scope{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.gs-keys{display:flex;align-items:center;gap:4px;flex-shrink:0;margin-inline-start:auto;white-space:nowrap}
.gs-keys kbd{min-width:16px;padding:2px 4px;border-radius:4px;background:rgb(var(--ink) / .08);color:var(--muted);font-family:inherit;font-size:9.5px;font-weight:600;line-height:1;text-align:center}
.gs-keys .gs-kl{margin-inline-end:6px}
.gs-keys .gs-kl:last-child{margin-inline-end:0}
@media (max-width:1180px){.gs .gs-kbd{display:none}}
@media (max-width:760px){.gs-keys{display:none}}
/* Narrow windows: the field is a search button; focused, it floats over the
   top bar (its place in the bar stays: nothing in the bar moves). */
@media (max-width:980px){
  .gs{flex:0 0 32px;min-width:32px}
  .gs .gs-field{width:32px;padding:0;justify-content:center;cursor:pointer}
  .gs .gs-field .gs-input{flex:0 0 0;width:0;opacity:0}
  .gs .gs-field .gs-clear,.gs.has-q .gs-field .gs-clear,.gs .gs-field .gs-kbd{display:none}
  .gs:focus-within .gs-field{position:fixed;top:8px;left:12px;right:12px;z-index:66;width:auto;padding:0 5px 0 10px;justify-content:flex-start;cursor:text;box-shadow:0 0 0 2px var(--accent),0 10px 28px var(--shadow)}
  .gs:focus-within .gs-field .gs-input{flex:1;width:auto;opacity:1}
  .gs.has-q:focus-within .gs-field .gs-clear{display:inline-flex}
  body.win-frameless .gs:focus-within .gs-field{right:150px}
  html[dir=rtl] body.win-frameless .gs:focus-within .gs-field{right:12px;left:150px}
}
@media (prefers-reduced-motion:reduce){
  .gs-pop,.gs.open .gs-pop{transform:none;transition:none}
  .gs-field,.gs-acts,.gs-row,.gs-act{transition:none}
  .gs-spin{animation-duration:1.6s}
}
`

const globalSearchJS = `
// ── Global search (global_search.go) ────────────────────────────────────────
var GS_MAX = 12;          // rows listed at most (the best matches)
var GS_DEBOUNCE_MS = 120; // the typing settles this long before the lists are searched
var GS_POP_W = 460;       // the drop-down's width (a narrower window: the window's)
var GS_LOCAL_TTL_MS = 30000; // the local base read again as the drop-down opens, when older
var gsState = { q: '', items: [], total: 0, sel: -1, act: -1, open: false, timer: null, again: null, mounted: false, localAt: 0 };
var GS_ICON = {
  search: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>',
  clear: '<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>',
};

// ── Matching and ranking (pure) ──
function gsLower(s) { return String(s === undefined || s === null ? '' : s).toLowerCase(); }
// The query as matched: lower case, its spaces folded.
function gsQuery(q) { return gsLower(q).replace(/\s+/g, ' ').trim(); }
// Where q starts a word of hay (both lower case): its index, -1 when nowhere.
function gsWordAt(hay, q) {
  for (var i = hay.indexOf(q); i >= 0; i = hay.indexOf(q, i + 1)) {
    if (i === 0 || !/[\p{L}\p{N}]/u.test(hay.charAt(i - 1))) return i;
  }
  return -1;
}
// How a machine matches the query: null, or { tier, field, at, len }. tier:
// 0 the name itself, 1 the name starts with it, 2 a word of the name does,
// 3 the name holds it, 4 another field (a VM's host, its addresses; a local
// connection's host, folder path, user) starts with it or one of its words
// does, 5 another field holds it, 6 each word of
// a query of several found somewhere. field: -1 the name, else the index in
// alts (-2: none to mark); at / len: what is highlighted.
function gsMatch(q, name, alts) {
  q = gsQuery(q);
  if (!q) return null;
  var n = gsLower(name), i;
  alts = Array.isArray(alts) ? alts : [];
  if (n === q) return { tier: 0, field: -1, at: 0, len: q.length };
  if (n.indexOf(q) === 0) return { tier: 1, field: -1, at: 0, len: q.length };
  if ((i = gsWordAt(n, q)) >= 0) return { tier: 2, field: -1, at: i, len: q.length };
  if ((i = n.indexOf(q)) >= 0) return { tier: 3, field: -1, at: i, len: q.length };
  var best = null;
  for (var k = 0; k < alts.length; k++) {
    var a = gsLower(alts[k]);
    if (!a) continue;
    if ((i = gsWordAt(a, q)) >= 0) return { tier: 4, field: k, at: i, len: q.length };
    if (!best && (i = a.indexOf(q)) >= 0) best = { tier: 5, field: k, at: i, len: q.length };
  }
  if (best) return best;
  var words = q.split(' ');
  if (words.length < 2) return null;
  var hay = [n].concat(alts.map(gsLower));
  for (var w = 0; w < words.length; w++) {
    if (!hay.some(function (h) { return h.indexOf(words[w]) >= 0; })) return null;
  }
  for (w = 0; w < words.length; w++) { // (the first word the name holds is marked)
    if ((i = n.indexOf(words[w])) >= 0) return { tier: 6, field: -1, at: i, len: words[w].length };
  }
  return { tier: 6, field: -2, at: -1, len: 0 };
}
// The best first: tier, then what opens now, devices before VMs before local
// connections, the shorter name (PC2 before PC20), then by name (numbers in
// order).
var GS_KIND_ORDER = { device: 0, vm: 1, local: 2 };
function gsKindRank(k) { return Object.prototype.hasOwnProperty.call(GS_KIND_ORDER, k) ? GS_KIND_ORDER[k] : 9; }
function gsCompare(a, b) {
  if (a.m.tier !== b.m.tier) return a.m.tier - b.m.tier;
  if (!!a.c.usable !== !!b.c.usable) return a.c.usable ? -1 : 1;
  if (a.c.kind !== b.c.kind) return gsKindRank(a.c.kind) - gsKindRank(b.c.kind);
  if (a.c.name.length !== b.c.name.length) return a.c.name.length - b.c.name.length;
  return a.c.name.localeCompare(b.c.name, undefined, { numeric: true, sensitivity: 'base' });
}
// { items: [{ c: candidate, m: match }] (at most max), total: every match }.
function gsRank(q, cands, max) {
  var hits = [];
  (Array.isArray(cands) ? cands : []).forEach(function (c) {
    var m = c ? gsMatch(q, c.name, c.alts) : null;
    if (m) hits.push({ c: c, m: m });
  });
  hits.sort(gsCompare);
  return { items: hits.slice(0, max > 0 ? max : GS_MAX), total: hits.length };
}

// ── What is searched: the lists' own machines (never more) ──
// The devices of the overview's groups (each once: a device listed twice
// stays one row); no VM console, local connection or code session.
function gsDeviceCands(groups) {
  var out = [], seen = new Set();
  (Array.isArray(groups) ? groups : []).forEach(function (g) {
    (g && Array.isArray(g.devices) ? g.devices : []).forEach(function (d) {
      if (!d || typeof d !== 'object' || d.id === undefined || d.id === null) return;
      if (devIsVm(d) || (typeof devIsLocal === 'function' && devIsLocal(d)) || (typeof devIsSupport === 'function' && devIsSupport(d))) return;
      var k = 'd:' + devKey(d);
      if (seen.has(k)) return;
      seen.add(k);
      out.push({ kind: 'device', key: k, name: String(d.hostname || d.id), alts: [], usable: devUsable(d), dev: d, group: g });
    });
  });
  return out;
}
// The VMs page's entries ({ key, pid, host, vm }): matched by their name,
// their host's name and their addresses.
function gsVmCands(entries) {
  return (Array.isArray(entries) ? entries : []).filter(function (e) { return e && e.vm && e.host; }).map(function (e) {
    var ips = Array.isArray(e.vm.ips) ? e.vm.ips : [];
    return { kind: 'vm', key: 'v:' + e.key, name: String(e.vm.name || e.vm.id || ''), alts: [String(e.host.hostname || '')].concat(ips), usable: vmsOpenable(e), entry: e };
  });
}
// The local base's connections (ui.go localDevGroups: the devices the Devices
// page lists for it, each folder a group whose name is its path): matched by
// their name, host, folder path and user (what their rows show; never a
// secret: cxnDevice keeps none).
function gsLocalCands(groups) {
  var out = [], seen = new Set();
  (Array.isArray(groups) ? groups : []).forEach(function (g) {
    (g && Array.isArray(g.devices) ? g.devices : []).forEach(function (d) {
      if (!d || !d._node || typeof devIsLocal !== 'function' || !devIsLocal(d)) return;
      var n = d._node, k = 'l:' + String(n.id);
      if (seen.has(k)) return;
      seen.add(k);
      out.push({ kind: 'local', key: k, name: String(d.hostname || n.label || n.host || ''),
        alts: [String(n.host || ''), String((g && g.name) || ''), String(n.username || '')], usable: devUsable(d), dev: d, group: g });
    });
  });
  return out;
}
// A local base exists (servers.go Kind "local"): its connections are
// searched, whatever the scope.
function gsLocalOn() {
  return typeof serverProfiles !== 'undefined' && Array.isArray(serverProfiles) && serverProfiles.some(function (p) { return p && p.local; });
}
// Single mode on the local base: no server, no tenant on screen.
function gsLocalOnly() {
  return typeof serverMode !== 'undefined' && serverMode !== 'multi' && typeof activeServerIsLocal === 'function' && activeServerIsLocal();
}
// The local base as the Devices page sees it: '' (none, or not read yet),
// 'ok' (unlocked, listed), 'locked', 'absent', 'unreadable'.
function gsLocalState() {
  if (!gsLocalOn() || typeof localConns === 'undefined' || !localConns) return '';
  return String(localConns.state || '');
}
// Not in a session window (no top bar). (A local base alone: its connections.)
function gsAvailable() {
  return !reachDetachedMode;
}
// Every machine searched now: the overview (what the Devices page lists), the
// VMs page's VMs (vmsAll: the same servers and tenants, its gates) and the
// local base's connections while it is unlocked (in every scope).
function gsCollect() {
  if (!gsAvailable()) return [];
  var out = [];
  if (!gsLocalOnly()) {
    var groups = (typeof overview !== 'undefined' && overview && Array.isArray(overview.groups)) ? overview.groups : [];
    out = gsDeviceCands(groups);
    if (typeof vmsAll === 'function') out = out.concat(gsVmCands(vmsAll()));
  }
  if (gsLocalState() === 'ok' && typeof localDevGroups === 'function') out = out.concat(gsLocalCands(localDevGroups()));
  return out;
}
// { items, total, locked: the local base is locked (its row says so) }.
function gsSearch(q) {
  var r = gsRank(q, gsCollect(), GS_MAX);
  r.locked = gsLocalState() === 'locked';
  return r;
}
// The rows of the drop-down: the matches, then the local base's locked row.
var GS_LOCKED_ITEM = { c: { kind: 'locked', key: 'locked', name: '', alts: [], usable: true }, m: null };
function gsRows(r) { return r.locked ? r.items.concat([GS_LOCKED_ITEM]) : r.items.slice(); }
// What is still being read (its note's key), '' when nothing: a server of the
// scope (multi-server mode) whose devices are not in yet, the VMs of one, the
// local base not read yet.
function gsLoading() {
  if (!gsLocalOnly()) {
    if (typeof serverMode !== 'undefined' && serverMode === 'multi' && typeof obServers === 'function') {
      var busy = obServers().some(function (p) {
        if (multiFilter.pid && p.id !== multiFilter.pid) return false;
        var st = srv.get(p.id);
        return !!(st && st.loading && st.signedIn !== false && !(st.groups && st.groups.length));
      });
      if (busy) return 'common.loading';
    }
    if (typeof vmsScopePids === 'function' && vmsScopePids().some(function (pid) { var d = vmsData.get(pid); return !d || !d.loaded; })) return 'search.loadingVms';
  }
  if (gsLocalOn() && typeof localConns !== 'undefined' && localConns && !localConns.state) return 'search.loadingLocal';
  return '';
}

// ── A row's parts ──
function gsMac() { return typeof reachOperatorIsMac === 'function' && reachOperatorIsMac(); }
function gsShortcutText() { return gsMac() ? '⌘K' : 'Ctrl K'; }
// text with [at, at+len) marked (nothing marked when lower case changes its length).
function gsHl(text, at, len) {
  var s = String(text === undefined || text === null ? '' : text);
  if (!(at >= 0) || !(len > 0) || gsLower(s).length !== s.length || at + len > s.length) return i18nEsc(s);
  return i18nEsc(s.slice(0, at)) + '<mark class="gs-hl">' + i18nEsc(s.slice(at, at + len)) + '</mark>' + i18nEsc(s.slice(at + len));
}
function gsMulti() { return typeof serverMode !== 'undefined' && serverMode === 'multi'; }
// A group's tenant, when the lists name it (the master's god view, or a
// server of multi-server mode listing several tenants).
function gsTenantName(pid, g) {
  if (g && typeof g.tenantName === 'string' && g.tenantName) return g.tenantName;
  if (!gsMulti() || !g) return '';
  var t = (srvState(pid).tenants || []).find(function (x) { return x && Number(x.id) === Number(g.tenantId); });
  return t && t.name ? String(t.name) : '';
}
// Where a device is: its server (multi-server mode), its tenant and group.
function gsDevWhere(c) {
  var pid = devPid(c.dev), g = c.group;
  var path = g ? devTreeGroupPath(g) : '';
  var tn = gsTenantName(pid, g);
  if (tn && path !== tn && path.indexOf(tn + ' / ') !== 0) path = path ? tn + ' / ' + path : tn;
  var srvHtml = gsMulti() ? '<span class="st-srv" style="background:' + srvColor(pid) + '"></span>' + i18nEsc(srvName(pid)) : '';
  return (srvHtml ? srvHtml + (path ? ' · ' : '') : '') + i18nEsc(path);
}
// Where a VM is: its Hyper-V host (marked when it matched), its server, the
// address that matched.
function gsVmWhere(c, m) {
  var e = c.entry, host = String(e.host.hostname || e.host.id || '');
  var h = m.field === 0 ? gsHl(host, m.at, m.len) : i18nEsc(host);
  var out = TH('search.vmOn', { host: '\u0001' }).replace('\u0001', h);
  if (gsMulti()) out = '<span class="st-srv" style="background:' + srvColor(e.pid) + '"></span>' + i18nEsc(srvName(e.pid)) + ' · ' + out;
  if (m.field >= 1 && c.alts[m.field]) out += ' · ' + gsHl(c.alts[m.field], m.at, m.len);
  return out;
}
// Where a local connection is: its folder path (the root: none), then
// user@host:port as its row in the Devices page shows it; what matched marked.
function gsLocalWhere(c, m) {
  var n = c.dev._node || {}, f = m ? m.field : -2;
  var path = c.alts[1] ? (f === 1 ? gsHl(c.alts[1], m.at, m.len) : i18nEsc(c.alts[1])) : '';
  var user = n.username ? (f === 2 ? gsHl(n.username, m.at, m.len) : i18nEsc(n.username)) + '@' : '';
  var host = (f === 0 ? gsHl(n.host, m.at, m.len) : i18nEsc(n.host || '')) + (n.port ? ':' + i18nEsc(String(n.port)) : '');
  return (path ? path + ' · ' : '') + user + host;
}
function gsVmDot(c) {
  var vm = c.entry.vm;
  if (typeof vmsHostWhy === 'function' && vmsHostWhy(c.entry.host)) return 'offline';
  return vm.state === 'running' ? 'online' : (vm.state === 'paused' || vm.state === 'saved') ? 'warn' : 'offline';
}
// A row's buttons: [{ act, html, title, disabled, txt }]. A device: the
// Devices page row's ▶, its terminals (CMD / PS or SSH; an unknown OS: a
// greyed one saying why) and Files, in the same states and words; a VM: its
// console; a local connection: its row's (ui.go localRowButtons: ▶ opening
// its SSH terminal / remote desktop, then SSH and Files for an SSH one, from
// cxnToolState through toolState); the local base locked: its Unlock.
function gsActs(c) {
  if (c.kind === 'locked') return [{ act: 'unlock', html: TH('vault.locked.submit'), title: T('vault.locked.submit'), disabled: false, txt: true }];
  if (c.kind === 'vm') {
    var why = vmsWhy(c.entry);
    return [{ act: 'console', html: VM_ICON, title: why || T('vms.console'), disabled: !!why }];
  }
  var d = c.dev, out = [];
  var launch = typeof reachDetachOn === 'function' && reachDetachOn();
  var agentOnly = d._tools && !(d.oblireach && d.oblireach.installed);
  var remoteTitle = c.kind === 'local' ? T(cxnIsSsh(d) ? 'devices.local.openSsh' : 'devices.local.openRdp')
    : (agentOnly && !launch) ? T('tools.openPage') : launch ? T('devices.openPageHere') : T('common.remote');
  out.push({ act: 'remote', html: DEV_ROW_ICONS.remote, title: remoteTitle, disabled: !devUsable(d) });
  if (toolsShellUnknown(d)) out.push({ act: 'shell', html: TOOLS_ICON.terminal, title: T('tools.reason.osUnknown'), disabled: true });
  devShellKinds(d).forEach(function (k) {
    var st = toolState(d, k);
    if (st.show) out.push({ act: k, html: TH(toolLabelKey(k)) + toolBadge(st), title: toolTitle(k, st), disabled: !st.enabled, txt: true });
  });
  var fs = toolState(d, 'files');
  if (fs.show) out.push({ act: 'files', html: TOOLS_ICON.folder + toolBadge(fs), title: toolTitle('files', fs), disabled: !fs.enabled });
  return out;
}
function gsActsHtml(acts) {
  return (acts || []).map(function (a, k) {
    return '<button class="gs-act' + (a.txt ? ' txt' : '') + '" type="button" tabindex="-1" data-act="' + i18nEsc(a.act) + '" data-k="' + k + '" title="' + i18nEsc(a.title) + '" aria-label="' + i18nEsc(a.title) + '"' +
      (a.disabled ? ' disabled' : '') + '>' + a.html + '</button>';
  }).join('');
}
// The local base locked: what it hides, its Unlock (the Devices page's own).
function gsLockRowHtml(it, i) {
  return '<div class="gs-row gs-lock" role="option" id="gs-opt-' + i + '" data-i="' + i + '" aria-selected="false">' +
    '<span class="gs-tile">' + TOOLS_ICON.lock + '</span>' +
    '<span class="gs-main"><span class="gs-name"><span class="gs-nm">' + TH('search.localLocked') + '</span><span class="gs-kind local">' + TH('devices.local.chip') + '</span></span>' +
    '<span class="gs-sub"><span class="gs-where">' + TH('search.localLockedSub') + '</span></span></span>' +
    '<span class="gs-acts">' + gsActsHtml(it.acts) + '</span></div>';
}
function gsRowHtml(it, i) {
  var c = it.c, m = it.m;
  if (c.kind === 'locked') return gsLockRowHtml(it, i);
  var vm = c.kind === 'vm', local = c.kind === 'local';
  var dot = vm ? gsVmDot(c) : devDotClass(c.dev);
  var kind = local ? T('devices.local.chip') : T(vm ? 'search.kindVm' : 'search.kindDevice');
  var name = m.field === -1 ? gsHl(c.name, m.at, m.len) : i18nEsc(c.name);
  var where = vm ? gsVmWhere(c, m) : local ? gsLocalWhere(c, m) : gsDevWhere(c);
  var why = vm && !c.usable ? vmsWhy(c.entry) : local && !c.usable ? T('devices.local.unreachable') : '';
  var tileCls = vm ? ' vm' : local ? ' local' : '';
  // A local connection: the icon of its type (localconn_tools.go cxnToolIcon), "Local" and SSH / RDP.
  var ssh = local && cxnIsSsh(c.dev);
  var icon = vm ? VM_ICON : local ? cxnToolIcon(ssh ? 'ssh' : 'rdp') : TOOLS_ICON.monitor;
  var badges = '<span class="gs-kind' + tileCls + '">' + i18nEsc(kind) + '</span>' +
    (local ? '<span class="gs-kind ' + (ssh ? 'ssh' : 'rdp') + '">' + i18nEsc(cxnTypeName(c.dev)) + '</span>' : '');
  return '<div class="gs-row' + (c.usable ? '' : ' off') + '" role="option" id="gs-opt-' + i + '" data-i="' + i + '" aria-selected="false"' + (why ? ' title="' + i18nEsc(why) + '"' : '') + '>' +
    '<span class="gs-tile' + tileCls + '" title="' + i18nEsc(kind) + '">' + icon + '<span class="dot ' + dot + '"></span></span>' +
    '<span class="gs-main"><span class="gs-name"><span class="gs-nm">' + name + '</span>' + badges + '</span>' +
    '<span class="gs-sub"><span class="gs-where">' + where + '</span></span></span>' +
    '<span class="gs-acts">' + gsActsHtml(it.acts) + '</span></div>';
}
// What the search covers, in the words of the menus next to it.
function gsScopeText() {
  if (gsMulti()) return typeof multiFilterLabel === 'function' ? multiFilterLabel() : '';
  if (gsLocalOnly()) return srvName(activeServerId); // (the base: no tenant)
  var parts = [];
  if (typeof serverProfiles !== 'undefined' && serverProfiles.length > 1) parts.push(srvName(activeServerId));
  var t = (typeof tenants !== 'undefined' && Array.isArray(tenants)) ? tenants.find(function (x) { return x && Number(x.id) === Number(currentTenantId); }) : null;
  if (t && t.name) parts.push(String(t.name));
  return parts.filter(Boolean).join(' · ');
}

// ── The drop-down ──
function gsEl(id) { return typeof document !== 'undefined' ? document.getElementById(id) : null; }
function gsRender() {
  var list = gsEl('gs-list'), foot = gsEl('gs-foot');
  if (!list || !foot) return;
  var items = gsState.items, loading = gsLoading(), html = '';
  items.forEach(function (it) { it.acts = gsActs(it.c); });
  // (the matches, then the local base's locked row: last in the list as in items)
  var found = items.filter(function (it) { return it.c.kind !== 'locked'; });
  var lock = items.length > found.length ? gsRowHtml(items[items.length - 1], items.length - 1) : '';
  if (found.length) {
    html = found.map(gsRowHtml).join('');
    if (gsState.total > found.length) html += '<div class="gs-note">' + TH('search.more', { shown: found.length, total: gsState.total }) + '</div>';
    if (loading) html += '<div class="gs-note"><span class="gs-spin"></span>' + TH(loading) + '</div>';
  } else if (loading) {
    html = '<div class="gs-msg"><span class="gs-spin"></span><div class="gs-msg-sub">' + TH('common.loading') + '</div></div>';
  } else {
    html = '<div class="gs-msg"><span class="gs-msg-ico">' + GS_ICON.search.replace('width="13" height="13"', 'width="16" height="16"') + '</span>' +
      '<div class="gs-msg-title">' + TH('search.noMatch') + '</div><div class="gs-msg-sub">' + TH(gsLocalOnly() ? 'search.noMatchSubLocal' : 'search.noMatchSub') + '</div></div>';
  }
  list.innerHTML = html + lock;
  var scope = gsScopeText();
  foot.innerHTML = '<span class="gs-scope"' + (scope ? ' title="' + i18nEsc(scope) + '"' : '') + '>' + (scope ? TH('search.scope', { scope: scope }) : '') + '</span>' +
    '<span class="gs-keys" aria-hidden="true"><kbd>↑</kbd><kbd>↓</kbd><span class="gs-kl">' + TH('search.hintMove') + '</span>' +
    '<kbd>↵</kbd><span class="gs-kl">' + TH('search.hintOpen') + '</span>' +
    '<kbd>Tab</kbd><span class="gs-kl">' + TH('search.hintActions') + '</span>' +
    '<kbd>Esc</kbd><span class="gs-kl">' + TH('search.hintClose') + '</span></span>';
  gsMark(true);
}
// The selected row (and its picked button) shown, without drawing the rows again.
function gsMark(scroll) {
  var list = gsEl('gs-list'), inp = gsEl('gs-input');
  if (!list) return;
  var rows = list.querySelectorAll('.gs-row');
  for (var i = 0; i < rows.length; i++) {
    var on = i === gsState.sel;
    rows[i].classList.toggle('sel', on);
    rows[i].setAttribute('aria-selected', on ? 'true' : 'false');
    var bs = rows[i].querySelectorAll('.gs-act');
    for (var k = 0; k < bs.length; k++) bs[k].classList.toggle('on', on && k === gsState.act);
    if (on && scroll && typeof rows[i].scrollIntoView === 'function') { try { rows[i].scrollIntoView({ block: 'nearest' }); } catch (e) {} }
  }
  if (inp) {
    if (gsState.sel >= 0 && rows[gsState.sel]) inp.setAttribute('aria-activedescendant', 'gs-opt-' + gsState.sel);
    else inp.removeAttribute('aria-activedescendant');
  }
}
function gsPlace() {
  var pop = gsEl('gs-pop'), f = gsEl('gs-field');
  if (!pop || !f || typeof f.getBoundingClientRect !== 'function') return;
  var r = f.getBoundingClientRect();
  var vw = window.innerWidth || (document.documentElement && document.documentElement.clientWidth) || 0;
  var w = Math.max(240, Math.min(GS_POP_W, vw - 24));
  var rtl = document.documentElement && document.documentElement.dir === 'rtl';
  var left = Math.max(12, Math.min(rtl ? r.right - w : r.left, vw - w - 12));
  pop.style.width = w + 'px';
  pop.style.left = Math.round(left) + 'px';
  pop.style.top = Math.round(r.bottom + 6) + 'px';
}
function gsOpen() {
  var wrap = gsEl('gs'), inp = gsEl('gs-input');
  if (!wrap) return;
  gsPlace();
  gsState.open = true;
  wrap.classList.add('open');
  if (inp) inp.setAttribute('aria-expanded', 'true');
}
function gsClose() {
  var wrap = gsEl('gs'), inp = gsEl('gs-input');
  if (gsState.timer) { clearTimeout(gsState.timer); gsState.timer = null; }
  gsState.open = false; gsState.sel = -1; gsState.act = -1;
  if (wrap) wrap.classList.remove('open');
  if (inp) { inp.setAttribute('aria-expanded', 'false'); inp.removeAttribute('aria-activedescendant'); }
}
// The field emptied and left (after a machine was opened).
function gsFinish() {
  var inp = gsEl('gs-input'), wrap = gsEl('gs');
  gsClose();
  gsState.q = ''; gsState.items = []; gsState.total = 0;
  if (inp) { inp.value = ''; try { inp.blur(); } catch (e) {} }
  if (wrap) wrap.classList.remove('has-q');
}
// The VMs read again when stale as the drop-down opens (vms_page.go: its
// TTL; the device lists keep themselves fresh); the local base too, when
// never read or older than GS_LOCAL_TTL_MS (locked or unlocked meanwhile,
// from the Vault page or a terminal): every list follows (overviewRedraw,
// which searches the open drop-down again through vmsKick).
function gsKick() {
  if (typeof vmsKick === 'function') vmsKick();
  gsLocalKick(Date.now());
}
function gsLocalKick(now) {
  if (!gsLocalOn() || typeof localConnsLoad !== 'function' || typeof localConns === 'undefined' || !localConns || localConns.loading) return false;
  if (localConns.state && now - gsState.localAt < GS_LOCAL_TTL_MS) return false;
  gsState.localAt = now;
  var done = function () { if (typeof overviewRedraw === 'function') overviewRedraw(); else gsDataChanged(); };
  try { var p = localConnsLoad(); if (p && typeof p.then === 'function') p.then(done, function () {}); } catch (e) {}
  return true;
}
// Searches the field's text now. keep: the selected machine stays selected.
function gsRun(keep) {
  if (gsState.timer) { clearTimeout(gsState.timer); gsState.timer = null; }
  var inp = gsEl('gs-input');
  var q = inp ? inp.value : '';
  gsState.q = q;
  if (!gsQuery(q) || !gsAvailable()) { gsClose(); return; }
  var was = gsState.open;
  var prev = keep && gsState.items[gsState.sel] ? gsState.items[gsState.sel].c.key : null, prevAct = gsState.act;
  var r = gsSearch(q);
  gsState.items = gsRows(r); gsState.total = r.total;
  var i = prev ? gsState.items.findIndex(function (it) { return it.c.key === prev; }) : -1;
  // (the locked row alone: not selected, Enter never unlocks unasked)
  gsState.sel = i >= 0 ? i : (r.items.length ? 0 : -1);
  gsState.act = i >= 0 ? prevAct : -1;
  gsRender();
  gsOpen();
  if (!was) gsKick();
}
// The lists changed (a read, a push, the VMs): the field shown or not, the
// open drop-down searched again (once for a burst).
function gsDataChanged() {
  gsSync();
  if (!gsState.open || gsState.again) return;
  gsState.again = setTimeout(function () {
    gsState.again = null;
    if (gsState.open && !gsState.timer) gsRun(true);
  }, 60);
}
function gsSync() {
  var wrap = gsEl('gs');
  if (!wrap) return;
  var on = gsAvailable();
  wrap.classList.toggle('hide', !on);
  if (!on && gsState.open) gsClose();
}
// The local base's Unlock: the Devices page's (the vault's form in a dialog,
// connections_page.go cxnUnlockThen); then read again, every list follows,
// and the field back with its text, searched again.
function gsUnlock() {
  gsClose();
  if (typeof cxnUnlockThen !== 'function') return;
  cxnUnlockThen(function () {
    gsState.localAt = Date.now();
    var p = typeof localConnsLoad === 'function' ? localConnsLoad() : null;
    var then = function () { if (typeof overviewRedraw === 'function') overviewRedraw(); gsFocus(); };
    if (p && typeof p.then === 'function') p.then(then, then); else then();
  });
}
// A row: a device, its row in the Devices page (devicesOpen); a local
// connection, the same (devicesOpen -> localDevOpen: its terminal / view); a
// VM, its console.
function gsRunDefault(it) {
  if (!it) return;
  if (it.c.kind === 'locked') return gsUnlock();
  gsFinish();
  if (it.c.kind === 'vm') return vmsOpenOne(it.c.entry);
  return devicesOpen(it.c.dev);
}
function gsRunAct(it, act) {
  if (!it) return;
  var a = (it.acts || []).find(function (x) { return x.act === act; });
  if (!a || a.disabled) return;
  if (it.c.kind === 'locked') return gsUnlock();
  gsFinish();
  if (it.c.kind === 'vm') return vmsOpenOne(it.c.entry);
  return devicesOpen(it.c.dev, act);
}
// The selected row's buttons that can be used: their indexes.
function gsActsOn(it) {
  var out = [];
  (it && it.acts ? it.acts : []).forEach(function (a, k) { if (!a.disabled) out.push(k); });
  return out;
}
function gsKey(e) {
  if (!e || e.isComposing) return;
  var n = gsState.items.length;
  var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    stop();
    if (!gsState.open) { gsRun(false); return; }
    if (!n) return;
    gsState.sel = e.key === 'ArrowDown' ? (gsState.sel + 1) % n : (gsState.sel <= 0 ? n - 1 : gsState.sel - 1);
    gsState.act = -1;
    gsMark(true);
    return;
  }
  if (e.key === 'Enter') {
    if (gsState.timer || !gsState.open) gsRun(true);
    var it = gsState.items[gsState.sel];
    if (!it) return;
    stop();
    if (gsState.act >= 0 && it.acts && it.acts[gsState.act]) gsRunAct(it, it.acts[gsState.act].act);
    else gsRunDefault(it);
    return;
  }
  if (e.key === 'Tab' && gsState.open && gsState.items[gsState.sel] && !e.ctrlKey && !e.altKey && !e.metaKey) {
    var on = gsActsOn(gsState.items[gsState.sel]);
    if (!on.length) return;
    stop();
    var at = on.indexOf(gsState.act), next;
    if (e.shiftKey) next = at < 0 ? on[on.length - 1] : at === 0 ? -1 : on[at - 1];
    else next = at < 0 ? on[0] : at === on.length - 1 ? -1 : on[at + 1];
    gsState.act = next;
    gsMark(false);
    return;
  }
  if (e.key === 'Escape') {
    stop();
    var inp = gsEl('gs-input');
    if (gsState.open) gsClose();
    else if (inp && inp.value) { inp.value = ''; gsState.q = ''; var w = gsEl('gs'); if (w) w.classList.remove('has-q'); }
    else if (inp) { try { inp.blur(); } catch (x) {} }
  }
}
function gsOnInput() {
  var inp = gsEl('gs-input'), wrap = gsEl('gs');
  if (wrap) wrap.classList.toggle('has-q', !!(inp && inp.value));
  if (gsState.timer) clearTimeout(gsState.timer);
  if (!inp || !gsQuery(inp.value)) { gsState.timer = null; gsClose(); return; }
  gsState.timer = setTimeout(function () { gsState.timer = null; gsRun(false); }, GS_DEBOUNCE_MS);
}
// Ctrl+K / Cmd+K: the field focused, its text selected. Not when the key is a
// remote picture's or a terminal's (they cancel it: it goes to the machine),
// under a dialog, or with the top bar hidden (focus mode, signed out).
function gsShortcut(e) {
  if (!e || e.defaultPrevented || e.altKey || e.shiftKey || e.repeat) return;
  if (!(e.ctrlKey || e.metaKey) || (e.ctrlKey && e.metaKey)) return;
  if (e.code !== 'KeyK' && String(e.key || '').toLowerCase() !== 'k') return;
  var t = e.target;
  if (t && typeof t.closest === 'function' && t.closest('canvas,.xterm,.remote-viewport,.modal-overlay')) return;
  if (document.querySelector && document.querySelector('.modal-overlay.visible')) return;
  if (!gsFocus()) return;
  if (e.preventDefault) e.preventDefault();
}
function gsFocus() {
  var inp = gsEl('gs-input'), wrap = gsEl('gs');
  if (!inp || !wrap || !gsAvailable() || wrap.offsetParent === null) return false;
  try { inp.focus(); inp.select(); } catch (e) { return false; }
  if (gsQuery(inp.value)) gsRun(true);
  return true;
}
function gsOutside(e) {
  var wrap = gsEl('gs');
  if (!gsState.open || !wrap || !e || !e.target) return;
  if (!wrap.contains(e.target)) gsClose();
}
function gsMount() {
  if (gsState.mounted || typeof document === 'undefined' || reachDetachedMode) return;
  var anchor = document.getElementById('tenant-switcher');
  if (!anchor || !anchor.parentNode) return;
  var wrap = document.createElement('div');
  wrap.className = 'gs'; wrap.id = 'gs';
  wrap.setAttribute('data-no-drag', '');
  wrap.setAttribute('role', 'search');
  wrap.innerHTML =
    '<div class="gs-field" id="gs-field">' +
      '<span class="gs-ico" aria-hidden="true">' + GS_ICON.search + '</span>' +
      '<input class="gs-input" id="gs-input" type="text" autocomplete="off" spellcheck="false" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="gs-list"' +
        ' placeholder="' + i18nEsc(T('search.placeholder')) + '" data-i18n-ph="search.placeholder" aria-label="' + i18nEsc(T('search.label')) + '" data-i18n-aria="search.label"/>' +
      '<kbd class="gs-kbd" aria-hidden="true">' + i18nEsc(gsShortcutText()) + '</kbd>' +
      '<button class="gs-clear" id="gs-clear" type="button" tabindex="-1" title="' + i18nEsc(T('search.clear')) + '" data-i18n-title="search.clear" aria-label="' + i18nEsc(T('search.clear')) + '" data-i18n-aria="search.clear">' + GS_ICON.clear + '</button>' +
    '</div>' +
    '<div class="gs-pop" id="gs-pop">' +
      '<div class="gs-list" id="gs-list" role="listbox" aria-label="' + i18nEsc(T('search.listLabel')) + '" data-i18n-aria="search.listLabel"></div>' +
      '<div class="gs-foot" id="gs-foot"></div>' +
    '</div>';
  anchor.parentNode.insertBefore(wrap, anchor.nextSibling);
  gsState.mounted = true;
  var field = wrap.querySelector('#gs-field'), inp = wrap.querySelector('#gs-input'), list = wrap.querySelector('#gs-list');
  // The tooltip in the language on screen, with the shortcut.
  field.addEventListener('mouseenter', function () { field.title = T('search.title', { keys: gsShortcutText().replace(' ', '+') }); });
  // A click anywhere on the field (its icon: the button of a narrow window) types in it.
  field.addEventListener('mousedown', function (e) {
    if (e.target === inp || (e.target && e.target.closest && e.target.closest('.gs-clear'))) return;
    e.preventDefault();
    try { inp.focus(); } catch (x) {}
  });
  wrap.querySelector('#gs-clear').addEventListener('mousedown', function (e) { e.preventDefault(); });
  wrap.querySelector('#gs-clear').addEventListener('click', function () {
    inp.value = ''; gsState.q = ''; wrap.classList.remove('has-q'); gsClose();
    try { inp.focus(); } catch (x) {}
  });
  inp.addEventListener('input', gsOnInput);
  inp.addEventListener('keydown', gsKey);
  inp.addEventListener('focus', function () { if (gsQuery(inp.value) && !gsState.open) gsRun(true); });
  wrap.addEventListener('focusout', function (e) {
    if (e && e.relatedTarget && wrap.contains(e.relatedTarget)) return;
    setTimeout(function () { if (!wrap.contains(document.activeElement)) gsClose(); }, 0);
  });
  // The rows: the focus stays in the field; a row opens its machine, a
  // button what it says; the pointer selects the row it is over.
  list.addEventListener('mousedown', function (e) { e.preventDefault(); });
  list.addEventListener('mousemove', function (e) {
    var row = e.target && e.target.closest ? e.target.closest('.gs-row') : null;
    if (!row) return;
    var i = Number(row.dataset.i);
    if (i !== gsState.sel) { gsState.sel = i; gsState.act = -1; gsMark(false); }
  });
  list.addEventListener('click', function (e) {
    var row = e.target && e.target.closest ? e.target.closest('.gs-row') : null;
    if (!row) return;
    var it = gsState.items[Number(row.dataset.i)];
    var b = e.target.closest('button[data-act]');
    if (b) { if (!b.disabled) gsRunAct(it, b.dataset.act); return; }
    gsRunDefault(it);
  });
  gsSync();
}
if (typeof document !== 'undefined' && document && typeof document.getElementById === 'function') {
  gsMount();
  if (typeof document.addEventListener === 'function') {
    document.addEventListener('keydown', gsShortcut);
    document.addEventListener('mousedown', gsOutside, true);
  }
  if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') window.addEventListener('resize', function () { if (gsState.open) gsPlace(); });
}
`
