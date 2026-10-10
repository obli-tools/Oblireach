//go:build windows

package main

// editsession_windows.go — starting the editor on a working copy
// (editsession.go), the only place the app starts a program for a file, and
// the native dialog that picks a custom editor.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	assocStrCommand  = 1
	ofnHideReadOnly  = 0x4
	ofnNoChangeDir   = 0x8
	ofnPathMustExist = 0x800
	ofnFileMustExist = 0x1000
	ofnExplorer      = 0x80000
	ofnDontAddRecent = 0x02000000
	errNoAssociation = 1155
	seErrNoAssoc     = 31
)

// Types that run when opened (scripts, programs, shortcuts): never handed to
// their "default application", whatever Windows says of them (Notepad then).
var editScriptExts = map[string]bool{
	".py": true, ".pyw": true, ".pyz": true, ".pyc": true, ".rb": true, ".rbw": true, ".pl": true, ".php": true, ".jar": true,
	".jnlp": true, ".ps1": true, ".psm1": true, ".psd1": true, ".ps1xml": true, ".psc1": true, ".vbs": true, ".vbe": true,
	".js": true, ".jse": true, ".wsf": true, ".wsh": true, ".wsc": true, ".bat": true, ".cmd": true, ".com": true, ".exe": true,
	".scr": true, ".msi": true, ".msp": true, ".mst": true, ".msc": true, ".hta": true, ".lnk": true, ".url": true, ".reg": true,
	".cpl": true, ".appref-ms": true, ".application": true, ".appx": true, ".appxbundle": true, ".msix": true, ".msixbundle": true,
	".appinstaller": true, ".library-ms": true, ".search-ms": true, ".searchconnector-ms": true, ".settingcontent-ms": true,
	".diagcab": true, ".website": true, ".sh": true, ".bash": true, ".ksh": true, ".csh": true, ".tcl": true, ".lua": true,
	".scf": true, ".inf": true, ".ins": true, ".isp": true, ".pif": true, ".gadget": true, ".chm": true, ".hlp": true,
	".xbap": true, ".vsto": true, ".ade": true, ".adp": true, ".mde": true, ".accde": true, ".sct": true, ".shb": true, ".shs": true,
	".xll": true, ".wll": true, ".iqy": true, ".slk": true, ".rdp": true, ".theme": true, ".themepack": true, ".desktopthemepackfile": true,
	".cab": true, ".ocx": true, ".dll": true, ".sys": true, ".drv": true, ".cer": true, ".crt": true, ".der": true, ".p7b": true,
	".mof": true, ".job": true, ".ws": true, ".mad": true, ".msh": true, ".msh1": true, ".msh2": true, ".mshxml": true,
}

func assocIsDangerous(ext string) bool {
	if err := procAssocIsDangerous.Find(); err != nil {
		return true
	}
	p, err := syscall.UTF16PtrFromString(ext)
	if err != nil {
		return true
	}
	r, _, _ := procAssocIsDangerous.Call(uintptr(unsafe.Pointer(p)))
	return r != 0
}

// assocHasVerb: the type has this verb ("edit": its editor, not its viewer).
func assocHasVerb(ext, verb string) bool {
	e16, err := syscall.UTF16PtrFromString(ext)
	if err != nil {
		return false
	}
	v16, err := syscall.UTF16PtrFromString(verb)
	if err != nil {
		return false
	}
	var n uint32
	hr, _, _ := procAssocQueryStringW.Call(0, assocStrCommand, uintptr(unsafe.Pointer(e16)), uintptr(unsafe.Pointer(v16)), 0, uintptr(unsafe.Pointer(&n)))
	return int32(hr) >= 0 && n > 1
}

func notepadPath() string {
	win := knownFolder(&folderIDWindows)
	if win == "" {
		if w, err := winDirCall(procGetSystemWindowsDirW); err == nil {
			win = w
		}
	}
	if win == "" {
		return ""
	}
	return filepath.Join(win, "System32", "notepad.exe")
}

// startProgram: prog with the working copy as its only argument, in the
// working copy's folder; not waited for.
func startProgram(prog, file, dir string) error {
	cmd := exec.Command(prog, file)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// systemEditLaunch: the type's "edit" verb when it has one, else its default
// one (ShellExecuteEx, no UI of the shell's).
func systemEditLaunch(file, dir, ext string) error {
	f16, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return fsE("invalid_path")
	}
	d16, _ := syscall.UTF16PtrFromString(dir)
	var verb *uint16
	if assocHasVerb(ext, "edit") {
		verb, _ = syscall.UTF16PtrFromString("edit")
	}
	return onComThread(func() error {
		sei := shellExecuteInfo{fMask: seeMaskNoAsync | seeMaskFlagNoUI, lpVerb: verb, lpFile: f16, lpDirectory: d16, nShow: swShowNormal}
		sei.cbSize = uint32(unsafe.Sizeof(sei))
		if r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei))); r == 0 {
			if en, ok := e.(syscall.Errno); ok && (en == errNoAssociation || en == seErrNoAssoc) {
				return fsE("no_editor")
			}
			return fsE("no_editor")
		}
		return nil
	})
}

// The launcher's calls (the tests watch them instead of starting programs).
var (
	editStartProgram = startProgram
	editSystemLaunch = systemEditLaunch
	editDangerousExt = assocIsDangerous
)

// plannedEditor: the editor launchEditor starts for this working copy with
// these settings: notepad (Notepad itself, or instead of the default
// application of a type that runs, a script or a program), system, custom.
func plannedEditor(file string, s FilesSettings) string {
	switch s.Editor {
	case "system":
		if systemEditFallback(file) {
			return "notepad"
		}
		return "system"
	case "custom":
		return "custom"
	}
	return "notepad"
}

