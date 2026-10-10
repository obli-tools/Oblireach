package main

// support_account.go — operator accounts on a support server: "Connect by
// code" needs, on the chosen server, either an account signed in from this
// app or a static operator key (Settings › Support, for private servers).
//
// The rendezvous server's account API (rdv):
//
//	GET  /v1/info           {name, version, mode, accounts, registerUrl}
//	POST /v1/account/login  {email, password, client} -> {token, expiresAt, account:{id,email,name,verified}}
//	                        401 invalid_credentials, 403 email_not_verified, 429 rate_limited (retryAfter)
//	POST /v1/account/logout (Bearer) -> 204
//	GET  /v1/account/me     (Bearer) -> {account} / 401
//
// The token is kept with the server in the DPAPI-sealed support settings
// (support.go), never in the preferences, and never handed to the page: the
// page only learns who is signed in (supportAccountView). Registering is done
// on the server's own web page, in the system browser.
//
// HTTP (support.go serveHTTP gates; the Origin required on every POST):
//
//	GET  /local/support/account/info?server=<id>  {accounts, register}
//	POST /local/support/account/login     {server, email, password} -> {servers} | {error, retry?}
//	POST /local/support/account/logout    {server}                  -> {servers}
//	POST /local/support/account/verify    {server}                  -> {servers}
//	POST /local/support/account/register  {server}                  -> {ok} | {error}

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oblireach/supportlink"
)

const (
	// supportPublicRegisterURL: the public server's sign-up page, when its
	// /v1/info cannot be read.
	supportPublicRegisterURL = supportPublicURL + "/register"
	// supportAccountTimeout bounds one call to the account API.
	supportAccountTimeout = 15 * time.Second
	// supportInfoTTL: how long a server's /v1/info answer is reused.
	supportInfoTTL = 10 * time.Minute
	// supportMaxEmail / supportMaxPassword: input bounds.
	supportMaxEmail    = 254
	supportMaxPassword = 1024
	// supportAccountBody caps an answer of the account API.
	supportAccountBody = 64 << 10
)

// supportSetCredentials: what the operator presents to the server: the
// account's token when signed in, else the static operator key.
func supportSetCredentials(cfg *supportlink.OperatorConfig, token, key string) {
	if token != "" {
		cfg.Token = token
		return
	}
	cfg.Key = key
}

// supportAccountError: a refusal the page shows (support_page.go SUP_ACCT_ERR).
type supportAccountError struct {
	Code  string
	Retry int // seconds (rate_limited)
}

func (e *supportAccountError) Error() string { return "support account: " + e.Code }

func acctErr(code string) error { return &supportAccountError{Code: code} }

// supportInfo: a server's /v1/info.
type supportInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Mode        string `json:"mode"`
	Accounts    bool   `json:"accounts"`
	RegisterURL string `json:"registerUrl"`
}

// supportAccount: who signed in, and the token.
type supportAccount struct {
	Token     string
	ExpiresAt int64 // unix seconds, 0 = not said
	// Unverified: the server says the e-mail address is not verified (an
	// instance without mail); absent = not said.
	Unverified bool
	ID         string
	Email      string
	Name       string
}

type supportAccountClient struct {
	http *http.Client
	now  func() time.Time

	mu   sync.Mutex
	info map[string]supportInfoEntry // by API base
}

type supportInfoEntry struct {
	info supportInfo
	at   time.Time
}

func newSupportAccountClient() *supportAccountClient {
	return &supportAccountClient{
		// The default transport: the system proxy settings are honoured.
		http: &http.Client{Timeout: supportAccountTimeout},
		now:  time.Now,
		info: map[string]supportInfoEntry{},
	}
}

// supportAPIBase: the origin of a support server's HTTP API ("wss://h/x" ->
// "https://h").
func supportAPIBase(server string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || u.Host == "" {
		return "", acctErr("url_invalid")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		return "https://" + u.Host, nil
	case "http", "ws":
		if !supportLoopbackHost(u.Hostname()) {
			return "", acctErr("url_invalid")
		}
		return "http://" + u.Host, nil
	}
	return "", acctErr("url_invalid")
}

// do sends one request; the answer's status and body (bounded).
func (c *supportAccountClient) do(ctx context.Context, method, u, token string, body interface{}) (int, http.Header, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, nil, acctErr("url_invalid")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OblireachDesktop/"+appVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, acctErr("unreachable")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, supportAccountBody))
	if err != nil {
		return 0, nil, nil, acctErr("unreachable")
	}
	return resp.StatusCode, resp.Header, b, nil
}

