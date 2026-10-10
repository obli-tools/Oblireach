package main

// Tenant alignment (multi-server mode). On Obliance, what a device
// action may do and where its records go follow the server session's CURRENT
// tenant (POST /api/remote/sessions: its restriction envelope, the session
// row's tenant; /api/reach/scripts; /api/scripts/:id/execute runs only on the
// devices of that tenant; chat:open reads it when emitted). In multi-server
// mode the Desktop lists every tenant of a server at once (GET
// /api/reach/overview?tenantId=all) without switching: an action on a device
// must then run with the server session on THAT device's tenant.
//
//   /spt/<profile>/<tenant id>/<path>  the reverse proxy of /sp/<profile>/,
//     after aligning that profile's server session on the tenant (POST
//     /api/tenant/switch, only when it is not known to be there already).
//     The switch and the request run under one lock per profile: pinned
//     requests of one server are serialized across every window, so another
//     request never moves the session to another tenant in between.
//   /local/tenant-align  POST { profile, tenantId } → { ok: true }: the
//     alignment alone (before a chat:open on the chat socket).
//
// Only in multi-server mode (the app's mode, not the page's): in single mode
// the session's tenant is the one the main window's tenant menu shows, and
// nothing may move it behind that menu. A pinned request of a page that has
// not heard of the change yet (a session window) is then forwarded as it is
// (as /sp/ would), and the alignment asked alone does nothing ({ ok: true,
// aligned: false }).
//
// What the session is on is remembered per profile (tenant, and a
// fingerprint of its cookies: a new sign-in is a new session), and forgotten
// when the page switches the tenant itself (a POST /api/tenant/switch through
// /proxy or /sp/: see noteTenantSwitch) or the profile is removed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// tenantPin: one profile's alignment state.
type tenantPin struct {
	mu     sync.Mutex // held for a switch and the request it serves
	tenant string     // the tenant the server session is on ("": not known)
	fp     string     // cookie fingerprint of that session
}

// validTenantID: an Obliance tenant id (a positive integer, at most 10 digits).
var validTenantIDRe = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

func validTenantID(s string) bool { return validTenantIDRe.MatchString(s) }

// fingerprint: the session's cookies, hashed (a new sign-in changes it).
func (s *SessionStore) fingerprint() string {
	s.mu.Lock()
	names := make([]string, 0, len(s.Cookies))
	for k := range s.Cookies {
		names = append(names, k)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, k := range names {
		fmt.Fprintf(h, "%s=%s\n", k, s.Cookies[k])
	}
	s.mu.Unlock()
	return hex.EncodeToString(h.Sum(nil))
}

func (p *Proxy) pinFor(pid string) *tenantPin {
	p.pinsMu.Lock()
	defer p.pinsMu.Unlock()
	if p.pins == nil {
		p.pins = map[string]*tenantPin{}
	}
	t := p.pins[pid]
	if t == nil {
		t = &tenantPin{}
		p.pins[pid] = t
	}
	return t
}

// forgetPin: a profile removed.
func (p *Proxy) forgetPin(pid string) {
	p.pinsMu.Lock()
	delete(p.pins, pid)
	p.pinsMu.Unlock()
}

// profileTarget: the server URL and cookie store of a profile (nil store:
// unknown profile).
func (p *Proxy) profileTarget(id string) (string, *SessionStore) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.cfg.Profiles {
		if p.cfg.Profiles[k].ID == id {
			return p.cfg.Profiles[k].ServerURL, p.storeForLocked(&p.cfg.Profiles[k])
		}
	}
	return "", nil
}

// multiMode: the app is in multi-server mode (servers.go serverMode).
func (p *Proxy) multiMode() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.serverMode() == "multi"
}

// activeProfileID: the active profile's id ("": none).
func (p *Proxy) activeProfileID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.ActiveID
}

// isTenantSwitch: the page switching its server session's tenant itself
// (the path is the one sent upstream, prefix removed).
func isTenantSwitch(r *http.Request, path string) bool {
	return r.Method == http.MethodPost && strings.TrimRight(path, "/") == "/api/tenant/switch"
}

// forwardSwitch forwards a tenant switch of the page (/proxy, /sp/) under
// the profile's lock, then forgets where that session is: pinned requests
// never run in between, and the next one aligns again.
func (p *Proxy) forwardSwitch(pid string, fwd func()) {
	if pid == "" {
		fwd()
		return
	}
	pin := p.pinFor(pid)
	pin.mu.Lock()
	defer pin.mu.Unlock()
	fwd()
	pin.tenant = ""
}

