package main

// global_search_test.go — the top bar's search (global_search.go): its
// matching and ranking, what it searches (the lists' own machines, never
// more), a row's buttons and what they open, the Ctrl+K shortcut. The page's
// own functions, extracted from the built page and run in node when it is
// installed (like vm_paste_test.go).

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGlobalSearchJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"}), "\r\n", "\n")
	var b strings.Builder
	for _, v := range []string{"GS_MAX", "GS_LOCAL_TTL_MS", "gsState", "GS_KIND_ORDER", "GS_LOCKED_ITEM", "LOCAL_PID", "CXN_KIND_FOLDER"} {
		b.WriteString(jsVar(t, html, v) + "\n")
	}
	for _, f := range []string{"gsLower", "gsQuery", "gsWordAt", "gsMatch", "gsKindRank", "gsCompare", "gsRank", "gsDeviceCands", "gsVmCands",
		"gsLocalCands", "gsLocalOn", "gsLocalOnly", "gsLocalState", "gsAvailable", "gsCollect", "gsSearch", "gsRows", "gsHl", "gsLocalWhere",
		"gsActs", "gsActsHtml", "gsLockRowHtml", "gsRowHtml", "gsLocalKick", "gsUnlock", "gsRunDefault", "gsRunAct", "gsActsOn", "gsFinish", "gsClose", "gsEl", "gsShortcut",
		// (the local connections' own: localconn_tools.go)
		"devIsLocal", "cxnDevice", "cxnIsSsh", "cxnTypeName", "cxnToolState", "cxnToolIcon"} {
		b.WriteString(jsFuncBody(t, html, f) + "\n")
	}
	script := `
var reachDetachedMode = false, serverMode = 'single', activeLocal = false, launch = false;
var overview = { groups: [] }, vmEntries = [], opened = [], focused = 0;
var srv = new Map();
var serverProfiles = [], localConns = { state: '', nodes: [], loading: false }, localGroups = [], loads = 0, redraws = 0, unlocks = 0;
var document = { getElementById: function () { return null; }, querySelector: function () { return null; } };
function activeServerIsLocal() { return activeLocal; }
function devIsVm(d) { return !!(d && d._vm); }
function devIsSupport(d) { return !!(d && d._support); }
function devKey(d) { return (d._pid || 'p') + ':' + d.id; }
function devUsable(d) { return devIsLocal(d) ? d._reach !== false : !!d.on; }
function devDotClass(d) { return 'dotx'; }
function localDevGroups() { return localGroups; }
function localConnsLoad() { loads++; localConns.loading = true; return Promise.resolve().then(function () { localConns.loading = false; localConns.state = 'ok'; }); }
function overviewRedraw() { redraws++; }
function cxnUnlockThen(fn) { unlocks++; fn(); }
function vmsAll() { return vmEntries; }
function vmsOpenable(e) { return e.vm.state === 'running'; }
function vmsWhy(e) { return e.vm.state === 'running' ? '' : 'not running'; }
function i18nEsc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }
function T(k, p) { return k + (p ? JSON.stringify(p) : ''); }
function TH(k, p) { return i18nEsc(T(k, p)); }
function reachDetachOn() { return launch; }
var DEV_ROW_ICONS = { remote: '<svg>play</svg>' }, TOOLS_ICON = { terminal: '<svg>t</svg>', folder: '<svg>f</svg>', monitor: '<svg>m</svg>', lock: '<svg>lock</svg>' }, VM_ICON = '<svg>vm</svg>';
function devShellKinds(d) { var os = d.os || d.osType; return os === 'windows' ? ['cmd', 'powershell'] : os === 'linux' ? ['ssh'] : []; }
function toolsShellUnknown(d) { return !!(d._tools && !devShellKinds(d).length); }
function toolsIsKind(k) { return ['cmd', 'powershell', 'ssh', 'rdp', 'files'].indexOf(k) >= 0; }
function toolState(d, k) {
  if (devIsLocal(d)) return toolsIsKind(k) ? cxnToolState(d, k) : { show: false, enabled: false, lock: 'none', reason: '' }; // (agent_tools.go)
  var s = (d.tools || {})[k]; return s || { show: false, enabled: false, lock: 'none', reason: '' };
}
function toolLabelKey(k) { return 'tools.' + k; }
function toolBadge(st) { return st.lock === 'locked' ? '<lock>' : ''; }
function toolTitle(k, st) { return st.enabled ? 'open ' + k : st.reason; }
function devicesOpen(dev, act) { opened.push(['dev', dev.id, act]); }
function vmsOpenOne(e) { opened.push(['vm', e.key]); }
function gsFocus() { focused++; return true; }
` + b.String() + `
var bad = [];
function check(c, m) { if (!c) bad.push(m); }
function tier(q, name, alts) { var m = gsMatch(q, name, alts); return m ? m.tier : null; }
// ── Matching ──
check(tier('web', 'WEB') === 0, 'the name itself, any case');
check(tier('web', 'web01') === 1, 'prefix');
var m = gsMatch('web', 'srv-web01'); check(m.tier === 2 && m.at === 4 && m.len === 3, 'a word starts with it: ' + JSON.stringify(m));
m = gsMatch('web', 'myweb'); check(m.tier === 3 && m.at === 2, 'contained: ' + JSON.stringify(m));
m = gsMatch('hv', 'dc01', ['HV-PARIS', '10.0.0.5']); check(m.tier === 4 && m.field === 0 && m.at === 0, 'another field, prefix: ' + JSON.stringify(m));
m = gsMatch('aris', 'dc01', ['HV-PARIS', '10.0.0.5']); check(m.tier === 5 && m.field === 0 && m.at === 4, 'another field, contained: ' + JSON.stringify(m));
m = gsMatch('0.0.5', 'dc01', ['HV-PARIS', '10.0.0.5']); check(m.tier === 4 && m.field === 1 && m.at === 3, 'an address part: ' + JSON.stringify(m));
m = gsMatch('paris dc', 'dc01', ['HV-PARIS']); check(m && m.tier === 6 && m.field === -1 && m.at === 0 && m.len === 2 && gsMatch('paris', 'dc01', ['HV-PARIS']).tier === 4, 'every word somewhere: ' + JSON.stringify(m));
check(gsMatch('paris lyon', 'dc01', ['HV-PARIS']) === null, 'a word found nowhere');
check(gsMatch('  ', 'dc01') === null && gsMatch('', 'dc01') === null, 'no query, no match');
check(gsMatch('zz', 'dc01', []) === null, 'no match');
check(tier('  Web   01 ', 'web 01') === 0, 'spaces folded');
check(tier('ecole', 'pc-ecole') === 2 && tier('cole', 'pc-école') === 3, 'unicode letters are word letters');
// ── Ranking ──
function dev(id, name, on, extra) { return Object.assign({ id: id, hostname: name, on: on }, extra || {}); }
var cands = [dev(1, 'srv-web', true), dev(2, 'web10', true), dev(3, 'web2', true), dev(4, 'web', false), dev(5, 'myweb', true), dev(6, 'web3', false)]
  .map(function (d) { return { kind: 'device', key: 'd:' + d.id, name: d.hostname, alts: [], usable: d.on, dev: d }; });
cands.push({ kind: 'vm', key: 'v:1', name: 'web-vm', alts: ['hv01'], usable: true });
cands.push({ kind: 'vm', key: 'v:2', name: 'web1', alts: ['hv01'], usable: true });
var r = gsRank('web', cands, 12);
var names = r.items.map(function (it) { return it.c.name; }).join(',');
check(names === 'web,web2,web10,web1,web-vm,web3,srv-web,myweb', 'ranked: ' + names);
check(r.total === 8, 'total');
var many = []; for (var i = 0; i < 30; i++) many.push({ kind: 'device', key: 'd:' + i, name: 'pc' + i, alts: [], usable: true });
r = gsRank('pc', many, GS_MAX);
check(GS_MAX === 12 && r.items.length === 12 && r.total === 30 && r.items[0].c.name === 'pc0' && r.items[2].c.name === 'pc2' && r.items[10].c.name === 'pc10', 'at most 12, numbers in order: ' + r.items.map(function (x) { return x.c.name; }).join(','));
// ── What is searched: the overview and the VMs page's list, never more ──
var shown = dev(10, 'alpha', true), twice = dev(11, 'alpha-2', true);
overview = { groups: [
  { id: 1, name: 'G1', devices: [shown, twice, dev(12, 'alpha-vmconsole', true, { _vm: { id: 'x' } }), dev(13, 'alpha-local', true, { _local: true, _pid: LOCAL_PID }), dev(14, 'alpha-code', true, { _support: true })] },
  { id: 2, name: 'G2', devices: [twice, null, { hostname: 'no-id' }] },
] };
srv.set('other', { signedIn: true, groups: [{ id: 9, name: 'Hidden', devices: [dev(99, 'alpha-secret', true)] }] }); // (a server's raw list: never searched)
vmEntries = [{ key: 'p:20~vm-a', pid: 'p', host: dev(20, 'hv-alpha', true), vm: { id: 'a', name: 'alpha-vm', state: 'running', ips: ['10.1.2.3'] } },
  { key: 'p:20~vm-b', pid: 'p', host: dev(20, 'hv-alpha', true), vm: { id: 'b', name: 'beta-vm', state: 'off', ips: [] } }];
var got = gsCollect().map(function (c) { return c.kind + ':' + c.name; }).join(',');
check(got === 'device:alpha,device:alpha-2,vm:alpha-vm,vm:beta-vm', 'the lists only, each device once: ' + got);
check(gsSearch('secret').total === 0, 'a device the lists do not show is never found');
r = gsSearch('hv-alpha');
check(r.items.length === 2 && r.items[0].c.kind === 'vm' && r.items[0].m.field === 0 && r.items[0].c.name === 'alpha-vm', 'VMs by their host, running first: ' + JSON.stringify(r.items.map(function (x) { return x.c.name; })));
check(gsSearch('10.1.2').items.length === 1, 'a VM by its address');
check(gsCollect().filter(function (c) { return c.kind === 'vm' && c.name === 'beta-vm'; })[0].usable === false, 'a VM that does not open: greyed');
reachDetachedMode = true; check(gsCollect().length === 0, 'a session window: nothing');
reachDetachedMode = false; activeLocal = true; check(gsAvailable() && gsCollect().length === 0, 'single mode on a local base: the field, no server device nor VM (no local base read here)');
serverMode = 'multi'; check(gsCollect().length === 4, 'multi-server mode: the overview (the tenant menu\'s choice)');
serverMode = 'single'; activeLocal = false;
// ── A row's buttons, what they open ──
var win = dev(30, 'win-pc', true, { os: 'windows', _tools: true, oblireach: { installed: true }, tools: {
  cmd: { show: true, enabled: true, lock: 'none' }, powershell: { show: true, enabled: false, lock: 'none', reason: 'conpty' }, files: { show: true, enabled: true, lock: 'locked' } } });
var acts = gsActs({ kind: 'device', dev: win });
check(acts.map(function (a) { return a.act; }).join(',') === 'remote,cmd,powershell,files', 'device buttons: ' + acts.map(function (a) { return a.act; }));
check(!acts[0].disabled && acts[0].title === 'common.remote' && acts[2].disabled && acts[2].title === 'conpty' && acts[3].html.indexOf('<lock>') > 0 && acts[1].txt, 'their states and words');
launch = true; check(gsActs({ kind: 'device', dev: win })[0].title === 'devices.openPageHere', '"Detach by default": ▶ opens its page here');
launch = false;
var agentOnly = dev(31, 'agent-only', true, { os: 'linux', _tools: true, oblireach: { installed: false }, tools: { ssh: { show: true, enabled: true, lock: 'none' }, files: { show: false } } });
acts = gsActs({ kind: 'device', dev: agentOnly });
check(acts.map(function (a) { return a.act; }).join(',') === 'remote,ssh' && acts[0].title === 'tools.openPage', 'agent only: SSH, no Files when not shown');
var off = dev(32, 'off', false, { _tools: true, os: '', tools: {} });
acts = gsActs({ kind: 'device', dev: off });
check(acts.map(function (a) { return a.act; }).join(',') === 'remote,shell' && acts[0].disabled && acts[1].disabled && acts[1].title === 'tools.reason.osUnknown', 'offline, unknown OS: greyed, why said');
acts = gsActs({ kind: 'vm', entry: vmEntries[1] });
check(acts.length === 1 && acts[0].act === 'console' && acts[0].disabled && acts[0].title === 'not running', 'a VM: its console, greyed with why');
var it = { c: { kind: 'device', dev: win }, acts: gsActs({ kind: 'device', dev: win }) };
gsRunAct(it, 'files'); gsRunAct(it, 'powershell'); gsRunAct(it, 'nope'); gsRunDefault(it);
var vit = { c: { kind: 'vm', entry: vmEntries[0] }, acts: gsActs({ kind: 'vm', entry: vmEntries[0] }) };
gsRunAct(vit, 'console'); gsRunDefault(vit);
check(JSON.stringify(opened) === JSON.stringify([['dev', 30, 'files'], ['dev', 30, null], ['vm', 'p:20~vm-a'], ['vm', 'p:20~vm-a']]), 'what opens: ' + JSON.stringify(opened));
check(JSON.stringify(gsActsOn(it)) === '[0,1,3]', 'Tab goes over the usable buttons');
// ── Highlight ──
check(gsHl('Web01', 0, 3) === '<mark class="gs-hl">Web</mark>01', 'marked');
check(gsHl('<b>x', 3, 1) === '&lt;b&gt;<mark class="gs-hl">x</mark>', 'escaped');
check(gsHl('İstanbul', 0, 2) === 'İstanbul', 'a length-changing lower case: nothing marked');
check(gsHl('abc', -1, 0) === 'abc', 'nothing to mark');
// ── Ctrl+K ──
function key(o) { return Object.assign({ key: 'k', code: 'KeyK', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, defaultPrevented: false, repeat: false, target: null, preventDefault: function () { this.prevented = true; } }, o); }
var e = key({ ctrlKey: true }); gsShortcut(e); check(focused === 1 && e.prevented, 'Ctrl+K');
e = key({ metaKey: true, key: 'K' }); gsShortcut(e); check(focused === 2, 'Cmd+K');
e = key({ ctrlKey: true, code: 'KeyK', key: 'л' }); gsShortcut(e); check(focused === 3, 'another layout: the K key');
gsShortcut(key({ ctrlKey: true, defaultPrevented: true })); check(focused === 3, 'a picture / terminal took it: left to the machine');
gsShortcut(key({ ctrlKey: true, target: { closest: function (s) { return s.indexOf('canvas') >= 0 ? {} : null; } } })); check(focused === 3, 'from a remote picture: left to it');
gsShortcut(key({ ctrlKey: true, shiftKey: true })); gsShortcut(key({ altKey: true, ctrlKey: true })); gsShortcut(key({})); gsShortcut(key({ ctrlKey: true, code: 'KeyJ', key: 'j' }));
check(focused === 3, 'other keys: nothing');
document.querySelector = function (s) { return s === '.modal-overlay.visible' ? {} : null; };
gsShortcut(key({ ctrlKey: true })); check(focused === 3, 'under a dialog: nothing');
document.querySelector = function () { return null; };
// ── The local base's connections (ui.go localDevGroups: its folders as groups) ──
function node(id, label, kind, host, port, user, parent) { return { id: id, label: label, kind: kind, host: host, port: port, username: user, parentId: parent || '', secret: 's3cr3t-' + id, passphrase: 'pp-' + id, authMethod: 0 }; }
var lWeb = cxnDevice(node('a1', 'alpha-web', CXN_KIND_SSH, 'web.lab.local', 22, 'deploy', 'f1'));
var lRdp = cxnDevice(node('a2', 'Compta RDP', CXN_KIND_RDP, '10.9.8.7', 3389, '', 'f2'));
var lRoot = cxnDevice(node('a3', 'nas', CXN_KIND_SSH, 'nas.home', 2222, 'root', ''));
lRdp._reach = false; // (its host found unreachable: ui.go localDevProbe)
localGroups = [{ id: 'f:f1', _pid: LOCAL_PID, name: 'Prod / Web', devices: [lWeb] }, { id: 'f:f2', _pid: LOCAL_PID, name: 'Imported / WinSCP', devices: [lRdp, lWeb] },
  { id: 'f:', _pid: LOCAL_PID, _flat: true, name: '', devices: [lRoot] }];
activeLocal = false; serverMode = 'single';
function locals() { return gsCollect().filter(function (c) { return c.kind === 'local'; }); }
// No local base: nothing of it, whatever localConns says.
serverProfiles = [{ id: 'p', name: 'Prod' }]; localConns = { state: 'ok', nodes: [], loading: false };
check(locals().length === 0 && gsSearch('alpha').locked === false, 'no local base: none of its connections');
serverProfiles = [{ id: 'p', name: 'Prod' }, { id: 'loc', name: 'Lab', local: true }];
// Unlocked: every connection once, in every scope.
check(locals().map(function (c) { return c.name; }).join(',') === 'alpha-web,Compta RDP,nas', 'unlocked: each connection once: ' + locals().map(function (c) { return c.name; }));
check(JSON.stringify(gsCollect()).indexOf('s3cr3t') < 0 && JSON.stringify(gsCollect()).indexOf('pp-a') < 0, 'never a secret in what is searched');
check(gsSearch('alpha').items.map(function (it) { return it.c.kind + ':' + it.c.name; }).join(',') === 'device:alpha,device:alpha-2,vm:alpha-vm,local:alpha-web,vm:beta-vm', 'single mode on a server: its devices, VMs and the local connections, by tier: ' + gsSearch('alpha').items.map(function (it) { return it.c.kind + ':' + it.c.name; }));
serverMode = 'multi'; check(locals().length === 3, 'multi-server mode, any server / tenant: the local connections too');
serverMode = 'single'; activeLocal = true;
check(gsAvailable() && gsCollect().map(function (c) { return c.kind; }).join(',') === 'local,local,local', 'single mode on the base: the field, its connections only');
activeLocal = false;
// Matching: label, host, folder path, user.
r = gsSearch('web.lab'); check(r.items.length === 1 && r.items[0].c.kind === 'local' && r.items[0].m.tier === 4 && r.items[0].m.field === 0, 'by host: ' + JSON.stringify(r.items.map(function (x) { return x.m; })));
r = gsSearch('winscp'); check(r.items.length === 1 && r.items[0].c.name === 'Compta RDP' && r.items[0].m.field === 1, 'by folder path (imported)');
r = gsSearch('deploy'); check(r.items.length === 1 && r.items[0].c.name === 'alpha-web' && r.items[0].m.field === 2, 'by user');
r = gsSearch('compta'); check(r.items.length === 1 && r.items[0].m.tier === 1 && r.items[0].c.usable === false, 'by label; unreachable: greyed');
check(gsSearch('rdp').items.some(function (it) { return it.c.name === 'Compta RDP'; }) && gsSearch('3389').total === 0, 'the port alone: not matched');
// Ranking: a device first on an equal tier, a better tier first.
r = gsRank('nas', [{ kind: 'local', key: 'l:1', name: 'nas', alts: [], usable: true }, { kind: 'device', key: 'd:1', name: 'nas', alts: [], usable: true }, { kind: 'vm', key: 'v:1', name: 'nas', alts: [], usable: true }, { kind: 'device', key: 'd:2', name: 'my-nas', alts: [], usable: true }], 12);
check(r.items.map(function (it) { return it.c.kind; }).join(',') === 'device,vm,local,device', 'equal tier: device, VM, local; then the next tier: ' + r.items.map(function (it) { return it.c.kind; }));
// Its line: folder path · user@host:port, what matched marked.
var lc = locals();
check(gsLocalWhere(lc[0], gsMatch('web.lab', lc[0].name, lc[0].alts)) === 'Prod / Web · deploy@<mark class="gs-hl">web.lab</mark>.local:22', 'its line: ' + gsLocalWhere(lc[0], gsMatch('web.lab', lc[0].name, lc[0].alts)));
check(gsLocalWhere(lc[1], gsMatch('winscp', lc[1].name, lc[1].alts)) === 'Imported / <mark class="gs-hl">WinSCP</mark> · 10.9.8.7:3389', 'path marked: ' + gsLocalWhere(lc[1], gsMatch('winscp', lc[1].name, lc[1].alts)));
check(gsLocalWhere(lc[2], null) === 'root@nas.home:2222', 'the root: no path');
var row = gsRowHtml({ c: lc[0], m: gsMatch('alpha', lc[0].name, lc[0].alts), acts: gsActs(lc[0]) }, 3);
check(row.indexOf('gs-tile local') > 0 && row.indexOf('<span class="gs-kind local">devices.local.chip</span><span class="gs-kind ssh">SSH</span>') > 0 && row.indexOf('<svg>t</svg>') > 0 && row.indexOf('s3cr3t') < 0, 'its row: Local + SSH, the terminal icon: ' + row);
row = gsRowHtml({ c: lc[1], m: gsMatch('compta', lc[1].name, lc[1].alts), acts: gsActs(lc[1]) }, 4);
check(row.indexOf('gs-row off') > 0 && row.indexOf('gs-kind rdp">RDP') > 0 && row.indexOf('<svg>m</svg>') > 0 && row.indexOf('title="devices.local.unreachable"') > 0, 'an RDP one, unreachable: ' + row);
// Its buttons: its Devices page row's (localRowButtons), what they open (devicesOpen -> localDevOpen).
acts = gsActs(lc[0]);
check(acts.map(function (a) { return a.act; }).join(',') === 'remote,ssh,files' && acts[0].title === 'devices.local.openSsh' && !acts[0].disabled && !acts[1].disabled && !acts[2].disabled, 'SSH: ▶, SSH, Files: ' + JSON.stringify(acts));
acts = gsActs(lc[1]);
check(acts.length === 1 && acts[0].act === 'remote' && acts[0].title === 'devices.local.openRdp' && acts[0].disabled, 'RDP: ▶ alone, greyed when unreachable: ' + JSON.stringify(acts));
opened = [];
var lit = { c: lc[0], acts: gsActs(lc[0]) };
gsRunDefault(lit); gsRunAct(lit, 'files'); gsRunAct(lit, 'ssh'); gsRunAct({ c: lc[1], acts: gsActs(lc[1]) }, 'remote');
check(JSON.stringify(opened) === JSON.stringify([['dev', 'a1', undefined], ['dev', 'a1', 'files'], ['dev', 'a1', 'ssh']]), 'what opens (a greyed ▶: nothing): ' + JSON.stringify(opened));
// Locked: nothing of it, a row whose Unlock is the page's; absent / unreadable: nothing.
localConns = { state: 'locked', nodes: [], loading: false };
r = gsSearch('alpha');
check(locals().length === 0 && r.locked === true && gsRows(r).length === r.items.length + 1 && gsRows(r)[r.items.length].c.kind === 'locked', 'locked: none of its connections, its row last');
check(gsSearch('compta').total === 0 && gsRows(gsSearch('compta')).length === 1, 'locked, nothing else: its row alone');
var lk = gsActs(GS_LOCKED_ITEM.c);
check(lk.length === 1 && lk[0].act === 'unlock' && lk[0].txt, 'its Unlock');
check(gsRowHtml({ c: GS_LOCKED_ITEM.c, m: null, acts: lk }, 7).indexOf('search.localLocked') > 0, 'its row says so');
['absent', 'unreadable', ''].forEach(function (s) {
  localConns = { state: s, nodes: [], loading: false };
  check(locals().length === 0 && gsSearch('alpha').locked === false, s + ': nothing, no row');
});
// Read again as the drop-down opens: never read, or older than the TTL; never while a read runs.
serverProfiles = [{ id: 'p', name: 'Prod' }];
localConns = { state: '', nodes: [], loading: false }; loads = 0;
check(gsLocalKick(1000) === false && loads === 0, 'no local base: no read');
serverProfiles = [{ id: 'p', name: 'Prod' }, { id: 'loc', name: 'Lab', local: true }];
gsState.localAt = 0;
check(gsLocalKick(1000) === true && loads === 1, 'never read: read');
check(gsLocalKick(1001) === false && loads === 1, 'a read running: not again');
localConns.loading = false; localConns.state = 'ok';
check(gsLocalKick(1000 + GS_LOCAL_TTL_MS - 1) === false && gsLocalKick(1000 + GS_LOCAL_TTL_MS + 1) === true && loads === 2, 'older than its TTL: read again');
reachDetachedMode = true; check(gsCollect().length === 0, 'a session window: still nothing'); reachDetachedMode = false;
check(redraws === 0, 'the lists follow the read once it is in');
setTimeout(function () { // (the reads above in first)
  check(redraws === 2, 'each read: the lists redrawn (' + redraws + ')');
  // Unlock: the page's dialog, then a read, the lists redrawn, the field back.
  localConns = { state: 'locked', nodes: [], loading: false };
  loads = 0; redraws = 0; unlocks = 0; var f0 = focused;
  gsRunDefault(GS_LOCKED_ITEM);
  check(unlocks === 1 && loads === 1, 'Unlock: the dialog, then a read');
  setTimeout(function () {
    check(redraws === 1 && focused === f0 + 1 && localConns.state === 'ok', 'Unlock: read, redraw, field: ' + [unlocks, loads, redraws, focused - f0]);
    check(locals().length === 3, 'unlocked: its connections found');
    if (bad.length) { console.log(JSON.stringify(bad, null, 1)); process.exit(1); }
  }, 0);
}, 0);
`
	// (on stdin: the script is longer than a Windows command line takes)
	cmd := exec.Command(node)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("global search: %v %s", err, out)
	}
}

