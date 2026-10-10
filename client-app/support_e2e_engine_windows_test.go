//go:build windows

package main

// End to end with the real ObliReach engine on this machine (opt-in: it
// captures the screen, moves the mouse pointer, uses the clipboard and
// writes a small file into the Downloads folder, then removes it):
//
//	OBLIREACH_SUPPORT_E2E_HOST=build go test -count=1 -run TestSupportEndToEndRealEngine -v .
//
// "build" builds the test host from ../support (go build -tags e2ehost: the
// thin client's controller and production backend with an auto-accepting
// view driven over stdin / stdout, see support/e2ehost_windows.go); any other
// value is the path of an already built host.
//
// Chain: a real oblireach-rdv (TLS off, one operator key) <-> the host
// (supportlink + engine.RunSession, disk writes off and no bundled DLL as in
// the released client) <-> the Desktop's support hub (connect, poll, tunnel)
// <-> the viewer's local WebSocket, driven by this test. Three sessions:
//  1. direct path, control + clipboard + files: init then video in order,
//     mouse input moves the real pointer, clipboard text both ways, a file
//     sent to the host, ended by the viewer;
//  2. direct path, control + clipboard: file transfer refused, ended by the
//     host (the person's Stop);
//  3. forced relay (TURN of the rdv), control: video and input, ended by the
//     viewer.
// Afterwards no file of the engine is left in the folders it used to write to.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/oblireach/supportlink"
)

var (
	e2eUser32          = syscall.NewLazyDLL("user32.dll")
	e2eGetCursorPos    = e2eUser32.NewProc("GetCursorPos")
	e2eSetCursorPos    = e2eUser32.NewProc("SetCursorPos")
	e2eGetSysMetrics   = e2eUser32.NewProc("GetSystemMetrics")
	e2eSetProcDPIAware = e2eUser32.NewProc("SetProcessDPIAware")
)

func e2eCursor() (int32, int32) {
	var p struct{ X, Y int32 }
	e2eGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p.X, p.Y
}

// e2eHostProc is the running test host.
type e2eHostProc struct {
	cmd   *exec.Cmd
	stdin *bufio.Writer
	lines chan string
	errs  *e2eLockedBuf
}

func (h *e2eHostProc) send(t *testing.T, cmd string) {
	t.Helper()
	if _, err := h.stdin.WriteString(cmd + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := h.stdin.Flush(); err != nil {
		t.Fatal(err)
	}
}

// expect waits for the next host line starting with prefix (other lines
// are logged and skipped).
func (h *e2eHostProc) expect(t *testing.T, prefix string, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-h.lines:
			if !ok {
				t.Fatalf("host output ended while waiting for %s\nhost stderr:\n%s", prefix, h.errs.String())
			}
			if strings.HasPrefix(l, prefix) {
				return l
			}
			t.Logf("host: %s", l)
		case <-deadline:
			t.Fatalf("host did not say %s within %v\nhost stderr:\n%s", prefix, d, h.errs.String())
		}
	}
}

type e2eLockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *e2eLockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *e2eLockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startE2EHost(t *testing.T, exe, rdv string) *e2eHostProc {
	t.Helper()
	if exe == "build" {
		exe = filepath.Join(t.TempDir(), "support-e2e-host.exe")
		b := exec.Command("go", "build", "-tags", "e2ehost", "-ldflags", "-s -w", "-o", exe, ".")
		b.Dir = filepath.Join("..", "support")
		if out, err := b.CombinedOutput(); err != nil {
			t.Fatalf("build the test host: %v\n%s", err, out)
		}
	}
	cmd := exec.Command(exe, rdv)
	cmd.Dir = filepath.Dir(exe)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	h := &e2eHostProc{cmd: cmd, stdin: bufio.NewWriter(in), lines: make(chan string, 64), errs: &e2eLockedBuf{}}
	cmd.Stderr = h.errs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("host stderr:\n%s", h.errs.String())
		}
	})
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
		close(h.lines)
	}()
	return h
}

// e2eViewer is the viewer's WebSocket on the Desktop's tunnel.
type e2eViewer struct {
	c       net.Conn
	frames  chan wsFrame
	readErr error
}

