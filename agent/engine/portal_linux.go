//go:build linux

package engine

// xdg-desktop-portal client: the ScreenCast and RemoteDesktop portals that
// let an unprivileged (and sandboxed: Flatpak) process see the screens and
// inject input under Wayland, after the desktop's own consent dialog.
//
//	RemoteDesktop.CreateSession            (ScreenCast.CreateSession when view only)
//	 → RemoteDesktop.SelectDevices          keyboard | pointer [+ persist_mode / restore_token]
//	 → ScreenCast.SelectSources             monitors, multiple, cursor mode
//	 → RemoteDesktop.Start                  the desktop's dialog; streams, devices, restore_token
//	 → ScreenCast.OpenPipeWireRemote        fd of the PipeWire connection (pipewire_linux.go)
//	input: RemoteDesktop.NotifyPointerMotionAbsolute / NotifyPointerButton /
//	       NotifyPointerAxisDiscrete / NotifyKeyboardKeycode / NotifyKeyboardKeysym
//
// Each portal method answers with a Request object; the result comes later
// as its Response signal (code 0 success, 1 cancelled by the user, 2 other).
// The subscription is made BEFORE the call, on the path the portal will use
// (request/SENDER/TOKEN), so a fast answer is never missed.
//
// The permission can be remembered (restore token, persist_mode 2) through
// Options.PortalPermission; without a store the desktop asks every session.
// A RemoteDesktop session carries the persistence on SelectDevices (the
// portal refuses it on SelectSources of a remote desktop session).
//
// Everything here runs over portalBus: portal_dbus_linux.go implements it
// with godbus on the session bus, portal_linux_test.go with a fake.
//
// Not done yet (TODO): ConnectToEIS (libei input, RemoteDesktop v2: lower
// latency, smooth scrolling) and the Clipboard portal.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest         = "org.freedesktop.portal.Desktop"
	portalPath         = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalIfaceSC      = "org.freedesktop.portal.ScreenCast"
	portalIfaceRD      = "org.freedesktop.portal.RemoteDesktop"
	portalIfaceRequest = "org.freedesktop.portal.Request"
	portalIfaceSession = "org.freedesktop.portal.Session"
)

// ScreenCast source types, cursor modes; RemoteDesktop device types.
const (
	portalSourceMonitor = 1
	portalSourceWindow  = 2
	portalSourceVirtual = 4

	portalCursorHidden   = 1
	portalCursorEmbedded = 2
	portalCursorMetadata = 4

	portalDeviceKeyboard    = 1
	portalDevicePointer     = 2
	portalDeviceTouchscreen = 4

	portalPersistNone      = 0
	portalPersistPermanent = 2
)

var (
	errPortalCancelled = errors.New("screen sharing was refused on this computer")
	errPortalClosed    = errors.New("screen sharing was stopped on this computer")
)

// portalSignal is the body of one D-Bus signal.
type portalSignal []interface{}

// portalSub is a live signal subscription.
type portalSub struct {
	C      <-chan portalSignal
	Cancel func()
}

// portalBus is the slice of D-Bus the portal client needs. Methods are
// called on portalDest at portalPath unless a path is given.
type portalBus interface {
	UniqueName() string
	Call(ctx context.Context, iface, method string, args ...interface{}) ([]interface{}, error)
	// Send calls without waiting for a reply (input events, in order).
	Send(iface, method string, args ...interface{}) error
	// CallPath calls a method on another object of the portal (Request /
	// Session Close).
	CallPath(ctx context.Context, path dbus.ObjectPath, iface, method string, args ...interface{}) error
	Property(iface, name string) (dbus.Variant, error)
	Subscribe(path dbus.ObjectPath, iface, member string) (*portalSub, error)
	Close() error
}

// portalConfig is what a session asks for.
type portalConfig struct {
	input bool                  // RemoteDesktop (keyboard + pointer), else ScreenCast only
	store PortalPermissionStore // nil: ask every time
}

