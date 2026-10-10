//go:build windows

package main

// vault_bridge_windows_test.go — the WebView bindings, using syncHost (the
// fake bridgeHost defined in localfs_bridge_test.go, same package) and a
// fresh in-memory Vault per test via testVaultOverride so tests never touch
// the real DPAPI file or leak state into each other.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func newTestVaultForBridge(t *testing.T) *Vault {
	t.Helper()
	store := &fakeStore{}
	v := NewVault(store, &fakeProtector{}, fakeSealer{})
	v.iterations = 2
	testVaultOverride = v
	t.Cleanup(func() { testVaultOverride = nil })
	return v
}

func TestVaultBindingsMainWindowOnly(t *testing.T) {
	mainOnly := []string{"reachVaultList", "reachVaultSave", "reachVaultDelete", "reachVaultReveal", "reachVaultCredentials"}
	// (the local connections' descriptions, never a secret, and their
	// terminal and explorer: a tab of one moves to any window)
	everyWindow := []string{"reachVaultState", "reachVaultForSession", "reachVaultUnlock", "reachVaultCreate", "reachVaultLock", "reachVaultType", "reachVaultTypeVm",
		"reachVaultByParent", "reachVaultConnInfo", "reachConnProbe", "reachSshConnect", "reachSshCwd", "reachSftpConnect", "reachSftpCmd", "reachSftpClose"}

	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321") // no .windows: a session window
	b.bind()
	have := map[string]bool{}
	for _, n := range h.binds {
		have[n] = true
	}
	for _, n := range mainOnly {
		if have[n] {
			t.Errorf("%s bound on a session-window bridge (main.windows == nil)", n)
		}
	}
	for _, n := range everyWindow {
		if !have[n] {
			t.Errorf("%s missing on a session-window bridge", n)
		}
	}

	h2 := &syncHost{}
	m, _ := newNativeBridge(h2, "http://127.0.0.1:4321")
	m.windows = &sessionWindows{} // marks it as the main window (see native_windows.go bind())
	m.bind()
	have2 := map[string]bool{}
	for _, n := range h2.binds {
		have2[n] = true
	}
	for _, n := range append(append([]string{}, mainOnly...), everyWindow...) {
		if !have2[n] {
			t.Errorf("main window lacks %s", n)
		}
	}
}

func TestVaultBindingsRequireToken(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	newTestVaultForBridge(t)
	if _, err := b.vaultState("wrong-token"); err != errNotAllowed {
		t.Fatalf("vaultState with a wrong token: %v, want errNotAllowed", err)
	}
	if ok, err := b.vaultUnlock("wrong-token", "aaaaaaaa", "code"); ok || err != errNotAllowed {
		t.Fatalf("vaultUnlock with a wrong token: %v %v, want false, errNotAllowed", ok, err)
	}
}

