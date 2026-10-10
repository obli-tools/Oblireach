//go:build windows

package main

import (
	"runtime"
	"strings"
	"testing"
)

// testSessionWindow: a session window of the manager with its own bridge (its
// page's calls land in the returned host's evals).
func testSessionWindow(t *testing.T, m *sessionWindows, hwnd uintptr, first string) (*detachedWindow, *nativeBridge, *fakeHost) {
	h := &fakeHost{}
	nb, err := newNativeBridge(h, "http://127.0.0.1:4321")
	if err != nil {
		t.Fatal(err)
	}
	w := &detachedWindow{webHost: webHost{hwnd: hwnd}, mgr: m, deviceID: first, bridge: nb}
	nb.detached = w
	nb.closeSelf = func() {}
	return w, nb, h
}

func evalHas(h *fakeHost, fn, arg string) bool {
	for _, e := range h.evals {
		if strings.Contains(e, `"`+fn+`"`) && strings.Contains(e, arg) {
			return true
		}
	}
	return false
}

// Session windows with tabs: what each page reports is where each device is;
// asking for a device again (main window, a "+" list) brings its window
// forward on its tab; a "+" list claims a free device for its window; the
// main window's own sessions are reachable too; closing one tab of several
// closes that tab only.
func TestSessionWindowTabs(t *testing.T) {
	b, host, m, focused, closed := testMainBridge(t)
	titles := map[uintptr]string{}
	m.titleHook = func(h uintptr, s string) { titles[h] = s }
	w1, d1, h1 := testSessionWindow(t, m, 0x100, "12")
	w2, d2, h2 := testSessionWindow(t, m, 0x200, "14")

	if _, err := d1.windowTabs("nope", windowTabsInfo{IDs: []string{"12"}}); err != errNotAllowed {
		t.Fatal("reachWindowTabs without token")
	}
	if ok, _ := d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"12", "13", "../x", "12", "<b>"}, Title: "PC-12\x00"}); !ok {
		t.Fatal("reachWindowTabs refused")
	}
	if strings.Join(w1.tabs, ",") != "12,13" || m.byDevice["12"] != w1 || m.byDevice["13"] != w1 || titles[0x100] != "PC-12" {
		t.Fatalf("window 1 report: tabs=%v title=%q", w1.tabs, titles[0x100])
	}
	if !evalHas(host, "__reachOnDetached", `["12","13"]`) {
		t.Fatalf("main page not told: %v", host.evals)
	}
	// A device another window holds stays there (one tab per device).
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"13", "14"}})
	if strings.Join(w2.tabs, ",") != "14" || m.byDevice["13"] != w1 || m.byDevice["14"] != w2 {
		t.Fatalf("window 2 report: tabs=%v 13 in %v", w2.tabs, m.byDevice["13"])
	}
	// A report too long is capped.
	many := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		many = append(many, "d"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if n := len(cleanTabIDs(many)); n != maxWindowTabs {
		t.Fatalf("cleanTabIDs cap: %d", n)
	}

	// The main window asks for 13: window 1 comes forward on that tab.
	if r, _ := b.openWindow(b.token, "13", "PC-13", detachOpts{}); r != "focused" || (*focused)[len(*focused)-1] != 0x100 || !evalHas(h1, "__reachShowTab", `"13"`) {
		t.Fatalf("main asks for 13: %q focused=%v evals=%v", r, *focused, h1.evals)
	}
	if ok, _ := b.focusWindow(b.token, "14"); !ok || (*focused)[len(*focused)-1] != 0x200 || !evalHas(h2, "__reachShowTab", `"14"`) {
		t.Fatal("reachFocusWindow: window 2 on its tab")
	}

	// "+" lists.
	if _, err := d1.openTab("x", "12"); err != errNotAllowed {
		t.Fatal("reachOpenTab without token")
	}
	if _, err := d1.openTab(d1.token, "../1"); err == nil {
		t.Fatal("reachOpenTab: invalid id accepted")
	}
	if r, _ := d1.openTab(d1.token, "12"); r != "self" {
		t.Fatalf("its own tab: %q", r)
	}
	h1.evals = nil
	if r, _ := d2.openTab(d2.token, "12"); r != "focused" || (*focused)[len(*focused)-1] != 0x100 || !evalHas(h1, "__reachShowTab", `"12"`) {
		t.Fatalf("another window's tab: %q", r)
	}
	host.evals = nil
	if r, _ := d2.openTab(d2.token, "20"); r != "here" || m.byDevice["20"] != w2 || strings.Join(w2.tabs, ",") != "14,20" || !evalHas(host, "__reachOnDetached", `"20"`) {
		t.Fatalf("free device: %q tabs=%v", r, w2.tabs)
	}
	if r, _ := d1.openTab(d1.token, "20"); r != "focused" {
		t.Fatalf("claimed device asked from another window: %q", r)
	}
	// The operator cancelled (the page reports its tabs without it): free again.
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"14"}})
	if m.byDevice["20"] != nil || strings.Join(w2.tabs, ",") != "14" {
		t.Fatal("cancelled claim not released")
	}
	// The main window's own sessions.
	if ok, _ := b.windowTabs(b.token, windowTabsInfo{IDs: []string{"30"}}); !ok || !m.mainTabs["30"] {
		t.Fatal("main window report")
	}
	host.evals = nil
	if r, _ := d1.openTab(d1.token, "30"); r != "main" || (*focused)[len(*focused)-1] != 0 || !evalHas(host, "__reachShowTab", `"30"`) || m.byDevice["30"] != nil {
		t.Fatalf("a session of the main window: %q", r)
	}
	m.opening["40"] = true
	if r, _ := d1.openTab(d1.token, "40"); r != "opening" || m.byDevice["40"] != nil {
		t.Fatalf("a window on its way: %q", r)
	}
	// A bridge that is neither (never the case): refused.
	lone, _ := newNativeBridge(&fakeHost{}, "http://127.0.0.1:4321")
	if ok, _ := lone.windowTabs(lone.token, windowTabsInfo{}); ok {
		t.Fatal("report without a window")
	}
	if _, err := lone.openTab(lone.token, "1"); err == nil {
		t.Fatal("openTab outside a session window")
	}
	if _, err := lone.tearOff(lone.token, "1", "x", detachOpts{}); err == nil {
		t.Fatal("tearOff outside a session window")
	}

	// Bring back 13 (window 1 has 12 and 13): only that tab closes.
	h1.evals = nil
	n := len(*closed)
	if ok, _ := b.closeWindow(b.token, "13"); !ok || len(*closed) != n || !evalHas(h1, "__reachCloseTab", `"13"`) || m.byDevice["13"] != nil || strings.Join(w1.tabs, ",") != "12" {
		t.Fatalf("close one tab of two: closed=%v evals=%v tabs=%v", *closed, h1.evals, w1.tabs)
	}
	// Bring back 14 (window 2's only tab): the window closes.
	if ok, _ := b.closeWindow(b.token, "14"); !ok || len(*closed) != n+1 || (*closed)[n] != 0x200 {
		t.Fatalf("close the last tab: %v", *closed)
	}

	// Sign-out: every window once, whatever its number of tabs.
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"12", "13", "15"}})
	n = len(*closed)
	if c, _ := b.closeWindows(b.token); c != 1 || len(*closed) != n+1 || (*closed)[n] != 0x100 {
		t.Fatalf("closeWindows with a 3-tab window: %d %v", c, *closed)
	}

	// A window gone: its devices are free.
	m.forget(w1)
	if m.byDevice["12"] != nil || len(w1.tabs) != 0 {
		t.Fatal("forget")
	}
	// A report of a destroyed window changes nothing.
	w1.finished = true
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"12"}})
	if m.byDevice["12"] != nil {
		t.Fatal("a destroyed window claimed a device")
	}
}

