package main

// files_dock.go — the file explorer of a session, in its side panel.
//
// A session's Files button (the ObliReach toolbar, a terminal's tool bar:
// files_page.go toolsSessionFilesButton) opens the device's explorer in the
// SIDE PANEL of the window, the one Scripts and Info use (ui.go sidePanel*:
// the same slot, head, width and behaviour), by default (Settings > Remote
// sessions > "File explorer of a session": Side panel / Tab):
//   - one panel at a time: Files replaces Scripts or Info (and the chat
//     panel) and the other way round; the open one's button closes it; the
//     panel's x closes it too. Closed or replaced, the explorer is only out
//     of sight: it lives as long as its device has a session in this window
//     (its folder, its selection, its transfers going on), the Files button
//     shows it again;
//   - it holds a full explorer (explorer_viewer.go: drops, uploads, Ctrl+V,
//     type-to-filter, the disk usage analyzer, the volumes strip) of the
//     session's device, the one explorer of that device (its key
//     "<device>~files" is this window's, reported like a tab: asked for
//     elsewhere, this window comes forward on it); not the split view (no
//     room for the local pane in a side panel);
//   - it follows the session: shown on its device's page, hidden elsewhere
//     (another device's session, a mosaic on screen, another page), closed
//     once the device has no session left in this window; its session
//     moving to another window (a tab dragged or moved there, detached,
//     brought back to the main window) takes it along, on the same folder,
//     shown or not (filesDockLeaving / filesDockAdopt);
//   - "Tab" (its head): the explorer becomes a tab of THIS window, on the
//     same folder, with all a tab's features, the local pane included (the
//     main window: its Files page; a session window: its tab strip, the
//     local pane through the file bindings it shares: localfs_windows.go
//     attachSharedLocalFS); the panel closes.
// A mosaic on screen has no single session: its Files buttons open a tab,
// as before.
//
// The explorers of the panels are tool tabs of kind 'files' with docked:
// true, kept in filesDocks (never in toolTabs: no tab strip, no Files page,
// no mosaic); terminal_viewer.go routes them here (toolTabByKey,
// toolShowTab, toolShown, toolSetStatus, toolCloseTab, toolDisposeWhere).
//
// Both consts are injected verbatim after the Files page (buildUI, not
// through Sprintf: '%' is safe): no backtick here.

const filesDockCSS = `
/* ── The explorer of a session in the side panel (files_dock.go) ── */
.side-panel.sp-files .side-panel-body{overflow:hidden}
.side-panel-body>.tool-body{flex:1;min-height:0;background:var(--bg)}
.sp-acts{display:flex;align-items:center;gap:8px;flex-shrink:0}
.sp-acts:empty{display:none}
.sp-acts .dot{flex-shrink:0}
.sp-acts .btn-sm{display:inline-flex;align-items:center;justify-content:center;gap:5px;padding:4px 8px}
.sp-acts .btn-sm:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.fd-tab{color:var(--accent-l);background:rgb(var(--accent-rgb) / .12)}
.fd-tab:hover{background:rgb(var(--accent-rgb) / .2)}
`

