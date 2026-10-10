package server

import (
	"sort"
	"strings"
	"time"

	"github.com/oblireach/supportlink/wire"
)

// secWindow is how long the security events of the admin dashboard are
// kept (in memory: a restart starts the window again).
const (
	secWindow = 24 * time.Hour
	secMax    = 100_000 // per kind: a flood cannot grow memory without bound
)

// secEvents keeps the times of recent failed code attempts, ID locks and IP
// bans. Guarded by hub.mu.
type secEvents struct {
	pakeFails, idLocks, bans []time.Time
}

func (e *secEvents) add(list []time.Time, now time.Time) []time.Time {
	if len(list) >= secMax {
		list = list[1:]
	}
	return append(list, now)
}

func (e *secEvents) gc(now time.Time) {
	cut := func(list []time.Time) []time.Time {
		i := sort.Search(len(list), func(i int) bool { return now.Sub(list[i]) < secWindow })
		if i == 0 {
			return list
		}
		return append(list[:0:0], list[i:]...)
	}
	e.pakeFails, e.idLocks, e.bans = cut(e.pakeFails), cut(e.idLocks), cut(e.bans)
}

// liveSession is a session in progress, as the admin pages show it. The
// person helped is only known by a truncated ID.
type liveSession struct {
	Handle    string // opaque handle of the terminate action (prefix of the sid)
	AccountID int64  // 0 for a static key
	KeyLabel  string // static key label
	HostTID   string
	State     string // pake, confirmed, accepted
	Path      string // direct, relay_udp, relay_tcp, relay_tls, "" before it is known
	Since     time.Time
	Relayed   int64
}

// Duration of the session so far (from acceptance once accepted).
func (l liveSession) Duration(now time.Time) time.Duration { return now.Sub(l.Since) }

// Relay reports a relayed path.
func (l liveSession) Relay() bool { return strings.HasPrefix(l.Path, "relay") }

// liveView is a snapshot of the hub for the admin and operator pages.
type liveView struct {
	At            time.Time
	HostsOnline   int
	Sessions      []liveSession
	Accepted      int // sessions accepted and in progress
	Direct, Relay int // accepted sessions by path (unknown path in neither)
	LockedIDs     int // IDs locked now
	BannedIPs     int // IPs banned now
	PakeFails24   int
	IDLocks24     int
	Bans24        int
	SecSince      time.Time      // events are counted since then (process start or 24 h ago)
	Operators     map[int64]bool // accounts with an operator signalling connection
}

// handleLen is the length of the terminate handle (a sid prefix).
const handleLen = 12

func (h *hub) live() liveView {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.sec.gc(now)
	v := liveView{At: now, HostsOnline: len(h.hosts), Operators: map[int64]bool{},
		PakeFails24: len(h.sec.pakeFails), IDLocks24: len(h.sec.idLocks), Bans24: len(h.sec.bans)}
	v.SecSince = now.Add(-secWindow)
	if h.s.started.After(v.SecSince) {
		v.SecSince = h.s.started
	}
	for _, d := range h.locked {
		if now.Before(d) {
			v.LockedIDs++
		}
	}
	for _, d := range h.banned {
		if now.Before(d) {
			v.BannedIPs++
		}
	}
	for c := range h.clients {
		if c.role == wire.RoleOperator && c.accountID != 0 {
			v.Operators[c.accountID] = true
		}
	}
	for _, s := range h.sessions {
		l := liveSession{Handle: s.sid[:handleLen], AccountID: s.accountID, HostTID: wire.TruncID(s.hostID),
			State: s.state(), Path: s.path, Since: s.created, Relayed: s.relayed.Load()}
		if s.accountID == 0 {
			l.KeyLabel = s.logAccount
		}
		if s.accepted {
			l.Since = s.acceptedAt
			v.Accepted++
			switch {
			case l.Path == "direct":
				v.Direct++
			case l.Relay():
				v.Relay++
			}
		}
		v.Sessions = append(v.Sessions, l)
	}
	sort.Slice(v.Sessions, func(i, j int) bool { return v.Sessions[i].Since.Before(v.Sessions[j].Since) })
	return v
}

// terminateLocked ends a session on the server's decision: both sides get
// a bye (the operator's and the host's clients close the session on it) and
// the TURN credentials die with it.
func (h *hub) terminateLocked(s *session, outcome string) {
	s.op.send(wire.Msg{T: wire.TBye, SID: s.sid, Reason: "cancelled"})
	s.host.send(wire.Msg{T: wire.TBye, SID: s.sid, Reason: "cancelled"})
	h.endLocked(s, outcome, nil)
}

// terminate ends the session of a handle; false when it is gone.
func (h *hub) terminate(handle string) bool {
	if len(handle) != handleLen {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sid, s := range h.sessions {
		if strings.HasPrefix(sid, handle) {
			h.terminateLocked(s, "admin_terminated")
			return true
		}
	}
	return false
}

// terminateAccount ends every session of an account (disabled, deleted).
func (h *hub) terminateAccount(accountID int64) int {
	if accountID == 0 {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.sessions {
		if s.accountID == accountID {
			h.terminateLocked(s, "admin_terminated")
			n++
		}
	}
	return n
}

// liveRelay returns the bytes relayed so far by the sessions in progress of
// an account.
func (h *hub) liveRelay(accountID int64) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.liveRelayLocked(accountID)
}
