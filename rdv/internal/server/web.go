package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

// web serves the account pages (server-rendered HTML, no external asset, a
// strict CSP, no script at all) and the JSON API used by Oblireach Desktop.
type web struct {
	s         *Server
	formKey   []byte
	ticketKey []byte           // signs the sign-in tickets between password and TOTP
	totpSeal  *accounts.Sealer // TOTP secrets at rest
	setupSeal *accounts.Sealer // TOTP secrets during enrolment (hidden field)
	pages     map[string]*template.Template
	csp       string

	login, signup, verify, api, mails, forgot *syncBuckets
	loginFails                                *syncFails

	reports   *syncBuckets // POST /v1/report per client IP
	reportKey []byte       // keys the reporter pseudonyms (distinct reporters)
}

const (
	formMaxAge     = 2 * time.Hour
	maxFormBytes   = 16 << 10
	maxJSONBytes   = 8 << 10
	csrfCookie     = "rdv_csrf"
	csrfCookieHost = "__Host-rdv_csrf" // over HTTPS: bound to this exact origin
)

func newWeb(s *Server) *web {
	l := s.cfg.Limits
	w := &web{
		s:          s,
		formKey:    deriveKey(s.cfg.WebSecret, "form"),
		ticketKey:  deriveKey(s.cfg.WebSecret, "signin-ticket"),
		totpSeal:   accounts.NewSealer(s.cfg.WebSecret, "totp"),
		setupSeal:  accounts.NewSealer(s.cfg.WebSecret, "totp-setup"),
		login:      newSyncBuckets(l.LoginPerMinIP, l.LoginBurstIP),
		signup:     newSyncBuckets(l.SignupPerHourIP/60, l.SignupBurstIP),
		verify:     newSyncBuckets(l.VerifyPerMinIP, l.VerifyBurstIP),
		api:        newSyncBuckets(l.APIPerMinIP, l.APIBurstIP),
		mails:      newSyncBuckets(l.MailsPerHourEmail/60, l.MailsBurstEmail),
		forgot:     newSyncBuckets(l.SignupPerHourIP/60, l.SignupBurstIP),
		loginFails: &syncFails{f: newFailLog(l.LoginFailWindow)},
		reports:    newSyncBuckets(l.ReportPerHourIP/60, l.ReportBurstIP),
		reportKey:  deriveKey(s.cfg.WebSecret, "report-reporter"),
	}
	sum := sha256.Sum256([]byte(pageCSS))
	w.csp = "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; img-src data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	w.pages = parsePages()
	return w
}

func (w *web) routes(mux *http.ServeMux) {
	mux.HandleFunc("/", w.page(w.landing))
	mux.HandleFunc("/terms", w.page(w.simple("terms", "terms.title", "terms.p1", "terms.p2", "terms.p3", "terms.p4")))
	mux.HandleFunc("/login-help", w.page(w.simple("help", "help.title", "help.p1", "help.p2", "help.p3")))
	mux.HandleFunc("/register", w.page(w.register))
	mux.HandleFunc("/verify", w.page(w.verifyPage))
	w.appRoutes(mux)
	mux.HandleFunc("/v1/info", w.apiInfo)
	mux.HandleFunc("/v1/account/login", w.apiLogin)
	mux.HandleFunc("/v1/account/logout", w.apiLogout)
	mux.HandleFunc("/v1/account/me", w.apiMe)
	mux.HandleFunc(wire.ReportPath, w.apiReport)
}

// janitor runs the periodic maintenance of the account side.
func (w *web) janitor(stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	purge := func() {
		if w.s.cfg.Accounts == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if k, err := w.s.cfg.Accounts.EnsureAdmins(ctx, w.s.cfg.AdminEmails); err != nil {
			w.s.log.warn("account_db_error", "op", "ensure_admins", "err", err.Error())
		} else if k > 0 {
			w.s.log.event("admins_promoted", "count", k)
		}
		n, err := w.s.cfg.Accounts.Purge(ctx)
		if err != nil {
			w.s.log.warn("account_db_error", "op", "purge", "err", err.Error())
		} else if n > 0 {
			w.s.log.event("accounts_purged", "inactive_accounts", n)
		}
	}
	purge()
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			for _, b := range []*syncBuckets{w.login, w.signup, w.verify, w.api, w.mails, w.forgot, w.reports} {
				b.gc(now)
			}
			w.loginFails.gc(now)
			if now.Sub(last) >= time.Hour {
				purge()
				last = now
			}
		}
	}
}

