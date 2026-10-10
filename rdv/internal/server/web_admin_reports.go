package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oblireach/rdv/internal/accounts"
)

// Admin pages of the abuse reports: the list (open / closed / all) and a
// report with its session, the operator's other reports and sessions, the
// notes, and the actions (close as handled or as not abuse, reopen, add a
// note; disable or ban the operator account through the account actions).
// Every action goes through the audit log; status changes are confirmed
// first.

type reportTab struct {
	Key, Label, URL string
	On              bool
}

type reportsData struct {
	Rows   []accounts.ReportRecord
	Status string
	Tabs   []reportTab
	P      pager
	Days   int
}

func (w *web) adminReports(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	_, tmpl, st := w.adminOnly(rw, r, d)
	if tmpl != "" || st != 0 {
		return tmpl, st
	}
	d.Nav = "reports"
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	status := r.URL.Query().Get("status")
	switch status {
	case accounts.ReportOpen, "closed", "all":
	default:
		status = accounts.ReportOpen
	}
	filter := status
	if status == "all" {
		filter = ""
	}
	page := pageNum(r)
	rows, total, err := w.s.cfg.Accounts.Reports(ctx, accounts.ReportFilter{Status: filter}, adminPageSize, (page-1)*adminPageSize)
	if err != nil {
		return w.dbFail(d, "reports", err)
	}
	rd := &reportsData{Rows: rows, Status: status, P: makePager(r, page, total), Days: int(w.s.cfg.HistoryRetention.Hours() / 24)}
	for _, k := range []string{accounts.ReportOpen, "closed", "all"} {
		rd.Tabs = append(rd.Tabs, reportTab{Key: k, Label: d.T["rtab."+k], URL: "/admin/reports?status=" + k, On: k == status})
	}
	d.Data = rd
	return "admin_reports", http.StatusOK
}

type reportView struct {
	R          accounts.ReportRecord
	Hist       *accounts.ReportableSession // the history row of the session (nil while it runs or once purged)
	Live       bool                        // the session is still in progress
	A          accounts.Account
	HasAccount bool
	Self       bool
	Others     []accounts.ReportRecord // the operator's other reports
	Sessions   []accounts.SessionRecord
	Ret        string // this page (account actions come back here)
	Retention  int
}

// Report actions on the report page.
var reportOps = map[string]struct {
	label, status string
}{
	"handled":   {"op.report_handled", accounts.ReportHandled},
	"not_abuse": {"op.report_not_abuse", accounts.ReportNotAbuse},
	"reopen":    {"op.report_reopen", accounts.ReportOpen},
}

func (w *web) adminReport(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	me, tmpl, st := w.adminOnly(rw, r, d)
	if me == nil {
		return tmpl, st
	}
	d.Nav = "reports"
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return message(d, "notfound.title", "notfound.p1"), http.StatusNotFound
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store := w.s.cfg.Accounts
	rep, err := store.Report(ctx, id)
	if errors.Is(err, accounts.ErrNotFound) {
		return message(d, "notfound.title", "notfound.p1"), http.StatusNotFound
	} else if err != nil {
		return w.dbFail(d, "report", err)
	}
	if r.Method == http.MethodPost {
		return w.reportAction(ctx, rw, r, d, me, rep)
	}
	return w.reportDetail(ctx, rw, r, d, me, rep, http.StatusOK)
}

