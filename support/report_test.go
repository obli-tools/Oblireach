package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/oblireach/supportlink/wire"
)

// The dialog: a reason is required, the text is bounded, "End the session
// now" is on by default and only applies while the session runs, a send
// locks the form, a failure can be retried (not a refusal).
func TestReportDialogStateMachine(t *testing.T) {
	d := NewReportDialog(true)
	if d.Step != ReportEditing || !d.EndSession || !d.SessionRunning || d.CanSubmit() {
		t.Fatalf("new dialog %+v", d)
	}
	if _, ok := d.Submit(); ok {
		t.Fatal("submitted without a reason")
	}
	d.SetReason("spam")
	if d.Reason != "" {
		t.Fatal("invalid reason kept")
	}
	d.SetReason(wire.ReportScam)
	d.SetText(strings.Repeat("é", wire.ReportMaxText+10))
	if len([]rune(d.Text)) != wire.ReportMaxText {
		t.Fatalf("text not bounded: %d", len([]rune(d.Text)))
	}
	d.SetText("  He wanted my card\x07 number  ")
	d.ToggleEnd()
	if d.EndSession {
		t.Fatal("toggle")
	}
	d.ToggleEnd()
	f, ok := d.Submit()
	if !ok || f.Reason != wire.ReportScam || f.Text != "He wanted my card number" || !f.EndSession || d.Step != ReportSending {
		t.Fatalf("submit %+v %+v", f, d)
	}
	// Locked while sending.
	d.SetReason(wire.ReportOther)
	d.SetText("changed")
	d.ToggleEnd()
	if d.Reason != wire.ReportScam || d.Text == "changed" || !d.EndSession || d.CanSubmit() {
		t.Fatalf("edited while sending: %+v", d)
	}
	if _, ok := d.Submit(); ok {
		t.Fatal("double submit")
	}
	// A network failure: retry.
	d.Finish(ReportErrUnreachable)
	if d.Step != ReportFailed || d.ErrKind != ReportErrUnreachable || !d.Retryable() || !d.CanSubmit() {
		t.Fatalf("failed %+v", d)
	}
	// The session ended meanwhile: the option no longer applies.
	d.SessionEnded()
	d.ToggleEnd()
	if f, ok := d.Submit(); !ok || f.EndSession {
		t.Fatalf("resubmit after the end %+v %v", f, ok)
	}
	d.Finish("")
	if d.Step != ReportSent {
		t.Fatalf("sent %+v", d)
	}
	d.Finish(ReportErrUnreachable) // a late result changes nothing
	if d.Step != ReportSent {
		t.Fatal("finish after sent")
	}

	// A refusal is final.
	r := NewReportDialog(false)
	if r.EndSession && r.SessionRunning {
		t.Fatal("ended session dialog")
	}
	r.SetReason(wire.ReportOther)
	if f, ok := r.Submit(); !ok || f.EndSession {
		t.Fatalf("end-screen submit %+v", f)
	}
	r.Finish(ReportErrRefused)
	if r.Retryable() || r.CanSubmit() {
		t.Fatal("a refused report can be retried")
	}
}

func TestReportErrKinds(t *testing.T) {
	for err, want := range map[error]string{
		nil: "", ErrReportUnavailable: ReportErrUnavailable,
		fmt.Errorf("%w: x", ErrReportRefused):     ReportErrRefused,
		fmt.Errorf("%w: x", ErrReportRateLimited): ReportErrRateLimited,
		fmt.Errorf("%w: x", ErrReportUnreachable): ReportErrUnreachable,
		errors.New("anything else"):               ReportErrUnreachable,
	} {
		if got := reportErrKind(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
	// Every reason has a label.
	for _, r := range reportReasons {
		if reportReasonKeys[r] == "" || !wire.ValidReportReason(r) {
			t.Errorf("reason %q", r)
		}
	}
}

// startSession runs the harness up to a running session.
func startSession(t *testing.T, h *harness, sid string) *fakeSession {
	t.Helper()
	host := h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)
	sessCh := make(chan *fakeSession, 1)
	go func() { s, _ := host.operator(context.Background(), sid, nil); sessCh <- s }()
	h.v.waitPhase(t, PhaseSession, nil)
	<-h.b.runs
	return <-sessCh
}

func waitTrue(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func waitKind(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case k := <-ch:
		return k
	case <-time.After(5 * time.Second):
		t.Fatal("no report result")
		return ""
	}
}

// Reporting during the session with "End the session now": the session
// stops at once (ended by the person) and the report carries the session's
// token; one report per session.
func TestControllerReportDuringSession(t *testing.T) {
	h := start(t, nil)
	s := startSession(t, h, "sid-r1")
	if st := h.c.State(); !st.Report.Available || !st.Report.Until.IsZero() || !st.Report.Open(time.Now()) {
		t.Fatalf("report info during the session %+v", st.Report)
	}
	kinds := make(chan string, 1)
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportScam, Text: "card", EndSession: true}, func(k string) { kinds <- k }); err != nil {
		t.Fatal(err)
	}
	end := h.v.waitPhase(t, PhaseEnded, nil)
	if end.Ended.Reason != EndLocal || !isClosed(s.done) {
		t.Fatalf("the session did not stop: %+v", end.Ended)
	}
	r := <-h.b.reports
	if r.SID != "sid-r1" || r.Token != "rpt1.sid-r1" || r.Reason != wire.ReportScam || r.Text != "card" || !r.EndSession {
		t.Fatalf("request %+v", r)
	}
	if k := waitKind(t, kinds); k != "" {
		t.Fatalf("result %q", k)
	}
	// The result and the end of the session race: read the final state.
	waitTrue(t, func() bool { st := h.c.State(); return st.Phase == PhaseEnded && st.Report.Sent })
	if h.c.State().Report.Open(time.Now()) {
		t.Fatal("still offered once sent")
	}
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportOther}, nil); !errors.Is(err, ErrReportUnavailable) {
		t.Fatalf("second report: %v", err)
	}
	// Leaving the end screen forgets everything.
	h.c.Acknowledge()
	st := h.v.waitPhase(t, PhaseWaiting, nil)
	if st.Report != (ReportInfo{}) {
		t.Fatalf("report info kept %+v", st.Report)
	}
}