// ---- request helpers ----

type pageData struct {
	T          map[string]string
	Lang       string
	Title      string
	Paras      []string
	Accounts   bool
	ServerName string
	Version    string

	// forms
	CSRF, TS string
	Name     string
	Email    string
	Token    string
	Errs     map[string]string
	FormErr  string
	Link     string // button link of message pages
	LinkText string

	// signed-in pages
	User     *accounts.Account
	IsAdmin  bool   // admin navigation
	Nav      string // current tab
	Wide     bool
	Refresh  int // seconds (meta refresh), 0 = none
	Flash    string
	Next     string
	Mail     bool // SMTP configured
	Data     any  // page-specific content
	Code     string
	Ticket   string
	SignedIn bool // a session exists (landing)
}

type pageHandler func(rw http.ResponseWriter, r *http.Request, d *pageData) (tmpl string, status int)

// redirect answers 303 See Other to a local path (page handlers return it).
func redirect(rw http.ResponseWriter, to string) (string, int) {
	rw.Header().Set("Location", to)
	return "", http.StatusSeeOther
}

// page wraps an HTML page: language, security headers, CSRF cookie.
func (w *web) page(h pageHandler) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		lang := pickLang(r)
		d := &pageData{T: webText[lang], Lang: lang, Accounts: w.s.cfg.Accounts != nil,
			ServerName: w.s.cfg.Name, Version: w.s.cfg.Version, Errs: map[string]string{}, Mail: w.s.cfg.Mailer != nil}
		hd := rw.Header()
		hd.Set("Content-Security-Policy", w.csp)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin") // keeps a real Origin on our own POSTs
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cache-Control", "no-store")
		hd.Set("Content-Language", lang)
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			hd.Set("Allow", "GET, HEAD, POST")
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tmpl, status := h(rw, r, d)
		if status == http.StatusSeeOther {
			rw.WriteHeader(status)
			return
		}
		if d.Title == "" {
			d.Title = d.T[pageTitles[tmpl]]
		}
		if d.User != nil && d.CSRF == "" {
			w.fillForm(rw, r, d, false) // the sign-out form of the header
		}
		hd.Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(status)
		if r.Method == http.MethodHead {
			return
		}
		t := w.pages[tmpl]
		if t == nil {
			t = w.pages["message"]
		}
		_ = t.ExecuteTemplate(rw, "layout", d)
	}
}

var pageTitles = map[string]string{"landing": "landing.title", "register": "reg.title", "verify": "verify.title",
	"login": "login.title", "totp": "totp.title", "forgot": "forgot.title", "reset": "reset.title", "confirmemail": "cemail.title",
	"acct_overview": "acct.title", "acct_settings": "settings.title", "acct_security": "security.title", "acct_2fa": "tfa.setup.title",
	"admin_dash": "adm.dash.title", "admin_accounts": "adm.accounts.title", "admin_account": "adm.account.title",
	"admin_live": "adm.live.title", "admin_audit": "adm.audit.title", "admin_history": "adm.history.title", "admin_confirm": "adm.confirm.title",
	"admin_reports": "adm.reports.title", "admin_report": "adm.report.title"}

// message fills a generic message page.
func message(d *pageData, title string, paras ...string) string {
	d.Title = d.T[title]
	for _, p := range paras {
		d.Paras = append(d.Paras, d.T[p])
	}
	return "message"
}

func (w *web) simple(name, title string, paras ...string) pageHandler {
	return func(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
		if r.Method == http.MethodPost {
			return message(d, "notfound.title", "notfound.p1"), http.StatusMethodNotAllowed
		}
		message(d, title, paras...)
		return "message", http.StatusOK
	}
}

func (w *web) landing(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	if r.URL.Path != "/" {
		return message(d, "notfound.title", "notfound.p1"), http.StatusNotFound
	}
	if u := w.currentUser(r); u != nil && r.Method != http.MethodPost {
		return redirect(rw, w.homeOf(u.a))
	}
	return "landing", http.StatusOK
}

