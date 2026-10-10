package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func snapConsole(id int, user string, now time.Time) followSnapshot {
	return snapConsoleState(id, "Active", user, now)
}

func snapConsoleState(id int, state, user string, now time.Time) followSnapshot {
	return followSnapshot{consoleID: id, console: wtsInfo{exists: id >= 0, state: state, user: user, ident: userIdent("PC", user)}, now: now}
}

func snapPinned(exists bool, state, user string, now time.Time) followSnapshot {
	return followSnapshot{consoleID: 1, pinned: wtsInfo{exists: exists, state: state, user: user, ident: userIdent("PC", user)}, now: now}
}

// legOf is a leg serving `user` (identity PC\user) in session sid.
func legOf(sid int, user string) legInfo {
	return legInfo{session: sid, hasUser: user != "", user: user, ident: userIdent("PC", user)}
}

func TestOpenModeFor(t *testing.T) {
	if m, _ := openModeFor(-1, 2); m != followConsole {
		t.Fatal("requested <0 must follow the console")
	}
	if m, _ := openModeFor(0, 2); m != followConsole {
		t.Fatal("requested 0 must follow the console")
	}
	if m, _ := openModeFor(2, 2); m != followConsole {
		t.Fatal("requesting the current console must follow the console")
	}
	if m, p := openModeFor(5, 2); m != followPinned || p != 5 {
		t.Fatalf("requesting another session must pin it: mode=%v p=%d", m, p)
	}
}

// An explicit id that resolveTargetSession mapped elsewhere (the
// recent-console safety net) follows the console instead of pinning a session
// that no longer exists (which ended the stream with session_logged_off).
func TestFollowRequestFor(t *testing.T) {
	if got := followRequestFor(3, 5); got != -1 {
		t.Fatalf("vanished id 3 resolved to console 5 must follow the console, got %d", got)
	}
	if m, _ := openModeFor(followRequestFor(3, 5), 5); m != followConsole {
		t.Fatal("the safety-net stream must be in console mode")
	}
	if got := followRequestFor(4, 4); got != 4 {
		t.Fatalf("an id that resolved to itself stays requested, got %d", got)
	}
	if got := followRequestFor(-1, 2); got != -1 {
		t.Fatalf("a console request stays a console request, got %d", got)
	}
	if m, p := openModeFor(followRequestFor(4, 4), 2); m != followPinned || p != 4 {
		t.Fatalf("an explicit live non-console id is pinned, got mode=%v p=%d", m, p)
	}
}

// Identities are domain-qualified and case-insensitive.
func TestUserIdent(t *testing.T) {
	if userIdent("", "") != "" || userIdent("PC", "") != "" {
		t.Fatal("no user → empty identity")
	}
	if userIdent("CORP", "Alice") != userIdent("corp", "ALICE") {
		t.Fatal("identity must be case-insensitive")
	}
	if userIdent("PC", "Administrator") == userIdent("CORP", "Administrator") {
		t.Fatal("the local and the domain Administrator are different people")
	}
	if userIdent("", "bob") != "bob" {
		t.Fatalf("no domain → the user alone, got %q", userIdent("", "bob"))
	}
	// sameUser: a sample whose domain could not be read compares names only;
	// two known, different domains are two people.
	if !sameUser(userIdent("PC", "alice"), userIdent("", "Alice")) {
		t.Fatal("a missing domain must not make the same person look like another")
	}
	if sameUser(userIdent("PC", "alice"), userIdent("CORP", "alice")) {
		t.Fatal("PC and CORP alice are different people")
	}
	if sameUser(userIdent("PC", "alice"), userIdent("", "bob")) || sameUser("", "bob") {
		t.Fatal("different names are different people")
	}
	if !sameUser("", "") {
		t.Fatal("no user on both sides is the same (nobody)")
	}
}

// The controller must commit a switch only after two agreeing samples ≥400ms
// apart — the console id flips for a moment during a re-attach.
func TestDebounceNeedsTwoAgreeingSamples(t *testing.T) {
	c := newFollowController(followConsole, 0, legInfo{session: 1, hasUser: false})
	// A sign-in on the console (id unchanged) — first sample: not committed.
	if _, ok := c.evaluate(snapConsole(1, "alice", t0)); ok {
		t.Fatal("first sample must not commit")
	}
	// Too soon: still not committed.
	if _, ok := c.evaluate(snapConsole(1, "alice", t0.Add(100*time.Millisecond))); ok {
		t.Fatal("a sample inside the debounce window must not commit")
	}
	// After the window, the same decision commits.
	dec, ok := c.evaluate(snapConsole(1, "alice", t0.Add(followDebounce)))
	if !ok || dec.action != actionSwitch || dec.reason != "logon" || !dec.hasUser || dec.user != "alice" || dec.ident != "pc\\alice" {
		t.Fatalf("expected a committed logon switch, got %+v ok=%v", dec, ok)
	}
}

