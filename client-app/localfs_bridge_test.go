//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// syncHost: a window's webview for the bridge tests, safe for the answers
// that come from other goroutines; its init scripts are kept.
type syncHost struct {
	mu    sync.Mutex
	inits []string
	binds []string
	queue []func()
	evals []string
}

func (h *syncHost) Init(js string) { h.mu.Lock(); h.inits = append(h.inits, js); h.mu.Unlock() }
func (h *syncHost) Bind(name string, _ interface{}) error {
	h.mu.Lock()
	h.binds = append(h.binds, name)
	h.mu.Unlock()
	return nil
}
func (h *syncHost) Dispatch(f func())      { h.mu.Lock(); h.queue = append(h.queue, f); h.mu.Unlock() }
func (h *syncHost) Eval(js string)         { h.mu.Lock(); h.evals = append(h.evals, js); h.mu.Unlock() }
func (h *syncHost) Window() unsafe.Pointer { return nil }

// drain runs what was dispatched (the UI thread), waiting up to 5 s for n evals.
func (h *syncHost) waitEvals(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		q := h.queue
		h.queue = nil
		h.mu.Unlock()
		for _, f := range q {
			f()
		}
		h.mu.Lock()
		got := append([]string(nil), h.evals...)
		h.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d evals, want %d: %v", len(got), n, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLocalFSBindingsMainWindowOnly(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.bind()
	for _, n := range localFsNames {
		for _, got := range h.binds {
			if got == n {
				t.Fatalf("%s bound without the local file system (a session window)", n)
			}
		}
	}
	// The token: the top-level local UI document only.
	if len(h.inits) != 1 || !strings.Contains(h.inits[0], `location.origin !== "http://127.0.0.1:4321" || location.pathname !== '/' || window.top !== window) return;`) {
		t.Fatalf("token reveal: %v", h.inits)
	}
	// The main window: every file binding.
	h2 := &syncHost{}
	m, _ := newNativeBridge(h2, "http://127.0.0.1:4321")
	l, _, _ := testLocalFS(t)
	m.attachLocalFS(l)
	defer m.shutdown()
	m.bind()
	have := map[string]bool{}
	for _, n := range h2.binds {
		have[n] = true
	}
	for _, n := range localFsNames {
		if !have[n] {
			t.Errorf("main window lacks %s", n)
		}
	}
}

