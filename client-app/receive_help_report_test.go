package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

// helpClock: a settable clock shared with the controller's goroutines.
type helpClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *helpClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *helpClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// helpReportSession: a started session (an operator accepted with the
// given report token); its host, its session link and its engine run.
func helpReportSession(t *testing.T, h *helpHost, b *fakeHelpBackend, sid, token string) (*fakeHelpHost, *fakeHelpSess, *fakeHelpRunner) {
	t.Helper()
	host := b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)
	res := approveAsync(host, context.Background(), sid)
	waitHelpPhase(t, h, helpApproval)
	if !h.decide(sid, true, helpPerms{Control: true}) || !<-res {
		t.Fatal("not accepted")
	}
	s := newFakeHelpSess(sid)
	s.report = token
	host.ops <- s
	r := b.nextRunner(t)
	waitHelpPhase(t, h, helpSession)
	return host, s, r
}

func TestHelpReportDuringSessionEndsFirst(t *testing.T) {
	h, b, sid := newTestHelpHost(t)
	if h.ReportWindow != 10*time.Minute {
		t.Fatalf("report window %v, want 10 min", h.ReportWindow)
	}
	type call struct {
		server   string
		req      helpReportReq
		stopped  bool // the session's engine was already stopped
		deadline bool
	}
	calls := make(chan call, 4)
	var runner *fakeHelpRunner
	b.report = func(ctx context.Context, server string, r helpReportReq) error {
		_, dl := ctx.Deadline()
		calls <- call{server, r, runner.ctxErr() != nil, dl}
		return nil
	}
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	_, sess, r := helpReportSession(t, h, b, "S1", "tok-1")
	runner = r
	v := h.view(0)
	if v.Report == nil || !v.Report.Available || v.Report.Sent || v.Report.Until != 0 || v.Report.Operator != "Jane Doe" || v.Report.Account != "jane@acme" {
		t.Fatalf("report during the session: %+v", v.Report)
	}
	if kind := h.submitReport("nope", "x", true); kind != helpReportErrUnavailable {
		t.Fatalf("an unknown reason: %q", kind)
	}
	if len(calls) != 0 || sess.closed() {
		t.Fatal("an invalid report reached the backend or ended the session")
	}
	text := "  He asked for my card number\x07  "
	if kind := h.submitReport(wire.ReportScam, text, true); kind != "" {
		t.Fatalf("report: %q", kind)
	}
	c := <-calls
	if !c.stopped {
		t.Error("the report went before the session was stopped")
	}
	if !c.deadline {
		t.Error("the report has no deadline")
	}
	b.mu.Lock()
	registered := b.server
	b.mu.Unlock()
	if c.server == "" || c.server != registered {
		t.Errorf("report sent to %q, the session came through %q", c.server, registered)
	}
	want := helpReportReq{SID: "S1", Token: "tok-1", Reason: wire.ReportScam, Text: "He asked for my card number", EndSession: true}
	if c.req != want {
		t.Errorf("report %+v, want %+v", c.req, want)
	}
	v = waitHelpNotice(t, h, helpNoticeEnded)
	if v.Ended == nil || v.Ended.Reason != helpEndLocal {
		t.Errorf("end %+v, want local", v.Ended)
	}
	if v.Report == nil || !v.Report.Sent || !v.Report.Available || v.Report.Until == 0 {
		t.Errorf("report after the end: %+v", v.Report)
	}
	// One report per session.
	if kind := h.submitReport(wire.ReportOther, "", false); kind != helpReportErrUnavailable {
		t.Errorf("second report: %q", kind)
	}
	if len(calls) != 0 {
		t.Error("a second report reached the backend")
	}
	// Leaving the end notice forgets the token.
	if !h.acknowledge() {
		t.Fatal("ack")
	}
	if v := h.view(0); v.Report != nil {
		t.Errorf("report kept after the end notice: %+v", v.Report)
	}
	h.mu.Lock()
	rep := h.rep
	h.mu.Unlock()
	if rep != (helpReportTarget{}) {
		t.Errorf("token kept in memory: %+v", rep)
	}
}

