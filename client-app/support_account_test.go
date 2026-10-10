package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oblireach/supportlink"
)

// fakeRdvAccounts implements the rendezvous server's account API (rdv
// contract: /v1/info, /v1/account/login, /logout, /me).
type fakeRdvAccounts struct {
	mu          sync.Mutex
	accounts    bool
	registerURL string
	noInfo      bool   // an older server: /v1/info is 404
	loginMode   string // "", "unverified", "limited", "disabled", "broken"
	meStatus    int
	logouts     []string
	logins      []map[string]string
}

func (f *fakeRdvAccounts) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/info" && r.Method == http.MethodGet:
			if f.noInfo {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "Lab rdv", "version": "1.2.3", "mode": "open",
				"accounts": f.accounts, "registerUrl": f.registerURL})
		case r.URL.Path == "/v1/account/login" && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.logins = append(f.logins, in)
			switch {
			case f.loginMode == "disabled":
				http.NotFound(w, r)
			case f.loginMode == "limited":
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limited","retryAfter":42}`))
			case f.loginMode == "unverified":
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"email_not_verified"}`))
			case f.loginMode == "broken":
				w.WriteHeader(http.StatusInternalServerError)
			case in["email"] != "ann@example.com" || in["password"] != "s3cret":
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_credentials"}`))
			default:
				_, _ = w.Write([]byte(`{"token":"TOKEN-abc123","expiresAt":"2099-01-02T03:04:05Z","account":{"id":"u1","email":"ann@example.com","name":"Ann\u0007 Smith","verified":true}}`))
			}
		case r.URL.Path == "/v1/account/logout" && r.Method == http.MethodPost:
			f.logouts = append(f.logouts, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/account/me" && r.Method == http.MethodGet:
			if f.meStatus != 0 && f.meStatus != 200 {
				w.WriteHeader(f.meStatus)
				return
			}
			if r.Header.Get("Authorization") != "Bearer TOKEN-abc123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"account":{"id":"u1","email":"ann@example.com","name":"Ann B. Smith","verified":true}}`))
		default:
			http.NotFound(w, r)
		}
	})
}

// failingTransport: no network (the built-in public server in tests).
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in tests")
}

func newAccountTestHub(t *testing.T, dial supportDialFunc) (*supportHub, *memSupportStore, *httptest.Server, *fakeRdvAccounts, string) {
	t.Helper()
	rdv := &fakeRdvAccounts{accounts: true}
	rs := httptest.NewServer(rdv.handler())
	t.Cleanup(rs.Close)
	store := &memSupportStore{}
	h := newSupportHub(store, dial)
	h.userName = func() string { return "operator" }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveHTTP(w, r, supOrigin) }))
	t.Cleanup(srv.Close)
	list, err := h.settings.save(supportServerInput{Label: "Lab", URL: rs.URL})
	if err != nil {
		t.Fatal(err)
	}
	return h, store, srv, rdv, supportUserServers(list)[0].ID
}

func serverViewOf(t *testing.T, out map[string]interface{}, id string) map[string]interface{} {
	t.Helper()
	list, _ := out["servers"].([]interface{})
	for _, x := range list {
		if m := x.(map[string]interface{}); m["id"] == id {
			return m
		}
	}
	t.Fatalf("server %s not in %v", id, out)
	return nil
}

