package main

// receive_help.go — "Receive help": this Desktop is the helped computer of
// an Oblireach Support session, without the thin client.
// Like TeamViewer's "Allow remote control", it runs in the background from
// the start of the app while "Allow incoming support" is on (the default;
// the choice is kept in the support settings): it registers with the
// default support server (Settings › Support; the built-in public one when
// nothing else is set up; no operator key is needed to be helped) and the
// Dashboard shows the ID and the one-time code (help_page.go). The person
// reads them to the operator, accepts or refuses the operator who proved the
// code (the same SAS on both screens, control / clipboard / files toggles),
// and can stop the session at any time.
//
// The ID stays the same as long as the registration lives (sessions
// included). supportlink keeps the registration across a signalling drop:
// it reconnects by itself and gets the same ID back within the server's
// grace window (OnState: the status dot); past it, or after a server
// restart, a new ID comes (reason "new_id", said on screen), as when
// someone was guessing codes. The code changes after every session and
// every failed attempt (supportlink), and on demand (RotateCode, same ID).
//
// The one-time code never leaves this process except on screen: it is never
// written anywhere (settings, preferences, logs), never sent to the
// rendezvous server (the PAKE proves it without revealing it).
//
// The host side runs in this process: supportlink registers with the
// rendezvous server and sets up the peer-to-peer link, the ObliReach engine
// (agent/engine) streams the screen and injects the operator's input
// (receive_help_windows.go). The engine is process-wide (one capture, one
// input injector, one session per process): this controller is a singleton
// and nothing else in Desktop runs the engine; the operator side of Desktop
// ("Connect by code") only decodes in the page, it never touches the engine.
//
// The controller mirrors the thin client's (support/controller.go):
//
//	idle ─start─► connecting ──registered──► waiting ──operator proved the code──► approval
//	               │   ▲                      ▲  ▲                                   │   │
//	               │   └─ retry ◄ unreachable │  └──── refused / timed out / ────────┘   │ accepted
//	               └─error─►──────┘           │        withdrawn / setup failed          ▼
//	                                          │                                       starting
//	                                          │                                          │ link up
//	                                          └── (notice "ended", a new code) ◄──── session
//
// stop (the toggle turned off) ends everything and goes back to idle. While
// a session runs, the password vault is locked and held locked (onSession:
// an operator who controls this computer must not reach the saved secrets,
// nor unlock them with this person's Windows Hello).
//
// HTTP, under the support routes' gates (support.go serveHTTP: loopback
// peer, the local Origin required on every POST):
//
//	GET  /local/support/help/state?chat=<seq>   the state (+ chat lines after seq)
//	POST /local/support/help/incoming {on, ack?} "Allow incoming support" (ack: the notice read)
//	POST /local/support/help/scam-ack {}        the anti-scam notice was read
//	POST /local/support/help/start   {}         = incoming on
//	POST /local/support/help/stop    {}         = incoming off (ends a session)
//	POST /local/support/help/newcode {}         a new one-time code
//	POST /local/support/help/retry   {}         try the server again now
//	POST /local/support/help/end     {}         end the running session (a new code follows)
//	POST /local/support/help/decide  {sid, accept, control, clipboard, files}
//	POST /local/support/help/ack     {}         dismiss the last session's end notice
//	POST /local/support/help/chat    {text}     one chat line to the operator
//	POST /local/support/help/report  {reason, text, end}  report the operator (receive_help_report.go)
//
// Nothing about a session is recorded: the chat transcript lives in memory
// and is dropped with the next session or when receiving help stops.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oblireach/supportlink/wire"
)

// Phases (the page's screens).
const (
	helpIdle        = "idle"
	helpConnecting  = "connecting"
	helpUnreachable = "unreachable"
	helpWaiting     = "waiting"
	helpApproval    = "approval"
	helpStarting    = "starting"
	helpSession     = "session"
)

// Notices shown with the waiting screen (help.notice.<n>).
const (
	helpNoticeLocked      = "locked"      // someone guessed codes: new ID
	helpNoticeFailed      = "failed"      // a wrong-code attempt burnt the code
	helpNoticeRefused     = "refused"     // the person refused
	helpNoticeTimedOut    = "timedOut"    // no answer in time
	helpNoticeWithdrawn   = "withdrawn"   // the operator left during the decision
	helpNoticeSetupFailed = "setupFailed" // accepted, but the link did not come up
	helpNoticeEnded       = "ended"       // a session just ended (why: EndReason)
	helpNoticeNewID       = "newId"       // the server could not keep the ID: a new one
)

