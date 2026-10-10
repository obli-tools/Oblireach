package main

// support.go — "Connect by code": the operator side of Oblireach Support
// (a person runs Oblireach Support, reads an ID and a one-time code over the
// phone; the operator types them here).
//
// The peer-to-peer link (rendezvous server, PAKE on the code, WebRTC data
// channels) is supportlink's, and it lives in this Go process. The page never
// sees the operator key nor the code once sent. Once the person accepted,
// the session is handed to the page as a one-time tunnel token: the page
// opens /local/support/tunnel/<token>, a WebSocket of this proxy that
// carries exactly what an ObliReach relay tunnel carries (text frames = the
// viewer protocol's JSON, binary frames = its frames), so the remote viewer
// (reach_viewer.go) runs unchanged on it: a code session is one more Reach
// tab. Closing the tab closes the WebSocket, which ends the session for both
// sides; the host ending it closes the WebSocket (code 1000 "host_ended",
// 4000 "lost" for a broken link).
//
// Settings: the support servers (label, URL, operator key, the operator
// account signed in on it: support_account.go) are kept in one file sealed
// by DPAPI for the current Windows user, in the vault's private folder
// (support_store_windows.go): never in the plain preferences. Neither the key
// nor the account token ever goes back to the page (hasKey, account.loggedIn
// only). Connecting needs one of them on the chosen server: an account
// (signed in from the app) or a static operator key (private servers).
//
// HTTP (behind the proxy's gates: the per-run secret cookie of this app's
// WebViews, an Origin of the local UI when present; here also a loopback
// peer, and the local Origin REQUIRED on every POST and on the WebSocket):
//   GET  /local/support/servers           {servers: [{id,label,url,hasKey,default}], defaultName}
//   POST /local/support/servers/save      {id?, label, url, key?, default?}  -> {servers}
//   POST /local/support/servers/remove    {id}                               -> {servers}
//   POST /local/support/connect           {server, id, code, name}           -> {conn}
//   GET  /local/support/connect/<conn>    {state, sas, error, retry, token, path}
//   POST /local/support/cancel            {conn}
//   GET  /local/support/account/info?server=<id>  {accounts, register} (support_account.go)
//   POST /local/support/account/login     {server, email, password}  -> {servers}
//   POST /local/support/account/logout    {server}                   -> {servers}
//   POST /local/support/account/verify    {server}                   -> {servers}
//   POST /local/support/account/register  {server}  (the system browser on its sign-up page)
//   GET  /local/support/tunnel/<token>    WebSocket (single use, 30 s to claim)
//
// Nothing is recorded about a code session (no recents, no history): IDs are
// ephemeral and codes single use.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

const (
	// supportMaxServers bounds the settings list.
	supportMaxServers = 32
	// supportMaxLabel / supportMaxURL / supportMaxKey / supportMaxName: input bounds.
	supportMaxLabel = 60
	supportMaxURL   = 512
	supportMaxKey   = 512
	supportMaxName  = 64
	// supportConnectTimeout bounds a whole setup: the server's 60 s before
	// the code is confirmed, the person's 120 s to decide, then ICE.
	supportConnectTimeout = 4 * time.Minute
	// supportTunnelTTL: an established session whose page never opened its
	// tunnel is closed.
	supportTunnelTTL = 30 * time.Second
	// supportMaxPending: setups in progress at once.
	supportMaxPending = 4
	// supportKeepFinished: a finished setup's state stays readable this long.
	supportKeepFinished = 2 * time.Minute
	// supportMaxPageFrame: the largest frame the page may send (file chunks).
	supportMaxPageFrame = 16 << 20
	// WebSocket close codes the page reads (support_page.go).
	supportCloseHostEnded = 1000
	supportCloseLost      = 4000
)

// ── Settings: the support servers ───────────────────────────────────────────

type supportServer struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	URL     string `json:"url"`
	Key     string `json:"key,omitempty"`
	Default bool   `json:"default,omitempty"`
	// Pinned: the built-in server was chosen as the default (else it is
	// the default only while no other server exists).
	Pinned bool `json:"pinned,omitempty"`
	// The operator account signed in on this server (support_account.go):
	// its bearer token, when it expires (unix seconds, 0 = not said), who.
	Token     string `json:"token,omitempty"`
	TokenExp  int64  `json:"tokenExp,omitempty"`
	AcctID    string `json:"acctId,omitempty"`
	AcctEmail string `json:"acctEmail,omitempty"`
	AcctName  string `json:"acctName,omitempty"`
	// AcctUnverified: the server could not verify the account's e-mail
	// address (an instance without mail).
	AcctUnverified bool `json:"acctUnverified,omitempty"`
}

