package main

import (
	"log"
	"net/http"
	"time"
)

// relayVersion is injected at build time via:
//   go build -ldflags="-X main.relayVersion=x.y.z"
// The server/VERSION file is the single source of truth — no need to edit this file.
var relayVersion = "dev"

// Server holds shared state for the relay service.
type Server struct {
	cfg        Config
	sessions   *sessionStore
	httpClient *http.Client
}

func main() {
	cfg := loadConfig()

	srv := &Server{
		cfg:      cfg,
		sessions: newSessionStore(cfg.MaxSessions),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	mux := http.NewServeMux()

	// ── Public endpoints ─────────────────────────────────────────────────────
	// Viewer WebSocket:  GET /relay/ws?role=viewer&token=<viewerToken>
	// Agent  WebSocket:  GET /relay/ws?role=agent&sessionToken=<token>
	//                        Header: X-Oblireach-ApiKey: <agentApiKey>
	mux.HandleFunc("/relay/ws", srv.handleRelay)

	// ── Internal endpoints (called by Obliance server, not exposed publicly) ─
	// POST /internal/issue-token  — mint a viewer token for a new session
	mux.HandleFunc("/internal/issue-token", srv.handleIssueToken)

	// GET  /health                — liveness + session count
	mux.HandleFunc("/health", srv.handleHealth)

	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		log.Printf("[oblireach] relay server listening on %s (TLS)", cfg.Addr)
		log.Fatal(http.ListenAndServeTLS(cfg.Addr, cfg.TLSCert, cfg.TLSKey, mux))
	} else {
		log.Printf("[oblireach] relay server listening on %s", cfg.Addr)
		log.Fatal(http.ListenAndServe(cfg.Addr, mux))
	}
}
