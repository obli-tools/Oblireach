//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHello replaces the WinRT calls; it restores them and the cache after.
func fakeHello(t *testing.T, avail bool, result int32, err error) *[]string {
	t.Helper()
	asked := &[]string{}
	oldP, oldV := helloProbeFn, helloVerifyFn
	helloProbeFn = func() (bool, error) { return avail, nil }
	helloVerifyFn = func(hwnd uintptr, msg string, timeout time.Duration) (int32, error) {
		*asked = append(*asked, msg)
		return result, err
	}
	helloForget()
	t.Cleanup(func() { helloProbeFn, helloVerifyFn = oldP, oldV; helloForget() })
	return asked
}

func helloBridge(t *testing.T) (*nativeBridge, *syncHost, *Vault, *fakePresence) {
	t.Helper()
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.windows = &sessionWindows{}
	v := newTestVaultForBridge(t)
	pr := &fakePresence{}
	v.presence = pr
	if err := v.Create("secret1", 0); err != nil {
		t.Fatal(err)
	}
	return b, h, v, pr
}

// Registered: the unlock in every window, the switch in the main one.
func TestVaultHelloBindings(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.bind()
	joined := "," + strings.Join(h.binds, ",") + ","
	if !strings.Contains(joined, ",reachVaultUnlockHello,") || strings.Contains(joined, ",reachVaultHelloSet,") {
		t.Fatalf("session window: %s", joined)
	}
	if ok, err := b.vaultUnlockHello("wrong", "aaaaaaaa"); ok || err != errNotAllowed {
		t.Fatal("vaultUnlockHello without the token")
	}
	if ok, err := b.vaultHelloSet("wrong", "aaaaaaaa", true); ok || err != errNotAllowed {
		t.Fatal("vaultHelloSet without the token")
	}
}

// The switch: only from an unlocked vault, only where Windows Hello is
// available, and turning it on asks Hello; the state tells the page.
func TestVaultHelloSwitch(t *testing.T) {
	b, h, v, pr := helloBridge(t)
	asked := fakeHello(t, true, helloVerified, nil)
	st, _ := b.vaultState(b.token)
	if !st.Hello.Available || st.Hello.Enabled {
		t.Fatalf("state: %+v", st.Hello)
	}
	b.vaultHelloSet(b.token, "aaaaaaaa", true)
	ev := h.waitEvals(t, 1)
	if !strings.Contains(ev[0], `"ok":true`) || !pr.on || len(*asked) != 1 || (*asked)[0] != tr("vault.hello.enablePrompt") {
		t.Fatalf("on: %v asked %v", ev, *asked)
	}
	if st, _ := b.vaultState(b.token); !st.Hello.Enabled {
		t.Fatal("state after on")
	}
	// Locked: refused.
	v.Lock()
	b.vaultHelloSet(b.token, "bbbbbbbb", false)
	ev = h.waitEvals(t, 2)
	if !strings.Contains(ev[1], `"code":"locked"`) || !pr.on {
		t.Fatalf("locked: %v", ev[1])
	}
	// Hello refused while turning it on: stays off.
	_ = v.Unlock("secret1")
	pr.on = false
	fakeHello(t, true, helloResultCanceled, nil)
	b.vaultHelloSet(b.token, "cccccccc", true)
	ev = h.waitEvals(t, 3)
	if !strings.Contains(ev[2], `"code":"hello_canceled"`) || pr.on {
		t.Fatalf("canceled: %v", ev[2])
	}
	// Not available: refused without a prompt.
	asked = fakeHello(t, false, helloVerified, nil)
	b.vaultHelloSet(b.token, "dddddddd", true)
	ev = h.waitEvals(t, 4)
	if !strings.Contains(ev[3], `"code":"hello_unavailable"`) || pr.on || len(*asked) != 0 {
		t.Fatalf("unavailable: %v", ev[3])
	}
	if st, _ := b.vaultState(b.token); st.Hello.Available {
		t.Fatal("state: available")
	}
}

// The unlock: each answer of Windows, in the page's codes; the prompt on
// this window with its text; "Verified" alone unlocks.
func TestVaultHelloUnlockResults(t *testing.T) {
	b, h, v, pr := helloBridge(t)
	pr.on = true
	v.Lock()
	n := 0
	for _, c := range []struct {
		res  int32
		err  error
		code string
	}{
		{helloResultCanceled, nil, "hello_canceled"},
		{helloRetriesExhausted, nil, "hello_retries"},
		{helloDeviceBusy, nil, "hello_busy"},
		{helloDeviceNotPresent, nil, "hello_unavailable"},
		{helloNotConfigured, nil, "hello_unavailable"},
		{helloDisabledByPolicy, nil, "hello_unavailable"},
		{42, nil, "hello_failed"},
		{0, errors.New("HRESULT 0x80004005"), "hello_failed"},
		{0, errHelloTimeout, "hello_canceled"},
	} {
		asked := fakeHello(t, true, c.res, c.err)
		b.vaultUnlockHello(b.token, "aaaaaaaa")
		n++
		ev := h.waitEvals(t, n)
		if !strings.Contains(ev[n-1], `"code":"`+c.code+`"`) || v.State().State != "locked" {
			t.Fatalf("result %d / %v: %v", c.res, c.err, ev[n-1])
		}
		if len(*asked) != 1 || (*asked)[0] != tr("vault.hello.prompt") {
			t.Fatalf("prompt: %v", *asked)
		}
	}
	fakeHello(t, true, helloVerified, nil)
	b.vaultUnlockHello(b.token, "bbbbbbbb")
	n++
	if ev := h.waitEvals(t, n); !strings.Contains(ev[n-1], `"ok":true`) || v.State().State != "unlocked" {
		t.Fatalf("verified: %v", ev[n-1])
	}
	// Turned off: refused, no prompt.
	v.Lock()
	pr.on = false
	asked := fakeHello(t, true, helloVerified, nil)
	b.vaultUnlockHello(b.token, "cccccccc")
	n++
	if ev := h.waitEvals(t, n); !strings.Contains(ev[n-1], `"code":"hello_off"`) || len(*asked) != 0 || v.State().State != "locked" {
		t.Fatalf("off: %v", ev[n-1])
	}
}

