package main

import (
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func withTestKnownHosts(t *testing.T) *knownHostsStore {
	t.Helper()
	old := testKnownHostsPathOverride
	testKnownHostsPathOverride = filepath.Join(t.TempDir(), "known_hosts")
	t.Cleanup(func() { testKnownHostsPathOverride = old })
	s, err := newKnownHostsStore()
	if err != nil {
		t.Fatalf("newKnownHostsStore: %v", err)
	}
	return s
}

func testHostKey(t *testing.T, seed byte) ssh.PublicKey {
	t.Helper()
	_, pub := genEd25519Signer(t, seed)
	return pub
}

func TestKnownHostsUnknownThenTrusted(t *testing.T) {
	s := withTestKnownHosts(t)
	key := testHostKey(t, 1)
	hp := normalizeHostPort("example.local", 22)

	state, err := s.check(hp, key)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if state != hostKeyUnknown {
		t.Fatalf("state = %v, want unknown", state)
	}

	if err := s.trust(hp, key); err != nil {
		t.Fatalf("trust: %v", err)
	}
	state, err = s.check(hp, key)
	if err != nil {
		t.Fatalf("check after trust: %v", err)
	}
	if state != hostKeyTrusted {
		t.Fatalf("state = %v, want trusted", state)
	}
}

func TestKnownHostsMismatchDetected(t *testing.T) {
	s := withTestKnownHosts(t)
	hp := normalizeHostPort("example.local", 22)
	original := testHostKey(t, 1)
	impostor := testHostKey(t, 2)

	if err := s.trust(hp, original); err != nil {
		t.Fatalf("trust: %v", err)
	}
	state, err := s.check(hp, impostor)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if state != hostKeyMismatch {
		t.Fatalf("state = %v, want mismatch (a different key must never be silently accepted)", state)
	}
	// The original key must still check out as trusted — trust() never
	// erases a prior line, and check() must not confuse "a mismatched
	// entry exists" with "this exact key is untrusted".
	state, err = s.check(hp, original)
	if err != nil {
		t.Fatalf("check original: %v", err)
	}
	if state != hostKeyTrusted {
		t.Fatalf("original key state = %v, want trusted", state)
	}
}

func TestKnownHostsDifferentPortIsolated(t *testing.T) {
	s := withTestKnownHosts(t)
	key := testHostKey(t, 1)
	hp22 := normalizeHostPort("example.local", 22)
	hp2222 := normalizeHostPort("example.local", 2222)

	if err := s.trust(hp22, key); err != nil {
		t.Fatalf("trust: %v", err)
	}
	state, err := s.check(hp2222, key)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if state != hostKeyUnknown {
		t.Fatalf("state = %v, want unknown (a different port is a different host entry)", state)
	}
}

func TestKnownHostsDifferentAlgoIsUnknownNotMismatch(t *testing.T) {
	s := withTestKnownHosts(t)
	hp := normalizeHostPort("example.local", 22)
	ed := testHostKey(t, 1)
	if err := s.trust(hp, ed); err != nil {
		t.Fatalf("trust: %v", err)
	}
	rsaKey := genRSASigner(t).PublicKey()
	state, err := s.check(hp, rsaKey)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if state != hostKeyUnknown {
		t.Fatalf("state = %v, want unknown for a new algorithm on an already-trusted host", state)
	}
}

func TestKnownHostsPersistsAcrossInstances(t *testing.T) {
	old := testKnownHostsPathOverride
	testKnownHostsPathOverride = filepath.Join(t.TempDir(), "known_hosts")
	t.Cleanup(func() { testKnownHostsPathOverride = old })

	hp := normalizeHostPort("persist.local", 22)
	key := testHostKey(t, 9)

	s1, err := newKnownHostsStore()
	if err != nil {
		t.Fatalf("newKnownHostsStore: %v", err)
	}
	if err := s1.trust(hp, key); err != nil {
		t.Fatalf("trust: %v", err)
	}

	s2, err := newKnownHostsStore()
	if err != nil {
		t.Fatalf("newKnownHostsStore (second instance): %v", err)
	}
	state, err := s2.check(hp, key)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if state != hostKeyTrusted {
		t.Fatalf("state = %v, want trusted (must survive across store instances / app restarts)", state)
	}
}