const filesDockJS = `
// ═════════════════════════════════════════════════════════════════════════
// The explorer of a session in the side panel (files_dock.go)
// ═════════════════════════════════════════════════════════════════════════
var filesDocks = [];          // the side panels' explorers of this window (tool tabs, docked: true)
var FILES_SESSION_PREF = 'oblireach_session_files';
var FILES_DOCK_TAB_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7a2 2 0 012-2h5l2 2h7a2 2 0 012 2v10a2 2 0 01-2 2H5a2 2 0 01-2-2z"/><line x1="3" y1="10" x2="21" y2="10"/></svg>';

// ── The setting (Settings > Remote sessions) ──
// 'panel' (the default: no value saved) or 'tab' (the explorer as a tab, as before).
function filesSessionMode() { return reachPref(FILES_SESSION_PREF, 'panel') === 'tab' ? 'tab' : 'panel'; }
function filesSetSessionMode(m) { reachSetPref(FILES_SESSION_PREF, m === 'tab' ? 'tab' : 'panel'); }

// ── Which ──
function filesDockByKey(k) {
  k = String(k || '');
  for (var i = 0; i < filesDocks.length; i++) if (filesDocks[i].key === k) return filesDocks[i];
  return null;
}
function filesDockOf(dev) { return dev ? filesDockByKey(toolKeyOf(dev, 'files')) : null; }
function filesPanelBody() { return document.getElementById('side-panel-body'); }
// A mosaic on screen: no single session the panel would belong to.
function filesDockMosaic() { return typeof tgShown === 'function' && !!tgShown(); }
// The panel can open now (the session's page, no mosaic on screen).
function filesDockUsable() { return !!filesPanelBody() && !filesDockMosaic(); }
// The explorer of the session on screen (null: none).
function filesDockFor() {
  if (typeof currentRoute === 'undefined' || currentRoute !== 'device-detail' || !selectedDevice || filesDockMosaic()) return null;
  return filesDockOf(selectedDevice);
}
// The device still has a session in this window (its ObliReach tab, a
// terminal, a local RDP tab): its explorer lives on.
function filesDockSessionAlive(dev) {
  if (!dev) return false;
  var k = devKey(dev);
  if (typeof sessionTabs !== 'undefined' && sessionTabs.some(function (t) { return !!t && !!t.device && devKey(t.device) === k; })) return true;
  return toolTabs.some(function (t) { return !!t && t.kind !== 'files' && !!t.device && devKey(t.device) === k; });
}
// Its explorer is the one in the side panel now (terminal_viewer.go toolShown).
function filesDockShown(tab) {
  var body = filesPanelBody();
  return !!tab && !!body && filesDocks.indexOf(tab) >= 0 && typeof sidePanelShown !== 'undefined' && sidePanelShown === 'files' &&
    sidePanelOpen() && !!tab.box && tab.box.parentElement === body;
}

// ── In the side panel (ui.go sidePanelRender) ──
// The explorer moved into the panel's body (emptied by the caller): back
// where its list was.
function filesPanelEnter(d, body) {
  body.appendChild(d.box);
  d.pane = body;
  explorerViews(d).forEach(function (v) { // (each view on screen: explorer_tabs.go)
    var l = v.ex && v.ex.ui && v.ex.ui.list;
    if (l && v.ex.scrollTop && xvIsShown(v)) { try { l.scrollTop = v.ex.scrollTop; } catch (e) {} }
  });
}
// The explorer leaves the panel (another panel, another device, closed):
// out of sight only, its session goes on.
function filesPanelLeave() {
  var body = filesPanelBody();
  filesDocks.forEach(function (d) {
    if (!body || d.pane !== body) return;
    if (d.box && d.box.parentElement === body) { try { body.removeChild(d.box); } catch (e) {} }
    d.pane = null;
  });
}
// The panel's head for the explorer: its state, "Tab".
function filesPanelHead(d, acts) {
  if (!acts) return;
  acts.innerHTML = '';
  var dot = document.createElement('span'); dot.className = 'dot ' + toolTabDot(d); dot.id = 'fd-dot';
  dot.title = toolTxt(d.statusText) || '';
  var b = document.createElement('button'); b.className = 'btn-sm fd-tab'; b.id = 'fd-tab';
  b.innerHTML = FILES_DOCK_TAB_ICON + '<span>' + TH('filesDock.toTab') + '</span>';
  b.title = T('filesDock.toTabTitle'); b.setAttribute('aria-label', b.title);
  b.addEventListener('click', function () { filesDockToTab(d); });
  acts.appendChild(dot); acts.appendChild(b);
}
// Drawn again (its state changed, a session closed, a page changed): the
// panel follows when Files is the panel chosen.
function filesDockRender() {
  if (typeof sidePanelKind !== 'undefined' && sidePanelKind === 'files') sidePanelRender();
  else filesDockButtons();
}
// The session's Files button shows whether its explorer is in the panel
// (el: one being built, the one on screen from then on).
var filesDockBtn = null;
function filesDockButtons(el) {
  if (el) filesDockBtn = el;
  var b = filesDockBtn;
  if (!b || !b.classList || (!el && b.isConnected === false)) return;
  var on = filesDockShown(filesDockFor());
  b.classList.toggle('on', on);
  b.setAttribute('aria-pressed', on ? 'true' : 'false');
}
// After any change of the tabs or of the page (renderSessionTabs,
// navigateTo): the explorers whose session went close, the panel follows
// the session on screen.
var filesDockSyncing = false;
function filesDockSync() {
  if (filesDockSyncing) return;
  filesDockSyncing = true;
  try {
    filesDocks.slice().forEach(function (d) {
      if (filesDockSessionAlive(d.device)) { d.dockHadSession = true; return; }
      var onPage = typeof currentRoute !== 'undefined' && currentRoute === 'device-detail' && !!selectedDevice && devSame(selectedDevice, d.device);
      if (d.dockHadSession || !onPage) filesDockClose(d, { quiet: true });
    });
    filesDockRender();
  } finally { filesDockSyncing = false; }
}

// ── Opening ──
// A session's Files button: its explorer shown in the panel, or the panel
// closed when it shows it already; no explorer yet: opened as the setting
// says (a mosaic on screen: a tab).
function filesSessionOpen(dev) {
  if (!dev) return Promise.resolve('none');
  var d = filesDockOf(dev);
  // (a mosaic on screen hides the panels: the device's explorer leaves its
  // panel for a tab, where a mosaic opens it)
  if (d && filesDockMosaic()) return Promise.resolve(filesDockToHere(d));
  if (d) {
    if (filesDockShown(d)) sidePanelClose(); else filesDockReveal(d, true);
    return Promise.resolve('self');
  }
  if (filesSessionMode() === 'panel' && filesDockUsable()) return filesDockOpen(dev);
  return toolOpen(dev, 'files', reachDetachedMode ? 'here' : 'auto');
}
// The device's explorer in this window's panel: its gates and its privacy
// code first (toolOpen's); open already elsewhere (a tab of this window,
// another window): brought to the front there (one explorer per device).
// o (optional): { path } its folder, { collapsed } kept out of sight (a
// panel that followed its session here closed: filesDockAdopt).
async function filesDockOpen(dev, o) {
  if (!dev) return 'none';
  var key = toolKeyOf(dev, 'files');
  var st = toolState(dev, 'files');
  if (!st.show || !st.enabled) { reachNotify(st.reason || T('tools.reason.agentOffline'), 'warn'); return 'blocked'; }
  if (toolOpening[key]) return 'busy';
  toolOpening[key] = true;
  try {
    if (!(await privacyEnsure(dev, toolFeature('files')))) return 'cancelled';
    var had = toolTabByKey(key);
    if (had) { toolShowTab(had); return 'self'; }
    if (!reachDetachedMode) {
      var shown = false;
      try { shown = (await reachNative('reachFocusWindow', key)) === true; } catch (e) { shown = false; }
      had = toolTabByKey(key);
      if (had) { toolShowTab(had); return 'self'; }
      if (shown) return 'focused';
    } else if (typeof window.reachOpenTab === 'function' && reachNativeToken()) {
      // (the app gives the key to this window, or says where it is)
      var at = '';
      try { at = String(await reachNative('reachOpenTab', key)); }
      catch (e) { reachNotify(T('viewer.pickOpenFailed', { error: reachErrText(e) }), 'warn'); return 'error'; }
      had = toolTabByKey(key);
      if (had) { toolShowTab(had); return 'self'; }
      if (at !== 'here' && at !== 'self') return at;
    }
    return filesDockCreate(dev, key, o);
  } finally {
    delete toolOpening[key];
  }
}
// o (optional): { path } its folder, { collapsed } kept out of sight.
function filesDockCreate(dev, key, o) {
  var tab = toolNewTab(dev, 'files', key, { docked: true });
  if (!tab) return 'error';
  if (o && typeof o.path === 'string' && o.path && tab.ex) tab.ex.path = o.path.slice(0, 4096);
  if (o && o.subs) tab.xvInit = o.subs; // (its views: explorer_tabs.go)
  tab.dockHadSession = filesDockSessionAlive(dev);
  filesDocks.push(tab);
  if (typeof reachWinClaims !== 'undefined') reachWinClaims.delete(key);
  reachReportTabs();
  // In the panel when its session is the one on screen (never in place of
  // the panel of another device's session).
  if (!(o && o.collapsed === true) && filesDockFor() === tab) { sidePanelShow('files'); filesDockFocus(tab); }
  else filesDockButtons();
  try {
    var p = TOOL_IMPL.files.start(tab);
    if (p && typeof p.catch === 'function') p.catch(function () {});
  } catch (e) {}
  return 'here';
}
function filesDockFocus(tab) {
  var go = function () { if (filesDockShown(tab)) toolFocus(tab); };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(go); else go();
}
// Asked for (toolShowTab: __reachShowTab, the Files button, the Files
// page's picker): its session's page with the explorer in the panel.
function filesDockReveal(tab, focus) {
  if (filesDocks.indexOf(tab) < 0) return;
  var onPage = typeof currentRoute !== 'undefined' && currentRoute === 'device-detail' && !!selectedDevice && devSame(selectedDevice, tab.device);
  if (!onPage || filesDockMosaic()) {
    var k = devKey(tab.device);
    var rt = typeof reachTabOfDevice === 'function' ? reachTabOfDevice(k) : null;
    var tt = rt ? null : toolTabs.filter(function (t) { return t.kind !== 'files' && !!t.device && devKey(t.device) === k; })[0];
    if (rt) { if (reachDetachedMode) reachWinShowTab(rt); else switchSessionTab(rt.id); }
    else if (tt) toolShowTab(tt);
    else openDeviceDetail(tab.device, 'remote');
    // (its session is in the mosaic on screen: no panel there, a tab instead)
    if (filesDockMosaic()) { filesDockToHere(tab); return; }
  }
  sidePanelShow('files');
  if (focus) filesDockFocus(tab);
}

// ── Closing ──
// The explorer ends (its session closed, sign-out) or leaves for a tab or
// another window. o (optional): { moved } its explorer went elsewhere
// (nothing ended); { quiet } the keyboard stays where it is.
function filesDockClose(tab, o) {
  var i = filesDocks.indexOf(tab);
  if (i < 0) return;
  var shown = filesDockShown(tab);
  try { TOOL_IMPL.files.dispose(tab, { end: !(o && o.moved) }); } catch (e) {}
  filesDocks.splice(i, 1);
  if (tab.box && tab.box.parentElement && typeof tab.box.parentElement.removeChild === 'function') { try { tab.box.parentElement.removeChild(tab.box); } catch (e) {} }
  tab.pane = null;
  reachReportTabs();
  if (shown) sidePanelClose(); else filesDockButtons();
  if (!(o && o.quiet) && typeof reachFocusCanvas === 'function') reachFocusCanvas();
}
// Sign-out, a server or tenant left (toolDisposeWhere): those explorers end.
function filesDockDisposeWhere(pred) {
  var gone = filesDocks.filter(function (t) { try { return !!pred(t); } catch (e) { return false; } });
  gone.forEach(function (t) { filesDockClose(t, { quiet: true }); });
  return gone.length;
}

// ── "Tab" ──
// The explorer leaves the panel for a tab of THIS window (the main window's
// Files page, a session window's tab strip), on the same folder, with the
// local pane (localFsAvailable: every window has the file bindings).
function filesDockToTab(tab) {
  if (filesDocks.indexOf(tab) < 0) return false;
  return filesDockToHere(tab) === 'here';
}
function filesDockToHere(tab) {
  if (filesDocks.indexOf(tab) < 0) return 'none';
  var dev = tab.device, key = tab.key;
  var path = (tab.ex && typeof tab.ex.path === 'string') ? tab.ex.path.slice(0, 4096) : '';
  var subs = xvSnapshot(tab); // (its views go with it: explorer_tabs.go)
  filesDockClose(tab, { moved: true, quiet: true });
  return toolCreateTab(dev, 'files', key, (path || subs) ? { path: path, subs: subs } : null);
}

// ── Following its session to another window ──
// A session leaving this window for another one (a tab dragged or moved
// there, "Detach", "Bring it back here", the main window's strip): when the
// device keeps no other session here, its explorer goes with it. Its state
// (the folder, in the panel or not) is handed over through localStorage,
// shared by every window of the app, and the explorer closes here without
// ending anything; the window the session reaches opens it again as it was
// (filesDockAdopt: when that session's tab is created there, or when the
// hand-over arrives after it, its storage event).
var FILES_DOCK_HANDOFF = 'oblireach_dock_handoff:';
var FILES_DOCK_HANDOFF_MS = 30000; // a hand-over older than this is stale (its session never arrived)
// The device of tab keeps another session in this window (an ObliReach tab,
// a terminal, a local RDP tab): its explorer stays.
function filesDockOtherSession(tab) {
  var dev = tab && tab.device;
  if (!dev) return false;
  var k = devKey(dev);
  if (typeof sessionTabs !== 'undefined' && sessionTabs.some(function (t) { return t !== tab && !!t && !!t.device && devKey(t.device) === k; })) return true;
  return toolTabs.some(function (t) { return t !== tab && !!t && t.kind !== 'files' && !!t.device && devKey(t.device) === k; });
}
// tab (a session of this window) is about to leave it: its device's
// explorer handed over and closed here. Returns what was handed over
// (filesDockStay takes it back if the move fails), or null (none, or it stays).
function filesDockLeaving(tab) {
  if (!tab || !tab.device || filesDockOtherSession(tab)) return null;
  var d = filesDockOf(tab.device);
  if (!d) return null;
  var st = { path: (d.ex && typeof d.ex.path === 'string') ? d.ex.path.slice(0, 4096) : '', collapsed: !filesDockShown(d), at: Date.now(), subs: xvSnapshot(d) };
  // Closed first: the app hears this window let go of the explorer before
  // the window the session goes to asks for it.
  filesDockClose(d, { moved: true, quiet: true });
  var key = FILES_DOCK_HANDOFF + devKey(tab.device);
  reachSetPref(key, JSON.stringify(st));
  st.key = key;
  return st;
}
// The move failed: the explorer back here as it was.
function filesDockStay(dev, st) {
  if (!dev || !st) return;
  try { localStorage.removeItem(st.key); } catch (e) {}
  if (filesDockOf(dev)) return;
  filesDockCreate(dev, toolKeyOf(dev, 'files'), { path: st.path, collapsed: st.collapsed, subs: st.subs });
}
// The hand-over for dev, taken (removed): { path, collapsed, subs (its views) }, or null
// (none, unreadable, stale).
function filesDockTakeHandoff(dev, now) {
  var key = FILES_DOCK_HANDOFF + devKey(dev), raw = reachPref(key, null);
  if (!raw) return null;
  try { localStorage.removeItem(key); } catch (e) {}
  var st = null;
  try { st = JSON.parse(raw); } catch (e) { return null; }
  if (!st || typeof st !== 'object' || typeof st.at !== 'number') return null;
  var t = typeof now === 'number' ? now : Date.now();
  if (st.at > t + 5000 || t - st.at > FILES_DOCK_HANDOFF_MS) return null;
  return { path: (typeof st.path === 'string' && st.path.length <= 4096) ? st.path : '', collapsed: st.collapsed === true, subs: xvsValid(st.subs) };
}
// A session of dev is in this window now: the explorer it had in the window
// it came from, opened here on the same folder, in the panel or not.
function filesDockAdopt(dev) {
  if (!dev || filesDockOf(dev) || !filesDockSessionAlive(dev)) return Promise.resolve('none');
  var st = filesDockTakeHandoff(dev);
  if (!st) return Promise.resolve('none');
  return filesDockOpen(dev, st);
}
// (after the tab being created is listed: addSessionTab, toolCreateTab)
function filesDockAdoptSoon(dev) {
  if (!dev || !reachPref(FILES_DOCK_HANDOFF + devKey(dev), null)) return;
  setTimeout(function () { filesDockAdopt(dev); }, 0);
}
// The session of the hand-over's device that this window has (null: none).
function filesDockSessionDev(key) {
  var t = (typeof sessionTabs !== 'undefined' ? sessionTabs : []).filter(function (x) { return !!x && !!x.device && devKey(x.device) === key; })[0] ||
    toolTabs.filter(function (x) { return !!x && x.kind !== 'files' && !!x.device && devKey(x.device) === key; })[0];
  return t ? t.device : null;
}
if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
  // A hand-over written after the session got here ("Bring it back here":
  // the session window lets go of it once the main window has its session).
  window.addEventListener('storage', function (e) {
    if (!e || typeof e.key !== 'string' || e.key.indexOf(FILES_DOCK_HANDOFF) !== 0 || !e.newValue) return;
    var dev = filesDockSessionDev(e.key.slice(FILES_DOCK_HANDOFF.length));
    if (dev) filesDockAdopt(dev);
  });
}
`
