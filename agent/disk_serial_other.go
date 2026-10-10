//go:build !windows && !linux && !darwin

package main

// readSystemDiskSerial for device identity v2 (device_identity.go): not
// implemented on this platform (the Obliance agent returns "" on FreeBSD too).
// An empty value never counts as a fingerprint change.
func readSystemDiskSerial() string { return "" }
