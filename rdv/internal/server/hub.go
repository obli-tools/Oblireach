package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

const (
	// A connection authenticates at most this many connects (a legitimate
	// operator sends one per signalling connection).
	connectAuthPerSec = 0.5
	connectAuthBurst  = 3

	wsReadLimit    = 64 << 10 // SDP + candidates only: no content transits here
	wsWriteTimeout = 10 * time.Second
	outQueue       = 64
)

// session follows this state machine:
//
//	NEW --pake--> PAKE_SENT --pake_reply--> PAKE_REPLIED --pake_ok--> CONFIRMED --accept--> ACCEPTED
//
// No offer/answer/candidate and no TURN credential before ACCEPTED. A session
// that got a pake_reply but never a pake_ok counts as one wrong code when the
// operator ends it (bye, disconnect) or lets it expire: hanging up does not
// dodge the counter. When the HOST leaves in that window (closed, connection
// lost), nothing is charged: the operator's attempt was not refused, and a
// host that goes away must not burn the operator's attempts nor lock its ID.
type session struct {
	sid, hostID string
	host, op    *client
	account     string // identity shown to the host (key label or account label)
	logAccount  string // identity written to logs (key label, or "acct:<id>": no email in logs)
	accountID   int64  // operator account, 0 for a static key
	opIP        string
	created     time.Time
	// relayed counts the bytes this session relayed through TURN, across
	// all its allocations (quota and logs).
	relayed atomic.Int64

	pakeSent, pakeReplied, confSent, pakeOK, accepted bool
	failed                                            bool // host reported pake_failed
	pakeOKAt, acceptedAt                              time.Time
	path                                              string
	// reportHash is the hash of the report token handed to the host at
	// acceptance ("" without one).
	reportHash string
}

// reportTicket lets the host of an accepted session report its operator,
// during the session and for ReportWindow after its end (hub memory; the
// session history keeps the hash for after a restart). Guarded by hub.mu.
type reportTicket struct {
	hash      string
	accountID int64
	keyLabel  string // static key label (accountID 0)
	operator  string // identity shown to the host
	hostTID   string
	started   time.Time
	accepted  time.Time
	ended     time.Time // zero while the session runs
	path      string
	reported  bool
}

func (s *session) state() string {
	switch {
	case s.accepted:
		return "accepted"
	case s.pakeOK:
		return "confirmed"
	default:
		return "pake"
	}
}

type client struct {
	h    *hub
	conn *websocket.Conn
	ip   string
	out  chan outMsg
	done chan struct{}
	once sync.Once

	// guarded by hub.mu
	hello  bool
	role   string
	hostID string
	// resumeHash is SHA-256 of the resume secret of hostID.
	resumeHash [32]byte
	account    string
	accountID  int64 // operator account of the last authenticated connect
	cur        *session
	msgs       bucket

	// authB bounds the connect authentications (database lookups) of
	// this connection; only its read goroutine touches it.
	authB bucket
}

type outMsg struct {
	m    wire.Msg
	last bool // close the connection once written
}

// send queues a message without blocking (callers may hold hub.mu). A peer
// that does not drain its queue is disconnected.
func (c *client) send(m wire.Msg) { c.enqueue(outMsg{m: m}) }

// sendAndClose writes a final message (an error) then closes.
func (c *client) sendAndClose(m wire.Msg) { c.enqueue(outMsg{m: m, last: true}) }

func (c *client) enqueue(o outMsg) {
	select {
	case c.out <- o:
	case <-c.done:
	default:
		go c.close()
	}
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *client) writeLoop(ping time.Duration) {
	t := time.NewTicker(ping)
	defer t.Stop()
	defer c.close()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
				return
			}
		case o := <-c.out:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteJSON(o.m); err != nil || o.last {
				if o.last {
					_ = c.conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				}
				return
			}
		}
	}
}

