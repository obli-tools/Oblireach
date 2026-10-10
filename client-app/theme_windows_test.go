//go:build windows

package main

import (
	"regexp"
	"strings"
	"testing"
)

// paletteFrame: a window's frame that records the palettes it was given.
type paletteFrame struct {
	fakeFrame
	palettes []themePalette
}

func (f *paletteFrame) applyPalette(p themePalette) { f.palettes = append(f.palettes, p) }

// A new theme reaches every window of the app (main window, session
// windows): its page (__reachOnTheme with the theme, behind the origin
// check) then its native colours, on the window's UI thread. A bridge
// without the capability is skipped; a window gone is told nothing.
func TestBroadcastTheme(t *testing.T) {
	r := newBridgeRegistry(func() uintptr { return 0 })
	h1, h2 := &fakeHost{}, &fakeHost{}
	b1, _ := newNativeBridge(h1, "http://127.0.0.1:4321")
	b2, _ := newNativeBridge(h2, "http://127.0.0.1:4321")
	f1 := &paletteFrame{}
	b1.win = f1
	r.register(0x10, b1)
	r.register(0x20, b2) // no frame (b.win nil): the page only
	r.register(0x30, fakeKeyOnly{})
	r.broadcastTheme("obli-dim")
	for i, h := range []*fakeHost{h1, h2} {
		if len(h.dispatched) != 1 {
			t.Fatalf("window %d: %d calls dispatched, want 1 (on its UI thread)", i, len(h.dispatched))
		}
		h.dispatched[0]()
		// (with the change count: the page ignores an older theme)
		if len(h.evals) != 1 || !strings.Contains(h.evals[0], `window["__reachOnTheme"]`) || !regexp.MustCompile(`f\("obli-dim",\d+\)`).MatchString(h.evals[0]) ||
			!strings.Contains(h.evals[0], `location.origin !== "http://127.0.0.1:4321"`) {
			t.Fatalf("window %d: %v", i, h.evals)
		}
	}
	if len(f1.palettes) != 1 || f1.palettes[0] != paletteOf("obli-dim") {
		t.Fatalf("native colours: %+v", f1.palettes)
	}
	// A window already gone: nothing evaluated, nothing painted.
	b1.shutdown()
	h1.evals, h1.dispatched = nil, nil
	r.broadcastTheme("obli-daylight")
	for _, f := range h1.dispatched {
		f()
	}
	if len(h1.evals) != 0 || len(f1.palettes) != 1 {
		t.Fatalf("closed window told: %v %d", h1.evals, len(f1.palettes))
	}
}

// The servers changed (servers.go notifyServers): every window is told
// (__reachOnServers, behind the origin check, on its UI thread); a bridge
// without the capability is skipped; a window gone is told nothing.
func TestBroadcastServers(t *testing.T) {
	r := newBridgeRegistry(func() uintptr { return 0 })
	h1, h2 := &fakeHost{}, &fakeHost{}
	b1, _ := newNativeBridge(h1, "http://127.0.0.1:4321")
	b2, _ := newNativeBridge(h2, "http://127.0.0.1:4321")
	r.register(0x10, b1)
	r.register(0x20, b2)
	r.register(0x30, fakeKeyOnly{})
	r.broadcastServers()
	for i, h := range []*fakeHost{h1, h2} {
		if len(h.dispatched) != 1 {
			t.Fatalf("window %d: %d calls dispatched, want 1 (on its UI thread)", i, len(h.dispatched))
		}
		h.dispatched[0]()
		if len(h.evals) != 1 || !strings.Contains(h.evals[0], `window["__reachOnServers"]`) || !strings.Contains(h.evals[0], `f()`) ||
			!strings.Contains(h.evals[0], `location.origin !== "http://127.0.0.1:4321"`) {
			t.Fatalf("window %d: %v", i, h.evals)
		}
	}
	b1.shutdown()
	h1.evals, h1.dispatched = nil, nil
	r.broadcastServers()
	for _, f := range h1.dispatched {
		f()
	}
	if len(h1.evals) != 0 {
		t.Fatalf("closed window told: %v", h1.evals)
	}
}
