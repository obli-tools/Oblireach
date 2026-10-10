package main

// localconn_tools.go — the LOCAL connections (the "Local" base: the vault's
// ssh/rdp entries) are devices of the app like an Obliance server's, with
// the same views and the same tools: the same rows in the Devices and Files
// lists and in a session window's "+" list (ui.go localDevGroups: folders as
// groups), the same terminal tab, the same file explorer; only the
// transport differs.
//
//   - A local connection is a synthetic device (cxnDevice): its server id
//     (_pid) is LOCAL_PID ('__local__', never a server profile's id: those
//     are 16 hex characters), so its key ('__local__:<vault id>') can never
//     be a server device's, and _local marks it. Nothing of it ever goes to
//     an Obliance server: devApi refuses it (ui.go), no audit, no step-up, no
//     privacy code, no Scripts / Chat (its own header, cxnSelectInto). Its
//     favourites and recents are the app's (ui.go favFor / addRecent: the
//     key of LOCAL_PID, this computer's storage, never a server's); its Info
//     panel is its own (localconn_info.go: what it is, and a live view asked
//     over the connection while it is open).
//   - Its terminal is THE terminal (terminal_viewer.go termImpl): the same
//     WebSocket protocol, through the local transport below
//     (TERM_TRANSPORTS.local): reachSshConnect dials it in Go (the host key
//     decided over the terminal, the vault's secret never in the page) and
//     hands a tunnel token; the shell is relayed by the proxy (ssh_relay.go)
//     exactly like an agent's, so the tab opens in a session window with
//     "Detach by default", is torn off, merged, parked and taken over in
//     another window (term_park.go), split, broadcast to, and takes dropped
//     files (term_drop.go: SFTP, its folder asked of the shell, streamed).
//   - Its explorer (TOOL_IMPL.files, explorer_viewer.go) sends the agent's
//     commands to the app's SFTP client (reachSftpCmd: localconn_explorer.go)
//     instead of the server socket (explorerSend). One SFTP session per
//     connection and window, opened on first use, closed with its last
//     explorer there. Files go both ways in chunks (no size limit); a file
//     saved from an explorer without the local pane goes to Downloads by the
//     app itself (sftp_save.go). A server without SFTP: the same explorer
//     over SCP and its shell (scp.go; no shell either: the drop only).
//   - RDP is a tab too (rdp_view.go, TOOL_IMPL.rdp): its own session window
//     with "Detach by default", torn off and merged like a terminal (its
//     session parked meanwhile: rdp_park.go); its picture, a native window,
//     follows its tab (shown at its place, hidden when another tab, page or
//     a dialog is on screen).
//
// Both consts are injected verbatim after the other tool blocks (ui.go's
// buildUI, not through Sprintf: '%' is safe): no backtick here.

const localConnCSS = `
/* ── Local connections in tool tabs (localconn_tools.go) ── */
.cxn-dev-chip{font-size:11px;color:var(--muted);padding:2px 8px;border-radius:999px;background:var(--input-bg);flex-shrink:0}
.cxn-pane{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:14px;color:var(--muted);font-size:13px;padding:24px;text-align:center}
.cxn-pane .acts{display:flex;gap:8px}
.cxn-hk-fp{font-family:monospace;font-size:12.5px;background:var(--input-bg);border-radius:8px;padding:9px 11px;word-break:break-all;margin:10px 0}
.cxn-hk-warn{font-size:12.5px;color:var(--text)}
.cxn-hk-warn.mismatch{color:var(--danger);font-weight:600}
.cxn-bar-meta{flex-shrink:1;min-width:0;font-size:11px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
` + localConnInfoCSS // (its Info panel: localconn_info.go)

