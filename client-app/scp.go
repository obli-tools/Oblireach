package main

// scp.go — files of a LOCAL SSH connection whose server has no SFTP
// subsystem (a router's dropbear, a busybox appliance, an sshd whose
// sftp-server is missing or disabled): sftpDial falls back to this, so the
// explorer, the Files page's split view and the files dropped on a terminal
// keep working with the same commands (localconn_explorer.go's vocabulary,
// sftp_stream.go's chunks) over the SAME SSH connection.
//
//   - The data goes by the SCP protocol, spoken here in Go against the
//     server's own `scp -t` (a file sent: "C<mode> <size> <name>", its bytes,
//     a 0, each step acknowledged) and `scp -f` (a file received), streamed:
//     never a whole file in memory, its size announced first (the page sends
//     it with upload_open). `-r` (D / E records) creates folders when no
//     shell answers, and a tree is received with its folders
//     (scpReceiveTree). A transfer stopped (cancelled, failed, the session
//     gone) closes its channel and removes the partial file.
//   - The rest (listing a folder, a file's size and kind, mkdir, mv, rm,
//     rmdir, the home folder) is asked of the server's shell on a channel of
//     its own (exec, no PTY: never seen in a terminal), in plain POSIX sh:
//     GNU find's -printf when there is one (any name, NUL separated), else a
//     loop of test / stat (busybox, BSD). Every path is single-quoted.
//   - No shell at all (an scp-only account): folders cannot be listed: the
//     explorer says so in words (errScpNoListing); files dropped on its
//     terminal still go (the folder asked, then scp -t; missing folders made
//     with D records).
//
// Nothing here ever carries a secret: the connection is already signed in.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	scpExecTimeout = 30 * time.Second
	scpExecOutMax  = 32 << 20 // a listing's output, at most
	scpStderrMax   = 4096
	scpMaxLine     = 8192 // an SCP record line
	scpProbeWord   = "oblireach-scp-ok"
)

var (
	errScpNoListing = errors.New("this server has neither SFTP nor a shell: its folders cannot be listed")
	errScpMissing   = errors.New("this server has no SFTP and no scp command")
	errScpNeedSize  = errors.New("scp: the file's size is needed first")
	errScpSize      = errors.New("scp: the file's size changed during the transfer")
	errScpProtocol  = errors.New("scp: unexpected answer from the server")
)

// ── Running a command on the connection ─────────────────────────────────────

// scpProc: a command running on the server (an SSH session channel; a pipe to
// a Go peer in tests).
type scpProc interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() string // what it said on stderr (bounded)
	Wait() error
	Close() error
}

// scpStarter starts commands on the connection.
type scpStarter interface {
	Start(cmd string) (scpProc, error)
}

// sshScpStarter: the real one, on an *ssh.Client.
type sshScpStarter struct{ c *ssh.Client }

type boundedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *boundedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type sshScpProc struct {
	s      *ssh.Session
	in     io.WriteCloser
	out    io.Reader
	errBuf *boundedBuf
}

func (p *sshScpProc) Stdin() io.WriteCloser { return p.in }
func (p *sshScpProc) Stdout() io.Reader     { return p.out }
func (p *sshScpProc) Stderr() string        { return p.errBuf.String() }
func (p *sshScpProc) Wait() error           { return p.s.Wait() }
func (p *sshScpProc) Close() error          { return p.s.Close() }

func (r sshScpStarter) Start(cmd string) (scpProc, error) {
	s, err := r.c.NewSession()
	if err != nil {
		return nil, err
	}
	in, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	out, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	eb := &boundedBuf{max: scpStderrMax}
	s.Stderr = eb
	if err := s.Start(cmd); err != nil {
		s.Close()
		return nil, err
	}
	return &sshScpProc{s: s, in: in, out: out, errBuf: eb}, nil
}

// shQuote: one argument for a POSIX shell, single-quoted (a quote inside
// closes the quoting, is escaped, and opens it again).
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// scpExecResult: a command's output, its stderr, its exit status (-1: it
// did not end normally).
type scpExecResult struct {
	out    []byte
	stderr string
	status int
}

