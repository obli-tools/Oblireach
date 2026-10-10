package main

// explorer_tabs.go — SUB-TABS of a file explorer: several views of the same
// device's files in ONE Files tab (the main window's Files page, a session
// window's Files tab, a session's side panel: files_dock.go).
//
//   - A small "+" above the breadcrumb opens another view of the device, on
//     the folder of the view in focus (Ctrl+T too); its strip shows once
//     there are two (the tab mosaic's sub-tabs: tab_groups.go tg-subtab),
//     each labelled with its folder's name (its tooltip: the whole path),
//     closed by its x, a middle click (Ctrl+W in it); the last one never
//     closes (the tab's own x does that).
//   - Each view is an explorer of its own: its folder, its selection, its
//     filter (type-to-filter), its sorting, its scroll, its disk usage
//     analyzer (explorer_viewer.go, usage_viewer.go: every function there
//     takes a view as "the tab"). The tab keeps what its views share: the
//     audit, the privacy prompt, the transfers of a session window's
//     explorer, the dock of the transfers and the files being edited, the
//     local pane of the split view (one per tab; its arrows, its drops and
//     Send work with the view in focus).
//   - A MOSAIC, as the tabs' (tab_groups.go): Ctrl+click on a sub-tab puts it
//     beside the view(s) on screen (2: two columns; 3-4: a grid; one above
//     the other when narrow), Ctrl+click on one of them takes it out; a
//     plain click shows that view alone again (as "Exit mosaic"). Ctrl+click
//     on "+": the new view beside the others. Each pane has its own bar and
//     breadcrumb; the one in focus wears the accent ring, and the keyboard
//     (type-to-filter, Ctrl+V, Delete, F2, Enter, Backspace, F5...) goes to
//     it only. The split view: the mosaic takes the device's side.
//   - No copy or move between two views: the device's commands have no
//     remote-to-remote copy (a drag from one view to another is refused).
//   - Nothing is kept across restarts (no explorer state is); a side panel's
//     explorer becoming a tab, or following its session to another window
//     (files_dock.go hand-over), keeps its views (xvSnapshot / tab.xvInit).
//
// How the state is split (xvRootDefine, xvMake): a view is an object made
// from its tab (Object.create: its id, device, label, local pane, box are
// the tab's; its device written back to the tab) with an explorer state of
// its own (its ex, made from the tab's: XV_KEYS are its own, XV_SHARED are
// the tab's) and its own pane, overlay and status. The tab's ex is the
// shared state, and its XV_KEYS (path, selection...) read the view in focus,
// so code written for "the tab" (the Files page, the dock, a move to another
// window) goes on working with what is on screen.
//
// Both consts are appended to the explorer's (explorer_viewer.go): no
// backtick here.

