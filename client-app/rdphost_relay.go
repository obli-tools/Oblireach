package main

// rdphost_relay.go — a local RDP connection streamed by the RDP helper
// (rdphost_proc.go) to the page's RDP tab (the Linux stream mode of
// TOOL_IMPL.rdp: rdp_stream_view.go), through a local WebSocket
// /local/rdp-tunnel/<token> that speaks the ObliReach viewer protocol (the
// one of the VM consoles: init, binary 0x02 H.264 frames, mouse / key input)
// with a few messages of its own.
//
// The session lives HERE, not in the page: its tab may move to another
// window (the WebSocket closes, the helper goes on; the new window's page
// opens a new tunnel and gets a fresh init and key frame), and it ends when
// its tab closes (reachRdpClose), its window closes, the helper ends, or
// nobody takes it over in time (a parked session, a tunnel never opened, a
// page gone without a word: rdpParkTTL).
//
// Helper -> page:
//
//	(tunnel open)        {type:'paired'}
//	'I' w,h              {type:'init', width, height, fps:30, codec:'h264', features}
//	'F' access unit      binary [0x02][Annex B]
//	'S' status           {type:'rdp_state', state, ...} (connecting, connected,
//	                     reconnecting, display_control, resize_unsupported, typing...)
//	'S' cert             {type:'rdp_cert', host, port, commonName, subject, issuer,
//	                     fingerprint, hostMismatch, gateway, changed, oldFingerprint}
//	                     (changed / oldFingerprint: from OUR trust store)
//	'S' credentials      {type:'rdp_credentials', reason, username, domain, canSave}
//	                     (never a password)
//	'S' ended            {type:'error', code:'rdp_<code>', message, retry}
//	                     (a normal end: user_disconnect / stdin_closed: nothing)
//	'P' pointer          {type:'cursor', kind:'hidden'|'default'|'shape', hx, hy, png}
//	'C' text             {type:'clipboard_content', text, origin:'remote_change'}
//	                     while the page asked for it (clipboard_sync push), else kept
//
// Page -> helper (a whitelist, bounded like the VM consoles' relay): mouse,
// key, type_text, type_cancel, release_all, refresh, sas (Ctrl+Alt+End),
// sync; clipboard_set {text, id} (answered clipboard_ack), clipboard_get
// {id} (answered from the last remote text), clipboard_sync {push};
// rdp_resize {width, height, dpr}; rdp_cert_reply {accept, remember,
// confirmChanged}; rdp_credentials {username, domain, password, save,
// cancel}. Anything else is dropped.
//
// Backpressure: one goroutine reads the helper and writes the page's
// WebSocket in the same loop (no unbounded queue): a slow page blocks the
// write, the helper's pipe fills, and the helper's adaptive bitrate goes
// down. While no page is attached, pictures are dropped; the next page asks
// for a refresh (a new init and a key frame).

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// A tunnel token must be used within this delay.
	rdpTunnelTTL = 30 * time.Second
	// Sessions at once (every window), and tunnel tokens waiting.
	rdpStreamMax        = 32
	rdpTunnelMaxPending = 64
	// The largest page message read (a clipboard text and its JSON).
	rdpPageMaxFrame = 4 << 20
	// The clipboard text accepted either way (the helper's own limit).
	rdpClipMax = 1 << 20
	// type_text: at most this many characters (the helper's limit).
	rdpTypeTextMax = 20000
	// A pointer shape larger than this is shown as the default arrow.
	rdpCursorMax = 384
)

var (
	errRdpWrongKind  = errors.New("rdp: entry is not an RDP connection")
	errRdpNotHere    = errors.New("rdp: session not in this window")
	errRdpStreamFull = errors.New("rdp: too many sessions")
	errRdpNotStream  = errors.New("rdp: not a streamed session")
)

// rdpConnectReq: what a session is opened with (the vault's entry resolved
// by the binding: rdp_bridge_linux.go). The password never reaches the page.
type rdpConnectReq struct {
	Label    string // the vault entry's id (logs only)
	Host     string
	Port     int
	Username string
	Domain   string
	Password string
	Width    int // the tab's size in device pixels
	Height   int
	Scale    int    // the page's devicePixelRatio x 100
	Layout   string // the keyboard layout's KLID ("0000040C")
	// SaveCreds: credentials typed in the prompt saved in the vault (nil:
	// not offered). Runs on its own goroutine.
	SaveCreds func(username, password string)
}

