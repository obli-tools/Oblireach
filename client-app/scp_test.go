package main

// scp_test.go — scp.go, the SCP fallback of a local SSH connection whose
// server has no SFTP subsystem:
//   - the protocol against a Go peer that answers like OpenSSH's `scp -t` /
//     `scp -f` (records, acknowledgements, errors, D/E folders): always run;
//   - the listing scripts' output parsed (GNU find, the shell's loop);
//   - end to end through a REAL SSH server (golang.org/x/crypto/ssh's server
//     side, no SFTP subsystem) whose exec requests run in a real POSIX shell
//     with the real OpenSSH scp (Git for Windows' sh.exe / scp.exe here):
//     sftpDial falls back, the explorer's commands and the chunked transfers
//     work, a cancelled upload leaves no partial file; and the same server
//     without a shell (scp only): listing refused in words, a file still
//     sent (its folders made by D records). Skipped when no such shell.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ── A Go SCP peer (OpenSSH's sink / source behaviour) ───────────────────────

type peerExit struct{ code int }

func (e *peerExit) Error() string   { return "exit " + strconv.Itoa(e.code) }
func (e *peerExit) ExitStatus() int { return e.code }

type fakeScpProc struct {
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	errBuf bytes.Buffer
	done   chan error
	closed sync.Once
	kill   func()
}

func (p *fakeScpProc) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeScpProc) Stdout() io.Reader     { return p.stdout }
func (p *fakeScpProc) Stderr() string        { return p.errBuf.String() }
func (p *fakeScpProc) Wait() error           { return <-p.done }
func (p *fakeScpProc) Close() error          { p.closed.Do(p.kill); return nil }

// goScpStarter: commands answered in Go against root (POSIX paths below it).
type goScpStarter struct {
	root  string
	shell bool
	mu    sync.Mutex
	cmds  []string
}

func (g *goScpStarter) local(p string) string {
	return filepath.Join(g.root, filepath.FromSlash(strings.TrimPrefix(p, "/")))
}

func unquoteSh(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") {
		s = s[1 : len(s)-1]
	}
	return strings.ReplaceAll(s, `'\''`, "'")
}

func (g *goScpStarter) Start(cmd string) (scpProc, error) {
	g.mu.Lock()
	g.cmds = append(g.cmds, cmd)
	g.mu.Unlock()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &fakeScpProc{stdin: inW, stdout: outR, done: make(chan error, 1)}
	p.kill = func() { inR.CloseWithError(io.ErrClosedPipe); outW.CloseWithError(io.ErrClosedPipe) }
	run := func(f func(r *bufio.Reader, w io.Writer) int) {
		go func() {
			code := f(bufio.NewReader(inR), outW)
			outW.Close()
			if code != 0 {
				p.done <- &peerExit{code}
			} else {
				p.done <- nil
			}
		}()
	}
	switch {
	case strings.HasPrefix(cmd, "scp -t "), strings.HasPrefix(cmd, "scp -r -t "):
		target := g.local(unquoteSh(cmd[strings.Index(cmd, "-t ")+3:]))
		run(func(r *bufio.Reader, w io.Writer) int { return goScpSink(r, w, target, &p.errBuf) })
	case strings.HasPrefix(cmd, "scp -f "):
		src := g.local(unquoteSh(cmd[len("scp -f "):]))
		run(func(r *bufio.Reader, w io.Writer) int { return goScpSourceFile(r, w, src) })
	case g.shell && cmd == "echo "+scpProbeWord:
		run(func(_ *bufio.Reader, w io.Writer) int { io.WriteString(w, scpProbeWord+"\n"); return 0 })
	default:
		run(func(_ *bufio.Reader, _ io.Writer) int { p.errBuf.WriteString("sh: not allowed\n"); return 127 })
	}
	return p, nil
}

