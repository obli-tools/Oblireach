//go:build linux

package main

// app_linux.go — the main window's application services on Linux
// (appBindings, trayHost, autostartHost, appUpdater, updateHost:
// platform.go):
//
//   - the notification-area icon (tray_windows.go): phase 3,
//     StatusNotifierItem on D-Bus; its notifications are already the
//     Notification portal's (notify_linux.go);
//   - start with the session: the Background portal (startup_linux.go);
//   - the update: Flatpak (the system's automatic updates, and the Flatpak
//     portal's UpdateMonitor for the one-click update: updater_linux.go).

import (
	"strconv"
	"time"
)

// ── updateHost ─────────────────────────────────────────────────────────────

type linuxUpdates struct{}

var platformUpdates updateHost = linuxUpdates{}

func (linuxUpdates) finishPending(*Config, string, time.Time) *updateResult { return nil }

func (linuxUpdates) sweep(string, time.Time) {}

// ── appUpdater (desktopUpdater: updater_linux.go) ──────────────────────────

func (b *nativeBridge) installUpdate(tok string) (updateStatus, error) {
	if !b.allowed(tok) || b.updater == nil {
		return updateStatus{}, errNotAllowed
	}
	return b.updater.start(), nil
}

func (b *nativeBridge) updateState(tok string) (updateStatus, error) {
	if !b.allowed(tok) || b.updater == nil {
		return updateStatus{}, errNotAllowed
	}
	return b.updater.state(), nil
}

// ── autostartHost (startupCtl: startup_linux.go) ───────────────────────────

func (b *nativeBridge) startupGet(tok string) (startupState, error) {
	if !b.allowed(tok) || b.startup == nil {
		return startupState{}, errNotAllowed
	}
	return b.startup.state()
}

func (b *nativeBridge) startupSet(tok, what string, on bool) (startupState, error) {
	if !b.allowed(tok) || b.startup == nil {
		return startupState{}, errNotAllowed
	}
	return b.startup.set(what, on)
}

// ── trayHost ───────────────────────────────────────────────────────────────

// appTray: the main window's notification-area icon (none yet: the
// StatusNotifierItem comes with the UI host) and its notifications, which
// already go through the Notification portal (notify_linux.go).
type appTray struct {
	// inFront: the main window is on screen and active (set by the UI
	// host; nil: unknown, every balloon is shown).
	inFront func() bool
	seq     int
}

func (*appTray) enable(bool)   {}
func (*appTray) hide()         {}
func (*appTray) restore()      {}
func (*appTray) active() bool  { return false }
func (*appTray) setUnread(int) {}
func (*appTray) remove()       {}

// balloon: a desktop notification, unless the main window is in front (the
// operator sees the chat already), as Windows shows its balloon only for a
// window hidden in the notification area.
func (t *appTray) balloon(title, text string) bool {
	if t.inFront != nil && t.inFront() {
		return false
	}
	title, text = trayNoteText(title, text)
	t.seq++
	return desktopNotify("chat-"+strconv.Itoa(t.seq%8), title, text)
}

func (b *nativeBridge) trayUnread(tok string, n int) (bool, error) {
	if !b.allowed(tok) || b.tray == nil {
		return false, errNotAllowed
	}
	b.tray.setUnread(n)
	return true, nil
}

func (b *nativeBridge) trayNotify(tok, title, text string) (bool, error) {
	if !b.allowed(tok) || b.tray == nil {
		return false, errNotAllowed
	}
	return b.tray.balloon(title, text), nil
}
