package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Session persistence ────────────────────────────────────────────────────────

type SessionStore struct {
	mu      sync.Mutex
	Cookies map[string]string `json:"cookies"` // name → value
	path    string
}

func loadSession(path string) *SessionStore {
	s := &SessionStore{path: path, Cookies: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, s)
	}
	return s
}

// saveLocked writes the cookies to disk. The caller holds s.mu: the map is
// read and the file written under the lock, so two responses setting cookies
// at once neither race on the map (a fatal runtime error) nor let an older
// snapshot overwrite a newer one.
func (s *SessionStore) saveLocked() {
	_ = os.MkdirAll(filepath.Dir(s.path), 0700)
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(s.path, data, 0600)
}

func (s *SessionStore) setCookies(cookies []*http.Cookie) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range cookies {
		if c.MaxAge < 0 {
			delete(s.Cookies, c.Name)
		} else {
			s.Cookies[c.Name] = c.Value
		}
	}
	s.saveLocked()
}

func (s *SessionStore) inject(req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, val := range s.Cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: val})
	}
}

func (s *SessionStore) hasCookies() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Cookies) > 0
}

func (s *SessionStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cookies = map[string]string{}
	s.saveLocked()
}

// ── SSO state ─────────────────────────────────────────────────────────────────

type pendingSso struct {
	requestID string
	state     string
	// The server and cookie store the flow was started on: the callback
	// completes it there, even if the active server changed meanwhile.
	serverURL string
	store     *SessionStore
}

// ── Proxy ─────────────────────────────────────────────────────────────────────

type Proxy struct {
	mu      sync.RWMutex // guards cfg, session, stores, sso and loginOrig
	dir     string       // config directory (session / favourites files)
	cfg     *Config
	session *SessionStore            // active server profile's cookies; read through sess() / activeTarget()
	stores  map[string]*SessionStore // one store per profile id (servers.go storeForLocked)
	mux     *http.ServeMux
	sso     *pendingSso // current desktop SSO flow (single-user app)
	port    int         // local server port (for SSO callback URL)
	origin  string      // local UI origin, http://127.0.0.1:<port>
	secret  string      // per-run secret: only this app's WebView has it (ServeHTTP)
	// loginOrig: a profile's URL before the sign-in page rewrote it, while
	// that edit has not led to a sign-in (servers.go loginEditLocked).
	loginOrig map[string]string
	// The app's language as last told (i18n.go syncLang), and who is told
	// when it changes (main.go: every window).
	langMu sync.Mutex
	lang   string
	onLang func(code string)
	// The app's theme as last told (theme.go syncTheme), and who is told when
	// it changes (main.go: every window).
	themeMu sync.Mutex
	theme   string
	onTheme func(theme string)
	// Who is told when the servers change (the active one, the mode, the
	// list: servers.go notifyServers; main.go: every window).
	serversMu sync.Mutex
	onServers func()
	// Tenant alignment of each profile's server session (tenantpin.go).
	pinsMu sync.Mutex
	pins   map[string]*tenantPin
	// tunnels: the password vault's registry of live ObliReach sessions
	// (reach_tunnels.go), filled from the POST /api/remote/sessions answer
	// and consumed by the vault bindings (vault_bridge.go).
	tunnels *reachTunnels
	// terms: the terminal tunnels, relayed so a tab can move to another
	// window without its shell ending (term_park.go).
	terms *termParks
	// localShells: the local SSH connections' shells (ssh_relay.go; nil:
	// appLocalShells).
	localShells *localShellRegistry
	// rdpStreams: the local RDP sessions streamed by the RDP helper
	// (rdphost_relay.go; nil: appRdpStreams).
	rdpStreams *rdpStreamRegistry
}

