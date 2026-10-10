package server

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestVersionLess(t *testing.T) {
	for _, c := range []struct {
		v, min string
		less   bool
	}{
		{"1.2.10", "1.10.0", true}, {"1.10.0", "1.2.10", false}, {"1.2.0", "1.2", false},
		{"", "1.0.0", true}, {"0.9", "", false}, {"v1.3.0", "1.2.9", false},
	} {
		if got := versionLess(c.v, c.min); got != c.less {
			t.Errorf("versionLess(%q, %q) = %v", c.v, c.min, got)
		}
	}
}

func TestWorsePath(t *testing.T) {
	p := ""
	for _, r := range []string{"direct", "relay_udp", "direct", "bogus"} {
		p = worsePath(p, r)
	}
	if p != "relay_udp" {
		t.Fatalf("got %q", p)
	}
	if p = worsePath(worsePath(p, "relay_tls"), "relay_tcp"); p != "relay_tls" {
		t.Fatalf("got %q", p)
	}
}

func TestBucketsAndLocks(t *testing.T) {
	now := time.Now()
	b := newBucketSet(60, 3) // 1/s, burst 3
	for i := 0; i < 3; i++ {
		if !b.take("k", now) {
			t.Fatal("burst refused")
		}
	}
	if b.take("k", now) {
		t.Fatal("over burst accepted")
	}
	if !b.take("k", now.Add(1100*time.Millisecond)) {
		t.Fatal("refill not applied")
	}
	b.gc(now.Add(time.Hour))
	if len(b.m) != 0 {
		t.Fatal("refilled bucket not collected")
	}

	f := newFailLog(10 * time.Minute)
	for i := 0; i < 4; i++ {
		f.add("id", now)
	}
	if n := f.add("id", now.Add(11*time.Minute)); n != 1 {
		t.Fatalf("window not sliding: %d", n)
	}

	d := deadlines{"x": now.Add(time.Minute)}
	if _, ok := d.active("x", now); !ok {
		t.Fatal("lock not active")
	}
	if _, ok := d.active("x", now.Add(2*time.Minute)); ok {
		t.Fatal("lock not expired")
	}
}

func TestByteLimiter(t *testing.T) {
	l := newByteLimiter(8000) // 1 MB/s, burst 256 KB
	sent := 0
	for l.allow(1200) {
		sent += 1200
	}
	if sent < 200<<10 || sent > 300<<10 {
		t.Fatalf("burst %d bytes", sent)
	}
	if newByteLimiter(0) != nil || !(*byteLimiter)(nil).allow(1<<30) {
		t.Fatal("0 must mean no cap")
	}
}

func TestPermission(t *testing.T) {
	ts := &turnServer{s: &Server{cfg: Config{PublicIP: net.ParseIP("203.0.113.10")}}}
	for ip, want := range map[string]bool{
		"203.0.113.10": true, "198.51.100.7": true,
		"10.1.2.3": false, "192.168.0.1": false, "127.0.0.1": false, "169.254.169.254": false,
		"0.0.0.0": false, "224.0.0.1": false, "fd00::1": false, "::1": false,
	} {
		if got := ts.permission(nil, net.ParseIP(ip)); got != want {
			t.Errorf("permission(%s) = %v", ip, got)
		}
	}
}

func TestLogIPModes(t *testing.T) {
	if got := newEventLog(nil, IPModeTruncated).ip("203.0.113.77"); got != "203.0.113.0/24" {
		t.Errorf("truncated v4: %s", got)
	}
	if got := newEventLog(nil, IPModeTruncated).ip("2001:db8:1:2::5"); got != "2001:db8:1::/48" {
		t.Errorf("truncated v6: %s", got)
	}
	h := newEventLog(nil, IPModeHMAC)
	a, b := h.ip("203.0.113.77"), h.ip("203.0.113.77")
	if a != b || !strings.HasPrefix(a, "h:") || strings.Contains(a, "203") {
		t.Errorf("hmac: %s %s", a, b)
	}
}

func TestOperatorAccount(t *testing.T) {
	s, err := New(Config{
		PublicIP: net.ParseIP("127.0.0.1"), RelayMinPort: 50000, RelayMaxPort: 50010,
		TURNSecret:   "0123456789abcdef",
		OperatorKeys: []OperatorKey{{Label: "a", Secret: "aaaaaaaaaaaaaaaa"}, {Secret: "bbbbbbbbbbbbbbbb"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if acc, ok := s.operatorAccount("bbbbbbbbbbbbbbbb"); !ok || acc != "key-2" {
		t.Fatalf("got %q %v", acc, ok)
	}
	if _, ok := s.operatorAccount(""); ok {
		t.Fatal("empty key accepted")
	}
	if _, ok := s.operatorAccount("aaaaaaaaaaaaaaab"); ok {
		t.Fatal("wrong key accepted")
	}
}
