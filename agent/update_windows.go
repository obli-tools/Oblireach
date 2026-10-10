//go:build windows

package main

// Windows side of the self-update: Windows Installer busy check, Authenticode
// check of the downloaded MSI, detached msiexec launch + exit code, process
// liveness. Pure syscall (no golang.org/x/sys bump).

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

func platformSelfUpdateSupported() bool { return true }

// updateSignerPin is the accepted publisher besides "same subject as the
// signer of this executable". Empty in source: the release build sets the
// publisher's certificate CN:
//
//	go build -ldflags "-X 'main.updateSignerPin=<certificate CN>'"
//
// Empty, only an update signed with the same subject as this executable is
// accepted.
var updateSignerPin = ""

// ── System DLLs ──────────────────────────────────────────────────────────────

var (
	suKernel32              = syscall.NewLazyDLL("kernel32.dll") // KnownDLL
	procGetSystemDirectoryW = suKernel32.NewProc("GetSystemDirectoryW")
	procOpenMutexW          = suKernel32.NewProc("OpenMutexW")

	systemDirOnce sync.Once
	systemDirPath string
)

func systemDir() string {
	systemDirOnce.Do(func() {
		buf := make([]uint16, 512)
		n, _, _ := procGetSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 && int(n) < len(buf) {
			systemDirPath = syscall.UTF16ToString(buf[:n])
			return
		}
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		systemDirPath = filepath.Join(root, "System32")
	})
	return systemDirPath
}

// systemDLL loads a DLL from System32 by full path: main() points the DLL
// search path at the install directory (codec DLLs), and wintrust / crypt32
// are not KnownDLLs.
func systemDLL(name string) *syscall.LazyDLL {
	return syscall.NewLazyDLL(filepath.Join(systemDir(), name))
}

type trustAPI struct {
	winVerifyTrustEx           *syscall.LazyProc
	cryptQueryObject           *syscall.LazyProc
	cryptMsgGetParam           *syscall.LazyProc
	cryptMsgClose              *syscall.LazyProc
	certCloseStore             *syscall.LazyProc
	certFindCertificateInStore *syscall.LazyProc
	certFreeCertificateContext *syscall.LazyProc
	certGetNameStringW         *syscall.LazyProc
}

var (
	trustOnce sync.Once
	trust     trustAPI
)

func trustProcs() *trustAPI {
	trustOnce.Do(func() {
		wt := systemDLL("wintrust.dll")
		c32 := systemDLL("crypt32.dll")
		trust = trustAPI{
			winVerifyTrustEx:           wt.NewProc("WinVerifyTrustEx"),
			cryptQueryObject:           c32.NewProc("CryptQueryObject"),
			cryptMsgGetParam:           c32.NewProc("CryptMsgGetParam"),
			cryptMsgClose:              c32.NewProc("CryptMsgClose"),
			certCloseStore:             c32.NewProc("CertCloseStore"),
			certFindCertificateInStore: c32.NewProc("CertFindCertificateInStore"),
			certFreeCertificateContext: c32.NewProc("CertFreeCertificateContext"),
			certGetNameStringW:         c32.NewProc("CertGetNameStringW"),
		}
	})
	return &trust
}

// ── Windows Installer busy ───────────────────────────────────────────────────

// windowsInstallerBusy: the Global\_MSIExecute mutex exists while an MSI
// transaction runs (ours, the Obliance agent's, Windows Update's…).
func windowsInstallerBusy() bool {
	name, err := syscall.UTF16PtrFromString(`Global\_MSIExecute`)
	if err != nil {
		return false
	}
	const synchronize = 0x00100000
	h, _, callErr := procOpenMutexW.Call(synchronize, 0, uintptr(unsafe.Pointer(name)))
	if h != 0 {
		syscall.CloseHandle(syscall.Handle(h))
		return true
	}
	var en syscall.Errno
	return errors.As(callErr, &en) && en == syscall.ERROR_ACCESS_DENIED
}

// ── Process liveness ─────────────────────────────────────────────────────────

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if syscall.GetExitCodeProcess(h, &code) != nil {
		return true
	}
	const stillActive = 259
	return code == stillActive
}

// ── msiexec ──────────────────────────────────────────────────────────────────

