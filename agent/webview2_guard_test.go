package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type wv2Clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *wv2Clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *wv2Clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestWV2Guard(t *testing.T) (*webview2Guard, *wv2Clock, string) {
	t.Helper()
	clk := &wv2Clock{t: time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)}
	p := filepath.Join(t.TempDir(), "webview2-state.json")
	g := newWebView2Guard(func() string { return p })
	g.now = clk.now
	return g, clk, p
}

func missing(at time.Time) webview2Detection {
	return webview2Detection{Scope: "none", At: at}
}

func TestWebView2GuardBackoffAndForce(t *testing.T) {
	g, clk, _ := newTestWV2Guard(t)
	g.noteDetection(missing(clk.now()))

	want := []time.Duration{10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, step := range want {
		d := g.begin("auto", "microsoft", false)
		if !d.OK {
			t.Fatalf("attempt %d refused: %s %s", i+1, d.Reason, d.Detail)
		}
		// A second request while it runs: one attempt at a time, even forced.
		if d2 := g.begin("manual", "microsoft", true); d2.OK || d2.Reason != "in_progress" {
			t.Fatalf("concurrent attempt: %+v", d2)
		}
		next := g.finish(d.Attempt, webview2Result{Reason: "network", ExitCode: 0x80072EFD})
		if got := next.Sub(clk.now()); got != step {
			t.Fatalf("failure %d: back-off %v, want %v", i+1, got, step)
		}
		// Automatic requests wait for the back-off...
		clk.add(step - time.Second)
		if d3 := g.begin("auto", "microsoft", false); d3.OK || d3.Reason != "cooldown" {
			t.Fatalf("failure %d: automatic attempt during the back-off: %+v", i+1, d3)
		}
		clk.add(time.Second)
	}
	// A forced request (device page button) ignores the back-off, not the 1-min floor.
	d := g.begin("auto", "microsoft", false)
	if !d.OK {
		t.Fatal("after the back-off an automatic attempt must start")
	}
	g.finish(d.Attempt, webview2Result{Reason: "exit_code", ExitCode: 1})
	if d := g.begin("manual", "microsoft", true); d.OK || d.Reason != "min_gap" {
		t.Fatalf("forced attempt within 1 min of the last start: %+v", d)
	}
	clk.add(webview2MinGap)
	if d := g.begin("manual", "microsoft", false); d.OK {
		t.Fatal("automatic attempt must still wait for the back-off")
	}
	if d := g.begin("manual", "microsoft", true); !d.OK {
		t.Fatalf("forced attempt must bypass the back-off: %+v", d)
	} else {
		g.finish(d.Attempt, webview2Result{Installed: true, Version: "154.0.1.2"})
	}
	st := g.snapshot()
	if st.Failures != 0 || !st.NextAttemptAt.IsZero() || st.LastOutcome != "installed" {
		t.Fatalf("success must clear the back-off: %+v", st)
	}
}

func TestWebView2GuardSkips(t *testing.T) {
	g, clk, _ := newTestWV2Guard(t)
	g.noteDetection(webview2Detection{Version: "154.0.4258.37", Scope: "machine", At: clk.now()})
	if d := g.begin("x", "microsoft", true); d.OK || d.Reason != "already_installed" {
		t.Fatalf("installed runtime: %+v", d)
	}
	g.noteDetection(webview2Detection{Scope: "none", Unsupported: true, At: clk.now()})
	if d := g.begin("x", "microsoft", true); d.OK || d.Reason != "unsupported" {
		t.Fatalf("unsupported OS, even forced: %+v", d)
	}
	g.noteDetection(webview2Detection{Scope: "none", PolicyBlocked: true, At: clk.now()})
	if d := g.begin("x", "microsoft", false); d.OK || d.Reason != "blocked" {
		t.Fatalf("policy block, automatic: %+v", d)
	}
	if d := g.begin("x", "microsoft", true); !d.OK {
		t.Fatalf("policy block, forced request is tried: %+v", d)
	}
}

func TestWebView2GuardSkipLogRateLimited(t *testing.T) {
	g, clk, _ := newTestWV2Guard(t)
	g.noteDetection(webview2Detection{Version: "1.2.3.4", Scope: "machine", At: clk.now()})
	if d := g.begin("a", "microsoft", false); !d.Log {
		t.Fatal("first skip is logged")
	}
	if d := g.begin("b", "microsoft", false); d.Log {
		t.Fatal("identical skip right after must not be logged again")
	}
	clk.add(webview2SkipLogEvery)
	if d := g.begin("c", "microsoft", false); !d.Log {
		t.Fatal("identical skip is logged again after the interval")
	}
}

func TestWebView2GuardPersistAndAdopt(t *testing.T) {
	g, clk, p := newTestWV2Guard(t)
	g.noteDetection(missing(clk.now()))
	d := g.begin("auto_1", "microsoft", false)
	if !d.OK {
		t.Fatal(d.Reason)
	}
	d.Attempt.PID = 4242
	g.launched(d.Attempt)

	// The service restarts (MSI upgrade) while the installer runs.
	clk.add(2 * time.Minute)
	g2 := newWebView2Guard(func() string { return p })
	g2.now = clk.now
	g2.noteDetection(missing(clk.now()))
	if s := g2.status(true); s == nil || s.State != wv2StateInstalling || s.InstallingSince == "" {
		t.Fatalf("pending attempt of the previous process must show as installing: %+v", s)
	}
	att := g2.adoptPending()
	if att == nil || att.ID != "auto_1" || att.PID != 4242 {
		t.Fatalf("adoptPending: %+v", att)
	}
	if d := g2.begin("manual", "microsoft", true); d.OK || d.Reason != "in_progress" {
		t.Fatalf("never a second installer while the adopted one runs: %+v", d)
	}
	g2.finish(att, webview2Result{Installed: true, Version: "154.0.1.2"})
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var st webview2PersistState
	if json.Unmarshal(data, &st) != nil || st.Pending != nil || st.LastOutcome != "installed" {
		t.Fatalf("state file after the adopted attempt: %s", data)
	}

	// A pending attempt older than the stale limit is not "installing".
	g3, clk3, _ := newTestWV2Guard(t)
	g3.noteDetection(missing(clk3.now()))
	d3 := g3.begin("old", "microsoft", false)
	g3.launched(d3.Attempt)
	g3.inFlight = nil // as if a previous process had launched it
	clk3.add(webview2StaleInFlight + time.Minute)
	if s := g3.status(true); s.State == wv2StateInstalling {
		t.Fatalf("stale attempt still installing: %+v", s)
	}
	if d := g3.begin("new", "microsoft", false); !d.OK {
		t.Fatalf("a stale attempt must not block forever: %+v", d)
	}
}

func TestWebView2Status(t *testing.T) {
	g, clk, _ := newTestWV2Guard(t)
	if g.status(true) != nil {
		t.Fatal("no detection yet: no block")
	}
	cases := []struct {
		det    webview2Detection
		state  string
		reason string
	}{
		{webview2Detection{Version: "154.0.4258.37", Scope: "machine"}, wv2StateOK, ""},
		{webview2Detection{Scope: "none"}, wv2StateMissing, ""},
		{webview2Detection{Scope: "user_only"}, wv2StateMissing, "per_user_only"},
		// Installed for the SYSTEM account only: the chat window runs as the
		// signed-in user, who cannot use it.
		{webview2Detection{Scope: "other"}, wv2StateMissing, "per_user_only"},
		{webview2Detection{Scope: "none", PolicyBlocked: true}, wv2StateBlocked, "blocked_by_policy"},
		{webview2Detection{Scope: "none", Unsupported: true}, wv2StateUnsupported, "unsupported_os"},
		{webview2Detection{Scope: "unknown", Err: "loader failed"}, wv2StateUnknown, "detect_error"},
	}
	for _, c := range cases {
		c.det.At = clk.now()
		g.noteDetection(c.det)
		s := g.status(true)
		if s.State != c.state || s.Reason != c.reason || !s.Installer {
			t.Fatalf("%+v → %+v, want %s/%s", c.det, s, c.state, c.reason)
		}
	}
	// failed: reason of the last attempt, attempts, next automatic attempt.
	g.noteDetection(missing(clk.now()))
	d := g.begin("a", "microsoft", false)
	if s := g.status(true); s.State != wv2StateInstalling {
		t.Fatalf("during an attempt: %+v", s)
	}
	g.finish(d.Attempt, webview2Result{Reason: "network", ExitCode: 0x80072EFD, Detail: "runtime still not detected"})
	s := g.status(false)
	if s.State != wv2StateFailed || s.Reason != "network" || s.Attempts != 1 || s.NextAttemptAt == "" || s.Installer ||
		!strings.Contains(s.LastError, "0x80072EFD") {
		t.Fatalf("after a failure: %+v", s)
	}
	// A runtime that appears (installed by hand) clears the back-off.
	g.noteDetection(webview2Detection{Version: "154.0.1.1", Scope: "machine", At: clk.now()})
	if s := g.status(true); s.State != wv2StateOK || s.Attempts != 0 || s.LastError != "" || s.NextAttemptAt != "" {
		t.Fatalf("runtime found later: %+v", s)
	}
	if st := g.snapshot(); st.Failures != 0 || !st.NextAttemptAt.IsZero() {
		t.Fatalf("back-off must be cleared: %+v", st)
	}
}

// ── Pipeline (fakes for detection / signature / launch) ─────────────────────

type wv2Fakes struct {
	mu        sync.Mutex
	installed bool
	detects   int
	launches  int
	verifyErr error
	exitCode  int64
	timedOut  bool
	installOn func() // called by the fake installer
	closed    int
}

type fakeLock struct{ f *wv2Fakes }

func (l fakeLock) Close() error { l.f.mu.Lock(); l.f.closed++; l.f.mu.Unlock(); return nil }

func installWV2Fakes(t *testing.T, bootstrapper bool) (*wv2Fakes, *webview2Guard) {
	t.Helper()
	f := &wv2Fakes{}
	dir := t.TempDir()
	boot := filepath.Join(dir, webview2BootstrapperName)
	if bootstrapper {
		if err := os.WriteFile(boot, []byte("MZ"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	g := newWebView2Guard(func() string { return filepath.Join(dir, "webview2-state.json") })
	saved := struct {
		g      *webview2Guard
		sup    func() bool
		det    func() webview2Detection
		path   func() string
		ver    func(string) (io.Closer, string, error)
		launch func(string, io.Closer) (int, func(time.Duration) (int64, bool, error), error)
		alive  func(int) bool
		sleep  func(time.Duration)
		now    func() time.Time
	}{webview2, webview2Supported, webview2DetectFn, webview2BootstrapperPath, webview2VerifyFn, webview2LaunchFn, webview2AliveFn, webview2Sleep, webview2Now}
	t.Cleanup(func() {
		webview2, webview2Supported, webview2DetectFn, webview2BootstrapperPath = saved.g, saved.sup, saved.det, saved.path
		webview2VerifyFn, webview2LaunchFn, webview2AliveFn, webview2Sleep, webview2Now = saved.ver, saved.launch, saved.alive, saved.sleep, saved.now
	})
	webview2 = g
	webview2Supported = func() bool { return true }
	webview2DetectFn = func() webview2Detection {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.detects++
		if f.installed {
			return webview2Detection{Version: "154.0.4258.37", Scope: "machine"}
		}
		return webview2Detection{Scope: "none"}
	}
	webview2BootstrapperPath = func() string { return boot }
	webview2VerifyFn = func(p string) (io.Closer, string, error) {
		if p != boot {
			t.Fatalf("verify called on %s", p)
		}
		if f.verifyErr != nil {
			return nil, "", f.verifyErr
		}
		return fakeLock{f}, "Microsoft Corporation", nil
	}
	webview2LaunchFn = func(p string, lock io.Closer) (int, func(time.Duration) (int64, bool, error), error) {
		f.mu.Lock()
		f.launches++
		f.mu.Unlock()
		if lock != nil {
			lock.Close()
		}
		return 777, func(time.Duration) (int64, bool, error) {
			if f.installOn != nil {
				f.installOn()
			}
			return f.exitCode, f.timedOut, nil
		}, nil
	}
	// Detection polling: a fake clock that advances with each sleep.
	clk := &wv2Clock{t: time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)}
	webview2Now = clk.now
	g.now = clk.now
	webview2Sleep = func(d time.Duration) { clk.add(d) }
	return f, g
}

func TestWebView2InstallPipeline(t *testing.T) {
	cmd := func(id string, force bool) *command {
		return &command{Type: "webview2_install", ID: id, Payload: map[string]interface{}{"source": "microsoft", "force": force}}
	}

	t.Run("installed", func(t *testing.T) {
		f, g := installWV2Fakes(t, true)
		f.installOn = func() { f.mu.Lock(); f.installed = true; f.mu.Unlock() }
		requestWebView2Install(cmd("auto_1", false))
		st := g.snapshot()
		if f.launches != 1 || st.LastOutcome != "installed" || st.Pending != nil || f.closed != 1 {
			t.Fatalf("launches=%d closed=%d state=%+v", f.launches, f.closed, st)
		}
		if s := g.status(true); s.State != wv2StateOK {
			t.Fatalf("status after install: %+v", s)
		}
		// Already installed: nothing launched again.
		requestWebView2Install(cmd("auto_2", true))
		if f.launches != 1 {
			t.Fatal("an installed runtime must not be reinstalled")
		}
	})

	t.Run("non-zero exit but detected", func(t *testing.T) {
		f, g := installWV2Fakes(t, true)
		f.exitCode = 0x80040C01
		f.installOn = func() { f.mu.Lock(); f.installed = true; f.mu.Unlock() }
		requestWebView2Install(cmd("a", false))
		if st := g.snapshot(); st.LastOutcome != "installed" {
			t.Fatalf("decided on detection, not the exit code: %+v", st)
		}
	})

	t.Run("network failure", func(t *testing.T) {
		f, g := installWV2Fakes(t, true)
		f.exitCode = 0x80072EFD
		requestWebView2Install(cmd("a", false))
		st := g.snapshot()
		if st.LastOutcome != "failed" || st.LastReason != "network" || st.Failures != 1 || st.NextAttemptAt.IsZero() {
			t.Fatalf("state: %+v", st)
		}
		if f.detects < 5 {
			t.Fatalf("detection must be polled after the installer exits (%d reads)", f.detects)
		}
		// Automatic retry during the back-off: skipped; forced (after 1 min): tried.
		requestWebView2Install(cmd("b", false))
		if f.launches != 1 {
			t.Fatal("automatic attempt during the back-off")
		}
		webview2Sleep(webview2MinGap)
		requestWebView2Install(cmd("c", true))
		if f.launches != 2 {
			t.Fatal("forced attempt must run")
		}
	})

	t.Run("exit 0 not detected", func(t *testing.T) {
		f, g := installWV2Fakes(t, true)
		requestWebView2Install(cmd("a", false))
		if st := g.snapshot(); st.LastReason != "not_detected" || f.launches != 1 {
			t.Fatalf("state: %+v", st)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		f, g := installWV2Fakes(t, true)
		f.timedOut = true
		f.exitCode = -1
		requestWebView2Install(cmd("a", false))
		if st := g.snapshot(); st.LastReason != "timeout" {
			t.Fatalf("state: %+v", st)
		}
	})

	t.Run("signature refused", func(t *testing.T) {
		f, g := installWV2Fakes(t, true)
		f.verifyErr = errors.New("signed by \"Evil\"")
		requestWebView2Install(cmd("a", false))
		st := g.snapshot()
		if f.launches != 0 || st.LastReason != "signature" || !strings.Contains(st.LastError, "Evil") {
			t.Fatalf("a badly signed bootstrapper must never run: launches=%d %+v", f.launches, st)
		}
	})

	t.Run("bootstrapper missing", func(t *testing.T) {
		f, g := installWV2Fakes(t, false)
		requestWebView2Install(cmd("a", false))
		if st := g.snapshot(); f.launches != 0 || st.LastReason != "bootstrapper_missing" {
			t.Fatalf("state: %+v", st)
		}
		if s := g.status(webview2HasInstaller()); s.Installer {
			t.Fatal("status must say the bootstrapper is not shipped")
		}
	})

	t.Run("other source ignored", func(t *testing.T) {
		f, _ := installWV2Fakes(t, true)
		requestWebView2Install(&command{Type: "webview2_install", ID: "x", Payload: map[string]interface{}{"source": "https://evil"}})
		if f.launches != 0 {
			t.Fatal("only the shipped bootstrapper is ever run")
		}
	})
}

func TestWebView2OnStartAdoptsRunningInstaller(t *testing.T) {
	f, g := installWV2Fakes(t, true)
	g.noteDetection(missing(webview2Now()))
	d := g.begin("auto_1", "microsoft", false)
	d.Attempt.PID = 4242
	g.launched(d.Attempt)
	g.inFlight = nil // the process that launched it is gone

	polls := 0
	webview2AliveFn = func(pid int) bool {
		if pid != 4242 {
			t.Fatalf("alive(%d)", pid)
		}
		polls++
		if polls == 3 {
			f.mu.Lock()
			f.installed = true
			f.mu.Unlock()
		}
		return polls < 4
	}
	webview2OnStart()
	if f.launches != 0 {
		t.Fatal("a restart must never launch a second installer")
	}
	st := g.snapshot()
	if st.Pending != nil || st.LastOutcome != "installed" {
		t.Fatalf("adopted attempt must be concluded from detection: %+v", st)
	}
}

func TestWebView2ExitReason(t *testing.T) {
	for code, want := range map[int64]string{
		0x80072EFD: "network", 0x80072EE7: "network", 0x80070005: "access_denied",
		0x800704C7: "cancelled", 0x80040C01: "installer_failed", 1: "exit_code",
	} {
		if got := webview2ExitReason(code); got != want {
			t.Fatalf("0x%X → %s, want %s", code, got, want)
		}
	}
}

func TestWebView2HeartbeatRequestedOnChange(t *testing.T) {
	f, _ := installWV2Fakes(t, true)
	// Drain a pending request.
	select {
	case <-heartbeatNowCh:
	default:
	}
	if s := webview2HeartbeatStatus(); s == nil || s.State != wv2StateMissing || !s.Installer {
		t.Fatalf("heartbeat block: %+v", s)
	}
	select {
	case <-heartbeatNowCh:
	default:
	}
	f.mu.Lock()
	f.installed = true
	f.mu.Unlock()
	webview2Refresh()
	select {
	case <-heartbeatNowCh:
	default:
		t.Fatal("a state change must request an immediate heartbeat")
	}
	webview2Refresh()
	select {
	case <-heartbeatNowCh:
		t.Fatal("no change: no extra heartbeat")
	default:
	}
}

func TestWebView2HeartbeatJSON(t *testing.T) {
	s := &webview2Status{State: wv2StateFailed, Scope: "none", Reason: "network", Installer: true, Attempts: 2,
		LastError: "network (installer exit code 0x80072EFD)", NextAttemptAt: "2026-09-29T10:30:00Z"}
	b, _ := json.Marshal(cmdHeartbeatMsg{Type: "heartbeat", WebView2: s})
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	w, ok := m["webview2"].(map[string]interface{})
	if !ok || w["state"] != "failed" || w["installer"] != true || w["attempts"].(float64) != 2 {
		t.Fatalf("heartbeat JSON: %s", b)
	}
	b2, _ := json.Marshal(cmdHeartbeatMsg{Type: "heartbeat"})
	if strings.Contains(string(b2), "webview2") {
		t.Fatalf("no block off Windows: %s", b2)
	}
}

// webview2ChatCheck: runtime missing per machine → no chat window, unless the
// helper's loader may accept something else (BrowserExecutableFolder policy,
// Microsoft Edge Beta / Dev / Canary, a signed-in user's own install): then
// the chat is tried and its handshake reports honestly.
func TestWebView2ChatCheckAlternatives(t *testing.T) {
	f, _ := installWV2Fakes(t, true)
	savedAlt := webview2ChatAltFn
	t.Cleanup(func() { webview2ChatAltFn = savedAlt })
	alt := ""
	webview2ChatAltFn = func() string { return alt }

	if ok, st := webview2ChatCheck(); ok || st == nil || st.State != wv2StateMissing {
		t.Fatalf("missing, no alternative: ok=%v st=%+v", ok, st)
	}
	alt = "Microsoft Edge Beta 131.0.2903.9"
	if ok, _ := webview2ChatCheck(); !ok {
		t.Fatal("an alternative the loader accepts: the chat is tried")
	}
	alt = ""
	f.mu.Lock()
	f.installed = true
	f.mu.Unlock()
	webview2ChatAltFn = func() string { t.Fatal("not asked when the runtime is installed"); return "" }
	if ok, st := webview2ChatCheck(); !ok || st == nil || st.State != wv2StateOK {
		t.Fatalf("installed: ok=%v st=%+v", ok, st)
	}
}
