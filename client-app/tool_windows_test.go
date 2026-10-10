//go:build windows

package main

import (
	"strings"
	"testing"
)

// Tool tabs (terminal_viewer.go) are keyed "<device key>~<kind>": a kind of
// toolKinds, once, after a valid device key.
func TestToolKeys(t *testing.T) {
	if toolKey("p", "7", "cmd") != "p:7~cmd" || toolKey("", "7", "files") != "7~files" || toolKey("p", "7", "") != "p:7" {
		t.Fatal("toolKey")
	}
	for k, want := range map[string]bool{
		"p:7~cmd": true, "7~files": true, "A:u-1~powershell": true, "a1b2c3d4e5f60718:12~ssh": true,
		"p:7": true, "7": true, // (the device's ObliReach key, unchanged)
		"p:7~bogus": false, "p:7~": false, "p:7~cmd~ssh": false, "~cmd": false, "p:~cmd": false, ":7~cmd": false,
		"p:7~CMD": false, "p:7~cmd ": false, "p:../7~cmd": false, "p~cmd:7": false, "~": false,
	} {
		if validDevKey(k) != want {
			t.Errorf("validDevKey(%q) = %v", k, !want)
		}
	}
	if keyProfile("p:7~cmd") != "p" || keyProfile("7~cmd") != "" {
		t.Fatal("keyProfile of a tool key")
	}
	if got := cleanTabIDs([]string{"A:7", "A:7~cmd", "A:7~cmd", "A:7~nope", "A:7~files"}); strings.Join(got, ",") != "A:7,A:7~cmd,A:7~files" {
		t.Fatalf("cleanTabIDs: %v", got)
	}
	// The option: a known kind, or none.
	for _, k := range []string{"cmd", "powershell", "ssh", "files", ""} {
		if _, err := (detachOpts{Kind: k}).check(); err != nil {
			t.Fatalf("kind %q refused: %v", k, err)
		}
	}
	for _, k := range []string{"x", "CMD", "reach", "cmd~ssh", "../cmd"} {
		if _, err := (detachOpts{Kind: k}).check(); err == nil || err.Error() != tr("detached.err.invalidKind") {
			t.Fatalf("kind %q accepted (%v)", k, err)
		}
	}
}

// A device's ObliReach window and its CMD window coexist (two keys); asking
// for each brings its own forward; closing its server closes both.
func TestOpenWindowToolKind(t *testing.T) {
	b, host, m, focused, closed := testMainBridge(t)
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: uintptr(0x500 + len(created))}, mgr: m, deviceID: r.deviceID}, nil
	}
	run := func() {
		q := host.dispatched
		host.dispatched = nil
		for _, f := range q {
			f()
		}
	}
	if _, err := b.openWindow(b.token, "7", "PC-7 · CMD", detachOpts{Profile: "p", Kind: "bogus"}); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if len(host.dispatched) != 0 {
		t.Fatal("a refused kind scheduled a window")
	}
	if r, _ := b.openWindow(b.token, "7", "PC-7", detachOpts{Profile: "p"}); r != "opening" {
		t.Fatal(r)
	}
	if r, _ := b.openWindow(b.token, "7", "PC-7 · CMD", detachOpts{Profile: "p", Kind: "cmd"}); r != "opening" {
		t.Fatalf("CMD window while the ObliReach one opens: %q", r)
	}
	if r, _ := b.openWindow(b.token, "7", "PC-7 · CMD", detachOpts{Profile: "p", Kind: "cmd"}); r != "opening" || len(host.dispatched) != 2 {
		t.Fatalf("double click on CMD: %q, %d dispatched", r, len(host.dispatched))
	}
	run()
	if len(created) != 2 || created[0].key != "p:7" || created[0].kind != "" || created[1].key != "p:7~cmd" || created[1].kind != "cmd" ||
		created[1].deviceID != "7" || created[1].profile != "p" {
		t.Fatalf("created: %+v", created)
	}
	wr, wc := m.byDevice["p:7"], m.byDevice["p:7~cmd"]
	if wr == nil || wc == nil || wr == wc || strings.Join(m.ids(), ",") != "p:7,p:7~cmd" || !evalHas(host, "__reachOnDetached", `["p:7","p:7~cmd"]`) {
		t.Fatalf("two windows: %v", m.byDevice)
	}
	// Each key reaches its own window.
	if r, _ := b.openWindow(b.token, "7", "PC-7 · CMD", detachOpts{Profile: "p", Kind: "cmd"}); r != "focused" || (*focused)[len(*focused)-1] != wc.hwnd {
		t.Fatalf("CMD again: %q", r)
	}
	if ok, _ := b.focusWindow(b.token, "p:7"); !ok || (*focused)[len(*focused)-1] != wr.hwnd {
		t.Fatal("focus the ObliReach window")
	}
	if ok, _ := b.focusWindow(b.token, "p:7~powershell"); ok {
		t.Fatal("a PowerShell window that does not exist was focused")
	}
	// The tool window's URL names its tool (checked by the page: reachParseDetached).
	if u := m.proxy.detachedEntryURL(created[1].deviceID, created[1].profile, created[1].session, created[1].viewOnly, created[1].kind); !strings.Contains(u, "m=cmd") {
		t.Fatalf("entry URL of the CMD window: %s", u)
	}
	// A "+" list of the ObliReach window asks for the CMD tab: the CMD window comes forward.
	_, d, _ := testSessionWindow(t, m, wr.hwnd, "7")
	d.detached = wr
	if r, _ := d.openTab(d.token, "p:7~cmd"); r != "focused" || (*focused)[len(*focused)-1] != wc.hwnd {
		t.Fatalf("openTab of the CMD key: %q", r)
	}
	// ... and a PowerShell tab is claimed for that window.
	if r, _ := d.openTab(d.token, "p:7~powershell"); r != "here" || m.byDevice["p:7~powershell"] != wr {
		t.Fatalf("openTab of a free tool key: %q", r)
	}
	if _, err := d.openTab(d.token, "p:7~bogus"); err == nil {
		t.Fatal("openTab of an invalid tool key")
	}
	// The main window's own tool tabs are reachable too.
	b.windowTabs(b.token, windowTabsInfo{IDs: []string{"p:9~ssh"}})
	if r, _ := d.openTab(d.token, "p:9~ssh"); r != "main" || !evalHas(host, "__reachShowTab", `"p:9~ssh"`) {
		t.Fatalf("a tool tab of the main window: %q", r)
	}
	// The server goes: both windows (and the claimed tab) close.
	n := len(*closed)
	if c, _ := b.closeProfileWindows(b.token, "p"); c != 3 || len(*closed) != n+2 {
		t.Fatalf("closeProfileWindows: %d, closed %v", c, (*closed)[n:])
	}
}

