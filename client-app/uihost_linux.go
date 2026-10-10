//go:build linux

package main

// uihost_linux.go — the Linux UI host (uiHost, platform.go): not implemented
// yet. Phase 2 of the Linux port: GTK4 + WebKitGTK 6.0 through a small cgo
// shim (one GtkApplicationWindow + WebKitWebView per window, the page's
// window.external.invoke wired to a script message handler, Init as a
// WebKitUserScript, Dispatch through g_main_context_invoke), or an
// Electron-style host behind this same interface if WebKitGTK fails the
// engine gate (live H.264 decoding through WebCodecs).

type linuxUI struct{}

var platformUI uiHost = linuxUI{}

func (linuxUI) prepare() {}

func (linuxUI) run(uiStart) (*localFS, error) {
	return nil, notImplemented("UI host (GTK4/WebKitGTK windows)")
}
