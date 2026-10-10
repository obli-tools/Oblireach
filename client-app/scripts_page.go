package main

// scripts_page.go — the "Scripts" nav page: lists the scripts of every server /
// tenant the tenant menu leaves open (single-server mode: the active server,
// current tenant), grouped by server > tenant > category (scope_pages.go),
// lets a user with the
// `scripts.manage` tenant capability create/edit/delete one (including the
// "availableInReach" flag, toggleable here even for a script not currently
// allowed in Reach), and shows everyone else the same list read-only. No new
// server endpoint: this consumes /api/scripts exactly like the web client
// does, so a 403 from the server is the sole source of truth for "can I
// manage" — never guessed at client-side.
//
// Both consts are injected verbatim after the other tool blocks (ui.go's
// buildUI, not through Sprintf: '%' is safe), same pattern as vault_ui.go.

const scriptsPageCSS = `
/* ── Scripts page (scripts_page.go) ── */
.scr-toolbar{display:flex;align-items:center;gap:10px}
.scr-toolbar>span{flex-shrink:0}
.scr-search{flex:1;max-width:280px;box-sizing:border-box;background:var(--input-bg);border:none;border-radius:8px;padding:7px 10px;font-size:11.5px;color:var(--text);outline:none;font-family:inherit}
.scr-card{display:flex;align-items:center;gap:12px;padding:12px 16px;box-shadow:inset 0 -1px 0 rgb(var(--ink) / .06)}
.scr-card:last-child{box-shadow:none}
.scr-card .info{flex:1;min-width:0}
.scr-card .info .lbl{font-size:13px;font-weight:600;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.scr-card .info .sub{font-size:11px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;margin-top:2px}
.scr-avail{display:flex;align-items:center;gap:6px;font-size:11px;color:var(--muted);flex-shrink:0;cursor:pointer;user-select:none}
.scr-avail input{accent-color:var(--accent);width:14px;height:14px;cursor:pointer}
.scr-card .acts{display:flex;gap:6px;flex-shrink:0}
.scr-form{display:flex;flex-direction:column;gap:9px;padding:2px;max-height:60vh;overflow:auto}
.scr-field{display:flex;flex-direction:column;gap:4px}
.scr-field label{font-size:11px;color:var(--muted);font-weight:600}
.scr-field input,.scr-field textarea,.scr-field select{background:var(--input-bg);border:none;border-radius:10px;padding:8px 11px;font-size:12.5px;color:var(--text);outline:none;font-family:inherit;resize:vertical}
.scr-field textarea{font-family:ui-monospace,Consolas,monospace;min-height:140px}
.scr-row2{display:flex;gap:9px}
.scr-row2 .scr-field{flex:1;min-width:0}
.scr-check-row{display:flex;align-items:center;gap:8px;font-size:11.5px;color:var(--text)}
.scr-check-row input{accent-color:var(--accent);width:14px;height:14px;cursor:pointer;flex-shrink:0}
.scr-box{width:520px}
`

