//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

// hardenWebView tightens the WebView2 that hosts the app once it is embedded,
// for the password vault (threat model: someone at the
// keyboard, or an XSS in server-supplied data):
//
//   - DevTools (F12) is disabled unless OBLIREACH_DEVTOOLS=1 is set for
//     debugging. The vault management bindings live in the main window and the
//     "type a saved secret" path is gated on the vault being unlocked; DevTools
//     would still be an unnecessary foothold for anyone at the machine.
//   - Password autosave and general autofill are turned off: the app is a
//     remote-session console, never a place for the browser to offer to save or
//     fill the operator's own credentials.
//
// DevTools has a Go wrapper. Autosave / autofill live on ICoreWebView2Settings3
// / Settings4, whose setters go-webview2 declares in the merged settings vtable
// but does not wrap; they are called by vtable slot, the same pattern
// putControllerBounds / closeController use for methods the package leaves
// unexposed. Every step is best-effort and never fails the embed.
func hardenWebView(c *edge.Chromium) {
	settings, err := c.GetSettings()
	if err != nil || settings == nil {
		return
	}
	_ = settings.PutAreDevToolsEnabled(os.Getenv("OBLIREACH_DEVTOOLS") == "1")

	// Merged ICoreWebView2Settings vtable (edge/ICoreWebViewSettings.go):
	// IUnknown (0-2), then get/put pairs. PutIsPasswordAutosaveEnabled is slot
	// 26, PutIsGeneralAutofillEnabled slot 28.
	const (
		slotPutPasswordAutosave = 26
		slotPutGeneralAutofill  = 28
	)
	vtbl := *(**[33]uintptr)(unsafe.Pointer(settings))
	if vtbl == nil {
		return
	}
	this := uintptr(unsafe.Pointer(settings))
	// BOOL false = 0 (autosave off, autofill off).
	syscall.SyscallN(vtbl[slotPutPasswordAutosave], this, 0)
	syscall.SyscallN(vtbl[slotPutGeneralAutofill], this, 0)
}
