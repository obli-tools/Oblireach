package main

import (
	"encoding/binary"
	"encoding/json"
	"testing"
)

// Lock the session when the connection ends (lock_on_end.go): the decision,
// the two messages, the WTS flags (Windows 7 / 2008 R2 inversion) and the
// per-stream state.

func boolPtr(b bool) *bool { return &b }

func TestDecideLockOnEnd(t *testing.T) {
	base := lockEndInput{LockedAtConnect: true, HasUser: true, EverLive: true}
	cases := []struct {
		name   string
		edit   func(in *lockEndInput)
		lock   bool
		reason string
	}{
		{"locked at connect, old viewer (no request): default rule locks", func(in *lockEndInput) {}, true, "lock"},
		{"not locked at connect, no request: stays unlocked", func(in *lockEndInput) { in.LockedAtConnect = false }, false, "not_locked_at_connect"},
		{"operator turned it off", func(in *lockEndInput) { in.Requested = boolPtr(false) }, false, "disabled_by_operator"},
		{"operator turned it on although it was unlocked", func(in *lockEndInput) { in.LockedAtConnect = false; in.Requested = boolPtr(true) }, true, "lock"},
		{"operator kept it on", func(in *lockEndInput) { in.Requested = boolPtr(true) }, true, "lock"},
		{"another viewer still on the session", func(in *lockEndInput) { in.OtherViewers = 1 }, false, "other_viewers"},
		{"another viewer, but off anyway", func(in *lockEndInput) { in.OtherViewers = 2; in.Requested = boolPtr(false) }, false, "disabled_by_operator"},
		{"sign-in screen: nothing to lock", func(in *lockEndInput) { in.HasUser = false }, false, "no_user"},
		{"sign-in screen even when asked", func(in *lockEndInput) { in.HasUser = false; in.Requested = boolPtr(true) }, false, "no_user"},
		{"already locked again", func(in *lockEndInput) { in.LockedNow = true }, false, "already_locked"},
		{"the stream never showed the session", func(in *lockEndInput) { in.EverLive = false }, false, "never_live"},
	}
	for _, c := range cases {
		in := base
		c.edit(&in)
		lock, reason := decideLockOnEnd(in)
		if lock != c.lock || reason != c.reason {
			t.Errorf("%s: got (%v, %s), want (%v, %s)", c.name, lock, reason, c.lock, c.reason)
		}
	}
}

func TestLockOnEndWanted(t *testing.T) {
	for _, c := range []struct {
		locked bool
		req    *bool
		want   bool
	}{{true, nil, true}, {false, nil, false}, {true, boolPtr(false), false}, {false, boolPtr(true), true}} {
		if got := lockOnEndWanted(c.locked, c.req); got != c.want {
			t.Errorf("lockOnEndWanted(%v, %v) = %v", c.locked, c.req, got)
		}
	}
}

