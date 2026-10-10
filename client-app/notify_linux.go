//go:build linux

package main

// notify_linux.go — desktop notifications on Linux: the Notification portal
// (org.freedesktop.portal.Notification: no permission in the Flatpak; the
// desktop shows the app's name and icon), else, outside a sandbox,
// org.freedesktop.Notifications (KDE Plasma's and GNOME Shell's own
// notification servers). A click on the notification brings the main window
// back (notificationActivated, set by the UI host).

import (
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	portalNotification = "org.freedesktop.portal.Notification"
	fdoNotifyName      = "org.freedesktop.Notifications"
	fdoNotifyPath      = dbus.ObjectPath("/org/freedesktop/Notifications")
	// The action of a click on a notification (the portal's
	// "app.<action>"; the freedesktop server's "default").
	notifyActionOpen = "open"
)

var (
	notifyMu sync.Mutex
	// notificationActivated: what a click on one of our notifications does
	// (the UI host: the main window in front, the chat opened). nil: the
	// single-instance activation (the main window).
	notificationActivated func(id, action string)
	notifyWatching        *dbus.Conn            // the connection whose action signals are watched
	notifyFdoIDs          = map[uint32]string{} // the freedesktop server's ids -> ours
)

// notificationAction: an action reached the app (the portal through
// org.freedesktop.Application.ActivateAction, or a signal).
func notificationAction(name string, params []dbus.Variant) {
	id := ""
	if len(params) > 0 {
		if s, ok := params[0].Value().(string); ok {
			id = s
		}
	}
	notifyMu.Lock()
	h := notificationActivated
	notifyMu.Unlock()
	if h != nil {
		h(id, strings.TrimPrefix(name, "app."))
		return
	}
	dispatchActivate(instanceRequest{})
}

// watchNotificationActions: the action signals of both services, once.
func watchNotificationActions(conn *dbus.Conn) {
	notifyMu.Lock()
	if notifyWatching == conn {
		notifyMu.Unlock()
		return
	}
	notifyWatching = conn // (a new connection after the bus came back: watched again)
	notifyMu.Unlock()
	_ = conn.AddMatchSignal(dbus.WithMatchInterface(portalNotification), dbus.WithMatchMember("ActionInvoked"))
	_ = conn.AddMatchSignal(dbus.WithMatchInterface(fdoNotifyName), dbus.WithMatchMember("ActionInvoked"))
	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)
	go func() {
		for s := range ch {
			switch s.Name {
			case portalNotification + ".ActionInvoked": // (app_id, id, action, parameter)
				if len(s.Body) >= 3 {
					id, _ := s.Body[1].(string)
					action, _ := s.Body[2].(string)
					if strings.HasPrefix(id, "oblireach-") {
						notificationAction(action, []dbus.Variant{dbus.MakeVariant(id)})
					}
				}
			case fdoNotifyName + ".ActionInvoked": // (id, action_key)
				if len(s.Body) >= 2 {
					n, _ := s.Body[0].(uint32)
					notifyMu.Lock()
					id, ours := notifyFdoIDs[n]
					delete(notifyFdoIDs, n)
					notifyMu.Unlock()
					if ours {
						notificationAction(notifyActionOpen, []dbus.Variant{dbus.MakeVariant(id)})
					}
				}
			}
		}
	}()
}

// desktopNotify shows a notification (id: ours, a later one with the same
// id replaces it through the portal). False: no notification service.
func desktopNotify(id, title, body string) bool {
	conn, err := sessionBus()
	if err != nil {
		return false
	}
	watchNotificationActions(conn)
	if !strings.HasPrefix(id, "oblireach-") {
		id = "oblireach-" + id
	}
	n := map[string]dbus.Variant{
		"title":                 dbus.MakeVariant(title),
		"body":                  dbus.MakeVariant(body),
		"priority":              dbus.MakeVariant("normal"),
		"default-action":        dbus.MakeVariant("app." + notifyActionOpen),
		"default-action-target": dbus.MakeVariant(id),
	}
	err = conn.Object(portalBusName, portalPath).Call(portalNotification+".AddNotification", 0, id, n).Err
	if err == nil {
		return true
	}
	if inFlatpak() {
		return false
	}
	// Outside a sandbox: the notification server itself.
	var nid uint32
	hints := map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant(ssAppID), "urgency": dbus.MakeVariant(byte(1))}
	err = conn.Object(fdoNotifyName, fdoNotifyPath).Call(fdoNotifyName+".Notify", 0,
		"Oblireach", uint32(0), ssAppID, title, body, []string{"default", title}, hints, int32(-1)).Store(&nid)
	if err != nil {
		return false
	}
	notifyMu.Lock()
	if len(notifyFdoIDs) > 256 {
		notifyFdoIDs = map[uint32]string{}
	}
	notifyFdoIDs[nid] = id
	notifyMu.Unlock()
	return true
}

// withdrawNotification removes one of our notifications (portal only).
func withdrawNotification(id string) {
	if conn, err := sessionBus(); err == nil {
		_ = conn.Object(portalBusName, portalPath).Call(portalNotification+".RemoveNotification", 0, "oblireach-"+strings.TrimPrefix(id, "oblireach-")).Err
	}
}
