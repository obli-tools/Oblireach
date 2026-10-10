//go:build windows

package main

// rdp_session.go — the local RDP/SSH connection tree's RDP transport
// (connections_page.go's kind=vaultKindRDP entries): launches the system's
// own mstsc.exe against a one-shot Credential Manager entry (never a saved
// password on disk), then reparents its window as a native WS_CHILD inside
// the Connections page's own content area — never a separate floating
// top-level window (rejected by design: a floating mstsc.exe drifts
// disconnected above the app's own window).
//
// This is NOT the Microsoft Terminal Services ActiveX control
// (MSTSCAX/IMsRdpClient) originally considered. Hosting that
// control means implementing an OLE container (IOleClientSite /
// IOleInPlaceSite / IOleInPlaceFrame) by hand — go-ole (present in the
// module cache) only covers COM *automation* of an already-hosted control,
// not standing up the container that hosts one — and the result would be
// hard to test without a live interactive RDP target to click
// through. Reparenting the real mstsc.exe still meets the actual
// requirement: the window this file manages never leaves the app's own
// window, is not resizable or movable on its own, and has no title bar or
// system menu of its own once embedded. The ActiveX path remains a possible
// future upgrade.
//
// Biggest unverified risk (documented, not swept aside): WebView2 renders
// through its own hardware-accelerated child window. A plain sibling HWND
// raised to the top of the z-order (as embed()/Resize() below do on every
// call) is the standard way apps overlay a native control on browser
// content, but it cannot be confirmed by automated tests that WebView2
// never repaints over it. This needs a manual check on a real interactive
// Windows session.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	procRdpEnumWindows              = modUser32.NewProc("EnumWindows")
	procRdpGetWindowThreadProcessId = modUser32.NewProc("GetWindowThreadProcessId")
	procRdpIsWindowVisible          = modUser32.NewProc("IsWindowVisible")
	procRdpGetWindowLongPtr         = modUser32.NewProc("GetWindowLongPtrW")
	procRdpSetWindowLongPtr         = modUser32.NewProc("SetWindowLongPtrW")
	procRdpSetParent                = modUser32.NewProc("SetParent")
	procRdpGetAncestor              = modUser32.NewProc("GetAncestor")
)

const (
	rdpGwlStyle  = ^uintptr(15) // GWL_STYLE = -16
	rdpWsChild   = 0x40000000
	rdpWsVisible = 0x10000000
	rdpWsPopup   = 0x80000000
	rdpWsCaption = 0x00C00000 // WS_BORDER | WS_DLGFRAME
	rdpWsThickFrame  = 0x00040000
	rdpWsSysMenu     = 0x00080000
	rdpWsMinimizeBox = 0x00020000
	rdpWsMaximizeBox = 0x00010000
	rdpGaRoot        = 2 // GA_ROOT

	rdpSwpFrameChanged = 0x0020
	rdpSwpShowWindow   = 0x0040
	rdpSwpNoActivate   = 0x0010

	rdpFindWindowTimeout = 20 * time.Second
	rdpFindWindowPoll    = 150 * time.Millisecond
)

var (
	errRdpUnavailable = errors.New("rdp: mstsc.exe not available")
	errRdpNoWindow    = errors.New("rdp: mstsc window not found in time")
)

// rdpAvailable reports whether the system has mstsc.exe (true on any normal
// Windows install; guards a stripped-down image / Windows Server Core).
func rdpAvailable() bool {
	_, err := exec.LookPath("mstsc.exe")
	return err == nil
}

type rdpSession struct {
	cmd        *exec.Cmd
	hwnd       uintptr
	parent     uintptr
	tempFile   string
	credTarget string
	closed     atomic.Bool
	onClosed   func(error)
}