const explorerTabsCSS = `
/* ── Sub-tabs of a file explorer (explorer_tabs.go): the tab mosaic's look ── */
.xv-strip{display:flex;align-items:center;gap:4px;padding:6px 10px 0;flex-shrink:0;min-width:0}
.xp-pane>.xv-strip{padding:6px 12px 0}
.xv-tabs{display:flex;align-items:center;gap:4px;min-width:0;overflow-x:auto;scrollbar-width:none}
.xv-tabs:empty{display:none}
.xv-strip .tg-subtab{background:rgb(var(--ink) / .05);transition:background .15s,color .15s}
.xv-strip .tg-subtab:hover{background:rgb(var(--ink) / .09)}
.xv-strip .tg-subtab.shown{background:rgb(var(--accent-rgb) / .08);color:var(--text)}
.xv-strip .tg-subtab.active{color:var(--accent-l)}
.xv-strip .tg-subtab:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.xv-add,.xv-exit{width:24px;height:24px;flex-shrink:0;padding:0;border:none;border-radius:7px;display:inline-flex;align-items:center;justify-content:center;background:none;color:var(--muted);cursor:pointer;transition:background .15s,color .15s}
.xv-add:hover:not(:disabled),.xv-exit:hover{background:rgb(var(--ink) / .07);color:var(--text)}
.xv-add:disabled{opacity:.35;cursor:default}
.xv-add:focus-visible,.xv-exit:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.xv-exit{margin-inline-start:auto;color:var(--accent-l)}
.xv-grid{flex:1;min-height:0;min-width:0;display:grid;grid-template-columns:minmax(0,1fr);grid-auto-rows:minmax(0,1fr)}
.xv-grid.mosaic{gap:6px;padding:6px}
.exp-host.xv-pane{position:relative;min-width:0;min-height:0;display:flex;flex-direction:column;overflow:hidden}
.exp-host.xv-pane.xv-off{display:none}
.xv-grid.mosaic>.xv-pane{padding:2px;border-radius:8px;background:var(--bg);box-shadow:0 0 0 1px var(--bg3) inset;container-type:inline-size;transition:box-shadow .15s}
.xp-pane .xv-grid.mosaic>.xv-pane{background:var(--bg2)}
.xv-grid.mosaic>.xv-pane.xv-focus{box-shadow:0 0 0 2px var(--accent) inset}
.xv-grid.mosaic .exp-bar{padding:5px 8px}
.xv-grid.mosaic .exp-list{border-radius:0 0 6px 6px}
`

