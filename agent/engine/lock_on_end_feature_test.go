package engine

import (
	"runtime"
	"testing"
)

// "lock_on_end" is advertised only by a helper the Windows supervisor
// launched (--follow): the supervisor sees the connection end and locks the
// session; a stray helper and the direct capture path cannot.
func TestLockOnEndFeatureAdvertisement(t *testing.T) {
	saved := RunningUnderFollow
	defer func() { RunningUnderFollow = saved }()

	RunningUnderFollow = false
	if hasFeature(agentFeatures(), featLockOnEnd) {
		t.Fatal("lock_on_end advertised without the supervisor")
	}
	RunningUnderFollow = true
	if got, want := hasFeature(agentFeatures(), featLockOnEnd), runtime.GOOS == "windows"; got != want {
		t.Fatalf("lock_on_end under the supervisor = %v, want %v (Windows only)", got, want)
	}
	if hasFeature(directStreamFeatures(), featLockOnEnd) {
		t.Fatal("lock_on_end advertised by the direct capture path")
	}
	if featLockOnEnd != "lock_on_end" {
		t.Fatal("the feature name is shared with the viewers: never rename it")
	}
}
