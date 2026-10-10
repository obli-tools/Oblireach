package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	sl "github.com/oblireach/supportlink"

	"github.com/oblireach/rdv/internal/accounts"
)

const (
	adminEmail = "boss@example.com"
	adminPass  = "admin passphrase 4 tests"
	opEmail    = "op@example.com"
	opPass     = "operator passphrase 4 tests"
)

// mkAccount creates a verified account directly in the store.
func (e *acctEnv) mkAccount(t *testing.T, email, name, pw string, admin bool) accounts.Account {
	t.Helper()
	ctx := context.Background()
	a, err := e.store.Create(ctx, email, name, accounts.HashPassword(pw), true)
	if err != nil {
		t.Fatal(err)
	}
	if admin {
		if err := e.store.SetRole(ctx, a.ID, accounts.RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = e.store.ByID(ctx, a.ID)
	return a
}

var (
	anyHiddenRe = regexp.MustCompile(`<input type="hidden" name="([a-z_]+)" value="([^"]*)">`)
	secretRe    = regexp.MustCompile(`<code class="secret">([A-Z2-7 ]+)</code>`)
)

// fields returns the hidden inputs of a page (the last value wins).
func fields(page string) url.Values {
	v := url.Values{}
	for _, m := range anyHiddenRe.FindAllStringSubmatch(page, -1) {
		v.Set(m[1], html.UnescapeString(m[2]))
	}
	return v
}

// csrf returns the CSRF fields of the browser (from a signed-in page or the
// login page).
func (e *acctEnv) csrf(t *testing.T, b *browser, path string) url.Values {
	t.Helper()
	code, page, _ := b.do("GET", e.base+path, nil, nil)
	if code != 200 {
		t.Fatalf("GET %s: %d", path, code)
	}
	f := url.Values{}
	h := hidden(page)
	f.Set("csrf", h.Get("csrf"))
	f.Set("ts", h.Get("ts"))
	if f.Get("csrf") == "" {
		t.Fatalf("no csrf on %s", path)
	}
	return f
}

// post submits a form with the browser's CSRF fields plus kv pairs.
func (e *acctEnv) post(t *testing.T, b *browser, csrfPage, path string, kv ...string) (int, string, http.Header) {
	t.Helper()
	f := e.csrf(t, b, csrfPage)
	for i := 0; i+1 < len(kv); i += 2 {
		f.Set(kv[i], kv[i+1])
	}
	return b.do("POST", e.base+path, f, nil)
}

// webLogin signs in with email and password; totp, if set, returns the
// code for the second step. It returns the final answer of the POST.
func (e *acctEnv) webLogin(t *testing.T, b *browser, email, pw string, totp func() string) (int, string, http.Header) {
	t.Helper()
	code, page, h := e.post(t, b, "/login", "/login", "email", email, "password", pw)
	if code == 200 && strings.Contains(page, `name="ticket"`) {
		if totp == nil {
			return code, page, h
		}
		f := fields(page)
		f.Set("code", totp())
		code, page, h = b.do("POST", e.base+"/login", f, nil)
	}
	return code, page, h
}

func loc(h http.Header) string { return h.Get("Location") }

// enrolTOTP enrols the signed-in browser's account; it returns the key.
func (e *acctEnv) enrolTOTP(t *testing.T, b *browser) []byte {
	t.Helper()
	code, page, _ := b.do("GET", e.base+"/account/2fa", nil, nil)
	if code != 200 || !strings.Contains(page, "<svg class=\"qr\"") {
		t.Fatalf("2fa page: %d", code)
	}
	m := secretRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no secret shown")
	}
	key, err := accounts.ParseTOTPSecret(m[1])
	if err != nil {
		t.Fatal(err)
	}
	f := fields(page)
	f.Set("code", "000000")
	if code, page, _ := b.do("POST", e.base+"/account/2fa", f, nil); code != 422 || !strings.Contains(page, "not valid") {
		t.Fatalf("wrong enrol code: %d", code)
	}
	f.Set("code", accounts.TOTPCode(key, accounts.TOTPStep(time.Now())-1))
	code, _, h := b.do("POST", e.base+"/account/2fa", f, nil)
	if code != 303 {
		t.Fatalf("enrol: %d", code)
	}
	_ = h
	return key
}

// totpAt returns a code generator for step offsets (each use must be a
// later step than the previous one).
func totpAt(key []byte, off int64) func() string {
	return func() string { return accounts.TOTPCode(key, accounts.TOTPStep(time.Now())+off) }
}

// adminBrowser returns a browser signed in as an admin with TOTP.
func (e *acctEnv) adminBrowser(t *testing.T) (*browser, accounts.Account, []byte) {
	t.Helper()
	a := e.mkAccount(t, adminEmail, "The Boss", adminPass, true)
	b := newBrowser(t)
	code, _, h := e.webLogin(t, b, adminEmail, adminPass, nil)
	if code != 303 || loc(h) != "/account/security?need2fa=1" {
		t.Fatalf("admin without TOTP: %d %q", code, loc(h))
	}
	key := e.enrolTOTP(t, b)
	if code, page, _ := b.do("GET", e.base+"/admin", nil, nil); code != 200 || !strings.Contains(page, "Dashboard") {
		t.Fatalf("admin dashboard after enrolment: %d", code)
	}
	return b, a, key
}

func TestWebLoginAndLanding(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	e.mkAccount(t, opEmail, "Op One", opPass, false)
	b := newBrowser(t)
	if code, page, _ := b.do("GET", e.base+"/", nil, nil); code != 200 || !strings.Contains(page, `href="/login"`) || !strings.Contains(page, `href="/register"`) {
		t.Fatalf("landing: %d", code)
	}
	// Wrong password: generic answer, recorded in the login history.
	code, page, _ := e.webLogin(t, b, opEmail, "wrong password here", nil)
	if code != 401 || !strings.Contains(page, "Incorrect email address or password.") {
		t.Fatalf("bad login: %d", code)
	}
	code, page, _ = e.webLogin(t, b, "nobody@example.com", opPass, nil)
	if code != 401 || !strings.Contains(page, "Incorrect email address or password.") {
		t.Fatalf("unknown login: %d", code)
	}
	code, _, h := e.webLogin(t, b, opEmail, opPass, nil)
	if code != 303 || loc(h) != "/account" {
		t.Fatalf("login: %d %q", code, loc(h))
	}
	var sess *http.Cookie
	for _, c := range h.Values("Set-Cookie") {
		if strings.HasPrefix(c, sessCookie+"=") {
			if !strings.Contains(c, "HttpOnly") || !strings.Contains(c, "SameSite=Strict") {
				t.Fatalf("session cookie flags: %s", c)
			}
			sess = &http.Cookie{}
		}
	}
	if sess == nil {
		t.Fatal("no session cookie")
	}
	if code, _, h := b.do("GET", e.base+"/", nil, nil); code != 303 || loc(h) != "/account" {
		t.Fatalf("landing signed in: %d %q", code, loc(h))
	}
	if code, page, _ := b.do("GET", e.base+"/account", nil, nil); code != 200 || !strings.Contains(page, "Hello Op One") {
		t.Fatalf("overview: %d", code)
	}
	a, _, _ := e.store.ByEmail(context.Background(), opEmail)
	logins, _ := e.store.Logins(context.Background(), a.ID, 10)
	if len(logins) != 2 || !logins[0].OK || logins[1].OK || logins[1].Reason != "bad_password" || logins[0].Kind != "web" {
		t.Fatalf("login history %+v", logins)
	}
	// Open redirect: next is kept only for our pages.
	b2 := newBrowser(t)
	f := e.csrf(t, b2, "/login")
	f.Set("email", opEmail)
	f.Set("password", opPass)
	f.Set("next", "//evil.example/x")
	if code, _, h := b2.do("POST", e.base+"/login", f, nil); code != 303 || loc(h) != "/account" {
		t.Fatalf("next not filtered: %q", loc(h))
	}
	b3 := newBrowser(t)
	if code, _, h := b3.do("GET", e.base+"/account/settings", nil, nil); code != 303 || loc(h) != "/login?next=%2Faccount%2Fsettings" {
		t.Fatalf("anonymous: %d %q", code, loc(h))
	}
	f = e.csrf(t, b3, "/login?next=%2Faccount%2Fsettings")
	f.Set("email", opEmail)
	f.Set("password", opPass)
	f.Set("next", "/account/settings")
	if code, _, h := b3.do("POST", e.base+"/login", f, nil); code != 303 || loc(h) != "/account/settings" {
		t.Fatalf("next: %q", loc(h))
	}
	// Logout needs the CSRF token, then ends the session server side.
	if code, _, _ := b.do("POST", e.base+"/logout", url.Values{}, nil); code != 403 {
		t.Fatalf("logout without csrf: %d", code)
	}
	if code, _, h := e.post(t, b, "/account", "/logout"); code != 303 || loc(h) != "/login?out=1" {
		t.Fatalf("logout: %d %q", code, loc(h))
	}
	if code, _, _ := b.do("GET", e.base+"/account", nil, nil); code != 303 {
		t.Fatal("still signed in after logout")
	}
	ws, _ := e.store.WebSessions(context.Background(), a.ID)
	if len(ws) != 2 { // b2 and b3 remain
		t.Fatalf("web sessions: %d", len(ws))
	}
}

func TestWebLoginRateLimitAndSecureCookie(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.TrustProxyHeaders = true })
	e.mkAccount(t, opEmail, "Op One", opPass, false)
	b := newBrowser(t)
	for i := 0; i < 10; i++ {
		if code, _, _ := e.webLogin(t, b, opEmail, "wrong password here", nil); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	// The address is blocked for the window, even with the right password.
	if code, page, _ := e.webLogin(t, b, opEmail, opPass, nil); code != 429 || !strings.Contains(page, "Too many attempts") {
		t.Fatalf("after 10 failures: %d", code)
	}
	// Over HTTPS (proxy header): __Host- cookies with Secure.
	e2 := newAcctEnv(t, false, func(c *Config) { c.TrustProxyHeaders = true })
	e2.mkAccount(t, opEmail, "Op One", opPass, false)
	https := map[string]string{"X-Forwarded-Proto": "https"}
	jar := newBrowser(t)
	_, page, h := jar.do("GET", e2.base+"/login", nil, https)
	if !strings.Contains(strings.Join(h.Values("Set-Cookie"), "\n"), csrfCookieHost+"=") {
		t.Fatal("no __Host- csrf cookie")
	}
	// The cookie jar does not send Secure cookies over http: send them by hand.
	var csrfVal string
	for _, c := range h.Values("Set-Cookie") {
		if v, ok := strings.CutPrefix(c, csrfCookieHost+"="); ok {
			csrfVal, _, _ = strings.Cut(v, ";")
		}
	}
	f := hidden(page)
	f.Set("email", opEmail)
	f.Set("password", opPass)
	req, _ := http.NewRequest("POST", e2.base+"/login", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(&http.Cookie{Name: csrfCookieHost, Value: csrfVal})
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sc := strings.Join(resp.Header.Values("Set-Cookie"), "\n")
	if resp.StatusCode != 303 || !strings.Contains(sc, sessCookieHost+"=") || !strings.Contains(sc, "Secure") || !strings.Contains(sc, "SameSite=Strict") {
		t.Fatalf("https login: %d %s", resp.StatusCode, sc)
	}
}

func TestAdminAccessControl(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	anon := newBrowser(t)
	if code, _, h := anon.do("GET", e.base+"/admin", nil, nil); code != 303 || !strings.HasPrefix(loc(h), "/login?next=%2Fadmin") {
		t.Fatalf("anonymous /admin: %d %q", code, loc(h))
	}
	ob := newBrowser(t)
	if code, _, _ := e.webLogin(t, ob, opEmail, opPass, nil); code != 303 {
		t.Fatal("operator login")
	}
	for _, p := range []string{"/admin", "/admin/accounts", "/admin/live", "/admin/history", "/admin/audit", fmt.Sprintf("/admin/accounts/%d", op.ID)} {
		if code, page, _ := ob.do("GET", e.base+p, nil, nil); code != 403 || !strings.Contains(page, "Access denied") {
			t.Fatalf("operator GET %s: %d", p, code)
		}
	}
	if code, _, _ := e.post(t, ob, "/account", fmt.Sprintf("/admin/accounts/%d", op.ID), "op", "promote", "confirmed", "1"); code != 403 {
		t.Fatalf("operator POST: %d", code)
	}
	if a, _ := e.store.ByID(context.Background(), op.ID); a.IsAdmin() {
		t.Fatal("operator promoted itself")
	}
	if code, _, h := ob.do("GET", e.base+"/login?next=%2Fadmin", nil, nil); code != 303 || loc(h) != "/admin" {
		t.Fatalf("signed-in login page: %q", loc(h)) // then 403 there
	}

	// Admin: TOTP required by default; the admin pages wait for it.
	ab, _, key := e.adminBrowser(t)
	// CSRF: missing token, foreign origin.
	f := url.Values{"op": {"promote"}, "confirmed": {"1"}}
	if code, _, _ := ab.do("POST", e.base+fmt.Sprintf("/admin/accounts/%d", op.ID), f, nil); code != 403 {
		t.Fatalf("admin POST without csrf: %d", code)
	}
	f = e.csrf(t, ab, "/admin")
	f.Set("op", "promote")
	f.Set("confirmed", "1")
	if code, _, _ := ab.do("POST", e.base+fmt.Sprintf("/admin/accounts/%d", op.ID), f, map[string]string{"Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("cross-origin POST: %d", code)
	}
	if a, _ := e.store.ByID(context.Background(), op.ID); a.IsAdmin() {
		t.Fatal("promoted through a forged request")
	}

	// A session of the admin without the TOTP check (signed in before the
	// enrolment elsewhere) must pass it before the admin pages.
	secret, err := e.store.NewWebSession(context.Background(), mustByEmail(t, e, adminEmail).ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	sb := newBrowser(t)
	u, _ := url.Parse(e.base)
	sb.c.Jar.SetCookies(u, []*http.Cookie{{Name: sessCookie, Value: secret, Path: "/"}})
	code, _, h := sb.do("GET", e.base+"/admin/accounts", nil, nil)
	if code != 303 || !strings.HasPrefix(loc(h), "/login/verify?next=") {
		t.Fatalf("step-up redirect: %d %q", code, loc(h))
	}
	code, page, _ := sb.do("GET", e.base+loc(h), nil, nil)
	if code != 200 || !strings.Contains(page, `action="/login/verify"`) {
		t.Fatalf("step-up page: %d", code)
	}
	ff := fields(page)
	ff.Set("code", "123456")
	if code, _, _ := sb.do("POST", e.base+"/login/verify", ff, nil); code != 401 {
		t.Fatalf("step-up wrong code: %d", code)
	}
	ff.Set("code", totpAt(key, 0)())
	if code, _, h := sb.do("POST", e.base+"/login/verify", ff, nil); code != 303 || loc(h) != "/admin/accounts" {
		t.Fatalf("step-up: %d %q", code, loc(h))
	}
	// The same code is refused a second time (replay).
	ff.Set("code", totpAt(key, 0)())
	if ok, _ := e.srv.web.checkTOTP(context.Background(), mustByEmail(t, e, adminEmail), totpAt(key, 0)()); ok {
		t.Fatal("TOTP code replayed")
	}
	if code, _, _ := sb.do("GET", e.base+"/admin/accounts", nil, nil); code != 200 {
		t.Fatalf("after step-up: %d", code)
	}

	// TOTP optional: an admin without it gets in.
	e2 := newAcctEnv(t, false, func(c *Config) { c.AdminTOTPOptional = true })
	e2.mkAccount(t, adminEmail, "The Boss", adminPass, true)
	b2 := newBrowser(t)
	if code, _, h := e2.webLogin(t, b2, adminEmail, adminPass, nil); code != 303 || loc(h) != "/admin" {
		t.Fatalf("optional TOTP login: %d %q", code, loc(h))
	}
	if code, _, _ := b2.do("GET", e2.base+"/admin", nil, nil); code != 200 {
		t.Fatalf("optional TOTP admin: %d", code)
	}
}

func mustByEmail(t *testing.T, e *acctEnv, email string) accounts.Account {
	t.Helper()
	a, _, err := e.store.ByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTOTPLogin(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	_, admin, key := e.adminBrowser(t)
	b := newBrowser(t)
	code, page, _ := e.webLogin(t, b, adminEmail, adminPass, nil)
	if code != 200 || !strings.Contains(page, "Two-step verification") || !strings.Contains(page, `autocomplete="one-time-code"`) {
		t.Fatalf("second step: %d", code)
	}
	f := fields(page)
	// A forged or altered ticket is refused.
	bad := url.Values{}
	for k, v := range f {
		bad[k] = v
	}
	bad.Set("ticket", strings.Replace(f.Get("ticket"), ".", ".9", 1))
	bad.Set("code", totpAt(key, 0)())
	if code, page, _ := b.do("POST", e.base+"/login", bad, nil); code != 401 || !strings.Contains(page, "expired") {
		t.Fatalf("forged ticket: %d", code)
	}
	f.Set("code", "000000")
	if code, page, _ := b.do("POST", e.base+"/login", f, nil); code != 401 || !strings.Contains(page, "not valid") {
		t.Fatalf("wrong code: %d", code)
	}
	f.Set("code", totpAt(key, 0)())
	if code, _, h := b.do("POST", e.base+"/login", f, nil); code != 303 || loc(h) != "/admin" {
		t.Fatalf("totp login: %d %q", code, loc(h))
	}
	logins, _ := e.store.Logins(context.Background(), admin.ID, 10)
	if len(logins) < 2 || !logins[0].OK || logins[1].Reason != "bad_totp" {
		t.Fatalf("history %+v", logins)
	}
	// The TOTP secret is sealed at rest: the database never holds it in clear.
	sealed, _ := e.store.TOTPSecret(context.Background(), admin.ID)
	if len(sealed) < 30 || strings.Contains(string(sealed), string(key)) {
		t.Fatal("TOTP secret not sealed")
	}
	// An admin cannot remove its TOTP while it is required; an operator can.
	if code, page, _ := e.post(t, b, "/account/security", "/account/security", "op", "tfa_disable", "password", adminPass, "code", totpAt(key, 1)()); code != 403 || !strings.Contains(page, "required for administrators") {
		t.Fatalf("admin tfa_disable: %d", code)
	}
	e.mkAccount(t, opEmail, "Op", opPass, false)
	ob := newBrowser(t)
	e.webLogin(t, ob, opEmail, opPass, nil)
	okey := e.enrolTOTP(t, ob)
	if code, _, _ := e.post(t, ob, "/account/security", "/account/security", "op", "tfa_disable", "password", "nope nope nope", "code", totpAt(okey, 0)()); code != 422 {
		t.Fatal("tfa_disable with a wrong password")
	}
	if code, _, h := e.post(t, ob, "/account/security", "/account/security", "op", "tfa_disable", "password", opPass, "code", totpAt(okey, 0)()); code != 303 || loc(h) != "/account/security?done=tfa_off" {
		t.Fatalf("tfa_disable: %d %q", code, loc(h))
	}
	if mustByEmail(t, e, opEmail).TOTP {
		t.Fatal("still enrolled")
	}
}

// adminDo runs an admin action through its confirmation page.
func (e *acctEnv) adminDo(t *testing.T, b *browser, id int64, op string, kv ...string) (int, string, http.Header) {
	t.Helper()
	path := fmt.Sprintf("/admin/accounts/%d", id)
	args := append([]string{"op", op}, kv...)
	code, page, _ := e.post(t, b, path, path, args...)
	if code != 200 || !strings.Contains(page, `name="confirmed" value="1"`) {
		return code, page, nil // refused before the confirmation
	}
	f := fields(page)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i] == "typed" {
			f.Set("typed", kv[i+1])
		}
	}
	return b.do("POST", e.base+path, f, nil)
}

func TestAdminActions(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	ab, admin, _ := e.adminBrowser(t)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()
	_, r := e.login(t, opEmail, opPass)
	if r.Token == "" {
		t.Fatal("desktop login")
	}
	ob := newBrowser(t)
	e.webLogin(t, ob, opEmail, opPass, nil)
	detail := fmt.Sprintf("/admin/accounts/%d", op.ID)

	// Detail page: profile, Desktop login with its client string.
	if code, page, _ := ab.do("GET", e.base+detail, nil, nil); code != 200 || !strings.Contains(page, "desktop/1.0.34") || !strings.Contains(page, opEmail) {
		t.Fatalf("detail: %d", code)
	}
	// Without confirmation nothing happens.
	if code, page, _ := e.post(t, ab, detail, detail, "op", "disable"); code != 200 || !strings.Contains(page, "Confirm this action") {
		t.Fatalf("confirm page: %d", code)
	}
	if mustByEmail(t, e, opEmail).Disabled {
		t.Fatal("disabled without confirmation")
	}

	// disable: tokens and web sessions end.
	if code, _, h := e.adminDo(t, ab, op.ID, "disable"); code != 303 || loc(h) != detail+"?done=disable" {
		t.Fatalf("disable: %d %q", code, loc(h))
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", r.Token); c != 401 {
		t.Fatal("token survived disable")
	}
	if code, _, _ := ob.do("GET", e.base+"/account", nil, nil); code != 303 {
		t.Fatal("web session survived disable")
	}
	if c, _ := e.login(t, opEmail, opPass); c != 401 {
		t.Fatal("disabled account signed in")
	}
	if code, _, _ := e.adminDo(t, ab, op.ID, "enable"); code != 303 || mustByEmail(t, e, opEmail).Disabled {
		t.Fatal("enable")
	}

	// revoke_tokens
	_, r = e.login(t, opEmail, opPass)
	if code, _, _ := e.adminDo(t, ab, op.ID, "revoke_tokens"); code != 303 {
		t.Fatal("revoke_tokens")
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", r.Token); c != 401 {
		t.Fatal("token survived revoke_tokens")
	}

	// reset_pw without SMTP: a one-time link shown to the admin.
	code, page, _ := e.adminDo(t, ab, op.ID, "reset_pw")
	m := regexp.MustCompile(`<code>(https?://[^<]+/reset\?token=[^<]+)</code>`).FindStringSubmatch(page)
	if code != 200 || m == nil {
		t.Fatalf("reset_pw: %d", code)
	}
	link := html.UnescapeString(m[1])
	nb := newBrowser(t)
	code, page, _ = nb.do("GET", link, nil, nil)
	if code != 200 {
		t.Fatalf("reset link: %d", code)
	}
	f := fields(page)
	f.Set("password", "brand new passphrase 1")
	f.Set("confirm", "brand new passphrase 1")
	if code, _, _ := nb.do("POST", e.base+"/reset", f, nil); code != 200 {
		t.Fatalf("reset: %d", code)
	}
	if c, _ := e.login(t, opEmail, "brand new passphrase 1"); c != 200 {
		t.Fatal("new password")
	}
	if code, _, _ := nb.do("GET", link, nil, nil); code != 404 {
		t.Fatal("reset link reused")
	}

	// quota: stored, shown, applied at the next connect.
	if code, _, _ := e.adminDo(t, ab, op.ID, "quota", "max_sessions", "abc"); code != 422 {
		t.Fatalf("bad quota: %d", code)
	}
	if code, _, _ := e.adminDo(t, ab, op.ID, "quota", "max_sessions", "2", "sessions_day", "1", "relay_gb", ""); code != 303 {
		t.Fatal("quota")
	}
	a := mustByEmail(t, e, opEmail)
	if a.Quota.MaxSessions == nil || *a.Quota.MaxSessions != 2 || *a.Quota.SessionsDay != 1 || a.Quota.RelayBytes != nil {
		t.Fatalf("quota stored %+v", a.Quota)
	}
	_, r = e.login(t, opEmail, "brand new passphrase 1")
	if _, _, os1, err := connectPair(t, e.testEnv, tokenOp(e.testEnv, r.Token)); err != nil {
		t.Fatal(err)
	} else {
		os1.Close()
	}
	hr := startHost(t, e.testEnv, loopback, allow, nil)
	if _, err := connect(t, tokenOp(e.testEnv, r.Token), hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrQuota) {
		t.Fatalf("daily override not applied: %v", err)
	}

	// promote / demote, last admin protection, self protection.
	if code, _, _ := e.adminDo(t, ab, op.ID, "promote"); code != 303 || !mustByEmail(t, e, opEmail).IsAdmin() {
		t.Fatal("promote")
	}
	if code, _, _ := e.adminDo(t, ab, op.ID, "demote"); code != 303 || mustByEmail(t, e, opEmail).IsAdmin() {
		t.Fatal("demote")
	}
	if code, page, _ := e.adminDo(t, ab, admin.ID, "demote"); code != 409 || !strings.Contains(page, "your own account") {
		t.Fatalf("self demote: %d", code)
	}
	if err := e.store.SetRole(ctx, admin.ID, accounts.RoleOperator); !errors.Is(err, accounts.ErrLastAdmin) {
		t.Fatalf("last admin: %v", err)
	}

	// reset_2fa
	ob2 := newBrowser(t)
	e.webLogin(t, ob2, opEmail, "brand new passphrase 1", nil)
	e.enrolTOTP(t, ob2)
	if code, _, _ := e.adminDo(t, ab, op.ID, "reset_2fa"); code != 303 || mustByEmail(t, e, opEmail).TOTP {
		t.Fatal("reset_2fa")
	}

	// verify (an unverified account created without SMTP).
	uv, _ := e.store.Create(ctx, "new@example.com", "Newbie", accounts.HashPassword(opPass), false)
	if code, page, _ := ab.do("GET", e.base+fmt.Sprintf("/admin/accounts/%d", uv.ID), nil, nil); code != 200 || strings.Contains(page, `value="resend"`) {
		t.Fatal("resend offered without SMTP")
	}
	if code, _, _ := e.adminDo(t, ab, uv.ID, "verify"); code != 303 || !mustByEmail(t, e, "new@example.com").Verified {
		t.Fatal("verify")
	}

	// delete: typed confirmation.
	if code, page, _ := e.adminDo(t, ab, uv.ID, "delete", "typed", "wrong@example.com"); code != 422 || !strings.Contains(page, "does not match") {
		t.Fatalf("delete mismatch: %d", code)
	}
	if code, _, h := e.adminDo(t, ab, uv.ID, "delete", "typed", "new@example.com"); code != 303 || loc(h) != "/admin/accounts?done=deleted" {
		t.Fatalf("delete: %d %q", code, loc(h))
	}
	if _, err := e.store.ByID(ctx, uv.ID); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatal("not deleted")
	}

	// Audit log: every action, newest first, visible to admins.
	code, page, _ = ab.do("GET", e.base+"/admin/audit", nil, nil)
	if code != 200 {
		t.Fatal("audit page")
	}
	for _, want := range []string{"disabled", "enabled", "sign-ins ended", "password reset", "quotas", "promoted", "demoted", "2FA removed", "verified", "deleted", "new@example.com"} {
		if !strings.Contains(page, want) {
			t.Fatalf("audit log misses %q", want)
		}
	}
	entries, total, _ := e.store.AuditLog(ctx, 100, 0)
	// disable, enable, revoke_tokens, reset_pw, quota, promote, demote,
	// reset_2fa, verify, delete (refused attempts are not actions).
	if total != 10 || entries[0].Action != "delete" || entries[0].Actor != adminEmail {
		t.Fatalf("audit entries %d %+v", total, entries[0])
	}
	if _, err := e.store.DB().ExecContext(ctx, `UPDATE audit_log SET actor = 'x'`); err == nil {
		t.Fatal("audit log is not append-only")
	}
	if e.metric(t, `rdv_admin_actions_total{action="disable"}`) != "1" {
		t.Fatal("admin metric")
	}

	// Accounts list: search and sort.
	code, page, _ = ab.do("GET", e.base+"/admin/accounts?q=op%40&sort=email", nil, nil)
	if code != 200 || !strings.Contains(page, opEmail) || strings.Contains(page, ">"+adminEmail+"<") || !strings.Contains(page, "1 account(s)") {
		t.Fatalf("search: %d", code)
	}
	code, page, _ = ab.do("GET", e.base+"/admin/accounts?sort=email&dir=desc", nil, nil)
	if code != 200 || strings.Index(page, opEmail) > strings.Index(page, ">"+adminEmail+"<") {
		t.Fatal("sort desc")
	}
}

func TestAdminResendAndResetWithSMTP(t *testing.T) {
	e := newAcctEnv(t, true, func(c *Config) { c.AdminTOTPOptional = true })
	e.mkAccount(t, adminEmail, "The Boss", adminPass, true)
	ab := newBrowser(t)
	e.webLogin(t, ab, adminEmail, adminPass, nil)
	ctx := context.Background()
	uv, _ := e.store.Create(ctx, "late@example.com", "Late", accounts.HashPassword(opPass), false)
	if code, _, _ := e.adminDo(t, ab, uv.ID, "resend"); code != 303 {
		t.Fatalf("resend: %d", code)
	}
	nb := newBrowser(t)
	e.verifyMail(t, nb, "late@example.com")
	if !mustByEmail(t, e, "late@example.com").Verified {
		t.Fatal("not verified by the resent link")
	}
	if code, _, _ := e.adminDo(t, ab, uv.ID, "reset_pw"); code != 303 {
		t.Fatal("reset_pw with SMTP")
	}
	msg := e.sink.Wait(t, 5*time.Second)
	if !strings.Contains(msg.Data, "/reset?token=") || msg.To[0] != "late@example.com" {
		t.Fatalf("reset mail %q", msg.Data)
	}
}

func TestForgotReset(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()
	_, r := e.login(t, opEmail, opPass)
	old := newBrowser(t)
	e.webLogin(t, old, opEmail, opPass, nil)

	b := newBrowser(t)
	if code, page, _ := b.do("GET", e.base+"/login", nil, nil); code != 200 || !strings.Contains(page, `href="/forgot"`) {
		t.Fatal("login page without forgot link")
	}
	// Unknown address: same answer, no mail.
	code, unknown, _ := e.post(t, b, "/forgot", "/forgot", "email", "nobody@example.com")
	code2, known, _ := e.post(t, b, "/forgot", "/forgot", "email", opEmail)
	if code != 200 || code2 != 200 || unknown != known {
		t.Fatal("forgot answers differ")
	}
	msg := e.sink.Wait(t, 5*time.Second)
	if msg.To[0] != opEmail {
		t.Fatalf("mail to %v", msg.To)
	}
	e.sink.None(t, 300*time.Millisecond)
	link := regexp.MustCompile(`https?://\S+/reset\?token=[A-Za-z0-9_%-]+`).FindString(msg.Data)
	if link == "" {
		t.Fatalf("no link in %q", msg.Data)
	}
	// The link is stored hashed only.
	tok, _ := url.Parse(link)
	if strings.Contains(dumpDB(t, e.store), tok.Query().Get("token")) {
		t.Fatal("reset token stored in clear")
	}
	code, page, _ := b.do("GET", link, nil, nil)
	if code != 200 || !strings.Contains(page, "Choose a new password") {
		t.Fatalf("reset page: %d", code)
	}
	f := fields(page)
	f.Set("password", "short")
	f.Set("confirm", "short")
	if code, _, _ := b.do("POST", e.base+"/reset", f, nil); code != 422 {
		t.Fatal("weak password accepted")
	}
	f.Set("password", "a fresh passphrase 99")
	f.Set("confirm", "a fresh passphrase 99")
	if code, page, _ := b.do("POST", e.base+"/reset", f, nil); code != 200 || !strings.Contains(page, "Password changed") {
		t.Fatalf("reset: %d", code)
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", r.Token); c != 401 {
		t.Fatal("Desktop token survived the reset")
	}
	if code, _, _ := old.do("GET", e.base+"/account", nil, nil); code != 303 {
		t.Fatal("web session survived the reset")
	}
	if code, _, _ := b.do("POST", e.base+"/reset", f, nil); code != 404 {
		t.Fatal("reset link reused")
	}
	if c, _ := e.login(t, opEmail, "a fresh passphrase 99"); c != 200 {
		t.Fatal("new password refused")
	}
	// Expiry: one hour.
	secret, _ := e.store.NewActionToken(ctx, op.ID, accounts.ActionReset, "", resetTTL)
	e.clk.add(61 * time.Minute)
	if code, _, _ := b.do("GET", e.base+"/reset?token="+secret, nil, nil); code != 404 {
		t.Fatal("expired reset link accepted")
	}

	// Without SMTP the page sends people to the administrator.
	e2 := newAcctEnv(t, false, nil)
	if code, page, _ := newBrowser(t).do("GET", e2.base+"/forgot", nil, nil); code != 200 || !strings.Contains(page, "contact the administrator") || strings.Contains(page, `name="email"`) {
		t.Fatalf("forgot without SMTP: %d", code)
	}
}

func dumpDB(t *testing.T, st *accounts.Store) string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT hash || '|' || new_email FROM action_tokens UNION ALL SELECT hash FROM web_sessions UNION ALL SELECT hash FROM tokens`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		b.WriteString(s + "\n")
	}
	return b.String()
}

func TestOperatorPages(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()
	b1, b2 := newBrowser(t), newBrowser(t)
	e.webLogin(t, b1, opEmail, opPass, nil)
	e.webLogin(t, b2, opEmail, opPass, nil)
	_, r1 := e.login(t, opEmail, opPass)

	// A session through the hub shows up in the overview once ended.
	if _, _, os1, err := connectPair(t, e.testEnv, tokenOp(e.testEnv, r1.Token)); err != nil {
		t.Fatal(err)
	} else {
		os1.Close()
	}
	waitFor(t, func() bool { s, _ := e.store.Sessions(ctx, op.ID, 5, 0); return len(s) == 1 })
	code, page, _ := b1.do("GET", e.base+"/account", nil, nil)
	if code != 200 || !strings.Contains(page, "Recent sessions") || !strings.Contains(page, "direct") || !strings.Contains(page, `<svg viewBox="0 0 480 200"`) {
		t.Fatalf("overview: %d", code)
	}

	// Display name.
	if code, _, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "name", "name", "<script>"); code != 422 {
		t.Fatal("bad name accepted")
	}
	if code, _, h := e.post(t, b1, "/account/settings", "/account/settings", "op", "name", "name", "Op Renamed"); code != 303 || loc(h) != "/account/settings?done=name" {
		t.Fatal("name")
	}
	if mustByEmail(t, e, opEmail).Name != "Op Renamed" {
		t.Fatal("name not saved")
	}
	if _, page, _ := b1.do("GET", e.base+"/account/settings?done=name", nil, nil); !strings.Contains(page, "Display name saved.") {
		t.Fatal("flash")
	}

	// Password: wrong current, mismatch, then success. Other browsers end,
	// this one stays, the Desktop login stays unless asked.
	if code, page, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "password", "current", "wrong one here", "password", "next passphrase 77", "confirm", "next passphrase 77"); code != 422 || !strings.Contains(page, "current password is incorrect") {
		t.Fatalf("wrong current: %d", code)
	}
	if code, _, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "password", "current", opPass, "password", "next passphrase 77", "confirm", "other passphrase 77"); code != 422 {
		t.Fatal("mismatch accepted")
	}
	if code, _, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "password", "current", opPass, "password", "next passphrase 77", "confirm", "next passphrase 77"); code != 303 {
		t.Fatal("password change")
	}
	if code, _, _ := b1.do("GET", e.base+"/account", nil, nil); code != 200 {
		t.Fatal("current browser signed out by its own password change")
	}
	if code, _, _ := b2.do("GET", e.base+"/account", nil, nil); code != 303 {
		t.Fatal("other browser still signed in")
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", r1.Token); c != 200 {
		t.Fatal("Desktop login revoked without the checkbox")
	}
	if code, _, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "password", "current", "next passphrase 77", "password", "third passphrase 77", "confirm", "third passphrase 77", "revoke_tokens", "on"); code != 303 {
		t.Fatal("password change 2")
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", r1.Token); c != 401 {
		t.Fatal("Desktop login kept despite the checkbox")
	}
	pw := "third passphrase 77"

	// Email change without SMTP: immediate, unverified, taken address refused.
	e.mkAccount(t, "taken@example.com", "Taken", opPass, false)
	if code, page, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "email", "email", "taken@example.com", "password", pw); code != 422 || !strings.Contains(page, "cannot be used") {
		t.Fatalf("taken email: %d", code)
	}
	if code, _, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "email", "email", "renamed@example.com", "password", "bad password x"); code != 422 {
		t.Fatal("email change without the password")
	}
	if code, _, _ := e.post(t, b1, "/account/settings", "/account/settings", "op", "email", "email", "renamed@example.com", "password", pw); code != 303 {
		t.Fatal("email change")
	}
	if a := mustByEmail(t, e, "renamed@example.com"); a.Verified || a.ID != op.ID {
		t.Fatalf("email change result %+v", a)
	}

	// Desktop logins: listed with client string, revoked one by one.
	_, r2 := e.login(t, "renamed@example.com", pw)
	_, r3 := e.login(t, "renamed@example.com", pw)
	code, page, _ = b1.do("GET", e.base+"/account/security", nil, nil)
	if code != 200 || strings.Count(page, "desktop/1.0.34") != 2 {
		t.Fatalf("tokens listed: %d", code)
	}
	toks, _ := e.store.Tokens(ctx, op.ID)
	if code, _, _ := e.post(t, b1, "/account/security", "/account/security", "op", "revoke_token", "id", fmt.Sprint(toks[0].ID)); code != 303 {
		t.Fatal("revoke one")
	}
	c2, _ := e.api(t, "GET", "/v1/account/me", r2.Token)
	c3, _ := e.api(t, "GET", "/v1/account/me", r3.Token)
	if c2+c3 != 401+200 {
		t.Fatalf("revoke one: %d %d", c2, c3)
	}
	// Another account's token id cannot be revoked from here.
	other := mustByEmail(t, e, "taken@example.com")
	_, ro := e.login(t, "taken@example.com", opPass)
	otoks, _ := e.store.Tokens(ctx, other.ID)
	e.post(t, b1, "/account/security", "/account/security", "op", "revoke_token", "id", fmt.Sprint(otoks[0].ID))
	if c, _ := e.api(t, "GET", "/v1/account/me", ro.Token); c != 200 {
		t.Fatal("revoked another account's login")
	}

	// Log out everywhere.
	b3 := newBrowser(t)
	e.webLogin(t, b3, "renamed@example.com", pw, nil)
	if code, _, h := e.post(t, b1, "/account/security", "/account/security", "op", "logout_all"); code != 303 || loc(h) != "/login?out=1" {
		t.Fatal("logout_all")
	}
	for _, b := range []*browser{b1, b3} {
		if code, _, _ := b.do("GET", e.base+"/account", nil, nil); code != 303 {
			t.Fatal("browser survived logout_all")
		}
	}
	if c2+c3 == 0 {
		t.Fatal()
	}
	if toks, _ := e.store.Tokens(ctx, op.ID); len(toks) != 0 {
		t.Fatal("Desktop logins survived logout_all")
	}

	// Delete my account: password and typed address.
	e.webLogin(t, b1, "renamed@example.com", pw, nil)
	if code, page, _ := e.post(t, b1, "/account/security", "/account/security", "op", "delete", "password", pw, "confirm", "nope@example.com"); code != 422 || !strings.Contains(page, "does not match") {
		t.Fatalf("delete mismatch: %d", code)
	}
	if code, page, _ := e.post(t, b1, "/account/security", "/account/security", "op", "delete", "password", pw, "confirm", "renamed@example.com"); code != 200 || !strings.Contains(page, "Account deleted") {
		t.Fatalf("delete: %d", code)
	}
	if _, err := e.store.ByID(ctx, op.ID); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatal("account not deleted")
	}
	if s, _ := e.store.Sessions(ctx, op.ID, 5, 0); len(s) != 0 {
		t.Fatal("history of a deleted account kept")
	}
}

func TestEmailChangeWithSMTP(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	b := newBrowser(t)
	e.webLogin(t, b, opEmail, opPass, nil)
	if code, _, h := e.post(t, b, "/account/settings", "/account/settings", "op", "email", "email", "moved@example.com", "password", opPass); code != 303 || loc(h) != "/account/settings?done=email_sent" {
		t.Fatal("email change request")
	}
	if mustByEmail(t, e, opEmail).ID != op.ID {
		t.Fatal("address changed before confirmation")
	}
	var link string
	for i := 0; i < 2; i++ {
		m := e.sink.Wait(t, 5*time.Second)
		if m.To[0] == "moved@example.com" {
			link = regexp.MustCompile(`https?://\S+/confirm-email\?token=[A-Za-z0-9_%-]+`).FindString(m.Data)
		} else if m.To[0] != opEmail {
			t.Fatalf("mail to %v", m.To)
		}
	}
	nb := newBrowser(t)
	code, page, _ := nb.do("GET", link, nil, nil)
	if code != 200 || !strings.Contains(page, "moved@example.com") {
		t.Fatalf("confirm page: %d", code)
	}
	if code, _, _ := nb.do("POST", e.base+"/confirm-email", fields(page), nil); code != 200 {
		t.Fatal("confirm")
	}
	if a := mustByEmail(t, e, "moved@example.com"); a.ID != op.ID || !a.Verified {
		t.Fatalf("after confirm %+v", a)
	}
}

