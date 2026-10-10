//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The committed release package (signed by the publisher's certificate).
const releaseMSI = "dist/oblireach-agent.msi"

func signedReleaseMSI(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(releaseMSI)
	if err != nil {
		t.Skip(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s not present", releaseMSI)
	}
	if err := winVerifyTrustFile(p); err != nil && !isChainTrustError(err) {
		t.Skipf("%s is not validly signed here (%s)", releaseMSI, trustErrorText(err))
	}
	return p
}

func TestVerifyUpdateSignerRejectsUnsigned(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oblireach-update-1.msi")
	if err := os.WriteFile(p, fakeMSI(100<<10), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyUpdateSigner(p); err == nil || !strings.Contains(err.Error(), "invalid Authenticode signature") {
		t.Fatalf("unsigned package: err = %v", err)
	}
}

func TestVerifyUpdateSignerReleaseMSI(t *testing.T) {
	p := signedReleaseMSI(t)

	leaf, err := readCodeSigner(p)
	if err != nil {
		t.Fatalf("readCodeSigner: %v", err)
	}
	if leaf.name == "" || len(leaf.subject) == 0 || leaf.thumbprint == [20]byte{} {
		t.Fatalf("incomplete signer: %+v", leaf)
	}

	// This test binary is unsigned: the pinned publisher is the reference
	// (set only by the release build, -ldflags -X).
	if updateSignerPin != "" {
		if name, err := verifyUpdateSigner(p); err != nil {
			t.Fatalf("release MSI refused: %v (signer %q, pin %q)", err, name, updateSignerPin)
		}
	}
	// Same subject as "this executable's" signer.
	if _, err := verifySignerAgainst(p, &codeSigner{subject: leaf.subject}, ""); err != nil {
		t.Fatalf("same subject refused: %v", err)
	}
	// The real case: the signed agent executable of the same release is the
	// running binary (subject match, no pin needed).
	if exe, err := filepath.Abs("dist/oblireach-agent.exe"); err == nil {
		if own, err := readCodeSigner(exe); err == nil {
			if name, err := verifySignerAgainst(p, own, ""); err != nil {
				t.Fatalf("MSI refused against the release exe's signer: %v", err)
			} else {
				t.Logf("release MSI signed by %q (same subject as dist/oblireach-agent.exe)", name)
			}
		}
	}
	// Another publisher.
	if _, err := verifySignerAgainst(p, &codeSigner{subject: []byte("other")}, "Someone Else"); err == nil ||
		!strings.Contains(err.Error(), "not by the publisher") {
		t.Fatalf("foreign publisher accepted: %v", err)
	}
}

func TestVerifyUpdateSignerRejectsTamperedMSI(t *testing.T) {
	src := signedReleaseMSI(t)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	// Flip bytes well inside the embedded cabinet.
	for i := len(data) / 2; i < len(data)/2+64; i++ {
		data[i] ^= 0xFF
	}
	p := filepath.Join(t.TempDir(), "oblireach-update-2.msi")
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyUpdateSigner(p); err == nil {
		t.Fatalf("tampered package accepted")
	} else {
		t.Logf("tampered package refused: %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatalf("own process reported dead")
	}
	cmd := exec.Command(filepath.Join(systemDir(), "cmd.exe"), "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if processAlive(cmd.Process.Pid) {
		t.Fatalf("exited process %d reported alive", cmd.Process.Pid)
	}
	if processAlive(0) || processAlive(-1) {
		t.Fatalf("invalid pid reported alive")
	}
}

func TestWindowsInstallerBusyCallable(t *testing.T) {
	// Only exercises the syscall path: an MSI transaction may legitimately
	// be running on the machine that runs the tests.
	t.Logf("Windows Installer busy: %v", windowsInstallerBusy())
}

func TestSystemDir(t *testing.T) {
	d := systemDir()
	if _, err := os.Stat(filepath.Join(d, "msiexec.exe")); err != nil {
		t.Fatalf("msiexec.exe not found in %q: %v", d, err)
	}
}
