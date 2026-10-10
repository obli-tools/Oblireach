package server

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

// accountStateTTL keeps the in-memory quota state of an idle account for a
// while, so queued counter writes land before it is read again from the
// database.
const accountStateTTL = 2 * time.Minute

// opAuth is the result of authenticating an operator's connect.
type opAuth struct {
	code      string // refusal (wire error code), "" when authenticated
	label     string // identity shown to the host
	logID     string // identity written to logs
	accountID int64  // 0 for a static key
	usage     accounts.Usage
	quota     AccountQuotas // instance quotas with the account's overrides
}

// quotasFor returns the quotas of an account: the instance ones, replaced
// by the account's overrides.
func (s *Server) quotasFor(a accounts.Account) AccountQuotas {
	q := s.cfg.Quotas
	if p := a.Quota.MaxSessions; p != nil {
		q.MaxConcurrent = int(*p)
	}
	if p := a.Quota.SessionsDay; p != nil {
		q.SessionsPerDay = int(*p)
	}
	if p := a.Quota.RelayBytes; p != nil {
		q.RelayBytesMonth = *p
	}
	return q
}

// authOperator resolves the account token or the static key of a connect.
// It may query the account database: call it without holding hub.mu.
func (s *Server) authOperator(key, token, ip string) opAuth {
	if token != "" && s.cfg.Accounts != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a, _, err := s.cfg.Accounts.ResolveTokenFrom(ctx, token, s.log.ip(ip))
		switch {
		case err == nil && (a.Verified || s.cfg.Mailer == nil):
			u, err := s.cfg.Accounts.Usage(ctx, a.ID)
			if err != nil {
				s.log.warn("account_db_error", "op", "usage", "err", err.Error())
				return opAuth{code: wire.ErrUnavailable}
			}
			return opAuth{label: accounts.Label(a), logID: "acct:" + strconv.FormatInt(a.ID, 10), accountID: a.ID, usage: u, quota: s.quotasFor(a)}
		case err == nil:
			// Created while verification was off; it must verify its
			// address now (the login answers email_not_verified).
		case !errors.Is(err, accounts.ErrNotFound):
			s.log.warn("account_db_error", "op", "resolve_token", "err", err.Error())
			return opAuth{code: wire.ErrUnavailable}
		}
	}
	if label, ok := s.operatorAccount(key); ok {
		return opAuth{label: label, logID: label}
	}
	if token != "" {
		return opAuth{code: wire.ErrAuthExpired}
	}
	return opAuth{code: wire.ErrOperatorRequired}
}

// acctLocked returns the quota state of an account, seeded from the
// database usage read by authOperator when it is not in memory.
func (h *hub) acctLocked(a opAuth, now time.Time) *acctState {
	st := h.accts[a.accountID]
	if st == nil {
		st = &acctState{
			day: now.UTC().Format("2006-01-02"), sessions: a.usage.SessionsToday,
			month: now.UTC().Format("2006-01"), relay: a.usage.RelayBytesMonth,
		}
		h.accts[a.accountID] = st
	}
	st.q = a.quota // overrides changed by an admin apply from the next connect
	h.rollLocked(st, now)
	st.touched = now
	return st
}

// rollLocked resets the counters of a past day or month (UTC).
func (h *hub) rollLocked(st *acctState, now time.Time) {
	if d := now.UTC().Format("2006-01-02"); st.day != d {
		st.day, st.sessions = d, 0
	}
	if m := now.UTC().Format("2006-01"); st.month != m {
		st.month, st.relay = m, 0
	}
}

// quotaLocked returns the quota a new session would exceed ("" if none) and
// when it resets (0 when it depends on other sessions ending).
func (h *hub) quotaLocked(accountID int64, st *acctState, now time.Time) (string, time.Duration) {
	q := st.q
	u := now.UTC()
	switch {
	case q.MaxConcurrent > 0 && st.live >= q.MaxConcurrent:
		return wire.QuotaConcurrent, 0
	case q.SessionsPerDay > 0 && st.sessions >= q.SessionsPerDay:
		next := time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, time.UTC)
		return wire.QuotaDaily, next.Sub(u)
	case q.RelayBytesMonth > 0 && st.relay+h.liveRelayLocked(accountID) >= q.RelayBytesMonth:
		next := time.Date(u.Year(), u.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return wire.QuotaRelayMonth, next.Sub(u)
	}
	return "", 0
}

// liveRelayLocked sums the bytes relayed so far by the sessions in
// progress of an account.
func (h *hub) liveRelayLocked(accountID int64) int64 {
	var n int64
	for _, s := range h.sessions {
		if s.accountID == accountID {
			n += s.relayed.Load()
		}
	}
	return n
}

// relayQuotaLocked ends the relayed sessions of accounts that went over
// their monthly relay volume during the session (checked every sweep).
func (h *hub) relayQuotaLocked() {
	if len(h.accts) == 0 {
		return
	}
	live := map[int64]int64{}
	for _, s := range h.sessions {
		if s.accountID != 0 {
			live[s.accountID] += s.relayed.Load()
		}
	}
	for id, n := range live {
		st := h.accts[id]
		if n == 0 || st == nil || st.q.RelayBytesMonth <= 0 || st.relay+n < st.q.RelayBytesMonth {
			continue
		}
		for _, s := range h.sessions {
			if s.accountID != id || s.relayed.Load() == 0 {
				continue // direct sessions cost nothing: they go on
			}
			h.s.m.inc(h.s.m.quota, wire.QuotaRelayMonth+"_ended")
			em := errMsg(wire.ErrQuota, s.sid, 0)
			em.Reason = wire.QuotaRelayMonth
			s.op.send(em)
			s.host.send(wire.Msg{T: wire.TBye, SID: s.sid, Reason: "quota"})
			h.endLocked(s, "quota", nil)
		}
	}
}

// relayCounter returns the relayed-bytes counter of a live session (nil if
// it is gone). Called by the TURN side, without holding its own lock.
func (h *hub) relayCounter(sid string) *atomic.Int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[sid]; s != nil {
		return &s.relayed
	}
	return nil
}
