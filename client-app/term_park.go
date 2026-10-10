package main

// term_park.go — moving a terminal tab to another window without losing its
// shell.
//
// A terminal's WebSocket goes from its page to this proxy, then to the
// Obliance server's relay (/api/remote/tunnel/<token>): when that upstream
// socket closes, the server closes the tunnel and the agent kills the shell.
// A page cannot hand its WebSocket to another page, so the handover happens
// here, where the upstream socket lives:
//
//  1. The proxy knows which tunnels are terminals from the server's own answer
//     to POST /api/remote/sessions (protocol cmd / powershell / ssh,
//     captureSessionResponse), never from the page. Those tunnels are relayed
//     frame by frame (termTunnel) instead of byte for byte, which lets the
//     page side change without the upstream side noticing.
//  2. The old page asks to PARK its tunnel (POST /local/term-park {token}):
//     the proxy detaches that page's connection (closes it: the page's own
//     close frame never reaches the server) and keeps the upstream socket open.
//     It answers with a one-time park token (256 random bits).
//  3. Output keeps arriving: every data frame of the tunnel is kept in a
//     bounded history (termRingCap, the oldest whole frames dropped first), so
//     the new page gets the terminal's recent output and not only what came
//     while parked. The server's pings are answered while no page is there.
//     The last {type:'resize'} the page sent is kept too (the history was
//     drawn at that size).
//  4. The tab is recreated in the target window (detached_windows.go tearOff,
//     with the tab's kind and the park token), whose page opens
//     /local/term-attach/<park token> (a WebSocket to this proxy): the token
//     is consumed at once (single use), the page gets a {type:'resume'} text
//     frame (session id, size, its own theme...), then the history, then live
//     traffic; its input goes upstream.
//  5. Not attached within termParkTTL: the upstream socket is closed cleanly
//     (a close frame, 1000), exactly as when a tab is closed.
//
// Who can use it: /local/term-park and /local/term-attach/ are behind the
// same gates as the rest of the proxy (ServeHTTP: this app's per-run secret
// cookie, only this app's WebViews have it, and an Origin of the local UI —
// required here, not only checked when present). The park token is
// unguessable, single use and short lived; the tunnel's session token keeps
// its server-side meaning (the session is the same, never a new one).

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// A parked terminal not taken over within this time ends.
	termParkTTL = 15 * time.Second
	// The history of a terminal's output (bytes of frames, oldest dropped first).
	termRingCap = 2 << 20
	// Terminal sessions known from the server's answers, at most (oldest forgotten).
	termMaxExpected = 256
	// A page frame larger than this is streamed upstream instead of read whole.
	termMaxPageFrame = 16 << 20
	// A page text frame this small is looked at: is it a {type:'resize'}?
	termResizePeek = 256
)

// termProtocols: the POST /api/remote/sessions protocols that are terminals
// (terminal_viewer.go opens them).
var termProtocols = map[string]bool{"cmd": true, "powershell": true, "ssh": true}

var (
	errTermUnknown = errors.New("no such terminal")
	errTermState   = errors.New("terminal not attached")
	errTermNoPark  = errors.New("no such parked terminal")
)

// termParkMeta: what the old page hands over with the park, returned to the
// new page in the resume frame (all validated: sanitizeParkMeta).
type termParkMeta struct {
	SessionID string `json:"sessionId"`
	Theme     string `json:"theme"` // the tab's own terminal theme (term_themes.go), '' its type's
	Degraded  bool   `json:"degraded"`
}

var (
	reTermSessionID = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)
	reTermTheme     = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	reParkToken     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func sanitizeParkMeta(m termParkMeta) termParkMeta {
	if !reTermSessionID.MatchString(m.SessionID) {
		m.SessionID = ""
	}
	if !reTermTheme.MatchString(m.Theme) {
		m.Theme = ""
	}
	return m
}

type termExpect struct{ origin string }

// termParks: the terminal tunnels of the proxy, and the parked ones.
// Lock order: termParks.mu, then termTunnel.mu (never the other way).
type termParks struct {
	mu       sync.Mutex
	expected map[string]termExpect // session token → server origin (from the server's answer)
	expOrder []string
	live     map[string]*termTunnel // session token → its relay
	parks    map[string]*termTunnel // park token → parked relay

	ttl      time.Duration
	ringCap  int
	after    func(d time.Duration, f func()) (stop func() bool) // time.AfterFunc (test seam)
	newToken func() string
}

