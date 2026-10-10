package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/oblireach/agent/engine"
)

// ── Command WS send ─────────────────────────────────────────────────────────
// Exposes the active command WS for sending agent→server messages (used by chat).

var (
	cmdWSActive   *wsConn
	cmdWSActiveMu sync.Mutex
)

// heartbeatNowCh asks the command-WS loop to send a heartbeat right away
// (buffered: one pending is enough). The follow supervisor triggers it after a
// session switch so the server's session list is fresh without waiting 30 s.
var heartbeatNowCh = make(chan struct{}, 1)

// requestHeartbeatNow schedules an immediate heartbeat (best-effort).
func requestHeartbeatNow() {
	select {
	case heartbeatNowCh <- struct{}{}:
	default:
	}
}

// cmdWSReconnectCh asks the command-WS loop to drop the connection and redial
// right away (identity v2: the device ID changed, the server must see the
// new ?uuid=). Buffered: one pending request is enough.
var cmdWSReconnectCh = make(chan struct{}, 1)

// reconnectCmdWS schedules a redial of the command WebSocket (best-effort).
func reconnectCmdWS() {
	select {
	case cmdWSReconnectCh <- struct{}{}:
	default:
	}
}

func cmdWSSend(payload []byte) error {
	cmdWSActiveMu.Lock()
	ws := cmdWSActive
	cmdWSActiveMu.Unlock()
	if ws == nil {
		return fmt.Errorf("command WS not connected")
	}
	return ws.WriteFrame(0x1, payload)
}

// ── Timing constants ──────────────────────────────────────────────────────────

const (
	// cmdWSHeartbeatInterval: how often we send the lightweight heartbeat.
	// 30 s matches the old push interval — keeps last_seen_at fresh and
	// delivers fresh session/OS info without flooding the server.
	cmdWSHeartbeatInterval = 30 * time.Second

	// cmdWSReadTimeout: maximum time to wait for any frame (message or server ping).
	// Server sends pings every 15 s, so 3 missed pings = 45 s; we use 60 s to
	// give one extra cycle before declaring the connection dead.
	cmdWSReadTimeout = 60 * time.Second

	// Reconnect backoff: starts at 2 s, grows ×1.5 each failure, caps at 60 s.
	cmdWSReconnectBase = 2 * time.Second
	cmdWSReconnectMax  = 60 * time.Second
)

// ── Message types ─────────────────────────────────────────────────────────────

// cmdHeartbeatMsg is the periodic status payload sent agent → server.
// Kept intentionally lightweight (no full software inventory).
type cmdHeartbeatMsg struct {
	Type       string `json:"type"`       // always "heartbeat"
	DeviceUUID string `json:"deviceUuid"` // currentDeviceUUID()
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Version    string `json:"version"`
	// Build line of this binary: "modern" (Go 1.22+, Windows 10+ / macOS /
	// Linux) or "nt6" (Go 1.20, Windows 7 SP1 / 8.1 / 2008 R2 - 2012 R2,
	// oblireach-agent-nt6.msi). See build_flavor.go.
	Flavor string `json:"flavor,omitempty"`
	// Real Windows version "major.minor.build" (RtlGetVersion), e.g.
	// "6.1.7601"; empty off Windows.
	OSBuild  string        `json:"osBuild,omitempty"`
	Sessions []engine.SessionInfo `json:"sessions,omitempty"` // active WTS sessions (Windows)
	// Self-update state (attempt in progress, last result, next attempt):
	// logged by the server and shown on the device page.
	SelfUpdate *selfUpdateStatus `json:"selfUpdate,omitempty"`
	// Microsoft Edge WebView2 runtime of the chat window (Windows only):
	// state, version, install attempts. The server decides from it whether
	// to send `webview2_install`; the device page shows it.
	WebView2 *webview2Status `json:"webview2,omitempty"`
}

// ── Public entry point ────────────────────────────────────────────────────────

// runCmdWS replaces runPushLoop as the agent's main loop.
// It opens a persistent WebSocket command channel to the server and
// auto-reconnects with exponential backoff whenever the connection drops.
//
// Heartbeats are sent every 30 s (same cadence as the old HTTP push).
// Commands (open_remote_tunnel, update, …) are delivered instantly by the
// server over the same connection; no polling required.
func runCmdWS(cfg *Config) {
	backoff := cmdWSReconnectBase

	for {
		err := cmdWSSession(cfg)
		if err == nil {
			// Clean server-side close (e.g. server restart / graceful shutdown).
			// Reconnect quickly — the server is likely coming back.
			log.Printf("Command WS: clean close — reconnecting in %s", cmdWSReconnectBase)
			backoff = cmdWSReconnectBase
		} else {
			log.Printf("Command WS: %v — reconnecting in %s", err, backoff)
			// Advance backoff for next iteration (×1.5, capped at max)
			next := time.Duration(float64(backoff) * 1.5)
			if next > cmdWSReconnectMax {
				next = cmdWSReconnectMax
			}
			backoff = next
		}
		time.Sleep(backoff)
	}
}

// ── Session ───────────────────────────────────────────────────────────────────