// A flip back to the old value before the window resets the debounce.
func TestDebounceResetsOnDisagreement(t *testing.T) {
	c := newFollowController(followConsole, 0, legOf(1, "alice"))
	// console changed to 2
	c.evaluate(snapConsole(2, "bob", t0))
	// flips back to 1 (still alice, the leg) → stay, pending cleared
	if _, ok := c.evaluate(snapConsole(1, "alice", t0.Add(200*time.Millisecond))); ok {
		t.Fatal("flip back must not commit")
	}
	// console 2 again — the clock is past the window, but the pending was reset,
	// so this is only the FIRST agreeing sample again: no commit yet.
	if _, ok := c.evaluate(snapConsole(2, "bob", t0.Add(1*time.Second))); ok {
		t.Fatal("a reset debounce must require two fresh agreeing samples")
	}
	if dec, ok := c.evaluate(snapConsole(2, "bob", t0.Add(1*time.Second+followDebounce))); !ok || dec.session != 2 {
		t.Fatalf("expected a committed switch to 2, got %+v ok=%v", dec, ok)
	}
}

func TestConsoleReasons(t *testing.T) {
	who := func(u string) wtsInfo { return wtsInfo{user: u, ident: userIdent("PC", u)} }
	// login screen (no user) → user signs in on a DIFFERENT console session id
	if r := consoleReason(legInfo{session: 1, hasUser: false}, who("alice")); r != "logon" {
		t.Fatalf("logon reason, got %q", r)
	}
	// user session moved away → console back to a login screen
	if r := consoleReason(legOf(2, "alice"), who("")); r != "logoff" {
		t.Fatalf("logoff reason, got %q", r)
	}
	// fast user switching: another user at the console
	if r := consoleReason(legOf(2, "alice"), who("bob")); r != "logon" {
		t.Fatalf("user-switch reason, got %q", r)
	}
	// same user, different console id (rare)
	if r := consoleReason(legOf(2, "alice"), who("alice")); r != "console_change" {
		t.Fatalf("console_change reason, got %q", r)
	}
	// same short name, another domain: another person
	if r := consoleReason(legOf(2, "Administrator"), wtsInfo{user: "Administrator", ident: userIdent("CORP", "Administrator")}); r != "logon" {
		t.Fatalf("a same-name user of another domain is a logon, got %q", r)
	}
}

// Lock / unlock / UAC: same console id, same user → stay (the native capture
// follows the secure desktop).
func TestLockUnlockStays(t *testing.T) {
	c := newFollowController(followConsole, 0, legOf(1, "alice"))
	for i := 0; i < 5; i++ {
		if dec, ok := c.evaluate(snapConsole(1, "alice", t0.Add(time.Duration(i)*time.Second))); ok || dec.action != actionStay {
			t.Fatalf("lock/unlock must stay, got %+v ok=%v", dec, ok)
		}
	}
}

func TestNoConsoleWaits(t *testing.T) {
	c := newFollowController(followConsole, 0, legInfo{session: 1, hasUser: false})
	if dec, ok := c.evaluate(followSnapshot{consoleID: -1, now: t0}); ok || dec.action != actionWait {
		t.Fatalf("no console → wait, got %+v ok=%v", dec, ok)
	}
}

// A console that names a Disconnected session (being torn down) is never a
// switch target, even with a live leg elsewhere.
func TestConsoleDisconnectedWaits(t *testing.T) {
	c := newFollowController(followConsole, 0, legOf(2, "alice"))
	for i := 0; i < 3; i++ {
		if dec, ok := c.evaluate(snapConsoleState(1, "Disconnected", "", t0.Add(time.Duration(i)*time.Second))); ok || dec.action != actionWait {
			t.Fatalf("a Disconnected console must wait, got %+v ok=%v", dec, ok)
		}
	}
}

