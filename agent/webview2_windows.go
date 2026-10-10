//go:build windows

package main

// Windows side of the WebView2 runtime support (webview2_guard.go):
// detection (the EdgeUpdate registrations the WebView2 loader itself reads,
// checked against the files, plus diagnostics), the Microsoft signature check
// of the shipped Evergreen Bootstrapper, its launch as this SYSTEM service
// (per-machine install) and the liveness check of an adopted attempt. Pure
// syscall.
//
// The service never loads WebView2Loader: go-webview2 loads it BY NAME
// (standard DLL search order, PATH included) before its in-memory copy, and a
// DLL planted in a user-writable PATH directory would then run as SYSTEM in
// this always-running process. The registry + files check below answers the
// same question (GetAvailableCoreWebView2BrowserVersionString reads these
// EdgeUpdate client keys) without loading anything.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

const (
	// webview2SignerPin: the bootstrapper must carry a valid Authenticode
	// signature chaining to a trusted root whose signer display name is this
	// (Microsoft's signature is never replaced: the signing step never touches the file).
	webview2SignerPin = "Microsoft Corporation"
	// The bootstrapper is Microsoft Edge Update's metainstaller: its signed
	// version resource names it (a different Microsoft-signed program placed
	// under that name is refused).
	webview2OriginalFilename = "MicrosoftEdgeUpdateSetup.exe"
	webview2ProductName      = "Microsoft Edge Update"
)

func platformWebView2Supported() bool { return true }

// ── Detection ────────────────────────────────────────────────────────────────

