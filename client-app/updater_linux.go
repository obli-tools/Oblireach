//go:build linux

package main

// updater_linux.go — the one-click update on Linux (appUpdater, platform.go).
//
// Oblireach Desktop for Linux is a Flatpak installed from the repository its
// Obliance server publishes (Obliance oblireachDesktopLinux.controller.ts;
// the version route's "linux" field says the latest version, the page
// compares it: ui_platform.go's late block). Flatpak verifies the GPG
// signature of what it installs; the app downloads nothing itself.
//
//   - Inside the Flatpak (flatpak >= 1.6): "Update now" asks the Flatpak
//     portal (org.freedesktop.portal.Flatpak, no permission needed) to update
//     THIS app: CreateUpdateMonitor, then UpdateMonitor.Update; its Progress
//     signals drive the banner (downloading, a percent), then "restart
//     Oblireach to use the new version" (the running instance keeps its
//     files: the next start is the new version). The system's own automatic
//     updates (Bazzite, GNOME Software, Discover) do the same in the
//     background.
//   - Outside the Flatpak (a development build, a package): nothing is
//     installed from here; the page says how (code "manual").

import (
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	flatpakPortalName    = "org.freedesktop.portal.Flatpak"
	flatpakPortalPath    = dbus.ObjectPath("/org/freedesktop/portal/Flatpak")
	flatpakPortalIface   = "org.freedesktop.portal.Flatpak"
	flatpakMonitorIface  = "org.freedesktop.portal.Flatpak.UpdateMonitor"
	flatpakUpdateTimeout = 30 * time.Minute

	// UpdateMonitor Progress "status" values.
	flatpakStatusRunning = 0
	flatpakStatusEmpty   = 1
	flatpakStatusDone    = 2
	flatpakStatusError   = 3
)

// flatpakProgress is one Progress signal of the update monitor.
type flatpakProgress struct {
	Status   uint32
	Progress uint32 // 0..100 of the current operation
	NOps     uint32
	Op       uint32
	ErrMsg   string
}

// flatpakMonitor: one update of this app through the portal (a fake in tests).
type flatpakMonitor interface {
	// update starts it; events then come on progress until a final status.
	update() error
	progress() <-chan flatpakProgress
	close()
}

// flatpakPortalMonitor: the real portal on the session bus.
var flatpakNewMonitor = func() (flatpakMonitor, error) { return newPortalFlatpakMonitor() }

type portalFlatpakMonitor struct {
	conn *dbus.Conn
	path dbus.ObjectPath
	sig  chan *dbus.Signal
	out  chan flatpakProgress
	stop chan struct{}
	once sync.Once
}

func newPortalFlatpakMonitor() (*portalFlatpakMonitor, error) {
	conn, err := sessionBus()
	if err != nil {
		return nil, err
	}
	var path dbus.ObjectPath
	call := conn.Object(flatpakPortalName, flatpakPortalPath).Call(flatpakPortalIface+".CreateUpdateMonitor", 0, map[string]dbus.Variant{})
	if call.Err != nil {
		return nil, call.Err
	}
	if err := call.Store(&path); err != nil {
		return nil, err
	}
	m := &portalFlatpakMonitor{conn: conn, path: path, sig: make(chan *dbus.Signal, 32), out: make(chan flatpakProgress, 32), stop: make(chan struct{})}
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(flatpakMonitorIface)); err != nil {
		return nil, err
	}
	conn.Signal(m.sig)
	go m.pump()
	return m, nil
}

func (m *portalFlatpakMonitor) pump() {
	for {
		select {
		case <-m.stop:
			return
		case s, ok := <-m.sig:
			if !ok {
				return
			}
			if s.Path != m.path || s.Name != flatpakMonitorIface+".Progress" || len(s.Body) < 1 {
				continue
			}
			info, _ := s.Body[0].(map[string]dbus.Variant)
			var p flatpakProgress
			u := func(k string) uint32 {
				if v, ok := info[k]; ok {
					if n, ok := v.Value().(uint32); ok {
						return n
					}
				}
				return 0
			}
			p.Status, p.Progress, p.NOps, p.Op = u("status"), u("progress"), u("n_ops"), u("op")
			if v, ok := info["error_message"]; ok {
				p.ErrMsg, _ = v.Value().(string)
			}
			select {
			case m.out <- p:
			case <-m.stop:
				return
			}
		}
	}
}

