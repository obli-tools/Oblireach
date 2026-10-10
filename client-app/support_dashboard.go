package main

// support_dashboard.go — the two Oblireach Support cards at the top of the
// Dashboard (ui.go renderDashboardPage), with Obliance servers and on a
// local base alike:
//   - "This computer" (like TeamViewer's "Allow remote control"): this
//     Desktop's ID and one-time code, big, with Copy, "New code", the
//     server and its state (online / connecting / offline, "Retry now"),
//     the toggle "Allow incoming support" (on by default, kept by Go), the
//     anti-scam notice until it was read once, the running session (who,
//     Stop, "Report this helper" when the server allows it) and "Details" (the Receive help page, help_page.go). Its state
//     is "Receive help"'s (receive_help.go), polled by help_page.go.
//   - "Connect to another computer": the ID, the code and Connect, greyed
//     out until the operator is logged in on the chosen server or has an
//     operator key for it; Log in / Register side by side otherwise
//     (support_page.go supAuthBox). Connect continues on the Connect by
//     code page (its waiting screen, the verification code).
//
// Both consts are injected verbatim after the other tool blocks (ui.go
// buildUI); no backtick here.

const supDashCSS = `
/* ── Oblireach Support cards on the Dashboard (support_dashboard.go) ── */
.sdash{display:grid;grid-template-columns:repeat(auto-fit,minmax(340px,1fr));gap:14px;margin-bottom:18px}
.sdash-card{background:var(--bg2);border-radius:16px;box-shadow:var(--surface-shadow);padding:16px 18px;display:flex;flex-direction:column;gap:12px;min-width:0}
.sdash-card.host{background:linear-gradient(160deg,rgb(var(--accent-rgb) / .10),var(--bg2) 60%)}
.sdash-head{display:flex;align-items:center;gap:10px}
.sdash-head h3{margin:0;font-size:14px;font-weight:700;color:var(--text);flex:1;min-width:0}
.sdash-head .sdash-ico{width:30px;height:30px;border-radius:9px;background:rgb(var(--accent-rgb) / .14);color:var(--accent-l);display:flex;align-items:center;justify-content:center;flex-shrink:0}
.sdash-toggle{display:flex;align-items:center;gap:8px;font-size:11.5px;color:var(--muted);cursor:pointer;user-select:none}
.sdash-switch{position:relative;width:34px;height:20px;flex-shrink:0}
.sdash-switch input{position:absolute;opacity:0;width:100%;height:100%;margin:0;cursor:pointer}
.sdash-switch .sdash-knob{position:absolute;inset:0;border-radius:10px;background:rgb(var(--ink) / .16);transition:background .15s}
.sdash-switch .sdash-knob::after{content:'';position:absolute;top:3px;left:3px;width:14px;height:14px;border-radius:7px;background:var(--bg);transition:transform .15s}
.sdash-switch input:checked + .sdash-knob{background:var(--accent-fill)}
.sdash-switch input:checked + .sdash-knob::after{transform:translateX(14px)}
.sdash-switch input:focus-visible + .sdash-knob{box-shadow:0 0 0 2px rgb(var(--accent-rgb) / .45)}
.sdash-creds{display:flex;gap:10px;flex-wrap:wrap}
.sdash-cred{flex:1;min-width:150px;display:flex;flex-direction:column;gap:4px;padding:10px 12px;border-radius:12px;background:rgb(var(--ink) / .045);container-type:inline-size}
.sdash-cred .help-cred-label{font-size:10.5px}
.sdash-cred .help-cred-value{font-size:clamp(13px,10.5cqi,26px)}
.sdash-cred .help-cred-row{gap:8px}
.sdash-cred .help-cred-copy{width:28px;height:28px}
.sdash-status{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--muted);min-height:18px;flex-wrap:wrap}
.sdash-status .sdash-text{flex:1;min-width:0}
.sdash-dot{width:8px;height:8px;border-radius:4px;flex-shrink:0;background:var(--muted2)}
.sdash-dot.online{background:var(--success)}
.sdash-dot.connecting{background:var(--warn);animation:helpPulse 1.2s infinite}
.sdash-dot.offline{background:var(--danger)}
.sdash-dot.session{background:var(--danger);animation:helpPulse 1.2s infinite}
.sdash-notice{font-size:12px;color:var(--warn);line-height:1.45}
.sdash-scam{display:flex;gap:10px;align-items:flex-start;padding:10px 12px;border-radius:12px;background:rgb(var(--warn-rgb) / .12);font-size:11.5px;color:var(--text);line-height:1.5}
.sdash-scam .help-scam-icon{color:var(--warn);flex-shrink:0}
.sdash-scam b{display:block;color:var(--warn);font-size:12.5px;margin-bottom:2px}
.sdash-scam .btn-sm{margin-top:6px}
.sdash-off{font-size:12px;color:var(--muted);line-height:1.5;padding:10px 12px;border-radius:12px;background:rgb(var(--ink) / .04)}
.sdash-foot{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.sdash-foot .sdash-spacer{flex:1}
.sdash-link{background:none;border:none;padding:4px 2px;color:var(--accent-l);font-size:12px;font-weight:600;cursor:pointer}
.sdash-link:hover{text-decoration:underline}
.sdash-form{display:flex;flex-direction:column;gap:10px}
.sdash-form .sup-row2{gap:10px}
.sdash-form .sup-digits{font-size:17px!important;padding:8px 10px!important}
.sdash-form .btn-primary{width:auto;padding:9px 20px}
.sdash-srv{font-size:11.5px;color:var(--muted);display:flex;align-items:center;gap:8px}
.sdash-srv select{flex:1;min-width:0}
`

