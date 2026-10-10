package main

// vault_page.go — the Vault page: the password vault's own
// entry in the sidebar (OVERVIEW > Vault), in every mode (one server, every
// server, a local base). Settings > Vault keeps the vault's settings only
// (vault_ui.go renderVaultSection); every entry is managed here: list, add,
// edit, delete, reveal (the vault code asked each time, vault_ui.go
// vaultOpenRevealModal), the machines a password is offered on.
//
// The list is grouped like the other multi-server pages (scope_pages.go
// pgRenderGrouped: coloured server headers, then a sub-section per tenant):
//   - "General" first: the entries offered on every server (no origin);
//   - one section per server: its whole-server entries (tenant 0) first,
//     then one sub-section per tenant;
//   - "Local": the local base's reusable credentials (each with the number
//     of connections using it) and the connections holding a secret of
//     their own (a connection using a credential is counted under it).
// The top bar's server / tenant choice bounds it (vaultPageScope): one
// server: that server + General; every server: everything; a local base:
// Local + General. Search, a type filter, a count; alphabetical in each group.
//
// A locked vault shows its unlock form (Windows Hello launched by itself:
// hello_auto.go) and nothing else: the vault file is sealed whole (vault.go:
// DPAPI over everything, names and places included), so no entry is known
// before it is unlocked. No secret is ever in this page unless revealed.
//
// Both consts are injected verbatim after the other tool blocks (ui.go's
// buildUI, not through Sprintf: '%' is safe): no backtick here.

const vaultPageCSS = `
/* ── The Vault page (vault_page.go) ── */
.vpage-toolbar{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.vpage-toolbar .vault-search{flex:1;min-width:160px;margin:0}
.vpage-toolbar select{background:var(--input-bg);border:none;border-radius:8px;padding:7px 10px;font-size:11.5px;color:var(--text);outline:none;font-family:inherit}
.vpage-toolbar .vpage-count{font-size:11.5px;color:var(--muted);white-space:nowrap}
.vpage-list{padding:4px 0 10px}
.vpage-list .vault-entry-card{margin:0 16px 6px 26px}
.vpage-list .pg-srv{margin-top:6px}
.vpage-list .pg-srv .srv-ico{display:flex;color:var(--accent-l)}
.vpage-state{padding:16px;max-width:360px}
.vpage-state .vault-form{gap:8px}
.vpage-empty{padding:30px 16px;text-align:center;font-size:12px;color:var(--muted2)}
.vpage-uses{margin:8px 0 0;padding:0 0 0 18px;font-size:12px;color:var(--text)}
`

