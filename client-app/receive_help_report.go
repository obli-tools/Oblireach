package main

// receive_help_report.go — abuse reports from "Receive help",
// as the thin client (support/report.go) does them: the person helped can
// report the operator during the session and for a while after its end
// (the end notice), once per session. The report goes to the support server
// the session came through (HTTPS POST, supportlink.SendReport, the same
// User-Agent as the signalling) with the token the server gave this host
// when the session was accepted: it is bound to that session.
//
// The token lives in memory only (helpHost.rep): forgotten when the end
// notice goes, when receiving help stops, or when the window is over.
// Nothing is written on disk and the free text is never logged.
//
//	POST /local/support/help/report {reason, text, end}
//	     -> {ok: true} | {ok: false, kind: "unreachable" | "rateLimited" | "refused" | "unavailable"}
//
// With end (only while the session runs) the session is stopped here first,
// then the report goes (the server also ends it on its side).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

// helpReportReq is what the backend sends to the support server.
type helpReportReq struct {
	SID, Token string
	Reason     string
	Text       string
	EndSession bool
}

// Report outcomes (helpBackend.Report errors, wrapped) and their kinds (the
// page's help.report.err.<kind> texts).
var (
	errHelpReportUnavailable = errors.New("report: not available for this session")
	errHelpReportRefused     = errors.New("report: refused by the server")
	errHelpReportRateLimited = errors.New("report: too many reports from this address")
	errHelpReportUnreachable = errors.New("report: server unreachable")
)

const (
	helpReportErrUnavailable = "unavailable" // nothing to report (final)
	helpReportErrRefused     = "refused"     // the server said no (final)
	helpReportErrRateLimited = "rateLimited" // try again later
	helpReportErrUnreachable = "unreachable" // try again
)

// helpReportKind maps a report error to its kind ("" for sent).
func helpReportKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errHelpReportUnavailable):
		return helpReportErrUnavailable
	case errors.Is(err, errHelpReportRefused):
		return helpReportErrRefused
	case errors.Is(err, errHelpReportRateLimited):
		return helpReportErrRateLimited
	}
	return helpReportErrUnreachable
}

// helpReportResult maps supportlink.SendReport's answer to the outcomes
// above: nil also when the server already had this report (a retry after a
// lost answer).
func helpReportResult(err error) error {
	var se *supportlink.ServerError
	switch {
	case err == nil, errors.Is(err, supportlink.ErrAlreadyReported):
		return nil
	case errors.Is(err, supportlink.ErrRateLimited):
		return fmt.Errorf("%w: %v", errHelpReportRateLimited, err)
	case errors.Is(err, supportlink.ErrUnreachable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %v", errHelpReportUnreachable, err)
	case errors.Is(err, supportlink.ErrReportInvalid), errors.Is(err, supportlink.ErrReportsDisabled), errors.As(err, &se):
		return fmt.Errorf("%w: %v", errHelpReportRefused, err)
	}
	return fmt.Errorf("%w: %v", errHelpReportUnreachable, err)
}

// helpSignalOptions: what Desktop sends the support server (the signalling
// and the reports alike).
func helpSignalOptions(version string) supportlink.SignalOptions {
	return supportlink.SignalOptions{Header: http.Header{"User-Agent": {"OblireachDesktop/" + version}}}
}

// helpSendReport: the production backend's Report (supportlink).
func helpSendReport(ctx context.Context, server, version string, r helpReportReq) error {
	return helpReportResult(supportlink.SendReport(ctx, server, helpSignalOptions(version), supportlink.Report{
		SID: r.SID, Token: r.Token, Reason: r.Reason, Text: r.Text, EndSession: r.EndSession,
	}))
}

// helpReportTarget: the session that can be reported (the current one or
// the last one), memory only.
type helpReportTarget struct {
	sid, token, server string
	operator, account  string
	live               bool      // the session runs
	until              time.Time // after its end: the end of the window
	sending, sent      bool
}

// helpReportView: the page's "report" (omitted when the server gave this
// session no token: it takes no reports).
type helpReportView struct {
	// Available: the report can be sent now (during the session, or before
	// Until after its end), whether or not it was sent already.
	Available bool `json:"available"`
	// Until: the end of the window after the session (unix ms; 0 while it
	// runs).
	Until    int64  `json:"until"`
	Sent     bool   `json:"sent"`
	Sending  bool   `json:"sending,omitempty"`
	Operator string `json:"operator,omitempty"`
	Account  string `json:"account,omitempty"`
}

// reportOpenLocked: a report about rep's session can go now (whatever was
// sent).
func (h *helpHost) reportOpenLocked(now time.Time) bool {
	r := h.rep
	if r.token == "" {
		return false
	}
	if r.live {
		return h.st.Phase == helpSession
	}
	return h.st.Notice == helpNoticeEnded && now.Before(r.until)
}

// tidyReportLocked forgets the token once it cannot serve any more: the end
// notice left the screen, or the window is over and nothing was sent (a
// sent report stays said until the notice goes). Never while sending.
func (h *helpHost) tidyReportLocked(now time.Time) {
	r := h.rep
	if r.sid == "" || r.live || r.sending {
		return
	}
	if h.st.Notice != helpNoticeEnded || (!r.sent && !now.Before(r.until)) {
		h.rep = helpReportTarget{}
	}
}

// reportViewLocked: the page's view of the report, nil when there is none.
func (h *helpHost) reportViewLocked(now time.Time) *helpReportView {
	h.tidyReportLocked(now)
	r := h.rep
	if r.token == "" || !(r.live || h.st.Notice == helpNoticeEnded) {
		return nil
	}
	v := &helpReportView{Available: h.reportOpenLocked(now), Sent: r.sent, Sending: r.sending, Operator: r.operator, Account: r.account}
	if !r.live {
		v.Until = r.until.UnixMilli()
	}
	return v
}

// submitReport sends a report about the operator of the current (or last)
// session and waits for the answer: "" when the server has it, else an
// error kind. With end while the session runs, the session is stopped
// first. One report per session (a failed one may be sent again).
func (h *helpHost) submitReport(reason, text string, end bool) string {
	if !wire.ValidReportReason(reason) {
		return helpReportErrUnavailable
	}
	h.mu.Lock()
	now := h.now()
	h.tidyReportLocked(now)
	rep := h.rep
	if rep.sending || rep.sent || !h.reportOpenLocked(now) {
		h.mu.Unlock()
		return helpReportErrUnavailable
	}
	h.rep.sending = true
	endNow := end && rep.live && h.sessStop != nil
	if endNow {
		h.localStop = true
		h.sessStop()
	}
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), h.ReportTimeout)
	err := h.b.Report(ctx, rep.server, helpReportReq{SID: rep.sid, Token: rep.token, Reason: reason, Text: wire.CleanReportText(text), EndSession: endNow})
	cancel()
	kind := helpReportKind(err)

	h.mu.Lock()
	if h.rep.sid == rep.sid && h.rep.token == rep.token {
		h.rep.sending = false
		h.rep.sent = kind == ""
	}
	h.mu.Unlock()
	return kind
}
