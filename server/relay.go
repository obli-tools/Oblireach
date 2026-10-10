package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   1 << 17, // 128 KiB
	WriteBufferSize:  1 << 17,
	CheckOrigin:      func(r *http.Request) bool { return true }, // auth is token-based
}

// controlMsg is a JSON control frame sent over text WebSocket messages.
type controlMsg struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

func sendControl(conn *websocket.Conn, mu *sync.Mutex, msg controlMsg) {
	b, _ := json.Marshal(msg)
	mu.Lock()
	conn.WriteMessage(websocket.TextMessage, b) //nolint:errcheck
	mu.Unlock()
}

// handleRelay is the main WebSocket handler for both viewers and agents.
//
// Query parameters:
//
//	role=viewer  — browser viewer; authentication via "token" query param
//	              (HMAC-signed session token issued by Obliance)
//	role=agent   — agent process; authentication via X-Oblireach-ApiKey header
//	              + "sessionToken" query param (the raw Obliance session token)
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	role := PeerRole(q.Get("role"))

	if role != RoleViewer && role != RoleAgent {
		http.Error(w, `{"error":"role must be viewer or agent"}`, http.StatusBadRequest)
		return
	}

	var pairingToken string // the key used to pair viewer ↔ agent in the store

	switch role {
	case RoleViewer:
		// token = "<sessionToken>.<expire>.<hmac>" issued by Obliance
		raw := q.Get("token")
		if raw == "" {
			http.Error(w, `{"error":"missing token"}`, http.StatusUnauthorized)
			return
		}
		tok, err := validateViewerToken(raw, s.cfg)
		if err != nil {
			log.Printf("[relay] viewer auth failed: %v", err)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		pairingToken = tok

	case RoleAgent:
		apiKey := r.Header.Get("X-Oblireach-ApiKey")
		sessionToken := q.Get("sessionToken")
		if apiKey == "" || sessionToken == "" {
			http.Error(w, `{"error":"missing X-Oblireach-ApiKey or sessionToken"}`, http.StatusUnauthorized)
			return
		}
		tok, err := validateAgentSession(sessionToken, apiKey, s.cfg.OblianceURL, s.httpClient)
		if err != nil {
			log.Printf("[relay] agent auth failed: %v", err)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		pairingToken = tok
	}

	// ── WebSocket upgrade ────────────────────────────────────────────────────
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[relay] upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Per-connection write mutex (gorilla requires single-writer)
	var writeMu sync.Mutex

	// ── Session registration ─────────────────────────────────────────────────
	entry, _ := s.sessions.getOrCreate(pairingToken)
	if entry == nil {
		sendControl(conn, &writeMu, controlMsg{Type: "error", Message: "server full"})
		return
	}

	p := &peer{
		role:   role,
		sendCh: make(chan []byte, 256),
		done:   make(chan struct{}),
	}

	partnerCh, taken, ok := entry.register(p)
	if !ok {
		if taken {
			sendControl(conn, &writeMu, controlMsg{Type: "error", Message: "role already taken for this session"})
		} else {
			sendControl(conn, &writeMu, controlMsg{Type: "error", Message: "registration failed"})
		}
		return
	}

	log.Printf("[relay] %s joined session %s…", role, pairingToken[:8])

	// Notify the peer that it is connected and waiting for its partner.
	sendControl(conn, &writeMu, controlMsg{Type: "waiting"})

	// ── Wait for partner (with timeout) ─────────────────────────────────────
	pairTimeout := time.Duration(s.cfg.PairTimeoutSec) * time.Second
	timer := time.NewTimer(pairTimeout)
	defer timer.Stop()

	var partner *peer
	select {
	case partner = <-partnerCh:
		timer.Stop()
	case <-timer.C:
		s.sessions.remove(pairingToken)
		sendControl(conn, &writeMu, controlMsg{Type: "error", Message: "timed out waiting for peer"})
		log.Printf("[relay] session %s timed out (no partner arrived)", pairingToken[:8])
		return
	case <-r.Context().Done():
		s.sessions.remove(pairingToken)
		return
	}

	// Notify both sides that the tunnel is ready.
	sendControl(conn, &writeMu, controlMsg{Type: "paired"})
	log.Printf("[relay] session %s paired — tunnelling begins", pairingToken[:8])

	// ── Outbound pump: partner's frames → our connection ────────────────────
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	stopAll := func() { stopOnce.Do(func() { close(stopCh) }) }

	go func() {
		defer stopAll()
		for {
			select {
			case <-stopCh:
				return
			case frame, ok := <-partner.sendCh:
				if !ok {
					return
				}
				// Determine WebSocket message type:
				// binary frames carry raw video/audio; text frames carry JSON.
				mt := websocket.BinaryMessage
				if len(frame) > 0 && frame[0] == '{' {
					mt = websocket.TextMessage
				}
				writeMu.Lock()
				err := conn.WriteMessage(mt, frame)
				writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	// ── Inbound pump: our connection → partner's sendCh ─────────────────────
	defer func() {
		close(p.done)
		stopAll()
		s.sessions.remove(pairingToken)
		// Send a close notification to partner if it is still listening
		select {
		case partner.sendCh <- []byte(`{"type":"peer_disconnected"}`):
		default:
		}
		log.Printf("[relay] session %s: %s disconnected", pairingToken[:8], role)
	}()

	// Idle timeout reset helper
	idleTimeout := time.Duration(s.cfg.IdleTimeoutSec) * time.Second
	var idleTimer *time.Timer
	if idleTimeout > 0 {
		idleTimer = time.AfterFunc(idleTimeout, func() {
			conn.Close()
		})
	}
	resetIdle := func() {
		if idleTimer != nil {
			idleTimer.Reset(idleTimeout)
		}
	}

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		resetIdle()

		// Intercept ping/pong so we don't forward them to the partner.
		if mt == websocket.TextMessage {
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &msg) == nil {
				if msg.Type == "ping" {
					writeMu.Lock()
					b, _ := json.Marshal(controlMsg{Type: "pong"})
					conn.WriteMessage(websocket.TextMessage, b) //nolint:errcheck
					writeMu.Unlock()
					continue
				}
			}
		}

		select {
		case partner.sendCh <- data:
		default:
			// Partner's send buffer full — drop frame (prefer video frame loss
			// over blocking the reader and stalling the whole connection).
		}
	}
}
