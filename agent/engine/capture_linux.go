//go:build linux

package engine

// Linux capture: two backends behind the engine's capture functions.
//
//   - portal (Wayland, the default when a Wayland session is detected):
//     xdg-desktop-portal ScreenCast (+ RemoteDesktop for the input) over the
//     session D-Bus, frames from PipeWire (capture_portal_linux.go,
//     portal_linux.go, pipewire_linux.go). Works unprivileged and inside a
//     Flatpak sandbox: no root, no /dev/uinput, no X server.
//   - x11: XGetImage + XTest (capture_x11_linux.go, input_x11_linux.go), the
//     historical path, kept for X11 sessions (and the agent run as a service
//     with DISPLAY set). Under Wayland it would only see XWayland clients.
//
// OBLIREACH_CAPTURE=portal|x11|auto (or SetLinuxCaptureBackend) forces one.
// A portal session is either held by RunSession for its whole life
// (session_linux.go: the desktop's dialog is shown once per session, a
// monitor switch reuses the streams) or, for a bare StreamSession, opened by
// the capture itself and closed with it.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

type MonitorInfo struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Primary  bool   `json:"primary,omitempty"`
	DPI      int    `json:"dpi,omitempty"`
	Scale    int    `json:"scale,omitempty"`
	Rotation int    `json:"rotation,omitempty"`
}

// linuxCapture is one capture backend.
type linuxCapture interface {
	name() string
	monitors() []MonitorInfo
	initMonitor(idx int) error
	offset() (x, y int)
	size() (w, h int)
	frame(buf []byte) (w, h int, err error)
	close()
}

const (
	linuxBackendAuto   = "auto"
	linuxBackendPortal = "portal"
	linuxBackendX11    = "x11"
)

var linuxBackendPref struct {
	mu   sync.Mutex
	name string
}

// SetLinuxCaptureBackend forces the Linux capture backend: "portal",
// "x11" or "auto" (the default). The OBLIREACH_CAPTURE environment variable
// wins over it (diagnostics).
func SetLinuxCaptureBackend(name string) {
	linuxBackendPref.mu.Lock()
	linuxBackendPref.name = name
	linuxBackendPref.mu.Unlock()
}

// chooseLinuxBackend resolves the backend from the host preference and the
// environment (pure: getenv is os.Getenv outside tests).
func chooseLinuxBackend(pref string, getenv func(string) string) string {
	norm := func(s string) string {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "portal", "wayland", "pipewire":
			return linuxBackendPortal
		case "x11", "xorg", "x":
			return linuxBackendX11
		}
		return ""
	}
	if b := norm(getenv("OBLIREACH_CAPTURE")); b != "" {
		return b
	}
	if b := norm(pref); b != "" {
		return b
	}
	if getenv("WAYLAND_DISPLAY") != "" || strings.EqualFold(getenv("XDG_SESSION_TYPE"), "wayland") {
		return linuxBackendPortal
	}
	if getenv("DISPLAY") != "" {
		return linuxBackendX11
	}
	// Neither: a sandbox or a service without a display. The portal is the
	// only path that can work there (it needs the session bus).
	return linuxBackendPortal
}

func linuxCaptureBackend() string {
	linuxBackendPref.mu.Lock()
	pref := linuxBackendPref.name
	linuxBackendPref.mu.Unlock()
	return chooseLinuxBackend(pref, os.Getenv)
}

// linuxCap is the active capture (one per process, like every platform).
var linuxCap struct {
	mu          sync.Mutex
	cur         linuxCapture
	portal      *portalSession // the session the capture uses (nil: X11)
	portalOwned bool           // opened by the capture itself (closed by captureClose)
}

// attachPortalSession hands a RunSession-held portal session to the capture.
func attachPortalSession(ps *portalSession) {
	linuxCap.mu.Lock()
	linuxCap.portal = ps
	linuxCap.portalOwned = false
	linuxCap.mu.Unlock()
}

// detachPortalSession forgets ps (RunSession ends; it closes ps itself).
func detachPortalSession(ps *portalSession) {
	linuxCap.mu.Lock()
	if linuxCap.portal == ps {
		if linuxCap.cur != nil {
			linuxCap.cur.close()
			linuxCap.cur = nil
		}
		linuxCap.portal = nil
		linuxCap.portalOwned = false
	}
	linuxCap.mu.Unlock()
	setLinuxInput(nil)
}

