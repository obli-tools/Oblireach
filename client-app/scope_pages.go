package main

// scope_pages.go — what the Scripts and Deployment pages show and where they
// create things, in single- and multi-server mode.
//
// Single-server mode: the active server, the session's current tenant (as
// before). Multi-server mode follows the tenant menu (multiFilter):
//   - every server: every tenant of every signed-in server, grouped by server
//     then tenant; "New ..." asks for the server and the tenant;
//   - one server, every tenant: that server's tenants; "New ..." asks for the
//     tenant;
//   - one server, one tenant: that tenant only; "New ..." creates there.
// Each (server, tenant) is read through the pinned proxy (/spt/<profile>/
// <tenant>, tenantpin.go) exactly like a device action, so Obliance's own
// tenant scoping and permissions decide what comes back: nothing is widened
// here. An older server (not "scoped": it lists its session's tenant only) is
// read on that tenant through /sp/.

const scopePagesCSS = `
/* ── Server / tenant grouping (scope_pages.go) ── */
.pg-srv{display:flex;align-items:center;gap:8px;padding:10px 16px;font-size:12.5px;font-weight:700;color:var(--text);background:rgb(var(--ink) / .035)}
.pg-srv .srv-dot{width:9px;height:9px;border-radius:50%;flex-shrink:0}
.pg-srv .st{margin-inline-start:auto;font-weight:400;font-size:11px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
.pg-srv .st.err{color:var(--danger)}
.pg-ten{display:flex;align-items:center;gap:6px;padding:9px 16px 5px 26px;font-size:11.5px;font-weight:700;color:var(--muted)}
.pg-ten .cnt,.pg-cat .cnt{font-weight:500;color:var(--muted2)}
.pg-cat{display:flex;gap:6px;padding:7px 16px 3px 34px;font-size:10.5px;font-weight:650;letter-spacing:.04em;text-transform:uppercase;color:var(--muted2)}
.pg-note{padding:8px 16px 10px 26px;font-size:11px;color:var(--muted2)}
.pg-dest{display:flex;gap:9px}
.pg-dest .scr-field{flex:1;min-width:0}
`

