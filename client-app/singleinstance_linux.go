//go:build linux

package main

// singleinstance_linux.go — one app per user session on Linux
// (instanceGuard, platform.go; the decision is singleinstance.go's
// decideInstance, shared with Windows).
//
// The app owns the well-known name tools.obli.Oblireach on the session bus
// (the Flatpak's app id: a sandboxed app may own its own id without any
// permission). A second launch cannot own it: it calls the running one —
// tools.obli.Oblireach.Instance.Activate(args, platform_data) on
// /tools/obli/Oblireach — which shows its main window (or opens what the
// arguments name: a URL, a deep link) and answers shown / quitting /
// starting, then ends. The running one also implements
// org.freedesktop.Application on the same path (what desktop launchers
// call for a D-Bus-activatable app, and what GApplication speaks), so a
// GTK host taking the name later keeps the same contract.
//
// No session bus (a bare TTY, a broken session): the guard cannot work and
// lets the launch run (never refuse to start for want of a bus).

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	instanceBusName = "tools.obli.Oblireach"
	instancePath    = dbus.ObjectPath("/tools/obli/Oblireach")
	instanceIface   = "tools.obli.Oblireach.Instance"
	fdoAppIface     = "org.freedesktop.Application"
)

// instanceRequest: what a second launch asks of the running app.
type instanceRequest struct {
	Args  []string // its command line (after the program)
	URIs  []string // the URLs / files / deep links among them
	Token string   // its xdg-activation token (to raise a window under Wayland)
	Quiet bool     // --minimized: nothing to show
}

// instanceActivate: the running app's answer to a second launch, set by the
// UI host once its main window exists (it returns singleInstanceAckShown or
// singleInstanceAckQuitting). nil: no window yet (the launch waits, bounded).
var (
	instanceMu       sync.Mutex
	instanceActivate func(r instanceRequest) uintptr
)

// setInstanceHandler: the UI host's activation handler (nil: none).
func setInstanceHandler(h func(r instanceRequest) uintptr) {
	instanceMu.Lock()
	instanceActivate = h
	instanceMu.Unlock()
}

func dispatchActivate(r instanceRequest) uintptr {
	instanceMu.Lock()
	h := instanceActivate
	instanceMu.Unlock()
	if h == nil {
		return 0
	}
	return h(r)
}

// launchURIs: the arguments that name something to open (a URL, a deep
// link, a file URI), not the options.
func launchURIs(args []string) []string {
	var out []string
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if i := strings.Index(a, ":"); i > 1 && !strings.ContainsAny(a[:i], "/\\ ") {
			out = append(out, a)
		}
	}
	return out
}

// instanceObject: the exported object of the running app.
type instanceObject struct{}

// Activate (tools.obli.Oblireach.Instance): a second launch.
func (instanceObject) Activate(args []string, pd map[string]dbus.Variant) (uint32, *dbus.Error) {
	r := instanceRequest{Args: args, URIs: launchURIs(args), Quiet: quietLaunch(args)}
	r.Token = platformToken(pd)
	return uint32(dispatchActivate(r)), nil
}

// fdoApplication: org.freedesktop.Application on the same object.
type fdoApplication struct{}

func (fdoApplication) Activate(pd map[string]dbus.Variant) *dbus.Error {
	dispatchActivate(instanceRequest{Token: platformToken(pd)})
	return nil
}

func (fdoApplication) Open(uris []string, pd map[string]dbus.Variant) *dbus.Error {
	dispatchActivate(instanceRequest{URIs: uris, Args: uris, Token: platformToken(pd)})
	return nil
}

func (fdoApplication) ActivateAction(name string, params []dbus.Variant, pd map[string]dbus.Variant) *dbus.Error {
	// (Notification actions of the portal reach the app this way.)
	notificationAction(name, params)
	dispatchActivate(instanceRequest{Token: platformToken(pd)})
	return nil
}

func platformToken(pd map[string]dbus.Variant) string {
	for _, k := range []string{"activation-token", "desktop-startup-id"} {
		if v, ok := pd[k]; ok {
			if s, ok := v.Value().(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// claimInstanceName: own the name (true) or not (false: another app has it).
func claimInstanceName(conn *dbus.Conn) (bool, error) {
	if err := conn.Export(instanceObject{}, instancePath, instanceIface); err != nil {
		return false, err
	}
	if err := conn.Export(fdoApplication{}, instancePath, fdoAppIface); err != nil {
		return false, err
	}
	reply, err := conn.RequestName(instanceBusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return false, err
	}
	return reply == dbus.RequestNameReplyPrimaryOwner || reply == dbus.RequestNameReplyAlreadyOwner, nil
}

// askRunning: the running app's answer to this launch (0: none, no window
// yet, or it did not answer).
func askRunning(conn *dbus.Conn, args []string) (window bool, ack uintptr) {
	pd := map[string]dbus.Variant{}
	if t := activationToken(); t != "" {
		pd["activation-token"] = dbus.MakeVariant(t)
		pd["desktop-startup-id"] = dbus.MakeVariant(t)
	}
	if args == nil {
		args = []string{}
	}
	var code uint32
	call := conn.Object(instanceBusName, instancePath).Call(instanceIface+".Activate", 0, args, pd)
	if call.Err != nil {
		if dbusNoMethod(call.Err) {
			// Another program on the name (a GTK host's GApplication):
			// the freedesktop interface, no answer to read.
			if err := conn.Object(instanceBusName, instancePath).Call(fdoAppIface+".Activate", 0, pd).Err; err == nil {
				return true, singleInstanceAckShown
			}
		}
		return false, 0
	}
	if err := call.Store(&code); err != nil {
		return false, 0
	}
	return code != 0, uintptr(code)
}

type linuxInstance struct{}

var platformInstance instanceGuard = linuxInstance{}

// claim: true, this launch is the app; false, it must end.
func (linuxInstance) claim(args []string) bool {
	conn, err := sessionBus()
	if err != nil {
		log.Printf("single instance: %v; running without the guard", err)
		return true
	}
	return claimOn(conn, args, time.Now)
}

func claimOn(conn *dbus.Conn, args []string, now func() time.Time) bool {
	quiet := quietLaunch(args)
	start := now()
	for {
		owned, err := claimInstanceName(conn)
		if err != nil {
			log.Printf("single instance: %v; running without the guard", err)
			return true
		}
		o := instanceObs{Acquired: owned, Elapsed: now().Sub(start), Quiet: quiet}
		if !owned {
			o.Window, o.Ack = askRunning(conn, args)
		}
		switch decideInstance(o) {
		case instanceRun:
			return true
		case instanceExit:
			if !o.Window || (o.Ack != singleInstanceAckShown && !quiet) {
				log.Printf("single instance: another Oblireach runs in this session; ending")
			}
			return false
		}
		time.Sleep(singleInstancePoll)
	}
}

// releaseInstance gives the name back (the app quits: a launch waiting for
// it may start at once).
func releaseInstance() {
	if conn, err := sessionBus(); err == nil {
		_, _ = conn.ReleaseName(instanceBusName)
	}
}
