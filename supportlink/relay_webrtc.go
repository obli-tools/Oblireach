package supportlink

// Shared by both build lines: the STUN encoding and the transport.Net type
// come from pion_v4.go (modern) or pion_v3_nt6.go (nt6), which only differ
// by the pion module versions they import.

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oblireach/supportlink/wire"
)

// Relay allocations: ICE gathers one relay candidate per TURN URL in every
// session, even when the direct path wins, and each holds a relay port on
// the server. relayTracker sees the sockets the TURN clients use (UDP
// through the transport.Net, TCP and TLS through the ICE proxy dialer),
// remembers the long-term credentials of each, and releases the
// allocations a session does not use (Refresh with LIFETIME 0, RFC 8656
// §7) once the selected pair has been stable for a while.
//
// The ICE library offers no way to close one local candidate. Releasing
// below it leaves the candidate in place but dead, which only matters if
// ICE later wants to switch to it: a session that loses its direct path
// fails instead of falling back to the relay (it is restarted by the
// application, as for any other network failure).

// DefaultRelayReleaseAfter is how long a selected pair must stay in place
// before the unused relay allocations are released.
const DefaultRelayReleaseAfter = 3 * time.Second

type relayTracker struct {
	user, pass string

	mu    sync.Mutex
	socks []*turnSock
}

// turnSock is one TURN client socket: one allocation.
type turnSock struct {
	t      *relayTracker
	pc     net.PacketConn // UDP
	c      net.Conn       // TCP or TLS
	local  net.Addr
	mu     sync.Mutex
	realm  []byte
	nonce  []byte
	server net.Addr
	done   bool
}

func (t *relayTracker) add(s *turnSock) {
	t.mu.Lock()
	t.socks = append(t.socks, s)
	t.mu.Unlock()
}

// sniff records the realm and nonce of an authenticated TURN request (and
// the server address, for UDP). Other traffic (indications, ChannelData)
// is skipped on the header alone.
func (s *turnSock) sniff(p []byte, to net.Addr) {
	realm, nonce, ok := stunRequestRealmNonce(p)
	if !ok {
		return
	}
	s.mu.Lock()
	s.realm, s.nonce = realm, nonce
	if to != nil {
		s.server = to
	}
	s.mu.Unlock()
}

// release deallocates and closes the socket. The TURN client above it then
// fails its next refresh, which ICE logs and ignores.
func (s *turnSock) release() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	realm, nonce, server := s.realm, s.nonce, s.server
	s.mu.Unlock()
	if realm == nil {
		s.closeLater(0) // never authenticated: nothing allocated
		return
	}
	raw, err := stunRefreshZero(s.t.user, s.t.pass, realm, nonce)
	if err != nil {
		s.closeLater(0)
		return
	}
	if s.pc != nil {
		if server == nil {
			s.closeLater(0)
			return
		}
		// UDP: sent twice against a loss; a second one gets 437, harmless.
		// The server reclaims idle allocations anyway.
		_, _ = s.pc.WriteTo(raw, server)
		time.AfterFunc(150*time.Millisecond, func() { _, _ = s.pc.WriteTo(raw, server) })
		s.closeLater(time.Second)
		return
	}
	// TCP / TLS: closing the connection alone deletes the allocation too.
	_, _ = s.c.Write(raw)
	s.closeLater(500 * time.Millisecond)
}

func (s *turnSock) closeLater(d time.Duration) {
	time.AfterFunc(d, func() {
		if s.pc != nil {
			_ = s.pc.Close()
		} else {
			_ = s.c.Close()
		}
	})
}

// releaseExcept releases every allocation but the one whose socket has the
// local address keep (the related address of the selected relay candidate;
// nil when the selected pair is not relayed here). It returns how many it
// released.
func (t *relayTracker) releaseExcept(keep *net.UDPAddr, keepTCP bool) int {
	t.mu.Lock()
	socks := append([]*turnSock(nil), t.socks...)
	t.mu.Unlock()
	n := 0
	for _, s := range socks {
		if keep != nil && sameLocal(s.local, keep, keepTCP) {
			continue
		}
		s.mu.Lock()
		done := s.done
		s.mu.Unlock()
		if !done {
			s.release()
			n++
		}
	}
	return n
}

func sameLocal(a net.Addr, keep *net.UDPAddr, tcp bool) bool {
	var ip net.IP
	var port int
	switch v := a.(type) {
	case *net.UDPAddr:
		if tcp {
			return false
		}
		ip, port = v.IP, v.Port
	case *net.TCPAddr:
		if !tcp {
			return false
		}
		ip, port = v.IP, v.Port
	default:
		return false
	}
	return port == keep.Port && (ip.Equal(keep.IP) || ip.IsUnspecified() || keep.IP.IsUnspecified())
}

// held is the number of allocations not released yet (tests).
func (t *relayTracker) held() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, s := range t.socks {
		s.mu.Lock()
		if !s.done && s.realm != nil {
			n++
		}
		s.mu.Unlock()
	}
	return n
}

