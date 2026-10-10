package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A pre-profiles config becomes one profile that keeps session.json /
// favorites.json: upgrading signs nobody out.
func TestProfilesMigration(t *testing.T) {
	c := &Config{ServerURL: "https://obliance.example", Username: "admin"}
	if !c.normalizeProfiles() {
		t.Fatal("migration not reported")
	}
	a := c.active()
	if a == nil || a.SessionFile != "session.json" || a.Favorites != "favorites.json" || a.Username != "admin" || a.Name != "obliance.example" || a.Color != "violet" {
		t.Fatalf("migrated profile = %+v", a)
	}
	if c.normalizeProfiles() {
		t.Fatal("second normalize changed the config")
	}
}

func TestProfilesAddActivateRemove(t *testing.T) {
	dir := t.TempDir()
	c := &Config{ServerURL: "https://one.example", Username: "u1"}
	c.normalizeProfiles()
	first := c.ActiveID

	b := c.addProfile("https://two.example")
	if b.SessionFile == "session.json" || b.Color == "violet" || !strings.HasPrefix(b.SessionFile, "session-") {
		t.Fatalf("second profile = %+v", b)
	}
	if again := c.addProfile("HTTPS://TWO.EXAMPLE"); again.ID != b.ID {
		t.Fatal("same server added twice")
	}
	if !c.activate(b.ID) || c.ServerURL != "https://two.example" || c.Username != "" {
		t.Fatalf("activate: %+v", c)
	}
	// The login page edits the active profile only.
	c.Username = "u2"
	c.syncActive()
	if c.active().Username != "u2" {
		t.Fatal("username not synced into the active profile")
	}
	for _, p := range c.Profiles {
		if p.ID == first && p.Username != "u1" {
			t.Fatal("other profile touched")
		}
	}
	// The active profile cannot be removed; another one can, with its files.
	if c.removeProfile(b.ID, dir) {
		t.Fatal("active profile removed")
	}
	_ = os.WriteFile(filepath.Join(dir, "session.json"), []byte("{}"), 0600)
	if !c.removeProfile(first, dir) || len(c.Profiles) != 1 {
		t.Fatal("remove failed")
	}
	if _, err := os.Stat(filepath.Join(dir, "session.json")); !os.IsNotExist(err) {
		t.Fatal("session file of the removed profile kept")
	}
}

func TestValidServerURL(t *testing.T) {
	for u, want := range map[string]string{"https://a.example": "https://a.example", "http://10.0.0.1:3000/": "http://10.0.0.1:3000", " HTTPS://A.example ": "https://A.example"} {
		if got, ok := validServerURL(u); !ok || got != want {
			t.Fatalf("%q -> %q %v, want %q", u, got, ok, want)
		}
	}
	// A path, query, fragment or credentials would be dropped by the proxy
	// but kept by the SSO / logout calls: refused.
	for _, u := range []string{"", "a.example", "file:///c:/x", "javascript:alert(1)", "https://",
		"https://a.example/obliance", "https://a.example/devices", "https://a.example?x", "https://a.example?",
		"https://a.example#f", "https://u:p@a.example"} {
		if _, ok := validServerURL(u); ok {
			t.Fatalf("%q accepted", u)
		}
	}
	// A URL already configured with a path: everything targets its origin.
	if o, ok := serverOrigin("https://a.example/devices?x#y"); !ok || o != "https://a.example" {
		t.Fatalf("serverOrigin = %q %v", o, ok)
	}
}

