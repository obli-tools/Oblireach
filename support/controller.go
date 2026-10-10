package main

// The controller: the life cycle of the thin client, independent of the UI
// and of the network (both are interfaces, faked in the tests).
//
//	connecting ──registered──► waiting ──operator proved the code──► approval
//	    ▲  │                     ▲  ▲                                 │   │
//	    │  └─error─► unreachable │  └──── refused / timed out / ──────┘   │ accepted
//	    │            (retry)     │        withdrawn / setup failed        ▼
//	    │                        │                                     starting
//	    │                        │                                        │ link up
//	    └──── acknowledged ◄── ended ◄──── stop / peer left / error ── session
//
// Every session gets a fresh registration afterwards: a new ID and a new
// code (the host is closed when the session ends). Closing the app (ctx)
// ends everything, the session included.

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"time"
)

// Permissions are what the person grants for one session.
type Permissions struct {
	Control   bool // mouse and keyboard
	Clipboard bool // clipboard text both ways
	Files     bool // file transfers
}

// DefaultPermissions: the acceptance window's initial toggles.
func DefaultPermissions() Permissions { return Permissions{Control: true, Clipboard: true} }

// Phase is the screen the person sees.
type Phase int

const (
	PhaseConnecting  Phase = iota // registering with the rendezvous server
	PhaseUnreachable              // registration failed; retry at State.RetryAt
	PhaseWaiting                  // ID and code shown
	PhaseApproval                 // an operator proved the code: accept or refuse
	PhaseStarting                 // accepted, peer-to-peer link being set up
	PhaseSession                  // session running
	PhaseEnded                    // session over, waiting for Acknowledge
)

var phaseNames = [...]string{"connecting", "unreachable", "waiting", "approval", "starting", "session", "ended"}

func (p Phase) String() string {
	if int(p) < len(phaseNames) {
		return phaseNames[p]
	}
	return "unknown"
}

// Notice is a one-line message shown with the waiting screen (an i18n
// "status.<notice>" key), "" for none.
type Notice string

const (
	NoticeNone        Notice = ""
	NoticeLocked      Notice = "locked"      // someone guessed codes: new ID
	NoticeFailed      Notice = "failed"      // a wrong-code attempt burnt the code
	NoticeRefused     Notice = "refused"     // the person refused
	NoticeTimedOut    Notice = "timedOut"    // no answer in time
	NoticeWithdrawn   Notice = "withdrawn"   // the operator left during the decision
	NoticeSetupFailed Notice = "setupFailed" // accepted, but the link did not come up
)

// Registration error kinds (Backend.ErrKind).
const (
	ErrKindUnreachable = "unreachable"
	ErrKindRefused     = "refused" // banned, rate limited...
	ErrKindUpgrade     = "upgrade" // this version is too old
)

// Approval is the acceptance request shown to the person.
type Approval struct {
	SID          string
	OperatorName string // self-declared: displayed as such
	Account      string // authenticated by the server ("" = none)
	SAS          string // verification code shown on both sides
	Deadline     time.Time
}

// SessionInfo describes the running (or last) session.
type SessionInfo struct {
	Operator string
	Account  string
	Perms    Permissions
	Path     string // "direct", "relay_udp", "relay_tcp", "unknown"
	Relayed  bool
	Started  time.Time
	// Files the operator sent during the session (the last one is shown in
	// the session banner for FileNoticeFor).
	FilesReceived int
	LastFile      string // base name
	LastFileDir   string // folder it was saved in
	LastFileAt    time.Time
	// Chat is the transcript with the operator (memory only, cleared when
	// the session ends).
	Chat []ChatMessage
}

// FileNoticeFor is how long the session banner shows a received file.
const FileNoticeFor = 20 * time.Second

// SessionEvents are what a running session reports to the controller.
type SessionEvents struct {
	// FileReceived: a file the operator sent was saved (full path).
	FileReceived func(path string)
	// ChatReceived: a chat line from the operator (raw, unsanitised).
	ChatReceived func(text string)
	// ChatReady hands the controller the function that sends a chat line to
	// the operator, before the session runs.
	ChatReady func(send func(text string) error)
}

// Chat errors (Controller.SendChat).
var (
	ErrChatNoSession = errors.New("chat: no session running")
	ErrChatBusy      = errors.New("chat: too many lines waiting to be sent")
	ErrChatEmpty     = errors.New("chat: nothing to send")
)