func TestAdminLiveView(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) {
		c.OperatorKeys = []OperatorKey{{Label: "helpdesk", Secret: testKey}}
	})
	ab, admin, _ := e.adminBrowser(t)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	_, r := e.login(t, opEmail, opPass)

	hr, hs, os1, err := connectPair(t, e.testEnv, tokenOp(e.testEnv, r.Token))
	if err != nil {
		t.Fatal(err)
	}
	hr2, hs2, os2, err := connectPair(t, e.testEnv, opConfig(e.testEnv, loopback, nil))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		v := e.srv.hub.live()
		return v.Accepted == 2 && v.Direct == 2
	})
	full1, full2 := hr.h.ID(), hr2.h.ID()
	code, page, _ := ab.do("GET", e.base+"/admin/live", nil, nil)
	if code != 200 || !strings.Contains(page, opEmail) || !strings.Contains(page, "helpdesk") || !strings.Contains(page, full1[:3]+"******") ||
		!strings.Contains(page, `http-equiv="refresh"`) || strings.Count(page, "connected") < 2 {
		t.Fatalf("live: %d", code)
	}
	if strings.Contains(page, full1) || strings.Contains(page, full2) || strings.Contains(page, hr.h.Code()) {
		t.Fatal("full host ID or code on the live page")
	}
	// Dashboard counters and accounts list (online, in session with).
	code, page, _ = ab.do("GET", e.base+"/admin", nil, nil)
	if code != 200 || !regexp.MustCompile(`Sessions connected</span><b>2</b>`).MatchString(page) || !regexp.MustCompile(`Computers online</span><b>2</b>`).MatchString(page) {
		t.Fatalf("dashboard: %d", code)
	}
	code, page, _ = ab.do("GET", e.base+"/admin/accounts", nil, nil)
	if code != 200 || !strings.Contains(page, ">online<") || !strings.Contains(page, full1[:3]+"******") {
		t.Fatalf("accounts list: %d", code)
	}

	// Terminate the account's session from the live page.
	v := e.srv.hub.live()
	var handle string
	for _, s := range v.Sessions {
		if s.AccountID == op.ID {
			handle = s.Handle
		}
	}
	code, page, _ = e.post(t, ab, "/admin/live", "/admin/live", "handle", handle)
	if code != 200 || !strings.Contains(page, "Confirm this action") {
		t.Fatalf("terminate confirm: %d", code)
	}
	if code, _, h := ab.do("POST", e.base+"/admin/live", fields(page), nil); code != 303 || loc(h) != "/admin/live?done=terminated" {
		t.Fatalf("terminate: %d %q", code, loc(h))
	}
	for _, s := range []*sl.Session{os1, hs} {
		select {
		case <-s.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("session not closed after terminate")
		}
	}
	waitFor(t, func() bool { return e.srv.hub.live().Accepted == 1 })
	ctx := context.Background()
	waitFor(t, func() bool { s, _ := e.store.Sessions(ctx, op.ID, 5, 0); return len(s) == 1 })
	hist, _ := e.store.Sessions(ctx, op.ID, 5, 0)
	if hist[0].EndReason != "admin_terminated" || hist[0].Path != "direct" || hist[0].HostTID != full1[:3]+"******" || hist[0].Accepted.IsZero() {
		t.Fatalf("history %+v", hist[0])
	}
	entries, _, _ := e.store.AuditLog(ctx, 5, 0)
	if entries[0].Action != "terminate_session" || entries[0].ActorID != admin.ID || entries[0].TargetID != op.ID {
		t.Fatalf("audit %+v", entries[0])
	}
	// The static key session ends normally and is recorded by its label.
	os2.Close()
	<-hs2.Done()
	waitFor(t, func() bool { s, _ := e.store.Sessions(ctx, 0, 5, 0); return len(s) == 2 })
	all, _ := e.store.Sessions(ctx, 0, 5, 0)
	if all[0].KeyLabel != "helpdesk" || all[0].AccountID != 0 {
		t.Fatalf("static key history %+v", all[0])
	}
	code, page, _ = ab.do("GET", e.base+"/admin/history", nil, nil)
	if code != 200 || !strings.Contains(page, "helpdesk") || !strings.Contains(page, "ended by an admin") || strings.Contains(page, full1) {
		t.Fatalf("history page: %d", code)
	}
	// The operator sees its own history, not the other one.
	ob := newBrowser(t)
	e.webLogin(t, ob, opEmail, opPass, nil)
	code, page, _ = ob.do("GET", e.base+"/account", nil, nil)
	if code != 200 || !strings.Contains(page, "ended by an admin") || strings.Contains(page, full2[:3]+"******") && full2[:3] != full1[:3] {
		t.Fatalf("operator history: %d", code)
	}
	// Gone handle.
	f := e.csrf(t, ab, "/admin/live")
	f.Set("handle", handle)
	if code, _, h := ab.do("POST", e.base+"/admin/live", f, nil); code != 303 || loc(h) != "/admin/live?done=gone" {
		t.Fatalf("gone: %q", loc(h))
	}
}

