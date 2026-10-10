package engine

import "testing"

func TestBgEventRelevant(t *testing.T) {
	cases := []struct {
		name            string
		event           uint32
		idObject, idChd int32
		want            bool
	}{
		{"foreground", bgEventSystemForeground, 0, 0, true},
		{"menu popup start", bgEventSystemMenuPopupStart, 0, 0, true},
		{"move/size start", bgEventSystemMoveSizeStart, 0, 0, true},
		{"move/size end", bgEventSystemMoveSizeEnd, 0, 0, true},
		{"drag/drop start", bgEventSystemDragDropStart, 0, 0, true},
		{"task switch", bgEventSystemSwitchStart, 0, 0, true},
		{"object show, top-level window", bgEventObjectShow, bgObjidWindow, bgChildidSelf, true},
		{"object reorder, top-level window", bgEventObjectReorder, bgObjidWindow, bgChildidSelf, true},
		// Noise that must not cost a re-assert:
		{"object show, cursor", bgEventObjectShow, -9 /*OBJID_CURSOR*/, 0, false},
		{"object show, caret", bgEventObjectShow, -8 /*OBJID_CARET*/, 0, false},
		{"object show, child control", bgEventObjectShow, bgObjidWindow, 3, false},
		{"object reorder, client area", bgEventObjectReorder, -4 /*OBJID_CLIENT*/, 0, false},
		{"unrelated event", 0x8005 /*EVENT_OBJECT_CREATE*/, 0, 0, false},
		{"menu popup end (not hooked)", 0x0007, 0, 0, false},
	}
	for _, c := range cases {
		if got := bgEventRelevant(c.event, c.idObject, c.idChd); got != c.want {
			t.Errorf("%s: bgEventRelevant(%#x,%d,%d) = %v, want %v", c.name, c.event, c.idObject, c.idChd, got, c.want)
		}
	}
}

// Every hooked event is accepted for a top-level window (nothing in the
// subscription list is dropped by the filter by accident).
func TestBgHookedEventsAllRelevant(t *testing.T) {
	for _, e := range bgHookedEvents {
		if !bgEventRelevant(e, bgObjidWindow, bgChildidSelf) {
			t.Errorf("hooked event %#x is filtered out for a top-level window", e)
		}
	}
}

// A burst collapses to a single re-assert: only the first request posts a
// wake, and after take() the next burst posts again.
func TestBgCoalescer(t *testing.T) {
	var c bgCoalescer
	if !c.request() {
		t.Fatal("first request should ask for a wake")
	}
	for i := 0; i < 100; i++ {
		if c.request() {
			t.Fatal("a request during a pending wake must not ask for another")
		}
	}
	c.take() // the window thread handled the wake
	if !c.request() {
		t.Fatal("a request after take() should ask for a fresh wake")
	}
	c.take()
	// Idempotent: take() with nothing pending leaves it clear.
	c.take()
	if !c.request() {
		t.Fatal("request after a redundant take() should still ask for a wake")
	}
}

func TestBgDragOriginalRoundTrip(t *testing.T) {
	for _, on := range []bool{true, false} {
		got, ok := bgDecodeDragOriginal(bgEncodeDragOriginal(on))
		if !ok || got != on {
			t.Errorf("round trip on=%v: got (%v,%v)", on, got, ok)
		}
	}
	// Tolerates surrounding whitespace / newline (a marker written with one).
	if v, ok := bgDecodeDragOriginal([]byte(" 1\r\n")); !ok || !v {
		t.Errorf("padded \"1\" decoded to (%v,%v)", v, ok)
	}
	// Rejects garbage (a truncated / corrupt marker): do not touch the setting.
	for _, bad := range []string{"", "x", "2", "true", "01"} {
		if _, ok := bgDecodeDragOriginal([]byte(bad)); ok {
			t.Errorf("garbage %q decoded as valid", bad)
		}
	}
}

func TestBgNeedForceDrag(t *testing.T) {
	if !bgNeedForceDrag(false) {
		t.Error("drag outline shows when full-window drag is off: must force it on")
	}
	if bgNeedForceDrag(true) {
		t.Error("already on: nothing to force")
	}
}

func TestBgDragMarkerNamePerSession(t *testing.T) {
	a, b := bgDragMarkerName(2), bgDragMarkerName(7)
	if a == b {
		t.Fatal("two sessions must not share one marker name")
	}
	for _, n := range []string{a, b} {
		if n == "" || n[len(n)-7:] != ".marker" {
			t.Errorf("unexpected marker name %q", n)
		}
	}
}