// chatQueueLen bounds the lines waiting to be sent.
const chatQueueLen = 16

type chatOut struct {
	id   int
	text string
}

// EndReason says why a session ended.
type EndReason int

const (
	EndLocal EndReason = iota // the person stopped it
	EndPeer                   // the operator left
	EndLost                   // the link failed
	EndError                  // the engine could not run (capture, encoder)
)

// EndInfo describes a finished session.
type EndInfo struct {
	Operator string
	Duration time.Duration
	Reason   EndReason
	Err      error
}

// State is everything a view needs to render.
type State struct {
	Phase    Phase
	ID, Code string // 9 digits each, "" while not registered
	Notice   Notice
	ErrKind  string    // PhaseUnreachable
	RetryAt  time.Time // PhaseUnreachable
	Approval Approval  // PhaseApproval
	Session  SessionInfo
	Ended    EndInfo // PhaseEnded
	Report   ReportInfo
}

// ReportInfo says whether the operator of the current (or last) session
// can be reported.
type ReportInfo struct {
	// Available: the server gave this session a report token.
	Available bool
	// Until is the end of the reporting window on the end screen (zero
	// while the session runs).
	Until time.Time
	// Sent: the report of this session was sent.
	Sent bool
}

// Open reports whether the report action is offered at now.
func (r ReportInfo) Open(now time.Time) bool {
	return r.Available && !r.Sent && (r.Until.IsZero() || now.Before(r.Until))
}

// HostHooks are the callbacks a registration reports to.
type HostHooks struct {
	Approve       func(ctx context.Context, a Approval) bool
	OnCredentials func(id, code, reason string) // must not block
}

// HostLink is a registered host (supportlink.Host).
type HostLink interface {
	ID() string
	Code() string
	WaitForOperator(ctx context.Context) (SessionLink, error)
	Done() <-chan struct{}
	Close() error
}

// SessionLink is an established session (supportlink.Session).
type SessionLink interface {
	WriteFrame(op byte, p []byte) error
	ReadFrame() (byte, []byte, error)
	Close() error
	SID() string
	Path() (path string, relayed bool)
	Done() <-chan struct{}
	Err() error
	// ReportToken authenticates a report about this session ("" when the
	// server takes no reports).
	ReportToken() string
}

// Backend is the network and the streaming engine.
type Backend interface {
	Register(ctx context.Context, hooks HostHooks) (HostLink, error)
	RunSession(ctx context.Context, conn SessionLink, perms Permissions, ev SessionEvents) error
	ErrKind(err error) string
	// Report sends an abuse report: nil when the server has it (also when
	// it already had it), else ErrReportRefused, ErrReportRateLimited or
	// ErrReportUnreachable (wrapped).
	Report(ctx context.Context, r ReportRequest) error
}

// View renders states and asks the person.
type View interface {
	// Show renders a state. It must not block (called from network
	// goroutines).
	Show(State)
	// Ask shows the acceptance window and blocks until the person decides
	// or ctx ends (the operator left, or the deadline passed): then it
	// must close the window and return false.
	Ask(ctx context.Context, a Approval) (Permissions, bool)
}

// Credentials change reasons (supportlink.Reason*).
const (
	reasonUsed     = "used"
	reasonRejected = "rejected"
	reasonFailed   = "failed"
	reasonExpired  = "expired"
	reasonLocked   = "locked"
)

// Controller drives one thin client.
type Controller struct {
	b   Backend
	v   View
	log func(format string, args ...any)

	// ApprovalTimeout auto-refuses an unanswered request (below the
	// server's 120 s expiry, so the person's answer always counts).
	ApprovalTimeout time.Duration
	// Backoff between failed registrations (the last value repeats).
	Backoff []time.Duration
	// UpgradeRetry is the delay after an "upgrade required" answer.
	UpgradeRetry time.Duration
	// PathRefresh is how often the session path is re-read.
	PathRefresh time.Duration
	// ReportWindow: how long the end screen offers the report (the server
	// accepts it for longer).
	ReportWindow time.Duration
	// ReportTimeout bounds the sending of a report.
	ReportTimeout time.Duration

	mu        sync.Mutex
	st        State
	granted   map[string]Permissions // sid -> what the person accepted
	stop      context.CancelFunc     // the running session
	localStop bool
	ack       chan struct{}

	chat     ChatLog
	chatSend func(text string) error
	chatQ    chan chatOut  // nil without a session
	chatUp   chan struct{} // closed once chatSend is known

	// rep is the report target of the current (or last) session (memory
	// only, forgotten when the end screen is left).
	rep reportTarget
}

