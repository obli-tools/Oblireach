package supportlink

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oblireach/supportlink/wire"
)

// Signalling keepalive: the server pings every 25 s; a connection silent for
// longer than sigReadTimeout is considered dead (NAT or proxy dropped it).
const (
	sigReadTimeout  = 60 * time.Second
	sigWriteTimeout = 10 * time.Second
	sigReadLimit    = 64 << 10 // SDP + candidates only: no content ever transits here
)

// SignalOptions are the connection settings shared by host and operator.
type SignalOptions struct {
	// TLSConfig for wss:// (nil = system roots). Pinning can be added here.
	TLSConfig *tls.Config
	// AllowPlainWS permits ws:// to a non-loopback server. Off by default:
	// signalling content is end-to-end protected, but the operator key and
	// metadata are not.
	AllowPlainWS bool
	// Header is sent with the WebSocket upgrade (e.g. User-Agent).
	Header http.Header
	// Proxy is the HTTP proxy for the signalling WebSocket and for TURN
	// over TCP and TLS (HTTP CONNECT): ProxyAuto ("", the default:
	// HTTPS_PROXY / NO_PROXY, else the Windows settings of the user,
	// static or PAC), ProxyDirect ("direct"), or a URL
	// "http://[user:password@]host:port" (Basic authentication only). UDP
	// never goes through a proxy: behind one, the relay is reached over
	// TURN-TLS on 443.
	Proxy string
}

// sigConn is a signalling WebSocket with a serialised writer and a read
// channel closed when the connection ends.
type sigConn struct {
	c   *websocket.Conn
	wmu sync.Mutex
	in  chan wire.Msg

	closeOnce sync.Once
}

// signalURL normalises the server setting: "rdv.example.com",
// "wss://rdv.example.com" and "wss://rdv.example.com/v1/ws" are equivalent.
func signalURL(server string, allowPlain bool) (string, error) {
	s := strings.TrimSpace(server)
	if s == "" {
		return "", fmt.Errorf("%w: no rendezvous server configured", ErrInvalidServer)
	}
	if !strings.Contains(s, "://") {
		s = "wss://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%w: bad server URL: %w", ErrInvalidServer, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return "", fmt.Errorf("%w: unsupported scheme %q", ErrInvalidServer, u.Scheme)
	}
	if u.Scheme == "ws" && !allowPlain && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("%w: refusing plain ws:// to a remote server (use wss://)", ErrInvalidServer)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = wire.Path
	}
	return u.String(), nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func dialSignal(ctx context.Context, server string, o SignalOptions, hello wire.Msg) (*sigConn, error) {
	u, err := signalURL(server, o.AllowPlainWS)
	if err != nil {
		return nil, err
	}
	proxy, err := resolveProxy(o.Proxy)
	if err != nil {
		return nil, err
	}
	scheme := "https"
	if strings.HasPrefix(u, "ws:") {
		scheme = "http"
	}
	d := websocket.Dialer{
		// The proxy is applied by the dialer (CONNECT), the same code as
		// for TURN over TCP/TLS.
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialVia(ctx, proxy, network, proxyTarget(scheme, addr), addr)
		},
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig:  o.TLSConfig,
	}
	c, resp, err := d.DialContext(ctx, u, o.Header)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	c.SetReadLimit(sigReadLimit)
	s := &sigConn{c: c, in: make(chan wire.Msg, 64)}
	_ = c.SetReadDeadline(time.Now().Add(sigReadTimeout))
	c.SetPingHandler(func(data string) error {
		_ = c.SetReadDeadline(time.Now().Add(sigReadTimeout))
		// A failed pong surfaces as a read error soon enough; never abort
		// the read loop from the handler.
		_ = c.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(sigWriteTimeout))
		return nil
	})
	go s.readLoop()
	hello.T = wire.THello
	hello.Proto = wire.ProtocolVersion
	if err := s.send(hello); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *sigConn) readLoop() {
	defer close(s.in)
	for {
		var m wire.Msg
		if err := s.c.ReadJSON(&m); err != nil {
			return
		}
		_ = s.c.SetReadDeadline(time.Now().Add(sigReadTimeout))
		s.in <- m
	}
}

func (s *sigConn) send(m wire.Msg) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.c.SetWriteDeadline(time.Now().Add(sigWriteTimeout))
	if err := s.c.WriteJSON(m); err != nil {
		return fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return nil
}

func (s *sigConn) close() {
	s.closeOnce.Do(func() {
		s.wmu.Lock()
		_ = s.c.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		s.wmu.Unlock()
		_ = s.c.Close()
	})
}
