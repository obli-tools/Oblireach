package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/oblireach/agent/engine"
)

// Agent side of the direct stream (stream.go holds the streaming engine):
// relay dial, session resolution and the coded failure report over the
// command channel.

// The relay WebSocket is the engine's transport for the direct path and the
// supervisor.
var _ engine.FrameConn = (*wsConn)(nil)

// activeStreams maps a stream token to its session (the direct path and the
// cross-session supervisor), so close_remote_tunnel can stop it.
var activeStreams sync.Map // token → *StreamSession

// agentStreamHooks wires a StreamSession to the agent: a coded failure goes to
// the server over the command WebSocket (tunnel_failed), and a stopped
// session leaves the token registry.
func agentStreamHooks(token string) engine.StreamHooks {
	return engine.StreamHooks{
		Failed:  func(code, message string) { reportTunnelFailed(token, code, message) },
		Stopped: func() { activeStreams.Delete(token) },
	}
}

// notifyStreamOpen tells the user of the session actually shown that an
// operator is connected. It runs after resolveTargetSession, so the toast goes
// to the session that is streamed (the recent-console safety net may map the
// requested id to the current console) and none is shown when the open is
// refused.
func notifyStreamOpen(sessionID int, operator string) {
	if operator == "" {
		return
	}
	go notifySession(sessionID, operator, true)
}

// dialStreamWS opens the relay WebSocket for the given token.
func dialStreamWS(cfg *Config, token string) (*wsConn, error) {
	base := strings.TrimRight(cfg.ServerURL, "/")
	var wsBase string
	switch {
	case strings.HasPrefix(base, "https://"):
		wsBase = "wss://" + base[8:]
	case strings.HasPrefix(base, "http://"):
		wsBase = "ws://" + base[7:]
	default:
		wsBase = base
	}
	wsURL := wsBase + "/api/remote/agent-tunnel/" + token
	log.Printf("Stream %s: connecting to %s", token, wsURL)
	return wsConnect(wsURL, http.Header{"X-Api-Key": []string{cfg.APIKey}})
}

// startStream opens a remote-control session.
// sessionID < 0 means "use the console session".
// On Windows, if the target session differs from this process's session,
// a helper subprocess is spawned in that session.
// files: the operator may transfer files (files_gate.go); the capture helper
// is started with --no-files otherwise. The direct capture path below has no
// file transfer at all.
func startStream(cfg *Config, token string, sessionID int, operator string, files bool) error {
	targetSession := sessionID
	if runtime.GOOS == "windows" {
		t, code, msg := resolveTargetSession(sessionID)
		if code != "" {
			// Said to the operator (coded, over the command WS)
			// instead of a helper that dies without a picture.
			reportTunnelFailed(token, code, msg)
			return fmt.Errorf("stream %s: %s: %s", token, code, msg)
		}
		targetSession = t
	} else if targetSession < 0 {
		targetSession = engine.FindCaptureSession()
	}

	// Cross-session on Windows: spawn capture helper.
	// Always use cross-session when running as a service (session 0),
	// even if the target is also session 0 — session 0 has no display,
	// so the helper must run in an interactive session for capture to work.
	if runtime.GOOS == "windows" {
		mySession := engine.CurrentSessionID()
		if targetSession != mySession || mySession == 0 {
			// If target is session 0 (fallback), try the console session
			// which exists even at the login screen (Winlogon desktop).
			if targetSession == 0 {
				consoleID := engine.ConsoleSessionID()
				if uint32(consoleID) != 0xFFFFFFFF {
					targetSession = consoleID
				}
			}
			notifyStreamOpen(targetSession, operator)
			// The follow supervisor keeps the relay across session changes:
			// the request decides console vs pinned — an explicit id that did
			// not survive resolution (the recent-console safety net) follows
			// the console (followRequestFor) — and the resolved targetSession
			// is where the first leg spawns.
			return startCrossSessionStream(cfg, token, followRequestFor(sessionID, targetSession), targetSession, operator, files)
		}
	}
	notifyStreamOpen(targetSession, operator)

	// Direct capture path (same session or non-Windows).
	ws, err := dialStreamWS(cfg, token)
	if err != nil {
		reportTunnelFailed(token, "internal", "could not reach the relay: "+err.Error())
		return fmt.Errorf("stream %s: WS connect failed: %w", token, err)
	}

	session := engine.NewStreamSession(token, ws, agentStreamHooks(token))
	activeStreams.Store(token, session)
	go session.Run()
	return nil
}

func stopStream(token string) {
	if v, ok := activeStreams.Load(token); ok {
		v.(*engine.StreamSession).Stop()
	}
}

// recentConsoleIDs remembers which session ids were the console recently, so a
// viewer reconnecting after a console sign-in (which gave the console a new id)
// can be resolved to the current console instead of "session not found".
var recentConsoleIDs struct {
	mu  sync.Mutex
	ids map[int]time.Time
}

const recentConsoleWindow = 10 * time.Minute

