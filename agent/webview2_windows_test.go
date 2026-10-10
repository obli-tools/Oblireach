//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jchv/go-webview2/webviewloader"
)

// The Evergreen Bootstrapper the MSI ships (installer/webview2/, Microsoft's
// file, never re-signed). Tests needing it are skipped when it is absent.
const shippedBootstrapper = `installer\webview2\` + webview2BootstrapperName

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWebView2BootstrapperSignature(t *testing.T) {
	if _, err := os.Stat(shippedBootstrapper); err != nil {
		t.Skip("bootstrapper not present: ", err)
	}
	// A copy: the lock checks below must never touch the shipped file.
	p := filepath.Join(t.TempDir(), webview2BootstrapperName)
	copyFile(t, shippedBootstrapper, p)

	if got := peVersionString(p, "OriginalFilename"); !strings.EqualFold(got, webview2OriginalFilename) {
		t.Fatalf("OriginalFilename = %q", got)
	}
	lock, signer, err := verifyWebView2Bootstrapper(p)
	if err != nil {
		t.Fatalf("Microsoft's bootstrapper refused: %v", err)
	}
	if signer != webview2SignerPin {
		t.Fatalf("signer %q", signer)
	}
	// While verified and not yet launched: no write, no rename, no delete.
	if f, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
		f.Close()
		lock.Close()
		t.Fatal("the verified file must not be writable while locked")
	}
	if err := os.Rename(p, p+".x"); err == nil {
		lock.Close()
		t.Fatal("the verified file must not be renamable while locked")
	}
	lock.Close()
	if f, err := os.OpenFile(p, os.O_WRONLY, 0); err != nil {
		t.Fatalf("after the lock is released the file is writable again: %v", err)
	} else {
		f.Close()
	}

	// One byte changed: the signature no longer matches.
	b, _ := os.ReadFile(p)
	b[len(b)/2] ^= 0xFF
	bad := filepath.Join(t.TempDir(), webview2BootstrapperName)
	if err := os.WriteFile(bad, b, 0644); err != nil {
		t.Fatal(err)
	}
	if l, _, err := verifyWebView2Bootstrapper(bad); err == nil {
		l.Close()
		t.Fatal("a modified bootstrapper must be refused")
	}
}

func TestWebView2VerifyRefusesOtherPrograms(t *testing.T) {
	// Unsigned program (this test binary) under the bootstrapper's name.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), webview2BootstrapperName)
	copyFile(t, exe, p)
	if l, _, err := verifyWebView2Bootstrapper(p); err == nil {
		l.Close()
		t.Fatal("an unsigned program must be refused")
	}
	// A genuine Microsoft-signed program that is not Edge Update's installer.
	matches, _ := filepath.Glob(`C:\Program Files (x86)\Microsoft\EdgeWebView\Application\*\msedgewebview2.exe`)
	if len(matches) == 0 {
		t.Log("no msedgewebview2.exe on this machine: other-Microsoft-program case skipped")
		return
	}
	q := filepath.Join(t.TempDir(), webview2BootstrapperName)
	copyFile(t, matches[0], q)
	l, signer, err := verifyWebView2Bootstrapper(q)
	if err == nil {
		l.Close()
		t.Fatalf("msedgewebview2.exe (signed by %q) accepted as the bootstrapper", signer)
	}
	if !strings.Contains(err.Error(), "not the Microsoft Edge Update installer") {
		t.Fatalf("refused for another reason: %v", err)
	}
}

func TestWebView2LockAllowsExec(t *testing.T) {
	// The lock (read, share read only) must not prevent CreateProcess.
	p := filepath.Join(t.TempDir(), "cmdcopy.exe")
	copyFile(t, filepath.Join(systemDir(), "cmd.exe"), p)
	lock, err := lockFileForExec(p)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = exec.Command(p, "/c", "exit 3").Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("exec of a locked file: %v", err)
	}
}

func TestWebView2LaunchAndAlive(t *testing.T) {
	// ping.exe copied under the bootstrapper's name stands for a running installer.
	dir := t.TempDir()
	p := filepath.Join(dir, webview2BootstrapperName)
	copyFile(t, filepath.Join(systemDir(), "PING.EXE"), p)
	lock, err := lockFileForExec(p)
	if err != nil {
		t.Fatal(err)
	}
	pid, wait, err := launchWebView2Bootstrapper(p, lock)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
		// Image mapped: Windows keeps the file from being rewritten anyway.
		f.Close()
		t.Fatal("running image writable")
	}
	if !webview2InstallerAlive(pid) {
		t.Fatal("the running installer is alive")
	}
	if webview2InstallerAlive(os.Getpid()) {
		t.Fatal("another program under that PID is not the installer")
	}
	// "/silent /install" are not ping arguments: it exits at once.
	code, timedOut, werr := wait(30 * time.Second)
	if timedOut || werr != nil {
		t.Fatalf("wait: code=%d timedOut=%v err=%v", code, timedOut, werr)
	}
	if webview2InstallerAlive(pid) {
		t.Fatal("exited process still alive")
	}
}

// fakeInstallerEnv: this test binary, copied under the bootstrapper's name
// and started with the variable set, plays an installer that never ends.
const fakeInstallerEnv = "OBLIREACH_WV2_FAKE_INSTALLER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeInstallerEnv) == "hang" {
		time.Sleep(3 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWebView2LaunchTimeoutStopsIt(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), webview2BootstrapperName)
	copyFile(t, exe, p)
	t.Setenv(fakeInstallerEnv, "hang")
	pid, wait, err := launchWebView2Bootstrapper(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !webview2InstallerAlive(pid) {
		t.Fatal("hanging installer must be alive")
	}
	start := time.Now()
	code, timedOut, _ := wait(2 * time.Second)
	if !timedOut || code != -1 {
		t.Fatalf("wait: code=%d timedOut=%v", code, timedOut)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("wait must return shortly after the timeout")
	}
	if webview2InstallerAlive(pid) {
		t.Fatal("a timed-out installer is stopped")
	}
}

func TestWebView2DetectThisMachine(t *testing.T) {
	d := platformWebView2Detect()
	t.Logf("detection: version=%q scope=%s policyBlocked=%v unsupported=%v err=%q", d.Version, d.Scope, d.PolicyBlocked, d.Unsupported, d.Err)
	if wv2OSTooOld() {
		t.Fatal("this build machine runs Windows 10+ / Server 2016+")
	}
	if pv := wv2MachinePV(); pv != "" {
		if d.Version == "" || d.Scope != "machine" {
			t.Fatalf("per-machine runtime %s registered but detection says %+v", pv, d)
		}
	}
	// Same answer as the WebView2 loader the chat helper uses (loaded here,
	// in the test process only — the service never loads it).
	lv, err := webviewloader.GetInstalledVersion()
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	if strings.TrimSpace(lv) != d.Version {
		t.Fatalf("registry detection %q, loader %q", d.Version, lv)
	}
	// A registration whose files are gone is not a runtime.
	var v string
	if wv2Installed(syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\NoSuchVendor\EdgeUpdate\Clients\`+webview2ClientGUID, &v) || v != "" {
		t.Fatal("no key: not installed")
	}
	if d.Version != "" && (d.PolicyBlocked || d.Unsupported) {
		t.Fatal("policy / OS diagnostics are only computed when the runtime is missing")
	}
}

func TestWebView2ToastText(t *testing.T) {
	for _, fr := range []bool{false, true} {
		for _, inst := range []bool{false, true} {
			title, msg := webview2ToastText(fr, inst)
			if title == "" || msg == "" || strings.ContainsAny(title+msg, `"\`) {
				t.Fatalf("toast text must be non-empty and command-line safe: %q %q", title, msg)
			}
			if fr && (strings.Contains(msg, " tu ") || strings.Contains(msg, " ton ") || strings.Contains(msg, " ta ")) {
				t.Fatalf("formal French only: %q", msg)
			}
			if inst != (strings.Contains(msg, "installation est en cours") || strings.Contains(msg, "being installed")) {
				t.Fatalf("installing flag not reflected: %q", msg)
			}
		}
	}
}
