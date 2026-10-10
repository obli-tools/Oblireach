package main

// vm_console.go — Hyper-V VM consoles in the Desktop (page side; the Go
// side is vm_console_go.go): Obliance's
// interactive console of a Hyper-V virtual machine (a helper on the host,
// FreeRDP to the VM's console, H.264 to the viewer), here at the same level
// as the other sessions: a tab of its own, detachable, in a mosaic, with the
// keyboard shortcuts of the tabs.
//
// The contracts (Obliance server ≥ 5.1.142, agent ≥ 4.5.87):
//   - GET /api/hyperv/devices/:id/vms: the VMs of a host, [{ vmId, name,
//     state: running | off | saved | paused | transitioning | unknown, ... }].
//     403 without the 'hyperv:view' capability (admins hold every one): then
//     nothing about VMs shows here (the rule: only what the user may see).
//   - POST /api/remote/sessions { deviceId: <host>, protocol: 'vmconsole',
//     vmId: <GUID>[, takeover: true] }: the capability 'remote' (the same as
//     an ObliReach session), then 409 vm_off (off or saved), 404
//     vm_not_found, 409 vm_not_hyperv, 409 vmconsole_disabled (helper missing
//     and its install frozen by policy), 409 vm_console_busy (another user's
//     console is open: an administrator may take it over), 423 privacy_mode,
//     409 without a code (the host agent's live channel is down). One
//     console per VM: the caller's own previous one is replaced.
//   - The tunnel speaks the ObliReach viewer protocol (init, 0x02 H.264
//     frames, mouse / key input) with its own messages: vm_state (preparing,
//     installing {phase, percent}, starting, connecting, connected,
//     reconnecting {attempt}, typing {done, total}, typing_cancelled), and,
//     with the features vm_type_text / vm_sas / vm_release_all (helper
//     protocol 2): type_text {text, layout: the guest's KLID}, type_cancel,
//     sas (Ctrl+Alt+Del), release_all, refresh (a new init and keyframe).
//     Its errors (vm_off, vm_console_lost...) never retry (retry: false).
//
// A VM console is a session tab whose device is its host with _vm { id
// (the GUID, lower case), name, host, guestOs, guestKind }: vmDevice().
// Ctrl+V / Shift+Insert there types the local clipboard's text into the VM
// (vmPasteClipboard: it has no shared clipboard), on the layout and guest
// system kept for that VM (vmLayoutGet / vmGuestGet). Its key is the host's
// and "~vm-<guid>" (devKey, reach_viewer.go), so the host has its ObliReach
// tab, its tool tabs and a tab per VM console, each in one place: tab
// strip, windows (detached_windows.go: kind "vm-<guid>", the VM's name in
// vmName), mosaics (tab_groups.go) and the "+" claims work on keys.
//
// Both consts are injected verbatim after the other tool blocks (buildUI,
// not through Sprintf: '%' is safe): no backtick here.

const vmConsoleCSS = `
/* ── Hyper-V VM consoles (vm_console.go) ── */
.dev-tools.vm-tools{margin-inline-start:4px}
.rsp-item .vm-ico{width:30px;height:30px;border-radius:8px;flex-shrink:0;display:flex;align-items:center;justify-content:center;background:rgb(var(--info-rgb) / .12);color:var(--info)}
.rsp-item .vm-chip{margin-inline-start:auto;flex-shrink:0;font-size:10px;font-weight:700;letter-spacing:.3px;padding:2px 7px;border-radius:6px;background:rgb(var(--ink) / .08);color:var(--muted)}
.rsp-item .vm-chip.running{background:rgb(var(--success-rgb) / .14);color:var(--success)}
.rsp-item .vm-chip.paused{background:rgb(var(--warn-rgb) / .14);color:var(--warn)}
.vm-pick-note{font-size:12px;color:var(--muted);padding:6px 2px}
.vm-type-form{display:flex;flex-direction:column;gap:9px}
.vm-type-area{width:100%;box-sizing:border-box;min-height:110px;resize:vertical;background:var(--input-bg);border:none;border-radius:10px;padding:8px 11px;font-size:12.5px;color:var(--text);font-family:monospace;outline:none}
.vm-type-area:focus{box-shadow:0 0 0 2px var(--accent)}
.vm-type-row{display:flex;align-items:center;gap:8px;font-size:11.5px;color:var(--muted)}
.vm-type-row select{flex:1;min-width:0;background:var(--input-bg);border:none;border-radius:8px;padding:6px 9px;font-size:12px;color:var(--text);cursor:pointer}
.vm-type-hint{font-size:11px;color:var(--muted2);line-height:1.4}
.vm-type-err{font-size:11px;color:var(--danger)}
.vm-type-progress{font-size:11.5px;color:var(--muted)}
`