func (v *e2eViewer) write(t *testing.T, op byte, p []byte) {
	t.Helper()
	f, err := buildMaskedFrame(op, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.c.Write(f); err != nil {
		t.Fatalf("viewer write: %v", err)
	}
}

func (v *e2eViewer) json(t *testing.T, m map[string]interface{}) {
	t.Helper()
	b, _ := json.Marshal(m)
	v.write(t, 0x1, b)
}

// nextJSON waits for a text message of type typ (video and other messages
// are skipped); ok filters further.
func (v *e2eViewer) nextJSON(t *testing.T, typ string, d time.Duration, ok func(map[string]interface{}) bool) map[string]interface{} {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, open := <-v.frames:
			if !open {
				t.Fatalf("viewer closed while waiting for %s", typ)
			}
			if f.op != 0x1 {
				continue
			}
			var m map[string]interface{}
			if json.Unmarshal(f.payload, &m) != nil || m["type"] != typ {
				continue
			}
			if ok == nil || ok(m) {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s message within %v", typ, d)
		}
	}
}

// waitClose waits for the page's close frame; its reason.
func (v *e2eViewer) waitClose(t *testing.T, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, open := <-v.frames:
			if !open {
				return "(connection closed)"
			}
			if f.op == 0x8 {
				if len(f.payload) >= 2 {
					return string(f.payload[2:])
				}
				return ""
			}
		case <-deadline:
			t.Fatalf("no close frame within %v", d)
		}
	}
}

// e2eRound is one session as the Desktop opens it.
type e2eRound struct {
	viewer     *e2eViewer
	init       map[string]interface{}
	path       string
	setup      time.Duration // connect request -> ready (PAKE, approval, ICE)
	firstInit  time.Duration // WebSocket open -> init
	firstVideo time.Duration // WebSocket open -> first video frame
	codec      byte
}

func e2eHasFeature(init map[string]interface{}, f string) bool {
	list, _ := init["features"].([]interface{})
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

// openRound connects the Desktop to the host and checks the stream start:
// the init first, then at least five video frames.
func openRound(t *testing.T, srv *httptest.Server, sid, id, code string) *e2eRound {
	t.Helper()
	t0 := time.Now()
	_, out := supPost(t, srv, "connect", map[string]string{"server": sid, "id": id, "code": code, "name": "E2E Operator"}, supOrigin)
	st := waitSupStateLong(t, srv, out["conn"].(string), "ready", 60*time.Second)
	r := &e2eRound{setup: time.Since(t0)}
	r.path, _ = st["path"].(string)
	status, c, rd := wsDial(t, srv, "/local/support/tunnel/"+st["token"].(string), supOrigin)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("tunnel status %d", status)
	}
	v := &e2eViewer{c: c, frames: make(chan wsFrame, 4096)}
	r.viewer = v
	t1 := time.Now()
	go func() {
		defer close(v.frames)
		for {
			hd, err := readWSHeader(rd)
			if err != nil {
				v.readErr = err
				return
			}
			p := make([]byte, hd.length)
			if _, err := io.ReadFull(rd, p); err != nil {
				v.readErr = err
				return
			}
			v.frames <- wsFrame{hd.opcode, p}
			if hd.opcode == 0x8 {
				return
			}
		}
	}()
	video := 0
	deadline := time.After(30 * time.Second)
	for video < 5 {
		select {
		case f, open := <-v.frames:
			if !open {
				t.Fatalf("viewer closed during the stream start (%v)", v.readErr)
			}
			switch f.op {
			case 0x1:
				var m map[string]interface{}
				_ = json.Unmarshal(f.payload, &m)
				if m["type"] == "error" {
					t.Fatalf("host error: %s", f.payload)
				}
				if m["type"] == "init" && r.init == nil {
					r.init, r.firstInit = m, time.Since(t1)
				}
			case 0x2:
				if len(f.payload) == 0 {
					t.Fatal("empty binary frame")
				}
				typ := f.payload[0]
				if typ < 0x01 || typ > 0x05 {
					continue // audio, file data
				}
				if r.init == nil {
					t.Fatalf("video frame 0x%02x before the init", typ)
				}
				if video == 0 {
					r.firstVideo, r.codec = time.Since(t1), typ
					checkFirstVideoFrame(t, f.payload)
				} else if typ != r.codec {
					t.Logf("codec changed 0x%02x -> 0x%02x", r.codec, typ)
				}
				video++
			}
		case <-deadline:
			t.Fatalf("stream start: init %v, %d video frames", r.init != nil, video)
		}
	}
	return r
}