// Registration failure kinds (helpBackend.ErrKind).
const (
	helpErrUnreachable = "unreachable"
	helpErrRefused     = "refused" // banned, rate limited, server too old...
	helpErrUpgrade     = "upgrade" // this version is too old for the server
)

// End reasons (help.ended.<r>).
const (
	helpEndLocal = "local" // the person stopped it
	helpEndPeer  = "peer"  // the operator left
	helpEndLost  = "lost"  // the link failed
	helpEndError = "error" // the engine could not run
)

const (
	// helpChatMaxBytes caps one chat line (the engine's engine.ChatMaxBytes).
	helpChatMaxBytes = 4096
	// helpChatKeep: the transcript keeps this many lines (the oldest go).
	helpChatKeep = 500
	// helpCredReasonLocked / helpCredReasonFailed: supportlink.Reason*.
	helpCredReasonLocked = "locked"
	helpCredReasonFailed = "failed"
	helpCredReasonNewID  = "new_id"
	helpCredReasonManual = "manual"
)

// helpPerms: what the person grants for one session.
type helpPerms struct {
	Control   bool `json:"control"`
	Clipboard bool `json:"clipboard"`
	Files     bool `json:"files"`
}

// helpApprovalReq: an operator who proved the code (supportlink.ApprovalRequest).
type helpApprovalReq struct {
	SID      string
	Operator string // self-declared: shown as such
	Account  string // authenticated by the server ("" = none)
	SAS      string
}

type helpHooks struct {
	Approve       func(ctx context.Context, a helpApprovalReq) bool
	OnCredentials func(id, code, reason string) // must not block
	// OnState: the signalling link of the registration ("online",
	// "connecting" while supportlink reconnects, "closed"); must not block.
	OnState func(state string)
}

// Signalling states (supportlink.HostState).
const (
	helpLinkOnline     = "online"
	helpLinkConnecting = "connecting"
	helpLinkClosed     = "closed"
)

// helpPeerInfo: who the operator of an established session is
// (supportlink.Session.Peer), when the link says it.
type helpPeerInfo interface {
	PeerInfo() (name, account string)
}

// helpHostLink is a registered host (supportlink.Host).
type helpHostLink interface {
	ID() string
	Code() string
	WaitForOperator(ctx context.Context) (helpSessionLink, error)
	Done() <-chan struct{}
	Close() error
}

// helpSessionLink is an established session (supportlink.Session).
type helpSessionLink interface {
	WriteFrame(op byte, p []byte) error
	ReadFrame() (byte, []byte, error)
	Close() error
	SID() string
	Path() (path string, relayed bool)
	Done() <-chan struct{}
	Err() error
	// ReportToken authenticates one abuse report about this session (""
	// when the server takes no reports).
	ReportToken() string
}

// helpEvents: what a running session reports.
type helpEvents struct {
	OnChat       func(text string) // a chat line from the operator
	FileReceived func(path string) // a file the operator sent was saved
}

// helpRunner is one engine session (engine.Session).
type helpRunner interface {
	Run(ctx context.Context) error
	SendChat(text string) error
}

// helpBackend: the network and the engine (faked in tests).
type helpBackend interface {
	Register(ctx context.Context, server string, hooks helpHooks) (helpHostLink, error)
	NewSession(conn helpSessionLink, p helpPerms, ev helpEvents) helpRunner
	ErrKind(err error) string
	// Report sends an abuse report to the support server: nil when the
	// server has it, else one of the errHelpReport* errors (wrapped).
	Report(ctx context.Context, server string, r helpReportReq) error
}

type helpDecision struct {
	perms helpPerms
	ok    bool
}

type helpPending struct {
	req      helpApprovalReq
	deadline time.Time
	res      chan helpDecision
}

type helpChatLine struct {
	Seq  int    `json:"seq"`
	From string `json:"from"` // "op" (the operator) or "me"
	Text string `json:"text"`
	At   int64  `json:"at"` // unix ms
}

// helpState: everything the page renders.
type helpState struct {
	Phase    string
	ServerID string
	Server   string // label
	ID, Code string
	Notice   string
	ErrKind  string
	RetryAt  time.Time

	Operator, Account string
	Perms             helpPerms
	Path              string
	Relayed           bool
	Started           time.Time
	Files             int
	LastFile          string
	LastFileAt        time.Time

	// Link: the registration's signalling link (helpLink*; "" = online).
	Link string

	// The last session's end (Notice helpNoticeEnded).
	EndReason   string
	EndDuration time.Duration
	EndError    string
}

// helpCodeRotator: a registration that can draw a new code on demand
// (keeping its ID). Without it, a new code means a new registration.
type helpCodeRotator interface {
	RotateCode() bool
}