// portalStream is one screen the user shared.
type portalStream struct {
	NodeID     uint32
	X, Y       int // logical (compositor) position, 0 when not given
	W, H       int // logical size, 0 when not given
	SourceType uint32
	MappingID  string
}

// portalSession is an open (started) portal session.
type portalSession struct {
	bus     portalBus
	handle  dbus.ObjectPath
	remote  bool   // a RemoteDesktop session (input possible)
	devices uint32 // granted device types
	streams []portalStream
	cursor  uint32 // cursor mode in use
	pwFD    int    // PipeWire fd (owned until taken), -1 when none

	closedSub *portalSub
	done      chan struct{}
	closeOnce sync.Once
	shutOnce  sync.Once
	mu        sync.Mutex
	closedErr error
	pw        *pwSession // PipeWire consumer (capture_portal_linux.go)
}

// Done is closed when the session ended (closed by us, or by the desktop:
// the user stopped sharing from the panel).
func (ps *portalSession) Done() <-chan struct{} { return ps.done }

// Err says why the session ended (nil while it runs, errPortalClosed when
// the desktop closed it).
func (ps *portalSession) Err() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.closedErr
}

func (ps *portalSession) finish(err error) {
	ps.closeOnce.Do(func() {
		ps.mu.Lock()
		ps.closedErr = err
		pw := ps.pw
		ps.pw = nil
		ps.mu.Unlock()
		if pw != nil {
			pw.close()
		}
		if ps.pwFD >= 0 {
			closeFD(ps.pwFD)
			ps.pwFD = -1
		}
		close(ps.done)
	})
}

// portalCloseTimeout bounds the Close calls made while tearing down.
const portalCloseTimeout = 2 * time.Second

// close ends the session and its D-Bus connection (idempotent; also after
// the desktop closed it).
func (ps *portalSession) close() {
	ps.finish(nil)
	ps.shutOnce.Do(func() {
		if ps.closedSub != nil {
			ps.closedSub.Cancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), portalCloseTimeout)
		_ = ps.bus.CallPath(ctx, ps.handle, portalIfaceSession, "Close")
		cancel()
		_ = ps.bus.Close()
	})
}

// watchClosed ends the session when the desktop closes it.
func (ps *portalSession) watchClosed() {
	if ps.closedSub == nil {
		return
	}
	go func() {
		select {
		case _, ok := <-ps.closedSub.C:
			if ok {
				log.Printf("portal: the desktop closed the session")
				ps.finish(errPortalClosed)
			}
		case <-ps.done:
		}
	}()
}

// ── Input (RemoteDesktop Notify*) ───────────────────────────────────────────

func (ps *portalSession) canPointer() bool  { return ps.remote && ps.devices&portalDevicePointer != 0 }
func (ps *portalSession) canKeyboard() bool { return ps.remote && ps.devices&portalDeviceKeyboard != 0 }

func (ps *portalSession) notify(method string, args ...interface{}) {
	select {
	case <-ps.done:
		return
	default:
	}
	full := append([]interface{}{ps.handle, map[string]dbus.Variant{}}, args...)
	if err := ps.bus.Send(portalIfaceRD, method, full...); err != nil {
		log.Printf("portal: %s: %v", method, err)
	}
}

func (ps *portalSession) motionAbs(node uint32, x, y float64) {
	if ps.canPointer() {
		ps.notify("NotifyPointerMotionAbsolute", node, x, y)
	}
}

func (ps *portalSession) button(code int32, pressed bool) {
	if ps.canPointer() {
		ps.notify("NotifyPointerButton", code, portalKeyState(pressed))
	}
}

func (ps *portalSession) axisDiscrete(axis uint32, steps int32) {
	if ps.canPointer() {
		ps.notify("NotifyPointerAxisDiscrete", axis, steps)
	}
}

func (ps *portalSession) keycode(code int32, pressed bool) {
	if ps.canKeyboard() {
		ps.notify("NotifyKeyboardKeycode", code, portalKeyState(pressed))
	}
}

