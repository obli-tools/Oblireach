package main

// explorer_tabs_test.go — the file explorer's sub-tabs (explorer_tabs.go):
// which views are on screen (a plain click, Ctrl+click, "+", closing, the
// mosaic's columns), what a move keeps of them, and how their state is
// split from their tab's. The page's own functions, extracted from the
// built page and run in node when it is installed (like vm_paste_test.go).

import (
	"os/exec"
	"strings"
	"testing"
)

func TestExplorerTabsJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin", Language: "en"}), "\r\n", "\n")
	var b strings.Builder
	for _, v := range []string{"XV_MAX", "XV_MAX_SHOWN", "XV_COL_MIN", "XV_KEYS", "XV_SHARED", "XV_UI_KEYS"} {
		b.WriteString(jsVar(t, html, v) + "\n")
	}
	for _, f := range []string{"explorerCrumbs", "xvStateNew", "xvsSortShown", "xvsAdd", "xvsShowOnly", "xvsToggle", "xvsFocus", "xvsRemove", "xvsCols", "xvsSpan",
		"xvsLabel", "xvsSnapshotOf", "xvsValid", "xvOwnerOf", "xvList", "explorerViews", "xvFocused", "xvView", "xvIsShown", "xvRootDefine", "xvMake"} {
		b.WriteString(jsFuncBody(t, html, f) + "\n")
	}
	script := `
function T(k) { return k === 'explorer.roots' ? 'Drives' : k; }
` + b.String() + `
var bad = [];
function check(c, what) { if (!c) bad.push(what); }
function same(a, b, what) { var x = JSON.stringify(a), y = JSON.stringify(b); if (x !== y) bad.push(what + ': got ' + x + ', want ' + y); }
function st(order, shown, focus) { var x = xvStateNew(); x.order = order.slice(); x.shown = shown.slice(); x.focus = focus; return x; }
function pick(x) { return [x.order, x.shown, x.focus]; }

// ── Which views are on screen ──
var x = xvStateNew();
same(xvsAdd(x, 'a', false), 'alone', 'the first view: alone');
same(pick(x), [['a'], ['a'], 'a'], '... on screen, in focus');
same(xvsAdd(x, 'b', false), 'alone', '"+": a new view');
same(pick(x), [['a', 'b'], ['b'], 'b'], '... alone on screen, in focus (the other one hidden)');
same(xvsToggle(x, 'a'), 'added', 'Ctrl+click on the hidden one: beside');
same(pick(x), [['a', 'b'], ['a', 'b'], 'a'], '... both shown in the strip\'s order, the one clicked in focus');
same(xvsToggle(x, 'zz'), 'none', 'Ctrl+click on an unknown view: nothing');
check(xvsFocus(x, 'b') && x.focus === 'b', 'a press in a pane shown: in focus');
same(xvsAdd(x, 'c', true), 'beside', 'Ctrl+click on "+": the new view beside');
same(pick(x), [['a', 'b', 'c'], ['a', 'b', 'c'], 'c'], '... three shown');
same(xvsAdd(x, 'd', true), 'beside', 'a fourth beside');
same(xvsAdd(x, 'e', true), 'max', 'a fifth with four shown: the mosaic is full');
same(pick(x), [['a', 'b', 'c', 'd', 'e'], ['e'], 'e'], '... alone then');
same(xvsShowOnly(x, 'c'), true, 'a plain click: alone');
same(pick(x).slice(1), [['c'], 'c'], '... only it on screen');
same(xvsToggle(x, 'c'), 'kept', 'Ctrl+click on the only one shown: kept');
check(!xvsFocus(x, 'a'), 'a hidden view never takes the focus');
['a', 'b', 'd'].forEach(function (id) { xvsToggle(x, id); });
same(pick(x).slice(1), [['a', 'b', 'c', 'd'], 'd'], 'four by Ctrl+click, in the strip\'s order');
same(xvsToggle(x, 'e'), 'max', 'Ctrl+click on a fifth: refused');
same(xvsToggle(x, 'd'), 'removed', 'Ctrl+click on one shown: taken out');
same(pick(x).slice(1), [['a', 'b', 'c'], 'c'], '... its neighbour in focus');
x.focus = 'a';
same(xvsToggle(x, 'b'), 'removed', 'taken out while another one is in focus');
same(x.focus, 'a', '... the focus stays');
check(!xvsShowOnly(x, 'nope'), 'a plain click on an unknown view: nothing');

// ── Closing ──
x = st(['a', 'b', 'c', 'd'], ['b', 'c'], 'c');
check(xvsRemove(x, 'c'), 'the view in focus of a mosaic closed');
same(pick(x), [['a', 'b', 'd'], ['b'], 'b'], '... its neighbour in the mosaic in focus');
x = st(['a', 'b', 'c'], ['b'], 'b');
xvsRemove(x, 'b');
same(pick(x), [['a', 'c'], ['c'], 'c'], 'the one alone closed: the next one of the strip alone');
x = st(['a', 'b'], ['b'], 'b');
xvsRemove(x, 'b');
same(pick(x), [['a'], ['a'], 'a'], 'the last of the strip closed: the one before');
x = st(['a', 'b', 'c'], ['a', 'c'], 'a');
xvsRemove(x, 'b');
same(pick(x), [['a', 'c'], ['a', 'c'], 'a'], 'a hidden view closed: the screen unchanged');
check(!xvsRemove(st(['a'], ['a'], 'a'), 'a'), 'the last view never closes');
check(!xvsRemove(st(['a', 'b'], ['a'], 'a'), 'z'), 'an unknown view: nothing closed');

// ── The mosaic's columns ──
same([xvsCols(1, 1600), xvsCols(2, 1600), xvsCols(3, 1600), xvsCols(4, 1600)], [1, 2, 2, 2], 'one: one column; 2 to 4: two');
same([xvsCols(2, 500), xvsCols(4, 679), xvsCols(4, 680), xvsCols(3, 0)], [1, 1, 2, 2], 'narrower than two columns of ' + XV_COL_MIN + ' px: one above the other (unknown width: two)');
same([xvsSpan(3, 2, 0), xvsSpan(3, 2, 1), xvsSpan(3, 1, 0), xvsSpan(4, 2, 0), xvsSpan(2, 2, 0)], [2, 1, 1, 1, 1], 'three on two columns: the first one down both rows');

// ── The sub-tab's name ──
same([xvsLabel('', 'windows'), xvsLabel('C:\\', 'windows'), xvsLabel('C:\\Users\\op', 'windows'), xvsLabel('/', 'unix'), xvsLabel('/var/log', 'unix')],
  ['Drives', 'C:', 'op', '/', 'log'], 'its folder\'s name (the drives, a drive, a folder; / on Unix)');

// ── What a move keeps ──
x = st(['a', 'b', 'c'], ['a', 'c'], 'c');
var paths = { a: 'C:\\', b: 'C:\\Users', c: '' };
var snap = xvsSnapshotOf(x, function (id) { return paths[id]; });
same(snap, { paths: ['C:\\', 'C:\\Users', ''], shown: [0, 2], focus: 2 }, 'a snapshot: the folders, those shown, the one in focus');
check(xvsSnapshotOf(st(['a'], ['a'], 'a'), function () { return 'C:\\'; }) === null, 'one view: nothing more than its folder');
same(xvsValid(snap), snap, 'a snapshot is valid as is');
same(xvsValid({ paths: ['C:\\', '..\\x', 7, 'a\u0001'], shown: [9, 1, 1, -1, 1.5, 'x'], focus: 3 }), { paths: ['C:\\', '', '', ''], shown: [1], focus: 1 },
  'checked: a bad path opens the top; bad indexes dropped; the focus one of those shown');
same(xvsValid({ paths: ['/', '/a', '/b', '/c', '/d', '/e'], shown: [5, 4, 3, 2, 1, 0], focus: 5 }).shown, [2, 3, 4, 5], 'at most ' + XV_MAX_SHOWN + ' shown');
check(xvsValid({ paths: new Array(40).fill('/') }).paths.length === XV_MAX, 'at most ' + XV_MAX + ' views');
same([xvsValid(null), xvsValid({}), xvsValid({ paths: ['C:\\'] }), xvsValid('x')], [null, null, null, null], 'nothing usable: null');

// ── The state: each view its own, the tab's shared, the tab reading the view in focus ──
var dev = { id: 42 }, other = { id: 43 };
var tab = { id: 'tool_1', kind: 'files', device: dev, label: 'WIN', status: 'starting', statusText: null, overlay: null, ovEl: null, lp: { path: 'D:\\' } };
tab.ex = { os: 'windows', split: true, disposed: false, audited: false, privacyGate: null, transfers: [], trSeq: 0, focusSide: 'remote', dockTab: '', ui: { dock: 'DOCK', tr: 'TR' } };
xvRootDefine(tab);
check(xvView(tab) === tab && xvList(tab).length === 0 && tab.ex.path === undefined, 'no view yet: the tab itself, its view fields empty');
var v1 = xvMake(tab); xvsAdd(tab.xv, v1.xvId, false);
var v2 = xvMake(tab); xvsAdd(tab.xv, v2.xvId, false);
same([v1.xvId, v2.xvId, v1.xvSfx, v2.xvSfx], ['xv1', 'xv2', '', '-2'], 'views: their ids (the first keeps the elements\' ids)');
check(xvOwnerOf(v1) === tab && xvOwnerOf(tab) === tab && v1.id === tab.id && v1.lp === tab.lp && v1.label === 'WIN', 'a view: its tab\'s id, label, local pane');
check(xvList(tab)[0] === v1 && explorerViews(tab).length === 2 && explorerViews(v2).length === 2, 'the tab\'s views, in order (from the tab or a view)');
check(xvFocused(tab) === v2 && xvView(tab) === v2 && xvView(v1) === v1, 'the view in focus: the tab\'s functions work on it');
v1.ex.path = 'C:\\'; v2.ex.path = 'C:\\Users';
v1.ex.sel.add('C:\\a.txt');
v2.ex.filter = 'x'; v2.ex.sortKey = 'size';
same([v1.ex.path, v2.ex.path, v1.ex.sel.size, v2.ex.sel.size, v1.ex.filter, v1.ex.sortKey], ['C:\\', 'C:\\Users', 1, 0, '', 'name'], 'folder, selection, filter, sorting: each view its own');
check(v1.ex.sel !== v2.ex.sel && v1.ex.rowEls !== v2.ex.rowEls && v1.ex.items !== v2.ex.items, '... never shared objects');
same([tab.ex.path, tab.ex.filter, tab.ex.sortKey], ['C:\\Users', 'x', 'size'], 'the tab reads the view in focus');
tab.ex.path = 'C:\\Windows';
same([v2.ex.path, v1.ex.path], ['C:\\Windows', 'C:\\'], '... and writes to it (a folder set by a move)');
tab.xv.shown = [v1.xvId, v2.xvId]; xvsFocus(tab.xv, v1.xvId);
same(tab.ex.path, 'C:\\', 'the focus moved: the tab reads the other one');
v2.ex.dockTab = 'ed'; v1.ex.audited = true; v1.ex.transfers.push({ id: 1 }); v2.ex.trSeq++;
same([tab.ex.dockTab, tab.ex.audited, tab.ex.transfers.length, tab.ex.trSeq, v1.ex.trSeq], ['ed', true, 1, 1, 1], 'the dock\'s tab, the audit, the transfers: the tab\'s, shared');
v2.ex.transfers = [];
check(tab.ex.transfers.length === 0 && v1.ex.transfers === tab.ex.transfers, '... replaced from a view: the tab\'s');
check(v1.ex.split === true && v1.ex.os === 'windows' && v1.ex.ui.dock === 'DOCK' && v1.ex.ui.tr === 'TR', 'what the tab built (split view, its dock, its transfers): a view reads it');
v1.ex.ui.list = 'L1'; v2.ex.ui.list = 'L2';
same([tab.ex.ui.list, tab.ex.ui.dock], ['L1', 'DOCK'], 'a view\'s elements: its own; the tab names the view in focus\'s');
check(!Object.prototype.hasOwnProperty.call(tab.ex.ui, 'xvAnything'), 'nothing leaks into the tab\'s elements');
v1.device = other;
check(tab.device === other && v2.device === other, 'the device refreshed by a view (its privacy code asked again): the tab\'s');
v1.status = 'live'; v1.overlay = { kind: 'busy' };
same([tab.status, v2.status, tab.overlay && tab.overlay.kind, v2.overlay], ['starting', 'starting', 'busy', null], 'status and notices: each view its own (the tab reads its overlay)');
v1.ex.disposed = true;
same([tab.ex.disposed, v2.ex.disposed], [false, false], 'a view ended: the others and the tab go on');
check(xvIsShown(v1) && xvIsShown(v2) && xvIsShown(tab), 'on screen: the views shown (a tab without views: itself)');
tab.xv.shown = [v2.xvId]; tab.xv.focus = v2.xvId;
check(!xvIsShown(v1), '... a hidden view: not');
XV_KEYS.forEach(function (k) { check(Object.prototype.hasOwnProperty.call(v1.ex, k), 'a view owns ' + k); });
XV_SHARED.forEach(function (k) { check(XV_KEYS.indexOf(k) < 0, k + ' is either shared or a view\'s, not both'); });

if (bad.length) { console.log(JSON.stringify(bad, null, 1)); process.exit(1); }
`
	// (on stdin: the script is longer than a Windows command line takes)
	cmd := exec.Command(node)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("explorer sub-tabs: %v %s", err, out)
	}
}

