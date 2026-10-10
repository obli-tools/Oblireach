//go:build !linux

package main

import "os/exec"

// rdpHelperProcAttr: nothing more off Linux (the helper path is used on
// Linux only today; Windows embeds mstsc.exe: rdp_session.go).
func rdpHelperProcAttr(cmd *exec.Cmd) {}
