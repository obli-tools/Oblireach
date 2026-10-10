package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Multi-server mode: POST /local/servers/mode, the mode with a
// switch, back to single mode when one server is left; the theme's source
// follows the mode.
func TestServersMode(t *testing.T) {
	dir := useTempConfig(t)
	defer setCurrentPalette(defaultTheme)
	cfg := &Config{ServerURL: "https://a.example", Profiles: []ServerProfile{
		{ID: "pa", ServerURL: "https://a.example", Name: "a", SessionFile: "session.json", AccountTheme: "obli-dim"},
		{ID: "pb", ServerURL: "https://b.example", Name: "b", SessionFile: "session-pb.json", AccountTheme: "neon"},
		{ID: "pc", ServerURL: "https://c.example", Name: "c", SessionFile: "session-pc.json"},
	}, ActiveID: "pb"}
	p := newProxy(cfg, dir, 0)
	var told []string
	p.setThemeHook(func(th string) { told = append(told, th) })
	call := func(path, body string) (int, map[string]interface{}) {
		rec := httptest.NewRecorder()
		method := http.MethodPost
		if body == "" {
			method = http.MethodGet
		}
		p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(method, path, strings.NewReader(body))))
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := call("/local/servers", ""); code != 200 || out["mode"] != "single" || out["theme"] != "neon" {
		t.Fatalf("GET: %d %v", code, out)
	}
	// Multi: saved, the first server's theme worn.
	if code, out := call("/local/servers/mode", `{"mode":"multi"}`); code != 200 || out["mode"] != "multi" || out["active"] != "pb" || out["theme"] != "obli-dim" ||
		cfg.ServerMode != "multi" || strings.Join(told, ",") != "obli-dim" {
		t.Fatalf("mode multi: %d %v told=%v", code, out, told)
	}
	// Refused: an unknown mode, none at all, GET.
	for _, b := range []string{`{"mode":"both"}`, `{}`, `not json`} {
		if code, _ := call("/local/servers/mode", b); code != 400 || cfg.ServerMode != "multi" {
			t.Fatalf("mode %s: %d (mode now %q)", b, code, cfg.ServerMode)
		}
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(http.MethodGet, "/local/servers/mode", nil)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mode: %d", rec.Code)
	}
	// A switch keeps the mode unless it gives one.
	if code, out := call("/local/servers/switch", `{"id":"pc"}`); code != 200 || out["mode"] != "multi" || out["active"] != "pc" {
		t.Fatalf("switch keeps multi: %d %v", code, out)
	}
	if code, out := call("/local/servers/switch", `{"id":"pb","mode":"single"}`); code != 200 || out["mode"] != "single" || out["active"] != "pb" ||
		out["theme"] != "neon" || told[len(told)-1] != "neon" {
		t.Fatalf("switch to single: %d %v told=%v", code, out, told)
	}
	if code, _ := call("/local/servers/switch", `{"id":"pa","mode":"bogus"}`); code != 400 {
		t.Fatalf("switch with a bad mode: %d", code)
	}
	if code, _ := call("/local/servers/switch", `{"id":"zz","mode":"multi"}`); code != 404 || cfg.ServerMode != "" {
		t.Fatalf("unknown server: %d (mode %q)", code, cfg.ServerMode)
	}
	call("/local/servers/switch", `{"id":"pb","mode":"multi"}`)
	// Down to one server: single mode (multi needs two).
	call("/local/servers/remove", `{"id":"pa"}`)
	if cfg.ServerMode != "multi" {
		t.Fatalf("two servers left: mode %q", cfg.ServerMode)
	}
	if code, out := call("/local/servers/remove", `{"id":"pc"}`); code != 200 || out["mode"] != "single" || cfg.ServerMode != "" {
		t.Fatalf("one server left: %d %v", code, out)
	}
	if code, _ := call("/local/servers/mode", `{"mode":"multi"}`); code != 400 || cfg.ServerMode != "" {
		t.Fatalf("multi with one server: %d", code)
	}
	if code, _ := call("/local/servers/switch", `{"id":"pb","mode":"multi"}`); code != 400 || cfg.ServerMode != "" {
		t.Fatalf("switch to multi with one server: %d", code)
	}
}

