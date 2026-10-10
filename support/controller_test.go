package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeView struct {
	states chan State
	// answer decides each request; nil waits for ctx (no answer).
	answer func(a Approval) (Permissions, bool)
	asked  chan Approval
}

func newFakeView() *fakeView {
	return &fakeView{states: make(chan State, 256), asked: make(chan Approval, 16)}
}

func (v *fakeView) Show(s State) {
	select {
	case v.states <- s:
	default:
		panic("fakeView: state buffer full")
	}
}

func (v *fakeView) Ask(ctx context.Context, a Approval) (Permissions, bool) {
	v.asked <- a
	if v.answer == nil {
		<-ctx.Done()
		return Permissions{}, false
	}
	return v.answer(a)
}

// waitPhase reads states until one in phase p (and matching ok, if given).
func (v *fakeView) waitPhase(t *testing.T, p Phase, ok func(State) bool) State {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-v.states:
			if s.Phase == p && (ok == nil || ok(s)) {
				return s
			}
		case <-deadline:
			t.Fatalf("timed out waiting for phase %s", p)
		}
	}
}

type fakeSession struct {
	sid       string
	path      string
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	err       error
	token     string // report token
}

func newFakeSession(sid string) *fakeSession {
	return &fakeSession{sid: sid, path: "direct", done: make(chan struct{}), token: "rpt1." + sid}
}

func (s *fakeSession) ReportToken() string { return s.token }

func (s *fakeSession) end(err error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *fakeSession) WriteFrame(byte, []byte) error    { return nil }
func (s *fakeSession) ReadFrame() (byte, []byte, error) { <-s.done; return 0, nil, s.Err() }
func (s *fakeSession) Close() error                     { s.end(io.EOF); return nil }
func (s *fakeSession) SID() string                      { return s.sid }
func (s *fakeSession) Path() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.path != "direct"
}

