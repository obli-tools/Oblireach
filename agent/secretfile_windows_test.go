//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// daclSDDL returns the D: part of the object's security descriptor.
func daclSDDL(t *testing.T, sd *windows.SECURITY_DESCRIPTOR) string {
	t.Helper()
	s := sd.String()
	if i := strings.Index(s, "D:"); i >= 0 {
		return s[i:]
	}
	t.Fatalf("no DACL in %q", s)
	return ""
}

func fileDACL(t *testing.T, p string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo %s: %v", p, err)
	}
	return daclSDDL(t, sd)
}

var aceRe = regexp.MustCompile(`\(([^;]*);([^;]*);([^;]*);([^;]*);([^;]*);([^)]*)\)`)

// assertSystemAdminsOnly: protected DACL, allow ACEs for SYSTEM and
// Administrators only, nothing inherited, no Users / Everyone /
// Authenticated Users / Interactive / Guests ACE.
func assertSystemAdminsOnly(t *testing.T, what, d string) {
	t.Helper()
	if !strings.HasPrefix(d, "D:P") {
		t.Fatalf("%s: DACL not protected: %s", what, d)
	}
	aces := aceRe.FindAllStringSubmatch(d, -1)
	if len(aces) == 0 {
		t.Fatalf("%s: empty DACL: %s", what, d)
	}
	for _, a := range aces {
		if a[1] != "A" || (a[6] != "SY" && a[6] != "BA") || strings.Contains(a[2], "ID") {
			t.Fatalf("%s: unexpected ACE %s in %s", what, a[0], d)
		}
	}
	for _, bad := range []string{";BU)", ";WD)", ";AU)", ";IU)", ";BG)"} {
		if strings.Contains(d, bad) {
			t.Fatalf("%s: DACL still grants %s: %s", what, bad, d)
		}
	}
}

func assertNoTempLeft(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// grantUsers gives BUILTIN\Users modify access (what the ProgramData
// inheritance / an old icacls left on config.json).
func grantUsers(t *testing.T, p string) {
	t.Helper()
	cmd := exec.Command("icacls", p, "/grant", "*S-1-5-32-545:(M)", "/C", "/Q")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("icacls grant (setup): %v (%s)", err, out)
	}
	if d := fileDACL(t, p); !strings.Contains(d, ";BU)") {
		t.Fatalf("setup: expected a BUILTIN\\Users ACE, got %s", d)
	}
}

// elevated: replacing / reading a protected file needs SYSTEM or an elevated
// Administrators token (that is the point); a non-elevated run can only
// create one.
func elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

func TestWriteSecretFileProtectedDACL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := writeSecretFile(p, []byte(`{"apiKey":"k"}`)); err != nil {
		t.Fatalf("writeSecretFile (create): %v", err)
	}
	assertSystemAdminsOnly(t, "config.json", fileDACL(t, p))
	assertNoTempLeft(t, dir)

	if !elevated() {
		t.Skip("replace case needs an elevated token (non-elevated users are locked out, as intended)")
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != `{"apiKey":"k"}` {
		t.Fatalf("read back as admin: %q, %v", b, err)
	}
	grantUsers(t, p)
	if err := writeSecretFile(p, []byte(`{"apiKey":"k2"}`)); err != nil {
		t.Fatalf("writeSecretFile (replace): %v", err)
	}
	assertSystemAdminsOnly(t, "config.json", fileDACL(t, p))
	assertNoTempLeft(t, dir)
}

// An existing config.json written by an older agent (inherited ACL with a
// Users ACE) is replaced by a protected one.
func TestWriteSecretFileReplacesUserReadableFile(t *testing.T) {
	if !elevated() {
		t.Skip("replacing needs SYSTEM or an elevated token (the temp file is SYSTEM + Administrators only)")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"apiKey":"old"}`), 0644); err != nil {
		t.Fatal(err)
	}
	grantUsers(t, p)
	if err := writeSecretFile(p, []byte(`{"apiKey":"new"}`)); err != nil {
		t.Fatalf("writeSecretFile: %v", err)
	}
	assertSystemAdminsOnly(t, "config.json", fileDACL(t, p))
	assertNoTempLeft(t, dir)
}

func TestProtectSecretFileStripsUsersGrant(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"apiKey":"k"}`), 0644); err != nil {
		t.Fatal(err)
	}
	grantUsers(t, p)
	if err := protectSecretFile(p); err != nil {
		t.Fatalf("protectSecretFile: %v", err)
	}
	assertSystemAdminsOnly(t, "config.json", fileDACL(t, p))
	if err := hardenSecretFile(filepath.Join(dir, "missing.json")); err != nil {
		t.Fatalf("hardenSecretFile on a missing file must be a no-op: %v", err)
	}
}

func TestSaveConfigProtected(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldFile := configDir, configFile
	configDir, configFile = dir, filepath.Join(dir, "config.json")
	t.Cleanup(func() { configDir, configFile = oldDir, oldFile })
	if err := saveConfig(&Config{ServerURL: "https://x", APIKey: "k"}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	assertSystemAdminsOnly(t, "config.json", fileDACL(t, configFile))
}

// The registry DACL is applied to a scratch HKCU key (HKLM needs elevation).
// Once applied, a non-elevated user can no longer delete the key: the test
// needs an elevated token.
func TestProtectRegistryKey(t *testing.T) {
	if !elevated() {
		t.Skip("needs an elevated token (the protected key could not be cleaned up)")
	}
	path := `Software\OblireachAgentTest-` + randomHex(4)
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.SetStringValue("ApiKey", "k")
	k.Close()
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })

	if err := protectRegistryKey(registry.CURRENT_USER, path); err != nil {
		t.Fatalf("protectRegistryKey: %v", err)
	}
	k, err = registry.OpenKey(registry.CURRENT_USER, path, windows.READ_CONTROL)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	sd, err := windows.GetSecurityInfo(windows.Handle(k), windows.SE_REGISTRY_KEY, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	assertSystemAdminsOnly(t, "registry key", daclSDDL(t, sd))

	if err := protectRegistryKey(registry.CURRENT_USER, path+`\missing`); err != nil {
		t.Fatalf("missing key must be a no-op: %v", err)
	}
}

// The SDDL strings themselves (shared with installer/product.wxs).
func TestSecretSDDLs(t *testing.T) {
	for _, s := range []string{secretFileSDDL, agentRegistrySDDL} {
		sd, err := windows.SecurityDescriptorFromString(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		assertSystemAdminsOnly(t, s, daclSDDL(t, sd))
	}
	wxs, err := os.ReadFile(filepath.Join("installer", "product.wxs"))
	if err != nil {
		t.Fatal(err)
	}
	w := string(wxs)
	if !strings.Contains(w, `Sddl="`+agentRegistrySDDL+`"`) {
		t.Fatalf("product.wxs: PermissionEx must use %s", agentRegistrySDDL)
	}
	if !regexp.MustCompile(`<Property Id="APIKEY"[^>]*Hidden="yes"`).MatchString(w) {
		t.Fatal(`product.wxs: the APIKEY property must be Hidden="yes" (msiexec logs)`)
	}
	if regexp.MustCompile(`Arguments=[^>]*APIKEY`).MatchString(w) {
		t.Fatal("product.wxs: the API key must not be a service argument (ImagePath is readable by Users)")
	}
	if regexp.MustCompile(`Key="SOFTWARE\\OblireachAgent"[^>]*\s+Name="ApiKey"`).MatchString(w) {
		t.Fatal(`product.wxs: ApiKey must not be written to the Users-readable SOFTWARE\OblireachAgent key`)
	}
}
