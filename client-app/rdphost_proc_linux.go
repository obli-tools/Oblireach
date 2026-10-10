//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// rdpHelperProcAttr: the helper in its own process group, killed with the
// app (Pdeathsig): never left connected behind a crashed Desktop.
func rdpHelperProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