func TestIPModeRendering(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want *regexp.Regexp
	}{
		{IPModeFull, regexp.MustCompile(`203\.0\.113\.77`)},
		{IPModeTruncated, regexp.MustCompile(`203\.0\.113\.0/24`)},
		{IPModeHMAC, regexp.MustCompile(`h:[0-9a-f]{16}`)},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			e := newAcctEnv(t, false, func(c *Config) {
				c.LogIPMode, c.TrustProxyHeaders, c.AdminTOTPOptional = tc.mode, true, true
			})
			admin := e.mkAccount(t, adminEmail, "The Boss", adminPass, true)
			xff := map[string]string{"X-Forwarded-For": "203.0.113.77"}
			b := newBrowser(t)
			_, page, _ := b.do("GET", e.base+"/login", nil, xff)
			f := hidden(page)
			f.Set("email", adminEmail)
			f.Set("password", adminPass)
			if code, _, _ := b.do("POST", e.base+"/login", f, xff); code != 303 {
				t.Fatal("login")
			}
			code, page, _ := b.do("GET", e.base+fmt.Sprintf("/admin/accounts/%d", admin.ID), nil, xff)
			if code != 200 || !tc.want.MatchString(page) {
				t.Fatalf("%s: ip not rendered", tc.mode)
			}
			if tc.mode != IPModeFull && strings.Contains(page, "203.0.113.77") {
				t.Fatalf("%s: full IP shown", tc.mode)
			}
			if tc.mode != IPModeFull && strings.Contains(dumpAll(t, e.store), "203.0.113.77") {
				t.Fatalf("%s: full IP stored", tc.mode)
			}
		})
	}
}