// launchMSIExec starts msiexec detached (the installer stops this service:
// msiexec must survive it) and returns a wait function giving its exit code.
// The wait only returns while this process is still alive, i.e. when the
// installer did NOT replace it (failure, busy…).
func launchMSIExec(msiPath, logPath string, cfg *Config) (int, func() (int, error), error) {
	cmd := exec.Command(filepath.Join(systemDir(), "msiexec.exe"),
		"/i", msiPath,
		"SERVERURL="+cfg.ServerURL,
		"APIKEY="+cfg.APIKey,
		"/quiet", "/norestart",
		"/l*v", logPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008, // DETACHED_PROCESS — survive the service stop
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	wait := func() (int, error) {
		err := cmd.Wait()
		if err == nil {
			return 0, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return cmd.Process.Pid, wait, nil
}

// ── Authenticode ─────────────────────────────────────────────────────────────
//
//  1. WinVerifyTrust (WINTRUST_ACTION_GENERIC_VERIFY_V2, no UI, no revocation
//     check).
//  2. The signer leaf certificate is read from the embedded PKCS#7.
//  3. Accepted when its raw Subject DN equals the Subject DN of the signer of
//     THIS executable, or its display name equals updateSignerPin (subject,
//     not thumbprint: a certificate renewal keeps working).
//  4. CERT_E_UNTRUSTEDROOT / CERT_E_CHAINING (a machine without the Certum
//     root): accepted ONLY when the leaf is the very same certificate
//     (thumbprint) as this executable's signer — anyone can self-sign a
//     matching subject.
//
// Same rules as the Obliance agent's check of the same MSI.

type trustGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var actionGenericVerifyV2 = trustGUID{0xaac56b, 0xcd44, 0x11d0, [8]byte{0x8c, 0xc2, 0x00, 0xc0, 0x4f, 0xc2, 0x95, 0xee}}

type wintrustFileInfo struct {
	Size         uint32
	FilePath     *uint16
	File         syscall.Handle
	KnownSubject *trustGUID
}

type wintrustData struct {
	Size               uint32
	PolicyCallbackData uintptr
	SIPClientData      uintptr
	UIChoice           uint32
	RevocationChecks   uint32
	UnionChoice        uint32
	FileInfo           *wintrustFileInfo
	StateAction        uint32
	StateData          syscall.Handle
	URLReference       *uint16
	ProvFlags          uint32
	UIContext          uint32
	SignatureSettings  uintptr
}

type dataBlob struct {
	Size uint32
	Data *byte
}

type cryptAlgorithmIdentifier struct {
	ObjID      *byte
	Parameters dataBlob
}

type cryptBitBlob struct {
	Size       uint32
	Data       *byte
	UnusedBits uint32
}

type certPublicKeyInfo struct {
	Algorithm cryptAlgorithmIdentifier
	PublicKey cryptBitBlob
}

type certInfo struct {
	Version              uint32
	SerialNumber         dataBlob
	SignatureAlgorithm   cryptAlgorithmIdentifier
	Issuer               dataBlob
	NotBefore            syscall.Filetime
	NotAfter             syscall.Filetime
	Subject              dataBlob
	SubjectPublicKeyInfo certPublicKeyInfo
	IssuerUniqueID       cryptBitBlob
	SubjectUniqueID      cryptBitBlob
	CountExtensions      uint32
	Extensions           uintptr
}

type certContext struct {
	EncodingType uint32
	EncodedCert  *byte
	Length       uint32
	CertInfo     *certInfo
	Store        syscall.Handle
}

// cmsgSignerInfoHead: leading part of CMSG_SIGNER_INFO.
type cmsgSignerInfoHead struct {
	Version      uint32
	Issuer       dataBlob
	SerialNumber dataBlob
}

const (
	wtdUINone                = 2
	wtdRevokeNone            = 0
	wtdChoiceFile            = 1
	wtdStateActionVerify     = 1
	wtdStateActionClose      = 2
	wtdRevocationCheckNone   = 0x10
	certQueryObjectFile      = 1
	certQueryContentPKCS7Emb = 1 << 10
	certQueryFormatBinary    = 1 << 1
	cmsgSignerInfoParam      = 6
	certEncodingX509PKCS7    = 0x00000001 | 0x00010000
	certFindSubjectCert      = 11 << 16
	certNameSimpleDisplay    = 4

	trustENoSignature        = 0x800B0100
	trustESubjectFormUnknown = 0x800B0003
	trustEBadDigest          = 0x80096010
	certEExpired             = 0x800B0101
	certEUntrustedRoot       = 0x800B0109
	certEChaining            = 0x800B010A
	certERevoked             = 0x800B010C
	trustEExplicitDistrust   = 0x800B0111
)

type codeSigner struct {
	subject    []byte   // raw DER Subject DN of the leaf
	name       string   // simple display name (CN)
	thumbprint [20]byte // SHA-1 of the encoded leaf certificate
}

var (
	ownSignerOnce sync.Once
	ownSigner     *codeSigner
)

// verifyUpdateSigner checks the downloaded MSI before msiexec runs it and
// returns the signer's display name.
func verifyUpdateSigner(path string) (string, error) {
	ownSignerOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		// Only a genuinely signed executable is a reference (an unsigned
		// dev build falls back to the pinned publisher).
		if terr := winVerifyTrustFile(exe); terr != nil && !isChainTrustError(terr) {
			return
		}
		if s, err := readCodeSigner(exe); err == nil {
			ownSigner = s
		}
	})
	return verifySignerAgainst(path, ownSigner, updateSignerPin)
}

func verifySignerAgainst(path string, ref *codeSigner, pin string) (string, error) {
	trustErr := winVerifyTrustFile(path)
	chainOnly := false
	if trustErr != nil {
		if !isChainTrustError(trustErr) {
			return "", fmt.Errorf("invalid Authenticode signature: %s", trustErrorText(trustErr))
		}
		chainOnly = true
	}
	leaf, err := readCodeSigner(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the signer certificate: %v", err)
	}
	if chainOnly {
		if ref != nil && leaf.thumbprint == ref.thumbprint {
			return leaf.name, nil
		}
		return leaf.name, fmt.Errorf("signer %q does not chain to a trusted root (%s)", leaf.name, trustErrorText(trustErr))
	}
	if ref != nil && len(ref.subject) > 0 && bytes.Equal(leaf.subject, ref.subject) {
		return leaf.name, nil
	}
	if pin != "" && leaf.name == pin {
		return leaf.name, nil
	}
	return leaf.name, fmt.Errorf("signed by %q, not by the publisher of this agent", leaf.name)
}

func isChainTrustError(err error) bool {
	var en syscall.Errno
	if !errors.As(err, &en) {
		return false
	}
	return uint32(en) == certEUntrustedRoot || uint32(en) == certEChaining
}

func trustErrorText(err error) string {
	var en syscall.Errno
	if !errors.As(err, &en) {
		return err.Error()
	}
	code := uint32(en)
	meaning := ""
	switch code {
	case trustENoSignature:
		meaning = "not signed"
	case trustESubjectFormUnknown:
		meaning = "file format not recognized"
	case trustEBadDigest:
		meaning = "content does not match the signature"
	case certEExpired:
		meaning = "certificate expired"
	case certEUntrustedRoot:
		meaning = "untrusted root"
	case certEChaining:
		meaning = "certificate chain could not be built"
	case certERevoked:
		meaning = "certificate revoked"
	case trustEExplicitDistrust:
		meaning = "certificate explicitly distrusted"
	}
	if meaning == "" {
		return fmt.Sprintf("0x%08X", code)
	}
	return fmt.Sprintf("0x%08X %s", code, meaning)
}

// winVerifyTrustFile runs WinVerifyTrust on a file's embedded signature.
func winVerifyTrustFile(path string) error {
	api := trustProcs()
	if err := api.winVerifyTrustEx.Find(); err != nil {
		return err
	}
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	fi := &wintrustFileInfo{Size: uint32(unsafe.Sizeof(wintrustFileInfo{})), FilePath: p16}
	data := &wintrustData{
		Size:             uint32(unsafe.Sizeof(wintrustData{})),
		UIChoice:         wtdUINone,
		RevocationChecks: wtdRevokeNone,
		UnionChoice:      wtdChoiceFile,
		FileInfo:         fi,
		StateAction:      wtdStateActionVerify,
		ProvFlags:        wtdRevocationCheckNone,
	}
	const invalidHWND = ^uintptr(0) // no UI
	r, _, _ := api.winVerifyTrustEx.Call(invalidHWND, uintptr(unsafe.Pointer(&actionGenericVerifyV2)), uintptr(unsafe.Pointer(data)))
	data.StateAction = wtdStateActionClose
	_, _, _ = api.winVerifyTrustEx.Call(invalidHWND, uintptr(unsafe.Pointer(&actionGenericVerifyV2)), uintptr(unsafe.Pointer(data)))
	runtime.KeepAlive(fi)
	runtime.KeepAlive(data)
	if int32(r) != 0 {
		return syscall.Errno(uint32(r))
	}
	return nil
}

// readCodeSigner extracts the signer leaf certificate of a file's embedded
// Authenticode signature (PE files and MSIs).
func readCodeSigner(path string) (*codeSigner, error) {
	api := trustProcs()
	if err := api.cryptQueryObject.Find(); err != nil {
		return nil, err
	}
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var encoding, contentType, formatType uint32
	var store, msg uintptr
	r, _, callErr := api.cryptQueryObject.Call(certQueryObjectFile, uintptr(unsafe.Pointer(p16)),
		certQueryContentPKCS7Emb, certQueryFormatBinary, 0,
		uintptr(unsafe.Pointer(&encoding)), uintptr(unsafe.Pointer(&contentType)), uintptr(unsafe.Pointer(&formatType)),
		uintptr(unsafe.Pointer(&store)), uintptr(unsafe.Pointer(&msg)), 0)
	if r == 0 {
		return nil, fmt.Errorf("no embedded signature (%v)", callErr)
	}
	defer api.certCloseStore.Call(store, 0)
	defer api.cryptMsgClose.Call(msg)

	var size uint32
	if r, _, e := api.cryptMsgGetParam.Call(msg, cmsgSignerInfoParam, 0, 0, uintptr(unsafe.Pointer(&size))); r == 0 {
		return nil, fmt.Errorf("CryptMsgGetParam: %v", e)
	}
	if size < uint32(unsafe.Sizeof(cmsgSignerInfoHead{})) || size > 1<<20 {
		return nil, fmt.Errorf("unexpected signer info size %d", size)
	}
	buf := make([]uint64, (size+7)/8) // 8-byte aligned (the struct holds pointers)
	if r, _, e := api.cryptMsgGetParam.Call(msg, cmsgSignerInfoParam, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return nil, fmt.Errorf("CryptMsgGetParam: %v", e)
	}
	si := (*cmsgSignerInfoHead)(unsafe.Pointer(&buf[0]))
	var ci certInfo
	ci.Issuer = si.Issuer
	ci.SerialNumber = si.SerialNumber
	certPtr, _, e := api.certFindCertificateInStore.Call(store, certEncodingX509PKCS7, 0, certFindSubjectCert,
		uintptr(unsafe.Pointer(&ci)), 0)
	runtime.KeepAlive(buf)
	if certPtr == 0 {
		return nil, fmt.Errorf("signer certificate not found: %v", e)
	}
	defer api.certFreeCertificateContext.Call(certPtr)
	cert := *(**certContext)(unsafe.Pointer(&certPtr))
	if cert.CertInfo == nil || cert.EncodedCert == nil || cert.Length == 0 {
		return nil, fmt.Errorf("empty signer certificate")
	}
	s := &codeSigner{}
	if cert.CertInfo.Subject.Size > 0 && cert.CertInfo.Subject.Data != nil {
		s.subject = append([]byte(nil), unsafe.Slice(cert.CertInfo.Subject.Data, cert.CertInfo.Subject.Size)...)
	}
	s.thumbprint = sha1.Sum(unsafe.Slice(cert.EncodedCert, cert.Length))
	if n, _, _ := api.certGetNameStringW.Call(certPtr, certNameSimpleDisplay, 0, 0, 0, 0); n > 1 {
		nb := make([]uint16, n)
		api.certGetNameStringW.Call(certPtr, certNameSimpleDisplay, 0, 0, uintptr(unsafe.Pointer(&nb[0])), n)
		s.name = syscall.UTF16ToString(nb)
	}
	return s, nil
}

// ── SHA-2 code signing support (Windows 7 / Server 2008 R2) ──────────────────

// platformSHA2SigningMissing: this Windows cannot verify SHA-256 Authenticode
// signatures (Windows 7 / 2008 R2 without KB4474419 / KB3033929): the
// self-update then reports "sha2_unsupported" instead of a bare trust error.
func platformSHA2SigningMissing() bool {
	return engine.SHA2SignatureUnsupported(engine.WindowsVersion(), sha2UpdateInstalled)
}

// sha2UpdateInstalled looks for KB4474419 / KB3033929 among the installed
// servicing packages (read only when a signature check failed). An
// unreadable key counts as installed (the error is then reported as is).
func sha2UpdateInstalled() bool {
	h, ok := regOpen(syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\Packages`)
	if !ok {
		return true
	}
	defer syscall.RegCloseKey(h)
	buf := make([]uint16, 512)
	for i := uint32(0); i < 200000; i++ {
		n := uint32(len(buf))
		if err := syscall.RegEnumKeyEx(h, i, &buf[0], &n, nil, nil, nil, nil); err != nil {
			switch err {
			case syscall.Errno(259): // ERROR_NO_MORE_ITEMS
				return false // every package seen: not installed
			case syscall.ERROR_MORE_DATA:
				continue // a name longer than any KB package name
			}
			return true // unreadable: never claim it is missing
		}
		if engine.SHA2KBPackageName(syscall.UTF16ToString(buf[:n])) {
			return true
		}
	}
	return false
}
