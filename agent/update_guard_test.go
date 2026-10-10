package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestGuard(t *testing.T, statePath string) (*selfUpdateGuard, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)}
	g := newSelfUpdateGuard(func() string { return statePath })
	g.now = clk.now
	return g, clk
}

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		target, current string
		want            bool
	}{
		{"1.0.200", "1.0.199", true},
		{"1.0.199", "1.0.199", false},
		{"1.0.198", "1.0.199", false},
		{"1.1.0", "1.0.999", true},
		{"2.0", "1.9.9", true},
		{"1.0.10", "1.0.9", true}, // numeric, not lexical
		{"v1.0.200", "1.0.199", true},
		{"1.0.200", "1.0.200-rc1", false},
		{"1.0.200.1", "1.0.200", true},
		{"1.0.200", "1.0.200.0", false},
		{"1.0.200", "dev", true}, // dev build follows the server
		{"dev", "dev", false},
		{"garbage", "1.0.199", true}, // unknown format: only identical is skipped
	}
	for _, c := range cases {
		if got := isNewerVersion(c.target, c.current); got != c.want {
			t.Errorf("isNewerVersion(%q, %q) = %v, want %v", c.target, c.current, got, c.want)
		}
	}
}

func TestSelfUpdateGuardSingleInFlight(t *testing.T) {
	g, clk := newTestGuard(t, "")
	d := g.begin("a1", "1.0.199", "1.0.200")
	if !d.OK || d.Attempt == nil {
		t.Fatalf("first begin refused: %+v", d)
	}
	// The server repeats the command on every heartbeat (30 s).
	for i := 0; i < 5; i++ {
		clk.advance(30 * time.Second)
		d2 := g.begin("a2", "1.0.199", "1.0.200")
		if d2.OK || d2.Reason != "in_progress" {
			t.Fatalf("begin #%d while in flight = %+v, want in_progress", i+2, d2)
		}
		if i == 0 && !d2.Log {
			t.Fatalf("first in_progress skip must be logged")
		}
		if i > 0 && d2.Log {
			t.Fatalf("repeated in_progress skip #%d must stay quiet", i+2)
		}
	}
	// Even another target waits for the attempt in flight.
	if d3 := g.begin("a3", "1.0.199", "1.0.201"); d3.OK || d3.Reason != "in_progress" {
		t.Fatalf("other target while in flight = %+v", d3)
	}
	// After 10 min the reminder is logged again.
	clk.advance(selfUpdateSkipLogEvery)
	if d4 := g.begin("a4", "1.0.199", "1.0.200"); d4.OK || !d4.Log {
		t.Fatalf("in_progress reminder after 10 min = %+v", d4)
	}
}

func TestSelfUpdateGuardFailureLadder(t *testing.T) {
	g, clk := newTestGuard(t, "")
	want := []time.Duration{2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 60 * time.Minute, 60 * time.Minute}
	for i, step := range want {
		d := g.begin("f", "1.0.199", "1.0.200")
		if !d.OK {
			t.Fatalf("attempt %d refused: %+v", i+1, d)
		}
		start := clk.now()
		next := g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "download", Code: -1, Detail: "HTTP 500"})
		if got := next.Sub(start); got != step {
			t.Fatalf("attempt %d: next attempt after %s, want %s", i+1, got, step)
		}
		clk.advance(step - time.Second)
		if d := g.begin("f", "1.0.199", "1.0.200"); d.OK || d.Reason != "cooldown" {
			t.Fatalf("attempt %d: begin 1 s before the end of the back-off = %+v", i+1, d)
		}
		clk.advance(time.Second)
	}
	if st := g.snapshot(); st.Failures != len(want) || !strings.Contains(st.LastError, "HTTP 500") {
		t.Fatalf("state after the ladder = %+v", st)
	}
}

func TestSelfUpdateGuardBusyLadderShort(t *testing.T) {
	g, clk := newTestGuard(t, "")
	want := []time.Duration{2 * time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, step := range want {
		d := g.begin("b", "1.0.199", "1.0.200")
		if !d.OK {
			t.Fatalf("attempt %d refused: %+v", i+1, d)
		}
		start := clk.now()
		next := g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeBusy, Stage: "precheck", Code: -1})
		if got := next.Sub(start); got != step {
			t.Fatalf("busy %d: next attempt after %s, want %s", i+1, got, step)
		}
		clk.advance(step)
	}
	st := g.snapshot()
	if st.Failures != 0 || st.BusyRetries != len(want) {
		t.Fatalf("busy must not count as a failure: %+v", st)
	}
}

