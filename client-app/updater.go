package main

// One-click self-update of the Desktop (download in-process, verify the
// signature, install, relaunch):
//
//  1. "Update now" in the update banner (ui.go) calls reachInstallUpdate. The
//     MSI is downloaded IN THIS PROCESS (no PowerShell, no downloader process:
//     the "trojan downloader" heuristic signal ran into) from the ACTIVE
//     server profile at a fixed path: the page never gives the URL.
//  2. It lands in a fresh folder under %TEMP% that only this user (and the
//     administrators) can write, and is kept open deny-write/deny-delete from
//     before its verification until this process ends.
//  3. Verified: WinVerifyTrust (valid chain), a signer with the SAME subject
//     as the signer of the running Oblireach.exe (an unsigned build refuses:
//     it has no reference), the Desktop's UpgradeCode, and a ProductVersion
//     newer than this one (no downgrade from the banner).
//  4. ONE elevation (ShellExecuteEx "runas": one UAC prompt) runs a small
//     generated .cmd (updateScript) that copies the MSI into an admin-only
//     folder at once (while this process still holds it), waits for this
//     process to end (bounded, by PID), runs msiexec /passive /norestart on
//     the copy, and relaunches the installed Oblireach through explorer.exe
//     (normal integrity, not elevated). It downloads nothing.
//  5. The app then closes the clean way (its sessions and chats end as on
//     sign-out) and the next run tells how the update went.
//
// This file holds the pure parts (tested on their own); the Win32 side is in
// updater_windows.go.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// Path of the Desktop's MSI on an Obliance server (public route,
	// server/src/routes/oblireachDesktop.routes.ts).
	desktopMSIPath = "/api/oblireach-desktop/download"
	// The Desktop's UpgradeCode (installer/product.wxs): the ObliReach agent's
	// MSI is signed by the same certificate and must never be taken for it.
	desktopUpgradeCode = "{A1B2C3D4-E5F6-7890-ABCD-0B11EACC0001}"
	// Where the MSI installs the app (installer/product.wxs: ProgramFiles64
	// \ OblireachClient \ ObliReach.exe).
	desktopInstallDir = "OblireachClient"
	desktopExeName    = "ObliReach.exe"
	// The downloaded MSI, in the private folder and in the admin-only one.
	updateMSIName = "OblireachDesktop.msi"
	// Name prefix of the private download folders under %TEMP%.
	updateTempPrefix = "Oblireach-update-"
	// Admin-only staging folders: <ProgramFiles>\OblireachClient\Update\<id>.
	updateStageDir = "Update"
	// Written by the elevated script into the staging folder once the MSI is
	// copied there (the app then closes), and its outcome when it failed.
	updateCopiedMarker = "copied.txt"
	updateResultFile   = "result.txt"
	updateLogFile      = "install.log"
	// Largest installer accepted (today's is ~6 MB).
	maxUpdateMSISize = 256 << 20
	// How long the elevated script waits for this process to end (seconds,
	// about one per round), and how long the app waits for the script to
	// have copied the MSI before it gives up (and stays open).
	updateWaitRounds  = 60
	updateCopyTimeout = 45 * time.Second
)

// updateDownloadURL: the installer's URL on a server profile's address. Only
// the scheme and host of the profile are used (servers.go serverOrigin), the
// path is fixed: nothing of it comes from the page.
func updateDownloadURL(serverURL string) (string, error) {
	o, ok := serverOrigin(serverURL)
	if !ok {
		return "", errors.New("no server configured")
	}
	return o + desktopMSIPath, nil
}

