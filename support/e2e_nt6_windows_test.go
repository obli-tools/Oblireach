//go:build windows && !nt6

package main

// End-to-end test of the nt6 build with the real engine: the e2e test host
// (e2ehost_windows.go) compiled like OblireachSupport-nt6.exe (Go 1.20, nt6
// tag, pion/webrtc v3, cgo engine) against an operator of the modern line
// (this test, pion/webrtc v4) through a local oblireach-rdv. Two rounds,
// direct then forced relay: the init message, the first video frames and
// their codec, and chat both ways (sanitised on the host side). The host
// accepts every request and captures the real screen, nothing moves the
// pointer. Only runs with SUPPORT_E2E_NT6=1:
//
//	SUPPORT_E2E_NT6=1 go test -run TestNT6EndToEnd -v .
//
// OBLIREACH_GO120 overrides the Go 1.20 toolchain (default C:\Go1.20\bin\go.exe);
// a MinGW-w64 gcc must be in PATH (cgo).

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oblireach/supportlink"
)

type nt6Host struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan string
}

func (h *nt6Host) expect(t *testing.T, word string, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-h.lines:
			if !ok {
				t.Fatalf("nt6 host exited before %s", word)
			}
			if strings.HasPrefix(l, word+" ") || l == word {
				return strings.TrimSpace(strings.TrimPrefix(l, word))
			}
		case <-deadline:
			t.Fatalf("nt6 host: no %s line", word)
		}
	}
}

func (h *nt6Host) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

