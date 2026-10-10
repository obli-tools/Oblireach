//go:build windows

package main

// The Win32 side of the one-click update (updater.go says how it works):
// download, Authenticode and MSI checks, the private folder, the elevation,
// and the bindings of the main window (reachInstallUpdate, reachUpdateState).

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	modWintrust = syscall.NewLazyDLL("wintrust.dll")
	modMsi      = syscall.NewLazyDLL("msi.dll")
	modAdvapi32 = syscall.NewLazyDLL("advapi32.dll")

	procWinVerifyTrust                 = modWintrust.NewProc("WinVerifyTrust")
	procWTHelperProvDataFromStateData  = modWintrust.NewProc("WTHelperProvDataFromStateData")
	procWTHelperGetProvSignerFromChain = modWintrust.NewProc("WTHelperGetProvSignerFromChain")

	procMsiOpenDatabaseW     = modMsi.NewProc("MsiOpenDatabaseW")
	procMsiDatabaseOpenViewW = modMsi.NewProc("MsiDatabaseOpenViewW")
	procMsiViewExecute       = modMsi.NewProc("MsiViewExecute")
	procMsiViewFetch         = modMsi.NewProc("MsiViewFetch")
	procMsiRecordGetStringW  = modMsi.NewProc("MsiRecordGetStringW")
	procMsiCloseHandle       = modMsi.NewProc("MsiCloseHandle")
	procGetSystemDirectoryW  = modKernel32.NewProc("GetSystemDirectoryW")
	procGetSystemWindowsDirW = modKernel32.NewProc("GetSystemWindowsDirectoryW")
	procGetShortPathNameW    = modKernel32.NewProc("GetShortPathNameW")
	procLocalFree            = modKernel32.NewProc("LocalFree")
	procShellExecuteExW      = modShell32.NewProc("ShellExecuteExW")
	procSHGetKnownFolderPath = modShell32.NewProc("SHGetKnownFolderPath")
	procCoInitializeEx       = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize       = modOle32.NewProc("CoUninitialize")
	procConvertStringSDToSD  = modAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
)

const (
	wtdUIChoiceNone       = 2
	wtdRevokeNone         = 0
	wtdChoiceFile         = 1
	wtdStateActionVerify  = 1
	wtdStateActionClose   = 2
	wtdRevocationChkNone  = 0x00000010
	wtdDisableMD2MD4      = 0x00002000
	msiDBOpenReadOnly     = 0
	errorNoMoreItems      = 259
	errorMoreData         = 234
	errorCancelled        = 1223
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	swHide                = 0
	coinitApartment       = 0x2
	coinitDisableOle1DDE  = 0x4
	fileShareRead         = 0x00000001
)

// WINTRUST_ACTION_GENERIC_VERIFY_V2
var wintrustActionGenericVerifyV2 = syscall.GUID{Data1: 0x00AAC56B, Data2: 0xCD44, Data3: 0x11D0,
	Data4: [8]byte{0x8C, 0xC2, 0x00, 0xC0, 0x4F, 0xC2, 0x95, 0xEE}}

// FOLDERID_ProgramFiles
var folderIDProgramFiles = syscall.GUID{Data1: 0x905E63B6, Data2: 0xC1BF, Data3: 0x494E,
	Data4: [8]byte{0xB2, 0x9C, 0x65, 0xB7, 0x32, 0xD3, 0xD2, 0x1A}}

// WINTRUST_FILE_INFO
type wintrustFileInfo struct {
	cbStruct       uint32
	pcwszFilePath  *uint16
	hFile          syscall.Handle
	pgKnownSubject *syscall.GUID
}

// WINTRUST_DATA
type wintrustData struct {
	cbStruct            uint32
	pPolicyCallbackData uintptr
	pSIPClientData      uintptr
	dwUIChoice          uint32
	fdwRevocationChecks uint32
	dwUnionChoice       uint32
	pFile               *wintrustFileInfo
	dwStateAction       uint32
	hWVTStateData       syscall.Handle
	pwszURLReference    *uint16
	dwProvFlags         uint32
	dwUIContext         uint32
	pSignatureSettings  uintptr
}

// The leading fields of CRYPT_PROVIDER_SGNR, CRYPT_PROVIDER_CERT and
// CERT_CONTEXT (the rest is not read).
type cryptProviderSgnr struct {
	cbStruct      uint32
	sftVerifyAsOf syscall.Filetime
	csCertChain   uint32
	pasCertChain  *cryptProviderCert
}

