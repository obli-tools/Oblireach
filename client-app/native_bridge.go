package main

// Native bridge between the embedded viewer page and the platform
// (platform-neutral part; the Windows side is native_windows.go, the
// platform seams are listed in platform.go).
//
// The page runs in the platform's web view (WebView2 on Windows) on
// http://127.0.0.1 and cannot touch the local clipboard or local files on its
// own in a way that works for a remote session (the browser clipboard API
// needs a gesture, file paths are hidden). These bindings give the page
// exactly what the ObliReach viewer needs:
//
//   reachClipboardRead(tok)          -> {text, hasText, hasFiles, tooLarge}
//   reachClipboardWrite(tok, text)   -> true (writes the clipboard's text)
//   reachClipboardFiles(tok)         -> [{handle, name, size, dir}] for the
//                                       files the operator copied
//   reachReadFileChunk(tok, h, o, n) -> base64 of n bytes of that file (or
//                                       of a file pasted in an explorer)
//   reachClipboardPaste / reachClipboardPasteKinds -> Ctrl+V in a file
//                                       explorer (clipboard_paste*.go)
//   reachClipboardWatch(tok, on)     -> start/stop pushing local clipboard
//                                       text changes to the page
//   reachKeyCapture / reachAttention -> keyhook_windows.go, attention_windows.go
//   reachWindowDrag / reachWindowControl / reachWindowState -> the page's own
//                                       title bar (frame_windows.go)
//   reachOpenWindow / reachFocusWindow / reachCloseWindow(s) (main window),
//   reachCloseSelf, reachOpenTab, reachTearOff (detached window),
//   reachWindowTabs, reachWindowTargets (both) -> detached_windows.go
//   reachInstallUpdate / reachUpdateState (main window) -> updater_windows.go
//   reachStartupGet / reachStartupSet (main window) -> startup_windows.go
//   reachTrayUnread / reachTrayNotify (main window) -> tray_windows.go
//   reachFs* / reachFilesSettings* / reachEdit* (main window) -> the local
//                                       files of the Files page and the
//                                       editing round trip (localfs*.go,
//                                       editsession*.go)
//   reachFs* / reachFilesSettingsGet (detached window) -> the local pane of
//                                       its Files tabs (attachSharedLocalFS)
//
// Every window of the app (the main one, each detached session window) has
// its own bridge, token and bindings.
//
// Only the local UI may use them. The binding glue (webhost_rpc.go) is
// injected into EVERY document and RPC messages are accepted from any of
// them (including the external SSO page the app navigates to), so each call
// carries a per-run secret (tok) that an init script reveals only to
// documents whose origin is the local UI. Clipboard pushes are evaluated
// behind the same origin check.
//
// Files are only reachable through opaque random handles handed out for the
// files that are on the operator's clipboard at the time of the call: the page
// never passes a path, and the local proxy has no file endpoint.
//
// All bindings run on the UI thread of the window's host (on Windows,
// webHost.onMessage runs RPC calls from the WebMessageReceived handler,
// which also owns the window used as clipboard owner, so every
// OpenClipboard/CloseClipboard pair happens on that one locked OS thread).

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	// Local clipboard text larger than this is not pushed to the page (the
	// agent caps clipboard text at 1 MiB after JSON encoding anyway).
	maxClipboardText = 2 << 20
	// Largest chunk the page may read in one reachReadFileChunk call.
	maxFileChunk = 1 << 20
	// Most files listed from one clipboard read.
	maxClipboardFiles = 200
	// Handles older than this are forgotten.
	fileHandleTTL  = 12 * time.Hour
	maxFileHandles = 2000
)

// ── Bridge ─────────────────────────────────────────────────────────────────

type clipboardInfo struct {
	Text     string `json:"text"`
	HasText  bool   `json:"hasText"`
	HasFiles bool   `json:"hasFiles"`
	TooLarge bool   `json:"tooLarge"`
	Seq      uint32 `json:"seq"` // clipboard sequence number when read
}

type clipFileInfo struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Dir    bool   `json:"dir"`
}

type fileHandle struct {
	path    string
	size    int64
	created time.Time
}