// Switching server swaps the session store and never mixes cookies.
func TestSwitchProfileSwapsSession(t *testing.T) {
	dir := t.TempDir()
	oldConfigDir, oldConfigFile := configDir, configFile
	configDir, configFile = dir, filepath.Join(dir, "config.json")
	defer func() { configDir, configFile = oldConfigDir, oldConfigFile }()

	c := &Config{ServerURL: "https://one.example"}
	c.normalizeProfiles()
	p := &Proxy{cfg: c, dir: dir}
	p.session = loadSession(p.sessionPath())
	p.sess().setCookies([]*http.Cookie{{Name: "sid", Value: "one"}})

	rec := httptest.NewRecorder()
	p.handleServers(rec, httptest.NewRequest(http.MethodPost, "/local/servers/add", strings.NewReader(`{"serverUrl":"https://two.example"}`)))
	if rec.Code != 200 || c.ServerURL != "https://two.example" || p.sess().hasCookies() {
		t.Fatalf("add+switch: code %d url %s cookies %v", rec.Code, c.ServerURL, p.sess().hasCookies())
	}
	rec = httptest.NewRecorder()
	p.handleServers(rec, httptest.NewRequest(http.MethodPost, "/local/servers/add", strings.NewReader(`{"serverUrl":"javascript:x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatal("invalid url accepted")
	}
	var firstID string
	for _, sp := range c.Profiles {
		if sp.ServerURL == "https://one.example" {
			firstID = sp.ID
		}
	}
	rec = httptest.NewRecorder()
	p.handleServers(rec, httptest.NewRequest(http.MethodPost, "/local/servers/switch", strings.NewReader(`{"id":"`+firstID+`"}`)))
	if rec.Code != 200 || !p.sess().hasCookies() {
		t.Fatal("switching back lost the first server's session")
	}
	if !strings.Contains(rec.Body.String(), `"active":"`+firstID+`"`) {
		t.Fatalf("listing: %s", rec.Body.String())
	}
}

// /sp/<id>/ reaches that profile's server with that profile's cookies, while
// /proxy/ stays on the active one.
func TestProfileProxyRoutesPerServer(t *testing.T) {
	dir := t.TempDir()
	oldConfigDir, oldConfigFile := configDir, configFile
	configDir, configFile = dir, filepath.Join(dir, "config.json")
	defer func() { configDir, configFile = oldConfigDir, oldConfigFile }()

	echo := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, _ := r.Cookie("sid")
			v := ""
			if c != nil {
				v = c.Value
			}
			w.Write([]byte(name + " " + r.URL.Path + " sid=" + v))
		}))
	}
	one, two := echo("one"), echo("two")
	defer one.Close()
	defer two.Close()

	c := &Config{ServerURL: one.URL}
	c.normalizeProfiles()
	p := &Proxy{cfg: c, dir: dir}
	p.session = loadSession(p.sessionPath())
	p.stores = map[string]*SessionStore{c.ActiveID: p.session}
	p.sess().setCookies([]*http.Cookie{{Name: "sid", Value: "s1"}})
	firstID := c.ActiveID

	p.mu.Lock()
	second := c.addProfile(two.URL)
	secondID := second.ID
	p.storeForLocked(second).setCookies([]*http.Cookie{{Name: "sid", Value: "s2"}})
	p.mu.Unlock()

	get := func(h http.HandlerFunc, path string) string {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Body.String()
	}
	if got := get(p.handleProfileProxy, "/sp/"+secondID+"/api/x"); got != "two /api/x sid=s2" {
		t.Fatalf("/sp second: %q", got)
	}
	if got := get(p.handleProxy, "/proxy/api/y"); got != "one /api/y sid=s1" {
		t.Fatalf("/proxy active: %q", got)
	}
	if got := get(p.handleProfileProxy, "/sp/"+firstID+"/api/z"); got != "one /api/z sid=s1" {
		t.Fatalf("/sp first: %q", got)
	}
	rec := httptest.NewRecorder()
	p.handleProfileProxy(rec, httptest.NewRequest(http.MethodGet, "/sp/unknown/api/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatal("unknown profile proxied")
	}
	// Switching keeps one store per profile: the second's cookies survive.
	if !p.switchProfile(secondID) || get(p.handleProxy, "/proxy/a") != "two /a sid=s2" {
		t.Fatal("switch lost the second server's store")
	}
	if got := get(p.handleProfileProxy, "/sp/"+firstID+"/b"); got != "one /b sid=s1" {
		t.Fatalf("first server after switch: %q", got)
	}
}

func useTempConfig(t *testing.T) string {
	dir := t.TempDir()
	oldConfigDir, oldConfigFile := configDir, configFile
	configDir, configFile = dir, filepath.Join(dir, "config.json")
	t.Cleanup(func() { configDir, configFile = oldConfigDir, oldConfigFile })
	return dir
}

// Typing another profile's URL on the sign-in page switches to that profile;
// the active one keeps its own URL, even though the page posted partial URLs
// while the operator typed (go-6).
func TestLoginEditSwitchesToExistingProfile(t *testing.T) {
	dir := useTempConfig(t)
	c := &Config{ServerURL: "https://a.example", Username: "ua"}
	c.normalizeProfiles()
	p := newProxy(c, dir, 0)
	p.sess().setCookies([]*http.Cookie{{Name: "sid", Value: "expired-a"}})
	p.mu.Lock()
	bID := c.addProfile("https://b.example").ID
	p.mu.Unlock()
	aID := c.ActiveID

	post := func(body string) {
		rec := httptest.NewRecorder()
		p.handleConfig(rec, httptest.NewRequest(http.MethodPost, "/local/config", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("config: %d", rec.Code)
		}
	}
	post(`{"serverUrl":"https://b.exa"}`) // a pause while typing
	if c.ActiveID != aID || c.active().ServerURL != "https://b.exa" {
		t.Fatalf("partial URL: active %s url %s", c.ActiveID, c.active().ServerURL)
	}
	post(`{"serverUrl":"https://b.example/","username":"ub"}`)
	if c.ActiveID != bID || c.ServerURL != "https://b.example" || c.active().Username != "ub" {
		t.Fatalf("not switched to the existing profile: %+v", c)
	}
	if a := c.profileByID(aID); a == nil || a.ServerURL != "https://a.example" || a.Name != "a.example" || a.Username != "ua" {
		t.Fatalf("first profile not restored: %+v", a)
	}
	if len(c.Profiles) != 2 {
		t.Fatalf("profiles: %+v", c.Profiles)
	}

	// A new URL that was signed in on (its store got cookies) is kept when
	// the operator later leaves it.
	post(`{"serverUrl":"https://c.example"}`)
	p.sess().setCookies([]*http.Cookie{{Name: "sid", Value: "c"}})
	post(`{"serverUrl":"https://a.example"}`)
	if c.ActiveID != aID {
		t.Fatalf("not switched back: %s", c.ActiveID)
	}
	if sp := c.profileByURL("https://c.example"); sp == nil || sp.ID != bID {
		t.Fatalf("signed-in edit lost: %+v", c.Profiles)
	}
}

// The local proxy serves only this app's WebView: other local users, tools
// and web pages get nothing (go-1).
func TestLocalProxyRequiresWebView(t *testing.T) {
	dir := useTempConfig(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("cookie=" + r.Header.Get("Cookie")))
	}))
	defer upstream.Close()
	c := &Config{ServerURL: upstream.URL}
	c.normalizeProfiles()
	p := newProxy(c, dir, 4321)
	p.sess().setCookies([]*http.Cookie{{Name: "sid", Value: "s1"}})

	do := func(method, path, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"serverUrl":"https://evil.example"}`))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, r)
		return rec
	}
	for _, path := range []string{"/", "/local/servers", "/proxy/api/x", "/sp/" + c.ActiveID + "/api/x", "/local/config"} {
		if rec := do(http.MethodGet, path, "", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without the secret: %d", path, rec.Code)
		}
	}
	if rec := do(http.MethodGet, entryPath+"?k=wrong", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("entry with a wrong secret: %d", rec.Code)
	}
	rec := do(http.MethodGet, strings.TrimPrefix(p.entryURL(), p.origin), "", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("entry: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var auth *http.Cookie
	for _, ck := range (&http.Response{Header: rec.Header()}).Cookies() {
		if ck.Name == "oblireach_k_4321" {
			auth = ck
		}
	}
	if auth == nil || !auth.HttpOnly || auth.SameSite != http.SameSiteLaxMode || auth.Value != p.secret {
		t.Fatalf("entry cookie: %+v", auth)
	}
	if rec := do(http.MethodGet, "/local/servers", "", auth); rec.Code != 200 {
		t.Fatalf("WebView GET: %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/local/servers", p.origin, auth); rec.Code != 200 {
		t.Fatalf("WebView same-origin request: %d", rec.Code)
	}
	// A web page's request (a text/plain POST needs no preflight) is refused
	// whatever cookie it carries, and no CORS header is ever sent.
	for _, o := range []string{"https://evil.example", "null", "http://127.0.0.1:9999"} {
		rec := do(http.MethodPost, "/local/servers/add", o, auth)
		if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("origin %s: %d", o, rec.Code)
		}
	}
	if len(c.Profiles) != 1 {
		t.Fatal("a foreign origin added a server")
	}
	// The SSO page redirects to the callback without the cookie.
	if rec := do(http.MethodGet, "/sso/callback", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "SSO Failed") {
		t.Fatalf("sso callback: %d", rec.Code)
	}
	// The local secret never reaches the server; the profile's cookie does.
	rec = do(http.MethodGet, "/proxy/api/x", "", auth)
	if body := rec.Body.String(); rec.Code != 200 || strings.Contains(body, "oblireach_k_") || !strings.Contains(body, "sid=s1") ||
		rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("proxied: %d %q", rec.Code, body)
	}
}