// The real switch: a DPAPI marker of this Windows user in the vault folder.
func TestFilePresenceFlagDPAPI(t *testing.T) {
	dir := t.TempDir()
	f := filePresenceFlag{path: filepath.Join(dir, vaultHelloName)}
	if f.Enabled() {
		t.Fatal("on without a marker")
	}
	if err := f.Set(true); err != nil || !f.Enabled() {
		t.Fatalf("on: %v", err)
	}
	b, _ := os.ReadFile(f.path)
	if strings.Contains(string(b), string(vaultHelloMarker)) {
		t.Fatal("the marker is not sealed")
	}
	// Anything else in its place: off.
	for _, junk := range [][]byte{[]byte(vaultHelloMarker), []byte("x"), {}} {
		os.WriteFile(f.path, junk, 0600)
		if f.Enabled() {
			t.Fatalf("junk %q taken for the switch", junk)
		}
	}
	// Sealed with another entropy (another DPAPI consumer): off.
	other, _ := dpapiProtectWith(vaultHelloMarker, []byte("another app"))
	os.WriteFile(f.path, other, 0600)
	if f.Enabled() {
		t.Fatal("a blob of another entropy taken for the switch")
	}
	if err := f.Set(false); err != nil || f.Enabled() {
		t.Fatalf("off: %v", err)
	}
	if err := f.Set(false); err != nil {
		t.Fatal("off twice")
	}
}

// fakeShown: whether the asking window counts as on screen; it also clears
// the automatic tries' book (hello_auto.go).
func fakeShown(t *testing.T, shown bool) {
	t.Helper()
	old := helloShownFn
	helloShownFn = func(uintptr) bool { return shown }
	helloAuto.mu.Lock()
	helloAuto.tried, helloAuto.lastFail = nil, time.Time{}
	helloAuto.mu.Unlock()
	t.Cleanup(func() {
		helloShownFn = old
		helloAuto.mu.Lock()
		helloAuto.tried, helloAuto.lastFail = nil, time.Time{}
		helloAuto.mu.Unlock()
	})
}

// The automatic prompt (an unlock prompt opening): launched once per
// opening, only for a window on screen, never again by itself after a
// cancel; otherwise "hello_skipped" and no prompt (the code field).
func TestVaultHelloUnlockAuto(t *testing.T) {
	b, h, v, pr := helloBridge(t)
	pr.on = true
	v.Lock()
	n := 0
	call := func(prompt string) string {
		b.vaultUnlockHelloAuto(b.token, "f"+prompt, prompt)
		n++
		return h.waitEvals(t, n)[n-1]
	}
	// A window in the background: no prompt.
	fakeShown(t, false)
	asked := fakeHello(t, true, helloVerified, nil)
	if ev := call("b0000001"); !strings.Contains(ev, `"code":"hello_skipped"`) || len(*asked) != 0 || v.State().State != "locked" {
		t.Fatalf("background: %v asked %v", ev, *asked)
	}
	// On screen, canceled: one prompt, the cancel told; the same opening
	// never prompts again, nor another one right after.
	fakeShown(t, true)
	asked = fakeHello(t, true, helloResultCanceled, nil)
	if ev := call("a0000001"); !strings.Contains(ev, `"code":"hello_canceled"`) || len(*asked) != 1 {
		t.Fatalf("canceled: %v asked %v", ev, *asked)
	}
	if ev := call("a0000001"); !strings.Contains(ev, `"code":"hello_skipped"`) || len(*asked) != 1 {
		t.Fatalf("same opening again: %v asked %v", ev, *asked)
	}
	if ev := call("a0000002"); !strings.Contains(ev, `"code":"hello_skipped"`) || len(*asked) != 1 {
		t.Fatalf("right after a cancel: %v asked %v", ev, *asked)
	}
	// Hello turned off, or unavailable: no prompt.
	fakeShown(t, true)
	pr.on = false
	asked = fakeHello(t, true, helloVerified, nil)
	if ev := call("c0000001"); !strings.Contains(ev, `"code":"hello_skipped"`) || len(*asked) != 0 {
		t.Fatalf("off: %v", ev)
	}
	pr.on = true
	asked = fakeHello(t, false, helloVerified, nil)
	if ev := call("d0000001"); !strings.Contains(ev, `"code":"hello_skipped"`) || len(*asked) != 0 {
		t.Fatalf("unavailable: %v", ev)
	}
	// Verified: unlocked, the prompt on this window with its text.
	asked = fakeHello(t, true, helloVerified, nil)
	if ev := call("e0000001"); !strings.Contains(ev, `"ok":true`) || v.State().State != "unlocked" || len(*asked) != 1 || (*asked)[0] != tr("vault.hello.prompt") {
		t.Fatalf("verified: %v asked %v", ev, *asked)
	}
	// A bad prompt id (with the right token): refused at once.
	if ok, err := b.vaultUnlockHelloAuto(b.token, "ffffffff", "x"); ok || err == nil {
		t.Fatal("bad prompt id accepted")
	}
	if ok, err := b.vaultUnlockHelloAuto("wrong", "ffffffff", "abcdef01"); ok || err != errNotAllowed {
		t.Fatal("without the token")
	}
}
