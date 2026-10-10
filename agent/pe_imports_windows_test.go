//go:build windows

package main

// Static imports of the agent executable. A DLL in the import table must
// exist for Windows to START the program at all: MFPlat.DLL (absent on
// Server editions without the Media Foundation feature and on N editions:
// the service did not start, MSI error 1920) and d3d11.dll / dxgi.dll are
// therefore loaded at run time (encode_windows.go, capture_windows.go,
// capture_mosaic_windows.go). For the nt6 build (Windows 7 SP1 / 2008 R2 and
// later), no function that only exists from Windows 8 may be imported either.
//
// The test binary links the same cgo code as the agent and is checked every
// run. A built exe is checked with:
//
//	set OBLIREACH_CHECK_EXE=%TEMP%\oblireach-agent-nt6.exe
//	C:\Go1.20\bin\go.exe test -modfile=go120.mod -run TestBuiltExeImports .
//
// (several paths separated by ";"; every one is checked with the nt6 rules).

import (
	"debug/pe"
	"os"
	"sort"
	"strings"
	"testing"
)

// peDLLsNeverStatic: never in the import table (loaded at run time).
var peDLLsNeverStatic = []string{"mfplat.dll", "mf.dll", "mfreadwrite.dll", "d3d11.dll", "dxgi.dll"}

// peNT6Forbidden: DLLs / functions that do not exist on Windows 7 SP1 /
// Server 2008 R2 (Windows 8 / 8.1 / 10 additions): a static import of any
// of them makes the loader refuse the exe there.
var peNT6Forbidden = map[string][]string{
	"*": {"shcore.dll", "dcomp.dll", "d3d12.dll", "bcryptprimitives.dll", "api-ms-win-core-synch-l1-2-0.dll",
		"api-ms-win-core-path-l1-1-0.dll", "api-ms-win-core-winrt-l1-1-0.dll"},
	"kernel32.dll": {"GetSystemTimePreciseAsFileTime", "CreateFile2", "GetCurrentThreadStackLimits",
		"SetThreadDescription", "GetThreadDescription", "PrefetchVirtualMemory", "SetProcessMitigationPolicy",
		"GetProcessMitigationPolicy", "WaitOnAddress", "WakeByAddressSingle", "WakeByAddressAll",
		"GetCurrentPackageId", "GetPackageFamilyName", "GetFirmwareType", "SetDefaultDllDirectories", "AddDllDirectory",
		"CreateFileMappingFromApp", "MapViewOfFileFromApp", "VirtualAllocFromApp"},
	"user32.dll": {"GetDpiForWindow", "GetDpiForSystem", "SetProcessDpiAwarenessContext",
		"SetThreadDpiAwarenessContext", "GetThreadDpiAwarenessContext", "EnableNonClientDpiScaling",
		"GetSystemMetricsForDpi", "AdjustWindowRectExForDpi", "GetPointerInfo", "GetPointerType",
		"InjectTouchInput", "InitializeTouchInjection", "SetDisplayAutoRotationPreferences",
		"GetDisplayAutoRotationPreferences", "IsImmersiveProcess", "RegisterPointerDeviceNotifications",
		"GetAwarenessFromDpiAwarenessContext", "SystemParametersInfoForDpi", "CreateSyntheticPointerDevice",
		"InjectSyntheticPointerInput"},
	"advapi32.dll": {"EventSetInformation"},
	"gdi32.dll":    {"D3DKMTOpenAdapterFromLuid"},
}

// peImportProblems returns the forbidden static imports of the PE file at
// path ("dll" or "dll:Function").
func peImportProblems(path string, nt6 bool) ([]string, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	syms, err := f.ImportedSymbols() // "Function:dll"
	if err != nil {
		return nil, err
	}
	libs := map[string]bool{}
	fns := map[string]bool{}
	for _, s := range syms {
		i := strings.LastIndex(s, ":")
		if i < 0 {
			continue
		}
		dll := strings.ToLower(s[i+1:])
		libs[dll] = true
		fns[dll+":"+s[:i]] = true
	}
	if more, err := f.ImportedLibraries(); err == nil {
		for _, l := range more {
			libs[strings.ToLower(l)] = true
		}
	}
	var out []string
	for _, d := range peDLLsNeverStatic {
		if libs[d] {
			out = append(out, d)
		}
	}
	if nt6 {
		for _, d := range peNT6Forbidden["*"] {
			if libs[d] {
				out = append(out, d)
			}
		}
		for dll, list := range peNT6Forbidden {
			for _, fn := range list {
				if fns[dll+":"+fn] {
					out = append(out, dll+":"+fn)
				}
			}
		}
		for l := range libs {
			if strings.HasPrefix(l, "api-ms-win-") || strings.HasPrefix(l, "ext-ms-win-") {
				out = append(out, l) // API sets: Windows 8+ forwarding, never needed here
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// The test binary carries the same cgo code (C preambles, #cgo LDFLAGS) as
// the agent: none of the run-time-loaded DLLs may be a static import.
func TestNoStaticMediaFoundationOrD3DImport(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	bad, err := peImportProblems(exe, false)
	if err != nil {
		t.Fatalf("%s: %v", exe, err)
	}
	if len(bad) > 0 {
		t.Fatalf("%s statically imports %v: the exe would not start where they are missing", exe, bad)
	}
}

// OBLIREACH_CHECK_EXE: the built agent(s) (nt6 rules: Windows 7 SP1).
func TestBuiltExeImports(t *testing.T) {
	list := strings.TrimSpace(os.Getenv("OBLIREACH_CHECK_EXE"))
	if list == "" {
		t.Skip("OBLIREACH_CHECK_EXE not set")
	}
	for _, p := range strings.Split(list, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		bad, err := peImportProblems(p, true)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(bad) > 0 {
			t.Errorf("%s: forbidden static imports for Windows 7 / 2008 R2: %v", p, bad)
			continue
		}
		t.Logf("%s: no MFPlat / d3d11 / dxgi / Windows 8+ static import", p)
	}
}