// checkFirstVideoFrame: the first frame of a stream must be decodable on
// its own (JPEG SOI, or an H.264 access unit carrying SPS + IDR).
func checkFirstVideoFrame(t *testing.T, p []byte) {
	t.Helper()
	switch p[0] {
	case 0x01:
		if len(p) < 3 || p[1] != 0xFF || p[2] != 0xD8 {
			t.Errorf("first JPEG frame without SOI: % x", p[:min(len(p), 8)])
		}
	case 0x02:
		var sps, idr bool
		b := p[1:]
		for i := 0; i+3 < len(b); i++ {
			if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
				switch b[i+3] & 0x1F {
				case 7:
					sps = true
				case 5:
					idr = true
				}
			}
		}
		if !sps || !idr {
			t.Errorf("first H.264 frame: SPS %v, IDR %v (% x)", sps, idr, p[:min(len(p), 16)])
		}
	}
}

// moveMouse sends a move to frame pixel (x, y) and checks the real pointer
// lands where the frame says (frame pixels scaled to the primary screen).
func moveMouse(t *testing.T, r *e2eRound, x, y int) {
	t.Helper()
	fw, _ := r.init["width"].(float64)
	fh, _ := r.init["height"].(float64)
	sw, _, _ := e2eGetSysMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := e2eGetSysMetrics.Call(1) // SM_CYSCREEN
	if fw <= 0 || fh <= 0 {
		t.Fatalf("init without a size: %v", r.init)
	}
	wx, wy := int32(float64(x)*float64(sw)/fw), int32(float64(y)*float64(sh)/fh)
	bx, by := e2eCursor()
	r.viewer.json(t, map[string]interface{}{"type": "mouse", "action": "move", "x": x, "y": y})
	tol := int32(8 + float64(sw)/fw)
	for i := 0; i < 60; i++ {
		time.Sleep(50 * time.Millisecond)
		cx, cy := e2eCursor()
		if abs32(cx-wx) <= tol && abs32(cy-wy) <= tol {
			t.Logf("mouse move to frame %d,%d: pointer %d,%d -> %d,%d (want %d,%d) in %v", x, y, bx, by, cx, cy, wx, wy, time.Duration(i+1)*50*time.Millisecond)
			return
		}
	}
	cx, cy := e2eCursor()
	t.Errorf("mouse move to frame %d,%d: pointer at %d,%d, want %d,%d (frame %vx%v, screen %dx%d)", x, y, cx, cy, wx, wy, fw, fh, sw, sh)
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// The host's clipboard, through PowerShell (another process, like any
// program of the person).
func psClipboard(t *testing.T, set *string) string {
	t.Helper()
	script := "Get-Clipboard -Raw"
	if set != nil {
		script = "Set-Clipboard -Value '" + strings.ReplaceAll(*set, "'", "''") + "'"
	}
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("clipboard (%s): %v %s", script, err, out)
	}
	return strings.TrimRight(string(out), "\r\n")
}

// uploadFile sends data as name; the file_done message (or the file_error).
func uploadFile(t *testing.T, v *e2eViewer, tid uint32, name string, data []byte) map[string]interface{} {
	t.Helper()
	v.json(t, map[string]interface{}{"type": "file_begin", "tid": tid, "name": name, "size": len(data), "mime": "text/plain", "dest": "downloads"})
	var reply map[string]interface{}
	deadline := time.After(15 * time.Second)
	for reply == nil {
		select {
		case f, open := <-v.frames:
			if !open {
				t.Fatal("viewer closed during file_begin")
			}
			var m map[string]interface{}
			if f.op == 0x1 && json.Unmarshal(f.payload, &m) == nil && (m["type"] == "file_accept" || m["type"] == "file_error") {
				reply = m
			}
		case <-deadline:
			t.Fatal("no answer to file_begin")
		}
	}
	if reply["type"] == "file_error" {
		return reply
	}
	chunk := int(reply["chunk"].(float64))
	seq := uint32(0)
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		fr := make([]byte, 9, 9+end-off)
		fr[0] = 0x20
		binary.LittleEndian.PutUint32(fr[1:], tid)
		binary.LittleEndian.PutUint32(fr[5:], seq)
		v.write(t, 0x2, append(fr, data[off:end]...))
		seq++
	}
	sum := sha256.Sum256(data)
	v.json(t, map[string]interface{}{"type": "file_end", "tid": tid, "sha256": hex.EncodeToString(sum[:])})
	return v.nextJSON(t, "file_done", 30*time.Second, func(m map[string]interface{}) bool { return m["tid"] == float64(tid) })
}

