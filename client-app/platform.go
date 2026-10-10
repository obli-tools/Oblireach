package main

// platform.go — the platform seams of Oblireach Desktop.
//
// One Go program, one page: everything that is not native lives in
// platform-neutral files (the local proxy, the page, SSH/SFTP/SCP, the vault
// format and its state machine, supportlink, the native bridge and its
// bindings...). What a platform must provide is declared here, implemented
// by *_windows.go on Windows and, for Linux, by *_linux.go (today compile-only
// stubs that answer errNotImplemented; the real implementations are phase 2
// of the Linux port).
//
// 1. Process-wide services, one package variable each, set by the platform:
//
//   platformUI        uiHost            UI host: the app's windows and their loop
//   platformInstance  instanceGuard     single instance per user session
//   platformUpdates   updateHost        start-up side of the self-update
//   platformPaths     appPaths          where the app keeps its files
//   sysClipboard      platformClipboard local clipboard (text, file paths)
//   platformDialogs   fileDialogs       Open / Save / pick-a-program dialogs
//   platformPresence  presenceVerifier  biometric unlock of the vault
//
// 2. Per-window objects (types of the same name on every platform):
//
//   bridgeHost (native_bridge.go), appWindow, mainAppWindow   a window and its web view
//   frameOps                          the page-drawn title bar (drag, min/max/close, state)
//   paletteHost (native_events.go)    native colours following the theme
//   trayHost        *appTray          notification-area icon, notifications
//   autostartHost   *startupCtl       start with the session, start minimized
//   appUpdater      *desktopUpdater   the one-click update of the main window
//   *sessionWindows, *detachedWindow  detached session windows (window manager)
//
// 3. Binding sets: the methods of *nativeBridge a platform implements
// (bind, native_bridge.go, registers them on every platform: the same
// reach* names, the same signatures — asserted below, so the page's
// contract cannot drift between platforms):
//
//   windowBindings, sessionWindowBindings, rdpBindings, clipboardPasteBindings,
//   inputBindings, appBindings
//
// 4. Existing seams, already interfaces or replaceable functions:
//
//   protector, sealer, vaultStore, presenceFlag (vault.go)  secret protector:
//       DPAPI (vault_windows.go getAppVault); Linux: Secret Service
//       (vault_linux.go, secretservice_linux.go)
//   supportBlobStore (support.go, newSupportStore)  sealed support settings
//   helpBackend (receive_help.go, newAppHelpHost, start/stopIncomingSupport)
//       "Receive help": the engine in process (receive_help_windows.go)
//   localSys (localfs.go, newLocalFS)  the local disk and shell (winSys)
//   systemUILanguage (i18n.go)  the system's display language
//   launchEditor, plannedEditor, editorDisplayName (editsession*.go)
//       open-with / default apps of the editing round trip
//   winscpRegistrySessions (connimport_windows.go)  WinSCP's own settings
//   foregroundRoot (keyhook_windows.go)  our window in the foreground, if any
//   vaultBaseDir (vault_windows.go)  the vault's private folder

import (
	"runtime"
	"time"
)

// errNotImplemented: a platform feature this build does not have yet (the
// Linux stubs answer it; Windows has every feature).
type errNotImplemented struct{ feature string }

func (e errNotImplemented) Error() string {
	return e.feature + ": not implemented on " + runtime.GOOS + " yet"
}

func notImplemented(feature string) error { return errNotImplemented{feature: feature} }

// ── 1. Process-wide services ───────────────────────────────────────────────

// uiStart: what the UI host needs to open the app's windows (main.go).
type uiStart struct {
	cfg       *Config
	proxy     *Proxy
	localBase string        // the local UI origin, http://127.0.0.1:<port>
	updated   *updateResult // how an earlier run's update went (updateHost)
	args      []string      // the command line (--minimized)
}

// uiHost: the platform's UI host and window manager. Windows: winUI
// (uihost_windows.go): frameless Win32 windows hosting WebView2, one message
// loop; Linux later: GTK4 + WebKitGTK (or an Electron-style host, behind
// this same interface, if WebKitGTK fails the engine gate).
type uiHost interface {
	// prepare: before any window exists (Windows: DPI awareness).
	prepare()
	// run opens the main window, its native bridge (bind) and its
	// notification-area icon, navigates to the local UI and runs until the
	// app quits. It returns the main window's local file system (its working
	// copies are cleaned up at exit), nil without one; an error when the
	// main window could not be created.
	run(s uiStart) (*localFS, error)
}

// instanceGuard: one app per user session. Windows: winInstance
// (singleinstance_windows.go: a named mutex and a registered message);
// Linux later: a unique D-Bus name (GApplication).
type instanceGuard interface {
	// claim: true, this launch is the app; false, it must end (the running
	// one was asked to show itself).
	claim(args []string) bool
}

// updateHost: the start-up side of the self-update. Windows: winUpdates
// (updater_windows.go: the MSI's result, its download folders); Linux later:
// Flatpak (the portal's UpdateMonitor).
type updateHost interface {
	// finishPending: how the update an earlier run started went (nil: none
	// was, or it is not known yet).
	finishPending(cfg *Config, running string, now time.Time) *updateResult
	// sweep removes the old download folders but keep.
	sweep(keep string, now time.Time)
}

