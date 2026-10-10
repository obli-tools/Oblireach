package engine

import "sync"

// ── Local-input block: shared state machine ─────────────────────────────────
//
// "Cut the mouse/keyboard of THIS machine" the way TeamViewer does: the person
// in front of the remote screen is frozen, the remote operator is not. The
// mechanism is platform-specific (Windows: WH_MOUSE_LL / WH_KEYBOARD_LL hooks
// that swallow physical input and pass injected operator input — see
// input_windows.go). This file is the PURE-Go, platform-agnostic part: the
// ownership/reconciliation logic, unit-tested without any OS input API.
//
// Why a manager and not a bare boolean:
//   - F3 (report the truth): input_block_status must report the state ACTUALLY
//     applied — whether the hooks really installed — not merely what the viewer
//     asked for. platformInputBlockApply returns the effective state and the
//     manager reports that. On a platform with no mechanism the apply is a
//     no-op returning false, so the answer is always blocked:false (never a
//     false "blocked" that would leave a viewer thinking the local user is
//     frozen when they are not).
//   - F4 (never leave a user stuck): the block is owned by a SET of viewers.
//     Any of these drops that viewer's ownership — the viewer turning the block
//     off, its stream ending, the viewer disconnecting, or (for the cross-
//     session helper) the service pipe breaking. The hooks come off when the
//     LAST owner is gone, so no crash/restart/auto-update can leave the local
//     keyboard and mouse dead.
//   - Several viewers on one session: the block is per session and additive.
//     Two operators watching one machine each own it independently; local input
//     stays frozen until the last one that asked for it leaves. A viewer that
//     never asked for the block never removes another viewer's block.
//
// In practice each capture process serves one viewer (the direct path can serve
// several concurrent StreamSessions, the cross-session path is one helper
// process per viewer), so within a process there is usually one owner — but the
// set-based rule is what keeps the multi-viewer and teardown cases correct, and
// what the tests pin down.

type inputBlockManager struct {
	mu      sync.Mutex
	owners  map[string]bool
	applied bool
	// apply installs (want=true) or removes (want=false) the real block and
	// returns whether it is in effect afterwards. Swapped for a fake in tests.
	apply func(want bool) bool
}

func newInputBlockManager(apply func(want bool) bool) *inputBlockManager {
	return &inputBlockManager{owners: map[string]bool{}, apply: apply}
}

// request records that owner wants the block on (on=true) or no longer wants it
// (on=false) and reconciles the hooks. Returns the state actually applied.
func (m *inputBlockManager) request(owner string, on bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		m.owners[owner] = true
	} else {
		delete(m.owners, owner)
	}
	return m.reconcileLocked()
}

// release drops owner entirely — its viewer left, its stream ended, or its pipe
// broke. Returns the state actually applied afterwards.
func (m *inputBlockManager) release(owner string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.owners, owner)
	return m.reconcileLocked()
}

// reconcileLocked drives the real state toward "blocked iff at least one owner
// wants it". It only calls apply on a transition, and retries later if apply
// reported it could not reach the wanted state (e.g. the hooks failed to
// install): the desired/applied mismatch simply persists until the next call.
func (m *inputBlockManager) reconcileLocked() bool {
	want := len(m.owners) > 0
	if want != m.applied {
		m.applied = m.apply(want)
	}
	return m.applied
}

func (m *inputBlockManager) active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applied
}

// autoRelease is called when the platform lifted the block by itself: the
// local user's escape hatch (Windows: Ctrl+Alt+Del, lock screen or a UAC
// prompt took the input desktop, as BlockInput used to unblock on
// Ctrl+Alt+Del). Every owner is dropped, so the block does not come back on
// its own, and inputBlockReleased wakes the capture loop, which tells the
// viewer (input_block_status false). An operator may block again.
func (m *inputBlockManager) autoRelease() {
	m.mu.Lock()
	m.owners = map[string]bool{}
	m.applied = m.apply(false)
	m.mu.Unlock()
	select {
	case inputBlockReleased <- struct{}{}:
	default:
	}
}

// inputBlockReleased signals an automatic release to the capture loop of this
// process (buffered: a release is never lost, a second one is redundant).
var inputBlockReleased = make(chan struct{}, 1)

// onPlatformAutoRelease is what the platform layer calls when it lifted the
// block by itself. Set in init (not in a var initializer) to avoid an
// initialization cycle through localInputBlock's apply function.
var onPlatformAutoRelease func()

func init() { onPlatformAutoRelease = func() { localInputBlock.autoRelease() } }

// localInputBlock is the process-wide block manager. Each capture process (the
// service's direct path, or a cross-session helper) has its own; the hooks are
// process-scoped, so the OS also removes them if the process dies — the manager
// covers the graceful paths, process death covers the rest.
var localInputBlock = newInputBlockManager(func(want bool) bool {
	return platformInputBlockApply(want)
})

// setInputBlock records owner's block request and returns the applied state.
func setInputBlock(owner string, on bool) bool { return localInputBlock.request(owner, on) }

// releaseInputBlock drops owner's block (teardown paths) and returns the
// applied state. Safe to call for an owner that never held the block.
func releaseInputBlock(owner string) bool { return localInputBlock.release(owner) }

// inputBlockActive reports whether local input is currently frozen.
func inputBlockActive() bool { return localInputBlock.active() }

// inputBlockSupported reports whether this build can freeze LOCAL input without
// freezing the operator. It gates the input_block_local feature advertised to
// viewers so the desktop/web only offer the button where it actually works.
func inputBlockSupported() bool { return platformInputBlockSupported() }
