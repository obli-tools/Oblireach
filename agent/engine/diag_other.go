//go:build !windows

package engine

// setNativeDiskWrites: no native diagnostic file off Windows.
func setNativeDiskWrites(enabled bool) {}