// appPaths: where the app keeps its files. Windows: winPaths
// (paths_windows.go); Linux later: the XDG base directories (the Flatpak's
// own ~/.var/app/<id>).
type appPaths interface {
	// configDir: the settings (config.json, session.json).
	configDir() string
	// localDataDir: the data that stays on this machine (the vault, the
	// working copies of the editing round trip); "" when unknown.
	localDataDir() string
}

// platformClipboard: the local clipboard, for the native bridge
// (native_bridge.go). owner is the window asking (its native handle).
// Windows: winClipboard (native_windows.go); Linux later: GdkClipboard.
type platformClipboard interface {
	// sequence: a number that moves at every change of the clipboard (0:
	// unknown); cheap, polled by the watcher.
	sequence() uint32
	// read: the text on the clipboard (capped by the caller's rules), and
	// whether files are on it.
	read(owner uintptr) (clipboardInfo, error)
	// writeText replaces the clipboard with text; returns the sequence
	// number right after the write.
	writeText(owner uintptr, text string) (uint32, error)
	// filePaths: the paths of the files copied (at most maxClipboardFiles).
	filePaths(owner uintptr) ([]string, error)
}

// fileDialogs: the platform's file dialogs, owned by window owner (its
// native handle). Windows: winDialogs, the common dialogs
// (connimport_windows.go, editsession_windows.go); Linux later: the
// FileChooser portal.
type fileDialogs interface {
	// pickFile: an Open (save false) or Save As dialog. filter:
	// "label\x00pattern\x00..." pairs. ok false: cancelled.
	pickFile(owner uintptr, save bool, title, filter, initialDir, suggested, defExt string) (string, bool)
	// pickProgram: a program to open files with (Settings > Files, the
	// editing round trip's editor). ok false: cancelled.
	pickProgram(owner uintptr, title, filterLabel string) (string, bool)
}

// presenceVerifier: a biometric (or PIN) check that the signed-in user is
// present, the opt-in second way to unlock the password vault
// (vault_hello.go: a gate, not a key — the vault stays sealed by the secret
// protector). Windows: winHello, Windows Hello (hello_windows.go); Linux
// later: fprintd.
type presenceVerifier interface {
	// available: the platform can verify this user now.
	available() (bool, error)
	// verify shows the prompt over window owner and returns one of the
	// helloVerified... answers (vault_hello.go), or errHelloTimeout /
	// errHelloCanceled.
	verify(owner uintptr, message string, timeout time.Duration) (int32, error)
	// shown: window owner is on screen, in the foreground (the automatic
	// prompt is only launched then: hello_auto.go).
	shown(owner uintptr) bool
}

// ── 2. Per-window objects ──────────────────────────────────────────────────

// appWindow: one of the app's top-level windows and its web view (webHost on
// Windows: webhost_windows.go).
type appWindow interface {
	bridgeHost
	// Navigate shows url in the window's web view.
	Navigate(url string)
}

// mainAppWindow: the main window, whose loop runs the app (mainWindow on
// Windows: mainwindow_windows.go).
type mainAppWindow interface {
	appWindow
	// Run pumps the app's events until the main window is closed.
	Run()
}

// windowStateInfo: what the page's title bar shows (reachWindowState, then
// window.__reachOnWindowState at every change).
type windowStateInfo struct {
	Maximized bool `json:"maximized"`
	Active    bool `json:"active"`
}

// frameOps: what the title bar bindings do to their window (fakes in tests;
// hostFrame on Windows: frame_windows.go).
type frameOps interface {
	startDrag(hit uintptr) // Windows moves / resizes the window with the mouse
	sysCommand(sc uintptr) // minimize, maximize, restore, close
	state() windowStateInfo
}

// trayHost: the main window's notification-area icon and its notifications
// (*appTray: tray_windows.go; Linux later: StatusNotifierItem + portal
// notifications).
type trayHost interface {
	enable(on bool)  // the icon shown or not (Settings > Application)
	hide()           // the main window hidden in it
	restore()        // the main window back on screen, in front
	active() bool    // the icon is there
	setUnread(n int) // the unread chat messages
	balloon(title, text string) bool
	remove()
}

// startupState: what Settings > Application shows.
type startupState struct {
	RunAtLogin      bool   `json:"runAtLogin"`            // the Run value exists and is not turned off
	DisabledInTM    bool   `json:"disabledInTaskManager"` // it exists, turned off in Task Manager
	OtherCopy       string `json:"otherCopy,omitempty"`   // it starts another program than this one
	Minimized       bool   `json:"minimized"`
	MinimizedAlways bool   `json:"minimizedAlways"`
	Tray            bool   `json:"tray"` // "Keep in the notification area" (tray.go)
	Exe             string `json:"exe"`
}

// autostartHost: start with the user's session, start minimized
// (*startupCtl: startup_windows.go, the Run key; Linux later: the Background
// portal).
type autostartHost interface {
	state() (startupState, error)
	set(what string, on bool) (startupState, error)
}