func TestSelfUpdateGuardMinimumGap(t *testing.T) {
	g, clk := newTestGuard(t, "")
	d := g.begin("m1", "1.0.199", "1.0.200")
	g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeInstalled, Code: 0})
	clk.advance(90 * time.Second)
	if d := g.begin("m2", "1.0.199", "1.0.201"); d.OK || d.Reason != "cooldown" {
		t.Fatalf("begin 90 s after a start = %+v, want cooldown", d)
	}
	clk.advance(30 * time.Second)
	if d := g.begin("m3", "1.0.199", "1.0.201"); !d.OK {
		t.Fatalf("begin 2 min after a start refused: %+v", d)
	}
}

func TestSelfUpdateGuardCooldownLoggedOncePerWindow(t *testing.T) {
	g, clk := newTestGuard(t, "")
	d := g.begin("c1", "1.0.199", "1.0.200")
	g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "download", Code: -1})
	logged := 0
	for i := 0; i < 3; i++ { // 1.5 min of heartbeats inside the 2-min window
		clk.advance(30 * time.Second)
		if d := g.begin("c", "1.0.199", "1.0.200"); !d.OK && d.Log {
			logged++
		}
	}
	if logged != 1 {
		t.Fatalf("cooldown skip logged %d times in one window, want 1", logged)
	}
}

func TestSelfUpdateGuardNewTargetResetsBackoff(t *testing.T) {
	g, clk := newTestGuard(t, "")
	for i := 0; i < 4; i++ { // 2+5+15+30
		d := g.begin("n", "1.0.199", "1.0.200")
		if !d.OK {
			t.Fatalf("attempt %d refused: %+v", i+1, d)
		}
		next := g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "msiexec", Code: 1603})
		if i < 3 {
			clk.t = next
		}
	}
	clk.advance(3 * time.Minute) // 27 min left on 1.0.200's back-off
	if d := g.begin("n", "1.0.199", "1.0.200"); d.OK {
		t.Fatalf("same target must still wait")
	}
	d := g.begin("n2", "1.0.199", "1.0.201")
	if !d.OK {
		t.Fatalf("a new target must not inherit the back-off: %+v", d)
	}
	if st := g.snapshot(); st.Failures != 0 || st.Target != "1.0.201" {
		t.Fatalf("counters not reset for the new target: %+v", st)
	}
}

func TestSelfUpdateGuardNotNewer(t *testing.T) {
	g, _ := newTestGuard(t, "")
	for _, target := range []string{"1.0.199", "1.0.198"} {
		d := g.begin("manual_update_1", "1.0.199", target)
		if d.OK || d.Reason != "not_newer" {
			t.Fatalf("target %s on 1.0.199 = %+v, want not_newer", target, d)
		}
	}
	if st := g.snapshot(); !st.LastStartAt.IsZero() {
		t.Fatalf("a refused command must not count as an attempt: %+v", st)
	}
}

func TestSelfUpdateGuardPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-state.json")

	// Process 1: msiexec launched, then the installer stops the service.
	g1, clk := newTestGuard(t, path)
	d := g1.begin("p1", "1.0.199", "1.0.200")
	att := *d.Attempt
	att.MSI, att.Log, att.PID = `C:\x\oblireach-update-1.msi`, `C:\x\oblireach-update-1.log`, 4242
	if err := g1.launched(&att); err != nil {
		t.Fatal(err)
	}

	// Process 2 (rolled-back old version): the heartbeat's command arrives
	// before selfUpdateOnStart adopted the pending attempt.
	g2 := newSelfUpdateGuard(func() string { return path })
	clk.advance(40 * time.Second)
	g2.now = clk.now
	if d := g2.begin("p2", "1.0.199", "1.0.200"); d.OK || d.Reason != "in_progress" {
		t.Fatalf("pending attempt of the previous process must block: %+v", d)
	}
	pend := g2.adoptPending()
	if pend == nil || pend.ID != "p1" || pend.PID != 4242 || pend.Log != att.Log {
		t.Fatalf("adoptPending = %+v", pend)
	}
	next := g2.finish(pend, concludePending(pend, "1.0.199", 1603, true))
	if want := clk.now().Add(2 * time.Minute); !next.Equal(want) {
		t.Fatalf("next after a failed msiexec = %s, want %s", next, want)
	}
	st := g2.snapshot()
	if st.Pending != nil || st.Failures != 1 || !strings.Contains(st.LastError, "1603") {
		t.Fatalf("state after conclusion = %+v", st)
	}

	// Process 3: the back-off survives another restart.
	g3 := newSelfUpdateGuard(func() string { return path })
	clk.advance(time.Minute)
	g3.now = clk.now
	if d := g3.begin("p3", "1.0.199", "1.0.200"); d.OK || d.Reason != "cooldown" {
		t.Fatalf("back-off lost across restart: %+v", d)
	}
	if g3.adoptPending() != nil {
		t.Fatalf("no pending attempt expected")
	}
}