// getInfo: the server's /v1/info (cached). A server without it (404, an
// older rdv) has no accounts.
func (c *supportAccountClient) getInfo(ctx context.Context, server string) (supportInfo, error) {
	base, err := supportAPIBase(server)
	if err != nil {
		return supportInfo{}, err
	}
	c.mu.Lock()
	if e, ok := c.info[base]; ok && c.now().Sub(e.at) < supportInfoTTL {
		c.mu.Unlock()
		return e.info, nil
	}
	c.mu.Unlock()
	status, _, b, err := c.do(ctx, http.MethodGet, base+"/v1/info", "", nil)
	if err != nil {
		return supportInfo{}, err
	}
	var info supportInfo
	switch {
	case status == http.StatusOK:
		if json.Unmarshal(b, &info) != nil {
			return supportInfo{}, acctErr("server_error")
		}
	case status == http.StatusNotFound:
		// (no account API on this server)
	default:
		return supportInfo{}, acctErr("server_error")
	}
	info.Name = cleanSupportText(info.Name, supportMaxLabel)
	if info.RegisterURL != "" {
		if u, ok := supportCleanRegisterURL(info.RegisterURL); ok {
			info.RegisterURL = u
		} else {
			info.RegisterURL = ""
		}
	}
	c.mu.Lock()
	c.info[base] = supportInfoEntry{info: info, at: c.now()}
	c.mu.Unlock()
	return info, nil
}

// supportCleanRegisterURL: a sign-up page the browser may open: absolute
// https (http only to this computer), no credentials in it.
func supportCleanRegisterURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 2048 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !supportLoopbackHost(u.Hostname()) {
			return "", false
		}
	default:
		return "", false
	}
	return u.String(), true
}

// apiError: the server's {"error": ...} code, if any.
func apiError(b []byte) (string, int) {
	var e struct {
		Error      string      `json:"error"`
		RetryAfter json.Number `json:"retryAfter"`
	}
	_ = json.Unmarshal(b, &e)
	n, _ := e.RetryAfter.Int64()
	if n < 0 {
		n = 0
	}
	return e.Error, int(n)
}

// parseExpiry: expiresAt as RFC 3339 or as unix seconds / milliseconds.
func parseExpiry(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Unix()
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			raw = json.RawMessage(strconv.FormatInt(n, 10))
		} else {
			return 0
		}
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil || n <= 0 {
		return 0
	}
	if n >= 1e11 { // milliseconds (1e11 s is the year 5138)
		n /= 1000
	}
	return int64(n)
}

type supportAccountJSON struct {
	ID       json.RawMessage `json:"id"`
	Email    string          `json:"email"`
	Name     string          `json:"name"`
	Verified *bool           `json:"verified"`
	// expiresAt: /me's, beside the account (unix seconds, 0 = not said).
	expiresAt int64
}

// unverified: the server said the address is not verified.
func (a supportAccountJSON) unverified() bool { return a.Verified != nil && !*a.Verified }

func (a supportAccountJSON) id() string {
	var s string
	if json.Unmarshal(a.ID, &s) == nil {
		return cleanSupportText(s, 128)
	}
	return cleanSupportText(strings.Trim(string(a.ID), `"`), 128)
}

