package main

// support_page.go — "Connect by code" in the page (Go: support.go): the
// operator types the ID and the one-time code an Oblireach Support user reads
// to them, picks a support server (Settings > Support: label, address,
// operator key — the key never comes back to the page), and waits for the
// person to accept while both screens show the same verification code (SAS).
// The ID, the code and Connect stay greyed out until the operator is logged
// in on the chosen server (an account: Log in / Register, support_account.go)
// or has an operator key for it (private servers); the Dashboard's card
// (support_dashboard.go) offers the same form.
// Accepted, the session opens as one more ObliReach tab: its "device" is a
// pseudo-device of its own (_pid SUPPORT_PID, _support {...}), its picture
// the usual remote viewer (reach_viewer.go) over /local/support/tunnel/<token>.
//
// What differs from an ObliReach tab of a server's device:
//   - no reconnection, ever: the code is spent. Its end (closed by the
//     operator, ended by the person, link lost) shows why, and "Connect
//     with a new code" (the form, its ID filled in);
//   - not detachable (its link lives in this window's tunnel), no Windows
//     session picker, no Scripts / Info / Chat / Files of a server, no
//     password vault (its entries belong to servers' devices);
//   - nothing is recorded (no recents: an ID is ephemeral, a code single use);
//   - a chip says the network path (direct or through the server's relay).
//
// Both consts are injected verbatim after the other tool blocks (ui.go
// buildUI, not through Sprintf: '%' is safe); no backtick here.

const supportCSS = `
/* ── Connect by code (support_page.go) ── */
.sup-wrap{max-width:560px;display:flex;flex-direction:column;gap:14px}
.sup-form{display:flex;flex-direction:column;gap:12px}
.sup-row2{display:flex;gap:12px}
.sup-row2 .cxn-field{flex:1;min-width:0}
.sup-digits{font-size:20px!important;font-weight:600;letter-spacing:2px;font-variant-numeric:tabular-nums;padding:10px 12px!important}
.sup-form .cxn-field input:focus,.sup-form .cxn-field select:focus{box-shadow:0 0 0 2px var(--accent)}
.sup-actions{display:flex;gap:8px;align-items:center}
.sup-actions .btn-primary{width:auto;padding:10px 22px}
.sup-note{padding:12px 14px;border-radius:12px;background:rgb(var(--ink) / .05);font-size:12px;color:var(--muted);line-height:1.5;display:flex;flex-direction:column;gap:8px;align-items:flex-start}
.sup-err{display:none;padding:11px 14px;border-radius:12px;background:rgb(var(--danger-rgb) / .10);color:var(--danger);font-size:12px;line-height:1.5}
.sup-err.visible{display:block}
.sup-err .sup-err-title{font-weight:700;margin-bottom:2px}
.sup-err.serious{background:rgb(var(--danger-rgb) / .18);padding:14px 16px}
.sup-err.serious .sup-err-title{font-size:14px}
.sup-wait{display:flex;flex-direction:column;align-items:center;gap:12px;padding:26px 18px;text-align:center}
.sup-wait-title{font-size:13px;color:var(--muted)}
.sup-sas{font-size:46px;font-weight:700;letter-spacing:6px;color:var(--text);font-variant-numeric:tabular-nums;padding:10px 26px;border-radius:16px;background:rgb(var(--accent-rgb) / .10)}
.sup-wait-sub{font-size:12px;color:var(--muted2);max-width:420px;line-height:1.5}
.sup-chip{display:inline-flex;align-items:center;gap:5px;padding:2px 9px;border-radius:10px;font-size:10.5px;font-weight:700;white-space:nowrap;background:rgb(var(--success-rgb) / .14);color:var(--success)}
.sup-chip.relay{background:rgb(var(--warn-rgb) / .14);color:var(--warn)}
.sup-chip.srv{background:rgb(var(--ink) / .07);color:var(--muted)}
.sup-srv-row{display:flex;align-items:center;gap:10px;padding:11px 12px;border-radius:12px;background:rgb(var(--ink) / .03);margin-bottom:6px}
.sup-srv-text{display:flex;flex-direction:column;gap:2px;min-width:0;flex:1}
.sup-srv-label{font-weight:600;font-size:12.5px;color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sup-srv-url{font-size:11px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sup-srv-row .btn-ghost{padding:6px 11px}
.sup-check{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--text)}
.sup-locked .cxn-field input:disabled,.sup-locked .btn-primary:disabled{opacity:.45;cursor:not-allowed}
.sup-auth{display:flex;align-items:center;gap:8px;flex-wrap:wrap;padding:10px 12px;border-radius:12px;background:rgb(var(--ink) / .04);font-size:12px;color:var(--muted);line-height:1.45}
.sup-auth.locked{background:rgb(var(--accent-rgb) / .08);color:var(--text)}
.sup-auth .sup-auth-text{flex:1;min-width:160px}
.sup-auth .sup-auth-who{color:var(--text);font-weight:600}
.sup-auth .sup-auth-btns{display:flex;gap:6px}
.sup-auth .btn-primary{width:auto;padding:7px 16px;font-size:12px}
.sup-auth .sup-auth-err{flex-basis:100%;color:var(--danger);font-size:11.5px}
.sup-srv-acct{font-size:11px;color:var(--muted);margin-top:1px}
.sup-srv-acct b{color:var(--text);font-weight:600}
`

