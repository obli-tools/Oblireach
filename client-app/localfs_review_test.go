//go:build windows

package main

// localfs_review_test.go — the hardening of the split view and the editing
// round trip: the Recycle Bin never deletes for good in silence, the
// app's own data out of the local pane's reach, working copies marked and in
// their own folder, tied to their server's address, the device's baseline
// kept across a reload, the editor planned before a binary warning, long
// existing names sent.

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecycleNeverSilentlyPermanent(t *testing.T) {
	if recycleFlags&fofAllowUndo == 0 {
		t.Fatal("recycling without FOF_ALLOWUNDO would delete for good")
	}
	if recycleFlags&fofWantNukeWarning == 0 {
		t.Fatal("an item the Recycle Bin cannot take must be warned about (FOF_WANTNUKEWARNING), never deleted for good in silence")
	}
	if recycleFlags&fofNoConfirmation == 0 || recycleFlags&fofSilent == 0 || recycleFlags&fofNoErrorUI == 0 {
		t.Fatalf("flags %#x", recycleFlags)
	}
	// The main window owns Windows' warning.
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.hwnd = 0x1234
	was := recycleOwner.Load()
	defer recycleOwner.Store(was)
	l, _, _ := testLocalFS(t)
	b.attachLocalFS(l)
	defer b.shutdown()
	if recycleOwner.Load() != 0x1234 {
		t.Fatalf("the warning's owner: %#x", recycleOwner.Load())
	}
}

func TestNewLocalFSProtectsAppData(t *testing.T) {
	local, app := t.TempDir(), t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", app)
	old := configDir
	configDir = filepath.Join(app, "OblireachClient")
	defer func() { configDir = old }()
	l := newLocalFS(nil)
	defer l.closeAll()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(app, filepath.Base(exe)) // go-webview2's default profile folder
	for _, p := range []string{
		filepath.Join(local, "OblireachClient", "x.txt"),
		filepath.Join(local, "OblireachClient", "edit", "A_42", "0123", "f", "web.config"),
		filepath.Join(web, "EBWebView", "Default", "Cookies"),
		filepath.Join(app, "OblireachClient", "session.json"),
		filepath.Join(app, "Microsoft", "Word", "STARTUP", "auto.dotm"),
		filepath.Join(app, "Microsoft", "Excel", "XLSTART", "auto.xlam"),
		filepath.Join(app, "Microsoft", "AddIns", "x.ppam"),
	} {
		if !l.writeProtected(p) {
			t.Errorf("%s writable from the local pane", p)
		}
	}
	for _, p := range []string{filepath.Join(local, "OblireachClient", "edit", "x"), filepath.Join(web, "Local State"), filepath.Join(app, "OblireachClient", "config.json")} {
		if !l.readProtected(p) {
			t.Errorf("%s readable from the local pane", p)
		}
	}
	if l.readProtected(filepath.Join(app, "Other", "f.txt")) || l.writeProtected(filepath.Join(local, "Other", "f.txt")) {
		t.Error("the rest of AppData is the operator's")
	}
	if _, err := l.list(filepath.Join(local, "OblireachClient"), fsListOpts{}); codeOf(err) != "denied" {
		t.Errorf("listing the app's own folder: %v", err)
	}
}

func TestEditWorkingCopyNeverMeetsBookkeeping(t *testing.T) {
	for _, name := range []string{".oblireach", ".OBLIREACH", ".oblireach-edit.json"} {
		l, _, _ := testLocalFS(t)
		m := l.edits
		clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
		m.now = func() time.Time { return clock }
		b, err := m.begin(editBeginOpts{Profile: "a1", DeviceID: "42", RemotePath: "/srv/" + name, Name: name, Size: 2})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.write(b.Handle, 0, base64.StdEncoding.EncodeToString([]byte("v1"))); err != nil {
			t.Fatal(err)
		}
		if _, err := l.writeEnd(b.Handle); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		e := m.byID(b.EditID)
		if filepath.Base(e.file) != name || filepath.Dir(e.file) != filepath.Join(e.dir, editFileDir) {
			t.Fatalf("%s: working copy %q", name, e.file)
		}
		if marker, err := os.ReadFile(filepath.Join(e.dir, editMarker)); err != nil || !strings.Contains(string(marker), `"remotePath"`) {
			t.Fatalf("%s: the marker overwritten: %s %v", name, marker, err)
		}
		save(t, e, "v2 saved", clock.Add(time.Minute))
		evs := looks(m, e, 3)
		if len(evs) != 1 || evs[0].Type != "changed" || evs[0].Seq != 1 {
			t.Fatalf("%s: a save of a file named like the bookkeeping is reported: %+v", name, evs)
		}
		sn, err := m.snapshot(e.ID, 1)
		if err != nil || sn.Size != int64(len("v2 saved")) {
			t.Fatalf("%s: its snapshot: %+v %v", name, sn, err)
		}
		l.closeRead(sn.Handle)
	}
}

