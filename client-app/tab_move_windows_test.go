//go:build windows

package main

import (
	"testing"
)

// "Move to window" (tab_move.go): the session windows a tab may go to
// (reachWindowTargets), from the main window and from a session window
// (never the caller's own, never one closing), each by a stable id, its
// title and its number of tabs.
func TestWindowTargets(t *testing.T) {
	b, _, m, _, _ := testMainBridge(t)
	m.titleHook = func(uintptr, string) {}
	w1, d1, _ := testSessionWindow(t, m, 0x100, "41")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:41", "A:42"}, Title: "WIN-ONE"})
	w2, d2, _ := testSessionWindow(t, m, 0x200, "50")
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"A:50"}, Title: "WIN-TWO"})

	if _, err := b.windowTargets("nope"); err != errNotAllowed {
		t.Fatalf("bad token: %v", err)
	}
	got, err := b.windowTargets(b.token)
	if err != nil || len(got) != 2 {
		t.Fatalf("main window: %+v %v", got, err)
	}
	byTitle := map[string]windowTarget{}
	for _, g := range got {
		byTitle[g.Title] = g
	}
	if byTitle["WIN-ONE"].Tabs != 2 || byTitle["WIN-TWO"].Tabs != 1 || byTitle["WIN-ONE"].ID != w1.id || byTitle["WIN-TWO"].ID != w2.id || w1.id == w2.id || w1.id <= 0 || w2.id <= 0 {
		t.Fatalf("targets %+v (ids %d %d)", got, w1.id, w2.id)
	}
	if got[0].ID > got[1].ID {
		t.Fatalf("not by id: %+v", got)
	}
	// From a session window: the others only; ids stay what they were.
	id1, id2 := w1.id, w2.id
	if got, _ := d1.windowTargets(d1.token); len(got) != 1 || got[0].ID != id2 || got[0].Title != "WIN-TWO" {
		t.Fatalf("from window 1: %+v", got)
	}
	if got, _ := d2.windowTargets(d2.token); len(got) != 1 || got[0].ID != id1 {
		t.Fatalf("from window 2: %+v", got)
	}
	// A window closing (or gone) is never offered, nor found by its id.
	w2.closing = true
	if got, _ := d1.windowTargets(d1.token); len(got) != 0 || m.windowByID(id2) != nil {
		t.Fatalf("a closing window offered: %+v", got)
	}
	w2.closing = false
	// No title yet: the default one.
	w2.title = ""
	if got, _ := d1.windowTargets(d1.token); len(got) != 1 || got[0].Title != tr("win.sessionTitle") {
		t.Fatalf("untitled: %+v", got)
	}
	if m.windowByID(0) != nil || m.windowByID(-1) != nil || m.windowByID(999) != nil {
		t.Fatal("a window for an unknown id")
	}
}

