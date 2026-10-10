package main

// config.json holds the ObliReach API key and the server URL: only root /
// SYSTEM / Administrators may read it (secretfile_windows.go,
// secretfile_unix.go). No other process needs it: the capture, chat and toast
// helpers that run in a user session get what they need from the service on
// their command line / loopback pipe and never read config.json.

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// randomHex returns n random bytes hex-encoded (temp file names).
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// stripServiceSecretArgs returns the service command line without its
// arguments when they carry the API key (--key / -key): older MSIs registered
// the service as `"oblireach-agent.exe" --url "..." --key "..."`, and a
// service's ImagePath is readable by every local user (`sc qc`, registry).
// ok is false when there is nothing to strip or the line cannot be parsed.
func stripServiceSecretArgs(imagePath string) (stripped string, ok bool) {
	s := strings.TrimSpace(imagePath)
	if s == "" {
		return "", false
	}
	var exe, rest string
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return "", false
		}
		exe, rest = s[1:1+end], s[2+end:]
	} else {
		// Unquoted: the executable ends at the first " -" (paths with spaces
		// are always quoted by the MSI; this only covers hand-made services).
		i := strings.Index(s, " -")
		if i < 0 {
			return "", false
		}
		exe, rest = s[:i], s[i:]
	}
	if exe == "" || !hasKeyArg(rest) {
		return "", false
	}
	return `"` + exe + `"`, true
}

func hasKeyArg(args string) bool {
	for _, f := range strings.Fields(args) {
		f = strings.TrimLeft(f, "-")
		if f == "key" || strings.HasPrefix(f, "key=") {
			return true
		}
	}
	return false
}