// parseVersion reads "1.0.12" (up to 4 numeric parts, as MSI versions and
// version.txt are written). ok is false for anything else.
func parseVersion(s string) ([4]int, bool) {
	var v [4]int
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	parts := strings.Split(s, ".")
	if s == "" || len(parts) > 4 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return v, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strings.TrimLeft(p, "0123456789") != "" {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// compareVersions: -1, 0 or 1; ok is false when either one does not parse.
func compareVersions(a, b string) (int, bool) {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range va {
		if va[i] != vb[i] {
			if va[i] > vb[i] {
				return 1, true
			}
			return -1, true
		}
	}
	return 0, true
}

// versionNewer: candidate is strictly newer than current (both valid).
func versionNewer(candidate, current string) bool {
	c, ok := compareVersions(candidate, current)
	return ok && c > 0
}

// sameUpgradeCode compares two MSI GUIDs, braces and case aside.
func sameUpgradeCode(a, b string) bool {
	norm := func(s string) string {
		return strings.ToUpper(strings.Trim(strings.TrimSpace(s), "{}"))
	}
	return norm(a) != "" && norm(a) == norm(b)
}

// ── The elevated script ────────────────────────────────────────────────────

// updateScriptParams: everything the elevated script needs, all absolute.
type updateScriptParams struct {
	// Tools, by absolute path (never through PATH, %SystemRoot% or
	// %ComSpec%: a user can redefine those, and this runs elevated).
	Tasklist, Find, Ping, Msiexec, Explorer string
	PID                                     int    // this process
	Image                                   string // its image name (tasklist)
	MSI                                     string // the verified MSI (private folder)
	StageBase                               string // <ProgramFiles>\OblireachClient\Update
	Stage                                   string // StageBase\<id>: created by the script
	Exe                                     string // the installed Oblireach, relaunched
}

// scriptPathOK: a path the script can hold between double quotes, read back
// the same by cmd whatever the console code page: absolute, printable ASCII
// (non-ASCII paths go through their 8.3 names), no double quote (it would
// end the quoted string) and no percent sign (cmd expands %...% even in
// quotes).
func scriptPathOK(p string) bool {
	if len(p) < 3 || len(p) > 32000 {
		return false
	}
	if !(p[1] == ':' && p[2] == '\\' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))) &&
		!strings.HasPrefix(p, `\\`) {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '%' {
			return false
		}
	}
	return !strings.HasSuffix(p, `\`) || len(p) == 3
}

// imageNameOK: the process image name matched in tasklist's output.
func imageNameOK(s string) bool {
	if s == "" || len(s) > 255 || strings.ContainsAny(s, `\/:*?"<>|%`) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// updateScript builds the .cmd the elevated cmd.exe runs (CRLF, ASCII).
//
// It is ONE parenthesised block: cmd reads and parses a block in full before
// running any of it, and it starts right after the UAC prompt, while this
// process still holds the script and the MSI deny-write. Whatever happens to
// the file afterwards, what runs is what was verified. Inside a block %VAR%
// expands at parse time: the flags are tested with "if defined" (run time)
// and the exit code of msiexec is read through "call set".
//
// The block's last command is "exit /b 0": after a block, cmd reopens the
// file and reads on from where the block ended. By then this process has
// exited (its locks are gone) and the file is in the user's %TEMP%, so a
// line appended meanwhile would run elevated; exiting from inside the block
// (already parsed) never reads the file again.
//
// Steps:
//  1. the staging folders of earlier updates go (only administrators can
//     write under StageBase: nothing there can be a user's junction);
//  2. the staging folder is created (it must not exist) and the MSI copied
//     into it: from here on it is out of reach of the user;
//  3. copied.txt tells the app, which then closes;
//  4. wait for this process to end, at most updateWaitRounds x ~1 s (it is
//     never killed: if it is still there, nothing is installed, and the
//     staging folder goes, as after any other abort);
//  5. msiexec /i /passive /norestart on the copy, logged next to it;
//  6. success: the staging folder goes; failure: its log stays, with
//     result.txt (OBR_RC=<msiexec exit code>) for the next run of the app;
//  7. the installed Oblireach is started again through explorer.exe, so that
//     it runs at the normal integrity level of the session, not elevated.
func updateScript(p updateScriptParams) (string, error) {
	for name, v := range map[string]string{
		"tasklist": p.Tasklist, "find": p.Find, "ping": p.Ping, "msiexec": p.Msiexec, "explorer": p.Explorer,
		"msi": p.MSI, "stage base": p.StageBase, "stage": p.Stage, "exe": p.Exe,
	} {
		if !scriptPathOK(v) {
			return "", fmt.Errorf("unsupported %s path", name)
		}
	}
	if !strings.EqualFold(p.StageBase+`\`, p.Stage[:min(len(p.Stage), len(p.StageBase)+1)]) ||
		strings.Contains(p.Stage[len(p.StageBase)+1:], `\`) || len(p.Stage) <= len(p.StageBase)+1 {
		return "", errors.New("the staging folder must be a direct child of its base")
	}
	if !imageNameOK(p.Image) {
		return "", errors.New("unsupported image name")
	}
	if p.PID <= 0 {
		return "", errors.New("invalid process id")
	}
	q := func(s string) string { return `"` + s + `"` }
	stageMSI := p.Stage + `\` + updateMSIName
	lines := []string{
		"@echo off",
		"(",
		`set "OBR_ABORT="`,
		`set "OBR_GONE="`,
		`set "OBR_OK="`,
		`set "OBR_RC="`,
		`set "OBR_MADE="`,
		`if exist ` + q(p.StageBase+`\`) + ` for /d %%d in (` + q(p.StageBase+`\*`) + `) do rd /s /q "%%~fd" >nul 2>nul`,
		`if exist ` + q(p.Stage) + ` set "OBR_ABORT=1"`,
		`if not defined OBR_ABORT (`,
		`  mkdir ` + q(p.Stage) + ` >nul 2>nul && set "OBR_MADE=1" || set "OBR_ABORT=1"`,
		`)`,
		`if not defined OBR_ABORT (`,
		`  copy /b /y ` + q(p.MSI) + ` ` + q(stageMSI) + ` >nul 2>nul || set "OBR_ABORT=1"`,
		`)`,
		`if not defined OBR_ABORT (`,
		`  >` + q(p.Stage+`\`+updateCopiedMarker) + ` echo ok`,
		`  for /l %%i in (1,1,` + strconv.Itoa(updateWaitRounds) + `) do if not defined OBR_GONE (`,
		`    ` + q(p.Tasklist) + ` /FI "PID eq ` + strconv.Itoa(p.PID) + `" /FO CSV /NH >` + q(p.Stage+`\pid.txt`) + ` 2>nul`,
		`    ` + q(p.Find) + ` /i ` + q(p.Image) + ` ` + q(p.Stage+`\pid.txt`) + ` >nul 2>nul`,
		`    if errorlevel 1 (set "OBR_GONE=1") else (` + q(p.Ping) + ` -n 2 127.0.0.1 >nul 2>nul)`,
		`  )`,
		`  if not defined OBR_GONE set "OBR_ABORT=1"`,
		`)`,
		`if defined OBR_ABORT if defined OBR_MADE (`,
		`  rd /s /q ` + q(p.Stage) + ` >nul 2>nul`,
		`  rd ` + q(p.StageBase) + ` >nul 2>nul`,
		`)`,
		`if not defined OBR_ABORT (`,
		`  start "" /b /wait ` + q(p.Msiexec) + ` /i ` + q(stageMSI) + ` /passive /norestart /l*v ` + q(p.Stage+`\`+updateLogFile),
		`  if errorlevel 1 (call set "OBR_RC=%%ERRORLEVEL%%") else (set "OBR_OK=1")`,
		`  del /f /q ` + q(stageMSI) + ` ` + q(p.Stage+`\pid.txt`) + ` ` + q(p.Stage+`\`+updateCopiedMarker) + ` >nul 2>nul`,
		`  if defined OBR_OK (rd /s /q ` + q(p.Stage) + ` >nul 2>nul) else (>` + q(p.Stage+`\`+updateResultFile) + ` set OBR_RC)`,
		`  rd ` + q(p.StageBase) + ` >nul 2>nul`,
		`  ` + q(p.Explorer) + ` ` + q(p.Exe),
		`)`,
		// Never back to the file: see above.
		"exit /b 0",
		")",
		"",
	}
	return strings.Join(lines, "\r\n"), nil
}

// updateCmdArgs: cmd.exe's command line for the script. /d: no AutoRun
// commands (HKCU\...\Command Processor, the user's); /e:on and /v:off
// whatever the user's registry says (the script relies on both); /s /c with
// the whole command in quotes: cmd strips the outer pair only.
func updateCmdArgs(script string) string {
	return `/d /e:on /v:off /s /c ""` + script + `""`
}

// ── The outcome, told by the next run ─────────────────────────────────────

// pendingUpdate is saved (config.json) right before the elevation: the next
// run of the app (the relaunched one, or a later one) tells how it went.
type pendingUpdate struct {
	Version string    `json:"version"` // what was being installed
	From    string    `json:"from"`    // the version that started it
	Dir     string    `json:"dir"`     // the private download folder (removed then)
	Stage   string    `json:"stage"`   // the admin-only staging folder (its log)
	At      time.Time `json:"at"`
}

// updateResult: what the page shows after an update ran.
type updateResult struct {
	OK      bool     `json:"ok"`
	Version string   `json:"version"`
	Code    int      `json:"code,omitempty"` // msiexec's exit code, when known
	Reboot  bool     `json:"reboot,omitempty"`
	Log     string   `json:"log,omitempty"`
	Message string   `json:"message"`
	Text    *i18nMsg `json:"text,omitempty"` // Message by its key (the page's language)
}

// say sets the result's message (the app's language) and its key.
func (r *updateResult) say(key string, kv ...interface{}) {
	r.Message, r.Text = tr(key, kv...), i18nM(key, kv...)
}

// parseUpdateRC reads result.txt ("OBR_RC=1603").
func parseUpdateRC(s string) (int, bool) {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] // "set OBR_RC" lists OBR_RC first
	}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "OBR_RC=") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s, "OBR_RC=")))
	if err != nil {
		return 0, false
	}
	return n, true
}

// updateOutcome: the result of a pending update, seen from the version now
// running and the script's result file (rcText; "" when there is none).
func updateOutcome(pu pendingUpdate, running, rcText, logPath string) updateResult {
	r := updateResult{Version: pu.Version}
	if c, ok := compareVersions(running, pu.Version); ok && c >= 0 {
		r.OK = true
		r.say("update.result.ok", "version", pu.Version)
		return r
	}
	rc, known := parseUpdateRC(rcText)
	switch {
	case known && (rc == 3010 || rc == 1641):
		r.Code, r.Reboot = rc, true
		r.say("update.result.reboot", "version", pu.Version)
	case known && rc == 1602:
		r.Code, r.Log = rc, logPath
		r.say("update.result.cancelled", "version", pu.Version)
	case known:
		r.Code, r.Log = rc, logPath
		r.say("update.result.failed", "version", pu.Version, "code", rc)
	default:
		r.say("update.result.incomplete", "version", pu.Version)
	}
	return r
}

// ── Start with Windows (HKCU Run key) ──────────────────────────────────────

const (
	runKeyPath        = `Software\Microsoft\Windows\CurrentVersion\Run`
	runApprovedPath   = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
	runValueName      = "Oblireach"
	startMinimizedArg = "--minimized"
)

// runKeyCommand: the Run value, the quoted path of the exe, plus
// --minimized when the app is to start minimized.
func runKeyCommand(exe string, minimized bool) (string, error) {
	if exe == "" || strings.ContainsAny(exe, "\"\r\n\x00") {
		return "", errors.New("unsupported executable path")
	}
	cmd := `"` + exe + `"`
	if minimized {
		cmd += " " + startMinimizedArg
	}
	return cmd, nil
}

// parseRunKeyCommand reads a Run value back: the program (quoted or not)
// and whether it carries --minimized.
func parseRunKeyCommand(v string) (exe string, minimized bool) {
	v = strings.TrimSpace(v)
	var rest string
	if strings.HasPrefix(v, `"`) {
		end := strings.Index(v[1:], `"`)
		if end < 0 {
			return strings.TrimPrefix(v, `"`), false
		}
		exe, rest = v[1:1+end], v[2+end:]
	} else if i := strings.IndexByte(v, ' '); i >= 0 {
		exe, rest = v[:i], v[i:]
	} else {
		exe = v
	}
	for _, a := range strings.Fields(rest) {
		if strings.EqualFold(a, startMinimizedArg) {
			minimized = true
		}
	}
	return exe, minimized
}

// startupApprovedEnabled: Task Manager's "Startup apps" state of a Run
// entry (StartupApproved\Run, REG_BINARY). No value: enabled. Its first byte
// is even when enabled (02, 06), odd when the user disabled it (03, 07).
func startupApprovedEnabled(data []byte, present bool) bool {
	if !present || len(data) == 0 {
		return true
	}
	return data[0]&1 == 0
}

// hasArg: the command line carries the flag (exact, case-insensitive).
func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if strings.EqualFold(a, flag) {
			return true
		}
	}
	return false
}

// ── config.json (Proxy.mu guards cfg) ──────────────────────────────────────

// setPendingUpdate records the update being installed (nil: none).
func (p *Proxy) setPendingUpdate(pu *pendingUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.PendingUpdate = pu
	saveConfig(p.cfg)
}

// startupPrefs: "Start minimized" and "every launch" (Settings > Application).
func (p *Proxy) startupPrefs() (minimized, always bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.StartMinimized, p.cfg.StartMinimizedAlways
}

func (p *Proxy) setStartupPrefs(minimized, always bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.StartMinimized, p.cfg.StartMinimizedAlways = minimized, always
	saveConfig(p.cfg)
}
