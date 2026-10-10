package server

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // TURN REST credentials are defined over HMAC-SHA1
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

const turnRealm = "oblireach"

// turnPassword is the TURN REST password of a username
// (draft-uberti-behave-turn-rest): base64(HMAC-SHA1(secret, username)).
func turnPassword(secret, username string) string {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// turnServer wraps pion/turn with the session binding: credentials are only
// valid while their session is alive, relays are metered and capped per
// session, reclaimed when idle, and closed when the session ends.
//
// Clients reach it over UDP and TCP on TURNPort, and over TLS on the
// signalling port when TURN-TLS is on (ServeTLS hands those connections to
// a dedicated listener config).
type turnServer struct {
	s    *Server
	srv  *turn.Server
	port int
	udp  net.PacketConn
	tcp  net.Listener

	mu     sync.Mutex
	relays map[string]*relayState // by session id
	tlsSrc map[string]bool        // remote addresses of TURN-TLS connections
}

// relayState is the relay usage of one session.
type relayState struct {
	sid     string
	conns   map[*relayConn]struct{}
	in, out *byteLimiter
	bytes   atomic.Int64
	// total is the session's counter (all its allocations, reclaimed ones
	// included); nil when the session was already gone.
	total  *atomic.Int64
	closed bool
}

func startTURN(s *Server) (*turnServer, error) {
	t := &turnServer{s: s, relays: map[string]*relayState{}, tlsSrc: map[string]bool{}}
	if err := t.listen(); err != nil {
		return nil, err
	}
	gen := func() turn.RelayAddressGenerator {
		return &relayGen{t: t, inner: &turn.RelayAddressGeneratorPortRange{
			RelayAddress: s.cfg.PublicIP,
			Address:      s.cfg.TURNListenIP,
			MinPort:      s.cfg.RelayMinPort,
			MaxPort:      s.cfg.RelayMaxPort,
			MaxRetries:   30,
		}}
	}
	listeners := []turn.ListenerConfig{{Listener: t.tcp, RelayAddressGenerator: gen(), PermissionHandler: t.permission}}
	if s.turnsL != nil {
		listeners = append(listeners, turn.ListenerConfig{Listener: s.turnsL, RelayAddressGenerator: gen(), PermissionHandler: t.permission})
	}
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm:         turnRealm,
		LoggerFactory: &slogFactory{l: s.cfg.Logger},
		AuthHandler:   t.auth,
		QuotaHandler:  t.quota,
		EventHandler: turn.EventHandler{
			OnAuth: func(src, _ net.Addr, _, _, _, _ string, ok bool) {
				if !ok {
					s.m.turnAuthFail.Add(1)
				}
			},
			OnAllocationCreated: func(src, _ net.Addr, _, user, _ string, _ net.Addr, _ int) {
				s.m.relayAllocs.Add(1)
				tr := t.clientTransport(src)
				s.m.inc(s.m.allocs, tr)
				host, _, _ := net.SplitHostPort(src.String())
				s.log.event("relay_alloc", "sid", truncSID(user), "client_transport", tr, "ip", s.log.ip(host))
			},
			OnAllocationDeleted: func(_, _ net.Addr, _, _, _ string) {
				s.m.relayAllocs.Add(-1)
			},
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: t.udp, RelayAddressGenerator: gen(), PermissionHandler: t.permission}},
		ListenerConfigs:   listeners,
	})
	if err != nil {
		t.udp.Close()
		t.tcp.Close()
		return nil, err
	}
	t.srv = srv
	if s.cfg.RelayIdleTimeout > 0 {
		go t.reclaimLoop(s.stop)
	}
	return t, nil
}

// clientTransport is how the client reaches TURN for an allocation: udp,
// tcp, or tls (connections split off the signalling port).
func (t *turnServer) clientTransport(src net.Addr) string {
	if _, ok := src.(*net.UDPAddr); ok {
		return "udp"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tlsSrc[src.String()] {
		return "tls"
	}
	return "tcp"
}

func (t *turnServer) markTLS(remote string, on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if on {
		t.tlsSrc[remote] = true
	} else {
		delete(t.tlsSrc, remote)
	}
}

// maxAllocs is the per-session allocation cap: each of the two peers may
// gather one relay candidate per TURN URL handed out (udp, tcp, and tls
// when on).
func (t *turnServer) maxAllocs() int {
	if t.s.turnsL != nil {
		return 6
	}
	return 4
}

// reclaimLoop closes the allocations that relayed nothing for
// RelayIdleTimeout. ICE gathers relay candidates in every session, direct
// ones included; without this each session would hold up to maxAllocs relay
// ports for its whole duration. Clients release the allocations they do not
// use once a pair is selected (supportlink); this covers older clients,
// lost releases (UDP) and crashed peers. A reclaimed allocation is gone for
// good: its client gets 437 (allocation mismatch) on the next refresh,
// which ICE ignores for a candidate it does not use.
func (t *turnServer) reclaimLoop(stop <-chan struct{}) {
	every := t.s.cfg.RelayIdleTimeout / 4
	if every > time.Second {
		every = time.Second
	}
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-tk.C:
			t.reclaimIdle(now)
		}
	}
}