func dumpAll(t *testing.T, st *accounts.Store) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{`SELECT last_login_ip FROM accounts`, `SELECT ip FROM web_sessions`, `SELECT ip FROM login_history`, `SELECT last_ip FROM tokens`, `SELECT ip FROM audit_log`} {
		rows, err := st.DB().Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			b.WriteString(s + "\n")
		}
		rows.Close()
	}
	return b.String()
}

func TestAdminEmailsBootstrap(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.AdminEmails = []string{"Boss@Example.com"}; c.AdminTOTPOptional = true })
	// Unverified (registered without SMTP): never promoted by the list.
	uv, _ := e.store.Create(context.Background(), adminEmail, "Pretender", accounts.HashPassword(adminPass), false)
	b := newBrowser(t)
	if code, _, h := e.webLogin(t, b, adminEmail, adminPass, nil); code != 303 || loc(h) != "/account" {
		t.Fatalf("unverified listed address: %q", loc(h))
	}
	if mustByEmail(t, e, adminEmail).IsAdmin() {
		t.Fatal("unverified account promoted")
	}
	// Once verified (admin create, or an admin's verify), the list applies.
	_ = e.store.SetVerified(context.Background(), uv.ID)
	b2 := newBrowser(t)
	if code, _, h := e.webLogin(t, b2, adminEmail, adminPass, nil); code != 303 || loc(h) != "/admin" {
		t.Fatalf("listed admin: %q", loc(h))
	}
	// Its demotion from the pages is refused.
	other := e.mkAccount(t, "second@example.com", "Second", adminPass, true)
	sb := newBrowser(t)
	e.webLogin(t, sb, "second@example.com", adminPass, nil)
	if code, page, _ := e.adminDo(t, sb, uv.ID, "demote"); code != 409 || !strings.Contains(page, "RDV_ADMIN_EMAILS") {
		t.Fatalf("env admin demote: %d", code)
	}
	_ = other
}

