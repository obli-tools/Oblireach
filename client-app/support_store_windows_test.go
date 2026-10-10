//go:build windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The support servers file is sealed by DPAPI: the operator key never
// appears in it, it reads back, and a tampered file is refused.
func TestSupportStoreDPAPI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	store := &fileSupportStore{dir: dir}
	if b, err := store.Load(); err != nil || b != nil {
		t.Fatalf("absent file: %q %v", b, err)
	}
	s := &supportSettings{store: store}
	if _, err := s.save(supportServerInput{Label: "Lab", URL: "support.example.com", Key: "PLAINTEXT-OPERATOR-KEY-42"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, supportFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("PLAINTEXT-OPERATOR-KEY-42")) || bytes.Contains(raw, []byte("support.example.com")) {
		t.Fatal("the settings file is not sealed")
	}
	list, err := (&supportSettings{store: &fileSupportStore{dir: dir}}).list()
	if user := supportUserServers(list); err != nil || len(list) != 2 || len(user) != 1 || user[0].Key != "PLAINTEXT-OPERATOR-KEY-42" {
		t.Fatalf("read back: %+v %v", list, err)
	}
	raw[len(raw)/2] ^= 0xFF
	if err := os.WriteFile(filepath.Join(dir, supportFileName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&supportSettings{store: &fileSupportStore{dir: dir}}).list(); err == nil {
		t.Fatal("a tampered file was accepted")
	}
}
