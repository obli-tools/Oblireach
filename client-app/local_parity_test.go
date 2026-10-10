package main

// local_parity_test.go: a local SSH connection's terminal is the agent's
// terminal (ssh_relay.go: the same tunnel protocol, relayed by the proxy so
// it parks and moves between windows like one), its files go in chunks
// (sftp_stream.go: no 100 MB per file), its shell's folder is asked of the
// connection (a drop of files), its host's reachability (conn_probe.go),
// and the local base wears its servers' theme.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ── A fake SSH connection and its shell ──

type fakeShell struct {
	mu      sync.Mutex
	in      []string
	cols    int
	rows    int
	closed  bool
	onData  func([]byte)
	onClose func(error)
	once    sync.Once
}

func (s *fakeShell) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("closed")
	}
	s.in = append(s.in, string(p))
	return len(p), nil
}

func (s *fakeShell) Resize(c, r int) error {
	s.mu.Lock()
	s.cols, s.rows = c, r
	s.mu.Unlock()
	return nil
}

func (s *fakeShell) end(err error) {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.once.Do(func() { s.onClose(err) })
}

func (s *fakeShell) Close() error { s.end(nil); return nil }

func (s *fakeShell) input() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.in, "")
}

func (s *fakeShell) size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

type fakeSSHClient struct {
	mu      sync.Mutex
	shell   *fakeShell
	opened  chan *fakeShell
	closed  bool
	openErr error
	execOut string
	execs   []string
}

func newFakeSSHClient() *fakeSSHClient { return &fakeSSHClient{opened: make(chan *fakeShell, 1)} }

func (c *fakeSSHClient) OpenShell(cols, rows int, onData func([]byte), onClose func(error)) (localShellConn, error) {
	if c.openErr != nil {
		return nil, c.openErr
	}
	s := &fakeShell{cols: cols, rows: rows, onData: onData, onClose: onClose}
	c.mu.Lock()
	c.shell = s
	c.mu.Unlock()
	c.opened <- s
	return s, nil
}

func (c *fakeSSHClient) Exec(cmd string, _ time.Duration) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execs = append(c.execs, cmd)
	return []byte(c.execOut), nil
}

