package main

// Server profiles (like the Obliance Android app): several Obliance
// servers, one active at a time. Each profile has its own session cookies and
// favourites. config.json keeps serverUrl / username as the ACTIVE profile's
// values, so the rest of the app (proxy, SSO, open-url) works unchanged.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type ServerProfile struct {
	ID          string `json:"id"`
	ServerURL   string `json:"serverUrl"`
	Username    string `json:"username,omitempty"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	SessionFile string `json:"sessionFile"`
	Favorites   string `json:"favoritesFile"`
	// Kind: "" (zero value, every profile before this field existed) is a
	// normal Obliance server, reached over HTTP with a real session. "local"
	// is a pure local RDP/SSH connection tree (vault.go's folder/ssh/rdp
	// entries, ParentID-keyed) — no ServerURL, no session, no account. Every
	// place that talks HTTP to the active profile (forward, the /sp/ proxy,
	// SSO, chat) keeps working unchanged for a local profile because its
	// ServerURL is simply "" and serverOrigin("") already fails cleanly
	// (proxy.go's forward: "invalid server url", not a crash).
	Kind string `json:"kind,omitempty"`
	// The preferred language of the Obliance account last signed in on this
	// server (reported by the page, i18n.go): the app's language when the
	// "Language" setting is automatic.
	AccountLanguage string `json:"accountLanguage,omitempty"`
	// The Obliance theme of that account (its preferredTheme, reported by the
	// page, theme.go): the app wears it when this server is the theme's
	// source (the active one; the first of the list in multi-server mode).
	AccountTheme string `json:"accountTheme,omitempty"`
}

// Identity colours: the Android app's closed palette (none reads as a status).
var serverPalette = []string{"violet", "teal", "fuchsia", "indigo", "cyan", "sand", "lavender", "mint"}

const profileKindLocal = "local"

// IsLocal: a pure local RDP/SSH connection tree, not a real Obliance server.
func (sp *ServerProfile) IsLocal() bool { return sp != nil && sp.Kind == profileKindLocal }

const (
	vaultLocalNameMax = 60
)

// addLocalProfile creates a "local" profile: a name only, no ServerURL, no
// session/favorites files (nothing to ever write there). name is trimmed;
// an empty one falls back to "Local" (the caller — handleServers — still
// validates non-emptiness of the raw input first so this is just a guard).
func (c *Config) addLocalProfile(name string) *ServerProfile {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Local"
	}
	if r := []rune(name); len(r) > vaultLocalNameMax {
		name = string(r[:vaultLocalNameMax])
	}
	id := newProfileID()
	p := ServerProfile{ID: id, Kind: profileKindLocal, Name: name, Color: c.nextColor()}
	c.Profiles = append(c.Profiles, p)
	return &c.Profiles[len(c.Profiles)-1]
}

func profileName(serverURL string) string {
	if u, err := url.Parse(serverURL); err == nil && u.Host != "" {
		return u.Host
	}
	return serverURL
}

func newProfileID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// serverOrigin: the scheme://host of a configured server URL, the only thing
// the app ever targets. The proxy has always kept just the scheme and host
// (forward); the SSO and logout calls use this too, so a path typed on the
// sign-in page (e.g. pasted from the browser) no longer sends them elsewhere.
func serverOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// validServerURL: a new server's address, an absolute http(s) URL with a host
// and nothing more (no path, query, fragment or credentials, which the app
// would silently drop). Returns the bare origin.
func validServerURL(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	o, ok := serverOrigin(raw)
	if !ok || !strings.EqualFold(o, raw) {
		return "", false
	}
	return o, true
}

func (c *Config) active() *ServerProfile {
	return c.profileByID(c.ActiveID)
}

// serverMode: "multi" (every server at once, Config.ServerMode, at least two
// profiles, one of them a real Obliance server) or "single" (the active
// profile only).
func (c *Config) serverMode() string {
	if c.ServerMode == "multi" && c.multiPossible() {
		return "multi"
	}
	return "single"
}

// multiPossible: "All servers" has something to show at once: two profiles
// or more, one of them at least a real Obliance server (local bases alone
// are one and the same connection tree: nothing to aggregate, no account).
func (c *Config) multiPossible() bool {
	return len(c.Profiles) >= 2 && c.firstRemote() != nil
}

// firstRemote: the first real Obliance server of the list (not a local
// base), nil when there is none.
func (c *Config) firstRemote() *ServerProfile {
	for i := range c.Profiles {
		if !c.Profiles[i].IsLocal() {
			return &c.Profiles[i]
		}
	}
	return nil
}

func (c *Config) profileByID(id string) *ServerProfile {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			return &c.Profiles[i]
		}
	}
	return nil
}

func (c *Config) nextColor() string {
	used := map[string]bool{}
	for _, p := range c.Profiles {
		used[p.Color] = true
	}
	for _, col := range serverPalette {
		if !used[col] {
			return col
		}
	}
	return serverPalette[len(c.Profiles)%len(serverPalette)]
}

// addProfile creates a profile for serverURL, or returns the existing one for
// the same URL. The first profile keeps the pre-profiles file names
// (session.json / favorites.json), so upgrading signs nobody out.
func (c *Config) addProfile(serverURL string) *ServerProfile {
	if p := c.profileByURL(serverURL); p != nil {
		return p
	}
	id := newProfileID()
	p := ServerProfile{ID: id, ServerURL: serverURL, Name: profileName(serverURL), Color: c.nextColor(),
		SessionFile: "session-" + id + ".json", Favorites: "favorites-" + id + ".json"}
	if len(c.Profiles) == 0 {
		p.SessionFile, p.Favorites = "session.json", "favorites.json"
	}
	c.Profiles = append(c.Profiles, p)
	return &c.Profiles[len(c.Profiles)-1]
}

// sameServer: two URLs the app would target the same way (same origin).
func sameServer(a, b string) bool {
	oa, okA := serverOrigin(a)
	ob, okB := serverOrigin(b)
	if okA && okB {
		return strings.EqualFold(oa, ob)
	}
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

// profileByURL returns the profile of that server, if any.
func (c *Config) profileByURL(serverURL string) *ServerProfile {
	for i := range c.Profiles {
		if sameServer(c.Profiles[i].ServerURL, serverURL) {
			return &c.Profiles[i]
		}
	}
	return nil
}

// activate makes profile id the active one (mirrors its URL and user name).
func (c *Config) activate(id string) bool {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			c.ActiveID = id
			c.ServerURL = c.Profiles[i].ServerURL
			c.Username = c.Profiles[i].Username
			return true
		}
	}
	return false
}

// normalizeProfiles: a pre-profiles config (one serverUrl) becomes one
// profile; the active profile's values are mirrored at the top level.
// Reports whether the config changed (to be saved).
func (c *Config) normalizeProfiles() bool {
	changed := false
	if len(c.Profiles) == 0 && c.ServerURL != "" {
		p := c.addProfile(c.ServerURL)
		p.Username = c.Username
		c.ActiveID = p.ID
		changed = true
	}
	if c.active() == nil && len(c.Profiles) > 0 {
		c.ActiveID = c.Profiles[0].ID
		changed = true
	}
	if a := c.active(); a != nil && (c.ServerURL != a.ServerURL || c.Username != a.Username) {
		c.activate(a.ID)
	}
	if id := c.remoteActiveFor(); id != "" {
		c.activate(id)
		changed = true
	}
	return changed
}

// remoteActiveFor: in multi-server mode the active profile is the account
// of the top bar, Logs and Settings, so it must be a real Obliance server: a
// local base there (made active, then "All servers" picked) would have the
// page ask to sign in to a base that has no account. Returns the id of the
// profile to make active instead, "" when the active one is fine.
func (c *Config) remoteActiveFor() string {
	if c.serverMode() != "multi" {
		return ""
	}
	if a := c.active(); a == nil || !a.IsLocal() {
		return ""
	}
	if r := c.firstRemote(); r != nil {
		return r.ID
	}
	return ""
}

// keepRemoteActiveLocked: remoteActiveFor applied to the proxy (its session
// store follows). Caller holds p.mu.
func (p *Proxy) keepRemoteActiveLocked() {
	if id := p.cfg.remoteActiveFor(); id != "" {
		p.switchLocked(id)
	}
}

// syncActive copies login-page edits (serverUrl / username) into the active
// profile, creating the first profile on a fresh install.
func (c *Config) syncActive() {
	a := c.active()
	if a == nil {
		if c.ServerURL == "" {
			return
		}
		a = c.addProfile(c.ServerURL)
		c.ActiveID = a.ID
	}
	if a.ServerURL != c.ServerURL {
		a.ServerURL = c.ServerURL
		a.Name = profileName(c.ServerURL)
	}
	a.Username = c.Username
}

// removeProfile deletes a non-active profile and its session / favourites files.
func (c *Config) removeProfile(id, dir string) bool {
	if id == c.ActiveID {
		return false
	}
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			p := c.Profiles[i]
			c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
			for _, f := range []string{p.SessionFile, p.Favorites} {
				if f != "" && filepath.Base(f) == f {
					_ = os.Remove(filepath.Join(dir, f))
				}
			}
			return true
		}
	}
	return false
}

// ── Proxy side ──────────────────────────────────────────────────────────────

func (p *Proxy) sess() *SessionStore {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.session
}

func (p *Proxy) profileFile(pick func(*ServerProfile) string, legacy string) string {
	if a := p.cfg.active(); a != nil {
		if f := pick(a); f != "" && filepath.Base(f) == f {
			return filepath.Join(p.dir, f)
		}
	}
	return filepath.Join(p.dir, legacy)
}

func (p *Proxy) sessionPath() string {
	return p.profileFile(func(a *ServerProfile) string { return a.SessionFile }, "session.json")
}

func (p *Proxy) favoritesPath() string {
	return p.profileFile(func(a *ServerProfile) string { return a.Favorites }, "favorites.json")
}

type serverView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ServerURL  string `json:"serverUrl"`
	Color      string `json:"color"`
	HasSession bool   `json:"hasSession"`
	// Legacy: the profile that owns the pre-profiles files (session.json /
	// favorites.json), hence the pre-profiles page keys too. Not "the first
	// in the list": that one changes when the first server is removed.
	Legacy bool `json:"legacy"`
	// The last known Obliance theme of its account ("": not known; theme.go).
	Theme string `json:"theme"`
	// Local: a pure local RDP/SSH connection tree, not a real Obliance
	// server — no sign-in, no HasSession, no theme/language sync.
	Local bool `json:"local"`
}

// modeOKLocked: a server mode that can be set now ("single" / "multi"; ""
// = single). Multi needs two servers or more. Caller holds p.mu.
func (p *Proxy) modeOKLocked(mode string) bool {
	switch mode {
	case "multi":
		return p.cfg.multiPossible()
	case "single", "":
		return true
	}
	return false
}

// setModeLocked: sets the server mode (saved when it changed). Caller holds
// p.mu. Reports whether the value was valid (modeOKLocked).
func (p *Proxy) setModeLocked(mode string) bool {
	if !p.modeOKLocked(mode) {
		return false
	}
	if mode == "single" {
		mode = ""
	}
	if p.cfg.ServerMode != mode {
		p.cfg.ServerMode = mode
		saveConfig(p.cfg)
	}
	return true
}

// /local/servers            GET  → { active, mode, theme, profiles } (mode: "single" / "multi"; theme: the app's now, theme.go)
// /local/servers/switch     POST { id, mode? }  (mode: "single" / "multi", kept when absent)
// /local/servers/mode       POST { mode }       ("multi" needs two servers or more)
// /local/servers/add        POST { serverUrl } → created (or existing) and made active
// /local/servers/add-local  POST { name }      → a local RDP/SSH connection tree, made active in single mode (no serverUrl, no session)
// /local/servers/remove     POST { id }        (not the active one; one server left: single mode)
func (p *Proxy) handleServers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	action := strings.TrimPrefix(r.URL.Path, "/local/servers")
	if action != "" && r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID        string  `json:"id"`
		ServerURL string  `json:"serverUrl"`
		Mode      *string `json:"mode"`
		Name      string  `json:"name"`
	}
	if action != "" {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	}
	switch action {
	case "":
	case "/switch":
		p.mu.Lock()
		if body.Mode != nil && !p.modeOKLocked(*body.Mode) { // (checked first: nothing half done)
			p.mu.Unlock()
			http.Error(w, `{"error":"invalid mode"}`, http.StatusBadRequest)
			return
		}
		ok := p.switchLocked(body.ID)
		if ok && body.Mode != nil {
			p.setModeLocked(*body.Mode)
		}
		if ok {
			p.keepRemoteActiveLocked()
		}
		p.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"unknown server"}`, http.StatusNotFound)
			return
		}
	case "/mode":
		p.mu.Lock()
		ok := body.Mode != nil && p.setModeLocked(*body.Mode)
		if ok {
			p.keepRemoteActiveLocked()
		}
		p.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"invalid mode"}`, http.StatusBadRequest)
			return
		}
	case "/add":
		u, ok := validServerURL(body.ServerURL)
		if !ok {
			http.Error(w, `{"error":"invalid server url"}`, http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.switchLocked(p.cfg.addProfile(u).ID)
		p.mu.Unlock()
	case "/add-local":
		if strings.TrimSpace(body.Name) == "" {
			http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.switchLocked(p.cfg.addLocalProfile(body.Name).ID)
		// The new local base is what comes on screen (its Connections page):
		// single mode on it, as picking it in the server menu does. Left in
		// multi-server mode, the top bar said "All servers" while the page
		// showed the local tree, and the active profile (the top bar's
		// account, Logs, Settings) was a base that has no account.
		p.setModeLocked("single")
		p.mu.Unlock()
	case "/remove":
		p.mu.Lock()
		ok := p.cfg.removeProfile(body.ID, p.dir)
		if ok {
			delete(p.stores, body.ID)
			delete(p.loginOrig, body.ID)
			if !p.cfg.multiPossible() {
				p.cfg.ServerMode = "" // one server left (or local bases only): nothing to show "at once"
			}
			saveConfig(p.cfg)
		}
		p.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"cannot remove this server"}`, http.StatusBadRequest)
			return
		}
		p.forgetPin(body.ID) // its tenant alignment (tenantpin.go)
	default:
		http.NotFound(w, r)
		return
	}
	if action != "" {
		p.syncLang()      // another server: maybe another account's language (i18n.go)
		p.syncTheme()     // ... and theme (theme.go; the mode picks its source too)
		p.notifyServers() // every window reads the servers again (the session windows)
	}
	theme, themeVer := p.themeNow() // (with its change count: theme.go themeGen)
	p.mu.RLock()
	out := struct {
		Active       string       `json:"active"`
		Mode         string       `json:"mode"`
		Theme        string       `json:"theme"`
		ThemeVersion uint64       `json:"themeVersion"`
		Profiles     []serverView `json:"profiles"`
	}{Active: p.cfg.ActiveID, Mode: p.cfg.serverMode(), Theme: theme, ThemeVersion: themeVer, Profiles: []serverView{}}
	for _, sp := range p.cfg.Profiles {
		v := serverView{ID: sp.ID, Name: sp.Name, ServerURL: sp.ServerURL, Color: sp.Color, Legacy: sp.SessionFile == "session.json",
			Theme: normalizeTheme(sp.AccountTheme), Local: sp.IsLocal()}
		if !sp.IsLocal() {
			if s, ok := p.stores[sp.ID]; ok {
				v.HasSession = s.hasCookies()
			} else if f := sp.SessionFile; f != "" && filepath.Base(f) == f {
				v.HasSession = loadSession(filepath.Join(p.dir, f)).hasCookies()
			}
		}
		out.Profiles = append(out.Profiles, v)
	}
	p.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(out)
}