// nt6Round connects to the nt6 host, checks init, video and chat, and
// closes the session.
func nt6Round(t *testing.T, h *nt6Host, server string, relay bool) {
	t.Helper()
	w := strings.Fields(h.expect(t, "WAITING", 30*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t0 := time.Now()
	s, err := supportlink.Connect(ctx, supportlink.OperatorConfig{
		Server: server, Key: e2eKey, Name: "Modern Operator",
		ICE: supportlink.ICEOptions{ForceRelay: relay},
	}, w[0], w[1])
	cancel()
	if err != nil {
		t.Fatalf("modern operator connect: %v", err)
	}
	defer s.Close()
	t.Logf("link up in %s, path %s (%s)", time.Since(t0).Round(time.Millisecond), s.Path(), s.PathDetail())
	if relay != s.Path().Relayed() {
		t.Errorf("path %s, relay wanted: %v", s.Path(), relay)
	}
	hp := h.expect(t, "SESSION", 20*time.Second)
	t.Logf("nt6 host sees: %s", hp)

	type frame struct {
		op  byte
		p   []byte
		err error
	}
	frames := make(chan frame, 256)
	go func() {
		for {
			op, p, err := s.ReadFrame()
			frames <- frame{op, p, err}
			if err != nil {
				return
			}
		}
	}()
	var init struct {
		Type     string   `json:"type"`
		Code     string   `json:"code"`
		Codec    string   `json:"codec"`
		Width    int      `json:"width"`
		Height   int      `json:"height"`
		Features []string `json:"features"`
	}
	gotInit, videos, videoBytes := false, 0, 0
	var firstVideo time.Duration
	t1 := time.Now()
	deadline := time.After(20 * time.Second)
	for !gotInit || videos < 10 {
		select {
		case f := <-frames:
			if f.err != nil {
				t.Fatalf("operator read: %v", f.err)
			}
			if f.op == supportlink.OpText {
				var m struct {
					Type  string `json:"type"`
					Code  string `json:"code"`
					Codec string `json:"codec"`
				}
				_ = json.Unmarshal(f.p, &m)
				switch m.Type {
				case "init":
					_ = json.Unmarshal(f.p, &init)
					gotInit = true
				case "codec_switch":
					t.Logf("codec switch to %s", m.Codec)
				case "error":
					if m.Code == "capture_init_failed" {
						t.Skipf("no capturable desktop: %s", f.p)
					}
					t.Logf("engine error message: %s", f.p)
				}
				continue
			}
			if f.op == supportlink.OpBinary && len(f.p) > 0 && f.p[0] >= 0x01 && f.p[0] <= 0x05 {
				if videos == 0 {
					firstVideo = time.Since(t1)
					t.Logf("first video frame: type 0x%02x, %d bytes", f.p[0], len(f.p))
				}
				videos++
				videoBytes += len(f.p)
			}
		case <-deadline:
			t.Fatalf("init %v, %d video frames after 20 s", gotInit, videos)
		}
	}
	chat := false
	for _, f := range init.Features {
		chat = chat || f == "chat"
	}
	t.Logf("init: %dx%d, codec %q, %d features (chat %v); first video after %s, 10 frames %d bytes",
		init.Width, init.Height, init.Codec, len(init.Features), chat, firstVideo.Round(time.Millisecond), videoBytes)
	if !chat {
		t.Fatalf("chat not advertised: %v", init.Features)
	}

	// Chat: operator -> nt6 host (sanitised by the client), then the reverse.
	line, _ := json.Marshal(map[string]string{"type": "chat", "text": "Hello from the operator \u202e\x07!"})
	if err := s.WriteFrame(supportlink.OpText, line); err != nil {
		t.Fatalf("operator chat: %v", err)
	}
	got := h.expect(t, "CHAT", 10*time.Second)
	var text string
	if err := json.Unmarshal([]byte(got), &text); err != nil || text != "Hello from the operator !" {
		t.Fatalf("nt6 host transcript: %s (%v)", got, err)
	}
	want := "Hello from the person, ça va ? 日本語"
	h.send(t, "chat "+want)
	end := time.After(10 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.err != nil {
				t.Fatalf("operator read: %v", f.err)
			}
			var m struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if f.op == supportlink.OpText && json.Unmarshal(f.p, &m) == nil && m.Type == "chat" {
				if m.Text != want {
					t.Fatalf("operator got chat %q", m.Text)
				}
				t.Logf("chat both ways ok")
				s.Close()
				if r := h.expect(t, "ENDED", 10*time.Second); r != "peer" {
					t.Errorf("nt6 host end reason %q, want peer", r)
				}
				return
			}
		case <-end:
			t.Fatal("the nt6 host's chat line did not reach the operator")
		}
	}
}

func TestNT6EndToEnd(t *testing.T) {
	if os.Getenv("SUPPORT_E2E_NT6") != "1" {
		t.Skip("set SUPPORT_E2E_NT6=1 (builds the nt6 test host with Go 1.20, captures the screen)")
	}
	go120 := os.Getenv("OBLIREACH_GO120")
	if go120 == "" {
		go120 = `C:\Go1.20\bin\go.exe`
	}
	if _, err := os.Stat(go120); err != nil {
		t.Skipf("Go 1.20 toolchain not found (%s): %v", go120, err)
	}
	bin := filepath.Join(t.TempDir(), "support-e2e-host-nt6.exe")
	build := exec.Command(go120, "build", "-tags", "e2ehost nt6", "-modfile=go120.mod", "-overlay=nt6overlay.json",
		"-ldflags", "-s -w", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the nt6 test host: %v\n%s", err, out)
	}
	server := startRDV(t)

	cmd := exec.Command(bin, server)
	cmd.Stderr = io.Discard // engine diagnostics
	if testing.Verbose() && os.Getenv("SUPPORT_E2E_NT6_STDERR") == "1" {
		cmd.Stderr = os.Stderr
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &nt6Host{cmd: cmd, in: in, lines: make(chan string, 64)}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
		close(h.lines)
	}()
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	t.Run("direct", func(t *testing.T) { nt6Round(t, h, server, false) })
	t.Run("relay", func(t *testing.T) { nt6Round(t, h, server, true) })

	h.send(t, "quit")
	t.Logf("nt6 host diagnostics (lines input encoder): %s", h.expect(t, "DIAG", 15*time.Second))
}
