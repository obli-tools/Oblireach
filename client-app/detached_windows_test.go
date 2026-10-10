//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unsafe"
)

// fakeHost stands for a window's webview in the bridge tests.
type fakeHost struct {
	dispatched []func()
	evals      []string
	binds      []string
}

func (f *fakeHost) Init(string) {}
func (f *fakeHost) Bind(name string, _ interface{}) error {
	f.binds = append(f.binds, name)
	return nil
}
func (f *fakeHost) Dispatch(fn func())     { f.dispatched = append(f.dispatched, fn) }
func (f *fakeHost) Eval(js string)         { f.evals = append(f.evals, js) }
func (f *fakeHost) Window() unsafe.Pointer { return nil }

// The entry of a detached window lands on the page in detached mode, always
// on this origin, built from validated values only.
func TestEntryTargetDetached(t *testing.T) {
	p := newProxy(&Config{}, t.TempDir(), 4321)
	enter := func(raw string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, raw, nil))
		return rec
	}
	u, err := url.Parse(p.detachedEntryURL("12", "a1b2c3d4e5f60718", 3, true, ""))
	if err != nil || u.Host != "127.0.0.1:4321" || u.Path != entryPath {
		t.Fatalf("detached entry URL = %v (%v)", u, err)
	}
	rec := enter(u.RequestURI())
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?detached=12&session=3&vo=1&p=a1b2c3d4e5f60718" {
		t.Fatalf("detached entry: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != p.authCookie() || !c[0].HttpOnly {
		t.Fatalf("entry cookie: %v", c)
	}
	// Automatic session, control, no server profile: only the device.
	u, _ = url.Parse(p.detachedEntryURL("abc-DEF_9", "", -1, false, ""))
	if loc := enter(u.RequestURI()).Header().Get("Location"); loc != "/?detached=abc-DEF_9" {
		t.Fatalf("auto session: %q", loc)
	}
	// The profile is validated like the device (dropped otherwise: the page
	// then refuses to start, as for another server).
	for _, pr := range []string{"../x", "a b", "<b>", "//evil", strings.Repeat("f", 65)} {
		loc := enter(entryPath + "?k=" + url.QueryEscape(p.secret) + "&d=7&p=" + url.QueryEscape(pr)).Header().Get("Location")
		if loc != "/?detached=7" {
			t.Fatalf("profile %q: landed on %q", pr, loc)
		}
	}
	k := url.QueryEscape(p.secret)
	for _, d := range []string{"//evil.example", "https://evil.example", "../x", "a b", "<script>", "1/2", "a%2F", strings.Repeat("9", 65), "\\\\evil"} {
		loc := enter(entryPath + "?k=" + k + "&d=" + url.QueryEscape(d)).Header().Get("Location")
		if loc != "/" {
			t.Fatalf("device %q: landed on %q, want /", d, loc)
		}
	}
	for _, s := range []string{"-1", "x", "1e3", "99999999999", " 3"} {
		loc := enter(entryPath + "?k=" + k + "&d=7&s=" + url.QueryEscape(s)).Header().Get("Location")
		if loc != "/?detached=7" {
			t.Fatalf("session %q: landed on %q", s, loc)
		}
	}
	if loc := enter(entryPath + "?k=" + k + "&d=7&vo=yes").Header().Get("Location"); loc != "/?detached=7" {
		t.Fatalf("vo=yes: %q", loc)
	}
	// The plain entry (main window) is unchanged; a wrong secret gets nothing.
	if loc := enter(entryPath + "?k=" + k).Header().Get("Location"); loc != "/" {
		t.Fatalf("main entry: %q", loc)
	}
	if rec := enter(entryPath + "?k=nope&d=7"); rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" {
		t.Fatalf("wrong secret: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func testMainBridge(t *testing.T) (*nativeBridge, *fakeHost, *sessionWindows, *[]uintptr, *[]uintptr) {
	host := &fakeHost{}
	b, err := newNativeBridge(host, "http://127.0.0.1:4321")
	if err != nil {
		t.Fatal(err)
	}
	m := newSessionWindows(b, newProxy(&Config{}, t.TempDir(), 4321), "http://127.0.0.1:4321")
	var focused, closed []uintptr
	m.focusHook = func(h uintptr) { focused = append(focused, h) }
	m.closeHook = func(h uintptr) { closed = append(closed, h) }
	b.windows = m
	return b, host, m, &focused, &closed
}