const supDashJS = `
// ── Oblireach Support cards on the Dashboard (support_dashboard.go) ─────────
// draft: what the operator typed in the Connect card (memory only). The
// Dashboard is rebuilt by the live device refresh: the card is drawn again
// from this draft, focus and caret included, so typing is never lost.
var SDASH = { hostKey: '', connKey: '', draft: { id: '', code: '', focus: '', s0: 0, s1: 0 } };
var SDASH_PC_ICON = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>';

function supDashOn() { return typeof currentRoute !== 'undefined' && currentRoute === 'dashboard' && !!document.getElementById('sdash'); }

// The cards, at the top of the Dashboard's body.
function supDashRender(body) {
  if (typeof reachDetachedMode !== 'undefined' && reachDetachedMode) return;
  var row = document.createElement('div'); row.className = 'sdash'; row.id = 'sdash';
  var host = document.createElement('div'); host.className = 'sdash-card host'; host.id = 'sdash-host';
  var conn = document.createElement('div'); conn.className = 'sdash-card'; conn.id = 'sdash-conn';
  row.appendChild(host); row.appendChild(conn);
  body.appendChild(row);
  SDASH.hostKey = ''; SDASH.connKey = '';
  supDashHost(host);
  supDashConn(conn);
  if (SUP.servers === null) supLoadServers().then(function () { supDashRefresh(true); });
  helpRefresh();
}
// Drawn again when what they show changed (conn: the servers changed too).
function supDashRefresh(conn) {
  if (!supDashOn()) return;
  supDashHost(document.getElementById('sdash-host'));
  if (conn) { SDASH.connKey = ''; supDashConn(document.getElementById('sdash-conn')); }
}

function supDashHead(card, titleKey, icon) {
  var head = document.createElement('div'); head.className = 'sdash-head';
  var ico = document.createElement('span'); ico.className = 'sdash-ico'; ico.innerHTML = icon;
  var h = document.createElement('h3'); h.textContent = T(titleKey);
  head.appendChild(ico); head.appendChild(h);
  card.appendChild(head);
  return head;
}

// ── This computer ──
function supDashHost(card) {
  if (!card) return;
  var st = HELP.st || { phase: 'idle', incoming: true };
  var on = !!st.incoming, ph = st.phase || 'idle';
  var key = [on, !!st.scamAck, ph, st.id, st.code, st.notice, st.errKind, st.server, st.link || '', HELP.copied, HELP.err,
    st.ended ? st.ended.reason + st.ended.seconds : '', st.session ? st.session.operator + (st.session.perms && st.session.perms.control) : '',
    helpReportKey(st)].join('|');
  if (key !== SDASH.hostKey) {
    SDASH.hostKey = key;
    card.innerHTML = '';
    var head = supDashHead(card, 'support.dash.hostTitle', SDASH_PC_ICON);
    var tg = document.createElement('label'); tg.className = 'sdash-toggle'; tg.title = T('support.dash.allow');
    var sw = document.createElement('span'); sw.className = 'sdash-switch';
    var cb = document.createElement('input'); cb.type = 'checkbox'; cb.id = 'sdash-incoming'; cb.checked = on;
    cb.setAttribute('role', 'switch'); cb.setAttribute('aria-label', T('support.dash.allow'));
    var knob = document.createElement('span'); knob.className = 'sdash-knob';
    sw.appendChild(cb); sw.appendChild(knob);
    var tl = document.createElement('span'); tl.textContent = T('support.dash.allow');
    tg.appendChild(tl); tg.appendChild(sw);
    cb.addEventListener('change', function () { helpSetIncoming(!!cb.checked, false); });
    head.appendChild(tg);
    if (!on) {
      var off = document.createElement('div'); off.className = 'sdash-off'; off.id = 'sdash-off'; off.textContent = T('support.dash.off');
      card.appendChild(off);
    } else {
      if (!st.scamAck) card.appendChild(supDashScam());
      supDashHostBody(card, st, ph);
    }
    if (HELP.err) { var e = document.createElement('div'); e.className = 'sdash-notice'; e.id = 'sdash-err'; e.textContent = helpErrText(HELP.err); card.appendChild(e); }
    var foot = document.createElement('div'); foot.className = 'sdash-foot';
    var sp = document.createElement('span'); sp.className = 'sdash-spacer';
    var det = document.createElement('button'); det.className = 'sdash-link'; det.id = 'sdash-details'; det.textContent = T('support.dash.details');
    det.addEventListener('click', function () { navigateTo('help'); });
    foot.appendChild(sp); foot.appendChild(det);
    card.appendChild(foot);
  }
  supDashHostLive(st);
}
function supDashScam() {
  var d = document.createElement('div'); d.className = 'sdash-scam'; d.id = 'sdash-scam';
  var i = document.createElement('span'); i.className = 'help-scam-icon'; i.innerHTML = HELP_WARN_ICON;
  var t = document.createElement('div');
  var b = document.createElement('b'); b.textContent = T('help.scam.title');
  var p = document.createElement('span'); p.textContent = T('help.scam.text');
  var ok = document.createElement('button'); ok.className = 'btn-sm'; ok.id = 'sdash-scam-ok'; ok.textContent = T('support.dash.scamOk');
  ok.addEventListener('click', helpScamAck);
  t.appendChild(b); t.appendChild(p); t.appendChild(document.createElement('br')); t.appendChild(ok);
  d.appendChild(i); d.appendChild(t);
  return d;
}
function supDashStatus(card, dot, text, id) {
  var s = document.createElement('div'); s.className = 'sdash-status'; s.id = id || 'sdash-status';
  var d = document.createElement('span'); d.className = 'sdash-dot ' + dot;
  var tx = document.createElement('span'); tx.className = 'sdash-text'; tx.id = 'sdash-status-text'; tx.textContent = text;
  s.appendChild(d); s.appendChild(tx);
  card.appendChild(s);
  return s;
}
function supDashHostBody(card, st, ph) {
  var server = st.server ? supServerLabel({ id: st.serverId, label: st.server }) : '';
  if (ph === 'session' || ph === 'starting') {
    var s = st.session || {};
    var name = helpOperatorName(st);
    supDashStatus(card, 'session', ph === 'starting' ? T('help.starting', { name: name })
      : T(s.perms && s.perms.control ? 'help.banner.control' : 'help.banner.view', { name: name }));
    var acts = document.createElement('div'); acts.className = 'sdash-foot';
    var end = document.createElement('button'); end.className = 'btn-sm help-danger'; end.id = 'sdash-end'; end.textContent = T('help.end');
    end.addEventListener('click', helpEndSession);
    acts.appendChild(end);
    if (ph === 'session' && helpCanReport(st)) acts.appendChild(helpReportButton('sdash-report', false));
    card.appendChild(acts);
    if (helpReportSent(st)) card.appendChild(helpReportDoneLine('sdash-reported'));
    return;
  }
  if (ph === 'waiting' || ph === 'approval') {
    var creds = document.createElement('div'); creds.className = 'sdash-creds';
    HELP.credPrefix = 'sdash-my';
    var a = helpCred('help.yourId', st.id || '', 'id'), b = helpCred('help.yourCode', st.code || '', 'code');
    HELP.credPrefix = '';
    a.className = b.className = 'sdash-cred';
    creds.appendChild(a); creds.appendChild(b);
    card.appendChild(creds);
    // The link dropped: supportlink reconnects and keeps the ID (shown).
    var re = st.link === 'connecting';
    var stl = supDashStatus(card, re ? 'connecting' : 'online', T(re ? 'support.dash.reconnecting' : 'support.dash.online', { server: server }));
    var nc = document.createElement('button'); nc.className = 'btn-sm'; nc.id = 'sdash-newcode'; nc.textContent = T('support.dash.newCode');
    nc.disabled = ph !== 'waiting';
    nc.addEventListener('click', helpNewCode);
    stl.appendChild(nc);
    if (st.notice === 'ended' && st.ended) card.appendChild(helpEndedLine(st, false));
    else if (st.notice) { var n = document.createElement('div'); n.className = 'sdash-notice'; n.id = 'sdash-notice'; n.textContent = T(HELP_NOTICE[st.notice] || 'help.notice.setupFailed'); card.appendChild(n); }
    return;
  }
  if (ph === 'unreachable') {
    var u = supDashStatus(card, 'offline', '');
    var rb = document.createElement('button'); rb.className = 'btn-sm'; rb.id = 'sdash-retry'; rb.textContent = T('support.dash.retry');
    rb.addEventListener('click', helpRetry);
    u.appendChild(rb);
    return;
  }
  supDashStatus(card, 'connecting', T('support.dash.connecting', { server: server || T('support.publicServer') }));
}
// The countdown, in place.
function supDashHostLive(st) {
  if (st.phase !== 'unreachable' || !st.incoming) return;
  var tx = document.getElementById('sdash-status-text');
  if (!tx) return;
  tx.textContent = st.errKind && st.errKind !== 'unreachable' ? T(HELP_UNREACH[st.errKind] || HELP_UNREACH.unreachable, { s: st.retryIn | 0 })
    : T('support.dash.offline', { s: st.retryIn | 0 });
}

// ── Connect to another computer ──
function supDashConn(card) {
  if (!card) return;
  var servers = SUP.servers || [];
  var sv = supPickServer();
  var key = [SUP.servers === null, SUP.storageError, servers.map(function (s) { return s.id + s.canConnect + s.hasKey + JSON.stringify(s.account || null); }).join(','),
    sv ? sv.id : '', sv && SUP.info[sv.id] ? SUP.info[sv.id].accounts : ''].join('|');
  if (key === SDASH.connKey) return;
  SDASH.connKey = key;
  var keepId = document.getElementById('sdash-id'), keepCode = document.getElementById('sdash-code');
  var idv = keepId && keepId.parentElement ? keepId.value : '', codev = keepCode && keepCode.parentElement ? keepCode.value : '';
  card.innerHTML = '';
  supDashHead(card, 'support.dash.connTitle', SUP_ICON);
  if (SUP.servers === null) { var l = document.createElement('div'); l.className = 'muted-hint'; l.textContent = T('common.loading'); card.appendChild(l); return; }
  if (!servers.length) {
    var ns = document.createElement('div'); ns.className = 'sdash-off'; ns.textContent = T(SUP.storageError ? 'support.err.storage' : 'support.err.noServer');
    card.appendChild(ns);
    return;
  }
  var form = document.createElement('div'); form.className = 'sdash-form';
  if (servers.length > 1) {
    var srow = document.createElement('div'); srow.className = 'sdash-srv';
    var lab = document.createElement('span'); lab.textContent = T('support.field.server');
    var sel = document.createElement('select'); sel.id = 'sdash-server';
    servers.forEach(function (s) { var o = document.createElement('option'); o.value = s.id; o.textContent = supServerOption(s); sel.appendChild(o); });
    if (sv) sel.value = sv.id;
    sel.addEventListener('change', function () { SUP.serverId = sel.value; supSetPref(SUP_SERVER_KEY, sel.value); SDASH.connKey = ''; supDashConn(card); });
    srow.appendChild(lab); srow.appendChild(sel);
    form.appendChild(srow);
  }
  var row = document.createElement('div'); row.className = 'sup-row2';
  var idF = cxnField('support.field.id'); idF.input.id = 'sdash-id'; idF.input.className = 'sup-digits';
  var codeF = cxnField('support.field.code'); codeF.input.id = 'sdash-code'; codeF.input.className = 'sup-digits';
  [idF.input, codeF.input].forEach(function (i) {
    i.setAttribute('inputmode', 'numeric'); i.setAttribute('autocomplete', 'off'); i.setAttribute('spellcheck', 'false');
    i.setAttribute('maxlength', '11'); i.placeholder = '123 456 789';
  });
  idF.input.value = idv || SDASH.draft.id || SUP.id || ''; codeF.input.value = codev || SDASH.draft.code;
  [[idF.input, 'id'], [codeF.input, 'code']].forEach(function (pair) {
    var inp = pair[0], which = pair[1];
    var keep = function () {
      SDASH.draft[which] = inp.value;
      if (document.activeElement === inp) { SDASH.draft.focus = inp.id; SDASH.draft.s0 = inp.selectionStart || 0; SDASH.draft.s1 = inp.selectionEnd || 0; }
    };
    // input: after the digit grouping of supWireDigits (registered later).
    inp.addEventListener('input', function () { setTimeout(keep, 0); });
    ['keyup', 'click', 'focus', 'select'].forEach(function (ev) { inp.addEventListener(ev, keep); });
    // A real move elsewhere forgets the focus; a rebuild of the card (the
    // element just leaves the document) keeps it for the restore below.
    inp.addEventListener('blur', function () {
      setTimeout(function () { if (inp.isConnected && SDASH.draft.focus === inp.id) SDASH.draft.focus = ''; }, 0);
    });
  });
  row.appendChild(idF.wrap); row.appendChild(codeF.wrap);
  form.appendChild(row);
  var can = supCanConnect(sv);
  form.classList.toggle('sup-locked', !can);
  idF.input.disabled = !can; codeF.input.disabled = !can;
  var acts = document.createElement('div'); acts.className = 'sdash-foot';
  var go = document.createElement('button'); go.className = 'btn-primary'; go.id = 'sdash-go'; go.textContent = T('common.connect');
  go.disabled = !can;
  var sp = document.createElement('span'); sp.className = 'sdash-spacer';
  var full = document.createElement('button'); full.className = 'sdash-link'; full.id = 'sdash-conn-page'; full.textContent = T('support.dash.connPage');
  full.addEventListener('click', function () { navigateTo('support'); });
  acts.appendChild(go); acts.appendChild(sp); acts.appendChild(full);
  form.appendChild(acts);
  card.appendChild(form);
  card.appendChild(supAuthBox(sv, function () { SDASH.connKey = ''; supDashConn(card); }));
  supWireDigits(idF.input, function (host) {
    var hit = servers.find(function (s) { return supHostOf(s.url) === host; });
    if (hit && (!sv || hit.id !== sv.id)) { SUP.serverId = hit.id; SDASH.connKey = ''; supDashConn(card); }
  });
  supWireDigits(codeF.input);
  var fid = SDASH.draft.focus;
  var fi = fid === 'sdash-id' ? idF.input : fid === 'sdash-code' ? codeF.input : null;
  if (fi && !fi.disabled && (!document.activeElement || document.activeElement === document.body)) {
    setTimeout(function () {
      if (!fi.isConnected) return;
      try { fi.focus(); var n = fi.value.length; fi.setSelectionRange(Math.min(SDASH.draft.s0, n), Math.min(SDASH.draft.s1, n)); } catch (x) {}
    }, 0);
  }
  idF.input.addEventListener('keydown', function (e) { if (e.key === 'Enter') { try { codeF.input.focus(); } catch (x) {} } });
  codeF.input.addEventListener('keydown', function (e) { if (e.key === 'Enter') go.click(); });
  // Connect: on the Connect by code page (its waiting screen, the SAS).
  go.addEventListener('click', function () {
    if (!supCanConnect(sv) || SUP.phase !== 'form') return;
    var f = { id: idF.input.value, code: codeF.input.value, server: sv.id, name: supDefaultOperatorName() };
    codeF.input.value = '';
    SDASH.draft.code = ''; SDASH.draft.focus = '';
    SUP.serverId = sv.id; SUP.id = supFmt(f.id); SUP.err = null;
    navigateTo('support', { id: supDigits(f.id) });
    supStart(f, document.getElementById('sup-err'));
  });
}
`
