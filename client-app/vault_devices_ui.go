package main

// vault_devices_ui.go — the password vault's machine picker:
// a saved password may be offered only on some machines ("the password of
// SRV-01's local admin, on SRV-01 only"). The entry form's "Machines" field
// says where it is offered (every machine of its tenant, as before, or only
// the ticked ones) and opens the picker: the Desktop's own device tree
// (devViewFill, ui.go: servers, tenants, groups, machines, the search), with
// the "+" list's boxes (picker_multi.go dpCheckHtml: a box per machine, a
// three-state box per server / tenant / group ticking everything under it).
//
// A Hyper-V host lists its VMs under it (those the server lists for this
// user: vms_page.go's data, vm_console.go's 'hyperv:view' gate): a ticked
// VM is its host's item plus { vmId, vmName }, offered in that VM's console
// only (never in the host's own sessions, and the host's items never in its
// VM consoles: vault.go sessionMatch).
//
// A ticked machine is kept as { origin, tenantId, uuid, id, name }: its
// server (the origin the proxy records a session's, vaultOriginOf), its
// tenant, its stable uuid, its numeric id and a name to show. Go matches a
// session against it (vault.go sessionMatch) from the server's own session
// answer, never from this page. A machine the tree does not list right now
// (its server signed out) stays ticked: only the operator unticks.
//
// The place chosen in the form bounds the picker: a server
// lists that server's machines only, a tenant that tenant's, "every server"
// every machine the tree lists (the tree itself: only what this user may
// see, ui.go). A place chosen afterwards that leaves ticked machines out
// unticks them, and the field says which (vault.devices.dropped).
//
// Injected verbatim after the other tool blocks (buildUI, not through
// Sprintf: '%' is safe): no backtick here.

const vaultDevicesCSS = `
/* ── Password vault: machine picker (vault_devices_ui.go) ── */
.vault-dev-summary{font-size:12px;color:var(--text);background:var(--input-bg);border-radius:10px;padding:8px 11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.vault-dev-summary.all{color:var(--muted)}
.vault-dev-acts{display:flex;gap:6px;flex-wrap:wrap}
.vault-dev-dropped{font-size:11px;color:var(--accent-l);background:rgb(var(--accent-rgb) / .08);border-radius:8px;padding:6px 10px}
.modal-overlay.vdp-overlay{z-index:360}
.vdp-overlay .modal-box{width:min(900px,94vw);max-height:88vh;display:flex;flex-direction:column}
.vdp-bar{display:flex;align-items:center;gap:10px;margin-bottom:8px}
.vdp-bar input{flex:1;min-width:0;background:var(--input-bg);border:none;border-radius:8px;padding:7px 10px;font-size:12px;color:var(--text);outline:none;font-family:inherit}
.vdp-list{flex:1;min-height:180px;overflow:auto;border-radius:10px;background:var(--bg2)}
.vdp-foot{display:flex;align-items:center;gap:10px;margin-top:10px}
.vdp-foot .vdp-count{font-size:12px;font-weight:600;color:var(--text)}
.vdp-foot .vdp-unlisted{font-size:11px;color:var(--muted)}
.vdp-foot .spacer{flex:1}
.vdp-foot .btn-primary{width:auto}
`