// helpHost drives "Receive help" (one per process: the engine is).
type helpHost struct {
	b        helpBackend
	settings *supportSettings
	now      func() time.Time
	// onSession runs when a session starts (true) and ends (false): the
	// vault held locked, the window flashed (receive_help_windows.go).
	onSession func(on bool)
	// onAttention: an operator asks to connect (bring the window forward).
	onAttention func(kind string)

	ApprovalTimeout time.Duration
	Backoff         []time.Duration
	UpgradeRetry    time.Duration
	PathRefresh     time.Duration
	// ReportWindow: how long after its end a session can be reported (the
	// server accepts it for longer); ReportTimeout bounds one sending.
	ReportWindow  time.Duration
	ReportTimeout time.Duration

	mu        sync.Mutex
	st        helpState
	gen       int // a new value per session (the page resets its transcript)
	cancel    context.CancelFunc
	runCtx    context.Context // receiving help (ends with stop)
	done      chan struct{}
	serverURL string       // the server registered with
	host      helpHostLink // the live registration (nil: none)
	granted   map[string]helpPerms
	pending   *helpPending
	sessStop  context.CancelFunc
	localStop bool
	runner    helpRunner
	retry     chan struct{} // "try again now" while unreachable
	chat      []helpChatLine
	chatSeq   int
	// rep: the session that can be reported (memory only).
	rep helpReportTarget

	// The preferences (support settings), read once.
	prefsRead bool
	incoming  bool
	scamAck   bool
}

func newHelpHost(settings *supportSettings, b helpBackend) *helpHost {
	return &helpHost{
		b: b, settings: settings, now: time.Now,
		ApprovalTimeout: 90 * time.Second,
		Backoff:         []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second},
		UpgradeRetry:    5 * time.Minute,
		PathRefresh:     2 * time.Second,
		ReportWindow:    10 * time.Minute,
		ReportTimeout:   30 * time.Second,
		st:              helpState{Phase: helpIdle},
		granted:         map[string]helpPerms{},
		retry:           make(chan struct{}, 1),
	}
}

// helpError: a request refused (its code: help_page.go HELP_ERR).
type helpError struct{ Code string }

func (e *helpError) Error() string { return "receive help: " + e.Code }

func (h *helpHost) update(f func(s *helpState)) {
	h.mu.Lock()
	f(&h.st)
	h.mu.Unlock()
}

func (h *helpHost) phase() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.st.Phase
}

// loadPrefsLocked: "Allow incoming support" and the notice's acknowledgement,
// read from the settings the first time.
func (h *helpHost) loadPrefsLocked() {
	if h.prefsRead {
		return
	}
	h.prefsRead = true
	h.incoming, h.scamAck, _ = h.settings.prefs()
}

// prefs: "Allow incoming support", the anti-scam notice read.
func (h *helpHost) prefs() (incoming, scamAck bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadPrefsLocked()
	return h.incoming, h.scamAck
}

// autoStart: at the app's start, receiving help runs if it is allowed.
func (h *helpHost) autoStart() {
	if on, _ := h.prefs(); on {
		_ = h.startDefault()
	}
}

// startDefault registers with the default support server.
func (h *helpHost) startDefault() error {
	sv, err := h.settings.defaultServer()
	if err != nil {
		// Unreadable settings: the built-in public server still works.
		sv = supportServer{ID: supportPublicID, Label: supportPublicLabel, URL: supportPublicURL}
	}
	return h.startWith(sv)
}

// setIncoming turns "Allow incoming support" on or off (kept in the
// settings); ack: the anti-scam notice was read.
func (h *helpHost) setIncoming(on, ack bool) error {
	h.mu.Lock()
	h.loadPrefsLocked()
	h.incoming = on
	if ack {
		h.scamAck = true
	}
	h.mu.Unlock()
	var ackp *bool
	if ack {
		ackp = &ack
	}
	perr := h.settings.setPrefs(&on, ackp)
	if on {
		if err := h.startDefault(); err != nil {
			var he *helpError
			if !errors.As(err, &he) || he.Code != "busy" {
				return err
			}
		}
	} else {
		h.stop()
	}
	if perr != nil {
		return &helpError{"storage"}
	}
	return nil
}

// acknowledgeScam: the anti-scam notice was read (kept: shown once).
func (h *helpHost) acknowledgeScam() error {
	h.mu.Lock()
	h.loadPrefsLocked()
	h.scamAck = true
	h.mu.Unlock()
	t := true
	if err := h.settings.setPrefs(nil, &t); err != nil {
		return &helpError{"storage"}
	}
	return nil
}