func TestPagesCSPAndI18n(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.AdminTOTPOptional = true })
	e.mkAccount(t, adminEmail, "The Boss", adminPass, true)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	sum := sha256.Sum256([]byte(pageCSS))
	wantCSP := "style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	for _, lang := range []string{"en", "fr"} {
		b := newBrowser(t)
		b.lang = lang
		e.webLogin(t, b, adminEmail, adminPass, nil)
		for _, p := range []string{"/", "/login", "/forgot", "/account", "/account/settings", "/account/security", "/account/2fa",
			"/admin", "/admin/accounts", fmt.Sprintf("/admin/accounts/%d", op.ID), "/admin/live", "/admin/history", "/admin/audit"} {
			code, page, h := b.do("GET", e.base+p, nil, nil)
			if code == 303 {
				continue
			}
			if code != 200 {
				t.Fatalf("%s %s: %d", lang, p, code)
			}
			csp := h.Get("Content-Security-Policy")
			if !strings.Contains(csp, wantCSP) || strings.Contains(csp, "script-src") || !strings.Contains(csp, "default-src 'none'") {
				t.Fatalf("%s: CSP %q", p, csp)
			}
			if strings.Contains(page, "<script") || strings.Contains(page, " style=") || strings.Contains(page, "%!") || strings.Contains(page, "http://") && !strings.Contains(page, "http://www.w3.org/2000/svg") {
				t.Fatalf("%s %s: script, style attribute, bad format or external URL", lang, p)
			}
			if h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" {
				t.Fatalf("%s: headers", p)
			}
		}
	}
	// Every English key has a French text (and the reverse).
	for k := range webText["en"] {
		if webText["fr"][k] == "" {
			t.Errorf("fr misses %q", k)
		}
	}
	for k := range webText["fr"] {
		if webText["en"][k] == "" {
			t.Errorf("en misses %q", k)
		}
	}
	// Every key referenced by a template exists.
	keyRe := regexp.MustCompile(`(?:index|tf) \$?[A-Za-z.]*T "([a-z0-9_.]+)"`)
	all := layoutTmpl + partialsTmpl
	for _, b := range pageBodies {
		all += b
	}
	for _, m := range keyRe.FindAllStringSubmatch(all, -1) {
		if _, ok := webText["en"][m[1]]; !ok {
			t.Errorf("template key %q has no text", m[1])
		}
	}
}