// login signs in on server; the account and its token.
func (c *supportAccountClient) login(ctx context.Context, server, email, password string) (supportAccount, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return supportAccount{}, acctErr("missing")
	}
	if utf8.RuneCountInString(email) > supportMaxEmail || len(password) > supportMaxPassword ||
		strings.IndexFunc(email, unicode.IsControl) >= 0 {
		return supportAccount{}, acctErr("invalid_credentials")
	}
	base, err := supportAPIBase(server)
	if err != nil {
		return supportAccount{}, err
	}
	status, hdr, b, err := c.do(ctx, http.MethodPost, base+"/v1/account/login", "",
		map[string]string{"email": email, "password": password, "client": "desktop/" + appVersion})
	if err != nil {
		return supportAccount{}, err
	}
	code, retry := apiError(b)
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return supportAccount{}, acctErr("invalid_credentials")
	case http.StatusForbidden:
		if code == "email_not_verified" {
			return supportAccount{}, acctErr("email_not_verified")
		}
		return supportAccount{}, acctErr("refused")
	case http.StatusTooManyRequests:
		if retry == 0 {
			retry, _ = strconv.Atoi(hdr.Get("Retry-After"))
		}
		return supportAccount{}, &supportAccountError{Code: "rate_limited", Retry: max(retry, 0)}
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return supportAccount{}, acctErr("accounts_disabled")
	default:
		return supportAccount{}, acctErr("server_error")
	}
	var out struct {
		Token     string             `json:"token"`
		ExpiresAt json.RawMessage    `json:"expiresAt"`
		Account   supportAccountJSON `json:"account"`
	}
	if json.Unmarshal(b, &out) != nil {
		return supportAccount{}, acctErr("server_error")
	}
	tok := strings.TrimSpace(out.Token)
	if tok == "" || len(tok) > 4096 || strings.IndexFunc(tok, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return supportAccount{}, acctErr("server_error")
	}
	a := supportAccount{Token: tok, ExpiresAt: parseExpiry(out.ExpiresAt), ID: out.Account.id(), Unverified: out.Account.unverified(),
		Email: cleanSupportText(out.Account.Email, supportMaxEmail), Name: cleanSupportText(out.Account.Name, supportMaxName)}
	if a.Email == "" {
		a.Email = cleanSupportText(email, supportMaxEmail)
	}
	return a, nil
}

// logout tells the server the token is no longer used (best effort).
func (c *supportAccountClient) logout(ctx context.Context, server, token string) {
	base, err := supportAPIBase(server)
	if err != nil || token == "" {
		return
	}
	_, _, _, _ = c.do(ctx, http.MethodPost, base+"/v1/account/logout", token, nil)
}

// errAccountGone: the server no longer takes the token.
var errAccountGone = errors.New("support account: signed out by the server")

// me reads who the token belongs to; errAccountGone on 401.
func (c *supportAccountClient) me(ctx context.Context, server, token string) (supportAccountJSON, error) {
	base, err := supportAPIBase(server)
	if err != nil {
		return supportAccountJSON{}, err
	}
	status, _, b, err := c.do(ctx, http.MethodGet, base+"/v1/account/me", token, nil)
	if err != nil {
		return supportAccountJSON{}, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return supportAccountJSON{}, errAccountGone
	default:
		return supportAccountJSON{}, acctErr("server_error")
	}
	var out struct {
		Account   supportAccountJSON `json:"account"`
		ExpiresAt json.RawMessage    `json:"expiresAt"`
	}
	if json.Unmarshal(b, &out) != nil {
		return supportAccountJSON{}, acctErr("server_error")
	}
	out.Account.expiresAt = parseExpiry(out.ExpiresAt)
	return out.Account, nil
}

// supportBrowserOpen: the system's default browser (tests replace it).
var supportBrowserOpen = func(u string) error { return hwBrowserOpen(u) }

// registerURL: where to create an account for server sv.
func (h *supportHub) registerURL(ctx context.Context, sv supportServer) (string, error) {
	info, err := h.account.getInfo(ctx, sv.URL)
	if err != nil {
		if sv.ID == supportPublicID {
			return supportPublicRegisterURL, nil
		}
		return "", err
	}
	if !info.Accounts {
		return "", acctErr("accounts_disabled")
	}
	if info.RegisterURL != "" {
		return info.RegisterURL, nil
	}
	if sv.ID == supportPublicID {
		return supportPublicRegisterURL, nil
	}
	return "", acctErr("no_register")
}

