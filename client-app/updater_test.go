package main

import (
	"strings"
	"testing"
	"time"
)

// The installer's URL is built from the active profile's address and a fixed
// path: only the scheme and host of the profile are kept.
func TestUpdateDownloadURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://obl.example":                     "https://obl.example/api/oblireach-desktop/download",
		"https://obl.example/":                    "https://obl.example/api/oblireach-desktop/download",
		"https://obl.example/some/path?x=1#frag":  "https://obl.example/api/oblireach-desktop/download",
		"http://10.0.0.5:3000":                    "http://10.0.0.5:3000/api/oblireach-desktop/download",
		" https://user:pass@obl.example:8443/x  ": "https://obl.example:8443/api/oblireach-desktop/download",
	} {
		got, err := updateDownloadURL(in)
		if err != nil || got != want {
			t.Fatalf("updateDownloadURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "obl.example", "javascript:alert(1)", "file:///C:/x.msi", "ftp://obl.example", "https://", "//obl.example"} {
		if got, err := updateDownloadURL(in); err == nil {
			t.Fatalf("updateDownloadURL(%q) = %q, want an error", in, got)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0.12", "1.0.11", 1}, {"1.0.11", "1.0.12", -1}, {"1.0.11", "1.0.11", 0},
		{"1.0.10", "1.0.9", 1}, // numeric, not lexical
		{"1.1", "1.0.99", 1}, {"2.0.0", "1.99.99", 1}, {"1.0", "1.0.0", 0},
		{"v1.0.12", "1.0.12", 0}, {"1.0.0.1", "1.0.0", 1}, {" 1.0.3 ", "1.0.2", 1},
	} {
		got, ok := compareVersions(c.a, c.b)
		if !ok || got != c.want {
			t.Fatalf("compareVersions(%q, %q) = %d, %v; want %d", c.a, c.b, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "1.0.x", "1..2", "-1.0", "1.0.0.0.0", "1.0.+1", "1.0.1-beta", "abc", "1.0.9999999999"} {
		if _, ok := parseVersion(bad); ok {
			t.Fatalf("parseVersion(%q) accepted", bad)
		}
		if versionNewer(bad, "1.0.0") || versionNewer("9.9.9", bad) {
			t.Fatalf("versionNewer with %q", bad)
		}
	}
	if !versionNewer("1.0.12", "1.0.11") || versionNewer("1.0.11", "1.0.11") || versionNewer("1.0.10", "1.0.11") {
		t.Fatal("versionNewer: no downgrade, no reinstall")
	}
	if !sameUpgradeCode("{a1b2c3d4-e5f6-7890-abcd-0b11eacc0001}", desktopUpgradeCode) ||
		!sameUpgradeCode("A1B2C3D4-E5F6-7890-ABCD-0B11EACC0001", desktopUpgradeCode) ||
		sameUpgradeCode("{A1B2C3D4-E5F6-7890-ABCD-0B11EACC0002}", desktopUpgradeCode) || sameUpgradeCode("", "") {
		t.Fatal("sameUpgradeCode")
	}
}

func testScriptParams(dir string) updateScriptParams {
	sys := `C:\Windows\system32`
	return updateScriptParams{
		Tasklist: sys + `\tasklist.exe`, Find: sys + `\find.exe`, Ping: sys + `\PING.EXE`, Msiexec: sys + `\msiexec.exe`,
		Explorer: `C:\Windows\explorer.exe`, PID: 4242, Image: "ObliReach.exe",
		MSI:       dir + `\Oblireach-update-0123456789abcdef\OblireachDesktop.msi`,
		StageBase: `C:\Program Files\OblireachClient\Update`,
		Stage:     `C:\Program Files\OblireachClient\Update\fedcba9876543210`,
		Exe:       `C:\Program Files\OblireachClient\ObliReach.exe`,
	}
}

// The script is one parenthesised block (parsed at once), CRLF, printable
// ASCII, every path between double quotes, the tools by absolute path.
func TestUpdateScriptShape(t *testing.T) {
	dir := `C:\Users\Jo Doe (IT) & Co ^1 !x 'y\AppData\Local\Temp`
	p := testScriptParams(dir)
	s, err := updateScript(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "@echo off\r\n(\r\n") || !strings.HasSuffix(s, "\r\n)\r\n") {
		t.Fatalf("not one block:\n%s", s)
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") || strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\r") {
		t.Fatal("line endings are not CRLF")
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\r' && c != '\n') || c > 0x7e {
			t.Fatalf("non printable ASCII byte 0x%02x", c)
		}
	}
	// Past its first line, the block is closed exactly once (the last line).
	body := strings.Split(strings.TrimSuffix(s, "\r\n"), "\r\n")
	if body[1] != "(" || body[len(body)-1] != ")" {
		t.Fatalf("block delimiters: %q ... %q", body[1], body[len(body)-1])
	}
	// ... and it exits from inside: cmd never reads the file again (after
	// the app exits, a line appended to it would run elevated).
	if body[len(body)-2] != "exit /b 0" || strings.Count(s, "exit /b") != 1 {
		t.Fatalf("the block does not end with exit /b 0: %q", body[len(body)-2])
	}
	for _, want := range []string{
		`"` + p.MSI + `"`, `"` + p.Stage + `\OblireachDesktop.msi"`, `"` + p.Exe + `"`, `"` + p.Explorer + `" "` + p.Exe + `"`,
		`"` + p.Tasklist + `" /FI "PID eq 4242" /FO CSV /NH`, `"` + p.Find + `" /i "ObliReach.exe"`,
		`start "" /b /wait "` + p.Msiexec + `" /i "` + p.Stage + `\OblireachDesktop.msi" /passive /norestart /l*v "` + p.Stage + `\install.log"`,
		`for /l %%i in (1,1,60) do if not defined OBR_GONE (`, `call set "OBR_RC=%%ERRORLEVEL%%"`,
		`set "OBR_ABORT="`, `set "OBR_GONE="`, `set "OBR_OK="`, `set "OBR_RC="`, `set "OBR_MADE="`,
		`>"` + p.Stage + `\copied.txt" echo ok`, `>"` + p.Stage + `\result.txt" set OBR_RC`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("script lacks %s:\n%s", want, s)
		}
	}
	// No tool through PATH or the environment (a user can redefine them).
	for _, bad := range []string{"%SystemRoot%", "%windir%", "%ComSpec%", "%TEMP%", " msiexec ", " explorer ", " taskkill", "powershell"} {
		if strings.Contains(strings.ToLower(s), strings.ToLower(bad)) {
			t.Fatalf("script uses %q:\n%s", bad, s)
		}
	}
	// No pipe (each side would run in a child cmd started through %ComSpec%):
	// only the || of "command || set flag".
	if strings.Contains(strings.ReplaceAll(s, "||", ""), "|") {
		t.Fatalf("script has a pipe:\n%s", s)
	}
	// Percent signs only in the batch escapes it needs.
	rest := s
	for _, ok := range []string{"%%~fd", "%%d", "%%i", "%%ERRORLEVEL%%"} {
		rest = strings.ReplaceAll(rest, ok, "")
	}
	if strings.Contains(rest, "%") {
		t.Fatalf("stray %% in the script:\n%s", s)
	}
	// The copy comes before the wait, the wait before msiexec, msiexec before
	// the relaunch.
	order := []string{"copy /b /y", "copied.txt", "for /l", "msiexec.exe", "explorer.exe", "exit /b 0"}
	last := -1
	for _, o := range order {
		i := strings.Index(s, o)
		if i <= last {
			t.Fatalf("%q out of order", o)
		}
		last = i
	}
	if a := updateCmdArgs(`C:\x y\update.cmd`); a != `/d /e:on /v:off /s /c ""C:\x y\update.cmd""` {
		t.Fatalf("cmd args: %s", a)
	}
}

func TestUpdateScriptRefusesUnsafeValues(t *testing.T) {
	base := testScriptParams(`C:\Temp`)
	cases := map[string]func(p *updateScriptParams){
		"percent in MSI":        func(p *updateScriptParams) { p.MSI = `C:\Temp\%USERPROFILE%\a.msi` },
		"quote in exe":          func(p *updateScriptParams) { p.Exe = `C:\x"\ObliReach.exe` },
		"non-ASCII MSI":         func(p *updateScriptParams) { p.MSI = `C:\Users\Jérôme\a.msi` },
		"newline in stage":      func(p *updateScriptParams) { p.Stage = p.StageBase + "\\x\r\ncalc" },
		"relative tool":         func(p *updateScriptParams) { p.Msiexec = `msiexec.exe` },
		"tool from env":         func(p *updateScriptParams) { p.Explorer = `%windir%\explorer.exe` },
		"stage outside base":    func(p *updateScriptParams) { p.Stage = `C:\Program Files\Other\x` },
		"stage grandchild":      func(p *updateScriptParams) { p.Stage = p.StageBase + `\a\b` },
		"stage is base":         func(p *updateScriptParams) { p.Stage = p.StageBase },
		"stage trailing slash":  func(p *updateScriptParams) { p.Stage = p.StageBase + `\x\` },
		"image with quote":      func(p *updateScriptParams) { p.Image = `a" & calc & "` },
		"image with percent":    func(p *updateScriptParams) { p.Image = `%x%.exe` },
		"no pid":                func(p *updateScriptParams) { p.PID = 0 },
		"base with prefix only": func(p *updateScriptParams) { p.Stage = p.StageBase + `x\y` },
	}
	for name, mod := range cases {
		p := base
		mod(&p)
		if s, err := updateScript(p); err == nil {
			t.Fatalf("%s: accepted:\n%s", name, s)
		}
	}
	if _, err := updateScript(base); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateOutcome(t *testing.T) {
	pu := pendingUpdate{Version: "1.0.12", From: "1.0.11", At: time.Now()}
	if r := updateOutcome(pu, "1.0.12", "", `C:\l.log`); !r.OK || r.Message != "Oblireach was updated to v1.0.12." || r.Log != "" {
		t.Fatalf("updated: %+v", r)
	}
	if r := updateOutcome(pu, "1.0.13", "OBR_RC=1603", ""); !r.OK {
		t.Fatalf("newer running: %+v", r)
	}
	r := updateOutcome(pu, "1.0.11", "OBR_RC=1603\r\n", `C:\s\install.log`)
	if r.OK || r.Code != 1603 || r.Log != `C:\s\install.log` || !strings.Contains(r.Message, "error 1603") {
		t.Fatalf("failed: %+v", r)
	}
	if r := updateOutcome(pu, "1.0.11", "OBR_RC=3010", `C:\l`); r.OK || !r.Reboot || r.Code != 3010 || !strings.Contains(r.Message, "Windows restarts") {
		t.Fatalf("reboot: %+v", r)
	}
	if r := updateOutcome(pu, "1.0.11", "OBR_RC=1602", `C:\l`); r.OK || r.Code != 1602 || !strings.Contains(r.Message, "cancelled") {
		t.Fatalf("cancelled: %+v", r)
	}
	if r := updateOutcome(pu, "1.0.11", "", `C:\l`); r.OK || r.Code != 0 || !strings.Contains(r.Message, "did not complete") {
		t.Fatalf("no result: %+v", r)
	}
	if r := updateOutcome(pu, "1.0.11", "OBR_RC=abc", ""); r.OK || r.Code != 0 {
		t.Fatalf("garbage result: %+v", r)
	}
	for in, want := range map[string]int{"OBR_RC=1603": 1603, " OBR_RC=5 \r\n": 5, "OBR_RC=1603\r\nOBR_RCX=1": 1603} {
		if n, ok := parseUpdateRC(in); !ok || n != want {
			t.Fatalf("parseUpdateRC(%q) = %d %v", in, n, ok)
		}
	}
}

// The Run value: the quoted exe, --minimized when asked; read back whatever
// its shape.
func TestRunKeyCommand(t *testing.T) {
	exe := `C:\Program Files\OblireachClient\ObliReach.exe`
	if v, err := runKeyCommand(exe, false); err != nil || v != `"C:\Program Files\OblireachClient\ObliReach.exe"` {
		t.Fatalf("plain: %q %v", v, err)
	}
	if v, err := runKeyCommand(exe, true); err != nil || v != `"C:\Program Files\OblireachClient\ObliReach.exe" --minimized` {
		t.Fatalf("minimized: %q %v", v, err)
	}
	for _, bad := range []string{"", `C:\a"b.exe`, "C:\\a\r\nb.exe", "C:\\a\x00.exe"} {
		if _, err := runKeyCommand(bad, false); err == nil {
			t.Fatalf("runKeyCommand(%q) accepted", bad)
		}
	}
	for in, want := range map[string]struct {
		exe string
		min bool
	}{
		`"C:\Program Files\X\ObliReach.exe" --minimized`: {`C:\Program Files\X\ObliReach.exe`, true},
		`"C:\Program Files\X\ObliReach.exe"`:             {`C:\Program Files\X\ObliReach.exe`, false},
		`C:\X\ObliReach.exe --MINIMIZED`:                 {`C:\X\ObliReach.exe`, true},
		`C:\X\ObliReach.exe`:                             {`C:\X\ObliReach.exe`, false},
		`"C:\X\ObliReach.exe" --minimizedx`:              {`C:\X\ObliReach.exe`, false},
		`"C:\X\unterminated.exe`:                         {`C:\X\unterminated.exe`, false},
	} {
		exe, min := parseRunKeyCommand(in)
		if exe != want.exe || min != want.min {
			t.Fatalf("parseRunKeyCommand(%q) = %q %v", in, exe, min)
		}
	}
	for data, want := range map[string]bool{"": true, "\x02\x00\x00": true, "\x06": true, "\x03\x00": false, "\x07": false} {
		if got := startupApprovedEnabled([]byte(data), true); got != want {
			t.Fatalf("approved %x = %v", data, got)
		}
	}
	if !startupApprovedEnabled(nil, false) {
		t.Fatal("no StartupApproved value: enabled")
	}
	if !hasArg([]string{"-x", "--Minimized"}, startMinimizedArg) || hasArg([]string{"--minimized=1"}, startMinimizedArg) || hasArg(nil, startMinimizedArg) {
		t.Fatal("hasArg")
	}
}
