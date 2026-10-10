//go:build windows

package main

import (
	"strings"
	"testing"
)

// The explorer of a session window's side panel (files_dock.go), "Open in a
// tab": the app moves it to the main window whatever is under the cursor
// (detachOpts.Main), on its folder, even as the window's last key; Main
// never goes with Merge or Into.
func TestTearOffToMainWindow(t *testing.T) {
	_, host, m, focused, _ := testMainBridge(t)
	w1, d1, _ := testSessionWindow(t, m, 0x100, "41")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:41", "A:41~files"}})
	m.cursorHook = func() winPoint { return winPoint{500, 500} }
	// Under the cursor: another session window, nothing, or this one; Main wins.
	w2, d2, _ := testSessionWindow(t, m, 0x200, "50")
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"A:50"}})
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return w2, false }
	for _, o := range []detachOpts{
		{Profile: "A", Kind: "files", Main: true, Merge: true},
		{Profile: "A", Kind: "files", Main: true, Into: "A:50"},
	} {
		if _, err := d1.tearOff(d1.token, "41", "x", o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	host.evals = nil
	r, err := d1.tearOff(d1.token, "41", "WIN-REACH · Files", detachOpts{Profile: "A", Kind: "files", Path: `C:\Users`, Main: true})
	if err != nil || r != "moved" || len(host.dispatched) != 0 {
		t.Fatalf("to the main window: %q %v", r, err)
	}
	if w1.hasTab("A:41~files") || w2.hasTab("A:41~files") || m.byDevice["A:41~files"] != nil || !m.mainTabs["A:41~files"] {
		t.Fatalf("not handed to the main window: w1=%v w2=%v main=%v", w1.tabs, w2.tabs, m.mainTabs)
	}
	if !evalHas(host, "__reachAdoptTab", `"kind":"files"`) || !evalHas(host, "__reachAdoptTab", `"path":"C:\\Users"`) || !evalHas(host, "__reachAdoptTab", `"profile":"A"`) {
		t.Fatalf("adopt call: %v", host.evals)
	}
	if (*focused)[len(*focused)-1] != 0 {
		t.Fatalf("main window not focused: %v", *focused)
	}
	if !w1.hasTab("A:41") {
		t.Fatal("the session itself moved")
	}
	// The window's last key may go too (its session closed meanwhile).
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:42~files"}})
	if r, err := d1.tearOff(d1.token, "42", "x", detachOpts{Profile: "A", Kind: "files", Main: true}); err != nil || r != "moved" {
		t.Fatalf("the last key: %q %v", r, err)
	}
	// A key this window does not hold: refused.
	if _, err := d1.tearOff(d1.token, "43", "x", detachOpts{Profile: "A", Kind: "files", Main: true}); err == nil {
		t.Fatal("a key of no window moved")
	}
}

// A Files page tab detached (files_page.go filesDetachTab): its window opens
// on the explorer's folder.
func TestOpenWindowFilesPath(t *testing.T) {
	b, host, m, _, _ := testMainBridge(t)
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: uintptr(0x900 + len(created))}, mgr: m, deviceID: r.deviceID}, nil
	}
	if _, err := b.openWindow(b.token, "42", "x", detachOpts{Profile: "A", Kind: "cmd", Path: `C:\`}); err == nil {
		t.Fatal("a folder for a terminal accepted")
	}
	if r, err := b.openWindow(b.token, "42", "WIN-AGENT · Files", detachOpts{Profile: "A", Kind: "files", Path: `D:\Data`}); err != nil || r != "opening" {
		t.Fatalf("open: %q %v", r, err)
	}
	for _, f := range host.dispatched {
		f()
	}
	if len(created) != 1 || created[0].kind != "files" || created[0].path != `D:\Data` || created[0].key != "A:42~files" {
		t.Fatalf("created: %+v", created)
	}
	u := m.proxy.detachedToolEntryURL(created[0].deviceID, created[0].profile, created[0].session, created[0].viewOnly, created[0].kind, created[0].park, created[0].path)
	if !strings.Contains(u, "m=files") || !strings.Contains(u, "fp=") {
		t.Fatalf("entry URL: %s", u)
	}
}