func (h *supportHub) serveAccount(w http.ResponseWriter, r *http.Request, path string) {
	fail := func(err error) {
		var ae *supportAccountError
		var se *supportSettingsError
		switch {
		case errors.As(err, &ae):
			out := map[string]interface{}{"error": ae.Code}
			if ae.Retry > 0 {
				out["retry"] = ae.Retry
			}
			supportJSON(w, http.StatusBadRequest, out)
		case errors.As(err, &se) && se.Code != "storage":
			supportJSON(w, http.StatusBadRequest, map[string]string{"error": se.Code})
		default:
			supportJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage"})
		}
	}
	server := func(id string) (supportServer, bool) {
		sv, ok, err := h.settings.get(id)
		if err != nil {
			fail(err)
			return sv, false
		}
		if !ok {
			fail(acctErr("no_server"))
			return sv, false
		}
		return sv, true
	}
	ctx, cancel := context.WithTimeout(r.Context(), supportAccountTimeout+5*time.Second)
	defer cancel()
	if path == "info" && r.Method == http.MethodGet {
		sv, ok := server(r.URL.Query().Get("server"))
		if !ok {
			return
		}
		info, err := h.account.getInfo(ctx, sv.URL)
		if err != nil {
			// The public server: accounts, its sign-up page (both known).
			if sv.ID == supportPublicID {
				supportJSON(w, http.StatusOK, map[string]interface{}{"accounts": true, "register": true, "reachable": false})
				return
			}
			supportJSON(w, http.StatusOK, map[string]interface{}{"accounts": nil, "register": false, "reachable": false})
			return
		}
		register := info.Accounts && (info.RegisterURL != "" || sv.ID == supportPublicID)
		supportJSON(w, http.StatusOK, map[string]interface{}{"accounts": info.Accounts, "register": register, "reachable": true})
		return
	}
	if r.Method != http.MethodPost {
		supportJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	switch path {
	case "login":
		var in struct {
			Server   string `json:"server"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		sv, ok := server(in.Server)
		if !ok {
			return
		}
		a, err := h.account.login(ctx, sv.URL, in.Email, in.Password)
		in.Password = ""
		if err != nil {
			fail(err)
			return
		}
		list, err := h.settings.updateServer(sv.ID, func(s *supportServer) {
			if s.URL != sv.URL { // (edited meanwhile: not this server's token)
				return
			}
			s.Token, s.TokenExp, s.AcctID, s.AcctEmail, s.AcctName, s.AcctUnverified = a.Token, a.ExpiresAt, a.ID, a.Email, a.Name, a.Unverified
		})
		if err != nil {
			fail(err)
			return
		}
		supportJSON(w, http.StatusOK, map[string]interface{}{"servers": supportViews(list)})
	case "logout":
		var in struct {
			Server string `json:"server"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		sv, ok := server(in.Server)
		if !ok {
			return
		}
		list, err := h.settings.updateServer(sv.ID, func(s *supportServer) { s.clearAccount() })
		if err != nil {
			fail(err)
			return
		}
		if tok := sv.Token; tok != "" {
			go func() { // (signed out here whatever the server answers)
				c, cancel := context.WithTimeout(context.Background(), supportAccountTimeout)
				defer cancel()
				h.account.logout(c, sv.URL, tok)
			}()
		}
		supportJSON(w, http.StatusOK, map[string]interface{}{"servers": supportViews(list)})
	case "verify":
		var in struct {
			Server string `json:"server"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		sv, ok := server(in.Server)
		if !ok {
			return
		}
		list, err := h.verifyAccount(ctx, sv)
		if err != nil {
			fail(err)
			return
		}
		supportJSON(w, http.StatusOK, map[string]interface{}{"servers": supportViews(list)})
	case "register":
		var in struct {
			Server string `json:"server"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		sv, ok := server(in.Server)
		if !ok {
			return
		}
		u, err := h.registerURL(ctx, sv)
		if err != nil {
			fail(err)
			return
		}
		if err := supportBrowserOpen(u); err != nil {
			fail(acctErr("browser"))
			return
		}
		supportJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		supportJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// verifyAccount asks the server who the saved token belongs to: signed out
// when it says the token is no longer valid (or it expired here), the name
// and e-mail refreshed otherwise; a network failure changes nothing.
func (h *supportHub) verifyAccount(ctx context.Context, sv supportServer) ([]supportServer, error) {
	tok := sv.accountToken(h.now())
	if tok == "" {
		if sv.Token == "" {
			return h.settings.list()
		}
		return h.settings.updateServer(sv.ID, func(s *supportServer) { s.clearAccount() })
	}
	a, err := h.account.me(ctx, sv.URL, tok)
	switch {
	case errors.Is(err, errAccountGone):
		return h.settings.updateServer(sv.ID, func(s *supportServer) {
			if s.Token == tok {
				s.clearAccount()
			}
		})
	case err != nil:
		return h.settings.list()
	}
	return h.settings.updateServer(sv.ID, func(s *supportServer) {
		if s.Token != tok {
			return
		}
		if e := cleanSupportText(a.Email, supportMaxEmail); e != "" {
			s.AcctEmail = e
		}
		if n := cleanSupportText(a.Name, supportMaxName); n != "" {
			s.AcctName = n
		}
		if a.Verified != nil {
			s.AcctUnverified = !*a.Verified
		}
		if a.expiresAt > 0 {
			s.TokenExp = a.expiresAt
		}
	})
}
