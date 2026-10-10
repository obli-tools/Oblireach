//go:build windows

package main

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

func testLoopbackListener(t *testing.T) *net.TCPListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln.(*net.TCPListener)
}

// Local connections that reach the helper port first (a silent
// one, one with a wrong hello, one with garbage) are dropped, and the real
// helper is still accepted at once — they neither receive anything nor delay
// or fail the leg.
func TestAcceptHelperIgnoresStrangers(t *testing.T) {
	ln := testLoopbackListener(t)
	defer ln.Close()
	addr := ln.Addr().String()
	nonce := newLegNonce()

	silent, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	wrong, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	_ = pipeSend(wrong, engine.PipeTypeHello, []byte(newLegNonce()))
	garbage, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer garbage.Close()
	_, _ = garbage.Write([]byte("GET / HTTP/1.1\r\n\r\n"))

	helper, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer helper.Close()
	_ = pipeSend(helper, engine.PipeTypeHello, []byte(nonce))

	start := time.Now()
	conn, err := acceptHelper(ln, nonce, time.Now().Add(10*time.Second), make(chan struct{}))
	if err != nil {
		t.Fatalf("the real helper must be accepted: %v", err)
	}
	defer conn.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("strangers delayed the helper: %v", d)
	}
	// The accepted connection is the helper's: its next frame arrives.
	_ = pipeSend(helper, engine.PipeTypeInit, []byte(`{"type":"init"}`))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, p, err := pipeRecv(conn)
	if err != nil || mt != engine.PipeTypeInit || string(p) != `{"type":"init"}` {
		t.Fatalf("expected the helper's init, got type=%d %q err=%v", mt, p, err)
	}
	// The stranger with the wrong hello was closed without being answered.
	_ = wrong.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := wrong.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Fatalf("a stranger must be dropped, read n=%d err=%v", n, err)
	}
}

// Only strangers until the deadline: a timeout, never a stranger's connection.
func TestAcceptHelperTimeout(t *testing.T) {
	ln := testLoopbackListener(t)
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = pipeSend(c, engine.PipeTypeHello, []byte(newLegNonce()))
	conn, err := acceptHelper(ln, newLegNonce(), time.Now().Add(700*time.Millisecond), make(chan struct{}))
	if conn != nil || !errors.Is(err, errAcceptTimeout) {
		t.Fatalf("expected a timeout, got conn=%v err=%v", conn, err)
	}
}

// The stream stops while waiting for the helper: aborted at once.
func TestAcceptHelperAbort(t *testing.T) {
	ln := testLoopbackListener(t)
	defer ln.Close()
	abort := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(abort)
	}()
	start := time.Now()
	conn, err := acceptHelper(ln, newLegNonce(), time.Now().Add(10*time.Second), abort)
	if conn != nil || !errors.Is(err, errAcceptAborted) {
		t.Fatalf("expected an abort, got conn=%v err=%v", conn, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the abort must not wait for the deadline")
	}
}

// Helpers are waited on and killed through the process handle kept
// from process creation (a recycled PID can never be hit).
func TestReapHelperByHandle(t *testing.T) {
	spawn := func(cmd string) syscall.Handle {
		t.Helper()
		var si syscall.StartupInfo
		si.Cb = uint32(unsafe.Sizeof(si))
		var pi syscall.ProcessInformation
		line, _ := syscall.UTF16PtrFromString(cmd)
		if err := syscall.CreateProcess(nil, line, nil, nil, false, 0x08000000 /* CREATE_NO_WINDOW */, nil, nil, &si, &pi); err != nil {
			t.Fatalf("CreateProcess(%s): %v", cmd, err)
		}
		_ = syscall.CloseHandle(pi.Thread)
		return pi.Process
	}
	// A process that exits by itself: waited, not killed.
	h := spawn(`cmd.exe /c exit 0`)
	if !waitHelperExit(h, 10*time.Second) {
		t.Fatal("a process that exits must be seen as exited")
	}
	reapHelper(h, time.Second)

	// A process that does not exit: terminated after the grace.
	h = spawn(`waitfor.exe /t 60 OblireachReapTest` + newLegNonce()[:8])
	if waitHelperExit(h, 200*time.Millisecond) {
		t.Fatal("the long-running process exited on its own")
	}
	dup := dupHandle(t, h)
	defer syscall.CloseHandle(dup)
	start := time.Now()
	reapHelper(h, 300*time.Millisecond)
	if !waitHelperExit(dup, 5*time.Second) {
		t.Fatal("reapHelper must terminate a helper that did not exit")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("reapHelper took too long")
	}
	// No handle: nothing to do.
	reapHelper(0, time.Second)
	if !waitHelperExit(0, time.Second) {
		t.Fatal("no handle counts as exited")
	}
}

func dupHandle(t *testing.T, h syscall.Handle) syscall.Handle {
	t.Helper()
	self, _ := syscall.GetCurrentProcess()
	var d syscall.Handle
	if err := syscall.DuplicateHandle(self, h, self, &d, 0, false, syscall.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatalf("DuplicateHandle: %v", err)
	}
	return d
}
