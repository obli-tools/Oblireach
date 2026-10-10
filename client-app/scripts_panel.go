package main

// scripts_panel.go — the scripts of a device as the session's Scripts side
// panel shows them, and the pieces the Run page (run_page.go) shares with it.
//
//   - The script lists (GET /api/reach/scripts): one per server and tenant in
//     multi-server mode (the session pinned on the device's tenant, /spt/),
//     the active server's in single mode (reachScripts, ui.go). Kept with the
//     time they were read: stale-while-revalidate. What is on screen is
//     shown at once and read again in the background when it is older than
//     SCRIPTS_TTL_MS (SCRIPTS_OPEN_MS when the panel opens or follows another
//     device); a changed answer updates the panel in place (the list keyed by
//     script, the script picked drawn again only when it changed, its run and
//     console kept). Obliance emits no socket event for a script created,
//     edited or deleted: the Scripts page (scripts_page.go) marks the lists
//     of its server stale after each change it makes (scriptsInvalidate) and
//     tells the other windows of the app (localStorage SCRIPTS_REV_KEY, the
//     session windows have their own lists); the age covers changes made
//     elsewhere (the web app, another operator).
//   - Tenant scope: a list keeps the scripts of the device's tenant, the
//     built-in ones and those the master tenant shares with it
//     (targetTenantIds). The server already scopes what it sends to the
//     session's tenant; a master session reads every tenant's (god view):
//     there the device's tenant decides (scriptsScoped).
//   - The panel: the list, then the script picked (its header, a preview
//     capped to a few lines with a button opening the whole script in a large
//     read-only view, its parameters form, Execute) and the console, which
//     takes the remaining height: everything reachable without scrolling the
//     panel. Parameters (Obliance Script.parameters, absent on an older
//     server: none): string / number fields, a switch, a list, several
//     choices, a secret typed in a password field (shown on demand, never
//     kept anywhere but that field, emptied once the run is sent); defaults
//     filled in, required ones checked before sending; the server's own check
//     (400 { details: [{ name, code }] }) shown on the fields.
//
// Injected verbatim after the Scripts page (buildUI, not through Sprintf:
// '%' is safe): no backtick here.

const scriptsPanelCSS = `
/* ── Scripts side panel (scripts_panel.go) ── */
.side-panel .scripts-list{width:auto;max-height:min(24%,184px);min-height:42px;flex-shrink:0;overflow-y:auto;background:rgb(var(--ink) / .03)}
.side-panel .script-item{padding:7px 14px}
.side-panel .script-detail{flex:1;min-height:240px;padding:12px 14px;gap:8px;overflow:hidden;display:flex;flex-direction:column}
.sc-head{display:flex;align-items:center;gap:6px;min-width:0;flex-shrink:0}
.sc-head h3{flex:1;min-width:0;font-size:13px;font-weight:600;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.sc-head .sc-rt{font-size:11px;color:var(--muted);flex-shrink:0}
.sc-desc{font-size:11.5px;color:var(--muted);line-height:1.4;flex-shrink:0;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.sc-icon-btn{display:inline-flex;align-items:center;justify-content:center;width:26px;height:26px;padding:0;border:none;border-radius:8px;background:rgb(var(--ink) / .06);color:var(--muted);cursor:pointer;flex-shrink:0;transition:background .1s,color .1s}
.sc-icon-btn:hover{background:rgb(var(--accent-rgb) / .14);color:var(--accent-l)}
.sc-icon-btn:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.sc-icon-btn.on{background:rgb(var(--accent-rgb) / .16);color:var(--accent-l)}
.sc-icon-btn svg{width:14px;height:14px;display:block}
.side-panel .script-code,.sc-preview{flex:0 1 auto;min-height:3.2em;max-height:calc(12 * 1.45em + 20px);overflow:auto;padding:10px 12px;margin:0;border-radius:10px;background:var(--bg4);color:var(--text);font-family:ui-monospace,Consolas,monospace;font-size:11px;line-height:1.45;white-space:pre;tab-size:4}
.sc-gone{flex-shrink:0;padding:7px 11px;border-radius:10px;background:rgb(var(--warn-rgb) / .12);color:var(--warn);font-size:11.5px}
.side-panel .script-detail>.btn-primary{flex-shrink:0;padding:9px}
.side-panel .exec-output{flex:1 1 120px;min-height:90px;max-height:none;overflow:auto}
.sc-note{flex-shrink:0;font-size:11.5px;color:var(--danger)}
/* Parameters form */
.spf{display:flex;flex-direction:column;gap:7px;flex-shrink:0;max-height:min(28vh,240px);overflow-y:auto;padding:2px}
.spf-field{display:flex;flex-direction:column;gap:3px;min-width:0}
.spf-label{font-size:11px;font-weight:600;color:var(--muted)}
.spf-label .req{color:var(--accent-l);margin-inline-start:2px}
.spf-desc{font-size:10.5px;color:var(--muted2);line-height:1.35}
.spf-err{font-size:10.5px;color:var(--danger)}
.spf-err:empty{display:none}
.spf-field input[type=text],.spf-field input[type=number],.spf-field input[type=password],.spf-field select{width:100%;box-sizing:border-box;background:var(--input-bg);border:none;border-radius:9px;padding:7px 10px;font-size:12px;color:var(--text);outline:none;font-family:inherit}
.spf-field input:focus-visible,.spf-field select:focus-visible{box-shadow:0 0 0 2px var(--accent)}
.spf-field.bad input,.spf-field.bad select,.spf-field.bad .spf-multi{box-shadow:0 0 0 1.5px var(--danger)}
.spf-secret{display:flex;gap:6px;align-items:center}
.spf-secret input{flex:1;min-width:0}
.spf-switch{display:inline-flex;align-items:center;gap:8px;cursor:pointer;font-size:12px;color:var(--text);user-select:none;align-self:flex-start}
.spf-switch input{position:absolute;opacity:0;width:1px;height:1px}
.spf-switch .track{position:relative;width:30px;height:17px;border-radius:9px;background:rgb(var(--ink) / .16);flex-shrink:0;transition:background .12s}
.spf-switch .track::after{content:'';position:absolute;top:2px;inset-inline-start:2px;width:13px;height:13px;border-radius:50%;background:var(--bg);transition:inset-inline-start .12s}
.spf-switch input:checked+.track{background:var(--accent-fill)}
.spf-switch input:checked+.track::after{inset-inline-start:15px}
.spf-switch input:focus-visible+.track{box-shadow:0 0 0 2px var(--accent)}
.spf-multi{display:flex;flex-wrap:wrap;gap:5px;border-radius:9px}
.spf-chip{height:26px;padding:0 10px;border:none;border-radius:8px;background:rgb(var(--ink) / .06);color:var(--muted);font-size:11.5px;cursor:pointer}
.spf-chip.on{background:rgb(var(--accent-rgb) / .16);color:var(--text);font-weight:600}
.spf-chip:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.spf-form-err{font-size:11px;color:var(--danger)}
.spf-form-err:empty{display:none}
/* The whole text (a script, an output) in a large read-only view */
.modal-overlay.sc-text-ov{z-index:380}
.sc-text-ov .modal-box{width:min(1100px,94vw);height:min(86vh,900px);padding:18px 20px;gap:10px}
.sc-text-ov .modal-head h3{flex:1;min-width:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.sc-text-ov .sc-text-sub{font-size:11px;color:var(--muted);flex-shrink:0}
.sc-text-pre{flex:1;min-height:0;margin:0;overflow:auto;padding:14px 16px;border-radius:12px;background:var(--bg4);color:var(--text);font-family:ui-monospace,Consolas,monospace;font-size:12px;line-height:1.5;white-space:pre;tab-size:4;user-select:text}
.sc-text-pre.term{background:var(--term-bg);color:var(--term-fg)}
.sc-text-empty{color:var(--muted2);font-style:italic}
html[dir=rtl] .sc-preview,html[dir=rtl] .sc-text-pre,html[dir=rtl] .spf-field input{direction:ltr}
`

