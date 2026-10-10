package main

import (
	"strings"
	"testing"
	"time"
)

func TestDecideInstance(t *testing.T) {
	late := singleInstanceWait + time.Second
	cases := []struct {
		name string
		o    instanceObs
		want instanceAction
	}{
		{"mutex free: this is the app", instanceObs{Acquired: true}, instanceRun},
		{"mutex free even late (the old one ended)", instanceObs{Acquired: true, Elapsed: late}, instanceRun},
		{"running app shown in front: end", instanceObs{Window: true, Ack: singleInstanceAckShown}, instanceExit},
		{"running app quitting (update): wait", instanceObs{Window: true, Ack: singleInstanceAckQuitting}, instanceWait},
		{"running app quitting, too long: end", instanceObs{Window: true, Ack: singleInstanceAckQuitting, Elapsed: late}, instanceExit},
		{"no window yet (starting / ending): wait", instanceObs{}, instanceWait},
		{"no window, too long: never a second app", instanceObs{Elapsed: late}, instanceExit},
		{"window hung (no answer): wait", instanceObs{Window: true}, instanceWait},
		{"window hung, too long: end", instanceObs{Window: true, Elapsed: late}, instanceExit},
		{"quiet launch (startup) with the app there: end, nothing shown", instanceObs{Window: true, Quiet: true}, instanceExit},
		{"quiet launch, the app ending (no window): wait", instanceObs{Quiet: true}, instanceWait},
		{"quiet launch, mutex free: run", instanceObs{Acquired: true, Quiet: true}, instanceRun},
	}
	for _, c := range cases {
		if got := decideInstance(c.o); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestQuietLaunch(t *testing.T) {
	if quietLaunch(nil) || quietLaunch([]string{"foo"}) {
		t.Fatal("no --minimized: not quiet")
	}
	if !quietLaunch([]string{"--MINIMIZED"}) || !quietLaunch([]string{"x", startMinimizedArg}) {
		t.Fatal("--minimized: quiet")
	}
}

func TestSingleInstanceNames(t *testing.T) {
	// Per session (terminal server: one app per user), never Global\.
	if !strings.HasPrefix(singleInstanceMutex, `Local\`) {
		t.Fatalf("mutex %q must be in the session namespace", singleInstanceMutex)
	}
	if singleInstanceAckShown == 0 || singleInstanceAckQuitting == 0 || singleInstanceAckShown == singleInstanceAckQuitting {
		t.Fatal("the answers must be distinct and non-zero (0: no answer)")
	}
	// The update relaunch waits for the old process by PID, the launch's
	// wait covers an app still ending: bounded, and longer than a poll.
	if singleInstanceWait < 5*time.Second || singleInstanceWait > 30*time.Second || singleInstancePoll >= singleInstanceWait {
		t.Fatal("unreasonable wait policy")
	}
}
