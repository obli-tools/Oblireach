package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The tokens every theme block declares (theme design), written out
// here on their own: a token dropped from theme.go's table fails this test.
var wantThemeTokens = []string{
	"color-scheme",
	"--bg", "--bg-rgb", "--bg2", "--bg3", "--bg4", "--chrome", "--border", "--border2", "--input-bg", "--input-border",
	"--text", "--text2", "--muted", "--muted2",
	"--success", "--success-rgb", "--warn", "--warn-rgb", "--danger", "--danger-rgb", "--offline", "--info", "--info-rgb",
	"--os-win", "--os-win-rgb", "--os-linux", "--os-linux-rgb", "--os-mac", "--os-mac-rgb",
	"--viewport-bg", "--term-bg", "--term-fg", "--ink", "--shadow", "--scrim", "--surface-shadow",
	"--accent", "--accent-rgb", "--accent-fill", "--accent-h", "--accent-text", "--accent-l", "--on-accent",
	"--srv-violet", "--srv-teal", "--srv-fuchsia", "--srv-indigo", "--srv-cyan", "--srv-sand", "--srv-lavender", "--srv-mint",
}

var themeBlockRe = regexp.MustCompile(`(?m)^(:root,)?\[data-theme="([a-z-]+)"\]\{([^}]*)\}$`)

// themeBlocks: the declarations of each theme block of a page, by theme.
func themeBlocks(t *testing.T, page string) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, m := range themeBlockRe.FindAllStringSubmatch(page, -1) {
		decl := map[string]string{}
		for _, d := range strings.Split(m[3], ";") {
			if d == "" {
				continue
			}
			k, v, ok := strings.Cut(d, ":")
			if !ok {
				t.Fatalf("%s: bad declaration %q", m[2], d)
			}
			if _, dup := decl[k]; dup {
				t.Errorf("%s: %s declared twice", m[2], k)
			}
			decl[k] = v
		}
		if m[2] == defaultTheme && m[1] == "" {
			t.Errorf("the default theme's block must also be :root's")
		}
		out[m[2]] = decl
	}
	return out
}

// Every theme block of the page declares every token (derived ones too: a
// var() is resolved where it is declared, so a token declared on :root only
// would keep Operator's value under another theme).
func TestThemeBlocksComplete(t *testing.T) {
	page := buildUI(&Config{Language: "en"})
	blocks := themeBlocks(t, page)
	if len(blocks) != len(knownThemes) {
		t.Fatalf("%d theme blocks, want %d: %v", len(blocks), len(knownThemes), blocks)
	}
	for _, th := range knownThemes {
		b := blocks[th]
		if b == nil {
			t.Fatalf("no block for %s", th)
		}
		for _, tok := range wantThemeTokens {
			if b[tok] == "" {
				t.Errorf("%s: %s not declared", th, tok)
			}
		}
		// Derived tokens refer to their own block's values.
		if b["--accent-l"] != "var(--accent-text)" || b["--input-border"] != "var(--border)" {
			t.Errorf("%s: derived tokens %q %q", th, b["--accent-l"], b["--input-border"])
		}
		// A -rgb triplet is its colour's.
		for _, base := range []string{"--bg", "--success", "--warn", "--danger", "--info", "--accent", "--os-win", "--os-linux", "--os-mac"} {
			v, ok := hexRGB(b[base])
			if !ok || b[base+"-rgb"] != rgbTriplet(v) {
				t.Errorf("%s: %s-rgb %q does not match %s %q", th, base, b[base+"-rgb"], base, b[base])
			}
		}
	}
	if blocks["obli-daylight"]["color-scheme"] != "light" || blocks["obli-dim"]["color-scheme"] != "dark" {
		t.Error("Daylight is the light theme, the others dark")
	}
	// The validated Dim values.
	dim := blocks["obli-dim"]
	for k, v := range map[string]string{"--bg": "#2E3440", "--chrome": "#282D37", "--bg2": "#3B4252", "--bg3": "#434C5E", "--bg4": "#465063",
		"--border": "#4C566A", "--border2": "#5E6A82", "--text": "#ECEFF4", "--text2": "#D8DEE9", "--muted": "#A3ABBC",
		"--success": "#6CCB8F", "--warn": "#EBCB8B", "--danger": "#FF6B6B", "--offline": "#8A93A6", "--info": "#88A6E0"} {
		if dim[k] != v {
			t.Errorf("dim %s = %q, want %s", k, dim[k], v)
		}
	}
	// Daylight: the logo's white wordmark in the text colour.
	if !strings.Contains(page, `[data-theme="obli-daylight"] #login-logo .lcls-3,[data-theme="obli-daylight"] #topbar-logo .tcls-3{fill:var(--text)}`) {
		t.Error("Daylight: the white wordmark of the logos is not recoloured")
	}
	// The theme's rules come before the page's own.
	if strings.Index(page, `[data-theme="neon"]{`) > strings.Index(page, ".topbar{height:44px") {
		t.Error("the theme <style> must come before the page's rules")
	}
}