func (ps *portalSession) keysym(sym int32, pressed bool) {
	if ps.canKeyboard() {
		ps.notify("NotifyKeyboardKeysym", sym, portalKeyState(pressed))
	}
}

func portalKeyState(pressed bool) uint32 {
	if pressed {
		return 1
	}
	return 0
}

// ── Opening a session ───────────────────────────────────────────────────────

// openPortalSession runs the whole handshake; on success the session is
// started and holds the PipeWire fd. ctx bounds the wait for the user (the
// pending request is closed when it is cancelled).
func openPortalSession(ctx context.Context, bus portalBus, cfg portalConfig) (ps *portalSession, err error) {
	o := &portalOpener{bus: bus, ctx: ctx}
	scVer, err := o.version(portalIfaceSC)
	if err != nil {
		return nil, fmt.Errorf("the screen sharing portal (ScreenCast) is not available: %w", err)
	}
	remote := false
	var rdVer uint32
	if cfg.input {
		if v, err := o.version(portalIfaceRD); err == nil {
			remote, rdVer = true, v
		} else {
			log.Printf("portal: RemoteDesktop unavailable (%v): view only", err)
		}
	}

	tokens := loadPortalTokens(cfg.store)
	persist := cfg.store != nil && ((remote && rdVer >= 2) || (!remote && scVer >= 4))

	createIface := portalIfaceSC
	if remote {
		createIface = portalIfaceRD
	}
	res, err := o.request(createIface, "CreateSession", nil, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(o.token()),
	})
	if err != nil {
		return nil, fmt.Errorf("CreateSession: %w", err)
	}
	handle := variantObjectPath(res["session_handle"])
	if handle == "" {
		return nil, errors.New("CreateSession: no session handle")
	}
	ps = &portalSession{bus: bus, handle: handle, remote: remote, pwFD: -1, done: make(chan struct{})}
	sess := ps // the named result is nil once an error is returned
	defer func() {
		if err != nil {
			sess.finish(err)
			if sess.closedSub != nil {
				sess.closedSub.Cancel()
			}
			cctx, cancel := context.WithTimeout(context.Background(), portalCloseTimeout)
			_ = bus.CallPath(cctx, handle, portalIfaceSession, "Close")
			cancel()
		}
	}()
	// The desktop may close the session at any time from now on.
	if sub, serr := bus.Subscribe(handle, portalIfaceSession, "Closed"); serr == nil {
		ps.closedSub = sub
	}

	if remote {
		avail, _ := o.uint32Property(portalIfaceRD, "AvailableDeviceTypes")
		types := uint32(portalDeviceKeyboard | portalDevicePointer)
		if avail != 0 {
			types &= avail
		}
		opts := map[string]dbus.Variant{"types": dbus.MakeVariant(types)}
		if persist {
			opts["persist_mode"] = dbus.MakeVariant(uint32(portalPersistPermanent))
			if tokens.RemoteDesktop != "" {
				opts["restore_token"] = dbus.MakeVariant(tokens.RemoteDesktop)
			}
		}
		if _, err = o.request(portalIfaceRD, "SelectDevices", []interface{}{handle}, opts); err != nil {
			return nil, fmt.Errorf("SelectDevices: %w", err)
		}
	}

	srcAvail, _ := o.uint32Property(portalIfaceSC, "AvailableSourceTypes")
	curAvail, _ := o.uint32Property(portalIfaceSC, "AvailableCursorModes")
	sources := uint32(portalSourceMonitor)
	if srcAvail != 0 && srcAvail&portalSourceMonitor == 0 {
		sources = srcAvail // no monitor sharing offered: take what is
	}
	opts := map[string]dbus.Variant{
		"types":    dbus.MakeVariant(sources),
		"multiple": dbus.MakeVariant(true),
	}
	ps.cursor = chooseCursorMode(curAvail)
	if ps.cursor != 0 {
		opts["cursor_mode"] = dbus.MakeVariant(ps.cursor)
	}
	if persist && !remote {
		opts["persist_mode"] = dbus.MakeVariant(uint32(portalPersistPermanent))
		if tokens.ScreenCast != "" {
			opts["restore_token"] = dbus.MakeVariant(tokens.ScreenCast)
		}
	}
	if _, err = o.request(portalIfaceSC, "SelectSources", []interface{}{handle}, opts); err != nil {
		return nil, fmt.Errorf("SelectSources: %w", err)
	}

	startIface := portalIfaceSC
	if remote {
		startIface = portalIfaceRD
	}
	res, err = o.request(startIface, "Start", []interface{}{handle, ""}, map[string]dbus.Variant{})
	if err != nil {
		return nil, fmt.Errorf("Start: %w", err)
	}
	ps.streams = parsePortalStreams(res["streams"])
	if len(ps.streams) == 0 {
		return nil, errors.New("Start: no screen was shared")
	}
	if remote {
		ps.devices = variantUint32(res["devices"])
	}
	if cfg.store != nil {
		tok := variantString(res["restore_token"])
		if remote {
			tokens.RemoteDesktop = tok
		} else {
			tokens.ScreenCast = tok
		}
		if !persist {
			tokens = portalTokens{} // nothing to remember on this desktop
		}
		if serr := cfg.store.Save(tokens.encode()); serr != nil {
			log.Printf("portal: cannot keep the permission: %v", serr)
		}
	}

	body, err := bus.Call(ctx, portalIfaceSC, "OpenPipeWireRemote", handle, map[string]dbus.Variant{})
	if err != nil {
		return nil, fmt.Errorf("OpenPipeWireRemote: %w", err)
	}
	fd := -1
	if len(body) > 0 {
		switch v := body[0].(type) {
		case dbus.UnixFD:
			fd = int(v)
		case int32:
			fd = int(v)
		}
	}
	if fd < 0 {
		return nil, errors.New("OpenPipeWireRemote: no file descriptor (the D-Bus connection cannot pass one)")
	}
	ps.pwFD = fd
	ps.watchClosed()
	log.Printf("portal: session started (remote=%v devices=%d cursor=%d persist=%v streams=%d)",
		remote, ps.devices, ps.cursor, persist, len(ps.streams))
	return ps, nil
}