// accountToken: the account token while it is valid ("" = not signed in).
func (s supportServer) accountToken(now time.Time) string {
	if s.Token == "" || (s.TokenExp > 0 && now.Unix() >= s.TokenExp) {
		return ""
	}
	return s.Token
}

// clearAccount forgets the account signed in on this server.
func (s *supportServer) clearAccount() {
	s.Token, s.TokenExp, s.AcctID, s.AcctEmail, s.AcctName, s.AcctUnverified = "", 0, "", "", "", false
}

// The built-in public rendezvous instance: always in the list (its address
// and name fixed, not removable; its operator key can be set like any
// other's). "Receive help" uses it when nothing else is set up; "Connect by
// code" needs an operator key on it like on any server.
const (
	supportPublicID    = "00000000000000b1"
	supportPublicURL   = "https://rdv.reach.obli.tools"
	supportPublicLabel = "Oblireach (public)" // (the page shows its own words: support.publicServer)
)

// supportServerView is the ONLY shape a server is handed to the page in.
type supportServerView struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	URL     string `json:"url"`
	HasKey  bool   `json:"hasKey"`
	Default bool   `json:"default"`
	Builtin bool   `json:"builtin,omitempty"`
	// Account: who is signed in (never the token); nil = nobody.
	Account *supportAccountView `json:"account,omitempty"`
	// CanConnect: "Connect by code" works with this server (an account
	// signed in, or an operator key).
	CanConnect bool `json:"canConnect"`
}

type supportAccountView struct {
	LoggedIn  bool   `json:"loggedIn"`
	Email     string `json:"email,omitempty"`
	Name      string `json:"name,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"` // unix ms
	// Unverified: the e-mail address was not verified by the server.
	Unverified bool `json:"unverified,omitempty"`
}

func (s supportServer) view(now time.Time) supportServerView {
	v := supportServerView{ID: s.ID, Label: s.Label, URL: s.URL, HasKey: s.Key != "", Default: s.Default, Builtin: s.ID == supportPublicID}
	if s.accountToken(now) != "" {
		v.Account = &supportAccountView{LoggedIn: true, Email: s.AcctEmail, Name: s.AcctName, Unverified: s.AcctUnverified}
		if s.TokenExp > 0 {
			v.Account.ExpiresAt = s.TokenExp * 1000
		}
	}
	v.CanConnect = v.HasKey || v.Account != nil
	return v
}

// withSupportBuiltin: the saved list with the built-in public server in it
// (added when absent, its name and address forced), one default.
func withSupportBuiltin(saved []supportServer) []supportServer {
	list := append([]supportServer(nil), saved...)
	found := false
	for i := range list {
		if list[i].ID == supportPublicID {
			list[i].Label, list[i].URL = supportPublicLabel, supportPublicURL
			found = true
		}
	}
	if !found {
		list = append(list, supportServer{ID: supportPublicID, Label: supportPublicLabel, URL: supportPublicURL})
	}
	fixSupportDefault(list)
	return list
}

// supportBlobStore keeps the sealed settings (DPAPI on Windows; in memory in
// tests). Load answers nil, nil when nothing was saved yet.
type supportBlobStore interface {
	Load() ([]byte, error)
	Save(plain []byte) error
}

type supportFile struct {
	V       int             `json:"v"`
	Servers []supportServer `json:"servers"`
	// Incoming: "Allow incoming support" (receive_help.go; nil = on, the
	// default). ScamAck: the anti-scam notice was read once.
	Incoming *bool `json:"incoming,omitempty"`
	ScamAck  bool  `json:"scamAck,omitempty"`
}

// supportSettingsError: a refusal the page shows (its code: support_page.go).
type supportSettingsError struct{ Code string }

func (e *supportSettingsError) Error() string { return "support settings: " + e.Code }

func settingsErr(code string) error { return &supportSettingsError{Code: code} }

type supportSettings struct {
	mu    sync.Mutex
	store supportBlobStore
}

