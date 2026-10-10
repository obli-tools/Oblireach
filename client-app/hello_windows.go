//go:build windows

package main

// hello_windows.go — Windows Hello (fingerprint, face, Windows Hello PIN) as
// a user-presence check, through the WinRT UserConsentVerifier
// (Windows.Security.Credentials.UI), from pure Go (no cgo, no generated
// projection): combase.dll's RoGetActivationFactory, then COM vtable calls.
//
//   - Availability: IUserConsentVerifierStatics::CheckAvailabilityAsync.
//   - Verification: IUserConsentVerifierInterop::RequestVerificationForWindowAsync
//     (the desktop-app variant: the prompt is parented to OUR window, the
//     one whose page asked, instead of the UWP CoreWindow the plain
//     RequestVerificationAsync needs and a desktop process does not have).
//   - The async operation is awaited by polling IAsyncInfo::get_Status (no
//     completion handler object to implement), then IAsyncOperation<T>::
//     GetResults reads the enum. The interop call needs the IID of
//     IAsyncOperation<UserConsentVerificationResult>, a parameterized
//     interface: computed from its WinRT signature (winrtPinterfaceIID, the
//     documented UUIDv5 algorithm; pinned by a test).
//
// Everything runs on a goroutine locked to its own OS thread, initialized
// multithreaded (RoInitialize MTA), never on the WebView2 UI thread (an STA
// that must keep pumping while the prompt is up).
//
// What this proves, and what it does not: Verified means Windows checked,
// right now, that the signed-in user is present (Hello's own anti-hammering
// applies). It is a gate, not a key: the vault stays sealed by DPAPI and is
// only unsealed after a Verified answer (vault.go UnlockWithPresence).

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	modCombase                 = syscall.NewLazyDLL("combase.dll")
	procRoInitialize           = modCombase.NewProc("RoInitialize")
	procRoUninitialize         = modCombase.NewProc("RoUninitialize")
	procRoGetActivationFactory = modCombase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = modCombase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = modCombase.NewProc("WindowsDeleteString")
)

type winGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// parseGUID: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" (constants of this file).
func parseGUID(s string) winGUID {
	s = strings.Trim(s, "{}")
	h := strings.ReplaceAll(s, "-", "")
	var b [16]byte
	for i := 0; i < 16; i++ {
		hi, _ := hexNibble(h[2*i])
		lo, _ := hexNibble(h[2*i+1])
		b[i] = hi<<4 | lo
	}
	return guidFromBytes(b)
}

func guidFromBytes(b [16]byte) winGUID {
	var g winGUID
	g.Data1 = binary.BigEndian.Uint32(b[0:4])
	g.Data2 = binary.BigEndian.Uint16(b[4:6])
	g.Data3 = binary.BigEndian.Uint16(b[6:8])
	copy(g.Data4[:], b[8:16])
	return g
}

func (g winGUID) bytes() [16]byte {
	var b [16]byte
	binary.BigEndian.PutUint32(b[0:4], g.Data1)
	binary.BigEndian.PutUint16(b[4:6], g.Data2)
	binary.BigEndian.PutUint16(b[6:8], g.Data3)
	copy(b[8:], g.Data4[:])
	return b
}

func (g winGUID) String() string {
	const hexd = "0123456789abcdef"
	b := g.bytes()
	var sb strings.Builder
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			sb.WriteByte('-')
		}
		sb.WriteByte(hexd[c>>4])
		sb.WriteByte(hexd[c&15])
	}
	return sb.String()
}

// winrtPinterfaceIID: the IID of a parameterized WinRT interface instance,
// from its signature ("pinterface({piid};<arg signatures>)"): UUIDv5 (SHA-1)
// of the WinRT namespace GUID and the UTF-8 signature.
func winrtPinterfaceIID(signature string) winGUID {
	ns := parseGUID("11f47ad5-7b73-42c0-abae-878b1e16adee").bytes()
	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(signature))
	sum := h.Sum(nil)
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0F) | 0x50 // version 5
	b[8] = (b[8] & 0x3F) | 0x80 // RFC 4122 variant
	return guidFromBytes(b)
}

const (
	helloClassName = "Windows.Security.Credentials.UI.UserConsentVerifier"
	// IAsyncOperation<TResult>'s generic IID, and the result enums' signatures.
	winrtIAsyncOperationPIID = "{9fc2b0bb-e446-44e2-aa61-9cab8f636af2}"
	helloResultSig           = "enum(Windows.Security.Credentials.UI.UserConsentVerificationResult;i4)"
)

