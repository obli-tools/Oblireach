package supportlink

import (
	"bytes"
	"net"
	"testing"
	"time"
)

var loopbackICE = ICEOptions{IncludeLoopback: true, IPFilter: func(ip net.IP) bool { return ip.IsLoopback() }}

// TestPionLoopback connects two real PeerConnections without a server
// (direct host candidates on loopback) and pushes frames both ways through
// the Session framing over SCTP.
func TestPionLoopback(t *testing.T) {
	hp, err := newRTCPeer(nil, loopbackICE, SignalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	op, err := newRTCPeer(nil, loopbackICE, SignalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	keys := sessionKeys{sas: "000 000"}
	hs := newSession("sid", keys, PeerInfo{}, hp)
	os := newSession("sid", keys, PeerInfo{}, op)
	defer hs.Close()
	defer os.Close()

	hp.onCandidate(func(c string) { _ = op.addCandidate(c) })
	start := time.Now()
	offer, err := hp.createOffer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprints(offer); err != nil {
		t.Fatal(err)
	}
	// Candidates of the answerer are added once the offer is set.
	op.onCandidate(func(c string) { _ = hp.addCandidate(c) })
	answer, err := op.acceptOffer(offer)
	if err != nil {
		t.Fatal(err)
	}
	if err := hp.setAnswer(answer); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Session{hs, os} {
		if err := s.waitOpen(10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("loopback setup %v, path %s (%s)", time.Since(start).Round(time.Millisecond), hs.Path(), hs.PathDetail())
	if hs.Path() != PathDirect {
		t.Fatalf("path = %s", hs.Path())
	}

	big := bytes.Repeat([]byte{0x5A}, 3<<20)
	t0 := time.Now()
	go func() {
		_ = hs.WriteFrame(OpText, []byte(`{"type":"init"}`))
		_ = hs.WriteFrame(OpBinary, big)
	}()
	for i := 0; i < 2; i++ {
		opc, p, err := os.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if opc == OpBinary && !bytes.Equal(p, big) {
			t.Fatal("binary frame corrupted")
		}
	}
	el := time.Since(t0)
	t.Logf("3 MiB frame in %v (%.0f Mbit/s)", el.Round(time.Millisecond), float64(len(big))*8/el.Seconds()/1e6)

	if err := os.WriteFrame(OpText, []byte("pong")); err != nil {
		t.Fatal(err)
	}
	if _, p, err := hs.ReadFrame(); err != nil || string(p) != "pong" {
		t.Fatalf("reverse: %q %v", p, err)
	}
	hs.Close()
	select {
	case <-os.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("remote close not detected")
	}
}

// TestPionInterleavedOrder runs the interleaving check over real SCTP.
func TestPionInterleavedOrder(t *testing.T) {
	hp, err := newRTCPeer(nil, loopbackICE, SignalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	op, err := newRTCPeer(nil, loopbackICE, SignalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	keys := sessionKeys{sas: "000 000"}
	hs := newSession("sid", keys, PeerInfo{}, hp)
	os := newSession("sid", keys, PeerInfo{}, op)
	defer hs.Close()
	defer os.Close()
	hp.onCandidate(func(c string) { _ = op.addCandidate(c) })
	offer, err := hp.createOffer()
	if err != nil {
		t.Fatal(err)
	}
	op.onCandidate(func(c string) { _ = hp.addCandidate(c) })
	answer, err := op.acceptOffer(offer)
	if err != nil {
		t.Fatal(err)
	}
	if err := hp.setAnswer(answer); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Session{hs, os} {
		if err := s.waitOpen(10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(checkInterleaved(t, hs, os, 600, 7))
}