// loadLocked: the saved servers and the built-in one.
func (s *supportSettings) loadLocked() ([]supportServer, error) {
	list, err := s.loadSavedLocked()
	if err != nil {
		return nil, err
	}
	return withSupportBuiltin(list), nil
}

func (s *supportSettings) loadSavedLocked() ([]supportServer, error) {
	f, err := s.loadFileLocked()
	return f.Servers, err
}

func (s *supportSettings) loadFileLocked() (supportFile, error) {
	if s.store == nil {
		return supportFile{}, settingsErr("storage")
	}
	b, err := s.store.Load()
	if err != nil {
		return supportFile{}, settingsErr("storage")
	}
	if len(b) == 0 {
		return supportFile{V: 1}, nil
	}
	var f supportFile
	if err := json.Unmarshal(b, &f); err != nil {
		return supportFile{}, settingsErr("storage")
	}
	return f, nil
}

func (s *supportSettings) saveFileLocked(f supportFile) error {
	f.V = 1
	b, err := json.Marshal(f)
	if err != nil {
		return settingsErr("storage")
	}
	if err := s.store.Save(b); err != nil {
		return settingsErr("storage")
	}
	return nil
}

// saveLocked: the servers, the rest of the file kept.
func (s *supportSettings) saveLocked(list []supportServer) error {
	f, err := s.loadFileLocked()
	if err != nil {
		return err
	}
	f.Servers = list
	return s.saveFileLocked(f)
}

// prefs: "Allow incoming support" (on unless turned off) and whether the
// anti-scam notice was acknowledged. Unreadable settings: on, not read.
func (s *supportSettings) prefs() (incoming, scamAck bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadFileLocked()
	if err != nil {
		return true, false, err
	}
	return f.Incoming == nil || *f.Incoming, f.ScamAck, nil
}

// setPrefs changes what is given (nil: kept).
func (s *supportSettings) setPrefs(incoming, scamAck *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadFileLocked()
	if err != nil {
		return err
	}
	if incoming != nil {
		v := *incoming
		f.Incoming = &v
	}
	if scamAck != nil {
		f.ScamAck = *scamAck
	}
	return s.saveFileLocked(f)
}

// updateServer applies f to server id (the built-in one included) and saves.
func (s *supportSettings) updateServer(id string, f func(sv *supportServer)) ([]supportServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			f(&list[i])
			if err := s.saveLocked(list); err != nil {
				return nil, err
			}
			return list, nil
		}
	}
	return nil, settingsErr("not_found")
}

// defaultServer: the server "Receive help" registers with (the default one;
// the built-in public server when nothing else is set up).
func (s *supportSettings) defaultServer() (supportServer, error) {
	list, err := s.list()
	if err != nil {
		return supportServer{}, err
	}
	for _, sv := range list {
		if sv.Default {
			return sv, nil
		}
	}
	for _, sv := range list {
		if sv.ID == supportPublicID {
			return sv, nil
		}
	}
	return supportServer{ID: supportPublicID, Label: supportPublicLabel, URL: supportPublicURL}, nil
}

func (s *supportSettings) list() ([]supportServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *supportSettings) get(id string) (supportServer, bool, error) {
	list, err := s.list()
	if err != nil {
		return supportServer{}, false, err
	}
	for _, sv := range list {
		if sv.ID == id {
			return sv, true, nil
		}
	}
	return supportServer{}, false, nil
}

// supportServerInput: what the settings form sends. An empty key on an
// existing server keeps the one saved.
type supportServerInput struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	URL     string `json:"url"`
	Key     string `json:"key"`
	Default bool   `json:"default"`
}

