//go:build windows

package main

// uihost_windows.go — the Windows UI host (uiHost, platform.go): the main
// window and its WebView2 (mainwindow_windows.go, webhost_windows.go,
// frame_windows.go), its native bridge (native_bridge.go), the detached
// session windows (detached_windows.go), the notification-area icon
// (tray_windows.go), one message loop for them all. Moved from main.go
// unchanged.

import (
	"fmt"
	"log"
)

type winUI struct{}

var platformUI uiHost = winUI{}

// prepare: Per-Monitor V2 DPI aware (dpi_windows.go), before any window
// exists: the manifest says so already; this covers a build without it.
func (winUI) prepare() { enableDpiAwareness() }

func (winUI) run(s uiStart) (*localFS, error) {
	cfg, proxy, localBase, updated := s.cfg, s.proxy, s.localBase, s.updated

	// The main window: frameless, its page draws the title bar
	// (mainwindow_windows.go, frame_windows.go), with its WebView2. Started
	// with Windows with "Start minimized" (--minimized), or always if so
	// chosen: minimized in the taskbar, or hidden in the notification area
	// with "Keep in the notification area" (tray_windows.go).
	minimized := hasArg(s.args, startMinimizedArg) || (cfg.StartMinimized && cfg.StartMinimizedAlways)
	w, err := newMainWindow("Oblireach", localBase, 1280, 800, minimized)
	if err != nil {
		return nil, err
	}
	// Its notification-area icon (tray.go), when so chosen.
	mainTray = newAppTray(w.hwnd)
	mainTray.focus = func() {
		if w.ready && !w.finished {
			w.chromium.Focus()
		}
	}
	mainTray.onQuit = func() { procDestroyWindow.Call(w.hwnd) }
	mainTray.enable(proxy.trayPref())
	if minimized {
		mainTray.hide() // (with the icon only: else it stays minimized in the taskbar)
	}

	// Inject Go bindings into every page.
	w.Init(fmt.Sprintf(`window.__reach_version = %q;`, appVersion))

	// Native clipboard + local file access for the remote viewer, the title
	// bar (native_bridge.go), usable by the local UI origin only. Bindings
	// must be registered before Navigate.
	var lfs *localFS
	if bridge, err := newNativeBridge(w, localBase); err != nil {
		log.Printf("native bridge disabled: %v", err)
	} else {
		bridge.win = hostFrame{&w.webHost}
		bridge.tunnels = proxy.tunnels
		w.frame.onState = bridge.pushWindowState
		// Detached session windows (detached_windows.go), opened from here.
		bridge.windows = newSessionWindows(bridge, proxy, localBase)
		// Settings > Application (startup_windows.go), the one-click update
		// (updater_windows.go): this window only.
		bridge.startup = newStartupCtl(proxy)
		// The notification-area icon (tray_windows.go): its setting, the
		// unread chat messages, the notifications, Quit the clean way.
		bridge.tray = mainTray
		bridge.startup.onTray = mainTray.enable
		mainTray.onQuit = func() { quitApp(w, bridge) }
		mainTray.onChat = func() { bridge.evalMain("__reachTrayOpenChat", "") }
		bridge.updater = newAppUpdater(proxy, bridge, func() { quitApp(w, bridge) }, updated)
		// The Files page's local pane and editing round trip (localfs*.go,
		// editsession*.go): this window's; the session windows share its
		// file bindings (their Files tabs' local pane).
		lfs = newLocalFS(proxy)
		bridge.attachLocalFS(lfs)
		bridge.windows.localfs = lfs // (the local pane of a session window's Files tabs: its file bindings only)
		bridge.bind()
		bridges.register(bridge.hwnd, bridge) // window_registry.go
		go bridge.watch()
	}
	// A new language (Settings, a server switch, the account's language):
	// every window redraws itself in it (i18n.go syncLang).
	proxy.setLangHook(bridges.broadcastLang)
	// A new theme (the account's, a server switch, the server mode): every
	// window wears it at once, page and native colours (theme.go syncTheme).
	proxy.setThemeHook(bridges.broadcastTheme)
	// The servers changed (the active one, the mode, the list): every window
	// reads them again; a session window follows them (servers.go).
	proxy.setServersHook(bridges.broadcastServers)

	// Navigate to the local UI, through the entry URL that hands the WebView
	// the per-run secret (proxy.go ServeHTTP).
	w.Navigate(proxy.entryURL())
	w.Run()
	return lfs, nil
}