// From a session window: reachTearOff { window } moves the tab into that
// window whatever is under the cursor, even the window's last tab; never
// into itself or a window gone; { newWindow } opens a new window at the
// cursor even over another window, never for the last tab; one destination
// at most.
func TestTearOffToWindow(t *testing.T) {
	_, host, m, focused, _ := testMainBridge(t)
	w1, d1, _ := testSessionWindow(t, m, 0x100, "41")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:41", "A:42"}})
	w2, d2, h2 := testSessionWindow(t, m, 0x200, "50")
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"A:50"}})
	m.windows() // (ids given)
	m.cursorHook = func() winPoint { return winPoint{700, 300} }
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return nil, false } // (nothing under the cursor)

	for _, o := range []detachOpts{
		{Profile: "A", Window: -1},
		{Profile: "A", Window: w2.id, Main: true},
		{Profile: "A", Window: w2.id, Merge: true},
		{Profile: "A", Window: w2.id, Into: "A:50"},
		{Profile: "A", Window: w2.id, NewWindow: true},
		{Profile: "A", NewWindow: true, Main: true},
		{Profile: "A", NewWindow: true, Merge: true},
	} {
		if _, err := d1.tearOff(d1.token, "42", "x", o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	if _, err := d1.tearOff(d1.token, "42", "x", detachOpts{Profile: "A", Window: w1.id}); err == nil {
		t.Fatal("moved into its own window")
	}
	if _, err := d1.tearOff(d1.token, "42", "x", detachOpts{Profile: "A", Window: 999}); err == nil {
		t.Fatal("moved into no window")
	}
	r, err := d1.tearOff(d1.token, "42", "WIN-42", detachOpts{Profile: "A", Session: intPtr(2), ViewOnly: true, Window: w2.id})
	if err != nil || r != "moved" || len(host.dispatched) != 0 {
		t.Fatalf("into window 2: %q %v", r, err)
	}
	if w1.hasTab("A:42") || !w2.hasTab("A:42") || m.byDevice["A:42"] != w2 {
		t.Fatalf("not moved: w1=%v w2=%v", w1.tabs, w2.tabs)
	}
	if !evalHas(h2, "__reachAdoptTab", `"deviceId":"42"`) || !evalHas(h2, "__reachAdoptTab", `"session":2`) || !evalHas(h2, "__reachAdoptTab", `"viewOnly":true`) {
		t.Fatalf("adopt: %v", h2.evals)
	}
	if (*focused)[len(*focused)-1] != 0x200 {
		t.Fatalf("window 2 not focused: %v", *focused)
	}
	// "New window" for the last tab: refused (it is a window of its own).
	if _, err := d1.tearOff(d1.token, "41", "x", detachOpts{Profile: "A", NewWindow: true}); err == nil {
		t.Fatal("the last tab to a new window")
	}
	// ... but it may join another window (this one then closes, its page knows).
	if r, err := d1.tearOff(d1.token, "41", "x", detachOpts{Profile: "A", Window: w2.id}); err != nil || r != "moved" || !w2.hasTab("A:41") || len(w1.tabs) != 0 {
		t.Fatalf("the last tab into window 2: %q %v (w1 %v, w2 %v)", r, err, w1.tabs, w2.tabs)
	}

	// "New window" over another window: a new window anyway, at the cursor.
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"A:50", "A:42", "A:41"}})
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return w1, false }
	m.placeHook = func(*detachedWindow, winPoint) (winPoint, winRect, int32) {
		return winPoint{1280, 820}, winRect{0, 0, 1920, 1040}, 96
	}
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: 0x300}, mgr: m, deviceID: r.deviceID}, nil
	}
	if r, err := d2.tearOff(d2.token, "41", "x", detachOpts{Profile: "A", NewWindow: true}); err != nil || r != "opening" {
		t.Fatalf("new window: %q %v", r, err)
	}
	for _, f := range host.dispatched {
		f()
	}
	if len(created) != 1 || created[0].key != "A:41" || created[0].rect == nil || w1.hasTab("A:41") || w2.hasTab("A:41") {
		t.Fatalf("created %+v", created)
	}
	if rc := *created[0].rect; rc.Left != 700-tearGrabX || rc.Right-rc.Left != 1280 {
		t.Fatalf("new window at %+v", rc)
	}
}

// From the main window: reachOpenWindow { window } hands its session (or
// its explorer, on its folder) to that session window; never a terminal
// (its shell is not parked), never a window gone.
func TestOpenIntoWindow(t *testing.T) {
	b, host, m, focused, _ := testMainBridge(t)
	w1, d1, h1 := testSessionWindow(t, m, 0x100, "41")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:41"}})
	m.windows()
	b.windowTabs(b.token, windowTabsInfo{IDs: []string{"A:42", "A:43~files", "A:44~cmd"}})

	if _, err := b.openWindow(b.token, "42", "x", detachOpts{Profile: "A", Window: 999}); err == nil {
		t.Fatal("into no window")
	}
	if _, err := b.openWindow(b.token, "44", "x", detachOpts{Profile: "A", Kind: "cmd", Window: w1.id}); err == nil {
		t.Fatal("a terminal of the main window moved without its shell")
	}
	host.evals = nil
	r, err := b.openWindow(b.token, "42", "WIN-42", detachOpts{Profile: "A", Session: intPtr(1), Window: w1.id})
	if err != nil || r != "moved" || len(host.dispatched) != 0 {
		t.Fatalf("session into window 1: %q %v", r, err)
	}
	if m.byDevice["A:42"] != w1 || !w1.hasTab("A:42") || m.mainTabs["A:42"] || m.opening["A:42"] {
		t.Fatalf("not moved: %v main=%v", w1.tabs, m.mainTabs)
	}
	if !evalHas(h1, "__reachAdoptTab", `"deviceId":"42"`) || !evalHas(h1, "__reachAdoptTab", `"session":1`) || !evalHas(host, "__reachOnDetached", `"A:42"`) {
		t.Fatalf("adopt %v / notify %v", h1.evals, host.evals)
	}
	if (*focused)[len(*focused)-1] != 0x100 {
		t.Fatalf("focus: %v", *focused)
	}
	r, err = b.openWindow(b.token, "43", "WIN-43 · Files", detachOpts{Profile: "A", Kind: "files", Path: `D:\Data`, Window: w1.id})
	if err != nil || r != "moved" || m.byDevice["A:43~files"] != w1 || !evalHas(h1, "__reachAdoptTab", `"path":"D:\\Data"`) || !evalHas(h1, "__reachAdoptTab", `"kind":"files"`) {
		t.Fatalf("explorer into window 1: %q %v %v", r, err, h1.evals)
	}
	// Already in a window: brought forward there (never a second tab).
	if r, _ := b.openWindow(b.token, "42", "x", detachOpts{Profile: "A", Window: w1.id}); r != "focused" {
		t.Fatalf("again: %q", r)
	}
}

func intPtr(n int) *int { return &n }
