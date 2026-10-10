package main

// tab_move.go — "Move to window": a tab sent to another window from its
// context menu, with the tear-off machinery of the session windows
// (detached_windows.go: reachTearOff from a session window, reachOpenWindow
// { window } from the main window). Dragging a tab out keeps working as it
// did.
//
// A right click on a tab offers "Move to window ▸", whose sub-menu lists:
//   - the session windows other than this one (reachWindowTargets: each by
//     its title, the device on screen there, and its number of tabs): the
//     tab joins that window, wherever the cursor is (it may be this
//     window's last tab: the window then closes, as after a merge);
//   - the main window (from a session window: reachTearOff { main: true });
//   - "New window": a new session window at the cursor (from the main
//     window: "Detach"; never a session window's last tab, which IS a
//     window of its own already).
// Offered on the session windows' ObliReach tabs and explorers, and on the
// main window's ObliReach session tabs (not a code session: it stays in the
// main window, support_page.go) and Files page explorers. A session moving
// takes its device's explorer side panel along (files_dock.go).
//
// Both consts are injected verbatim after the side panel's (buildUI, not
// through Sprintf: '%' is safe): no backtick here.

const tabMoveCSS = `
/* ── A tab's "Move to window" menu (tab_move.go) ── */
.tab-move-menu{position:fixed;z-index:420;min-width:200px;max-width:340px;padding:5px;display:flex;flex-direction:column;border-radius:12px;background:var(--bg2);box-shadow:0 10px 30px var(--shadow)}
.tab-move-menu button{display:flex;align-items:center;gap:8px;min-width:0;padding:7px 10px;border:none;border-radius:8px;background:none;color:var(--text);font-size:12px;text-align:start;cursor:pointer}
.tab-move-menu button:hover,.tab-move-menu button:focus-visible,.tab-move-menu button.open{background:rgb(var(--ink) / .06);outline:none}
.tab-move-menu button:disabled{opacity:.4;cursor:default}
.tab-move-menu button svg{flex-shrink:0;color:var(--muted)}
.tm-label{flex:1;min-width:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.tm-count{flex-shrink:0;min-width:18px;padding:1px 6px;border-radius:9px;background:rgb(var(--ink) / .07);color:var(--muted);font-size:10px;font-weight:600;text-align:center}
.tm-arrow{display:inline-flex;margin-inline-start:auto}
html[dir=rtl] .tm-arrow svg{transform:scaleX(-1)}
.tm-sep{height:1px;margin:4px 6px;background:rgb(var(--ink) / .08)}
.tm-note{padding:6px 10px;color:var(--muted);font-size:11px}
`

