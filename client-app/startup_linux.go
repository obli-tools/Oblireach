//go:build linux

package main

// startup_linux.go — "Start with the session" and "Start minimized",
// Settings > Application, on Linux (autostartHost, platform.go).
//
//   - in the Flatpak: the Background portal (RequestBackground with
//     autostart and the command line): the portal writes, or removes, the
//     user's ~/.config/autostart/tools.obli.Oblireach.desktop itself (a
//     sandboxed app may not) and may ask the user once ("let it run in the
//     background?"). A refusal is reported as "off";
//   - outside a sandbox (a development build): that same desktop file,
//     written here (Exec = this program, --minimized when chosen).
//
// The state shown is the desktop file's: present and not disabled
// (Hidden=true or X-GNOME-Autostart-enabled=false, what the desktops'
// autostart settings write) = on; present but disabled = "turned off
// elsewhere" (the Windows Task Manager case).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
)

const portalBackground = "org.freedesktop.portal.Background"

var errBadArgument = errors.New("invalid argument")

// autostartBackend: what writes the autostart entry (a fake in the tests).
type autostartBackend interface {
	set(on bool, commandline []string) (bool, error)
}

// portalAutostart: the Background portal.
type portalAutostart struct{}

func (portalAutostart) set(on bool, commandline []string) (bool, error) {
	opts := map[string]dbus.Variant{
		"reason":           dbus.MakeVariant(tr("startup.backgroundReason")),
		"autostart":        dbus.MakeVariant(on),
		"commandline":      dbus.MakeVariant(commandline),
		"dbus-activatable": dbus.MakeVariant(false),
	}
	res, err := portalCall(portalBackground, "RequestBackground", portalInteractiveTimeout, opts, portalParentWindow(0))
	if err != nil {
		if errors.Is(err, errPortalCancel) || errors.Is(err, errPortalFailed) {
			return false, nil // (refused by the user)
		}
		return false, err
	}
	if v, ok := res["autostart"]; ok {
		b, _ := v.Value().(bool)
		return b, nil
	}
	return on, nil
}

// fileAutostart: the desktop file written directly (no sandbox).
type fileAutostart struct{ path, exe string }

func desktopExecQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`%") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`, "%", "%%")
	return `"` + r.Replace(s) + `"`
}

func (f fileAutostart) set(on bool, commandline []string) (bool, error) {
	if !on {
		if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return true, err
		}
		return false, nil
	}
	parts := []string{desktopExecQuote(f.exe)}
	for _, a := range commandline[1:] {
		parts = append(parts, desktopExecQuote(a))
	}
	content := "[Desktop Entry]\nType=Application\nName=Oblireach\nIcon=" + ssAppID + "\nExec=" + strings.Join(parts, " ") +
		"\nX-GNOME-Autostart-enabled=true\nX-KDE-autostart-phase=2\n"
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return false, err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// autostartFile: the user's autostart entry of this app.
func autostartFile() string {
	return filepath.Join(hostConfigHome(), "autostart", ssAppID+".desktop")
}

// readAutostart: present, enabled, and whether --minimized is on its Exec.
func readAutostart(path string) (present, enabled, minimized bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false, false
	}
	enabled = true
	inEntry := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Hidden":
			if strings.EqualFold(strings.TrimSpace(v), "true") {
				enabled = false
			}
		case "X-GNOME-Autostart-enabled":
			if strings.EqualFold(strings.TrimSpace(v), "false") {
				enabled = false
			}
		case "Exec":
			minimized = strings.Contains(v, startMinimizedArg)
		}
	}
	return true, enabled, minimized
}

// startupCtl: the start-up settings of the main window.
type startupCtl struct {
	exe      string
	file     string
	backend  autostartBackend
	prefs    func() (minimized, always bool)
	setPrefs func(minimized, always bool)
	tray     func() bool
	setTray  func(on bool)
	onTray   func(on bool)
}

func newStartupCtl(p *Proxy) *startupCtl {
	exe, _ := os.Executable()
	c := &startupCtl{exe: exe, file: autostartFile(), prefs: p.startupPrefs, setPrefs: p.setStartupPrefs,
		tray: p.trayPref, setTray: p.setTrayPref}
	if inFlatpak() {
		c.backend = portalAutostart{}
	} else {
		c.backend = fileAutostart{path: c.file, exe: exe}
	}
	return c
}

// commandline: what the session starts (the Flatpak's own command inside
// it: the portal wraps it in "flatpak run").
func (c *startupCtl) commandline(minimized bool) []string {
	cmd := []string{"oblireach"}
	if !inFlatpak() && c.exe != "" {
		cmd[0] = c.exe
	}
	if minimized {
		cmd = append(cmd, startMinimizedArg)
	}
	return cmd
}

func (c *startupCtl) state() (startupState, error) {
	minimized, always := c.prefs()
	st := startupState{Minimized: minimized, MinimizedAlways: always, Exe: c.exe}
	if c.tray != nil {
		st.Tray = c.tray()
	}
	present, enabled, _ := readAutostart(c.file)
	if present {
		st.RunAtLogin = enabled
		st.DisabledInTM = !enabled
	}
	return st, nil
}

func (c *startupCtl) set(what string, on bool) (startupState, error) {
	minimized, always := c.prefs()
	switch what {
	case "runAtLogin":
		if _, err := c.backend.set(on, c.commandline(minimized)); err != nil {
			return c.stateOr(err)
		}
	case "minimized":
		c.setPrefs(on, always)
		if present, enabled, _ := readAutostart(c.file); present && enabled {
			if _, err := c.backend.set(true, c.commandline(on)); err != nil {
				return c.stateOr(err)
			}
		}
	case "minimizedAlways":
		c.setPrefs(minimized, on)
	case "tray":
		if c.setTray == nil {
			return startupState{}, errBadArgument
		}
		c.setTray(on)
		if c.onTray != nil {
			c.onTray(on)
		}
	default:
		return startupState{}, errBadArgument
	}
	return c.state()
}

func (c *startupCtl) stateOr(err error) (startupState, error) {
	st, _ := c.state()
	return st, err
}