// updateStatus is what the page's banner shows (window.__reachOnUpdate).
type updateStatus struct {
	// idle | downloading | verifying | elevating | installing | cancelled | error
	State    string        `json:"state"`
	Received int64         `json:"received"`
	Total    int64         `json:"total"`   // 0: unknown
	Percent  int           `json:"percent"` // -1: unknown
	Version  string        `json:"version,omitempty"`
	Code     string        `json:"code,omitempty"`
	Message  string        `json:"message,omitempty"`
	Text     *i18nMsg      `json:"text,omitempty"`   // Message by its key (the page's language)
	Result   *updateResult `json:"result,omitempty"` // how the last update went (after a restart)
	// Seq orders the statuses: the page keeps the latest (a push can reach
	// it before the answer of the call that started the run).
	Seq int64 `json:"seq"`
}

// appUpdater: the one-click update of the main window (*desktopUpdater:
// updater_windows.go, the signed MSI; Linux later: Flatpak).
type appUpdater interface {
	start() updateStatus
	state() updateStatus
}

// ── 3. Binding sets of *nativeBridge ───────────────────────────────────────

// windowBindings: the page's own title bar (frame_windows.go), the taskbar
// attention (attention_windows.go), every window.
type windowBindings interface {
	windowDrag(tok, edge string) (bool, error)
	windowControl(tok, action string) (bool, error)
	windowState(tok string) (windowStateInfo, error)
	pushWindowState(s windowStateInfo)
	attention(tok string) (bool, error)
}

// sessionWindowBindings: the detached session windows (detached_windows.go):
// opened and closed from the main window, their tabs.
type sessionWindowBindings interface {
	openWindow(tok, deviceID, title string, opts detachOpts) (string, error)
	focusWindow(tok, key string) (bool, error)
	closeWindow(tok, key string) (bool, error)
	closeWindows(tok string) (int, error)
	closeProfileWindows(tok, profile string) (int, error)
	listWindows(tok string) ([]string, error)
	windowTabs(tok string, info windowTabsInfo) (bool, error)
	windowTargets(tok string) ([]windowTarget, error)
	closeOwnWindow(tok string) (bool, error)
	openTab(tok, key string) (string, error)
	tearOff(tok, deviceID, title string, opts detachOpts) (string, error)
}

// rdpBindings: the local RDP sessions of the connection tree, per window.
// Windows: mstsc.exe reparented into the page (rdp_bridge_windows.go,
// rdp_session.go); Linux later: the FreeRDP helper streaming H.264 to the
// ObliReach viewer (a tab like the others).
type rdpBindings interface {
	rdpConnect(tok, reqID, idHex string, x, y, w, h int32) (bool, error)
	rdpResize(tok, connID string, x, y, w, h int32) (bool, error)
	rdpShow(tok, connID string, show bool, x, y, w, h int32) (bool, error)
	rdpPark(tok, connID string) (string, error)
	rdpAttach(tok, park string, x, y, w, h int32) (string, error)
	rdpClose(tok, connID string) (bool, error)
	rdpCloseOwned()
	// rdpTunnel: stream mode only (Linux): a one-time token of the
	// session's picture tunnel, /local/rdp-tunnel/<token> (rdphost_relay.go).
	rdpTunnel(tok, connID string) (string, error)
}

// clipboardPasteBindings: Ctrl+V of files and images in a file explorer
// (clipboard_paste_windows.go).
type clipboardPasteBindings interface {
	clipboardPaste(tok, reqID string, large bool) (bool, error)
	clipboardPasteKinds(tok string) (pasteKinds, error)
}

// inputBindings: the system keys a session canvas captures (the Windows
// key: keyhook_windows.go; Linux later: the keyboard-shortcuts-inhibit
// protocol).
type inputBindings interface {
	keyCapture(tok string, on bool) (bool, error)
}

// appBindings: the main window's application settings: the update, start
// with the session, the notification-area icon.
type appBindings interface {
	installUpdate(tok string) (updateStatus, error)
	updateState(tok string) (updateStatus, error)
	startupGet(tok string) (startupState, error)
	startupSet(tok, what string, on bool) (startupState, error)
	trayUnread(tok string, n int) (bool, error)
	trayNotify(tok, title, text string) (bool, error)
}

// Every platform implements every binding set and every per-window object.
var (
	_ windowBindings         = (*nativeBridge)(nil)
	_ sessionWindowBindings  = (*nativeBridge)(nil)
	_ rdpBindings            = (*nativeBridge)(nil)
	_ clipboardPasteBindings = (*nativeBridge)(nil)
	_ inputBindings          = (*nativeBridge)(nil)
	_ appBindings            = (*nativeBridge)(nil)
	_ regBridge              = (*nativeBridge)(nil)
	_ langBridge             = (*nativeBridge)(nil)
	_ themeBridge            = (*nativeBridge)(nil)
	_ serversBridge          = (*nativeBridge)(nil)

	_ trayHost      = (*appTray)(nil)
	_ autostartHost = (*startupCtl)(nil)
	_ appUpdater    = (*desktopUpdater)(nil)
)
