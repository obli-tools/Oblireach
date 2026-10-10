//go:build !windows

package main

import (
	"errors"
	"io"
	"runtime"
	"time"
)

// The chat window (WebView2) exists on Windows only: nothing to detect or
// install elsewhere, and the heartbeat carries no `webview2` block.

func platformWebView2Supported() bool { return false }

func platformWebView2Detect() webview2Detection { return webview2Detection{} }

func platformWebView2ChatAlternative() string { return "" }

func platformWebView2BootstrapperPath() string { return "" }

func verifyWebView2Bootstrapper(path string) (io.Closer, string, error) {
	return nil, "", errors.New("WebView2 is not used on " + runtime.GOOS)
}

func launchWebView2Bootstrapper(path string, lock io.Closer) (int, func(time.Duration) (int64, bool, error), error) {
	if lock != nil {
		lock.Close()
	}
	return 0, nil, errors.New("WebView2 is not used on " + runtime.GOOS)
}

func webview2InstallerAlive(pid int) bool { return false }

func platformNotifyChatUnavailable(sessionID int, installing bool) {}