func TestOpenWindowBinding(t *testing.T) {
	b, host, m, focused, closed := testMainBridge(t)
	tok := b.token
	session := func(n int) detachOpts { return detachOpts{Session: &n} }

	if _, err := b.openWindow("nope", "12", "PC", detachOpts{}); err != errNotAllowed {
		t.Fatalf("bad token: %v", err)
	}
	for _, id := range []string{"", "../1", "1 2", "//x", strings.Repeat("a", 65)} {
		if _, err := b.openWindow(tok, id, "PC", detachOpts{}); err == nil {
			t.Fatalf("device %q accepted", id)
		}
	}
	for _, s := range []int{-2, maxDetachSession + 1} {
		if _, err := b.openWindow(tok, "12", "PC", session(s)); err == nil {
			t.Fatalf("session %d accepted", s)
		}
	}
	for _, pr := range []string{"../x", "a b", "<b>", strings.Repeat("f", 65)} {
		if _, err := b.openWindow(tok, "12", "PC", detachOpts{Profile: pr}); err == nil {
			t.Fatalf("profile %q accepted", pr)
		}
	}
	if len(host.dispatched) != 0 {
		t.Fatal("a refused call scheduled a window")
	}

	// Valid: the window is created from the main loop (dispatched), once.
	if r, err := b.openWindow(tok, "12", "PC-12", session(3)); err != nil || r != "opening" {
		t.Fatalf("open: %q %v", r, err)
	}
	if r, _ := b.openWindow(tok, "12", "PC-12", detachOpts{}); r != "opening" || len(host.dispatched) != 1 {
		t.Fatalf("double click: %q, %d dispatched", r, len(host.dispatched))
	}
	// Being created: it has its window already (a click on it in the main
	// list does not start again), nothing to bring forward yet.
	if ok, err := b.focusWindow(tok, "12"); err != nil || !ok || len(*focused) != 0 {
		t.Fatalf("focus while opening: %v %v %v", ok, err, *focused)
	}
	// Its window exists: focused, not opened again.
	m.byDevice["12"] = &detachedWindow{webHost: webHost{hwnd: 0x1200}, mgr: m, deviceID: "12"}
	delete(m.opening, "12")
	if r, _ := b.openWindow(tok, "12", "PC-12", detachOpts{}); r != "focused" || len(*focused) != 1 || (*focused)[0] != 0x1200 {
		t.Fatalf("existing window: %q focused=%v", r, *focused)
	}
	if ok, _ := b.focusWindow(tok, "12"); !ok || len(*focused) != 2 {
		t.Fatal("reachFocusWindow")
	}
	if ok, _ := b.focusWindow(tok, "99"); ok {
		t.Fatal("focus of a device without window")
	}
	if _, err := b.focusWindow("", "12"); err != errNotAllowed {
		t.Fatal("focus without token")
	}

	// Close one, close all.
	m.byDevice["13"] = &detachedWindow{webHost: webHost{hwnd: 0x1300}, mgr: m, deviceID: "13"}
	if ok, _ := b.closeWindow(tok, "13"); !ok || len(*closed) != 1 || (*closed)[0] != 0x1300 {
		t.Fatalf("reachCloseWindow: %v", *closed)
	}
	if _, err := b.closeWindow("x", "13"); err != errNotAllowed {
		t.Fatal("close without token")
	}
	// (13's tab is going: no longer listed; only 12's window is left.)
	if n, _ := b.closeWindows(tok); n != 1 || len(*closed) != 2 || (*closed)[1] != 0x1200 {
		t.Fatalf("reachCloseWindows: %d %v", n, *closed)
	}
	if _, err := b.closeWindows(""); err != errNotAllowed {
		t.Fatal("close all without token")
	}

	// The main page hears which devices have their window.
	host.evals = nil
	m.notify()
	if len(host.evals) != 1 || !strings.Contains(host.evals[0], `"__reachOnDetached"`) || !strings.Contains(host.evals[0], `"12"`) ||
		!strings.Contains(host.evals[0], `location.origin !== "http://127.0.0.1:4321"`) {
		t.Fatalf("notification: %v", host.evals)
	}

	// Only the main window has these bindings; a detached one closes itself.
	host.binds = nil
	b.bind()
	if strings.Join(host.binds, ",") != "reachClipboardRead,reachClipboardWrite,reachClipboardFiles,reachReadFileChunk,reachClipboardWatch,reachClipboardPaste,reachClipboardPasteKinds,reachKeyCapture,reachAttention,reachVaultState,reachVaultForSession,reachVaultUnlock,reachVaultUnlockHello,reachVaultUnlockHelloAuto,reachVaultCreate,reachVaultLock,reachVaultType,reachVaultTypeVm,reachSshConnect,reachSshCwd,reachLocalInfo,reachVaultConnInfo,reachVaultByParent,reachConnProbe,reachSftpConnect,reachSftpClose,reachSftpCmd,reachRdpConnect,reachRdpResize,reachRdpClose,reachRdpShow,reachRdpPark,reachRdpAttach,reachRdpTunnel,reachWindowDrag,reachWindowControl,reachWindowState,reachOpenWindow,reachFocusWindow,reachCloseWindow,reachCloseWindows,reachCloseProfileWindows,reachListWindows,reachWindowTabs,reachWindowTargets,reachVaultList,reachVaultSave,reachVaultDelete,reachVaultReveal,reachVaultCredentials,reachVaultHelloSet,reachConnImportPick,reachConnImportPassphrase,reachConnImportCommit,reachConnImportDrop,reachConnExport" {
		t.Fatalf("main bindings: %v", host.binds)
	}
	if ids, err := b.listWindows(tok); err != nil || len(ids) != 1 {
		t.Fatalf("reachListWindows: %v %v", ids, err)
	}
	if _, err := b.listWindows(""); err != errNotAllowed {
		t.Fatal("list without token")
	}
	dh := &fakeHost{}
	d, _ := newNativeBridge(dh, "http://127.0.0.1:4321")
	selfClosed := 0
	d.closeSelf = func() { selfClosed++ }
	d.bind()
	if strings.Contains(strings.Join(dh.binds, ","), "reachOpenWindow") || dh.binds[len(dh.binds)-1] != "reachCloseSelf" {
		t.Fatalf("detached bindings: %v", dh.binds)
	}
	// A real session window also gets its tab bindings.
	dh.binds = nil
	d.detached = &detachedWindow{mgr: m}
	d.bind()
	if got := strings.Join(dh.binds[len(dh.binds)-5:], ","); got != "reachCloseSelf,reachWindowTabs,reachOpenTab,reachTearOff,reachWindowTargets" || strings.Contains(strings.Join(dh.binds, ","), "reachOpenWindow") {
		t.Fatalf("session window bindings: %v", dh.binds)
	}
	d.detached = nil
	if _, err := d.closeOwnWindow("nope"); err != errNotAllowed || selfClosed != 0 {
		t.Fatal("reachCloseSelf without token")
	}
	if ok, _ := d.closeOwnWindow(d.token); !ok || selfClosed != 1 {
		t.Fatal("reachCloseSelf")
	}
	// Without a manager (never the case for the main window): refused.
	if _, err := d.openWindow(d.token, "12", "PC", detachOpts{}); err == nil {
		t.Fatal("a detached window opened a window")
	}
}