type cryptProviderCert struct {
	cbStruct uint32
	pCert    *certContext
}

type certContext struct {
	dwCertEncodingType uint32
	pbCertEncoded      *byte
	cbCertEncoded      uint32
	pCertInfo          uintptr
	hCertStore         uintptr
}

// SHELLEXECUTEINFOW
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     syscall.Handle
}

// ── Authenticode ────────────────────────────────────────────────────────────

// signerInfo: the leaf certificate of a verified Authenticode signature.
type signerInfo struct {
	subject    []byte // raw DER Subject
	name       string // its common name (messages)
	thumbprint [20]byte
}

// trustError: WinVerifyTrust refused the file.
type trustError struct{ code uint32 }

func (e trustError) Error() string { return e.i18nMsg().text(i18nCurrent()) }

// i18nMsg: why, as a fragment of the banner's text ("The installer's
// signature is not valid (...)"), said again by the page in a new language.
func (e trustError) i18nMsg() *i18nMsg {
	switch e.code {
	case 0x800B0100:
		return i18nM("update.trust.notSigned")
	case 0x80096010:
		return i18nM("update.trust.modified")
	case 0x800B0109:
		return i18nM("update.trust.untrustedRoot")
	case 0x800B010A:
		return i18nM("update.trust.incompleteChain")
	case 0x800B0101:
		return i18nM("update.trust.expired")
	case 0x800B0111:
		return i18nM("update.trust.distrusted")
	case 0x800B0004:
		return i18nM("update.trust.notTrusted")
	}
	return i18nM("update.trust.other", "code", fmt.Sprintf("0x%08X", e.code))
}

// verifyAuthenticode runs WinVerifyTrust (generic Authenticode policy, valid
// chain required, no revocation lookups: works offline and behind proxies)
// on a file, through its open handle when one is given (the very content
// that is kept locked), and returns the leaf certificate WinVerifyTrust
// validated.
func verifyAuthenticode(path string, file syscall.Handle) (signerInfo, error) {
	var si signerInfo
	if err := procWinVerifyTrust.Find(); err != nil {
		return si, err
	}
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return si, err
	}
	fi := &wintrustFileInfo{pcwszFilePath: p16, hFile: file}
	fi.cbStruct = uint32(unsafe.Sizeof(*fi))
	wd := &wintrustData{
		dwUIChoice:          wtdUIChoiceNone,
		fdwRevocationChecks: wtdRevokeNone,
		dwUnionChoice:       wtdChoiceFile,
		pFile:               fi,
		dwStateAction:       wtdStateActionVerify,
		dwProvFlags:         wtdRevocationChkNone | wtdDisableMD2MD4,
	}
	wd.cbStruct = uint32(unsafe.Sizeof(*wd))
	noUI := ^uintptr(0) // INVALID_HANDLE_VALUE: never any UI
	r, _, _ := procWinVerifyTrust.Call(noUI, uintptr(unsafe.Pointer(&wintrustActionGenericVerifyV2)), uintptr(unsafe.Pointer(wd)))
	defer func() {
		wd.dwStateAction = wtdStateActionClose
		procWinVerifyTrust.Call(noUI, uintptr(unsafe.Pointer(&wintrustActionGenericVerifyV2)), uintptr(unsafe.Pointer(wd)))
		runtime.KeepAlive(fi)
		runtime.KeepAlive(p16)
	}()
	if code := uint32(r); code != 0 {
		return si, trustError{code}
	}
	prov, _, _ := procWTHelperProvDataFromStateData.Call(uintptr(wd.hWVTStateData))
	if prov == 0 {
		return si, errors.New("no provider data")
	}
	sp, _, _ := procWTHelperGetProvSignerFromChain.Call(prov, 0, 0, 0)
	if sp == 0 {
		return si, errors.New("no signer")
	}
	sgnr := (*cryptProviderSgnr)(globalPtr(sp))
	if sgnr.csCertChain == 0 || sgnr.pasCertChain == nil || sgnr.pasCertChain.pCert == nil {
		return si, errors.New("no signer certificate")
	}
	cc := sgnr.pasCertChain.pCert
	if cc.pbCertEncoded == nil || cc.cbCertEncoded == 0 || cc.cbCertEncoded > 1<<20 {
		return si, errors.New("empty signer certificate")
	}
	der := append([]byte(nil), unsafe.Slice(cc.pbCertEncoded, cc.cbCertEncoded)...)
	si.thumbprint = sha1.Sum(der)
	if cert, err := x509.ParseCertificate(der); err == nil {
		si.subject = cert.RawSubject
		si.name = cert.Subject.CommonName
	}
	return si, nil
}

