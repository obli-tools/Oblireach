//go:build !windows

package main

// tryRunAsService always returns false on non-Windows platforms.
func tryRunAsService(runFn func()) bool { return false }

func setDLLSearchPath(dir string) {}