// A tab dragged out of a session window: a new window at the drop point, the
// size of the one it came from, with the same session (Windows session, view
// only, server); dropped on another session window's title bar, it moves
// there; never the last tab of a window.
func TestTearOff(t *testing.T) {
	_, host, m, focused, _ := testMainBridge(t)
	w1, d1, h1 := testSessionWindow(t, m, 0x100, "12")
	w2, d2, h2 := testSessionWindow(t, m, 0x200, "14")
	// Window 1 holds devices of server A (keys "A:<id>"), window 2 one of no
	// profile (its bare id): tabs of any server live side by side.
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:13", "A:15"}})
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"14"}})
	cursor := winPoint{1500, 300}
	var dropOn *detachedWindow
	m.cursorHook = func() winPoint { return cursor }
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return dropOn, false }
	work := winRect{0, 0, 1920, 1040}
	m.placeHook = func(w *detachedWindow, pt winPoint) (winPoint, winRect, int32) {
		if w != w1 || pt != cursor {
			t.Errorf("placement asked for %v at %v", w.hwnd, pt)
		}
		return winPoint{1000, 700}, work, 96
	}
	var created []detachReq
	var fail error
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		if fail != nil {
			return nil, fail
		}
		return &detachedWindow{webHost: webHost{hwnd: uintptr(0x300 + len(created))}, mgr: m, deviceID: r.deviceID}, nil
	}
	runDispatched := func() {
		q := host.dispatched
		host.dispatched = nil
		for _, f := range q {
			f()
		}
	}
	three, bad := 3, -2

	// Refused: no token, bad values, a device not in this window, the last tab.
	if _, err := d1.tearOff("x", "13", "PC-13", detachOpts{}); err != errNotAllowed {
		t.Fatal("tear-off without token")
	}
	for _, o := range []detachOpts{{Session: &bad}, {Profile: "../x"}} {
		if _, err := d1.tearOff(d1.token, "13", "PC-13", o); err == nil {
			t.Fatalf("tear-off options %+v accepted", o)
		}
	}
	if _, err := d1.tearOff(d1.token, "../13", "PC-13", detachOpts{}); err == nil {
		t.Fatal("tear-off: invalid id accepted")
	}
	if _, err := d1.tearOff(d1.token, "14", "PC-14", detachOpts{}); err == nil {
		t.Fatal("tore off another window's tab")
	}
	if _, err := d2.tearOff(d2.token, "14", "PC-14", detachOpts{}); err == nil {
		t.Fatal("tore off the last tab of a window")
	}
	if len(host.dispatched) != 0 {
		t.Fatal("a refused tear-off scheduled a window")
	}

	// Dropped on the desktop: a new window there, the tab's session carried.
	r, err := d1.tearOff(d1.token, "13", "PC-13\n", detachOpts{Session: &three, ViewOnly: true, Profile: "A"})
	if err != nil || r != "opening" || len(host.dispatched) != 1 {
		t.Fatalf("tear-off: %q %v", r, err)
	}
	if strings.Join(w1.tabs, ",") != "A:12,A:15" || m.byDevice["A:13"] != nil || !m.opening["A:13"] {
		t.Fatalf("while its window opens: tabs=%v opening=%v", w1.tabs, m.opening)
	}
	// A report of its old window sent before the page learnt it (the tab still
	// listed), then the one without it: neither takes it back.
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:13", "A:15"}})
	if m.byDevice["A:13"] != nil || w1.hasTab("A:13") {
		t.Fatal("a stale report took back a tab whose window is on its way")
	}
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:15"}})
	host.evals = nil
	runDispatched()
	if len(created) != 1 {
		t.Fatal("no window created")
	}
	c := created[0]
	if c.deviceID != "13" || c.key != "A:13" || c.title != "PC-13" || c.session != 3 || !c.viewOnly || c.profile != "A" || c.gen != m.gen || c.from != w1 {
		t.Fatalf("carried state: %+v", c)
	}
	// The drop point lands on its title bar: 120 px from its left, mid-height of the 34 px bar.
	if c.rect == nil || *c.rect != (winRect{1380, 283, 2380, 983}) {
		t.Fatalf("new window rect: %v", c.rect)
	}
	if w := m.byDevice["A:13"]; w == nil || w.hwnd != 0x301 || strings.Join(w.tabs, ",") != "A:13" || m.opening["A:13"] || !evalHas(host, "__reachOnDetached", `"A:13"`) {
		t.Fatalf("after creation: %v opening=%v", m.byDevice["A:13"], m.opening)
	}
	// The request of a window asked from the main window places nothing.
	if created[0].from == nil {
		t.Fatal("tear-off request without its source")
	}

	// Dropped at the bottom right corner of the screen: its title bar stays on it.
	cursor = winPoint{1915, 1035}
	if r, _ := d1.tearOff(d1.token, "15", "PC-15", detachOpts{Profile: "A"}); r != "opening" {
		t.Fatal(r)
	}
	runDispatched()
	if c := created[len(created)-1]; c.rect == nil || *c.rect != (winRect{1760, 1006, 2760, 1706}) || c.session != -1 || c.viewOnly {
		t.Fatalf("clamped rect / defaults: %+v %v", c, c.rect)
	}

	// Its window cannot open: the tab goes back where it was.
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:16"}})
	fail = errBadArgument
	cursor = winPoint{500, 500}
	if r, _ := d1.tearOff(d1.token, "16", "PC-16", detachOpts{ViewOnly: true, Profile: "A"}); r != "opening" {
		t.Fatal(r)
	}
	h1.evals = nil
	host.evals = nil
	runDispatched()
	if m.byDevice["A:16"] != w1 || !w1.hasTab("A:16") || !evalHas(h1, "__reachAdoptTab", `"deviceId":"16"`) || !evalHas(h1, "__reachAdoptTab", `"viewOnly":true`) || m.opening["A:16"] {
		t.Fatalf("failed window: tab not back: %v %v", m.byDevice["A:16"], h1.evals)
	}
	if evalHas(host, "__reachOnDetachFailed", "16") {
		t.Fatal("the main page was told of a failure the source window handled")
	}
	fail = nil

	// Dropped on window 2's title bar: the tab moves there.
	dropOn = w2
	h2.evals = nil
	nCreated := len(created)
	r, err = d1.tearOff(d1.token, "16", "PC-16", detachOpts{Session: &three, Profile: "A"})
	if err != nil || r != "moved" || len(created) != nCreated || len(host.dispatched) != 0 {
		t.Fatalf("drop on another window: %q %v", r, err)
	}
	if m.byDevice["A:16"] != w2 || !w2.hasTab("A:16") || w1.hasTab("A:16") || (*focused)[len(*focused)-1] != 0x200 {
		t.Fatalf("moved: %v tabs1=%v tabs2=%v", m.byDevice["A:16"], w1.tabs, w2.tabs)
	}
	if !evalHas(h2, "__reachAdoptTab", `{"deviceId":"16","title":"PC-16","session":3,"viewOnly":false,"profile":"A"}`) {
		t.Fatalf("adopt call: %v", h2.evals)
	}
	// Dropped on its own title bar, or on a window that is closing: a new window.
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:17", "A:18"}})
	dropOn = w1
	if r, _ := d1.tearOff(d1.token, "17", "PC-17", detachOpts{Profile: "A"}); r != "opening" {
		t.Fatalf("drop on its own title bar: %q", r)
	}
	dropOn = w2
	w2.closing = true
	if r, _ := d1.tearOff(d1.token, "18", "PC-18", detachOpts{Profile: "A"}); r != "opening" || w2.hasTab("A:18") {
		t.Fatalf("drop on a closing window: %q", r)
	}
}

