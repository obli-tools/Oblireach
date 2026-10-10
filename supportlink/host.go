package supportlink

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/oblireach/supportlink/wire"
)

// HostConfig configures the assisted side.
type HostConfig struct {
	// Server is the rendezvous server ("rdv.example.com" or a wss:// URL).
	Server string
	SignalOptions
	// ClientVersion is announced to the server (minimum version checks).
	ClientVersion string

	// Approve is called once an operator has proven knowledge of the code.
	// It must ask the person and return true only on an explicit "allow".
	// ctx is cancelled if the operator leaves or the server expires the
	// request (120 s). Required.
	Approve func(ctx context.Context, req ApprovalRequest) bool

	// OnCredentials is called (from the host goroutine; do not block) every
	// time the ID or the code changes after Register returned.
	OnCredentials func(CredentialsChange)

	// OnState is called (from the host goroutine; do not block) when the
	// signalling link drops (HostConnecting) and when it is back
	// (HostOnline). Meanwhile the host reconnects with backoff and asks
	// for its ID back: the server keeps it for a grace window (10 min by
	// default), so a network blip does not change the ID. Past the window
	// (or after a server restart) a new ID comes through OnCredentials
	// (ReasonNewID).
	OnState func(HostState)
	// DisableReconnect restores the former behaviour: a lost signalling
	// link closes the host (Done, ErrHostClosed).
	DisableReconnect bool

	ICE ICEOptions
	// Logger is a *log/slog.Logger (golang.org/x/exp/slog on the nt6
	// line, Go 1.20); nil discards.
	Logger *slogLogger

	// CodeLifetime rotates an unused code (0 = 10 min).
	CodeLifetime time.Duration
	// PakeTimeout: an operator that does not confirm the key within this
	// delay counts as a failed attempt (0 = 20 s).
	PakeTimeout time.Duration
}

// ApprovalRequest is what the acceptance window shows.
type ApprovalRequest struct {
	SID string
	// OperatorName is self-declared by the operator: display it as such.
	OperatorName string
	// Account is the operator identity authenticated by the server: the
	// label of a static instance key, or for an operator account
	// "Name <email>" where the email was verified by the server ("Name" is
	// chosen by the operator), with " (unverified)" appended when the
	// instance could not verify the address. At most 64 characters.
	Account string
	// SAS is the verification code also shown to the operator.
	SAS string
}

// Reasons for a credentials change.
const (
	ReasonUsed     = "used"     // a session was accepted: the code is spent
	ReasonRejected = "rejected" // the person refused: the code is spent
	ReasonFailed   = "failed"   // wrong code, abandoned or unconfirmed exchange
	ReasonExpired  = "expired"  // unused for CodeLifetime
	ReasonLocked   = "locked"   // someone was guessing: new ID and new code
	ReasonManual   = "manual"   // RotateCode: the person asked for a new code
	ReasonNewID    = "new_id"   // reconnected after the grace window: new ID and new code
)

// HostState is the signalling state of a Host.
type HostState string

const (
	HostOnline     HostState = "online"     // registered: operators can connect
	HostConnecting HostState = "connecting" // signalling lost, reconnecting (the ID is kept when possible)
	HostClosed     HostState = "closed"     // Close was called (or the link was lost with DisableReconnect)
)

// CredentialsChange carries the new ID and code to display.
type CredentialsChange struct {
	ID, Code string
	Reason   string
}

// ErrHostClosed is returned by WaitForOperator once the host is closed (or,
// with HostConfig.DisableReconnect, its signalling connection is lost: the
// ID is gone, Register again; that case also matches ErrUnreachable).
var ErrHostClosed = errors.New("supportlink: host closed")

// Host is a registered assisted machine waiting for an operator.
type Host struct {
	cfg HostConfig
	log *slogLogger
	sig *sigConn

	mu     sync.Mutex
	id     string
	code   string
	resume string // resume secret of id (memory only)
	state  HostState

	results   chan hostResult
	events    chan func()
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error

	// owned by the run goroutine
	pending map[string]*hostPending
}

