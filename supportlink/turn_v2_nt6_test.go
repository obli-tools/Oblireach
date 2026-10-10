//go:build nt6

package supportlink

// TURN server and client of TestRelayRelease, nt6 line (pion/turn v2,
// Go 1.20). Same as turn_v5_test.go but for the v2 API.

import (
	"net"
	"testing"
	"time"

	"github.com/pion/turn/v2"
)

type testTURN struct{ s *turn.Server }

func (s testTURN) count() int { return s.s.AllocationCount() }
func (s testTURN) close()     { _ = s.s.Close() }

func startTestTURN(t *testing.T, realm, user, pass string, udp net.PacketConn, tcp, tl net.Listener) testTURN {
	t.Helper()
	gen := func() turn.RelayAddressGenerator {
		return &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"}
	}
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: realm,
		AuthHandler: func(username, r string, _ net.Addr) ([]byte, bool) {
			return turn.GenerateAuthKey(username, realm, pass), username == user
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: gen()}},
		ListenerConfigs: []turn.ListenerConfig{
			{Listener: tcp, RelayAddressGenerator: gen()},
			{Listener: tl, RelayAddressGenerator: gen()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return testTURN{srv}
}

// testTURNAllocate allocates over pc (UDP, to server) or c (TCP / TLS) and
// returns the client's Close.
func testTURNAllocate(t *testing.T, realm, user, pass string, pc net.PacketConn, c net.Conn, server string) func() {
	t.Helper()
	cfg := &turn.ClientConfig{Conn: pc, Username: user, Password: pass, Realm: realm, RTO: 200 * time.Millisecond}
	if pc != nil {
		cfg.TURNServerAddr = server
	} else {
		cfg.Conn = turn.NewSTUNConn(c)
	}
	cl, err := turn.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.Listen(); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Allocate(); err != nil {
		t.Fatal(err)
	}
	return cl.Close
}