// rdpConnect launches mstsc.exe against host:port as username/password (RDP
// entries are password-only in this vault, see vault.go), reparents its
// window into containerHwnd positioned at (x,y) sized (w,h), and returns
// once the window is embedded and shown — not once the remote session has
// finished negotiating. Any further prompt or error mstsc itself would show
// (certificate warning, wrong password) appears inside its own, now
// embedded, window exactly as it would standalone.
func rdpConnect(host string, port int, username, password string, containerHwnd uintptr, x, y, w, h int32, onClosed func(error)) (*rdpSession, error) {
	if !rdpAvailable() {
		return nil, errRdpUnavailable
	}
	credTarget := "TERMSRV/" + host

	// cmdkey stores the credential in the current user's Windows Credential
	// Manager for mstsc to read silently instead of prompting. Never
	// written into the .rdp file itself. Always deleted in Close() or by
	// the exit goroutine below, whichever runs — the one gap is a hard kill
	// of the whole app between here and either of those.
	addCmd := exec.Command("cmdkey.exe", "/generic:"+credTarget, "/user:"+username, "/pass:"+password)
	addCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := addCmd.Run(); err != nil {
		return nil, fmt.Errorf("cmdkey add: %w", err)
	}
	cleanupCred := func() {
		del := exec.Command("cmdkey.exe", "/delete:"+credTarget)
		del.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = del.Run()
	}

	tmp, err := os.CreateTemp("", "oblireach-rdp-*.rdp")
	if err != nil {
		cleanupCred()
		return nil, err
	}
	tmpPath := tmp.Name()
	fmt.Fprintf(tmp, "full address:s:%s:%d\r\nusername:s:%s\r\nprompt for credentials:i:0\r\nauthentication level:i:2\r\n", host, port, username)
	if cerr := tmp.Close(); cerr != nil {
		os.Remove(tmpPath)
		cleanupCred()
		return nil, cerr
	}

	cmd := exec.Command("mstsc.exe", tmpPath)
	if err := cmd.Start(); err != nil {
		os.Remove(tmpPath)
		cleanupCred()
		return nil, err
	}

	hwnd, err := rdpFindWindow(uint32(cmd.Process.Pid), rdpFindWindowTimeout)
	if err != nil {
		_ = cmd.Process.Kill()
		os.Remove(tmpPath)
		cleanupCred()
		return nil, err
	}

	s := &rdpSession{cmd: cmd, hwnd: hwnd, parent: containerHwnd, tempFile: tmpPath, credTarget: credTarget, onClosed: onClosed}
	s.embed(x, y, w, h)

	go func() {
		waitErr := cmd.Wait()
		if s.closed.CompareAndSwap(false, true) {
			os.Remove(tmpPath)
			cleanupCred()
			if s.onClosed != nil {
				s.onClosed(waitErr)
			}
		}
	}()
	return s, nil
}

// embed reparents hwnd into s.parent as a WS_CHILD filling (x,y,w,h),
// stripping the top-level frame styles it was created with (caption, thick
// resize frame, system menu, min/max boxes) and raising it to the top of
// its new siblings' z-order — the operator never sees or can use mstsc's
// own window chrome, only the app's "Back" bar around it.
func (s *rdpSession) embed(x, y, w, h int32) {
	style, _, _ := procRdpGetWindowLongPtr.Call(s.hwnd, rdpGwlStyle)
	style &^= rdpWsPopup | rdpWsCaption | rdpWsThickFrame | rdpWsSysMenu | rdpWsMinimizeBox | rdpWsMaximizeBox
	style |= rdpWsChild
	procRdpSetWindowLongPtr.Call(s.hwnd, rdpGwlStyle, style)
	procRdpSetParent.Call(s.hwnd, s.parent)
	procSetWindowPos.Call(s.hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), rdpSwpFrameChanged|rdpSwpShowWindow)
}

// Resize repositions the embedded window when the page's placeholder
// element moves or changes size (a ResizeObserver push from the JS side),
// re-asserting the top of the z-order each time in case WebView2 repainted
// over it in between (see this file's header — unverified without a real
// session).
func (s *rdpSession) Resize(x, y, w, h int32) {
	if s.closed.Load() {
		return
	}
	procSetWindowPos.Call(s.hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), rdpSwpNoActivate)
}

// Close ends the session: kills mstsc.exe (a plain WM_CLOSE could pop a
// "disconnect?" confirmation INSIDE the now chromeless, embedded window,
// with no way for the operator to see or answer it — killing the process
// is the only deterministic close available from an embedded child),
// removes the temp .rdp file and the Credential Manager entry.
func (s *rdpSession) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	os.Remove(s.tempFile)
	del := exec.Command("cmdkey.exe", "/delete:"+s.credTarget)
	del.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = del.Run()
}