// A tab torn out of a session window, dropped back on the main window's own
// tab strip: it regroups there (the reverse of the main page's Detach
// feature), never a new window nor another session window.
func TestTearOffOntoMainWindow(t *testing.T) {
	_, host, m, focused, _ := testMainBridge(t)
	w1, d1, _ := testSessionWindow(t, m, 0x100, "12")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"12", "13"}})
	m.cursorHook = func() winPoint { return winPoint{200, 20} }
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return nil, true }
	host.evals = nil
	r, err := d1.tearOff(d1.token, "13", "PC-13", detachOpts{Session: intp(3), ViewOnly: true})
	if err != nil || r != "moved" || len(host.dispatched) != 0 {
		t.Fatalf("drop on main window: %q %v", r, err)
	}
	if m.byDevice["13"] != nil || w1.hasTab("13") || !m.mainTabs["13"] {
		t.Fatalf("not regrouped into main: byDevice=%v tabs=%v mainTabs=%v", m.byDevice["13"], w1.tabs, m.mainTabs)
	}
	if (*focused)[len(*focused)-1] != 0 {
		t.Fatalf("main window not focused: %v", *focused)
	}
	if !evalHas(host, "__reachAdoptTab", `{"deviceId":"13","title":"PC-13","session":3,"viewOnly":true,"profile":""}`) {
		t.Fatalf("adopt call: %v", host.evals)
	}
	if !evalHas(host, "__reachOnDetached", `["12"]`) {
		t.Fatalf("main page not told the tab left window 1: %v", host.evals)
	}
}

