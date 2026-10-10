package main

// files_page.go — the Files page of the main window (nav rail "Files", right
// under Chat): the device file explorers, in large, as the Chat page shows
// the conversations.
//
// Like the Chat page (ui.go chatPage*): the page header (how many explorers
// are open, or what to do), under it one tab per explorer (its server's
// colour when several servers show, its state, × to close it) with "+" after
// them; then the explorer in front or, with none in front or after "+", a
// device picker: the Devices list (same rows, tree, search / state / OS
// filters, multi-server separators: devViewFill) listing the devices whose
// explorer this user may use at all (filesListed: the server lists the
// Obliance agent's tools, the team holds the 'files' capability, not a legacy
// agent; the others never open it and are left out, as the Chat page leaves
// out the devices without ObliReach). Those that cannot open it NOW are
// greyed (the agent offline, no live channel, privacy mode without a code),
// their button off, the reason in its tooltip and, on a click, in the
// picker's note. A click opens the device's explorer through toolOpen
// (agent_tools.go: the gates, then the device's security code when privacy
// mode is on, the web's unlock flow); a device that already has an explorer
// switches to it. Visibility: only the devices the server lists for this
// user (GET /api/reach/overview), narrowed, never widened.
//
// The explorer itself is explorer_viewer.go (one implementation: the tool
// tabs' TOOL_IMPL.files); its tabs are the tool tabs of kind 'files'
// (terminal_viewer.go toolTabs: the same keys, reports to the app, disposal at
// a sign-out / a server or tenant left). WHERE an explorer opens, whatever
// asks for it (the coherent rule of every entry point):
//   - the main window: in its Files page. The Devices / Dashboard row
//     button, the device page's header and agent pane and this page's
//     picker bring the device's explorer to the front there, opening it
//     first; with "Detach by default", in a session window of its own
//     instead, like a session (its Detach button sends one of this page
//     there too: filesDetachTab). The session tab strip keeps the ObliReach
//     sessions and the terminals.
//   - a session's Files button (toolsSessionFilesButton): in the side panel
//     of Scripts and Info, next to the session, by default (files_dock.go),
//     else as above.
//   - a session window (no nav rail): a tab of that window, as before (its
//     "+" list), with the local pane too (the file bindings it shares with
//     the main window: local_pane.go localFsAvailable).
//   - asked for while it is open in another window: that window comes to the
//     front on it (toolTabOpen: a session window; the main window's Files
//     page through __reachShowTab).
// toolShowTab / toolShown / toolSetStatus / toolCloseTab / toolDisposeWhere
// route the main window's explorers here (toolInFilesPage).
//
// Both consts are injected verbatim after the explorer (buildUI, not through
// Sprintf: '%' is safe): no backtick here.

const filesPageCSS = `
/* ── Files page (files_page.go): the main window's file explorers ── */
.files-page{flex:1;display:flex;flex-direction:column;min-height:0;min-width:0;overflow:hidden}
.files-page .page-header .breadcrumb:empty{display:none}
.files-page-tabbar{display:flex;align-items:flex-start;gap:6px;padding:0 24px 10px;flex-shrink:0}
.files-page .chat-tabs{flex:0 1 auto;min-width:0;flex-wrap:wrap;gap:6px;padding:0;overflow:visible}
.files-page .chat-tab{font-size:12px;padding:7px 9px 7px 11px}
.files-page-pane{flex:1;min-height:0;display:flex;flex-direction:column;position:relative}
.reach-files-btn .tool-lock{margin-inline-start:1px}
`