type hostResult struct {
	sess *Session
	err  error
}

type hostPending struct {
	sid       string
	keys      sessionKeys
	confirmed bool
	accepted  bool
	cancel    context.CancelFunc // approval in progress
	timer     *time.Timer
	peer      rtcPeer
	sess      *Session
	remoteSet bool
	queued    []string
	delivered bool
	operator  PeerInfo // what the person accepted (Session.Peer)
	report    string   // abuse-report token of the session (Session.ReportToken)
}

// Register connects to the rendezvous server, obtains an ephemeral ID and
// draws the first one-time code. Nothing is written to disk. The host then
// keeps its signalling link up by itself (see HostConfig.OnState).
func Register(ctx context.Context, cfg HostConfig) (*Host, error) {
	if cfg.Approve == nil {
		return nil, errors.New("supportlink: HostConfig.Approve is required")
	}
	if cfg.CodeLifetime <= 0 {
		cfg.CodeLifetime = 10 * time.Minute
	}
	if cfg.PakeTimeout <= 0 {
		cfg.PakeTimeout = 20 * time.Second
	}
	if cfg.ICE.ConnectTimeout <= 0 {
		cfg.ICE.ConnectTimeout = 30 * time.Second
	}
	sc, err := dialSignal(ctx, cfg.Server, cfg.SignalOptions, wire.Msg{Role: wire.RoleHost, Ver: cfg.ClientVersion})
	if err != nil {
		return nil, err
	}
	if err := sc.send(wire.Msg{T: wire.TRegister}); err != nil {
		sc.close()
		return nil, err
	}
	// Wait for the ID before starting the loop.
	for {
		select {
		case <-ctx.Done():
			sc.close()
			return nil, ctx.Err()
		case m, ok := <-sc.in:
			if !ok {
				return nil, fmt.Errorf("%w: connection closed during registration", ErrUnreachable)
			}
			switch m.T {
			case wire.TRegistered:
				id := wire.Digits(m.ID)
				if !wire.ValidID(id) {
					sc.close()
					return nil, errors.New("supportlink: server sent a malformed ID")
				}
				h := &Host{
					cfg: cfg, log: logger(cfg.Logger), sig: sc,
					id: id, code: NewCode(), resume: m.Resume, state: HostOnline,
					results: make(chan hostResult, 1),
					events:  make(chan func(), 16),
					done:    make(chan struct{}),
					pending: map[string]*hostPending{},
				}
				go h.run()
				return h, nil
			case wire.TError:
				sc.close()
				return nil, serverError(m)
			}
		}
	}
}

// ID is the current host ID (9 digits, no separators).
func (h *Host) ID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.id
}

// Code is the current one-time code (9 digits, no separators).
func (h *Host) Code() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.code
}

