//go:build linux

package main

// dbus_linux.go — the session bus and the XDG desktop portals, shared by the
// Linux platform services (secretservice_linux.go, singleinstance_linux.go,
// portal_files_linux.go, notify_linux.go, startup_linux.go, localfs_linux.go).
//
// Pure Go (github.com/godbus/dbus/v5): no cgo, nothing of GTK. Inside the
// Flatpak the session bus is xdg-dbus-proxy's filtered view: only the names
// the manifest grants (flatpak/tools.obli.Oblireach.yml finish-args) and the
// portals (org.freedesktop.portal.*, always reachable) answer; every other
// name looks absent (ServiceUnknown).
//
// One private connection for the whole process (sessionBus), opened on first
// use; the tests point dbusDial at a private dbus-daemon.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalBusName = "org.freedesktop.portal.Desktop"
	portalPath    = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalRequest = "org.freedesktop.portal.Request"

	// The portal's answers (Request.Response).
	portalResponseOK        = 0
	portalResponseCancelled = 1
	portalResponseOther     = 2

	// How long an interactive portal request may stay open (a file dialog,
	// an application chooser, a permission question): the operator may take
	// their time, but never forever.
	portalInteractiveTimeout = 30 * time.Minute
	// A request that shows nothing (Background, OpenURI without chooser).
	portalQuickTimeout = 2 * time.Minute
)

var (
	errNoSessionBus = errors.New("no D-Bus session bus")
	errPortalAbsent = errors.New("xdg-desktop-portal is not available")
	errPortalCancel = errors.New("cancelled")
	errPortalFailed = errors.New("the portal refused the request")
)

// dbusDial opens the process's session bus connection (tests: a private
// bus). The connection must support Unix fd passing (OpenURI, Trash).
var dbusDial = func() (*dbus.Conn, error) { return dbus.ConnectSessionBus() }

var (
	busMu   sync.Mutex
	busConn *dbus.Conn
)

// sessionBus: the shared connection, (re)opened when absent or closed.
func sessionBus() (*dbus.Conn, error) {
	busMu.Lock()
	defer busMu.Unlock()
	if busConn != nil && busConn.Connected() {
		return busConn, nil
	}
	c, err := dbusDial()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoSessionBus, err)
	}
	busConn = c
	return c, nil
}

// resetSessionBus drops the shared connection (tests, between two buses).
func resetSessionBus() {
	busMu.Lock()
	defer busMu.Unlock()
	if busConn != nil {
		busConn.Close()
		busConn = nil
	}
}

// inFlatpak: this process runs inside a Flatpak sandbox.
func inFlatpak() bool {
	if os.Getenv("OBLIREACH_FAKE_FLATPAK") == "1" { // (tests)
		return true
	}
	_, err := os.Stat("/.flatpak-info")
	return err == nil
}

// dbusErrName: the D-Bus error name of err ("" when it is not one).
func dbusErrName(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name
	}
	var dp *dbus.Error
	if errors.As(err, &dp) && dp != nil {
		return dp.Name
	}
	return ""
}

// dbusNameMissing: the call failed because nobody owns the name (or, in the
// Flatpak, the proxy hides it).
func dbusNameMissing(err error) bool {
	switch dbusErrName(err) {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.Spawn.ServiceNotFound",
		"org.freedesktop.DBus.Error.Spawn.ChildExited", "org.freedesktop.DBus.Error.Spawn.Failed":
		return true
	}
	return false
}

// dbusNoMethod: the peer exists but has no such method / interface / object
// (an older portal version, a partial implementation).
func dbusNoMethod(err error) bool {
	switch dbusErrName(err) {
	case "org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.UnknownObject", "org.freedesktop.DBus.Error.NotSupported":
		return true
	}
	return false
}

// ── Signals ────────────────────────────────────────────────────────────────

// signalWaiter: the signals of one object and member, from before the call
// that triggers them (no race with a fast answer).
type signalWaiter struct {
	conn  *dbus.Conn
	ch    chan *dbus.Signal
	opts  []dbus.MatchOption
	path  dbus.ObjectPath
	name  string // interface.member
	close sync.Once
}

func watchSignal(conn *dbus.Conn, path dbus.ObjectPath, iface, member string) (*signalWaiter, error) {
	w := &signalWaiter{
		conn: conn, ch: make(chan *dbus.Signal, 16), path: path, name: iface + "." + member,
		opts: []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(iface), dbus.WithMatchMember(member)},
	}
	if err := conn.AddMatchSignal(w.opts...); err != nil {
		return nil, err
	}
	conn.Signal(w.ch)
	return w, nil
}