const explorerTabsJS = `
// ═════════════════════════════════════════════════════════════════════════
// Sub-tabs of a file explorer (explorer_tabs.go)
// ═════════════════════════════════════════════════════════════════════════
var XV_MAX = 12;       // views of one explorer at most
var XV_MAX_SHOWN = 4;  // side by side at most (2: two columns; 3-4: a grid)
var XV_COL_MIN = 340;  // a column's width at least (px): narrower, one above the other
// A view's own explorer state: the tab's fields of these names read (and
// write) the view in focus.
var XV_KEYS = ['path', 'items', 'view', 'sel', 'anchor', 'filter', 'sortKey', 'sortDir', 'listed', 'loading', 'listSeq', 'fatal', 'starting',
  'rowEls', 'scrollTop', 'usage', 'volumes', 'rootVolumes', 'dropPath', 'pasting'];
// The tab's, shared by its views (a view's fields of these names are the tab's).
var XV_SHARED = ['audited', 'privacyGate', 'transfers', 'trSeq', 'focusSide', 'dockTab'];
// A view's elements (the tab's ui reads the view in focus's: code written for
// the tab, and the tests, name them there).
var XV_UI_KEYS = ['bar', 'up', 'refresh', 'crumbs', 'path', 'filter', 'mkdir', 'upload', 'file', 'download', 'rename', 'delGrp', 'del', 'usagePipe',
  'usageBtn', 'vols', 'list', 'th', 'tbody', 'note', 'drop', 'usPane', 'us'];
var XV_PLUS_SVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>';

// ── The views on screen (pure: explorer_tabs_test.go) ──
// x: { order: [view ids, the strip's order], shown: [those on screen, in that
// order], focus: the one in focus (always one of shown) }.
function xvStateNew() { return { order: [], shown: [], focus: '', byId: {}, seq: 0, gone: [], fwd: '' }; }
function xvsSortShown(x) { x.shown.sort(function (a, b) { return x.order.indexOf(a) - x.order.indexOf(b); }); }
// A view added at the end of the strip: alone on screen, or (mosaic) beside
// those on screen. 'alone' | 'beside' | 'max' (the mosaic was full: alone).
function xvsAdd(x, id, mosaic, max) {
  max = max || XV_MAX_SHOWN;
  if (x.order.indexOf(id) < 0) x.order.push(id);
  if (mosaic && x.shown.length > 0 && x.shown.length < max) {
    if (x.shown.indexOf(id) < 0) x.shown.push(id);
    xvsSortShown(x);
    x.focus = id;
    return 'beside';
  }
  var full = !!mosaic && x.shown.length >= max;
  x.shown = [id]; x.focus = id;
  return full ? 'max' : 'alone';
}
// A plain click on a sub-tab: that view alone (the mosaic left).
function xvsShowOnly(x, id) {
  if (x.order.indexOf(id) < 0) return false;
  x.shown = [id]; x.focus = id;
  return true;
}
// Ctrl+click on a sub-tab: beside those on screen ('added', in focus), or
// taken out of the mosaic ('removed'; its neighbour in focus when it was);
// 'kept': the only one on screen stays; 'max': the mosaic is full.
function xvsToggle(x, id, max) {
  max = max || XV_MAX_SHOWN;
  if (x.order.indexOf(id) < 0) return 'none';
  var i = x.shown.indexOf(id);
  if (i >= 0) {
    if (x.shown.length < 2) return 'kept';
    x.shown.splice(i, 1);
    if (x.focus === id || x.shown.indexOf(x.focus) < 0) x.focus = x.shown[Math.min(i, x.shown.length - 1)];
    return 'removed';
  }
  if (x.shown.length >= max) return 'max';
  x.shown.push(id);
  xvsSortShown(x);
  x.focus = id;
  return 'added';
}
// A click in a pane on screen: in focus.
function xvsFocus(x, id) {
  if (x.shown.indexOf(id) < 0) return false;
  x.focus = id;
  return true;
}
// A view closed (never the last one): its neighbour in the mosaic takes the
// focus; the one shown alone: the next one of the strip (else the one before).
function xvsRemove(x, id) {
  var i = x.order.indexOf(id);
  if (i < 0 || x.order.length < 2) return false;
  x.order.splice(i, 1);
  var si = x.shown.indexOf(id);
  if (si >= 0) x.shown.splice(si, 1);
  if (!x.shown.length) x.shown = [x.order[Math.min(i, x.order.length - 1)]];
  if (x.focus === id || x.shown.indexOf(x.focus) < 0) x.focus = x.shown[Math.max(0, Math.min(si, x.shown.length - 1))];
  return true;
}
// The mosaic's columns: one view, one; two to four, two (one above the
// other when the width cannot hold two columns of colMin).
function xvsCols(n, width, colMin) {
  if (!(n > 1)) return 1;
  colMin = colMin || XV_COL_MIN;
  return (width > 0 && width < colMin * 2) ? 1 : 2;
}
// Rows a pane spans: three on two columns, the first one runs down both.
function xvsSpan(n, cols, i) { return (n === 3 && cols === 2 && i === 0) ? 2 : 1; }
// A sub-tab's name: its folder's (the drives: "Drives"; C:\ -> "C:"; '/').
function xvsLabel(path, os) {
  var cs = explorerCrumbs(path, os), last = cs[cs.length - 1];
  return String((last && last.label) || '');
}
// What a tab moving keeps of its views (files_dock.go): { paths, shown,
// focus } (shown and focus: indexes in paths); null with one view.
function xvsSnapshotOf(x, pathOf) {
  if (!x || x.order.length < 2) return null;
  return {
    paths: x.order.map(function (id) { return String(pathOf(id) || '').slice(0, 4096); }),
    shown: x.shown.map(function (id) { return x.order.indexOf(id); }).filter(function (i) { return i >= 0; }),
    focus: Math.max(0, x.order.indexOf(x.focus)),
  };
}
// Such a note, checked (it went through localStorage): null when unusable.
// A path that does not look like one opens on the top of the tree.
function xvsValid(st) {
  if (!st || typeof st !== 'object' || !Array.isArray(st.paths)) return null;
  var paths = st.paths.slice(0, XV_MAX).map(function (p) {
    return (typeof p === 'string' && p.length <= 4096 && !/[\u0000-\u001f]/.test(p) && p.indexOf('..') < 0) ? p : '';
  });
  if (paths.length < 2) return null;
  var n = paths.length, ok = function (i) { return typeof i === 'number' && isFinite(i) && Math.floor(i) === i && i >= 0 && i < n; };
  var shown = [];
  (Array.isArray(st.shown) ? st.shown : []).forEach(function (i) { if (ok(i) && shown.indexOf(i) < 0 && shown.length < XV_MAX_SHOWN) shown.push(i); });
  shown.sort(function (a, b) { return a - b; });
  var focus = ok(st.focus) ? st.focus : 0;
  if (!shown.length) shown = [focus];
  if (shown.indexOf(focus) < 0) focus = shown[0];
  return { paths: paths, shown: shown, focus: focus };
}

// ── Views and their tab ──
function xvOwnerOf(t) { return (t && t.xvOwner) || t; }
// A tab's views, in the strip's order ([]: none, a device's context...).
function xvList(tab) {
  var x = tab && tab.xv;
  return x ? x.order.map(function (id) { return x.byId[id]; }).filter(Boolean) : [];
}
// The explorers of a tab (its views; itself when it has none).
function explorerViews(t) { var o = xvOwnerOf(t), l = xvList(o); return l.length ? l : (o ? [o] : []); }
function xvFocused(tab) {
  var x = tab && tab.xv;
  if (!x) return null;
  return x.byId[x.focus] || x.byId[x.order[0]] || null;
}
// The view a function of the explorer works on: a view as is; a tab: its
// view in focus.
function xvView(t) { if (!t || t.xvOwner || !t.xv) return t; return xvFocused(t) || t; }
// A view on screen in its tab (one alone, or in the mosaic).
function xvIsShown(v) {
  var o = xvOwnerOf(v), x = o && o.xv;
  return !x || v === o || x.shown.indexOf(v.xvId) >= 0;
}
// (its tab on screen: a view is not in the lists of tabs, toolShown knows its tab)
function explorerShown(t) { return !!t && toolShown(xvOwnerOf(t)) && xvIsShown(t); }
// The tab's state: its XV_KEYS (and its ui's XV_UI_KEYS) read the view in focus.
function xvRootDefine(tab) {
  var ex = tab.ex, ui = ex.ui;
  tab.xv = xvStateNew();
  var fwd = function (obj, k, of) {
    Object.defineProperty(obj, k, { configurable: true, enumerable: true,
      get: function () { var v = xvFocused(tab); return v ? of(v)[k] : undefined; },
      set: function (val) { var v = xvFocused(tab); if (v) of(v)[k] = val; } });
  };
  XV_KEYS.forEach(function (k) { fwd(ex, k, function (v) { return v.ex; }); });
  XV_UI_KEYS.forEach(function (k) { fwd(ui, k, function (v) { return v.ex.ui; }); });
  // Each view draws its notices over its own pane: the tab's overlay is its
  // view in focus's (read), and the tab's own cover is not used.
  fwd(tab, 'overlay', function (v) { return v; });
  if (tab.ovEl && tab.ovEl.parentElement && typeof tab.ovEl.parentElement.removeChild === 'function') { try { tab.ovEl.parentElement.removeChild(tab.ovEl); } catch (e) {} }
  tab.ovEl = null;
}
// A new view of the tab (its state only: xvNew draws it).
function xvMake(tab) {
  var x = tab.xv, n = ++x.seq, root = tab.ex;
  var own = function (o, k, val) { Object.defineProperty(o, k, { value: val, writable: true, enumerable: true, configurable: true }); };
  var v = Object.create(tab);
  own(v, 'xvOwner', tab); own(v, 'xvId', 'xv' + n); own(v, 'xvN', n); own(v, 'xvSfx', n > 1 ? '-' + n : '');
  Object.defineProperty(v, 'device', { configurable: true, enumerable: true, get: function () { return tab.device; }, set: function (d) { tab.device = d; } });
  own(v, 'status', 'starting'); own(v, 'statusText', null); own(v, 'overlay', null);
  var ex = Object.create(root);
  XV_SHARED.forEach(function (k) {
    Object.defineProperty(ex, k, { configurable: true, enumerable: true, get: function () { return root[k]; }, set: function (val) { root[k] = val; } });
  });
  var init = { path: '', items: [], view: [], sel: new Set(), anchor: '', filter: '', sortKey: 'name', sortDir: 1, listed: false, loading: false, listSeq: 0,
    fatal: null, starting: false, rowEls: new Map(), scrollTop: 0, usage: undefined, volumes: undefined, rootVolumes: undefined, dropPath: undefined, pasting: false };
  XV_KEYS.forEach(function (k) { own(ex, k, init[k]); });
  own(ex, 'disposed', false); own(ex, 'closed', false);
  var ui = Object.create(root.ui);
  XV_UI_KEYS.forEach(function (k) { own(ui, k, undefined); });
  own(ex, 'ui', ui);
  own(v, 'ex', ex);
  x.byId[v.xvId] = v;
  return v;
}

// ── The strip, the grid ──
// side: where the device's explorer goes (the tab's host, or the device's
// side of the split view).
function xvBuild(tab, side) {
  var ui = tab.ex.ui;
  var strip = ui.xvStrip = explorerEl('div', 'xv-strip', side, 'xv-strip');
  var tabs = ui.xvTabs = explorerEl('div', 'xv-tabs', strip);
  tabs.setAttribute('role', 'tablist'); tabs.setAttribute('aria-label', T('explorerTabs.strip'));
  var add = ui.xvAdd = explorerEl('button', 'xv-add', strip, 'xv-add');
  add.innerHTML = XV_PLUS_SVG; add.title = T('explorerTabs.new'); add.setAttribute('aria-label', add.title);
  add.addEventListener('mousedown', function (e) { if (e && e.button === 0 && e.preventDefault) e.preventDefault(); }); // (the keyboard stays in the pane)
  add.addEventListener('click', function (e) { xvNewHere(tab, !!(e && (e.ctrlKey || e.metaKey))); });
  var exit = ui.xvExit = explorerEl('button', 'xv-exit', strip, 'xv-exit');
  exit.innerHTML = TG_ICON; exit.title = T('tabs.group.exit'); exit.setAttribute('aria-label', exit.title);
  exit.style.display = 'none';
  exit.addEventListener('mousedown', function (e) { if (e && e.button === 0 && e.preventDefault) e.preventDefault(); });
  exit.addEventListener('click', function () { var f = xvFocused(tab); if (f) xvShowOnly(tab, f); });
  // (a double click on the strip's empty part: a new view, as in a browser)
  strip.addEventListener('dblclick', function (e) { if (e && (e.target === strip || e.target === tabs)) xvNewHere(tab, false); });
  var grid = ui.xvGrid = explorerEl('div', 'xv-grid', side, 'xv-grid');
  if (typeof ResizeObserver === 'function') {
    try { ui.xvRo = new ResizeObserver(function () { xvLayout(tab); }); ui.xvRo.observe(grid); } catch (e) { ui.xvRo = null; }
  }
}
// "+", Ctrl+T: a new view on the folder of the one in focus (mosaic: beside it).
function xvNewHere(tab, mosaic) {
  var f = xvFocused(tab);
  return xvNew(tab, f ? f.ex.path : '', { mosaic: mosaic, start: true, focus: true });
}
// o: { mosaic (beside those on screen), start (listed now), focus (the
// keyboard to it), quiet (nothing drawn: the tab being built) }.
function xvNew(tab, path, o) {
  o = o || {};
  var x = tab && tab.xv;
  if (!x || !tab.ex || tab.ex.disposed) return null;
  if (x.order.length >= XV_MAX) { reachNotify(T('explorerTabs.tooMany', { count: XV_MAX }), 'warn'); return null; }
  var v = xvMake(tab);
  v.ex.path = typeof path === 'string' ? path.slice(0, 4096) : '';
  var pane = v.host = explorerEl('div', 'exp-host xv-pane xv-off', tab.ex.ui.xvGrid);
  pane.dataset.view = v.xvId;
  v.ovEl = document.createElement('div'); v.ovEl.className = 'tool-overlay';
  // A press or the keyboard in a pane: that view in focus.
  pane.addEventListener('mousedown', function () { xvFocus(tab, v, false); });
  pane.addEventListener('focusin', function () { xvFocus(tab, v, false); });
  explorerCreateView(v);
  var how = xvsAdd(x, v.xvId, !!o.mosaic, XV_MAX_SHOWN);
  if (how === 'max') reachNotify(T('explorerTabs.maxShown', { count: XV_MAX_SHOWN }), 'warn');
  explorerRender(v);
  if (!o.quiet) xvApply(tab, !!o.focus);
  if (o.start) {
    // (the tab audited already: its folder listed at once, no busy cover)
    try {
      var p = (tab.ex.audited || devIsLocal(tab.device)) ? explorerList(v, v.ex.path) : explorerOpen(v);
      if (p && typeof p.catch === 'function') p.catch(function () {});
    } catch (e) {}
  }
  return v;
}
// The views on screen as the state says: their panes, the mosaic's grid,
// the strip, the tab's state (the view in focus's).
function xvApply(tab, focusIt) {
  var x = tab && tab.xv, ui = tab && tab.ex && tab.ex.ui;
  if (!x || !ui || !ui.xvGrid) return;
  var n = x.shown.length;
  xvList(tab).forEach(function (v) {
    var on = x.shown.indexOf(v.xvId) >= 0;
    v.host.classList.toggle('xv-off', !on);
    v.host.classList.toggle('xv-focus', on && n > 1 && v.xvId === x.focus);
  });
  ui.xvGrid.classList.toggle('mosaic', n > 1);
  xvLayout(tab);
  xvRenderStrip(tab);
  xvForward(tab);
  var go = function () {
    if (tab.ex.disposed) return;
    xvLayout(tab);
    // (a pane shown again: back where its list was)
    xvList(tab).forEach(function (v) {
      var l = v.ex.ui.list;
      if (xvIsShown(v) && l && v.ex.scrollTop && l.scrollTop !== v.ex.scrollTop) { try { l.scrollTop = v.ex.scrollTop; } catch (e) {} }
    });
    if (focusIt) explorerFocus(tab);
  };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(go); else go();
}
// The grid's columns for its width (the panes one above the other when narrow).
function xvLayout(tab) {
  var x = tab && tab.xv, grid = tab && tab.ex && tab.ex.ui.xvGrid;
  if (!x || !grid) return;
  var shown = xvList(tab).filter(function (v) { return x.shown.indexOf(v.xvId) >= 0; });
  var n = shown.length, cols = xvsCols(n, Number(grid.clientWidth) || 0, XV_COL_MIN);
  var tpl = 'repeat(' + cols + ', minmax(0, 1fr))';
  if (grid.style.gridTemplateColumns !== tpl) grid.style.gridTemplateColumns = tpl;
  xvList(tab).forEach(function (v) {
    var i = shown.indexOf(v), s = (i >= 0 && xvsSpan(n, cols, i) > 1) ? 'span 2' : '';
    if (v.host.style.gridRow !== s) v.host.style.gridRow = s;
  });
}
// The tab's state is its view in focus's (its tab, its bars, the device's
// title of the split view, the local pane's arrows).
function xvForward(tab) {
  var f = xvFocused(tab), x = tab.xv;
  if (!f) return;
  if (x.fwd !== f.xvId || tab.status !== f.status || tab.statusText !== f.statusText) {
    x.fwd = f.xvId;
    toolSetStatus(tab, f.status, f.statusText);
  }
  explorerUpdateBar(f);
  if (tab.ex.split) explorerRenderHead(tab);
}
// A view's state (explorer_viewer.go: in place of toolSetStatus): its
// sub-tab's dot; the tab's when it is in focus.
function explorerSetStatus(v, status, text) {
  var o = xvOwnerOf(v);
  if (!o || o === v || !o.xv) { toolSetStatus(v, status, text); return; }
  v.status = status; v.statusText = text || null;
  if (xvFocused(o) === v) { o.xv.fwd = v.xvId; toolSetStatus(o, status, text); }
  xvRenderStrip(o);
}
function xvPathText(v) { var p = v.ex.path; return p ? p : (v.ex.os === 'windows' ? T('explorer.roots') : '/'); }
// The sub-tabs (shown once there are two).
function xvRenderStrip(tab) {
  var x = tab && tab.xv, ui = tab && tab.ex && tab.ex.ui;
  if (!x || !ui || !ui.xvTabs) return;
  var vs = xvList(tab), mosaic = x.shown.length > 1;
  ui.xvTabs.innerHTML = '';
  if (vs.length > 1) vs.forEach(function (v) { ui.xvTabs.appendChild(xvTabEl(tab, v, mosaic)); });
  ui.xvStrip.classList.toggle('multi', vs.length > 1);
  ui.xvExit.style.display = mosaic ? '' : 'none';
  ui.xvAdd.disabled = vs.length >= XV_MAX;
}
// One sub-tab: the tab mosaic's (tab_groups.go tgSubTabEl): its state, its
// folder's name, x. Click: alone; Ctrl+click: the mosaic; middle click: closed.
function xvTabEl(tab, v, mosaic) {
  var x = tab.xv, on = v.xvId === x.focus, shown = x.shown.indexOf(v.xvId) >= 0;
  var label = xvsLabel(v.ex.path, v.ex.os) || xvPathText(v);
  var el = document.createElement('div');
  el.className = 'tg-subtab xv-tab' + (on ? ' active' : '') + (shown && mosaic ? ' shown' : '');
  el.setAttribute('role', 'tab'); el.setAttribute('aria-selected', shown ? 'true' : 'false');
  el.tabIndex = 0;
  el.dataset.view = v.xvId;
  el.title = T('explorerTabs.tip', { path: xvPathText(v) });
  var dot = document.createElement('span'); dot.className = 'dot ' + toolTabDot(v);
  var nm = document.createElement('span'); nm.className = 'tg-sub-name'; nm.textContent = label;
  var cx = document.createElement('span'); cx.className = 'tg-sub-x'; cx.textContent = '\u00D7';
  cx.title = T('tools.closeTab', { name: label }); cx.setAttribute('aria-label', cx.title); cx.setAttribute('role', 'button');
  cx.addEventListener('mousedown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); if (e && e.preventDefault) e.preventDefault(); });
  cx.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); xvClose(tab, v); });
  el.appendChild(dot); el.appendChild(nm); el.appendChild(cx);
  // (the keyboard stays in the pane; a middle press never scrolls)
  el.addEventListener('mousedown', function (e) { if (e && (e.button === 0 || e.button === 1) && e.preventDefault) e.preventDefault(); });
  el.addEventListener('click', function (e) { xvTabClick(tab, v, e || {}); });
  el.addEventListener('auxclick', function (e) { if (e && e.button === 1) { if (e.preventDefault) e.preventDefault(); xvClose(tab, v); } });
  el.addEventListener('keydown', function (e) {
    if (!e || e.target !== el) return;
    if (e.key === 'Enter' || e.key === ' ') { if (e.preventDefault) e.preventDefault(); xvTabClick(tab, v, e); }
    else if (e.key === 'Delete') { if (e.preventDefault) e.preventDefault(); xvClose(tab, v); }
  });
  return el;
}
function xvTabClick(tab, v, e) {
  if (e && (e.ctrlKey || e.metaKey)) xvToggle(tab, v); else xvShowOnly(tab, v);
}
function xvShowOnly(tab, v) { if (xvsShowOnly(tab.xv, v.xvId)) xvApply(tab, true); }
function xvToggle(tab, v) {
  var r = xvsToggle(tab.xv, v.xvId, XV_MAX_SHOWN);
  if (r === 'max') { reachNotify(T('explorerTabs.maxShown', { count: XV_MAX_SHOWN }), 'warn'); return; }
  if (r === 'kept') { xvFocus(tab, v, true); return; }
  if (r !== 'none') xvApply(tab, true);
}
// A view in focus (a press or the keyboard in its pane; focusIt: the
// keyboard to its list).
function xvFocus(tab, v, focusIt) {
  var x = tab && tab.xv;
  if (!x || !v || x.byId[v.xvId] !== v || tab.ex.disposed) return;
  if (x.focus !== v.xvId && xvsFocus(x, v.xvId)) {
    var n = x.shown.length;
    xvList(tab).forEach(function (w) { w.host.classList.toggle('xv-focus', n > 1 && w.xvId === x.focus); });
    xvRenderStrip(tab);
    xvForward(tab);
  }
  if (focusIt) explorerFocus(v);
}
// The view under an element (its pane), else the one in focus.
function xvAt(tab, t) {
  var vs = xvList(tab);
  for (var i = 0; t && i < vs.length; i++) { try { if (vs[i].host && typeof vs[i].host.contains === 'function' && vs[i].host.contains(t)) return vs[i]; } catch (e) {} }
  return xvFocused(tab) || vs[0] || tab;
}
// A view closed (its x, a middle click, Ctrl+W; never the last one): its
// commands on their way go on to their end (a session window's transfer
// finishes; the transfers of the split view are the device's queue
// anyway), its analyzer and its dialog stop; the tab disposes of it when it
// closes.
function xvClose(tab, v) {
  var x = tab && tab.xv;
  if (!x || !v || x.order.indexOf(v.xvId) < 0 || x.order.length < 2) return false;
  if (v.host && v.host.classList.contains('editing')) explorerEditPath(v, false); // (its path being typed: given up)
  xvsRemove(x, v.xvId);
  delete x.byId[v.xvId];
  v.ex.closed = true;
  usageDispose(v);
  if (explorerDlgDone && explorerDlgTab === v) explorerDlgDone(null);
  if (typeof xpDrag !== 'undefined' && xpDrag && xpDrag.view === v) xpDragEnd();
  explorerMenuClose();
  x.gone.push(v);
  if (v.host && v.host.parentElement && typeof v.host.parentElement.removeChild === 'function') { try { v.host.parentElement.removeChild(v.host); } catch (e) {} }
  xvApply(tab, true);
  return true;
}
// The Close of a view's "could not open" notice: that view (the tab with the last one).
function explorerCloseView(v) {
  var o = xvOwnerOf(v);
  if (o !== v && xvList(o).length > 1) { xvClose(o, v); return; }
  toolCloseTab(o);
}
// Its views, for a move (files_dock.go): null with one.
function xvSnapshot(tab) {
  var x = tab && tab.xv;
  return x ? xvsSnapshotOf(x, function (id) { var v = x.byId[id]; return v ? v.ex.path : ''; }) : null;
}
// The views of a tab moved here (explorerStart, before anything is listed).
function xvRestore(tab, st) {
  st = xvsValid(st);
  var x = tab && tab.xv, first = xvList(tab)[0];
  if (!st || !first) return;
  first.ex.path = st.paths[0];
  var vs = [first];
  for (var i = 1; i < st.paths.length; i++) { var v = xvNew(tab, st.paths[i], { quiet: true }); if (v) vs.push(v); }
  x.shown = st.shown.filter(function (k) { return k < vs.length; }).map(function (k) { return vs[k].xvId; });
  if (!x.shown.length) x.shown = [vs[0].xvId];
  xvsSortShown(x);
  var f = vs[Math.min(st.focus, vs.length - 1)];
  x.focus = x.shown.indexOf(f.xvId) >= 0 ? f.xvId : x.shown[0];
  xvApply(tab, false);
}
`