const filesPageJS = `
// ═════════════════════════════════════════════════════════════════════════
// Files page (files_page.go): the main window's file explorers
// ═════════════════════════════════════════════════════════════════════════
var filesPageView = null;  // built at its first visit, kept (the same elements at every visit)
var filesActiveId = null;  // the explorer in front (a tool tab's id)
var filesPicking = false;  // the device picker asked for ("+")
var filesPickerFilter = { q: '', online: '', os: '', filesOnly: true }; // (devFilterMatch: filesListed / filesUsable)
var filesPickerSearchFolded = new Set();
var FILES_PLUS_SVG = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>';

// ── Which explorers ──
// The main window keeps its explorers in this page; a session window as tabs.
// (an explorer of a session's side panel is the panel's: files_dock.go)
function toolInFilesPage(tab) { return !!tab && tab.kind === 'files' && !tab.docked && !reachDetachedMode; }
function filesPageKind(kind) { return kind === 'files' && !reachDetachedMode; }
function filesTabs() { return reachDetachedMode ? [] : toolTabs.filter(function (t) { return !!t && t.kind === 'files'; }); }
function filesTabOf(dev) { if (!dev) return null; var t = toolTabByKey(toolKeyOf(dev, 'files')); return toolInFilesPage(t) ? t : null; }
function filesActiveTab() { var t = filesActiveId ? toolTabById(filesActiveId) : null; return toolInFilesPage(t) ? t : null; }
function filesPageShown() { return !!filesPageView && typeof currentRoute !== 'undefined' && currentRoute === 'files'; }
function filesPickerShown() { return !!filesPageView && (filesPicking || !filesActiveTab()); }
// This explorer's pane is the one on screen (terminal_viewer.go toolShown).
function filesTabShown(tab) {
  var v = filesPageView;
  return !!v && !!tab && filesPageShown() && !filesPickerShown() && filesActiveId === tab.id && !!tab.pane && tab.pane.parentElement === v.pane;
}
// The devices of the picker: those whose explorer this user may use at all
// (the others never open it: left out). filesUsable: it opens now (the
// explorer's gates, toolState): the other rows are greyed.
function filesListed(d) {
  if (typeof devIsLocal === 'function' && devIsLocal(d)) return cxnIsSsh(d); // (a local SSH connection: its SFTP)
  return !!(d && d._tools) && devCan(d, 'files') && !devLegacy(d);
}
function filesUsable(d) { return toolState(d, 'files').enabled; }

// ── The page ──
function filesPageBuild() {
  var mk = function (tag, cls) { var e = document.createElement(tag); if (cls) e.className = cls; return e; };
  var root = mk('div', 'files-page'); root.id = 'files-page';
  var head = mk('div', 'page-header');
  var h1 = mk('h1'); h1.textContent = T('nav.files'); h1.setAttribute('data-i18n', 'nav.files');
  var count = mk('span', 'breadcrumb');
  var actions = mk('div', 'page-actions');
  // (the picker's: the machines as they are now)
  var refresh = mk('button', 'btn-sm'); refresh.textContent = T('common.refresh'); refresh.setAttribute('data-i18n', 'common.refresh');
  refresh.addEventListener('click', async function () {
    // (a local base's connections read again; a server's devices, unless the base alone is on screen)
    if (typeof serverProfiles !== 'undefined' && serverProfiles.some(function (p) { return p.local; })) { try { await localConnsLoad(); localDevProbe(true); } catch (e) {} }
    if (!activeServerIsLocal()) { try { await loadOverview(); } catch (e) {} }
    filesPickerRefreshIfShown();
  });
  // The files being edited, every device's (edit_page.go): the pill, its popover.
  var pill = mk('button', 'files-edit-pill hide'); // (no id: the page's elements have none but its root)
  pill.setAttribute('aria-haspopup', 'dialog'); pill.setAttribute('aria-expanded', 'false');
  pill.addEventListener('click', function () { editPopToggle(pill); });
  actions.appendChild(pill);
  actions.appendChild(refresh);
  head.appendChild(h1); head.appendChild(count); head.appendChild(actions);
  var tabbar = mk('div', 'files-page-tabbar');
  var tabs = mk('div', 'chat-tabs'); tabs.setAttribute('role', 'tablist');
  var add = mk('button', 'chat-tab-add');
  add.title = T('files.page.newExplorer'); add.setAttribute('data-i18n-title', 'files.page.newExplorer');
  add.setAttribute('aria-label', T('files.page.newExplorer')); add.setAttribute('data-i18n-aria', 'files.page.newExplorer');
  add.innerHTML = FILES_PLUS_SVG;
  add.addEventListener('click', filesPickerToggle);
  tabbar.appendChild(tabs); tabbar.appendChild(add);
  // The explorer in front: its tool pane (terminal_viewer.go toolRenderPane:
  // its state, then the explorer's own toolbar and list).
  var pane = mk('div', 'files-page-pane');
  // The device picker: the Devices page's filters and table (devViewFill),
  // its own filters, no id (the elements are the view's).
  var picker = mk('div', 'page-body files-picker');
  var bar = mk('div', 'filter-bar');
  var q = mk('input'); q.style.minWidth = '220px';
  q.placeholder = T('devices.searchPlaceholder'); q.setAttribute('data-i18n-ph', 'devices.searchPlaceholder'); q.value = filesPickerFilter.q;
  q.addEventListener('input', function (e) { filesPickerFilter.q = e.target.value; filesPickerSearchFolded = new Set(); filesPickerNote(''); filesPickerFill(); });
  bar.appendChild(q);
  bar.appendChild(devPickerSelect('', [['', 'devices.filter.allStates'], ['online', 'devices.filter.online'], ['offline', 'devices.filter.offline']], filesPickerFilter.online, function (val) { filesPickerFilter.online = val; filesPickerFill(); }));
  // (SSH with a local base: its connections, listed with the machines: ui.go
  // localDevMatch; filesSyncOsOptions)
  var osSel = devPickerSelect('', [['', 'devices.filter.allOs'], ['windows', 'Windows'], ['linux', 'Linux'], ['macos', 'macOS']], filesPickerFilter.os, function (val) { filesPickerFilter.os = val; filesPickerFill(); });
  bar.appendChild(osSel);
  bar.appendChild(mk('span', 'spacer'));
  var hint = mk('span', 'muted-hint');
  var pickCount = mk('span'); pickCount.textContent = '0';
  var pickCountLabel = mk('span'); pickCountLabel.textContent = T('devices.resultsWord', { count: 0 });
  pickCountLabel.style.marginInlineStart = '.25em'; // (the count, then the word)
  hint.appendChild(pickCount); hint.appendChild(pickCountLabel);
  bar.appendChild(hint);
  var pickNote = mk('div', 'chat-picker-note hide');
  var list = mk('div', 'panel');
  var pb = mk('div', 'panel-body flush'); pb.style.overflowX = 'auto';
  var table = mk('table', 'dt');
  var thead = mk('thead'); thead.innerHTML = devTableHead();
  var pickBody = mk('tbody');
  table.appendChild(thead); table.appendChild(pickBody); pb.appendChild(table); list.appendChild(pb);
  picker.appendChild(bar); picker.appendChild(pickNote); picker.appendChild(list);
  // Esc: back to the explorer in front (when there is one).
  picker.addEventListener('keydown', function (e) { if (e && e.key === 'Escape' && filesActiveTab()) { if (e.preventDefault) e.preventDefault(); filesPickerToggle(); } });
  root.appendChild(head); root.appendChild(tabbar); root.appendChild(pane); root.appendChild(picker);
  filesPageView = { root: root, count: count, refresh: refresh, editPill: pill, tabbar: tabbar, tabs: tabs, add: add, pane: pane, paneTabId: null, osSel: osSel,
    picker: picker, pickQ: q, pickBody: pickBody, pickCount: pickCount, pickCountLabel: pickCountLabel, pickNote: pickNote, pickSig: null };
  return filesPageView;
}
// navigateTo('files') (ui.go). A visit shows the explorer in front, the
// picker when there is none.
// The type filter "SSH" while a local base exists (its connections' SFTP).
function filesSyncOsOptions(v) {
  var sel = v && v.osSel;
  if (!sel) return;
  var want = typeof serverProfiles !== 'undefined' && serverProfiles.some(function (p) { return p.local; });
  var has = null;
  for (var i = 0; i < sel.children.length; i++) if (sel.children[i].value === 'ssh') has = sel.children[i];
  if (want && !has) { var o = document.createElement('option'); o.value = 'ssh'; o.textContent = 'SSH'; sel.appendChild(o); }
  else if (!want && has) {
    try { sel.removeChild(has); } catch (e) {}
    if (filesPickerFilter.os === 'ssh') { filesPickerFilter.os = ''; sel.value = ''; }
  }
}
function renderFilesPage(pc) {
  var v = filesPageView || filesPageBuild();
  filesSyncOsOptions(v);
  // A local base: its connections read again (their SFTP: the picker lists them).
  if (typeof serverProfiles !== 'undefined' && serverProfiles.some(function (p) { return p.local; })) localConnsLoad().then(function () { filesPickerRefreshIfShown(); }, function () {});
  pc.appendChild(v.root); // the same elements at every visit: nothing bound twice
  filesPicking = !filesActiveTab();
  filesPickerNote('');
  v.pickSig = null;   // (the list drawn again: the machines may have changed meanwhile)
  v.paneTabId = null; // (its explorer drawn again in the page, where it was scrolled)
  filesRender();
  filesEditPillRender();
  editRecover(); // (the page reloaded: the files still being edited)
  if (filesPickerShown()) filesPickerFocus();
}
// Draws the page from the explorers of this window: the header, the tabs,
// then the explorer in front (moved into the page when it changed, not at
// every state change: the operator may be typing in its filter) or the picker.
function filesRender() {
  var v = filesPageView;
  if (!v) return;
  if (filesActiveId && !filesActiveTab()) filesActiveId = null; // (closed)
  var tabs = filesTabs(), picking = filesPickerShown();
  var srvDots = filesShowServers(tabs);
  v.tabs.innerHTML = '';
  tabs.forEach(function (t) { v.tabs.appendChild(filesTabEl(t, picking ? null : filesActiveId, srvDots)); });
  v.count.textContent = tabs.length ? T('files.page.count', { count: tabs.length }) : T('files.page.pickHint');
  v.tabbar.classList.toggle('hide', !tabs.length);
  v.add.classList.toggle('on', picking);
  v.add.setAttribute('aria-pressed', picking ? 'true' : 'false');
  v.pane.classList.toggle('hide', picking);
  v.picker.classList.toggle('hide', !picking);
  v.refresh.classList.toggle('hide', !picking);
  if (picking) {
    // The explorer leaves the page while the picker is up (its elements, ids
    // included, off the document; it goes on): drawn again when it comes
    // back, where it was scrolled, and it takes the keyboard.
    v.pane.innerHTML = '';
    v.paneTabId = null;
  } else if (filesPageShown()) {
    var a = filesActiveTab();
    if (v.paneTabId !== a.id || !a.pane || a.pane.parentElement !== v.pane || a.box.parentElement !== a.pane) {
      v.pane.innerHTML = '';
      v.paneTabId = a.id;
      toolRenderPane(v.pane, a);
    }
  }
  // The picker's rows: drawn when it comes on screen and when the explorers
  // change (a device with an explorer switches to it), not at every state.
  var sig = picking && filesPageShown() ? tabs.map(function (t) { return t.key; }).join('\n') : null;
  if (sig !== null && sig !== v.pickSig) { v.pickSig = sig; filesPickerFill(); }
  else if (sig === null) v.pickSig = null;
}
// Explorers of several servers (or multi-server mode): each tab shows its
// server (a dot in its colour, never a border), as the chat tabs do.
function filesShowServers(tabs) {
  if (typeof serverMode !== 'undefined' && serverMode === 'multi') return true;
  var seen = {}, n = 0;
  (tabs || filesTabs()).forEach(function (t) { var p = devPid(t.device); if (!seen[p]) { seen[p] = true; n++; } });
  return n > 1;
}
function filesTabDot(tab) {
  var s = tab && tab.status;
  return s === 'live' ? 'on' : (s === 'starting' || s === 'connecting') ? 'warn' : s === 'error' ? 'bad' : '';
}
// One tab (a new element at every render). activeId: the tab shown as
// active (none while the picker is up).
function filesTabEl(tab, activeId, srvDots) {
  var on = tab.id === activeId;
  var t = document.createElement('div');
  t.className = 'chat-tab files-tab' + (on ? ' active' : '');
  t.setAttribute('role', 'tab'); t.setAttribute('aria-selected', on ? 'true' : 'false');
  // Reachable from the keyboard (Tab); Enter / Space bring it to the front.
  t.tabIndex = 0; t.setAttribute('data-tool-id', tab.id);
  var pid = devPid(tab.device);
  var sn = (srvDots || pid !== (activeServerId || '')) && typeof srvName === 'function' ? srvName(pid) : '';
  // Its tooltip: its server, where it is (the folder on screen).
  t.title = [sn, tab.label, toolTxt(tab.statusText)].filter(Boolean).join(' · ');
  if (sn) {
    var sd = document.createElement('span'); sd.className = 'ct-srv'; sd.style.background = srvColor(pid); sd.title = sn;
    t.appendChild(sd);
  }
  var st = filesTabDot(tab);
  var dot = document.createElement('span'); dot.className = 'ct-dot' + (st ? ' ' + st : '');
  var nm = document.createElement('span'); nm.textContent = tab.label;
  var x = document.createElement('button'); x.className = 'ct-x'; x.textContent = '×';
  x.title = T('tools.closeTab', { name: tab.label }); x.setAttribute('aria-label', x.title);
  x.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); filesCloseTab(tab); });
  t.appendChild(dot); t.appendChild(nm); t.appendChild(x);
  t.addEventListener('click', function () { filesShowTab(tab); });
  t.addEventListener('auxclick', function (e) { if (e && e.button === 1) { if (e.preventDefault) e.preventDefault(); filesCloseTab(tab); } });
  // A right click: "Move to window" (tab_move.go), a session window's or a new one.
  t.addEventListener('contextmenu', function (e) { if (tabMoveOffered(tab)) tabMoveMenuOpen(tab, e); });
  t.addEventListener('keydown', function (e) {
    if (!e || (e.key !== 'Enter' && e.key !== ' ')) return;
    if (e.target && e.target !== t) return; // (its × button: its own key)
    if (e.preventDefault) e.preventDefault();
    filesShowTab(tab); // (the explorer takes the keyboard, as after a click)
  });
  return t;
}

// Closing an explorer whose device's transfers run (the split view): asked
// first, they are cancelled. Its files being edited go on (the header's
// pill keeps them). Nothing running: closed at once.
async function filesCloseTab(tab) {
  if (filesLeaveAsks(tab) && !(await filesLeaveOk(tab))) return;
  toolCloseTab(tab);
}
// The explorer may leave this page (closed, detached): its device's
// transfers running are cancelled once the operator agreed. false: kept.
// (Nothing running: no question, and filesCloseTab closes at once.)
function filesLeaveAsks(tab) { return !!tab && !!tab.ex && !!tab.ex.split && typeof xferDevActive === 'function' && xferDevActive(tab.device); }
async function filesLeaveOk(tab) {
  if (filesLeaveAsks(tab)) {
    var ok = await explorerDialog({ tab: tab, title: T('xfer.closeTitle'), text: T('xfer.closeConfirm', { device: reachDevName(tab.device) }), ok: 'xfer.closeAnyway', danger: true, icon: EXPLORER_ICON.upload });
    if (ok !== true || toolTabs.indexOf(tab) < 0) return false;
    xferCancelAll(xferQueues.get(devKey(tab.device)));
  }
  return !!tab && toolTabs.indexOf(tab) >= 0;
}

// ── Detach: an explorer of this page in a session window of its own ──
// The same folder there (reachOpenWindow { kind: 'files', path }); its
// window then works like any session window's tab: dragged into another
// session window, onto this window's title bar to come back here (its local
// pane with it), into a mosaic.
function filesDetachButton(acts, tab) {
  if (!acts || !toolInFilesPage(tab) || typeof reachCanDetach !== 'function' || !reachCanDetach()) return null;
  var b = document.createElement('button');
  b.className = 'btn-sm reach-ico-btn files-detach'; b.id = 'files-detach';
  b.innerHTML = REACH_ICON.popout;
  b.title = T('files.detach'); b.setAttribute('aria-label', b.title);
  b.addEventListener('click', function () { filesDetachTab(tab); });
  acts.appendChild(b);
  return b;
}
async function filesDetachTab(tab) {
  if (!toolInFilesPage(tab) || !reachCanDetach() || tab.detaching) return false;
  tab.detaching = true;
  try {
    if (filesLeaveAsks(tab) && !(await filesLeaveOk(tab))) return false;
    var dev = tab.device;
    var path = (tab.ex && typeof tab.ex.path === 'string') ? tab.ex.path : '';
    toolCloseTab(tab); // (this window lets go of it first: the app gives its key to the new window)
    var r = await toolOpenDetached(dev, 'files', path);
    return r === 'opening' || r === 'focused';
  } finally { tab.detaching = false; }
}

// ── An explorer in front ──
// toolShowTab (terminal_viewer.go) of a main window's explorer: this page,
// with it in front (the page comes on screen when another one was).
function filesShowTab(tab) {
  if (!toolInFilesPage(tab) || toolTabs.indexOf(tab) < 0) return;
  var again = filesTabShown(tab);
  filesActiveId = tab.id; filesPicking = false;
  filesPickerNote('');
  // The app knows this window holds it (a session window asking for it gets
  // this one): reported when it changed (reach_viewer.go reachReportTabs).
  reachReportTabs();
  if (!filesPageShown()) { navigateTo('files'); return; }
  filesRender();
  if (again) toolFocus(tab);
}
// An explorer of this page closing (toolCloseTab, before it leaves the
// list): the one in front goes, its neighbour takes its place (none left:
// the picker).
function filesBeforeClose(tab) {
  if (!toolInFilesPage(tab) || filesActiveId !== tab.id) return;
  var all = filesTabs(), i = all.indexOf(tab);
  var rest = all.filter(function (t) { return t !== tab; });
  var next = rest.length ? rest[Math.min(Math.max(i, 0), rest.length - 1)] : null;
  filesActiveId = next ? next.id : null;
}
// The explorers changed (one opened, closed, disposed, its state): the page follows.
function filesTabsChanged() {
  if (reachDetachedMode || !filesPageView) return;
  filesRender();
}

// ── The device picker ──
// Shown when no explorer is in front (none open, or the last one closed)
// and after "+": the tabs stay above it, none of them active.
function filesPickerView() {
  var v = filesPageView;
  return { tbodyEl: function () { return v && v.pickBody; }, countEl: function () { return v && v.pickCount; }, countLabelEl: function () { return v && v.pickCountLabel; },
    filter: function () { return filesPickerFilter; }, searchFolded: function () { return filesPickerSearchFolded; },
    actions: [['files', '']], toolButtons: filesPickerButton, usable: filesUsable,
    localConns: true, // (the local base's SSH connections too: their SFTP, ui.go localDevGroups)
    refresh: filesPickerFill, open: function (dev) { filesPickerChoose(dev); } };
}
// A row's button (ui.go devViewDeviceRow, view.toolButtons): the explorer,
// in the accent; on when it opens now or is already open here (its tab),
// off with the reason otherwise; its privacy lock (a code will be asked).
function filesPickerButton(dev) {
  var st = toolState(dev, 'files'), tab = filesTabOf(dev);
  if (!st.show && !tab) return '';
  var on = st.enabled || !!tab;
  return '<button class="icon-btn tool-ico primary" title="' + i18nEsc(tab ? T('tools.openFiles') : toolTitle('files', st)) + '" data-act="files"' + (on ? '' : ' disabled') + '>' +
    TOOLS_ICON.folder + (tab ? '' : toolBadge(st)) + '</button>';
}
function filesPickerFill() { if (filesPageView) devViewFill(filesPickerView()); }
// The overview read again (a refresh, a server's devices in), the language:
// the picker on screen follows.
function filesPickerRefreshIfShown() { if (filesPageShown() && filesPickerShown()) filesPickerFill(); }
function filesPickerNote(text) {
  var n = filesPageView && filesPageView.pickNote;
  if (!n) return;
  n.textContent = text || '';
  n.classList.toggle('hide', !text);
}
function filesPickerFocus() {
  var q = filesPageView && filesPageView.pickQ;
  if (q) { try { q.focus(); } catch (e) {} }
}
// "+" (and Esc in the picker): the picker, or back to the explorer in front.
function filesPickerToggle() {
  var a = filesActiveTab();
  filesPicking = !(filesPickerShown() && a);
  filesPickerNote('');
  filesRender();
  if (filesPicking) filesPickerFocus();
}
// A device picked (its row, or its button). Already open here: in front.
// Else its gates (why not, in the note), then toolOpen: the security code
// when privacy mode is on, the tab here ('auto': this page); open in another
// window: that window comes to the front (said).
async function filesPickerChoose(dev) {
  if (!dev) return 'none';
  var t = filesTabOf(dev);
  if (t) { filesShowTab(t); return 'self'; }
  var st = toolState(dev, 'files');
  if (!st.show || !st.enabled) {
    filesPickerNote(T('files.page.unavailable', { device: reachDevName(dev), reason: st.reason || T('tools.reason.agentOffline') }));
    return 'blocked';
  }
  filesPickerNote('');
  var r = await toolOpen(dev, 'files', 'auto', { notify: filesPickerNote });
  if (r === 'focused' || r === 'opening') filesPickerNote(T('files.page.elsewhere', { device: reachDevName(dev) }));
  return r;
}

// ── A language switch (ui.go i18nRedrawApp): its texts ──
function filesPageI18n() {
  if (!filesPageView) return;
  i18nApply(filesPageView.root);
  filesTabsChanged();
  filesEditPillRender();
  filesPickerRefreshIfShown(); // (its rows: states, tooltips, the count's word)
}

// ── The session toolbar's Files button (ui.go renderRemoteTab) ──
// The explorer of the session's device, next to Chat, where the device
// header is not (focus mode, a session window): in the side panel of this
// window by default, the one of Scripts and Info, one at a time
// (files_dock.go filesSessionOpen: Settings > Remote sessions), else as a
// tab (the main window's Files page, or a session window of its own with
// "Detach by default"; a tab of a session window). A mosaic on screen: a
// tab. Pressed while the panel shows it; a click then closes the panel (the
// explorer kept). Shown when its server lists the agent's tools; off with the
// reason, its privacy lock, as the device page's button.
// Drawn again with the header once the device's unlocks are read
// (agent_tools.go toolsPageOpened -> toolsRefreshShown: toolsShown.session),
// its lock badge and tooltip as the header's (the toolbar is built before).
// own (a terminal's tool bar): no such refresh (the device page's header
// keeps its own).
function toolsSessionFilesButton(toolbar, dev, own) {
  if (!toolbar || !toolState(dev, 'files').show) return null;
  var b = document.createElement('button');
  b.className = 'btn-sm reach-ico-btn reach-side-btn reach-files-btn'; b.id = 'reach-files-btn';
  var fill = function () {
    var st = toolState(dev, 'files');
    b.innerHTML = TOOLS_ICON.folder + '<span>' + TH('tools.files') + '</span>' + toolBadge(st);
    b.title = toolTitle('files', st);
    b.disabled = !st.enabled;
  };
  fill();
  b.addEventListener('click', function () { if (!b.disabled) filesSessionOpen(dev); });
  toolbar.appendChild(b);
  if (typeof filesDockButtons === 'function') filesDockButtons(b); // (pressed while its panel is open)
  if (own) return b;
  if (toolsShown.dev !== dev) toolsShown = { dev: dev, header: null, pane: null, session: null };
  toolsShown.session = function () { if (b.isConnected !== false) fill(); };
  return b;
}
`