// A user signing in to the SAME session id the login-screen helper was spawned
// on → switch reason logon (so the watermark is on the user's desktop).
func TestSameSessionUserAppears(t *testing.T) {
	c := newFollowController(followConsole, 0, legInfo{session: 1, hasUser: false})
	c.evaluate(snapConsole(1, "alice", t0))
	dec, ok := c.evaluate(snapConsole(1, "alice", t0.Add(followDebounce)))
	if !ok || dec.action != actionSwitch || dec.session != 1 || !dec.hasUser || dec.reason != "logon" {
		t.Fatalf("expected a same-session logon switch, got %+v ok=%v", dec, ok)
	}
}

// The same console id reused by another user (the previous one signed out):
// a switch reason logon (notify the new person, never replay the block).
func TestConsoleSameIDOtherUser(t *testing.T) {
	c := newFollowController(followConsole, 0, legOf(1, "alice"))
	c.evaluate(snapConsole(1, "bob", t0))
	dec, ok := c.evaluate(snapConsole(1, "bob", t0.Add(followDebounce)))
	if !ok || dec.action != actionSwitch || dec.reason != "logon" || dec.user != "bob" {
		t.Fatalf("expected a logon switch to bob, got %+v ok=%v", dec, ok)
	}
}

func TestPinnedDisconnectReconnect(t *testing.T) {
	c := newFollowController(followPinned, 5, legOf(5, "alice"))
	// Disconnected → no_display (after debounce).
	c.evaluate(snapPinned(true, "Disconnected", "alice", t0))
	dec, ok := c.evaluate(snapPinned(true, "Disconnected", "alice", t0.Add(followDebounce)))
	if !ok || dec.action != actionNoDisplay {
		t.Fatalf("disconnected → no_display, got %+v ok=%v", dec, ok)
	}
	c.appliedNoDisplay()
	// Still disconnected → stay (already reported).
	if dec, ok := c.evaluate(snapPinned(true, "Disconnected", "alice", t0.Add(2*time.Second))); ok || dec.action != actionStay {
		t.Fatalf("still disconnected → stay, got %+v ok=%v", dec, ok)
	}
	// Comes back Active → switch reason reconnected.
	c.evaluate(snapPinned(true, "Active", "alice", t0.Add(3*time.Second)))
	dec, ok = c.evaluate(snapPinned(true, "Active", "alice", t0.Add(3*time.Second+followDebounce)))
	if !ok || dec.action != actionSwitch || dec.reason != "reconnected" || dec.session != 5 {
		t.Fatalf("reconnected switch, got %+v ok=%v", dec, ok)
	}
}

func TestPinnedLoggedOff(t *testing.T) {
	c := newFollowController(followPinned, 5, legOf(5, "alice"))
	c.evaluate(snapPinned(false, "", "", t0))
	dec, ok := c.evaluate(snapPinned(false, "", "", t0.Add(followDebounce)))
	if !ok || dec.action != actionErrorEnd || dec.errCode != "session_logged_off" {
		t.Fatalf("logged off → error, got %+v ok=%v", dec, ok)
	}
}

// A user signs in to a PINNED login-prompt session (RDS "drive a
// new login"): re-spawn on the Default desktop, reason logon, so the watermark
// and REC pill are on the user's desktop and the user is notified.
func TestPinnedLoginPromptUserAppears(t *testing.T) {
	c := newFollowController(followPinned, 7, legInfo{session: 7, hasUser: false})
	if dec, ok := c.evaluate(snapPinned(true, "Connected", "", t0)); ok || dec.action != actionStay {
		t.Fatalf("login prompt, no user: stay, got %+v ok=%v", dec, ok)
	}
	c.evaluate(snapPinned(true, "Active", "bob", t0.Add(time.Second)))
	dec, ok := c.evaluate(snapPinned(true, "Active", "bob", t0.Add(time.Second+followDebounce)))
	if !ok || dec.action != actionSwitch || dec.session != 7 || !dec.hasUser || dec.user != "bob" || dec.reason != "logon" {
		t.Fatalf("expected a logon switch in the pinned session, got %+v ok=%v", dec, ok)
	}
}