func TestHelpReportWindowAndErrors(t *testing.T) {
	h, b, sid := newTestHelpHost(t)
	clk := &helpClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	h.now = clk.Now
	var mu sync.Mutex
	var got []helpReportReq
	results := []error{
		fmt.Errorf("%w: down", errHelpReportUnreachable),
		fmt.Errorf("%w: slow down", errHelpReportRateLimited),
		nil,
	}
	b.report = func(ctx context.Context, server string, r helpReportReq) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r)
		err := results[0]
		results = results[1:]
		return err
	}
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	_, _, r := helpReportSession(t, h, b, "S1", "tok-1")
	r.end <- nil // the operator left
	v := waitHelpNotice(t, h, helpNoticeEnded)
	if v.Report == nil || !v.Report.Available || v.Report.Until != clk.Now().Add(10*time.Minute).UnixMilli() {
		t.Fatalf("report after the end: %+v", v.Report)
	}
	clk.add(9*time.Minute + 59*time.Second)
	if kind := h.submitReport(wire.ReportUnwanted, "", true); kind != helpReportErrUnreachable {
		t.Fatalf("first try: %q", kind)
	}
	if v := h.view(0); v.Report == nil || !v.Report.Available || v.Report.Sent || v.Report.Sending {
		t.Fatalf("after an unreachable server (retry offered): %+v", v.Report)
	}
	if kind := h.submitReport(wire.ReportUnwanted, "", true); kind != helpReportErrRateLimited {
		t.Fatalf("second try: %q", kind)
	}
	mu.Lock()
	if len(got) != 2 || got[0].EndSession || got[1].EndSession {
		t.Errorf("reports after the end must not ask to end the session: %+v", got)
	}
	mu.Unlock()
	clk.add(time.Second) // 10 min after the end: over
	if v := h.view(0); v.Report != nil {
		t.Fatalf("report offered after the window: %+v", v.Report)
	}
	if kind := h.submitReport(wire.ReportUnwanted, "", false); kind != helpReportErrUnavailable {
		t.Fatalf("after the window: %q", kind)
	}
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 2 {
		t.Errorf("%d backend calls, want 2", n)
	}
	h.mu.Lock()
	if h.rep != (helpReportTarget{}) {
		t.Errorf("token kept after the window: %+v", h.rep)
	}
	h.mu.Unlock()
}

func TestHelpReportRefusedAndForgotten(t *testing.T) {
	h, b, sid := newTestHelpHost(t)
	var mu sync.Mutex
	calls := 0
	b.report = func(context.Context, string, helpReportReq) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return fmt.Errorf("%w: invalid_report", errHelpReportRefused)
	}
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	// A server that takes no reports: no token, nothing offered.
	host, _, r := helpReportSession(t, h, b, "S0", "")
	if v := h.view(0); v.Report != nil {
		t.Fatalf("report offered without a token: %+v", v.Report)
	}
	if kind := h.submitReport(wire.ReportScam, "", true); kind != helpReportErrUnavailable {
		t.Fatalf("no token: %q", kind)
	}
	r.end <- nil
	waitHelpNotice(t, h, helpNoticeEnded)
	if v := h.view(0); v.Report != nil {
		t.Fatalf("report offered without a token after the end: %+v", v.Report)
	}
	// With a token: refused by the server (final for the page; the
	// controller would let it try again).
	res := approveAsync(host, context.Background(), "S1")
	waitHelpPhase(t, h, helpApproval)
	h.decide("S1", true, helpPerms{})
	<-res
	s := newFakeHelpSess("S1")
	s.report = "tok-1"
	host.ops <- s
	r = b.nextRunner(t)
	waitHelpPhase(t, h, helpSession)
	if kind := h.submitReport(wire.ReportScam, "", false); kind != helpReportErrRefused {
		t.Fatalf("refused: %q", kind)
	}
	if v := h.view(0); v.Phase != helpSession || v.Report == nil || v.Report.Sent {
		t.Fatalf("a report without end: the session goes on (%s, %+v)", v.Phase, v.Report)
	}
	r.end <- nil
	waitHelpNotice(t, h, helpNoticeEnded)
	// A new operator's request clears the end notice: the token goes.
	res = approveAsync(host, context.Background(), "S2")
	waitHelpPhase(t, h, helpApproval)
	if v := h.view(0); v.Report != nil {
		t.Errorf("report kept once the end notice left: %+v", v.Report)
	}
	h.decide("S2", false, helpPerms{})
	<-res
	// Stop receiving help during a session: forgotten too.
	res = approveAsync(host, context.Background(), "S3")
	waitHelpPhase(t, h, helpApproval)
	h.decide("S3", true, helpPerms{})
	<-res
	s = newFakeHelpSess("S3")
	s.report = "tok-3"
	host.ops <- s
	b.nextRunner(t)
	waitHelpPhase(t, h, helpSession)
	h.stop()
	h.mu.Lock()
	rep := h.rep
	h.mu.Unlock()
	if rep != (helpReportTarget{}) {
		t.Errorf("token kept after stop: %+v", rep)
	}
	if v := h.view(0); v.Report != nil {
		t.Errorf("report after stop: %+v", v.Report)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("%d backend calls, want 1", calls)
	}
}

