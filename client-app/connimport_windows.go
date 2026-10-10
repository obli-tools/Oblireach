//go:build windows

package main

// connimport_windows.go — the Windows side of the connection tree's import /
// export (connimport_bridge.go): Windows' Open / Save dialogs (fileDialogs,
// platform.go) and the WinSCP registry.

import (
	"strings"
	"syscall"
	"unsafe"
)

var procConnGetSaveFileNameW = modComdlg32.NewProc("GetSaveFileNameW")

const connOfnOverwritePrompt = 0x2

// winDialogs: Windows' common dialogs (fileDialogs, platform.go).
type winDialogs struct{}

var platformDialogs fileDialogs = winDialogs{}

func (winDialogs) pickFile(owner uintptr, save bool, title, filter, initialDir, suggested, defExt string) (string, bool) {
	return connPickFile(owner, save, title, filter, initialDir, suggested, defExt)
}

func (winDialogs) pickProgram(owner uintptr, title, filterLabel string) (string, bool) {
	return pickEditorDialog(owner, title, filterLabel)
}

// connPickFile: Windows' Open (save false) or Save As dialog, owned by the
// main window. filter: "label\x00pattern\x00..." pairs. ok false: cancelled.
func connPickFile(owner uintptr, save bool, title, filter, initialDir, suggested, defExt string) (string, bool) {
	proc := procGetOpenFileNameW
	if save {
		proc = procConnGetSaveFileNameW
	}
	if err := proc.Find(); err != nil {
		return "", false
	}
	file := make([]uint16, 32768)
	if suggested != "" {
		if u, err := syscall.UTF16FromString(suggested); err == nil && len(u) < len(file) {
			copy(file, u)
		}
	}
	f16, _ := syscall.UTF16FromString(filter + "\x00")
	t16, _ := syscall.UTF16PtrFromString(strings.ReplaceAll(title, "\x00", ""))
	var dir16, ext16 *uint16
	if initialDir != "" {
		dir16, _ = syscall.UTF16PtrFromString(initialDir)
	}
	if defExt != "" {
		ext16, _ = syscall.UTF16PtrFromString(defExt)
	}
	ofn := openFileNameW{hwndOwner: owner, lpstrFilter: &f16[0], nFilterIndex: 1, lpstrFile: &file[0], nMaxFile: uint32(len(file)),
		lpstrInitialDir: dir16, lpstrTitle: t16, lpstrDefExt: ext16,
		Flags: ofnPathMustExist | ofnHideReadOnly | ofnNoChangeDir | ofnExplorer | ofnDontAddRecent}
	if save {
		ofn.Flags |= connOfnOverwritePrompt
	} else {
		ofn.Flags |= ofnFileMustExist
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return "", false
	}
	p := syscall.UTF16ToString(file)
	return p, p != ""
}

// ── WinSCP's registry ──────────────────────────────────────────────────────

const winscpRegRoot = `Software\Martin Prikryl\WinSCP 2`

func regOpen(path string) (syscall.Handle, bool) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var h syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, p16, 0, syscall.KEY_READ, &h) != nil {
		return 0, false
	}
	return h, true
}

// regValue: a REG_SZ as is, a REG_DWORD in decimal ("" when absent).
func regValue(h syscall.Handle, name string) string {
	n16, _ := syscall.UTF16PtrFromString(name)
	var typ, size uint32
	if syscall.RegQueryValueEx(h, n16, nil, &typ, nil, &size) != nil || size == 0 || size > 1<<20 {
		return ""
	}
	buf := make([]byte, size)
	if syscall.RegQueryValueEx(h, n16, nil, &typ, &buf[0], &size) != nil {
		return ""
	}
	switch typ {
	case syscall.REG_SZ, syscall.REG_EXPAND_SZ:
		u := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[0])), size/2)
		return syscall.UTF16ToString(u)
	case syscall.REG_DWORD:
		if size >= 4 {
			return itoa(int(int32(uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24)))
		}
	}
	return ""
}

// winscpRegistrySessions: WinSCP's sessions from the registry (the values
// the importer reads), and whether a master password is set. found false:
// WinSCP keeps nothing in the registry for this user.
func winscpRegistrySessions() (sessions []winscpSession, master, found bool) {
	if h, ok := regOpen(winscpRegRoot + `\Configuration\Security`); ok {
		master = regValue(h, "UseMasterPassword") == "1"
		syscall.RegCloseKey(h)
	}
	root, ok := regOpen(winscpRegRoot + `\Sessions`)
	if !ok {
		return nil, master, false
	}
	defer syscall.RegCloseKey(root)
	for i := uint32(0); i < 10000; i++ {
		name := make([]uint16, 1024)
		n := uint32(len(name))
		if syscall.RegEnumKeyEx(root, i, &name[0], &n, nil, nil, nil, nil) != nil {
			break
		}
		munged := syscall.UTF16ToString(name[:n])
		h, ok := regOpen(winscpRegRoot + `\Sessions\` + munged)
		if !ok {
			continue
		}
		s := winscpSession{Name: winscpUnmunge(munged), Values: map[string]string{}}
		for _, v := range []string{"HostName", "UserName", "PortNumber", "FSProtocol", "Ftps", "Password", "PublicKeyFile"} {
			if x := regValue(h, v); x != "" {
				s.Values[v] = x
			}
		}
		syscall.RegCloseKey(h)
		sessions = append(sessions, s)
	}
	return sessions, master, true
}