// A session window shares the main window's local file system for the local
// pane of its Files tabs: the file bindings only (never the editing round
// trip, the settings written, the editor picked), each window answered on
// its own page.
func TestLocalFSSharedSessionWindow(t *testing.T) {
	hm, hd := &syncHost{}, &syncHost{}
	m, _ := newNativeBridge(hm, "http://127.0.0.1:4321")
	d, _ := newNativeBridge(hd, "http://127.0.0.1:4321")
	root := t.TempDir()
	l := newLocalFSWith(&fakeSys{}, filepath.Join(root, "edit"))
	m.attachLocalFS(l)
	d.attachSharedLocalFS(l)
	defer m.shutdown()
	defer d.shutdown()
	d.bind()
	have := map[string]bool{}
	for _, n := range hd.binds {
		have[n] = true
	}
	for _, n := range localFsWindowNames {
		if !have[n] {
			t.Errorf("session window lacks %s", n)
		}
	}
	shared := map[string]bool{}
	for _, n := range localFsWindowNames {
		shared[n] = true
	}
	for _, n := range localFsNames {
		if !shared[n] && have[n] {
			t.Errorf("session window has the main window's %s", n)
		}
	}
	// The main window's own calls: refused there, whatever the token.
	if _, err := d.editList(d.token); err != errNotAllowed {
		t.Fatalf("edit list: %v", err)
	}
	if _, err := d.editBegin(d.token, "abcd1234", editBeginOpts{}); err != errNotAllowed {
		t.Fatalf("edit begin: %v", err)
	}
	if _, err := d.filesSettingsSet(d.token, filesSettingsIn{}); err != errNotAllowed {
		t.Fatalf("settings set: %v", err)
	}
	if _, err := d.filesPickEditor(d.token, "abcd1234"); err != errNotAllowed {
		t.Fatalf("pick editor: %v", err)
	}
	if _, err := d.filesSettingsGet(d.token); err != nil {
		t.Fatalf("settings read: %v", err)
	}
	if _, err := d.fsList(m.token, "abcd1234", root, fsListOpts{}); err != errNotAllowed {
		t.Fatal("the main window's token accepted by a session window")
	}
	// Each window gets its own answers.
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("x"), 0o644)
	if ok, err := d.fsList(d.token, "0123abcd-0000-4000-8000-00000000000d", root, fsListOpts{}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ev := hd.waitEvals(t, 1)[0]; !strings.Contains(ev, `f("0123abcd-0000-4000-8000-00000000000d", true, {"path":`) || !strings.Contains(ev, `"name":"b.txt"`) {
		t.Fatalf("session window answer: %s", ev)
	}
	if ok, err := m.fsList(m.token, "0123abcd-0000-4000-8000-00000000000e", root, fsListOpts{}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ev := hm.waitEvals(t, 1)[0]; !strings.Contains(ev, `f("0123abcd-0000-4000-8000-00000000000e", true,`) {
		t.Fatalf("main window answer: %s", ev)
	}
	hd.mu.Lock()
	n := len(hd.evals)
	hd.mu.Unlock()
	if n != 1 {
		t.Fatalf("the main window's answer reached the session window: %d evals", n)
	}
	// The session window gone: its answers dropped, the main window's go on.
	d.shutdown()
	d.fsList(d.token, "0123abcd-0000-4000-8000-00000000000f", root, fsListOpts{})
	m.fsList(m.token, "0123abcd-0000-4000-8000-000000000010", root, fsListOpts{})
	hm.waitEvals(t, 2)
	hd.waitEvals(t, 1)
	hd.mu.Lock()
	n = len(hd.evals)
	hd.mu.Unlock()
	if n != 1 {
		t.Fatalf("evaluated in a closed session window: %d", n)
	}
}

func TestLocalFSBindingsChecks(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	root := t.TempDir()
	l := newLocalFSWith(&fakeSys{}, filepath.Join(root, "edit"))
	b.attachLocalFS(l)
	defer b.shutdown()
	tok := b.token
	if _, err := b.fsList("nope", "abcd1234", root, fsListOpts{}); err != errNotAllowed {
		t.Fatalf("wrong token: %v", err)
	}
	for _, id := range []string{"", "abc", "ABCD1234", "abcd1234'); alert(1); ('", strings.Repeat("a", 65), "abcd 1234"} {
		if _, err := b.fsList(tok, id, root, fsListOpts{}); err != errFsBadRequest {
			t.Fatalf("request id %q: %v", id, err)
		}
	}
	if _, err := b.fsReveal("", root); err != errNotAllowed {
		t.Fatal("reveal without token")
	}
	if _, err := b.editList(""); err != errNotAllowed {
		t.Fatal("edit list without token")
	}
	if _, err := b.filesSettingsSet("", filesSettingsIn{}); err != errNotAllowed {
		t.Fatal("settings without token")
	}
	// An answer: evaluated behind the top-level local UI check.
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644)
	if ok, err := b.fsList(tok, "0123abcd-0000-4000-8000-000000000001", root, fsListOpts{}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	ev := h.waitEvals(t, 1)[0]
	for _, want := range []string{`location.origin !== "http://127.0.0.1:4321"`, `location.pathname !== '/'`, `window.top !== window`,
		`window.__reachLocalFs`, `f("0123abcd-0000-4000-8000-000000000001", true, {"path":`, `"name":"a.txt"`} {
		if !strings.Contains(ev, want) {
			t.Fatalf("answer lacks %q: %s", want, ev)
		}
	}
	// A refusal: its code only.
	b.fsList(tok, "0123abcd-0000-4000-8000-000000000002", `\\evil\share`, fsListOpts{})
	ev = h.waitEvals(t, 2)[1]
	if !strings.Contains(ev, `f("0123abcd-0000-4000-8000-000000000002", false, {"code":"invalid_path"})`) {
		t.Fatalf("refusal: %s", ev)
	}
	// An editing event: the same guard.
	l.edits.push(editEvent{EditID: "e1", Type: "missing"})
	ev = h.waitEvals(t, 3)[2]
	if !strings.Contains(ev, `window.__reachLocalEdit`) || !strings.Contains(ev, `location.pathname !== '/'`) || !strings.Contains(ev, `{"editId":"e1","type":"missing"}`) {
		t.Fatalf("edit event: %s", ev)
	}
	// Too many waiting: busy.
	l.pending.Store(localMaxPending)
	if _, err := b.fsList(tok, "abcd1234", root, fsListOpts{}); err != errFsBusy {
		t.Fatalf("busy: %v", err)
	}
	l.pending.Store(0)
	// The window gone: nothing evaluated any more.
	b.shutdown()
	n := len(h.evals)
	b.evalLocalFs("abcd1234", true, "{}")
	if len(h.evals) != n {
		t.Fatal("evaluated after the window closed")
	}
}
