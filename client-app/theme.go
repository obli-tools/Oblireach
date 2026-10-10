package main

// The app's colour themes: the whole Desktop
// follows the theme the operator chose on the Obliance server of the active
// profile (in multi-server mode: the first server of the list), in every
// window (the main one, the detached session windows, the viewer, the
// dialogs, the native title strip, the SSO return pages).
//
// The themes are Obliance's (client/src/index.css, [data-theme=...] blocks):
// obli-operator (the default, also for an unknown value), obli-daylight
// (light), obli-dim, modern, neon. Their neutrals and status colours are
// mirrored here; the accent is ALWAYS the app's own red family (the values
// Obliance uses for that theme), never taken from anything the server says.
//
// themeTokens is the one source of truth: the page's CSS custom properties
// (themeCSS, one block per theme, every block declaring every token) and the
// native colours (themePalettes) are both built from it.
//
// The theme is set on <body data-theme="..."> (buildUI, then
// window.__reachOnTheme): <html> keeps exactly its lang / dir attributes.
//
// Two settings of the Desktop may choose the theme instead (Settings >
// Application > Appearance, themeOverride): in multi-server mode,
// and on a local base alone on screen. The accent stays the app's either way.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// knownThemes: Obliance's theme ids, in the order of themeToken.v.
var knownThemes = []string{"obli-operator", "obli-daylight", "obli-dim", "modern", "neon"}

const defaultTheme = "obli-operator"

// normalizeTheme: an Obliance theme id as the app knows it (trimmed, lower
// case), or "" for anything else.
func normalizeTheme(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, t := range knownThemes {
		if s == t {
			return t
		}
	}
	return ""
}

// themeSource: the server profile whose account's theme the app wears (when
// no setting of the Desktop chooses it, themeOverride below): the
// active one; in multi-server mode (Config.ServerMode), the first real
// Obliance server of the list (signed in or not: its last known theme; a
// local base has no account, hence no theme).
//
// A local base on screen (single mode) has no account: the app keeps the
// theme of its Obliance servers (the first one's, as in multi-server mode),
// so it looks the same whatever base is on screen (before, a local base
// showed the default theme: another background than the servers').
func (c *Config) themeSource() *ServerProfile {
	if c.serverMode() == "multi" {
		return c.firstRemote()
	}
	a := c.active()
	if a != nil && a.IsLocal() {
		if r := c.firstRemote(); r != nil {
			return r
		}
	}
	return a
}

// The Desktop's own theme settings (Settings > Application > Appearance):
// "Theme in multi-server mode" (Config.MultiTheme) and "Theme
// of the local base" (Config.LocalTheme). Each one is "" (follow the
// servers: the rule above, the default) or one of the Obli suite's themes
// (themeCatalog). They never touch a single real server on screen: there the
// theme stays its account's.
//
// themeOverride: the setting that rules the theme now ("multi" in
// multi-server mode, "local" for a local base alone on screen, "" when a
// real server is on screen) and the theme it gives ("" : follow).
func (c *Config) themeOverride() (kind, theme string) {
	if c.serverMode() == "multi" {
		return "multi", normalizeTheme(c.MultiTheme)
	}
	if a := c.active(); a != nil && a.IsLocal() {
		return "local", normalizeTheme(c.LocalTheme)
	}
	return "", ""
}

// uiTheme: the theme the app shows now, for this config.
func (c *Config) uiTheme() string {
	if _, t := c.themeOverride(); t != "" {
		return t
	}
	if src := c.themeSource(); src != nil {
		if t := normalizeTheme(src.AccountTheme); t != "" {
			return t
		}
	}
	return defaultTheme
}

// ── The catalogue ───────────────────────────────────────────────────────────

