package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oblireach/rdv/internal/accounts"
)

// Admin console (/admin): dashboard, accounts, live sessions, history,
// audit log. Admins only (403 otherwise), TOTP checked unless optional.
// Every action is a POST with CSRF, shown on a confirmation page first, and
// appended to the audit log.

const (
	adminPageSize = 50
	onlineWindow  = 10 * time.Minute // Desktop login used that recently = online
)

func (w *web) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/admin", w.page(w.adminDash))
	mux.HandleFunc("/admin/accounts", w.page(w.adminAccounts))
	mux.HandleFunc("/admin/accounts/{id}", w.page(w.adminAccount))
	mux.HandleFunc("/admin/live", w.page(w.adminLive))
	mux.HandleFunc("/admin/history", w.page(w.adminHistory))
	mux.HandleFunc("/admin/audit", w.page(w.adminAudit))
	mux.HandleFunc("/admin/reports", w.page(w.adminReports))
	mux.HandleFunc("/admin/reports/{id}", w.page(w.adminReport))
}

func pageNum(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	if p > 100000 {
		p = 100000
	}
	return p
}

type pager struct {
	Page, Pages, Total int
	Prev, Next         string
}

func makePager(r *http.Request, page, total int) pager {
	p := pager{Page: page, Total: total, Pages: (total + adminPageSize - 1) / adminPageSize}
	if p.Pages < 1 {
		p.Pages = 1
	}
	link := func(n int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(n))
		q.Del("done")
		return r.URL.Path + "?" + q.Encode()
	}
	if page > 1 {
		p.Prev = link(page - 1)
	}
	if page < p.Pages {
		p.Next = link(page + 1)
	}
	return p
}

// ---- dashboard ----

type dashData struct {
	C                     accounts.Counts
	V                     liveView
	Today, Week, Month    accounts.SessionStats
	RelayMonth            int64
	LoginsOK, LoginsFail  int
	SessChart, RelayChart template.HTML
	SignupChart           template.HTML
	Recent                []accounts.SessionRecord
	Refresh               bool
	NoRefresh             string
	HistoryDays           int
	ReportsOpen           int
	ReportsWeek           int
}

func startOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (w *web) adminDash(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	_, tmpl, st := w.adminOnly(rw, r, d)
	if tmpl != "" || st != 0 {
		return tmpl, st
	}
	d.Nav = "dash"
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store := w.s.cfg.Accounts
	now := time.Now()
	dd := &dashData{V: w.s.hub.live(), HistoryDays: int(w.s.cfg.HistoryRetention.Hours() / 24)}
	var err error
	if dd.C, err = store.AccountCounts(ctx); err != nil {
		return w.dbFail(d, "counts", err)
	}
	if dd.Today, err = store.SessionStatsSince(ctx, 0, startOfDay(now)); err != nil {
		return w.dbFail(d, "stats", err)
	}
	if dd.Week, err = store.SessionStatsSince(ctx, 0, now.Add(-7*24*time.Hour)); err != nil {
		return w.dbFail(d, "stats", err)
	}
	if dd.Month, err = store.SessionStatsSince(ctx, 0, now.Add(-30*24*time.Hour)); err != nil {
		return w.dbFail(d, "stats", err)
	}
	u := now.UTC()
	cal, err := store.SessionStatsSince(ctx, 0, time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return w.dbFail(d, "stats", err)
	}
	dd.RelayMonth = cal.Relayed
	for _, l := range dd.V.Sessions {
		dd.RelayMonth += l.Relayed
	}
	if dd.LoginsOK, dd.LoginsFail, err = store.LoginCounts(ctx, now.Add(-24*time.Hour)); err != nil {
		return w.dbFail(d, "logins", err)
	}
	if dd.ReportsOpen, dd.ReportsWeek, err = store.ReportCounts(ctx, now.Add(-7*24*time.Hour)); err != nil {
		return w.dbFail(d, "reports", err)
	}
	days, err := store.DailyStats(ctx, 0, 30)
	if err != nil {
		return w.dbFail(d, "daily", err)
	}
	dd.SessChart, dd.RelayChart = sessionsChart(d.T, days), relayChart(d.T, days)
	weeks, err := store.WeeklySignups(ctx, 12)
	if err != nil {
		return w.dbFail(d, "signups", err)
	}
	labels, vals := make([]string, len(weeks)), make([]float64, len(weeks))
	for i, n := range weeks {
		labels[i] = startOfDay(now.Add(-time.Duration(len(weeks)-1-i) * 7 * 24 * time.Hour)).Format("01-02")
		vals[i] = float64(n)
	}
	dd.SignupChart = barChart(d.T["chart.signups"], labels, []barSeries{{colDirect, vals}}, func(v float64) string { return strconv.Itoa(int(v)) })
	if dd.Recent, err = store.Sessions(ctx, 0, 15, 0); err != nil {
		return w.dbFail(d, "sessions", err)
	}
	dd.Refresh = r.URL.Query().Get("refresh") != "0"
	if dd.Refresh {
		d.Refresh = 15
	}
	d.Data = dd
	return "admin_dash", http.StatusOK
}