const vaultPageJS = `
// ── The Vault page (vault_page.go) ──────────────────────────────────────────
var vaultPageFilter = { q: '', type: '' }; // (kept while the app runs)
var VAULT_PAGE_GLOBE = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 014 10 15.3 15.3 0 01-4 10 15.3 15.3 0 01-4-10 15.3 15.3 0 014-10z"/></svg>';
var VAULT_PAGE_LAPTOP = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="3" y="4" width="18" height="12" rx="2"/><line x1="2" y1="20" x2="22" y2="20"/></svg>';

// What the top bar's choice lets this page show: { servers (the server
// sections at all), pid ('' every server), tenantId (null every tenant),
// local (the Local section), unknown (servers no profile names any more) }.
function vaultPageScope() {
  var multi = typeof serverMode !== 'undefined' && serverMode === 'multi';
  if (!multi) {
    if (typeof activeServerIsLocal === 'function' && activeServerIsLocal()) return { servers: false, pid: '', tenantId: null, local: true, unknown: false };
    return { servers: true, pid: (typeof homePid === 'function' ? homePid() : '') || '', tenantId: null, local: false, unknown: false };
  }
  var f = (typeof multiFilter !== 'undefined' && multiFilter) ? multiFilter : { pid: '', tenantId: null };
  if (!f.pid) return { servers: true, pid: '', tenantId: null, local: true, unknown: true };
  if (typeof srvIsLocal === 'function' && srvIsLocal(f.pid)) return { servers: false, pid: '', tenantId: null, local: true, unknown: false };
  return { servers: true, pid: f.pid, tenantId: Number.isInteger(f.tenantId) ? f.tenantId : null, local: false, unknown: false };
}
function vaultPageCmp(a, b) { return String(a || '').localeCompare(String(b || ''), undefined, { sensitivity: 'base' }); }
function vaultPageTenantName(pid, en) {
  var t = vaultTenantsOf(pid).find(function (x) { return Number(x.id) === Number(en.tenantId); });
  return (t && t.name) ? String(t.name) : (en.tenantName || T('topbar.tenantFallback', { id: en.tenantId }));
}
// The type filter: '' every entry; 'machine' a password offered on every
// machine of its place; 'restricted' one offered on some machines only;
// 'local' the local base's credentials and connections.
function vaultPageMatch(f, kind, it) {
  if (f.type === 'local' && kind !== 'cred' && kind !== 'conn') return false;
  if ((f.type === 'machine' || f.type === 'restricted') && kind !== 'entry') return false;
  if (kind === 'entry' && f.type) {
    var restricted = Array.isArray(it.devices) && it.devices.length > 0;
    if (f.type === 'machine' && restricted) return false;
    if (f.type === 'restricted' && !restricted) return false;
  }
  var q = String(f.q || '').trim().toLowerCase();
  if (!q) return true;
  var hay = kind === 'entry' ? [it.label, it.username, it.tenantName, it.note].concat((Array.isArray(it.devices) ? it.devices : []).map(function (d) { return d ? [d.name, d.vmName].join(' ') : ''; }))
    : kind === 'cred' ? [it.label, it.username, it.note]
    : [it.n && it.n.label, it.n && it.n.username, it.n && it.n.host, it.path];
  return hay.some(function (v) { return String(v || '').toLowerCase().indexOf(q) >= 0; });
}
// The page's sections, from the vault's lists (data: { entries, creds,
// conns: [{ n, path }] }), the scope (vaultPageScope) and the filter:
// { sections: [{ kind: 'general', items } | { kind: 'server', pid, origin,
// name, color, known, head, tenants: [{ tid, name, items }] } | { kind:
// 'local', creds, conns }], total }. Pure: tests drive it directly.
function vaultPageModel(data, scope, f) {
  f = f || { q: '', type: '' };
  var sections = [], total = 0;
  var entries = (data && data.entries) || [], creds = (data && data.creds) || [], conns = (data && data.conns) || [];
  var byLabel = function (a, b) { return vaultPageCmp(a.label || a.username, b.label || b.username) || vaultPageCmp(a.id, b.id); };
  var general = entries.filter(function (en) { return !en.origin && vaultPageMatch(f, 'entry', en); }).sort(byLabel);
  if (general.length) { sections.push({ kind: 'general', items: general }); total += general.length; }
  if (scope.servers) {
    var srvs = [], byKey = new Map();
    (typeof serverProfiles !== 'undefined' ? serverProfiles : []).forEach(function (p) {
      if (p.local || !vaultOriginOf(p.serverUrl)) return;
      if (scope.pid && p.id !== scope.pid) return;
      var o = vaultOriginOf(p.serverUrl);
      if (byKey.has(o)) return; // (two profiles of one server: one section)
      var s = { kind: 'server', pid: p.id, origin: o, name: String(p.name || o), color: srvColor(p.id), known: true, head: [], tenants: [] };
      byKey.set(o, s); srvs.push(s);
    });
    entries.forEach(function (en) {
      if (!en.origin || !vaultPageMatch(f, 'entry', en)) return;
      var s = byKey.get(en.origin);
      if (!s) {
        if (!scope.unknown) return;
        s = { kind: 'server', pid: '', origin: en.origin, name: en.origin, color: 'var(--muted2)', known: false, head: [], tenants: [] };
        byKey.set(en.origin, s); srvs.push(s);
      }
      if (!en.tenantId) { s.head.push(en); return; }
      if (scope.tenantId !== null && scope.tenantId !== undefined && Number(en.tenantId) !== scope.tenantId) return;
      var t = s.tenants.find(function (x) { return x.tid === Number(en.tenantId); });
      if (!t) { t = { tid: Number(en.tenantId), name: s.known ? vaultPageTenantName(s.pid, en) : (en.tenantName || T('topbar.tenantFallback', { id: en.tenantId })), items: [] }; s.tenants.push(t); }
      t.items.push(en);
    });
    srvs.forEach(function (s) {
      s.head.sort(byLabel);
      s.tenants.forEach(function (t) { t.items.sort(byLabel); total += t.items.length; });
      s.tenants.sort(function (a, b) { return vaultPageCmp(a.name, b.name) || (a.tid - b.tid); });
      total += s.head.length;
      if (s.head.length || s.tenants.length) sections.push(s);
    });
  }
  if (scope.local) {
    var cs = creds.filter(function (c) { return vaultPageMatch(f, 'cred', c); }).sort(byLabel);
    var ns = conns.filter(function (x) { return x && x.n && x.n.stored && !x.n.credId && vaultPageMatch(f, 'conn', x); })
      .sort(function (a, b) { return vaultPageCmp(a.n.label, b.n.label) || vaultPageCmp(a.path, b.path); });
    if (cs.length || ns.length) { sections.push({ kind: 'local', creds: cs, conns: ns }); total += cs.length + ns.length; }
  }
  return { sections: sections, total: total };
}

function renderVaultPage(pc) {
  var h = document.createElement('div'); h.className = 'page-header';
  var note = (typeof pgMulti === 'function' && pgMulti()) ? esc(multiFilterLabel()) : activeServerNote();
  h.innerHTML = '<h1>' + TH('nav.vault') + '</h1>' + (note ? '<div class="breadcrumb">' + note + '</div>' : '');
  pc.appendChild(h);
  var body = document.createElement('div'); body.className = 'page-body';
  var panel = document.createElement('div'); panel.className = 'panel'; panel.id = 'vault-page';
  body.appendChild(panel); pc.appendChild(body);
  vaultPageRender(panel);
}
// The page's panel for the vault's state: its creation, its unlock, or its list.
function vaultPageRender(panel) {
  panel.innerHTML = '';
  if (!reachVaultManageAvailable()) {
    var na = document.createElement('div'); na.className = 'vpage-empty'; na.textContent = T('vault.page.unavailable'); panel.appendChild(na);
    return;
  }
  var again = function () { if (panel.isConnected !== false) vaultPageRender(panel); };
  vaultSyncCall('reachVaultState').then(function (st) {
    st = st || { state: 'unreadable' };
    if (panel.isConnected === false) return;
    var box = document.createElement('div'); box.className = 'vpage-state';
    if (st.state === 'unreadable') { box.textContent = T('vault.locked.unreadable'); panel.appendChild(box); return; }
    if (st.state === 'absent' || st.state === 'locked') {
      var head = document.createElement('div'); head.className = 'panel-header'; head.textContent = T('vault.manage.title');
      panel.appendChild(head);
      var wrap = document.createElement('div'); wrap.className = 'vault-form';
      if (st.state === 'locked') { var msg = document.createElement('div'); msg.className = 'sub'; msg.textContent = T('vault.manage.locked'); box.appendChild(msg); }
      box.appendChild(wrap); panel.appendChild(box);
      // (unlocked here: the local base's connections read again too)
      var done = function () { if (typeof localConnsLoad === 'function') localConnsLoad().then(function () { if (typeof overviewRedraw === 'function') overviewRedraw(); }, function () {}); again(); };
      if (st.state === 'absent') vaultMenuCreateForm(wrap, null, done); else vaultMenuUnlockForm(wrap, null, done);
      return;
    }
    vaultPageList(panel, again);
  });
}
// The vault's lists, read now: { entries, creds, conns: [{ n, path }] }.
// conns: the local base's connection tree (ui.go localConnsLoad), each
// with its folder path.
async function vaultPageData() {
  var res = await Promise.all([
    vaultSyncCall('reachVaultList'),
    typeof window.reachVaultCredentials === 'function' ? vaultSyncCall('reachVaultCredentials') : Promise.resolve([]),
    typeof localConnsLoad === 'function' ? localConnsLoad().catch(function () { return null; }) : Promise.resolve(null),
  ]);
  var conns = [];
  var lc = res[2];
  if (lc && lc.state === 'ok' && Array.isArray(lc.nodes)) {
    var byId = new Map();
    lc.nodes.forEach(function (x) { if (x && x.n) byId.set(x.n.id, x); });
    var pathOf = function (pid, depth) {
      if (!pid || depth > 16) return '';
      var p = byId.get(pid);
      if (!p) return '';
      var up = pathOf(p.parent, depth + 1);
      return (up ? up + ' / ' : '') + String(p.n.label || '');
    };
    lc.nodes.forEach(function (x) {
      if (!x || !x.n || x.n.kind === CXN_KIND_FOLDER) return;
      conns.push({ n: x.n, path: pathOf(x.parent, 0) });
    });
  }
  return { entries: Array.isArray(res[0]) ? res[0] : [], creds: Array.isArray(res[1]) ? res[1] : [], conns: conns };
}
function vaultPageList(panel, again) {
  var head = document.createElement('div'); head.className = 'panel-header vpage-toolbar';
  var search = document.createElement('input'); search.type = 'text'; search.className = 'vault-search'; search.id = 'vault-page-q';
  search.placeholder = T('vault.manage.search'); search.value = vaultPageFilter.q;
  var type = document.createElement('select'); type.id = 'vault-page-type'; type.title = T('vault.page.filterTitle');
  [['', 'vault.page.filter.all'], ['machine', 'vault.page.filter.machine'], ['restricted', 'vault.page.filter.restricted'], ['local', 'vault.page.filter.local']].forEach(function (o) {
    var op = document.createElement('option'); op.value = o[0]; op.textContent = T(o[1]); type.appendChild(op);
  });
  type.value = vaultPageFilter.type;
  var count = document.createElement('span'); count.className = 'vpage-count'; count.id = 'vault-page-count';
  var add = document.createElement('button'); add.className = 'btn-sm'; add.id = 'vault-page-add'; add.textContent = T('vault.manage.add');
  var addCred = document.createElement('button'); addCred.className = 'btn-sm'; addCred.id = 'vault-page-add-cred'; addCred.textContent = T('vault.cred.newTitle');
  var lock = document.createElement('button'); lock.className = 'btn-sm'; lock.id = 'vault-page-lock'; lock.textContent = T('vault.lockNow');
  head.appendChild(search); head.appendChild(type); head.appendChild(count); head.appendChild(add); head.appendChild(addCred); head.appendChild(lock);
  var list = document.createElement('div'); list.className = 'vpage-list'; list.id = 'vault-page-list';
  panel.appendChild(head); panel.appendChild(list);
  var scope = vaultPageScope();
  // (a local credential: where the local base is shown)
  addCred.style.display = scope.local ? '' : 'none';
  add.addEventListener('click', function () { vaultOpenEntryModal(null, again); });
  addCred.addEventListener('click', function () { vaultOpenCredModal(null, function () { again(); }); });
  lock.addEventListener('click', function () { vaultSyncCall('reachVaultLock').then(function () { if (typeof localConnsLoad === 'function') localConnsLoad().catch(function () {}); again(); }); });
  var data = null;
  var paint = function () {
    if (!data) return;
    var model = vaultPageModel(data, scope, vaultPageFilter);
    count.textContent = T('vault.page.count', { count: model.total });
    vaultPageDraw(list, model, again, !data.entries.length && !data.creds.length && !data.conns.some(function (x) { return x.n.stored && !x.n.credId; }));
  };
  search.addEventListener('input', function () { vaultPageFilter.q = search.value; paint(); });
  type.addEventListener('change', function () { vaultPageFilter.type = type.value; paint(); });
  vaultPageData().then(function (d) { data = d; paint(); }, function () { data = { entries: [], creds: [], conns: [] }; paint(); });
}
// A section header: the server's way (scope_pages.go .pg-srv), its colour
// as a stripe and a dot, or an icon (General, Local).
function vaultPageSrvHead(name, color, n, icon) {
  var h = document.createElement('div'); h.className = 'pg-srv';
  h.style.boxShadow = 'inset 4px 0 0 ' + color;
  h.innerHTML = (icon ? '<span class="srv-ico">' + icon + '</span>' : '<span class="srv-dot" style="background:' + color + '"></span>') +
    '<span>' + esc(name) + '</span><span class="cnt" style="font-weight:500;color:var(--muted2)">' + n + '</span>';
  return h;
}
function vaultPageTenHead(name, n) {
  var h = document.createElement('div'); h.className = 'pg-ten';
  h.innerHTML = '<span>' + esc(name) + '</span><span class="cnt">' + n + '</span>';
  return h;
}
function vaultPageDraw(list, model, again, vaultEmpty) {
  list.innerHTML = '';
  if (!model.total) {
    var e = document.createElement('div'); e.className = 'vpage-empty';
    e.textContent = T(vaultEmpty ? 'vault.manage.empty' : 'vault.page.noMatch');
    list.appendChild(e);
    return;
  }
  var card = function (en) { return vaultEntryCard(en, again, { place: false }); };
  model.sections.forEach(function (s) {
    if (s.kind === 'general') {
      list.appendChild(vaultPageSrvHead(T('vault.page.general'), 'var(--accent)', s.items.length, VAULT_PAGE_GLOBE));
      s.items.forEach(function (en) { list.appendChild(card(en)); });
      return;
    }
    if (s.kind === 'server') {
      var n = s.head.length;
      s.tenants.forEach(function (t) { n += t.items.length; });
      list.appendChild(vaultPageSrvHead(s.known ? s.name : T('vault.page.unknownServer', { server: s.name }), s.color, n));
      if (s.head.length) {
        list.appendChild(vaultPageTenHead(T('vault.scope.allTenants'), s.head.length));
        s.head.forEach(function (en) { list.appendChild(card(en)); });
      }
      s.tenants.forEach(function (t) {
        list.appendChild(vaultPageTenHead(t.name, t.items.length));
        t.items.forEach(function (en) { list.appendChild(card(en)); });
      });
      return;
    }
    // Local: its credentials, then its connections holding their own secret.
    list.appendChild(vaultPageSrvHead(T('vault.page.local'), (typeof LOCAL_PID !== 'undefined' && srvColor(LOCAL_PID) !== 'var(--muted2)') ? srvColor(LOCAL_PID) : 'var(--muted)', s.creds.length + s.conns.length, VAULT_PAGE_LAPTOP));
    if (s.creds.length) {
      list.appendChild(vaultPageTenHead(T('vault.page.credentials'), s.creds.length));
      s.creds.forEach(function (c) { list.appendChild(vaultCredCard(c, again)); });
    }
    if (s.conns.length) {
      list.appendChild(vaultPageTenHead(T('vault.page.connections'), s.conns.length));
      s.conns.forEach(function (x) { list.appendChild(vaultConnCard(x, again)); });
    }
  });
}
// A card: label, a line under it, its buttons.
function vaultPageCard(label, subText, buttons) {
  var c = document.createElement('div'); c.className = 'vault-entry-card';
  var info = document.createElement('div'); info.className = 'info';
  var l = document.createElement('div'); l.className = 'lbl'; l.textContent = label || '?';
  var s = document.createElement('div'); s.className = 'sub'; s.textContent = subText;
  info.appendChild(l); info.appendChild(s); c.appendChild(info);
  var acts = document.createElement('div'); acts.className = 'acts';
  buttons.forEach(function (b) {
    var bt = document.createElement('button'); bt.className = 'btn-sm'; bt.textContent = T(b[0]);
    bt.addEventListener('click', b[1]); acts.appendChild(bt);
  });
  c.appendChild(acts);
  return c;
}
function vaultCredAuthText(c) {
  if (c.authMethod === CXN_AUTH_KEY) return T(c.keyProtected ? 'vault.cred.keyProtected' : 'connections.auth.key');
  return T('connections.auth.password');
}
function vaultCredCard(c, again) {
  var uses = Array.isArray(c.uses) ? c.uses.length : 0;
  var sub = [c.username, vaultCredAuthText(c), T('vault.cred.uses', { count: uses })].filter(Boolean).join(' · ');
  return vaultPageCard(c.label, sub, [
    ['vault.manage.reveal', function () { vaultOpenRevealModal(c); }],
    ['vault.manage.editTitle', function () { vaultOpenCredModal(c, function () { again(); }); }],
    ['vault.manage.delete', function () { vaultCredDelete(c, again); }],
  ]);
}
function vaultConnCard(x, again) {
  var n = x.n;
  var where = (n.username ? n.username + '@' : '') + n.host + ':' + n.port;
  var sub = [n.kind === CXN_KIND_RDP ? 'RDP' : 'SSH', where, x.path].filter(Boolean).join(' · ');
  var reload = function () { if (typeof overviewRedraw === 'function') overviewRedraw(); again(); };
  return vaultPageCard(n.label, sub, [
    ['vault.manage.reveal', function () { vaultOpenRevealModal(n); }],
    ['vault.manage.editTitle', function () { cxnOpenConnectionModal(n, reload); }],
    ['vault.manage.delete', function () { cxnConfirmDelete(n, reload); }],
  ]);
}
// A credential still used is not deleted (vault.go Delete refuses too): the
// connections using it are listed, to give them another one first.
function vaultCredDelete(c, again) {
  var uses = Array.isArray(c.uses) ? c.uses : [];
  if (uses.length) {
    var m = vaultBaseModal('vault.cred.inUseTitle');
    var p = document.createElement('p'); p.className = 'desc'; p.textContent = T('vault.cred.inUse', { name: c.label });
    var ul = document.createElement('ul'); ul.className = 'vpage-uses'; ul.id = 'vault-cred-uses';
    uses.forEach(function (u) { var li = document.createElement('li'); li.textContent = (u.kind === CXN_KIND_RDP ? 'RDP' : 'SSH') + ' · ' + u.label; ul.appendChild(li); });
    var actions = document.createElement('div'); actions.className = 'modal-actions';
    var ok = document.createElement('button'); ok.className = 'btn-primary'; ok.textContent = T('common.ok');
    ok.addEventListener('click', m.close);
    actions.appendChild(ok);
    m.box.appendChild(p); m.box.appendChild(ul); m.box.appendChild(actions);
    return;
  }
  vaultOpenConfirmModal(T('vault.cred.deleteConfirm', { name: c.label }), function () {
    vaultSyncCall('reachVaultDelete', c.id).then(function (r) {
      if (r !== true) reachToast(T('vault.err.credInUse'), 'warn');
      again();
    });
  });
}

// ── A reusable credential of the local base: new / edit ────────────────────
// onSaved(id) once saved. The secret and the passphrase are only typed in
// here (the operator's own form), never read back: an edit leaves them
// empty to keep them.
function vaultOpenCredModal(c, onSaved) {
  var m = vaultBaseModal(c ? 'vault.cred.editTitle' : 'vault.cred.newTitle');
  var form = document.createElement('div'); form.className = 'vault-form';
  var label = vaultField('connections.field.name'); label.input.value = (c && c.label) || ''; label.input.id = 'vault-cred-label';
  var user = vaultField('connections.field.username'); user.input.value = (c && c.username) || ''; user.input.id = 'vault-cred-user';
  var auth = vaultSelectField('connections.field.authMethod'); auth.input.id = 'vault-cred-auth';
  [[CXN_AUTH_PASSWORD, 'connections.auth.password'], [CXN_AUTH_KEY, 'connections.auth.key']].forEach(function (o) {
    var op = document.createElement('option'); op.value = String(o[0]); op.textContent = T(o[1]); auth.input.appendChild(op);
  });
  auth.input.value = String((c && c.authMethod) || CXN_AUTH_PASSWORD);
  var secret = vaultField('connections.field.secret', 'password'); secret.input.id = 'vault-cred-secret';
  var key = vaultField('connections.field.privateKey', 'textarea'); key.input.id = 'vault-cred-key'; key.input.rows = 5; key.input.spellcheck = false;
  var pass = vaultField('vault.cred.passphrase', 'password'); pass.input.id = 'vault-cred-pass';
  var keepHint = c ? T('connections.field.secretKeepHint') : '';
  secret.input.placeholder = keepHint; key.input.placeholder = keepHint;
  pass.input.placeholder = (c && c.keyProtected) ? keepHint : T('vault.cred.passphraseOptional');
  var clear = document.createElement('div'); clear.className = 'vault-check-row';
  var clearCb = document.createElement('input'); clearCb.type = 'checkbox'; clearCb.id = 'vault-cred-pass-clear';
  var clearLbl = document.createElement('label'); clearLbl.setAttribute('for', 'vault-cred-pass-clear'); clearLbl.textContent = T('vault.cred.passphraseClear');
  clear.appendChild(clearCb); clear.appendChild(clearLbl);
  var note = vaultField('vault.manage.note', 'textarea'); note.input.value = (c && c.note) || '';
  var err = document.createElement('div'); err.className = 'modal-err'; err.id = 'vault-cred-err';
  var isKey = function () { return parseInt(auth.input.value, 10) === CXN_AUTH_KEY; };
  var show = function () {
    secret.wrap.style.display = isKey() ? 'none' : '';
    key.wrap.style.display = isKey() ? '' : 'none';
    pass.wrap.style.display = isKey() ? '' : 'none';
    clear.style.display = (isKey() && c && c.keyProtected) ? '' : 'none';
  };
  auth.input.addEventListener('change', show);
  [label, user, auth, secret, key, pass].forEach(function (f) { form.appendChild(f.wrap); });
  form.appendChild(clear); form.appendChild(note.wrap); form.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  var save = document.createElement('button'); save.className = 'btn-primary'; save.id = 'vault-cred-save'; save.textContent = T('vault.manage.save');
  var wipe = function () { secret.input.value = ''; key.input.value = ''; pass.input.value = ''; };
  var close = function () { wipe(); m.close(); };
  cancel.addEventListener('click', close);
  save.addEventListener('click', function () {
    var name = label.input.value.trim();
    if (!name) { err.textContent = T('connections.field.nameRequired'); return; }
    var am = isKey() ? CXN_AUTH_KEY : CXN_AUTH_PASSWORD;
    var sec = am === CXN_AUTH_KEY ? key.input.value : secret.input.value;
    // (new, or another kind of secret: it is required)
    if (!sec && (!c || (c.authMethod || CXN_AUTH_PASSWORD) !== am)) { err.textContent = T(am === CXN_AUTH_KEY ? 'vault.cred.keyRequired' : 'connections.field.secretRequired'); return; }
    var input = { id: (c && c.id) || '', kind: CXN_KIND_CREDENTIAL, label: name, username: user.input.value.trim(), authMethod: am,
      secret: sec, note: note.input.value.trim(),
      passphrase: am === CXN_AUTH_KEY ? pass.input.value : '', clearPassphrase: !!(am === CXN_AUTH_KEY && clearCb.checked && !pass.input.value) };
    save.disabled = true; err.textContent = '';
    vaultAsyncCall('reachVaultSave', input).then(function (r) { wipe(); m.close(); if (onSaved) onSaved((r && r.id) || input.id); },
      function (e) { save.disabled = false; err.textContent = vaultErrText(e); });
  });
  actions.appendChild(cancel); actions.appendChild(save);
  form.appendChild(actions);
  m.box.appendChild(form);
  show();
  setTimeout(function () { try { label.input.focus(); } catch (e) {} }, 0);
}
var CXN_KIND_CREDENTIAL = 4; // (vault_codec.go vaultKindCredential)
`
