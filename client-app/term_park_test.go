package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// tcpPair: two ends of a loopback TCP connection.
func tcpPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b = <-ch
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

type wsFrame struct {
	op      byte
	payload []byte
}

// readFrame reads one frame (unmasking it if masked), 2 s at most.
func readFrame(t *testing.T, c net.Conn, rd *bufio.Reader) (wsFrame, error) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	h, err := readWSHeader(rd)
	if err != nil {
		return wsFrame{}, err
	}
	p := make([]byte, h.length)
	if _, err := io.ReadFull(rd, p); err != nil {
		return wsFrame{}, err
	}
	if h.masked {
		p = unmaskPayload(p, h.mask)
	}
	return wsFrame{h.opcode, p}, nil
}

func mustFrame(t *testing.T, c net.Conn, rd *bufio.Reader, op byte, want string) {
	t.Helper()
	f, err := readFrame(t, c, rd)
	if err != nil {
		t.Fatalf("waiting for %q: %v", want, err)
	}
	if f.op != op || string(f.payload) != want {
		t.Fatalf("got op %d %q, want op %d %q", f.op, f.payload, op, want)
	}
}

func masked(t *testing.T, op byte, p string) []byte {
	f, err := buildMaskedFrame(op, []byte(p))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// fakeTimers: the park TTL under the test's control.
type fakeTimers struct {
	mu sync.Mutex
	fs []func()
}

func (ft *fakeTimers) after(_ time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	i := len(ft.fs)
	ft.fs = append(ft.fs, f)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		ok := ft.fs[i] != nil
		ft.fs[i] = nil
		return ok
	}
}

func (ft *fakeTimers) fire(i int) {
	ft.mu.Lock()
	f := ft.fs[i]
	ft.mu.Unlock()
	if f != nil {
		f()
	}
}

// wsDial opens a WebSocket to url's path on srv with the Origin given; the
// HTTP status of the answer, the connection and its reader on a 101.
func wsDial(t *testing.T, srv *httptest.Server, path, origin string) (int, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	req := "GET " + path + " HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	req += "\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(c)
	resp, err := http.ReadResponse(rd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusSwitchingProtocols && resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept key %q", resp.Header.Get("Sec-WebSocket-Accept"))
	}
	return resp.StatusCode, c, rd
}

func testTermProxy() (*Proxy, *fakeTimers) {
	ft := &fakeTimers{}
	p := &Proxy{origin: "http://127.0.0.1:5555", terms: newTermParks()}
	p.terms.after = ft.after
	return p, ft
}

