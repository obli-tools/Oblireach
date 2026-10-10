package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

// POST /v1/report: an abuse report from the person helped.
// Bound to a real session: the session id plus the token the host received
// at acceptance (report.go). One report per session, a per-IP rate limit,
// nothing about the session content (the server has none).

// sidLen is the length of a session id (16 random bytes, hex).
const sidLen = 32

func validSID(s string) bool {
	if len(s) != sidLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (w *web) apiReport(rw http.ResponseWriter, r *http.Request) {
	st := w.s.cfg.Accounts
	if st == nil {
		apiError(rw, http.StatusNotFound, wire.ErrReportsDisabled)
		return
	}
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", http.MethodPost)
		apiError(rw, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	now := time.Now()
	ip := w.ip(r)
	if ok, wait := w.reports.take(ip, now); !ok {
		w.s.m.inc(w.s.m.reports, "rate_limited")
		w.s.m.inc(w.s.m.rateLimited, "report_ip")
		rateLimited(rw, wait)
		return
	}
	var req wire.ReportRequest
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxJSONBytes))
	if err := dec.Decode(&req); err != nil || !validSID(req.SID) || req.Token == "" || len(req.Token) > 128 ||
		!wire.ValidReportReason(req.Reason) || len(req.Text) > 4*wire.ReportMaxText+64 {
		w.s.m.inc(w.s.m.reports, "bad_request")
		apiError(rw, http.StatusBadRequest, wire.ErrBadRequest)
		return
	}
	text := wire.CleanReportText(req.Text)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rec := accounts.ReportRecord{SID: req.SID, Reason: req.Reason, Text: text,
		ReporterIP: w.s.log.ip(ip), ReporterKey: w.reporterKey(ip)}
	tk, live, status := w.s.hub.checkTicket(req.SID, req.Token, now)
	switch status {
	case ticketValid:
		rec.AccountID, rec.KeyLabel, rec.Operator, rec.HostTID = tk.accountID, tk.keyLabel, tk.operator, tk.hostTID
		rec.Started, rec.Accepted, rec.Ended, rec.Path = tk.started, tk.accepted, tk.ended, tk.path
	case ticketUnknown:
		// Not in memory (a restart since the session): the history keeps
		// the token hash of the sessions that ended.
		h, err := st.SessionBySID(ctx, req.SID)
		switch {
		case errors.Is(err, accounts.ErrNotFound):
		case err != nil:
			w.s.log.warn("account_db_error", "op", "session_by_sid", "err", err.Error())
			apiError(rw, http.StatusServiceUnavailable, wire.ErrUnavailable)
			return
		case h.ReportHash != "" && !h.Accepted.IsZero() && hashEqual(accounts.HashSecret(req.Token), h.ReportHash) &&
			now.Sub(h.Ended) <= w.s.cfg.ReportWindow:
			status = ticketValid
			rec.AccountID, rec.HostTID = h.AccountID, h.HostTID
			rec.Started, rec.Accepted, rec.Ended, rec.Path = h.Started, h.Accepted, h.Ended, h.Path
			if h.AccountID == 0 {
				rec.KeyLabel, rec.Operator = h.KeyLabel, h.KeyLabel
			} else if a, err := st.ByID(ctx, h.AccountID); err == nil {
				rec.Operator = accounts.Label(a)
			}
		}
	}
	if status != ticketValid {
		// One answer for an unknown session, a wrong token and a window
		// that is over: nothing to probe.
		w.s.m.inc(w.s.m.reports, "invalid")
		w.s.log.event("report", "result", "invalid", "sid", wire.TruncSID(req.SID), "ip", w.s.log.ip(ip))
		apiError(rw, http.StatusForbidden, wire.ErrInvalidReport)
		return
	}
	id, err := st.AddReport(ctx, rec)
	switch {
	case errors.Is(err, accounts.ErrExists):
		w.s.m.inc(w.s.m.reports, "duplicate")
		apiError(rw, http.StatusConflict, wire.ErrAlreadyReported)
		return
	case errors.Is(err, accounts.ErrNotFound):
		// The operator account was deleted meanwhile: keep the report
		// without it (the label stays).
		rec.AccountID = 0
		if rec.KeyLabel == "" {
			rec.KeyLabel = "deleted"
		}
		id, err = st.AddReport(ctx, rec)
	}
	if err != nil {
		if errors.Is(err, accounts.ErrExists) {
			apiError(rw, http.StatusConflict, wire.ErrAlreadyReported)
			return
		}
		w.s.log.warn("account_db_error", "op", "add_report", "err", err.Error())
		apiError(rw, http.StatusServiceUnavailable, wire.ErrUnavailable)
		return
	}
	w.s.hub.markReported(req.SID)
	ended := false
	if req.End && live {
		ended = w.s.hub.terminateSID(req.SID, "reported")
	}
	w.s.m.inc(w.s.m.reports, "ok_"+req.Reason)
	attrs := []any{"result", "ok", "report", id, "sid", wire.TruncSID(req.SID), "id", rec.HostTID, "reason", req.Reason,
		"ip", w.s.log.ip(ip), "session_ended", ended}
	if rec.AccountID != 0 {
		attrs = append(attrs, "account", acctKey(rec.AccountID))
	} else {
		attrs = append(attrs, "account", rec.KeyLabel)
	}
	w.s.log.event("report", attrs...)
	w.notifyAdmins("mail.report.subject", "mail.report.body", w.s.cfg.Name, strconv.FormatInt(id, 10), req.Reason,
		reportWho(rec), w.s.cfg.PublicURL+"/admin/reports/"+strconv.FormatInt(id, 10))
	if rec.AccountID != 0 {
		w.autoProtect(ctx, rec.AccountID)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

// reportWho names the operator of a report for the admins.
func reportWho(r accounts.ReportRecord) string {
	if r.Operator != "" {
		return r.Operator
	}
	if r.AccountID != 0 {
		return acctKey(r.AccountID)
	}
	return "key " + r.KeyLabel
}

// reporterKey is a keyed pseudonym of the reporter's address, kept with the
// report only to count distinct reporters (auto-protection). IPv6 counts by
// /64: an address that rotates inside its prefix is one reporter.
func (w *web) reporterKey(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return ""
	}
	b := []byte(ip.To4())
	if b == nil {
		b = ip.Mask(net.CIDRMask(64, 128))
	}
	m := hmac.New(sha256.New, w.reportKey)
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// autoProtect disables an operator account, pending an admin's review, once
// it has been reported by ReportAutoDisable distinct reporters within
// ReportAutoDisableWindow (reports closed as not abuse do not count). The
// account's sessions end, the action goes to the audit log as "system".
func (w *web) autoProtect(ctx context.Context, accountID int64) {
	limit := w.s.cfg.ReportAutoDisable
	if limit <= 0 {
		return
	}
	st := w.s.cfg.Accounts
	n, err := st.DistinctReporters(ctx, accountID, time.Now().Add(-w.s.cfg.ReportAutoDisableWindow))
	if err != nil {
		w.s.log.warn("account_db_error", "op", "distinct_reporters", "err", err.Error())
		return
	}
	if n < limit {
		return
	}
	a, err := st.ByID(ctx, accountID)
	if err != nil || a.Disabled {
		return
	}
	if err := st.SetDisabledID(ctx, accountID, true); err != nil {
		w.s.log.warn("report_autodisable_failed", "account", acctKey(accountID), "err", err.Error())
		return
	}
	if err := st.SetReview(ctx, accountID, true); err != nil {
		w.s.log.warn("account_db_error", "op", "set_review", "err", err.Error())
	}
	ended := w.s.hub.terminateAccount(accountID)
	days := int(w.s.cfg.ReportAutoDisableWindow.Hours() / 24)
	detail := fmt.Sprintf("reporters=%d days=%d sessions_ended=%d", n, days, ended)
	if err := st.Audit(ctx, accounts.AuditEntry{Actor: "system", Action: "auto_disable", TargetID: a.ID, Target: a.Email, Detail: detail}); err != nil {
		w.s.log.warn("account_db_error", "op", "audit", "err", err.Error())
	}
	w.s.m.inc(w.s.m.reports, "auto_disabled")
	w.s.m.inc(w.s.m.admin, "auto_disable")
	w.s.log.warn("report_autodisable", "account", acctKey(accountID), "reporters", n, "days", days, "sessions_ended", ended)
	w.notifyAdmins("mail.autodisable.subject", "mail.autodisable.body", w.s.cfg.Name, a.Email, strconv.Itoa(n), strconv.Itoa(days),
		w.s.cfg.PublicURL+"/admin/accounts/"+strconv.FormatInt(a.ID, 10))
}

// notifyAdmins mails the addresses of RDV_ADMIN_EMAILS (with SMTP only;
// at most MailsPerHourEmail messages per address: the console has the
// rest). English: the admins' language is not known.
func (w *web) notifyAdmins(subjectKey, bodyKey string, args ...any) {
	if w.s.cfg.Mailer == nil || w.s.cfg.PublicURL == "" {
		return
	}
	for _, to := range w.s.cfg.AdminEmails {
		w.sendMail(to, "en", subjectKey, bodyKey, args...)
	}
}
