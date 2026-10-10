//go:build windows

package engine

// Windows side of the file transfers (see filexfer.go). The capture helper
// runs with a SYSTEM token in the user's session; every file access is made
// AS the session user (WTSQueryUserToken + ImpersonateLoggedOnUser): an
// upload lands in the user's own Downloads folder with the user's rights (a
// standard, non-admin user owns it), and a download can only read what the
// user can read. No user signed in (logon screen) → no_user_session.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	fxUser32                     = newSystemLazyDLL("user32.dll")
	fxOpenClipboard              = fxUser32.NewProc("OpenClipboard")
	fxCloseClipboard             = fxUser32.NewProc("CloseClipboard")
	fxGetClipboardData           = fxUser32.NewProc("GetClipboardData")
	fxIsClipboardFormatAvailable = fxUser32.NewProc("IsClipboardFormatAvailable")
	fxShell32                    = newSystemLazyDLL("shell32.dll")
	fxDragQueryFileW             = fxShell32.NewProc("DragQueryFileW")
	fxSHGetKnownFolderPath       = fxShell32.NewProc("SHGetKnownFolderPath")
	fxOle32                      = newSystemLazyDLL("ole32.dll")
	fxCoTaskMemFree              = fxOle32.NewProc("CoTaskMemFree")
	fxWtsapi32                   = newSystemLazyDLL("wtsapi32.dll")
	fxWTSQueryUserToken          = fxWtsapi32.NewProc("WTSQueryUserToken")
	fxAdvapi32                   = newSystemLazyDLL("advapi32.dll")
	fxImpersonateLoggedOnUser    = fxAdvapi32.NewProc("ImpersonateLoggedOnUser")
	fxRevertToSelf               = fxAdvapi32.NewProc("RevertToSelf")
	fxKernel32                   = newSystemLazyDLL("kernel32.dll")
	fxMoveFileExW                = fxKernel32.NewProc("MoveFileExW")
	fxGetUserDefaultUILanguage   = fxKernel32.NewProc("GetUserDefaultUILanguage")
)

const (
	fxCFHDROP              = 15
	fxMoveFileWriteThrough = 0x8
	fxErrorDiskFull        = syscall.Errno(112) // ERROR_DISK_FULL
	fxErrorHandleDiskFull  = syscall.Errno(39)  // ERROR_HANDLE_DISK_FULL
)

type fxGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// FOLDERID_Downloads {374DE290-123F-4565-9164-39C4925E467B}
var fxFolderDownloads = fxGUID{0x374DE290, 0x123F, 0x4565, [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}

func isDiskFull(err error) bool {
	return errors.Is(err, fxErrorDiskFull) || errors.Is(err, fxErrorHandleDiskFull)
}

// errPrivilegeNotHeld (ERROR_PRIVILEGE_NOT_HELD): WTSQueryUserToken needs
// SeTcbPrivilege, which only LocalSystem holds.
const errPrivilegeNotHeld = syscall.Errno(1314)

// sessionUserToken is the primary token of the user signed in to this
// helper's session (the caller closes it). A process without SeTcbPrivilege
// (a host running RunSession as the signed-in user, not as the agent's
// SYSTEM helper) gets token 0: its own identity is the session user's, and
// 0 means "this process's user" to the callers.
func sessionUserToken() (syscall.Handle, error) {
	var tok syscall.Handle
	r, _, e := fxWTSQueryUserToken.Call(uintptr(uint32(CurrentSessionID())), uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		if e == errPrivilegeNotHeld {
			return 0, nil
		}
		return 0, fxErr(fxErrNoUser, e)
	}
	return tok, nil
}

// asSessionUser runs fn impersonating the session user, on a locked OS
// thread (impersonation is per thread). With token 0 (the process runs as
// the session user) fn runs as is.
func asSessionUser(fn func() error) error {
	tok, err := sessionUserToken()
	if err != nil {
		return err
	}
	if tok == 0 {
		return fn()
	}
	defer syscall.CloseHandle(tok)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, e := fxImpersonateLoggedOnUser.Call(uintptr(tok)); r == 0 {
		return fmt.Errorf("impersonate the session user: %w", e)
	}
	defer fxRevertToSelf.Call()
	return fn()
}

// sessionDownloadsDir is the session user's Downloads folder (known folder:
// follows a relocation / redirection).
func sessionDownloadsDir() (string, error) {
	tok, err := sessionUserToken()
	if err != nil {
		return "", err
	}
	if tok != 0 {
		defer syscall.CloseHandle(tok)
	}
	return downloadsDirOf(tok)
}

// downloadsDirOf: the Downloads folder of the user of `tok` (0: this process's user).
func downloadsDirOf(tok syscall.Handle) (string, error) {
	var p *uint16
	hr, _, _ := fxSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&fxFolderDownloads)), 0, uintptr(tok), uintptr(unsafe.Pointer(&p)))
	if p != nil {
		defer fxCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	}
	if hr != 0 || p == nil {
		return "", fxErr(fxErrDenied, fmt.Errorf("Downloads folder of the session user not found (hr=0x%08x)", uint32(hr)))
	}
	n := 0
	for n < 32767 && *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	dir := string(utf16.Decode(unsafe.Slice(p, n)))
	if dir == "" {
		return "", fxErr(fxErrDenied, errors.New("empty Downloads path"))
	}
	return dir, nil
}

