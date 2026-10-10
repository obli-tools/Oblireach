//go:build windows

package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The Win32 pieces the session wrappers are made of, run as the current user
// (the wrappers themselves need the SYSTEM token of the capture helper).
func TestWindowsFileHelpers(t *testing.T) {
	dir, err := downloadsDirOf(0)
	if err != nil || dir == "" || !filepath.IsAbs(dir) {
		t.Fatalf("Downloads of the current user: %q, %v", dir, err)
	}
	if !strings.EqualFold(filepath.Base(dir), "Downloads") && !strings.Contains(strings.ToLower(dir), "download") {
		t.Logf("Downloads folder relocated: %s", dir)
	}

	tmp := t.TempDir()
	a, b := filepath.Join(tmp, "a.txt"), filepath.Join(tmp, "b.txt")
	_ = os.WriteFile(a, []byte("A"), 0o644)
	_ = os.WriteFile(b, []byte("B"), 0o644)
	ap, _ := syscall.UTF16PtrFromString(a)
	bp, _ := syscall.UTF16PtrFromString(b)
	err = moveNoReplace(a, b, ap, bp)
	if err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("a rename onto an existing file must fail with ErrExist, got %v", err)
	}
	if got, _ := os.ReadFile(b); string(got) != "B" {
		t.Fatal("the existing file was replaced")
	}
	c := filepath.Join(tmp, "c.txt")
	cp, _ := syscall.UTF16PtrFromString(c)
	if err := moveNoReplace(a, c, ap, cp); err != nil {
		t.Fatalf("rename to a free name: %v", err)
	}
	if got, _ := os.ReadFile(c); string(got) != "A" {
		t.Fatal("renamed content differs")
	}
	if !isDiskFull(&os.PathError{Op: "write", Path: c, Err: syscall.Errno(112)}) {
		t.Fatal("ERROR_DISK_FULL must map to disk_full")
	}
	if fxCode(&os.PathError{Op: "open", Path: c, Err: syscall.ERROR_ACCESS_DENIED}, "x") != fxErrDenied {
		t.Fatal("access denied must map to denied")
	}
}

// TestWriteZoneIdentifier: the Mark-of-the-Web ADS written on a paste-as-RDP
// upload is readable back exactly like the one client-app's localfs_windows.go
// writes for a downloaded working copy (same content, same convention).
func TestWriteZoneIdentifier(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeZoneIdentifier(p); err != nil {
		t.Fatalf("writeZoneIdentifier: %v", err)
	}
	got, err := os.ReadFile(p + ":Zone.Identifier")
	if err != nil || !strings.Contains(string(got), "ZoneId=3") {
		t.Fatalf("Mark-of-the-Web: %q %v", got, err)
	}
}