func TestSelfUpdateGuardStaleInFlight(t *testing.T) {
	g, clk := newTestGuard(t, "")
	d := g.begin("s1", "1.0.199", "1.0.200")
	clk.advance(selfUpdateStaleInFlight + time.Second)
	d2 := g.begin("s2", "1.0.199", "1.0.200")
	if !d2.OK {
		t.Fatalf("stuck attempt must not block forever: %+v", d2)
	}
	// The late finish of the stuck attempt must not end the new one.
	g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "msiexec", Code: 1603})
	if cur := g.inFlightAttempt(); cur == nil || cur.ID != "s2" {
		t.Fatalf("in flight after a stale finish = %+v", cur)
	}
	if st := g.snapshot(); st.Failures != 0 {
		t.Fatalf("stale finish counted: %+v", st)
	}
}

func TestSelfUpdateGuardUnsupportedQuiet(t *testing.T) {
	g, clk := newTestGuard(t, "")
	d := g.begin("u1", "1.0.199", "1.0.200")
	next := g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeUnsupported, Code: -1})
	if next.Sub(clk.now()) != selfUpdateUnsupportedCooldown {
		t.Fatalf("unsupported cooldown = %s", next.Sub(clk.now()))
	}
	logged := 0
	for i := 0; i < 2*60*6; i++ { // 6 h of heartbeats
		clk.advance(30 * time.Second)
		if d := g.begin("u", "1.0.199", "1.0.200"); d.OK {
			break
		} else if d.Log {
			logged++
		}
	}
	if logged > 1 {
		t.Fatalf("unsupported platform logged %d skips in 6 h", logged)
	}
}

func TestConcludePending(t *testing.T) {
	att := &selfUpdateAttempt{ID: "x", From: "1.0.199", Target: "1.0.200"}
	cases := []struct {
		name    string
		running string
		status  int
		found   bool
		want    selfUpdateOutcome
	}{
		{"new version runs", "1.0.200", 0, true, outcomeInstalled},
		{"new version runs, log missing", "1.0.200", 0, false, outcomeInstalled},
		{"newer than target (installed by the Obliance agent)", "1.0.201", 0, false, outcomeInstalled},
		{"installer busy", "1.0.199", 1618, true, outcomeBusy},
		{"rolled back", "1.0.199", 1603, true, outcomeFailed},
		{"corrupt package", "1.0.199", 1620, true, outcomeFailed},
		{"ok but not replaced", "1.0.199", 0, true, outcomeNoRestart},
		{"reboot required", "1.0.199", 3010, true, outcomeNoRestart},
		{"unknown", "1.0.199", 0, false, outcomeFailed},
	}
	for _, c := range cases {
		if got := concludePending(att, c.running, c.status, c.found); got.Outcome != c.want {
			t.Errorf("%s: outcome %s, want %s (%+v)", c.name, got.Outcome, c.want, got)
		}
	}
}

func utf16LEWithBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func TestParseMSILog(t *testing.T) {
	full := "=== Verbose logging started: 29/09/2026  02:09:49  Build type: SHIP UNICODE 5.00.10011.00 ===\r\n" +
		"MSI (s) (94:EC) [02:10:01:100]: Product: Oblireach Agent -- Installation completed successfully.\r\n" +
		"MSI (s) (94:EC) [02:10:01:100]: Windows Installer installed the product. Product Name: Oblireach Agent. Product Version: 1.0.200. Installation success or error status: 0.\r\n" +
		"MSI (s) (94:EC) [02:10:01:200]: MainEngineThread is returning 0\r\n" +
		"MSI (c) (68:48) [02:10:01:300]: MainEngineThread is returning 0\r\n" +
		"=== Verbose logging stopped: 29/09/2026  02:10:01 ===\r\n"
	if st, found, done := parseMSILog(utf16LEWithBOM(full)); st != 0 || !found || !done {
		t.Fatalf("UTF-16 success log: status=%d found=%v done=%v", st, found, done)
	}

	busy := "=== Verbose logging started ===\r\nMSI (c) (68:48) [02:10:01:300]: MainEngineThread is returning 1618\r\n=== Verbose logging stopped ===\r\n"
	if st, found, done := parseMSILog([]byte(busy)); st != 1618 || !found || !done {
		t.Fatalf("ANSI busy log: status=%d found=%v done=%v", st, found, done)
	}

	failed := strings.Replace(full, "status: 0.", "status: 1603.", 1)
	failed = strings.Replace(failed, "returning 0", "returning 1603", -1)
	if st, found, _ := parseMSILog(utf16LEWithBOM(failed)); st != 1603 || !found {
		t.Fatalf("failed log: status=%d found=%v", st, found)
	}

	// msiexec still running (the new service starts before msiexec ends).
	running := "=== Verbose logging started ===\r\nMSI (s) (94:EC) [02:10:00:000]: Doing action: StartServices\r\n"
	if _, found, done := parseMSILog(utf16LEWithBOM(running)); found || done {
		t.Fatalf("incomplete log: found=%v done=%v", found, done)
	}
	if _, found, done := parseMSILog(nil); found || done {
		t.Fatalf("empty log: found=%v done=%v", found, done)
	}
}