func newProxy(cfg *Config, cfgDir string, port int) *Proxy {
	p := &Proxy{
		cfg:       cfg,
		dir:       cfgDir,
		port:      port,
		origin:    fmt.Sprintf("http://127.0.0.1:%d", port),
		secret:    randomToken(),
		loginOrig: map[string]string{},
		tunnels:   newReachTunnels(),
		terms:     newTermParks(),
	}
	p.session = loadSession(p.sessionPath())
	p.lang = cfg.uiLang()
	p.theme = cfg.uiTheme()
	p.stores = map[string]*SessionStore{}
	if cfg.ActiveID != "" {
		p.stores[cfg.ActiveID] = p.session
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handleUI)
	mux.HandleFunc("/proxy/", p.handleProxy)
	mux.HandleFunc("/sp/", p.handleProfileProxy) // servers.go: a given server profile
	mux.HandleFunc("/spt/", p.handleTenantProxy) // tenantpin.go: ... on a given tenant
	mux.HandleFunc("/local/tenant-align", p.handleTenantAlign)
	mux.HandleFunc("/local/config", p.handleConfig)
	mux.HandleFunc("/local/servers", p.handleServers) // servers.go
	mux.HandleFunc("/local/servers/", p.handleServers)
	mux.HandleFunc("/local/logout", p.handleLogout)
	mux.HandleFunc("/local/favorites", p.handleFavorites)
	mux.HandleFunc("/local/open-url", p.handleOpenURL)
	mux.HandleFunc("/local/open-support", p.handleOpenSupport) // hwinfo.go: the maker's support page (Info > More details)
	mux.HandleFunc("/local/socket.io.min.js", p.handleSocketIoJS)
	mux.HandleFunc("/local/xterm.js", p.handleXtermJS) // xterm_assets.go: the remote shells' terminal
	mux.HandleFunc("/local/xterm-addon-fit.js", p.handleXtermFitJS)
	mux.HandleFunc("/local/xterm.css", p.handleXtermCSS)
	mux.HandleFunc("/local/i18n", p.handleI18n) // i18n.go
	mux.HandleFunc("/local/i18n/", p.handleI18n)
	mux.HandleFunc("/local/theme", p.handleTheme)                  // theme.go
	mux.HandleFunc("/local/theme/settings", p.handleThemeSettings) // ... the Desktop's own theme settings
	mux.HandleFunc("/local/term-park", p.handleTermPark)           // term_park.go: a terminal tab moving to another window
	mux.HandleFunc("/local/term-attach/", p.handleTermAttach)      // ... taken over there
	mux.HandleFunc("/local/ssh-tunnel/", p.handleSSHTunnel)        // ssh_relay.go: a local SSH connection's terminal
	mux.HandleFunc("/local/rdp-tunnel/", p.handleRdpTunnel)        // rdphost_relay.go: a local RDP session streamed by the RDP helper (Linux)
	mux.HandleFunc("/local/support/", p.handleSupport)             // support.go: "Connect by code" (settings, setup, its tunnel)
	mux.HandleFunc("/sso/callback", p.handleSsoCallback)
	p.mux = mux
	return p
}

// setLangHook: who is told when the app's language changes (i18n.go syncLang).
func (p *Proxy) setLangHook(f func(code string)) {
	p.langMu.Lock()
	p.onLang = f
	p.langMu.Unlock()
}

// entryPath is the first page the WebView opens (main.go): it carries the
// per-run secret and turns it into a cookie.
const entryPath = "/local/enter"

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

// entryURL is the URL main.go opens in the WebView.
func (p *Proxy) entryURL() string { return p.origin + entryPath + "?k=" + p.secret }

// authCookie names the secret cookie. Cookies ignore the port, so the name
// carries it: two instances (two ports) never overwrite each other's cookie.
func (p *Proxy) authCookie() string { return "oblireach_k_" + strconv.Itoa(p.port) }

func (p *Proxy) secretOK(v string) bool {
	return p.secret != "" && subtle.ConstantTimeCompare([]byte(v), []byte(p.secret)) == 1
}

// ServeHTTP: the proxy listens on loopback, which every local user (RDS host)
// and every web page can reach, and it holds the operator's sessions on every
// server profile. Only this app's WebView may use it:
//   - a request with an Origin header must come from the local UI (web pages
//     send theirs on POSTs, CORS requests and WebSockets);
//   - every request must carry the per-run secret cookie (HttpOnly,
//     SameSite=Lax), set by the entry URL that only main.go knows. Lax, not
//     Strict: the return from SSO (Obligate -> /sso/callback -> refresh to /)
//     is a top-level navigation that must carry it; the cookie only exists in
//     this app's WebView2 profile, and cross-site POSTs / WebSockets are still
//     refused by the Origin check above.
//
// /sso/callback is the exception: the SSO page redirects the WebView there (a
// cross-site navigation, before any cookie is set for it). It only
// completes a flow this app started (single-use request id + server state).
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/sso/callback" {
		p.mux.ServeHTTP(w, r)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && o != p.origin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path == entryPath {
		p.handleEntry(w, r)
		return
	}
	if c, err := r.Cookie(p.authCookie()); err != nil || !p.secretOK(c.Value) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	p.mux.ServeHTTP(w, r)
}

