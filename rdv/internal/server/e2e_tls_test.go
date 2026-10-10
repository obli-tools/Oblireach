package server

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sl "github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}, pool
}

// newTLSEnv serves the signalling over TLS through ServeTLS, with TURN-TLS
// on the same port (the production layout of 443).
func newTLSEnv(t *testing.T, mut func(*Config)) *testEnv {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return newTLSEnvOn(t, ln, ln.Addr().(*net.TCPAddr).Port, mut)
}

// newTLSEnvOn serves the shared TLS port on ln; clients reach it on
// 127.0.0.1:port (ln itself, or a proxy in front).
func newTLSEnvOn(t *testing.T, ln net.Listener, port int, mut func(*Config)) *testEnv {
	t.Helper()
	logs := &syncBuf{}
	cfg := testConfig(logs)
	cfg.TURNTLSPort, cfg.TURNTLSHost = port, "127.0.0.1"
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
	cert, pool := selfSigned(t)
	hs := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- s.ServeTLS(hs, ln, &tls.Config{Certificates: []tls.Certificate{cert}}) }()
	as := httptest.NewServer(s.AdminHandler())
	t.Cleanup(func() {
		s.Close()
		_ = hs.Close()
		if err := <-served; err != http.ErrServerClosed {
			t.Errorf("ServeTLS returned %v", err)
		}
		as.Close()
	})
	return &testEnv{srv: s, url: "wss://127.0.0.1:" + strconv.Itoa(port) + wire.Path, admin: as.URL, logs: logs,
		sig: sl.SignalOptions{TLSConfig: &tls.Config{RootCAs: pool}, Proxy: sl.ProxyDirect}}
}

func (e *testEnv) tlsAddr() string {
	return "127.0.0.1:" + strconv.Itoa(e.srv.cfg.TURNTLSPort)
}

func tlsOnly() sl.ICEOptions {
	r := loopback
	r.ForceRelay, r.TURNTLSOnly = true, true
	return r
}

// TestTLSMux: HTTPS, TURN and garbage on the shared port.
func TestTLSMux(t *testing.T) {
	e := newTLSEnv(t, nil)
	roots := e.sig.TLSConfig.RootCAs

	// HTTPS, and no h2 on the shared port.
	c, err := tls.Dial("tcp", e.tlsAddr(), &tls.Config{RootCAs: roots, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if p := c.ConnectionState().NegotiatedProtocol; p != "http/1.1" {
		t.Fatalf("ALPN %q", p)
	}
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	c.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<h1>Oblireach Support</h1>") {
		t.Fatalf("GET /: %d %q", resp.StatusCode, body)
	}

	// STUN binding over TLS, routed by the first bytes (no ALPN) and by
	// ALPN stun.turn (RFC 7443).
	for _, alpn := range [][]string{nil, {"stun.turn"}} {
		c, err := tls.Dial("tcp", e.tlsAddr(), &tls.Config{RootCAs: roots, NextProtos: alpn})
		if err != nil {
			t.Fatal(err)
		}
		req := make([]byte, 20)
		binary.BigEndian.PutUint16(req[0:2], 0x0001) // Binding request
		copy(req[4:8], stunMagic)
		copy(req[8:20], "abcdefghijkl")
		if _, err := c.Write(req); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		hdr := make([]byte, 20)
		if _, err := io.ReadFull(c, hdr); err != nil {
			t.Fatalf("alpn %v: no STUN answer: %v", alpn, err)
		}
		if binary.BigEndian.Uint16(hdr[0:2]) != 0x0101 || string(hdr[8:20]) != "abcdefghijkl" {
			t.Fatalf("alpn %v: answer %x", alpn, hdr)
		}
		c.Close()
	}

	// Neither HTTP nor STUN: closed.
	c, err = tls.Dial("tcp", e.tlsAddr(), &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte{0x80, 1, 2, 3, 4, 5, 6, 7, 8})
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := c.Read(make([]byte, 16)); err == nil || n > 0 {
		t.Fatalf("garbage not closed: %d %v", n, err)
	}
	c.Close()
}

// TestDirectTLS: signalling over the shared port, direct media.
func TestDirectTLS(t *testing.T) {
	runPathEnv(t, newTLSEnv(t, nil), loopback, loopback, sl.PathDirect)
}

// TestRelayTLS: only TURN over TLS on the signalling port is reachable
// (no STUN, no turn: URL on 3478).
func TestRelayTLS(t *testing.T) {
	e := newTLSEnv(t, nil)
	runPathEnv(t, e, tlsOnly(), tlsOnly(), sl.PathRelayTLS)
	if e.metric(t, `rdv_relay_alloc_total{transport="tls"}`) == "" ||
		e.metric(t, `rdv_relay_alloc_total{transport="udp"}`) != "" || e.metric(t, `rdv_relay_alloc_total{transport="tcp"}`) != "" {
		t.Fatalf("allocations by transport: tls=%q udp=%q tcp=%q", e.metric(t, `rdv_relay_alloc_total{transport="tls"}`),
			e.metric(t, `rdv_relay_alloc_total{transport="udp"}`), e.metric(t, `rdv_relay_alloc_total{transport="tcp"}`))
	}
	if !strings.Contains(e.logs.String(), `"client_transport":"tls"`) {
		t.Fatal("relay_alloc not logged with client_transport tls")
	}
}

// connectProxy is a minimal HTTP CONNECT proxy recording its targets.
type connectProxy struct {
	l       net.Listener
	mu      sync.Mutex
	targets []string
}

func newConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &connectProxy{l: l}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go p.handle(c)
		}
	}()
	t.Cleanup(func() { l.Close() })
	return p
}

