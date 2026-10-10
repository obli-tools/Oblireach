//go:build !windows

package main

import (
	"fmt"
	"log"
	"runtime"
)

func runHelperMode(addr string) {
	log.Fatalf("capture helper mode not supported on %s", runtime.GOOS)
}

func startCrossSessionStream(cfg *Config, token string, requestedSessionID, resolved int, operator string, files bool) error {
	return fmt.Errorf("cross-session streaming not supported on %s", runtime.GOOS)
}

// streamCurrentSession is a no-op off Windows (no follow supervisor).
func streamCurrentSession(token string) (int, bool) { return 0, false }

// spawnInSessionGo: no Windows sessions to spawn into.
func spawnInSessionGo(sessionID int, cmdLine string) (uint32, int) { return 0, -1 }
