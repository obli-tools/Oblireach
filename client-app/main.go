package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// appVersion is injected at build time via -ldflags "-X main.appVersion=x.y.z"
var appVersion = "1.0.0"

// ── Config ────────────────────────────────────────────────────────────────────

type Config struct {
	ServerURL   string          `json:"serverUrl"` // the active server profile's (servers.go)
	Username    string          `json:"username"`
	SessionFile string          `json:"-"`
	Profiles    []ServerProfile `json:"profiles,omitempty"`
	ActiveID    string          `json:"activeProfile,omitempty"`
	LocalPort   int             `json:"localPort,omitempty"` // UI origin port (listenLocal)
	// Settings > Application (startup_windows.go): start minimized when
	// started with Windows (the Run value then carries --minimized), and at
	// every launch too.
	StartMinimized       bool `json:"startMinimized,omitempty"`
	StartMinimizedAlways bool `json:"startMinimizedAlways,omitempty"`
	// ... and "Keep in the notification area" (tray.go): an icon next to
	// the clock; minimizing or closing the main window hides it there.
	MinimizeToTray bool `json:"minimizeToTray,omitempty"`
	// An update being installed (updater.go): the next run tells how it went.
	PendingUpdate *pendingUpdate `json:"pendingUpdate,omitempty"`
	// Settings > Application > Language (i18n.go): a language code, or ""
	// for automatic (the Obliance account's, else Windows', else English).
	Language string `json:"language,omitempty"`
	// Server mode (servers.go serverMode): "" / "single" (the active server)
	// or "multi" (every server at once). The theme follows the first server
	// of the list in multi-server mode (theme.go), unless MultiTheme is set.
	ServerMode string `json:"serverMode,omitempty"`
	// Settings > Application > Appearance (theme.go themeOverride): the theme
	// of the whole app in multi-server mode, and on a local base alone on
	// screen; "" follows the servers (the first one's account theme).
	MultiTheme string `json:"multiTheme,omitempty"`
	LocalTheme string `json:"localTheme,omitempty"`
	// Settings > Files (editsession.go): the editor of the editing round
	// trip, the size that warns, the upload confirmation, hidden files.
	Files FilesSettings `json:"files"`
}

var (
	configDir  string
	configFile string
	cookieFile string
)

func init() {
	// The platform's folders (platform.go appPaths; Windows:
	// %APPDATA%\OblireachClient, paths_windows.go).
	configDir = platformPaths.configDir()
	configFile = filepath.Join(configDir, "config.json")
	cookieFile = filepath.Join(configDir, "session.json")
}

func loadConfig() *Config {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return &Config{}
	}
	var cfg Config
	_ = json.Unmarshal(data, &cfg)
	if cfg.normalizeProfiles() {
		saveConfig(&cfg)
	}
	return &cfg
}

func saveConfig(cfg *Config) {
	_ = os.MkdirAll(configDir, 0700)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(configFile, data, 0600)
}

// listenLocal listens on loopback, on the port of the previous runs: the UI
// origin (http://127.0.0.1:<port>) and with it the page's localStorage
// (favourites, recents, pinned rail...) then survive a restart. The first
// run, or when that port is taken, gets a random port; it is remembered the
// first time only, so a second instance never moves the first one's origin.
func listenLocal(cfg *Config) (net.Listener, error) {
	if cfg.LocalPort > 0 {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.LocalPort)); err == nil {
			return ln, nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if cfg.LocalPort <= 0 {
		cfg.LocalPort = ln.Addr().(*net.TCPAddr).Port
		saveConfig(cfg)
	}
	return ln, nil
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// The platform's UI host (platform.go uiHost), before any window exists
	// (Windows: Per-Monitor V2 DPI aware, dpi_windows.go).
	platformUI.prepare()
	// One app per user session: a second launch brings the running one's
	// main window in front and ends (singleinstance.go). Before anything
	// reads or writes the config, or takes the local port.
	if !platformInstance.claim(os.Args[1:]) {
		return
	}
	cfg := loadConfig()
	// The app's language (i18n.go), before anything says a word.
	i18nSetCurrent(cfg.uiLang())
	// ... and its theme (theme.go): the windows' native colours from the start.
	setCurrentPalette(cfg.uiTheme())
	// The update an earlier run started (updater_windows.go): how it went.
	updated := platformUpdates.finishPending(cfg, appVersion, time.Now())
	keep := ""
	if cfg.PendingUpdate != nil {
		keep = cfg.PendingUpdate.Dir
	}
	go platformUpdates.sweep(keep, time.Now())

	// Start the local proxy server (loopback only; see Proxy.ServeHTTP).
	ln, err := listenLocal(cfg)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	localBase := fmt.Sprintf("http://127.0.0.1:%d", port)

	proxy := newProxy(cfg, configDir, port)
	go func() {
		if err := http.Serve(ln, proxy); err != nil && !strings.Contains(err.Error(), "use of closed") {
			log.Printf("proxy: %v", err)
		}
	}()

	// "Receive help" in the background (receive_help.go): this computer's ID
	// and code on the Dashboard, while "Allow incoming support" is on.
	startIncomingSupport()

	// The app's windows (platform.go uiHost; Windows: uihost_windows.go),
	// until the app quits.
	lfs, err := platformUI.run(uiStart{cfg: cfg, proxy: proxy, localBase: localBase, updated: updated, args: os.Args[1:]})
	if err != nil {
		log.Fatalf("main window: %v", err)
	}
	stopIncomingSupport()
	// A clean exit: the working copies the devices have are deleted, the
	// others kept (editsession.go).
	if lfs != nil {
		lfs.edits.shutdown()
		lfs.closeAll()
	}
}
