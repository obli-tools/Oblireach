package main

// rdp_stream_view.go — the RDP tab in STREAM mode (Linux; ui_platform.go
// injects it after rdp_view.go's block on that platform only, so the
// Windows page is unchanged). Wayland cannot embed mstsc-like windows: the
// RDP helper streams H.264 (rdphost_relay.go) and the tab draws it itself,
// like an ObliReach session: a canvas, WebCodecs, the mouse and keyboard
// sent as the viewer protocol's messages.
//
// The tab stays TOOL_IMPL.rdp (session window, "Detach by default",
// tear-off, merge, mosaic): this block replaces the implementation and the
// three functions terminal_viewer.go calls by name (rdpTabStart, rdpTabPark,
// rdpTabButtons; the later declaration wins in the page's script).
//
//   - start: reachRdpConnect (the vault's secret stays in Go; x carries the
//     page's scale), or reachRdpAttach (a session moved here), then
//     reachRdpTunnel and the WebSocket /local/rdp-tunnel/<token>;
//   - the server's certificate (rdp_cert) and a credentials question
//     (rdp_credentials) are dialogs of the tab; a changed certificate is
//     only accepted once the operator ticked "I understand it changed";
//   - the tab's size follows its placeholder (rdp_resize, in device
//     pixels) when the server can resize (init feature rdp_resize), else the
//     picture is scaled;
//   - clipboard text both ways (Ctrl+V sends the local text first; the
//     remote copies come back when the sync is on), Ctrl+Alt+End (the
//     secure attention sequence of RDP), the server's pointer shapes;
//   - moving the tab: reachRdpPark (the helper goes on), the new window's
//     page attaches and opens a new tunnel (a fresh key frame).
//
// Injected verbatim (no Sprintf); no backtick here.

const rdpStreamCSS = `
/* ── Local RDP tab, stream mode (rdp_stream_view.go) ── */
.rdp-host .rdps-canvas{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;outline:none;display:none}
.rdp-host.live .rdps-canvas{display:block}
.rdps-dlg .rdps-fp{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:11.5px;word-break:break-all;padding:8px 10px;border-radius:8px;background:rgb(var(--ink) / .06);color:var(--text)}
.rdps-dlg .rdps-warn{color:var(--danger);font-weight:600;font-size:12.5px}
.rdps-dlg label.rdps-chk{display:flex;align-items:center;gap:8px;font-size:12.5px;color:var(--text);margin-top:8px;cursor:pointer}
.rdps-dlg .rdps-field{display:flex;flex-direction:column;gap:4px;margin-top:8px;font-size:12px;color:var(--muted)}
.rdps-dlg .rdps-field input{padding:8px 10px;border:none;border-radius:8px;background:var(--input-bg);color:var(--text);font-size:12.5px}
`

