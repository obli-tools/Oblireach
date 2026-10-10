//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// helperExitCode / watchHelperExit on a real short-lived process.
func TestHelperExitCodeAndWatch(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 3")
	if err := cmd.Start(); err != nil {
		t.Skipf("cmd.exe: %v", err)
	}
	defer cmd.Wait() //nolint:errcheck
	h, err := syscall.OpenProcess(syscall.SYNCHRONIZE|0x1000 /* PROCESS_QUERY_LIMITED_INFORMATION */, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Skipf("OpenProcess: %v", err)
	}
	defer syscall.CloseHandle(h) //nolint:errcheck

	stop := make(chan struct{})
	exited, done := watchHelperExit(h, stop)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("watchHelperExit did not see the exit")
	}
	<-done
	code, ok := helperExitCode(h, time.Second)
	if !ok || code != 3 {
		t.Fatalf("helperExitCode = (%d, %v), want (3, true)", code, ok)
	}
	if _, ok := helperExitCode(0, 0); ok {
		t.Fatal("no handle: not exited")
	}

	// stop ends a watch on a process that keeps running.
	run := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	if err := run.Start(); err != nil {
		t.Skipf("cmd.exe: %v", err)
	}
	defer func() { _ = run.Process.Kill(); _ = run.Wait() }()
	h2, err := syscall.OpenProcess(syscall.SYNCHRONIZE|0x1000, false, uint32(run.Process.Pid))
	if err != nil {
		t.Skipf("OpenProcess: %v", err)
	}
	defer syscall.CloseHandle(h2) //nolint:errcheck
	if _, ok := helperExitCode(h2, 50*time.Millisecond); ok {
		t.Fatal("a running process is not exited")
	}
	stop2 := make(chan struct{})
	exited2, done2 := watchHelperExit(h2, stop2)
	close(stop2)
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not end with stop")
	}
	select {
	case <-exited2:
		t.Fatal("a stopped watch must not report an exit")
	default:
	}
}
