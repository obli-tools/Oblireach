package engine

// Capture in an RDP session (symptom: "Oblireach never manages to connect
// to an active session taken over RDP — it spins on 'Reconnecting' forever").
//
// An RDP session has no physical display: its screen is drawn by the RDP
// display driver, and only while the RDP client shows it. Three things follow:
//   - DXGI Desktop Duplication is often unavailable there (no DXGI output, or
//     the Remote Display Adapter) — GDI BitBlt is the path that works;
//   - the Magnification fallback (the weakest path, meant for the sign-in
//     screen of a console session) is not reliable in a remote session: its
//     callback may never fire, and every frame then fails silently;
//   - while the RDP client window is MINIMISED (or the session disconnected)
//     Windows draws nothing at all: no capture path gets a picture until the
//     window is restored. The viewer must say so instead of reconnecting.
//
// This file is PURE Go (no cgo, no build tag): the decisions are unit-tested
// with both toolchains (rdp_capture_test.go). The Windows glue is
// rdp_session_windows.go (WTS / GetSystemMetrics queries) and the helper loop
// in remote_session_windows.go.

import (
	"fmt"
	"time"
)

// WTS_INFO_CLASS WTSClientProtocolType values.
const (
	wtsProtoConsole = 0
	wtsProtoICA     = 1
	wtsProtoRDP     = 2
)

// sessionDisplayInfo is what a capture process knows about how its Windows
// session is displayed.
type sessionDisplayInfo struct {
	// remote: GetSystemMetrics(SM_REMOTESESSION) != 0, or the WTS protocol
	// type is not the console (the session is shown by a remote client).
	remote bool
	// protocol: WTSClientProtocolType (0 console, 1 ICA, 2 RDP), -1 unknown.
	protocol int
	// state: the WTS connect state name (wtsStateName), "" unknown.
	state string
}

// protocolName names a WTSClientProtocolType value for the logs.
func protocolName(p int) string {
	switch p {
	case wtsProtoConsole:
		return "console"
	case wtsProtoICA:
		return "ICA"
	case wtsProtoRDP:
		return "RDP"
	default:
		return "unknown"
	}
}

// isRemoteDisplay decides whether a session is displayed by a remote client,
// from SM_REMOTESESSION and the WTS protocol type (either is enough: some
// hosts report one and not the other — a RemoteFX / RDP-to-console session
// reports SM_REMOTESESSION, a disconnected one only its protocol).
func isRemoteDisplay(smRemote bool, protocol int) bool {
	return smRemote || protocol == wtsProtoRDP || protocol == wtsProtoICA
}

func (d sessionDisplayInfo) String() string {
	st := d.state
	if st == "" {
		st = "?"
	}
	return fmt.Sprintf("remote=%v protocol=%s state=%s", d.remote, protocolName(d.protocol), st)
}

// skipMagnification: after DXGI Desktop Duplication failed, keep the GDI
// capture instead of detouring through the Magnification API — on Windows 7 /
// 2008 R2 (no DXGI at all: GDI is THE capture there) and in a remote session
// (Magnification is not reliable there; GDI BitBlt is the documented path,
// and the native thread keeps retrying DXGI from GDI anyway). A console
// session keeps the historical order (DXGI → Magnification → GDI): there
// Magnification is what captures the sign-in screen.
func skipMagnification(remote, dxgiPossible bool) bool {
	return remote || !dxgiPossible
}

// capturePathName names the helper's single-monitor capture path for the logs
// and the viewer (capture_status.path).
func capturePathName(mosaic, mag, gdi bool) string {
	switch {
	case mosaic:
		return "mosaic"
	case mag:
		return "magnification"
	case gdi:
		return "gdi"
	default:
		return "dxgi"
	}
}

// Reasons of a capture_status no_frames message (the viewers translate them).
const (
	stallRDPMinimized    = "rdp_minimized"    // remote session Active: the RDP client window is most likely minimised
	stallRDPDisconnected = "rdp_disconnected" // remote session not Active: nothing is drawn until its user reconnects
	stallNoFrames        = "capture_stalled"  // a console session that gives no picture (driver, secure desktop...)
)

// stallReason explains a capture that gives no picture, from how the session
// is displayed.
func stallReason(d sessionDisplayInfo) string {
	if !d.remote {
		return stallNoFrames
	}
	switch d.state {
	case "Disconnected", "Idle", "Down", "Listen":
		return stallRDPDisconnected
	default:
		return stallRDPMinimized
	}
}

// Frame-stall watchdog timings.
const (
	// firstFrameStall: no frame since the stream's (last) init for this long
	// → the capture gives no picture: say so to the viewer, log why.
	firstFrameStall = 6 * time.Second
	// stallLogEvery: while stalled, a diagnostic line this often.
	stallLogEvery = 30 * time.Second
)

// frameStallWatch is the helper's "no picture" watchdog. It only judges the
// first frame after each init: DXGI legitimately delivers nothing while the
// screen does not change, so a gap mid-stream is not a stall — but a stream
// that never delivered its first picture is, whatever the capture path.
type frameStallWatch struct {
	since     time.Time // the last init (start, view change, re-init)
	gotFrame  bool      // a frame was sent since `since`
	stalled   bool      // capture_status no_frames was reported
	lastLog   time.Time
	fallback  bool // the one-shot DXGI → GDI fallback was asked for
	frames    int64
	lastError string
}