// ---- accounts list ----

type acctRowView struct {
	accounts.AccountRow
	Online     bool
	Live       []liveSession
	RelayLimit int64
	EnvAdmin   bool
}

type sortCol struct {
	Key, Label, URL string
	On, Desc        bool
}

type accountsData struct {
	Rows []acctRowView
	Q    string
	Cols []sortCol
	P    pager
	Now  time.Time
}

func (w *web) adminAccounts(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	_, tmpl, st := w.adminOnly(rw, r, d)
	if tmpl != "" || st != 0 {
		return tmpl, st
	}
	d.Nav = "accounts"
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	qv := r.URL.Query()
	q := strings.TrimSpace(qv.Get("q"))
	if len(q) > 64 {
		q = q[:64]
	}
	sortKey, desc := qv.Get("sort"), qv.Get("dir") == "desc"
	if !accounts.ValidSort(sortKey) {
		sortKey, desc = "created", true
	}
	page := pageNum(r)
	rows, total, err := w.s.cfg.Accounts.SearchAccounts(ctx, q, sortKey, desc, adminPageSize, (page-1)*adminPageSize)
	if err != nil {
		return w.dbFail(d, "search", err)
	}
	live := w.s.hub.live()
	ad := &accountsData{Q: q, P: makePager(r, page, total), Now: live.At}
	for _, row := range rows {
		v := acctRowView{AccountRow: row, RelayLimit: w.s.quotasFor(row.Account).RelayBytesMonth,
			EnvAdmin: slices.Contains(w.s.cfg.AdminEmails, row.Email)}
		for _, l := range live.Sessions {
			if l.AccountID == row.ID {
				v.Live = append(v.Live, l)
				v.RelayMonth += l.Relayed
			}
		}
		v.Online = len(v.Live) > 0 || live.Operators[row.ID] || (!row.TokenUsed.IsZero() && live.At.Sub(row.TokenUsed) < onlineWindow)
		ad.Rows = append(ad.Rows, v)
	}
	for _, c := range []struct{ key, label string }{
		{"name", "col.name"}, {"email", "col.email"}, {"verified", "col.verified"}, {"role", "col.role"},
		{"created", "col.created"}, {"login", "col.lastlogin"}, {"sessions", "col.sessions30"}, {"relay", "col.relay"},
	} {
		sc := sortCol{Key: c.key, Label: d.T[c.label], On: c.key == sortKey, Desc: desc}
		v := url.Values{}
		if q != "" {
			v.Set("q", q)
		}
		v.Set("sort", c.key)
		if c.key == sortKey && !desc {
			v.Set("dir", "desc")
		}
		sc.URL = "/admin/accounts?" + v.Encode()
		ad.Cols = append(ad.Cols, sc)
	}
	d.Data = ad
	return "admin_accounts", http.StatusOK
}

// ---- account detail and actions ----

type accountData struct {
	A           accounts.Account
	Q, Defaults AccountQuotas
	Today       int
	RelayMonth  int64
	RelayMeter  template.HTML
	S30         accounts.SessionStats
	Tokens      []accounts.TokenInfo
	WebSessions []accounts.WebSession
	History     []accounts.SessionRecord
	P           pager
	Logins      []accounts.LoginRecord
	Live        []liveSession
	Online      bool
	TokenUsed   time.Time
	EnvAdmin    bool
	Self        bool
	Now         time.Time
	Reports     []accounts.ReportRecord
	OpenReports int
}

type confirmData struct {
	Op      string
	OpLabel string
	Target  accounts.Account
	Hidden  [][2]string
	Lines   []string
	Typed   bool // delete: type the email address
	PostTo  string
	Back    string
	Danger  bool
}

type linkData struct {
	Target accounts.Account
	Link   string
	Back   string
}