const rdpStreamJS = `
// ── Local RDP tab, stream mode (rdp_stream_view.go) ─────────────────────────
var RDPS_RESIZE_MS = 300;
var RDPS_ERR_KEYS = { rdp_auth_failed: 1, rdp_auth_cancelled: 1, rdp_cert_rejected: 1, rdp_dns_failed: 1, rdp_unreachable: 1, rdp_security_failed: 1,
  rdp_nla_required: 1, rdp_password_expired: 1, rdp_account_restricted: 1, rdp_kerberos_failed: 1, rdp_other_connection: 1,
  rdp_server_disconnected: 1, rdp_logged_off: 1, rdp_idle_timeout: 1, rdp_logon_timeout: 1, rdp_denied: 1, rdp_license_error: 1,
  rdp_reconnect_failed: 1, rdp_encoder_failed: 1, rdp_connect_failed: 1, rdp_config_invalid: 1, rdp_helper_failed: 1 };
function rdpsErrText(e) {
  var c = e && typeof e.code === 'string' ? e.code : '';
  if (RDPS_ERR_KEYS[c]) return { k: 'connections.rdp.err.' + c.slice(4) };
  return (e && e.message) ? { k: 'connections.rdp.endedWith', p: { reason: String(e.message).slice(0, 300) } } : { k: 'connections.rdp.ended' };
}
// The tab's size in device pixels, and the page's scale.
function rdpsSize(tab) {
  var r = tab.host && typeof tab.host.getBoundingClientRect === 'function' ? tab.host.getBoundingClientRect() : null;
  var k = rdpPixelRatio();
  if (!r) return { w: 0, h: 0, scale: Math.round(k * 100), dpr: k };
  return { w: Math.round(r.width * k), h: Math.round(r.height * k), scale: Math.round(k * 100), dpr: k };
}
function rdpsCanvas(tab) {
  if (tab.rdpsCanvas) return tab.rdpsCanvas;
  var c = document.createElement('canvas'); c.className = 'rdps-canvas'; c.id = 'rdps-canvas-' + tab.id;
  c.setAttribute('tabindex', '0');
  tab.host.appendChild(c);
  tab.rdpsCanvas = c;
  rdpsWire(tab, c);
  return c;
}
function rdpsSend(tab, m) {
  var ws = tab.rdpsWs;
  if (!ws || ws.readyState !== 1) return false;
  try { ws.send(JSON.stringify(m)); return true; } catch (e) { return false; }
}
// Everything of the current tunnel let go (the session itself is Go's).
function rdpsDrop(tab) {
  var ws = tab.rdpsWs; tab.rdpsWs = null;
  if (ws) { try { ws.close(); } catch (e) {} }
  if (tab.rdpsDec) { try { tab.rdpsDec.close(); } catch (e) {} tab.rdpsDec = null; }
  if (tab.rdpsResizeTimer) { clearTimeout(tab.rdpsResizeTimer); tab.rdpsResizeTimer = null; }
  if (tab.rdpsRO) { try { tab.rdpsRO.disconnect(); } catch (e) {} tab.rdpsRO = null; }
  if (tab.rdpsDlg) { try { tab.rdpsDlg.close(); } catch (e) {} tab.rdpsDlg = null; }
  tab.rdpsInit = null; tab.rdpsKeyQ = null;
  if (tab.host && tab.host.classList) tab.host.classList.remove('live');
}
function rdpsEnd(tab, err) {
  var id = tab.connId;
  rdpsDrop(tab);
  if (id) vaultSyncCall('reachRdpClose', id);
  if (err) rdpTabFail(tab, rdpsErrText(err)); else rdpTabEnded(tab, '');
}
async function rdpTabStart(tab) {
  if (tab.connId) { var old = tab.connId; rdpsDrop(tab); vaultSyncCall('reachRdpClose', old); rdpForget(tab); }
  var gen = tab.gen = (tab.gen || 0) + 1;
  tab.live = false; tab.rdpsErr = null; tab.rdpsParking = false;
  var node = tab.device && tab.device._node;
  toolSetStatus(tab, 'connecting', { k: 'connections.rdp.connecting' });
  toolSetOverlay(tab, { kind: 'busy', title: { k: 'connections.rdp.connecting' }, text: '' });
  rdpTabButtons(tab);
  for (var i = 0; i < 20 && toolTabs.indexOf(tab) >= 0 && toolShown(tab) && rdpsSize(tab).w <= 0; i++) await new Promise(function (r) { setTimeout(r, 50); });
  if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return null;
  var sz = rdpsSize(tab), w = sz.w > 0 ? sz.w : 1280, h = sz.h > 0 ? sz.h : 800, connId = '';
  if (tab.rdpPark) {
    var park = tab.rdpPark; tab.rdpPark = '';
    var got = await vaultSyncCall('reachRdpAttach', park, 0, 0, w, h);
    connId = typeof got === 'string' ? got : '';
    if (!connId) { rdpTabFail(tab, { k: 'connections.rdp.moveLost' }); return null; }
  } else {
    if (!node) { rdpTabFail(tab, { k: 'vault.err.internal' }); return null; }
    var res = null;
    try { res = await vaultAsyncCall('reachRdpConnect', String(node.id), sz.scale, 0, w, h); }
    catch (e) {
      if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return null;
      if (e && e.code === 'locked') { rdpTabLocked(tab); return null; }
      rdpTabFail(tab, vaultErrText(e));
      return null;
    }
    connId = (res && typeof res.connId === 'string') ? res.connId : '';
    if (!connId) { rdpTabFail(tab, { k: 'vault.err.internal' }); return null; }
  }
  if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) { vaultSyncCall('reachRdpClose', connId); return null; }
  tab.connId = connId;
  rdpTabsByConn[connId] = tab;
  var tun = await vaultSyncCall('reachRdpTunnel', connId);
  if (gen !== tab.gen || toolTabs.indexOf(tab) < 0) return null;
  if (typeof tun !== 'string' || !/^[0-9a-f]{64}$/.test(tun)) { rdpsEnd(tab, { code: 'rdp_helper_failed' }); return null; }
  rdpsOpen(tab, tun, gen);
  return connId;
}
function rdpsOpen(tab, tun, gen) {
  var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  var ws = new WebSocket(proto + '//' + location.host + '/local/rdp-tunnel/' + tun);
  ws.binaryType = 'arraybuffer';
  tab.rdpsWs = ws;
  rdpsCanvas(tab);
  ws.onmessage = function (ev) {
    if (tab.rdpsWs !== ws) return;
    if (typeof ev.data === 'string') { var m = null; try { m = JSON.parse(ev.data); } catch (e) { return; } if (m && typeof m === 'object') rdpsOnText(tab, m); return; }
    if (ev.data instanceof ArrayBuffer) rdpsOnBinary(tab, new Uint8Array(ev.data));
  };
  ws.onclose = function () {
    if (tab.rdpsWs !== ws) return;
    tab.rdpsWs = null;
    if (tab.rdpsParking || gen !== tab.gen) return; // (moving to another window)
    rdpsEnd(tab, tab.rdpsErr);
  };
}
function rdpsOnText(tab, m) {
  switch (m.type) {
    case 'paired': return;
    case 'init': rdpsOnInit(tab, m); return;
    case 'rdp_state': rdpsOnState(tab, m); return;
    case 'rdp_cert': rdpsCertDialog(tab, m); return;
    case 'rdp_credentials': rdpsCredsDialog(tab, m); return;
    case 'error': tab.rdpsErr = { code: String(m.code || ''), message: String(m.message || '') }; return;
    case 'cursor': rdpsOnCursor(tab, m); return;
    case 'clipboard_content':
      if (typeof m.text === 'string' && m.text && (m.origin !== 'remote_change' || reachClipSyncEnabled())) reachNativeClipboardWrite(m.text);
      return;
    case 'clipboard_ack':
      if (m.ok === false && m.error === 'too_large') reachNotify(T('viewer.clip.tooLargePaste'), 'warn');
      return;
  }
}
function rdpsOnState(tab, m) {
  if (typeof m.resize === 'boolean') tab.rdpsResize = m.resize;
  if (m.state === 'reconnecting') {
    toolSetStatus(tab, 'connecting', { k: 'connections.rdp.reconnecting' });
    toolSetOverlay(tab, { kind: 'busy', title: { k: 'connections.rdp.reconnecting' }, text: '' });
  } else if (m.state === 'connecting' && !tab.rdpsInit) {
    toolSetOverlay(tab, { kind: 'busy', title: { k: 'connections.rdp.connecting' }, text: '' });
  } else if (m.state === 'display_control') {
    rdpsResizeSoon(tab);
  }
}
function rdpsOnInit(tab, m) {
  var w = m.width | 0, h = m.height | 0;
  if (w <= 0 || h <= 0) return;
  var feats = Array.isArray(m.features) ? m.features : [];
  tab.rdpsInit = { w: w, h: h, features: feats };
  tab.rdpsResize = feats.indexOf('rdp_resize') >= 0;
  var c = rdpsCanvas(tab);
  if (c.width !== w || c.height !== h) { c.width = w; c.height = h; }
  if (tab.rdpsDec) { try { tab.rdpsDec.close(); } catch (e) {} }
  tab.rdpsDec = rdpsDecoder(tab, w, h);
  tab.rdpsNeedKey = true; tab.rdpsTs = 0;
  tab.live = true;
  tab.host.classList.add('live');
  toolSetOverlay(tab, null);
  toolSetStatus(tab, 'live', { k: 'connections.rdp.connected' });
  rdpTabButtons(tab);
  if (feats.indexOf('clipboard_push') >= 0) rdpsSend(tab, { type: 'clipboard_sync', push: reachClipSyncEnabled() });
  if (!tab.rdpsRO && typeof ResizeObserver === 'function') {
    try { tab.rdpsRO = new ResizeObserver(function () { rdpsResizeSoon(tab); }); tab.rdpsRO.observe(tab.host); } catch (e) { tab.rdpsRO = null; }
  }
  rdpsResizeSoon(tab);
}
function rdpsDecoder(tab, w, h) {
  if (typeof VideoDecoder === 'undefined') return null;
  var dec = null;
  try {
    dec = new VideoDecoder({
      output: function (frame) {
        try {
          if (tab.rdpsDec !== dec || !tab.rdpsCanvas) return;
          var c = tab.rdpsCanvas;
          if (c.width !== frame.displayWidth || c.height !== frame.displayHeight) { c.width = frame.displayWidth; c.height = frame.displayHeight; }
          var ctx = c.getContext('2d'); if (ctx) ctx.drawImage(frame, 0, 0);
        } finally { frame.close(); }
      },
      error: function () { if (tab.rdpsDec === dec) { tab.rdpsDec = null; tab.rdpsNeedKey = true; } },
    });
    dec.configure({ codec: reachCodecString('h264'), codedWidth: w, codedHeight: h, optimizeForLatency: true });
  } catch (e) { try { if (dec) dec.close(); } catch (x) {} return null; }
  return dec;
}
function rdpsOnBinary(tab, u8) {
  if (!u8.length || u8[0] !== REACH.FRAME.H264 || !tab.rdpsInit) return;
  var p = u8.subarray(1), key = reachH264IsKey(p);
  if (!tab.rdpsDec || tab.rdpsDec.state === 'closed') {
    if (!key) return;
    tab.rdpsDec = rdpsDecoder(tab, tab.rdpsInit.w, tab.rdpsInit.h);
    if (!tab.rdpsDec) return;
  }
  if (tab.rdpsNeedKey) { if (!key) return; tab.rdpsNeedKey = false; }
  try { tab.rdpsDec.decode(new EncodedVideoChunk({ type: key ? 'key' : 'delta', timestamp: tab.rdpsTs, data: p })); }
  catch (e) { tab.rdpsNeedKey = true; rdpsSend(tab, { type: 'refresh' }); }
  tab.rdpsTs += 33333;
}
function rdpsOnCursor(tab, m) {
  var c = tab.rdpsCanvas;
  if (!c) return;
  if (m.kind === 'hidden') c.style.cursor = 'none';
  else if (m.kind === 'shape' && typeof m.png === 'string' && /^[A-Za-z0-9+\/=]+$/.test(m.png)) c.style.cursor = 'url(data:image/png;base64,' + m.png + ') ' + (m.hx | 0) + ' ' + (m.hy | 0) + ', default';
  else c.style.cursor = 'default';
}
// The tab's size to the server (when it can resize), a moment after the
// last change.
function rdpsResizeSoon(tab) {
  if (tab.rdpsResizeTimer) clearTimeout(tab.rdpsResizeTimer);
  tab.rdpsResizeTimer = setTimeout(function () {
    tab.rdpsResizeTimer = null;
    if (!tab.rdpsResize || !tab.rdpsInit || !toolShown(tab)) return;
    var sz = rdpsSize(tab);
    if (sz.w < 200 || sz.h < 200) return;
    var sig = sz.w + 'x' + sz.h + '@' + sz.scale;
    if (sig === tab.rdpsSentSize) return;
    tab.rdpsSentSize = sig;
    rdpsSend(tab, { type: 'rdp_resize', width: sz.w, height: sz.h, dpr: sz.dpr });
  }, RDPS_RESIZE_MS);
}
// ── Input ──
function rdpsPoint(tab, e) {
  var c = tab.rdpsCanvas, i = tab.rdpsInit;
  if (!c || !i) return null;
  var r = c.getBoundingClientRect();
  return reachMapPointer(e.clientX - r.left, e.clientY - r.top, r.width, r.height, c.width || i.w, c.height || i.h, true);
}
function rdpsMouse(tab, action, e) {
  var p = rdpsPoint(tab, e);
  if (!p || (action === 'down' && !p.inside) || (action === 'move' && !p.inside && !e.buttons)) return;
  var m = { type: 'mouse', action: action, x: p.x, y: p.y };
  if (action === 'down' || action === 'up') m.button = e.button | 0;
  rdpsSend(tab, m);
}
function rdpsWire(tab, c) {
  c.addEventListener('pointerdown', function (e) { try { c.setPointerCapture(e.pointerId); } catch (x) {} });
  c.addEventListener('mousedown', function (e) { e.preventDefault(); try { c.focus({ preventScroll: true }); } catch (x) {} rdpsMouse(tab, 'down', e); });
  c.addEventListener('mousemove', function (e) { rdpsMouse(tab, 'move', e); });
  c.addEventListener('mouseup', function (e) { rdpsMouse(tab, 'up', e); });
  c.addEventListener('wheel', function (e) {
    e.preventDefault();
    var p = rdpsPoint(tab, e);
    if (!p) return;
    if (e.deltaY) rdpsSend(tab, { type: 'mouse', action: 'scroll', x: p.x, y: p.y, delta: Math.sign(-e.deltaY) });
    if (e.deltaX) rdpsSend(tab, { type: 'mouse', action: 'hscroll', x: p.x, y: p.y, delta: Math.sign(e.deltaX) });
  }, { passive: false });
  c.addEventListener('contextmenu', function (e) { e.preventDefault(); });
  c.addEventListener('keydown', function (e) { e.preventDefault(); rdpsKey(tab, 'down', e); });
  c.addEventListener('keyup', function (e) { e.preventDefault(); rdpsKey(tab, 'up', e); });
  c.addEventListener('focus', function () { reachSetKeyCapture(true); });
  c.addEventListener('blur', function () { reachSetKeyCapture(false); rdpsSend(tab, { type: 'release_all' }); });
}
function rdpsKey(tab, action, e) {
  var k = reachKeyMsg(tab, { type: 'key', action: action, code: String(e.code || ''), key: String(e.key || '').length <= 8 ? String(e.key || '') : '' });
  if (!k.code && !k.key) return;
  if (tab.rdpsKeyQ) { tab.rdpsKeyQ.push(k); return; }
  var paste = action === 'down' && !e.repeat && reachClipSyncEnabled() &&
    ((((e.ctrlKey || e.metaKey) && !e.altKey) && e.code === 'KeyV') || (e.shiftKey && !e.ctrlKey && e.code === 'Insert'));
  if (!paste || !(tab.rdpsInit && tab.rdpsInit.features.indexOf('clipboard_text') >= 0)) { rdpsSend(tab, k); return; }
  // Ctrl+V: the local text goes first, then the keys (in order).
  tab.rdpsKeyQ = [k];
  var done = false;
  var flush = function () {
    if (done) return; done = true;
    var q = tab.rdpsKeyQ || []; tab.rdpsKeyQ = null;
    q.forEach(function (x) { rdpsSend(tab, x); });
  };
  var guard = setTimeout(flush, 700);
  reachNativeClipboardRead().then(function (info) {
    if (!done && info && info.text && info.text !== tab.rdpsLastSet) {
      tab.rdpsLastSet = info.text;
      rdpsSend(tab, { type: 'clipboard_set', text: info.text, id: Date.now() % 1000000 });
    }
  }).catch(function () {}).then(function () { clearTimeout(guard); flush(); });
}
// ── Dialogs ──
function rdpsField(box, labelKey, id, type, value) {
  var f = document.createElement('label'); f.className = 'rdps-field';
  var s = document.createElement('span'); s.textContent = T(labelKey); f.appendChild(s);
  var i = document.createElement('input'); i.id = id; i.type = type; i.value = value || ''; i.setAttribute('autocomplete', 'off'); i.setAttribute('spellcheck', 'false');
  f.appendChild(i); box.appendChild(f);
  return i;
}
function rdpsCheck(box, labelKey, id) {
  var l = document.createElement('label'); l.className = 'rdps-chk';
  var c = document.createElement('input'); c.type = 'checkbox'; c.id = id; l.appendChild(c);
  var s = document.createElement('span'); s.textContent = T(labelKey); l.appendChild(s);
  box.appendChild(l);
  return c;
}
function rdpsActions(box) { var a = document.createElement('div'); a.className = 'modal-actions'; box.appendChild(a); return a; }
function rdpsBtn(row, cls, id, labelKey, run) {
  var b = document.createElement('button'); b.className = cls; b.id = id; b.textContent = T(labelKey);
  b.addEventListener('click', run); row.appendChild(b);
  return b;
}
// The server's certificate, not trusted yet: accepted (this time, or
// remembered) or refused. A changed one says so in red and is only
// accepted once its change is confirmed.
function rdpsCertDialog(tab, m) {
  if (tab.rdpsDlg) { try { tab.rdpsDlg.close(); } catch (e) {} }
  var answered = false;
  var reply = function (accept, remember, confirm) {
    if (answered) return; answered = true;
    rdpsSend(tab, { type: 'rdp_cert_reply', accept: !!accept, remember: !!remember, confirmChanged: !!confirm });
  };
  var dlg = vmModal('connections.rdp.cert.title', 'rdps-cert', function () { reply(false, false, false); if (tab.rdpsDlg === dlg) tab.rdpsDlg = null; });
  tab.rdpsDlg = dlg;
  dlg.overlay.classList.add('rdps-dlg');
  var p = document.createElement('p'); p.className = 'desc'; p.id = 'rdps-cert-text';
  p.textContent = T(m.gateway ? 'connections.rdp.cert.textGateway' : 'connections.rdp.cert.text', { host: String(m.host || '') + ':' + (m.port | 0) });
  dlg.box.appendChild(p);
  if (m.changed) { var w = document.createElement('p'); w.className = 'rdps-warn'; w.id = 'rdps-cert-changed'; w.textContent = T('connections.rdp.cert.changed'); dlg.box.appendChild(w); }
  if (m.hostMismatch) { var n = document.createElement('p'); n.className = 'rdps-warn'; n.id = 'rdps-cert-mismatch'; n.textContent = T('connections.rdp.cert.mismatch', { name: String(m.commonName || '') }); dlg.box.appendChild(n); }
  var fp = document.createElement('div'); fp.className = 'rdps-fp'; fp.id = 'rdps-cert-fp'; fp.textContent = String(m.fingerprint || ''); dlg.box.appendChild(fp);
  if (m.issuer) { var is = document.createElement('p'); is.className = 'desc'; is.textContent = T('connections.rdp.cert.issuer', { issuer: String(m.issuer) }); dlg.box.appendChild(is); }
  var remember = m.canRemember ? rdpsCheck(dlg.box, 'connections.rdp.cert.remember', 'rdps-cert-remember') : null;
  var confirm = m.changed ? rdpsCheck(dlg.box, 'connections.rdp.cert.confirmChanged', 'rdps-cert-confirm') : null;
  var row = rdpsActions(dlg.box);
  rdpsBtn(row, 'btn-ghost', 'rdps-cert-refuse', 'connections.rdp.cert.refuse', function () { reply(false, false, false); dlg.close(); });
  var ok = rdpsBtn(row, m.changed ? 'exp-btn-danger' : 'btn-primary', 'rdps-cert-accept', 'connections.rdp.cert.accept', function () {
    if (confirm && !confirm.checked) return;
    reply(true, remember && remember.checked, confirm && confirm.checked); dlg.close();
  });
  if (confirm) { ok.disabled = true; confirm.addEventListener('change', function () { ok.disabled = !confirm.checked; }); }
}
// The server asks for credentials (none saved, or refused): typed here,
// sent to the helper, optionally saved in the vault (Go does it).
function rdpsCredsDialog(tab, m) {
  if (tab.rdpsDlg) { try { tab.rdpsDlg.close(); } catch (e) {} }
  var answered = false;
  var reply = function (o) { if (answered) return; answered = true; rdpsSend(tab, o); };
  var dlg = vmModal('connections.rdp.creds.title', 'rdps-creds', function () { reply({ type: 'rdp_credentials', cancel: true }); if (tab.rdpsDlg === dlg) tab.rdpsDlg = null; });
  tab.rdpsDlg = dlg;
  dlg.overlay.classList.add('rdps-dlg');
  var p = document.createElement('p'); p.className = 'desc'; p.id = 'rdps-creds-text';
  p.textContent = T(m.reason === 'gateway' ? 'connections.rdp.creds.textGateway' : 'connections.rdp.creds.text');
  dlg.box.appendChild(p);
  var user = rdpsField(dlg.box, 'connections.rdp.creds.user', 'rdps-creds-user', 'text', String(m.username || ''));
  var dom = rdpsField(dlg.box, 'connections.rdp.creds.domain', 'rdps-creds-domain', 'text', String(m.domain || ''));
  var pass = rdpsField(dlg.box, 'connections.rdp.creds.password', 'rdps-creds-pass', 'password', '');
  var save = m.canSave ? rdpsCheck(dlg.box, 'connections.rdp.creds.save', 'rdps-creds-save') : null;
  var row = rdpsActions(dlg.box);
  rdpsBtn(row, 'btn-ghost', 'rdps-creds-cancel', 'common.cancel', function () { reply({ type: 'rdp_credentials', cancel: true }); dlg.close(); });
  var send = function () {
    reply({ type: 'rdp_credentials', username: String(user.value || ''), domain: String(dom.value || ''), password: String(pass.value || ''), save: !!(save && save.checked) });
    pass.value = '';
    dlg.close();
  };
  rdpsBtn(row, 'btn-primary', 'rdps-creds-ok', 'connections.rdp.creds.connect', send);
  pass.addEventListener('keydown', function (e) { if (e && e.key === 'Enter') { e.preventDefault(); send(); } });
  try { (user.value ? pass : user).focus(); } catch (e) {}
}
// ── Tool bar, park, the implementation ──
function rdpTabButtons(tab) {
  var acts = tab && tab.ui && tab.ui.actions;
  if (!acts || !toolShown(tab)) return;
  acts.innerHTML = '';
  if (tab.connId && tab.live) {
    var s = document.createElement('button'); s.className = 'btn-sm'; s.id = 'rdps-sas';
    s.textContent = T('connections.rdp.sas'); s.title = T('connections.rdp.sasTip');
    s.addEventListener('click', function () { rdpsSend(tab, { type: 'sas' }); if (tab.rdpsCanvas) try { tab.rdpsCanvas.focus(); } catch (e) {} });
    acts.appendChild(s);
    return;
  }
  if (tab.connId || tab.status === 'connecting') return;
  var b = document.createElement('button'); b.className = 'btn-sm'; b.id = 'rdp-reconnect';
  b.textContent = T('term.reconnect');
  b.addEventListener('click', function () { rdpTabStart(tab); });
  acts.appendChild(b);
}
async function rdpTabPark(tab) {
  if (!tab.connId) return '';
  var id = tab.connId;
  tab.rdpsParking = true;
  var t = await vaultSyncCall('reachRdpPark', id);
  if (typeof t !== 'string' || !/^[0-9a-f]{64}$/.test(t)) { tab.rdpsParking = false; return ''; }
  if (rdpTabsByConn[id] === tab) delete rdpTabsByConn[id];
  rdpsDrop(tab);
  tab.connId = null; tab.live = false;
  return t;
}
var rdpStreamTabImpl = {
  create: function (tab) {
    tab.host.className = 'tool-host rdp-host';
    tab.connId = null; tab.live = false; tab.gen = 0; tab.rdpsWs = null; tab.rdpsCanvas = null;
  },
  start: function (tab) { return rdpTabStart(tab); },
  render: function (tab, ui) { tab.ui = ui; rdpTabButtons(tab); if (tab.live) rdpsResizeSoon(tab); },
  dispose: function (tab, o) {
    tab.gen = (tab.gen || 0) + 1;
    var id = tab.connId;
    rdpsDrop(tab);
    rdpForget(tab);
    if (id && !(o && o.end === false)) vaultSyncCall('reachRdpClose', id);
  },
  focus: function (tab) { if (tab.rdpsCanvas && tab.live) { try { tab.rdpsCanvas.focus({ preventScroll: true }); } catch (e) {} } },
};
TOOL_IMPL.rdp = rdpStreamTabImpl;
`