// cmdWSSession runs one WS session from dial to close.
// Returns nil on a clean WebSocket close frame, non-nil on any error.
func cmdWSSession(cfg *Config) error {
	// Build wss:// URL, appending ?uuid= so the server can identify the device
	// at handshake time without waiting for the first heartbeat message.
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
	// A reconnect requested before this dial is already honoured by it
	// (currentDeviceUUID is switched before reconnectCmdWS is called).
	select {
	case <-cmdWSReconnectCh:
	default:
	}
	wsURL := wsBase + "/api/oblireach/ws?uuid=" + url.QueryEscape(currentDeviceUUID())

	wsHeaders := http.Header{"X-Api-Key": []string{cfg.APIKey}}
	// Identity v2: announce the pending switch to the shared device ID.
	applyIdentityHeaders(wsHeaders)
	ws, err := wsConnect(wsURL, wsHeaders)
	if err != nil {
		return fmt.Errorf("connect %s: %w", wsBase, err)
	}
	defer ws.Close()

	cmdWSActiveMu.Lock()
	cmdWSActive = ws
	cmdWSActiveMu.Unlock()
	defer func() {
		cmdWSActiveMu.Lock()
		cmdWSActive = nil
		cmdWSActiveMu.Unlock()
	}()

	log.Printf("Command WS: connected to %s", wsBase)

	// Send the first heartbeat immediately — this registers/updates the device
	// record in the DB and triggers delivery of any offline-queued command.
	if err := sendCmdHeartbeat(ws, cfg); err != nil {
		return fmt.Errorf("initial heartbeat: %w", err)
	}

	// Periodic heartbeat ticker
	hbTicker := time.NewTicker(cmdWSHeartbeatInterval)
	defer hbTicker.Stop()

	// Set initial read deadline; reset on every received frame so any activity
	// (server ping, command) keeps the connection alive.
	if err := ws.conn.SetReadDeadline(time.Now().Add(cmdWSReadTimeout)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}

	// Read frames in a background goroutine and forward them to a channel.
	// This lets us select{} on both the ticker and incoming frames without
	// blocking the ticker when the connection is idle.
	type wsFrame struct {
		opcode  byte
		payload []byte
		err     error
	}
	frameCh := make(chan wsFrame, 8)
	go func() {
		for {
			op, pay, err := ws.ReadFrame()
			frameCh <- wsFrame{op, pay, err}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {

		// ── Periodic heartbeat ────────────────────────────────────────────────
		case <-hbTicker.C:
			if err := sendCmdHeartbeat(ws, cfg); err != nil {
				return fmt.Errorf("heartbeat send: %w", err)
			}

		// ── Immediate heartbeat (follow supervisor after a session switch) ────
		case <-heartbeatNowCh:
			if err := sendCmdHeartbeat(ws, cfg); err != nil {
				return fmt.Errorf("heartbeat send: %w", err)
			}

		// ── Device ID switched (identity v2): redial with ?uuid=<new> ────────
		case <-cmdWSReconnectCh:
			log.Printf("Command WS: device ID is now %s — reconnecting", currentDeviceUUID())
			return nil

		// ── Incoming frame ────────────────────────────────────────────────────
		case f := <-frameCh:
			if f.err != nil {
				return fmt.Errorf("read: %w", f.err)
			}

			// Any received frame resets the inactivity deadline.
			_ = ws.conn.SetReadDeadline(time.Now().Add(cmdWSReadTimeout))

			switch f.opcode {
			case 0x8: // close — server initiated graceful close
				return nil

			case 0x9: // ping from server — reply with pong
				_ = ws.SendPong(f.payload)

			case 0xA: // pong — ignore (we also send pings in stream.go, not here)

			case 0x1: // text frame — command JSON from server
				var cmd command
				if err := json.Unmarshal(f.payload, &cmd); err != nil {
					log.Printf("Command WS: malformed JSON: %v", err)
					continue
				}
				// Identity v2: the Obliance server accepted the switch to the
				// shared device ID (sent on this channel: the agent no longer
				// pushes over HTTP). Switching redials with the new ?uuid=.
				if cmd.Type == "identity_ack" {
					if id, _ := cmd.Payload["deviceId"].(string); id != "" {
						acceptIdentityAck(id)
					}
					continue
				}
				log.Printf("Command WS: received command type=%s id=%s", cmd.Type, cmd.ID)
				// Execute asynchronously so the read loop never blocks on
				// long-running commands (e.g. screen-capture setup).
				go handleCommand(cfg, &cmd)
			}
		}
	}
}

// ── Heartbeat ─────────────────────────────────────────────────────────────────

// sendCmdHeartbeat marshals and sends the current device state over the WS.
func sendCmdHeartbeat(ws *wsConn, cfg *Config) error {
	hostname, _ := os.Hostname()
	msg := cmdHeartbeatMsg{
		Type:       "heartbeat",
		DeviceUUID: currentDeviceUUID(),
		Hostname:   hostname,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Version:    agentVersion,
		Flavor:     agentFlavorName(),
		OSBuild:    engine.WindowsVersion().String(),
		Sessions:   engine.EnumerateSessions(),
		SelfUpdate: selfUpdate.status(),
		WebView2:   webview2HeartbeatStatus(),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal heartbeat: %w", err)
	}
	return ws.WriteFrame(0x1, data)
}
