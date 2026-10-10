//go:build windows

package main

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSys: the real disk, with the drive type, the Recycle Bin and Explorer
// watched instead of used.
type fakeSys struct {
	winSys
	drive    uint32
	recycled [][]string
	revealed []string
}

func (f *fakeSys) driveType(string) uint32 {
	if f.drive == 0 {
		return localDriveFixed
	}
	return f.drive
}
func (f *fakeSys) recycle(p []string) error { f.recycled = append(f.recycled, p); return nil }
func (f *fakeSys) reveal(p string) error    { f.revealed = append(f.revealed, p); return nil }

func testLocalFS(t *testing.T) (*localFS, *fakeSys, string) {
	t.Helper()
	root := t.TempDir()
	fs := &fakeSys{}
	l := newLocalFSWith(fs, filepath.Join(root, "edit"))
	t.Cleanup(l.closeAll)
	return l, fs, root
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	return fsCodeOf(err)
}

func TestLocalPathOK(t *testing.T) {
	ok := map[string]string{
		`C:\`: `C:\`, `c:\Users\bob`: `c:\Users\bob`, `D:\a\b\`: `D:\a\b`, `Z:\share\file.txt`: `Z:\share\file.txt`,
		`C:/Users/bob`: `C:\Users\bob`, `C:\a\\b`: `C:\a\b`, `C:\.hidden`: `C:\.hidden`, `C:\con.d\x`: ``, `C:\report 2026.pdf`: `C:\report 2026.pdf`,
	}
	for in, want := range ok {
		got, err := localPathOK(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q accepted as %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("localPathOK(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", `relative\x`, `\\server\share\x`, `\\?\C:\x`, `\\.\PhysicalDrive0`, `\??\C:\x`, `C:\a\..\b`, `C:\a\.\b`,
		`C:\a\file.txt:Zone.Identifier`, `C:\a\b.`, `C:\a\b \c`, `C:\CON`, `C:\x\con.txt`, `C:\x\COM1`, "C:\\x\\com\u00B9.log", `C:\x\LPT9`,
		`C:\x\CONIN$`, `C:\x\nul `, "C:\\a\x01b", `C:\a*`, `C:\a?`, `C:\a"b`, `C:\a<b`, `C:\a|b`, `C:x`, `1:\x`, `C:\` + strings.Repeat("a", 1100)} {
		if got, err := localPathOK(bad); err == nil || codeOf(err) != "invalid_path" {
			t.Errorf("localPathOK(%q) = %q, %v: want invalid_path", bad, got, err)
		}
	}
}