type hub struct {
	s *Server

	mu       sync.Mutex
	clients  map[*client]struct{}
	hosts    map[string]*client
	sessions map[string]*session
	locked   deadlines // ID -> locked until
	banned   deadlines // IP -> banned until
	idFails  *failLog
	ipFails  *failLog
	connIP   *bucketSet
	connID   *bucketSet
	regIP    *bucketSet
	// resumable keeps the IDs of hosts whose signalling dropped, for
	// ResumeGrace: only the holder of the resume secret gets them back.
	resumable map[string]resumeEntry
	// accts holds the quota state of the operator accounts seen recently;
	// this process is the only writer of the counters, so memory is
	// authoritative and the database is its persistent copy.
	accts map[int64]*acctState
	// sec keeps the times of recent security events (admin dashboard).
	sec secEvents
	// tickets are the report tickets of the accepted sessions, by sid.
	tickets map[string]*reportTicket
}

type resumeEntry struct {
	hash  [32]byte
	until time.Time
}

type acctState struct {
	q        AccountQuotas // effective quotas, refreshed at each connect
	day      string
	sessions int // created today (UTC)
	month    string
	relay    int64 // relayed this month by ended sessions (UTC)
	live     int   // sessions in progress
	touched  time.Time
}

func newHub(s *Server) *hub {
	l := s.cfg.Limits
	return &hub{
		s:       s,
		clients: map[*client]struct{}{}, hosts: map[string]*client{}, sessions: map[string]*session{},
		locked: deadlines{}, banned: deadlines{},
		idFails: newFailLog(l.FailWindow), ipFails: newFailLog(l.IPFailWindow),
		connIP: newBucketSet(l.ConnectPerMinIP, l.ConnectBurstIP),
		connID: newBucketSet(l.ConnectPerMinID, l.ConnectBurstID),
		regIP:  newBucketSet(l.RegisterPerMinIP, l.RegisterBurstIP),
		accts:  map[int64]*acctState{},

		resumable: map[string]resumeEntry{},
		tickets:   map[string]*reportTicket{},
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize: 4096, WriteBufferSize: 4096,
	// Any origin: a page that opens this socket only gets a client of its
	// own; nothing is bound to cookies or ambient credentials.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (h *hub) clientIP(r *http.Request) string {
	if h.s.cfg.TrustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *hub) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{h: h, conn: conn, ip: h.clientIP(r), out: make(chan outMsg, outQueue), done: make(chan struct{})}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	go c.writeLoop(h.s.cfg.PingInterval)

	idle := h.s.cfg.IdleTimeout
	conn.SetReadLimit(wsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(idle))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(idle)) })
	for {
		var m wire.Msg
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(idle))
		h.handle(c, m)
	}
	h.disconnect(c)
	c.close()
}

func (h *hub) closeAll() {
	h.mu.Lock()
	cs := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		cs = append(cs, c)
	}
	h.mu.Unlock()
	for _, c := range cs {
		c.close()
	}
}

func (h *hub) disconnect(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	if c.hostID != "" && h.hosts[c.hostID] == c {
		delete(h.hosts, c.hostID)
		if g := h.s.cfg.ResumeGrace; g > 0 {
			h.resumable[c.hostID] = resumeEntry{hash: c.resumeHash, until: time.Now().Add(g)}
		}
	}
	if s := c.cur; s != nil {
		if other := s.other(c); other != nil {
			other.send(wire.Msg{T: wire.TBye, SID: s.sid, Reason: "peer_disconnected"})
		}
		h.endLocked(s, "disconnected", c)
	}
}

