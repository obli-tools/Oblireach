//go:build windows

package main

// The abuse report of "Receive help" end to end on this machine (opt-in, as
// TestReceiveHelpEndToEnd: it captures the screen):
//
//	OBLIREACH_HELP_E2E=1 go test -count=1 -run TestReceiveHelpReportEndToEnd -v .
//
// A real oblireach-rdv (TLS off) with its account database (where reports
// live), the helped side driven over its HTTP routes like the page, the
// operator side through "Connect by code" (view only: nothing moves the
// pointer). The person reports with "End the session now": the session ends
// on both sides, the report is in the server's store (read back with the
// server's CLI), a second report is refused here, and leaving the end
// notice forgets the report.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReceiveHelpReportEndToEnd(t *testing.T) {
	if os.Getenv("OBLIREACH_HELP_E2E") == "" {
		t.Skip("set OBLIREACH_HELP_E2E=1 (captures the screen of this session)")
	}
	rdv, bin, dir := startRDVEnv(t, "RDV_ACCOUNTS=true")
	hub := newSupportHub(&memSupportStore{}, supportlinkDial)
	hub.userName = func() string { return "op" }
	help := newHelpHost(hub.settings, &helpLinkBackend{version: "e2e"})
	hub.help = help
	t.Cleanup(help.stop)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.serveHTTP(w, r, supOrigin) }))
	defer srv.Close()

	_, out := supPost(t, srv, "servers/save", map[string]interface{}{"label": "E2E", "url": rdv, "key": e2eOperatorKey}, supOrigin)
	var sid string
	for _, s := range out["servers"].([]interface{}) {
		if m := s.(map[string]interface{}); m["builtin"] == nil {
			sid = m["id"].(string)
		}
	}
	if st, out := supPost(t, srv, "help/start", map[string]string{"server": sid}, supOrigin); st != 200 {
		t.Fatalf("start %d %v", st, out)
	}
	st := waitHelpE2E(t, srv, "waiting", 30*time.Second)
	accepted := helpE2EAccept(srv, map[string]interface{}{})
	r := openRound(t, srv, sid, st["id"].(string), st["code"].(string))
	if <-accepted == nil {
		t.Fatal("no acceptance request")
	}
	st = waitHelpE2E(t, srv, "session", 10*time.Second)
	rep, _ := st["report"].(map[string]interface{})
	if rep == nil || rep["available"] != true || rep["sent"] != false {
		t.Fatalf("report not offered during the session: %v", st["report"])
	}

	t0 := time.Now()
	code, res := supPost(t, srv, "help/report", map[string]interface{}{"reason": "scam", "text": "E2E: the helper asked for my card number", "end": true}, supOrigin)
	if code != 200 || res["ok"] != true {
		t.Fatalf("report: %d %v", code, res)
	}
	t.Logf("report sent in %v", time.Since(t0).Round(time.Millisecond))
	if reason := r.viewer.waitClose(t, 15*time.Second); reason == "" {
		t.Error("the operator side did not close")
	} else {
		t.Logf("operator side closed (%s) %v after the report", reason, time.Since(t0).Round(time.Millisecond))
	}
	st = waitHelpE2EEnded(t, srv, 10*time.Second)
	if e, _ := st["ended"].(map[string]interface{}); e == nil || e["reason"] != "local" {
		t.Errorf("ended %v, want local", st["ended"])
	}
	if rep, _ := st["report"].(map[string]interface{}); rep == nil || rep["sent"] != true {
		t.Errorf("report state after the end: %v", st["report"])
	}
	if _, res := supPost(t, srv, "help/report", map[string]interface{}{"reason": "other", "text": "", "end": false}, supOrigin); res["ok"] != false || res["kind"] != "unavailable" {
		t.Errorf("second report: %v", res)
	}

	cmd := exec.Command(bin, "report", "list")
	cmd.Env = append(os.Environ(), "RDV_DATA_DIR="+dir)
	listing, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("report list: %v\n%s", err, listing)
	}
	t.Logf("server store:\n%s", listing)
	for _, want := range []string{"scam", "key:e2e", "card number", "1 report(s)"} {
		if !strings.Contains(string(listing), want) {
			t.Errorf("report list lacks %q", want)
		}
	}

	supPost(t, srv, "help/ack", map[string]string{}, supOrigin)
	if st := helpE2EState(t, srv); st["report"] != nil {
		t.Errorf("report kept after the end notice: %v", st["report"])
	}
	supPost(t, srv, "help/stop", map[string]string{}, supOrigin)
}