const scopePagesJS = `
// ── Server / tenant scope of the Scripts and Deployment pages ──────────────
function pgMulti() { return typeof serverMode !== 'undefined' && serverMode === 'multi'; }
function pgTenantName(pid, tid) {
  if (tid === null || tid === undefined) return '';
  var list = pgMulti() ? (srvState(pid).tenants || []) : (typeof tenants !== 'undefined' && Array.isArray(tenants) ? tenants : []);
  var t = list.find(function (x) { return Number(x.id) === Number(tid); });
  return t ? String(t.name || '') : T('topbar.tenantFallback', { id: tid });
}
// The (server, tenant) pairs the page shows, in the tenant menu's order.
// off: 'signedOut' | 'loading' | 'error' (a server shown with its state only).
function pgTargets() {
  if (!pgMulti()) {
    var tid = Number.isInteger(currentTenantId) ? currentTenantId : null;
    return [{ pid: homePid(), tid: tid, pinned: false, single: true }];
  }
  var out = [];
  serverProfiles.forEach(function (p) {
    if (p.local) return; // (a local base: no scripts, no deployment, no server to ask)
    if (multiFilter.pid && p.id !== multiFilter.pid) return;
    var st = srvState(p.id);
    if (st.signedIn !== true) { out.push({ pid: p.id, tid: null, off: st.signedIn === false ? 'signedOut' : (st.error ? 'error' : 'loading'), error: st.error }); return; }
    if (!st.scoped) {
      var own = st.me && Number.isInteger(st.me.tenantId) ? st.me.tenantId : null;
      out.push({ pid: p.id, tid: own, pinned: false, old: true });
      return;
    }
    (st.tenants || []).forEach(function (t) {
      var id = Number(t.id);
      if (multiFilter.pid === p.id && multiFilter.tenantId !== null && id !== multiFilter.tenantId) return;
      out.push({ pid: p.id, tid: id, pinned: true });
    });
  });
  return out;
}
function pgBase(t) { return (pgMulti() && t.pinned && t.tid) ? '/spt/' + t.pid + '/' + t.tid : srvBase(t.pid); }
async function pgApi(t, method, path, body) {
  var b = pgBase(t);
  return b === '/proxy' ? api(method, path, body) : apiAt(b, method, path, body);
}
// Runs fn(target) for every readable target: the servers side by side, the
// tenants of one server one after the other (its pinned requests are
// serialized anyway). A failure is kept on its target (t.err), never thrown.
async function pgEach(targets, fn) {
  var byPid = new Map();
  targets.forEach(function (t) { if (t.off) return; if (!byPid.has(t.pid)) byPid.set(t.pid, []); byPid.get(t.pid).push(t); });
  await Promise.all(Array.from(byPid.values()).map(async function (ts) {
    for (var i = 0; i < ts.length; i++) { try { await fn(ts[i]); } catch (e) { ts[i].err = e; } }
  }));
}
function pgTargetKey(t) { return t.pid + '/' + (t.tid === null ? '' : t.tid); }
// Where a listed item lives: its own tenant when the server lists several
// (a master admin reading tenant 1 sees every tenant's items), else the
// tenant it was read on (a script shared by the master into this tenant).
function pgOwnerTarget(item, t, targets) {
  var tid = Number(item && item.tenantId);
  if (pgMulti() && Number.isInteger(tid) && tid > 0 && tid !== t.tid) {
    var same = targets.find(function (x) { return x.pid === t.pid && x.tid === tid; });
    if (same) return same;
  }
  return t;
}
// Renders rows grouped server > tenant > sub-group into container.
// rows: [{ t, group, groupOrder, el }] (t: the owner target; group: the
// sub-group's label, '' for none). Server and tenant headers only in
// multi-server mode; sub-group headers whenever there is more than one.
function pgRenderGrouped(container, targets, rows, emptyHtml) {
  container.innerHTML = '';
  var multi = pgMulti();
  var any = false;
  var pids = [];
  targets.forEach(function (t) { if (pids.indexOf(t.pid) < 0) pids.push(t.pid); });
  pids.forEach(function (pid) {
    var ts = targets.filter(function (t) { return t.pid === pid; });
    var srvRows = rows.filter(function (r) { return r.t.pid === pid; });
    if (multi) {
      var h = document.createElement('div'); h.className = 'pg-srv';
      h.style.boxShadow = 'inset 4px 0 0 ' + srvColor(pid);
      var off = ts[0] && ts[0].off;
      var errT = ts.find(function (t) { return t.err; });
      var st = off === 'signedOut' ? TH('scope.signIn', { server: srvName(pid) })
        : off === 'error' ? TH('devices.server.error', { server: srvName(pid), error: ts[0].error || '' })
        : off === 'loading' ? TH('common.loading')
        : (ts[0] && ts[0].old) ? TH('devices.server.currentTenantOnly') : '';
      h.innerHTML = '<span class="srv-dot" style="background:' + srvColor(pid) + '"></span><span>' + esc(srvName(pid)) + '</span>' +
        '<span class="cnt" style="font-weight:500;color:var(--muted2)">' + srvRows.length + '</span>' +
        (st ? '<span class="st' + (off === 'error' ? ' err' : '') + '" title="' + st + '">' + st + '</span>' : '');
      container.appendChild(h);
      if (off) return;
    }
    ts.forEach(function (t) {
      var tRows = srvRows.filter(function (r) { return r.t === t; });
      if (multi && (tRows.length || t.err)) {
        var th = document.createElement('div'); th.className = 'pg-ten';
        th.innerHTML = '<span>' + esc(pgTenantName(t.pid, t.tid) || srvName(t.pid)) + '</span><span class="cnt">' + tRows.length + '</span>';
        container.appendChild(th);
      }
      if (t.err) {
        var n = document.createElement('div'); n.className = 'pg-note';
        n.textContent = (t.err && t.err.status === 403) ? T('scope.noAccess') : T('status.loadFailed', { error: (t.err && t.err.message) || String(t.err) });
        container.appendChild(n);
      }
      var groups = [];
      tRows.forEach(function (r) { if (!groups.some(function (g) { return g.label === r.group; })) groups.push({ label: r.group, order: r.groupOrder }); });
      groups.sort(function (a, b) { return (a.order - b.order) || String(a.label).localeCompare(String(b.label)); });
      groups.forEach(function (g) {
        var gRows = tRows.filter(function (r) { return r.group === g.label; });
        if (groups.length > 1 || (g.label && multi)) {
          var gh = document.createElement('div'); gh.className = 'pg-cat';
          gh.innerHTML = '<span>' + esc(g.label || T('scripts.mgmt.noCategory')) + '</span><span class="cnt">' + gRows.length + '</span>';
          container.appendChild(gh);
        }
        gRows.forEach(function (r) { container.appendChild(r.el); any = true; });
      });
    });
  });
  if (!any && !container.querySelector('.pg-srv,.pg-note')) container.innerHTML = emptyHtml;
  else if (!any && !multi) container.insertAdjacentHTML('beforeend', emptyHtml);
}
// "Where to create it": a server picker then a tenant picker, limited to what
// the tenant menu leaves open. Returns { wrap (null: nothing to choose),
// get() -> target or null, onChange(fn) }.
function pgDestPicker(targets) {
  var ok = targets.filter(function (t) { return !t.off; });
  var listeners = [];
  if (ok.length <= 1) return { wrap: null, get: function () { return ok[0] || null; }, onChange: function () {} };
  var wrap = document.createElement('div'); wrap.className = 'pg-dest';
  var pids = [];
  ok.forEach(function (t) { if (pids.indexOf(t.pid) < 0) pids.push(t.pid); });
  var sf = scriptsField('scope.server', 'select');
  pids.forEach(function (pid) { var o = document.createElement('option'); o.value = pid; o.textContent = srvName(pid) || pid; sf.input.appendChild(o); });
  var tf = scriptsField('scope.tenant', 'select');
  var fillTenants = function () {
    tf.input.innerHTML = '';
    ok.filter(function (t) { return t.pid === sf.input.value; }).forEach(function (t) {
      var o = document.createElement('option'); o.value = String(t.tid === null ? '' : t.tid);
      o.textContent = pgTenantName(t.pid, t.tid) || T('scope.sessionTenant'); tf.input.appendChild(o);
    });
    // Prefer the tenant the menu or the session is on.
    var pref = (multiFilter && multiFilter.pid === sf.input.value && multiFilter.tenantId !== null) ? multiFilter.tenantId
      : (srvState(sf.input.value).me && srvState(sf.input.value).me.tenantId);
    if (pref !== null && pref !== undefined && tf.input.querySelector('option[value="' + pref + '"]')) tf.input.value = String(pref);
  };
  var get = function () {
    var pid = sf.input.value, v = tf.input.value;
    return ok.find(function (t) { return t.pid === pid && String(t.tid === null ? '' : t.tid) === v; }) || null;
  };
  var fire = function () { var t = get(); listeners.forEach(function (f) { try { f(t); } catch (e) {} }); };
  sf.input.addEventListener('change', function () { fillTenants(); fire(); });
  tf.input.addEventListener('change', fire);
  if (pids.indexOf(homePid()) >= 0) sf.input.value = homePid();
  fillTenants();
  if (pids.length > 1) wrap.appendChild(sf.wrap);
  wrap.appendChild(tf.wrap);
  return { wrap: wrap, get: get, onChange: function (f) { listeners.push(f); } };
}
`