func (w *web) adminAccount(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	me, tmpl, st := w.adminOnly(rw, r, d)
	if me == nil {
		return tmpl, st
	}
	d.Nav = "accounts"
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return message(d, "notfound.title", "notfound.p1"), http.StatusNotFound
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store := w.s.cfg.Accounts
	a, err := store.ByID(ctx, id)
	if errors.Is(err, accounts.ErrNotFound) {
		return message(d, "notfound.title", "notfound.p1"), http.StatusNotFound
	} else if err != nil {
		return w.dbFail(d, "by_id", err)
	}
	if r.Method == http.MethodPost {
		return w.adminAction(ctx, rw, r, d, me, a)
	}
	return w.accountDetail(ctx, rw, r, d, me, a, http.StatusOK)
}

func (w *web) accountDetail(ctx context.Context, rw http.ResponseWriter, r *http.Request, d *pageData, me *user, a accounts.Account, status int) (string, int) {
	store := w.s.cfg.Accounts
	ad := &accountData{A: a, Q: w.s.quotasFor(a), Defaults: w.s.cfg.Quotas, Self: a.ID == me.a.ID,
		EnvAdmin: slices.Contains(w.s.cfg.AdminEmails, a.Email), Now: time.Now()}
	usage, err := store.Usage(ctx, a.ID)
	if err != nil {
		return w.dbFail(d, "usage", err)
	}
	live := w.s.hub.live()
	ad.Today, ad.RelayMonth = usage.SessionsToday, usage.RelayBytesMonth
	for _, l := range live.Sessions {
		if l.AccountID == a.ID {
			ad.Live = append(ad.Live, l)
			ad.RelayMonth += l.Relayed
		}
	}
	ad.RelayMeter = meter(ad.RelayMonth, ad.Q.RelayBytesMonth)
	if ad.S30, err = store.SessionStatsSince(ctx, a.ID, time.Now().Add(-30*24*time.Hour)); err != nil {
		return w.dbFail(d, "stats", err)
	}
	if ad.Tokens, err = store.Tokens(ctx, a.ID); err != nil {
		return w.dbFail(d, "tokens", err)
	}
	if ad.WebSessions, err = store.WebSessions(ctx, a.ID); err != nil {
		return w.dbFail(d, "web_sessions", err)
	}
	if ad.TokenUsed, err = store.LastTokenUse(ctx, a.ID); err != nil {
		return w.dbFail(d, "tokens", err)
	}
	ad.Online = len(ad.Live) > 0 || live.Operators[a.ID] || (!ad.TokenUsed.IsZero() && live.At.Sub(ad.TokenUsed) < onlineWindow)
	page := pageNum(r)
	if ad.History, err = store.Sessions(ctx, a.ID, adminPageSize, (page-1)*adminPageSize); err != nil {
		return w.dbFail(d, "sessions", err)
	}
	total := (page-1)*adminPageSize + len(ad.History)
	if len(ad.History) == adminPageSize {
		total++ // one more page at least
	}
	ad.P = makePager(r, page, total)
	if ad.Logins, err = store.Logins(ctx, a.ID, 30); err != nil {
		return w.dbFail(d, "logins", err)
	}
	if ad.Reports, _, err = store.Reports(ctx, accounts.ReportFilter{AccountID: a.ID}, 50, 0); err != nil {
		return w.dbFail(d, "reports", err)
	}
	for _, rp := range ad.Reports {
		if rp.Status == accounts.ReportOpen {
			ad.OpenReports++
		}
	}
	d.Data = ad
	d.Wide = true
	w.fillForm(rw, r, d, false)
	return "admin_account", status
}

// adminOps are the account actions: label key, dangerous, needs typing.
var adminOps = map[string]struct {
	label  string
	danger bool
}{
	"verify": {"op.verify", false}, "resend": {"op.resend", false}, "disable": {"op.disable", true}, "enable": {"op.enable", false},
	"reset_pw": {"op.reset_pw", false}, "revoke_tokens": {"op.revoke_tokens", true}, "quota": {"op.quota", false},
	"promote": {"op.promote", true}, "demote": {"op.demote", true}, "reset_2fa": {"op.reset_2fa", true},
	"terminate": {"op.terminate", true}, "delete": {"op.delete", true}, "ban": {"op.ban", true},
}

