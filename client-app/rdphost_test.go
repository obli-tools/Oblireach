package main

// rdphost_test.go — the RDP helper adapter (rdphost_proc.go,
// rdphost_relay.go) against a FAKE helper: this test binary run again with
// OBLIREACH_FAKE_RDPHOST=1 speaks the helper's framed protocol (init below).

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake helper: before any test runs (init precedes TestMain).
func init() {
	if os.Getenv("OBLIREACH_FAKE_RDPHOST") == "1" {
		os.Exit(fakeRdpHost())
	}
}

func fakeLogLine(s string) {
	if p := os.Getenv("OBLIREACH_FAKE_RDPHOST_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.WriteString(s + "\n")
			f.Close()
		}
	}
}

func fakeRdpFrame(w *bufio.Writer, typ byte, p []byte) {
	var h [5]byte
	h[0] = typ
	binary.LittleEndian.PutUint32(h[1:], uint32(len(p)))
	w.Write(h[:])
	w.Write(p)
	w.Flush()
}

func fakeStatus(w *bufio.Writer, m map[string]interface{}) {
	b, _ := json.Marshal(m)
	fakeRdpFrame(w, 'S', b)
}

func fakeInit(w *bufio.Writer, wd, ht uint32) {
	var p [8]byte
	binary.LittleEndian.PutUint32(p[:], wd)
	binary.LittleEndian.PutUint32(p[4:], ht)
	fakeRdpFrame(w, 'I', p[:])
	fakeRdpFrame(w, 'F', []byte{0, 0, 0, 1, 0x65, 0x88, 0x84})
}

// fakeRdpHost: OBLIREACH_FAKE_RDPHOST_MODE "full" (certificate, credentials,
// connected, picture, pointer, clipboard, then the input echoed in the log),
// "die" (exits 3 at once after the connect line).
func fakeRdpHost() int {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		os.Stdout.WriteString("rdphost protocol=1 freerdp=fake x264=0\n")
		return 0
	}
	// (the secret must never be in the arguments or the environment)
	fakeLogLine("ARGS " + strings.Join(os.Args[1:], " "))
	fakeLogLine("ENV " + strings.Join(os.Environ(), " "))
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	out := bufio.NewWriter(os.Stdout)
	fakeStatus(out, map[string]interface{}{"state": "waiting_config"})
	line, err := in.ReadString('\n')
	if err != nil {
		return 2
	}
	fakeLogLine("CONNECT " + strings.TrimSpace(line))
	if os.Getenv("OBLIREACH_FAKE_RDPHOST_MODE") == "die" {
		return 3
	}
	var cfg struct {
		Host  string   `json:"host"`
		Port  int      `json:"port"`
		Known []string `json:"knownFingerprints"`
	}
	json.Unmarshal([]byte(line), &cfg)
	next := func() map[string]interface{} {
		l, err := in.ReadString('\n')
		if err != nil {
			return nil
		}
		fakeLogLine("IN " + strings.TrimSpace(l))
		var m map[string]interface{}
		json.Unmarshal([]byte(l), &m)
		return m
	}
	fakeStatus(out, map[string]interface{}{"state": "connecting", "host": cfg.Host, "port": cfg.Port})
	known := false
	for _, k := range cfg.Known {
		if normRdpFingerprint(k) == "abcdef" {
			known = true
		}
	}
	if !known {
		fakeStatus(out, map[string]interface{}{"state": "cert", "host": cfg.Host, "port": cfg.Port, "commonName": "srv", "fingerprint": "sha256 AB:CD:EF"})
		m := next()
		if m == nil || m["type"] != "cert_reply" || m["accept"] != true {
			fakeStatus(out, map[string]interface{}{"state": "ended", "code": "cert_rejected", "message": "certificate rejected"})
			return 4
		}
	}
	fakeStatus(out, map[string]interface{}{"state": "credentials", "reason": "nla", "username": "bob", "domain": ""})
	if m := next(); m == nil || m["type"] != "credentials" {
		return 4
	}
	fakeStatus(out, map[string]interface{}{"state": "connected", "width": 640, "height": 480, "displayControl": true, "clipboard": true})
	fakeStatus(out, map[string]interface{}{"state": "display_control", "ready": true})
	fakeInit(out, 640, 480)
	// a 2x2 pointer: red, transparent, green, blue (BGRA)
	ptr := []byte{2, 1, 0, 1, 0, 2, 0, 2, 0,
		0, 0, 255, 255, 0, 0, 0, 0, 0, 255, 0, 255, 255, 0, 0, 255}
	fakeRdpFrame(out, 'P', ptr)
	fakeRdpFrame(out, 'C', []byte("remote text"))
	for {
		m := next()
		if m == nil {
			return 0
		}
		switch m["type"] {
		case "refresh":
			fakeInit(out, 640, 480)
		case "disconnect":
			return 0
		case "resize":
			fakeInit(out, uint32(m["width"].(float64)), uint32(m["height"].(float64)))
		}
	}
}