// systemEditFallback: a type never handed to its default application (no
// extension, a script or a program, a type Windows deems dangerous).
func systemEditFallback(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == "" || editScriptExts[ext] || editDangerousExt(ext)
}

// launchEditor: the working copy in the editor of Settings > Files.
func launchEditor(e *editSession, s FilesSettings) (*editLaunchResult, error) {
	notepad := func(fallback string) (*editLaunchResult, error) {
		np := notepadPath()
		if np == "" {
			return nil, fsE("no_editor")
		}
		if err := editStartProgram(np, e.file, e.dir); err != nil {
			return nil, fsE("no_editor")
		}
		return &editLaunchResult{Editor: "notepad", Fallback: fallback}, nil
	}
	switch s.Editor {
	case "system":
		if systemEditFallback(e.file) {
			return notepad("notepad")
		}
		if err := editSystemLaunch(e.file, e.dir, strings.ToLower(filepath.Ext(e.file))); err != nil {
			return nil, err
		}
		return &editLaunchResult{Editor: "system"}, nil
	case "custom":
		if err := editorPathOK(s.EditorPath); err != nil { // (again at launch: it may have changed)
			return nil, err
		}
		if err := editStartProgram(s.EditorPath, e.file, e.dir); err != nil {
			return nil, fsE("no_editor")
		}
		return &editLaunchResult{Editor: "custom", EditorName: editorDisplayName(s.EditorPath)}, nil
	}
	return notepad("")
}

// editorDisplayName: the program's description (its version information:
// "Visual Studio Code"), else its file name without .exe.
func editorDisplayName(p string) string {
	name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	if d := exeDescription(p); d != "" {
		name = d
	}
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > 80 {
		out = string(r[:80])
	}
	return out
}

func exeDescription(p string) string {
	if err := procGetFileVersionInfoSizeW.Find(); err != nil {
		return ""
	}
	p16, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return ""
	}
	size, _, _ := procGetFileVersionInfoSizeW.Call(uintptr(unsafe.Pointer(p16)), 0)
	if size == 0 || size > 1<<20 {
		return ""
	}
	buf := make([]byte, size)
	if r, _, _ := procGetFileVersionInfoW.Call(uintptr(unsafe.Pointer(p16)), 0, size, uintptr(unsafe.Pointer(&buf[0]))); r == 0 {
		return ""
	}
	base := uintptr(unsafe.Pointer(&buf[0]))
	query := func(sub string) (int, int) {
		s16, err := syscall.UTF16PtrFromString(sub)
		if err != nil {
			return -1, 0
		}
		var ptr uintptr
		var n uint32
		if r, _, _ := procVerQueryValueW.Call(base, uintptr(unsafe.Pointer(s16)), uintptr(unsafe.Pointer(&ptr)), uintptr(unsafe.Pointer(&n))); r == 0 || ptr < base || ptr >= base+uintptr(len(buf)) {
			return -1, 0
		}
		return int(ptr - base), int(n)
	}
	off, n := query(`\VarFileInfo\Translation`)
	langs := []string{}
	if off >= 0 && n >= 4 && off+4 <= len(buf) {
		lang := uint16(buf[off]) | uint16(buf[off+1])<<8
		cp := uint16(buf[off+2]) | uint16(buf[off+3])<<8
		langs = append(langs, fmt.Sprintf("%04x%04x", lang, cp))
	}
	langs = append(langs, "040904b0", "040904e4", "000004b0")
	for _, lc := range langs {
		off, n := query(`\StringFileInfo\` + lc + `\FileDescription`)
		if off < 0 || n <= 1 || off+2*n > len(buf) {
			continue
		}
		u := make([]uint16, n)
		for i := range u {
			u[i] = uint16(buf[off+2*i]) | uint16(buf[off+2*i+1])<<8
		}
		if s := strings.TrimSpace(syscall.UTF16ToString(u)); s != "" {
			return s
		}
	}
	return ""
}

// OPENFILENAMEW (64-bit layout).
type openFileNameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	Flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	FlagsEx           uint32
}

// pickEditorDialog: Windows' own Open dialog, programs only (a shortcut is
// followed to its program), owned by the main window. UI thread.
func pickEditorDialog(owner uintptr, title, filterLabel string) (string, bool) {
	if err := procGetOpenFileNameW.Find(); err != nil {
		return "", false
	}
	var filter []uint16
	for _, s := range []string{filterLabel + " (*.exe)", "*.exe"} {
		u, err := syscall.UTF16FromString(strings.ReplaceAll(s, "\x00", ""))
		if err != nil {
			return "", false
		}
		filter = append(filter, u...)
	}
	filter = append(filter, 0)
	file := make([]uint16, 32768)
	t16, _ := syscall.UTF16PtrFromString(strings.ReplaceAll(title, "\x00", ""))
	var dir16 *uint16
	if pf, err := programFilesDir(); err == nil {
		dir16, _ = syscall.UTF16PtrFromString(pf)
	}
	def, _ := syscall.UTF16PtrFromString("exe")
	ofn := openFileNameW{hwndOwner: owner, lpstrFilter: &filter[0], nFilterIndex: 1, lpstrFile: &file[0], nMaxFile: uint32(len(file)),
		lpstrInitialDir: dir16, lpstrTitle: t16, lpstrDefExt: def,
		Flags: ofnFileMustExist | ofnPathMustExist | ofnHideReadOnly | ofnNoChangeDir | ofnExplorer | ofnDontAddRecent}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))
	if r, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return "", false
	}
	p := syscall.UTF16ToString(file)
	if p == "" {
		return "", false
	}
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}
