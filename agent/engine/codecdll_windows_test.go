//go:build windows

package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// copyDistDLLs copies the listed agent/dist DLLs into a new temporary folder.
func copyDistDLLs(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join("..", "dist", n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func needAMD64(t *testing.T) {
	t.Helper()
	if runtime.GOARCH != "amd64" {
		t.Skip("the shipped codec DLLs are 64-bit")
	}
}

func refusal(dir, name string) string {
	cdlMu.Lock()
	defer cdlMu.Unlock()
	if err := cdlRefused[cdlKey(dir, name)]; err != nil {
		return err.Error()
	}
	return ""
}

// cdlForget unloads what the loader loaded from dir (the test's temporary
// folder can then be removed).
func cdlForget(t *testing.T, dir string) {
	t.Cleanup(func() {
		cdlMu.Lock()
		defer cdlMu.Unlock()
		prefix := strings.ToLower(dir) + `\`
		for k, h := range cdlLoaded {
			if strings.HasPrefix(k, prefix) {
				cdlFreeLibrary.Call(uintptr(h))
				delete(cdlLoaded, k)
			}
		}
	})
}

// TestBundledDLLsGenuineLoad: the genuine shipped DLLs pass the verification
// and load (the agent keeps its bundled codecs).
func TestBundledDLLsGenuineLoad(t *testing.T) {
	needAMD64(t)
	dir := copyDistDLLs(t, "openh264-2.4.1-win64.dll", "libvpx-1.dll", "libwinpthread-1.dll", "libSvtAv1Enc-2.dll")
	cdlForget(t, dir)
	for name, fn := range map[string]string{
		"openh264-2.4.1-win64.dll;openh264.dll": "WelsCreateSVCEncoder",
		"libSvtAv1Enc-2.dll;SvtAv1Enc.dll":      "svt_av1_enc_init_handle",
	} {
		h := loadBundledDLLFrom(dir, strings.Split(name, ";"))
		if h == 0 {
			t.Errorf("%s: refused (%s)", name, refusal(dir, strings.ToLower(strings.Split(name, ";")[0])))
			continue
		}
		if _, err := syscall.GetProcAddress(h, fn); err != nil {
			t.Errorf("%s: %s: %v", name, fn, err)
		}
	}

	// libvpx imports libwinpthread-1.dll: the x86-64 build shipped next to it
	// (pinned) must resolve, or VP9 is silently unavailable (an i386 build
	// was shipped until 2026-10: refused here with a machine mismatch, and
	// LoadLibrary fails with ERROR_BAD_EXE_FORMAT).
	h := loadBundledDLLFrom(dir, []string{"libvpx-1.dll"})
	if h == 0 {
		t.Fatalf("libvpx refused (%s)", refusal(dir, "libvpx-1.dll"))
	}
	proc, err := syscall.GetProcAddress(h, "vpx_codec_vp9_cx")
	if err != nil {
		t.Fatal(err)
	}
	if iface, _, _ := syscall.SyscallN(proc); iface == 0 {
		t.Error("vpx_codec_vp9_cx returned no encoder interface")
	}
}

// TestDistDLLsAreAMD64: every DLL the MSI ships from agent/dist is an
// x86-64 image (the agent is a 64-bit process).
func TestDistDLLsAreAMD64(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "dist", "*.dll"))
	if len(files) == 0 {
		t.Fatal("no DLL in agent/dist")
	}
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := peMachineMatches(data, "amd64"); err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
		}
	}
}

func TestBundledDLLTamperedRefused(t *testing.T) {
	needAMD64(t)
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "dist", "openh264-2.4.1-win64.dll"))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-100] ^= 0x55
	if err := os.WriteFile(filepath.Join(dir, "openh264-2.4.1-win64.dll"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if h := loadBundledDLLFrom(dir, []string{"openh264-2.4.1-win64.dll"}); h != 0 {
		t.Fatal("modified OpenH264 DLL loaded")
	}
	if r := refusal(dir, "openh264-2.4.1-win64.dll"); !strings.Contains(r, "does not match") {
		t.Errorf("refusal %q", r)
	}
}

func TestBundledDLLUnpinnedNameRefused(t *testing.T) {
	needAMD64(t)
	dir := copyDistDLLs(t, "openh264-2.4.1-win64.dll")
	if err := os.Rename(filepath.Join(dir, "openh264-2.4.1-win64.dll"), filepath.Join(dir, "openh264.dll")); err != nil {
		t.Fatal(err)
	}
	if h := loadBundledDLLFrom(dir, []string{"openh264-2.4.1-win64.dll", "openh264.dll"}); h != 0 {
		t.Fatal("DLL under an unpinned name loaded")
	}
	if r := refusal(dir, "openh264.dll"); !strings.Contains(r, "no pin") {
		t.Errorf("refusal %q", r)
	}
}

// TestBundledDLLPlantedDependencyRefused: a genuine codec DLL next to a
// planted dependency is refused (the dependency is verified first).
func TestBundledDLLPlantedDependencyRefused(t *testing.T) {
	needAMD64(t)
	dir := copyDistDLLs(t, "libvpx-1.dll", "openh264-2.4.1-win64.dll")
	cdlForget(t, dir)
	if err := os.Rename(filepath.Join(dir, "openh264-2.4.1-win64.dll"), filepath.Join(dir, "libwinpthread-1.dll")); err != nil {
		t.Fatal(err)
	}
	if h := loadBundledDLLFrom(dir, []string{"libvpx-1.dll"}); h != 0 {
		t.Fatal("libvpx loaded with a planted libwinpthread-1.dll")
	}
	if r := refusal(dir, "libvpx-1.dll"); !strings.Contains(r, "libwinpthread-1.dll") {
		t.Errorf("refusal %q", r)
	}
	// Dependency absent: refused too.
	dir2 := copyDistDLLs(t, "libvpx-1.dll")
	if h := loadBundledDLLFrom(dir2, []string{"libvpx-1.dll"}); h != 0 {
		t.Fatal("libvpx loaded without its verified dependency")
	}
}

func TestBundledDLLsDisabled(t *testing.T) {
	SetBundledDLLs(false)
	defer SetBundledDLLs(true)
	if BundledDLLsEnabled() {
		t.Fatal("still enabled")
	}
	if h := loadBundledDLL([]string{"openh264-2.4.1-win64.dll"}); h != 0 {
		t.Fatal("a bundled DLL was loaded while disabled")
	}
}

// TestCodecDLLOpenLockedDeniesChanges: while the verifier holds a DLL, it
// cannot be written, renamed or deleted.
func TestCodecDLLOpenLockedDeniesChanges(t *testing.T) {
	dir := copyDistDLLs(t, "libwinpthread-1.dll")
	p := filepath.Join(dir, "libwinpthread-1.dll")
	f, err := cdlOpenLocked(p)
	if err != nil {
		t.Fatal(err)
	}
	if w, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
		w.Close()
		t.Error("opened for writing while locked")
	}
	if err := os.Rename(p, p+".x"); err == nil {
		t.Error("renamed while locked")
	}
	if err := os.Remove(p); err == nil {
		t.Error("deleted while locked")
	}
	f.Close()
	if _, err := cdlOpenLocked(filepath.Join(dir, "absent.dll")); err != errDLLAbsent {
		t.Errorf("absent file: %v", err)
	}
	// A writer already holding the file makes the verifier's open fail.
	w, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if f, err := cdlOpenLocked(p); err == nil {
		f.Close()
		t.Error("opened while another handle can write")
	}
}

func TestNewSystemLazyDLLUsesSystem32(t *testing.T) {
	sys := systemDirectory()
	if sys == "" {
		t.Fatal("no System32 path")
	}
	d := newSystemLazyDLL("wtsapi32.dll")
	if !strings.EqualFold(d.Name, sys+`\wtsapi32.dll`) {
		t.Errorf("name %q", d.Name)
	}
	if err := d.Load(); err != nil {
		t.Fatal(err)
	}
	if err := d.NewProc("WTSQuerySessionInformationW").Find(); err != nil {
		t.Error(err)
	}
}