// Sign-out / server switch (closeWindows) while a window is queued or being
// created: it never shows (it would open signed out, or on the new server's
// device with that id), and the main page never hears of it.
func TestCloseWindowsCancelsPending(t *testing.T) {
	b, host, m, _, closed := testMainBridge(t)
	tok := b.token
	var created []detachReq
	var during func()
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		if during != nil {
			during() // as the main page's calls inside Embed's nested loop
		}
		return &detachedWindow{webHost: webHost{hwnd: uintptr(0x100 + len(created))}, mgr: m, deviceID: r.deviceID}, nil
	}
	// (keys: the device of a server profile, "<profile>:<id>")
	notified := func(key string) bool {
		for _, e := range host.evals {
			if strings.Contains(e, `"__reachOnDetached"`) && strings.Contains(e, `"`+key+`"`) {
				return true
			}
		}
		return false
	}
	runDispatched := func() {
		q := host.dispatched
		host.dispatched = nil
		for _, f := range q {
			f()
		}
	}

	// Queued, then closeWindows: never created.
	if r, _ := b.openWindow(tok, "12", "PC-12", detachOpts{Profile: "srvA"}); r != "opening" {
		t.Fatal(r)
	}
	if n, _ := b.closeWindows(tok); n != 0 || len(m.opening) != 0 {
		t.Fatalf("closeWindows: %d, opening %v", n, m.opening)
	}
	runDispatched()
	if len(created) != 0 || len(m.byDevice) != 0 || notified("srvA:12") {
		t.Fatalf("a cancelled window was created: %v", created)
	}

	// Asked again (not stuck "opening"); closeWindows runs while it is being
	// created: closed at once, never registered nor announced.
	if r, _ := b.openWindow(tok, "12", "PC-12", detachOpts{Profile: "srvA"}); r != "opening" || len(host.dispatched) != 1 {
		t.Fatalf("asked again: %q", r)
	}
	during = func() { _, _ = b.closeWindows(tok) }
	runDispatched()
	if len(created) != 1 || len(*closed) != 1 || (*closed)[0] != 0x101 || len(m.byDevice) != 0 || notified("srvA:12") {
		t.Fatalf("closed during create: created=%d closed=%v byDevice=%v notified=%v", len(created), *closed, m.byDevice, notified("srvA:12"))
	}

	// Normal case: created with what was asked, registered, announced; a
	// second click while it is being created is not queued again.
	during = func() {
		if r, _ := b.openWindow(tok, "12", "PC-12", detachOpts{Profile: "srvB"}); r != "opening" {
			t.Errorf("click during create: %q", r)
		}
	}
	sess := 4
	b.openWindow(tok, "12", "PC-12", detachOpts{Session: &sess, ViewOnly: true, Profile: "srvB"})
	runDispatched()
	last := created[len(created)-1]
	if len(created) != 2 || last.profile != "srvB" || last.session != 4 || !last.viewOnly || last.title != "PC-12" || last.gen != m.gen {
		t.Fatalf("request: %+v", last)
	}
	if m.byDevice["srvB:12"] == nil || len(m.opening) != 0 || !notified("srvB:12") || len(host.dispatched) != 0 { // (nothing more queued)
		t.Fatalf("normal open: byDevice=%v opening=%v notified=%v dispatched=%d", m.byDevice, m.opening, notified("srvB:12"), len(host.dispatched))
	}
}