// themeNames: the themes' names, as every Obli app shows them (Obligate's
// catalogue, shared/src/themes.ts THEME_CATALOG, which owns the ids; the same
// labels in Obliance's ThemePicker). Proper names: not translated.
//
// The Obli suite's themes are these five ids in every app (Obliance,
// Obligate, Obliview, Obliguard, Oblimap, Oblihub, ObliDesk, Obliwan,
// Obliplan, Obligarth; Oblifield has no Daylight): the Desktop offers them
// once each (by name), with Obliance's values (client/src/index.css), the
// app Oblireach belongs to. Some apps tint the neutrals of Modern / Neon
// towards their own hue: the same themes in another app's colours.
var themeNames = map[string]string{
	"obli-operator": "Obli Operator",
	"obli-daylight": "Obli Daylight",
	"obli-dim":      "Obli Dim",
	"modern":        "Modern UI",
	"neon":          "Neon UI",
}

// themeCatalogEntry: what the settings' list shows of a theme: its id, name,
// whether it is light, and its preview (page, surface, text, accent).
type themeCatalogEntry struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Light  bool      `json:"light"`
	Swatch [4]string `json:"swatch"`
}

// themeCatalog: every theme the settings offer, in knownThemes order, from
// themeTokens (the colours worn are the colours shown).
var themeCatalog = func() []themeCatalogEntry {
	out := make([]themeCatalogEntry, 0, len(knownThemes))
	for i, t := range knownThemes {
		out = append(out, themeCatalogEntry{ID: t, Name: themeNames[t], Light: themeTokenValue("color-scheme", i) == "light",
			Swatch: [4]string{themeTokenValue("--bg", i), themeTokenValue("--bg2", i), themeTokenValue("--text", i), themeTokenValue("--accent", i)}})
	}
	return out
}()

// themeCatalogJS: the catalogue for the page (ui.go buildUI: THEME_CATALOG).
func themeCatalogJS() string {
	b, _ := json.Marshal(themeCatalog)
	return string(b)
}

// ── The tokens ──────────────────────────────────────────────────────────────

// themeToken: one CSS declaration of every theme block. v: its value per
// theme, in knownThemes order. rgb: the block also declares <name>-rgb, the
// "R G B" triplet of that colour, for tints (rgb(var(--x-rgb) / .12)).
type themeToken struct {
	name string
	v    [5]string
	rgb  bool
}

func same(v string) [5]string { return [5]string{v, v, v, v, v} }