func (w *web) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return w.s.cfg.TrustProxyHeaders && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (w *web) cookieName(r *http.Request) string {
	if w.secure(r) {
		return csrfCookieHost
	}
	return csrfCookie
}

// csrfToken returns the double-submit token of the request (cookie), set a
// new one when absent.
func (w *web) csrfToken(rw http.ResponseWriter, r *http.Request, renew bool) string {
	name := w.cookieName(r)
	var v string
	if c, err := r.Cookie(name); err == nil && len(c.Value) == 43 && !renew {
		v = c.Value // set again below: its lifetime restarts with each form served
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		v = base64.RawURLEncoding.EncodeToString(b)
	}
	http.SetCookie(rw, &http.Cookie{Name: name, Value: v, Path: "/", HttpOnly: true, Secure: w.secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(formMaxAge.Seconds())})
	return v
}

// formStamp signs the time a form was served, bound to its CSRF token.
func (w *web) formStamp(csrf string, now time.Time) string {
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	return ts + "." + w.stampMAC(ts, csrf)
}

func (w *web) stampMAC(ts, csrf string) string {
	m := hmac.New(sha256.New, w.formKey)
	m.Write([]byte(ts + "|" + csrf))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// fillForm prepares the hidden fields of a form.
func (w *web) fillForm(rw http.ResponseWriter, r *http.Request, d *pageData, renew bool) {
	d.CSRF = w.csrfToken(rw, r, renew)
	d.TS = w.formStamp(d.CSRF, time.Now())
}

// checkForm validates a posted form: same-origin, CSRF double submit, and
// the signed time stamp. tooFast reports a submission faster than
// FormMinFill (a bot: callers answer as if it worked and do nothing).
func (w *web) checkForm(r *http.Request) (ok, tooFast bool) {
	// "null" comes from privacy contexts; the double-submit token and the
	// SameSite=Strict cookie still apply.
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		if err != nil || !w.sameSite(r, u.Host) {
			return false, false
		}
	}
	c, err := r.Cookie(w.cookieName(r))
	if err != nil || c.Value == "" {
		return false, false
	}
	form := r.PostFormValue("csrf")
	if subtle.ConstantTimeCompare([]byte(form), []byte(c.Value)) != 1 {
		return false, false
	}
	ts, mac, found := strings.Cut(r.PostFormValue("ts"), ".")
	if !found || !hmac.Equal([]byte(mac), []byte(w.stampMAC(ts, c.Value))) {
		return false, false
	}
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false, false
	}
	age := time.Since(time.UnixMilli(ms))
	if age > formMaxAge || age < -time.Minute {
		return false, false
	}
	return true, w.s.cfg.FormMinFill > 0 && age < w.s.cfg.FormMinFill
}

// sameSite reports whether host (from the Origin header) is this server:
// the Host of the request, the host of PublicURL, or behind a trusted
// reverse proxy the X-Forwarded-Host it sets.
func (w *web) sameSite(r *http.Request, host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, r.Host) {
		return true
	}
	if u, err := url.Parse(w.s.cfg.PublicURL); err == nil && u.Host != "" && strings.EqualFold(host, u.Host) {
		return true
	}
	return w.s.cfg.TrustProxyHeaders && strings.EqualFold(host, forwardedHost(r))
}

// forwardedHost is the first X-Forwarded-Host (set by the proxy in front).
func forwardedHost(r *http.Request) string {
	h, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	return strings.TrimSpace(h)
}

// baseURL is the absolute URL of this server for links shown to the
// requester. Emails never use it (Host header poisoning): they use
// PublicURL only.
func (w *web) baseURL(r *http.Request) string {
	if w.s.cfg.PublicURL != "" {
		return w.s.cfg.PublicURL
	}
	scheme := "http"
	if w.secure(r) {
		scheme = "https"
	}
	host := r.Host
	if fh := forwardedHost(r); w.s.cfg.TrustProxyHeaders && fh != "" {
		host = fh
	}
	return scheme + "://" + host
}

func (w *web) ip(r *http.Request) string { return w.s.hub.clientIP(r) }