// The sub-tabs are the explorer's (appended to its consts, in the page
// once), built with every explorer (the strip above the views, the keys
// for the view they are typed in) and drawn with the tab mosaic's look.
func TestExplorerTabsWiring(t *testing.T) {
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin", Language: "en"}), "\r\n", "\n")
	for _, want := range []string{
		".xv-strip{display:flex;", ".xv-grid.mosaic>.xv-pane.xv-focus{box-shadow:0 0 0 2px var(--accent) inset}",
		"function xvBuild(tab, side) {", "function xvRootDefine(tab) {",
		"  xvBuild(tab, side);\n  explorerWireDrops(tab, side);\n", // (explorerCreate: the strip above the views, the drops for the view under the pointer)
		"  host.addEventListener('keydown', function (e) { explorerHostKey(tab, e); });",
		"el.className = 'tg-subtab xv-tab'", // (the mosaic's sub-tabs: tab_groups.go)
		".exp-host.editing>.exp-bar .exp-path{display:block}",
		"if (kind === 'files' && o.subs) tab.xvInit = o.subs;", // (a tab made with its views: terminal_viewer.go toolCreateTab)
	} {
		if c := strings.Count(html, want); c != 1 {
			t.Errorf("page holds %q %d times, want 1", want, c)
		}
	}
	// Every explorer function that may be given a tab works on its view in focus.
	for _, f := range []string{"explorerOpen", "explorerList", "explorerRefresh", "explorerRender", "explorerRenderList", "explorerPaintSel", "explorerSelItems",
		"explorerDownloadOff", "explorerUpdateBar", "explorerFocus", "explorerDownloadSel", "explorerFilterOf", "explorerPaste", "explorerEditPath"} {
		body := jsFuncBody(t, html, f)
		if !strings.Contains(body, "tab = xvView(tab);") {
			t.Errorf("%s does not work on the tab's view in focus", f)
		}
	}
	// The view's own status, never the tab's toolSetStatus directly.
	for _, f := range []string{"explorerOpen", "explorerFatal", "explorerList"} {
		if body := jsFuncBody(t, html, f); strings.Contains(body, "toolSetStatus(") {
			t.Errorf("%s sets the tab's status itself (explorerSetStatus: the view's)", f)
		}
	}
}