type reportTarget struct {
	sid, token    string
	sending, sent bool
}

// NewController returns a controller with the production timings.
func NewController(b Backend, v View, logf func(string, ...any)) *Controller {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Controller{
		b: b, v: v, log: logf,
		ApprovalTimeout: 90 * time.Second,
		Backoff:         []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second},
		UpgradeRetry:    5 * time.Minute,
		PathRefresh:     2 * time.Second,
		ReportWindow:    10 * time.Minute,
		ReportTimeout:   30 * time.Second,
		granted:         map[string]Permissions{},
		ack:             make(chan struct{}, 1),
	}
}

// State returns the current state.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// update changes the state under the lock and shows the result.
func (c *Controller) update(f func(s *State)) {
	c.mu.Lock()
	f(&c.st)
	st := c.st
	c.mu.Unlock()
	c.v.Show(st)
}

// StopSession ends the running session at once (the person's Stop button
// or hotkey). No-op without a session.
func (c *Controller) StopSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.localStop = true
		c.stop()
	}
}

// Acknowledge leaves the end screen (a new ID and code follow).
func (c *Controller) Acknowledge() {
	c.mu.Lock()
	ended := c.st.Phase == PhaseEnded
	c.mu.Unlock()
	if !ended {
		return
	}
	select {
	case c.ack <- struct{}{}:
	default:
	}
}

// Run drives the client until ctx ends.
func (c *Controller) Run(ctx context.Context) {
	attempt := 0
	hooks := HostHooks{Approve: c.approve, OnCredentials: c.credentials}
	for ctx.Err() == nil {
		c.update(func(s *State) {
			s.Phase, s.ID, s.Code, s.ErrKind = PhaseConnecting, "", "", ""
		})
		host, err := c.b.Register(ctx, hooks)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			kind := c.b.ErrKind(err)
			d := c.Backoff[min(attempt, len(c.Backoff)-1)]
			if kind == ErrKindUpgrade {
				d = c.UpgradeRetry
			}
			attempt++
			c.log("register failed (%s, retry in %s): %v", kind, d, err)
			c.update(func(s *State) {
				s.Phase, s.ErrKind, s.RetryAt, s.Notice = PhaseUnreachable, kind, time.Now().Add(d), NoticeNone
			})
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			continue
		}
		attempt = 0
		c.log("registered")
		c.update(func(s *State) {
			s.Phase, s.ID, s.Code, s.ErrKind = PhaseWaiting, host.ID(), host.Code(), ""
		})
		again := c.serve(ctx, host)
		_ = host.Close()
		if !again {
			return
		}
	}
}

