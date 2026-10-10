package engine

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIsRemoteDisplay(t *testing.T) {
	cases := []struct {
		sm    bool
		proto int
		want  bool
	}{
		{false, wtsProtoConsole, false},
		{false, -1, false},
		{true, wtsProtoConsole, true}, // SM_REMOTESESSION alone is enough
		{false, wtsProtoRDP, true},    // the protocol alone too (a disconnected RDP session)
		{false, wtsProtoICA, true},
		{true, wtsProtoRDP, true},
	}
	for _, c := range cases {
		if got := isRemoteDisplay(c.sm, c.proto); got != c.want {
			t.Errorf("isRemoteDisplay(%v, %d) = %v, want %v", c.sm, c.proto, got, c.want)
		}
	}
}

func TestProtocolName(t *testing.T) {
	for p, want := range map[int]string{0: "console", 1: "ICA", 2: "RDP", -1: "unknown", 7: "unknown"} {
		if got := protocolName(p); got != want {
			t.Errorf("protocolName(%d) = %q, want %q", p, got, want)
		}
	}
	s := sessionDisplayInfo{remote: true, protocol: 2, state: "Active"}.String()
	if !strings.Contains(s, "remote=true") || !strings.Contains(s, "protocol=RDP") || !strings.Contains(s, "state=Active") {
		t.Errorf("String() = %q", s)
	}
	if s := (sessionDisplayInfo{protocol: -1}).String(); !strings.Contains(s, "state=?") {
		t.Errorf("unknown state: %q", s)
	}
}

// After DXGI failed, a remote session keeps GDI (Magnification skipped); a
// console session keeps the historical DXGI → Magnification → GDI order;
// Windows 7 (no DXGI) always keeps GDI.
func TestSkipMagnification(t *testing.T) {
	cases := []struct {
		remote, dxgiPossible, want bool
	}{
		{false, true, false}, // console, Windows 8+: Magnification still tried (sign-in screen)
		{true, true, true},   // RDP session: GDI
		{false, false, true}, // Windows 7 console: GDI
		{true, false, true},  // Windows 7 RDP: GDI
	}
	for _, c := range cases {
		if got := skipMagnification(c.remote, c.dxgiPossible); got != c.want {
			t.Errorf("skipMagnification(remote=%v, dxgi=%v) = %v, want %v", c.remote, c.dxgiPossible, got, c.want)
		}
	}
}

func TestCapturePathName(t *testing.T) {
	cases := []struct {
		mosaic, mag, gdi bool
		want             string
	}{
		{false, false, false, "dxgi"},
		{false, false, true, "gdi"},
		{false, true, false, "magnification"},
		{true, false, false, "mosaic"},
		{true, true, true, "mosaic"},
	}
	for _, c := range cases {
		if got := capturePathName(c.mosaic, c.mag, c.gdi); got != c.want {
			t.Errorf("capturePathName(%v,%v,%v) = %q, want %q", c.mosaic, c.mag, c.gdi, got, c.want)
		}
	}
}