// sameSigner: the installer is signed by the publisher of this app: the same
// Subject (a renewed certificate keeps it), or the very same certificate.
func sameSigner(a, b signerInfo) bool {
	if a.thumbprint == b.thumbprint && a.thumbprint != [20]byte{} {
		return true
	}
	return len(a.subject) > 0 && bytes.Equal(a.subject, b.subject)
}

// ownSignerOnce: the signer of the running Oblireach.exe (valid chain).
var (
	ownSignerOnce sync.Once
	ownSignerVal  signerInfo
	ownSignerErr  error
)

func ownSigner() (signerInfo, error) {
	ownSignerOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			ownSignerErr = err
			return
		}
		ownSignerVal, ownSignerErr = verifyAuthenticode(exe, 0)
	})
	return ownSignerVal, ownSignerErr
}

// ── The MSI ────────────────────────────────────────────────────────────────

// msiProperties reads a few properties of an MSI's Property table
// (read-only: nothing of the package runs).
func msiProperties(path string, names ...string) (map[string]string, error) {
	if err := procMsiOpenDatabaseW.Find(); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var db uintptr
	if r, _, _ := procMsiOpenDatabaseW.Call(uintptr(unsafe.Pointer(p16)), msiDBOpenReadOnly, uintptr(unsafe.Pointer(&db))); r != 0 {
		return nil, fmt.Errorf("not an installer package (error %d)", r)
	}
	defer procMsiCloseHandle.Call(db)
	q, _ := syscall.UTF16PtrFromString("SELECT `Property`, `Value` FROM `Property`")
	var view uintptr
	if r, _, _ := procMsiDatabaseOpenViewW.Call(db, uintptr(unsafe.Pointer(q)), uintptr(unsafe.Pointer(&view))); r != 0 {
		return nil, fmt.Errorf("installer package without properties (error %d)", r)
	}
	defer procMsiCloseHandle.Call(view)
	if r, _, _ := procMsiViewExecute.Call(view, 0); r != 0 {
		return nil, fmt.Errorf("installer package unreadable (error %d)", r)
	}
	out := map[string]string{}
	for i := 0; i < 10000; i++ {
		var rec uintptr
		r, _, _ := procMsiViewFetch.Call(view, uintptr(unsafe.Pointer(&rec)))
		if r == errorNoMoreItems {
			break
		}
		if r != 0 {
			return nil, fmt.Errorf("installer package unreadable (error %d)", r)
		}
		name := msiRecordString(rec, 1)
		if want[name] {
			out[name] = msiRecordString(rec, 2)
		}
		procMsiCloseHandle.Call(rec)
	}
	return out, nil
}

func msiRecordString(rec uintptr, field int) string {
	n := uint32(256)
	for tries := 0; tries < 3; tries++ {
		buf := make([]uint16, n+1)
		size := n + 1
		r, _, _ := procMsiRecordGetStringW.Call(rec, uintptr(field), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
		if r == 0 {
			return syscall.UTF16ToString(buf[:min(int(size), len(buf))])
		}
		if r != errorMoreData || size > 1<<16 {
			return ""
		}
		n = size
	}
	return ""
}

// ── Files and folders ──────────────────────────────────────────────────────

// lockFileRead opens a file deny-write, deny-delete: nothing can change or
// replace it while the handle is open (it is kept until the process ends).
func lockFileRead(path string) (syscall.Handle, error) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	return syscall.CreateFile(p16, syscall.GENERIC_READ, fileShareRead, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
}

// readLocked reads a locked file from its start through its handle (at
// most max bytes).
func readLocked(h syscall.Handle, max int) ([]byte, error) {
	if _, err := syscall.Seek(h, 0, io.SeekStart); err != nil {
		return nil, err
	}
	out := make([]byte, 0, max)
	buf := make([]byte, 32<<10)
	for len(out) < max {
		var n uint32
		if err := syscall.ReadFile(h, buf[:min(len(buf), max-len(out))], &n, nil); err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		out = append(out, buf[:n]...)
	}
	return out, nil
}

// currentUserSID: the SID of this process's user ("S-1-5-21-...").
func currentUserSID() (string, error) {
	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String()
}

// makePrivateDir creates a fresh folder under root that only this user, the
// administrators and SYSTEM can open (protected DACL, set at creation).
func makePrivateDir(root string) (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	sddl, _ := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	var sd uintptr
	if r, _, e := procConvertStringSDToSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return "", fmt.Errorf("security descriptor: %v", e)
	}
	defer procLocalFree.Call(sd)
	sa := syscall.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	for i := 0; i < 5; i++ {
		id, err := randomHandle()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(root, updateTempPrefix+id[:16])
		d16, _ := syscall.UTF16PtrFromString(dir)
		err = syscall.CreateDirectory(d16, &sa)
		if err == nil {
			return dir, nil
		}
		if err != syscall.ERROR_ALREADY_EXISTS {
			return "", err
		}
	}
	return "", errors.New("no free folder name")
}