func (w *web) reportDetail(ctx context.Context, rw http.ResponseWriter, r *http.Request, d *pageData, me *user, rep accounts.ReportRecord, status int) (string, int) {
	store := w.s.cfg.Accounts
	v := &reportView{R: rep, Ret: "/admin/reports/" + strconv.FormatInt(rep.ID, 10), Retention: int(w.s.cfg.HistoryRetention.Hours() / 24)}
	if h, err := store.SessionBySID(ctx, rep.SID); err == nil {
		v.Hist = &h
	} else if !errors.Is(err, accounts.ErrNotFound) {
		return w.dbFail(d, "session_by_sid", err)
	}
	for _, l := range w.s.hub.live().Sessions {
		if strings.HasPrefix(rep.SID, l.Handle) {
			v.Live = true
		}
	}
	var err error
	if rep.AccountID != 0 {
		if v.A, err = store.ByID(ctx, rep.AccountID); err == nil {
			v.HasAccount = true
			v.Self = v.A.ID == me.a.ID
		} else if !errors.Is(err, accounts.ErrNotFound) {
			return w.dbFail(d, "by_id", err)
		}
		if v.Others, _, err = store.Reports(ctx, accounts.ReportFilter{AccountID: rep.AccountID}, 50, 0); err != nil {
			return w.dbFail(d, "reports", err)
		}
		if v.Sessions, err = store.Sessions(ctx, rep.AccountID, 20, 0); err != nil {
			return w.dbFail(d, "sessions", err)
		}
	} else if rep.KeyLabel != "" {
		if v.Others, _, err = store.Reports(ctx, accounts.ReportFilter{KeyLabel: rep.KeyLabel}, 50, 0); err != nil {
			return w.dbFail(d, "reports", err)
		}
	}
	v.Others = slices.DeleteFunc(v.Others, func(o accounts.ReportRecord) bool { return o.ID == rep.ID })
	d.Data = v
	d.Wide = true
	w.fillForm(rw, r, d, false)
	return "admin_report", status
}

func (w *web) reportAction(ctx context.Context, rw http.ResponseWriter, r *http.Request, d *pageData, me *user, rep accounts.ReportRecord) (string, int) {
	store := w.s.cfg.Accounts
	back := "/admin/reports/" + strconv.FormatInt(rep.ID, 10)
	fail := func(key string, status int) (string, int) {
		d.FormErr = d.T[key]
		return w.reportDetail(ctx, rw, r, d, me, rep, status)
	}
	if !w.parsePost(rw, r) {
		return fail("err.form", http.StatusForbidden)
	}
	target := accounts.Account{ID: rep.AccountID}
	if rep.AccountID != 0 {
		if a, err := store.ByID(ctx, rep.AccountID); err == nil {
			target = a
		}
	}
	if target.Email == "" {
		target.Email = "key:" + rep.KeyLabel
	}
	detail := "report=" + strconv.FormatInt(rep.ID, 10)
	op := r.PostFormValue("op")
	if op == "note" {
		text := strings.TrimSpace(r.PostFormValue("note"))
		if text == "" || len([]rune(text)) > accounts.MaxNoteLen {
			return fail("err.note", http.StatusUnprocessableEntity)
		}
		if err := store.AddReportNote(ctx, rep.ID, me.a.ID, me.a.Email, text); err != nil {
			return w.dbFail(d, "report_note", err)
		}
		w.audit(ctx, r, me.a, "report_note", target, detail)
		return redirect(rw, back+"?done=report_note")
	}
	spec, ok := reportOps[op]
	if !ok {
		return fail("err.form", http.StatusBadRequest)
	}
	if (spec.status == accounts.ReportOpen) == (rep.Status == accounts.ReportOpen) {
		return redirect(rw, back) // already in that state
	}
	if r.PostFormValue("confirmed") != "1" {
		lines := []string{d.T["col.report"] + ": #" + strconv.FormatInt(rep.ID, 10) + " (" + d.T["rreason."+rep.Reason] + ")",
			d.T["col.operator"] + ": " + reportWho(rep)}
		d.Data = &confirmData{Op: op, OpLabel: d.T[spec.label], Hidden: [][2]string{{"op", op}}, Lines: lines,
			PostTo: back, Back: back, Danger: false}
		w.fillForm(rw, r, d, false)
		return "admin_confirm", http.StatusOK
	}
	if err := store.SetReportStatus(ctx, rep.ID, spec.status, me.a.Email); err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			return message(d, "notfound.title", "notfound.p1"), http.StatusNotFound
		}
		return w.dbFail(d, "report_status", err)
	}
	action := "report_" + op
	w.audit(ctx, r, me.a, action, target, detail)
	return redirect(rw, back+"?done="+action)
}