func TestHistoryRetentionPurge(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.HistoryRetention = 2 * 24 * time.Hour; c.AuditRetention = 5 * 24 * time.Hour })
	ctx := context.Background()
	op := e.mkAccount(t, opEmail, "Op", opPass, false)
	now := e.clk.now()
	_ = e.store.AddSessionRecord(ctx, accounts.SessionRecord{AccountID: op.ID, HostTID: "123******", Started: now, Ended: now, Path: "direct", EndReason: "bye"})
	_ = e.store.AddLogin(ctx, op.ID, accounts.LoginWeb, true, "", "1.2.3.4")
	_ = e.store.Audit(ctx, accounts.AuditEntry{Actor: "cli", Action: "cli_promote", TargetID: op.ID, Target: opEmail})
	secret, _ := e.store.NewWebSession(ctx, op.ID, "", false)
	e.clk.add(3 * 24 * time.Hour)
	_, _, _ = e.store.IssueToken(ctx, op.ID) // keeps the account active
	if _, err := e.store.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.store.Sessions(ctx, op.ID, 5, 0); len(s) != 0 {
		t.Fatal("session history kept past the retention")
	}
	if l, _ := e.store.Logins(ctx, op.ID, 5); len(l) != 0 {
		t.Fatalf("login history kept past the retention: %d", len(l))
	}
	if _, total, _ := e.store.AuditLog(ctx, 5, 0); total != 1 {
		t.Fatal("audit purged before its own retention")
	}
	if _, _, err := e.store.ResolveWebSession(ctx, secret); err == nil {
		t.Fatal("web session past its absolute lifetime")
	}
	e.clk.add(3 * 24 * time.Hour)
	_, _ = e.store.Purge(ctx)
	if _, total, _ := e.store.AuditLog(ctx, 5, 0); total != 0 {
		t.Fatal("audit kept past its retention")
	}
}

