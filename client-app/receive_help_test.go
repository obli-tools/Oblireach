package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Fakes: the rendezvous registration, the session link, the engine ───────

type fakeHelpSess struct {
	sid     string
	path    string
	relayed bool
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	err     error
	report  string // the report token the server gave
}

func newFakeHelpSess(sid string) *fakeHelpSess {
	return &fakeHelpSess{sid: sid, path: "direct", done: make(chan struct{})}
}

func (s *fakeHelpSess) WriteFrame(byte, []byte) error { return nil }
func (s *fakeHelpSess) ReadFrame() (byte, []byte, error) {
	<-s.done
	return 0, nil, io.EOF
}
func (s *fakeHelpSess) Close() error { s.end(nil); return nil }
func (s *fakeHelpSess) end(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}
func (s *fakeHelpSess) SID() string { return s.sid }
func (s *fakeHelpSess) Path() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.relayed
}
func (s *fakeHelpSess) Done() <-chan struct{} { return s.done }
func (s *fakeHelpSess) ReportToken() string   { return s.report }
func (s *fakeHelpSess) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *fakeHelpSess) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// fakeHelpHost: one registration. ops feeds WaitForOperator.
type fakeHelpHost struct {
	id, code string
	hooks    helpHooks
	ops      chan interface{} // *fakeHelpSess or error
	done     chan struct{}
	once     sync.Once
}

func (h *fakeHelpHost) ID() string            { return h.id }
func (h *fakeHelpHost) Code() string          { return h.code }
func (h *fakeHelpHost) Done() <-chan struct{} { return h.done }
func (h *fakeHelpHost) Close() error          { h.once.Do(func() { close(h.done) }); return nil }
func (h *fakeHelpHost) WaitForOperator(ctx context.Context) (helpSessionLink, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.done:
		return nil, errors.New("host closed")
	case op := <-h.ops:
		if err, ok := op.(error); ok {
			return nil, err
		}
		return op.(*fakeHelpSess), nil
	}
}

// fakeHelpRunner: the engine session. end makes Run return.
type fakeHelpRunner struct {
	perms helpPerms
	ev    helpEvents
	end   chan error
	mu    sync.Mutex
	sent  []string
	fail  bool
	ctx   context.Context
}

func (r *fakeHelpRunner) Run(ctx context.Context) error {
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil
	case err := <-r.end:
		return err
	}
}
func (r *fakeHelpRunner) SendChat(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("closed")
	}
	r.sent = append(r.sent, text)
	return nil
}

// ctxErr: the engine session's context error (nil while it may run).
func (r *fakeHelpRunner) ctxErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx == nil {
		return nil
	}
	return r.ctx.Err()
}

func (r *fakeHelpRunner) chatSent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

type fakeHelpBackend struct {
	mu      sync.Mutex
	n       int
	server  string
	regErr  []error // the next registrations fail with these
	rotate  bool    // the registrations can draw a new code (helpCodeRotator)
	hosts   chan *fakeHelpHost
	runners chan *fakeHelpRunner
	// report answers Report (nil: errHelpReportUnavailable).
	report func(ctx context.Context, server string, r helpReportReq) error
}

func (b *fakeHelpBackend) Report(ctx context.Context, server string, r helpReportReq) error {
	if b.report == nil {
		return errHelpReportUnavailable
	}
	return b.report(ctx, server, r)
}

func newFakeHelpBackend() *fakeHelpBackend {
	return &fakeHelpBackend{hosts: make(chan *fakeHelpHost, 8), runners: make(chan *fakeHelpRunner, 8)}
}