func closeFD(fd int) { _ = syscall.Close(fd) }

// takePWFD hands the PipeWire fd over (the consumer owns it from now on).
func (ps *portalSession) takePWFD() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	fd := ps.pwFD
	ps.pwFD = -1
	return fd
}

// chooseCursorMode: the pointer drawn by us from metadata (it stays exact
// even when the compositor sends no new frame for a pointer move), else
// drawn by the compositor, else hidden; 0 = do not ask (old portal).
func chooseCursorMode(avail uint32) uint32 {
	switch {
	case avail&portalCursorMetadata != 0:
		return portalCursorMetadata
	case avail&portalCursorEmbedded != 0:
		return portalCursorEmbedded
	case avail&portalCursorHidden != 0:
		return portalCursorHidden
	}
	return 0
}

type portalOpener struct {
	bus portalBus
	ctx context.Context
	n   int
}

func (o *portalOpener) token() string {
	o.n++
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("obli%d_%s", o.n, hex.EncodeToString(b[:]))
}

func (o *portalOpener) version(iface string) (uint32, error) {
	v, err := o.bus.Property(iface, "version")
	if err != nil {
		return 0, err
	}
	n := variantUint32(v)
	if n == 0 {
		return 0, fmt.Errorf("%s: no version", iface)
	}
	return n, nil
}

func (o *portalOpener) uint32Property(iface, name string) (uint32, error) {
	v, err := o.bus.Property(iface, name)
	if err != nil {
		return 0, err
	}
	return variantUint32(v), nil
}

// portalRequestPath is the Request object a call with handle_token tok
// will use (xdg-desktop-portal ≥ 0.9).
func portalRequestPath(unique, tok string) dbus.ObjectPath {
	sender := strings.ReplaceAll(strings.TrimPrefix(unique, ":"), ".", "_")
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + tok)
}

