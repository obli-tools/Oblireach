package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oblireach/rdv/internal/accounts"
)

// Web sign-in for the account pages (/login, /account, /admin). It shares
// the accounts of the Desktop API but not its tokens: a browser holds a
// server-side session (hashed in the database) through an HttpOnly,
// SameSite=Strict cookie, "__Host-" prefixed over HTTPS.

const (
	sessCookie     = "rdv_session"
	sessCookieHost = "__Host-rdv_session"
	ticketTTL      = 5 * time.Minute // password checked -> TOTP code entered
	resetTTL       = time.Hour
	emailTTL       = 24 * time.Hour
)

// deriveKey derives a purpose key from the web secret.
func deriveKey(secret []byte, purpose string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("oblireach-rdv web v1|" + purpose))
	return m.Sum(nil)
}

func (w *web) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/login", w.page(w.loginPage))
	mux.HandleFunc("/login/verify", w.page(w.stepUpPage))
	mux.HandleFunc("/logout", w.page(w.logoutPage))
	mux.HandleFunc("/forgot", w.page(w.forgotPage))
	mux.HandleFunc("/reset", w.page(w.resetPage))
	mux.HandleFunc("/confirm-email", w.page(w.confirmEmailPage))
	w.accountRoutes(mux)
	w.adminRoutes(mux)
}

func (w *web) sessName(r *http.Request) string {
	if w.secure(r) {
		return sessCookieHost
	}
	return sessCookie
}

func (w *web) setSessionCookie(rw http.ResponseWriter, r *http.Request, secret string) {
	http.SetCookie(rw, &http.Cookie{Name: w.sessName(r), Value: secret, Path: "/", HttpOnly: true, Secure: w.secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(accounts.WebAbsoluteTTL.Seconds())})
}