// scriptSafePath: the path as the elevated script can hold it (updater.go
// scriptPathOK): as is when it is plain ASCII, else its 8.3 short form.
func scriptSafePath(p string) (string, error) {
	if scriptPathOK(p) {
		return p, nil
	}
	p16, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 32768)
	n, _, e := procGetShortPathNameW.Call(uintptr(unsafe.Pointer(p16)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		return "", fmt.Errorf("no short name for %s: %v", p, e)
	}
	s := syscall.UTF16ToString(buf[:n])
	if !scriptPathOK(s) {
		return "", fmt.Errorf("the path %s has characters the installer script cannot use", p)
	}
	return s, nil
}

func winDirCall(proc *syscall.LazyProc) (string, error) {
	buf := make([]uint16, 1024)
	n, _, e := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		return "", e
	}
	return syscall.UTF16ToString(buf[:n]), nil
}

func programFilesDir() (string, error) {
	var p *uint16
	if hr, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&folderIDProgramFiles)), 0, 0, uintptr(unsafe.Pointer(&p))); int32(hr) < 0 || p == nil {
		return "", fmt.Errorf("Program Files folder: 0x%08X", uint32(hr))
	}
	s := utf16PtrString(p, 32768)
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return s, nil
}

// updateTools: the absolute paths and facts the elevated step uses.
type updateTools struct {
	Cmd, Tasklist, Find, Ping, Msiexec, Explorer string
	SysDir                                       string
	StageBase                                    string // <ProgramFiles>\OblireachClient\Update
	Exe                                          string // the installed Oblireach
	Image                                        string // this process's image name
	PID                                          int
}

func systemUpdateTools() (updateTools, error) {
	var t updateTools
	sys, err := winDirCall(procGetSystemDirectoryW)
	if err != nil {
		return t, fmt.Errorf("system folder: %v", err)
	}
	win, err := winDirCall(procGetSystemWindowsDirW)
	if err != nil {
		return t, fmt.Errorf("Windows folder: %v", err)
	}
	pf, err := programFilesDir()
	if err != nil {
		return t, err
	}
	exe, err := os.Executable()
	if err != nil {
		return t, err
	}
	t = updateTools{
		Cmd: filepath.Join(sys, "cmd.exe"), Tasklist: filepath.Join(sys, "tasklist.exe"), Find: filepath.Join(sys, "find.exe"),
		Ping: filepath.Join(sys, "PING.EXE"), Msiexec: filepath.Join(sys, "msiexec.exe"), Explorer: filepath.Join(win, "explorer.exe"),
		SysDir:    sys,
		StageBase: filepath.Join(pf, desktopInstallDir, updateStageDir),
		Exe:       filepath.Join(pf, desktopInstallDir, desktopExeName),
		Image:     filepath.Base(exe),
		PID:       os.Getpid(),
	}
	return t, nil
}

// ── Elevation ──────────────────────────────────────────────────────────────

var errUACDeclined = errors.New("the Windows prompt was declined")

// elevatedProc: the elevated script's process (its handle).
type elevatedProc interface {
	exited() bool
	release()
}

type winProc struct{ h syscall.Handle }

func (p winProc) exited() bool {
	if p.h == 0 {
		return true
	}
	ev, err := syscall.WaitForSingleObject(p.h, 0)
	return err == nil && ev == syscall.WAIT_OBJECT_0
}

func (p winProc) release() {
	if p.h != 0 {
		syscall.CloseHandle(p.h)
	}
}