// Disk watch: the folders the engine used to write its diagnostics to.
type e2eDiskEntry struct {
	size int64
	mod  time.Time
}

func e2eDiskSnapshot() map[string]e2eDiskEntry {
	snap := map[string]e2eDiskEntry{}
	for _, d := range []string{os.TempDir(), `C:\Users\Public`, `C:\Windows\Temp`, `C:\ProgramData`} {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(d, e.Name())
			if fi, err := e.Info(); err == nil {
				snap[p] = e2eDiskEntry{fi.Size(), fi.ModTime()}
			}
			if e.IsDir() && strings.Contains(strings.ToLower(e.Name()), "oblireach") {
				sub, _ := os.ReadDir(p)
				for _, s := range sub {
					if fi, err := s.Info(); err == nil {
						snap[filepath.Join(p, s.Name())] = e2eDiskEntry{fi.Size(), fi.ModTime()}
					}
				}
			}
		}
	}
	return snap
}

func TestSupportEndToEndRealEngine(t *testing.T) {
	exe := os.Getenv("OBLIREACH_SUPPORT_E2E_HOST")
	if exe == "" {
		t.Skip("set OBLIREACH_SUPPORT_E2E_HOST=build (or the path of a host built with -tags e2ehost in ../support)")
	}
	e2eSetProcDPIAware.Call()
	diskBefore := e2eDiskSnapshot()
	clipBefore := psClipboard(t, nil)
	px, py := e2eCursor()
	defer func() {
		psClipboard(t, &clipBefore)
		e2eSetCursorPos.Call(uintptr(px), uintptr(py))
	}()

	rdv := startRDV(t)
	tHost := time.Now()
	host := startE2EHost(t, exe, rdv)
	var forceRelay atomic.Bool
	dial := func(ctx context.Context, cfg supportlink.OperatorConfig, id, code string) (supportSess, error) {
		cfg.ICE.ForceRelay = forceRelay.Load()
		return supportlinkDial(ctx, cfg, id, code)
	}
	h := newSupportHub(&memSupportStore{}, dial)
	h.userName = func() string { return "op" }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveHTTP(w, r, supOrigin) }))
	defer srv.Close()
	_, out := supPost(t, srv, "servers/save", map[string]interface{}{"label": "E2E", "url": rdv, "key": e2eOperatorKey}, supOrigin)
	sid := out["servers"].([]interface{})[0].(map[string]interface{})["id"].(string)

	waiting := func() (string, string) {
		f := strings.Fields(host.expect(t, "WAITING ", 30*time.Second))
		if len(f) != 3 || len(f[1]) != 9 || len(f[2]) != 9 {
			t.Fatalf("host waiting line %q", f)
		}
		return f[1], f[2]
	}

	// ── Round 1: direct, control + clipboard + files, ended by the viewer ──
	host.send(t, "perms control clipboard files")
	id, code := waiting()
	t.Logf("host registered in %v", time.Since(tHost).Round(time.Millisecond))
	r := openRound(t, srv, sid, id, code)
	sess := host.expect(t, "SESSION ", 10*time.Second)
	t.Logf("round 1: path %s (host: %s), setup %v, init %v, first video frame (0x%02x) %v after the tunnel opened",
		r.path, sess, r.setup.Round(time.Millisecond), r.firstInit.Round(time.Millisecond), r.codec, r.firstVideo.Round(time.Millisecond))
	if strings.Contains(r.path, "relay") || strings.HasSuffix(sess, "true") {
		t.Errorf("round 1 relayed: %s / %s", r.path, sess)
	}
	if !e2eHasFeature(r.init, "file_upload") || !e2eHasFeature(r.init, "clipboard_text") {
		t.Errorf("round 1 features %v", r.init["features"])
	}
	moveMouse(t, r, 40, 40)
	moveMouse(t, r, 200, 120)

	// Clipboard, viewer -> host.
	tag := make([]byte, 6)
	_, _ = rand.Read(tag)
	toHost := "e2e-viewer-" + hex.EncodeToString(tag)
	r.viewer.json(t, map[string]interface{}{"type": "clipboard_set", "text": toHost, "id": "c1"})
	ack := r.viewer.nextJSON(t, "clipboard_ack", 10*time.Second, nil)
	if ack["ok"] != true {
		t.Errorf("clipboard_set: %v", ack)
	}
	if got := psClipboard(t, nil); got != toHost {
		t.Errorf("host clipboard %q, want %q", got, toHost)
	}
	// Clipboard, host -> viewer (push on change, then an explicit get).
	r.viewer.json(t, map[string]interface{}{"type": "clipboard_sync", "push": true})
	time.Sleep(300 * time.Millisecond)
	toViewer := "e2e-host-" + hex.EncodeToString(tag)
	psClipboard(t, &toViewer)
	push := r.viewer.nextJSON(t, "clipboard_content", 10*time.Second, func(m map[string]interface{}) bool { return m["text"] == toViewer })
	r.viewer.json(t, map[string]interface{}{"type": "clipboard_get", "id": "g1"})
	get := r.viewer.nextJSON(t, "clipboard_content", 10*time.Second, func(m map[string]interface{}) bool { return m["id"] == "g1" })
	if get["text"] != toViewer {
		t.Errorf("clipboard_get %v", get)
	}
	t.Logf("clipboard: viewer->host ok, host->viewer push (origin %v) and get ok", push["origin"])

	// A file to the host (allowed).
	payload := bytes.Repeat([]byte("oblireach support e2e "), 20000) // 440 KB: two data frames
	name := "oblireach-e2e-" + hex.EncodeToString(tag) + ".txt"
	tUp := time.Now()
	done := uploadFile(t, r.viewer, 1, name, payload)
	if done["ok"] != true {
		t.Fatalf("upload: %v", done)
	}
	saved, _ := done["path"].(string)
	defer os.Remove(saved)
	fileLine := host.expect(t, "FILE ", 10*time.Second)
	if strings.TrimPrefix(fileLine, "FILE ") != saved {
		t.Errorf("host notice %q, saved %q", fileLine, saved)
	}
	if b, err := os.ReadFile(saved); err != nil || !bytes.Equal(b, payload) {
		t.Errorf("saved file: %d bytes, %v", len(b), err)
	}
	t.Logf("file: %d bytes to %s in %v, host notice in its banner (no second process)", len(payload), saved, time.Since(tUp).Round(time.Millisecond))

	// End from the viewer (the operator closes the tab).
	tEnd := time.Now()
	r.viewer.write(t, 0x8, []byte("\x03\xe8"))
	if e := host.expect(t, "ENDED ", 15*time.Second); e != "ENDED peer" {
		t.Errorf("round 1 end: %q, want ENDED peer", e)
	}
	t.Logf("round 1 ended by the viewer, host saw it after %v", time.Since(tEnd).Round(time.Millisecond))
	waitLive(t, h, 0)

	// ── Round 2: direct, no files, ended by the host ──
	host.send(t, "perms control clipboard")
	id, code = waiting()
	r = openRound(t, srv, sid, id, code)
	host.expect(t, "SESSION ", 10*time.Second)
	t.Logf("round 2: path %s, setup %v, first video frame %v", r.path, r.setup.Round(time.Millisecond), r.firstVideo.Round(time.Millisecond))
	if e2eHasFeature(r.init, "file_upload") || e2eHasFeature(r.init, "file_download") || e2eHasFeature(r.init, "clipboard_files") {
		t.Errorf("files not granted but advertised: %v", r.init["features"])
	}
	refused := uploadFile(t, r.viewer, 2, "oblireach-e2e-refused-"+hex.EncodeToString(tag)+".txt", []byte("no"))
	if refused["type"] != "file_error" || refused["code"] != "forbidden" {
		t.Errorf("upload without the permission: %v", refused)
	}
	if dl, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(dl, "Downloads", "oblireach-e2e-refused-"+hex.EncodeToString(tag)+".txt")); err == nil {
			t.Error("refused file written anyway")
		}
	}
	tEnd = time.Now()
	host.send(t, "stop")
	if reason := r.viewer.waitClose(t, 15*time.Second); reason != "host_ended" {
		t.Errorf("viewer close reason %q, want host_ended", reason)
	}
	t.Logf("round 2 ended by the host, viewer closed after %v", time.Since(tEnd).Round(time.Millisecond))
	if e := host.expect(t, "ENDED ", 15*time.Second); e != "ENDED local" {
		t.Errorf("round 2 end: %q, want ENDED local", e)
	}
	waitLive(t, h, 0)

	// ── Round 3: forced relay, control only, ended by the viewer ──
	host.send(t, "perms control")
	forceRelay.Store(true)
	id, code = waiting()
	r = openRound(t, srv, sid, id, code)
	sess = host.expect(t, "SESSION ", 10*time.Second)
	t.Logf("round 3: path %s (host: %s), setup %v, init %v, first video frame (0x%02x) %v",
		r.path, sess, r.setup.Round(time.Millisecond), r.firstInit.Round(time.Millisecond), r.codec, r.firstVideo.Round(time.Millisecond))
	if !strings.HasPrefix(r.path, "relay") || !strings.HasSuffix(sess, "true") {
		t.Errorf("round 3 not relayed: %s / %s", r.path, sess)
	}
	if e2eHasFeature(r.init, "clipboard_text") {
		t.Errorf("clipboard not granted but advertised: %v", r.init["features"])
	}
	moveMouse(t, r, 120, 80)
	// Count the relayed stream for a moment.
	frames, bytesIn, t3 := 0, 0, time.Now()
	for time.Since(t3) < 2*time.Second {
		select {
		case f := <-r.viewer.frames:
			if f.op == 0x2 && len(f.payload) > 0 && f.payload[0] <= 0x05 {
				frames++
				bytesIn += len(f.payload)
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Logf("round 3: %d video frames, %d bytes in 2 s over the relay", frames, bytesIn)
	r.viewer.write(t, 0x8, []byte("\x03\xe8"))
	if e := host.expect(t, "ENDED ", 15*time.Second); e != "ENDED peer" {
		t.Errorf("round 3 end: %q, want ENDED peer", e)
	}
	waitLive(t, h, 0)

	// Quit: the engine's diagnostics went to the host, nothing to disk.
	host.expect(t, "WAITING ", 30*time.Second)
	host.send(t, "quit")
	diag := strings.Fields(host.expect(t, "DIAG ", 15*time.Second))
	t.Logf("engine diagnostics routed to the host: %v lines (input %v, encoder %v)", diag[1], diag[2], diag[3])
	if diag[2] == "0" || diag[3] == "0" {
		t.Errorf("input / encoder diagnostics missing: %v", diag)
	}
	waitExit := make(chan error, 1)
	go func() { waitExit <- host.cmd.Wait() }()
	select {
	case err := <-waitExit:
		if err != nil {
			t.Errorf("host exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("host did not exit")
	}
	var ours, other []string
	for p, a := range e2eDiskSnapshot() {
		if b, ok := diskBefore[p]; ok && b == a {
			continue
		}
		if lp := strings.ToLower(p); strings.Contains(lp, "oblireach") && !strings.Contains(lp, "oblireach-chat-wv2-") {
			ours = append(ours, p)
		} else {
			other = append(other, p)
		}
	}
	if len(other) > 0 {
		t.Logf("other changes in the watched folders (not the engine): %d entries", len(other))
	}
	if os.Getenv("OBLIREACH_SUPPORT_E2E_VERBOSE") != "" {
		t.Logf("host stderr:\n%s", host.errs.String())
	}
	if len(ours) > 0 {
		t.Errorf("the host wrote engine files on disk: %v", ours)
	}
}
