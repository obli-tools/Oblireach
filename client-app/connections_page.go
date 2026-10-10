package main

// connections_page.go — the management of the "local" server profile's
// folder/connection tree (servers.go's ServerProfile.Kind == "local"): a pure
// local RDP/SSH connection manager, independent of any Obliance server.
//
// Its connections are listed by the Devices page itself, exactly like a
// server's devices (ui.go localDevGroups / localTreeRows: folders as groups,
// in single mode on the base and in "All servers"); this file holds what
// that page manages them with: the folder and connection dialogs (new,
// edit) and the deletion, opened from the page's actions and the rows'
// buttons (ui.go localRowButtons / localFolderButtons). A row opens its
// connection through localconn_tools.go (SSH: its terminal tab, RDP: its
// view; the files: the machines' own explorer).
//
// The data is entirely the password vault's folder/ssh/rdp entries
// (vault.go's Kind/ParentID fields, vault_bridge_windows.go's
// reachVaultByParent/reachVaultSave/reachVaultDelete).
//
// A connection signs in with its own username + password / key, or with a
// reusable credential of the vault (e.g. "root lab", "admin
// NAS"...): the connection refers to it (credId), so changing the
// credential changes every connection using it (vault_conn.go). The
// credentials are listed by reachVaultCredentials (never a secret) and
// created in place (vault_page.go vaultOpenCredModal).
//
// Both consts are injected verbatim after the other tool blocks (ui.go's
// buildUI, not through Sprintf: '%' is safe), same pattern as vault_ui.go /
// scripts_page.go.

const connectionsPageCSS = `
/* ── Local RDP/SSH connections (connections_page.go) ── */
.cxn-form{display:flex;flex-direction:column;gap:9px;padding:2px}
.cxn-field{display:flex;flex-direction:column;gap:4px}
.cxn-field label{font-size:11px;color:var(--muted);font-weight:600}
.cxn-field input,.cxn-field select,.cxn-field textarea{background:var(--input-bg);border:none;border-radius:10px;padding:8px 11px;font-size:12.5px;color:var(--text);outline:none;font-family:inherit}
.cxn-row2{display:flex;gap:9px}
.cxn-row2 .cxn-field{flex:1;min-width:0}
.cxn-row2 .cxn-field.cxn-port{flex:0 0 90px}
/* (.cxn-kind-tabs: SSH / RDP, Vault credential / its own: the common segmented control, controls.go .seg) */
.cxn-kind-tabs>*{flex:1 1 0;min-width:0}
.cxn-field .hint{font-size:10.5px;color:var(--muted2);font-weight:400}
`