func TestSessionLockStateJSON(t *testing.T) {
	got := string(sessionLockStateJSON(true, false))
	if want := `{"type":"session_lock_state","lockedAtConnect":true,"lockOnEnd":false}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestParseSetLockOnEnd(t *testing.T) {
	for _, c := range []struct {
		in                    string
		enabled, valid, isMsg bool
	}{
		{`{"type":"set_lock_on_end","enabled":true}`, true, true, true},
		{`{"type":"set_lock_on_end","enabled":false}`, false, true, true},
		{`{"enabled":true,"type":"set_lock_on_end"}`, true, true, true},
		{`{"type":"set_lock_on_end"}`, false, false, true},
		{`{"type":"set_lock_on_end","enabled":"true"}`, false, false, true},
		{`{"type":"set_lock_on_end","enabled":1}`, false, false, true},
		{`{"type":"mouse","x":1,"note":"set_lock_on_end"}`, false, false, false},
		{`{"type":"set_lock_on_end_x","enabled":true}`, false, false, false},
		{`{"type":"keepalive"}`, false, false, false},
		{`not json set_lock_on_end`, false, false, false},
	} {
		e, v, m := parseSetLockOnEnd([]byte(c.in))
		if e != c.enabled || v != c.valid || m != c.isMsg {
			t.Errorf("%s: got (%v,%v,%v), want (%v,%v,%v)", c.in, e, v, m, c.enabled, c.valid, c.isMsg)
		}
	}
}

// WTS_SESSIONSTATE_LOCK = 0, _UNLOCK = 1, swapped on Windows 7 / 2008 R2.
func TestSessionFlagsLocked(t *testing.T) {
	if !wtsLockFlagsInverted(6, 1) || wtsLockFlagsInverted(6, 2) || wtsLockFlagsInverted(6, 3) || wtsLockFlagsInverted(10, 0) || wtsLockFlagsInverted(0, 0) {
		t.Fatal("only NT 6.1 (Windows 7 / Server 2008 R2) is inverted")
	}
	for _, c := range []struct {
		flags         int32
		inverted      bool
		locked, known bool
	}{
		{0, false, true, true}, {1, false, false, true},
		{0, true, false, true}, {1, true, true, true},
		{-1, false, false, false}, {-1, true, false, false}, {7, false, false, false},
	} {
		l, k := sessionFlagsLocked(c.flags, c.inverted)
		if l != c.locked || k != c.known {
			t.Errorf("flags %d inverted %v: got (%v,%v)", c.flags, c.inverted, l, k)
		}
	}
}

func TestParseWTSInfoEx(t *testing.T) {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[0:], 1)  // Level
	binary.LittleEndian.PutUint32(b[8:], 3)  // SessionId
	binary.LittleEndian.PutUint32(b[12:], 0) // WTSActive
	binary.LittleEndian.PutUint32(b[16:], 0) // WTS_SESSIONSTATE_LOCK
	if id, fl, ok := parseWTSInfoEx(b); !ok || id != 3 || fl != 0 {
		t.Fatalf("got (%d,%d,%v)", id, fl, ok)
	}
	binary.LittleEndian.PutUint32(b[16:], 0xFFFFFFFF)
	if _, fl, ok := parseWTSInfoEx(b); !ok || fl != -1 {
		t.Fatalf("unknown flags: got (%d,%v)", fl, ok)
	}
	binary.LittleEndian.PutUint32(b[0:], 2)
	if _, _, ok := parseWTSInfoEx(b); ok {
		t.Fatal("another level accepted")
	}
	if _, _, ok := parseWTSInfoEx(b[:19]); ok {
		t.Fatal("a short buffer accepted")
	}
}

func TestLockOnEndState(t *testing.T) {
	var s lockOnEndState
	s.reset(-1, "", false)
	if s.sameTarget(2, `corp\ann`) {
		t.Fatal("an unset state matched a session")
	}
	s.reset(2, `corp\ann`, true)
	var m map[string]interface{}
	if err := json.Unmarshal(s.message(), &m); err != nil || m["lockedAtConnect"] != true || m["lockOnEnd"] != true {
		t.Fatalf("default: %v %v", m, err)
	}
	s.set(false)
	if _, locked, req, on := s.snapshot(); !locked || req == nil || *req || on {
		t.Fatalf("after set(false): locked=%v req=%v on=%v", locked, req, on)
	}
	if !s.sameTarget(2, `ann`) || s.sameTarget(2, `corp\bob`) || s.sameTarget(3, `corp\ann`) {
		t.Fatal("sameTarget: same session and user only")
	}
	// A switch to another session starts over: the choice is not carried.
	s.reset(3, `corp\bob`, false)
	if _, locked, req, on := s.snapshot(); locked || req != nil || on {
		t.Fatalf("after reset: locked=%v req=%v on=%v", locked, req, on)
	}
	s.set(true)
	if _, _, _, on := s.snapshot(); !on {
		t.Fatal("set(true) on an unlocked session: lock at the end")
	}
}
