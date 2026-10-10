package main

// agent_tools.go — the Obliance agent's tools on a device, from the Desktop:
// remote terminals (CMD / PowerShell on Windows, "SSH" on Linux and macOS:
// the login shell the Obliance agent opens in a PTY, root / SYSTEM, no
// credentials, exactly what the web's header buttons open) and the file
// explorer. This file holds the device model (what a device offers, and why
// not), the row buttons and states, the CMD / PowerShell picker, the device
// page's tools group and agent pane, and the privacy gate (the device's
// security code, POST /api/devices/:id/privacy/unlock, like the web).
//
// The data: GET /api/reach/overview?agents=1 (recent Obliance servers) lists the
// approved devices without ObliReach too, and on every device agent { live,
// flavor, version }, privacy { enabled, passwordSet } and can { remote, files },
// with features.agents = true. An older server ignores the parameter: every
// device then has _tools false and nothing here shows (the Desktop behaves as
// before). The server stays the authority: these are hints, each action is
// checked again by its own route (capabilities, restrictions and step-up,
// legacy refusal, privacy, audit).
//
// Opening a terminal or explorer tab is toolTabOpen(dev, kind, where) (the
// tool tabs, another file): toolOpen calls it once its gates pass, when it
// exists. Helpers kept for those tabs: privacyIsDenial(text) and
// privacyRetryGate(dev, feature) (the agent refused for privacy: the flags
// read again, the code asked again, one retry).
//
// Both consts are injected verbatim after the viewer library (buildUI, not
// through Sprintf: '%' is safe), in the same <script> / <style>: no backtick.

const toolsCSS = `
/* ── Obliance agent tools (agent_tools.go) ── */
.dot.agent{background:var(--info)}
.tag-chip.agent{background:rgb(var(--info-rgb) / .12);color:var(--info)}
.dt .tool-btn{min-width:30px;height:24px;padding:0 6px;border-radius:7px;background:rgb(var(--ink) / .06);border:none;color:var(--muted);font-size:10px;font-weight:700;letter-spacing:.3px;cursor:pointer;display:inline-flex;align-items:center;justify-content:center;gap:3px;transition:all .1s}
.dt .tool-btn:hover{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.dt .tool-btn:disabled{opacity:.3;cursor:default}
.dt .tool-btn:disabled:hover{background:rgb(var(--ink) / .06);color:var(--muted)}
.dt .icon-btn.tool-ico{width:auto;min-width:26px;padding:0 4px;gap:2px}
.tool-lock{display:inline-flex;align-items:center;color:var(--warn)}
.tool-lock.open{color:var(--success)}
.tool-lock svg{width:9px;height:9px}
.dev-priv{display:inline-flex;align-items:center;flex-shrink:0}
.dev-priv.locked{color:var(--warn)}
.dev-priv.blocked{color:var(--muted2)}
.dev-tools{display:flex;align-items:center;gap:4px;margin-inline-start:10px;flex-shrink:0}
.tool-hbtn{height:26px;padding:0 9px;border-radius:8px;border:none;background:rgb(var(--ink) / .06);color:var(--muted);font-size:11px;font-weight:600;cursor:pointer;display:inline-flex;align-items:center;gap:5px;transition:all .12s}
.tool-hbtn:hover{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.tool-hbtn:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.tool-hbtn:disabled{opacity:.35;cursor:default}
.tool-hbtn:disabled:hover{background:rgb(var(--ink) / .06);color:var(--muted)}
.tool-hbtn svg{flex-shrink:0}
.tools-pane{padding:24px;text-align:center}
.tools-pane .tp-line{font-size:13px;color:var(--muted)}
.tools-pane .tp-sub{font-size:12px;color:var(--muted2)}
.tools-pane .tp-acts{display:flex;gap:12px;flex-wrap:wrap;justify-content:center;margin-top:8px}
.tools-pane .tp-act{display:flex;flex-direction:column;align-items:center;gap:6px;width:170px}
.tools-pane .tp-btn{width:100%;min-height:64px;padding:12px 10px;border-radius:14px;border:none;background:rgb(var(--ink) / .06);color:var(--text);font-size:13px;font-weight:600;cursor:pointer;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:6px;box-shadow:var(--surface-shadow);transition:all .12s}
.tools-pane .tp-btn:hover{background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.tools-pane .tp-btn:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent)}
.tools-pane .tp-btn:disabled{opacity:.4;cursor:default}
.tools-pane .tp-btn:disabled:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.tools-pane .tp-btn .tp-name{display:inline-flex;align-items:center;gap:5px}
.tools-pane .tp-why{font-size:11px;color:var(--muted2);line-height:1.4}
.tools-pane .tp-why.block{font-size:12px;color:var(--warn);margin-top:6px}
.rsp-item:disabled{opacity:.45;cursor:default}
.rsp-item:disabled:hover{background:rgb(var(--ink) / .06)}
.rsp-item .tsp-ico{width:30px;height:30px;border-radius:8px;flex-shrink:0;display:flex;align-items:center;justify-content:center;background:rgb(var(--accent-rgb) / .12);color:var(--accent-l)}
.modal-overlay.ppv-overlay{z-index:360}
.ppv-input{background:var(--input-bg);border:none;border-radius:12px;padding:11px 15px;font-size:13px;color:var(--text);outline:none;width:100%;transition:box-shadow .15s}
.ppv-input:focus{box-shadow:0 0 0 2px var(--accent)}
.ppv-input:disabled{opacity:.6}
.modal-box p.ppv-hint{font-size:11px;color:var(--muted2)}
.info-live{padding:0 16px 16px;display:flex;flex-direction:column;gap:9px;font-size:12px}
.info-live .il-head{display:flex;align-items:center;gap:7px;font-size:10px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;color:var(--muted2);margin-top:2px}
.info-live .il-pulse{width:7px;height:7px;border-radius:50%;background:var(--muted2);flex-shrink:0}
.info-live .il-pulse.on{background:var(--success);animation:ilPulse 2s ease-in-out infinite}
@keyframes ilPulse{0%,100%{opacity:1}50%{opacity:.35}}
.info-live .il-sect{font-size:10px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;color:var(--muted2);margin-top:4px}
.info-live .il-row{display:grid;grid-template-columns:minmax(40px,76px) 1fr auto;align-items:center;column-gap:8px;row-gap:3px}
.info-live .il-k{color:var(--muted);font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.info-live .il-bar{height:6px;border-radius:3px;background:rgb(var(--ink) / .08);overflow:hidden}
.info-live .il-fill{display:block;height:100%;border-radius:3px;background:var(--info);transition:width .4s ease}
.info-live .il-fill.warn{background:var(--warn)}
.info-live .il-fill.crit{background:var(--danger)}
.info-live .il-v{min-width:38px;text-align:end;font-size:11px;font-weight:600;color:var(--text);font-variant-numeric:tabular-nums}
.info-live .il-sub{grid-column:2 / 4;font-size:10px;color:var(--muted2)}
.info-live .il-spark{grid-column:2 / 4;height:22px;color:var(--info);opacity:.8}
.info-live .il-spark svg{display:block;width:100%;height:22px}
.info-live .il-wait{font-size:11px;color:var(--muted)}
.info-live .il-thr{display:flex;flex-direction:column;gap:5px}
.info-live .il-thr-head{display:flex;align-items:center;gap:6px;background:none;border:none;padding:0;cursor:pointer;color:var(--muted2);font:inherit;font-size:10px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;text-align:start}
.info-live .il-thr-head:hover{color:var(--text)}
.info-live .il-thr-head svg{transition:transform .15s;flex-shrink:0}
.info-live .il-thr.closed .il-thr-head svg{transform:rotate(-90deg)}
.info-live .il-thr-sum{font-size:10px;font-weight:500;letter-spacing:0;text-transform:none;color:var(--muted2)}
.info-live .il-thr.closed .il-thr-body{display:none}
.info-live .il-thr.stale .il-thr-body{opacity:.55}
.info-live .il-cols{display:flex;align-items:flex-end;gap:2px;height:38px}
.info-live .il-col{flex:1;min-width:3px;height:100%;border-radius:2px;background:rgb(var(--ink) / .08);display:flex;align-items:flex-end;overflow:hidden}
.info-live .il-col > span{display:block;width:100%;background:var(--info);transition:height .4s ease}
.info-live .il-col > span.warn{background:var(--warn)}
.info-live .il-col > span.crit{background:var(--danger)}
.info-live .il-heat{display:grid;grid-template-columns:repeat(auto-fill,minmax(11px,1fr));gap:2px;max-height:104px;overflow-y:auto;overscroll-behavior:contain;padding-right:2px}
.info-live .il-cell{height:11px;border-radius:2px;background:rgb(var(--ink) / .08)}
.info-live .il-cell.l1{background:rgb(var(--info-rgb) / .30)}
.info-live .il-cell.l2{background:rgb(var(--info-rgb) / .60)}
.info-live .il-cell.l3{background:var(--info)}
.info-live .il-cell.l4{background:var(--warn)}
.info-live .il-cell.l5{background:var(--danger)}
.info-more{padding:0 16px 16px;display:flex;flex-direction:column;gap:6px;font-size:12px}
.info-more .im-head{display:flex;align-items:center;gap:6px;margin-top:2px}
.info-more .im-toggle{display:flex;align-items:center;gap:7px;flex:1;background:none;border:none;padding:0;cursor:pointer;color:var(--muted2);font:inherit;font-size:10px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;text-align:start}
.info-more .im-toggle:hover{color:var(--text)}
.info-more .im-toggle:focus-visible,.info-more .im-reload:focus-visible,.info-more .im-copy:focus-visible,.info-more .im-link:focus-visible{outline:none;box-shadow:0 0 0 2px var(--accent);border-radius:4px}
.info-more .im-toggle svg{transition:transform .15s;flex-shrink:0}
.info-more.closed .im-toggle svg{transform:rotate(-90deg)}
.info-more .im-reload{width:22px;height:22px;border-radius:6px;border:none;background:none;color:var(--muted2);cursor:pointer;display:inline-flex;align-items:center;justify-content:center}
.info-more .im-reload:hover{background:rgb(var(--ink) / .06);color:var(--text)}
.info-more .im-reload:disabled{opacity:.4;cursor:default}
.info-more .im-body{display:flex;flex-direction:column;gap:4px}
.info-more .im-sect{font-size:10px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;color:var(--muted2);margin-top:8px}
.info-more .im-row{display:grid;grid-template-columns:minmax(60px,104px) 1fr;align-items:baseline;column-gap:8px}
.info-more .im-k{color:var(--muted);font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.info-more .im-v{color:var(--text);min-width:0;overflow-wrap:anywhere}
.info-more .im-copy{cursor:copy;border-radius:4px;margin:-1px -4px;padding:1px 4px;transition:background .12s}
.info-more .im-copy:hover{background:rgb(var(--accent-rgb) / .10)}
.info-more .im-mono{font-family:monospace;font-size:11px}
.info-more .im-dim{color:var(--muted);font-size:11px}
.info-more .im-warn{color:var(--warn)}
.info-more .im-link{justify-self:start;display:inline-flex;align-items:center;gap:4px;background:none;border:none;padding:0;cursor:pointer;color:var(--accent-l);font:inherit;font-size:11px}
.info-more .im-link:hover{text-decoration:underline}
.info-more .im-note{font-size:11px;color:var(--muted2);margin-top:6px}
`

