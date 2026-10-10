package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var scriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// The page goes through fmt.Sprintf: a stray '%' in ui.go shows up as
// "%!x(MISSING)" style artifacts. The viewer library and protocol constants
// are injected afterwards and must be present exactly once.
func TestBuildUIRendersCleanly(t *testing.T) {
	// English, whatever the language of this Windows: the dump below is what
	// the JS tests (test-viewer.mjs) run, and they read English texts.
	html := buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin", Language: "en"})
	if i := strings.Index(html, "%!"); i >= 0 {
		t.Fatalf("fmt artifact in page near: %q", html[max(0, i-80):min(len(html), i+40)])
	}
	for _, marker := range []string{"<!--REACH_CSS-->", "<!--REACH_LIB-->"} {
		if strings.Contains(html, marker) {
			t.Fatalf("placeholder %s was not replaced", marker)
		}
	}
	for _, want := range []string{"var REACH = Object.freeze(", "function reachMapPointer(", ".reach-overlay{", "reachRestoreView();",
		"function toolOpen(", ".dev-tools{", // (the agent's tools: agent_tools.go)
		"function termStart(", "function toolTabOpen(", ".tool-pane{", // (their tabs, the terminals: terminal_viewer.go)
		"function explorerSend(", "TOOL_IMPL.files = explorerImpl;", ".tool-host.exp-host{", // (the file explorer: explorer_viewer.go)
		"function renderFilesPage(", "function toolsSessionFilesButton(", ".files-page{", // (the Files page: files_page.go)
		"function lpCreate(", "window.__reachLocalFs = function", ".xp-split{flex:1;", // (the split view's local pane: local_pane.go)
		"function xferPump(", "function explorerCtxFor(", ".xp-dock{", // (the transfer queue: transfers.go; the device's context: explorer_viewer.go)
		"function editSync(", "window.__reachLocalEdit = function", "function renderFilesSection(", ".files-edit-pill{", // (the editing round trip: edit_page.go)
		"function termParseOsc7(", "function termDropWire(", "function termDropCloseGuard(", ".term-drop{", ".term-xfer-chip{", // (files dropped on a terminal: term_drop.go)
		"function tgRenderShown(", "function tgCtrlClick(", ".tg-grid{", // (mosaics: tab_groups.go)
		"function termThemeMenuOpen(", "var TERM_THEMES = [", ".term-theme-pop{"} { // (the terminals' themes: term_themes.go)
		if c := strings.Count(html, want); c != 1 {
			t.Fatalf("%q found %d times, want 1", want, c)
		}
	}
	scripts := scriptRe.FindAllStringSubmatch(html, -1)
	if len(scripts) < 3 {
		t.Fatalf("expected protocol, library and main scripts, got %d", len(scripts))
	}
	// Optional: dump the scripts for `node --check` and the JS unit tests.
	if dir := os.Getenv("OBLIREACH_DUMP_JS"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i, m := range scripts {
			name := filepath.Join(dir, fmt.Sprintf("script-%d.js", i))
			if err := os.WriteFile(name, []byte(m[1]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The relay sends {"type":"paired"} and the buffered agent frames right after
// the 101 response; they must reach the page even when they arrive in the
// same TCP read as the handshake headers.
func TestWSProxyKeepsFramesSentWithHandshake(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	frame := []byte{0x81, 0x11}
	frame = append(frame, []byte(`{"type":"paired"}`)...)
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		// Headers and the first WebSocket frame in ONE write.
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: x\r\n\r\n"
		_, _ = c.Write(append([]byte(resp), frame...))
		time.Sleep(500 * time.Millisecond)
	}()

	cfg := &Config{ServerURL: "http://" + up.Addr().String()}
	p := newProxy(cfg, t.TempDir(), 0)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = http.Serve(ln, p) }()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET /proxy/api/remote/tunnel/abc HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n" +
		"Cookie: " + p.authCookie() + "=" + p.secret + "\r\n\r\n" // the WebView's per-run secret
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := make([]byte, len(frame))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("first frame lost: %v", err)
	}
	if string(got) != string(frame) {
		t.Fatalf("frame = %q, want %q", got, frame)
	}
}