func goScpSink(r *bufio.Reader, w io.Writer, target string, stderr *bytes.Buffer) int {
	w.Write([]byte{0})
	stack := []string{target}
	isDir := func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0 // (the end of its input: done)
		}
		line = strings.TrimSuffix(line, "\n")
		cur := stack[len(stack)-1]
		switch line[0] {
		case 'E':
			stack = stack[:len(stack)-1]
			w.Write([]byte{0})
		case 'D', 'C':
			rec, err := parseScpHeader(line)
			if err != nil {
				fmt.Fprintf(w, "\x02scp: protocol error\n")
				return 1
			}
			np := cur
			if isDir(cur) {
				np = filepath.Join(cur, rec.Name)
			}
			if rec.Kind == 'D' {
				if !isDir(np) {
					if err := os.Mkdir(np, 0o755); err != nil {
						fmt.Fprintf(w, "\x01scp: %s: No such file or directory\n", np)
						return 1
					}
				}
				stack = append(stack, np)
				w.Write([]byte{0})
				continue
			}
			f, err := os.OpenFile(np, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				fmt.Fprintf(w, "\x01scp: %s: No such file or directory\n", rec.Name)
				continue
			}
			w.Write([]byte{0})
			_, cerr := io.CopyN(f, r, rec.Size)
			f.Close()
			if cerr != nil {
				stderr.WriteString("scp: short read\n")
				return 1 // (its partial file stays: as OpenSSH's)
			}
			if b, err := r.ReadByte(); err != nil || b != 0 {
				return 1
			}
			w.Write([]byte{0})
		default:
			fmt.Fprintf(w, "\x02scp: unexpected <newline>\n")
			return 1
		}
	}
}

func goScpSourceFile(r *bufio.Reader, w io.Writer, src string) int {
	if b, err := r.ReadByte(); err != nil || b != 0 {
		return 1
	}
	fi, err := os.Stat(src)
	if err != nil {
		fmt.Fprintf(w, "\x01scp: %s: No such file or directory\n", src)
		return 1
	}
	if fi.IsDir() {
		return goScpSourceDir(r, w, src)
	}
	return goScpSendFile(r, w, src, fi)
}

func goScpSendFile(r *bufio.Reader, w io.Writer, src string, fi os.FileInfo) int {
	fmt.Fprintf(w, "C0644 %d %s\n", fi.Size(), filepath.Base(src))
	if b, err := r.ReadByte(); err != nil || b != 0 {
		return 1
	}
	f, err := os.Open(src)
	if err != nil {
		return 1
	}
	if _, err := io.Copy(w, f); err != nil {
		f.Close()
		return 1
	}
	f.Close()
	w.Write([]byte{0})
	if b, err := r.ReadByte(); err != nil || b != 0 {
		return 1
	}
	return 0
}

// (-r: the folder, its files and sub-folders)
func goScpSourceDir(r *bufio.Reader, w io.Writer, dir string) int {
	fmt.Fprintf(w, "D0755 0 %s\n", filepath.Base(dir))
	if b, err := r.ReadByte(); err != nil || b != 0 {
		return 1
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		fi, _ := os.Stat(p)
		if fi.IsDir() {
			if c := goScpSourceDir(r, w, p); c != 0 {
				return c
			}
		} else if c := goScpSendFile(r, w, p, fi); c != 0 {
			return c
		}
	}
	io.WriteString(w, "E\n")
	if b, err := r.ReadByte(); err != nil || b != 0 {
		return 1
	}
	return 0
}

// ── The protocol, against the Go peer ───────────────────────────────────────

