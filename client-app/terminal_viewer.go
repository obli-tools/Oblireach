package main

// terminal_viewer.go — the tool tabs of the Desktop and the remote terminals.
//
// A device's terminals (CMD / PowerShell on Windows; "SSH" on Linux and macOS:
// the Obliance agent's login shell in a PTY, root / SYSTEM, no credentials:
// exactly what the web's header buttons and its shell panel open) and its file
// explorer (explorer_viewer.go) open in TOOL TABS, next to the ObliReach
// session tabs. sessionTabs stays ObliReach-only (its code is untouched);
// toolTabs holds the tools; activeSessionTabId names either ('tab_N' or
// 'tool_N'): while a tool tab is on screen reachActiveTab() is null and every
// ObliReach function (toolbar, keepalive, clipboard) goes idle by itself.
//
//   - One tab per (device, tool), in one window: its key is the device's key
//     and "~<kind>" (detached_windows.go toolKey), so a device may hold its
//     ObliReach tab and a CMD, a PowerShell and a Files tab at once. Asking
//     for it again (a row, the "+" list, the device page) brings it to the
//     front wherever it is (this window, a session window, the main one).
//   - Main window, "Detach by default": a terminal opens in a session window
//     of its own (reachOpenWindow with its kind; the window boots on it,
//     ?...&m=<kind>); otherwise here. A session window: here (reachOpenTab).
//   - The file explorer of the main window lives in its Files page
//     (files_page.go: toolInFilesPage), never in the session tab strip nor
//     in a window of its own; a session window keeps its explorer as a tab.
//   - A tool tab dragged out of a session window moves (toolTearOff): a
//     terminal keeps its shell: the local proxy parks its tunnel (the
//     upstream socket stays open, its output kept) and the page of the other
//     window takes it over with a one-time park token (term_park.go:
//     /local/term-park, /local/term-attach/); the file explorer reopens
//     there on the same folder. A terminal that is not connected stays.
//
// The terminal (the web's protocol, GlobalShellPanel): POST /api/remote/
// sessions {deviceId, protocol} on the device's server and tenant (devApi
// pinned: the server's gates, capability 'remote', restriction
// remote.session_start with its 2FA step-up, the legacy refusal, the audit),
// then the relay's WebSocket through the device's proxy (/sp/<profile>, never
// /spt/): text frames {type:'waiting'|'paired'|'error'} from the relay, binary
// frames of the PTY; keystrokes go as BINARY frames (the agent drops text
// frames that are not {type:'resize'}). Closing the tab ends the session
// (POST /end, then the socket closes: remote.session_ended is audited).
// Reconnect is a new session (no resume exists), in the same terminal. Files
// dropped on a terminal go to the shell's folder, through the explorer's
// gated path (term_drop.go).
//
// A LOCAL SSH connection (localconn_tools.go) is the same terminal, with
// another transport (TERM_TRANSPORTS: how the tunnel is obtained, nothing
// else): the app dials it (reachSshConnect) and relays its shell itself on
// /local/ssh-tunnel/<token> with the relay's protocol (ssh_relay.go), so it
// opens in a session window with "Detach by default", tears off, merges,
// parks and is taken over exactly like an agent's.
//
// Tabs of any kind are grouped into MOSAICS by Ctrl+click (tab_groups.go);
// each terminal wears a theme, its type's or its own (term_themes.go).
//
// xterm.js 5.3.0 and its fit addon (vendored, MIT: xterm_assets.go) load on
// first use from /local/. Both consts are injected verbatim after the agent's
// tools (buildUI, not through Sprintf: '%' is safe): no backtick here.

const termCSS = `
/* ── Tool tabs, terminals (terminal_viewer.go) ── */
.tool-pane{flex:1;min-height:0;display:flex;flex-direction:column;position:relative}
.tool-bar{flex-wrap:nowrap}
.tool-bar .dot{flex-shrink:0}
.tool-kind{flex-shrink:0;padding:3px 8px;border-radius:7px;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);font-size:11px;font-weight:700;letter-spacing:.3px;display:inline-flex;align-items:center;gap:5px}
.tool-status{flex:1;min-width:0;font-size:11px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.tool-acts{display:flex;align-items:center;gap:6px;flex-shrink:0}
.tool-body{flex:1;min-height:0;position:relative;overflow:hidden;background:var(--term-bg)}
.tool-host{position:absolute;top:0;right:0;bottom:0;left:0}
.tool-host.term-host{top:8px;right:4px;bottom:6px;left:10px}
.tool-host .xterm{height:100%}
.tool-overlay{position:absolute;top:0;right:0;bottom:0;left:0;z-index:6;display:none;align-items:center;justify-content:center;padding:20px;background:var(--scrim)}
.tool-overlay.visible{display:flex}
.tool-overlay.dock{top:auto;align-items:flex-end;padding:14px;background:none;pointer-events:none}
.tool-overlay.dock .reach-overlay-box{pointer-events:auto;max-width:560px;box-shadow:0 8px 30px var(--shadow)}
.tool-overlay.error .reach-overlay-title{color:var(--danger)}
.session-tab .st-kind{flex-shrink:0;padding:1px 5px;border-radius:5px;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);font-size:9.5px;font-weight:700;letter-spacing:.3px;display:inline-flex;align-items:center;gap:4px}
.wt-tab .wt-tab-kind{flex-shrink:0;padding:1px 5px;border-radius:5px;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l);font-size:9.5px;font-weight:700;letter-spacing:.3px;display:inline-flex;align-items:center;gap:4px}
`

