package main

// End to end, opt-in (OBLIREACH_SUPPORT_E2E=1: it builds and runs the
// rendezvous server of ../rdv): a real oblireach-rdv (TLS off, one operator
// key), a host registered with supportlink whose "engine" sends an init and
// a few video frames and records the input it gets, and the Desktop side
// through its own HTTP endpoints (connect, poll, tunnel): the viewer's
// WebSocket gets the init and the frames, its input reaches the host, and
// closing the WebSocket ends the host's session.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oblireach/supportlink"
)

const e2eOperatorKey = "e2e-operator-key-0123456789abcdef"

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startRDV builds ../rdv and runs it; its signalling URL.
func startRDV(t *testing.T) string {
	t.Helper()
	url, _, _ := startRDVEnv(t)
	return url
}

// startRDVEnv is startRDV with extra RDV_* settings; it also returns the
// server binary and its data directory (CLI checks).
func startRDVEnv(t *testing.T, extra ...string) (url, bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "oblireach-rdv.exe")
	build := exec.Command("go", "build", "-o", bin, "./cmd/oblireach-rdv")
	build.Dir = filepath.Join("..", "rdv")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build rdv: %v\n%s", err, out)
	}
	port, admin, turn := freePort(t, "tcp"), freePort(t, "tcp"), freePort(t, "udp")
	relay := 46000 + int(time.Now().UnixNano()%2000)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"RDV_MODE=private", "RDV_TLS=off",
		fmt.Sprintf("RDV_LISTEN=127.0.0.1:%d", port), fmt.Sprintf("RDV_ADMIN_LISTEN=127.0.0.1:%d", admin),
		"RDV_PUBLIC_IP=127.0.0.1", "RDV_ICE_HOST=127.0.0.1", "RDV_RELAY_ALLOW_PRIVATE=true", "RDV_TURN_LISTEN_IP=127.0.0.1", fmt.Sprintf("RDV_TURN_PORT=%d", turn),
		fmt.Sprintf("RDV_RELAY_PORTS=%d-%d", relay, relay+99), "RDV_DATA_DIR="+dir,
		"RDV_OPERATOR_KEYS=e2e:"+e2eOperatorKey, "RDV_TURN_SECRET=e2e-turn-secret-0123456789", "RDV_LOG_LEVEL=warn")
	cmd.Env = append(cmd.Env, extra...)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("rdv logs:\n%s", logs.String())
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", admin)); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("rdv did not start:\n%s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), bin, dir
}

// fakeEngine: what a host's engine does with a session: the init, three
// frames, then every input frame to got, until the session ends.
func fakeEngine(sess *supportlink.Session, got chan<- string) {
	_ = sess.WriteFrame(supportlink.OpText, []byte(`{"type":"init","width":1280,"height":720,"fps":30,"codec":"h264","hostname":"E2E-HOST","features":["keepalive"]}`))
	for i := 0; i < 3; i++ {
		frame := append([]byte{0x01, byte(i)}, bytes.Repeat([]byte{byte(0x10 + i)}, 40000*(i+1))...)
		_ = sess.WriteFrame(supportlink.OpBinary, frame)
	}
	for {
		op, p, err := sess.ReadFrame()
		if err != nil {

			return
		}
		got <- fmt.Sprintf("%d:%s", op, p)
	}
}