// scpExec runs cmd to its end (timeout: the channel is closed).
func scpExec(st scpStarter, cmd string, timeout time.Duration) (scpExecResult, error) {
	p, err := st.Start(cmd)
	if err != nil {
		return scpExecResult{status: -1}, err
	}
	_ = p.Stdin().Close()
	type res struct {
		out []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, rerr := io.ReadAll(io.LimitReader(p.Stdout(), scpExecOutMax+1))
		if rerr == nil && len(out) > scpExecOutMax {
			rerr = errors.New("scp: answer too large")
		}
		werr := p.Wait()
		if rerr == nil {
			rerr = werr
		}
		ch <- res{out, rerr}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		_ = p.Close()
		status := 0
		if r.err != nil {
			var ee *ssh.ExitError
			if errors.As(r.err, &ee) {
				status = ee.ExitStatus()
			} else if se, ok := r.err.(interface{ ExitStatus() int }); ok {
				status = se.ExitStatus()
			} else {
				return scpExecResult{out: r.out, stderr: p.Stderr(), status: -1}, r.err
			}
		}
		return scpExecResult{out: r.out, stderr: p.Stderr(), status: status}, nil
	case <-t.C:
		_ = p.Close()
		return scpExecResult{status: -1}, errors.New("ssh: command timed out")
	}
}

// scpErrFrom: a failed command's words as an error the explorer understands
// (not found, permission, exists), else its first line, bounded.
func scpErrFrom(stderr string, fallback string) error {
	s := strings.TrimSpace(stderr)
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "no such file"), strings.Contains(low, "not found") && !strings.Contains(low, "command not found"):
		return &os.PathError{Op: "scp", Path: "", Err: os.ErrNotExist}
	case strings.Contains(low, "permission denied"), strings.Contains(low, "operation not permitted"):
		return &os.PathError{Op: "scp", Path: "", Err: os.ErrPermission}
	case strings.Contains(low, "file exists"):
		return errExplorerExists
	case strings.Contains(low, "directory not empty"):
		return errExplorerNotEmpty
	case strings.Contains(low, "scp: not found"), strings.Contains(low, "scp: command not found"), strings.Contains(low, "scp: no such"):
		return errScpMissing
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "scp: "), "sh: ")
	if s == "" {
		s = fallback
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return errors.New(s)
}

// ── The SCP protocol ────────────────────────────────────────────────────────

// scpReadAck: the peer's answer to a step: 0 (ok), 1 (a warning: an error
// for this file) or 2 (fatal), the latter two with a message line.
func scpReadAck(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return err
	}
	if b == 0 {
		return nil
	}
	if b != 1 && b != 2 {
		return errScpProtocol
	}
	line, _ := scpReadLine(r)
	return scpErrFrom(line, "scp failed")
}

func scpReadLine(r *bufio.Reader) (string, error) {
	var b []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			return string(b), err
		}
		if c == '\n' {
			return string(b), nil
		}
		if len(b) >= scpMaxLine {
			return string(b), errScpProtocol
		}
		b = append(b, c)
	}
}

// scpNameOK: a name an SCP record may carry (one element, no line break).
func scpNameOK(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, "/\n\r\x00")
}

// scpRecord: a record of a source (`scp -f`): a file (C: its body follows),
// a folder entered (D) or left (E).
type scpRecord struct {
	Kind byte // 'C', 'D', 'E'
	Mode os.FileMode
	Size int64
	Name string
}