// Nothing about a report is kept in the settings (the only storage).
func TestHelpReportHTTP(t *testing.T) {
	store := &memSupportStore{}
	sh := newSupportHub(store, nil)
	b := newFakeHelpBackend()
	var mu sync.Mutex
	var got []helpReportReq
	b.report = func(ctx context.Context, server string, r helpReportReq) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r)
		if len(got) == 1 {
			return fmt.Errorf("%w: x", errHelpReportRateLimited)
		}
		return nil
	}
	sh.help = newHelpHost(sh.settings, b)
	sh.help.ApprovalTimeout = 2 * time.Second
	t.Cleanup(sh.help.stop)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sh.serveHTTP(w, r, supOrigin) }))
	defer srv.Close()
	supPost(t, srv, "servers/save", map[string]interface{}{"label": "Desk", "url": "https://desk.example.com"}, supOrigin)
	supPost(t, srv, "help/start", map[string]string{}, supOrigin)

	body := map[string]interface{}{"reason": "scam", "text": "secret words", "end": true}
	if st, out := supPost(t, srv, "help/report", body, supOrigin); st != 200 || out["ok"] != false || out["kind"] != "unavailable" {
		t.Fatalf("no session: %d %v", st, out)
	}
	_, _, r := helpReportSession(t, sh.help, b, "H1", "tok-h1")
	_, view, raw := supGet(t, srv, "help/state")
	rep, _ := view["report"].(map[string]interface{})
	if rep == nil || rep["available"] != true || rep["sent"] != false || rep["until"] != float64(0) || strings.Contains(raw, "tok-h1") {
		t.Fatalf("state during the session: %s", raw)
	}
	if st, _ := supPost(t, srv, "help/report", body, ""); st != http.StatusForbidden {
		t.Fatalf("report without the Origin: %d", st)
	}
	for _, bad := range []struct {
		body map[string]interface{}
		code string
	}{
		{map[string]interface{}{"reason": "spam", "text": ""}, "bad_reason"},
		{map[string]interface{}{"reason": "", "text": ""}, "bad_reason"},
		{map[string]interface{}{"reason": "other", "text": strings.Repeat("é", wire.ReportMaxText+1)}, "too_long"},
	} {
		if st, out := supPost(t, srv, "help/report", bad.body, supOrigin); st != 400 || out["error"] != bad.code {
			t.Errorf("%v: %d %v", bad.body["reason"], st, out)
		}
	}
	if st, out := supPost(t, srv, "help/report", body, supOrigin); st != 200 || out["ok"] != false || out["kind"] != "rateLimited" {
		t.Fatalf("rate limited: %d %v", st, out)
	}
	_ = r
	full := map[string]interface{}{"reason": "other", "text": strings.Repeat("é", wire.ReportMaxText), "end": false}
	if st, out := supPost(t, srv, "help/report", full, supOrigin); st != 200 || out["ok"] != true {
		t.Fatalf("a 1000-character report: %d %v", st, out)
	}
	if st, out := supPost(t, srv, "help/report", full, supOrigin); out["ok"] != false || out["kind"] != "unavailable" {
		t.Fatalf("a second report: %d %v", st, out)
	}
	mu.Lock()
	if len(got) != 2 || !got[0].EndSession || got[1].EndSession || got[0].Text != "secret words" || len([]rune(got[1].Text)) != wire.ReportMaxText {
		t.Errorf("reports sent %+v", got)
	}
	mu.Unlock()
	// The first report asked to end the session: it ended here.
	v := waitHelpNotice(t, sh.help, helpNoticeEnded)
	if v.Ended.Reason != helpEndLocal {
		t.Errorf("end %+v", v.Ended)
	}
	if v.Report == nil || !v.Report.Sent {
		t.Errorf("report state after the end: %+v", v.Report)
	}
	store.mu.Lock()
	saved := string(store.data)
	store.mu.Unlock()
	if saved == "" || strings.Contains(saved, "secret") || strings.Contains(saved, "tok-h1") {
		t.Errorf("report data in the settings store: %s", saved)
	}
}

