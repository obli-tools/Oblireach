package main

// Lock the session when the connection ends ("lock_on_end").
//
// When the Windows session an operator connects to is LOCKED (the operator
// then unlocks it to work), the agent locks it again when the connection ends:
// the viewer disconnecting normally, or the stream / relay dropping. The
// operator can change that during the session (a padlock button in both
// viewers): lock at the end, or leave the session unlocked.
//
// Protocol (shared with the Obliance web viewer, contract: the Obliance viewer protocol):
//   - init.features of a supervised Windows helper lists "lock_on_end"
//     (engine/protocol.go featLockOnEnd); a viewer offers the button only then;
//   - agent -> viewer, right after the session is ready, after each change and
//     after each session switch:
//       {"type":"session_lock_state","lockedAtConnect":<bool>,"lockOnEnd":<bool>}
//     lockOnEnd = the viewer's last set_lock_on_end, else lockedAtConnect;
//   - viewer -> agent: {"type":"set_lock_on_end","enabled":<bool>}, answered
//     with session_lock_state (also when the request was malformed: the
//     state is unchanged then).
//
// The decision at the end (decideLockOnEnd) is pure and tested; the Windows
// side (lock_on_end_windows.go) reads the lock state of the session
// (WTSSessionInfoEx) and locks it by running LockWorkStation as the user
// signed in to it. This file holds no platform code.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"sync"
)

// sessionLockStateJSON is the agent -> viewer message.
func sessionLockStateJSON(lockedAtConnect, lockOnEnd bool) []byte {
	b, _ := json.Marshal(struct {
		Type            string `json:"type"`
		LockedAtConnect bool   `json:"lockedAtConnect"`
		LockOnEnd       bool   `json:"lockOnEnd"`
	}{"session_lock_state", lockedAtConnect, lockOnEnd})
	return b
}

var setLockOnEndMark = []byte(`set_lock_on_end`)

// parseSetLockOnEnd reads a viewer JSON frame. isMsg: it is a
// set_lock_on_end (the relay keeps it: it never reaches the capture helper);
// valid: "enabled" is a JSON boolean (anything else changes nothing). The
// byte pre-check keeps the relay from parsing every mouse move.
func parseSetLockOnEnd(payload []byte) (enabled, valid, isMsg bool) {
	if !bytes.Contains(payload, setLockOnEndMark) {
		return false, false, false
	}
	var m struct {
		Type    string          `json:"type"`
		Enabled json.RawMessage `json:"enabled"`
	}
	if json.Unmarshal(payload, &m) != nil || m.Type != "set_lock_on_end" {
		return false, false, false
	}
	switch string(bytes.TrimSpace(m.Enabled)) {
	case "true":
		return true, true, true
	case "false":
		return false, true, true
	}
	return false, false, true
}

// lockEndInput is what the end-of-connection decision looks at.
type lockEndInput struct {
	LockedAtConnect bool  // the session was locked when the operator connected
	Requested       *bool // the viewer's last set_lock_on_end (nil: never sent, an older viewer)
	OtherViewers    int   // other streams still showing the same Windows session
	HasUser         bool  // a user is signed in to the session (not the sign-in screen)
	LockedNow       bool  // the session is already locked (best effort)
	EverLive        bool  // the stream showed the session (its first capture helper came up)
}

// lockOnEndWanted: the state shown to the viewers — the operator's choice,
// else the default rule (lock again what was locked at connect).
func lockOnEndWanted(lockedAtConnect bool, requested *bool) bool {
	if requested != nil {
		return *requested
	}
	return lockedAtConnect
}

