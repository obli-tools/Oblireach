//go:build windows

package main

// "Receive help" end to end on this machine (opt-in: it captures the screen,
// moves the mouse pointer):
//
//	OBLIREACH_HELP_E2E=1 go test -count=1 -run TestReceiveHelpEndToEnd -v .
//
// Both sides are this Desktop's own code in this one process, through a real
// oblireach-rdv (TLS off):
//   - the helped side: the "Receive help" controller (receive_help.go) with
//     its production backend (supportlink + the ObliReach engine in process,
//     receive_help_windows.go), driven over its HTTP routes like the page;
//   - the operator side: "Connect by code" (support.go), its local tunnel
//     WebSocket read and written by this test as the viewer would.
// Two sessions: (1) init + video, the mouse moves the real pointer, chat
// both ways, ended by the helped person's Stop, then the same ID and a new
// code;
// (2) ended by the operator. The vault hook runs on and off around each
// session, and the engine leaves no file behind.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func helpE2EState(t *testing.T, srv *httptest.Server) map[string]interface{} {
	t.Helper()
	_, st, _ := supGet(t, srv, "help/state")
	return st
}

func waitHelpE2E(t *testing.T, srv *httptest.Server, phase string, d time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		st := helpE2EState(t, srv)
		if st["phase"] == phase {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("receive help: phase %v, want %s (%v)", st["phase"], phase, st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitHelpE2EEnded waits for the end of a session: the waiting screen with
// the "ended" notice (there is no "ended" phase: the registration goes on).
func waitHelpE2EEnded(t *testing.T, srv *httptest.Server, d time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		st := helpE2EState(t, srv)
		if st["notice"] == "ended" && st["phase"] == "waiting" {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("receive help: no end notice (%v)", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// helpE2EAccept answers the next acceptance request (as the person would,
// in the page) while the operator side waits for "ready"; the request seen.
func helpE2EAccept(srv *httptest.Server, perms map[string]interface{}) chan map[string]interface{} {
	out := make(chan map[string]interface{}, 1)
	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(srv.URL + "/local/support/help/state")
			if err == nil {
				var st map[string]interface{}
				_ = json.NewDecoder(resp.Body).Decode(&st)
				resp.Body.Close()
				if a, ok := st["approval"].(map[string]interface{}); ok && st["phase"] == "approval" {
					body := map[string]interface{}{"sid": a["sid"], "accept": true}
					for k, v := range perms {
						body[k] = v
					}
					b, _ := json.Marshal(body)
					req, _ := http.NewRequest(http.MethodPost, srv.URL+"/local/support/help/decide", strings.NewReader(string(b)))
					req.Header.Set("Origin", supOrigin)
					req.Header.Set("Content-Type", "application/json")
					if r, err := http.DefaultClient.Do(req); err == nil {
						r.Body.Close()
					}
					out <- a
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		out <- nil
	}()
	return out
}

func TestReceiveHelpEndToEnd(t *testing.T) {
	if os.Getenv("OBLIREACH_HELP_E2E") == "" {
		t.Skip("set OBLIREACH_HELP_E2E=1 (captures the screen and moves the pointer of this session)")
	}
	diskBefore := e2eDiskSnapshot()
	px, py := e2eCursor()
	defer e2eSetCursorPos.Call(uintptr(px), uintptr(py))

	rdv := startRDV(t)
	hub := newSupportHub(&memSupportStore{}, supportlinkDial)
	hub.userName = func() string { return "op" }
	var hookMu sync.Mutex
	var hooks []bool
	help := newHelpHost(hub.settings, &helpLinkBackend{version: "e2e"})
	help.onSession = func(on bool) { hookMu.Lock(); hooks = append(hooks, on); hookMu.Unlock() }
	hub.help = help
	t.Cleanup(help.stop)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.serveHTTP(w, r, supOrigin) }))
	defer srv.Close()

	// One support server for both sides: the operator's key on it (the
	// helped side needs none).
	_, out := supPost(t, srv, "servers/save", map[string]interface{}{"label": "E2E", "url": rdv, "key": e2eOperatorKey}, supOrigin)
	var sid string
	for _, s := range out["servers"].([]interface{}) {
		if m := s.(map[string]interface{}); m["builtin"] == nil {
			sid = m["id"].(string)
		}
	}

	// ── Receive help: register, an ID and a code ──
	t0 := time.Now()
	if st, out := supPost(t, srv, "help/start", map[string]string{"server": sid}, supOrigin); st != 200 {
		t.Fatalf("start %d %v", st, out)
	}
	st := waitHelpE2E(t, srv, "waiting", 30*time.Second)
	id1, code1 := st["id"].(string), st["code"].(string)
	t.Logf("registered in %v: ID %s", time.Since(t0).Round(time.Millisecond), supFmtGo(id1))

	// ── Session 1: operator connects by code, the person accepts ──
	accepted := helpE2EAccept(srv, map[string]interface{}{"control": true, "clipboard": true})
	r := openRound(t, srv, sid, id1, code1)
	a := <-accepted
	if a == nil {
		t.Fatal("no acceptance request")
	}
	if a["operator"] != "E2E Operator" || a["sas"] == "" {
		t.Errorf("acceptance request %v", a)
	}
	st = waitHelpE2E(t, srv, "session", 10*time.Second)
	sess := st["session"].(map[string]interface{})
	t.Logf("session 1: path %s, setup %v, init %v, first video frame (0x%02x) %v; acceptance showed %q (SAS %v)",
		r.path, r.setup.Round(time.Millisecond), r.firstInit.Round(time.Millisecond), r.codec, r.firstVideo.Round(time.Millisecond), a["operator"], a["sas"])
	if perms := sess["perms"].(map[string]interface{}); perms["control"] != true || perms["files"] != false {
		t.Errorf("session perms %v", perms)
	}
	if !e2eHasFeature(r.init, "chat") || !e2eHasFeature(r.init, "clipboard_text") || e2eHasFeature(r.init, "file_upload") {
		t.Errorf("init features %v", r.init["features"])
	}
	moveMouse(t, r, 40, 40)
	moveMouse(t, r, 220, 140)

	// Chat, operator -> person: in the page's state.
	r.viewer.json(t, map[string]interface{}{"type": "chat", "text": "Hello from the operator <b>!</b>"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		st = helpE2EState(t, srv)
		if lines, _ := st["chat"].([]interface{}); len(lines) > 0 {
			l := lines[0].(map[string]interface{})
			if l["from"] != "op" || l["text"] != "Hello from the operator <b>!</b>" {
				t.Errorf("chat line %v", l)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the operator's chat line did not arrive")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Chat, person -> operator.
	if code, out := supPost(t, srv, "help/chat", map[string]string{"text": "Hello from the person"}, supOrigin); code != 200 {
		t.Fatalf("chat %d %v", code, out)
	}
	line := r.viewer.nextJSON(t, "chat", 10*time.Second, nil)
	if line["text"] != "Hello from the person" {
		t.Errorf("chat to the operator %v", line)
	}
	t.Logf("chat both ways ok")

	// The person's Stop.
	tEnd := time.Now()
	supPost(t, srv, "help/end", map[string]string{}, supOrigin)
	if reason := r.viewer.waitClose(t, 15*time.Second); reason != "host_ended" {
		t.Errorf("operator side closed with %q, want host_ended", reason)
	}
	st = waitHelpE2EEnded(t, srv, 10*time.Second)
	if e := st["ended"].(map[string]interface{}); e["reason"] != "local" {
		t.Errorf("ended %v", e)
	}
	t.Logf("session 1 stopped by the person, operator told after %v", time.Since(tEnd).Round(time.Millisecond))
	waitLive(t, hub, 0)

	// The same ID (the registration lives on), a new code.
	supPost(t, srv, "help/ack", map[string]string{}, supOrigin)
	st = waitHelpE2E(t, srv, "waiting", 30*time.Second)
	id2, code2 := st["id"].(string), st["code"].(string)
	if id2 != id1 || code2 == code1 || code2 == "" {
		t.Errorf("credentials after a session: %s/%s (before %s/%s)", id2, code2, id1, code1)
	}

	// ── Session 2: view only, ended by the operator ──
	accepted = helpE2EAccept(srv, map[string]interface{}{})
	r = openRound(t, srv, sid, id2, code2)
	<-accepted
	waitHelpE2E(t, srv, "session", 10*time.Second)
	if e2eHasFeature(r.init, "clipboard_text") {
		t.Errorf("clipboard advertised without the permission: %v", r.init["features"])
	}
	bx, by := e2eCursor()
	r.viewer.json(t, map[string]interface{}{"type": "mouse", "action": "move", "x": 300, "y": 200})
	time.Sleep(500 * time.Millisecond)
	if cx, cy := e2eCursor(); cx != bx || cy != by {
		t.Errorf("view only: the pointer moved %d,%d -> %d,%d", bx, by, cx, cy)
	}
	tEnd = time.Now()
	r.viewer.write(t, 0x8, []byte("\x03\xe8"))
	st = waitHelpE2EEnded(t, srv, 15*time.Second)
	if e := st["ended"].(map[string]interface{}); e["reason"] != "peer" {
		t.Errorf("ended %v", e)
	}
	t.Logf("session 2 (view only) ended by the operator, seen after %v", time.Since(tEnd).Round(time.Millisecond))

	// Stop receiving help.
	if code, out := supPost(t, srv, "help/stop", map[string]string{}, supOrigin); code != 200 || out["phase"] != "idle" {
		t.Fatalf("stop %d %v", code, out)
	}
	hookMu.Lock()
	got := hooks
	hookMu.Unlock()
	if len(got) != 4 || !got[0] || got[1] || !got[2] || got[3] {
		t.Errorf("vault hook calls %v, want on,off,on,off", got)
	}
	var ours []string
	for p, a := range e2eDiskSnapshot() {
		if b, ok := diskBefore[p]; ok && b == a {
			continue
		}
		if lp := strings.ToLower(p); strings.Contains(lp, "oblireach") && !strings.Contains(lp, "oblireach-chat-wv2-") {
			ours = append(ours, p)
		}
	}
	if len(ours) > 0 {
		t.Errorf("engine files written on disk: %v", ours)
	}
}

// supFmtGo: "482913507" -> "482 913 507" (logs).
func supFmtGo(s string) string {
	if len(s) != 9 {
		return s
	}
	return s[:3] + " " + s[3:6] + " " + s[6:]
}
