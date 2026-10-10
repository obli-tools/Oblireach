// Package engine is the ObliReach streaming engine: screen capture (DXGI,
// GDI, Magnification and the multi-monitor mosaic on Windows; ScreenCaptureKit
// on macOS; the xdg-desktop-portal + PipeWire under Wayland, or X11, on
// Linux), the video encoders, the viewer protocol (init /
// features, binary frames 0x01..0x07, file frames 0x20 / 0x21), input
// injection, clipboard, file transfer and audio.
//
// The Oblireach agent uses it through its own transport (the relay WebSocket,
// the capture helper pipe). Other hosts run a session with RunSession over
// any FrameConn.
package engine

// Public entry point of the engine for a host that is not the agent service:
// a remote-help client runs one session over any FrameConn (a WebRTC data
// channel pair, a WebSocket, an in-memory pipe in tests) and decides what the
// viewer may do through Options.
//
// What a session runs:
//   - Windows: the same capture loop as the agent's capture helper
//     (RunCaptureLoop: DXGI / GDI / Magnification, mosaic, every encoder,
//     input, clipboard, file transfers, audio), reached through frameLink,
//     which turns its messages into viewer frames exactly like the agent
//     service relay does;
//   - macOS / Linux: the direct stream (StreamSession).
// Every frame in both directions goes through the gate (gate.go), which
// applies Options and handles chat.
//
// The engine keeps process-wide state (one screen capture, one input
// injector): one session at a time per process (ErrSessionActive).
//
// The host should call EnablePerMonitorDPIAwareness once at start, before it
// creates any window, so the capture and the injected input share one
// physical pixel space on mixed-DPI multi-monitor machines.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"unicode/utf8"
)

// FrameConn is the transport of a viewer stream: WebSocket-style frames, op
// 0x1 = JSON text, 0x2 = binary, 0x8 = close, 0x9 = ping, 0xA = pong. The
// agent's relay WebSocket (wsConn) implements it; any message-oriented
// channel (a WebRTC data channel pair, an in-memory pipe in tests) can too.
// ReadFrame is called from one goroutine; WriteFrame may be called from
// several (the implementation serialises them if it must). ReadFrame returns
// an error once the transport is closed.
type FrameConn interface {
	WriteFrame(op byte, p []byte) error
	ReadFrame() (byte, []byte, error)
	Close() error
}

// Options says what the viewer may do in a session (zero value: view only)
// and how the host hears about it. type_text (the password vault of the
// agent) and session follow are never offered through this entry point.
type Options struct {
	AllowInput        bool // mouse, keyboard, Ctrl+Alt+Del request
	AllowClipboard    bool // clipboard text in both directions (and its sync)
	AllowFiles        bool // file upload to the user's Downloads, download of copied files
	AllowBlankScreen  bool // Windows: show the local user a blank page while the viewer sees the desktop
	AllowInputBlock   bool // Windows: freeze the local keyboard / mouse (needs AllowInput)
	AllowDisplayModes bool // Windows: the viewer's resolution selector

	// OnStart runs once, when the first init message reached the viewer.
	OnStart func()
	// OnEnd runs once when the session ends: nil on a normal end (the viewer
	// left, the context was cancelled), else why it could not run.
	OnEnd func(err error)
	// OnChat receives each chat line the viewer sends ({"type":"chat"}).
	// Nil: chat is off (not advertised, viewer lines dropped, SendChat refused).
	OnChat func(text string)
	// OnFileReceived runs after a file the viewer uploaded was written to its
	// final place (path is the full path), instead of the engine's default
	// notice. Nil: the default notice (Windows: the agent's toast, which
	// re-runs the host executable with --notify-* flags, so a host that does
	// not implement those flags must set this hook).
	OnFileReceived func(path string)

	// PortalPermission (Linux under Wayland only, ignored elsewhere): where
	// the desktop's screen sharing / remote control permission is
	// remembered between sessions (xdg-desktop-portal restore token,
	// persist_mode 2), so the desktop's own dialog is not shown again. Nil:
	// the desktop asks at every session (persist_mode 0). The Oblireach
	// consent itself is the host's business and is never skipped.
	PortalPermission PortalPermissionStore
}

// PortalPermissionStore keeps one opaque string between sessions (the
// engine's encoding of the portal restore tokens). The host decides where
// (a file, sealed or not, a keyring). Load returns "" when nothing is kept;
// an error from either method only costs the remembered permission (the
// desktop asks again).
type PortalPermissionStore interface {
	Load() (string, error)
	Save(blob string) error
}

// ChatMaxBytes caps one chat line, both directions (longer lines are cut on a
// rune boundary).
const ChatMaxBytes = 4096

// featChat: init.features entry of a session whose host handles chat
// ({"type":"chat","text":...} both ways).
const featChat = "chat"

var (
	// ErrSessionActive: a session already runs in this process.
	ErrSessionActive = errors.New("engine: a session is already running in this process")
	// ErrChatDisabled: SendChat on a session without Options.OnChat.
	ErrChatDisabled = errors.New("engine: chat is not enabled for this session")
)

// sessionMu admits one session per process (the capture and the input
// injection are process-wide).
var sessionMu sync.Mutex

// Session is one remote-help session over a FrameConn.
type Session struct {
	opts Options
	g    *gate
}

// NewSession prepares a session; Run starts it.
func NewSession(conn FrameConn, opts Options) *Session {
	return &Session{opts: opts, g: newGate(conn, opts)}
}

// RunSession runs a session until the viewer leaves, the transport fails or
// ctx is cancelled. It blocks; conn is closed when it returns.
func RunSession(ctx context.Context, conn FrameConn, opts Options) error {
	return NewSession(conn, opts).Run(ctx)
}

// Run runs the session (see RunSession).
func (s *Session) Run(ctx context.Context) (err error) {
	if !sessionMu.TryLock() {
		return ErrSessionActive
	}
	defer sessionMu.Unlock()
	defer func() {
		_ = s.g.Close()
		if s.opts.OnEnd != nil {
			s.opts.OnEnd(err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil
	}
	return runSession(ctx, s.g, s.opts)
}

// SendChat sends one chat line to the viewer.
func (s *Session) SendChat(text string) error {
	if s.opts.OnChat == nil {
		return ErrChatDisabled
	}
	b, err := json.Marshal(map[string]string{"type": "chat", "text": cutUTF8(text, ChatMaxBytes)})
	if err != nil {
		return err
	}
	return s.g.WriteFrame(0x1, b)
}

// cutUTF8 returns s cut to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// captureFailedMsg is the coded error a viewer gets when the capture cannot
// start (same shape as the agent's direct path).
func captureFailedMsg(err error) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"type": "error", "code": "capture_init_failed", "retry": true,
		"message": "Screen capture unavailable: " + err.Error(),
	})
	return b
}