func TestSupportEndToEnd(t *testing.T) {
	if os.Getenv("OBLIREACH_SUPPORT_E2E") != "1" {
		t.Skip("set OBLIREACH_SUPPORT_E2E=1 (builds and runs ../rdv)")
	}
	rdv := startRDV(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var approvedSAS string
	host, err := supportlink.Register(ctx, supportlink.HostConfig{
		Server: rdv, ClientVersion: "1.0.0",
		Approve: func(_ context.Context, req supportlink.ApprovalRequest) bool {
			approvedSAS = req.SAS
			return req.OperatorName == "E2E Operator" && req.Account == "e2e"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	hostSess := make(chan *supportlink.Session, 1)
	got := make(chan string, 16)
	go func() {
		for {
			s, err := host.WaitForOperator(ctx)
			if err != nil {
				return
			}
			hostSess <- s
			fakeEngine(s, got)
		}
	}()

	// The Desktop side: its hub with the real dial, its HTTP endpoints.
	h := newSupportHub(&memSupportStore{}, supportlinkDial)
	h.userName = func() string { return "op" }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveHTTP(w, r, supOrigin) }))
	defer srv.Close()
	code, out := supPost(t, srv, "servers/save", map[string]interface{}{"label": "E2E", "url": rdv, "key": e2eOperatorKey}, supOrigin)
	if code != 200 {
		t.Fatal(code, out)
	}
	sid := out["servers"].([]interface{})[0].(map[string]interface{})["id"].(string)

	// A wrong code first: refused, and the host draws a new code.
	wrong := "000000000"
	if host.Code() == wrong {
		wrong = "111111111"
	}
	_, out = supPost(t, srv, "connect", map[string]string{"server": sid, "id": host.ID(), "code": wrong, "name": "E2E Operator"}, supOrigin)
	st := waitSupStateLong(t, srv, out["conn"].(string), "failed", 30*time.Second)
	if st["error"] != "wrong_code" {
		t.Fatalf("wrong code: %v", st)
	}
	// An unknown ID.
	unknown := "123456789"
	if host.ID() == unknown {
		unknown = "987654321"
	}
	_, out = supPost(t, srv, "connect", map[string]string{"server": sid, "id": unknown, "code": "123456789", "name": "E2E Operator"}, supOrigin)
	if st := waitSupStateLong(t, srv, out["conn"].(string), "failed", 30*time.Second); st["error"] != "unknown_id" {
		t.Fatalf("unknown id: %v", st)
	}

	t0 := time.Now()
	_, out = supPost(t, srv, "connect", map[string]string{"server": sid, "id": host.ID(), "code": host.Code(), "name": "E2E Operator"}, supOrigin)
	conn := out["conn"].(string)
	st = waitSupStateLong(t, srv, conn, "ready", 60*time.Second)
	t.Logf("accepted and linked in %v, path %v, SAS %v", time.Since(t0).Round(time.Millisecond), st["path"], st["sas"])
	if st["sas"] != approvedSAS || approvedSAS == "" {
		t.Fatalf("SAS %v, host saw %q", st["sas"], approvedSAS)
	}
	var hs *supportlink.Session
	select {
	case hs = <-hostSess:
	case <-time.After(10 * time.Second):
		t.Fatal("no host session")
	}

	// The viewer's WebSocket.
	status, c, rd := wsDial(t, srv, "/local/support/tunnel/"+st["token"].(string), supOrigin)
	if status != http.StatusSwitchingProtocols {
		t.Fatal(status)
	}
	f := readFrameWithin(t, c, rd, 10*time.Second)
	if f.op != 0x1 || !bytes.Contains(f.payload, []byte(`"type":"init"`)) || !bytes.Contains(f.payload, []byte(`"hostname":"E2E-HOST"`)) {
		t.Fatalf("init: %d %q", f.op, f.payload)
	}
	for i := 0; i < 3; i++ {
		f = readFrameWithin(t, c, rd, 10*time.Second)
		if f.op != 0x2 || len(f.payload) != 2+40000*(i+1) || f.payload[0] != 0x01 || f.payload[1] != byte(i) || f.payload[len(f.payload)-1] != byte(0x10+i) {
			t.Fatalf("frame %d: op %d len %d", i, f.op, len(f.payload))
		}
	}
	// Input from the viewer reaches the host.
	c.Write(masked(t, 0x1, `{"type":"mouse","action":"move","x":0.5,"y":0.25}`))
	c.Write(masked(t, 0x1, `{"type":"key","action":"down","key":"a","code":"KeyA"}`))
	c.Write(masked(t, 0x2, "\x20file-chunk"))
	// Text and binary frames share one ordered stream: the host reads them
	// in the order the viewer sent them, across both kinds.
	var seq []string
	for len(seq) < 3 {
		select {
		case g := <-got:
			seq = append(seq, g)
		case <-time.After(10 * time.Second):
			t.Fatalf("host got only %q", seq)
		}
	}
	if seq[0] != `1:{"type":"mouse","action":"move","x":0.5,"y":0.25}` || seq[1] != `1:{"type":"key","action":"down","key":"a","code":"KeyA"}` ||
		seq[2] != "2:\x20file-chunk" {
		t.Fatalf("host got %q", seq)
	}
	// Closing the viewer (the tab) ends the host's session.
	c.Write(masked(t, 0x8, "\x03\xe8"))
	select {
	case <-hs.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the host's session did not end")
	}
	waitLive(t, h, 0)

	// The host ends a second session: the viewer reads "host_ended".
	_, out = supPost(t, srv, "connect", map[string]string{"server": sid, "id": host.ID(), "code": host.Code(), "name": "E2E Operator"}, supOrigin)
	st = waitSupStateLong(t, srv, out["conn"].(string), "ready", 60*time.Second)
	select {
	case hs = <-hostSess:
	case <-time.After(10 * time.Second):
		t.Fatal("no second host session")
	}
	_, c2, rd2 := wsDial(t, srv, "/local/support/tunnel/"+st["token"].(string), supOrigin)
	readFrameWithin(t, c2, rd2, 10*time.Second) // init
	hs.Close()
	c2.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		hd, err := readWSHeader(rd2)
		if err != nil {
			t.Fatalf("no close frame: %v", err)
		}
		p := make([]byte, hd.length)
		if _, err := io.ReadFull(rd2, p); err != nil {
			t.Fatal(err)
		}
		if hd.opcode == 0x8 {
			if len(p) < 2 || string(p[2:]) != "host_ended" {
				t.Fatalf("close payload %q", p)
			}
			break
		}
	}
}

func waitSupStateLong(t *testing.T, srv *httptest.Server, conn, want string, d time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		_, out, _ := supGet(t, srv, "connect/"+conn)
		if out["state"] == want {
			return out
		}
		if s, _ := out["state"].(string); s == "failed" || s == "ready" || s == "cancelled" || time.Now().After(deadline) {
			t.Fatalf("state %v, want %s", out, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readFrameWithin(t *testing.T, c net.Conn, rd *bufio.Reader, d time.Duration) wsFrame {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	defer c.SetReadDeadline(time.Time{})
	hd, err := readWSHeader(rd)
	if err != nil {
		t.Fatal(err)
	}
	p := make([]byte, hd.length)
	if _, err := io.ReadFull(rd, p); err != nil {
		t.Fatal(err)
	}
	return wsFrame{hd.opcode, p}
}