func TestWebSessionIdle(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	e.mkAccount(t, opEmail, "Op", opPass, false)
	b := newBrowser(t)
	e.webLogin(t, b, opEmail, opPass, nil)
	e.clk.add(11 * time.Hour)
	if code, _, _ := b.do("GET", e.base+"/account", nil, nil); code != 200 {
		t.Fatal("signed out before the idle limit")
	}
	e.clk.add(11 * time.Hour) // 11 h since the last request
	if code, _, _ := b.do("GET", e.base+"/account", nil, nil); code != 200 {
		t.Fatal("idle timer not refreshed by use")
	}
	e.clk.add(13 * time.Hour)
	if code, _, _ := b.do("GET", e.base+"/account", nil, nil); code != 303 {
		t.Fatal("idle session still valid")
	}
}

func TestSecEventsWindow(t *testing.T) {
	var e secEvents
	now := time.Now()
	e.pakeFails = e.add(e.pakeFails, now.Add(-25*time.Hour))
	e.pakeFails = e.add(e.pakeFails, now.Add(-time.Hour))
	e.bans = e.add(e.bans, now)
	e.gc(now)
	if len(e.pakeFails) != 1 || len(e.bans) != 1 || len(e.idLocks) != 0 {
		t.Fatalf("window: %d %d %d", len(e.pakeFails), len(e.bans), len(e.idLocks))
	}
	for i := 0; i < secMax+10; i++ {
		e.idLocks = e.add(e.idLocks, now)
	}
	if len(e.idLocks) != secMax {
		t.Fatal("unbounded")
	}
}
