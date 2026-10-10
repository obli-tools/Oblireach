package main

import (
	"regexp"
	"strings"
	"testing"
)

// jsFunc returns the source of `function name(` up to the next top-level
// `function ` (the viewer script declares its functions at column 0).
func jsFunc(t *testing.T, src, name string) string {
	t.Helper()
	i := strings.Index(src, "\nfunction "+name+"(")
	if i < 0 {
		t.Fatalf("function %s not found in the viewer script", name)
	}
	rest := src[i+1:]
	if j := strings.Index(rest[1:], "\nfunction "); j >= 0 {
		return rest[:j+1]
	}
	return rest
}

// "Reconnecting" forever on an RDP session: the viewer reset
// its reconnect budget on every init, and an agent sends its init BEFORE the
// capture gives a picture — a session that never shows anything reconnected
// endlessly at "1/8". The budget is now reset by a picture only.
func TestReachReconnectBudgetResetByPictureNotInit(t *testing.T) {
	src := reachViewerJS
	reset := regexp.MustCompile(`reconnectAttempts\s*=\s*0`)
	if code := stripJSComments(jsFunc(t, src, "reachOnInit")); reset.MatchString(code) {
		t.Error("reachOnInit must not reset the reconnect budget (an init is not a picture)")
	}
	if !reset.MatchString(jsFunc(t, src, "reachPaint")) {
		t.Error("reachPaint (a picture shown) must reset the reconnect budget")
	}
	// The budget still runs out: scheduleRemoteReconnect refuses past the
	// last delay (and the caller shows the error overlay).
	sched := jsFunc(t, src, "scheduleRemoteReconnect")
	if !strings.Contains(sched, "if (n >= delays.length)") || !strings.Contains(sched, "return false") {
		t.Error("scheduleRemoteReconnect must give up after the last delay")
	}
}

// capture_status (the agent's no-picture watchdog) is shown, translated, and
// never triggers a reconnect.
func TestReachCaptureStatusHandled(t *testing.T) {
	src := reachViewerJS
	if !strings.Contains(src, "case 'capture_status': reachOnCaptureStatus(tab, msg); return;") {
		t.Fatal("reachOnText must route capture_status")
	}
	fn := stripJSComments(jsFunc(t, src, "reachOnCaptureStatus"))
	for _, bad := range []string{"startRemote(", "scheduleRemoteReconnect(", ".close("} {
		if strings.Contains(fn, bad) {
			t.Errorf("reachOnCaptureStatus must keep the connection (found %q)", bad)
		}
	}
	for _, reason := range []string{"rdp_minimized", "rdp_disconnected", "capture_stalled"} {
		if !regexp.MustCompile(`\b` + reason + `:\s*'viewer\.capture\.`).MatchString(src) {
			t.Errorf("REACH_CAPTURE_STALL_TEXT has no text for %s", reason)
		}
	}
	if !strings.Contains(src, "capture_unavailable: 'viewer.err.captureUnavailable'") {
		t.Error("REACH_ERROR_TEXT must translate capture_unavailable")
	}
	if !regexp.MustCompile(`capture_unavailable:\s*false`).MatchString(reachProtocolJS) {
		t.Error("REACH.ERROR_RETRY: capture_unavailable must not be retried")
	}
}

// stripJSComments drops // line comments (enough for the checks above).
func stripJSComments(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