// A pinned session is bound to its user: if the id now shows a login
// prompt or belongs to another user, the pinned session is gone.
func TestPinnedUserReplacedEnds(t *testing.T) {
	for _, tc := range []struct {
		name, user, domain string
	}{
		{"login prompt", "", ""},
		{"another user", "bob", "PC"},
		{"same name, other domain", "alice", "CORP"},
	} {
		c := newFollowController(followPinned, 5, legOf(5, "alice"))
		snap := func(now time.Time) followSnapshot {
			return followSnapshot{consoleID: 1, pinned: wtsInfo{exists: true, state: "Active", user: tc.user, ident: userIdent(tc.domain, tc.user)}, now: now}
		}
		c.evaluate(snap(t0))
		dec, ok := c.evaluate(snap(t0.Add(followDebounce)))
		if !ok || dec.action != actionErrorEnd || dec.errCode != "session_logged_off" {
			t.Fatalf("%s: expected session_logged_off, got %+v ok=%v", tc.name, dec, ok)
		}
	}
}

// A pinned session's helper died: respawn in the pinned session after the
// settle time; died while the session disconnected: no_display.
func TestPinnedHelperGone(t *testing.T) {
	c := newFollowController(followPinned, 5, legOf(5, "alice"))
	c.helperGone(t0)
	c.evaluate(snapPinned(true, "Active", "alice", t0.Add(600*time.Millisecond)))
	dec, ok := c.evaluate(snapPinned(true, "Active", "alice", t0.Add(helperGoneSettle+100*time.Millisecond)))
	if !ok || dec.action != actionSwitch || dec.session != 5 || dec.reason != "helper_restart" {
		t.Fatalf("expected a helper_restart respawn in the pinned session, got %+v ok=%v", dec, ok)
	}
	c2 := newFollowController(followPinned, 5, legOf(5, "alice"))
	c2.helperGone(t0)
	c2.evaluate(snapPinned(true, "Disconnected", "alice", t0.Add(600*time.Millisecond)))
	dec, ok = c2.evaluate(snapPinned(true, "Disconnected", "alice", t0.Add(helperGoneSettle+100*time.Millisecond)))
	if !ok || dec.action != actionNoDisplay {
		t.Fatalf("expected no_display, got %+v ok=%v", dec, ok)
	}
}

func TestHelperRestartBudget(t *testing.T) {
	c := newFollowController(followConsole, 0, legOf(1, "alice"))
	now := t0
	for i := 0; i < helperRestartMax; i++ {
		if !c.helperGone(now) {
			t.Fatalf("helper death %d must be allowed a respawn", i)
		}
		c.appliedSwitch(followDecision{action: actionSwitch, session: 1, hasUser: true, user: "alice", ident: userIdent("PC", "alice")})
		now = now.Add(time.Second)
	}
	// One too many within the window → the stream must end.
	if c.helperGone(now) {
		t.Fatal("over budget must refuse the respawn")
	}
	// Well past the window: allowed again.
	if !c.helperGone(now.Add(2 * helperRestartWindow)) {
		t.Fatal("after the window a respawn is allowed again")
	}
}

// The helper dies while the console is being re-attached. No respawn is
// decided from ONE sample: while the console has no session, or still names
// the session being torn down (Disconnected), the controller waits; nothing
// commits before helperGoneSettle; the new console is followed, not the dying
// session.
func TestHelperGoneDuringReattachFollowsNewConsole(t *testing.T) {
	c := newFollowController(followConsole, 0, legInfo{session: 1, hasUser: false})
	if !c.helperGone(t0) {
		t.Fatal("first helper death must be allowed")
	}
	// Console momentarily has no session → wait.
	if dec, ok := c.evaluate(followSnapshot{consoleID: -1, now: t0.Add(100 * time.Millisecond)}); ok || dec.action != actionWait {
		t.Fatalf("no console → wait, got %+v ok=%v", dec, ok)
	}
	// Console still names the dying login session, now Disconnected → wait.
	if dec, ok := c.evaluate(snapConsoleState(1, "Disconnected", "", t0.Add(300*time.Millisecond))); ok || dec.action != actionWait {
		t.Fatalf("a torn-down console session → wait, got %+v ok=%v", dec, ok)
	}
	// Alice's re-attached session 2 becomes the console: two samples, but the
	// first pair is inside helperGoneSettle → not committed yet.
	c.evaluate(snapConsole(2, "alice", t0.Add(500*time.Millisecond)))
	if _, ok := c.evaluate(snapConsole(2, "alice", t0.Add(900*time.Millisecond))); ok {
		t.Fatal("nothing commits before helperGoneSettle after a helper death")
	}
	dec, ok := c.evaluate(snapConsole(2, "alice", t0.Add(helperGoneSettle+100*time.Millisecond)))
	if !ok || dec.action != actionSwitch || dec.session != 2 || !dec.hasUser || dec.reason != "logon" {
		t.Fatalf("expected a committed switch to the re-attached session 2, got %+v ok=%v", dec, ok)
	}
}

