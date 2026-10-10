//go:build !windows

package main

func notifySession(sessionID int, username string, connected bool) {
	// Notifications are only supported on Windows.
}

func runToastNotification(title, message string, timeoutSec int) {
	// Toast notifications are only supported on Windows.
}
