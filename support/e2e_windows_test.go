//go:build windows

package main

// End-to-end test on a real machine: a local oblireach-rdv (TLS off, one
// operator key), the thin client's controller with the production backend
// (supportlink + the ObliReach engine capturing the real screen) and an
// auto-accepting view, and a minimal operator built on supportlink.Connect.
//
// It moves the real mouse pointer, so it only runs with SUPPORT_E2E=1:
//
//	SUPPORT_E2E=1 go test -ldflags "-s -w" -run TestEndToEnd -v .

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/oblireach/agent/engine"
	"github.com/oblireach/supportlink"
)

const e2eKey = "e2e-operator-key-0123456789abcdef"

// autoView accepts every request with fixed permissions and forwards the
// states.
type autoView struct {
	perms  Permissions
	states chan State
}

func (v *autoView) Show(s State) {
	select {
	case v.states <- s:
	default:
	}
}

func (v *autoView) Ask(ctx context.Context, a Approval) (Permissions, bool) {
	return v.perms, true
}

func (v *autoView) wait(t *testing.T, p Phase, ok func(State) bool) State {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case s := <-v.states:
			if s.Phase == p && (ok == nil || ok(s)) {
				return s
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", p)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startRDV builds and starts the rendezvous server; it returns its
// signalling URL.
func startRDV(t *testing.T) string {
	t.Helper()
	server, _, _ := startRDVEnv(t)
	return server
}

// startRDVEnv is startRDV with extra RDV_* settings; it also returns the
// server binary and its data directory (CLI checks).
func startRDVEnv(t *testing.T, extra ...string) (server, bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "rdv.exe")
	build := exec.Command("go", "build", "-ldflags", "-s -w", "-o", bin, "./cmd/oblireach-rdv")
	build.Dir = filepath.Join("..", "rdv")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the rendezvous server: %v\n%s", err, out)
	}
	listen := freePort(t)
	admin := freePort(t)
	turn := freePort(t)
	relayBase := 47000 + (os.Getpid() % 50 * 20)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"RDV_TLS=off",
		fmt.Sprintf("RDV_LISTEN=127.0.0.1:%d", listen),
		fmt.Sprintf("RDV_ADMIN_LISTEN=127.0.0.1:%d", admin),
		"RDV_PUBLIC_IP=127.0.0.1",
		"RDV_ICE_HOST=127.0.0.1",
		fmt.Sprintf("RDV_TURN_PORT=%d", turn),
		"RDV_TURN_LISTEN_IP=127.0.0.1",
		fmt.Sprintf("RDV_RELAY_PORTS=%d-%d", relayBase, relayBase+19),
		"RDV_RELAY_ALLOW_PRIVATE=true",
		"RDV_DATA_DIR="+dir,
		"RDV_OPERATOR_KEYS=helpdesk:"+e2eKey,
		"RDV_LOG_LEVEL=warn",
	)
	cmd.Env = append(cmd.Env, extra...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	for i := 0; i < 100; i++ {
		if r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", admin)); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return fmt.Sprintf("ws://127.0.0.1:%d", listen), bin, dir
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("rendezvous server did not start")
	return "", "", ""
}

var pGetCursorPos = user32.NewProc("GetCursorPos")

func cursorPos() point {
	var p point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p
}

// operatorRound connects, checks the init message (chat advertised) and one
// video frame, and sends a mouse move. It returns the session for the
// caller to end, and the chat lines the host sends.
func operatorRound(t *testing.T, server, id, code string, relay bool, target int32) (*supportlink.Session, bool, <-chan string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t0 := time.Now()
	s, err := supportlink.Connect(ctx, supportlink.OperatorConfig{
		Server: server, Key: e2eKey, Name: "E2E Operator",
		ICE: supportlink.ICEOptions{ForceRelay: relay},
	}, id, code)
	if err != nil {
		t.Fatalf("operator connect: %v", err)
	}
	t.Logf("operator: link up in %s, path %s (%s)", time.Since(t0).Round(time.Millisecond), s.Path(), s.PathDetail())
	if relay != s.Path().Relayed() {
		t.Errorf("path %s, relay wanted: %v", s.Path(), relay)
	}

	type frame struct {
		op  byte
		p   []byte
		err error
	}
	frames := make(chan frame, 64)
	go func() {
		for {
			op, p, err := s.ReadFrame()
			frames <- frame{op, p, err}
			if err != nil {
				return
			}
		}
	}()
	chats := make(chan string, 16)
	gotInit, video := false, false
	deadline := time.After(20 * time.Second)
	t1 := time.Now()
	for !(gotInit && video) {
		select {
		case f := <-frames:
			if f.err != nil {
				t.Fatalf("operator read: %v", f.err)
			}
			if f.op == supportlink.OpText {
				var m struct {
					Type     string   `json:"type"`
					Code     string   `json:"code"`
					Width    int      `json:"width"`
					Height   int      `json:"height"`
					Features []string `json:"features"`
				}
				_ = json.Unmarshal(f.p, &m)
				switch m.Type {
				case "init":
					gotInit = true
					t.Logf("init after %s: %dx%d, %d features", time.Since(t1).Round(time.Millisecond), m.Width, m.Height, len(m.Features))
					chat := false
					for _, ft := range m.Features {
						if strings.HasPrefix(ft, "file") {
							t.Errorf("files not granted but advertised: %q", ft)
						}
						chat = chat || ft == "chat"
					}
					if !chat {
						t.Errorf("chat not advertised: %v", m.Features)
					}
				case "error":
					if m.Code == "capture_init_failed" {
						t.Logf("no capturable desktop: %s", f.p)
						return s, false, chats
					}
				}
				continue
			}
			if f.op == supportlink.OpBinary && len(f.p) > 0 && f.p[0] >= 0x01 && f.p[0] <= 0x05 {
				if !video {
					t.Logf("first video frame (type 0x%02x, %d bytes) %s after link up", f.p[0], len(f.p), time.Since(t1).Round(time.Millisecond))
				}
				video = true
			}
		case <-deadline:
			t.Fatalf("no init / video frame (init %v, video %v)", gotInit, video)
		}
	}
	// Drain frames in the background so the stream keeps flowing; chat
	// lines go to the caller.
	go func() {
		for f := range frames {
			if f.err != nil {
				return
			}
			if f.op == supportlink.OpText {
				var m struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(f.p, &m) == nil && m.Type == "chat" {
					select {
					case chats <- m.Text:
					default:
					}
				}
			}
		}
	}()

	before := cursorPos()
	// The frame has the desktop's size here (single monitor, no scaling
	// asked): frame coordinates are desktop coordinates, within rounding.
	mv, _ := json.Marshal(map[string]any{"type": "mouse", "action": "move", "x": target, "y": target})
	if err := s.WriteFrame(supportlink.OpText, mv); err != nil {
		t.Fatalf("mouse move: %v", err)
	}
	moved := false
	for i := 0; i < 40 && !moved; i++ {
		time.Sleep(50 * time.Millisecond)
		p := cursorPos()
		moved = abs32(p.X-target) <= 8 && abs32(p.Y-target) <= 8
	}
	t.Logf("mouse move to %d,%d: cursor %v -> %v (reached: %v)", target, target, before, cursorPos(), moved)
	if !moved {
		t.Errorf("the mouse move did not reach the pointer")
	}
	return s, true, chats
}

// chatRound exchanges chat lines both ways: the operator's line reaches the
// transcript sanitised, the person's line reaches the operator.
func chatRound(t *testing.T, s *supportlink.Session, chats <-chan string, c *Controller, view *autoView) {
	t.Helper()
	before := len(c.State().Session.Chat)
	line, _ := json.Marshal(map[string]string{"type": "chat", "text": "Hello from the operator \u202e\x07!"})
	t0 := time.Now()
	if err := s.WriteFrame(supportlink.OpText, line); err != nil {
		t.Fatalf("operator chat: %v", err)
	}
	st := view.wait(t, PhaseSession, func(st State) bool { return len(st.Session.Chat) > before })
	m := st.Session.Chat[len(st.Session.Chat)-1]
	t.Logf("operator -> host chat in %s: %q", time.Since(t0).Round(time.Millisecond), m.Text)
	if m.From != ChatFromOperator || m.Text != "Hello from the operator !" {
		t.Errorf("host transcript line %+v", m)
	}
	t0 = time.Now()
	if err := c.SendChat("Hello from the person, ça va ? 日本語"); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	select {
	case got := <-chats:
		t.Logf("host -> operator chat in %s: %q", time.Since(t0).Round(time.Millisecond), got)
		if got != "Hello from the person, ça va ? 日本語" {
			t.Errorf("operator got %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the person's chat line did not reach the operator")
	}
	st = view.wait(t, PhaseSession, func(st State) bool {
		n := len(st.Session.Chat)
		return n > 0 && st.Session.Chat[n-1].From == ChatFromMe
	})
	if st.Session.Chat[len(st.Session.Chat)-1].Failed {
		t.Error("the person's line is marked as not sent")
	}
}

// lockedBuf collects the engine diagnostics.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// diskWatchDirs: where the engine used to leave its diagnostic files.
func diskWatchDirs() []string {
	return []string{os.TempDir(), `C:\Users\Public`, `C:\Windows\Temp`, `C:\ProgramData`}
}

type diskEntry struct {
	size int64
	mod  time.Time
}

// diskSnapshot lists the watched folders (top level, and one level into
// any entry whose name contains "oblireach").
func diskSnapshot() map[string]diskEntry {
	snap := map[string]diskEntry{}
	for _, d := range diskWatchDirs() {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(d, e.Name())
			if fi, err := e.Info(); err == nil {
				snap[p] = diskEntry{fi.Size(), fi.ModTime()}
			}
			if e.IsDir() && strings.Contains(strings.ToLower(e.Name()), "oblireach") {
				sub, _ := os.ReadDir(p)
				for _, s := range sub {
					if fi, err := s.Info(); err == nil {
						snap[filepath.Join(p, s.Name())] = diskEntry{fi.Size(), fi.ModTime()}
					}
				}
			}
		}
	}
	return snap
}

// diskChanges: entries new or modified since before. ours: those whose
// path names oblireach (the engine's files); other: everything else (other
// processes, the test's own temporary folders), reported only.
func diskChanges(before, after map[string]diskEntry) (ours, other []string) {
	for p, a := range after {
		if b, ok := before[p]; ok && b == a {
			continue
		}
		lp := strings.ToLower(p)
		// oblireach-chat-wv2-*: temporary folders of the agent's own chat
		// tests (agent/chat_windows_test.go), not the engine.
		if strings.Contains(lp, "oblireach") && !strings.Contains(lp, "oblireach-chat-wv2-") {
			ours = append(ours, p)
		} else {
			other = append(other, p)
		}
	}
	return ours, other
}

func TestEndToEnd(t *testing.T) {
	if os.Getenv("SUPPORT_E2E") != "1" {
		t.Skip("set SUPPORT_E2E=1 (moves the real mouse pointer)")
	}
	// As in main: no engine file on disk, no bundled DLL; the diagnostics
	// go to a writer (the -debug log in the real client).
	configureEngine()
	diag := &lockedBuf{}
	engine.SetDiagnosticOutput(diag)
	defer engine.SetDiagnosticOutput(nil)
	diskBefore := diskSnapshot()
	defer func() {
		ours, other := diskChanges(diskBefore, diskSnapshot())
		if len(other) > 0 {
			t.Logf("other changes in the watched folders during the test (not the engine): %v", other)
		}
		if len(ours) > 0 {
			t.Errorf("the engine wrote on disk with disk writes off: %v", ours)
		}
		d := diag.String()
		t.Logf("engine diagnostics routed to the host writer: %d lines (input %d, encoder %d)",
			strings.Count(d, "\n"), strings.Count(d, "[engine input]"), strings.Count(d, "[engine encoder]"))
		if !strings.Contains(d, "[engine input]") || !strings.Contains(d, "[engine encoder]") {
			t.Errorf("input / encoder diagnostics missing from the host writer:\n%s", d)
		}
	}()
	server := startRDV(t)
	view := &autoView{perms: Permissions{Control: true, Clipboard: true}, states: make(chan State, 1024)}
	b := &linkBackend{server: server, version: "e2e"}
	c := NewController(b, view, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t0 := time.Now()
	go func() { defer close(done); c.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	// Round 1: direct path, the operator ends the session.
	w := view.wait(t, PhaseWaiting, nil)
	t.Logf("registered in %s: ID %s", time.Since(t0).Round(time.Millisecond), supportlink.FormatDigits(w.ID))
	s, captured, chats := operatorRound(t, server, w.ID, w.Code, false, 8)
	st := view.wait(t, PhaseSession, nil)
	if st.Session.Relayed {
		t.Errorf("host sees a relayed path on round 1: %s", st.Session.Path)
	}
	if captured {
		chatRound(t, s, chats, c, view)
	}
	s.Close()
	end := view.wait(t, PhaseEnded, nil)
	if len(end.Session.Chat) != 0 || len(c.State().Session.Chat) != 0 {
		t.Error("the transcript outlived the session")
	}
	if end.Ended.Reason != EndPeer && captured {
		t.Errorf("operator close: reason %d, want EndPeer", end.Ended.Reason)
	}
	t.Logf("round 1 ended by the operator after %s", end.Ended.Duration.Round(time.Millisecond))
	c.Acknowledge()
	w2 := view.wait(t, PhaseWaiting, nil)
	if w2.ID == w.ID || w2.Code == w.Code {
		t.Errorf("no new ID / code after the session: %s / %s", w2.ID, w2.Code)
	}
	if !captured {
		t.Skip("no capturable desktop on this machine: video checks skipped")
	}

	// Round 2: forced relay, the person ends the session.
	s, _, chats = operatorRound(t, server, w2.ID, w2.Code, true, 48)
	st = view.wait(t, PhaseSession, nil)
	t.Logf("host path on round 2: %s (relayed %v)", st.Session.Path, st.Session.Relayed)
	if len(st.Session.Chat) != 0 {
		t.Errorf("round 2 starts with %d chat lines", len(st.Session.Chat))
	}
	chatRound(t, s, chats, c, view)
	tStop := time.Now()
	c.StopSession()
	select {
	case <-s.Done():
		t.Logf("operator saw the stop after %s (%v)", time.Since(tStop).Round(time.Millisecond), s.Err())
	case <-time.After(10 * time.Second):
		t.Fatal("the operator's session did not end after Stop")
	}
	end = view.wait(t, PhaseEnded, nil)
	if end.Ended.Reason != EndLocal {
		t.Errorf("stop: reason %d, want EndLocal", end.Ended.Reason)
	}
	c.Acknowledge()
	view.wait(t, PhaseWaiting, nil)
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