// rdpConnectLine is the helper's first line (rdphost.c "connect").
type rdpConnectLine struct {
	Type              string   `json:"type"`
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	Username          string   `json:"username,omitempty"`
	Domain            string   `json:"domain,omitempty"`
	Password          string   `json:"password,omitempty"`
	Security          string   `json:"security"`
	Width             int      `json:"width"`
	Height            int      `json:"height"`
	DesktopScale      int      `json:"desktopScale"`
	DeviceScale       int      `json:"deviceScale"`
	Fps               int      `json:"fps"`
	KeyboardLayout    string   `json:"keyboardLayout,omitempty"`
	KnownFingerprints []string `json:"knownFingerprints"`
	CertTimeoutMs     int      `json:"certTimeoutMs"`
	Clipboard         bool     `json:"clipboard"`
	Audio             string   `json:"audio"`
	ResizeReconnect   bool     `json:"resizeReconnect"`
	AutoReconnect     bool     `json:"autoReconnect"`
	ConfigDir         string   `json:"configDir,omitempty"`
}

// rdpSplitUser: "DOMAIN\user" -> ("user", "DOMAIN"); "user@realm" and
// "user" stay whole (FreeRDP resolves a UPN itself).
func rdpSplitUser(u string) (user, domain string) {
	if i := strings.IndexByte(u, '\\'); i > 0 && i < len(u)-1 {
		return u[i+1:], u[:i]
	}
	return u, ""
}

// rdpEvenSize: a desktop size the server accepts (even, 200..8192).
func rdpEvenSize(v int) int {
	if v < 200 {
		v = 200
	}
	if v > 8192 {
		v = 8192
	}
	return v &^ 1
}

// rdpScales: the desktop scale (100..500) and the matching device scale
// (100, 140 or 180) of a page scale.
func rdpScales(scale int) (desktop, device int) {
	if scale < 100 {
		scale = 100
	}
	if scale > 500 {
		scale = 500
	}
	switch {
	case scale >= 180:
		device = 180
	case scale >= 140:
		device = 140
	default:
		device = 100
	}
	return scale, device
}

// rdpLayoutOf: the keyboard layout (KLID) of the app's language, the
// default of a connection.
func rdpLayoutOf(lang string) string {
	switch lang {
	case "fr":
		return "0000040C"
	case "de":
		return "00000407"
	case "es":
		return "0000040A"
	case "it":
		return "00000410"
	case "pt-BR":
		return "00000416"
	case "nl":
		return "00000413"
	case "sv":
		return "0000041D"
	case "da":
		return "00000406"
	case "pl":
		return "00000415"
	case "cs":
		return "00000405"
	case "ru":
		return "00000419"
	case "uk":
		return "00000422"
	case "tr":
		return "0000041F"
	case "ja":
		return "00000411"
	case "ko":
		return "00000412"
	case "zh-CN":
		return "00000804"
	case "ar":
		return "00000401"
	}
	return "00000409"
}

// rdpConfigDir: FreeRDP's own folder (a cache: it never writes its trust
// store, the acceptance is per session; our store is rdp_known_hosts).
func rdpConfigDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return ""
	}
	d := filepath.Join(dir, "oblireach", "freerdp")
	if os.MkdirAll(d, 0o700) != nil {
		return ""
	}
	return d
}

// ── Sessions ────────────────────────────────────────────────────────────────

// rdpCertPending: the certificate the helper waits for an answer about.
type rdpCertPending struct {
	key     string // its server (host:port) in the trust store
	fp      string // normalized
	changed bool   // another fingerprint is trusted for that server
	msg     []byte // what the page was told (told again to a new page)
}

// rdpStream is one local RDP session.
type rdpStream struct {
	id     string
	label  string
	helper *rdpHelper
	known  *rdpKnownHosts
	reg    *rdpStreamRegistry
	parks  *rdpParkReg

	// saveCreds: credentials typed in the prompt, to be saved in the vault
	// (nil: not offered). Runs on its own goroutine.
	saveCreds func(username, password string)
	resizeRC  bool // the connect line's resizeReconnect

	mu        sync.Mutex
	owner     *nativeBridge
	viewer    *rdpViewer
	parked    bool
	graceStop func() bool
	width     uint32
	height    uint32
	cursor    []byte // the last pointer message
	state     []byte // the last rdp_state message
	creds     []byte // a credentials question waiting
	cert      *rdpCertPending
	remote    string // the remote clipboard's last text
	clipPush  bool
	clipOn    bool
	dispReady bool
	ended     bool
	endMsg    []byte // the error told to the page (nil: a normal end)
	done      chan struct{}
}

// rdpViewer is one attached page (a WebSocket).
type rdpViewer struct {
	c      net.Conn
	wmu    sync.Mutex
	closed bool
}

