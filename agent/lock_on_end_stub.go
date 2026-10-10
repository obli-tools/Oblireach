//go:build !windows

package main

// runLockWorkstation: --lock-workstation is a Windows mode (lock_on_end_windows.go);
// the feature is not advertised elsewhere.
func runLockWorkstation() int { return 1 }
