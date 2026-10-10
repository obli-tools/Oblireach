package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/cpace"
	"github.com/gorilla/websocket"
	"github.com/pion/turn/v5"

	sl "github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

const (
	testKey     = "test-operator-key-0123456789"
	testAccount = "helpdesk"
	testName    = "Jane Operator"
)

// syncBuf collects the JSON logs of a test server.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testEnv struct {
	srv   *Server
	url   string
	admin string
	logs  *syncBuf
	sig   sl.SignalOptions // TLS roots, proxy
}

var relayBase atomic.Int32

func newEnv(t *testing.T, mut func(*Config)) *testEnv {
	t.Helper()
	logs := &syncBuf{}
	cfg := testConfig(logs)
	if mut != nil {
		mut(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartTURN(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	as := httptest.NewServer(s.AdminHandler())
	t.Cleanup(func() {
		s.Close()
		ts.Close()
		as.Close()
	})
	return &testEnv{srv: s, url: "ws://" + ts.Listener.Addr().String() + wire.Path, admin: as.URL, logs: logs}
}

func testConfig(logs *syncBuf) Config {
	lim := DefaultLimits()
	lim.ConnectPerMinIP, lim.ConnectBurstIP = 6000, 1000 // the whole suite comes from 127.0.0.1
	base := uint16(43000 + 200*relayBase.Add(1))
	return Config{
		PublicIP:     net.ParseIP("127.0.0.1").To4(),
		TURNListenIP: "127.0.0.1",
		RelayMinPort: base, RelayMaxPort: base + 199,
		TURNSecret:   "test-turn-secret-0123456789",
		OperatorKeys: []OperatorKey{{Label: testAccount, Secret: testKey}},
		RelayMaxKbps: 200000,
		Limits:       lim,
		Logger:       slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
}

func (e *testEnv) metric(t *testing.T, line string) string {
	t.Helper()
	resp, err := http.Get(e.admin + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, line+" ") {
			return strings.TrimPrefix(l, line+" ")
		}
	}
	return ""
}

var loopback = sl.ICEOptions{IncludeLoopback: true, IPFilter: func(ip net.IP) bool { return ip.IsLoopback() }}

type hostRec struct {
	h     *sl.Host
	mu    sync.Mutex
	reqs  []sl.ApprovalRequest
	creds []sl.CredentialsChange
}

func (r *hostRec) lastCreds() (sl.CredentialsChange, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.creds) == 0 {
		return sl.CredentialsChange{}, 0
	}
	return r.creds[len(r.creds)-1], len(r.creds)
}

func startHost(t *testing.T, e *testEnv, ice sl.ICEOptions, approve func(context.Context, sl.ApprovalRequest) bool, mut func(*sl.HostConfig)) *hostRec {
	t.Helper()
	r := &hostRec{}
	cfg := sl.HostConfig{
		Server: e.url, ClientVersion: "1.0.0", ICE: ice, SignalOptions: e.sig,
		Approve: func(ctx context.Context, req sl.ApprovalRequest) bool {
			r.mu.Lock()
			r.reqs = append(r.reqs, req)
			r.mu.Unlock()
			return approve(ctx, req)
		},
		OnCredentials: func(c sl.CredentialsChange) {
			r.mu.Lock()
			r.creds = append(r.creds, c)
			r.mu.Unlock()
		},
	}
	if mut != nil {
		mut(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := sl.Register(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	r.h = h
	return r
}

func allow(context.Context, sl.ApprovalRequest) bool { return true }

func opConfig(e *testEnv, ice sl.ICEOptions, sas *string) sl.OperatorConfig {
	return sl.OperatorConfig{
		Server: e.url, ClientVersion: "1.0.0", Key: testKey, Name: testName, ICE: ice, SignalOptions: e.sig,
		OnAwaitingApproval: func(s string) {
			if sas != nil {
				*sas = s
			}
		},
	}
}

func connect(t *testing.T, cfg sl.OperatorConfig, id, code string) (*sl.Session, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return sl.Connect(ctx, cfg, id, code)
}

// pair connects an operator to a fresh host and returns both sessions.
func pair(t *testing.T, e *testEnv, hostICE, opICE sl.ICEOptions) (*hostRec, *sl.Session, *sl.Session, string, time.Duration) {
	t.Helper()
	hr := startHost(t, e, hostICE, allow, nil)
	type res struct {
		s   *sl.Session
		err error
	}
	hc := make(chan res, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, err := hr.h.WaitForOperator(ctx)
		hc <- res{s, err}
	}()
	var sas string
	t0 := time.Now()
	os, err := connect(t, opConfig(e, opICE, &sas), wire.Group3(hr.h.ID()), wire.Group3(hr.h.Code()))
	if err != nil {
		t.Fatalf("operator: %v", err)
	}
	setup := time.Since(t0)
	r := <-hc
	if r.err != nil {
		t.Fatalf("host: %v", r.err)
	}
	t.Cleanup(func() { os.Close(); r.s.Close() })
	return hr, r.s, os, sas, setup
}

// exchange checks both directions and returns the RTT median of ctl
// round trips and the throughput of a binary burst.
func exchange(t *testing.T, hs, os *sl.Session) (rtt time.Duration, mbps float64) {
	t.Helper()
	go func() {
		for {
			op, p, err := os.ReadFrame()
			if err != nil {
				return
			}
			if op == sl.OpText && strings.HasPrefix(string(p), "ping") {
				_ = os.WriteFrame(sl.OpText, append([]byte("pong"), p[4:]...))
			}
			if op == sl.OpBinary && len(p) == 1 {
				_ = os.WriteFrame(sl.OpText, []byte("bulk-done"))
			}
		}
	}()
	var rtts []time.Duration
	for i := 0; i < 20; i++ {
		t0 := time.Now()
		if err := hs.WriteFrame(sl.OpText, []byte(fmt.Sprintf("ping%d", i))); err != nil {
			t.Fatal(err)
		}
		op, p, err := hs.ReadFrame()
		if err != nil || op != sl.OpText || string(p) != fmt.Sprintf("pong%d", i) {
			t.Fatalf("pong %d: %x %q %v", i, op, p, err)
		}
		rtts = append(rtts, time.Since(t0))
	}
	// 16 MiB of 128 KiB binary frames (engine-sized), then a 1-byte marker.
	frame := bytes.Repeat([]byte{0xA5}, 128<<10)
	t0 := time.Now()
	for sent := 0; sent < 16<<20; sent += len(frame) {
		if err := hs.WriteFrame(sl.OpBinary, frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := hs.WriteFrame(sl.OpBinary, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, p, err := hs.ReadFrame(); err != nil || string(p) != "bulk-done" {
		t.Fatalf("bulk: %q %v", p, err)
	}
	mbps = float64(16<<20) * 8 / time.Since(t0).Seconds() / 1e6
	return median(rtts), mbps
}

func median(d []time.Duration) time.Duration {
	for i := range d {
		for j := i + 1; j < len(d); j++ {
			if d[j] < d[i] {
				d[i], d[j] = d[j], d[i]
			}
		}
	}
	return d[len(d)/2]
}

func runPath(t *testing.T, hostICE, opICE sl.ICEOptions, want sl.Path) {
	runPathEnv(t, newEnv(t, nil), hostICE, opICE, want)
}

func runPathEnv(t *testing.T, e *testEnv, hostICE, opICE sl.ICEOptions, want sl.Path) {
	t.Helper()
	hr, hs, os, sas, setup := pair(t, e, hostICE, opICE)
	if hs.Path() != want || os.Path() != want {
		t.Fatalf("path host=%s (%s) operator=%s (%s), want %s", hs.Path(), hs.PathDetail(), os.Path(), os.PathDetail(), want)
	}
	if hs.SAS() != os.SAS() || hs.SAS() != sas {
		t.Fatalf("SAS mismatch: host %q operator %q callback %q", hs.SAS(), os.SAS(), sas)
	}
	hr.mu.Lock()
	req := hr.reqs[0]
	hr.mu.Unlock()
	if req.Account != testAccount || req.OperatorName != testName || req.SAS != sas {
		t.Fatalf("approval request %+v", req)
	}
	rtt, mbps := exchange(t, hs, os)
	t.Logf("%s: setup %v, ctl RTT p50 %v, bulk %.0f Mbit/s, pair %s", want, setup.Round(time.Millisecond), rtt.Round(10*time.Microsecond), mbps, os.PathDetail())

	// The code is spent once a session is accepted.
	if c, _ := hr.lastCreds(); c.Reason != sl.ReasonUsed || c.Code == "" {
		t.Fatalf("code not rotated after use: %+v", c)
	}

	os.Close()
	select {
	case <-hs.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("host session not closed after operator close")
	}
	waitFor(t, func() bool { return e.metric(t, `rdv_session_end_total{outcome="bye"}`) == "1" })
	if got := e.metric(t, fmt.Sprintf(`rdv_session_path_total{path=%q}`, want)); got != "1" {
		t.Fatalf("path metric = %q", got)
	}
	if want.Relayed() {
		if e.metric(t, "rdv_relay_bytes_total") == "0" {
			t.Fatal("no relayed bytes counted")
		}
		// Ending the session closes its allocations at once.
		waitFor(t, func() bool { return e.metric(t, "rdv_relay_active") == "0" && e.metric(t, "rdv_relay_sessions") == "0" })
	}
	if e.metric(t, "rdv_turn_auth_fail_total") != "0" {
		t.Fatalf("TURN auth failures: %s", e.metric(t, "rdv_turn_auth_fail_total"))
	}
	assertLogsClean(t, e, hr)
}

// assertLogsClean: no code, no display name, no SDP, no full ID in logs.
func assertLogsClean(t *testing.T, e *testEnv, hr *hostRec) {
	t.Helper()
	logs := e.logs.String()
	hr.mu.Lock()
	defer hr.mu.Unlock()
	for _, c := range hr.creds {
		if strings.Contains(logs, c.Code) || strings.Contains(logs, c.ID) {
			t.Fatal("a code or a full ID reached the logs")
		}
	}
	for _, bad := range []string{testName, "a=fingerprint", "candidate:", testKey} {
		if strings.Contains(logs, bad) {
			t.Fatalf("%q reached the logs", bad)
		}
	}
	if !strings.Contains(logs, `"event":"session_end"`) {
		t.Fatal("session_end not logged")
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestDirect(t *testing.T) { runPath(t, loopback, loopback, sl.PathDirect) }

func TestRelayUDP(t *testing.T) {
	r := loopback
	r.ForceRelay = true
	runPath(t, r, r, sl.PathRelayUDP)
}

func TestRelayTCP(t *testing.T) {
	r := loopback
	r.ForceRelay, r.TURNTCPOnly = true, true
	runPath(t, r, r, sl.PathRelayTCP)
}

// TestRelayDefaultWait measures what the 500 ms relay acceptance wait buys
// against the WebRTC library default (2 s).
func TestRelayDefaultWait(t *testing.T) {
	if testing.Short() {
		t.Skip("timing comparison")
	}
	for _, w := range []time.Duration{-1, 0} {
		e := newEnv(t, nil)
		r := loopback
		r.ForceRelay, r.RelayAcceptanceMinWait = true, w
		_, _, _, _, setup := pair(t, e, r, r)
		name := "default 500ms"
		if w < 0 {
			name = "library default 2s"
		}
		t.Logf("relay setup with %s: %v", name, setup.Round(time.Millisecond))
	}
}

func TestWrongCodeLockout(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Limits.ConnectPerMinIP, c.Limits.ConnectBurstIP = 10, 10 })
	hr := startHost(t, e, loopback, allow, nil)
	id := hr.h.ID()
	cfg := opConfig(e, loopback, nil)
	for i := 1; i <= 5; i++ {
		code := hr.h.Code()
		wrong := "000000000"
		if code == wrong {
			wrong = "000000001"
		}
		_, err := connect(t, cfg, id, wrong)
		if !errors.Is(err, sl.ErrWrongCode) {
			t.Fatalf("attempt %d: want wrong code, got %v", i, err)
		}
		// The host burns its code after every failed attempt.
		waitFor(t, func() bool { return hr.h.Code() != code })
	}
	_, err := connect(t, cfg, id, "000000000")
	if !errors.Is(err, sl.ErrIDLocked) {
		t.Fatalf("6th attempt: want id_locked, got %v", err)
	}
	// Even the right code is refused on the locked ID...
	waitFor(t, func() bool { return hr.h.ID() != id })
	if _, err := connect(t, cfg, id, hr.h.Code()); !errors.Is(err, sl.ErrIDLocked) {
		t.Fatalf("right code on the locked ID: want id_locked, got %v", err)
	}
	// ...but the host moved to a new ID and a legitimate operator gets in.
	c, _ := hr.lastCreds()
	if c.Reason != sl.ReasonLocked || c.ID == id {
		t.Fatalf("host not moved to a new ID: %+v", c)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if s, err := hr.h.WaitForOperator(ctx); err == nil {
			defer s.Close()
			<-s.Done()
		}
	}()
	s, err := connect(t, cfg, hr.h.ID(), hr.h.Code())
	if err != nil {
		t.Fatalf("legitimate operator after the lock: %v", err)
	}
	s.Close()
	if got := e.metric(t, "rdv_pake_failed_total"); got != "5" {
		t.Fatalf("pake_failed = %s", got)
	}
	if got := e.metric(t, "rdv_id_locked_total"); got != "1" {
		t.Fatalf("id_locked = %s", got)
	}
}

// TestMITM: a compromised server replaces the host's sealed offer (it cannot
// produce a valid one without the code). Both sides abort before any frame.
func TestMITM(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.tamper = func(m *wire.Msg) {
			if m.T == wire.TOffer {
				raw, _ := base64.StdEncoding.DecodeString(m.Box)
				raw[len(raw)-1] ^= 0xFF
				m.Box = base64.StdEncoding.EncodeToString(raw)
			}
		}
	})
	hr := startHost(t, e, loopback, allow, nil)
	hc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := hr.h.WaitForOperator(ctx)
		hc <- err
	}()
	_, err := connect(t, opConfig(e, loopback, nil), hr.h.ID(), hr.h.Code())
	if !errors.Is(err, sl.ErrMITM) {
		t.Fatalf("operator: want ErrMITM, got %v", err)
	}
	if err := <-hc; !errors.Is(err, sl.ErrMITM) {
		t.Fatalf("host: want ErrMITM, got %v", err)
	}
}

// TestMITMCandidate: a tampered candidate is detected as well.
func TestMITMCandidate(t *testing.T) {
	var n atomic.Int32
	e := newEnv(t, func(c *Config) {
		c.tamper = func(m *wire.Msg) {
			if m.T == wire.TCand && n.Add(1) == 1 {
				m.Box = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64))
			}
		}
	})
	hr := startHost(t, e, loopback, allow, nil)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = hr.h.WaitForOperator(ctx)
	}()
	_, err := connect(t, opConfig(e, loopback, nil), hr.h.ID(), hr.h.Code())
	if !errors.Is(err, sl.ErrMITM) {
		t.Fatalf("want ErrMITM, got %v", err)
	}
}

func TestReject(t *testing.T) {
	e := newEnv(t, nil)
	hr := startHost(t, e, loopback, func(context.Context, sl.ApprovalRequest) bool { return false }, nil)
	code := hr.h.Code()
	_, err := connect(t, opConfig(e, loopback, nil), hr.h.ID(), code)
	if !errors.Is(err, sl.ErrRejected) {
		t.Fatalf("want rejected, got %v", err)
	}
	waitFor(t, func() bool { c, _ := hr.lastCreds(); return c.Reason == sl.ReasonRejected && c.Code != code })
	if e.metric(t, "rdv_rejected_total") != "1" || e.metric(t, "rdv_pake_failed_total") != "0" {
		t.Fatal("a refusal is not a wrong code")
	}
}

// TestDecisionTimeout: nobody answers the acceptance window; the server
// expires the request, the window is cancelled and the code rotated.
func TestDecisionTimeout(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.DecisionTimeout, c.SweepInterval = time.Second, 100*time.Millisecond })
	cancelled := make(chan struct{})
	hr := startHost(t, e, loopback, func(ctx context.Context, _ sl.ApprovalRequest) bool {
		<-ctx.Done()
		close(cancelled)
		return false
	}, nil)
	code := hr.h.Code()
	t0 := time.Now()
	_, err := connect(t, opConfig(e, loopback, nil), hr.h.ID(), code)
	if !errors.Is(err, sl.ErrExpired) {
		t.Fatalf("want expired, got %v", err)
	}
	if el := time.Since(t0); el > 3*time.Second {
		t.Fatalf("expiry took %v", el)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("acceptance window not cancelled")
	}
	waitFor(t, func() bool { return hr.h.Code() != code })
}