// Parallel Set-Cookie responses on one store used to crash the app
// ("concurrent map iteration and map write", go-3).
func TestSessionStoreConcurrentSetCookies(t *testing.T) {
	s := loadSession(filepath.Join(t.TempDir(), "session.json"))
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				s.setCookies([]*http.Cookie{{Name: fmt.Sprintf("c%d_%d", g, i), Value: "v"}})
			}
		}(g)
	}
	wg.Wait()
	var saved struct {
		Cookies map[string]string `json:"cookies"`
	}
	data, _ := os.ReadFile(s.path)
	if err := json.Unmarshal(data, &saved); err != nil || len(saved.Cookies) != 8*40 {
		t.Fatalf("saved %d cookies (%v)", len(saved.Cookies), err)
	}
}

// A request in flight during a server switch never carries one server's
// cookies to the other (go-2).
func TestProxySwitchNeverMixesCookies(t *testing.T) {
	dir := useTempConfig(t)
	echo := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v := ""
			if ck, _ := r.Cookie("sid"); ck != nil {
				v = ck.Value
			}
			w.Write([]byte(name + " sid=" + v))
		}))
	}
	one, two := echo("one"), echo("two")
	defer one.Close()
	defer two.Close()
	c := &Config{ServerURL: one.URL}
	c.normalizeProfiles()
	p := newProxy(c, dir, 0)
	p.sess().setCookies([]*http.Cookie{{Name: "sid", Value: "one"}})
	firstID := c.ActiveID
	p.mu.Lock()
	second := c.addProfile(two.URL)
	secondID := second.ID
	p.storeForLocked(second).setCookies([]*http.Cookie{{Name: "sid", Value: "two"}})
	p.mu.Unlock()

	done := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var bad []string
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				rec := httptest.NewRecorder()
				p.handleProxy(rec, httptest.NewRequest(http.MethodGet, "/proxy/api/x", nil))
				if b := rec.Body.String(); b != "one sid=one" && b != "two sid=two" {
					mu.Lock()
					bad = append(bad, b)
					mu.Unlock()
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		id := firstID
		if i%2 == 0 {
			id = secondID
		}
		p.switchProfile(id)
	}
	close(done)
	wg.Wait()
	if len(bad) > 0 {
		t.Fatalf("%d mixed requests, e.g. %q", len(bad), bad[0])
	}
}

