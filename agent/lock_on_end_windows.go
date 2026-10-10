//go:build windows

package main

// Lock the session when the connection ends — the Windows side (the decision
// and the protocol: lock_on_end.go).
//
// Detection: WTSQuerySessionInformationW(WTSSessionInfoEx) of the streamed
// session, SessionFlags WTS_SESSIONSTATE_LOCK / _UNLOCK (inverted on Windows 7
// / Server 2008 R2: wtsLockFlagsInverted), read by the service when the stream
// opens (before the operator can unlock anything) and again after a switch to
// another session or user. Unknown = not locked: never locked by guess.
//
// Locking: LockWorkStation must be called by a process of the session's
// interactive desktop. The service (session 0) starts this executable with
// --lock-workstation IN the session, AS THE USER signed in to it
// (WTSQueryUserToken, winsta0\default, like the chat window: never with the
// service's SYSTEM token), and waits for its exit code. Nobody signed in
// (sign-in screen) = nothing to lock: the decision already refused it.

import (
	"fmt"
	"log"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

var (
	loeWTSQuerySessionInformationW = csWtsapi32.NewProc("WTSQuerySessionInformationW")
	loeWTSFreeMemory               = csWtsapi32.NewProc("WTSFreeMemory")
	loeLockWorkStation             = syscall.NewLazyDLL("user32.dll").NewProc("LockWorkStation")
)

const (
	loeWTSSessionInfoEx = 25 // WTS_INFO_CLASS WTSSessionInfoEx (Windows 7 / 2008 R2+)
	loeLockWait         = 10 * time.Second
	loeVerifyAfter      = 1500 * time.Millisecond
)

// sessionLockedNow reads the lock state of a session. known=false when it
// could not be read (or the session id does not match): never locked then.
func sessionLockedNow(sid int) (locked, known bool, flags int32) {
	if sid < 0 || loeWTSQuerySessionInformationW.Find() != nil || loeWTSFreeMemory.Find() != nil {
		return false, false, -1
	}
	var buf unsafe.Pointer
	var n uint32
	r, _, _ := loeWTSQuerySessionInformationW.Call(0 /* WTS_CURRENT_SERVER_HANDLE */, uintptr(uint32(sid)),
		loeWTSSessionInfoEx, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || buf == nil {
		return false, false, -1
	}
	defer loeWTSFreeMemory.Call(uintptr(buf))
	id, fl, ok := parseWTSInfoEx(unsafe.Slice((*byte)(buf), int(n)))
	if !ok || int(id) != sid {
		return false, false, -1
	}
	v := engine.WindowsVersion()
	locked, known = sessionFlagsLocked(fl, wtsLockFlagsInverted(v.Major, v.Minor))
	return locked, known, fl
}

// runLockWorkstation is the --lock-workstation mode: lock this session (the
// process runs on its interactive desktop as its user). Exit code 0 = the lock
// was started, else the Win32 error.
func runLockWorkstation() int {
	if loeLockWorkStation.Find() != nil {
		return 127
	}
	r, _, e := loeLockWorkStation.Call()
	if r != 0 {
		return 0
	}
	if errno, ok := e.(syscall.Errno); ok && errno != 0 {
		return int(errno)
	}
	return 1
}

// runAsSessionUser starts cmdLine in session sid as the user signed in to it
// (winsta0\default, the user's environment, no window) and waits up to wait
// for its exit code. Same token path as spawnChatHelperAsUser, but the
// process handle is kept for the exit code.
func runAsSessionUser(sid int, cmdLine string, wait time.Duration) (uint32, error) {
	csEnablePrivilege("SeTcbPrivilege")
	csEnablePrivilege("SeAssignPrimaryTokenPrivilege")
	csEnablePrivilege("SeIncreaseQuotaPrivilege")
	var tok syscall.Token
	if r, _, e := csWTSQueryUserToken.Call(uintptr(uint32(sid)), uintptr(unsafe.Pointer(&tok))); r == 0 {
		return 0, fmt.Errorf("no user token for session %d: %v", sid, e)
	}
	defer tok.Close()
	var env *uint16
	if r, _, _ := csCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&env)), uintptr(tok), 0); r == 0 || env == nil {
		return 0, fmt.Errorf("CreateEnvironmentBlock failed for session %d", sid)
	}
	defer csDestroyEnvironmentBlock.Call(uintptr(unsafe.Pointer(env)))
	cmd16, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, err
	}
	desktop, _ := syscall.UTF16PtrFromString(`winsta0\default`)
	si := syscall.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi syscall.ProcessInformation
	if err := syscall.CreateProcessAsUser(tok, nil, cmd16, nil, nil, false,
		csCreateUnicodeEnvironment|csCreateNoWindow, env, nil, &si, &pi); err != nil {
		return 0, fmt.Errorf("CreateProcessAsUser in session %d: %v", sid, err)
	}
	syscall.CloseHandle(pi.Thread)
	code, exited := helperExitCode(pi.Process, wait)
	reapHelper(pi.Process, 0) // (terminated if still there; the handle is closed)
	if !exited {
		return 0, fmt.Errorf("PID %d did not finish within %s", pi.ProcessId, wait)
	}
	return code, nil
}