func TestScpProtocolRoundTrip(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "srv", "in"), 0o755)
	g := &goScpStarter{root: root, shell: true}
	fs := newScpFS(g)
	if !fs.shell {
		t.Fatal("the probe should find the shell")
	}
	data := bytes.Repeat([]byte("0123456789abcdef"), 70000) // 1.1 MB, several writes
	w, err := fs.CreateSized("/srv/in/big.bin", int64(len(data)))
	if err != nil {
		t.Fatalf("CreateSized: %v", err)
	}
	for off := 0; off < len(data); off += 300000 {
		end := off + 300000
		if end > len(data) {
			end = len(data)
		}
		if _, err := w.Write(data[off:end]); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "srv", "in", "big.bin"))
	if !bytes.Equal(got, data) {
		t.Fatalf("sent %d bytes, the server has %d", len(data), len(got))
	}
	// Received back, streamed.
	rc, err := fs.Open("/srv/in/big.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	back, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(back, data) {
		t.Fatalf("received %d bytes (%v), want %d", len(back), err, len(data))
	}
	// An empty file both ways.
	w0, err := fs.CreateSized("/srv/in/empty", 0)
	if err != nil || w0.Close() != nil {
		t.Fatalf("empty file: %v", err)
	}
	rc0, err := fs.Open("/srv/in/empty")
	if err != nil {
		t.Fatalf("open empty: %v", err)
	}
	if b, err := io.ReadAll(rc0); err != nil || len(b) != 0 {
		t.Fatalf("empty file read: %q %v", b, err)
	}
	rc0.Close()
	// A missing file: "not found" for the explorer.
	if _, err := fs.Open("/srv/nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v, want not found", err)
	}
	// A missing folder for a new file: the sink's own words.
	if _, err := fs.CreateSized("/srv/missing/x.txt", 3); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing folder: %v", err)
	}
	// The size first: a plain Create is refused, the explorer's helper picks CreateSized.
	if _, err := fs.Create("/srv/in/x"); !errors.Is(err, errScpNeedSize) {
		t.Fatalf("Create: %v", err)
	}
	wc, err := explorerCreate(fs, "/srv/in/via.txt", 2)
	if err != nil {
		t.Fatal(err)
	}
	wc.Write([]byte("ok"))
	if err := wc.Close(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "srv", "in", "via.txt")); string(b) != "ok" {
		t.Fatalf("via explorerCreate: %q", b)
	}
}

func TestScpUploadStoppedEarly(t *testing.T) {
	root := t.TempDir()
	g := &goScpStarter{root: root, shell: true}
	fs := newScpFS(g)
	w, err := fs.CreateSized("/part.bin", 1000)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(make([]byte, 400))
	// Closed before its size: abandoned (an error), never acknowledged as whole.
	if err := w.Close(); !errors.Is(err, errScpSize) {
		t.Fatalf("early close: %v, want errScpSize", err)
	}
	// More than announced: refused before it is sent.
	w2, _ := fs.CreateSized("/over.bin", 2)
	if _, err := w2.Write([]byte("abc")); !errors.Is(err, errScpSize) {
		t.Fatalf("too much: %v", err)
	}
	w2.Close()
}

func TestScpMkdirWithoutShellUsesDRecords(t *testing.T) {
	root := t.TempDir()
	g := &goScpStarter{root: root, shell: false}
	fs := newScpFS(g)
	if fs.shell {
		t.Fatal("no shell expected")
	}
	if _, err := fs.ReadDir("/"); !errors.Is(err, errScpNoListing) {
		t.Fatalf("listing without a shell: %v", err)
	}
	// explorerMkdirAll walks up (Lstat fails: no shell), then D records, top down.
	if err := explorerMkdirAll(fs, "/a/b/c", 0); err != nil {
		t.Fatalf("mkdirAll: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "a", "b", "c")); err != nil || !fi.IsDir() {
		t.Fatalf("folders not made: %v", err)
	}
	// Again: existing folders are kept as they are.
	if err := explorerMkdirAll(fs, "/a/b/c", 0); err != nil {
		t.Fatalf("mkdirAll again: %v", err)
	}
	var withR int
	for _, c := range g.cmds {
		if strings.HasPrefix(c, "scp -r -t ") {
			withR++
		}
	}
	if withR < 3 {
		t.Fatalf("D records by scp -r -t: %d commands (%v)", withR, g.cmds)
	}
	// And a file in it, by SCP (the drop's path without a shell).
	st := newSftpStreams()
	res, handled, err := streamCmd(fs, st, "upload_open", []byte(`{"path":"/a/b/c/new/f.txt","size":5}`))
	if !handled || err != nil {
		t.Fatalf("upload_open: %v", err)
	}
	h := res.(map[string]string)["handle"]
	if _, _, err := streamCmd(fs, st, "upload_write", []byte(`{"handle":"`+h+`","offset":0,"data":"`+base64.StdEncoding.EncodeToString([]byte("hello"))+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := streamCmd(fs, st, "upload_close", []byte(`{"handle":"`+h+`"}`)); err != nil {
		t.Fatalf("upload_close: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a", "b", "c", "new", "f.txt")); string(b) != "hello" {
		t.Fatalf("file: %q", b)
	}
}

func TestScpReceiveTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "tree")
	os.MkdirAll(filepath.Join(src, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("BB"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "deep", "c.txt"), []byte("CCC"), 0o644)
	g := &goScpStarter{root: root}
	p, _ := g.Start("scp -f '/tree'")
	s, err := newScpSource(p)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	n, err := scpReceiveTree(s, dst)
	if err != nil || n != 3 {
		t.Fatalf("receive tree: %d files, %v", n, err)
	}
	for rel, want := range map[string]string{"tree/a.txt": "A", "tree/sub/b.txt": "BB", "tree/sub/deep/c.txt": "CCC"} {
		if b, _ := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel))); string(b) != want {
			t.Errorf("%s = %q, want %q", rel, b, want)
		}
	}
	// A hostile server's names never leave the folder (a Windows separator, a drive, a device).
	for _, bad := range []string{`..\evil.txt`, `C:evil.txt`, "CON", "a:b"} {
		src := &scpSource{r: bufio.NewReader(strings.NewReader("C0644 1 " + bad + "\nx\x00")), w: io.Discard}
		out := t.TempDir()
		if _, err := scpReceiveTree(src, out); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if ents, _ := os.ReadDir(out); len(ents) != 0 {
			t.Errorf("%q: something written", bad)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(out), "evil.txt")); err == nil {
			t.Errorf("%q: written beside the folder", bad)
		}
	}
}

