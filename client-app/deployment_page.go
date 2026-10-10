package main

// deployment_page.go — the "Deployment" nav page: generates the same
// install commands / MSI links as the web client's GlobalAddAgentModal
// (Obliance client, components/layout/GlobalAddAgentModal.tsx), from
// Oblireach Desktop, so a technician never has to open the web UI just to
// grab an install one-liner. No new server endpoint: this consumes
// /api/agent/keys and /api/agent/installer/* exactly like the web client
// does (GET /agent/keys needs 'agent_config:keys' or 'agent_config:approval',
// POST /agent/keys is admin-only — the server's 403 is the sole source of
// truth, never guessed at client-side). Same const-injection pattern as
// scripts_page.go / vault_ui.go.

const deploymentPageCSS = `
/* ── Deployment page (deployment_page.go) ── */
.dep-toolbar{display:flex;align-items:center;gap:10px}
.dep-toolbar>span{flex-shrink:0}
.dep-key-list{display:flex;flex-direction:column}
.dep-key-card{display:flex;align-items:center;gap:10px;padding:10px 16px;cursor:pointer;box-shadow:inset 0 -1px 0 rgb(var(--ink) / .06)}
.dep-key-card:last-child{box-shadow:none}
.dep-key-card:hover{background:var(--hover-bg)}
.dep-key-card.sel{background:rgb(var(--accent-rgb) / .10)}
.dep-key-card .nm{font-size:13px;font-weight:600;color:var(--text);flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dep-key-card .grp{font-size:11px;color:var(--accent);flex-shrink:0}
.dep-key-card .cnt{font-size:11px;color:var(--muted);flex-shrink:0}
.dep-panel{padding:16px;display:flex;flex-direction:column;gap:12px}
/* (.dep-os-tabs: the common segmented control, controls.go .seg) */
.dep-os-tabs{align-self:flex-start}
.dep-os-tab{padding:6px 14px;font-weight:600}
.dep-variant-row{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap}
.dep-variant-row .hint{font-size:11px;color:var(--muted);flex:1;min-width:200px}
.dep-variant-row select{background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:11px;color:var(--text);outline:none;font-family:inherit}
.dep-cmd-box{display:flex;align-items:flex-start;gap:10px;background:var(--input-bg);border-radius:10px;padding:12px}
.dep-cmd-box code{flex:1;min-width:0;font-family:ui-monospace,Consolas,monospace;font-size:11.5px;color:var(--text);word-break:break-all;line-height:1.5}
.dep-copy-btn{flex-shrink:0}
.dep-manual-box{display:flex;align-items:center;justify-content:space-between;gap:12px;background:var(--input-bg);border-radius:10px;padding:12px}
.dep-manual-box .desc{flex:1;font-size:11px;color:var(--muted);line-height:1.5}
.dep-empty{text-align:center;padding:24px;color:var(--muted2);font-size:12px}
.dep-form{display:flex;flex-direction:column;gap:9px;padding:2px}
.dep-field{display:flex;flex-direction:column;gap:4px}
.dep-field label{font-size:11px;color:var(--muted);font-weight:600}
.dep-field input,.dep-field select{background:var(--input-bg);border:none;border-radius:10px;padding:8px 11px;font-size:12.5px;color:var(--text);outline:none;font-family:inherit}
.dep-box{width:420px}
`

