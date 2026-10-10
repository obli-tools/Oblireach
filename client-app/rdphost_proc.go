package main

// rdphost_proc.go — the local RDP helper process (rdphost, ../rdphost: a
// windowless FreeRDP 3 client that streams H.264), as the stream backend of
// a local RDP connection (rdphost_relay.go): launched, fed, read, stopped.
//
// The helper's protocol (protocol 1; the reference is the header of
// rdphost.c):
//   - stdout: frames [u8 type][u32 LE length][payload]: 'I' init (u32 w,
//     u32 h), 'F' one H.264 Annex B access unit, 'S' a JSON status, 'P' the
//     pointer, 'C' the remote clipboard's text;
//   - stdin: one JSON object per line; the FIRST is the connect line (host,
//     credentials, size...), then input, resize, clipboard, replies;
//   - stderr: its diagnostics, kept in a small ring here (never sent to the
//     page: it may name the host).
//
// Secrets: the credentials go on stdin only (never argv, the environment or
// a file); the line is wiped right after the write and never logged. The
// helper gets a minimal environment (no secret is ever in it either).
//
// Where the helper is (rdpHelperPath): OBLIREACH_RDPHOST when set (an
// absolute path: the developers' override), inside the Flatpak
// /app/libexec/oblireach/rdphost (the manifest's rdphost module) or
// /app/libexec/oblirdp, else next to this executable (rdphost, or
// rdphost.exe on Windows, where the helper path is not used yet: mstsc.exe
// is). Its protocol is checked once (rdphost --version: "protocol=1").

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// The helper protocol this adapter speaks.
	rdpHelperProtocol = 1
	// The largest frame accepted from the helper (a 8K key frame is far
	// smaller; a bigger length means a broken stream).
	rdpHelperMaxFrame = 32 << 20
	// The largest line written to the helper (its own limit: 4 MiB).
	rdpHelperMaxLine = 4 << 20
	// How long a disconnect may take before the helper is killed.
	rdpHelperStopWait = 3 * time.Second
	// stderr lines kept (diagnostics).
	rdpHelperTailLines = 64
)

var (
	errRdpHelperMissing  = errors.New("rdp: the RDP helper (rdphost) is not installed")
	errRdpHelperProtocol = errors.New("rdp: the RDP helper speaks another protocol")
	errRdpHelperClosed   = errors.New("rdp: the RDP helper has ended")
)

// rdpHelperOverride: tests point the adapter at a fake helper (the test
// binary itself); "" = rdpHelperPath's search.
var rdpHelperOverride string

// rdpHelperCandidates: where the helper may be, in order.
func rdpHelperCandidates() []string {
	var out []string
	if p := os.Getenv("OBLIREACH_RDPHOST"); p != "" && filepath.IsAbs(p) {
		out = append(out, p)
	}
	if runtime.GOOS == "linux" {
		out = append(out, "/app/libexec/oblireach/rdphost", "/app/libexec/oblirdp")
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		name := "rdphost"
		if runtime.GOOS == "windows" {
			name = "rdphost.exe"
		}
		out = append(out, filepath.Join(dir, name), filepath.Join(dir, "libexec", name))
	}
	return out
}

// rdpHelperPath: the helper to run (errRdpHelperMissing when none is there).
func rdpHelperPath() (string, error) {
	if rdpHelperOverride != "" {
		return rdpHelperOverride, nil
	}
	for _, p := range rdpHelperCandidates() {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && (runtime.GOOS == "windows" || st.Mode().Perm()&0o111 != 0) {
			return p, nil
		}
	}
	return "", errRdpHelperMissing
}

// rdpHelperChecked: the helpers whose protocol was checked (path -> error).
var rdpHelperChecked sync.Map

// rdpHelperVersion: the protocol of the helper at path, from its --version
// line ("rdphost protocol=1 freerdp=3.x x264=164").
func rdpHelperVersion(path string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = rdpHelperEnv()
	rdpHelperProcAttr(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("rdp: the RDP helper does not start: %w", err)
	}
	return parseRdpHelperVersion(string(out))
}

func parseRdpHelperVersion(s string) (int, error) {
	for _, f := range strings.Fields(s) {
		if v, ok := strings.CutPrefix(f, "protocol="); ok {
			n := 0
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
				return n, nil
			}
		}
	}
	return 0, errRdpHelperProtocol
}