func newTermParks() *termParks {
	return &termParks{
		expected: map[string]termExpect{}, live: map[string]*termTunnel{}, parks: map[string]*termTunnel{},
		ttl: termParkTTL, ringCap: termRingCap,
		after:    func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
		newToken: randomToken,
	}
}

// expect: the server created a terminal session with this token, on origin.
func (r *termParks) expect(token, origin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.expected[token]; !ok {
		r.expOrder = append(r.expOrder, token)
	}
	r.expected[token] = termExpect{origin: origin}
	for len(r.expOrder) > termMaxExpected {
		delete(r.expected, r.expOrder[0])
		r.expOrder = r.expOrder[1:]
	}
}

// isTerminal: token is a terminal session of that server (its tunnel is
// relayed by run, so it can be parked).
func (r *termParks) isTerminal(token, origin string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.expected[token]
	return ok && e.origin == origin
}

// ── One terminal tunnel ──

type termTunnel struct {
	reg   *termParks
	token string

	upMu sync.Mutex // writes upstream: the page's frames, pongs, the final close
	up   io.Writer
	upC  io.Closer

	wmu sync.Mutex // writes to the page (a frame, or the whole replay on attach)

	mu        sync.Mutex
	page      io.WriteCloser // the page's connection; nil while parked
	parked    bool
	parkTok   string
	stopTimer func() bool
	upDone    bool // the upstream socket ended
	ended     bool // nothing more will happen (closed, expired)
	ring      termRing
	cols      int
	rows      int
	meta      termParkMeta
}

// run relays one terminal tunnel until its upstream socket ends (the caller
// closes the connections afterwards). page / pageRd: the page that opened it.
func (r *termParks) run(token string, up net.Conn, upRd *bufio.Reader, page net.Conn, pageRd *bufio.Reader) {
	t := r.newTunnel(token, up, up, page)
	go t.pumpPage(page, pageRd)
	t.pumpUp(upRd)
}

func (r *termParks) newTunnel(token string, up io.Writer, upC io.Closer, page io.WriteCloser) *termTunnel {
	t := &termTunnel{reg: r, token: token, up: up, upC: upC, page: page, ring: termRing{cap: r.ringCap}}
	r.mu.Lock()
	r.live[token] = t // (the same token opened twice: the newest relay is the one a park finds)
	r.mu.Unlock()
	return t
}

func (r *termParks) dropLive(t *termTunnel) {
	r.mu.Lock()
	if r.live[t.token] == t {
		delete(r.live, t.token)
	}
	r.mu.Unlock()
}

// park: the page of a terminal tunnel lets it go; the upstream socket stays
// open, termParkTTL at most. Returns the one-time park token.
func (r *termParks) park(token string, meta termParkMeta) (string, error) {
	r.mu.Lock()
	t := r.live[token]
	if t == nil {
		r.mu.Unlock()
		return "", errTermUnknown
	}
	t.mu.Lock()
	if t.parked || t.ended || t.upDone || t.page == nil {
		t.mu.Unlock()
		r.mu.Unlock()
		return "", errTermState
	}
	tok := r.newToken()
	pc := t.page
	t.page = nil
	t.parked = true
	t.parkTok = tok
	t.meta = sanitizeParkMeta(meta)
	t.stopTimer = r.after(r.ttl, func() { r.expire(tok, t) })
	t.mu.Unlock()
	r.parks[tok] = t
	r.mu.Unlock()
	_ = pc.Close() // (the old page: its connection ends; nothing it sends later goes upstream)
	return tok, nil
}

// claim consumes a park token (single use): its tunnel, still parked.
func (r *termParks) claim(tok string) (*termTunnel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.parks[tok]
	if t == nil {
		return nil, errTermNoPark
	}
	delete(r.parks, tok)
	t.mu.Lock()
	if t.stopTimer != nil {
		t.stopTimer()
		t.stopTimer = nil
	}
	t.mu.Unlock()
	return t, nil
}

// expire: a park token not claimed in time: its tunnel ends cleanly.
func (r *termParks) expire(tok string, t *termTunnel) {
	r.mu.Lock()
	if r.parks[tok] != t {
		r.mu.Unlock()
		return // claimed meanwhile
	}
	delete(r.parks, tok)
	if r.live[t.token] == t {
		delete(r.live, t.token)
	}
	t.mu.Lock()
	t.ended = true
	t.parked = false
	t.parkTok = ""
	t.ring = termRing{cap: t.ring.cap} // (its output is nobody's now)
	t.mu.Unlock()
	r.mu.Unlock()
	t.closeUp(true)
}

// parkedCount: tunnels waiting for a page (tests).
func (r *termParks) parkedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.parks)
}