// serve waits for operators on one registration. It returns true to
// register again (after a session, or when the server link was lost).
func (c *Controller) serve(ctx context.Context, host HostLink) bool {
	for {
		sess, err := host.WaitForOperator(ctx)
		if ctx.Err() != nil {
			if sess != nil {
				_ = sess.Close()
			}
			return false
		}
		if err != nil {
			select {
			case <-host.Done():
				c.log("signalling lost: %v", err)
				return true
			default:
			}
			c.log("session setup failed: %v", err)
			c.mu.Lock()
			for k := range c.granted { // no clear builtin on Go 1.20 (nt6 line)
				delete(c.granted, k)
			}
			c.mu.Unlock()
			c.update(func(s *State) { s.Phase, s.Notice = PhaseWaiting, NoticeSetupFailed })
			continue
		}
		if !c.runSession(ctx, sess) {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		// One registration per session: the next helper gets a new ID.
		_ = host.Close()
		select {
		case <-c.ack:
		case <-ctx.Done():
			return false
		}
		c.update(func(s *State) {
			s.Notice = NoticeNone
			// Leaving the end screen forgets the report token.
			s.Report = ReportInfo{}
			c.rep = reportTarget{}
		})
		return true
	}
}

// runSession runs one accepted session to its end. It returns false when
// the session was not one the person accepted (then it is closed at once).
func (c *Controller) runSession(ctx context.Context, sess SessionLink) bool {
	c.mu.Lock()
	perms, ok := c.granted[sess.SID()]
	delete(c.granted, sess.SID())
	c.mu.Unlock()
	if !ok {
		c.log("session without an acceptance: closed")
		_ = sess.Close()
		return false
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	path, relayed := sess.Path()
	start := time.Now()
	c.mu.Lock()
	c.stop, c.localStop = cancel, false
	c.rep = reportTarget{sid: sess.SID(), token: sess.ReportToken()}
	c.mu.Unlock()
	c.update(func(s *State) {
		s.Phase = PhaseSession
		s.Session.Perms, s.Session.Path, s.Session.Relayed, s.Session.Started = perms, path, relayed, start
		s.Report = ReportInfo{Available: c.rep.token != ""}
	})
	c.log("session started (path %s)", path)

	// Drop a stale acknowledgement (a double click on the previous end
	// screen) so this session's end screen waits for a fresh one.
	select {
	case <-c.ack:
	default:
	}
	go c.watchPath(sctx, sess, path)
	q := make(chan chatOut, chatQueueLen)
	c.mu.Lock()
	c.chat.Clear()
	up := make(chan struct{})
	c.chatSend, c.chatQ, c.chatUp = nil, q, up
	c.mu.Unlock()
	go c.chatSender(sctx, q, up)
	err := c.b.RunSession(sctx, sess, perms, SessionEvents{
		FileReceived: c.fileReceived,
		ChatReceived: c.chatReceived,
		ChatReady:    c.chatReady,
	})
	cancel()
	_ = sess.Close()

	c.mu.Lock()
	c.stop = nil
	local := c.localStop
	// The transcript ends with the session.
	c.chat.Clear()
	c.chatSend, c.chatQ, c.chatUp = nil, nil, nil
	c.mu.Unlock()
	if ctx.Err() != nil {
		return true // the app is closing: no end screen
	}
	reason := EndPeer
	switch {
	case local:
		reason, err = EndLocal, nil
	case err != nil:
		reason = EndError
	case sess.Err() != nil && !errors.Is(sess.Err(), io.EOF):
		reason, err = EndLost, sess.Err()
	}
	c.log("session ended (reason %d): %v", reason, err)
	c.update(func(s *State) {
		s.Phase = PhaseEnded
		s.Ended = EndInfo{Operator: s.Session.Operator, Duration: time.Since(start), Reason: reason, Err: err}
		s.Session.Chat = nil
		if s.Report.Available {
			s.Report.Until = time.Now().Add(c.ReportWindow)
		}
	})
	return true
}

// SubmitReport sends a report about the operator of the current (or last)
// session in the background; done receives the result ("" when sent, else
// an error kind). With f.EndSession the session is stopped at once (the
// end screen follows). It returns ErrReportUnavailable (done is not
// called) when there is nothing to report: no token, already sent or being
// sent, or the end-screen window is over.
func (c *Controller) SubmitReport(f ReportForm, done func(kind string)) error {
	c.mu.Lock()
	rep := c.rep
	info := c.st.Report
	if rep.token == "" || rep.sent || rep.sending || !info.Open(time.Now()) {
		c.mu.Unlock()
		return ErrReportUnavailable
	}
	c.rep.sending = true
	if f.EndSession && c.stop != nil {
		c.localStop = true
		c.stop()
	}
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), c.ReportTimeout)
		err := c.b.Report(ctx, ReportRequest{SID: rep.sid, Token: rep.token, Reason: f.Reason, Text: f.Text, EndSession: f.EndSession})
		cancel()
		kind := reportErrKind(err)
		if err != nil {
			c.log("report not sent (%s): %v", kind, err)
		} else {
			c.log("report sent")
		}
		c.update(func(s *State) {
			if c.rep.sid != rep.sid {
				return // the end screen was left meanwhile
			}
			c.rep.sending = false
			if kind == "" {
				c.rep.sent = true
				s.Report.Sent = true
			}
		})
		if done != nil {
			done(kind)
		}
	}()
	return nil
}

// fileReceived records a file the operator sent (a session goroutine).
func (c *Controller) fileReceived(path string) {
	c.log("file received")
	c.update(func(s *State) {
		if s.Phase != PhaseSession {
			return
		}
		s.Session.FilesReceived++
		s.Session.LastFile, s.Session.LastFileDir = filepath.Base(path), filepath.Dir(path)
		s.Session.LastFileAt = time.Now()
	})
}