func noteConsoleID(id int) {
	if id <= 0 {
		return
	}
	recentConsoleIDs.mu.Lock()
	defer recentConsoleIDs.mu.Unlock()
	if recentConsoleIDs.ids == nil {
		recentConsoleIDs.ids = map[int]time.Time{}
	}
	now := time.Now()
	recentConsoleIDs.ids[id] = now
	for k, t := range recentConsoleIDs.ids {
		if now.Sub(t) > recentConsoleWindow {
			delete(recentConsoleIDs.ids, k)
		}
	}
}

func wasRecentConsole(id int) bool {
	recentConsoleIDs.mu.Lock()
	defer recentConsoleIDs.mu.Unlock()
	t, ok := recentConsoleIDs.ids[id]
	return ok && time.Since(t) <= recentConsoleWindow
}

// consoleUsable reports whether the console session can be captured now: not the
// tear-down "Disconnected" state. A console not in the enumeration (a bare login
// screen that EnumerateSessions filtered) is treated as usable.
func consoleUsable(id int) bool {
	for _, s := range engine.EnumerateSessions() {
		if s.ID == id {
			return s.State != "Disconnected"
		}
	}
	return true
}

// stableConsoleID returns the console session id only when it is the same across
// two quick samples and usable — during a re-attach the id flips for a moment
// onto a session being torn down. Returns -1 while it is not stable.
func stableConsoleID() int {
	c1 := engine.ConsoleSessionID()
	if uint32(c1) == 0xFFFFFFFF || c1 <= 0 {
		return -1
	}
	time.Sleep(150 * time.Millisecond)
	if c2 := engine.ConsoleSessionID(); c1 != c2 {
		return -1
	}
	if !consoleUsable(c1) {
		return -1
	}
	return c1
}

// consoleWaitMax bounds the wait for a console session while Windows moves a
// session onto the console (a sign-in on the console that reconnects the
// user's disconnected session: for a moment the console has no session).
const consoleWaitMax = 6 * time.Second

// resolveTargetSession picks / checks the Windows session a stream is asked
// for. sessionID < 0 or 0 = the console (else the first active
// user session), waiting up to consoleWaitMax while the console is being
// reassigned — the viewer's automatic reconnect after a sign-in on the
// console lands right in that moment. A named session that no longer exists
// (signed out, or merged into the console by that sign-in) or that is
// disconnected (its user closed the RDP window: nothing is drawn, there is no
// screen to capture) is refused with a code the viewer explains and offers
// "pick another session" for. Returns (session, "", "") when usable.
func resolveTargetSession(sessionID int) (int, string, string) {
	if sessionID <= 0 {
		deadline := time.Now().Add(consoleWaitMax)
		for {
			// Wait until the console id is stable AND its session is
			// Active/Connected before picking it: during a re-attach the console
			// id flips for a moment onto a session that is being torn down.
			if c := stableConsoleID(); c > 0 {
				noteConsoleID(c)
				return c, "", ""
			}
			for _, s := range engine.EnumerateSessions() {
				if s.State == "Active" && s.Username != "" {
					return s.ID, "", ""
				}
			}
			if time.Now().After(deadline) {
				return engine.FindCaptureSession(), "", ""
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	sessions := engine.EnumerateSessions()
	if len(sessions) == 0 {
		return sessionID, "", "" // enumeration failed: try as before
	}
	for _, s := range sessions {
		if s.ID != sessionID {
			continue
		}
		if s.State == "Disconnected" {
			who := ""
			if s.Username != "" {
				who = " (" + s.Username + ")"
			}
			return sessionID, "session_no_display", fmt.Sprintf(
				"Windows session %d%s is disconnected: it has no screen until its user reconnects. Pick the console or an active session.", sessionID, who)
		}
		return sessionID, "", ""
	}
	// The requested session vanished. A viewer reconnecting after a console
	// sign-in (which merged the old session into the console and gave it a new
	// id) asked for that old id: if it was the console in the last 10 minutes,
	// resolve to the current console instead of refusing (safety net for any
	// viewer that still does a full reconnect).
	if wasRecentConsole(sessionID) {
		if c := engine.ConsoleSessionID(); uint32(c) != 0xFFFFFFFF && c > 0 {
			noteConsoleID(c)
			return c, "", ""
		}
	}
	return sessionID, "session_not_found", fmt.Sprintf(
		"Windows session %d no longer exists (signed out, or moved to the console by a sign-in). Reconnect to the console or pick another session.", sessionID)
}

// reportTunnelFailed tells the server, over the command WS, that a session
// could not be opened (otherwise CMD/PS/ObliReach silently "don't launch").
// The server (oblireachHub._handleTunnelFailed → remoteService.failSessionFromAgent)
// then fails the session and pushes a coded error to the viewer, instead of the
// viewer waiting out its 45 s launch timeout. Best-effort; the token is the
// relay session token from the open_remote_tunnel command.
func reportTunnelFailed(token, code, message string) {
	if token == "" {
		return
	}
	msg, _ := json.Marshal(map[string]interface{}{
		"type": "tunnel_failed", "sessionToken": token, "code": code, "message": message,
	})
	if err := cmdWSSend(msg); err != nil {
		log.Printf("Stream %s: could not report tunnel_failed (%s): %v", token, code, err)
	}
}