// A terminal tab moves only with its park token (term_park.go: its shell
// goes on in the other window); the explorer moves with its folder. Both
// keep their tool key; a terminal without a park token is refused.
func TestTearOffToolKind(t *testing.T) {
	_, host, m, _, _ := testMainBridge(t)
	w1, d1, _ := testSessionWindow(t, m, 0x100, "12")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:12~cmd", "A:12~files", "A:13"}})
	if strings.Join(w1.tabs, ",") != "A:12,A:12~cmd,A:12~files,A:13" {
		t.Fatalf("tabs: %v", w1.tabs)
	}
	m.cursorHook = func() winPoint { return winPoint{500, 500} }
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return nil, false }
	for _, k := range []string{"cmd", "powershell", "ssh"} {
		if _, err := d1.tearOff(d1.token, "12", "PC-12 · CMD", detachOpts{Profile: "A", Kind: k}); err == nil || err.Error() != tr("detached.err.notMovable") {
			t.Fatalf("tear-off of a %s tab without a park token: %v", k, err)
		}
	}
	park := strings.Repeat("ab", 32)
	for _, o := range []detachOpts{
		{Profile: "A", Kind: "cmd", Park: "xyz"},                       // (not a park token)
		{Profile: "A", Kind: "files", Park: park},                      // (the explorer has none)
		{Profile: "A", Park: park},                                     // (an ObliReach tab neither)
		{Profile: "A", Kind: "cmd", Park: park, Path: "C:\\"},          // (a folder: the explorer's only)
		{Profile: "A", Kind: "files", Path: "C:\\x\x07"},               // (control characters)
		{Profile: "A", Kind: "files", Path: strings.Repeat("a", 4097)}, // (too long)
	} {
		if _, err := d1.tearOff(d1.token, "12", "x", o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	if _, err := m.tearOffFrom(w1, detachReq{deviceID: "12", profile: "A", kind: "cmd"}); err == nil {
		t.Fatal("the manager moved a terminal without its park token")
	}
	if len(host.dispatched) != 0 || !w1.hasTab("A:12~cmd") || m.byDevice["A:12~cmd"] != w1 {
		t.Fatal("a refused tear-off changed something")
	}
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: uintptr(0x700 + len(created))}, mgr: m, deviceID: r.deviceID}, nil
	}
	run := func() {
		q := host.dispatched
		host.dispatched = nil
		for _, f := range q {
			f()
		}
	}
	// A parked terminal: a new window at the drop point, on its park token.
	if r, err := d1.tearOff(d1.token, "12", "PC-12 · CMD", detachOpts{Profile: "A", Kind: "cmd", Park: park}); err != nil || r != "opening" {
		t.Fatalf("a parked terminal: %q %v", r, err)
	}
	if w1.hasTab("A:12~cmd") || !m.opening["A:12~cmd"] {
		t.Fatal("the terminal did not leave its window")
	}
	run()
	if len(created) != 1 || created[0].kind != "cmd" || created[0].park != park || created[0].key != "A:12~cmd" || created[0].rect == nil {
		t.Fatalf("created: %+v", created)
	}
	u := m.proxy.detachedToolEntryURL(created[0].deviceID, created[0].profile, created[0].session, created[0].viewOnly, created[0].kind, created[0].park, created[0].path)
	if !strings.Contains(u, "m=cmd") || !strings.Contains(u, "pk="+park) {
		t.Fatalf("entry URL: %s", u)
	}
	// The explorer, dropped on the main window's title bar: adopted there on its folder.
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return nil, true }
	if r, err := d1.tearOff(d1.token, "12", "PC-12 · Files", detachOpts{Profile: "A", Kind: "files", Path: `C:\Users\x`}); err != nil || r != "moved" {
		t.Fatalf("the explorer: %q %v", r, err)
	}
	if !evalHas(host, "__reachAdoptTab", `"kind":"files"`) || !evalHas(host, "__reachAdoptTab", `"path":"C:\\Users\\x"`) || !m.mainTabs["A:12~files"] {
		t.Fatal("the explorer was not handed to the main window with its folder")
	}
	// Its ObliReach tab still moves.
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return nil, false }
	if r, err := d1.tearOff(d1.token, "13", "PC-13", detachOpts{Profile: "A"}); err != nil || r != "opening" {
		t.Fatalf("an ObliReach tab: %q %v", r, err)
	}
}
