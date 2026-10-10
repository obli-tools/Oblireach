//go:build windows

package engine

// Verified loading of the third-party codec DLLs shipped next to the
// executable (DLL planting: a portable exe typically runs from Downloads,
// where anyone who can drop a file could otherwise get code loaded).
//
// The C encoders call obliLoadCodecDLL with their candidate file names. For
// each name present in the executable's folder:
//   - the file is opened without FILE_SHARE_WRITE / FILE_SHARE_DELETE (nobody
//     can change, replace, rename or delete it while it is open; a writer
//     already holding it makes the open fail) and without following a
//     reparse point;
//   - its content is hashed and must match a pin (codec_pins.go);
//   - its imports are resolved first: a pinned sibling DLL goes through the
//     same checks (recursively), any other DLL is loaded from System32 only,
//     API sets are left to the loader;
//   - it is loaded with LoadLibraryExW(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR |
//     LOAD_LIBRARY_SEARCH_SYSTEM32), so its own dependencies are never
//     searched in the current directory or PATH (Windows 7 without
//     KB2533623: LOAD_WITH_ALTERED_SEARCH_PATH, every dependency being
//     already loaded at that point);
//   - the loaded module must be the very file that was hashed (same volume
//     and file index), else it is unloaded and refused;
//   - every handle stays open until LoadLibraryExW has returned.
// A refused DLL is logged and the encoder falls back (Media Foundation,
// JPEG). SetBundledDLLs(false) skips all of this: nothing is ever looked for
// in the executable's folder.
//
// This file exports a Go function to C, so its preamble holds declarations
// only (cgo rule).