// chatReady records the session's chat sender (a session goroutine).
func (c *Controller) chatReady(send func(text string) error) {
	c.mu.Lock()
	if c.chatQ != nil && c.chatSend == nil {
		c.chatSend = send
		close(c.chatUp)
	}
	c.mu.Unlock()
}

// chatReceived adds an operator line to the transcript (a session
// goroutine).
func (c *Controller) chatReceived(text string) {
	c.update(func(s *State) {
		if c.chatQ == nil || s.Phase != PhaseSession {
			return
		}
		if _, ok := c.chat.Add(ChatFromOperator, text, time.Now()); ok {
			s.Session.Chat = c.chat.Messages()
		}
	})
}

// SendChat queues one line for the operator and adds it to the transcript;
// a line that then fails to go out is marked Failed. It does not block.
func (c *Controller) SendChat(text string) error {
	var err error
	c.update(func(s *State) {
		switch {
		case c.chatQ == nil || s.Phase != PhaseSession:
			err = ErrChatNoSession
			return
		case len(c.chatQ) == cap(c.chatQ):
			err = ErrChatBusy
			return
		}
		m, ok := c.chat.Add(ChatFromMe, text, time.Now())
		if !ok {
			err = ErrChatEmpty
			return
		}
		c.chatQ <- chatOut{id: m.ID, text: m.Text} // room checked above, under the lock
		s.Session.Chat = c.chat.Messages()
	})
	return err
}

// chatSender sends the queued lines in order until the session ends
// (lines typed before the engine is ready wait for it).
func (c *Controller) chatSender(ctx context.Context, q chan chatOut, up chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case <-up:
	}
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-q:
			c.mu.Lock()
			send := c.chatSend
			c.mu.Unlock()
			err := ErrChatNoSession
			if send != nil {
				err = send(m.text)
			}
			if err == nil {
				continue
			}
			c.log("chat line not sent: %v", err)
			c.update(func(s *State) {
				if c.chatQ != q || s.Phase != PhaseSession {
					return
				}
				c.chat.MarkFailed(m.id)
				s.Session.Chat = c.chat.Messages()
			})
		}
	}
}

// watchPath re-reads the network path while the session runs (ICE may move
// from relay to direct).
func (c *Controller) watchPath(ctx context.Context, sess SessionLink, last string) {
	t := time.NewTicker(c.PathRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Done():
			return
		case <-t.C:
			p, relayed := sess.Path()
			if p == last {
				continue
			}
			last = p
			c.update(func(s *State) {
				if s.Phase == PhaseSession {
					s.Session.Path, s.Session.Relayed = p, relayed
				}
			})
		}
	}
}

// approve is the registration's acceptance hook (a network goroutine).
func (c *Controller) approve(pctx context.Context, a Approval) bool {
	ctx, cancel := context.WithTimeout(pctx, c.ApprovalTimeout)
	defer cancel()
	a.Deadline = time.Now().Add(c.ApprovalTimeout)
	c.update(func(s *State) {
		s.Phase, s.Approval, s.Notice = PhaseApproval, a, NoticeNone
	})
	perms, ok := c.v.Ask(ctx, a)
	if ok && ctx.Err() == nil {
		c.mu.Lock()
		c.granted[a.SID] = perms
		c.mu.Unlock()
		c.update(func(s *State) {
			s.Phase = PhaseStarting
			s.Session = SessionInfo{Operator: a.OperatorName, Account: a.Account, Perms: perms}
		})
		return true
	}
	n := NoticeRefused
	switch {
	case pctx.Err() != nil:
		n = NoticeWithdrawn
	case ctx.Err() != nil:
		n = NoticeTimedOut
	}
	c.update(func(s *State) {
		if s.Phase == PhaseApproval {
			s.Phase, s.Notice = PhaseWaiting, n
		}
	})
	return false
}

// credentials is the registration's ID / code change hook.
func (c *Controller) credentials(id, code, reason string) {
	c.update(func(s *State) {
		s.ID, s.Code = id, code
		if s.Phase != PhaseWaiting {
			return
		}
		switch reason {
		case reasonLocked:
			s.Notice = NoticeLocked
		case reasonFailed:
			s.Notice = NoticeFailed
		}
	})
}