func intp(n int) *int { return &n }

func TestTearOffRect(t *testing.T) {
	work := winRect{0, 0, 1920, 1040}
	size, grab := winPoint{1000, 700}, winPoint{120, 17}
	for _, c := range []struct {
		pt   winPoint
		want winRect
	}{
		{winPoint{800, 400}, winRect{680, 383, 1680, 1083}},   // where dropped (the bottom may hang off, like any window)
		{winPoint{10, 5}, winRect{-110, 0, 890, 700}},         // top edge: the title bar stays below it
		{winPoint{-900, 400}, winRect{-840, 383, 160, 1083}},  // far left: 160 px stay visible
		{winPoint{3000, 400}, winRect{1760, 383, 2760, 1083}}, // far right: likewise
		{winPoint{800, 2000}, winRect{680, 1006, 1680, 1706}}, // below the screen: the title bar stays on it
	} {
		if got := tearOffRect(c.pt, size, grab, work, 34, 160); got != c.want {
			t.Fatalf("tearOffRect(%v) = %v, want %v", c.pt, got, c.want)
		}
	}
	// A second screen on the left (negative coordinates).
	if got := tearOffRect(winPoint{-1000, 300}, size, grab, winRect{-1920, 0, 0, 1040}, 34, 160); got != (winRect{-1120, 283, -120, 983}) {
		t.Fatalf("left screen: %v", got)
	}
	// Unknown work area: exactly where dropped.
	if got := tearOffRect(winPoint{5, 5}, size, grab, winRect{}, 34, 160); got != (winRect{-115, -12, 885, 688}) {
		t.Fatalf("no work area: %v", got)
	}
	// A narrow window keeps all of itself visible horizontally.
	if got := tearOffRect(winPoint{3000, 400}, winPoint{100, 300}, winPoint{33, 17}, work, 34, 160); got != (winRect{1820, 383, 1920, 683}) {
		t.Fatalf("narrow window: %v", got)
	}
	if a := pointArgs(winPoint{-1, 2}); runtime.GOARCH != "386" && (len(a) != 1 || uint64(a[0]) != 0x00000002FFFFFFFF) {
		t.Fatalf("POINT by value: %x", a)
	}
}