/*
#include <stdint.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const (
	cdlLoadWithAlteredSearchPath = 0x00000008
	cdlLoadSearchDLLLoadDir      = 0x00000100
	cdlLoadSearchSystem32        = 0x00000800

	cdlFileFlagOpenReparsePoint = 0x00200000
	cdlFileReadAttributes       = 0x0080
	cdlMaxDLLSize               = 64 << 20

	cdlErrorInvalidParameter = syscall.Errno(87)
)

var (
	cdlKernel32            = syscall.NewLazyDLL("kernel32.dll") // KnownDLL
	cdlLoadLibraryExW      = cdlKernel32.NewProc("LoadLibraryExW")
	cdlGetModuleFileNameW  = cdlKernel32.NewProc("GetModuleFileNameW")
	cdlFreeLibrary         = cdlKernel32.NewProc("FreeLibrary")
	cdlGetSystemDirectoryW = cdlKernel32.NewProc("GetSystemDirectoryW")
)

// errDLLAbsent: the candidate file is not in the folder (try the next name).
var errDLLAbsent = errors.New("not present")

var (
	cdlMu      sync.Mutex
	cdlLoaded  = map[string]syscall.Handle{} // lower-case full path → verified module
	cdlRefused = map[string]error{}          // lower-case full path → why (logged once)
	cdlOffOnce sync.Once
)

//export obliLoadCodecDLL
func obliLoadCodecDLL(names *C.char) C.uintptr_t {
	if names == nil {
		return 0
	}
	return C.uintptr_t(loadBundledDLL(strings.Split(C.GoString(names), ";")))
}

// bundledDLLDir is the folder the codec DLLs are loaded from: the
// executable's (tests point it elsewhere).
var bundledDLLDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// loadBundledDLL loads the first candidate that is present and verified in
// the executable's folder; 0 when none.
func loadBundledDLL(names []string) syscall.Handle {
	if !BundledDLLsEnabled() {
		cdlOffOnce.Do(func() {
			log.Printf("engine: bundled codec DLLs disabled by the host (system encoders only)")
		})
		return 0
	}
	dir, err := bundledDLLDir()
	if err != nil {
		log.Printf("engine: codec DLLs: executable path unknown: %v", err)
		return 0
	}
	return loadBundledDLLFrom(dir, names)
}

// loadBundledDLLFrom: loadBundledDLL for the folder dir.
func loadBundledDLLFrom(dir string, names []string) syscall.Handle {
	cdlMu.Lock()
	defer cdlMu.Unlock()
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		h, err := loadVerifiedDLL(dir, n)
		if err == nil {
			return h
		}
		if !errors.Is(err, errDLLAbsent) {
			key := cdlKey(dir, n)
			if _, logged := cdlRefused[key]; !logged {
				cdlRefused[key] = err
				log.Printf("engine: codec DLL %s refused: %v", filepath.Join(dir, n), err)
			}
		}
	}
	return 0
}

// loadVerifiedDLL: one candidate with its dependencies (cdlMu held). The
// locks taken while resolving are released once the load has returned.
func loadVerifiedDLL(dir, name string) (syscall.Handle, error) {
	if h, ok := cdlLoaded[cdlKey(dir, name)]; ok {
		return h, nil
	}
	if err, ok := cdlRefused[cdlKey(dir, name)]; ok {
		return 0, err
	}
	var locks []*os.File
	defer func() {
		for _, f := range locks {
			f.Close()
		}
	}()
	return loadVerifiedLocked(dir, name, &locks, 0)
}

func loadVerifiedLocked(dir, name string, locks *[]*os.File, depth int) (syscall.Handle, error) {
	if h, ok := cdlLoaded[cdlKey(dir, name)]; ok {
		return h, nil
	}
	if depth > 8 {
		return 0, errors.New("dependency chain too deep")
	}
	path := filepath.Join(dir, name)
	f, err := cdlOpenLocked(path)
	if err == nil {
		if _, pinned := codecDLLPins[name]; !pinned {
			f.Close()
			return 0, errors.New("not a known codec DLL (no pin)")
		}
	}
	if err != nil {
		return 0, err
	}
	*locks = append(*locks, f)

	data, err := io.ReadAll(io.LimitReader(f, cdlMaxDLLSize+1))
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	if len(data) > cdlMaxDLLSize {
		return 0, errors.New("file too large")
	}
	if ok, sum := codecDLLPinned(name, data); !ok {
		return 0, fmt.Errorf("content does not match the pinned build (sha256 %s)", sum)
	}

	if err := peMachineMatches(data, runtime.GOARCH); err != nil {
		return 0, err
	}
	imports, err := peImportDLLs(data)
	if err != nil {
		return 0, err
	}
	for _, imp := range imports {
		switch {
		case isAPISetName(imp):
		case codecDLLPins[imp] != nil:
			if _, err := loadVerifiedLocked(dir, imp, locks, depth+1); err != nil {
				return 0, fmt.Errorf("dependency %s: %w", imp, err)
			}
		default:
			if err := cdlPreloadSystemDLL(imp); err != nil {
				return 0, fmt.Errorf("dependency %s is not a System32 DLL: %w", imp, err)
			}
		}
	}

	h, err := cdlLoadLibraryEx(path, cdlLoadSearchDLLLoadDir|cdlLoadSearchSystem32)
	if errors.Is(err, cdlErrorInvalidParameter) { // Windows 7 / 2008 R2 without KB2533623
		h, err = cdlLoadLibraryEx(path, cdlLoadWithAlteredSearchPath)
	}
	if err != nil {
		return 0, fmt.Errorf("LoadLibraryExW: %w", err)
	}
	if err := cdlSameFile(h, f); err != nil {
		cdlFreeLibrary.Call(uintptr(h))
		return 0, err
	}
	cdlLoaded[cdlKey(dir, name)] = h
	return h, nil
}

// cdlKey: the cache key of a DLL file.
func cdlKey(dir, name string) string { return strings.ToLower(filepath.Join(dir, name)) }

// cdlOpenLocked opens path for reading, sharing read access only (no write,
// no delete / rename while open), without following a reparse point.
func cdlOpenLocked(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL|cdlFileFlagOpenReparsePoint, 0)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
			return nil, errDLLAbsent
		}
		return nil, fmt.Errorf("open: %w", err)
	}
	var fi syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &fi); err != nil {
		syscall.CloseHandle(h)
		return nil, fmt.Errorf("file information: %w", err)
	}
	if fi.FileAttributes&(syscall.FILE_ATTRIBUTE_REPARSE_POINT|syscall.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		syscall.CloseHandle(h)
		return nil, errors.New("not a regular file (reparse point or directory)")
	}
	return os.NewFile(uintptr(h), path), nil
}

// cdlSameFile checks that module h was mapped from the file open in f.
func cdlSameFile(h syscall.Handle, f *os.File) error {
	buf := make([]uint16, 32768)
	n, _, e := cdlGetModuleFileNameW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		return fmt.Errorf("GetModuleFileNameW: %v", e)
	}
	mp := &buf[0]
	mh, err := syscall.CreateFile(mp, cdlFileReadAttributes,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("open loaded module: %w", err)
	}
	defer syscall.CloseHandle(mh)
	var a, b syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(mh, &a); err != nil {
		return err
	}
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &b); err != nil {
		return err
	}
	if a.VolumeSerialNumber != b.VolumeSerialNumber || a.FileIndexHigh != b.FileIndexHigh || a.FileIndexLow != b.FileIndexLow {
		return fmt.Errorf("the loaded module (%s) is not the verified file", syscall.UTF16ToString(buf[:n]))
	}
	return nil
}

// cdlLoadLibraryEx wraps LoadLibraryExW.
func cdlLoadLibraryEx(name string, flags uintptr) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	if err := cdlLoadLibraryExW.Find(); err != nil {
		return 0, err
	}
	r, _, e := cdlLoadLibraryExW.Call(uintptr(unsafe.Pointer(p)), 0, flags)
	if r == 0 {
		if errno, ok := e.(syscall.Errno); ok && errno != 0 {
			return 0, errno
		}
		return 0, syscall.EINVAL
	}
	return syscall.Handle(r), nil
}

// cdlPreloadSystemDLL loads a DLL from System32 only (kept loaded), so that a
// codec DLL importing it resolves to that module and never to a file of the
// same name in the executable's folder.
func cdlPreloadSystemDLL(name string) error {
	if strings.ContainsAny(name, `\/:`) {
		return errors.New("not a plain DLL name")
	}
	_, err := loadSystemDLL(name)
	return err
}

// loadSystemDLL loads name from System32 only, its dependencies too:
// LOAD_LIBRARY_SEARCH_SYSTEM32, or on Windows 7 / 2008 R2 without KB2533623
// the full System32 path with LOAD_WITH_ALTERED_SEARCH_PATH.
func loadSystemDLL(name string) (syscall.Handle, error) {
	h, err := cdlLoadLibraryEx(name, cdlLoadSearchSystem32)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, cdlErrorInvalidParameter) {
		return 0, err
	}
	sys := systemDirectory()
	if sys == "" {
		return 0, errors.New("System32 path unknown")
	}
	return cdlLoadLibraryEx(sys+`\`+name, cdlLoadWithAlteredSearchPath)
}

var (
	sysDirOnce sync.Once
	sysDir     string
)

// systemDirectory returns the System32 path ("" if Windows does not say).
func systemDirectory() string {
	sysDirOnce.Do(func() {
		buf := make([]uint16, 300)
		n, _, _ := cdlGetSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 && int(n) < len(buf) {
			sysDir = syscall.UTF16ToString(buf[:n])
		}
	})
	return sysDir
}

// newSystemLazyDLL is syscall.NewLazyDLL for a Windows DLL that must come
// from System32 only, its dependencies too: the module is loaded now with
// loadSystemDLL and the LazyDLL names it by its full System32 path (which
// then resolves to that loaded module). A bare-name syscall.NewLazyDLL would
// search the executable's folder first (DLL planting). If the DLL does not
// exist on this Windows, procedures fail at Find / Call as before.
func newSystemLazyDLL(name string) *syscall.LazyDLL {
	_, _ = loadSystemDLL(name)
	if sys := systemDirectory(); sys != "" {
		return syscall.NewLazyDLL(sys + `\` + name)
	}
	return syscall.NewLazyDLL(name)
}