const scriptsPageJS = `
// ── Scripts page ─────────────────────────────────────────────────────────
var scriptsTargets = [];     // pgTargets() of the last load
var scriptsCats = new Map();  // pgTargetKey(target) -> [{ id, name }]

function renderScriptsPage(pc) {
  const h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('nav.scripts') + '</h1><div class="breadcrumb">' + (pgMulti() ? esc(multiFilterLabel()) : activeServerNote()) + '</div>';
  pc.appendChild(h);
  const body = document.createElement('div'); body.className = 'page-body';
  const panel = document.createElement('div'); panel.className = 'panel';
  panel.innerHTML =
    '<div class="panel-header scr-toolbar">' +
      '<span>' + TH('nav.scripts') + '</span>' +
      '<input type="text" class="scr-search" id="scr-search" placeholder="' + esc(T('scripts.mgmt.search')) + '">' +
      '<button class="btn-primary" id="scr-new-btn" style="width:auto;padding:8px 16px;flex-shrink:0">' + TH('scripts.mgmt.new') + '</button>' +
    '</div>' +
    '<div class="panel-body flush" id="scr-list"></div>';
  body.appendChild(panel);
  pc.appendChild(body);
  const search = panel.querySelector('#scr-search');
  let deb = null;
  search.addEventListener('input', function () { clearTimeout(deb); deb = setTimeout(function () { scriptsLoad(search.value.trim()); }, 250); });
  panel.querySelector('#scr-new-btn').addEventListener('click', function () { scriptsOpenModal(null); });
  scriptsLoad('');
}

async function scriptsReadJson(r) {
  if (!r.ok) { const e = new Error('http_' + r.status); e.status = r.status; throw e; }
  const d = await r.json();
  return (d && d.data) || [];
}
function scriptsCatsOf(t) {
  if (!t) return [];
  const own = scriptsCats.get(pgTargetKey(t));
  if (own) return own;
  // (a tenant not read on this load: the categories known on its server)
  const seen = new Map();
  scriptsCats.forEach(function (list, k) { if (k.indexOf(t.pid + '/') === 0) list.forEach(function (c) { seen.set(c.id, c); }); });
  return Array.from(seen.values());
}
function scriptsCategoryName(t, id) {
  const c = scriptsCatsOf(t).find(function (c) { return c.id === id; });
  return c ? c.name : '';
}

async function scriptsLoad(search) {
  const list = document.getElementById('scr-list');
  if (!list) return;
  list.innerHTML = '<div style="text-align:center;padding:20px;color:var(--muted2)">' + TH('common.loading') + '</div>';
  const qs = search ? ('?search=' + encodeURIComponent(search)) : '';
  const targets = pgTargets();
  const found = [];
  scriptsCats = new Map();
  await pgEach(targets, async function (t) {
    const items = await scriptsReadJson(await pgApi(t, 'GET', '/api/scripts' + qs));
    try { scriptsCats.set(pgTargetKey(t), await scriptsReadJson(await pgApi(t, 'GET', '/api/scripts/categories'))); } catch (e) { scriptsCats.set(pgTargetKey(t), []); }
    items.forEach(function (it) { found.push({ it: it, t: t }); });
  });
  scriptsTargets = targets;
  if (document.getElementById('scr-list') !== list) return; // (the page changed meanwhile)
  // One row per script of a server (a master admin reading tenant 1 sees
  // every tenant's; a shared script shows in each tenant it is shared to),
  // placed in the tenant that owns it.
  const seen = new Set();
  const rows = [];
  found.forEach(function (f) {
    const k = f.t.pid + ':' + f.it.id;
    if (seen.has(k)) return;
    seen.add(k);
    const owner = pgOwnerTarget(f.it, f.t, targets);
    const cat = f.it.categoryId ? scriptsCategoryName(owner, f.it.categoryId) || scriptsCategoryName(f.t, f.it.categoryId) : '';
    rows.push({ t: owner, group: cat, groupOrder: cat ? 0 : 1, el: scriptsCard(f.it, owner) });
  });
  rows.sort(function (a, b) { return String(a.el.dataset.name).localeCompare(String(b.el.dataset.name)); });
  const single = targets.length === 1 && targets[0].single;
  if (single && targets[0].err) {
    list.innerHTML = '<div style="text-align:center;padding:20px;color:var(--muted2)">' +
      (targets[0].err.status === 403 ? TH('scripts.mgmt.noAccess') : TH('status.loadFailed', { error: targets[0].err.message || String(targets[0].err) })) + '</div>';
    return;
  }
  pgRenderGrouped(list, targets, rows, '<div style="text-align:center;padding:20px;color:var(--muted2)">' + TH('scripts.mgmt.empty') + '</div>');
}

function scriptsCard(it, t) {
  const card = document.createElement('div'); card.className = 'scr-card';
  card.dataset.name = String(it.name || '');
  const info = document.createElement('div'); info.className = 'info';
  const bits = [it.platform, it.runtime, it.purpose].filter(Boolean);
  info.innerHTML = '<div class="lbl">' + esc(it.name) + '</div><div class="sub">' + esc(bits.join(' · ')) + '</div>';
  card.appendChild(info);
  const availLbl = document.createElement('label'); availLbl.className = 'scr-avail';
  const cb = document.createElement('input'); cb.type = 'checkbox'; cb.checked = !!it.availableInReach;
  cb.addEventListener('change', function () { scriptsToggleAvailable(it, t, cb); });
  availLbl.appendChild(cb);
  const span = document.createElement('span'); span.textContent = T('scripts.mgmt.availableInReach');
  availLbl.appendChild(span);
  card.appendChild(availLbl);
  const acts = document.createElement('div'); acts.className = 'acts';
  const editBtn = document.createElement('button'); editBtn.className = 'btn-ghost'; editBtn.textContent = T('scripts.mgmt.edit');
  editBtn.addEventListener('click', function () { scriptsOpenModal(it, t); });
  const delBtn = document.createElement('button'); delBtn.className = 'exp-btn-danger'; delBtn.textContent = T('scripts.mgmt.delete');
  delBtn.addEventListener('click', function () { scriptsConfirmDelete(it, t); });
  acts.appendChild(editBtn); acts.appendChild(delBtn);
  card.appendChild(acts);
  return card;
}

function scriptsErrText(err) {
  if (err && err.status === 403) return T('scripts.mgmt.forbidden');
  if (err && err.status === 404) return T('scripts.mgmt.notFound');
  return T('status.loadFailed', { error: (err && err.message) || String(err) });
}
function scriptsReload() { scriptsLoad(document.getElementById('scr-search')?.value?.trim() || ''); }

async function scriptsToggleAvailable(it, t, cb) {
  const prev = !!it.availableInReach;
  it.availableInReach = cb.checked;
  try {
    const r = await pgApi(t, 'PATCH', '/api/scripts/' + it.id, { availableInReach: cb.checked });
    if (!r.ok) { const e = new Error('http_' + r.status); e.status = r.status; throw e; }
    scriptsInvalidate(t.pid); // (the Scripts panels and the Run page of every window follow: scripts_panel.go)
  } catch (err) {
    it.availableInReach = prev; cb.checked = prev;
    reachNotify(scriptsErrText(err), 'error');
  }
}

async function scriptsConfirmDelete(it, t) {
  scriptsOpenConfirmModal(T('scripts.mgmt.confirmDelete', { name: it.name }), async function () {
    try {
      const r = await pgApi(t, 'DELETE', '/api/scripts/' + it.id);
      if (!r.ok && r.status !== 204) { const e = new Error('http_' + r.status); e.status = r.status; throw e; }
      scriptsInvalidate(t.pid);
      scriptsReload();
    } catch (err) { reachNotify(scriptsErrText(err), 'error'); }
  });
}

// A small modal shell on the app's generic .modal-overlay/.modal-box (the
// same one every other page's dialogs use — 2FA prompt, vault, explorer's
// confirmations): a header with a close button, ESC and a backdrop click
// both dismiss it.
function scriptsBaseModal(titleKey, extraClass) {
  const ov = document.createElement('div'); ov.className = 'modal-overlay visible';
  const box = document.createElement('div'); box.className = 'modal-box' + (extraClass ? ' ' + extraClass : '');
  const head = document.createElement('div'); head.className = 'modal-head';
  const h = document.createElement('h3'); h.textContent = T(titleKey);
  const x = document.createElement('button'); x.className = 'close';
  x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
  head.appendChild(h); head.appendChild(x);
  box.appendChild(head);
  ov.appendChild(box);
  let esc2 = null;
  const close = function () {
    if (ov.parentElement) ov.parentElement.removeChild(ov);
    if (esc2) document.removeEventListener('keydown', esc2);
  };
  esc2 = function (e) { if (e.key === 'Escape') close(); };
  x.addEventListener('click', close);
  ov.addEventListener('mousedown', function (e) { if (e.target === ov) close(); });
  document.addEventListener('keydown', esc2);
  document.body.appendChild(ov);
  return { box: box, close: close };
}
function scriptsField(labelKey, type) {
  const wrap = document.createElement('div'); wrap.className = 'scr-field';
  const lbl = document.createElement('label'); lbl.textContent = T(labelKey);
  const input = document.createElement(type === 'textarea' ? 'textarea' : (type === 'select' ? 'select' : 'input'));
  if (type !== 'textarea' && type !== 'select') input.type = type || 'text';
  wrap.appendChild(lbl); wrap.appendChild(input);
  return { wrap: wrap, input: input };
}

const SCRIPT_PLATFORMS = ['windows', 'linux', 'macos', 'freebsd', 'all'];
const SCRIPT_RUNTIMES = ['powershell', 'pwsh', 'cmd', 'bash', 'zsh', 'sh', 'python', 'python3', 'perl', 'ruby'];
const SCRIPT_PURPOSES = ['check', 'resolve', 'execute', 'compliance', 'metric'];

function scriptsOpenModal(it, owner) {
  const m = scriptsBaseModal(it ? 'scripts.mgmt.editTitle' : 'scripts.mgmt.newTitle', 'scr-box');
  const form = document.createElement('div'); form.className = 'scr-form';
  // Where it goes: its own (server, tenant) when editing; a new one asks
  // when the tenant menu leaves several open (scope_pages.go).
  const dest = it ? null : pgDestPicker(scriptsTargets.length ? scriptsTargets : pgTargets());
  const where = function () { return it ? owner : (dest && dest.get()); };
  if (!it && !where()) { m.close(); reachNotify(T('scope.nowhere'), 'warn'); return; }
  if (dest && dest.wrap) form.appendChild(dest.wrap);
  const name = scriptsField('scripts.mgmt.field.name'); name.input.value = (it && it.name) || '';
  const desc = scriptsField('scripts.mgmt.field.description', 'textarea'); desc.wrap.querySelector('textarea').style.minHeight = '50px'; desc.input.value = (it && it.description) || '';
  const row1 = document.createElement('div'); row1.className = 'scr-row2';
  const platform = scriptsField('scripts.mgmt.field.platform', 'select');
  SCRIPT_PLATFORMS.forEach(function (p) { const o = document.createElement('option'); o.value = p; o.textContent = p; platform.input.appendChild(o); });
  platform.input.value = (it && it.platform) || 'windows';
  const runtime = scriptsField('scripts.mgmt.field.runtime', 'select');
  SCRIPT_RUNTIMES.forEach(function (r) { const o = document.createElement('option'); o.value = r; o.textContent = r; runtime.input.appendChild(o); });
  runtime.input.value = (it && it.runtime) || 'powershell';
  row1.appendChild(platform.wrap); row1.appendChild(runtime.wrap);
  const row2 = document.createElement('div'); row2.className = 'scr-row2';
  const purpose = scriptsField('scripts.mgmt.field.purpose', 'select');
  const noPurpose = document.createElement('option'); noPurpose.value = ''; noPurpose.textContent = T('scripts.mgmt.field.noPurpose'); purpose.input.appendChild(noPurpose);
  SCRIPT_PURPOSES.forEach(function (p) { const o = document.createElement('option'); o.value = p; o.textContent = p; purpose.input.appendChild(o); });
  purpose.input.value = (it && it.purpose) || '';
  const category = scriptsField('scripts.mgmt.field.category', 'select');
  const noCat = document.createElement('option'); noCat.value = ''; noCat.textContent = T('scripts.mgmt.noCategory'); category.input.appendChild(noCat);
  const fillCats = function () {
    const keep = category.input.value;
    while (category.input.options.length > 1) category.input.remove(1);
    scriptsCatsOf(where()).forEach(function (c) { const o = document.createElement('option'); o.value = c.id; o.textContent = c.name; category.input.appendChild(o); });
    category.input.value = category.input.querySelector('option[value="' + keep + '"]') ? keep : '';
  };
  fillCats();
  category.input.value = (it && it.categoryId) || '';
  if (dest) dest.onChange(fillCats);
  row2.appendChild(purpose.wrap); row2.appendChild(category.wrap);
  const content = scriptsField('scripts.mgmt.field.content', 'textarea'); content.input.value = (it && it.content) || '';
  const check = document.createElement('div'); check.className = 'scr-check-row';
  const cb = document.createElement('input'); cb.type = 'checkbox'; cb.id = 'scr-avail-check';
  cb.checked = it ? !!it.availableInReach : false;
  const cbLbl = document.createElement('label'); cbLbl.setAttribute('for', 'scr-avail-check'); cbLbl.textContent = T('scripts.mgmt.availableInReach');
  check.appendChild(cb); check.appendChild(cbLbl);
  const err = document.createElement('div'); err.className = 'modal-err';
  form.appendChild(name.wrap); form.appendChild(desc.wrap); form.appendChild(row1); form.appendChild(row2);
  form.appendChild(content.wrap); form.appendChild(check); form.appendChild(err);
  const actions = document.createElement('div'); actions.className = 'modal-actions';
  const cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  const save = document.createElement('button'); save.className = 'btn-primary'; save.textContent = T('scripts.mgmt.save');
  save.addEventListener('click', async function () {
    const nameVal = name.input.value.trim();
    if (!nameVal) { err.textContent = T('scripts.mgmt.field.nameRequired'); return; }
    const payload = {
      name: nameVal, description: desc.input.value.trim() || undefined,
      platform: platform.input.value, runtime: runtime.input.value,
      purpose: purpose.input.value || undefined,
      categoryId: category.input.value ? parseInt(category.input.value, 10) : undefined,
      content: content.input.value, availableInReach: cb.checked,
    };
    err.textContent = '';
    save.disabled = true;
    try {
      const t = where();
      if (!t) { err.textContent = T('scope.nowhere'); save.disabled = false; return; }
      const r = it
        ? await pgApi(t, 'PATCH', '/api/scripts/' + it.id, payload)
        : await pgApi(t, 'POST', '/api/scripts', payload);
      if (!r.ok) { const e = new Error('http_' + r.status); e.status = r.status; throw e; }
      m.close();
      scriptsInvalidate(t.pid);
      scriptsReload();
    } catch (e) {
      err.textContent = scriptsErrText(e);
      save.disabled = false;
    }
  });
  actions.appendChild(cancel); actions.appendChild(save);
  form.appendChild(actions);
  m.box.appendChild(form);
  setTimeout(function () { try { name.input.focus(); } catch (e) {} }, 0);
}

function scriptsOpenConfirmModal(text, onConfirm) {
  const m = scriptsBaseModal('scripts.mgmt.delete');
  const p = document.createElement('p'); p.className = 'desc'; p.textContent = text;
  const actions = document.createElement('div'); actions.className = 'modal-actions';
  const cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  const ok = document.createElement('button'); ok.className = 'exp-btn-danger'; ok.textContent = T('scripts.mgmt.delete');
  ok.addEventListener('click', function () { m.close(); onConfirm(); });
  actions.appendChild(cancel); actions.appendChild(ok);
  m.box.appendChild(p); m.box.appendChild(actions);
}
`
