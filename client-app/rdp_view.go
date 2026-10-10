package main

// rdp_view.go — a LOCAL RDP connection (the vault's rdp entries,
// localconn_tools.go) is a TAB like its SSH terminal (TOOL_IMPL.rdp,
// terminal_viewer.go's tool tabs): it opens in a session window of its own
// with "Detach by default" (else a tab of the main window), from the Devices
// list, its page or a session window's "+" list; it is torn off, merged into
// another window and closed like any tab. Backed by rdp_session.go /
// rdp_bridge_windows.go: a real, embedded mstsc.exe window (see
// rdp_session.go's header for why, and for its one unverified risk).
//
// The picture is not DOM: it is a native window placed over the tab's
// placeholder (tab.host). The page keeps it there (rdpSync, a few times a
// second and on every change): shown at the placeholder's rect while the tab
// is on screen (toolShown), hidden otherwise — another tab, another page of
// the main window, a dialog or the "+" list over it (a native window would
// cover them). The process is Per-Monitor DPI aware (dpi_windows.go): the
// native side works in physical client pixels, so the rect's CSS pixels are
// converted with devicePixelRatio (the screen's scale and the page's zoom).
//
// Moving the tab (toolTearOff): reachRdpPark (its picture hidden, held by the
// main window, a one-time token) then, in the new window's page,
// reachRdpAttach (rdp_park.go): the SAME session goes on, as a terminal's
// shell does. It ends when its tab closes, when its window closes, or when
// mstsc.exe exits (the tab then offers Reconnect).
//
// Both consts are injected verbatim after the other tool blocks (ui.go's
// buildUI, not through Sprintf: '%' is safe); no backtick here.

const rdpViewCSS = `
/* ── Local RDP tab (rdp_view.go) ── */
.tool-host.rdp-host{top:0;right:0;bottom:0;left:0}
`