// handleEntry turns the secret of the entry URL into the session cookie, then
// shows the UI (the redirect also takes the secret out of the address bar).
func (p *Proxy) handleEntry(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !p.secretOK(q.Get("k")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: p.authCookie(), Value: p.secret, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, entryTarget(q), http.StatusFound)
}

// detachedEntryURL is the URL a detached session window opens
// (detached_windows.go): the entry, then the page in detached mode for that
// device. A new WebView of the app shares the main one's cookies, but going
// through the entry does not depend on it; the secret never reaches the page.
// profile: the server profile the device id belongs to (the page refuses to
// start on another one: ids of two servers overlap). kind: a tool window (a
// terminal or the file explorer of the Obliance agent, toolKinds), empty for
// an ObliReach session.
func (p *Proxy) detachedEntryURL(deviceID, profile string, session int, viewOnly bool, kind string) string {
	return p.detachedToolEntryURL(deviceID, profile, session, viewOnly, kind, "", "")
}

// detachedToolEntryURL: detachedEntryURL for a tool tab moving to a new
// window (detached_windows.go tearOff): a terminal's park token (its new
// page takes the parked tunnel over, term_park.go), the explorer's folder.
func (p *Proxy) detachedToolEntryURL(deviceID, profile string, session int, viewOnly bool, kind, park, path string) string {
	q := url.Values{}
	q.Set("k", p.secret)
	q.Set("d", deviceID)
	if session >= 0 {
		q.Set("s", strconv.Itoa(session))
	}
	if viewOnly {
		q.Set("vo", "1")
	}
	if profile != "" {
		q.Set("p", profile)
	}
	if validWindowKind(kind) { // (a tool, or a VM console: vm_console_go.go)
		q.Set("m", kind)
	}
	if park != "" {
		q.Set("pk", park)
	}
	if path != "" {
		q.Set("fp", path)
	}
	return p.origin + entryPath + "?" + q.Encode()
}

// toolKinds: the tools a session window may hold instead of an ObliReach
// session (terminal_viewer.go, the page's TOOL_KINDS): the Obliance agent's
// terminals (CMD / PowerShell on Windows, "SSH": its login shell on Linux and
// macOS) and its file explorer; a local RDP connection's picture (rdp_view.go).
// Their tabs are keyed "<device key>~<kind>".
var toolKinds = map[string]bool{"cmd": true, "powershell": true, "ssh": true, "files": true, "rdp": true}

// toolParkKind: a tool tab that moves between windows parked (its session
// goes on: a terminal's shell, term_park.go; a local RDP session,
// rdp_park.go), with its one-time park token.
func toolParkKind(kind string) bool { return termProtocols[kind] || kind == "rdp" }

// validDetachID: the device id a detached window is opened for (Obliance
// ids are numbers; uuids are accepted too). Nothing else reaches the
// redirect below or the page.
func validDetachID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// entryTarget: where the entry lands. Always a path of this origin built from
// validated values only (never an address taken from the query): "/", or
// "/?detached=<id>[&session=<n>][&vo=1][&p=<profile>][&m=<tool>]" for a
// detached session window (m: one of toolKinds, or a VM console "vm-<guid>"
// with its name in vn, exactly; anything else is dropped and the window is an
// ObliReach one).
func entryTarget(q url.Values) string {
	id := q.Get("d")
	if !validDetachID(id) {
		return "/"
	}
	t := "/?detached=" + id
	if s := q.Get("s"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 1<<20 {
			t += "&session=" + strconv.Itoa(n)
		}
	}
	if q.Get("vo") == "1" {
		t += "&vo=1"
	}
	if pr := q.Get("p"); validDetachID(pr) {
		t += "&p=" + pr
	}
	m := q.Get("m")
	if validWindowKind(m) {
		t += "&m=" + m
	}
	// A VM console's window: its VM's name (its tab's title, vm_console_go.go).
	if vn := vmWindowName(q.Get("vn")); vmKind(m) && vn != "" {
		t += "&vn=" + url.QueryEscape(vn)
	}
	// A tool tab moved here (term_park.go, detached_windows.go tearOff): a
	// terminal's park token, the explorer's folder (escaped).
	if pk := q.Get("pk"); toolParkKind(m) && reParkToken.MatchString(pk) {
		t += "&pk=" + pk
	}
	if fp := q.Get("fp"); m == "files" && fp != "" && validToolPath(fp) {
		t += "&fp=" + url.QueryEscape(fp)
	}
	return t
}

