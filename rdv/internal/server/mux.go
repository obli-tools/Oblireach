package server

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// TURN over TLS shares the signalling port: networks that only
// let 443/TCP out still reach the relay. ServeTLS terminates TLS itself and
// routes each connection after the handshake:
//
//   - ALPN "stun.turn" (RFC 7443), or a first message that is a STUN header
//     (top two bits 00, magic cookie 0x2112A442 at offset 4): TURN;
//   - ALPN "acme-tls/1": the TLS-ALPN-01 challenge, done by the handshake;
//   - anything else starting with an ASCII letter (an HTTP method): HTTPS.
//
// The certificate is the signalling one (ACME included). A TURN client
// always starts with an Allocate request, so ChannelData (first byte 0x40 -
// 0x7F) never opens a connection.

const (
	alpnTURN = "stun.turn"
	alpnACME = "acme-tls/1"
	alpnHTTP = "http/1.1"

	muxHandshakeTimeout = 10 * time.Second
	muxMaxPending       = 2048 // connections between accept and routing
)

var stunMagic = []byte{0x21, 0x12, 0xA4, 0x42}

// muxALPN is the ALPN list of the shared port: HTTP/1.1 first (the
// signalling WebSocket is HTTP/1.1; h2 is dropped since the HTTP side gets
// plain connections), then TURN, then the ACME challenge when present.
func muxALPN(in []string) []string {
	out := []string{alpnHTTP, alpnTURN}
	for _, p := range in {
		if p == alpnACME {
			out = append(out, alpnACME)
		}
	}
	return out
}

// ServeTLS serves the signalling over TLS on l and, when TURN-TLS is
// configured, hands the TURN connections to the TURN server. It returns
// http.ErrServerClosed after hs.Shutdown or hs.Close.
func (s *Server) ServeTLS(hs *http.Server, l net.Listener, tc *tls.Config) error {
	cfg := tc.Clone()
	cfg.NextProtos = muxALPN(tc.NextProtos)
	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS12
	}
	httpL := newConnQueue(l.Addr(), func() { _ = l.Close() })
	served := make(chan error, 1)
	go func() { served <- hs.Serve(httpL) }()

	sem := make(chan struct{}, muxMaxPending)
	var delay time.Duration
	for {
		c, err := l.Accept()
		if err != nil {
			if httpL.isClosed() || errors.Is(err, net.ErrClosed) {
				httpL.Close()
				if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
					return err
				}
				return http.ErrServerClosed
			}
			// Out of file descriptors and the like: back off like net/http.
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay *= 2; delay > time.Second {
				delay = time.Second
			}
			s.log.warn("accept_error", "err", err.Error())
			time.Sleep(delay)
			continue
		}
		delay = 0
		select {
		case sem <- struct{}{}:
		default:
			s.m.muxDropped.Add(1)
			_ = c.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			s.route(c, cfg, httpL)
		}()
	}
}

// route completes the TLS handshake and hands the connection to HTTP or
// TURN.
func (s *Server) route(raw net.Conn, cfg *tls.Config, httpL *connQueue) {
	_ = raw.SetDeadline(time.Now().Add(muxHandshakeTimeout))
	tc := tls.Server(raw, cfg)
	if err := tc.Handshake(); err != nil {
		_ = raw.Close()
		return
	}
	alpn := tc.ConnectionState().NegotiatedProtocol
	if alpn == alpnACME {
		_ = tc.Close() // the challenge is answered by the certificate itself
		return
	}
	br := bufio.NewReaderSize(tc, 1024)
	turnConn := alpn == alpnTURN
	if !turnConn {
		b, err := br.Peek(1)
		if err != nil {
			_ = tc.Close()
			return
		}
		switch c := b[0]; {
		case c < 0x40:
			h, err := br.Peek(8)
			if err != nil || !bytes.Equal(h[4:8], stunMagic) {
				_ = tc.Close()
				return
			}
			turnConn = true
		case c >= 'A' && c <= 'Z':
		default:
			_ = tc.Close()
			return
		}
	}
	_ = raw.SetDeadline(time.Time{})
	pc := &peekedConn{Conn: tc, r: br}
	if !turnConn {
		if !httpL.push(pc) {
			_ = tc.Close()
		}
		return
	}
	if s.turnsL == nil || s.turn == nil {
		_ = tc.Close()
		return
	}
	key := raw.RemoteAddr().String()
	s.turn.markTLS(key, true)
	pc.onClose = func() { s.turn.markTLS(key, false) }
	if !s.turnsL.push(pc) {
		_ = pc.Close()
	}
}

// peekedConn replays the bytes read while routing.
type peekedConn struct {
	net.Conn
	r       *bufio.Reader
	once    sync.Once
	onClose func()
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *peekedConn) Close() error {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.Conn.Close()
}

// connQueue is a net.Listener fed by ServeTLS.
type connQueue struct {
	addr    net.Addr
	ch      chan net.Conn
	done    chan struct{}
	once    sync.Once
	onClose func()
}

func newConnQueue(addr net.Addr, onClose func()) *connQueue {
	return &connQueue{addr: addr, ch: make(chan net.Conn), done: make(chan struct{}), onClose: onClose}
}

// push hands c to Accept; false once the queue is closed.
func (q *connQueue) push(c net.Conn) bool {
	select {
	case q.ch <- c:
		return true
	case <-q.done:
		return false
	}
}

func (q *connQueue) Accept() (net.Conn, error) {
	select {
	case c := <-q.ch:
		return c, nil
	case <-q.done:
		return nil, net.ErrClosed
	}
}

func (q *connQueue) Close() error {
	q.once.Do(func() {
		close(q.done)
		if q.onClose != nil {
			q.onClose()
		}
	})
	return nil
}

func (q *connQueue) isClosed() bool {
	select {
	case <-q.done:
		return true
	default:
		return false
	}
}

func (q *connQueue) Addr() net.Addr { return q.addr }