func (v *rdpViewer) write(op byte, p []byte) error {
	v.wmu.Lock()
	defer v.wmu.Unlock()
	if v.closed {
		return net.ErrClosed
	}
	return writeServerFrame(v.c, op, p)
}

func (v *rdpViewer) writeJSON(m interface{}) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return v.write(0x1, b)
}

// close: a close frame, then the connection (once).
func (v *rdpViewer) close() {
	v.wmu.Lock()
	defer v.wmu.Unlock()
	if v.closed {
		return
	}
	v.closed = true
	_ = writeServerFrame(v.c, 0x8, []byte{0x03, 0xE8})
	_ = v.c.Close()
}

// rdpStreamRegistry: the live sessions and their tunnel tokens.
type rdpStreamRegistry struct {
	mu      sync.Mutex
	byID    map[string]*rdpStream
	tunnels map[string]*rdpTunnelTok
	ttl     time.Duration
	after   func(d time.Duration, f func()) func() bool
	newTok  func() string
	parks   *rdpParkReg // a session moving to another window (rdp_park.go)
}

type rdpTunnelTok struct {
	s    *rdpStream
	stop func() bool
}

func newRdpStreamRegistry() *rdpStreamRegistry {
	return &rdpStreamRegistry{byID: map[string]*rdpStream{}, tunnels: map[string]*rdpTunnelTok{}, ttl: rdpTunnelTTL,
		after:  func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
		newTok: randomToken, parks: appRdpParks}
}

var appRdpStreams = newRdpStreamRegistry()

func (r *rdpStreamRegistry) get(id string) *rdpStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

func (r *rdpStreamRegistry) remove(s *rdpStream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID[s.id] == s {
		delete(r.byID, s.id)
	}
	for tok, t := range r.tunnels {
		if t.s == s {
			delete(r.tunnels, tok)
			t.stop()
		}
	}
}

// ownedBy: the sessions a window shows.
func (r *rdpStreamRegistry) ownedBy(b *nativeBridge) []*rdpStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*rdpStream
	for _, s := range r.byID {
		s.mu.Lock()
		if s.owner == b && b != nil {
			out = append(out, s)
		}
		s.mu.Unlock()
	}
	return out
}

func (r *rdpStreamRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// issueTunnel: a one-time token the page opens /local/rdp-tunnel/<token>
// with. Not used in time: a session without a page ends.
func (r *rdpStreamRegistry) issueTunnel(s *rdpStream) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID[s.id] != s {
		return "", errRdpNotHere
	}
	if len(r.tunnels) >= rdpTunnelMaxPending {
		return "", errRdpStreamFull
	}
	tok := r.newTok()
	t := &rdpTunnelTok{s: s}
	t.stop = r.after(r.ttl, func() {
		r.mu.Lock()
		if r.tunnels[tok] != t {
			r.mu.Unlock()
			return
		}
		delete(r.tunnels, tok)
		r.mu.Unlock()
		s.endIfOrphan()
	})
	r.tunnels[tok] = t
	return tok, nil
}

// takeTunnel consumes a token (single use).
func (r *rdpStreamRegistry) takeTunnel(tok string) *rdpStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tunnels[tok]
	if t == nil {
		return nil
	}
	delete(r.tunnels, tok)
	t.stop()
	return t.s
}

// startRdpStream launches the helper, sends the connect line and registers
// the session, shown by window owner.
func (r *rdpStreamRegistry) start(req rdpConnectReq, owner *nativeBridge, known *rdpKnownHosts) (*rdpStream, error) {
	key := rdpHostKey(req.Host, req.Port)
	if key == "" {
		return nil, errBadChars
	}
	if r.count() >= rdpStreamMax {
		return nil, errRdpStreamFull
	}
	path, err := rdpHelperPath()
	if err != nil {
		return nil, err
	}
	if rdpHelperOverride == "" {
		if err := rdpHelperCheck(path); err != nil {
			return nil, err
		}
	}
	h, err := startRdpHelper(path)
	if err != nil {
		return nil, err
	}
	desktop, device := rdpScales(req.Scale)
	line := rdpConnectLine{
		Type: "connect", Host: req.Host, Port: req.Port, Username: req.Username, Domain: req.Domain, Password: req.Password,
		Security: "auto", Width: rdpEvenSize(req.Width), Height: rdpEvenSize(req.Height), DesktopScale: desktop, DeviceScale: device,
		Fps: 30, KeyboardLayout: req.Layout, KnownFingerprints: known.known(key), CertTimeoutMs: 120000,
		Clipboard: true, Audio: "local", AutoReconnect: true, ConfigDir: rdpConfigDir(),
	}
	if line.KnownFingerprints == nil {
		line.KnownFingerprints = []string{}
	}
	err = h.writeLine(line)
	line.Password = ""
	if err != nil {
		h.kill()
		return nil, err
	}
	s := &rdpStream{id: r.newTok(), label: req.Label, helper: h, known: known, reg: r, parks: r.parks,
		saveCreds: req.SaveCreds, resizeRC: line.ResizeReconnect, owner: owner, clipOn: true, done: make(chan struct{})}
	r.mu.Lock()
	r.byID[s.id] = s
	r.mu.Unlock()
	go s.pump()
	return s, nil
}

