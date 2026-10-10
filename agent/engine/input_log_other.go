//go:build !windows

package engine

// inputLogFiles is empty off Windows: the input diagnostic log only exists on
// Windows (the C sink in input_windows.go), so there is nothing to purge.
func inputLogFiles() []string { return nil }
