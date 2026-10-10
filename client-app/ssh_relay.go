package main

// ssh_relay.go — the terminal of a LOCAL SSH connection (localconn_tools.go,
// the vault's ssh entries) speaks the SAME protocol as an Obliance agent's
// terminal, so its tab is the same tab with the same code paths: detached
// windows, tear-off and merge, park and take-over (term_park.go), split,
// broadcast, drop of files, reconnect.
//
// An agent's terminal is a WebSocket from the page to this proxy, relayed to
// the server's /api/remote/tunnel/<token>: text frames {type:'paired' |
// 'waiting' | 'error'} from the relay, binary frames of the PTY's output;
// keystrokes as binary frames, {type:'resize'} as text. A local connection
// has no server: the relay is HERE. The page opens /local/ssh-tunnel/<token>
// (a token reachSshConnect handed it once the connection was dialed: host
// key checked, signed in with the vault's secret, which never reaches the
// page), and the proxy runs that tunnel through termParks.run exactly like
// an agent's, its "upstream" being an in-memory pipe whose other end is the
// SSH shell (localRelay below). So the shell lives in Go, not in the page:
// parked while its tab moves to another window, taken over there with its
// recent output, ended when its tab closes (the page's close frame, or its
// connection gone) or when it was parked and never taken over.
//
// Who can use it: /local/ssh-tunnel/ is behind the proxy's gates (ServeHTTP:
// the per-run secret cookie of this app's WebViews, an Origin of the local
// UI, required here); the token is 256 random bits, single use, and an
// unclaimed one expires (localShellTTL: its connection closes).

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// A dialed connection whose page never opened its tunnel is closed.
	localShellTTL = 30 * time.Second
	// Local shells waiting for their page, at most.
	localShellMaxPending = 64
)

// localShellPending: a dialed connection (host key decided, signed in),
// waiting for its page's tunnel.
type localShellPending struct {
	client     sshClientLike
	cols, rows int
	label      string // (logs only: the vault entry's id)
	stop       func() bool
}

// sshClientLike: what the relay needs of an *ssh.Client (a fake in tests).
type sshClientLike interface {
	OpenShell(cols, rows int, onData func([]byte), onClose func(error)) (localShellConn, error)
	Exec(cmd string, timeout time.Duration) ([]byte, error)
	Close() error
}

// localShellConn: a live shell (sshConn in ssh_session.go).
type localShellConn interface {
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Close() error
}

// realSSHClient adapts *ssh.Client.
type realSSHClient struct {
	c  *ssh.Client
	id string
}

func (r realSSHClient) OpenShell(cols, rows int, onData func([]byte), onClose func(error)) (localShellConn, error) {
	return openShellConn(r.id, r.c, cols, rows, onData, onClose)
}

func (r realSSHClient) Exec(cmd string, timeout time.Duration) ([]byte, error) {
	sess, err := r.c.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	type res struct {
		out []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := sess.Output(cmd)
		ch <- res{out, err}
	}()
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(timeout):
		return nil, errors.New("ssh: command timed out")
	}
}

func (r realSSHClient) Close() error { return r.c.Close() }

// localShells: the connections waiting for their tunnel, and the live shells
// (by their tunnel token: the drop of files asks a live one for its folder).
type localShellRegistry struct {
	mu      sync.Mutex
	pending map[string]*localShellPending
	live    map[string]sshClientLike
	labels  map[string]string // live token -> its vault entry (its Info panel: localconn_info.go)
	ttl     time.Duration
	after   func(d time.Duration, f func()) func() bool
	newTok  func() string
}

func newLocalShellRegistry() *localShellRegistry {
	return &localShellRegistry{
		pending: map[string]*localShellPending{}, live: map[string]sshClientLike{}, labels: map[string]string{}, ttl: localShellTTL,
		after:  func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
		newTok: randomToken,
	}
}

var appLocalShells = newLocalShellRegistry()

var errLocalShellFull = errors.New("ssh: too many connections waiting")

// add: a dialed connection; its one-time tunnel token.
func (r *localShellRegistry) add(client sshClientLike, cols, rows int, label string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) >= localShellMaxPending {
		return "", errLocalShellFull
	}
	tok := r.newTok()
	p := &localShellPending{client: client, cols: cols, rows: rows, label: label}
	p.stop = r.after(r.ttl, func() { r.expire(tok, p) })
	r.pending[tok] = p
	return tok, nil
}