func TestVaultBindingsEndToEnd(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.windows = &sessionWindows{}
	b.tunnels = newReachTunnels()
	v := newTestVaultForBridge(t)

	// Absent → create.
	st, err := b.vaultState(b.token)
	if err != nil || st.State != "absent" {
		t.Fatalf("initial state = %+v, %v", st, err)
	}
	ok, err := b.vaultCreateWithCode(b.token, "aaaaaaaa", "secret1")
	if !ok || err != nil {
		t.Fatalf("vaultCreateWithCode: %v %v", ok, err)
	}
	evals := h.waitEvals(t, 1)
	if !strings.Contains(evals[0], `"ok":true`) {
		t.Fatalf("create result eval: %v", evals)
	}

	// Save an entry (main-window binding) scoped to a device tenant.
	in := vaultSaveInput{Origin: "https://a.example", TenantID: 5, Label: "Domain admin", Username: "adm", Secret: "hunter2", SecureOnly: false}
	if ok, err := b.vaultSave(b.token, "bbbbbbbb", in); !ok || err != nil {
		t.Fatalf("vaultSave: %v %v", ok, err)
	}
	evals = h.waitEvals(t, 2)
	var saveResult struct {
		ReqID string          `json:"reqID"`
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
	}
	if err := parsePushed(evals[1], &saveResult); err != nil || !saveResult.OK {
		t.Fatalf("save push: %v (%v)", evals[1], err)
	}
	var idOut struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(saveResult.Data, &idOut)
	if idOut.ID == "" {
		t.Fatalf("expected a non-empty entry id, got %v", evals[1])
	}

	// Register a live tunnel for a session token in that same scope and type
	// the saved secret into it.
	b.tunnels.register("sess-tok", "https://a.example", 5, 99)
	tun, _, ok2 := b.tunnels.lookup("sess-tok")
	if !ok2 {
		t.Fatalf("tunnel not registered")
	}
	_ = tun
	fakeInj := newWSInjector(&lockedBuffer{})
	b.tunnels.attach("sess-tok", fakeInj)

	gen := 1
	if ok, err := b.vaultType(b.token, "cccccccc", "sess-tok", idOut.ID, false, false, &gen, false); !ok || err != nil {
		t.Fatalf("vaultType: %v %v", ok, err)
	}
	evals = h.waitEvals(t, 3)
	var typeResult struct {
		OK bool `json:"ok"`
	}
	if err := parsePushed(evals[2], &typeResult); err != nil || !typeResult.OK {
		t.Fatalf("type push: %v (%v)", evals[2], err)
	}
	// The push (and every eval before it) must never contain the secret.
	for i, e := range evals {
		if strings.Contains(e, "hunter2") {
			t.Fatalf("eval %d leaked the secret: %v", i, e)
		}
	}

	// Wrong scope (different tenant) must be refused.
	b.tunnels.register("other-tok", "https://a.example", 999, 100)
	fakeInj2 := newWSInjector(&lockedBuffer{})
	b.tunnels.attach("other-tok", fakeInj2)
	if ok, err := b.vaultType(b.token, "dddddddd", "other-tok", idOut.ID, false, false, &gen, false); !ok || err != nil {
		t.Fatalf("vaultType (wrong scope, accept): %v %v", ok, err)
	}
	evals = h.waitEvals(t, 4)
	var scopeResult struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := parsePushed(evals[3], &scopeResult); err != nil || scopeResult.OK {
		t.Fatalf("expected the wrong-scope type to fail: %v", evals[3])
	}

	// Reveal requires the code again, even though the vault is unlocked.
	if ok, err := b.vaultReveal(b.token, "eeeeeeee", idOut.ID, "wrong"); !ok || err != nil {
		t.Fatalf("vaultReveal call: %v %v", ok, err)
	}
	evals = h.waitEvals(t, 5)
	var revealFail struct{ OK bool }
	if err := parsePushed(evals[4], &revealFail); err != nil || revealFail.OK {
		t.Fatalf("expected wrong-code reveal to fail: %v", evals[4])
	}
	if ok, err := b.vaultReveal(b.token, "ffffffff", idOut.ID, "secret1"); !ok || err != nil {
		t.Fatalf("vaultReveal call: %v %v", ok, err)
	}
	evals = h.waitEvals(t, 6)
	var revealOK struct {
		OK   bool
		Data struct{ Secret string }
	}
	if err := parsePushed(evals[5], &revealOK); err != nil || !revealOK.OK || revealOK.Data.Secret != "hunter2" {
		t.Fatalf("expected reveal with the right code to return the secret: %v (%v)", evals[5], err)
	}

	// Delete.
	if ok, err := b.vaultDelete(b.token, idOut.ID); !ok || err != nil {
		t.Fatalf("vaultDelete: %v %v", ok, err)
	}
	if len(v.List()) != 0 {
		t.Fatalf("expected no entries after delete")
	}
}

// TestVaultByParentLocalTree covers the "local" server profile's folder/ssh
// connection tree: reachVaultByParent must only ever see folder/ssh/rdp
// entries (never a password), and reachVaultList must never see them back.
func TestVaultByParentLocalTree(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.windows = &sessionWindows{}
	v := newTestVaultForBridge(t)

	if _, err := b.vaultCreateWithCode(b.token, "aaaaaaaa", "secret1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	h.waitEvals(t, 1)

	// A password entry (unrelated scope) must never show up in the tree.
	if _, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "pwd", Secret: "s"}); err != nil {
		t.Fatalf("seed password: %v", err)
	}

	// Root folder.
	folderIn := vaultSaveInput{Label: "Prod", Kind: vaultKindFolder}
	if ok, err := b.vaultSave(b.token, "bbbbbbbb", folderIn); !ok || err != nil {
		t.Fatalf("vaultSave folder: %v %v", ok, err)
	}
	var folderResult struct {
		OK   bool
		Data struct{ ID string }
	}
	if err := parsePushed(h.waitEvals(t, 2)[1], &folderResult); err != nil || !folderResult.OK {
		t.Fatalf("folder save push: %v", err)
	}
	folderID := folderResult.Data.ID

	// An SSH connection inside that folder.
	sshIn := vaultSaveInput{Label: "db1", Kind: vaultKindSSH, ParentID: folderID, Host: "10.0.0.5", Port: 22, Secret: "hunter2"}
	if ok, err := b.vaultSave(b.token, "cccccccc", sshIn); !ok || err != nil {
		t.Fatalf("vaultSave ssh: %v %v", ok, err)
	}
	var sshResult struct {
		OK   bool
		Data struct{ ID string }
	}
	if err := parsePushed(h.waitEvals(t, 3)[2], &sshResult); err != nil || !sshResult.OK {
		t.Fatalf("ssh save push: %v", err)
	}

	// Root lists only the folder.
	root, err := b.vaultByParent(b.token, "")
	if err != nil || len(root) != 1 || root[0].ID != folderID || root[0].Kind != vaultKindFolder {
		t.Fatalf("root children = %+v, %v", root, err)
	}

	// The folder lists only the ssh connection, with its host/port, never a secret field.
	children, err := b.vaultByParent(b.token, folderID)
	if err != nil || len(children) != 1 || children[0].Kind != vaultKindSSH || children[0].Host != "10.0.0.5" || children[0].Port != 22 {
		t.Fatalf("folder children = %+v, %v", children, err)
	}

	// reachVaultList (passwords, always vaultKindPassword — Vault.List filters
	// on it) must never include the folder/ssh nodes just created.
	pwList, err := b.vaultList(b.token)
	if err != nil {
		t.Fatalf("vaultList: %v", err)
	}
	for _, e := range pwList {
		if e.Label == "Prod" || e.Label == "db1" {
			t.Fatalf("vaultList leaked a local connection-tree entry: %+v", e)
		}
	}

	// Deleting a non-empty folder is refused.
	if ok, err := b.vaultDelete(b.token, folderID); ok || err != errFolderNotEmpty {
		t.Fatalf("expected errFolderNotEmpty, got %v %v", ok, err)
	}
}