const deploymentPageJS = `
// ── Deployment page ──────────────────────────────────────────────────────
var depKeys = [];          // [{ k, t }]: a key and the (server, tenant) it belongs to
var depSelectedKeyId = null; // "<profile>:<key id>"
var depTargets = [];
var depOsTab = 'windows';
var depWinVariant = 'modern';
var depLinuxVariant = 'modern';
var depServerUrlCache = new Map();

// The public address of a key's server (the one the install command points
// agents at): its profile's URL, else the app's configured server.
async function depServerUrl(pid) {
  pid = pid || homePid();
  if (depServerUrlCache.has(pid)) return depServerUrlCache.get(pid);
  let url = '';
  try {
    const prof = (typeof serverProfiles !== 'undefined' ? serverProfiles : []).find(function (p) { return p.id === pid; });
    if (prof && prof.serverUrl) url = prof.serverUrl.replace(/\/$/, '');
    else { const cfg = await (await fetch('/local/config')).json(); url = (cfg.serverUrl || '').replace(/\/$/, ''); }
  } catch (e) { url = ''; }
  depServerUrlCache.set(pid, url);
  return url;
}

// The header's "new key" button is a real created element (appendChild'd
// after the surrounding markup), not looked up via querySelector on an
// innerHTML-built subtree: a plain string assigned to .innerHTML never
// re-parses into real, queryable nodes (see vault_ui.go / scripts_page.go
// for the same constraint) — only nodes actually made with
// document.createElement can reliably get an event listener attached.
function renderDeploymentPage(pc) {
  const h = document.createElement('div'); h.className = 'page-header';
  h.innerHTML = '<h1>' + TH('nav.deployment') + '</h1><div class="breadcrumb">' + (pgMulti() ? esc(multiFilterLabel()) : activeServerNote()) + '</div>';
  pc.appendChild(h);
  const body = document.createElement('div'); body.className = 'page-body';
  const left = document.createElement('div'); left.className = 'panel';
  left.style.maxWidth = '360px'; left.style.minWidth = '300px';
  const leftHead = document.createElement('div'); leftHead.className = 'panel-header dep-toolbar';
  const leftHeadSpan = document.createElement('span'); leftHeadSpan.textContent = T('deploy.selectKey');
  const newKeyBtn = document.createElement('button'); newKeyBtn.className = 'btn-ghost';
  newKeyBtn.style.cssText = 'width:auto;padding:6px 12px;flex-shrink:0;margin-left:auto';
  newKeyBtn.textContent = T('deploy.newKey');
  newKeyBtn.addEventListener('click', depOpenNewKeyModal);
  leftHead.appendChild(leftHeadSpan); leftHead.appendChild(newKeyBtn);
  const leftBody = document.createElement('div'); leftBody.className = 'panel-body flush dep-key-list'; leftBody.id = 'dep-key-list';
  left.appendChild(leftHead); left.appendChild(leftBody);
  const right = document.createElement('div'); right.className = 'panel'; right.style.flex = '1';
  right.innerHTML = '<div class="panel-header"><span>' + TH('deploy.installCommand') + '</span></div><div class="dep-panel" id="dep-cmd-panel"></div>';
  body.style.display = 'flex'; body.style.gap = '14px'; body.style.alignItems = 'flex-start';
  body.appendChild(left); body.appendChild(right);
  pc.appendChild(body);
  depLoadKeys();
}

async function depApiKeys(t) {
  const r = await pgApi(t, 'GET', '/api/agent/keys');
  if (!r.ok) { const e = new Error('http_' + r.status); e.status = r.status; throw e; }
  const d = await r.json();
  return d?.data || [];
}
function depKeyId(e) { return e.t.pid + ':' + e.k.id; }

async function depLoadKeys() {
  const list = document.getElementById('dep-key-list');
  if (!list) return;
  list.innerHTML = '<div class="dep-empty">' + TH('common.loading') + '</div>';
  const targets = pgTargets();
  const found = [];
  await pgEach(targets, async function (t) { (await depApiKeys(t)).forEach(function (k) { found.push({ k: k, t: t }); }); });
  if (document.getElementById('dep-key-list') !== list) return; // (the page changed meanwhile)
  depTargets = targets;
  const seen = new Set();
  depKeys = [];
  found.forEach(function (f) {
    if (seen.has(f.t.pid + ':' + f.k.id)) return;
    seen.add(f.t.pid + ':' + f.k.id);
    depKeys.push({ k: f.k, t: pgOwnerTarget(f.k, f.t, targets) });
  });
  depKeys.sort(function (a, b) { return String(a.k.name || '').localeCompare(String(b.k.name || '')); });
  if (targets.length === 1 && targets[0].single && targets[0].err) {
    const err = targets[0].err;
    list.innerHTML = '<div class="dep-empty">' + (err.status === 403 ? TH('deploy.noAccess') : TH('status.loadFailed', { error: err.message || String(err) })) + '</div>';
    depRenderCmdPanel();
    return;
  }
  if (!depSelectedKeyId || !depKeys.some(function (e) { return depKeyId(e) === depSelectedKeyId; })) depSelectedKeyId = depKeys.length ? depKeyId(depKeys[0]) : null;
  depRenderKeyList(list);
  depRenderCmdPanel();
}

function depRenderKeyList(list) {
  const rows = depKeys.map(function (e) {
    const k = e.k;
    const card = document.createElement('div'); card.className = 'dep-key-card' + (depKeyId(e) === depSelectedKeyId ? ' sel' : '');
    card.innerHTML = '<span class="nm">' + esc(k.name || String(k.key || '').slice(0, 8)) + '</span>' +
      (k.defaultGroupName ? '<span class="grp">→ ' + esc(k.defaultGroupName) + '</span>' : '') +
      '<span class="cnt">' + (k.deviceCount || 0) + '</span>';
    card.addEventListener('click', function () { depSelectedKeyId = depKeyId(e); depRenderKeyList(list); depRenderCmdPanel(); });
    return { t: e.t, group: '', groupOrder: 0, el: card };
  });
  pgRenderGrouped(list, depTargets, rows, '<div class="dep-empty">' + TH('deploy.noKeys') + '</div>');
}

function depSelectedEntry() { return depKeys.find(function (e) { return depKeyId(e) === depSelectedKeyId; }) || null; }

// -ArgumentList and -BinaryPathName use a single-quoted PowerShell string
// wrapping the embedded double-quoted path/values (instead of PowerShell's
// backtick escape for a nested double quote): the Go source of this JS
// block is a raw string and must never contain a literal backtick.
function depWindowsCmd(origin, key, variant) {
  if (variant === 'x86') return "$m=\"$env:TEMP\\obliance-agent-x86.msi\"; Invoke-WebRequest \"" + origin + "/api/agent/installer/windows-x86.msi\" -OutFile $m -UseBasicParsing; Start-Process msiexec -ArgumentList '/i \"$m\" SERVERURL=\"" + origin + "\" APIKEY=\"" + key + "\" /quiet' -Wait -Verb RunAs; Remove-Item $m";
  if (variant === 'oldtls') return "$m=\"$env:TEMP\\obliance-agent.msi\"; Import-Module BitsTransfer; Start-BitsTransfer -Source \"" + origin + "/api/agent/installer/windows.msi\" -Destination $m; Start-Process msiexec -ArgumentList '/i \"$m\" SERVERURL=\"" + origin + "\" APIKEY=\"" + key + "\" /quiet' -Wait -Verb RunAs; Remove-Item $m";
  if (variant === 'legacy') return "$d=\"C:\\Program Files\\OblianceAgent\"; New-Item -ItemType Directory -Force -Path $d | Out-Null; Import-Module BitsTransfer; Start-BitsTransfer -Source \"" + origin + "/api/agent/download/obliance-legacy.exe\" -Destination \"$d\\obliance-agent.exe\"; New-Service -Name OblianceAgent -BinaryPathName '\"$d\\obliance-agent.exe\" --url " + origin + " --key " + key + "' -StartupType Automatic -DisplayName \"Obliance Monitoring Agent\"; Start-Service OblianceAgent";
  return "$m=\"$env:TEMP\\obliance-agent.msi\"; Invoke-WebRequest \"" + origin + "/api/agent/installer/windows.msi\" -OutFile $m -UseBasicParsing; Start-Process msiexec -ArgumentList '/i \"$m\" SERVERURL=\"" + origin + "\" APIKEY=\"" + key + "\" /quiet' -Wait -Verb RunAs; Remove-Item $m";
}

async function depRenderCmdPanel() {
  const panel = document.getElementById('dep-cmd-panel');
  if (!panel) return;
  const sel = depSelectedEntry();
  if (!sel) { panel.innerHTML = '<div class="dep-empty">' + TH('deploy.pickKeyFirst') + '</div>'; return; }
  const key = sel.k;
  const origin = await depServerUrl(sel.t.pid);
  if (depSelectedEntry() !== sel) return; // (another key picked meanwhile)
  panel.innerHTML = '';
  const tabs = document.createElement('div'); tabs.className = 'dep-os-tabs seg fit';
  [['windows', 'Windows'], ['linux', 'Linux'], ['macos', 'macOS'], ['freebsd', 'FreeBSD']].forEach(function (pair) {
    const b = document.createElement('button'); b.type = 'button'; b.className = 'dep-os-tab' + (depOsTab === pair[0] ? ' on' : '');
    b.textContent = pair[1];
    b.addEventListener('click', function () { depOsTab = pair[0]; depRenderCmdPanel(); });
    tabs.appendChild(b);
  });
  panel.appendChild(tabs);

  if (depOsTab === 'windows') {
    const row = document.createElement('div'); row.className = 'dep-variant-row';
    const hint = document.createElement('div'); hint.className = 'hint';
    hint.textContent = depWinVariant === 'legacy' ? T('deploy.windowsLegacyHint') : depWinVariant === 'oldtls' ? T('deploy.windowsOldTlsHint') : depWinVariant === 'x86' ? T('deploy.windowsX86Hint') : depWinVariant === 'manual' ? T('deploy.manualHint') : T('deploy.windowsHint');
    const sel = document.createElement('select');
    [['modern', 'Windows 10+ (64-bit)'], ['x86', 'Windows 7/10 (32-bit)'], ['oldtls', 'Server 2012/2016'], ['legacy', 'Server 2008 R2'], ['manual', T('deploy.manualOption')]].forEach(function (o) {
      const opt = document.createElement('option'); opt.value = o[0]; opt.textContent = o[1]; sel.appendChild(opt);
    });
    sel.value = depWinVariant;
    sel.addEventListener('change', function () { depWinVariant = sel.value; depRenderCmdPanel(); });
    row.appendChild(hint); row.appendChild(sel);
    panel.appendChild(row);
    if (depWinVariant === 'manual') {
      panel.appendChild(depManualBox('deploy.manualWizardTitle', 'deploy.manualWizardDescription', origin + '/api/agent/installer/wizard.exe?keyId=' + key.id));
    } else {
      panel.appendChild(depCmdBox(depWindowsCmd(origin, key.key, depWinVariant)));
    }
  } else if (depOsTab === 'linux') {
    const row = document.createElement('div'); row.className = 'dep-variant-row';
    const hint = document.createElement('div'); hint.className = 'hint';
    hint.textContent = depLinuxVariant === 'manual' ? T('deploy.linuxManualHint') : T('deploy.linuxHint');
    const sel = document.createElement('select');
    [['modern', 'curl | bash'], ['manual', T('deploy.manualOption')]].forEach(function (o) {
      const opt = document.createElement('option'); opt.value = o[0]; opt.textContent = o[1]; sel.appendChild(opt);
    });
    sel.value = depLinuxVariant;
    sel.addEventListener('change', function () { depLinuxVariant = sel.value; depRenderCmdPanel(); });
    row.appendChild(hint); row.appendChild(sel);
    panel.appendChild(row);
    if (depLinuxVariant === 'manual') {
      panel.appendChild(depManualBox('deploy.linuxManualTitle', 'deploy.linuxManualDescription', origin + '/api/agent/installer/wizard-linux-amd64?keyId=' + key.id));
    } else {
      panel.appendChild(depCmdBox('curl -fsSL "' + origin + '/api/agent/installer/linux?key=' + encodeURIComponent(key.key) + '" | bash'));
    }
  } else if (depOsTab === 'macos') {
    const hint = document.createElement('div'); hint.className = 'hint'; hint.style.marginBottom = '2px'; hint.textContent = T('deploy.macosHint');
    panel.appendChild(hint);
    panel.appendChild(depCmdBox('sudo bash -c "$(curl -fsSL \'' + origin + '/api/agent/installer/macos?key=' + encodeURIComponent(key.key) + '\')"'));
  } else if (depOsTab === 'freebsd') {
    const hint = document.createElement('div'); hint.className = 'hint'; hint.style.marginBottom = '2px'; hint.textContent = T('deploy.freebsdHint');
    panel.appendChild(hint);
    panel.appendChild(depCmdBox('fetch -qo - "' + origin + '/api/agent/installer/freebsd?key=' + encodeURIComponent(key.key) + '" | sh'));
  }
}

function depCmdBox(cmd) {
  const box = document.createElement('div'); box.className = 'dep-cmd-box';
  const code = document.createElement('code'); code.textContent = cmd;
  const btn = document.createElement('button'); btn.className = 'btn-ghost dep-copy-btn'; btn.textContent = T('deploy.copy');
  btn.addEventListener('click', function () {
    reachNativeClipboardWrite(cmd).then(function (ok) {
      if (!ok) { reachNotify(T('deploy.copyFailed'), 'error'); return; }
      btn.textContent = T('deploy.copied');
      setTimeout(function () { btn.textContent = T('deploy.copy'); }, 1500);
    });
  });
  box.appendChild(code); box.appendChild(btn);
  return box;
}

// Wizard/manual downloads sit behind the same authenticated route as the
// web client (agentAdmin router: requireAuth + requireTenant) — Desktop has
// no native authenticated-download plumbing for this HTTP route (unlike the
// agent-tunnel file transfer), so we hand the technician the link to open in
// a browser tab where they are already logged in to this Obliance server,
// same as copying any other link.
function depManualBox(titleKey, descKey, link) {
  const box = document.createElement('div'); box.className = 'dep-manual-box';
  const desc = document.createElement('div'); desc.className = 'desc';
  desc.innerHTML = '<div style="font-weight:600;color:var(--text);margin-bottom:2px">' + esc(T(titleKey)) + '</div>' + esc(T(descKey)) + '<div style="margin-top:6px;font-style:italic">' + esc(T('deploy.manualOpenNote')) + '</div>';
  const btn = document.createElement('button'); btn.className = 'btn-primary'; btn.style.width = 'auto'; btn.style.flexShrink = '0'; btn.textContent = T('deploy.copyLink');
  btn.addEventListener('click', function () {
    reachNativeClipboardWrite(link).then(function (ok) {
      reachNotify(ok ? T('deploy.linkCopied') : T('deploy.copyFailed'), ok ? 'success' : 'error');
    });
  });
  box.appendChild(desc); box.appendChild(btn);
  return box;
}

function depFlattenGroups(nodes, depth, out) {
  (nodes || []).forEach(function (n) {
    out.push({ id: n.id, name: (depth > 0 ? '— '.repeat(depth) : '') + (n.name || ''), });
    if (n.children && n.children.length) depFlattenGroups(n.children, depth + 1, out);
  });
  return out;
}

async function depOpenNewKeyModal() {
  const dest = pgDestPicker(depTargets.length ? depTargets : pgTargets());
  if (!dest.get()) { reachNotify(T('scope.nowhere'), 'warn'); return; }
  const m = scriptsBaseModal('deploy.newKeyTitle', 'dep-box');
  const form = document.createElement('div'); form.className = 'dep-form';
  if (dest.wrap) form.appendChild(dest.wrap);
  const name = document.createElement('div'); name.className = 'dep-field';
  name.innerHTML = '<label>' + esc(T('deploy.field.name')) + '</label>';
  const nameInput = document.createElement('input'); nameInput.type = 'text'; name.appendChild(nameInput);
  const grp = document.createElement('div'); grp.className = 'dep-field';
  grp.innerHTML = '<label>' + esc(T('deploy.field.group')) + '</label>';
  const grpSelect = document.createElement('select');
  const noGrp = document.createElement('option'); noGrp.value = ''; noGrp.textContent = T('deploy.field.noGroup'); grpSelect.appendChild(noGrp);
  grp.appendChild(grpSelect);
  const err = document.createElement('div'); err.className = 'modal-err';
  form.appendChild(name); form.appendChild(grp); form.appendChild(err);
  const actions = document.createElement('div'); actions.className = 'modal-actions';
  const cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.textContent = T('common.cancel');
  cancel.addEventListener('click', m.close);
  const save = document.createElement('button'); save.className = 'btn-primary'; save.textContent = T('deploy.save');
  save.addEventListener('click', async function () {
    const nm = nameInput.value.trim();
    if (!nm) { err.textContent = T('deploy.field.nameRequired'); return; }
    err.textContent = ''; save.disabled = true;
    try {
      const t = dest.get();
      if (!t) { err.textContent = T('scope.nowhere'); save.disabled = false; return; }
      const r = await pgApi(t, 'POST', '/api/agent/keys', { name: nm, defaultGroupId: grpSelect.value ? parseInt(grpSelect.value, 10) : null });
      if (!r.ok) { const e = new Error('http_' + r.status); e.status = r.status; throw e; }
      m.close();
      depLoadKeys();
    } catch (e) {
      err.textContent = (e && e.status === 403) ? T('deploy.forbidden') : T('status.loadFailed', { error: e.message || String(e) });
      save.disabled = false;
    }
  });
  actions.appendChild(cancel); actions.appendChild(save);
  form.appendChild(actions);
  m.box.appendChild(form);
  setTimeout(function () { try { nameInput.focus(); } catch (e) {} }, 0);
  let seq = 0;
  const fillGroups = async function () {
    const my = ++seq;
    while (grpSelect.options.length > 1) grpSelect.remove(1);
    const t = dest.get();
    if (!t) return;
    try {
      const r = await pgApi(t, 'GET', '/api/groups/tree');
      if (!r.ok || my !== seq) return;
      const d = await r.json();
      if (my !== seq) return;
      depFlattenGroups(d?.data || [], 0, []).forEach(function (g) {
        const opt = document.createElement('option'); opt.value = g.id; opt.textContent = g.name; grpSelect.appendChild(opt);
      });
    } catch (e) {}
  };
  dest.onChange(fillGroups);
  fillGroups();
}
`