// rawOperator speaks the signalling protocol directly, to misbehave.
func rawOperator(t *testing.T, e *testEnv, id string) (*websocket.Conn, string) {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(e.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.WriteJSON(wire.Msg{T: wire.THello, Proto: wire.ProtocolVersion, Role: wire.RoleOperator, Ver: "1.0.0"})
	_ = c.WriteJSON(wire.Msg{T: wire.TConnect, ID: id, Key: testKey})
	var m wire.Msg
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := c.ReadJSON(&m); err != nil || m.T != wire.TSession {
		t.Fatalf("session: %+v %v", m, err)
	}
	return c, m.SID
}

func readUntil(t *testing.T, c *websocket.Conn, typ string, within time.Duration) wire.Msg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	for {
		var m wire.Msg
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("waiting for %s: %v", typ, err)
		}
		if m.T == typ {
			return m
		}
	}
}

// TestPakeTimeouts: an operator that opens a session and stays silent is
// expired by the server (no charge); one that starts the PAKE and never
// confirms is failed by the host (one charge, code burned).
func TestPakeTimeouts(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.PakeTimeout, c.SweepInterval = time.Second, 100*time.Millisecond })
	hr := startHost(t, e, loopback, allow, func(c *sl.HostConfig) { c.PakeTimeout = 500 * time.Millisecond })

	c, _ := rawOperator(t, e, hr.h.ID())
	m := readUntil(t, c, wire.TError, 3*time.Second)
	if m.Err != wire.ErrExpired {
		t.Fatalf("silent operator: %+v", m)
	}
	if e.metric(t, "rdv_pake_failed_total") != "0" {
		t.Fatal("a silent operator must not be charged")
	}

	code := hr.h.Code()
	c2, sid := rawOperator(t, e, hr.h.ID())
	// A well-formed CPace message (wrong code), then silence: the host
	// answers and waits for a confirmation that never comes.
	msgA, _, err := cpace.Start("000000000", cpace.NewContextInfo("operator", "host:"+hr.h.ID(), []byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	_ = c2.WriteJSON(wire.Msg{T: wire.TPake, SID: sid, Pake: base64.StdEncoding.EncodeToString(msgA)})
	readUntil(t, c2, wire.TPakeReply, 3*time.Second)
	m = readUntil(t, c2, wire.TError, 3*time.Second)
	if m.Err != wire.ErrBadCode {
		t.Fatalf("unconfirmed PAKE: %+v", m)
	}
	waitFor(t, func() bool { return hr.h.Code() != code })
	if e.metric(t, "rdv_pake_failed_total") != "1" {
		t.Fatal("an unconfirmed PAKE must be charged")
	}
}

// TestNoWebRTCBeforeAccept: offers, answers and candidates are dropped and
// no TURN credential exists before the host accepted.
func TestNoWebRTCBeforeAccept(t *testing.T) {
	e := newEnv(t, nil)
	hr := startHost(t, e, loopback, func(ctx context.Context, _ sl.ApprovalRequest) bool { <-ctx.Done(); return false }, nil)
	c, sid := rawOperator(t, e, hr.h.ID())
	_ = c.WriteJSON(wire.Msg{T: wire.TAnswer, SID: sid, Box: "AAAA"})
	_ = c.WriteJSON(wire.Msg{T: wire.TCand, SID: sid, Box: "AAAA"})
	_ = c.WriteJSON(wire.Msg{T: wire.TPakeOK, SID: sid}) // host-only message
	time.Sleep(200 * time.Millisecond)
	e.srv.hub.mu.Lock()
	s := e.srv.hub.sessions[sid]
	e.srv.hub.mu.Unlock()
	if s == nil || s.accepted || s.pakeOK {
		t.Fatalf("session state changed by the operator: %+v", s)
	}
	if e.srv.hub.sessionAccepted(sid) {
		t.Fatal("TURN would accept credentials before acceptance")
	}
}

// TestTURNCredentialsDieWithSession: the credentials handed out for a
// session stop working as soon as the session ends.
func TestTURNCredentialsDieWithSession(t *testing.T) {
	var mu sync.Mutex
	var ice *wire.ICEInfo
	e := newEnv(t, func(c *Config) {
		c.tamper = func(m *wire.Msg) {
			if m.T == wire.TAccept {
				mu.Lock()
				ice = m.ICE
				mu.Unlock()
			}
		}
	})
	keep := loopback
	keep.RelayReleaseAfter = -1 // the peers keep their allocations
	_, hs, os, _, _ := pair(t, e, keep, keep)
	mu.Lock()
	creds := *ice
	mu.Unlock()
	// During the session the credentials authenticate; the request is
	// refused by the per-session quota only (both peers already hold their
	// 4 allocations: ICE gathers relay candidates even when direct wins).
	if err := allocate(e, creds); err == nil || !strings.Contains(err.Error(), "486") {
		t.Fatalf("allocation during the session: want quota refusal (486), got %v", err)
	}
	if e.metric(t, "rdv_turn_auth_fail_total") != "0" {
		t.Fatal("valid credentials refused")
	}
	os.Close()
	<-hs.Done()
	waitFor(t, func() bool { return !e.srv.hub.sessionAccepted(strings.SplitN(creds.Username, ":", 2)[1]) })
	waitFor(t, func() bool { return e.metric(t, "rdv_relay_active") == "0" })
	if err := allocate(e, creds); err == nil {
		t.Fatal("credentials still valid after the session ended")
	}
	if e.metric(t, "rdv_turn_revoked_total") == "0" {
		t.Fatal("revoked credentials not refused at authentication")
	}
}

func allocate(e *testEnv, ice wire.ICEInfo) error {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer conn.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", e.srv.TURNPort())
	c, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: addr, TURNServerAddr: addr, Conn: conn,
		Username: ice.Username, Password: ice.Credential, Realm: turnRealm,
		RTO: 200 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Listen(); err != nil {
		return err
	}
	relay, err := c.Allocate()
	if err != nil {
		return err
	}
	return relay.Close()
}

func TestOperatorChecks(t *testing.T) {
	e := newEnv(t, nil)
	hr := startHost(t, e, loopback, allow, nil)
	cfg := opConfig(e, loopback, nil)

	bad := cfg
	bad.Key = "not-a-valid-operator-key"
	if _, err := connect(t, bad, hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrOperatorRequired) {
		t.Fatalf("bad key: %v", err)
	}
	bad.Key = ""
	if _, err := connect(t, bad, hr.h.ID(), hr.h.Code()); !errors.Is(err, sl.ErrOperatorRequired) {
		t.Fatalf("no key: %v", err)
	}
	unknown := "999999999"
	if unknown == hr.h.ID() {
		unknown = "999999998"
	}
	if _, err := connect(t, cfg, unknown, "123456789"); !errors.Is(err, sl.ErrUnknownID) {
		t.Fatalf("unknown ID: %v", err)
	}
	if _, err := connect(t, cfg, hr.h.ID(), "12345"); !errors.Is(err, sl.ErrInvalidCode) {
		t.Fatalf("short code: %v", err)
	}
	if e.metric(t, "rdv_pake_failed_total") != "0" {
		t.Fatal("refusals before the PAKE are not code attempts")
	}
}

func TestBusy(t *testing.T) {
	e := newEnv(t, nil)
	hr, _, _, _, _ := pair(t, e, loopback, loopback)
	_, err := connect(t, opConfig(e, loopback, nil), hr.h.ID(), hr.h.Code())
	if !errors.Is(err, sl.ErrBusy) {
		t.Fatalf("want busy, got %v", err)
	}
}

func TestRateLimitAndBan(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.Limits.ConnectPerMinIP, c.Limits.ConnectBurstIP = 10, 10
		c.Limits.FailsPerIP = 3
	})
	cfg := opConfig(e, loopback, nil)
	limited := false
	for i := 0; i < 12; i++ {
		_, err := connect(t, cfg, fmt.Sprintf("1%08d", i), "123456789")
		if errors.Is(err, sl.ErrRateLimited) {
			limited = i == 10
			break
		}
	}
	if !limited {
		t.Fatal("the 11th connect from one IP must be rate limited")
	}
	bad := cfg
	bad.Key = "wrong-operator-key-000000"
	for i := 0; i < 3; i++ {
		_, _ = connect(t, bad, "123456789", "123456789")
	}
	if _, err := connect(t, cfg, "123456789", "123456789"); !errors.Is(err, sl.ErrBanned) {
		t.Fatalf("want banned after repeated bad keys, got %v", err)
	}
}