// activeTarget snapshots the active server URL and its cookie store under one
// lock, so a request never pairs one server with another server's cookies
// (switchProfile changes both).
func (p *Proxy) activeTarget() (string, *SessionStore) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.ServerURL, p.session
}

func (p *Proxy) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page's address (a session window's: its device and server profile
	// ids) is never sent anywhere as a Referer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	p.mu.RLock()
	page := buildUI(p.cfg)
	p.mu.RUnlock()
	fmt.Fprint(w, page)
}

func (p *Proxy) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		var body struct {
			ServerURL string `json:"serverUrl"`
			Username  string `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		was := p.cfg.ActiveID
		p.loginEditLocked(body.ServerURL, body.Username) // servers.go
		switched := p.cfg.ActiveID != was
		p.mu.Unlock()
		p.syncLang()  // the sign-in page may have switched servers (i18n.go)
		p.syncTheme() // ... and with it the theme (theme.go)
		if switched {
			p.notifyServers() // (the session windows follow: servers.go)
		}
	}
	p.mu.RLock()
	out := map[string]interface{}{
		"serverUrl":  p.cfg.ServerURL,
		"username":   p.cfg.Username,
		"hasSession": p.session.hasCookies(),
		"localPort":  p.port,
	}
	p.mu.RUnlock()
	json.NewEncoder(w).Encode(out)
}

// /local/logout  POST [{ profile }]: signs out of that server profile (the
// active one when absent).
func (p *Proxy) handleLogout(w http.ResponseWriter, r *http.Request) {
	serverURL, store := p.activeTarget()
	var body struct {
		Profile string `json:"profile"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	}
	if body.Profile != "" {
		if serverURL, store = p.profileTarget(body.Profile); store == nil {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unknown server"}`, http.StatusNotFound)
			return
		}
	}
	// Best-effort call to server logout endpoint.
	if u, ok := serverOrigin(serverURL); ok {
		req, _ := http.NewRequest(http.MethodPost, u+"/api/auth/logout", nil)
		if req != nil {
			store.inject(req)
			client := &http.Client{Timeout: 5000000000}
			_, _ = client.Do(req)
		}
	}
	store.clear()
	p.mu.Lock()
	for id, s := range p.stores {
		if s == store {
			delete(p.loginOrig, id) // signed in on its current URL: that one is kept
		}
	}
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (p *Proxy) handleFavorites(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	favPath := p.favoritesPath() // per server profile
	p.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
			return
		}
		_ = os.MkdirAll(filepath.Dir(favPath), 0700)
		_ = os.WriteFile(favPath, body, 0600)
		fmt.Fprint(w, `{"ok":true}`)
		return
	}
	data, err := os.ReadFile(favPath)
	if err != nil {
		fmt.Fprint(w, `[]`)
		return
	}
	w.Write(data)
}

// handleSocketIoJS serves the embedded Socket.IO client bundle.
// We ship our own copy instead of proxying /socket.io/socket.io.js because the
// upstream Obliance server may or may not expose it depending on its Socket.IO
// config (serveClient flag, nginx routing, etc.) — serving locally makes the
// chat independent of that.
func (p *Proxy) handleSocketIoJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(socketIoClientJS)
}