// pinHTTP: the client of the switch requests (a test may replace it).
var pinHTTP = &http.Client{Timeout: 15 * time.Second}

// ensureTenantLocked puts the profile's server session on tenant tid unless
// it is known to be there (same tenant, same cookies). Caller holds pin.mu.
// Returns 0 when aligned, else the status and JSON body to answer with.
func (p *Proxy) ensureTenantLocked(pin *tenantPin, tid, serverURL string, store *SessionStore) (int, string) {
	fp := store.fingerprint()
	if pin.tenant == tid && pin.fp == fp {
		return 0, ""
	}
	pin.tenant = ""
	origin, ok := serverOrigin(serverURL)
	if !ok {
		return http.StatusInternalServerError, `{"error":"invalid server url"}`
	}
	req, err := http.NewRequest(http.MethodPost, origin+"/api/tenant/switch", strings.NewReader(`{"tenantId":`+tid+`}`))
	if err != nil {
		return http.StatusInternalServerError, `{"error":"invalid server url"}`
	}
	req.Header.Set("Content-Type", "application/json")
	store.inject(req)
	resp, err := pinHTTP.Do(req)
	if err != nil {
		j, _ := json.Marshal(map[string]string{"error": "proxy: " + err.Error()})
		return http.StatusBadGateway, string(j)
	}
	defer resp.Body.Close()
	if cookies := resp.Cookies(); len(cookies) > 0 {
		store.setCookies(cookies)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		pin.tenant, pin.fp = tid, store.fingerprint()
		return 0, ""
	case resp.StatusCode == http.StatusForbidden:
		return http.StatusForbidden, `{"error":"tenant access denied"}`
	default:
		// 401 as it is (signed out, or a 2FA step the page knows how to ask).
		if len(body) == 0 || !json.Valid(body) {
			j, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("tenant switch: HTTP %d", resp.StatusCode)})
			body = j
		}
		return resp.StatusCode, string(body)
	}
}

// handleTenantProxy: /spt/<profile>/<tenant id>/<path>.
func (p *Proxy) handleTenantProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/spt/"), "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[2] == "" {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	pid, tid := parts[0], parts[1]
	if !validTenantID(tid) {
		http.Error(w, `{"error":"invalid tenant"}`, http.StatusBadRequest)
		return
	}
	// A tunnel lives on: it would hold the lock for its whole life. Sessions
	// are started pinned, their tunnel goes through /sp/.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, `{"error":"no websocket here"}`, http.StatusBadRequest)
		return
	}
	serverURL, store := p.profileTarget(pid)
	if store == nil {
		http.Error(w, `{"error":"unknown server"}`, http.StatusNotFound)
		return
	}
	prefix := "/spt/" + pid + "/" + tid
	if !p.multiMode() {
		// Single mode: never moved here (see the top of this file).
		if isTenantSwitch(r, "/"+parts[2]) {
			p.forwardSwitch(pid, func() { p.forward(w, r, prefix, serverURL, store) })
			return
		}
		p.forward(w, r, prefix, serverURL, store)
		return
	}
	pin := p.pinFor(pid)
	pin.mu.Lock()
	defer pin.mu.Unlock()
	if status, body := p.ensureTenantLocked(pin, tid, serverURL, store); status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return
	}
	if isTenantSwitch(r, "/"+parts[2]) {
		pin.tenant = "" // (the page switching through here: where it lands is not known)
	}
	p.forward(w, r, prefix, serverURL, store)
}

// handleTenantAlign: /local/tenant-align POST { profile, tenantId } → { ok }.
func (p *Proxy) handleTenantAlign(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Profile  string      `json:"profile"`
		TenantID json.Number `json:"tenantId"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil || !validTenantID(body.TenantID.String()) {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	pid := body.Profile
	if pid == "" {
		pid = p.activeProfileID()
	}
	serverURL, store := p.profileTarget(pid)
	if store == nil {
		http.Error(w, `{"error":"unknown server"}`, http.StatusNotFound)
		return
	}
	if !p.multiMode() {
		_, _ = io.WriteString(w, `{"ok":true,"aligned":false}`) // (single mode: see the top of this file)
		return
	}
	pin := p.pinFor(pid)
	pin.mu.Lock()
	status, msg := p.ensureTenantLocked(pin, body.TenantID.String(), serverURL, store)
	pin.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, msg)
		return
	}
	_, _ = io.WriteString(w, `{"ok":true}`)
}
