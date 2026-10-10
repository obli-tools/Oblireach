package engine

import "testing"

// fakeApply records the wanted transitions and lets a test decide what the
// "hardware" reports back (so we can simulate a hook install that fails).
type fakeApply struct {
	calls   []bool // every want passed to apply, in order
	succeed bool   // what apply returns for want=true (want=false always → false)
}

func (f *fakeApply) apply(want bool) bool {
	f.calls = append(f.calls, want)
	if want {
		return f.succeed
	}
	return false
}

// A single viewer turning the block on then off: apply is called once per
// transition and the reported state follows what was applied.
func TestBlockSingleOwnerToggles(t *testing.T) {
	f := &fakeApply{succeed: true}
	m := newInputBlockManager(f.apply)

	if got := m.request("v1", true); !got {
		t.Fatalf("request on → applied=%v, want true", got)
	}
	if !m.active() {
		t.Fatal("active() should report blocked after a successful apply")
	}
	if got := m.request("v1", false); got {
		t.Fatalf("request off → applied=%v, want false", got)
	}
	if m.active() {
		t.Fatal("active() should report unblocked after release")
	}
	// One apply(true) and one apply(false): no redundant toggling.
	if len(f.calls) != 2 || f.calls[0] != true || f.calls[1] != false {
		t.Fatalf("apply calls = %v, want [true false]", f.calls)
	}
}

// When the mechanism fails to engage (hooks didn't install), the reported
// state is false — never a phantom "blocked" that lies to the viewer.
func TestBlockReportsActuallyApplied(t *testing.T) {
	f := &fakeApply{succeed: false} // apply(true) cannot engage
	m := newInputBlockManager(f.apply)

	if got := m.request("v1", true); got {
		t.Fatalf("apply failed but request returned %v, want false", got)
	}
	if m.active() {
		t.Fatal("active() must be false when the hooks never installed")
	}
}

// Several viewers on one session: the block is additive and only lifts when the
// LAST owner that wanted it is gone (release rule for multi-viewer).
func TestBlockMultiOwnerRefcount(t *testing.T) {
	f := &fakeApply{succeed: true}
	m := newInputBlockManager(f.apply)

	m.request("v1", true) // installs
	m.request("v2", true) // second owner, already installed → no new apply
	if len(f.calls) != 1 {
		t.Fatalf("apply calls after two owners = %v, want a single [true]", f.calls)
	}
	if !m.active() {
		t.Fatal("blocked while two owners want it")
	}

	m.request("v1", false) // one owner left, v2 still wants it
	if !m.active() {
		t.Fatal("must stay blocked while v2 still wants it")
	}
	if len(f.calls) != 1 {
		t.Fatalf("apply should not have been called again: %v", f.calls)
	}

	m.request("v2", false) // last owner gone → lift
	if m.active() {
		t.Fatal("must unblock when the last owner drops it")
	}
	if len(f.calls) != 2 || f.calls[1] != false {
		t.Fatalf("apply calls = %v, want [true false]", f.calls)
	}
}

// A viewer that disappears without cleanly turning the block off (stream
// ended, disconnect, service pipe broke) is dropped by release(), and the block
// lifts once no owner remains — a local user can never stay stuck.
func TestBlockReleaseOwnerFreesStuckUser(t *testing.T) {
	f := &fakeApply{succeed: true}
	m := newInputBlockManager(f.apply)

	m.request("v1", true)
	if !m.active() {
		t.Fatal("blocked after request")
	}
	// The viewer never sends set_input_block:false; its stream just ends.
	if got := m.release("v1"); got {
		t.Fatalf("release → applied=%v, want false", got)
	}
	if m.active() {
		t.Fatal("release of the only owner must lift the block")
	}
}

// release() for an owner that never held the block (a teardown defer firing for
// a viewer that never blocked) is a harmless no-op and touches no hardware.
func TestBlockReleaseUnknownOwnerIsNoop(t *testing.T) {
	f := &fakeApply{succeed: true}
	m := newInputBlockManager(f.apply)

	if got := m.release("ghost"); got {
		t.Fatalf("release of unknown owner → %v, want false", got)
	}
	if len(f.calls) != 0 {
		t.Fatalf("release of unknown owner must not call apply: %v", f.calls)
	}
}

// Releasing one of two owners while the other still holds it must keep the
// block on; releasing the remaining one lifts it. Covers the "released when the
// viewer that set it leaves OR when the last viewer leaves" rule end to end.
func TestBlockReleaseLastOwnerLifts(t *testing.T) {
	f := &fakeApply{succeed: true}
	m := newInputBlockManager(f.apply)

	m.request("a", true)
	m.request("b", true)
	if got := m.release("a"); !got {
		t.Fatalf("still one owner, want blocked, got %v", got)
	}
	if got := m.release("b"); got {
		t.Fatalf("no owners left, want unblocked, got %v", got)
	}
}

// A failed install can be retried by a later request: the desired/applied
// mismatch persists and reconcile calls apply again next time.
func TestBlockRetriesAfterFailedApply(t *testing.T) {
	f := &fakeApply{succeed: false}
	m := newInputBlockManager(f.apply)

	m.request("v1", true) // apply(true) → false, applied stays false
	if m.active() {
		t.Fatal("must not be blocked after a failed apply")
	}
	f.succeed = true      // the mechanism can engage now
	m.request("v2", true) // want still true, applied false → apply retried
	if !m.active() {
		t.Fatal("a later request must retry the install and succeed")
	}
	if len(f.calls) != 2 {
		t.Fatalf("apply calls = %v, want two attempts", f.calls)
	}
}

// The local user's escape hatch (Windows: Ctrl+Alt+Del, lock, UAC prompt)
// lifts the block for EVERY owner, signals the capture loop so the viewer is
// told, and the block does not come back until an operator asks again.
func TestBlockAutoReleaseDropsAllOwners(t *testing.T) {
	for len(inputBlockReleased) > 0 { // drain a leftover signal
		<-inputBlockReleased
	}
	f := &fakeApply{succeed: true}
	m := newInputBlockManager(f.apply)
	m.request("v1", true)
	m.request("v2", true)

	m.autoRelease()

	if m.active() {
		t.Fatal("autoRelease must lift the block")
	}
	select {
	case <-inputBlockReleased:
	default:
		t.Fatal("autoRelease must signal the capture loop")
	}
	// A later "off" or release from a former owner changes nothing.
	calls := len(f.calls)
	if got := m.request("v1", false); got {
		t.Fatal("still blocked after autoRelease + off")
	}
	if got := m.release("v2"); got {
		t.Fatal("still blocked after autoRelease + release")
	}
	if len(f.calls) != calls {
		t.Fatalf("no new apply expected after autoRelease, got %v", f.calls[calls:])
	}
	// An operator can block again.
	if got := m.request("v1", true); !got {
		t.Fatal("blocking again after autoRelease must work")
	}
}

// The wire name Oblireach Desktop matches on, and the feature follows the
// platform's real support (Windows helper path only).
func TestInputBlockLocalFeature(t *testing.T) {
	if featInputBlockLocal != "input_block_local" {
		t.Fatalf("wire name changed: %q", featInputBlockLocal)
	}
	has := false
	for _, f := range agentFeatures() {
		if f == featInputBlockLocal {
			has = true
		}
	}
	if has != inputBlockSupported() {
		t.Fatalf("agentFeatures advertises input_block_local=%v, platform support=%v", has, inputBlockSupported())
	}
}
