package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// relLum / contrast: WCAG 2 relative luminance and contrast ratio of two
// #RRGGBB colours.
func relLum(hex string) float64 {
	v, ok := hexRGB(hex)
	if !ok {
		return -1
	}
	ch := func(c uint32) float64 {
		x := float64(c) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(v>>16&0xFF) + 0.7152*ch(v>>8&0xFF) + 0.0722*ch(v&0xFF)
}

func contrast(a, b string) float64 {
	x, y := relLum(a), relLum(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

// The server profiles' colours are theme tokens: every theme declares each
// one, and each mark stays visible (3:1 or more, non-text contrast) on that
// theme's surfaces, Daylight included. The page's server colours are those
// tokens, one per colour the app gives a profile.
func TestServerColourTokens(t *testing.T) {
	blocks := themeBlocks(t, buildUI(&Config{}))
	for _, theme := range knownThemes {
		decl := blocks[theme]
		for _, name := range serverPalette {
			v := decl["--srv-"+name]
			if v == "" {
				t.Fatalf("%s: --srv-%s not declared", theme, name)
			}
			for _, surf := range []string{"--bg", "--bg2", "--bg3"} {
				if c := contrast(v, decl[surf]); c < 3 {
					t.Errorf("%s: --srv-%s %s on %s %s: %.2f:1, want 3:1 or more", theme, name, v, surf, decl[surf], c)
				}
			}
		}
	}
	page := buildUI(&Config{})
	for _, name := range serverPalette {
		if !strings.Contains(page, name+": 'var(--srv-"+name+")'") {
			t.Errorf("SERVER_COLORS.%s is not its theme token", name)
		}
	}
}

// Neon's bar on the active nav item is on the rail's inner side in both
// directions (the rail is on the right in right-to-left languages).
func TestNeonNavBarRTL(t *testing.T) {
	page := buildUI(&Config{})
	for _, rule := range []string{
		`[data-theme="neon"] .nav-item.active{box-shadow:inset 3px 0 0 var(--accent)}`,
		`html[dir="rtl"] [data-theme="neon"] .nav-item.active{box-shadow:inset -3px 0 0 var(--accent)}`,
	} {
		if !strings.Contains(page, rule) {
			t.Errorf("missing: %s", rule)
		}
	}
}

// Every theme answer carries the change count (theme.go themeGen): it grows
// with each change of the theme worn, never otherwise, and the windows' notice
// carries the same count (the page keeps the newest).
func TestThemeVersion(t *testing.T) {
	useTempConfig(t)
	defer setCurrentPalette(defaultTheme)
	cfg := &Config{ServerURL: "https://a.example", Profiles: []ServerProfile{
		{ID: "pa", ServerURL: "https://a.example", Name: "a", SessionFile: "session.json"},
		{ID: "pb", ServerURL: "https://b.example", Name: "b", SessionFile: "session-pb.json", AccountTheme: "neon"},
	}, ActiveID: "pa"}
	p := newProxy(cfg, configDir, 0)
	var toldVer []uint64
	p.setThemeHook(func(string) { toldVer = append(toldVer, themeGen.Load()) })
	theme := func(method, body string) themeState {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(method, "/local/theme", strings.NewReader(body))))
		var st themeState
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return st
	}
	servers := func(path, body string) map[string]interface{} {
		rec := httptest.NewRecorder()
		method := http.MethodPost
		if body == "" {
			method = http.MethodGet
		}
		p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(method, path, strings.NewReader(body))))
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	v0 := theme(http.MethodGet, "").Version
	st := theme(http.MethodPost, `{"profile":"pa","theme":"obli-dim"}`)
	if st.Theme != "obli-dim" || st.Version != v0+1 || len(toldVer) != 1 || toldVer[0] != st.Version {
		t.Fatalf("a change: %+v (from %d), told %v", st, v0, toldVer)
	}
	if again := theme(http.MethodPost, `{"profile":"pa","theme":"obli-dim"}`); again.Version != st.Version || len(toldVer) != 1 {
		t.Fatalf("no change: %+v, told %v", again, toldVer)
	}
	if other := theme(http.MethodPost, `{"profile":"pb","theme":"modern"}`); other.Theme != "obli-dim" || other.Version != st.Version {
		t.Fatalf("another server's account (not worn): %+v", other)
	}
	out := servers("/local/servers/switch", `{"id":"pb"}`)
	if out["theme"] != "modern" || uint64(out["themeVersion"].(float64)) != st.Version+1 || len(toldVer) != 2 || toldVer[1] != st.Version+1 {
		t.Fatalf("a switch: %v, told %v", out, toldVer)
	}
	if got := servers("/local/servers", ""); uint64(got["themeVersion"].(float64)) != st.Version+1 || theme(http.MethodGet, "").Version != st.Version+1 {
		t.Fatalf("read again: %v", got)
	}
}

// The local page's address never leaves the machine: the page asks for no
// Referer, and the proxy drops one if sent.
func TestNoRefererUpstream(t *testing.T) {
	useTempConfig(t)
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Referer"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer up.Close()
	cfg := &Config{ServerURL: up.URL, Profiles: []ServerProfile{{ID: "pa", ServerURL: up.URL, Name: "a", SessionFile: "session.json"}}, ActiveID: "pa"}
	p := newProxy(cfg, configDir, 0)
	for _, path := range []string{"/proxy/api/x", "/sp/pa/api/y"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Referer", "http://127.0.0.1:0/?detached=7&p=pa")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, withSecret(p, r))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	if len(seen) != 2 || seen[0] != "" || seen[1] != "" {
		t.Fatalf("Referer sent upstream: %q", seen)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(http.MethodGet, "/", nil)))
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("page Referrer-Policy: %q", got)
	}
}

// A server that accepts the connection and never answers is given up (its
// answer's headers are waited for upstreamHeaderTimeout): 502, the page's
// request freed. The default is well above a socket.io long-poll.
func TestUpstreamHeaderTimeout(t *testing.T) {
	if tr, ok := upstreamTransport.(*http.Transport); !ok || tr.ResponseHeaderTimeout != upstreamHeaderTimeout || upstreamHeaderTimeout < 60*time.Second {
		t.Fatalf("upstream transport: %T %v", upstreamTransport, upstreamHeaderTimeout)
	}
	useTempConfig(t)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	defer up.Close()
	defer close(release)
	old := upstreamTransport
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 100 * time.Millisecond
	upstreamTransport = tr
	defer func() { upstreamTransport = old }()
	cfg := &Config{ServerURL: up.URL, Profiles: []ServerProfile{{ID: "pa", ServerURL: up.URL, Name: "a", SessionFile: "session.json"}}, ActiveID: "pa"}
	p := newProxy(cfg, configDir, 0)
	start := time.Now()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(http.MethodGet, "/sp/pa/api/reach/overview", nil)))
	if rec.Code != http.StatusBadGateway || time.Since(start) > 3*time.Second {
		t.Fatalf("a server that never answers: %d after %v", rec.Code, time.Since(start))
	}
}
