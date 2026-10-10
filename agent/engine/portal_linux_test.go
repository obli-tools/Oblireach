//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// ── Fake portal bus ─────────────────────────────────────────────────────────

type fakeCall struct {
	path   dbus.ObjectPath
	iface  string
	method string
	args   []interface{}
}

type fakeResp struct {
	code    uint32
	results map[string]dbus.Variant
}

type fakeSub struct {
	path dbus.ObjectPath
	name string
	ch   chan portalSignal
	dead bool
}

type fakePortalBus struct {
	mu        sync.Mutex
	unique    string
	props     map[string]dbus.Variant
	responses map[string]fakeResp // by method
	hold      string              // method whose Response never comes
	oldPortal bool                // answers on another request path (ignores handle_token)
	fd        int
	calls     []fakeCall
	sends     []fakeCall
	pathCalls []fakeCall
	subs      []*fakeSub
	closed    bool
}

const fakeSession = dbus.ObjectPath("/org/freedesktop/portal/desktop/session/1_42/obli_s")

func newFakePortalBus() *fakePortalBus {
	return &fakePortalBus{
		unique: ":1.42",
		fd:     -1,
		props: map[string]dbus.Variant{
			portalIfaceSC + ".version":              dbus.MakeVariant(uint32(5)),
			portalIfaceSC + ".AvailableSourceTypes": dbus.MakeVariant(uint32(7)),
			portalIfaceSC + ".AvailableCursorModes": dbus.MakeVariant(uint32(7)),
			portalIfaceRD + ".version":              dbus.MakeVariant(uint32(2)),
			portalIfaceRD + ".AvailableDeviceTypes": dbus.MakeVariant(uint32(7)),
		},
		responses: map[string]fakeResp{
			"CreateSession": {0, map[string]dbus.Variant{"session_handle": dbus.MakeVariant(string(fakeSession))}},
			"SelectDevices": {0, map[string]dbus.Variant{}},
			"SelectSources": {0, map[string]dbus.Variant{}},
			"Start": {0, map[string]dbus.Variant{
				"devices":       dbus.MakeVariant(uint32(3)),
				"restore_token": dbus.MakeVariant("new-token"),
				"streams": dbus.MakeVariant([][]interface{}{
					{uint32(57), map[string]dbus.Variant{
						"position":    dbus.MakeVariant([]interface{}{int32(0), int32(0)}),
						"size":        dbus.MakeVariant([]interface{}{int32(1280), int32(720)}),
						"source_type": dbus.MakeVariant(uint32(1)),
					}},
					{uint32(58), map[string]dbus.Variant{
						"position": dbus.MakeVariant([]interface{}{int32(1280), int32(-100)}),
						"size":     dbus.MakeVariant([]interface{}{int32(1920), int32(1080)}),
					}},
				}),
			}},
		},
	}
}

func (b *fakePortalBus) UniqueName() string { return b.unique }

func (b *fakePortalBus) emit(path dbus.ObjectPath, name string, body portalSignal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		if !s.dead && s.path == path && s.name == name {
			s.ch <- body
		}
	}
}

func (b *fakePortalBus) Call(ctx context.Context, iface, method string, args ...interface{}) ([]interface{}, error) {
	b.mu.Lock()
	b.calls = append(b.calls, fakeCall{path: portalPath, iface: iface, method: method, args: args})
	b.mu.Unlock()
	if method == "OpenPipeWireRemote" {
		return []interface{}{dbus.UnixFD(b.fd)}, nil
	}
	opts, _ := args[len(args)-1].(map[string]dbus.Variant)
	tok, _ := opts["handle_token"].Value().(string)
	if tok == "" {
		return nil, errors.New("fake: no handle_token")
	}
	path := portalRequestPath(b.unique, tok)
	if b.oldPortal {
		path = dbus.ObjectPath("/org/freedesktop/portal/desktop/request/legacy/" + method)
	}
	if r, ok := b.responses[method]; ok && method != b.hold {
		reply := path
		go func() {
			// After the reply, like a real portal (the old-portal case
			// needs its subscription made after the call returned).
			time.Sleep(5 * time.Millisecond)
			b.emit(reply, portalIfaceRequest+".Response", portalSignal{r.code, r.results})
		}()
	}
	return []interface{}{path}, nil
}

func (b *fakePortalBus) Send(iface, method string, args ...interface{}) error {
	b.mu.Lock()
	b.sends = append(b.sends, fakeCall{iface: iface, method: method, args: args})
	b.mu.Unlock()
	return nil
}