func platformWebView2Detect() webview2Detection {
	d := webview2Detection{At: time.Now()}
	switch {
	case wv2Installed(syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\`+webview2ClientGUID, &d.Version),
		wv2Installed(syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\`+webview2ClientGUID, &d.Version):
		d.Scope = "machine"
	case wv2Installed(syscall.HKEY_USERS, `S-1-5-18\Software\Microsoft\EdgeUpdate\Clients\`+webview2ClientGUID, new(string)):
		// Per-user install of the SYSTEM account only: the chat window runs
		// as the signed-in user (chat_spawn_windows.go), who cannot use it.
		// Not installed; a per-machine install fixes it.
		d.Scope = "other"
	case wv2MachinePV() != "":
		// Registered per machine but its files are gone (damaged install):
		// a reinstall is the fix.
		d.Scope = "machine"
	case wv2AnyUserPV():
		// Installed for a user only: only that user's chat window could use
		// it (webview2ChatCheck lets the chat try). A per-machine install
		// serves every user.
		d.Scope = "user_only"
	default:
		d.Scope = "none"
	}
	if d.Version == "" {
		d.PolicyBlocked = wv2PolicyBlocked()
		d.Unsupported = wv2OSTooOld()
	}
	return d
}

func wv2ValidPV(pv string) bool {
	pv = strings.TrimSpace(pv)
	return pv != "" && pv != "0.0.0.0"
}

// wv2Installed: the EdgeUpdate client key at path registers a runtime (pv)
// whose browser process exists (<location>\<pv>\msedgewebview2.exe). Sets
// *version when so.
func wv2Installed(root syscall.Handle, path string, version *string) bool {
	pv, ok := regReadString(root, path, "pv")
	pv = strings.TrimSpace(pv)
	if !ok || !wv2ValidPV(pv) || strings.ContainsAny(pv, `\/:`) || strings.Contains(pv, "..") {
		return false
	}
	loc, _ := regReadString(root, path, "location")
	loc = strings.TrimSpace(loc)
	if loc == "" || !filepath.IsAbs(loc) {
		pf := os.Getenv("ProgramFiles(x86)")
		if pf == "" {
			pf = `C:\Program Files (x86)`
		}
		loc = filepath.Join(pf, `Microsoft\EdgeWebView\Application`)
	}
	info, err := os.Stat(filepath.Join(loc, pv, "msedgewebview2.exe"))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	*version = pv
	return true
}

// wv2MachinePV: the per-machine registration (EdgeUpdate client key, value pv).
func wv2MachinePV() string {
	for _, p := range []string{
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webview2ClientGUID,
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webview2ClientGUID,
	} {
		if pv, ok := regReadString(syscall.HKEY_LOCAL_MACHINE, p, "pv"); ok && wv2ValidPV(pv) {
			return pv
		}
	}
	return ""
}

// wv2AnyUserPV: a per-user registration in a loaded user hive (HKU\<SID>).
func wv2AnyUserPV() bool {
	for _, sid := range regSubKeys(syscall.HKEY_USERS, "") {
		if !strings.HasPrefix(sid, "S-1-5-21-") || strings.HasSuffix(strings.ToLower(sid), "_classes") {
			continue
		}
		if pv, ok := regReadString(syscall.HKEY_USERS, sid+`\Software\Microsoft\EdgeUpdate\Clients\`+webview2ClientGUID, "pv"); ok && wv2ValidPV(pv) {
			return true
		}
	}
	return false
}

// Microsoft Edge preview channels the WebView2 loader also accepts when the
// WebView2 Runtime is absent (its default search order: Runtime, Beta, Dev,
// Canary).
var wv2PreviewChannels = []struct{ name, guid string }{
	{"Beta", "{2CD8A007-E189-409D-A2C8-9AF4EF3C72AA}"},
	{"Dev", "{0D50BFEC-CD6A-4F9A-964C-C7416E3ACB10}"},
	{"Canary", "{65C35B14-6C1D-4122-AC46-7148CC9D6497}"},
}

// wv2BrowserFolderPolicyKey: WebView2 "BrowserExecutableFolder" policy (a
// Fixed Version runtime chosen by the administrator), values named after the
// application's executable or "*".
const wv2BrowserFolderPolicyKey = `SOFTWARE\Policies\Microsoft\Edge\WebView2\BrowserExecutableFolder`

// platformWebView2ChatAlternative: the WebView2 Runtime is not installed per
// machine, but the chat window's WebView2 loader — running as the signed-in
// user — may still find a browser it accepts: the BrowserExecutableFolder
// policy (machine or a signed-in user's), a per-machine Microsoft Edge Beta /
// Dev / Canary, or a per-user installation of a signed-in user. Returns what
// was found ("" = nothing): the chat window is then tried, and its own
// handshake reports honestly if WebView2 cannot start.
func platformWebView2ChatAlternative() string {
	exe := "oblireach-agent.exe"
	if p, err := os.Executable(); err == nil {
		exe = filepath.Base(p)
	}
	userHives := []string{}
	for _, sid := range regSubKeys(syscall.HKEY_USERS, "") {
		if strings.HasPrefix(sid, "S-1-5-21-") && !strings.HasSuffix(strings.ToLower(sid), "_classes") {
			userHives = append(userHives, sid)
		}
	}
	for _, name := range []string{exe, "*"} {
		if v, ok := regReadString(syscall.HKEY_LOCAL_MACHINE, wv2BrowserFolderPolicyKey, name); ok && strings.TrimSpace(v) != "" {
			return "the WebView2 BrowserExecutableFolder policy"
		}
		for _, sid := range userHives {
			if v, ok := regReadString(syscall.HKEY_USERS, sid+`\`+wv2BrowserFolderPolicyKey, name); ok && strings.TrimSpace(v) != "" {
				return "the WebView2 BrowserExecutableFolder policy of a signed-in user"
			}
		}
	}
	for _, ch := range wv2PreviewChannels {
		for _, p := range []string{
			`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + ch.guid,
			`SOFTWARE\Microsoft\EdgeUpdate\Clients\` + ch.guid,
		} {
			if pv, ok := regReadString(syscall.HKEY_LOCAL_MACHINE, p, "pv"); ok && wv2ValidPV(pv) {
				return "Microsoft Edge " + ch.name + " " + strings.TrimSpace(pv)
			}
		}
	}
	for _, sid := range userHives {
		for _, guid := range []string{webview2ClientGUID, wv2PreviewChannels[0].guid, wv2PreviewChannels[1].guid, wv2PreviewChannels[2].guid} {
			if pv, ok := regReadString(syscall.HKEY_USERS, sid+`\Software\Microsoft\EdgeUpdate\Clients\`+guid, "pv"); ok && wv2ValidPV(pv) {
				return "a per-user installation of a signed-in user"
			}
		}
	}
	return ""
}

// wv2PolicyBlocked: Microsoft Edge Update policies. Install{WebView2 GUID}
// decides when set (0 = installs prevented); otherwise InstallDefault = 0
// prevents installs of every Edge Update application.
func wv2PolicyBlocked() bool {
	const key = `SOFTWARE\Policies\Microsoft\EdgeUpdate`
	if v, ok := regReadDWORD(syscall.HKEY_LOCAL_MACHINE, key, "Install"+webview2ClientGUID); ok {
		return v == 0
	}
	if v, ok := regReadDWORD(syscall.HKEY_LOCAL_MACHINE, key, "InstallDefault"); ok {
		return v == 0
	}
	return false
}

// wv2OSTooOld: WebView2 needs Windows 10 / Server 2016 or later (the
// Runtime stopped at 109 on Windows 7 / 8.1 / 2008 R2 - 2012 R2): the
// install pipeline then reports "unsupported" and never runs the
// bootstrapper (webview2Guard.begin), and the chat uses its native window.
func wv2OSTooOld() bool { return engine.WindowsVersion().WebView2Unsupported() }

// ── Registry (syscall only) ──────────────────────────────────────────────────

const wv2KeyWow6464 = 0x0100 // KEY_WOW64_64KEY

func regOpen(root syscall.Handle, path string) (syscall.Handle, bool) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var h syscall.Handle
	if syscall.RegOpenKeyEx(root, p16, 0, syscall.KEY_READ|wv2KeyWow6464, &h) != nil {
		return 0, false
	}
	return h, true
}

func regQuery(h syscall.Handle, name string) (uint32, []byte, bool) {
	n16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, nil, false
	}
	var typ, size uint32
	if syscall.RegQueryValueEx(h, n16, nil, &typ, nil, &size) != nil || size == 0 || size > 64<<10 {
		return 0, nil, false
	}
	buf := make([]byte, size)
	if syscall.RegQueryValueEx(h, n16, nil, &typ, &buf[0], &size) != nil {
		return 0, nil, false
	}
	return typ, buf[:size], true
}

func regReadString(root syscall.Handle, path, name string) (string, bool) {
	h, ok := regOpen(root, path)
	if !ok {
		return "", false
	}
	defer syscall.RegCloseKey(h)
	typ, buf, ok := regQuery(h, name)
	if !ok || (typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ) || len(buf) < 2 {
		return "", false
	}
	u := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[0])), len(buf)/2)
	return syscall.UTF16ToString(u), true
}

func regReadDWORD(root syscall.Handle, path, name string) (uint32, bool) {
	h, ok := regOpen(root, path)
	if !ok {
		return 0, false
	}
	defer syscall.RegCloseKey(h)
	typ, buf, ok := regQuery(h, name)
	if !ok || typ != syscall.REG_DWORD || len(buf) < 4 {
		return 0, false
	}
	return uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24, true
}

func regSubKeys(root syscall.Handle, path string) []string {
	var h syscall.Handle
	if path == "" {
		h = root
	} else {
		var ok bool
		if h, ok = regOpen(root, path); !ok {
			return nil
		}
		defer syscall.RegCloseKey(h)
	}
	var out []string
	for i := uint32(0); i < 4096; i++ {
		buf := make([]uint16, 256)
		n := uint32(len(buf))
		if syscall.RegEnumKeyEx(h, i, &buf[0], &n, nil, nil, nil, nil) != nil {
			break
		}
		out = append(out, syscall.UTF16ToString(buf[:n]))
	}
	return out
}

// ── Bootstrapper ─────────────────────────────────────────────────────────────

func platformWebView2BootstrapperPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), webview2BootstrapperName)
}

// wv2FileLock: an open handle that denies writing, renaming and deleting
// the file while it is verified and launched (a swap between the check and
// CreateProcess is impossible).
type wv2FileLock struct {
	once sync.Once
	h    syscall.Handle
}

func (l *wv2FileLock) Close() error {
	l.once.Do(func() { syscall.CloseHandle(l.h) })
	return nil
}

func lockFileForExec(path string) (*wv2FileLock, error) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p16, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return &wv2FileLock{h: h}, nil
}

// verifyWebView2Bootstrapper locks the file, then checks its Authenticode
// signature (trusted chain, signer "Microsoft Corporation" — no fallback on
// an untrusted chain) and its version resource. On success the caller owns
// the returned lock (closed by the launch).
func verifyWebView2Bootstrapper(path string) (io.Closer, string, error) {
	lock, err := lockFileForExec(path)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %v", path, err)
	}
	signer, err := verifySignerAgainst(path, nil, webview2SignerPin)
	if err != nil {
		lock.Close()
		return nil, signer, fmt.Errorf("the bootstrapper is not signed by %s: %v", webview2SignerPin, err)
	}
	orig := peVersionString(path, "OriginalFilename")
	product := peVersionString(path, "ProductName")
	if !strings.EqualFold(orig, webview2OriginalFilename) && product != webview2ProductName {
		lock.Close()
		return nil, signer, fmt.Errorf("unexpected program (OriginalFilename %q, ProductName %q): not the Microsoft Edge Update installer", orig, product)
	}
	return lock, signer, nil
}

var (
	wv2VersionOnce sync.Once
	wv2GetSize     *syscall.LazyProc
	wv2GetInfo     *syscall.LazyProc
	wv2VerQuery    *syscall.LazyProc
	wv2Kernel32    = syscall.NewLazyDLL("kernel32.dll")
	wv2QueryImage  = wv2Kernel32.NewProc("QueryFullProcessImageNameW")
)

// peVersionString reads a string of a PE file's version resource ("" when absent).
func peVersionString(path, field string) string {
	wv2VersionOnce.Do(func() {
		v := systemDLL("version.dll")
		wv2GetSize = v.NewProc("GetFileVersionInfoSizeW")
		wv2GetInfo = v.NewProc("GetFileVersionInfoW")
		wv2VerQuery = v.NewProc("VerQueryValueW")
	})
	if wv2GetSize.Find() != nil || wv2GetInfo.Find() != nil || wv2VerQuery.Find() != nil {
		return ""
	}
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	size, _, _ := wv2GetSize.Call(uintptr(unsafe.Pointer(p16)), 0)
	if size == 0 || size > 4<<20 {
		return ""
	}
	buf := make([]byte, size)
	if r, _, _ := wv2GetInfo.Call(uintptr(unsafe.Pointer(p16)), 0, size, uintptr(unsafe.Pointer(&buf[0]))); r == 0 {
		return ""
	}
	query := func(sub string) (uintptr, uint32) {
		s16, err := syscall.UTF16PtrFromString(sub)
		if err != nil {
			return 0, 0
		}
		var ptr uintptr
		var n uint32
		if r, _, _ := wv2VerQuery.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(s16)),
			uintptr(unsafe.Pointer(&ptr)), uintptr(unsafe.Pointer(&n))); r == 0 || ptr == 0 {
			return 0, 0
		}
		return ptr, n
	}
	base := uintptr(unsafe.Pointer(&buf[0]))
	inBuf := func(ptr uintptr, bytes uintptr) bool {
		return ptr >= base && ptr+bytes <= base+uintptr(len(buf))
	}
	var langs []string
	if ptr, n := query(`\VarFileInfo\Translation`); ptr != 0 && inBuf(ptr, uintptr(n)) {
		for i := uint32(0); i+4 <= n; i += 4 {
			off := ptr - base + uintptr(i)
			lang := uint16(buf[off]) | uint16(buf[off+1])<<8
			cp := uint16(buf[off+2]) | uint16(buf[off+3])<<8
			langs = append(langs, fmt.Sprintf("%04x%04x", lang, cp))
		}
	}
	langs = append(langs, "040904b0", "040904e4", "000004b0")
	out := ""
	for _, l := range langs {
		ptr, n := query(`\StringFileInfo\` + l + `\` + field)
		if ptr == 0 || n == 0 || !inBuf(ptr, uintptr(n)*2) {
			continue
		}
		off := ptr - base
		u := make([]uint16, n)
		for i := range u {
			u[i] = uint16(buf[off+uintptr(2*i)]) | uint16(buf[off+uintptr(2*i)+1])<<8
		}
		out = strings.TrimSpace(syscall.UTF16ToString(u))
		if out != "" {
			break
		}
	}
	runtime.KeepAlive(buf)
	return out
}

// launchWebView2Bootstrapper runs "<bootstrapper> /silent /install" as this
// service (SYSTEM: per-machine install). The lock is released once the
// process exists (its image is mapped). The installer is NOT tied to this
// process: a service stop (MSI upgrade) leaves it running, and the next
// start adopts it (webview2OnStart). wait(timeout) returns its exit code
// (an HRESULT for EdgeUpdate installers), or timedOut after stopping it.
func launchWebView2Bootstrapper(path string, lock io.Closer) (int, func(time.Duration) (int64, bool, error), error) {
	cmd := exec.Command(path, "/silent", "/install")
	cmd.Dir = filepath.Dir(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	err := cmd.Start()
	if lock != nil {
		lock.Close()
	}
	if err != nil {
		return 0, nil, err
	}
	done := make(chan struct{})
	var werr error
	go func() {
		werr = cmd.Wait()
		close(done)
	}()
	wait := func(timeout time.Duration) (int64, bool, error) {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-done:
			if werr == nil {
				return 0, false, nil
			}
			var ee *exec.ExitError
			if errors.As(werr, &ee) {
				return int64(uint32(ee.ExitCode())), false, nil
			}
			return -1, false, werr
		case <-t.C:
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
			return -1, true, nil
		}
	}
	return cmd.Process.Pid, wait, nil
}

// webview2ToastText: the toast shown to the session's user when a technician
// asks for a chat that cannot open (fixed texts: nothing chosen by an
// operator goes on the helper's command line).
func webview2ToastText(fr, installing bool) (title, msg string) {
	if fr {
		title = "Obliance — Demande de discussion"
		msg = "Un technicien souhaite discuter avec vous, mais la fenêtre de discussion ne peut pas s'ouvrir sur cet ordinateur : un composant Microsoft (Edge WebView2 Runtime) est absent."
		if installing {
			msg += " Son installation est en cours."
		}
		msg += " Le technicien en a été informé."
		return title, msg
	}
	title = "Obliance — Chat request"
	msg = "A technician would like to chat with you, but the chat window cannot open on this computer: a Microsoft component (Edge WebView2 Runtime) is missing."
	if installing {
		msg += " It is being installed."
	}
	msg += " The technician has been informed."
	return title, msg
}

// platformNotifyChatUnavailable shows that toast in the chat's session.
func platformNotifyChatUnavailable(sessionID int, installing bool) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	title, msg := webview2ToastText(chatIsFrench(), installing)
	cmdLine := fmt.Sprintf(`"%s" --notify-title "%s" --notify-msg "%s" --notify-timeout 12`, exe, title, msg)
	if _, rc := spawnInSessionGo(sessionID, cmdLine); rc != 0 {
		log.Printf("webview2: chat-unavailable toast in session %d failed (code %d)", sessionID, rc)
	}
}

// webview2InstallerAlive: the process pid still runs AND is the bootstrapper
// (a recycled PID of another program is not waited for).
func webview2InstallerAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if syscall.GetExitCodeProcess(h, &code) != nil || code != 259 /* STILL_ACTIVE */ {
		return false
	}
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := wv2QueryImage.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return false
	}
	return strings.EqualFold(filepath.Base(syscall.UTF16ToString(buf[:n])), webview2BootstrapperName)
}
