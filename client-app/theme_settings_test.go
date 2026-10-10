package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The Desktop's own theme settings (Settings > Application > Appearance,
// theme.go themeOverride): "Theme in multi-server mode" and
// "Theme of the local base". Each one rules only its case; "" follows the
// servers (the rule before, the default); a real server on screen always
// wears its account's theme.
func TestThemeOverrideRule(t *testing.T) {
	srvA := ServerProfile{ID: "a", Name: "A", ServerURL: "https://a", AccountTheme: "obli-dim"}
	srvB := ServerProfile{ID: "b", Name: "B", ServerURL: "https://b", AccountTheme: "neon"}
	loc := ServerProfile{ID: "l", Name: "Local", Kind: profileKindLocal}
	mk := func(active, mode, multi, local string, profs ...ServerProfile) *Config {
		return &Config{Profiles: append([]ServerProfile(nil), profs...), ActiveID: active, ServerMode: mode, MultiTheme: multi, LocalTheme: local}
	}
	cases := []struct {
		name       string
		cfg        *Config
		want, kind string
	}{
		{"multi, follow: the first server's", mk("b", "multi", "", "", srvA, srvB, loc), "obli-dim", "multi"},
		{"multi, chosen", mk("b", "multi", "obli-daylight", "", srvA, srvB, loc), "obli-daylight", "multi"},
		{"multi, the local setting does not count", mk("b", "multi", "", "modern", srvA, srvB, loc), "obli-dim", "multi"},
		{"multi, chosen value in any case", mk("a", "multi", " MODERN ", "", srvA, srvB), "modern", "multi"},
		{"multi, an unknown value: follow", mk("a", "multi", "bogus", "", srvA, srvB), "obli-dim", "multi"},
		{"single real server: its account's, whatever the settings", mk("b", "", "obli-daylight", "modern", srvA, srvB, loc), "neon", ""},
		{"single real server (multi asked but impossible): its own", mk("a", "multi", "obli-daylight", "modern", srvA), "obli-dim", ""},
		{"local base, follow: the servers' (parity pass)", mk("l", "", "obli-daylight", "", srvA, srvB, loc), "obli-dim", "local"},
		{"local base, chosen", mk("l", "", "obli-daylight", "modern", srvA, srvB, loc), "modern", "local"},
		{"local base alone, follow: the default", mk("l", "", "", "", loc), defaultTheme, "local"},
		{"local base alone, chosen", mk("l", "", "", "obli-daylight", loc), "obli-daylight", "local"},
	}
	for _, c := range cases {
		kind, _ := c.cfg.themeOverride()
		if got := c.cfg.uiTheme(); got != c.want || kind != c.kind {
			t.Errorf("%s: theme %q kind %q, want %q %q", c.name, got, kind, c.want, c.kind)
		}
	}
	for in, want := range map[string]string{"": "", "follow": "", " Follow ": "", "obli-dim": "obli-dim", "NEON": "neon"} {
		if got, ok := themeSettingValue(in); !ok || got != want {
			t.Errorf("themeSettingValue(%q) = %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"bogus", "operator", "obli-operator;", "#fff"} {
		if _, ok := themeSettingValue(bad); ok {
			t.Errorf("themeSettingValue(%q) accepted", bad)
		}
	}
}

// /local/theme/settings saves each setting (config.json), the app wears what
// the rule gives at once and tells every window once per change; the
// settings never re-theme a real server on screen; refusals save nothing.
func TestThemeSettingsEndpoint(t *testing.T) {
	oldDir, oldFile := configDir, configFile
	configDir = t.TempDir()
	configFile = filepath.Join(configDir, "config.json")
	defer func() { configDir, configFile = oldDir, oldFile }()
	defer setCurrentPalette(defaultTheme)

	cfg := &Config{ServerURL: "https://a.example", Profiles: []ServerProfile{
		{ID: "pa", ServerURL: "https://a.example", Name: "a", SessionFile: "session.json", AccountTheme: "obli-dim"},
		{ID: "pb", ServerURL: "https://b.example", Name: "b", SessionFile: "session-pb.json", AccountTheme: "neon"},
		{ID: "pl", Name: "Local", Kind: profileKindLocal},
	}, ActiveID: "pb"}
	p := newProxy(cfg, configDir, 0)
	var told []string
	p.setThemeHook(func(th string) { told = append(told, th) })
	do := func(method, body string) (*httptest.ResponseRecorder, themeState) {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(method, "/local/theme/settings", strings.NewReader(body))))
		var st themeState
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return rec, st
	}
	servers := func(path, body string) int {
		rec := httptest.NewRecorder()
		p.handleServers(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec.Code
	}
	last := func() string {
		if len(told) == 0 {
			return ""
		}
		return told[len(told)-1]
	}

	if rec, st := do(http.MethodGet, ""); rec.Code != 200 || st.Theme != "neon" || st.Source != "pb" || st.Setting != "" || st.Chosen || st.MultiTheme != "" || st.LocalTheme != "" {
		t.Fatalf("GET: %d %+v", rec.Code, st)
	}
	// A real server on screen: both settings saved, its account's theme kept.
	if rec, st := do(http.MethodPost, `{"multiTheme":"obli-daylight","localTheme":"MODERN"}`); rec.Code != 200 || st.Theme != "neon" ||
		st.MultiTheme != "obli-daylight" || st.LocalTheme != "modern" || len(told) != 0 {
		t.Fatalf("single server: %d %+v told=%v", rec.Code, st, told)
	}
	if cfg.MultiTheme != "obli-daylight" || cfg.LocalTheme != "modern" {
		t.Fatalf("not kept: %q %q", cfg.MultiTheme, cfg.LocalTheme)
	}
	// Persistence: config.json holds them, a new run reads them back.
	if re := loadConfig(); re.MultiTheme != "obli-daylight" || re.LocalTheme != "modern" {
		t.Fatalf("config.json: %q %q", re.MultiTheme, re.LocalTheme)
	}
	// "All servers": the multi-server setting is worn, every window told once.
	p.mu.Lock()
	cfg.ServerMode = "multi"
	p.mu.Unlock()
	p.syncTheme()
	if strings.Join(told, ",") != "obli-daylight" || currentPalette() != paletteOf("obli-daylight") {
		t.Fatalf("multi mode: told=%v", told)
	}
	if _, st := do(http.MethodGet, ""); st.Setting != "multi" || !st.Chosen || st.Source != "" || st.Theme != "obli-daylight" {
		t.Errorf("multi GET: %+v", st)
	}
	// Another theme chosen: worn at once; the same again: nothing said.
	do(http.MethodPost, `{"multiTheme":"obli-operator"}`)
	do(http.MethodPost, `{"multiTheme":"obli-operator"}`)
	if strings.Join(told, ",") != "obli-daylight,obli-operator" {
		t.Errorf("multi changed: told=%v", told)
	}
	// An account's theme reported meanwhile does not override the setting.
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, withSecret(p, httptest.NewRequest(http.MethodPost, "/local/theme", strings.NewReader(`{"profile":"pa","theme":"neon"}`))))
	if len(told) != 2 || cfg.Profiles[0].AccountTheme != "neon" {
		t.Errorf("account theme in multi mode with a setting: told=%v", told)
	}
	// "Follow the first server": its account's theme again.
	if _, st := do(http.MethodPost, `{"multiTheme":"follow"}`); st.Theme != "neon" || st.Chosen || st.Source != "pa" || last() != "neon" || cfg.MultiTheme != "" {
		t.Errorf("follow: %+v told=%v", st, told)
	}
	// Back to one server: the multi setting has no say.
	do(http.MethodPost, `{"multiTheme":"obli-daylight"}`)
	if code := servers("/local/servers/mode", `{"mode":"single"}`); code != 200 {
		t.Fatalf("mode single: %d", code)
	}
	if cfg.serverMode() != "single" || cfg.uiTheme() != cfg.active().AccountTheme {
		t.Errorf("single again: mode %q theme %q", cfg.serverMode(), cfg.uiTheme())
	}
	// The local base on screen: its setting (modern), told at once.
	if code := servers("/local/servers/switch", `{"id":"pl"}`); code != 200 {
		t.Fatalf("switch local: %d", code)
	}
	if last() != "modern" || cfg.uiTheme() != "modern" {
		t.Errorf("local base: told=%v", told)
	}
	if _, st := do(http.MethodGet, ""); st.Setting != "local" || !st.Chosen {
		t.Errorf("local GET: %+v", st)
	}
	// Its setting changed, then "follow": the servers' (the first one's).
	do(http.MethodPost, `{"localTheme":"obli-dim"}`)
	if last() != "obli-dim" {
		t.Errorf("local changed: told=%v", told)
	}
	do(http.MethodPost, `{"localTheme":""}`)
	if last() != "neon" || cfg.LocalTheme != "" {
		t.Errorf("local follow: told=%v (first server neon)", told)
	}
	// Refused: an unknown theme (nothing saved, even the valid field), a bad
	// body, a body over 4 KB, another method.
	n := len(told)
	if rec, _ := do(http.MethodPost, `{"multiTheme":"obli-dim","localTheme":"bogus"}`); rec.Code != 400 || cfg.MultiTheme != "obli-daylight" {
		t.Errorf("unknown theme: %d multi %q", rec.Code, cfg.MultiTheme)
	}
	if rec, _ := do(http.MethodPost, `not json`); rec.Code != 400 {
		t.Errorf("bad body: %d", rec.Code)
	}
	if rec, _ := do(http.MethodPost, `{"multiTheme":"`+strings.Repeat("x", 5000)+`"}`); rec.Code != 400 {
		t.Errorf("a body over 4 KB: %d", rec.Code)
	}
	if rec, _ := do(http.MethodDelete, ""); rec.Code != 405 {
		t.Errorf("DELETE: %d", rec.Code)
	}
	if len(told) != n {
		t.Errorf("refusals told something: %v", told)
	}
}