func (c *fakeSSHClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeSSHClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func waitShell(t *testing.T, c *fakeSSHClient) *fakeShell {
	t.Helper()
	select {
	case s := <-c.opened:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("no shell opened")
	}
	return nil
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func testLocalShellProxy() (*Proxy, *fakeTimers, *fakeTimers) {
	p, parkTimers := testTermProxy()
	shellTimers := &fakeTimers{}
	p.localShells = newLocalShellRegistry()
	p.localShells.after = shellTimers.after
	return p, parkTimers, shellTimers
}

// The whole life of a local terminal: its tunnel (the relay's protocol: paired,
// output, keystrokes, resize), its folder asked, a move to another window
// (parked, the shell kept, its history and live traffic on the new page,
// exactly as an agent's: term_park.go), then its end with the tab.
func TestLocalShellTunnelParkAttach(t *testing.T) {
	p, _, _ := testLocalShellProxy()
	fc := newFakeSSHClient()
	fc.execOut = "1\n/srv/my app\n"
	tok, err := p.localShells.add(fc, 100, 30, "conn")
	if err != nil || !reParkToken.MatchString(tok) {
		t.Fatalf("add: %q %v", tok, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(p.handleSSHTunnel))
	defer srv.Close()
	// Refused: another origin, no origin, a malformed token, an unknown one.
	if code, _, _ := wsDial(t, srv, "/local/ssh-tunnel/"+tok, "http://evil"); code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", code)
	}
	if code, _, _ := wsDial(t, srv, "/local/ssh-tunnel/"+tok, ""); code != http.StatusForbidden {
		t.Fatalf("no origin: %d", code)
	}
	if code, _, _ := wsDial(t, srv, "/local/ssh-tunnel/"+strings.ToUpper(tok), p.origin); code != http.StatusForbidden {
		t.Fatalf("malformed token: %d", code)
	}
	if code, _, _ := wsDial(t, srv, "/local/ssh-tunnel/"+strings.Repeat("ab", 32), p.origin); code != http.StatusGone {
		t.Fatalf("unknown token: %d", code)
	}
	if n, _ := p.localShells.counts(); n != 1 {
		t.Fatal("a refused tunnel consumed the connection")
	}

	code, page, pageRd := wsDial(t, srv, "/local/ssh-tunnel/"+tok, p.origin)
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("tunnel: %d", code)
	}
	sh := waitShell(t, fc)
	if c, r := sh.size(); c != 100 || r != 30 {
		t.Fatalf("shell size %dx%d", c, r)
	}
	mustFrame(t, page, pageRd, 0x1, `{"type":"paired"}`)
	sh.onData([]byte("root@box:~# "))
	mustFrame(t, page, pageRd, 0x2, "root@box:~# ")
	page.Write(masked(t, 0x2, "ls\r"))
	page.Write(masked(t, 0x1, `{"type":"resize","cols":132,"rows":40}`))
	eventually(t, "keystrokes and size reach the shell", func() bool { c, r := sh.size(); return sh.input() == "ls\r" && c == 132 && r == 40 })
	// (a text frame that is not a resize never reaches the shell)
	page.Write(masked(t, 0x1, `hello`))
	// Single use.
	if code, _, _ := wsDial(t, srv, "/local/ssh-tunnel/"+tok, p.origin); code != http.StatusGone {
		t.Fatalf("a second tunnel: %d", code)
	}
	// Its folder, asked on the same connection (a drop of files).
	eventually(t, "the shell is live", func() bool { return p.localShells.getLive(tok) != nil })
	if c := p.localShells.cwd(tok); c.Path != "/srv/my app" || !c.Sure {
		t.Fatalf("cwd: %+v", c)
	}
	if len(fc.execs) != 1 || !strings.Contains(fc.execs[0], "SSH_CONNECTION") {
		t.Fatalf("cwd script: %v", fc.execs)
	}

	// The move: parked like an agent's terminal.
	var park string
	for i := 0; i < 50; i++ {
		if park, err = p.terms.park(tok, termParkMeta{Theme: "nord"}); err != errTermUnknown {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || !reParkToken.MatchString(park) {
		t.Fatalf("park: %q %v", park, err)
	}
	sh.onData([]byte("while parked"))
	if sh.input() != "ls\r" {
		t.Fatalf("shell input: %q", sh.input())
	}
	att := httptest.NewServer(http.HandlerFunc(p.handleTermAttach))
	defer att.Close()
	code, np, npRd := wsDial(t, att, "/local/term-attach/"+park, p.origin)
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("attach: %d", code)
	}
	f, err := readFrame(t, np, npRd)
	var res termResume
	if err != nil || json.Unmarshal(f.payload, &res) != nil || res.Type != "resume" || res.Token != tok || res.Cols != 132 || res.Rows != 40 || res.Theme != "nord" || res.Ended {
		t.Fatalf("resume: %s %v", f.payload, err)
	}
	mustFrame(t, np, npRd, 0x1, `{"type":"paired"}`)
	mustFrame(t, np, npRd, 0x2, "root@box:~# ")
	mustFrame(t, np, npRd, 0x2, "while parked")
	sh.onData([]byte("live"))
	mustFrame(t, np, npRd, 0x2, "live")
	np.Write(masked(t, 0x2, "pwd\r"))
	eventually(t, "the new page types into the same shell", func() bool { return sh.input() == "ls\rpwd\r" })

	// The tab closes: the shell and its connection end, nothing left behind.
	np.Write(masked(t, 0x8, "\x03\xe8"))
	eventually(t, "the shell and its connection closed", func() bool { sh.mu.Lock(); c := sh.closed; sh.mu.Unlock(); return c && fc.isClosed() })
	eventually(t, "its live entry dropped", func() bool { return p.localShells.getLive(tok) == nil })
	if c := p.localShells.cwd(tok); c.Path != "" {
		t.Fatalf("cwd of an ended shell: %+v", c)
	}
}

// The shell ends by itself: an exit is no error (a close only), a broken
// connection says why first ({type:'error'}, as the agent's relay does).
func TestLocalShellEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"exit", &ssh.ExitMissingError{}, ""},
		{"eof", nil, ""},
		{"broken", errors.New("connection reset"), "connection reset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _ := testLocalShellProxy()
			fc := newFakeSSHClient()
			tok, _ := p.localShells.add(fc, 80, 24, "c")
			srv := httptest.NewServer(http.HandlerFunc(p.handleSSHTunnel))
			defer srv.Close()
			code, page, rd := wsDial(t, srv, "/local/ssh-tunnel/"+tok, p.origin)
			if code != http.StatusSwitchingProtocols {
				t.Fatalf("tunnel: %d", code)
			}
			sh := waitShell(t, fc)
			mustFrame(t, page, rd, 0x1, `{"type":"paired"}`)
			sh.end(tc.err)
			if tc.reason != "" {
				f, err := readFrame(t, page, rd)
				var m map[string]string
				if err != nil || f.op != 0x1 || json.Unmarshal(f.payload, &m) != nil || m["type"] != "error" || m["message"] != tc.reason {
					t.Fatalf("error frame: %v %s", err, f.payload)
				}
			}
			mustFrame(t, page, rd, 0x8, "\x03\xe8")
			eventually(t, "its connection closed", fc.isClosed)
		})
	}
	// A shell that cannot open: said, then closed.
	p, _, _ := testLocalShellProxy()
	fc := newFakeSSHClient()
	fc.openErr = errors.New("no pty")
	tok, _ := p.localShells.add(fc, 80, 24, "c")
	srv := httptest.NewServer(http.HandlerFunc(p.handleSSHTunnel))
	defer srv.Close()
	_, page, rd := wsDial(t, srv, "/local/ssh-tunnel/"+tok, p.origin)
	mustFrame(t, page, rd, 0x1, `{"type":"paired"}`)
	mustFrame(t, page, rd, 0x1, `{"code":"","message":"no pty","type":"error"}`)
	mustFrame(t, page, rd, 0x8, "\x03\xe8")
}