// Device keys (multi-server mode): a device id, or "<profile>:<device id>".
func TestDevKeys(t *testing.T) {
	for k, want := range map[string]bool{
		"12": true, "abc-D_9": true, "a1b2c3d4e5f60718:12": true, "A:u-1": true,
		"": false, ":12": false, "A:": false, "A:1:2": false, "../x": false, "A:../x": false, "<b>:1": false,
		strings.Repeat("f", 65) + ":1": false, "A:" + strings.Repeat("9", 65): false,
	} {
		if validDevKey(k) != want {
			t.Errorf("validDevKey(%q) = %v", k, !want)
		}
	}
	if devKey("", "7") != "7" || devKey("A", "7") != "A:7" || keyProfile("A:7") != "A" || keyProfile("7") != "" {
		t.Fatal("devKey / keyProfile")
	}
}

// The same device id on two servers: two devices, two windows.
func TestSameIDTwoServers(t *testing.T) {
	b, host, m, focused, _ := testMainBridge(t)
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
	if r, _ := b.openWindow(b.token, "7", "PC-A7", detachOpts{Profile: "A"}); r != "opening" {
		t.Fatal(r)
	}
	if r, _ := b.openWindow(b.token, "7", "PC-B7", detachOpts{Profile: "B"}); r != "opening" {
		t.Fatalf("device 7 of B while 7 of A opens: %q", r)
	}
	run()
	if len(created) != 2 || created[0].key != "A:7" || created[1].key != "B:7" || created[0].deviceID != "7" || created[1].profile != "B" {
		t.Fatalf("created: %+v", created)
	}
	wa, wb := m.byDevice["A:7"], m.byDevice["B:7"]
	if wa == nil || wb == nil || wa == wb || strings.Join(m.ids(), ",") != "A:7,B:7" || !evalHas(host, "__reachOnDetached", `["A:7","B:7"]`) {
		t.Fatalf("two windows: %v", m.byDevice)
	}
	// Each key reaches its own window; a bare id is neither.
	if ok, _ := b.focusWindow(b.token, "B:7"); !ok || (*focused)[len(*focused)-1] != wb.hwnd {
		t.Fatal("focus B:7")
	}
	if ok, _ := b.focusWindow(b.token, "7"); ok {
		t.Fatal("a bare id focused a window of a profile")
	}
	if r, _ := b.openWindow(b.token, "7", "PC-A7", detachOpts{Profile: "A"}); r != "focused" || (*focused)[len(*focused)-1] != wa.hwnd {
		t.Fatalf("A:7 again: %q", r)
	}
	if ids, _ := b.listWindows(b.token); strings.Join(ids, ",") != "A:7,B:7" {
		t.Fatalf("reachListWindows: %v", ids)
	}
}