// linuxCaptureOverride (tests only): replaces the backends with a fake.
var linuxCaptureOverride func() (linuxCapture, linuxInput)

// errNoSessionBus: the portal backend cannot reach a session D-Bus.
var errNoSessionBus = errors.New("no session D-Bus (the screen sharing portal needs the user's session)")

func captureInit() error { return captureInitMonitor(0) }

func captureInitMonitor(idx int) error {
	linuxCap.mu.Lock()
	defer linuxCap.mu.Unlock()
	if linuxCap.cur != nil {
		linuxCap.cur.close()
		linuxCap.cur = nil
	}
	if linuxCaptureOverride != nil {
		c, in := linuxCaptureOverride()
		if err := c.initMonitor(idx); err != nil {
			return err
		}
		linuxCap.cur = c
		setLinuxInput(in)
		return nil
	}
	backend := linuxCaptureBackend()
	if backend == linuxBackendPortal {
		ps := linuxCap.portal
		if ps == nil {
			// A bare StreamSession (no RunSession around it): the capture
			// opens the portal session itself and closes it with the stream.
			var err error
			ps, err = openPortalSessionDefault(portalConfig{input: true})
			if err != nil {
				if errors.Is(err, errNoSessionBus) && os.Getenv("DISPLAY") != "" &&
					strings.TrimSpace(os.Getenv("OBLIREACH_CAPTURE")) == "" {
					log.Printf("capture: portal unavailable (%v), falling back to X11", err)
					backend = linuxBackendX11
				} else {
					return err
				}
			} else {
				linuxCap.portal = ps
				linuxCap.portalOwned = true
			}
		}
		if backend == linuxBackendPortal {
			pc := newPortalCapture(ps)
			if err := pc.initMonitor(idx); err != nil {
				return err
			}
			linuxCap.cur = pc
			setLinuxInput(newPortalInput(ps, pc))
			return nil
		}
	}
	c := newX11Capture()
	if err := c.initMonitor(idx); err != nil {
		return err
	}
	linuxCap.cur = c
	setLinuxInput(nil) // the X11 input
	return nil
}

func captureClose() {
	linuxCap.mu.Lock()
	cur := linuxCap.cur
	linuxCap.cur = nil
	var owned *portalSession
	if linuxCap.portalOwned {
		owned = linuxCap.portal
		linuxCap.portal = nil
		linuxCap.portalOwned = false
	}
	linuxCap.mu.Unlock()
	if cur != nil {
		cur.close()
	}
	if owned != nil {
		setLinuxInput(nil)
		owned.close()
	}
}

func enumerateMonitors() []MonitorInfo {
	linuxCap.mu.Lock()
	cur, ps := linuxCap.cur, linuxCap.portal
	linuxCap.mu.Unlock()
	if cur != nil {
		return cur.monitors()
	}
	if ps != nil {
		return portalMonitors(ps.streams, nil)
	}
	if linuxCaptureBackend() == linuxBackendX11 {
		return newX11Capture().monitors()
	}
	return nil
}

func captureMonitorOffset() (x, y int) {
	linuxCap.mu.Lock()
	defer linuxCap.mu.Unlock()
	if linuxCap.cur == nil {
		return 0, 0
	}
	return linuxCap.cur.offset()
}

func captureWidth() int {
	linuxCap.mu.Lock()
	defer linuxCap.mu.Unlock()
	if linuxCap.cur == nil {
		return 0
	}
	w, _ := linuxCap.cur.size()
	return w
}

func captureHeight() int {
	linuxCap.mu.Lock()
	defer linuxCap.mu.Unlock()
	if linuxCap.cur == nil {
		return 0
	}
	_, h := linuxCap.cur.size()
	return h
}

func captureFrame(buf []byte) (width, height int, err error) {
	linuxCap.mu.Lock()
	cur := linuxCap.cur
	linuxCap.mu.Unlock()
	if cur == nil {
		return 0, 0, fmt.Errorf("capture not initialised")
	}
	return cur.frame(buf)
}
