//go:build !windows

package engine

// WindowsVersion: not Windows (zero = unknown: modern behaviour everywhere).
var WindowsVersion = func() WinVersion { return WinVersion{} }