func (b *fakePortalBus) CallPath(ctx context.Context, path dbus.ObjectPath, iface, method string, args ...interface{}) error {
	b.mu.Lock()
	b.pathCalls = append(b.pathCalls, fakeCall{path: path, iface: iface, method: method, args: args})
	b.mu.Unlock()
	return nil
}

func (b *fakePortalBus) Property(iface, name string) (dbus.Variant, error) {
	v, ok := b.props[iface+"."+name]
	if !ok {
		return dbus.Variant{}, errors.New("fake: no such property")
	}
	return v, nil
}

func (b *fakePortalBus) Subscribe(path dbus.ObjectPath, iface, member string) (*portalSub, error) {
	s := &fakeSub{path: path, name: iface + "." + member, ch: make(chan portalSignal, 4)}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()
	return &portalSub{C: s.ch, Cancel: func() { b.mu.Lock(); s.dead = true; b.mu.Unlock() }}, nil
}

func (b *fakePortalBus) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

func (b *fakePortalBus) call(method string) *fakeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.calls {
		if b.calls[i].method == method {
			return &b.calls[i]
		}
	}
	return nil
}

func (b *fakePortalBus) callOrder() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var s []string
	for _, c := range b.calls {
		s = append(s, strings.TrimPrefix(c.iface, "org.freedesktop.portal.")+"."+c.method)
	}
	return strings.Join(s, " ")
}

func optsOf(t *testing.T, c *fakeCall) map[string]dbus.Variant {
	t.Helper()
	if c == nil {
		t.Fatal("call not made")
	}
	m, ok := c.args[len(c.args)-1].(map[string]dbus.Variant)
	if !ok {
		t.Fatalf("%s: last argument is not the options", c.method)
	}
	return m
}

type memStore struct {
	blob    string
	saves   int
	loadErr error
}

func (m *memStore) Load() (string, error) { return m.blob, m.loadErr }
func (m *memStore) Save(s string) error   { m.blob = s; m.saves++; return nil }

func testFD(t *testing.T) int {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(f.Fd()))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestPortalRequestPath(t *testing.T) {
	got := portalRequestPath(":1.234", "obli1_ab")
	if got != "/org/freedesktop/portal/desktop/request/1_234/obli1_ab" {
		t.Fatalf("request path = %s", got)
	}
}