const scriptsPanelJS = `
// ── Script lists: per server and tenant, stale-while-revalidate ─────────────
var SCRIPTS_TTL_MS = 60000;   // a list older than this is read again when shown
var SCRIPTS_OPEN_MS = 5000;   // the panel opening (or following another device): read again unless this fresh
var SCRIPTS_REV_KEY = 'oblireach_scripts_rev'; // (a change made in one window: the others mark their lists stale)
var scriptsCache = new Map(); // key -> { pid, list (multi-server mode), loading, at, stale, seq, promise, failed }

// Single mode: one list (reachScripts, the active server's, or this session
// window's server's). Multi-server mode: one per server and tenant.
function scriptsCacheKey(dev) {
  return (serverMode === 'multi' && dev) ? devPid(dev) + ':' + (devTenant(dev) || '') : '~single';
}
function scriptsEntry(k, dev) {
  var e = scriptsCache.get(k);
  if (!e) {
    e = { pid: (serverMode === 'multi' && dev) ? devPid(dev) : homePid(), list: [], loading: false, at: 0, stale: true, seq: 0, promise: null, failed: false };
    scriptsCache.set(k, e);
  }
  return e;
}
// The server's single-mode bootstrap (enterApp): its list read now.
async function loadScripts() {
  scriptsCache.clear();
  if (serverMode === 'multi') return;
  await scriptsRevalidate(null, true);
}
// A device's scripts as known now (a stale list is read again meanwhile).
// maxAge (optional): how old the list may be before it is read again.
function scriptsFor(dev, maxAge) {
  if (dev && typeof devIsLocal === 'function' && devIsLocal(dev)) return [];
  var e = scriptsEntry(scriptsCacheKey(dev), dev);
  scriptsRevalidate(dev, false, maxAge);
  var list = (serverMode === 'multi' && dev) ? e.list : reachScripts;
  return scriptsScoped(list, dev ? devTenant(dev) : null);
}
// The first read of a list is under way (nothing to show yet).
function scriptsLoading(dev) {
  var e = scriptsCache.get(scriptsCacheKey(dev));
  return !!(e && e.loading && !e.at);
}
// Reads the list again when it is stale or older than maxAge (force: at
// once). Resolves to true when the list changed.
function scriptsRevalidate(dev, force, maxAge) {
  if (dev && typeof devIsLocal === 'function' && devIsLocal(dev)) return Promise.resolve(false);
  var multi = serverMode === 'multi' && !!dev;
  var k = scriptsCacheKey(dev), e = scriptsEntry(k, dev);
  if (e.loading) return e.promise;
  var limit = (typeof maxAge === 'number' && maxAge >= 0) ? maxAge : SCRIPTS_TTL_MS;
  if (!force && !e.stale && Date.now() - e.at < limit) return Promise.resolve(false);
  e.loading = true; e.stale = false;
  var seq = ++e.seq, first = !e.at;
  var req;
  try { req = multi ? devApi(dev, 'GET', '/api/reach/scripts', undefined, true) : api('GET', '/api/reach/scripts'); }
  catch (err) { req = Promise.reject(err); }
  e.promise = Promise.resolve(req)
    .then(function (r) { return (r && r.ok) ? r.json() : null; })
    .catch(function () { return null; })
    .then(function (d) {
      if (scriptsCache.get(k) !== e || e.seq !== seq) return false; // (forgotten or invalidated meanwhile)
      e.loading = false; e.at = Date.now();
      var list = (d && d.data && Array.isArray(d.data.scripts)) ? d.data.scripts : null;
      e.failed = !list;
      var changed = false;
      if (list) {
        var cur = multi ? e.list : reachScripts;
        if (scriptsSig(list) !== scriptsSig(cur)) {
          if (multi) e.list = list; else reachScripts = list;
          changed = true;
        }
      }
      if (changed || first) scriptsChanged(k);
      return changed;
    });
  return e.promise;
}
// What a list says (every field shown or sent: the panel follows any edit).
function scriptsSig(list) { try { return JSON.stringify(list || []); } catch (e) { return String(Math.random()); } }
// The scripts of a tenant: its own, the built-in ones, those shared with it.
var scriptsScopedMemo = new WeakMap();
function scriptsScoped(list, tid) {
  list = Array.isArray(list) ? list : [];
  if (!tid) return list;
  var fits = function (sc) {
    if (!sc || sc.tenantId === undefined || sc.tenantId === null) return true;
    var own = Number(sc.tenantId);
    if (!Number.isInteger(own) || own === tid) return true;
    return Array.isArray(sc.targetTenantIds) && sc.targetTenantIds.some(function (x) { return Number(x) === tid; });
  };
  if (list.every(fits)) return list; // (the same array: nothing redrawn for nothing)
  var m = scriptsScopedMemo.get(list);
  if (!m) { m = new Map(); scriptsScopedMemo.set(list, m); }
  if (!m.has(tid)) m.set(tid, list.filter(fits));
  return m.get(tid);
}
// A list changed (or its first read ended): what shows it follows.
function scriptsChanged(k) {
  if (typeof sidePanelShown !== 'undefined' && sidePanelShown === 'scripts' && selectedDevice && scriptsCacheKey(selectedDevice) === k) sidePanelScriptsRefresh();
  if (typeof runScriptsChanged === 'function') runScriptsChanged(k);
}
// Scripts created, edited, deleted or allowed in Reach on a server (pid;
// '': every server): its lists are read again where they show, in every
// window of the app (fromOther: told by another window).
function scriptsInvalidate(pid, fromOther) {
  pid = pid || '';
  scriptsCache.forEach(function (e) {
    if (pid && e.pid !== pid) return;
    e.stale = true;
    if (e.loading) { e.loading = false; e.seq++; } // (an answer read before the change: dropped)
  });
  if (!fromOther) {
    try { localStorage.setItem(SCRIPTS_REV_KEY, JSON.stringify({ pid: pid, at: Date.now(), n: Math.floor(Math.random() * 1e9) })); } catch (e) {}
  }
  scriptsRefreshShown();
}
// The lists on screen read again when stale (the Scripts panel, the Run page).
function scriptsRefreshShown(maxAge) {
  if (typeof sidePanelShown !== 'undefined' && sidePanelShown === 'scripts' && selectedDevice) scriptsRevalidate(selectedDevice, false, maxAge);
  if (typeof runRevalidateShown === 'function') runRevalidateShown(maxAge);
}
if (typeof window !== 'undefined' && window && typeof window.addEventListener === 'function') {
  window.addEventListener('storage', function (ev) {
    if (!ev || ev.key !== SCRIPTS_REV_KEY || !ev.newValue) return;
    var m = null;
    try { m = JSON.parse(ev.newValue); } catch (e) { m = null; }
    scriptsInvalidate(m && typeof m.pid === 'string' ? m.pid : '', true);
  });
  // Back in front: what shows is read again when it is old.
  window.addEventListener('focus', function () { try { scriptsRefreshShown(SCRIPTS_TTL_MS); } catch (e) {} });
}

// ── The Scripts side panel ───────────────────────────────────────────────────
var SC_ICON = {
  expand: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>',
  copy: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1"/></svg>',
  eye: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>',
  eyeOff: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0112 20c-7 0-11-8-11-8a18.45 18.45 0 015.06-5.94M9.9 4.24A9.12 9.12 0 0112 4c7 0 11 8 11 8a18.5 18.5 0 01-2.16 3.19m-6.72-1.07a3 3 0 11-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>',
  close: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>',
};
function scMk(tag, cls, text) {
  var e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined && text !== null) e.textContent = String(text);
  return e;
}
function scIconBtn(icon, titleKey, cls) {
  var b = scMk('button', 'sc-icon-btn' + (cls ? ' ' + cls : ''));
  b.type = 'button';
  b.innerHTML = SC_ICON[icon];
  b.title = T(titleKey); b.setAttribute('aria-label', T(titleKey));
  return b;
}

// The Scripts of this device (per server: device ids of two servers overlap):
// the elements shown last time, kept and brought up to date in place, or new
// ones. opening: the panel opens or follows this device (read again unless
// read SCRIPTS_OPEN_MS ago).
function sidePanelScriptsFor(dev, opening) {
  var key = devKey(dev);
  var list = scriptsFor(dev, opening === false ? undefined : SCRIPTS_OPEN_MS); // (its server's and tenant's)
  var s = sidePanelScripts.get(key);
  if (!s) {
    s = { scripts: list, nodes: [], picked: false, pickedId: null, scroll: 0, kind: '', list: null, detail: null, dev: dev };
    renderScriptsPanel(s, dev);
  } else {
    s.dev = dev;
    if (s.scripts !== list) scriptsPanelUpdate(s, list, dev);
  }
  sidePanelScripts.delete(key); sidePanelScripts.set(key, s); // most recent last
  while (sidePanelScripts.size > 20) {
    var old = sidePanelScripts.keys().next().value;
    scriptsPanelDrop(sidePanelScripts.get(old));
    sidePanelScripts.delete(old);
  }
  return s;
}
// The list of the panel on screen changed (scriptsChanged): brought up to
// date in place; its elements replaced only when the panel's kind changed
// (no script / scripts), where it was scrolled.
function sidePanelScriptsRefresh() {
  var dev = selectedDevice;
  if (!dev || sidePanelShown !== 'scripts') return;
  var s = sidePanelScripts.get(devKey(dev));
  if (!s || s !== sidePanelShownScripts) return;
  var list = scriptsFor(dev);
  if (s.scripts === list && s.kind !== 'empty') return;
  if (!scriptsPanelUpdate(s, list, dev)) return;
  var body = document.getElementById('side-panel-body');
  if (!body) return;
  var top = body.scrollTop || 0;
  body.innerHTML = '';
  s.nodes.forEach(function (n) { body.appendChild(n); });
  body.scrollTop = top;
}
// Sign-out, server switch: nothing of the previous server or user is kept
// (the runs going on stop being followed).
function sidePanelForget() {
  sidePanelScripts.forEach(function (s) { scriptsPanelDrop(s); });
  sidePanelScripts.clear();
  scriptsCache.clear();
  if (typeof runForget === 'function') runForget(); // (the Run page's selection and runs: run_page.go)
}
function scriptsPanelDrop(s) {
  var ui = s && s.detail && s.detail._sp;
  if (!ui) return;
  ui.dead = true;
  if (ui.form) ui.form.clearSecrets();
}

// Builds the panel's elements (s.nodes): the list and the detail, or what
// says there is none (still loading).
function renderScriptsPanel(s, dev) {
  var scripts = Array.isArray(s.scripts) ? s.scripts : [];
  if (!scripts.length) {
    var e = scMk('div', 'empty'); e.style.padding = '40px 20px';
    e.innerHTML = '<span>' + (scriptsLoading(dev) ? TH('common.loading') : TH('scripts.none')) + '</span>';
    s.nodes = [e]; s.kind = 'empty'; s.list = null; s.detail = null; s.picked = false; s.pickedId = null;
    return;
  }
  s.kind = 'list';
  s.list = scMk('div', 'scripts-list');
  s.list.setAttribute('role', 'listbox');
  s.detail = scMk('div', 'script-detail');
  s.detail.innerHTML = '<div style="color:var(--muted2);font-size:12px">' + TH('scripts.pick') + '</div>';
  s.picked = false; s.pickedId = null;
  scriptsPanelFillList(s, dev);
  s.nodes = [s.list, s.detail];
}
// The list, keyed by script (dom_patch.go): an item rebuilt only when what it
// shows changed; its click reads the script it shows now.
function scriptsPanelFillList(s, dev) {
  domKeyed(s.list, (s.scripts || []).map(function (sc) {
    var sub = String(sc.description || sc.runtime || '');
    return {
      key: 'sc:' + String(sc.id), sig: String(sc.name || '') + '\u0001' + sub, data: sc,
      build: function () {
        var item = scMk('div', 'script-item');
        item.setAttribute('role', 'option'); item.tabIndex = 0;
        var n = scMk('div', 'sname', sc.name || ''), d = scMk('div', 'sdesc', sub);
        item.appendChild(n); item.appendChild(d);
        var pick = function () { scriptsPanelPick(s, item._domData || sc, s.dev || dev); };
        item.addEventListener('click', pick);
        item.addEventListener('keydown', function (ev) { if (ev && (ev.key === 'Enter' || ev.key === ' ')) { if (ev.preventDefault) ev.preventDefault(); pick(); } });
        return item;
      },
    };
  }));
  scriptsPanelMarkActive(s);
}
function scriptsPanelMarkActive(s) {
  var want = s.picked ? 'sc:' + String(s.pickedId) : null;
  Array.prototype.forEach.call(s.list && s.list.children || [], function (c) {
    var on = !!want && c._domKey === want;
    c.classList.toggle('active', on);
    if (typeof c.setAttribute === 'function') c.setAttribute('aria-selected', on ? 'true' : 'false');
  });
}
function scriptsPanelPick(s, sc, dev) {
  s.picked = true; s.pickedId = sc.id;
  scriptsPanelMarkActive(s);
  showScriptDetail(sc, s.detail, dev);
}
// The list read again: the panel follows in place. Returns true when its
// elements were replaced (the caller puts them on screen).
function scriptsPanelUpdate(s, list, dev) {
  s.scripts = list;
  if (s.kind !== 'list' || (!list.length && !s.picked)) {
    if (s.kind === 'list') scriptsPanelDrop(s);
    renderScriptsPanel(s, dev);
    return true;
  }
  scriptsPanelFillList(s, dev);
  if (s.picked && s.detail) {
    var cur = list.find(function (x) { return String(x.id) === String(s.pickedId); });
    var ui = s.detail._sp;
    if (!cur) scriptsDetailGone(s.detail);
    else if (!ui || ui.sig !== scriptDefSig(cur) || ui.gone) showScriptDetail(cur, s.detail, dev);
  }
  return false;
}
// What the detail of a script shows and sends (its definition).
function scriptDefSig(sc) {
  return scriptsSig([sc && sc.name, sc && sc.platform, sc && sc.runtime, sc && sc.description, sc && sc.content, scriptParams(sc)]);
}
// A script's parameters, in their order (none from an older server).
function scriptParams(sc) {
  var p = sc && Array.isArray(sc.parameters) ? sc.parameters.filter(function (x) { return x && typeof x.name === 'string' && x.name; }) : [];
  return p.slice().sort(function (a, b) { return (Number(a.sortOrder) || 0) - (Number(b.sortOrder) || 0); });
}
// The script picked is no longer offered (deleted, no longer allowed in
// Reach): said; its console stays, Execute is off.
function scriptsDetailGone(detail) {
  var ui = detail && detail._sp;
  if (!ui) return;
  ui.gone = true;
  if (ui.goneEl) { ui.goneEl.style.display = ''; ui.goneEl.textContent = T('scripts.gone'); }
  if (ui.btn) ui.btn.disabled = true;
}

// dev: the device of the panel it was built for (kept, it runs on that one).
// Drawn again for the same script (its definition changed): the values typed
// and the console are kept.
function showScriptDetail(script, detail, dev) {
  var prev = detail._sp || null;
  var keep = prev && prev.form ? prev.form.raw() : null;
  var outText = prev && prev.out ? prev.out.textContent : '';
  var outShown = !!(prev && prev.out && prev.out.style.display !== 'none');
  var running = !!(prev && prev.running && prev.scriptId === script.id);
  if (prev && prev.form && prev.scriptId !== script.id) prev.form.clearSecrets();
  var pc = { windows: 'windows', linux: 'linux', macos: 'macos', all: 'all' };
  detail.innerHTML = '';
  var head = scMk('div', 'sc-head');
  var h3 = scMk('h3', '', script.name || ''); h3.title = String(script.name || '');
  var badge = scMk('span', 'badge ' + (pc[script.platform] || 'all'), script.platform || '');
  var rt = scMk('span', 'sc-rt', script.runtime || '');
  var expand = scIconBtn('expand', 'scripts.preview.expand', 'sc-expand');
  expand.addEventListener('click', function () { scriptsShowWhole(detail._sp ? detail._sp.script : script); });
  head.appendChild(h3); head.appendChild(badge); head.appendChild(rt); head.appendChild(expand);
  detail.appendChild(head);
  if (script.description) { var desc = scMk('p', 'sc-desc', script.description); desc.title = String(script.description); detail.appendChild(desc); }
  var code = scMk('pre', 'script-code sc-preview', script.content || '');
  code.tabIndex = 0;
  code.setAttribute('aria-label', T('scripts.preview.label'));
  detail.appendChild(code);
  var gone = scMk('div', 'sc-gone'); gone.style.display = 'none';
  detail.appendChild(gone);
  var params = scriptParams(script);
  var form = params.length ? scriptParamsForm(params, keep) : null;
  if (form) detail.appendChild(form.el);
  var note = scMk('div', 'sc-note'); note.style.display = 'none';
  detail.appendChild(note);
  var btn = scMk('button', 'btn-primary', dev ? '▶ ' + T('scripts.executeOn', { device: String(dev.hostname || '') }) : T('scripts.pickDevice'));
  btn.disabled = !dev || running;
  detail.appendChild(btn);
  var out = scMk('div', 'exec-output');
  out.setAttribute('role', 'log'); out.setAttribute('aria-live', 'polite');
  out.textContent = outText;
  out.style.display = outShown ? 'block' : 'none';
  detail.appendChild(out);
  detail._sp = { script: script, scriptId: script.id, sig: scriptDefSig(script), btn: btn, out: out, form: form, note: note, goneEl: gone, running: running, gone: false, dead: false, poll: prev ? prev.poll : null };
  if (dev) btn.addEventListener('click', function () { executeScript(detail._sp.script, detail, dev); });
}
// The whole script, in a large read-only view (copy, Esc closes it).
function scriptsShowWhole(script) {
  if (!script) return null;
  return scriptsTextModal({ title: String(script.name || ''), sub: [script.platform, script.runtime].filter(Boolean).join(' · '), text: String(script.content || '') });
}

// An id a server sent, put in an API path: a plain token (letters, digits,
// '-', '_'), else null. Never '..', '/', '?', '#' or a percent sign: the
// URL's own rules would take the request out of that server's path (to
// another server's /sp/, /proxy, or the app's /local/).
function apiId(v) {
  var s = (v === undefined || v === null) ? '' : String(v);
  return /^[A-Za-z0-9_-]{1,128}$/.test(s) ? s : null;
}
var EXEC_TERMINAL = ['success', 'failure', 'timeout', 'cancelled', 'skipped'];
// The console's text of an execution (the panel and the Run page show the same).
function scriptsConsoleText(e) {
  if (!e) return '';
  if (EXEC_TERMINAL.indexOf(e.status) >= 0) {
    var t = T('scripts.statusExit', { status: statusWord(EXEC_STATUS, e.status), code: (e.exitCode === null || e.exitCode === undefined) ? '?' : e.exitCode }) + '\n\n';
    if (e.stdout) t += '--- stdout ---\n' + e.stdout + '\n';
    if (e.stderr) t += '--- stderr ---\n' + e.stderr + '\n';
    return t;
  }
  return T('scripts.statusRunning', { status: statusWord(EXEC_STATUS, e.status) }) + '\n';
}
// A refusal's text (the server's own words when it gives some).
function scriptsRunErrText(status, d) {
  var msg = d && typeof d.error === 'string' && d.error ? d.error : ('HTTP ' + status);
  if (status === 403 && d && d.error === 'tenant access denied') return T('devices.server.tenantDenied');
  if (status === 403) return T('scripts.run.forbidden', { error: msg });
  if (status === 400 && d && d.error === 'No target devices found') return T('scripts.run.noTarget');
  return T('scripts.error', { error: msg });
}

async function executeScript(script, detail, dev) {
  if (!dev) return;
  var ui = detail._sp || { btn: detail.querySelector('#exec-btn'), out: detail.querySelector('#exec-output'), form: null, note: null };
  var btn = ui.btn, out = ui.out;
  var say = function (t) { if (ui.note) { ui.note.textContent = t || ''; ui.note.style.display = t ? '' : 'none'; } };
  say('');
  var values = {};
  if (ui.form) {
    var v = ui.form.collect();
    if (!v.ok) { ui.form.focusError(); return; }
    values = v.values;
  }
  out.style.display = 'block'; out.textContent = T('scripts.executing') + '\n'; btn.disabled = true; ui.running = true;
  var done = function () { ui.running = false; if (!ui.gone) btn.disabled = false; };
  try {
    // Its server, its session on the device's tenant (multi-server mode):
    // Obliance runs a script on the devices of the session's tenant only.
    var sid = apiId(script && script.id);
    if (sid === null) { out.textContent += T('scripts.invalidId') + '\n'; done(); return; }
    var r = await devApi(dev, 'POST', '/api/scripts/' + sid + '/execute', { deviceIds: [dev.id], parameterValues: values }, true);
    values = null;
    if (ui.form) ui.form.clearSecrets(); // (a secret is typed for one run)
    var d = null;
    try { d = await r.json(); } catch (e) { d = null; }
    if (!r.ok) {
      if (r.status === 400 && d && Array.isArray(d.details) && ui.form) { ui.form.setErrors(d.details); out.textContent += T('scripts.params.invalid') + '\n'; }
      else out.textContent += scriptsRunErrText(r.status, d) + '\n';
      done(); return;
    }
    if (d && d.data && !Array.isArray(d.data) && d.data.status === 'pending_approval') { out.textContent += T('scripts.run.pendingApproval') + '\n'; done(); return; }
    var execs = (d && d.data) || d, exec = Array.isArray(execs) ? execs[0] : execs;
    if (!exec) { out.textContent += T('scripts.noExecution') + '\n'; done(); return; }
    var eid = apiId(exec.id);
    if (eid === null) { out.textContent += T('scripts.invalidId') + '\n'; done(); return; }
    out.textContent += T('scripts.executionId', { id: exec.id }) + '\n' + T('scripts.status', { status: statusWord(EXEC_STATUS, exec.status) }) + '\n';
    if (exec.status === 'pending' && typeof devAgentReachable === 'function' && !devAgentReachable(dev)) out.textContent += T('scripts.run.queuedOffline') + '\n';
    var att = 0;
    if (ui.poll) clearInterval(ui.poll);
    var poll = setInterval(async function () {
      var cur = detail._sp || ui;
      if (cur.dead) { clearInterval(poll); return; }
      att++; if (att > 900) { clearInterval(poll); cur.out.textContent += '\n' + T('scripts.timeout') + '\n'; cur.running = false; if (!cur.gone) cur.btn.disabled = false; return; }
      try {
        var pr = await devApi(dev, 'GET', '/api/executions/' + eid, undefined, true); var pd = await pr.json(); var e = pd.data || pd;
        cur = detail._sp || ui; // (drawn again meanwhile: its console now)
        if (EXEC_TERMINAL.indexOf(e.status) >= 0) {
          clearInterval(poll);
          cur.out.textContent = scriptsConsoleText(e);
          cur.running = false; if (!cur.gone) cur.btn.disabled = false;
        } else {
          cur.out.textContent = scriptsConsoleText(e) + (e.status === 'pending' && typeof devAgentReachable === 'function' && !devAgentReachable(dev) ? T('scripts.run.queuedOffline') + '\n' : '');
        }
      } catch (e2) {}
    }, 2000);
    ui.poll = poll;
  } catch (err) { if (ui.form) ui.form.clearSecrets(); out.textContent += T('scripts.error', { error: err.message }) + '\n'; done(); }
}

// ── Parameters form (the panel and the Run page) ─────────────────────────────
// params: the script's (scriptParams). keep: values typed before (raw(),
// by name), put back for the parameters still there with the same type.
// Returns { el, collect() -> { ok, values, errors }, setErrors(details),
// clearSecrets(), raw(), focusError(), clearErrors() }.
// The server's codes (400 { code: 'invalid_parameter_values', details:
// [{ name, code }] }) and the form's own, in words.
var SPF_ERR = {
  required: 'scripts.params.err.required', invalid_type: 'scripts.params.err.type', invalid_number: 'scripts.params.err.number',
  invalid_option: 'scripts.params.err.option', too_long: 'scripts.params.err.tooLong', unknown: 'scripts.params.err.unknown',
  type: 'scripts.params.err.type', option: 'scripts.params.err.option',
};
var spfSeq = 0;
function spfDefault(p) {
  var v = p.defaultValue;
  if (p.type === 'boolean') return v === true || /^(true|1|yes|on)$/i.test(String(v === null || v === undefined ? '' : v).trim());
  if (p.type === 'multiselect') {
    if (Array.isArray(v)) return v.map(String);
    var s = String(v === null || v === undefined ? '' : v).trim();
    if (!s) return [];
    if (s.charAt(0) === '[') { try { var a = JSON.parse(s); if (Array.isArray(a)) return a.map(String); } catch (e) {} }
    return s.split(',').map(function (x) { return x.trim(); }).filter(Boolean);
  }
  if (p.type === 'secret') return ''; // (never pre-filled from anywhere)
  return v === null || v === undefined ? '' : String(v);
}
function scriptParamsForm(params, keep) {
  var root = scMk('div', 'spf');
  root.setAttribute('role', 'group'); root.setAttribute('aria-label', T('scripts.params.title'));
  var formErr = scMk('div', 'spf-form-err');
  var fields = [];
  params.forEach(function (p) {
    var id = 'spf-' + (++spfSeq);
    var type = ['string', 'number', 'boolean', 'secret', 'select', 'multiselect'].indexOf(p.type) >= 0 ? p.type : 'string';
    var opts = Array.isArray(p.options) ? p.options.map(String) : [];
    var row = scMk('div', 'spf-field');
    var label = scMk('label', 'spf-label');
    label.setAttribute('for', id);
    label.appendChild(scMk('span', '', p.label || p.name));
    if (p.required) { var star = scMk('span', 'req', '*'); star.title = T('scripts.params.required'); label.appendChild(star); }
    var f = { p: p, type: type, opts: opts, row: row, err: scMk('div', 'spf-err'), input: null, chips: [], get: null, set: null, focus: null };
    var kept = keep && Object.prototype.hasOwnProperty.call(keep, p.name) && keep[p.name] && keep[p.name].type === type ? keep[p.name].v : undefined;
    var init = kept !== undefined ? kept : spfDefault(Object.assign({}, p, { type: type }));
    if (type === 'boolean') {
      var sw = scMk('label', 'spf-switch');
      var cb = scMk('input'); cb.type = 'checkbox'; cb.id = id; cb.checked = !!init;
      sw.appendChild(cb); sw.appendChild(scMk('span', 'track')); sw.appendChild(scMk('span', '', p.label || p.name));
      row.appendChild(sw);
      f.input = cb;
      f.get = function () { return !!cb.checked; };
      f.focus = function () { try { cb.focus(); } catch (e) {} };
    } else if (type === 'select') {
      row.appendChild(label);
      var sel = scMk('select'); sel.id = id;
      if (!p.required || !init) { var o0 = scMk('option', '', p.required ? T('scripts.params.choose') : T('scripts.params.none')); o0.value = ''; sel.appendChild(o0); }
      opts.forEach(function (o) { var op = scMk('option', '', o); op.value = o; sel.appendChild(op); });
      sel.value = opts.indexOf(String(init)) >= 0 ? String(init) : '';
      row.appendChild(sel);
      f.input = sel;
      f.get = function () { return String(sel.value || ''); };
      f.focus = function () { try { sel.focus(); } catch (e) {} };
    } else if (type === 'multiselect') {
      row.appendChild(label);
      var box = scMk('div', 'spf-multi'); box.setAttribute('role', 'group'); box.setAttribute('aria-label', p.label || p.name);
      var on = new Set((Array.isArray(init) ? init : []).map(String).filter(function (x) { return opts.indexOf(x) >= 0; }));
      opts.forEach(function (o) {
        var c = scMk('button', 'spf-chip' + (on.has(o) ? ' on' : ''), o);
        c.type = 'button'; c.setAttribute('aria-pressed', on.has(o) ? 'true' : 'false');
        c.addEventListener('click', function () {
          if (on.has(o)) on.delete(o); else on.add(o);
          c.classList.toggle('on', on.has(o)); c.setAttribute('aria-pressed', on.has(o) ? 'true' : 'false');
        });
        box.appendChild(c); f.chips.push(c);
      });
      row.appendChild(box);
      f.input = box;
      f.get = function () { return opts.filter(function (o) { return on.has(o); }); };
      f.focus = function () { if (f.chips[0]) { try { f.chips[0].focus(); } catch (e) {} } };
    } else if (type === 'secret') {
      row.appendChild(label);
      var wrap = scMk('div', 'spf-secret');
      var pw = scMk('input'); pw.type = 'password'; pw.id = id; pw.value = String(init || '');
      pw.setAttribute('autocomplete', 'off'); pw.setAttribute('spellcheck', 'false');
      var eye = scIconBtn('eye', 'scripts.params.reveal');
      eye.setAttribute('aria-pressed', 'false');
      eye.addEventListener('click', function () {
        var show = pw.type === 'password';
        pw.type = show ? 'text' : 'password';
        eye.innerHTML = show ? SC_ICON.eyeOff : SC_ICON.eye;
        eye.title = T(show ? 'scripts.params.hide' : 'scripts.params.reveal'); eye.setAttribute('aria-label', eye.title);
        eye.setAttribute('aria-pressed', show ? 'true' : 'false');
        eye.classList.toggle('on', show);
      });
      wrap.appendChild(pw); wrap.appendChild(eye);
      row.appendChild(wrap);
      f.input = pw; f.eye = eye;
      f.get = function () { return String(pw.value || ''); };
      f.focus = function () { try { pw.focus(); } catch (e) {} };
    } else {
      row.appendChild(label);
      var inp = scMk('input'); inp.type = type === 'number' ? 'number' : 'text'; inp.id = id; inp.value = String(init === undefined || init === null ? '' : init);
      if (type === 'number') inp.setAttribute('inputmode', 'decimal');
      row.appendChild(inp);
      f.input = inp;
      f.get = function () { return String(inp.value === undefined || inp.value === null ? '' : inp.value); };
      f.focus = function () { try { inp.focus(); } catch (e) {} };
    }
    if (p.description) row.appendChild(scMk('div', 'spf-desc', p.description));
    row.appendChild(f.err);
    root.appendChild(row);
    fields.push(f);
  });
  root.appendChild(formErr);
  var showErr = function (f, code) {
    f.err.textContent = code ? T(SPF_ERR[code] || SPF_ERR.type) : '';
    f.row.classList.toggle('bad', !!code);
    if (f.input && typeof f.input.setAttribute === 'function') f.input.setAttribute('aria-invalid', code ? 'true' : 'false');
  };
  var api2 = {
    el: root,
    fields: fields,
    clearErrors: function () { fields.forEach(function (f) { showErr(f, ''); }); formErr.textContent = ''; },
    // The values to send (by name), checked as the server checks them.
    collect: function () {
      api2.clearErrors();
      var values = {}, errors = [];
      fields.forEach(function (f) {
        var p = f.p, v = f.get(), code = '';
        if (f.type === 'boolean') values[p.name] = !!v;
        else if (f.type === 'multiselect') {
          if (!v.length) { if (p.required) code = 'required'; }
          else values[p.name] = v;
        } else if (f.type === 'number') {
          var s = String(v).trim();
          if (!s) { if (p.required) code = 'required'; }
          else if (!isFinite(Number(s))) code = 'invalid_number';
          else values[p.name] = Number(s);
        } else if (f.type === 'select') {
          if (!v) { if (p.required) code = 'required'; }
          else if (f.opts.indexOf(v) < 0) code = 'invalid_option';
          else values[p.name] = v;
        } else {
          var empty = f.type === 'secret' ? v === '' : !String(v).trim();
          if (empty) { if (p.required) code = 'required'; }
          else values[p.name] = v;
        }
        if (code) { showErr(f, code); errors.push({ name: p.name, code: code }); }
      });
      return { ok: !errors.length, values: errors.length ? null : values, errors: errors };
    },
    // The server's check (400 { details: [{ name, code }] }): on the fields;
    // a name the form does not know, above them.
    setErrors: function (details) {
      api2.clearErrors();
      var other = [];
      (Array.isArray(details) ? details : []).forEach(function (x) {
        var f = fields.find(function (y) { return x && y.p.name === x.name; });
        if (f) showErr(f, Object.prototype.hasOwnProperty.call(SPF_ERR, String(x.code)) ? String(x.code) : 'invalid_type');
        else if (x && x.name) other.push(String(x.name));
      });
      if (other.length) formErr.textContent = T('scripts.params.err.unknownNames', { names: other.join(', ') });
    },
    focusError: function () { var f = fields.find(function (y) { return y.row.classList.contains('bad'); }); if (f) f.focus(); },
    // The secrets typed are emptied (sent once, kept nowhere).
    clearSecrets: function () {
      fields.forEach(function (f) {
        if (f.type !== 'secret') return;
        f.input.value = '';
        if (f.input.type !== 'password') { f.input.type = 'password'; if (f.eye) { f.eye.innerHTML = SC_ICON.eye; f.eye.classList.toggle('on', false); f.eye.setAttribute('aria-pressed', 'false'); } }
      });
    },
    // What is typed, to keep across a redraw (by name and type).
    raw: function () {
      var out = {};
      fields.forEach(function (f) { out[f.p.name] = { type: f.type, v: f.get() }; });
      return out;
    },
    hasSecret: function () { return fields.some(function (f) { return f.type === 'secret'; }); },
  };
  return api2;
}

// ── A whole text in a large read-only view ──────────────────────────────────
// o: { title, sub, text, term (the console's colours) }. Copy; Esc, the
// close button or the backdrop close it. Returns { ov, pre, close }.
function scriptsTextModal(o) {
  o = o || {};
  var ov = scMk('div', 'modal-overlay visible sc-text-ov');
  ov.setAttribute('role', 'dialog'); ov.setAttribute('aria-modal', 'true');
  var box = scMk('div', 'modal-box');
  var head = scMk('div', 'modal-head');
  var h = scMk('h3', '', o.title || ''); h.title = String(o.title || '');
  head.appendChild(h);
  if (o.sub) head.appendChild(scMk('span', 'sc-text-sub', o.sub));
  var text = String(o.text === undefined || o.text === null ? '' : o.text);
  var copy = scMk('button', 'btn-sm');
  copy.type = 'button';
  copy.innerHTML = '<span style="display:inline-flex;width:12px;height:12px;vertical-align:-2px;margin-inline-end:5px">' + SC_ICON.copy + '</span>' + TH('scripts.text.copy');
  copy.addEventListener('click', async function () {
    var ok2 = await reachNativeClipboardWrite(text);
    reachNotify(T(ok2 ? 'term.copied' : 'term.clipboardFailed'), ok2 ? 'ok' : 'warn');
  });
  var x = scMk('button', 'close'); x.type = 'button';
  x.innerHTML = SC_ICON.close; x.title = T('common.close'); x.setAttribute('aria-label', T('common.close'));
  head.appendChild(copy); head.appendChild(x);
  var pre = scMk('pre', 'sc-text-pre' + (o.term ? ' term' : ''));
  pre.tabIndex = 0;
  if (text) pre.textContent = text;
  else { var em = scMk('span', 'sc-text-empty', T('scripts.text.empty')); pre.appendChild(em); }
  box.appendChild(head); box.appendChild(pre);
  ov.appendChild(box);
  var prevFocus = typeof document !== 'undefined' ? document.activeElement : null;
  var onKey = null, closed = false;
  var close = function () {
    if (closed) return;
    closed = true;
    if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') ov.parentElement.removeChild(ov);
    ov.classList.remove('visible');
    if (onKey && typeof document.removeEventListener === 'function') document.removeEventListener('keydown', onKey, true);
    if (prevFocus && typeof prevFocus.focus === 'function') { try { prevFocus.focus(); } catch (e) {} }
  };
  onKey = function (e) { if (e && e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); close(); } };
  x.addEventListener('click', close);
  ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) close(); });
  if (typeof document.addEventListener === 'function') document.addEventListener('keydown', onKey, true);
  document.body.appendChild(ov);
  try { pre.focus(); } catch (e) {}
  return { ov: ov, pre: pre, copy: copy, close: close, onKey: onKey };
}
`