var reSupportID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (s *supportSettings) save(in supportServerInput) ([]supportServer, error) {
	if in.ID == supportPublicID { // (the built-in one: its key and default only)
		in.URL, in.Label = supportPublicURL, ""
	}
	u, err := normalizeSupportURL(in.URL)
	if err != nil {
		return nil, err
	}
	label := cleanSupportText(in.Label, supportMaxLabel)
	if label == "" {
		if pu, err := url.Parse(u); err == nil {
			label = pu.Hostname()
		}
	}
	key := strings.TrimSpace(in.Key)
	if utf8.RuneCountInString(key) > supportMaxKey || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return nil, settingsErr("key_invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	idx := -1
	if in.ID != "" {
		if !reSupportID.MatchString(in.ID) {
			return nil, settingsErr("not_found")
		}
		for i := range list {
			if list[i].ID == in.ID {
				idx = i
			}
		}
		if idx < 0 {
			return nil, settingsErr("not_found")
		}
	}
	if idx < 0 {
		if len(list) >= supportMaxServers+1 { // (+ the built-in one)
			return nil, settingsErr("too_many")
		}
		list = append(list, supportServer{ID: randomHex(8)})
		idx = len(list) - 1
	}
	sv := &list[idx]
	if sv.URL != "" && sv.URL != u {
		sv.clearAccount() // (an account belongs to the server it was signed in on)
	}
	sv.Label, sv.URL = label, u
	if sv.ID == supportPublicID {
		sv.Label, sv.URL = supportPublicLabel, supportPublicURL
	}
	if key != "" {
		sv.Key = key
	}
	if in.Default {
		for i := range list {
			list[i].Default = i == idx
			list[i].Pinned = i == idx && list[i].ID == supportPublicID
		}
	}
	fixSupportDefault(list)
	if err := s.saveLocked(list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *supportSettings) remove(id string) ([]supportServer, error) {
	if id == supportPublicID {
		return nil, settingsErr("builtin")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := list[:0:0]
	found := false
	for _, sv := range list {
		if sv.ID == id {
			found = true
			continue
		}
		out = append(out, sv)
	}
	if !found {
		return nil, settingsErr("not_found")
	}
	fixSupportDefault(out)
	if err := s.saveLocked(out); err != nil {
		return nil, err
	}
	return out, nil
}

// fixSupportDefault: exactly one default when the list is not empty (none
// chosen: the first server added, else the built-in one).
func fixSupportDefault(list []supportServer) {
	for i := range list {
		if list[i].ID == supportPublicID {
			list[i].Default = list[i].Pinned // (chosen, or only for want of another)
		}
	}
	seen := false
	for i := range list {
		if list[i].Default && !seen {
			seen = true
			continue
		}
		list[i].Default = false
	}
	if !seen && len(list) > 0 {
		pick := 0
		for i := range list {
			if list[i].ID != supportPublicID {
				pick = i
				break
			}
		}
		list[pick].Default = true
	}
}

// normalizeSupportURL: "support.example.com" -> "https://support.example.com";
// https / wss anywhere, http / ws only to this computer (the operator key
// would travel in clear otherwise). No credentials, query or fragment.
func normalizeSupportURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > supportMaxURL || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", settingsErr("url_invalid")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", settingsErr("url_invalid")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "https", "wss":
	case "http", "ws":
		if !supportLoopbackHost(u.Hostname()) {
			return "", settingsErr("url_plain")
		}
	default:
		return "", settingsErr("url_invalid")
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), nil
}

func supportLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// cleanSupportText: trimmed, no control characters, at most max runes.
func cleanSupportText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// supportDefaultName: the Windows user name ("DOMAIN\\name" -> "name").
func supportDefaultName() string {
	n := os.Getenv("USERNAME")
	if n == "" {
		if u, err := user.Current(); err == nil {
			n = u.Username
		}
	}
	if i := strings.LastIndexAny(n, `\/`); i >= 0 {
		n = n[i+1:]
	}
	return cleanSupportText(n, supportMaxName)
}

// ── Setups and tunnels ──────────────────────────────────────────────────────

// supportSess is what the bridge needs of a *supportlink.Session (a fake in
// tests).
type supportSess interface {
	WriteFrame(op byte, p []byte) error
	ReadFrame() (byte, []byte, error)
	Close() error
	Path() supportlink.Path
	SAS() string
}

type supportDialFunc func(ctx context.Context, cfg supportlink.OperatorConfig, id, code string) (supportSess, error)

func supportlinkDial(ctx context.Context, cfg supportlink.OperatorConfig, id, code string) (supportSess, error) {
	s, err := supportlink.Connect(ctx, cfg, id, code)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Setup states.
const (
	supStateConnecting = "connecting" // signalling, PAKE
	supStateAwaiting   = "awaiting"   // code verified, the person decides (SAS shown)
	supStateReady      = "ready"      // accepted and linked: token to open
	supStateFailed     = "failed"
	supStateCancelled  = "cancelled"
)

type supportConn struct {
	id      string
	state   string
	sas     string
	errCode string
	retry   int
	token   string
	path    string
	cancel  context.CancelFunc
	ended   time.Time
}

// supportConnView: GET /local/support/connect/<conn>.
type supportConnView struct {
	State string `json:"state"`
	SAS   string `json:"sas,omitempty"`
	Error string `json:"error,omitempty"`
	Retry int    `json:"retry,omitempty"`
	Token string `json:"token,omitempty"`
	Path  string `json:"path,omitempty"`
}

type supportTunnel struct {
	sess supportSess
	stop func() bool
}

type supportHub struct {
	settings *supportSettings
	dial     supportDialFunc
	// account: the operator accounts' HTTP API (support_account.go).
	account  *supportAccountClient
	after    func(d time.Duration, f func()) func() bool
	now      func() time.Time
	userName func() string

	mu      sync.Mutex
	conns   map[string]*supportConn
	tunnels map[string]*supportTunnel
	live    int // tunnels claimed and running

	// help: "Receive help", this computer helped (receive_help.go); nil:
	// its routes answer 404.
	help *helpHost
}

func newSupportHub(store supportBlobStore, dial supportDialFunc) *supportHub {
	return &supportHub{
		settings: &supportSettings{store: store},
		dial:     dial,
		account:  newSupportAccountClient(),
		after:    func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
		now:      time.Now,
		userName: supportDefaultName,
		conns:    map[string]*supportConn{},
		tunnels:  map[string]*supportTunnel{},
	}
}

var (
	appSupportOnce sync.Once
	appSupport     *supportHub
)

func appSupportHub() *supportHub {
	appSupportOnce.Do(func() {
		appSupport = newSupportHub(newSupportStore(), supportlinkDial)
		appSupport.help = newAppHelpHost(appSupport.settings)
	})
	return appSupport
}

// supportStartError: a setup refused before it starts.
type supportStartError struct{ Code string }

func (e *supportStartError) Error() string { return "support: " + e.Code }

// start begins a setup to host id with code on server sv.
func (h *supportHub) start(sv supportServer, id, code, name string) (string, error) {
	id, code = wire.Digits(id), wire.Digits(code)
	if !wire.ValidID(id) {
		return "", &supportStartError{"invalid_id"}
	}
	if !wire.ValidCode(code) {
		return "", &supportStartError{"invalid_code"}
	}
	// An account signed in on this server, else its operator key.
	token := sv.accountToken(h.now())
	if token == "" && sv.Key == "" {
		return "", &supportStartError{"login_required"}
	}
	name = cleanSupportText(name, supportMaxName)
	h.mu.Lock()
	h.purgeLocked()
	n := 0
	for _, c := range h.conns {
		if c.state == supStateConnecting || c.state == supStateAwaiting {
			n++
		}
	}
	if n >= supportMaxPending {
		h.mu.Unlock()
		return "", &supportStartError{"too_many"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), supportConnectTimeout)
	c := &supportConn{id: randomToken(), state: supStateConnecting, cancel: cancel}
	h.conns[c.id] = c
	h.mu.Unlock()

	cfg := supportlink.OperatorConfig{
		Server:        sv.URL,
		ClientVersion: appVersion,
		Name:          name,
		SignalOptions: supportlink.SignalOptions{Header: http.Header{"User-Agent": {"OblireachDesktop/" + appVersion}}},
		OnAwaitingApproval: func(sas string) {
			h.mu.Lock()
			if c.state == supStateConnecting {
				c.state, c.sas = supStateAwaiting, sas
			}
			h.mu.Unlock()
		},
	}
	supportSetCredentials(&cfg, token, sv.Key)
	go func() {
		defer cancel()
		sess, err := h.dial(ctx, cfg, id, code)
		if token != "" && errors.Is(err, supportlink.ErrAuthExpired) {
			// The server no longer takes the account's token: logged out
			// here too, before the page reads the failure (it then asks to
			// log in again).
			_, _ = h.settings.updateServer(sv.ID, func(s *supportServer) {
				if s.Token == token {
					s.clearAccount()
				}
			})
		}
		h.finish(c, sess, err)
	}()
	return c.id, nil
}

func (h *supportHub) finish(c *supportConn, sess supportSess, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.ended = h.now()
	if c.state == supStateCancelled {
		if sess != nil {
			go sess.Close()
		}
		return
	}
	if err != nil || sess == nil {
		if err == nil {
			err = errors.New("no session")
		}
		c.state = supStateFailed
		c.errCode, c.retry = supportErrCode(err)
		return
	}
	tok := randomToken()
	t := &supportTunnel{sess: sess}
	t.stop = h.after(supportTunnelTTL, func() { h.expireTunnel(tok, t) })
	h.tunnels[tok] = t
	c.state, c.token, c.path, c.sas = supStateReady, tok, string(sess.Path()), sess.SAS()
}

func (h *supportHub) expireTunnel(tok string, t *supportTunnel) {
	h.mu.Lock()
	if h.tunnels[tok] != t {
		h.mu.Unlock()
		return
	}
	delete(h.tunnels, tok)
	h.mu.Unlock()
	_ = t.sess.Close()
}

// claim consumes a tunnel token (single use).
func (h *supportHub) claim(tok string) supportSess {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.tunnels[tok]
	if t == nil {
		return nil
	}
	delete(h.tunnels, tok)
	if t.stop != nil {
		t.stop()
	}
	return t.sess
}

func (h *supportHub) state(id string) (supportConnView, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.conns[id]
	if c == nil {
		return supportConnView{}, false
	}
	v := supportConnView{State: c.state, SAS: c.sas, Error: c.errCode, Retry: c.retry}
	if c.state == supStateReady {
		v.Token, v.Path = c.token, c.path
	}
	return v, true
}

// cancel stops a setup (the operator's Cancel): one in progress is
// abandoned (the server and the person are told), an established one whose
// tunnel was not opened yet is closed.
func (h *supportHub) cancel(id string) bool {
	h.mu.Lock()
	c := h.conns[id]
	if c == nil {
		h.mu.Unlock()
		return false
	}
	var sess supportSess
	switch c.state {
	case supStateConnecting, supStateAwaiting:
		c.state = supStateCancelled
		c.ended = h.now()
		c.cancel()
	case supStateReady:
		if t := h.tunnels[c.token]; t != nil {
			delete(h.tunnels, c.token)
			if t.stop != nil {
				t.stop()
			}
			sess = t.sess
		}
		c.state = supStateCancelled
	}
	h.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
	return true
}

func (h *supportHub) purgeLocked() {
	now := h.now()
	for id, c := range h.conns {
		if !c.ended.IsZero() && now.Sub(c.ended) > supportKeepFinished {
			delete(h.conns, id)
		}
	}
}

// counts (tests): setups known, tunnels waiting, tunnels running.
func (h *supportHub) counts() (conns, waiting, live int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns), len(h.tunnels), h.live
}

// supportErrCode: a setup's failure as the code the page translates
// (support_page.go SUP_ERR), and the delay the server asked for.
func supportErrCode(err error) (string, int) {
	var se *supportlink.ServerError
	var ue *supportlink.UpgradeError
	retry := 0
	if errors.As(err, &se) && se.Retry > 0 {
		retry = int((se.Retry + time.Second - 1) / time.Second)
	}
	switch {
	case errors.As(err, &ue):
		if ue.Side == supportlink.UpgradeServer {
			return "server_too_old", 0
		}
		return "app_too_old", 0
	case errors.Is(err, supportlink.ErrMITM):
		return "mitm", 0
	case errors.Is(err, supportlink.ErrWrongCode):
		return "wrong_code", 0
	case errors.Is(err, supportlink.ErrIDLocked):
		return "id_locked", retry
	case errors.Is(err, supportlink.ErrUnknownID):
		return "unknown_id", 0
	case errors.Is(err, supportlink.ErrRejected):
		return "rejected", 0
	case errors.Is(err, supportlink.ErrExpired):
		return "expired", 0
	case errors.Is(err, supportlink.ErrBusy):
		return "busy", retry
	case errors.Is(err, supportlink.ErrRateLimited):
		return "rate_limited", retry
	case errors.Is(err, supportlink.ErrAuthExpired):
		return "auth_expired", 0
	case errors.Is(err, supportlink.ErrQuota):
		switch se.Detail { // (which quota of the operator's account)
		case wire.QuotaConcurrent:
			return "quota_concurrent", retry
		case wire.QuotaDaily:
			return "quota_daily", retry
		case wire.QuotaRelayMonth:
			return "quota_relay", retry
		}
		return "quota", retry
	case errors.Is(err, supportlink.ErrOperatorRequired):
		return "key_refused", 0
	case errors.Is(err, supportlink.ErrBanned):
		return "banned", retry
	case errors.Is(err, supportlink.ErrUpgradeRequired): // (a server answer without the details)
		return "upgrade_required", 0
	case errors.Is(err, supportlink.ErrPeerGone):
		return "peer_gone", 0
	case errors.Is(err, supportlink.ErrInvalidID):
		return "invalid_id", 0
	case errors.Is(err, supportlink.ErrInvalidCode):
		return "invalid_code", 0
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", 0
	case errors.Is(err, context.Canceled):
		return "cancelled", 0
	case errors.Is(err, supportlink.ErrInvalidServer):
		return "url_invalid", 0
	case errors.Is(err, supportlink.ErrUnreachable):
		return "unreachable", 0
	}
	if se != nil {
		if se.Code == wire.ErrUnavailable {
			return "unavailable", retry
		}
		if se.Code == wire.ErrRelayQuota {
			return "relay_quota", retry
		}
		return "server_error", retry
	}
	return "network", 0
}

// ── HTTP ────────────────────────────────────────────────────────────────────

func (p *Proxy) handleSupport(w http.ResponseWriter, r *http.Request) {
	appSupportHub().serveHTTP(w, r, p.origin)
}

func supportPeerLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func supportJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func supportViews(list []supportServer) []supportServerView {
	out := make([]supportServerView, 0, len(list))
	now := time.Now()
	for _, s := range list {
		out = append(out, s.view(now))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Default && !out[j].Default })
	return out
}

