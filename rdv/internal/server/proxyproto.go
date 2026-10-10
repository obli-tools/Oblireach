package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// PROXY protocol (HAProxy, v1 text and v2 binary) on the signalling port,
// for a TCP / SNI-passthrough proxy in front (nginx "stream" with
// proxy_protocol on): TLS, and TURN over TLS, then stay end to end, and
// the real client address still reaches rate limits, bans, logs and the
// TURN permissions. With RDV_PROXY_PROTOCOL on, every connection must start
// with the header: listen on an address only the proxy can reach.

const proxyHeaderTimeout = 10 * time.Second

var proxyV2Sig = []byte("\r\n\r\n\x00\r\nQUIT\n")

// ProxyProtocolListener wraps l: a background loop accepts connections
// and reads their PROXY header (concurrently, 10 s at most each); Accept
// returns connections whose RemoteAddr is the client given by the proxy.
// Connections without a valid header are closed.
func ProxyProtocolListener(l net.Listener) net.Listener {
	pl := &proxyListener{Listener: l, ready: make(chan net.Conn), done: make(chan struct{})}
	go pl.loop()
	return pl
}

type proxyListener struct {
	net.Listener
	ready chan net.Conn
	done  chan struct{} // closed when the inner Accept failed for good
	err   error         // set before done is closed
}

func (l *proxyListener) loop() {
	sem := make(chan struct{}, muxMaxPending)
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			l.err = err
			close(l.done)
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = c.Close() // too many connections without a header yet
			continue
		}
		go func() {
			defer func() { <-sem }()
			r := bufio.NewReaderSize(c, 256)
			_ = c.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
			src, err := readProxyHeader(r)
			_ = c.SetReadDeadline(time.Time{})
			if err != nil {
				_ = c.Close()
				return
			}
			pc := &proxyConn{Conn: c, r: r, src: src}
			select {
			case l.ready <- pc:
			case <-l.done:
				_ = c.Close()
			}
		}()
	}
}

func (l *proxyListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ready:
		return c, nil
	case <-l.done:
		return nil, l.err
	}
}

type proxyConn struct {
	net.Conn
	r   *bufio.Reader
	src net.Addr
}

func (c *proxyConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// RemoteAddr is the client address given by the proxy (the proxy's own
// address for a LOCAL / UNKNOWN header, e.g. its health checks).
func (c *proxyConn) RemoteAddr() net.Addr {
	if c.src != nil {
		return c.src
	}
	return c.Conn.RemoteAddr()
}

var errProxyHeader = errors.New("invalid or missing PROXY protocol header")

// readProxyHeader reads a v1 or v2 header; it returns a nil address for
// LOCAL / UNKNOWN (keep the TCP peer).
func readProxyHeader(r *bufio.Reader) (net.Addr, error) {
	sig, err := r.Peek(12)
	if err != nil {
		return nil, errProxyHeader
	}
	if bytes.Equal(sig, proxyV2Sig) {
		return readProxyV2(r)
	}
	if !bytes.HasPrefix(sig, []byte("PROXY ")) {
		return nil, errProxyHeader
	}
	// v1: one line of at most 107 bytes, CRLF terminated.
	var line []byte
	for len(line) < 108 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, errProxyHeader
		}
		line = append(line, b)
		if b == '\n' {
			break
		}
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return nil, errProxyHeader
	}
	f := strings.Fields(string(line[:len(line)-2]))
	if len(f) >= 2 && f[1] == "UNKNOWN" {
		return nil, nil
	}
	if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
		return nil, errProxyHeader
	}
	ip := net.ParseIP(f[2])
	port, err := strconv.Atoi(f[4])
	if ip == nil || err != nil || port < 0 || port > 65535 || (f[1] == "TCP4") != (ip.To4() != nil) {
		return nil, errProxyHeader
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}

func readProxyV2(r *bufio.Reader) (net.Addr, error) {
	var h [16]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, errProxyHeader
	}
	verCmd, fam := h[12], h[13]
	n := int(binary.BigEndian.Uint16(h[14:16]))
	if verCmd>>4 != 2 || n > 512 {
		return nil, errProxyHeader
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, errProxyHeader
	}
	switch verCmd & 0x0F {
	case 0x0: // LOCAL: the proxy's own connection
		return nil, nil
	case 0x1: // PROXY
	default:
		return nil, errProxyHeader
	}
	switch fam {
	case 0x11: // TCP over IPv4
		if n < 12 {
			return nil, errProxyHeader
		}
		return &net.TCPAddr{IP: net.IP(append([]byte(nil), body[0:4]...)), Port: int(binary.BigEndian.Uint16(body[8:10]))}, nil
	case 0x21: // TCP over IPv6
		if n < 36 {
			return nil, errProxyHeader
		}
		return &net.TCPAddr{IP: net.IP(append([]byte(nil), body[0:16]...)), Port: int(binary.BigEndian.Uint16(body[32:34]))}, nil
	case 0x00: // UNSPEC
		return nil, nil
	}
	return nil, errProxyHeader
}
