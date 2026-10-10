package main

// vms_picker_test.go — the "+" list of a session window with each Hyper-V
// host's VMs under it (vms_page.go dpVm*, picker_multi.go, agent_tools.go
// devPickerLaunch): the VMs page's own lists read while the list is open,
// the search on VM names, a VM's row (greyed with why), its box and its
// service. The page's own functions, extracted from the built page and run
// in node when it is installed (like global_search_test.go).

import (
	"os/exec"
	"strings"
	"testing"
)

func TestVmsPickerJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"}), "\r\n", "\n")
	var b strings.Builder
	for _, v := range []string{"dpVmIdx", "dpVmDevs", "dpVmQOnly", "dpVmQ", "dpSel"} {
		b.WriteString(jsVar(t, html, v) + "\n")
	}
	for _, f := range []string{"vmsOn", "vmsPickerOn", "vmsDataOn", "dpVmPrepare", "dpVmEntriesOf", "dpVmNameHas", "dpVmQMore", "dpVmListed",
		"dpVmDev", "dpVmListedDevs", "dpVmDot", "dpVmRowParts", "dpVmRows", "dpVmLaunch", "dpServices", "dpKindName", "devPickerLaunch"} {
		b.WriteString(jsFuncBody(t, html, f) + "\n")
	}
	script := `
var reachDetachedMode = true, pickerOpen = false, entries = [], opened = [], notes = [], tools = [];
var DEV_TREE_MAX_DEPTH = 4, DEV_TREE_INDENT = 16, VM_ICON = '<svg>vm</svg>';
function devPickerIsOpen() { return pickerOpen; }
function vmsAll() { return entries; }
function devKey(d) { return 'A:' + d.id + (d._vm ? '~vm-' + d._vm.id : ''); }
function devIsVm(d) { return !!(d && d._vm); }
function devIsLocal(d) { return false; }
function vmDevice(host, vm) { var d = Object.assign({}, host); d._vm = { id: vm.id, name: vm.name }; d._host = host; return d; }
function vmsHostWhy(h) { return h.off ? 'host offline' : ''; }
function vmCanOpen(vm) { return vm.state === 'running' || vm.state === 'paused'; }
function vmsWhy(e) { return vmsHostWhy(e.host) || (vmCanOpen(e.vm) ? '' : 'not running'); }
function vmsOpenable(e) { return !vmsWhy(e); }
function vmsHostName(h) { return h.hostname; }
function vmsStateChip(vm) { return '<chip ' + vm.state + '>'; }
function vmsDash() { return '-'; }
function vmOpen(host, vm, where, opts) { opened.push([host.id, vm.id, where]); return Promise.resolve('here'); }
function toolsSay(opts, text) { opts.notify(text); }
function devPickerNote(text) { if (text) notes.push(text); }
function devPickerTool(run) { return run(); }
function dpCheckHtml(state, off, group, tip) { return '<box ' + state + (off ? ' off' : '') + ' tip="' + tip + '">'; }
function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }
function T(k, p) { return k + (p ? JSON.stringify(p) : ''); }
function TH(k, p) { return esc(T(k, p)); }
function toolsIsKind(k) { return ['cmd', 'powershell', 'ssh', 'rdp', 'files'].indexOf(k) >= 0; }
function toolNameKey(k) { return 'tools.' + k; }
function devReachOnline(d) { return !!d.reach; }
function devShellKinds(d) { return ['cmd', 'powershell']; }
function toolState(d, k) { return { enabled: false }; }
function reachWinAddDevice(d) { opened.push(['reach', d.id]); return Promise.resolve('here'); }
` + b.String() + `
var bad = [];
function check(c, m) { if (!c) bad.push(m); }
// ── The data: read in a session window only while its "+" list is open ──
check(vmsDataOn() === false, 'a session window, its list closed: no VM data');
pickerOpen = true;
check(vmsDataOn() === true && vmsOn() === false, 'its list open: the data, never the page');
// ── The rows ──
var hv1 = { id: 41, hostname: 'HV1' }, hv2 = { id: 43, hostname: 'HV2', off: true }, pc = { id: 42, hostname: 'PC1' };
function E(host, id, name, state) { return { key: 'A:' + host.id + '~vm-' + id, pid: 'A', host: host, vm: { id: id, name: name, state: state, guestOs: '' } }; }
entries = [E(hv1, 'a', 'DC01', 'running'), E(hv1, 'b', 'ARCHIVE', 'off'), E(hv1, 'c', 'BUILD', 'paused'), E(hv2, 'd', 'WEB01', 'running')];
dpVmPrepare({ q: '' });
check(dpVmListed(hv1).map(function (e) { return e.vm.name; }).join() === 'DC01,ARCHIVE,BUILD', 'a host: its VMs');
check(dpVmListed(pc).length === 0, 'a machine without VMs: none');
var rows = dpVmRows({ dev: hv1, depth: 1 }, { select: true });
check(rows.length === 3 && rows[0].key === 'v:A:41~vm-a', 'one row per VM, keyed by its console: ' + rows.map(function (r) { return r.key; }));
var p = dpVmRowParts({ dev: hv1, depth: 1 }, entries[0], { select: true });
check(p.cls === 'dp-vm-row' && p.html.indexOf('<span class="dot online">') >= 0 && p.html.indexOf('<span class="vm-row-ico"><svg>vm</svg></span>DC01') >= 0, 'a running VM: its dot, its icon, its name: ' + p.html);
check(p.html.indexOf('<span class="tag-chip vm">search.kindVm</span>') >= 0 && p.html.indexOf('<chip running>') >= 0, '... the VM badge, its state');
check(p.html.indexOf('padding-inline-start:32px') >= 0, '... one level under its host');
check(p.html.indexOf('data-act="vm">') >= 0 && p.html.indexOf('<box none tip=') >= 0, '... its console button, its box');
p = dpVmRowParts({ dev: hv1, depth: 1 }, entries[1], { select: true });
check(p.cls === 'dp-vm-row dev-offline' && p.title === 'not running' && p.html.indexOf('data-act="vm" disabled') >= 0 && p.html.indexOf('<box none off tip="not running">') >= 0, 'an off VM: greyed, why: ' + p.title);
p = dpVmRowParts({ dev: hv2, depth: 1 }, entries[3], { select: false });
check(p.title === 'host offline' && p.html.indexOf('dot offline') >= 0 && p.html.indexOf('<box') < 0, 'its host offline: greyed, why; no box without a selection');
check(dpVmRowParts({ dev: hv1, depth: 1 }, entries[2], {}).html.indexOf('dot warn') >= 0, 'paused: amber');
// ── The search: a host for its own fields (every VM), or for a VM's name (that one) ──
dpVmPrepare({ q: 'DC0' });
check(dpVmQMore(hv1, 'dc0') === true && dpVmQMore(pc, 'dc0') === false, 'a VM\'s name lists its host');
check(dpVmListed(hv1).map(function (e) { return e.vm.name; }).join() === 'DC01', '... with that VM only');
dpVmPrepare({ q: 'hv1' });
check(dpVmListed(hv1).length === 3, 'the host matched by itself: every VM of it');
// ── The selection's service, the openings ──
dpVmPrepare({ q: '' });
var devs = dpVmListedDevs([hv1, hv2, pc]);
check(devs.map(function (d) { return dpServices(d).join(); }).join('|') === 'vm||vm|', 'a VM\'s service: its console when it opens: ' + devs.map(function (d) { return dpServices(d).join(); }).join('|'));
check(dpVmDev(entries[0]) === devs[0] && devs[0]._vmEntry === entries[0], 'one device per VM for a drawing');
check(dpKindName('vm') === 'picker.multi.vmConsole', 'named');
devPickerLaunch(devs[0], 'vm');
devPickerLaunch(devs[1]);
devPickerLaunch(pc);
setTimeout(function () {
  check(JSON.stringify(opened) === JSON.stringify([[41, 'a', 'here'], ['reach', 42]]), 'a VM: its console here; a device: as before: ' + JSON.stringify(opened));
  check(JSON.stringify(notes) === JSON.stringify(['ARCHIVE: not running']), 'a VM that cannot open: why, nothing opened: ' + JSON.stringify(notes));
  if (bad.length) { console.error(bad.join('\n')); process.exit(1); }
}, 0);
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("vms picker: %v %s", err, out)
	}
}

// The wiring: the "+" list's view draws the VMs, its search reaches them, its
// opening reads them, its boxes tick them, its clicks open them.
func TestVmsPickerWiring(t *testing.T) {
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin", Language: "en"}), "\r\n", "\n")
	for _, want := range []string{
		"prepare: dpVmPrepare, children: dpVmRows,",
		"onTree: (tree) => { dpPrune(tree.devs.concat(dpVmListedDevs(tree.devs))); dpFooter(); } };",
		"filter: () => Object.assign({}, devPickerFilter, { qMore: dpVmQMore }),",
		"!(typeof f.qMore === 'function' && f.qMore(d, q))) return false;",
		"if (view.prepare) view.prepare(f);",
		"if (dev && n === 0 && view.children) (view.children(row, view) || []).forEach(c => items.push(c));",
		"if (typeof vmsKick === 'function') vmsKick(); // (the Hyper-V hosts' VMs, read while it is open",
		"if (vmsPickerOn() && typeof devPickerRefresh === 'function') devPickerRefresh();",
		"function vmsScopePids() {\n  if (!vmsDataOn()) return [];",
		"if (dev && typeof devIsVm === 'function' && devIsVm(dev)) return devPickerTool(function () { return dpVmLaunch(dev, note); });",
		".dt .tag-chip.vm{",
	} {
		if c := strings.Count(html, want); c != 1 {
			t.Errorf("page holds %q %d times, want 1", want, c)
		}
	}
}