func (s *fakeSession) setPath(p string) {
	s.mu.Lock()
	s.path = p
	s.mu.Unlock()
}
func (s *fakeSession) Done() <-chan struct{} { return s.done }
func (s *fakeSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

type fakeHost struct {
	id     string
	hooks  HostHooks
	code   string
	result chan hostResultFake
	done   chan struct{}
	closed chan struct{}
	once   sync.Once
	// noToken: sessions get no report token.
	noToken bool
}

type hostResultFake struct {
	s   SessionLink
	err error
}

func (h *fakeHost) ID() string            { return h.id }
func (h *fakeHost) Code() string          { return h.code }
func (h *fakeHost) Done() <-chan struct{} { return h.done }
func (h *fakeHost) Close() error {
	h.once.Do(func() { close(h.closed) })
	return nil
}

func (h *fakeHost) WaitForOperator(ctx context.Context) (SessionLink, error) {
	select {
	case r := <-h.result:
		return r.s, r.err
	case <-h.done:
		return nil, errors.New("host closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// operator simulates an operator who proved the code: it runs the approval
// like supportlink does and, when accepted, delivers the session (or a setup
// error). It returns the approval result.
func (h *fakeHost) operator(ctx context.Context, sid string, setupErr error) (*fakeSession, bool) {
	ok := h.hooks.Approve(ctx, Approval{SID: sid, OperatorName: "Jane", Account: "helpdesk", SAS: "123 456"})
	if !ok {
		h.hooks.OnCredentials(h.id, "222222222", reasonRejected)
		return nil, false
	}
	h.hooks.OnCredentials(h.id, "333333333", reasonUsed)
	if setupErr != nil {
		h.result <- hostResultFake{err: setupErr}
		return nil, true
	}
	s := newFakeSession(sid)
	if h.noToken {
		s.token = "" // a server without reports
	}
	h.result <- hostResultFake{s: s}
	return s, true
}

type fakeBackend struct {
	mu       sync.Mutex
	n        int
	failures []error // returned by the first Register calls
	hosts    chan *fakeHost
	runs     chan Permissions
	runErr   error
	events   SessionEvents // of the last RunSession

	reports    chan ReportRequest // every report sent
	reportErr  error              // result of Report
	reportHold chan struct{}      // when set, Report waits for it
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{hosts: make(chan *fakeHost, 16), runs: make(chan Permissions, 16), reports: make(chan ReportRequest, 16)}
}

func (b *fakeBackend) Report(ctx context.Context, r ReportRequest) error {
	b.mu.Lock()
	hold, err := b.reportHold, b.reportErr
	b.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.reports <- r
	return err
}

func (b *fakeBackend) Register(ctx context.Context, hooks HostHooks) (HostLink, error) {
	b.mu.Lock()
	if len(b.failures) > 0 {
		err := b.failures[0]
		b.failures = b.failures[1:]
		b.mu.Unlock()
		return nil, err
	}
	b.n++
	h := &fakeHost{
		id: fmt.Sprintf("10000000%d", b.n), code: "111111111", hooks: hooks,
		result: make(chan hostResultFake, 1), done: make(chan struct{}), closed: make(chan struct{}),
	}
	b.mu.Unlock()
	b.hosts <- h
	return h, nil
}

func (b *fakeBackend) RunSession(ctx context.Context, conn SessionLink, p Permissions, ev SessionEvents) error {
	b.mu.Lock()
	b.events = ev
	b.mu.Unlock()
	b.runs <- p
	select {
	case <-ctx.Done():
	case <-conn.Done():
	}
	_ = conn.Close()
	return b.runErr
}

func (b *fakeBackend) ErrKind(err error) string {
	if err.Error() == "upgrade" {
		return ErrKindUpgrade
	}
	return ErrKindUnreachable
}

func (b *fakeBackend) nextHost(t *testing.T) *fakeHost {
	t.Helper()
	select {
	case h := <-b.hosts:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("no registration")
		return nil
	}
}

type harness struct {
	b      *fakeBackend
	v      *fakeView
	c      *Controller
	cancel context.CancelFunc
	done   chan struct{}
}

func start(t *testing.T, setup func(*harness)) *harness {
	t.Helper()
	h := &harness{b: newFakeBackend(), v: newFakeView()}
	h.v.answer = func(Approval) (Permissions, bool) { return DefaultPermissions(), true }
	h.c = NewController(h.b, h.v, t.Logf)
	h.c.Backoff = []time.Duration{10 * time.Millisecond}
	h.c.PathRefresh = 10 * time.Millisecond
	if setup != nil {
		setup(h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		h.c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("controller did not stop")
		}
	})
	return h
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// ── tests ────────────────────────────────────────────────────────────────────

// The full cycle: waiting → approval → starting → session → stop → ended →
// acknowledged → a new registration (new ID and code).
func TestControllerCycleLocalStop(t *testing.T) {
	h := start(t, nil)
	host := h.b.nextHost(t)
	st := h.v.waitPhase(t, PhaseWaiting, nil)
	if st.ID != "100000001" || st.Code != "111111111" {
		t.Fatalf("waiting state shows %q / %q", st.ID, st.Code)
	}

	sessCh := make(chan *fakeSession, 1)
	go func() { s, _ := host.operator(context.Background(), "sid-1", nil); sessCh <- s }()
	ap := h.v.waitPhase(t, PhaseApproval, nil)
	if ap.Approval.OperatorName != "Jane" || ap.Approval.Account != "helpdesk" || ap.Approval.SAS != "123 456" {
		t.Fatalf("approval = %+v", ap.Approval)
	}
	if ap.Approval.Deadline.IsZero() {
		t.Fatal("approval without a deadline")
	}
	h.v.waitPhase(t, PhaseStarting, nil)
	ss := h.v.waitPhase(t, PhaseSession, nil)
	if ss.Session.Operator != "Jane" || ss.Session.Path != "direct" || ss.Session.Relayed {
		t.Fatalf("session info = %+v", ss.Session)
	}
	if p := <-h.b.runs; p != DefaultPermissions() {
		t.Fatalf("engine got %+v, want the accepted %+v", p, DefaultPermissions())
	}

	h.c.StopSession()
	end := h.v.waitPhase(t, PhaseEnded, nil)
	if end.Ended.Reason != EndLocal || end.Ended.Operator != "Jane" {
		t.Fatalf("ended = %+v", end.Ended)
	}
	if sess := <-sessCh; !isClosed(sess.done) {
		t.Fatal("session not closed after Stop")
	}
	if !isClosed(host.closed) {
		t.Fatal("the registration must end with the session (ephemeral ID)")
	}
	// No new registration before the person leaves the end screen.
	select {
	case <-h.b.hosts:
		t.Fatal("registered again before Acknowledge")
	case <-time.After(50 * time.Millisecond):
	}
	h.c.Acknowledge()
	h.v.waitPhase(t, PhaseConnecting, nil)
	h.b.nextHost(t)
	st = h.v.waitPhase(t, PhaseWaiting, nil)
	if st.ID != "100000002" || st.Notice != NoticeNone {
		t.Fatalf("after the session: ID %q notice %q", st.ID, st.Notice)
	}
}

func TestControllerPeerEndsAndPermissions(t *testing.T) {
	want := Permissions{Control: false, Clipboard: false, Files: true}
	h := start(t, func(h *harness) {
		h.v.answer = func(Approval) (Permissions, bool) { return want, true }
	})
	host := h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)
	sessCh := make(chan *fakeSession, 1)
	go func() { s, _ := host.operator(context.Background(), "sid-2", nil); sessCh <- s }()
	ss := h.v.waitPhase(t, PhaseSession, nil)
	if ss.Session.Perms != want {
		t.Fatalf("session perms %+v, want %+v", ss.Session.Perms, want)
	}
	if p := <-h.b.runs; p != want {
		t.Fatalf("engine got %+v, want %+v", p, want)
	}
	s := <-sessCh
	s.setPath("relay_udp")
	ss = h.v.waitPhase(t, PhaseSession, func(s State) bool { return s.Session.Relayed })
	if ss.Session.Path != "relay_udp" {
		t.Fatalf("path not refreshed: %+v", ss.Session)
	}
	// A file the operator sent shows in the session state (banner notice).
	h.b.mu.Lock()
	ev := h.b.events
	h.b.mu.Unlock()
	if ev.FileReceived == nil {
		t.Fatal("RunSession got no FileReceived hook")
	}
	ev.FileReceived(filepath.Join("C:\\Users\\x\\Downloads", "report.pdf"))
	ss = h.v.waitPhase(t, PhaseSession, func(s State) bool { return s.Session.FilesReceived == 1 })
	if ss.Session.LastFile != "report.pdf" || ss.Session.LastFileDir != filepath.Join("C:\\Users\\x", "Downloads") || ss.Session.LastFileAt.IsZero() {
		t.Fatalf("file notice %+v", ss.Session)
	}
	s.end(io.EOF) // the operator left
	end := h.v.waitPhase(t, PhaseEnded, nil)
	if end.Ended.Reason != EndPeer {
		t.Fatalf("reason %d, want EndPeer", end.Ended.Reason)
	}
}

func TestControllerLinkLostAndEngineError(t *testing.T) {
	h := start(t, nil)
	host := h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)
	sessCh := make(chan *fakeSession, 1)
	go func() { s, _ := host.operator(context.Background(), "a", nil); sessCh <- s }()
	h.v.waitPhase(t, PhaseSession, nil)
	(<-sessCh).end(errors.New("ice failed"))
	if e := h.v.waitPhase(t, PhaseEnded, nil); e.Ended.Reason != EndLost {
		t.Fatalf("reason %d, want EndLost", e.Ended.Reason)
	}
	h.c.Acknowledge()

	h.b.runErr = errors.New("capture unavailable")
	host = h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)
	go func() { s, _ := host.operator(context.Background(), "b", nil); sessCh <- s }()
	h.v.waitPhase(t, PhaseSession, nil)
	(<-sessCh).end(io.EOF)
	if e := h.v.waitPhase(t, PhaseEnded, nil); e.Ended.Reason != EndError || e.Ended.Err == nil {
		t.Fatalf("ended %+v, want EndError", e.Ended)
	}
}

func TestControllerRefusedTimedOutWithdrawn(t *testing.T) {
	answer := make(chan bool, 1)
	h := start(t, func(h *harness) {
		h.c.ApprovalTimeout = 100 * time.Millisecond
		h.v.answer = func(a Approval) (Permissions, bool) {
			select {
			case ok := <-answer:
				return DefaultPermissions(), ok
			default:
				return Permissions{}, false
			}
		}
	})
	host := h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)

	// Refused.
	answer <- false
	if _, ok := host.operator(context.Background(), "r", nil); ok {
		t.Fatal("refused request reported accepted")
	}
	// The rotated code follows the refusal.
	h.v.waitPhase(t, PhaseWaiting, func(s State) bool { return s.Notice == NoticeRefused && s.Code == "222222222" })

	// No answer: refused at the deadline.
	h.v.answer = nil
	if _, ok := host.operator(context.Background(), "t", nil); ok {
		t.Fatal("timed out request reported accepted")
	}
	h.v.waitPhase(t, PhaseWaiting, func(s State) bool { return s.Notice == NoticeTimedOut })

	// The operator leaves during the decision.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-h.v.asked
		<-h.v.asked // the two earlier requests were also recorded
		<-h.v.asked
		cancel()
	}()
	if _, ok := host.operator(ctx, "w", nil); ok {
		t.Fatal("withdrawn request reported accepted")
	}
	h.v.waitPhase(t, PhaseWaiting, func(s State) bool { return s.Notice == NoticeWithdrawn })
	if isClosed(host.closed) {
		t.Fatal("a refusal must keep the registration")
	}
}

