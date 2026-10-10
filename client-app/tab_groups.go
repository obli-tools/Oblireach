package main

// tab_groups.go — MOSAICS: groups of tabs shown together (replacing the
// terminals' "split view").
//
// A mosaic is a GROUP of tabs of this window, of any kind: ObliReach
// sessions (sessionTabs), terminals (SSH / CMD / PowerShell, remote or local),
// the file explorer of a session window, a local RDP session (toolTabs). It is
// ONE tab of the strip (an icon, its first names, its count); shown, its
// members are its sub-tabs and their panes share the page in a grid.
//
//   - Ctrl+click on a tab while another (not in a mosaic) is on screen: a
//     mosaic of the two. Ctrl+click on a tab while a mosaic is on screen: it
//     joins that one. Ctrl+click on a sub-tab: it leaves (a tab of the strip
//     again, next to the mosaic). A mosaic left with one tab is that tab
//     again (a mosaic of one shows nothing more than the tab).
//   - Only "Exit mosaic" (and closing it) breaks a mosaic. Opening a tab
//     (the "+" list, a device, a tool, a tab moving in) never does: the new
//     tab is a tab of the strip, on screen; the mosaic waits in its tab.
//   - Several mosaics in one window; the broadcast (what is typed in one of
//     its terminals goes to all of them) belongs to each.
//   - The member in focus (its cell outlined) is activeSessionTabId: every
//     function written for "the tab on screen" goes on working with it. An
//     ObliReach member in focus is the full Remote pane (toolbar, keyboard,
//     mouse) in its cell; the others show their live picture (click: in
//     focus). Terminals, the explorer and RDP are live in every cell.
//   - Closing a member closes that tab (it leaves its mosaic); closing the
//     mosaic (its tab's ×, asked first) closes them all.
//   - A session window's mosaic moves whole (dragged out of the strip, or the
//     window dragged by it onto another window): its first member goes the
//     usual way, then the others into that member's window (tearOff's "into",
//     detached_windows.go); the window they reach builds the mosaic again
//     from a note left in localStorage (shared by every window of the app).
//     A sub-tab dragged out of its strip tears that member off alone.
//   - Ctrl+Alt+< / > (terminal_viewer.go tabShortcutStep): the tabs of the
//     strip in turn; a mosaic on screen is stepped through member by member.
//
// Both consts are injected verbatim after the terminals' (ui.go buildUI, not
// through Sprintf: '%' is safe); no backtick here.

const tabGroupsCSS = `
/* ── Mosaics (tab_groups.go) ── */
.tg-tab .tg-ico{display:inline-flex;align-items:center;color:var(--accent-l);flex-shrink:0}
.tg-tab .tg-count{flex-shrink:0;padding:0 5px;border-radius:8px;background:rgb(var(--accent-rgb) / .14);color:var(--accent-l);font-size:10px;font-weight:700;line-height:15px}
.tg-wrap{flex:1;min-height:0;display:flex;flex-direction:column}
.tg-head{display:flex;align-items:center;gap:8px;padding:6px 8px;flex-shrink:0;min-width:0}
.tg-subtabs{flex:1;min-width:0;display:flex;align-items:center;gap:4px;overflow-x:auto;scrollbar-width:none}
.tg-subtab{display:inline-flex;align-items:center;gap:5px;padding:4px 8px;border-radius:7px;background:var(--bg2);color:var(--muted);font-size:11.5px;cursor:pointer;white-space:nowrap;flex-shrink:0;max-width:240px;user-select:none}
.tg-subtab:hover{color:var(--text)}
.tg-subtab.active{color:var(--accent-l);box-shadow:inset 0 -2px 0 var(--accent)}
.tg-subtab.tearing{opacity:.45}
.tg-subtab .dot{width:6px;height:6px;flex-shrink:0}
.tg-subtab .tg-sub-kind{flex-shrink:0;padding:1px 5px;border-radius:5px;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);font-size:9.5px;font-weight:700;letter-spacing:.3px}
.tg-subtab .tg-sub-name{overflow:hidden;text-overflow:ellipsis}
.tg-subtab .tg-sub-x{opacity:.55;padding:0 2px;font-size:13px;line-height:1}
.tg-subtab .tg-sub-x:hover{opacity:1;color:var(--danger)}
.tg-acts{display:flex;align-items:center;gap:6px;flex-shrink:0}
.tg-grid{flex:1;min-height:0;display:grid;grid-auto-rows:minmax(0,1fr);gap:6px;padding:0 6px 6px}
.tg-cell{display:flex;flex-direction:column;min-height:0;min-width:0;padding:2px;background:var(--term-bg);border-radius:8px;overflow:hidden;box-shadow:0 0 0 1px var(--bg3) inset}
.tg-cell.focus{box-shadow:0 0 0 2px var(--accent) inset}
.tg-cell-bar{flex-shrink:0;flex-wrap:nowrap}
.tg-cell-bar .tb-devname{min-width:0;flex:1}
/* The machine's name next to the kind chip in every mosaic cell: .tb-devname
   is hidden outside the focused full-window toolbar (reach_viewer.go), and
   in a frameless window; a cell must always say which machine it is. */
body .tg-cell .tg-cell-bar.tool-bar .tb-devname{display:inline-flex!important;max-width:none}
.tg-reach-live{flex:1;min-height:0;display:flex;flex-direction:column;position:relative;overflow:hidden}
.tg-reach-live #remote-pane{flex:1;min-height:0}
.tg-reach-pic{flex:1;min-height:0;position:relative;background:var(--viewport-bg);cursor:pointer;overflow:hidden}
.tg-reach-pic canvas{position:absolute;inset:0;margin:auto;display:block;max-width:100%;max-height:100%}
.tg-reach-hint{position:absolute;left:0;right:0;bottom:8px;text-align:center;font-size:11px;color:var(--muted);pointer-events:none}
`

