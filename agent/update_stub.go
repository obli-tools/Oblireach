//go:build !windows

package main

import (
	"errors"
	"runtime"
)

// Self-update is not implemented on Linux/macOS yet: those agents are updated
// by replacing the binary and restarting the service. The guard still applies
// (one "not implemented" line per 6 h instead of one per heartbeat).

func platformSelfUpdateSupported() bool { return false }

func windowsInstallerBusy() bool { return false }

func processAlive(pid int) bool { return false }

func verifyUpdateSigner(path string) (string, error) {
	return "", errors.New("signature check not implemented on " + runtime.GOOS)
}

func launchMSIExec(msiPath, logPath string, cfg *Config) (int, func() (int, error), error) {
	return 0, nil, errors.New("msiexec not available on " + runtime.GOOS)
}

// platformSHA2SigningMissing: Windows only.
func platformSHA2SigningMissing() bool { return false }
