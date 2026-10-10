package main

// rdphost_e2e_test.go — the adapter against the REAL helper and a real RDP
// server (skipped unless OBLIREACH_RDPHOST_E2E=<host:port> and
// OBLIREACH_RDPHOST=<absolute path of rdphost> are set; the build host's
// throw-away bench runs it against Weston's RDP backend): the certificate
// question, remembered, then not asked again; the H.264 stream; input and a
// resize reach the helper; the end.

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRdpHostE2E(t *testing.T) {
	target := os.Getenv("OBLIREACH_RDPHOST_E2E")
	if target == "" || os.Getenv("OBLIREACH_RDPHOST") == "" {
		t.Skip("set OBLIREACH_RDPHOST_E2E=<host:port> and OBLIREACH_RDPHOST=<rdphost>")
	}
	host, ps, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(ps)
	old := rdpHelperOverride
	rdpHelperOverride = ""
	defer func() { rdpHelperOverride = old }()
	path, err := rdpHelperPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := rdpHelperCheck(path); err != nil {
		t.Fatalf("helper %s: %v", path, err)
	}
	_, srv, reg := rdpTestServer(t)
	known := &rdpKnownHosts{path: filepath.Join(t.TempDir(), rdpKnownHostsFile)}
	session := func(expectCert bool) {
		s, err := reg.start(rdpConnectReq{Host: host, Port: port, Username: os.Getenv("OBLIREACH_RDPHOST_E2E_USER"), Password: os.Getenv("OBLIREACH_RDPHOST_E2E_PASS"),
			Width: 1024, Height: 768, Scale: 100, Layout: "0000040C"}, nil, known)
		if err != nil {
			t.Fatal(err)
		}
		defer s.helper.kill()
		tok, _ := reg.issueTunnel(s)
		code, c, rd := wsDial(t, srv, "/local/rdp-tunnel/"+tok, "http://127.0.0.1:5555")
		if code != http.StatusSwitchingProtocols {
			t.Fatalf("tunnel %d", code)
		}
		deadline := time.Now().Add(40 * time.Second)
		gotInit, frames, certAsked := false, 0, false
		for time.Now().Before(deadline) && (!gotInit || frames < 1) {
			c.SetReadDeadline(time.Now().Add(20 * time.Second))
			h, err := readWSHeader(rd)
			if err != nil {
				t.Fatalf("read: %v (helper: %s)", err, s.helper.tail.String())
			}
			p := make([]byte, h.length)
			if _, err := readFull(rd, p); err != nil {
				t.Fatal(err)
			}
			switch h.opcode {
			case 0x2:
				if len(p) > 5 && p[0] == 0x02 {
					frames++
				}
			case 0x8:
				t.Fatalf("closed (helper: %s)", s.helper.tail.String())
			case 0x1:
				t.Logf("page <- %s", truncate(string(p), 200))
				switch {
				case hasType(p, "rdp_cert"):
					certAsked = true
					c.Write(masked(t, 0x1, `{"type":"rdp_cert_reply","accept":true,"remember":true}`))
				case hasType(p, "rdp_credentials"):
					c.Write(masked(t, 0x1, `{"type":"rdp_credentials","cancel":true}`))
				case hasType(p, "init"):
					gotInit = true
				case hasType(p, "error"):
					t.Fatalf("error: %s (helper: %s)", p, s.helper.tail.String())
				}
			}
		}
		if !gotInit || frames < 1 {
			t.Fatalf("no picture: init %v frames %d", gotInit, frames)
		}
		if certAsked != expectCert {
			t.Fatalf("certificate asked: %v, want %v", certAsked, expectCert)
		}
		// input and a resize, then the end
		c.Write(masked(t, 0x1, `{"type":"mouse","action":"move","x":100,"y":100}`))
		c.Write(masked(t, 0x1, `{"type":"key","action":"down","code":"KeyA","key":"a"}`))
		c.Write(masked(t, 0x1, `{"type":"key","action":"up","code":"KeyA","key":"a"}`))
		c.Write(masked(t, 0x1, `{"type":"rdp_resize","width":1280,"height":800,"dpr":1}`))
		time.Sleep(2 * time.Second)
		s.end()
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			t.Fatal("did not end")
		}
		t.Logf("helper exit: %v; stderr tail:\n%s", s.helper.exit, s.helper.tail.String())
	}
	session(true)
	if k := known.known(rdpHostKey(host, port)); len(k) != 1 {
		t.Fatalf("not remembered: %v", k)
	}
	session(false) // (trusted now: no question)
}

func readFull(rd io.Reader, p []byte) (int, error) { return io.ReadFull(rd, p) }

func hasType(p []byte, typ string) bool { return strings.Contains(string(p), `"type":"`+typ+`"`) }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