// trackingNet hands ICE the TURN client UDP sockets through the tracker.
// ICE only uses ListenPacket for TURN over UDP; host and srflx candidates
// use ListenUDP.
type trackingNet struct {
	transportNet
	t *relayTracker
}

func newTrackingNet(t *relayTracker) (transportNet, error) {
	n, err := newStdNet()
	if err != nil {
		return nil, err
	}
	return &trackingNet{transportNet: n, t: t}, nil
}

func (n *trackingNet) ListenPacket(network, address string) (net.PacketConn, error) {
	pc, err := n.transportNet.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	s := &turnSock{t: n.t, pc: pc, local: pc.LocalAddr()}
	n.t.add(s)
	return &trackedPacketConn{PacketConn: pc, s: s}, nil
}

type trackedPacketConn struct {
	net.PacketConn
	s *turnSock
}

func (c *trackedPacketConn) WriteTo(p []byte, a net.Addr) (int, error) {
	c.s.sniff(p, a)
	return c.PacketConn.WriteTo(p, a)
}

type trackedConn struct {
	net.Conn
	s *turnSock
}

func (c *trackedConn) Write(p []byte) (int, error) {
	c.s.sniff(p, nil)
	return c.Conn.Write(p)
}

// turnDialer is the ICE proxy dialer: every TURN connection over TCP (turn:
// ...?transport=tcp) and TLS (turns:) goes through it, directly or through
// the HTTP proxy (CONNECT). It terminates TLS itself for turns: servers,
// with the signalling TLS settings (roots, pinning) and the URL host as
// server name.
type turnDialer struct {
	proxy   proxyFunc
	tlsHost map[string]string // "host:port" of turns: URLs -> server name
	tlsConf *tls.Config
	t       *relayTracker
	// tcpDelay / tlsDelay hold back the TURN connections over TCP and TLS
	// (relayHoldBack; zero on the modern line).
	tcpDelay, tlsDelay time.Duration
}

// relayHoldBack returns how long to hold back the TURN connections over
// TCP and over TLS for the ICE servers a session uses. ICE v4 (modern line)
// ranks relay candidates UDP > TCP > TLS by itself (relayHoldBackStep is 0
// there). ICE v2 (nt6 line, pion/webrtc v3) gives every relay candidate the
// same priority, so the first pair to succeed wins and a session could end
// up relayed over TCP or TLS while UDP works. Gathering the stream relays
// later reproduces the modern order: UDP first when a UDP TURN URL is in
// use, TCP before TLS otherwise. Where UDP is blocked, it only delays the
// stream relay candidates by that much.
func relayHoldBack(ice *wire.ICEInfo, o ICEOptions) (tcp, tls time.Duration) {
	if ice == nil || relayHoldBackStep == 0 {
		return 0, 0
	}
	udpTURN, tcpTURN := false, false
	for _, u := range ice.URLs {
		if !strings.HasPrefix(u, "turn:") || o.TURNTLSOnly {
			continue
		}
		if strings.Contains(u, "transport=tcp") {
			tcpTURN = true
		} else if !o.TURNTCPOnly {
			udpTURN = true
		}
	}
	switch {
	case udpTURN:
		return relayHoldBackStep, relayHoldBackStep + relayHoldBackStep/2
	case tcpTURN:
		return 0, relayHoldBackStep / 2
	}
	return 0, 0
}

const turnDialTimeout = 10 * time.Second

func (d *turnDialer) Dial(network, addr string) (net.Conn, error) {
	wait := d.tcpDelay
	if _, ok := d.tlsHost[addr]; ok {
		wait = d.tlsDelay
	}
	if wait > 0 {
		time.Sleep(wait)
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnDialTimeout)
	defer cancel()
	if network == "" || !strings.HasPrefix(network, "tcp") {
		network = "tcp"
	}
	c, err := dialVia(ctx, d.proxy, network, proxyTarget("https", addr), addr)
	if err != nil {
		return nil, err
	}
	if name, ok := d.tlsHost[addr]; ok {
		var cfg *tls.Config
		if d.tlsConf != nil {
			cfg = d.tlsConf.Clone()
		} else {
			cfg = &tls.Config{}
		}
		cfg.ServerName = name
		cfg.NextProtos = []string{"stun.turn"} // RFC 7443
		if cfg.MinVersion == 0 {
			cfg.MinVersion = tls.VersionTLS12
		}
		tc := tls.Client(c, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, err
		}
		c = tc
	}
	s := &turnSock{t: d.t, c: c, local: c.LocalAddr()}
	d.t.add(s)
	return &trackedConn{Conn: c, s: s}, nil
}

// turnTLSHosts maps the "host:port" ICE dials for each turns: URL to the
// TLS server name.
func turnTLSHosts(urls []string) map[string]string {
	out := map[string]string{}
	for _, u := range urls {
		if !strings.HasPrefix(u, "turns:") {
			continue
		}
		host, port, err := stunURIHostPort(u)
		if err != nil {
			continue
		}
		if port == 0 {
			port = 5349
		}
		out[net.JoinHostPort(host, strconv.Itoa(port))] = host
	}
	return out
}