// setServersHook: who is told when the servers change (main.go: every window,
// bridges.broadcastServers).
func (p *Proxy) setServersHook(f func()) {
	p.serversMu.Lock()
	p.onServers = f
	p.serversMu.Unlock()
}

// notifyServers: the active server, the mode or the list may have changed
// (a switch, the mode, a server added or removed, the sign-in page moving to
// another server). Every window is told (window.__reachOnServers): a session
// window, which changes none of it, must know the mode and the active server
// now (servers.go /local/servers): its device actions and the tabs it may
// take follow them.
func (p *Proxy) notifyServers() {
	p.serversMu.Lock()
	f := p.onServers
	p.serversMu.Unlock()
	if f != nil {
		f()
	}
}

// storeForLocked returns THE session store of a profile: one instance per
// file, or two would overwrite each other's cookies. Caller holds p.mu.
func (p *Proxy) storeForLocked(sp *ServerProfile) *SessionStore {
	if p.stores == nil {
		p.stores = map[string]*SessionStore{}
	}
	if s, ok := p.stores[sp.ID]; ok {
		return s
	}
	f := sp.SessionFile
	if f == "" || filepath.Base(f) != f {
		f = "session-" + sp.ID + ".json"
	}
	s := loadSession(filepath.Join(p.dir, f))
	p.stores[sp.ID] = s
	return s
}