// A helper crash with no WTS change: respawn in place, only after the settle
// time and two agreeing samples, reason helper_restart.
func TestHelperGoneRespawnsInPlace(t *testing.T) {
	c := newFollowController(followConsole, 0, legOf(1, "alice"))
	c.helperGone(t0)
	c.evaluate(snapConsole(1, "alice", t0.Add(200*time.Millisecond)))
	if _, ok := c.evaluate(snapConsole(1, "alice", t0.Add(700*time.Millisecond))); ok {
		t.Fatal("must not respawn before helperGoneSettle")
	}
	dec, ok := c.evaluate(snapConsole(1, "alice", t0.Add(helperGoneSettle)))
	if !ok || dec.action != actionSwitch || dec.session != 1 || dec.reason != "helper_restart" || dec.ident != userIdent("pc", "ALICE") {
		t.Fatalf("expected a helper_restart respawn in session 1, got %+v ok=%v", dec, ok)
	}
	// Once respawned, the same state is a stay again.
	c.appliedSwitch(dec)
	if dec, ok := c.evaluate(snapConsole(1, "alice", t0.Add(3*time.Second))); ok || dec.action != actionStay {
		t.Fatalf("after the respawn: stay, got %+v ok=%v", dec, ok)
	}
}

// A failed switch is retried on the next debounced samples (same target, same
// reason) — and follows the session if it moved meanwhile.
func TestSwitchFailedRetries(t *testing.T) {
	c := newFollowController(followConsole, 0, legInfo{session: 1, hasUser: false})
	c.evaluate(snapConsole(2, "alice", t0))
	dec, ok := c.evaluate(snapConsole(2, "alice", t0.Add(followDebounce)))
	if !ok || dec.session != 2 {
		t.Fatalf("expected a switch to 2, got %+v ok=%v", dec, ok)
	}
	c.switchFailed(dec, t0.Add(time.Second))
	// Same state: retried (not before helperGoneSettle + two samples), reason kept.
	c.evaluate(snapConsole(2, "alice", t0.Add(1100*time.Millisecond)))
	if _, ok := c.evaluate(snapConsole(2, "alice", t0.Add(1600*time.Millisecond))); ok {
		t.Fatal("a retry must wait helperGoneSettle")
	}
	dec, ok = c.evaluate(snapConsole(2, "alice", t0.Add(2100*time.Millisecond)))
	if !ok || dec.action != actionSwitch || dec.session != 2 || dec.reason != "logon" {
		t.Fatalf("expected the logon switch to be retried, got %+v ok=%v", dec, ok)
	}
	// If the console moved meanwhile, the retry goes there instead.
	c.switchFailed(dec, t0.Add(3*time.Second))
	c.evaluate(snapConsole(3, "bob", t0.Add(4*time.Second)))
	dec, ok = c.evaluate(snapConsole(3, "bob", t0.Add(5*time.Second)))
	if !ok || dec.session != 3 || dec.user != "bob" {
		t.Fatalf("expected the retry to follow the console to 3, got %+v ok=%v", dec, ok)
	}
}