func (w *web) accountEvent(action, result string, accountID int64, r *http.Request) {
	w.s.m.inc(w.s.m.account, action+"_"+result)
	attrs := []any{"action", action, "result", result, "ip", w.s.log.ip(w.ip(r))}
	if accountID != 0 {
		attrs = append(attrs, "account", "acct:"+strconv.FormatInt(accountID, 10))
	}
	w.s.log.event("account", attrs...)
}

// ---- registration ----

func (w *web) register(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
	st := w.s.cfg.Accounts
	if st == nil {
		return message(d, "notfound.title", "disabled.p1"), http.StatusNotFound
	}
	if r.Method != http.MethodPost {
		w.fillForm(rw, r, d, false)
		return "register", http.StatusOK
	}
	if ok, _ := w.signup.take(w.ip(r), time.Now()); !ok {
		w.accountEvent("register", "rate_limited", 0, r)
		return message(d, "limit.title", "limit.p1"), http.StatusTooManyRequests
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "register", http.StatusBadRequest
	}
	ok, tooFast := w.checkForm(r)
	d.Name, d.Email = r.PostFormValue("name"), r.PostFormValue("email")
	if !ok {
		w.accountEvent("register", "csrf", 0, r)
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.form"]
		return "register", http.StatusForbidden
	}
	mailOn := w.s.cfg.Mailer != nil
	if tooFast || r.PostFormValue("website") != "" {
		// Anti-bot: answer like a success, do nothing.
		w.accountEvent("register", "bot", 0, r)
		if mailOn {
			return message(d, "sent.title", "sent.p1", "sent.p2"), http.StatusOK
		}
		return message(d, "ready.title", "ready.p1", "ready.p2"), http.StatusOK
	}

	name, nameOK := accounts.CleanDisplayName(d.Name)
	email := accounts.NormalizeEmail(d.Email)
	pw, pw2 := r.PostFormValue("password"), r.PostFormValue("confirm")
	if !nameOK {
		d.Errs["name"] = d.T["err.name"]
	}
	if !accounts.ValidEmail(email) {
		d.Errs["email"] = d.T["err.email"]
	}
	switch err := accounts.CheckPolicy(pw, email); {
	case errors.Is(err, accounts.ErrPasswordShort):
		d.Errs["password"] = d.T["err.pw.short"]
	case errors.Is(err, accounts.ErrPasswordLong):
		d.Errs["password"] = d.T["err.pw.long"]
	case err != nil:
		d.Errs["password"] = d.T["err.pw.common"]
	}
	if d.Errs["password"] == "" && pw != pw2 {
		d.Errs["confirm"] = d.T["err.pw.match"]
	}
	if r.PostFormValue("terms") != "on" {
		d.Errs["terms"] = d.T["err.terms"]
	}
	if len(d.Errs) > 0 {
		w.fillForm(rw, r, d, false)
		return "register", http.StatusUnprocessableEntity
	}
	hash := accounts.HashPassword(pw) // always: the answer takes the same time for every address
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// An address banned after abuse reports cannot register again. With
	// verification the answer stays the same as for any address.
	if banned, err := st.EmailBanned(ctx, email); err == nil && banned {
		w.accountEvent("register", "banned", 0, r)
		if mailOn {
			return message(d, "sent.title", "sent.p1", "sent.p2"), http.StatusOK
		}
		d.Errs["email"] = d.T["err.email.banned"]
		w.fillForm(rw, r, d, false)
		return "register", http.StatusUnprocessableEntity
	}

	if !mailOn {
		a, err := st.Create(ctx, email, name, hash, false)
		if errors.Is(err, accounts.ErrExists) {
			// Without email verification the address cannot be kept
			// secret: telling is the only useful answer.
			w.accountEvent("register", "exists", 0, r)
			d.Errs["email"] = d.T["err.email.taken"]
			w.fillForm(rw, r, d, false)
			return "register", http.StatusUnprocessableEntity
		}
		if err != nil {
			w.s.log.warn("account_db_error", "op", "create", "err", err.Error())
			w.fillForm(rw, r, d, false)
			d.FormErr = d.T["err.server"]
			return "register", http.StatusServiceUnavailable
		}
		w.accountEvent("register", "ok_unverified", a.ID, r)
		return message(d, "ready.title", "ready.p1", "ready.p2"), http.StatusOK
	}

	// With verification: same answer whatever the address, the mailbox
	// owner gets the explanation.
	a, _, err := st.ByEmail(ctx, email)
	switch {
	case err == nil && a.Verified:
		w.accountEvent("register", "exists", a.ID, r)
		w.sendMail(email, d.Lang, "mail.exists.subject", "mail.exists.body", w.s.cfg.PublicURL)
	case err == nil || errors.Is(err, accounts.ErrNotFound):
		secret, err := st.AddPending(ctx, email, name, hash)
		if err != nil {
			w.s.log.warn("account_db_error", "op", "add_pending", "err", err.Error())
			w.fillForm(rw, r, d, false)
			d.FormErr = d.T["err.server"]
			return "register", http.StatusServiceUnavailable
		}
		w.accountEvent("register", "pending", 0, r)
		link := w.s.cfg.PublicURL + "/verify?token=" + url.QueryEscape(secret)
		w.sendMail(email, d.Lang, "mail.verify.subject", "mail.verify.body", w.s.cfg.PublicURL, link)
	default:
		w.s.log.warn("account_db_error", "op", "by_email", "err", err.Error())
		w.fillForm(rw, r, d, false)
		d.FormErr = d.T["err.server"]
		return "register", http.StatusServiceUnavailable
	}
	return message(d, "sent.title", "sent.p1", "sent.p2"), http.StatusOK
}

