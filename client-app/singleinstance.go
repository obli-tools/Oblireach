package main

// singleinstance.go — one Oblireach Desktop per user session (the pure
// part; the Win32 side is singleinstance_windows.go).
//
// A second launch (the Start menu, a desktop shortcut, a double click on the
// exe) brings the running app's MAIN window back on screen, in front (out of
// the taskbar or the notification area), and ends. The session windows
// (detached_windows.go) are never the target: they live in the same process
// and are reached from the main window.
//
// The one-click update (updater.go) is not affected: its script waits for
// the old process to be GONE (by PID) before installing, then relaunches the
// installed exe; the old process's handle on the mutex went with it. A launch
// that meets an instance which is quitting (its main window already gone, or
// answering "quitting") waits for it to end, at most singleInstanceWait, and
// then starts.

import (
	"strings"
	"time"
)

const (
	// The mutex: Local\ = this session's namespace (one app per user on a
	// terminal server, each in its own session).
	singleInstanceMutex = `Local\Oblireach.Desktop.SingleInstance`
	// The registered window message the main window answers (mainWndProc).
	singleInstanceMsgName = "Oblireach.Desktop.Activate"
	// Its answers: shown and in front / quitting (an update installs, or the
	// app is closing: the launch waits for it to end).
	singleInstanceAckShown    = 0x0B1A
	singleInstanceAckQuitting = 0x0B1B
	// How long a launch waits for an instance that holds the mutex but has
	// no main window to show (starting, or ending), polling every
	// singleInstancePoll. Past it, the launch ends: never two apps.
	singleInstanceWait = 15 * time.Second
	singleInstancePoll = 250 * time.Millisecond
)

// instanceObs: what a launch sees, at one try.
type instanceObs struct {
	Acquired bool          // the mutex was free: this launch is THE app
	Window   bool          // the running app's main window was found
	Ack      uintptr       // its answer to the activation message (0: none, hung)
	Elapsed  time.Duration // since the first try
	Quiet    bool          // started with --minimized (Windows startup): nothing to show
}

type instanceAction int

const (
	instanceRun  instanceAction = iota // start the app
	instanceExit                       // another one runs (shown now, or nothing to show): end
	instanceWait                       // try again after singleInstancePoll
)

// decideInstance: what the launch does now. Pure (tests: singleinstance_test.go).
func decideInstance(o instanceObs) instanceAction {
	if o.Acquired {
		return instanceRun
	}
	if o.Window && o.Quiet {
		// A start with Windows while the app already runs (a second Run
		// entry, a re-logon race): it stays as it is, not pushed in front.
		return instanceExit
	}
	if o.Window && o.Ack == singleInstanceAckShown {
		return instanceExit
	}
	// No window yet (starting, or already gone while the process ends), a
	// quitting app, or one that did not answer: wait, bounded.
	if o.Elapsed < singleInstanceWait {
		return instanceWait
	}
	return instanceExit
}

// quietLaunch: the arguments ask for no window in front (--minimized).
func quietLaunch(args []string) bool {
	for _, a := range args {
		if strings.EqualFold(strings.TrimSpace(a), startMinimizedArg) {
			return true
		}
	}
	return false
}
