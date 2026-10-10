//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A new language reaches the page of every window of the app (main window,
// session windows): __reachOnLang with the code, behind the origin check, on
// the window's UI thread. A bridge without the capability is skipped.
func TestBroadcastLang(t *testing.T) {
	r := newBridgeRegistry(func() uintptr { return 0 })
	h1, h2 := &fakeHost{}, &fakeHost{}
	b1, _ := newNativeBridge(h1, "http://127.0.0.1:4321")
	b2, _ := newNativeBridge(h2, "http://127.0.0.1:4321")
	r.register(0x10, b1)
	r.register(0x20, b2)
	r.register(0x30, fakeKeyOnly{})
	r.broadcastLang("fr")
	for i, h := range []*fakeHost{h1, h2} {
		if len(h.dispatched) != 1 {
			t.Fatalf("window %d: %d calls dispatched, want 1 (on its UI thread)", i, len(h.dispatched))
		}
		h.dispatched[0]()
		if len(h.evals) != 1 || !strings.Contains(h.evals[0], `window["__reachOnLang"]`) || !strings.Contains(h.evals[0], `f("fr")`) ||
			!strings.Contains(h.evals[0], `location.origin !== "http://127.0.0.1:4321"`) {
			t.Fatalf("window %d: %v", i, h.evals)
		}
	}
	// A window already gone: nothing evaluated.
	b2.shutdown()
	h2.evals, h2.dispatched = nil, nil
	r.broadcastLang("de")
	for _, f := range h2.dispatched {
		f()
	}
	if len(h2.evals) != 0 {
		t.Fatalf("closed window told: %v", h2.evals)
	}
}

type fakeKeyOnly struct{}

func (fakeKeyOnly) keyCaptureOn() bool   { return false }
func (fakeKeyOnly) emitKey(bool, string) {}

// The Windows display language is read (a code the app knows, or "" when
// Windows does not say or speaks a language the app has not).
func TestWindowsUILanguage(t *testing.T) {
	l := windowsUILanguage()
	if l != "" && !strings.Contains(l, "-") && len(l) > 3 {
		t.Errorf("unexpected locale name %q", l)
	}
	t.Logf("Windows display language: %q -> %q", l, normalizeLang(l))
}

// A message the page keeps on screen (the update banner) carries its key:
// the page says it again in a new language. Nested fragments (the reason of
// a signature refusal, "another publisher") stay keys too.
func TestI18nMsg(t *testing.T) {
	m := i18nM("update.err.signature", "error", trustError{0x80096010})
	j, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(j) != `{"key":"update.err.signature","params":{"error":{"key":"update.trust.modified"}}}` {
		t.Errorf("JSON: %s", j)
	}
	en, fr := m.text("en"), m.text("fr")
	if en != trLang("en", "update.err.signature", "error", trLang("en", "update.trust.modified")) ||
		fr != trLang("fr", "update.err.signature", "error", trLang("fr", "update.trust.modified")) || en == fr {
		t.Errorf("text: %q / %q", en, fr)
	}
	// Numbers stay numbers (plurals), errors become their message.
	m = i18nM("update.err.cutShort", "got", int64(5), "total", 9, "size", uint32(3))
	if m.Params["got"] != int64(5) || m.Params["total"] != 9 || m.Params["size"] != uint32(3) {
		t.Errorf("numbers: %#v", m.Params)
	}
	m = i18nM("update.err.folder", "error", errors.New("disk full"))
	if m.Params["error"] != "disk full" || !strings.Contains(m.text("fr"), "disk full") {
		t.Errorf("error param: %#v %q", m.Params, m.text("fr"))
	}
	// tr with a fragment says it in the same language.
	if got := trLang("fr", "update.err.signerMismatch", "signer", i18nM("update.err.anotherPublisher")); !strings.Contains(got, trLang("fr", "update.err.anotherPublisher")) {
		t.Errorf("fragment: %q", got)
	}
	// uerr and the update outcome carry their key.
	ue := uerr("signature", "update.err.signature", "error", trustError{0x800B0100})
	if ue.Text == nil || ue.Text.Key != "update.err.signature" || ue.Message != ue.Text.text(i18nCurrent()) {
		t.Errorf("uerr: %#v", ue)
	}
	r := updateOutcome(pendingUpdate{Version: "1.2.3"}, "1.0.0", "OBR_RC=1603", `C:\l.log`)
	if r.Text == nil || r.Text.Key != "update.result.failed" || r.Text.Params["code"] != 1603 || r.Message != r.Text.text(i18nCurrent()) {
		t.Errorf("outcome: %#v", r)
	}
	if m := (*i18nMsg)(nil); m.text("fr") != "" {
		t.Error("nil message")
	}
}