// settingsChanged: the support servers changed; a new default server (or a
// new address for it) is followed, unless someone is being helped or asks
// to (it follows at the next registration).
func (h *helpHost) settingsChanged() {
	sv, err := h.settings.defaultServer()
	if err != nil {
		return
	}
	h.mu.Lock()
	running := h.cancel != nil
	same := h.st.ServerID == sv.ID && h.serverURL == sv.URL
	busy := h.runner != nil || h.pending != nil || h.st.Phase == helpApproval || h.st.Phase == helpStarting || h.st.Phase == helpSession
	h.mu.Unlock()
	if !running {
		return
	}
	if same {
		h.update(func(s *helpState) { s.Server = sv.Label })
		return
	}
	if busy {
		return
	}
	h.stop()
	_ = h.startWith(sv)
}

// start registers with server (a support server's id from the settings).
func (h *helpHost) start(serverID string) error {
	sv, ok, err := h.settings.get(serverID)
	if err != nil {
		return &helpError{"storage"}
	}
	if !ok {
		return &helpError{"no_server"}
	}
	return h.startWith(sv)
}

func (h *helpHost) startWith(sv supportServer) error {
	h.mu.Lock()
	if h.cancel != nil {
		h.mu.Unlock()
		return &helpError{"busy"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.cancel, h.done, h.runCtx, h.serverURL = cancel, done, ctx, sv.URL
	h.st = helpState{Phase: helpConnecting, ServerID: sv.ID, Server: sv.Label}
	h.chat, h.chatSeq = nil, 0
	h.rep = helpReportTarget{}
	clear(h.granted)
	select {
	case <-h.retry:
	default:
	}
	h.mu.Unlock()
	go func() {
		defer close(done)
		h.run(ctx, sv.URL)
		h.mu.Lock()
		h.cancel, h.done, h.pending, h.host = nil, nil, nil, nil
		h.st = helpState{Phase: helpIdle, ServerID: h.st.ServerID, Server: h.st.Server}
		h.chat = nil
		h.rep = helpReportTarget{} // (no end notice any more: nothing to report)
		h.mu.Unlock()
	}()
	return nil
}

// stop ends receiving help (the session too) and waits briefly for the
// registration to close.
func (h *helpHost) stop() {
	h.mu.Lock()
	cancel, done := h.cancel, h.done
	if h.sessStop != nil {
		h.localStop = true
	}
	h.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// endSession ends the running session at once (the person's Stop).
func (h *helpHost) endSession() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessStop == nil {
		return false
	}
	h.localStop = true
	h.sessStop()
	return true
}

// acknowledge dismisses the last session's end notice.
func (h *helpHost) acknowledge() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.st.Notice != helpNoticeEnded {
		return false
	}
	h.st.Notice = ""
	// Leaving the end notice forgets the report token (a report being
	// sent still goes; its answer is then dropped).
	h.rep = helpReportTarget{}
	return true
}

// newCode draws a new one-time code while waiting for an operator: the
// registration's own (same ID) when it can, else a new registration (a new
// ID too).
func (h *helpHost) newCode() bool {
	h.mu.Lock()
	host := h.host
	ok := host != nil && h.st.Phase == helpWaiting && h.pending == nil
	if ok {
		h.st.Notice = ""
	}
	h.mu.Unlock()
	if !ok {
		return false
	}
	if r, can := host.(helpCodeRotator); can {
		// The same ID; the new code comes through OnCredentials. Refused
		// while an operator exchange runs (then nothing changes).
		return r.RotateCode()
	}
	h.update(func(s *helpState) { s.Phase, s.ID, s.Code = helpConnecting, "", "" })
	_ = host.Close() // (serve sees it gone: a new registration)
	return true
}

// retryNow: while the server cannot be reached, try again at once.
func (h *helpHost) retryNow() bool {
	if h.phase() != helpUnreachable {
		return false
	}
	select {
	case h.retry <- struct{}{}:
	default:
	}
	return true
}

// decide answers the pending acceptance request sid.
func (h *helpHost) decide(sid string, accept bool, p helpPerms) bool {
	h.mu.Lock()
	pd := h.pending
	if pd == nil || pd.req.SID != sid {
		h.mu.Unlock()
		return false
	}
	h.pending = nil
	h.mu.Unlock()
	select {
	case pd.res <- helpDecision{perms: p, ok: accept}:
	default:
	}
	return true
}

func (h *helpHost) run(ctx context.Context, server string) {
	attempt := 0
	hooks := helpHooks{Approve: h.approve, OnCredentials: h.credentials, OnState: h.linkState}
	for ctx.Err() == nil {
		h.update(func(s *helpState) { s.Phase, s.ID, s.Code, s.ErrKind = helpConnecting, "", "", "" })
		host, err := h.b.Register(ctx, server, hooks)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			kind := h.b.ErrKind(err)
			d := h.Backoff[min(attempt, len(h.Backoff)-1)]
			if kind == helpErrUpgrade {
				d = h.UpgradeRetry
			}
			attempt++
			h.update(func(s *helpState) {
				s.Phase, s.ErrKind, s.RetryAt, s.Notice = helpUnreachable, kind, h.now().Add(d), ""
			})
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-h.retry:
				t.Stop()
			case <-t.C:
			}
			continue
		}
		attempt = 0
		h.mu.Lock()
		h.host = host
		notice := h.st.Notice
		if notice != helpNoticeEnded {
			notice = ""
		}
		h.st.Phase, h.st.ID, h.st.Code, h.st.ErrKind, h.st.Notice, h.st.Link = helpWaiting, host.ID(), host.Code(), "", notice, helpLinkOnline
		h.mu.Unlock()
		again := h.serve(ctx, host)
		h.mu.Lock()
		if h.host == host {
			h.host = nil
		}
		h.mu.Unlock()
		_ = host.Close()
		if !again {
			return
		}
	}
}

