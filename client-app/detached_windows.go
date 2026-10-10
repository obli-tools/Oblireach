//go:build windows

package main

// Detached session windows: remote sessions in windows of their own, so
// several sessions run side by side, on several screens.
//
// Each is a frameless host window with its own WebView2 (webHost,
// webhost_windows.go; frame_windows.go), created on the app's UI thread like
// the main window, whose message loop pumps the messages of every window of
// the thread. It shows the local UI in detached mode (/?detached=<device id>):
// focus mode, connected at once, under a slim title bar of the page's. Its
// page has its own native bridge (clipboard, Windows key, taskbar attention,
// title bar...).
//
// A session window holds TABS: one session per tab, in the title
// bar. Its "+" button connects another device in it (reachOpenTab), a tab
// dragged out of it goes to a new window at the drop point, or into another
// session window dropped on its title bar (reachTearOff), and the window
// closes with its last tab. The page tells which devices it has
// (reachWindowTabs): a device has a tab in ONE place only, so asking for it
// again (the main window, a "+" list) brings its window forward on its tab
// (__reachShowTab) instead of opening a second session.
//
// Unlike the main window, closing it closes its WebView2 controller (the
// page, and the remote sessions with it, would otherwise outlive the
// window), and never quits the app.
//
// Closing the window ends its sessions: its page is told first (it closes
// its tunnels cleanly), then the WebView2 controller is closed, which tears
// the page down whatever it does, and the window is destroyed.
//
// Everything here runs on the UI thread (bindings, dispatched functions and
// the window procedure), except Dispatch, which any goroutine may call.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

var (
	modGdi32 = syscall.NewLazyDLL("gdi32.dll")

	procRegisterClassExW    = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW     = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW      = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procShowWindow          = modUser32.NewProc("ShowWindow")
	procUpdateWindow        = modUser32.NewProc("UpdateWindow")
	procPostMessageW        = modUser32.NewProc("PostMessageW")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procIsIconic            = modUser32.NewProc("IsIconic")
	procSetTimer            = modUser32.NewProc("SetTimer")
	procKillTimer           = modUser32.NewProc("KillTimer")
	procGetWindowPlacement  = modUser32.NewProc("GetWindowPlacement")
	procSetWindowPlacement  = modUser32.NewProc("SetWindowPlacement")
	procLoadCursorW         = modUser32.NewProc("LoadCursorW")
	procSetWindowTextW      = modUser32.NewProc("SetWindowTextW")
	procWindowFromPoint     = modUser32.NewProc("WindowFromPoint")
	procGetWindow           = modUser32.NewProc("GetWindow")
	procIsWindowVisibleDW   = modUser32.NewProc("IsWindowVisible")
	procGetWindowRectDW     = modUser32.NewProc("GetWindowRect")
	procMonitorFromPoint    = modUser32.NewProc("MonitorFromPoint")
	procExtractIconExW      = modShell32.NewProc("ExtractIconExW")
	procGetModuleFileNameW  = modKernel32.NewProc("GetModuleFileNameW")
	procCreateSolidBrush    = modGdi32.NewProc("CreateSolidBrush")
)

const (
	gwHwndNext      = 2 // GetWindow: the window below in z-order
	wmDestroy       = 0x0002
	wmMove          = 0x0003
	wmSize          = 0x0005
	wmActivate      = 0x0006
	wmClose         = 0x0010
	wmGetMinMaxInfo = 0x0024
	wmTimer         = 0x0113
	wmMoving        = 0x0216

	wsOverlappedWindow = 0x00CF0000
	cwUseDefault       = 0x80000000
	swShowNormal       = 1
	swShowMaximized    = 3
	swRestore          = 9
	csHRedraw          = 0x0002
	csVRedraw          = 0x0001
	idcArrow           = 32512

	closeTimerID = 1
	// The page gets this long to close its tunnel before its WebView goes.
	closeGraceMs = 300

	defaultSessionW = 1280
	defaultSessionH = 820
	minSessionW     = 480
	minSessionH     = 320
	// The page's slim title bar (reach_viewer.go .win-titlebar), and so the
	// native strip while the WebView2 starts.
	sessionCaptionH = 34

	// (maxWindowTabs, the most tabs a session window reports:
	// detached_model.go.)
	// A torn-off tab's window: the drop point lands this far into its title
	// bar (96 DPI), and at least this much of it stays on the screen.
	tearGrabX      = 120
	tearMinVisible = 160
)

// WNDCLASSEXW
type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type winPoint struct{ X, Y int32 }

// MINMAXINFO
type minMaxInfo struct {
	PtReserved     winPoint
	PtMaxSize      winPoint
	PtMaxPosition  winPoint
	PtMinTrackSize winPoint
	PtMaxTrackSize winPoint
}

// WINDOWPLACEMENT
type windowPlacement struct {
	Length           uint32
	Flags            uint32
	ShowCmd          uint32
	PtMinPosition    winPoint
	PtMaxPosition    winPoint
	RcNormalPosition struct{ Left, Top, Right, Bottom int32 }
}

// ── Manager (main window bindings) ─────────────────────────────────────────

// sessionWindows opens and tracks the session windows and their tabs.
type sessionWindows struct {
	main   *nativeBridge // the main window's bridge (notifications, dispatch)
	proxy  *Proxy        // entry URL of the new windows
	origin string        // local UI origin
	// localfs: the main window's local file system, shared with every
	// session window for the local pane of its Files tabs (create:
	// attachSharedLocalFS); nil: none (tests).
	localfs *localFS
	// byDevice: the session window of each device that has a tab in one (a
	// device has ONE tab, in one window). Set when a window is created for a
	// device, when a page claims one for its "+" list or a tab moves, and
	// from what the pages report (reachWindowTabs).
	byDevice  map[string]*detachedWindow
	mainTabs  map[string]bool            // devices with a session tab in the main window (its report)
	opening   map[string]bool            // asked for, window not created yet (queued, or inside create)
	places    map[string]windowPlacement // where each device's window was last (this run)
	graveyard []*detachedWindow          // closed: native code may still hold their handlers
	seq       int                        // the last window id given (windows)
	// gen counts closeWindows calls (sign-out, server switch). A window asked
	// for before the last one is never shown: open skips it, or closes it at
	// once when closeWindows ran while it was being created (create's nested
	// message loop runs the main page's calls).
	gen uint64

	// Test seams (nil: the real Win32 calls).
	focusHook  func(hwnd uintptr)
	closeHook  func(hwnd uintptr)
	createHook func(r detachReq) (*detachedWindow, error)
	titleHook  func(hwnd uintptr, title string)
	cursorHook func() winPoint                                                               // where a tab was dropped
	dropHook   func(pt winPoint) (*detachedWindow, bool)                                     // the session window (or the main window, true) whose title bar is at pt
	placeHook  func(w *detachedWindow, pt winPoint) (size winPoint, work winRect, dpi int32) // tear-off geometry
}