// bridgeHost is the part of a window the bridge uses: the main window or a
// detached session window (webHost, webhost_windows.go).
type bridgeHost interface {
	Init(js string)
	Bind(name string, f interface{}) error
	Dispatch(f func())
	Eval(js string)
	Window() unsafe.Pointer
}

type nativeBridge struct {
	w      bridgeHost
	hwnd   uintptr
	origin string // local UI origin, e.g. http://127.0.0.1:52001
	token  string // per-run secret, only revealed to the local UI

	capture atomic.Bool // the page captures the Windows key (keyhook_windows.go)
	closed  atomic.Bool // the window is gone: nothing is evaluated any more
	done    chan struct{}

	// The title bar bindings' window (frame_windows.go; nil: they do nothing).
	win frameOps
	// Main window only: opens and closes the detached session windows.
	windows *sessionWindows
	// Detached window only: closes its own window (reachCloseSelf).
	closeSelf func()
	// Detached window only: its window, for its tab bindings (reachOpenTab...).
	detached *detachedWindow
	// Main window only: the one-click update (updater_windows.go) and
	// Settings > Application (startup_windows.go).
	updater *desktopUpdater
	startup *startupCtl
	// Main window only: its notification-area icon (tray_windows.go).
	tray *appTray
	// The operator's own files (localfs*.go): the main window's, with the
	// editing round trip (editsession*.go; attachLocalFS); shared with a
	// session window for the local pane of its Files tabs (localfsShared:
	// the file bindings only, attachSharedLocalFS).
	localfs       *localFS
	localfsShared bool
	// Every window: the password vault's session registry (reach_tunnels.go),
	// shared with the Proxy — nil only in a test bridge that never sets it.
	tunnels *reachTunnels

	watchOn atomic.Bool
	lastSeq atomic.Uint32 // last sequence number seen by the watcher
	ownSeq  atomic.Uint32 // sequence number produced by our last write

	mu             sync.Mutex
	lastRemoteHash [32]byte // text we last wrote from the remote (echo guard)
	haveRemoteHash bool
	files          map[string]fileHandle
}

func newNativeBridge(w bridgeHost, localOrigin string) (*nativeBridge, error) {
	tok, err := randomHandle()
	if err != nil {
		return nil, err
	}
	return &nativeBridge{
		w:      w,
		hwnd:   uintptr(w.Window()),
		origin: localOrigin,
		token:  tok,
		done:   make(chan struct{}),
		files:  map[string]fileHandle{},
	}, nil
}

// shutdown: the window of this bridge is gone (detached window closed).
func (b *nativeBridge) shutdown() {
	if b.closed.Swap(true) {
		return
	}
	b.watchOn.Store(false)
	b.capture.Store(false)
	b.rdpCloseOwned() // (its local RDP sessions end with it: rdp_bridge_windows.go)
	close(b.done)
}

var errNotAllowed = errors.New("not allowed")

