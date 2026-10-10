package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// PBKDF2 against RFC 6070 test vectors (password="password", salt="salt").
// ---------------------------------------------------------------------------

func TestPBKDF2SHA256(t *testing.T) {
	// Known-answer test: with a single iteration, PBKDF2 is exactly
	// HMAC-SHA256(password, salt || INT32BE(1)) — computed here
	// independently with crypto/hmac + crypto/sha256 (not the function under
	// test) as the oracle.
	mac := hmac.New(sha256.New, []byte("password"))
	saltBlock := append([]byte("salt"), 0, 0, 0, 1)
	mac.Write(saltBlock)
	want := mac.Sum(nil)
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	if !bytes.Equal(got, want) {
		t.Fatalf("pbkdf2 c=1: got %x want %x", got, want)
	}
	got2 := pbkdf2SHA256([]byte("password"), []byte("salt"), 2, 32)
	if bytes.Equal(got, got2) {
		t.Fatalf("pbkdf2 c=1 and c=2 must differ")
	}
	got3 := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	if !bytes.Equal(got, got3) {
		t.Fatalf("pbkdf2 must be deterministic")
	}
}

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeProtector "encrypts" by XORing with a fixed key and prefixing a marker;
// good enough to prove the Vault drives Protect/Unprotect correctly, and to
// simulate "wrong account" by making Unprotect fail.
type fakeProtector struct {
	fail bool
}

func (p *fakeProtector) Protect(plain []byte) ([]byte, error) {
	if p.fail {
		return nil, errors.New("fail")
	}
	out := make([]byte, len(plain)+4)
	copy(out, "FAKE")
	for i, b := range plain {
		out[4+i] = b ^ 0x5a
	}
	return out, nil
}

func (p *fakeProtector) Unprotect(blob []byte, use func([]byte) error) error {
	if p.fail || len(blob) < 4 || string(blob[:4]) != "FAKE" {
		return errors.New("fail")
	}
	plain := make([]byte, len(blob)-4)
	for i, b := range blob[4:] {
		plain[i] = b ^ 0x5a
	}
	defer wipeBytesLocal(plain)
	return use(plain)
}

// fakeSealer just copies bytes (no real protection) so tests can inspect the
// codec / state machine without depending on CryptProtectMemory.
type fakeSealer struct{}

func (fakeSealer) Seal(p []byte) ([]byte, error) { return append([]byte(nil), p...), nil }
func (fakeSealer) Open(s []byte, use func([]byte) error) error {
	return use(append([]byte(nil), s...))
}

type fakeStore struct {
	data []byte
	err  error
}

func (s *fakeStore) Load() ([]byte, error) { return s.data, s.err }
func (s *fakeStore) Save(outer []byte) error {
	s.data = append([]byte(nil), outer...)
	return nil
}
func (s *fakeStore) Reset(setAside bool) error { s.data = nil; return nil }
func (s *fakeStore) Path() string              { return "fake.dat" }

func newTestVault() (*Vault, *fakeStore) {
	store := &fakeStore{}
	v := NewVault(store, &fakeProtector{}, fakeSealer{})
	v.iterations = 2 // keep the suite fast; the algorithm itself is covered above
	return v, store
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

func TestVaultLifecycle(t *testing.T) {
	v, store := newTestVault()
	if got := v.State().State; got != "absent" {
		t.Fatalf("initial state = %q, want absent", got)
	}
	if err := v.Create("secret1", 15); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := v.State().State; got != "unlocked" {
		t.Fatalf("after Create = %q, want unlocked", got)
	}

	id, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "Domain admin", Secret: "hunter2", SecureOnly: true})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(v.List()) != 1 {
		t.Fatalf("expected 1 entry")
	}

	v.Lock()
	if got := v.State().State; got != "locked" {
		t.Fatalf("after Lock = %q, want locked", got)
	}
	if len(v.List()) != 0 {
		t.Fatalf("List must be empty while locked")
	}

	// Reopen against the same backing store (simulates a restart).
	v2 := NewVault(store, &fakeProtector{}, fakeSealer{})
	v2.iterations = 2
	if got := v2.State().State; got != "locked" {
		t.Fatalf("reopened state = %q, want locked", got)
	}
	if err := v2.Unlock("wrong"); !errors.Is(err, errWrongCode) {
		t.Fatalf("Unlock(wrong) = %v, want errWrongCode", err)
	}
	if err := v2.Unlock("secret1"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	entries := v2.EntriesForScope("https://a.example", 1)
	if len(entries) != 1 || entries[0].Label != "Domain admin" {
		t.Fatalf("EntriesForScope = %+v", entries)
	}
	if entries := v2.EntriesForScope("https://other.example", 1); len(entries) != 0 {
		t.Fatalf("cross-origin leak: %+v", entries)
	}

	typed, err := v2.BuildTypeText(id, VaultSession{Origin: "https://a.example", TenantID: 1}, false, func(user string, secret []byte) ([]byte, error) {
		return append([]byte(nil), secret...), nil
	})
	if err != nil || string(typed) != "hunter2" {
		t.Fatalf("BuildTypeText = %q, %v", typed, err)
	}

	if _, err := v2.BuildTypeText(id, VaultSession{Origin: "https://a.example", TenantID: 999}, false, func(string, []byte) ([]byte, error) { return nil, nil }); !errors.Is(err, errScopeMismatch) {
		t.Fatalf("BuildTypeText wrong tenant = %v, want errScopeMismatch", err)
	}
}