// A closed bridge evaluates nothing any more and its watcher ends.
func TestBridgeShutdown(t *testing.T) {
	host := &fakeHost{}
	b, _ := newNativeBridge(host, "http://127.0.0.1:1")
	b.capture.Store(true)
	b.watchOn.Store(true)
	b.shutdown()
	b.shutdown() // twice: no panic (closed channel)
	b.evalKey(true, "MetaLeft")
	b.evalLocal("null", "{}")
	b.evalMain("__reachOnDetached", "[]")
	b.emitKey(true, "MetaLeft")
	if len(host.evals) != 0 || len(host.dispatched) != 0 || b.capture.Load() || b.watchOn.Load() {
		t.Fatalf("closed bridge still active: evals=%v dispatched=%d", host.evals, len(host.dispatched))
	}
	done := make(chan struct{})
	go func() { b.watch(); close(done) }()
	<-done // returns at once
}

func TestDetachTitle(t *testing.T) {
	for in, want := range map[string]string{
		"PC-01":                       "PC-01",
		"  srv\x00\r\n02\t ":          "srv02",
		"":                            "Oblireach session",
		"\x1b[31m":                    "[31m",
		strings.Repeat("é", 130):      strings.Repeat("é", 120),
		"Poste de Jérôme · RDS (bob)": "Poste de Jérôme · RDS (bob)",
	} {
		if got := detachTitle(in); got != want {
			t.Fatalf("detachTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// The RPC of a session window follows go-webview2's protocol (the page glue
// is the same).
func TestSessionWindowRPC(t *testing.T) {
	var r rpcBindings
	if err := r.add("x", 42); err == nil {
		t.Fatal("a non-function was bound")
	}
	_ = r.add("sum", func(a string, n int) (string, error) { return a + strings.Repeat("!", n), nil })
	_ = r.add("fail", func() error { return errNotAllowed })
	_ = r.add("opts", func(tok string, o detachOpts) (bool, error) {
		return o.Session != nil && *o.Session == 3 && o.ViewOnly, nil
	})
	cases := []struct {
		msg, res, err string
		id            int
	}{
		{`{"id":3,"method":"sum","params":["hi",2]}`, `"hi!!"`, "", 3},
		{`{"id":4,"method":"sum","params":["hi"]}`, "", "function arguments mismatch", 4},
		{`{"id":5,"method":"sum","params":[1,2]}`, "", "cannot unmarshal", 5},
		{`{"id":6,"method":"fail","params":[]}`, "", "not allowed", 6},
		{`{"id":7,"method":"nope","params":[]}`, "null", "", 7},
		{`{"id":8,"method":"opts","params":["t",{"session":3,"viewOnly":true}]}`, "true", "", 8},
		{`not json`, "", "invalid", -1},
	}
	for _, c := range cases {
		id, res, err := r.call(c.msg)
		if id != c.id || res != c.res || (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Fatalf("%s -> %d %q %v", c.msg, id, res, err)
		}
	}
	if g := rpcGlue("reachOpenWindow"); !strings.Contains(g, `var name = "reachOpenWindow";`) || !strings.Contains(g, "window.external.invoke") {
		t.Fatal("page glue")
	}
}

// Dispatch of a destroyed window drops the call; drain runs the queue in order.
func TestSessionWindowDispatch(t *testing.T) {
	w := &detachedWindow{}
	var got []int
	w.queue = []func(){func() { got = append(got, 1) }, func() { got = append(got, 2) }}
	w.drain()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 || len(w.queue) != 0 {
		t.Fatalf("drain: %v", got)
	}
	w.gone = true
	w.Dispatch(func() { got = append(got, 3) })
	w.drain()
	if len(got) != 2 {
		t.Fatal("dispatched to a destroyed window")
	}
	// Init / Eval before the WebView exists or after the window closed: nothing.
	w.Init("x")
	w.Eval("x")
}