// bind registers the JS bindings. Must be called before Navigate.
func (b *nativeBridge) bind() {
	origin, _ := json.Marshal(b.origin)
	token, _ := json.Marshal(b.token)
	// Runs before any page script in every document; location cannot be
	// forged by the page. Only the top-level local UI document gets it (the
	// app's page is "/", a session window's "/?detached=..."): never a page
	// of a server proxied on this origin (/proxy/..., /sp/...) nor a frame.
	b.w.Init("(function () { if (location.origin !== " + string(origin) + " || location.pathname !== '/' || window.top !== window) return; " +
		"Object.defineProperty(window, '__reachNativeToken', { value: " + string(token) + " }); })();")
	_ = b.w.Bind("reachClipboardRead", b.clipboardRead)
	_ = b.w.Bind("reachClipboardWrite", b.clipboardWrite)
	_ = b.w.Bind("reachClipboardFiles", b.clipboardFiles)
	_ = b.w.Bind("reachReadFileChunk", b.readFileChunk)
	_ = b.w.Bind("reachClipboardWatch", b.clipboardWatch)
	_ = b.w.Bind("reachClipboardPaste", b.clipboardPaste)           // (Ctrl+V in a file explorer: clipboard_paste_windows.go)
	_ = b.w.Bind("reachClipboardPasteKinds", b.clipboardPasteKinds) // (... its menu item)
	_ = b.w.Bind("reachKeyCapture", b.keyCapture) // keyhook_windows.go
	_ = b.w.Bind("reachAttention", b.attention)   // attention_windows.go
	// The password vault (vault_bridge.go): read/unlock/type in every
	// window, management (below) in the main window only.
	_ = b.w.Bind("reachVaultState", b.vaultState)
	_ = b.w.Bind("reachVaultForSession", b.vaultForSession)
	_ = b.w.Bind("reachVaultUnlock", b.vaultUnlock)
	_ = b.w.Bind("reachVaultUnlockHello", b.vaultUnlockHello)         // Windows Hello (vault_hello.go)
	_ = b.w.Bind("reachVaultUnlockHelloAuto", b.vaultUnlockHelloAuto) // ... launched as a prompt opens (hello_auto.go)
	_ = b.w.Bind("reachVaultCreate", b.vaultCreateWithCode)
	_ = b.w.Bind("reachVaultLock", b.vaultLock)
	_ = b.w.Bind("reachVaultType", b.vaultType)
	_ = b.w.Bind("reachVaultTypeVm", b.vaultTypeVm) // (a Hyper-V VM console: vm_console.go)
	// The local RDP/SSH connection tree's SSH terminals (ssh_bridge.go,
	// ssh_relay.go): every window — a terminal tab moves between windows like
	// an agent's (its shell lives in the proxy, term_park.go).
	_ = b.w.Bind("reachSshConnect", b.sshConnect)
	_ = b.w.Bind("reachSshCwd", b.sshCwd)       // (files dropped on its terminal: its folder)
	_ = b.w.Bind("reachLocalInfo", b.localInfo) // (its Info panel, while it is open: localconn_info.go)
	// A local connection's description (no secret), for a window it moves
	// to (vault_bridge.go vaultConnInfo), and its file explorer
	// (sftp_bridge.go): every window too, like its terminal.
	_ = b.w.Bind("reachVaultConnInfo", b.vaultConnInfo)
	_ = b.w.Bind("reachVaultByParent", b.vaultByParent)
	_ = b.w.Bind("reachConnProbe", b.connProbe) // (their hosts answering: conn_probe.go)
	_ = b.w.Bind("reachSftpConnect", b.sftpConnect)
	_ = b.w.Bind("reachSftpClose", b.sftpClose)
	_ = b.w.Bind("reachSftpCmd", b.sftpCmd) // (the explorer's own commands: localconn_explorer.go)
	// The local RDP/SSH connection tree's RDP sessions (rdp_bridge_windows.go):
	// same per-window rule as the SSH bindings just above.
	_ = b.w.Bind("reachRdpConnect", b.rdpConnect)
	_ = b.w.Bind("reachRdpResize", b.rdpResize)
	_ = b.w.Bind("reachRdpClose", b.rdpClose)
	_ = b.w.Bind("reachRdpShow", b.rdpShow)     // (its tab on screen or not)
	_ = b.w.Bind("reachRdpPark", b.rdpPark)     // (its tab moving to another window: rdp_park.go)
	_ = b.w.Bind("reachRdpAttach", b.rdpAttach) // (... taken over there)
	_ = b.w.Bind("reachRdpTunnel", b.rdpTunnel) // (stream mode: its picture's tunnel, rdphost_relay.go)
	// The page's own title bar (frame_windows.go), in every window.
	_ = b.w.Bind("reachWindowDrag", b.windowDrag)
	_ = b.w.Bind("reachWindowControl", b.windowControl)
	_ = b.w.Bind("reachWindowState", b.windowState)
	// Main window: the detached session windows (detached_windows.go).
	if b.windows != nil {
		rdpParkHwnd.Store(b.hwnd) // (a local RDP session moving between windows is held here: rdp_park.go)
		rdpMainBridge.Store(b)
		_ = b.w.Bind("reachOpenWindow", b.openWindow)
		_ = b.w.Bind("reachFocusWindow", b.focusWindow)
		_ = b.w.Bind("reachCloseWindow", b.closeWindow)
		_ = b.w.Bind("reachCloseWindows", b.closeWindows)
		_ = b.w.Bind("reachCloseProfileWindows", b.closeProfileWindows) // one server's sessions (multi-server mode)
		_ = b.w.Bind("reachListWindows", b.listWindows)
		_ = b.w.Bind("reachWindowTabs", b.windowTabs)
		_ = b.w.Bind("reachWindowTargets", b.windowTargets) // "Move to window" (tab_move.go)
		// The password vault's management (main window only): the full list,
		// add/edit/delete, and revealing a secret on screen.
		_ = b.w.Bind("reachVaultList", b.vaultList)
		_ = b.w.Bind("reachVaultSave", b.vaultSave)
		_ = b.w.Bind("reachVaultDelete", b.vaultDelete)
		_ = b.w.Bind("reachVaultReveal", b.vaultReveal)
		_ = b.w.Bind("reachVaultCredentials", b.vaultCredentials) // (the local base's reusable credentials: vault page, connection form)
		_ = b.w.Bind("reachVaultHelloSet", b.vaultHelloSet)       // "Unlock with Windows Hello" (vault_hello.go)
		// Its import / export (connimport_bridge.go), main window only too.
		b.bindConnImport()
	}
	// Main window: the update and the start-up settings.
	if b.updater != nil {
		_ = b.w.Bind("reachInstallUpdate", b.installUpdate)
		_ = b.w.Bind("reachUpdateState", b.updateState)
	}
	if b.startup != nil {
		_ = b.w.Bind("reachStartupGet", b.startupGet)
		_ = b.w.Bind("reachStartupSet", b.startupSet)
	}
	// Main window: its notification-area icon (tray_windows.go).
	if b.tray != nil {
		_ = b.w.Bind("reachTrayUnread", b.trayUnread)
		_ = b.w.Bind("reachTrayNotify", b.trayNotify)
	}
	// The local files of a Files tab's split view (localfs_bridge.go): every
	// binding in the main window (the editing round trip too); the file
	// bindings only in a session window (attachSharedLocalFS).
	if b.localfs != nil {
		b.bindLocalFS()
	}
	if b.closeSelf != nil { // a detached window closes itself
		_ = b.w.Bind("reachCloseSelf", b.closeOwnWindow)
	}
	if b.detached != nil { // a detached window's tabs
		_ = b.w.Bind("reachWindowTabs", b.windowTabs)
		_ = b.w.Bind("reachOpenTab", b.openTab)
		_ = b.w.Bind("reachTearOff", b.tearOff)
		_ = b.w.Bind("reachWindowTargets", b.windowTargets)
	}
}