var errPushedNotFound = errors.New("pushed vault event not found in eval string")

// parsePushed extracts the {"type","reqID","ok","data"} JSON object from one
// of evalVault's window.__reachVaultEvent(...) call strings (evalVault emits
// quoted-key JSON precisely so this works: f({"type":"done",...});).
func parsePushed(evalJS string, out interface{}) error {
	idx := strings.Index(evalJS, "f({")
	if idx < 0 {
		return errPushedNotFound
	}
	start := idx + 2 // "f({" is 3 bytes; +2 lands exactly on the opening '{'
	depth := 0
	for i := start; i < len(evalJS); i++ {
		switch evalJS[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return json.Unmarshal([]byte(evalJS[start:i+1]), out)
			}
		}
	}
	return errPushedNotFound
}

// TestVaultViewStructHasNoSecretField guards, by reflection, against ever
// adding a secret field to a shape a binding returns to the page.
func TestVaultViewStructHasNoSecretField(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(vaultEntryJSON{}), reflect.TypeOf(vaultEntryFullJSON{}), reflect.TypeOf(vaultForSessionJSON{}), reflect.TypeOf(vaultStateJSON{}), reflect.TypeOf(vaultNodeJSON{}),
		reflect.TypeOf(vaultCredentialJSON{}), reflect.TypeOf(vaultCredUseJSON{}),
	} {
		walkStructFields(t, typ, typ.Name())
	}
}

func walkStructFields(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "secret") || strings.Contains(name, "password") {
			t.Fatalf("%s.%s looks like a secret field on a page-facing shape", path, f.Name)
		}
		if f.Type.Kind() == reflect.Struct {
			walkStructFields(t, f.Type, path+"."+f.Name)
		}
	}
}

// A device-restricted entry (vaultSave's devices) is listed by
// reachVaultForSession only for a session on one of its devices — the
// session's device as the SERVER answered it (tunnel registry), never a value
// the page sends.
func TestVaultBindingsDeviceRestriction(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.windows = &sessionWindows{}
	b.tunnels = newReachTunnels()
	v := newTestVaultForBridge(t)
	if err := v.Create("secret1", 15); err != nil {
		t.Fatal(err)
	}
	in := vaultSaveInput{Origin: "https://a.example", TenantID: 5, Label: "SRV-61 admin", Secret: "pw",
		Devices: []vaultDeviceJSON{{Origin: "https://a.example", TenantID: 5, UUID: "uuid-61", ID: 61, Name: "SRV-61"}}}
	if ok, err := b.vaultSave(b.token, "bbbbbbbb", in); !ok || err != nil {
		t.Fatalf("vaultSave: %v %v", ok, err)
	}
	evals := h.waitEvals(t, 1)
	if !strings.Contains(evals[0], `"ok":true`) {
		t.Fatalf("save: %v", evals)
	}
	list, _ := b.vaultList(b.token)
	if len(list) != 1 || len(list[0].Devices) != 1 || list[0].Devices[0].UUID != "uuid-61" || list[0].Devices[0].ID != 61 {
		t.Fatalf("vaultList devices: %+v", list)
	}
	b.tunnels.register("on-61", "https://a.example", 5, 61)
	b.tunnels.register("on-62", "https://a.example", 5, 62)
	b.tunnels.registerDevice("on-61-reenrolled", "https://a.example", 5, 777, "uuid-61")
	for tok, want := range map[string]int{"on-61": 1, "on-62": 0, "on-61-reenrolled": 1} {
		res, err := b.vaultForSession(b.token, tok)
		if err != nil || !res.Scope.Known || len(res.Entries) != want {
			t.Errorf("%s: %+v %v, want %d entries", tok, res, err, want)
		}
	}
	// A tenant id that would wrap onto another tenant is refused.
	bad := vaultSaveInput{Origin: "https://a.example", TenantID: 5, Label: "x", Secret: "pw",
		Devices: []vaultDeviceJSON{{Origin: "https://a.example", TenantID: 1 << 32, ID: 61}}}
	if ok, err := b.vaultSave(b.token, "cccccccc", bad); !ok || err != nil {
		t.Fatalf("vaultSave call: %v %v", ok, err)
	}
	evals = h.waitEvals(t, 2)
	if !strings.Contains(evals[1], `"ok":false`) || !strings.Contains(evals[1], `"invalid"`) {
		t.Fatalf("a wrapping tenant id must be refused: %v", evals[1])
	}
}
