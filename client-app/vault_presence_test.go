package main

import (
	"errors"
	"testing"
	"time"
)

type fakePresence struct {
	on   bool
	sets []bool
	fail error
}

func (p *fakePresence) Enabled() bool { return p.on }
func (p *fakePresence) Set(on bool) error {
	if p.fail != nil {
		return p.fail
	}
	p.on = on
	p.sets = append(p.sets, on)
	return nil
}

// countingProtector: how many times the vault file was unsealed.
type countingProtector struct {
	fakeProtector
	opens int
}

func (p *countingProtector) Unprotect(blob []byte, use func([]byte) error) error {
	p.opens++
	return p.fakeProtector.Unprotect(blob, use)
}

func newPresenceVault(t *testing.T) (*Vault, *fakePresence, *countingProtector) {
	t.Helper()
	prot := &countingProtector{}
	v := NewVault(&fakeStore{}, prot, fakeSealer{})
	v.iterations = 2
	pr := &fakePresence{}
	v.presence = pr
	if err := v.Create("secret1", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Save(SaveInput{Origin: "https://a", TenantID: 1, Label: "L", Username: "u", Secret: "hunter2"}); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	return v, pr, prot
}

// Off (the default): Windows Hello never unlocks, the prompt is never shown.
func TestVaultPresenceOffByDefault(t *testing.T) {
	v, pr, _ := newPresenceVault(t)
	asked := 0
	if err := v.UnlockWithPresence(func() error { asked++; return nil }); !errors.Is(err, errPresenceOff) || asked != 0 {
		t.Fatalf("off: %v, asked %d", err, asked)
	}
	if v.State().State != "locked" || v.PresenceEnabled() {
		t.Fatal("unlocked while off")
	}
	// Turning it on needs an unlocked vault.
	if err := v.SetPresence(true); !errors.Is(err, errVaultLocked) || pr.on {
		t.Fatalf("SetPresence on a locked vault: %v", err)
	}
	// No switch at all (another OS, a test bridge): never.
	v2 := NewVault(&fakeStore{}, &fakeProtector{}, fakeSealer{})
	v2.iterations = 2
	_ = v2.Create("secret1", 0)
	v2.Lock()
	if err := v2.UnlockWithPresence(func() error { return nil }); !errors.Is(err, errPresenceOff) {
		t.Fatalf("no switch: %v", err)
	}
	if err := v2.Unlock("secret1"); err != nil {
		t.Fatal(err)
	}
	if err := v2.SetPresence(true); !errors.Is(err, errPresenceOff) {
		t.Fatalf("SetPresence without a switch: %v", err)
	}
}

// On: the file is unsealed only after the check said yes; a refusal leaves
// it locked and sealed; the code backoff is not touched.
func TestVaultPresenceUnlock(t *testing.T) {
	v, pr, prot := newPresenceVault(t)
	if err := v.Unlock("secret1"); err != nil {
		t.Fatal(err)
	}
	if err := v.SetPresence(true); err != nil || !pr.on || !v.PresenceEnabled() {
		t.Fatalf("SetPresence: %v", err)
	}
	v.Lock()
	opens := prot.opens
	refused := errors.New("hello_canceled")
	if err := v.UnlockWithPresence(func() error { return refused }); err != refused {
		t.Fatalf("refused: %v", err)
	}
	if v.State().State != "locked" || prot.opens != opens {
		t.Fatal("a refused check unsealed or unlocked the vault")
	}
	// The prompt runs with the vault not held (the UI keeps asking its state).
	err := v.UnlockWithPresence(func() error {
		done := make(chan string, 1)
		go func() { done <- v.State().State }()
		select {
		case st := <-done:
			if st != "locked" {
				t.Errorf("state during the prompt: %s", st)
			}
		case <-time.After(2 * time.Second):
			t.Error("the vault was held while the prompt was up")
		}
		if prot.opens != opens {
			t.Error("unsealed before the check answered")
		}
		return nil
	})
	if err != nil || v.State().State != "unlocked" || prot.opens != opens+1 {
		t.Fatalf("verified: %v, state %s, opens %d", err, v.State().State, prot.opens-opens)
	}
	if len(v.List()) != 1 {
		t.Fatal("entries not loaded")
	}
	if s, err := v.Reveal(v.List()[0].ID); err != nil || s != "hunter2" {
		t.Fatalf("secret after a Hello unlock: %q %v", s, err)
	}
	// Already unlocked: nothing asked.
	asked := 0
	if err := v.UnlockWithPresence(func() error { asked++; return nil }); err != nil || asked != 0 {
		t.Fatal("asked again while unlocked")
	}
	// Turned off while the prompt was up (another window): refused after it.
	v.Lock()
	if err := v.UnlockWithPresence(func() error { pr.on = false; return nil }); !errors.Is(err, errPresenceOff) || v.State().State != "locked" {
		t.Fatalf("turned off meanwhile: %v", err)
	}
	// The code still works, and its backoff is its own.
	pr.on = true
	for i := 0; i < 5; i++ {
		_ = v.Unlock("wrong-code")
	}
	var we *vaultWaitError
	if err := v.Unlock("secret1"); !errors.As(err, &we) {
		t.Fatalf("backoff after 5 wrong codes: %v", err)
	}
	if err := v.UnlockWithPresence(func() error { return nil }); err != nil || v.State().State != "unlocked" {
		t.Fatalf("Windows Hello during the code backoff: %v", err)
	}
}

// A new or reset vault never inherits the switch; changing the code keeps it.
func TestVaultPresenceClearedOnResetAndCreate(t *testing.T) {
	v, pr, _ := newPresenceVault(t)
	_ = v.Unlock("secret1")
	_ = v.SetPresence(true)
	if err := v.ChangeCode("secret1", "secret2"); err != nil || !pr.on {
		t.Fatalf("change code: %v, on %v", err, pr.on)
	}
	if err := v.Reset(false); err != nil || pr.on {
		t.Fatalf("reset: %v, on %v", err, pr.on)
	}
	if v.PresenceEnabled() {
		t.Fatal("an absent vault says Hello is on")
	}
	pr.on = true // (a marker left behind by a vault deleted by hand)
	if err := v.Create("secret3", 0); err != nil || pr.on {
		t.Fatalf("create: %v, on %v", err, pr.on)
	}
	// An unreadable vault: never.
	v.Lock()
	pr.on = true
	v.store.(*fakeStore).data = []byte("garbage")
	if err := v.UnlockWithPresence(func() error { return nil }); err == nil {
		t.Fatal("unlocked an unreadable vault")
	}
}
