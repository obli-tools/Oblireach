package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	sl "github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
	"github.com/oblireach/rdv/internal/smtptest"
)

const (
	acctEmail = "jane.doe@example.com"
	acctName  = "Jane Doe"
	acctPass  = "correct horse battery staple"
)

type acctEnv struct {
	*testEnv
	base  string // http://127.0.0.1:port
	store *accounts.Store
	sink  *smtptest.Sink // nil without SMTP
	clk   *testClock
}

type testClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

// newAcctEnv starts a public instance with accounts (and SMTP when mail).
func newAcctEnv(t *testing.T, mail bool, mut func(*Config)) *acctEnv {
	t.Helper()
	st, err := accounts.Open(filepath.Join(t.TempDir(), accounts.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{}
	st.SetClock(clk.now)
	e := &acctEnv{store: st, clk: clk}
	// The public URL is only known once the HTTP server listens: a
	// handler indirection lets the config carry it.
	var h http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	e.base = ts.URL
	if mail {
		e.sink = smtptest.Start(t, nil)
	}
	logs := &syncBuf{}
	cfg := testConfig(logs)
	cfg.Mode, cfg.OperatorKeys, cfg.Accounts, cfg.PublicURL = ModePublic, nil, st, ts.URL
	cfg.FormMinFill = -1
	// The whole suite comes from 127.0.0.1.
	cfg.Limits.SignupPerHourIP, cfg.Limits.SignupBurstIP = 60000, 1000
	cfg.Limits.LoginPerMinIP, cfg.Limits.LoginBurstIP = 6000, 1000
	cfg.Limits.VerifyPerMinIP, cfg.Limits.VerifyBurstIP = 6000, 1000
	cfg.Limits.ReportPerHourIP, cfg.Limits.ReportBurstIP = 60000, 1000
	if mail {
		m, err := accounts.NewSMTPMailer(accounts.SMTPConfig{Host: "127.0.0.1", Port: e.sink.Port(), From: "Oblireach <noreply@example.com>", TLS: accounts.SMTPNone})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Mailer = m
	}
	if mut != nil {
		mut(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartTURN(); err != nil {
		t.Fatal(err)
	}
	h = s.Handler()
	as := httptest.NewServer(s.AdminHandler())
	t.Cleanup(func() {
		s.Close()
		ts.Close()
		as.Close()
		st.Close()
	})
	e.testEnv = &testEnv{srv: s, url: "ws://" + ts.Listener.Addr().String() + wire.Path, admin: as.URL, logs: logs}
	return e
}

// browser is a cookie-keeping HTTP client.
type browser struct {
	t    *testing.T
	c    *http.Client
	lang string
}

func newBrowser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, lang: "en"}
}

func (b *browser) do(method, u string, form url.Values, hdr map[string]string) (int, string, http.Header) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, u, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept-Language", b.lang)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

var hiddenRe = regexp.MustCompile(`name="(csrf|ts|token)" value="([^"]*)"`)

func hidden(page string) url.Values {
	v := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		v.Set(m[1], strings.ReplaceAll(m[2], "&#43;", "+"))
	}
	return v
}

func regForm(page, name, email, pw, confirm string) url.Values {
	f := hidden(page)
	f.Set("name", name)
	f.Set("email", email)
	f.Set("password", pw)
	f.Set("confirm", confirm)
	f.Set("terms", "on")
	return f
}

// register submits the registration form and returns the result page.
func (e *acctEnv) register(t *testing.T, b *browser, name, email, pw string) (int, string) {
	t.Helper()
	code, page, _ := b.do("GET", e.base+"/register", nil, nil)
	if code != 200 || !strings.Contains(page, `name="csrf"`) {
		t.Fatalf("register form: %d", code)
	}
	code, page, _ = b.do("POST", e.base+"/register", regForm(page, name, email, pw, pw), nil)
	return code, page
}

var linkRe = regexp.MustCompile(`https?://[^\s]+/verify\?token=[A-Za-z0-9_%-]+`)

// verifyMail takes the verification link from the next mail and confirms.
func (e *acctEnv) verifyMail(t *testing.T, b *browser, to string) {
	t.Helper()
	msg := e.sink.Wait(t, 5*time.Second)
	if len(msg.To) != 1 || msg.To[0] != to {
		t.Fatalf("mail to %v", msg.To)
	}
	link := linkRe.FindString(msg.Data)
	if link == "" || !strings.HasPrefix(link, e.base+"/verify?token=") {
		t.Fatalf("no verification link in %q", msg.Data)
	}
	code, page, _ := b.do("GET", link, nil, nil)
	if code != 200 || !strings.Contains(page, "Confirm my address") {
		t.Fatalf("verify page: %d %s", code, page)
	}
	code, page, _ = b.do("POST", e.base+"/verify", hidden(page), nil)
	if code != 200 || !strings.Contains(page, "Address confirmed") {
		t.Fatalf("verify: %d %s", code, page)
	}
}

type loginResp struct {
	Token      string `json:"token"`
	ExpiresAt  string `json:"expiresAt"`
	Error      string `json:"error"`
	RetryAfter int    `json:"retryAfter"`
	Account    struct {
		ID       int64  `json:"id"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	} `json:"account"`
}

func (e *acctEnv) login(t *testing.T, email, pw string) (int, loginResp) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": pw, "client": "desktop/1.0.34"})
	resp, err := http.Post(e.base+"/v1/account/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r loginResp
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return resp.StatusCode, r
}

func (e *acctEnv) api(t *testing.T, method, path, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, e.base+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

// tokenOp is an operator configured with an account token instead of a key.
func tokenOp(e *testEnv, token string) sl.OperatorConfig {
	cfg := opConfig(e, loopback, nil)
	cfg.Key, cfg.Token = "", token
	return cfg
}

// connectPair connects an operator to a fresh host; it returns the host
// recorder and both sessions, or the operator's error.
func connectPair(t *testing.T, e *testEnv, op sl.OperatorConfig) (*hostRec, *sl.Session, *sl.Session, error) {
	t.Helper()
	hr := startHost(t, e, op.ICE, allow, nil)
	hc := make(chan *sl.Session, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, _ := hr.h.WaitForOperator(ctx)
		hc <- s
	}()
	os, err := connect(t, op, hr.h.ID(), hr.h.Code())
	if err != nil {
		return hr, nil, nil, err
	}
	hs := <-hc
	if hs == nil {
		t.Fatal("host side failed")
	}
	t.Cleanup(func() { os.Close(); hs.Close() })
	return hr, hs, os, nil
}

func TestAccountFlowVerified(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	b := newBrowser(t)
	code, page := e.register(t, b, acctName, acctEmail, acctPass)
	if code != 200 || !strings.Contains(page, "Check your inbox") {
		t.Fatalf("register: %d", code)
	}
	// Before the verification: right password -> 403, wrong -> 401.
	if c, r := e.login(t, acctEmail, acctPass); c != 403 || r.Error != "email_not_verified" {
		t.Fatalf("unverified login: %d %+v", c, r)
	}
	if c, r := e.login(t, acctEmail, "not the password at all"); c != 401 || r.Error != "invalid_credentials" {
		t.Fatalf("wrong password: %d %+v", c, r)
	}
	e.verifyMail(t, b, acctEmail)

	c, r := e.login(t, " Jane.Doe@Example.com ", acctPass)
	if c != 200 || r.Token == "" || !r.Account.Verified || r.Account.Email != acctEmail || r.Account.Name != acctName || r.Account.ID == 0 {
		t.Fatalf("login: %d %+v", c, r)
	}
	exp, err := time.Parse(time.RFC3339, r.ExpiresAt)
	if err != nil || exp.Sub(time.Now()) < 29*24*time.Hour {
		t.Fatalf("expiresAt %q", r.ExpiresAt)
	}
	if c, m := e.api(t, "GET", "/v1/account/me", r.Token); c != 200 || m["account"].(map[string]any)["email"] != acctEmail {
		t.Fatalf("me: %d %v", c, m)
	}

	// The operator connects with the token; the host sees the verified
	// account, separate from the self-declared name.
	hr, hs, os, err := connectPair(t, e.testEnv, tokenOp(e.testEnv, r.Token))
	if err != nil {
		t.Fatal(err)
	}
	hr.mu.Lock()
	req := hr.reqs[0]
	hr.mu.Unlock()
	if req.Account != "Jane Doe <jane.doe@example.com>" || req.OperatorName != testName || hs.Peer().Account != req.Account {
		t.Fatalf("approval %+v", req)
	}
	os.Close()
	waitFor(t, func() bool { return e.metric(t, `rdv_session_end_total{outcome="bye"}`) == "1" })
	if logs := e.logs.String(); strings.Contains(logs, acctEmail) || strings.Contains(logs, acctName) || strings.Contains(logs, r.Token) {
		t.Fatal("email, name or token reached the logs")
	} else if !strings.Contains(logs, fmt.Sprintf(`"account":"acct:%d"`, r.Account.ID)) {
		t.Fatal("account id not logged")
	}

	// Logout revokes the token: me 401, connect -> ErrAuthExpired.
	if c, _ := e.api(t, "POST", "/v1/account/logout", r.Token); c != 204 {
		t.Fatalf("logout %d", c)
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", r.Token); c != 401 {
		t.Fatalf("me after logout %d", c)
	}
	hr2 := startHost(t, e.testEnv, loopback, allow, nil)
	_, err = connect(t, tokenOp(e.testEnv, r.Token), hr2.h.ID(), hr2.h.Code())
	if !errors.Is(err, sl.ErrAuthExpired) || !errors.Is(err, sl.ErrOperatorRequired) {
		t.Fatalf("revoked token: %v", err)
	}
	if e.metric(t, "rdv_pake_failed_total") != "0" || e.metric(t, "rdv_ip_banned_total") != "0" {
		t.Fatal("an expired login is not a code attempt")
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", ""); c != 401 {
		t.Fatal("me without token")
	}
}

func TestRegisterEnumerationSafe(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	b := newBrowser(t)
	_, first := e.register(t, b, acctName, acctEmail, acctPass)
	e.verifyMail(t, b, acctEmail)

	// Same address again (verified account) and a fresh address: same
	// status, same page; the owner of the existing account gets a notice.
	c1, p1 := e.register(t, newBrowser(t), "Someone", acctEmail, "another fine passphrase")
	c2, p2 := e.register(t, newBrowser(t), "Someone", "fresh@example.com", "another fine passphrase")
	if c1 != 200 || c1 != c2 || p1 != p2 || p1 != first {
		t.Fatalf("answers differ: %d/%d", c1, c2)
	}
	got := map[string]string{}
	for i := 0; i < 2; i++ {
		m := e.sink.Wait(t, 5*time.Second)
		got[m.To[0]] = m.Data
	}
	if !strings.Contains(got[acctEmail], "already has one") || strings.Contains(got[acctEmail], "token=") {
		t.Fatalf("notice mail: %q", got[acctEmail])
	}
	if !linkRe.MatchString(got["fresh@example.com"]) {
		t.Fatal("no link for the fresh address")
	}
	// The existing account keeps its password.
	if c, _ := e.login(t, acctEmail, acctPass); c != 200 {
		t.Fatalf("existing account changed: %d", c)
	}
	// Login: unknown address and wrong password answer alike.
	c3, r3 := e.login(t, "nobody@example.com", "whatever password")
	c4, r4 := e.login(t, acctEmail, "whatever password")
	if c3 != 401 || c3 != c4 || r3.Error != r4.Error {
		t.Fatalf("login answers differ: %d %+v / %d %+v", c3, r3, c4, r4)
	}
	// Mails to one address are capped (3 per hour by default).
	for i := 0; i < 3; i++ {
		e.register(t, newBrowser(t), "Someone", "fresh@example.com", "another fine passphrase")
	}
	for i := 0; i < 2; i++ {
		e.sink.Wait(t, 5*time.Second)
	}
	e.sink.None(t, 500*time.Millisecond)
}

func TestRegisterWithoutSMTP(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	b := newBrowser(t)
	code, page := e.register(t, b, acctName, acctEmail, acctPass)
	if code != 200 || !strings.Contains(page, "Your account is ready") || !strings.Contains(page, "unverified") {
		t.Fatalf("register: %d", code)
	}
	c, r := e.login(t, acctEmail, acctPass)
	if c != 200 || r.Account.Verified {
		t.Fatalf("login %d %+v", c, r)
	}
	// Taken address: said plainly (nothing to protect without verification).
	code, page = e.register(t, newBrowser(t), "Other", acctEmail, "another fine passphrase")
	if code != 422 || !strings.Contains(page, "cannot be used") {
		t.Fatalf("duplicate: %d", code)
	}
	// The host sees the address marked as unverified.
	hr, _, _, err := connectPair(t, e.testEnv, tokenOp(e.testEnv, r.Token))
	if err != nil {
		t.Fatal(err)
	}
	hr.mu.Lock()
	defer hr.mu.Unlock()
	if hr.reqs[0].Account != "Jane Doe <jane.doe@example.com> (unverified)" {
		t.Fatalf("label %q", hr.reqs[0].Account)
	}
	// /verify does not exist without SMTP.
	if c, _, _ := b.do("GET", e.base+"/verify?token=x", nil, nil); c != 404 {
		t.Fatalf("verify without SMTP: %d", c)
	}
}

func TestCSRFAndAntiBot(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	b := newBrowser(t)
	_, page, _ := b.do("GET", e.base+"/register", nil, nil)
	good := regForm(page, acctName, acctEmail, acctPass, acctPass)

	// No cookie (another browser), wrong token, cross-site origin.
	if c, p, _ := newBrowser(t).do("POST", e.base+"/register", good, nil); c != 403 || !strings.Contains(p, "expired") {
		t.Fatalf("no cookie: %d", c)
	}
	bad := regForm(page, acctName, acctEmail, acctPass, acctPass)
	bad.Set("csrf", "AAAA"+bad.Get("csrf")[4:])
	if c, _, _ := b.do("POST", e.base+"/register", bad, nil); c != 403 {
		t.Fatalf("wrong token: %d", c)
	}
	_, page, _ = b.do("GET", e.base+"/register", nil, nil)
	good = regForm(page, acctName, acctEmail, acctPass, acctPass)
	if c, _, _ := b.do("POST", e.base+"/register", good, map[string]string{"Origin": "https://evil.example"}); c != 403 {
		t.Fatalf("cross origin: %d", c)
	}
	_, page, _ = b.do("GET", e.base+"/register", nil, nil)
	tampered := regForm(page, acctName, acctEmail, acctPass, acctPass)
	tampered.Set("ts", "1."+strings.SplitN(tampered.Get("ts"), ".", 2)[1])
	if c, _, _ := b.do("POST", e.base+"/register", tampered, nil); c != 403 {
		t.Fatalf("tampered stamp: %d", c)
	}

	// Honeypot: answered like a success, nothing done.
	_, page, _ = b.do("GET", e.base+"/register", nil, nil)
	hp := regForm(page, acctName, acctEmail, acctPass, acctPass)
	hp.Set("website", "http://spam.example")
	if c, p, _ := b.do("POST", e.base+"/register", hp, nil); c != 200 || !strings.Contains(p, "Check your inbox") {
		t.Fatalf("honeypot: %d", c)
	}
	e.sink.None(t, 300*time.Millisecond)
	if hs, _ := e.store.PendingHashes(context.Background(), acctEmail); len(hs) != 0 {
		t.Fatal("bot registration recorded")
	}

	// Verify POST without the form's token is refused too.
	if c, _, _ := newBrowser(t).do("POST", e.base+"/verify", url.Values{"token": {"x"}}, nil); c != 403 {
		t.Fatalf("verify csrf: %d", c)
	}
}

func TestFormMinFill(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.FormMinFill = 2 * time.Second })
	b := newBrowser(t)
	code, page := e.register(t, b, acctName, acctEmail, acctPass) // submitted at once
	if code != 200 || !strings.Contains(page, "ready") {
		t.Fatalf("fast submit: %d", code)
	}
	if _, _, err := e.store.ByEmail(context.Background(), acctEmail); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatal("a form filled in less than the minimum time created an account")
	}
	_, page, _ = b.do("GET", e.base+"/register", nil, nil)
	time.Sleep(2100 * time.Millisecond)
	if c, _, _ := b.do("POST", e.base+"/register", regForm(page, acctName, acctEmail, acctPass, acctPass), nil); c != 200 {
		t.Fatalf("slow submit: %d", c)
	}
	if _, _, err := e.store.ByEmail(context.Background(), acctEmail); err != nil {
		t.Fatal("a human-paced form was ignored")
	}
}

func TestRegisterValidationI18n(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	b := newBrowser(t)
	_, page, _ := b.do("GET", e.base+"/register", nil, nil)
	for _, tc := range []struct {
		name, email, pw, confirm, want string
		terms                          bool
	}{
		{acctName, acctEmail, "short", "short", "at least 10 characters", true},
		{acctName, acctEmail, "password123", "password123", "too common", true},
		{acctName, acctEmail, acctPass, acctPass + "x", "do not match", true},
		{acctName, "not-an-email", acctPass, acctPass, "valid email", true},
		{"Boss <ceo@bank.com>", acctEmail, acctPass, acctPass, "display name", true},
		{acctName, acctEmail, acctPass, acctPass, "accept the terms", false},
	} {
		f := regForm(page, tc.name, tc.email, tc.pw, tc.confirm)
		if !tc.terms {
			f.Del("terms")
		}
		c, p, _ := b.do("POST", e.base+"/register", f, nil)
		if c != 422 || !strings.Contains(p, tc.want) {
			t.Errorf("%q: %d, want %q", tc.want, c, tc.want)
		}
		page = p
	}
	e.sink.None(t, 200*time.Millisecond)

	// French, chosen from Accept-Language, formal "vous".
	b.lang = "fr-FR,fr;q=0.9,en;q=0.5"
	_, page, _ = b.do("GET", e.base+"/register", nil, nil)
	if !strings.Contains(page, `lang="fr"`) || !strings.Contains(page, "Créer un compte opérateur") {
		t.Fatal("French page not served")
	}
	_, p, _ := b.do("POST", e.base+"/register", regForm(page, acctName, acctEmail, "short", "short"), nil)
	if !strings.Contains(p, "au moins 10 caractères") {
		t.Fatal("French error not served")
	}
	b.lang = "de-DE,de;q=0.9,fr;q=0.4,en;q=0.6"
	if _, p, _ := b.do("GET", e.base+"/", nil, nil); !strings.Contains(p, `lang="en"`) {
		t.Fatal("q-values ignored")
	}
}

func TestAccountRateLimits(t *testing.T) {
	e := newAcctEnv(t, true, func(c *Config) {
		c.Limits = DefaultLimits()
		c.Limits.LoginPerMinIP, c.Limits.LoginBurstIP = 6, 6
		c.Limits.LoginFailsPerEmail = 3
		c.Limits.SignupPerHourIP, c.Limits.SignupBurstIP = 1, 2
		c.Limits.VerifyPerMinIP, c.Limits.VerifyBurstIP = 1, 3
	})
	// Wrong passwords on one address: the 4th try is refused even with the
	// right password.
	_, _ = e.store.Create(context.Background(), acctEmail, acctName, accounts.HashPassword(acctPass), true)
	for i := 0; i < 3; i++ {
		if c, _ := e.login(t, acctEmail, "wrong password here"); c != 401 {
			t.Fatalf("try %d: %d", i, c)
		}
	}
	if c, r := e.login(t, acctEmail, acctPass); c != 429 || r.Error != "rate_limited" || r.RetryAfter < 1 {
		t.Fatalf("per address: %d %+v", c, r)
	}
	// Per IP: 6 requests, then 429.
	_, _ = e.login(t, "other@example.com", "wrong password here")
	_, _ = e.login(t, "other@example.com", "wrong password here")
	if c, r := e.login(t, "other@example.com", "wrong password here"); c != 429 || r.RetryAfter < 1 {
		t.Fatalf("per IP: %d %+v", c, r)
	}

	// Registration: 2 posts, then 429.
	b := newBrowser(t)
	for i := 0; i < 2; i++ {
		e.register(t, b, "N", fmt.Sprintf("n%d@example.com", i), acctPass)
	}
	if c, _ := e.register(t, b, "N", "n9@example.com", acctPass); c != 429 {
		t.Fatalf("signup: %d", c)
	}
	// Verify: 3, then 429.
	for i := 0; i < 3; i++ {
		b.do("GET", e.base+"/verify?token=nope", nil, nil)
	}
	if c, _, _ := b.do("GET", e.base+"/verify?token=nope", nil, nil); c != 429 {
		t.Fatalf("verify: %d", c)
	}
}

func TestTokenExpiryAndDisabled(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	ctx := context.Background()
	_, _ = e.store.Create(ctx, acctEmail, acctName, accounts.HashPassword(acctPass), true)
	_, r := e.login(t, acctEmail, acctPass)

	// Sliding expiry: used after 2 days, the token lives 30 days from then.
	e.clk.add(48 * time.Hour)
	c, m := e.api(t, "GET", "/v1/account/me", r.Token)
	exp1, _ := time.Parse(time.RFC3339, r.ExpiresAt)
	exp2, _ := time.Parse(time.RFC3339, fmt.Sprint(m["expiresAt"]))
	if c != 200 || exp2.Sub(exp1) < 47*time.Hour {
		t.Fatalf("slide: %d %v -> %v", c, exp1, exp2)
	}
	// Unused for 30 days: expired.
	e.clk.add(31 * 24 * time.Hour)
	if c, _ := e.api(t, "GET", "/v1/account/me", r.Token); c != 401 {
		t.Fatalf("expired token: %d", c)
	}
	hr := startHost(t, e.testEnv, loopback, allow, nil)
	if _, err := connect(t, tokenOp(e.testEnv, r.Token), hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrAuthExpired) {
		t.Fatalf("expired token connect: %v", err)
	}

	// Disabled account: tokens die, login refused, back after enable.
	_, r = e.login(t, acctEmail, acctPass)
	if err := e.store.SetDisabled(ctx, acctEmail, true); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.api(t, "GET", "/v1/account/me", r.Token); c != 401 {
		t.Fatal("disabled account token still valid")
	}
	if _, err := connect(t, tokenOp(e.testEnv, r.Token), hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrAuthExpired) {
		t.Fatalf("disabled connect: %v", err)
	}
	if c, r := e.login(t, acctEmail, acctPass); c != 401 || r.Error != "invalid_credentials" {
		t.Fatalf("disabled login: %d", c)
	}
	_ = e.store.SetDisabled(ctx, acctEmail, false)
	if c, _ := e.login(t, acctEmail, acctPass); c != 200 {
		t.Fatalf("re-enabled login: %d", c)
	}
}

func TestQuotas(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.Quotas = AccountQuotas{MaxConcurrent: 1, SessionsPerDay: 2} })
	ctx := context.Background()
	a, _ := e.store.Create(ctx, acctEmail, acctName, accounts.HashPassword(acctPass), false)
	_, r := e.login(t, acctEmail, acctPass)
	op := tokenOp(e.testEnv, r.Token)

	// An unknown ID does not cost a session.
	if _, err := connect(t, op, "999999999", "123456789"); !errors.Is(err, sl.ErrUnknownID) {
		t.Fatalf("unknown id: %v", err)
	}
	_, _, os1, err := connectPair(t, e.testEnv, op)
	if err != nil {
		t.Fatal(err)
	}
	// Second session at once: concurrent quota.
	hr := startHost(t, e.testEnv, loopback, allow, nil)
	_, err = connect(t, op, hr.h.ID(), hr.h.Code())
	var se *sl.ServerError
	if !errors.Is(err, sl.ErrQuota) || !errors.As(err, &se) || se.Detail != wire.QuotaConcurrent {
		t.Fatalf("concurrent: %v", err)
	}
	os1.Close()
	waitFor(t, func() bool { return e.metric(t, `rdv_session_end_total{outcome="bye"}`) == "1" })
	_, _, os2, err := connectPair(t, e.testEnv, op)
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	os2.Close()
	waitFor(t, func() bool { return e.metric(t, `rdv_session_end_total{outcome="bye"}`) == "2" })
	// Third of the day: daily quota, with the time to midnight UTC.
	hr = startHost(t, e.testEnv, loopback, allow, nil)
	_, err = connect(t, op, hr.h.ID(), hr.h.Code())
	if !errors.As(err, &se) || se.Code != wire.ErrQuota || se.Detail != wire.QuotaDaily || se.Retry <= 0 || se.Retry > 24*time.Hour {
		t.Fatalf("daily: %v", err)
	}
	if e.metric(t, `rdv_quota_refused_total{quota="sessions_per_day"}`) != "1" {
		t.Fatal("quota metric")
	}
	// Counters are persisted.
	waitFor(t, func() bool {
		u, _ := e.store.Usage(ctx, a.ID)
		return u.SessionsToday == 2
	})
	// Static keys are never subject to account quotas (none configured
	// here: a key-less public instance refuses key-less operators).
	keyless := opConfig(e.testEnv, loopback, nil)
	keyless.Key = ""
	if _, err := connect(t, keyless, hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrOperatorRequired) || errors.Is(err, sl.ErrAuthExpired) {
		t.Fatalf("keyless operator: %v", err)
	}
}

func TestRelayQuota(t *testing.T) {
	const limit = 2 << 20
	e := newAcctEnv(t, false, func(c *Config) { c.Quotas = AccountQuotas{RelayBytesMonth: limit} })
	ctx := context.Background()
	a, _ := e.store.Create(ctx, acctEmail, acctName, accounts.HashPassword(acctPass), false)
	_, r := e.login(t, acctEmail, acctPass)
	op := tokenOp(e.testEnv, r.Token)
	op.ICE = sl.ICEOptions{ForceRelay: true, IncludeLoopback: true, IPFilter: loopback.IPFilter}

	// A relayed session that goes over the monthly volume is cut.
	hr, hs, os, err := connectPair(t, e.testEnv, op)
	if err != nil {
		t.Fatal(err)
	}
	_ = hr
	if !os.Path().Relayed() {
		t.Fatalf("path %s", os.Path())
	}
	go func() {
		for {
			if _, _, err := os.ReadFrame(); err != nil {
				return
			}
		}
	}()
	chunk := make([]byte, 64<<10)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err := hs.WriteFrame(sl.OpBinary, chunk); err != nil {
			break
		}
		select {
		case <-os.Done():
			deadline = time.Time{}
		default:
		}
	}
	select {
	case <-os.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("relayed session not cut at the quota")
	}
	if e.metric(t, `rdv_quota_refused_total{quota="relay_month_ended"}`) != "1" {
		t.Fatal("relay quota end not counted")
	}
	waitFor(t, func() bool {
		u, _ := e.store.Usage(ctx, a.ID)
		return u.RelayBytesMonth >= limit
	})
	// The next connect is refused until next month.
	hr2 := startHost(t, e.testEnv, loopback, allow, nil)
	_, err = connect(t, tokenOp(e.testEnv, r.Token), hr2.h.ID(), hr2.h.Code())
	var se *sl.ServerError
	if !errors.As(err, &se) || se.Code != wire.ErrQuota || se.Detail != wire.QuotaRelayMonth || se.Retry <= 0 {
		t.Fatalf("relay month: %v", err)
	}
}

func TestPublicModeHostsAndKeys(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) {
		c.OperatorKeys = []OperatorKey{{Label: testAccount, Secret: testKey}}
		c.Quotas = AccountQuotas{MaxConcurrent: 1}
	})
	// Hosts need nothing; key-less, token-less operators are refused;
	// a garbage token is an expired login.
	hr := startHost(t, e.testEnv, loopback, allow, nil)
	none := opConfig(e.testEnv, loopback, nil)
	none.Key = ""
	if _, err := connect(t, none, hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrOperatorRequired) || errors.Is(err, sl.ErrAuthExpired) {
		t.Fatalf("anonymous operator: %v", err)
	}
	if _, err := connect(t, tokenOp(e.testEnv, "rdv1.garbage"), hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrAuthExpired) {
		t.Fatalf("garbage token: %v", err)
	}
	// Static keys keep working (and have no account quota): two at once.
	_, _, _, err := connectPair(t, e.testEnv, opConfig(e.testEnv, loopback, nil))
	if err != nil {
		t.Fatal(err)
	}
	hr2, _, _, err := connectPair(t, e.testEnv, opConfig(e.testEnv, loopback, nil))
	if err != nil {
		t.Fatalf("second key session: %v", err)
	}
	hr2.mu.Lock()
	defer hr2.mu.Unlock()
	if hr2.reqs[0].Account != testAccount {
		t.Fatalf("key label %q", hr2.reqs[0].Account)
	}
	// A key plus a garbage token: the key is used.
	both := opConfig(e.testEnv, loopback, nil)
	both.Token = "rdv1.garbage"
	if _, _, _, err := connectPair(t, e.testEnv, both); err != nil {
		t.Fatalf("key with a bad token: %v", err)
	}
}

func TestInfoAndPages(t *testing.T) {
	e := newAcctEnv(t, true, nil)
	c, m := e.api(t, "GET", "/v1/info", "")
	if c != 200 || m["mode"] != "public" || m["accounts"] != true || m["registerUrl"] != e.base+"/register" || m["name"] != "Oblireach" || m["version"] != "dev" {
		t.Fatalf("info %d %v", c, m)
	}
	b := newBrowser(t)
	for path, want := range map[string]int{"/": 200, "/register": 200, "/terms": 200, "/login-help": 200, "/nope": 404, "/verify?token=bad": 404} {
		code, page, h := b.do("GET", e.base+path, nil, nil)
		if code != want {
			t.Errorf("%s: %d", path, code)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe") {
			t.Fatalf("%s: CSP %q", path, csp)
		}
		// The inline style is allowed by its hash: it must match exactly.
		i, j := strings.Index(page, "<style>"), strings.Index(page, "</style>")
		sum := sha256.Sum256([]byte(page[i+7 : j]))
		if !strings.Contains(csp, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
			t.Fatalf("%s: style hash not in the CSP", path)
		}
		if strings.Contains(page, "<script") || strings.Contains(page, "http://") || strings.Contains(page, "https://") {
			t.Fatalf("%s: script or external reference", path)
		}
	}
	if _, page, _ := b.do("GET", e.base+"/", nil, nil); !strings.Contains(page, `href="/register"`) {
		t.Fatal("landing without register link")
	}
	if c, _ := e.api(t, "POST", "/v1/info", ""); c != 405 {
		t.Fatal("info accepts POST")
	}

	// Private instance with keys only: no accounts.
	p := newEnv(t, nil)
	resp, err := http.Get(strings.Replace(strings.TrimSuffix(p.url, wire.Path), "ws://", "http://", 1) + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info["accounts"] != false || info["registerUrl"] != "" || info["mode"] != "private" {
		t.Fatalf("private info %v", info)
	}
	resp, _ = http.Get(strings.Replace(strings.TrimSuffix(p.url, wire.Path), "ws://", "http://", 1) + "/register")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("register page on a key-only instance")
	}
}

// TestBehindProxy: nginx terminates TLS and proxies to RDV_TLS=off with
// RDV_TRUST_PROXY=true (X-Forwarded-Proto/For/Host), possibly without
// forwarding the Host header.
func TestBehindProxy(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) {
		c.TrustProxyHeaders = true
		c.PublicURL = "https://rdv.example.test"
		c.Limits.LoginPerMinIP, c.Limits.LoginBurstIP = 2, 2
	})
	hc := &http.Client{}
	do := func(method, path string, body url.Values, hdr map[string]string) (*http.Response, string) {
		var rd io.Reader
		if body != nil {
			rd = strings.NewReader(body.Encode())
		}
		req, _ := http.NewRequest(method, e.base+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-For", "198.51.100.7")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	resp, page := do("GET", "/register", nil, nil)
	ck := resp.Header.Get("Set-Cookie")
	if !strings.HasPrefix(ck, "__Host-rdv_csrf=") || !strings.Contains(ck, "Secure") || !strings.Contains(ck, "SameSite=Strict") || !strings.Contains(ck, "HttpOnly") {
		t.Fatalf("cookie behind https proxy: %q", ck)
	}
	cookie := strings.SplitN(ck, ";", 2)[0]
	form := regForm(page, acctName, acctEmail, acctPass, acctPass)
	if r, _ := do("POST", "/register", form, map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}); r.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", r.StatusCode)
	}
	// The browser's Origin is the public one while the backend sees
	// Host: 127.0.0.1:port.
	if r, p := do("POST", "/register", form, map[string]string{"Cookie": cookie, "Origin": "https://rdv.example.test"}); r.StatusCode != 200 || !strings.Contains(p, "ready") {
		t.Fatalf("register behind proxy: %d", r.StatusCode)
	}
	// Plain-HTTP cookie name is not accepted for an HTTPS request.
	if r, _ := do("POST", "/register", form, map[string]string{"Cookie": strings.Replace(cookie, "__Host-", "", 1)}); r.StatusCode != 403 {
		t.Fatalf("http cookie on https: %d", r.StatusCode)
	}
	if c, m := e.api(t, "GET", "/v1/info", ""); c != 200 || m["registerUrl"] != "https://rdv.example.test/register" {
		t.Fatalf("info: %v", m)
	}

	// Rate limits follow X-Forwarded-For when trusted.
	login := func(ip string) int {
		body := strings.NewReader(`{"email":"a@example.com","password":"wrong password here"}`)
		req, _ := http.NewRequest("POST", e.base+"/v1/account/login", body)
		req.Header.Set("X-Forwarded-For", ip)
		r, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	login("203.0.113.1")
	login("203.0.113.1")
	if c := login("203.0.113.1"); c != 429 {
		t.Fatalf("same client: %d", c)
	}
	if c := login("203.0.113.2"); c != 401 {
		t.Fatalf("other client limited with it: %d", c)
	}
	// A client cannot spoof its address through the header: the proxy
	// appends the real one, and only the last entry counts.
	if c := login("203.0.113.9, 203.0.113.1"); c != 429 {
		t.Fatalf("spoofed first entry: %d", c)
	}

	// Without RDV_TRUST_PROXY the headers are ignored.
	p := newAcctEnv(t, false, func(c *Config) {
		c.PublicURL = ""
		c.Limits.LoginPerMinIP, c.Limits.LoginBurstIP = 2, 2
	})
	for i, want := range []int{401, 401, 429} {
		body := strings.NewReader(`{"email":"a@example.com","password":"wrong password here"}`)
		req, _ := http.NewRequest("POST", p.base+"/v1/account/login", body)
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i))
		r, _ := hc.Do(req)
		r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("untrusted XFF, request %d: %d", i, r.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", p.base+"/register", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	r, _ := hc.Do(req)
	r.Body.Close()
	if ck := r.Header.Get("Set-Cookie"); !strings.HasPrefix(ck, "rdv_csrf=") || strings.Contains(ck, "Secure") {
		t.Fatalf("untrusted X-Forwarded-Proto honoured: %q", ck)
	}
	if _, m := p.api(t, "GET", "/v1/info", ""); m["registerUrl"] != p.base+"/register" {
		t.Fatalf("derived register URL %v", m["registerUrl"])
	}
}

// TestConnectAuthBounded: one signalling connection cannot turn connect
// messages into a flood of database lookups.
func TestConnectAuthBounded(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	c, _, err := websocket.DefaultDialer.Dial(e.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.WriteJSON(wire.Msg{T: wire.THello, Proto: wire.ProtocolVersion, Role: wire.RoleOperator, Ver: "1.0.0"})
	var codes []string
	for i := 0; i < 5; i++ {
		_ = c.WriteJSON(wire.Msg{T: wire.TConnect, ID: "123456789", Token: "rdv1.garbage"})
		var m wire.Msg
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := c.ReadJSON(&m); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, m.Err)
	}
	want := []string{wire.ErrAuthExpired, wire.ErrAuthExpired, wire.ErrAuthExpired, wire.ErrRateLimited, wire.ErrRateLimited}
	if fmt.Sprint(codes) != fmt.Sprint(want) {
		t.Fatalf("codes %v, want %v", codes, want)
	}
}