// shellElevate runs file with params through ShellExecuteEx "runas": ONE UAC
// prompt, owned by hwnd. It returns when the prompt was answered (declined:
// errUACDeclined). Runs on its own locked OS thread with COM initialised, as
// ShellExecuteEx wants; never on the UI thread (the prompt can stay open).
func shellElevate(hwnd uintptr, file, params, dir string) (elevatedProc, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr, _, _ := procCoInitializeEx.Call(0, coinitApartment|coinitDisableOle1DDE); int32(hr) >= 0 {
		defer procCoUninitialize.Call()
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	f16, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return nil, err
	}
	p16, err := syscall.UTF16PtrFromString(params)
	if err != nil {
		return nil, err
	}
	d16, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	sei := &shellExecuteInfo{
		fMask: seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		hwnd:  hwnd, lpVerb: verb, lpFile: f16, lpParameters: p16, lpDirectory: d16, nShow: swHide,
	}
	sei.cbSize = uint32(unsafe.Sizeof(*sei))
	r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(sei)))
	runtime.KeepAlive(sei)
	if r == 0 {
		if en, ok := e.(syscall.Errno); ok && en == errorCancelled {
			return nil, errUACDeclined
		}
		return nil, e
	}
	return winProc{h: sei.hProcess}, nil
}

// ── The update run ─────────────────────────────────────────────────────────

// (updateStatus, what the page's banner shows: platform.go.)

// updateErr: a failure the page explains (Code for the page, Message for
// the operator, in the app's language: i18n.go; Text: the same by its key,
// which the page says again after a language switch).
type updateErr struct {
	Code, Message string
	Text          *i18nMsg
}

func (e *updateErr) Error() string { return e.Message }

// uerr: the failure code, and the message of key (kv: its parameters, tr).
func uerr(code, key string, kv ...interface{}) *updateErr {
	return &updateErr{Code: code, Message: tr(key, kv...), Text: i18nM(key, kv...)}
}

// updaterEnv: what an update run uses (the real one: newDesktopUpdater;
// fakes in the tests).
type updaterEnv struct {
	appVersion string
	serverURL  func() string // the active profile's address
	client     *http.Client
	tempRoot   func() string
	privateDir func(root string) (string, error)
	ownSigner  func() (signerInfo, error)
	verify     func(path string, h syscall.Handle) (signerInfo, error)
	msiProps   func(path string) (map[string]string, error)
	lock       func(path string) (syscall.Handle, error)
	unlock     func(h syscall.Handle)
	safePath   func(p string) (string, error)
	tools      func() (updateTools, error)
	elevate    func(file, params, dir string) (elevatedProc, error)
	copied     func(stage string) bool // the script copied the MSI (copied.txt)
	quit       func()                  // closes the app the clean way (UI thread)
	save       func(*pendingUpdate)    // config.json
	push       func(updateStatus)      // to the page
	now        func() time.Time
	pollEvery  time.Duration
	copyWait   time.Duration
	progressIv time.Duration
}

type desktopUpdater struct {
	env updaterEnv

	mu     sync.Mutex
	status updateStatus
	seq    int64
	busy   bool
	last   *updateResult    // the previous update's outcome, told once
	held   []syscall.Handle // the MSI and the script, deny-write until the process ends
}

func newDesktopUpdater(env updaterEnv, last *updateResult) *desktopUpdater {
	return &desktopUpdater{env: env, last: last, status: updateStatus{State: "idle", Percent: -1}}
}

// set records the status and tells the page.
func (u *desktopUpdater) set(s updateStatus) {
	u.mu.Lock()
	u.seq++
	s.Seq = u.seq
	u.status = s
	u.mu.Unlock()
	if u.env.push != nil {
		u.env.push(s)
	}
}

// state: the status now, with the last update's outcome the first time.
func (u *desktopUpdater) state() updateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.status
	if u.last != nil {
		s.Result = u.last
		u.last = nil
	}
	return s
}

// start begins an update unless one runs. Returns the status right away
// (the rest comes by push).
func (u *desktopUpdater) start() updateStatus {
	u.mu.Lock()
	if u.busy {
		s := u.status
		u.mu.Unlock()
		return s
	}
	u.busy = true
	u.seq++
	u.status = updateStatus{State: "downloading", Percent: -1, Seq: u.seq}
	s := u.status
	u.mu.Unlock()
	go u.run()
	return s
}

func (u *desktopUpdater) run() { u.finish(u.install()) }

