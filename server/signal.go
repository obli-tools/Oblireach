package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// handleHealth returns a simple liveness check.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"version":  relayVersion,
		"sessions": s.sessions.count(),
	})
}

// handleIssueToken is called by the Obliance server to obtain a short-lived
// viewer token for a given session.
//
// POST /internal/issue-token
//
//	Body: { "sessionToken": "<hex>", "ttlSeconds": 3600 }
//	Header: X-Internal-Secret: <cfg.RelaySecret>
//
// Returns: { "viewerToken": "<sessionToken>.<expireUnix>.<hmac>" }
//
// Obliance sends this token to the browser; the browser presents it when
// opening the relay WebSocket.  The relay validates it locally (no DB call).
func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Internal-Secret") != s.cfg.RelaySecret {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		SessionToken string `json:"sessionToken"`
		TTLSeconds   int    `json:"ttlSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SessionToken == "" {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	ttl := body.TTLSeconds
	if ttl <= 0 || ttl > 86400 {
		ttl = 3600 // default 1 h
	}

	viewerToken := issueViewerToken(body.SessionToken, ttl, s.cfg.RelaySecret)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"viewerToken": viewerToken})
}

// issueViewerToken creates a signed viewer token.
// Format: "<sessionToken>.<expireUnix>.<hmac-sha256-hex>"
func issueViewerToken(sessionToken string, ttlSeconds int, secret string) string {
	expire := strconv.FormatInt(time.Now().Add(time.Duration(ttlSeconds)*time.Second).Unix(), 10)
	sig := signHMAC(sessionToken+"."+expire, secret)
	return fmt.Sprintf("%s.%s.%s", sessionToken, expire, sig)
}

func signHMAC(data, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