// WaitForOperator blocks until an operator has been accepted and the
// peer-to-peer link is up, and returns the session. A setup failure after
// acceptance (MITM detected, no network path) is returned as an error; the
// host stays registered and WaitForOperator may be called again. Only one
// session runs at a time: further operators get "busy" until it ends.
func (h *Host) WaitForOperator(ctx context.Context) (*Session, error) {
	select {
	case r := <-h.results:
		return r.sess, r.err
	case <-h.done:
		select {
		case r := <-h.results:
			return r.sess, r.err
		default:
		}
		return nil, h.closeErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Done is closed when the host is closed or loses its signalling link.
func (h *Host) Done() <-chan struct{} { return h.done }

// Close unregisters the host. Established sessions are not closed.
func (h *Host) Close() error {
	h.shutdown(ErrHostClosed)
	return nil
}

func (h *Host) shutdown(err error) {
	h.closeOnce.Do(func() {
		h.closeErr = err
		close(h.done)
		h.mu.Lock()
		sc := h.sig
		h.state = HostClosed
		h.mu.Unlock()
		sc.close()
	})
}

// State is the current signalling state.
func (h *Host) State() HostState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// RotateCode draws a new one-time code for the same ID and returns it; the
// code never leaves this machine, so the server is not involved. It is
// refused ("" returned, code unchanged) while an operator exchange, an
// approval or a session is in progress, and once the host is closed.
// OnCredentials reports the change (ReasonManual).
func (h *Host) RotateCode() string {
	res := make(chan string, 1)
	f := func() {
		if len(h.pending) > 0 {
			res <- ""
			return
		}
		h.newCode(ReasonManual)
		res <- h.Code()
	}
	select {
	case h.events <- f:
	case <-h.done:
		return ""
	}
	select {
	case c := <-res:
		return c
	case <-h.done:
		return ""
	}
}

func (h *Host) signal() *sigConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sig
}

func (h *Host) setState(st HostState) {
	h.mu.Lock()
	changed := h.state != st && h.state != HostClosed
	if changed {
		h.state = st
	}
	h.mu.Unlock()
	if changed && h.cfg.OnState != nil {
		h.cfg.OnState(st)
	}
}

// post runs f on the host goroutine.
func (h *Host) post(f func()) {
	select {
	case h.events <- f:
	case <-h.done:
	}
}

func (h *Host) run() {
	rotate := time.NewTicker(h.cfg.CodeLifetime)
	defer rotate.Stop()
	defer func() {
		for _, p := range h.pending {
			h.dropPending(p, false)
		}
	}()
	in := h.signal().in
	var back chan reconnected // non-nil while reconnecting
	for {
		select {
		case <-h.done:
			return
		case f := <-h.events:
			f()
		case <-rotate.C:
			if len(h.pending) == 0 {
				h.newCode(ReasonExpired)
			}
		case r := <-back:
			back = nil
			in = r.sc.in
			h.resumed(r)
		case m, ok := <-in:
			if !ok {
				if h.cfg.DisableReconnect {
					h.shutdown(fmt.Errorf("%w: %w: signalling connection lost", ErrHostClosed, ErrUnreachable))
					return
				}
				in = nil
				h.lost()
				back = make(chan reconnected, 1)
				go h.reconnect(back)
				continue
			}
			h.handle(m)
		}
	}
}

func (h *Host) send(m wire.Msg) {
	if err := h.signal().send(m); err != nil {
		h.log.Debug("signalling send failed", "t", m.T, "err", err)
	}
}

// reconnected is a new signalling link and the registration it got.
type reconnected struct {
	sc         *sigConn
	id, resume string
}

// lost handles a dropped signalling link: the server ended every exchange
// and session of the old link, so they end here too.
func (h *Host) lost() {
	h.log.Warn("signalling connection lost; reconnecting")
	inFlight := len(h.pending) > 0
	cut := fmt.Errorf("%w: signalling connection lost", ErrUnreachable)
	for _, p := range h.pending {
		switch {
		case p.delivered:
			p.sess.closeWith(false, cut)
			h.dropPending(p, false)
		case p.accepted:
			h.abort(p, cut, false)
		default:
			h.dropPending(p, false)
		}
	}
	if inFlight {
		h.newCode(ReasonFailed) // an exchange was cut: do not reuse its code
	}
	h.setState(HostConnecting)
}

// resumed installs the new link (host goroutine).
func (h *Host) resumed(r reconnected) {
	h.mu.Lock()
	old := h.sig
	h.sig = r.sc
	same := r.id == h.id
	h.id, h.resume = r.id, r.resume
	h.mu.Unlock()
	old.close()
	if same {
		h.log.Info("signalling back, same ID")
	} else {
		h.log.Info("signalling back with a new ID (grace window over)")
		h.newCode(ReasonNewID)
	}
	h.setState(HostOnline)
}

// Reconnection backoff (variables: tests shorten them).
var (
	reconnectMin = time.Second
	reconnectMax = 30 * time.Second
)

// reconnect dials and registers again until it works or the host closes,
// asking for the current ID back with its resume secret.
func (h *Host) reconnect(back chan<- reconnected) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-h.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	delay := reconnectMin
	for {
		select {
		case <-h.done:
			return
		case <-time.After(jitter(delay)):
		}
		if delay *= 2; delay > reconnectMax {
			delay = reconnectMax
		}
		h.mu.Lock()
		id, secret := h.id, h.resume
		h.mu.Unlock()
		r, err := h.registerOn(ctx, id, secret)
		if err != nil {
			h.log.Debug("reconnect failed", "err", err)
			continue
		}
		select {
		case back <- r:
		case <-h.done:
			r.sc.close()
		}
		return
	}
}