func (r *localShellRegistry) expire(tok string, p *localShellPending) {
	r.mu.Lock()
	if r.pending[tok] != p {
		r.mu.Unlock()
		return
	}
	delete(r.pending, tok)
	r.mu.Unlock()
	_ = p.client.Close()
}

// take consumes a token (single use).
func (r *localShellRegistry) take(tok string) *localShellPending {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pending[tok]
	if p == nil {
		return nil
	}
	delete(r.pending, tok)
	if p.stop != nil {
		p.stop()
	}
	return p
}

func (r *localShellRegistry) setLive(tok string, c sshClientLike, label string) {
	r.mu.Lock()
	r.live[tok] = c
	if r.labels == nil {
		r.labels = map[string]string{}
	}
	r.labels[tok] = label
	r.mu.Unlock()
}

func (r *localShellRegistry) dropLive(tok string, c sshClientLike) {
	r.mu.Lock()
	if r.live[tok] == c {
		delete(r.live, tok)
		delete(r.labels, tok)
	}
	r.mu.Unlock()
}

func (r *localShellRegistry) getLive(tok string) sshClientLike {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live[tok]
}

// counts (tests).
func (r *localShellRegistry) counts() (pending, live int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending), len(r.live)
}

// ── The relay: the shell behind the tunnel ──────────────────────────────────

// localRelay speaks the agent relay's side of a terminal tunnel on c (the
// "upstream" end termParks.run reads from and writes to): it says
// {type:'paired'}, sends the shell's output as binary frames, and takes the
// page's binary frames as keystrokes, its {type:'resize'} text frames as the
// PTY's size. It ends when the shell ends (a close frame, after an
// {type:'error'} when it failed), or when the page side closes.
type localRelay struct {
	c     net.Conn
	wmu   sync.Mutex
	shell localShellConn
	once  sync.Once
}

func (l *localRelay) writeFrame(opcode byte, payload []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return writeServerFrame(l.c, opcode, payload)
}

func (l *localRelay) sendJSON(v interface{}) {
	j, _ := json.Marshal(v)
	_ = l.writeFrame(0x1, j)
}

// end: the tunnel's end, once (reason: why the shell could not go on, or nil).
func (l *localRelay) end(reason error) {
	l.once.Do(func() {
		if reason != nil {
			msg := reason.Error()
			if len(msg) > 300 {
				msg = msg[:300]
			}
			l.sendJSON(map[string]string{"type": "error", "code": "", "message": msg})
		}
		_ = l.writeFrame(0x8, []byte{0x03, 0xE8})
		_ = l.c.Close()
	})
}