// rdpHelperCheck: the helper at path speaks rdpHelperProtocol (checked once
// per path and run of the app).
func rdpHelperCheck(path string) error {
	if v, ok := rdpHelperChecked.Load(path); ok {
		if v == nil {
			return nil
		}
		return v.(error)
	}
	n, err := rdpHelperVersion(path)
	if err == nil && n != rdpHelperProtocol {
		err = fmt.Errorf("%w (%d, %d expected)", errRdpHelperProtocol, n, rdpHelperProtocol)
	}
	if err != nil {
		rdpHelperChecked.Store(path, err)
		return err
	}
	rdpHelperChecked.Store(path, nil)
	return nil
}

// rdpHelperEnv: the helper's environment: what FreeRDP needs to find the
// user's folders, the sound server and Kerberos, nothing else.
func rdpHelperEnv() []string {
	keep := []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR",
		"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
		"PULSE_SERVER", "PULSE_COOKIE", "KRB5_CONFIG", "KRB5CCNAME", "FLATPAK_ID",
		"SYSTEMROOT", "WINDIR", "PATH"} // (Windows: the helper's DLLs)
	env := []string{"WLOG_LEVEL=WARN"}
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, rdpHelperTestEnv...)
}

// rdpHelperTestEnv: what the tests' fake helper is told (nil in the app).
var rdpHelperTestEnv []string

// stderrTail keeps the last lines a helper wrote on stderr.
type stderrTail struct {
	mu    sync.Mutex
	lines []string
}

func (t *stderrTail) consume(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		t.mu.Lock()
		t.lines = append(t.lines, sc.Text())
		if len(t.lines) > rdpHelperTailLines {
			t.lines = t.lines[len(t.lines)-rdpHelperTailLines:]
		}
		t.mu.Unlock()
	}
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// rdpHelper is one running helper.
type rdpHelper struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
	tail  *stderrTail
	inMu  sync.Mutex // one line at a time
	done  chan struct{}
	exit  error // after done
	once  sync.Once
}

// startRdpHelper launches the helper at path (no argument: it waits for its
// connect line).
func startRdpHelper(path string) (*rdpHelper, error) {
	cmd := exec.Command(path)
	cmd.Env = rdpHelperEnv()
	if d := os.TempDir(); d != "" {
		cmd.Dir = d
	}
	rdpHelperProcAttr(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("rdp: the RDP helper does not start: %w", err)
	}
	h := &rdpHelper{cmd: cmd, stdin: stdin, out: bufio.NewReaderSize(stdout, 256<<10), tail: &stderrTail{}, done: make(chan struct{})}
	go h.tail.consume(stderr)
	go func() {
		h.exit = cmd.Wait()
		close(h.done)
	}()
	return h, nil
}

// wipe zeroes b (a line that held credentials).
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// writeLine sends one JSON object as a line, then wipes what it marshalled
// (the connect and credentials lines hold secrets). Never logged.
func (h *rdpHelper) writeLine(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	defer wipe(b)
	return h.writeRaw(b)
}

// writeRaw sends one line already marshalled (no newline in it).
func (h *rdpHelper) writeRaw(line []byte) error {
	if len(line)+1 > rdpHelperMaxLine || bytes.IndexByte(line, '\n') >= 0 {
		return errors.New("rdp: line refused")
	}
	h.inMu.Lock()
	defer h.inMu.Unlock()
	select {
	case <-h.done:
		return errRdpHelperClosed
	default:
	}
	buf := make([]byte, len(line)+1)
	copy(buf, line)
	buf[len(line)] = '\n'
	_, err := h.stdin.Write(buf)
	wipe(buf)
	return err
}

// readFrame: the next frame of the helper's stdout.
func (h *rdpHelper) readFrame() (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(h.out, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[1:])
	if n > rdpHelperMaxFrame {
		return 0, nil, fmt.Errorf("rdp: frame of %d bytes refused", n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(h.out, p); err != nil {
		return 0, nil, err
	}
	return hdr[0], p, nil
}

// stop: a disconnect, then the helper's own end (rdpHelperStopWait), else it
// is killed.
func (h *rdpHelper) stop() {
	h.once.Do(func() {
		_ = h.writeLine(map[string]string{"type": "disconnect"})
		select {
		case <-h.done:
		case <-time.After(rdpHelperStopWait):
			_ = h.cmd.Process.Kill()
			<-h.done
		}
		_ = h.stdin.Close()
	})
}

// kill ends the helper at once (the app quits, a broken stream).
func (h *rdpHelper) kill() {
	select {
	case <-h.done:
	default:
		_ = h.cmd.Process.Kill()
	}
}

// exited: the helper process has ended.
func (h *rdpHelper) exited() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}