// sendMail sends in the background (the answer must not depend on the
// mail server) and at most MailsPerHourEmail messages per address.
func (w *web) sendMail(to, lang, subjectKey, bodyKey string, args ...any) {
	if ok, _ := w.mails.take(to, time.Now()); !ok {
		w.s.log.event("mail_skipped", "reason", "rate_limited")
		return
	}
	t := webText[lang]
	subject, body := t[subjectKey], fmt.Sprintf(t[bodyKey], args...)
	m := w.s.cfg.Mailer
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := m.Send(ctx, to, subject, body); err != nil {
			w.s.log.warn("mail_failed", "err", err.Error())
			return
		}
		w.s.m.inc(w.s.m.account, "mail_sent")
	}()
}

// verifyPage shows a confirmation button (GET) and activates the account
// (POST). A GET never activates anything: mail scanners open links.
func (w *web) verifyPage(rw http.ResponseWriter, r *http.Request, d *pageData) (string, int) {
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
		if len(token) > 128 || !st.PendingValid(ctx, token) {
			return message(d, "verify.bad.title", "verify.bad.p1"), http.StatusNotFound
		}
		d.Token = token
		w.fillForm(rw, r, d, false)
		return "verify", http.StatusOK
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return message(d, "verify.bad.title", "verify.bad.p1"), http.StatusBadRequest
	}
	token := r.PostFormValue("token")
	if ok, _ := w.checkForm(r); !ok {
		// Expired form or cross-site post: show the button again.
		w.accountEvent("verify", "csrf", 0, r)
		if len(token) > 128 || !st.PendingValid(ctx, token) {
			return message(d, "verify.bad.title", "verify.bad.p1"), http.StatusForbidden
		}
		d.Token = token
		d.FormErr = d.T["err.form"]
		w.fillForm(rw, r, d, false)
		return "verify", http.StatusForbidden
	}
	a, _, err := st.Verify(ctx, token)
	if errors.Is(err, accounts.ErrNotFound) {
		w.accountEvent("verify", "invalid", 0, r)
		return message(d, "verify.bad.title", "verify.bad.p1"), http.StatusNotFound
	}
	if err != nil {
		w.s.log.warn("account_db_error", "op", "verify", "err", err.Error())
		return message(d, "error.title", "err.server"), http.StatusServiceUnavailable
	}
	w.accountEvent("verify", "ok", a.ID, r)
	return message(d, "verify.ok.title", "verify.ok.p1"), http.StatusOK
}

// ---- JSON API ----

type apiAccount struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
}

