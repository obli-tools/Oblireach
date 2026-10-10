package main

// detached_model.go — the platform-neutral model of the detached session
// windows (detached_windows.go is the Windows window manager): the device
// keys, the options of reachOpenWindow / reachTearOff and their checks, a
// window's title, what the pages report of their tabs. Moved from
// detached_windows.go unchanged.

import (
	"errors"
	"strings"
	"unicode"
)

// Most tabs a session window reports (reachWindowTabs).
const maxWindowTabs = 64

// devKey: the key of a device of a server profile.
func devKey(profile, deviceID string) string {
	if profile == "" {
		return deviceID
	}
	return profile + ":" + deviceID
}

// toolKey: the key of a tool tab of a device (terminal_viewer.go): its
// device's key and "~<kind>" (a kind of toolKinds); the device's own key for
// its ObliReach session (kind ""). "~" is outside validDetachID and is not
// the profile separator: a device's ObliReach window and its CMD window are
// two keys, two windows (or two tabs of one).
func toolKey(profile, deviceID, kind string) string {
	k := devKey(profile, deviceID)
	if kind != "" {
		k += "~" + kind
	}
	return k
}

// validDevKey: a device id, or "<profile>:<device id>", each part a
// validDetachID, optionally followed by "~<kind>" (a tool tab, toolKinds; a
// Hyper-V VM console of that host, "vm-<guid>": vm_console_go.go).
func validDevKey(k string) bool {
	if i := strings.LastIndexByte(k, '~'); i >= 0 {
		if !validWindowKind(k[i+1:]) { // (a tool, or a VM console: vm_console_go.go)
			return false
		}
		k = k[:i]
	}
	if i := strings.IndexByte(k, ':'); i >= 0 {
		return validDetachID(k[:i]) && validDetachID(k[i+1:])
	}
	return validDetachID(k)
}

// keyProfile: the profile part of a key ("": none).
func keyProfile(k string) string {
	if i := strings.IndexByte(k, ':'); i >= 0 {
		return k[:i]
	}
	return ""
}

// detachOpts: the options of reachOpenWindow.
type detachOpts struct {
	Session  *int   `json:"session"`  // Windows session to show; absent or -1: automatic
	ViewOnly bool   `json:"viewOnly"` // the session starts in view only
	Profile  string `json:"profile"`  // server profile on the main page ('' without profiles)
	Kind     string `json:"kind"`     // a tool window (toolKinds: a terminal, the file explorer), a VM console ("vm-<guid>"); '': ObliReach
	VmName   string `json:"vmName"`   // a VM console: its VM's name (its tab's title)
	Park     string `json:"park"`     // a terminal tab moving (tearOff): its park token (term_park.go)
	Path     string `json:"path"`     // an explorer tab moving (tearOff): its folder
	// Merge: the window's ONLY tab, after the whole window was dragged and
	// dropped onto another window's tab strip (tabDragEnded): it joins that
	// window (never a new one) and this window, empty, closes. That tab may
	// be a mosaic of the page (tab_groups.go): its first member merges, the
	// others follow with Into.
	Merge bool `json:"merge"`
	// Into: a member of a mosaic moving whole (tab_groups.go tgTearOff): the
	// key (devKey / toolKey) of the member that went first; this tab joins
	// the window that one went to (a session window, or the main window),
	// wherever the cursor is now, and may be this window's last.
	Into string `json:"into"`
	// Main: the tab goes to the main window, wherever the cursor is (the
	// file explorer's side panel of a session window, "Open in a tab":
	// files_dock.go); it may be the window's last.
	Main bool `json:"main"`
	// Window: the tab goes into that session window (its id, from
	// reachWindowTargets: a tab's "Move to window" menu, tab_move.go),
	// wherever the cursor is; it may be the window's last. From the main
	// window (reachOpenWindow): its session or explorer joins that window.
	Window int `json:"window"`
	// NewWindow: the tab goes to a new window at the cursor (that menu's "New
	// window"), never into the window under it.
	NewWindow bool `json:"newWindow"`
}

const maxDetachSession = 1 << 20

// check validates the options: the Windows session (-1: automatic), the
// profile, the tool kind.
func (o detachOpts) check() (int, error) {
	session := -1
	if o.Session != nil {
		if *o.Session < -1 || *o.Session > maxDetachSession {
			return 0, errors.New(tr("detached.err.invalidSession"))
		}
		session = *o.Session
	}
	if o.Profile != "" && !validDetachID(o.Profile) {
		return 0, errors.New(tr("detached.err.invalidServer"))
	}
	if o.Kind != "" && !validWindowKind(o.Kind) {
		return 0, errors.New(tr("detached.err.invalidKind"))
	}
	if o.VmName != "" && !vmKind(o.Kind) {
		return 0, errors.New(tr("detached.err.invalidKind"))
	}
	// A park token: a terminal's only; a folder: the explorer's only.
	if o.Park != "" && (!toolParkKind(o.Kind) || !reParkToken.MatchString(o.Park)) {
		return 0, errors.New(tr("detached.err.notMovable"))
	}
	if o.Path != "" && (o.Kind != "files" || !validToolPath(o.Path)) {
		return 0, errors.New(tr("detached.err.invalidKind"))
	}
	if o.Into != "" && (o.Merge || !validDevKey(o.Into)) {
		return 0, errors.New(tr("detached.err.invalidDevice"))
	}
	if o.Main && (o.Merge || o.Into != "") {
		return 0, errors.New(tr("detached.err.invalidDevice"))
	}
	// One destination at most: a window by its id, a new one, or the others.
	if o.Window < 0 || (o.Window > 0 && (o.Merge || o.Into != "" || o.Main || o.NewWindow)) ||
		(o.NewWindow && (o.Merge || o.Into != "" || o.Main)) {
		return 0, errors.New(tr("detached.err.noSuchWindow"))
	}
	return session, nil
}

// detachTitle: the window title, the device name as the main window sent it
// (no control characters, at most 120 characters).
func detachTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	if s == "" {
		s = tr("win.sessionTitle")
	}
	return s
}

// windowTarget: a session window a tab may move into (reachWindowTargets).
type windowTarget struct {
	ID    int    `json:"id"`
	Title string `json:"title"` // its title: the device on screen there
	Tabs  int    `json:"tabs"`  // how many tabs it has
}

// windowTabsInfo: what a page reports of its session tabs (reachWindowTabs).
type windowTabsInfo struct {
	IDs   []string `json:"ids"`   // the devices it has a tab for (and the ones its "+" list claimed)
	Title string   `json:"title"` // a session window's title: the device on screen
}

// cleanTabIDs: the valid device keys of a report, once each, at most
// maxWindowTabs.
func cleanTabIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !validDevKey(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == maxWindowTabs {
			break
		}
	}
	return out
}