func sessionCreateFile(path string) (*os.File, error) {
	var f *os.File
	err := asSessionUser(func() error {
		var e error
		f, e = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		return e
	})
	return f, err
}

func sessionOpenFile(path string) (*os.File, error) {
	var f *os.File
	err := asSessionUser(func() error {
		var e error
		f, e = os.Open(path)
		return e
	})
	return f, err
}

// sessionRename moves src to dst without ever replacing dst (MoveFileEx
// without MOVEFILE_REPLACE_EXISTING; os.Rename would overwrite).
func sessionRename(src, dst string) error {
	s, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	d, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return asSessionUser(func() error { return moveNoReplace(src, dst, s, d) })
}

func moveNoReplace(src, dst string, s, d *uint16) error {
	if r, _, e := fxMoveFileExW.Call(uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(d)), fxMoveFileWriteThrough); r == 0 {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: e}
	}
	return nil
}

// clipboardFileList returns the paths of the files copied on the remote
// (CF_HDROP: Explorer copy), and whether the clipboard holds files at all.
func clipboardFileList() ([]string, bool) {
	if r, _, _ := fxIsClipboardFormatAvailable.Call(fxCFHDROP); r == 0 {
		return nil, false
	}
	runtime.LockOSThread() // OpenClipboard / CloseClipboard: same thread
	defer runtime.UnlockOSThread()
	opened := false
	for i := 0; i < 10; i++ {
		if r, _, _ := fxOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !opened {
		return nil, true
	}
	defer fxCloseClipboard.Call()
	h, _, _ := fxGetClipboardData.Call(fxCFHDROP)
	if h == 0 {
		return nil, true
	}
	count, _, _ := fxDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	var paths []string
	for i := uintptr(0); i < count && len(paths) < 100; i++ {
		l, _, _ := fxDragQueryFileW.Call(h, i, 0, 0)
		if l == 0 || l > 32767 {
			continue
		}
		buf := make([]uint16, l+1)
		fxDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		paths = append(paths, syscall.UTF16ToString(buf))
	}
	return paths, true
}

// statAsSessionUser: sizes of the listed files, read with the user's rights.
func statAsSessionUser(p string) (os.FileInfo, error) {
	var fi os.FileInfo
	err := asSessionUser(func() error {
		var e error
		fi, e = os.Stat(p)
		return e
	})
	return fi, err
}

// fileReceivedHook is Options.OnFileReceived of the running session (nil: the
// agent's toast). Set by runSession for the duration of a session.
var fileReceivedHook atomic.Pointer[func(path string)]

// setFileReceivedHook installs (or, with nil, removes) the session's hook.
func setFileReceivedHook(f func(path string)) {
	if f == nil {
		fileReceivedHook.Store(nil)
		return
	}
	fileReceivedHook.Store(&f)
}

// fileReceived tells the session user a file arrived: through the host's
// hook when one is set, else with the agent's toast.
func fileReceived(name, dir string) {
	if h := fileReceivedHook.Load(); h != nil {
		(*h)(filepath.Join(dir, name))
		return
	}
	notifyFileReceived(name, dir)
}

// notifyFileReceived shows the session user a toast: a file just landed in
// their Downloads folder (they are never left wondering where it came from).
func notifyFileReceived(name, dir string) {
	lang, _, _ := fxGetUserDefaultUILanguage.Call()
	title, msg := "Oblireach — File received",
		fmt.Sprintf("Your technician sent you \"%s\" (folder %s).", name, filepath.Base(dir))
	if lang&0x3FF == 0x0C { // LANG_FRENCH
		title, msg = "Oblireach — Fichier reçu",
			fmt.Sprintf("Votre technicien vous a envoyé « %s » (dossier %s).", name, filepath.Base(dir))
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "--notify-title", title, "--notify-msg", msg, "--notify-timeout", "10")
	if err := cmd.Start(); err != nil {
		log.Printf("helper: file-received toast failed: %v", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}

// writeZoneIdentifier is the actual Mark-of-the-Web write (zone 3, Internet),
// factored out of sessionMarkMOTW so it can be unit-tested without the
// impersonation that real callers need (filexfer_windows_test.go runs it
// directly, as the test's own user).
func writeZoneIdentifier(path string) error {
	return os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
}

// sessionMarkMOTW best-effort tags path with Mark-of-the-Web, as the session
// user — matching what Windows Explorer marks on a real RDP clipboard paste.
// Failure is never surfaced to the operator: some Windows versions /
// filesystems do not support the alternate data stream at all (this is
// accepted: the mark is only a hint), so this
// is pure best-effort.
func sessionMarkMOTW(path string) {
	_ = asSessionUser(func() error { return writeZoneIdentifier(path) })
}

// newHelperFileXfer wires the transfers of a capture helper to its pipe.
func newHelperFileXfer(send func(byte, []byte) error) *fileXfer {
	return newFileXfer(fileXferEnv{
		sendJSON: func(m map[string]interface{}) {
			if b, err := json.Marshal(m); err == nil {
				_ = send(PipeTypeControl, b)
			}
		},
		sendFrame:      func(frame []byte) error { return send(PipeTypeFileDown, frame) },
		downloadsDir:   sessionDownloadsDir,
		createFile:     sessionCreateFile,
		openFile:       sessionOpenFile,
		rename:         sessionRename,
		received:       fileReceived,
		markDownloaded: sessionMarkMOTW,
	})
}