// toolsJS: INFO_HW_RULES (hwinfo.go: the makers and hypervisors table, the
// Info panel's "More details") then the page code.
var toolsJS = hwRulesJS() + `
// ── Obliance agent tools (agent_tools.go) ──────────────────────────────────
var TOOL_KINDS = ['cmd', 'powershell', 'ssh', 'files', 'rdp']; // (rdp: a local RDP connection's tab, rdp_view.go)
function toolsIsKind(k) { return typeof k === 'string' && TOOL_KINDS.indexOf(k) >= 0; }
// The privacy feature (and team capability) a tool needs: the explorer
// 'files', the terminals 'remote' (the web's gates).
function toolFeature(kind) { return kind === 'files' ? 'files' : 'remote'; }

var TOOLS_ICON = {
  terminal: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>',
  folder: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/></svg>',
  lock: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0110 0v4"/></svg>',
  unlock: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 019.9-1"/></svg>',
  blocked: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="12" cy="12" r="10"/><line x1="4.93" y1="4.93" x2="19.07" y2="19.07"/></svg>',
  monitor: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>',
};

// ── Data: GET /api/reach/overview?agents=1 ──
// The answer says whether this server lists the agents' tools: an older
// one ignores ?agents=1, sends no features, and its devices keep _tools false.
function toolsSupported(data) { return !!(data && typeof data === 'object' && data.features && data.features.agents === true); }
// Every device of groups: _tools on or off (after srvStamp).
function srvStampTools(groups, on) {
  on = !!on;
  var gs = Array.isArray(groups) ? groups : [];
  for (var i = 0; i < gs.length; i++) {
    var g = gs[i];
    if (!g || typeof g !== 'object' || !Array.isArray(g.devices)) continue;
    for (var j = 0; j < g.devices.length; j++) { var d = g.devices[j]; if (d && typeof d === 'object') d._tools = on; }
  }
  return groups;
}

// ── Predicates ──
// Reach: the ObliReach agent (sessions and chat go through it). devIsOffline
// (ui.go) is its negation and keeps that meaning.
function devReachOnline(d) { return !!(d && d.oblireach && d.oblireach.online); }
function devReachInstalled(d) { return !!(d && d.oblireach && d.oblireach.installed); }
// The OS family one report names: 'macos', 'windows', 'unix', or '' (it says
// nothing: empty, 'other', 'unknown', a word it does not know). 'darwin'
// holds "win": macOS is tested first. gopsutil's Windows version ("6.3.9600.
// 20563 Build 9600.20563") and the legacy agent's ("Windows Server 2008 R2
// Standard (Build 7601)") name Windows too.
function devOsWord(v) {
  var s = String(v === undefined || v === null ? '' : v).toLowerCase().trim();
  if (!s || s === 'other' || s === 'unknown') return '';
  if (/mac\s?os|darwin|os x\b|macintosh/.test(s)) return 'macos';
  if (/windows|^win(32|64|nt)?$|microsoft/.test(s)) return 'windows';
  if (/^\d+\.\d+\.\d{4,6}(\.\d+)?\s+build\s+\d/.test(s)) return 'windows';
  if (/linux|bsd|ubuntu|debian|cent\s?os|red\s?hat|rhel|fedora|suse|alpine|\barch\b|rocky|alma|amazon|gentoo|\bmint\b|raspbian|proxmox|unix/.test(s)) return 'unix';
  return '';
}
// The OS family of a device, from the Obliance agent's reports (its type
// first, then the OS name, its version): 'windows', 'macos', 'unix' (Linux,
// BSD), or 'unknown' — never guessed: an unknown OS gets no terminal
// (devShellKinds), its buttons say why. The same rule as the server
// (utils/deviceOs.ts) and the web (utils/remoteShell.ts): the consoles are
// opened by the Obliance agent, so only its reports count (never the
// ObliReach agent's own).
function devOsKind(d) {
  if (!d) return 'unknown';
  var srcs = [d.osType, d.osName, d.osVersion];
  for (var i = 0; i < srcs.length; i++) { var k = devOsWord(srcs[i]); if (k) return k; }
  return 'unknown';
}
// The Windows build of a device (9600: 2012 R2, 14393: 2016, 17763: 2019 /
// 10 1809…): the server's osBuild, else read in its version or name; null
// when not Windows or not reported.
function devWinBuild(d) {
  if (!d || devOsKind(d) !== 'windows') return null;
  var ok = function (n) { return Number.isInteger(n) && n >= 1000 && n <= 999999; };
  var n = (typeof d.osBuild === 'number' || typeof d.osBuild === 'string') && String(d.osBuild).trim() !== '' ? Number(String(d.osBuild).trim().split('.')[0]) : NaN;
  if (ok(n)) return n;
  var srcs = [d.osVersion, d.osName];
  for (var i = 0; i < srcs.length; i++) {
    var s = String(srcs[i] === undefined || srcs[i] === null ? '' : srcs[i]);
    var m = /\bbuild\s+(\d{4,6})\b/i.exec(s) || /^\s*(?:6|10)\.\d+\.(\d{4,6})\b/.exec(s);
    if (m && ok(Number(m[1]))) return Number(m[1]);
  }
  return null;
}
// CMD and PowerShell run in the Windows pseudo console (ConPTY): Windows 10
// 1809 / Windows Server 2019 (build 17763) or later. A build KNOWN to be
// older greys them out; an unknown build leaves them (the agent refuses
// clearly, the server answers 409 conpty_unsupported when it knows).
var CONPTY_MIN_BUILD = 17763;
function devConptyMissing(d) { var b = devWinBuild(d); return b !== null && b < CONPTY_MIN_BUILD; }
// The web's matrix (DeviceDetailPage): Windows CMD + PowerShell, macOS /
// Linux / BSD SSH; an unknown OS none (toolsShellUnknown says why).
function devShellKinds(d) {
  var k = devOsKind(d);
  return k === 'windows' ? ['cmd', 'powershell'] : (k === 'macos' || k === 'unix') ? ['ssh'] : [];
}
// The device lists the agent's tools but its OS is unknown: no terminal is
// offered, a greyed one says why.
function toolsShellUnknown(d) { return !!(d && d._tools && !devShellKinds(d).length); }
function devToolKinds(d) { return devShellKinds(d).concat(['files']); }
// The Obliance agent answers (the web's isAgentReachable): a warning or
// critical device is online with a metric alert; updating, maintenance,
// pending, suspended, offline are not reachable.
function devAgentReachable(d) { var s = d && d.status; return s === 'online' || s === 'warning' || s === 'critical'; }
// Reachable through the Obliance agent, as far as this server says (_tools).
function devAgentOnline(d) { return !!(d && d._tools) && devAgentReachable(d); }
function devLegacy(d) { return !!(d && d.agent && d.agent.flavor === 'legacy'); }
// A capability the server says this user lacks on the device (can.x false).
function devCan(d, cap) { return !(d && d.can && typeof d.can === 'object') || d.can[cap] !== false; }
function devPrivacy(d) { var p = d && d.privacy; return { enabled: !!(p && p.enabled), passwordSet: !!(p && p.passwordSet) }; }

// What a tool offers on a device: { show, enabled, lock: 'none' | 'locked'
// | 'unlocked', reason (text), reasonKey }. The web's gates, in its order.
function toolState(d, kind) {
  var st = { show: false, enabled: false, lock: 'none', reason: '', reasonKey: '' };
  if (devIsLocal(d)) return toolsIsKind(kind) ? cxnToolState(d, kind) : st; // (a local connection: localconn_tools.go)
  if (!d || !d._tools || !toolsIsKind(kind)) return st;
  if (kind !== 'files' && devShellKinds(d).indexOf(kind) < 0) return st;
  st.show = true;
  var no = function (key) { st.reasonKey = key; st.reason = T(key); return st; };
  if (!devAgentReachable(d)) return no('tools.reason.agentOffline');
  if (devLegacy(d)) return no('tools.reason.legacy');
  if ((kind === 'cmd' || kind === 'powershell') && devConptyMissing(d)) return no('tools.reason.conpty');
  var feature = toolFeature(kind);
  if (!devCan(d, feature)) return no('tools.reason.notPermitted');
  // The explorer is live only (no queue): the agent's channel must be up.
  if (kind === 'files' && d.agent && d.agent.live === false) return no('tools.reason.noLiveChannel');
  var pv = devPrivacy(d);
  if (pv.enabled && !pv.passwordSet) return no('tools.reason.privacyBlocked');
  st.enabled = true;
  if (pv.enabled) {
    var until = privacyUnlockedUntil(d, feature);
    if (until) {
      st.lock = 'unlocked'; st.reasonKey = 'tools.reason.unlocked';
      st.reason = T('tools.reason.unlocked', { minutes: Math.max(1, Math.ceil((until - Date.now()) / 60000)) });
    } else {
      st.lock = 'locked'; st.reasonKey = 'tools.reason.privacyLocked'; st.reason = T('tools.reason.privacyLocked');
    }
  }
  return st;
}
function devToolsUsable(d) {
  var ks = devToolKinds(d);
  for (var i = 0; i < ks.length; i++) if (toolState(d, ks[i]).enabled) return true;
  return false;
}
// Not offline: ObliReach online, or the Obliance agent reachable (the row is
// greyed, and listed by the "Offline" filter, only when neither answers). An
// older server: ObliReach alone, as before.
function devUsable(d) {
  // A local connection: usable unless its host was found unreachable (ui.go localDevProbe).
  if (typeof devIsLocal === 'function' && devIsLocal(d)) return d._reach !== false;
  return devReachOnline(d) || devAgentOnline(d);
}
// Why nothing opens on a device (its first blocking reason, or offline).
function toolsBlockedText(d) {
  if (devAgentOnline(d)) {
    var ks = devToolKinds(d);
    for (var i = 0; i < ks.length; i++) { var st = toolState(d, ks[i]); if (st.show && !st.enabled && st.reason) return st.reason; }
  }
  return T('tools.launchOffline', { device: reachDevName(d) });
}

// ── Row parts (Devices list, "+" list, Dashboard) ──
function devDotClass(d) {
  if (typeof devIsLocal === 'function' && devIsLocal(d)) return d._reach === true ? 'online' : d._reach === false ? 'offline' : 'warn';
  if (devReachOnline(d)) return 'online';
  if (devAgentOnline(d)) return 'agent';
  return devReachInstalled(d) ? 'warn' : 'offline';
}
// The Status column: { cls, key }.
function devStateChip(d) {
  // A local connection: its host and port answering or not (ui.go localDevProbe).
  if (typeof devIsLocal === 'function' && devIsLocal(d)) {
    return d._reach === true ? { cls: 'on', key: 'devices.local.reachable' } : d._reach === false ? { cls: 'off', key: 'devices.local.unreachable' } : { cls: 'off', key: 'devices.local.checking' };
  }
  if (devReachOnline(d)) return { cls: 'on', key: 'devices.state.online' };
  if (devAgentOnline(d)) {
    if (devLegacy(d)) return { cls: 'warn', key: 'tools.state.legacy' };
    return { cls: 'agent', key: devReachInstalled(d) ? 'tools.state.reachOffline' : 'tools.state.agentOnly' };
  }
  if (devReachInstalled(d) || (d && d._tools)) return { cls: 'off', key: 'devices.state.offline' };
  return { cls: 'off', key: 'devices.state.notInstalled' };
}
function devStateChipHtml(d) { var c = devStateChip(d); return '<span class="tag-chip ' + c.cls + '">' + TH(c.key) + '</span>'; }
// After the hostname: privacy mode (orange lock: a code unlocks it; muted:
// blocked, no code set).
function devPrivacyIcon(d) {
  if (!d || !d._tools) return '';
  var pv = devPrivacy(d);
  if (!pv.enabled) return '';
  return pv.passwordSet
    ? '<span class="dev-priv locked" title="' + TH('tools.privacy.locked') + '">' + TOOLS_ICON.lock + '</span>'
    : '<span class="dev-priv blocked" title="' + TH('tools.privacy.blocked') + '">' + TOOLS_ICON.blocked + '</span>';
}
function toolLabelKey(kind) { return kind === 'cmd' ? 'tools.cmd' : kind === 'powershell' ? 'tools.psShort' : kind === 'ssh' ? 'tools.ssh' : kind === 'rdp' ? 'tools.rdp' : 'tools.files'; }
function toolNameKey(kind) { return kind === 'powershell' ? 'tools.powershell' : toolLabelKey(kind); }
function toolTitleKey(kind) { return kind === 'cmd' ? 'tools.openCmd' : kind === 'powershell' ? 'tools.openPowershell' : kind === 'ssh' ? 'tools.openSsh' : kind === 'rdp' ? 'tools.openRdp' : 'tools.openFiles'; }
// A tool button's tooltip: what it opens (and its privacy state), or why not.
function toolTitle(kind, st) {
  if (!st.enabled) return st.reason;
  var t = T(toolTitleKey(kind));
  return st.lock !== 'none' ? t + ' — ' + st.reason : t;
}
function toolBadge(st) {
  if (st.lock === 'locked') return '<span class="tool-lock">' + TOOLS_ICON.lock + '</span>';
  if (st.lock === 'unlocked') return '<span class="tool-lock open">' + TOOLS_ICON.unlock + '</span>';
  return '';
}
// The row buttons of the actions 'tools' (CMD + PS, or SSH) and 'files' (a
// folder): shown when the server lists the tools, disabled with the reason.
function toolsRowButtons(d, act) {
  var kinds = act === 'files' ? ['files'] : act === 'tools' ? devShellKinds(d) : [];
  var out = '';
  // An unknown OS: a greyed terminal that says why (never a guessed one).
  if (act === 'tools' && toolsShellUnknown(d)) {
    return '<button class="tool-btn" title="' + i18nEsc(T('tools.reason.osUnknown')) + '" data-act="shell" disabled>' + TOOLS_ICON.terminal + '</button>';
  }
  for (var i = 0; i < kinds.length; i++) {
    var k = kinds[i], st = toolState(d, k);
    if (!st.show) continue;
    out += '<button class="' + (k === 'files' ? 'icon-btn tool-ico' : 'tool-btn') + '" title="' + i18nEsc(toolTitle(k, st)) + '" data-act="' + k + '"' +
      (st.enabled ? '' : ' disabled') + '>' + (k === 'files' ? TOOLS_ICON.folder : TH(toolLabelKey(k))) + toolBadge(st) + '</button>';
  }
  return out;
}

// ── Launch (detached mode: a click on a device) ──
// Reach online (or installed and nothing else usable): its session, as
// before (reachLaunchDevice: its stale-list reload and offline notice). Else
// the agent's tools: SSH, or the CMD / PowerShell picker. Nothing usable: its
// server read again once, then why.
async function devLaunch(dev) {
  if (!dev) return 'none';
  if (!dev._tools || devReachOnline(dev) || (devReachInstalled(dev) && !devToolsUsable(dev))) return reachLaunchDevice(dev);
  if (devToolsUsable(dev)) return toolsDefaultAction(dev, 'auto');
  var id = String(dev.id), pid = devPid(dev);
  try { if (typeof loadOverview === 'function') await loadOverview(pid); } catch (e) {}
  var fresh = reachFindDevice(id, pid) || dev;
  if (devReachOnline(fresh)) return reachLaunchDevice(fresh);
  if (devToolsUsable(fresh)) return toolsDefaultAction(fresh, 'auto');
  reachNotify(toolsBlockedText(fresh), 'warn');
  return 'offline';
}
// "+" list of a session window: the same rule, here ('here'): Reach through
// reachWinAddDevice, the tools in this window. act: a row button. A Hyper-V
// VM listed under its host (vms_page.go dpVmRows): its console here.
function devPickerLaunch(dev, act) {
  var note = { notify: function (text) { if (typeof devPickerNote === 'function') devPickerNote(text); } };
  if (dev && typeof devIsVm === 'function' && devIsVm(dev)) return devPickerTool(function () { return dpVmLaunch(dev, note); });
  if (toolsIsKind(act)) return devPickerTool(function () { return toolOpen(dev, act, 'here', note); });
  // A local connection: its terminal, or its RDP tab, here (localconn_tools.go).
  if (typeof devIsLocal === 'function' && devIsLocal(dev)) {
    var lk = cxnIsSsh(dev) ? 'ssh' : 'rdp';
    return devPickerTool(function () { return toolOpen(dev, lk, 'here', note); });
  }
  if (!dev || !dev._tools || devReachOnline(dev) || (devReachInstalled(dev) && !devToolsUsable(dev))) return reachWinAddDevice(dev);
  if (devToolsUsable(dev)) return devPickerTool(function () { return toolsDefaultAction(dev, 'here', note); });
  note.notify(toolsBlockedText(dev));
  return Promise.resolve('offline');
}
async function devPickerTool(run) {
  if (typeof devPickerNote === 'function') devPickerNote('');
  var r = await run();
  if (['blocked', 'cancelled', 'busy', 'none', 'error'].indexOf(r) < 0 && typeof devPickerClose === 'function') devPickerClose();
  return r;
}
function toolsSay(opts, text) {
  if (opts && typeof opts.notify === 'function') opts.notify(text);
  else reachNotify(text, 'warn');
}
// The default tool of a device: SSH on Linux / macOS, the CMD / PowerShell
// picker on Windows; the explorer when only it is allowed.
async function toolsDefaultAction(dev, where, opts) {
  var shells = devShellKinds(dev).filter(function (k) { return toolState(dev, k).enabled; });
  if (!shells.length) {
    if (toolState(dev, 'files').enabled) return toolOpen(dev, 'files', where, opts);
    toolsSay(opts, toolsBlockedText(dev));
    return 'blocked';
  }
  if (devOsKind(dev) !== 'windows') return toolOpen(dev, shells[0], where, opts);
  var k = await toolsPickShell(dev);
  if (!k) return 'cancelled';
  return toolOpen(dev, k, where, opts);
}

// ── CMD / PowerShell picker (Windows) ──
// Built like the Windows session picker (reachPickSession, same classes),
// and shares its slot (reachPickerDone): a new picker cancels the one on
// screen. Resolves to 'cmd', 'powershell' or null (cancelled). The shell
// picked last is focused first (per computer).
var TOOLS_SHELL_LAST = 'oblireach_shell_last';
function toolsShellLast() { try { var v = localStorage.getItem(TOOLS_SHELL_LAST); return (v === 'cmd' || v === 'powershell') ? v : ''; } catch (e) { return ''; } }
function toolsSetShellLast(k) { try { localStorage.setItem(TOOLS_SHELL_LAST, k); } catch (e) {} }
function toolsPickShell(dev) {
  if (reachPickerDone) reachPickerDone(null);
  var kinds = ['cmd', 'powershell'];
  return new Promise(function (resolve) {
    var ov = document.createElement('div');
    ov.className = 'modal-overlay visible reach-sess-picker tools-picker'; ov.id = 'tsp-overlay';
    var box = document.createElement('div'); box.className = 'modal-box';
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T('tools.pick.title'));
    var head = document.createElement('div'); head.className = 'modal-head';
    var ico = document.createElement('div'); ico.className = 'icon'; ico.innerHTML = TOOLS_ICON.terminal.replace(/width="13" height="13"/, 'width="18" height="18"');
    var h = document.createElement('h3'); h.textContent = T('tools.pick.title');
    var x = document.createElement('button'); x.className = 'close'; x.id = 'tsp-close'; x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    head.appendChild(ico); head.appendChild(h); head.appendChild(x);
    var desc = document.createElement('p'); desc.className = 'desc'; desc.id = 'tsp-desc';
    desc.textContent = T(devReachOnline(dev) ? 'tools.pick.desc' : 'tools.pick.descNoReach', { device: reachDevName(dev) });
    var list = document.createElement('div'); list.className = 'rsp-list';
    var items = [], enabled = [];
    var finish = function (v) {
      if (reachPickerDone !== finish) return;
      reachPickerDone = null;
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    kinds.forEach(function (k) {
      var st = toolState(dev, k);
      var b = document.createElement('button');
      b.className = 'rsp-item' + (st.enabled ? '' : ' off');
      b.id = 'tsp-' + k;
      b.dataset.kind = k;
      var ic = document.createElement('span'); ic.className = 'tsp-ico'; ic.innerHTML = TOOLS_ICON.terminal;
      var tx = document.createElement('span'); tx.className = 'rsp-text';
      var u = document.createElement('span'); u.className = 'rsp-user'; u.innerHTML = TH(toolNameKey(k)) + toolBadge(st);
      var m = document.createElement('span'); m.className = 'rsp-meta';
      var meta = T(k === 'cmd' ? 'tools.pick.cmdMeta' : 'tools.pick.psMeta');
      m.textContent = st.enabled ? (st.lock !== 'none' ? meta + ' · ' + st.reason : meta) : st.reason;
      tx.appendChild(u); tx.appendChild(m);
      b.appendChild(ic); b.appendChild(tx);
      b.title = T(toolTitleKey(k));
      if (!st.enabled) { b.disabled = true; b.setAttribute('aria-disabled', 'true'); }
      else enabled.push(b);
      b.addEventListener('click', function () { if (!b.disabled) finish(k); });
      items.push(b);
      list.appendChild(b);
    });
    var acts = document.createElement('div'); acts.className = 'modal-actions';
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'tsp-cancel'; cancel.textContent = T('common.cancel');
    acts.appendChild(cancel);
    box.appendChild(head); box.appendChild(desc); box.appendChild(list); box.appendChild(acts);
    ov.appendChild(box);
    x.addEventListener('click', function () { finish(null); });
    cancel.addEventListener('click', function () { finish(null); });
    ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) finish(null); }); // the backdrop
    ov.addEventListener('keydown', function (e) {
      if (!e) return;
      var stop = function () { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); };
      if (e.key === 'Escape') { stop(); finish(null); return; }
      var i = enabled.indexOf(document.activeElement);
      if (e.key === 'Enter' && i >= 0) { stop(); finish(enabled[i].dataset.kind); return; }
      if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && enabled.length) {
        var n = e.key === 'ArrowDown' ? (i + 1) % enabled.length : (i <= 0 ? enabled.length - 1 : i - 1);
        stop();
        try { enabled[n].focus(); } catch (x2) {}
      }
    });
    reachPickerDone = finish;
    document.body.appendChild(ov);
    var last = toolsShellLast(), first = null;
    for (var i = 0; i < enabled.length; i++) if (enabled[i].dataset.kind === last) first = enabled[i];
    first = first || enabled[0] || cancel;
    try { first.focus(); } catch (e) {}
  });
}

// ── Open a tool ──
// The gates this device shows (toolState), then privacy (its code asked when
// it is locked), then the tab (toolTabOpen: the tool tabs, where 'auto':
// this window or a session window per "Detach by default", the explorer of
// the main window in its Files page (files_page.go); 'here': this window).
// Returns 'blocked', 'cancelled', 'busy' (being opened: a double
// click), 'none' (no tool tabs in this build), or what toolTabOpen says.
// opts.notify(text): where a refusal is said (default: a toast).
var toolOpening = {};
async function toolOpen(dev, kind, where, opts) {
  if (!dev || !toolsIsKind(kind)) return 'none';
  var st = toolState(dev, kind);
  if (!st.show || !st.enabled) { toolsSay(opts, st.reason || T('tools.reason.agentOffline')); return 'blocked'; }
  var key = devKey(dev) + '~' + kind;
  if (toolOpening[key]) return 'busy';
  toolOpening[key] = true;
  try {
    if (!(await privacyEnsure(dev, toolFeature(kind), opts))) return 'cancelled';
    if (kind === 'cmd' || kind === 'powershell') toolsSetShellLast(kind);
    return (typeof toolTabOpen === 'function') ? await toolTabOpen(dev, kind, where || 'auto') : 'none';
  } finally {
    delete toolOpening[key];
  }
}

// ── Device page: the tools group of the header, the agent pane ──
// Tools shown on the header line (after the side tabs): the same states as
// the rows. 'Reach' (ObliReach devices, once the tool tabs exist:
// toolShowReach) goes back to the remote screen.
// toolsShown: what the device page on screen draws from its tool states, to
// draw again once its unlocks are read (toolsRefreshShown): the header group,
// the agent pane, the session toolbar's Files button (files_page.go).
var toolsShown = { dev: null, header: null, pane: null, session: null };
function toolsHeaderGroup(dev) {
  if (!dev || !dev._tools) return null;
  var g = document.createElement('div'); g.className = 'dev-tools'; g.id = 'dev-tools';
  var fill = function () {
    g.innerHTML = '';
    if (devReachInstalled(dev) && typeof toolShowReach === 'function') {
      var rb = document.createElement('button'); rb.className = 'tool-hbtn'; rb.id = 'hdr-tool-reach';
      rb.innerHTML = TOOLS_ICON.monitor + '<span>' + TH('tools.reach') + '</span>';
      rb.title = T('tools.openReach');
      rb.addEventListener('click', function () { toolShowReach(dev); });
      g.appendChild(rb);
    }
    if (toolsShellUnknown(dev)) { // (an unknown OS: a greyed terminal, why in its tooltip)
      var ub = document.createElement('button'); ub.className = 'tool-hbtn'; ub.id = 'hdr-tool-shell';
      ub.innerHTML = TOOLS_ICON.terminal + '<span>' + TH('tools.console') + '</span>';
      ub.title = T('tools.reason.osUnknown');
      ub.disabled = true;
      g.appendChild(ub);
    }
    devToolKinds(dev).forEach(function (k) {
      var st = toolState(dev, k);
      if (!st.show) return;
      var b = document.createElement('button'); b.className = 'tool-hbtn'; b.id = 'hdr-tool-' + k;
      b.dataset.kind = k;
      b.innerHTML = (k === 'files' ? TOOLS_ICON.folder : TOOLS_ICON.terminal) + '<span>' + TH(toolLabelKey(k)) + '</span>' + toolBadge(st);
      b.title = toolTitle(k, st);
      b.disabled = !st.enabled;
      b.addEventListener('click', function () { if (!b.disabled) toolOpen(dev, k, reachDetachedMode ? 'here' : 'auto'); });
      g.appendChild(b);
    });
  };
  fill();
  // (refreshed later only while it is on screen: a page drawn again has its own)
  toolsShown = { dev: dev, header: function () { if (g.isConnected !== false) fill(); }, pane: null, session: null }; // (the page's pane / toolbar come after it)
  return g;
}
// The Remote pane of a device whose ObliReach is not online (not installed,
// or offline): its terminals and explorer, through the Obliance agent.
function toolsRenderAgentPane(tc, dev) {
  var cur = null;
  var fill = function () {
    tc.innerHTML = '';
    var box = document.createElement('div'); box.className = 'empty tools-pane'; box.id = 'tools-pane';
    cur = box;
    var ico = document.createElement('div'); ico.className = 'empty-icon';
    ico.innerHTML = TOOLS_ICON.terminal.replace(/width="13" height="13"/, 'width="48" height="48" style="opacity:.3"');
    var l1 = document.createElement('span'); l1.className = 'tp-line'; l1.id = 'tp-line';
    l1.textContent = T(devReachInstalled(dev) ? 'tools.pane.reachOffline' : 'tools.pane.noReach');
    var l2 = document.createElement('span'); l2.className = 'tp-sub'; l2.textContent = T('tools.pane.viaAgent');
    var acts = document.createElement('div'); acts.className = 'tp-acts';
    var firstReason = '', any = false;
    if (toolsShellUnknown(dev)) { // (an unknown OS: a greyed terminal and why)
      var ua = document.createElement('div'); ua.className = 'tp-act';
      var ub = document.createElement('button'); ub.className = 'tp-btn'; ub.id = 'tp-shell';
      ub.innerHTML = TOOLS_ICON.terminal.replace(/width="13" height="13"/, 'width="22" height="22"') + '<span class="tp-name">' + TH('tools.console') + '</span>';
      ub.title = T('tools.reason.osUnknown');
      ub.disabled = true;
      var uw = document.createElement('span'); uw.className = 'tp-why'; uw.textContent = T('tools.reason.osUnknown');
      ua.appendChild(ub); ua.appendChild(uw); acts.appendChild(ua);
    }
    devToolKinds(dev).forEach(function (k) {
      var st = toolState(dev, k);
      if (!st.show) return;
      if (st.enabled) any = true; else if (!firstReason) firstReason = st.reason;
      var a = document.createElement('div'); a.className = 'tp-act';
      var b = document.createElement('button'); b.className = 'tp-btn'; b.id = 'tp-' + k;
      b.innerHTML = (k === 'files' ? TOOLS_ICON.folder : TOOLS_ICON.terminal).replace(/width="13" height="13"/, 'width="22" height="22"') +
        '<span class="tp-name">' + TH(toolNameKey(k)) + toolBadge(st) + '</span>';
      b.title = toolTitle(k, st);
      b.disabled = !st.enabled;
      b.addEventListener('click', function () { if (!b.disabled) toolOpen(dev, k, reachDetachedMode ? 'here' : 'auto'); });
      a.appendChild(b);
      if (st.reason) { var w = document.createElement('span'); w.className = 'tp-why'; w.textContent = st.reason; a.appendChild(w); }
      acts.appendChild(a);
    });
    box.appendChild(ico); box.appendChild(l1); box.appendChild(l2); box.appendChild(acts);
    if (!firstReason && toolsShellUnknown(dev)) firstReason = T('tools.reason.osUnknown');
    if (!any && firstReason) { var why = document.createElement('span'); why.className = 'tp-why block'; why.id = 'tp-why'; why.textContent = firstReason; box.appendChild(why); }
    tc.appendChild(box);
  };
  fill();
  if (toolsShown.dev !== dev) { toolsShown.header = null; toolsShown.session = null; }
  toolsShown.dev = dev;
  toolsShown.pane = function () { if (cur && cur.parentElement === tc && cur.isConnected !== false) fill(); };
}
// The device page opened: its unlocks read (the lock badges and the
// countdown follow), when privacy applies.
function toolsPageOpened(dev) {
  if (!dev || !dev._tools || !devPrivacy(dev).enabled) return;
  privacyUnlocksRefresh(dev).then(function () { toolsRefreshShown(dev); }).catch(function () {});
}
function toolsRefreshShown(dev) {
  if (!dev || toolsShown.dev !== dev) return;
  if (typeof selectedDevice !== 'undefined' && selectedDevice !== dev) return;
  try { if (toolsShown.header) toolsShown.header(); } catch (e) {}
  try { if (toolsShown.pane && !devReachOnline(dev)) toolsShown.pane(); } catch (e) {}
  try { if (toolsShown.session) toolsShown.session(); } catch (e) {}
}
// Info panel rows: the Obliance agent and privacy mode.
function toolsInfoRows(d, row) {
  if (!d || !d._tools) return '';
  var a = (d.agent && typeof d.agent === 'object') ? d.agent : {};
  var agentVal = (devAgentReachable(d) ? '<span style="color:var(--success)">' + TH('info.online') + '</span>' : TH('info.offline')) +
    (a.version ? ' <span style="font-size:11px;color:var(--muted)">v' + i18nEsc(String(a.version)) + '</span>' : '') +
    (devLegacy(d) ? ' <span class="tag-chip warn">' + TH('tools.state.legacy') + '</span>' : '');
  var pv = devPrivacy(d);
  var privVal = !pv.enabled ? TH('tools.info.privacyOff') : pv.passwordSet ? TH('tools.info.privacyCode') : TH('tools.info.privacyBlocked');
  return row('tools.info.agent', agentVal) + row('tools.info.privacy', privVal);
}
// Info panel rows: the OS (its name, version and Windows build, as the
// Obliance agent reports them; the type alone with an older server).
// Agent-reported text: escaped.
function toolsOsRows(d, row) {
  var txt = function (v) { return (typeof v === 'string' && v.trim()) ? v.trim().slice(0, 200) : ''; };
  var type = txt(d && d.osType), name = txt(d && d.osName), ver = txt(d && d.osVersion);
  // (the legacy agent names its OS "windows", its type says as much)
  var label = (name && name.toLowerCase() !== type.toLowerCase()) ? name : (type || name || '?');
  var out = row('info.os', i18nEsc(label));
  if (ver && ver !== label) out += row('info.osVersion', i18nEsc(ver));
  var b = devWinBuild(d);
  if (b !== null) {
    out += row('info.osBuild', i18nEsc(String(b)) + (devConptyMissing(d)
      ? ' <span class="tag-chip warn" title="' + i18nEsc(T('tools.reason.conpty')) + '">' + TH('info.noConsole') + '</span>' : ''));
  }
  return out;
}

// ── Info panel: IP addresses ──
// LAN: the main address the Obliance agent reports (devices.ip_local), then
// the other IPv4 addresses of its network cards (hardware inventory), loopback
// and link-local left out. WAN: the address Obliance sees the agent come from
// through its trusted proxies (devices.ip_public: a relay address is never
// stored). Stored values, so shown for an offline device too. Read on the
// device's server and tenant (devApi, pinned), kept a minute per device.
var INFO_NET_TTL_MS = 60000;
var infoNetCache = new Map(); // devKey -> { at, lan, others: [{ ip, iface }], wan, rec }
var infoNetPending = new Map(); // devKey -> { lan, wan }: read by the live section first
function toolsNetRowsHost(d) { return (d && d._tools) ? '<div id="info-net" style="display:contents"></div>' : ''; }
function infoNetIp(v) { return (typeof v === 'string' && /^[0-9a-fA-F:.]{2,45}$/.test(v.trim())) ? v.trim() : ''; }
function infoNetSkip(ip) { return !ip || /^127\./.test(ip) || /^169\.254\./.test(ip) || ip.indexOf(':') >= 0 || ip === '0.0.0.0'; }
var infoNetRow = null; // (the panel's row renderer, for a later refill)
async function infoNetLoad(dev, row) {
  if (!dev || !dev._tools) return;
  infoNetRow = row;
  var key = devKey(dev), c = infoNetCache.get(key);
  if (!c || Date.now() - c.at > INFO_NET_TTL_MS) {
    c = { at: Date.now(), lan: '', others: [], wan: '', rec: false };
    // (the device record: read here only when the live section does not read
    // it anyway — infoLiveFetch hands its answer over, infoNetTake)
    if (!infoLiveWanted(dev)) {
      try {
        var r = await devApi(dev, 'GET', '/api/devices/' + encodeURIComponent(String(dev.id)), undefined, true);
        if (r && r.ok) { var j = await r.json(); var x = (j && j.data) || {}; c.lan = infoNetIp(x.ipLocal); c.wan = infoNetIp(x.ipPublic); c.rec = true; infoRecSet(dev, x); }
      } catch (e) {}
    }
    // (the hardware inventory: one read shared with "More details", infoHwGet)
    try {
      var he = await infoHwGet(dev);
      if (he && he.status === 'ok' && he.data) {
        var nics = Array.isArray(he.data.networkInterfaces) ? he.data.networkInterfaces : [];
        var seen = new Set(c.lan ? [c.lan] : []);
        nics.forEach(function (n) {
          (Array.isArray(n && n.addresses) ? n.addresses : []).forEach(function (a) {
            var ip = infoNetIp(String(a || '').split('/')[0]);
            if (infoNetSkip(ip) || seen.has(ip)) return;
            seen.add(ip);
            c.others.push({ ip: ip, iface: String((n && n.name) || '').slice(0, 60) });
          });
        });
      }
    } catch (e) {}
    var pend = infoNetPending.get(key);
    if (pend) { infoNetPending.delete(key); c.lan = pend.lan; c.wan = pend.wan; c.rec = true; c.others = c.others.filter(function (o) { return o.ip !== pend.lan; }); }
    infoNetCache.set(key, c);
  }
  var host = document.getElementById('info-net');
  if (!host || typeof selectedDevice === 'undefined' || selectedDevice !== dev) return;
  var h = '';
  if (c.lan || c.others.length) {
    var lan = c.lan ? '<span style="font-family:monospace">' + i18nEsc(c.lan) + '</span>' : '';
    var more = c.others.slice(0, 8).map(function (o) {
      return '<span style="font-family:monospace">' + i18nEsc(o.ip) + '</span>' + (o.iface ? ' <span style="font-size:11px;color:var(--muted)">' + i18nEsc(o.iface) + '</span>' : '');
    });
    h += row('info.ipLan', [lan].concat(more).filter(Boolean).join(', '));
  }
  if (c.wan) h += row('info.ipWan', '<span style="font-family:monospace">' + i18nEsc(c.wan) + '</span>');
  host.innerHTML = h;
}

// The device record the live section read: its addresses, into the block.
function infoNetTake(dev, data) {
  if (!dev || !data) return;
  infoRecSet(dev, data); // (and "More details": its record fields)
  var c = infoNetCache.get(devKey(dev));
  var lan = infoNetIp(data.ipLocal), wan = infoNetIp(data.ipPublic);
  if (!c) { infoNetPending.set(devKey(dev), { lan: lan, wan: wan }); return; } // (the block not read yet: it takes them)
  if (c.rec && c.lan === lan && c.wan === wan) return;
  c.lan = lan; c.wan = wan; c.rec = true;
  c.others = c.others.filter(function (o) { return o.ip !== lan; });
  if (typeof infoNetRow === 'function' && selectedDevice === dev) infoNetLoad(dev, infoNetRow);
}

// ── Info panel: live CPU / RAM / disks (the Obliance agent) ──
// While the panel shows a device whose Obliance agent answers: POST
// /api/devices/:id/live-metrics {mode:'live', windowSec:60} (the agent then
// streams a sample every 3 s over its channel; the web's device page does the
// same), sent again every INFO_LIVE_REARM_MS, on the device's server and
// tenant (devApi, pinned: like the Files page). The samples come as
// DEVICE_METRICS_PUSHED on that server's socket (chatSock: one per server):
// the socket asks DEVICE_METRICS_WATCH {deviceId} for them too, when it is
// not in the device's tenant room (god view, multi-server mode); an older
// server ignores it. Meanwhile, and when no sample comes, GET
// /api/devices/:id (its latestMetrics: the last push) every INFO_LIVE_POLL_MS.
// Everything stops when the panel closes or shows another device.
var INFO_LIVE_REARM_MS = 50000, INFO_LIVE_POLL_MS = 15000, INFO_LIVE_FRESH_MS = 12000, INFO_LIVE_HIST = 24, INFO_LIVE_DISKS = 6;
var infoLive = null; // { key, dev, pid, id, el, m, sampleAt, pushAt, cpu: [], ram: [], cores, coresAt, rearm, poll }
var infoLiveHooked = false;
function infoLiveWanted(d) { return !!(d && d._tools && devAgentReachable(d)); }
// Its element (in the panel, after the rows) and its timers: a device that
// is already shown keeps them (the panel drawn again), another one starts over.
function infoLiveOpen(dev, host) {
  // ("More details" below it, the live section or not: infoMoreOpen)
  if (!infoLiveWanted(dev) || !host) { infoLiveClose(); infoMoreOpen(dev, host); return; }
  var el = document.createElement('div'); el.className = 'info-live'; el.id = 'info-live';
  // (the threads' fold, kept on this computer: one listener on the block)
  el.addEventListener('click', function (e) {
    var b = e.target && e.target.closest ? e.target.closest('[data-il-threads]') : null;
    if (!b) return;
    infoThreadsOpen = !infoThreadsOpen;
    try { localStorage.setItem(INFO_THREADS_PREF, infoThreadsOpen ? '1' : '0'); } catch (x) {}
    infoLiveRender();
  });
  host.appendChild(el);
  infoMoreOpen(dev, host);
  var key = devKey(dev);
  if (infoLive && infoLive.key === key) { infoLive.dev = dev; infoLive.el = el; infoLiveRender(); return; }
  infoLiveClose();
  infoLiveHook();
  var st = infoLive = { key: key, dev: dev, pid: devPid(dev), id: String(dev.id), el: el, m: null, sampleAt: 0, pushAt: null, cpu: [], ram: [], cores: null, coresAt: 0, rearm: null, poll: null };
  infoLiveRender();
  infoLiveArm(st);
  infoLiveFetch(st);
  st.rearm = setInterval(function () { infoLiveArm(st); }, INFO_LIVE_REARM_MS);
  // (no sample lately: the pulse stops, the last push is read)
  st.poll = setInterval(function () { if (Date.now() - st.sampleAt > INFO_LIVE_FRESH_MS) { infoLiveRender(); infoLiveFetch(st); } }, INFO_LIVE_POLL_MS);
}
function infoLiveClose() {
  var st = infoLive;
  if (!st) return;
  infoLive = null;
  clearInterval(st.rearm); clearInterval(st.poll);
  var s = (typeof chatSock === 'function') ? chatSock(st.pid) : null;
  if (s && s.connected) { try { s.emit('DEVICE_METRICS_WATCH', { deviceId: Number(st.id), on: false }); } catch (e) {} }
}
// Every server socket hears its devices' samples (terminal_viewer.go's
// socket hooks: the sockets opened later), the ones open now wired at once.
function infoLiveHook() {
  if (infoLiveHooked) return;
  infoLiveHooked = true;
  if (typeof toolSocketHooks !== 'undefined' && Array.isArray(toolSocketHooks)) toolSocketHooks.push(infoLiveWire);
  if (typeof chatSockets === 'object' && chatSockets) for (var k in chatSockets) infoLiveWire(chatSockets[k], k);
}
function infoLiveWire(sock, pid) {
  if (!sock || typeof sock.on !== 'function' || sock._infoLiveWired) return;
  sock._infoLiveWired = true;
  pid = pid || '';
  sock.on('DEVICE_METRICS_PUSHED', function (msg) { var st = infoLive; if (st && st.pid === pid) infoLiveOnMetrics(st, msg); });
  sock.on('connect', function () { var st = infoLive; if (st && st.pid === pid) infoLiveWatch(st); });
}
// Live mode asked again, and the watch renewed (the server drops it after 2 min).
function infoLiveArm(st) {
  if (infoLive !== st) return;
  try {
    var p = devApi(st.dev, 'POST', '/api/devices/' + encodeURIComponent(st.id) + '/live-metrics', { mode: 'live', windowSec: 60 }, true);
    if (p && typeof p.catch === 'function') p.catch(function () {});
  } catch (e) {}
  infoLiveWatch(st);
}
function infoLiveWatch(st) {
  if (infoLive !== st) return;
  var s = (typeof chatSock === 'function') ? chatSock(st.pid) : null;
  if (!s) return;
  infoLiveWire(s, st.pid);
  if (s.connected) { try { s.emit('DEVICE_METRICS_WATCH', { deviceId: Number(st.id), on: true }); } catch (e) {} }
}
// The last push (GET /api/devices/:id: its latestMetrics).
async function infoLiveFetch(st) {
  if (infoLive !== st) return;
  var d = null;
  try {
    var r = await devApi(st.dev, 'GET', '/api/devices/' + encodeURIComponent(st.id), undefined, true);
    if (r && r.ok) d = await r.json();
  } catch (e) { d = null; }
  if (infoLive !== st || !d || !d.data || typeof d.data !== 'object') return;
  infoNetTake(st.dev, d.data);
  var m = d.data.latestMetrics;
  if (typeof m === 'string') { try { m = JSON.parse(m); } catch (e) { m = null; } }
  if (!m || typeof m !== 'object') return;
  var fresh = d.data.lastPushAt !== st.pushAt;
  st.pushAt = d.data.lastPushAt;
  if (fresh || !st.m) infoLiveTake(st, m, false);
}
function infoLiveOnMetrics(st, msg) {
  if (!msg || typeof msg !== 'object' || String(msg.deviceId) !== st.id) return;
  var m = msg.metrics;
  if (typeof m === 'string') { try { m = JSON.parse(m); } catch (e) { return; } }
  if (!m || typeof m !== 'object') return;
  infoLiveTake(st, m, true);
}
function infoLivePct(v) { var n = Number(v); return (v === null || v === undefined || v === '' || !isFinite(n)) ? null : Math.max(0, Math.min(100, n)); }
// A sample (its missing parts kept from the previous one), into the curves.
function infoLiveTake(st, m, live) {
  var prev = st.m || {};
  st.m = {
    cpu: (m.cpu && typeof m.cpu === 'object') ? m.cpu : prev.cpu,
    memory: (m.memory && typeof m.memory === 'object') ? m.memory : prev.memory,
    disks: Array.isArray(m.disks) ? m.disks : prev.disks,
  };
  if (live) st.sampleAt = Date.now();
  // Per thread (logical processor): the Obliance agent's live samples carry
  // cpu.cores from 4.5.85, its regular push for longer (latestMetrics). A
  // sample without them keeps the last ones (shown dimmed once old).
  var cores = infoCoresOf(m.cpu);
  if (cores) { st.cores = cores; st.coresAt = Date.now(); }
  var c = infoLivePct(st.m.cpu && st.m.cpu.percent), r = infoLivePct(st.m.memory && st.m.memory.percent);
  if (c !== null) { st.cpu.push(c); if (st.cpu.length > INFO_LIVE_HIST) st.cpu.shift(); }
  if (r !== null) { st.ram.push(r); if (st.ram.length > INFO_LIVE_HIST) st.ram.shift(); }
  infoLiveRender();
}
function infoLiveLevel(p) { return p >= 90 ? ' crit' : p >= 75 ? ' warn' : ''; }
function infoLiveSpark(hist) {
  if (!hist || hist.length < 2) return '';
  var n = hist.length, pts = [];
  for (var i = 0; i < n; i++) pts.push((i * 100 / (n - 1)).toFixed(1) + ',' + (21 - hist[i] * 20 / 100).toFixed(1));
  return '<div class="il-spark"><svg viewBox="0 0 100 22" preserveAspectRatio="none" aria-hidden="true"><polyline fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" vector-effect="non-scaling-stroke" points="' + pts.join(' ') + '"/></svg></div>';
}
// A gauge: its label (already escaped), the percentage, what it is out of.
function infoLiveBar(id, label, pct, sub, hist) {
  return '<div class="il-row" id="il-' + id + '"><span class="il-k" title="' + label + '">' + label + '</span>' +
    '<span class="il-bar"><span class="il-fill' + infoLiveLevel(pct) + '" style="width:' + pct.toFixed(1) + '%"></span></span>' +
    '<span class="il-v">' + TH('info.percent', { n: i18nNum(pct, 0) }) + '</span>' +
    (sub ? '<span class="il-sub">' + sub + '</span>' : '') + infoLiveSpark(hist) + '</div>';
}
function infoLiveSize(used, total) { return i18nEsc(T('info.sizeOf', { used: reachFmtBytes(used), total: reachFmtBytes(total) })); }
function infoLiveRender() {
  var st = infoLive;
  if (!st || !st.el) return;
  var live = !!st.sampleAt && Date.now() - st.sampleAt < INFO_LIVE_FRESH_MS;
  var h = '<div class="il-head"><span class="il-pulse' + (live ? ' on' : '') + '" id="il-pulse"></span>' + TH('info.live') + '</div>';
  var m = st.m;
  // (each sample updates the section in place, dom_patch.go domMorph: its
  // gauges, threads and their tooltips are kept, only values change)
  if (!m) { domMorph(st.el, h + '<div class="il-wait" id="il-wait">' + TH('info.liveWaiting') + '</div>'); return; }
  var cpu = infoLivePct(m.cpu && m.cpu.percent), mem = m.memory || {}, ram = infoLivePct(mem.percent);
  if (cpu !== null) h += infoLiveBar('cpu', TH('info.cpu'), cpu, '', st.cpu);
  if (st.cores && st.cores.length > 1) h += infoThreadsHtml(st.cores, Date.now() - st.coresAt > INFO_THREADS_STALE_MS);
  if (ram !== null) {
    var mt = Number(mem.totalMb), mu = Number(mem.usedMb);
    h += infoLiveBar('ram', TH('info.ram'), ram, (isFinite(mt) && mt > 0 && isFinite(mu)) ? infoLiveSize(mu * 1048576, mt * 1048576) : '', st.ram);
  }
  var disks = (Array.isArray(m.disks) ? m.disks : []).filter(function (x) { return x && typeof x === 'object' && infoLivePct(x.percent) !== null; });
  if (disks.length) {
    h += '<div class="il-sect">' + TH('info.disks') + '</div>';
    disks.slice(0, INFO_LIVE_DISKS).forEach(function (x, i) {
      var tg = Number(x.totalGb), ug = Number(x.usedGb);
      // (the mount point is the agent's text: escaped)
      h += infoLiveBar('disk' + i, i18nEsc(String(x.mount || '?').slice(0, 64)), infoLivePct(x.percent),
        (isFinite(tg) && tg > 0 && isFinite(ug)) ? infoLiveSize(ug * 1073741824, tg * 1073741824) : '', null);
    });
  }
  // (a re-render keeps where the threads' grid was scrolled to)
  var grid = st.el.querySelector ? st.el.querySelector('.il-heat') : null;
  var top = grid ? grid.scrollTop : 0;
  domMorph(st.el, h);
  if (top) { var g2 = st.el.querySelector ? st.el.querySelector('.il-heat') : null; if (g2) g2.scrollTop = top; }
}

// ── Info panel: CPU per thread ──
// Few threads: one bar each (columns). Many (above INFO_THREADS_BARS_MAX):
// a compact heat map, one coloured cell per thread, its value in the
// tooltip, its height bounded (it scrolls) — never the whole panel. The
// section folds (remembered on this computer). The overall % stays above.
var INFO_THREADS_BARS_MAX = 16, INFO_THREADS_MAX = 512, INFO_THREADS_STALE_MS = 60000;
var INFO_THREADS_PREF = 'oblireach_info_threads_open';
var infoThreadsOpen = (function () { try { return localStorage.getItem(INFO_THREADS_PREF) !== '0'; } catch (e) { return true; } })();
function infoCoresOf(cpu) {
  if (!cpu || typeof cpu !== 'object' || !Array.isArray(cpu.cores) || !cpu.cores.length) return null;
  var out = [];
  for (var i = 0; i < cpu.cores.length && i < INFO_THREADS_MAX; i++) {
    var p = infoLivePct(cpu.cores[i]);
    out.push(p === null ? 0 : p);
  }
  return out;
}
// Heat level of a thread (cells): 0 idle .. 5 saturated.
function infoThreadLevel(p) { return p >= 90 ? 5 : p >= 75 ? 4 : p >= 50 ? 3 : p >= 25 ? 2 : p >= 5 ? 1 : 0; }
function infoThreadsHtml(cores, stale) {
  var n = cores.length, max = 0, busy = 0;
  for (var i = 0; i < n; i++) { if (cores[i] > max) max = cores[i]; if (cores[i] >= 90) busy++; }
  var sum = TH('info.threadsMax', { n: i18nNum(max, 0) }) + (busy ? ' \u00b7 ' + TH('info.threadsBusy', { n: busy }) : '');
  var chev = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg>';
  var h = '<div class="il-thr' + (infoThreadsOpen ? '' : ' closed') + (stale ? ' stale' : '') + '" id="il-threads"' +
    (stale ? ' title="' + TH('info.threadsStale') + '"' : '') + '>' +
    '<button type="button" class="il-thr-head" data-il-threads="1" aria-expanded="' + (infoThreadsOpen ? 'true' : 'false') + '" title="' + TH(infoThreadsOpen ? 'info.threadsHide' : 'info.threadsShow') + '">' +
    chev + '<span>' + TH('info.threads', { n: n }) + '</span><span class="il-thr-sum">' + sum + '</span></button>';
  if (infoThreadsOpen) {
    var cells = [];
    if (n <= INFO_THREADS_BARS_MAX) {
      for (var j = 0; j < n; j++) {
        cells.push('<span class="il-col" title="' + TH('info.threadTip', { n: j, pct: i18nNum(cores[j], 0) }) + '"><span class="' + infoLiveLevel(cores[j]).trim() + '" style="height:' + cores[j].toFixed(1) + '%"></span></span>');
      }
      h += '<div class="il-thr-body il-cols" role="img" aria-label="' + TH('info.threads', { n: n }) + '">' + cells.join('') + '</div>';
    } else {
      for (var k = 0; k < n; k++) {
        cells.push('<span class="il-cell l' + infoThreadLevel(cores[k]) + '" title="' + TH('info.threadTip', { n: k, pct: i18nNum(cores[k], 0) }) + '"></span>');
      }
      h += '<div class="il-thr-body il-heat" role="img" aria-label="' + TH('info.threads', { n: n }) + '">' + cells.join('') + '</div>';
    }
  }
  return h + '</div>';
}

// ── Privacy gate (the web's rules) ──
// Privacy on without a code: terminals and explorer blocked (toolState).
// With a code: the code is asked (POST /privacy/unlock, pinned to the
// device's tenant) unless the server holds an unlock of this user for the
// feature (GET /privacy/unlocks: 15 min sliding, 2 h at most, in the server's
// memory; shared with the web for the same user). The server injects the
// token itself: the Desktop never sees it, and never keeps the code.
var privacyUnlocks = new Map(); // devKey -> { at, list: [{ feature, expiresAt (local ms) }] }
var PRIVACY_DENIED_RE = /privacy mode is enabled/i;
// The agent's refusal ("privacy mode is enabled - remote access denied"): its
// unlock is gone (a restart, the TTL, privacy just turned on).
function privacyIsDenial(text) { return PRIVACY_DENIED_RE.test(String(text || '')); }
function privacyUnlockedUntil(dev, feature) {
  if (!dev) return 0;
  var e = privacyUnlocks.get(devKey(dev));
  if (!e || !Array.isArray(e.list)) return 0;
  var now = Date.now(), best = 0;
  for (var i = 0; i < e.list.length; i++) {
    var u = e.list[i];
    if (u && u.feature === feature && u.expiresAt > now && u.expiresAt > best) best = u.expiresAt;
  }
  return best;
}
function privacyRemember(dev, feature, expiresAt) {
  var k = devKey(dev), e = privacyUnlocks.get(k) || { at: Date.now(), list: [] };
  e.list = e.list.filter(function (u) { return u.feature !== feature; });
  e.list.push({ feature: feature, expiresAt: expiresAt });
  privacyUnlocks.set(k, e);
}
function privacyForget(dev, feature) {
  var e = dev ? privacyUnlocks.get(devKey(dev)) : null;
  if (e) e.list = e.list.filter(function (u) { return feature !== undefined && u.feature !== feature; });
}
// The server's clock against this one (its Date header), so an expiry it
// gives is read in local time; 0 when unknown.
function privacySkew(r) {
  try {
    var h = r && r.headers && typeof r.headers.get === 'function' ? r.headers.get('Date') : null;
    var t = h ? Date.parse(h) : NaN;
    return isFinite(t) ? t - Date.now() : 0;
  } catch (e) { return 0; }
}
// GET /api/devices/:id/privacy/unlocks: this user's unlocks of the device
// (scoped by user and device: not pinned). Errors leave the cache as it is.
async function privacyUnlocksRefresh(dev) {
  if (!dev) return;
  try {
    var r = await devApi(dev, 'GET', '/api/devices/' + encodeURIComponent(String(dev.id)) + '/privacy/unlocks', undefined, false);
    if (!r || !r.ok) return;
    var d = await r.json();
    var sk = privacySkew(r), skew = Math.abs(sk) > 5000 ? sk : 0; // (a few seconds apart: as given)
    var list = (d && Array.isArray(d.data)) ? d.data : [];
    var out = [];
    for (var i = 0; i < list.length; i++) {
      var u = list[i], exp = u ? Number(u.expiresAt) : NaN;
      if (u && typeof u.feature === 'string' && isFinite(exp)) out.push({ feature: u.feature, expiresAt: exp - skew });
    }
    privacyUnlocks.set(devKey(dev), { at: Date.now(), list: out });
  } catch (e) {}
}
// true: the feature may be used now (privacy off, or unlocked); false:
// blocked, or the prompt cancelled. opts.notify: where a refusal is said.
async function privacyEnsure(dev, feature, opts) {
  if (devIsLocal(dev)) return true; // (a local connection: no agent, no privacy mode)
  var pv = devPrivacy(dev);
  if (!pv.enabled) return true;
  if (!pv.passwordSet) { toolsSay(opts, T('tools.reason.privacyBlocked')); return false; }
  await privacyUnlocksRefresh(dev);
  if (privacyUnlockedUntil(dev, feature)) return true;
  return privacyPrompt(dev, feature, opts);
}
// The agent refused an action for privacy after the fact: the unlock is
// forgotten, the device's flags read again (its server), and the code asked
// again (the prompt says why). Resolves to the device to retry ONCE with, or
// null (blocked, cancelled).
async function privacyRetryGate(dev, feature) {
  if (!dev) return null;
  if (devIsLocal(dev)) return dev; // (a local connection: never a server read)
  privacyForget(dev, feature);
  var id = String(dev.id), pid = devPid(dev);
  try { if (typeof loadOverview === 'function') await loadOverview(pid); } catch (e) {}
  var fresh = reachFindDevice(id, pid) || dev;
  var pv = devPrivacy(fresh);
  if (!pv.enabled) return fresh; // (turned off meanwhile)
  if (!pv.passwordSet) { reachNotify(T('tools.reason.privacyBlocked'), 'warn'); return null; }
  return (await privacyPrompt(fresh, feature, { denied: true })) ? fresh : null;
}
function privacyFeatureName(feature) { return T(feature === 'files' ? 'privacy.feature.files' : 'privacy.feature.remote'); }
// An answer of POST /privacy/unlock, in words.
function privacyErrText(status, d, fallback) {
  // 401: the agent's refusal of the code (a wrong code, NOT a sign-out);
  // only the server's own "Authentication required" is one.
  if (status === 401 && d && d.error === 'Authentication required') return T('privacy.err.failed', { error: T('detached.signedOut') });
  if (status === 401) return T('privacy.err.incorrect');
  if (status === 429) return T('privacy.err.tooMany');
  if (status === 503) return T('privacy.err.agentOffline');
  if (status === 400) return T('privacy.err.noCode');
  if (status === 403) return T('tools.reason.notPermitted');
  if (status === 404) return T('privacy.err.notFound');
  var e = (d && typeof d.error === 'string' && d.error) ? d.error : (fallback || (status ? 'HTTP ' + status : T('common.error')));
  return T('privacy.err.failed', { error: e });
}
// The device's security code, asked in a dialog. Resolves true once
// unlocked, false when cancelled (a new prompt cancels the one on screen).
// The code is sent once and dropped: never stored, the field emptied.
var privacyPromptDone = null;
function privacyPrompt(dev, feature, opts) {
  if (privacyPromptDone) privacyPromptDone(false);
  opts = opts || {};
  return new Promise(function (resolve) {
    var busy = false, done = false;
    var ov = document.createElement('div'); ov.className = 'modal-overlay visible ppv-overlay'; ov.id = 'ppv-overlay';
    var box = document.createElement('div'); box.className = 'modal-box';
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-label', T('privacy.title'));
    var head = document.createElement('div'); head.className = 'modal-head';
    var ico = document.createElement('div'); ico.className = 'icon'; ico.innerHTML = TOOLS_ICON.lock.replace(/width="12" height="12"/, 'width="18" height="18"');
    var h = document.createElement('h3'); h.textContent = T('privacy.title');
    var x = document.createElement('button'); x.className = 'close'; x.id = 'ppv-close'; x.title = T('common.cancel');
    x.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    head.appendChild(ico); head.appendChild(h); head.appendChild(x);
    var desc = document.createElement('p'); desc.className = 'desc'; desc.id = 'ppv-desc';
    desc.textContent = T('privacy.desc', { device: reachDevName(dev), feature: privacyFeatureName(feature) });
    var pid = devPid(dev), multi = typeof serverMode !== 'undefined' && serverMode === 'multi';
    var srvEl = document.createElement('p'); srvEl.className = 'desc'; srvEl.id = 'ppv-server';
    srvEl.textContent = multi && typeof srvName === 'function' ? T('tfa.server', { server: srvName(pid) }) : '';
    srvEl.style.display = srvEl.textContent ? '' : 'none';
    var input = document.createElement('input'); input.id = 'ppv-code'; input.className = 'ppv-input';
    input.type = 'password'; input.setAttribute('autocomplete', 'off'); input.setAttribute('maxlength', '256'); input.maxLength = 256;
    input.setAttribute('aria-label', T('privacy.code')); input.placeholder = T('privacy.code');
    var hint = document.createElement('p'); hint.className = 'desc ppv-hint'; hint.id = 'ppv-hint'; hint.textContent = T('privacy.hint');
    var err = document.createElement('div'); err.className = 'modal-err'; err.id = 'ppv-error'; err.setAttribute('role', 'alert');
    err.textContent = opts.denied ? T('privacy.denied') : '';
    var acts = document.createElement('div'); acts.className = 'modal-actions';
    var cancel = document.createElement('button'); cancel.className = 'btn-ghost'; cancel.id = 'ppv-cancel'; cancel.textContent = T('common.cancel');
    var submit = document.createElement('button'); submit.className = 'btn-primary'; submit.id = 'ppv-submit';
    submit.style.cssText = 'padding:8px 18px;font-size:12px;border-radius:10px';
    submit.textContent = T(opts.denied ? 'privacy.retry' : 'privacy.submit');
    submit.disabled = true;
    acts.appendChild(cancel); acts.appendChild(submit);
    box.appendChild(head); box.appendChild(desc); box.appendChild(srvEl); box.appendChild(input); box.appendChild(hint); box.appendChild(err); box.appendChild(acts);
    ov.appendChild(box);
    var setBusy = function (on) {
      busy = on;
      input.disabled = on; cancel.disabled = on; x.disabled = on;
      submit.disabled = on || !input.value;
    };
    var finish = function (v) {
      if (done) return;
      done = true;
      if (privacyPromptDone === finish) privacyPromptDone = null;
      input.value = '';
      ov.classList.remove('visible');
      if (ov.parentElement && typeof ov.parentElement.removeChild === 'function') { try { ov.parentElement.removeChild(ov); } catch (e) {} }
      resolve(v);
    };
    var cancelIt = function () { if (!busy) finish(false); };
    var send = async function () {
      if (busy || done || !input.value) return;
      var code = input.value;
      setBusy(true); err.textContent = '';
      var r = null, d = null, fail = '';
      try {
        r = await devApi(dev, 'POST', '/api/devices/' + encodeURIComponent(String(dev.id)) + '/privacy/unlock', { password: code, feature: feature }, true);
      } catch (e) { fail = reachErrText(e); }
      code = '';
      if (r) { try { d = await r.json(); } catch (e) { d = null; } }
      if (done) return; // (replaced by another prompt meanwhile)
      if (r && r.ok) {
        var ttl = Number(d && d.data && d.data.ttlSeconds);
        privacyRemember(dev, feature, Date.now() + (isFinite(ttl) && ttl > 0 ? ttl : 900) * 1000);
        reachNotify(T('privacy.unlocked', { feature: privacyFeatureName(feature) }), 'ok');
        finish(true);
        return;
      }
      input.value = '';
      setBusy(false);
      err.textContent = privacyErrText(r ? r.status : 0, d, fail);
      try { input.focus(); } catch (e) {}
    };
    input.addEventListener('input', function () { if (!busy) submit.disabled = !input.value; });
    input.addEventListener('keydown', function (e) {
      if (!e) return;
      if (e.key === 'Enter') { if (e.preventDefault) e.preventDefault(); send(); }
    });
    ov.addEventListener('keydown', function (e) {
      if (e && e.key === 'Escape') { if (e.preventDefault) e.preventDefault(); if (e.stopPropagation) e.stopPropagation(); cancelIt(); }
    });
    submit.addEventListener('click', function () { send(); });
    cancel.addEventListener('click', cancelIt);
    x.addEventListener('click', cancelIt);
    ov.addEventListener('mousedown', function (e) { if (e && e.target === ov) cancelIt(); }); // the backdrop
    privacyPromptDone = finish;
    document.body.appendChild(ov);
    try { input.focus(); } catch (e) {}
  });
}

// ── Info panel: "More details" ──
// Below "Live" (which stays where it is), a folding section (remembered on
// this computer) with what Obliance knows of the machine: its maker and model
// as a person says them, serial number (and the maker's support page),
// firmware, processor, memory, disks and volumes, graphics, network cards,
// system, agents. Only what the routes give this user, rows without a value
// left out, every value copied by a click.
// Sources: the hardware inventory GET /api/inventory/:id/hardware (read ONCE
// for the panel, shared with the LAN addresses above: infoHwGet, kept
// INFO_HW_TTL_MS, reloaded by its button), the device record the panel
// reads anyway (GET /api/devices/:id, by the live section or the addresses:
// infoRecSet; never read again for this), the overview's device. On the
// device's server and tenant (devApi, pinned). 403: the hardware rows are
// left out, one discreet line says why. The side panel scrolls (its body).
var INFO_HW_TTL_MS = 300000, INFO_HW_ERR_TTL_MS = 30000;
var infoHwCache = new Map(); // devKey -> { at, status: loading|ok|none|denied|error, data, p }
var infoRecCache = new Map(); // devKey -> the device record (GET /api/devices/:id data)
var INFO_MORE_PREF = 'oblireach_info_more_open';
var infoMoreIsOpen = (function () { try { return localStorage.getItem(INFO_MORE_PREF) === '1'; } catch (e) { return false; } })();
var infoMore = null; // { key, dev, el, last, copies: [], links: [] }

// The hardware inventory of a device: one request at a time, kept a few
// minutes (an error less long). force: read again (the reload button).
function infoHwGet(dev, force) {
  if (!dev) return Promise.resolve({ status: 'none', data: null });
  var key = devKey(dev), e = infoHwCache.get(key);
  if (e && e.p) return e.p;
  if (e && !force && Date.now() - e.at < (e.status === 'error' ? INFO_HW_ERR_TTL_MS : INFO_HW_TTL_MS)) return Promise.resolve(e);
  var ne = { at: Date.now(), status: 'loading', data: null, p: null };
  ne.p = (async function () {
    var st = 'error', data = null;
    try {
      var r = await devApi(dev, 'GET', '/api/inventory/' + encodeURIComponent(String(dev.id)) + '/hardware', undefined, true);
      if (r && r.ok) {
        var j = await r.json();
        data = (j && j.data && typeof j.data === 'object' && !Array.isArray(j.data)) ? j.data : null;
        st = data ? 'ok' : 'none';
      } else if (r && (r.status === 401 || r.status === 403)) st = 'denied';
      else if (r && r.status === 404) st = 'none'; // (no inventory yet, or a device out of reach: the server says 404 for both)
    } catch (x) { st = 'error'; }
    ne.status = st; ne.data = data; ne.at = Date.now(); ne.p = null;
    return ne;
  })();
  infoHwCache.set(key, ne);
  return ne.p;
}
// The device record the panel read: "More details" redrawn when shown.
function infoRecSet(dev, data) {
  if (!dev || !data || typeof data !== 'object') return;
  infoRecCache.set(devKey(dev), data);
  if (infoMore && infoMore.key === devKey(dev)) infoMoreRender();
}

// ── Makers and models (the rules: hwinfo.go, INFO_HW_RULES) ──
var infoHwReCache = null;
function infoHwRe() {
  if (infoHwReCache) return infoHwReCache;
  var R = (typeof INFO_HW_RULES === 'object' && INFO_HW_RULES) ? INFO_HW_RULES : { vendors: [], vms: [], junk: '^$', serial: '^$' };
  var mk = function (p, f) { try { return new RegExp(p, f); } catch (e) { return /$^/; } };
  infoHwReCache = {
    R: R,
    vendors: R.vendors.map(function (v) { return mk(v.match, 'i'); }),
    serials: R.vendors.map(function (v) { return v.serial ? mk(v.serial, '') : null; }),
    vms: R.vms.map(function (v) { return mk(v.match, 'i'); }),
    junk: mk(R.junk, 'i'), serial: mk(R.serial, ''),
  };
  return infoHwReCache;
}
// A firmware string: trimmed, "" for a placeholder ("To Be Filled By
// O.E.M.", "Default string", "0000000"...).
function infoHwClean(s) {
  if (s === null || s === undefined || typeof s === 'object' || typeof s === 'boolean') return '';
  s = String(s).replace(/[\t\n\f\r ]+/g, ' ').trim();
  if (!s || infoHwRe().junk.test(s)) return '';
  if (s.length > 1) {
    var same = true;
    for (var i = 1; i < s.length; i++) if (s[i] !== s[0]) { same = false; break; }
    if (same) return '';
  } else if (s === '0' || s === '.' || s === '-') return '';
  return s;
}
function infoHwVendorIdx(man) {
  man = infoHwClean(man);
  if (!man) return -1;
  var re = infoHwRe();
  for (var i = 0; i < re.vendors.length; i++) if (re.vendors[i].test(man)) return i;
  return -1;
}
function infoHwVmOf(parts) {
  var keep = [];
  for (var i = 0; i < parts.length; i++) { var c = infoHwClean(parts[i]); if (c) keep.push(c); }
  var all = keep.join(' | '), re = infoHwRe();
  for (var j = 0; j < re.vms.length; j++) if (re.vms[j].test(all)) return re.R.vms[j].key;
  return '';
}
function infoHwVmName(key) {
  var vms = infoHwRe().R.vms;
  for (var i = 0; i < vms.length; i++) if (vms[i].key === key) return vms[i].name;
  return key;
}
var INFO_HW_SUFFIX = /[ ,]+(inc\.?|incorporated|corp\.?|corporation|co\.?,? ?ltd\.?|co\.?|ltd\.?|limited|llc|gmbh|s\.?a\.?|b\.?v\.?|ag|technology|technologies|computer|computers|international|electronics)$/i;
function infoHwTidyMaker(man) {
  var s = infoHwClean(man);
  for (var i = 0; i < 4; i++) {
    var t = s.replace(INFO_HW_SUFFIX, '').trim();
    if (t === s || !t) break;
    s = t;
  }
  if (s && s === s.toUpperCase() && s.length > 3) {
    s = s.toLowerCase().split(/\s+/).filter(Boolean).map(function (w) { return w.charAt(0).toUpperCase() + w.slice(1); }).join(' ');
  }
  return s;
}
function infoHwDedup(m) {
  var i = m.indexOf('_');
  if (i > 0 && i === m.length - i - 1 && m.slice(0, i) === m.slice(i + 1)) return m.slice(0, i);
  var j = m.lastIndexOf('_');
  if (j > 0) {
    var head = m.slice(0, j), tail = m.slice(j + 1);
    if (tail && head.slice(-tail.length) === tail) return head;
  }
  return m;
}
// The label of a machine (hwinfo.go hwMachine, the same rules and cases):
// { label, maker, vendor, vm }.
function infoHwMachine(manufacturer, model, family, extra) {
  var out = { label: '', maker: '', vendor: '', vm: '' };
  var vm = infoHwVmOf([manufacturer, model, family].concat(Array.isArray(extra) ? extra : []));
  if (vm) { out.vm = vm; return out; }
  var R = infoHwRe().R, vi = infoHwVendorIdx(manufacturer);
  if (vi >= 0) { out.vendor = R.vendors[vi].key; out.maker = R.vendors[vi].name; } else out.maker = infoHwTidyMaker(manufacturer);
  var m = infoHwDedup(infoHwClean(model)), fam = infoHwClean(family);
  if (vi >= 0 && R.vendors[vi].modelFromFamily && fam && fam.indexOf(' ') >= 0 && (!m || /^[0-9A-Z]{4,12}$/.test(m))) m = fam;
  if (!m) return out;
  var low = m.toLowerCase(), pre = [out.maker.toLowerCase(), infoHwClean(manufacturer).toLowerCase()];
  for (var i = 0; i < pre.length; i++) {
    if (pre[i] && low.indexOf(pre[i] + ' ') === 0) { m = m.slice(pre[i].length).trim(); break; }
  }
  if (out.vendor === 'apple' && fam && fam.toLowerCase() !== m.toLowerCase()) m += ' (' + fam + ')';
  out.label = out.maker ? out.maker + ' ' + m : m;
  return out;
}
// A serial number as shown ('' a placeholder or odd characters).
function infoHwSerial(s) {
  s = infoHwClean(s);
  return (s && infoHwRe().serial.test(s)) ? s : '';
}
// The maker's support page for a serial (hwinfo.go hwSupportURL decides and
// opens it): { vendor, serial, inUrl } or null.
function infoHwSupport(vendor, serial) {
  var s = infoHwSerial(serial).toUpperCase(), re = infoHwRe();
  if (!s || !vendor) return null;
  for (var i = 0; i < re.R.vendors.length; i++) {
    var v = re.R.vendors[i];
    if (v.key !== vendor || !v.support) continue;
    if (!re.serials[i] || !re.serials[i].test(s)) return null;
    return { vendor: v.key, serial: s, inUrl: !!v.serialInUrl };
  }
  return null;
}

// ── What the inventory says (pure: tested) ──
function infoHwObj(v) { return (v && typeof v === 'object' && !Array.isArray(v)) ? v : {}; }
function infoHwArr(v) { return Array.isArray(v) ? v : []; }
function infoHwPick() { for (var i = 0; i < arguments.length; i++) { var c = infoHwClean(arguments[i]); if (c) return c; } return ''; }
// The machine itself (not its board): a server that keeps it (hw.system,
// hw.bios.serial), else the agent's raw report: Windows raw.wmi.system /
// raw.wmi.bios.serial (Win32_ComputerSystem / Win32_BIOS), Linux raw.dmi
// (/sys/class/dmi/id), macOS raw.system_profiler (SPHardwareDataType).
function infoHwSystem(hw) {
  hw = infoHwObj(hw);
  var raw = infoHwObj(hw.raw), wmi = infoHwObj(raw.wmi), dmi = infoHwObj(raw.dmi), sys = infoHwObj(hw.system), ws = infoHwObj(wmi.system);
  var sp = infoHwObj(raw.system_profiler), mac = infoHwObj(infoHwArr(sp.SPHardwareDataType)[0]);
  var out = {
    manufacturer: infoHwPick(sys.manufacturer, sys.vendor, ws.manufacturer, ws.vendor, dmi.sys_vendor),
    model: infoHwPick(sys.model, sys.product, ws.model, ws.product, dmi.product_name),
    family: infoHwPick(sys.family, sys.version, ws.family, ws.version, dmi.product_version, dmi.product_family),
    serial: infoHwPick(sys.serial, infoHwObj(hw.bios).serial, ws.serial, infoHwObj(wmi.bios).serial, dmi.product_serial),
    chassis: '', mac: false,
  };
  if (!out.model && infoHwClean(mac.machine_name)) {
    out.manufacturer = 'Apple Inc.'; out.model = infoHwClean(mac.machine_name); out.family = infoHwClean(mac.machine_model);
    out.serial = out.serial || infoHwClean(mac.serial_number); out.mac = true;
    if (/macbook/i.test(out.model)) out.chassis = 'laptop';
    else if (/imac/i.test(out.model)) out.chassis = 'allInOne';
    else if (/mac ?(mini|pro|studio)/i.test(out.model)) out.chassis = 'desktop';
  }
  out.chassis = out.chassis || infoHwChassisWord(sys.chassis) || infoHwChassisWord(ws.chassis) ||
    infoHwChassisSmbios(sys.chassisType) || infoHwChassisSmbios(dmi.chassis_type) || infoHwChassisPcType(ws.pcSystemType);
  return out;
}
function infoHwChassisWord(v) {
  var s = String(v === undefined || v === null ? '' : v).toLowerCase();
  if (!s || typeof v === 'number') return '';
  if (/laptop|notebook|portable|mobile|convertible|detachable/.test(s)) return 'laptop';
  if (/tablet|slate/.test(s)) return 'tablet';
  if (/server|rack|blade/.test(s)) return 'server';
  if (/all.?in.?one/.test(s)) return 'allInOne';
  if (/desktop|tower|mini ?pc/.test(s)) return 'desktop';
  return '';
}
// SMBIOS chassis types (dmi chassis_type, Win32_SystemEnclosure).
function infoHwChassisSmbios(v) {
  var n = Number(Array.isArray(v) ? v[0] : v);
  if (!Number.isInteger(n)) return '';
  if ([8, 9, 10, 14, 31, 32].indexOf(n) >= 0) return 'laptop';
  if (n === 30) return 'tablet';
  if (n === 13) return 'allInOne';
  if ([17, 23, 25, 28, 29].indexOf(n) >= 0) return 'server';
  if ([3, 4, 5, 6, 7, 15, 16, 24, 35, 36].indexOf(n) >= 0) return 'desktop';
  return '';
}
// Win32_ComputerSystem.PCSystemType.
function infoHwChassisPcType(v) {
  var n = Number(v);
  return n === 2 ? 'laptop' : n === 8 ? 'tablet' : (n === 4 || n === 5 || n === 7) ? 'server' : (n === 1 || n === 3) ? 'desktop' : '';
}
var INFO_CHASSIS_KEY = { laptop: 'info.more.chassisLaptop', desktop: 'info.more.chassisDesktop', server: 'info.more.chassisServer',
  tablet: 'info.more.chassisTablet', allInOne: 'info.more.chassisAllInOne', vm: 'info.more.chassisVm' };
// A firmware or inventory date: WMI ("20230915000000.000000+000"), US
// ("09/15/2023"), ISO; null when unreadable.
function infoHwDate(v) {
  var s = infoHwClean(v);
  if (!s) return null;
  var m = /^(\d{4})(\d{2})(\d{2})\d{6}/.exec(s);
  if (m) return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  m = /^(\d{1,2})\/(\d{1,2})\/(\d{4})$/.exec(s);
  if (m) return new Date(Number(m[3]), Number(m[1]) - 1, Number(m[2]));
  var t = Date.parse(s);
  return isFinite(t) ? new Date(t) : null;
}
function infoHwDay(d) { try { return d.toLocaleDateString(i18nLang()); } catch (e) { return d.toISOString().slice(0, 10); } }
function infoHwDur(ms) {
  var m = Math.max(0, Math.floor(ms / 60000)), d = Math.floor(m / 1440), h = Math.floor((m % 1440) / 60);
  if (d) return T('info.more.durDays', { d: d, h: h });
  if (h) return T('info.more.durHours', { h: h, m: m % 60 });
  return T('info.more.durMinutes', { m: m });
}

// The sections: [{ key, rows: [{ label | text, html, copy, link }] }], in
// order, empty ones left out. he: infoHwGet's entry; rec: the device record.
function infoMoreFacts(dev, he, rec) {
  dev = dev || {}; rec = infoHwObj(rec);
  var hw = (he && he.status === 'ok' && he.data) ? he.data : null, H = infoHwObj(hw), raw = infoHwObj(H.raw), wmi = infoHwObj(raw.wmi);
  var S = [], cur = null;
  var sect = function (k) { cur = { key: k, rows: [] }; S.push(cur); };
  var E = function (s) { return i18nEsc(String(s).slice(0, 200)); };
  var mono = function (s) { return '<span class="im-mono">' + E(s) + '</span>'; };
  var add = function (label, html, copy, more) {
    if (!html) return;
    var r = { html: html, copy: copy || '' };
    if (typeof label === 'string') r.label = label; else r.text = String(label.text).slice(0, 80);
    if (more) for (var k in more) r[k] = more[k];
    cur.rows.push(r);
  };
  var sep = ' · ';

  // Machine
  sect('info.more.machine');
  var sy = infoHwSystem(H), mb = infoHwObj(H.motherboard), bios = infoHwObj(H.bios), m = { vm: '', label: '', maker: '', vendor: '' };
  if (hw) {
    m = infoHwMachine(sy.manufacturer, sy.model, sy.family, [bios.vendor, mb.manufacturer, mb.model, mb.version]);
    if (m.vm) { var vt = T('info.more.vm', { name: infoHwVmName(m.vm) }); add('info.more.model', E(vt), vt); }
    else if (m.label) add('info.more.model', E(m.label), m.label);
    else if (m.maker) add('info.more.maker', E(m.maker), m.maker);
    if (!m.vm && !m.label) { // (the machine itself not reported: its board, said as such)
      var bm = infoHwMachine(mb.manufacturer, mb.model, '');
      if (bm.label) add('info.more.board', E(bm.label), bm.label);
    }
    var serial = infoHwSerial(sy.serial);
    if (serial) {
      var link = m.vm ? null : infoHwSupport(m.vendor || (infoHwVendorIdx(sy.manufacturer) >= 0 ? infoHwRe().R.vendors[infoHwVendorIdx(sy.manufacturer)].key : ''), serial);
      add('info.more.serial', mono(serial), serial, link ? { link: link } : null);
    }
    var ch = m.vm ? 'vm' : sy.chassis;
    if (!ch && infoHwObj(H.battery).present === true) ch = 'laptop';
    if (!ch && /server/i.test(infoHwClean(infoHwObj(H.os).edition) || String(dev.osName || ''))) ch = 'server';
    if (ch && INFO_CHASSIS_KEY[ch]) add('info.more.chassis', TH(INFO_CHASSIS_KEY[ch]), T(INFO_CHASSIS_KEY[ch]));
  }
  var wd = infoHwDate(rec.warrantyExpiry);
  if (wd) {
    var wt = T(wd.getTime() < Date.now() ? 'info.more.warrantyExpired' : 'info.more.warrantyUntil', { date: infoHwDay(wd) });
    var wv = infoHwClean(rec.warrantyVendor);
    add('info.more.warranty', E(wt) + (wv ? sep + E(wv) : ''), wt);
  }
  var pd = infoHwDate(rec.purchaseDate);
  if (pd) add('info.more.purchased', E(infoHwDay(pd)), infoHwDay(pd));
  var bat = infoHwObj(H.battery);
  if (bat.present === true) {
    var bp = [], hp = Number(bat.healthPercent), cy = Number(bat.cycleCount);
    if (isFinite(hp) && hp > 0) bp.push(T('info.more.batteryHealth', { pct: i18nNum(Math.min(hp, 100), 0) }));
    if (isFinite(cy) && cy > 0) bp.push(T('info.more.batteryCycles', { n: cy }));
    if (bp.length) add('info.more.battery', E(bp.join(sep)), bp.join(sep));
  }

  // Firmware
  sect('info.more.firmware');
  if (hw) {
    var bv = [infoHwClean(bios.vendor), infoHwClean(bios.version)].filter(Boolean).join(' ');
    if (bv) add('info.more.bios', E(bv), bv);
    var bd = infoHwDate(bios.date);
    if (bd) add('info.more.biosDate', E(infoHwDay(bd)), infoHwDay(bd));
    var sb = typeof H.secureBoot === 'boolean' ? H.secureBoot : typeof wmi.secureBoot === 'boolean' ? wmi.secureBoot : null;
    if (sb !== null) add('info.more.secureBoot', TH(sb ? 'info.more.on' : 'info.more.off'), T(sb ? 'info.more.on' : 'info.more.off'));
    var tpm = (H.tpm && typeof H.tpm === 'object') ? H.tpm : (wmi.tpm && typeof wmi.tpm === 'object') ? wmi.tpm : null;
    if (tpm && tpm.present === false) add('info.more.tpm', TH('info.more.tpmAbsent'), T('info.more.tpmAbsent'));
    else if (tpm && tpm.present === true) {
      var tv = (/^\s*(\d+(\.\d+)?)/.exec(String(tpm.specVersion || '')) || [])[1] || '';
      var ton = tpm.enabled === true && tpm.activated === true;
      var tt = (tv ? 'TPM ' + tv + sep : '') + T(ton ? 'info.more.on' : 'info.more.off') + (infoHwClean(tpm.manufacturerName) ? sep + infoHwClean(tpm.manufacturerName) : '');
      add('info.more.tpm', E(tt), tt);
    }
  }

  // Processor
  sect('info.more.cpu');
  var cpu = infoHwObj(H.cpu);
  var cm = infoHwPick(cpu.model, rec.cpuModel);
  if (cm) add('info.more.model', E(cm), cm);
  var co = Number(cpu.cores) || Number(rec.cpuCores) || 0, th = Number(cpu.threads) || 0;
  if (co > 0) {
    var ct = th > 0 && th !== co ? T('info.more.coresThreads', { cores: co, threads: th }) : T('info.more.cores', { n: co });
    add('info.more.cpuCores', E(ct), ct);
  }
  var gz = Number(cpu.speed);
  if (isFinite(gz) && gz > 0) { var gt = T('info.more.ghz', { n: i18nNum(gz, 2) }); add('info.more.frequency', E(gt), gt); }

  // Memory
  sect('info.more.memory');
  var mem = infoHwObj(H.memory), tot = Number(mem.total) > 0 ? Number(mem.total) : (Number(rec.ramTotalGb) > 0 ? Number(rec.ramTotalGb) * 1073741824 : 0);
  if (tot > 0) add('info.more.memTotal', E(reachFmtBytes(tot)), reachFmtBytes(tot));
  var slots = infoHwArr(mem.slots).filter(function (s) { return s && Number(s.size) > 0; });
  if (slots.length) {
    var groups = [], idx = {};
    slots.forEach(function (s) {
      var ty = infoHwClean(s.type); if (/^unknown$/i.test(ty)) ty = '';
      var k = reachFmtBytes(Number(s.size)) + (ty ? ' ' + ty : '') + (Number(s.speed) > 0 ? ' ' + Number(s.speed) + ' MHz' : '');
      if (idx[k] === undefined) { idx[k] = groups.length; groups.push({ k: k, n: 0 }); }
      groups[idx[k]].n++;
    });
    var mt = groups.map(function (g) { return g.n + ' × ' + g.k; }).join(', ');
    add('info.more.modules', E(mt), mt);
  }

  // Storage: the disks, then the volumes (the record's last push, BitLocker)
  sect('info.more.storage');
  infoHwArr(H.disks).slice(0, 12).forEach(function (d, i) {
    if (!d || typeof d !== 'object') return;
    var dm = infoHwPick(d.model, d.device), sz = Number(d.size), ty = infoHwClean(d.type);
    if (/^unknown$/i.test(ty)) ty = '';
    var parts = [dm, sz > 0 ? reachFmtBytes(sz) : '', ty].filter(Boolean);
    if (parts.length) add({ text: T('info.more.disk', { n: i + 1 }) }, E(parts.join(sep)), dm || parts.join(sep));
  });
  var lm = rec.latestMetrics;
  if (typeof lm === 'string') { try { lm = JSON.parse(lm); } catch (e) { lm = null; } }
  var vols = infoHwArr(infoHwObj(lm).disks), bl = {};
  infoHwArr(H.bitlocker).forEach(function (b) {
    if (b && typeof b === 'object' && infoHwClean(b.driveLetter)) bl[infoHwClean(b.driveLetter).replace(/[:\\\/]+$/, '').toUpperCase()] = b; // (status only: never its recovery keys)
  });
  var blText = function (b) {
    if (!b) return '';
    var p = Number(b.encryptionPercentage);
    if (/progress/i.test(String(b.status || '')) && isFinite(p)) return T('info.more.bitlockerProgress', { pct: i18nNum(p, 0) });
    if (String(b.protectionStatus) === 'On' || /^fullyencrypted$/i.test(String(b.status || ''))) return T('info.more.bitlockerOn');
    if (String(b.protectionStatus) === 'Off' || /decrypted/i.test(String(b.status || ''))) return T('info.more.bitlockerOff');
    return '';
  };
  var seenVol = {};
  vols.slice(0, 12).forEach(function (v) {
    if (!v || typeof v !== 'object') return;
    var mnt = infoHwClean(v.mount); if (!mnt) return;
    var letter = mnt.replace(/[:\\\/]+$/, '').toUpperCase(), tg = Number(v.totalGb), ug = Number(v.usedGb);
    seenVol[letter] = true;
    var parts = [];
    if (isFinite(tg) && tg > 0 && isFinite(ug)) parts.push(T('info.sizeOf', { used: reachFmtBytes(ug * 1073741824), total: reachFmtBytes(tg * 1073741824) }));
    var b = blText(bl[letter]); if (b) parts.push(b);
    if (parts.length) add({ text: mnt }, E(parts.join(sep)), mnt);
  });
  Object.keys(bl).forEach(function (letter) {
    if (seenVol[letter]) return;
    var b = blText(bl[letter]);
    if (b) add({ text: letter + ':' }, E(b), letter + ':');
  });

  // Graphics
  sect('info.more.graphics');
  infoHwArr(H.gpu).slice(0, 6).forEach(function (g) {
    if (!g || typeof g !== 'object') return;
    var gn = infoHwPick(g.name, g.model); if (!gn) return;
    var parts = [gn], vr = Number(g.vram);
    if (vr > 0) parts.push(reachFmtBytes(vr));
    if (infoHwClean(g.driver)) parts.push(T('info.more.driver', { v: infoHwClean(g.driver) }));
    add('info.more.gpu', E(parts.join(sep)), gn);
  });
  // (displays: macOS reports them, SPDisplaysDataType)
  infoHwArr(infoHwObj(raw.system_profiler).SPDisplaysDataType).forEach(function (g) {
    infoHwArr(infoHwObj(g).spdisplays_ndrvs).slice(0, 6).forEach(function (s) {
      s = infoHwObj(s);
      var dn = infoHwPick(s._name), rs = infoHwPick(s._spdisplays_resolution, s.spdisplays_resolution);
      if (dn || rs) add('info.more.display', E([dn, rs].filter(Boolean).join(sep)), dn || rs);
    });
  });

  // Network cards (their addresses are above: LAN / WAN)
  sect('info.more.network');
  infoHwArr(H.networkInterfaces).slice(0, 10).forEach(function (n) {
    if (!n || typeof n !== 'object') return;
    var mac = infoHwClean(n.mac || n.macAddress), nm = infoHwClean(n.name);
    if (!mac || /^(00[:-]){5}00$/i.test(mac) || /^lo\d*$|loopback/i.test(nm)) return;
    var extra = [infoHwClean(n.type), infoHwClean(n.speed)].filter(Boolean);
    add({ text: nm || T('info.more.network') }, mono(mac) + (extra.length ? '<span class="im-dim">' + sep + E(extra.join(sep)) + '</span>' : ''), mac);
  });

  // System
  sect('info.more.system');
  var os = infoHwObj(H.os);
  var ed = infoHwPick(os.edition, dev.osName), dv = infoHwClean(os.displayVersion), bn = infoHwClean(os.buildNumber);
  if (ed) {
    var ot = ed + (dv && ed.indexOf(dv) < 0 ? ' ' + dv : '') + (bn ? sep + T('info.more.build', { b: bn }) : '');
    add('info.more.os', E(ot), ot);
  }
  if (infoHwClean(rec.osArch)) add('info.more.arch', E(infoHwClean(rec.osArch)), infoHwClean(rec.osArch));
  var idt = infoHwDate(os.installDate);
  if (idt) add('info.more.installed', E(infoHwDay(idt)), infoHwDay(idt));
  var lb = infoHwDate(rec.lastRebootAt);
  if (lb) {
    var lt = fmtDateTime(lb.getTime());
    var up = (typeof devAgentReachable === 'function' && devAgentReachable(dev)) ? T('info.more.upFor', { dur: infoHwDur(Date.now() - lb.getTime()) }) : '';
    add('info.more.lastBoot', E(lt) + (up ? '<span class="im-dim">' + sep + E(up) + '</span>' : ''), lt);
  }
  if (rec.rebootPending === true) add('info.more.reboot', '<span class="im-warn">' + TH('info.more.rebootPending') + '</span>', T('info.more.rebootPending'));
  var dom = infoHwPick(rec.domain, os.domain);
  if (dom) add('info.more.domain', E(dom), dom);
  if (infoHwClean(rec.timezone)) add('info.more.timezone', E(infoHwClean(rec.timezone)), infoHwClean(rec.timezone));
  if (infoHwClean(rec.psVersion)) add('info.more.powershell', E(infoHwClean(rec.psVersion)), infoHwClean(rec.psVersion));
  if (infoHwClean(os.officeVersion)) add('info.more.office', E(infoHwClean(os.officeVersion)), infoHwClean(os.officeVersion));

  // Agents
  sect('info.more.agents');
  var ag = infoHwObj(dev.agent);
  var av = infoHwPick(rec.agentVersion, ag.version);
  if (av) {
    var vars = [];
    if (rec.agentFlavor === 'legacy' || ag.flavor === 'legacy') vars.push(T('tools.state.legacy'));
    else if (rec.agentBuild === 'nt6') vars.push('nt6');
    add('info.more.agentObliance', E(av) + (vars.length ? ' <span class="im-dim">(' + E(vars.join(', ')) + ')</span>' : ''), av);
  }
  var rv = infoHwPick(infoHwObj(dev.oblireach).version);
  if (rv) add('info.more.agentReach', E(rv), rv);
  var lp = infoHwDate(rec.lastPushAt);
  if (lp) add('info.more.lastReport', E(fmtDateTime(lp.getTime())), fmtDateTime(lp.getTime()));

  return S.filter(function (s) { return s.rows.length > 0; });
}

// ── Its section in the panel ──
function infoMoreOpen(dev, host) {
  if (!dev || !host || (typeof devIsLocal === 'function' && devIsLocal(dev))) { infoMore = null; return; }
  var el = document.createElement('div'); el.className = 'info-more'; el.id = 'info-more';
  el.addEventListener('click', infoMoreClick);
  el.addEventListener('keydown', function (e) { if (e && (e.key === 'Enter' || e.key === ' ')) { if (infoMoreClick(e) && e.preventDefault) e.preventDefault(); } });
  host.appendChild(el);
  var st = infoMore = { key: devKey(dev), dev: dev, el: el, last: '', copies: [], links: [] };
  infoMoreRender();
  if (infoMoreIsOpen) infoMoreLoad(st, false);
}
function infoMoreLoad(st, force) {
  var p = infoHwGet(st.dev, force);
  infoMoreRender();
  return p.then(function () { if (infoMore === st) infoMoreRender(); });
}
function infoMoreClose() { infoMore = null; }
function infoMoreClick(e) {
  var t = e && e.target && e.target.closest ? e.target.closest('[data-im]') : null;
  if (!t) return false;
  infoMoreAction(t.getAttribute('data-im'), Number(t.getAttribute('data-im-i')));
  return true;
}
// An action of the section: 'toggle', 'reload', 'copy' (i), 'support' (i).
function infoMoreAction(a, i) {
  var st = infoMore;
  if (!st) return null;
  if (a === 'toggle') {
    infoMoreIsOpen = !infoMoreIsOpen;
    try { localStorage.setItem(INFO_MORE_PREF, infoMoreIsOpen ? '1' : '0'); } catch (x) {}
    if (infoMoreIsOpen) return infoMoreLoad(st, false);
    infoMoreRender();
    return null;
  }
  if (a === 'reload') {
    // (the addresses above read the same inventory: drawn again too)
    infoNetCache.delete(st.key);
    var p = infoMoreLoad(st, true);
    if (typeof infoNetRow === 'function') p.then(function () { if (infoMore === st) infoNetLoad(st.dev, infoNetRow); });
    return p;
  }
  if (a === 'copy') return infoMoreCopy(st.copies[i]);
  if (a === 'support') return infoMoreSupport(st.links[i]);
  return null;
}
async function infoMoreCopy(v) {
  if (typeof v !== 'string' || !v) return false;
  var ok = await reachNativeClipboardWrite(v);
  reachNotify(T(ok ? 'term.copied' : 'term.clipboardFailed'), ok ? 'ok' : 'warn');
  return ok;
}
// The maker's support page, in the default browser (/local/open-support:
// the app checks the vendor and the serial and builds the address). A page
// that does not take the serial: it is copied first, to paste there.
async function infoMoreSupport(l) {
  if (!l || typeof l !== 'object') return false;
  if (!l.inUrl) {
    var cp = await reachNativeClipboardWrite(l.serial);
    if (cp) reachNotify(T('info.more.supportPaste'), 'ok');
  }
  try {
    var r = await fetch('/local/open-support', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ vendor: l.vendor, serial: l.serial }) });
    if (r && r.ok) return true;
  } catch (e) {}
  reachNotify(T('info.more.supportFailed'), 'warn');
  return false;
}
var INFO_MORE_RELOAD_SVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 11-2.12-9.36L23 10"/></svg>';
var INFO_MORE_LINK_SVG = '<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>';
function infoMoreRender() {
  var st = infoMore;
  if (!st || !st.el) return;
  var he = infoHwCache.get(st.key) || null, rec = infoRecCache.get(st.key) || null;
  var chev = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg>';
  var h = '<div class="im-head"><button type="button" class="im-toggle" id="im-toggle" data-im="toggle" aria-expanded="' + (infoMoreIsOpen ? 'true' : 'false') + '">' +
    chev + '<span>' + TH('info.more.title') + '</span></button>';
  var copies = [], links = [];
  if (infoMoreIsOpen) {
    var busy = !!(he && he.p);
    h += '<button type="button" class="im-reload" id="im-reload" data-im="reload" title="' + TH('info.more.reload') + '" aria-label="' + TH('info.more.reload') + '"' + (busy ? ' disabled' : '') + '>' + INFO_MORE_RELOAD_SVG + '</button></div><div class="im-body" id="im-body">';
    var secs = infoMoreFacts(st.dev, he, rec);
    secs.forEach(function (s) {
      h += '<div class="im-sect">' + TH(s.key) + '</div>';
      s.rows.forEach(function (r) {
        var lbl = r.label ? TH(r.label) : i18nEsc(r.text);
        var val = r.html;
        if (r.copy) { copies.push(r.copy); val = '<span class="im-v im-copy" role="button" tabindex="0" data-im="copy" data-im-i="' + (copies.length - 1) + '" title="' + TH('info.more.copyTip') + '">' + r.html + '</span>'; }
        else val = '<span class="im-v">' + r.html + '</span>';
        h += '<div class="im-row"><span class="im-k" title="' + lbl + '">' + lbl + '</span>' + val + '</div>';
        if (r.link) {
          links.push(r.link);
          h += '<div class="im-row"><span class="im-k"></span><button type="button" class="im-link" id="im-support" data-im="support" data-im-i="' + (links.length - 1) + '">' + TH('info.more.support') + INFO_MORE_LINK_SVG + '</button></div>';
        }
      });
    });
    var stt = he ? he.status : 'loading';
    if (busy || stt === 'loading') h += '<div class="im-note" id="im-note">' + TH('info.more.loading') + '</div>';
    else if (stt === 'denied') h += '<div class="im-note" id="im-note">' + TH('info.more.denied') + '</div>';
    else if (stt === 'none') h += '<div class="im-note" id="im-note">' + TH('info.more.none') + '</div>';
    else if (stt === 'error') h += '<div class="im-note" id="im-note">' + TH('info.more.error') + '</div>';
    else if (stt === 'ok' && he.data) {
      var sa = infoHwDate(he.data.scannedAt);
      if (sa) h += '<div class="im-note" id="im-note">' + TH('info.more.scannedAt', { date: fmtDateTime(sa.getTime()) }) + '</div>';
    }
    h += '</div>';
  } else h += '</div>';
  st.copies = copies; st.links = links;
  if (h === st.last) return;
  st.last = h;
  st.el.className = 'info-more' + (infoMoreIsOpen ? '' : ' closed');
  st.el.innerHTML = h;
}
`