// ── The helper's side ───────────────────────────────────────────────────────

// pump reads the helper until it ends.
func (s *rdpStream) pump() {
	for {
		typ, p, err := s.helper.readFrame()
		if err != nil {
			break
		}
		switch typ {
		case 'I':
			if len(p) >= 8 {
				w, h := binary.LittleEndian.Uint32(p), binary.LittleEndian.Uint32(p[4:])
				if w > 0 && h > 0 && w <= 16384 && h <= 16384 {
					s.mu.Lock()
					s.width, s.height = w, h
					msg := s.initMsgLocked()
					v := s.viewer
					s.mu.Unlock()
					s.send(v, 0x1, msg)
				}
			}
		case 'F':
			s.mu.Lock()
			v := s.viewer
			s.mu.Unlock()
			if v != nil {
				out := make([]byte, 1+len(p))
				out[0] = 0x02
				copy(out[1:], p)
				s.send(v, 0x2, out)
			}
		case 'S':
			s.onStatus(p)
		case 'P':
			if msg := rdpCursorMsg(p); msg != nil {
				s.mu.Lock()
				s.cursor = msg
				v := s.viewer
				s.mu.Unlock()
				s.send(v, 0x1, msg)
			}
		case 'C':
			text := string(p)
			if !utf8.ValidString(text) || len(text) > rdpClipMax {
				continue
			}
			s.mu.Lock()
			s.remote = text
			push, v := s.clipPush, s.viewer
			s.mu.Unlock()
			if push {
				s.sendJSON(v, map[string]interface{}{"type": "clipboard_content", "text": text, "origin": "remote_change"})
			}
		}
	}
	<-s.helper.done
	s.finish()
}

// send writes one frame to v (nil: nobody attached). A failed write closes
// v (the page went: its reader notices).
func (s *rdpStream) send(v *rdpViewer, op byte, p []byte) {
	if v == nil || p == nil {
		return
	}
	if err := v.write(op, p); err != nil {
		v.close()
	}
}

func (s *rdpStream) sendJSON(v *rdpViewer, m interface{}) {
	if v == nil {
		return
	}
	b, err := json.Marshal(m)
	if err == nil {
		s.send(v, 0x1, b)
	}
}

// initMsgLocked: the init of the current picture.
func (s *rdpStream) initMsgLocked() []byte {
	feats := []string{"rdp_local", "rdp_cursor", "rdp_type_text", "rdp_sas"}
	if s.clipOn {
		feats = append(feats, "clipboard_text", "clipboard_push")
	}
	if s.dispReady || s.resizeRC {
		feats = append(feats, "rdp_resize")
	}
	b, _ := json.Marshal(map[string]interface{}{"type": "init", "width": s.width, "height": s.height, "fps": 30, "codec": "h264", "features": feats})
	return b
}

// rdpHelperStatus: an 'S' payload.
type rdpHelperStatus struct {
	State          string `json:"state"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	CommonName     string `json:"commonName"`
	Subject        string `json:"subject"`
	Issuer         string `json:"issuer"`
	Fingerprint    string `json:"fingerprint"`
	HostMismatch   bool   `json:"hostMismatch"`
	Gateway        bool   `json:"gateway"`
	Reason         string `json:"reason"`
	Username       string `json:"username"`
	Domain         string `json:"domain"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	DisplayControl bool   `json:"displayControl"`
	Clipboard      *bool  `json:"clipboard"`
	Attempt        int    `json:"attempt"`
	Done           int    `json:"done"`
	Total          int    `json:"total"`
	Skipped        int    `json:"skipped"`
	Code           string `json:"code"`
	Message        string `json:"message"`
}

// rdpCut: a text of the helper as the page shows it (no control
// character, at most n bytes).
func rdpCut(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if len(s) > n {
		s = cutUTF8(s, n)
	}
	return s
}

// cutUTF8 returns s cut to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// rdpRetryCodes: the ends a Reconnect may fix by itself (the page offers
// Reconnect for all of them anyway).
var rdpRetryCodes = map[string]bool{"reconnect_failed": true, "unreachable": true, "server_disconnected": true}