const termJS = `
// ── Tool tabs (terminal_viewer.go) ──────────────────────────────────────────
var toolTabs = [];   // [{ id: 'tool_N', kind, device, key, label, status, statusText, overlay, box, host, ... }]
var toolTabSeq = 0;
// The implementation of each tool kind: { create(tab), start(tab), render(tab, ui),
// dispose(tab, { end }), focus(tab) }. The terminals are termImpl (below);
// the file explorer registers TOOL_IMPL.files (explorer_viewer.go).
var TOOL_IMPL = {};
// Hooks run once for each server socket (chatSocketFor): the file explorer's
// FILE_EXPLORER_RESULT handler. hook(sock, pid).
var toolSocketHooks = [];

// ── Keys ──
// A tool tab's key: its device's key and "~<kind>" (detached_windows.go toolKey).
function toolKeyOf(dev, kind) { return devKey(dev) + '~' + kind; }
function toolKeyKind(k) { k = String(k === undefined || k === null ? '' : k); var i = k.lastIndexOf('~'); return i >= 0 ? k.slice(i + 1) : ''; }
function toolKeyBase(k) { k = String(k === undefined || k === null ? '' : k); var i = k.lastIndexOf('~'); return i >= 0 ? k.slice(0, i) : k; }
function toolTabByKey(k) {
  k = String(k || '');
  for (var i = 0; i < toolTabs.length; i++) if (toolTabs[i].key === k) return toolTabs[i];
  // (an explorer in a session's side panel: files_dock.go)
  return typeof filesDockByKey === 'function' ? filesDockByKey(k) : null;
}
function toolTabById(id) {
  for (var i = 0; i < toolTabs.length; i++) if (toolTabs[i].id === id) return toolTabs[i];
  return null;
}
// The tool tab on screen (null: none, or an ObliReach tab).
function toolActiveTab() { return (typeof activeSessionTabId !== 'undefined' && activeSessionTabId) ? toolTabById(activeSessionTabId) : null; }
// Tab switching shortcut (tabShortcutStep below): next / previous tab of THIS
// window (ObliReach sessions, then terminals), wrapping around; a mosaic is
// one of them (tab_groups.go): stepping into it shows its first member (its
// last one backwards), and while it is on screen each of its members comes
// in turn before the next tab of the strip. Caught in the
// capture phase, before the session canvas or xterm: the shortcut's key press
// and its release never reach the remote machine (Ctrl, Alt and Shift do,
// press and release, so nothing stays held there).
// Chosen over Ctrl+Tab, which the operator may mean for the remote.
var tabKeyEaten = {};
function tabShortcutOrder(step) {
  var out = [], cur = tgShown();
  tgStripItems().forEach(function (it) {
    if (!it.group) { out.push(it.tab); return; }
    var ms = tgMembers(it.group);
    if (it.group === cur) ms.forEach(function (t) { out.push(t); });
    else if (ms.length) out.push(step < 0 ? ms[ms.length - 1] : ms[0]);
  });
  return out;
}
function tabShortcutGo(step) {
  var list = tabShortcutOrder(step);
  if (!list.length) return;
  var cur = -1;
  for (var i = 0; i < list.length; i++) if (list[i].id === activeSessionTabId) { cur = i; break; }
  var next = list[cur < 0 ? 0 : (cur + step + list.length) % list.length];
  if (!next || (cur >= 0 && next.id === activeSessionTabId)) return;
  if (reachDetachedMode && typeof reachWinShowTab === 'function') reachWinShowTab(next);
  else if (typeof switchSessionTab === 'function') switchSessionTab(next.id);
}
// Main keys (one hand): Ctrl+Alt+< = next, Ctrl+Alt+Shift+<
// (i.e. ">") = previous — the physical key next to the left Shift on ISO
// keyboards (AZERTY, QWERTZ, UK...), matched by code so the layout does not
// matter. Ctrl+Alt+PageDown / PageUp stay as the fallback for US keyboards,
// which have no such key.
function tabShortcutStep(e) {
  if (!e || !e.ctrlKey || !e.altKey || e.metaKey) return 0;
  if (e.code === 'IntlBackslash') return e.shiftKey ? -1 : 1;
  if (e.shiftKey) return 0;
  if (e.code === 'PageDown') return 1;
  if (e.code === 'PageUp') return -1;
  return 0;
}
window.addEventListener('keydown', function (e) {
  var step = tabShortcutStep(e);
  if (!step) return;
  if (e.preventDefault) e.preventDefault();
  if (e.stopImmediatePropagation) e.stopImmediatePropagation(); else if (e.stopPropagation) e.stopPropagation();
  tabKeyEaten[e.code] = true;
  tabShortcutGo(step);
}, true);
window.addEventListener('keyup', function (e) {
  if (!e || !tabKeyEaten[e.code]) return;
  delete tabKeyEaten[e.code];
  if (e.preventDefault) e.preventDefault();
  if (e.stopImmediatePropagation) e.stopImmediatePropagation(); else if (e.stopPropagation) e.stopPropagation();
}, true);
// Every tab of this window (a session window closes with its last one).
function winTabCount() { return (typeof sessionTabs !== 'undefined' ? sessionTabs.length : 0) + toolTabs.length; }
// "HOST · CMD": a tool tab's title (window title, tooltips).
function toolTabTitle(tab) { return String((tab && tab.label) || '') + ' · ' + T(toolLabelKey(tab && tab.kind)); }
function toolTabDot(tab) {
  var s = tab && tab.status;
  return s === 'live' ? 'online' : (s === 'starting' || s === 'connecting') ? 'warn' : 'offline';
}

// A text kept as a string, or as a catalog key { k, p } (said again in the
// language on screen at each drawing; a parameter may be one too).
function toolTxt(x) {
  if (x === null || x === undefined) return '';
  if (typeof x === 'string') return x;
  if (typeof x === 'object' && typeof x.k === 'string') {
    if (!x.p || typeof x.p !== 'object') return T(x.k);
    var p = {};
    for (var n in x.p) {
      if (!Object.prototype.hasOwnProperty.call(x.p, n)) continue;
      var v = x.p[n];
      p[n] = (v && typeof v === 'object') ? toolTxt(v) : v;
    }
    return T(x.k, p);
  }
  return String(x);
}
function toolSay(text) {
  if (reachDetachedMode && typeof devPickerIsOpen === 'function' && devPickerIsOpen() && typeof devPickerNote === 'function') { devPickerNote(text); return; }
  reachNotify(text, 'warn');
}
// Where a tool's session calls go once it exists (its /end, its record):
// pinned like its start (multi-server mode: the device's tenant), and by the
// device's server name in single mode too (/proxy follows the active server,
// which a switch changes while the call is on its way).
function toolPinnedBase(dev) {
  var b = devBase(dev, true), pid = devPid(dev);
  return (b === '/proxy' && pid) ? '/sp/' + pid : b;
}

// ── Opening ──
// toolOpen (agent_tools.go) calls it once the device's gates and its privacy
// code passed. where: 'auto' (the main window: a session window of its own
// with "Detach by default", else here; the explorer always here, in the
// Files page), 'here' (this window), 'boot' (a session window opened for
// it). Resolves once the tab exists (its session
// goes on connecting): 'self' (already open here: shown), 'here' (a new tab
// here), 'focused' / 'main' / 'opening' (elsewhere: brought to the front, or
// a window on its way), 'none' (no such tool in this build), 'error'.
// o (optional, 'boot'): { park, path } of a tab moved here (toolTearOff).
async function toolTabOpen(dev, kind, where, o) {
  if (!dev || !toolsIsKind(kind) || !TOOL_IMPL[kind]) return 'none';
  var key = toolKeyOf(dev, kind);
  var mine = toolTabByKey(key);
  if (mine) { toolShowTab(mine); return 'self'; }
  if (where === 'boot') return toolCreateTab(dev, kind, key, o);
  // (a local connection, localconn_tools.go, opens like any device: the
  // same windows, the same "Detach by default", the same tabs)
  if (!reachDetachedMode) {
    // This tool of this device already open in a session window: that one.
    var shown = false;
    try { shown = (await reachNative('reachFocusWindow', key)) === true; } catch (e) { shown = false; }
    mine = toolTabByKey(key);
    if (mine) { toolShowTab(mine); return 'self'; }
    if (shown) return 'focused';
    // ("Detach by default": the explorer too, in a session window of its own)
    if (where === 'auto' && reachDetachOn()) return toolOpenDetached(dev, kind);
    return toolCreateTab(dev, kind, key);
  }
  // A session window: where the app says this tool is (every window reports its tabs).
  if (typeof window.reachOpenTab === 'function' && reachNativeToken()) {
    var at = '';
    try { at = String(await reachNative('reachOpenTab', key)); }
    catch (e) { toolSay(T('viewer.pickOpenFailed', { error: reachErrText(e) })); return 'error'; }
    mine = toolTabByKey(key);
    if (mine) { toolShowTab(mine); return 'self'; }
    if (at !== 'here' && at !== 'self') return at; // (brought to the front where it is)
  }
  return toolCreateTab(dev, kind, key);
}
// Main window, "Detach by default": its own session window (the app creates
// it, keyed by device and tool; one already open comes to the front).
// path (optional): the explorer's folder (a Files page tab detached).
async function toolOpenDetached(dev, kind, path) {
  var title = String(dev.hostname || dev.id) + ' · ' + T(toolLabelKey(kind));
  var opts = { session: -1, viewOnly: false, profile: devPid(dev), kind: kind };
  if (kind === 'files' && typeof path === 'string' && path) opts.path = path.slice(0, 4096);
  var r = '';
  try { r = String(await reachNative('reachOpenWindow', String(dev.id), title, opts)); }
  catch (e) { reachNotify(T('viewer.windowOpenFailed', { error: reachErrText(e) }), 'warn'); return 'error'; }
  if (typeof reachLaunchRecent === 'function') reachLaunchRecent(dev);
  return r === 'focused' ? 'focused' : 'opening';
}
// o (optional): a tab moved from another window (toolTearOff): { park } a
// terminal's parked tunnel to take over, { path } the explorer's folder.
// A tool tab's object, its DOM, its tool created (not listed anywhere yet;
// null: it failed, said). extra: fields set before the tool is created (an
// explorer of a side panel: { docked: true }, files_dock.go).
function toolNewTab(dev, kind, key, extra) {
  var impl = TOOL_IMPL[kind];
  if (!impl) return null;
  var tab = { id: 'tool_' + (++toolTabSeq), kind: kind, device: dev, key: key,
    label: String(dev.hostname || T('viewer.deviceN', { id: dev.id })), status: 'starting', statusText: null, overlay: null, pane: null };
  if (extra && typeof extra === 'object') Object.keys(extra).forEach(function (n) { tab[n] = extra[n]; });
  // Its DOM lives as long as the tab: moved into each new page, never rebuilt.
  tab.box = document.createElement('div'); tab.box.className = 'tool-body';
  tab.host = document.createElement('div'); tab.host.className = 'tool-host';
  tab.ovEl = document.createElement('div'); tab.ovEl.className = 'tool-overlay';
  tab.box.appendChild(tab.host); tab.box.appendChild(tab.ovEl);
  try { if (typeof impl.create === 'function') impl.create(tab); }
  catch (e) { reachNotify(T('term.startFailed', { error: reachErrText(e) }), 'warn'); return null; }
  return tab;
}
function toolCreateTab(dev, kind, key, o) {
  var impl = TOOL_IMPL[kind];
  if (!impl) return 'none';
  var tab = toolNewTab(dev, kind, key);
  if (!tab) return 'error';
  if (o && typeof o === 'object') {
    if (termIsTab(tab) && typeof o.park === 'string' && /^[0-9a-f]{64}$/.test(o.park)) tab.parkToken = o.park;
    if (kind === 'rdp' && typeof o.park === 'string' && /^[0-9a-f]{64}$/.test(o.park)) tab.rdpPark = o.park; // (its session moved here: rdp_view.go)
    if (kind === 'files' && tab.ex && typeof o.path === 'string' && o.path) tab.ex.path = o.path.slice(0, 4096);
    if (kind === 'files' && o.subs) tab.xvInit = o.subs; // (its views: explorer_tabs.go, opened by its start)
  }
  toolTabs.push(tab);
  tgAdoptCheck(tab); // (one of a mosaic moving here: in it again, tab_groups.go)
  if (typeof reachWinClaims !== 'undefined') reachWinClaims.delete(key); // (claimed for this window: its tab now)
  if (typeof reachLaunchRecent === 'function' && !reachDetachedMode) reachLaunchRecent(dev);
  toolShowTab(tab);
  if (reachDetachedMode && typeof chatSyncSockets === 'function') chatSyncSockets(); // (its server's socket)
  try {
    var p = typeof impl.start === 'function' ? impl.start(tab) : null;
    if (p && typeof p.catch === 'function') p.catch(function () {});
  } catch (e) {}
  // A session (not an explorer) that came from another window with its
  // explorer's side panel: the panel here too (files_dock.go).
  if (kind !== 'files' && typeof filesDockAdoptSoon === 'function') filesDockAdoptSoon(dev);
  return 'here';
}

// ── On screen ──
function toolShowTab(tab) {
  if (tab && tab.docked) { if (typeof filesDockReveal === 'function') filesDockReveal(tab, true); return; } // (a session's side panel: files_dock.go)
  if (!tab || toolTabs.indexOf(tab) < 0) return;
  // (a member of a mosaic: its mosaic on screen, focused on it; any other
  // tab: shown alone, the mosaics left as they are: tab_groups.go)
  tgNoteShown(tab);
  // The main window's explorer: its Files page, with it in front.
  if (toolInFilesPage(tab)) { filesShowTab(tab); return; }
  activeSessionTabId = tab.id;
  selectedDevice = tab.device;
  // (no ObliReach session on screen: the globals point at none)
  remoteWs = null; remoteDecoder = null;
  renderSessionTabs();
  openDeviceDetail(tab.device, 'remote');
  reachUpdateClipboardWatch();
}
// Its pane is the one on screen (its box in the page's current tool pane):
// the device page's, or the Files page's (files_page.go).
function toolShown(tab) {
  if (tab && tab.docked) return typeof filesDockShown === 'function' && filesDockShown(tab); // (files_dock.go)
  if (!tab || !tab.pane || !tab.box || tab.box.parentElement !== tab.pane) return false;
  if (toolInFilesPage(tab)) return filesTabShown(tab);
  var onDevicePage = typeof currentRoute !== 'undefined' && currentRoute === 'device-detail';
  // A mosaic: every member is on screen at once, whatever device each is on.
  var g = tgShown();
  if (g && g.members.indexOf(tab.id) >= 0) return onDevicePage;
  return tab.id === activeSessionTabId && onDevicePage && !!selectedDevice && devSame(selectedDevice, tab.device);
}
function toolFocus(tab) {
  var impl = tab && TOOL_IMPL[tab.kind];
  if (impl && typeof impl.focus === 'function') { try { impl.focus(tab); } catch (e) {} }
}
// renderRemoteTab (ui.go), a tool tab of the device on screen: a slim
// toolbar (its state, the tool's actions), then its box (moved, not rebuilt).
function toolRenderPane(tc, tab) {
  var impl = TOOL_IMPL[tab.kind] || {};
  // The main window's focus mode belongs to the ObliReach picture (no way
  // out of it here); a session window always shows its title bar.
  if (!reachDetachedMode && document.body && document.body.classList) document.body.classList.remove('reach-focus');
  remoteWs = null; remoteDecoder = null;
  var pane = document.createElement('div'); pane.className = 'tool-pane'; pane.id = 'tool-pane';
  var bar = document.createElement('div'); bar.className = 'remote-toolbar tool-bar'; bar.id = 'tool-bar';
  var name = document.createElement('span'); name.className = 'tb-devname'; name.title = tab.label;
  var nm = document.createElement('span'); nm.textContent = tab.label; name.appendChild(nm);
  var dot = document.createElement('span'); dot.className = 'dot ' + toolTabDot(tab); dot.id = 'tool-dot';
  var chip = document.createElement('span'); chip.className = 'tool-kind'; chip.id = 'tool-kind'; chip.textContent = T(toolNameKey(tab.kind));
  if (termIsTab(tab)) termThemeContext(chip, tab); // (its theme: tab_groups.go's menu, term_themes.go)
  var st = document.createElement('span'); st.className = 'tool-status'; st.id = 'tool-status'; st.textContent = toolTxt(tab.statusText); if (tab.degraded) st.title = T('term.degradedTip');
  var acts = document.createElement('div'); acts.className = 'tool-acts'; acts.id = 'tool-acts';
  bar.appendChild(dot); bar.appendChild(name); bar.appendChild(chip); bar.appendChild(st); bar.appendChild(acts);
  // A session window hides the device header: its Scripts and Info here (a
  // local connection: what it is, localconn_tools.go, no server to ask).
  if (reachDetachedMode && devIsLocal(tab.device) && typeof cxnBarMeta === 'function') cxnBarMeta(bar, tab.device);
  else if (reachDetachedMode && typeof reachBuildSideTools === 'function') {
    var sep = document.createElement('div'); sep.className = 'toolbar-sep'; bar.appendChild(sep);
    reachBuildSideTools(bar);
  }
  // A terminal is a session too: its device's explorer (files_page.go: the
  // side panel by default, files_dock.go).
  if (termIsTab(tab) && typeof toolsSessionFilesButton === 'function') toolsSessionFilesButton(bar, tab.device, true);
  pane.appendChild(bar);
  pane.appendChild(tab.box);
  tc.appendChild(pane);
  tab.pane = pane;
  try { if (typeof impl.render === 'function') impl.render(tab, { pane: pane, bar: bar, actions: acts, status: st }); } catch (e) {}
  // An explorer of the Files page: in a session window of its own (files_page.go).
  if (toolInFilesPage(tab) && typeof filesDetachButton === 'function') filesDetachButton(acts, tab);
  toolRenderOverlay(tab);
}
// status: 'starting' | 'connecting' | 'live' | 'ended' | 'error'; text: a
// string or { k, p }. The toolbar and the tab strips follow (the Files
// page's for the main window's explorers).
function toolSetStatus(tab, status, text) {
  // An explorer of a session's side panel: the panel's head (files_dock.go).
  if (tab && tab.docked) {
    tab.status = status; tab.statusText = text || null;
    if (typeof filesDockShown === 'function' && filesDockShown(tab)) filesDockRender();
    return;
  }
  // A degraded shell (pipe mode) keeps saying so in its live status.
  if (text && typeof text === 'object' && text.k === 'term.degradedStatus') text = text.p && text.p.status;
  if (tab.degraded && status === 'live' && text) text = { k: 'term.degradedStatus', p: { status: text } };
  tab.status = status; tab.statusText = text || null;
  var inPage = toolInFilesPage(tab);
  if (toolShown(tab)) {
    var s = document.getElementById('tool-status');
    if (s) { s.textContent = toolTxt(tab.statusText); s.title = tab.degraded ? T('term.degradedTip') : ''; }
    var d = document.getElementById('tool-dot'); if (d) d.className = 'dot ' + toolTabDot(tab);
    // (the slim title bar of a session window follows the tool on screen;
    // the Files page is not a session: the main window's bar is not its)
    if (!inPage && tab.id === activeSessionTabId && typeof reachWinSetTitle === 'function') reachWinSetTitle(tab.device);
  }
  if (toolTabs.indexOf(tab) < 0) return;
  if (inPage) { filesTabsChanged(); return; }
  // A mosaic's cell: its own dot (the strips and the sub-tabs follow below).
  tgMemberStatus(tab);
  renderSessionTabs();
}
// A notice over the tool: { kind: 'busy' | 'error' | 'info', title, text,
// acts: [{ label, run, primary }] } (texts: strings or { k, p }); null: none.
// busy covers the tool; the others sit at its bottom (its output stays
// readable and selectable).
function toolSetOverlay(tab, o) { tab.overlay = o || null; toolRenderOverlay(tab); }
function toolRenderOverlay(tab) {
  var el = tab && tab.ovEl;
  if (!el) return;
  var o = tab.overlay;
  el.innerHTML = '';
  el.className = 'tool-overlay' + (o ? ' visible ' + (o.kind || 'info') + (o.kind === 'busy' ? '' : ' dock') : '');
  if (!o) return;
  var box = document.createElement('div'); box.className = 'reach-overlay-box';
  box.setAttribute('role', o.kind === 'error' ? 'alert' : 'status');
  if (o.kind === 'busy') { var sp = document.createElement('div'); sp.className = 'reach-spinner'; box.appendChild(sp); }
  var t = document.createElement('div'); t.className = 'reach-overlay-title'; t.textContent = toolTxt(o.title); box.appendChild(t);
  var tx = toolTxt(o.text);
  if (tx) { var p = document.createElement('div'); p.className = 'reach-overlay-text'; p.textContent = tx; box.appendChild(p); }
  if (o.acts && o.acts.length) {
    var row = document.createElement('div'); row.className = 'reach-overlay-actions';
    o.acts.forEach(function (a) {
      var b = document.createElement('button'); b.className = 'btn-sm' + (a.primary ? ' reach-btn-primary' : '');
      b.textContent = toolTxt(a.label);
      if (a.id) b.id = a.id;
      b.addEventListener('click', function () { a.run(); });
      row.appendChild(b);
    });
    box.appendChild(row);
  }
  el.appendChild(box);
}

// ── Closing ──
// o (optional): { moved: true }: the tab went to another window
// (toolTearOff): its session goes on there, nothing is ended here.
function toolCloseTab(tab, o) {
  if (tab && tab.docked) { if (typeof filesDockClose === 'function') filesDockClose(tab, o); return; } // (files_dock.go)
  var idx = toolTabs.indexOf(tab);
  if (idx < 0) return;
  var moved = !!(o && o.moved);
  // A terminal whose dropped files are on their way: asked first (term_drop.go).
  if (!moved && typeof termDropCloseGuard === 'function' && termDropCloseGuard(tab)) return;
  var wasActive = activeSessionTabId === tab.id;
  var grp = tgOf(tab.id), grpShown = !!grp && tgShown() === grp;
  var inPage = toolInFilesPage(tab);
  if (inPage) filesBeforeClose(tab); // (its neighbour in front of the Files page)
  // Its neighbour in the session tab strip (the Files page's explorers are
  // not there: closing a terminal never jumps to them).
  var strip = function () { return toolTabs.filter(function (t) { return !toolInFilesPage(t); }); };
  var stripIdx = strip().indexOf(tab);
  var impl = TOOL_IMPL[tab.kind];
  try { if (impl && typeof impl.dispose === 'function') impl.dispose(tab, { end: !moved }); } catch (e) {}
  toolTabs.splice(idx, 1);
  // Its mosaic, if any (tab_groups.go): without it (one tab left: that tab).
  var grpNext = tgOnGone(tab.id);
  // A session window closes with its last tab.
  if (reachDetachedMode && winTabCount() === 0) {
    activeSessionTabId = null;
    renderSessionTabs();
    reachCloseOwnWindow();
    return;
  }
  // The main window's explorer: its Files page follows (the session tab
  // strip never showed it; renderSessionTabs reports this window's tabs).
  if (inPage) { renderSessionTabs(); filesTabsChanged(); return; }
  // A member of the mosaic on screen: the mosaic goes on (or its last tab).
  if (grpShown && (grpNext || !wasActive)) {
    if (wasActive) tgShowTab(grpNext); else tgRerender();
    if (reachDetachedMode && typeof chatSyncSockets === 'function') chatSyncSockets();
    return;
  }
  if (wasActive) {
    var rest = strip();
    var next = rest[Math.min(Math.max(stripIdx, 0), rest.length - 1)] || null;
    if (next) toolShowTab(next);
    else if (sessionTabs.length) switchSessionTab(sessionTabs[sessionTabs.length - 1].id);
    else {
      activeSessionTabId = null;
      if (typeof currentRoute !== 'undefined' && currentRoute === 'device-detail' && selectedDevice && devSame(selectedDevice, tab.device)) openDeviceDetail(tab.device, 'remote');
    }
  }
  renderSessionTabs();
  if (reachDetachedMode && typeof chatSyncSockets === 'function') chatSyncSockets();
}
// Sign-out, a server left, a tenant left: those tool tabs end (their
// sessions with them). Returns how many.
function toolDisposeWhere(pred) {
  // (the devices' transfers and files being edited too: explorer_viewer.go)
  if (typeof explorerCtxDisposeWhere === 'function') explorerCtxDisposeWhere(pred);
  // (the explorers of the sessions' side panels: files_dock.go)
  var docks = typeof filesDockDisposeWhere === 'function' ? filesDockDisposeWhere(pred) : 0;
  var gone = toolTabs.filter(function (t) { try { return !!pred(t); } catch (e) { return false; } });
  if (!gone.length) return docks;
  var act = toolActiveTab(), wasShown = !!act && gone.indexOf(act) >= 0 && toolShown(act);
  gone.forEach(function (t) {
    var impl = TOOL_IMPL[t.kind];
    try { if (impl && typeof impl.dispose === 'function') impl.dispose(t, { end: true }); } catch (e) {}
    var i = toolTabs.indexOf(t); if (i >= 0) toolTabs.splice(i, 1);
    if (activeSessionTabId === t.id) activeSessionTabId = null;
  });
  tgPrune(); // (their mosaics without them)
  renderSessionTabs();
  filesTabsChanged(); // (the Files page: without them; the picker when the one in front went)
  // Its pane was on screen and the app goes on (a tenant left...): the
  // device's page without it (a sign-out redraws everything itself).
  if (wasShown && typeof appReady !== 'undefined' && appReady && selectedDevice) openDeviceDetail(selectedDevice, 'remote');
  return gone.length + docks;
}
// Every tool tab ends. Resolves once their sessions' ends reached the
// server (3 s at most): a sign-out waits for it (its request would make them
// unauthenticated).
function toolDisposeAll() {
  toolDisposeWhere(function () { return true; });
  return toolEndsWait();
}
var toolEnding = new Set(); // the POST /end on their way
function toolEndsWait(ms) {
  if (!toolEnding.size) return Promise.resolve();
  var all = Promise.all(Array.from(toolEnding).map(function (p) { return p.then(function () {}, function () {}); }));
  return Promise.race([all, new Promise(function (r) { setTimeout(r, ms || 3000); })]);
}

// ── Header button 'Reach' (agent_tools.go toolsHeaderGroup) ──
// The device's ObliReach session tab, or its Reach pane (Connect).
function toolShowReach(dev) {
  if (!dev) return;
  var rt = reachTabOfDevice(devKey(dev));
  if (rt) { switchSessionTab(rt.id); return; }
  if (toolActiveTab()) activeSessionTabId = null;
  selectedDevice = dev;
  renderSessionTabs();
  openDeviceDetail(dev, 'remote');
}

// ── Tab strips ──
// A tool tab of the main window's strip (renderSessionTabs, ui.go: in its
// place among the ObliReach tabs and the mosaics, tab_groups.go; the
// explorers are the Files page's tabs: files_page.go). Ctrl+click: mosaics.
function toolMainTabEl(tab) {
  var multi = typeof serverMode !== 'undefined' && serverMode === 'multi';
  var el = document.createElement('div');
  el.className = 'session-tab tool-tab' + (tab.id === activeSessionTabId ? ' active' : '');
  el.dataset.tabId = tab.id;
  el.title = toolTabTitle(tab);
  if (multi && tab.device) {
    var sd = document.createElement('span'); sd.className = 'st-srv'; sd.style.background = srvColor(devPid(tab.device)); sd.title = srvName(devPid(tab.device));
    el.appendChild(sd);
  }
  var dot = document.createElement('span'); dot.className = 'dot ' + toolTabDot(tab); dot.style.width = '6px'; dot.style.height = '6px';
  var kc = document.createElement('span'); kc.className = 'st-kind'; kc.textContent = T(toolLabelKey(tab.kind));
  var nm = document.createElement('span'); nm.textContent = tab.label;
  var x = document.createElement('span'); x.className = 'tab-close'; x.title = T('tools.closeTab', { name: toolTabTitle(tab) }); x.textContent = '×';
  x.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); toolCloseTab(tab); });
  el.appendChild(dot); el.appendChild(kc); el.appendChild(nm); el.appendChild(x);
  el.addEventListener('click', function (e) { if (e && (e.ctrlKey || e.metaKey)) tgCtrlClick(tab); else toolShowTab(tab); });
  el.addEventListener('auxclick', function (e) { if (e && e.button === 1) { if (e.preventDefault) e.preventDefault(); toolCloseTab(tab); } });
  if (termIsTab(tab)) termThemeContext(el, tab);
  return el;
}
// A tool tab of a session window's strip (reachRenderWinTabs): the same
// gestures as its ObliReach tabs (a click shows it, Ctrl+click: mosaics, a
// drag scrolls the strip or tears it off).
function toolWinTabEl(tab, srvDots) {
  var on = tab.id === activeSessionTabId;
  var el = document.createElement('div');
  el.className = 'wt-tab tool-tab' + (on ? ' active' : '');
  el.setAttribute('role', 'tab');
  el.setAttribute('aria-selected', on ? 'true' : 'false');
  el.title = toolTabTitle(tab);
  el.dataset.tabId = tab.id;
  if (srvDots && typeof srvName === 'function') {
    var pid = devPid(tab.device), sn = srvName(pid);
    var sd = document.createElement('span'); sd.className = 'wt-tab-srv'; sd.style.background = srvColor(pid);
    if (sn) { sd.title = sn; el.title = sn + ' / ' + el.title; }
    el.appendChild(sd);
  }
  var dot = document.createElement('span'); dot.className = 'dot ' + toolTabDot(tab);
  var kc = document.createElement('span'); kc.className = 'wt-tab-kind'; kc.textContent = T(toolLabelKey(tab.kind));
  var nm = document.createElement('span'); nm.className = 'wt-tab-name'; nm.textContent = tab.label;
  el.appendChild(dot); el.appendChild(kc); el.appendChild(nm);
  var x = document.createElement('button');
  x.className = 'wt-tab-x'; x.tabIndex = -1; x.title = T('tools.closeTab', { name: toolTabTitle(tab) });
  x.innerHTML = '<svg width="9" height="9" viewBox="0 0 10 10"><path d="M1 1l8 8M9 1l-8 8" fill="none" stroke="currentColor" stroke-width="1.6"/></svg>';
  x.addEventListener('pointerdown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); }); // not a tab drag
  x.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); });
  x.addEventListener('click', function (e) { if (e && e.stopPropagation) e.stopPropagation(); toolCloseTab(tab); });
  el.appendChild(x);
  reachWireWinTab(el, tab);
  reachWinTabs.els[tab.id] = el;
  if (termIsTab(tab)) termThemeContext(el, tab);
  return el;
}

// ── Each server's socket (chatSocketFor, ui.go): the session updates ──
// REMOTE_SESSION_UPDATED (the whole session, in this user's room) tells why
// a terminal session ended before its tunnel was attached (a failed start:
// the agent refused, privacy...), which the tunnel cannot say then.
function toolsWireSocket(sock, pid) {
  if (!sock || sock._toolsWired) return;
  sock._toolsWired = true;
  pid = pid || '';
  try { sock.on('REMOTE_SESSION_UPDATED', function (s) { termOnSessionUpdate(pid, s); }); } catch (e) {}
  for (var i = 0; i < toolSocketHooks.length; i++) { try { toolSocketHooks[i](sock, pid); } catch (e) {} }
}

// ═════════════════════════════════════════════════════════════════════════
// Terminals: CMD / PowerShell / SSH (the Obliance agent's PTY)
// ═════════════════════════════════════════════════════════════════════════
var TERM_CONNECT_MS = 30000;  // live delivery: the agent pairs within 30 s (the web's)
var TERM_QUEUED_MS = 300000;  // queued (agent not connected): the queue's 5 minutes
var TERM_END_WAIT_MS = 3000;  // a closing tunnel waits this long for its POST /end
var TERM_REASON = { session_closed: 'term.reason.sessionClosed', agent_timeout: 'term.reason.agentTimeout', agent_offline: 'term.reason.agentOffline',
  conpty_unsupported: 'tools.reason.conpty' }; // (the Obliance agent: no ConPTY on this Windows)

// ── xterm.js, loaded once (xterm_assets.go) ──
var toolsXtermPromise = null;
var toolsXtermCss = false;
function toolsXtermReady() { return typeof window.Terminal === 'function' && !!window.FitAddon && typeof window.FitAddon.FitAddon === 'function'; }
function toolsLoadXterm() {
  if (toolsXtermReady()) return Promise.resolve(true);
  if (toolsXtermPromise) return toolsXtermPromise;
  toolsXtermPromise = new Promise(function (resolve, reject) {
    var fail = function () { toolsXtermPromise = null; reject(new Error('xterm')); };
    try {
      if (!toolsXtermCss) {
        toolsXtermCss = true;
        var l = document.createElement('link'); l.rel = 'stylesheet'; l.href = '/local/xterm.css';
        document.head.appendChild(l);
      }
      var load = function (src, next) {
        var s = document.createElement('script'); s.src = src;
        s.onload = next; s.onerror = fail;
        document.head.appendChild(s);
      };
      load('/local/xterm.js', function () {
        load('/local/xterm-addon-fit.js', function () { if (toolsXtermReady()) resolve(true); else fail(); });
      });
    } catch (e) { fail(); }
  });
  return toolsXtermPromise;
}
// ── The tab ──
var termImpl = {
  create: function (tab) {
    tab.host.className = 'tool-host term-host';
    tab.term = null; tab.fit = null; tab.opened = false;
    tab.gen = 0; tab.ws = null; tab.sessionId = null; tab.delivery = 'live';
    tab.live = false; tab.error = null; tab.userEnded = false; tab.everStarted = false; tab.privacyRetried = false;
    tab.endReason = null; tab.serverEnded = false;
    // (files dropped on it go to the shell's folder: term_drop.go)
    if (typeof termCwdReset === 'function') termCwdReset(tab);
    if (typeof termDropWire === 'function') termDropWire(tab);
    // Its theme (its type's until one of its own is picked: term_themes.go),
    // and its menu on a right click inside it: Copy / Paste only (the themes:
    // right click on its tab, or its palette button).
    tab.themeKey = null;
    termApplyTabTheme(tab);
    tab.host.addEventListener('contextmenu', function (e) {
      if (e && e.preventDefault) e.preventDefault();
      termThemeMenuOpen({ x: (e && e.clientX) || 0, y: (e && e.clientY) || 0 }, tab, { clip: true, clipOnly: true });
    });
  },
  start: function (tab) { return tab.parkToken ? termResume(tab) : termStart(tab); },
  render: function (tab, ui) { termRender(tab, ui); },
  dispose: function (tab, o) { termDispose(tab, o); },
  focus: function (tab) { termFocus(tab); },
};
TOOL_IMPL.cmd = termImpl;
TOOL_IMPL.powershell = termImpl;
TOOL_IMPL.ssh = termImpl;
function termIsTab(tab) { return !!tab && TOOL_IMPL[tab.kind] === termImpl; }

// Its xterm (once the library is there): keys, output, size.
function termEnsure(tab) {
  if (tab.term) return true;
  if (!toolsXtermReady()) return false;
  try {
    var term = new window.Terminal({ fontFamily: '"Cascadia Mono","Cascadia Code",Consolas,"Courier New",monospace', fontSize: 14,
      cursorBlink: true, convertEol: false, scrollback: 5000, theme: termThemeOf(tab) }); // (its theme: term_themes.go)
    var fit = new window.FitAddon.FitAddon();
    term.loadAddon(fit);
    tab.term = term; tab.fit = fit;
    if (typeof termCwdWire === 'function') termCwdWire(tab, term); // (the shell's folder reports: term_drop.go)
    term.onData(function (d) { termHandleInput(tab, d); });
    if (typeof term.onBinary === 'function') term.onBinary(function (d) { termSendBinary(tab, d); });
    term.onResize(function () { termSendResize(tab); });
    term.attachCustomKeyEventHandler(function (e) { return termKey(tab, e); });
    termWatchTheme();
    return true;
  } catch (e) { tab.term = null; tab.fit = null; return false; }
}
// xterm draws into its host once, when the host is in the page (a terminal
// opened on a detached element measures nothing); then moves with it.
function termAttach(tab) {
  if (!tab.term || tab.opened || !toolShown(tab) || tab.host.isConnected === false) return;
  try { tab.term.open(tab.host); tab.opened = true; } catch (e) {}
}
function termRender(tab, ui) {
  var mk = function (id, key, tip, fn) {
    var b = document.createElement('button'); b.className = 'btn-sm'; b.id = id;
    b.textContent = T(key); b.title = T(tip || key);
    b.addEventListener('mousedown', function (e) { if (e && e.preventDefault) e.preventDefault(); }); // (the keyboard stays in the terminal)
    b.addEventListener('click', fn);
    ui.actions.appendChild(b);
    return b;
  };
  mk('term-copy', 'term.copy', 'term.copyTip', function () { termCopy(tab, true); });
  mk('term-paste', 'term.paste', 'term.pasteTip', function () { termPaste(tab); });
  mk('term-clear', 'common.clear', null, function () { if (tab.term) { try { tab.term.clear(); } catch (e) {} } termFocus(tab); });
  var rc = mk('term-reconnect', 'term.reconnect', null, function () { termReconnect(tab); });
  rc.disabled = !(tab.status === 'ended' || tab.status === 'error');
  ui.actions.appendChild(termThemeButton(tab, 'term-theme'));
  termApplyTabTheme(tab);
  termAttach(tab);
  termObserveResize(tab);
  // (the keyboard: the tab in focus only — a mosaic draws several)
  termFitSoon(tab, tab.id === activeSessionTabId);
}
function termFocus(tab) {
  if (tab && tab.term && tab.opened) { try { tab.term.focus(); } catch (e) {} }
}
// The terminal takes the size of its pane (the PTY follows: onResize).
function termObserveResize(tab) {
  if (tab.ro) { try { tab.ro.disconnect(); } catch (e) {} tab.ro = null; }
  if (typeof ResizeObserver !== 'function') return; // (the window's resize event, below)
  try { tab.ro = new ResizeObserver(function () { termFitSoon(tab, false); }); tab.ro.observe(tab.box); } catch (e) { tab.ro = null; }
}
function termFitSoon(tab, focus) {
  if (focus) tab.fitFocus = true;
  if (tab.fitPending) return;
  tab.fitPending = true;
  var go = function () {
    tab.fitPending = false;
    var f = tab.fitFocus; tab.fitFocus = false;
    termFit(tab);
    if (f && toolShown(tab)) termFocus(tab);
  };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(go); else setTimeout(go, 0);
}
function termFit(tab) {
  if (!tab.term || !tab.fit || !toolShown(tab)) return;
  termAttach(tab);
  if (!tab.opened) return;
  // Hidden or collapsed (0 x 0): never a 2 x 1 terminal (the shell would wrap every line).
  var w = tab.host.clientWidth, h = tab.host.clientHeight;
  if (typeof w === 'number' && typeof h === 'number' && (w < 40 || h < 20)) return;
  try { tab.fit.fit(); } catch (e) {}
}
if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
  window.addEventListener('resize', function () { var t = toolActiveTab(); if (termIsTab(t)) termFitSoon(t, false); });
  // The page goes (a window closing, a reload): the live sessions end with
  // it, best effort (the request outlives the page).
  window.addEventListener('pagehide', function () {
    toolTabs.forEach(function (t) { if (termIsTab(t) && t.sessionId && t.ws) termEndSession(t.device, t.sessionId); });
  });
}

// ── The session ──
function termStatusText(tab) {
  var tr = termTransport(tab);
  if (tr && tr.waitKey) return { k: tr.waitKey };
  return { k: tab.delivery === 'queued' ? 'term.queued' : 'term.waiting' };
}
function termClampDim(n, def) { n = Math.floor(Number(n)); if (!(n >= 1)) return def; return n > 65535 ? 65535 : n; }
function termClearTimer(tab) { if (tab.connectTimer) { clearTimeout(tab.connectTimer); tab.connectTimer = null; } }

// ── Transports: how a terminal gets its tunnel ──
// Whatever the device, a terminal is ONE WebSocket speaking the relay's
// protocol (text {type:'waiting'|'paired'|'error'}, binary output; binary
// keystrokes, {type:'resize'}), relayed by the local proxy (term_park.go:
// it can be parked and taken over in another window). Only how that tunnel
// is obtained differs:
//   - agent: an Obliance device's PTY, POST /api/remote/sessions on its
//     server (its gates), then the relay's /api/remote/tunnel/<token>;
//   - local: a local SSH connection (localconn_tools.go), dialed by the app
//     (reachSshConnect: its host key decided over the terminal, the vault's
//     secret never in the page), then /local/ssh-tunnel/<token> (ssh_relay.go).
// open(tab, gen, o) resolves to { path, sessionId, token, delivery }, or
// null when it did not get one (it said why itself, or the tab moved on).
// waitKey: the status while the tunnel pairs (the agent's: its delivery).
var TERM_TRANSPORTS = {};
function termTransport(tab) { return TERM_TRANSPORTS[tab && devIsLocal(tab.device) ? 'local' : 'agent']; }
TERM_TRANSPORTS.agent = {
  open: async function (tab, gen) {
    var dev = tab.device, r = null, d = null;
    try {
      // The device's server, on its tenant (multi-server mode); never a
      // Windows session: CMD / PowerShell as SYSTEM, like the web.
      r = await devApi(dev, 'POST', '/api/remote/sessions', { deviceId: dev.id, protocol: tab.kind }, true);
      try { d = await r.json(); } catch (e) { d = null; }
    } catch (e) {
      if (gen !== tab.gen) return null;
      termFail(tab, { k: 'term.startFailed', p: { error: reachErrText(e) } }, true);
      return null;
    }
    var s = d && d.data;
    if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) {
      // Closed (or restarted) meanwhile: that session ends at once.
      if (r && r.ok && s && s.id) termEndSession(dev, String(s.id));
      return null;
    }
    if (!r.ok) {
      if (r.status === 401 && d && d.twoFactorRequired) { termFail(tab, { k: 'viewer.tfaRequired' }, true); return null; }
      // (Windows older than build 17763: no pseudo console for CMD / PowerShell)
      if (r.status === 409 && d && d.code === 'conpty_unsupported') { termFail(tab, { k: 'tools.reason.conpty' }, true); return null; }
      termFail(tab, { k: 'term.startFailed', p: { error: (d && typeof d.error === 'string' && d.error) || ('HTTP ' + r.status) } }, true);
      return null;
    }
    // restricted: an approval can never open a remote session (the server's
    // approvals refuse open_*_session).
    if (r.status === 202 || (s && s.status === 'pending_approval')) { termFail(tab, { k: 'term.pendingApproval' }, true); return null; }
    var token = (s && typeof s.sessionToken === 'string') ? s.sessionToken : '';
    if (!/^[0-9a-f]{64}$/.test(token)) {
      if (s && s.id) termEndSession(dev, String(s.id));
      termFail(tab, { k: 'term.startFailed', p: { error: { k: 'viewer.noToken' } } }, true);
      return null;
    }
    // (the token belongs to the device's server: its tunnel goes there, /sp/, never /spt/)
    return { path: devBase(dev, false) + '/api/remote/tunnel/' + token, sessionId: String(s.id || ''), token: token,
      delivery: s.delivery === 'queued' ? 'queued' : 'live' };
  },
};

// A new session for this tab (its first, or a reconnect: no resume exists).
// o (optional): the transport's options (a local connection: { trust }).
async function termStart(tab, o) {
  if (toolTabs.indexOf(tab) < 0) return;
  var gen = tab.gen = (tab.gen || 0) + 1;
  termClearTimer(tab);
  if (tab.ws) { var old = tab.ws; tab.ws = null; try { old.close(); } catch (e) {} }
  tab.sessionId = null; tab.live = false; tab.error = null; tab.userEnded = false; tab.endReason = null; tab.serverEnded = false;
  tab.delivery = 'live';
  tab.everStarted = true;
  if (typeof termCwdReset === 'function') termCwdReset(tab); // (a new shell: its folder not known yet)
  toolSetStatus(tab, 'starting', { k: 'term.starting' });
  toolSetOverlay(tab, { kind: 'busy', title: { k: 'term.starting' }, text: '' });
  var loaded = await toolsLoadXterm().then(function () { return true; }, function () { return false; });
  if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return;
  if (!loaded || !termEnsure(tab)) { termFail(tab, { k: 'term.loadFailed' }, false); return; }
  termAttach(tab);
  termFitSoon(tab, false);
  var t = null;
  try { t = await termTransport(tab).open(tab, gen, o || {}); }
  catch (e) { t = null; if (gen === tab.gen && toolTabs.indexOf(tab) >= 0) termFail(tab, { k: 'term.startFailed', p: { error: reachErrText(e) } }, true); }
  if (!t || gen !== tab.gen || toolTabs.indexOf(tab) < 0) return;
  tab.sessionId = t.sessionId || null;
  tab.token = t.token; // (its tunnel, to park it when the tab moves: toolTearOff)
  tab.delivery = t.delivery === 'queued' ? 'queued' : 'live';
  var ws;
  try {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + t.path);
    ws.binaryType = 'arraybuffer';
  } catch (e) {
    if (tab.sessionId) termEndSession(tab.device, tab.sessionId);
    termFail(tab, { k: 'term.wsFailed' }, true);
    return;
  }
  tab.ws = ws;
  toolSetStatus(tab, 'connecting', termStatusText(tab));
  toolSetOverlay(tab, { kind: 'busy', title: termStatusText(tab), text: '' });
  var waitMs = tab.delivery === 'queued' ? TERM_QUEUED_MS : TERM_CONNECT_MS;
  tab.connectTimer = setTimeout(function () { tab.connectTimer = null; if (tab.ws === ws && !tab.live) termNoAnswer(tab); }, waitMs);
  ws.onopen = function () { if (tab.ws === ws) termSendResize(tab); };
  ws.onmessage = function (ev) { if (tab.ws === ws) termOnMessage(tab, ev); };
  ws.onerror = function () {};
  ws.onclose = function (ev) { if (tab.ws !== ws) return; if (termMovingClose(tab, ev)) return; tab.ws = null; termOnClose(tab, ev); };
}
// The agent answered (the relay paired it, or its first output): live.
function termMarkLive(tab) {
  if (tab.live) return;
  tab.live = true;
  termClearTimer(tab);
  toolSetOverlay(tab, null);
  toolSetStatus(tab, 'live', { k: 'term.connected' });
  termRefreshButtons(tab);
  if (toolShown(tab)) termFocus(tab);
}
function termOnMessage(tab, ev) {
  var data = ev ? ev.data : null;
  if (typeof data === 'string') {
    var msg = null;
    if (data.charAt(0) === '{') { try { msg = JSON.parse(data); } catch (e) { msg = null; } }
    if (msg && typeof msg === 'object' && typeof msg.type === 'string') {
      if (msg.type === 'paired') { termMarkLive(tab); termSendResize(tab); return; }
      if (msg.type === 'waiting') {
        if (!tab.live) { toolSetStatus(tab, 'connecting', termStatusText(tab)); toolSetOverlay(tab, { kind: 'busy', title: termStatusText(tab), text: '' }); }
        return;
      }
      if (msg.type === 'error') { termOnError(tab, msg); return; }
      // (the local proxy, first on a terminal taken over: term_park.go)
      if (msg.type === 'resume') { if (tab.resuming) termOnResume(tab, msg); return; }
      // The agent's shell backing: "pipe" = old Windows without ConPTY
      // (no full-screen programs, no live resize): shown next to the status.
      if (msg.type === 'mode') {
        var deg = msg.pty === 'pipe';
        if (deg !== !!tab.degraded) { tab.degraded = deg; toolSetStatus(tab, tab.status, tab.statusText); }
        return;
      }
      return; // (another control frame: nothing to show)
    }
    termMarkLive(tab);
    termWrite(tab, data);
    return;
  }
  if (data && typeof data === 'object' && typeof data.byteLength === 'number') {
    termMarkLive(tab);
    termWrite(tab, new Uint8Array(data));
  }
}
// (each piece keeps its arrival: the shell's folder reports are judged by it, term_drop.go)
function termWrite(tab, data) {
  if (!tab.term) return;
  var parsed = typeof termRxTrack === 'function' ? termRxTrack(tab) : undefined;
  try { tab.term.write(data, parsed); } catch (e) { if (parsed) parsed(); }
}
// Broadcast (a mosaic's Broadcast button, tab_groups.go): a keystroke typed
// into one of its terminals goes to every terminal of that mosaic (never to
// another mosaic's). Off, or a tab in none: unchanged.
function termHandleInput(tab, d) {
  var all = tgBroadcastTargets(tab);
  if (all && all.length) { all.forEach(function (t) { termSendData(t, d); }); return; }
  termSendData(tab, d);
}
function termSendData(tab, d) {
  var ws = tab.ws;
  if (!ws || ws.readyState !== 1 || typeof d !== 'string' || !d) return;
  if (typeof termNoteInput === 'function') termNoteInput(tab, d); // (an Enter: the folder no longer sure, term_drop.go)
  // Always BINARY: the agent reads text frames as control frames only.
  try { ws.send(new TextEncoder().encode(d)); } catch (e) {}
}
function termSendBinary(tab, d) {
  var ws = tab.ws;
  if (!ws || ws.readyState !== 1 || typeof d !== 'string' || !d) return;
  var b = new Uint8Array(d.length);
  for (var i = 0; i < d.length; i++) b[i] = d.charCodeAt(i) & 255;
  try { ws.send(b); } catch (e) {}
}
function termSendResize(tab) {
  var ws = tab.ws, t = tab.term;
  if (!ws || ws.readyState !== 1 || !t) return false;
  try { ws.send(JSON.stringify({ type: 'resize', cols: termClampDim(t.cols, 80), rows: termClampDim(t.rows, 24) })); return true; } catch (e) { return false; }
}
// The relay or the agent said why the session stops (the socket closes next).
function termOnError(tab, msg) {
  termClearTimer(tab);
  var code = (typeof msg.code === 'string' && /^[a-z0-9_]{1,40}$/.test(msg.code)) ? msg.code : '';
  var text = typeof msg.message === 'string' ? msg.message.slice(0, 500) : '';
  tab.error = { code: code, message: text };
  if (privacyIsDenial(text)) { termPrivacyDenied(tab); return; }
  termShowEnded(tab, termReasonText(code, text), true);
}
function termReasonText(code, text) {
  if (TERM_REASON[code]) return { k: TERM_REASON[code] };
  if (text) return text; // (the agent's own words: why its shell could not start)
  if (code === 'agent_error') return { k: 'term.reason.agentError' };
  return code || { k: 'term.wsFailed' };
}
// A session's end reason as the server recorded it (remote_sessions.end_reason).
function termCleanReason(r) {
  r = String(r || '');
  var m = /^command_failure(?::\s*([\s\S]*))?$/.exec(r); // (the server's 'command_failure[: <agent text>]')
  if (m) return m[1] ? m[1] : { k: 'term.reason.agentError' };
  if (/^agent_error(:|$)/.test(r)) return { k: 'term.reason.agentError' };
  if (r === 'agent_offline') return { k: 'term.reason.agentOffline' };
  if (r === 'timeout') return { k: 'term.reason.agentTimeout' };
  if (r === 'user_disconnect' || r === 'server_restart') return { k: 'term.reason.sessionClosed' };
  return r;
}
function termActs(tab) {
  return [
    { label: { k: 'term.reconnect' }, primary: true, id: 'term-ov-reconnect', run: function () { termReconnect(tab); } },
    { label: { k: 'common.close' }, id: 'term-ov-close', run: function () { toolCloseTab(tab); } },
  ];
}
// Ended (reason: why, or null: it simply ended) or failed. A reason of the
// app ({ k }) is its own sentence; the agent's or the server's words are
// quoted ("The session ended: ...").
function termShowEnded(tab, reason, isError) {
  termClearTimer(tab);
  tab.live = false;
  var title = !reason ? { k: 'term.ended' } : (typeof reason === 'object' ? reason : { k: 'term.endedBy', p: { reason: String(reason) } });
  toolSetStatus(tab, isError ? 'error' : 'ended', { k: 'term.ended' });
  toolSetOverlay(tab, { kind: isError ? 'error' : 'info', title: title, text: '', acts: termActs(tab) });
  termRefreshButtons(tab);
}
// It could not start (never a tunnel): the reason, [Reconnect] [Close].
function termFail(tab, text, isError) {
  termClearTimer(tab);
  tab.error = tab.error || { code: 'start_failed', message: '' };
  tab.live = false;
  toolSetStatus(tab, 'error', { k: 'term.ended' });
  toolSetOverlay(tab, { kind: 'error', title: text, text: '', acts: termActs(tab) });
  termRefreshButtons(tab);
}
function termRefreshButtons(tab) {
  if (!toolShown(tab)) return;
  var b = document.getElementById('term-reconnect');
  if (b) b.disabled = !(tab.status === 'ended' || tab.status === 'error');
}
// No 'paired' nor output in time: the session ends (POST /end), and says so.
function termNoAnswer(tab) {
  tab.error = { code: 'agent_timeout', message: '' };
  termStopSession(tab, true);
  termShowEnded(tab, { k: 'term.agentNoAnswer' }, true);
}
function termOnClose(tab, ev) {
  termClearTimer(tab);
  if (tab.userEnded) return;
  if (tab.error) { termRefreshButtons(tab); return; } // (its reason is on screen)
  if (tab.live) { termShowEnded(tab, null, false); return; }
  // Never paired: the socket was refused (the session already failed: the
  // agent refused it, privacy...). Why: the server's socket said it, else
  // the session's record.
  var gen = tab.gen;
  toolSetStatus(tab, 'error', { k: 'term.ended' });
  termFindEndReason(tab).then(function (why) {
    if (gen !== tab.gen || toolTabs.indexOf(tab) < 0 || tab.userEnded) return;
    if (why && privacyIsDenial(why)) { tab.error = { code: 'agent_error', message: String(why) }; termPrivacyDenied(tab); return; }
    tab.error = { code: 'closed', message: why ? String(why) : '' };
    termShowEnded(tab, why ? termCleanReason(why) : { k: 'term.wsFailed' }, true);
  });
}
async function termFindEndReason(tab) {
  if (tab.endReason) return tab.endReason;
  var id = tab.sessionId, dev = tab.device;
  if (!id || !dev || devIsLocal(dev)) return '';
  try {
    var r = await devApi(dev, 'GET', '/api/remote/sessions?deviceId=' + encodeURIComponent(String(dev.id)), undefined, true);
    if (!r || !r.ok) return tab.endReason || '';
    var d = await r.json();
    var items = (d && d.data && Array.isArray(d.data.items)) ? d.data.items : [];
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      if (it && String(it.id) === id && typeof it.endReason === 'string' && it.endReason) return it.endReason.slice(0, 300);
    }
  } catch (e) {}
  return tab.endReason || '';
}
// REMOTE_SESSION_UPDATED of a session of one of these tabs (its server's socket).
function termOnSessionUpdate(pid, s) {
  if (!s || typeof s !== 'object' || !s.id) return;
  var id = String(s.id);
  toolTabs.forEach(function (tab) {
    if (tab.sessionId !== id || devPid(tab.device) !== (pid || '')) return;
    if (typeof s.endReason === 'string' && s.endReason) tab.endReason = s.endReason.slice(0, 300);
    if (['closed', 'failed', 'expired', 'timeout'].indexOf(s.status) >= 0) tab.serverEnded = true;
  });
}

// ── Privacy mode: the agent refused (its unlock is gone: a restart, the
// time is over, privacy just turned on) ──
// The code is asked again at once (the operator is waiting for this
// terminal), then ONE new session; a second refusal stays on screen with
// [Unlock and retry] [Close].
function termPrivacyDenied(tab) {
  termClearTimer(tab);
  tab.live = false;
  toolSetStatus(tab, 'error', { k: 'term.ended' });
  toolSetOverlay(tab, { kind: 'error', title: { k: 'privacy.denied' }, text: '', acts: [
    { label: { k: 'privacy.retry' }, primary: true, id: 'term-ov-unlock', run: function () { termPrivacyRetry(tab); } },
    { label: { k: 'common.close' }, id: 'term-ov-close', run: function () { toolCloseTab(tab); } },
  ] });
  termRefreshButtons(tab);
  if (!tab.privacyRetried) termPrivacyRetry(tab);
}
async function termPrivacyRetry(tab) {
  if (toolTabs.indexOf(tab) < 0 || tab.privacyAsking) return;
  tab.privacyRetried = true;
  tab.privacyAsking = true;
  var fresh = null;
  try { fresh = await privacyRetryGate(tab.device, 'remote'); } finally { tab.privacyAsking = false; }
  if (!fresh || toolTabs.indexOf(tab) < 0) return; // (cancelled: [Unlock and retry] stays)
  if (selectedDevice === tab.device) selectedDevice = fresh;
  tab.device = fresh;
  termRestart(tab);
}

// ── Reconnect, close ──
// A new session (no resume exists), in the same terminal (its output kept).
function termRestart(tab) {
  if (tab.term && tab.opened) termWrite(tab, '\r\n\u001b[90m' + T('term.restartNote') + '\u001b[0m\r\n');
  termStart(tab);
}
async function termReconnect(tab) {
  if (toolTabs.indexOf(tab) < 0 || tab.reconnecting) return;
  tab.reconnecting = true;
  try {
    termStopSession(tab, true);
    // Privacy mode may have come (or its unlock gone) since: its code first
    // (a local connection has none: privacyEnsure says so).
    if (!(await privacyEnsure(tab.device, 'remote'))) return;
    if (toolTabs.indexOf(tab) < 0) return;
    tab.privacyRetried = false;
    termRestart(tab);
  } finally { tab.reconnecting = false; }
}
// Its session stops: an open one ends first (POST /end, so the server
// audits remote.session_ended), then its tunnel closes (3 s at most later).
function termStopSession(tab, end) {
  tab.gen = (tab.gen || 0) + 1; // (a start on its way is abandoned)
  termClearTimer(tab);
  var ws = tab.ws, sid = tab.sessionId;
  tab.ws = null; tab.live = false;
  if (!ws) return;
  var closed = false;
  var close = function () { if (closed) return; closed = true; try { ws.close(); } catch (e) {} };
  if (end && sid && !tab.serverEnded) {
    var t = setTimeout(close, TERM_END_WAIT_MS);
    termEndSession(tab.device, sid).then(function () { clearTimeout(t); close(); });
  } else close();
}
// POST /api/remote/sessions/:id/end (204; errors ignored): outlives the page
// (keepalive), pinned like the start. Resolves to true once ended.
function termEndSession(dev, id) {
  id = String(id || '');
  if (!dev || !/^[0-9A-Za-z-]{1,64}$/.test(id)) return Promise.resolve(false);
  var p;
  try {
    p = fetch(toolPinnedBase(dev) + '/api/remote/sessions/' + encodeURIComponent(id) + '/end',
      { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', keepalive: true });
  } catch (e) { return Promise.resolve(false); }
  var e = Promise.resolve(p).then(function (r) { return !!(r && (r.ok || r.status === 204)); }, function () { return false; });
  toolEnding.add(e);
  e.then(function () { toolEnding.delete(e); });
  return e;
}
function termDispose(tab, o) {
  tab.userEnded = true;
  termStopSession(tab, !o || o.end !== false);
  if (tab.ro) { try { tab.ro.disconnect(); } catch (e) {} tab.ro = null; }
  if (tab.term) { try { tab.term.dispose(); } catch (e) {} tab.term = null; tab.fit = null; }
}

// ── Moving to another window (toolTearOff, term_park.go) ──
// A tool tab dragged out of a session window (reachTearOffTab): the app
// opens it elsewhere (a new window at the drop point, or the window whose
// title bar it was dropped on) and it goes from here without ending. A
// terminal first parks its tunnel in the local proxy: the other window's
// page takes it over (the same shell, its recent output replayed); if the
// move fails, this page takes it back itself. The explorer reopens there on
// its folder. Resolves like reachTearOffTab: 'opening' / 'moved', or false.
var TERM_PARK_RE = /^[0-9a-f]{64}$/;
async function toolTearOff(tab, how) {
  var merge = !!(how && how.merge); // (this window's only tab joining another window)
  // into: a mosaic moving whole (tab_groups.go tgTearOff), the window its
  // first tab went to (that tab's key) — the last of them may empty this one.
  var into = (how && typeof how.into === 'string') ? how.into : '';
  // A tab's "Move to window" (tab_move.go): { window } a session window by
  // its id, { main } the main window (either may take the window's last
  // tab), { newWindow } a new window at the cursor.
  var toWin = (how && how.window > 0) ? how.window : 0, toMain = !!(how && how.main), newWin = !!(how && how.newWindow);
  if (toolTabs.indexOf(tab) < 0 || (!merge && !into && !toWin && !toMain && winTabCount() < 2) || tab.moving) return false;
  var opts = { session: -1, viewOnly: false, profile: devPid(tab.device), kind: tab.kind };
  if (merge) opts.merge = true;
  if (into) opts.into = into;
  if (toWin) opts.window = toWin;
  else if (toMain) opts.main = true;
  else if (newWin) opts.newWindow = true;
  var park = '';
  if (termIsTab(tab)) {
    // Only a live shell moves (an ended one: its Reconnect is here).
    if (!tab.live || !tab.ws || tab.ws.readyState !== 1 || !tab.token) { reachNotify(T('term.cannotMove'), 'warn'); return false; }
    if (typeof termDropBusy === 'function' && termDropBusy(tab)) { reachNotify(T('term.moveBusy'), 'warn'); return false; }
    park = await termPark(tab);
    if (!park) { reachNotify(T('term.moveFailed'), 'warn'); return false; }
    opts.park = park;
  } else if (tab.kind === 'rdp') {
    // A local RDP session moves parked too (rdp_view.go): the same session there.
    if (!tab.connId) { reachNotify(T('connections.rdp.cannotMove'), 'warn'); return false; }
    park = await rdpTabPark(tab);
    if (!park) { reachNotify(T('term.moveFailed'), 'warn'); return false; }
    opts.park = park;
  } else if (tab.kind === 'files' && tab.ex && typeof tab.ex.path === 'string' && tab.ex.path) {
    opts.path = tab.ex.path.slice(0, 4096);
  }
  // A session (not an explorer) takes its device's side panel along, when
  // it was that device's last session here (files_dock.go).
  var dock = tab.kind !== 'files' && typeof filesDockLeaving === 'function' ? filesDockLeaving(tab) : null;
  var r = null;
  try { r = await reachNative('reachTearOff', String(tab.device.id), toolTabTitle(tab), opts); }
  catch (e) { r = null; reachToast(T('viewer.tabMoveFailed', { error: reachErrText(e) }), 'warn'); }
  if (r !== 'opening' && r !== 'moved') {
    if (dock) filesDockStay(tab.device, dock);
    if (park && toolTabs.indexOf(tab) >= 0) {
      if (tab.kind === 'rdp') { tab.rdpPark = park; rdpTabStart(tab); } // (back here: the same RDP session)
      else { tab.parkToken = park; termResume(tab); } // (back here: the same shell)
    }
    return false;
  }
  if (toolTabs.indexOf(tab) >= 0) toolCloseTab(tab, { moved: true });
  return r;
}
// The proxy keeps the tunnel (the page's connection closes, the shell goes
// on): the one-time park token, or '' (not parked: the tab stays as it was).
async function termPark(tab) {
  var ws = tab.ws;
  tab.moving = true; tab.movingClosed = false;
  var r = null, d = null;
  try {
    r = await fetch('/local/term-park', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: String(tab.token || ''), sessionId: String(tab.sessionId || ''), theme: String(tab.themeKey || ''), degraded: !!tab.degraded }) });
    d = await r.json();
  } catch (e) { r = null; d = null; }
  var park = (r && r.ok && d && typeof d.park === 'string' && TERM_PARK_RE.test(d.park)) ? d.park : '';
  tab.moving = false;
  if (!park) {
    // Not parked: the tab goes on; its socket, if it ended meanwhile, says so now.
    if (tab.movingClosed && tab.ws === ws) { tab.movingClosed = false; tab.ws = null; termOnClose(tab, {}); }
    return '';
  }
  // Its connection is the proxy's now: let go of it without a word.
  if (tab.ws === ws) tab.ws = null;
  if (ws) { try { ws.onmessage = null; ws.onclose = null; ws.onerror = null; } catch (e) {} }
  termClearTimer(tab);
  tab.live = false;
  return park;
}
// A tab's socket closing while it is being parked: the proxy's doing (or a
// real end, told once the park answered): nothing now.
function termMovingClose(tab) {
  if (!tab.moving) return false;
  tab.movingClosed = true;
  return true;
}
// This tab takes a parked terminal over (a tab moved here, or back here):
// /local/term-attach/<park token>, then the proxy's {type:'resume'}, the
// shell's recent output, and live traffic.
async function termResume(tab) {
  if (toolTabs.indexOf(tab) < 0) return;
  var park = String(tab.parkToken || '');
  tab.parkToken = null;
  var gen = tab.gen = (tab.gen || 0) + 1;
  termClearTimer(tab);
  if (tab.ws) { var old = tab.ws; tab.ws = null; try { old.close(); } catch (e) {} }
  tab.live = false; tab.error = null; tab.userEnded = false; tab.endReason = null; tab.serverEnded = false;
  tab.everStarted = true; tab.moving = false; tab.delivery = 'live';
  toolSetStatus(tab, 'connecting', { k: 'term.resuming' });
  toolSetOverlay(tab, { kind: 'busy', title: { k: 'term.resuming' }, text: '' });
  var loaded = await toolsLoadXterm().then(function () { return true; }, function () { return false; });
  if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return;
  if (!loaded || !termEnsure(tab)) { termFail(tab, { k: 'term.loadFailed' }, false); return; }
  termAttach(tab);
  if (!TERM_PARK_RE.test(park)) { termFail(tab, { k: 'term.moveLost' }, true); return; }
  var ws;
  try {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + '/local/term-attach/' + park);
    ws.binaryType = 'arraybuffer';
  } catch (e) { termFail(tab, { k: 'term.moveLost' }, true); return; }
  tab.ws = ws;
  tab.resuming = true;
  tab.connectTimer = setTimeout(function () {
    tab.connectTimer = null;
    if (tab.ws !== ws || !tab.resuming) return;
    tab.resuming = false; tab.ws = null;
    try { ws.close(); } catch (e) {}
    tab.error = { code: 'move_lost', message: '' };
    termShowEnded(tab, { k: 'term.moveLost' }, true);
  }, TERM_CONNECT_MS);
  ws.onmessage = function (ev) { if (tab.ws === ws) termOnMessage(tab, ev); };
  ws.onerror = function () {};
  ws.onclose = function (ev) {
    if (tab.ws !== ws) return;
    if (termMovingClose(tab, ev)) return;
    tab.ws = null;
    if (tab.resuming) {
      // Never taken over (too late, or already taken): its session is gone.
      tab.resuming = false;
      termClearTimer(tab);
      tab.error = { code: 'move_lost', message: '' };
      termShowEnded(tab, { k: 'term.moveLost' }, true);
      return;
    }
    termOnClose(tab, ev);
  };
}
// The proxy's first frame on a taken-over terminal: the session (its id for
// its end, its token to move it again), the size its output was drawn at,
// the tab's own theme (term_themes.go); then the output comes, and live traffic.
function termOnResume(tab, msg) {
  tab.resuming = false;
  if (typeof msg.sessionId === 'string' && /^[0-9A-Za-z-]{1,64}$/.test(msg.sessionId)) tab.sessionId = msg.sessionId;
  if (typeof msg.token === 'string' && /^[A-Za-z0-9_-]{1,512}$/.test(msg.token)) tab.token = msg.token;
  if (typeof msg.theme === 'string' && termThemeKeyOk(msg.theme) && !tab.themeKey) termSetTabTheme(tab, msg.theme); // (its own theme came with it)
  tab.degraded = msg.degraded === true;
  if (tab.term) {
    // (a terminal taken back here: its screen is redrawn from the output kept)
    try { tab.term.reset(); } catch (e) {}
    var c = termClampDim(msg.cols, 0), r = termClampDim(msg.rows, 0);
    if (c && r) { try { tab.term.resize(c, r); } catch (e) {} }
  }
  termMarkLive(tab);
  termFitSoon(tab, true);
}

// ── Keys and clipboard ──
// Ctrl+C copies a selection (without one it is ^C for the shell); Ctrl+Shift+C
// copies; Ctrl+Shift+V pastes the clipboard (the app's, like the pictures');
// plain Ctrl+V is the browser's paste (xterm reads it). Every other key goes
// to the shell, those of the browser (F5 / Ctrl+R reload, Ctrl+F find,
// Ctrl+P print...) included: xterm cancels the keys it sends, and these are
// cancelled here too, so the page never reloads, finds, prints or zooms.
// Checked in a real WebView2: with xterm alone F5, Ctrl+R, Ctrl+F,
// Ctrl+P, F3 and Ctrl+G reach the shell and nothing else, but Ctrl+Shift+R
// reloads the page (xterm sends nothing for Ctrl+Shift+<letter>, so it does
// not cancel it): every Ctrl+Shift+<letter> is cancelled here.
function termBrowserKey(e) {
  var k = String(e.key || '').toLowerCase(), code = String(e.code || '');
  if (e.altKey || e.metaKey) return false;
  if (!e.ctrlKey) return k === 'f5' || code === 'F5' || k === 'f3' || code === 'F3' || k === 'f7' || code === 'F7';
  if (e.shiftKey && /^Key[A-Z]$/.test(code)) return true; // (reload, the devtools, ...: never the page's)
  if (['Equal', 'Minus', 'Digit0', 'NumpadAdd', 'NumpadSubtract', 'Numpad0'].indexOf(code) >= 0 || ['=', '+', '-', '_', '0'].indexOf(k) >= 0) return true; // (zoom)
  return ['KeyR', 'KeyF', 'KeyP', 'KeyG', 'KeyU', 'KeyS', 'KeyJ', 'KeyH'].indexOf(code) >= 0 || ['r', 'f', 'p', 'g', 'u', 's', 'j', 'h'].indexOf(k) >= 0 ||
    code === 'F5' || k === 'f5';
}
function termKey(tab, e) {
  if (!e || e.type !== 'keydown') return true;
  var k = String(e.key || '').toLowerCase(), code = String(e.code || '');
  var ctrl = !!e.ctrlKey, sh = !!e.shiftKey, plain = !e.altKey && !e.metaKey;
  var isC = code === 'KeyC' || k === 'c', isV = code === 'KeyV' || k === 'v';
  var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
  if (ctrl && sh && plain && isC) { stop(); termCopy(tab, false); return false; }
  if (ctrl && sh && plain && isV) { stop(); termPaste(tab); return false; }
  if (ctrl && !sh && plain && isC && tab.term && typeof tab.term.hasSelection === 'function' && tab.term.hasSelection()) {
    stop(); termCopy(tab, false);
    try { tab.term.clearSelection(); } catch (x) {}
    return false;
  }
  if (ctrl && !sh && plain && isV) return false; // (the paste event: xterm reads it)
  if (termBrowserKey(e) && e.preventDefault) e.preventDefault();
  return true;
}
// all: without a selection, everything the terminal holds (its toolbar's Copy).
async function termCopy(tab, all) {
  var t = tab.term;
  if (!t) return;
  var text = '';
  try { text = t.getSelection() || ''; } catch (e) { text = ''; }
  if (!text && all) {
    try {
      var b = t.buffer.active, lines = [];
      for (var i = 0; i < b.length; i++) { var l = b.getLine(i); lines.push(l ? l.translateToString(true) : ''); }
      text = lines.join('\n').replace(/\s+$/, '');
    } catch (e) { text = ''; }
  }
  if (!text) return;
  var ok = await reachNativeClipboardWrite(text);
  reachNotify(T(ok ? 'term.copied' : 'term.clipboardFailed'), ok ? 'ok' : 'warn');
  if (all) termFocus(tab);
}
async function termPaste(tab) {
  var r = await reachNativeClipboardRead();
  if (r && r.tooLarge) { reachNotify(T('term.pasteTooLarge'), 'warn'); return; }
  if (r && r.failed) { reachNotify(T('term.clipboardFailed'), 'warn'); return; }
  var text = (r && typeof r.text === 'string') ? r.text : '';
  if (!text || !tab.term) return;
  try { tab.term.paste(text); } catch (e) {}
  termFocus(tab);
}
`