func (w *web) clearSessionCookie(rw http.ResponseWriter, r *http.Request) {
	http.SetCookie(rw, &http.Cookie{Name: w.sessName(r), Value: "", Path: "/", HttpOnly: true, Secure: w.secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// user is the signed-in account of a request.
type user struct {
	ws accounts.WebSession
	a  accounts.Account
}

// currentUser resolves the session cookie (nil when signed out).
func (w *web) currentUser(r *http.Request) *user {
	st := w.s.cfg.Accounts
	if st == nil {
		return nil
	}
	c, err := r.Cookie(w.sessName(r))
	if err != nil || c.Value == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ws, a, err := st.ResolveWebSession(ctx, c.Value)
	if err != nil {
		if !errors.Is(err, accounts.ErrNotFound) {
			w.s.log.warn("account_db_error", "op", "web_session", "err", err.Error())
		}
		return nil
	}
	if w.s.cfg.Mailer != nil && !a.Verified {
		return nil
	}
	return &user{ws: ws, a: a}
}

// adminReady reports whether an admin may use the admin pages now (TOTP
// enrolled and checked for this session, unless TOTP is optional).
func (w *web) adminReady(u *user) bool {
	return u.a.IsAdmin() && (w.s.cfg.AdminTOTPOptional || (u.a.TOTP && u.ws.MFA))
}

// homeOf is where a signed-in account lands.
func (w *web) homeOf(a accounts.Account) string {
	if a.IsAdmin() {
		return "/admin"
	}
	return "/account"
}

// safeNext keeps a post-login destination only when it is one of our
// signed-in pages (no open redirect).
func safeNext(next string) string {
	if next == "" || strings.ContainsAny(next, "\\\r\n") || strings.HasPrefix(next, "//") {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	if u.Path == "/account" || strings.HasPrefix(u.Path, "/account/") || u.Path == "/admin" || strings.HasPrefix(u.Path, "/admin/") {
		return next
	}
	return ""
}

// signedIn fills the page data of a signed-in page; nil (with the answer
// to return) when the visitor is not signed in.
func (w *web) signedIn(rw http.ResponseWriter, r *http.Request, d *pageData) (*user, string, int) {
	if w.s.cfg.Accounts == nil {
		tmpl := message(d, "notfound.title", "disabled.p1")
		return nil, tmpl, http.StatusNotFound
	}
	u := w.currentUser(r)
	if u == nil {
		if r.Method == http.MethodPost {
			w.fillForm(rw, r, d, false)
			d.FormErr = d.T["login.expired"]
			return nil, "login", http.StatusUnauthorized
		}
		tmpl, st := redirect(rw, "/login?next="+url.QueryEscape(r.URL.RequestURI()))
		return nil, tmpl, st
	}
	d.User, d.IsAdmin = &u.a, w.adminReady(u)
	switch r.URL.Query().Get("done") {
	case "":
	default:
		if f := d.T["done."+r.URL.Query().Get("done")]; f != "" {
			d.Flash = f
		}
	}
	return u, "", 0
}

// adminOnly is signedIn for the admin pages: 403 for other accounts, the
// TOTP enrolment (or check) first when it is required.
func (w *web) adminOnly(rw http.ResponseWriter, r *http.Request, d *pageData) (*user, string, int) {
	u, tmpl, st := w.signedIn(rw, r, d)
	if u == nil {
		return nil, tmpl, st
	}
	d.Wide = true
	if !u.a.IsAdmin() {
		d.Wide = false
		return nil, message(d, "forbidden.title", "forbidden.p1"), http.StatusForbidden
	}
	if !w.adminReady(u) {
		if !u.a.TOTP {
			tmpl, st := redirect(rw, "/account/security?need2fa=1")
			return nil, tmpl, st
		}
		tmpl, st := redirect(rw, "/login/verify?next="+url.QueryEscape(r.URL.RequestURI()))
		return nil, tmpl, st
	}
	return u, "", 0
}

// parsePost reads a posted form and checks its CSRF token and stamp.
func (w *web) parsePost(rw http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(rw, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return false
	}
	ok, _ := w.checkForm(r)
	return ok
}

// ---- credentials ----

type credResult int

const (
	credOK credResult = iota
	credBad
	credUnverified
	credLimited
	credError
	credReview // right password, account disabled pending the review of reports
)

// checkCredentials checks an email and password for a sign-in (web or
// Desktop): per-address failure limit, constant work for unknown
// addresses, the same answer for a wrong password and a disabled account.
// Attempts on existing accounts go to their login history.
func (w *web) checkCredentials(ctx context.Context, r *http.Request, email, pw, event, kind string) (accounts.Account, string, credResult) {
	st := w.s.cfg.Accounts
	now := time.Now()
	ip := w.s.log.ip(w.ip(r))
	if n := w.loginFails.count(email, now); n >= w.s.cfg.Limits.LoginFailsPerEmail {
		w.accountEvent(event, "rate_limited", 0, r)
		return accounts.Account{}, "", credLimited
	}
	fail := func(a accounts.Account, reason string) (accounts.Account, string, credResult) {
		w.loginFails.add(email, now)
		w.accountEvent(event, "fail", a.ID, r)
		if a.ID != 0 {
			w.loginRecord(ctx, a.ID, kind, false, reason, ip)
		}
		return accounts.Account{}, "", credBad
	}
	if !accounts.ValidEmail(email) || pw == "" || len(pw) > accounts.MaxPasswordLen {
		accounts.BurnPasswordCheck(pw)
		return fail(accounts.Account{}, "")
	}
	a, hash, err := st.ByEmail(ctx, email)
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		if w.s.cfg.Mailer != nil {
			hashes, err := st.PendingHashes(ctx, email)
			if err != nil {
				w.s.log.warn("account_db_error", "op", "pending", "err", err.Error())
				return accounts.Account{}, "", credError
			}
			for _, h := range hashes {
				if accounts.CheckPassword(h, pw) {
					w.accountEvent(event, "not_verified", 0, r)
					return accounts.Account{}, "", credUnverified
				}
			}
			if len(hashes) > 0 {
				return fail(accounts.Account{}, "")
			}
		}
		accounts.BurnPasswordCheck(pw)
		return fail(accounts.Account{}, "")
	case err != nil:
		w.s.log.warn("account_db_error", "op", "by_email", "err", err.Error())
		return accounts.Account{}, "", credError
	}
	if !accounts.CheckPassword(hash, pw) {
		return fail(a, "bad_password")
	}
	if a.Disabled {
		if !a.Review.IsZero() && !a.Banned {
			// Disabled by the report protection: the owner (the password
			// is right) is told the account is under review.
			w.loginFails.add(email, now)
			w.accountEvent(event, "under_review", a.ID, r)
			w.loginRecord(ctx, a.ID, kind, false, "under_review", ip)
			return accounts.Account{}, "", credReview
		}
		return fail(a, "disabled")
	}
	if w.s.cfg.Mailer != nil && !a.Verified {
		w.accountEvent(event, "not_verified", a.ID, r)
		w.loginRecord(ctx, a.ID, kind, false, "not_verified", ip)
		return accounts.Account{}, "", credUnverified
	}
	return a, hash, credOK
}

func (w *web) loginRecord(ctx context.Context, id int64, kind string, ok bool, reason, ip string) {
	if err := w.s.cfg.Accounts.AddLogin(ctx, id, kind, ok, reason, ip); err != nil {
		w.s.log.warn("account_db_error", "op", "login_history", "err", err.Error())
	}
}

// loginOK records a successful sign-in and applies RDV_ADMIN_EMAILS.
func (w *web) loginOK(ctx context.Context, r *http.Request, a accounts.Account, kind string) {
	ip := w.s.log.ip(w.ip(r))
	w.loginFails.reset(a.Email)
	w.loginFails.reset(acctKey(a.ID))
	w.loginRecord(ctx, a.ID, kind, true, "", ip)
	if kind == accounts.LoginWeb {
		if err := w.s.cfg.Accounts.TouchLogin(ctx, a.ID, ip); err != nil {
			w.s.log.warn("account_db_error", "op", "touch_login", "err", err.Error())
		}
	}
	if slices.Contains(w.s.cfg.AdminEmails, a.Email) && !a.IsAdmin() {
		if _, err := w.s.cfg.Accounts.EnsureAdmins(ctx, []string{a.Email}); err != nil {
			w.s.log.warn("account_db_error", "op", "ensure_admins", "err", err.Error())
		}
	}
}

func acctKey(id int64) string { return "acct:" + strconv.FormatInt(id, 10) }

// ---- sign-in tickets (password checked, TOTP code expected) ----

func (w *web) ticketMAC(id, exp int64, pwHash string) string {
	m := hmac.New(sha256.New, w.ticketKey)
	m.Write([]byte(strconv.FormatInt(id, 10) + "|" + strconv.FormatInt(exp, 10) + "|"))
	sum := sha256.Sum256([]byte(pwHash)) // a password change voids the ticket
	m.Write(sum[:])
	return hex.EncodeToString(m.Sum(nil))
}

func (w *web) newTicket(a accounts.Account, pwHash string) string {
	exp := time.Now().Add(ticketTTL).Unix()
	return strconv.FormatInt(a.ID, 10) + "." + strconv.FormatInt(exp, 10) + "." + w.ticketMAC(a.ID, exp, pwHash)
}

// checkTicket returns the account of a valid ticket.
func (w *web) checkTicket(ctx context.Context, t string) (accounts.Account, bool) {
	parts := strings.Split(t, ".")
	if len(parts) != 3 || len(t) > 200 {
		return accounts.Account{}, false
	}
	id, err1 := strconv.ParseInt(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || time.Now().Unix() > exp {
		return accounts.Account{}, false
	}
	a, err := w.s.cfg.Accounts.ByID(ctx, id)
	if err != nil {
		return accounts.Account{}, false
	}
	_, hash, err := w.s.cfg.Accounts.ByEmail(ctx, a.Email)
	if err != nil || !hmac.Equal([]byte(parts[2]), []byte(w.ticketMAC(id, exp, hash))) || a.Disabled || !a.TOTP {
		return accounts.Account{}, false
	}
	return a, true
}

// checkTOTP checks a code against the enrolled secret of an account; a code
// works once, and failures count towards the account's sign-in limit.
func (w *web) checkTOTP(ctx context.Context, a accounts.Account, code string) (ok, limited bool) {
	now := time.Now()
	key := acctKey(a.ID)
	if w.loginFails.count(key, now) >= w.s.cfg.Limits.LoginFailsPerEmail {
		return false, true
	}
	sealed, err := w.s.cfg.Accounts.TOTPSecret(ctx, a.ID)
	if err != nil || sealed == nil {
		return false, false
	}
	secret, err := w.totpSeal.Open(sealed, []byte(key))
	if err != nil {
		w.s.log.warn("totp_unreadable", "account", key) // web secret changed: reset with the CLI
		return false, false
	}
	step := accounts.CheckTOTP(secret, code, now)
	if step >= 0 {
		if fresh, err := w.s.cfg.Accounts.UseTOTPStep(ctx, a.ID, step); err == nil && fresh {
			return true, false
		}
	}
	w.loginFails.add(key, now)
	return false, false
}

// ---- pages ----

func (w *web) loginPage(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	st := w.s.cfg.Accounts
	if st == nil {
		return message(d, "notfound.title", "disabled.p1"), http.StatusNotFound
	}
	d.Next = safeNext(r.FormValue("next"))
	if r.Method != http.MethodPost {
		if u := w.currentUser(r); u != nil {
			if d.Next != "" {
				return redirect(rw, d.Next)
			}
			return redirect(rw, w.homeOf(u.a))
		}
		if r.URL.Query().Get("out") == "1" {
			d.Flash = d.T["login.out"]
		}
		w.fillForm(rw, r, d, false)
		return "login", http.StatusOK
	}
	if ok, _ := w.login.take(w.ip(r), time.Now()); !ok {
		w.accountEvent("web_login", "rate_limited", 0, r)
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "login", http.StatusBadRequest
	}
	d.Next = safeNext(r.PostFormValue("next"))
	ok, tooFast := w.checkForm(r)
	if !ok {
		w.accountEvent("web_login", "csrf", 0, r)
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "login", http.StatusForbidden
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if r.PostFormValue("step") == "totp" {
		return w.loginTOTP(rw, r, d, ctx)
	}
	d.Email = r.PostFormValue("email")
	if tooFast || r.PostFormValue("website") != "" {
		w.accountEvent("web_login", "bot", 0, r)
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["login.bad"]
		return "login", http.StatusUnauthorized
	}
	email := accounts.NormalizeEmail(d.Email)
	a, hash, res := w.checkCredentials(ctx, r, email, r.PostFormValue("password"), "web_login", accounts.LoginWeb)
	switch res {
	case credLimited:
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	case credError:
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.server"]
		return "login", http.StatusServiceUnavailable
	case credUnverified:
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["login.unverified"]
		return "login", http.StatusForbidden
	case credBad:
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["login.bad"]
		return "login", http.StatusUnauthorized
	case credReview:
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["login.review"]
		return "login", http.StatusForbidden
	}
	if a.TOTP {
		d.Ticket = w.newTicket(a, hash)
		w.fillForm(rw, r, d, false)
		return "totp", http.StatusOK
	}
	return w.startSession(rw, r, d, ctx, a, false)
}

func (w *web) loginTOTP(rw http.ResponseWriter, r *http.Request, d *pageData, ctx context.Context) (string, int) {
	a, ok := w.checkTicket(ctx, r.PostFormValue("ticket"))
	if !ok {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["login.expired"]
		return "login", http.StatusUnauthorized
	}
	good, limited := w.checkTOTP(ctx, a, r.PostFormValue("code"))
	if limited {
		w.accountEvent("web_login", "rate_limited", a.ID, r)
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	}
	if !good {
		w.accountEvent("web_login", "bad_totp", a.ID, r)
		w.loginRecord(ctx, a.ID, accounts.LoginWeb, false, "bad_totp", w.s.log.ip(w.ip(r)))
		d.Ticket = r.PostFormValue("ticket")
		w.fillForm(rw, r, d, false)
		d.Errs["code"] = d.T["totp.bad"]
		return "totp", http.StatusUnauthorized
	}
	return w.startSession(rw, r, d, ctx, a, true)
}

// startSession opens a web session after a complete sign-in.
func (w *web) startSession(rw http.ResponseWriter, r *http.Request, d *pageData, ctx context.Context, a accounts.Account, mfa bool) (string, int) {
	secret, err := w.s.cfg.Accounts.NewWebSession(ctx, a.ID, w.s.log.ip(w.ip(r)), mfa)
	if err != nil {
		w.s.log.warn("account_db_error", "op", "web_session", "err", err.Error())
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.server"]
		return "login", http.StatusServiceUnavailable
	}
	w.loginOK(ctx, r, a, accounts.LoginWeb)
	w.accountEvent("web_login", "ok", a.ID, r)
	w.setSessionCookie(rw, r, secret)
	w.csrfToken(rw, r, true) // a new CSRF token for the signed-in browser
	if a2, err := w.s.cfg.Accounts.ByID(ctx, a.ID); err == nil {
		a = a2 // role possibly granted by RDV_ADMIN_EMAILS
	}
	if a.IsAdmin() && !w.s.cfg.AdminTOTPOptional && !a.TOTP {
		return redirect(rw, "/account/security?need2fa=1")
	}
	if d.Next != "" && (a.IsAdmin() || !strings.HasPrefix(d.Next, "/admin")) {
		return redirect(rw, d.Next)
	}
	return redirect(rw, w.homeOf(a))
}

// stepUpPage asks the TOTP code of a session that did not check it (TOTP
// enrolled after its sign-in elsewhere) before the admin pages.
func (w *web) stepUpPage(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	u, tmpl, st := w.signedIn(rw, r, d)
	if u == nil {
		return tmpl, st
	}
	d.Next = safeNext(r.FormValue("next"))
	if !u.a.TOTP || u.ws.MFA {
		if d.Next != "" {
			return redirect(rw, d.Next)
		}
		return redirect(rw, w.homeOf(u.a))
	}
	if r.Method != http.MethodPost {
		w.fillForm(rw, r, d, false)
		return "totp", http.StatusOK
	}
	if !w.parsePost(rw, r) {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "totp", http.StatusForbidden
	}
	d.Next = safeNext(r.PostFormValue("next"))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	good, limited := w.checkTOTP(ctx, u.a, r.PostFormValue("code"))
	if limited {
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	}
	if !good {
		w.fillForm(rw, r, d, false)
		d.Errs["code"] = d.T["totp.bad"]
		return "totp", http.StatusUnauthorized
	}
	if err := w.s.cfg.Accounts.SetWebSessionMFA(ctx, u.ws.Hash); err != nil {
		return message(d, "error.title", "err.server"), http.StatusServiceUnavailable
	}
	if d.Next != "" {
		return redirect(rw, d.Next)
	}
	return redirect(rw, w.homeOf(u.a))
}

func (w *web) logoutPage(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	if r.Method != http.MethodPost {
		return redirect(rw, "/login")
	}
	if !w.parsePost(rw, r) {
		return message(d, "error.title", "err.form"), http.StatusForbidden
	}
	if u := w.currentUser(r); u != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_ = w.s.cfg.Accounts.DeleteWebSession(ctx, u.ws.Hash)
		w.accountEvent("web_logout", "ok", u.a.ID, r)
	}
	w.clearSessionCookie(rw, r)
	return redirect(rw, "/login?out=1")
}

// forgotPage sends a reset link when SMTP is configured; the answer is the
// same whether the address has an account or not.
func (w *web) forgotPage(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	st := w.s.cfg.Accounts
	if st == nil {
		return message(d, "notfound.title", "disabled.p1"), http.StatusNotFound
	}
	if r.Method != http.MethodPost {
		w.fillForm(rw, r, d, false)
		return "forgot", http.StatusOK
	}
	if w.s.cfg.Mailer == nil {
		return "forgot", http.StatusOK
	}
	if ok, _ := w.forgot.take(w.ip(r), time.Now()); !ok {
		w.accountEvent("forgot", "rate_limited", 0, r)
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "forgot", http.StatusBadRequest
	}
	ok, tooFast := w.checkForm(r)
	if !ok {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "forgot", http.StatusForbidden
	}
	email := accounts.NormalizeEmail(r.PostFormValue("email"))
	if tooFast || r.PostFormValue("website") != "" || !accounts.ValidEmail(email) {
		w.accountEvent("forgot", "ignored", 0, r)
		return message(d, "forgot.sent.title", "forgot.sent.p1"), http.StatusOK
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	a, _, err := st.ByEmail(ctx, email)
	switch {
	case err == nil && !a.Disabled:
		secret, err := st.NewActionToken(ctx, a.ID, accounts.ActionReset, "", resetTTL)
		if err != nil {
			w.s.log.warn("account_db_error", "op", "reset_token", "err", err.Error())
			return message(d, "error.title", "err.server"), http.StatusServiceUnavailable
		}
		w.accountEvent("forgot", "sent", a.ID, r)
		w.sendMail(email, d.Lang, "mail.reset.subject", "mail.reset.body", w.s.cfg.PublicURL, w.s.cfg.PublicURL+"/reset?token="+url.QueryEscape(secret))
	case err == nil || errors.Is(err, accounts.ErrNotFound):
		w.accountEvent("forgot", "unknown", 0, r)
	default:
		w.s.log.warn("account_db_error", "op", "by_email", "err", err.Error())
	}
	return message(d, "forgot.sent.title", "forgot.sent.p1"), http.StatusOK
}

// resetPage sets a new password from a single-use link (forgot password,
// admin reset, first admin set-up). Every sign-in of the account ends.
func (w *web) resetPage(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	st := w.s.cfg.Accounts
	if st == nil {
		return message(d, "notfound.title", "disabled.p1"), http.StatusNotFound
	}
	if ok, _ := w.verify.take(w.ip(r), time.Now()); !ok {
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.Method != http.MethodPost {
		token := r.URL.Query().Get("token")
		if _, _, err := st.ActionToken(ctx, token, accounts.ActionReset); err != nil {
			return message(d, "reset.bad.title", "reset.bad.p1"), http.StatusNotFound
		}
		d.Token = token
		w.fillForm(rw, r, d, false)
		return "reset", http.StatusOK
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return message(d, "reset.bad.title", "reset.bad.p1"), http.StatusBadRequest
	}
	d.Token = r.PostFormValue("token")
	id, _, err := st.ActionToken(ctx, d.Token, accounts.ActionReset)
	if err != nil {
		return message(d, "reset.bad.title", "reset.bad.p1"), http.StatusNotFound
	}
	if ok, _ := w.checkForm(r); !ok {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "reset", http.StatusForbidden
	}
	a, err := st.ByID(ctx, id)
	if err != nil {
		return message(d, "reset.bad.title", "reset.bad.p1"), http.StatusNotFound
	}
	pw := r.PostFormValue("password")
	if key := pwPolicyErr(pw, a.Email); key != "" {
		d.Errs["password"] = d.T[key]
	} else if pw != r.PostFormValue("confirm") {
		d.Errs["confirm"] = d.T["err.pw.match"]
	}
	if len(d.Errs) > 0 {
		w.fillForm(rw, r, d, false)
		return "reset", http.StatusUnprocessableEntity
	}
	hash := accounts.HashPassword(pw)
	if _, _, err := st.ConsumeActionToken(ctx, d.Token, accounts.ActionReset); err != nil {
		return message(d, "reset.bad.title", "reset.bad.p1"), http.StatusNotFound
	}
	if err := st.SetPassword(ctx, a.ID, hash); err != nil {
		w.s.log.warn("account_db_error", "op", "set_password", "err", err.Error())
		return message(d, "error.title", "err.server"), http.StatusServiceUnavailable
	}
	_, _ = st.DeleteWebSessions(ctx, a.ID, "")
	_, _ = st.RevokeTokens(ctx, a.ID)
	w.loginFails.reset(a.Email)
	w.accountEvent("reset", "ok", a.ID, r)
	w.clearSessionCookie(rw, r)
	d.Link, d.LinkText = "/login", d.T["login.submit"]
	return message(d, "reset.ok.title", "reset.ok.p1"), http.StatusOK
}

// confirmEmailPage applies an email change from the link sent to the new
// address (GET shows a button: mail scanners open links).
func (w *web) confirmEmailPage(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	st := w.s.cfg.Accounts
	if st == nil || w.s.cfg.Mailer == nil {
		return message(d, "notfound.title", "disabled.p1"), http.StatusNotFound
	}
	if ok, _ := w.verify.take(w.ip(r), time.Now()); !ok {
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.Method != http.MethodPost {
		token := r.URL.Query().Get("token")
		_, email, err := st.ActionToken(ctx, token, accounts.ActionEmail)
		if err != nil {
			return message(d, "verify.bad.title", "cemail.bad.p1"), http.StatusNotFound
		}
		d.Token, d.Email = token, email
		w.fillForm(rw, r, d, false)
		return "confirmemail", http.StatusOK
	}
	if !w.parsePost(rw, r) {
		return message(d, "error.title", "err.form"), http.StatusForbidden
	}
	id, email, err := st.ConsumeActionToken(ctx, r.PostFormValue("token"), accounts.ActionEmail)
	if err != nil {
		return message(d, "verify.bad.title", "cemail.bad.p1"), http.StatusNotFound
	}
	if err := st.SetEmail(ctx, id, email, true); errors.Is(err, accounts.ErrExists) {
		return message(d, "error.title", "err.email.taken"), http.StatusConflict
	} else if err != nil {
		return message(d, "error.title", "err.server"), http.StatusServiceUnavailable
	}
	_, _ = st.DeleteWebSessions(ctx, id, "") // signed in under the old address
	w.accountEvent("email_change", "ok", id, r)
	d.Link, d.LinkText = "/login", d.T["login.submit"]
	return message(d, "cemail.ok.title", "cemail.ok.p1"), http.StatusOK
}

// pwPolicyErr returns the text key of a password policy failure ("" if ok).
func pwPolicyErr(pw, email string) string {
	switch err := accounts.CheckPolicy(pw, email); {
	case err == nil:
		return ""
	case errors.Is(err, accounts.ErrPasswordShort):
		return "err.pw.short"
	case errors.Is(err, accounts.ErrPasswordLong):
		return "err.pw.long"
	default:
		return "err.pw.common"
	}
}

// audit appends an admin action to the audit log.
func (w *web) audit(ctx context.Context, r *http.Request, actor accounts.Account, action string, target accounts.Account, detail string) {
	w.s.m.inc(w.s.m.admin, action)
	e := accounts.AuditEntry{ActorID: actor.ID, Actor: actor.Email, Action: action, TargetID: target.ID, Target: target.Email,
		Detail: detail, IP: w.s.log.ip(w.ip(r))}
	if err := w.s.cfg.Accounts.Audit(ctx, e); err != nil {
		w.s.log.warn("account_db_error", "op", "audit", "err", err.Error())
	}
	w.s.log.event("admin_action", "action", action, "actor", acctKey(actor.ID), "target", acctKey(target.ID))
}