func TestControllerSetupFailureKeepsRegistration(t *testing.T) {
	h := start(t, nil)
	host := h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)
	go host.operator(context.Background(), "x", errors.New("dtls failed"))
	st := h.v.waitPhase(t, PhaseWaiting, func(s State) bool { return s.Notice == NoticeSetupFailed })
	if st.ID != host.id || isClosed(host.closed) {
		t.Fatal("setup failure must keep the same registration")
	}
	// A session that nobody accepted is closed at once.
	stray := newFakeSession("never-accepted")
	host.result <- hostResultFake{s: stray}
	select {
	case <-stray.done:
	case <-time.After(5 * time.Second):
		t.Fatal("unaccepted session left open")
	}
}

func TestControllerRegisterRetryAndLocked(t *testing.T) {
	h := start(t, func(h *harness) {
		h.b.failures = []error{errors.New("dial: refused"), errors.New("upgrade")}
		h.c.UpgradeRetry = 20 * time.Millisecond
	})
	u := h.v.waitPhase(t, PhaseUnreachable, nil)
	if u.ErrKind != ErrKindUnreachable || u.RetryAt.IsZero() {
		t.Fatalf("unreachable state %+v", u)
	}
	h.v.waitPhase(t, PhaseUnreachable, func(s State) bool { return s.ErrKind == ErrKindUpgrade })
	host := h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)

	host.hooks.OnCredentials("987654321", "444444444", reasonLocked)
	st := h.v.waitPhase(t, PhaseWaiting, func(s State) bool { return s.Notice == NoticeLocked })
	if st.ID != "987654321" || st.Code != "444444444" {
		t.Fatalf("locked: shows %q / %q", st.ID, st.Code)
	}
	host.hooks.OnCredentials("987654321", "555555555", reasonFailed)
	h.v.waitPhase(t, PhaseWaiting, func(s State) bool { return s.Notice == NoticeFailed && s.Code == "555555555" })

	// Signalling lost: register again.
	close(host.done)
	h.v.waitPhase(t, PhaseConnecting, nil)
	h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)
}

// Closing the app during a session ends it, without an end screen.
func TestControllerQuitDuringSession(t *testing.T) {
	h := start(t, nil)
	host := h.b.nextHost(t)
	h.v.waitPhase(t, PhaseWaiting, nil)
	sessCh := make(chan *fakeSession, 1)
	go func() { s, _ := host.operator(context.Background(), "q", nil); sessCh <- s }()
	h.v.waitPhase(t, PhaseSession, nil)
	s := <-sessCh
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if !isClosed(s.done) || !isClosed(host.closed) {
		t.Fatal("quitting must close the session and the registration")
	}
	for {
		select {
		case st := <-h.v.states:
			if st.Phase == PhaseEnded {
				t.Fatal("end screen shown while quitting")
			}
			continue
		default:
		}
		break
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0:00", 7 * time.Second: "0:07", 3*time.Minute + 7*time.Second: "3:07",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03", -time.Second: "0:00",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
