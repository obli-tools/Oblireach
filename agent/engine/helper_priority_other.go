//go:build !windows

package engine

// No process-priority bump off Windows (the capture helper is Windows-only).
func raiseHelperPriority() func() { return func() {} }