func TestVaultWrongCodeBackoff(t *testing.T) {
	v, _ := newTestVault()
	_ = v.Create("secret1", 15)
	v.Lock()

	base := time.Unix(1_700_000_000, 0)
	v.nowFn = func() time.Time { return base }

	for i := 0; i < 4; i++ {
		if err := v.Unlock("wrong"); !errors.Is(err, errWrongCode) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// 5th failure triggers the 30s backoff.
	if err := v.Unlock("wrong"); !errors.Is(err, errWrongCode) {
		t.Fatalf("5th attempt: %v", err)
	}
	if err := v.Unlock("secret1"); err == nil {
		t.Fatalf("expected the backoff to refuse even the right code")
	} else if we, ok := err.(*vaultWaitError); !ok || we.Seconds <= 0 {
		t.Fatalf("expected *vaultWaitError, got %v", err)
	}

	// Advance past the backoff window: the right code now works.
	v.nowFn = func() time.Time { return base.Add(31 * time.Second) }
	if err := v.Unlock("secret1"); err != nil {
		t.Fatalf("Unlock after backoff: %v", err)
	}
}

func TestVaultUnreadableNeverOverwritten(t *testing.T) {
	store := &fakeStore{data: []byte("not a vault file at all")}
	v := NewVault(store, &fakeProtector{}, fakeSealer{})
	if got := v.State().State; got != "unreadable" {
		t.Fatalf("state = %q, want unreadable", got)
	}
	before := append([]byte(nil), store.data...)
	if err := v.Unlock("anything"); err == nil {
		t.Fatalf("Unlock on unreadable must fail")
	}
	if !bytes.Equal(store.data, before) {
		t.Fatalf("unreadable vault file must never be modified by Unlock")
	}
	if err := v.Reset(true); err != nil {
		t.Fatalf("Reset(setAside): %v", err)
	}
	if got := v.State().State; got != "absent" {
		t.Fatalf("after set-aside = %q, want absent", got)
	}
}

func TestVaultSaveValidation(t *testing.T) {
	v, _ := newTestVault()
	_ = v.Create("secret1", 15)

	if _, err := v.Save(SaveInput{Label: "", Secret: "x"}); !errors.Is(err, errEmptyLabel) {
		t.Fatalf("empty label: %v", err)
	}
	if _, err := v.Save(SaveInput{Label: "ok", Secret: ""}); !errors.Is(err, errEmptySecret) {
		t.Fatalf("empty secret on create: %v", err)
	}
	if _, err := v.Save(SaveInput{Label: "ok\nbad", Secret: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("control char in label: %v", err)
	}

	id, err := v.Save(SaveInput{Label: "A", Username: "root", Secret: "first"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Edit with an empty secret keeps the old one.
	if _, err := v.Save(SaveInput{ID: id, Label: "A renamed", Username: "root", Secret: ""}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	kept, err := v.Reveal(id)
	if err != nil || kept != "first" {
		t.Fatalf("Reveal after empty-secret edit = %q, %v, want \"first\"", kept, err)
	}

	if err := v.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(v.List()) != 0 {
		t.Fatalf("expected no entries after Delete")
	}
}

func TestVaultChangeCode(t *testing.T) {
	v, store := newTestVault()
	_ = v.Create("secret1", 15)
	if err := v.ChangeCode("wrong", "newsecret"); !errors.Is(err, errWrongCode) {
		t.Fatalf("ChangeCode wrong old: %v", err)
	}
	if err := v.ChangeCode("secret1", "newsecret"); err != nil {
		t.Fatalf("ChangeCode: %v", err)
	}
	v.Lock()
	v2 := NewVault(store, &fakeProtector{}, fakeSealer{})
	v2.iterations = 2
	if err := v2.Unlock("secret1"); err == nil {
		t.Fatalf("old code must no longer unlock")
	}
	if err := v2.Unlock("newsecret"); err != nil {
		t.Fatalf("new code must unlock: %v", err)
	}
}

func TestPersistRoundTripEncodesEntries(t *testing.T) {
	v, store := newTestVault()
	_ = v.Create("secret1", 15)
	_, _ = v.Save(SaveInput{Origin: "https://a", TenantID: 2, TenantName: "Acme", Label: "L", Username: "u", Note: "n", Secret: "s3cr3t", SecureOnly: true})
	if len(store.data) == 0 {
		t.Fatalf("expected persisted bytes")
	}
	v.Lock()
	v2 := NewVault(store, &fakeProtector{}, fakeSealer{})
	v2.iterations = 2
	if err := v2.Unlock("secret1"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	got := v2.List()
	if len(got) != 1 || got[0].TenantName != "Acme" || got[0].Note != "n" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// v2: local connection tree (folders + ssh/rdp), the data model for a
// standalone RDP/SSH base independent of any Obliance server.
// ---------------------------------------------------------------------------

func TestVaultFolderTree(t *testing.T) {
	v, _ := newTestVault()
	_ = v.Create("secret1", 15)

	rootFolder, err := v.Save(SaveInput{Kind: vaultKindFolder, Label: "Clients"})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	subFolder, err := v.Save(SaveInput{Kind: vaultKindFolder, Label: "Acme", ParentID: rootFolder})
	if err != nil {
		t.Fatalf("create sub-folder: %v", err)
	}
	sshID, err := v.Save(SaveInput{Kind: vaultKindSSH, Label: "web01", ParentID: subFolder,
		Host: "10.0.0.5", Port: 22, Username: "root", Secret: "hunter2"})
	if err != nil {
		t.Fatalf("create ssh entry: %v", err)
	}
	rdpID, err := v.Save(SaveInput{Kind: vaultKindRDP, Label: "win01", ParentID: subFolder,
		Host: "10.0.0.6", Port: 3389, Username: "administrator", Secret: "hunter2"})
	if err != nil {
		t.Fatalf("create rdp entry: %v", err)
	}

	if got := v.ByParent(""); len(got) != 1 || got[0].ID != rootFolder {
		t.Fatalf("root listing = %+v, want just the root folder", got)
	}
	children := v.ByParent(rootFolder)
	if len(children) != 1 || children[0].ID != subFolder {
		t.Fatalf("root folder's children = %+v, want just the sub-folder", children)
	}
	grandchildren := v.ByParent(subFolder)
	if len(grandchildren) != 2 {
		t.Fatalf("sub-folder's children = %+v, want ssh+rdp", grandchildren)
	}
	byID := map[string]VaultEntryMeta{}
	for _, e := range grandchildren {
		byID[e.ID] = e
	}
	if e, ok := byID[sshID]; !ok || e.Kind != vaultKindSSH || e.Host != "10.0.0.5" || e.Port != 22 {
		t.Fatalf("ssh entry mismatch: %+v", e)
	}
	if e, ok := byID[rdpID]; !ok || e.Kind != vaultKindRDP || e.Host != "10.0.0.6" || e.Port != 3389 {
		t.Fatalf("rdp entry mismatch: %+v", e)
	}

	// Password entries and local connections never leak into each other's views.
	_, _ = v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "Domain admin", Secret: "s3cr3t"})
	if got := v.List(); len(got) != 1 {
		t.Fatalf("List() must only ever show password entries, got %+v", got)
	}
	if got := v.ByParent(""); len(got) != 1 {
		t.Fatalf("ByParent(root) must not show the password entry, got %+v", got)
	}
	if got := v.EntriesForScope("https://a.example", 1); len(got) != 1 {
		t.Fatalf("EntriesForScope must not show local connections, got %+v", got)
	}

	// A folder with children refuses to delete.
	if err := v.Delete(subFolder); !errors.Is(err, errFolderNotEmpty) {
		t.Fatalf("Delete non-empty folder = %v, want errFolderNotEmpty", err)
	}
	if err := v.Delete(sshID); err != nil || v.Delete(rdpID) != nil {
		t.Fatalf("Delete leaf entries failed")
	}
	if err := v.Delete(subFolder); err != nil {
		t.Fatalf("Delete now-empty folder: %v", err)
	}
}

func TestVaultConnectionValidation(t *testing.T) {
	v, _ := newTestVault()
	_ = v.Create("secret1", 15)

	if _, err := v.Save(SaveInput{Kind: vaultKindSSH, Label: "no-host", Port: 22, Secret: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("ssh without host = %v, want errBadChars", err)
	}
	if _, err := v.Save(SaveInput{Kind: vaultKindSSH, Label: "no-port", Host: "1.2.3.4", Secret: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("ssh without port = %v, want errBadChars", err)
	}
	if _, err := v.Save(SaveInput{Kind: vaultKindRDP, Label: "key-auth", Host: "1.2.3.4", Port: 3389, AuthMethod: vaultAuthKey, Secret: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("rdp with key auth = %v, want errBadChars (not supported this pass)", err)
	}
	if _, err := v.Save(SaveInput{Kind: vaultKindFolder, Label: "bad", Host: "1.2.3.4"}); !errors.Is(err, errBadChars) {
		t.Fatalf("folder with a host = %v, want errBadChars", err)
	}
	if _, err := v.Save(SaveInput{Kind: vaultKindSSH, Label: "no-parent", Host: "1.2.3.4", Port: 22, Secret: "x", ParentID: "deadbeef"}); !errors.Is(err, errBadChars) {
		t.Fatalf("malformed parent id = %v, want errBadChars", err)
	}
	folder, err := v.Save(SaveInput{Kind: vaultKindFolder, Label: "F"})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	// A parent that exists but is not a folder (a password entry here) is refused.
	pw, _ := v.Save(SaveInput{Origin: "https://a", TenantID: 1, Label: "P", Secret: "x"})
	if _, err := v.Save(SaveInput{Kind: vaultKindSSH, Label: "bad-parent", Host: "1.2.3.4", Port: 22, Secret: "x", ParentID: pw}); !errors.Is(err, errNotFound) {
		t.Fatalf("non-folder parent = %v, want errNotFound", err)
	}
	if _, err := v.Save(SaveInput{Kind: vaultKindSSH, Label: "ok", Host: "1.2.3.4", Port: 22, Secret: "x", ParentID: folder}); err != nil {
		t.Fatalf("valid ssh under an existing folder: %v", err)
	}
	// A folder cannot become its own parent on edit.
	if _, err := v.Save(SaveInput{ID: folder, Kind: vaultKindFolder, Label: "F", ParentID: folder}); !errors.Is(err, errBadChars) {
		t.Fatalf("folder as its own parent = %v, want errBadChars", err)
	}
	// An entry never changes kind across an edit.
	if _, err := v.Save(SaveInput{ID: folder, Kind: vaultKindSSH, Label: "F", Host: "1.2.3.4", Port: 22, Secret: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("changing kind on edit = %v, want errBadChars", err)
	}
}

// TestVaultV1UpgradeIsPasswordKind proves a real, working vault whose file
// happens to be in the pre-v2 (v1) on-disk shape still unlocks, with its
// entry usable exactly as a v2 password entry (root-parented, Kind
// vaultKindPassword) — the actual code hash comes from a real Create/Save, not
// a fabricated sample, so Unlock exercises the genuine password path.
func TestVaultV1UpgradeIsPasswordKind(t *testing.T) {
	v, store := newTestVault()
	_ = v.Create("secret1", 15)
	if _, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "Domain admin", Username: "adm", Secret: "hunter2"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Downgrade the just-persisted file to the historical v1 byte shape (same
	// DPAPI blob, decoded then re-encoded without the v2 tail).
	outer := append([]byte(nil), store.data...)
	format, blob, ok := parseOuter(outer)
	if !ok {
		t.Fatalf("parseOuter failed")
	}
	var plain *vaultPlain
	if err := (&fakeProtector{}).Unprotect(blob, func(p []byte) error {
		pv, ok := decodeVaultPlain(p)
		if !ok {
			t.Fatalf("decodeVaultPlain failed")
		}
		plain = pv
		return nil
	}); err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	v1Plain := buildV1Bytes(plain)
	v1Blob, err := (&fakeProtector{}).Protect(v1Plain)
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	store.data = buildOuter(format, v1Blob)

	v2 := NewVault(store, &fakeProtector{}, fakeSealer{})
	v2.iterations = 2
	if got := v2.State().State; got != "locked" {
		t.Fatalf("state after downgrade = %q, want locked", got)
	}
	if err := v2.Unlock("secret1"); err != nil {
		t.Fatalf("Unlock a v1-shaped file: %v", err)
	}
	got := v2.List()
	if len(got) != 1 || got[0].Kind != vaultKindPassword || got[0].ParentID != "" || got[0].Label != "Domain admin" {
		t.Fatalf("upgraded v1 entry = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// SSH private key entries (kind=vaultKindSSH, authMethod=vaultAuthKey): a
// PEM key is neither password-length nor free of newlines, unlike every
// other secret this vault stores — found while wiring the local SSH
// terminal (ssh_session.go) to actually reveal a saved key: without this,
// validateSaveInput rejected any real key on both length and "control
// character" (its own newlines) before the SSH work ever ran.
// ---------------------------------------------------------------------------

func fakePEMPrivateKey(lines int) string {
	s := "-----BEGIN OPENSSH PRIVATE KEY-----\n"
	for i := 0; i < lines; i++ {
		s += "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFB\n"
	}
	s += "-----END OPENSSH PRIVATE KEY-----\n"
	return s
}

func TestVaultSSHKeySecretAllowsNewlinesAndLargerSize(t *testing.T) {
	v, _ := newTestVault()
	if err := v.Create("secret1", 15); err != nil {
		t.Fatalf("Create: %v", err)
	}
	pem := fakePEMPrivateKey(60) // well past vaultMaxSecretRunes (128), under vaultMaxKeyRunes
	if utf8.RuneCountInString(pem) <= vaultMaxSecretRunes {
		t.Fatalf("test fixture too short to exercise the raised ceiling")
	}
	id, err := v.Save(SaveInput{
		Label: "prod box", Kind: vaultKindSSH, ParentID: "", Host: "10.0.0.5", Port: 22,
		Username: "root", AuthMethod: vaultAuthKey, Secret: pem,
	})
	if err != nil {
		t.Fatalf("Save an SSH key entry: %v (a PEM key must not be rejected for its newlines or its length)", err)
	}
	got, err := v.Reveal(id)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if got != pem {
		t.Fatalf("revealed secret does not round-trip: got %d bytes, want %d", len(got), len(pem))
	}
}

func TestVaultPasswordSecretStillRejectsNewlines(t *testing.T) {
	v, _ := newTestVault()
	if err := v.Create("secret1", 15); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "x", Secret: "line1\nline2"})
	if !errors.Is(err, errBadChars) {
		t.Fatalf("password Secret with a newline: err = %v, want errBadChars (only an SSH key gets that exemption)", err)
	}
}

func TestVaultSSHKeySecretStillRejectsOtherControlChars(t *testing.T) {
	v, _ := newTestVault()
	if err := v.Create("secret1", 15); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := v.Save(SaveInput{
		Label: "x", Kind: vaultKindSSH, Host: "h", Port: 22, AuthMethod: vaultAuthKey,
		Secret: "line1\x00line2",
	})
	if !errors.Is(err, errBadChars) {
		t.Fatalf("SSH key Secret with a NUL byte: err = %v, want errBadChars (only newlines/CR are exempt)", err)
	}
}
