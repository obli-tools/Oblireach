package main

// native_events.go — what the app tells a window's page on its own (the
// platform-neutral glue of the native bridge, moved unchanged from
// i18n_windows.go, theme_windows.go, keyhook_windows.go and
// detached_windows.go): a new language, a new theme, the servers changed, a
// captured system key; each on the window's UI thread, behind the local UI
// origin check (evalMain, evalKey).

import (
	"encoding/json"
	"strconv"
)

// emitLang tells this window's page the app's language changed (i18n.go
// syncLang, through the bridge registry): it redraws itself in it.
func (b *nativeBridge) emitLang(code string) {
	j, _ := json.Marshal(code)
	b.w.Dispatch(func() { b.evalMain("__reachOnLang", string(j)) })
}

// emitTheme tells this window the app's theme changed (theme.go syncTheme,
// through the bridge registry): on its UI thread, its page wears it, then
// its native colours follow.
// The notice carries the change count (theme.go themeGen, read now: syncTheme
// holds its lock while every window is told).
func (b *nativeBridge) emitTheme(theme string) {
	j, _ := json.Marshal(theme)
	args := string(j) + "," + strconv.FormatUint(themeGen.Load(), 10)
	pal := paletteOf(theme)
	b.w.Dispatch(func() {
		if b.closed.Load() {
			return
		}
		b.evalMain("__reachOnTheme", args)
		if ph, ok := b.win.(paletteHost); ok {
			ph.applyPalette(pal)
		}
	})
}

// paletteHost: a window whose native colours follow the theme (hostFrame,
// theme_windows.go).
type paletteHost interface {
	applyPalette(p themePalette)
}

// emitServers tells this window the servers changed (window_registry.go
// broadcastServers): on its UI thread, window.__reachOnServers().
func (b *nativeBridge) emitServers() {
	b.w.Dispatch(func() {
		if b.closed.Load() {
			return
		}
		b.evalMain("__reachOnServers", "")
	})
}

// evalMain calls window.<fn>(<args>) in this bridge's page if it is the local
// UI (UI thread).
func (b *nativeBridge) evalMain(fn, args string) {
	if b.closed.Load() {
		return
	}
	origin, _ := json.Marshal(b.origin)
	name, _ := json.Marshal(fn)
	b.w.Eval("(function () { if (location.origin !== " + string(origin) + ") return; " +
		"var f = window[" + string(name) + "]; if (typeof f === 'function') f(" + args + "); })();")
}

// keyCaptureOn (window_registry.go): this window's page captures the key.
func (b *nativeBridge) keyCaptureOn() bool { return b.capture.Load() }

// emitKey (hook thread) hands a Windows key to this window's page, on its UI
// thread. A closed window gets nothing (the release of a press it got is
// still swallowed: the local shell never saw that press).
func (b *nativeBridge) emitKey(down bool, code string) {
	if b.closed.Load() {
		return
	}
	b.w.Dispatch(func() { b.evalKey(down, code) })
}

// evalKey hands a Windows key to the page, only if the document on screen is
// the local UI (the webview may be showing the SSO page).
func (b *nativeBridge) evalKey(down bool, code string) {
	if b.closed.Load() {
		return
	}
	origin, _ := json.Marshal(b.origin)
	c, _ := json.Marshal(code)
	b.w.Eval("(function () { if (location.origin !== " + string(origin) + ") return; " +
		"var f = window.__reachNativeKey; if (typeof f === 'function') f(" + strconv.FormatBool(down) + ", " + string(c) + "); })();")
}