const connectionsPageJS = `
// ── Local RDP/SSH connection tree ───────────────────────────────────────
// The local base is what is on screen: single mode on it. In multi-server
// mode ("All servers") the Devices page is the servers' list, whatever the
// active profile (servers.go keeps a real server active there): a local
// base has its own section in that list (ui.go multiTreeRows), never the
// whole page. (Before, a base just added in multi-server mode took the
// Devices page over while the top bar still said "All servers".)
function activeServerIsLocal() {
  if (typeof serverMode !== 'undefined' && serverMode === 'multi') return false;
  var p = serverProfiles.find(function (x) { return x.id === activeServerId; });
  return !!(p && p.local);
}

var CXN_KIND_FOLDER = 1, CXN_KIND_SSH = 2, CXN_KIND_RDP = 3;
var CXN_AUTH_PASSWORD = 0, CXN_AUTH_KEY = 1;

function cxnConfirmDelete(n, onDone) {
  var m = scriptsBaseModal(n.kind === CXN_KIND_FOLDER ? 'connections.deleteFolder' : 'connections.deleteConnection');
  var p = document.createElement('p'); p.className = 'desc'; p.textContent = T('connections.deleteConfirm', { name: n.label });
  var err = document.createElement('div'); err.className = 'modal-err';
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var ok = document.createElement('button'); ok.className = 'exp-btn-danger'; ok.textContent = T('connections.delete');
  ok.addEventListener('click', function () {
    vaultSyncCall('reachVaultDelete', n.id).then(function (res) {
      if (res === false) { err.textContent = T('connections.folderNotEmpty'); return; }
      m.close(); onDone();
    });
  });
  actions.appendChild(cancel); actions.appendChild(ok);
  m.box.appendChild(p); m.box.appendChild(err); m.box.appendChild(actions);
}

function cxnField(labelKey, type) {
  var wrap = document.createElement('div'); wrap.className = 'cxn-field';
  var lbl = document.createElement('label'); lbl.textContent = T(labelKey);
  var input = document.createElement(type === 'select' ? 'select' : 'input');
  if (type !== 'select') input.type = type || 'text';
  wrap.appendChild(lbl); wrap.appendChild(input);
  return { wrap: wrap, input: input };
}

// parentId: where a new folder goes ('' the root; an edit keeps its own).
function cxnOpenFolderModal(n, onSaved, parentId) {
  var m = scriptsBaseModal(n ? 'connections.editFolder' : 'connections.newFolder');
  var form = document.createElement('div'); form.className = 'cxn-form';
  var label = cxnField('connections.field.name'); label.input.value = (n && n.label) || '';
  var err = document.createElement('div'); err.className = 'modal-err';
  form.appendChild(label.wrap); form.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var save = document.createElement('button'); save.className = 'btn-primary'; save.textContent = T('connections.save');
  save.addEventListener('click', function () {
    var name = label.input.value.trim();
    if (!name) { err.textContent = T('connections.field.nameRequired'); return; }
    var input = { id: (n && n.id) || '', label: name, kind: CXN_KIND_FOLDER, parentId: n ? (n.parentId || '') : String(parentId || '') };
    save.disabled = true;
    vaultAsyncCall('reachVaultSave', input).then(function () { m.close(); onSaved(); }, function (e) { err.textContent = vaultErrText(e); save.disabled = false; });
  });
  actions.appendChild(cancel); actions.appendChild(save);
  form.appendChild(actions);
  m.box.appendChild(form);
  setTimeout(function () { try { label.input.focus(); } catch (e) {} }, 0);
}

// parentId: the folder a new connection goes in ('' the root; an edit keeps its own).
function cxnOpenConnectionModal(n, onSaved, parentId) {
  var m = scriptsBaseModal(n ? 'connections.editConnection' : 'connections.newConnection', 'scr-box');
  var form = document.createElement('div'); form.className = 'cxn-form';
  var kind = (n && n.kind) || CXN_KIND_SSH;

  var tabs = document.createElement('div'); tabs.className = 'cxn-kind-tabs seg';
  var sshTab = document.createElement('button'); sshTab.type = 'button'; sshTab.textContent = 'SSH';
  var rdpTab = document.createElement('button'); rdpTab.type = 'button'; rdpTab.textContent = 'RDP';
  tabs.appendChild(sshTab); tabs.appendChild(rdpTab);
  if (n) { tabs.style.display = 'none'; } // an entry never changes kind across an edit (vault.go)

  var label = cxnField('connections.field.name'); label.input.value = (n && n.label) || '';
  var row1 = document.createElement('div'); row1.className = 'cxn-row2';
  var host = cxnField('connections.field.host'); host.input.value = (n && n.host) || '';
  var port = cxnField('connections.field.port'); port.wrap.classList.add('cxn-port'); port.input.type = 'number'; port.input.min = '1'; port.input.max = '65535';
  row1.appendChild(host.wrap); row1.appendChild(port.wrap);
  var user = cxnField('connections.field.username'); user.input.value = (n && n.username) || '';
  var authRow = document.createElement('div'); authRow.className = 'cxn-field';
  var authLbl = document.createElement('label'); authLbl.textContent = T('connections.field.authMethod');
  var authSel = document.createElement('select');
  [[CXN_AUTH_PASSWORD, T('connections.auth.password')], [CXN_AUTH_KEY, T('connections.auth.key')]].forEach(function (o) {
    var opt = document.createElement('option'); opt.value = o[0]; opt.textContent = o[1]; authSel.appendChild(opt);
  });
  authRow.appendChild(authLbl); authRow.appendChild(authSel);
  var secret = cxnField('connections.field.secret', 'password');
  secret.input.placeholder = (n && !n.credId) ? T('connections.field.secretKeepHint') : '';
  // A private key's passphrase (optional; an edit keeps it when left empty).
  var pass = cxnField('vault.cred.passphrase', 'password'); pass.input.id = 'cxn-pass';
  pass.input.placeholder = (n && n.keyProtected && !n.credId) ? T('connections.field.secretKeepHint') : T('vault.cred.passphraseOptional');
  var err = document.createElement('div'); err.className = 'modal-err';

  // Its sign-in: a credential of the vault, or its own (as before).
  var creds = [], useCred = !!(n && n.credId);
  var credOk = typeof window.reachVaultCredentials === 'function';
  var modeRow = document.createElement('div'); modeRow.className = 'cxn-field';
  var modeLbl = document.createElement('label'); modeLbl.textContent = T('connections.cred.mode');
  var modeTabs = document.createElement('div'); modeTabs.className = 'cxn-kind-tabs seg';
  var vaultTab = document.createElement('button'); vaultTab.type = 'button'; vaultTab.id = 'cxn-cred-vault'; vaultTab.textContent = T('connections.cred.useVault');
  var ownTab = document.createElement('button'); ownTab.type = 'button'; ownTab.id = 'cxn-cred-own'; ownTab.textContent = T('connections.cred.useOwn');
  modeTabs.appendChild(vaultTab); modeTabs.appendChild(ownTab);
  modeRow.appendChild(modeLbl); modeRow.appendChild(modeTabs);
  var credPick = cxnField('connections.cred.pick', 'select'); credPick.input.id = 'cxn-cred-select';
  var credHint = document.createElement('span'); credHint.className = 'hint'; credHint.textContent = T('connections.cred.passwordOnly');
  credPick.wrap.appendChild(credHint);
  var CRED_NEW = '__new';
  var fillCreds = function (want) {
    var keep = want !== undefined ? want : credPick.input.value;
    credPick.input.innerHTML = '';
    var usable = creds.filter(function (c) { return kind !== CXN_KIND_RDP || (c.authMethod || CXN_AUTH_PASSWORD) === CXN_AUTH_PASSWORD; });
    if (!usable.length) {
      var none = document.createElement('option'); none.value = ''; none.disabled = true; none.textContent = T('connections.cred.none'); credPick.input.appendChild(none);
    }
    usable.forEach(function (c) {
      var o = document.createElement('option'); o.value = c.id;
      o.textContent = c.label + (c.username ? ' (' + c.username + ')' : '');
      credPick.input.appendChild(o);
    });
    var mk = document.createElement('option'); mk.value = CRED_NEW; mk.textContent = T('connections.cred.create'); credPick.input.appendChild(mk);
    credPick.input.value = usable.some(function (c) { return c.id === keep; }) ? keep : (usable[0] ? usable[0].id : '');
    credHint.style.display = kind === CXN_KIND_RDP ? '' : 'none';
  };
  var credLast = '';
  credPick.input.addEventListener('change', function () {
    if (credPick.input.value !== CRED_NEW) { credLast = credPick.input.value; return; }
    credPick.input.value = credLast; // (back to the one chosen, until the new one is saved)
    vaultOpenCredModal(null, function (id) {
      vaultSyncCall('reachVaultCredentials').then(function (list) { creds = Array.isArray(list) ? list : []; fillCreds(id); credLast = credPick.input.value; });
    });
  });
  var showSignIn = function () {
    var key = kind === CXN_KIND_SSH && authSel.value === String(CXN_AUTH_KEY);
    modeRow.style.display = credOk ? '' : 'none';
    vaultTab.classList.toggle('on', useCred); ownTab.classList.toggle('on', !useCred);
    credPick.wrap.style.display = useCred ? '' : 'none';
    user.wrap.style.display = useCred ? 'none' : '';
    authRow.style.display = (!useCred && kind === CXN_KIND_SSH) ? '' : 'none';
    secret.wrap.style.display = useCred ? 'none' : '';
    pass.wrap.style.display = (!useCred && key) ? '' : 'none';
  };
  vaultTab.addEventListener('click', function () { useCred = true; showSignIn(); });
  ownTab.addEventListener('click', function () { useCred = false; showSignIn(); });
  if (credOk) {
    vaultSyncCall('reachVaultCredentials').then(function (list) {
      creds = Array.isArray(list) ? list : [];
      fillCreds((n && n.credId) || '');
      credLast = credPick.input.value;
    });
  }

  var setKind = function (k) {
    kind = k;
    sshTab.classList.toggle('on', k === CXN_KIND_SSH);
    rdpTab.classList.toggle('on', k === CXN_KIND_RDP);
    authRow.style.display = k === CXN_KIND_SSH ? '' : 'none';
    if (k === CXN_KIND_RDP) authSel.value = String(CXN_AUTH_PASSWORD);
    port.input.value = port.input.value || (k === CXN_KIND_RDP ? '3389' : '22');
    secret.wrap.querySelector('label').textContent = T(k === CXN_KIND_SSH && authSel.value === String(CXN_AUTH_KEY) ? 'connections.field.privateKey' : 'connections.field.secret');
    if (creds.length || credOk) fillCreds();
    showSignIn();
  };
  sshTab.addEventListener('click', function () { setKind(CXN_KIND_SSH); });
  rdpTab.addEventListener('click', function () { setKind(CXN_KIND_RDP); });
  authSel.addEventListener('change', function () { setKind(kind); });
  if (n) { authSel.value = String(n.authMethod || CXN_AUTH_PASSWORD); port.input.value = n.port || ''; }

  form.appendChild(tabs); form.appendChild(label.wrap); form.appendChild(row1);
  form.appendChild(modeRow); form.appendChild(credPick.wrap);
  form.appendChild(user.wrap); form.appendChild(authRow); form.appendChild(secret.wrap); form.appendChild(pass.wrap); form.appendChild(err);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  var save = document.createElement('button'); save.className = 'btn-primary'; save.textContent = T('connections.save');
  save.addEventListener('click', function () {
    var name = label.input.value.trim();
    if (!name) { err.textContent = T('connections.field.nameRequired'); return; }
    var hostVal = host.input.value.trim();
    var portVal = parseInt(port.input.value, 10);
    if (!hostVal || !portVal || portVal < 1 || portVal > 65535) { err.textContent = T('connections.field.hostRequired'); return; }
    var input = {
      id: (n && n.id) || '', label: name, kind: kind, parentId: n ? (n.parentId || '') : String(parentId || ''),
      host: hostVal, port: portVal, username: user.input.value.trim(),
      authMethod: kind === CXN_KIND_RDP ? CXN_AUTH_PASSWORD : parseInt(authSel.value, 10),
      secret: secret.input.value,
    };
    if (useCred) {
      // (the credential's username and secret, not a copy: none of its own)
      var cid = credPick.input.value;
      if (!cid || cid === CRED_NEW) { err.textContent = T('connections.cred.required'); return; }
      input.credId = cid; input.username = ''; input.authMethod = CXN_AUTH_PASSWORD; input.secret = '';
    } else {
      // (new, or letting its credential go: a secret of its own is required)
      if ((!n || n.credId) && !input.secret) { err.textContent = T('connections.field.secretRequired'); return; }
      if (input.authMethod === CXN_AUTH_KEY && pass.input.value) input.passphrase = pass.input.value;
    }
    save.disabled = true;
    vaultAsyncCall('reachVaultSave', input).then(function () { secret.input.value = ''; pass.input.value = ''; m.close(); onSaved(); }, function (e) { err.textContent = vaultErrText(e); save.disabled = false; });
  });
  actions.appendChild(cancel); actions.appendChild(save);
  form.appendChild(actions);
  m.box.appendChild(form);
  setKind(kind);
  setTimeout(function () { try { label.input.focus(); } catch (e) {} }, 0);
}
`
