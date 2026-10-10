//go:build !windows

package main

func runChatHelperMode(addr, chatID, operatorName, ui string) {}

// platformChatNativeAvailable: the native chat window is Windows only.
func platformChatNativeAvailable() bool { return false }

// platformChatPreferNative: Windows only.
func platformChatPreferNative() bool { return false }

// spawnChatHelperAsUser: the chat window exists on Windows only.
func spawnChatHelperAsUser(sessionID int, cmdLine string) (uint32, int) { return 0, -1 }

// platformChatSessionUserName: the chat window exists on Windows only.
func platformChatSessionUserName(sessionID int) string { return "" }