// detachReq: a window asked for (the main page's openWindow, a tab torn off
// a session window), created later from the main loop (open).
type detachReq struct {
	deviceID, title string
	profile         string // server profile the device id belongs to ('' without profiles)
	kind            string // a tool window (toolKinds), a VM console of the device ("vm-<guid>"); '': the device's ObliReach session
	vmName          string // a VM console: its VM's name (vmWindowName)
	key             string // toolKey(profile, deviceID, kind): the device (and tool), whatever its server
	session         int    // Windows session; -1: automatic
	viewOnly        bool
	// A tool tab moving (tearOff): a terminal's park token (term_park.go: its
	// new page takes the parked tunnel over), the explorer's folder.
	park, path string
	merge      bool   // the last tab joining another window (detachOpts.Merge)
	into       string // a mosaic's tab joining the window of that key's tab (detachOpts.Into)
	toMain     bool   // the tab going to the main window (detachOpts.Main)
	window     int    // the tab going into that session window (detachOpts.Window)
	newWindow  bool   // the tab going to a new window at the cursor (detachOpts.NewWindow)
	gen        uint64 // sessionWindows.gen when asked
	// A torn-off tab: where its window goes (screen coordinates), and the
	// window it came from (it takes the tab back if the new one fails).
	rect *winRect
	from *detachedWindow
}

func newSessionWindows(main *nativeBridge, proxy *Proxy, origin string) *sessionWindows {
	return &sessionWindows{main: main, proxy: proxy, origin: origin,
		byDevice: map[string]*detachedWindow{}, mainTabs: map[string]bool{}, opening: map[string]bool{}, places: map[string]windowPlacement{}}
}

// Device keys (multi-server mode). Device ids of two servers
// overlap (both have a device 7), and several servers' devices are on screen
// at once: every map of the manager, and every id the pages and the app
// exchange about tabs (reachWindowTabs, __reachOnDetached, __reachShowTab,
// __reachCloseTab, reachFocusWindow, reachCloseWindow, reachOpenTab), is a
// KEY: "<profile>:<device id>", or the bare device id without a profile.
// The window's own URL and a tab handed over (__reachAdoptTab) keep the bare
// id and the profile apart. A tool tab (a terminal or the file explorer of
// the Obliance agent, terminal_viewer.go) is a key of its own: the device's
// key and "~<kind>" (toolKey), so a device has at most one ObliReach tab and
// one tab per tool, each in one place. A tool tab moves (tearOff) with what
// its new page needs: a terminal its park token (its shell goes on,
// term_park.go), the explorer its folder (reopened there).

// (devKey, toolKey, validDevKey, keyProfile, detachOpts and its check,
// detachTitle: detached_model.go.)

// openWindow (JS reachOpenWindow, main window): opens a session window for a
// device, or brings forward the one that has its tab, on that tab. Returns
// "opening" or "focused". The window is created from the main loop, not
// inside this WebView2 event: creating a WebView2 runs a nested message loop.
// opts.Into: the session goes as a new tab into the window holding that key
// (openInto: several devices ticked on the Devices page, devices_select.go).
func (b *nativeBridge) openWindow(tok, deviceID, title string, opts detachOpts) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	m := b.windows
	if m == nil {
		return "", errors.New(tr("detached.err.unavailable"))
	}
	if !validDetachID(deviceID) {
		return "", errors.New(tr("detached.err.invalidDevice"))
	}
	session, err := opts.check()
	if err != nil {
		return "", err
	}
	key := toolKey(opts.Profile, deviceID, opts.Kind) // (a tool window: its own key, next to the device's ObliReach one)
	if w := m.byDevice[key]; w != nil {
		m.show(w, key)
		return "focused", nil
	}
	if m.opening[key] {
		return "opening", nil
	}
	// (opts.Path: a main window's explorer detached, reopened on its folder)
	r := detachReq{deviceID: deviceID, title: detachTitle(title), profile: opts.Profile, kind: opts.Kind, vmName: vmWindowName(opts.VmName), key: key, session: session, viewOnly: opts.ViewOnly, path: opts.Path, gen: m.gen}
	if opts.Into != "" {
		return m.openInto(r, opts.Into)
	}
	if opts.Window > 0 {
		if toolParkKind(opts.Kind) {
			return "", errors.New(tr("detached.err.notMovable")) // (a terminal of the main window has no parked shell to hand over)
		}
		return m.openIntoWindow(r, opts.Window)
	}
	m.opening[key] = true
	b.w.Dispatch(func() { m.open(r) })
	return "opening", nil
}

// openInto: a device of the main window's batch (the Devices page's
// "Connect" / "Open as mosaic" on several ticked devices) joins the session
// window of the batch's first one (into: its key) as a new tab, like a tab
// moving there (__reachAdoptTab: the page connects it, on the Windows
// session given, or opens the tool). "moved"; "main" when the main window
// holds it already (shown there); "pending" while that window is still being
// created (the page asks again); an error when it has none any more.
func (m *sessionWindows) openInto(r detachReq, into string) (string, error) {
	if into == r.key {
		return "", errors.New(tr("detached.err.noSuchTab"))
	}
	if m.mainTabs[r.key] {
		m.focus(m.main.hwnd)
		m.main.evalMain("__reachShowTab", jsonString(r.key))
		return "main", nil
	}
	dst := m.byDevice[into]
	if dst == nil || dst.finished || dst.closing {
		if dst == nil && m.opening[into] {
			return "pending", nil
		}
		return "", errors.New(tr("detached.err.noSuchTab"))
	}
	m.byDevice[r.key] = dst
	dst.addTab(r.key)
	m.pageCall(dst, "__reachAdoptTab", r.adoptJSON())
	m.focus(dst.hwnd)
	m.notify()
	return "moved", nil
}