func (b *fakeHelpBackend) Register(ctx context.Context, server string, hooks helpHooks) (helpHostLink, error) {
	b.mu.Lock()
	b.server = server
	if len(b.regErr) > 0 {
		err := b.regErr[0]
		b.regErr = b.regErr[1:]
		b.mu.Unlock()
		return nil, err
	}
	b.n++
	n := b.n
	b.mu.Unlock()
	h := &fakeHelpHost{id: strings.Repeat(string(rune('0'+n%10)), 9), code: "12345678" + string(rune('0'+n%10)), hooks: hooks,
		ops: make(chan interface{}, 4), done: make(chan struct{})}
	b.hosts <- h
	if b.rotate {
		return &fakeRotHost{h}, nil
	}
	return h, nil
}

func (b *fakeHelpBackend) NewSession(conn helpSessionLink, p helpPerms, ev helpEvents) helpRunner {
	r := &fakeHelpRunner{perms: p, ev: ev, end: make(chan error, 1)}
	b.runners <- r
	return r
}

func (b *fakeHelpBackend) ErrKind(err error) string {
	if strings.Contains(err.Error(), "banned") {
		return helpErrRefused
	}
	return helpErrUnreachable
}

func (b *fakeHelpBackend) nextHost(t *testing.T) *fakeHelpHost {
	t.Helper()
	select {
	case h := <-b.hosts:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("no registration")
		return nil
	}
}

func (b *fakeHelpBackend) nextRunner(t *testing.T) *fakeHelpRunner {
	t.Helper()
	select {
	case r := <-b.runners:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no engine session")
		return nil
	}
}