func (p *connectProxy) url() string { return "http://" + p.l.Addr().String() }

func (p *connectProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func (p *connectProxy) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		return
	}
	p.mu.Lock()
	p.targets = append(p.targets, req.Host)
	p.mu.Unlock()
	up, err := net.Dial("tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// TestRelayTLSProxy: both sides behind an HTTP proxy that only allows
// CONNECT; signalling and TURN-TLS go through it.
func TestRelayTLSProxy(t *testing.T) {
	e := newTLSEnv(t, nil)
	p := newConnectProxy(t)
	e.sig.Proxy = p.url()
	runPathEnv(t, e, tlsOnly(), tlsOnly(), sl.PathRelayTLS)
	got := p.seen()
	n := 0
	for _, h := range got {
		if h != e.tlsAddr() {
			t.Fatalf("unexpected CONNECT target %q", h)
		}
		n++
	}
	// 2 signalling connections + at least one TURN-TLS connection per side.
	if n < 4 {
		t.Fatalf("CONNECTs through the proxy: %v", got)
	}
	t.Logf("CONNECT tunnels: %d", n)
}

// TestRelayTLSProxyOneSide: a host in a "443 through the proxy only"
// network and an operator on an open network.
func TestRelayTLSProxyOneSide(t *testing.T) {
	e := newTLSEnv(t, nil)
	p := newConnectProxy(t)
	hostSig := e.sig
	hostSig.Proxy = p.url()
	hr := startHost(t, e, tlsOnly(), allow, func(c *sl.HostConfig) { c.SignalOptions = hostSig })
	hc := make(chan *sl.Session, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, err := hr.h.WaitForOperator(ctx)
		if err != nil {
			t.Error(err)
		}
		hc <- s
	}()
	t0 := time.Now()
	os, err := connect(t, opConfig(e, loopback, nil), wire.Group3(hr.h.ID()), wire.Group3(hr.h.Code()))
	if err != nil {
		t.Fatal(err)
	}
	setup := time.Since(t0)
	hs := <-hc
	if hs == nil {
		t.FailNow()
	}
	defer hs.Close()
	defer os.Close()
	if hs.Path() != sl.PathRelayTLS || !os.Path().Relayed() {
		t.Fatalf("paths host=%s (%s) operator=%s (%s)", hs.Path(), hs.PathDetail(), os.Path(), os.PathDetail())
	}
	rtt, mbps := exchange(t, hs, os)
	t.Logf("host relay_tls via proxy, operator %s: setup %v, ctl RTT p50 %v, bulk %.0f Mbit/s", os.Path(), setup.Round(time.Millisecond), rtt.Round(10*time.Microsecond), mbps)
	if len(p.seen()) < 2 {
		t.Fatalf("host did not use the proxy: %v", p.seen())
	}
}

// allocSample polls the open allocations of a session from connect to
// `until`, returning the peak and the last value.
func allocSample(t *testing.T, e *testEnv, until time.Duration) (peak, last int) {
	t.Helper()
	end := time.Now().Add(until)
	for time.Now().Before(end) {
		v, _ := strconv.Atoi(e.metric(t, "rdv_relay_active"))
		if v > peak {
			peak = v
		}
		last = v
		time.Sleep(50 * time.Millisecond)
	}
	return peak, last
}

// TestRelayAllocations measures the relay allocations a session holds,
// before (allocations kept for the whole session) and after the client
// release and the server idle reclaim.
func TestRelayAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement, about 40 s")
	}
	type scen struct {
		name     string
		release  time.Duration // client RelayReleaseAfter
		idle     time.Duration // server RelayIdleTimeout
		relay    bool
		wantLast int // max allocations left at the end
	}
	const window = 5500 * time.Millisecond
	var report []string
	for _, sc := range []scen{
		{"direct, before (kept)", -1, -1, false, 6},
		{"direct, client release 3 s", 0, -1, false, 0},
		{"direct, server reclaim 3 s only", -1, 3 * time.Second, false, 0},
		{"relay, before (kept)", -1, -1, true, 6},
		{"relay, client release 3 s", 0, -1, true, 2},
		{"relay, server reclaim 3 s only", -1, 3 * time.Second, true, 2},
	} {
		t.Run(sc.name, func(t *testing.T) {
			e := newTLSEnv(t, func(c *Config) { c.RelayIdleTimeout = sc.idle })
			ice := loopback
			ice.ForceRelay, ice.RelayReleaseAfter = sc.relay, sc.release
			_, hs, os, _, setup := pair(t, e, ice, ice)
			peak, last := allocSample(t, e, window)
			if last > sc.wantLast {
				t.Fatalf("%d allocations left after %v (peak %d), want <= %d", last, window, peak, sc.wantLast)
			}
			// The session survives the release / reclaim.
			rtt, mbps := exchange(t, hs, os)
			line := fmt.Sprintf("%-34s path %-9s setup %5v  peak %d  after %v: %d  (then RTT %v, %.0f Mbit/s)",
				sc.name, os.Path(), setup.Round(time.Millisecond), peak, window, last, rtt.Round(10*time.Microsecond), mbps)
			report = append(report, line)
		})
	}
	for _, l := range report {
		t.Log(l)
	}
}