// request calls a portal method that answers through a Request and waits
// for its Response. args are the method's arguments before the options.
func (o *portalOpener) request(iface, method string, args []interface{}, opts map[string]dbus.Variant) (map[string]dbus.Variant, error) {
	tok := o.token()
	opts["handle_token"] = dbus.MakeVariant(tok)
	want := portalRequestPath(o.bus.UniqueName(), tok)
	sub, err := o.bus.Subscribe(want, portalIfaceRequest, "Response")
	if err != nil {
		return nil, err
	}
	defer sub.Cancel()
	body, err := o.bus.Call(o.ctx, iface, method, append(append([]interface{}{}, args...), opts)...)
	if err != nil {
		return nil, err
	}
	got := want
	if len(body) > 0 {
		if p, ok := body[0].(dbus.ObjectPath); ok && p != "" {
			got = p
		}
	}
	ch := sub.C
	if got != want {
		// An old portal that ignores handle_token: listen on its path (the
		// answer may in theory have been missed: a user dialog never is).
		sub2, err := o.bus.Subscribe(got, portalIfaceRequest, "Response")
		if err != nil {
			return nil, err
		}
		defer sub2.Cancel()
		ch = sub2.C
	}
	select {
	case <-o.ctx.Done():
		cctx, cancel := context.WithTimeout(context.Background(), portalCloseTimeout)
		_ = o.bus.CallPath(cctx, got, portalIfaceRequest, "Close")
		cancel()
		return nil, o.ctx.Err()
	case sig, ok := <-ch:
		if !ok {
			return nil, errors.New("the portal connection closed")
		}
		return parsePortalResponse(sig)
	}
}

// parsePortalResponse decodes a Request.Response body (u response, a{sv}).
func parsePortalResponse(sig portalSignal) (map[string]dbus.Variant, error) {
	if len(sig) < 1 {
		return nil, errors.New("malformed portal response")
	}
	code, ok := sig[0].(uint32)
	if !ok {
		return nil, errors.New("malformed portal response code")
	}
	results := map[string]dbus.Variant{}
	if len(sig) > 1 {
		if m, ok := sig[1].(map[string]dbus.Variant); ok {
			results = m
		}
	}
	switch code {
	case 0:
		return results, nil
	case 1:
		return nil, errPortalCancelled
	default:
		return nil, fmt.Errorf("the portal refused the request (code %d)", code)
	}
}

// parsePortalStreams decodes Start's "streams" (a(ua{sv})).
func parsePortalStreams(v dbus.Variant) []portalStream {
	var out []portalStream
	add := func(node uint32, props map[string]dbus.Variant) {
		s := portalStream{NodeID: node}
		if p, ok := props["position"]; ok {
			s.X, s.Y = variantIntPair(p)
		}
		if p, ok := props["size"]; ok {
			s.W, s.H = variantIntPair(p)
		}
		s.SourceType = variantUint32(props["source_type"])
		s.MappingID = variantString(props["mapping_id"])
		out = append(out, s)
	}
	switch list := v.Value().(type) {
	case [][]interface{}:
		for _, e := range list {
			if len(e) < 2 {
				continue
			}
			node, ok := e[0].(uint32)
			props, ok2 := e[1].(map[string]dbus.Variant)
			if ok && ok2 {
				add(node, props)
			}
		}
	case []interface{}:
		for _, ev := range list {
			e, ok := ev.([]interface{})
			if !ok || len(e) < 2 {
				continue
			}
			node, ok := e[0].(uint32)
			props, ok2 := e[1].(map[string]dbus.Variant)
			if ok && ok2 {
				add(node, props)
			}
		}
	}
	return out
}

func variantUint32(v dbus.Variant) uint32 {
	switch n := v.Value().(type) {
	case uint32:
		return n
	case int32:
		if n >= 0 {
			return uint32(n)
		}
	case uint64:
		return uint32(n)
	case int64:
		if n >= 0 {
			return uint32(n)
		}
	}
	return 0
}