// A dialed connection whose page never opened its tunnel is closed; the
// pending ones are bounded.
func TestLocalShellPendingExpires(t *testing.T) {
	_, _, timers := testLocalShellProxy()
	reg := newLocalShellRegistry()
	reg.after = timers.after
	fc := newFakeSSHClient()
	tok, _ := reg.add(fc, 80, 24, "c")
	timers.fire(0)
	if !fc.isClosed() || reg.take(tok) != nil {
		t.Fatal("an unclaimed connection stays open")
	}
	// Claimed in time: the timer is stopped, the connection kept.
	fc2 := newFakeSSHClient()
	tok2, _ := reg.add(fc2, 80, 24, "c")
	if reg.take(tok2) == nil {
		t.Fatal("take")
	}
	timers.fire(1)
	if fc2.isClosed() {
		t.Fatal("a claimed connection closed by its timer")
	}
	for i := 0; i < localShellMaxPending; i++ {
		if _, err := reg.add(newFakeSSHClient(), 80, 24, "c"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if _, err := reg.add(newFakeSSHClient(), 80, 24, "c"); err != errLocalShellFull {
		t.Fatalf("over the bound: %v", err)
	}
}

func TestParseLocalCwd(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want localCwd
	}{
		{"1\n/home/op\n", localCwd{"/home/op", true}},
		{"0\n/var/www\n", localCwd{"/var/www", false}},
		{"1\n/srv/a b\n", localCwd{"/srv/a b", true}},
		{"1\nrelative\n", localCwd{}},
		{"", localCwd{}},
		{"1\n/a\x00b\n", localCwd{}},
		{"1\n/" + strings.Repeat("a", maxToolPath+1) + "\n", localCwd{}},
	} {
		if got := parseLocalCwd([]byte(tc.in)); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
	// The script reads nothing it should not: its own processes only, no PTY of its own.
	for _, s := range []string{`[ -r "$d/environ" ]`, `SSH_CONNECTION=$c`, `[ "$5" != "0" ]`, `readlink "$d/cwd"`} {
		if !strings.Contains(localCwdScript, s) {
			t.Errorf("cwd script lacks %s", s)
		}
	}
}

// Files of any size to and from a local SSH connection, in chunks, on the
// REAL in-process SFTP server: in order only, its folders created, an
// existing file refused unless overwriting, a stopped upload's partial file
// removed, the handles bounded and closed with the session.
func TestSftpStreamRoundTrip(t *testing.T) {
	srv := startTestSFTPServer(t, "u", "p")
	sess := withEd25519HostKeyDial(t, srv, "u", "p")
	fs := sftpExplorerFS{sess}
	home := explorerHome(t, fs)
	cmd := func(typ string, payload interface{}) map[string]interface{} {
		t.Helper()
		b, _ := json.Marshal(payload)
		return sftpRunExplorerCmd(sess, typ, b)
	}
	result := func(out map[string]interface{}) map[string]interface{} {
		t.Helper()
		if out["ok"] != true {
			t.Fatalf("command failed: %v", out)
		}
		j, _ := json.Marshal(out["result"])
		var m map[string]interface{}
		_ = json.Unmarshal(j, &m)
		return m
	}
	file := explorerJoinPath(explorerJoinPath(explorerJoinPath(home, "deep"), "er"), "big.bin")
	h := result(cmd("upload_open", map[string]interface{}{"path": file}))["handle"].(string)
	chunks := []string{strings.Repeat("A", 1000), strings.Repeat("B", 1000), "end"}
	var off int64
	for _, c := range chunks {
		out := result(cmd("upload_write", map[string]interface{}{"handle": h, "offset": off, "data": base64.StdEncoding.EncodeToString([]byte(c))}))
		off += int64(len(c))
		if int64(out["written"].(float64)) != off {
			t.Fatalf("written %v, want %d", out["written"], off)
		}
	}
	// Out of order: refused (never a hole in the file).
	if out := cmd("upload_write", map[string]interface{}{"handle": h, "offset": 5, "data": "QQ=="}); out["ok"] != false {
		t.Fatalf("out of order write: %v", out)
	}
	if out := result(cmd("upload_close", map[string]interface{}{"handle": h})); int64(out["size"].(float64)) != off {
		t.Fatalf("close: %v", out)
	}
	if out := cmd("upload_write", map[string]interface{}{"handle": h, "offset": off, "data": "QQ=="}); out["ok"] != false {
		t.Fatal("a closed handle still writes")
	}
	// Already there: refused without overwrite (the transfer asks), replaced with it.
	if out := cmd("upload_open", map[string]interface{}{"path": file}); out["ok"] != false || !strings.Contains(out["error"].(string), "already exists") {
		t.Fatalf("existing without overwrite: %v", out)
	}
	// Read back in chunks.
	d := result(cmd("download_open", map[string]interface{}{"path": file}))
	rh, size := d["handle"].(string), int64(d["size"].(float64))
	if size != off {
		t.Fatalf("size %d", size)
	}
	var got strings.Builder
	for o := int64(0); o < size; o += 700 {
		r := result(cmd("download_read", map[string]interface{}{"handle": rh, "offset": o, "length": 700}))
		b, _ := base64.StdEncoding.DecodeString(r["data"].(string))
		got.Write(b)
	}
	result(cmd("download_close", map[string]interface{}{"handle": rh}))
	if got.String() != strings.Join(chunks, "") {
		t.Fatalf("read back %d bytes, differ", got.Len())
	}
	// A stopped upload: its partial file goes.
	part := explorerJoinPath(home, "part.bin")
	ph := result(cmd("upload_open", map[string]interface{}{"path": part}))["handle"].(string)
	result(cmd("upload_write", map[string]interface{}{"handle": ph, "offset": 0, "data": "QUJD"}))
	result(cmd("upload_abort", map[string]interface{}{"handle": ph}))
	if _, err := fs.Lstat(part); err == nil {
		t.Fatal("a stopped upload left its partial file")
	}
	// Bounded, and closed with the session.
	var hs []string
	for i := 0; i < sftpStreamMax; i++ {
		hs = append(hs, result(cmd("download_open", map[string]interface{}{"path": file}))["handle"].(string))
	}
	if out := cmd("download_open", map[string]interface{}{"path": file}); out["ok"] != false {
		t.Fatal("more handles than the bound")
	}
	if n := sess.streamsOf().count(); n != sftpStreamMax {
		t.Fatalf("handles: %d", n)
	}
	sess.Close()
	if n := sess.streamsOf().count(); n != 0 {
		t.Fatalf("handles left after close: %d", n)
	}
	if out := cmd("download_read", map[string]interface{}{"handle": hs[0], "offset": 0, "length": 10}); out["ok"] != false || out["gone"] != true {
		t.Fatalf("a closed session's handle: %v", out)
	}
}

// The one-message upload creates the file's folders too (a folder's files
// sent one by one, as the agent does).
func TestLocalUploadCreatesFolders(t *testing.T) {
	srv := startTestSFTPServer(t, "u", "p")
	sess := withEd25519HostKeyDial(t, srv, "u", "p")
	fs := sftpExplorerFS{sess}
	home := explorerHome(t, fs)
	p := explorerJoinPath(explorerJoinPath(explorerJoinPath(home, "a"), "b"), "c.txt")
	if _, err := explorerRun(t, fs, "upload_file", map[string]interface{}{"path": p, "data": base64.StdEncoding.EncodeToString([]byte("x"))}); err != nil {
		t.Fatalf("upload into new folders: %v", err)
	}
	if fi, err := fs.Lstat(p); err != nil || fi.Size() != 1 {
		t.Fatalf("uploaded file: %v", err)
	}
}

// The Status column: a TCP connection to each connection's host and port.
func TestProbeConns(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	addrs := map[string][2]interface{}{"up": {"127.0.0.1", port}, "down": {"127.0.0.1", 1}}
	got := probeConns([]string{"up", "down", "unknown", "up"}, func(id string) (string, int, bool) {
		a, ok := addrs[id]
		if !ok {
			return "", 0, false
		}
		return a[0].(string), a[1].(int), true
	}, func(addr string, d time.Duration) (net.Conn, error) { return net.DialTimeout("tcp", addr, time.Second) })
	if len(got) != 2 || got["up"] != true || got["down"] != false {
		t.Fatalf("probe: %v", got)
	}
}

// A local base (single mode) has no account: the app keeps its servers'
// theme (before: the default one, another background).
func TestThemeLocalBaseKeepsServersTheme(t *testing.T) {
	cfg := &Config{Profiles: []ServerProfile{
		{ID: "aaaaaaaaaaaaaaaa", Name: "Srv", ServerURL: "https://srv", AccountTheme: "obli-dim"},
		{ID: "bbbbbbbbbbbbbbbb", Name: "Local", Kind: profileKindLocal},
	}, ActiveID: "bbbbbbbbbbbbbbbb"}
	if got := cfg.uiTheme(); got != "obli-dim" {
		t.Fatalf("local base theme: %q", got)
	}
	cfg.ActiveID = "aaaaaaaaaaaaaaaa"
	if got := cfg.uiTheme(); got != "obli-dim" {
		t.Fatalf("server theme: %q", got)
	}
	// Only a local base: the default.
	cfg.Profiles = cfg.Profiles[1:]
	cfg.ActiveID = "bbbbbbbbbbbbbbbb"
	if got := cfg.uiTheme(); got != defaultTheme {
		t.Fatalf("local base alone: %q", got)
	}
}