func (s *rdpStream) onStatus(p []byte) {
	var st rdpHelperStatus
	if json.Unmarshal(p, &st) != nil {
		return
	}
	var msg []byte
	switch st.State {
	case "waiting_config":
		return
	case "cert":
		key := rdpHostKey(st.Host, st.Port)
		fp := normRdpFingerprint(st.Fingerprint)
		old := s.known.known(key)
		changed := len(old) > 0
		m := map[string]interface{}{"type": "rdp_cert", "host": rdpCut(st.Host, 255), "port": st.Port,
			"commonName": rdpCut(st.CommonName, 255), "subject": rdpCut(st.Subject, 512), "issuer": rdpCut(st.Issuer, 512),
			"fingerprint": rdpCut(st.Fingerprint, 200), "hostMismatch": st.HostMismatch, "gateway": st.Gateway,
			"changed": changed, "canRemember": s.known != nil && key != "" && fp != ""}
		if changed {
			m["oldFingerprint"] = old[0]
		}
		msg, _ = json.Marshal(m)
		s.mu.Lock()
		s.cert = &rdpCertPending{key: key, fp: fp, changed: changed, msg: msg}
		v := s.viewer
		s.mu.Unlock()
		s.send(v, 0x1, msg)
		return
	case "credentials":
		msg, _ = json.Marshal(map[string]interface{}{"type": "rdp_credentials", "reason": rdpCut(st.Reason, 32),
			"username": rdpCut(st.Username, 256), "domain": rdpCut(st.Domain, 256), "canSave": s.saveCreds != nil})
		s.mu.Lock()
		s.creds = msg
		v := s.viewer
		s.mu.Unlock()
		s.send(v, 0x1, msg)
		return
	case "ended":
		if st.Code == "user_disconnect" || st.Code == "stdin_closed" {
			return
		}
		code := "rdp_" + rdpCut(st.Code, 40)
		msg, _ = json.Marshal(map[string]interface{}{"type": "error", "code": code, "message": rdpCut(st.Message, 300), "retry": rdpRetryCodes[st.Code]})
		s.mu.Lock()
		s.endMsg = msg
		v := s.viewer
		s.mu.Unlock()
		s.send(v, 0x1, msg)
		return
	}
	m := map[string]interface{}{"type": "rdp_state", "state": rdpCut(st.State, 40)}
	s.mu.Lock()
	switch st.State {
	case "connected":
		s.cert, s.creds = nil, nil
		if st.Clipboard != nil {
			s.clipOn = *st.Clipboard
		}
		m["width"], m["height"] = st.Width, st.Height
		m["displayControl"] = st.DisplayControl
	case "display_control":
		s.dispReady = true
	case "reconnecting":
		m["attempt"], m["reason"] = st.Attempt, rdpCut(st.Reason, 32)
	case "typing", "typing_cancelled":
		m["done"], m["total"], m["skipped"] = st.Done, st.Total, st.Skipped
	}
	m["resize"] = s.dispReady || s.resizeRC
	msg, _ = json.Marshal(m)
	if st.State != "typing" && st.State != "typing_cancelled" {
		s.state = msg
	}
	v := s.viewer
	s.mu.Unlock()
	s.send(v, 0x1, msg)
}

// rdpCursorMsg: a 'P' payload as the page's cursor message (nil: invalid).
func rdpCursorMsg(p []byte) []byte {
	if len(p) < 1 {
		return nil
	}
	switch p[0] {
	case 0:
		return []byte(`{"type":"cursor","kind":"hidden"}`)
	case 1:
		return []byte(`{"type":"cursor","kind":"default"}`)
	case 2:
	default:
		return nil
	}
	if len(p) < 9 {
		return nil
	}
	hx, hy := int(binary.LittleEndian.Uint16(p[1:])), int(binary.LittleEndian.Uint16(p[3:]))
	w, h := int(binary.LittleEndian.Uint16(p[5:])), int(binary.LittleEndian.Uint16(p[7:]))
	if w <= 0 || h <= 0 || w > rdpCursorMax || h > rdpCursorMax || len(p) < 9+w*h*4 {
		return []byte(`{"type":"cursor","kind":"default"}`)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	src := p[9:]
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = src[i*4+2]
		img.Pix[i*4+1] = src[i*4+1]
		img.Pix[i*4+2] = src[i*4+0]
		img.Pix[i*4+3] = src[i*4+3]
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return nil
	}
	if hx >= w {
		hx = w - 1
	}
	if hy >= h {
		hy = h - 1
	}
	b, _ := json.Marshal(map[string]interface{}{"type": "cursor", "kind": "shape", "hx": hx, "hy": hy, "png": base64.StdEncoding.EncodeToString(buf.Bytes())})
	return b
}

