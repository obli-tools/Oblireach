//go:build !windows

package engine

// SessionInfo describes a logon session (non-Windows stub).
type SessionInfo struct {
	ID            int    `json:"id"`
	Username      string `json:"username"`
	Domain        string `json:"-"`
	State         string `json:"state"`
	StationName   string `json:"stationName,omitempty"`
	IsConsole     bool   `json:"isConsole"`
	IsLoginPrompt bool   `json:"isLoginPrompt,omitempty"`
}

func EnumerateSessions() []SessionInfo         { return nil }
func ConsoleSessionID() int                    { return 0 }
func CurrentSessionID() int                    { return 0 }
func FindCaptureSession() int                  { return 0 }
func IsServer() bool                           { return false }
