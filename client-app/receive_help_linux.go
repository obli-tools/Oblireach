//go:build linux

package main

// "Receive help" on Linux: the same backend as Windows (receive_help_link.go:
// supportlink + the engine in this process), the engine's Linux capture:
// under Wayland the xdg-desktop-portal (ScreenCast + RemoteDesktop: the
// desktop's own dialog, which screens and whether the helper may control),
// PipeWire, the portal's input; X11 elsewhere.
//
// What differs from Windows, said on the page (help_linux_page.go, the
// catalog's linux.* texts):
//   - the desktop asks its own permission at every session, unless the user
//     turned on "Remember the screen sharing permission" (Settings >
//     Support): the portal's restore token is then kept in the app's data
//     folder (portal-permission, 0600: it lets this app skip the desktop's
//     dialog, nothing else; the desktop can revoke it). Oblireach's own
//     consent (the code, the SAS, Accept) is never skipped;
//   - no screen curtain (no Wayland API hides the user's screen) and no
//     local input block;
//   - H.264 needs Cisco's OpenH264: inside the Flatpak the runtime's
//     extension (org.freedesktop.Platform.openh264) provides it; outside,
//     the user downloads Cisco's binary on demand (Settings > Support, with
//     Cisco's notice, as its license asks; engine.FetchOpenH264 checks the
//     pinned hashes). Without it the session falls back to JPEG.
//
// Settings: help-linux.json in the app's config folder (0600).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oblireach/agent/engine"
)

const (
	helpLinuxSettingsFile = "help-linux.json"
	helpPortalFile        = "portal-permission"
)

// helpLinuxSettings: what Settings > Support keeps on Linux.
type helpLinuxSettings struct {
	RememberPortal bool `json:"rememberScreenShare"`
}

type helpLinuxCfg struct {
	mu        sync.Mutex
	path      string // the settings file
	portal    string // the portal permission's file ("": none can be kept)
	fetching  bool
	fetchErr  string
	fetchFn   func(ctx context.Context, dir string) (string, error)
	installed func() bool
}

var helpLinux = newHelpLinuxCfg(platformPaths.configDir(), platformPaths.localDataDir())

func newHelpLinuxCfg(configDir, dataDir string) *helpLinuxCfg {
	c := &helpLinuxCfg{fetchFn: engine.FetchOpenH264, installed: engine.OpenH264Installed}
	if configDir != "" {
		c.path = filepath.Join(configDir, helpLinuxSettingsFile)
	}
	if dataDir != "" {
		c.portal = filepath.Join(dataDir, helpPortalFile)
	}
	return c
}

func (c *helpLinuxCfg) load() helpLinuxSettings {
	var s helpLinuxSettings
	if c.path == "" {
		return s
	}
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (c *helpLinuxCfg) save(s helpLinuxSettings) error {
	if c.path == "" {
		return errors.New("no config folder")
	}
	b, _ := json.Marshal(s)
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// portalStore: the store the engine gets (nil: the desktop asks every time).
func (c *helpLinuxCfg) portalStore() engine.PortalPermissionStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.portal == "" || !c.load().RememberPortal {
		return nil
	}
	return engine.NewFilePortalStore(c.portal)
}

// setRemember: the setting; turned off, the kept permission is forgotten.
func (c *helpLinuxCfg) setRemember(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.load()
	s.RememberPortal = on
	if err := c.save(s); err != nil {
		return err
	}
	if !on && c.portal != "" {
		if err := os.Remove(c.portal); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// helpLinuxView: what the page shows of it.
type helpLinuxView struct {
	RememberPortal bool   `json:"rememberScreenShare"`
	CanRemember    bool   `json:"canRemember"`
	InFlatpak      bool   `json:"inFlatpak"`
	Wayland        bool   `json:"wayland"`
	OpenH264       bool   `json:"openh264"`         // Cisco's binary is installed (ours, outside the Flatpak)
	CanFetch       bool   `json:"canFetchOpenh264"` // outside the Flatpak, a pinned build for this machine
	Fetching       bool   `json:"fetching"`
	FetchError     string `json:"fetchError,omitempty"`
	Notice         string `json:"notice"` // Cisco's attribution (never translated)
}

func (c *helpLinuxCfg) view() helpLinuxView {
	c.mu.Lock()
	defer c.mu.Unlock()
	fl := inFlatpak()
	inst := c.installed()
	return helpLinuxView{
		RememberPortal: c.load().RememberPortal, CanRemember: c.portal != "" && c.path != "",
		InFlatpak: fl, Wayland: os.Getenv("WAYLAND_DISPLAY") != "",
		OpenH264: inst, CanFetch: !fl && !inst, Fetching: c.fetching, FetchError: c.fetchErr,
		Notice: engine.OpenH264Notice,
	}
}

// fetch downloads Cisco's OpenH264 (the user asked, the notice on screen).
func (c *helpLinuxCfg) fetch() bool {
	c.mu.Lock()
	if c.fetching || inFlatpak() {
		c.mu.Unlock()
		return false
	}
	c.fetching, c.fetchErr = true, ""
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, err := c.fetchFn(ctx, "")
		c.mu.Lock()
		c.fetching = false
		if err != nil {
			c.fetchErr = "failed"
		}
		c.mu.Unlock()
	}()
	return true
}

// serveHTTP: help/linux (GET the view; POST {rememberScreenShare}) and
// help/openh264 (POST: the download).
func (c *helpLinuxCfg) serveHTTP(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == "linux" && r.Method == http.MethodGet:
		supportJSON(w, http.StatusOK, c.view())
	case path == "linux" && r.Method == http.MethodPost:
		var in struct {
			Remember bool `json:"rememberScreenShare"`
		}
		if !supportDecode(w, r, &in) {
			return true
		}
		if err := c.setRemember(in.Remember); err != nil {
			supportJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage"})
			return true
		}
		supportJSON(w, http.StatusOK, c.view())
	case path == "openh264" && r.Method == http.MethodPost:
		c.fetch()
		supportJSON(w, http.StatusOK, c.view())
	default:
		return false
	}
	return true
}

func init() {
	helpPortalPermission = func() engine.PortalPermissionStore { return helpLinux.portalStore() }
	helpPlatformHTTP = func(w http.ResponseWriter, r *http.Request, path string) bool { return helpLinux.serveHTTP(w, r, path) }
}

// newAppHelpHost: the app's "Receive help" (support.go appSupportHub).
func newAppHelpHost(settings *supportSettings) *helpHost {
	h := newHelpHost(settings, &helpLinkBackend{version: appVersion})
	h.onSession = helpOnSession
	h.onAttention = func(string) { helpAttention() }
	return h
}

// helpOnSession: while a session runs the password vault is locked and held
// locked (no code, no fingerprint: the operator drives this computer).
func helpOnSession(on bool) {
	if v, err := getAppVault(); err == nil {
		v.SetHeld(on)
	}
}

// helpAttention: an operator asks to connect: a desktop notification (the
// window comes forward through its UI host when it has one).
func helpAttention() {
	desktopNotify("help-ask", tr("help.notify.askTitle"), tr("help.notify.askText"))
}

// startIncomingSupport: at the app's start, "Receive help" runs in the
// background while "Allow incoming support" is on (single instance).
func startIncomingSupport() {
	go appSupportHub().help.autoStart()
}

// stopIncomingSupport: the app quits: the registration closes.
func stopIncomingSupport() {
	if h := appSupportHub().help; h != nil {
		h.stop()
	}
}