// The accent is the app's own, never the server's: red for Operator,
// Daylight and Dim (fill #C83232), crimson for Modern and Neon; the accent
// used as text has its own variable, the accent by default.
func TestThemeAccentRule(t *testing.T) {
	blocks := themeBlocks(t, buildUI(&Config{Language: "en"}))
	want := map[string][3]string{ // accent, fill, hover of the fill
		"obli-operator": {"#E03A3A", "#C83232", "#B41E1E"},
		"obli-daylight": {"#E03A3A", "#C83232", "#B41E1E"},
		"obli-dim":      {"#E03A3A", "#C83232", "#B41E1E"},
		"modern":        {"#C2001B", "#C2001B", "#960014"},
		"neon":          {"#C2001B", "#C2001B", "#960014"},
	}
	for th, w := range want {
		b := blocks[th]
		if b["--accent"] != w[0] || b["--accent-fill"] != w[1] || b["--accent-h"] != w[2] {
			t.Errorf("%s: accent %q fill %q hover %q, want %v", th, b["--accent"], b["--accent-fill"], b["--accent-h"], w)
		}
		if b["--accent-text"] == "" || b["--on-accent"] != "#FFFFFF" {
			t.Errorf("%s: accent text %q, on accent %q", th, b["--accent-text"], b["--on-accent"])
		}
		if b["--accent-text"] != "var(--accent)" {
			t.Logf("%s: the accent as text is %s (a tint chosen for it)", th, b["--accent-text"])
		}
	}
	// Nothing in the page sets the accent at run time any more (the old
	// applyServerTheme swapped three accent colours).
	page := buildUI(&Config{Language: "en"})
	if strings.Contains(page, "setProperty('--accent") {
		t.Error("the page still sets --accent itself")
	}
}