// handleOpenURL opens the given URL in the system default browser.
// Only URLs matching the currently-configured server origin are accepted
// (or, with &profile=<id>, that server profile's), so a compromised page
// can't turn the proxy into a generic URL-opener.
func (p *Proxy) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	profile := r.URL.Query().Get("profile")
	if raw == "" {
		http.Error(w, `{"error":"url required"}`, http.StatusBadRequest)
		return
	}
	// Scope: must be an absolute URL whose origin matches the configured server.
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(w, `{"error":"invalid url"}`, http.StatusBadRequest)
		return
	}
	serverURL, _ := p.activeTarget()
	if profile != "" {
		var store *SessionStore
		if serverURL, store = p.profileTarget(profile); store == nil {
			http.Error(w, `{"error":"unknown server"}`, http.StatusNotFound)
			return
		}
	}
	if serverURL == "" {
		http.Error(w, `{"error":"no server configured"}`, http.StatusServiceUnavailable)
		return
	}
	srv, err := url.Parse(serverURL)
	if err != nil || parsed.Host != srv.Host {
		http.Error(w, `{"error":"url outside configured server"}`, http.StatusForbidden)
		return
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", raw)
	case "darwin":
		cmd = exec.Command("open", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("open-url: %v", err)
		http.Error(w, `{"error":"launcher failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

// handleSsoCallback handles the OAuth redirect from Obligate back to the local app.
// GET /sso/callback?code=xxx&state=yyy
func (p *Proxy) handleSsoCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	p.mu.Lock()
	sso := p.sso
	if code != "" && state != "" {
		p.sso = nil // single-use
	}
	p.mu.Unlock()

	if code == "" || state == "" || sso == nil {
		ssoFailedPage(w, tr("sso.missingParams"), "")
		return
	}

	// Exchange code+state via Obliance desktop-complete endpoint.
	body, _ := json.Marshal(map[string]string{
		"requestId": sso.requestID,
		"code":      code,
		"state":     state,
	})

	req, _ := http.NewRequest("POST", sso.serverURL+"/api/auth/sso-desktop-complete", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	sso.store.inject(req)

	client := &http.Client{Timeout: 15000000000} // 15s
	resp, err := client.Do(req)
	if err != nil {
		ssoFailedPage(w, tr("sso.err.network"), err.Error())
		return
	}
	defer resp.Body.Close()

	// Capture session cookies from Obliance response.
	if cookies := resp.Cookies(); len(cookies) > 0 {
		sso.store.setCookies(cookies)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		why, detail := ssoRefusalText(resp.StatusCode, respBody)
		ssoFailedPage(w, why, detail)
		return
	}

	// Success — redirect to local UI. In the app's theme (theme.go).
	pal := currentPalette()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html `+htmlLangAttrs(i18nCurrent())+`><head><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=/"><style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:`+cssColor(pal.Bg)+`;color:`+cssColor(pal.Muted)+`;font-family:sans-serif;color-scheme:`+paletteScheme(pal)+`}.s{text-align:center}.d{width:28px;height:28px;border:2.5px solid `+cssColor(pal.Border)+`;border-top-color:`+cssColor(pal.Accent)+`;border-radius:50%;animation:r .6s linear infinite;margin:0 auto 14px}@keyframes r{to{transform:rotate(360deg)}}</style></head><body><div class="s"><div class="d"></div><div>`+html.EscapeString(tr("sso.signingIn"))+`</div></div></body></html>`)
}

// paletteScheme: the CSS color-scheme of a palette.
func paletteScheme(p themePalette) string {
	if p.Light {
		return "light"
	}
	return "dark"
}

// ssoRefusalText: why the server refused the end of an SSO sign-in (its
// JSON {error}), in the app's language, and the server's own words as the
// detail (English, for the record). An answer that is not that JSON: the
// HTTP status only.
func ssoRefusalText(status int, body []byte) (why, detail string) {
	var r struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &r) == nil {
		detail = strings.TrimSpace(r.Error)
		if rs := []rune(detail); len(rs) > 200 {
			detail = string(rs[:200])
		}
	}
	switch detail {
	case "Request expired", "Invalid or expired request", "Missing required fields":
		return tr("sso.err.expired"), detail
	case "State mismatch":
		return tr("sso.err.state"), detail
	case "Code exchange failed":
		return tr("sso.err.exchange"), detail
	}
	return tr("sso.err.refused", "status", status), detail
}

// ssoFailedPage: the SSO return failed; why and a detail (escaped, the
// detail smaller: the words of the server or of Windows), and the way back
// to the sign-in page. In the app's language (i18n.go) and theme (theme.go).
func ssoFailedPage(w http.ResponseWriter, why, detail string) {
	pal := currentPalette()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	det := ""
	if detail != "" && detail != why {
		det = `<p style="color:` + cssColor(pal.Muted) + `;font-size:12px">` + html.EscapeString(detail) + `</p>`
	}
	fmt.Fprint(w, `<!DOCTYPE html><html `+htmlLangAttrs(i18nCurrent())+`><head><meta charset="utf-8"></head><body style="background:`+cssColor(pal.Bg)+`;color:`+cssColor(pal.Danger)+`;color-scheme:`+paletteScheme(pal)+`;font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:100vh"><div style="text-align:center"><h2>`+
		html.EscapeString(tr("sso.failedTitle"))+`</h2><p>`+html.EscapeString(why)+`</p>`+det+`<p><a href="/" style="color:`+cssColor(pal.Info)+`">`+
		html.EscapeString(tr("sso.backToLogin"))+`</a></p></div></body></html>`)
}

