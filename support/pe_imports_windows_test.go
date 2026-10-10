//go:build windows

package main

// Static imports of the support executables. A DLL in the import table must
// exist for Windows to START the program at all: MFPlat.DLL (absent on
// Server editions without the Media Foundation feature and on N editions)
// and d3d11.dll / dxgi.dll are loaded at run time by the engine. For the nt6
// build (OblireachSupport-nt6.exe, Windows 7 SP1 / 2008 R2 and later), no
// function that only exists from Windows 8 may be imported either: the
// loader would refuse the exe there. Same rules as the agent's
// pe_imports_windows_test.go (keep the lists in step).
//
// The test binary links the same cgo code as the client and is checked
// every run (with the Windows 7 rules when it is the nt6 line). The built executables in dist\ are checked when present
// (OblireachSupport-nt6.exe with the nt6 rules), or the ones listed in
// OBLIREACH_CHECK_EXE (";"-separated, nt6 rules):
//
//	C:\Go1.20\bin\go.exe test -tags nt6 -modfile=go120.mod -overlay=nt6overlay.json -run TestBuiltExeImports .

import (
	"debug/pe"
	"os"
	"path/filepath"
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

func TestNoStaticMediaFoundationOrD3DImport(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	bad, err := peImportProblems(exe, testNT6)
	if err != nil {
		t.Fatalf("%s: %v", exe, err)
	}
	if len(bad) > 0 {
		t.Fatalf("%s statically imports %v: the exe would not start where they are missing", exe, bad)
	}
}

func TestBuiltExeImports(t *testing.T) {
	type target struct {
		path string
		nt6  bool
	}
	var list []target
	if env := strings.TrimSpace(os.Getenv("OBLIREACH_CHECK_EXE")); env != "" {
		for _, p := range strings.Split(env, ";") {
			if p = strings.TrimSpace(p); p != "" {
				list = append(list, target{p, true})
			}
		}
	} else {
		for _, c := range []target{
			{filepath.Join("dist", "OblireachSupport.exe"), false},
			{filepath.Join("dist", "OblireachSupport-nt6.exe"), true},
		} {
			if _, err := os.Stat(c.path); err == nil {
				list = append(list, c)
			}
		}
	}
	if len(list) == 0 {
		t.Skip(`no built executable in dist\ and OBLIREACH_CHECK_EXE not set`)
	}
	for _, c := range list {
		bad, err := peImportProblems(c.path, c.nt6)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if len(bad) > 0 {
			t.Errorf("%s: forbidden static imports (nt6 rules %v): %v", c.path, c.nt6, bad)
			continue
		}
		t.Logf("%s: no MFPlat / d3d11 / dxgi static import (Windows 7 rules: %v)", c.path, c.nt6)
	}
}