func TestScpHeadersAndNames(t *testing.T) {
	for _, bad := range []string{"C0644 12", "Cxyz 1 a", "C0644 -1 a", "C0644 1 ../x", "C0644 1 a/b", "D0755 0 ..", "X", "C99999 1 a"} {
		if _, err := parseScpHeader(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	r, err := parseScpHeader("C0640 42 name with spaces.txt")
	if err != nil || r.Kind != 'C' || r.Size != 42 || r.Name != "name with spaces.txt" || r.Mode != 0o640 {
		t.Fatalf("header: %+v %v", r, err)
	}
	if shQuote("it's") != `'it'\''s'` {
		t.Fatalf("shQuote: %s", shQuote("it's"))
	}
	for _, c := range []struct{ in, want string }{
		{"scp: /x: No such file or directory", "not found"},
		{"rm: cannot remove '/x': Permission denied", "permission denied"},
		{"mkdir: cannot create directory '/x': File exists", "already exists"},
	} {
		if got := explorerCmdErrText(scpErrFrom(c.in, "")); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseScpListing(t *testing.T) {
	// GNU find -printf: any name (spaces, a newline), its mode and time.
	out := "F\n" + "d 4096 1700000000.5 755 dir one\x00" + "f 12 1700000100.0000000000 640 a.txt\x00" + "l 7 1700000200 777 link\x00" + "f 3 1700000300 644 new\nline\x00" + "p 0 1700000400 644 fifo\x00"
	infos, exists, err := parseScpListing([]byte(out))
	if err != nil || !exists || len(infos) != 5 {
		t.Fatalf("listing: %d entries, %v %v", len(infos), exists, err)
	}
	by := map[string]os.FileInfo{}
	for _, i := range infos {
		by[i.Name()] = i
	}
	if d := by["dir one"]; d == nil || !d.IsDir() || d.Mode().Perm() != 0o755 {
		t.Fatalf("dir: %+v", d)
	}
	if f := by["a.txt"]; f == nil || f.Size() != 12 || f.Mode().Perm() != 0o640 || f.ModTime().Unix() != 1700000100 {
		t.Fatalf("file: %+v", f)
	}
	if l := by["link"]; l == nil || l.Mode()&os.ModeSymlink == 0 || !strings.HasPrefix(l.Mode().String(), "L") {
		t.Fatalf("link: %+v", l)
	}
	if by["new\nline"] == nil || by["fifo"] == nil {
		t.Fatal("odd names / kinds kept")
	}
	// The shell's loop (busybox, BSD): no mode ("-").
	out = "L\n" + "d 0 1700000000 - etc\x00" + "f 99 1700000001 - x y\x00"
	infos, _, err = parseScpListing([]byte(out))
	if err != nil || len(infos) != 2 || !infos[0].IsDir() || infos[1].Name() != "x y" || infos[1].Size() != 99 {
		t.Fatalf("loop listing: %+v %v", infos, err)
	}
	// Not there; nonsense.
	if _, exists, err := parseScpListing([]byte("N\n")); exists || err != nil {
		t.Fatal("N: not there")
	}
	if _, _, err := parseScpListing([]byte("garbage")); err == nil {
		t.Fatal("garbage accepted")
	}
	// A name with a slash (impossible: refused, never joined into a path).
	infos, _, _ = parseScpListing([]byte("F\nf 1 1 644 a/b\x00"))
	if len(infos) != 0 {
		t.Fatal("a name with a slash kept")
	}
}

// ── End to end: a real SSH server without SFTP, a real shell and scp ───────

// gitShell: a POSIX sh with scp, find and stat (Git for Windows here), its
// bin folder, and how a test path is written for it.
func gitShell(t *testing.T) (sh, bin string, posix func(string) string) {
	t.Helper()
	cands := []string{}
	if p, err := exec.LookPath("sh"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, `C:\Program Files\Git\usr\bin\sh.exe`, `C:\Program Files\Git\bin\sh.exe`, "/bin/sh")
	for _, c := range cands {
		if _, err := os.Stat(c); err != nil {
			continue
		}
		bin = filepath.Dir(c)
		if filepath.Base(bin) == "bin" && filepath.Base(filepath.Dir(bin)) == "Git" {
			bin = filepath.Join(filepath.Dir(bin), "usr", "bin")
		}
		if _, err := os.Stat(filepath.Join(bin, "scp.exe")); err != nil {
			if _, err := os.Stat(filepath.Join(bin, "scp")); err != nil {
				continue
			}
		}
		sh = c
		break
	}
	if sh == "" {
		t.Skip("no POSIX shell with scp here")
	}
	// (as that shell writes it: Git's maps the temporary folder to /tmp)
	posix = func(p string) string {
		c := exec.Command(sh, "-c", "pwd")
		c.Dir = p
		out, err := c.Output()
		if err != nil {
			t.Skipf("the shell cannot say its folder: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	return sh, bin, posix
}

type shellSSHServer struct {
	addr      string
	sh, bin   string
	home      string
	shellOK   bool // false: only "scp ..." commands run (an scp-only account)
	mu        sync.Mutex
	commands  []string
	subsystem int // subsystem requests seen (refused)
}

func startShellSSHServer(t *testing.T, sh, bin, home string, shellOK bool) *shellSSHServer {
	t.Helper()
	hostSigner, _ := genEd25519Signer(t, 11)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if c.User() == "u" && string(pw) == "pw" {
			return nil, nil
		}
		return nil, errAuthFailed
	}}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &shellSSHServer{addr: ln.Addr().String(), sh: sh, bin: bin, home: home, shellOK: shellOK}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(nc, cfg)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *shellSSHServer) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, rq, err := nch.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, rq)
	}
}

func (s *shellSSHServer) session(ch ssh.Channel, rq <-chan *ssh.Request) {
	defer ch.Close()
	for req := range rq {
		switch req.Type {
		case "subsystem": // (no SFTP here)
			s.mu.Lock()
			s.subsystem++
			s.mu.Unlock()
			req.Reply(false, nil)
		case "exec":
			var p struct{ Cmd string }
			if ssh.Unmarshal(req.Payload, &p) != nil {
				req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.commands = append(s.commands, p.Cmd)
			s.mu.Unlock()
			req.Reply(true, nil)
			code := s.run(ch, p.Cmd)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *shellSSHServer) run(ch ssh.Channel, cmd string) int {
	if !s.shellOK && !strings.HasPrefix(cmd, "scp ") {
		io.WriteString(ch.Stderr(), "This account can only use scp.\n")
		return 1
	}
	c := exec.Command(s.sh, "-c", cmd)
	c.Dir = s.home
	c.Env = append(os.Environ(), "PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c.Stdout = ch
	c.Stderr = ch.Stderr()
	in, _ := c.StdinPipe()
	if err := c.Start(); err != nil {
		return 127
	}
	go func() { io.Copy(in, ch); in.Close() }()
	if err := c.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}

func dialScpFallback(t *testing.T, srv *shellSSHServer) *sftpSession {
	t.Helper()
	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, srv.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := sftpDial(ctx, sshDialParams{Host: host, Port: port, Username: "u", AuthMethod: vaultAuthPassword, Secret: "pw"}, store, true)
	if err != nil {
		t.Fatalf("sftpDial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func runCmd(t *testing.T, s *sftpSession, typ string, payload interface{}) map[string]interface{} {
	t.Helper()
	b, _ := json.Marshal(payload)
	return sftpRunExplorerCmd(s, typ, b)
}

func TestScpFallbackRealServer(t *testing.T) {
	sh, bin, posix := gitShell(t)
	home := t.TempDir()
	srv := startShellSSHServer(t, sh, bin, home, true)
	sess := dialScpFallback(t, srv)
	if sess.mode() != "scp" || sess.scp == nil || !sess.scp.shell {
		t.Fatalf("mode %s (shell %v): SCP with a shell expected", sess.mode(), sess.scp != nil && sess.scp.shell)
	}
	if srv.subsystem == 0 {
		t.Fatal("SFTP was never asked for")
	}
	base := posix(home)
	// The home (pwd), then the listing (an empty folder).
	out := runCmd(t, sess, "list_directory", map[string]string{"path": ""})
	if out["ok"] != true {
		t.Fatalf("list home: %v", out)
	}
	if p := out["result"].(explorerListing).Path; !strings.EqualFold(p, base) {
		t.Fatalf("home %q, want %q", p, base)
	}
	// A folder, a file in chunks (its size first), the listing, the file back.
	if out := runCmd(t, sess, "create_directory", map[string]string{"path": base + "/d ir"}); out["ok"] != true {
		t.Fatalf("mkdir: %v", out)
	}
	data := bytes.Repeat([]byte{0, 1, 2, 250, 'x', '\n', '\r'}, 300000) // 2.1 MB, binary
	open := runCmd(t, sess, "upload_open", map[string]interface{}{"path": base + "/d ir/up 1.bin", "size": len(data)})
	if open["ok"] != true {
		t.Fatalf("upload_open: %v", open)
	}
	h := open["result"].(map[string]string)["handle"]
	for off := 0; off < len(data); off += sftpStreamMaxChunk / 2 {
		end := off + sftpStreamMaxChunk/2
		if end > len(data) {
			end = len(data)
		}
		w := runCmd(t, sess, "upload_write", map[string]interface{}{"handle": h, "offset": off, "data": base64.StdEncoding.EncodeToString(data[off:end])})
		if w["ok"] != true {
			t.Fatalf("upload_write at %d: %v", off, w)
		}
	}
	if c := runCmd(t, sess, "upload_close", map[string]string{"handle": h}); c["ok"] != true {
		t.Fatalf("upload_close: %v", c)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "d ir", "up 1.bin")); !bytes.Equal(b, data) {
		t.Fatalf("on the server: %d bytes, want %d", len(b), len(data))
	}
	ls := runCmd(t, sess, "list_directory", map[string]string{"path": base + "/d ir"})
	if ls["ok"] != true {
		t.Fatalf("list: %v", ls)
	}
	files := ls["result"].(explorerListing).Files
	if len(files) != 1 || files[0].Name != "up 1.bin" || files[0].Size != int64(len(data)) || files[0].IsDir {
		t.Fatalf("listing: %+v", files)
	}
	d := runCmd(t, sess, "download_open", map[string]string{"path": base + "/d ir/up 1.bin"})
	if d["ok"] != true {
		t.Fatalf("download_open: %v", d)
	}
	dh := d["result"].(map[string]interface{})["handle"].(string)
	var back []byte
	for off := 0; off < len(data); off += sftpStreamMaxChunk / 2 {
		r := runCmd(t, sess, "download_read", map[string]interface{}{"handle": dh, "offset": off, "length": sftpStreamMaxChunk / 2})
		if r["ok"] != true {
			t.Fatalf("download_read: %v", r)
		}
		chunk, _ := base64.StdEncoding.DecodeString(r["result"].(map[string]interface{})["data"].(string))
		back = append(back, chunk...)
	}
	runCmd(t, sess, "download_close", map[string]string{"handle": dh})
	if !bytes.Equal(back, data) {
		t.Fatalf("received %d bytes, want %d", len(back), len(data))
	}
	// A whole small file (the explorer's single-message commands).
	if u := runCmd(t, sess, "upload_file", map[string]interface{}{"path": base + "/small.txt", "data": base64.StdEncoding.EncodeToString([]byte("hé"))}); u["ok"] != true {
		t.Fatalf("upload_file: %v", u)
	}
	if u := runCmd(t, sess, "upload_file", map[string]interface{}{"path": base + "/small.txt", "data": "eA=="}); u["ok"] == true || !strings.Contains(fmt.Sprint(u["error"]), "already exists") {
		t.Fatalf("no overwrite: %v", u)
	}
	df := runCmd(t, sess, "download_file", map[string]string{"path": base + "/small.txt"})
	if df["ok"] != true {
		t.Fatalf("download_file: %v", df)
	}
	// Rename, delete (a folder with contents: recursive only).
	if r := runCmd(t, sess, "rename_file", map[string]string{"oldPath": base + "/small.txt", "newPath": base + "/d ir/renamed.txt"}); r["ok"] != true {
		t.Fatalf("rename: %v", r)
	}
	if r := runCmd(t, sess, "delete_file", map[string]interface{}{"path": base + "/d ir"}); r["ok"] == true {
		t.Fatal("a folder with contents deleted without recursive")
	}
	if r := runCmd(t, sess, "delete_file", map[string]interface{}{"path": base + "/d ir", "recursive": true}); r["ok"] != true {
		t.Fatalf("recursive delete: %v", r)
	}
	if _, err := os.Stat(filepath.Join(home, "d ir")); !os.IsNotExist(err) {
		t.Fatal("folder still there")
	}
	// A transfer stopped halfway: its partial file removed (rm by the shell).
	o2 := runCmd(t, sess, "upload_open", map[string]interface{}{"path": base + "/half.bin", "size": 100000})
	h2 := o2["result"].(map[string]string)["handle"]
	runCmd(t, sess, "upload_write", map[string]interface{}{"handle": h2, "offset": 0, "data": base64.StdEncoding.EncodeToString(make([]byte, 50000))})
	if a := runCmd(t, sess, "upload_abort", map[string]string{"handle": h2}); a["ok"] != true {
		t.Fatalf("abort: %v", a)
	}
	eventuallyGone := func() bool { _, err := os.Stat(filepath.Join(home, "half.bin")); return os.IsNotExist(err) }
	eventually(t, "the partial file removed", eventuallyGone)
	// Missing: in words.
	if r := runCmd(t, sess, "list_directory", map[string]string{"path": base + "/nope"}); r["ok"] == true || r["error"] != "not found" {
		t.Fatalf("missing folder: %v", r)
	}
}

func TestScpFallbackScpOnlyAccount(t *testing.T) {
	sh, bin, posix := gitShell(t)
	home := t.TempDir()
	srv := startShellSSHServer(t, sh, bin, home, false)
	sess := dialScpFallback(t, srv)
	if sess.mode() != "scp" || sess.scp.shell {
		t.Fatal("SCP without a shell expected")
	}
	// Listing: refused, with its code (the page says it in its language).
	out := runCmd(t, sess, "list_directory", map[string]string{"path": ""})
	if out["ok"] == true || out["code"] != "scp_no_listing" {
		t.Fatalf("listing: %v", out)
	}
	// A file dropped into a folder that does not exist yet: sent all the same.
	base := posix(home)
	data := []byte("dropped by scp")
	open := runCmd(t, sess, "upload_open", map[string]interface{}{"path": base + "/new/sub/file.txt", "size": len(data)})
	if open["ok"] != true {
		t.Fatalf("upload_open: %v", open)
	}
	h := open["result"].(map[string]string)["handle"]
	runCmd(t, sess, "upload_write", map[string]interface{}{"handle": h, "offset": 0, "data": base64.StdEncoding.EncodeToString(data)})
	if c := runCmd(t, sess, "upload_close", map[string]string{"handle": h}); c["ok"] != true {
		t.Fatalf("upload_close: %v", c)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "new", "sub", "file.txt")); !bytes.Equal(b, data) {
		t.Fatalf("on the server: %q", b)
	}
	for _, c := range srv.commands {
		if !strings.HasPrefix(c, "scp ") && !strings.HasPrefix(c, "echo ") {
			t.Errorf("a shell command was sent to an scp-only account: %q", c)
		}
	}
}