// handleSsoInit is called from the proxy to start the desktop SSO flow.
// POST /proxy/api/auth/sso-desktop-init — intercepts to store the requestId locally.
func (p *Proxy) startSsoFlow(w http.ResponseWriter, r *http.Request, serverURL string, store *SessionStore) {
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/sso/callback", p.port)

	body, _ := json.Marshal(map[string]string{"localCallbackUrl": callbackURL})
	req, _ := http.NewRequest("POST", serverURL+"/api/auth/sso-desktop-init", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	store.inject(req)

	client := &http.Client{Timeout: 10000000000}
	resp, err := client.Do(req)
	if err != nil {
		// (JSON-encoded: a Go network error quotes the URL.)
		j, _ := json.Marshal(map[string]interface{}{"success": false, "error": err.Error()})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write(j)
		return
	}
	defer resp.Body.Close()

	// Capture session cookies.
	if cookies := resp.Cookies(); len(cookies) > 0 {
		store.setCookies(cookies)
	}

	respBody, _ := io.ReadAll(resp.Body)

	// Parse response to extract requestId.
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			RequestID    string `json:"requestId"`
			AuthorizeURL string `json:"authorizeUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err == nil && result.Success {
		p.mu.Lock()
		p.sso = &pendingSso{
			requestID: result.Data.RequestID,
			state:     "", // state is managed server-side
			serverURL: serverURL,
			store:     store,
		}
		p.mu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// handleProxy reverse-proxies /proxy/<path> → <serverUrl>/<path>.
func (p *Proxy) handleProxy(w http.ResponseWriter, r *http.Request) {
	serverURL, store := p.activeTarget()
	if serverURL == "" {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"no server configured"}`, http.StatusServiceUnavailable)
		return
	}
	u, ok := serverOrigin(serverURL)
	if !ok {
		http.Error(w, `{"error":"invalid server url"}`, http.StatusInternalServerError)
		return
	}

	// Intercept desktop SSO init to store requestId locally.
	if strings.TrimPrefix(r.URL.Path, "/proxy") == "/api/auth/sso-desktop-init" && r.Method == http.MethodPost {
		p.startSsoFlow(w, r, u, store)
		return
	}
	// The page switching the tenant itself (tenantpin.go): serialized with
	// the pinned requests of that server, whose alignment it undoes.
	if isTenantSwitch(r, strings.TrimPrefix(r.URL.Path, "/proxy")) {
		p.forwardSwitch(p.activeProfileID(), func() { p.forward(w, r, "/proxy", u, store) })
		return
	}
	p.forward(w, r, "/proxy", u, store)
}

// upstreamHeaderTimeout bounds the wait for a server's answer (its headers):
// a server that accepts the connection and never answers must not hold the
// page's requests (and the WebView's few connections to this proxy) for
// ever. Well above a socket.io long-poll (25 s between pings by default),
// and above anything the Desktop asks Obliance.
const upstreamHeaderTimeout = 90 * time.Second

// upstreamTransport: the reverse proxy's transport (DefaultTransport's
// settings, its answer bounded by upstreamHeaderTimeout).
var upstreamTransport = func() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = upstreamHeaderTimeout
	return t
}()