func (t *turnServer) reclaimIdle(now time.Time) {
	limit := now.Add(-t.s.cfg.RelayIdleTimeout).UnixNano()
	var idle []*relayConn
	t.mu.Lock()
	for _, st := range t.relays {
		for c := range st.conns {
			if c.last.Load() < limit {
				idle = append(idle, c)
			}
		}
	}
	t.mu.Unlock()
	for _, c := range idle {
		t.s.m.relayReclaimed.Add(1)
		_ = c.Close() // pion deletes the allocation when its relay socket fails
	}
}

// listen binds UDP and TCP on the same port (port 0 picks one, tests only).
func (t *turnServer) listen() error {
	ip := t.s.cfg.TURNListenIP
	if t.s.cfg.TURNPort != 0 {
		hp := net.JoinHostPort(ip, strconv.Itoa(t.s.cfg.TURNPort))
		udp, err := net.ListenPacket("udp4", hp)
		if err != nil {
			return fmt.Errorf("turn udp: %w", err)
		}
		tcp, err := net.Listen("tcp4", hp)
		if err != nil {
			udp.Close()
			return fmt.Errorf("turn tcp: %w", err)
		}
		t.udp, t.tcp, t.port = udp, tcp, t.s.cfg.TURNPort
		return nil
	}
	// Free port (tests): TCP first. Windows reserves TCP port ranges
	// (Hyper-V exclusions) that the UDP ephemeral allocator happily hands
	// out, while a free TCP port is almost always free in UDP too.
	// Excluded ranges come in blocks of 100+ consecutive ports: keep trying.
	for try := 0; try < 500; try++ {
		tcp, err := net.Listen("tcp4", net.JoinHostPort(ip, "0"))
		if err != nil {
			return fmt.Errorf("turn tcp: %w", err)
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		udp, err := net.ListenPacket("udp4", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err != nil {
			tcp.Close()
			continue
		}
		t.udp, t.tcp, t.port = udp, tcp, port
		return nil
	}
	return errors.New("turn: no free port for udp+tcp")
}

func (t *turnServer) close() {
	if t.srv != nil {
		_ = t.srv.Close()
	}
}

// sidOf extracts the session id from a TURN REST username "expiry:sid".
func sidOf(username string) (sid string, exp int64, ok bool) {
	i := strings.IndexByte(username, ':')
	if i <= 0 {
		return "", 0, false
	}
	exp, err := strconv.ParseInt(username[:i], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return username[i+1:], exp, true
}

// auth validates TURN REST credentials and additionally requires the
// session to be alive and accepted: ending a session revokes its
// credentials at once (refreshes and new permissions fail).
func (t *turnServer) auth(ra *turn.RequestAttributes) (string, []byte, bool) {
	sid, exp, ok := sidOf(ra.Username)
	// pion reports a refused user without an OnAuth event: count here.
	if !ok || time.Now().Unix() > exp {
		t.s.m.turnAuthFail.Add(1)
		return "", nil, false
	}
	if !t.s.hub.sessionAccepted(sid) {
		// Includes the clients' own deallocation after the session ended.
		t.s.m.turnRevoked.Add(1)
		return "", nil, false
	}
	return sid, turn.GenerateAuthKey(ra.Username, ra.Realm, turnPassword(t.s.cfg.TURNSecret, ra.Username)), true
}

// quota admits an allocation: at most maxAllocs per session, and at most
// RelayMaxConcurrent sessions relaying at once. pion passes the user id the
// auth handler returned, i.e. the session id.
func (t *turnServer) quota(sid, _ string, _ net.Addr) bool {
	if !t.s.hub.sessionAccepted(sid) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.relays[sid]; st != nil {
		if len(st.conns) >= t.maxAllocs() {
			t.s.m.relayRefused.Add(1)
			return false
		}
		return true
	}
	if len(t.relays) >= t.s.cfg.RelayMaxConcurrent {
		t.s.m.relayRefused.Add(1)
		t.s.log.warn("relay_quota", "sid", truncSID(sid))
		return false
	}
	return true
}

// permission filters the peers a relay may send to. The relay address
// itself (relay-to-relay) is always allowed; private, loopback, link-local,
// multicast and unspecified addresses are refused so the relay cannot reach
// the server's own network (SSRF).
//
// TODO(v1): restrict to the addresses of the other party of the session.
func (t *turnServer) permission(_ net.Addr, peer net.IP) bool {
	if peer.Equal(t.s.cfg.PublicIP) {
		return true
	}
	if t.s.cfg.RelayAllowPrivatePeers {
		return true
	}
	return !(peer.IsPrivate() || peer.IsLoopback() || peer.IsLinkLocalUnicast() || peer.IsLinkLocalMulticast() ||
		peer.IsMulticast() || peer.IsUnspecified() || peer.IsInterfaceLocalMulticast())
}

// revoke closes the relays of a session and returns the bytes it relayed.
func (t *turnServer) revoke(sid string) int64 {
	t.mu.Lock()
	st := t.relays[sid]
	if st == nil {
		t.mu.Unlock()
		return 0
	}
	st.closed = true
	conns := make([]*relayConn, 0, len(st.conns))
	for c := range st.conns {
		conns = append(conns, c)
	}
	t.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return st.bytes.Load()
}

func (t *turnServer) attach(sid string, pc net.PacketConn) (*relayConn, error) {
	total := t.s.hub.relayCounter(sid) // hub.mu before t.mu, never the reverse
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.relays[sid]
	if st == nil {
		st = &relayState{sid: sid, conns: map[*relayConn]struct{}{},
			in: newByteLimiter(t.s.cfg.RelayMaxKbps), out: newByteLimiter(t.s.cfg.RelayMaxKbps), total: total}
		t.relays[sid] = st
		t.s.m.relaySessions.Add(1)
	}
	if st.closed {
		return nil, errors.New("session ended")
	}
	c := &relayConn{PacketConn: pc, st: st, t: t}
	c.last.Store(time.Now().UnixNano())
	st.conns[c] = struct{}{}
	return c, nil
}

func (t *turnServer) detach(c *relayConn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := c.st
	delete(st.conns, c)
	if len(st.conns) == 0 && t.relays[st.sid] == st {
		delete(t.relays, st.sid)
		t.s.m.relaySessions.Add(-1)
	}
}

// relayGen allocates UDP relays in the configured port range and wraps them
// for metering. TCP relays (RFC 6062) are not offered: WebRTC never uses
// them, and fewer features mean less attack surface. Clients may still
// reach TURN over TCP; the relayed leg is UDP.
type relayGen struct {
	t     *turnServer
	inner *turn.RelayAddressGeneratorPortRange
}

var errNoTCPRelay = errors.New("tcp relay allocations are not supported")

func (g *relayGen) Validate() error { return g.inner.Validate() }

func (g *relayGen) AllocatePacketConn(conf turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	pc, addr, err := g.inner.AllocatePacketConn(conf)
	if err != nil {
		return nil, nil, err
	}
	c, err := g.t.attach(conf.UserID, pc)
	if err != nil {
		pc.Close()
		return nil, nil, err
	}
	return c, addr, nil
}

func (g *relayGen) AllocateListener(turn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	return nil, nil, errNoTCPRelay
}

func (g *relayGen) AllocateConn(turn.AllocateConnConfig) (net.Conn, error) {
	return nil, errNoTCPRelay
}

// relayConn meters and polices one relay socket.
type relayConn struct {
	net.PacketConn
	st   *relayState
	t    *turnServer
	once sync.Once
	last atomic.Int64 // unix nanoseconds of the last relayed packet
}

func (c *relayConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, a, err := c.PacketConn.ReadFrom(p)
		if err != nil || n == 0 {
			return n, a, err
		}
		if !c.st.in.allow(n) {
			c.t.s.m.relayDropped.Add(int64(n))
			continue
		}
		c.count(n)
		return n, a, nil
	}
}

func (c *relayConn) WriteTo(p []byte, a net.Addr) (int, error) {
	// Activity is what the authenticated client sends: packets arriving
	// from the internet on the relay port must not keep it alive.
	c.last.Store(time.Now().UnixNano())
	if !c.st.out.allow(len(p)) {
		c.t.s.m.relayDropped.Add(int64(len(p)))
		return len(p), nil
	}
	n, err := c.PacketConn.WriteTo(p, a)
	c.count(n)
	return n, err
}

func (c *relayConn) count(n int) {
	c.st.bytes.Add(int64(n))
	if c.st.total != nil {
		c.st.total.Add(int64(n))
	}
	c.t.s.m.relayBytes.Add(int64(n))
}

func (c *relayConn) Close() error {
	c.once.Do(func() { c.t.detach(c) })
	return c.PacketConn.Close()
}

func truncSID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

// slogFactory routes pion's logs to the JSON logger at debug level: they
// may contain addresses, which RDV_LOG_IP_MODE would not cover.
type slogFactory struct{ l *slog.Logger }

func (f *slogFactory) NewLogger(scope string) logging.LeveledLogger {
	return &slogLogger{l: f.l.With("component", "turn", "scope", scope)}
}

type slogLogger struct{ l *slog.Logger }

func (s *slogLogger) Trace(string)                  {}
func (s *slogLogger) Tracef(string, ...interface{}) {}
func (s *slogLogger) Debug(m string)                { s.l.Debug(m) }
func (s *slogLogger) Debugf(f string, a ...interface{}) {
	s.l.Debug(fmt.Sprintf(f, a...))
}
func (s *slogLogger) Info(m string)                    { s.l.Debug(m) }
func (s *slogLogger) Infof(f string, a ...interface{}) { s.l.Debug(fmt.Sprintf(f, a...)) }
func (s *slogLogger) Warn(m string)                    { s.l.Debug(m) }
func (s *slogLogger) Warnf(f string, a ...interface{}) { s.l.Debug(fmt.Sprintf(f, a...)) }
func (s *slogLogger) Error(m string)                   { s.l.Debug(m) }
func (s *slogLogger) Errorf(f string, a ...interface{}) {
	s.l.Debug(fmt.Sprintf(f, a...))
}
