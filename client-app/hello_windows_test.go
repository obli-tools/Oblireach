//go:build windows

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// The IID of IAsyncOperation<UserConsentVerificationResult>, from its WinRT
// signature, is the one Windows' own headers use.
func TestHelloPinterfaceIID(t *testing.T) {
	if got := iidAsyncOpVerificationResult.String(); got != "fd596ffd-2318-558f-9dbe-d21df43764a5" {
		t.Fatalf("IAsyncOperation<UserConsentVerificationResult>: %s", got)
	}
	// (another known instance: IAsyncOperation<bool>)
	if got := winrtPinterfaceIID("pinterface({9fc2b0bb-e446-44e2-aa61-9cab8f636af2};b1)").String(); got != "cdb5efb3-5788-509d-9be1-71ccb8a3362a" {
		t.Fatalf("IAsyncOperation<bool>: %s", got)
	}
	if parseGUID("{39E050C3-4E74-441A-8DC0-B81104DF949C}").String() != "39e050c3-4e74-441a-8dc0-b81104df949c" {
		t.Fatal("parseGUID")
	}
}

// The real WinRT path (activation factory, async operation, result): the
// availability check, which shows nothing. Its answer depends on the machine.
func TestHelloAvailabilityProbe(t *testing.T) {
	ok, err := helloCheckAvailability()
	t.Logf("Windows Hello available: %v (err %v)", ok, err)
	if err != nil {
		t.Fatalf("the WinRT call failed: %v", err)
	}
}

// The verification call itself (IUserConsentVerifierInterop::
// RequestVerificationForWindowAsync, the pinterface IID): opt-in
// (OBLIREACH_HELLO_VERIFY=1), since on a machine where Windows Hello is set
// up it shows the real prompt. Where no device is present it answers
// DeviceNotPresent at once, without any UI (checked on a test machine:
// availability 1, result 1).
func TestHelloVerifyForWindowOptIn(t *testing.T) {
	if os.Getenv("OBLIREACH_HELLO_VERIFY") == "" {
		t.Skip("OBLIREACH_HELLO_VERIFY not set")
	}
	a, err := helloAvailabilityRaw()
	t.Logf("availability %d (%v)", a, err)
	cls, _ := syscall.UTF16PtrFromString("STATIC")
	ti, _ := syscall.UTF16PtrFromString("Oblireach Windows Hello test")
	hwnd, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(ti)), wsOverlappedWindow, 100, 100, 300, 200, 0, 0, 0, 0)
	defer procDestroyWindow.Call(hwnd)
	r, err := helloVerifyForWindow(hwnd, "Oblireach test", 60*time.Second)
	t.Logf("verification result %d (%v)", r, err)
	if err != nil {
		t.Fatal(err)
	}
}
