package server

import (
	"context"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oblireach/rdv/internal/accounts"
)

// Operator area (/account): own statistics and history, profile, password,
// TOTP, Desktop logins, account deletion. Every signed-in account has it.

func (w *web) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/account", w.page(w.acctOverview))
	mux.HandleFunc("/account/settings", w.page(w.acctSettings))
	mux.HandleFunc("/account/security", w.page(w.acctSecurity))
	mux.HandleFunc("/account/2fa", w.page(w.acctTOTPSetup))
}

type quotaView struct {
	MaxConcurrent  int
	SessionsPerDay int
	RelayBytes     int64
}

type overviewData struct {
	Live         []liveSession
	S7, S30      accounts.SessionStats
	DirectPct    int
	RelayPct     int
	Today        int
	RelayMonth   int64
	Quota        quotaView
	RelayMeter   template.HTML
	DayMeter     template.HTML
	Recent       []accounts.SessionRecord
	Chart        template.HTML
	Now          time.Time
	Unverified   bool
	AdminNoTOTP  bool
	TOTPOptional bool
	HistoryDays  int
	UsageFailed  bool
}

func (w *web) acctOverview(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	u, tmpl, st := w.signedIn(rw, r, d)
	if u == nil {
		return tmpl, st
	}
	if r.Method == http.MethodPost {
		return message(d, "notfound.title", "notfound.p1"), http.StatusMethodNotAllowed
	}
	d.Nav, d.Wide = "acct", true
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store := w.s.cfg.Accounts
	now := time.Now()
	q := w.s.quotasFor(u.a)
	od := &overviewData{Now: now, Unverified: !u.a.Verified, HistoryDays: int(w.s.cfg.HistoryRetention.Hours() / 24),
		Quota:       quotaView{q.MaxConcurrent, q.SessionsPerDay, q.RelayBytesMonth},
		AdminNoTOTP: u.a.IsAdmin() && !u.a.TOTP, TOTPOptional: w.s.cfg.AdminTOTPOptional}
	var err error
	if od.S7, err = store.SessionStatsSince(ctx, u.a.ID, now.Add(-7*24*time.Hour)); err != nil {
		return w.dbFail(d, "stats", err)
	}
	if od.S30, err = store.SessionStatsSince(ctx, u.a.ID, now.Add(-30*24*time.Hour)); err != nil {
		return w.dbFail(d, "stats", err)
	}
	if od.S30.Sessions > 0 {
		od.DirectPct = (od.S30.Direct*100 + od.S30.Sessions/2) / od.S30.Sessions
		od.RelayPct = (od.S30.Relay*100 + od.S30.Sessions/2) / od.S30.Sessions
	}
	usage, err := store.Usage(ctx, u.a.ID)
	if err != nil {
		return w.dbFail(d, "usage", err)
	}
	od.Today = usage.SessionsToday
	od.RelayMonth = usage.RelayBytesMonth + w.s.hub.liveRelay(u.a.ID)
	od.RelayMeter = meter(od.RelayMonth, q.RelayBytesMonth)
	od.DayMeter = meter(int64(od.Today), int64(q.SessionsPerDay))
	for _, l := range w.s.hub.live().Sessions {
		if l.AccountID == u.a.ID {
			od.Live = append(od.Live, l)
		}
	}
	if od.Recent, err = store.Sessions(ctx, u.a.ID, 20, 0); err != nil {
		return w.dbFail(d, "sessions", err)
	}
	days, err := store.DailyStats(ctx, u.a.ID, 30)
	if err != nil {
		return w.dbFail(d, "daily", err)
	}
	od.Chart = sessionsChart(d.T, days)
	d.Data = od
	return "acct_overview", http.StatusOK
}

// dbFail answers a database error with a generic page.
func (w *web) dbFail(d *pageData, op string, err error) (string, int) {
	w.s.log.warn("account_db_error", "op", op, "err", err.Error())
	return message(d, "error.title", "err.server"), http.StatusServiceUnavailable
}

// sessionsChart is the daily sessions chart (direct, relayed, unknown path).
func sessionsChart(t map[string]string, days []accounts.DayStat) template.HTML {
	labels := make([]string, len(days))
	direct, relay, other := make([]float64, len(days)), make([]float64, len(days)), make([]float64, len(days))
	for i, d := range days {
		labels[i] = d.Day[5:]
		direct[i], relay[i], other[i] = float64(d.Direct), float64(d.Relay), float64(d.Other)
	}
	return barChart(t["chart.sessions"], labels, []barSeries{{colDirect, direct}, {colRelay, relay}, {colOther, other}},
		func(v float64) string { return strconv.Itoa(int(v)) })
}

