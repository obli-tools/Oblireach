//go:build linux

package main

// platform_linux_test.go — the Linux platform services against a private
// D-Bus (dbusfake_linux_test.go): the Secret Service protector and the
// vault on it, the support store, the memory sealer, the single instance,
// the portals (FileChooser, OpenURI, Notification, Background), and the
// local file system (mounts, POSIX paths, attributes, the Trash).

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

type cryptoRand struct{}

func (cryptoRand) Read(b []byte) (int, error) { return rand.Read(b) }

func syscallClose(fd int) { unix.Close(fd) }

// isolateXDG: the app's folders and the user's under t's temp dir.
func isolateXDG(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	t.Setenv("HOST_XDG_DATA_HOME", "")
	t.Setenv("HOST_XDG_CONFIG_HOME", "")
	t.Setenv("OBLIREACH_FAKE_FLATPAK", "")
	appKeyStore.profile = ""
	return root
}

// ── Secret Service protector ───────────────────────────────────────────────

func TestSecretServiceProtectorRoundTrip(t *testing.T) {
	for _, plainOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "dh", true: "plain"}[plainOnly], func(t *testing.T) {
			isolateXDG(t)
			b := startTestBus(t)
			ss := startFakeSS(t, b)
			ss.plainOnly = plainOnly
			p := ssProtector{purpose: ssPurposeVault, keys: &ssKeyStore{}}
			blob, err := p.Protect([]byte("inner secret"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(blob, ssBlobMagic) || bytes.Contains(blob, []byte("inner secret")) {
				t.Fatalf("blob not sealed: %q", blob)
			}
			var got []byte
			if err := p.Unprotect(blob, func(pl []byte) error { got = append([]byte(nil), pl...); return nil }); err != nil {
				t.Fatal(err)
			}
			if string(got) != "inner secret" {
				t.Fatalf("got %q", got)
			}
			// One key per purpose and profile: a second Protect reuses it.
			if _, err := p.Protect([]byte("again")); err != nil {
				t.Fatal(err)
			}
			if ss.count() != 1 {
				t.Fatalf("items = %d, want 1", ss.count())
			}
			for _, it := range ss.items {
				if it.attrs["app"] != ssAppID || it.attrs["purpose"] != ssPurposeVault || it.attrs["profile"] == "" ||
					len(it.attrs["key-id"]) != 32 || it.attrs["xdg:schema"] != ssSchema || len(it.secret) != ssKeyLen {
					t.Fatalf("item attrs %v (secret %d bytes)", it.attrs, len(it.secret))
				}
			}
			if plainOnly && ss.algos[0] != ssAlgoDH {
				t.Fatalf("DH not tried first: %v", ss.algos)
			}
			if !plainOnly {
				for _, a := range ss.algos {
					if a != ssAlgoDH {
						t.Fatalf("algos %v", a)
					}
				}
			}
			// Another purpose: another key; a tampered blob fails cleanly.
			p2 := ssProtector{purpose: ssPurposeSupport, keys: &ssKeyStore{}}
			if err := p2.Unprotect(blob, func([]byte) error { return nil }); err == nil {
				t.Fatal("a vault blob opened as a support blob")
			}
			bad := append([]byte(nil), blob...)
			bad[len(bad)-1] ^= 1
			if err := p.Unprotect(bad, func([]byte) error { return nil }); !errors.Is(err, errSSBlob) {
				t.Fatalf("tampered: %v", err)
			}
			// The key is found by its id even from another profile.
			moved := ssProtector{purpose: ssPurposeVault, keys: &ssKeyStore{profile: "elsewhere"}}
			if err := moved.Unprotect(blob, func([]byte) error { return nil }); err != nil {
				t.Fatalf("moved profile: %v", err)
			}
		})
	}
}