func newTestHelpHost(t *testing.T) (*helpHost, *fakeHelpBackend, string) {
	t.Helper()
	b := newFakeHelpBackend()
	s := &supportSettings{store: &memSupportStore{}}
	list, err := s.save(supportServerInput{Label: "Help desk", URL: "support.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	list = supportUserServers(list)
	h := newHelpHost(s, b)
	h.ApprovalTimeout = 2 * time.Second
	h.Backoff = []time.Duration{50 * time.Millisecond}
	h.PathRefresh = 20 * time.Millisecond
	t.Cleanup(h.stop)
	return h, b, list[0].ID
}

func waitHelpNotice(t *testing.T, h *helpHost, want string) helpView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		v := h.view(0)
		if v.Notice == want {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("notice %q, want %q (%+v)", v.Notice, want, v)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitHelpPhase(t *testing.T, h *helpHost, want string) helpView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		v := h.view(0)
		if v.Phase == want {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase %q, want %q (%+v)", v.Phase, want, v)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// approveAsync: what supportlink does when an operator proved the code.
func approveAsync(h *fakeHelpHost, ctx context.Context, sid string) chan bool {
	res := make(chan bool, 1)
	go func() {
		res <- h.hooks.Approve(ctx, helpApprovalReq{SID: sid, Operator: "Jane\x07 Doe", Account: "jane@acme", SAS: "47 12"})
	}()
	return res
}

func TestHelpHostSessionFlow(t *testing.T) {
	h, b, sid := newTestHelpHost(t)
	var events []string
	var evMu sync.Mutex
	h.onSession = func(on bool) {
		evMu.Lock()
		events = append(events, map[bool]string{true: "on", false: "off"}[on])
		evMu.Unlock()
	}
	h.onAttention = func(kind string) { evMu.Lock(); events = append(events, "attention:"+kind); evMu.Unlock() }

	if err := h.start("unknown"); err == nil || err.(*helpError).Code != "no_server" {
		t.Fatalf("unknown server: %v", err)
	}
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	if err := h.start(sid); err == nil || err.(*helpError).Code != "busy" {
		t.Fatalf("second start: %v", err)
	}
	host := b.nextHost(t)
	if b.server != "https://support.example.com" {
		t.Errorf("registered with %q", b.server)
	}
	v := waitHelpPhase(t, h, helpWaiting)
	if v.ID != host.id || v.Code != host.code || v.Server != "Help desk" {
		t.Fatalf("waiting view %+v", v)
	}
	// A wrong-code attempt burns the code: a new code and the notice.
	host.hooks.OnCredentials(host.id, "999888777", "failed")
	v = h.view(0)
	if v.Code != "999888777" || v.Notice != helpNoticeFailed {
		t.Fatalf("after a failed attempt %+v", v)
	}

	// An operator proved the code: the acceptance request.
	res := approveAsync(host, context.Background(), "S1")
	v = waitHelpPhase(t, h, helpApproval)
	if v.Approval == nil || v.Approval.SID != "S1" || v.Approval.Operator != "Jane Doe" || v.Approval.Account != "jane@acme" ||
		v.Approval.SAS != "47 12" || v.Approval.Remaining < 1 {
		t.Fatalf("approval view %+v", v.Approval)
	}
	if h.decide("other", true, helpPerms{}) {
		t.Fatal("a decision for another request was taken")
	}
	if !h.decide("S1", true, helpPerms{Control: true, Clipboard: false, Files: true}) {
		t.Fatal("decide refused")
	}
	if !<-res {
		t.Fatal("the hook did not accept")
	}
	if h.view(0).Phase != helpStarting {
		t.Fatalf("after accepting: %s", h.view(0).Phase)
	}
	sess := newFakeHelpSess("S1")
	host.ops <- sess
	r := b.nextRunner(t)
	if r.perms != (helpPerms{Control: true, Files: true}) {
		t.Errorf("engine permissions %+v", r.perms)
	}
	v = waitHelpPhase(t, h, helpSession)
	if v.Session == nil || v.Session.Operator != "Jane Doe" || v.Session.Path != "direct" || !v.Session.Perms.Files || !v.ChatOn {
		t.Fatalf("session view %+v", v.Session)
	}
	gen := v.Gen

	// Chat both ways, cleaned, numbered; the lines after a seq.
	r.ev.OnChat("hello\x1b[31m <b>there</b>\r\nline 2")
	if err := h.sendChat("  hi  "); err != nil {
		t.Fatal(err)
	}
	if err := h.sendChat(strings.Repeat("x", helpChatMaxBytes+1)); err == nil || err.(*helpError).Code != "too_long" {
		t.Fatalf("too long: %v", err)
	}
	if err := h.sendChat(" \x00 "); err == nil || err.(*helpError).Code != "empty" {
		t.Fatalf("empty: %v", err)
	}
	if got := r.chatSent(); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("sent %q", got)
	}
	v = h.view(0)
	if len(v.Chat) != 2 || v.Chat[0].From != "op" || v.Chat[0].Text != "hello[31m <b>there</b> \nline 2" || v.Chat[1].From != "me" || v.ChatSeq != 2 {
		t.Fatalf("chat %+v", v.Chat)
	}
	if v = h.view(1); len(v.Chat) != 1 || v.Chat[0].Seq != 2 {
		t.Fatalf("chat after 1: %+v", v.Chat)
	}
	// A file received: in the session view.
	// (a path of this OS: the engine reports native paths)
	r.ev.FileReceived(filepath.Join(t.TempDir(), "Downloads", "report.pdf"))
	if v = h.view(0); v.Session.Files != 1 || v.Session.LastFile != "report.pdf" {
		t.Fatalf("file %+v", v.Session)
	}
	// The path moves to the relay.
	sess.mu.Lock()
	sess.path, sess.relayed = "relay_udp", true
	sess.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for h.view(0).Session.Path != "relay_udp" {
		if time.Now().After(deadline) {
			t.Fatal("path not refreshed")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The person's Stop: back to waiting, notice "ended" (why: "local"),
	// the link closed, the transcript dropped; the SAME registration goes on
	// (its ID kept, the code renewed by supportlink).
	if !h.endSession() {
		t.Fatal("endSession")
	}
	v = waitHelpNotice(t, h, helpNoticeEnded)
	if v.Phase != helpWaiting || v.Ended == nil || v.Ended.Reason != helpEndLocal || v.Ended.Operator != "Jane Doe" || !sess.closed() {
		t.Fatalf("ended %+v / %+v, closed %v", v, v.Ended, sess.closed())
	}
	if v.ID != host.id || len(v.Chat) != 0 {
		t.Fatalf("after the session: %+v", v)
	}
	if err := h.sendChat("late"); err == nil || err.(*helpError).Code != "no_session" {
		t.Fatalf("chat after the end: %v", err)
	}
	select {
	case <-host.done:
		t.Error("the registration was closed after the session (the ID must stay)")
	default:
	}
	// Dismissed: the notice goes.
	if !h.acknowledge() || h.view(0).Notice != "" || h.view(0).Ended != nil {
		t.Fatalf("acknowledge: %+v", h.view(0))
	}
	if h.acknowledge() {
		t.Fatal("a second acknowledgement")
	}

	// The second session (same registration): ended by the operator.
	res = approveAsync(host, context.Background(), "S2")
	waitHelpPhase(t, h, helpApproval)
	h.decide("S2", true, helpPerms{Control: true})
	<-res
	sess2 := newFakeHelpSess("S2")
	host.ops <- sess2
	r2 := b.nextRunner(t)
	v = waitHelpPhase(t, h, helpSession)
	if v.Gen == gen {
		t.Error("the session generation did not change")
	}
	// Another operator during the session: refused at once, nothing shown.
	if <-approveAsync(host, context.Background(), "INTRUDER") {
		t.Fatal("a second operator was accepted during a session")
	}
	if h.view(0).Phase != helpSession {
		t.Fatalf("the session view changed: %+v", h.view(0))
	}
	r2.end <- nil
	if v = waitHelpNotice(t, h, helpNoticeEnded); v.Ended.Reason != helpEndPeer || v.ID != host.id {
		t.Fatalf("operator left: %+v", v)
	}
	res = approveAsync(host, context.Background(), "S3")
	waitHelpPhase(t, h, helpApproval)
	if h.view(0).Notice != "" {
		t.Error("a new request keeps the end notice")
	}
	h.decide("S3", true, helpPerms{})
	<-res
	sess3 := newFakeHelpSess("S3")
	host.ops <- sess3
	r3 := b.nextRunner(t)
	waitHelpPhase(t, h, helpSession)
	sess3.end(errors.New("ice failed"))
	r3.end <- nil
	if v = waitHelpNotice(t, h, helpNoticeEnded); v.Ended.Reason != helpEndLost {
		t.Fatalf("link lost: %s", v.Ended.Reason)
	}
	res = approveAsync(host, context.Background(), "S4")
	waitHelpPhase(t, h, helpApproval)
	h.decide("S4", true, helpPerms{})
	<-res
	host.ops <- newFakeHelpSess("S4")
	r4 := b.nextRunner(t)
	waitHelpPhase(t, h, helpSession)
	r4.end <- errors.New("capture failed")
	if v = waitHelpNotice(t, h, helpNoticeEnded); v.Ended.Reason != helpEndError {
		t.Fatalf("engine failure: %s", v.Ended.Reason)
	}
	select {
	case <-b.hosts:
		t.Fatal("a new registration after sessions (the ID must stay)")
	default:
	}
	host4 := host

	// Stop: back to idle, the registration closed.
	h.stop()
	if v = h.view(0); v.Phase != helpIdle {
		t.Fatalf("after stop %+v", v)
	}
	select {
	case <-host4.done:
	default:
		t.Error("registration still open after stop")
	}
	evMu.Lock()
	got := strings.Join(events, ",")
	evMu.Unlock()
	if got != "attention:approval,on,off,attention:approval,on,off,attention:approval,on,off,attention:approval,on,off" {
		t.Errorf("session events %s", got)
	}
}

func TestHelpHostRefusals(t *testing.T) {
	h, b, sid := newTestHelpHost(t)
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	host := b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)

	// Refused by the person.
	res := approveAsync(host, context.Background(), "R1")
	waitHelpPhase(t, h, helpApproval)
	h.decide("R1", false, helpPerms{Control: true})
	if <-res {
		t.Fatal("refusal accepted")
	}
	if v := h.view(0); v.Phase != helpWaiting || v.Notice != helpNoticeRefused {
		t.Fatalf("after a refusal %+v", v)
	}
	// Not answered in time.
	h.ApprovalTimeout = 100 * time.Millisecond
	if <-approveAsync(host, context.Background(), "R2") {
		t.Fatal("timeout accepted")
	}
	if v := h.view(0); v.Notice != helpNoticeTimedOut {
		t.Fatalf("after a timeout %+v", v)
	}
	// The operator left during the decision.
	h.ApprovalTimeout = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	res = approveAsync(host, ctx, "R3")
	waitHelpPhase(t, h, helpApproval)
	cancel()
	if <-res {
		t.Fatal("withdrawn accepted")
	}
	if v := h.view(0); v.Phase != helpWaiting || v.Notice != helpNoticeWithdrawn || v.Approval != nil {
		t.Fatalf("after a withdrawal %+v", v)
	}
	// A session nobody accepted: closed at once, no engine.
	rogue := newFakeHelpSess("ROGUE")
	host.ops <- rogue
	deadline := time.Now().Add(2 * time.Second)
	for !rogue.closed() {
		if time.Now().After(deadline) {
			t.Fatal("a session without acceptance was not closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-b.runners:
		t.Fatal("the engine ran a session nobody accepted")
	default:
	}
	// Accepted, but the link never came up.
	res = approveAsync(host, context.Background(), "R4")
	waitHelpPhase(t, h, helpApproval)
	h.decide("R4", true, helpPerms{})
	<-res
	host.ops <- errors.New("ice timeout")
	deadline = time.Now().Add(2 * time.Second)
	for h.view(0).Notice != helpNoticeSetupFailed {
		if time.Now().After(deadline) {
			t.Fatalf("setup failure not said: %+v", h.view(0))
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Locked ID.
	host.hooks.OnCredentials("555444333", "111222333", "locked")
	if v := h.view(0); v.ID != "555444333" || v.Notice != helpNoticeLocked {
		t.Fatalf("locked %+v", v)
	}
	// Stop during the decision: back to idle, the request withdrawn.
	res = approveAsync(host, context.Background(), "R5")
	waitHelpPhase(t, h, helpApproval)
	h.stop()
	if v := h.view(0); v.Phase != helpIdle {
		t.Fatalf("stop during the decision %+v", v)
	}
	h.decide("R5", true, helpPerms{}) // (too late: nobody waits)
	select {
	case ok := <-res:
		_ = ok
	case <-time.After(3 * time.Second):
		t.Fatal("the approval hook is still blocked after stop")
	}
}

func TestHelpHostUnreachableThenStop(t *testing.T) {
	h, b, sid := newTestHelpHost(t)
	b.regErr = []error{errors.New("dial: refused"), errors.New("banned")}
	h.Backoff = []time.Duration{300 * time.Millisecond}
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	v := waitHelpPhase(t, h, helpUnreachable)
	if v.ErrKind != helpErrUnreachable || v.RetryIn < 1 {
		t.Fatalf("unreachable %+v", v)
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.view(0).ErrKind != helpErrRefused {
		if time.Now().After(deadline) {
			t.Fatalf("second failure kind: %+v", h.view(0))
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)
	h.stop()
	if v := h.view(0); v.Phase != helpIdle {
		t.Fatalf("after stop %+v", v)
	}
}

func TestHelpHostStopDuringSession(t *testing.T) {
	h, b, sid := newTestHelpHost(t)
	var on, off int
	h.onSession = func(v bool) {
		if v {
			on++
		} else {
			off++
		}
	}
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	host := b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)
	res := approveAsync(host, context.Background(), "S")
	waitHelpPhase(t, h, helpApproval)
	h.decide("S", true, helpPerms{Control: true})
	<-res
	sess := newFakeHelpSess("S")
	host.ops <- sess
	b.nextRunner(t)
	waitHelpPhase(t, h, helpSession)
	h.stop()
	if v := h.view(0); v.Phase != helpIdle || len(v.Chat) != 0 {
		t.Fatalf("after stop %+v", v)
	}
	if !sess.closed() || on != 1 || off != 1 {
		t.Fatalf("closed %v, onSession %d/%d", sess.closed(), on, off)
	}
	// It can start again.
	if err := h.start(sid); err != nil {
		t.Fatal(err)
	}
	b.nextHost(t)
	waitHelpPhase(t, h, helpWaiting)
}

func TestCleanChatText(t *testing.T) {
	cases := map[string]string{
		"  hi  ":                     "hi",
		"a\r\nb\tc":                  "a \nb c",
		"x\x00\x07\x1b\u2028\u2029y": "xy",
		"\xff\xfe ok":                "\uFFFD ok", // (a run of invalid bytes: one replacement)
	}
	for in, want := range cases {
		if got := cleanChatText(in); got != want {
			t.Errorf("cleanChatText(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 3000) // 6000 bytes
	got := cleanChatText(long)
	if len(got) > helpChatMaxBytes || len(got) < helpChatMaxBytes-1 || !strings.HasPrefix(long, got) {
		t.Errorf("a long line: %d bytes", len(got))
	}
}

func TestHelpHTTP(t *testing.T) {
	sh := newSupportHub(&memSupportStore{}, nil)
	b := newFakeHelpBackend()
	sh.help = newHelpHost(sh.settings, b)
	sh.help.ApprovalTimeout = 2 * time.Second
	t.Cleanup(sh.help.stop)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sh.serveHTTP(w, r, supOrigin) }))
	defer srv.Close()

	// No key needed to receive help.
	st, out := supPost(t, srv, "servers/save", map[string]interface{}{"label": "Desk", "url": "https://desk.example.com"}, supOrigin)
	if st != 200 {
		t.Fatalf("save without a key: %d %v", st, out)
	}
	sid := out["servers"].([]interface{})[0].(map[string]interface{})["id"].(string)

	if st, _, _ := supGet(t, srv, "help/state"); st != 200 {
		t.Fatalf("state %d", st)
	}
	if st, _ := supPost(t, srv, "help/start", map[string]string{"server": sid}, ""); st != http.StatusForbidden {
		t.Fatalf("start without the Origin: %d", st)
	}
	if st, _ := supPost(t, srv, "help/start", map[string]string{"server": sid}, "http://evil.example"); st != http.StatusForbidden {
		t.Fatalf("start from another Origin: %d", st)
	}
	_ = sid
	// "start" = "Allow incoming support" on, with the default server.
	if st, out := supPost(t, srv, "help/start", map[string]string{}, supOrigin); st != 200 || out["phase"] != "connecting" || out["incoming"] != true {
		t.Fatalf("start: %d %v", st, out)
	}
	host := b.nextHost(t)
	waitHelpPhase(t, sh.help, helpWaiting)
	_, view, raw := supGet(t, srv, "help/state")
	if view["id"] != host.id || view["code"] != host.code || view["server"] != "Desk" || strings.Contains(raw, "desk.example.com") {
		t.Fatalf("waiting state %s", raw)
	}
	res := approveAsync(host, context.Background(), "H1")
	waitHelpPhase(t, sh.help, helpApproval)
	if st, out := supPost(t, srv, "help/decide", map[string]interface{}{"sid": "H1", "accept": true, "control": true, "clipboard": true}, ""); st != http.StatusForbidden {
		t.Fatalf("decide without the Origin: %d %v", st, out)
	}
	if _, out := supPost(t, srv, "help/decide", map[string]interface{}{"sid": "H1", "accept": true, "control": true, "clipboard": true}, supOrigin); out["ok"] != true {
		t.Fatalf("decide %v", out)
	}
	<-res
	host.ops <- newFakeHelpSess("H1")
	r := b.nextRunner(t)
	if r.perms != (helpPerms{Control: true, Clipboard: true}) {
		t.Errorf("perms %+v", r.perms)
	}
	waitHelpPhase(t, sh.help, helpSession)
	r.ev.OnChat("from the operator")
	if st, _ := supPost(t, srv, "help/chat", map[string]string{"text": "hello"}, supOrigin); st != 200 {
		t.Fatalf("chat %d", st)
	}
	if st, out := supPost(t, srv, "help/chat", map[string]string{"text": strings.Repeat("ü", 2049)}, supOrigin); st != 400 || out["error"] != "too_long" {
		t.Fatalf("a 4098-byte line: %d %v", st, out)
	}
	if st, _ := supPost(t, srv, "help/chat", map[string]string{"text": strings.Repeat("ü", 2048)}, supOrigin); st != 200 {
		t.Fatalf("a 4096-byte line: %d", st)
	}
	_, view, _ = supGet(t, srv, "help/state?chat=0&gen=1")
	if lines := view["chat"].([]interface{}); len(lines) != 3 || lines[0].(map[string]interface{})["from"] != "op" {
		t.Fatalf("chat lines %v", view["chat"])
	}
	_, view, _ = supGet(t, srv, "help/state?chat=2&gen=1")
	if lines := view["chat"].([]interface{}); len(lines) != 1 {
		t.Fatalf("chat after 2: %v", view["chat"])
	}
	// Another session's page (gen) gets them all.
	_, view, _ = supGet(t, srv, "help/state?chat=2&gen=0")
	if lines := view["chat"].([]interface{}); len(lines) != 3 {
		t.Fatalf("chat for another gen: %v", view["chat"])
	}
	if _, out := supPost(t, srv, "help/end", map[string]string{}, supOrigin); out["ok"] != true {
		t.Fatalf("end %v", out)
	}
	waitHelpNotice(t, sh.help, helpNoticeEnded)
	if _, out := supPost(t, srv, "help/ack", map[string]string{}, supOrigin); out["ok"] != true {
		t.Fatalf("ack %v", out)
	}
	if st, out := supPost(t, srv, "help/stop", map[string]string{}, supOrigin); st != 200 || out["phase"] != "idle" || out["incoming"] != false {
		t.Fatalf("stop %d %v", st, out)
	}
	if on, _, _ := sh.help.settings.prefs(); on {
		t.Fatal("stop did not keep incoming support off")
	}
	if st, _ := supPost(t, srv, "help/nope", map[string]string{}, supOrigin); st != 404 {
		t.Fatalf("unknown route %d", st)
	}
	// A hub without "Receive help": 404.
	sh2 := newSupportHub(&memSupportStore{}, nil)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sh2.serveHTTP(w, r, supOrigin) }))
	defer srv2.Close()
	if st, _, _ := supGet(t, srv2, "help/state"); st != 404 {
		t.Fatalf("no help host: %d", st)
	}
}

func TestVaultHeld(t *testing.T) {
	v, _ := newTestVault()
	if err := v.Create("correct horse battery", 15); err != nil {
		t.Fatal(err)
	}
	v.SetHeld(true)
	if st := v.State().State; st != "locked" || !v.Held() {
		t.Fatalf("held: state %s", st)
	}
	if err := v.Unlock("correct horse battery"); !errors.Is(err, errVaultHeld) {
		t.Fatalf("unlock while held: %v", err)
	}
	if err := v.UnlockWithPresence(func() error { t.Fatal("presence asked while held"); return nil }); !errors.Is(err, errVaultHeld) {
		t.Fatalf("presence unlock while held: %v", err)
	}
	if err := v.Reset(false); !errors.Is(err, errVaultHeld) {
		t.Fatalf("reset while held: %v", err)
	}
	v.SetHeld(false)
	if err := v.Unlock("correct horse battery"); err != nil {
		t.Fatalf("unlock after: %v", err)
	}
}