// forward proxies r (its path minus prefix) to serverURL with the cookies of
// store: the active server under /proxy, any server profile under /sp/<id>.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, prefix, serverURL string, store *SessionStore) {
	// Only the scheme and host are used (servers.go serverOrigin).
	u, ok := serverOrigin(serverURL)
	target, err := url.Parse(u)
	if !ok || err != nil {
		http.Error(w, `{"error":"invalid server url"}`, http.StatusInternalServerError)
		return
	}

	// Strip the prefix to get the real path.
	r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	r.URL.RawPath = ""
	r.URL.Host = target.Host
	r.URL.Scheme = target.Scheme
	r.RequestURI = ""

	// Inject session cookies. The WebView's own cookies (the local secret)
	// never leave the machine, nor does the local page's address (Referer:
	// a session window's URL names its server profile).
	r.Header.Del("Cookie")
	r.Header.Del("Referer")
	store.inject(r)

	// WebSocket upgrade → special handling.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		p.handleWSProxy(w, r, target, store)
		return
	}

	// The password vault's session registry (reach_tunnels.go) learns a
	// session's scope from the server's own answer to this exact route —
	// never from the page, which could be compromised.
	isSessionsPost := r.Method == http.MethodPost && r.URL.Path == "/api/remote/sessions"
	originForVault := u

	// Regular HTTP reverse proxy.
	rp := &httputil.ReverseProxy{
		Transport: upstreamTransport,
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			if isSessionsPost {
				// So the body below is always plain JSON, even behind a
				// gzip-by-default reverse proxy in front of the server.
				req.Header.Del("Accept-Encoding")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if cookies := resp.Cookies(); len(cookies) > 0 {
				store.setCookies(cookies)
			}
			resp.Header.Del("Set-Cookie")
			if isSessionsPost && resp.StatusCode == http.StatusCreated {
				p.captureSessionResponse(resp, originForVault)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":"proxy: %s"}`, err.Error())
		},
	}
	rp.ServeHTTP(w, r)
}

