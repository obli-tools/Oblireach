package main

// vms_page.go — the main window's "VMs" page: a "VMs" section under Devices,
// shown when the active server (or one of the servers in multi-server mode)
// has VMs, keeping the Server / Tenant / Group / Host / VMs hierarchy.
//
// The VMs are never matched with the devices (the hypervisors may be on
// one Obliance server, the VMs' own agents on another): a VM is listed under
// its Hyper-V HOST, the host where the device lists put it (its server, its
// tenant, its group), never in the Devices page.
//
// What is read (Obliance, hyperv.routes.ts):
//   - GET /api/hyperv/vms: the VMs of the session's tenant (every tenant for
//     an administrator on the master tenant), each with hostDeviceId; for a
//     non-administrator, of the hosts the user can see. Read per server: in
//     single mode on the session's tenant (like the device lists); in
//     multi-server mode on every tenant the tenant menu leaves open, each
//     through the pinned proxy (/spt/<profile>/<tenant>, tenantpin.go), only
//     tenant 1 for an administrator who has it (the god view lists them
//     all); an older server (not "scoped") on its session's tenant. A 401 /
//     403 / 404 (an older server without the route) = no VM, nothing said.
//   - That list does not check the 'hyperv:view' capability: for a
//     non-administrator, a host's VMs are listed only once GET
//     /api/hyperv/devices/:id/vms (which does) answered with VMs for this
//     user (vm_console.go vmLoad, its cache). An administrator holds every
//     capability.
//   - A VM is listed only under a host the device lists hold (what the server
//     lets this user reach): a VM of a host they do not list is left out.
//   - POST /api/hyperv/devices/:id/vms/:vmId/action { action }: start,
//     shutdown, restart, stop (hard), save, pause, resume ('hyperv:power';
//     2FA / a second administrator per the tenant's restrictions: 401 asked
//     by _apiWithTfa, 202 pending_approval). A 403 hides that host's power
//     buttons.
//   - Socket.io HYPERV_VMS_UPDATED { hostDeviceId, vms } (the session's
//     tenant room): applied to the hosts this page already lists only.
//
// The sidebar entry ("VMs", under Devices) shows once a server in the scope
// (single: the active one; multi: the tenant menu's choice) lists a VM this
// user may see; never with a local base alone, never in a session window.
// The lists' data are read again when stale (VMS_TTL_MS) as the device lists
// are drawn, every VMS_POLL_MS while the page is on screen, and by its
// Refresh (the hosts asked to enumerate again: POST .../live).
//
// The page is the Devices page's: the same table, tree rows, search, filters
// (state, host), count, boxes (VM / host / group / tenant / server), its bar
// ("Open as mosaic", "Open the consoles": devices_select.go dsPlan, "Detach
// by default" and "Connection of several devices" respected). A VM's console
// is vm_console.go's tab (vmOpen): greyed with its reason when it cannot open.
//
// A session window's "+" list (ui.go devViewPicker) draws each Hyper-V
// host's VMs under it from these same lists (vmsAll), read there only while
// that list is open (vmsDataOn): see the last section.
//
// Both consts are injected verbatim after vm_console.go's (ui.go buildUI, not
// through Sprintf: '%' is safe); no backtick here.

const vmsPageCSS = `
/* ── VMs page (vms_page.go) ── */
.dt .vm-row-ico{display:inline-flex;align-items:center;justify-content:center;width:20px;height:20px;border-radius:6px;flex-shrink:0;background:rgb(var(--info-rgb) / .12);color:var(--info)}
.dt .vm-guest{font-size:11px;color:var(--muted2);font-weight:400;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
.dt .vm-num{white-space:nowrap;font-variant-numeric:tabular-nums}
.dt .vm-ip{white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--muted)}
.dt .tag-chip.vm{background:rgb(var(--info-rgb) / .12);color:var(--info)}
.dt tbody tr.dp-vm-row:focus-visible{outline:1px solid var(--accent);outline-offset:-1px}
.dt .vm-hostcell{color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:180px}
.tree-host .tree-head .tree-name{font-weight:600}
.tree-host .tag-chip{margin-inline-start:4px}
.dt tbody tr.tree-host td{background:rgb(var(--ink) / .012)}
.dt .icon-btn.vm-danger:hover{color:var(--danger)}
.vms-pop{position:fixed;z-index:70;min-width:220px;padding:6px;border:none;border-radius:12px;background:var(--bg3);background:color-mix(in srgb,var(--bg3) 84%,transparent);-webkit-backdrop-filter:blur(16px) saturate(1.2);backdrop-filter:blur(16px) saturate(1.2);box-shadow:0 10px 32px var(--shadow)}
.vms-pop button{display:flex;align-items:center;gap:8px;width:100%;margin:0;padding:7px 9px;border:none;outline:none;border-radius:8px;background:transparent;color:var(--text);font:inherit;font-size:12px;cursor:pointer;text-align:start}
.vms-pop button:hover,.vms-pop button:focus-visible{background:color-mix(in srgb,var(--text) 8%,transparent)}
.vms-pop button.danger{color:var(--danger)}
.vms-pop button.danger:hover{background:color-mix(in srgb,var(--danger) 12%,transparent)}
.vms-confirm-text{font-size:12.5px;color:var(--text);line-height:1.5;margin:4px 0 10px}
`