// parseScpHeader: "C0644 12 name" / "D0755 0 name" (the leading letter included).
func parseScpHeader(line string) (scpRecord, error) {
	if line == "E" {
		return scpRecord{Kind: 'E'}, nil
	}
	if len(line) < 2 {
		return scpRecord{}, errScpProtocol
	}
	k := line[0]
	if k != 'C' && k != 'D' {
		return scpRecord{}, errScpProtocol
	}
	parts := strings.SplitN(line[1:], " ", 3)
	if len(parts) != 3 {
		return scpRecord{}, errScpProtocol
	}
	mode, err := strconv.ParseUint(parts[0], 8, 32)
	if err != nil || mode > 07777 {
		return scpRecord{}, errScpProtocol
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || size < 0 {
		return scpRecord{}, errScpProtocol
	}
	if !scpNameOK(parts[2]) {
		return scpRecord{}, errScpProtocol
	}
	return scpRecord{Kind: k, Mode: os.FileMode(mode) & os.ModePerm, Size: size, Name: parts[2]}, nil
}

// scpSource reads what a `scp -f` sends, record by record.
type scpSource struct {
	p    scpProc
	r    *bufio.Reader
	w    io.Writer
	body *io.LimitedReader // the file in progress
}

func newScpSource(p scpProc) (*scpSource, error) {
	s := &scpSource{p: p, r: bufio.NewReaderSize(p.Stdout(), 64*1024), w: p.Stdin()}
	if _, err := s.w.Write([]byte{0}); err != nil { // (ready)
		return nil, err
	}
	return s, nil
}

// next: the next record (T times are acknowledged and skipped); io.EOF at the end.
func (s *scpSource) next() (scpRecord, error) {
	if s.body != nil {
		return scpRecord{}, errScpProtocol // (the file's body not read yet)
	}
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return scpRecord{}, io.EOF
			}
			return scpRecord{}, err
		}
		if c == 1 || c == 2 {
			line, _ := scpReadLine(s.r)
			return scpRecord{}, scpErrFrom(line, "scp failed")
		}
		_ = s.r.UnreadByte()
		line, err := scpReadLine(s.r)
		if err != nil {
			return scpRecord{}, errScpProtocol
		}
		if strings.HasPrefix(line, "T") {
			if _, err := s.w.Write([]byte{0}); err != nil {
				return scpRecord{}, err
			}
			continue
		}
		rec, err := parseScpHeader(line)
		if err != nil {
			return scpRecord{}, err
		}
		if _, err := s.w.Write([]byte{0}); err != nil {
			return scpRecord{}, err
		}
		if rec.Kind == 'C' {
			s.body = &io.LimitedReader{R: s.r, N: rec.Size}
		}
		return rec, nil
	}
}

// fileDone: the file's body read to its end: its status, acknowledged.
func (s *scpSource) fileDone() error {
	if s.body == nil || s.body.N != 0 {
		return errScpProtocol
	}
	s.body = nil
	if err := scpReadAck(s.r); err != nil {
		return err
	}
	_, err := s.w.Write([]byte{0})
	return err
}

// scpSink writes to a `scp -t` (folders with -r).
type scpSink struct {
	p scpProc
	r *bufio.Reader
	w io.WriteCloser
}

func newScpSink(p scpProc) (*scpSink, error) {
	s := &scpSink{p: p, r: bufio.NewReader(p.Stdout()), w: p.Stdin()}
	if err := scpReadAck(s.r); err != nil {
		return nil, s.explain(err)
	}
	return s, nil
}

// explain: an early end of the command says why on stderr (scp missing...).
func (s *scpSink) explain(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		if e := strings.TrimSpace(s.p.Stderr()); e != "" {
			low := strings.ToLower(e)
			if strings.Contains(low, "not found") && strings.Contains(low, "scp") {
				return errScpMissing
			}
			return scpErrFrom(e, "scp failed")
		}
		return errScpMissing
	}
	return err
}

func (s *scpSink) record(line string) error {
	if _, err := io.WriteString(s.w, line+"\n"); err != nil {
		return s.explain(err)
	}
	if err := scpReadAck(s.r); err != nil {
		return s.explain(err)
	}
	return nil
}

func (s *scpSink) dir(name string, mode os.FileMode) error {
	if !scpNameOK(name) {
		return errExplorerBadPath
	}
	return s.record(fmt.Sprintf("D%04o 0 %s", mode&os.ModePerm, name))
}

func (s *scpSink) endDir() error { return s.record("E") }

// fileBegin: a file's record; its body is written next (exactly size bytes),
// then fileEnd.
func (s *scpSink) fileBegin(name string, mode os.FileMode, size int64) error {
	if !scpNameOK(name) {
		return errExplorerBadPath
	}
	return s.record(fmt.Sprintf("C%04o %d %s", mode&os.ModePerm, size, name))
}

func (s *scpSink) fileEnd() error {
	if _, err := s.w.Write([]byte{0}); err != nil {
		return s.explain(err)
	}
	if err := scpReadAck(s.r); err != nil {
		return s.explain(err)
	}
	return nil
}

// finish: the end of the transfer (the sink exits on its input's end).
func (s *scpSink) finish(timeout time.Duration) error {
	_ = s.w.Close()
	done := make(chan error, 1)
	go func() { done <- s.p.Wait() }()
	select {
	case err := <-done:
		_ = s.p.Close()
		// (every step was acknowledged: only a real failure status counts,
		// not a server that sends no status at all)
		var ee *ssh.ExitError
		if errors.As(err, &ee) && ee.ExitStatus() != 0 {
			return scpErrFrom(s.p.Stderr(), "scp failed")
		}
		if se, ok := err.(interface{ ExitStatus() int }); ok && se.ExitStatus() != 0 {
			return scpErrFrom(s.p.Stderr(), "scp failed")
		}
		return nil
	case <-time.After(timeout):
		_ = s.p.Close()
		return nil // (everything was acknowledged: the file is there)
	}
}