// rdpTestEnv: the fake helper set up; its log's path.
func rdpTestEnv(t *testing.T, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "helper.log")
	oldO, oldE := rdpHelperOverride, rdpHelperTestEnv
	rdpHelperOverride = exe
	rdpHelperTestEnv = []string{"OBLIREACH_FAKE_RDPHOST=1", "OBLIREACH_FAKE_RDPHOST_MODE=" + mode, "OBLIREACH_FAKE_RDPHOST_LOG=" + log}
	t.Cleanup(func() { rdpHelperOverride, rdpHelperTestEnv = oldO, oldE })
	return log
}

func readLog(t *testing.T, p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// waitLog waits for want in the helper's log.
func waitLog(t *testing.T, p, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := readLog(t, p); strings.Contains(s, want) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("helper log never had %q:\n%s", want, readLog(t, p))
	return ""
}

// nextJSON: the next text frame of the page's tunnel whose type is typ
// (others skipped; binary frames counted).
func nextJSON(t *testing.T, c net.Conn, rd *bufio.Reader, typ string, bins *int) map[string]interface{} {
	t.Helper()
	for i := 0; i < 64; i++ {
		f, err := readFrame(t, c, rd)
		if err != nil {
			t.Fatalf("waiting for %s: %v", typ, err)
		}
		if f.op == 0x2 {
			if bins != nil {
				*bins++
			}
			continue
		}
		if f.op == 0x8 {
			t.Fatalf("closed while waiting for %s", typ)
		}
		var m map[string]interface{}
		if json.Unmarshal(f.payload, &m) == nil && m["type"] == typ {
			return m
		}
	}
	t.Fatalf("no %s", typ)
	return nil
}

func rdpTestServer(t *testing.T) (*Proxy, *httptest.Server, *rdpStreamRegistry) {
	reg := newRdpStreamRegistry()
	p := &Proxy{origin: "http://127.0.0.1:5555", rdpStreams: reg}
	srv := httptest.NewServer(http.HandlerFunc(p.handleRdpTunnel))
	t.Cleanup(srv.Close)
	return p, srv, reg
}

func TestRdpHelperVersionParse(t *testing.T) {
	if n, err := parseRdpHelperVersion("rdphost protocol=1 freerdp=3.32.0 x264=164\n"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := parseRdpHelperVersion("something else"); err == nil {
		t.Fatal("no protocol accepted")
	}
	rdpTestEnv(t, "full")
	if n, err := rdpHelperVersion(rdpHelperOverride); err != nil || n != 1 {
		t.Fatalf("fake helper version %d %v", n, err)
	}
}

func TestRdpHelperHelpers(t *testing.T) {
	if u, d := rdpSplitUser(`CORP\alice`); u != "alice" || d != "CORP" {
		t.Fatal(u, d)
	}
	if u, d := rdpSplitUser("alice@corp.example"); u != "alice@corp.example" || d != "" {
		t.Fatal(u, d)
	}
	if rdpEvenSize(1023) != 1022 || rdpEvenSize(10) != 200 || rdpEvenSize(99999) != 8192 {
		t.Fatal("sizes")
	}
	if d, v := rdpScales(150); d != 150 || v != 140 {
		t.Fatal(d, v)
	}
	if d, v := rdpScales(20); d != 100 || v != 100 {
		t.Fatal(d, v)
	}
	if rdpLayoutOf("fr") != "0000040C" || rdpLayoutOf("xx") != "00000409" {
		t.Fatal("layouts")
	}
	if rdpHostKey("Srv.Example", 3389) != "srv.example:3389" || rdpHostKey("", 1) != "" || rdpHostKey("a b", 1) != "" || rdpHostKey("a", 70000) != "" {
		t.Fatal("host keys")
	}
}

// The input whitelist: bounded, nothing else gets through.
func TestRdpFilterInput(t *testing.T) {
	f := func(s string) string {
		var m rdpPageMsg
		if json.Unmarshal([]byte(s), &m) != nil {
			t.Fatal(s)
		}
		b, ok := rdpFilterInput(&m)
		if !ok {
			return "-"
		}
		return string(b)
	}
	cases := map[string]string{
		`{"type":"mouse","action":"move","x":-5,"y":99999}`:           `{"action":"move","type":"mouse","x":0,"y":65535}`,
		`{"type":"mouse","action":"down","x":1,"y":2,"button":9}`:     `{"action":"down","button":4,"type":"mouse","x":1,"y":2}`,
		`{"type":"mouse","action":"scroll","x":1,"y":2,"delta":0}`:    `-`,
		`{"type":"mouse","action":"scroll","x":1,"y":2,"delta":-500}`: `{"action":"scroll","delta":-100,"type":"mouse","x":1,"y":2}`,
		`{"type":"mouse","action":"teleport"}`:                        `-`,
		`{"type":"key","action":"down","code":"KeyA","key":"a"}`:      `{"action":"down","code":"KeyA","key":"a","type":"key"}`,
		`{"type":"key","action":"down","key":"aaaaaaaaaaaaa"}`:        `-`,
		`{"type":"key","action":"press","code":"KeyA"}`:               `-`,
		`{"type":"sas"}`:                      `{"type":"sas"}`,
		`{"type":"refresh"}`:                  `{"type":"refresh"}`,
		`{"type":"connect","host":"evil"}`:    `-`,
		`{"type":"disconnect"}`:               `-`,
		`{"type":"cert_reply","accept":true}`: `-`,
		`{"type":"type_text","text":""}`:      `-`,
		`{"type":"sync","caps":true}`:         `{"caps":true,"num":false,"scroll":false,"type":"sync"}`,
	}
	for in, want := range cases {
		if got := f(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestRdpCursorMsg(t *testing.T) {
	if string(rdpCursorMsg([]byte{0})) != `{"type":"cursor","kind":"hidden"}` || rdpCursorMsg([]byte{7}) != nil {
		t.Fatal("kinds")
	}
	ptr := []byte{2, 1, 0, 1, 0, 2, 0, 1, 0, 0, 0, 255, 255, 0, 255, 0, 128}
	var m map[string]interface{}
	json.Unmarshal(rdpCursorMsg(ptr), &m)
	raw, _ := base64.StdEncoding.DecodeString(m["png"].(string))
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil || m["kind"] != "shape" || m["hx"].(float64) != 1 {
		t.Fatalf("%v %v", m, err)
	}
	r, g, b, a := img.At(0, 0).RGBA()
	if r>>8 != 255 || g != 0 || b != 0 || a>>8 != 255 {
		t.Fatalf("pixel 0: %d %d %d %d", r, g, b, a)
	}
	// (a truncated shape: the default arrow)
	if string(rdpCursorMsg(ptr[:12])) != `{"type":"cursor","kind":"default"}` {
		t.Fatal("truncated")
	}
}

func TestRdpKnownHostsStore(t *testing.T) {
	s := &rdpKnownHosts{path: filepath.Join(t.TempDir(), "sub", rdpKnownHostsFile)}
	if len(s.known("a:1")) != 0 {
		t.Fatal("empty store")
	}
	if err := s.trust("srv:3389", "sha256 AB:CD:EF", false); err != nil {
		t.Fatal(err)
	}
	s.trust("srv:3389", "abcdef", false) // (the same: once)
	s.trust("other:3389", "0102", false)
	if k := s.known("srv:3389"); len(k) != 1 || k[0] != "abcdef" {
		t.Fatal(k)
	}
	s.trust("srv:3389", "ffff", false)
	if k := s.known("srv:3389"); len(k) != 2 {
		t.Fatal(k)
	}
	// a confirmed change replaces the old ones
	s.trust("srv:3389", "eeee", true)
	if k := s.known("srv:3389"); len(k) != 1 || k[0] != "eeee" {
		t.Fatal(k)
	}
	if k := s.known("other:3389"); len(k) != 1 {
		t.Fatal(k)
	}
	if err := s.trust("srv:3389", "zz", false); err == nil {
		t.Fatal("invalid fingerprint filed")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(s.path); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
	var nilStore *rdpKnownHosts
	if nilStore.known("srv:3389") != nil || nilStore.trust("srv:3389", "ab", false) == nil {
		t.Fatal("nil store")
	}
}

// The whole session: the secret on stdin only, the certificate asked and
// remembered, the credentials prompt (never a password to the page), the
// picture, pointer, clipboard, input, a park and take-over by another
// window, then the end.
func TestRdpStreamFlow(t *testing.T) {
	log := rdpTestEnv(t, "full")
	_, srv, reg := rdpTestServer(t)
	known := &rdpKnownHosts{path: filepath.Join(t.TempDir(), rdpKnownHostsFile)}
	parks := newRdpParkReg()
	reg.parks = parks
	owner, other := &nativeBridge{}, &nativeBridge{}
	var saved struct {
		sync.Mutex
		user, pass string
	}
	s, err := reg.start(rdpConnectReq{Host: "Srv.Example", Port: 3389, Username: "alice", Domain: "CORP", Password: "s3cr3t-P@ss",
		Width: 1023, Height: 769, Scale: 150, Layout: "0000040C",
		SaveCreds: func(u, p string) { saved.Lock(); saved.user, saved.pass = u, p; saved.Unlock() }}, owner, known)
	if err != nil {
		t.Fatal(err)
	}
	defer s.helper.kill()
	lg := waitLog(t, log, "CONNECT ")
	if strings.Contains(lg[strings.Index(lg, "ARGS"):strings.Index(lg, "CONNECT")], "s3cr3t") {
		t.Fatal("the secret reached the helper's arguments or environment")
	}
	var conn map[string]interface{}
	json.Unmarshal([]byte(strings.SplitN(lg[strings.Index(lg, "CONNECT ")+8:], "\n", 2)[0]), &conn)
	if conn["password"] != "s3cr3t-P@ss" || conn["username"] != "alice" || conn["domain"] != "CORP" || conn["width"].(float64) != 1022 ||
		conn["desktopScale"].(float64) != 150 || conn["deviceScale"].(float64) != 140 || conn["keyboardLayout"] != "0000040C" {
		t.Fatalf("connect line %v", conn)
	}
	if s.ownedBy(owner) == false || s.ownedBy(other) {
		t.Fatal("owner")
	}
	tok, err := reg.issueTunnel(s)
	if err != nil {
		t.Fatal(err)
	}
	// wrong origin, unknown token
	if code, _, _ := wsDial(t, srv, "/local/rdp-tunnel/"+tok, "http://evil"); code != http.StatusForbidden {
		t.Fatalf("origin: %d", code)
	}
	if code, _, _ := wsDial(t, srv, "/local/rdp-tunnel/"+strings.Repeat("0", 64), "http://127.0.0.1:5555"); code != http.StatusGone {
		t.Fatalf("unknown token: %d", code)
	}
	code, c, rd := wsDial(t, srv, "/local/rdp-tunnel/"+tok, "http://127.0.0.1:5555")
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("tunnel: %d", code)
	}
	nextJSON(t, c, rd, "paired", nil)
	cert := nextJSON(t, c, rd, "rdp_cert", nil)
	if cert["fingerprint"] != "sha256 AB:CD:EF" || cert["changed"] != false || cert["canRemember"] != true {
		t.Fatalf("cert %v", cert)
	}
	c.Write(masked(t, 0x1, `{"type":"rdp_cert_reply","accept":true,"remember":true}`))
	creds := nextJSON(t, c, rd, "rdp_credentials", nil)
	if creds["username"] != "bob" || creds["canSave"] != true {
		t.Fatalf("creds %v", creds)
	}
	if k := known.known("srv.example:3389"); len(k) != 1 || k[0] != "abcdef" {
		t.Fatalf("not remembered: %v", k)
	}
	c.Write(masked(t, 0x1, `{"type":"rdp_credentials","username":"CORP\\bob","password":"typed-Pw","save":true}`))
	bins := 0
	init := nextJSON(t, c, rd, "init", &bins)
	if init["width"].(float64) != 640 || init["codec"] != "h264" || !strings.Contains(strings.Join(toStrings(init["features"]), ","), "rdp_resize") {
		t.Fatalf("init %v", init)
	}
	f, _ := readFrame(t, c, rd)
	if f.op != 0x2 || f.payload[0] != 0x02 || !bytes.Equal(f.payload[1:], []byte{0, 0, 0, 1, 0x65, 0x88, 0x84}) {
		t.Fatalf("frame %v", f)
	}
	if cur := nextJSON(t, c, rd, "cursor", nil); cur["kind"] != "shape" {
		t.Fatalf("cursor %v", cur)
	}
	lg = waitLog(t, log, `"type":"credentials"`)
	if !strings.Contains(lg, `"domain":"CORP","password":"typed-Pw","type":"credentials","username":"bob"`) {
		t.Fatalf("credentials line: %s", lg)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		saved.Lock()
		u, p := saved.user, saved.pass
		saved.Unlock()
		if u == `CORP\bob` && p == "typed-Pw" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not saved: %q %q", u, p)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// clipboard both ways: the remote text kept, asked, then pushed
	c.Write(masked(t, 0x1, `{"type":"clipboard_get","id":7}`))
	if m := nextJSON(t, c, rd, "clipboard_content", nil); m["text"] != "remote text" || m["id"].(float64) != 7 {
		t.Fatalf("clipboard_get %v", m)
	}
	c.Write(masked(t, 0x1, `{"type":"clipboard_set","text":"local text","id":3}`))
	if m := nextJSON(t, c, rd, "clipboard_ack", nil); m["ok"] != true || m["id"].(float64) != 3 {
		t.Fatalf("ack %v", m)
	}
	waitLog(t, log, `{"text":"local text","type":"clipboard_set"}`)
	// input, filtered; a resize
	c.Write(masked(t, 0x1, `{"type":"key","action":"down","code":"KeyA","key":"a"}`))
	c.Write(masked(t, 0x1, `{"type":"connect","host":"evil"}`))
	c.Write(masked(t, 0x1, `{"type":"sas"}`))
	c.Write(masked(t, 0x1, `{"type":"rdp_resize","width":801,"height":601,"dpr":2}`))
	lg = waitLog(t, log, `"type":"resize"`)
	if !strings.Contains(lg, `{"action":"down","code":"KeyA","key":"a","type":"key"}`) || !strings.Contains(lg, `IN {"type":"sas"}`) || strings.Contains(lg, "evil") ||
		!strings.Contains(lg, `"desktopScale":200,"deviceScale":180,"height":600,"type":"resize","width":800`) {
		t.Fatalf("input lines: %s", lg)
	}
	if in := nextJSON(t, c, rd, "init", nil); in["width"].(float64) != 800 {
		t.Fatalf("resize init %v", in)
	}
	// The tab moves: parked (the page let go, the helper goes on), taken
	// over by another window with a new tunnel: a fresh init and key frame.
	ptok, err := s.park()
	if err != nil {
		t.Fatal(err)
	}
	for {
		f, err := readFrame(t, c, rd)
		if err != nil || f.op == 0x8 {
			break
		}
	}
	if s.helper.exited() || s.ownedBy(owner) {
		t.Fatal("parked session ended or still owned")
	}
	if id := parks.take(ptok); id != s.id {
		t.Fatalf("park token: %q", id)
	}
	s.adopt(other)
	tok2, _ := reg.issueTunnel(s)
	_, c2, rd2 := wsDial(t, srv, "/local/rdp-tunnel/"+tok2, "http://127.0.0.1:5555")
	nextJSON(t, c2, rd2, "paired", nil)
	if in := nextJSON(t, c2, rd2, "init", nil); in["width"].(float64) != 800 {
		t.Fatalf("attach init %v", in)
	}
	waitLog(t, log, `IN {"type":"refresh"}`)
	if !s.ownedBy(other) {
		t.Fatal("not owned by the new window")
	}
	// The end: disconnect, the page's tunnel closed, the session forgotten.
	s.end()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	waitLog(t, log, `IN {"type":"disconnect"}`)
	if reg.get(s.id) != nil {
		t.Fatal("still registered")
	}
	for {
		f, err := readFrame(t, c2, rd2)
		if err != nil {
			t.Fatal("no close frame")
		}
		if f.op == 0x8 {
			break
		}
		if f.op == 0x1 && strings.Contains(string(f.payload), `"error"`) {
			t.Fatalf("a normal end told as an error: %s", f.payload)
		}
	}
}

func toStrings(v interface{}) []string {
	var out []string
	if a, ok := v.([]interface{}); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// A known certificate: no question; a refused one ends with rdp_cert_rejected;
// a changed one is only accepted once confirmed as changed.
func TestRdpStreamCertificates(t *testing.T) {
	log := rdpTestEnv(t, "full")
	_, srv, reg := rdpTestServer(t)
	known := &rdpKnownHosts{path: filepath.Join(t.TempDir(), rdpKnownHostsFile)}
	known.trust("srv:3389", "AB:CD:EF", false)
	s, err := reg.start(rdpConnectReq{Host: "srv", Port: 3389, Width: 800, Height: 600}, nil, known)
	if err != nil {
		t.Fatal(err)
	}
	defer s.helper.kill()
	lg := waitLog(t, log, "CONNECT ")
	if !strings.Contains(lg, `"knownFingerprints":["abcdef"]`) {
		t.Fatalf("known fingerprints not passed: %s", lg)
	}
	tok, _ := reg.issueTunnel(s)
	_, c, rd := wsDial(t, srv, "/local/rdp-tunnel/"+tok, "http://127.0.0.1:5555")
	nextJSON(t, c, rd, "rdp_credentials", nil) // (no certificate question)
	s.end()
	<-s.done

	// Another fingerprint trusted for that server: "changed".
	log2 := rdpTestEnv(t, "full")
	known2 := &rdpKnownHosts{path: filepath.Join(t.TempDir(), rdpKnownHostsFile)}
	known2.trust("srv:3389", "0102", false)
	s2, _ := reg.start(rdpConnectReq{Host: "srv", Port: 3389, Width: 800, Height: 600}, nil, known2)
	defer s2.helper.kill()
	tok2, _ := reg.issueTunnel(s2)
	_, c2, rd2 := wsDial(t, srv, "/local/rdp-tunnel/"+tok2, "http://127.0.0.1:5555")
	cert := nextJSON(t, c2, rd2, "rdp_cert", nil)
	if cert["changed"] != true || cert["oldFingerprint"] != "0102" {
		t.Fatalf("changed cert %v", cert)
	}
	// accepted and remembered WITHOUT confirming the change: refused
	c2.Write(masked(t, 0x1, `{"type":"rdp_cert_reply","accept":true,"remember":true}`))
	if e := nextJSON(t, c2, rd2, "error", nil); e["code"] != "rdp_cert_rejected" || e["retry"] != false {
		t.Fatalf("error %v", e)
	}
	waitLog(t, log2, `IN {"accept":false,"type":"cert_reply"}`)
	if k := known2.known("srv:3389"); len(k) != 1 || k[0] != "0102" {
		t.Fatalf("store changed: %v", k)
	}
	<-s2.done

	// Confirmed: accepted, the old fingerprint replaced.
	rdpTestEnv(t, "full")
	s3, _ := reg.start(rdpConnectReq{Host: "srv", Port: 3389, Width: 800, Height: 600}, nil, known2)
	defer s3.helper.kill()
	tok3, _ := reg.issueTunnel(s3)
	_, c3, rd3 := wsDial(t, srv, "/local/rdp-tunnel/"+tok3, "http://127.0.0.1:5555")
	nextJSON(t, c3, rd3, "rdp_cert", nil)
	c3.Write(masked(t, 0x1, `{"type":"rdp_cert_reply","accept":true,"remember":true,"confirmChanged":true}`))
	nextJSON(t, c3, rd3, "rdp_credentials", nil)
	if k := known2.known("srv:3389"); len(k) != 1 || k[0] != "abcdef" {
		t.Fatalf("not replaced: %v", k)
	}
	c3.Write(masked(t, 0x1, `{"type":"rdp_credentials","cancel":false,"username":"x","password":"y"}`))
	nextJSON(t, c3, rd3, "init", nil)
	s3.end()
	<-s3.done
}

// A helper that dies without a word: the page is told rdp_helper_failed;
// a session nobody opens the tunnel of ends.
func TestRdpStreamHelperDiesAndOrphans(t *testing.T) {
	rdpTestEnv(t, "die")
	_, srv, reg := rdpTestServer(t)
	s, err := reg.start(rdpConnectReq{Host: "srv", Port: 3389, Width: 800, Height: 600}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-s.done
	if reg.get(s.id) != nil {
		t.Fatal("still registered")
	}
	// (a page opening its tunnel late gets the error)
	reg.mu.Lock()
	reg.byID[s.id] = s
	reg.mu.Unlock()
	tok, _ := reg.issueTunnel(s)
	_, c, rd := wsDial(t, srv, "/local/rdp-tunnel/"+tok, "http://127.0.0.1:5555")
	nextJSON(t, c, rd, "paired", nil)
	if e := nextJSON(t, c, rd, "error", nil); e["code"] != "rdp_helper_failed" {
		t.Fatalf("error %v", e)
	}

	// The tunnel token expires unused: the session ends.
	log := rdpTestEnv(t, "full")
	ft := &fakeTimers{}
	reg2 := newRdpStreamRegistry()
	reg2.after = ft.after
	s2, err := reg2.start(rdpConnectReq{Host: "srv", Port: 3389, Width: 800, Height: 600}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.helper.kill()
	waitLog(t, log, "CONNECT ")
	if _, err := reg2.issueTunnel(s2); err != nil {
		t.Fatal(err)
	}
	ft.fire(0)
	select {
	case <-s2.done:
	case <-time.After(5 * time.Second):
		t.Fatal("an orphan session did not end")
	}
}

// The helper is searched where the Flatpak and a development tree put it.
func TestRdpHelperCandidates(t *testing.T) {
	c := rdpHelperCandidates()
	joined := strings.Join(c, "|")
	if runtime.GOOS == "linux" && (!strings.Contains(joined, "/app/libexec/oblireach/rdphost") || !strings.Contains(joined, "/app/libexec/oblirdp")) {
		t.Fatal(c)
	}
	t.Setenv("OBLIREACH_RDPHOST", "relative/path")
	if strings.Contains(strings.Join(rdpHelperCandidates(), "|"), "relative/path") {
		t.Fatal("a relative override accepted")
	}
	old := rdpHelperOverride
	rdpHelperOverride = ""
	defer func() { rdpHelperOverride = old }()
	t.Setenv("OBLIREACH_RDPHOST", filepath.Join(t.TempDir(), "absent"))
	if _, err := rdpHelperPath(); err == nil && runtime.GOOS != "linux" {
		t.Fatal("a missing helper found")
	}
}