// reachCloseProfileWindows: one server's sessions end everywhere; the other
// servers' tabs stay, the windows asked for on that server never open.
func TestCloseProfileWindows(t *testing.T) {
	b, host, m, _, closed := testMainBridge(t)
	w1, d1, h1 := testSessionWindow(t, m, 0x100, "7")
	w2, d2, _ := testSessionWindow(t, m, 0x200, "8")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:7", "B:7", "B:9"}}) // mixed
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"B:8"}})               // B only
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: 0x900}, mgr: m, deviceID: r.deviceID}, nil
	}
	b.openWindow(b.token, "11", "PC-B11", detachOpts{Profile: "B"}) // queued
	b.openWindow(b.token, "12", "PC-A12", detachOpts{Profile: "A"}) // queued
	if _, err := b.closeProfileWindows("nope", "B"); err != errNotAllowed {
		t.Fatal("without token")
	}
	if _, err := b.closeProfileWindows(b.token, "../x"); err == nil {
		t.Fatal("bad profile accepted")
	}
	host.evals = nil
	n, err := b.closeProfileWindows(b.token, "B")
	if err != nil || n != 3 {
		t.Fatalf("closed %d tabs (%v)", n, err)
	}
	if strings.Join(w1.tabs, ",") != "A:7" || m.byDevice["A:7"] != w1 || m.byDevice["B:7"] != nil || m.byDevice["B:9"] != nil {
		t.Fatalf("mixed window: tabs=%v", w1.tabs)
	}
	if !evalHas(h1, "__reachCloseTab", `"B:7"`) || !evalHas(h1, "__reachCloseTab", `"B:9"`) || evalHas(h1, "__reachCloseTab", `"A:7"`) {
		t.Fatalf("mixed window told: %v", h1.evals)
	}
	if len(*closed) != 1 || (*closed)[0] != w2.hwnd || m.byDevice["B:8"] != nil {
		t.Fatalf("B-only window: closed=%v", *closed)
	}
	if m.opening["B:11"] || !m.opening["A:12"] || !evalHas(host, "__reachOnDetached", `["A:7"]`) {
		t.Fatalf("opening=%v evals=%v", m.opening, host.evals)
	}
	run := host.dispatched
	host.dispatched = nil
	for _, f := range run {
		f()
	}
	if len(created) != 1 || created[0].key != "A:12" || m.byDevice["B:11"] != nil {
		t.Fatalf("the cancelled window of B opened: %+v", created)
	}
	// Nothing of B left: nothing to do.
	if n, _ := b.closeProfileWindows(b.token, "B"); n != 0 {
		t.Fatalf("again: %d", n)
	}
	// Being created when it is cancelled: closed at once, never announced.
	b.openWindow(b.token, "13", "PC-B13", detachOpts{Profile: "B"})
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		_, _ = b.closeProfileWindows(b.token, "B") // (the main page's call inside Embed's nested loop)
		return &detachedWindow{webHost: webHost{hwnd: 0x913}, mgr: m, deviceID: r.deviceID}, nil
	}
	host.evals = nil
	run = host.dispatched
	host.dispatched = nil
	for _, f := range run {
		f()
	}
	if m.byDevice["B:13"] != nil || (*closed)[len(*closed)-1] != 0x913 || evalHas(host, "__reachOnDetached", `"B:13"`) {
		t.Fatalf("created while cancelled: byDevice=%v closed=%v", m.byDevice, *closed)
	}
}

