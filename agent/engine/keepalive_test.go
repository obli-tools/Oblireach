package engine

import (
	"encoding/json"
	"testing"
	"time"
)

func hasFeature(list []string, f string) bool {
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

// initFeatures reads init.features back from the JSON the viewers receive.
func initFeatures(t *testing.T, m map[string]interface{}) []string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal init: %v", err)
	}
	var wire struct {
		Type     string   `json:"type"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal init: %v", err)
	}
	if wire.Type != "init" {
		t.Fatalf("init type = %q", wire.Type)
	}
	return wire.Features
}

// The wire name is what both viewers test (REACH.FEATURE / ReachFeature).
func TestKeepaliveFeatureName(t *testing.T) {
	if featKeepalive != "keepalive" {
		t.Fatalf("featKeepalive = %q, the viewers look for \"keepalive\"", featKeepalive)
	}
}

// Windows service + capture helper path: the helper's init (buildInitMessage)
// advertises the keepalive with the rest of the protocol.
func TestHelperInitAdvertisesKeepalive(t *testing.T) {
	if !hasFeature(agentFeatures(), featKeepalive) {
		t.Fatalf("agentFeatures() lacks %q: %v", featKeepalive, agentFeatures())
	}
	v := singleTileView(0, 0, 0, 1920, 1080, 0, true)
	m := buildInitMessage(1920, 1080, 30, []string{"h264", "jpeg"}, nil, v, 1, false, 48000, nil)
	if f := initFeatures(t, m); !hasFeature(f, featKeepalive) {
		t.Fatalf("helper init features = %v, want %q", f, featKeepalive)
	}
}

// Direct path (Windows interactive, macOS, Linux): the path that owns the
// inactivity timeout advertises the keepalive — on the first init AND on the
// re-init after a monitor switch (the viewers reset their feature set on every
// init) — and nothing it does not implement.
func TestDirectInitAdvertisesOnlyKeepalive(t *testing.T) {
	f := directStreamFeatures()
	if len(f) != 1 || f[0] != featKeepalive {
		t.Fatalf("directStreamFeatures() = %v, want only %q", f, featKeepalive)
	}
	mons := []MonitorInfo{{Index: 0, Width: 1920, Height: 1080, Primary: true}}
	m := buildDirectInitMessage(1920, 1080, 30, []string{"h264", "jpeg"}, mons)
	got := initFeatures(t, m)
	if len(got) != 1 || got[0] != featKeepalive {
		t.Fatalf("direct init features = %v, want [%s]", got, featKeepalive)
	}
	for _, unimplemented := range []string{featViewGen, featMosaic, featClipboardText, featClipboardPush,
		featClipboardFiles, featFileUpload, featFileDownload, featAudioV2, featSessionFollow} {
		if hasFeature(got, unimplemented) {
			t.Fatalf("direct init advertises %q, which the direct path does not implement", unimplemented)
		}
	}
	// The fields the viewers already read from the direct path are still there.
	for _, k := range []string{"type", "width", "height", "fps", "codec", "codecs", "monitors"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("direct init lost %q: %v", k, m)
		}
	}
	if m["codec"] != "h264" || m["width"] != 1920 || m["height"] != 1080 || m["fps"] != 30 {
		t.Fatalf("direct init fields changed: %v", m)
	}
}

func TestIsKeepaliveMsg(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"type":"keepalive"}`, true},
		{` { "type" : "keepalive" , "ts" : 12 } `, true},
		{`{"type":"keepalive2"}`, false},
		{`{"type":"Keepalive"}`, false},
		{`{"type":"clipboard_set","text":"keepalive"}`, false},
		{`{"type":"key","action":"down","key":"keepalive"}`, false},
		{`{"type":"mouse","action":"move","x":1,"y":2}`, false},
		{`{"type":"keepalive"`, false}, // truncated JSON
		{`"keepalive"`, false},
		{`keepalive`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := IsKeepaliveMsg([]byte(c.in)); got != c.want {
			t.Errorf("isKeepaliveMsg(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func fired(ch <-chan time.Time) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func waitFire(t *testing.T, ch <-chan time.Time, what string, within time.Duration) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(within):
		t.Fatalf("%s did not fire within %v", what, within)
	}
}

// A reset (keepalive, input) moves the warning and the expiry a full period
// later: the original deadline passes without either firing.
func TestIdleWatchResetPostponesTimeout(t *testing.T) {
	const timeout, warn = 400 * time.Millisecond, 100 * time.Millisecond
	w := newIdleWatch(timeout, warn)
	defer w.stop()

	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	w.reset()

	// Past the ORIGINAL warning (300 ms) and expiry (400 ms) deadlines:
	// timers never fire early, and the reset ones are due at +300/+400 ms.
	time.Sleep(250 * time.Millisecond)
	if fired(w.warning()) || fired(w.expired()) {
		t.Fatal("the idle watch fired on its pre-reset deadline")
	}

	waitFire(t, w.warning(), "warning", 2*time.Second)
	if !w.takeWarning() {
		t.Fatal("first warning of the period not taken")
	}
	if w.takeWarning() {
		t.Fatal("warning taken twice in one idle period")
	}
	waitFire(t, w.expired(), "expiry", 2*time.Second)
	if el := time.Since(start); el < timeout {
		t.Fatalf("expired %v after the reset, before the full timeout %v", el, timeout)
	}
}

// A fire the loop has not consumed yet (keepalive arriving right as the timer
// fired) must not end the stream after the reset.
func TestIdleWatchResetDrainsStaleFire(t *testing.T) {
	w := newIdleWatch(40*time.Millisecond, 10*time.Millisecond)
	defer w.stop()
	time.Sleep(120 * time.Millisecond) // both fired, nobody read them
	if !w.takeWarning() {
		t.Fatal("warning not takeable")
	}
	w.reset()
	if fired(w.warning()) || fired(w.expired()) {
		t.Fatal("a stale fire survived the reset")
	}
	if !w.takeWarning() {
		t.Fatal("reset did not re-arm the warning")
	}
	waitFire(t, w.expired(), "expiry after reset", 2*time.Second)
}

// Mirrors the direct stream loop (stream.go run): a viewer that keeps sending
// {"type":"keepalive"} holds a session with no input open past the timeout;
// once it stops (hidden with "while visible", or "never"), the unchanged
// timeout ends the session as before.
func TestKeepaliveHoldsIdleSessionOpen(t *testing.T) {
	const timeout = 500 * time.Millisecond
	w := newIdleWatch(timeout, 100*time.Millisecond)
	defer w.stop()

	frames := make(chan []byte, 16) // what the relay reader hands the loop
	stopSending := make(chan struct{})
	go func() {
		tk := time.NewTicker(25 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stopSending:
				return
			case <-tk.C:
				frames <- []byte(`{"type":"keepalive"}`)
			}
		}
	}()

	start := time.Now()
	keptUntil := start.Add(3 * timeout)
	expiredAt := time.Time{}
	sendingStopped := false
	for expiredAt.IsZero() {
		if !sendingStopped && time.Now().After(keptUntil) {
			close(stopSending)
			sendingStopped = true
		}
		select {
		case p := <-frames:
			if IsKeepaliveMsg(p) {
				w.reset()
			}
		case <-w.warning():
			w.takeWarning()
		case <-w.expired():
			expiredAt = time.Now()
		case <-time.After(5 * time.Second):
			t.Fatal("the idle session never expired after the keepalives stopped")
		}
	}
	if expiredAt.Before(keptUntil) {
		t.Fatalf("session expired after %v although keepalives kept coming", expiredAt.Sub(start))
	}
}