// wait: the first matching signal's body, or errPortalCancel at timeout.
func (w *signalWaiter) wait(timeout time.Duration) ([]interface{}, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case s, ok := <-w.ch:
			if !ok {
				return nil, errNoSessionBus
			}
			if s.Path == w.path && s.Name == w.name {
				return s.Body, nil
			}
		case <-t.C:
			return nil, fmt.Errorf("%w: no answer within %s", errPortalCancel, timeout)
		}
	}
}

func (w *signalWaiter) stop() {
	w.close.Do(func() {
		w.conn.RemoveSignal(w.ch)
		_ = w.conn.RemoveMatchSignal(w.opts...)
	})
}

// ── Portal requests ────────────────────────────────────────────────────────

var portalTokenSeq atomic.Uint64

// newHandleToken: a handle_token the request object's path is built from
// (letters, digits and '_' only).
func newHandleToken() string {
	return fmt.Sprintf("oblireach_%d_%s", portalTokenSeq.Add(1), randHex(4))
}

// portalRequestPath: the Request object a call with this token creates
// (/org/freedesktop/portal/desktop/request/SENDER/TOKEN, SENDER the unique
// name without ':' and with '.' as '_').
func portalRequestPath(conn *dbus.Conn, token string) dbus.ObjectPath {
	sender := strings.TrimPrefix(conn.Names()[0], ":")
	sender = strings.ReplaceAll(sender, ".", "_")
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
}

// portalCall calls a portal method that answers through a Request object:
// options gets a fresh handle_token, the Response signal is watched before
// the call, then awaited (timeout: the request is closed, errPortalCancel).
// args are the method's arguments BEFORE its options (a{sv}), which must be
// its last one. Results: the Response's results on success.
func portalCall(iface, method string, timeout time.Duration, options map[string]dbus.Variant, args ...interface{}) (map[string]dbus.Variant, error) {
	conn, err := sessionBus()
	if err != nil {
		return nil, err
	}
	if options == nil {
		options = map[string]dbus.Variant{}
	}
	token := newHandleToken()
	options["handle_token"] = dbus.MakeVariant(token)
	expected := portalRequestPath(conn, token)
	w, err := watchSignal(conn, expected, portalRequest, "Response")
	if err != nil {
		return nil, err
	}
	defer w.stop()
	obj := conn.Object(portalBusName, portalPath)
	var handle dbus.ObjectPath
	call := obj.Call(iface+"."+method, 0, append(args, options)...)
	if call.Err != nil {
		if dbusNameMissing(call.Err) || dbusNoMethod(call.Err) {
			return nil, fmt.Errorf("%w: %s.%s: %v", errPortalAbsent, iface, method, call.Err)
		}
		return nil, call.Err
	}
	if err := call.Store(&handle); err != nil {
		return nil, err
	}
	if handle != expected {
		// An old portal (before handle_token) picks its own path: watch it
		// (a fast answer may be lost; the timeout then closes the request).
		w.stop()
		w2, err := watchSignal(conn, handle, portalRequest, "Response")
		if err != nil {
			return nil, err
		}
		defer w2.stop()
		w = w2
	}
	body, err := w.wait(timeout)
	if err != nil {
		_ = conn.Object(portalBusName, handle).Call(portalRequest+".Close", 0).Err
		return nil, err
	}
	if len(body) < 2 {
		return nil, errPortalFailed
	}
	code, _ := body[0].(uint32)
	results, _ := body[1].(map[string]dbus.Variant)
	switch code {
	case portalResponseOK:
		if results == nil {
			results = map[string]dbus.Variant{}
		}
		return results, nil
	case portalResponseCancelled:
		return nil, errPortalCancel
	}
	return nil, errPortalFailed
}

// portalVersion: the version property of a portal interface (0: unknown or
// absent).
func portalVersion(iface string) uint32 {
	conn, err := sessionBus()
	if err != nil {
		return 0
	}
	v, err := conn.Object(portalBusName, portalPath).GetProperty(iface + ".version")
	if err != nil {
		return 0
	}
	n, _ := v.Value().(uint32)
	return n
}

// portalParentWindow: the parent_window identifier of the portals' dialogs
// for a window of the app ("wayland:<xdg-foreign handle>" or "x11:<xid>"),
// given by the UI host once it exists; "" (no parent) until then.
var portalParentWindow = func(owner uintptr) string { return "" }

// activationToken: the xdg-activation / startup-notification token this
// launch received (a second launch forwards it, so the running app may
// raise its window under Wayland).
func activationToken() string {
	if t := os.Getenv("XDG_ACTIVATION_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("DESKTOP_STARTUP_ID")
}