const rdpViewJS = `
// ── Local RDP tab (rdp_view.go) ─────────────────────────────────────────────
var RDP_SYNC_MS = 250;
var rdpTabsByConn = {}; // connId -> its tab (the events of rdp_bridge_windows.go)
// Fire-and-forget push from rdp_bridge_windows.go: the session ended
// (mstsc.exe exited, its connection lost or closed from inside).
window.__reachRdpEvent = function (evt) {
  if (!evt || typeof evt.connId !== 'string') return;
  var tab = rdpTabsByConn[evt.connId];
  if (!tab) return;
  if (evt.type === 'closed') rdpTabEnded(tab, String(evt.reason || ''));
};
// The placeholder's rect in physical client pixels (the CSS pixels times
// devicePixelRatio: the process is Per-Monitor DPI aware), its edges rounded
// so that the picture covers it exactly.
function rdpPixelRatio() {
  var d = typeof window !== 'undefined' ? Number(window.devicePixelRatio) : 1;
  return isFinite(d) && d > 0 ? d : 1;
}
function rdpRectOf(el) {
  var r = el && typeof el.getBoundingClientRect === 'function' ? el.getBoundingClientRect() : null;
  if (!r) return [0, 0, 0, 0];
  var k = rdpPixelRatio(), x = Math.round(r.left * k), y = Math.round(r.top * k);
  return [x, y, Math.round((r.left + r.width) * k) - x, Math.round((r.top + r.height) * k) - y];
}
// Something of the page over the picture: a dialog, the "+" list, a menu.
function rdpCovered() {
  try {
    if (typeof devPickerIsOpen === 'function' && devPickerIsOpen()) return true;
    // (a terminal's theme menu over a mosaic's cells: term_themes.go)
    return !!(document.querySelector && (document.querySelector('.modal-overlay.visible') || document.querySelector('.term-theme-pop')));
  } catch (e) { return false; }
}
function rdpWanted(tab) {
  if (!tab || !tab.connId || toolTabs.indexOf(tab) < 0 || !toolShown(tab) || tab.host.isConnected === false) return false;
  if (typeof document.visibilityState === 'string' && document.visibilityState === 'hidden') return false;
  return !rdpCovered();
}
// Its picture where its tab is (or hidden): told on each change only.
function rdpSync(tab) {
  if (!tab || !tab.connId) return;
  var want = rdpWanted(tab), r = want ? rdpRectOf(tab.host) : [0, 0, 0, 0];
  if (want && (r[2] <= 0 || r[3] <= 0)) want = false;
  var sig = want ? r.join(',') : '-';
  if (sig === tab.rdpSig) return;
  tab.rdpSig = sig;
  vaultSyncCall('reachRdpShow', tab.connId, want, r[0], r[1], r[2], r[3]);
}
function rdpSyncStart(tab) {
  if (tab.rdpTimer) return;
  tab.rdpSig = null;
  tab.rdpTimer = setInterval(function () { rdpSync(tab); }, RDP_SYNC_MS);
  rdpSync(tab);
}
function rdpSyncStop(tab) {
  if (tab.rdpTimer) { clearInterval(tab.rdpTimer); tab.rdpTimer = null; }
}
function rdpForget(tab) {
  if (tab.connId && rdpTabsByConn[tab.connId] === tab) delete rdpTabsByConn[tab.connId];
  tab.connId = null;
  rdpSyncStop(tab);
}
// The session: a new one (the vault's secret used in the app, never here),
// or one moved here (tab.rdpPark: reachRdpAttach).
async function rdpTabStart(tab) {
  if (tab.connId) { vaultSyncCall('reachRdpClose', tab.connId); rdpForget(tab); }
  var gen = tab.gen = (tab.gen || 0) + 1;
  tab.live = false;
  var node = tab.device && tab.device._node;
  toolSetStatus(tab, 'connecting', { k: 'connections.rdp.connecting' });
  toolSetOverlay(tab, { kind: 'busy', title: { k: 'connections.rdp.connecting' }, text: '' });
  rdpTabButtons(tab);
  // (its placeholder laid out first: the picture's first place)
  for (var i = 0; i < 20 && toolTabs.indexOf(tab) >= 0 && toolShown(tab) && rdpRectOf(tab.host)[2] <= 0; i++) await new Promise(function (r) { setTimeout(r, 50); });
  if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return null;
  var r0 = rdpRectOf(tab.host), connId = '';
  if (tab.rdpPark) {
    var park = tab.rdpPark; tab.rdpPark = '';
    var got = await vaultSyncCall('reachRdpAttach', park, r0[0], r0[1], r0[2], r0[3]);
    connId = typeof got === 'string' ? got : '';
    if (!connId) { rdpTabFail(tab, { k: 'connections.rdp.moveLost' }); return null; }
  } else {
    if (!node) { rdpTabFail(tab, { k: 'vault.err.internal' }); return null; }
    var res = null;
    try { res = await vaultAsyncCall('reachRdpConnect', String(node.id), r0[0], r0[1], r0[2], r0[3]); }
    catch (e) {
      if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return null;
      if (e && e.code === 'locked') { rdpTabLocked(tab); return null; }
      rdpTabFail(tab, vaultErrText(e));
      return null;
    }
    connId = (res && typeof res.connId === 'string') ? res.connId : '';
    if (!connId) { rdpTabFail(tab, { k: 'vault.err.internal' }); return null; }
    // (closed or restarted meanwhile: that session ends at once)
    if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) { vaultSyncCall('reachRdpClose', connId); return null; }
  }
  tab.connId = connId; tab.live = true;
  rdpTabsByConn[connId] = tab;
  toolSetOverlay(tab, null);
  toolSetStatus(tab, 'live', { k: 'connections.rdp.connected' });
  rdpTabButtons(tab);
  rdpSyncStart(tab);
  return connId;
}
function rdpTabFail(tab, text) {
  rdpForget(tab);
  tab.live = false;
  toolSetStatus(tab, 'error', text);
  toolSetOverlay(tab, { kind: 'error', title: text, text: '', acts: rdpTabActs(tab) });
  rdpTabButtons(tab);
}
function rdpTabEnded(tab, reason) {
  rdpForget(tab);
  tab.live = false;
  var t = reason ? { k: 'connections.rdp.endedWith', p: { reason: reason } } : { k: 'connections.rdp.ended' };
  toolSetStatus(tab, reason ? 'error' : 'ended', t);
  toolSetOverlay(tab, { kind: reason ? 'error' : 'info', title: t, text: '', acts: rdpTabActs(tab) });
  rdpTabButtons(tab);
}
function rdpTabActs(tab) {
  return [
    { label: { k: 'term.reconnect' }, primary: true, id: 'rdp-ov-reconnect', run: function () { rdpTabStart(tab); } },
    { label: { k: 'common.close' }, id: 'rdp-ov-close', run: function () { toolCloseTab(tab); } },
  ];
}
// The vault is locked (its secret is there): its unlock, then the session.
function rdpTabLocked(tab) {
  rdpForget(tab);
  toolSetStatus(tab, 'error', { k: 'vault.locked.title' });
  toolSetOverlay(tab, { kind: 'error', title: { k: 'vault.locked.title' }, text: '', acts: [
    { label: { k: 'vault.locked.submit' }, primary: true, id: 'rdp-ov-unlock', run: function () { cxnUnlockThen(function () { rdpTabStart(tab); }); } },
    { label: { k: 'common.close' }, id: 'rdp-ov-close', run: function () { toolCloseTab(tab); } },
  ] });
  rdpTabButtons(tab);
}
// Its tool bar: Reconnect while it is not connected.
function rdpTabButtons(tab) {
  var acts = tab && tab.ui && tab.ui.actions;
  if (!acts || !toolShown(tab)) return;
  acts.innerHTML = '';
  if (tab.connId || tab.status === 'connecting') return;
  var b = document.createElement('button'); b.className = 'btn-sm'; b.id = 'rdp-reconnect';
  b.textContent = T('term.reconnect');
  b.addEventListener('click', function () { rdpTabStart(tab); });
  acts.appendChild(b);
}
// Its tab moving to another window (toolTearOff): the session parked, its
// one-time token ('' when it cannot move).
async function rdpTabPark(tab) {
  if (!tab.connId) return '';
  var id = tab.connId;
  rdpSyncStop(tab);
  var t = await vaultSyncCall('reachRdpPark', id);
  if (typeof t !== 'string' || !/^[0-9a-f]{64}$/.test(t)) { rdpSyncStart(tab); return ''; }
  if (rdpTabsByConn[id] === tab) delete rdpTabsByConn[id];
  tab.connId = null; tab.live = false;
  return t;
}
var rdpTabImpl = {
  create: function (tab) {
    tab.host.className = 'tool-host rdp-host';
    tab.connId = null; tab.live = false; tab.gen = 0; tab.rdpSig = null; tab.rdpTimer = null;
  },
  start: function (tab) { return rdpTabStart(tab); },
  render: function (tab, ui) { tab.ui = ui; tab.rdpSig = null; rdpTabButtons(tab); if (tab.connId) rdpSync(tab); },
  dispose: function (tab, o) {
    tab.gen = (tab.gen || 0) + 1;
    var id = tab.connId;
    rdpForget(tab);
    // (moved: its session goes on in the other window)
    if (id && !(o && o.end === false)) vaultSyncCall('reachRdpClose', id);
  },
  focus: function (tab) { if (tab.connId) rdpSync(tab); },
};
TOOL_IMPL.rdp = rdpTabImpl;
`
