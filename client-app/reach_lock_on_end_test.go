package main

// reach_lock_on_end_test.go — "lock the session when the connection ends"
// on the Desktop side (reach_viewer.go reachOnLockState & co., the padlock
// next to the annotation button, Settings > Sessions). The protocol is shared
// with the Obliance web viewer and the agent (agent/lock_on_end.go): the
// names below are a contract. The page's own functions are extracted from the
// built page and run in node when it is installed (like mackeys_test.go).

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// lockEndJSFunc returns the source of the page's function name (braces matched).
func lockEndJSFunc(t *testing.T, src, name string) string {
	t.Helper()
	loc := regexp.MustCompile(`\nfunction ` + regexp.QuoteMeta(name) + `\(`).FindStringIndex(src)
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

func TestLockOnEndPageWiring(t *testing.T) {
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"}), "\r\n", "\n")
	for _, want := range []string{
		"LOCK_ON_END: 'lock_on_end',",
		"case 'session_lock_state': reachOnLockState(tab, msg); return;",
		"tab.lockState = null;",
		"reachRefreshLockOnEnd(tab);\n  reachRefreshResolution(tab);",
		"pb.appendChild(renderLockOnEndSetting());",
		"var REACH_LOCK_OFFER_KEY = 'oblireach_lock_on_end';",
		".reach-lockend-btn.on{color:var(--danger);",
	} {
		if c := strings.Count(html, want); c != 1 {
			t.Errorf("page has %q %d times, want 1", want, c)
		}
	}
	// The padlock sits right after the annotation button (the toolbar of the
	// main window, its tabs and the detached windows is this same one).
	anno := strings.Index(html, "toolbar.appendChild(annoBtn);")
	pad := strings.Index(html, "reachBuildLockOnEndTool(toolbar);")
	qa := strings.Index(html, "const qaBtn = document.createElement('button');")
	if anno < 0 || pad < anno || qa < pad {
		t.Fatalf("the padlock is not right after the annotation button (anno=%d padlock=%d quick actions=%d)", anno, pad, qa)
	}
}

func TestLockOnEndProtocolJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	html := strings.ReplaceAll(buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"}), "\r\n", "\n")
	var b strings.Builder
	b.WriteString("var REACH_LOCK_OFFER_KEY = 'oblireach_lock_on_end';\n")
	for _, f := range []string{"reachLockOfferEnabled", "reachSetLockOfferEnabled", "reachLockOfferApply", "reachCanLockOnEnd",
		"reachOnLockState", "reachToggleLockOnEnd", "reachRefreshLockOnEnd", "reachBuildLockOnEndTool"} {
		b.WriteString(lockEndJSFunc(t, html, f) + "\n")
	}
	script := `
var store = new Map(), sent = [], shown = null, sessionTabs = [];
var localStorage = { getItem: function (k) { return store.has(k) ? store.get(k) : null; }, setItem: function (k, v) { store.set(k, String(v)); } };
function reachPref(k, d) { var v = localStorage.getItem(k); return v === null ? d : v; }
function reachSetPref(k, v) { localStorage.setItem(k, v); }
var REACH = { FEATURE: { LOCK_ON_END: 'lock_on_end' } };
var REACH_ICON = { lock: 'LOCK', unlock: 'UNLOCK' };
function T(k) { return k; }
function reachHas(tab, f) { return tab.features.has(f); }
function reachSend(tab, o) { if (!tab.ws) return false; sent.push({ tab: tab.id, msg: o }); return true; }
function reachUI(tab) { return tab === shown; }
function reachDisplayedTab() { return shown; }
function mkBtn() {
  var b = { style: {}, attrs: {}, classes: new Set(), title: '', innerHTML: '', className: '', id: '', handlers: {},
    setAttribute: function (k, v) { this.attrs[k] = v; },
    addEventListener: function (e, f) { this.handlers[e] = f; } };
  b.classList = { toggle: function (c, on) { if (on) b.classes.add(c); else b.classes.delete(c); } };
  return b;
}
var built = [];
var document = { createElement: function () { return mkBtn(); }, getElementById: function (id) { return built.find(function (x) { return x.id === id; }) || null; } };
` + b.String() + `
var bad = [];
function check(c, m) { if (!c) bad.push(m); }
function mkTab(id, features) { return { id: id, ws: {}, gotInit: true, features: new Set(features), lockState: null }; }

// The padlock: built hidden, next to the other tools.
var toolbar = { appendChild: function (x) { built.push(x); } };
reachBuildLockOnEndTool(toolbar);
var btn = document.getElementById('reach-lockend-btn');
check(btn && btn.style.display === 'none', 'built hidden');

// 1. The session was locked at connect: red, a click asks for "leave unlocked".
var a = mkTab(1, ['lock_on_end']); sessionTabs.push(a); shown = a;
reachOnLockState(a, { type: 'session_lock_state', lockedAtConnect: true, lockOnEnd: true });
check(a.lockState.lockedAtConnect === true && a.lockState.lockOnEnd === true, 'state kept');
check(btn.style.display === '' && btn.classes.has('on') && btn.innerHTML === 'LOCK' && btn.title === 'viewer.lockOnEnd.titleOn', 'red padlock: ' + JSON.stringify(btn));
check(sent.length === 0, 'nothing sent while the setting is on');
btn.handlers.click();
check(sent.length === 1 && sent[0].msg.type === 'set_lock_on_end' && sent[0].msg.enabled === false, 'click red -> enabled:false ' + JSON.stringify(sent));
check(a.lockState.lockOnEnd === true && btn.classes.has('on'), 'the agent is the source of truth: unchanged until it answers');
reachOnLockState(a, { type: 'session_lock_state', lockedAtConnect: true, lockOnEnd: false });
check(!btn.classes.has('on') && btn.innerHTML === 'UNLOCK' && btn.title === 'viewer.lockOnEnd.titleOff' && btn.attrs['aria-pressed'] === 'false', 'neutral padlock');
sent = []; btn.handlers.click();
check(sent.length === 1 && sent[0].msg.enabled === true, 'click neutral -> enabled:true');

// 2. Not locked at connect: neutral by default.
var c = mkTab(3, ['lock_on_end']); shown = c;
reachOnLockState(c, { type: 'session_lock_state', lockedAtConnect: false, lockOnEnd: false });
check(btn.style.display === '' && !btn.classes.has('on'), 'unlocked at connect: neutral');

// 3. An older agent (no feature): no padlock, its message ignored, nothing sent.
var old = mkTab(2, []); shown = old; sent = [];
reachOnLockState(old, { type: 'session_lock_state', lockedAtConnect: true, lockOnEnd: true });
reachRefreshLockOnEnd(old);
check(old.lockState === null && btn.style.display === 'none', 'old agent: hidden');
check(!reachToggleLockOnEnd(old) && sent.length === 0, 'old agent: nothing sent');
// A malformed state is ignored.
var m = mkTab(4, ['lock_on_end']);
reachOnLockState(m, { type: 'session_lock_state', lockedAtConnect: true });
check(m.lockState === null, 'malformed ignored');
// Not streaming yet: hidden, no click.
var w = mkTab(5, ['lock_on_end']); w.gotInit = false; w.lockState = { lockedAtConnect: true, lockOnEnd: true }; shown = w;
reachRefreshLockOnEnd(w);
check(btn.style.display === 'none' && !reachToggleLockOnEnd(w), 'not streaming: hidden');

// 4. Setting off: every session that would lock is told not to, the padlock hides.
shown = a; sent = [];
reachOnLockState(a, { type: 'session_lock_state', lockedAtConnect: true, lockOnEnd: true });
reachSetLockOfferEnabled(false);
check(store.get('oblireach_lock_on_end') === '0', 'setting stored');
check(sent.length === 1 && sent[0].tab === 1 && sent[0].msg.enabled === false, 'setting off -> set_lock_on_end false at once: ' + JSON.stringify(sent));
check(btn.style.display === 'none', 'setting off: hidden');
check(!reachToggleLockOnEnd(a), 'setting off: no toggle');
// The agent answers false: never asked twice (no loop).
sent = [];
reachOnLockState(a, { type: 'session_lock_state', lockedAtConnect: true, lockOnEnd: false });
check(sent.length === 0, 'no loop on the answer');
// A new session (or a session switch) says true again: told at once.
var d = mkTab(6, ['lock_on_end']); sessionTabs.push(d);
reachOnLockState(d, { type: 'session_lock_state', lockedAtConnect: true, lockOnEnd: true });
check(sent.length === 1 && sent[0].tab === 6 && sent[0].msg.enabled === false, 'setting off: new state answered with false');
// Setting on again: the padlock is back, nothing sent.
sent = []; reachSetLockOfferEnabled(true);
check(sent.length === 0 && btn.style.display === '' && store.get('oblireach_lock_on_end') === '1', 'setting on again');
check(reachLockOfferEnabled() && (store.delete('oblireach_lock_on_end'), reachLockOfferEnabled()), 'on by default');

if (bad.length) { console.log(JSON.stringify(bad)); process.exit(1); }
`
	cmd := exec.Command(node, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock on end: %v %s", err, out)
	}
}