// shellEndReason: a shell that simply ended (exit, logout, an exit status)
// is no error; a broken connection is.
func shellEndReason(err error) error {
	if err == nil {
		return nil
	}
	var ee *ssh.ExitError
	var em *ssh.ExitMissingError
	if errors.As(err, &ee) || errors.As(err, &em) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// runLocalRelay: the relay of one tunnel, until it ends.
func runLocalRelay(c net.Conn, p *localShellPending, tok string, reg *localShellRegistry) {
	l := &localRelay{c: c}
	defer p.client.Close()
	l.sendJSON(map[string]string{"type": "paired"})
	sh, err := p.client.OpenShell(p.cols, p.rows,
		func(chunk []byte) { _ = l.writeFrame(0x2, chunk) },
		func(e error) { l.end(shellEndReason(e)) })
	if err != nil {
		l.end(err)
		return
	}
	l.shell = sh
	if reg != nil {
		reg.setLive(tok, p.client, p.label)
		defer reg.dropLive(tok, p.client)
	}
	defer sh.Close()
	rd := bufio.NewReader(c)
	var cont byte // the opcode of a fragmented message in progress
	var textBuf []byte
	for {
		h, err := readWSHeader(rd)
		if err != nil {
			l.end(nil)
			return
		}
		if h.length > termMaxPageFrame {
			l.end(errors.New("frame too large"))
			return
		}
		payload := make([]byte, h.length)
		if _, err := io.ReadFull(rd, payload); err != nil {
			l.end(nil)
			return
		}
		if h.masked {
			payload = unmaskPayload(payload, h.mask)
		}
		op := h.opcode
		switch {
		case op == 0x8: // close: the page went (its tab closed)
			l.end(nil)
			return
		case op == 0x9: // ping
			_ = l.writeFrame(0xA, payload)
			continue
		case op == 0xA:
			continue
		case op == 0x0: // a continuation
			op = cont
		default:
			cont = op
			textBuf = textBuf[:0]
		}
		switch op {
		case 0x2:
			if len(payload) > 0 {
				if _, err := sh.Write(payload); err != nil {
					l.end(nil)
					return
				}
			}
		case 0x1:
			textBuf = append(textBuf, payload...)
			if !h.fin {
				if len(textBuf) > termResizePeek*4 {
					textBuf = textBuf[:0]
				}
				continue
			}
			var m struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(textBuf, &m) == nil && m.Type == "resize" && m.Cols >= 1 && m.Rows >= 1 && m.Cols <= 65535 && m.Rows <= 65535 {
				_ = sh.Resize(m.Cols, m.Rows)
			}
			textBuf = textBuf[:0]
		}
	}
}

// /local/ssh-tunnel/<token>  (WebSocket): a local connection's terminal.
func (p *Proxy) handleSSHTunnel(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, "/local/ssh-tunnel/")
	key := r.Header.Get("Sec-WebSocket-Key")
	if r.Method != http.MethodGet || !p.localOriginOK(r) || p.terms == nil || !reParkToken.MatchString(tok) ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHasToken(r.Header.Get("Connection"), "upgrade") ||
		r.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	reg := p.localShells
	if reg == nil {
		reg = appLocalShells
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	pend := reg.take(tok) // (consumed now, whatever happens next)
	if pend == nil {
		http.Error(w, "gone", http.StatusGone)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		_ = pend.client.Close()
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(key + wsGUID))
	_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err := buf.Flush(); err != nil {
		_ = pend.client.Close()
		return
	}
	up, relayEnd := net.Pipe()
	defer up.Close()
	go runLocalRelay(relayEnd, pend, tok, reg)
	p.terms.run(tok, up, bufio.NewReader(up), conn, buf.Reader)
}

// ── The shell's folder (a drop of files on its terminal) ────────────────────

// localCwdScript: on Linux, the folder of THIS connection's login shell and
// whether it is at its prompt, asked on a second channel of the same SSH
// connection (no PTY: never seen in the terminal). The shell is the session
// leader with a terminal whose SSH_CONNECTION is this connection's (the
// same client port: one shell per connection); at its prompt when its
// process group holds the terminal (no command running). Prints
// "<1|0>\n<path>"; fails elsewhere (no /proc: macOS, BSD) or when unsure.
const localCwdScript = `c="$SSH_CONNECTION"; [ -n "$c" ] || exit 3
for d in /proc/[0-9]*; do
  [ -r "$d/environ" ] || continue
  read -r s < "$d/stat" 2>/dev/null || continue
  s=${s##*") "}
  set -- $s
  p=${d#/proc/}
  [ "$4" = "$p" ] || continue
  [ "$5" != "0" ] || continue
  tr '\0' '\n' < "$d/environ" 2>/dev/null | grep -qxF "SSH_CONNECTION=$c" || continue
  w=$(readlink "$d/cwd" 2>/dev/null) || continue
  if [ "$3" = "$6" ]; then f=1; else f=0; fi
  printf '%s\n%s\n' "$f" "$w"
  exit 0
done
exit 4`

// localCwd: what a live shell reports (path "" when it cannot be known).
type localCwd struct {
	Path string `json:"path"`
	Sure bool   `json:"sure"`
}

func parseLocalCwd(out []byte) localCwd {
	s := strings.TrimRight(string(out), "\n")
	i := strings.IndexByte(s, '\n')
	if i < 0 {
		return localCwd{}
	}
	flag, path := s[:i], s[i+1:]
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\n\r") || len(path) > maxToolPath {
		return localCwd{}
	}
	return localCwd{Path: path, Sure: flag == "1"}
}

func (r *localShellRegistry) cwd(tok string) localCwd {
	c := r.getLive(tok)
	if c == nil {
		return localCwd{}
	}
	if out, err := c.Exec(localCwdScript, 8*time.Second); err == nil {
		if lc := parseLocalCwd(out); lc.Path != "" {
			return lc
		}
	}
	// No /proc (macOS, the BSDs): ps names the shell, lsof / pwdx / procstat its folder.
	return localCwdPs(c)
}