const tabMoveJS = `
// ═════════════════════════════════════════════════════════════════════════
// A tab's "Move to window" (tab_move.go)
// ═════════════════════════════════════════════════════════════════════════
var TAB_MOVE_ICON = {
  move: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="13" height="13" rx="2"/><path d="M9 3h10a2 2 0 012 2v10"/></svg>',
  win: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><line x1="3" y1="9" x2="21" y2="9"/></svg>',
  main: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><line x1="9" y1="4" x2="9" y2="20"/></svg>',
  add: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><line x1="12" y1="9" x2="12" y2="15"/><line x1="9" y1="12" x2="15" y2="12"/></svg>',
  arrow: '<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 6 15 12 9 18"/></svg>',
};
var tabMoveEl = null, tabMoveSub = null, tabMoveWired = false, tabMoveSeq = 0;

// The tab offers "Move to window" in this window.
function tabMoveOffered(tab) {
  if (!tab || tab.members || tab.docked || !tab.device) return false;
  var isSession = typeof sessionTabs !== 'undefined' && sessionTabs.indexOf(tab) >= 0;
  var isFiles = toolTabs.indexOf(tab) >= 0 && tab.kind === 'files';
  if (reachDetachedMode) return (isSession || isFiles) && typeof window.reachTearOff === 'function' && !!reachNativeToken();
  if (!reachCanDetach()) return false;
  if (isSession) return !devIsSupport(tab.device);
  return isFiles && toolInFilesPage(tab);
}
// Where it may go: the other session windows (by id), the main window (from
// a session window), a new window (off for a session window's last tab).
async function tabMoveTargets() {
  var list = [];
  try { list = await reachNative('reachWindowTargets'); } catch (e) { list = []; }
  var out = (Array.isArray(list) ? list : []).filter(function (w) { return !!w && Number(w.id) > 0; }).map(function (w) {
    return { kind: 'window', id: Number(w.id) | 0, title: String(w.title || '') || T('win.sessionTitle'), tabs: Math.max(0, Number(w.tabs) | 0) };
  });
  if (reachDetachedMode) out.push({ kind: 'main' });
  out.push({ kind: 'new', off: reachDetachedMode && winTabCount() < 2 });
  return out;
}

// ── The menu ──
function tabMoveMenuClose() {
  [tabMoveSub, tabMoveEl].forEach(function (m) {
    if (m && m.parentElement && typeof m.parentElement.removeChild === 'function') { try { m.parentElement.removeChild(m); } catch (e) {} }
  });
  tabMoveSub = null; tabMoveEl = null;
}
function tabMoveMenuOpen(tab, e) {
  if (!tabMoveOffered(tab)) return Promise.resolve(false);
  if (e && e.preventDefault) e.preventDefault();
  if (e && e.stopPropagation) e.stopPropagation();
  var at = { x: (e && typeof e.clientX === 'number') ? e.clientX : 40, y: (e && typeof e.clientY === 'number') ? e.clientY : 40 };
  var seq = ++tabMoveSeq;
  tabMoveMenuClose();
  return tabMoveTargets().then(function (targets) {
    if (seq !== tabMoveSeq || !tabMoveOffered(tab)) return false; // (another menu since, or the tab went)
    tabMoveMenuShow(tab, at, targets);
    return true;
  });
}
// Positioned at x, y, kept inside the page (w, h: its size, estimated when
// the page cannot measure it).
function tabMovePlace(m, x, y, w, h) {
  var vw = (typeof window !== 'undefined' && window.innerWidth) || 1200, vh = (typeof window !== 'undefined' && window.innerHeight) || 800;
  m.style.left = Math.max(4, Math.min(x, vw - w - 4)) + 'px';
  m.style.top = Math.max(4, Math.min(y, vh - h - 4)) + 'px';
}
function tabMoveSizeOf(m, n) {
  var r = typeof m.getBoundingClientRect === 'function' ? m.getBoundingClientRect() : null;
  return { w: (r && r.width) || 220, h: (r && r.height) || (32 * n + 12) };
}
function tabMoveMenuShow(tab, at, targets) {
  tabMoveMenuClose();
  var m = document.createElement('div');
  m.className = 'tab-move-menu'; m.id = 'tab-move-menu'; m.setAttribute('role', 'menu');
  var item = tabMoveItem(m, 'tm-move', TAB_MOVE_ICON.move, T('tabMove.moveTo'), '<span class="tm-arrow">' + TAB_MOVE_ICON.arrow + '</span>');
  item.setAttribute('aria-haspopup', 'menu'); item.setAttribute('aria-expanded', 'false');
  var open = function (focus) { tabMoveSubOpen(tab, item, targets, focus); };
  item.addEventListener('mouseenter', function () { open(false); });
  item.addEventListener('click', function () { open(true); });
  m.addEventListener('keydown', function (ev) {
    if (!ev) return;
    if (ev.key === 'Escape') { if (ev.preventDefault) ev.preventDefault(); tabMoveMenuClose(); return; }
    if (ev.key === 'ArrowRight' || ev.key === 'Enter' || ev.key === ' ') { if (ev.preventDefault) ev.preventDefault(); open(true); }
  });
  document.body.appendChild(m);
  var s = tabMoveSizeOf(m, 1);
  tabMovePlace(m, at.x, at.y, s.w, s.h);
  tabMoveEl = m;
  tabMoveWire();
  try { item.focus(); } catch (e) {}
}
function tabMoveItem(parent, id, icon, label, extra) {
  var b = document.createElement('button');
  b.id = id; b.setAttribute('role', 'menuitem');
  b.innerHTML = icon;
  var l = document.createElement('span'); l.className = 'tm-label'; l.textContent = label;
  b.appendChild(l);
  if (extra) { var x = document.createElement('span'); x.innerHTML = extra; b.appendChild(x); }
  parent.appendChild(b);
  return b;
}
// The sub-menu: the windows, the main window, a new window.
function tabMoveSubOpen(tab, item, targets, focus) {
  var s = tabMoveSub;
  if (!s) {
    s = document.createElement('div');
    s.className = 'tab-move-menu tm-sub'; s.id = 'tab-move-sub'; s.setAttribute('role', 'menu');
    s.setAttribute('aria-label', T('tabMove.moveTo'));
    var wins = targets.filter(function (t) { return t.kind === 'window'; });
    wins.forEach(function (t) {
      var b = tabMoveItem(s, 'tm-win-' + t.id, TAB_MOVE_ICON.win, t.title);
      var c = document.createElement('span'); c.className = 'tm-count'; c.textContent = String(t.tabs);
      b.appendChild(c);
      b.title = t.title;
      b.addEventListener('click', function () { tabMoveTo(tab, { window: t.id }); });
    });
    if (!wins.length && !reachDetachedMode) {
      var n = document.createElement('div'); n.className = 'tm-note'; n.id = 'tm-none'; n.textContent = T('tabMove.noWindow');
      s.appendChild(n);
    }
    targets.forEach(function (t) {
      if (t.kind !== 'main') return;
      var b = tabMoveItem(s, 'tm-main', TAB_MOVE_ICON.main, T('tabMove.mainWindow'));
      b.addEventListener('click', function () { tabMoveTo(tab, { main: true }); });
    });
    var sep = document.createElement('div'); sep.className = 'tm-sep'; s.appendChild(sep);
    targets.forEach(function (t) {
      if (t.kind !== 'new') return;
      var b = tabMoveItem(s, 'tm-new', TAB_MOVE_ICON.add, T('tabMove.newWindow'));
      if (t.off) { b.disabled = true; b.title = T('tabMove.lastTab'); }
      b.addEventListener('click', function () { if (!b.disabled) tabMoveTo(tab, { newWindow: true }); });
    });
    s.addEventListener('keydown', function (ev) {
      if (!ev) return;
      if (ev.key === 'Escape' || ev.key === 'ArrowLeft') {
        if (ev.preventDefault) ev.preventDefault();
        if (ev.stopPropagation) ev.stopPropagation();
        tabMoveSubClose(item);
        try { item.focus(); } catch (e) {}
        return;
      }
      if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
        if (ev.preventDefault) ev.preventDefault();
        var bs = Array.prototype.filter.call(s.children, function (c) { return c.tagName === 'BUTTON' && !c.disabled; });
        if (!bs.length) return;
        var i = bs.indexOf(document.activeElement);
        var k = ev.key === 'ArrowDown' ? (i + 1) % bs.length : (i <= 0 ? bs.length - 1 : i - 1);
        try { bs[k].focus(); } catch (e) {}
      }
    });
    document.body.appendChild(s);
    tabMoveSub = s;
    // Beside the item (on its left when there is no room on its right).
    var r = typeof item.getBoundingClientRect === 'function' ? item.getBoundingClientRect() : null;
    var rm = tabMoveEl && typeof tabMoveEl.getBoundingClientRect === 'function' ? tabMoveEl.getBoundingClientRect() : null;
    var sz = tabMoveSizeOf(s, targets.length + 1);
    var vw = (typeof window !== 'undefined' && window.innerWidth) || 1200;
    var right = rm && typeof rm.right === 'number' ? rm.right : ((r && r.right) || 240);
    var left = rm && typeof rm.left === 'number' ? rm.left : 20;
    var x = right + sz.w + 4 <= vw ? right + 2 : left - sz.w - 2;
    tabMovePlace(s, x, ((r && r.top) || 40) - 5, sz.w, sz.h);
  }
  item.classList.add('open'); item.setAttribute('aria-expanded', 'true');
  if (focus) {
    var first = Array.prototype.filter.call(s.children, function (c) { return c.tagName === 'BUTTON' && !c.disabled; })[0];
    if (first) { try { first.focus(); } catch (e) {} }
  }
}
function tabMoveSubClose(item) {
  var s = tabMoveSub;
  tabMoveSub = null;
  if (s && s.parentElement && typeof s.parentElement.removeChild === 'function') { try { s.parentElement.removeChild(s); } catch (e) {} }
  if (item) { item.classList.remove('open'); item.setAttribute('aria-expanded', 'false'); }
}
// (once for the page: a press elsewhere, the window's size or focus lost close it)
function tabMoveWire() {
  if (tabMoveWired) return;
  tabMoveWired = true;
  document.addEventListener('mousedown', function (ev) {
    var t = ev && ev.target, inside = function (m) { return !!m && !!t && typeof m.contains === 'function' && m.contains(t); };
    if ((tabMoveEl || tabMoveSub) && !inside(tabMoveEl) && !inside(tabMoveSub)) tabMoveMenuClose();
  }, true);
  if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
    window.addEventListener('resize', tabMoveMenuClose);
    window.addEventListener('blur', tabMoveMenuClose);
  }
}

// ── Moving ──
// where: { window: id }, { main: true }, { newWindow: true }. Resolves to
// what happened ('moved', 'opening', 'focused') or false (nothing moved).
function tabMoveTo(tab, where) {
  tabMoveMenuClose();
  if (!tab || !where) return Promise.resolve(false);
  // A session window: its tear-off, the destination given (reach_viewer.go
  // reachTearOffTab, terminal_viewer.go toolTearOff).
  if (reachDetachedMode) return Promise.resolve(reachTearOffTab(tab, where));
  if (typeof sessionTabs !== 'undefined' && sessionTabs.indexOf(tab) >= 0) return tabMoveMainSession(tab, where);
  if (toolTabs.indexOf(tab) >= 0 && tab.kind === 'files') return tabMoveMainFiles(tab, where);
  return Promise.resolve(false);
}
// The main window's ObliReach session: into a session window (connected
// there again on the same Windows session and view-only state) or a new one
// ("Detach"); it ends here once it is on its way, its side panel with it.
async function tabMoveMainSession(tab, where) {
  var dev = tab.device;
  if (!dev || devIsSupport(dev) || !reachCanDetach() || tab.tmMoving) return false;
  var key = devKey(dev), sid = reachReconnectSessionId(tab);
  sid = (typeof sid === 'number' && sid >= 0) ? sid : -1;
  tab.tmMoving = true;
  var dock = filesDockLeaving(tab), r = '';
  try {
    if (where.window > 0) {
      reachDetachedForgetState(key);
      r = String(await reachNative('reachOpenWindow', String(dev.id), String(dev.hostname || dev.id),
        Object.assign({ session: sid, viewOnly: !!tab.viewOnly, profile: devPid(dev), window: where.window }, vmWindowOpts(dev))));
    } else {
      r = (await reachOpenDetached(dev, sid, !!tab.viewOnly)) ? 'opening' : '';
    }
  } catch (e) {
    r = '';
    reachNotify(T('viewer.tabMoveFailed', { error: reachErrText(e) }), 'warn');
  } finally { tab.tmMoving = false; }
  if (r !== 'moved' && r !== 'opening' && r !== 'focused') { filesDockStay(dev, dock); return false; }
  if (r === 'moved') { reachDetachedIds.add(key); reachDetachedInfo[key] = { session: sid, viewOnly: !!tab.viewOnly }; }
  if (sessionTabs.indexOf(tab) >= 0) reachEndTabHere(tab);
  if (typeof currentRoute !== 'undefined' && currentRoute === 'device-detail' && selectedDevice && devSame(selectedDevice, dev)) openDeviceDetail(dev, 'remote');
  return r;
}
// The main window's explorer (a Files page tab): into a session window, on
// the same folder, or a new one (its Detach button: files_page.go).
async function tabMoveMainFiles(tab, where) {
  if (!(where.window > 0)) return filesDetachTab(tab);
  if (!toolInFilesPage(tab) || !reachCanDetach() || tab.detaching) return false;
  tab.detaching = true;
  try {
    if (filesLeaveAsks(tab) && !(await filesLeaveOk(tab))) return false;
    var dev = tab.device;
    var opts = { session: -1, viewOnly: false, profile: devPid(dev), kind: 'files', window: where.window };
    var path = (tab.ex && typeof tab.ex.path === 'string') ? tab.ex.path : '';
    if (path) opts.path = path.slice(0, 4096);
    var r = '';
    try { r = String(await reachNative('reachOpenWindow', String(dev.id), toolTabTitle(tab), opts)); }
    catch (e) { reachNotify(T('viewer.tabMoveFailed', { error: reachErrText(e) }), 'warn'); return false; }
    if (r !== 'moved') return false;
    if (toolTabs.indexOf(tab) >= 0) toolCloseTab(tab, { moved: true });
    return r;
  } finally { tab.detaching = false; }
}
`
