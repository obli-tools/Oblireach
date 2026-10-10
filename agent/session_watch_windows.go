//go:build windows

package main

import (
	"log"
	"syscall"
	"time"

	"github.com/oblireach/agent/engine"
)

// Session watcher (Windows) for the follow supervisor. Two sources:
//   - SERVICE_CONTROL_SESSIONCHANGE from the SCM (service_windows.go pushes a
//     wakeup on sessionChangeCh) — instant, but only when running as a service;
//   - a 500 ms poll that also covers interactive mode and any event the SCM
//     coalesced.
// Each wake builds a followSnapshot from EnumerateSessions() + the console id
// and hands it to the supervisor, which owns the followController.

// sessionChangeCh is signalled (non-blocking) by the service control handler on
// every WTS session change. Buffered so a burst coalesces into one poll.
var sessionChangeCh = make(chan struct{}, 1)

const sessionPollInterval = 500 * time.Millisecond

// snapshotSessions builds a followSnapshot for the given follow target. The
// console session's user/state are read best-effort from EnumerateSessions; the
// console id itself is authoritative (WTSGetActiveConsoleSessionId), so a bare
// login screen the enumeration filtered out is still followed (hasUser stays
// false → the helper spawns on the Winlogon desktop).
func snapshotSessions(mode followMode, pinned int) followSnapshot {
	now := time.Now()
	sessions := engine.EnumerateSessions()
	byID := make(map[int]engine.SessionInfo, len(sessions))
	for _, s := range sessions {
		byID[s.ID] = s
	}
	c := engine.ConsoleSessionID()
	snap := followSnapshot{now: now}
	if uint32(c) == 0xFFFFFFFF || c <= 0 {
		snap.consoleID = -1
	} else {
		snap.consoleID = c
		noteConsoleID(c) // feed the recent-console safety net (stream.go)
		info := wtsInfo{exists: true}
		if s, ok := byID[c]; ok {
			info.state = s.State
			info.user = s.Username
			info.ident = userIdent(s.Domain, s.Username)
		}
		snap.console = info
	}
	if mode == followPinned {
		if s, ok := byID[pinned]; ok {
			snap.pinned = wtsInfo{exists: true, state: s.State, user: s.Username, ident: userIdent(s.Domain, s.Username)}
		} else {
			snap.pinned = wtsInfo{exists: false}
		}
	}
	return snap
}

// sessionUser reports the session's logged-in user, if any: whether there is
// one (chooses the Default vs Winlogon desktop for the first leg), its name
// (shown) and its identity (userIdent, for the same-user rules).
func sessionUser(id int) (hasUser bool, user, ident string) {
	for _, s := range engine.EnumerateSessions() {
		if s.ID == id {
			return s.Username != "", s.Username, userIdent(s.Domain, s.Username)
		}
	}
	return false, "", ""
}

// ── Helper process wait / terminate (stopping the old leg) ──────────────────
// By HANDLE, never by PID: the process handle kept from CreateProcessAsUserW
// pins the process object, so its PID cannot be reused by another process while
// we wait on / terminate it. Reaping by PID after the helper died could have
// hit an unrelated process that got the recycled PID (winlogon.exe during a
// logon — a bugcheck) with the service's SYSTEM rights. A
// same-session respawn must wait for the old helper to exit first so the two
// never fight over the single DXGI duplication, the input hooks or the
// watermark window.

// waitHelperExit waits up to timeout for the helper process to exit. Returns
// true when it exited (or there is no handle: nothing to wait for).
func waitHelperExit(h syscall.Handle, timeout time.Duration) bool {
	if h == 0 {
		return true
	}
	ev, err := syscall.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return err == nil && ev == syscall.WAIT_OBJECT_0
}

// helperExitCode waits up to wait for the helper process to exit and returns
// its exit code (HelperExitText names it). exited=false: still running (or no
// handle / the code could not be read).
func helperExitCode(h syscall.Handle, wait time.Duration) (code uint32, exited bool) {
	if h == 0 || !waitHelperExit(h, wait) {
		return 0, false
	}
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return 0, false
	}
	return code, true
}

// watchHelperExit closes the returned channel when the helper process exits
// (polled, so the watch ends with stop and never touches a handle the caller
// released afterwards: the caller closes stop and waits on done first).
func watchHelperExit(h syscall.Handle, stop <-chan struct{}) (exited, done <-chan struct{}) {
	ex := make(chan struct{})
	dn := make(chan struct{})
	go func() {
		defer close(dn)
		if h == 0 {
			return
		}
		for {
			select {
			case <-stop:
				return
			default:
			}
			ev, err := syscall.WaitForSingleObject(h, 200)
			if err != nil {
				return
			}
			if ev == syscall.WAIT_OBJECT_0 {
				close(ex)
				return
			}
		}
	}()
	return ex, dn
}

// terminateHelper force-kills a helper process that did not exit in time. The
// helper's own OS teardown removes its input hooks anyway; this is the fallback
// so a same-session respawn is never blocked by a stuck old helper.
func terminateHelper(h syscall.Handle) {
	if h == 0 {
		return
	}
	if err := syscall.TerminateProcess(h, 1); err != nil {
		log.Printf("follow: TerminateProcess failed: %v", err)
	}
}

// reapHelper stops waiting on a helper: terminates it when it has not exited
// within grace, then releases the handle.
func reapHelper(h syscall.Handle, grace time.Duration) {
	if h == 0 {
		return
	}
	if !waitHelperExit(h, grace) {
		terminateHelper(h)
		waitHelperExit(h, time.Second)
	}
	_ = syscall.CloseHandle(h)
}
