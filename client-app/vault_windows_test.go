//go:build windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDPAPIProtectorRoundTrip(t *testing.T) {
	p := dpapiProtector{}
	plain := []byte("hunter2 é€ secret")
	blob, err := p.Protect(plain)
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	if bytes.Contains(blob, plain) {
		t.Fatalf("DPAPI blob must not contain the plaintext verbatim")
	}
	var got []byte
	if err := p.Unprotect(blob, func(b []byte) error {
		got = append([]byte(nil), b...)
		return nil
	}); err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: got %q want %q", got, plain)
	}
}

func TestDPAPIProtectorWrongEntropyFails(t *testing.T) {
	p := dpapiProtector{}
	blob, err := p.Protect([]byte("x"))
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	// Corrupt the blob: DPAPI must refuse to decrypt tampered ciphertext.
	corrupt := append([]byte(nil), blob...)
	corrupt[len(corrupt)/2] ^= 0xff
	if err := p.Unprotect(corrupt, func([]byte) error { return nil }); err == nil {
		t.Fatalf("expected Unprotect to fail on a corrupted blob")
	}
}

func TestDPAPIMemSealerRoundTrip(t *testing.T) {
	s := dpapiMemSealer{}
	for _, plain := range [][]byte{
		[]byte(""), []byte("a"), []byte("exactly16bytes!!"), []byte("hunter2 with a longer secret é€"),
	} {
		sealed, err := s.Seal(plain)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plain, err)
		}
		// (a 1-byte plaintext is found by chance in random ciphertext: only
		// plaintexts long enough to be meaningful are checked)
		if len(plain) >= 4 && bytes.Contains(sealed, plain) {
			t.Fatalf("sealed form must not contain the plaintext verbatim for %q", plain)
		}
		var got []byte
		if err := s.Open(sealed, func(b []byte) error {
			got = append([]byte(nil), b...)
			return nil
		}); err != nil {
			t.Fatalf("Open(%q): %v", plain, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("round trip mismatch: got %q want %q", got, plain)
		}
	}
}

func TestFileVaultStoreAtomicReplaceKeepsPrev(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	if err := makePrivateNamedDir(dir); err != nil {
		t.Fatalf("makePrivateNamedDir: %v", err)
	}
	s := &fileVaultStore{dir: dir, path: filepath.Join(dir, vaultFileName)}

	if err := s.Save([]byte("v1")); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	if err := s.Save([]byte("v2")); err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	got, err := s.Load()
	if err != nil || string(got) != "v2" {
		t.Fatalf("Load = %q, %v, want v2", got, err)
	}
	prev, err := os.ReadFile(filepath.Join(dir, vaultPrevName))
	if err != nil || string(prev) != "v1" {
		t.Fatalf("vault.prev = %q, %v, want v1", prev, err)
	}
}

func TestFileVaultStoreResetSetAside(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	_ = makePrivateNamedDir(dir)
	s := &fileVaultStore{dir: dir, path: filepath.Join(dir, vaultFileName)}
	_ = s.Save([]byte("data"))

	if err := s.Reset(true); err != nil {
		t.Fatalf("Reset(setAside): %v", err)
	}
	got, err := s.Load()
	if err != nil || got != nil {
		t.Fatalf("Load after set-aside = %q, %v, want absent", got, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "vault-unreadable-*.dat"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one set-aside file, got %v", matches)
	}
}

func TestFileVaultStoreResetDelete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	_ = makePrivateNamedDir(dir)
	s := &fileVaultStore{dir: dir, path: filepath.Join(dir, vaultFileName)}
	_ = s.Save([]byte("data"))
	if err := s.Reset(false); err != nil {
		t.Fatalf("Reset(delete): %v", err)
	}
	if got, err := s.Load(); err != nil || got != nil {
		t.Fatalf("Load after delete = %q, %v, want absent", got, err)
	}
}

// TestVaultEndToEndWithRealDPAPI exercises the full Vault state machine
// against the real Windows protector and sealer (not fakes) — the one test
// that would catch a DPAPI/CryptProtectMemory integration mistake the
// portable tests (vault_test.go) cannot see.
func TestVaultEndToEndWithRealDPAPI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	store := &fileVaultStore{dir: dir}
	if err := makePrivateNamedDir(dir); err != nil {
		t.Fatalf("makePrivateNamedDir: %v", err)
	}
	store.path = filepath.Join(dir, vaultFileName)

	v := NewVault(store, dpapiProtector{}, dpapiMemSealer{})
	v.iterations = 2
	if err := v.Create("secret1", 15); err != nil {
		t.Fatalf("Create: %v", err)
	}
	id, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "L", Secret: "hunter2"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	v.Lock()

	v2 := NewVault(store, dpapiProtector{}, dpapiMemSealer{})
	v2.iterations = 2
	if err := v2.Unlock("secret1"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	typed, err := v2.BuildTypeText(id, VaultSession{Origin: "https://a.example", TenantID: 1}, false, func(_ string, secret []byte) ([]byte, error) {
		return append([]byte(nil), secret...), nil
	})
	if err != nil || string(typed) != "hunter2" {
		t.Fatalf("BuildTypeText = %q, %v", typed, err)
	}
}