// Every change of the servers (switch, mode, add, remove, the sign-in page
// moving to another server) tells the windows once (the session windows
// follow the mode and the active server); a read, a refused change, or a
// sign-in page edit that stays on the same server tells nothing.
func TestServersHook(t *testing.T) {
	dir := useTempConfig(t)
	defer setCurrentPalette(defaultTheme)
	cfg := &Config{ServerURL: "https://a.example", Profiles: []ServerProfile{
		{ID: "pa", ServerURL: "https://a.example", Name: "a", SessionFile: "session.json"},
		{ID: "pb", ServerURL: "https://b.example", Name: "b", SessionFile: "session-pb.json"},
	}, ActiveID: "pa"}
	p := newProxy(cfg, dir, 0)
	n := 0
	p.setServersHook(func() { n++ })
	call := func(method, path, body string) int {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(method, path, strings.NewReader(body))))
		return rec.Code
	}
	steps := []struct {
		method, path, body string
		code, told         int
	}{
		{http.MethodGet, "/local/servers", "", 200, 0},
		{http.MethodPost, "/local/servers/mode", `{"mode":"multi"}`, 200, 1},
		{http.MethodPost, "/local/servers/mode", `{"mode":"both"}`, 400, 0},
		{http.MethodPost, "/local/servers/switch", `{"id":"pb"}`, 200, 1},
		{http.MethodPost, "/local/servers/switch", `{"id":"zz"}`, 404, 0},
		{http.MethodPost, "/local/servers/add", `{"serverUrl":"https://c.example"}`, 200, 1},
		{http.MethodPost, "/local/servers/remove", `{"id":"pa"}`, 200, 1},
		{http.MethodPost, "/local/config", `{"username":"op"}`, 200, 0},                 // same server
		{http.MethodPost, "/local/config", `{"serverUrl":"https://b.example"}`, 200, 1}, // another profile's URL: switched to it
		{http.MethodGet, "/local/config", "", 200, 0},
	}
	for _, st := range steps {
		was := n
		if code := call(st.method, st.path, st.body); code != st.code || n-was != st.told {
			t.Fatalf("%s %s %s: %d, told %d times (want %d, %d)", st.method, st.path, st.body, code, n-was, st.code, st.told)
		}
	}
}

// fakeObliance: a server whose ONE session is on a tenant (POST
// /api/tenant/switch), and whose other requests say which tenant they ran on.
type fakeObliance struct {
	mu      sync.Mutex
	tenant  string
	log     []string
	deny    map[string]bool // tenants the user may not switch to
	status  int             // non-zero: every switch answers this
	slow    time.Duration   // how long a request takes (interleaving test)
	cookies []string        // the session cookie of each request
}

func (f *fakeObliance) handler(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("sid")
	sid := ""
	if c != nil {
		sid = c.Value
	}
	if r.URL.Path == "/api/tenant/switch" {
		var b struct {
			TenantID int `json:"tenantId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		tid := fmt.Sprint(b.TenantID)
		f.mu.Lock()
		f.log = append(f.log, "switch-"+tid)
		f.cookies = append(f.cookies, sid)
		st, deny := f.status, f.deny[tid]
		if st == 0 && !deny {
			f.tenant = tid
		}
		f.mu.Unlock()
		switch {
		case st != 0:
			w.WriteHeader(st)
			_, _ = io.WriteString(w, `{"error":"Authentication required"}`)
		case deny:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"Access denied to this tenant"}`)
		default:
			_, _ = io.WriteString(w, `{"success":true}`)
		}
		return
	}
	f.mu.Lock()
	on := f.tenant
	f.mu.Unlock()
	if f.slow > 0 {
		time.Sleep(f.slow)
	}
	f.mu.Lock()
	f.log = append(f.log, "req-"+r.URL.Query().Get("t")+"@"+on)
	f.cookies = append(f.cookies, sid)
	f.mu.Unlock()
	_, _ = io.WriteString(w, `{"tenant":"`+on+`"}`)
}