func TestSecretServiceLockedAndPrompt(t *testing.T) {
	isolateXDG(t)
	b := startTestBus(t)
	ss := startFakeSS(t, b)
	p := ssProtector{purpose: ssPurposeVault, keys: &ssKeyStore{}}
	blob, err := p.Protect([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	// Locked, the prompt dismissed: unavailable (not damaged).
	ss.locked, ss.dismiss = true, true
	err = p.Unprotect(blob, func([]byte) error { return nil })
	if !errors.Is(err, errKeyringUnavailable) {
		t.Fatalf("dismissed: %v", err)
	}
	// Locked, the prompt accepted: unlocked, opened.
	ss.dismiss = false
	if err := p.Unprotect(blob, func([]byte) error { return nil }); err != nil {
		t.Fatalf("accepted: %v", err)
	}
	if ss.prompts != 2 {
		t.Fatalf("prompts = %d", ss.prompts)
	}
}

func TestSecretServiceDefaultCollectionCreated(t *testing.T) {
	isolateXDG(t)
	b := startTestBus(t)
	ss := startFakeSS(t, b)
	ss.noDefault = true
	p := ssProtector{purpose: ssPurposeVault, keys: &ssKeyStore{}}
	if _, err := p.Protect([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if ss.prompts != 1 || ss.noDefault || ss.count() != 1 {
		t.Fatalf("prompts %d noDefault %v items %d", ss.prompts, ss.noDefault, ss.count())
	}
}

func TestSecretServiceMissing(t *testing.T) {
	isolateXDG(t)
	startTestBus(t) // nobody owns org.freedesktop.secrets
	p := ssProtector{purpose: ssPurposeVault, keys: &ssKeyStore{}}
	_, err := p.Protect([]byte("x"))
	if !errors.Is(err, errKeyringUnavailable) || !errors.Is(err, errKeyringMissing) {
		t.Fatalf("missing: %v", err)
	}
	if !strings.Contains(err.Error(), "org.freedesktop.secrets") {
		t.Fatalf("no guidance: %v", err)
	}
}

func TestSecretServiceDHKeyAgreement(t *testing.T) {
	a, apub, _ := ssDHKeyPair(rand.Reader)
	b, bpub, _ := ssDHKeyPair(rand.Reader)
	k1, err := ssDHAESKey(a, bpub)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := ssDHAESKey(b, apub)
	if !bytes.Equal(k1, k2) || len(k1) != 16 {
		t.Fatal("DH keys differ")
	}
	if _, err := ssDHAESKey(a, []byte{1}); err == nil {
		t.Fatal("a public value of 1 accepted")
	}
	s := &ssSession{path: "/s", aes: k1}
	for _, n := range []int{0, 1, 15, 16, 17, 32} {
		msg := bytes.Repeat([]byte{7}, n)
		sec, err := s.encode(msg, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		got, err := (&ssSession{path: "/s", aes: k2}).decode(sec)
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("n=%d: %v", n, err)
		}
	}
}

// TestSecretServiceRealKeyring: against a real Secret Service on the
// session bus (GNOME Keyring / KWallet), only when
// OBLIREACH_REAL_SECRET_SERVICE=1 (integration: dbus-run-session +
// gnome-keyring-daemon --unlock). It locks the collection at the end and
// expects "unavailable" (no prompter without a display), never "damaged".
func TestSecretServiceRealKeyring(t *testing.T) {
	if os.Getenv("OBLIREACH_REAL_SECRET_SERVICE") != "1" {
		t.Skip("OBLIREACH_REAL_SECRET_SERVICE=1 not set")
	}
	resetSessionBus()
	defer resetSessionBus()
	keys := &ssKeyStore{profile: "it-" + randHex(4)}
	p := ssProtector{purpose: ssPurposeVault, keys: keys}
	blob, err := p.Protect([]byte("real keyring"))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := p.Unprotect(blob, func(pl []byte) error { got = string(pl); return nil }); err != nil || got != "real keyring" {
		t.Fatalf("unprotect: %q %v", got, err)
	}
	c, err := ssDial()
	if err != nil {
		t.Fatal(err)
	}
	algo := "plain"
	if c.sess.aes != nil {
		algo = "dh"
	}
	t.Logf("transfer session: %s", algo)
	items, err := c.search(map[string]string{"app": ssAppID, "profile": keys.profile})
	if err != nil || len(items) != 1 {
		t.Fatalf("items %v %v", items, err)
	}
	attrs, _ := c.attributes(items[0])
	t.Logf("item %s attributes %v", items[0], attrs)
	// Lock the default collection: Unprotect must say "unavailable".
	coll, err := c.defaultCollection()
	if err != nil {
		t.Fatal(err)
	}
	var locked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := c.svc.Call(ssIface+".Lock", 0, []dbus.ObjectPath{coll}).Store(&locked, &prompt); err != nil {
		t.Fatal(err)
	}
	c.close()
	err = p.Unprotect(blob, func([]byte) error { return nil })
	t.Logf("after Lock: %v", err)
	if err != nil && !errors.Is(err, errKeyringUnavailable) {
		t.Fatalf("locked keyring reported as %v", err)
	}
	if err == nil {
		t.Log("the service unlocked again without a prompt (allowed)")
	}
}

// ── The vault on it ────────────────────────────────────────────────────────

func newLinuxTestVault(t *testing.T) *Vault {
	store, err := newFileVaultStore()
	if err != nil {
		t.Fatal(err)
	}
	v := NewVault(store, ssProtector{purpose: ssPurposeVault, keys: &ssKeyStore{}}, linuxMemSealer{})
	v.iterations = 1000
	return v
}

func TestLinuxVaultEndToEnd(t *testing.T) {
	root := isolateXDG(t)
	b := startTestBus(t)
	ss := startFakeSS(t, b)
	v := newLinuxTestVault(t)
	if err := v.Create("123456", 15); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".local", "share", "oblireach", "vault")
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("vault dir: %v %v", st.Mode(), err)
	}
	fst, err := os.Stat(filepath.Join(dir, vaultFileName))
	if err != nil || fst.Mode().Perm() != 0o600 {
		t.Fatalf("vault file: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, vaultFileName))
	format, blob, ok := parseOuter(raw)
	if !ok || format != 1 || !bytes.HasPrefix(blob, ssBlobMagic) {
		t.Fatalf("outer: format %d ok %v", format, ok)
	}
	v.Lock()
	// The keyring locked and its dialog dismissed: still locked, never
	// unreadable.
	ss.locked, ss.dismiss = true, true
	if err := v.Unlock("123456"); !errors.Is(err, errKeyringUnavailable) {
		t.Fatalf("unlock with a locked keyring: %v", err)
	}
	if vaultErrCode(errKeyringUnavailable) != "keyring_unavailable" {
		t.Fatal("code")
	}
	if s := v.State(); s.State != "locked" {
		t.Fatalf("state %q", s.State)
	}
	ss.dismiss = false
	if err := v.Unlock("000000"); !errors.Is(err, errWrongCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := v.Unlock("123456"); err != nil {
		t.Fatal(err)
	}
	if s := v.State(); s.State != "unlocked" {
		t.Fatalf("state %q", s.State)
	}
	// A fresh process (new Vault, new memory key) opens the same file.
	v.Lock()
	v2 := newLinuxTestVault(t)
	if err := v2.Unlock("123456"); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxVaultCreateWithoutKeyring(t *testing.T) {
	isolateXDG(t)
	startTestBus(t)
	v := newLinuxTestVault(t)
	if err := v.Create("123456", 15); !errors.Is(err, errKeyringUnavailable) {
		t.Fatalf("create: %v", err)
	}
	if s := v.State(); s.State != "absent" {
		t.Fatalf("state %q", s.State)
	}
}

func TestLinuxPresenceFlag(t *testing.T) {
	root := isolateXDG(t)
	b := startTestBus(t)
	startFakeSS(t, b)
	f := filePresenceFlag{path: filepath.Join(root, "hello"), prot: ssProtector{purpose: ssPurposePresence, keys: &ssKeyStore{}}}
	if f.Enabled() {
		t.Fatal("on before set")
	}
	if err := f.Set(true); err != nil {
		t.Fatal(err)
	}
	if !f.Enabled() {
		t.Fatal("off after set")
	}
	os.WriteFile(f.path, []byte("forged"), 0o600)
	if f.Enabled() {
		t.Fatal("a forged marker is on")
	}
	f.Set(false)
	if f.Enabled() {
		t.Fatal("on after unset")
	}
}

func TestLinuxSupportStore(t *testing.T) {
	isolateXDG(t)
	b := startTestBus(t)
	startFakeSS(t, b)
	s := &fileSupportStore{dir: vaultBaseDir(), prot: ssProtector{purpose: ssPurposeSupport, keys: &ssKeyStore{}}}
	if got, err := s.Load(); err != nil || got != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	if err := s.Save([]byte(`{"servers":[]}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.path())
	if bytes.Contains(raw, []byte("servers")) {
		t.Fatal("support.dat in clear")
	}
	got, err := s.Load()
	if err != nil || string(got) != `{"servers":[]}` {
		t.Fatalf("load: %q %v", got, err)
	}
}

func TestLinuxMemSealer(t *testing.T) {
	var s linuxMemSealer
	for _, msg := range [][]byte{nil, []byte("p"), bytes.Repeat([]byte("x"), 1000)} {
		sealed, err := s.Seal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if len(msg) > 8 && bytes.Contains(sealed, msg) { // (a short one may occur by chance)
			t.Fatal("not sealed")
		}
		var got []byte
		if err := s.Open(sealed, func(p []byte) error { got = append([]byte(nil), p...); return nil }); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatal("round trip")
		}
		sealed[len(sealed)-1] ^= 1
		if s.Open(sealed, func([]byte) error { return nil }) == nil {
			t.Fatal("damaged sealed opened")
		}
	}
}

// ── Single instance ────────────────────────────────────────────────────────

func TestLinuxSingleInstance(t *testing.T) {
	b := startTestBus(t)
	first := b.mustConnect(t)
	second := b.mustConnect(t)
	if !claimOn(first, nil, time.Now) {
		t.Fatal("the first launch did not get the name")
	}
	got := make(chan instanceRequest, 4)
	setInstanceHandler(func(r instanceRequest) uintptr { got <- r; return singleInstanceAckShown })
	defer setInstanceHandler(nil)
	t.Setenv("XDG_ACTIVATION_TOKEN", "tok-1")
	if claimOn(second, []string{"oblireach://connect/abc", "--foo"}, time.Now) {
		t.Fatal("a second launch ran")
	}
	select {
	case r := <-got:
		if len(r.URIs) != 1 || r.URIs[0] != "oblireach://connect/abc" || r.Token != "tok-1" || r.Quiet {
			t.Fatalf("request %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no activation")
	}
	// freedesktop Application.Open reaches the same handler.
	if err := second.Object(instanceBusName, instancePath).Call(fdoAppIface+".Open", 0, []string{"file:///tmp/x"}, map[string]dbus.Variant{}).Err; err != nil {
		t.Fatal(err)
	}
	if r := <-got; len(r.URIs) != 1 || r.URIs[0] != "file:///tmp/x" {
		t.Fatalf("open %+v", r)
	}
	// No window yet (no handler): the launch waits, then ends (bounded).
	setInstanceHandler(nil)
	fake := time.Now()
	clock := func() time.Time { fake = fake.Add(10 * time.Second); return fake }
	if claimOn(second, nil, clock) {
		t.Fatal("ran while the first instance holds the name")
	}
	// The first one quits: the name is free again.
	first.ReleaseName(instanceBusName)
	if !claimOn(second, nil, time.Now) {
		t.Fatal("the name was not taken over")
	}
}

func TestLaunchURIs(t *testing.T) {
	got := launchURIs([]string{"--minimized", "https://x/y", "C:\\x", "plain", "oblireach:abc", "/abs"})
	if strings.Join(got, ",") != "https://x/y,oblireach:abc" {
		t.Fatalf("%v", got)
	}
}

// ── Portals ────────────────────────────────────────────────────────────────

func TestFileChooserPortal(t *testing.T) {
	b := startTestBus(t)
	p := startFakePortal(t, b)
	dir := t.TempDir()
	p.answer(0, map[string]dbus.Variant{"uris": dbus.MakeVariant([]string{"file://" + filepath.Join(dir, "a%20b.ini")})})
	path, ok := platformDialogs.pickFile(0, false, "Pick", "WinSCP.ini (*.ini)\x00*.ini\x00*.*\x00*.*\x00", dir, "", "")
	if !ok || path != filepath.Join(dir, "a b.ini") {
		t.Fatalf("pick: %q %v", path, ok)
	}
	c := p.last()
	if c.method != "OpenFile" || c.title != "Pick" {
		t.Fatalf("call %+v", c)
	}
	specs, _ := c.options["filters"].Value().([][]interface{})
	if len(specs) != 2 {
		t.Fatalf("filters %#v", c.options["filters"].Value())
	}
	if folder, _ := c.options["current_folder"].Value().([]byte); string(folder) != dir+"\x00" {
		t.Fatalf("folder %q", folder)
	}
	if m, _ := c.options["multiple"].Value().(bool); m {
		t.Fatal("multiple")
	}
	// Save: the suggested name, the extension added.
	p.answer(0, map[string]dbus.Variant{"uris": dbus.MakeVariant([]string{"file://" + filepath.Join(dir, "out")})})
	path, ok = platformDialogs.pickFile(0, true, "Save", "Oblireach (*.json)\x00*.json\x00", dir, "conn.json", "json")
	if !ok || path != filepath.Join(dir, "out.json") {
		t.Fatalf("save: %q %v", path, ok)
	}
	if n, _ := p.last().options["current_name"].Value().(string); n != "conn.json" || p.last().method != "SaveFile" {
		t.Fatalf("save call %+v", p.last())
	}
	// Multiple.
	p.answer(0, map[string]dbus.Variant{"uris": dbus.MakeVariant([]string{"file:///a", "file:///b", "https://x"})})
	paths, err := chooseFiles(fileChooserOpts{Title: "Many", Multiple: true})
	if err != nil || strings.Join(paths, ",") != "/a,/b" {
		t.Fatalf("multiple: %v %v", paths, err)
	}
	// Cancelled.
	p.answer(1, map[string]dbus.Variant{})
	if _, ok := platformDialogs.pickFile(0, false, "Pick", "", "", "", ""); ok {
		t.Fatal("cancel picked")
	}
}

func TestPortalAbsent(t *testing.T) {
	startTestBus(t)
	_, err := chooseFiles(fileChooserOpts{Title: "x"})
	if !errors.Is(err, errPortalAbsent) {
		t.Fatalf("absent: %v", err)
	}
}

func TestPortalTimeoutClosesRequest(t *testing.T) {
	b := startTestBus(t)
	p := startFakePortal(t, b)
	p.silent = true
	_, err := portalCall(portalFileChooser, "OpenFile", 200*time.Millisecond, nil, "", "x")
	if !errors.Is(err, errPortalCancel) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestParseWinFilter(t *testing.T) {
	f := parseWinFilter("1Remote (*.db, *.json)\x00*.db;*.json\x00*.*\x00*.*\x00")
	if len(f) != 2 || f[0].Label != "1Remote (*.db, *.json)" || strings.Join(f[0].Patterns, ",") != "*.db,*.json" || f[1].Patterns[0] != "*" {
		t.Fatalf("%+v", f)
	}
}

func TestOpenURIPortal(t *testing.T) {
	b := startTestBus(t)
	p := startFakePortal(t, b)
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	os.WriteFile(file, []byte("x"), 0o644)
	if err := openURIFile(0, file, true, false); err != nil {
		t.Fatal(err)
	}
	c := p.last()
	w, _ := c.options["writable"].Value().(bool)
	ask, _ := c.options["ask"].Value().(bool)
	if c.method != "OpenFile" || c.fdPath != file || !w || ask {
		t.Fatalf("OpenFile %+v", c)
	}
	if err := (linuxSys{}).reveal(file); err != nil {
		t.Fatal(err)
	}
	if c := p.last(); c.method != "OpenDirectory" || c.fdPath != file {
		t.Fatalf("OpenDirectory %+v", c)
	}
}

func TestEditorLaunchPolicy(t *testing.T) {
	cases := []struct {
		file, editor string
		ask          bool
	}{
		{"a.txt", "notepad", false}, {"a.json", "notepad", false}, {"a.sh", "notepad", true}, {"noext", "notepad", true},
		{"a.docx", "notepad", true}, {"a.docx", "system", false}, {"a.py", "system", true}, {"a.desktop", "system", true},
		{"a.html", "system", true}, {"a.txt", "custom", true}, {"", "system", true},
	}
	for _, c := range cases {
		if got := editAsk(c.file, c.editor); got != c.ask {
			t.Errorf("editAsk(%q, %q) = %v", c.file, c.editor, got)
		}
	}
	var calls []string
	old := editOpenURI
	defer func() { editOpenURI = old }()
	editOpenURI = func(owner uintptr, path string, writable, ask bool) error {
		calls = append(calls, path+"|"+map[bool]string{true: "w", false: "r"}[writable]+map[bool]string{true: "ask", false: ""}[ask])
		return nil
	}
	r, err := launchEditor(&editSession{file: "/tmp/w/run.sh"}, FilesSettings{Editor: "system"})
	if err != nil || r.Editor != "system" || calls[0] != "/tmp/w/run.sh|wask" {
		t.Fatalf("%v %v %v", r, err, calls)
	}
	editOpenURI = func(uintptr, string, bool, bool) error { return errPortalCancel }
	if _, err := launchEditor(&editSession{file: "/tmp/w/a.txt"}, FilesSettings{}); fsCodeOf(err) != "cancelled" {
		t.Fatalf("cancel: %v", err)
	}
}

func TestNotificationPortal(t *testing.T) {
	b := startTestBus(t)
	p := startFakePortal(t, b)
	tray := &appTray{}
	if !tray.balloon("Alice", "hello\nthere") {
		t.Fatal("not shown")
	}
	c := p.last()
	if c.method != "AddNotification" || !strings.HasPrefix(c.id, "oblireach-chat-") {
		t.Fatalf("%+v", c)
	}
	if title, _ := c.notification["title"].Value().(string); title != "Alice" {
		t.Fatalf("title %q", title)
	}
	tray.inFront = func() bool { return true }
	if tray.balloon("x", "y") {
		t.Fatal("shown over the window in front")
	}
	// An action through the portal's signal reaches the handler.
	done := make(chan string, 1)
	notifyMu.Lock()
	notificationActivated = func(id, action string) { done <- id + "/" + action }
	notifyMu.Unlock()
	defer func() { notifyMu.Lock(); notificationActivated = nil; notifyMu.Unlock() }()
	p.conn.Emit(portalPath, portalNotification+".ActionInvoked", ssAppID, c.id, "app.open", []dbus.Variant{})
	select {
	case got := <-done:
		if got != c.id+"/open" {
			t.Fatalf("action %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no action")
	}
}

func TestAutostartBackgroundPortal(t *testing.T) {
	root := isolateXDG(t)
	b := startTestBus(t)
	p := startFakePortal(t, b)
	t.Setenv("OBLIREACH_FAKE_FLATPAK", "1")
	minimized, always, tray := false, false, false
	c := &startupCtl{exe: "/app/bin/oblireach", file: filepath.Join(root, ".config", "autostart", ssAppID+".desktop"),
		backend: portalAutostart{}, prefs: func() (bool, bool) { return minimized, always },
		setPrefs: func(m, a bool) { minimized, always = m, a }, tray: func() bool { return tray }, setTray: func(on bool) { tray = on }}
	p.answer(0, map[string]dbus.Variant{"background": dbus.MakeVariant(true), "autostart": dbus.MakeVariant(true)})
	if _, err := c.set("runAtLogin", true); err != nil {
		t.Fatal(err)
	}
	call := p.last()
	cmd, _ := call.options["commandline"].Value().([]string)
	auto, _ := call.options["autostart"].Value().(bool)
	if call.method != "RequestBackground" || !auto || strings.Join(cmd, " ") != "oblireach" {
		t.Fatalf("%+v %v", call, cmd)
	}
	// The portal wrote the file (simulated): state follows it.
	os.MkdirAll(filepath.Dir(c.file), 0o755)
	os.WriteFile(c.file, []byte("[Desktop Entry]\nExec=flatpak run tools.obli.Oblireach\n"), 0o644)
	st, _ := c.state()
	if !st.RunAtLogin || st.DisabledInTM {
		t.Fatalf("state %+v", st)
	}
	// Start minimized re-asks with --minimized.
	if _, err := c.set("minimized", true); err != nil {
		t.Fatal(err)
	}
	cmd, _ = p.last().options["commandline"].Value().([]string)
	if strings.Join(cmd, " ") != "oblireach --minimized" || !minimized {
		t.Fatalf("minimized: %v", cmd)
	}
	// Disabled from the desktop's settings.
	os.WriteFile(c.file, []byte("[Desktop Entry]\nExec=x\nX-GNOME-Autostart-enabled=false\n"), 0o644)
	if st, _ := c.state(); st.RunAtLogin || !st.DisabledInTM {
		t.Fatalf("disabled %+v", st)
	}
	if _, err := c.set("bogus", true); !errors.Is(err, errBadArgument) {
		t.Fatal("bogus accepted")
	}
}

func TestAutostartFileOutsideSandbox(t *testing.T) {
	root := isolateXDG(t)
	path := filepath.Join(root, ".config", "autostart", ssAppID+".desktop")
	f := fileAutostart{path: path, exe: "/opt/my apps/oblireach"}
	if on, err := f.set(true, []string{"/opt/my apps/oblireach", "--minimized"}); err != nil || !on {
		t.Fatal(err)
	}
	present, enabled, minimized := readAutostart(path)
	b, _ := os.ReadFile(path)
	if !present || !enabled || !minimized || !strings.Contains(string(b), `Exec="/opt/my apps/oblireach" --minimized`) {
		t.Fatalf("%v %v %v\n%s", present, enabled, minimized, b)
	}
	if on, err := f.set(false, nil); err != nil || on {
		t.Fatal(err)
	}
	if present, _, _ := readAutostart(path); present {
		t.Fatal("still there")
	}
}

// ── Local files ────────────────────────────────────────────────────────────

const testMountInfo = `22 1 0:21 / / rw,relatime shared:1 - btrfs /dev/nvme0n1p3 rw,compress=zstd:1
23 22 0:5 / /proc rw,nosuid - proc proc rw
60 22 0:21 /home /var/home rw,relatime shared:2 - btrfs /dev/nvme0n1p3 rw
90 22 8:17 / /run/media/alice/USB\040KEY rw,nosuid shared:50 - exfat /dev/sdb1 rw
91 22 0:60 / /mnt/nas rw - cifs //nas/share rw
92 22 7:0 / /run/media/alice/DVD ro - iso9660 /dev/sr0 ro
93 22 0:61 / /media/data rw - ext4 /dev/sdc1 rw
94 22 0:62 / /run/user/1000/doc rw - fuse.portal portal rw
`

func TestMountInfo(t *testing.T) {
	t.Setenv("USER", "alice")
	ms := parseMountInfo(testMountInfo)
	if len(ms) != 8 || ms[3].point != "/run/media/alice/USB KEY" || ms[3].fstype != "exfat" || ms[4].source != "//nas/share" {
		t.Fatalf("%+v", ms)
	}
	vols := volumeMounts(ms)
	var got []string
	for _, v := range vols {
		got = append(got, v.point+"="+driveKindOf(v))
	}
	want := "/media/data=removable,/mnt/nas=network,/run/media/alice/DVD=cdrom,/run/media/alice/USB KEY=removable"
	if strings.Join(got, ",") != want {
		t.Fatalf("%v", got)
	}
	m, _ := mountOf(ms, "/run/media/alice/USB KEY/dir/f")
	if fsDriveType(m) != localDriveFixed {
		t.Fatal("usb not trashable")
	}
	m, _ = mountOf(ms, "/mnt/nas/x")
	if fsDriveType(m) != driveRemote {
		t.Fatal("nas")
	}
	m, _ = mountOf(ms, "/var/home/alice/x")
	if m.point != "/var/home" {
		t.Fatalf("home mount %q", m.point)
	}
}

func TestLinuxPathRules(t *testing.T) {
	ok := []string{"/", "/home/a", "/home/a/b c/d:e?.txt", "/x/./y"}
	for _, p := range ok[:3] {
		if _, err := localPathOK(p); err != nil {
			t.Errorf("%q refused", p)
		}
	}
	for _, p := range []string{"", "rel/x", "C:\\x", "/a/../b", "/a/./b", "/a\x00b", "/a\nb", "/" + strings.Repeat("x", 256)} {
		if _, err := localPathOK(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
	if !pathUnder("/home/a/b", "/home/a") || pathUnder("/home/ab", "/home/a") || pathUnder("/HOME/a", "/home/a") || !pathUnder("/x", "/") {
		t.Fatal("pathUnder")
	}
	if !localExistingNameOK("a:b?.") || localExistingNameOK("a/b") || localExistingNameOK("..") {
		t.Fatal("existing names")
	}
	if !isDriveRoot("/") || isDriveRoot("/home") {
		t.Fatal("isDriveRoot")
	}
}

func TestUserDirs(t *testing.T) {
	d := parseUserDirs("# x\nXDG_DESKTOP_DIR=\"$HOME/Bureau\"\nXDG_DOWNLOAD_DIR=\"/data/dl\"\nXDG_MUSIC_DIR=\"$HOME/\"\nXDG_BAD=\"x\"\nXDG_VIDEOS_DIR=\"rel\"\n", "/home/a")
	if d["DESKTOP"] != "/home/a/Bureau" || d["DOWNLOAD"] != "/data/dl" || d["MUSIC"] != "/home/a" || len(d) != 3 {
		t.Fatalf("%v", d)
	}
}

func TestLinuxEntries(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("12345"), 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "ro"), nil, 0o444)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.Symlink("sub", filepath.Join(dir, "lnk"))
	os.Symlink("missing", filepath.Join(dir, "dangling"))
	unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600)
	es, err := (linuxSys{}).readDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]localEntry{}
	for _, e := range es {
		by[e.Name] = e
	}
	if by["f.txt"].Size != 5 || by["f.txt"].IsDir || by["f.txt"].Hidden {
		t.Fatalf("f %+v", by["f.txt"])
	}
	if !by[".hidden"].Hidden || !by["sub"].IsDir || !by["lnk"].IsDir || !by["lnk"].Link || !by["dangling"].Link || !by["fifo"].System {
		t.Fatalf("%+v", by)
	}
	if os.Getuid() != 0 && !by["ro"].ReadOnly {
		t.Fatal("ro")
	}
	if fp, _ := (linuxSys{}).finalPath(filepath.Join(dir, "lnk")); fp != filepath.Join(dir, "sub") {
		t.Fatalf("final %q", fp)
	}
	err = (linuxSys{}).moveNoReplace(filepath.Join(dir, "f.txt"), filepath.Join(dir, "ro"))
	if fsCodeOf(err) != "exists" {
		t.Fatalf("no replace: %v", err)
	}
	if err := (linuxSys{}).moveNoReplace(filepath.Join(dir, "f.txt"), filepath.Join(dir, "g.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestTrashHome(t *testing.T) {
	root := isolateXDG(t)
	t.Setenv("HOST_XDG_DATA_HOME", filepath.Join(root, "hostdata"))
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	f := filepath.Join(work, "a b%.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	when := time.Date(2026, 10, 5, 14, 3, 9, 0, time.Local)
	if err := trashPath(f, when); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(root, "hostdata", "Trash")
	if _, err := os.Stat(filepath.Join(trash, "files", "a b%.txt")); err != nil {
		t.Fatal("not in files/")
	}
	info, _ := os.ReadFile(filepath.Join(trash, "info", "a b%.txt.trashinfo"))
	want := "[Trash Info]\nPath=" + trashEscape(f) + "\nDeletionDate=2026-10-05T14:03:09\n"
	if string(info) != want || !strings.Contains(string(info), "a%20b%25.txt") {
		t.Fatalf("info %q", info)
	}
	// The same name again: a second slot.
	os.WriteFile(f, []byte("y"), 0o644)
	if err := trashPath(f, when); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(trash, "info", "a b%.txt.2.trashinfo")); err != nil {
		t.Fatal("no second slot")
	}
	if st, _ := os.Stat(trash); st.Mode().Perm() != 0o700 {
		t.Fatalf("trash mode %v", st.Mode())
	}
	// The Trash itself (or a folder holding it) is never trashed.
	if err := trashPath(filepath.Join(root, "hostdata"), when); fsCodeOf(err) != "protected" {
		t.Fatalf("trash in trash: %v", err)
	}
}

func TestTrashTopdir(t *testing.T) {
	root := isolateXDG(t)
	vol := filepath.Join(root, "vol")
	os.MkdirAll(filepath.Join(vol, "d"), 0o755)
	mi := filepath.Join(root, "mountinfo")
	os.WriteFile(mi, []byte("1 0 0:1 / / rw - ext4 /dev/a rw\n2 1 0:2 / "+vol+" rw - exfat /dev/b rw\n"), 0o644)
	oldMI, oldSame := mountInfoPath, trashSameDevice
	mountInfoPath = mi
	trashSameDevice = func(a, b string) bool { return false } // vol is "another volume"
	defer func() { mountInfoPath, trashSameDevice = oldMI, oldSame }()
	f := filepath.Join(vol, "d", "x.bin")
	os.WriteFile(f, nil, 0o644)
	if err := trashPath(f, time.Now()); err != nil {
		t.Fatal(err)
	}
	uid := strconv.Itoa(os.Getuid())
	info, err := os.ReadFile(filepath.Join(vol, ".Trash-"+uid, "info", "x.bin.trashinfo"))
	if err != nil || !strings.Contains(string(info), "\nPath=d/x.bin\n") {
		t.Fatalf("topdir info %q %v", info, err)
	}
	// An administrator's sticky .Trash: $topdir/.Trash/$uid.
	os.Mkdir(filepath.Join(vol, ".Trash"), 0o777)
	os.Chmod(filepath.Join(vol, ".Trash"), 0o777|os.ModeSticky)
	g := filepath.Join(vol, "y.bin")
	os.WriteFile(g, nil, 0o644)
	if err := trashPath(g, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vol, ".Trash", uid, "files", "y.bin")); err != nil {
		t.Fatal("not in the shared .Trash")
	}
	// A volume root itself: never.
	if err := trashPath(vol, time.Now()); fsCodeOf(err) != "no_recycle" {
		t.Fatalf("volume root: %v", err)
	}
}

func TestLinuxLocalFS(t *testing.T) {
	root := isolateXDG(t)
	t.Setenv("HOST_XDG_DATA_HOME", filepath.Join(root, "hostdata"))
	work := filepath.Join(root, "Documents", "w")
	os.MkdirAll(work, 0o755)
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("hi"), 0o644)
	l := newLocalFS(nil)
	res, err := l.list(work, fsListOpts{})
	if err != nil || len(res.Entries) != 1 || res.Parent != filepath.Join(root, "Documents") {
		t.Fatalf("list %+v %v", res, err)
	}
	// Protected: the shell profile, ~/.config, the app's own data.
	for _, p := range []string{filepath.Join(root, ".bashrc"), filepath.Join(root, ".config", "autostart", "x.desktop"),
		filepath.Join(root, ".local", "share", "applications", "x.desktop"), "/etc/passwd", "/usr/bin/ls"} {
		if !l.writeProtected(p) {
			t.Errorf("%s writable", p)
		}
	}
	if !l.readProtected(filepath.Join(root, ".local", "share", "oblireach", "vault", "vault.dat")) {
		t.Error("the vault readable from the pane")
	}
	if l.writeProtected(filepath.Join(root, "Documents", "x")) || l.writeProtected("/var/home/alice/x") || l.writeProtected("/run/media/alice/USB/x") {
		t.Error("ordinary folders protected")
	}
	if !l.keptRoot(root) || !l.keptRoot("/") {
		t.Error("home / root not kept")
	}
	if _, err := l.rename(filepath.Join(work, "a.txt"), "b:c.txt"); err == nil {
		// (names this app creates stay portable: ':' refused)
		t.Error("a non-portable new name accepted")
	}
	if r, err := l.rename(filepath.Join(work, "a.txt"), "b.txt"); err != nil || r.Path != filepath.Join(work, "b.txt") {
		t.Fatalf("rename %v %v", r, err)
	}
	tr, err := l.trash([]string{filepath.Join(work, "b.txt")})
	if err != nil || tr.Trashed != 1 {
		t.Fatalf("trash %v %v", tr, err)
	}
	if _, err := os.Stat(filepath.Join(root, "hostdata", "Trash", "files", "b.txt")); err != nil {
		t.Fatal("not trashed")
	}
	if _, err := l.trash([]string{root}); fsCodeOf(err) != "protected" {
		t.Fatalf("home trashed: %v", err)
	}
	r := (linuxSys{}).roots()
	if r.Home != root || len(r.Places) == 0 || r.Places[0].ID != "home" {
		t.Fatalf("roots %+v", r)
	}
}