// openIntoWindow: a session or an explorer of the main window goes into the
// session window of that id ("Move to window", tab_move.go): its new tab
// there (__reachAdoptTab: connected again on the Windows session given, the
// explorer on its folder), "moved"; the main page then lets go of its own.
// An error when that window is gone.
func (m *sessionWindows) openIntoWindow(r detachReq, id int) (string, error) {
	dst := m.windowByID(id)
	if dst == nil {
		return "", errors.New(tr("detached.err.noSuchWindow"))
	}
	delete(m.mainTabs, r.key) // (the main page reports it gone right after)
	m.byDevice[r.key] = dst
	dst.addTab(r.key)
	m.pageCall(dst, "__reachAdoptTab", r.adoptJSON())
	m.focus(dst.hwnd)
	m.notify()
	return "moved", nil
}

// (windowTarget, a session window a tab may move into: detached_model.go.)

// windows: the session windows holding at least one tab, open (not closing),
// each once, by id (an id is given to each at its first listing).
func (m *sessionWindows) windows() []*detachedWindow {
	seen := map[*detachedWindow]bool{}
	var out []*detachedWindow
	for _, w := range m.byDevice {
		if w == nil || seen[w] || w.finished || w.closing {
			continue
		}
		seen[w] = true
		if w.id == 0 {
			m.seq++
			w.id = m.seq
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// windowByID: the open session window of that id (nil: none, or gone).
func (m *sessionWindows) windowByID(id int) *detachedWindow {
	if id <= 0 {
		return nil
	}
	for _, w := range m.windows() {
		if w.id == id {
			return w
		}
	}
	return nil
}

// windowTargets (JS reachWindowTargets, every window): the session windows a
// tab of this one may move into ("Move to window", tab_move.go), this one
// left out. The main window is not listed: a session window's page offers it
// itself (reachTearOff { main: true }).
func (b *nativeBridge) windowTargets(tok string) ([]windowTarget, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	m := b.windows
	if m == nil && b.detached != nil {
		m = b.detached.mgr
	}
	out := []windowTarget{}
	if m == nil {
		return out, nil
	}
	for _, w := range m.windows() {
		if w == b.detached {
			continue
		}
		title := w.title
		if title == "" {
			title = tr("win.sessionTitle")
		}
		out = append(out, windowTarget{ID: w.id, Title: title, Tabs: len(w.tabs)})
	}
	return out, nil
}

// focusWindow (JS reachFocusWindow): brings a device's window forward, on its
// tab. True as well while its window is being created (it comes up on its
// own): a second click on the device in the main window (detached mode,
// reachLaunchDevice) must not prompt for its session again.
// key: devKey (the device of a server profile).
func (b *nativeBridge) focusWindow(tok, key string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if b.windows == nil {
		return false, nil
	}
	w := b.windows.byDevice[key]
	if w == nil {
		return b.windows.opening[key], nil
	}
	b.windows.show(w, key)
	return true, nil
}

// closeWindow (JS reachCloseWindow): ends a device's session in its window,
// e.g. to bring it back into the main window: its tab closes, and the window
// with it when that was its last one.
// key: devKey (the device of a server profile).
func (b *nativeBridge) closeWindow(tok, key string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	m := b.windows
	if m == nil {
		return false, nil
	}
	w := m.byDevice[key]
	if w == nil {
		return false, nil
	}
	m.closeTab(w, key)
	return true, nil
}

// closeTab: a device's tab of window w ends; the window closes with its last tab.
func (m *sessionWindows) closeTab(w *detachedWindow, key string) {
	delete(m.byDevice, key)
	w.dropTab(key)
	if len(w.tabs) == 0 {
		m.close(w.hwnd)
	} else {
		m.pageCall(w, "__reachCloseTab", jsonString(key))
	}
}

// closeProfileWindows (JS reachCloseProfileWindows, main window): the sessions
// of one server profile end, in every session window (multi-server mode left
// for another server, that server removed): their tabs close, a window with
// no other tab closes, the windows asked for on that server are cancelled.
// The tabs of the other servers stay. Returns the number of tabs closed.
func (b *nativeBridge) closeProfileWindows(tok, profile string) (int, error) {
	if !b.allowed(tok) {
		return 0, errNotAllowed
	}
	if profile != "" && !validDetachID(profile) {
		return 0, errors.New(tr("detached.err.invalidServer"))
	}
	m := b.windows
	if m == nil {
		return 0, nil
	}
	for key := range m.opening {
		if keyProfile(key) == profile {
			delete(m.opening, key) // open skips it (or closes it, if being created)
		}
	}
	keys := make([]string, 0, len(m.byDevice))
	for key := range m.byDevice {
		if keyProfile(key) == profile {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	n := 0
	for _, key := range keys {
		if w := m.byDevice[key]; w != nil {
			m.closeTab(w, key)
			n++
		}
	}
	if n > 0 {
		m.notify()
	}
	return n, nil
}

// closeWindows (JS reachCloseWindows): closes every session window (sign-out,
// server switch: their sessions belong to the previous server session), and
// cancels the ones asked for but not created yet (queued, or being created):
// they would open signed out, or on the new server's device with that id.
func (b *nativeBridge) closeWindows(tok string) (int, error) {
	if !b.allowed(tok) {
		return 0, errNotAllowed
	}
	m := b.windows
	if m == nil {
		return 0, nil
	}
	m.gen++
	m.opening = map[string]bool{}
	seen := map[*detachedWindow]bool{} // a window with several tabs, once
	for _, w := range m.byDevice {
		if !seen[w] {
			seen[w] = true
			m.close(w.hwnd)
		}
	}
	return len(seen), nil
}

// closeAll closes every session window, the ones being created too, and
// cancels the ones asked for (the app quits for an update: quitApp). Each
// closes its own way (beginClose: its page ends its sessions).
func (m *sessionWindows) closeAll() int {
	m.gen++
	m.opening = map[string]bool{}
	n := 0
	for hwnd, w := range sessionWindowByHwnd {
		if w.mgr == m {
			m.close(hwnd)
			n++
		}
	}
	return n
}

// listWindows (JS reachListWindows): the devices that have their window (the
// main page asks after a reload; later changes come by __reachOnDetached).
func (b *nativeBridge) listWindows(tok string) ([]string, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	if b.windows == nil {
		return []string{}, nil
	}
	return b.windows.ids(), nil
}

// closeOwnWindow (JS reachCloseSelf, detached window): its "Close" buttons,
// its last tab closed.
func (b *nativeBridge) closeOwnWindow(tok string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	b.closeSelf()
	return true, nil
}

// (windowTabsInfo, cleanTabIDs: detached_model.go.)

// windowTabs (JS reachWindowTabs, every window): the page's session tabs, at
// every change. A session window: which devices it holds (and its title);
// the main window: its own session tabs (reachOpenTab brings it forward
// rather than opening a second session of one of them).
func (b *nativeBridge) windowTabs(tok string, info windowTabsInfo) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	ids := cleanTabIDs(info.IDs)
	switch {
	case b.windows != nil:
		b.windows.setMainTabs(ids)
	case b.detached != nil && b.detached.mgr != nil:
		b.detached.mgr.setTabs(b.detached, ids, info.Title)
	default:
		return false, nil
	}
	return true, nil
}

// openTab (JS reachOpenTab, session window): its "+" list picked a device.
// "self": it already has a tab here; "focused": another session window has
// it (brought forward, on that tab); "main": the main window has a session
// of it (brought forward, on it); "opening": a window is on its way for it;
// "here": nowhere else, claimed for this window (the page connects it in a
// new tab, or reports its tabs without it when the operator cancels).
// key: devKey (the device of a server profile).
func (b *nativeBridge) openTab(tok, key string) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	w := b.detached
	if w == nil || w.mgr == nil {
		return "", errors.New(tr("detached.err.notSessionWindow"))
	}
	if !validDevKey(key) {
		return "", errors.New(tr("detached.err.invalidDevice"))
	}
	return w.mgr.openTabIn(w, key), nil
}

// tearOff (JS reachTearOff, session window): one of its tabs was dragged out
// and released. Dropped on the title bar of another session window (or of
// the main window): the tab moves there ("moved"); anywhere else: a new
// session window opens at the drop point with that session ("opening"). An
// ObliReach session ends in this window (the page closes the tab) and starts
// again in the other one, on the same Windows session and view-only state. A
// terminal keeps its shell: its page parked the tunnel first
// (term_park.go) and the other window takes it over with opts.Park; a
// terminal without one is refused. The file explorer reopens there on its
// folder (opts.Path). The last tab of a window is never torn off (dragging
// it moves the window).
func (b *nativeBridge) tearOff(tok, deviceID, title string, opts detachOpts) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	w := b.detached
	if w == nil || w.mgr == nil {
		return "", errors.New(tr("detached.err.notSessionWindow"))
	}
	if !validDetachID(deviceID) {
		return "", errors.New(tr("detached.err.invalidDevice"))
	}
	session, err := opts.check()
	if err != nil {
		return "", err
	}
	// A terminal moves only parked (a second session of its shell would be
	// another shell: what the operator had there would be lost).
	if toolParkKind(opts.Kind) && opts.Park == "" {
		return "", errors.New(tr("detached.err.notMovable"))
	}
	return w.mgr.tearOffFrom(w, detachReq{deviceID: deviceID, title: detachTitle(title), profile: opts.Profile, kind: opts.Kind, vmName: vmWindowName(opts.VmName),
		key: toolKey(opts.Profile, deviceID, opts.Kind), session: session, viewOnly: opts.ViewOnly, park: opts.Park, path: opts.Path,
		merge: opts.Merge, into: opts.Into, toMain: opts.Main, window: opts.Window, newWindow: opts.NewWindow})
}

// adoptInfo: a tab another session window hands over (__reachAdoptTab).
type adoptInfo struct {
	DeviceID string `json:"deviceId"`
	Title    string `json:"title"`
	Session  int    `json:"session"`
	ViewOnly bool   `json:"viewOnly"`
	Profile  string `json:"profile"`
	Kind     string `json:"kind,omitempty"`   // a tool tab (toolKinds), a VM console ("vm-<guid>")
	VmName   string `json:"vmName,omitempty"` // ... a VM console's VM name
	Park     string `json:"park,omitempty"`   // ... a terminal's park token (term_park.go)
	Path     string `json:"path,omitempty"`   // ... the explorer's folder
}

func (r detachReq) adoptJSON() string {
	j, _ := json.Marshal(adoptInfo{DeviceID: r.deviceID, Title: r.title, Session: r.session, ViewOnly: r.viewOnly, Profile: r.profile,
		Kind: r.kind, VmName: r.vmName, Park: r.park, Path: r.path})
	return string(j)
}

func (m *sessionWindows) focus(hwnd uintptr) {
	if m.focusHook != nil {
		m.focusHook(hwnd)
		return
	}
	// The main window hidden in the notification area (tray_windows.go):
	// back on screen first.
	if mainTray != nil && mainTray.hidden && hwnd == mainTray.hwnd {
		mainTray.restore()
		return
	}
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procSetForegroundWindow.Call(hwnd)
}

func (m *sessionWindows) close(hwnd uintptr) {
	if m.closeHook != nil {
		m.closeHook(hwnd)
		return
	}
	procPostMessageW.Call(hwnd, wmClose, 0, 0)
}

func (m *sessionWindows) ids() []string {
	out := make([]string, 0, len(m.byDevice))
	for id := range m.byDevice {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// notify tells the main page which devices have a tab in a session window
// (window.__reachOnDetached): its Remote pane shows "open in its own window".
func (m *sessionWindows) notify() {
	list, _ := json.Marshal(m.ids())
	m.main.evalMain("__reachOnDetached", string(list))
}

// pageCall calls window.<fn>(<args>) in a session window's page (the local
// UI only).
func (m *sessionWindows) pageCall(w *detachedWindow, fn, args string) {
	if w != nil && w.bridge != nil {
		w.bridge.evalMain(fn, args)
	}
}

// show brings a session window forward on a device's tab (key: devKey).
func (m *sessionWindows) show(w *detachedWindow, key string) {
	m.focus(w.hwnd)
	m.pageCall(w, "__reachShowTab", jsonString(key))
}

// setTitle: the window's title (taskbar, Alt+Tab): the device on screen.
func (m *sessionWindows) setTitle(w *detachedWindow, title string) {
	w.title = title // (its name in "Move to window": windowTargets)
	if m.titleHook != nil {
		m.titleHook(w.hwnd, title)
		return
	}
	if w.hwnd == 0 || w.finished {
		return
	}
	t, err := syscall.UTF16PtrFromString(title)
	if err == nil {
		procSetWindowTextW.Call(w.hwnd, uintptr(unsafe.Pointer(t)))
	}
}

func (m *sessionWindows) setMainTabs(ids []string) {
	m.mainTabs = map[string]bool{}
	for _, id := range ids {
		m.mainTabs[id] = true
	}
}

// setTabs: what a session window's page reports. A device another window
// holds stays there (a device has one tab), and so does one a window is
// being created for (a report sent before the page learnt its tab was torn
// off); one this window no longer lists is free again. The main page hears
// of any change.
func (m *sessionWindows) setTabs(w *detachedWindow, ids []string, title string) {
	if w.finished {
		return
	}
	keep := map[string]bool{}
	for _, id := range ids {
		keep[id] = true
	}
	changed := false
	for id, ow := range m.byDevice {
		if ow == w && !keep[id] {
			delete(m.byDevice, id)
			changed = true
		}
	}
	tabs := make([]string, 0, len(ids))
	for _, id := range ids {
		if ow := m.byDevice[id]; (ow != nil && ow != w) || m.opening[id] {
			continue // another window has it, or one is on its way for it (a torn-off tab)
		}
		if m.byDevice[id] != w {
			m.byDevice[id] = w
			changed = true
		}
		tabs = append(tabs, id)
	}
	w.tabs = tabs
	if title != "" {
		m.setTitle(w, detachTitle(title))
	}
	if changed {
		m.notify()
	}
}

// openTabIn: a session window's "+" list asks for a device (openTab; key: devKey).
func (m *sessionWindows) openTabIn(w *detachedWindow, key string) string {
	if ow := m.byDevice[key]; ow != nil {
		if ow == w {
			return "self"
		}
		m.show(ow, key)
		return "focused"
	}
	if m.opening[key] {
		return "opening"
	}
	if m.mainTabs[key] {
		m.focus(m.main.hwnd)
		m.main.evalMain("__reachShowTab", jsonString(key))
		return "main"
	}
	m.byDevice[key] = w
	w.addTab(key)
	m.notify()
	return "here"
}

// tearOffFrom: a tab of w was dropped (tearOff). r: the tab's session.
func (m *sessionWindows) tearOffFrom(w *detachedWindow, r detachReq) (string, error) {
	if r.key == "" {
		r.key = toolKey(r.profile, r.deviceID, r.kind)
	}
	if toolParkKind(r.kind) && r.park == "" {
		return "", errors.New(tr("detached.err.notMovable")) // (a terminal moves parked only: see tearOff)
	}
	if m.byDevice[r.key] != w || !w.hasTab(r.key) {
		return "", errors.New(tr("detached.err.noSuchTab"))
	}
	pt := m.cursorPos()
	var dst *detachedWindow
	var onMain bool
	switch {
	case r.into != "":
		// A member of a mosaic following its first one: where that one went
		// (never a new window: it is there already), whatever the cursor.
		if r.into == r.key {
			return "", errors.New(tr("detached.err.noSuchTab"))
		}
		if m.mainTabs[r.into] {
			onMain = true
		} else if dst = m.byDevice[r.into]; dst == nil || dst == w || dst.finished || dst.closing {
			return "", errors.New(tr("detached.err.noSuchTab"))
		}
	case r.toMain:
		// Asked for the main window (never a new window), wherever the cursor is.
		if m.main == nil {
			return "", errors.New(tr("detached.err.unavailable"))
		}
		onMain = true
	case r.window > 0:
		// "Move to window" (tab_move.go): into that session window, wherever
		// the cursor is; the last tab too (this window then closes).
		if dst = m.windowByID(r.window); dst == nil || dst == w {
			return "", errors.New(tr("detached.err.noSuchWindow"))
		}
	case r.newWindow:
		// "Move to window > New window": a new window at the cursor, whatever
		// is under it (the menu is over this window). Not this window's last tab.
		if len(w.tabs) < 2 {
			return "", errors.New(tr("detached.err.lastTab"))
		}
	case r.merge:
		// The only tab of a window that was just dragged whole (one tab, or
		// the first member of its only mosaic): it may only join the window
		// it was dropped on (under this one), never a new one.
		dst, onMain = m.dropTargetBelow(pt, w.hwnd)
		if !onMain && (dst == nil || dst == w) {
			return "", errors.New(tr("detached.err.lastTab"))
		}
	default:
		if len(w.tabs) < 2 {
			return "", errors.New(tr("detached.err.lastTab"))
		}
		dst, onMain = m.dropTarget(pt)
	}
	if onMain {
		// Onto the main window's tab strip: the tab regroups there (the
		// reverse of tearing it out of it in the first place).
		w.dropTab(r.key)
		delete(m.byDevice, r.key)
		m.mainTabs[r.key] = true
		m.main.evalMain("__reachAdoptTab", r.adoptJSON())
		m.focus(m.main.hwnd)
		m.notify()
		return "moved", nil
	}
	if dst != nil && dst != w && !dst.finished && !dst.closing {
		// Onto another session window's title bar: the tab goes there.
		w.dropTab(r.key)
		m.byDevice[r.key] = dst
		dst.addTab(r.key)
		m.pageCall(dst, "__reachAdoptTab", r.adoptJSON())
		m.focus(dst.hwnd)
		return "moved", nil
	}
	// A new window where the tab was dropped, the size of this one. The
	// device is "opening" until it exists (not in byDevice: the main page
	// keeps it as detached, it hears of its new window from open).
	size, work, dpi := m.placement(w, pt)
	grab := winPoint{min32(scaleDpi(tearGrabX, dpi), size.X/3), scaleDpi(sessionCaptionH, dpi) / 2}
	rect := tearOffRect(pt, size, grab, work, scaleDpi(sessionCaptionH, dpi), scaleDpi(tearMinVisible, dpi))
	w.dropTab(r.key)
	delete(m.byDevice, r.key)
	m.opening[r.key] = true
	r.gen, r.rect, r.from = m.gen, &rect, w
	m.main.w.Dispatch(func() { m.open(r) })
	return "opening", nil
}

// tearOffRect: where a torn-off tab's window goes: size (width, height), the
// drop point grab into it (on its title bar), kept on the screen it was
// dropped on: its title bar inside the work area vertically, at least
// minVisible of it horizontally. (A window larger than the work area keeps
// its size: Windows clips it like any other.)
func tearOffRect(pt, size, grab winPoint, work winRect, caption, minVisible int32) winRect {
	left, top := pt.X-grab.X, pt.Y-grab.Y
	if work.Right > work.Left && work.Bottom > work.Top {
		vis := min32(minVisible, size.X)
		if left > work.Right-vis {
			left = work.Right - vis
		}
		if left+size.X < work.Left+vis {
			left = work.Left + vis - size.X
		}
		if top > work.Bottom-caption {
			top = work.Bottom - caption
		}
		if top < work.Top {
			top = work.Top
		}
	}
	return winRect{left, top, left + size.X, top + size.Y}
}

// cursorPos: where the mouse is (the drop point of a torn-off tab: the page's
// own coordinates are not trusted, nor needed).
func (m *sessionWindows) cursorPos() winPoint {
	if m.cursorHook != nil {
		return m.cursorHook()
	}
	var pt winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return pt
}

// pointArgs: a POINT passed by value (8 bytes: one register, two on 386).
func pointArgs(pt winPoint) []uintptr {
	if runtime.GOARCH == "386" {
		return []uintptr{uintptr(pt.X), uintptr(pt.Y)}
	}
	return []uintptr{uintptr(uint64(uint32(pt.X)) | uint64(uint32(pt.Y))<<32)}
}

// dropTarget: the window whose title bar (the page's slim one for a session
// window, the top bar for the main one — both hold the tab strip) is at
// screen point pt, if any: another session window, or the main window
// (onMain), so a torn-off tab can regroup into either.
func (m *sessionWindows) dropTarget(pt winPoint) (dst *detachedWindow, onMain bool) {
	if m.dropHook != nil {
		return m.dropHook(pt)
	}
	hit, _, _ := procWindowFromPoint.Call(pointArgs(pt)...)
	if hit == 0 {
		return nil, false
	}
	root, _, _ := procGetAncestor.Call(hit, gaRoot)
	if m.main != nil && root == m.main.hwnd {
		return nil, inCaptionBand(root, pt, mainCaptionH)
	}
	w := sessionWindowByHwnd[root]
	if w == nil || !w.ready || w.finished {
		return nil, false
	}
	if !inCaptionBand(root, pt, sessionCaptionH) {
		return nil, false
	}
	return w, false
}

// dropTargetBelow: like dropTarget, for a window self that was just dragged
// (it is under the cursor itself): the first visible top-level window below
// it in z-order that contains pt decides — another session window's title
// bar, or the main window's top bar.
func (m *sessionWindows) dropTargetBelow(pt winPoint, self uintptr) (dst *detachedWindow, onMain bool) {
	if m.dropHook != nil {
		return m.dropHook(pt)
	}
	for h, _, _ := procGetWindow.Call(self, gwHwndNext); h != 0; h, _, _ = procGetWindow.Call(h, gwHwndNext) {
		if v, _, _ := procIsWindowVisibleDW.Call(h); v == 0 {
			continue
		}
		var rc winRect
		procGetWindowRectDW.Call(h, uintptr(unsafe.Pointer(&rc)))
		if pt.X < rc.Left || pt.X >= rc.Right || pt.Y < rc.Top || pt.Y >= rc.Bottom {
			continue
		}
		if m.main != nil && h == m.main.hwnd {
			return nil, inCaptionBand(h, pt, mainCaptionH)
		}
		w := sessionWindowByHwnd[h]
		if w == nil || !w.ready || w.finished || !inCaptionBand(h, pt, sessionCaptionH) {
			return nil, false
		}
		return w, false
	}
	return nil, false
}

// tabDragEnded: a window was dragged by its only tab (frameProc wmHostDrag):
// dropped onto another window's tab strip, its page is asked to hand the tab
// over (__reachMergeTab: a terminal parks its shell first, like any tab
// move); anywhere else, the window simply moved.
// (Its page started that drag only with a single tab in its strip: one tab,
// or one mosaic of several, tab_groups.go.)
func (m *sessionWindows) tabDragEnded(w *detachedWindow) {
	if w == nil || w.finished || w.closing || len(w.tabs) == 0 {
		return
	}
	dst, onMain := m.dropTargetBelow(m.cursorPos(), w.hwnd)
	if !onMain && (dst == nil || dst == w) {
		return
	}
	m.pageCall(w, "__reachMergeTab", "")
}

// inCaptionBand: screen point pt falls inside root's client area, within
// captionH (96-DPI) of its top.
func inCaptionBand(root uintptr, pt winPoint, captionH int32) bool {
	cp := pt
	procScreenToClient.Call(root, uintptr(unsafe.Pointer(&cp)))
	var rc winRect
	procGetClientRect.Call(root, uintptr(unsafe.Pointer(&rc)))
	return cp.X >= 0 && cp.X < rc.Right && cp.Y >= 0 && cp.Y < scaleDpi(captionH, dpiOf(root))
}

// placement: a torn-off tab's window: the size of the window it comes from
// (restored), the work area of the screen at pt, that screen's DPI; the size
// is converted to it (dpi_windows.go tearOffSize: the same size in DIPs on a
// screen of another scale).
func (m *sessionWindows) placement(w *detachedWindow, pt winPoint) (winPoint, winRect, int32) {
	if m.placeHook != nil {
		return m.placeHook(w, pt)
	}
	dpi := dpiOf(w.hwnd)
	size := winPoint{scaleDpi(defaultSessionW, dpi), scaleDpi(defaultSessionH, dpi)}
	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := procGetWindowPlacement.Call(w.hwnd, uintptr(unsafe.Pointer(&wp))); r != 0 {
		if nw, nh := wp.RcNormalPosition.Right-wp.RcNormalPosition.Left, wp.RcNormalPosition.Bottom-wp.RcNormalPosition.Top; nw > 0 && nh > 0 {
			size = winPoint{nw, nh}
		}
	}
	var work winRect
	to := dpi
	args := append(pointArgs(pt), monitorNearest)
	if mon, _, _ := procMonitorFromPoint.Call(args...); mon != 0 {
		mi := monitorInfo{}
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			work = mi.RcWork
		}
		to = monitorDpi(mon)
	}
	return tearOffSize(size, dpi, to), work, to
}

// open creates the window of a device (main loop, UI thread). The device
// stays "opening" until the window exists (a second click meanwhile is not
// queued again).
func (m *sessionWindows) open(r detachReq) {
	if r.key == "" {
		r.key = toolKey(r.profile, r.deviceID, r.kind)
	}
	if r.gen != m.gen || !m.opening[r.key] {
		return // closeWindows (sign-out, server switch) or closeProfileWindows ran since it was asked for
	}
	if w := m.byDevice[r.key]; w != nil {
		delete(m.opening, r.key)
		m.focus(w.hwnd)
		return
	}
	create := m.create
	if m.createHook != nil {
		create = m.createHook
	}
	w, err := create(r)
	if r.gen != m.gen || !m.opening[r.key] {
		// closeWindows / closeProfileWindows ran inside that nested loop: this
		// window goes at once (create gave it no page), and the main page
		// never hears of it.
		if err != nil {
			log.Printf("session window: %v", err)
		} else if w != nil {
			m.close(w.hwnd)
		}
		return
	}
	delete(m.opening, r.key)
	if err != nil {
		log.Printf("session window: %v", err)
		if f := r.from; f != nil && !f.finished && !f.closing && m.byDevice[r.key] == nil {
			// A torn-off tab whose window could not open: back where it was.
			m.byDevice[r.key] = f
			f.addTab(r.key)
			m.pageCall(f, "__reachAdoptTab", r.adoptJSON())
			return
		}
		msg, _ := json.Marshal(err.Error())
		id, _ := json.Marshal(r.key)
		m.main.evalMain("__reachOnDetachFailed", string(id)+", "+string(msg))
		return
	}
	if w.key == "" {
		w.key = r.key
	}
	m.byDevice[r.key] = w
	w.tabs = []string{r.key}
	m.notify()
}

// destroyed: WM_DESTROY of a session window: its devices are free again.
func (m *sessionWindows) destroyed(w *detachedWindow) {
	if w.bridge != nil {
		w.bridge.shutdown()
		bridges.unregister(w.hwnd)
	}
	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := procGetWindowPlacement.Call(w.hwnd, uintptr(unsafe.Pointer(&wp))); r != 0 {
		m.places[w.placeKey()] = wp
		for _, id := range w.tabs {
			m.places[id] = wp
		}
	}
	delete(sessionWindowByHwnd, w.hwnd)
	m.forget(w)
	m.graveyard = append(m.graveyard, w)
	m.notify()
}

// forget: the devices of a window gone.
func (m *sessionWindows) forget(w *detachedWindow) {
	for id, ow := range m.byDevice {
		if ow == w {
			delete(m.byDevice, id)
		}
	}
	w.tabs = nil
}

// ── Session window ─────────────────────────────────────────────────────────

// detachedWindow is a session window (its webHost implements bridgeHost).
type detachedWindow struct {
	webHost
	mgr      *sessionWindows
	deviceID string   // the device it was opened for
	key      string   // ... and its key (devKey: with its server profile)
	tabs     []string // the device keys it has a tab for (setTabs; the manager's view)
	bridge   *nativeBridge
	id       int    // its id for "Move to window" (sessionWindows.windows; 0: none yet)
	title    string // its title (setTitle): the device on screen

	closeLate bool // WM_CLOSE while the WebView2 was being created: closed once ready
	closing   bool // WM_CLOSE seen: the page was told, the timer runs
}

// placeKey: where its place is kept (m.places): its first device's key.
func (w *detachedWindow) placeKey() string {
	if w.key != "" {
		return w.key
	}
	return w.deviceID
}

func (w *detachedWindow) hasTab(id string) bool {
	for _, t := range w.tabs {
		if t == id {
			return true
		}
	}
	return false
}

func (w *detachedWindow) addTab(id string) {
	if !w.hasTab(id) {
		w.tabs = append(w.tabs, id)
	}
}

func (w *detachedWindow) dropTab(id string) {
	out := w.tabs[:0]
	for _, t := range w.tabs {
		if t != id {
			out = append(out, t)
		}
	}
	w.tabs = out
}

var sessionWindowByHwnd = map[uintptr]*detachedWindow{} // UI thread only

// create builds the window, its WebView2 and its bridge, then opens the page
// (not when closeWindows ran during Embed: open closes that window).
func (m *sessionWindows) create(r detachReq) (*detachedWindow, error) {
	cls, err := registerWindowClass("OblireachSessionWindow", sessionWndProc)
	if err != nil {
		return nil, err
	}
	deviceID := r.deviceID
	if r.key == "" {
		r.key = toolKey(r.profile, deviceID, r.kind)
	}
	w := &detachedWindow{mgr: m, deviceID: deviceID, key: r.key}
	w.origin = m.origin
	w.frame = frameState{minW: minSessionW, minH: minSessionH, captionH: sessionCaptionH}
	titleW, _ := syscall.UTF16PtrFromString(r.title)
	hinst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, e := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(titleW)),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, defaultSessionW, defaultSessionH,
		0, 0, hinst, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowEx: %v", e)
	}
	w.hwnd = hwnd
	sessionWindowByHwnd[hwnd] = w
	frameReady(hwnd) // our frame, before the window is ever shown

	// A torn-off tab: where it was dropped. Else back where this device's
	// window was last closed (this run); the system moves it onto a screen if
	// that screen is gone.
	// Both rectangles are physical pixels meant for the screen they are on
	// (dpi_windows.go): placed as they are, whatever that screen's scale.
	if rc := r.rect; rc != nil {
		w.place(func() {
			procSetWindowPos.Call(hwnd, 0, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top), swpNoZOrder|swpNoActivate|swpFrameChanged)
			procShowWindow.Call(hwnd, swShowNormal)
		})
	} else if wp, ok := m.places[r.key]; ok {
		if wp.ShowCmd != swShowMaximized {
			wp.ShowCmd = swShowNormal
		}
		w.place(func() { procSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))) })
	} else {
		// The default size, at the DPI of the screen Windows put it on.
		dpi := dpiOf(hwnd)
		w.setInitialSize(scaleDpi(defaultSessionW, dpi), scaleDpi(defaultSessionH, dpi))
		procShowWindow.Call(hwnd, swShowNormal)
	}
	procUpdateWindow.Call(hwnd)

	if err := w.embed(); err != nil {
		w.finished = true
		procDestroyWindow.Call(hwnd)
		return nil, err
	}

	nb, err := newNativeBridge(w, m.origin)
	if err != nil {
		w.finished = true
		closeController(w.chromium)
		procDestroyWindow.Call(hwnd)
		return nil, err
	}
	nb.closeSelf = func() { procPostMessageW.Call(hwnd, wmClose, 0, 0) }
	nb.detached = w // its tab bindings
	nb.win = hostFrame{&w.webHost}
	if m.proxy != nil {
		nb.tunnels = m.proxy.tunnels
	}
	if m.localfs != nil {
		nb.attachSharedLocalFS(m.localfs) // (the local pane of its Files tabs)
	}
	w.frame.onState = nb.pushWindowState
	w.frame.onTabDragEnd = func() { m.tabDragEnded(w) }
	w.bridge = nb
	w.Init(fmt.Sprintf(`window.__reach_version = %q;`, appVersion))
	nb.bind()
	bridges.register(hwnd, nb)
	go nb.watch()

	if r.gen == m.gen {
		w.Navigate(vmEntryURL(m.proxy.detachedToolEntryURL(deviceID, r.profile, r.session, r.viewOnly, r.kind, r.park, r.path), r.kind, r.vmName))
		procSetForegroundWindow.Call(hwnd)
		w.chromium.Focus()
	}
	if w.closeLate {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
	return w, nil
}

