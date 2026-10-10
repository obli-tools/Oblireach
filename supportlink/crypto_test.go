package supportlink

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testSDP = "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n" +
	"a=fingerprint:sha-256 AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99\r\n" +
	"m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"

func pake(t *testing.T, hostCode, opCode string) (host, op sessionKeys) {
	t.Helper()
	const id, sid = "482913507", "0123456789abcdef"
	msgA, st, err := operatorPakeStart(opCode, id, sid)
	if err != nil {
		t.Fatal(err)
	}
	msgB, kh, err := hostPakeExchange(hostCode, id, sid, msgA)
	if err != nil {
		t.Fatal(err)
	}
	ko, err := st.Finish(msgB)
	if err != nil {
		t.Fatal(err)
	}
	return deriveKeys(kh), deriveKeys(ko)
}

func TestPakeRightCode(t *testing.T) {
	h, o := pake(t, "123456789", "123456789")
	if !bytes.Equal(h.boxHost, o.boxHost) || h.sas != o.sas {
		t.Fatal("same code must give the same keys and SAS")
	}
	if !checkMAC(o.confirmHost, hostConfirmData("0123456789abcdef"), macB64(h.confirmHost, hostConfirmData("0123456789abcdef"))) {
		t.Fatal("host confirmation must verify")
	}
	if len(h.sas) != 7 || h.sas[3] != ' ' {
		t.Fatalf("SAS format %q", h.sas)
	}
	// Different labels must give independent keys.
	if bytes.Equal(h.boxHost, h.boxOp) || bytes.Equal(h.fpHost, h.fpOp) || bytes.Equal(h.confirmHost, h.confirmOp) {
		t.Fatal("per-direction keys must differ")
	}
}

func TestPakeWrongCode(t *testing.T) {
	h, o := pake(t, "123456789", "123456780")
	sid := "0123456789abcdef"
	if checkMAC(o.confirmHost, hostConfirmData(sid), macB64(h.confirmHost, hostConfirmData(sid))) {
		t.Fatal("a wrong code must fail the key confirmation")
	}
}

func TestPakeContextBinding(t *testing.T) {
	msgA, _, err := operatorPakeStart("123456789", "482913507", "sid-one")
	if err != nil {
		t.Fatal(err)
	}
	// Replaying the message into another session derives another key.
	_, k1, _ := hostPakeExchange("123456789", "482913507", "sid-one", msgA)
	_, k2, _ := hostPakeExchange("123456789", "482913507", "sid-two", msgA)
	if bytes.Equal(k1, k2) {
		t.Fatal("PAKE output must depend on the session id")
	}
}

func TestSealOpen(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	box, err := seal(key, "sid", "cand", []byte("candidate:1 1 udp 1 127.0.0.1 5000 typ host"))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := open(key, "sid", "cand", box); err != nil || !strings.HasPrefix(string(p), "candidate:") {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err := open(key, "sid2", "cand", box); err == nil {
		t.Fatal("a box moved to another session must fail")
	}
	if _, err := open(key, "sid", "offer", box); err == nil {
		t.Fatal("a box moved to another message type must fail")
	}
	raw := []byte(box)
	raw[len(raw)/2] ^= 1
	if _, err := open(key, "sid", "cand", string(raw)); err == nil {
		t.Fatal("a modified box must fail")
	}
}

func TestSDPBinding(t *testing.T) {
	h, o := pake(t, "111222333", "111222333")
	sid := "0123456789abcdef"
	box, mac, err := sealSDP(h.boxHost, h.fpHost, sid, "offer", testSDP)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openSDP(o.boxHost, o.fpHost, sid, "offer", box, mac); err != nil || got != testSDP {
		t.Fatalf("openSDP: %v", err)
	}
	// A server that cannot open the box can only replace it.
	if _, err := openSDP(o.boxHost, o.fpHost, sid, "offer", strings.Repeat("A", 80), mac); !errors.Is(err, ErrMITM) {
		t.Fatalf("replaced box: want ErrMITM, got %v", err)
	}
	// Even inside an authentic channel, a substituted fingerprint is caught.
	swapped := strings.Replace(testSDP, "AA:BB:CC", "AB:AB:AB", 1)
	if _, err := verifyFingerprints(o.fpHost, sid, swapped, mac); !errors.Is(err, ErrMITM) {
		t.Fatalf("swapped fingerprint: want ErrMITM, got %v", err)
	}
	// The operator's key cannot verify the host's fingerprint MAC.
	if _, err := verifyFingerprints(o.fpOp, sid, testSDP, mac); !errors.Is(err, ErrMITM) {
		t.Fatal("fingerprint MAC must be direction-bound")
	}
}

func TestFingerprints(t *testing.T) {
	a, err := fingerprints("a=fingerprint:SHA-256 aa:bb\na=fingerprint:sha-256 AA:BB\r\na=fingerprint:sha-1 cc\n")
	if err != nil || a != "sha-1 CC,sha-256 AA:BB" {
		t.Fatalf("got %q %v", a, err)
	}
	if _, err := fingerprints("v=0\n"); err == nil {
		t.Fatal("an SDP without fingerprint must be refused")
	}
}

func TestCleanName(t *testing.T) {
	if got := cleanName("  Jane‮Doe\x07 "); got != "JaneDoe" {
		t.Fatalf("got %q", got)
	}
	if got := cleanName(strings.Repeat("x", 100)); len(got) != 64 {
		t.Fatalf("len %d", len(got))
	}
}