// serve waits for operators on one registration (sessions included: the ID
// stays; signalling drops are supportlink's to repair); true to register
// again (the registration was closed: replaced by "New code" without
// RotateCode, or given up by supportlink).
func (h *helpHost) serve(ctx context.Context, host helpHostLink) bool {
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
				return true
			default:
			}
			h.mu.Lock()
			clear(h.granted)
			h.mu.Unlock()
			h.update(func(s *helpState) { s.Phase, s.Notice = helpWaiting, helpNoticeSetupFailed })
			continue
		}
		if !h.runSession(ctx, sess) {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		select {
		case <-host.Done():
			return true // (lost during the session: a new registration)
		default:
		}
		// The same registration goes on: its ID, the new code supportlink
		// drew when the session was accepted.
		h.update(func(s *helpState) { s.ID, s.Code = host.ID(), host.Code() })
	}
}

// runSession runs one accepted session to its end; false when the session
// was not one the person accepted (closed at once).
func (h *helpHost) runSession(ctx context.Context, sess helpSessionLink) bool {
	h.mu.Lock()
	perms, ok := h.granted[sess.SID()]
	delete(h.granted, sess.SID())
	h.mu.Unlock()
	if !ok {
		_ = sess.Close()
		return false
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	path, relayed := sess.Path()
	start := h.now()
	if p, ok := sess.(helpPeerInfo); ok {
		name, account := p.PeerInfo()
		name, account = cleanSupportText(name, supportMaxName), cleanSupportText(account, 128)
		h.update(func(s *helpState) {
			if s.Operator == "" {
				s.Operator = name
			}
			if s.Account == "" {
				s.Account = account
			}
		})
	}
	runner := h.b.NewSession(sess, perms, helpEvents{OnChat: h.chatIn, FileReceived: h.fileReceived})
	h.mu.Lock()
	h.sessStop, h.localStop, h.runner = cancel, false, runner
	h.gen++
	h.chat, h.chatSeq = nil, 0
	h.st.Phase, h.st.Notice = helpSession, ""
	h.st.Perms, h.st.Path, h.st.Relayed, h.st.Started = perms, path, relayed, start
	h.st.Files, h.st.LastFile, h.st.LastFileAt = 0, "", time.Time{}
	// The operator of this session can be reported (the previous one's
	// token is forgotten).
	h.rep = helpReportTarget{sid: sess.SID(), token: sess.ReportToken(), server: h.serverURL,
		operator: h.st.Operator, account: h.st.Account, live: true}
	h.mu.Unlock()
	if h.onSession != nil {
		h.onSession(true)
	}
	go h.watchPath(sctx, sess, path)
	err := runner.Run(sctx)
	cancel()
	_ = sess.Close()
	if h.onSession != nil {
		h.onSession(false)
	}

	h.mu.Lock()
	h.sessStop, h.runner = nil, nil
	local := h.localStop
	h.chat = nil
	if ctx.Err() != nil {
		h.rep = helpReportTarget{} // (no end notice: nothing to report from)
	}
	h.mu.Unlock()
	if ctx.Err() != nil {
		return true // receiving help stops: no end notice
	}
	reason, errText := helpEndPeer, ""
	switch {
	case local:
		reason = helpEndLocal
	case err != nil:
		reason, errText = helpEndError, err.Error()
	case sess.Err() != nil && !errors.Is(sess.Err(), io.EOF):
		reason = helpEndLost
	}
	h.update(func(s *helpState) {
		s.Phase, s.Notice = helpWaiting, helpNoticeEnded
		s.EndReason, s.EndDuration, s.EndError = reason, h.now().Sub(start), errText
		// The report stays offered with the end notice, for ReportWindow.
		if h.rep.sid == sess.SID() && h.rep.live {
			h.rep.live, h.rep.until = false, h.now().Add(h.ReportWindow)
		}
	})
	return true
}

func (h *helpHost) watchPath(ctx context.Context, sess helpSessionLink, last string) {
	t := time.NewTicker(h.PathRefresh)
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
			h.update(func(s *helpState) {
				if s.Phase == helpSession {
					s.Path, s.Relayed = p, relayed
				}
			})
		}
	}
}

