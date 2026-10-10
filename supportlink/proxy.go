package supportlink

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Proxy settings (SignalOptions.Proxy).
const (
	// ProxyAuto: HTTPS_PROXY / HTTP_PROXY / NO_PROXY from the environment,
	// else (Windows) the system settings of the user: WinHTTP / Internet
	// Options, static proxy or PAC / WPAD. The default.
	ProxyAuto = ""
	// ProxyDirect never uses a proxy.
	ProxyDirect = "direct"
)

// ErrProxy wraps the failures of the HTTP proxy itself (refused CONNECT,
// authentication required), as opposed to the rendezvous server being
// unreachable.
var ErrProxy = errors.New("supportlink: HTTP proxy error")

// proxyDialTimeout bounds a connection through the proxy (TCP + CONNECT).
const proxyDialTimeout = 15 * time.Second

// proxyFunc returns the proxy to use for a target URL (nil = direct).
type proxyFunc func(target *url.URL) (*url.URL, error)

// resolveProxy turns the Proxy setting into a proxyFunc.
func resolveProxy(setting string) (proxyFunc, error) {
	s := strings.TrimSpace(setting)
	switch strings.ToLower(s) {
	case ProxyDirect, "none", "off":
		return func(*url.URL) (*url.URL, error) { return nil, nil }, nil
	case ProxyAuto, "auto", "system":
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
			if os.Getenv(k) != "" {
				// Standard semantics: NO_PROXY honoured, loopback never proxied.
				return func(u *url.URL) (*url.URL, error) {
					return http.ProxyFromEnvironment(&http.Request{URL: u})
				}, nil
			}
		}
		return systemProxy, nil
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: bad proxy address %q", ErrInvalidServer, setting)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: unsupported proxy scheme %q (http or https)", ErrInvalidServer, u.Scheme)
	}
	// An explicit proxy applies to every destination, loopback included.
	return func(*url.URL) (*url.URL, error) { return u, nil }, nil
}

// dialVia opens a TCP connection to addr ("host:port"), through the proxy
// when there is one (HTTP CONNECT). target selects the proxy.
func dialVia(ctx context.Context, proxy proxyFunc, network string, target *url.URL, addr string) (net.Conn, error) {
	var p *url.URL
	if proxy != nil {
		var err error
		if p, err = proxy(target); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrProxy, err)
		}
	}
	d := net.Dialer{Timeout: proxyDialTimeout, KeepAlive: 30 * time.Second}
	if p == nil {
		return d.DialContext(ctx, network, addr)
	}
	return dialConnect(ctx, &d, p, addr)
}

// dialConnect opens a tunnel to addr through an HTTP(S) proxy.
func dialConnect(ctx context.Context, d *net.Dialer, p *url.URL, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, proxyDialTimeout)
	defer cancel()
	pa := p.Host
	if p.Port() == "" {
		if p.Scheme == "https" {
			pa = net.JoinHostPort(p.Hostname(), "443")
		} else {
			pa = net.JoinHostPort(p.Hostname(), "80")
		}
	}
	c, err := d.DialContext(ctx, "tcp", pa)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProxy, err)
	}
	if p.Scheme == "https" {
		tc := tls.Client(c, &tls.Config{ServerName: p.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, fmt.Errorf("%w: %w", ErrProxy, err)
		}
		c = tc
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = c.SetDeadline(time.Unix(1, 0)) // unblock the exchange below
		case <-done:
		}
	}()
	conn, err := connectExchange(c, p, addr)
	close(done)
	<-stopped
	if err == nil && ctx.Err() != nil {
		conn.Close()
		err = fmt.Errorf("%w: %w", ErrProxy, ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func connectExchange(c net.Conn, p *url.URL, addr string) (net.Conn, error) {

	req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if p.User != nil {
		pw, _ := p.User.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(p.User.Username()+":"+pw)) + "\r\n"
	}
	req += "\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		c.Close()
		return nil, fmt.Errorf("%w: %w", ErrProxy, err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("%w: %w", ErrProxy, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		if resp.StatusCode == http.StatusProxyAuthRequired {
			return nil, fmt.Errorf("%w: proxy authentication required (%s; only Basic credentials in the proxy URL are supported)",
				ErrProxy, resp.Header.Get("Proxy-Authenticate"))
		}
		return nil, fmt.Errorf("%w: CONNECT %s: %s", ErrProxy, addr, resp.Status)
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// bufferedConn returns bytes the proxy sent right after its answer first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// proxyTarget is the URL a proxy is selected for. CONNECT is the same for
// every scheme; https keeps HTTPS_PROXY in charge of TURN over TCP and TLS.
func proxyTarget(scheme, addr string) *url.URL {
	return &url.URL{Scheme: scheme, Host: addr}
}