func TestSupportAccountLoginErrors(t *testing.T) {
	_, _, srv, rdv, sid := newAccountTestHub(t, nil)
	cases := []struct {
		mode, email, pass, want string
		retry                   float64
	}{
		{"", "ann@example.com", "wrong", "invalid_credentials", 0},
		{"", "", "x", "missing", 0},
		{"unverified", "ann@example.com", "s3cret", "email_not_verified", 0},
		{"limited", "ann@example.com", "s3cret", "rate_limited", 42},
		{"disabled", "ann@example.com", "s3cret", "accounts_disabled", 0},
		{"broken", "ann@example.com", "s3cret", "server_error", 0},
	}
	for _, c := range cases {
		rdv.mu.Lock()
		rdv.loginMode = c.mode
		rdv.mu.Unlock()
		st, out := supPost(t, srv, "account/login", map[string]string{"server": sid, "email": c.email, "password": c.pass}, supOrigin)
		if st != 400 || out["error"] != c.want {
			t.Errorf("%s/%s: %d %v, want %s", c.mode, c.pass, st, out, c.want)
		}
		if c.retry > 0 && out["retry"] != c.retry {
			t.Errorf("%s: retry %v", c.mode, out["retry"])
		}
	}
	if st, out := supPost(t, srv, "account/login", map[string]string{"server": "nope", "email": "a@b", "password": "x"}, supOrigin); st != 400 || out["error"] != "no_server" {
		t.Errorf("unknown server: %d %v", st, out)
	}
	if st, _ := supPost(t, srv, "account/login", map[string]string{"server": sid, "email": "ann@example.com", "password": "s3cret"}, "http://evil.example"); st != http.StatusForbidden {
		t.Errorf("login from another Origin: %d", st)
	}
	// Unreachable server.
	h2 := newSupportHub(&memSupportStore{}, nil)
	list, _ := h2.settings.save(supportServerInput{URL: "http://127.0.0.1:1"})
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h2.serveHTTP(w, r, supOrigin) }))
	defer srv2.Close()
	if st, out := supPost(t, srv2, "account/login", map[string]string{"server": supportUserServers(list)[0].ID, "email": "a@b.c", "password": "x"}, supOrigin); st != 400 || out["error"] != "unreachable" {
		t.Errorf("unreachable: %d %v", st, out)
	}
}

