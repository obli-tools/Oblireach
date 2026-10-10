//go:build windows

package main

// End-to-end test of the abuse report on a real machine: a local
// oblireach-rdv with its account database (where reports live), the thin
// client's controller with the production backend (supportlink + the
// engine capturing the real screen, view only: nothing moves the pointer),
// and an operator on a static key. The person reports during the session
// with "End the session now": the session ends on both sides and the report
// is in the server's store (read back with the server's CLI).
//
//	SUPPORT_E2E=1 go test -ldflags "-s -w" -run TestEndToEndReport -v .

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

func TestEndToEndReport(t *testing.T) {
	if os.Getenv("SUPPORT_E2E") != "1" {
		t.Skip("set SUPPORT_E2E=1 (captures the real screen)")
	}
	configureEngine()
	server, bin, dir := startRDVEnv(t, "RDV_ACCOUNTS=true")
	view := &autoView{perms: Permissions{}, states: make(chan State, 1024)}
	c := NewController(&linkBackend{server: server, version: "e2e"}, view, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	w := view.wait(t, PhaseWaiting, nil)
	cctx, ccancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ccancel()
	s, err := supportlink.Connect(cctx, supportlink.OperatorConfig{Server: server, Key: e2eKey, Name: "E2E Operator"}, w.ID, w.Code)
	if err != nil {
		t.Fatalf("operator connect: %v", err)
	}
	defer s.Close()
	if s.ReportToken() != "" {
		t.Fatal("the operator side has a report token")
	}
	go func() {
		for {
			if _, _, err := s.ReadFrame(); err != nil {
				return
			}
		}
	}()
	st := view.wait(t, PhaseSession, nil)
	if !st.Report.Available || !st.Report.Open(time.Now()) {
		t.Fatalf("report not offered during the session: %+v", st.Report)
	}

	kinds := make(chan string, 1)
	t0 := time.Now()
	text := "E2E: the helper asked for my card number"
	if err := c.SubmitReport(ReportForm{Reason: wire.ReportScam, Text: text, EndSession: true}, func(k string) { kinds <- k }); err != nil {
		t.Fatal(err)
	}
	end := view.wait(t, PhaseEnded, nil)
	if end.Ended.Reason != EndLocal {
		t.Errorf("end reason %d, want EndLocal", end.Ended.Reason)
	}
	select {
	case <-s.Done():
		t.Logf("operator session ended %s after the report", time.Since(t0).Round(time.Millisecond))
	case <-time.After(10 * time.Second):
		t.Fatal("the operator's session did not end")
	}
	select {
	case k := <-kinds:
		if k != "" {
			t.Fatalf("report not sent: %s", k)
		}
		t.Logf("report sent in %s", time.Since(t0).Round(time.Millisecond))
	case <-time.After(30 * time.Second):
		t.Fatal("no report result")
	}
	if err := c.SubmitReport(ReportForm{Reason: wire.ReportOther}, nil); !errors.Is(err, ErrReportUnavailable) {
		t.Errorf("second report: %v", err)
	}

	// The report is in the server's store, bound to the session.
	cmd := exec.Command(bin, "report", "list")
	cmd.Env = append(os.Environ(), "RDV_DATA_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("report list: %v\n%s", err, out)
	}
	t.Logf("server store:\n%s", out)
	for _, want := range []string{wire.TruncSID(s.SID()), "scam", "key:helpdesk", "card number", "1 report(s)"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("report list lacks %q", want)
		}
	}

	c.Acknowledge()
	if w2 := view.wait(t, PhaseWaiting, nil); w2.Report != (ReportInfo{}) {
		t.Errorf("report info kept after the end screen: %+v", w2.Report)
	}
}