// themeTokens: the Desktop's variables, from Obliance's tokens (the name in
// the comment); "*": no Obliance token, a value chosen for the Desktop.
//
//	operator, daylight, dim, modern, neon
var themeTokens = []themeToken{
	{name: "color-scheme", v: [5]string{"dark", "light", "dark", "dark", "dark"}},
	// Surfaces.
	{name: "--bg", v: [5]string{"#0B0D1A", "#E5E9F0", "#2E3440", "#0E0B0C", "#07080A"}, rgb: true}, // bg-primary
	{name: "--bg2", v: [5]string{"#131728", "#ECEFF4", "#3B4252", "#161112", "#0D0E11"}},           // bg-secondary
	{name: "--bg3", v: [5]string{"#181C30", "#D8DEE9", "#434C5E", "#1E1819", "#131418"}},           // bg-tertiary
	{name: "--bg4", v: [5]string{"#1D2238", "#DEE3EC", "#465063", "#282021", "#1B1B20"}},           // bg-hover
	{name: "--chrome", v: [5]string{"#131728", "#ECEFF4", "#282D37", "#161112", "#0D0E11"}},        // bg-chrome (Dim), else bg-secondary: top bar, nav rail, title bars
	{name: "--border", v: [5]string{"#2A3048", "#D4DCE7", "#4C566A", "#3E3234", "#323339"}},        // border
	{name: "--border2", v: [5]string{"#3E4664", "#C5CEDB", "#5E6A82", "#4E4042", "#414249"}},       // border-light (* Daylight: darker than border, as a stronger line)
	// The fields (*): no border around them any more (borderless design), so
	// a step away from the cards (bg2)
	// they sit on: the page colour on the dark themes (as Obliance's own chat
	// input, bg-primary), bg-tertiary on Daylight (darker than its cards).
	{name: "--input-bg", v: [5]string{"#0B0D1A", "#D8DEE9", "#2E3440", "#0E0B0C", "#07080A"}},
	{name: "--input-border", v: same("var(--border)")}, // border
	// Depth of the cards, panels and filter bars (*), in place of their
	// border: a soft shadow, the way Obliance's Daylight gives depth ("depth
	// via shadow, no borders"): there the cards (#ECEFF4) are close to the
	// page (#E5E9F0); on the dark themes a faint one (the surface steps say
	// the rest).
	{name: "--surface-shadow", v: [5]string{"0 1px 3px rgba(0,0,0,.35)", "0 1px 3px rgba(46,52,64,.10),0 4px 14px rgba(46,52,64,.07)", "0 1px 3px rgba(20,23,30,.35)", "0 1px 3px rgba(0,0,0,.4)", "0 1px 3px rgba(0,0,0,.45)"}},
	// Text.
	{name: "--text", v: [5]string{"#F0F4FC", "#2E3440", "#ECEFF4", "#EBE4E4", "#F0EAE2"}},  // text-primary
	{name: "--text2", v: [5]string{"#B4BCD7", "#4C566A", "#D8DEE9", "#94888A", "#988A76"}}, // text-secondary
	// Daylight (*, the light theme's legibility pass): Obliance's
	// text-muted #818DA1 and status-paused #94A3B8 are 2.5:1 and 1.9:1 on
	// its fields (#D8DEE9): darker steps of the same slate, 3.8:1 and 3:1 or
	// more on every Daylight surface (text2 #4C566A stays above them).
	{name: "--muted", v: [5]string{"#828CAF", "#636E82", "#A3ABBC", "#706668", "#6A5E4E"}},  // text-muted
	{name: "--muted2", v: [5]string{"#4B5273", "#727E93", "#8A93A6", "#564D4F", "#725834"}}, // status-paused (* Modern, Daylight)
	// Status.
	{name: "--success", v: [5]string{"#1EDD8A", "#10A360", "#6CCB8F", "#2EA043", "#00DC6E"}, rgb: true}, // status-up
	{name: "--warn", v: [5]string{"#F59E0B", "#D97706", "#EBCB8B", "#D29922", "#FFBE00"}, rgb: true},    // status-warning (Modern / Neon: pending; * Operator)
	{name: "--danger", v: [5]string{"#EF4444", "#DC2626", "#FF6B6B", "#F85149", "#FF3860"}, rgb: true},  // status-critical (Modern / Neon: status-down; * Operator)
	{name: "--offline", v: [5]string{"#6B7399", "#94A3B8", "#8A93A6", "#706668", "#6A5E4E"}},            // status-down (Modern / Neon: text-muted)
	{name: "--info", v: [5]string{"#4F7BFF", "#2563EB", "#88A6E0", "#94A3B8", "#94A3B8"}, rgb: true},    // status-pending (Modern / Neon: maintenance)
	// Operating systems (*).
	{name: "--os-win", v: [5]string{"#60A5FA", "#2563EB", "#88A6E0", "#60A5FA", "#60A5FA"}, rgb: true},
	{name: "--os-linux", v: [5]string{"#4ADE80", "#15803D", "#6CCB8F", "#4ADE80", "#4ADE80"}, rgb: true},
	{name: "--os-mac", v: [5]string{"#FBBF24", "#B45309", "#EBCB8B", "#FBBF24", "#FBBF24"}, rgb: true},
	// The remote picture's letterbox and the minimap, the script output (*):
	// dark in every theme (a remote desktop is shown on it).
	{name: "--viewport-bg", v: [5]string{"#05060D", "#2E3440", "#242933", "#080607", "#030405"}},
	{name: "--term-bg", v: [5]string{"#05060D", "#2E3440", "#242933", "#080607", "#030405"}},
	{name: "--term-fg", v: [5]string{"#4ADE80", "#6CCB8F", "#6CCB8F", "#2EA043", "#00DC6E"}},
	// Overlays (*): the ink of hovers and faint tints (white on the dark
	// themes, Polar Night on Daylight), shadows, the backdrop of dialogs.
	{name: "--ink", v: [5]string{"255 255 255", "46 52 64", "255 255 255", "255 255 255", "255 255 255"}},
	{name: "--shadow", v: [5]string{"rgba(0,0,0,.5)", "rgba(46,52,64,.18)", "rgba(20,23,30,.45)", "rgba(0,0,0,.5)", "rgba(0,0,0,.55)"}},
	{name: "--scrim", v: [5]string{"rgba(0,0,0,.65)", "rgba(46,52,64,.40)", "rgba(20,23,30,.60)", "rgba(0,0,0,.65)", "rgba(0,0,0,.7)"}},
	// The accent: the app's own, never the server's (Obliance's value for
	// that theme; the Daylight one follows the brand red, where
	// Obliance's own Daylight block uses #DC2626).
	{name: "--accent", v: [5]string{"#E03A3A", "#E03A3A", "#E03A3A", "#C2001B", "#C2001B"}, rgb: true}, // accent: borders, focus, dots, progress
	{name: "--accent-fill", v: [5]string{"#C83232", "#C83232", "#C83232", "#C2001B", "#C2001B"}},       // filled buttons, bubbles, avatars, badges
	{name: "--accent-h", v: [5]string{"#B41E1E", "#B41E1E", "#B41E1E", "#960014", "#960014"}},          // accent-dark: a fill's hover
	// The accent used as TEXT: its own variable, the accent by default. Dim:
	// Obliance's lighter tint #FF6868 (contrast: #E03A3A text
	// is only 2.3:1 on the Dim cards, #FF6868 3.6:1), as in Obliance's web.
	{name: "--accent-text", v: [5]string{"var(--accent)", "var(--accent)", "#FF6868", "var(--accent)", "var(--accent)"}},
	{name: "--accent-l", v: same("var(--accent-text)")}, // the name the rules have always used for it
	{name: "--on-accent", v: same("#FFFFFF")},           // accent-text (Obliance): text on a fill
	// The server profiles' colours (*, servers.go serverPalette; multi-server
	// mode marks each server with its own: separators, tenant menu, chat tabs,
	// title-bar tabs): the pastel set on the dark themes (Dim: a lighter
	// indigo), deeper tones of the same hues on Daylight (3:1 or more on
	// every Daylight surface).
	{name: "--srv-violet", v: [5]string{"#A78BFA", "#7C3AED", "#A78BFA", "#A78BFA", "#A78BFA"}},
	{name: "--srv-teal", v: [5]string{"#2DD4BF", "#0F766E", "#2DD4BF", "#2DD4BF", "#2DD4BF"}},
	{name: "--srv-fuchsia", v: [5]string{"#E879F9", "#A21CAF", "#E879F9", "#E879F9", "#E879F9"}},
	{name: "--srv-indigo", v: [5]string{"#818CF8", "#4F46E5", "#99A3FA", "#818CF8", "#818CF8"}},
	{name: "--srv-cyan", v: [5]string{"#67E8F9", "#0E7490", "#67E8F9", "#67E8F9", "#67E8F9"}},
	{name: "--srv-sand", v: [5]string{"#D6B98C", "#8A6A2F", "#D6B98C", "#D6B98C", "#D6B98C"}},
	{name: "--srv-lavender", v: [5]string{"#C4B5FD", "#7E5BD6", "#C4B5FD", "#C4B5FD", "#C4B5FD"}},
	{name: "--srv-mint", v: [5]string{"#5EEAD4", "#047857", "#5EEAD4", "#5EEAD4", "#5EEAD4"}},
}

