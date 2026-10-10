//go:build windows

package engine

import (
	"sync"
	"unsafe"
)

var (
	osvNtdll             = newSystemLazyDLL("ntdll.dll") // KnownDLL
	osvProcRtlGetVersion = osvNtdll.NewProc("RtlGetVersion")

	osvOnce sync.Once
	osvVer  WinVersion
)

type osvVersionInfoEx struct {
	Size        uint32
	Major       uint32
	Minor       uint32
	Build       uint32
	Platform    uint32
	CSDVersion  [128]uint16
	SPMajor     uint16
	SPMinor     uint16
	SuiteMask   uint16
	ProductType byte
	Reserved    byte
}

// WindowsVersion: the real version of this Windows (read once). A hook so
// tests can play another Windows.
var WindowsVersion = platformWindowsVersion

func platformWindowsVersion() WinVersion {
	osvOnce.Do(func() {
		if osvProcRtlGetVersion.Find() != nil {
			return
		}
		var vi osvVersionInfoEx
		vi.Size = uint32(unsafe.Sizeof(vi))
		if r, _, _ := osvProcRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi))); r != 0 {
			return
		}
		const verNTWorkstation = 1
		osvVer = WinVersion{Major: vi.Major, Minor: vi.Minor, Build: vi.Build, SPMajor: vi.SPMajor,
			Server: vi.ProductType != 0 && vi.ProductType != verNTWorkstation}
	})
	return osvVer
}