// The whole move: live traffic, park (the old page cut, the shell kept),
// output while parked and a ping answered, the take-over (resume, history in
// order, live again both ways), the park token used once.
func TestTermParkAttachFlow(t *testing.T) {
	p, _ := testTermProxy()
	const tok = "abc123"
	p.terms.expect(tok, "https://srv")
	if !p.terms.isTerminal(tok, "https://srv") || p.terms.isTerminal(tok, "https://other") || p.terms.isTerminal("zzz", "https://srv") {
		t.Fatal("isTerminal")
	}
	srvEnd, upConn := tcpPair(t)    // the Obliance server / the proxy's upstream socket
	pageEnd, pageConn := tcpPair(t) // the old page / the proxy's side of it
	srvRd, pageRd := bufio.NewReader(srvEnd), bufio.NewReader(pageEnd)
	runDone := make(chan struct{})
	go func() {
		p.terms.run(tok, upConn, bufio.NewReader(upConn), pageConn, bufio.NewReader(pageConn))
		close(runDone)
	}()

	srvEnd.Write(buildServerFrame(0x1, []byte(`{"type":"paired"}`)))
	srvEnd.Write(buildServerFrame(0x2, []byte("hello")))
	mustFrame(t, pageEnd, pageRd, 0x1, `{"type":"paired"}`)
	mustFrame(t, pageEnd, pageRd, 0x2, "hello")
	pageEnd.Write(masked(t, 0x2, "ls\r"))
	mustFrame(t, srvEnd, srvRd, 0x2, "ls\r")
	pageEnd.Write(masked(t, 0x1, `{"type":"resize","cols":132,"rows":40}`))
	mustFrame(t, srvEnd, srvRd, 0x1, `{"type":"resize","cols":132,"rows":40}`)

	// Park: the old page's connection ends; its close frame never goes upstream.
	var park string
	var err error
	for i := 0; i < 50; i++ { // (run registers the tunnel from its goroutine)
		if park, err = p.terms.park(tok, termParkMeta{SessionID: "77", Theme: "solarized-dark", Degraded: true}); err != errTermUnknown {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || !reParkToken.MatchString(park) {
		t.Fatalf("park: %q %v", park, err)
	}
	if _, err := p.terms.park(tok, termParkMeta{}); err != errTermState {
		t.Fatalf("a second park: %v", err)
	}
	if _, err := readFrame(t, pageEnd, pageRd); err == nil {
		t.Fatal("the old page still receives")
	}
	pageEnd.Write(masked(t, 0x8, "\x03\xe8"))

	// While parked: output kept, a ping answered by the proxy.
	srvEnd.Write(buildServerFrame(0x2, []byte("a")))
	srvEnd.Write(buildServerFrame(0x2, []byte("b")))
	srvEnd.Write(buildServerFrame(0x9, []byte("pp")))
	mustFrame(t, srvEnd, srvRd, 0xA, "pp") // (not the old page's close)
	srvEnd.Write(buildServerFrame(0x2, []byte("c")))
	time.Sleep(50 * time.Millisecond)

	// Another origin, no origin, a bad token: refused, the park untouched.
	srv := httptest.NewServer(http.HandlerFunc(p.handleTermAttach))
	defer srv.Close()
	if code, _, _ := wsDial(t, srv, "/local/term-attach/"+park, "http://evil"); code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", code)
	}
	if code, _, _ := wsDial(t, srv, "/local/term-attach/"+park, ""); code != http.StatusForbidden {
		t.Fatalf("no origin: %d", code)
	}
	if code, _, _ := wsDial(t, srv, "/local/term-attach/"+strings.ToUpper(park), p.origin); code != http.StatusForbidden {
		t.Fatalf("malformed token: %d", code)
	}
	if p.terms.parkedCount() != 1 {
		t.Fatal("a refused attach consumed the park")
	}

	// The new page: resume, then the history in order, then live.
	code, newEnd, newRd := wsDial(t, srv, "/local/term-attach/"+park, p.origin)
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("attach: %d", code)
	}
	f, err := readFrame(t, newEnd, newRd)
	if err != nil || f.op != 0x1 {
		t.Fatalf("resume frame: %v %v", f, err)
	}
	var res termResume
	if json.Unmarshal(f.payload, &res) != nil || res.Type != "resume" || res.SessionID != "77" || res.Token != tok || res.Cols != 132 || res.Rows != 40 ||
		res.Theme != "solarized-dark" || !res.Degraded || res.Ended || res.Truncated {
		t.Fatalf("resume: %s", f.payload)
	}
	mustFrame(t, newEnd, newRd, 0x1, `{"type":"paired"}`)
	mustFrame(t, newEnd, newRd, 0x2, "hello")
	mustFrame(t, newEnd, newRd, 0x2, "a")
	mustFrame(t, newEnd, newRd, 0x2, "b")
	mustFrame(t, newEnd, newRd, 0x2, "c")
	srvEnd.Write(buildServerFrame(0x2, []byte("d")))
	mustFrame(t, newEnd, newRd, 0x2, "d")
	newEnd.Write(masked(t, 0x2, "pwd\r"))
	mustFrame(t, srvEnd, srvRd, 0x2, "pwd\r")

	// Single use.
	if code, _, _ := wsDial(t, srv, "/local/term-attach/"+park, p.origin); code != http.StatusGone {
		t.Fatalf("a second attach: %d", code)
	}
	if p.terms.parkedCount() != 0 {
		t.Fatal("park left behind")
	}

	// The new page closes the tab: its close goes upstream, the tunnel ends.
	newEnd.Write(masked(t, 0x8, "\x03\xe8"))
	mustFrame(t, srvEnd, srvRd, 0x8, "\x03\xe8")
	srvEnd.Close()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the relay did not end")
	}
	if _, err := p.terms.park(tok, termParkMeta{}); err != errTermUnknown {
		t.Fatalf("park of an ended tunnel: %v", err)
	}
}

