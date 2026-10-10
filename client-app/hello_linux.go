//go:build linux

package main

// hello_linux.go — the vault's biometric unlock on Linux (presenceVerifier,
// platform.go): fprintd, the fingerprint daemon on the SYSTEM bus
// (net.reactivated.Fprint). The same model as Windows Hello
// (hello_windows.go, vault_hello.go): a presence check, a gate and never a
// key (the vault stays sealed by the Secret Service; it is only opened after
// a "match"), opt-in, the vault code always works, the automatic prompt's
// rules of hello_auto.go.
//
//   - available: a default reader (Manager.GetDefaultDevice) with at least
//     one finger enrolled for this user (Device.ListEnrolledFingers). No
//     fprintd, no reader or nothing enrolled: unavailable, and the page
//     hides the option (vaultHelloJSON.available false).
//   - verify: Device.Claim(""), the VerifyStatus signals watched, then
//     VerifyStart("any"); "verify-match" = verified; "verify-no-match" up to
//     fprintVerifyTries times (VerifyStart again), then "retries
//     exhausted"; the scan-quality answers (retry-scan, swipe-too-short...)
//     wait for the next scan; a disconnected reader = not present. Always
//     VerifyStop and Release at the end (the reader is shared with the
//     lock screen and sudo).
//
// polkit lets the active local session verify its own user by default
// (net.reactivated.fprint.device.verify). Inside the Flatpak the manifest
// grants --system-talk-name=net.reactivated.Fprint (flatpak/tools.obli.
// Oblireach.yml): without it the proxy hides the name and the option stays
// hidden.

import (
	"errors"
	"os/user"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	fprintBusName     = "net.reactivated.Fprint"
	fprintManagerPath = dbus.ObjectPath("/net/reactivated/Fprint/Manager")
	fprintManagerIf   = "net.reactivated.Fprint.Manager"
	fprintDeviceIf    = "net.reactivated.Fprint.Device"
	fprintErrPrefix   = "net.reactivated.Fprint.Error."
	// "no match" answers accepted before the prompt gives up.
	fprintVerifyTries = 3
	// One D-Bus call (not the scan itself).
	fprintCallTimeout = 10 * time.Second
)

// fprintDial: the system bus (tests: a private bus).
var fprintDial = func() (*dbus.Conn, error) { return dbus.ConnectSystemBus() }

// fprintUser: the user whose fingers are checked ("" = the caller's, which
// is what Claim("") means).
var fprintUser = func() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// linuxWindowShown: the UI host says whether window owner is on screen and
// active (the automatic prompt waits for it); nil until a UI host exists.
var linuxWindowShown func(owner uintptr) bool

type linuxPresence struct{}

var platformPresence presenceVerifier = linuxPresence{}

func fprintErr(err error) string {
	if n := dbusErrName(err); len(n) > len(fprintErrPrefix) && n[:len(fprintErrPrefix)] == fprintErrPrefix {
		return n[len(fprintErrPrefix):]
	}
	return ""
}

// fprintDevice: the default reader's object (conn open).
func fprintDevice(conn *dbus.Conn) (dbus.ObjectPath, error) {
	var dev dbus.ObjectPath
	call := conn.Object(fprintBusName, fprintManagerPath).Call(fprintManagerIf+".GetDefaultDevice", 0)
	if call.Err != nil {
		return "", call.Err
	}
	if err := call.Store(&dev); err != nil {
		return "", err
	}
	if !dev.IsValid() || dev == "/" {
		return "", errors.New("fprintd: no reader")
	}
	return dev, nil
}

func (linuxPresence) available() (bool, error) {
	conn, err := fprintDial()
	if err != nil {
		return false, nil // (no system bus: unavailable, not an error)
	}
	defer conn.Close()
	dev, err := fprintDevice(conn)
	if err != nil {
		return false, nil
	}
	var fingers []string
	call := conn.Object(fprintBusName, dev).Call(fprintDeviceIf+".ListEnrolledFingers", 0, fprintUser())
	if call.Err != nil || call.Store(&fingers) != nil {
		return false, nil
	}
	return len(fingers) > 0, nil
}

func (linuxPresence) verify(owner uintptr, message string, timeout time.Duration) (int32, error) {
	conn, err := fprintDial()
	if err != nil {
		return helloDeviceNotPresent, nil
	}
	defer conn.Close()
	dev, err := fprintDevice(conn)
	if err != nil {
		return helloDeviceNotPresent, nil
	}
	obj := conn.Object(fprintBusName, dev)
	if c := obj.Call(fprintDeviceIf+".Claim", 0, ""); c.Err != nil {
		switch fprintErr(c.Err) {
		case "AlreadyInUse":
			return helloDeviceBusy, nil
		case "PermissionDenied":
			return helloDisabledByPolicy, nil
		case "NoEnrolledPrints":
			return helloNotConfigured, nil
		case "NoSuchDevice":
			return helloDeviceNotPresent, nil
		}
		return 0, c.Err
	}
	defer obj.Call(fprintDeviceIf+".Release", 0)
	w, err := watchSignal(conn, dev, fprintDeviceIf, "VerifyStatus")
	if err != nil {
		return 0, err
	}
	defer w.stop()
	start := func() (int32, error, bool) {
		if c := obj.Call(fprintDeviceIf+".VerifyStart", 0, "any"); c.Err != nil {
			switch fprintErr(c.Err) {
			case "NoEnrolledPrints":
				return helloNotConfigured, nil, true
			case "AlreadyInUse":
				return helloDeviceBusy, nil, true
			}
			return 0, c.Err, true
		}
		return 0, nil, false
	}
	if r, err, stop := start(); stop {
		return r, err
	}
	deadline := time.Now().Add(timeout)
	misses := 0
	for {
		left := time.Until(deadline)
		if left <= 0 {
			obj.Call(fprintDeviceIf+".VerifyStop", 0)
			return 0, errHelloTimeout
		}
		body, err := w.wait(left)
		if err != nil {
			obj.Call(fprintDeviceIf+".VerifyStop", 0)
			if errors.Is(err, errPortalCancel) {
				return 0, errHelloTimeout
			}
			return 0, err
		}
		result, _ := body[0].(string)
		done := len(body) > 1 && body[1] == true
		switch result {
		case "verify-match":
			obj.Call(fprintDeviceIf+".VerifyStop", 0)
			return helloVerified, nil
		case "verify-disconnected":
			obj.Call(fprintDeviceIf+".VerifyStop", 0)
			return helloDeviceNotPresent, nil
		case "verify-unknown-error":
			obj.Call(fprintDeviceIf+".VerifyStop", 0)
			return 0, errors.New("fprintd: verify-unknown-error")
		case "verify-no-match":
			misses++
			if misses >= fprintVerifyTries {
				obj.Call(fprintDeviceIf+".VerifyStop", 0)
				return helloRetriesExhausted, nil
			}
		}
		// (a finished attempt that did not match: the next one)
		if done {
			obj.Call(fprintDeviceIf+".VerifyStop", 0)
			if r, err, stop := start(); stop {
				return r, err
			}
		}
	}
}

func (linuxPresence) shown(owner uintptr) bool {
	if linuxWindowShown == nil || owner == 0 {
		return false
	}
	return linuxWindowShown(owner)
}