func TestUpgradeRequired(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MinClientVersion = "1.2.0" })
	cfg := opConfig(e, loopback, nil)
	cfg.ClientVersion = "1.1.9"
	if _, err := connect(t, cfg, "123456789", "123456789"); !errors.Is(err, sl.ErrUpgradeRequired) {
		t.Fatalf("want upgrade_required, got %v", err)
	}
}

// TestHostLeavesDuringPake: the host goes away after answering the PAKE and
// before the operator confirmed. The operator is told the person left (not
// a wrong code), and nothing is charged: no failed attempt on the ID, none
// on the operator's IP.
func TestHostLeavesDuringPake(t *testing.T) {
	var cur atomic.Pointer[sl.Host]
	e := newEnv(t, func(c *Config) {
		c.tamper = func(m *wire.Msg) {
			if m.T == wire.TPakeReply {
				m.T = "held" // the operator never gets the reply (ignored type)...
				if h := cur.Load(); h != nil {
					go h.Close() // ...and the host closes meanwhile
				}
			}
		}
	})
	for i := 0; i < 6; i++ {
		hr := startHost(t, e, loopback, allow, nil)
		cur.Store(hr.h)
		_, err := connect(t, opConfig(e, loopback, nil), hr.h.ID(), hr.h.Code())
		if !errors.Is(err, sl.ErrPeerGone) || errors.Is(err, sl.ErrWrongCode) {
			t.Fatalf("round %d: want ErrPeerGone, got %v", i, err)
		}
	}
	waitFor(t, func() bool { return e.metric(t, `rdv_session_end_total{outcome="disconnected"}`) == "6" })
	if got := e.metric(t, "rdv_pake_failed_total"); got != "0" {
		t.Fatalf("pake_failed = %s: a host leaving must not be charged", got)
	}
	e.srv.hub.mu.Lock()
	ipFails, idFails := len(e.srv.hub.ipFails.m), len(e.srv.hub.idFails.m)
	e.srv.hub.mu.Unlock()
	if ipFails != 0 || idFails != 0 {
		t.Fatalf("failures recorded: ip %d, id %d", ipFails, idFails)
	}
}

