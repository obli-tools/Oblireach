//go:build windows

package main

// The Windows side of the themes (theme.go): a new theme reaches every
// window (native_events.go emitTheme), its page (window.__reachOnTheme) and
// its native colours (paletteHost: the WebView2's background until the page
// paints, the strip above a page without a title bar, the background of the
// window).

import (
	"github.com/jchv/go-webview2/pkg/edge"
)

// (emitTheme, the theme switch told to a window, and paletteHost:
// native_events.go.)

var _ paletteHost = hostFrame{}

func (f hostFrame) applyPalette(p themePalette) { f.h.applyPalette(p) }

// applyPalette: the WebView2's own background (seen until the page paints,
// and on a page that paints none) and the native strip, repainted (UI thread).
func (h *webHost) applyPalette(p themePalette) {
	if h.finished {
		return
	}
	if h.ready && h.chromium != nil {
		putWebViewBackground(h.chromium, p.Bg)
	}
	h.invalidate()
}

// putWebViewBackground: ICoreWebView2Controller2::put_DefaultBackgroundColor,
// opaque (0xRRGGBB).
func putWebViewBackground(c *edge.Chromium, rgb uint32) {
	ctrl := c.GetController()
	if ctrl == nil {
		return
	}
	if c2 := ctrl.GetICoreWebView2Controller2(); c2 != nil {
		_ = c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: uint8(rgb >> 16), G: uint8(rgb >> 8), B: uint8(rgb)})
	}
}
