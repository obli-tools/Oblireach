package main

// Agent side of the blank screen (blank_screen.go): the session-follow
// state cache never replays it into another session.

import "testing"

// Session follow: the blank screen is never replayed into another session —
// the supervisor tells the viewer it is off (takeBlank) instead.
func TestStateCacheBlankNeverReplayed(t *testing.T) {
	c := newStreamStateCache()
	alice := userIdent("PC", "alice")
	c.observeToHelper([]byte(`{"type":"set_blank_screen","on":true}`), alice)
	c.observeFromHelper([]byte(`{"type":"blank_screen_status","active":true}`))
	msgs, _ := c.replayMessages(alice)
	for _, ty := range replayTypes(t, msgs) {
		if ty == "set_blank_screen" {
			t.Fatal("the blank screen must never be replayed")
		}
	}
	if !c.takeBlank() {
		t.Fatal("a blank screen on at the switch is reported dropped")
	}
	if c.takeBlank() {
		t.Fatal("reported once")
	}
	// Refused / removed on the agent side: nothing to report at the switch.
	c.observeToHelper([]byte(`{"type":"set_blank_screen","on":true}`), alice)
	c.observeFromHelper([]byte(`{"type":"blank_screen_status","active":false,"reason":"os_version"}`))
	if c.takeBlank() {
		t.Fatal("a refused blank screen is not reported as dropped")
	}
	c.observeToHelper([]byte(`{"type":"set_blank_screen","on":true}`), alice)
	c.observeToHelper([]byte(`{"type":"set_blank_screen","on":false}`), alice)
	if c.takeBlank() {
		t.Fatal("turned off by the operator: nothing to report")
	}
}