// ── Without /proc: ps, then lsof / pwdx / procstat ──────────────────────────
// The login shell of THIS connection is the child with a terminal of the
// connection's own sshd process (the parent of this exec's shell: OpenSSH
// runs every channel of a connection under the same process, sshd or
// sshd-session; through a non-POSIX login shell, its parent). At its prompt
// when ps says it is in its terminal's foreground group ('+' in its state:
// no command running). Both scripts run under /bin/sh whatever the account's
// shell (fish, csh...). What stays asked of the operator (the drop's folder
// question): a server without ps (or whose ps knows no -o), without lsof,
// pwdx and procstat; a shell running a command (not at its prompt: the
// folder is proposed, not taken); a connection with several terminals of its
// own (multiplexed: ambiguous); a Windows OpenSSH server (cmd, PowerShell).
// A shell that reports its folder itself (OSC 7: term_drop.go) needs none of it.
const localCwdPsScript = `p=$PPID
c=$(ps -o comm= -p "$p" 2>/dev/null)
case "$c" in *sshd*) ;; *) p=$(ps -o ppid= -p "$p" 2>/dev/null | tr -d ' ');; esac
echo "P $p"
ps -A -o pid= -o ppid= -o tty= -o stat= 2>/dev/null`

// localPidCwdScript: the folder of process pid, by whichever tool the
// system has (macOS: lsof; Solaris, Linux: pwdx; FreeBSD: procstat).
func localPidCwdScript(pid int) string {
	p := strconv.Itoa(pid)
	return "echo @lsof; lsof -a -p " + p + " -d cwd -Fn 2>/dev/null; echo @pwdx; pwdx " + p + " 2>/dev/null; echo @procstat; procstat -h -f " + p + " 2>/dev/null"
}

// parsePsShell: the login shell's pid in localCwdPsScript's output, and
// whether it is at its prompt; ok false when there is none or several.
func parsePsShell(out []byte) (pid int, prompt bool, ok bool) {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "P ") {
		return 0, false, false
	}
	conn, err := strconv.Atoi(strings.TrimSpace(lines[0][2:]))
	if err != nil || conn <= 1 {
		return 0, false, false
	}
	found := 0
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		id, err1 := strconv.Atoi(f[0])
		pp, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || pp != conn || id <= 1 {
			continue
		}
		if tty := f[2]; tty == "?" || tty == "??" || tty == "-" {
			continue
		}
		st := ""
		if len(f) > 3 {
			st = f[3]
		}
		found++
		pid, prompt = id, strings.Contains(st, "+")
	}
	if found != 1 {
		return 0, false, false
	}
	return pid, prompt, true
}

// parsePidCwd: the folder in localPidCwdScript's output ("" when none said it).
func parsePidCwd(out []byte) string {
	sec := ""
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		if strings.HasPrefix(l, "@") {
			sec = l
			continue
		}
		var p string
		switch sec {
		case "@lsof": // "p123", "fcwd", "n/path"
			if strings.HasPrefix(l, "n/") {
				p = l[1:]
			}
		case "@pwdx": // "123: /path"
			if i := strings.Index(l, ": /"); i > 0 {
				p = l[i+2:]
			}
		case "@procstat": // "  123 zsh   cwd v d -------- - - - /path"
			if f := strings.Fields(l); len(f) >= 4 && f[2] == "cwd" {
				if i := strings.Index(l, " /"); i >= 0 {
					p = strings.TrimSpace(l[i+1:])
				}
			}
		}
		if p != "" && strings.HasPrefix(p, "/") && !strings.ContainsRune(p, 0) && len(p) <= maxToolPath {
			return p
		}
	}
	return ""
}

func localCwdPs(c sshClientLike) localCwd {
	out, err := c.Exec("/bin/sh -c "+shQuote(localCwdPsScript), 8*time.Second)
	if err != nil {
		return localCwd{}
	}
	pid, prompt, ok := parsePsShell(out)
	if !ok {
		return localCwd{}
	}
	out2, _ := c.Exec("/bin/sh -c "+shQuote(localPidCwdScript(pid)), 8*time.Second)
	p := parsePidCwd(out2)
	if p == "" {
		return localCwd{}
	}
	return localCwd{Path: p, Sure: prompt}
}
