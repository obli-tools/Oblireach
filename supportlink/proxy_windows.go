//go:build windows

package supportlink

import (
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// systemProxy reads the proxy of the current user the way WinHTTP does:
// the Internet Options (static proxy and bypass list, or a PAC script / WPAD
// auto-detection, evaluated by WinHTTP). Errors and timeouts mean direct.

var (
	modWinHTTP              = windows.NewLazySystemDLL("winhttp.dll")
	procGetIEProxyConfig    = modWinHTTP.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procWinHTTPOpen         = modWinHTTP.NewProc("WinHttpOpen")
	procWinHTTPGetProxyForU = modWinHTTP.NewProc("WinHttpGetProxyForUrl")
	procWinHTTPCloseHandle  = modWinHTTP.NewProc("WinHttpCloseHandle")
	procGlobalFree          = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

// WINHTTP_CURRENT_USER_IE_PROXY_CONFIG
type winhttpIEProxyConfig struct {
	autoDetect    int32
	autoConfigURL *uint16
	proxy         *uint16
	proxyBypass   *uint16
}

// WINHTTP_AUTOPROXY_OPTIONS
type winhttpAutoProxyOptions struct {
	flags                 uint32
	autoDetectFlags       uint32
	autoConfigURL         *uint16
	reserved1             uintptr
	reserved2             uint32
	autoLogonIfChallenged int32
}

// WINHTTP_PROXY_INFO
type winhttpProxyInfo struct {
	accessType  uint32
	proxy       *uint16
	proxyBypass *uint16
}

const (
	winhttpAutoproxyAutoDetect = 0x1
	winhttpAutoproxyConfigURL  = 0x2
	winhttpAutoDetectDHCP      = 0x1
	winhttpAutoDetectDNSA      = 0x2
	winhttpAccessNoProxy       = 1
	winhttpAccessNamedProxy    = 3

	// PAC / WPAD evaluation can be slow (DHCP, DNS, download).
	pacTimeout = 5 * time.Second
)

type ieConfig struct {
	autoDetect bool
	pacURL     string
	proxy      string
	bypass     string
}

// PAC answers are cached per destination: one session dials the signalling
// and up to two TURN connections to the same server.
const pacCacheTTL = 5 * time.Minute

type pacEntry struct {
	p  *url.URL
	at time.Time
}

var (
	pacMu    sync.Mutex
	pacCache = map[string]pacEntry{}
)

var (
	ieOnce sync.Once
	ieConf ieConfig
	ieOK   bool
)

func takeString(p *uint16) string {
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	_, _, _ = procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}

func readIEConfig() (ieConfig, bool) {
	ieOnce.Do(func() {
		if procGetIEProxyConfig.Find() != nil {
			return
		}
		var c winhttpIEProxyConfig
		r, _, _ := procGetIEProxyConfig.Call(uintptr(unsafe.Pointer(&c)))
		ieConf = ieConfig{
			autoDetect: c.autoDetect != 0,
			pacURL:     takeString(c.autoConfigURL),
			proxy:      takeString(c.proxy),
			bypass:     takeString(c.proxyBypass),
		}
		ieOK = r != 0
	})
	return ieConf, ieOK
}

func systemProxy(target *url.URL) (*url.URL, error) {
	c, ok := readIEConfig()
	if !ok {
		return nil, nil
	}
	if c.autoDetect || c.pacURL != "" {
		key := target.Scheme + "://" + target.Host
		pacMu.Lock()
		e, hit := pacCache[key]
		pacMu.Unlock()
		if hit && time.Since(e.at) < pacCacheTTL {
			return e.p, nil
		}
		if p, ok := pacProxy(target, c); ok {
			pacMu.Lock()
			pacCache[key] = pacEntry{p: p, at: time.Now()}
			pacMu.Unlock()
			return p, nil
		}
	}
	if c.proxy == "" || bypassed(target.Hostname(), c.bypass) {
		return nil, nil
	}
	return pickProxy(c.proxy, target.Scheme), nil
}

// pacProxy evaluates the PAC script (or WPAD) through WinHTTP. ok is false
// when it could not be evaluated, so the static setting applies.
func pacProxy(target *url.URL, c ieConfig) (*url.URL, bool) {
	type res struct {
		p  *url.URL
		ok bool
	}
	ch := make(chan res, 1)
	go func() {
		p, ok := winhttpProxyForURL(target, c)
		ch <- res{p, ok}
	}()
	select {
	case r := <-ch:
		return r.p, r.ok
	case <-time.After(pacTimeout):
		return nil, false
	}
}

func winhttpProxyForURL(target *url.URL, c ieConfig) (*url.URL, bool) {
	if procWinHTTPOpen.Find() != nil || procWinHTTPGetProxyForU.Find() != nil {
		return nil, false
	}
	agent, _ := windows.UTF16PtrFromString("OblireachSupport")
	h, _, _ := procWinHTTPOpen.Call(uintptr(unsafe.Pointer(agent)), winhttpAccessNoProxy, 0, 0, 0)
	if h == 0 {
		return nil, false
	}
	defer procWinHTTPCloseHandle.Call(h) //nolint:errcheck
	u := *target
	if u.Scheme != "http" && u.Scheme != "https" {
		u.Scheme = "https"
	}
	wu, err := windows.UTF16PtrFromString(u.String())
	if err != nil {
		return nil, false
	}
	var opts winhttpAutoProxyOptions
	if c.pacURL != "" {
		opts.flags |= winhttpAutoproxyConfigURL
		opts.autoConfigURL, _ = windows.UTF16PtrFromString(c.pacURL)
	}
	if c.autoDetect {
		opts.flags |= winhttpAutoproxyAutoDetect
		opts.autoDetectFlags = winhttpAutoDetectDHCP | winhttpAutoDetectDNSA
	}
	opts.autoLogonIfChallenged = 1
	var info winhttpProxyInfo
	r, _, _ := procWinHTTPGetProxyForU.Call(h, uintptr(unsafe.Pointer(wu)), uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return nil, false
	}
	list, bypass := takeString(info.proxy), takeString(info.proxyBypass)
	if info.accessType != winhttpAccessNamedProxy || list == "" || bypassed(target.Hostname(), bypass) {
		return nil, true // the script said DIRECT
	}
	return pickProxy(list, target.Scheme), true
}

// pickProxy reads a WinHTTP proxy list: "host:port", "host:port;host2:port"
// or per scheme "http=host:port;https=host:port" (socks entries ignored).
func pickProxy(list, scheme string) *url.URL {
	var plain, http, https string
	for _, e := range strings.FieldsFunc(list, func(r rune) bool { return r == ';' || r == ' ' }) {
		k, v, kv := strings.Cut(e, "=")
		switch {
		case !kv:
			if plain == "" {
				plain = e
			}
		case strings.EqualFold(k, "https"):
			if https == "" {
				https = v
			}
		case strings.EqualFold(k, "http"):
			if http == "" {
				http = v
			}
		}
	}
	p := plain
	if scheme == "https" || scheme == "wss" {
		if https != "" {
			p = https
		} else if p == "" {
			p = http
		}
	} else if http != "" {
		p = http
	}
	if p == "" {
		return nil
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	u, err := url.Parse(p)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

// bypassed applies a WinHTTP bypass list ("<local>", "*.corp.example",
// "10.*").
func bypassed(host, list string) bool {
	host = strings.ToLower(host)
	for _, e := range strings.FieldsFunc(list, func(r rune) bool { return r == ';' || r == ' ' || r == ',' }) {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "<local>" {
			if !strings.Contains(host, ".") || host == "localhost" || host == "127.0.0.1" || host == "::1" {
				return true
			}
			continue
		}
		e = strings.TrimPrefix(strings.TrimPrefix(e, "http://"), "https://")
		if ok, _ := path.Match(e, host); ok || e == host {
			return true
		}
	}
	return false
}