// handleWSProxy tunnels a WebSocket connection to the upstream server.
func (p *Proxy) handleWSProxy(w http.ResponseWriter, r *http.Request, target *url.URL, store *SessionStore) {
	// Dial upstream (raw TCP, then HTTP upgrade).
	host := target.Host
	if !strings.Contains(host, ":") {
		if target.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	var upConn net.Conn
	var err error
	if target.Scheme == "https" {
		upConn, err = tls.Dial("tcp", host, &tls.Config{ServerName: target.Hostname()})
	} else {
		upConn, err = net.Dial("tcp", host)
	}
	if err != nil {
		http.Error(w, "upstream dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upConn.Close()

	// Send upgrade request to upstream.
	upReq, _ := http.NewRequest("GET", r.URL.String(), nil)
	for k, vs := range r.Header {
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}
	store.inject(upReq)
	upReq.Host = target.Host
	upReq.Header.Del("Origin")
	upReq.Header.Del("Referer")
	// The vault's frame injector (reach_tunnels.go) needs to reason about
	// WebSocket message boundaries on the raw wire bytes: never negotiate a
	// permessage-deflate (or other) extension on a tunnel it might attach to.
	// Harmless for every other WebSocket this proxies (terminals, socket.io):
	// the server already defaults to no compression (server/src/index.ts).
	vaultToken, vaultEligible := reachTunnelToken(r.URL.Path)
	if vaultEligible {
		upReq.Header.Del("Sec-WebSocket-Extensions")
	}
	_ = upReq.Write(upConn)

	// Read upstream response to complete the handshake.
	// Keep the buffered reader: the relay writes {"type":"paired"} and the
	// buffered agent frames (init, first keyframe) right after the 101, and
	// they often land in the same TCP read as the headers. Copying from the
	// raw conn afterwards used to drop them, leaving the viewer stuck on
	// "waiting for stream" (no init ever reached the page).
	upReader := bufio.NewReader(upConn)
	upResp, err := http.ReadResponse(upReader, upReq)
	if err != nil || upResp.StatusCode != http.StatusSwitchingProtocols {
		code := http.StatusBadGateway
		if upResp != nil {
			code = upResp.StatusCode
		}
		http.Error(w, fmt.Sprintf("upstream WS handshake failed: %v", err), code)
		return
	}

	// Hijack the client connection.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Forward the 101 response to the client.
	_ = upResp.Write(buf)
	_ = buf.Flush()

	// The vault's injector attaches only when: this is an
	// /api/remote/tunnel/<token> URL, that token was registered (an
	// ObliReach session, by captureSessionResponse), its registered origin
	// matches THIS request's target (a token of server A replayed against
	// /sp/B never gets one), and the handshake did not end up negotiating an
	// extension despite the Sec-WebSocket-Extensions strip above (some
	// intermediary could still add one back).
	if vaultEligible && upResp.Header.Get("Sec-WebSocket-Extensions") == "" {
		if tun, _, found := p.tunnels.lookup(vaultToken); found && tun.origin == target.Scheme+"://"+target.Host {
			inj := newWSInjector(upConn)
			detach := p.tunnels.attach(vaultToken, inj)
			defer detach()
			done := make(chan struct{}, 2)
			go func() { relayClientFrames(buf.Reader, inj); done <- struct{}{} }()
			go func() { io.Copy(clientConn, upReader); done <- struct{}{} }()
			<-done
			return
		}
		// A terminal (the server said so: captureSessionResponse): relayed
		// frame by frame, so its tab can move to another window without the
		// shell ending (term_park.go).
		if p.terms != nil && p.terms.isTerminal(vaultToken, target.Scheme+"://"+target.Host) {
			_ = buf.Flush()
			p.terms.run(vaultToken, upConn, upReader, clientConn, buf.Reader)
			return
		}
	}

	// Bidirectional tunnel. Read both sides through their buffered readers so
	// bytes already buffered during the handshake are forwarded first.
	done := make(chan struct{}, 2)
	go func() { io.Copy(upConn, buf.Reader); done <- struct{}{} }()
	go func() { io.Copy(clientConn, upReader); done <- struct{}{} }()
	<-done
}

// reachTunnelToken extracts the token from an exact
// /api/remote/tunnel/<token> path (the only WebSocket route the vault ever
// attaches to). Anything else — a different route, a nested path — is not
// eligible.
func reachTunnelToken(path string) (string, bool) {
	const prefix = "/api/remote/tunnel/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	tok := path[len(prefix):]
	if tok == "" || strings.Contains(tok, "/") {
		return "", false
	}
	return tok, true
}

// captureSessionResponse parses the JSON body of a successful
// POST /api/remote/sessions answer and, for an oblireach session, registers
// its scope in p.tunnels — the ONLY place the vault learns a session's
// tenant, since it comes from the server's own answer, never from the page.
// The response body is fully restored for the client afterwards, byte for
// byte, whether or not it parses (a malformed or oversized body is simply
// not registered; the client still gets its normal answer or error).
func (p *Proxy) captureSessionResponse(resp *http.Response, origin string) {
	const maxBody = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	if len(body) > maxBody {
		// Restore everything read plus whatever is left unread — never drop
		// bytes the client was going to receive.
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), resp.Body))
		return
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))

	var parsed struct {
		Data struct {
			SessionToken   string `json:"sessionToken"`
			Protocol       string `json:"protocol"`
			TenantID       int64  `json:"tenantId"`
			DeviceID       int64  `json:"deviceId"`
			DeviceTenantID *int64 `json:"deviceTenantId"`
			// deviceUuid: optional (a server that sends it); the vault then
			// matches a device-restricted entry by uuid (vault.go vaultDeviceIs).
			DeviceUUID string `json:"deviceUuid"`
			// vmId: a VM console's VM (its GUID); the vault offers a VM's
			// console only the entries bound to that VM (vault.go sessionMatch).
			VMID string `json:"vmId"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return
	}
	d := parsed.Data
	if termProtocols[d.Protocol] && validSessionToken(d.SessionToken) && p.terms != nil {
		p.terms.expect(d.SessionToken, origin) // (its tunnel can be parked: term_park.go)
		return
	}
	// A Hyper-V VM console (vm_console.go) is registered like an ObliReach
	// session, marked: the vault types into it its own way (vaultTypeVm).
	if (d.Protocol != "oblireach" && d.Protocol != "vmconsole") || !validSessionToken(d.SessionToken) {
		return
	}
	var tenant int64
	switch {
	case d.DeviceTenantID != nil && *d.DeviceTenantID > 0:
		tenant = *d.DeviceTenantID
	case d.TenantID > 0 && d.TenantID != 1:
		// A non-master SESSION tenant is proof enough that the device is in
		// it (no god-view ambiguity possible in that case).
		tenant = d.TenantID
	default:
		tenant = 0 // unknown: typing is refused with 'scope_unknown'
	}
	uuid := d.DeviceUUID
	if !validDeviceUUID(uuid) {
		uuid = "" // malformed: ignored (matched by id, as with an older server)
	}
	if d.Protocol == "vmconsole" {
		vmID, ok := normVMID(d.VMID)
		if !ok {
			vmID = "" // malformed: no VM-bound entry is offered (fail closed)
		}
		p.tunnels.registerVMConsole(d.SessionToken, origin, tenant, d.DeviceID, uuid, vmID)
		return
	}
	p.tunnels.registerSession(d.SessionToken, origin, tenant, d.DeviceID, uuid, false)
}

// validSessionToken bounds what captureSessionResponse will key the vault
// registry with — defence in depth, the server already generates these.
func validSessionToken(tok string) bool {
	if len(tok) == 0 || len(tok) > 512 {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