var (
	iidUserConsentVerifierStatics = parseGUID("af4f3f91-564c-4ddc-b8b5-973447627c65")
	iidUserConsentVerifierInterop = parseGUID("39e050c3-4e74-441a-8dc0-b81104df949c")
	iidIAsyncInfo                 = parseGUID("00000036-0000-0000-c000-000000000046")
	// IAsyncOperation<UserConsentVerificationResult>
	iidAsyncOpVerificationResult = winrtPinterfaceIID("pinterface(" + winrtIAsyncOperationPIID + ";" + helloResultSig + ")")
)

// Vtable slots. IUnknown: 0 QueryInterface, 1 AddRef, 2 Release;
// IInspectable: 3 GetIids, 4 GetRuntimeClassName, 5 GetTrustLevel.
const (
	vtQueryInterface = 0
	vtRelease        = 2
	// IUserConsentVerifierStatics
	vtCheckAvailabilityAsync = 6
	// IUserConsentVerifierInterop
	vtRequestVerificationForWindowAsync = 6
	// IAsyncOperation<T>: 6 put_Completed, 7 get_Completed, 8 GetResults
	vtAsyncOpGetResults = 8
	// IAsyncInfo: 6 get_Id, 7 get_Status, 8 get_ErrorCode, 9 Cancel, 10 Close
	vtAsyncInfoStatus    = 7
	vtAsyncInfoErrorCode = 8
	vtAsyncInfoCancel    = 9
)

// comObject: a COM interface pointer (its first field is the vtable).
type comObject struct{ lpVtbl *[16]uintptr }

func (o *comObject) call(slot int, args ...uintptr) uint32 {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(o.lpVtbl[slot], all...)
	return uint32(r)
}

func (o *comObject) release() {
	if o != nil {
		o.call(vtRelease)
	}
}

func (o *comObject) query(iid *winGUID) (*comObject, error) {
	var out *comObject
	if hr := o.call(vtQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); failedHR(hr) || out == nil {
		return nil, hresultError(hr)
	}
	return out, nil
}

func failedHR(hr uint32) bool { return int32(hr) < 0 }

type hresultError uint32

func (e hresultError) Error() string { return "HRESULT 0x" + strings.ToUpper(hex32(uint32(e))) }

func hex32(v uint32) string {
	const hexd = "0123456789abcdef"
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = hexd[v&15]
		v >>= 4
	}
	return string(b[:])
}

// hstring: a WinRT string (WindowsCreateString); free it with its func.
func hstring(s string) (uintptr, func(), error) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, func() {}, err
	}
	var h uintptr
	r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	if failedHR(uint32(r)) {
		return 0, func() {}, hresultError(uint32(r))
	}
	return h, func() { procWindowsDeleteString.Call(h) }, nil
}

// activationFactory: the class factory of UserConsentVerifier, as iid.
func helloFactory(iid *winGUID) (*comObject, error) {
	if err := procRoGetActivationFactory.Find(); err != nil {
		return nil, errHelloUnavailable
	}
	cls, free, err := hstring(helloClassName)
	if err != nil {
		return nil, err
	}
	defer free()
	var f *comObject
	r, _, _ := procRoGetActivationFactory.Call(cls, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&f)))
	if failedHR(uint32(r)) || f == nil {
		return nil, hresultError(uint32(r))
	}
	return f, nil
}

// Async status (AsyncStatus).
const (
	asyncStarted   = 0
	asyncCompleted = 1
	asyncCanceled  = 2
	asyncError     = 3
)

// (errHelloTimeout, errHelloCanceled: vault_hello.go.)
var errHelloUnavailable = errors.New("windows hello: unavailable")