// themeIndex: the position of a known theme in knownThemes (-1: unknown).
func themeIndex(theme string) int {
	for i, t := range knownThemes {
		if t == theme {
			return i
		}
	}
	return -1
}

// hexRGB: 0xRRGGBB of "#RRGGBB".
func hexRGB(s string) (uint32, bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

func rgbTriplet(v uint32) string {
	return fmt.Sprintf("%d %d %d", v>>16&0xFF, v>>8&0xFF, v&0xFF)
}

// themeBlock: the declarations of one theme (every token, in table order).
func themeBlock(i int) string {
	var b strings.Builder
	for _, tk := range themeTokens {
		b.WriteString(tk.name + ":" + tk.v[i] + ";")
		if tk.rgb {
			if v, ok := hexRGB(tk.v[i]); ok {
				b.WriteString(tk.name + "-rgb:" + rgbTriplet(v) + ";")
			}
		}
	}
	return b.String()
}

// themeCSS: the page's theme rules (buildUI, a <style> before the others).
// :root wears Operator (the default); <body data-theme> the theme shown.
// Every block declares every token, derived ones included: a var() is
// resolved on the element that declares it, so a token declared on :root
// only would keep Operator's value under another theme.
var themeCSS = func() string {
	var b strings.Builder
	b.WriteString("\n") // (each block on its own line)
	for i, t := range knownThemes {
		sel := `[data-theme="` + t + `"]`
		if t == defaultTheme {
			sel = ":root," + sel
		}
		b.WriteString(sel + "{" + themeBlock(i) + "}\n")
	}
	// Daylight: the logo's white wordmark in the text colour.
	b.WriteString(`[data-theme="obli-daylight"] #login-logo .lcls-3,[data-theme="obli-daylight"] #topbar-logo .tcls-3{fill:var(--text)}` + "\n")
	// Neon: Obliance's glowing underline of the header, the active item's bar.
	b.WriteString(`[data-theme="neon"] .topbar{box-shadow:0 1px 0 rgb(var(--accent-rgb) / .55)}` + "\n")
	b.WriteString(`[data-theme="neon"] .nav-item.active{box-shadow:inset 3px 0 0 var(--accent)}` + "\n")
	// (right to left: the rail is on the right, the bar on its inner side)
	b.WriteString(`html[dir="rtl"] [data-theme="neon"] .nav-item.active{box-shadow:inset -3px 0 0 var(--accent)}` + "\n")
	return b.String()
}()

// ── Native colours ──────────────────────────────────────────────────────────

// themePalette: the colours the native side paints with (0xRRGGBB): the
// strip above a page without a title bar (frame_windows.go), the window's
// background until its page paints (webhost_windows.go), the SSO return
// pages (proxy.go).
type themePalette struct {
	Bg, Chrome, Text, Muted, Border, Danger, Info, Accent uint32
	Light                                                 bool
}

func themeTokenValue(name string, i int) string {
	for _, tk := range themeTokens {
		if tk.name == name {
			return tk.v[i]
		}
	}
	return ""
}

// themePalettes: every theme's palette, from themeTokens.
var themePalettes = func() map[string]themePalette {
	m := map[string]themePalette{}
	for i, t := range knownThemes {
		c := func(name string) uint32 { v, _ := hexRGB(themeTokenValue(name, i)); return v }
		m[t] = themePalette{
			Bg: c("--bg"), Chrome: c("--chrome"), Text: c("--text"), Muted: c("--muted"),
			Border: c("--border"), Danger: c("--danger"), Info: c("--info"), Accent: c("--accent"),
			Light: themeTokenValue("color-scheme", i) == "light",
		}
	}
	return m
}()

// paletteOf: a theme's palette (Operator's for an unknown theme).
func paletteOf(theme string) themePalette {
	if p, ok := themePalettes[theme]; ok {
		return p
	}
	return themePalettes[defaultTheme]
}

// The palette on screen now (set at start, then by Proxy.syncTheme): read by
// the native painting code on the UI thread, any goroutine may change it.
var curPalette atomic.Value // themePalette

func currentPalette() themePalette {
	if p, ok := curPalette.Load().(themePalette); ok {
		return p
	}
	return paletteOf(defaultTheme)
}

func setCurrentPalette(theme string) { curPalette.Store(paletteOf(theme)) }

// cssColor: "#rrggbb".
func cssColor(v uint32) string { return fmt.Sprintf("#%06x", v&0xFFFFFF) }

// colorRef: a Windows COLORREF (0x00BBGGRR) of 0xRRGGBB.
func colorRef(v uint32) uint32 { return (v&0xFF)<<16 | (v>>8&0xFF)<<8 | (v >> 16 & 0xFF) }

// blendRGB: a over b with alpha a (0..1), 0xRRGGBB.
func blendRGB(b, a uint32, alpha float64) uint32 {
	ch := func(s uint) uint32 {
		x, y := float64(b>>s&0xFF), float64(a>>s&0xFF)
		return uint32(x+(y-x)*alpha+0.5) & 0xFF
	}
	return ch(16)<<16 | ch(8)<<8 | ch(0)
}

// stripColors: the native strip's colours (0xRRGGBB), as the page draws its
// own title bar: the chrome, its text (muted) and glyphs (text), and the
// buttons' hover / pressed backgrounds (the ink over the chrome at 8% / 5%:
// white on the dark themes, the text colour on the light one). The close
// button keeps Windows' red.
func stripColors(p themePalette) (strip, title, glyph, hover, pressed uint32) {
	ink := uint32(0xFFFFFF)
	if p.Light {
		ink = p.Text
	}
	return p.Chrome, p.Muted, p.Text, blendRGB(p.Chrome, ink, .08), blendRGB(p.Chrome, ink, .05)
}

// ── Proxy side ──────────────────────────────────────────────────────────────

// setThemeHook: who is told when the app's theme changes (main.go: every
// window, bridges.broadcastTheme).
func (p *Proxy) setThemeHook(f func(theme string)) {
	p.themeMu.Lock()
	p.onTheme = f
	p.themeMu.Unlock()
}

// themeGen counts the theme changes (syncTheme): every answer and every
// window's notice carries it, so a page applies a theme only if it is not
// older than the one it has (two reports answered out of order, a notice
// and an answer crossing).
var themeGen atomic.Uint64

// themeNow: the theme the app wears and its change count, read together.
func (p *Proxy) themeNow() (string, uint64) {
	p.themeMu.Lock()
	defer p.themeMu.Unlock()
	t := p.theme
	if t == "" { // (a Proxy not made by newProxy)
		p.mu.RLock()
		t = p.cfg.uiTheme()
		p.mu.RUnlock()
	}
	return t, themeGen.Load()
}

// syncTheme: after anything that may change the theme (an account's theme
// reported, a server switch, a server added or removed, the server mode):
// the native palette follows and every window is told, once per change.
// Serialized (themeMu held throughout): two changes at once never leave the
// windows on the older one.
func (p *Proxy) syncTheme() {
	p.themeMu.Lock()
	defer p.themeMu.Unlock()
	p.mu.RLock()
	t := p.cfg.uiTheme()
	p.mu.RUnlock()
	// "" : not known yet (a Proxy not made by newProxy): noted, nothing told.
	changed := p.theme != "" && t != p.theme
	p.theme = t
	if !changed {
		return
	}
	setCurrentPalette(t)
	themeGen.Add(1) // (before the windows are told: their notice carries it)
	if p.onTheme != nil {
		p.onTheme(t)
	}
}

// themeState: what /local/theme answers.
type themeState struct {
	Theme   string `json:"theme"`   // the theme the app shows
	Source  string `json:"source"`  // the profile it comes from ("" none, or a setting chose it)
	Version uint64 `json:"version"` // its change count (themeGen): an older answer is ignored
	// The Desktop's settings (themeOverride): "" = follow the servers.
	MultiTheme string `json:"multiTheme"`
	LocalTheme string `json:"localTheme"`
	// Setting: the one that rules now ("multi", "local", "" none); Chosen:
	// it names a theme (the theme worn is that one).
	Setting string `json:"setting"`
	Chosen  bool   `json:"chosen"`
}

func (p *Proxy) themeState() themeState {
	var st themeState
	st.Theme, st.Version = p.themeNow()
	p.mu.RLock()
	kind, ov := p.cfg.themeOverride()
	st.Setting, st.Chosen = kind, ov != ""
	if src := p.cfg.themeSource(); src != nil && ov == "" {
		st.Source = src.ID
	}
	st.MultiTheme, st.LocalTheme = normalizeTheme(p.cfg.MultiTheme), normalizeTheme(p.cfg.LocalTheme)
	p.mu.RUnlock()
	return st
}

// themeSettingValue: a setting's value as asked ("" / "follow": follow the
// servers; else a theme id, trimmed, any case); ok false for anything else.
func themeSettingValue(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "follow" {
		return "", true
	}
	if t := normalizeTheme(s); t != "" {
		return t, true
	}
	return "", false
}

// /local/theme/settings  GET  → the same as /local/theme
//
//	POST { multiTheme?, localTheme? } → the same: each field given is
//	saved ("" / "follow", or a theme id; anything else: 400, nothing
//	saved); the app then wears what the rule gives now, every window
//	re-themed at once (syncTheme).
func (p *Proxy) handleThemeSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var body struct {
			MultiTheme *string `json:"multiTheme"`
			LocalTheme *string `json:"localTheme"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		var multi, local string
		ok := true
		if body.MultiTheme != nil {
			multi, ok = themeSettingValue(*body.MultiTheme)
		}
		if ok && body.LocalTheme != nil {
			local, ok = themeSettingValue(*body.LocalTheme)
		}
		if !ok {
			http.Error(w, `{"error":"unknown theme"}`, http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		changed := false
		if body.MultiTheme != nil && p.cfg.MultiTheme != multi {
			p.cfg.MultiTheme, changed = multi, true
		}
		if body.LocalTheme != nil && p.cfg.LocalTheme != local {
			p.cfg.LocalTheme, changed = local, true
		}
		if changed {
			saveConfig(p.cfg)
		}
		p.mu.Unlock()
		p.syncTheme()
	default:
		http.Error(w, `{"error":"GET or POST"}`, http.StatusMethodNotAllowed)
		return
	}
	_ = json.NewEncoder(w).Encode(p.themeState())
}

// /local/theme  GET  → { theme, source, version }
//
//	POST { profile, theme } → the same: the Obliance theme of that
//	profile's account (the active profile's when profile is ""), as the
//	page read it from /api/auth/me; kept for that server, the app then
//	wears the theme of its source profile.
func (p *Proxy) handleTheme(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var body struct {
			Profile string `json:"profile"`
			Theme   string `json:"theme"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		sp := p.cfg.active()
		if body.Profile != "" {
			sp = p.cfg.profileByID(body.Profile)
		}
		if sp == nil {
			p.mu.Unlock()
			http.Error(w, `{"error":"unknown server"}`, http.StatusNotFound)
			return
		}
		if t := normalizeTheme(body.Theme); sp.AccountTheme != t {
			sp.AccountTheme = t
			saveConfig(p.cfg)
		}
		p.mu.Unlock()
		p.syncTheme()
	default:
		http.Error(w, `{"error":"GET or POST"}`, http.StatusMethodNotAllowed)
		return
	}
	_ = json.NewEncoder(w).Encode(p.themeState())
}