// adoptActiveStoreLocked points p.session at the active profile's store (the
// current store is kept when it already is that profile's file).
func (p *Proxy) adoptActiveStoreLocked() {
	a := p.cfg.active()
	if a == nil {
		return
	}
	if _, ok := p.stores[a.ID]; !ok && p.session != nil && p.session.path == p.sessionPath() {
		p.stores[a.ID] = p.session
	}
	p.session = p.storeForLocked(a)
}

// /sp/<profile id>/... : the reverse proxy for a given server profile, used by
// the chat (one socket per server, whichever server is on screen).
func (p *Proxy) handleProfileProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/sp/")
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		http.NotFound(w, r)
		return
	}
	id := rest[:i]
	serverURL, store := p.profileTarget(id) // tenantpin.go
	if store == nil {
		http.Error(w, `{"error":"unknown server"}`, http.StatusNotFound)
		return
	}
	if isTenantSwitch(r, rest[i:]) {
		p.forwardSwitch(id, func() { p.forward(w, r, "/sp/"+id, serverURL, store) })
		return
	}
	p.forward(w, r, "/sp/"+id, serverURL, store)
}

// switchProfile makes id the active server: its session, a fresh SSO state.
func (p *Proxy) switchProfile(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.switchLocked(id)
}

// switchLocked: switchProfile with p.mu held. A sign-in page edit of the
// profile being left that led to no sign-in (its store is still empty) is
// undone: the profile gets its previous URL back (loginOrig).
func (p *Proxy) switchLocked(id string) bool {
	if p.cfg.profileByID(id) == nil {
		return false
	}
	if prev := p.cfg.active(); prev != nil && prev.ID != id {
		if orig, ok := p.loginOrig[prev.ID]; ok {
			delete(p.loginOrig, prev.ID)
			if (p.session == nil || !p.session.hasCookies()) && p.cfg.profileByURL(orig) == nil {
				prev.ServerURL, prev.Name = orig, profileName(orig)
			}
		}
	}
	p.cfg.activate(id)
	p.session = p.storeForLocked(p.cfg.active())
	p.sso = nil
	saveConfig(p.cfg)
	return true
}