// rdpFindWindow polls for a visible top-level window owned by pid — mstsc
// may create other owned windows (tooltips, a certificate prompt) before
// its main frame; GetAncestor(GA_ROOT) filters those out.
func rdpFindWindow(pid uint32, timeout time.Duration) (uintptr, error) {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var wpid uint32
		procRdpGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&wpid)))
		if wpid != pid {
			return 1
		}
		if vis, _, _ := procRdpIsWindowVisible.Call(hwnd); vis == 0 {
			return 1
		}
		if root, _, _ := procRdpGetAncestor.Call(hwnd, rdpGaRoot); root != hwnd {
			return 1
		}
		found = hwnd
		return 0
	})
	deadline := time.Now().Add(timeout)
	for {
		found = 0
		procRdpEnumWindows.Call(cb, 0)
		if found != 0 {
			return found, nil
		}
		if time.Now().After(deadline) {
			return 0, errRdpNoWindow
		}
		time.Sleep(rdpFindWindowPoll)
	}
}

// ---------------------------------------------------------------------------
// Container window: a plain child HWND of the page's own top-level window,
// positioned to match a DOM placeholder element (rdp_view.go), that hosts
// the reparented mstsc.exe window. Registered once, like the app's other
// window classes (webhost_windows.go's registerWindowClass).
// ---------------------------------------------------------------------------

func rdpContainerWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

func rdpCreateContainer(parent uintptr, x, y, w, h int32) (uintptr, error) {
	cls, err := registerWindowClass("OblireachRdpHost", rdpContainerWndProc)
	if err != nil {
		return 0, err
	}
	hinst, _, _ := procGetModuleHandleW.Call(0)
	empty, _ := syscall.UTF16PtrFromString("")
	hwnd, _, e := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(empty)),
		rdpWsChild|rdpWsVisible,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, 0, hinst, 0)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowEx: %v", e)
	}
	return hwnd, nil
}

func rdpResizeContainer(hwnd uintptr, x, y, w, h int32) {
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), rdpSwpNoActivate)
}

// rdpShowContainer: shown at (x,y,w,h) of its window, or hidden (its tab
// not on screen: another tab, another page, a dialog over it).
func rdpShowContainer(hwnd uintptr, show bool, x, y, w, h int32) {
	if !show {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
		return
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), rdpSwpNoActivate|rdpSwpShowWindow)
}

// rdpReparentContainer: the container into another window of the app (all
// of them live on the UI thread: a plain SetParent).
func rdpReparentContainer(hwnd, parent uintptr) {
	procRdpSetParent.Call(hwnd, parent)
}

func rdpDestroyContainer(hwnd uintptr) {
	procDestroyWindow.Call(hwnd)
}

// ---------------------------------------------------------------------------
// Registry: every live local RDP session, keyed by an opaque connection id
// the page holds (never a secret) — same shape as ssh_session.go's
// sshConnRegistry.
// ---------------------------------------------------------------------------

type rdpEntry struct {
	sess      *rdpSession
	container uintptr
	owner     *nativeBridge // the window showing it (its events go there): another one once moved (rdp_park.go)
}

type rdpConnRegistry struct {
	mu    sync.Mutex
	conns map[string]*rdpEntry
	seq   uint64
}

var appRDPRegistry = &rdpConnRegistry{conns: map[string]*rdpEntry{}}

func (r *rdpConnRegistry) newID() string {
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("lrdp_%d_%d", time.Now().UnixNano(), r.seq)
	r.mu.Unlock()
	return id
}

func (r *rdpConnRegistry) put(id string, s *rdpSession, container uintptr, owner *nativeBridge) {
	r.mu.Lock()
	r.conns[id] = &rdpEntry{sess: s, container: container, owner: owner}
	r.mu.Unlock()
}

// take removes and returns an entry (once: whoever takes it ends it).
func (r *rdpConnRegistry) take(id string) (*rdpEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.conns[id]
	if ok {
		delete(r.conns, id)
	}
	return e, ok
}

// ownerOf: the window showing it.
func (r *rdpConnRegistry) ownerOf(id string) *nativeBridge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.conns[id]; ok {
		return e.owner
	}
	return nil
}

func (r *rdpConnRegistry) setOwner(id string, b *nativeBridge) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.conns[id]
	if ok {
		e.owner = b
	}
	return ok
}

// ownedBy: the sessions a window shows (it closes: they end).
func (r *rdpConnRegistry) ownedBy(b *nativeBridge) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for id, e := range r.conns {
		if e.owner == b {
			out = append(out, id)
		}
	}
	return out
}

func (r *rdpConnRegistry) get(id string) (*rdpEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.conns[id]
	return e, ok
}

func (r *rdpConnRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.conns, id)
	r.mu.Unlock()
}