// retPath is where an account action started from the report page goes
// back to ("" for the account page): only a report page of this console.
func retPath(r *http.Request) string {
	v := r.PostFormValue("ret")
	id, ok := strings.CutPrefix(v, "/admin/reports/")
	if !ok || id == "" || len(id) > 18 {
		return ""
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return v
}

func parseQuotaField(v string, max int64) (*int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > max {
		return nil, false
	}
	return &n, true
}

func (w *web) adminAction(ctx context.Context, rw http.ResponseWriter, r *http.Request, d *pageData, me *user, a accounts.Account) (string, int) {
	store := w.s.cfg.Accounts
	back := "/admin/accounts/" + strconv.FormatInt(a.ID, 10)
	fail := func(key string, status int) (string, int) {
		d.FormErr = d.T[key]
		return w.accountDetail(ctx, rw, r, d, me, a, status)
	}
	if !w.parsePost(rw, r) {
		return fail("err.form", http.StatusForbidden)
	}
	op := r.PostFormValue("op")
	spec, ok := adminOps[op]
	if !ok {
		return fail("err.form", http.StatusBadRequest)
	}
	ret := retPath(r)
	postTo := back // the confirmation posts here; back is where it returns
	if ret != "" {
		back = ret
	}
	self := a.ID == me.a.ID
	envAdmin := slices.Contains(w.s.cfg.AdminEmails, a.Email)
	switch {
	case self && (op == "disable" || op == "demote" || op == "delete" || op == "reset_2fa" || op == "ban"):
		return fail("err.self", http.StatusConflict)
	case op == "demote" && envAdmin:
		return fail("err.envadmin", http.StatusConflict)
	case op == "resend" && (w.s.cfg.Mailer == nil || a.Verified):
		return fail("err.form", http.StatusBadRequest)
	}

	// Parameters of the quota action.
	var quota accounts.QuotaOverride
	var lines []string
	hidden := [][2]string{{"op", op}}
	if ret != "" {
		hidden = append(hidden, [2]string{"ret", ret})
	}
	if op == "ban" {
		lines = append(lines, d.T["ban.p1"])
	}
	if op == "quota" {
		var ok1, ok2, ok3 bool
		quota.MaxSessions, ok1 = parseQuotaField(r.PostFormValue("max_sessions"), 10_000)
		quota.SessionsDay, ok2 = parseQuotaField(r.PostFormValue("sessions_day"), 1_000_000)
		gb, ok3 := parseQuotaField(r.PostFormValue("relay_gb"), 1_000_000)
		if !ok1 || !ok2 || !ok3 {
			return fail("err.quota", http.StatusUnprocessableEntity)
		}
		if gb != nil {
			b := *gb * 1_000_000_000
			quota.RelayBytes = &b
		}
		for _, f := range []string{"max_sessions", "sessions_day", "relay_gb"} {
			hidden = append(hidden, [2]string{f, strings.TrimSpace(r.PostFormValue(f))})
		}
		show := func(label string, p *int64, unit int64, def string) string {
			if p == nil {
				return d.T[label] + ": " + d.T["quota.default"] + " (" + def + ")"
			}
			if *p == 0 {
				return d.T[label] + ": " + d.T["quota.unlimited"]
			}
			return d.T[label] + ": " + strconv.FormatInt(*p/unit, 10)
		}
		q := w.s.cfg.Quotas
		lines = append(lines,
			show("quota.max", quota.MaxSessions, 1, quotaText(d.T, int64(q.MaxConcurrent), 1)),
			show("quota.day", quota.SessionsDay, 1, quotaText(d.T, int64(q.SessionsPerDay), 1)),
			show("quota.relay", quota.RelayBytes, 1_000_000_000, quotaText(d.T, q.RelayBytesMonth, 1_000_000_000)))
	}

	if r.PostFormValue("confirmed") != "1" {
		cd := &confirmData{Op: op, OpLabel: d.T[spec.label], Target: a, Hidden: hidden, Lines: lines, Typed: op == "delete",
			PostTo: postTo, Back: back, Danger: spec.danger}
		d.Data = cd
		w.fillForm(rw, r, d, false)
		return "admin_confirm", http.StatusOK
	}
	if op == "delete" && accounts.NormalizeEmail(r.PostFormValue("typed")) != a.Email {
		cd := &confirmData{Op: op, OpLabel: d.T[spec.label], Target: a, Hidden: hidden, Typed: true, PostTo: postTo, Back: back, Danger: true}
		d.Data = cd
		d.FormErr = d.T["del.mismatch"]
		w.fillForm(rw, r, d, false)
		return "admin_confirm", http.StatusUnprocessableEntity
	}

	var err error
	detail := ""
	switch op {
	case "verify":
		err = store.SetVerified(ctx, a.ID)
		if err == nil && envAdmin {
			_, err = store.EnsureAdmins(ctx, []string{a.Email})
		}
	case "resend":
		var hash string
		if _, hash, err = store.ByEmail(ctx, a.Email); err == nil {
			var secret string
			if secret, err = store.AddPending(ctx, a.Email, a.Name, hash); err == nil {
				w.sendMail(a.Email, d.Lang, "mail.verify.subject", "mail.verify.body", w.s.cfg.PublicURL, w.s.cfg.PublicURL+"/verify?token="+url.QueryEscape(secret))
			}
		}
	case "disable", "enable":
		err = store.SetDisabledID(ctx, a.ID, op == "disable")
		if err == nil && op == "disable" {
			detail = fmt.Sprintf("sessions_ended=%d", w.s.hub.terminateAccount(a.ID))
		}
		if err == nil && op == "enable" && (a.Banned || !a.Review.IsZero()) {
			detail = fmt.Sprintf("banned=%v review=%v (lifted)", a.Banned, !a.Review.IsZero())
		}
	case "ban":
		reason := "admin"
		if ret != "" {
			reason = "report " + strings.TrimPrefix(ret, "/admin/reports/")
		}
		if err = store.BanAccount(ctx, a.ID, me.a.Email, reason); err == nil {
			detail = fmt.Sprintf("sessions_ended=%d %s", w.s.hub.terminateAccount(a.ID), reason)
		}
	case "reset_pw":
		var secret string
		if secret, err = store.NewActionToken(ctx, a.ID, accounts.ActionReset, "", resetTTL); err == nil {
			link := w.baseURL(r) + "/reset?token=" + url.QueryEscape(secret)
			if w.s.cfg.Mailer != nil {
				w.sendMail(a.Email, d.Lang, "mail.reset.subject", "mail.reset.body", w.s.cfg.PublicURL, w.s.cfg.PublicURL+"/reset?token="+url.QueryEscape(secret))
				detail = "mailed"
			} else {
				detail = "link_shown"
				w.audit(ctx, r, me.a, op, a, detail)
				d.Data = &linkData{Target: a, Link: link, Back: back}
				return "admin_link", http.StatusOK
			}
		}
	case "revoke_tokens":
		var n, m int64
		if n, err = store.RevokeTokens(ctx, a.ID); err == nil {
			m, err = store.DeleteWebSessions(ctx, a.ID, "")
		}
		detail = fmt.Sprintf("tokens=%d web_sessions=%d", n, m)
	case "quota":
		err = store.SetQuota(ctx, a.ID, quota)
		detail = strings.Join(lines, "; ")
	case "promote":
		err = store.SetRole(ctx, a.ID, accounts.RoleAdmin)
	case "demote":
		err = store.SetRole(ctx, a.ID, accounts.RoleOperator)
	case "reset_2fa":
		if err = store.SetTOTP(ctx, a.ID, nil); err == nil {
			_, err = store.DeleteWebSessions(ctx, a.ID, "")
		}
	case "terminate":
		detail = fmt.Sprintf("sessions_ended=%d", w.s.hub.terminateAccount(a.ID))
	case "delete":
		if err = store.DeleteID(ctx, a.ID); err == nil {
			detail = fmt.Sprintf("sessions_ended=%d", w.s.hub.terminateAccount(a.ID))
			w.audit(ctx, r, me.a, op, a, detail)
			return redirect(rw, "/admin/accounts?done=deleted")
		}
	}
	switch {
	case errors.Is(err, accounts.ErrLastAdmin):
		return fail("err.lastadmin", http.StatusConflict)
	case errors.Is(err, accounts.ErrNotFound):
		return message(d, "notfound.title", "notfound.p1"), http.StatusNotFound
	case err != nil:
		return w.dbFail(d, "admin_"+op, err)
	}
	w.audit(ctx, r, me.a, op, a, detail)
	return redirect(rw, back+"?done="+op)
}

func quotaText(t map[string]string, v, unit int64) string {
	if v <= 0 {
		return t["quota.unlimited"]
	}
	return strconv.FormatInt(v/unit, 10)
}

// ---- live sessions ----

type liveRow struct {
	liveSession
	Email string
	Name  string
}

type liveData struct {
	V       liveView
	Rows    []liveRow
	Refresh bool
}

func (w *web) liveRows(ctx context.Context, v liveView) []liveRow {
	names := map[int64]accounts.Account{}
	var out []liveRow
	for _, l := range v.Sessions {
		row := liveRow{liveSession: l}
		if l.AccountID != 0 {
			a, ok := names[l.AccountID]
			if !ok {
				a, _ = w.s.cfg.Accounts.ByID(ctx, l.AccountID)
				names[l.AccountID] = a
			}
			row.Email, row.Name = a.Email, a.Name
		}
		out = append(out, row)
	}
	return out
}

func (w *web) adminLive(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	me, tmpl, st := w.adminOnly(rw, r, d)
	if me == nil {
		return tmpl, st
	}
	d.Nav = "live"
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.Method == http.MethodPost {
		if !w.parsePost(rw, r) {
			d.FormErr = d.T["err.form"]
		} else {
			handle := r.PostFormValue("handle")
			v := w.s.hub.live()
			var target *liveSession
			for i := range v.Sessions {
				if v.Sessions[i].Handle == handle {
					target = &v.Sessions[i]
				}
			}
			if target == nil {
				return redirect(rw, "/admin/live?done=gone")
			}
			if r.PostFormValue("confirmed") != "1" {
				rows := w.liveRows(ctx, liveView{Sessions: []liveSession{*target}})
				who := rows[0].Email
				if who == "" {
					who = rows[0].KeyLabel
				}
				d.Data = &confirmData{Op: "terminate", OpLabel: d.T["op.terminate_one"], Hidden: [][2]string{{"handle", handle}},
					Lines: []string{d.T["col.operator"] + ": " + who, d.T["col.host"] + ": " + target.HostTID,
						d.T["col.duration"] + ": " + fmtDur(target.Duration(time.Now()))},
					PostTo: "/admin/live", Back: "/admin/live", Danger: true}
				w.fillForm(rw, r, d, false)
				return "admin_confirm", http.StatusOK
			}
			if w.s.hub.terminate(handle) {
				var a accounts.Account
				if target.AccountID != 0 {
					a, _ = w.s.cfg.Accounts.ByID(ctx, target.AccountID)
				} else {
					a.Email = "key:" + target.KeyLabel
				}
				w.audit(ctx, r, me.a, "terminate_session", a, "host="+target.HostTID)
				return redirect(rw, "/admin/live?done=terminated")
			}
			return redirect(rw, "/admin/live?done=gone")
		}
	}
	ld := &liveData{V: w.s.hub.live(), Refresh: r.URL.Query().Get("refresh") != "0"}
	ld.Rows = w.liveRows(ctx, ld.V)
	if ld.Refresh {
		d.Refresh = 15
	}
	d.Data = ld
	w.fillForm(rw, r, d, false)
	return "admin_live", http.StatusOK
}

// ---- history and audit ----

type historyData struct {
	Rows []accounts.SessionRecord
	P    pager
	Days int
}

func (w *web) adminHistory(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	_, tmpl, st := w.adminOnly(rw, r, d)
	if tmpl != "" || st != 0 {
		return tmpl, st
	}
	d.Nav = "history"
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	page := pageNum(r)
	rows, err := w.s.cfg.Accounts.Sessions(ctx, 0, adminPageSize, (page-1)*adminPageSize)
	if err != nil {
		return w.dbFail(d, "sessions", err)
	}
	total := (page-1)*adminPageSize + len(rows)
	if len(rows) == adminPageSize {
		total++
	}
	d.Data = &historyData{Rows: rows, P: makePager(r, page, total), Days: int(w.s.cfg.HistoryRetention.Hours() / 24)}
	return "admin_history", http.StatusOK
}

type auditData struct {
	Rows []accounts.AuditEntry
	P    pager
	Days int
}

func (w *web) adminAudit(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	_, tmpl, st := w.adminOnly(rw, r, d)
	if tmpl != "" || st != 0 {
		return tmpl, st
	}
	d.Nav = "audit"
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	page := pageNum(r)
	rows, total, err := w.s.cfg.Accounts.AuditLog(ctx, adminPageSize, (page-1)*adminPageSize)
	if err != nil {
		return w.dbFail(d, "audit", err)
	}
	d.Data = &auditData{Rows: rows, P: makePager(r, page, total), Days: int(w.s.cfg.AuditRetention.Hours() / 24)}
	return "admin_audit", http.StatusOK
}