func variantString(v dbus.Variant) string {
	switch s := v.Value().(type) {
	case string:
		return s
	case dbus.ObjectPath:
		return string(s)
	}
	return ""
}

func variantObjectPath(v dbus.Variant) dbus.ObjectPath {
	return dbus.ObjectPath(variantString(v))
}

func variantIntPair(v dbus.Variant) (int, int) {
	switch p := v.Value().(type) {
	case []interface{}:
		if len(p) == 2 {
			a, ok1 := p[0].(int32)
			b, ok2 := p[1].(int32)
			if ok1 && ok2 {
				return int(a), int(b)
			}
		}
	case []int32:
		if len(p) == 2 {
			return int(p[0]), int(p[1])
		}
	}
	return 0, 0
}

// ── Remembered permission (restore tokens) ──────────────────────────────────

// portalTokens is what Options.PortalPermission keeps: one restore token per
// kind of session (a view-only session and a remote control session are two
// different permissions for the desktop). A token is single use: every
// session that used it gets a new one, saved over it.
type portalTokens struct {
	V             int    `json:"v"`
	RemoteDesktop string `json:"rd,omitempty"`
	ScreenCast    string `json:"sc,omitempty"`
}

func (t portalTokens) encode() string {
	if t.RemoteDesktop == "" && t.ScreenCast == "" {
		return ""
	}
	t.V = 1
	b, _ := json.Marshal(t)
	return string(b)
}

func decodePortalTokens(s string) portalTokens {
	var t portalTokens
	if strings.TrimSpace(s) == "" || json.Unmarshal([]byte(s), &t) != nil || t.V != 1 {
		return portalTokens{}
	}
	return t
}

func loadPortalTokens(store PortalPermissionStore) portalTokens {
	if store == nil {
		return portalTokens{}
	}
	s, err := store.Load()
	if err != nil {
		log.Printf("portal: cannot read the kept permission: %v", err)
		return portalTokens{}
	}
	return decodePortalTokens(s)
}

// ── Default store / bare StreamSession use ──────────────────────────────────

var defaultPortalStore struct {
	mu    sync.Mutex
	store PortalPermissionStore
}

// SetPortalPermissionStore sets the store a bare StreamSession (the agent's
// direct path, without RunSession and its Options) uses on Linux. Nil (the
// default): the desktop asks every time.
func SetPortalPermissionStore(s PortalPermissionStore) {
	defaultPortalStore.mu.Lock()
	defaultPortalStore.store = s
	defaultPortalStore.mu.Unlock()
}

// openPortalSessionDefault opens a session on the session bus for a bare
// StreamSession (no context: the user's dialog may take its time).
func openPortalSessionDefault(cfg portalConfig) (*portalSession, error) {
	if cfg.store == nil {
		defaultPortalStore.mu.Lock()
		cfg.store = defaultPortalStore.store
		defaultPortalStore.mu.Unlock()
	}
	bus, err := dialPortalBus()
	if err != nil {
		return nil, err
	}
	ps, err := openPortalSession(context.Background(), bus, cfg)
	if err != nil {
		_ = bus.Close()
		return nil, err
	}
	return ps, nil
}

// portalMonitors describes the shared screens as the viewer's monitor list:
// logical position and size from the portal (frame size when the portal gave
// none), scale = frame pixels per logical pixel in percent.
func portalMonitors(streams []portalStream, frameSize func(i int) (int, int)) []MonitorInfo {
	out := make([]MonitorInfo, 0, len(streams))
	for i, s := range streams {
		m := MonitorInfo{Index: i, Name: fmt.Sprintf("Screen %d", i+1), X: s.X, Y: s.Y, Width: s.W, Height: s.H}
		fw, fh := 0, 0
		if frameSize != nil {
			fw, fh = frameSize(i)
		}
		if m.Width <= 0 || m.Height <= 0 {
			m.Width, m.Height = fw, fh
		} else if fw > 0 && fw != m.Width {
			m.Scale = fw * 100 / m.Width
		}
		// The portal does not say which screen is the primary one.
		out = append(out, m)
	}
	return out
}