func (b *nativeBridge) allowed(tok string) bool {
	return len(tok) == len(b.token) && subtle.ConstantTimeCompare([]byte(tok), []byte(b.token)) == 1
}

func (b *nativeBridge) clipboardRead(tok string) (clipboardInfo, error) {
	if !b.allowed(tok) {
		return clipboardInfo{}, errNotAllowed
	}
	return b.readClipboard()
}

// readClipboard: the local clipboard now (platformClipboard, this window as
// its owner).
func (b *nativeBridge) readClipboard() (clipboardInfo, error) {
	return sysClipboard.read(b.hwnd)
}

func (b *nativeBridge) clipboardWrite(tok, text string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if len(text) > maxClipboardText {
		return false, errors.New("text too large")
	}
	h := sha256.Sum256([]byte(text))
	b.mu.Lock()
	b.lastRemoteHash = h
	b.haveRemoteHash = true
	b.mu.Unlock()
	seq, err := sysClipboard.writeText(b.hwnd, text)
	if err != nil {
		return false, err
	}
	b.ownSeq.Store(seq)
	return true, nil
}

func (b *nativeBridge) clipboardFiles(tok string) ([]clipFileInfo, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	paths, err := sysClipboard.filePaths(b.hwnd)
	if err != nil {
		return nil, err
	}

	out := make([]clipFileInfo, 0, len(paths))
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneHandlesLocked(now)
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		fi := clipFileInfo{Name: filepath.Base(p), Dir: st.IsDir()}
		if !fi.Dir {
			fi.Size = st.Size()
			handle, err := randomHandle()
			if err != nil {
				return nil, err
			}
			b.files[handle] = fileHandle{path: p, size: fi.Size, created: now}
			fi.Handle = handle
		}
		out = append(out, fi)
	}
	return out, nil
}