func TestStallReason(t *testing.T) {
	cases := []struct {
		d    sessionDisplayInfo
		want string
	}{
		{sessionDisplayInfo{remote: true, protocol: 2, state: "Active"}, stallRDPMinimized},
		{sessionDisplayInfo{remote: true, protocol: 2, state: ""}, stallRDPMinimized},
		{sessionDisplayInfo{remote: true, protocol: 2, state: "Disconnected"}, stallRDPDisconnected},
		{sessionDisplayInfo{remote: true, protocol: 2, state: "Idle"}, stallRDPDisconnected},
		{sessionDisplayInfo{remote: false, protocol: 0, state: "Active"}, stallNoFrames},
		{sessionDisplayInfo{remote: false, protocol: 0, state: "Disconnected"}, stallNoFrames},
	}
	for _, c := range cases {
		if got := stallReason(c.d); got != c.want {
			t.Errorf("stallReason(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFrameStallWatch(t *testing.T) {
	t0 := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	var w frameStallWatch

	// Not started: never stalls.
	if a := w.check(t0.Add(time.Hour)); a != stallNone {
		t.Fatalf("unstarted watch: %v", a)
	}

	w.reset(t0)
	if a := w.check(t0.Add(firstFrameStall - time.Millisecond)); a != stallNone {
		t.Fatalf("before the deadline: %v", a)
	}
	if a := w.check(t0.Add(firstFrameStall)); a != stallReport {
		t.Fatalf("at the deadline: %v, want report", a)
	}
	// Reported once, then only a log line every stallLogEvery.
	if a := w.check(t0.Add(firstFrameStall + time.Second)); a != stallNone {
		t.Fatalf("right after the report: %v", a)
	}
	if a := w.check(t0.Add(firstFrameStall + stallLogEvery)); a != stallLogAgain {
		t.Fatalf("after stallLogEvery: %v, want log again", a)
	}
	// A frame ends the stall (capture_status ok), once.
	if !w.frame() {
		t.Fatal("the first frame after a stall must report the picture back")
	}
	if w.frame() {
		t.Fatal("later frames must not report again")
	}
	if a := w.check(t0.Add(time.Hour)); a != stallNone {
		t.Fatalf("frames flowing: %v (a static DXGI screen is not a stall)", a)
	}

	// A new init re-arms the first-frame wait.
	t1 := t0.Add(2 * time.Hour)
	w.reset(t1)
	if a := w.check(t1.Add(firstFrameStall)); a != stallReport {
		t.Fatalf("after a new init without a frame: %v, want report", a)
	}
	if w.frames != 2 {
		t.Errorf("frames = %d, want 2", w.frames)
	}
}

func TestFrameStallWatchNoReportWhenFramesFlow(t *testing.T) {
	t0 := time.Now()
	var w frameStallWatch
	w.reset(t0)
	if w.frame() {
		t.Fatal("a frame without a prior stall must not report a recovery")
	}
	if a := w.check(t0.Add(10 * firstFrameStall)); a != stallNone {
		t.Fatalf("got %v", a)
	}
}

func TestFrameStallWatchErrors(t *testing.T) {
	var w frameStallWatch
	w.noteError(nil)
	if w.lastError != "" {
		t.Fatal("nil error recorded")
	}
	w.noteError(errors.New("cap_fetch failed (code -1)"))
	if w.lastError != "cap_fetch failed (code -1)" {
		t.Fatalf("lastError = %q", w.lastError)
	}
	w.frame()
	if w.lastError != "" {
		t.Fatal("a frame clears the last error")
	}
}

// The DXGI → GDI fallback is asked for once, only for a remote session
// captured by DXGI.
func TestWantGDIFallback(t *testing.T) {
	var w frameStallWatch
	if w.wantGDIFallback(false, "dxgi") {
		t.Fatal("console session: no fallback")
	}
	if w.wantGDIFallback(true, "gdi") || w.wantGDIFallback(true, "magnification") || w.wantGDIFallback(true, "mosaic") {
		t.Fatal("only from DXGI")
	}
	if !w.wantGDIFallback(true, "dxgi") {
		t.Fatal("remote + DXGI: fallback expected")
	}
	if w.wantGDIFallback(true, "dxgi") {
		t.Fatal("the fallback is one-shot")
	}
}

func TestCaptureStatusMsg(t *testing.T) {
	b, _ := json.Marshal(captureStatusMsg("no_frames", stallRDPMinimized, "gdi", true))
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "capture_status" || m["state"] != "no_frames" || m["reason"] != "rdp_minimized" || m["path"] != "gdi" || m["remote"] != true {
		t.Errorf("no_frames message = %s", b)
	}
	b, _ = json.Marshal(captureStatusMsg("ok", "", "", false))
	if string(b) != `{"state":"ok","type":"capture_status"}` {
		t.Errorf("ok message = %s", b)
	}
}

func TestEncoderStartPlan(t *testing.T) {
	cases := []struct {
		o, w bool
		want string
	}{
		{true, false, "openh264"},
		{true, true, "openh264"},
		{false, true, "wmf"},
		{false, false, "jpeg"}, // was: frames through the uninitialised WMF encoder, all dropped
	}
	for _, c := range cases {
		if got := encoderStartPlan(c.o, c.w); got != c.want {
			t.Errorf("encoderStartPlan(%v,%v) = %q, want %q", c.o, c.w, got, c.want)
		}
	}
}

func TestHelperExitText(t *testing.T) {
	cases := map[uint32]string{
		0:          "exit code 0",
		1:          "fatal error",
		2:          "panic",
		0xC0000005: "access violation",
		0xC0000409: "fail-fast",
		0x40010004: "logging off",
		0x1234:     "0x00001234",
	}
	for code, want := range cases {
		if got := HelperExitText(code); !strings.Contains(got, want) {
			t.Errorf("helperExitText(0x%x) = %q, want it to contain %q", code, got, want)
		}
	}
}

func TestHelperLogHint(t *testing.T) {
	if got := HelperLogHint(3, 4242); got != `C:\Windows\Temp\oblireach-helper-s3-pid4242.log` {
		t.Errorf("helperLogHint = %q", got)
	}
}

// A stream that never relayed a picture ends for good (no reconnect loop); one
// that did keeps the retryable session_switch_timeout.
func TestNoPictureEndCode(t *testing.T) {
	if code, retry := NoPictureEndCode(0); code != "capture_unavailable" || retry {
		t.Errorf("no picture: (%q, %v), want (capture_unavailable, false)", code, retry)
	}
	if code, retry := NoPictureEndCode(1); code != "session_switch_timeout" || !retry {
		t.Errorf("pictures relayed: (%q, %v), want (session_switch_timeout, true)", code, retry)
	}
}

func TestCaptureUnavailableText(t *testing.T) {
	r := CaptureUnavailableText(4, sessionDisplayInfo{remote: true, protocol: 2, state: "Active"})
	if !strings.Contains(r, "session 4") || !strings.Contains(r, "RDP") || !strings.Contains(r, "minimised") {
		t.Errorf("remote text = %q", r)
	}
	c := CaptureUnavailableText(1, sessionDisplayInfo{protocol: 0})
	if !strings.Contains(c, "session 1") || strings.Contains(c, "minimised") {
		t.Errorf("console text = %q", c)
	}
}