func (s *session) other(c *client) *client {
	if c == s.host {
		return s.op
	}
	return s.host
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func errMsg(code string, sid string, retry time.Duration) wire.Msg {
	r := 0
	if retry > 0 {
		r = int(retry.Round(time.Second) / time.Second)
		if r == 0 {
			r = 1
		}
	}
	return wire.Msg{T: wire.TError, Err: code, SID: sid, Retry: r}
}

func (h *hub) handle(c *client, m wire.Msg) {
	// A connect is authenticated before taking the hub lock: token lookups
	// hit the account database. c.hello and c.role are only written by this
	// client's read goroutine (this one), so reading them here is safe.
	var auth opAuth
	if c.hello && m.T == wire.TConnect && c.role == wire.RoleOperator {
		if c.authB.take(time.Now(), connectAuthPerSec, connectAuthBurst) {
			auth = h.s.authOperator(m.Key, m.Token, c.ip)
		} else {
			auth = opAuth{code: wire.ErrRateLimited}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	l := h.s.cfg.Limits
	if !c.msgs.take(now, l.MsgPerSec, l.MsgBurst) {
		h.s.m.inc(h.s.m.rateLimited, "messages")
		c.sendAndClose(errMsg(wire.ErrRateLimited, "", 0))
		return
	}
	if !c.hello {
		h.hello(c, m)
		return
	}
	switch m.T {
	case wire.TRegister:
		h.register(c, m, now)
	case wire.TConnect:
		h.connect(c, m, auth, now)
	case wire.THello:
		c.send(errMsg(wire.ErrBadRequest, "", 0))
	default:
		h.route(c, m, now)
	}
}

func (h *hub) hello(c *client, m wire.Msg) {
	if m.T != wire.THello || (m.Role != wire.RoleHost && m.Role != wire.RoleOperator) {
		c.sendAndClose(errMsg(wire.ErrBadRequest, "", 0))
		return
	}
	if m.Proto != wire.ProtocolVersion || versionLess(m.Ver, h.s.cfg.MinClientVersion) {
		// Proto tells the client which side is too old; Reason is the
		// minimum client version (may be empty).
		c.sendAndClose(wire.Msg{T: wire.TError, Err: wire.ErrUpgradeRequired, Proto: wire.ProtocolVersion, Reason: h.s.cfg.MinClientVersion})
		return
	}
	c.hello, c.role = true, m.Role
}

func (h *hub) register(c *client, m wire.Msg, now time.Time) {
	if c.role != wire.RoleHost {
		c.send(errMsg(wire.ErrBadRequest, "", 0))
		return
	}
	if d, ok := h.banned.active(c.ip, now); ok {
		c.send(errMsg(wire.ErrBanned, "", d))
		return
	}
	if !h.regIP.take(c.ip, now) {
		h.s.m.inc(h.s.m.rateLimited, "register_ip")
		c.send(errMsg(wire.ErrRateLimited, "", 30*time.Second))
		return
	}
	if c.hostID != "" && h.hosts[c.hostID] == c {
		delete(h.hosts, c.hostID) // re-register (after id_locked): the old ID is released
	}
	id := h.resumeLocked(c, m, now)
	resumed := id != ""
	for id == "" {
		id = wire.RandomDigits(wire.IDLen, true)
		if _, taken := h.hosts[id]; taken {
			id = ""
		} else if _, locked := h.locked.active(id, now); locked {
			id = ""
		} else if r, kept := h.resumable[id]; kept && now.Before(r.until) {
			id = "" // reserved for its host
		}
	}
	h.hosts[id] = c
	c.hostID = id
	secret := randHex(16)
	c.resumeHash = sha256.Sum256([]byte(secret))
	if resumed {
		h.s.m.resumed.Add(1)
	} else {
		h.s.m.registered.Add(1)
	}
	h.s.log.event("register", "id", wire.TruncID(id), "ip", h.s.log.ip(c.ip), "resumed", resumed)
	reply := wire.Msg{T: wire.TRegistered, ID: id}
	if g := h.s.cfg.ResumeGrace; g > 0 {
		reply.Resume, reply.TTL = secret, int(g.Seconds())
	}
	c.send(reply)
}

// resumeLocked returns the ID the host asks back when it proves its resume
// secret, "" otherwise (then it gets a new ID; the answer is the same as a
// first registration either way). An ID locked by wrong codes is never
// resumed. A holder still registered (its old connection not yet seen as
// dead) is replaced and disconnected.
func (h *hub) resumeLocked(c *client, m wire.Msg, now time.Time) string {
	want := wire.Digits(m.ID)
	if h.s.cfg.ResumeGrace <= 0 || m.Resume == "" || len(m.Resume) > 64 || !wire.ValidID(want) {
		return ""
	}
	if _, locked := h.locked.active(want, now); locked {
		return ""
	}
	sum := sha256.Sum256([]byte(m.Resume))
	if old := h.hosts[want]; old != nil {
		if old == c || subtle.ConstantTimeCompare(sum[:], old.resumeHash[:]) != 1 {
			return ""
		}
		delete(h.hosts, want)
		old.hostID = ""
		go old.close() // its session, if any, ends with its disconnection
		return want
	}
	r, ok := h.resumable[want]
	if !ok || !now.Before(r.until) || subtle.ConstantTimeCompare(sum[:], r.hash[:]) != 1 {
		return ""
	}
	delete(h.resumable, want)
	return want
}

func (h *hub) connect(c *client, m wire.Msg, auth opAuth, now time.Time) {
	result := func(r string) {
		h.s.m.inc(h.s.m.connect, r)
		h.s.log.event("connect", "result", r, "id", wire.TruncID(wire.Digits(m.ID)), "ip", h.s.log.ip(c.ip), "account", c.account)
	}
	if c.role != wire.RoleOperator || c.cur != nil {
		c.send(errMsg(wire.ErrBadRequest, "", 0))
		return
	}
	if d, ok := h.banned.active(c.ip, now); ok {
		result(wire.ErrBanned)
		c.send(errMsg(wire.ErrBanned, "", d))
		return
	}
	if auth.code != "" {
		result(auth.code)
		var retry time.Duration
		switch auth.code {
		case wire.ErrOperatorRequired:
			// A wrong static key is a guess; an expired login is not
			// (tokens carry 256 random bits).
			h.ipFailLocked(c.ip, now)
		case wire.ErrRateLimited:
			h.s.m.inc(h.s.m.rateLimited, "connect_auth")
			retry = 2 * time.Second
		case wire.ErrUnavailable:
			retry = 10 * time.Second
		}
		c.send(errMsg(auth.code, "", retry))
		return
	}
	c.account, c.accountID = auth.logID, auth.accountID
	id := wire.Digits(m.ID)
	if d, ok := h.locked.active(id, now); ok {
		result(wire.ErrIDLocked)
		c.send(errMsg(wire.ErrIDLocked, "", d))
		return
	}
	if !h.connIP.take(c.ip, now) {
		h.s.m.inc(h.s.m.rateLimited, "connect_ip")
		result(wire.ErrRateLimited)
		c.send(errMsg(wire.ErrRateLimited, "", 30*time.Second))
		return
	}
	if !h.connID.take(id, now) {
		h.s.m.inc(h.s.m.rateLimited, "connect_id")
		result(wire.ErrRateLimited)
		c.send(errMsg(wire.ErrRateLimited, "", 30*time.Second))
		return
	}
	var st *acctState
	if auth.accountID != 0 {
		st = h.acctLocked(auth, now)
		if quota, retry := h.quotaLocked(auth.accountID, st, now); quota != "" {
			result(wire.ErrQuota)
			h.s.m.inc(h.s.m.quota, quota)
			em := errMsg(wire.ErrQuota, "", retry)
			em.Reason = quota
			c.send(em)
			return
		}
	}
	host := h.hosts[id]
	if host == nil || !wire.ValidID(id) {
		result(wire.ErrUnknownID) // same answer for offline and unknown
		c.send(errMsg(wire.ErrUnknownID, "", 0))
		return
	}
	if host.cur != nil {
		result(wire.ErrBusy)
		c.send(errMsg(wire.ErrBusy, "", 0))
		return
	}
	s := &session{sid: randHex(16), hostID: id, host: host, op: c, account: auth.label, logAccount: auth.logID,
		accountID: auth.accountID, opIP: c.ip, created: now}
	h.sessions[s.sid] = s
	host.cur, c.cur = s, s
	if st != nil {
		st.live++
		st.sessions++
		accountID, store := auth.accountID, h.s.cfg.Accounts
		h.s.dbWriteLocked(func(ctx context.Context) {
			if err := store.AddSession(ctx, accountID); err != nil {
				h.s.log.warn("account_db_error", "op", "add_session", "err", err.Error())
			}
		})
	}
	result("ok")
	c.send(wire.Msg{T: wire.TSession, SID: s.sid, ID: id})
}

// route forwards a session message after checking who may send it and in
// which state.
func (h *hub) route(c *client, m wire.Msg, now time.Time) {
	s := h.sessions[m.SID]
	if s == nil || (s.host != c && s.op != c) {
		c.send(errMsg(wire.ErrNoSession, m.SID, 0))
		return
	}
	fromHost := c == s.host
	dst := s.other(c)
	fwd := wire.Msg{T: m.T, SID: s.sid}
	switch m.T {
	case wire.TPake:
		if fromHost || s.pakeSent {
			return
		}
		s.pakeSent = true
		fwd.Pake = m.Pake

	case wire.TPakeReply:
		if !fromHost || !s.pakeSent || s.pakeReplied {
			return
		}
		s.pakeReplied = true
		fwd.Pake, fwd.Conf = m.Pake, m.Conf

	case wire.TConfOp:
		if fromHost || !s.pakeReplied || s.confSent {
			return
		}
		s.confSent = true
		// The account is what the server authenticated; the name is
		// self-declared and only passed along.
		fwd.Conf, fwd.Name, fwd.Account = m.Conf, m.Name, s.account

	case wire.TPakeOK:
		if !fromHost || !s.confSent || s.pakeOK {
			return
		}
		s.pakeOK, s.pakeOKAt = true, now
		return

	case wire.TPakeFailed:
		if !fromHost || s.pakeOK {
			return
		}
		s.failed = true
		s.op.send(errMsg(wire.ErrBadCode, s.sid, 0))
		h.endLocked(s, "pake_failed", nil)
		return

	case wire.TAccept:
		if !fromHost || !s.pakeOK || s.accepted {
			return
		}
		s.accepted, s.acceptedAt = true, now
		h.s.m.accepted.Add(1)
		ice := h.s.iceInfo(s.sid, now)
		h.s.log.event("accepted", "sid", wire.TruncSID(s.sid), "id", wire.TruncID(s.hostID), "account", s.logAccount)
		// The report token goes to the host only (never in the forwarded
		// accept): it binds a report to this session.
		c.send(wire.Msg{T: wire.TICE, SID: s.sid, ICE: ice, Report: h.issueTicketLocked(s, now)})
		fwd.ICE, fwd.Opts = ice, m.Opts

	case wire.TReject:
		if !fromHost || !s.pakeOK || s.accepted {
			return
		}
		h.s.m.rejected.Add(1)
		dst.send(fwd)
		h.endLocked(s, "rejected", nil)
		return

	case wire.TOffer, wire.TAnswer:
		if !s.accepted || fromHost != (m.T == wire.TOffer) {
			return // no WebRTC before explicit acceptance
		}
		fwd.Box, fwd.MAC = m.Box, m.MAC

	case wire.TCand:
		if !s.accepted {
			return
		}
		fwd.Box = m.Box

	case wire.TPath:
		if s.accepted {
			s.path = worsePath(s.path, m.Path)
		}
		return

	case wire.TBye:
		fwd.Reason = shortReason(m.Reason)
		dst.send(fwd)
		h.endLocked(s, "bye", c)
		return

	default:
		c.send(errMsg(wire.ErrBadRequest, s.sid, 0))
		return
	}
	if h.s.cfg.tamper != nil {
		h.s.cfg.tamper(&fwd)
	}
	dst.send(fwd)
}

// endLocked removes a session and charges a failed code attempt when due.
// leaver is the client whose bye or disconnection ends the session (nil for
// the server's own decisions: expiry, pake_failed, rejection).
func (h *hub) endLocked(s *session, outcome string, leaver *client) {
	if h.sessions[s.sid] != s {
		return
	}
	now := time.Now()
	delete(h.sessions, s.sid)
	if s.host.cur == s {
		s.host.cur = nil
	}
	if s.op.cur == s {
		s.op.cur = nil
	}
	hostLeft := leaver != nil && leaver == s.host
	badCode := s.failed || (s.pakeReplied && !s.pakeOK && !hostLeft)
	if badCode {
		h.sec.pakeFails = h.sec.add(h.sec.pakeFails, now)
		h.s.m.pakeFailed.Add(1)
		h.s.log.event("pake_failed", "sid", wire.TruncSID(s.sid), "id", wire.TruncID(s.hostID), "ip", h.s.log.ip(s.opIP), "account", s.logAccount)
		h.ipFailLocked(s.opIP, now)
		if n := h.idFails.add(s.hostID, now); n >= h.s.cfg.Limits.FailsPerID {
			lock := h.s.cfg.Limits.LockDuration
			h.locked[s.hostID] = now.Add(lock)
			h.idFails.reset(s.hostID)
			h.s.m.idLocked.Add(1)
			h.sec.idLocks = h.sec.add(h.sec.idLocks, now)
			h.s.log.warn("id_locked", "id", wire.TruncID(s.hostID), "seconds", int(lock.Seconds()))
			// Tell the host: it moves to a new ID and the guesser loses
			// its target.
			if host := h.hosts[s.hostID]; host != nil {
				host.send(errMsg(wire.ErrIDLocked, "", lock))
			}
		}
	}
	var relayed int64
	if h.s.turn != nil {
		relayed = h.s.turn.revoke(s.sid)
	}
	if r := s.relayed.Load(); r > relayed {
		relayed = r // all allocations of the session, reclaimed ones included
	}
	if s.accountID != 0 {
		if st := h.accts[s.accountID]; st != nil {
			st.live--
			h.rollLocked(st, now)
			st.relay += relayed
			st.touched = now
		}
		if relayed > 0 {
			accountID, store := s.accountID, h.s.cfg.Accounts
			h.s.dbWriteLocked(func(ctx context.Context) {
				if err := store.AddRelay(ctx, accountID, relayed); err != nil {
					h.s.log.warn("account_db_error", "op", "add_relay", "err", err.Error())
				}
			})
		}
	}
	path := s.path
	if path == "" {
		path = "unknown"
	}
	if s.accepted {
		h.s.m.inc(h.s.m.paths, path)
	}
	h.s.m.inc(h.s.m.ends, outcome)
	if tk := h.tickets[s.sid]; tk != nil {
		tk.ended, tk.path = now, path
		if tk.reported {
			// Reported while it ran: the report gets the end of the session.
			sid, store := s.sid, h.s.cfg.Accounts
			h.s.dbWriteLocked(func(ctx context.Context) {
				if err := store.SetReportSessionEnd(ctx, sid, now, path); err != nil {
					h.s.log.warn("account_db_error", "op", "report_session_end", "err", err.Error())
				}
			})
		}
	}
	since := s.created
	if s.accepted {
		since = s.acceptedAt
	}
	h.s.log.event("session_end", "sid", wire.TruncSID(s.sid), "id", wire.TruncID(s.hostID), "outcome", outcome,
		"accepted", s.accepted, "duration_ms", now.Sub(since).Milliseconds(), "path", path, "bytes_relayed", relayed)
	if store := h.s.cfg.Accounts; store != nil {
		rec := accounts.SessionRecord{
			AccountID: s.accountID, HostTID: wire.TruncID(s.hostID), Started: s.created, Ended: now,
			Path: path, Relayed: relayed, EndReason: outcome, SID: s.sid, ReportHash: s.reportHash,
		}
		if s.accountID == 0 {
			rec.KeyLabel = s.logAccount
		}
		if s.accepted {
			rec.Accepted = s.acceptedAt
		}
		if badCode {
			rec.EndReason = "bad_code"
		}
		h.s.dbWriteLocked(func(ctx context.Context) {
			if err := store.AddSessionRecord(ctx, rec); err != nil {
				h.s.log.warn("account_db_error", "op", "session_history", "err", err.Error())
			}
		})
	}
}

func (h *hub) ipFailLocked(ip string, now time.Time) {
	l := h.s.cfg.Limits
	if n := h.ipFails.add(ip, now); n >= l.FailsPerIP {
		h.banned[ip] = now.Add(l.IPBanDuration)
		h.ipFails.reset(ip)
		h.s.m.ipBanned.Add(1)
		h.sec.bans = h.sec.add(h.sec.bans, now)
		h.s.log.warn("ip_banned", "ip", h.s.log.ip(ip), "seconds", int(l.IPBanDuration.Seconds()))
	}
}

// sessionAccepted is the TURN-side check: credentials only work while their
// session is alive and accepted.
func (h *hub) sessionAccepted(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sid]
	return s != nil && s.accepted
}

func (h *hub) sweepLoop(stop <-chan struct{}) {
	t := time.NewTicker(h.s.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			h.sweep(now)
		}
	}
}

func (h *hub) sweep(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		expired := (!s.pakeOK && now.Sub(s.created) > h.s.cfg.PakeTimeout) ||
			(s.pakeOK && !s.accepted && now.Sub(s.pakeOKAt) > h.s.cfg.DecisionTimeout)
		if expired {
			s.op.send(errMsg(wire.ErrExpired, s.sid, 0))
			s.host.send(wire.Msg{T: wire.TBye, SID: s.sid, Reason: "expired"})
			h.endLocked(s, "expired", nil)
		}
	}
	h.relayQuotaLocked()
	h.sec.gc(now)
	for id, st := range h.accts {
		if st.live <= 0 && now.Sub(st.touched) > accountStateTTL {
			delete(h.accts, id)
		}
	}
	h.connIP.gc(now)
	h.connID.gc(now)
	h.regIP.gc(now)
	h.idFails.gc(now)
	h.ipFails.gc(now)
	h.locked.gc(now)
	h.banned.gc(now)
	for id, r := range h.resumable {
		if !now.Before(r.until) {
			delete(h.resumable, id)
		}
	}
	for sid, tk := range h.tickets {
		if !tk.ended.IsZero() && now.Sub(tk.ended) > h.s.cfg.ReportWindow {
			delete(h.tickets, sid)
		}
	}
}