const vmsPageJS = `
// ── VMs page (vms_page.go) ──────────────────────────────────────────────────
var VMS_TTL_MS = 60000;      // a server's VM list kept this long
var VMS_POLL_MS = 30000;     // read again this often while the page is on screen
var VMS_LIVE_WAIT_MS = 1500; // Refresh: the hosts' enumeration given this long
var VMS_LIVE_MAX = 40;       // Refresh: at most this many hosts asked
var VMS_IPS_MAX = 8, VMS_TEXT_MAX = 100;
var VMS_COLS = 9;
var VMS_ICON = {
  more: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/></svg>',
  start: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="6 3 21 12 6 21 6 3"/></svg>',
  shutdown: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18.36 6.64a9 9 0 11-12.73 0"/><line x1="12" y1="2" x2="12" y2="12"/></svg>',
  restart: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 102.13-9.36L1 10"/></svg>',
  host: '<svg class="tree-ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="4" width="20" height="6" rx="2"/><rect x="2" y="14" width="20" height="6" rx="2"/><line x1="6" y1="7" x2="6.01" y2="7"/><line x1="6" y1="17" x2="6.01" y2="17"/></svg>',
};
// The power actions: their words, their confirmation (the destructive ones).
var VMS_ACTIONS = {
  start:    { label: 'vms.action.start' },
  resume:   { label: 'vms.action.resume' },
  shutdown: { label: 'vms.action.shutdown', confirm: 'vms.confirm.shutdown' },
  restart:  { label: 'vms.action.restart', confirm: 'vms.confirm.restart' },
  stop:     { label: 'vms.action.stop', confirm: 'vms.confirm.stop' },
  save:     { label: 'vms.action.save' },
  pause:    { label: 'vms.action.pause' },
};
var VMS_STATES = ['running', 'off', 'paused', 'saved']; // the state filter's own values ('other': the rest)

var vmsFilter = { q: '', state: '', host: '' };
var vmsSearchFolded = new Set();
var vmsData = new Map();     // pid -> { at, sig, loaded, err, byHost: Map(hostId -> [vm]), pending, pendingSig, seq }
var vmsGateTried = new Map(); // devKey(host) -> when its 'hyperv:view' check was last asked
var vmsPowerDenied = new Set(); // devKey(host): its power actions refused (403)
var vmsPollTimer = null;

// ── Scope: which servers ──
function vmsOn() { return !reachDetachedMode; }
// The data (never the entry nor the page) are read in a session window too
// while its "+" list is open: that list draws each Hyper-V host's VMs under
// it (the section at the end of this file), from these very lists.
function vmsPickerOn() { return typeof devPickerIsOpen === 'function' && devPickerIsOpen(); }
function vmsDataOn() { return vmsOn() || vmsPickerOn(); }
function vmsMulti() { return typeof serverMode !== 'undefined' && serverMode === 'multi'; }
// The servers whose VMs this page lists: the active one (single mode, never
// a local base), every signed-in server the tenant menu leaves (multi).
function vmsScopePids() {
  if (!vmsDataOn()) return [];
  if (vmsMulti()) {
    return obServers().filter(function (p) { return (!multiFilter.pid || p.id === multiFilter.pid) && srvState(p.id).signedIn === true; }).map(function (p) { return p.id; });
  }
  if (typeof activeServerIsLocal === 'function' && activeServerIsLocal()) return [];
  return [homePid()];
}
// Where a server's VMs are read (see the top of the file).
function vmsTargets(pid) {
  if (!vmsMulti()) return [{ base: null }];
  var st = srvState(pid);
  if (!st.scoped) return [{ base: srvBase(pid) }];
  var ids = (st.tenants || []).map(function (t) { return Number(t.id); }).filter(function (n) { return Number.isInteger(n) && n > 0; });
  if (multiFilter.pid === pid && multiFilter.tenantId !== null) ids = ids.filter(function (n) { return n === multiFilter.tenantId; });
  if (vmIsAdmin({ _pid: pid }) && ids.indexOf(1) >= 0) ids = [1];
  return ids.map(function (id) { return { base: '/spt/' + pid + '/' + id }; });
}
// What the list was read for: another tenant, server mode or account = read again.
function vmsSig(pid) {
  return (vmsMulti() ? 'm|' + vmsTargets(pid).map(function (t) { return t.base; }).join(',') : 's|' + String(currentTenantId)) + '|' + (vmIsAdmin({ _pid: pid }) ? 'a' : 'u');
}

// ── A VM as the server sends it (untrusted text: cleaned, shown as text) ──
function vmsText(s, max) {
  var t = String(s === undefined || s === null ? '' : s).replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, '').trim();
  return t.length > max ? t.slice(0, max).trim() : t;
}
function vmsNum(v, min, max) {
  if (v === null || v === undefined || v === '') return null;
  var n = Number(v);
  if (!isFinite(n) || n < min) return null;
  return max !== undefined && n > max ? max : n;
}
function vmsNorm(v) {
  if (!v || typeof v !== 'object' || !vmIdOk(v.vmId)) return null;
  var hostId = Number(v.hostDeviceId);
  if (!Number.isInteger(hostId) || hostId <= 0) return null;
  var id = v.vmId.toLowerCase();
  var st = (typeof v.state === 'string' && Object.prototype.hasOwnProperty.call(VM_STATE_TEXT, v.state)) ? v.state : 'unknown';
  var ips = (Array.isArray(v.ipAddresses) ? v.ipAddresses : []).filter(function (x) { return typeof x === 'string'; })
    .map(function (x) { return vmsText(x, 64); }).filter(Boolean).slice(0, VMS_IPS_MAX);
  var tid = Number(v.tenantId);
  return { id: id, rawId: v.vmId, hostId: hostId, name: vmCleanName(v.name) || id, state: st,
    cpu: vmsNum(v.cpuUsagePercent, 0, 100), mem: vmsNum(v.memoryBytes, 0), uptime: vmsNum(v.uptimeSeconds, 0), ips: ips,
    guestOs: vmsText(v.guestOs, VMS_TEXT_MAX), tenantId: Number.isInteger(tid) && tid > 0 ? tid : null };
}
function vmsMerge(byHost, list) {
  (Array.isArray(list) ? list : []).forEach(function (raw) {
    var v = vmsNorm(raw);
    if (!v) return;
    var arr = byHost.get(v.hostId);
    if (!arr) { arr = []; byHost.set(v.hostId, arr); }
    if (arr.some(function (x) { return x.id === v.id; })) return; // (the same VM read through two tenants)
    arr.push(v);
  });
  byHost.forEach(function (arr) { arr.sort(function (a, b) { return a.name.localeCompare(b.name); }); });
  return byHost;
}

// ── Reading a server's VMs ──
async function vmsGet(t) {
  return t.base === null ? api('GET', '/api/hyperv/vms') : apiAt(t.base, 'GET', '/api/hyperv/vms');
}
function vmsLoadPid(pid, force) {
  var sig = vmsSig(pid), d = vmsData.get(pid);
  if (!force && d && d.loaded && d.sig === sig && Date.now() - d.at < VMS_TTL_MS) return Promise.resolve(d);
  if (d && d.pending && d.pendingSig === sig) return d.pending;
  if (!d) { d = { at: 0, sig: '', loaded: false, err: '', byHost: new Map(), pending: null, pendingSig: '', seq: 0 }; vmsData.set(pid, d); }
  var mine = ++d.seq;
  d.pendingSig = sig;
  d.pending = (async function () {
    var byHost = new Map(), err = '', anyOk = false;
    var targets = vmsTargets(pid);
    for (var i = 0; i < targets.length; i++) {
      try {
        var r = await vmsGet(targets[i]);
        if (r && r.ok) {
          anyOk = true;
          var j = null;
          try { j = await r.json(); } catch (e) {}
          vmsMerge(byHost, j && j.data);
        } else if (r && (r.status === 401 || r.status === 403 || r.status === 404)) anyOk = true; // (no VM: an older server, no right)
        else err = 'HTTP ' + (r ? r.status : '?');
      } catch (e) { err = (e && e.message) || String(e); }
    }
    if (mine !== d.seq) return d; // (a newer read counts)
    if (anyOk || !err || !d.loaded) d.byHost = byHost; // (a read that failed whole keeps what was known)
    d.at = Date.now(); d.sig = sig; d.loaded = true; d.err = err; d.pending = null;
    vmsChanged();
    return d;
  })();
  return d.pending;
}
// The servers in the scope read again when stale, the entry and the page
// following. Called as the device lists are drawn (overviewRedraw,
// loadOverview): cheap when everything is fresh.
function vmsKick() {
  if (typeof gsDataChanged === 'function') gsDataChanged(); // (the top bar's search: global_search.go)
  if (!vmsDataOn()) return;
  vmsScopePids().forEach(function (pid) { vmsLoadPid(pid); });
  vmsNavSync();
}
function vmsChanged() {
  vmsNavSync();
  if (typeof gsDataChanged === 'function') gsDataChanged(); // (the top bar's search: global_search.go)
  if (typeof currentRoute !== 'undefined' && currentRoute === 'vms') vmsFill();
  if (vmsPickerOn() && typeof devPickerRefresh === 'function') devPickerRefresh(); // (a session window's "+" list)
}

// ── The hosts: the device lists' records ──
// A server's groups as the lists hold them (multi: narrowed by the tenant menu).
function vmsGroupsOf(pid) {
  if (!vmsMulti()) return (overview && Array.isArray(overview.groups)) ? overview.groups : [];
  var st = srvState(pid);
  if (st.signedIn !== true) return [];
  var narrow = multiFilter.pid === pid && multiFilter.tenantId !== null && st.scoped;
  return (st.groups || []).filter(function (g) { return !narrow || Number(g.tenantId) === multiFilter.tenantId; });
}
// May this user see the VMs of this host: an administrator, else the host's
// own list answered with VMs ('hyperv:view', vm_console.go vmLoad). Unknown
// yet: asked (once per VM_LIST_TTL_MS), hidden meanwhile.
function vmsHostOk(host) {
  if (vmIsAdmin(host)) return true;
  var k = devKey(host), c = vmLists.get(k), now = Date.now();
  if ((!c || now - c.at >= VM_LIST_TTL_MS) && !vmListPending.has(k) && !(now - (vmsGateTried.get(k) || 0) < VM_LIST_TTL_MS)) {
    vmsGateTried.set(k, now);
    vmLoad(host).then(function () { vmsChanged(); }, function () {});
  }
  return !!(c && c.status === 'host');
}
// The VMs of a host this page lists ([] when none or not allowed).
function vmsOfHost(host) {
  if (!host || devIsVm(host) || (typeof devIsLocal === 'function' && devIsLocal(host))) return [];
  var d = vmsData.get(devPid(host));
  var list = d && d.byHost.get(Number(host.id));
  if (!list || !list.length) return [];
  return vmsHostOk(host) ? list : [];
}
// Why no console opens on this host's VMs ('' when one can).
function vmsHostWhy(host) {
  var why = vmHostReason(host);
  if (why) return why;
  return devLegacy(host) ? T('vm.err.unsupported') : '';
}
// A host's own name (its server is said by its section).
function vmsHostName(h) { return String((h && h.hostname) || (h && h.id) || ''); }
function vmsEntry(host, vm) { return { key: devKey(host) + '~' + vmKindOf(vm.id), pid: devPid(host), host: host, vm: vm }; }
function vmsOpenable(e) { return vmCanOpen(e.vm) && !vmsHostWhy(e.host); }
function vmsWhy(e) { return vmsHostWhy(e.host) || (vmCanOpen(e.vm) ? '' : T('vm.reason.notRunning')); }
// Every VM of the scope this user may see, unfiltered.
function vmsAll() {
  var out = [];
  vmsScopePids().forEach(function (pid) {
    vmsGroupsOf(pid).forEach(function (g) {
      (g && Array.isArray(g.devices) ? g.devices : []).forEach(function (h) {
        if (!h) return;
        vmsOfHost(h).forEach(function (vm) { out.push(vmsEntry(h, vm)); });
      });
    });
  });
  return out;
}

// ── The sidebar entry ──
function vmsNavVisible() { return vmsOn() && vmsAll().length > 0; }
function vmsNavSync() {
  var shown = vmsNavVisible();
  var rail = document.getElementById('nav-rail');
  Array.prototype.forEach.call((rail && rail.children) || [], function (c) {
    if (c && c.dataset && c.dataset.route === 'vms') c.style.display = shown ? '' : 'none';
  });
  return shown;
}

// ── Search and filters ──
function vmsStateKey(s) { return VMS_STATES.indexOf(s) >= 0 ? s : 'other'; }
function vmsMatch(e, g) {
  var f = vmsFilter, vm = e.vm, host = e.host;
  if (f.state && vmsStateKey(vm.state) !== f.state) return false;
  if (f.host && devKey(host) !== f.host) return false;
  var q = String(f.q || '').trim().toLowerCase();
  if (!q) return true;
  var hay = [vm.name, vm.guestOs, host.hostname, g && g.name, g && devTreeGroupPath(g), g && g.tenantName, vmsMulti() ? srvName(e.pid) : '']
    .concat(vm.ips);
  return hay.some(function (v) { return String(v || '').toLowerCase().indexOf(q) >= 0; });
}

// ── The tree: server > tenant > group > host > VMs ──
// Folding remembered on this computer (its own keys: never the Devices page's).
var vmsFoldStore = null;
var VMS_FOLD_KEY = 'oblireach_vmtree_folded';
function vmsSaved() {
  if (!vmsFoldStore) {
    var arr = [];
    try { arr = JSON.parse(localStorage.getItem(VMS_FOLD_KEY) || '[]'); } catch (e) { arr = []; }
    vmsFoldStore = new Set(Array.isArray(arr) ? arr.filter(function (x) { return typeof x === 'string'; }) : []);
  }
  return vmsFoldStore;
}
function vmsSearching() { return String(vmsFilter.q || '').trim() !== ''; }
function vmsFolded(key) { return vmsSearching() ? vmsSearchFolded.has(key) : vmsSaved().has(key); }
function vmsToggleFold(key) {
  if (vmsSearching()) { if (!vmsSearchFolded.delete(key)) vmsSearchFolded.add(key); return; }
  var s = vmsSaved();
  if (!s.delete(key)) { s.add(key); while (s.size > 2000) s.delete(s.values().next().value); }
  try { localStorage.setItem(VMS_FOLD_KEY, JSON.stringify(Array.from(s))); } catch (e) {}
}
// Returns { rows, total, entries (every VM listed, folded or not), all
// (every VM of the scope, unfiltered) }.
function vmsTreeRows() {
  var rows = [], entries = [], all = [], total = 0, multi = vmsMulti();
  vmsScopePids().forEach(function (pid) {
    var sk = 's:' + pid;
    var matched = new Map(); // host key -> its VMs listed
    var any = 0;
    var match = function (h, g) {
      var vms = vmsOfHost(h);
      if (!vms.length) return false;
      any += vms.length;
      var es = vms.map(function (vm) { return vmsEntry(h, vm); });
      es.forEach(function (e) { all.push(e); });
      var ok = es.filter(function (e) { return vmsMatch(e, g); });
      if (!ok.length) return false;
      matched.set(devKey(h), ok);
      return true;
    };
    var sub = devTreeRows(vmsGroupsOf(pid), match, function (key) { return vmsFolded(sk + '|' + key); });
    var vmsOf = function (hosts) { var out = []; (hosts || []).forEach(function (h) { out.push.apply(out, matched.get(devKey(h)) || []); }); return out; };
    var count = vmsOf(sub.devs).length;
    if (!any || !count) return; // (a server without VMs, or none listed by the search / filters: no section)
    total += count;
    var d = vmsData.get(pid);
    var sFolded = multi && vmsFolded(sk);
    if (multi) rows.push({ kind: 'server', key: sk, pid: pid, name: srvName(pid), color: srvColor(pid), count: count, folded: sFolded, vms: vmsOf(sub.devs), err: d && d.err ? d.err : '' });
    entries.push.apply(entries, vmsOf(sub.devs));
    if (sFolded) return;
    sub.rows.forEach(function (r) {
      if (r.kind === 'device') {
        var h = r.dev, es = matched.get(devKey(h)) || [];
        var hk = sk + '|h:' + devKey(h);
        var hf = vmsFolded(hk);
        rows.push({ kind: 'host', key: hk, host: h, group: r.group, depth: r.depth, count: es.length, folded: hf, vms: es });
        if (!hf) es.forEach(function (e) { rows.push({ kind: 'vm', e: e, group: r.group, depth: r.depth + 1 }); });
        return;
      }
      rows.push({ kind: r.kind, key: sk + '|' + r.key, name: r.name, path: r.path, depth: r.depth, ungrouped: r.ungrouped, folded: r.folded,
        count: vmsOf(r.devs).length, vms: vmsOf(r.devs) });
    });
  });
  return { rows: rows, total: total, entries: entries, all: all };
}

// ── Selection (the Devices page's way: devices_select.go) ──
var vmsSel = new Map(); // entry key -> entry
var vmsAnchor = null, vmsRowOrder = [], vmsHidden = 0;
function vmsGroupState(es) {
  var c = (es || []).filter(vmsOpenable), n = 0;
  c.forEach(function (e) { if (vmsSel.has(e.key)) n++; });
  return { total: c.length, state: !c.length || !n ? 'none' : n === c.length ? 'all' : 'some' };
}
function vmsBoxHtml(e) {
  var on = vmsOpenable(e);
  return dpCheckHtml(vmsSel.has(e.key) ? 'all' : 'none', !on, false, on ? T('devices.select.selectTip') : vmsWhy(e));
}
function vmsGroupBoxHtml(es) {
  var g = vmsGroupState(es);
  return dpCheckHtml(g.state, !g.total, true, g.total ? T('vms.select.groupTip') : T('picker.multi.unavailable'));
}
function vmsToggle(e, ev) {
  if (!vmsOpenable(e)) return;
  var on = !vmsSel.has(e.key);
  var a = (ev && ev.shiftKey && vmsAnchor && vmsAnchor !== e.key) ? vmsRowOrder.findIndex(function (x) { return x.key === vmsAnchor; }) : -1;
  var b = a >= 0 ? vmsRowOrder.findIndex(function (x) { return x.key === e.key; }) : -1;
  if (a >= 0 && b >= 0) {
    for (var i = Math.min(a, b); i <= Math.max(a, b); i++) {
      var x = vmsRowOrder[i];
      if (!vmsOpenable(x)) continue;
      if (on) vmsSel.set(x.key, x); else vmsSel.delete(x.key);
    }
  } else if (on) vmsSel.set(e.key, e); else vmsSel.delete(e.key);
  vmsAnchor = e.key;
}
function vmsToggleMany(es) {
  var c = (es || []).filter(vmsOpenable);
  if (!c.length) return;
  var allOn = c.every(function (e) { return vmsSel.has(e.key); });
  c.forEach(function (e) { if (allOn) vmsSel.delete(e.key); else vmsSel.set(e.key, e); });
}
// The selection against the data: what is gone or no longer opens leaves it
// (its fresh record kept); a server not read yet keeps its ticked VMs.
function vmsPrune(tree) {
  var fresh = new Map();
  tree.all.forEach(function (e) { fresh.set(e.key, e); });
  var next = new Map();
  vmsSel.forEach(function (e, k) {
    var f = fresh.get(k);
    if (f) { if (vmsOpenable(f)) next.set(k, f); }
    else { var d = vmsData.get(e.pid); if (!d || !d.loaded) next.set(k, e); }
  });
  vmsSel = next;
  var listed = new Set(tree.entries.map(function (e) { return e.key; }));
  vmsHidden = 0;
  vmsSel.forEach(function (e, k) { if (!listed.has(k)) vmsHidden++; });
}
function vmsBarEl() {
  var bar = document.createElement('div'); bar.className = 'ds-bar'; bar.id = 'vms-bar'; bar.style.display = 'none';
  bar.setAttribute('role', 'toolbar'); bar.setAttribute('aria-label', T('vms.select.barLabel')); bar.setAttribute('data-i18n-aria', 'vms.select.barLabel');
  var cnt = document.createElement('span'); cnt.className = 'ds-count'; cnt.id = 'vms-count-sel'; cnt.setAttribute('aria-live', 'polite');
  var sp = document.createElement('span'); sp.className = 'spacer';
  var clr = document.createElement('button'); clr.className = 'btn-sm'; clr.id = 'vms-clear';
  clr.textContent = T('devices.select.clear'); clr.setAttribute('data-i18n', 'devices.select.clear');
  clr.addEventListener('click', function () { vmsClear(); });
  var mos = document.createElement('button'); mos.className = 'btn-sm'; mos.id = 'vms-mosaic';
  mos.title = T('vms.select.mosaicTip'); mos.setAttribute('data-i18n-title', 'vms.select.mosaicTip');
  var mi = document.createElement('span'); mi.className = 'tg-ico'; mi.innerHTML = TG_ICON;
  var ml = document.createElement('span'); ml.textContent = T('devices.select.mosaic'); ml.setAttribute('data-i18n', 'devices.select.mosaic');
  mos.appendChild(mi); mos.appendChild(ml);
  mos.addEventListener('click', function () { vmsConnect(true); });
  var go = document.createElement('button'); go.className = 'btn-primary'; go.id = 'vms-go';
  go.textContent = T('vms.select.open'); go.setAttribute('data-i18n', 'vms.select.open');
  go.addEventListener('click', function () { vmsConnect(false); });
  bar.appendChild(cnt); bar.appendChild(sp); bar.appendChild(clr); bar.appendChild(mos); bar.appendChild(go);
  return bar;
}
function vmsBar() {
  var bar = document.getElementById('vms-bar');
  if (!bar || bar.className !== 'ds-bar') return;
  var n = vmsSel.size;
  bar.style.display = n ? '' : 'none';
  var c = document.getElementById('vms-count-sel');
  if (c) {
    c.innerHTML = i18nEsc(T('vms.select.selected', { count: n })) +
      (vmsHidden ? ' <span class="ds-hidden">' + i18nEsc(T('devices.select.hidden', { count: vmsHidden })) + '</span>' : '');
    c.title = Array.from(vmsSel.values()).map(function (e) { return e.vm.name; }).join(', ');
  }
}
function vmsClear() {
  vmsSel = new Map(); vmsAnchor = null; vmsHidden = 0;
  if (typeof currentRoute !== 'undefined' && currentRoute === 'vms') vmsFill();
  vmsBar();
}

// ── Opening consoles ──
function vmsJob(e) { return { dev: vmDevice(e.host, e.vm), host: e.host, vm: e.vm, kind: 'vm' }; }
function vmsJobKey(job) { return devKey(job.dev); }
function vmsNote(job) { return function (text) { devPickerNote(job.vm.name + ': ' + text); }; }
// One console here (the main window): vm_console.go's own path.
function vmsLaunchHere(job) { return vmOpen(job.host, job.vm, 'auto', { notify: vmsNote(job) }); }
// One console in a session window (into: the key whose window it joins).
async function vmsOpenWin(job, into) {
  var dev = job.dev, key = vmsJobKey(job), say = vmsNote(job);
  var why = vmsWhy({ host: job.host, vm: job.vm });
  if (why) { say(why); return 'blocked'; }
  var ex = await reachLaunchExisting(key);
  if (ex) return ex;
  if (!(await privacyEnsure(job.host, 'remote', { notify: say }))) return 'cancelled';
  var opts = Object.assign({ session: -1, viewOnly: false, profile: devPid(dev) }, vmWindowOpts(dev));
  if (into) opts.into = into;
  var r = '', until = Date.now() + TG_MOVE_WAIT_MS;
  for (;;) {
    try { r = String(await reachNative('reachOpenWindow', String(dev.id), String(dev.hostname || dev.id), opts)); }
    catch (e) { say(T('viewer.windowOpenFailed', { error: reachErrText(e) })); return 'error'; }
    if (r !== 'pending' || Date.now() >= until) break;
    await tgSleep(TG_MOVE_POLL_MS); // (its window is still being created)
  }
  if (r === 'pending') { say(T('viewer.windowOpenFailedNoReason')); return 'error'; }
  if (r === 'opening' || r === 'moved') {
    reachDetachedIds.add(key);
    if (!reachDetachedInfo[key]) reachDetachedInfo[key] = { session: -1, viewOnly: false };
  }
  return r;
}
var VMS_PLAN = { key: vmsJobKey, open: vmsOpenWin, here: vmsLaunchHere, tabOf: function (job) { return reachTabOfDevice(vmsJobKey(job)); } };
// "Open the consoles" (mosaic false) / "Open as mosaic": every ticked VM that
// opens, one after the other, the Devices page's way (dsPlan).
async function vmsConnect(mosaic) {
  if (!vmsSel.size) return 'none';
  if (typeof dpBatch !== 'undefined' && dpBatch) return 'busy';
  var jobs = Array.from(vmsSel.values()).filter(vmsOpenable).map(vmsJob);
  if (!jobs.length) return 'none';
  if (jobs.length > DP_MANY_TABS && !confirm(T('devices.select.confirmMany', { count: jobs.length }))) return 'cancelled';
  vmsClear();
  var how = dsPlan(jobs, !!mosaic, VMS_PLAN);
  var r = await dpRun(jobs, how && how.launch);
  if (how && how.after) await how.after(jobs, r);
  return r;
}
// A VM's own console (its row, its button).
function vmsOpenOne(e) {
  var why = vmsWhy(e);
  if (why) { reachNotify(why, 'warn'); return Promise.resolve('blocked'); }
  return vmOpen(e.host, e.vm, reachDetachedMode ? 'here' : 'auto');
}

// ── Power actions ──
// What a VM in this state offers: [primary, secondaries...].
function vmsActionsFor(vm) {
  var s = vm && vm.state;
  if (s === 'running') return ['shutdown', 'restart', 'stop', 'save', 'pause'];
  if (s === 'paused') return ['resume', 'stop'];
  if (s === 'off' || s === 'saved') return ['start'].concat(s === 'saved' ? ['stop'] : []);
  return [];
}
function vmsPowerWhy(host) {
  if (vmsPowerDenied.has(devKey(host))) return T('vms.action.denied');
  if (!devAgentReachable(host) || (host.agent && host.agent.live === false)) return T('vm.reason.hostOffline');
  return '';
}
function vmsConfirm(e, action) {
  var a = VMS_ACTIONS[action];
  if (!a || !a.confirm) return Promise.resolve(true);
  return new Promise(function (resolve) {
    var answer = false;
    var m = vmModal('vms.confirm.title', 'vms-confirm', function () { resolve(answer); });
    var p = document.createElement('p'); p.className = 'vms-confirm-text'; p.id = 'vms-confirm-text';
    p.textContent = T(a.confirm, { vm: e.vm.name, host: reachDevName(e.host) });
    m.box.appendChild(p);
    var acts = document.createElement('div'); acts.className = 'modal-actions';
    var no = document.createElement('button'); no.className = 'btn-ghost'; no.id = 'vms-confirm-cancel'; no.textContent = T('common.cancel');
    no.addEventListener('click', function () { m.close(); });
    var yes = document.createElement('button'); yes.className = 'exp-btn-danger'; yes.id = 'vms-confirm-ok'; yes.textContent = T(a.label);
    yes.addEventListener('click', function () { answer = true; m.close(); });
    acts.appendChild(no); acts.appendChild(yes);
    m.box.appendChild(acts);
  });
}
async function vmsAction(e, action) {
  var a = VMS_ACTIONS[action];
  if (!a || vmsActionsFor(e.vm).indexOf(action) < 0) return 'none';
  var why = vmsPowerWhy(e.host);
  if (why) { reachNotify(why, 'warn'); return 'blocked'; }
  if (!(await vmsConfirm(e, action))) return 'cancelled';
  var words = { action: T(a.label), vm: e.vm.name };
  var r = null, j = null;
  try {
    r = await devApi(e.host, 'POST', '/api/hyperv/devices/' + encodeURIComponent(String(e.host.id)) + '/vms/' + encodeURIComponent(String(e.vm.rawId || e.vm.id)) + '/action', { action: action }, true);
    try { j = await r.json(); } catch (x) {}
  } catch (err) { reachNotify(T('vms.action.failed', Object.assign({ error: reachErrText(err) }, words)), 'warn'); return 'error'; }
  if (r && r.status === 202) { reachNotify(T('vms.action.pending', words), 'warn'); return 'pending'; }
  if (r && r.ok) {
    reachNotify(T('vms.action.sent', words));
    var pid = e.pid;
    setTimeout(function () { vmsLoadPid(pid, true); }, 4000); // (the host reports the new state)
    return 'sent';
  }
  if (r && r.status === 403) {
    vmsPowerDenied.add(devKey(e.host));
    reachNotify(T('vms.action.denied'), 'warn');
    if (typeof currentRoute !== 'undefined' && currentRoute === 'vms') vmsFill();
    return 'denied';
  }
  if (r && r.status === 404) { reachNotify(T('vm.err.notFound'), 'warn'); vmsLoadPid(e.pid, true); return 'error'; }
  var msg = (j && typeof j.error === 'string' && j.error) ? vmsText(j.error, 200) : ('HTTP ' + (r ? r.status : '?'));
  reachNotify(T('vms.action.failed', Object.assign({ error: msg }, words)), 'warn');
  return 'error';
}
// The "..." menu of a VM's other actions.
var vmsPop = null;
function vmsPopClose() {
  var p = vmsPop;
  if (!p) return;
  vmsPop = null;
  if (p.parentElement && typeof p.parentElement.removeChild === 'function') { try { p.parentElement.removeChild(p); } catch (e) {} }
  if (typeof document.removeEventListener === 'function') { document.removeEventListener('mousedown', vmsPopOutside, true); document.removeEventListener('keydown', vmsPopKey, true); }
}
function vmsPopOutside(ev) { if (vmsPop && ev && !(vmsPop.contains && vmsPop.contains(ev.target))) vmsPopClose(); }
function vmsPopKey(ev) { if (ev && ev.key === 'Escape') vmsPopClose(); }
function vmsMenuOpen(at, e, actions) {
  vmsPopClose();
  var pop = document.createElement('div'); pop.className = 'vms-pop'; pop.id = 'vms-pop'; pop.setAttribute('role', 'menu');
  actions.forEach(function (act) {
    var b = document.createElement('button'); b.type = 'button'; b.id = 'vms-pop-' + act; b.setAttribute('role', 'menuitem');
    if (VMS_ACTIONS[act].confirm) b.className = 'danger';
    b.textContent = T(VMS_ACTIONS[act].label);
    b.addEventListener('click', function (ev) { if (ev && ev.stopPropagation) ev.stopPropagation(); vmsPopClose(); vmsAction(e, act); });
    pop.appendChild(b);
  });
  document.body.appendChild(pop);
  vmsPop = pop;
  var x = 0, y = 0;
  if (at && typeof at.getBoundingClientRect === 'function') { var r = at.getBoundingClientRect(); x = r.left; y = (typeof r.bottom === 'number' ? r.bottom : r.top + r.height) + 4; }
  pop.style.left = Math.round(x) + 'px'; pop.style.top = Math.round(y) + 'px';
  var pr = pop.getBoundingClientRect(), vw = window.innerWidth || 0, vh = window.innerHeight || 0;
  if (vw && pr.width && x + pr.width > vw - 6) pop.style.left = Math.max(6, vw - pr.width - 6) + 'px';
  if (vh && pr.height && y + pr.height > vh - 6) pop.style.top = Math.max(6, y - pr.height - 30) + 'px';
  setTimeout(function () {
    if (vmsPop !== pop || typeof document.addEventListener !== 'function') return;
    document.addEventListener('mousedown', vmsPopOutside, true);
    document.addEventListener('keydown', vmsPopKey, true);
  }, 0);
  return pop;
}

// ── The page ──
function vmsTableHead() {
  var th = function (key, style) { return '<th' + (style ? ' style="' + style + '"' : '') + (key ? ' data-i18n="' + key + '">' + TH(key) : '>') + '</th>'; };
  return '<tr>' + th('', 'width:40px') + th('vms.col.name') + th('vms.col.state') + th('vms.col.cpu') + th('vms.col.memory') +
    th('vms.col.uptime') + th('vms.col.ip') + th('vms.col.host') + th('devices.col.actions', 'text-align:end') + '</tr>';
}
function renderVmsPage(pc) {
  var h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('nav.vms') + '</h1><div class="breadcrumb">' + TH('vms.inventory') + (vmsMulti() ? ' · ' + esc(multiFilterLabel()) : activeServerNote()) + '</div>';
  var acts = document.createElement('div'); acts.className = 'page-actions';
  var refresh = document.createElement('button'); refresh.className = 'btn-sm'; refresh.id = 'vms-refresh'; refresh.textContent = T('common.refresh');
  refresh.addEventListener('click', function () { vmsRefresh(); });
  acts.appendChild(refresh);
  h.appendChild(acts);
  pc.appendChild(h);
  var body = document.createElement('div'); body.className = 'page-body';
  var bar = document.createElement('div'); bar.className = 'filter-bar';
  var q = document.createElement('input'); q.id = 'vq'; q.style.minWidth = '220px'; q.placeholder = T('vms.searchPlaceholder'); q.value = vmsFilter.q;
  q.addEventListener('input', function (ev) { vmsFilter.q = String((ev && ev.target ? ev.target.value : q.value) || ''); vmsSearchFolded = new Set(); vmsFill(); });
  var st = document.createElement('select'); st.id = 'vq-state';
  st.innerHTML = '<option value="">' + TH('devices.filter.allStates') + '</option>' +
    VMS_STATES.map(function (s) { return '<option value="' + s + '">' + TH(VM_STATE_TEXT[s]) + '</option>'; }).join('') +
    '<option value="other">' + TH('vms.filter.otherStates') + '</option>';
  st.value = vmsFilter.state;
  st.addEventListener('change', function (ev) { vmsFilter.state = String((ev && ev.target ? ev.target.value : st.value) || ''); vmsFill(); });
  var hs = document.createElement('select'); hs.id = 'vq-host';
  hs.addEventListener('change', function (ev) { vmsFilter.host = String((ev && ev.target ? ev.target.value : hs.value) || ''); vmsFill(); });
  var sp = document.createElement('span'); sp.className = 'spacer';
  var cnt = document.createElement('span'); cnt.className = 'muted-hint';
  var cn = document.createElement('span'); cn.id = 'vms-count'; cn.textContent = '0';
  var cl = document.createElement('span'); cl.id = 'vms-count-label'; cl.textContent = T('devices.resultsWord', { count: 0 });
  cl.style.marginInlineStart = '4px';
  cnt.appendChild(cn); cnt.appendChild(cl);
  bar.appendChild(q); bar.appendChild(st); bar.appendChild(hs); bar.appendChild(sp); bar.appendChild(cnt);
  body.appendChild(bar);
  var panel = document.createElement('div'); panel.className = 'panel';
  var pb = document.createElement('div'); pb.className = 'panel-body flush'; pb.style.overflowX = 'auto';
  var table = document.createElement('table'); table.className = 'dt'; table.id = 'vms-table';
  var thead = document.createElement('thead'); thead.innerHTML = vmsTableHead();
  var tbody = document.createElement('tbody'); tbody.id = 'vms-tbody';
  tbody.addEventListener('mousedown', function (ev) { if (ev && ev.shiftKey && dpIsCheck(ev) && typeof ev.preventDefault === 'function') ev.preventDefault(); });
  table.appendChild(thead); table.appendChild(tbody); pb.appendChild(table); panel.appendChild(pb);
  body.appendChild(panel);
  if (vmsOn()) body.appendChild(vmsBarEl());
  pc.appendChild(body);
  vmsFill();
  vmsKick();
  vmsPollStart();
}
// The host filter's choices: the hosts of the scope that have VMs.
function vmsHostOptions(all) {
  var sel = document.getElementById('vq-host');
  if (!sel || sel.tagName !== 'SELECT') return;
  var seen = new Map();
  all.forEach(function (e) { var k = devKey(e.host); if (!seen.has(k)) seen.set(k, e); });
  var list = Array.from(seen.values()).sort(function (a, b) { return vmsHostName(a.host).localeCompare(vmsHostName(b.host)); });
  var several = vmsMulti() && new Set(list.map(function (e) { return e.pid; })).size > 1;
  if (vmsFilter.host && !seen.has(vmsFilter.host)) vmsFilter.host = '';
  var html = '<option value="">' + TH('vms.filter.allHosts') + '</option>' + list.map(function (e) {
    var k = devKey(e.host);
    return '<option value="' + esc(k) + '">' + esc(vmsHostName(e.host) + (several ? ' (' + srvName(e.pid) + ')' : '')) + '</option>';
  }).join('');
  if (sel._vmsHtml !== html) { sel.innerHTML = html; sel._vmsHtml = html; }
  sel.value = vmsFilter.host;
}
function vmsLoadingAny() {
  return vmsScopePids().some(function (pid) { var d = vmsData.get(pid); return !d || !d.loaded; });
}
function vmsFill() {
  var tbody = document.getElementById('vms-tbody');
  if (!tbody || tbody.tagName !== 'TBODY') return;
  var tree = vmsTreeRows();
  vmsHostOptions(tree.all);
  vmsPrune(tree);
  vmsRowOrder = tree.rows.filter(function (r) { return r.kind === 'vm'; }).map(function (r) { return r.e; });
  var cn = document.getElementById('vms-count'); if (cn && cn.textContent !== tree.total) cn.textContent = tree.total;
  var cl = document.getElementById('vms-count-label'); if (cl) domText(cl, T('devices.resultsWord', { count: tree.total }));
  // Rows kept by key, built again only when what they show changed
  // (dom_patch.go): hover, focus, an open menu's row survive a refresh.
  var items = tree.rows.map(function (r) {
    var p = r.kind === 'vm' ? vmsVmRowParts(r) : vmsHeadRowParts(r);
    return { key: (r.kind === 'vm' ? 'v:' + r.e.key : 'h:' + r.key), sig: p.sig, data: r,
      build: function () { return r.kind === 'vm' ? vmsVmRow(r, p) : vmsHeadRow(r, p); } };
  });
  if (!tree.rows.length) {
    var key = vmsLoadingAny() ? 'common.loading' : tree.all.length ? 'vms.noMatch' : 'vms.empty';
    var html = '<td colspan="' + VMS_COLS + '" style="text-align:center;padding:30px;color:var(--muted2)">' + TH(key) + '</td>';
    items.push({ key: 'empty', sig: html, data: null, build: function () {
      var tr = document.createElement('tr');
      tr.innerHTML = html;
      tr.id = 'vms-empty';
      return tr;
    } });
  }
  domKeyed(tbody, items);
  vmsBar();
}
// Server / tenant / group / host row: a click (or Enter / Space) folds it;
// its box ticks every VM under it that opens. Its parts (what it shows, as
// strings: its signature) are computed without building it; its listeners
// read the row it shows now (tr._domData: the row kept across refreshes).
function vmsHeadRowParts(row) {
  var cls = 'tree-row tree-' + (row.kind === 'host' ? 'host' : row.kind) + (row.folded ? ' folded' : '');
  var chk = vmsOn() ? vmsGroupBoxHtml(row.vms) : '';
  var cnt = '<span class="tree-count">' + TH('vms.count', { count: row.count }) + '</span>';
  var title, html;
  if (row.kind === 'server') {
    title = row.name;
    var st = row.err ? '<span class="spacer"></span><span class="srv-state err" title="' + TH('vms.readFailed', { error: row.err }) + '">' + TH('vms.readFailed', { error: row.err }) + '</span>' : '';
    html = '<td colspan="' + VMS_COLS + '" style="border-inline-start:4px solid ' + row.color + '"><div class="tree-head">' + DEV_TREE_ICONS.chev + chk +
      '<span class="srv-dot" style="background:' + row.color + '"></span><span class="tree-name">' + esc(row.name) + '</span>' + cnt + st + '</div></td>';
  } else if (row.kind === 'tenant') {
    title = row.name;
    html = '<td colspan="' + VMS_COLS + '"><div class="tree-head">' + DEV_TREE_ICONS.chev + chk + DEV_TREE_ICONS.tenant +
      '<span class="tree-name">' + esc(row.name) + '</span>' + cnt + '</div></td>';
  } else if (row.kind === 'host') {
    var h = row.host, on = devAgentReachable(h) && !(h.agent && h.agent.live === false);
    title = T('vms.hostTip', { host: vmsHostName(h) });
    var pad = Math.min(Math.max(row.depth, 0), DEV_TREE_MAX_DEPTH + 1) * DEV_TREE_INDENT;
    html = '<td></td><td colspan="' + (VMS_COLS - 1) + '"><div class="tree-head" style="padding-inline-start:' + pad + 'px">' + DEV_TREE_ICONS.chev + chk +
      VMS_ICON.host + '<span class="dot ' + (on ? 'online' : 'offline') + '"></span><span class="tree-name">' + esc(vmsHostName(h)) + '</span>' + devPrivacyIcon(h) +
      '<span class="tag-chip ' + (on ? 'on' : 'off') + '">' + TH(on ? 'vms.host.online' : 'vms.host.offline') + '</span>' + cnt + '</div></td>';
  } else {
    title = row.path || row.name;
    var pad2 = Math.min(row.depth, DEV_TREE_MAX_DEPTH) * DEV_TREE_INDENT;
    html = '<td></td><td colspan="' + (VMS_COLS - 1) + '"><div class="tree-head" style="padding-inline-start:' + pad2 + 'px">' + DEV_TREE_ICONS.chev + chk +
      (row.ungrouped ? DEV_TREE_ICONS.ungrouped : DEV_TREE_ICONS.group) + '<span class="tree-name">' + esc(row.name) + '</span>' + cnt + '</div></td>';
  }
  return { cls: cls, title: title, html: html, sig: cls + '\u0001' + title + '\u0001' + html };
}
function vmsHeadRow(row, parts) {
  var p = parts || vmsHeadRowParts(row);
  var tr = document.createElement('tr');
  tr.className = p.cls;
  tr.dataset.treeKey = row.key;
  tr.tabIndex = 0;
  tr.setAttribute('aria-expanded', row.folded ? 'false' : 'true');
  tr.title = p.title;
  tr.innerHTML = p.html;
  tr._domData = row;
  var cur = function () { return tr._domData || row; };
  var toggle = function (refocus) {
    var key = cur().key;
    vmsToggleFold(key);
    vmsFill();
    if (!refocus) return;
    var tb = document.getElementById('vms-tbody');
    Array.prototype.forEach.call((tb && tb.children) || [], function (r) { if (r.dataset && r.dataset.treeKey === key && r.focus) r.focus(); });
  };
  tr.addEventListener('click', function (ev) {
    if (vmsOn() && dpIsCheck(ev)) { if (ev.stopPropagation) ev.stopPropagation(); vmsToggleMany(cur().vms); vmsFill(); return; }
    toggle(false);
  });
  tr.addEventListener('keydown', function (ev) {
    if (vmsOn() && (ev.key === ' ' || ev.key === 'Enter') && dpIsCheck(ev)) {
      ev.preventDefault(); vmsToggleMany(cur().vms); vmsFill(); dpRefocus('treeKey', cur().key, document.getElementById('vms-tbody')); return;
    }
    if (ev && ev.target && ev.target !== tr) return;
    if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); toggle(true); }
  });
  return tr;
}
function vmsStateChip(vm) {
  var cls = vm.state === 'running' ? 'on' : vm.state === 'paused' ? 'warn' : 'off';
  return '<span class="tag-chip ' + cls + '">' + TH(VM_STATE_TEXT[vm.state] || 'vm.state.unknown') + '</span>';
}
function vmsDash() { return '<span class="muted-hint">-</span>'; }
function vmsRowButtons(e) {
  var vm = e.vm, why = vmsWhy(e), pwhy = vmsPowerWhy(e.host), acts = vmsActionsFor(vm);
  var btn = function (act, title, icon, cls, disabled) {
    return '<button class="icon-btn' + (cls ? ' ' + cls : '') + '" title="' + esc(title) + '" data-act="' + act + '"' + (disabled ? ' disabled' : '') + '>' + icon + '</button>';
  };
  var out = btn('console', why || T('vms.console'), VM_ICON, 'primary', !!why);
  if (!vmsPowerDenied.has(devKey(e.host)) && acts.length) {
    var first = acts[0], more = acts.slice(1).filter(function (a) { return a !== 'restart'; });
    out += btn(first, pwhy || T(VMS_ACTIONS[first].label), first === 'shutdown' ? VMS_ICON.shutdown : VMS_ICON.start, VMS_ACTIONS[first].confirm ? 'vm-danger' : '', !!pwhy);
    if (acts.indexOf('restart') >= 0) out += btn('restart', pwhy || T(VMS_ACTIONS.restart.label), VMS_ICON.restart, 'vm-danger', !!pwhy);
    if (more.length) out += btn('more', pwhy || T('vms.action.more'), VMS_ICON.more, '', !!pwhy);
  }
  return out;
}
// A VM's row: its parts (signature) first, the row built only when they
// changed; its listeners read the entry it shows now (tr._domData).
function vmsVmRowParts(row) {
  var e = row.e, vm = e.vm;
  var why = vmsWhy(e);
  var pad = Math.min(Math.max(row.depth, 0), DEV_TREE_MAX_DEPTH + 1) * DEV_TREE_INDENT;
  var running = vm.state === 'running';
  var ip = vm.ips.length ? '<span class="vm-ip" title="' + esc(vm.ips.join(', ')) + '">' + esc(vm.ips[0]) + (vm.ips.length > 1 ? ' +' + (vm.ips.length - 1) : '') + '</span>' : vmsDash();
  var html =
    '<td></td>' +
    '<td><div class="cell-hostname" style="padding-inline-start:' + pad + 'px">' + (vmsOn() ? vmsBoxHtml(e) : '') +
      '<span class="vm-row-ico">' + VM_ICON + '</span>' + esc(vm.name) + (vm.guestOs ? '<span class="vm-guest" title="' + esc(vm.guestOs) + '">' + esc(vm.guestOs) + '</span>' : '') + '</div></td>' +
    '<td>' + vmsStateChip(vm) + '</td>' +
    '<td class="vm-num">' + (running && vm.cpu !== null ? TH('info.percent', { n: i18nNum(vm.cpu, 0) }) : vmsDash()) + '</td>' +
    '<td class="vm-num">' + (vm.mem !== null && vm.mem > 0 ? esc(reachFmtBytes(vm.mem)) : vmsDash()) + '</td>' +
    '<td class="vm-num">' + (running && vm.uptime ? esc(cxnInfoUptime(vm.uptime)) : vmsDash()) + '</td>' +
    '<td>' + ip + '</td>' +
    '<td><div class="vm-hostcell" title="' + esc(reachDevName(e.host)) + '">' + esc(vmsHostName(e.host)) + '</div></td>' +
    '<td class="actions">' + vmsRowButtons(e) + '</td>';
  var cls = why ? 'dev-offline' : '', title = why || T('vms.console');
  return { cls: cls, title: title, html: html, sig: cls + '\u0001' + title + '\u0001' + html };
}
function vmsVmRow(row, parts) {
  var p = parts || vmsVmRowParts(row);
  var tr = document.createElement('tr');
  if (p.cls) tr.className = p.cls;
  tr.dataset.vmKey = row.e.key;
  tr.innerHTML = p.html;
  tr.title = p.title;
  tr._domData = row;
  var cur = function () { return (tr._domData || row).e; };
  tr.addEventListener('click', function (ev) {
    var e = cur();
    if (vmsOn() && dpIsCheck(ev)) { if (ev.stopPropagation) ev.stopPropagation(); vmsToggle(e, ev); vmsFill(); return; }
    var b = ev && ev.target && typeof ev.target.closest === 'function' ? ev.target.closest('button[data-act]') : null;
    if (b) {
      if (ev.stopPropagation) ev.stopPropagation();
      if (b.disabled) return;
      var act = b.dataset.act;
      if (act === 'console') vmsOpenOne(e);
      else if (act === 'more') vmsMenuOpen(b, e, vmsActionsFor(e.vm).slice(1).filter(function (a) { return a !== 'restart'; }));
      else vmsAction(e, act);
      return;
    }
    vmsOpenOne(e);
  });
  tr.addEventListener('keydown', function (ev) {
    var e = cur();
    if (vmsOn() && (ev.key === ' ' || ev.key === 'Enter') && dpIsCheck(ev)) { ev.preventDefault(); vmsToggle(e, ev); vmsFill(); dpRefocus('vmKey', e.key, document.getElementById('vms-tbody')); }
  });
  return tr;
}

// ── Refresh: periodic, the button, the server's pushes ──
// Every VMS_POLL_MS, or what Settings > Application > "Device list refresh"
// says (devices_live.go dlIntervalMs: the device lists' pace, Manual = none),
// read again at each tick (a change of the setting counts from the next one).
function vmsPollMs() { return typeof dlIntervalMs === 'function' ? dlIntervalMs() : VMS_POLL_MS; }
function vmsPollStart() {
  if (vmsPollTimer !== null) return;
  var ms = vmsPollMs();
  if (!ms) return;
  vmsPollTimer = setTimeout(vmsPollTick, ms);
}
function vmsPollTick() {
  if (vmsPollTimer !== null) clearTimeout(vmsPollTimer);
  vmsPollTimer = null;
  if (typeof currentRoute === 'undefined' || currentRoute !== 'vms') return;
  if (!(typeof document !== 'undefined' && document.hidden)) vmsScopePids().forEach(function (pid) { vmsLoadPid(pid, true); }); // (the window hidden: nothing read)
  vmsPollStart();
}
// The hosts asked to enumerate again (an ephemeral push, no task), the
// device lists and the VMs read again.
async function vmsRefresh() {
  var btn = document.getElementById('vms-refresh');
  if (btn) btn.disabled = true;
  try {
    var seen = new Set(), hosts = [];
    vmsAll().forEach(function (e) { var k = devKey(e.host); if (!seen.has(k) && hosts.length < VMS_LIVE_MAX) { seen.add(k); hosts.push(e.host); } });
    await Promise.all(hosts.map(function (h) {
      return devApi(h, 'POST', '/api/hyperv/devices/' + encodeURIComponent(String(h.id)) + '/live', undefined, true).catch(function () {});
    }));
    if (hosts.length && VMS_LIVE_WAIT_MS > 0) await new Promise(function (res) { setTimeout(res, VMS_LIVE_WAIT_MS); });
    try { await loadOverview(); } catch (e) {}
    await Promise.all(vmsScopePids().map(function (pid) { return vmsLoadPid(pid, true); }));
  } finally {
    if (btn) btn.disabled = false;
    vmsChanged();
  }
}
// HYPERV_VMS_UPDATED (Obliance hyperV.service ingestVMs): the fresh list of
// one host, applied only to a host this page already lists for this user.
function vmsOnPushed(pid, msg) {
  if (!msg || typeof msg !== 'object') return;
  var d = vmsData.get(pid || ''), hostId = Number(msg.hostDeviceId);
  if (!d || !d.loaded || !Number.isInteger(hostId) || !d.byHost.has(hostId)) return;
  var list = (Array.isArray(msg.vms) ? msg.vms : []).map(function (v) {
    return (v && typeof v === 'object') ? Object.assign({}, v, { hostDeviceId: hostId }) : v;
  });
  var m = vmsMerge(new Map(), list);
  var fresh = m.get(hostId) || [];
  var was = d.byHost.get(hostId) || [];
  fresh.forEach(function (v) { if (v.tenantId === null) { var o = was.find(function (x) { return x.id === v.id; }); v.tenantId = o ? o.tenantId : null; } });
  d.byHost.set(hostId, fresh);
  vmsChanged();
}
if (typeof toolSocketHooks !== 'undefined' && Array.isArray(toolSocketHooks)) {
  toolSocketHooks.push(function (sock, pid) {
    if (!sock || typeof sock.on !== 'function') return;
    sock.on('HYPERV_VMS_UPDATED', function (msg) { vmsOnPushed(pid || '', msg); });
  });
}

// ── The "+" list of a session window (ui.go devViewPicker) ──
// Under each Hyper-V host, its VMs (vmsAll: this page's servers, tenants,
// hosts and 'hyperv:view' gates, read by vmsKick while the list is open:
// vmsDataOn), each with its box (picker_multi.go: its service 'vm'), its
// state, its console button; a click, Enter or that button opens its
// console as a tab of this window (agent_tools.go devPickerLaunch: dpVmLaunch).
// One that cannot open now is greyed with why (vmsWhy). The search lists a
// host for its own fields (then every VM of it) or for its VMs' names (then
// those VMs only).
var dpVmIdx = new Map();   // devKey(host) -> [entry], read as the list is drawn (dpVmPrepare)
var dpVmDevs = new Map();  // entry key -> its console's device (vmDevice + _vmEntry)
var dpVmQOnly = new Set(); // devKey(host): listed by the search through a VM's name only
var dpVmQ = '';
function dpVmPrepare(f) {
  dpVmIdx = new Map(); dpVmDevs = new Map(); dpVmQOnly = new Set();
  dpVmQ = String((f && f.q) || '').trim().toLowerCase();
  vmsAll().forEach(function (e) {
    var k = devKey(e.host), a = dpVmIdx.get(k);
    if (!a) { a = []; dpVmIdx.set(k, a); }
    a.push(e);
  });
}
function dpVmEntriesOf(host) { return (host && !devIsVm(host) && dpVmIdx.get(devKey(host))) || []; }
function dpVmNameHas(e, q) { return String(e.vm.name || '').toLowerCase().indexOf(q) >= 0; }
// The search (ui.go devFilterMatch: f.qMore), once the host's own fields
// did not match: a VM's name does.
function dpVmQMore(host, q) {
  var hit = dpVmEntriesOf(host).some(function (e) { return dpVmNameHas(e, q); });
  if (hit) dpVmQOnly.add(devKey(host));
  return hit;
}
// The VMs listed under a host.
function dpVmListed(host) {
  var es = dpVmEntriesOf(host);
  if (!dpVmQ || !dpVmQOnly.has(devKey(host))) return es;
  return es.filter(function (e) { return dpVmNameHas(e, dpVmQ); });
}
// A VM as the list's selection and openings hold it: its console's device,
// its entry with it (dpServices, dpVmLaunch).
function dpVmDev(e) {
  var d = dpVmDevs.get(e.key);
  if (!d) { d = vmDevice(e.host, e.vm); d._vmEntry = e; dpVmDevs.set(e.key, d); }
  return d;
}
// Every VM listed under these hosts (folded or not): what the selection keeps.
function dpVmListedDevs(hosts) {
  var out = [];
  (hosts || []).forEach(function (h) { dpVmListed(h).forEach(function (e) { out.push(dpVmDev(e)); }); });
  return out;
}
// Its state's dot (the top bar's search: gsVmDot's rule).
function dpVmDot(e) {
  if (vmsHostWhy(e.host)) return 'offline';
  var s = e.vm.state;
  return s === 'running' ? 'online' : (s === 'paused' || s === 'saved') ? 'warn' : 'offline';
}
function dpVmRowParts(row, e, view) {
  var vm = e.vm, dev = dpVmDev(e), why = vmsWhy(e);
  var pad = Math.min(Math.max(row.depth, 0) + 1, DEV_TREE_MAX_DEPTH + 1) * DEV_TREE_INDENT;
  var box = view.select ? dpCheckHtml(dpSel.has(devKey(dev)) ? 'all' : 'none', !!why, false, why || T('picker.multi.selectTip')) : '';
  var html =
    '<td></td>' +
    '<td><div class="cell-hostname" style="padding-inline-start:' + pad + 'px">' + box + '<span class="dot ' + dpVmDot(e) + '"></span>' +
      '<span class="vm-row-ico">' + VM_ICON + '</span>' + esc(vm.name) +
      (vm.guestOs ? '<span class="vm-guest" title="' + esc(vm.guestOs) + '">' + esc(vm.guestOs) + '</span>' : '') + '</div></td>' +
    '<td><span class="tag-chip vm">' + TH('search.kindVm') + '</span></td>' +
    '<td>' + vmsDash() + '</td>' +
    '<td>' + vmsStateChip(vm) + '</td>' +
    '<td></td>' +
    '<td class="actions"><button class="icon-btn primary" title="' + esc(why || T('vms.console')) + '" data-act="vm"' + (why ? ' disabled' : '') + '>' + VM_ICON + '</button></td>';
  var cls = 'dp-vm-row' + (why ? ' dev-offline' : ''), title = why || T('vm.tabTitle', { vm: vm.name, host: vmsHostName(e.host) });
  return { cls: cls, title: title, html: html, sig: cls + '\u0001' + title + '\u0001' + html };
}
function dpVmRowEl(row, e, view, p) {
  var tr = document.createElement('tr');
  tr.className = p.cls;
  tr.innerHTML = p.html;
  tr.title = p.title;
  tr.tabIndex = 0;
  tr.dataset.devKey = devKey(dpVmDev(e));
  tr.dataset.vmKey = e.key;
  var cur = function () { return (tr._domData && tr._domData.e) || e; };
  var tick = function (ev) {
    var d = dpVmDev(cur());
    if (typeof ev.stopPropagation === 'function') ev.stopPropagation();
    dpToggleDev(d);
    devPickerRefresh();
    return d;
  };
  tr.addEventListener('click', function (ev) {
    if (view.select && dpIsCheck(ev)) { tick(ev); return; } // (its box: ticks, opens nothing)
    var b = ev && ev.target && typeof ev.target.closest === 'function' ? ev.target.closest('button[data-act]') : null;
    if (b) { if (typeof ev.stopPropagation === 'function') ev.stopPropagation(); if (b.disabled) return; }
    devPickerLaunch(dpVmDev(cur()), 'vm');
  });
  tr.addEventListener('keydown', function (ev) {
    if (view.select && (ev.key === ' ' || ev.key === 'Enter') && dpIsCheck(ev)) {
      ev.preventDefault();
      dpRefocus('devKey', devKey(tick(ev)));
      return;
    }
    if (ev.target && ev.target !== tr) return; // (its console button: its own key)
    if (ev.key === 'Enter') { ev.preventDefault(); devPickerLaunch(dpVmDev(cur()), 'vm'); }
  });
  return tr;
}
// The rows under a host (ui.go devViewFill: view.children).
function dpVmRows(row, view) {
  return dpVmListed(row.dev).map(function (e) {
    var p = dpVmRowParts(row, e, view);
    return { key: 'v:' + e.key, sig: p.sig, data: { row: row, e: e }, build: function () { return dpVmRowEl(row, e, view, p); } };
  });
}
// A VM's console here: its gates said in the list, then vm_console.go's own
// path into this window (vmOpen 'here': its tab here brought forward, one
// in another window brought to the front there, else a new tab here).
function dpVmLaunch(dev, opts) {
  var e = dev && dev._vmEntry;
  if (!e) return Promise.resolve('none');
  var say = { notify: function (text) { toolsSay(opts, e.vm.name + ': ' + text); } };
  var why = vmsWhy(e);
  if (why) { say.notify(why); return Promise.resolve('blocked'); }
  return vmOpen(e.host, e.vm, 'here', say);
}
`