const supportJS = `
// ── Connect by code (support_page.go; Go: support.go) ───────────────────────
var SUPPORT_PID = '__support__';
var SUP_POLL_MS = 400;
var SUP_NAME_KEY = 'oblireach_support_name';
var SUP_SERVER_KEY = 'oblireach_support_server';
// A setup's failure (support.go supportErrCode) in words.
var SUP_ERR = {
  wrong_code: 'support.err.wrongCode', id_locked: 'support.err.idLocked', unknown_id: 'support.err.unknownId',
  rejected: 'support.err.rejected', expired: 'support.err.expired', busy: 'support.err.busy',
  rate_limited: 'support.err.rateLimited', key_refused: 'support.err.keyRefused', banned: 'support.err.banned',
  upgrade_required: 'support.err.upgradeRequired', app_too_old: 'support.err.appTooOld', server_too_old: 'support.err.serverTooOld', peer_gone: 'support.err.peerGone', invalid_id: 'support.err.invalidId',
  invalid_code: 'support.err.invalidCode', timeout: 'support.err.timeout', relay_quota: 'support.err.relayQuota',
  server_error: 'support.err.serverError', url_invalid: 'support.err.urlInvalid', unreachable: 'support.err.unreachable',
  network: 'support.err.network', login_required: 'support.err.loginRequired', no_server: 'support.err.noServer',
  too_many: 'support.err.tooMany', mitm: 'support.err.mitm', storage: 'support.err.storage', failed: 'support.err.failed',
  auth_expired: 'support.err.authExpired', quota: 'support.err.quota', quota_concurrent: 'support.err.quotaConcurrent',
  quota_daily: 'support.err.quotaDaily', quota_relay: 'support.err.quotaRelay', unavailable: 'support.err.unavailable',
};
// The account requests' refusals (support_account.go supportAccountError).
var SUP_ACCT_ERR = {
  invalid_credentials: 'support.acct.err.invalidCredentials', email_not_verified: 'support.acct.err.emailNotVerified',
  rate_limited: 'support.acct.err.rateLimited', accounts_disabled: 'support.acct.err.accountsDisabled',
  no_register: 'support.acct.err.noRegister', unreachable: 'support.acct.err.unreachable', server_error: 'support.acct.err.serverError',
  missing: 'support.acct.err.missing', refused: 'support.acct.err.refused', browser: 'support.acct.err.browser',
  url_invalid: 'support.err.urlInvalid', no_server: 'support.err.noServer', storage: 'support.err.storage', failed: 'support.acct.err.failed',
};
// The settings' refusals (support.go supportSettingsError).
var SUP_SET_ERR = {
  url_invalid: 'support.set.err.urlInvalid', url_plain: 'support.set.err.urlPlain',
  key_invalid: 'support.set.err.keyInvalid', too_many: 'support.set.err.tooMany', not_found: 'support.set.err.notFound',
  builtin: 'support.set.err.builtin',
  storage: 'support.err.storage',
};
var SUP_ICON = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="4"/><line x1="4.93" y1="4.93" x2="9.17" y2="9.17"/><line x1="14.83" y1="14.83" x2="19.07" y2="19.07"/><line x1="14.83" y1="9.17" x2="19.07" y2="4.93"/><line x1="4.93" y1="19.07" x2="9.17" y2="14.83"/></svg>';

// The page's state, kept across navigation (a setup goes on while another
// page is on screen; its tab opens wherever the operator is).
var SUP = { servers: null, defaultName: '', storageError: false, phase: 'form', conn: null, sas: '', err: null,
  id: '', serverId: '', timer: null, seq: 0, startedId: '', info: {}, verified: {} };

// The built-in public server (support.go supportPublicID): always listed,
// its name in the page's language, its address fixed, not removable.
var SUP_PUBLIC_ID = '00000000000000b1';
function supServerLabel(s) { return s && (s.builtin || s.id === SUP_PUBLIC_ID) ? T('support.publicServer') : String((s && s.label) || ''); }
function supServerOption(s) {
  var l = supServerLabel(s), h = supHostOf(s.url);
  return l + (h && h !== l ? ' (' + h + ')' : '');
}

function devIsSupport(d) { return !!(d && d._pid === SUPPORT_PID && d._support && typeof d._support === 'object'); }
function supDigits(s) { return String(s === undefined || s === null ? '' : s).replace(/[^0-9]/g, ''); }
// "482913507" -> "482 913 507" (what is typed so far, 9 digits at most).
function supFmt(s) {
  var d = supDigits(s).slice(0, 9), out = '';
  for (var i = 0; i < d.length; i++) { if (i && i % 3 === 0) out += ' '; out += d[i]; }
  return out;
}
// An ID as pasted: "482 913 507", "482-913-507", "482913507@support.example.com".
function supParseId(text) {
  var s = String(text === undefined || text === null ? '' : text).trim(), host = '';
  var at = s.indexOf('@');
  if (at >= 0) { host = s.slice(at + 1).trim().toLowerCase(); s = s.slice(0, at); }
  return { id: supDigits(s).slice(0, 9), host: host.replace(/[^a-z0-9.:\-\[\]]/g, '') };
}
function supHostOf(u) { var m = /^[a-z]+:\/\/([^\/]+)/i.exec(String(u || '')); return m ? m[1].toLowerCase() : ''; }
function supPref(k, d) { try { var v = localStorage.getItem(k); return v === null ? d : v; } catch (e) { return d; } }
function supSetPref(k, v) { try { localStorage.setItem(k, v); } catch (e) {} }

// A digits field: grouped by 3 as typed, the caret kept after the same digit.
function supWireDigits(input, onPasteHost) {
  input.addEventListener('input', function () {
    var pos = typeof input.selectionStart === 'number' ? input.selectionStart : input.value.length;
    var before = supDigits(String(input.value).slice(0, pos)).length;
    input.value = supFmt(input.value);
    var p = 0, seen = 0;
    while (p < input.value.length && seen < before) { if (/[0-9]/.test(input.value[p])) seen++; p++; }
    try { input.setSelectionRange(p, p); } catch (e) {}
  });
  input.addEventListener('paste', function (ev) {
    var text = '';
    try { text = (ev.clipboardData || window.clipboardData).getData('text'); } catch (e) {}
    if (!text) return;
    ev.preventDefault();
    var r = supParseId(text);
    input.value = supFmt(r.id);
    if (r.host && onPasteHost) onPasteHost(r.host);
  });
}

async function supFetch(method, path, body) {
  var o = { method: method, headers: {} };
  if (body !== undefined) { o.headers['Content-Type'] = 'application/json'; o.body = JSON.stringify(body); }
  var r = await fetch('/local/support/' + path, o);
  var d = null;
  try { d = await r.json(); } catch (e) {}
  return { ok: r.ok, status: r.status, data: d || {} };
}
async function supLoadServers() {
  try {
    var r = await supFetch('GET', 'servers');
    SUP.servers = Array.isArray(r.data.servers) ? r.data.servers : [];
    SUP.defaultName = typeof r.data.defaultName === 'string' ? r.data.defaultName : '';
    SUP.storageError = r.data.error === 'storage';
  } catch (e) { SUP.servers = []; SUP.storageError = true; }
  return SUP.servers;
}
function supServerById(id) { return (SUP.servers || []).find(function (s) { return s.id === id; }) || null; }
function supDefaultServer() {
  var list = SUP.servers || [];
  return supServerById(supPref(SUP_SERVER_KEY, '')) || list.find(function (s) { return s.default; }) || list[0] || null;
}
function supDefaultOperatorName() {
  var saved = supPref(SUP_NAME_KEY, '');
  if (saved) return saved;
  if (typeof currentOperatorName !== 'undefined' && currentOperatorName) return String(currentOperatorName);
  return SUP.defaultName || '';
}
function supErrText(code) { return T(SUP_ERR[code] || 'support.err.failed'); }
function supCanConnect(s) { return !!(s && (s.canConnect || s.hasKey || (s.account && s.account.loggedIn))); }
function supLoggedIn(s) { return !!(s && s.account && s.account.loggedIn); }

// ── Accounts (Go: support_account.go) ──
function supAcctErrText(code, retry) { return T(SUP_ACCT_ERR[code] || 'support.acct.err.failed', { s: retry | 0 }); }
function supAcctWho(s) {
  var a = (s && s.account) || {};
  var email = String(a.email || ''), name = String(a.name || '');
  var who = name && email && name !== email ? T('support.acct.loggedInAs', { name: name, email: email }) : T('support.acct.loggedInEmail', { email: email || name });
  // An instance without mail could not verify the address: said.
  return a.unverified ? who + ' \u00B7 ' + T('support.acct.unverified') : who;
}
// What the server says of its accounts: {accounts: true | false | null, register}.
async function supAcctInfo(s) {
  if (!s) return null;
  if (SUP.info[s.id]) return SUP.info[s.id];
  var r = null;
  try { r = await supFetch('GET', 'account/info?server=' + encodeURIComponent(s.id)); } catch (e) { r = null; }
  var info = r && r.ok ? r.data : null;
  if (info && info.reachable !== false) SUP.info[s.id] = info;
  return info;
}
function supApplyServers(r) {
  if (r && r.data && Array.isArray(r.data.servers)) SUP.servers = r.data.servers;
  if (typeof supDashRefresh === 'function') supDashRefresh(true);
}
// Logged in once per run: the server asked whether the login still holds.
function supAcctVerify(s, then) {
  if (!supLoggedIn(s) || SUP.verified[s.id]) return;
  SUP.verified[s.id] = true;
  supFetch('POST', 'account/verify', { server: s.id }).then(function (r) {
    if (!r || !r.ok) return;
    var before = supLoggedIn(s);
    supApplyServers(r);
    if (before !== supLoggedIn(supServerById(s.id)) && then) then();
  }).catch(function () {});
}
async function supLogout(s, then) {
  var r = null;
  try { r = await supFetch('POST', 'account/logout', { server: s.id }); } catch (e) { r = null; }
  if (r && r.ok) supApplyServers(r);
  if (then) then();
}
// Register: the server's sign-up page in the system browser (Go opens it).
async function supRegister(s, onErr) {
  var r = null;
  try { r = await supFetch('POST', 'account/register', { server: s.id }); } catch (e) { r = null; }
  if (r && r.ok) return;
  var code = (r && r.data && r.data.error) || 'failed';
  if (code === 'accounts_disabled') { SUP.info[s.id] = { accounts: false, register: false }; }
  if (onErr) onErr(code);
}
// The login window: e-mail and password, sent to Go (never kept by the page).
function supOpenLogin(s, onDone) {
  var m = scriptsBaseModal('support.acct.loginTitle');
  var form = document.createElement('div'); form.className = 'cxn-form';
  var where = document.createElement('p'); where.className = 'desc'; where.id = 'sup-login-server';
  where.textContent = T('support.acct.loginOn', { server: supServerLabel(s) });
  var email = cxnField('support.acct.email', 'email'); email.input.id = 'sup-login-email'; email.input.setAttribute('autocomplete', 'username');
  email.input.setAttribute('maxlength', '254'); email.input.setAttribute('spellcheck', 'false');
  var pass = cxnField('support.acct.password', 'password'); pass.input.id = 'sup-login-password'; pass.input.setAttribute('autocomplete', 'current-password');
  pass.input.setAttribute('maxlength', '1024');
  var err = document.createElement('div'); err.className = 'modal-err'; err.id = 'sup-login-err';
  form.appendChild(where); form.appendChild(email.wrap); form.appendChild(pass.wrap); form.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var go = document.createElement('button'); go.className = 'btn-primary'; go.id = 'sup-login-go'; go.textContent = T('support.acct.login');
  var submit = async function () {
    var e = String(email.input.value || '').trim(), p = String(pass.input.value || '');
    if (!e || !p) { err.textContent = supAcctErrText('missing'); return; }
    go.disabled = true; err.textContent = '';
    var r = null;
    try { r = await supFetch('POST', 'account/login', { server: s.id, email: e, password: p }); } catch (x) { r = null; }
    pass.input.value = '';
    go.disabled = false;
    if (!r || !r.ok) { err.textContent = supAcctErrText(r && r.data && r.data.error, r && r.data && r.data.retry); return; }
    SUP.verified[s.id] = true;
    supApplyServers(r);
    m.close();
    if (onDone) onDone();
  };
  go.addEventListener('click', submit);
  [email.input, pass.input].forEach(function (i) { i.addEventListener('keydown', function (ev) { if (ev && ev.key === 'Enter') submit(); }); });
  actions.appendChild(cancel); actions.appendChild(go);
  form.appendChild(actions);
  m.box.appendChild(form);
  setTimeout(function () { try { email.input.focus(); } catch (x) {} }, 0);
  return m;
}
// The account line of a server (the Dashboard's card, the page's form):
// logged in (who, Log out), an operator key, or Log in / Register (or, on a
// server without accounts, where its keys are set). onChange: drawn again.
function supAuthBox(s, onChange) {
  var box = document.createElement('div'); box.className = 'sup-auth'; box.id = 'sup-auth';
  var text = document.createElement('div'); text.className = 'sup-auth-text';
  var btns = document.createElement('div'); btns.className = 'sup-auth-btns';
  var err = document.createElement('div'); err.className = 'sup-auth-err'; err.id = 'sup-auth-err';
  box.appendChild(text); box.appendChild(btns);
  if (!s) return box;
  var mk = function (id, key, cls, fn) {
    var b = document.createElement('button'); b.className = cls || 'btn-sm'; b.id = id; b.textContent = T(key);
    b.addEventListener('click', fn); btns.appendChild(b); return b;
  };
  if (supLoggedIn(s)) {
    var who = document.createElement('span'); who.className = 'sup-auth-who'; who.id = 'sup-auth-who'; who.textContent = supAcctWho(s);
    text.appendChild(who);
    mk('sup-logout', 'support.acct.logout', 'btn-sm', function () { supLogout(s, onChange); });
    supAcctVerify(s, onChange);
    return box;
  }
  if (s.hasKey) { text.id = 'sup-auth-key'; text.textContent = T('support.acct.keyOnly'); return box; }
  box.className = 'sup-auth locked';
  var info = SUP.info[s.id];
  if (info && info.accounts === false) {
    text.id = 'sup-auth-keys-only'; text.textContent = T('support.acct.keysOnly');
    mk('sup-auth-settings', 'support.openSettings', 'btn-sm', function () { navigateTo('settings', { sub: 'support' }); });
    return box;
  }
  text.id = 'sup-auth-locked'; text.textContent = T('support.dash.locked');
  mk('sup-login', 'support.acct.login', 'btn-primary', function () { supOpenLogin(s, onChange); });
  mk('sup-register', 'support.acct.register', 'btn-sm', function () {
    err.textContent = '';
    supRegister(s, function (code) {
      err.textContent = supAcctErrText(code);
      if (!err.parentElement) box.appendChild(err);
      if (code === 'accounts_disabled' && onChange) onChange();
    });
  });
  if (!info) supAcctInfo(s).then(function (i) { if (i && i.accounts === false && onChange) onChange(); });
  return box;
}

// ── The page ──
function renderSupportPage(pc, params) {
  if (params && params.id) { SUP.id = supFmt(params.id); }
  var h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('support.title') + '</h1><div class="breadcrumb">' + TH('support.subtitle') + '</div>';
  pc.appendChild(h);
  var body = document.createElement('div'); body.className = 'page-body';
  var wrap = document.createElement('div'); wrap.className = 'sup-wrap'; wrap.id = 'sup-wrap';
  body.appendChild(wrap);
  pc.appendChild(body);
  if (SUP.servers === null) {
    wrap.innerHTML = '<div class="muted-hint">' + TH('common.loading') + '</div>';
    supLoadServers().then(function () { if (currentRoute === 'support') supRender(); });
    return;
  }
  supRender();
  supLoadServers().then(function () { if (currentRoute === 'support' && SUP.phase === 'form') supRender(); });
}
function supWrap() { return (typeof currentRoute !== 'undefined' && currentRoute === 'support') ? document.getElementById('sup-wrap') : null; }

function supRender() {
  var wrap = supWrap();
  if (!wrap) return;
  wrap.innerHTML = '';
  var panel = document.createElement('div'); panel.className = 'panel';
  var pb = document.createElement('div'); pb.className = 'panel-body';
  panel.appendChild(pb);
  wrap.appendChild(panel);
  if (SUP.phase === 'connecting' || SUP.phase === 'awaiting') { supRenderWait(pb); return; }
  supRenderForm(pb);
}

function supRenderWait(pb) {
  var w = document.createElement('div'); w.className = 'sup-wait';
  if (SUP.phase === 'awaiting' && SUP.sas) {
    var t = document.createElement('div'); t.className = 'sup-wait-title'; t.textContent = T('support.wait.readCode');
    var sas = document.createElement('div'); sas.className = 'sup-sas'; sas.id = 'sup-sas'; sas.textContent = SUP.sas;
    var sub = document.createElement('div'); sub.className = 'sup-wait-sub'; sub.textContent = T('support.wait.awaiting');
    w.appendChild(t); w.appendChild(sas); w.appendChild(sub);
  } else {
    var sp = document.createElement('div'); sp.className = 'reach-spinner';
    var t2 = document.createElement('div'); t2.className = 'sup-wait-title'; t2.id = 'sup-wait-title'; t2.textContent = T('support.wait.connecting');
    w.appendChild(sp); w.appendChild(t2);
  }
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'sup-cancel'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', supCancel);
  w.appendChild(cancel);
  pb.appendChild(w);
}

// The server a form starts on: the last one used, else the default.
function supPickServer() {
  return supServerById(SUP.serverId) || supDefaultServer();
}

function supRenderForm(pb) {
  var servers = SUP.servers || [];
  if (SUP.storageError || !servers.length) {
    var note = document.createElement('div'); note.className = 'sup-note'; note.id = 'sup-nokey';
    var p = document.createElement('div'); p.textContent = T(SUP.storageError ? 'support.err.storage' : 'support.err.noServer');
    var go = document.createElement('button'); go.className = 'btn-sm'; go.id = 'sup-open-settings'; go.textContent = T('support.openSettings');
    go.addEventListener('click', function () { navigateTo('settings', { sub: 'support' }); });
    note.appendChild(p); note.appendChild(go);
    pb.appendChild(note);
    if (!servers.length) return;
  }
  var usable = servers;
  var form = document.createElement('div'); form.className = 'sup-form';
  var row = document.createElement('div'); row.className = 'sup-row2';
  var idF = cxnField('support.field.id'); idF.input.id = 'sup-id'; idF.input.className = 'sup-digits';
  var codeF = cxnField('support.field.code'); codeF.input.id = 'sup-code'; codeF.input.className = 'sup-digits';
  [idF.input, codeF.input].forEach(function (i) {
    i.setAttribute('inputmode', 'numeric'); i.setAttribute('autocomplete', 'off'); i.setAttribute('spellcheck', 'false');
    i.setAttribute('maxlength', '11'); i.placeholder = '123 456 789';
  });
  idF.input.value = SUP.id || '';
  row.appendChild(idF.wrap); row.appendChild(codeF.wrap);
  form.appendChild(row);

  var srvF = cxnField('support.field.server', 'select'); srvF.input.id = 'sup-server';
  usable.forEach(function (s) {
    var o = document.createElement('option'); o.value = s.id;
    o.textContent = supServerOption(s);
    srvF.input.appendChild(o);
  });
  var pick = supPickServer();
  if (pick) srvF.input.value = pick.id; else if (usable[0]) srvF.input.value = usable[0].id;
  form.appendChild(srvF.wrap);
  var authSlot = document.createElement('div'); authSlot.id = 'sup-auth-slot';
  form.appendChild(authSlot);

  var nameF = cxnField('support.field.name'); nameF.input.id = 'sup-name'; nameF.input.setAttribute('maxlength', '64');
  nameF.input.value = supDefaultOperatorName();
  var hint = document.createElement('span'); hint.className = 'hint'; hint.textContent = T('support.field.nameHint');
  nameF.wrap.appendChild(hint);
  form.appendChild(nameF.wrap);

  var err = document.createElement('div'); err.className = 'sup-err'; err.id = 'sup-err';
  form.appendChild(err);
  if (SUP.err) supShowErr(err, SUP.err);

  var acts = document.createElement('div'); acts.className = 'sup-actions';
  var goBtn = document.createElement('button'); goBtn.className = 'btn-primary'; goBtn.id = 'sup-go'; goBtn.textContent = T('common.connect');
  acts.appendChild(goBtn);
  form.appendChild(acts);
  pb.appendChild(form);

  // Greyed out until logged in on the server (or a key for it).
  var gate = function () {
    var sv = supServerById(srvF.input.value);
    var on = supCanConnect(sv);
    form.classList.toggle('sup-locked', !on);
    idF.input.disabled = !on; codeF.input.disabled = !on; nameF.input.disabled = !on; goBtn.disabled = !on;
    authSlot.innerHTML = '';
    authSlot.appendChild(supAuthBox(sv, function () { if (supWrap() && SUP.phase === 'form') { SUP.serverId = srvF.input.value; supRender(); } }));
  };
  srvF.input.addEventListener('change', function () { SUP.serverId = srvF.input.value; gate(); });
  gate();
  supWireDigits(idF.input, function (host) {
    var hit = usable.find(function (s) { return supHostOf(s.url) === host; });
    if (hit) { srvF.input.value = hit.id; SUP.serverId = hit.id; gate(); }
  });
  supWireDigits(codeF.input);
  idF.input.addEventListener('keydown', function (e) { if (e.key === 'Enter') { try { codeF.input.focus(); } catch (x) {} } });
  codeF.input.addEventListener('keydown', function (e) { if (e.key === 'Enter') goBtn.click(); });
  goBtn.addEventListener('click', function () {
    supStart({ id: idF.input.value, code: codeF.input.value, server: srvF.input.value, name: nameF.input.value }, err);
  });
  setTimeout(function () { try { (supDigits(idF.input.value).length === 9 ? codeF.input : idF.input).focus(); } catch (e) {} }, 0);
}

// err: {code, retry}
function supShowErr(el, e) {
  if (!el) return;
  el.innerHTML = '';
  el.className = 'sup-err visible' + (e.code === 'mitm' ? ' serious' : '');
  var t = document.createElement('div'); t.className = 'sup-err-title';
  t.textContent = e.code === 'mitm' ? T('support.err.mitmTitle') : T('support.err.title');
  var p = document.createElement('div'); p.textContent = supErrText(e.code);
  el.appendChild(t); el.appendChild(p);
  if (e.code === 'key_refused') {
    var go = document.createElement('button'); go.className = 'btn-sm'; go.style.marginTop = '8px'; go.textContent = T('support.openSettings');
    go.addEventListener('click', function () { navigateTo('settings', { sub: 'support' }); });
    el.appendChild(go);
  }
  // Logged out by the server: log in again from here.
  var sv = supServerById(SUP.serverId);
  if ((e.code === 'auth_expired' || e.code === 'login_required') && sv) {
    var li = document.createElement('button'); li.className = 'btn-sm'; li.id = 'sup-err-login'; li.style.marginTop = '8px'; li.textContent = T('support.acct.login');
    li.addEventListener('click', function () { supOpenLogin(sv, function () { SUP.err = null; if (supWrap()) supRender(); }); });
    el.appendChild(li);
  }
}

async function supStart(f, errEl) {
  if (SUP.phase !== 'form') return;
  var id = supDigits(f.id), code = supDigits(f.code), sv = supServerById(f.server);
  SUP.id = supFmt(id);
  if (id.length !== 9) { supShowErr(errEl, { code: 'invalid_id' }); return; }
  if (code.length !== 9) { supShowErr(errEl, { code: 'invalid_code' }); return; }
  if (!sv) { supShowErr(errEl, { code: 'no_server' }); return; }
  if (!supCanConnect(sv)) { SUP.serverId = sv.id; supShowErr(errEl, { code: 'login_required' }); return; }
  var name = String(f.name || '').trim().slice(0, 64);
  supSetPref(SUP_NAME_KEY, name);
  supSetPref(SUP_SERVER_KEY, sv.id);
  SUP.serverId = sv.id; SUP.err = null; SUP.sas = ''; SUP.phase = 'connecting'; SUP.startedId = id;
  var seq = ++SUP.seq;
  supRender();
  var r = null;
  try { r = await supFetch('POST', 'connect', { server: sv.id, id: id, code: code, name: name }); } catch (e) { r = null; }
  if (seq !== SUP.seq) return;
  if (!r || !r.ok || !r.data.conn) { supFail({ code: (r && r.data && r.data.error) || 'failed' }); return; }
  SUP.conn = r.data.conn;
  supPoll(seq, sv);
}

function supPoll(seq, sv) {
  clearTimeout(SUP.timer);
  SUP.timer = setTimeout(async function () {
    if (seq !== SUP.seq || !SUP.conn) return;
    var r = null;
    try { r = await supFetch('GET', 'connect/' + encodeURIComponent(SUP.conn)); } catch (e) { r = null; }
    if (seq !== SUP.seq) return;
    if (!r || !r.ok) { supFail({ code: 'failed' }); return; }
    var st = r.data;
    if (st.state === 'awaiting') {
      if (SUP.phase !== 'awaiting' || SUP.sas !== st.sas) { SUP.phase = 'awaiting'; SUP.sas = String(st.sas || ''); supRender(); }
    } else if (st.state === 'ready') {
      var id = SUP.startedId;
      SUP.conn = null; SUP.phase = 'form'; SUP.err = null; SUP.seq++;
      supOpenTab({ token: st.token, path: st.path, sas: st.sas || SUP.sas, id: id, server: sv ? supServerLabel(sv) : '' });
      return;
    } else if (st.state === 'failed') {
      supFail({ code: st.error || 'failed', retry: st.retry | 0 });
      return;
    } else if (st.state === 'cancelled') {
      SUP.conn = null; SUP.phase = 'form'; supRender();
      return;
    }
    supPoll(seq, sv);
  }, SUP_POLL_MS);
}

function supFail(e) {
  SUP.conn = null; SUP.phase = 'form'; SUP.err = e; SUP.sas = ''; SUP.seq++;
  // Signed out by the server (Go forgot the token): the form asks to log in.
  if (e.code === 'auth_expired') supLoadServers().then(function () { if (supWrap() && SUP.phase === 'form') supRender(); if (typeof supDashRefresh === 'function') supDashRefresh(true); });
  if (supWrap()) supRender();
  else reachNotify(supErrText(e.code), 'warn'); // (another page on screen)
}

function supCancel() {
  var conn = SUP.conn;
  SUP.conn = null; SUP.phase = 'form'; SUP.sas = ''; SUP.seq++;
  clearTimeout(SUP.timer);
  if (conn) supFetch('POST', 'cancel', { conn: conn }).catch(function () {});
  supRender();
}

// ── The session tab ──
function supPathKind(p) { return p === 'direct' ? 'direct' : (p === 'relay_udp' || p === 'relay_tcp') ? 'relay' : ''; }
function supPathChip(p) {
  var k = supPathKind(p);
  if (!k) return null;
  var c = document.createElement('span'); c.className = 'sup-chip' + (k === 'relay' ? ' relay' : ''); c.id = 'sup-path-chip';
  c.textContent = T(k === 'relay' ? 'support.path.relay' : 'support.path.direct');
  c.title = T(k === 'relay' ? 'support.path.relayTip' : 'support.path.directTip');
  return c;
}
function supOpenTab(r) {
  var n = ++supTabSeq;
  var fid = supFmt(r.id);
  var dev = {
    id: 'sup' + n, _pid: SUPPORT_PID, uuid: '', _tools: false, osType: '', status: 'online', tenantId: 0,
    hostname: T('support.tabTitle', { id: fid }),
    oblireach: { installed: true, online: true, version: null, updateAvailable: false, sessions: [] },
    _support: { id: supDigits(r.id), server: String(r.server || ''), path: String(r.path || ''), sas: String(r.sas || ''), token: String(r.token || ''), ended: '' },
  };
  var tab = addSessionTab(dev);
  tab.label = dev.hostname;
  openDeviceDetail(dev, 'remote');
  supAttach(tab);
  return tab;
}
var supTabSeq = 0;

// Opens the tab's tunnel (once: its token is single use).
function supAttach(tab) {
  var s = tab.device._support, token = s.token;
  s.token = '';
  tab.noAutoReconnect = true; tab.userStopped = false; tab.reconnectAttempts = 0;
  if (tab.reconnectTimer) { clearTimeout(tab.reconnectTimer); tab.reconnectTimer = null; }
  reachResetConnState(tab);
  if (!token || !/^[A-Za-z0-9_-]{16,128}$/.test(token)) { s.ended = 'lost'; supConnClosed(tab); return; }
  reachSetStatus(tab, T('viewer.connecting'));
  reachShowOverlay(tab, 'busy', T('viewer.starting'), T('support.opening'), [reachActCancel(tab)]);
  reachShowStopButton(tab, true);
  var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  var ws = new WebSocket(proto + '//' + location.host + '/local/support/tunnel/' + encodeURIComponent(token));
  ws.binaryType = 'arraybuffer';
  tab.ws = ws;
  if (reachUI(tab)) remoteWs = ws;
  reachArmConnectTimer(tab, REACH.CONNECT_TIMEOUT_MS);
  ws.onopen = function () { if (tab.ws === ws) renderSessionTabs(); };
  ws.onclose = function (ev) {
    if (tab.ws !== ws) return;
    s.ended = tab.userStopped ? 'local' : (tab.lastError && tab.lastError.code) ? 'error'
      : (ev && ev.code === 1000 && ev.reason === 'host_ended') ? 'host' : 'lost';
    reachConnClosed(tab);
  };
  ws.onerror = function () { if (tab.ws === ws && !tab.gotInit) reachSetStatus(tab, T('viewer.wsError')); };
  ws.onmessage = function (ev) {
    if (tab.ws !== ws) return;
    if (typeof ev.data === 'string' && ev.data.indexOf('"init"') >= 0) supPeekInit(tab, ev.data);
    handleRemoteMessage(ev, tab);
  };
  renderSessionTabs();
}

// The peer's machine name, when its init says it: the tab's title.
function supPeekInit(tab, text) {
  var m = null;
  try { m = JSON.parse(text); } catch (e) { return; }
  if (!m || m.type !== 'init') return;
  var raw = typeof m.hostname === 'string' ? m.hostname : (typeof m.machine === 'string' ? m.machine : '');
  var name = String(raw).replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, '').trim().slice(0, 64);
  if (!name) return;
  var title = T('support.tabTitleName', { name: name });
  if (tab.label === title) return;
  tab.label = title; tab.device.hostname = title;
  var hn = document.getElementById('sup-head-name');
  if (hn && selectedDevice === tab.device) hn.textContent = title;
  renderSessionTabs();
}

// Its end (reach_viewer.go reachConnClosed): never a reconnection.
function supConnClosed(tab) {
  tab.overlay = null;
  reachShowIdleUI(tab);
  reachSetStatus(tab, T('viewer.disconnected'));
  supShowEnded(tab);
  renderSessionTabs();
}
function supShowEnded(tab) {
  var why = tab.device._support.ended || 'lost';
  var text = why === 'local' ? T('support.ended.local') : why === 'host' ? T('support.ended.host')
    : why === 'error' ? (reachErrorWhy(tab.lastError) || T('support.ended.lost')) : T('support.ended.lost');
  reachShowOverlay(tab, why === 'local' || why === 'host' ? 'info' : 'error', T('support.ended.title'),
    text + ' ' + T('support.ended.oneTime'), [supActNewCode(tab), supActCloseTab(tab)]);
}
// An error the host reported while the link is up (reach_viewer.go
// reachErrorOverlay): said, with Disconnect instead of Retry.
function supErrorOverlay(tab) {
  if (!tab.ws) { supShowEnded(tab); return; }
  var e = tab.lastError || {};
  var title = reachCodeText(REACH_ERROR_TEXT, e.code) || T('viewer.stopped');
  var detail = (e.message && e.message !== title) ? e.message : '';
  reachShowOverlay(tab, 'error', title, detail, [reachActDisconnect(tab)]);
}
function supActNewCode(tab) {
  return { label: T('support.newCode'), primary: true, run: function () { supNewCode(tab); } };
}
function supActCloseTab(tab) {
  return { label: T('common.close'), run: function () { if (sessionTabs.indexOf(tab) >= 0) closeSessionTab(tab.id); } };
}
// "Connect with a new code": the form, its ID filled in (the person's
// Oblireach Support keeps its ID until it is closed).
function supNewCode(tab) {
  var id = tab && tab.device && tab.device._support ? tab.device._support.id : '';
  if (tab && sessionTabs.indexOf(tab) >= 0 && !tab.ws) closeSessionTab(tab.id);
  navigateTo('support', id ? { id: id } : null);
}
// A start asked for a code tab (reach_viewer.go startRemote: Retry...): its
// tunnel if never opened, else its end.
function supRestart(tab) {
  if (tab.device._support.token) { supAttach(tab); return; }
  if (!tab.ws) supConnClosed(tab);
}

// Its page (ui.go selectDeviceInto): its name, its path, its server; the
// Remote pane below.
function supSelectInto(dev, area) {
  selectedDevice = dev;
  var chatBtn = document.getElementById('btn-chat');
  if (chatBtn && !(typeof chatConvs !== 'undefined' && chatConvs.size)) chatBtn.style.display = 'none';
  area.style.display = 'flex'; area.style.flexDirection = 'column'; area.style.overflow = 'hidden';
  var hdr = document.createElement('div'); hdr.className = 'device-header merged';
  var back = document.createElement('button'); back.className = 'btn-sm'; back.style.cssText = 'padding:5px 8px'; back.title = T('support.title');
  back.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 18 9 12 15 6"/></svg>';
  back.addEventListener('click', function () { navigateTo('support'); });
  hdr.appendChild(back);
  var dot = document.createElement('span'); dot.className = 'dot online'; dot.style.width = '9px'; dot.style.height = '9px';
  hdr.appendChild(dot);
  var h2 = document.createElement('h2'); h2.id = 'sup-head-name'; h2.textContent = dev.hostname;
  hdr.appendChild(h2);
  var chip = supPathChip(dev._support.path);
  if (chip) hdr.appendChild(chip);
  if (dev._support.server) { var sc = document.createElement('span'); sc.className = 'sup-chip srv'; sc.textContent = dev._support.server; hdr.appendChild(sc); }
  var meta = document.createElement('span'); meta.className = 'dev-meta'; meta.style.fontSize = '11px'; meta.style.color = 'var(--muted)';
  meta.textContent = T('support.headerMeta', { sas: dev._support.sas || '-' });
  hdr.appendChild(meta);
  area.appendChild(hdr);
  var tc = document.createElement('div'); tc.className = 'tab-content'; tc.id = 'tab-content';
  area.appendChild(tc);
  switchTab(activeTab, area);
}

// The Remote pane's toolbar (ui.go renderRemoteTab): "Connect with a new
// code" in place of Connect, its path chip in place of Detach.
function supToolbarStart(btn, dev) {
  btn.textContent = T('support.newCode');
  btn.addEventListener('click', function () {
    var tab = sessionTabs.find(function (t) { return devSame(t.device, dev); }) || null;
    supNewCode(tab || { device: dev });
  });
}
function supToolbarChip(toolbar, dev) {
  var chip = supPathChip(dev._support.path);
  if (chip) { chip.id = 'sup-tb-path'; toolbar.appendChild(chip); }
}

// "Connect by code" next to a page's own actions (Dashboard, Devices).
function supAddEntry(h) {
  if (typeof reachDetachedMode !== 'undefined' && reachDetachedMode) return;
  var acts = (h.querySelector && h.querySelector('.page-actions')) || null;
  if (!acts) { acts = document.createElement('div'); acts.className = 'page-actions'; h.appendChild(acts); }
  var b = document.createElement('button'); b.className = 'btn-sm'; b.id = 'sup-entry'; b.textContent = T('support.connectByCode');
  b.addEventListener('click', function () { navigateTo('support'); });
  if (acts.firstChild) acts.insertBefore(b, acts.firstChild); else acts.appendChild(b);
}

// ── Settings > Support ──
function renderSupportSection(sb) {
  var panel = document.createElement('div'); panel.className = 'panel';
  var head = document.createElement('div'); head.className = 'panel-header'; head.textContent = T('settings.nav.support');
  var pa = document.createElement('div'); pa.className = 'panel-actions';
  var add = document.createElement('button'); add.className = 'btn-sm'; add.id = 'sup-add'; add.textContent = T('support.set.add');
  add.addEventListener('click', function () { supOpenServerModal(null, function () { supSectionList(list); }); });
  pa.appendChild(add); head.appendChild(pa);
  var pb = document.createElement('div'); pb.className = 'panel-body';
  var hint = document.createElement('p'); hint.className = 'muted-hint'; hint.style.marginBottom = '12px'; hint.textContent = T('support.set.hint');
  var list = document.createElement('div'); list.id = 'sup-srv-list';
  list.innerHTML = '<div class="muted-hint" style="padding:14px">' + TH('common.loading') + '</div>';
  pb.appendChild(hint); pb.appendChild(list);
  panel.appendChild(head); panel.appendChild(pb);
  sb.appendChild(panel);
  supLoadServers().then(function () { supSectionList(list); });
}
function supSectionList(list) {
  list.innerHTML = '';
  if (SUP.storageError) { list.innerHTML = '<div class="muted-hint" style="padding:14px">' + TH('support.err.storage') + '</div>'; return; }
  var servers = SUP.servers || [];
  if (!servers.length) { list.innerHTML = '<div class="muted-hint" style="padding:14px">' + TH('support.set.empty') + '</div>'; return; }
  servers.forEach(function (s) {
    var row = document.createElement('div'); row.className = 'sup-srv-row';
    var tx = document.createElement('div'); tx.className = 'sup-srv-text';
    var l = document.createElement('div'); l.className = 'sup-srv-label'; l.textContent = supServerLabel(s);
    var u = document.createElement('div'); u.className = 'sup-srv-url'; u.textContent = s.url;
    // Its account: who is logged in, else whether a key is saved.
    var ac = document.createElement('div'); ac.className = 'sup-srv-acct'; ac.id = 'sup-srv-acct-' + s.id;
    if (supLoggedIn(s)) { var w = document.createElement('b'); w.textContent = supAcctWho(s); ac.appendChild(w); }
    else ac.textContent = T(s.hasKey ? 'support.acct.keyOnly' : 'support.acct.notLoggedIn');
    tx.appendChild(l); tx.appendChild(u); tx.appendChild(ac);
    row.appendChild(tx);
    if (s.builtin) { var bi = document.createElement('span'); bi.className = 'sup-chip srv'; bi.textContent = T('support.set.builtin'); row.appendChild(bi); }
    if (s.default) { var d = document.createElement('span'); d.className = 'sup-chip srv'; d.textContent = T('support.set.default'); row.appendChild(d); }
    if (s.hasKey) { var k = document.createElement('span'); k.className = 'sup-chip'; k.textContent = T('support.set.keySaved'); row.appendChild(k); }
    var redraw = function () { supSectionList(list); };
    var acct = document.createElement('button'); acct.className = 'btn-ghost'; acct.id = 'sup-srv-login-' + s.id;
    if (supLoggedIn(s)) {
      acct.textContent = T('support.acct.logout');
      acct.addEventListener('click', function () { supLogout(s, redraw); });
      row.appendChild(acct);
    } else if (!(SUP.info[s.id] && SUP.info[s.id].accounts === false)) {
      acct.textContent = T('support.acct.login');
      acct.addEventListener('click', function () { supOpenLogin(s, redraw); });
      row.appendChild(acct);
      var reg = document.createElement('button'); reg.className = 'btn-ghost'; reg.id = 'sup-srv-register-' + s.id; reg.textContent = T('support.acct.register');
      reg.addEventListener('click', function () {
        supRegister(s, function (code) { ac.textContent = supAcctErrText(code); if (code === 'accounts_disabled') setTimeout(redraw, 2500); });
      });
      row.appendChild(reg);
    }
    var edit = document.createElement('button'); edit.className = 'btn-ghost'; edit.textContent = T('support.set.edit');
    edit.addEventListener('click', function () { supOpenServerModal(s, function () { supSectionList(list); }); });
    var del = document.createElement('button'); del.className = 'btn-ghost'; del.textContent = T('support.set.remove');
    del.addEventListener('click', function () { supConfirmRemove(s, function () { supSectionList(list); }); });
    row.appendChild(edit);
    if (!s.builtin) row.appendChild(del); // (the built-in one stays: its key and default only)
    list.appendChild(row);
  });
}
function supSetErrText(code) { return T(SUP_SET_ERR[code] || 'support.err.failed'); }
function supOpenServerModal(s, onSaved) {
  var m = scriptsBaseModal(s ? 'support.set.editTitle' : 'support.set.addTitle');
  var form = document.createElement('div'); form.className = 'cxn-form';
  var label = cxnField('support.set.label'); label.input.id = 'sup-f-label'; label.input.value = s ? supServerLabel(s) : ''; label.input.setAttribute('maxlength', '60');
  var url = cxnField('support.set.url'); url.input.id = 'sup-f-url'; url.input.value = s ? s.url : ''; url.input.placeholder = 'https://support.example.com';
  url.input.setAttribute('spellcheck', 'false');
  if (s && s.builtin) { label.input.disabled = true; url.input.disabled = true; } // (fixed: support.go)
  var key = cxnField('support.set.key', 'password'); key.input.id = 'sup-f-key'; key.input.setAttribute('autocomplete', 'off');
  if (s && s.hasKey) key.input.placeholder = T('support.set.keyKeep');
  var keyHint = document.createElement('span'); keyHint.className = 'hint'; keyHint.textContent = T('support.set.keyHint');
  key.wrap.appendChild(keyHint);
  var defRow = document.createElement('label'); defRow.className = 'sup-check';
  var def = document.createElement('input'); def.type = 'checkbox'; def.id = 'sup-f-default'; def.checked = !!(s ? s.default : !(SUP.servers || []).length);
  var defTxt = document.createElement('span'); defTxt.textContent = T('support.set.makeDefault');
  defRow.appendChild(def); defRow.appendChild(defTxt);
  var err = document.createElement('div'); err.className = 'modal-err'; err.id = 'sup-f-err';
  form.appendChild(label.wrap); form.appendChild(url.wrap); form.appendChild(key.wrap); form.appendChild(defRow); form.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var save = document.createElement('button'); save.className = 'btn-primary'; save.id = 'sup-f-save'; save.textContent = T('connections.save');
  save.addEventListener('click', async function () {
    if (!String(url.input.value).trim()) { err.textContent = T('support.set.err.urlInvalid'); return; }
    save.disabled = true;
    var r = null;
    try { r = await supFetch('POST', 'servers/save', { id: s ? s.id : '', label: label.input.value, url: url.input.value, key: key.input.value, default: def.checked }); } catch (e) { r = null; }
    key.input.value = '';
    save.disabled = false;
    if (!r || !r.ok) { err.textContent = supSetErrText(r && r.data && r.data.error); return; }
    SUP.servers = Array.isArray(r.data.servers) ? r.data.servers : SUP.servers;
    m.close();
    onSaved();
  });
  actions.appendChild(cancel); actions.appendChild(save);
  form.appendChild(actions);
  m.box.appendChild(form);
  setTimeout(function () { try { (s ? key.input : url.input).focus(); } catch (e) {} }, 0);
}
function supConfirmRemove(s, onDone) {
  var m = scriptsBaseModal('support.set.removeTitle');
  var p = document.createElement('p'); p.className = 'desc'; p.textContent = T('support.set.removeConfirm', { name: supServerLabel(s) });
  var err = document.createElement('div'); err.className = 'modal-err';
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var ok = document.createElement('button'); ok.className = 'exp-btn-danger'; ok.id = 'sup-f-remove'; ok.textContent = T('support.set.remove');
  ok.addEventListener('click', async function () {
    var r = null;
    try { r = await supFetch('POST', 'servers/remove', { id: s.id }); } catch (e) { r = null; }
    if (!r || !r.ok) { err.textContent = supSetErrText(r && r.data && r.data.error); return; }
    SUP.servers = Array.isArray(r.data.servers) ? r.data.servers : SUP.servers;
    m.close(); onDone();
  });
  actions.appendChild(cancel); actions.appendChild(ok);
  m.box.appendChild(p); m.box.appendChild(err); m.box.appendChild(actions);
}
`
