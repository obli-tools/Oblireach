package main

import (
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// Listen address for the relay server (e.g. ":7900")
	Addr string

	// Base URL of the Obliance API server — used to validate sessions
	// e.g. "https://obliance.example.com"
	OblianceURL string

	// Shared secret between Obliance and this relay.
	// Obliance signs a short-lived HMAC-SHA256 token embedded in every
	// session object it issues; the relay verifies this locally without
	// any extra round-trip.  Must match OBLIREACH_SECRET in Obliance .env.
	RelaySecret string

	// Maximum number of concurrent sessions the relay will handle.
	MaxSessions int

	// Inactivity timeout for a session whose second peer has not yet
	// connected (seconds).
	PairTimeoutSec int

	// Inactivity timeout for an established session with no traffic
	// (seconds).  0 = disabled.
	IdleTimeoutSec int

	// TLS certificate / key paths.  Both must be set to enable TLS.
	TLSCert string
	TLSKey  string
}

func loadConfig() Config {
	cfg := Config{
		Addr:           envStr("OBLIREACH_ADDR", ":7900"),
		OblianceURL:    envStr("OBLIANCE_URL", "http://localhost:3001"),
		RelaySecret:    envStr("OBLIREACH_SECRET", "change-me-in-production"),
		MaxSessions:    envInt("OBLIREACH_MAX_SESSIONS", 500),
		PairTimeoutSec: envInt("OBLIREACH_PAIR_TIMEOUT", 60),
		IdleTimeoutSec: envInt("OBLIREACH_IDLE_TIMEOUT", 3600),
		TLSCert:        envStr("OBLIREACH_TLS_CERT", ""),
		TLSKey:         envStr("OBLIREACH_TLS_KEY", ""),
	}

	if cfg.RelaySecret == "change-me-in-production" {
		log.Println("[WARN] OBLIREACH_SECRET is not set — using insecure default. Set it in production!")
	}
	if cfg.OblianceURL == "" {
		log.Fatal("[FATAL] OBLIANCE_URL must be set")
	}
	cfg.OblianceURL = strings.TrimRight(cfg.OblianceURL, "/")
	return cfg
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