// The catalogue the settings offer: the Obli suite's five themes once each,
// their names, their preview from the very tokens worn; the accent of every
// theme stays the app's red family, and choosing a theme in the settings
// changes the theme worn, never the theme blocks (the accent included).
func TestThemeCatalogAndAccent(t *testing.T) {
	if len(themeCatalog) != len(knownThemes) {
		t.Fatalf("%d entries", len(themeCatalog))
	}
	names := map[string]string{"obli-operator": "Obli Operator", "obli-daylight": "Obli Daylight", "obli-dim": "Obli Dim", "modern": "Modern UI", "neon": "Neon UI"}
	for i, e := range themeCatalog {
		if e.ID != knownThemes[i] || e.Name != names[e.ID] {
			t.Errorf("entry %d: %+v", i, e)
		}
		want := [4]string{themeTokenValue("--bg", i), themeTokenValue("--bg2", i), themeTokenValue("--text", i), themeTokenValue("--accent", i)}
		if e.Swatch != want || e.Light != (e.ID == "obli-daylight") {
			t.Errorf("%s: swatch %v light %v", e.ID, e.Swatch, e.Light)
		}
		if a := e.Swatch[3]; a != "#E03A3A" && a != "#C2001B" {
			t.Errorf("%s: accent %s is not the app's red", e.ID, a)
		}
	}
	base := buildUI(&Config{Language: "en", ActiveID: "a", Profiles: []ServerProfile{{ID: "a", ServerURL: "https://a", AccountTheme: "obli-dim"}, {ID: "b", ServerURL: "https://b"}}})
	chosen := buildUI(&Config{Language: "en", ActiveID: "a", ServerMode: "multi", MultiTheme: "obli-daylight",
		Profiles: []ServerProfile{{ID: "a", ServerURL: "https://a", AccountTheme: "obli-dim"}, {ID: "b", ServerURL: "https://b"}}})
	if !strings.Contains(chosen, "\n<body data-theme=\"obli-daylight\">\n") || !strings.Contains(base, "\n<body data-theme=\"obli-dim\">\n") {
		t.Error("the page's first paint does not wear the theme the rule gives")
	}
	bb, cb := themeBlocks(t, base), themeBlocks(t, chosen)
	for _, th := range knownThemes {
		for _, k := range []string{"--accent", "--accent-fill", "--accent-h", "--accent-text"} {
			if bb[th][k] == "" || bb[th][k] != cb[th][k] {
				t.Errorf("%s %s: %q / %q", th, k, bb[th][k], cb[th][k])
			}
		}
	}
	if strings.Contains(chosen, "/*THEME_CATALOG*/") || !strings.Contains(chosen, "const THEME_CATALOG = "+themeCatalogJS()+";") {
		t.Error("the catalogue is not injected in the page")
	}
}