func TestNonceAndCmdLine(t *testing.T) {
	n := newLegNonce()
	if !validNonce(n) {
		t.Fatalf("generated nonce %q rejected", n)
	}
	for _, bad := range []string{"", "xyz", strings.Repeat("f", 31), strings.Repeat("f", 33), "0123456789ABCDEF0123456789abcdef"} {
		if validNonce(bad) {
			t.Errorf("bad nonce accepted: %q", bad)
		}
	}
	line, err := captureHelperCmdLine(`C:\Program Files\Oblireach\oblireach-agent.exe`, "127.0.0.1:50210", n, legGenBase(3), 0, false)
	if err != nil {
		t.Fatalf("valid command line refused: %v", err)
	}
	want := `"C:\Program Files\Oblireach\oblireach-agent.exe" --capture-helper --addr=127.0.0.1:50210 --follow --nonce=` + n + " --gen-base=3145728"
	if line != want {
		t.Fatalf("command line\n got %s\nwant %s", line, want)
	}
	// A non-loopback or malformed addr, or a bad nonce, is refused (no injection).
	if _, err := captureHelperCmdLine("x.exe", "10.0.0.1:5", n, 0, 0, false); err == nil {
		t.Fatal("non-loopback addr accepted")
	}
	if _, err := captureHelperCmdLine("x.exe", "127.0.0.1:5 --url=http://x", n, 0, 0, false); err == nil {
		t.Fatal("addr with an injected argument accepted")
	}
	if _, err := captureHelperCmdLine("x.exe", "127.0.0.1:5", "nope", 0, 0, false); err == nil {
		t.Fatal("bad nonce accepted")
	}
	// A switch leg asks the helper to wait for DXGI; the value is bounded.
	line, err = captureHelperCmdLine("x.exe", "127.0.0.1:5", n, 0, switchDXGIWaitMs, false)
	if err != nil || !strings.HasSuffix(line, " --dxgi-wait=5000") {
		t.Fatalf("switch leg command line: %q err=%v", line, err)
	}
	for _, bad := range []int{-1, maxDXGIWaitMs + 1} {
		if _, err := captureHelperCmdLine("x.exe", "127.0.0.1:5", n, 0, bad, false); err == nil {
			t.Fatalf("dxgi-wait %d accepted", bad)
		}
	}
}

func TestLegGenBaseMonotonic(t *testing.T) {
	prev := -1
	for i := 0; i < 8; i++ {
		g := legGenBase(i)
		if g <= prev {
			t.Fatalf("gen base not increasing at leg %d: %d <= %d", i, g, prev)
		}
		// A leg has a whole 1<<20 slice before the next leg's base.
		if g != i<<20 {
			t.Fatalf("gen base %d unexpected: %d", i, g)
		}
		prev = g
	}
}

func TestStateCacheReplaySameUser(t *testing.T) {
	c := newStreamStateCache()
	alice := userIdent("PC", "alice")
	c.observeToHelper([]byte(`{"type":"set_recording","recording":true}`), alice)
	c.observeToHelper([]byte(`{"type":"set_codec","codec":"vp9"}`), alice)
	c.observeToHelper([]byte(`{"type":"set_view","mode":"mosaic","maxWidth":3840}`), alice)
	c.observeToHelper([]byte(`{"type":"set_input_block","block":true}`), alice)

	msgs, dropped := c.replayMessages(userIdent("pc", "ALICE"))
	if dropped {
		t.Fatal("same user: the block must be replayed, not dropped")
	}
	types := replayTypes(t, msgs)
	// recording, codec, view, input_block — in that order.
	want := []string{"set_recording", "set_codec", "set_view", "set_input_block"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("replay order got %v want %v", types, want)
	}
}

func TestStateCacheReplayDifferentUserDropsBlock(t *testing.T) {
	c := newStreamStateCache()
	c.observeToHelper([]byte(`{"type":"set_input_block","block":true}`), userIdent("PC", "alice"))
	bob := userIdent("PC", "bob")
	msgs, dropped := c.replayMessages(bob)
	if !dropped {
		t.Fatal("a different user must drop the local-input block")
	}
	for _, m := range replayTypes(t, msgs) {
		if m == "set_input_block" {
			t.Fatal("the block must not be replayed to a different user")
		}
	}
	// And a later replay to the same different user must not resurrect it.
	msgs2, dropped2 := c.replayMessages(bob)
	if dropped2 {
		t.Fatal("the block was already dropped")
	}
	for _, m := range replayTypes(t, msgs2) {
		if m == "set_input_block" {
			t.Fatal("the block must stay dropped")
		}
	}
}

// The same short name in another domain is another person: the block
// is not replayed to them.
func TestStateCacheOtherDomainDropsBlock(t *testing.T) {
	c := newStreamStateCache()
	c.observeToHelper([]byte(`{"type":"set_input_block","block":true}`), userIdent("PC", "Administrator"))
	if _, dropped := c.replayMessages(userIdent("CORP", "Administrator")); !dropped {
		t.Fatal(`CORP\Administrator is not PC\Administrator: the block must be dropped`)
	}
}

