package main

// Abuse reports: the person helped can report the operator
// during the session (banner, main window) and for a few minutes after it
// (end screen). The report goes to the rendezvous server over HTTPS with the
// token the server gave this host when the session was accepted: it is
// bound to that session. Nothing is written on disk.
//
// The dialog is a small state machine, independent of the UI:
//
//	editing ──send──► sending ──ok──► sent
//	   ▲                 │
//	   └──── edit ◄── failed (retry)

import (
	"errors"

	"github.com/oblireach/supportlink/wire"
)

// reportReasons in display order (wire values).
var reportReasons = []string{wire.ReportScam, wire.ReportUnwanted, wire.ReportHarassment, wire.ReportOther}

// reportReasonKeys: the i18n key of each reason.
var reportReasonKeys = map[string]string{
	wire.ReportScam:       "report.reason.scam",
	wire.ReportUnwanted:   "report.reason.unwanted",
	wire.ReportHarassment: "report.reason.harassment",
	wire.ReportOther:      "report.reason.other",
}

// ReportRequest is what the backend sends to the rendezvous server.
type ReportRequest struct {
	SID, Token string
	Reason     string
	Text       string
	EndSession bool
}

// ReportForm is what the person filled in.
type ReportForm struct {
	Reason     string
	Text       string
	EndSession bool
}

// Report errors (Backend.Report) and their kinds (an i18n
// "report.err.<kind>" key).
var (
	ErrReportUnavailable = errors.New("report: not available for this session")
	ErrReportRefused     = errors.New("report: refused by the server (unknown session or window over)")
	ErrReportRateLimited = errors.New("report: too many reports from this address")
	ErrReportUnreachable = errors.New("report: server unreachable")
)

const (
	ReportErrUnavailable = "unavailable"
	ReportErrRefused     = "refused"
	ReportErrRateLimited = "rateLimited"
	ReportErrUnreachable = "unreachable"
)

// reportErrKind maps a report error to its kind ("" for success).
func reportErrKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrReportUnavailable):
		return ReportErrUnavailable
	case errors.Is(err, ErrReportRefused):
		return ReportErrRefused
	case errors.Is(err, ErrReportRateLimited):
		return ReportErrRateLimited
	}
	return ReportErrUnreachable
}

// ReportStep is the state of the dialog.
type ReportStep int

const (
	ReportEditing ReportStep = iota
	ReportSending
	ReportSent
	ReportFailed
)

// ReportDialog is the state of the report dialog.
type ReportDialog struct {
	Step   ReportStep
	Reason string // "" until the person picks one
	Text   string
	// EndSession: "End the session now" (on by default); it only applies
	// while the session runs (SessionRunning).
	EndSession     bool
	SessionRunning bool
	ErrKind        string // ReportFailed
}

// NewReportDialog opens a dialog; running tells whether the session is
// still in progress.
func NewReportDialog(running bool) *ReportDialog {
	return &ReportDialog{EndSession: true, SessionRunning: running}
}

// Editable: the form can change (not while sending, not once sent).
func (d *ReportDialog) Editable() bool { return d.Step == ReportEditing || d.Step == ReportFailed }

// SetReason picks a reason (one of reportReasons).
func (d *ReportDialog) SetReason(r string) {
	if d.Editable() && wire.ValidReportReason(r) {
		d.Reason = r
	}
}

// SetText keeps the free text, cut to the server's limit.
func (d *ReportDialog) SetText(s string) {
	if !d.Editable() {
		return
	}
	if r := []rune(s); len(r) > wire.ReportMaxText {
		s = string(r[:wire.ReportMaxText])
	}
	d.Text = s
}

// ToggleEnd flips "End the session now" (only while the session runs).
func (d *ReportDialog) ToggleEnd() {
	if d.Editable() && d.SessionRunning {
		d.EndSession = !d.EndSession
	}
}

// SessionEnded: the session is over (the option disappears).
func (d *ReportDialog) SessionEnded() { d.SessionRunning = false }

// CanSubmit: a reason is chosen, nothing is being sent, and a failure
// left something to retry.
func (d *ReportDialog) CanSubmit() bool {
	return d.Editable() && d.Reason != "" && (d.Step != ReportFailed || d.Retryable())
}

// Submit moves to sending and returns the form to send.
func (d *ReportDialog) Submit() (ReportForm, bool) {
	if !d.CanSubmit() {
		return ReportForm{}, false
	}
	d.Step, d.ErrKind = ReportSending, ""
	return ReportForm{Reason: d.Reason, Text: wire.CleanReportText(d.Text), EndSession: d.EndSession && d.SessionRunning}, true
}

// Finish applies the result of a send ("" = sent, else an error kind).
func (d *ReportDialog) Finish(kind string) {
	if d.Step != ReportSending {
		return
	}
	if kind == "" {
		d.Step = ReportSent
		return
	}
	d.Step, d.ErrKind = ReportFailed, kind
}

// Retryable: a failed send may be tried again (not when the server
// refused the report for good).
func (d *ReportDialog) Retryable() bool {
	return d.Step == ReportFailed && d.ErrKind != ReportErrRefused && d.ErrKind != ReportErrUnavailable
}