// ── Upstream → page ──

func (t *termTunnel) pumpUp(rd *bufio.Reader) {
	defer t.upEnded()
	for {
		h, err := readWSHeader(rd)
		if err != nil {
			return
		}
		if h.length > int64(t.reg.ringCap) {
			// Too large to keep (never a terminal's): streamed to the page if
			// one is there, and the history restarts after it (a gap).
			if t.streamLarge(h, rd) != nil {
				return
			}
			continue
		}
		frame := make([]byte, len(h.raw)+int(h.length))
		copy(frame, h.raw)
		if _, err := io.ReadFull(rd, frame[len(h.raw):]); err != nil {
			return
		}
		switch {
		case h.opcode == 0x9: // ping: the page answers it, or this proxy while parked
			if !t.deliver(frame, false) {
				t.writeUp(0xA, frame[len(h.raw):])
			}
		case h.opcode >= 0x8: // close, pong: the page's, if any
			t.deliver(frame, false)
		default: // data: kept in the history, and to the page
			t.deliver(frame, true)
		}
	}
}

// deliver writes one whole frame to the page (if one is attached), after
// adding it to the history (record). False when no page took it.
func (t *termTunnel) deliver(frame []byte, record bool) bool {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	t.mu.Lock()
	if record {
		t.ring.push(frame)
	}
	pc := t.page
	t.mu.Unlock()
	if pc == nil {
		return false
	}
	if _, err := pc.Write(frame); err != nil {
		t.pageEnded(pc) // (parked meanwhile: nothing; the frame is in the history)
		return false
	}
	return true
}

func (t *termTunnel) streamLarge(h wsHeader, rd *bufio.Reader) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	t.mu.Lock()
	t.ring.reset()
	pc := t.page
	t.mu.Unlock()
	if pc == nil {
		_, err := io.CopyN(io.Discard, rd, h.length)
		return err
	}
	if _, err := pc.Write(h.raw); err != nil {
		t.pageEnded(pc)
		_, err = io.CopyN(io.Discard, rd, h.length)
		return err
	}
	_, err := io.CopyN(pc, rd, h.length)
	return err
}

// upEnded: the upstream socket ended. An attached page's connection closes;
// a parked tunnel waits for its page (it gets the history, then the end).
func (t *termTunnel) upEnded() {
	t.mu.Lock()
	t.upDone = true
	pc := t.page
	parked := t.parked
	if !parked {
		t.ended = true
		t.page = nil
	}
	t.mu.Unlock()
	t.reg.dropLive(t)
	if !parked && pc != nil {
		_ = pc.Close()
	}
}

// ── Page → upstream ──

// pumpPage forwards the frames of one page's connection upstream, whole,
// while that page is the tunnel's; it stops at the first frame after the
// tunnel was parked (the page's close frame among them: never forwarded).
func (t *termTunnel) pumpPage(pc io.WriteCloser, rd *bufio.Reader) {
	for {
		h, err := readWSHeader(rd)
		if err != nil {
			t.pageEnded(pc)
			return
		}
		if !t.isCurrent(pc) {
			return
		}
		if h.length > termMaxPageFrame {
			t.upMu.Lock()
			_, err = t.up.Write(h.raw)
			if err == nil {
				_, err = io.CopyN(t.up, rd, h.length)
			}
			t.upMu.Unlock()
			if err != nil {
				t.pageEnded(pc)
				return
			}
			continue
		}
		frame := make([]byte, len(h.raw)+int(h.length))
		copy(frame, h.raw)
		if _, err := io.ReadFull(rd, frame[len(h.raw):]); err != nil {
			t.pageEnded(pc)
			return
		}
		if h.opcode == 0x1 && h.fin && h.masked && h.length <= termResizePeek {
			t.noteResize(unmaskPayload(frame[len(h.raw):], h.mask))
		}
		// Checked again, with the upstream held: a frame read before the park
		// was sent before it (the operator's input), one after is dropped.
		t.upMu.Lock()
		if !t.isCurrent(pc) {
			t.upMu.Unlock()
			return
		}
		_, err = t.up.Write(frame)
		t.upMu.Unlock()
		if err != nil {
			t.pageEnded(pc)
			return
		}
	}
}

func (t *termTunnel) isCurrent(pc io.WriteCloser) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.page == pc && !t.ended
}

// pageEnded: a page's connection failed or ended. Its own (not parked): the
// tunnel ends, as it always did when a page went.
func (t *termTunnel) pageEnded(pc io.WriteCloser) {
	if !t.isCurrent(pc) {
		return
	}
	t.closeUp(false)
}

