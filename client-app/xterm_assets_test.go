package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The terminal's library (xterm.js 5.3.0 + fit 0.8.0, vendored): embedded,
// each file with its MIT notice first, served only to this app's WebView
// (the per-run secret cookie, a local Origin), with its type.
func TestXtermAssets(t *testing.T) {
	for name, c := range map[string]struct {
		body   []byte
		banner string
	}{
		"xterm.min.js":           {xtermJS, "/*! xterm.js v5.3.0 | (c) 2017-2019 The xterm.js authors, (c) 2014-2016 SourceLair, (c) 2012-2013 Christopher Jeffrey | MIT License"},
		"xterm-addon-fit.min.js": {xtermFitJS, "/*! xterm-addon-fit v0.8.0 | (c) 2019 The xterm.js authors | MIT License"},
		"xterm.css":              {xtermCSS, "/*! xterm.js v5.3.0 css/xterm.css"},
	} {
		s := string(c.body)
		if len(c.body) < 1000 || !strings.HasPrefix(s, c.banner) {
			t.Fatalf("%s: %d bytes, starts %q", name, len(c.body), s[:min(len(s), 120)])
		}
		// The whole MIT permission notice, not only a name.
		head := s[:min(len(s), 3000)] // (the banner comes first)
		if !strings.Contains(head, "Permission is hereby granted, free of charge") || !strings.Contains(head, "The above copyright notice and this permission notice shall be included") {
			t.Fatalf("%s: the MIT notice is missing", name)
		}
		if strings.Contains(s, "sourceMappingURL=") {
			t.Fatalf("%s: points at a source map that is not served", name)
		}
	}
	if !strings.Contains(string(xtermJS), "e.Terminal=") || !strings.Contains(string(xtermFitJS), "e.FitAddon=t()") {
		t.Fatal("the UMD bundles no longer set window.Terminal / window.FitAddon")
	}

	p := newProxy(&Config{}, t.TempDir(), 4321)
	get := func(path string, cookie bool, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: p.authCookie(), Value: p.secret})
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec
	}
	for path, want := range map[string]struct {
		ctype string
		body  []byte
	}{
		"/local/xterm.js":           {"application/javascript; charset=utf-8", xtermJS},
		"/local/xterm-addon-fit.js": {"application/javascript; charset=utf-8", xtermFitJS},
		"/local/xterm.css":          {"text/css; charset=utf-8", xtermCSS},
	} {
		rec := get(path, true, "")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != want.ctype || rec.Body.String() != string(want.body) ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
			t.Fatalf("%s: %d %q (%d bytes)", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
		if rec := get(path, true, "http://127.0.0.1:4321"); rec.Code != http.StatusOK {
			t.Fatalf("%s from the local UI: %d", path, rec.Code)
		}
		if rec := get(path, false, ""); rec.Code != http.StatusForbidden || rec.Body.Len() > 100 {
			t.Fatalf("%s without the secret cookie: %d", path, rec.Code)
		}
		if rec := get(path, true, "https://evil.example"); rec.Code != http.StatusForbidden {
			t.Fatalf("%s from another origin: %d", path, rec.Code)
		}
	}
}

// A tool window's entry: m=<kind> is kept only when it is exactly one of the
// tools; anything else is dropped (the window is then an ObliReach one).
func TestEntryTargetToolKind(t *testing.T) {
	p := newProxy(&Config{}, t.TempDir(), 4321)
	k := url.QueryEscape(p.secret)
	enter := func(q string) string {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, entryPath+"?k="+k+q, nil))
		return rec.Header().Get("Location")
	}
	for _, kind := range []string{"cmd", "powershell", "ssh", "files"} {
		if loc := enter("&d=7&p=A&m=" + kind); loc != "/?detached=7&p=A&m="+kind {
			t.Fatalf("m=%s: %q", kind, loc)
		}
	}
	for _, m := range []string{"evil", "cmd&x=1", "CMD", "cmd ", "", "files/../x", "<script>", "ssh~cmd"} {
		if loc := enter("&d=7&p=A&m=" + url.QueryEscape(m)); loc != "/?detached=7&p=A" {
			t.Fatalf("m=%q: landed on %q", m, loc)
		}
	}
	// Never without a valid device.
	if loc := enter("&d=..%2Fx&m=cmd"); loc != "/" {
		t.Fatalf("invalid device with a kind: %q", loc)
	}
}

// The URL a tool window opens carries its kind; an ObliReach one none.
func TestDetachedEntryURLKind(t *testing.T) {
	p := newProxy(&Config{}, t.TempDir(), 4321)
	for kind, want := range map[string]string{"cmd": "cmd", "powershell": "powershell", "ssh": "ssh", "files": "files", "": "", "bogus": "", "CMD": ""} {
		u, err := url.Parse(p.detachedEntryURL("12", "A", -1, false, kind))
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Query().Get("m"); got != want || (want == "" && u.Query().Has("m")) {
			t.Fatalf("kind %q: m=%q", kind, got)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
		loc := "/?detached=12&p=A"
		if want != "" {
			loc += "&m=" + want
		}
		if rec.Header().Get("Location") != loc {
			t.Fatalf("kind %q lands on %q, want %q", kind, rec.Header().Get("Location"), loc)
		}
	}
}