// finish: the helper ended: the page is told (an error said, or nothing
// for a normal end), the session forgotten.
func (s *rdpStream) finish() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	v := s.viewer
	s.viewer = nil
	if s.graceStop != nil {
		s.graceStop()
		s.graceStop = nil
	}
	endMsg := s.endMsg
	if endMsg == nil && s.width == 0 && !s.helperEndedNormally() {
		// (never connected, no word from the helper: its stderr stays here)
		endMsg, _ = json.Marshal(map[string]interface{}{"type": "error", "code": "rdp_helper_failed", "message": "", "retry": false})
		s.endMsg = endMsg
	}
	s.mu.Unlock()
	if v != nil {
		if endMsg != nil {
			s.send(v, 0x1, endMsg)
		}
		v.close()
	}
	s.reg.remove(s)
	if s.parks != nil {
		s.parks.dropConn(s.id)
	}
	close(s.done)
}

// helperEndedNormally: the helper exited with 0 (a disconnect, stdin closed).
func (s *rdpStream) helperEndedNormally() bool {
	return s.helper.exited() && s.helper.exit == nil
}

// end: the session ends (its tab or window closed, a park or tunnel never
// taken over): the helper disconnects, then finish runs (pump).
func (s *rdpStream) end() {
	go s.helper.stop()
}

// endIfOrphan: a tunnel token expired: the session ends if no page shows it
// and it is not moving to another window.
func (s *rdpStream) endIfOrphan() {
	s.mu.Lock()
	orphan := s.viewer == nil && !s.parked && !s.ended
	s.mu.Unlock()
	if orphan {
		s.end()
	}
}

// ── The page's side ─────────────────────────────────────────────────────────

// attach: v becomes the page of the session (a previous one is closed); it
// gets paired, the picture's init, the pointer, a question waiting, and a
// refresh is asked (a key frame).
func (s *rdpStream) attach(v *rdpViewer) bool {
	s.mu.Lock()
	if s.ended {
		endMsg := s.endMsg
		s.mu.Unlock()
		_ = v.writeJSON(map[string]string{"type": "paired"})
		if endMsg != nil {
			_ = v.write(0x1, endMsg)
		}
		v.close()
		return false
	}
	old := s.viewer
	s.viewer, s.parked = v, false
	if s.graceStop != nil {
		s.graceStop()
		s.graceStop = nil
	}
	var initMsg []byte
	if s.width > 0 {
		initMsg = s.initMsgLocked()
	}
	state, cursor, creds := s.state, s.cursor, s.creds
	var cert []byte
	if s.cert != nil {
		cert = s.cert.msg
	}
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	s.sendJSON(v, map[string]string{"type": "paired"})
	if state != nil {
		s.send(v, 0x1, state)
	}
	if initMsg != nil {
		s.send(v, 0x1, initMsg)
		_ = s.helper.writeLine(map[string]string{"type": "refresh"})
	}
	if cursor != nil {
		s.send(v, 0x1, cursor)
	}
	if cert != nil {
		s.send(v, 0x1, cert)
	}
	if creds != nil {
		s.send(v, 0x1, creds)
	}
	return true
}

// detach: page v went. Not moving to another window (park) nor ended: the
// session waits rdpParkTTL for a page (a reload), then ends.
func (s *rdpStream) detach(v *rdpViewer) {
	v.close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.viewer != v {
		return
	}
	s.viewer = nil
	if s.parked || s.ended {
		return
	}
	stop := s.reg.after(rdpParkTTL, func() { s.endIfOrphan() })
	s.graceStop = stop
}

// park: the session's tab moves to another window: its page lets go (the
// helper goes on); a one-time token takes it over (rdp_park.go).
func (s *rdpStream) park() (string, error) {
	tok, err := s.parks.park(s.id, func(string) { s.end() })
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.parked = true
	s.owner = nil
	v := s.viewer
	s.viewer = nil
	if s.graceStop != nil {
		s.graceStop()
		s.graceStop = nil
	}
	s.mu.Unlock()
	if v != nil {
		v.close()
	}
	return tok, nil
}

// adopt: a parked session taken over by window b.
func (s *rdpStream) adopt(b *nativeBridge) {
	s.mu.Lock()
	s.owner, s.parked = b, false
	s.mu.Unlock()
}

func (s *rdpStream) ownedBy(b *nativeBridge) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner == b
}

