package main

// The terminal component of the remote shells (terminal_viewer.go): xterm.js
// 5.3.0 and its fit addon 0.8.0, the versions the Obliance web uses
// (client/package.json), vendored next to socket.io.min.js with their MIT
// notices in a banner at the top of each file. They hold '%' and backticks:
// never in the Sprintf page nor in a Go raw string, but embedded as files and
// loaded by the page on first use (toolsLoadXterm). Served behind the same
// guard as every local route (ServeHTTP: the per-run secret cookie, a local
// Origin).

import (
	_ "embed"
	"net/http"
)

//go:embed xterm.min.js
var xtermJS []byte

//go:embed xterm-addon-fit.min.js
var xtermFitJS []byte

//go:embed xterm.css
var xtermCSS []byte

func serveLocalAsset(w http.ResponseWriter, ctype string, body []byte) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

// handleXtermJS serves /local/xterm.js (window.Terminal).
func (p *Proxy) handleXtermJS(w http.ResponseWriter, _ *http.Request) {
	serveLocalAsset(w, "application/javascript; charset=utf-8", xtermJS)
}

// handleXtermFitJS serves /local/xterm-addon-fit.js (window.FitAddon.FitAddon).
func (p *Proxy) handleXtermFitJS(w http.ResponseWriter, _ *http.Request) {
	serveLocalAsset(w, "application/javascript; charset=utf-8", xtermFitJS)
}

// handleXtermCSS serves /local/xterm.css.
func (p *Proxy) handleXtermCSS(w http.ResponseWriter, _ *http.Request) {
	serveLocalAsset(w, "text/css; charset=utf-8", xtermCSS)
}