const vmConsoleJS = `
// ── Hyper-V VM consoles (vm_console.go; Go: vm_console_go.go) ──────────────
var VM_GUID_RE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
var VM_KIND_RE = /^vm-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
// The first opening on a host installs the console component (about 50 MB):
// the web viewer's 8 minutes, armed again by every progress message.
var VM_CONNECT_TIMEOUT_MS = 480000;
// Automatic reconnections of a VM console tab (a NEW session each), only
// after a connection that showed a picture; reset by Retry only.
var VM_AUTO_RECONNECT_MAX = 3;
var VM_LIST_TTL_MS = 60000;
var VM_TYPE_TEXT_MAX = 20000; // the helper's bound (agent vmConsoleMaxTypeText)
var VM_NAME_MAX = 100;
// What a VM console's agent says it understands (init.features).
var VM_FEATURE = Object.freeze({ CONSOLE: 'vm_console', TYPE_TEXT: 'vm_type_text', SAS: 'vm_sas', RELEASE_ALL: 'vm_release_all', STATUS: 'vm_status' });
var VM_ICON = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><rect x="6" y="7" width="12" height="6" rx="1"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>';
var VM_TYPE_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="8" y="2" width="8" height="4" rx="1"/><path d="M16 4h2a2 2 0 012 2v14a2 2 0 01-2 2H6a2 2 0 01-2-2V6a2 2 0 012-2h2"/><path d="M9 12v-1h6v1"/><path d="M11 17h2"/><path d="M12 11v6"/></svg>';
var VM_REFRESH_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M23 4v6h-6"/><path d="M1 20v-6h6"/><path d="M3.51 9a9 9 0 0114.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0020.49 15"/></svg>';
// A VM's state, in words (GET /api/hyperv/.../vms).
var VM_STATE_TEXT = {
  running: 'vm.state.running', paused: 'vm.state.paused', off: 'vm.state.off', saved: 'vm.state.saved',
  transitioning: 'vm.state.transitioning', unknown: 'vm.state.unknown',
};
// The guest's keyboard layouts of "Type text" (Windows KLIDs; the web's list,
// client/src/components/remote/vmKeyboardLayouts.ts). The names are what
// Windows shows: not translated.
var VM_LAYOUTS = [
  { klid: '0000040C', label: 'Français (France) — AZERTY', langs: ['fr-fr', 'fr'] },
  { klid: '0000080C', label: 'Français (Belgique)', langs: ['fr-be', 'nl-be'] },
  { klid: '0000100C', label: 'Français (Suisse)', langs: ['fr-ch'] },
  { klid: '00001009', label: 'Français (Canada)', langs: ['fr-ca'] },
  { klid: '00000409', label: 'English (US) — QWERTY', langs: ['en-us', 'en'] },
  { klid: '00000809', label: 'English (UK)', langs: ['en-gb', 'en-ie'] },
  { klid: '00020409', label: 'English (US International)', langs: [] },
  { klid: '00000407', label: 'Deutsch (Deutschland) — QWERTZ', langs: ['de-de', 'de', 'de-at'] },
  { klid: '00000807', label: 'Deutsch (Schweiz)', langs: ['de-ch'] },
  { klid: '0000040A', label: 'Español (España)', langs: ['es-es', 'es'] },
  { klid: '0000080A', label: 'Español (Latinoamérica)', langs: ['es-mx', 'es-419', 'es-ar', 'es-co'] },
  { klid: '00000410', label: 'Italiano', langs: ['it'] },
  { klid: '00000816', label: 'Português (Portugal)', langs: ['pt-pt'] },
  { klid: '00000416', label: 'Português (Brasil ABNT)', langs: ['pt-br', 'pt'] },
  { klid: '00000413', label: 'Nederlands', langs: ['nl-nl', 'nl'] },
  { klid: '00000406', label: 'Dansk', langs: ['da'] },
  { klid: '0000041D', label: 'Svenska', langs: ['sv'] },
  { klid: '00000414', label: 'Norsk', langs: ['nb', 'no', 'nn'] },
  { klid: '0000040B', label: 'Suomi', langs: ['fi'] },
  { klid: '00000415', label: 'Polski (programisty)', langs: ['pl'] },
  { klid: '00000405', label: 'Čeština', langs: ['cs'] },
  { klid: '0000041F', label: 'Türkçe (Q)', langs: ['tr'] },
  { klid: '00000419', label: 'Русский', langs: ['ru'] },
  { klid: '00000422', label: 'Українська', langs: ['uk'] },
  { klid: '00000411', label: '日本語', langs: ['ja'] },
  { klid: '00000412', label: '한국어', langs: ['ko'] },
  { klid: '00000804', label: '中文 (简体) — US', langs: ['zh', 'zh-cn'] },
  { klid: '00000401', label: 'العربية (101)', langs: ['ar'] },
];
var VM_LAYOUT_KEY = 'oblireach.vm.layout';
// The layout matching the app's language (the operator's guests usually share it).
function vmGuessLayout(langs) {
  var list = Array.isArray(langs) ? langs : [];
  for (var i = 0; i < list.length; i++) {
    var lang = String(list[i] || '').toLowerCase();
    if (!lang) continue;
    var hit = null, base = lang.split('-')[0];
    VM_LAYOUTS.forEach(function (l) { if (!hit && l.langs.indexOf(lang) >= 0) hit = l; });
    VM_LAYOUTS.forEach(function (l) { if (!hit && l.langs.indexOf(base) >= 0) hit = l; });
    if (hit) return hit.klid;
  }
  return '00000409';
}
function vmLayoutValid(k) { return VM_LAYOUTS.some(function (l) { return l.klid === k; }); }
var VM_LAYOUT_US = '00000409';
// The guest's system, from what Hyper-V reports (guestOs: the guest's KVP
// OSName, "Windows Server 2022 Datacenter", or for a Linux guest often its
// distribution alone, "Ubuntu 22.04.4 LTS (Jammy Jellyfish)", "Debian
// GNU/Linux 12"): 'windows' when it says Windows, 'linux' for any other
// name, 'unknown' when there is none (no integration services report).
function vmGuestKind(guestOs) {
  var s = String(guestOs === undefined || guestOs === null ? '' : guestOs).trim();
  if (!s) return 'unknown';
  return /windows/i.test(s) ? 'windows' : 'linux';
}
// What a VM's console knows of it: its GUID and guestOs (vmDevice: _vm; a
// tab moved to another window has no guestOs: the lists' copy is read).
function vmOf(x) {
  var vm = x && x._vm ? x._vm : x;
  return (vm && typeof vm.id === 'string') ? vm : null;
}
function vmGuestOsOf(x) {
  var vm = vmOf(x);
  if (!vm) return '';
  if (vm.guestOs) return vm.guestOs;
  var hit = '';
  try {
    vmLists.forEach(function (c) { (c && c.vms || []).forEach(function (v) { if (!hit && v.id === vm.id && v.guestOs) hit = v.guestOs; }); });
    if (!hit && typeof vmsData !== 'undefined') vmsData.forEach(function (d) {
      d && d.byHost && d.byHost.forEach(function (arr) { (arr || []).forEach(function (v) { if (!hit && v.id === vm.id && v.guestOs) hit = v.guestOs; }); });
    });
  } catch (e) {}
  return hit;
}
// Kept per VM on this computer (its GUID): the guest's keyboard layout and
// its system, as chosen in "Type text".
function vmPrefKey(kind, vm) { return 'oblireach.vm.' + kind + '.' + String(vm.id).toLowerCase(); }
function vmPrefGet(kind, x) {
  var vm = vmOf(x);
  if (!vm || !vmIdOk(vm.id)) return '';
  try { return localStorage.getItem(vmPrefKey(kind, vm)) || ''; } catch (e) { return ''; }
}
function vmPrefSet(kind, x, v) {
  var vm = vmOf(x);
  if (!vm || !vmIdOk(vm.id)) return;
  try { localStorage.setItem(vmPrefKey(kind, vm), v); } catch (e) {}
}
// The guest's system: the one chosen for this VM, else what Hyper-V says
// ('unknown': Windows, the console helper's own default).
function vmGuestGet(x) {
  var g = vmPrefGet('guest', x);
  if (g === 'windows' || g === 'linux') return g;
  return vmGuestKind(vmGuestOsOf(x)) === 'linux' ? 'linux' : 'windows';
}
function vmGuestSet(x, g) { if (g === 'windows' || g === 'linux') vmPrefSet('guest', x, g); }
// Whether a layout was chosen for this VM (else the default applies).
function vmLayoutChosen(x) { return vmLayoutValid(vmPrefGet('layout', x)); }
// The layout of a VM: the one chosen for it; else, for a Linux guest, US
// (its console keymap, unless set otherwise); else the last one chosen on
// this computer; else the app's language's. guest: the system to assume
// (the dialog's choice), else vmGuestGet.
function vmLayoutGet(x, guest) {
  var v = vmPrefGet('layout', x);
  if (vmLayoutValid(v)) return v;
  if (vmOf(x) && (guest || vmGuestGet(x)) === 'linux') return VM_LAYOUT_US;
  try { v = localStorage.getItem(VM_LAYOUT_KEY); if (v && vmLayoutValid(v)) return v; } catch (e) {}
  var langs = [];
  try { if (typeof i18nLang === 'function') langs.push(i18nLang()); } catch (e) {}
  try { if (typeof navigator !== 'undefined' && navigator && navigator.language) langs.push(navigator.language); } catch (e) {}
  return vmGuessLayout(langs);
}
// Kept for this VM, and as this computer's last choice (other VMs' default).
function vmLayoutSet(x, k) {
  if (!vmLayoutValid(k)) return;
  if (vmOf(x)) vmPrefSet('layout', x, k);
  try { localStorage.setItem(VM_LAYOUT_KEY, k); } catch (e) {}
}

// ── The VM device ──
function vmKindOf(id) { return 'vm-' + String(id === undefined || id === null ? '' : id).toLowerCase(); }
function vmIdOk(id) { return typeof id === 'string' && VM_GUID_RE.test(id); }
// A VM's name as the server sends it: shown as text only, no control
// characters, at most 100 characters.
function vmCleanName(s) { return vmCleanText(s, VM_NAME_MAX); }
// Text of the server (a VM's name, its guest system): no control
// characters, at most max characters, shown as text only.
var VM_GUEST_OS_MAX = 200;
function vmCleanText(s, max) {
  var t = String(s === undefined || s === null ? '' : s).replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, '').trim();
  return t.length > max ? t.slice(0, max).trim() : t;
}
function devIsVm(d) { return !!(d && d._vm && typeof d._vm.id === 'string' && d._vm.id); }
// A key (devKey) of a VM console: "<host key>~vm-<guid>".
function keyIsVm(k) { var s = String(k === undefined || k === null ? '' : k), i = s.lastIndexOf('~'); return i >= 0 && VM_KIND_RE.test(s.slice(i + 1)); }
// A key of a terminal / explorer tab (terminal_viewer.go), never a VM console's.
function keyIsTool(k) { return String(k === undefined || k === null ? '' : k).indexOf('~') >= 0 && !keyIsVm(k); }
function vmTabTitle(vmName, hostName) { return T('vm.tabTitle', { vm: vmName, host: hostName }); }
// The device of a VM console: its host's fields (server, tenant, privacy,
// capabilities: every action goes to the host), its own name, no tools, no
// chat (uuid ''), "online" (the console goes through the Obliance agent,
// not ObliReach; the server and the host say whether it can open).
function vmDevice(host, vm) {
  var id = String(vm.id).toLowerCase(), hostName = String((host && host.hostname) || '');
  var name = vmCleanName(vm.name) || id;
  var d = Object.assign({}, host);
  var guestOs = vmCleanText(vm.guestOs, VM_GUEST_OS_MAX);
  // guestOs / guestKind: what Hyper-V says the guest runs (the default of
  // "Type text"'s guest system and keyboard: vmGuestGet, vmLayoutGet).
  d._vm = { id: id, name: name, host: hostName, guestOs: guestOs, guestKind: vmGuestKind(guestOs) };
  d._host = devIsVm(host) ? host._host : host;
  d.hostname = vmTabTitle(name, hostName);
  d.uuid = '';
  d._tools = false;
  d.oblireach = { installed: true, online: true, version: null, updateAvailable: false, sessions: [] };
  return d;
}
// The host of a VM console as the lists know it now (else as it was).
function vmHostOf(dev) {
  if (!devIsVm(dev)) return dev;
  var fresh = (typeof reachFindDevice === 'function') ? reachFindDevice(dev.id, devPid(dev)) : null;
  return fresh || dev._host || dev;
}
// What a session window of a VM console is opened with (detached_windows.go).
function vmWindowOpts(dev) { return devIsVm(dev) ? { kind: vmKindOf(dev._vm.id), vmName: dev._vm.name } : {}; }

// ── Who may see the VMs of a device ──
// A Windows device with the Obliance agent (a modern one), listed with its
// tools (?agents=1), where the user holds 'remote'. The server answers the
// rest (403 without 'hyperv:view': nothing shows).
function vmHostCandidate(d) {
  if (!d || devIsVm(d) || (typeof devIsLocal === 'function' && devIsLocal(d))) return false;
  if (!d._tools || devOsKind(d) !== 'windows' || devLegacy(d)) return false;
  return devCan(d, 'remote');
}
// Why the console of a VM of this host cannot open now ('' when it can).
function vmHostReason(host) {
  if (!devAgentReachable(host) || (host && host.agent && host.agent.live === false)) return T('vm.reason.hostOffline');
  if (!devCan(host, 'remote')) return T('tools.reason.notPermitted');
  var pv = devPrivacy(host);
  if (pv.enabled && !pv.passwordSet) return T('tools.reason.privacyBlocked');
  return '';
}
// A console opens on a running or paused VM (the server refuses off / saved).
function vmCanOpen(vm) { return !!vm && (vm.state === 'running' || vm.state === 'paused'); }
// The account of the device's server is an administrator (takeover).
function vmIsAdmin(dev) {
  try {
    var st = (typeof srvState === 'function') ? srvState(devPid(dev)) : null;
    var me = st && st.me, p = st && st.mePayload;
    var role = (me && me.role) || (p && ((p.user && p.user.role) || p.role)) || '';
    return role === 'admin';
  } catch (e) { return false; }
}

// ── The VM list of a host (GET /api/hyperv/devices/:id/vms, its server) ──
var vmLists = new Map();   // devKey(host) -> { at, status: 'host' | 'none' | 'denied' | 'error', vms }
var vmListPending = new Map();
function vmNormalize(list) {
  var out = [], seen = {};
  (Array.isArray(list) ? list : []).forEach(function (v) {
    if (!v || typeof v !== 'object' || !vmIdOk(v.vmId)) return;
    var id = v.vmId.toLowerCase();
    if (seen[id]) return;
    seen[id] = true;
    var st = (typeof v.state === 'string' && Object.prototype.hasOwnProperty.call(VM_STATE_TEXT, v.state)) ? v.state : 'unknown';
    out.push({ id: id, name: vmCleanName(v.name) || id, state: st, guestOs: vmCleanText(v.guestOs, VM_GUEST_OS_MAX) });
  });
  out.sort(function (a, b) { return a.name.localeCompare(b.name); });
  return out;
}
async function vmLoad(host, force) {
  var key = devKey(host), c = vmLists.get(key);
  if (!force && c && Date.now() - c.at < VM_LIST_TTL_MS) return c;
  if (vmListPending.has(key)) return vmListPending.get(key);
  var p = (async function () {
    var out = { at: Date.now(), status: 'error', vms: [] };
    try {
      var r = await devApi(host, 'GET', '/api/hyperv/devices/' + encodeURIComponent(String(host.id)) + '/vms', undefined, true);
      if (r && r.ok) {
        var j = null;
        try { j = await r.json(); } catch (e) {}
        out.vms = vmNormalize(j && j.data);
        out.status = out.vms.length ? 'host' : 'none';
      } else if (r && (r.status === 401 || r.status === 403 || r.status === 404)) out.status = 'denied';
    } catch (e) {}
    // (a failed read is not kept: the next look asks again)
    if (out.status !== 'error') vmLists.set(key, out); else vmLists.delete(key);
    return out;
  })();
  vmListPending.set(key, p);
  try { return await p; } finally { vmListPending.delete(key); }
}
// The host's enumeration asked again (POST .../live: an ephemeral one, no
// task), then the list read again.
async function vmRefresh(host) {
  try { await devApi(host, 'POST', '/api/hyperv/devices/' + encodeURIComponent(String(host.id)) + '/live', undefined, true); } catch (e) {}
  await new Promise(function (res) { setTimeout(res, 1500); });
  return vmLoad(host, true);
}

// ── Where the VMs are offered: the device page's header, the session toolbar ──
// Shown only once the server listed VMs of this host for this user.
function vmHeaderTools(dev, hdr) {
  if (!vmHostCandidate(dev) || !hdr) return null;
  var g = document.createElement('div'); g.className = 'dev-tools vm-tools'; g.id = 'dev-vm-tools'; g.style.display = 'none';
  hdr.insertBefore(g, hdr.querySelector('.dev-meta'));
  vmLoad(dev).then(function (c) {
    if (!c || c.status !== 'host' || g.isConnected === false) return;
    var b = document.createElement('button'); b.className = 'tool-hbtn'; b.id = 'hdr-tool-vms';
    b.innerHTML = VM_ICON + '<span>' + TH('vm.button') + '</span>';
    b.title = T('vm.buttonTitle', { n: c.vms.length });
    b.addEventListener('click', function () { vmPickAndOpen(dev); });
    g.appendChild(b);
    g.style.display = '';
  });
  return g;
}
// The session toolbar of a host (focus mode and session windows hide the header).
function vmToolbarButton(toolbar, dev) {
  if (!toolbar || !vmHostCandidate(dev)) return null;
  var b = document.createElement('button');
  b.className = 'btn-sm reach-ico-btn reach-side-btn'; b.id = 'reach-vms-btn'; b.style.display = 'none';
  b.innerHTML = VM_ICON + '<span>' + TH('vm.button') + '</span>';
  b.addEventListener('click', function () { vmPickAndOpen(dev); });
  toolbar.appendChild(b);
  vmLoad(dev).then(function (c) {
    if (!c || c.status !== 'host') return;
    b.title = T('vm.buttonTitle', { n: c.vms.length });
    b.style.display = '';
  });
  return b;
}

// ── The VM picker (built like the Windows session picker, same classes) ──
// Resolves to the VM chosen, or null (cancelled).
function vmPick(host) {
  if (reachPickerDone) reachPickerDone(null);
  return new Promise(function (resolve) {
    var ov = document.createElement('div');
    ov.className = 'modal-overlay visible reach-sess-picker vm-picker'; ov.id = 'vm-picker';
    var box = document.createElement('div'); box.className = 'modal-box';
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T('vm.pick.title'));
    var head = document.createElement('div'); head.className = 'modal-head';
    var ico = document.createElement('div'); ico.className = 'icon'; ico.innerHTML = VM_ICON.replace(/width="13" height="13"/, 'width="18" height="18"');
    var h = document.createElement('h3'); h.textContent = T('vm.pick.title');
    var x = document.createElement('button'); x.className = 'close'; x.id = 'vmp-close'; x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    head.appendChild(ico); head.appendChild(h); head.appendChild(x);
    var desc = document.createElement('p'); desc.className = 'desc'; desc.id = 'vmp-desc';
    desc.textContent = T('vm.pick.desc', { device: reachDevName(host) });
    var list = document.createElement('div'); list.className = 'rsp-list'; list.id = 'vmp-list';
    var items = [];
    var finish = function (v) {
      if (reachPickerDone !== finish) return;
      reachPickerDone = null;
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    var note = function (key) { var n = document.createElement('div'); n.className = 'vm-pick-note'; n.id = 'vmp-note'; n.textContent = T(key); list.appendChild(n); };
    var fill = function (c) {
      list.innerHTML = '';
      items = [];
      if (!c || c.status === 'error') { note('vm.pick.error'); return; }
      if (c.status === 'denied') { note('vm.pick.denied'); return; }
      if (!c.vms.length) { note('vm.pick.empty'); return; }
      var hostWhy = vmHostReason(host);
      c.vms.forEach(function (vm) {
        var b = document.createElement('button');
        var can = vmCanOpen(vm) && !hostWhy;
        b.className = 'rsp-item' + (can ? '' : ' off');
        b.id = 'vmp-' + vm.id;
        var ic = document.createElement('span'); ic.className = 'vm-ico'; ic.innerHTML = VM_ICON;
        var tx = document.createElement('span'); tx.className = 'rsp-text';
        var u = document.createElement('span'); u.className = 'rsp-user'; u.textContent = vm.name;
        var m = document.createElement('span'); m.className = 'rsp-meta';
        m.textContent = can ? T('vm.pick.open') : (hostWhy || T('vm.reason.notRunning'));
        tx.appendChild(u); tx.appendChild(m);
        var chip = document.createElement('span'); chip.className = 'vm-chip ' + vm.state; chip.textContent = T(VM_STATE_TEXT[vm.state] || 'vm.state.unknown');
        b.appendChild(ic); b.appendChild(tx); b.appendChild(chip);
        b.title = vm.name + ' — ' + chip.textContent;
        if (!can) { b.disabled = true; b.setAttribute('aria-disabled', 'true'); }
        else items.push(b);
        b.addEventListener('click', function () { if (!b.disabled) finish(vm); });
        list.appendChild(b);
      });
    };
    var acts = document.createElement('div'); acts.className = 'modal-actions';
    var refresh = document.createElement('button'); refresh.className = 'btn-ghost'; refresh.id = 'vmp-refresh'; refresh.textContent = T('vm.pick.refresh');
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'vmp-cancel'; cancel.textContent = T('common.cancel');
    acts.appendChild(refresh); acts.appendChild(cancel);
    box.appendChild(head); box.appendChild(desc); box.appendChild(list); box.appendChild(acts);
    ov.appendChild(box);
    x.addEventListener('click', function () { finish(null); });
    cancel.addEventListener('click', function () { finish(null); });
    refresh.addEventListener('click', function () {
      if (refresh.disabled) return;
      refresh.disabled = true; list.innerHTML = ''; note('vm.pick.loading');
      vmRefresh(host).then(function (c) { if (reachPickerDone === finish) fill(c); }).finally(function () { refresh.disabled = false; });
    });
    ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) finish(null); }); // the backdrop
    ov.addEventListener('keydown', function (e) {
      if (!e) return;
      var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
      if (e.key === 'Escape') { stop(); finish(null); return; }
      if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && items.length) {
        var i = items.indexOf(document.activeElement);
        var n = e.key === 'ArrowDown' ? (i + 1) % items.length : (i <= 0 ? items.length - 1 : i - 1);
        stop();
        try { items[n].focus(); } catch (x2) {}
      }
    });
    reachPickerDone = finish;
    note('vm.pick.loading');
    document.body.appendChild(ov);
    vmLoad(host).then(function (c) {
      if (reachPickerDone !== finish) return;
      fill(c);
      try { (items[0] || cancel).focus(); } catch (e) {}
    });
  });
}
async function vmPickAndOpen(host) {
  var vm = await vmPick(host);
  if (!vm) return 'cancelled';
  return vmOpen(host, vm, reachDetachedMode ? 'here' : 'auto');
}

// ── Open a VM console ──
// The gates (the VM's state, the host's agent, privacy: its code asked when
// it is locked, the 'remote' feature like an ObliReach session), then its
// tab: where 'auto', the main window's or a session window per "Detach by
// default", 'here': this window. Returns 'main', 'focused', 'opening',
// 'here', 'self', 'blocked', 'cancelled', 'busy', 'error'.
var vmOpening = {};
async function vmOpen(host, vm, where, opts) {
  if (!host || !vm || !vmIdOk(vm.id)) return 'none';
  if (!vmCanOpen(vm)) { toolsSay(opts, T('vm.reason.notRunning')); return 'blocked'; }
  var why = vmHostReason(host);
  if (why) { toolsSay(opts, why); return 'blocked'; }
  var dev = vmDevice(host, vm), key = devKey(dev);
  if (vmOpening[key]) return 'busy';
  vmOpening[key] = true;
  try {
    if (!(await privacyEnsure(host, 'remote', opts))) return 'cancelled';
    if (where === 'here' || reachDetachedMode) return await vmOpenHere(dev);
    if (reachDetachOn()) {
      var ex = await reachLaunchExisting(key);
      if (ex) return ex;
      return (await reachOpenDetached(dev, -1, false)) ? 'opening' : 'error';
    }
    var mine = reachTabOfDevice(key);
    if (mine) { switchSessionTab(mine.id); return 'main'; }
    var shown = false;
    try { shown = await reachNative('reachFocusWindow', key); } catch (e) {}
    if (shown === true) return 'focused';
    openDeviceDetail(dev, 'remote');
    var tab = addSessionTab(dev);
    tab.reconnectAttempts = 0;
    startRemote(undefined, tab);
    return 'main';
  } finally {
    delete vmOpening[key];
  }
}
// In this window (a session window: claimed first, like the "+" list does).
async function vmOpenHere(dev) {
  var key = devKey(dev);
  var mine = reachTabOfDevice(key);
  if (mine) { reachWinShowTab(mine); return 'self'; }
  var where = 'here';
  if (reachDetachedMode && typeof window.reachOpenTab === 'function' && reachNativeToken()) {
    try { where = String(await reachNative('reachOpenTab', key)); }
    catch (e) { reachToast(T('viewer.pickOpenFailed', { error: reachErrText(e) }), 'warn'); return 'error'; }
  }
  if (where === 'self') { var t = reachTabOfDevice(key); if (t) reachWinShowTab(t); return where; }
  if (where !== 'here') return where; // brought to the front where it is
  reachWinConnect(dev, -1, false);
  return 'here';
}
// A VM console moved to this window (__reachAdoptTab): its host found here.
async function vmWinAdopt(id, vmId, vmName, viewOnly, profile) {
  var key = devKeyOf(profile, id) + '~' + vmKindOf(vmId);
  var had = reachTabOfDevice(key);
  if (had) { reachWinShowTab(had); return had; }
  reachWinClaims.add(key); // the app moved it here: ours until its tab exists
  try {
    var host = await reachAdoptFind(id, profile);
    if (reachDetachedMode) devPickerClose();
    return reachWinConnect(vmDevice(host, { id: vmId, name: vmName }), -1, viewOnly);
  } catch (e) {
    reachToast(T('viewer.adoptFailed', { error: reachErrText(e) }), 'warn');
    return null;
  } finally {
    reachWinClaims.delete(key);
    reachReportTabs();
  }
}

// ── Starting a VM console (startRemote, reach_viewer.go) ──
// The POST body of a VM console.
function vmStartBody(dev, tab) {
  var b = { deviceId: dev.id, protocol: 'vmconsole', vmId: dev._vm.id };
  if (tab && tab.vmTakeover) b.takeover = true;
  return b;
}
// Why the server refused to open a VM console, in words.
function vmStartErrorText(status, d) {
  var code = (d && typeof d.code === 'string') ? d.code : '';
  var known = reachCodeText(REACH_ERROR_TEXT, code);
  if (code === 'vm_console_busy') return T('vm.err.busy');
  // (the server's text carries the host's agent version and the minimum)
  if (code === 'vm_console_agent_outdated') return T('vm.err.agentOutdated') + ((d && typeof d.error === 'string' && d.error) ? ' (' + d.error + ')' : '');
  if (known) return known;
  if (status === 423) return T('viewer.err.privacyMode');
  // Any other coded refusal: the server's words, never "host offline".
  if (code && d && typeof d.error === 'string' && d.error) return d.error;
  // The other 409 of the route: the host agent's live channel is down (the
  // legacy agent's refusal keeps its own words).
  if (status === 409 && !(d && typeof d.error === 'string' && /legacy/i.test(d.error))) return T('vm.err.hostOffline');
  return (d && typeof d.error === 'string' && d.error) || ('HTTP ' + status);
}
// A dialog of this file (the vault's look): { box, close }; onClose runs
// once, however it closes (a button, ×, Esc, the backdrop).
function vmModal(titleKey, id, onClose) {
  var ov = document.createElement('div'); ov.className = 'modal-overlay visible vm-modal'; ov.id = id;
  var box = document.createElement('div'); box.className = 'modal-box';
  box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T(titleKey));
  var head = document.createElement('div'); head.className = 'modal-head';
  var h = document.createElement('h3'); h.textContent = T(titleKey);
  var x = document.createElement('button'); x.className = 'close'; x.id = id + '-x'; x.title = T('common.close');
  x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
  head.appendChild(h); head.appendChild(x);
  box.appendChild(head);
  ov.appendChild(box);
  var closed = false, onKey = null;
  var close = function () {
    if (closed) return;
    closed = true;
    if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
    if (onKey && typeof document.removeEventListener === 'function') document.removeEventListener('keydown', onKey);
    if (typeof onClose === 'function') onClose();
  };
  onKey = function (e) { if (e && e.key === 'Escape') close(); };
  x.addEventListener('click', close);
  ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) close(); });
  if (typeof document.addEventListener === 'function') document.addEventListener('keydown', onKey);
  document.body.appendChild(ov);
  return { box: box, close: close, overlay: ov };
}
// An administrator is asked whether to take the console over (the other
// user is disconnected). Resolves true / false.
function vmConfirmTakeover(dev, serverText) {
  return new Promise(function (resolve) {
    var answer = false;
    var m = vmModal('vm.takeover.title', 'vm-takeover', function () { resolve(answer); });
    var finish = function (v) { answer = v; m.close(); };
    var p = document.createElement('p'); p.className = 'desc'; p.id = 'vm-takeover-text';
    p.textContent = T('vm.takeover.text', { vm: dev._vm.name });
    m.box.appendChild(p);
    if (serverText) { var s = document.createElement('p'); s.className = 'desc'; s.id = 'vm-takeover-detail'; s.textContent = String(serverText).slice(0, 300); m.box.appendChild(s); }
    var actions = document.createElement('div'); actions.className = 'modal-actions';
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'vm-takeover-cancel'; cancel.textContent = T('common.cancel');
    cancel.addEventListener('click', function () { finish(false); });
    var ok = document.createElement('button'); ok.className = 'exp-btn-danger'; ok.id = 'vm-takeover-ok'; ok.textContent = T('vm.takeover.confirm');
    ok.addEventListener('click', function () { finish(true); });
    actions.appendChild(cancel); actions.appendChild(ok);
    m.box.appendChild(actions); // (the backdrop, Esc or ×: not taken over)
  });
}
// A refusal of the POST for a VM console: what startRemote does next.
// 'retry': asked again (taken over, or privacy unlocked); else the text to
// show (and no automatic retry: every VM refusal is the server's answer).
async function vmStartRefused(tab, status, d) {
  var dev = tab.device;
  if (status === 409 && d && d.code === 'vm_console_busy' && !tab.vmTakeover && vmIsAdmin(dev)) {
    if (await vmConfirmTakeover(dev, d.error)) { tab.vmTakeover = true; return 'retry'; }
    return T('vm.err.busy');
  }
  if (status === 423 && !tab.vmPrivacyAsked) {
    tab.vmPrivacyAsked = true;
    var fresh = await privacyRetryGate(vmHostOf(dev), 'remote');
    if (fresh) { dev._host = fresh; return 'retry'; }
  }
  return vmStartErrorText(status, d);
}

// ── The console's messages ──
function vmPhaseText(phase) { return T(phase === 'verify' ? 'vm.phase.verify' : phase === 'extract' ? 'vm.phase.extract' : 'vm.phase.download'); }
// The overlay of a vm_state: { title, text }.
function vmStateTexts(s) {
  var st = s && s.state;
  if (st === 'installing') {
    var pc = (typeof s.percent === 'number' && isFinite(s.percent)) ? Math.max(0, Math.min(100, Math.round(s.percent))) : 0;
    return { title: T('vm.wait.installingTitle'), text: T('vm.wait.installingText', { phase: vmPhaseText(s.phase), percent: pc }) };
  }
  if (st === 'starting' || st === 'connecting') return { title: T('vm.wait.connectingTitle'), text: T('vm.wait.connectingText') };
  if (st === 'reconnecting') return { title: T('vm.wait.reconnectingTitle'), text: T('vm.wait.reconnectingText', { n: (s.attempt | 0) || 1 }) };
  if (st === 'preparing') return { title: T('vm.wait.openingTitle'), text: T('vm.wait.preparingText') };
  return { title: T('vm.wait.openingTitle'), text: T('vm.wait.openingText') };
}
// The agent answered (paired): the VM's own words in place of ObliReach's.
function vmOnPaired(tab) {
  var t = vmStateTexts(tab.vmState);
  reachSetStatus(tab, t.title);
  reachShowOverlay(tab, 'busy', t.title, t.text, [reachActCancel(tab)]);
}
function vmOnState(tab, msg) {
  if (!devIsVm(tab.device)) return;
  var st = typeof msg.state === 'string' ? msg.state : '';
  if (!st) return;
  tab.paired = true;
  if (st === 'typing') {
    var done = Math.max(0, msg.done | 0), total = Math.max(0, msg.total | 0), end = total > 0 && done >= total;
    // A secret of the vault: no progress and no count ever shown (its length).
    tab.vmTyping = (!tab.vmVaultTyping && total > 0 && !end) ? { done: done, total: total } : null;
    if (end && reachUI(tab)) reachToast(tab.vmVaultTyping ? T('vm.type.doneSecret') : T('vm.type.done', { n: total }), 'ok');
    if (end) tab.vmVaultTyping = false;
    vmRefreshTools(tab);
    if (vmTypeDialog && vmTypeDialog.tab === tab) vmTypeDialog.paint();
    return;
  }
  if (st === 'typing_cancelled') {
    tab.vmTyping = null; tab.vmVaultTyping = false;
    if (reachUI(tab)) reachToast(T('vm.type.cancelled'));
    vmRefreshTools(tab);
    if (vmTypeDialog && vmTypeDialog.tab === tab) vmTypeDialog.paint();
    return;
  }
  if (st === 'connected') {
    tab.vmState = null;
    if (tab.overlay && tab.overlay.vm) reachHideOverlay(tab);
    if (tab.gotInit) reachSetStatus(tab, reachStreamStatus(tab));
    return;
  }
  tab.vmState = {
    state: st,
    phase: typeof msg.phase === 'string' ? msg.phase : '',
    percent: typeof msg.percent === 'number' ? msg.percent : null,
    attempt: typeof msg.attempt === 'number' ? msg.attempt : 0,
  };
  // Every progress message keeps the launch alive (a first install takes minutes).
  if (!tab.gotInit) reachArmConnectTimer(tab, VM_CONNECT_TIMEOUT_MS);
  var t = vmStateTexts(tab.vmState);
  reachSetStatus(tab, t.title);
  // Streaming already (the VM restarts): the picture stays under the notice.
  reachShowOverlay(tab, 'busy', t.title, t.text, [tab.gotInit ? reachActDisconnect(tab) : reachActCancel(tab)]);
  if (tab.overlay) tab.overlay.vm = true;
}

// ── The console's tools (session toolbar of a VM console) ──
// "Type text" (a VM console has no shared clipboard: the text is typed on
// the guest's keyboard layout), "Refresh the picture". Ctrl+Alt+Del is the
// toolbar's own button (sendSystemKeys: {type:'sas'}).
function vmBuildTools(toolbar) {
  var tb = document.createElement('button');
  tb.className = 'btn-sm reach-ico-btn'; tb.id = 'vm-type-btn'; tb.disabled = true;
  tb.title = T('vm.type.buttonTitle'); tb.setAttribute('aria-label', T('vm.type.buttonTitle'));
  tb.innerHTML = VM_TYPE_ICON + '<span>' + TH('vm.type.button') + '</span>';
  tb.addEventListener('click', function () { var t = reachLiveTab(); if (t && devIsVm(t.device)) vmOpenTypeText(t); });
  toolbar.appendChild(tb);
  var rb = document.createElement('button');
  rb.className = 'btn-sm reach-ico-btn'; rb.id = 'vm-refresh-btn'; rb.disabled = true;
  rb.title = T('vm.refresh'); rb.setAttribute('aria-label', T('vm.refresh'));
  rb.innerHTML = VM_REFRESH_ICON;
  rb.addEventListener('click', function () { var t = reachLiveTab(); if (t) vmSendRefresh(t); });
  toolbar.appendChild(rb);
}
// The tools follow the tab on screen (reachRefreshToolbar).
function vmRefreshTools(tab) {
  if (!tab || !reachUI(tab)) return;
  var live = !!(tab.ws && tab.gotInit && devIsVm(tab.device));
  var tb = document.getElementById('vm-type-btn');
  if (tb) {
    var can = live && reachHas(tab, VM_FEATURE.TYPE_TEXT);
    tb.disabled = !can || !!tab.viewOnly;
    tb.title = !live ? T('vm.type.buttonTitle') : !reachHas(tab, VM_FEATURE.TYPE_TEXT) ? T('vm.type.unavailable') : T('vm.type.buttonTitle');
    var lbl = tab.vmTyping ? T('vm.type.progressShort', { done: tab.vmTyping.done, total: tab.vmTyping.total }) : T('vm.type.button');
    tb.innerHTML = VM_TYPE_ICON + '<span>' + i18nEsc(lbl) + '</span>';
  }
  var rb = document.getElementById('vm-refresh-btn');
  if (rb) rb.disabled = !(live && reachHas(tab, VM_FEATURE.RELEASE_ALL));
}
function vmSendRefresh(tab) {
  if (!tab || !devIsVm(tab.device) || !reachHas(tab, VM_FEATURE.RELEASE_ALL)) return false;
  tab.needKey = true;
  return reachSend(tab, { type: 'refresh' });
}
// Keys and mouse buttons the helper still holds are released (the focus
// left the picture, view only turned on).
function vmReleaseAll(tab) {
  if (!tab || !devIsVm(tab.device) || !reachHas(tab, VM_FEATURE.RELEASE_ALL)) return false;
  return reachSend(tab, { type: 'release_all' });
}
// Sends a text to type into the VM (layout: the guest's KLID; guest: its
// system, 'windows' | 'linux': how the helper types there, an older one
// ignores it). Either left out: the ones kept for this VM.
function vmSendTypeText(tab, text, layout, guest) {
  if (!tab || !devIsVm(tab.device) || !reachHas(tab, VM_FEATURE.TYPE_TEXT) || tab.viewOnly) return false;
  var s = String(text || '');
  if (!s || Array.from(s).length > VM_TYPE_TEXT_MAX) return false;
  var g = (guest === 'windows' || guest === 'linux') ? guest : vmGuestGet(tab.device);
  var k = vmLayoutValid(layout) ? layout : vmLayoutGet(tab.device, g);
  if (!reachSend(tab, { type: 'type_text', text: s, layout: k, guest: g })) return false;
  tab.vmTyping = { done: 0, total: Array.from(s).length };
  tab.vmVaultTyping = false;
  vmRefreshTools(tab);
  return true;
}
// The "Type text" dialog: the text (the local clipboard's, when it holds
// text), the guest's keyboard layout (kept per computer), Send / Cancel;
// while a text is being typed, its progress and "Stop typing".
var vmTypeDialog = null; // the "Type text" dialog on screen: { tab, paint, close }
function vmOpenTypeText(tab) {
  if (vmTypeDialog) vmTypeDialog.close();
  var m = vmModal('vm.type.title', 'vm-type-dialog', function () { if (vmTypeDialog && vmTypeDialog.m === m) vmTypeDialog = null; });
  var form = document.createElement('div'); form.className = 'vm-type-form';
  var hint = document.createElement('div'); hint.className = 'vm-type-hint'; hint.textContent = T('vm.type.hint');
  var area = document.createElement('textarea'); area.className = 'vm-type-area'; area.id = 'vm-type-text'; area.placeholder = T('vm.type.placeholder');
  area.setAttribute('aria-label', T('vm.type.title'));
  // The guest's system (kept per VM; the default: what Hyper-V says it runs).
  var dev = tab.device;
  var grow = document.createElement('div'); grow.className = 'vm-type-row';
  var glbl = document.createElement('label'); glbl.textContent = T('vm.type.guest'); glbl.setAttribute('for', 'vm-type-guest');
  var gsel = document.createElement('select'); gsel.id = 'vm-type-guest';
  // (system names: not translated, like the layouts' names)
  [['windows', 'Windows'], ['linux', 'Linux']].forEach(function (g) { var o = document.createElement('option'); o.value = g[0]; o.textContent = g[1]; gsel.appendChild(o); });
  gsel.value = vmGuestGet(dev);
  grow.appendChild(glbl); grow.appendChild(gsel);
  var row = document.createElement('div'); row.className = 'vm-type-row';
  var lbl = document.createElement('label'); lbl.textContent = T('vm.type.layout'); lbl.setAttribute('for', 'vm-type-layout');
  var sel = document.createElement('select'); sel.id = 'vm-type-layout';
  VM_LAYOUTS.forEach(function (l) { var o = document.createElement('option'); o.value = l.klid; o.textContent = l.label; sel.appendChild(o); });
  sel.value = vmLayoutGet(dev, gsel.value);
  row.appendChild(lbl); row.appendChild(sel);
  // A Linux guest without a layout chosen for it: US, said why.
  var lhint = document.createElement('div'); lhint.className = 'vm-type-hint'; lhint.id = 'vm-type-linux-hint'; lhint.textContent = T('vm.type.linuxLayoutHint');
  var layoutTouched = vmLayoutChosen(dev), guestTouched = false;
  var paintGuest = function () { lhint.style.display = (gsel.value === 'linux' && !layoutTouched) ? '' : 'none'; };
  sel.addEventListener('change', function () { layoutTouched = true; paintGuest(); });
  gsel.addEventListener('change', function () {
    guestTouched = true;
    if (!layoutTouched) sel.value = vmLayoutGet(dev, gsel.value); // (nothing chosen for this VM: the new system's default)
    paintGuest();
  });
  paintGuest();
  // Not known here yet (a console moved to this window): the host's VM list read.
  if (!vmPrefGet('guest', dev) && !vmGuestOsOf(dev) && devIsVm(dev)) {
    vmLoad(vmHostOf(dev)).then(function () {
      if (guestTouched || !gsel.isConnected) return;
      gsel.value = vmGuestGet(dev);
      if (!layoutTouched) sel.value = vmLayoutGet(dev, gsel.value);
      paintGuest();
    }, function () {});
  }
  var err = document.createElement('div'); err.className = 'vm-type-err'; err.id = 'vm-type-err'; err.style.display = 'none';
  var prog = document.createElement('div'); prog.className = 'vm-type-progress'; prog.id = 'vm-type-progress'; prog.style.display = 'none';
  form.appendChild(hint); form.appendChild(area); form.appendChild(grow); form.appendChild(row); form.appendChild(lhint); form.appendChild(err); form.appendChild(prog);
  var actions = document.createElement('div'); actions.className = 'modal-actions';
  var stop = document.createElement('button'); stop.className = 'btn-ghost'; stop.id = 'vm-type-stop'; stop.textContent = T('vm.type.stop');
  var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'vm-type-cancel'; cancel.textContent = T('common.cancel');
  var send = document.createElement('button'); send.className = 'reach-btn-primary btn-sm'; send.id = 'vm-type-send'; send.textContent = T('vm.type.send');
  actions.appendChild(stop); actions.appendChild(cancel); actions.appendChild(send);
  m.box.appendChild(form); m.box.appendChild(actions);
  var paint = function () {
    var n = Array.from(area.value || '').length, too = n > VM_TYPE_TEXT_MAX;
    err.style.display = too ? '' : 'none';
    err.textContent = too ? T('vm.type.tooLong', { max: VM_TYPE_TEXT_MAX }) : '';
    send.disabled = !n || too;
    var typing = tab.vmTyping;
    stop.style.display = typing ? '' : 'none';
    prog.style.display = typing ? '' : 'none';
    prog.textContent = typing ? T('vm.type.progress', { done: typing.done, total: typing.total }) : '';
  };
  area.addEventListener('input', paint);
  // Keys typed in the dialog are the dialog's, never the session's.
  area.addEventListener('keydown', function (e) { if (e && e.stopPropagation) e.stopPropagation(); });
  stop.addEventListener('click', function () { reachSend(tab, { type: 'type_cancel' }); });
  cancel.addEventListener('click', function () { m.close(); reachFocusCanvas(); });
  send.addEventListener('click', function () {
    if (send.disabled) return;
    vmGuestSet(dev, gsel.value);
    if (layoutTouched) vmLayoutSet(dev, sel.value); // (an untouched default stays a default: US follows a Linux guest)
    if (vmSendTypeText(tab, area.value, sel.value, gsel.value)) { m.close(); reachFocusCanvas(); }
    else { err.style.display = ''; err.textContent = T('vm.type.failed'); }
  });
  paint();
  vmTypeDialog = { m: m, tab: tab, paint: paint, close: m.close };
  try { area.focus(); } catch (e) {}
  // The local clipboard pre-fills it (the operator copied what to type).
  if (typeof reachNativeClipboardRead === 'function') {
    reachNativeClipboardRead().then(function (r) {
      if (r && typeof r.text === 'string' && r.text && !area.value) { area.value = r.text; paint(); }
    }).catch(function () {});
  }
  return m;
}

// ── Ctrl+V / Shift+Insert in a VM console: the local clipboard typed ──
// A VM console has no shared clipboard (the agent drops clipboard_set): the
// paste gesture (reach_viewer.go sendInputKey) and the Clipboard menu type
// the local clipboard's text instead, on the layout and guest system kept
// for this VM, with "Type text"'s progress. Past VM_PASTE_CONFIRM_CHARS
// characters, asked first (in the page: typing takes a while).
var VM_PASTE_CONFIRM_CHARS = 500;
// The modifiers the operator holds (the Ctrl of Ctrl+V) let go in the VM
// first: typed under a held Ctrl, every character would be a shortcut.
function vmReleaseModifiers(tab) {
  var keys = (tab && tab.keysHeld) || {};
  Object.keys(keys).forEach(function (c) {
    if (!REACH_MOD_CODES[c]) return;
    reachSend(tab, reachKeyMsg(tab, { type: 'key', action: 'up', key: keys[c].key, code: keys[c].code, shift: false, ctrl: false, alt: false, meta: false }));
    delete keys[c];
  });
}
// The confirmation of a long text. Resolves true / false.
function vmConfirmPaste(tab, n, guest, layout) {
  return new Promise(function (resolve) {
    var answer = false;
    var m = vmModal('vm.paste.confirmTitle', 'vm-paste-confirm', function () { resolve(answer); });
    var finish = function (v) { answer = v; m.close(); };
    var lay = VM_LAYOUTS.find(function (l) { return l.klid === layout; });
    var p = document.createElement('p'); p.className = 'desc'; p.id = 'vm-paste-confirm-text';
    p.textContent = T('vm.paste.confirmText', { n: n, vm: (tab.device && tab.device._vm && tab.device._vm.name) || '' });
    var d = document.createElement('p'); d.className = 'desc'; d.id = 'vm-paste-confirm-detail';
    d.textContent = T('vm.paste.confirmDetail', { guest: guest === 'linux' ? 'Linux' : 'Windows', layout: lay ? lay.label : layout });
    var actions = document.createElement('div'); actions.className = 'modal-actions';
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'vm-paste-cancel'; cancel.textContent = T('common.cancel');
    cancel.addEventListener('click', function () { finish(false); });
    var ok = document.createElement('button'); ok.className = 'reach-btn-primary btn-sm'; ok.id = 'vm-paste-ok'; ok.textContent = T('vm.type.send');
    ok.addEventListener('click', function () { finish(true); });
    actions.appendChild(cancel); actions.appendChild(ok);
    m.box.appendChild(p); m.box.appendChild(d); m.box.appendChild(actions);
    try { ok.focus(); } catch (e) {}
  });
}
// Types the local clipboard's text into the VM console of tab. fromKey:
// the paste gesture (its modifiers are released in the VM first).
var vmPasting = new Set(); // tab ids reading / asking right now
async function vmPasteClipboard(tab, fromKey) {
  if (!tab || !devIsVm(tab.device) || tab.viewOnly || !tab.ws || !tab.gotInit) return false;
  if (!reachHas(tab, VM_FEATURE.TYPE_TEXT)) { reachToast(T('vm.type.unavailable'), 'warn'); return false; }
  if (vmPasting.has(tab.id)) return false;
  vmPasting.add(tab.id);
  try {
    var r = await reachNativeClipboardRead();
    if (r && r.tooLarge) { reachToast(T('vm.type.tooLong', { max: VM_TYPE_TEXT_MAX }), 'warn'); return false; }
    var text = String((r && r.text) || '').replace(/\r\n?/g, '\n'); // (Enter once per line)
    if (!text) { reachToast(T('vm.paste.empty'), 'warn'); return false; }
    var n = Array.from(text).length;
    if (n > VM_TYPE_TEXT_MAX) { reachToast(T('vm.type.tooLong', { max: VM_TYPE_TEXT_MAX }), 'warn'); return false; }
    if (tab.vmTyping) { reachToast(T('vm.paste.busy'), 'warn'); return false; }
    var guest = vmGuestGet(tab.device), layout = vmLayoutGet(tab.device, guest);
    if (n > VM_PASTE_CONFIRM_CHARS) {
      var go = await vmConfirmPaste(tab, n, guest, layout);
      reachFocusCanvas();
      if (!go) return false;
      if (!tab.ws || tab.viewOnly || tab.vmTyping) return false; // (closed, view only or typing meanwhile)
    }
    if (fromKey) vmReleaseModifiers(tab);
    if (!vmSendTypeText(tab, text, layout, guest)) { reachToast(T('vm.type.failed'), 'warn'); return false; }
    if (reachUI(tab)) reachToast(T('vm.paste.typing', { n: n }));
    return true;
  } catch (e) {
    return false;
  } finally {
    vmPasting.delete(tab.id);
  }
}
`
