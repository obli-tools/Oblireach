//go:build windows

package main

// "Start with Windows" and "Start minimized", Settings > Application in the
// main window.
//
//   - Start with Windows: the per-user Run key (HKCU, no administrator
//     rights), value "Oblireach" = the quoted path of the running exe, plus
//     --minimized when "Start minimized" is on. The page shows what the
//     registry says; turning it off deletes the value. Task Manager's
//     "Startup apps" can turn the entry off (StartupApproved\Run): shown as
//     off, and turning it on again here re-enables it.
//   - Start minimized (config.json): the launches from the Run key start
//     minimized in the taskbar (--minimized); "every launch" (also a launch
//     by hand) is a second choice. With "Keep in the notification area"
//     (tray.go, tray_windows.go) the window starts hidden there instead.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	procRegCreateKeyExW = modAdvapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = modAdvapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = modAdvapi32.NewProc("RegDeleteValueW")
)

const (
	regOptionNonVolatile = 0
	regSZ                = 1
	regBinary            = 3
	errorFileNotFound    = 2
)

// runKeyStore: the registry values behind "Start with Windows" (a fake in
// the tests).
type runKeyStore interface {
	get() (value string, present bool, err error)     // the Run value
	set(value string) error                           // writes it
	remove() error                                    // deletes it (absent: fine)
	approved() (data []byte, present bool, err error) // its StartupApproved value
	clearApproved() error                             // deletes that (absent: fine)
}

// regRunKey: HKCU\<runKey>\<name> and HKCU\<approvedKey>\<name>.
type regRunKey struct{ runKey, approvedKey, name string }

func (r regRunKey) get() (string, bool, error) {
	data, present, err := regRead(r.runKey, r.name)
	if err != nil || !present {
		return "", present, err
	}
	u := make([]uint16, len(data)/2)
	for i := range u {
		u[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u)), true, nil
}

func (r regRunKey) set(value string) error {
	u := utf16.Encode([]rune(value + "\x00"))
	return regWrite(r.runKey, r.name, regSZ, unsafe.Pointer(&u[0]), uint32(len(u)*2))
}

func (r regRunKey) remove() error { return regDelete(r.runKey, r.name) }

func (r regRunKey) approved() ([]byte, bool, error) { return regRead(r.approvedKey, r.name) }

func (r regRunKey) clearApproved() error { return regDelete(r.approvedKey, r.name) }

// regRead reads HKCU\key\name (any type). A missing key or value: absent.
func regRead(key, name string) ([]byte, bool, error) {
	var k syscall.Handle
	k16, _ := syscall.UTF16PtrFromString(key)
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, k16, 0, syscall.KEY_QUERY_VALUE, &k); err != nil {
		if err == syscall.ERROR_FILE_NOT_FOUND {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer syscall.RegCloseKey(k)
	n16, _ := syscall.UTF16PtrFromString(name)
	var typ, size uint32
	if err := syscall.RegQueryValueEx(k, n16, nil, &typ, nil, &size); err != nil {
		if err == syscall.ERROR_FILE_NOT_FOUND {
			return nil, false, nil
		}
		return nil, false, err
	}
	if size == 0 {
		return []byte{}, true, nil
	}
	if size > 64<<10 {
		return nil, true, errors.New(tr("startup.err.valueTooLarge"))
	}
	buf := make([]byte, size)
	if err := syscall.RegQueryValueEx(k, n16, nil, &typ, &buf[0], &size); err != nil {
		return nil, false, err
	}
	return buf[:size], true, nil
}

func regWrite(key, name string, typ uint32, data unsafe.Pointer, size uint32) error {
	k16, _ := syscall.UTF16PtrFromString(key)
	var k syscall.Handle
	if r, _, _ := procRegCreateKeyExW.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(k16)), 0, 0,
		regOptionNonVolatile, syscall.KEY_SET_VALUE, 0, uintptr(unsafe.Pointer(&k)), 0); r != 0 {
		return syscall.Errno(r)
	}
	defer syscall.RegCloseKey(k)
	n16, _ := syscall.UTF16PtrFromString(name)
	if r, _, _ := procRegSetValueExW.Call(uintptr(k), uintptr(unsafe.Pointer(n16)), 0, uintptr(typ), uintptr(data), uintptr(size)); r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func regDelete(key, name string) error {
	var k syscall.Handle
	k16, _ := syscall.UTF16PtrFromString(key)
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, k16, 0, syscall.KEY_SET_VALUE, &k); err != nil {
		if err == syscall.ERROR_FILE_NOT_FOUND {
			return nil
		}
		return err
	}
	defer syscall.RegCloseKey(k)
	n16, _ := syscall.UTF16PtrFromString(name)
	if r, _, _ := procRegDeleteValueW.Call(uintptr(k), uintptr(unsafe.Pointer(n16))); r != 0 && r != errorFileNotFound {
		return syscall.Errno(r)
	}
	return nil
}