const tabGroupsJS = `
// ── Mosaics (tab_groups.go) ─────────────────────────────────────────────────
// [{ id: 'grp_N', members: [tab id, ...], focus: tab id, broadcast: bool,
//    move: null | { token, keys } (being rebuilt here: a mosaic moving in) }]
var tabGroups = [];
var tabGroupSeq = 0;
var TG_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5"/></svg>';

// ── The tabs ──
function tgAnyTab(id) {
  if (!id) return null;
  if (typeof sessionTabs !== 'undefined') for (var i = 0; i < sessionTabs.length; i++) if (sessionTabs[i].id === id) return sessionTabs[i];
  return typeof toolTabById === 'function' ? toolTabById(id) : null;
}
function tgIsReach(tab) { return !!tab && typeof sessionTabs !== 'undefined' && sessionTabs.indexOf(tab) >= 0; }
function tgIsTool(tab) { return !!tab && typeof toolTabs !== 'undefined' && toolTabs.indexOf(tab) >= 0; }
// A tab of this window's strip (the main window's explorers are its Files
// page's: never in a mosaic).
function tgCandidate(tab) { return tgIsReach(tab) || (tgIsTool(tab) && (reachDetachedMode || !toolInFilesPage(tab))); }
// The key the app knows a tab by (its device's; a tool's: "~<kind>" more).
function tgKeyOf(tab) { return tgIsTool(tab) ? String(tab.key || '') : (tab && tab.device ? devKey(tab.device) : ''); }
function tgById(id) {
  for (var i = 0; i < tabGroups.length; i++) if (tabGroups[i].id === id) return tabGroups[i];
  return null;
}
function tgOf(id) {
  if (!id) return null;
  for (var i = 0; i < tabGroups.length; i++) if (tabGroups[i].members.indexOf(id) >= 0) return tabGroups[i];
  return null;
}
function tgMembers(g) { return g ? g.members.map(tgAnyTab).filter(Boolean) : []; }
// The mosaic on screen: the one the tab in focus belongs to.
function tgShown() { return (typeof activeSessionTabId !== 'undefined') ? tgOf(activeSessionTabId) : null; }
function tgDrop(g) { var i = tabGroups.indexOf(g); if (i >= 0) tabGroups.splice(i, 1); }
// Members gone (closed, a sign-out...) out; a mosaic of one is its tab again
// (not while it is being rebuilt here: its other tabs are on their way).
function tgPrune() {
  tabGroups.slice().forEach(function (g) {
    g.members = g.members.filter(function (id) { return !!tgAnyTab(id); });
    if (g.members.indexOf(g.focus) < 0) g.focus = g.members[0] || null;
    if (!g.members.length || (g.members.length < 2 && !g.move)) tgDrop(g);
  });
}
// The tabs of the strip in their order (ObliReach sessions, then tools).
function tgStripTabs() {
  var out = [];
  if (typeof sessionTabs !== 'undefined') sessionTabs.forEach(function (t) { out.push(t); });
  if (typeof toolTabs !== 'undefined') toolTabs.forEach(function (t) { if (reachDetachedMode || !toolInFilesPage(t)) out.push(t); });
  return out;
}
// What the strip shows: each tab, a mosaic in place of its members (where its first one is).
function tgStripItems() {
  tgPrune();
  var out = [], seen = {};
  tgStripTabs().forEach(function (t) {
    var g = tgOf(t.id);
    if (!g) { out.push({ tab: t }); return; }
    if (!seen[g.id]) { seen[g.id] = true; out.push({ group: g }); }
  });
  return out;
}
function tgTopCount() { return tgStripItems().length; }
// A member's name: its device, and its tool's (ObliReach: the device alone).
function tgName(tab) { return tgIsTool(tab) ? String(tab.label || '') + ' ' + T(toolLabelKey(tab.kind)) : String((tab && tab.label) || (tab && tab.device && tab.device.hostname) || ''); }
function tgShortName(g) {
  var ms = tgMembers(g);
  return ms.slice(0, 2).map(function (t) { var n = String(t.label || (t.device && t.device.hostname) || ''); return n.length > 14 ? n.slice(0, 13) + '…' : n; }).join(' · ');
}
function tgTitle(g) { return T('tabs.group.tip', { names: tgMembers(g).map(tgName).join(', ') }); }
function tgDotOf(tab) { return tgIsReach(tab) ? (typeof reachTabStatus === 'function' ? reachTabStatus(tab) : 'offline') : toolTabDot(tab); }
function tgKindLabel(tab) { return tgIsReach(tab) ? T(tab && devIsVm(tab.device) ? 'vm.kind' : 'tools.reach') : T(toolLabelKey(tab.kind)); } // (a Hyper-V VM console: vm_console.go)

// ── On screen ──
// A tab on screen (its mosaic when it has one, with it in focus): always the
// page drawn again (its sessions go on).
function tgShowTab(tab) {
  if (!tab) return;
  if (reachDetachedMode && typeof devPickerClose === 'function') devPickerClose();
  if (tgIsTool(tab)) toolShowTab(tab);
  else if (tgIsReach(tab)) { switchSessionTab(tab.id); if (reachDetachedMode && typeof reachFocusCanvas === 'function') reachFocusCanvas(); }
}
function tgShowGroup(g) {
  var ms = tgMembers(g);
  if (!ms.length) return;
  var f = tgAnyTab(g.focus);
  tgShowTab(f && g.members.indexOf(f.id) >= 0 ? f : ms[0]);
}
// toolShowTab / switchSessionTab: a member on screen is its mosaic, focused on it.
function tgNoteShown(tab) { var g = tab && tgOf(tab.id); if (g) g.focus = tab.id; }
// The page as it is, drawn again (a mosaic changed).
function tgRerender() {
  var a = tgAnyTab(typeof activeSessionTabId !== 'undefined' ? activeSessionTabId : null);
  if (a) tgShowTab(a); else if (typeof renderSessionTabs === 'function') renderSessionTabs();
}
// Another member in focus. Between two tools of one page (a session window,
// or one device in the main window): only the outline and the keyboard move;
// else (an ObliReach member, whose full pane goes with the focus; another
// device's page) the page is drawn again.
function tgFocusMember(tab) {
  var g = tab && tgOf(tab.id);
  if (!g || activeSessionTabId === tab.id) return;
  var cur = tgAnyTab(activeSessionTabId);
  g.focus = tab.id;
  var light = !!cur && tgOf(cur.id) === g && !tgIsReach(cur) && !tgIsReach(tab) &&
    (reachDetachedMode || (!!cur.device && !!tab.device && devSame(cur.device, tab.device)));
  if (!light) { tgShowTab(tab); return; }
  activeSessionTabId = tab.id;
  selectedDevice = tab.device;
  tgMembers(g).forEach(function (t) { if (t.tgCellEl && t.tgCellEl.classList) t.tgCellEl.classList.toggle('focus', t.id === tab.id); });
  renderSessionTabs();
  if (reachDetachedMode && typeof reachWinSetTitle === 'function') reachWinSetTitle(tab.device);
  if (typeof reachUpdateClipboardWatch === 'function') reachUpdateClipboardWatch();
  toolFocus(tab);
}

// ── Making and breaking ──
// Ctrl+click on a tab (the strip) or a sub-tab (the mosaic's).
function tgCtrlClick(tab) {
  if (!tab) return;
  if (tab.members) { tgShowGroup(tab); return; } // (a mosaic's own tab: shown)
  if (!tgCandidate(tab)) { tgShowTab(tab); return; }
  if (tgOf(tab.id)) { tgRemove(tab); return; }
  var shown = tgShown();
  if (shown) { tgAdd(shown, tab); return; }
  var cur = tgAnyTab(activeSessionTabId);
  if (cur && cur !== tab && tgCandidate(cur) && !tgOf(cur.id)) { tgCreate([cur, tab], tab); return; }
  tgShowTab(tab);
}
function tgCreate(tabs, focus) {
  var g = { id: 'grp_' + (++tabGroupSeq), members: [], focus: null, broadcast: false, move: null };
  tabs.forEach(function (t) { if (t && g.members.indexOf(t.id) < 0 && !tgOf(t.id)) g.members.push(t.id); });
  if (g.members.length < 2) return null;
  g.focus = (focus && g.members.indexOf(focus.id) >= 0) ? focus.id : g.members[0];
  tabGroups.push(g);
  tgShowGroup(g);
  return g;
}
function tgAdd(g, tab) {
  if (!g || !tab || tgOf(tab.id) || !tgCandidate(tab)) return;
  g.members.push(tab.id);
  g.focus = tab.id;
  tgShowGroup(g);
}
// A member out (a tab of the strip again); the mosaic stays on screen (one of
// two left: that tab alone).
function tgRemove(tab) {
  var g = tab && tgOf(tab.id);
  if (!g) return;
  g.members.splice(g.members.indexOf(tab.id), 1);
  if (g.focus === tab.id) g.focus = g.members[0] || null;
  if (g.members.length < 2 && !g.move) { tgDrop(g); var last = tgAnyTab(g.members[0]); tgShowTab(last || tab); return; }
  tgShowGroup(g);
}
// "Exit mosaic": its tabs go back to the strip, the one in focus on screen.
function tgDissolve(g) {
  if (!g) return;
  var f = tgAnyTab(g.focus) || tgMembers(g)[0];
  tgDrop(g);
  if (f) tgShowTab(f); else renderSessionTabs();
}
// Closing a mosaic (its tab's ×): asked first, then every member closes.
function tgCloseGroup(g) {
  var ms = tgMembers(g);
  if (!ms.length) { tgDrop(g); renderSessionTabs(); return; }
  if (ms.length > 1 && typeof confirm === 'function' && !confirm(T('tabs.group.closeConfirm', { count: ms.length }))) return;
  tgDrop(g);
  ms.forEach(function (t) {
    if (tgIsTool(t)) toolCloseTab(t);
    else if (tgIsReach(t)) closeSessionTab(t.id);
  });
}
// A tab closing or moving away (toolCloseTab, closeSessionTab): out of its
// mosaic. Returns its mosaic's tab to show next when it was in focus there
// (null: it was in none).
function tgOnGone(id) {
  var g = tgOf(id);
  if (!g) return null;
  var i = g.members.indexOf(id);
  var rest = g.members.filter(function (m) { return m !== id && !!tgAnyTab(m); });
  g.members = rest;
  if (!rest.length) { tgDrop(g); return null; }
  var next = rest[Math.min(i, rest.length - 1)];
  if (g.focus === id || rest.indexOf(g.focus) < 0) g.focus = next;
  if (rest.length < 2 && !g.move) tgDrop(g);
  return tgAnyTab(g.focus) || tgAnyTab(next);
}

// ── Broadcast: what is typed in one of its terminals goes to them all ──
function tgBroadcastTargets(tab) {
  var g = tab && tgOf(tab.id);
  if (!g || !g.broadcast) return null;
  return tgMembers(g).filter(function (t) { return termIsTab(t); });
}

// ── The strips ──
function tgGroupClose(g, e) { if (e && e.stopPropagation) e.stopPropagation(); tgCloseGroup(g); }
// The main window's tab strip (renderSessionTabs, ui.go).
function tgMainGroupEl(g) {
  var el = document.createElement('div');
  var on = tgShown() === g;
  el.className = 'session-tab tg-tab' + (on ? ' active' : '');
  el.dataset.groupId = g.id;
  el.title = tgTitle(g);
  var ic = document.createElement('span'); ic.className = 'tg-ico'; ic.innerHTML = TG_ICON;
  var nm = document.createElement('span'); nm.className = 'tg-name'; nm.textContent = tgShortName(g);
  var ct = document.createElement('span'); ct.className = 'tg-count'; ct.textContent = String(g.members.length);
  var x = document.createElement('span'); x.className = 'tab-close'; x.title = T('tabs.group.close', { count: g.members.length }); x.textContent = '×';
  x.addEventListener('click', function (e) { tgGroupClose(g, e); });
  el.appendChild(ic); el.appendChild(nm); el.appendChild(ct); el.appendChild(x);
  el.addEventListener('click', function () { tgShowGroup(g); });
  el.addEventListener('auxclick', function (e) { if (e && e.button === 1) { if (e.preventDefault) e.preventDefault(); tgCloseGroup(g); } });
  return el;
}
// A session window's title bar (reachRenderWinTabs): dragged like a tab
// (out of the strip: the whole mosaic to another window).
function tgWinGroupEl(g) {
  var on = tgShown() === g;
  var el = document.createElement('div');
  el.className = 'wt-tab tg-tab' + (on ? ' active' : '');
  el.setAttribute('role', 'tab');
  el.setAttribute('aria-selected', on ? 'true' : 'false');
  el.title = tgTitle(g);
  el.dataset.groupId = g.id;
  var ic = document.createElement('span'); ic.className = 'tg-ico'; ic.innerHTML = TG_ICON;
  var nm = document.createElement('span'); nm.className = 'wt-tab-name'; nm.textContent = tgShortName(g);
  var ct = document.createElement('span'); ct.className = 'tg-count'; ct.textContent = String(g.members.length);
  el.appendChild(ic); el.appendChild(nm); el.appendChild(ct);
  var x = document.createElement('button');
  x.className = 'wt-tab-x'; x.tabIndex = -1; x.title = T('tabs.group.close', { count: g.members.length });
  x.innerHTML = '<svg width="9" height="9" viewBox="0 0 10 10"><path d="M1 1l8 8M9 1l-8 8" fill="none" stroke="currentColor" stroke-width="1.6"/></svg>';
  x.addEventListener('pointerdown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); }); // not a tab drag
  x.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); });
  x.addEventListener('click', function (e) { tgGroupClose(g, e); });
  el.appendChild(x);
  reachWireWinTab(el, g);
  reachWinTabs.els[g.id] = el;
  g.members.forEach(function (id) { reachWinTabs.els[id] = el; }); // (its members scroll it into view)
  return el;
}

// ── The mosaic on screen (renderRemoteTab, ui.go) ──
var tgCellRendering = false; // an ObliReach member's Remote pane being drawn in its cell
var tgSubStrip = null;       // { el, groupId }: the sub-tabs on screen
function tgRenderShown(tc) {
  if (tgCellRendering) return false;
  tgPrune();
  var g = tgShown();
  if (!g) { tgSubStrip = null; return false; }
  var ms = tgMembers(g);
  if (!ms.length) { tgSubStrip = null; return false; }
  if (!reachDetachedMode && document.body && document.body.classList) document.body.classList.remove('reach-focus');
  var act = tgAnyTab(activeSessionTabId);
  if (!tgIsReach(act)) { remoteWs = null; remoteDecoder = null; }
  var wrap = document.createElement('div'); wrap.className = 'tg-wrap'; wrap.id = 'tg-wrap';
  var head = document.createElement('div'); head.className = 'tg-head'; head.id = 'tg-head';
  var subs = document.createElement('div'); subs.className = 'tg-subtabs'; subs.id = 'tg-subtabs';
  subs.setAttribute('role', 'tablist'); subs.title = T('tabs.group.hint');
  tgSubStrip = { el: subs, groupId: g.id };
  tgFillSubTabs(subs, g);
  head.appendChild(subs);
  var acts = document.createElement('div'); acts.className = 'tg-acts';
  if (ms.filter(function (t) { return termIsTab(t); }).length >= 2) {
    var bc = document.createElement('button'); bc.className = 'btn-sm' + (g.broadcast ? ' reach-btn-primary' : ''); bc.id = 'tg-broadcast';
    bc.textContent = T('tabs.group.broadcast'); bc.title = T('tabs.group.broadcastTip');
    bc.setAttribute('aria-pressed', g.broadcast ? 'true' : 'false');
    bc.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); }); // (the keyboard stays where it is)
    bc.addEventListener('click', function () {
      g.broadcast = !g.broadcast;
      bc.className = 'btn-sm' + (g.broadcast ? ' reach-btn-primary' : '');
      bc.setAttribute('aria-pressed', g.broadcast ? 'true' : 'false');
      var f = tgAnyTab(activeSessionTabId); if (f) toolFocus(f);
    });
    acts.appendChild(bc);
  }
  var ex = document.createElement('button'); ex.className = 'btn-sm'; ex.id = 'tg-exit';
  ex.textContent = T('tabs.group.exit'); ex.title = T('tabs.group.exitTip');
  ex.addEventListener('click', function () { tgDissolve(g); });
  acts.appendChild(ex);
  head.appendChild(acts);
  wrap.appendChild(head);
  var n = ms.length, cols = n <= 2 ? n : (n <= 4 ? 2 : (n <= 9 ? 3 : 4));
  var grid = document.createElement('div'); grid.className = 'tg-grid'; grid.id = 'tg-grid';
  grid.style.gridTemplateColumns = 'repeat(' + cols + ', minmax(0, 1fr))';
  wrap.appendChild(grid);
  tc.appendChild(wrap);
  // (each cell in the page BEFORE its pane is drawn: an ObliReach member in
  // focus finds its Remote pane by id, reachUI / reachRestoreView, and a
  // pane drawn off the page showed its session "Not connected")
  ms.forEach(function (tab) { tgCell(g, tab, grid); });
  return true;
}
// A cell: its title (state, kind, name, theme, close), its pane.
function tgCell(g, tab, grid) {
  var focus = tab.id === activeSessionTabId;
  var cell = document.createElement('div'); cell.className = 'tg-cell' + (focus ? ' focus' : '');
  cell.dataset.tabId = tab.id;
  tab.tgCellEl = cell;
  if (grid) grid.appendChild(cell);
  if (tgIsReach(tab) && focus) {
    // In focus: its full Remote pane, the keyboard and the mouse its (the
    // same session: its socket and decoder, reachRestoreView; never another).
    tgUnwatchReachPic(tab);
    var live = document.createElement('div'); live.className = 'tg-reach-live';
    cell.appendChild(live);
    tgCellRendering = true;
    try { renderRemoteTab(live); } catch (e) {} finally { tgCellRendering = false; }
    return cell;
  }
  var bar = document.createElement('div'); bar.className = 'remote-toolbar tool-bar tg-cell-bar';
  var dot = document.createElement('span'); dot.className = 'dot ' + tgDotOf(tab);
  tab.tgDot = dot;
  var chip = document.createElement('span'); chip.className = 'tool-kind'; chip.textContent = tgIsReach(tab) ? tgKindLabel(tab) : T(toolNameKey(tab.kind));
  var nm = document.createElement('span'); nm.className = 'tb-devname'; nm.title = String(tab.label || '');
  var nmTxt = document.createElement('span'); nmTxt.textContent = String(tab.label || ''); nm.appendChild(nmTxt);
  bar.appendChild(dot); bar.appendChild(chip); bar.appendChild(nm);
  var acts = document.createElement('div'); acts.className = 'tool-acts';
  if (termIsTab(tab)) acts.appendChild(termThemeButton(tab));
  bar.appendChild(acts);
  var x = document.createElement('span'); x.className = 'tab-close'; x.textContent = '×';
  x.title = tgIsReach(tab) ? T('viewer.tabDisconnect', { name: String(tab.label || '') }) : T('tools.closeTab', { name: toolTabTitle(tab) });
  x.addEventListener('mousedown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); });
  x.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); if (tgIsReach(tab)) closeSessionTab(tab.id); else toolCloseTab(tab); });
  bar.appendChild(x);
  if (termIsTab(tab)) termThemeContext(bar, tab);
  cell.appendChild(bar);
  if (tgIsReach(tab)) {
    // Not in focus: its live picture (reachPaint paints it), a click takes it.
    var pic = document.createElement('div'); pic.className = 'tg-reach-pic';
    var cv = tgReachCanvas(tab);
    pic.appendChild(cv);
    var hint = document.createElement('div'); hint.className = 'tg-reach-hint'; hint.textContent = T('tabs.group.takeControl');
    pic.appendChild(hint);
    cell.appendChild(pic);
    tgWatchReachPic(tab, pic);
    cell.addEventListener('mousedown', function () { tgFocusMember(tab); });
    return cell;
  }
  var impl = TOOL_IMPL[tab.kind] || {};
  cell.appendChild(tab.box);
  tab.pane = cell;
  if (termIsTab(tab)) termApplyTabTheme(tab);
  cell.addEventListener('mousedown', function () { if (activeSessionTabId !== tab.id) tgFocusMember(tab); });
  // (the explorer draws its own bar; a terminal's buttons are its menu's)
  var ui = { pane: cell, bar: bar, actions: tab.kind === 'rdp' ? acts : document.createElement('div'), status: document.createElement('span') };
  try { if (typeof impl.render === 'function') impl.render(tab, ui); } catch (e) {}
  toolRenderOverlay(tab);
  return cell;
}
// An ObliReach member's picture in its cell: a canvas of its own, kept with
// the tab (reachPaint draws every frame into it while it is in a mosaic).
function tgReachCanvas(tab) {
  if (!tab.groupCanvas) {
    tab.groupCanvas = document.createElement('canvas');
    tab.groupCanvas.className = 'tg-reach-canvas';
    var lc = tab.lastCanvas;
    if (lc && lc.width > 0 && lc.height > 0) {
      tab.groupCanvas.width = lc.width; tab.groupCanvas.height = lc.height;
      try { var c = tab.groupCanvas.getContext('2d'); if (c) c.drawImage(lc, 0, 0); } catch (e) {}
    }
  }
  return tab.groupCanvas;
}
// The picture fitted to its cell (the Remote pane's Fit): as large as the box
// allows, its ratio kept, centred; in CSS pixels.
function tgFitRect(boxW, boxH, w, h) {
  if (!(boxW > 0 && boxH > 0 && w > 0 && h > 0)) return null;
  var s = Math.min(boxW / w, boxH / h);
  var fw = Math.max(1, Math.round(w * s)), fh = Math.max(1, Math.round(h * s));
  return { x: Math.round((boxW - fw) / 2), y: Math.round((boxH - fh) / 2), w: fw, h: fh };
}
// Its picture laid out in its cell, measured once the cell is on the page
// (never before: a box not laid out yet has no size) and again whenever the
// cell or the picture changes size. Until then the stylesheet fits it
// (max-width / max-height, centred).
function tgFitReachPic(tab) {
  var pic = tab && tab.groupPic, c = tab && tab.groupCanvas;
  if (!pic || !c) return null;
  if (c.parentElement !== pic || pic.isConnected === false) { tgUnwatchReachPic(tab); return null; } // (its cell is gone)
  var r = tgFitRect(pic.clientWidth, pic.clientHeight, c.width, c.height);
  if (!r) return null;
  var st = c.style;
  st.left = r.x + 'px'; st.top = r.y + 'px'; st.width = r.w + 'px'; st.height = r.h + 'px';
  st.right = 'auto'; st.bottom = 'auto'; st.margin = '0';
  return r;
}
function tgUnwatchReachPic(tab) {
  if (tab.groupRO) { try { tab.groupRO.disconnect(); } catch (e) {} }
  tab.groupRO = null; tab.groupPic = null;
}
function tgWatchReachPic(tab, pic) {
  tgUnwatchReachPic(tab);
  tab.groupPic = pic;
  tgFitReachPic(tab);
  if (typeof ResizeObserver === 'function') {
    try { tab.groupRO = new ResizeObserver(function () { tgFitReachPic(tab); }); tab.groupRO.observe(pic); } catch (e) { tab.groupRO = null; }
  }
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(function () { tgFitReachPic(tab); });
}
// reachPaint (reach_viewer.go): a frame of a session in a mosaic.
function tgPaintReach(tab, img, w, h) {
  var c = tab && tab.groupCanvas;
  if (!c || !tgOf(tab.id) || !(w > 0 && h > 0)) return;
  if (c.width !== w || c.height !== h) { c.width = w; c.height = h; tgFitReachPic(tab); }
  try { var ctx = c.getContext('2d'); if (ctx) ctx.drawImage(img, 0, 0); } catch (e) {}
}
// A member's state changed (toolSetStatus): its cell's dot, and the strips.
function tgMemberStatus(tab) {
  if (tab && tab.tgDot) tab.tgDot.className = 'dot ' + tgDotOf(tab);
}

// ── The sub-tabs ──
function tgFillSubTabs(subs, g) {
  subs.innerHTML = '';
  tgMembers(g).forEach(function (tab) { subs.appendChild(tgSubTabEl(g, tab)); });
}
// renderSessionTabs / reachRenderWinTabs: the sub-tabs follow (states, focus).
function tgRefreshSubTabs() {
  var s = tgSubStrip;
  if (!s || tgSubDrag) return;
  var g = tgShown();
  if (!g || g.id !== s.groupId || s.el.isConnected === false) return;
  tgFillSubTabs(s.el, g);
}
function tgSubTabEl(g, tab) {
  var on = tab.id === activeSessionTabId;
  var el = document.createElement('div'); el.className = 'tg-subtab' + (on ? ' active' : '');
  el.setAttribute('role', 'tab'); el.setAttribute('aria-selected', on ? 'true' : 'false');
  el.dataset.tabId = tab.id;
  el.title = tgName(tab) + ' — ' + T('tabs.group.subTip');
  var dot = document.createElement('span'); dot.className = 'dot ' + tgDotOf(tab);
  var kc = document.createElement('span'); kc.className = 'tg-sub-kind'; kc.textContent = tgKindLabel(tab);
  var nm = document.createElement('span'); nm.className = 'tg-sub-name'; nm.textContent = String(tab.label || '');
  var x = document.createElement('span'); x.className = 'tg-sub-x'; x.textContent = '×';
  x.title = tgIsReach(tab) ? T('viewer.tabDisconnect', { name: String(tab.label || '') }) : T('tools.closeTab', { name: toolTabTitle(tab) });
  x.addEventListener('pointerdown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); });
  x.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); if (tgIsReach(tab)) closeSessionTab(tab.id); else toolCloseTab(tab); });
  el.appendChild(dot); el.appendChild(kc); el.appendChild(nm); el.appendChild(x);
  el.addEventListener('mousedown', function (e) { if (e && e.button === 0 && e.preventDefault) e.preventDefault(); }); // (the keyboard stays in the pane)
  if (reachDetachedMode) {
    // A session window: a press is a click, or a drag out of the sub-tabs
    // tearing this member off alone (to a new window, or another one).
    el.addEventListener('pointerdown', function (e) { tgSubDown(e, el, tab); });
    el.addEventListener('pointermove', tgSubMove);
    el.addEventListener('pointerup', tgSubUp);
    el.addEventListener('pointercancel', function () { tgSubEnd(); });
    el.addEventListener('lostpointercapture', function () { tgSubEnd(); });
  } else {
    el.addEventListener('click', function (e) { tgSubClick(tab, e); });
  }
  el.addEventListener('auxclick', function (e) {
    if (e && e.button === 1) { if (e.preventDefault) e.preventDefault(); if (tgIsReach(tab)) closeSessionTab(tab.id); else toolCloseTab(tab); }
  });
  if (termIsTab(tab)) termThemeContext(el, tab);
  return el;
}
function tgSubClick(tab, e) {
  if (e && (e.ctrlKey || e.metaKey)) { tgRemove(tab); return; }
  tgFocusMember(tab);
}
var tgSubDrag = null;
function tgSubDown(e, el, tab) {
  if (!e || e.button !== 0 || tgSubDrag) return;
  var s = tgSubStrip && tgSubStrip.el;
  var r = (s && typeof s.getBoundingClientRect === 'function') ? s.getBoundingClientRect() : { top: 0, height: 30 };
  var bottom = typeof r.bottom === 'number' ? r.bottom : (r.top || 0) + (r.height || 0);
  tgSubDrag = { tab: tab, el: el, pointerId: e.pointerId, x0: e.clientX, y0: e.clientY, top: r.top || 0, bottom: bottom, mode: 'click', moved: false };
  try { if (el.setPointerCapture) el.setPointerCapture(e.pointerId); } catch (x) {}
}
function tgSubMove(e) {
  var d = tgSubDrag;
  if (!d || !e) return;
  var mode = reachTabDragMode({ dx: e.clientX - d.x0, dy: e.clientY - d.y0, x: e.clientX, y: e.clientY, stripTop: d.top, stripBottom: d.bottom,
    winW: window.innerWidth, winH: window.innerHeight, tabs: 2, moved: d.moved });
  if (mode === 'click') return;
  d.moved = true;
  d.mode = mode;
  if (d.el && d.el.classList) d.el.classList.toggle('tearing', mode === 'tear');
  if (document.body && document.body.classList) document.body.classList.toggle('wt-tearing', mode === 'tear');
}
function tgSubUp(e) {
  var d = tgSubDrag;
  tgSubEnd();
  if (!d) return;
  if (!d.moved) { tgSubClick(d.tab, e); return; }
  if (d.mode === 'tear') reachTearOffTab(d.tab);
}
function tgSubEnd() {
  var d = tgSubDrag;
  if (!d) return;
  tgSubDrag = null;
  if (d.el && d.el.classList) d.el.classList.remove('tearing');
  if (document.body && document.body.classList) document.body.classList.remove('wt-tearing');
  try { if (d.el && d.el.releasePointerCapture) d.el.releasePointerCapture(d.pointerId); } catch (x) {}
}

// ── A mosaic moving to another window (a session window's) ──
// The note left for the window it goes to (localStorage, shared by every
// window): { <token>: { keys: [its tabs' keys, in order], focus, broadcast,
// ts, ready, flat } }. ready: its first tab arrived there (the others may
// follow: tearOff "into" that tab's window). flat: no mosaic, only the
// ready signal (the Devices page's "Connect" on several devices, all of
// them as tabs of one session window: devices_select.go).
var TG_MOVE_KEY = 'oblireach_tabgroup_moves';
var TG_MOVE_TTL_MS = 60000;
var TG_MOVE_WAIT_MS = 15000;
var TG_MOVE_POLL_MS = 150;
function tgMovesRead() {
  var v = null;
  try { v = JSON.parse(localStorage.getItem(TG_MOVE_KEY) || 'null'); } catch (e) { v = null; }
  var out = {}, now = Date.now();
  if (!v || typeof v !== 'object' || Array.isArray(v)) return out;
  for (var k in v) {
    if (!Object.prototype.hasOwnProperty.call(v, k) || !/^[0-9a-f]{16,64}$/.test(k)) continue;
    var r = v[k];
    if (!r || typeof r !== 'object' || !Array.isArray(r.keys) || !(now - (+r.ts || 0) < TG_MOVE_TTL_MS)) continue;
    out[k] = { keys: r.keys.filter(function (x) { return typeof x === 'string'; }).slice(0, 64), focus: typeof r.focus === 'string' ? r.focus : '',
      broadcast: r.broadcast === true, ts: +r.ts, ready: r.ready === true, flat: r.flat === true };
  }
  return out;
}
function tgMovesWrite(m) { try { localStorage.setItem(TG_MOVE_KEY, JSON.stringify(m)); } catch (e) {} }
function tgMoveToken() {
  var b = new Uint8Array(16);
  try { crypto.getRandomValues(b); } catch (e) { for (var i = 0; i < b.length; i++) b[i] = Math.floor(Math.random() * 256); }
  var s = '';
  for (var j = 0; j < b.length; j++) s += (b[j] < 16 ? '0' : '') + b[j].toString(16);
  return s;
}
// Can it move? A terminal: only live (its shell parked, taken over there);
// a local RDP session: connected; the others always.
function tgMovable(tab) {
  if (termIsTab(tab)) return !!(tab.live && tab.ws && tab.ws.readyState === 1 && tab.token) && !(typeof termDropBusy === 'function' && termDropBusy(tab));
  if (tgIsTool(tab) && tab.kind === 'rdp') return !!tab.connId;
  return true;
}
function tgSleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }
async function tgMoveWaitReady(token) {
  var until = Date.now() + TG_MOVE_WAIT_MS;
  for (;;) {
    var m = tgMovesRead()[token];
    if (!m) return false;
    if (m.ready) return true;
    if (Date.now() >= until) return false;
    await tgSleep(TG_MOVE_POLL_MS);
  }
}
// The whole mosaic to another window (reachTearOffTab of its tab; how.merge:
// this window dragged by it onto another one). Resolves like reachTearOffTab.
async function tgTearOff(g, how) {
  var ms = tgMembers(g);
  var merge = !!(how && how.merge);
  if (!ms.length || g.moving || (!merge && tgTopCount() < 2)) return false;
  if (ms.some(function (t) { return !tgMovable(t); })) { reachNotify(T('tabs.group.cannotMove'), 'warn'); return false; }
  var token = tgMoveToken(), keys = ms.map(tgKeyOf), f = tgAnyTab(g.focus);
  var mv = tgMovesRead();
  mv[token] = { keys: keys, focus: f ? tgKeyOf(f) : keys[0], broadcast: !!g.broadcast, ts: Date.now(), ready: false };
  tgMovesWrite(mv);
  g.moving = true;
  tgDrop(g); // (its tabs leave as tabs; the mosaic is built again where they land)
  var r = await reachTearOffTab(ms[0], merge ? { merge: true } : undefined);
  g.moving = false;
  if (r !== 'opening' && r !== 'moved') {
    // Nothing moved: the mosaic stays here as it was.
    var m2 = tgMovesRead(); delete m2[token]; tgMovesWrite(m2);
    if (tgMembers(g).length >= 2 && tabGroups.indexOf(g) < 0) { g.members = g.members.filter(function (id) { return !!tgAnyTab(id); }); tabGroups.push(g); }
    tgRerender();
    return false;
  }
  for (var i = 1; i < ms.length; i++) {
    if (!tgAnyTab(ms[i].id)) continue; // (closed meanwhile)
    if (!(await tgMoveWaitReady(token))) break; // (its first tab never arrived: the others stay)
    await reachTearOffTab(ms[i], { into: keys[0] });
  }
  return r;
}
// A tab created here (toolCreateTab, addSessionTab): one of a mosaic moving
// here joins its mosaic again (the first one makes it).
function tgAdoptCheck(tab) {
  var key = tgKeyOf(tab);
  // (the main window's explorer goes to its Files page: never in a mosaic;
  // what arrived of the others stays one when the wait is over)
  if (!key || !tgCandidate(tab)) return;
  var mv = tgMovesRead(), tok = null;
  for (var k in mv) if (Object.prototype.hasOwnProperty.call(mv, k) && mv[k].keys.indexOf(key) >= 0) { tok = k; break; }
  if (!tok) return;
  var rec = mv[tok], g = null;
  if (rec.flat) { // (tabs of one window, no mosaic: arrived, the others may follow)
    rec.keys = rec.keys.filter(function (kk) { return kk !== key; });
    rec.ready = true;
    if (rec.keys.length) mv[tok] = rec; else delete mv[tok];
    tgMovesWrite(mv);
    return;
  }
  for (var i = 0; i < tabGroups.length; i++) if (tabGroups[i].move && tabGroups[i].move.token === tok) g = tabGroups[i];
  if (!g) {
    g = { id: 'grp_' + (++tabGroupSeq), members: [], focus: null, broadcast: rec.broadcast, move: { token: tok, keys: rec.keys.slice() } };
    tabGroups.push(g);
    var gg = g;
    setTimeout(function () { // (some never came: what arrived stays a mosaic, if two)
      if (!gg.move || gg.move.token !== tok) return;
      gg.move = null;
      var was = tgShown() === gg;
      tgPrune();
      if (was) tgRerender(); else renderSessionTabs();
    }, TG_MOVE_WAIT_MS + 5000);
  }
  if (g.members.indexOf(tab.id) < 0 && !tgOf(tab.id)) g.members.push(tab.id);
  var pos = function (id) { var t = tgAnyTab(id); var p = rec.keys.indexOf(t ? tgKeyOf(t) : ''); return p < 0 ? 999 : p; };
  g.members.sort(function (a, b) { return pos(a) - pos(b); });
  if (key === rec.focus || !g.focus) g.focus = tab.id;
  var done = rec.keys.every(function (kk) { return g.members.some(function (id) { var t = tgAnyTab(id); return !!t && tgKeyOf(t) === kk; }); });
  if (done) { g.move = null; delete mv[tok]; }
  else { rec.ready = true; mv[tok] = rec; }
  tgMovesWrite(mv);
}
`