func toAPI(a accounts.Account) apiAccount {
	return apiAccount{ID: a.ID, Email: a.Email, Name: a.Name, Verified: a.Verified}
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	h := rw.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func apiError(rw http.ResponseWriter, status int, code string) {
	writeJSON(rw, status, map[string]string{"error": code})
}

func rateLimited(rw http.ResponseWriter, wait time.Duration) {
	secs := int((wait + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	rw.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(rw, http.StatusTooManyRequests, map[string]any{"error": "rate_limited", "retryAfter": secs})
}

func (w *web) apiInfo(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		apiError(rw, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	reg := ""
	if w.s.cfg.Accounts != nil {
		reg = w.baseURL(r) + "/register"
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"name": w.s.cfg.Name, "version": w.s.cfg.Version, "mode": w.s.cfg.Mode,
		"accounts": w.s.cfg.Accounts != nil, "registerUrl": reg,
	})
}

// apiPrelude checks the method, that accounts are on, and a rate limit.
func (w *web) apiPrelude(rw http.ResponseWriter, r *http.Request, method string, b *syncBuckets) bool {
	if w.s.cfg.Accounts == nil {
		apiError(rw, http.StatusNotFound, "accounts_disabled")
		return false
	}
	if r.Method != method {
		rw.Header().Set("Allow", method)
		apiError(rw, http.StatusMethodNotAllowed, "method_not_allowed")
		return false
	}
	if ok, wait := b.take(w.ip(r), time.Now()); !ok {
		rateLimited(rw, wait)
		return false
	}
	return true
}

func (w *web) apiLogin(rw http.ResponseWriter, r *http.Request) {
	if !w.apiPrelude(rw, r, http.MethodPost, w.login) {
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Client   string `json:"client"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxJSONBytes))
	if err := dec.Decode(&req); err != nil {
		apiError(rw, http.StatusBadRequest, "bad_request")
		return
	}
	st := w.s.cfg.Accounts
	email := accounts.NormalizeEmail(req.Email)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	a, _, res := w.checkCredentials(ctx, r, email, req.Password, "login", accounts.LoginDesktop)
	switch res {
	case credLimited:
		rateLimited(rw, w.s.cfg.Limits.LoginFailWindow)
		return
	case credBad, credReview:
		// The Desktop API keeps one answer for a disabled account (the
		// review notice is on the web sign-in page).
		apiError(rw, http.StatusUnauthorized, "invalid_credentials")
		return
	case credUnverified:
		apiError(rw, http.StatusForbidden, "email_not_verified")
		return
	case credError:
		apiError(rw, http.StatusServiceUnavailable, "unavailable")
		return
	}
	ip := w.s.log.ip(w.ip(r))
	token, exp, err := st.IssueTokenFor(ctx, a.ID, req.Client, ip)
	if err != nil {
		w.s.log.warn("account_db_error", "op", "issue_token", "err", err.Error())
		apiError(rw, http.StatusServiceUnavailable, "unavailable")
		return
	}
	w.loginOK(ctx, r, a, accounts.LoginDesktop)
	w.accountEvent("login", "ok", a.ID, r)
	writeJSON(rw, http.StatusOK, map[string]any{
		"token": token, "expiresAt": exp.UTC().Format(time.RFC3339), "account": toAPI(a),
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (w *web) apiLogout(rw http.ResponseWriter, r *http.Request) {
	if !w.apiPrelude(rw, r, http.MethodPost, w.api) {
		return
	}
	tok := bearer(r)
	if tok == "" || len(tok) > 128 {
		apiError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := w.s.cfg.Accounts.RevokeToken(ctx, tok); err != nil {
		w.s.log.warn("account_db_error", "op", "revoke", "err", err.Error())
		apiError(rw, http.StatusServiceUnavailable, "unavailable")
		return
	}
	w.accountEvent("logout", "ok", 0, r)
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusNoContent)
}

func (w *web) apiMe(rw http.ResponseWriter, r *http.Request) {
	if !w.apiPrelude(rw, r, http.MethodGet, w.api) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	a, exp, err := w.s.cfg.Accounts.ResolveTokenFrom(ctx, bearer(r), w.s.log.ip(w.ip(r)))
	if err != nil && !errors.Is(err, accounts.ErrNotFound) {
		w.s.log.warn("account_db_error", "op", "resolve_token", "err", err.Error())
		apiError(rw, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if err != nil || (w.s.cfg.Mailer != nil && !a.Verified) {
		apiError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"account": toAPI(a), "expiresAt": exp.UTC().Format(time.RFC3339)})
}
