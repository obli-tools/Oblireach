//go:build linux

package main

// portal_files_linux.go — files and the desktop through the XDG desktop
// portals (no permission needed in the Flatpak):
//
//   - org.freedesktop.portal.FileChooser: OpenFile / SaveFile (filters,
//     multiple, current folder, suggested name), the fileDialogs of the
//     connection import / export (connimport_linux.go). Inside the Flatpak a
//     file outside what the app may see comes back as a Documents portal
//     path (/run/user/<uid>/doc/...), readable / writable all the same;
//   - org.freedesktop.portal.OpenURI: OpenFile (a file in its default
//     application, or the portal's application chooser with ask=true;
//     writable=true lets the application save into it: the editing round
//     trip), OpenDirectory (the file manager on a file's folder, the file
//     selected). The file goes as an O_PATH file descriptor, never as a
//     path the portal would re-resolve;
//   - outside a sandbox, with no portal answering: org.freedesktop.FileManager1
//     for "show in folder".
//
// The calls block until the portal answers (a dialog may stay up a while):
// the UI host calls them off its UI thread.

import (
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const (
	portalFileChooser = "org.freedesktop.portal.FileChooser"
	portalOpenURI     = "org.freedesktop.portal.OpenURI"
)

// ── FileChooser ────────────────────────────────────────────────────────────

// portalFilter: one filter of the dialog, its glob patterns.
type portalFilter struct {
	Label    string
	Patterns []string
}

// fileChooserOpts: an Open or Save dialog.
type fileChooserOpts struct {
	Owner     uintptr
	Title     string
	Save      bool
	Multiple  bool // (Open only)
	Directory bool // pick folders (Open only, FileChooser version 3+)
	Filters   []portalFilter
	Folder    string // the folder it starts in ("" : the portal's choice)
	Name      string // the suggested file name (Save)
	Accept    string // the accept button's label ("" : the portal's)
}

// parseWinFilter: the fileDialogs filter ("label\x00pat;pat\x00...") as
// portal filters ("*.*" is "*").
func parseWinFilter(f string) []portalFilter {
	parts := strings.Split(strings.TrimRight(f, "\x00"), "\x00")
	var out []portalFilter
	for i := 0; i+1 < len(parts); i += 2 {
		var pats []string
		for _, p := range strings.Split(parts[i+1], ";") {
			p = strings.TrimSpace(p)
			if p == "*.*" {
				p = "*"
			}
			if p != "" {
				pats = append(pats, p)
			}
		}
		if len(pats) > 0 {
			out = append(out, portalFilter{Label: parts[i], Patterns: pats})
		}
	}
	return out
}

// portalFilterValue: a(sa(us)) (0 = glob pattern).
type portalFilterRule struct {
	Kind    uint32
	Pattern string
}

type portalFilterSpec struct {
	Label string
	Rules []portalFilterRule
}

func (f portalFilter) spec() portalFilterSpec {
	s := portalFilterSpec{Label: f.Label}
	for _, p := range f.Patterns {
		s.Rules = append(s.Rules, portalFilterRule{Kind: 0, Pattern: p})
	}
	return s
}

// nulPath: a path as the portal's bytestring (NUL-terminated).
func nulPath(p string) []byte { return append([]byte(p), 0) }

// uriToPath: a file:// URI as a local path ("" otherwise).
func uriToPath(u string) string {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "file" || (pu.Host != "" && pu.Host != "localhost") {
		return ""
	}
	if !filepath.IsAbs(pu.Path) {
		return ""
	}
	return filepath.Clean(pu.Path)
}

// chooseFiles shows the dialog; the paths picked (nil, errPortalCancel:
// cancelled).
func chooseFiles(o fileChooserOpts) ([]string, error) {
	opts := map[string]dbus.Variant{"modal": dbus.MakeVariant(true)}
	if o.Accept != "" {
		opts["accept_label"] = dbus.MakeVariant(o.Accept)
	}
	if len(o.Filters) > 0 {
		specs := make([]portalFilterSpec, 0, len(o.Filters))
		for _, f := range o.Filters {
			specs = append(specs, f.spec())
		}
		opts["filters"] = dbus.MakeVariant(specs)
		opts["current_filter"] = dbus.MakeVariant(specs[0])
	}
	if o.Folder != "" {
		if st, err := os.Stat(o.Folder); err == nil && st.IsDir() {
			opts["current_folder"] = dbus.MakeVariant(nulPath(o.Folder))
		}
	}
	method := "OpenFile"
	if o.Save {
		method = "SaveFile"
		if o.Name != "" {
			opts["current_name"] = dbus.MakeVariant(o.Name)
		}
	} else {
		opts["multiple"] = dbus.MakeVariant(o.Multiple)
		if o.Directory {
			opts["directory"] = dbus.MakeVariant(true)
		}
	}
	res, err := portalCall(portalFileChooser, method, portalInteractiveTimeout, opts, portalParentWindow(o.Owner), o.Title)
	if err != nil {
		return nil, err
	}
	v, ok := res["uris"]
	if !ok {
		return nil, errPortalFailed
	}
	uris, _ := v.Value().([]string)
	var out []string
	for _, u := range uris {
		if p := uriToPath(u); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, errPortalFailed
	}
	return out, nil
}

// linuxDialogs: fileDialogs through the FileChooser portal.
type linuxDialogs struct{}

var platformDialogs fileDialogs = linuxDialogs{}

// pickFile: an Open or Save As dialog (defExt added to a saved name without
// an extension).
func (linuxDialogs) pickFile(owner uintptr, save bool, title, filter, initialDir, suggested, defExt string) (string, bool) {
	paths, err := chooseFiles(fileChooserOpts{Owner: owner, Title: title, Save: save, Filters: parseWinFilter(filter),
		Folder: initialDir, Name: suggested})
	if err != nil || len(paths) == 0 {
		if err != nil && !errors.Is(err, errPortalCancel) {
			logPortalError("file dialog", err)
		}
		return "", false
	}
	p := paths[0]
	if save && defExt != "" && filepath.Ext(p) == "" {
		p += "." + strings.TrimPrefix(defExt, ".")
	}
	return p, true
}

// pickProgram: a sandboxed app cannot run a program of the host; the
// editing round trip's "another program" is the portal's application
// chooser at each launch instead (editsession_linux.go): no program is
// picked here.
func (linuxDialogs) pickProgram(uintptr, string, string) (string, bool) { return "", false }

// ── OpenURI ────────────────────────────────────────────────────────────────

// openPathFD: an O_PATH descriptor of path (what the portal takes).
func openPathFD(path string) (int, error) {
	return unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
}

// openURIFile opens a file in an application: its default one, or the
// portal's chooser (ask). writable: the application may save into it.
func openURIFile(owner uintptr, path string, writable, ask bool) error {
	fd, err := openPathFD(path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	opts := map[string]dbus.Variant{"writable": dbus.MakeVariant(writable), "ask": dbus.MakeVariant(ask)}
	if t := activationToken(); t != "" {
		opts["activation_token"] = dbus.MakeVariant(t)
	}
	timeout := portalQuickTimeout
	if ask {
		timeout = portalInteractiveTimeout
	}
	_, err = portalCall(portalOpenURI, "OpenFile", timeout, opts, portalParentWindow(owner), dbus.UnixFD(fd))
	return err
}

// openURIDirectory: the file manager on path's folder, path selected.
func openURIDirectory(path string) error {
	fd, err := openPathFD(path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	opts := map[string]dbus.Variant{}
	if t := activationToken(); t != "" {
		opts["activation_token"] = dbus.MakeVariant(t)
	}
	_, err = portalCall(portalOpenURI, "OpenDirectory", portalQuickTimeout, opts, portalParentWindow(0), dbus.UnixFD(fd))
	return err
}

// fileManagerShow: org.freedesktop.FileManager1.ShowItems (Dolphin,
// Nautilus...), outside a sandbox.
func fileManagerShow(path string) error {
	conn, err := sessionBus()
	if err != nil {
		return fsE("io")
	}
	u := (&url.URL{Scheme: "file", Path: path}).String()
	err = conn.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1").
		Call("org.freedesktop.FileManager1.ShowItems", 0, []string{u}, activationToken()).Err
	if err != nil {
		return fsE("io")
	}
	return nil
}

// logPortalError: a portal failure worth a line in the log (never a path's
// content, never a secret).
func logPortalError(what string, err error) {
	if err != nil {
		log.Printf("portal: %s: %v", what, err)
	}
}
