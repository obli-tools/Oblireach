package supportlink

// Shared by both build lines; the TURN server and client of the test come
// from turn_v5_test.go (modern) or turn_v2_nt6_test.go (nt6).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/oblireach/supportlink/wire"
)

func TestICEServersFilter(t *testing.T) {
	ice := &wire.ICEInfo{URLs: []string{
		"stun:rdv:3478", "turn:rdv:3478?transport=udp", "turn:rdv:3478?transport=tcp", "turns:rdv:443?transport=tcp",
	}, Username: "u", Credential: "c"}
	count := func(o ICEOptions) (stun, turn int) {
		for _, s := range iceServers(ice, o) {
			if s.Username == "" {
				stun += len(s.URLs)
			} else {
				turn += len(s.URLs)
			}
		}
		return
	}
	for _, c := range []struct {
		o          ICEOptions
		stun, turn int
	}{
		{ICEOptions{}, 1, 3},
		{ICEOptions{TURNTCPOnly: true}, 0, 2},
		{ICEOptions{TURNTLSOnly: true}, 0, 1},
	} {
		if s, tu := count(c.o); s != c.stun || tu != c.turn {
			t.Fatalf("%+v: stun %d turn %d", c.o, s, tu)
		}
	}
	h := turnTLSHosts(ice.URLs)
	if len(h) != 1 || h["rdv:443"] != "rdv" {
		t.Fatalf("tls hosts %v", h)
	}
}

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

// TestRelayRelease allocates through the tracked sockets (UDP, TCP, TLS)
// and checks that release deletes the allocations on a real TURN server.
func TestRelayRelease(t *testing.T) {
	const user, pass, realm = "1:sid", "secret", "test"
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cert, pool := selfSigned(t)
	tl, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	srv := startTestTURN(t, realm, user, pass, udp, tcp, tl)
	defer srv.close()

	rt := &relayTracker{user: user, pass: pass}
	tn, err := newTrackingNet(rt)
	if err != nil {
		t.Fatal(err)
	}
	tlsAddr := tl.Addr().String()
	d := &turnDialer{tlsHost: map[string]string{tlsAddr: "127.0.0.1"}, tlsConf: &tls.Config{RootCAs: pool}, t: rt}

	allocate := func(pc net.PacketConn, c net.Conn, server string) func() {
		return testTURNAllocate(t, realm, user, pass, pc, c, server)
	}
	pc, err := tn.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer allocate(pc, nil, udp.LocalAddr().String())()
	tc, err := d.Dial("tcp4", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer allocate(nil, tc, "")()
	sc, err := d.Dial("tcp4", tlsAddr)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sc.(*trackedConn).Conn.(*tls.Conn); !ok {
		t.Fatal("turns: connection not wrapped in TLS")
	}
	defer allocate(nil, sc, "")()

	if n := srv.count(); n != 3 || rt.held() != 3 {
		t.Fatalf("allocations %d, held %d", n, rt.held())
	}
	// Keep the TCP one (as when it carries the selected relay pair).
	keep := tc.LocalAddr().(*net.TCPAddr)
	if n := rt.releaseExcept(&net.UDPAddr{IP: keep.IP, Port: keep.Port}, true); n != 2 {
		t.Fatalf("released %d", n)
	}
	waitUntil(t, func() bool { return srv.count() == 1 })
	if rt.held() != 1 {
		t.Fatalf("held %d", rt.held())
	}
	rt.releaseExcept(nil, false)
	waitUntil(t, func() bool { return srv.count() == 0 })
}

func waitUntil(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

// TestRelayHoldBack: on the nt6 line the stream relays are gathered after
// the UDP one (TCP before TLS); nothing is held back on the modern line.
func TestRelayHoldBack(t *testing.T) {
	ice := &wire.ICEInfo{URLs: []string{
		"stun:rdv:3478", "turn:rdv:3478?transport=udp", "turn:rdv:3478?transport=tcp", "turns:rdv:443?transport=tcp",
	}, Username: "u", Credential: "c"}
	step := relayHoldBackStep
	for _, c := range []struct {
		o        ICEOptions
		tcp, tls time.Duration
	}{
		{ICEOptions{}, step, step + step/2},
		{ICEOptions{ForceRelay: true}, step, step + step/2},
		{ICEOptions{TURNTCPOnly: true}, 0, step / 2},
		{ICEOptions{TURNTLSOnly: true}, 0, 0},
	} {
		if tcp, tls := relayHoldBack(ice, c.o); tcp != c.tcp || tls != c.tls {
			t.Errorf("%+v: tcp %v tls %v, want %v %v", c.o, tcp, tls, c.tcp, c.tls)
		}
	}
	if tcp, tls := relayHoldBack(&wire.ICEInfo{URLs: []string{"turns:rdv:443?transport=tcp"}}, ICEOptions{}); tcp != 0 || tls != 0 {
		t.Errorf("TLS only on the server: %v %v", tcp, tls)
	}
}