// rdpPageMsg: what the page may send (the fields of every message).
type rdpPageMsg struct {
	Type           string   `json:"type"`
	Action         string   `json:"action"`
	X              *float64 `json:"x"`
	Y              *float64 `json:"y"`
	Button         *float64 `json:"button"`
	Delta          *float64 `json:"delta"`
	Code           string   `json:"code"`
	Key            string   `json:"key"`
	Text           *string  `json:"text"`
	ID             *float64 `json:"id"`
	Push           bool     `json:"push"`
	Caps           bool     `json:"caps"`
	Num            bool     `json:"num"`
	Scroll         bool     `json:"scroll"`
	Width          float64  `json:"width"`
	Height         float64  `json:"height"`
	Dpr            float64  `json:"dpr"`
	Accept         bool     `json:"accept"`
	Remember       bool     `json:"remember"`
	ConfirmChanged bool     `json:"confirmChanged"`
	Username       string   `json:"username"`
	Domain         string   `json:"domain"`
	Password       string   `json:"password"`
	Save           bool     `json:"save"`
	Cancel         bool     `json:"cancel"`
}

var rdpMouseActions = map[string]bool{"move": true, "down": true, "up": true, "scroll": true, "hscroll": true}

func rdpClamp(v float64, lo, hi int) int {
	if math.IsNaN(v) || v < float64(lo) {
		return lo
	}
	if v > float64(hi) {
		return hi
	}
	return int(v)
}

// rdpFilterInput: a page message as the helper's line (ok false: not input).
func rdpFilterInput(m *rdpPageMsg) ([]byte, bool) {
	out := map[string]interface{}{"type": m.Type}
	switch m.Type {
	case "mouse":
		if !rdpMouseActions[m.Action] {
			return nil, false
		}
		out["action"] = m.Action
		x, y := 0, 0
		if m.X != nil {
			x = rdpClamp(*m.X, 0, 65535)
		}
		if m.Y != nil {
			y = rdpClamp(*m.Y, 0, 65535)
		}
		out["x"], out["y"] = x, y
		if m.Action == "down" || m.Action == "up" {
			b := 0
			if m.Button != nil {
				b = rdpClamp(*m.Button, 0, 4)
			}
			out["button"] = b
		}
		if m.Action == "scroll" || m.Action == "hscroll" {
			d := 0
			if m.Delta != nil {
				d = rdpClamp(*m.Delta, -100, 100)
			}
			if d == 0 {
				return nil, false
			}
			out["delta"] = d
		}
	case "key":
		if m.Action != "down" && m.Action != "up" {
			return nil, false
		}
		if len(m.Code) > 40 || utf8.RuneCountInString(m.Key) > 8 || (m.Code == "" && m.Key == "") {
			return nil, false
		}
		out["action"] = m.Action
		if m.Code != "" {
			out["code"] = m.Code
		}
		if m.Key != "" {
			out["key"] = m.Key
		}
	case "type_text":
		if m.Text == nil || *m.Text == "" {
			return nil, false
		}
		t := strings.ToValidUTF8(*m.Text, "")
		if r := []rune(t); len(r) > rdpTypeTextMax {
			t = string(r[:rdpTypeTextMax])
		}
		out["text"] = t
	case "sync":
		out["caps"], out["num"], out["scroll"] = m.Caps, m.Num, m.Scroll
	case "type_cancel", "sas", "release_all", "refresh":
	default:
		return nil, false
	}
	b, err := json.Marshal(out)
	return b, err == nil
}