const vaultDevicesJS = `
// ── Password vault: machine picker (vault_devices_ui.go) ────────────────────
// A server profile's origin by its id (as a session's is recorded).
function vaultPidOrigin(pid) {
  var pr = (typeof serverProfiles !== 'undefined' ? serverProfiles : []).find(function (p) { return p && p.id === pid; });
  return pr ? vaultOriginOf(pr.serverUrl) : '';
}
function vaultOriginPid(origin) {
  var pr = (typeof serverProfiles !== 'undefined' ? serverProfiles : []).find(function (p) { return p && vaultOriginOf(p.serverUrl) === origin; });
  return pr ? pr.id : undefined;
}
// A machine of the tree as a restriction item, or null when its server or
// tenant cannot be told (it cannot be ticked then). Its tenant: its own (or
// its group's), else the one its server's session is on (a server that lists
// one tenant).
function vaultDevItem(dev) {
  if (!dev) return null;
  var pid = devPid(dev), origin = vaultPidOrigin(pid);
  var t = devTenant(dev);
  if (!t) {
    var st = (typeof srvState === 'function' && pid !== undefined) ? srvState(pid) : null;
    if (st && st.me && Number.isInteger(st.me.tenantId) && st.me.tenantId > 0) t = st.me.tenantId;
    else if (typeof currentTenantId !== 'undefined' && Number.isInteger(currentTenantId) && currentTenantId > 0 && pid === reachServerId()) t = currentTenantId;
  }
  var uuid = typeof dev.uuid === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(dev.uuid) ? dev.uuid : '';
  var id = Number.isInteger(dev.id) && dev.id > 0 ? dev.id : 0;
  if (!origin || !t || (!uuid && !id)) return null;
  return { origin: origin, tenantId: t, uuid: uuid, id: id, name: String(dev.hostname || dev.displayName || uuid || id) };
}
// A Hyper-V VM of a host as a restriction item: its host's item and the
// VM (GUID, lower case; its name to show). Go matches it to that VM's
// console only (vault.go sessionMatch), the host's own sessions never.
function vaultVmItem(host, vm) {
  var it = vaultDevItem(host);
  if (!it || !vm || typeof vm.id !== 'string' || !VM_GUID_RE.test(vm.id)) return null;
  it.vmId = vm.id.toLowerCase();
  it.vmName = vmCleanName(vm.name) || it.vmId;
  return it;
}
function vaultDevKey(d) {
  return d.origin + '|' + d.tenantId + '|' + (d.uuid ? 'u:' + String(d.uuid).toLowerCase() : 'i:' + d.id) + (d.vmId ? '|vm:' + String(d.vmId).toLowerCase() : '');
}
// An item's name to show: the machine's, or the VM's with its host's.
function vaultDevName(d) {
  if (!d) return '';
  var host = d.name || d.uuid || String(d.id);
  return d.vmId ? T('vault.devices.vmOnHost', { vm: d.vmName || d.vmId, host: host }) : host;
}
// Whether a restriction item lies within the form's place ({ origin: ''
// every server, tenantId: 0 every tenant }).
function vaultDevInPlace(d, place) {
  if (!d) return false;
  if (!place || !place.origin) return true;
  return d.origin === place.origin && (!place.tenantId || d.tenantId === place.tenantId);
}
// The tenant's name of an item, when its server lists it.
function vaultDevTenantName(d) {
  var t = vaultTenantsOf(vaultOriginPid(d.origin)).find(function (x) { return Number(x.id) === d.tenantId; });
  return t ? String(t.name || '') : '';
}

// The form's field: what it says, "Choose machines…", "Every machine".
// onChange(restricted) after each change (never while it is built). place:
// the form's place (vaultDevInPlace), changed with setPlace(place).
function vaultDevicesField(initial, onChange, place) {
  var where = place || { origin: '', pid: '', tenantId: 0 };
  var chosen = (Array.isArray(initial) ? initial : []).filter(function (d) { return d && d.origin && d.tenantId; }).map(function (d) {
    var it = { origin: String(d.origin), tenantId: Number(d.tenantId), uuid: String(d.uuid || ''), id: Number(d.id) || 0, name: String(d.name || '') };
    if (d.vmId) { it.vmId = String(d.vmId).toLowerCase(); it.vmName = String(d.vmName || ''); }
    return it;
  });
  var wrap = document.createElement('div'); wrap.className = 'vault-field';
  var lbl = document.createElement('label'); lbl.textContent = T('vault.devices.label');
  var summary = document.createElement('div'); summary.id = 'vault-dev-summary';
  var acts = document.createElement('div'); acts.className = 'vault-dev-acts';
  var pick = document.createElement('button'); pick.className = 'btn-sm'; pick.id = 'vault-dev-pick'; pick.textContent = T('vault.devices.pick');
  var reset = document.createElement('button'); reset.className = 'btn-sm'; reset.id = 'vault-dev-reset'; reset.textContent = T('vault.devices.reset');
  acts.appendChild(pick); acts.appendChild(reset);
  var dropped = document.createElement('div'); dropped.className = 'vault-dev-dropped'; dropped.id = 'vault-dev-dropped'; dropped.style.display = 'none';
  wrap.appendChild(lbl); wrap.appendChild(summary); wrap.appendChild(dropped); wrap.appendChild(acts);
  var paint = function () {
    if (!chosen.length) {
      summary.className = 'vault-dev-summary all'; summary.title = '';
      summary.textContent = T(!where.origin ? 'vault.devices.allServers' : !where.tenantId ? 'vault.devices.allServer' : 'vault.devices.all');
      reset.style.display = 'none';
      return;
    }
    var names = chosen.map(vaultDevName);
    summary.className = 'vault-dev-summary';
    summary.textContent = T('vault.devices.only', { count: chosen.length, names: names.slice(0, 4).join(', ') + (names.length > 4 ? ', …' : '') });
    summary.title = names.join(', ');
    reset.style.display = '';
  };
  pick.addEventListener('click', function (e) {
    if (e && e.preventDefault) e.preventDefault();
    vaultOpenDevicePicker(chosen, function (list) { chosen = list; dropped.style.display = 'none'; paint(); if (onChange) onChange(chosen.length > 0); }, where);
  });
  reset.addEventListener('click', function (e) {
    if (e && e.preventDefault) e.preventDefault();
    chosen = []; dropped.style.display = 'none'; paint(); if (onChange) onChange(false);
  });
  paint();
  return { wrap: wrap,
    value: function () {
      return chosen.map(function (d) {
        var o = { origin: d.origin, tenantId: d.tenantId, uuid: d.uuid, id: d.id, name: d.name };
        if (d.vmId) { o.vmId = d.vmId; o.vmName = d.vmName || ''; }
        return o;
      });
    },
    // Another place: the machines outside it unticked, said.
    setPlace: function (p) {
      where = p || { origin: '', pid: '', tenantId: 0 };
      var out = chosen.filter(function (d) { return !vaultDevInPlace(d, where); });
      if (out.length) {
        chosen = chosen.filter(function (d) { return vaultDevInPlace(d, where); });
        var names = out.map(vaultDevName);
        dropped.textContent = T('vault.devices.dropped', { count: out.length, names: names.slice(0, 4).join(', ') + (names.length > 4 ? ', …' : '') });
        dropped.style.display = '';
        if (onChange) onChange(chosen.length > 0);
      }
      paint();
    } };
}

// The picker: the device tree with its boxes. onDone(list) on "OK" only.
var vdpSel = new Map();       // vaultDevKey -> item (ticked, listed or not)
var vdpListed = new Set();    // the keys the tree lists right now
var vdpFilter = { q: '', online: '', os: '' };
var vdpSearchFolded = new Set();
var vdpPlace = { origin: '', pid: '', tenantId: 0 }; // (the form's place: what the picker lists)
// A machine of the tree within the place: its server (by its profile, or
// its origin), its tenant when the place names one.
function vdpInPlace(dev) {
  if (!vdpPlace.origin) return true;
  var pid = devPid(dev);
  if ((vdpPlace.pid ? pid !== vdpPlace.pid : vaultPidOrigin(pid) !== vdpPlace.origin)) return false;
  if (!vdpPlace.tenantId) return true;
  var it = vaultDevItem(dev);
  if (it) return it.tenantId === vdpPlace.tenantId;
  var t = devTenant(dev); // (no usable identity: listed greyed, unless another tenant's)
  return !t || t === vdpPlace.tenantId;
}
var vdpHooks = {
  dev: function (dev) {
    var it = vdpInPlace(dev) ? vaultDevItem(dev) : null;
    return dpCheckHtml(it && vdpSel.has(vaultDevKey(it)) ? 'all' : 'none', !it, false, it ? T('vault.devices.tickTip') : T('vault.devices.unavailable'));
  },
  group: function (devs) {
    var items = (devs || []).filter(vdpInPlace).map(vaultDevItem).filter(Boolean), n = 0;
    items.forEach(function (it) { if (vdpSel.has(vaultDevKey(it))) n++; });
    var state = !items.length || !n ? 'none' : n === items.length ? 'all' : 'some';
    return dpCheckHtml(state, !items.length, true, items.length ? T('vault.devices.groupTip') : T('vault.devices.unavailable'));
  },
  toggleDev: function (dev) {
    var it = vdpInPlace(dev) ? vaultDevItem(dev) : null;
    if (!it) return;
    var k = vaultDevKey(it);
    if (vdpSel.has(k)) vdpSel.delete(k); else vdpSel.set(k, it);
  },
  toggleMany: function (devs) {
    var items = (devs || []).filter(vdpInPlace).map(vaultDevItem).filter(Boolean);
    if (!items.length) return;
    var all = items.every(function (it) { return vdpSel.has(vaultDevKey(it)); });
    items.forEach(function (it) { var k = vaultDevKey(it); if (all) vdpSel.delete(k); else vdpSel.set(k, it); });
  },
};
// ── A Hyper-V host's VMs, listed under it (vms_page.go's data: only the VMs
// the server lists for this user, of hosts the device lists hold; a
// non-administrator's only once the host's own list answered, 'hyperv:view').
var vdpVmAsked = new Set();   // what this picker opening already asked for (a server's list, a host's gate)
var vdpRefreshTimer = null;
function vdpRefreshSoon() {
  if (vdpRefreshTimer) return;
  vdpRefreshTimer = setTimeout(function () { vdpRefreshTimer = null; if (document.getElementById('vdp-overlay')) vdpRefresh(); }, 60);
}
function vdpVmsOf(host) {
  if (!host || devIsVm(host) || typeof vmsOfHost !== 'function' || typeof vmsLoadPid !== 'function') return [];
  if (typeof devIsLocal === 'function' && devIsLocal(host)) return [];
  var pid = devPid(host);
  if (!vdpVmAsked.has('p:' + pid)) {
    vdpVmAsked.add('p:' + pid);
    // (asked outside vdpRefresh: the server menu's own narrowing, not the picker's)
    setTimeout(function () { vmsLoadPid(pid).then(vdpRefreshSoon, function () {}); }, 0);
  }
  var d = vmsData.get(pid), raw = d && d.byHost.get(Number(host.id));
  if (raw && raw.length && !vmIsAdmin(host)) {
    var hk = 'h:' + devKey(host), c = vmLists.get(devKey(host));
    if ((!c || Date.now() - c.at >= VM_LIST_TTL_MS) && !vdpVmAsked.has(hk)) {
      vdpVmAsked.add(hk);
      vmLoad(host).then(vdpRefreshSoon, function () {});
    }
  }
  return vdpInPlace(host) ? vmsOfHost(host) : [];
}
function vdpVmRowParts(row, vm) {
  var host = row.dev, it = vaultVmItem(host, vm);
  var on = !!(it && vdpSel.has(vaultDevKey(it)));
  var pad = Math.min(row.depth + 1, DEV_TREE_MAX_DEPTH + 1) * DEV_TREE_INDENT;
  var html = '<td></td>' +
    '<td><div class="cell-hostname" style="padding-inline-start:' + pad + 'px">' +
      dpCheckHtml(on ? 'all' : 'none', !it, false, it ? T('vault.devices.vmTickTip') : T('vault.devices.unavailable')) +
      '<span class="vm-row-ico">' + VM_ICON + '</span>' + i18nEsc(vm.name) + '</div></td>' +
    '<td><span class="muted-hint">' + i18nEsc(vm.guestOs || T('vault.devices.vmChip')) + '</span></td>' +
    '<td></td>' +
    '<td><span class="muted-hint">' + i18nEsc(T(VM_STATE_TEXT[vm.state] || 'vm.state.unknown')) + '</span></td>' +
    '<td></td><td></td>';
  return { it: it, html: html, title: T('vault.devices.vmOnHost', { vm: vm.name, host: String(host.hostname || host.id) }) };
}
function vdpToggleVm(it) {
  if (!it) return;
  var k = vaultDevKey(it);
  if (vdpSel.has(k)) vdpSel.delete(k); else vdpSel.set(k, it);
  vdpRefresh();
}
// The rows under a host (ui.go devViewFill: view.children).
function vdpVmRows(row) {
  return vdpVmsOf(row.dev).map(function (vm) {
    var p = vdpVmRowParts(row, vm);
    return { key: 'c:' + devKey(row.dev) + '~' + vmKindOf(vm.id), sig: p.title + '\u0001' + p.html, data: { it: p.it },
      build: function () {
        var tr = document.createElement('tr');
        tr.className = 'vdp-vm-row';
        tr.innerHTML = p.html;
        tr.title = p.title;
        if (p.it) tr.dataset.devKey = vaultDevKey(p.it);
        var cur = function () { return (tr._domData && tr._domData.it) || p.it; };
        tr.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); vdpToggleVm(cur()); });
        tr.addEventListener('keydown', function (e) {
          if ((e.key === ' ' || e.key === 'Enter') && dpIsCheck(e)) {
            e.preventDefault();
            var it = cur();
            vdpToggleVm(it);
            if (it) dpRefocus('devKey', vaultDevKey(it), document.getElementById('vdp-tbody'));
          }
        });
        return tr;
      } };
  });
}
function vdpView() {
  return { tbodyId: 'vdp-tbody', countId: 'vdp-count', filter: function () { return vdpFilter; }, searchFolded: function () { return vdpSearchFolded; },
    actions: [], usable: function () { return true; }, refresh: vdpRefresh,
    open: function (dev) { vdpHooks.toggleDev(dev); vdpRefresh(); }, // (the row itself: ticks, opens nothing)
    select: vdpHooks,
    children: vdpVmRows,
    onTree: function (tree) {
      var listed = new Set();
      (tree.devs || []).forEach(function (dev) {
        var it = vaultDevItem(dev);
        if (it) listed.add(vaultDevKey(it));
        vdpVmsOf(dev).forEach(function (vm) { var v = vaultVmItem(dev, vm); if (v) listed.add(vaultDevKey(v)); });
      });
      vdpListed = listed;
    } };
}
// The form's place, whatever the main list's server / tenant menu narrows
// it to (every server: every server; a server: its section; a tenant: that
// tenant), put back right after. vdpFilter.only drops the rows outside it
// (ui.go devFilterMatch: an older server, single-server mode).
function vdpRefresh() {
  var saved = (typeof multiFilter !== 'undefined') ? multiFilter : null;
  if (saved) multiFilter = { pid: vdpPlace.origin ? (vdpPlace.pid || '') : '', tenantId: (vdpPlace.origin && vdpPlace.pid && vdpPlace.tenantId) ? vdpPlace.tenantId : null };
  try { devViewFill(vdpView()); } finally { if (saved) multiFilter = saved; }
  vdpFooter();
}
function vdpFooter() {
  var c = document.getElementById('vdp-sel-count');
  if (c) c.textContent = T('vault.devices.count', { count: vdpSel.size });
  var u = document.getElementById('vdp-unlisted'), n = 0;
  vdpSel.forEach(function (it, k) { if (!vdpListed.has(k)) n++; });
  if (u) { u.textContent = n ? T('vault.devices.unlisted', { count: n }) : ''; u.style.display = n ? '' : 'none'; }
}
function vaultOpenDevicePicker(chosen, onDone, place) {
  vdpSel = new Map();
  (chosen || []).forEach(function (d) { vdpSel.set(vaultDevKey(d), d); });
  vdpPlace = place && place.origin ? { origin: place.origin, pid: place.pid || vaultOriginPid(place.origin) || '', tenantId: place.tenantId || 0 } : { origin: '', pid: '', tenantId: 0 };
  vdpFilter = { q: '', online: '', os: '', only: vdpInPlace };
  vdpSearchFolded = new Set();
  vdpVmAsked = new Set();
  var ov = document.createElement('div'); ov.className = 'modal-overlay visible vdp-overlay'; ov.id = 'vdp-overlay';
  var box = document.createElement('div'); box.className = 'modal-box';
  box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T('vault.devices.title'));
  var head = document.createElement('div'); head.className = 'modal-head';
  var h = document.createElement('h3'); h.textContent = T('vault.devices.title');
  var x = document.createElement('button'); x.className = 'close'; x.id = 'vdp-close'; x.title = T('common.cancel');
  x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
  head.appendChild(h); head.appendChild(x);
  var desc = document.createElement('p'); desc.className = 'desc'; desc.textContent = T('vault.devices.desc');
  var bar = document.createElement('div'); bar.className = 'vdp-bar';
  var q = document.createElement('input'); q.id = 'vdp-q'; q.type = 'text'; q.placeholder = T('devices.searchPlaceholder');
  q.addEventListener('input', function (e) { vdpFilter.q = e.target.value; vdpSearchFolded = new Set(); vdpRefresh(); });
  var hint = document.createElement('span'); hint.className = 'muted-hint';
  var cnt = document.createElement('span'); cnt.id = 'vdp-count'; cnt.textContent = '0';
  var cl = document.createElement('span'); cl.id = 'vdp-count-label'; cl.style.marginInlineStart = '.25em';
  hint.appendChild(cnt); hint.appendChild(cl);
  bar.appendChild(q); bar.appendChild(hint);
  var list = document.createElement('div'); list.className = 'vdp-list';
  var table = document.createElement('table'); table.className = 'dt';
  var thead = document.createElement('thead'); thead.innerHTML = devTableHead();
  var tbody = document.createElement('tbody'); tbody.id = 'vdp-tbody';
  table.appendChild(thead); table.appendChild(tbody); list.appendChild(table);
  var foot = document.createElement('div'); foot.className = 'vdp-foot';
  var sc = document.createElement('span'); sc.className = 'vdp-count'; sc.id = 'vdp-sel-count';
  var un = document.createElement('span'); un.className = 'vdp-unlisted'; un.id = 'vdp-unlisted'; un.style.display = 'none';
  var sp = document.createElement('span'); sp.className = 'spacer';
  var clr = document.createElement('button'); clr.className = 'btn-sm'; clr.id = 'vdp-clear'; clr.textContent = T('common.clear');
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'vdp-cancel'; cancel.textContent = T('common.cancel');
  var ok = document.createElement('button'); ok.className = 'btn-primary'; ok.id = 'vdp-ok'; ok.textContent = T('common.ok');
  foot.appendChild(sc); foot.appendChild(un); foot.appendChild(sp); foot.appendChild(clr); foot.appendChild(cancel); foot.appendChild(ok);
  box.appendChild(head); box.appendChild(desc); box.appendChild(bar); box.appendChild(list); box.appendChild(foot);
  ov.appendChild(box);
  var done = false;
  var finish = function (list) {
    if (done) return;
    done = true;
    if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
    document.removeEventListener('keydown', esc, true);
    if (list && onDone) onDone(list);
  };
  // (Escape closes this picker only, not the entry form under it.)
  var esc = function (e) { if (e && e.key === 'Escape') { if (e.stopImmediatePropagation) e.stopImmediatePropagation(); if (e.stopPropagation) e.stopPropagation(); finish(null); } };
  x.addEventListener('click', function () { finish(null); });
  cancel.addEventListener('click', function () { finish(null); });
  clr.addEventListener('click', function () { vdpSel = new Map(); vdpRefresh(); });
  ok.addEventListener('click', function () { finish(Array.from(vdpSel.values())); });
  ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) finish(null); });
  document.addEventListener('keydown', esc, true);
  document.body.appendChild(ov);
  vdpRefresh();
  try { q.focus(); } catch (e) {}
}
`