// A mosaic moving whole (tab_groups.go tgTearOff): its first tab goes the
// usual way, the others follow with Into (the key of the first): into the
// window it went to, whatever the cursor, even when that empties this
// window; never before that window exists, never into this one, never with
// Merge. A window dragged by its only tab, a mosaic of several: Merge for its
// first member (more than one tab in the window), and tabDragEnded asks the
// page to hand them over.
func TestTearOffMosaicInto(t *testing.T) {
	_, host, m, _, _ := testMainBridge(t)
	w1, d1, h1 := testSessionWindow(t, m, 0x100, "12")
	w2, d2, h2 := testSessionWindow(t, m, 0x200, "20")
	park := strings.Repeat("cd", 32)
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:13~ssh", "A:14", "A:15"}})
	d2.windowTabs(d2.token, windowTabsInfo{IDs: []string{"A:20"}})
	cursor := winPoint{900, 400}
	var dropOn *detachedWindow
	m.cursorHook = func() winPoint { return cursor }
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return dropOn, false }
	m.placeHook = func(w *detachedWindow, pt winPoint) (winPoint, winRect, int32) {
		return winPoint{1000, 700}, winRect{0, 0, 1920, 1040}, 96
	}
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: uintptr(0x300 + len(created))}, mgr: m, deviceID: r.deviceID}, nil
	}
	runDispatched := func() {
		q := host.dispatched
		host.dispatched = nil
		for _, f := range q {
			f()
		}
	}

	// Refused: Into with Merge, a bad key, its own key, a key nowhere.
	for _, o := range []detachOpts{{Profile: "A", Into: "A:12", Merge: true}, {Profile: "A", Into: "../x"}} {
		if _, err := d1.tearOff(d1.token, "14", "PC-14", o); err == nil {
			t.Fatalf("tear-off options %+v accepted", o)
		}
	}
	if _, err := d1.tearOff(d1.token, "14", "PC-14", detachOpts{Profile: "A", Into: "A:14"}); err == nil {
		t.Fatal("into its own key")
	}
	if _, err := d1.tearOff(d1.token, "14", "PC-14", detachOpts{Profile: "A", Into: "A:99"}); err == nil {
		t.Fatal("into a key no window has")
	}

	// Its first tab dropped on the desktop: a new window on its way.
	if r, err := d1.tearOff(d1.token, "12", "PC-12", detachOpts{Profile: "A"}); err != nil || r != "opening" {
		t.Fatalf("first member: %q %v", r, err)
	}
	// The next one before that window exists: refused (the page waits for it).
	if _, err := d1.tearOff(d1.token, "14", "PC-14", detachOpts{Profile: "A", Into: "A:12"}); err == nil || !w1.hasTab("A:14") {
		t.Fatal("into a window still opening")
	}
	runDispatched()
	w3 := m.byDevice["A:12"]
	if w3 == nil || w3.hwnd != 0x301 {
		t.Fatalf("first member's window: %v", w3)
	}
	// The others: into it, wherever the cursor is (over window 2 now).
	dropOn = w2
	cursor = winPoint{10, 10}
	if r, err := d1.tearOff(d1.token, "13", "PC-13 · SSH", detachOpts{Profile: "A", Kind: "ssh", Park: park, Into: "A:12"}); err != nil || r != "moved" {
		t.Fatalf("terminal into: %q %v", r, err)
	}
	if m.byDevice["A:13~ssh"] != w3 || !w3.hasTab("A:13~ssh") || w1.hasTab("A:13~ssh") || w2.hasTab("A:13~ssh") || len(created) != 1 {
		t.Fatalf("terminal moved: %v tabs1=%v tabs3=%v", m.byDevice["A:13~ssh"], w1.tabs, w3.tabs)
	}
	// Window 1's last tabs: into it as well (this window then closes itself).
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:14"}})
	if r, err := d1.tearOff(d1.token, "14", "PC-14", detachOpts{Profile: "A", Into: "A:12"}); err != nil || r != "moved" || m.byDevice["A:14"] != w3 || len(w1.tabs) != 0 {
		t.Fatalf("last tab into: %q %v tabs1=%v", r, err, w1.tabs)
	}

	// A mosaic whose first tab went to the main window: the others too.
	w4, d4, _ := testSessionWindow(t, m, 0x400, "30")
	d4.windowTabs(d4.token, windowTabsInfo{IDs: []string{"A:30", "A:31"}})
	m.mainTabs["A:29"] = true
	host.evals = nil
	if r, err := d4.tearOff(d4.token, "30", "PC-30", detachOpts{Profile: "A", Into: "A:29"}); err != nil || r != "moved" || !m.mainTabs["A:30"] || w4.hasTab("A:30") {
		t.Fatalf("into the main window: %q %v", r, err)
	}
	if !evalHas(host, "__reachAdoptTab", `"deviceId":"30"`) {
		t.Fatalf("main page not handed the tab: %v", host.evals)
	}

	// The window dragged by its only mosaic (several tabs) onto window 2:
	// the page is asked to hand it over; its first member merges.
	w5, d5, h5 := testSessionWindow(t, m, 0x500, "40")
	d5.windowTabs(d5.token, windowTabsInfo{IDs: []string{"A:40", "A:41"}})
	dropOn = w2
	h5.evals = nil
	m.tabDragEnded(w5)
	if !evalHas(h5, "__reachMergeTab", "") {
		t.Fatalf("a window of two tabs (one mosaic) dropped on another: not asked to merge: %v", h5.evals)
	}
	h2.evals = nil
	if r, err := d5.tearOff(d5.token, "40", "PC-40", detachOpts{Profile: "A", Merge: true}); err != nil || r != "moved" || m.byDevice["A:40"] != w2 {
		t.Fatalf("first member merged: %q %v", r, err)
	}
	if r, err := d5.tearOff(d5.token, "41", "PC-41", detachOpts{Profile: "A", Into: "A:40"}); err != nil || r != "moved" || m.byDevice["A:41"] != w2 || len(w5.tabs) != 0 {
		t.Fatalf("second member into: %q %v", r, err)
	}
	if !evalHas(h2, "__reachAdoptTab", `"deviceId":"40"`) || !evalHas(h2, "__reachAdoptTab", `"deviceId":"41"`) {
		t.Fatalf("window 2 not handed both: %v", h2.evals)
	}
	_ = h1
}