// reset starts waiting for the first frame of a new init.
func (w *frameStallWatch) reset(now time.Time) {
	w.since = now
	w.gotFrame = false
}

// frame records a frame sent to the viewer. Returns true when it ends a
// reported stall (the helper then sends capture_status ok).
func (w *frameStallWatch) frame() bool {
	w.frames++
	w.gotFrame = true
	w.lastError = ""
	if w.stalled {
		w.stalled = false
		return true
	}
	return false
}

// noteError remembers the last reason a tick produced no frame (logs).
func (w *frameStallWatch) noteError(err error) {
	if err != nil {
		w.lastError = err.Error()
	}
}

// stallAction is what the helper does on a watchdog tick.
type stallAction int

const (
	stallNone     stallAction = iota
	stallReport               // first detection: log the diagnostic, tell the viewer
	stallLogAgain             // still stalled: one more diagnostic line
)

// check judges one tick. A stall is reported once (then logged every
// stallLogEvery) until a frame ends it.
func (w *frameStallWatch) check(now time.Time) stallAction {
	if w.gotFrame || w.since.IsZero() || now.Sub(w.since) < firstFrameStall {
		return stallNone
	}
	if !w.stalled {
		w.stalled = true
		w.lastLog = now
		return stallReport
	}
	if now.Sub(w.lastLog) >= stallLogEvery {
		w.lastLog = now
		return stallLogAgain
	}
	return stallNone
}

// wantGDIFallback: on a reported stall, a remote session captured by DXGI
// gets ONE switch to GDI (the Remote Display Adapter can accept a
// duplication and then never present a frame). Returns true once.
func (w *frameStallWatch) wantGDIFallback(remote bool, path string) bool {
	if w.fallback || !remote || path != "dxgi" {
		return false
	}
	w.fallback = true
	return true
}

// captureStatusMsg is the helper → viewer capture_status message: state
// "no_frames" (reason, path, remote) or "ok".
func captureStatusMsg(state, reason, path string, remote bool) map[string]interface{} {
	m := map[string]interface{}{"type": "capture_status", "state": state}
	if reason != "" {
		m["reason"] = reason
	}
	if path != "" {
		m["path"] = path
	}
	if remote {
		m["remote"] = true
	}
	return m
}

// encoderStartPlan: which encoder the helper starts on. OpenH264 when it
// initialised, else WMF when it did, else JPEG — before, a helper whose two
// H.264 encoders both failed (Media Foundation is often missing or unusable in
// a remote / Server Core session) kept "encoding" through the uninitialised
// WMF encoder, every frame failed silently and the viewer never got a picture.
func encoderStartPlan(openH264OK, wmfOK bool) string {
	switch {
	case openH264OK:
		return "openh264"
	case wmfOK:
		return "wmf"
	default:
		return "jpeg"
	}
}

// wmfFailFallback: consecutive WMF encode errors after which the helper falls
// back to JPEG (an error is not the "0 bytes while buffering" case, which has
// its own jpegFallbackThreshold).
const wmfFailFallback = 15

// HelperExitText describes a helper process exit code for the logs and the
// error sent to the viewer (the usual NTSTATUS crash codes are named).
func HelperExitText(code uint32) string {
	switch code {
	case 0:
		return "exit code 0"
	case 1:
		return "exit code 1 (fatal error: see the helper log)"
	case 0xC0000005:
		return "exit code 0xC0000005 (access violation: crash)"
	case 0xC0000409:
		return "exit code 0xC0000409 (stack buffer overrun / fail-fast: crash)"
	case 0xC0000374:
		return "exit code 0xC0000374 (heap corruption: crash)"
	case 0xC000013A:
		return "exit code 0xC000013A (terminated: Ctrl+C / session ending)"
	case 0x40010004:
		return "exit code 0x40010004 (terminated: the session is logging off)"
	case 2:
		return "exit code 2 (Go runtime panic: see the helper log)"
	default:
		return fmt.Sprintf("exit code %d (0x%08X)", code, code)
	}
}

// HelperLogHint is where the helper of a session writes its log (for the
// service log lines that say "see the helper log").
func HelperLogHint(sessionID int, pid uint32) string {
	return fmt.Sprintf(`C:\Windows\Temp\oblireach-helper-s%d-pid%d.log`, sessionID, pid)
}

// NoPictureEndCode: the code a stream ends with when its session change could
// not be followed. A stream that NEVER delivered a picture ends for good
// (capture_unavailable, not retried): a full reconnect would only start the
// same capture again — with viewers that reset their reconnect budget on each
// init, that was an endless "Reconnecting" loop. A stream that did show the
// session keeps the retryable session_switch_timeout.
func NoPictureEndCode(framesForwarded int64) (code string, retryable bool) {
	if framesForwarded == 0 {
		return "capture_unavailable", false
	}
	return "session_switch_timeout", true
}

// CaptureUnavailableText is the English detail of capture_unavailable (the
// viewers show their own translation of the code).
func CaptureUnavailableText(sessionID int, d sessionDisplayInfo) string {
	if d.remote {
		return fmt.Sprintf("Windows session %d (%s) gave no picture: a remote session is drawn only while its client window is shown (not minimised) and connected.", sessionID, protocolName(d.protocol))
	}
	return fmt.Sprintf("Windows session %d gave no picture: the capture could not start there (see the agent logs).", sessionID)
}