func (t *termTunnel) noteResize(p []byte) {
	var m struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if json.Unmarshal(p, &m) != nil || m.Type != "resize" || m.Cols < 1 || m.Rows < 1 || m.Cols > 65535 || m.Rows > 65535 {
		return
	}
	t.mu.Lock()
	t.cols, t.rows = m.Cols, m.Rows
	t.mu.Unlock()
}

// writeUp sends one masked frame upstream (a pong while parked).
func (t *termTunnel) writeUp(opcode byte, payload []byte) {
	f, err := buildMaskedFrame(opcode, payload)
	if err != nil {
		return
	}
	t.upMu.Lock()
	_, _ = t.up.Write(f)
	t.upMu.Unlock()
}

// closeUp ends the upstream socket (clean: with a close frame first).
func (t *termTunnel) closeUp(clean bool) {
	if clean {
		t.writeUp(0x8, []byte{0x03, 0xE8}) // 1000: normal closure
	}
	t.upMu.Lock()
	_ = t.upC.Close()
	t.upMu.Unlock()
}

// ── The new page ──

// termResume: the first frame a page taking over a parked terminal gets.
type termResume struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	Token     string `json:"token,omitempty"` // the tunnel's session token (to park it again: a second move)
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Theme     string `json:"theme,omitempty"`
	Degraded  bool   `json:"degraded,omitempty"`
	Truncated bool   `json:"truncated,omitempty"` // the history lost its oldest output
	Ended     bool   `json:"ended,omitempty"`     // the shell ended meanwhile: history, then the end
}

// attach hands a claimed tunnel to a new page: the resume frame, the history
// (in order), then live traffic. Returns false when the tunnel is over (the
// page got what there was, and its connection is closed).
func (t *termTunnel) attach(pc io.WriteCloser) bool {
	t.wmu.Lock()
	t.mu.Lock()
	frames := t.ring.snapshot()
	res := termResume{Type: "resume", SessionID: t.meta.SessionID, Token: t.token, Cols: t.cols, Rows: t.rows, Theme: t.meta.Theme,
		Degraded: t.meta.Degraded, Truncated: t.ring.dropped, Ended: t.upDone}
	t.mu.Unlock()
	j, _ := json.Marshal(res)
	err := writeServerFrame(pc, 0x1, j)
	for _, f := range frames {
		if err != nil {
			break
		}
		_, err = pc.Write(f)
	}
	t.mu.Lock()
	t.parked = false
	t.parkTok = ""
	done := t.upDone || t.ended
	if !done && err == nil {
		t.page = pc
	}
	t.mu.Unlock()
	t.wmu.Unlock()
	if err != nil && !done {
		t.mu.Lock()
		t.ended = true
		t.mu.Unlock()
		t.closeUp(true) // (the new page failed at once: the tunnel ends, like a page gone)
		_ = pc.Close()
		return false
	}
	if done {
		_ = writeServerFrame(pc, 0x8, []byte{0x03, 0xE8})
		_ = pc.Close()
		return false
	}
	return true
}

// ── History ──

// termRing: the recent data frames of a tunnel, whole, cap bytes at most;
// the oldest go first (and a message is never left without its start).
type termRing struct {
	frames  [][]byte
	size    int
	cap     int
	dropped bool
}

func (r *termRing) push(f []byte) {
	if len(f) > r.cap {
		r.reset()
		return
	}
	r.frames = append(r.frames, f)
	r.size += len(f)
	for r.size > r.cap && len(r.frames) > 0 {
		r.popHead()
		// (continuations of a message whose start went: go too)
		for len(r.frames) > 0 && frameOpcode(r.frames[0]) == 0x0 {
			r.popHead()
		}
	}
}

func (r *termRing) popHead() {
	r.size -= len(r.frames[0])
	r.frames[0] = nil
	r.frames = r.frames[1:]
	r.dropped = true
}

func (r *termRing) reset() {
	r.frames = nil
	r.size = 0
	r.dropped = true
}

func (r *termRing) snapshot() [][]byte {
	out := make([][]byte, 0, len(r.frames))
	for _, f := range r.frames {
		if len(out) == 0 && frameOpcode(f) == 0x0 {
			continue
		}
		out = append(out, f)
	}
	return out
}

func frameOpcode(f []byte) byte {
	if len(f) == 0 {
		return 0xFF
	}
	return f[0] & 0x0F
}

// ── WebSocket framing ──

type wsHeader struct {
	raw    []byte // the header as read (with the mask key)
	fin    bool
	opcode byte
	masked bool
	mask   [4]byte
	length int64
}

