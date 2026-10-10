package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"sync"
	"time"
)

// IP logging modes (RDV_LOG_IP_MODE).
const (
	IPModeFull      = "full"
	IPModeTruncated = "truncated" // IPv4 /24, IPv6 /48
	IPModeHMAC      = "hmac"      // pseudonymised with a key rotated every 24 h
)

// eventLog writes one JSON line per event. It never receives content: no
// SDP, no candidate, no display name, no code. IDs and session ids are
// truncated by the callers.
type eventLog struct {
	l    *slog.Logger
	mode string

	mu      sync.Mutex
	key     []byte
	keyFrom time.Time
}

func newEventLog(l *slog.Logger, mode string) *eventLog {
	if mode == "" {
		mode = IPModeFull
	}
	return &eventLog{l: l, mode: mode}
}

func (e *eventLog) event(name string, attrs ...any) {
	e.l.Info(name, append([]any{"event", name}, attrs...)...)
}

func (e *eventLog) warn(name string, attrs ...any) {
	e.l.Warn(name, append([]any{"event", name}, attrs...)...)
}

// ip renders an address according to the logging mode.
func (e *eventLog) ip(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return "?"
	}
	switch e.mode {
	case IPModeTruncated:
		if v4 := ip.To4(); v4 != nil {
			return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
		}
		return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
	case IPModeHMAC:
		e.mu.Lock()
		if e.key == nil || time.Since(e.keyFrom) > 24*time.Hour {
			e.key = make([]byte, 32)
			_, _ = rand.Read(e.key)
			e.keyFrom = time.Now()
		}
		m := hmac.New(sha256.New, e.key)
		e.mu.Unlock()
		m.Write(ip)
		return "h:" + hex.EncodeToString(m.Sum(nil)[:8])
	default:
		return ip.String()
	}
}
