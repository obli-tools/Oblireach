package server

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

// Report tickets (abuse reports). At acceptance the server
// hands the HOST a random token (256 bits) bound to the session; a report
// must carry the session id and that token, so the host can only report the
// operator of a session it accepted itself, and nobody else can report it
// (the operator never sees the token). The hub keeps the ticket in memory
// during the session and for ReportWindow after it; the session history
// keeps the token hash for the same window after a restart.

type ticketStatus int

const (
	ticketUnknown ticketStatus = iota // not in memory: look in the history
	ticketInvalid                     // wrong token, or the window is over
	ticketValid
)

// issueTicketLocked creates the report ticket of a session being accepted
// and returns its token ("" without the account database, where reports
// are stored).
func (h *hub) issueTicketLocked(s *session, now time.Time) string {
	if h.s.cfg.Accounts == nil {
		return ""
	}
	secret, hash := accounts.NewSecret("rpt1.")
	s.reportHash = hash
	tk := &reportTicket{hash: hash, accountID: s.accountID, operator: s.account, hostTID: wire.TruncID(s.hostID),
		started: s.created, accepted: now}
	if s.accountID == 0 {
		tk.keyLabel = s.logAccount
	}
	h.tickets[s.sid] = tk
	return secret
}

// checkTicket verifies a report token against the ticket of its session.
// live tells whether the session still runs.
func (h *hub) checkTicket(sid, token string, now time.Time) (reportTicket, bool, ticketStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tk := h.tickets[sid]
	if tk == nil {
		return reportTicket{}, false, ticketUnknown
	}
	if !hashEqual(accounts.HashSecret(token), tk.hash) {
		return reportTicket{}, false, ticketInvalid
	}
	if !tk.ended.IsZero() && now.Sub(tk.ended) > h.s.cfg.ReportWindow {
		return reportTicket{}, false, ticketInvalid
	}
	return *tk, tk.ended.IsZero(), ticketValid
}

// markReported records that the session of sid was reported (its end, if
// it already happened or happens later, goes to the report).
func (h *hub) markReported(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tk := h.tickets[sid]
	if tk == nil {
		return
	}
	tk.reported = true
	if !tk.ended.IsZero() {
		ended, path, store := tk.ended, tk.path, h.s.cfg.Accounts
		h.s.dbWriteLocked(func(ctx context.Context) {
			if err := store.SetReportSessionEnd(ctx, sid, ended, path); err != nil {
				h.s.log.warn("account_db_error", "op", "report_session_end", "err", err.Error())
			}
		})
	}
}

// terminateSID ends a session in progress on the server's decision; false
// when it is not running.
func (h *hub) terminateSID(sid, outcome string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sid]
	if s == nil {
		return false
	}
	h.terminateLocked(s, outcome)
	return true
}

// dropTickets forgets every report ticket (tests: a restarted server).
func (h *hub) dropTickets() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sid := range h.tickets {
		delete(h.tickets, sid)
	}
}

func hashEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