func TestIsOwnUpdateFile(t *testing.T) {
	dir := t.TempDir()
	ok := []string{
		filepath.Join(dir, "oblireach-update-123456.msi"),
		filepath.Join(dir, "oblireach-update-123456.log"),
		filepath.Join(dir, "oblireach-update.msi"),
	}
	for _, p := range ok {
		if !isOwnUpdateFile(p, dir) {
			t.Errorf("%s rejected", p)
		}
	}
	bad := []string{
		"",
		"oblireach-update-1.msi", // relative
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "sub", "oblireach-update-1.msi"),
		filepath.Join(filepath.Dir(dir), "oblireach-update-1.msi"),
		filepath.Join(dir, "..", "oblireach-update-1.msi"),
		filepath.Join(dir, "oblireach-update-1.exe"),
	}
	for _, p := range bad {
		if isOwnUpdateFile(p, dir) {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestCleanupSelfUpdateFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldMSI := write("oblireach-update-1.msi", time.Hour)
	legacyMSI := write("oblireach-update.msi", 48*time.Hour)
	keptMSI := write("oblireach-update-2.msi", time.Hour)
	freshMSI := write("oblireach-update-3.msi", time.Minute) // a download in progress
	unrelated := write("something-else.msi", 30*24*time.Hour)
	var logs []string
	for i := 0; i < 7; i++ {
		logs = append(logs, write("oblireach-update-1"+string(rune('0'+i))+".log", time.Duration(i+1)*time.Hour))
	}
	ancient := write("oblireach-update-99.log", 20*24*time.Hour)

	cleanupSelfUpdateFiles(dir, map[string]bool{keptMSI: true}, now)

	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	for _, p := range []string{oldMSI, legacyMSI, ancient} {
		if exists(p) {
			t.Errorf("%s not removed", filepath.Base(p))
		}
	}
	for _, p := range []string{keptMSI, freshMSI, unrelated} {
		if !exists(p) {
			t.Errorf("%s removed", filepath.Base(p))
		}
	}
	for i, p := range logs {
		if want := i < selfUpdateKeepLogs; exists(p) != want {
			t.Errorf("log %d (%s): exists=%v, want %v", i, filepath.Base(p), exists(p), want)
		}
	}
}

func TestSelfUpdateGuardClockWentBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-state.json")
	g1, clk := newTestGuard(t, path)
	d := g1.begin("k1", "1.0.199", "1.0.200")
	g1.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "download", Code: -1})

	g2 := newSelfUpdateGuard(func() string { return path })
	back := clk.now().Add(-72 * time.Hour) // clock reset, then restart
	g2.now = func() time.Time { return back }
	if d := g2.begin("k2", "1.0.199", "1.0.200"); !d.OK {
		t.Fatalf("a back-off computed with a clock 3 days ahead must not block: %+v", d)
	}
}

