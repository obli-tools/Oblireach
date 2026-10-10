package main

// vault_ui.go — the password vault's UI: a toolbar button + popover in every
// open Reach session (unlock/create, then a filterable list that types a
// saved secret through vault_bridge_windows.go's reachVaultType), the
// entry form, the reveal and delete dialogs, and Settings > Vault (the
// vault's settings only: Windows Hello, Ctrl+Alt+Del, lock). The entries
// themselves are managed on the Vault page (vault_page.go, main window
// only), built on the same bindings (vault_bridge_windows.go).
//
// Windows Hello (vault_hello_windows.go): when reachVaultState says it is
// available and turned on, the unlock forms offer it next to the code field
// (reachVaultUnlockHello: Windows' prompt, over this window); Settings >
// Vault has its switch (reachVaultHelloSet, main window). The code always
// stays: the fallback, and what Reveal asks.
//
// Every asynchronous binding (Unlock/Create/Type/Save/Reveal) answers later
// through window.__reachVaultEvent, not its own promise (vaultAsync,
// vault_bridge_windows.go) — mirrored here exactly the way local_pane.go's
// localFs() answers reachFs* through window.__reachLocalFs. The synchronous
// ones (State/ForSession/Lock/List/Delete) go straight through reachNative().
//
// No secret ever sits in a data attribute or a closure kept alive after its
// modal closes: reveal's value is cleared from the DOM the moment its modal
// is dismissed, and the type actions never see the secret at all (it is
// built into the type_text frame in Go, vault_bridge_windows.go).
//
// Both consts are injected verbatim after the other tool blocks (buildUI,
// not through Sprintf: '%' is safe): no backtick here.

const vaultCSS = `
/* ── Password vault (vault_ui.go) ── */
.vault-menu{width:270px;max-height:380px;overflow:auto}
.vault-search{width:100%;box-sizing:border-box;background:var(--input-bg);border:none;border-radius:8px;padding:7px 10px;font-size:11.5px;color:var(--text);outline:none;margin:2px 2px 6px;font-family:inherit}
.vault-entry-row{display:flex;align-items:center;gap:4px}
.vault-entry-row button.vault-entry-btn{flex:1;min-width:0;display:flex;flex-direction:column;align-items:flex-start;gap:1px;white-space:normal}
.vault-entry-btn .vault-entry-lbl{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.vault-entry-btn .vault-entry-sub{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:9.5px;color:var(--muted2)}
.vault-entry-row button.vault-entry-user{flex-shrink:0;width:26px;padding:6px 0!important;text-align:center!important;display:flex;align-items:center;justify-content:center}
.vault-form{display:flex;flex-direction:column;gap:9px;padding:2px}
.vault-field{display:flex;flex-direction:column;gap:4px}
.vault-field label{font-size:11px;color:var(--muted);font-weight:600}
.vault-row2{display:flex;gap:9px}
.vault-row2 .vault-field{flex:1;min-width:0}
.vault-field input,.vault-field textarea,.vault-field select{background:var(--input-bg);border:none;border-radius:10px;padding:8px 11px;font-size:12.5px;color:var(--text);outline:none;font-family:inherit;resize:vertical}
.vault-check-row{display:flex;align-items:flex-start;gap:8px;font-size:11.5px;color:var(--text)}
.vault-check-row input{margin-top:2px;accent-color:var(--accent);width:14px;height:14px;cursor:pointer;flex-shrink:0}
.vault-check-row .hint{display:block;font-size:10.5px;color:var(--muted2);margin-top:2px;font-weight:400}
.vault-manage-toolbar{display:flex;gap:8px;align-items:center;margin-bottom:10px}
.vault-manage-toolbar .vault-search{margin:0;flex:1}
.vault-entry-card{display:flex;align-items:center;gap:10px;padding:10px 12px;border-radius:10px;background:var(--bg2);margin-bottom:6px}
.vault-entry-card .info{flex:1;min-width:0}
.vault-entry-card .info .lbl{font-size:13px;font-weight:600;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.vault-entry-card .info .sub{font-size:11px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.vault-entry-card .acts{display:flex;gap:6px;flex-shrink:0}
.vault-hello-btn{display:flex;align-items:center;justify-content:center;gap:7px;width:100%;box-sizing:border-box}
.vault-hello-btn svg{flex-shrink:0}
.vault-hello-row{display:flex;align-items:flex-start;gap:10px;padding:10px 12px;border-radius:10px;background:var(--bg2);margin-bottom:10px}
.vault-hello-row .info{flex:1;min-width:0}
.vault-hello-row .info .lbl{font-size:12.5px;font-weight:600;color:var(--text)}
.vault-hello-row .info .sub{font-size:11px;color:var(--muted);margin-top:2px}
.vault-hello-row input{margin-top:2px;accent-color:var(--accent);width:16px;height:16px;cursor:pointer;flex-shrink:0}
.vault-field .hint{font-size:10.5px;color:var(--muted2);font-weight:400}
.vault-reveal-value{font-family:monospace;font-size:13px;background:var(--input-bg);border-radius:8px;padding:9px 11px;word-break:break-all;user-select:all}
`