// registerOn opens a signalling link and registers, asking for id back.
func (h *Host) registerOn(ctx context.Context, id, secret string) (reconnected, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sc, err := dialSignal(ctx, h.cfg.Server, h.cfg.SignalOptions, wire.Msg{Role: wire.RoleHost, Ver: h.cfg.ClientVersion})
	if err != nil {
		return reconnected{}, err
	}
	req := wire.Msg{T: wire.TRegister}
	if secret != "" {
		req.ID, req.Resume = id, secret
	}
	if err := sc.send(req); err != nil {
		sc.close()
		return reconnected{}, err
	}
	for {
		select {
		case <-ctx.Done():
			sc.close()
			return reconnected{}, ctx.Err()
		case m, ok := <-sc.in:
			if !ok {
				return reconnected{}, fmt.Errorf("%w: connection closed during registration", ErrUnreachable)
			}
			switch m.T {
			case wire.TRegistered:
				nid := wire.Digits(m.ID)
				if !wire.ValidID(nid) {
					sc.close()
					return reconnected{}, errors.New("supportlink: server sent a malformed ID")
				}
				return reconnected{sc: sc, id: nid, resume: m.Resume}, nil
			case wire.TError:
				sc.close()
				return reconnected{}, serverError(m)
			}
		}
	}
}

// jitter spreads reconnections (+/- 20 %).
func jitter(d time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d)/5*2+1))
	if err != nil {
		return d
	}
	return d - d/5 + time.Duration(n.Int64())
}

func (h *Host) newCode(reason string) {
	h.mu.Lock()
	h.code = NewCode()
	c := CredentialsChange{ID: h.id, Code: h.code, Reason: reason}
	h.mu.Unlock()
	h.log.Info("code rotated", "reason", reason)
	if h.cfg.OnCredentials != nil {
		h.cfg.OnCredentials(c)
	}
}

// fail ends a pending exchange that never got confirmed: it costs the
// guesser one attempt on the server and burns the code locally.
func (h *Host) fail(p *hostPending, tell bool) {
	if tell {
		h.send(wire.Msg{T: wire.TPakeFailed, SID: p.sid})
	}
	h.dropPending(p, false)
	h.newCode(ReasonFailed)
}

func (h *Host) dropPending(p *hostPending, sendBye bool) {
	if _, ok := h.pending[p.sid]; !ok {
		return
	}
	delete(h.pending, p.sid)
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.cancel != nil {
		p.cancel()
	}
	if sendBye {
		h.send(wire.Msg{T: wire.TBye, SID: p.sid})
	}
}

