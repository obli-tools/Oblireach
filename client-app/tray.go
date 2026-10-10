package main

// tray.go — the notification-area icon's pure parts (tray_windows.go has the
// Win32 side): its setting, its tooltip, its menu, a notification's texts.
//
// Settings > Application > "Keep in the notification area" (off by default):
// on, Oblireach has an icon next to the clock; minimizing or closing the main
// window hides it there (the app, its sessions and chats go on), a
// double-click on the icon brings it back, its menu opens it or quits the
// app, and says how many chat messages are unread. Started minimized
// ("Start minimized"), the app then starts hidden there. A chat message that
// arrives while the window is hidden shows a notification of the icon (a
// click on it opens the Chat page); otherwise the taskbar button flashes as
// before.

import (
	"strconv"
	"strings"
	"unicode"
)

// Tray menu commands.
const (
	trayCmdOpen = 1
	trayCmdChat = 2
	trayCmdQuit = 3
)

// trayMenuItem: one line of the icon's menu (sep: a separator).
type trayMenuItem struct {
	id   int
	text string
	bold bool
	sep  bool
	grey bool
}

// trayMenu: Open (the default: bold), the unread chat messages when there
// are some (opens the Chat page), Quit.
func trayMenu(unread int) []trayMenuItem {
	items := []trayMenuItem{{id: trayCmdOpen, text: tr("tray.open"), bold: true}}
	if unread > 0 {
		items = append(items, trayMenuItem{id: trayCmdChat, text: tr("tray.unread", "count", trayCount(unread))})
	}
	items = append(items, trayMenuItem{sep: true}, trayMenuItem{id: trayCmdQuit, text: tr("tray.quit")})
	return items
}

// trayTip: the icon's tooltip (127 UTF-16 units at most).
func trayTip(unread int) string {
	t := "Oblireach"
	if unread > 0 {
		t = tr("tray.tipUnread", "count", trayCount(unread))
	}
	return truncUTF16(t, 127)
}

func trayCount(n int) string {
	if n > 99 {
		return "99+"
	}
	return strconv.Itoa(n)
}

// trayNoteText: a notification's title and text as the page sent them: no
// control characters, bounded (63 and 255 UTF-16 units, the icon's limits).
func trayNoteText(title, text string) (string, string) {
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, s)
		return strings.TrimSpace(s)
	}
	title, text = clean(title), clean(text)
	if title == "" {
		title = "Oblireach"
	}
	if text == "" {
		text = tr("tray.chatNew")
	}
	return truncUTF16(title, 63), truncUTF16(text, 255)
}

// truncUTF16: s cut to max UTF-16 units (never inside a surrogate pair), "…" at the end when cut.
func truncUTF16(s string, max int) string {
	n := 0
	for _, r := range s {
		n += runeUTF16Len(r)
	}
	if n <= max {
		return s
	}
	var b strings.Builder
	n = 0
	for _, r := range s {
		l := runeUTF16Len(r)
		if n+l > max-1 {
			break
		}
		b.WriteRune(r)
		n += l
	}
	b.WriteRune('…')
	return b.String()
}

func runeUTF16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// The setting (config.json minimizeToTray).
func (p *Proxy) trayPref() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.MinimizeToTray
}

func (p *Proxy) setTrayPref(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.MinimizeToTray = on
	saveConfig(p.cfg)
}