func readWSHeader(rd *bufio.Reader) (wsHeader, error) {
	var h wsHeader
	var b [2]byte
	if _, err := io.ReadFull(rd, b[:]); err != nil {
		return h, err
	}
	h.raw = append(h.raw, b[:]...)
	h.fin = b[0]&0x80 != 0
	h.opcode = b[0] & 0x0F
	h.masked = b[1]&0x80 != 0
	h.length = int64(b[1] & 0x7F)
	switch h.length {
	case 126:
		var e [2]byte
		if _, err := io.ReadFull(rd, e[:]); err != nil {
			return h, err
		}
		h.raw = append(h.raw, e[:]...)
		h.length = int64(binary.BigEndian.Uint16(e[:]))
	case 127:
		var e [8]byte
		if _, err := io.ReadFull(rd, e[:]); err != nil {
			return h, err
		}
		h.raw = append(h.raw, e[:]...)
		v := binary.BigEndian.Uint64(e[:])
		if v > 1<<62 {
			return h, errors.New("frame too large")
		}
		h.length = int64(v)
	}
	if h.masked {
		if _, err := io.ReadFull(rd, h.mask[:]); err != nil {
			return h, err
		}
		h.raw = append(h.raw, h.mask[:]...)
	}
	return h, nil
}

func unmaskPayload(p []byte, mask [4]byte) []byte {
	out := make([]byte, len(p))
	for i, c := range p {
		out[i] = c ^ mask[i%4]
	}
	return out
}

// buildServerFrame: one unmasked frame (proxy → page).
func buildServerFrame(opcode byte, payload []byte) []byte {
	n := len(payload)
	var h []byte
	switch {
	case n < 126:
		h = []byte{0x80 | opcode, byte(n)}
	case n <= 0xFFFF:
		h = []byte{0x80 | opcode, 126, byte(n >> 8), byte(n)}
	default:
		h = make([]byte, 10)
		h[0], h[1] = 0x80|opcode, 127
		binary.BigEndian.PutUint64(h[2:], uint64(n))
	}
	return append(h, payload...)
}

func writeServerFrame(w io.Writer, opcode byte, payload []byte) error {
	_, err := w.Write(buildServerFrame(opcode, payload))
	return err
}

// ── HTTP: park, attach ──

// localOriginOK: the request comes from the local UI (its Origin is
// required here, not only checked when present).
func (p *Proxy) localOriginOK(r *http.Request) bool {
	return r.Header.Get("Origin") == p.origin
}

// /local/term-park  POST {token, sessionId, theme, degraded} → {park}
func (p *Proxy) handleTermPark(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost || !p.localOriginOK(r) || p.terms == nil {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	var body struct {
		Token string `json:"token"`
		termParkMeta
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil || !validSessionToken(body.Token) {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	tok, err := p.terms.park(body.Token, body.termParkMeta)
	switch err {
	case nil:
	case errTermUnknown:
		http.Error(w, `{"error":"unknown terminal"}`, http.StatusNotFound)
		return
	default:
		http.Error(w, `{"error":"terminal not attached"}`, http.StatusConflict)
		return
	}
	j, _ := json.Marshal(map[string]string{"park": tok})
	_, _ = w.Write(j)
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// /local/term-attach/<park token>  (WebSocket): takes a parked terminal over.
func (p *Proxy) handleTermAttach(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, "/local/term-attach/")
	key := r.Header.Get("Sec-WebSocket-Key")
	if r.Method != http.MethodGet || !p.localOriginOK(r) || p.terms == nil || !reParkToken.MatchString(tok) ||
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
	t, err := p.terms.claim(tok) // (consumed now, whatever happens next)
	if err != nil {
		http.Error(w, "gone", http.StatusGone)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		t.mu.Lock()
		t.ended = true
		t.mu.Unlock()
		t.closeUp(true)
		return
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err := buf.Flush(); err != nil {
		_ = conn.Close()
		t.mu.Lock()
		t.ended = true
		t.mu.Unlock()
		t.closeUp(true)
		return
	}
	if !t.attach(conn) {
		return
	}
	t.pumpPage(conn, buf.Reader)
	_ = conn.Close()
}

func headerHasToken(v, token string) bool {
	for _, part := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// maxToolPath: the longest folder an explorer tab takes to another window.
const maxToolPath = 4096

// validToolPath: a folder an explorer tab takes to another window (the
// agent's path, as the page listed it): bounded, no control characters.
func validToolPath(p string) bool {
	if len(p) > maxToolPath || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