// approve: the registration's acceptance hook (a network goroutine). The
// page shows the request (any page: help_page.go) and answers with decide.
func (h *helpHost) approve(pctx context.Context, a helpApprovalReq) bool {
	// One session at a time (the engine is process-wide): another operator
	// while someone helps, or while a request is on screen, is refused.
	h.mu.Lock()
	busy := h.runner != nil || h.sessStop != nil || h.pending != nil || h.st.Phase == helpStarting || h.st.Phase == helpSession
	h.mu.Unlock()
	if busy {
		return false
	}
	ctx, cancel := context.WithTimeout(pctx, h.ApprovalTimeout)
	defer cancel()
	pd := &helpPending{req: a, deadline: h.now().Add(h.ApprovalTimeout), res: make(chan helpDecision, 1)}
	a.Operator = cleanSupportText(a.Operator, supportMaxName)
	a.Account = cleanSupportText(a.Account, 128)
	pd.req = a
	h.mu.Lock()
	h.pending = pd
	h.st.Phase, h.st.Notice = helpApproval, ""
	stopped := context.Background().Done()
	if h.runCtx != nil {
		stopped = h.runCtx.Done()
	}
	h.mu.Unlock()
	if h.onAttention != nil {
		h.onAttention("approval")
	}
	var d helpDecision
	select {
	case d = <-pd.res:
	case <-ctx.Done():
	case <-stopped: // receiving help stopped: refused
	}
	h.mu.Lock()
	if h.pending == pd {
		h.pending = nil
	}
	h.mu.Unlock()
	if d.ok && ctx.Err() == nil && !helpClosed(stopped) {
		h.mu.Lock()
		h.granted[a.SID] = d.perms
		h.st.Phase = helpStarting
		h.st.Operator, h.st.Account, h.st.Perms = a.Operator, a.Account, d.perms
		h.mu.Unlock()
		return true
	}
	n := helpNoticeRefused
	switch {
	case pctx.Err() != nil:
		n = helpNoticeWithdrawn
	case ctx.Err() != nil:
		n = helpNoticeTimedOut
	}
	h.update(func(s *helpState) {
		if s.Phase == helpApproval {
			s.Phase, s.Notice = helpWaiting, n
		}
	})
	return false
}

func helpClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// credentials: the registration's ID / code change hook.
func (h *helpHost) credentials(id, code, reason string) {
	h.update(func(s *helpState) {
		s.ID, s.Code = id, code
		if s.Phase != helpWaiting {
			return
		}
		switch reason {
		case helpCredReasonLocked:
			s.Notice = helpNoticeLocked
		case helpCredReasonFailed:
			s.Notice = helpNoticeFailed
		case helpCredReasonNewID:
			s.Notice = helpNoticeNewID
		case helpCredReasonManual:
			if s.Notice != helpNoticeEnded {
				s.Notice = ""
			}
		}
	})
}

// linkState: the registration's signalling link (supportlink reconnects by
// itself; the ID and the code stay on screen meanwhile).
func (h *helpHost) linkState(state string) {
	h.update(func(s *helpState) { s.Link = state })
}

func (h *helpHost) fileReceived(path string) {
	h.update(func(s *helpState) {
		if s.Phase != helpSession {
			return
		}
		s.Files++
		s.LastFile, s.LastFileAt = filepath.Base(path), h.now()
	})
}

// cleanChatText: a chat line as kept and shown: no control characters
// except line breaks, at most helpChatMaxBytes, not blank.
func cleanChatText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > helpChatMaxBytes {
		n := helpChatMaxBytes
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return s
}

func (h *helpHost) addChatLocked(from, text string) {
	h.chatSeq++
	h.chat = append(h.chat, helpChatLine{Seq: h.chatSeq, From: from, Text: text, At: h.now().UnixMilli()})
	if len(h.chat) > helpChatKeep {
		h.chat = append([]helpChatLine(nil), h.chat[len(h.chat)-helpChatKeep:]...)
	}
}

// chatIn: a line from the operator (a session goroutine).
func (h *helpHost) chatIn(text string) {
	text = cleanChatText(text)
	if text == "" {
		return
	}
	h.mu.Lock()
	if h.runner != nil {
		h.addChatLocked("op", text)
	}
	h.mu.Unlock()
}