// A block set while no leg runs (no known user) is never replayed.
func TestStateCacheBlockWithoutUserDropped(t *testing.T) {
	c := newStreamStateCache()
	c.observeToHelper([]byte(`{"type":"set_input_block","block":true}`), "")
	if _, dropped := c.replayMessages(userIdent("PC", "alice")); !dropped {
		t.Fatal("a block without a known user must be dropped, not applied to whoever is there")
	}
}

// The local user lifted the block with the escape hatch
// (Ctrl+Alt+Del, lock, UAC): the helper reports input_block_status false, and
// the next same-user switch must NOT freeze them again.
func TestStateCacheEscapeHatchNotReplayed(t *testing.T) {
	c := newStreamStateCache()
	alice := userIdent("PC", "alice")
	c.observeToHelper([]byte(`{"type":"set_input_block","block":true}`), alice)
	c.observeFromHelper([]byte(`{"type":"input_block_status","blocked":true}`)) // applied: kept
	if msgs, dropped := c.replayMessages(alice); dropped || replayTypes(t, msgs)[len(msgs)-1] != "set_input_block" {
		t.Fatal("an applied block is replayed to the same user")
	}
	c.observeFromHelper([]byte(`{"type":"input_block_status","blocked":false}`)) // escape hatch
	msgs, dropped := c.replayMessages(alice)
	if dropped {
		t.Fatal("nothing to drop: the block was already lifted")
	}
	for _, m := range replayTypes(t, msgs) {
		if m == "set_input_block" {
			t.Fatal("a block the local user lifted must not come back on its own")
		}
	}
	// The operator may block again.
	c.observeToHelper([]byte(`{"type":"set_input_block","block":true}`), alice)
	msgs, _ = c.replayMessages(alice)
	if types := replayTypes(t, msgs); types[len(types)-1] != "set_input_block" {
		t.Fatalf("an operator's new block is replayed, got %v", types)
	}
}

func TestStateCacheRecordingOffStillReplayed(t *testing.T) {
	c := newStreamStateCache()
	// Never recording: a fresh helper must still be told recording:false so it
	// does not inherit a REC pill from a previous leg.
	msgs, _ := c.replayMessages("alice")
	if len(msgs) != 1 {
		t.Fatalf("expected only set_recording, got %d messages", len(msgs))
	}
	var m struct {
		Type      string `json:"type"`
		Recording bool   `json:"recording"`
	}
	if json.Unmarshal(msgs[0], &m) != nil || m.Type != "set_recording" || m.Recording {
		t.Fatalf("expected set_recording:false, got %s", msgs[0])
	}
}

func TestStateCacheTidTracking(t *testing.T) {
	c := newStreamStateCache()
	c.observeToHelper([]byte(`{"type":"file_begin","tid":7,"name":"a.txt","size":10}`), "alice")
	c.observeToHelper([]byte(`{"type":"file_get","tid":9,"source":"clipboard","idx":0}`), "alice")
	// One completes before the switch.
	c.observeFromHelper([]byte(`{"type":"file_done","tid":7,"ok":true}`))
	got := c.takeTids()
	if len(got) != 1 || got[0] != 9 {
		t.Fatalf("expected only tid 9 in flight, got %v", got)
	}
	// takeTids cleared the set.
	if len(c.takeTids()) != 0 {
		t.Fatal("takeTids must clear the tracked transfers")
	}
}

// A transfer request that arrives while no helper runs is refused at once.
func TestTransferStartTid(t *testing.T) {
	if tid, ok := transferStartTid([]byte(`{"type":"file_begin","tid":12,"name":"a"}`)); !ok || tid != 12 {
		t.Fatalf("file_begin: tid=%d ok=%v", tid, ok)
	}
	if tid, ok := transferStartTid([]byte(`{"type":"file_get","tid":3}`)); !ok || tid != 3 {
		t.Fatalf("file_get: tid=%d ok=%v", tid, ok)
	}
	for _, p := range []string{`{"type":"file_cancel","tid":3}`, `{"type":"file_begin"}`, `{"type":"mouse","x":1}`, `nope`} {
		if _, ok := transferStartTid([]byte(p)); ok {
			t.Fatalf("not a transfer start: %s", p)
		}
	}
}

func replayTypes(t *testing.T, msgs [][]byte) []string {
	t.Helper()
	var out []string
	for _, b := range msgs {
		var m struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("replay message not JSON: %s", b)
		}
		out = append(out, m.Type)
	}
	return out
}