// loginEditLocked applies the sign-in page's fields to the active profile.
// Caller holds p.mu.
//   - The URL of ANOTHER profile: switch to it, instead of turning the active
//     profile into a second entry for that server (the original server would
//     vanish from the list, its favourites and chat keyed to the other one).
//   - Any other new URL rewrites the active profile in place (fresh install,
//     "Sign out & change"). The page posts partial URLs while the operator
//     types, so the profile's URL before the edit is kept in loginOrig until
//     a sign-in happens on the new one, and restored by switchLocked.
func (p *Proxy) loginEditLocked(serverURL, username string) {
	changed := false
	// A local base is never turned into a server: an address typed while it
	// is the active profile goes to that server's own profile (created when
	// new), the base and its connections stay as they are.
	if u := strings.TrimRight(serverURL, "/"); u != "" {
		if a := p.cfg.active(); a.IsLocal() {
			if o, ok := validServerURL(u); ok {
				p.switchLocked(p.cfg.addProfile(o).ID)
			} else {
				return // (a partial address while the operator types: nothing to keep yet)
			}
		}
	}
	if u := strings.TrimRight(serverURL, "/"); u != "" && u != p.cfg.ServerURL {
		if other := p.cfg.profileByURL(u); other != nil && other.ID != p.cfg.ActiveID {
			p.switchLocked(other.ID)
		} else {
			if a := p.cfg.active(); a != nil {
				if p.loginOrig == nil {
					p.loginOrig = map[string]string{}
				}
				// Every URL change empties the store: cookies now mean a
				// sign-in on the current URL, which is then the one to keep.
				if _, ok := p.loginOrig[a.ID]; !ok || p.session.hasCookies() {
					p.loginOrig[a.ID] = a.ServerURL
				}
			}
			p.cfg.ServerURL = u
			p.session.clear() // new server = new session
			changed = true
		}
	}
	if username != "" {
		p.cfg.Username = username
		changed = true
	}
	if changed {
		p.cfg.syncActive()         // the login page edits the active server profile
		p.adoptActiveStoreLocked() // first profile of a fresh install
		saveConfig(p.cfg)
	}
}