// onPage handles one text message of page v.
func (s *rdpStream) onPage(v *rdpViewer, raw []byte) {
	var m rdpPageMsg
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return
	}
	defer func() { m.Password = "" }()
	if line, ok := rdpFilterInput(&m); ok {
		_ = s.helper.writeRaw(line)
		return
	}
	switch m.Type {
	case "clipboard_set":
		if m.Text == nil {
			return
		}
		ack := map[string]interface{}{"type": "clipboard_ack", "ok": true}
		if m.ID != nil {
			ack["id"] = *m.ID
		}
		if len(*m.Text) > rdpClipMax {
			ack["ok"], ack["error"] = false, "too_large"
		} else if !s.clipEnabled() {
			ack["ok"], ack["error"] = false, "unavailable"
		} else if err := s.helper.writeLine(map[string]string{"type": "clipboard_set", "text": strings.ToValidUTF8(*m.Text, "")}); err != nil {
			ack["ok"], ack["error"] = false, "failed"
		}
		s.sendJSON(v, ack)
	case "clipboard_get":
		s.mu.Lock()
		text := s.remote
		s.mu.Unlock()
		reply := map[string]interface{}{"type": "clipboard_content", "text": text, "origin": "request"}
		if m.ID != nil {
			reply["id"] = *m.ID
		}
		s.sendJSON(v, reply)
	case "clipboard_sync":
		s.mu.Lock()
		s.clipPush = m.Push
		s.mu.Unlock()
	case "rdp_resize":
		if m.Width <= 0 || m.Height <= 0 {
			return
		}
		scale := 100
		if m.Dpr > 0 && !math.IsNaN(m.Dpr) {
			scale = int(math.Round(m.Dpr * 100))
		}
		desktop, device := rdpScales(scale)
		_ = s.helper.writeLine(map[string]interface{}{"type": "resize", "width": rdpEvenSize(rdpClamp(m.Width, 0, 1<<20)),
			"height": rdpEvenSize(rdpClamp(m.Height, 0, 1<<20)), "desktopScale": desktop, "deviceScale": device})
	case "rdp_cert_reply":
		s.mu.Lock()
		c := s.cert
		s.cert = nil
		s.mu.Unlock()
		if c == nil {
			return
		}
		accept := m.Accept
		// (a changed certificate is only accepted once confirmed as such)
		if c.changed && !m.ConfirmChanged {
			accept = false
		}
		if accept && m.Remember && c.key != "" && c.fp != "" {
			_ = s.known.trust(c.key, c.fp, c.changed)
		}
		_ = s.helper.writeLine(map[string]interface{}{"type": "cert_reply", "accept": accept})
	case "rdp_credentials":
		s.mu.Lock()
		waiting := s.creds != nil
		s.creds = nil
		s.mu.Unlock()
		if !waiting {
			return
		}
		if m.Cancel {
			_ = s.helper.writeLine(map[string]interface{}{"type": "credentials", "cancel": true})
			return
		}
		user, dom := m.Username, m.Domain
		if dom == "" {
			user, dom = rdpSplitUser(user)
		}
		if len(user) > 256 || len(dom) > 256 || len(m.Password) > 512 {
			_ = s.helper.writeLine(map[string]interface{}{"type": "credentials", "cancel": true})
			return
		}
		_ = s.helper.writeLine(map[string]interface{}{"type": "credentials", "username": user, "domain": dom, "password": m.Password})
		if m.Save && s.saveCreds != nil {
			full := user
			if dom != "" {
				full = dom + `\` + user
			}
			pass := m.Password
			go s.saveCreds(full, pass)
		}
	}
}

func (s *rdpStream) clipEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clipOn
}

// serve reads page v until it goes.
func (s *rdpStream) serve(v *rdpViewer, rd *bufio.Reader) {
	defer s.detach(v)
	var cont byte
	var text []byte
	for {
		h, err := readWSHeader(rd)
		if err != nil || h.length > rdpPageMaxFrame {
			return
		}
		payload := make([]byte, h.length)
		if _, err := io.ReadFull(rd, payload); err != nil {
			return
		}
		if h.masked {
			payload = unmaskPayload(payload, h.mask)
		}
		op := h.opcode
		switch {
		case op == 0x8:
			return
		case op == 0x9:
			_ = v.write(0xA, payload)
			continue
		case op == 0xA:
			continue
		case op == 0x0:
			op = cont
		default:
			cont = op
			text = text[:0]
		}
		if op != 0x1 {
			continue
		}
		text = append(text, payload...)
		wipe(payload)
		if len(text) > rdpPageMaxFrame {
			return
		}
		if !h.fin {
			continue
		}
		s.onPage(v, text)
		wipe(text) // (a credentials answer held a password)
		text = text[:0]
	}
}

// /local/rdp-tunnel/<token>  (WebSocket): the picture and input of a local
// RDP session (behind the proxy's gates: ServeHTTP; the token is 256 random
// bits, single use, short-lived).
func (p *Proxy) handleRdpTunnel(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, "/local/rdp-tunnel/")
	key := r.Header.Get("Sec-WebSocket-Key")
	if r.Method != http.MethodGet || !p.localOriginOK(r) || !reParkToken.MatchString(tok) ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHasToken(r.Header.Get("Connection"), "upgrade") ||
		r.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	reg := p.rdpStreams
	if reg == nil {
		reg = appRdpStreams
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	s := reg.takeTunnel(tok)
	if s == nil {
		http.Error(w, "gone", http.StatusGone)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		s.endIfOrphan()
		return
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err := buf.Flush(); err != nil {
		conn.Close()
		s.endIfOrphan()
		return
	}
	v := &rdpViewer{c: conn}
	if !s.attach(v) {
		return
	}
	s.serve(v, buf.Reader)
}
