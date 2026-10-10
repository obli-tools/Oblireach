//go:build windows

package engine

import (
	"encoding/json"
	"testing"
)

// TestPlanTypingAscii checks the split between real key events and Unicode
// fallback on the machine's current layout. ASCII letters/digits are always
// keys on any Latin layout; Tab/Enter parts become their key events.
func TestPlanTypingAscii(t *testing.T) {
	msg, code := parseTypeText(ttFrame("a1", false, []map[string]interface{}{
		b64part("aA1"), keyPart("Tab"), keyPart("Enter"),
	}))
	if code != "" {
		t.Fatalf("parse: %s", code)
	}
	defer msg.wipe()
	plan := planTyping(&msg)
	// a, A, 1  → 3 events (each a key); Tab, Enter → 2 key events. Total 5.
	if len(plan) != 5 {
		t.Fatalf("expected 5 events, got %d: %+v", len(plan), plan)
	}
	for i, e := range plan[:3] {
		if e.kind != typeEvKey {
			t.Fatalf("ascii event %d not a key: %+v", i, e)
		}
	}
	// 'A' needs Shift on a Latin layout.
	if plan[1].mods&1 == 0 {
		t.Fatalf("'A' should carry Shift: %+v", plan[1])
	}
	// Tab / Enter.
	if plan[3].vk != 0x09 || plan[4].vk != 0x0D {
		t.Fatalf("Tab/Enter vks wrong: %+v", plan[3:])
	}
}

// TestPlanTypingSurrogate: a non-BMP rune (emoji) becomes two Unicode events.
func TestPlanTypingSurrogate(t *testing.T) {
	msg, code := parseTypeText(ttFrame("a1", false, []map[string]interface{}{b64part("\U0001F600")}))
	if code != "" {
		t.Fatalf("parse: %s", code)
	}
	defer msg.wipe()
	plan := planTyping(&msg)
	if len(plan) != 2 || plan[0].kind != typeEvUnicode || plan[1].kind != typeEvUnicode {
		t.Fatalf("emoji should be two Unicode events: %+v", plan)
	}
	if plan[0].unit < 0xD800 || plan[0].unit > 0xDBFF || plan[1].unit < 0xDC00 || plan[1].unit > 0xDFFF {
		t.Fatalf("surrogate pair units wrong: %+v", plan)
	}
}

// TestFeaturesAdvertiseTypeText: the Windows helper advertises type_text and
// input_desktop.
func TestFeaturesAdvertiseTypeText(t *testing.T) {
	f := agentFeatures()
	var hasT, hasD bool
	for _, x := range f {
		if x == featTypeText {
			hasT = true
		}
		if x == featInputDesktop {
			hasD = true
		}
	}
	if !hasT || !hasD {
		t.Fatalf("helper features missing type_text/input_desktop: %v", f)
	}
	// The direct path never advertises them.
	for _, x := range directStreamFeatures() {
		if x == featTypeText || x == featInputDesktop {
			t.Fatalf("direct path must not advertise %s", x)
		}
	}
}

// TestHandleTypeTextRejectsStaleGen proves type_text is bound to the view
// generation: a type_text frame aimed at a view/leg the
// agent has since moved on from is refused with session_changed and never
// typed, exactly like an ordinary key/mouse event aimed at a stale gen is
// dropped by dispatchInputJSON.
func TestHandleTypeTextRejectsStaleGen(t *testing.T) {
	setCurrentView(reachView{}, 7)
	defer clearCurrentView()

	var replies [][]byte
	reply := func(b []byte) { replies = append(replies, b) }

	// Wrong gen: refused, never typed (SecureOnly=false so a bug would type
	// into the current foreground app — the test would need a live desktop to
	// observe that directly, so it asserts the refusal contract instead).
	handleTypeText(ttFrameGen("a1", false, 6, []map[string]interface{}{b64part("s3cret")}), reply)
	if len(replies) != 1 {
		t.Fatalf("expected exactly one reply, got %d", len(replies))
	}
	var res map[string]interface{}
	if err := json.Unmarshal(replies[0], &res); err != nil {
		t.Fatal(err)
	}
	if res["ok"] != false || res["code"] != "session_changed" {
		t.Fatalf("stale gen should be refused as session_changed: %v", res)
	}

	// No view at all (a session switch in progress): same refusal.
	clearCurrentView()
	replies = nil
	handleTypeText(ttFrameGen("a2", false, 7, []map[string]interface{}{b64part("s3cret")}), reply)
	if len(replies) != 1 {
		t.Fatal("expected exactly one reply")
	}
	json.Unmarshal(replies[0], &res)
	if res["ok"] != false || res["code"] != "session_changed" {
		t.Fatalf("no current view should be refused as session_changed: %v", res)
	}
}

// TestInputDesktopMsg builds a well-formed frame.
func TestInputDesktopMsg(t *testing.T) {
	var m map[string]interface{}
	if err := json.Unmarshal(inputDesktopMsg(true), &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "input_desktop" || m["secure"] != true {
		t.Fatalf("bad frame: %v", m)
	}
}

// The helper advertises type_text_sas (it honours autoSas), with type_text.
func TestFeaturesAdvertiseTypeTextSAS(t *testing.T) {
	has := false
	for _, x := range agentFeatures() {
		if x == featTypeTextSAS {
			has = true
		}
	}
	if !has {
		t.Fatalf("agentFeatures lacks %s: %v", featTypeTextSAS, agentFeatures())
	}
}

// The real probes answer within their ranges on this machine (no panic, no
// content read): init.domain one of the three values, the lock state and the
// caret tri-state.
func TestWindowsTypePrepProbes(t *testing.T) {
	if d := domainJoinState(); d != "" && d != "joined" && d != "workgroup" {
		t.Fatalf("domainJoinState = %q", d)
	}
	s := probeTypeScreen()
	if s.Locked < -1 || s.Locked > 1 || s.CredField < -1 || s.CredField > 1 {
		t.Fatalf("probeTypeScreen out of range: %+v", s)
	}
	d := winTypePrepDeps()
	if d.probe == nil || d.sendSAS == nil || d.sleep == nil || d.now == nil {
		t.Fatal("winTypePrepDeps incomplete")
	}
}