// The field is in the top bar, next to the tenant menu, in the main window
// only; the lists' redraws and the VMs' reads search the open drop-down again.
func TestGlobalSearchWiring(t *testing.T) {
	html := buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin", Language: "en"})
	for _, want := range []string{
		".gs-pop{position:fixed;", // (its CSS: in the page once)
		"function gsMount() {",
		"if (gsState.mounted || typeof document === 'undefined' || reachDetachedMode) return;", // (no top bar in a session window)
		"var anchor = document.getElementById('tenant-switcher');",
		"wrap.setAttribute('data-no-drag', '');", // (the top bar moves the window: never from the field)
		"document.addEventListener('keydown', gsShortcut);", // (bubbling: a picture / terminal cancels its keys first)
		"function vmsKick() {\n  if (typeof gsDataChanged === 'function') gsDataChanged();",
		"vmsNavSync();\n  if (typeof gsDataChanged === 'function') gsDataChanged();",
	} {
		if c := strings.Count(strings.ReplaceAll(html, "\r\n", "\n"), want); c != 1 {
			t.Errorf("page holds %q %d times, want 1", want, c)
		}
	}
	// The field after the tenant menu's markup, which the script finds.
	if !strings.Contains(html, `id="tenant-switcher"`) {
		t.Error("no tenant menu to put the field after")
	}
}