const localConnJS = `
// ── Local connections (localconn_tools.go) ──────────────────────────────────
var LOCAL_PID = '__local__';
function devIsLocal(dev) { return !!dev && dev._local === true && dev._pid === LOCAL_PID; }
// The synthetic device of a connection (n: a reachVaultByParent node).
function cxnDevice(n) {
  var node = { id: String(n.id), label: String(n.label || ''), kind: n.kind, host: String(n.host || ''), port: Number(n.port) || 0,
    username: String(n.username || ''), authMethod: n.authMethod, parentId: String(n.parentId || '') };
  var ssh = node.kind === CXN_KIND_SSH;
  return { id: node.id, _pid: LOCAL_PID, _local: true, _node: node, hostname: node.label || node.host,
    // (an SSH server: a Unix-like shell and paths, as far as the app knows)
    osType: ssh ? 'linux' : '', status: 'online', oblireach: { online: false, installed: false } };
}
function cxnIsSsh(dev) { return !!(dev && dev._node && dev._node.kind === CXN_KIND_SSH); }
function cxnTypeName(dev) { return cxnIsSsh(dev) ? 'SSH' : 'RDP'; }
function cxnNodeText(node) {
  if (!node) return '';
  return (node.kind === CXN_KIND_RDP ? 'RDP' : 'SSH') + ' · ' + (node.username ? node.username + '@' : '') + node.host + ':' + node.port;
}
// What a tool offers on a local connection (agent_tools.go toolState):
// SSH: its terminal and its files; RDP: its picture (rdp_view.go).
function cxnToolState(dev, kind) {
  var on = cxnIsSsh(dev) ? (kind === 'ssh' || kind === 'files') : (!!(dev && dev._node) && kind === 'rdp');
  return { show: on, enabled: on, lock: 'none', reason: '', reasonKey: '' };
}
// Its tools, in the order of its header and pane.
var CXN_TOOL_KINDS = ['ssh', 'rdp', 'files'];
function cxnToolIcon(k) { return k === 'files' ? TOOLS_ICON.folder : k === 'rdp' ? TOOLS_ICON.monitor : TOOLS_ICON.terminal; }
// A connection of the vault by its id (a tab moved to this window, a
// window opened for it), as a device, or null.
async function cxnFindDevice(id) {
  var n = null;
  try { n = await vaultSyncCall('reachVaultConnInfo', String(id)); } catch (e) { n = null; }
  return (n && typeof n === 'object' && typeof n.id === 'string') ? cxnDevice(n) : null;
}

// ── Opening (the Devices and Files lists, its page, a "+" list) ──
// Like a device's tools: toolOpen (agent_tools.go), 'auto' in the main
// window (a session window of its own with "Detach by default"; the
// explorer: the Files page), here in a session window.
function cxnWhere() { return reachDetachedMode ? 'here' : 'auto'; }
function cxnOpenNode(n, where) {
  if (!n) return null;
  if (n.kind === CXN_KIND_SSH) return toolOpen(cxnDevice(n), 'ssh', where || cxnWhere());
  if (n.kind === CXN_KIND_RDP) return toolOpen(cxnDevice(n), 'rdp', where || cxnWhere());
  return null;
}
function cxnOpenNodeFiles(n, where) {
  if (!n || n.kind !== CXN_KIND_SSH) return null;
  return toolOpen(cxnDevice(n), 'files', where || cxnWhere());
}

// ── Its page (ui.go selectDeviceInto): a header of its own ──
function cxnSelectInto(dev, area) {
  selectedDevice = dev;
  var chatBtn = document.getElementById('btn-chat');
  if (chatBtn) chatBtn.style.display = (typeof chatConvs !== 'undefined' && chatConvs.size) ? '' : 'none';
  area.style.display = 'flex'; area.style.flexDirection = 'column'; area.style.overflow = 'hidden';
  var hdr = document.createElement('div'); hdr.className = 'device-header merged';
  var back = document.createElement('button'); back.className = 'btn-sm'; back.style.cssText = 'padding:5px 8px'; back.title = T('detail.back');
  back.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 18 9 12 15 6"/></svg>';
  back.addEventListener('click', function () { navigateTo('devices'); });
  hdr.appendChild(back);
  var h2 = document.createElement('h2'); h2.textContent = dev.hostname; hdr.appendChild(h2);
  var chip = document.createElement('span'); chip.className = 'cxn-dev-chip'; chip.textContent = T('devices.local.chip'); hdr.appendChild(chip);
  // Info, as for a server's device (a side panel: localconn_info.go).
  var tabs = document.createElement('div'); tabs.className = 'tabs';
  var ib = document.createElement('button'); ib.className = 'tab-btn side-tab'; ib.id = 'hdr-info';
  ib.textContent = T('common.info'); ib.title = T('detail.infoTip');
  ib.addEventListener('click', function () { sidePanelToggle('info'); });
  tabs.appendChild(ib); hdr.appendChild(tabs);
  var g = document.createElement('div'); g.className = 'dev-tools'; g.id = 'dev-tools';
  CXN_TOOL_KINDS.forEach(function (k) {
    if (!cxnToolState(dev, k).show) return;
    var b = document.createElement('button'); b.className = 'tool-hbtn'; b.id = 'hdr-tool-' + k; b.dataset.kind = k;
    b.innerHTML = cxnToolIcon(k) + '<span>' + TH(toolLabelKey(k)) + '</span>';
    b.title = T(toolNameKey(k));
    b.addEventListener('click', function () { toolOpen(dev, k, cxnWhere()); });
    g.appendChild(b);
  });
  hdr.appendChild(g);
  var meta = document.createElement('span'); meta.className = 'dev-meta'; meta.style.cssText = 'font-size:11px;color:var(--muted)';
  meta.textContent = cxnNodeText(dev._node); hdr.appendChild(meta);
  area.appendChild(hdr);
  var tc = document.createElement('div'); tc.className = 'tab-content'; tc.id = 'tab-content';
  area.appendChild(tc);
  switchTab('remote', area);
}
// Its page with none of its tabs open (ui.go renderRemoteTab): its tools.
function cxnRenderLocalPane(tc, dev) {
  var p = document.createElement('div'); p.className = 'cxn-pane';
  var t = document.createElement('div'); t.textContent = cxnNodeText(dev._node); p.appendChild(t);
  var acts = document.createElement('div'); acts.className = 'acts';
  CXN_TOOL_KINDS.forEach(function (k) {
    if (!cxnToolState(dev, k).show) return;
    var b = document.createElement('button'); b.className = k !== 'files' ? 'btn-primary' : 'btn-sm';
    b.style.cssText = 'width:auto;padding:8px 16px';
    b.textContent = T(toolNameKey(k));
    b.addEventListener('click', function () { toolOpen(dev, k, cxnWhere()); });
    acts.appendChild(b);
  });
  p.appendChild(acts);
  tc.appendChild(p);
}
// A session window's tool bar (no device header there): what it is, and
// its Info panel (an SSH connection: localconn_info.go).
function cxnBarMeta(bar, dev) {
  var m = document.createElement('span'); m.className = 'cxn-bar-meta'; m.id = 'cxn-bar-meta';
  m.textContent = cxnNodeText(dev && dev._node); m.title = m.textContent;
  bar.appendChild(m);
  if (!cxnIsSsh(dev)) return;
  var sep = document.createElement('div'); sep.className = 'toolbar-sep'; bar.appendChild(sep);
  var b = document.createElement('button'); b.className = 'btn-sm reach-ico-btn reach-side-btn'; b.id = 'tb-info';
  b.title = T('detail.infoTip'); b.innerHTML = REACH_ICON.info + '<span>' + TH('common.info') + '</span>';
  b.addEventListener('click', function () { sidePanelToggle('info'); });
  bar.appendChild(b);
}

// ── The terminal's transport (terminal_viewer.go TERM_TRANSPORTS) ──
// The app dials the connection (reachSshConnect) and answers with a tunnel
// token: /local/ssh-tunnel/<token>, the relay's protocol (ssh_relay.go).
// An unknown or changed host key: its fingerprint over the terminal, then a
// new start with { trust }. The vault locked: its unlock, then a new start.
TERM_TRANSPORTS.local = {
  waitKey: 'connections.ssh.connecting',
  open: async function (tab, gen, o) {
    toolSetStatus(tab, 'connecting', { k: 'connections.ssh.connecting' });
    toolSetOverlay(tab, { kind: 'busy', title: { k: 'connections.ssh.connecting' }, text: '' });
    var node = tab.device && tab.device._node, t = tab.term, res = null;
    if (!node) { termFail(tab, { k: 'vault.err.internal' }, true); return null; }
    try {
      res = await vaultAsyncCall('reachSshConnect', String(node.id), termClampDim(t && t.cols, 80), termClampDim(t && t.rows, 24), !!(o && o.trust));
    } catch (e) {
      if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return null;
      if (e && e.code === 'locked') { cxnTermLocked(tab); return null; }
      termFail(tab, vaultErrText(e), true);
      return null;
    }
    // (closed or restarted meanwhile: its token, never used, expires in the app)
    if (gen !== tab.gen || toolTabs.indexOf(tab) < 0 || tab.userEnded) return null;
    if (res && res.hostKey) { cxnTermHostKey(tab, res.hostKey); return null; }
    var token = (res && typeof res.token === 'string') ? res.token : '';
    if (!/^[0-9a-f]{64}$/.test(token)) { termFail(tab, { k: 'vault.err.internal' }, true); return null; }
    return { path: '/local/ssh-tunnel/' + token, sessionId: '', token: token, delivery: 'live' };
  },
};
// An unknown or changed host key: its fingerprint over the terminal.
function cxnTermHostKey(tab, hk) {
  var mismatch = hk && hk.state === 'mismatch';
  toolSetStatus(tab, 'connecting', { k: 'connections.ssh.connecting' });
  toolSetOverlay(tab, { kind: mismatch ? 'error' : 'info',
    title: { k: mismatch ? 'connections.ssh.hostKeyMismatch' : 'connections.ssh.hostKeyUnknown', p: { host: String((hk && hk.hostport) || '') } },
    text: String((hk && hk.fingerprint) || ''),
    acts: [
      { label: { k: 'connections.ssh.trustAndConnect' }, primary: !mismatch, id: 'term-ov-trust', run: function () { termStart(tab, { trust: true }); } },
      { label: { k: 'common.close' }, id: 'term-ov-close', run: function () { toolCloseTab(tab); } },
    ] });
}
// The vault is locked (its secret is there): its unlock, then the connection.
function cxnTermLocked(tab) {
  termClearTimer(tab);
  tab.live = false;
  toolSetStatus(tab, 'error', { k: 'term.ended' });
  toolSetOverlay(tab, { kind: 'error', title: { k: 'vault.locked.title' }, text: '', acts: [
    { label: { k: 'vault.locked.submit' }, primary: true, id: 'term-ov-unlock', run: function () { cxnUnlockThen(function () { termStart(tab); }); } },
    { label: { k: 'common.close' }, id: 'term-ov-close', run: function () { toolCloseTab(tab); } },
  ] });
  termRefreshButtons(tab);
}
// The vault's own unlock form (Windows Hello included), in a dialog; then fn.
function cxnUnlockThen(fn) {
  var m = scriptsBaseModal('vault.locked.title');
  var wrap = document.createElement('div'); wrap.className = 'vault-form';
  m.box.appendChild(wrap);
  vaultMenuUnlockForm(wrap, null, function () { m.close(); if (typeof fn === 'function') fn(); });
}

// ── The explorer's transport (explorer_viewer.go explorerSend) ──
var cxnSftp = new Map(); // vault id -> { p: Promise<connId> }
// The connection's SFTP session (opened once; fresh: the previous one ended).
function cxnSftpConn(dev, fresh) {
  var id = String(dev._node.id), c = cxnSftp.get(id);
  if (c && !fresh) return c.p;
  if (c) { cxnSftp.delete(id); c.p.then(function (cid) { if (cid) vaultSyncCall('reachSftpClose', cid); }, function () {}); }
  c = { p: null };
  c.p = (async function () {
    var trust = false;
    for (var i = 0; i < 2; i++) {
      var res = null;
      try { res = await vaultAsyncCall('reachSftpConnect', id, trust); }
      catch (e) { throw explorerErr(vaultErrText(e), 'not_connected'); }
      if (res && res.hostKey) {
        if (trust || !(await cxnHostKeyAsk(res.hostKey))) throw explorerErr({ k: 'connections.ssh.hostKeyNotTrusted' }, 'invalid');
        trust = true;
        continue;
      }
      if (res && typeof res.connId === 'string' && res.connId) {
        // No SFTP on that server: SCP and its shell (scp.go), said once.
        if (res.mode === 'scp') reachNotify(T(res.listing === false ? 'connections.scp.noShell' : 'connections.scp.mode', { name: String(dev.hostname || '') }), 'warn');
        return res.connId;
      }
      break;
    }
    throw explorerErr({ k: 'vault.err.internal' }, 'not_connected');
  })();
  cxnSftp.set(id, c);
  c.p.catch(function () { if (cxnSftp.get(id) === c) cxnSftp.delete(id); });
  return c.p;
}
// One host key prompt per connection at a time (a listing and a transfer can ask together).
var cxnHostKeyAsking = new Map();
function cxnHostKeyAsk(hk) {
  var key = String((hk && hk.hostport) || '') + '|' + String((hk && hk.fingerprint) || '');
  if (cxnHostKeyAsking.has(key)) return cxnHostKeyAsking.get(key);
  var p = new Promise(function (resolve) {
    var mismatch = hk && hk.state === 'mismatch', done = false;
    var m = scriptsBaseModal('connections.ssh.hostKeyTitle');
    var finish = function (v) { if (done) return; done = true; clearInterval(watch); m.close(); resolve(v); };
    var warn = document.createElement('div'); warn.className = 'cxn-hk-warn' + (mismatch ? ' mismatch' : '');
    warn.textContent = T(mismatch ? 'connections.ssh.hostKeyMismatch' : 'connections.ssh.hostKeyUnknown', { host: String((hk && hk.hostport) || '') });
    var fp = document.createElement('div'); fp.className = 'cxn-hk-fp'; fp.textContent = String((hk && hk.fingerprint) || '');
    var actions = document.createElement('div'); actions.className = 'modal-actions';
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
    cancel.addEventListener('click', function () { finish(false); });
    var ok = document.createElement('button'); ok.className = mismatch ? 'exp-btn-danger' : 'btn-primary'; ok.textContent = T('connections.ssh.trustAndConnect');
    ok.addEventListener('click', function () { finish(true); });
    actions.appendChild(cancel); actions.appendChild(ok);
    m.box.appendChild(warn); m.box.appendChild(fp); m.box.appendChild(actions);
    // (closed by its X, Escape or a click outside: not trusted)
    var watch = setInterval(function () { if (m.box.isConnected === false) finish(false); }, 400);
  });
  cxnHostKeyAsking.set(key, p);
  var drop = function () { cxnHostKeyAsking.delete(key); };
  p.then(drop, drop);
  return p;
}
// One explorer command on the connection's SFTP session: the agent's
// result shapes (localconn_explorer.go); a session that ended is opened
// again once.
async function cxnSftpSend(tab, type, payload, ms) {
  var dev = tab.device;
  for (var attempt = 0; attempt < 2; attempt++) {
    var connId = await cxnSftpConn(dev, attempt > 0);
    if (tab.ex && tab.ex.disposed) throw explorerErr('', 'cancelled');
    var out = null;
    try { out = await vaultAsyncCallWithin(ms || EXPLORER_SHORT_MS, 'reachSftpCmd', [connId, String(type), JSON.stringify(payload || {})]); }
    catch (e) {
      if (e && e.code === 'timeout') throw explorerErr({ k: 'explorer.err.timeout' }, 'timeout');
      throw explorerErr(vaultErrText(e), 'not_connected');
    }
    if (tab.ex && tab.ex.disposed) throw explorerErr('', 'cancelled');
    if (out && out.ok === true) return out.result;
    // (SCP's limits, in the page's language: scp.go)
    if (out && out.code === 'scp_no_listing') throw explorerErr({ k: 'connections.scp.noListing' }, '');
    if (out && out.code === 'scp_missing') throw explorerErr({ k: 'connections.scp.missing' }, '');
    // (a streamed transfer's handle lives in its session: never resent on a new one)
    if (out && out.gone && attempt === 0 && !/^((upload|download)_(write|read|close|abort)|save_(status|cancel))$/.test(String(type))) continue;
    throw explorerErr(String((out && out.error) || '').slice(0, 300), out && out.gone ? 'not_connected' : '');
  }
  throw explorerErr({ k: 'explorer.err.notConnected' }, 'not_connected');
}
// An explorer tab of a local connection closing: its SFTP session ends with
// the last one (a transfer or an edit context still using it keeps it).
function cxnSftpRelease(tab) {
  var dev = tab && tab.device;
  if (!devIsLocal(dev)) return;
  var k = devKey(dev);
  var others = toolTabs.some(function (t) { return t !== tab && t.kind === 'files' && t.device && devKey(t.device) === k; });
  if (others || (typeof explorerCtxs !== 'undefined' && explorerCtxs.has(k))) return;
  var id = String(dev._node.id), c = cxnSftp.get(id);
  if (!c) return;
  cxnSftp.delete(id);
  c.p.then(function (cid) { if (cid) vaultSyncCall('reachSftpClose', cid); }, function () {});
}
// The page goes (a reload, the app quitting, a session window closing): its
// SFTP sessions end (its terminals end with their tunnels: ssh_relay.go).
if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
  window.addEventListener('pagehide', function () {
    cxnSftp.forEach(function (c) { c.p.then(function (cid) { if (cid) vaultSyncCall('reachSftpClose', cid); }, function () {}); });
  });
}
` + localConnInfoJS // (its Info panel: localconn_info.go)
