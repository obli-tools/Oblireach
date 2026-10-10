package server

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	sl "github.com/oblireach/supportlink"
)

func v2Header(cmd byte, fam byte, src net.IP, port uint16) []byte {
	var body []byte
	switch fam {
	case 0x11:
		body = append(append(append([]byte{}, src.To4()...), 192, 0, 2, 1), 0, 0, 0, 0)
		binary.BigEndian.PutUint16(body[8:], port)
		binary.BigEndian.PutUint16(body[10:], 443)
	case 0x21:
		body = make([]byte, 36)
		copy(body, src.To16())
		binary.BigEndian.PutUint16(body[32:], port)
	}
	h := append(append([]byte{}, proxyV2Sig...), 0x20|cmd, fam, 0, 0)
	binary.BigEndian.PutUint16(h[14:], uint16(len(body)))
	return append(h, body...)
}

func TestReadProxyHeader(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		want string // "" = keep the TCP peer, "err" = refused
	}{
		"v1 tcp4":      {"PROXY TCP4 198.51.100.7 192.0.2.1 51234 443\r\nGET", "198.51.100.7:51234"},
		"v1 tcp6":      {"PROXY TCP6 2001:db8::7 2001:db8::1 51234 443\r\n", "[2001:db8::7]:51234"},
		"v1 unknown":   {"PROXY UNKNOWN\r\n", ""},
		"v1 mismatch":  {"PROXY TCP4 2001:db8::7 192.0.2.1 1 443\r\n", "err"},
		"v1 no crlf":   {"PROXY TCP4 198.51.100.7 192.0.2.1 51234 443\n", "err"},
		"v1 too long":  {"PROXY TCP4 " + strings.Repeat("1", 120) + "\r\n", "err"},
		"no header":    {"GET / HTTP/1.1\r\nHost: x\r\n\r\n", "err"},
		"tls hello":    {"\x16\x03\x01\x02\x00\x01\x00\x01\xfc\x03\x03\x00", "err"},
		"v2 tcp4":      {string(v2Header(1, 0x11, net.ParseIP("203.0.113.5"), 4000)), "203.0.113.5:4000"},
		"v2 tcp6":      {string(v2Header(1, 0x21, net.ParseIP("2001:db8::5"), 4000)), "[2001:db8::5]:4000"},
		"v2 local":     {string(v2Header(0, 0x00, nil, 0)), ""},
		"v2 bad cmd":   {string(v2Header(7, 0x11, net.ParseIP("203.0.113.5"), 1)), "err"},
		"v2 truncated": {string(v2Header(1, 0x11, net.ParseIP("203.0.113.5"), 1))[:20], "err"},
	} {
		a, err := readProxyHeader(bufio.NewReader(strings.NewReader(c.in)))
		got := ""
		switch {
		case err != nil:
			got = "err"
		case a != nil:
			got = a.String()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// TestProxyProtocolListener: the server sees the client given by the
// proxy, bytes after the header are intact, and a connection without a
// header is dropped without blocking the others.
func TestProxyProtocolListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := ProxyProtocolListener(inner)
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.RemoteAddr)
	})}
	go hs.Serve(l)
	defer hs.Close()

	// A silent connection (no header yet) must not hold up the next ones.
	silent, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	get := func(header string) (string, error) {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			return "", err
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(c, header+"GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
			return "", err
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}
	if got, err := get("PROXY TCP4 198.51.100.7 192.0.2.1 51234 443\r\n"); err != nil || got != "198.51.100.7:51234" {
		t.Fatalf("v1: %q %v", got, err)
	}
	if got, err := get(string(v2Header(1, 0x11, net.ParseIP("203.0.113.5"), 4000))); err != nil || got != "203.0.113.5:4000" {
		t.Fatalf("v2: %q %v", got, err)
	}
	if got, err := get(string(v2Header(0, 0x00, nil, 0))); err != nil || !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("v2 local: %q %v", got, err)
	}
	if got, err := get(""); err == nil {
		t.Fatalf("no header served: %q", got)
	}
	l.Close()
	if _, err := l.Accept(); err == nil {
		t.Fatal("accept after close")
	}
}

// streamProxy is a TCP proxy that prepends a PROXY v1 header claiming the
// client is src (nginx "stream" with proxy_protocol on).
func streamProxy(t *testing.T, backend, src string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer b.Close()
				port := c.RemoteAddr().(*net.TCPAddr).Port
				if _, err := io.WriteString(b, "PROXY TCP4 "+src+" 127.0.0.1 "+itoa(port)+" 443\r\n"); err != nil {
					return
				}
				go func() { _, _ = io.Copy(b, c); b.Close() }()
				_, _ = io.Copy(c, b)
			}()
		}
	}()
	t.Cleanup(func() { l.Close() })
	return l
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestRelayTLSBehindProxyProtocol: signalling and TURN over TLS through an
// SNI-passthrough proxy; rdv logs the client address from the header.
func TestRelayTLSBehindProxyProtocol(t *testing.T) {
	inner, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	front := streamProxy(t, inner.Addr().String(), "198.51.100.9")
	e := newTLSEnvOn(t, ProxyProtocolListener(inner), front.Addr().(*net.TCPAddr).Port, nil)
	runPathEnv(t, e, tlsOnly(), tlsOnly(), sl.PathRelayTLS)
	if logs := e.logs.String(); !strings.Contains(logs, `"ip":"198.51.100.9"`) {
		t.Fatal("client address of the PROXY header not used")
	}
}