// (startupState, what Settings > Application shows: platform.go.)

// startupCtl: the settings of the main window (its bridge's).
type startupCtl struct {
	exe      string
	reg      runKeyStore
	prefs    func() (minimized, always bool)
	setPrefs func(minimized, always bool)
	tray     func() bool
	setTray  func(on bool)
	onTray   func(on bool) // the icon follows (tray_windows.go appTray.enable)
}

func newStartupCtl(p *Proxy) *startupCtl {
	exe, _ := os.Executable()
	return &startupCtl{
		exe:      exe,
		reg:      regRunKey{runKey: runKeyPath, approvedKey: runApprovedPath, name: runValueName},
		prefs:    p.startupPrefs,
		setPrefs: p.setStartupPrefs,
		tray:     p.trayPref,
		setTray:  p.setTrayPref,
	}
}

func (c *startupCtl) state() (startupState, error) {
	minimized, always := c.prefs()
	st := startupState{Minimized: minimized, MinimizedAlways: always, Exe: c.exe}
	if c.tray != nil {
		st.Tray = c.tray()
	}
	v, present, err := c.reg.get()
	if err != nil {
		return st, err
	}
	if present {
		data, apPresent, aerr := c.reg.approved()
		if aerr != nil {
			return st, aerr
		}
		if startupApprovedEnabled(data, apPresent) {
			st.RunAtLogin = true
		} else {
			st.DisabledInTM = true
		}
		if exe, _ := parseRunKeyCommand(v); !pathsEqual(exe, c.exe) {
			st.OtherCopy = exe
		}
	}
	return st, nil
}

// set applies one choice: "runAtLogin", "minimized", "minimizedAlways" or
// "tray" (the notification-area icon).
func (c *startupCtl) set(what string, on bool) (startupState, error) {
	minimized, always := c.prefs()
	switch what {
	case "runAtLogin":
		if on {
			cmd, err := runKeyCommand(c.exe, minimized)
			if err != nil {
				return c.stateOr(err)
			}
			if err := c.reg.set(cmd); err != nil {
				return c.stateOr(err)
			}
			// Turned on here: on for Windows too (Task Manager's switch).
			if err := c.reg.clearApproved(); err != nil {
				return c.stateOr(err)
			}
		} else {
			if err := c.reg.remove(); err != nil {
				return c.stateOr(err)
			}
			_ = c.reg.clearApproved()
		}
	case "minimized":
		c.setPrefs(on, always)
		// The Run value carries the flag: rewritten when there is one of
		// this program (Task Manager's switch is left as it is).
		if v, present, err := c.reg.get(); err == nil && present {
			if exe, _ := parseRunKeyCommand(v); pathsEqual(exe, c.exe) {
				if cmd, err := runKeyCommand(c.exe, on); err == nil {
					if err := c.reg.set(cmd); err != nil {
						return c.stateOr(err)
					}
				}
			}
		}
	case "minimizedAlways":
		c.setPrefs(minimized, on)
	case "tray":
		if c.setTray == nil {
			return startupState{}, errBadArgument
		}
		c.setTray(on)
		if c.onTray != nil {
			c.onTray(on)
		}
	default:
		return startupState{}, errBadArgument
	}
	return c.state()
}

func (c *startupCtl) stateOr(err error) (startupState, error) {
	st, _ := c.state()
	return st, err
}

func pathsEqual(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ── Bindings (main window) ─────────────────────────────────────────────────

// startupGet (JS reachStartupGet): Settings > Application.
func (b *nativeBridge) startupGet(tok string) (startupState, error) {
	if !b.allowed(tok) {
		return startupState{}, errNotAllowed
	}
	if b.startup == nil {
		return startupState{}, errors.New(tr("startup.err.unavailable"))
	}
	return b.startup.state()
}

// startupSet (JS reachStartupSet): one choice of Settings > Application.
// The program registered is always this one (its path is Go's).
func (b *nativeBridge) startupSet(tok, what string, on bool) (startupState, error) {
	if !b.allowed(tok) {
		return startupState{}, errNotAllowed
	}
	if b.startup == nil {
		return startupState{}, errors.New(tr("startup.err.unavailable"))
	}
	return b.startup.set(what, on)
}
