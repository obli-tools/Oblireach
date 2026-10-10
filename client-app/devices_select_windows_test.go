//go:build windows

package main

import (
	"strings"
	"testing"
)

// The Devices page's batch ("Connect" / "Open as mosaic" on several ticked
// devices, devices_select.go) with "Detach by default": the first device
// opens its session window, the others join it as tabs (reachOpenWindow
// with Into): adopted by its page (__reachAdoptTab), the main page told.
func TestOpenWindowInto(t *testing.T) {
	b, host, m, focused, _ := testMainBridge(t)
	tok := b.token

	// Into a key whose window is still being created: asked again later.
	if r, _ := b.openWindow(tok, "12", "PC-12", detachOpts{Profile: "A"}); r != "opening" {
		t.Fatalf("first device: %q", r)
	}
	if r, err := b.openWindow(tok, "13", "PC-13", detachOpts{Profile: "A", Into: "A:12"}); err != nil || r != "pending" {
		t.Fatalf("into a window being created: %q %v", r, err)
	}
	if m.byDevice["A:13"] != nil || m.opening["A:13"] {
		t.Fatal("a pending join must leave nothing behind")
	}
	// Its window exists now.
	w, _, wh := testSessionWindow(t, m, 0x300, "12")
	delete(m.opening, "A:12")
	m.byDevice["A:12"] = w
	w.tabs = []string{"A:12"}

	host.evals = nil
	sess := 2
	if r, err := b.openWindow(tok, "13", "PC-13", detachOpts{Profile: "A", Session: &sess, Into: "A:12"}); err != nil || r != "moved" {
		t.Fatalf("ObliReach into the window: %q %v", r, err)
	}
	if m.byDevice["A:13"] != w || strings.Join(w.tabs, ",") != "A:12,A:13" {
		t.Fatalf("the tab is that window's: tabs=%v", w.tabs)
	}
	if !evalHas(wh, "__reachAdoptTab", `"deviceId":"13"`) || !evalHas(wh, "__reachAdoptTab", `"session":2`) || !evalHas(wh, "__reachAdoptTab", `"profile":"A"`) {
		t.Fatalf("its page adopts it, on the session given: %v", wh.evals)
	}
	if (*focused)[len(*focused)-1] != 0x300 || !evalHas(host, "__reachOnDetached", `"A:13"`) {
		t.Fatalf("the window comes forward, the main page is told: %v", host.evals)
	}
	// A tool joins the same way (its own key).
	if r, _ := b.openWindow(tok, "14", "PC-14 · SSH", detachOpts{Profile: "A", Kind: "ssh", Into: "A:12"}); r != "moved" || m.byDevice["A:14~ssh"] != w {
		t.Fatalf("a tool into the window: %q", r)
	}
	if !evalHas(wh, "__reachAdoptTab", `"kind":"ssh"`) {
		t.Fatal("the tool adopted as a tool")
	}
	// Already somewhere: brought forward, never a second tab.
	if r, _ := b.openWindow(tok, "13", "PC-13", detachOpts{Profile: "A", Into: "A:12"}); r != "focused" {
		t.Fatalf("already in the window: %q", r)
	}
	// The main window's own session of it: shown there.
	m.mainTabs["A:15"] = true
	if r, _ := b.openWindow(tok, "15", "PC-15", detachOpts{Profile: "A", Into: "A:12"}); r != "main" || m.byDevice["A:15"] != nil {
		t.Fatalf("held by the main window: %q", r)
	}
	// Refused: into itself, an unknown window, a closing one, a bad key.
	if _, err := b.openWindow(tok, "16", "PC-16", detachOpts{Profile: "A", Into: "A:16"}); err == nil {
		t.Fatal("into itself accepted")
	}
	if _, err := b.openWindow(tok, "16", "PC-16", detachOpts{Profile: "A", Into: "A:99"}); err == nil {
		t.Fatal("into a window that does not exist accepted")
	}
	w.closing = true
	if _, err := b.openWindow(tok, "16", "PC-16", detachOpts{Profile: "A", Into: "A:12"}); err == nil {
		t.Fatal("into a closing window accepted")
	}
	w.closing = false
	if _, err := b.openWindow(tok, "16", "PC-16", detachOpts{Profile: "A", Into: "../x"}); err == nil {
		t.Fatal("a bad key accepted")
	}
	if m.byDevice["A:16"] != nil || m.opening["A:16"] {
		t.Fatal("a refused join must leave nothing behind")
	}
}
