//go:build windows

package engine

import (
	"path/filepath"
	"syscall"
	"testing"
)

// The host's OnFileReceived replaces the agent's toast (which re-runs the
// executable with --notify-* flags) while a session runs.
func TestFileReceivedHookReplacesToast(t *testing.T) {
	dir := t.TempDir()
	var got []string
	setFileReceivedHook(func(p string) { got = append(got, p) })
	defer setFileReceivedHook(nil)
	fileReceived("report.pdf", dir)
	if len(got) != 1 || got[0] != filepath.Join(dir, "report.pdf") {
		t.Fatalf("hook calls %v", got)
	}
	setFileReceivedHook(nil)
	if fileReceivedHook.Load() != nil {
		t.Fatal("hook not removed")
	}
}

// A host running as the signed-in user (no SeTcbPrivilege, unlike the
// agent's SYSTEM helper) writes received files as itself, into its own
// Downloads folder, instead of failing with no_user_session.
func TestSessionUserWithoutTcbPrivilege(t *testing.T) {
	tok, err := sessionUserToken()
	if err != nil {
		t.Fatalf("session user token: %v", err)
	}
	if tok != 0 {
		syscall.CloseHandle(tok)
		t.Skip("this process holds SeTcbPrivilege (LocalSystem): the impersonation path applies")
	}
	dir, err := sessionDownloadsDir()
	if err != nil || dir == "" {
		t.Fatalf("Downloads folder: %q, %v", dir, err)
	}
	ran := false
	if err := asSessionUser(func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("asSessionUser: ran %v, %v", ran, err)
	}
	t.Logf("received files go to %s", dir)
}
