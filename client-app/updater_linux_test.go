//go:build linux

package main

// updater_linux_test.go — the one-click update through a FAKE Flatpak
// portal on a private session bus (dbusfake_linux_test.go).

import (
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const fakeMonitorPath = dbus.ObjectPath("/org/freedesktop/portal/Flatpak/update_monitor/t1")

type fakeFlatpakPortal struct {
	conn    *dbus.Conn
	mu      sync.Mutex
	steps   []map[string]dbus.Variant
	updates int
	closed  int
}

func startFakeFlatpakPortal(t *testing.T, steps []map[string]dbus.Variant) *fakeFlatpakPortal {
	t.Helper()
	b := startTestBus(t)
	c := b.mustConnect(t)
	f := &fakeFlatpakPortal{conn: c, steps: steps}
	c.ExportMethodTable(map[string]interface{}{
		"CreateUpdateMonitor": func(opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) { return fakeMonitorPath, nil },
	}, flatpakPortalPath, flatpakPortalIface)
	c.ExportMethodTable(map[string]interface{}{
		"Update": func(parent string, opts map[string]dbus.Variant) *dbus.Error {
			f.mu.Lock()
			f.updates++
			steps := f.steps
			f.mu.Unlock()
			go func() {
				for _, s := range steps {
					time.Sleep(10 * time.Millisecond)
					f.conn.Emit(fakeMonitorPath, flatpakMonitorIface+".Progress", s)
				}
			}()
			return nil
		},
		"Close": func() *dbus.Error { f.mu.Lock(); f.closed++; f.mu.Unlock(); return nil },
	}, fakeMonitorPath, flatpakMonitorIface)
	ownName(t, c, flatpakPortalName)
	return f
}

func prog(status, progress, nOps, op uint32, msg string) map[string]dbus.Variant {
	m := map[string]dbus.Variant{"status": dbus.MakeVariant(status), "progress": dbus.MakeVariant(progress),
		"n_ops": dbus.MakeVariant(nOps), "op": dbus.MakeVariant(op)}
	if msg != "" {
		m["error_message"] = dbus.MakeVariant(msg)
	}
	return m
}

// statuses: the updater with its pushes recorded.
func testUpdater(inFlatpakNow bool) (*desktopUpdater, func() []updateStatus) {
	var mu sync.Mutex
	var got []updateStatus
	u := newDesktopUpdater(func(s updateStatus) { mu.Lock(); got = append(got, s); mu.Unlock() })
	u.inFlatpak = func() bool { return inFlatpakNow }
	return u, func() []updateStatus { mu.Lock(); defer mu.Unlock(); return append([]updateStatus(nil), got...) }
}

func waitFinal(t *testing.T, all func() []updateStatus) updateStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g := all(); len(g) > 0 {
			if l := g[len(g)-1]; l.State == "idle" || l.State == "error" {
				return l
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no final status: %+v", all())
	return updateStatus{}
}

func TestLinuxUpdaterThroughPortal(t *testing.T) {
	f := startFakeFlatpakPortal(t, []map[string]dbus.Variant{prog(0, 10, 2, 0, ""), prog(0, 50, 2, 1, ""), prog(2, 100, 2, 1, "")})
	u, all := testUpdater(true)
	if s := u.state(); s.State != "idle" || s.Code != "" {
		t.Fatalf("idle %+v", s)
	}
	if s := u.start(); s.State != "downloading" {
		t.Fatalf("start %+v", s)
	}
	if s := u.start(); s.State != "downloading" || f.updates > 1 {
		t.Fatal("a second update started")
	}
	last := waitFinal(t, all)
	if last.Code != "restart" || last.Result == nil || !last.Result.OK || last.Result.Text == nil || last.Result.Text.Key != "update.linux.restart" {
		t.Fatalf("final %+v", last)
	}
	g := all()
	pcts := []int{}
	for _, s := range g {
		if s.State == "downloading" && s.Percent >= 0 {
			pcts = append(pcts, s.Percent)
		}
	}
	if len(pcts) != 2 || pcts[0] != 5 || pcts[1] != 75 {
		t.Fatalf("percents %v", pcts)
	}
	for i := 1; i < len(g); i++ {
		if g[i].Seq <= g[i-1].Seq {
			t.Fatal("seq not increasing")
		}
	}
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed != 1 {
		t.Fatalf("monitor closed %d times", closed)
	}
}

func TestLinuxUpdaterErrors(t *testing.T) {
	startFakeFlatpakPortal(t, []map[string]dbus.Variant{prog(3, 0, 1, 0, "Remote not found")})
	u, all := testUpdater(true)
	u.start()
	if l := waitFinal(t, all); l.State != "error" || l.Code != "flatpak" || l.Text == nil || l.Text.Params["error"] != "Remote not found" {
		t.Fatalf("error %+v", l)
	}
	// Nothing to update.
	startFakeFlatpakPortal(t, []map[string]dbus.Variant{prog(1, 0, 0, 0, "")})
	u2, all2 := testUpdater(true)
	u2.start()
	if l := waitFinal(t, all2); l.State != "idle" || l.Code != "uptodate" {
		t.Fatalf("empty %+v", l)
	}
	// No answer in time.
	startFakeFlatpakPortal(t, nil)
	u3, all3 := testUpdater(true)
	u3.timeout = 200 * time.Millisecond
	u3.start()
	if l := waitFinal(t, all3); l.Code != "timeout" {
		t.Fatalf("timeout %+v", l)
	}
	// No portal at all.
	startTestBus(t)
	u4, all4 := testUpdater(true)
	u4.start()
	if l := waitFinal(t, all4); l.Code != "portal" {
		t.Fatalf("no portal %+v", l)
	}
}

// Outside the Flatpak: never installs, says how.
func TestLinuxUpdaterManual(t *testing.T) {
	u, _ := testUpdater(false)
	if s := u.state(); s.Code != "manual" {
		t.Fatalf("state %+v", s)
	}
	if s := u.start(); s.State != "error" || s.Code != "manual" || s.Text == nil || s.Text.Key != "update.linux.manual" {
		t.Fatalf("start %+v", s)
	}
}