// The page's CSS holds no colour of its own any more: the theme's variables
// only, except what is drawn on the remote picture (its overlays, the mosaic
// labels, the performance HUD, the recording mark, the minimap on its dark
// letterbox) and Windows' red close button.
func TestNoHardcodedColours(t *testing.T) {
	page := buildUI(&Config{Language: "en"})
	// The page's head (the logos' SVGs carry their own brand colours).
	head := page[:strings.Index(page, "</head>")]
	styles := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindAllStringSubmatch(head, -1)
	if len(styles) < 3 {
		t.Fatalf("%d style blocks, want the theme's, the page's and the viewer's", len(styles))
	}
	allowed := []string{".perf-hud", ".rec-indicator", ".rec-dot", ".reach-overlay{", ".reach-tile{", ".reach-tile-label{",
		".toolbar-dropdown-menu .reach-minimap-mon{", ".toolbar-dropdown-menu .reach-minimap-mon:hover", ".toolbar-dropdown-menu .reach-minimap-mon.active",
		".reach-vo-chip{", ".wc-btn.wc-close:hover", ".wc-btn.wc-close:active"}
	forbidden := regexp.MustCompile(`#[0-9a-fA-F]{3,6}\b|rgba\(|color:white|:white\b`)
	rules := 0
	for _, st := range styles[1:] { // [0]: the theme's own values
		for _, line := range strings.Split(st[1], "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "@keyframes") {
				continue
			}
			rules++
			ok := false
			for _, a := range allowed {
				ok = ok || strings.HasPrefix(line, a)
			}
			if !ok && forbidden.MatchString(line) {
				t.Errorf("a colour of its own: %s", line)
			}
		}
	}
	if rules < 300 {
		t.Fatalf("only %d rules scanned: the scan is broken", rules)
	}
	// Nowhere in the page (its scripts' inline styles too): the old palette.
	// The theme's own token block is left out: #FF6868 is legitimately Dim's
	// --accent-text there (contrast choice), and nowhere else.
	outsideTokens := strings.Replace(page, styles[0][1], "", 1)
	if !strings.Contains(styles[0][1], "--accent-text:#FF6868") {
		t.Errorf("the Dim block lacks --accent-text:#FF6868")
	}
	for _, old := range []string{"rgba(194,0,27", "#161b22", "#0d1117", "#1c2128", "#21262d", "color:white", "fill=\"white\"", "#e84050", "#FF6868"} {
		if strings.Contains(outsideTokens, old) {
			t.Errorf("the page still holds %q", old)
		}
	}
	// The fills are the fill colour, text on them the on-accent one.
	for _, want := range []string{".btn-primary{background:var(--accent-fill);border:none;color:var(--on-accent)",
		".chat-msg.op .bubble{background:var(--accent-fill);color:var(--on-accent)", ".nav-item .nav-badge{", "background:var(--accent-fill);color:var(--on-accent);font-size:10px",
		".reach-btn-primary{background:var(--accent-fill)!important", ".topbar{height:44px;background:var(--chrome)", "background:var(--chrome);font-size:12px;color:var(--text);user-select:none}",
		"::-webkit-scrollbar-thumb{background:var(--border)", ".remote-viewport{flex:1;background:var(--viewport-bg)", ".exec-output{background:var(--term-bg)"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// The theme worn: the active server's account's; in multi-server mode the
// first server's (signed in or not); Operator when unknown.
func TestUITheme(t *testing.T) {
	cfg := &Config{Profiles: []ServerProfile{{ID: "a", AccountTheme: "obli-dim"}, {ID: "b", AccountTheme: " NEON "}, {ID: "c", AccountTheme: "bogus"}}, ActiveID: "b"}
	if got := cfg.uiTheme(); got != "neon" {
		t.Errorf("single mode: %q, want the active server's neon", got)
	}
	cfg.ActiveID = "c"
	if got := cfg.uiTheme(); got != "obli-operator" {
		t.Errorf("an unknown theme: %q, want obli-operator", got)
	}
	cfg.ServerMode = "multi"
	if got, src := cfg.uiTheme(), cfg.themeSource(); got != "obli-dim" || src == nil || src.ID != "a" {
		t.Errorf("multi mode: %q, want the first server's obli-dim", got)
	}
	cfg.Profiles[0].AccountTheme = ""
	if got := cfg.uiTheme(); got != "obli-operator" {
		t.Errorf("multi mode, first server's theme unknown: %q", got)
	}
	// One server only: no multi mode (its own theme).
	one := &Config{ServerMode: "multi", Profiles: []ServerProfile{{ID: "x", AccountTheme: "modern"}}, ActiveID: "x"}
	if one.serverMode() != "single" || one.uiTheme() != "modern" {
		t.Errorf("one server: mode %q theme %q", one.serverMode(), one.uiTheme())
	}
	if got := (&Config{}).uiTheme(); got != "obli-operator" {
		t.Errorf("no server: %q", got)
	}
	for in, want := range map[string]string{"obli-daylight": "obli-daylight", "OBLI-DIM": "obli-dim", " modern": "modern", "operator": "", "dark": "", "": ""} {
		if got := normalizeTheme(in); got != want {
			t.Errorf("normalizeTheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// The page wears the theme from its first paint: on <body> (<html> keeps
// exactly its lang / dir attributes).
func TestBuildUIBodyTheme(t *testing.T) {
	page := buildUI(&Config{Language: "en", ActiveID: "a", Profiles: []ServerProfile{{ID: "a", AccountTheme: "obli-dim"}}})
	if !strings.Contains(page, "\n<body data-theme=\"obli-dim\">\n") || strings.Contains(page, "\n<body>\n") {
		t.Error("the body does not wear the account's theme")
	}
	if !strings.Contains(page, `<html lang="en" dir="ltr">`) {
		t.Error("<html lang dir> changed")
	}
	if n := len(regexp.MustCompile(`\n<body[ >]`).FindAllString(page, -1)); n != 1 {
		t.Errorf("%d body tags", n)
	}
	if !strings.Contains(buildUI(&Config{Language: "en"}), "\n<body data-theme=\"obli-operator\">\n") {
		t.Error("no server: Operator")
	}
	if strings.Contains(page, "<!--THEME_CSS-->") {
		t.Error("the theme placeholder was not replaced")
	}
}

// The native colours come from the same table as the page's.
func TestPaletteTable(t *testing.T) {
	if len(themePalettes) != len(knownThemes) {
		t.Fatalf("%d palettes", len(themePalettes))
	}
	for _, th := range knownThemes {
		p := paletteOf(th)
		if p.Bg == 0 && th != "neon" || p.Chrome == 0 || p.Text == 0 || p.Muted == 0 || p.Border == 0 || p.Danger == 0 || p.Info == 0 || p.Accent == 0 {
			t.Errorf("%s: incomplete palette %+v", th, p)
		}
		if p.Light != (th == "obli-daylight") {
			t.Errorf("%s: light = %v", th, p.Light)
		}
	}
	if paletteOf("bogus") != paletteOf("obli-operator") || paletteOf("") != paletteOf("obli-operator") {
		t.Error("an unknown theme: Operator's palette")
	}
	dim := paletteOf("obli-dim")
	if dim.Bg != 0x2E3440 || dim.Chrome != 0x282D37 || dim.Text != 0xECEFF4 || dim.Muted != 0xA3ABBC || dim.Accent != 0xE03A3A {
		t.Errorf("dim palette %+v", dim)
	}
	if colorRef(0x2E3440) != 0x40342E || cssColor(0x2E3440) != "#2e3440" {
		t.Errorf("colorRef %06x cssColor %s", colorRef(0x2E3440), cssColor(0x2E3440))
	}
	// The strip: hover / pressed = the ink over the chrome at 8% / 5%.
	s, title, g, hv, pr := stripColors(paletteOf("obli-operator"))
	if s != 0x131728 || title != 0x828CAF || g != 0xF0F4FC || hv != blendRGB(0x131728, 0xFFFFFF, .08) || pr != blendRGB(0x131728, 0xFFFFFF, .05) || hv == s {
		t.Errorf("operator strip %06x %06x %06x %06x %06x", s, title, g, hv, pr)
	}
	if _, _, _, hv, _ := stripColors(paletteOf("obli-daylight")); hv != blendRGB(0xECEFF4, 0x2E3440, .08) || hv >= 0xECEFF4 {
		t.Errorf("daylight hover %06x: darker than the chrome", hv)
	}
	if blendRGB(0x000000, 0xFFFFFF, 1) != 0xFFFFFF || blendRGB(0x102030, 0xFFFFFF, 0) != 0x102030 {
		t.Error("blendRGB ends")
	}
	// The palette on screen follows setCurrentPalette.
	defer setCurrentPalette(defaultTheme)
	setCurrentPalette("neon")
	if currentPalette() != paletteOf("neon") {
		t.Error("currentPalette")
	}
}

// /local/theme stores the account's theme on its profile; the app wears the
// theme of its source profile, and every window is told once per change: an
// account's new theme, a server switch, the server mode, a server removed.
func TestThemeEndpointAndBroadcast(t *testing.T) {
	oldDir, oldFile := configDir, configFile
	configDir = t.TempDir()
	configFile = filepath.Join(configDir, "config.json")
	defer func() { configDir, configFile = oldDir, oldFile }()
	defer setCurrentPalette(defaultTheme)

	cfg := &Config{ServerURL: "https://a.example", Profiles: []ServerProfile{
		{ID: "pa", ServerURL: "https://a.example", Name: "a", SessionFile: "session.json"},
		{ID: "pb", ServerURL: "https://b.example", Name: "b", SessionFile: "session-pb.json", AccountTheme: "neon"},
		{ID: "pc", ServerURL: "https://c.example", Name: "c", SessionFile: "session-pc.json"},
	}, ActiveID: "pa"}
	p := newProxy(cfg, configDir, 0)
	var told []string
	p.setThemeHook(func(th string) { told = append(told, th) })
	do := func(method, body string) (*httptest.ResponseRecorder, themeState) {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(method, "/local/theme", strings.NewReader(body))))
		var st themeState
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return rec, st
	}
	servers := func(path, body string) (int, map[string]interface{}) {
		rec := httptest.NewRecorder()
		method := http.MethodPost
		if body == "" {
			method = http.MethodGet
		}
		p.handleServers(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if rec, st := do(http.MethodGet, ""); rec.Code != 200 || st.Theme != "obli-operator" || st.Source != "pa" {
		t.Fatalf("GET: %d %+v", rec.Code, st)
	}
	// The active server's account reports Dim: kept, the app wears it, told once.
	if _, st := do(http.MethodPost, `{"profile":"pa","theme":"obli-dim"}`); st.Theme != "obli-dim" || cfg.Profiles[0].AccountTheme != "obli-dim" ||
		strings.Join(told, ",") != "obli-dim" || currentPalette() != paletteOf("obli-dim") {
		t.Fatalf("account dim: %+v told=%v", st, told)
	}
	// Reported again: nothing new said. An empty profile: the active one.
	do(http.MethodPost, `{"profile":"","theme":"OBLI-DIM"}`)
	if len(told) != 1 {
		t.Errorf("same theme: told again %v", told)
	}
	// Another server's account: kept for it, the app does not change.
	do(http.MethodPost, `{"profile":"pc","theme":"obli-daylight"}`)
	if cfg.Profiles[2].AccountTheme != "obli-daylight" || len(told) != 1 {
		t.Errorf("another server's theme: %q told=%v", cfg.Profiles[2].AccountTheme, told)
	}
	saved, _ := os.ReadFile(configFile)
	if !strings.Contains(string(saved), `"accountTheme": "obli-dim"`) || !strings.Contains(string(saved), `"accountTheme": "obli-daylight"`) {
		t.Errorf("config.json keeps the accounts' themes: %s", saved)
	}
	// /local/servers says each server's theme and the one worn.
	code, out := servers("/local/servers", "")
	profs, _ := out["profiles"].([]interface{})
	if code != 200 || out["theme"] != "obli-dim" || out["mode"] != "single" || len(profs) != 3 ||
		profs[1].(map[string]interface{})["theme"] != "neon" || profs[0].(map[string]interface{})["theme"] != "obli-dim" {
		t.Fatalf("GET /local/servers: %d %v", code, out)
	}
	// A server switch: its account's theme at once.
	if code, out := servers("/local/servers/switch", `{"id":"pb"}`); code != 200 || out["theme"] != "neon" || told[len(told)-1] != "neon" || len(told) != 2 {
		t.Fatalf("switch: %d %v told=%v", code, out, told)
	}
	// An unknown theme from the server: Operator.
	do(http.MethodPost, `{"theme":"something-new"}`)
	if cfg.Profiles[1].AccountTheme != "" || told[len(told)-1] != "obli-operator" || len(told) != 3 {
		t.Errorf("unknown theme: %q told=%v", cfg.Profiles[1].AccountTheme, told)
	}
	// Multi-server mode: the first server's theme, whichever is active.
	p.mu.Lock()
	cfg.ServerMode = "multi"
	p.mu.Unlock()
	p.syncTheme()
	if told[len(told)-1] != "obli-dim" || len(told) != 4 {
		t.Errorf("multi mode: told=%v", told)
	}
	do(http.MethodPost, `{"profile":"pb","theme":"modern"}`) // not the source: no change
	if len(told) != 4 {
		t.Errorf("multi mode, another server's theme: told=%v", told)
	}
	// The first server removed: the next one is the source.
	servers("/local/servers/switch", `{"id":"pc"}`)
	if len(told) != 4 {
		t.Errorf("multi mode, switch: the source is still the first server: told=%v", told)
	}
	if code, _ := servers("/local/servers/remove", `{"id":"pa"}`); code != 200 || told[len(told)-1] != "modern" || len(told) != 5 {
		t.Errorf("first server removed: %d told=%v", code, told)
	}
	// Refused: a bad body, an unknown profile, another method.
	if rec, _ := do(http.MethodPost, `not json`); rec.Code != 400 {
		t.Errorf("bad body: %d", rec.Code)
	}
	if rec, _ := do(http.MethodPost, `{"profile":"zz","theme":"neon"}`); rec.Code != 404 {
		t.Errorf("unknown profile: %d", rec.Code)
	}
	if rec, _ := do(http.MethodDelete, ""); rec.Code != 405 {
		t.Errorf("DELETE: %d", rec.Code)
	}
	if rec, _ := do(http.MethodPost, `{"theme":"`+strings.Repeat("x", 5000)+`"}`); rec.Code != 400 {
		t.Errorf("a body over 4 KB: %d", rec.Code)
	}
	if len(told) != 5 {
		t.Errorf("refusals told something: %v", told)
	}
}

// The SSO return pages wear the theme on screen.
func TestSsoPagesThemed(t *testing.T) {
	defer setCurrentPalette(defaultTheme)
	setCurrentPalette("obli-daylight")
	rec := httptest.NewRecorder()
	ssoFailedPage(rec, "why", "detail")
	body := rec.Body.String()
	if !strings.Contains(body, "background:#e5e9f0") || !strings.Contains(body, "color-scheme:light") || !strings.Contains(body, "color:#636e82") ||
		strings.Contains(body, "#0d1117") {
		t.Errorf("SSO failed page not in Daylight: %s", body)
	}
	setCurrentPalette("obli-dim")
	rec = httptest.NewRecorder()
	ssoFailedPage(rec, "why", "")
	if !strings.Contains(rec.Body.String(), "background:#2e3440") || !strings.Contains(rec.Body.String(), "color:#ff6b6b") {
		t.Errorf("SSO failed page not in Dim: %s", rec.Body.String())
	}
}

// withSecret: r as the app's own WebView sends it (the per-run secret cookie).
func withSecret(p *Proxy, r *http.Request) *http.Request {
	r.AddCookie(&http.Cookie{Name: p.authCookie(), Value: p.secret})
	return r
}