func (f *fakeObliance) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.log
	f.log = nil
	return l
}

func pinProxy(t *testing.T, f *fakeObliance) (*Proxy, *httptest.Server) {
	dir := useTempConfig(t)
	up := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(up.Close)
	cfg := &Config{ServerURL: "https://active.example", Profiles: []ServerProfile{
		{ID: "pact", ServerURL: "https://active.example", Name: "act", SessionFile: "session.json"},
		{ID: "pa", ServerURL: up.URL, Name: "a", SessionFile: "session-pa.json"},
	}, ActiveID: "pact", ServerMode: "multi"} // (pinned requests align in multi-server mode only)
	p := newProxy(cfg, dir, 0)
	_, store := p.profileTarget("pa")
	store.setCookies([]*http.Cookie{{Name: "sid", Value: "s1"}})
	return p, up
}

func pinGet(p *Proxy, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	p.ServeHTTP(rec, withSecret(p, r))
	return rec
}

// /spt/<profile>/<tenant>/...: the server session is put on the device's
// tenant first (once, then remembered), never interleaved with another
// pinned request of that server.
func TestTenantPinProxy(t *testing.T) {
	f := &fakeObliance{tenant: "1", deny: map[string]bool{"9": true}}
	p, _ := pinProxy(t, f)

	if rec := pinGet(p, http.MethodGet, "/spt/pa/5/api/x?t=5", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tenant":"5"`) {
		t.Fatalf("first pinned request: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(f.take(), ","); got != "switch-5,req-5@5" {
		t.Fatalf("switch then request: %s", got)
	}
	// Known to be there: no second switch.
	pinGet(p, http.MethodPost, "/spt/pa/5/api/y?t=5", "{}", nil)
	if got := strings.Join(f.take(), ","); got != "req-5@5" {
		t.Fatalf("aligned already: %s", got)
	}
	// Another tenant: switched again.
	pinGet(p, http.MethodGet, "/spt/pa/6/api/x?t=6", "", nil)
	if got := strings.Join(f.take(), ","); got != "switch-6,req-6@6" {
		t.Fatalf("another tenant: %s", got)
	}
	// The page switching the tenant itself (through /sp/): forgotten, the
	// next pinned request aligns again.
	if rec := pinGet(p, http.MethodPost, "/sp/pa/api/tenant/switch", `{"tenantId":7}`, map[string]string{"Content-Type": "application/json"}); rec.Code != 200 {
		t.Fatalf("passthrough switch: %d", rec.Code)
	}
	pinGet(p, http.MethodGet, "/spt/pa/6/api/x?t=6", "", nil)
	if got := strings.Join(f.take(), ","); got != "switch-7,switch-6,req-6@6" {
		t.Fatalf("after a passthrough switch: %s", got)
	}
	// A new sign-in (other cookies): a new session, aligned again.
	_, store := p.profileTarget("pa")
	store.setCookies([]*http.Cookie{{Name: "sid", Value: "s2"}})
	pinGet(p, http.MethodGet, "/spt/pa/6/api/x?t=6", "", nil)
	if got := strings.Join(f.take(), ","); got != "switch-6,req-6@6" {
		t.Fatalf("after a new sign-in: %s", got)
	}
	f.mu.Lock()
	last := f.cookies[len(f.cookies)-1]
	f.mu.Unlock()
	if last != "s2" {
		t.Fatalf("the profile's cookies were not sent: %q", last)
	}
	// A tenant the user may not reach: 403 "tenant access denied", nothing
	// forwarded; the next request aligns again.
	rec := pinGet(p, http.MethodGet, "/spt/pa/9/api/x?t=9", "", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenant access denied") {
		t.Fatalf("denied tenant: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(f.take(), ","); got != "switch-9" {
		t.Fatalf("denied tenant forwarded: %s", got)
	}
	// Signed out upstream: its 401 passed on as it is.
	f.status = http.StatusUnauthorized
	rec = pinGet(p, http.MethodGet, "/spt/pa/5/api/x?t=5", "", nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Authentication required") {
		t.Fatalf("401: %d %s", rec.Code, rec.Body.String())
	}
	f.status = 0
	f.take()
	// Refused: a WebSocket (a tunnel would hold the lock), a bad tenant id,
	// an unknown profile, a path without anything after the tenant.
	if rec := pinGet(p, http.MethodGet, "/spt/pa/5/api/remote/tunnel/x", "", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("websocket: %d", rec.Code)
	}
	for _, bad := range []string{"/spt/pa/0/api/x", "/spt/pa/-1/api/x", "/spt/pa/abc/api/x", "/spt/pa/12345678901/api/x", "/spt/pa/05/api/x"} {
		if rec := pinGet(p, http.MethodGet, bad, "", nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
	if rec := pinGet(p, http.MethodGet, "/spt/zz/5/api/x", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile: %d", rec.Code)
	}
	if rec := pinGet(p, http.MethodGet, "/spt/pa/5/", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no path: %d", rec.Code)
	}
	if got := f.take(); len(got) != 0 {
		t.Fatalf("a refused request reached the server: %v", got)
	}
	// Removing the profile forgets its alignment.
	p.pinFor("pa")
	p.forgetPin("pa")
	p.pinsMu.Lock()
	_, kept := p.pins["pa"]
	p.pinsMu.Unlock()
	if kept {
		t.Fatal("pin kept")
	}
}

// Two pinned requests for two tenants at once, from two windows: the server
// never sees one's switch between the other's switch and request.
func TestTenantPinSerialized(t *testing.T) {
	f := &fakeObliance{tenant: "1", slow: 30 * time.Millisecond}
	p, _ := pinProxy(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		for _, tid := range []string{"5", "6"} {
			wg.Add(1)
			go func(tid string) {
				defer wg.Done()
				rec := pinGet(p, http.MethodGet, "/spt/pa/"+tid+"/api/x?t="+tid, "", nil)
				if !strings.Contains(rec.Body.String(), `"tenant":"`+tid+`"`) {
					t.Errorf("request for %s ran on %s", tid, rec.Body.String())
				}
			}(tid)
		}
	}
	wg.Wait()
	for _, e := range f.take() {
		if strings.HasPrefix(e, "req-") {
			parts := strings.SplitN(strings.TrimPrefix(e, "req-"), "@", 2)
			if parts[0] != parts[1] {
				t.Fatalf("interleaved: %s", e)
			}
		}
	}
}

// /local/tenant-align: the alignment alone (before a chat:open).
func TestTenantAlign(t *testing.T) {
	f := &fakeObliance{tenant: "1", deny: map[string]bool{"9": true}}
	p, _ := pinProxy(t, f)
	align := func(body string) *httptest.ResponseRecorder {
		return pinGet(p, http.MethodPost, "/local/tenant-align", body, map[string]string{"Content-Type": "application/json"})
	}
	if rec := align(`{"profile":"pa","tenantId":5}`); rec.Code != 200 || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("align: %d %s", rec.Code, rec.Body.String())
	}
	align(`{"profile":"pa","tenantId":5}`)
	if got := strings.Join(f.take(), ","); got != "switch-5" {
		t.Fatalf("aligned once: %s", got)
	}
	// The next pinned request on that tenant needs no switch.
	pinGet(p, http.MethodGet, "/spt/pa/5/api/x?t=5", "", nil)
	if got := strings.Join(f.take(), ","); got != "req-5@5" {
		t.Fatalf("after align: %s", got)
	}
	if rec := align(`{"profile":"pa","tenantId":9}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenant access denied") {
		t.Fatalf("denied: %d %s", rec.Code, rec.Body.String())
	}
	for _, b := range []string{`{"profile":"pa","tenantId":0}`, `{"profile":"pa","tenantId":"5; x"}`, `{"profile":"pa"}`, `nope`, `{"profile":"pa","tenantId":1.5}`} {
		if rec := align(b); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", b, rec.Code)
		}
	}
	if rec := align(`{"profile":"zz","tenantId":5}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile: %d", rec.Code)
	}
	if rec := pinGet(p, http.MethodGet, "/local/tenant-align", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
}

// Single mode (the app's): the session's tenant is the main window's tenant
// menu's. A request still pinned (a session window that has not heard of the
// mode change) is forwarded as it is, never switched; the alignment asked
// alone does nothing. Back in multi-server mode, pinning aligns again.
func TestTenantPinSingleMode(t *testing.T) {
	f := &fakeObliance{tenant: "2"}
	p, _ := pinProxy(t, f)
	p.mu.Lock()
	p.cfg.ServerMode = ""
	p.mu.Unlock()
	if rec := pinGet(p, http.MethodGet, "/spt/pa/5/api/x?t=5", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tenant":"2"`) {
		t.Fatalf("single mode, pinned request: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(f.take(), ","); got != "req-5@2" {
		t.Fatalf("single mode: the session was moved: %s", got)
	}
	if rec := pinGet(p, http.MethodPost, "/local/tenant-align", `{"profile":"pa","tenantId":5}`, map[string]string{"Content-Type": "application/json"}); rec.Code != 200 || rec.Body.String() != `{"ok":true,"aligned":false}` {
		t.Fatalf("single mode, align: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.take(); len(got) != 0 {
		t.Fatalf("single mode, align reached the server: %v", got)
	}
	// Still refused as before: a WebSocket, a bad tenant, an unknown profile.
	if rec := pinGet(p, http.MethodGet, "/spt/pa/5/api/remote/tunnel/x", "", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("single mode, websocket: %d", rec.Code)
	}
	if rec := pinGet(p, http.MethodGet, "/spt/pa/0/api/x", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("single mode, bad tenant: %d", rec.Code)
	}
	if rec := pinGet(p, http.MethodGet, "/spt/zz/5/api/x", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("single mode, unknown profile: %d", rec.Code)
	}
	// Multi-server mode again: aligned.
	p.mu.Lock()
	p.cfg.ServerMode = "multi"
	p.mu.Unlock()
	pinGet(p, http.MethodGet, "/spt/pa/5/api/x?t=5", "", nil)
	if got := strings.Join(f.take(), ","); got != "switch-5,req-5@5" {
		t.Fatalf("multi-server mode again: %s", got)
	}
}

// /local/logout {profile}: signs out of that server only.
func TestLogoutProfile(t *testing.T) {
	f := &fakeObliance{tenant: "1"}
	p, _ := pinProxy(t, f)
	p.sess().setCookies([]*http.Cookie{{Name: "sid", Value: "act"}})
	_, storeA := p.profileTarget("pa")
	if rec := pinGet(p, http.MethodPost, "/local/logout", `{"profile":"pa"}`, nil); rec.Code != 200 {
		t.Fatalf("logout pa: %d", rec.Code)
	}
	if storeA.hasCookies() || !p.sess().hasCookies() {
		t.Fatal("logout of pa: wrong store cleared")
	}
	if rec := pinGet(p, http.MethodPost, "/local/logout", `{"profile":"zz"}`, nil); rec.Code != http.StatusNotFound || !p.sess().hasCookies() {
		t.Fatalf("unknown profile: %d", rec.Code)
	}
	// Without a profile: the active one, as before.
	pinGet(p, http.MethodPost, "/local/logout", "", nil)
	if p.sess().hasCookies() {
		t.Fatal("logout without profile kept the active session")
	}
}
