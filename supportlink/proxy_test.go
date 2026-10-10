package supportlink

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oblireach/supportlink/wire"
)

// connectProxy is a minimal HTTP CONNECT proxy for tests. It records the
// requested targets and can require Basic credentials.
type connectProxy struct {
	l    net.Listener
	auth string // "user:pass", or "" for none

	mu      sync.Mutex
	targets []string
}

func newConnectProxy(t *testing.T, auth string) *connectProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &connectProxy{l: l, auth: auth}
	go p.serve()
	t.Cleanup(func() { l.Close() })
	return p
}

func (p *connectProxy) url() string { return "http://" + p.l.Addr().String() }

func (p *connectProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func (p *connectProxy) serve() {
	for {
		c, err := p.l.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *connectProxy) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
		return
	}
	if p.auth != "" && req.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(p.auth)) {
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"test\"\r\n\r\n")
		return
	}
	p.mu.Lock()
	p.targets = append(p.targets, req.Host)
	p.mu.Unlock()
	up, err := net.Dial("tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}

func TestResolveProxy(t *testing.T) {
	target := &url.URL{Scheme: "https", Host: "rdv.example.com:443"}
	for _, s := range []string{"direct", "none", "OFF"} {
		f, err := resolveProxy(s)
		if err != nil {
			t.Fatal(err)
		}
		if p, _ := f(target); p != nil {
			t.Fatalf("%q: proxy %v", s, p)
		}
	}
	f, err := resolveProxy("proxy.corp:3128")
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := f(&url.URL{Scheme: "https", Host: "127.0.0.1:443"}); p == nil || p.String() != "http://proxy.corp:3128" {
		t.Fatalf("explicit proxy: %v (loopback included)", p)
	}
	for _, bad := range []string{"socks5://p:1080", "http://"} {
		if _, err := resolveProxy(bad); !errors.Is(err, ErrInvalidServer) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestResolveProxyEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://envproxy:8080")
	t.Setenv("NO_PROXY", "skip.example.com")
	f, err := resolveProxy(ProxyAuto)
	if err != nil {
		t.Fatal(err)
	}
	// net/http caches the environment once per process: only check that
	// the environment path is taken and answers without error.
	if _, err := f(&url.URL{Scheme: "https", Host: "rdv.example.com:443"}); err != nil {
		t.Fatal(err)
	}
}

func TestDialConnect(t *testing.T) {
	echo := echoServer(t)
	p := newConnectProxy(t, "alice:s3cret")
	pu, _ := url.Parse(p.url())
	pu.User = url.UserPassword("alice", "s3cret")
	f, _ := resolveProxy(pu.String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialVia(ctx, f, "tcp", proxyTarget("https", echo), echo)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo through the proxy: %q %v", buf, err)
	}
	if got := p.seen(); len(got) != 1 || got[0] != echo {
		t.Fatalf("proxy targets %v", got)
	}

	// Wrong credentials: a proxy error, not a server one.
	f, _ = resolveProxy(p.url())
	if _, err := dialVia(ctx, f, "tcp", proxyTarget("https", echo), echo); !errors.Is(err, ErrProxy) || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("407: %v", err)
	}
	// Proxy down.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String()
	l.Close()
	f, _ = resolveProxy(dead)
	if _, err := dialVia(ctx, f, "tcp", proxyTarget("https", echo), echo); !errors.Is(err, ErrProxy) {
		t.Fatalf("dead proxy: %v", err)
	}
}

// TestSignalThroughProxy: the signalling WebSocket honours an explicit
// proxy, and a refused proxy surfaces as ErrUnreachable + ErrProxy.
func TestSignalThroughProxy(t *testing.T) {
	p := newConnectProxy(t, "")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	})}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = dialSignal(ctx, "ws://"+l.Addr().String(), SignalOptions{Proxy: p.url()}, wire.Msg{Role: wire.RoleHost})
	if err == nil || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("handshake refused by the server: %v", err)
	}
	if got := p.seen(); len(got) != 1 || got[0] != l.Addr().String() {
		t.Fatalf("proxy targets %v", got)
	}
	_, err = dialSignal(ctx, "ws://"+l.Addr().String(), SignalOptions{Proxy: "http://127.0.0.1:1"}, wire.Msg{Role: wire.RoleHost})
	if !errors.Is(err, ErrUnreachable) || !errors.Is(err, ErrProxy) {
		t.Fatalf("dead proxy: %v", err)
	}
}