// relayChart is the daily relayed volume chart.
func relayChart(t map[string]string, days []accounts.DayStat) template.HTML {
	labels := make([]string, len(days))
	v := make([]float64, len(days))
	for i, d := range days {
		labels[i] = d.Day[5:]
		v[i] = float64(d.Relayed)
	}
	return barChart(t["chart.relay"], labels, []barSeries{{colRelay, v}}, func(x float64) string { return fmtBytes(int64(x)) })
}

// ---- settings: name, email, password ----

func (w *web) acctSettings(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	u, tmpl, st := w.signedIn(rw, r, d)
	if u == nil {
		return tmpl, st
	}
	d.Nav = "settings"
	d.Name, d.Email = u.a.Name, u.a.Email
	if r.Method != http.MethodPost {
		w.fillForm(rw, r, d, false)
		return "acct_settings", http.StatusOK
	}
	if !w.parsePost(rw, r) {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "acct_settings", http.StatusForbidden
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store := w.s.cfg.Accounts
	again := func(status int) (string, int) {
		w.fillForm(rw, r, d, false)
		return "acct_settings", status
	}
	switch r.PostFormValue("op") {
	case "name":
		name, ok := accounts.CleanDisplayName(r.PostFormValue("name"))
		if !ok {
			d.Name = r.PostFormValue("name")
			d.Errs["name"] = d.T["err.name"]
			return again(http.StatusUnprocessableEntity)
		}
		if err := store.SetName(ctx, u.a.ID, name); err != nil {
			return w.dbFail(d, "set_name", err)
		}
		return redirect(rw, "/account/settings?done=name")

	case "email":
		email := accounts.NormalizeEmail(r.PostFormValue("email"))
		d.Email = r.PostFormValue("email")
		if !accounts.ValidEmail(email) {
			d.Errs["email"] = d.T["err.email"]
			return again(http.StatusUnprocessableEntity)
		}
		if ok, limited := w.checkOwnPassword(ctx, u.a, r.PostFormValue("password")); !ok {
			d.Errs["email_pw"] = d.T[map[bool]string{true: "limit.p1", false: "err.pw.current"}[limited]]
			return again(http.StatusUnprocessableEntity)
		}
		if email == u.a.Email {
			return redirect(rw, "/account/settings")
		}
		if w.s.cfg.Mailer != nil {
			// Proof of the new mailbox first; same answer whether the
			// address is taken or not (checked again when confirmed).
			secret, err := store.NewActionToken(ctx, u.a.ID, accounts.ActionEmail, email, emailTTL)
			if err != nil {
				return w.dbFail(d, "email_token", err)
			}
			w.sendMail(email, d.Lang, "mail.email.subject", "mail.email.body", w.s.cfg.PublicURL, w.s.cfg.PublicURL+"/confirm-email?token="+url.QueryEscape(secret))
			w.sendMail(u.a.Email, d.Lang, "mail.emailold.subject", "mail.emailold.body", w.s.cfg.PublicURL)
			return redirect(rw, "/account/settings?done=email_sent")
		}
		if err := store.SetEmail(ctx, u.a.ID, email, false); errors.Is(err, accounts.ErrExists) {
			d.Errs["email"] = d.T["err.email.taken"]
			return again(http.StatusUnprocessableEntity)
		} else if err != nil {
			return w.dbFail(d, "set_email", err)
		}
		w.accountEvent("email_change", "ok_unverified", u.a.ID, r)
		return redirect(rw, "/account/settings?done=email")

	case "password":
		cur, pw, pw2 := r.PostFormValue("current"), r.PostFormValue("password"), r.PostFormValue("confirm")
		if ok, limited := w.checkOwnPassword(ctx, u.a, cur); !ok {
			d.Errs["current"] = d.T[map[bool]string{true: "limit.p1", false: "err.pw.current"}[limited]]
			return again(http.StatusUnprocessableEntity)
		}
		if key := pwPolicyErr(pw, u.a.Email); key != "" {
			d.Errs["password"] = d.T[key]
			return again(http.StatusUnprocessableEntity)
		}
		if pw != pw2 {
			d.Errs["confirm"] = d.T["err.pw.match"]
			return again(http.StatusUnprocessableEntity)
		}
		if err := store.SetPassword(ctx, u.a.ID, accounts.HashPassword(pw)); err != nil {
			return w.dbFail(d, "set_password", err)
		}
		// Other browsers are signed out; Desktop logins only on request.
		if _, err := store.DeleteWebSessions(ctx, u.a.ID, u.ws.Hash); err != nil {
			return w.dbFail(d, "web_sessions", err)
		}
		if r.PostFormValue("revoke_tokens") == "on" {
			if _, err := store.RevokeTokens(ctx, u.a.ID); err != nil {
				return w.dbFail(d, "revoke_tokens", err)
			}
		}
		w.accountEvent("password_change", "ok", u.a.ID, r)
		return redirect(rw, "/account/settings?done=password")
	}
	return again(http.StatusBadRequest)
}

// checkOwnPassword re-checks the password of the signed-in account before a
// sensitive change; failures count towards its sign-in limit.
func (w *web) checkOwnPassword(ctx context.Context, a accounts.Account, pw string) (ok, limited bool) {
	now := time.Now()
	key := acctKey(a.ID)
	if w.loginFails.count(key, now) >= w.s.cfg.Limits.LoginFailsPerEmail {
		return false, true
	}
	_, hash, err := w.s.cfg.Accounts.ByEmail(ctx, a.Email)
	if err != nil || pw == "" || len(pw) > accounts.MaxPasswordLen || !accounts.CheckPassword(hash, pw) {
		w.loginFails.add(key, now)
		return false, false
	}
	return true, false
}

// ---- security: TOTP, Desktop logins, web sessions, deletion ----

type securityData struct {
	TOTP         bool
	Need2FA      bool // an admin must enrol before the admin pages
	TOTPRequired bool // admins cannot remove it
	Tokens       []accounts.TokenInfo
	Sessions     []webSessView
	OnlyAdmin    bool
}

type webSessView struct {
	accounts.WebSession
	Current bool
}

func (w *web) securityPage(ctx context.Context, rw http.ResponseWriter, r *http.Request, d *pageData, u *user, status int) (string, int) {
	store := w.s.cfg.Accounts
	sd := &securityData{TOTP: u.a.TOTP, Need2FA: r.URL.Query().Get("need2fa") == "1" && u.a.IsAdmin() && !u.a.TOTP,
		TOTPRequired: u.a.IsAdmin() && !w.s.cfg.AdminTOTPOptional}
	var err error
	if sd.Tokens, err = store.Tokens(ctx, u.a.ID); err != nil {
		return w.dbFail(d, "tokens", err)
	}
	ws, err := store.WebSessions(ctx, u.a.ID)
	if err != nil {
		return w.dbFail(d, "web_sessions", err)
	}
	for _, s := range ws {
		sd.Sessions = append(sd.Sessions, webSessView{s, s.Hash == u.ws.Hash})
	}
	d.Data = sd
	w.fillForm(rw, r, d, false)
	return "acct_security", status
}

func (w *web) acctSecurity(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	u, tmpl, st := w.signedIn(rw, r, d)
	if u == nil {
		return tmpl, st
	}
	d.Nav, d.Wide = "security", true
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.Method != http.MethodPost {
		return w.securityPage(ctx, rw, r, d, u, http.StatusOK)
	}
	if !w.parsePost(rw, r) {
		d.FormErr = d.T["err.form"]
		return w.securityPage(ctx, rw, r, d, u, http.StatusForbidden)
	}
	store := w.s.cfg.Accounts
	switch r.PostFormValue("op") {
	case "revoke_token":
		id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
		if err := store.RevokeTokenID(ctx, u.a.ID, id); err != nil && !errors.Is(err, accounts.ErrNotFound) {
			return w.dbFail(d, "revoke_token", err)
		}
		return redirect(rw, "/account/security?done=token")

	case "logout_all":
		if _, err := store.RevokeTokens(ctx, u.a.ID); err != nil {
			return w.dbFail(d, "revoke_tokens", err)
		}
		if _, err := store.DeleteWebSessions(ctx, u.a.ID, ""); err != nil {
			return w.dbFail(d, "web_sessions", err)
		}
		w.accountEvent("logout_all", "ok", u.a.ID, r)
		w.clearSessionCookie(rw, r)
		return redirect(rw, "/login?out=1")

	case "tfa_disable":
		if u.a.IsAdmin() && !w.s.cfg.AdminTOTPOptional {
			d.Errs["tfa"] = d.T["tfa.required"]
			return w.securityPage(ctx, rw, r, d, u, http.StatusForbidden)
		}
		if ok, limited := w.checkOwnPassword(ctx, u.a, r.PostFormValue("password")); !ok {
			d.Errs["tfa"] = d.T[map[bool]string{true: "limit.p1", false: "err.pw.current"}[limited]]
			return w.securityPage(ctx, rw, r, d, u, http.StatusUnprocessableEntity)
		}
		if ok, _ := w.checkTOTP(ctx, u.a, r.PostFormValue("code")); !ok {
			d.Errs["tfa"] = d.T["totp.bad"]
			return w.securityPage(ctx, rw, r, d, u, http.StatusUnprocessableEntity)
		}
		if err := store.SetTOTP(ctx, u.a.ID, nil); err != nil {
			return w.dbFail(d, "set_totp", err)
		}
		w.accountEvent("totp_disable", "ok", u.a.ID, r)
		return redirect(rw, "/account/security?done=tfa_off")

	case "delete":
		if ok, limited := w.checkOwnPassword(ctx, u.a, r.PostFormValue("password")); !ok {
			d.Errs["delete"] = d.T[map[bool]string{true: "limit.p1", false: "err.pw.current"}[limited]]
			return w.securityPage(ctx, rw, r, d, u, http.StatusUnprocessableEntity)
		}
		if accounts.NormalizeEmail(r.PostFormValue("confirm")) != u.a.Email {
			d.Errs["delete"] = d.T["del.mismatch"]
			return w.securityPage(ctx, rw, r, d, u, http.StatusUnprocessableEntity)
		}
		if err := store.DeleteID(ctx, u.a.ID); errors.Is(err, accounts.ErrLastAdmin) {
			d.Errs["delete"] = d.T["err.lastadmin"]
			return w.securityPage(ctx, rw, r, d, u, http.StatusConflict)
		} else if err != nil {
			return w.dbFail(d, "delete", err)
		}
		w.s.hub.terminateAccount(u.a.ID)
		w.accountEvent("delete_self", "ok", u.a.ID, r)
		w.clearSessionCookie(rw, r)
		d.User, d.IsAdmin = nil, false
		return message(d, "del.done.title", "del.done.p1"), http.StatusOK
	}
	return w.securityPage(ctx, rw, r, d, u, http.StatusBadRequest)
}

// ---- TOTP enrolment ----

type totpSetupData struct {
	QR      template.HTML
	Secret  string // grouped by 4 for typing
	Pending string // the secret sealed for this account (hidden field)
}

func (w *web) acctTOTPSetup(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	u, tmpl, st := w.signedIn(rw, r, d)
	if u == nil {
		return tmpl, st
	}
	d.Nav = "security"
	if u.a.TOTP {
		return redirect(rw, "/account/security")
	}
	ad := []byte("totp-setup|" + acctKey(u.a.ID))
	render := func(secret string, status int) (string, int) {
		uri := accounts.TOTPURI(w.s.cfg.Name, u.a.Email, secret)
		qrc, err := qrSVG(uri)
		if err != nil {
			return w.dbFail(d, "qr", err)
		}
		var groups []string
		for i := 0; i < len(secret); i += 4 {
			groups = append(groups, secret[i:min(i+4, len(secret))])
		}
		sealed := w.setupSeal.Seal([]byte(secret), ad)
		d.Data = &totpSetupData{QR: qrc, Secret: strings.Join(groups, " "), Pending: base64.RawURLEncoding.EncodeToString(sealed)}
		w.fillForm(rw, r, d, false)
		return "acct_2fa", status
	}
	if r.Method != http.MethodPost {
		return render(accounts.NewTOTPSecret(), http.StatusOK)
	}
	if !w.parsePost(rw, r) {
		d.FormErr = d.T["err.form"]
		return render(accounts.NewTOTPSecret(), http.StatusForbidden)
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.PostFormValue("pending"))
	var secret []byte
	if err == nil {
		secret, err = w.setupSeal.Open(raw, ad)
	}
	if err != nil {
		d.FormErr = d.T["err.form"]
		return render(accounts.NewTOTPSecret(), http.StatusBadRequest)
	}
	key, err := accounts.ParseTOTPSecret(string(secret))
	if err != nil {
		d.FormErr = d.T["err.form"]
		return render(accounts.NewTOTPSecret(), http.StatusBadRequest)
	}
	step := accounts.CheckTOTP(key, r.PostFormValue("code"), time.Now())
	if step < 0 {
		d.Errs["code"] = d.T["totp.bad"]
		return render(string(secret), http.StatusUnprocessableEntity)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store := w.s.cfg.Accounts
	if err := store.SetTOTP(ctx, u.a.ID, w.totpSeal.Seal(key, []byte(acctKey(u.a.ID)))); err != nil {
		return w.dbFail(d, "set_totp", err)
	}
	_, _ = store.UseTOTPStep(ctx, u.a.ID, step)
	if err := store.SetWebSessionMFA(ctx, u.ws.Hash); err != nil {
		return w.dbFail(d, "web_session", err)
	}
	// Other browsers signed in with the password only: they sign in again.
	_, _ = store.DeleteWebSessions(ctx, u.a.ID, u.ws.Hash)
	w.accountEvent("totp_enable", "ok", u.a.ID, r)
	if u.a.IsAdmin() {
		return redirect(rw, "/admin?done=tfa_on")
	}
	return redirect(rw, "/account/security?done=tfa_on")
}