func (h *hub) gauges() gauges {
	h.mu.Lock()
	defer h.mu.Unlock()
	g := gauges{hostsOnline: len(h.hosts), sessions: map[string]int{}}
	for _, s := range h.sessions {
		g.sessions[s.state()]++
	}
	return g
}

// iceInfo mints the TURN REST credentials of an accepted session. The
// username carries the expiry and the session id; the TURN auth handler
// also checks that the session is still alive, so credentials die with it.
func (s *Server) iceInfo(sid string, now time.Time) *wire.ICEInfo {
	exp := now.Add(s.cfg.RelayMaxDuration)
	user := strconv.FormatInt(exp.Unix(), 10) + ":" + sid
	hp := net.JoinHostPort(s.cfg.ICEHost, strconv.Itoa(s.TURNPort()))
	urls := []string{
		"stun:" + hp,
		"turn:" + hp + "?transport=udp",
		"turn:" + hp + "?transport=tcp",
	}
	if s.turnsL != nil {
		urls = append(urls, "turns:"+net.JoinHostPort(s.cfg.TURNTLSHost, strconv.Itoa(s.cfg.TURNTLSPort))+"?transport=tcp")
	}
	return &wire.ICEInfo{
		URLs:       urls,
		Username:   user,
		Credential: turnPassword(s.cfg.TURNSecret, user),
		TTL:        int(s.cfg.RelayMaxDuration.Seconds()),
	}
}

// worsePath keeps the most expensive path reported by either side.
func worsePath(cur, rep string) string {
	rank := map[string]int{"": 0, "unknown": 0, "direct": 1, "relay_udp": 2, "relay_tcp": 3, "relay_tls": 4}
	r, ok := rank[rep]
	if !ok {
		return cur
	}
	if r > rank[cur] {
		return rep
	}
	return cur
}

// shortReason passes only known bye reasons along (no free text).
func shortReason(r string) string {
	switch r {
	case "fingerprint_mismatch", "bad_code", "cancelled", "setup_failed", "expired", "peer_disconnected", "quota":
		return r
	}
	return ""
}

// versionLess compares dotted numeric versions ("1.2.10" < "1.10.0"). An
// empty client version is older than any minimum.
func versionLess(v, min string) bool {
	if min == "" {
		return false
	}
	if v == "" {
		return true
	}
	a, b := strings.Split(v, "."), strings.Split(min, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x, _ = strconv.Atoi(strings.TrimLeft(a[i], "v"))
		}
		if i < len(b) {
			y, _ = strconv.Atoi(strings.TrimLeft(b[i], "v"))
		}
		if x != y {
			return x < y
		}
	}
	return false
}