// scpReceiveTree: everything a `scp -rf` sends, under localDir (its folders
// made, its files written). Names are checked (one element each): a hostile
// server can never write outside localDir.
func scpReceiveTree(src *scpSource, localDir string) (files int, err error) {
	stack := []string{localDir}
	for {
		rec, err := src.next()
		if errors.Is(err, io.EOF) {
			if len(stack) != 1 {
				return files, errScpProtocol
			}
			return files, nil
		}
		if err != nil {
			return files, err
		}
		cur := stack[len(stack)-1]
		// (a name this computer takes as one element: never "..\x", "C:x", a device)
		if rec.Kind != 'E' && !portableNameOK(rec.Name) {
			return files, errScpProtocol
		}
		switch rec.Kind {
		case 'D':
			if len(stack) > 64 {
				return files, errScpProtocol
			}
			d := cur + string(os.PathSeparator) + rec.Name
			if err := os.Mkdir(d, 0o755); err != nil && !os.IsExist(err) {
				return files, err
			}
			stack = append(stack, d)
		case 'E':
			if len(stack) == 1 {
				return files, errScpProtocol
			}
			stack = stack[:len(stack)-1]
		case 'C':
			f, err := os.OpenFile(cur+string(os.PathSeparator)+rec.Name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return files, err
			}
			_, cerr := io.Copy(f, src.body)
			if e := f.Close(); cerr == nil {
				cerr = e
			}
			if cerr != nil {
				return files, cerr
			}
			if err := src.fileDone(); err != nil {
				return files, err
			}
			files++
		}
	}
}

// ── The explorer's file system over SCP + the shell ────────────────────────

// scpFS: localconn_explorer.go's explorerFS without SFTP.
type scpFS struct {
	st    scpStarter
	shell bool // a POSIX shell answers (listing, sizes, mkdir, mv, rm)
}

// newScpFS probes the shell once (echo): without it, listing is off.
func newScpFS(st scpStarter) *scpFS {
	f := &scpFS{st: st}
	if r, err := scpExec(st, "echo "+scpProbeWord, 10*time.Second); err == nil && r.status == 0 && strings.TrimSpace(string(r.out)) == scpProbeWord {
		f.shell = true
	}
	return f
}

// run: a shell command; its stdout, or its failure in words.
func (f *scpFS) run(cmd string) ([]byte, error) {
	if !f.shell {
		return nil, errScpNoListing
	}
	r, err := scpExec(f.st, cmd, scpExecTimeout)
	if err != nil {
		return nil, err
	}
	if r.status != 0 {
		return nil, scpErrFrom(r.stderr, "command failed")
	}
	return r.out, nil
}

func (f *scpFS) RealPath(p string) (string, error) {
	cmd := "pwd -P 2>/dev/null || pwd"
	if p != "." && p != "" {
		cmd = "cd " + shQuote(p) + " && { pwd -P 2>/dev/null || pwd; }"
	}
	out, err := f.run(cmd)
	if err != nil {
		return "", err
	}
	s := strings.TrimRight(string(out), "\r\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if !strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\x00\r") {
		return "", errExplorerBadPath
	}
	return s, nil
}

// scpListScript: the entries of a folder (one=false) or the entry itself
// (one=true), as "<F|L>\n" then records "<type> <size> <mtime> <mode> <name>\0"
// (type: d, l, f; F: GNU find -printf, L: the shell's loop, its mode "-").
// "N\n" when the path does not exist.
func scpListScript(p string, one bool) string {
	q := shQuote(p)
	if one {
		return "f=" + q + "\n" +
			`[ -e "$f" ] || [ -L "$f" ] || { printf 'N\n'; exit 0; }
if find "$f" -maxdepth 0 -printf '' >/dev/null 2>&1; then printf 'F\n'; exec find "$f" -maxdepth 0 -printf '%y %s %T@ %m %f\0'; fi
printf 'L\n'
if [ -L "$f" ]; then t=l; elif [ -d "$f" ]; then t=d; else t=f; fi
s=$(stat -c '%s %Y' "$f" 2>/dev/null || stat -f '%z %m' "$f" 2>/dev/null || echo '0 0')
printf '%s %s - %s\0' "$t" "$s" "${f##*/}"`
	}
	return "d=" + q + "\n" +
		`[ -d "$d" ] || { [ -e "$d" ] && { echo "Not a directory" >&2; exit 2; }; echo "No such file or directory" >&2; exit 2; }
[ -r "$d" ] && [ -x "$d" ] || { echo "Permission denied" >&2; exit 2; }
if find "$d" -maxdepth 0 -printf '' >/dev/null 2>&1; then printf 'F\n'; exec find "$d" -mindepth 1 -maxdepth 1 -printf '%y %s %T@ %m %f\0' 2>/dev/null; fi
printf 'L\n'
for f in "$d"/* "$d"/.[!.]* "$d"/..?*; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  if [ -L "$f" ]; then t=l; elif [ -d "$f" ]; then t=d; else t=f; fi
  s=$(stat -c '%s %Y' "$f" 2>/dev/null || stat -f '%z %m' "$f" 2>/dev/null || echo '0 0')
  printf '%s %s - %s\0' "$t" "$s" "${f##*/}"
done`
}