// The "Update Reach" button (manual_update_*) retries at once after a
// failure: the back-off does not apply to it; the minimum gap and one attempt
// at a time still do. Automatic commands keep the back-off.
func TestSelfUpdateGuardManualBypassesBackoff(t *testing.T) {
	g, clk := newTestGuard(t, "")
	for i := 0; i < 3; i++ { // back-off now 15 min
		d := g.begin("auto_update_ws_1", "1.0.199", "1.0.200")
		if !d.OK {
			t.Fatalf("attempt %d refused: %+v", i+1, d)
		}
		g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "msiexec", Code: 1603})
		if i < 2 {
			clk.advance(backoffStep(selfUpdateFailureSteps, i+1))
		}
	}
	clk.advance(3 * time.Minute) // inside the 15-min back-off, past the 2-min gap
	if d := g.begin("auto_update_ws_2", "1.0.199", "1.0.200"); d.OK || d.Reason != "cooldown" {
		t.Fatalf("automatic command inside the back-off = %+v, want cooldown", d)
	}
	if !isManualUpdateID("manual_update_1727600000000") || isManualUpdateID("auto_update_1") {
		t.Fatalf("isManualUpdateID")
	}
	d := g.begin("manual_update_1", "1.0.199", "1.0.200")
	if !d.OK {
		t.Fatalf("manual command inside the back-off refused: %+v", d)
	}
	// One at a time still applies to it.
	if d2 := g.begin("manual_update_2", "1.0.199", "1.0.200"); d2.OK || d2.Reason != "in_progress" {
		t.Fatalf("second manual command while in flight = %+v", d2)
	}
	g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "signature", Code: -1, Detail: "refused"})
	// … and the minimum gap too.
	clk.advance(time.Minute)
	if d3 := g.begin("manual_update_3", "1.0.199", "1.0.200"); d3.OK || d3.Reason != "cooldown" {
		t.Fatalf("manual command 1 min after a start = %+v, want cooldown (minimum gap)", d3)
	}
	clk.advance(time.Minute)
	if d4 := g.begin("manual_update_4", "1.0.199", "1.0.200"); !d4.OK {
		t.Fatalf("manual command after the minimum gap refused: %+v", d4)
	}
}

// The heartbeat's self-update block: nothing before any attempt; in progress
// with its target; after a failure the outcome, error and next automatic
// attempt; after a success no next attempt.
func TestSelfUpdateGuardStatus(t *testing.T) {
	g, clk := newTestGuard(t, filepath.Join(t.TempDir(), "update-state.json"))
	if s := g.status(); s != nil {
		t.Fatalf("status before any attempt = %+v, want nil", s)
	}
	d := g.begin("a1", "1.0.199", "1.0.200")
	s := g.status()
	if s == nil || !s.InProgress || s.Target != "1.0.200" || s.Since != clk.now().UTC().Format(time.RFC3339) || s.NextAttemptAt != "" {
		t.Fatalf("status in progress = %+v", s)
	}
	clk.advance(40 * time.Second)
	g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeFailed, Stage: "signature", Code: -1, Detail: strings.Repeat("é", 400)})
	s = g.status()
	if s == nil || s.InProgress || s.LastOutcome != "failed" || s.Failures != 1 || !strings.HasPrefix(s.LastError, "failed at signature") {
		t.Fatalf("status after a failure = %+v", s)
	}
	if len(s.LastError) > selfUpdateStatusMaxText+len("…") || !strings.HasSuffix(s.LastError, "…") || !utf8.ValidString(s.LastError) {
		t.Fatalf("error not capped cleanly: %d bytes", len(s.LastError))
	}
	if next, err := time.Parse(time.RFC3339, s.NextAttemptAt); err != nil || !next.Equal(clk.now().Add(2*time.Minute)) {
		t.Fatalf("next attempt = %q", s.NextAttemptAt)
	}
	if s.LastResultAt != clk.now().UTC().Format(time.RFC3339) {
		t.Fatalf("lastResultAt = %q", s.LastResultAt)
	}
	// The state survives a restart (the outcome is persisted).
	g2, clk2 := newTestGuard(t, g.path())
	clk2.t = clk.now()
	if s2 := g2.status(); s2 == nil || s2.LastOutcome != "failed" || s2.NextAttemptAt == "" {
		t.Fatalf("status after a restart = %+v", s2)
	}
	// Pending attempt of the previous process: in progress.
	clk.advance(3 * time.Minute)
	d = g.begin("a2", "1.0.199", "1.0.200")
	if err := g.launched(d.Attempt); err != nil {
		t.Fatal(err)
	}
	g3, clk3 := newTestGuard(t, g.path())
	clk3.t = clk.now().Add(20 * time.Second)
	if s3 := g3.status(); s3 == nil || !s3.InProgress || s3.Target != "1.0.200" {
		t.Fatalf("status with a pending msiexec = %+v", s3)
	}
	// Success: no next attempt, no error.
	g.finish(d.Attempt, selfUpdateResult{Outcome: outcomeInstalled, Code: 0})
	if s = g.status(); s == nil || s.LastOutcome != "installed" || s.NextAttemptAt != "" || s.LastError != "" || s.InProgress {
		t.Fatalf("status after a success = %+v", s)
	}
}
