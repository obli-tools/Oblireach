package main

// vm_paste_test.go — the page side of typing into a Hyper-V VM console
// (vm_console.go): the guest system read from guestOs, the keyboard layout
// and guest system kept per VM, the type_text frame's fields, and Ctrl+V
// typing the local clipboard (modifiers released first, long texts asked,
// empty / too long / busy refused). The page's own functions, extracted from
// the built page and run in node when it is installed (like mackeys_test.go).

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// jsFuncBody returns the source of the page's function name (its whole
// body, braces matched; async functions too).
func jsFuncBody(t *testing.T, src, name string) string {
	t.Helper()
	loc := regexp.MustCompile(`\n(async )?function ` + regexp.QuoteMeta(name) + `\(`).FindStringIndex(src)
	if loc == nil {
		t.Fatalf("function %s not found", name)
	}
	depth := 0
	for j := strings.IndexByte(src[loc[0]:], '{') + loc[0]; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[loc[0] : j+1]
			}
		}
	}
	t.Fatalf("function %s: no end", name)
	return ""
}

// jsVar returns the page's statement "var name = ...;" (to its ; at depth 0).
func jsVar(t *testing.T, src, name string) string {
	t.Helper()
	loc := regexp.MustCompile(`\nvar ` + regexp.QuoteMeta(name) + ` = `).FindStringIndex(src)
	if loc == nil {
		t.Fatalf("var %s not found", name)
	}
	depth := 0
	for j := loc[1]; j < len(src); j++ {
		switch src[j] {
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case ';':
			if depth == 0 {
				return src[loc[0] : j+1]
			}
		}
	}
	t.Fatalf("var %s: no end", name)
	return ""
}

func TestVmPasteAndLayoutJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"}), "\r\n", "\n")
	var b strings.Builder
	for _, v := range []string{"VM_GUID_RE", "VM_LAYOUTS", "VM_LAYOUT_KEY", "VM_LAYOUT_US", "VM_TYPE_TEXT_MAX", "VM_PASTE_CONFIRM_CHARS", "VM_FEATURE", "vmPasting", "REACH_MOD_CODES"} {
		b.WriteString(jsVar(t, html, v) + "\n")
	}
	for _, f := range []string{"vmGuessLayout", "vmLayoutValid", "vmGuestKind", "vmOf", "vmGuestOsOf", "vmPrefKey", "vmPrefGet", "vmPrefSet", "vmGuestGet",
		"vmGuestSet", "vmLayoutChosen", "vmLayoutGet", "vmLayoutSet", "vmIdOk", "vmSendTypeText", "vmPasteClipboard", "vmReleaseModifiers", "devIsVm"} {
		b.WriteString(jsFuncBody(t, html, f) + "\n")
	}
	script := `
var store = new Map(), sent = [], toasts = [], clip = { text: '' }, confirmAnswer = true, confirmAsked = 0;
var localStorage = { getItem: function (k) { return store.has(k) ? store.get(k) : null; }, setItem: function (k, v) { store.set(k, String(v)); } };
var navigator = { language: 'fr-FR' };
function i18nLang() { return 'fr'; }
var vmLists = new Map(), vmsData = new Map();
function T(k, p) { return k + (p ? JSON.stringify(p) : ''); }
function reachHas(tab, f) { return tab.features.has(f); }
function reachSend(tab, o) { sent.push(o); return true; }
function reachKeyMsg(tab, k) { return k; }
function reachToast(t) { toasts.push(t); }
function reachUI() { return true; }
function reachFocusCanvas() {}
function vmRefreshTools() {}
async function reachNativeClipboardRead() { return clip; }
async function vmConfirmPaste() { confirmAsked++; return confirmAnswer; }
` + b.String() + `
var bad = [];
function check(c, m) { if (!c) bad.push(m); }
var G = '0f8fad5b-d9cb-469f-a165-70867728950e';
(async function () {
  check(vmGuestKind('Windows Server 2022 Datacenter') === 'windows', 'windows');
  check(vmGuestKind('Ubuntu 22.04.4 LTS (Jammy Jellyfish)') === 'linux', 'a distribution alone: linux');
  check(vmGuestKind('Debian GNU/Linux 12') === 'linux', 'debian');
  check(vmGuestKind('') === 'unknown' && vmGuestKind(null) === 'unknown' && vmGuestKind('  ') === 'unknown', 'none: unknown');
  var win = { _vm: { id: G, guestOs: 'Windows 11 Pro' } }, lin = { _vm: { id: G.replace('0f', '1f'), guestOs: 'Ubuntu 24.04' } }, unk = { _vm: { id: G.replace('0f', '2f'), guestOs: '' } };
  check(vmGuestGet(win) === 'windows' && vmGuestGet(lin) === 'linux' && vmGuestGet(unk) === 'windows', 'guest defaults');
  check(vmLayoutGet(win) === '0000040C', 'no choice: the app language');
  check(vmLayoutGet(lin) === '00000409', 'a Linux guest without a choice: US');
  store.set('oblireach.vm.layout', '00000407');
  check(vmLayoutGet(win) === '00000407', 'the computer\'s last choice');
  check(vmLayoutGet(lin) === '00000409', 'Linux: US before the computer\'s last choice');
  vmLayoutSet(lin, '0000040C');
  check(vmLayoutGet(lin) === '0000040C' && vmLayoutChosen(lin) && store.get('oblireach.vm.layout.' + lin._vm.id) === '0000040C', 'kept per VM');
  check(vmLayoutGet(unk) === '0000040C', 'the last choice also the computer\'s');
  vmGuestSet(lin, 'windows'); check(vmGuestGet(lin) === 'windows', 'guest kept per VM');
  vmGuestSet(lin, 'macos'); check(vmGuestGet(lin) === 'windows', 'an unknown guest ignored');
  var tab = { id: 1, device: { _vm: { id: G.replace('0f', '3f'), guestOs: 'Debian GNU/Linux 12', name: 'deb' } }, features: new Set(['vm_type_text']), ws: {}, gotInit: true, keysHeld: {} };
  check(vmSendTypeText(tab, 'abc') && sent[0].type === 'type_text' && sent[0].guest === 'linux' && sent[0].layout === '00000409', 'frame from the VM: ' + JSON.stringify(sent[0]));
  sent = []; vmSendTypeText(tab, 'x', '0000040C', 'windows');
  check(sent[0].guest === 'windows' && sent[0].layout === '0000040C', 'frame: explicit');
  tab.vmTyping = null; sent = [];
  tab.keysHeld = { ControlLeft: { key: 'Control', code: 'ControlLeft' }, KeyA: { key: 'a', code: 'KeyA' } };
  clip = { text: 'line1\r\nline2' };
  check(await vmPasteClipboard(tab, true), 'Ctrl+V typed');
  check(sent.length === 2 && sent[0].type === 'key' && sent[0].action === 'up' && sent[0].code === 'ControlLeft' && sent[1].text === 'line1\nline2', 'Ctrl let go first, one Enter per line: ' + JSON.stringify(sent));
  check(!tab.keysHeld.ControlLeft && tab.keysHeld.KeyA, 'only the modifiers let go');
  check(!(await vmPasteClipboard(tab, true)) && /vm.paste.busy/.test(toasts[toasts.length - 1]), 'busy');
  tab.vmTyping = null;
  clip = { text: '' };
  check(!(await vmPasteClipboard(tab, true)) && /vm.paste.empty/.test(toasts[toasts.length - 1]), 'empty');
  clip = { text: 'x'.repeat(20001) };
  check(!(await vmPasteClipboard(tab, true)) && /vm.type.tooLong/.test(toasts[toasts.length - 1]), 'too long');
  clip = { text: 'y'.repeat(501) }; confirmAnswer = false; sent = [];
  check(!(await vmPasteClipboard(tab, true)) && confirmAsked === 1 && !sent.length, 'long: asked, declined');
  confirmAnswer = true;
  check((await vmPasteClipboard(tab, false)) && confirmAsked === 2 && sent.length === 1, 'long: asked, typed');
  tab.vmTyping = null; clip = { text: 'z'.repeat(500) };
  check((await vmPasteClipboard(tab, false)) && confirmAsked === 2, '500 characters: not asked');
  tab.vmTyping = null; tab.features = new Set();
  check(!(await vmPasteClipboard(tab, true)) && /vm.type.unavailable/.test(toasts[toasts.length - 1]), 'an older helper');
  if (bad.length) { console.log(JSON.stringify(bad)); process.exit(1); }
})().catch(function (e) { console.log(String(e && e.stack || e)); process.exit(1); });
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("VM paste / layout: %v %s", err, out)
	}
}

// The paste gesture of a VM console types instead of sending the keys, and
// its Clipboard menu offers typing (no shared clipboard there).
func TestVmPasteWiring(t *testing.T) {
	html := buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"})
	for _, want := range []string{
		"if (pasteKeys && devIsVm(tab.device)) {",
		"if (!e.repeat) vmPasteClipboard(tab, true);",
		"add(T('vm.clip.typeClipboard'), function () { vmPasteClipboard(tab, false); }, !canType);",
		"vaultAsyncCall('reachVaultTypeVm', tab.sessionToken, en.id, !!withUser, false, vmLayoutGet(tab.device), vmGuestGet(tab.device))",
		"children: vdpVmRows,",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