func (h *Host) handle(m wire.Msg) {
	p := h.pending[m.SID]
	switch m.T {
	case wire.TRegistered: // after id_locked: a fresh ID
		id := wire.Digits(m.ID)
		if !wire.ValidID(id) {
			return
		}
		h.mu.Lock()
		h.id, h.resume = id, m.Resume
		h.mu.Unlock()
		h.newCode(ReasonLocked)

	case wire.TPake:
		if p != nil {
			return
		}
		msgA, err := base64.StdEncoding.DecodeString(m.Pake)
		p = &hostPending{sid: m.SID}
		h.pending[m.SID] = p
		if err != nil {
			h.fail(p, true)
			return
		}
		msgB, key, err := hostPakeExchange(h.Code(), h.ID(), m.SID, msgA)
		if err != nil {
			h.fail(p, true)
			return
		}
		p.keys = deriveKeys(key)
		h.send(wire.Msg{T: wire.TPakeReply, SID: m.SID,
			Pake: base64.StdEncoding.EncodeToString(msgB),
			Conf: macB64(p.keys.confirmHost, hostConfirmData(m.SID))})
		p.timer = time.AfterFunc(h.cfg.PakeTimeout, func() {
			h.post(func() {
				if q := h.pending[p.sid]; q == p && !p.confirmed {
					h.fail(p, true)
				}
			})
		})

	case wire.TConfOp:
		if p == nil || p.confirmed {
			return
		}
		if !checkMAC(p.keys.confirmOp, opConfirmData(m.SID), m.Conf) {
			h.fail(p, true)
			return
		}
		p.confirmed = true
		p.timer.Stop()
		h.send(wire.Msg{T: wire.TPakeOK, SID: m.SID})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		p.cancel = cancel
		req := ApprovalRequest{SID: m.SID, OperatorName: cleanName(m.Name), Account: cleanName(m.Account), SAS: p.keys.sas}
		p.operator = PeerInfo{Name: req.OperatorName, Account: req.Account}
		go func() {
			ok := h.cfg.Approve(ctx, req) && ctx.Err() == nil
			h.post(func() { h.decided(p, ok) })
		}()

	case wire.TICE:
		if p == nil || !p.accepted || p.peer != nil {
			return
		}
		p.report = cleanReportToken(m.Report)
		h.startPeer(p, m.ICE)

	case wire.TAnswer:
		if p == nil || p.peer == nil || p.remoteSet {
			return
		}
		sdp, err := openSDP(p.keys.boxOp, p.keys.fpOp, p.sid, wire.TAnswer, m.Box, m.MAC)
		if err == nil {
			err = p.peer.setAnswer(sdp)
		}
		if err != nil {
			h.abort(p, err, true)
			return
		}
		p.remoteSet = true
		for _, c := range p.queued {
			_ = p.peer.addCandidate(c)
		}
		p.queued = nil

	case wire.TCand:
		if p == nil || p.peer == nil {
			return
		}
		c, err := open(p.keys.boxOp, p.sid, wire.TCand, m.Box)
		if err != nil {
			h.abort(p, fmt.Errorf("%w: candidate %v", ErrMITM, err), true)
			return
		}
		if p.remoteSet {
			_ = p.peer.addCandidate(string(c))
		} else {
			p.queued = append(p.queued, string(c))
		}

	case wire.TBye:
		if p == nil {
			return
		}
		var err error = ErrPeerGone
		if m.Reason == "fingerprint_mismatch" {
			err = fmt.Errorf("%w: reported by the operator", ErrMITM)
		} else if m.Reason == "expired" {
			err = ErrExpired
		}
		h.peerEnded(p, err)

	case wire.TError:
		switch {
		case m.Err == wire.ErrIDLocked && m.SID == "":
			// Someone is guessing this ID: move to a new one, the attacker
			// loses the target.
			h.log.Warn("ID locked by the server after repeated wrong codes; registering a new ID")
			h.send(wire.Msg{T: wire.TRegister})
		case p != nil:
			h.peerEnded(p, serverError(m))
		}
	}
}

// peerEnded handles the end of a session announced by the server (the
// operator left, or the server expired or ended the session).
func (h *Host) peerEnded(p *hostPending, err error) {
	switch {
	case p.delivered:
		p.sess.closeWith(false, io.EOF)
		h.dropPending(p, false)
	case p.accepted:
		// Accepted but not up yet: report the failure to WaitForOperator.
		h.abort(p, err, false)
	case p.confirmed:
		// The operator proved the code but the session was never accepted
		// (left, or the person did not answer in time): the code is spent.
		h.dropPending(p, false)
		h.newCode(ReasonExpired)
	case p.keys.confirmHost != nil:
		// Left after seeing the host confirmation without confirming: a
		// wrong guess (the server charges it too).
		h.fail(p, false)
	default:
		h.dropPending(p, false)
	}
}