func (h *supportHub) serveHTTP(w http.ResponseWriter, r *http.Request, origin string) {
	if !supportPeerLoopback(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/local/support/")
	if strings.HasPrefix(path, "tunnel/") {
		h.serveTunnel(w, r, origin, strings.TrimPrefix(path, "tunnel/"))
		return
	}
	if r.Method == http.MethodPost {
		if origin == "" || r.Header.Get("Origin") != origin {
			supportJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
	} else if r.Method != http.MethodGet {
		supportJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	if strings.HasPrefix(path, "account/") {
		h.serveAccount(w, r, strings.TrimPrefix(path, "account/"))
		return
	}
	if strings.HasPrefix(path, "help/") {
		if h.help == nil {
			supportJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		h.help.serveHTTP(w, r, strings.TrimPrefix(path, "help/"))
		return
	}
	switch {
	case path == "servers" && r.Method == http.MethodGet:
		list, err := h.settings.list()
		out := map[string]interface{}{"servers": supportViews(list), "defaultName": h.userName()}
		if err != nil {
			out["error"] = "storage"
		}
		supportJSON(w, http.StatusOK, out)
	case path == "servers/save" && r.Method == http.MethodPost:
		var in supportServerInput
		if !supportDecode(w, r, &in) {
			return
		}
		list, err := h.settings.save(in)
		h.settingsAnswer(w, list, err)
		h.settingsChanged(err)
	case path == "servers/remove" && r.Method == http.MethodPost:
		var in struct {
			ID string `json:"id"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		list, err := h.settings.remove(in.ID)
		h.settingsAnswer(w, list, err)
		h.settingsChanged(err)
	case path == "connect" && r.Method == http.MethodPost:
		var in struct {
			Server string `json:"server"`
			ID     string `json:"id"`
			Code   string `json:"code"`
			Name   string `json:"name"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		sv, ok, err := h.settings.get(in.Server)
		if err != nil {
			supportJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage"})
			return
		}
		if !ok {
			supportJSON(w, http.StatusBadRequest, map[string]string{"error": "no_server"})
			return
		}
		id, err := h.start(sv, in.ID, in.Code, in.Name)
		if err != nil {
			code := "failed"
			var se *supportStartError
			if errors.As(err, &se) {
				code = se.Code
			}
			supportJSON(w, http.StatusBadRequest, map[string]string{"error": code})
			return
		}
		supportJSON(w, http.StatusOK, map[string]string{"conn": id})
	case strings.HasPrefix(path, "connect/") && r.Method == http.MethodGet:
		v, ok := h.state(strings.TrimPrefix(path, "connect/"))
		if !ok {
			supportJSON(w, http.StatusNotFound, map[string]string{"error": "unknown"})
			return
		}
		supportJSON(w, http.StatusOK, v)
	case path == "cancel" && r.Method == http.MethodPost:
		var in struct {
			Conn string `json:"conn"`
		}
		if !supportDecode(w, r, &in) {
			return
		}
		supportJSON(w, http.StatusOK, map[string]bool{"ok": h.cancel(in.Conn)})
	default:
		supportJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// settingsChanged: the servers changed: "Receive help" follows a new
// default server.
func (h *supportHub) settingsChanged(err error) {
	if err == nil && h.help != nil {
		go h.help.settingsChanged()
	}
}

func supportDecode(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(v); err != nil {
		supportJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return false
	}
	return true
}

func (h *supportHub) settingsAnswer(w http.ResponseWriter, list []supportServer, err error) {
	if err != nil {
		code := "storage"
		var se *supportSettingsError
		if errors.As(err, &se) {
			code = se.Code
		}
		status := http.StatusBadRequest
		if code == "storage" {
			status = http.StatusInternalServerError
		}
		supportJSON(w, status, map[string]string{"error": code})
		return
	}
	supportJSON(w, http.StatusOK, map[string]interface{}{"servers": supportViews(list)})
}

// ── The tunnel: WebSocket <-> session ───────────────────────────────────────

func (h *supportHub) serveTunnel(w http.ResponseWriter, r *http.Request, origin, tok string) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if r.Method != http.MethodGet || origin == "" || r.Header.Get("Origin") != origin || !reParkToken.MatchString(tok) ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHasToken(r.Header.Get("Connection"), "upgrade") ||
		r.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	sess := h.claim(tok) // (consumed now, whatever happens next)
	if sess == nil {
		http.Error(w, "gone", http.StatusGone)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		_ = sess.Close()
		return
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err := buf.Flush(); err != nil {
		_ = sess.Close()
		_ = conn.Close()
		return
	}
	h.mu.Lock()
	h.live++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.live--
		h.mu.Unlock()
	}()
	runSupportBridge(conn, buf.Reader, sess)
}

// supportClosePayload: a close frame's payload (status code + reason).
func supportClosePayload(code uint16, reason string) []byte {
	b := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(b, code)
	return append(b, reason...)
}

// runSupportBridge relays one session to one page until either side ends:
// session frames (text / binary) go to the page as they are; the page's
// frames (fragmented or not) go to the session; ping is answered here; the
// page's close, or its connection gone, closes the session (the person is
// told); the session's end closes the page's WebSocket with why.
func runSupportBridge(c net.Conn, rd *bufio.Reader, sess supportSess) {
	var wmu sync.Mutex
	write := func(op byte, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeServerFrame(c, op, p)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			op, p, err := sess.ReadFrame()
			if err != nil {
				code, reason := uint16(supportCloseHostEnded), "host_ended"
				if !errors.Is(err, io.EOF) {
					code, reason = supportCloseLost, "lost"
				}
				_ = write(0x8, supportClosePayload(code, reason))
				_ = c.Close()
				return
			}
			if op != 0x1 && op != 0x2 {
				continue
			}
			if err := write(op, p); err != nil {
				_ = sess.Close()
				_ = c.Close()
				return
			}
		}
	}()
	var cont byte
	var msg []byte
	for {
		hd, err := readWSHeader(rd)
		if err != nil || !hd.masked || hd.length > supportMaxPageFrame || int64(len(msg))+hd.length > supportMaxPageFrame {
			break
		}
		payload := make([]byte, hd.length)
		if _, err := io.ReadFull(rd, payload); err != nil {
			break
		}
		payload = unmaskPayload(payload, hd.mask)
		op := hd.opcode
		switch op {
		case 0x8:
			_ = sess.Close()
			<-done
			return
		case 0x9:
			_ = write(0xA, payload)
			continue
		case 0xA:
			continue
		case 0x0:
			if cont == 0 {
				continue // (a continuation of nothing)
			}
			op = cont
		case 0x1, 0x2:
			cont = op
			msg = msg[:0]
		default:
			continue
		}
		msg = append(msg, payload...)
		if !hd.fin {
			continue
		}
		cont = 0
		frame := msg
		msg = nil
		if err := sess.WriteFrame(op, frame); err != nil {
			break
		}
	}
	_ = sess.Close()
	_ = c.Close()
	<-done
}