// sendChat: a line from the person to the operator.
func (h *helpHost) sendChat(text string) error {
	if len(text) > helpChatMaxBytes {
		return &helpError{"too_long"}
	}
	text = cleanChatText(text)
	if text == "" {
		return &helpError{"empty"}
	}
	h.mu.Lock()
	r := h.runner
	h.mu.Unlock()
	if r == nil {
		return &helpError{"no_session"}
	}
	if err := r.SendChat(text); err != nil {
		return &helpError{"chat_failed"}
	}
	h.mu.Lock()
	if h.runner == r {
		h.addChatLocked("me", text)
	}
	h.mu.Unlock()
	return nil
}

func (h *helpHost) sessionGen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gen
}

// ── The page's view ─────────────────────────────────────────────────────────

type helpApprovalView struct {
	SID       string `json:"sid"`
	Operator  string `json:"operator"`
	Account   string `json:"account,omitempty"`
	SAS       string `json:"sas"`
	Remaining int    `json:"remaining"` // seconds left to decide
}

type helpSessionView struct {
	Operator string    `json:"operator"`
	Account  string    `json:"account,omitempty"`
	Perms    helpPerms `json:"perms"`
	Path     string    `json:"path"`
	Relayed  bool      `json:"relayed"`
	Seconds  int       `json:"seconds"` // since it started
	Files    int       `json:"files"`
	LastFile string    `json:"lastFile,omitempty"`
	FileAgo  int       `json:"fileAgo,omitempty"` // seconds since the last file
}

type helpEndedView struct {
	Operator string `json:"operator"`
	Reason   string `json:"reason"`
	Seconds  int    `json:"seconds"`
}

type helpView struct {
	Phase    string            `json:"phase"`
	Gen      int               `json:"gen"`
	ServerID string            `json:"serverId,omitempty"`
	Server   string            `json:"server,omitempty"`
	ID       string            `json:"id,omitempty"`
	Code     string            `json:"code,omitempty"`
	Notice   string            `json:"notice,omitempty"`
	ErrKind  string            `json:"errKind,omitempty"`
	RetryIn  int               `json:"retryIn,omitempty"`
	Approval *helpApprovalView `json:"approval,omitempty"`
	Session  *helpSessionView  `json:"session,omitempty"`
	Ended    *helpEndedView    `json:"ended,omitempty"` // (notice "ended": the last session)
	Chat     []helpChatLine    `json:"chat"`
	ChatSeq  int               `json:"chatSeq"`
	ChatOn   bool              `json:"chatOn"`
	// Link: "connecting" while the registration reconnects (else omitted).
	Link string `json:"link,omitempty"`
	// Incoming: "Allow incoming support"; ScamAck: the notice was read.
	Incoming bool `json:"incoming"`
	ScamAck  bool `json:"scamAck"`
	// Report: the operator of the current (or last) session can be
	// reported (receive_help_report.go); omitted without a token.
	Report *helpReportView `json:"report,omitempty"`
}