func TestEditTiedToServerAddress(t *testing.T) {
	l, _, _ := testLocalFS(t)
	m := l.edits
	addr := map[string]string{"A": "https://obl-a.example", "": "https://obl-a.example"}
	m.origin = func(p string) string { return addr[p] }
	if _, err := m.begin(editBeginOpts{Profile: "nope", DeviceID: "42", RemotePath: "/x", Name: "x", Size: 1}); codeOf(err) != "server_changed" {
		t.Fatalf("a profile this app does not know: %v", err)
	}
	b, err := m.begin(editBeginOpts{Profile: "A", DeviceID: "42", RemotePath: `C:\www\web.config`, Name: "web.config", Size: 5, Modified: "2026-09-29T08:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.write(b.Handle, 0, base64.StdEncoding.EncodeToString([]byte("v1..."))); err != nil {
		t.Fatal(err)
	}
	if _, err := l.writeEnd(b.Handle); err != nil {
		t.Fatal(err)
	}
	e := m.byID(b.EditID)
	// The device's baseline: from the download, then as the page lists it.
	got := m.list()
	if len(got) != 1 || got[0].Remote != (editRemote{Size: 5, Modified: "2026-09-29T08:00:00Z"}) {
		t.Fatalf("the baseline of a download: %+v", got)
	}
	if !m.setRemote(e.ID, editRemote{Size: 7, Modified: "2026-09-29T09:00:00Z"}) || m.setRemote("nope", editRemote{}) || m.setRemote(e.ID, editRemote{Size: -1}) {
		t.Fatal("setRemote")
	}
	m.setRemote(e.ID, editRemote{Size: 7, Modified: "2026-09-29T09:00:00Z\x1b[2J"})
	if r := m.list()[0].Remote; r.Size != 7 || r.Modified != "" {
		t.Fatalf("a date with control characters is not kept: %+v", r)
	}
	m.setRemote(e.ID, editRemote{Size: 7, Modified: "2026-09-29T09:00:00Z"})
	if r := m.list()[0].Remote; r != (editRemote{Size: 7, Modified: "2026-09-29T09:00:00Z"}) {
		t.Fatalf("the baseline kept: %+v", r)
	}
	// A save, then the profile's address rewritten (the sign-in page): never sent elsewhere.
	clock := time.Now().Add(time.Hour)
	save(t, e, "mine, unsent", clock)
	evs := looks(m, e, 3)
	if len(evs) != 1 || evs[0].Seq != 1 {
		t.Fatalf("the save: %+v", evs)
	}
	addr["A"] = "https://other.example"
	if _, err := m.snapshot(e.ID, 1); codeOf(err) != "server_changed" {
		t.Fatalf("a snapshot for another server: %v", err)
	}
	if _, err := m.reload(e.ID, fsWriteOpts{Size: 1}); codeOf(err) != "server_changed" {
		t.Fatalf("a reload from another server: %v", err)
	}
	// After a reload of the page: not listed; stopped, its unsent copy kept on disk.
	if l := m.list(); len(l) != 0 {
		t.Fatalf("listed for another server: %+v", l)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.byID(e.ID) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.byID(e.ID) != nil {
		t.Fatal("still watched")
	}
	if _, err := os.Stat(e.file); err != nil {
		t.Fatal("its unsent changes are kept on disk")
	}
	// A synced one of a moved server: deleted.
	addr["a1b2"] = "https://obl-b.example"
	e2, _ := testEdit(t, l, "same")
	if e2.origin != "https://obl-b.example" {
		t.Fatalf("tied to %q", e2.origin)
	}
	addr["a1b2"] = "https://moved.example"
	m.list()
	for m.byID(e2.ID) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(e2.dir); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(e2.dir); !os.IsNotExist(err) {
		t.Fatal("a synced copy of a moved server is deleted")
	}
}

func TestProxyProfileOrigin(t *testing.T) {
	p := &Proxy{cfg: &Config{ServerURL: "https://Obl-A.example/", Profiles: []ServerProfile{{ID: "A", ServerURL: "https://Obl-A.example"}, {ID: "B", ServerURL: "http://10.0.0.5:3000/x"}}, ActiveID: "A"}}
	if p.profileOrigin("A") != "https://obl-a.example" || p.profileOrigin("B") != "http://10.0.0.5:3000" || p.profileOrigin("") != "https://obl-a.example" || p.profileOrigin("C") != "" {
		t.Fatalf("origins: %q %q %q %q", p.profileOrigin("A"), p.profileOrigin("B"), p.profileOrigin(""), p.profileOrigin("C"))
	}
	p.cfg.ServerURL = "https://other.example"
	p.cfg.syncActive() // (the sign-in page of single-server mode: the same id, another address)
	if p.profileOrigin("A") != "https://other.example" {
		t.Fatalf("rewritten in place: %q", p.profileOrigin("A"))
	}
}

func TestPlannedEditor(t *testing.T) {
	was := editDangerousExt
	defer func() { editDangerousExt = was }()
	dangerous := map[string]bool{".xyz": true}
	editDangerousExt = func(ext string) bool { return dangerous[ext] }
	cases := []struct {
		file, editor, want string
	}{
		{`C:\e\f\report.docx`, "notepad", "notepad"},
		{`C:\e\f\report.docx`, "system", "system"},
		{`C:\e\f\report.docx`, "custom", "custom"},
		{`C:\e\f\run.ps1`, "system", "notepad"},
		{`C:\e\f\tool.exe`, "system", "notepad"},
		{`C:\e\f\odd.xyz`, "system", "notepad"},
		{`C:\e\f\Makefile`, "system", "notepad"},
	}
	for _, c := range cases {
		if got := plannedEditor(c.file, FilesSettings{Editor: c.editor, EditorPath: `C:\x\e.exe`}); got != c.want {
			t.Errorf("plannedEditor(%s, %s) = %s, want %s", c.file, c.editor, got, c.want)
		}
	}
	// writeEnd of a working copy says it (the page warns of a binary only for Notepad).
	l, _, _ := testLocalFS(t)
	l.settings = func() FilesSettings { return FilesSettings{Editor: "system"}.normalized() }
	b, err := l.edits.begin(editBeginOpts{Profile: "a1", DeviceID: "42", RemotePath: "/r.docx", Name: "r.docx", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	l.write(b.Handle, 0, base64.StdEncoding.EncodeToString([]byte{'P', 'K', 0, 3}))
	end, err := l.writeEnd(b.Handle)
	if err != nil || !end.Binary || end.Editor != "system" {
		t.Fatalf("a binary for its default application: %+v %v", end, err)
	}
}

func TestWalkLongExistingName(t *testing.T) {
	l, _, root := testLocalFS(t)
	w := filepath.Join(root, "w")
	long := strings.Repeat("r", 226) + ".pdf" // 230 UTF-16 units: a legal NTFS name, over the 200 this app creates
	mustWrite(t, filepath.Join(w, long), "%PDF")
	mustWrite(t, filepath.Join(w, "b.txt"), "b")
	r, err := l.walk(w, []string{"b.txt", long})
	if err != nil || len(r.Items) != 2 || r.Items[1].Rel != long || r.TotalBytes != 5 {
		t.Fatalf("a long existing name is sent with the others: %+v %v", r, err)
	}
	for _, n := range []string{strings.Repeat("r", 256), "a/b", `a\b`, "..", "CON", "x.", "x ", "a:b", "a\x01"} {
		if _, err := l.walk(w, []string{n}); codeOf(err) != "invalid_name" {
			t.Errorf("walk(%q): %v", n, err)
		}
	}
}

func TestNewestUnderTwoLevels(t *testing.T) {
	root := t.TempDir()
	sess := filepath.Join(root, "A_42", "0123")
	mustWrite(t, filepath.Join(sess, editFileDir, "web.config"), "x")
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	for _, p := range []string{filepath.Join(sess, editFileDir), sess} {
		os.Chtimes(p, old, old)
	}
	os.Chtimes(filepath.Join(sess, editFileDir, "web.config"), recent, recent)
	if got := newestUnder(sess); got.Before(recent.Add(-time.Second)) {
		t.Fatalf("the working copy's own date counts: %v", got)
	}
	sweepEditRoot(root, time.Now(), editSweepAge)
	if _, err := os.Stat(filepath.Join(sess, editFileDir, "web.config")); err != nil {
		t.Fatal("a copy saved an hour ago is not swept")
	}
}