// decideLockOnEnd: lock the session at the end of this connection? reason is
// for the log ("lock" when it locks).
//   - never when the operator turned it off, or by default when the session
//     was not locked at connect;
//   - never while another viewer still shows the same session (the last one
//     to leave decides, with its own state);
//   - never a session without a signed-in user (sign-in screen: nothing to
//     lock), never one already locked, never when the stream never showed it.
func decideLockOnEnd(in lockEndInput) (lock bool, reason string) {
	switch {
	case !in.EverLive:
		return false, "never_live"
	case in.Requested != nil && !*in.Requested:
		return false, "disabled_by_operator"
	case in.Requested == nil && !in.LockedAtConnect:
		return false, "not_locked_at_connect"
	case in.OtherViewers > 0:
		return false, "other_viewers"
	case !in.HasUser:
		return false, "no_user"
	case in.LockedNow:
		return false, "already_locked"
	}
	return true, "lock"
}

// WTS_SESSIONSTATE_LOCK / _UNLOCK (WTSINFOEX_LEVEL1.SessionFlags). Windows 7
// and Server 2008 R2 report them the other way round (documented bug, see
// WTSINFOEX_LEVEL1_W remarks): wtsLockFlagsInverted.
const (
	wtsSessionStateLock   = 0
	wtsSessionStateUnlock = 1
)

// wtsLockFlagsInverted: Windows 7 / Server 2008 R2 (NT 6.1) swap LOCK and
// UNLOCK in WTSINFOEX_LEVEL1.SessionFlags. Unknown version (0.0): not inverted.
func wtsLockFlagsInverted(major, minor uint32) bool { return major == 6 && minor == 1 }

// sessionFlagsLocked reads SessionFlags. known=false for
// WTS_SESSIONSTATE_UNKNOWN (-1) or any other value: never treated as locked.
func sessionFlagsLocked(flags int32, inverted bool) (locked, known bool) {
	switch flags {
	case wtsSessionStateLock:
		return !inverted, true
	case wtsSessionStateUnlock:
		return inverted, true
	}
	return false, false
}

// parseWTSInfoEx reads the head of a WTSINFOEXW buffer (x64 and x86 share
// it): DWORD Level @0, then the union aligned on 8 (WTSINFOEX_LEVEL1_W holds
// LARGE_INTEGERs) @8: ULONG SessionId, WTS_CONNECTSTATE_CLASS SessionState,
// LONG SessionFlags @16.
func parseWTSInfoEx(b []byte) (sessionID uint32, flags int32, ok bool) {
	if len(b) < 20 || binary.LittleEndian.Uint32(b[0:4]) != 1 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint32(b[8:12]), int32(binary.LittleEndian.Uint32(b[16:20])), true
}

// lockOnEndState is one stream's state: the session it is about, whether
// that session was locked at connect, the viewer's choice.
type lockOnEndState struct {
	mu              sync.Mutex
	session         int
	ident           string // the session's user (userIdent) when the state was taken
	lockedAtConnect bool
	requested       *bool
}

// reset starts over for a session (the first leg, or a switch to another
// session / user): the viewer's choice concerned the previous one.
func (s *lockOnEndState) reset(session int, ident string, lockedAtConnect bool) {
	s.mu.Lock()
	s.session, s.ident, s.lockedAtConnect, s.requested = session, ident, lockedAtConnect, nil
	s.mu.Unlock()
}

// sameTarget: the state is already about this session and user.
func (s *lockOnEndState) sameTarget(session int, ident string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session == session && sameUser(s.ident, ident)
}

func (s *lockOnEndState) set(enabled bool) {
	s.mu.Lock()
	v := enabled
	s.requested = &v
	s.mu.Unlock()
}

// snapshot: the session, lockedAtConnect, the request (a copy) and the state
// shown to the viewers.
func (s *lockOnEndState) snapshot() (session int, lockedAtConnect bool, requested *bool, lockOnEnd bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requested != nil {
		v := *s.requested
		requested = &v
	}
	return s.session, s.lockedAtConnect, requested, lockOnEndWanted(s.lockedAtConnect, s.requested)
}

func (s *lockOnEndState) message() []byte {
	_, locked, _, on := s.snapshot()
	return sessionLockStateJSON(locked, on)
}