func (b *nativeBridge) pruneHandlesLocked(now time.Time) {
	for k, v := range b.files {
		if now.Sub(v.created) > fileHandleTTL {
			delete(b.files, k)
		}
	}
	if len(b.files) < maxFileHandles {
		return
	}
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(b.files))
	for k, v := range b.files {
		all = append(all, kv{k, v.created})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for i := 0; i < len(all)-maxFileHandles/2; i++ {
		delete(b.files, all[i].k)
	}
}

func (b *nativeBridge) readFileChunk(tok, handle string, offset int64, length int) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	b.mu.Lock()
	fh, ok := b.files[handle]
	b.mu.Unlock()
	if !ok {
		// (a file pasted in a file explorer: clipboard_paste.go)
		return clipPastes.readB64(handle, offset, length)
	}
	if offset < 0 || length <= 0 || length > maxFileChunk || offset > fh.size {
		return "", errors.New("invalid range")
	}
	f, err := os.Open(fh.path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), nil
}

func (b *nativeBridge) clipboardWatch(tok string, on bool) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if on && !b.watchOn.Load() {
		// Only changes made from now on are pushed; the page syncs the current
		// content itself when a session starts.
		b.lastSeq.Store(sysClipboard.sequence())
	}
	b.watchOn.Store(on)
	return on, nil
}

// watch polls the clipboard sequence number (cheap, no clipboard open) and,
// when it moved and the change is not our own write, reads the text on the
// UI thread and hands it to the page. With several windows (detached
// sessions) only the window the operator worked in last gets the copy
// (window_registry.go clipboardTarget); the others only learn that the local
// clipboard changed (their staged text is outdated). Ends with the window.
func (b *nativeBridge) watch() {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-t.C:
		}
		// Every tick: the foreground window of ours is remembered even while
		// its page does not watch (it is still the one the operator uses).
		target := bridges.clipboardTarget(b.hwnd)
		if !b.watchOn.Load() {
			continue
		}
		seq := sysClipboard.sequence()
		if seq == 0 || seq == b.lastSeq.Load() {
			continue
		}
		b.lastSeq.Store(seq)
		if seq == b.ownSeq.Load() {
			continue
		}
		if !target {
			b.w.Dispatch(func() { b.evalLocal("null", fmt.Sprintf(`{"seq": %d, "elsewhere": true}`, seq)) })
			continue
		}
		b.w.Dispatch(b.pushLocalClipboard)
	}
}

func (b *nativeBridge) pushLocalClipboard() {
	if !b.watchOn.Load() {
		return
	}
	info, err := b.readClipboard()
	if err != nil {
		return
	}
	if info.TooLarge {
		b.evalLocal("null", fmt.Sprintf(`{"tooLarge": true, "seq": %d}`, info.Seq))
		return
	}
	if !info.HasText || info.Text == "" {
		// Files, an image...: the page only needs to know the local side
		// copied last ("last copy wins" for Ctrl+V).
		b.evalLocal("null", fmt.Sprintf(`{"seq": %d, "hasFiles": %t}`, info.Seq, info.HasFiles))
		return
	}
	h := sha256.Sum256([]byte(info.Text))
	b.mu.Lock()
	echo := b.haveRemoteHash && h == b.lastRemoteHash
	b.mu.Unlock()
	if echo {
		return
	}
	js, err := json.Marshal(info.Text)
	if err != nil {
		return
	}
	b.evalLocal(string(js), fmt.Sprintf(`{"seq": %d}`, info.Seq))
}

// evalLocal hands clipboard text to the page, but only if the document on
// screen is the local UI (the webview may be showing the SSO page).
func (b *nativeBridge) evalLocal(textJSON, metaJSON string) {
	if b.closed.Load() {
		return
	}
	origin, _ := json.Marshal(b.origin)
	b.w.Eval("(function () { if (location.origin !== " + string(origin) + ") return; " +
		"var f = window.__reachOnLocalClipboard; if (typeof f === 'function') f(" + textJSON + ", " + metaJSON + "); })();")
}

func randomHandle() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