// decided applies the person's answer.
func (h *Host) decided(p *hostPending, ok bool) {
	if h.pending[p.sid] != p {
		return // the operator left meanwhile
	}
	p.cancel()
	p.cancel = nil
	if !ok {
		h.send(wire.Msg{T: wire.TReject, SID: p.sid})
		h.dropPending(p, false)
		h.newCode(ReasonRejected)
		return
	}
	p.accepted = true
	h.newCode(ReasonUsed) // one-time: the code dies once a session is accepted
	h.send(wire.Msg{T: wire.TAccept, SID: p.sid, Opts: []string{"view", "control"}})
}

func (h *Host) startPeer(p *hostPending, ice *wire.ICEInfo) {
	peer, err := newRTCPeer(ice, h.cfg.ICE, h.cfg.SignalOptions)
	if err != nil {
		h.abort(p, err, true)
		return
	}
	p.peer = peer
	sid, keys := p.sid, p.keys
	peer.onCandidate(func(c string) {
		box, err := seal(keys.boxHost, sid, wire.TCand, []byte(c))
		if err == nil {
			h.send(wire.Msg{T: wire.TCand, SID: sid, Box: box})
		}
	})
	s := newSession(sid, keys, p.operator, peer)
	s.report = p.report
	s.setOnClosed(func(bool, error) {
		h.post(func() {
			if h.pending[sid] == p && p.delivered {
				h.dropPending(p, true)
			}
		})
	})
	p.sess = s
	sdp, err := peer.createOffer()
	if err != nil {
		h.abort(p, err, true)
		return
	}
	box, mac, err := sealSDP(keys.boxHost, keys.fpHost, sid, wire.TOffer, sdp)
	if err != nil {
		h.abort(p, err, true)
		return
	}
	h.send(wire.Msg{T: wire.TOffer, SID: sid, Box: box, MAC: mac})
	go func() {
		err := s.waitOpen(h.cfg.ICE.ConnectTimeout)
		h.post(func() {
			if h.pending[sid] != p {
				return
			}
			if err != nil {
				h.abort(p, err, true)
				return
			}
			// The session may have ended between waitOpen and here.
			if s.Err() != nil {
				h.abort(p, s.Err(), true)
				return
			}
			h.send(wire.Msg{T: wire.TPath, SID: sid, Path: string(s.Path())})
			p.delivered = true
			select {
			case h.results <- hostResult{sess: s}:
			default:
				// Nobody waiting and a result already queued: should not
				// happen with one session at a time; do not leak the link.
				s.Close()
				h.dropPending(p, true)
			}
		})
	}()
}

// abort tears down an accepted session that failed to come up.
func (h *Host) abort(p *hostPending, err error, sendBye bool) {
	h.log.Warn("session setup failed", "sid", wire.TruncSID(p.sid), "err", err)
	if p.sess != nil {
		p.sess.closeWith(false, err)
	} else if p.peer != nil {
		_ = p.peer.close()
	}
	reason := "setup_failed"
	if errors.Is(err, ErrMITM) {
		reason = "fingerprint_mismatch"
	}
	if sendBye {
		h.send(wire.Msg{T: wire.TBye, SID: p.sid, Reason: reason})
	}
	h.dropPending(p, false)
	select {
	case h.results <- hostResult{err: err}:
	default:
	}
}

// cleanName bounds and sanitises a display string received from the network
// before it reaches a UI.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '‮' || r == '‭' || r == '⁦' || r == '⁧' || r == '⁨' || r == '⁩' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}