// rawHost registers a host speaking the signalling protocol directly.
func rawHost(t *testing.T, e *testEnv) (*websocket.Conn, string) {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(e.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.WriteJSON(wire.Msg{T: wire.THello, Proto: wire.ProtocolVersion, Role: wire.RoleHost, Ver: "1.0.0"})
	_ = c.WriteJSON(wire.Msg{T: wire.TRegister})
	m := readUntil(t, c, wire.TRegistered, 5*time.Second)
	return c, m.ID
}

// TestByeDuringPake: a host bye between pake_reply and pake_ok is not a
// wrong code; an operator bye in the same window is (hanging up does not
// dodge the counter).
func TestByeDuringPake(t *testing.T) {
	e := newEnv(t, nil)
	host, id := rawHost(t, e)
	exchange := func() (*websocket.Conn, string) {
		op, sid := rawOperator(t, e, id)
		_ = op.WriteJSON(wire.Msg{T: wire.TPake, SID: sid, Pake: "AAAA"})
		readUntil(t, host, wire.TPake, 3*time.Second)
		_ = host.WriteJSON(wire.Msg{T: wire.TPakeReply, SID: sid, Pake: "AAAA", Conf: "AAAA"})
		readUntil(t, op, wire.TPakeReply, 3*time.Second)
		return op, sid
	}

	op, sid := exchange()
	_ = host.WriteJSON(wire.Msg{T: wire.TBye, SID: sid})
	if m := readUntil(t, op, wire.TBye, 3*time.Second); m.SID != sid {
		t.Fatalf("bye %+v", m)
	}
	waitFor(t, func() bool { return e.metric(t, `rdv_session_end_total{outcome="bye"}`) == "1" })
	if got := e.metric(t, "rdv_pake_failed_total"); got != "0" {
		t.Fatalf("host bye during the PAKE charged: pake_failed = %s", got)
	}

	op, sid = exchange()
	_ = op.WriteJSON(wire.Msg{T: wire.TBye, SID: sid, Reason: "bad_code"})
	readUntil(t, host, wire.TBye, 3*time.Second)
	waitFor(t, func() bool { return e.metric(t, "rdv_pake_failed_total") == "1" })

	// An operator that disconnects in the same window is charged as well.
	op, _ = exchange()
	op.Close()
	readUntil(t, host, wire.TBye, 3*time.Second)
	waitFor(t, func() bool { return e.metric(t, "rdv_pake_failed_total") == "2" })
}

// TestUpgradeSides: the server says which side is too old.
func TestUpgradeSides(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MinClientVersion = "1.2.0" })
	cfg := opConfig(e, loopback, nil)
	cfg.ClientVersion = "1.1.9"
	_, err := connect(t, cfg, "123456789", "123456789")
	var ue *sl.UpgradeError
	if !errors.As(err, &ue) || ue.Side != sl.UpgradeClient || ue.MinVersion != "1.2.0" || ue.ServerProto != wire.ProtocolVersion {
		t.Fatalf("old client: %#v (%v)", ue, err)
	}
	// The host side gets the same typed error at registration.
	_, err = sl.Register(context.Background(), sl.HostConfig{Server: e.url, ClientVersion: "1.0.0", Approve: allow})
	if !errors.As(err, &ue) || ue.Side != sl.UpgradeClient || !errors.Is(err, sl.ErrUpgradeRequired) {
		t.Fatalf("old host: %v", err)
	}
	// A client of an older protocol is told the server's protocol.
	c, _, err := websocket.DefaultDialer.Dial(e.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.WriteJSON(wire.Msg{T: wire.THello, Proto: wire.ProtocolVersion - 1, Role: wire.RoleOperator, Ver: "9.9.9"})
	m := readUntil(t, c, wire.TError, 3*time.Second)
	if m.Err != wire.ErrUpgradeRequired || m.Proto != wire.ProtocolVersion || m.Reason != "1.2.0" {
		t.Fatalf("old protocol: %+v", m)
	}
}