// Not taken over in time: the upstream socket gets a clean close (1000),
// the token is dead.
func TestTermParkExpiry(t *testing.T) {
	p, ft := testTermProxy()
	p.terms.expect("t1", "https://srv")
	srvEnd, upConn := tcpPair(t)
	pageEnd, pageConn := tcpPair(t)
	_ = pageEnd
	srvRd := bufio.NewReader(srvEnd)
	go p.terms.run("t1", upConn, bufio.NewReader(upConn), pageConn, bufio.NewReader(pageConn))
	var park string
	var err error
	for i := 0; i < 50; i++ {
		if park, err = p.terms.park("t1", termParkMeta{}); err != errTermUnknown {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	ft.fire(0)
	mustFrame(t, srvEnd, srvRd, 0x8, "\x03\xe8")
	srvEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := srvRd.ReadByte(); err == nil {
		t.Fatal("upstream still open after the expiry")
	}
	if _, err := p.terms.claim(park); err != errTermNoPark {
		t.Fatalf("claim after expiry: %v", err)
	}
	if p.terms.parkedCount() != 0 {
		t.Fatal("expired park kept")
	}
}

// The shell ends while parked: the new page gets the resume (ended), the
// history, then a close.
func TestTermParkUpstreamEndsWhileParked(t *testing.T) {
	p, _ := testTermProxy()
	p.terms.expect("t2", "https://srv")
	srvEnd, upConn := tcpPair(t)
	_, pageConn := tcpPair(t)
	go p.terms.run("t2", upConn, bufio.NewReader(upConn), pageConn, bufio.NewReader(pageConn))
	var park string
	var err error
	for i := 0; i < 50; i++ {
		if park, err = p.terms.park("t2", termParkMeta{SessionID: "9"}); err != errTermUnknown {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	srvEnd.Write(buildServerFrame(0x2, []byte("bye")))
	srvEnd.Close()
	time.Sleep(50 * time.Millisecond)
	tun, err := p.terms.claim(park)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	wc := &bufCloser{Buffer: &out}
	if tun.attach(wc) {
		t.Fatal("attach of an ended tunnel said live")
	}
	rd := bufio.NewReader(&out)
	h, _ := readWSHeader(rd)
	pl := make([]byte, h.length)
	io.ReadFull(rd, pl)
	if !strings.Contains(string(pl), `"ended":true`) {
		t.Fatalf("resume: %s", pl)
	}
	h, _ = readWSHeader(rd)
	pl = make([]byte, h.length)
	io.ReadFull(rd, pl)
	if string(pl) != "bye" {
		t.Fatalf("history: %q", pl)
	}
	if h, _ = readWSHeader(rd); h.opcode != 0x8 || !wc.closed {
		t.Fatal("no close after the history")
	}
}

type bufCloser struct {
	*bytes.Buffer
	closed bool
}

func (b *bufCloser) Close() error { b.closed = true; return nil }

// The history is bounded: whole frames, the oldest first, never a message
// without its start; replay order kept.
func TestTermRingBound(t *testing.T) {
	r := termRing{cap: 100}
	fr := func(op byte, n int, c byte) []byte { return buildServerFrame(op, bytes.Repeat([]byte{c}, n)) }
	for i := 0; i < 10; i++ {
		r.push(fr(0x2, 28, byte('a'+i))) // 30 bytes each
		if r.size > r.cap {
			t.Fatalf("size %d over %d", r.size, r.cap)
		}
	}
	s := r.snapshot()
	if len(s) != 3 || !r.dropped || s[0][2] != 'h' || s[1][2] != 'i' || s[2][2] != 'j' {
		t.Fatalf("kept %d frames", len(s))
	}
	// A fragmented message whose first frame goes takes its continuations along.
	r = termRing{cap: 100}
	first := buildServerFrame(0x2, bytes.Repeat([]byte{'x'}, 28))
	first[0] &^= 0x80 // (not final)
	r.push(first)
	r.push(buildServerFrame(0x0, bytes.Repeat([]byte{'y'}, 28)))
	r.push(fr(0x2, 28, 'z'))
	r.push(fr(0x2, 28, 'w')) // over: "x" goes, and "y" (its continuation) with it
	s = r.snapshot()
	if len(s) != 2 || s[0][2] != 'z' || s[1][2] != 'w' {
		t.Fatalf("after eviction: %d frames, head %q", len(s), s[0][2])
	}
	// Larger than the cap: a gap, nothing kept.
	r.push(fr(0x2, 200, 'q'))
	if len(r.snapshot()) != 0 || r.size != 0 {
		t.Fatal("an oversized frame was kept")
	}
	// The real cap.
	if termRingCap != 2<<20 || newTermParks().ringCap != termRingCap || newTermParks().ttl != 15*time.Second {
		t.Fatal("bounds")
	}
}

// Frames of a page that is no longer the tunnel's never go upstream.
func TestTermPumpPageAfterPark(t *testing.T) {
	var up bytes.Buffer
	reg := newTermParks()
	old := &bufCloser{Buffer: &bytes.Buffer{}}
	tun := reg.newTunnel("t3", &up, io.NopCloser(nil), old)
	tun.page = nil // (parked)
	in := bufio.NewReader(bytes.NewReader(append(masked(t, 0x2, "rm -rf /\r"), masked(t, 0x8, "\x03\xe8")...)))
	tun.pumpPage(old, in)
	if up.Len() != 0 {
		t.Fatalf("forwarded %d bytes of a detached page", up.Len())
	}
}

// /local/term-park: POST only, from the local UI only, a known terminal only.
func TestTermParkHandler(t *testing.T) {
	p, _ := testTermProxy()
	post := func(origin, body string) int {
		r := httptest.NewRequest(http.MethodPost, "/local/term-park", strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		p.handleTermPark(w, r)
		return w.Code
	}
	if c := post("", `{"token":"abc"}`); c != http.StatusForbidden {
		t.Fatalf("no origin: %d", c)
	}
	if c := post("http://evil", `{"token":"abc"}`); c != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", c)
	}
	if c := post(p.origin, `{"token":"../x"}`); c != http.StatusBadRequest {
		t.Fatalf("bad token: %d", c)
	}
	if c := post(p.origin, `{"token":"abc"}`); c != http.StatusNotFound {
		t.Fatalf("unknown terminal: %d", c)
	}
	// A live one: parked, a token back; again: conflict.
	reg := p.terms
	reg.newTunnel("live1", io.Discard, io.NopCloser(nil), &bufCloser{Buffer: &bytes.Buffer{}})
	r := httptest.NewRequest(http.MethodPost, "/local/term-park", strings.NewReader(`{"token":"live1","sessionId":"5","theme":"<b>"}`))
	r.Header.Set("Origin", p.origin)
	w := httptest.NewRecorder()
	p.handleTermPark(w, r)
	var out struct{ Park string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || !reParkToken.MatchString(out.Park) {
		t.Fatalf("park: %d %s", w.Code, w.Body)
	}
	tun, _ := reg.claim(out.Park)
	if tun == nil || tun.meta.SessionID != "5" || tun.meta.Theme != "" {
		t.Fatalf("meta not sanitized: %+v", tun)
	}
	if c := post(p.origin, `{"token":"live1"}`); c != http.StatusConflict {
		t.Fatalf("park of a claimed (not attached) tunnel: %d", c)
	}
	// Two parks: two different tokens.
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("x%d", i)
		reg.newTunnel(id, io.Discard, io.NopCloser(nil), &bufCloser{Buffer: &bytes.Buffer{}})
		tk, err := reg.park(id, termParkMeta{})
		if err != nil || seen[tk] {
			t.Fatalf("park %d: %q %v", i, tk, err)
		}
		seen[tk] = true
	}
}

// The server's answer decides what is a terminal (never the page).
func TestCaptureSessionResponseTerminal(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels(), terms: newTermParks()}
	for _, proto := range []string{"cmd", "powershell", "ssh"} {
		body := `{"data":{"sessionToken":"tok_` + proto + `","protocol":"` + proto + `"}}`
		resp := &http.Response{StatusCode: 201, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
		p.captureSessionResponse(resp, "https://srv")
		if !p.terms.isTerminal("tok_"+proto, "https://srv") {
			t.Fatalf("%s not registered", proto)
		}
		if _, _, ok := p.tunnels.lookup("tok_" + proto); ok {
			t.Fatalf("%s registered for the vault", proto)
		}
	}
	resp := &http.Response{StatusCode: 201, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":{"sessionToken":"r1","protocol":"oblireach"}}`))}
	p.captureSessionResponse(resp, "https://srv")
	if p.terms.isTerminal("r1", "https://srv") {
		t.Fatal("an ObliReach session taken for a terminal")
	}
	for i := 0; i < termMaxExpected+10; i++ {
		p.terms.expect(fmt.Sprintf("e%d", i), "o")
	}
	if len(p.terms.expected) != termMaxExpected || p.terms.isTerminal("e0", "o") {
		t.Fatal("expected terminals unbounded")
	}
}

// A moved tool tab's window: the park token for a terminal only, the folder
// for the explorer only (escaped), anything else dropped.
func TestEntryTargetMovedTool(t *testing.T) {
	park := strings.Repeat("0f", 32)
	p := &Proxy{origin: "http://127.0.0.1:1", secret: "s"}
	u, _ := url.Parse(p.detachedToolEntryURL("12", "A", -1, false, "ssh", park, ""))
	if got := entryTarget(u.Query()); got != "/?detached=12&p=A&m=ssh&pk="+park {
		t.Fatalf("terminal: %s", got)
	}
	u, _ = url.Parse(p.detachedToolEntryURL("12", "", -1, false, "files", "", "C:/a b&c"))
	if got := entryTarget(u.Query()); got != "/?detached=12&m=files&fp=C%3A%2Fa+b%26c" {
		t.Fatalf("explorer: %s", got)
	}
	for _, q := range []url.Values{
		{"d": {"12"}, "m": {"files"}, "pk": {park}},
		{"d": {"12"}, "pk": {park}},
		{"d": {"12"}, "m": {"cmd"}, "pk": {"ABC"}},
		{"d": {"12"}, "m": {"cmd"}, "fp": {"C:/"}},
		{"d": {"12"}, "m": {"files"}, "fp": {"a\nb"}},
	} {
		if got := entryTarget(q); strings.Contains(got, "pk=") || strings.Contains(got, "fp=") {
			t.Fatalf("%v kept: %s", q, got)
		}
	}
}
