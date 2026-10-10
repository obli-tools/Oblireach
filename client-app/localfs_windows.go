//go:build windows

package main

// localfs_windows.go — the Windows side of localfs.go: the disk and the shell
// (winSys, the platform's localSys) and the main window's local file system
// (newLocalFS: this Windows' protected locations). The bindings are
// localfs_bridge.go.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	modShlwapi  = syscall.NewLazyDLL("shlwapi.dll")
	modComdlg32 = syscall.NewLazyDLL("comdlg32.dll")
	modVersion  = syscall.NewLazyDLL("version.dll")

	procGetLogicalDrives          = modKernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW             = modKernel32.NewProc("GetDriveTypeW")
	procGetVolumeInformationW     = modKernel32.NewProc("GetVolumeInformationW")
	procMoveFileExW               = modKernel32.NewProc("MoveFileExW")
	procGetFileAttributesW        = modKernel32.NewProc("GetFileAttributesW")
	procSetFileAttributesW        = modKernel32.NewProc("SetFileAttributesW")
	procGetFinalPathNameByHandleW = modKernel32.NewProc("GetFinalPathNameByHandleW")
	procSHFileOperationW          = modShell32.NewProc("SHFileOperationW")
	procILCreateFromPathW         = modShell32.NewProc("ILCreateFromPathW")
	procILFree                    = modShell32.NewProc("ILFree")
	procSHOpenFolderAndSelect     = modShell32.NewProc("SHOpenFolderAndSelectItems")
	procAssocIsDangerous          = modShlwapi.NewProc("AssocIsDangerous")
	procAssocQueryStringW         = modShlwapi.NewProc("AssocQueryStringW")
	procGetOpenFileNameW          = modComdlg32.NewProc("GetOpenFileNameW")
	procGetFileVersionInfoSizeW   = modVersion.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW       = modVersion.NewProc("GetFileVersionInfoW")
	procVerQueryValueW            = modVersion.NewProc("VerQueryValueW")
)

const (
	fileAttrReadOnly   = 0x1
	fileAttrHidden     = 0x2
	fileAttrSystem     = 0x4
	fileAttrDirectory  = 0x10
	fileAttrReparse    = 0x400
	reparseSurrogate   = 0x20000000 // a reparse tag naming another file: a link
	moveReplace        = 0x1
	moveWriteThrough   = 0x8
	foDelete           = 0x3
	fofSilent          = 0x4
	fofNoConfirmation  = 0x10
	fofAllowUndo       = 0x40
	fofNoErrorUI       = 0x400
	fofWantNukeWarning = 0x4000
	openExisting       = 3
	fileFlagBackup     = 0x02000000
	fileReadAttrs      = 0x80
	driveRemovable     = 2
	driveRemote        = 4
	driveCDROM         = 5
	driveRAMDisk       = 6
)

// Known folders (SHGetKnownFolderPath).
var (
	folderIDDesktop   = syscall.GUID{Data1: 0xB4BFCC3A, Data2: 0xDB2C, Data3: 0x424C, Data4: [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}}
	folderIDDocuments = syscall.GUID{Data1: 0xFDD39AD0, Data2: 0x238F, Data3: 0x46AF, Data4: [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7}}
	folderIDDownloads = syscall.GUID{Data1: 0x374DE290, Data2: 0x123F, Data3: 0x4565, Data4: [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}
	folderIDStartup   = syscall.GUID{Data1: 0xB97D20BB, Data2: 0xF46A, Data3: 0x4C97, Data4: [8]byte{0xBA, 0x10, 0x5E, 0x36, 0x08, 0x43, 0x08, 0x54}}
	folderIDComStart  = syscall.GUID{Data1: 0x82A5EA35, Data2: 0xD9CD, Data3: 0x47C5, Data4: [8]byte{0x96, 0x29, 0xE1, 0x5D, 0x2F, 0x71, 0x4E, 0x6E}}
	folderIDProgX86   = syscall.GUID{Data1: 0x7C5A40EF, Data2: 0xA0FB, Data3: 0x4BFC, Data4: [8]byte{0x87, 0x4A, 0xC0, 0xF2, 0xE0, 0xB9, 0xFA, 0x8E}}
	folderIDWindows   = syscall.GUID{Data1: 0xF38BF404, Data2: 0x1D43, Data3: 0x42F2, Data4: [8]byte{0x93, 0x05, 0x67, 0xDE, 0x0B, 0x28, 0xFC, 0x23}}
)

func knownFolder(id *syscall.GUID) string {
	var p *uint16
	if hr, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(id)), 0, 0, uintptr(unsafe.Pointer(&p))); int32(hr) < 0 || p == nil {
		if p != nil {
			procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
		}
		return ""
	}
	s := utf16PtrString(p, 32768)
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}