const vaultJS = `
// ═════════════════════════════════════════════════════════════════════════
// The password vault (vault_ui.go; Go: vault*.go, vault_bridge_windows.go)
// ═════════════════════════════════════════════════════════════════════════
var vaultWaiting = new Map(); // reqId -> { resolve, reject, timer }
var VAULT_TIMEOUT_MS = 30000;
// Go's answer to an asynchronous call (vault_bridge_windows.go evalVault),
// pushed to every window (main and each detached session window alike).
window.__reachVaultEvent = function (evt) {
  if (!evt || evt.type !== 'done') return;
  var id = String(evt.reqID), w = vaultWaiting.get(id);
  if (!w) return;
  vaultWaiting.delete(id); clearTimeout(w.timer);
  if (evt.ok) w.resolve(evt.data || {}); else w.reject(vaultErrFrom(evt.data));
};
function vaultErrFrom(d) {
  var code = (d && typeof d.code === 'string') ? d.code : 'internal';
  var e = new Error(code); e.code = code;
  e.wait = (d && typeof d.seconds === 'number') ? d.seconds : 0;
  return e;
}
function vaultErrText(e) {
  var code = (e && e.code) || 'internal';
  if (code === 'wait') return T('vault.locked.wait', { seconds: (e && e.wait) || 0 });
  if (code === 'wrong_code') return T('vault.locked.wrongCode');
  if (code === 'scope' || code === 'scope_unknown' || code === 'session_changed') return T('vault.scopeUnknown');
  if (code === 'busy') return T('vault.err.busy');
  if (code === 'keyring_unavailable') return T('vault.err.keyring');
  if (code === 'closed' || code === 'unavailable') return T('vault.err.unavailable');
  if (code === 'locked') return T('vault.locked.title');
  if (code === 'rdp_unavailable') return T('connections.rdp.unavailable');
  if (code === 'rdp_timeout') return T('connections.rdp.timeout');
  if (code === 'hello_canceled') return T('vault.hello.canceled');
  if (code === 'hello_retries') return T('vault.hello.retries');
  if (code === 'hello_unavailable') return T('vault.hello.unavailable');
  if (code === 'hello_busy') return T('vault.hello.busy');
  if (code === 'hello_off') return T('vault.hello.off');
  if (code === 'hello_failed') return T('vault.hello.failed');
  if (code === 'help_session') return T('vault.err.helpSession'); // (Receive help: help_page.go)
  if (code === 'cred_missing') return T('vault.err.credMissing');
  if (code === 'cred_in_use') return T('vault.err.credInUse');
  if (code === 'cred_rdp_key') return T('vault.err.credRdpKey');
  return T('vault.err.internal');
}
// An asynchronous vault binding: its request id, then its answer (a
// promise) — mirrors local_pane.go's localFs() for reachFs*, for the same
// reason (Go answers later, through a page-wide event, not the binding's
// own return value, which only says the request was accepted).
function vaultAsyncCall(name) {
  return vaultAsyncCallWithin(VAULT_TIMEOUT_MS, name, Array.prototype.slice.call(arguments, 1));
}
// The same, answered within ms (Windows Hello: the operator in front of its prompt).
var VAULT_HELLO_TIMEOUT_MS = 200000;
function vaultAsyncCallWithin(ms, name, args) {
  var id = explorerReqId();
  return new Promise(function (resolve, reject) {
    var w = { resolve: resolve, reject: reject, timer: null };
    w.timer = setTimeout(function () { if (vaultWaiting.get(id) === w) { vaultWaiting.delete(id); reject(vaultErrFrom({ code: 'timeout' })); } }, ms);
    vaultWaiting.set(id, w);
    reachNative.apply(null, [name, id].concat(args)).then(function (r) {
      if (r === true || vaultWaiting.get(id) !== w) return;
      vaultWaiting.delete(id); clearTimeout(w.timer); reject(vaultErrFrom({ code: 'unavailable' }));
    }, function (e) {
      if (vaultWaiting.get(id) !== w) return;
      vaultWaiting.delete(id); clearTimeout(w.timer); reject((e && e.code) ? e : vaultErrFrom({ code: 'unavailable' }));
    });
  });
}
// A synchronous vault binding: direct call, no reqID (reachVaultState,
// reachVaultForSession, reachVaultLock, reachVaultList, reachVaultDelete).
function vaultSyncCall(name) {
  var args = Array.prototype.slice.call(arguments, 1);
  return reachNative.apply(null, [name].concat(args)).catch(function () { return null; });
}
function reachVaultAvailable() { return typeof window.reachVaultState === 'function' && !!reachNativeToken(); }
function reachVaultManageAvailable() { return typeof window.reachVaultList === 'function' && !!reachNativeToken(); }

// ── Toolbar button + popover (one per open Reach session) ──────────────────
function reachBuildVaultTool(toolbar) {
  if (!reachVaultAvailable()) return;
  reachDropdown(toolbar, 'reach-vault', REACH_ICON.vaultKey, T('vault.button.title'), reachRenderVaultMenu, 'vault-menu');
}
function reachRenderVaultMenu(tab) {
  var menu = document.getElementById('reach-vault-menu');
  if (!menu) return;
  menu.innerHTML = '';
  if (!tab || !tab.sessionToken) { vaultMenuNote(menu, T('vault.scopeUnknown')); return; }
  var loading = document.createElement('div'); loading.className = 'reach-menu-note'; loading.textContent = '…';
  menu.appendChild(loading);
  vaultSyncCall('reachVaultForSession', tab.sessionToken).then(function (res) {
    if (document.getElementById('reach-vault-menu') !== menu) return; // closed/rebuilt meanwhile
    menu.innerHTML = '';
    vaultRenderMenuBody(menu, tab, res || { state: 'unreadable', entries: [] });
  });
}
function vaultRenderMenuBody(menu, tab, res) {
  if (res.state === 'unreadable') { vaultMenuNote(menu, T('vault.locked.unreadable')); return; }
  if (res.state === 'absent') { vaultMenuCreateForm(menu, tab); return; }
  if (res.state === 'locked') { vaultMenuUnlockForm(menu, tab); return; }
  // unlocked
  if (!res.scope || !res.scope.known) { vaultMenuNote(menu, T('vault.scopeUnknown')); return; }
  var entries = res.entries || [];
  if (!entries.length) {
    vaultMenuNote(menu, T('vault.empty'));
  } else {
    var search = document.createElement('input');
    search.type = 'text'; search.className = 'vault-search'; search.placeholder = T('vault.manage.search');
    menu.appendChild(search);
    var list = document.createElement('div');
    menu.appendChild(list);
    var paint = function () {
      var q = search.value.trim().toLowerCase();
      list.innerHTML = '';
      entries.filter(function (en) {
        return !q || (en.label || '').toLowerCase().indexOf(q) >= 0 || (en.username || '').toLowerCase().indexOf(q) >= 0;
      }).forEach(function (en) { list.appendChild(vaultEntryRow(tab, en)); });
    };
    search.addEventListener('input', paint);
    search.addEventListener('click', function (e) { e.stopPropagation(); });
    paint();
  }
  // A VM console: only this VM's entries and the unrestricted ones (vault.go
  // sessionMatch), said; "Save for this VM" pre-binds a new entry to it (the
  // VM and its host as the proxy recorded them from the server's answer).
  var vmSess = !!(res.scope && res.scope.vm);
  if (vmSess) vaultMenuNote(menu, T('vault.vm.scopeNote'));
  var sep = document.createElement('div'); sep.className = 'reach-menu-sep'; menu.appendChild(sep);
  if (vmSess && res.scope.vmId && reachVaultManageAvailable()) {
    var saveVm = document.createElement('button'); saveVm.id = 'vault-save-for-vm';
    saveVm.textContent = T('vault.vm.saveFor') + '…';
    saveVm.addEventListener('click', function () {
      document.querySelectorAll('.toolbar-dropdown-menu.open').forEach(function (m) { m.classList.remove('open'); });
      vaultSaveForVm(tab, res.scope);
    });
    menu.appendChild(saveVm);
  }
  if (reachVaultManageAvailable()) {
    var manage = document.createElement('button');
    manage.textContent = T('vault.manage.title') + '…';
    manage.addEventListener('click', function () { navigateTo('vault'); }); // (the Vault page: vault_page.go)
    menu.appendChild(manage);
  }
  var lock = document.createElement('button');
  lock.textContent = T('vault.lockNow');
  lock.addEventListener('click', function () { vaultSyncCall('reachVaultLock').then(function () { reachRenderVaultMenu(tab); }); });
  menu.appendChild(lock);
}
function vaultMenuNote(menu, text) {
  var n = document.createElement('div'); n.className = 'reach-menu-note'; n.textContent = text; menu.appendChild(n);
}
// "Save for this VM" (main window): the entry form with the VM ticked. Its
// identity is the session's as Go recorded it (scope: origin, tenant, host
// id / uuid, VM GUID), its names the page's lists (shown only).
function vaultSaveForVm(tab, scope) {
  if (!scope || !scope.vm || !scope.vmId || !scope.origin || !scope.tenantId) return;
  var vm = (tab && tab.device && tab.device._vm) || {};
  var item = { origin: scope.origin, tenantId: scope.tenantId, uuid: scope.deviceUuid || '', id: scope.deviceId || 0,
    name: String(vm.host || ''), vmId: scope.vmId, vmName: String(vm.name || '') };
  vaultOpenEntryModal(null, function () { if (tab) reachRenderVaultMenu(tab); }, { devices: [item], label: item.vmName });
}
// What a click on an entry types on this machine: 'user' = username, Tab,
// password; 'pass' = the password alone. No username: the password alone
// (nothing before it). With one, it follows the machine's domain membership
// (tab.domain, the agent's init): outside a domain ('workgroup') the sign-in
// screen has no user field, so the password alone; in a domain, or unknown
// (an older agent), username + password as the entry says. The row's small
// button always offers the other way.
function vaultUserMode(tab, en) {
  if (!en || !String(en.username || '').trim()) return 'pass';
  return (tab && tab.domain === 'workgroup') ? 'pass' : 'user';
}
function vaultEntryRow(tab, en) {
  var row = document.createElement('div'); row.className = 'vault-entry-row';
  var btn = document.createElement('button'); btn.className = 'vault-entry-btn';
  var lbl = document.createElement('span'); lbl.className = 'vault-entry-lbl'; lbl.textContent = en.label || en.username || '?';
  btn.appendChild(lbl);
  var hasUser = vaultUserMode(null, en) === 'user', mode = vaultUserMode(tab, en);
  if (hasUser) { var sub = document.createElement('span'); sub.className = 'vault-entry-sub'; sub.textContent = en.username; btn.appendChild(sub); }
  btn.title = !hasUser ? T('vault.entry.type') : mode === 'user' ? T('vault.entry.typeWithUser') : T('vault.entry.typeNoDomain');
  btn.addEventListener('click', function (e) { e.stopPropagation(); vaultTypeEntry(tab, en, mode === 'user'); });
  row.appendChild(btn);
  if (hasUser) {
    // The other way: the password alone, or (outside a domain) the username too.
    var other = document.createElement('button'); other.className = 'vault-entry-user';
    other.title = mode === 'user' ? T('vault.entry.type') : T('vault.entry.typeWithUser');
    other.innerHTML = mode === 'user' ? REACH_ICON.vaultKey : REACH_ICON.vaultUser;
    other.addEventListener('click', function (e) { e.stopPropagation(); vaultTypeEntry(tab, en, mode !== 'user'); });
    row.appendChild(other);
  }
  return row;
}
// Ctrl+Alt+Del before typing when the remote session is locked (the agent
// decides, agent typetext_prep.go): Settings > Vault, on by default. A
// per-computer preference (this app's storage), not a vault secret.
var VAULT_AUTOSAS_KEY = 'oblireach.vault.autoSas';
function vaultAutoSasOn() { try { return localStorage.getItem(VAULT_AUTOSAS_KEY) !== '0'; } catch (e) { return true; } }
function vaultSetAutoSas(on) { try { localStorage.setItem(VAULT_AUTOSAS_KEY, on ? '1' : '0'); } catch (e) {} }
function vaultTypeEntry(tab, en, withUser) {
  if (!tab || !tab.sessionToken) return;
  // A Hyper-V VM console (vm_console.go): typed by its helper on the guest's
  // keyboard layout (the one chosen in its "Type text"); never its length said.
  var vm = typeof devIsVm === 'function' && devIsVm(tab.device);
  if (vm) tab.vmVaultTyping = true;
  (vm ? vaultAsyncCall('reachVaultTypeVm', tab.sessionToken, en.id, !!withUser, false, vmLayoutGet(tab.device), vmGuestGet(tab.device))
      : vaultAsyncCall('reachVaultType', tab.sessionToken, en.id, !!withUser, false, tab.gen, vaultAutoSasOn()))
    .then(function () { document.querySelectorAll('.toolbar-dropdown-menu.open').forEach(function (m) { m.classList.remove('open'); }); })
    .catch(function (e) { if (vm) tab.vmVaultTyping = false; reachToast(vaultErrText(e), 'warn'); });
}
// The agent's answer once it typed (type_text_result, through the session's
// own WebSocket): ok, or a code — never a length, never any content. prep:
// what it did first ('sas_sent': Ctrl+Alt+Del, the session was locked).
var VAULT_TYPE_ERR = {
  no_credential_screen: 'vault.err.noCredScreen',
  username_required: 'vault.err.usernameRequired',
  field_focus_failed: 'vault.err.fieldFocusFailed',
  password_field_missing: 'vault.err.pwFieldMissing',
  session_changed: 'vault.scopeUnknown',
  busy: 'vault.err.busy', rate_limited: 'vault.err.busy',
};
function vaultOnTypeResult(tab, msg) {
  if (!msg || typeof msg !== 'object') return;
  if (msg.ok === true) {
    if (msg.prep === 'sas_sent') reachToast(T('vault.typed.sasSent'));
    return;
  }
  reachToast(T(VAULT_TYPE_ERR[msg.code] || 'vault.err.typeFailed'), 'warn');
}
// Shared by the toolbar popover (tab set, onDone omitted: re-renders the
// popover) and the Settings > Vault page (tab null, onDone re-renders the
// management list) — both hand this a bare container to fill.
function vaultMenuUnlockForm(menu, tab, onDone) {
  var note = document.createElement('div'); note.className = 'reach-menu-note'; note.textContent = T('vault.locked.title');
  menu.appendChild(note);
  var input = document.createElement('input');
  input.type = 'password'; input.className = 'vault-search'; input.placeholder = T('vault.locked.placeholder');
  input.autocomplete = 'off';
  menu.appendChild(input);
  var err = document.createElement('div'); err.className = 'modal-err'; menu.appendChild(err);
  var btn = document.createElement('button'); btn.className = 'btn-primary'; btn.textContent = T('vault.locked.submit');
  menu.appendChild(btn);
  var submitting = false;
  var done = function () { submitting = false; if (onDone) onDone(); else reachRenderVaultMenu(tab); };
  var submit = function () {
    if (submitting || !input.value) return;
    submitting = true; err.textContent = '';
    vaultAsyncCall('reachVaultUnlock', input.value).then(done, function (e) { submitting = false; err.textContent = vaultErrText(e); });
  };
  // Windows Hello, when available and turned on for this vault: under the
  // code, and launched by itself as this prompt opens (once; the code field
  // comes back when it is canceled or fails).
  var form = {
    id: explorerReqId(), autoTried: false, input: input,
    codeShown: function (on) {
      input.style.display = on ? '' : 'none'; btn.style.display = on ? '' : 'none';
      if (on) setTimeout(function () { try { input.focus(); } catch (e) {} }, 0);
    },
  };
  vaultHelloOffer(menu, err, function () { return submitting; }, function (on) { submitting = on; }, done, form);
  btn.addEventListener('click', function (e) { e.stopPropagation(); submit(); });
  input.addEventListener('click', function (e) { e.stopPropagation(); });
  input.addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); submit(); } });
  setTimeout(function () { try { input.focus(); } catch (e) {} }, 0);
}
function vaultMenuCreateForm(menu, tab, onDone) {
  var title = document.createElement('div'); title.className = 'reach-menu-note'; title.style.fontWeight = '600'; title.style.color = 'var(--text)';
  title.textContent = T('vault.create.title');
  menu.appendChild(title);
  var intro = document.createElement('div'); intro.className = 'reach-menu-note'; intro.textContent = T('vault.create.intro');
  menu.appendChild(intro);
  var code1 = document.createElement('input'); code1.type = 'password'; code1.className = 'vault-search'; code1.placeholder = T('vault.create.placeholder');
  var code2 = document.createElement('input'); code2.type = 'password'; code2.className = 'vault-search'; code2.placeholder = T('vault.create.confirmPlaceholder');
  menu.appendChild(code1); menu.appendChild(code2);
  var err = document.createElement('div'); err.className = 'modal-err'; menu.appendChild(err);
  var btn = document.createElement('button'); btn.className = 'btn-primary'; btn.textContent = T('vault.create.submit');
  menu.appendChild(btn);
  [code1, code2].forEach(function (i) { i.addEventListener('click', function (e) { e.stopPropagation(); }); });
  var submitting = false;
  var submit = function () {
    if (submitting) return;
    if (code1.value.length < 6) { err.textContent = T('vault.create.tooShort'); return; }
    if (code1.value !== code2.value) { err.textContent = T('vault.create.mismatch'); return; }
    submitting = true; err.textContent = '';
    vaultAsyncCall('reachVaultCreate', code1.value).then(function () {
      submitting = false;
      if (onDone) onDone(); else reachRenderVaultMenu(tab);
    }, function (e) { submitting = false; err.textContent = vaultErrText(e); });
  };
  btn.addEventListener('click', function (e) { e.stopPropagation(); submit(); });
  [code1, code2].forEach(function (i) { i.addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); submit(); } }); });
  setTimeout(function () { try { code1.focus(); } catch (e) {} }, 0);
}

// Windows Hello under an unlock form (reachVaultState: available and on).
// busy / setBusy: the form's own "a request is on its way".
var VAULT_HELLO_ICON = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M7 11a5 5 0 0 1 10 0v1.5"/><path d="M4.5 9.5a7.8 7.8 0 0 1 15 0"/><path d="M12 11v4.5c0 1.8.7 3.4 1.8 4.5"/><path d="M9 12.5v2c0 2.6 1 4.9 2.6 6.5"/><path d="M15 14.5c0 1.3.3 2.6.9 3.7"/></svg>';
// form (vaultMenuUnlockForm): { id (one per opening), autoTried, input,
// codeShown(on) }: Windows Hello is launched by itself once per opening, the
// app deciding (reachVaultUnlockHelloAuto, hello_auto.go: this window on
// screen and in the foreground, not right after a cancel); "hello_skipped":
// the code field, no error. One automatic prompt at a time in this page
// (vaultHelloAutoRunning; the app shares one prompt between windows).
var vaultHelloAutoRunning = false;
function vaultHelloAutoStart(menu, err, busy, setBusy, done, form) {
  if (!form || form.autoTried) return;
  form.autoTried = true;
  if (typeof window.reachVaultUnlockHelloAuto !== 'function' || vaultHelloAutoRunning || busy()) return;
  if ((form.input && form.input.value) || document.visibilityState === 'hidden' || menu.isConnected === false) return;
  vaultHelloAutoRunning = true;
  setBusy(true); err.textContent = '';
  form.codeShown(false);
  var wait = document.createElement('div'); wait.className = 'reach-menu-note vault-hello-wait'; wait.textContent = T('vault.hello.waiting');
  menu.insertBefore(wait, err);
  var end = function () { vaultHelloAutoRunning = false; try { wait.remove(); } catch (e) {} };
  vaultAsyncCallWithin(VAULT_HELLO_TIMEOUT_MS, 'reachVaultUnlockHelloAuto', [form.id]).then(function (r) { end(); done(r); }, function (x) {
    end(); setBusy(false);
    form.codeShown(true);
    if (!x || x.code !== 'hello_skipped') err.textContent = vaultErrText(x);
  });
}
function vaultHelloOffer(menu, err, busy, setBusy, done, form) {
  if (typeof window.reachVaultUnlockHello !== 'function') return;
  vaultSyncCall('reachVaultState').then(function (st) {
    var h = st && st.hello;
    if (!h || !h.available || !h.enabled || st.state !== 'locked' || menu.isConnected === false) return;
    var hb = document.createElement('button'); hb.className = 'btn-sm vault-hello-btn'; hb.id = 'vault-hello-unlock';
    hb.innerHTML = VAULT_HELLO_ICON;
    var hl = document.createElement('span'); hl.textContent = T('vault.hello.unlock'); hb.appendChild(hl);
    hb.title = T('vault.hello.unlockTip');
    hb.addEventListener('click', function (e) {
      if (e && e.stopPropagation) e.stopPropagation();
      if (busy()) return;
      setBusy(true); err.textContent = '';
      vaultAsyncCallWithin(VAULT_HELLO_TIMEOUT_MS, 'reachVaultUnlockHello', []).then(done, function (x) { setBusy(false); err.textContent = vaultErrText(x); });
    });
    menu.appendChild(hb);
    vaultHelloAutoStart(menu, err, busy, setBusy, done, form);
  });
}

// ── Settings > Vault: the vault's settings only (main window) ─────────────
// Windows Hello, Ctrl+Alt+Del before typing, lock now; the entries are
// managed on the Vault page (vault_page.go), one click away.
function renderVaultSection(sb) {
  var panel = document.createElement('div'); panel.className = 'panel';
  var head = document.createElement('div'); head.className = 'panel-header'; head.textContent = T('settings.nav.vault');
  var pb = document.createElement('div'); pb.className = 'panel-body';
  panel.appendChild(head); panel.appendChild(pb); sb.appendChild(panel);
  var body = document.createElement('div'); body.id = 'vault-settings-body';
  pb.appendChild(body);
  vaultRenderSettings(body);
}
function vaultRenderSettings(body) {
  body.innerHTML = '';
  // Where the entries are: the Vault page.
  var go = document.createElement('div'); go.className = 'vault-hello-row';
  var gi = document.createElement('div'); gi.className = 'info';
  var gl = document.createElement('div'); gl.className = 'lbl'; gl.textContent = T('nav.vault');
  var gs = document.createElement('div'); gs.className = 'sub'; gs.textContent = T('vault.settings.manageHint');
  gi.appendChild(gl); gi.appendChild(gs);
  var gb = document.createElement('button'); gb.className = 'btn-sm'; gb.id = 'vault-settings-open'; gb.textContent = T('vault.settings.open');
  gb.addEventListener('click', function () { navigateTo('vault'); });
  go.appendChild(gi); go.appendChild(gb);
  body.appendChild(go);
  // This computer's choice: whatever the vault's state.
  vaultAutoSasSettingRow(body);
  vaultSyncCall('reachVaultState').then(function (st) {
    st = st || { state: 'unreadable' };
    if (body.isConnected === false) return;
    if (st.state === 'unreadable') { var n = document.createElement('div'); n.className = 'sub'; n.textContent = T('vault.locked.unreadable'); body.appendChild(n); return; }
    if (st.state === 'absent') {
      var wrap = document.createElement('div'); wrap.className = 'vault-form';
      body.appendChild(wrap);
      vaultMenuCreateForm(wrap, null, function () { vaultRenderSettings(body); });
      return;
    }
    if (st.state === 'locked') {
      // (Windows Hello is turned on or off from an unlocked vault)
      var msg = document.createElement('div'); msg.className = 'sub'; msg.style.marginBottom = '10px'; msg.textContent = T('vault.settings.locked');
      body.appendChild(msg);
      var wrap2 = document.createElement('div'); wrap2.className = 'vault-form';
      body.appendChild(wrap2);
      vaultMenuUnlockForm(wrap2, null, function () { vaultRenderSettings(body); });
      return;
    }
    vaultHelloSettingRow(body);
    var lockBtn = document.createElement('button'); lockBtn.className = 'btn-sm'; lockBtn.id = 'vault-settings-lock'; lockBtn.textContent = T('vault.lockNow');
    lockBtn.addEventListener('click', function () { vaultSyncCall('reachVaultLock').then(function () { vaultRenderSettings(body); }); });
    body.appendChild(lockBtn);
  });
}
// "Unlock with Windows Hello" (main window, unlocked vault), shown when
// Windows can verify this user. Turning it on asks Hello once.
function vaultHelloSettingRow(body) {
  if (typeof window.reachVaultHelloSet !== 'function') return;
  var row = document.createElement('div'); row.className = 'vault-hello-row'; row.style.display = 'none';
  var info = document.createElement('div'); info.className = 'info';
  var lbl = document.createElement('div'); lbl.className = 'lbl'; lbl.textContent = T('vault.hello.setting');
  var sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('vault.hello.settingSub');
  info.appendChild(lbl); info.appendChild(sub);
  var cb = document.createElement('input'); cb.type = 'checkbox'; cb.id = 'vault-hello-set'; cb.title = T('vault.hello.setting');
  row.appendChild(info); row.appendChild(cb);
  body.appendChild(row);
  vaultSyncCall('reachVaultState').then(function (st) {
    var h = st && st.hello;
    if (!h || (!h.available && !h.enabled)) return; // (no Windows Hello here: nothing to offer)
    row.style.display = '';
    cb.checked = !!h.enabled;
  });
  cb.addEventListener('change', function () {
    var on = !!cb.checked;
    cb.disabled = true;
    vaultAsyncCallWithin(VAULT_HELLO_TIMEOUT_MS, 'reachVaultHelloSet', [on]).then(function () {
      cb.disabled = false; cb.checked = on;
      setStatus(T(on ? 'vault.hello.onDone' : 'vault.hello.offDone'));
    }, function (e) {
      cb.disabled = false; cb.checked = !on;
      reachToast(vaultErrText(e), 'warn');
    });
  });
}
// "Ctrl+Alt+Del before typing" (vaultAutoSasOn): this computer's choice.
function vaultAutoSasSettingRow(body) {
  var row = document.createElement('div'); row.className = 'vault-hello-row';
  var info = document.createElement('div'); info.className = 'info';
  var lbl = document.createElement('div'); lbl.className = 'lbl'; lbl.textContent = T('vault.autoSas.setting');
  var sub = document.createElement('div'); sub.className = 'sub'; sub.textContent = T('vault.autoSas.settingSub');
  info.appendChild(lbl); info.appendChild(sub);
  var cb = document.createElement('input'); cb.type = 'checkbox'; cb.id = 'vault-autosas-set'; cb.title = T('vault.autoSas.setting');
  cb.checked = vaultAutoSasOn();
  cb.addEventListener('change', function () { vaultSetAutoSas(!!cb.checked); });
  row.appendChild(info); row.appendChild(cb);
  body.appendChild(row);
}
// One password entry of the Vault page (vault_page.go). reload: what to do
// after an edit or a deletion (the page's list read again). opts.place
// false: its tenant left out (the page's section already says it).
function vaultEntryCard(en, reload, opts) {
  var again = typeof reload === 'function' ? reload : function () {};
  var card = document.createElement('div'); card.className = 'vault-entry-card';
  var info = document.createElement('div'); info.className = 'info';
  var lbl = document.createElement('div'); lbl.className = 'lbl'; lbl.textContent = en.label || en.username || '?';
  var sub = document.createElement('div'); sub.className = 'sub';
  var nDev = Array.isArray(en.devices) ? en.devices.length : 0;
  var showPlace = !(opts && opts.place === false);
  // Its machines: how many; its VMs by name, with their host (vault_devices_ui.go vaultDevName).
  var devNames = nDev ? en.devices.map(vaultDevName) : [];
  var vmNames = nDev ? en.devices.filter(function (d) { return d && d.vmId; }).map(vaultDevName) : [];
  sub.textContent = [en.username, showPlace ? en.tenantName : '', nDev ? T('vault.devices.short', { count: nDev }) : '',
    vmNames.slice(0, 2).join(', ') + (vmNames.length > 2 ? ', …' : '')].filter(Boolean).join(' · ');
  if (devNames.length) sub.title = devNames.join(', ');
  info.appendChild(lbl); info.appendChild(sub);
  card.appendChild(info);
  var acts = document.createElement('div'); acts.className = 'acts';
  var revealBtn = document.createElement('button'); revealBtn.className = 'btn-sm'; revealBtn.textContent = T('vault.manage.reveal');
  revealBtn.addEventListener('click', function () { vaultOpenRevealModal(en); });
  var editBtn = document.createElement('button'); editBtn.className = 'btn-sm'; editBtn.textContent = T('vault.manage.editTitle');
  editBtn.addEventListener('click', function () { vaultOpenEntryModal(en, again); });
  var delBtn = document.createElement('button'); delBtn.className = 'btn-sm'; delBtn.textContent = T('vault.manage.delete');
  delBtn.addEventListener('click', function () {
    vaultOpenConfirmModal(T('vault.manage.deleteConfirm'), function () {
      vaultSyncCall('reachVaultDelete', en.id).then(again);
    });
  });
  acts.appendChild(revealBtn); acts.appendChild(editBtn); acts.appendChild(delBtn);
  card.appendChild(acts);
  return card;
}

// ── Modals (add/edit entry, reveal, delete confirm) ─────────────────────────
// A small modal shell on the app's generic .modal-overlay/.modal-box (the
// 2FA prompt, explorer's confirmations): a header with a close button, ESC
// and a backdrop click both dismiss it. Returns { box, close } for the
// caller to fill and to override close() with (vaultOpenRevealModal wipes
// the revealed secret from the DOM before deferring to this one).
function vaultBaseModal(titleKey) {
  var ov = document.createElement('div'); ov.className = 'modal-overlay visible';
  var box = document.createElement('div'); box.className = 'modal-box';
  var head = document.createElement('div'); head.className = 'modal-head';
  var h = document.createElement('h3'); h.textContent = T(titleKey);
  var x = document.createElement('button'); x.className = 'close';
  x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
  head.appendChild(h); head.appendChild(x);
  box.appendChild(head);
  ov.appendChild(box);
  var esc = null;
  var close = function () {
    if (ov.parentElement) ov.parentElement.removeChild(ov);
    if (esc) document.removeEventListener('keydown', esc);
  };
  esc = function (e) { if (e.key === 'Escape') close(); };
  x.addEventListener('click', close);
  ov.addEventListener('mousedown', function (e) { if (e.target === ov) close(); });
  document.addEventListener('keydown', esc);
  document.body.appendChild(ov);
  return { box: box, close: close };
}
function vaultField(labelKey, type) {
  var wrap = document.createElement('div'); wrap.className = 'vault-field';
  var lbl = document.createElement('label'); lbl.textContent = T(labelKey);
  var input = document.createElement(type === 'textarea' ? 'textarea' : 'input');
  if (type !== 'textarea') input.type = type || 'text';
  wrap.appendChild(lbl); wrap.appendChild(input);
  return { wrap: wrap, input: input };
}
function vaultSelectField(labelKey) {
  var wrap = document.createElement('div'); wrap.className = 'vault-field';
  var lbl = document.createElement('label'); lbl.textContent = T(labelKey);
  var input = document.createElement('select');
  wrap.appendChild(lbl); wrap.appendChild(input);
  return { wrap: wrap, input: input };
}
// A server profile's origin as the proxy records a session's (servers.go
// serverOrigin: scheme://host, host as written).
function vaultOriginOf(url) {
  var m = /^(https?):\/\/([^\/?#]+)/i.exec(String(url || '').trim());
  return m ? m[1].toLowerCase() + '://' + m[2] : '';
}
// The tenants the app knows for a server: multi-server mode reads every
// server's; single mode knows the active server's (the tenant menu's).
function vaultTenantsOf(pid) {
  var list = [];
  if (typeof srvState === 'function' && pid !== undefined) list = srvState(pid).tenants || [];
  if (!list.length && typeof tenants !== 'undefined' && Array.isArray(tenants) && (pid === undefined || pid === (typeof homePid === 'function' ? homePid() : ''))) list = tenants;
  return list.filter(function (t) { return t && Number.isInteger(Number(t.id)) && Number(t.id) > 0; });
}
function vaultPreferredTenant(pid) {
  if (typeof multiFilter !== 'undefined' && multiFilter.pid === pid && multiFilter.tenantId !== null) return multiFilter.tenantId;
  var st = (typeof srvState === 'function' && pid !== undefined) ? srvState(pid) : null;
  if (st && st.me && Number.isInteger(st.me.tenantId)) return st.me.tenantId;
  return (typeof currentTenantId !== 'undefined' && Number.isInteger(currentTenantId)) ? currentTenantId : null;
}
// The place values of the entry form: "every server" / "every tenant".
var VAULT_ALL_SERVERS = '*', VAULT_ALL_TENANTS = '0';
// opts (a new entry only): { devices: the machines / VMs ticked from the
// start, label: its label to start with } ("Save for this VM").
function vaultOpenEntryModal(en, onSaved, opts) {
  var pre = (!en && opts && Array.isArray(opts.devices) && opts.devices.length) ? opts.devices : null;
  var m = vaultBaseModal(en ? 'vault.manage.editTitle' : 'vault.manage.newTitle');
  var form = document.createElement('div'); form.className = 'vault-form';
  // Server and tenant are PICKED: the server among the
  // app's server profiles (stored as its origin, exactly as the proxy
  // records a session's: servers.go serverOrigin), the tenant among that
  // server's tenants (its name stored with it, never typed). Or wider:
  // every tenant of the server (tenant 0), every server
  // (no origin: the Vault page's "General" section).
  var origin = vaultSelectField('vault.manage.origin');
  var tenant = vaultSelectField('vault.manage.tenantPick');
  origin.input.id = 'vault-place-server'; tenant.input.id = 'vault-place-tenant';
  var profs = (typeof serverProfiles !== 'undefined' ? serverProfiles : []).filter(function (pr) { return vaultOriginOf(pr.serverUrl); });
  var oAll = document.createElement('option'); oAll.value = VAULT_ALL_SERVERS; oAll.textContent = T('vault.scope.allServers');
  origin.input.appendChild(oAll);
  profs.forEach(function (pr) {
    var o = document.createElement('option'); o.value = vaultOriginOf(pr.serverUrl); o.textContent = String(pr.name || o.value); o.dataset.pid = pr.id;
    origin.input.appendChild(o);
  });
  if (en && en.origin && !origin.input.querySelector('option[value="' + CSS.escape(en.origin) + '"]')) {
    var oo = document.createElement('option'); oo.value = en.origin; oo.textContent = en.origin; origin.input.appendChild(oo);
  }
  var here = profs.find(function (pr) { return pr.id === (typeof homePid === 'function' ? homePid() : ''); });
  origin.input.value = en ? (en.origin || VAULT_ALL_SERVERS) : (here ? vaultOriginOf(here.serverUrl) : (profs[0] ? vaultOriginOf(profs[0].serverUrl) : VAULT_ALL_SERVERS));
  // (ticked from the start: its server, when the app knows it)
  if (pre && origin.input.querySelector('option[value="' + CSS.escape(pre[0].origin) + '"]')) origin.input.value = pre[0].origin;
  var fillTenants = function () {
    tenant.input.innerHTML = '';
    var all = origin.input.value === VAULT_ALL_SERVERS;
    tenant.wrap.style.display = all ? 'none' : ''; // (every server: every tenant)
    var tAll = document.createElement('option'); tAll.value = VAULT_ALL_TENANTS; tAll.textContent = T('vault.scope.allTenants');
    tenant.input.appendChild(tAll);
    if (all) { tenant.input.value = VAULT_ALL_TENANTS; return; }
    var opt = origin.input.selectedOptions && origin.input.selectedOptions[0];
    var pid = opt && opt.dataset ? opt.dataset.pid : undefined;
    var list = vaultTenantsOf(pid);
    list.forEach(function (t) { var o = document.createElement('option'); o.value = String(t.id); o.textContent = String(t.name || t.id); tenant.input.appendChild(o); });
    if (en && en.tenantId && en.origin === origin.input.value && !tenant.input.querySelector('option[value="' + en.tenantId + '"]')) {
      var ot = document.createElement('option'); ot.value = String(en.tenantId); ot.textContent = en.tenantName || String(en.tenantId); tenant.input.appendChild(ot);
    }
    if (tenant.input.options.length === 1) {
      var none = document.createElement('option'); none.value = ''; none.disabled = true;
      none.textContent = T('vault.manage.tenantsUnknown', { server: (opt && opt.textContent) || origin.input.value });
      tenant.input.appendChild(none);
    }
    var pref = (en && en.origin === origin.input.value) ? en.tenantId
      : (pre && pre[0].origin === origin.input.value) ? pre[0].tenantId : vaultPreferredTenant(pid);
    tenant.input.value = (pref !== null && pref !== undefined && tenant.input.querySelector('option[value="' + pref + '"]')) ? String(pref) : VAULT_ALL_TENANTS;
  };
  // The place as the machines field reads it: { origin ('' every server),
  // pid (its server profile), tenantId (0 every tenant) }.
  var placeNow = function () {
    var all = origin.input.value === VAULT_ALL_SERVERS;
    var opt = origin.input.selectedOptions && origin.input.selectedOptions[0];
    return { origin: all ? '' : origin.input.value, pid: all ? '' : ((opt && opt.dataset && opt.dataset.pid) || vaultOriginPid(origin.input.value) || ''),
      tenantId: all ? 0 : (parseInt(tenant.input.value, 10) || 0) };
  };
  origin.input.addEventListener('change', function () { fillTenants(); devField.setPlace(placeNow()); });
  tenant.input.addEventListener('change', function () { devField.setPlace(placeNow()); });
  fillTenants();
  var label = vaultField('vault.manage.label');
  label.input.value = (en && en.label) || (!en && opts && typeof opts.label === 'string' ? opts.label : '');
  var username = vaultField('vault.manage.username');
  username.input.value = (en && en.username) || '';
  var uHint = document.createElement('span'); uHint.className = 'hint'; uHint.textContent = T('vault.manage.usernameHint');
  username.wrap.appendChild(uHint);
  // The machines it is offered on (vault_devices_ui.go): every machine of
  // the chosen place, or only the ticked ones — taken within that place
  // (the picker lists its machines only; a machine outside a place chosen
  // afterwards is unticked, said in the field).
  var devField = vaultDevicesField((en && en.devices) || pre || [], null, placeNow());
  var secret = vaultField('vault.manage.secret', 'password');
  secret.input.placeholder = en ? '••••••••' : '';
  var note = vaultField('vault.manage.note', 'textarea');
  note.input.value = (en && en.note) || '';
  var check = document.createElement('div'); check.className = 'vault-check-row';
  var cb = document.createElement('input'); cb.type = 'checkbox'; cb.id = 'vault-secure-only';
  cb.checked = en ? !!en.secureOnly : true;
  var cbLbl = document.createElement('label'); cbLbl.setAttribute('for', 'vault-secure-only');
  cbLbl.innerHTML = TH('vault.manage.secureOnly') + '<span class="hint">' + TH('vault.manage.secureOnlyHint') + '</span>';
  check.appendChild(cb); check.appendChild(cbLbl);
  var err = document.createElement('div'); err.className = 'modal-err';
  var place = document.createElement('div'); place.className = 'vault-row2';
  place.appendChild(origin.wrap); place.appendChild(tenant.wrap);
  form.appendChild(place);
  form.appendChild(devField.wrap);
  [label, username, secret, note].forEach(function (f) { form.appendChild(f.wrap); });
  form.appendChild(check); form.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var save = document.createElement('button'); save.className = 'btn-primary'; save.textContent = T('vault.manage.save');
  save.addEventListener('click', function () {
    var pl = placeNow();
    var input = {
      id: (en && en.id) || '', origin: pl.origin, tenantId: pl.tenantId,
      tenantName: (pl.tenantId && tenant.input.selectedOptions && tenant.input.selectedOptions[0]) ? tenant.input.selectedOptions[0].textContent : '',
      label: label.input.value.trim(), username: username.input.value.trim(),
      secret: secret.input.value, note: note.input.value.trim(), secureOnly: cb.checked,
      devices: devField.value(),
    };
    if (pl.origin && tenant.input.value === '') { err.textContent = T('vault.manage.pickPlace'); return; }
    if (!input.label) { err.textContent = T('vault.err.internal'); return; }
    if (!en && !input.secret) { err.textContent = T('vault.err.internal'); return; }
    vaultAsyncCall('reachVaultSave', input).then(function () { m.close(); if (onSaved) onSaved(); }, function (e) { err.textContent = vaultErrText(e); });
  });
  actions.appendChild(cancel); actions.appendChild(save);
  form.appendChild(actions);
  m.box.appendChild(form);
  setTimeout(function () { try { label.input.focus(); } catch (e) {} }, 0);
}
function vaultOpenRevealModal(en) {
  var m = vaultBaseModal('vault.manage.reveal');
  var form = document.createElement('div'); form.className = 'vault-form';
  var code = document.createElement('input'); code.type = 'password'; code.className = 'vault-search';
  code.placeholder = T('vault.manage.revealCode'); code.style.margin = '0';
  var err = document.createElement('div'); err.className = 'modal-err';
  var value = document.createElement('div'); value.className = 'vault-reveal-value'; value.style.display = 'none';
  // (a private key's passphrase, when it has one: shown under the key)
  var passLbl = document.createElement('div'); passLbl.className = 'sub'; passLbl.textContent = T('vault.cred.passphrase'); passLbl.style.display = 'none';
  var pass = document.createElement('div'); pass.className = 'vault-reveal-value'; pass.id = 'vault-reveal-pass'; pass.style.display = 'none';
  form.appendChild(code); form.appendChild(err); form.appendChild(value); form.appendChild(passLbl); form.appendChild(pass);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  var go = document.createElement('button'); go.className = 'btn-primary'; go.textContent = T('vault.manage.reveal');
  // Wipe the revealed secret from the DOM the moment this modal closes,
  // whichever way it closes (Cancel, X, Escape, backdrop click).
  var wipe = function () { value.textContent = ''; pass.textContent = ''; };
  var closeAndWipe = function () { wipe(); m.close(); };
  cancel.addEventListener('click', closeAndWipe);
  var revealed = false;
  var submit = function () {
    if (revealed) { // Hide: clear the secret, ask for the code again next time
      wipe(); value.style.display = 'none'; pass.style.display = 'none'; passLbl.style.display = 'none'; revealed = false;
      go.textContent = T('vault.manage.reveal'); code.style.display = ''; code.value = '';
      return;
    }
    if (!code.value) return;
    err.textContent = '';
    vaultAsyncCall('reachVaultReveal', en.id, code.value).then(function (r) {
      value.textContent = (r && r.secret) || '';
      value.style.display = '';
      if (r && r.passphrase) { pass.textContent = r.passphrase; pass.style.display = ''; passLbl.style.display = ''; }
      code.value = ''; code.style.display = 'none';
      revealed = true; go.textContent = T('vault.manage.hide');
    }, function (e) { err.textContent = vaultErrText(e); });
  };
  go.addEventListener('click', submit);
  code.addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); submit(); } });
  actions.appendChild(cancel); actions.appendChild(go);
  form.appendChild(actions);
  m.box.appendChild(form);
  var origClose = m.close;
  m.close = function () { wipe(); origClose(); };
  setTimeout(function () { try { code.focus(); } catch (e) {} }, 0);
}
function vaultOpenConfirmModal(text, onConfirm) {
  var m = vaultBaseModal('vault.manage.delete');
  var p = document.createElement('p'); p.className = 'desc'; p.textContent = text;
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var ok = document.createElement('button'); ok.className = 'exp-btn-danger'; ok.textContent = T('vault.manage.delete');
  ok.addEventListener('click', function () { m.close(); onConfirm(); });
  actions.appendChild(cancel); actions.appendChild(ok);
  m.box.appendChild(p); m.box.appendChild(actions);
}
`