func TestPortalRemoteDesktopFlow(t *testing.T) {
	bus := newFakePortalBus()
	bus.fd = testFD(t)
	store := &memStore{blob: portalTokens{RemoteDesktop: "old-rd", ScreenCast: "old-sc"}.encode()}
	ps, err := openPortalSession(context.Background(), bus, portalConfig{input: true, store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.close()

	if got, want := bus.callOrder(), "RemoteDesktop.CreateSession RemoteDesktop.SelectDevices ScreenCast.SelectSources RemoteDesktop.Start ScreenCast.OpenPipeWireRemote"; got != want {
		t.Fatalf("calls:\n got %s\nwant %s", got, want)
	}
	dev := optsOf(t, bus.call("SelectDevices"))
	if v := dev["types"].Value(); v != uint32(3) {
		t.Errorf("SelectDevices types = %v, want keyboard|pointer", v)
	}
	if v := dev["persist_mode"].Value(); v != uint32(2) {
		t.Errorf("SelectDevices persist_mode = %v, want 2", v)
	}
	if v := dev["restore_token"].Value(); v != "old-rd" {
		t.Errorf("SelectDevices restore_token = %v", v)
	}
	src := optsOf(t, bus.call("SelectSources"))
	if _, ok := src["persist_mode"]; ok {
		t.Error("SelectSources of a remote desktop session must not carry persist_mode")
	}
	if _, ok := src["restore_token"]; ok {
		t.Error("SelectSources of a remote desktop session must not carry restore_token")
	}
	if src["types"].Value() != uint32(1) || src["multiple"].Value() != true || src["cursor_mode"].Value() != uint32(4) {
		t.Errorf("SelectSources options = %v", src)
	}
	if args := bus.call("SelectSources").args; args[0] != fakeSession {
		t.Errorf("SelectSources session = %v", args[0])
	}
	start := bus.call("Start")
	if start.iface != portalIfaceRD || start.args[0] != fakeSession || start.args[1] != "" {
		t.Errorf("Start = %+v", start)
	}
	if !ps.remote || ps.devices != 3 || ps.cursor != portalCursorMetadata {
		t.Errorf("session remote=%v devices=%d cursor=%d", ps.remote, ps.devices, ps.cursor)
	}
	if len(ps.streams) != 2 || ps.streams[0] != (portalStream{NodeID: 57, W: 1280, H: 720, SourceType: 1}) ||
		ps.streams[1] != (portalStream{NodeID: 58, X: 1280, Y: -100, W: 1920, H: 1080}) {
		t.Errorf("streams = %+v", ps.streams)
	}
	if ps.pwFD < 0 {
		t.Error("no PipeWire fd kept")
	}
	got := decodePortalTokens(store.blob)
	if got.RemoteDesktop != "new-token" || got.ScreenCast != "old-sc" || store.saves != 1 {
		t.Errorf("kept tokens = %+v (saves %d)", got, store.saves)
	}

	// Input goes to the session, as Notify* calls without waiting.
	ps.motionAbs(57, 10.5, 20.25)
	ps.button(evdevBtnLeft, true)
	ps.axisDiscrete(0, -2)
	ps.keycode(30, true)
	ps.keysym(0x20ac, false)
	want := []string{
		"NotifyPointerMotionAbsolute [57 10.5 20.25]",
		"NotifyPointerButton [272 1]",
		"NotifyPointerAxisDiscrete [0 -2]",
		"NotifyKeyboardKeycode [30 1]",
		"NotifyKeyboardKeysym [8364 0]",
	}
	if len(bus.sends) != len(want) {
		t.Fatalf("sends = %d, want %d", len(bus.sends), len(want))
	}
	for i, s := range bus.sends {
		if s.iface != portalIfaceRD || s.args[0] != fakeSession {
			t.Errorf("send %d to %s %v", i, s.iface, s.args[0])
		}
		if _, ok := s.args[1].(map[string]dbus.Variant); !ok {
			t.Errorf("send %d: no options", i)
		}
		if got := s.method + " " + fmtArgs(s.args[2:]); got != want[i] {
			t.Errorf("send %d = %s, want %s", i, got, want[i])
		}
	}
}

func fmtArgs(a []interface{}) string { return "[" + fmt.Sprint(a...) + "]" }

func TestPortalViewOnlyFlow(t *testing.T) {
	bus := newFakePortalBus()
	bus.fd = testFD(t)
	store := &memStore{blob: portalTokens{RemoteDesktop: "keep-rd", ScreenCast: "old-sc"}.encode()}
	ps, err := openPortalSession(context.Background(), bus, portalConfig{input: false, store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.close()
	if got, want := bus.callOrder(), "ScreenCast.CreateSession ScreenCast.SelectSources ScreenCast.Start ScreenCast.OpenPipeWireRemote"; got != want {
		t.Fatalf("calls:\n got %s\nwant %s", got, want)
	}
	src := optsOf(t, bus.call("SelectSources"))
	if src["persist_mode"].Value() != uint32(2) || src["restore_token"].Value() != "old-sc" {
		t.Errorf("SelectSources persistence = %v", src)
	}
	if ps.remote || ps.canPointer() || ps.canKeyboard() {
		t.Error("a view-only session must not inject")
	}
	ps.motionAbs(57, 1, 1)
	ps.keycode(30, true)
	if len(bus.sends) != 0 {
		t.Errorf("view-only session sent %d input calls", len(bus.sends))
	}
	got := decodePortalTokens(store.blob)
	if got.ScreenCast != "new-token" || got.RemoteDesktop != "keep-rd" {
		t.Errorf("kept tokens = %+v", got)
	}
}

func TestPortalAskEveryTime(t *testing.T) {
	bus := newFakePortalBus()
	bus.fd = testFD(t)
	ps, err := openPortalSession(context.Background(), bus, portalConfig{input: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.close()
	for _, m := range []string{"SelectDevices", "SelectSources"} {
		o := optsOf(t, bus.call(m))
		if _, ok := o["persist_mode"]; ok {
			t.Errorf("%s: persist_mode without a store", m)
		}
		if _, ok := o["restore_token"]; ok {
			t.Errorf("%s: restore_token without a store", m)
		}
	}
}

func TestPortalOldRemoteDesktopDoesNotPersist(t *testing.T) {
	bus := newFakePortalBus()
	bus.fd = testFD(t)
	bus.props[portalIfaceRD+".version"] = dbus.MakeVariant(uint32(1))
	store := &memStore{blob: portalTokens{RemoteDesktop: "x"}.encode()}
	ps, err := openPortalSession(context.Background(), bus, portalConfig{input: true, store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.close()
	if _, ok := optsOf(t, bus.call("SelectDevices"))["persist_mode"]; ok {
		t.Error("RemoteDesktop v1 has no persistence")
	}
	if store.blob != "" {
		t.Errorf("nothing to keep on this desktop, got %q", store.blob)
	}
}

func TestPortalNoRemoteDesktopFallsBackToViewOnly(t *testing.T) {
	bus := newFakePortalBus()
	bus.fd = testFD(t)
	delete(bus.props, portalIfaceRD+".version")
	ps, err := openPortalSession(context.Background(), bus, portalConfig{input: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.close()
	if ps.remote || bus.call("SelectDevices") != nil {
		t.Error("without RemoteDesktop the session is a screen cast")
	}
}

func TestPortalNoScreenCast(t *testing.T) {
	bus := newFakePortalBus()
	delete(bus.props, portalIfaceSC+".version")
	if _, err := openPortalSession(context.Background(), bus, portalConfig{input: true}); err == nil {
		t.Fatal("no ScreenCast portal must fail")
	}
}

func TestPortalUserCancels(t *testing.T) {
	bus := newFakePortalBus()
	bus.responses["Start"] = fakeResp{1, map[string]dbus.Variant{}}
	store := &memStore{blob: portalTokens{RemoteDesktop: "keep"}.encode()}
	_, err := openPortalSession(context.Background(), bus, portalConfig{input: true, store: store})
	if !errors.Is(err, errPortalCancelled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	closed := false
	for _, c := range bus.pathCalls {
		if c.path == fakeSession && c.iface == portalIfaceSession && c.method == "Close" {
			closed = true
		}
	}
	if !closed {
		t.Error("the half-open session was not closed")
	}
	if store.saves != 0 {
		t.Error("a refused session must not touch the kept permission")
	}
}

func TestPortalOtherError(t *testing.T) {
	bus := newFakePortalBus()
	bus.responses["SelectSources"] = fakeResp{2, nil}
	_, err := openPortalSession(context.Background(), bus, portalConfig{input: true})
	if err == nil || errors.Is(err, errPortalCancelled) || !strings.Contains(err.Error(), "code 2") {
		t.Fatalf("err = %v", err)
	}
}

func TestPortalNoStreams(t *testing.T) {
	bus := newFakePortalBus()
	bus.responses["Start"] = fakeResp{0, map[string]dbus.Variant{"devices": dbus.MakeVariant(uint32(3))}}
	if _, err := openPortalSession(context.Background(), bus, portalConfig{input: true}); err == nil {
		t.Fatal("a start without streams must fail")
	}
}

func TestPortalOldPortalRequestPath(t *testing.T) {
	bus := newFakePortalBus()
	bus.fd = testFD(t)
	bus.oldPortal = true
	ps, err := openPortalSession(context.Background(), bus, portalConfig{input: true})
	if err != nil {
		t.Fatal(err)
	}
	ps.close()
}

func TestPortalContextCancelClosesRequest(t *testing.T) {
	bus := newFakePortalBus()
	bus.hold = "Start" // the user never answers the dialog
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := openPortalSession(ctx, bus, portalConfig{input: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	var reqClosed, sessClosed bool
	for _, c := range bus.pathCalls {
		if c.iface == portalIfaceRequest && c.method == "Close" && strings.Contains(string(c.path), "/request/1_42/") {
			reqClosed = true
		}
		if c.path == fakeSession && c.method == "Close" {
			sessClosed = true
		}
	}
	if !reqClosed || !sessClosed {
		t.Errorf("request closed %v, session closed %v", reqClosed, sessClosed)
	}
}

func TestPortalDesktopClosesSession(t *testing.T) {
	bus := newFakePortalBus()
	bus.fd = testFD(t)
	ps, err := openPortalSession(context.Background(), bus, portalConfig{input: true})
	if err != nil {
		t.Fatal(err)
	}
	bus.emit(fakeSession, portalIfaceSession+".Closed", portalSignal{map[string]dbus.Variant{}})
	select {
	case <-ps.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not end when the desktop closed it")
	}
	if !errors.Is(ps.Err(), errPortalClosed) {
		t.Errorf("Err = %v", ps.Err())
	}
	ps.keycode(30, true)
	if len(bus.sends) != 0 {
		t.Error("input sent to a closed session")
	}
	ps.close()
	ps.close() // idempotent
	if !bus.closed {
		t.Error("bus not closed")
	}
}

func TestPortalTokensBlob(t *testing.T) {
	if (portalTokens{}).encode() != "" {
		t.Error("empty tokens must encode to nothing")
	}
	in := portalTokens{RemoteDesktop: "a\"b", ScreenCast: "c"}
	out := decodePortalTokens(in.encode())
	if out.RemoteDesktop != in.RemoteDesktop || out.ScreenCast != in.ScreenCast {
		t.Errorf("round trip = %+v", out)
	}
	for _, bad := range []string{"", "  ", "garbage", `{"v":2,"rd":"x"}`, `["x"]`} {
		if got := decodePortalTokens(bad); got != (portalTokens{}) {
			t.Errorf("decode(%q) = %+v", bad, got)
		}
	}
	if got := loadPortalTokens(&memStore{loadErr: errors.New("x"), blob: in.encode()}); got != (portalTokens{}) {
		t.Error("a store error must read as nothing kept")
	}
}

func TestFilePortalStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "perm")
	s := NewFilePortalStore(p)
	if v, err := s.Load(); err != nil || v != "" {
		t.Fatalf("missing file: %q %v", v, err)
	}
	if err := s.Save("blob1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Load(); v != "blob1" {
		t.Errorf("Load = %q", v)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v %v", st.Mode(), err)
	}
	if err := s.Save(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("an empty blob must remove the file")
	}
	if err := s.Save(""); err != nil {
		t.Errorf("removing twice: %v", err)
	}
}

func TestChooseCursorMode(t *testing.T) {
	for avail, want := range map[uint32]uint32{7: 4, 6: 4, 3: 2, 1: 1, 0: 0, 2: 2} {
		if got := chooseCursorMode(avail); got != want {
			t.Errorf("chooseCursorMode(%d) = %d, want %d", avail, got, want)
		}
	}
}

func TestParsePortalStreamsShapes(t *testing.T) {
	// godbus may hand the array as [][]interface{} or []interface{}.
	v := dbus.MakeVariant([]interface{}{
		[]interface{}{uint32(9), map[string]dbus.Variant{"size": dbus.MakeVariant([]interface{}{int32(800), int32(600)})}},
		"junk",
	})
	s := parsePortalStreams(v)
	if len(s) != 1 || s[0].NodeID != 9 || s[0].W != 800 || s[0].H != 600 {
		t.Errorf("streams = %+v", s)
	}
	if len(parsePortalStreams(dbus.MakeVariant("x"))) != 0 {
		t.Error("garbage must give no stream")
	}
}

func TestPortalMonitors(t *testing.T) {
	streams := []portalStream{{NodeID: 1, W: 1280, H: 720}, {NodeID: 2, X: 1280, W: 0, H: 0}}
	m := portalMonitors(streams, func(i int) (int, int) {
		if i == 0 {
			return 2560, 1440
		}
		return 1920, 1080
	})
	if len(m) != 2 || m[0].Scale != 200 || m[0].Width != 1280 || m[1].Width != 1920 || m[1].X != 1280 || m[1].Name != "Screen 2" {
		t.Errorf("monitors = %+v", m)
	}
}

func TestChooseLinuxBackend(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		pref string
		env  func(string) string
		want string
	}{
		{"", env("WAYLAND_DISPLAY", "wayland-0", "DISPLAY", ":0"), "portal"},
		{"", env("XDG_SESSION_TYPE", "wayland"), "portal"},
		{"", env("DISPLAY", ":0", "XDG_SESSION_TYPE", "x11"), "x11"},
		{"", env(), "portal"},
		{"x11", env("WAYLAND_DISPLAY", "wayland-0"), "x11"},
		{"auto", env("DISPLAY", ":0"), "x11"},
		{"portal", env("DISPLAY", ":0"), "portal"},
		{"x11", env("OBLIREACH_CAPTURE", "pipewire"), "portal"},
		{"portal", env("OBLIREACH_CAPTURE", "X11"), "x11"},
	}
	for i, c := range cases {
		if got := chooseLinuxBackend(c.pref, c.env); got != c.want {
			t.Errorf("case %d: got %s, want %s", i, got, c.want)
		}
	}
}
