//go:build windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A working copy downloaded (begin, write, end) with the given content.
func testEdit(t *testing.T, l *localFS, content string) (*editSession, *fsWriteEndResult) {
	t.Helper()
	b, err := l.edits.begin(editBeginOpts{Profile: "a1b2", DeviceID: "42", RemotePath: `C:\inetpub\web.config`, Name: "web.config", Size: int64(len(content))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.write(b.Handle, 0, base64.StdEncoding.EncodeToString([]byte(content))); err != nil {
		t.Fatal(err)
	}
	end, err := l.writeEnd(b.Handle)
	if err != nil {
		t.Fatal(err)
	}
	return l.edits.byID(b.EditID), end
}

// save: the editor saves (a new content, a later date).
func save(t *testing.T, e *editSession, content string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(e.file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(e.file, at, at); err != nil {
		t.Fatal(err)
	}
}

func looks(m *editManager, e *editSession, n int) []editEvent {
	var out []editEvent
	for i := 0; i < n; i++ {
		out = append(out, m.check(e)...)
	}
	return out
}

func TestEditSessionRoundTrip(t *testing.T) {
	l, _, root := testLocalFS(t)
	m := l.edits
	e, end := testEdit(t, l, "<config/>")
	if end.Binary || !strings.HasPrefix(e.dir, filepath.Join(root, "edit", "a1b2_42")+`\`) || filepath.Base(e.file) != "web.config" {
		t.Fatalf("working copy %q (%v)", e.file, end.Binary)
	}
	// Marked as coming from the Internet, as any download (security: the
	// default application of a document opens it protected).
	if motw, err := os.ReadFile(e.file + ":Zone.Identifier"); err != nil || !strings.Contains(string(motw), "ZoneId=3") {
		t.Fatalf("a working copy is marked (Mark-of-the-Web): %q %v", motw, err)
	}
	if filepath.Dir(e.file) != filepath.Join(e.dir, editFileDir) || end.Editor != "notepad" {
		t.Fatalf("the working copy alone in its folder, Notepad planned: %q %q", e.file, end.Editor)
	}
	if marker, err := os.ReadFile(filepath.Join(e.dir, editMarker)); err != nil || !strings.Contains(string(marker), `"remotePath": "C:\\inetpub\\web.config"`) {
		t.Fatalf("marker %s %v", marker, err)
	}
	// The same remote file again: exists, with its id.
	_, err := m.begin(editBeginOpts{Profile: "a1b2", DeviceID: "42", RemotePath: `C:\inetpub\web.config`, Name: "web.config", Size: 1})
	var fe *fsErr
	if !errors.As(err, &fe) || fe.Code != "exists" || fe.Extra["editId"] != e.ID {
		t.Fatalf("edited twice: %v", err)
	}
	if evs := looks(m, e, 4); len(evs) != 0 {
		t.Fatalf("no change, no event: %+v", evs)
	}
	// A save: seen, stable for two looks, then reported (seq 1).
	t0 := time.Now().Add(time.Minute)
	save(t, e, "<config a='1'/>", t0)
	if evs := looks(m, e, 2); len(evs) != 0 {
		t.Fatalf("reported before two stable looks: %+v", evs)
	}
	evs := m.check(e)
	if len(evs) != 1 || evs[0].Type != "changed" || evs[0].Seq != 1 || evs[0].Size != 15 {
		t.Fatalf("changed: %+v", evs)
	}
	// Its snapshot is frozen: saved again meanwhile, the snapshot keeps seq 1.
	save(t, e, "<config a='2'/>", t0.Add(time.Second))
	sn, err := m.snapshot(e.ID, 1)
	if err != nil || sn.Size != 15 {
		t.Fatal(sn, err)
	}
	r, _ := l.read(sn.Handle, 0, 15)
	if raw, _ := base64.StdEncoding.DecodeString(r.Data); string(raw) != "<config a='1'/>" {
		t.Fatalf("snapshot content %q", raw)
	}
	l.closeRead(sn.Handle)
	if !m.synced(e.ID, 1) {
		t.Fatal("synced")
	}
	// The second save: seq 2; the snapshot of seq 1 is now stale.
	evs = looks(m, e, 3)
	if len(evs) != 1 || evs[0].Seq != 2 {
		t.Fatalf("the next save: %+v", evs)
	}
	if _, err := m.snapshot(e.ID, 1); codeOf(err) != "stale" {
		t.Fatalf("an older seq: %v", err)
	}
	// Saved back as the device has it (seq 1's content): nothing to send.
	m.synced(e.ID, 2)
	save(t, e, "<config a='2'/>", t0.Add(2*time.Second))
	if evs := looks(m, e, 4); len(evs) != 0 {
		t.Fatalf("a save without a change: %+v", evs)
	}
	// A burst of saves: one event, the latest content.
	for i, c := range []string{"x1", "x22", "x333"} {
		save(t, e, c, t0.Add(time.Duration(3+i)*time.Second))
		m.check(e)
	}
	evs = looks(m, e, 3)
	if len(evs) != 1 || evs[0].Size != 4 || evs[0].Seq != 3 {
		t.Fatalf("a burst: %+v", evs)
	}
	if l := m.list(); len(l) != 1 || !l[0].Dirty || l[0].Seq != 3 || l[0].State != "watching" || l[0].RemotePath != `C:\inetpub\web.config` {
		t.Fatalf("list: %+v", l)
	}
	// Missing for 10 s, then back.
	now := time.Now()
	m.now = func() time.Time { return now }
	os.Remove(e.file)
	if evs := looks(m, e, 2); len(evs) != 0 {
		t.Fatalf("missing too soon: %+v", evs)
	}
	now = now.Add(11 * time.Second)
	if evs := m.check(e); len(evs) != 1 || evs[0].Type != "missing" {
		t.Fatalf("missing: %+v", evs)
	}
	save(t, e, "back", t0.Add(10*time.Second))
	if evs := m.check(e); len(evs) != 1 || evs[0].Type != "back" {
		t.Fatalf("back: %+v", evs)
	}
	if evs := looks(m, e, 3); len(evs) != 1 || evs[0].Type != "changed" {
		t.Fatalf("back, then its content: %+v", evs)
	}
	// Over 100 MB: tooLarge, never a snapshot.
	f, _ := os.OpenFile(e.file, os.O_WRONLY, 0)
	_ = f.Truncate(localMaxFileBytes + 1)
	f.Close()
	if evs := looks(m, e, 3); len(evs) != 1 || evs[0].Type != "tooLarge" {
		t.Fatalf("too large: %+v", evs)
	}
	// Stop: unsynced (the device has not the latest), then forced: gone.
	save(t, e, "unsent", t0.Add(20*time.Second))
	if _, err := m.stop(e.ID, editStopOpts{}); codeOf(err) != "unsynced" {
		t.Fatalf("stop with changes: %v", err)
	}
	if len(m.list()) != 1 {
		t.Fatal("refused stop: still listed")
	}
	if r, err := m.stop(e.ID, editStopOpts{Force: true}); err != nil || !r.Stopped {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(e.dir); !os.IsNotExist(err) {
		t.Fatal("stopped: its folder is left")
	}
	if len(m.list()) != 0 {
		t.Fatal("stopped: still listed")
	}
}

func TestEditSessionStopKeepAndBinary(t *testing.T) {
	l, _, _ := testLocalFS(t)
	m := l.edits
	e, end := testEdit(t, l, "MZ\x00\x01binary")
	if !end.Binary {
		t.Fatal("a NUL byte in the first 8 KB: binary")
	}
	// Synced (nothing changed): stop deletes it.
	if r, err := m.stop(e.ID, editStopOpts{}); err != nil || !r.Stopped || r.Kept {
		t.Fatal(r, err)
	}
	e2, _ := testEdit(t, l, "text")
	save(t, e2, "changed", time.Now().Add(time.Minute))
	if r, err := m.stop(e2.ID, editStopOpts{Keep: true}); err != nil || !r.Kept {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(e2.file); err != nil {
		t.Fatal("kept: its changes are on disk")
	}
	if len(m.list()) != 0 {
		t.Fatal("kept: no longer watched")
	}
	// Invalid ids are refused; the root is never left.
	for _, o := range []editBeginOpts{{Profile: "../x", DeviceID: "1", RemotePath: "/a", Name: "a"}, {Profile: "p", DeviceID: "1/2", RemotePath: "/a", Name: "a"},
		{Profile: "p", DeviceID: "", RemotePath: "/a", Name: "a"}, {Profile: "p", DeviceID: "1", RemotePath: "", Name: "a"}, {Profile: "p", DeviceID: "1", RemotePath: "/a\nb", Name: "a"}} {
		if _, err := m.begin(o); !errors.Is(err, errFsBadRequest) {
			t.Errorf("begin(%+v): %v", o, err)
		}
	}
	if _, err := m.begin(editBeginOpts{Profile: "p", DeviceID: "1", RemotePath: "/big", Name: "big", Size: localMaxFileBytes + 1}); codeOf(err) != "too_large" {
		t.Errorf("over 100 MB: %v", err)
	}
	// A remote name that is a path: one element in the working folder.
	b, err := m.begin(editBeginOpts{Profile: "", DeviceID: "7", RemotePath: "/etc/../x", Name: "../../evil.exe", Size: 0})
	if err != nil || b.LocalName != ".._.._evil.exe" {
		t.Fatalf("a name with separators: %+v %v", b, err)
	}
	e3 := m.byID(b.EditID)
	if !strings.Contains(e3.dir, `\local_7\`) || filepath.Dir(e3.file) != filepath.Join(e3.dir, editFileDir) {
		t.Fatalf("working folder %q", e3.file)
	}
	l.writeAbort(b.Handle)
	if _, err := m.launch(b.EditID); codeOf(err) != "not_found" {
		t.Fatalf("launched without its copy: %v", err)
	}
	m.stop(b.EditID, editStopOpts{Force: true})
	// Reload: the device's content over the working copy (a new baseline).
	e4, _ := testEdit(t, l, "v1")
	save(t, e4, "local", time.Now().Add(time.Minute))
	h, err := m.reload(e4.ID, fsWriteOpts{Size: 6})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.reload(e4.ID, fsWriteOpts{Size: 6}); !errors.Is(err, errFsBusy) {
		t.Fatalf("two reloads at once: %v", err)
	}
	_, _ = l.write(h.Handle, 0, base64.StdEncoding.EncodeToString([]byte("remote")))
	if _, err := l.writeEnd(h.Handle); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(e4.file); string(got) != "remote" {
		t.Fatalf("reloaded: %q", got)
	}
	if evs := looks(m, e4, 4); len(evs) != 0 {
		t.Fatalf("a reload is not a change: %+v", evs)
	}
	if r, err := m.stop(e4.ID, editStopOpts{}); err != nil || !r.Stopped {
		t.Fatal(r, err)
	}
}

func TestEditSessionCap(t *testing.T) {
	l, _, _ := testLocalFS(t)
	for i := 0; i < editMaxSessions; i++ {
		b, err := l.edits.begin(editBeginOpts{Profile: "p", DeviceID: "1", RemotePath: "/f" + itoa64(int64(i)), Name: "f", Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		l.writeAbort(b.Handle)
	}
	if _, err := l.edits.begin(editBeginOpts{Profile: "p", DeviceID: "1", RemotePath: "/one-more", Name: "f", Size: 1}); codeOf(err) != "limit" {
		t.Fatalf("51st: %v", err)
	}
}

func TestEditShutdownAndSweep(t *testing.T) {
	l, _, root := testLocalFS(t)
	m := l.edits
	synced, _ := testEdit(t, l, "same")
	b, _ := m.begin(editBeginOpts{Profile: "a1b2", DeviceID: "43", RemotePath: "/etc/hosts", Name: "hosts", Size: 2})
	_, _ = l.write(b.Handle, 0, base64.StdEncoding.EncodeToString([]byte("h1")))
	_, _ = l.writeEnd(b.Handle)
	dirty := m.byID(b.EditID)
	save(t, dirty, "h2-unsent", time.Now().Add(time.Minute))
	m.shutdown()
	if _, err := os.Stat(synced.dir); !os.IsNotExist(err) {
		t.Fatal("a clean exit: the synced copy is left")
	}
	if _, err := os.Stat(dirty.file); err != nil {
		t.Fatal("a clean exit: the copy with changes is deleted")
	}
	// The sweep: only what is older than a day, under the root; a junction
	// in there goes without what it names.
	editRoot := filepath.Join(root, "edit")
	old := filepath.Join(editRoot, "x_1", "0011223344556677")
	mustWrite(t, filepath.Join(old, "a.txt"), "old")
	past := time.Now().Add(-25 * time.Hour)
	_ = os.Chtimes(filepath.Join(old, "a.txt"), past, past)
	_ = os.Chtimes(old, past, past)
	outside := filepath.Join(root, "outside")
	mustWrite(t, filepath.Join(outside, "keep.txt"), "keep")
	if junction(t, filepath.Join(old, "link"), outside) {
		_ = os.Chtimes(old, past, past)
	}
	sweepEditRoot(editRoot, time.Now(), editSweepAge)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("an old working copy is left")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatal("the sweep followed a junction out of its root")
	}
	if _, err := os.Stat(dirty.file); err != nil {
		t.Fatal("the sweep took a recent copy")
	}
	// Never outside the root, nor the root itself.
	removeTreeUnder(editRoot, outside)
	removeTreeUnder(editRoot, editRoot)
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatal("removeTreeUnder left its root")
	}
	if _, err := os.Stat(editRoot); err != nil {
		t.Fatal("removeTreeUnder removed its root")
	}
}

func TestEditLauncher(t *testing.T) {
	l, _, _ := testLocalFS(t)
	e, _ := testEdit(t, l, "x")
	var started [][2]string
	var system []string
	dangerous := false
	was1, was2, was3 := editStartProgram, editSystemLaunch, editDangerousExt
	defer func() { editStartProgram, editSystemLaunch, editDangerousExt = was1, was2, was3 }()
	editStartProgram = func(prog, file, dir string) error { started = append(started, [2]string{prog, file}); return nil }
	editSystemLaunch = func(file, dir, ext string) error { system = append(system, ext); return nil }
	editDangerousExt = func(string) bool { return dangerous }
	win := os.Getenv("SystemRoot")
	// Notepad by its absolute path.
	r, err := launchEditor(e, FilesSettings{}.normalized())
	if err != nil || r.Editor != "notepad" || len(started) != 1 || !strings.EqualFold(started[0][0], filepath.Join(win, "System32", "notepad.exe")) || started[0][1] != e.file {
		t.Fatalf("notepad: %+v %v %v", r, err, started)
	}
	// The default application: not for a dangerous type, nor a script.
	r, _ = launchEditor(e, FilesSettings{Editor: "system"})
	if r.Editor != "system" || len(system) != 1 || system[0] != ".config" {
		t.Fatalf("system: %+v %v", r, system)
	}
	dangerous = true
	r, _ = launchEditor(e, FilesSettings{Editor: "system"})
	if r.Editor != "notepad" || r.Fallback != "notepad" || len(system) != 1 {
		t.Fatalf("a dangerous type: %+v", r)
	}
	dangerous = false
	py := &editSession{file: filepath.Join(e.dir, "run.py"), dir: e.dir}
	if r, _ := launchEditor(py, FilesSettings{Editor: "system"}); r.Fallback != "notepad" {
		t.Fatalf("a script: %+v", r)
	}
	// A custom editor: checked again at launch.
	if _, err := launchEditor(e, FilesSettings{Editor: "custom", EditorPath: filepath.Join(win, "System32", "cmd.exe")}); codeOf(err) != "dangerous_editor" {
		t.Fatalf("cmd.exe as the editor: %v", err)
	}
	n := len(started)
	np := filepath.Join(win, "System32", "notepad.exe")
	if r, err := launchEditor(e, FilesSettings{Editor: "custom", EditorPath: np}); err != nil || r.Editor != "custom" || len(started) != n+1 || started[n][0] != np {
		t.Fatalf("a custom editor: %+v %v", r, err)
	}
	// Through the manager: launched only when its copy exists.
	l.edits.launcher = launchEditor
	if r, err := l.edits.launch(e.ID); err != nil || r.Editor != "notepad" {
		t.Fatalf("launch: %+v %v", r, err)
	}
	if _, err := l.edits.launch("nope"); codeOf(err) != "stale_handle" {
		t.Fatalf("an unknown id: %v", err)
	}
}

func TestEditorPathOK(t *testing.T) {
	win := os.Getenv("SystemRoot")
	sys := filepath.Join(win, "System32")
	for _, p := range []string{filepath.Join(sys, "cmd.exe"), filepath.Join(sys, "WindowsPowerShell", "v1.0", "powershell.exe"), filepath.Join(sys, "wscript.exe"),
		filepath.Join(sys, "mshta.exe"), filepath.Join(sys, "rundll32.exe"), filepath.Join(win, "explorer.exe"), `C:\Python312\python.exe`, `C:\x\pythonw.exe`,
		filepath.Join(sys, "notepad.txt"), `\\evil\share\editor.exe`, `C:\x\..\cmd.exe`} {
		if err := editorPathOK(p); codeOf(err) != "dangerous_editor" {
			t.Errorf("editorPathOK(%q) = %v, want dangerous_editor", p, err)
		}
	}
	if err := editorPathOK(`C:\nope\editor.exe`); codeOf(err) != "no_editor" {
		t.Errorf("a missing program: %v", err)
	}
	if err := editorPathOK(filepath.Join(sys, "notepad.exe")); err != nil {
		t.Errorf("notepad: %v", err)
	}
	if n := editorDisplayName(filepath.Join(sys, "notepad.exe")); n == "" || strings.ContainsAny(n, "\x00\r\n") {
		t.Errorf("a program's name: %q", n)
	}
}

func TestFilesSettings(t *testing.T) {
	l, _, _ := testLocalFS(t)
	var stored FilesSettings
	l.settings = func() FilesSettings { return stored.normalized() }
	l.setSettings = func(s FilesSettings) { stored = s }
	v := l.settingsView()
	if v.Editor != "notepad" || v.WarnMB != 10 || v.ConfirmUpload || v.ShowHidden || v.EditorPath != "" {
		t.Fatalf("defaults: %+v", v)
	}
	// Custom without a program picked: not taken; no path from the page.
	v = l.applySettings(filesSettingsIn{Editor: "custom", WarnMB: 25, ConfirmUpload: true, ShowHidden: true})
	if v.Editor != "notepad" || v.WarnMB != 25 || !v.ConfirmUpload || !v.ShowHidden {
		t.Fatalf("set: %+v", v)
	}
	v = l.applySettings(filesSettingsIn{Editor: "system", WarnMB: 7})
	if v.Editor != "system" || v.WarnMB != 25 {
		t.Fatalf("an unknown size is not taken: %+v", v)
	}
	var in filesSettingsIn
	if err := json.Unmarshal([]byte(`{"editor":"custom","editorPath":"C:\\Windows\\System32\\cmd.exe"}`), &in); err != nil {
		t.Fatal(err)
	}
	l.applySettings(in)
	if stored.EditorPath != "" || stored.Editor == "custom" {
		t.Fatalf("the page set a program: %+v", stored)
	}
	// The dialog: a launcher refused, an editor kept.
	var pushes []string
	l.push = func(id string, ok bool, p []byte) {
		pushes = append(pushes, id+"|"+map[bool]string{true: "ok", false: "ko"}[ok]+"|"+string(p))
	}
	pick := ""
	l.pickEditor = func(done func(string, bool)) { done(pick, pick != "") }
	pick = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	_ = l.pickEditorAsync("r0000001")
	pick = filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	_ = l.pickEditorAsync("r0000002")
	pick = ""
	_ = l.pickEditorAsync("r0000003")
	if len(pushes) != 3 || pushes[0] != `r0000001|ko|{"code":"dangerous_editor"}` || !strings.HasPrefix(pushes[1], `r0000002|ok|{"picked":true,"editorPath":`) || pushes[2] != `r0000003|ok|{"picked":false}` {
		t.Fatalf("picks: %v", pushes)
	}
	if stored.Editor != "custom" || !strings.EqualFold(filepath.Base(stored.EditorPath), "notepad.exe") {
		t.Fatalf("the program picked is kept: %+v", stored)
	}
	if l.pending.Load() != 0 {
		t.Fatal("pending count")
	}
}