func secondsCeil(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// view: the state, with the chat lines after since.
func (h *helpHost) view(since int) helpView {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	s := h.st
	h.loadPrefsLocked()
	v := helpView{Phase: s.Phase, Gen: h.gen, ServerID: s.ServerID, Server: s.Server, ID: s.ID, Code: s.Code,
		Notice: s.Notice, ErrKind: s.ErrKind, ChatSeq: h.chatSeq, Chat: []helpChatLine{},
		Incoming: h.incoming, ScamAck: h.scamAck}
	if s.Link != "" && s.Link != helpLinkOnline {
		v.Link = s.Link
	}
	switch s.Phase {
	case helpUnreachable:
		v.RetryIn = secondsCeil(s.RetryAt.Sub(now))
	case helpApproval:
		if pd := h.pending; pd != nil {
			v.Approval = &helpApprovalView{SID: pd.req.SID, Operator: pd.req.Operator, Account: pd.req.Account, SAS: pd.req.SAS,
				Remaining: secondsCeil(pd.deadline.Sub(now))}
		}
	case helpStarting, helpSession:
		sv := &helpSessionView{Operator: s.Operator, Account: s.Account, Perms: s.Perms, Path: s.Path, Relayed: s.Relayed}
		if s.Phase == helpSession {
			sv.Seconds = int(now.Sub(s.Started) / time.Second)
			sv.Files, sv.LastFile = s.Files, s.LastFile
			if s.LastFile != "" {
				sv.FileAgo = int(now.Sub(s.LastFileAt) / time.Second)
			}
		}
		v.Session = sv
	}
	if s.Notice == helpNoticeEnded {
		v.Ended = &helpEndedView{Operator: s.Operator, Reason: s.EndReason, Seconds: int(s.EndDuration / time.Second)}
	}
	v.ChatOn = h.runner != nil
	v.Report = h.reportViewLocked(now)
	if s.Phase == helpSession {
		for _, l := range h.chat {
			if l.Seq > since {
				v.Chat = append(v.Chat, l)
			}
		}
	}
	return v
}

// ── HTTP ────────────────────────────────────────────────────────────────────

// serveHTTP answers /local/support/help/<path> (support.go has checked the
// peer and, for a POST, the Origin).
func (h *helpHost) serveHTTP(w http.ResponseWriter, r *http.Request, path string) {
	fail := func(err error) {
		code := "failed"
		var he *helpError
		if errors.As(err, &he) {
			code = he.Code
		}
		status := http.StatusBadRequest
		if code == "storage" {
			status = http.StatusInternalServerError
		}
		supportJSON(w, status, map[string]string{"error": code})
	}
	switch {
	case path == "state" && r.Method == http.MethodGet:
		// The lines after chat=<seq> of the page's session gen=<g>; all of
		// them for another session.
		q := r.URL.Query()
		since, _ := strconv.Atoi(q.Get("chat"))
		if g, err := strconv.Atoi(q.Get("gen")); err != nil || g != h.sessionGen() {
			since = 0
		}
		supportJSON(w, http.StatusOK, h.view(since))
	case (path == "incoming" || path == "start" || path == "stop") && r.Method == http.MethodPost:
		var in struct {
			On  bool `json:"on"`
			Ack bool `json:"ack"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		if path != "incoming" {
			in.On = path == "start"
		}
		if err := h.setIncoming(in.On, in.Ack); err != nil {
			fail(err)
			return
		}
		supportJSON(w, http.StatusOK, h.view(0))
	case path == "scam-ack" && r.Method == http.MethodPost:
		if err := h.acknowledgeScam(); err != nil {
			fail(err)
			return
		}
		supportJSON(w, http.StatusOK, h.view(0))
	case path == "newcode" && r.Method == http.MethodPost:
		supportJSON(w, http.StatusOK, map[string]bool{"ok": h.newCode()})
	case path == "retry" && r.Method == http.MethodPost:
		supportJSON(w, http.StatusOK, map[string]bool{"ok": h.retryNow()})
	case path == "end" && r.Method == http.MethodPost:
		supportJSON(w, http.StatusOK, map[string]bool{"ok": h.endSession()})
	case path == "ack" && r.Method == http.MethodPost:
		supportJSON(w, http.StatusOK, map[string]bool{"ok": h.acknowledge()})
	case path == "decide" && r.Method == http.MethodPost:
		var in struct {
			SID       string `json:"sid"`
			Accept    bool   `json:"accept"`
			Control   bool   `json:"control"`
			Clipboard bool   `json:"clipboard"`
			Files     bool   `json:"files"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		supportJSON(w, http.StatusOK, map[string]bool{"ok": h.decide(in.SID, in.Accept, helpPerms{Control: in.Control, Clipboard: in.Clipboard, Files: in.Files})})
	case path == "chat" && r.Method == http.MethodPost:
		var in struct {
			Text string `json:"text"`
		}
		// (a 4096-byte line, escaped: more than supportDecode's 8 KiB)
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			supportJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if err := h.sendChat(in.Text); err != nil {
			fail(err)
			return
		}
		supportJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case path == "report" && r.Method == http.MethodPost:
		var in struct {
			Reason string `json:"reason"`
			Text   string `json:"text"`
			End    bool   `json:"end"`
		}
		// (1000 characters, escaped: more than supportDecode's 8 KiB)
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			supportJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
		if !wire.ValidReportReason(in.Reason) {
			supportJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_reason"})
			return
		}
		if utf8.RuneCountInString(in.Text) > wire.ReportMaxText {
			supportJSON(w, http.StatusBadRequest, map[string]string{"error": "too_long"})
			return
		}
		// The text is never logged nor kept: it only goes to the server.
		if kind := h.submitReport(in.Reason, in.Text, in.End); kind != "" {
			supportJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "kind": kind})
			return
		}
		supportJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		// (a platform's own settings: receive_help_linux.go)
		if helpPlatformHTTP != nil && helpPlatformHTTP(w, r, path) {
			return
		}
		supportJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// helpPlatformHTTP: the requests of /local/support/help/ a platform answers
// itself (true: answered); nil on Windows.
var helpPlatformHTTP func(w http.ResponseWriter, r *http.Request, path string) bool