// longPath: the \\?\ form of a long drive path (the raw calls below do not
// get the os package's own fix).
func longPath(p string) string {
	if len(p) >= 240 && !strings.HasPrefix(p, `\\`) {
		return `\\?\` + p
	}
	return p
}

// ── winSys ─────────────────────────────────────────────────────────────────

type winSys struct{}

func filetimeToTime(ft syscall.Filetime) time.Time {
	if ft.HighDateTime == 0 && ft.LowDateTime == 0 {
		return time.Time{}
	}
	return time.Unix(0, ft.Nanoseconds())
}

func entryOf(d *syscall.Win32finddata) localEntry {
	a := d.FileAttributes
	e := localEntry{
		Name:     syscall.UTF16ToString(d.FileName[:]),
		Size:     int64(d.FileSizeHigh)<<32 | int64(d.FileSizeLow),
		Modified: filetimeToTime(d.LastWriteTime),
		Hidden:   a&fileAttrHidden != 0,
		System:   a&fileAttrSystem != 0,
		ReadOnly: a&fileAttrReadOnly != 0,
	}
	// A symbolic link, a junction, a mount point, a WSL link: a name
	// surrogate. A OneDrive placeholder, a deduplicated file: data, kept.
	e.Link = a&fileAttrReparse != 0 && d.Reserved0&reparseSurrogate != 0
	e.IsDir = a&fileAttrDirectory != 0
	return e
}

func (winSys) readDir(dir string) ([]localEntry, error) {
	pat := longPath(filepath.Join(dir, "*"))
	p16, err := syscall.UTF16PtrFromString(pat)
	if err != nil {
		return nil, fsE("invalid_path")
	}
	var d syscall.Win32finddata
	h, err := syscall.FindFirstFile(p16, &d)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return []localEntry{}, nil // (an empty drive root)
		}
		return nil, &os.PathError{Op: "list", Path: dir, Err: err}
	}
	defer syscall.FindClose(h)
	var out []localEntry
	for {
		e := entryOf(&d)
		if e.Name != "." && e.Name != ".." && e.Name != "" {
			out = append(out, e)
		}
		if err := syscall.FindNextFile(h, &d); err != nil {
			break
		}
	}
	return sortedEntries(out), nil
}

func (winSys) lstat(path string) (localEntry, error) {
	if isDriveRoot(path) {
		st, err := os.Stat(path)
		if err != nil {
			return localEntry{}, err
		}
		return localEntry{Name: path, IsDir: st.IsDir(), Modified: st.ModTime()}, nil
	}
	p16, err := syscall.UTF16PtrFromString(longPath(path))
	if err != nil {
		return localEntry{}, fsE("invalid_path")
	}
	var d syscall.Win32finddata
	h, err := syscall.FindFirstFile(p16, &d)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
			return localEntry{}, &os.PathError{Op: "lstat", Path: path, Err: syscall.ERROR_FILE_NOT_FOUND}
		}
		return localEntry{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	syscall.FindClose(h)
	return entryOf(&d), nil
}

// finalPath: the path once every reparse point on its way is followed
// (GetFinalPathNameByHandle on the element itself).
func (winSys) finalPath(path string) (string, error) {
	p16, err := syscall.UTF16PtrFromString(longPath(path))
	if err != nil {
		return "", err
	}
	h, err := syscall.CreateFile(p16, fileReadAttrs, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, openExisting, fileFlagBackup, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 1024)
	for {
		n, _, e := procGetFinalPathNameByHandleW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			return "", e
		}
		if int(n) < len(buf) {
			s := syscall.UTF16ToString(buf[:n])
			switch {
			case strings.HasPrefix(s, `\\?\UNC\`):
				s = `\\` + s[len(`\\?\UNC\`):]
			case strings.HasPrefix(s, `\\?\`):
				s = s[len(`\\?\`):]
			}
			return s, nil
		}
		if n > 32768 {
			return "", fsE("invalid_path")
		}
		buf = make([]uint16, n+1)
	}
}

func driveTypeOf(root string) uint32 {
	p16, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return 0
	}
	r, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p16)))
	return uint32(r)
}

func (winSys) driveType(path string) uint32 {
	if strings.HasPrefix(path, `\\`) {
		return driveRemote
	}
	if len(path) < 2 || path[1] != ':' {
		return 0
	}
	return driveTypeOf(path[:2] + `\`)
}

func volumeLabel(root string) string {
	p16, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 261)
	r, _, _ := procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(p16)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0, 0, 0, 0)
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func driveKind(t uint32) string {
	switch t {
	case localDriveFixed:
		return "fixed"
	case driveRemovable:
		return "removable"
	case driveRemote:
		return "network"
	case driveCDROM:
		return "cdrom"
	case driveRAMDisk:
		return "ram"
	}
	return "unknown"
}

func (winSys) roots() localRoots {
	var r localRoots
	r.Computer, _ = os.Hostname()
	r.Home, _ = os.UserHomeDir()
	r.Places = []localPlace{}
	if r.Home != "" {
		r.Places = append(r.Places, localPlace{ID: "home", Path: r.Home})
	}
	for _, kf := range []struct {
		id string
		g  *syscall.GUID
	}{{"desktop", &folderIDDesktop}, {"documents", &folderIDDocuments}, {"downloads", &folderIDDownloads}} {
		if p := knownFolder(kf.g); p != "" {
			r.Places = append(r.Places, localPlace{ID: kf.id, Path: p})
		}
	}
	mask, _, _ := procGetLogicalDrives.Call()
	type lab struct {
		i int
		s string
	}
	r.Drives = []localDrive{}
	ch := make(chan lab, 26)
	n := 0
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		t := driveTypeOf(root)
		if t == 1 { // DRIVE_NO_ROOT_DIR
			continue
		}
		r.Drives = append(r.Drives, localDrive{Path: root, Kind: driveKind(t)})
		idx := len(r.Drives) - 1
		n++
		go func(root string, idx int) { ch <- lab{idx, volumeLabel(root)} }(root, idx)
	}
	// Each label within 3 s (a disconnected network drive can hang): none after.
	deadline := time.After(3 * time.Second)
	for got := 0; got < n; got++ {
		select {
		case l := <-ch:
			r.Drives[l.i].Label = l.s
		case <-deadline:
			got = n
		}
	}
	return r
}

func setFileAttrs(path string, add uint32) {
	p16, err := syscall.UTF16PtrFromString(longPath(path))
	if err != nil {
		return
	}
	a, _, _ := procGetFileAttributesW.Call(uintptr(unsafe.Pointer(p16)))
	if uint32(a) == 0xFFFFFFFF {
		return
	}
	procSetFileAttributesW.Call(uintptr(unsafe.Pointer(p16)), uintptr(uint32(a)|add))
}

func (winSys) setHidden(path string) { setFileAttrs(path, fileAttrHidden) }

// markOfTheWeb: the Internet zone, as a browser marks a download: Windows
// then warns before running it, Office opens it protected.
func (winSys) markOfTheWeb(path string) error {
	return os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
}

func moveFile(src, dst string, flags uintptr) error {
	s16, err := syscall.UTF16PtrFromString(longPath(src))
	if err != nil {
		return err
	}
	d16, err := syscall.UTF16PtrFromString(longPath(dst))
	if err != nil {
		return err
	}
	if r, _, e := procMoveFileExW.Call(uintptr(unsafe.Pointer(s16)), uintptr(unsafe.Pointer(d16)), flags); r == 0 {
		return &os.LinkError{Op: "move", Old: src, New: dst, Err: e}
	}
	return nil
}

func (winSys) moveNoReplace(src, dst string) error { return moveFile(src, dst, moveWriteThrough) }
func (winSys) moveReplace(src, dst string) error {
	return moveFile(src, dst, moveReplace|moveWriteThrough)
}

// SHFILEOPSTRUCTW (64-bit layout: the app is built for amd64).
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

// onComThread runs f on a locked OS thread with COM (the shell's calls).
func onComThread(f func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := procCoInitializeEx.Call(0, coinitApartment|coinitDisableOle1DDE)
		if int32(hr) >= 0 {
			defer procCoUninitialize.Call()
		}
		done <- f()
	}()
	return <-done
}

// recycleFlags: to the Recycle Bin (FOF_ALLOWUNDO), without the shell's own
// "move to the Recycle Bin?" (the local pane asked), its progress or its
// error dialogs. An item the Recycle Bin cannot take (larger than the bin, a
// drive whose bin deletes at once, a path too long for it) would then be
// deleted for good without a word (FOF_NOCONFIRMATION): FOF_WANTNUKEWARNING
// makes Windows ask before that, in its own dialog owned by the main window;
// declined, nothing more is deleted (cancelled). Never a silent permanent
// delete.
const recycleFlags = fofAllowUndo | fofNoConfirmation | fofWantNukeWarning | fofSilent | fofNoErrorUI

// (recycleOwner, the main window, the owner of that dialog: localfs_bridge.go.)

// recycle: to the Recycle Bin (recycleFlags).
func (winSys) recycle(paths []string) error {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return fsE("no_recycle")
	}
	var from []uint16
	for _, p := range paths {
		u, err := syscall.UTF16FromString(p)
		if err != nil {
			return fsE("invalid_path")
		}
		from = append(from, u...) // (its NUL included)
	}
	from = append(from, 0)
	return onComThread(func() error {
		op := shFileOpStruct{hwnd: recycleOwner.Load(), wFunc: foDelete, pFrom: &from[0], fFlags: recycleFlags}
		r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
		if r != 0 {
			return fsEx("io", map[string]interface{}{"shell": int(r)})
		}
		if op.fAnyOperationsAborted != 0 {
			return fsE("cancelled")
		}
		return nil
	})
}

// reveal: Explorer on the element's folder, the element selected.
func (winSys) reveal(path string) error {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fsE("invalid_path")
	}
	go onComThread(func() error {
		pidl, _, _ := procILCreateFromPathW.Call(uintptr(unsafe.Pointer(p16)))
		if pidl == 0 {
			return fsE("not_found")
		}
		defer procILFree.Call(pidl)
		procSHOpenFolderAndSelect.Call(pidl, 0, 0, 0)
		return nil
	})
	return nil
}

// ── The main window's local file system ────────────────────────────────────

// newLocalFS: the main window's (main.go): the protected locations of this
// Windows and this user, the working copies' root, the settings kept in
// config.json (Proxy.mu), the working copies older than a day swept.
func newLocalFS(p *Proxy) *localFS {
	local := os.Getenv("LOCALAPPDATA")
	home, _ := os.UserHomeDir()
	if local == "" && home != "" {
		local = filepath.Join(home, "AppData", "Local")
	}
	editRoot := ""
	if local != "" {
		editRoot = filepath.Join(local, "OblireachClient", "edit")
	}
	l := newLocalFSWith(winSys{}, editRoot)
	add := func(list *[]string, ps ...string) {
		for _, x := range ps {
			if x != "" && filepath.IsAbs(x) {
				*list = append(*list, filepath.Clean(x))
			}
		}
	}
	win := knownFolder(&folderIDWindows)
	if win == "" {
		win = os.Getenv("SystemRoot")
	}
	pf, _ := programFilesDir()
	appData := os.Getenv("APPDATA")
	programData := os.Getenv("ProgramData")
	exe, _ := os.Executable()
	docs := knownFolder(&folderIDDocuments)
	add(&l.protectW, win, pf, knownFolder(&folderIDProgX86), os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"),
		knownFolder(&folderIDStartup), knownFolder(&folderIDComStart), configDir)
	if programData != "" {
		add(&l.protectW, filepath.Join(programData, "Microsoft", "Windows", "Start Menu"))
	}
	if appData != "" {
		add(&l.protectW, filepath.Join(appData, "Microsoft", "Windows", "Start Menu"))
	}
	if local != "" {
		add(&l.protectW, filepath.Join(local, "Microsoft", "WindowsApps"))
	}
	if docs != "" { // the PowerShell profiles run at every start of PowerShell
		add(&l.protectW, filepath.Join(docs, "WindowsPowerShell"), filepath.Join(docs, "PowerShell"))
	}
	if exe != "" {
		add(&l.protectW, filepath.Dir(exe))
	}
	// The app's own data beyond config.json's folder: its folder of
	// %LOCALAPPDATA% (the working copies among it, managed by the edit
	// bindings only) and the WebView2 profile of this page (go-webview2's
	// default: %APPDATA%\<the exe's name>: its cookies, its storage, its
	// cache), neither written nor read from the local pane. So are Office's
	// folders it loads at every start (templates and add-ins run there).
	ownLocal, webProfile := "", ""
	if local != "" {
		ownLocal = filepath.Join(local, "OblireachClient")
	}
	if appData != "" && exe != "" {
		webProfile = filepath.Join(appData, filepath.Base(exe))
	}
	add(&l.protectW, ownLocal, webProfile)
	if appData != "" {
		add(&l.protectW, filepath.Join(appData, "Microsoft", "Word", "STARTUP"), filepath.Join(appData, "Microsoft", "Excel", "XLSTART"),
			filepath.Join(appData, "Microsoft", "AddIns"))
	}
	add(&l.protectR, configDir, ownLocal, webProfile)
	add(&l.keepRoots, home, knownFolder(&folderIDDesktop), docs, knownFolder(&folderIDDownloads), appData, local, programData,
		os.Getenv("PUBLIC"), filepath.Dir(home))
	if p != nil {
		l.settings = p.filesSettings
		l.setSettings = p.setFilesSettings
		l.edits.origin = p.profileOrigin // (a working copy tied to its server's address)
	}
	if editRoot != "" {
		go sweepEditRoot(editRoot, time.Now(), editSweepAge)
	}
	return l
}

// (attachLocalFS and the file bindings: localfs_bridge.go.)