func TestSupportAccountLoginLogoutVerify(t *testing.T) {
	h, store, srv, rdv, sid := newAccountTestHub(t, nil)
	// Not signed in: cannot connect.
	_, out, _ := supGet(t, srv, "servers")
	if v := serverViewOf(t, out, sid); v["canConnect"] != false || v["account"] != nil {
		t.Fatalf("before login %v", v)
	}
	st, out := supPost(t, srv, "account/login", map[string]string{"server": sid, "email": " ann@example.com ", "password": "s3cret"}, supOrigin)
	if st != 200 {
		t.Fatalf("login %d %v", st, out)
	}
	v := serverViewOf(t, out, sid)
	acct, _ := v["account"].(map[string]interface{})
	if acct == nil || acct["loggedIn"] != true || acct["email"] != "ann@example.com" || acct["name"] != "Ann Smith" || v["canConnect"] != true {
		t.Fatalf("after login %v", v)
	}
	if acct["expiresAt"] != float64(time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC).Unix()*1000) {
		t.Errorf("expiresAt %v", acct["expiresAt"])
	}
	rdv.mu.Lock()
	if len(rdv.logins) == 0 || !strings.HasPrefix(rdv.logins[len(rdv.logins)-1]["client"], "desktop/") {
		t.Errorf("login body %v", rdv.logins)
	}
	rdv.mu.Unlock()
	// The token: in the (sealed) store, never in an answer to the page.
	if !strings.Contains(string(store.data), "TOKEN-abc123") {
		t.Fatal("the token was not saved")
	}
	_, _, raw := supGet(t, srv, "servers")
	if strings.Contains(raw, "TOKEN") || strings.Contains(raw, "s3cret") {
		t.Fatalf("the token reached the page: %s", raw)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "TOKEN") {
		t.Fatalf("the login answer carries the token: %s", b)
	}
	if strings.Contains(string(store.data), "s3cret") {
		t.Fatal("the password was saved")
	}
	// Verify: the server's current name.
	_, out = supPost(t, srv, "account/verify", map[string]string{"server": sid}, supOrigin)
	if a := serverViewOf(t, out, sid)["account"].(map[string]interface{}); a["name"] != "Ann B. Smith" {
		t.Errorf("verify %v", a)
	}
	// A network / server failure while verifying changes nothing.
	rdv.mu.Lock()
	rdv.meStatus = 500
	rdv.mu.Unlock()
	_, out = supPost(t, srv, "account/verify", map[string]string{"server": sid}, supOrigin)
	if serverViewOf(t, out, sid)["account"] == nil {
		t.Error("a 500 signed the account out")
	}
	// Editing the server keeps the account; a new address drops it.
	if _, err := h.settings.save(supportServerInput{ID: sid, Label: "Lab 2", URL: srv.URL[:0] + rdvURL(h, sid)}); err != nil {
		t.Fatal(err)
	}
	if sv, _, _ := h.settings.get(sid); sv.Token == "" {
		t.Error("a label edit dropped the account")
	}
	// Logout: cleared here, the server told with the bearer.
	_, out = supPost(t, srv, "account/logout", map[string]string{"server": sid}, supOrigin)
	if v := serverViewOf(t, out, sid); v["account"] != nil || v["canConnect"] != false {
		t.Fatalf("after logout %v", v)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		rdv.mu.Lock()
		n := len(rdv.logouts)
		got := ""
		if n > 0 {
			got = rdv.logouts[0]
		}
		rdv.mu.Unlock()
		if n > 0 {
			if got != "Bearer TOKEN-abc123" {
				t.Errorf("logout header %q", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server was not told about the logout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(string(store.data), "TOKEN") {
		t.Fatal("the token is still saved after logout")
	}
	// Signed in again, then the server revokes the token: verify signs out.
	supPost(t, srv, "account/login", map[string]string{"server": sid, "email": "ann@example.com", "password": "s3cret"}, supOrigin)
	rdv.mu.Lock()
	rdv.meStatus = 401
	rdv.mu.Unlock()
	_, out = supPost(t, srv, "account/verify", map[string]string{"server": sid}, supOrigin)
	if serverViewOf(t, out, sid)["account"] != nil {
		t.Error("a revoked token stays signed in")
	}
	// A new address: the account goes.
	supPost(t, srv, "account/login", map[string]string{"server": sid, "email": "ann@example.com", "password": "s3cret"}, supOrigin)
	if _, err := h.settings.save(supportServerInput{ID: sid, URL: "other.example.com"}); err != nil {
		t.Fatal(err)
	}
	if sv, _, _ := h.settings.get(sid); sv.Token != "" || sv.AcctEmail != "" {
		t.Error("a new address kept the account")
	}
}

func rdvURL(h *supportHub, id string) string {
	sv, _, _ := h.settings.get(id)
	return sv.URL
}

func TestSupportAccountExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	sv := supportServer{Token: "t", TokenExp: now.Unix() + 10}
	if sv.accountToken(now) != "t" || sv.accountToken(now.Add(11*time.Second)) != "" {
		t.Fatal("expiry")
	}
	if (supportServer{Token: "t"}).accountToken(now) != "t" {
		t.Fatal("no expiry said")
	}
	for raw, want := range map[string]int64{`"2001-09-09T01:46:40Z"`: 1_000_000_000, `1000000000`: 1_000_000_000, `1000000000000`: 1_000_000_000, `null`: 0, `"x"`: 0, `"1000000000"`: 1_000_000_000} {
		if got := parseExpiry(json.RawMessage(raw)); got != want {
			t.Errorf("parseExpiry(%s) = %d, want %d", raw, got, want)
		}
	}
}

func TestSupportRegister(t *testing.T) {
	h, _, srv, rdv, sid := newAccountTestHub(t, nil)
	var opened []string
	old := supportBrowserOpen
	supportBrowserOpen = func(u string) error { opened = append(opened, u); return nil }
	defer func() { supportBrowserOpen = old }()

	// Accounts on, a sign-up page.
	rdv.registerURL = "https://lab.example.com/register?from=desktop"
	if _, out, _ := supGet(t, srv, "account/info?server="+sid); out["accounts"] != true || out["register"] != true {
		t.Fatalf("info %v", out)
	}
	if st, out := supPost(t, srv, "account/register", map[string]string{"server": sid}, supOrigin); st != 200 || out["ok"] != true {
		t.Fatalf("register %d %v", st, out)
	}
	if len(opened) != 1 || opened[0] != "https://lab.example.com/register?from=desktop" {
		t.Fatalf("opened %v", opened)
	}
	// Accounts off: operator keys only (the cached info expired first).
	h.account.mu.Lock()
	clear(h.account.info)
	h.account.mu.Unlock()
	rdv.mu.Lock()
	rdv.accounts, rdv.registerURL = false, ""
	rdv.mu.Unlock()
	if _, out, _ := supGet(t, srv, "account/info?server="+sid); out["accounts"] != false || out["register"] != false {
		t.Fatalf("info without accounts %v", out)
	}
	if st, out := supPost(t, srv, "account/register", map[string]string{"server": sid}, supOrigin); st != 400 || out["error"] != "accounts_disabled" {
		t.Fatalf("register without accounts %d %v", st, out)
	}
	// An unsafe sign-up address is never opened.
	h.account.mu.Lock()
	clear(h.account.info)
	h.account.mu.Unlock()
	rdv.mu.Lock()
	rdv.accounts, rdv.registerURL = true, "javascript:alert(1)"
	rdv.mu.Unlock()
	if st, out := supPost(t, srv, "account/register", map[string]string{"server": sid}, supOrigin); st != 400 || out["error"] != "no_register" {
		t.Fatalf("unsafe register URL %d %v", st, out)
	}
	// An older server (no /v1/info): no accounts.
	h.account.mu.Lock()
	clear(h.account.info)
	h.account.mu.Unlock()
	rdv.mu.Lock()
	rdv.noInfo = true
	rdv.mu.Unlock()
	if _, out, _ := supGet(t, srv, "account/info?server="+sid); out["accounts"] != false {
		t.Fatalf("older server %v", out)
	}
	// The built-in public server, unreachable here: its known sign-up page.
	h.account.http.Transport = failingTransport{}
	if st, out := supPost(t, srv, "account/register", map[string]string{"server": supportPublicID}, supOrigin); st != 200 || out["ok"] != true {
		t.Fatalf("public register %d %v", st, out)
	}
	if opened[len(opened)-1] != "https://rdv.reach.obli.tools/register" {
		t.Fatalf("public sign-up page %v", opened)
	}
	if _, out, _ := supGet(t, srv, "account/info?server="+supportPublicID); out["accounts"] != true || out["register"] != true {
		t.Fatalf("public info offline %v", out)
	}
	if len(opened) != 2 {
		t.Fatalf("opened %v", opened)
	}
}

func TestSupportCleanRegisterURL(t *testing.T) {
	for in, ok := range map[string]bool{
		"https://a.example/register":  true,
		"http://127.0.0.1:8080/r":     true,
		"http://a.example/register":   false,
		"https://user:pw@a.example/":  false,
		"javascript:alert(1)":         false,
		"file:///c:/x":                false,
		"https://a.example/ register": false,
		"":                            false,
		"/register":                   false,
	} {
		if _, got := supportCleanRegisterURL(in); got != ok {
			t.Errorf("%q: %v, want %v", in, got, ok)
		}
	}
}

// Connect by code: an account or a key is required; the token (else the
// key) is what the operator presents; an expired login signs out.
func TestSupportConnectGating(t *testing.T) {
	var mu sync.Mutex
	var cfgs []supportlink.OperatorConfig
	fail := error(nil)
	dial := func(ctx context.Context, cfg supportlink.OperatorConfig, id, code string) (supportSess, error) {
		mu.Lock()
		cfgs = append(cfgs, cfg)
		err := fail
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		return newFakeSupSess(), nil
	}
	h, _, srv, _, sid := newAccountTestHub(t, dial)
	ft := &fakeTimers{}
	h.after = ft.after
	connect := func() (int, map[string]interface{}) {
		return supPost(t, srv, "connect", map[string]string{"server": sid, "id": "482913507", "code": "123456789", "name": "Op"}, supOrigin)
	}
	if st, out := connect(); st != 400 || out["error"] != "login_required" {
		t.Fatalf("without an account: %d %v", st, out)
	}
	// An expired login counts as none.
	if _, err := h.settings.updateServer(sid, func(s *supportServer) { s.Token, s.TokenExp = "OLD", time.Now().Add(-time.Minute).Unix() }); err != nil {
		t.Fatal(err)
	}
	if st, out := connect(); st != 400 || out["error"] != "login_required" {
		t.Fatalf("an expired login: %d %v", st, out)
	}
	// Signed in: the token, not the key.
	supPost(t, srv, "account/login", map[string]string{"server": sid, "email": "ann@example.com", "password": "s3cret"}, supOrigin)
	if _, err := h.settings.save(supportServerInput{ID: sid, URL: rdvURL(h, sid), Key: "static-key"}); err != nil {
		t.Fatal(err)
	}
	_, out := connect()
	waitSupState(t, srv, out["conn"].(string), "ready")
	mu.Lock()
	if c := cfgs[len(cfgs)-1]; c.Token != "TOKEN-abc123" || c.Key != "" {
		t.Errorf("signed in: token %q key %q", c.Token, c.Key)
	}
	mu.Unlock()
	// The server says the login expired: failed "auth_expired", signed out.
	mu.Lock()
	fail = supportlink.ErrAuthExpired
	mu.Unlock()
	_, out = connect()
	if st := waitSupState(t, srv, out["conn"].(string), "failed"); st["error"] != "auth_expired" {
		t.Fatalf("auth expired: %v", st)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if sv, _, _ := h.settings.get(sid); sv.Token == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still signed in after auth_expired")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The static key still works (private servers).
	mu.Lock()
	fail = nil
	mu.Unlock()
	_, out = connect()
	waitSupState(t, srv, out["conn"].(string), "ready")
	mu.Lock()
	if c := cfgs[len(cfgs)-1]; c.Token != "" || c.Key != "static-key" {
		t.Errorf("key only: token %q key %q", c.Token, c.Key)
	}
	mu.Unlock()
	// Quota.
	mu.Lock()
	fail = &supportlink.ServerError{Code: supportlink.ErrQuota.Code, Detail: "daily", Retry: 3 * time.Second}
	mu.Unlock()
	_, out = connect()
	if st := waitSupState(t, srv, out["conn"].(string), "failed"); st["error"] != "quota" || st["retry"] != float64(3) {
		t.Fatalf("quota: %v", st)
	}
	_, _, raw := supGet(t, srv, "servers")
	if strings.Contains(raw, "static-key") || strings.Contains(raw, "TOKEN") {
		t.Fatalf("a secret reached the page: %s", raw)
	}
}

func TestSupportErrCodeAccounts(t *testing.T) {
	cases := map[string]error{
		"auth_expired":     supportlink.ErrAuthExpired,
		"key_refused":      supportlink.ErrOperatorRequired,
		"quota":            supportlink.ErrQuota,
		"quota_concurrent": &supportlink.ServerError{Code: supportlink.ErrQuota.Code, Detail: "concurrent_sessions"},
		"quota_daily":      &supportlink.ServerError{Code: supportlink.ErrQuota.Code, Detail: "sessions_per_day"},
		"quota_relay":      &supportlink.ServerError{Code: supportlink.ErrQuota.Code, Detail: "relay_month"},
		"unavailable":      &supportlink.ServerError{Code: "unavailable", Retry: 5 * time.Second},
	}
	for want, err := range cases {
		if got, _ := supportErrCode(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
	if _, r := supportErrCode(&supportlink.ServerError{Code: "unavailable", Retry: 5 * time.Second}); r != 5 {
		t.Errorf("unavailable retry %d", r)
	}
}

// An instance without mail: the account is said unverified; /me refreshes
// the expiry.
func TestSupportAccountUnverified(t *testing.T) {
	rdv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/account/login":
			_, _ = w.Write([]byte(`{"token":"T1","expiresAt":"2090-01-01T00:00:00Z","account":{"id":7,"email":"bo@x.test","name":"Bo","verified":false}}`))
		case "/v1/account/me":
			_, _ = w.Write([]byte(`{"account":{"id":7,"email":"bo@x.test","name":"Bo","verified":true},"expiresAt":"2091-01-01T00:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer rdv.Close()
	h := newSupportHub(&memSupportStore{}, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveHTTP(w, r, supOrigin) }))
	defer srv.Close()
	list, _ := h.settings.save(supportServerInput{URL: rdv.URL})
	sid := supportUserServers(list)[0].ID
	_, out := supPost(t, srv, "account/login", map[string]string{"server": sid, "email": "bo@x.test", "password": "p"}, supOrigin)
	a := serverViewOf(t, out, sid)["account"].(map[string]interface{})
	if a["unverified"] != true {
		t.Fatalf("unverified %v", a)
	}
	_, out = supPost(t, srv, "account/verify", map[string]string{"server": sid}, supOrigin)
	a = serverViewOf(t, out, sid)["account"].(map[string]interface{})
	if a["unverified"] != nil || a["expiresAt"] != float64(time.Date(2091, 1, 1, 0, 0, 0, 0, time.UTC).Unix()*1000) {
		t.Fatalf("after /me %v", a)
	}
}