// The listing names the profile that owns the pre-profiles keys (go-5): not
// the first one once that one has been removed.
func TestServersListingLegacyFlag(t *testing.T) {
	dir := useTempConfig(t)
	c := &Config{ServerURL: "https://a.example"}
	c.normalizeProfiles()
	p := newProxy(c, dir, 0)
	aID := c.ActiveID
	list := func(path, body string) map[string]bool {
		rec := httptest.NewRecorder()
		p.handleServers(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		var out struct {
			Profiles []serverView `json:"profiles"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		m := map[string]bool{}
		for _, v := range out.Profiles {
			m[v.ServerURL] = v.Legacy
		}
		return m
	}
	if m := list("/local/servers/add", `{"serverUrl":"https://b.example"}`); len(m) != 2 || !m["https://a.example"] || m["https://b.example"] {
		t.Fatalf("legacy flags: %v", m)
	}
	if m := list("/local/servers/remove", `{"id":"`+aID+`"}`); len(m) != 1 || m["https://b.example"] {
		t.Fatalf("after removing the legacy profile: %v", m)
	}
}

// A "local" profile (the RDP/SSH connection tree, not a real Obliance
// server): created with a name only, listed with Local:true and an empty
// ServerURL/HasSession, and any HTTP forward attempted against it fails
// cleanly instead of crashing or reaching some other server's session.
func TestLocalServerProfile(t *testing.T) {
	dir := useTempConfig(t)
	c := &Config{ServerURL: "https://a.example"}
	c.normalizeProfiles()
	p := newProxy(c, dir, 0)

	rec := httptest.NewRecorder()
	p.handleServers(rec, httptest.NewRequest(http.MethodPost, "/local/servers/add-local", strings.NewReader(`{"name":"Mes connexions"}`)))
	var out struct {
		Active   string       `json:"active"`
		Profiles []serverView `json:"profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var local *serverView
	for i := range out.Profiles {
		if out.Profiles[i].Local {
			local = &out.Profiles[i]
		}
	}
	if local == nil {
		t.Fatalf("no local profile in listing: %+v", out.Profiles)
	}
	if local.Name != "Mes connexions" || local.ServerURL != "" || local.HasSession {
		t.Fatalf("local profile = %+v", local)
	}
	if out.Active != local.ID {
		t.Fatalf("adding a local profile did not make it active: active=%s local=%s", out.Active, local.ID)
	}

	// An empty name is refused (400), no profile created.
	rec2 := httptest.NewRecorder()
	p.handleServers(rec2, httptest.NewRequest(http.MethodPost, "/local/servers/add-local", strings.NewReader(`{"name":"  "}`)))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("empty name: status = %d", rec2.Code)
	}

	// The local profile is now active: any attempt to forward an HTTP
	// request to "the active server" must fail cleanly (never dial out,
	// never reach another profile's session/cookies).
	serverURL, store := p.activeTarget()
	if serverURL != "" || store == nil {
		t.Fatalf("activeTarget for a local profile = %q, store nil=%v", serverURL, store == nil)
	}
	frec := httptest.NewRecorder()
	p.forward(frec, httptest.NewRequest(http.MethodGet, "/devices", nil), "", serverURL, store)
	if frec.Code != http.StatusInternalServerError {
		t.Fatalf("forward on a local profile: status = %d, body = %s", frec.Code, frec.Body.String())
	}
}

// A local base is never an Obliance server: adding
// one in multi-server mode puts single mode on it (the top bar said "All
// servers" while the page showed its connections); "All servers" needs a
// real server; in multi-server mode the active profile (the account of the
// top bar) is always a real server; the theme of multi-server mode is the
// first real server's.
func TestLocalBaseNeverAServer(t *testing.T) {
	dir := useTempConfig(t)
	c := &Config{ServerURL: "https://a.example"}
	c.normalizeProfiles()
	p := newProxy(c, dir, 0)
	post := func(path, body string) int {
		rec := httptest.NewRecorder()
		p.handleServers(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec.Code
	}
	if code := post("/local/servers/add", `{"serverUrl":"https://b.example"}`); code != 200 {
		t.Fatalf("add: %d", code)
	}
	if code := post("/local/servers/mode", `{"mode":"multi"}`); code != 200 || c.serverMode() != "multi" {
		t.Fatalf("multi: %d %s", code, c.serverMode())
	}
	if code := post("/local/servers/add-local", `{"name":"Local"}`); code != 200 {
		t.Fatalf("add-local: %d", code)
	}
	if c.serverMode() != "single" || !c.active().IsLocal() {
		t.Fatalf("after add-local: mode %s, active %+v", c.serverMode(), c.active())
	}
	localID := c.ActiveID

	// "All servers" again: the active profile goes back to a real server.
	if code := post("/local/servers/mode", `{"mode":"multi"}`); code != 200 {
		t.Fatalf("multi again: %d", code)
	}
	if a := c.active(); a == nil || a.IsLocal() || c.serverMode() != "multi" {
		t.Fatalf("multi with a local active: %+v", a)
	}
	// Switching to the local base with mode multi: a real server stays active.
	if code := post("/local/servers/switch", `{"id":"`+localID+`","mode":"multi"}`); code != 200 || c.active().IsLocal() {
		t.Fatalf("switch local+multi: %d %+v", code, c.active())
	}
	// ... with mode single: the local base is on screen.
	if code := post("/local/servers/switch", `{"id":"`+localID+`","mode":"single"}`); code != 200 || !c.active().IsLocal() || c.serverMode() != "single" {
		t.Fatalf("switch local single: %d %+v %s", code, c.active(), c.serverMode())
	}

	// Theme of multi-server mode: the first REAL server's, even when a local
	// base is first in the list.
	c2 := &Config{Profiles: []ServerProfile{
		{ID: "l", Kind: profileKindLocal, Name: "Local"},
		{ID: "a", ServerURL: "https://a.example", AccountTheme: "obli-dim"},
	}, ActiveID: "l", ServerMode: "multi"}
	if src := c2.themeSource(); src == nil || src.ID != "a" {
		t.Fatalf("themeSource = %+v", src)
	}
	// A config saved with a local base active in multi-server mode is
	// repaired at load.
	if !c2.normalizeProfiles() || c2.ActiveID != "a" {
		t.Fatalf("normalize: active %s", c2.ActiveID)
	}
	// Local bases alone: no "All servers".
	c3 := &Config{Profiles: []ServerProfile{{ID: "l1", Kind: profileKindLocal}, {ID: "l2", Kind: profileKindLocal}}, ActiveID: "l1", ServerMode: "multi"}
	if c3.multiPossible() || c3.serverMode() != "single" || c3.themeSource() == nil || c3.themeSource().ID != "l1" {
		t.Fatalf("local bases only: possible=%v mode=%s", c3.multiPossible(), c3.serverMode())
	}
}

// The sign-in page never turns a local base into a server: an address typed
// while it is active goes to that server's own profile; partial addresses
// (typing) change nothing.
func TestLoginEditKeepsLocalBase(t *testing.T) {
	dir := useTempConfig(t)
	c := &Config{ServerURL: "https://a.example"}
	c.normalizeProfiles()
	p := newProxy(c, dir, 0)
	p.mu.Lock()
	p.switchLocked(c.addLocalProfile("Local").ID)
	p.mu.Unlock()
	localID := c.ActiveID
	post := func(body string) {
		rec := httptest.NewRecorder()
		p.handleConfig(rec, httptest.NewRequest(http.MethodPost, "/local/config", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("config: %d", rec.Code)
		}
	}
	post(`{"serverUrl":"https:/"}`) // (partial, not an address yet: nothing)
	if l := c.profileByID(localID); l == nil || !l.IsLocal() || l.ServerURL != "" || c.ActiveID != localID {
		t.Fatalf("partial address changed the local base: %+v", l)
	}
	post(`{"serverUrl":"https://new.example","username":"u"}`)
	if l := c.profileByID(localID); l == nil || !l.IsLocal() || l.ServerURL != "" || l.Username != "" {
		t.Fatalf("local base rewritten: %+v", l)
	}
	if a := c.active(); a == nil || a.IsLocal() || a.ServerURL != "https://new.example" || a.Username != "u" {
		t.Fatalf("new server profile: %+v", a)
	}
	post(`{"serverUrl":"https://a.example"}`) // an existing server: switched to it
	if a := c.active(); a == nil || a.ServerURL != "https://a.example" || len(c.Profiles) != 3 {
		t.Fatalf("existing: %+v (%d profiles)", a, len(c.Profiles))
	}
}