// finish: a run ended; an error is told to the page (and another run may
// start). nil: the app is closing for the installer.
func (u *desktopUpdater) finish(err error) {
	if err == nil {
		return // the app is closing; busy stays (no second run)
	}
	var ue *updateErr
	if !errors.As(err, &ue) {
		ue = &updateErr{Code: "error", Message: err.Error()}
	}
	st := updateStatus{State: "error", Code: ue.Code, Message: ue.Message, Text: ue.Text, Percent: -1}
	if ue.Code == "cancelled" {
		st.State = "cancelled"
	}
	u.mu.Lock()
	st.Version = u.status.Version
	u.busy = false
	u.mu.Unlock()
	u.set(st)
}

// install does it all; nil once the app is closing for the installer.
func (u *desktopUpdater) install() (err error) {
	e := u.env
	// No reference signer, no update: an unsigned build (a development
	// one) could not tell a genuine installer from another.
	own, oerr := e.ownSigner()
	if oerr != nil {
		var te trustError
		if errors.As(oerr, &te) && te.code == 0x800B0100 {
			return uerr("unsigned_app", "update.err.unsignedApp")
		}
		return uerr("unsigned_app", "update.err.ownSignature", "error", oerr)
	}
	dlURL, uErr := updateDownloadURL(e.serverURL())
	if uErr != nil {
		return uerr("no_server", "update.err.noServer")
	}
	dir, derr := e.privateDir(e.tempRoot())
	if derr != nil {
		return uerr("storage", "update.err.folder", "error", derr)
	}
	var held []syscall.Handle
	defer func() {
		if err != nil { // nothing runs: the folder and its files go
			for _, h := range held {
				e.unlock(h)
			}
			_ = os.RemoveAll(dir)
		}
	}()
	msi := filepath.Join(dir, updateMSIName)
	if err := u.download(dlURL, msi); err != nil {
		return err
	}

	// From here the file cannot change: deny-write, deny-delete (and the
	// folder cannot be renamed while a file in it is open).
	h, lerr := e.lock(msi)
	if lerr != nil {
		return uerr("storage", "update.err.lockInstaller", "error", lerr)
	}
	held = append(held, h)
	u.set(updateStatus{State: "verifying", Percent: 100})
	sig, verr := e.verify(msi, h)
	if verr != nil {
		return uerr("signature", "update.err.signature", "error", verr)
	}
	if !sameSigner(sig, own) {
		var who interface{} = sig.name
		if sig.name == "" {
			who = i18nM("update.err.anotherPublisher")
		}
		return uerr("signer_mismatch", "update.err.signerMismatch", "signer", who)
	}
	props, perr := e.msiProps(msi)
	if perr != nil {
		return uerr("not_desktop", "update.err.notPackage", "error", perr)
	}
	if !sameUpgradeCode(props["UpgradeCode"], desktopUpgradeCode) {
		return uerr("not_desktop", "update.err.notDesktop")
	}
	version := props["ProductVersion"]
	if _, ok := parseVersion(version); !ok {
		return uerr("not_desktop", "update.err.noVersion", "version", version)
	}
	if !versionNewer(version, e.appVersion) {
		return uerr("not_newer", "update.err.notNewer", "version", version, "current", e.appVersion)
	}
	u.mu.Lock()
	u.status.Version = version
	u.mu.Unlock()

	// The elevated step.
	tools, terr := e.tools()
	if terr != nil {
		return uerr("script", "update.err.prepare", "error", terr)
	}
	id, _ := randomHandle()
	stage := tools.StageBase + `\` + id[:16]
	sp := updateScriptParams{Tasklist: tools.Tasklist, Find: tools.Find, Ping: tools.Ping, Msiexec: tools.Msiexec,
		Explorer: tools.Explorer, PID: tools.PID, Image: tools.Image, StageBase: tools.StageBase, Stage: stage, Exe: tools.Exe}
	if sp.MSI, err = e.safePath(msi); err != nil {
		return uerr("script", "update.err.prepare", "error", err)
	}
	script, serr := updateScript(sp)
	if serr != nil {
		return uerr("script", "update.err.prepare", "error", serr)
	}
	scriptPath := filepath.Join(dir, "update.cmd")
	if werr := os.WriteFile(scriptPath, []byte(script), 0o600); werr != nil {
		return uerr("storage", "update.err.scriptWrite", "error", werr)
	}
	sh, lerr := e.lock(scriptPath)
	if lerr != nil {
		return uerr("storage", "update.err.scriptLock", "error", lerr)
	}
	held = append(held, sh)
	// What cmd will read is what was written: read back through the lock
	// (another program of this user could have changed it before).
	if got, rerr := readLocked(sh, len(script)+1); rerr != nil || string(got) != script {
		return uerr("tampered", "update.err.tampered")
	}
	scriptArg, serr := e.safePath(scriptPath)
	if serr != nil {
		return uerr("script", "update.err.prepare", "error", serr)
	}

	e.save(&pendingUpdate{Version: version, From: e.appVersion, Dir: dir, Stage: stage, At: e.now()})
	u.set(updateStatus{State: "elevating", Version: version, Percent: 100})
	proc, xerr := e.elevate(tools.Cmd, updateCmdArgs(scriptArg), tools.SysDir)
	if xerr != nil {
		e.save(nil)
		if errors.Is(xerr, errUACDeclined) {
			return uerr("cancelled", "update.err.declined")
		}
		return uerr("elevation", "update.err.elevation", "error", xerr)
	}
	defer proc.release()
	u.set(updateStatus{State: "installing", Version: version, Percent: 100})

	// The script copies the MSI first (this process still holds it), then
	// waits for this process to end: the app closes once the copy is there.
	deadline := e.now().Add(e.copyWait)
	for !e.copied(stage) {
		if proc.exited() {
			e.save(nil)
			return uerr("installer_not_started", "update.err.stoppedEarly")
		}
		if e.now().After(deadline) {
			e.save(nil)
			return uerr("installer_not_started", "update.err.notInTime")
		}
		time.Sleep(e.pollEvery)
	}
	u.mu.Lock()
	u.held = append(u.held, held...) // until the process ends
	u.mu.Unlock()
	held = nil
	e.quit()
	return nil
}

// download fetches the installer into path (created, never overwritten),
// with progress for the page. Redirects are refused: the fixed URL of the
// active server, nothing else.
func (u *desktopUpdater) download(rawURL, path string) error {
	e := u.env
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return uerr("network", "update.err.downloadStart", "error", err)
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := e.client.Do(req)
	if err != nil {
		return uerr("network", "update.err.download", "error", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return uerr("redirect", "update.err.redirect", "status", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return uerr("http_status", "update.err.httpStatus", "status", resp.StatusCode)
	}
	total := resp.ContentLength
	if total > maxUpdateMSISize {
		return uerr("too_large", "update.err.tooLarge", "size", maxUpdateMSISize>>20)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return uerr("storage", "update.err.save", "error", err)
	}
	var got int64
	last := time.Time{}
	tell := func(force bool) {
		if !force && e.now().Sub(last) < e.progressIv {
			return
		}
		last = e.now()
		pct := -1
		if total > 0 {
			pct = int(got * 100 / total)
		}
		u.set(updateStatus{State: "downloading", Received: got, Total: max(total, 0), Percent: pct})
	}
	tell(true)
	buf := make([]byte, 64<<10)
	var werr error
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if got+int64(n) > maxUpdateMSISize {
				werr = uerr("too_large", "update.err.tooLarge", "size", maxUpdateMSISize>>20)
				break
			}
			if _, err := f.Write(buf[:n]); err != nil {
				werr = uerr("storage", "update.err.save", "error", err)
				break
			}
			got += int64(n)
			tell(false)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			werr = uerr("network", "update.err.download", "error", rerr)
			break
		}
	}
	if cerr := f.Close(); werr == nil && cerr != nil {
		werr = uerr("storage", "update.err.save", "error", cerr)
	}
	if werr == nil && total > 0 && got != total {
		werr = uerr("network", "update.err.cutShort", "got", got, "total", total)
	}
	if werr == nil && got == 0 {
		werr = uerr("network", "update.err.empty")
	}
	if werr != nil {
		return werr
	}
	tell(true)
	return nil
}

// updateHTTPClient: the default transport (system proxy, TLS verified), no
// redirect followed.
func updateHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// newDesktopUpdater wiring for the main window (main.go).
func newAppUpdater(p *Proxy, b *nativeBridge, quit func(), last *updateResult) *desktopUpdater {
	env := updaterEnv{
		appVersion: appVersion,
		serverURL:  func() string { s, _ := p.activeTarget(); return s },
		client:     updateHTTPClient(),
		tempRoot:   os.TempDir,
		privateDir: makePrivateDir,
		ownSigner:  ownSigner,
		verify:     verifyAuthenticode,
		msiProps: func(path string) (map[string]string, error) {
			return msiProperties(path, "ProductVersion", "UpgradeCode", "ProductName")
		},
		lock:     lockFileRead,
		unlock:   func(h syscall.Handle) { syscall.CloseHandle(h) },
		safePath: scriptSafePath,
		tools:    systemUpdateTools,
		elevate: func(file, params, dir string) (elevatedProc, error) {
			return shellElevate(b.hwnd, file, params, dir)
		},
		copied: func(stage string) bool {
			_, err := os.Stat(filepath.Join(stage, updateCopiedMarker))
			return err == nil
		},
		quit: func() { b.w.Dispatch(quit) },
		save: p.setPendingUpdate,
		push: func(s updateStatus) {
			j, _ := json.Marshal(s)
			b.w.Dispatch(func() { b.evalMain("__reachOnUpdate", string(j)) })
		},
		now:        time.Now,
		pollEvery:  200 * time.Millisecond,
		copyWait:   updateCopyTimeout,
		progressIv: 120 * time.Millisecond,
	}
	return newDesktopUpdater(env, last)
}

// ── Bindings (main window) ─────────────────────────────────────────────────

// installUpdate (JS reachInstallUpdate): "Update now". Nothing comes from the
// page but the token: the server, the URL and the checks are Go's.
func (b *nativeBridge) installUpdate(tok string) (updateStatus, error) {
	if !b.allowed(tok) {
		return updateStatus{}, errNotAllowed
	}
	if b.updater == nil {
		return updateStatus{}, errors.New(tr("update.err.unavailableWindow"))
	}
	return b.updater.start(), nil
}

// updateState (JS reachUpdateState): the update now (a reloaded page), and
// the outcome of the last one, once.
func (b *nativeBridge) updateState(tok string) (updateStatus, error) {
	if !b.allowed(tok) {
		return updateStatus{}, errNotAllowed
	}
	if b.updater == nil {
		return updateStatus{State: "idle", Percent: -1}, nil
	}
	return b.updater.state(), nil
}

// ── After a restart ────────────────────────────────────────────────────────

// winUpdates: the start-up side of the MSI update (updateHost, platform.go).
type winUpdates struct{}

var platformUpdates updateHost = winUpdates{}

func (winUpdates) finishPending(cfg *Config, running string, now time.Time) *updateResult {
	return finishPendingUpdate(cfg, running, now)
}

func (winUpdates) sweep(keep string, now time.Time) { sweepUpdateDirs(keep, now) }

// finishPendingUpdate (start-up): how the update started by an earlier run
// went, if one was. Its download folder goes; the record is cleared, unless
// the installer seems to be still at work (then nothing is said yet).
func finishPendingUpdate(cfg *Config, running string, now time.Time) *updateResult {
	pu := cfg.PendingUpdate
	if pu == nil {
		return nil
	}
	if pu.Stage != "" && now.Sub(pu.At) < 10*time.Minute {
		if _, err := os.Stat(filepath.Join(pu.Stage, updateMSIName)); err == nil {
			if c, ok := compareVersions(running, pu.Version); !ok || c < 0 {
				return nil // copied, not installed yet: the installer runs
			}
		}
	}
	cfg.PendingUpdate = nil
	saveConfig(cfg)
	removeUpdateDir(pu.Dir)
	if now.Sub(pu.At) > 7*24*time.Hour {
		return nil
	}
	rc := ""
	if pu.Stage != "" {
		if f, err := os.Open(filepath.Join(pu.Stage, updateResultFile)); err == nil {
			data, _ := io.ReadAll(io.LimitReader(f, 4096))
			f.Close()
			rc = string(data)
		}
	}
	logPath := ""
	if pu.Stage != "" {
		logPath = filepath.Join(pu.Stage, updateLogFile)
	}
	r := updateOutcome(*pu, running, rc, logPath)
	log.Printf("update: %s", r.Message)
	return &r
}

// removeUpdateDir removes a download folder of ours: a direct child of the
// temporary folder, with our prefix (config.json could say anything).
func removeUpdateDir(dir string) {
	if dir == "" || !strings.HasPrefix(filepath.Base(dir), updateTempPrefix) ||
		!strings.EqualFold(filepath.Clean(filepath.Dir(dir)), filepath.Clean(os.TempDir())) {
		return
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	_ = os.RemoveAll(dir)
}

// sweepUpdateDirs removes the download folders of ours that a run left
// behind (closed during a download), a day old or more; keep: the one of an
// update still being installed.
func sweepUpdateDirs(keep string, now time.Time) {
	root := os.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), updateTempPrefix) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if keep != "" && strings.EqualFold(dir, keep) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) >= 24*time.Hour {
			removeUpdateDir(dir)
		}
	}
}