// awaitInt32 waits for an IAsyncOperation<enum> (polling its IAsyncInfo)
// and reads its result. Canceled at the deadline.
func awaitInt32(op *comObject, timeout time.Duration) (int32, error) {
	info, err := op.query(&iidIAsyncInfo)
	if err != nil {
		return 0, err
	}
	defer info.release()
	deadline := time.Now().Add(timeout)
	for {
		var st int32
		if hr := info.call(vtAsyncInfoStatus, uintptr(unsafe.Pointer(&st))); failedHR(hr) {
			return 0, hresultError(hr)
		}
		switch st {
		case asyncCompleted:
			var res int32
			if hr := op.call(vtAsyncOpGetResults, uintptr(unsafe.Pointer(&res))); failedHR(hr) {
				return 0, hresultError(hr)
			}
			return res, nil
		case asyncCanceled:
			return 0, errHelloCanceled
		case asyncError:
			var code uint32
			info.call(vtAsyncInfoErrorCode, uintptr(unsafe.Pointer(&code)))
			return 0, hresultError(code)
		}
		if time.Now().After(deadline) {
			info.call(vtAsyncInfoCancel)
			return 0, errHelloTimeout
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// onWinRTThread runs fn on a fresh OS thread initialized for WinRT (MTA).
func onWinRTThread(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // (never unlocked: the thread goes with the goroutine)
		const roInitMultithreaded = 1
		r, _, _ := procRoInitialize.Call(roInitMultithreaded)
		if failedHR(uint32(r)) {
			done <- hresultError(uint32(r))
			return
		}
		defer procRoUninitialize.Call()
		done <- fn()
	}()
	return <-done
}

// UserConsentVerifierAvailability (the UserConsentVerificationResult
// values: vault_hello.go).
const helloAvailabilityOK = 0

// winHello: Windows Hello as the platform's presence verifier
// (presenceVerifier, platform.go).
type winHello struct{}

var platformPresence presenceVerifier = winHello{}

func (winHello) available() (bool, error) { return helloCheckAvailability() }

func (winHello) verify(owner uintptr, message string, timeout time.Duration) (int32, error) {
	return helloVerifyForWindow(owner, message, timeout)
}

func (winHello) shown(owner uintptr) bool { return helloWindowShown(owner) }

// helloWindowShown: hwnd is visible, not minimized, and the foreground
// window is it (or one of its own: the same root).
func helloWindowShown(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if v, _, _ := procIsWindowVisibleDW.Call(hwnd); v == 0 {
		return false
	}
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		return false
	}
	root, _, _ := procGetAncestor.Call(hwnd, gaRoot)
	if root == 0 {
		root = hwnd
	}
	fg := foregroundRoot()
	return fg != 0 && fg == root
}

// helloCheckAvailability: Windows Hello can verify this user now (a
// device, set up for this account, not disabled by policy).
func helloCheckAvailability() (bool, error) {
	a, err := helloAvailabilityRaw()
	return err == nil && a == helloAvailabilityOK, err
}

// helloAvailabilityRaw: the UserConsentVerifierAvailability value.
func helloAvailabilityRaw() (int32, error) {
	if err := modCombase.Load(); err != nil {
		return -1, errHelloUnavailable
	}
	var avail int32 = -1
	err := onWinRTThread(func() error {
		st, err := helloFactory(&iidUserConsentVerifierStatics)
		if err != nil {
			return err
		}
		defer st.release()
		var op *comObject
		if hr := st.call(vtCheckAvailabilityAsync, uintptr(unsafe.Pointer(&op))); failedHR(hr) || op == nil {
			return hresultError(hr)
		}
		defer op.release()
		a, err := awaitInt32(op, 15*time.Second)
		avail = a
		return err
	})
	return avail, err
}

// helloVerifyForWindow shows the Windows Hello prompt, parented to hwnd,
// with message, and returns the UserConsentVerificationResult.
func helloVerifyForWindow(hwnd uintptr, message string, timeout time.Duration) (int32, error) {
	if hwnd == 0 {
		return 0, errHelloUnavailable
	}
	if err := modCombase.Load(); err != nil {
		return 0, errHelloUnavailable
	}
	var res int32 = -1
	err := onWinRTThread(func() error {
		f, err := helloFactory(&iidUserConsentVerifierInterop)
		if err != nil {
			return err
		}
		defer f.release()
		msg, free, err := hstring(message)
		if err != nil {
			return err
		}
		defer free()
		var op *comObject
		if hr := f.call(vtRequestVerificationForWindowAsync, hwnd, msg, uintptr(unsafe.Pointer(&iidAsyncOpVerificationResult)), uintptr(unsafe.Pointer(&op))); failedHR(hr) || op == nil {
			return hresultError(hr)
		}
		defer op.release()
		r, err := awaitInt32(op, timeout)
		res = r
		return err
	})
	return res, err
}