// lockEndMu serialises the end-of-connection decisions (and the inheritance
// read at connect) against the registry of streams: of two viewers of the
// same session leaving at the same moment, exactly one sees itself as the last.
var lockEndMu sync.Mutex

// sessionInView: the Windows session this stream shows (or is switching to).
func (s *supervisor) sessionInView() int {
	if l := s.curLeg.Load(); l != nil {
		return l.sessionID
	}
	return int(s.lastSession.Load())
}

// lockTake reads whether the session is locked as the stream opens on it (or
// switches to it) and starts its lock-on-end state over. A stream joining a
// session another stream already shows inherits "locked at connect" from it:
// the first operator unlocked it, the session is to be locked again when the
// last one leaves.
func (s *supervisor) lockTake(sid int, hasUser bool, ident, why string) {
	locked, known, flags := false, false, int32(-1)
	if hasUser {
		locked, known, flags = sessionLockedNow(sid)
	}
	inherited := false
	if hasUser && !locked {
		lockEndMu.Lock()
		activeSupervisors.Range(func(_, v interface{}) bool {
			o := v.(*supervisor)
			if o == s {
				return true
			}
			osid, olocked, _, _ := o.lockState.snapshot()
			if olocked && osid == sid && o.lockState.sameTarget(sid, ident) {
				inherited = true
				return false
			}
			return true
		})
		lockEndMu.Unlock()
	}
	s.lockState.reset(sid, ident, locked || inherited)
	log.Printf("Stream %s: lock on end (%s): session %d user=%v locked=%v (known=%v flags=%d inherited=%v) — lock at the end: %v",
		s.token, why, sid, hasUser, locked, known, flags, inherited, locked || inherited)
}

func (s *supervisor) sendLockState() {
	_ = s.wsSend(0x1, s.lockState.message())
}

// onSetLockOnEnd applies a viewer's set_lock_on_end and answers with the
// state (unchanged for a malformed request).
func (s *supervisor) onSetLockOnEnd(enabled, valid bool) {
	if valid {
		s.lockState.set(enabled)
		sid, _, _, _ := s.lockState.snapshot()
		log.Printf("Stream %s: lock on end set by the operator: %v (session %d)", s.token, enabled, sid)
	}
	s.sendLockState()
}

// lockAtEnd runs when the stream ends (viewer gone, tunnel closed or dropped,
// error): it takes the stream out of the registry and, per decideLockOnEnd,
// locks the session it showed. Never another session, never one whose user
// changed since the state was taken.
func (s *supervisor) lockAtEnd() {
	lockEndMu.Lock()
	activeSupervisors.Delete(s.token)
	sid, lockedAtConnect, requested, want := s.lockState.snapshot()
	others := 0
	activeSupervisors.Range(func(_, v interface{}) bool {
		if o := v.(*supervisor); o != s && sid >= 0 && o.sessionInView() == sid {
			others++
		}
		return true
	})
	lockEndMu.Unlock()

	if sid < 0 {
		return // the stream never got that far
	}
	if inView := s.sessionInView(); inView != sid {
		log.Printf("Stream %s: lock on end: not locking — the state is about session %d, the stream showed session %d", s.token, sid, inView)
		return
	}
	hasUser, _, ident := sessionUser(sid)
	if hasUser && !s.lockState.sameTarget(sid, ident) {
		log.Printf("Stream %s: lock on end: not locking session %d — another user is signed in to it now", s.token, sid)
		return
	}
	lockedNow, known, _ := false, false, int32(-1)
	if hasUser && want {
		lockedNow, known, _ = sessionLockedNow(sid)
	}
	lock, reason := decideLockOnEnd(lockEndInput{
		LockedAtConnect: lockedAtConnect, Requested: requested, OtherViewers: others,
		HasUser: hasUser, LockedNow: known && lockedNow, EverLive: s.everLive,
	})
	req := "default"
	if requested != nil {
		req = fmt.Sprint(*requested)
	}
	log.Printf("Stream %s: lock on end: session %d lockedAtConnect=%v requested=%s otherViewers=%d user=%v — %s",
		s.token, sid, lockedAtConnect, req, others, hasUser, reason)
	if lock {
		go lockSessionNow(s.token, sid)
	}
}

// lockSessionNow locks session sid (LockWorkStation run in it as its user)
// and logs the outcome, read back from WTS.
func lockSessionNow(token string, sid int) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("Stream %s: lock on end: os.Executable: %v", token, err)
		return
	}
	code, err := runAsSessionUser(sid, fmt.Sprintf(`"%s" --lock-workstation`, exe), loeLockWait)
	if err != nil {
		log.Printf("Stream %s: lock on end: could not lock session %d: %v", token, sid, err)
		return
	}
	if code != 0 {
		log.Printf("Stream %s: lock on end: LockWorkStation in session %d failed (code %d)", token, sid, code)
		return
	}
	time.Sleep(loeVerifyAfter)
	locked, known, _ := sessionLockedNow(sid)
	log.Printf("Stream %s: lock on end: session %d locked (read back: locked=%v known=%v)", token, sid, locked, known)
}