// Reporting without ending the session keeps it running.
func TestControllerReportKeepSession(t *testing.T) {
	h := start(t, nil)
	s := startSession(t, h, "sid-r2")
	kinds := make(chan string, 1)
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportHarassment}, func(k string) { kinds <- k }); err != nil {
		t.Fatal(err)
	}
	if r := <-h.b.reports; r.EndSession {
		t.Fatal("end asked")
	}
	if k := waitKind(t, kinds); k != "" {
		t.Fatal(k)
	}
	st := h.v.waitPhase(t, PhaseSession, func(s State) bool { return s.Report.Sent })
	if isClosed(s.done) || st.Phase != PhaseSession {
		t.Fatal("the session ended")
	}
}

// On the end screen: offered for ReportWindow, then gone; a failure can be
// retried; a send in progress blocks a second one.
func TestControllerReportEndScreen(t *testing.T) {
	h := start(t, func(h *harness) { h.c.ReportWindow = 400 * time.Millisecond })
	s := startSession(t, h, "sid-r3")
	s.end(io.EOF) // the operator left
	end := h.v.waitPhase(t, PhaseEnded, nil)
	if !end.Report.Available || end.Report.Until.IsZero() || !end.Report.Open(time.Now()) {
		t.Fatalf("end screen report %+v", end.Report)
	}
	// A failure, then a retry while held, then success.
	h.b.mu.Lock()
	h.b.reportErr = fmt.Errorf("%w: offline", ErrReportUnreachable)
	h.b.mu.Unlock()
	kinds := make(chan string, 2)
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportUnwanted, EndSession: true}, func(k string) { kinds <- k }); err != nil {
		t.Fatal(err)
	}
	if r := <-h.b.reports; r.SID != "sid-r3" {
		t.Fatal(r)
	}
	if k := waitKind(t, kinds); k != ReportErrUnreachable {
		t.Fatalf("kind %q", k)
	}
	if h.c.State().Report.Sent {
		t.Fatal("marked sent after a failure")
	}
	hold := make(chan struct{})
	h.b.mu.Lock()
	h.b.reportErr, h.b.reportHold = nil, hold
	h.b.mu.Unlock()
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportUnwanted}, func(k string) { kinds <- k }); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportUnwanted}, nil); !errors.Is(err, ErrReportUnavailable) {
		t.Fatalf("second send while the first runs: %v", err)
	}
	close(hold)
	<-h.b.reports
	if k := waitKind(t, kinds); k != "" {
		t.Fatal(k)
	}
	h.v.waitPhase(t, PhaseEnded, func(s State) bool { return s.Report.Sent })

	// The next session: the window closes after ReportWindow.
	h.c.Acknowledge()
	s = startSession(t, h, "sid-r4")
	s.end(io.EOF)
	h.v.waitPhase(t, PhaseEnded, nil)
	time.Sleep(500 * time.Millisecond)
	if h.c.State().Report.Open(time.Now()) {
		t.Fatal("still offered after the window")
	}
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportScam}, nil); !errors.Is(err, ErrReportUnavailable) {
		t.Fatalf("after the window: %v", err)
	}
}

// A server without reports: no token, nothing offered.
func TestControllerReportUnavailable(t *testing.T) {
	h := start(t, nil)
	host := h.b.nextHost(t)
	host.noToken = true
	h.v.waitPhase(t, PhaseWaiting, nil)
	go func() { _, _ = host.operator(context.Background(), "sid-r5", nil) }()
	st := h.v.waitPhase(t, PhaseSession, nil)
	<-h.b.runs
	if st.Report.Available || st.Report.Open(time.Now()) {
		t.Fatalf("offered without a token: %+v", st.Report)
	}
	if err := h.c.SubmitReport(ReportForm{Reason: wire.ReportScam, EndSession: true}, nil); !errors.Is(err, ErrReportUnavailable) {
		t.Fatalf("report without a token: %v", err)
	}
	if h.c.State().Phase != PhaseSession {
		t.Fatal("a refused report ended the session")
	}
}

// The new texts exist in the 18 catalogs, translated (not left in English).
func TestReportCatalogs(t *testing.T) {
	keys := []string{"report.open", "report.short", "report.title", "report.intro", "report.who", "report.reason",
		"report.details", "report.endNow", "report.send", "report.sending", "report.sent.title", "report.sent.body",
		"report.advice.title", "report.advice", "report.close", "report.retry", "report.done", "report.err.unreachable",
		"report.err.rateLimited", "report.err.refused", "report.err.unavailable", "common.cancel"}
	for _, k := range reportReasonKeys {
		keys = append(keys, k)
	}
	for _, l := range languages {
		for _, k := range keys {
			v := catalogs[l][k]
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: %q missing", l, k)
				continue
			}
			if l != "en" && v == catalogs["en"][k] {
				t.Errorf("%s: %q not translated: %q", l, k, v)
			}
		}
	}
}
