//go:build windows

package engine

import (
	"testing"
)

// The WTS / SM_REMOTESESSION queries answer for this process's own session
// (a test run over RDP reports remote=true protocol=RDP).
func TestOwnSessionDisplay(t *testing.T) {
	d := ownSessionDisplay()
	if d.protocol < -1 || d.protocol > 2 {
		t.Fatalf("protocol = %d", d.protocol)
	}
	if d.protocol == wtsProtoRDP && !d.remote {
		t.Fatalf("an RDP session must be remote: %s", d)
	}
	// (Seen on Server 2025, the test run from a disconnected RDP session:
	// SM_REMOTESESSION=1 but protocol=console — why either signal is enough.)
	t.Logf("session %d: %s", CurrentSessionID(), d)
	// A session that does not exist: no connect state, never remote (Windows
	// may still answer the protocol query with 0, "console").
	if g := SessionDisplay(0x7FFFFFF0); g.remote || g.state != "" {
		t.Errorf("missing session: %s", g)
	}
}