// sessionWndProc is the window procedure of every session window.
func sessionWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	w := sessionWindowByHwnd[hwnd]
	if w == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	if r, done := w.frameProc(msg, wParam, lParam); done {
		return r
	}
	switch msg {
	case wmHostDispatch:
		w.drain()
		return 0
	case wmActivate:
		// Back to this window (Alt+Tab, taskbar): the keyboard goes to the
		// page (the session picture), not to the empty host window.
		if wParam&0xFFFF != waInactive && w.ready && !w.finished && !w.closing {
			w.chromium.Focus()
		}
	case wmClose:
		w.beginClose()
		return 0
	case wmTimer:
		if wParam == closeTimerID {
			w.finishClose()
			return 0
		}
	case wmDestroy:
		w.markGone()
		w.mgr.destroyed(w)
		return 0 // never PostQuitMessage: the app goes on
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// beginClose: the page ends its session cleanly, then (timer) the WebView
// and the window go.
func (w *detachedWindow) beginClose() {
	if !w.ready && !w.finished {
		// Still inside Embed (its nested loop runs this): destroying the
		// parent now would make the pending WebView2 creation fail, which
		// the edge package treats as fatal. Closed as soon as it is ready.
		w.closeLate = true
		return
	}
	if w.closing {
		return
	}
	w.closing = true
	w.Eval("(function () { var f = window.__reachDetachedClosing; if (typeof f === 'function') { try { f(); } catch (e) {} } })();")
	if r, _, _ := procSetTimer.Call(w.hwnd, closeTimerID, closeGraceMs, 0); r == 0 {
		w.finishClose()
	}
}

func (w *detachedWindow) finishClose() {
	procKillTimer.Call(w.hwnd, closeTimerID)
	if w.finished {
		return
	}
	w.finished = true
	if w.bridge != nil {
		w.bridge.shutdown()
		bridges.unregister(w.hwnd)
	}
	closeController(w.chromium)
	procDestroyWindow.Call(w.hwnd)
}

// closeController closes a WebView2 (ICoreWebView2Controller::Close): its
// page and everything it holds (the session tunnel) end now. The edge
// package keeps the vtable private: this is the COM ABI, where Close is slot
// 24 (IUnknown's 3 methods, then IsVisible get/put, Bounds get/put,
// ZoomFactor get/put, ZoomFactorChanged add/remove, SetBoundsAndZoomFactor,
// MoveFocus, MoveFocusRequested / GotFocus / LostFocus /
// AcceleratorKeyPressed add/remove, ParentWindow get/put,
// NotifyParentWindowPositionChanged, Close).
func closeController(c *edge.Chromium) {
	if c == nil {
		return
	}
	ctrl := c.GetController()
	if ctrl == nil {
		return
	}
	vtbl := *(**[26]uintptr)(unsafe.Pointer(ctrl))
	syscall.SyscallN(vtbl[24], uintptr(unsafe.Pointer(ctrl)))
}

// (emitServers, evalMain: native_events.go.)