// scpFileInfo: an entry of a listing as an os.FileInfo.
type scpFileInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
}

func (i scpFileInfo) Name() string       { return i.name }
func (i scpFileInfo) Size() int64        { return i.size }
func (i scpFileInfo) Mode() os.FileMode  { return i.mode }
func (i scpFileInfo) ModTime() time.Time { return i.mtime }
func (i scpFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i scpFileInfo) Sys() interface{}   { return nil }

// parseScpListing reads scpListScript's output (exists false: "N").
func parseScpListing(out []byte) (infos []os.FileInfo, exists bool, err error) {
	i := bytes.IndexByte(out, '\n')
	if i < 0 {
		return nil, false, errScpProtocol
	}
	kind := string(bytes.TrimSpace(out[:i]))
	if kind == "N" {
		return nil, false, nil
	}
	if kind != "F" && kind != "L" {
		return nil, false, errScpProtocol
	}
	for _, rec := range bytes.Split(out[i+1:], []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		// "<t> <size> <mtime> <mode> <name>": the name is the rest (spaces kept).
		f := strings.SplitN(string(rec), " ", 5)
		if len(f) != 5 || f[4] == "" || strings.ContainsAny(f[4], "/\x00") {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		if size < 0 {
			size = 0
		}
		var mt time.Time
		if sec, err := strconv.ParseFloat(f[2], 64); err == nil && sec > 0 {
			mt = time.Unix(int64(sec), int64((sec-float64(int64(sec)))*1e9)).UTC()
		}
		perm := os.FileMode(0o644)
		if f[3] != "-" {
			if m, err := strconv.ParseUint(f[3], 8, 32); err == nil {
				perm = os.FileMode(m) & os.ModePerm
			}
		} else if f[0] == "d" {
			perm = 0o755
		}
		var mode os.FileMode
		switch f[0] {
		case "d":
			mode = os.ModeDir | perm
		case "l":
			mode = os.ModeSymlink | perm
		case "f":
			mode = perm
		default: // (a socket, a pipe, a device: listed as a file)
			mode = os.ModeIrregular | perm
		}
		infos = append(infos, scpFileInfo{name: f[4], size: size, mode: mode, mtime: mt})
	}
	sort.SliceStable(infos, func(a, b int) bool { return infos[a].Name() < infos[b].Name() })
	return infos, true, nil
}

func (f *scpFS) ReadDir(dir string) ([]os.FileInfo, error) {
	out, err := f.run(scpListScript(dir, false))
	if err != nil {
		return nil, err
	}
	infos, exists, err := parseScpListing(out)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, &os.PathError{Op: "list", Path: dir, Err: os.ErrNotExist}
	}
	return infos, nil
}

func (f *scpFS) Lstat(p string) (os.FileInfo, error) {
	out, err := f.run(scpListScript(p, true))
	if err != nil {
		return nil, err
	}
	infos, exists, err := parseScpListing(out)
	if err != nil {
		return nil, err
	}
	if !exists || len(infos) != 1 {
		return nil, &os.PathError{Op: "lstat", Path: p, Err: os.ErrNotExist}
	}
	return infos[0], nil
}