func TestSanitizeLocalName(t *testing.T) {
	cases := map[string]string{
		"report.pdf": "report.pdf", "a/b": "a_b", `a\b`: "a_b", "a:b*c?d\"e<f>g|h": "a_b_c_d_e_f_g_h", "tab\there": "tab_here",
		"trailing. . ": "trailing", "CON": "_CON", "con.txt": "_con.txt", "COM1": "_COM1", "lpt\u00B2.x": "_lpt\u00B2.x", "": "file", "...": "file",
		"..": "file", ".": "file", ".bashrc": ".bashrc", "../../etc/passwd": ".._.._etc_passwd",
	}
	for in, want := range cases {
		if got := sanitizeLocalName(in); got != want {
			t.Errorf("sanitizeLocalName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("x", 300) + ".tar.gz"
	got := sanitizeLocalName(long)
	if utf16Len(got) != 200 || !strings.HasSuffix(got, ".gz") {
		t.Errorf("a long name: %d units, %q", utf16Len(got), got[len(got)-10:])
	}
	emoji := strings.Repeat("\U0001F600", 150) + ".txt" // 2 units each
	if got := sanitizeLocalName(emoji); utf16Len(got) > 200 || !strings.HasSuffix(got, ".txt") {
		t.Errorf("surrogate pairs: %d units", utf16Len(got))
	}
	if localNameOK("a:b") || localNameOK("x.") || localNameOK("..") || !localNameOK("Rapport final.docx") || localNameOK("") {
		t.Error("localNameOK")
	}
	if nameWithN("report.pdf", 2) != "report (2).pdf" || nameWithN(".bashrc", 3) != ".bashrc (3)" || nameWithN("a", 99) != "a (99)" {
		t.Error("nameWithN")
	}
}

func TestPathUnder(t *testing.T) {
	if !pathUnder(`C:\Windows\System32`, `c:\windows`) || !pathUnder(`C:\Windows`, `C:\Windows\`) || pathUnder(`C:\WindowsApps`, `C:\Windows`) ||
		!pathUnder(`D:\x`, `D:\`) || pathUnder(`D:\x`, "") {
		t.Error("pathUnder")
	}
}

func mustWrite(t *testing.T, p string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// junction: a directory junction (mklink /J); false when not permitted here.
func junction(t *testing.T, link, target string) bool {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Logf("mklink /J: %v %s", err, out)
		return false
	}
	return true
}

func TestLocalFSListAndProtection(t *testing.T) {
	l, _, root := testLocalFS(t)
	mustWrite(t, filepath.Join(root, "work", "a.txt"), "hello")
	mustWrite(t, filepath.Join(root, "work", "sub", "b.txt"), "b")
	mustWrite(t, filepath.Join(root, "work", ".secret"), "h")
	setFileAttrs(filepath.Join(root, "work", ".secret"), fileAttrHidden)
	mustWrite(t, filepath.Join(root, "prot", "x.dll"), "x")
	mustWrite(t, filepath.Join(root, "cfg", "session.json"), "cookie")
	l.protectW = []string{filepath.Join(root, "prot")}
	l.protectR = []string{filepath.Join(root, "cfg")}

	r, err := l.list(filepath.Join(root, "work"), fsListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range r.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "a.txt,sub" || r.Parent != root || r.Truncated {
		t.Fatalf("listing without hidden files: %v parent %q", names, r.Parent)
	}
	r, _ = l.list(filepath.Join(root, "work"), fsListOpts{Hidden: true})
	if len(r.Entries) != 3 || !r.Entries[0].Hidden || r.Entries[0].Name != ".secret" || r.Entries[1].Size != 5 || r.Entries[1].Modified == "" || !r.Entries[2].IsDir {
		t.Fatalf("listing with hidden files: %+v", r.Entries)
	}
	was := localListCap
	localListCap = 1
	r, _ = l.list(filepath.Join(root, "work"), fsListOpts{Hidden: true})
	localListCap = was
	if len(r.Entries) != 1 || !r.Truncated {
		t.Fatalf("cap: %+v", r)
	}
	if _, err := l.list(filepath.Join(root, "cfg"), fsListOpts{}); codeOf(err) != "denied" {
		t.Fatalf("the settings folder listed: %v", err)
	}
	if _, err := l.openRead(filepath.Join(root, "cfg", "session.json")); codeOf(err) != "denied" {
		t.Fatalf("the session cookies read: %v", err)
	}
	if _, err := l.list(filepath.Join(root, "work", "a.txt"), fsListOpts{}); codeOf(err) != "not_dir" {
		t.Fatalf("a file listed: %v", err)
	}
	if _, err := l.list(filepath.Join(root, "nope"), fsListOpts{}); codeOf(err) != "not_found" {
		t.Fatalf("missing: %v", err)
	}
	// Protected: nothing written, created, renamed nor recycled there.
	if _, err := l.writeBegin(filepath.Join(root, "prot"), "evil.dll", fsWriteOpts{Size: 1}); codeOf(err) != "protected" {
		t.Fatalf("write in a protected folder: %v", err)
	}
	if _, err := l.mkdir(filepath.Join(root, "prot"), "x"); codeOf(err) != "protected" {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := l.rename(filepath.Join(root, "prot", "x.dll"), "y.dll"); codeOf(err) != "protected" {
		t.Fatalf("rename: %v", err)
	}
	if _, err := l.trash([]string{filepath.Join(root, "prot", "x.dll")}); codeOf(err) != "protected" {
		t.Fatalf("trash: %v", err)
	}
	if _, err := l.writeBegin(l.editRoot, "x", fsWriteOpts{Size: 1}); codeOf(err) != "protected" && codeOf(err) != "not_found" {
		t.Fatalf("the working copies' root written: %v", err)
	}
	// Through a junction: judged on its final path.
	if junction(t, filepath.Join(root, "work", "j"), filepath.Join(root, "prot")) {
		if _, err := l.writeBegin(filepath.Join(root, "work", "j"), "evil.dll", fsWriteOpts{Size: 1}); codeOf(err) != "protected" {
			t.Fatalf("write through a junction: %v", err)
		}
		r, _ := l.list(filepath.Join(root, "work"), fsListOpts{})
		found := false
		for _, e := range r.Entries {
			if e.Name == "j" {
				found = e.Link && e.IsDir
			}
		}
		if !found {
			t.Fatalf("a junction is listed as a link: %+v", r.Entries)
		}
		if junction(t, filepath.Join(root, "work", "k"), filepath.Join(root, "cfg")) {
			if _, err := l.list(filepath.Join(root, "work", "k"), fsListOpts{}); codeOf(err) != "denied" {
				t.Fatalf("the settings folder through a junction: %v", err)
			}
		}
	}
}

func TestLocalFSWalk(t *testing.T) {
	l, _, root := testLocalFS(t)
	w := filepath.Join(root, "w")
	mustWrite(t, filepath.Join(w, "top.txt"), "12345")
	mustWrite(t, filepath.Join(w, "dir", "a.txt"), "aa")
	mustWrite(t, filepath.Join(w, "dir", "deep", "b.txt"), "b")
	if err := os.MkdirAll(filepath.Join(w, "dir", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	big, _ := os.Create(filepath.Join(w, "dir", "big.iso"))
	_ = big.Truncate(localMaxFileBytes + 1)
	big.Close()
	links := 0
	if junction(t, filepath.Join(w, "dir", "loop"), w) {
		links = 1
	}
	r, err := l.walk(w, []string{"top.txt", "dir"})
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, it := range r.Items {
		rels = append(rels, it.Rel)
	}
	if strings.Join(rels, ",") != "top.txt,dir,dir/a.txt,dir/deep,dir/deep/b.txt,dir/empty" {
		t.Fatalf("walk: %v", rels)
	}
	if r.TotalBytes != 8 || r.Skipped.TooLarge != 1 || r.Skipped.TooLargeNames[0] != "dir/big.iso" || r.Skipped.Links != links {
		t.Fatalf("walk counts: %+v", r)
	}
	if _, err := l.walk(w, []string{"../x"}); codeOf(err) != "invalid_name" {
		t.Fatalf("a name with a separator: %v", err)
	}
	was := localWalkCap
	localWalkCap = 3
	_, err = l.walk(w, []string{"top.txt", "dir"})
	localWalkCap = was
	var fe *fsErr
	if !errors.As(err, &fe) || fe.Code != "limit" || fe.Extra["reason"] != "entries" {
		t.Fatalf("entries cap: %v", err)
	}
	// Depth.
	p := filepath.Join(root, "d")
	for i := 0; i < localWalkMaxDepth+2; i++ {
		p = filepath.Join(p, "n")
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = l.walk(root, []string{"d"})
	if !errors.As(err, &fe) || fe.Code != "limit" || fe.Extra["reason"] != "depth" {
		t.Fatalf("depth cap: %v", err)
	}
}

func TestLocalFSWriteLifecycle(t *testing.T) {
	l, _, root := testLocalFS(t)
	dir := filepath.Join(root, "dl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("hello, remote file")
	b, err := l.writeBegin(dir, "../report?.txt", fsWriteOpts{Size: int64(len(data)), Modified: "2026-09-20T08:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != ".._report_.txt" || b.Path != filepath.Join(dir, ".._report_.txt") {
		t.Fatalf("the remote name sanitised: %+v", b)
	}
	h := l.writes[b.Handle]
	if !strings.HasPrefix(filepath.Base(h.tmp), "..._report_.txt.oblireach-part-") {
		t.Fatalf("temporary file %q", h.tmp)
	}
	if e, err := (winSys{}).lstat(h.tmp); err != nil || !e.Hidden {
		t.Fatalf("temporary file not hidden: %+v %v", e, err)
	}
	if _, err := l.write(b.Handle, 5, base64.StdEncoding.EncodeToString(data[:5])); !errors.Is(err, errFsBadRequest) {
		t.Fatalf("out of order: %v", err)
	}
	if _, err := l.write(b.Handle, 0, base64.StdEncoding.EncodeToString(append(data, 'x'))); codeOf(err) != "too_large" {
		t.Fatalf("more than announced: %v", err)
	}
	if r, err := l.write(b.Handle, 0, base64.StdEncoding.EncodeToString(data[:6])); err != nil || r.Written != 6 {
		t.Fatal(r, err)
	}
	if r, err := l.write(b.Handle, 6, base64.StdEncoding.EncodeToString(data[6:])); err != nil || r.Written != int64(len(data)) {
		t.Fatal(r, err)
	}
	end, err := l.writeEnd(b.Handle)
	if err != nil || end.Path != b.Path {
		t.Fatal(end, err)
	}
	got, _ := os.ReadFile(b.Path)
	st, _ := os.Stat(b.Path)
	if string(got) != string(data) || !st.ModTime().Equal(time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("content %q, date %v", got, st.ModTime())
	}
	if motw, err := os.ReadFile(b.Path + ":Zone.Identifier"); err != nil || !strings.Contains(string(motw), "ZoneId=3") {
		t.Fatalf("Mark-of-the-Web: %q %v", motw, err)
	}
	if _, err := os.Stat(h.tmp); !os.IsNotExist(err) {
		t.Fatal("temporary file left")
	}
	if _, err := l.writeEnd(b.Handle); codeOf(err) != "stale_handle" {
		t.Fatalf("a second end: %v", err)
	}
	// It exists now: fail (with the file there and a free name), rename, overwrite.
	_, err = l.writeBegin(dir, ".._report_.txt", fsWriteOpts{Size: 2})
	var fe *fsErr
	if !errors.As(err, &fe) || fe.Code != "exists" || fe.Extra["suggested"] != ".._report_ (2).txt" || fe.Extra["existing"].(fsExisting).Size != int64(len(data)) {
		t.Fatalf("conflict fail: %v %+v", err, fe)
	}
	put := func(name, conflict, content string) (*fsWriteEndResult, error) {
		b, err := l.writeBegin(dir, name, fsWriteOpts{Size: int64(len(content)), Conflict: conflict})
		if err != nil {
			return nil, err
		}
		if _, err := l.write(b.Handle, 0, base64.StdEncoding.EncodeToString([]byte(content))); err != nil {
			return nil, err
		}
		return l.writeEnd(b.Handle)
	}
	if r, err := put(".._report_.txt", "rename", "v2"); err != nil || r.Name != ".._report_ (2).txt" {
		t.Fatalf("keep both: %+v %v", r, err)
	}
	if r, err := put(".._report_.txt", "overwrite", "v3"); err != nil || r.Name != ".._report_.txt" {
		t.Fatalf("overwrite: %+v %v", r, err)
	}
	if got, _ := os.ReadFile(b.Path); string(got) != "v3" {
		t.Fatalf("overwritten: %q", got)
	}
	setFileAttrs(b.Path, fileAttrReadOnly)
	if _, err := put(".._report_.txt", "overwrite", "v4"); codeOf(err) != "denied" {
		t.Fatalf("a read-only file overwritten: %v", err)
	}
	// Size short at the end: refused, nothing left.
	b2, _ := l.writeBegin(dir, "short.bin", fsWriteOpts{Size: 10})
	tmp2 := l.writes[b2.Handle].tmp
	_, _ = l.write(b2.Handle, 0, base64.StdEncoding.EncodeToString([]byte("abc")))
	if _, err := l.writeEnd(b2.Handle); codeOf(err) != "io" {
		t.Fatalf("short: %v", err)
	}
	if _, err := os.Stat(tmp2); !os.IsNotExist(err) {
		t.Fatal("short: temporary file left")
	}
	if _, err := os.Stat(filepath.Join(dir, "short.bin")); !os.IsNotExist(err) {
		t.Fatal("short: a file was made")
	}
	// Abort; idle expiry.
	b3, _ := l.writeBegin(dir, "gone.bin", fsWriteOpts{Size: 10})
	tmp3 := l.writes[b3.Handle].tmp
	l.writeAbort(b3.Handle)
	if _, err := os.Stat(tmp3); !os.IsNotExist(err) {
		t.Fatal("abort: temporary file left")
	}
	b4, _ := l.writeBegin(dir, "idle.bin", fsWriteOpts{Size: 10})
	tmp4 := l.writes[b4.Handle].tmp
	l.now = func() time.Time { return time.Now().Add(2 * localHandleIdle) }
	l.prune()
	l.now = time.Now
	if _, ok := l.writes[b4.Handle]; ok {
		t.Fatal("idle write handle kept")
	}
	if _, err := os.Stat(tmp4); !os.IsNotExist(err) {
		t.Fatal("idle: temporary file left")
	}
	if _, err := l.writeBegin(dir, "x", fsWriteOpts{Size: localMaxFileBytes + 1}); codeOf(err) != "too_large" {
		t.Fatalf("over 100 MB: %v", err)
	}
	if _, err := l.writeBegin(filepath.Join(dir, "short.bin"), "x", fsWriteOpts{Size: 1}); codeOf(err) != "not_found" {
		t.Fatalf("into a missing folder: %v", err)
	}
	for i := 0; i < localMaxWrites; i++ {
		if _, err := l.writeBegin(dir, "many.bin", fsWriteOpts{Size: 1, Conflict: "rename"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.writeBegin(dir, "many.bin", fsWriteOpts{Size: 1}); codeOf(err) != "limit" {
		t.Fatalf("17th write handle: %v", err)
	}
}

func TestLocalFSReadHandles(t *testing.T) {
	l, _, root := testLocalFS(t)
	p := filepath.Join(root, "up.bin")
	data := make([]byte, 2*localReadChunk+10)
	for i := range data {
		data[i] = byte(i * 7)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := l.openRead(p)
	if err != nil || o.Size != int64(len(data)) || o.Name != "up.bin" {
		t.Fatal(o, err)
	}
	var joined strings.Builder
	for off := int64(0); off < o.Size; off += localReadChunk {
		n := localReadChunk
		if off+int64(n) > o.Size {
			n = int(o.Size - off)
		}
		r, err := l.read(o.Handle, off, n)
		if err != nil {
			t.Fatal(err)
		}
		joined.WriteString(r.Data)
	}
	if joined.String() != base64.StdEncoding.EncodeToString(data) {
		t.Fatal("the chunks' base64, joined, is not the file's")
	}
	if _, err := l.read(o.Handle, 0, 100); !errors.Is(err, errFsBadRequest) {
		t.Fatalf("a chunk not a multiple of 3 in the middle: %v", err)
	}
	if _, err := l.read(o.Handle, 0, localReadChunk+3); !errors.Is(err, errFsBadRequest) {
		t.Fatalf("a chunk too large: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(p, append(data, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.read(o.Handle, 0, 3); codeOf(err) != "changed" {
		t.Fatalf("changed while read: %v", err)
	}
	l.closeRead(o.Handle)
	if _, err := l.read(o.Handle, 0, 3); codeOf(err) != "stale_handle" {
		t.Fatalf("closed: %v", err)
	}
	if _, err := l.openRead(root); codeOf(err) != "is_dir" {
		t.Fatalf("a folder: %v", err)
	}
	big := filepath.Join(root, "big.bin")
	f, _ := os.Create(big)
	_ = f.Truncate(localMaxFileBytes + 1)
	f.Close()
	if _, err := l.openRead(big); codeOf(err) != "too_large" {
		t.Fatalf("over 100 MB: %v", err)
	}
	o2, _ := l.openRead(p)
	l.now = func() time.Time { return time.Now().Add(2 * localHandleIdle) }
	l.prune()
	l.now = time.Now
	if _, err := l.read(o2.Handle, 0, 3); codeOf(err) != "stale_handle" {
		t.Fatalf("an idle read handle: %v", err)
	}
}

func TestLocalFSMkdirRenameTrashReveal(t *testing.T) {
	l, fs, root := testLocalFS(t)
	d := filepath.Join(root, "d")
	mustWrite(t, filepath.Join(d, "a.txt"), "a")
	mustWrite(t, filepath.Join(d, "b.txt"), "b")
	r, err := l.mkdir(d, "New: folder")
	if err != nil || r.Name != "New_ folder" || r.Existed {
		t.Fatalf("mkdir: %+v %v", r, err)
	}
	if r, err := l.mkdir(d, "New_ folder"); err != nil || !r.Existed {
		t.Fatalf("mkdir again: %+v %v", r, err)
	}
	if _, err := l.mkdir(d, "a.txt"); codeOf(err) != "exists" {
		t.Fatalf("mkdir over a file: %v", err)
	}
	if _, err := l.rename(filepath.Join(d, "a.txt"), "x:y"); codeOf(err) != "invalid_name" {
		t.Fatalf("rename to an invalid name: %v", err)
	}
	if _, err := l.rename(filepath.Join(d, "a.txt"), "b.txt"); codeOf(err) != "exists" {
		t.Fatalf("rename over another: %v", err)
	}
	if p, err := l.rename(filepath.Join(d, "a.txt"), "A.txt"); err != nil || filepath.Base(p.Path) != "A.txt" {
		t.Fatalf("a change of case: %+v %v", p, err)
	}
	if _, err := l.rename(`C:\`, "x"); codeOf(err) != "protected" {
		t.Fatalf("a drive root renamed: %v", err)
	}
	l.keepRoots = []string{d}
	if _, err := l.trash([]string{d}); codeOf(err) != "protected" {
		t.Fatalf("a known folder recycled: %v", err)
	}
	l.keepRoots = nil
	if _, err := l.trash([]string{`C:\`}); codeOf(err) != "protected" {
		t.Fatalf("a drive root recycled: %v", err)
	}
	fs.drive = driveRemovable
	if _, err := l.trash([]string{filepath.Join(d, "b.txt")}); codeOf(err) != "no_recycle" {
		t.Fatalf("a removable drive: %v", err)
	}
	fs.drive = driveRemote
	if _, err := l.trash([]string{filepath.Join(d, "b.txt")}); codeOf(err) != "no_recycle" {
		t.Fatalf("a network drive: %v", err)
	}
	fs.drive = 0
	if tr, err := l.trash([]string{filepath.Join(d, "b.txt"), filepath.Join(d, "A.txt")}); err != nil || tr.Trashed != 2 || len(fs.recycled) != 1 || len(fs.recycled[0]) != 2 {
		t.Fatalf("trash: %+v %v %v", tr, err, fs.recycled)
	}
	if _, err := l.trash([]string{filepath.Join(d, "missing.txt")}); codeOf(err) != "not_found" {
		t.Fatalf("trash of nothing: %v", err)
	}
	if err := l.reveal(filepath.Join(d, "b.txt")); err != nil || len(fs.revealed) != 1 {
		t.Fatalf("reveal: %v", err)
	}
	if err := l.reveal(`\\evil\share\x`); codeOf(err) != "invalid_path" {
		t.Fatalf("reveal of a UNC path: %v", err)
	}
}

func TestFsErrJSON(t *testing.T) {
	if string(fsErrJSON(fsE("denied"))) != `{"code":"denied"}` {
		t.Error(string(fsErrJSON(fsE("denied"))))
	}
	if s := string(fsErrJSON(&os.PathError{Op: "open", Path: "x", Err: os.ErrNotExist})); s != `{"code":"not_found"}` {
		t.Error(s)
	}
	if s := string(fsErrJSON(errors.New("some text the page must not see"))); s != `{"code":"io"}` {
		t.Error(s)
	}
	if s := string(fsErrJSON(fsEx("exists", map[string]interface{}{"suggested": "a (2)", "code": "x"}))); s != `{"code":"exists","suggested":"a (2)"}` {
		t.Error(s)
	}
}

// The roots of this machine: its drives, its known folders.
func TestLocalRoots(t *testing.T) {
	r := winSys{}.roots()
	if r.Computer == "" || r.Home == "" || len(r.Drives) == 0 {
		t.Fatalf("roots: %+v", r)
	}
	sys := false
	for _, d := range r.Drives {
		if len(d.Path) != 3 || d.Kind == "" {
			t.Errorf("drive %+v", d)
		}
		sys = sys || strings.EqualFold(d.Path, os.Getenv("SystemDrive")+`\`)
	}
	if !sys {
		t.Errorf("the system drive is not listed: %+v", r.Drives)
	}
	if len(r.Places) == 0 || r.Places[0].ID != "home" {
		t.Errorf("places: %+v", r.Places)
	}
}