func (m *portalFlatpakMonitor) update() error {
	return m.conn.Object(flatpakPortalName, m.path).Call(flatpakMonitorIface+".Update", 0, portalParentWindow(0), map[string]dbus.Variant{}).Err
}

func (m *portalFlatpakMonitor) progress() <-chan flatpakProgress { return m.out }

func (m *portalFlatpakMonitor) close() {
	m.once.Do(func() {
		close(m.stop)
		m.conn.RemoveSignal(m.sig)
		_ = m.conn.RemoveMatchSignal(dbus.WithMatchObjectPath(m.path), dbus.WithMatchInterface(flatpakMonitorIface))
		_ = m.conn.Object(flatpakPortalName, m.path).Call(flatpakMonitorIface+".Close", 0).Err
	})
}

// desktopUpdater: the one-click update of the main window.
type desktopUpdater struct {
	push      func(updateStatus) // tells the page (window.__reachOnUpdate)
	inFlatpak func() bool
	timeout   time.Duration

	mu     sync.Mutex
	status updateStatus
	seq    int64
	busy   bool
}

func newDesktopUpdater(push func(updateStatus)) *desktopUpdater {
	return &desktopUpdater{push: push, inFlatpak: inFlatpak, timeout: flatpakUpdateTimeout, status: updateStatus{State: "idle", Percent: -1}}
}

func (u *desktopUpdater) set(s updateStatus) {
	u.mu.Lock()
	u.seq++
	s.Seq = u.seq
	u.status = s
	push := u.push
	u.mu.Unlock()
	if push != nil {
		push(s)
	}
}

// state: where the update is; outside the Flatpak, "manual" (the page says
// how to update instead of offering "Update now").
func (u *desktopUpdater) state() updateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.status
	if s.State == "idle" && s.Code == "" && !u.inFlatpak() {
		s.Code = "manual"
	}
	return s
}

// fail: an error status, its text in the app's language and by its key.
func (u *desktopUpdater) fail(code, message string, text *i18nMsg) {
	u.set(updateStatus{State: "error", Percent: -1, Code: code, Message: message, Text: text})
}

// start: the update through the portal (once at a time).
func (u *desktopUpdater) start() updateStatus {
	if !u.inFlatpak() {
		s := updateStatus{State: "error", Percent: -1, Code: "manual", Message: tr("update.linux.manual"), Text: i18nM("update.linux.manual")}
		u.set(s)
		return u.state()
	}
	u.mu.Lock()
	if u.busy {
		s := u.status
		u.mu.Unlock()
		return s
	}
	u.busy = true
	u.mu.Unlock()
	u.set(updateStatus{State: "downloading", Percent: -1})
	go u.run()
	return u.state()
}

func (u *desktopUpdater) run() {
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()
	portalFailed := func() { u.fail("portal", tr("update.linux.portalFailed"), i18nM("update.linux.portalFailed")) }
	m, err := flatpakNewMonitor()
	if err != nil {
		portalFailed()
		return
	}
	defer m.close()
	if err := m.update(); err != nil {
		portalFailed()
		return
	}
	deadline := time.NewTimer(u.timeout)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			u.fail("timeout", tr("update.linux.timeout"), i18nM("update.linux.timeout"))
			return
		case p, ok := <-m.progress():
			if !ok {
				portalFailed()
				return
			}
			switch p.Status {
			case flatpakStatusRunning:
				pct := int(p.Progress)
				if p.NOps > 1 {
					pct = int((p.Op*100 + p.Progress) / p.NOps)
				}
				if pct > 100 {
					pct = 100
				}
				u.set(updateStatus{State: "downloading", Percent: pct})
			case flatpakStatusEmpty:
				r := &updateResult{OK: true}
				r.say("update.linux.upToDate")
				u.set(updateStatus{State: "idle", Percent: -1, Code: "uptodate", Result: r})
				return
			case flatpakStatusDone:
				r := &updateResult{OK: true}
				r.say("update.linux.restart")
				u.set(updateStatus{State: "idle", Percent: -1, Code: "restart", Result: r})
				return
			case flatpakStatusError:
				msg := p.ErrMsg
				if len(msg) > 300 {
					msg = msg[:300]
				}
				u.fail("flatpak", tr("update.linux.failed", "error", msg), i18nM("update.linux.failed", "error", msg))
				return
			}
		}
	}
}