// supportlink.SendReport's answers, through the production mapping, from a
// fake report endpoint (the User-Agent of Desktop's signalling).
func TestHelpSendReportAnswers(t *testing.T) {
	var mu sync.Mutex
	var bodies []wire.ReportRequest
	var agents []string
	status, answer := 200, `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wire.ReportPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var in wire.ReportRequest
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &in)
		mu.Lock()
		bodies = append(bodies, in)
		agents = append(agents, r.Header.Get("User-Agent"))
		st, a := status, answer
		mu.Unlock()
		if st == 429 {
			w.Header().Set("Retry-After", "60")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = io.WriteString(w, a)
	}))
	defer srv.Close()
	req := helpReportReq{SID: "S1", Token: "tok-1", Reason: wire.ReportHarassment, Text: "words", EndSession: true}
	for _, c := range []struct {
		status int
		answer string
		kind   string
	}{
		{200, `{"ok":true}`, ""},
		{409, `{"error":"already_reported"}`, ""},
		{429, `{"error":"rate_limited"}`, helpReportErrRateLimited},
		{403, `{"error":"invalid_report"}`, helpReportErrRefused},
		{403, `{"error":"reports_disabled"}`, helpReportErrRefused},
		{404, ``, helpReportErrRefused},
		{400, `{"error":"bad_request"}`, helpReportErrRefused},
		{503, `{"error":"unavailable"}`, helpReportErrUnreachable},
		{502, ``, helpReportErrUnreachable},
	} {
		mu.Lock()
		status, answer = c.status, c.answer
		mu.Unlock()
		err := helpSendReport(context.Background(), srv.URL, "9.9.9", req)
		if k := helpReportKind(err); k != c.kind {
			t.Errorf("%d %s: kind %q (%v), want %q", c.status, c.answer, k, err, c.kind)
		}
	}
	mu.Lock()
	if len(bodies) == 0 || bodies[0] != (wire.ReportRequest{SID: "S1", Token: "tok-1", Reason: "harassment", Text: "words", End: true}) {
		t.Errorf("request %+v", bodies)
	}
	for _, a := range agents {
		if a != "OblireachDesktop/9.9.9" {
			t.Errorf("User-Agent %q", a)
		}
	}
	mu.Unlock()
	// A server that does not answer: unreachable; an expired context too.
	url := srv.URL
	srv.Close()
	if k := helpReportKind(helpSendReport(context.Background(), url, "9.9.9", req)); k != helpReportErrUnreachable {
		t.Errorf("closed server: %q", k)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if k := helpReportKind(helpSendReport(ctx, url, "9.9.9", req)); k != helpReportErrUnreachable {
		t.Errorf("cancelled: %q", k)
	}
}

func TestHelpReportResultMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		kind string
	}{
		{nil, ""},
		{supportlink.ErrAlreadyReported, ""},
		{fmt.Errorf("x: %w", supportlink.ErrAlreadyReported), ""},
		{&supportlink.ServerError{Code: wire.ErrRateLimited, Retry: time.Minute}, helpReportErrRateLimited},
		{fmt.Errorf("%w: dial", supportlink.ErrUnreachable), helpReportErrUnreachable},
		{context.DeadlineExceeded, helpReportErrUnreachable},
		{context.Canceled, helpReportErrUnreachable},
		{supportlink.ErrReportInvalid, helpReportErrRefused},
		{supportlink.ErrReportsDisabled, helpReportErrRefused},
		{&supportlink.ServerError{Code: "http_418"}, helpReportErrRefused},
		{errors.New("something else"), helpReportErrUnreachable},
	} {
		if k := helpReportKind(helpReportResult(c.err)); k != c.kind {
			t.Errorf("%v: %q, want %q", c.err, k, c.kind)
		}
	}
	if helpReportKind(errHelpReportUnavailable) != helpReportErrUnavailable {
		t.Error("unavailable")
	}
}