// Mkdir: mkdir by the shell, else a D record of `scp -r -t` in its parent
// (an existing folder of that name is kept as it is by the sink).
func (f *scpFS) Mkdir(p string) error {
	if f.shell {
		_, err := f.run("mkdir " + shQuote(p))
		return err
	}
	parent, name := path.Dir(p), path.Base(p)
	if !scpNameOK(name) {
		return errExplorerBadPath
	}
	proc, err := f.st.Start("scp -r -t " + shQuote(parent))
	if err != nil {
		return err
	}
	s, err := newScpSink(proc)
	if err != nil {
		_ = proc.Close()
		return err
	}
	if err := s.dir(name, 0o755); err != nil {
		_ = proc.Close()
		return err
	}
	if err := s.endDir(); err != nil {
		_ = proc.Close()
		return err
	}
	return s.finish(10 * time.Second)
}

func (f *scpFS) Rename(o, n string) error {
	_, err := f.run("mv " + shQuote(o) + " " + shQuote(n))
	return err
}

func (f *scpFS) Remove(p string) error {
	_, err := f.run("rm -f " + shQuote(p))
	return err
}

func (f *scpFS) RemoveDirectory(p string) error {
	_, err := f.run("rmdir " + shQuote(p))
	return err
}

// Open: the file by `scp -f` (its body streamed as it is read).
func (f *scpFS) Open(p string) (io.ReadCloser, error) {
	proc, err := f.st.Start("scp -f " + shQuote(p))
	if err != nil {
		return nil, err
	}
	src, err := newScpSource(proc)
	if err != nil {
		_ = proc.Close()
		return nil, err
	}
	rec, err := src.next()
	if err != nil {
		_ = proc.Close()
		if errors.Is(err, io.EOF) {
			return nil, (&scpSink{p: proc}).explain(io.EOF)
		}
		return nil, err
	}
	if rec.Kind != 'C' {
		_ = proc.Close()
		return nil, errExplorerBadPath
	}
	return &scpReader{p: proc, src: src, size: rec.Size}, nil
}

// scpReader: a file being received.
type scpReader struct {
	p    scpProc
	src  *scpSource
	size int64
	done bool
	err  error
}

func (r *scpReader) Read(b []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	n, err := r.src.body.Read(b)
	if r.src.body.N == 0 {
		if e := r.src.fileDone(); e != nil {
			r.err = e
			return n, e
		}
		r.done = true
		_ = r.p.Stdin().Close()
		if n > 0 {
			return n, nil
		}
		return 0, io.EOF
	}
	if errors.Is(err, io.EOF) {
		r.err = io.ErrUnexpectedEOF
		return n, r.err
	}
	return n, err
}

func (r *scpReader) Close() error {
	_ = r.p.Close()
	return nil
}

// Create: SCP announces a file's size first: CreateSized.
func (f *scpFS) Create(string) (io.WriteCloser, error) { return nil, errScpNeedSize }

// CreateSized: the file by `scp -t` (its parent must exist), size bytes to
// write, then Close. A Close before them aborts: the channel ends (the
// caller removes the partial file).
func (f *scpFS) CreateSized(p string, size int64) (io.WriteCloser, error) {
	if size < 0 {
		return nil, errScpNeedSize
	}
	name := path.Base(p)
	if !scpNameOK(name) {
		return nil, errExplorerBadPath
	}
	proc, err := f.st.Start("scp -t " + shQuote(p))
	if err != nil {
		return nil, err
	}
	s, err := newScpSink(proc)
	if err != nil {
		_ = proc.Close()
		return nil, err
	}
	if err := s.fileBegin(name, 0o644, size); err != nil {
		_ = proc.Close()
		return nil, err
	}
	return &scpWriter{s: s, left: size}, nil
}

type scpWriter struct {
	s      *scpSink
	left   int64
	closed bool
	err    error
}

func (w *scpWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if int64(len(b)) > w.left {
		w.err = errScpSize
		_ = w.s.p.Close()
		return 0, w.err
	}
	n, err := w.s.w.Write(b)
	w.left -= int64(n)
	if err != nil {
		w.err = w.s.explain(err)
		_ = w.s.p.Close()
		return n, w.err
	}
	return n, nil
}

func (w *scpWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	if w.left != 0 { // (stopped before its end: the transfer is abandoned)
		_ = w.s.p.Close()
		w.err = errScpSize
		return w.err
	}
	if err := w.s.fileEnd(); err != nil {
		_ = w.s.p.Close()
		w.err = err
		return err
	}
	w.err = w.s.finish(10 * time.Second)
	return w.err
}

// explorerSizedFS: a file system whose new files need their size first (SCP).
type explorerSizedFS interface {
	CreateSized(p string, size int64) (io.WriteCloser, error)
}
