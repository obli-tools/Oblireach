package main

// localfs.go — the operator's own files, for the Files page's split view
// (local_pane.go, transfers.go) and the editing round trip (editsession.go).
//
// The page is a WebView fed by server data: it never touches the local disk
// itself. Everything goes through the narrow bindings of this file (their
// JS side: localfs_bridge.go attachLocalFS), which exist on the MAIN window's
// bridge (all of them) and on the session windows' (the local pane's file
// bindings only, attachSharedLocalFS: the editing round trip and the settings
// written stay the main window's), carry the per-run token that
// only the top-level local UI document (/) is given, and answer
// asynchronously (window.__reachLocalFs(reqId, ok, json), behind the same
// origin / path / top check). Rules, whatever the page asks:
//   - paths: absolute drive paths only (X:\...): no raw UNC path (\\server:
//     no NTLM towards a host a name came from), no device namespace (\\?\,
//     \\.\, \??\), no ".." nor "." element, no wildcard nor alternate data
//     stream, no reserved device name (CON, NUL, COM1...), no element ending
//     in a dot or a space, 1024 UTF-16 units at most (localPathOK);
//   - names that reach the disk (a remote file's, a folder created, a
//     rename) are ONE element, sanitised here (sanitizeLocalName): whatever
//     the server sent, it never becomes a path;
//   - protected locations (Windows, Program Files, the Start menus and
//     Startup folders, WindowsApps, PowerShell profile folders, Office's
//     start-up and add-in folders, the app's own folders and its WebView2
//     profile): nothing is written, created, renamed nor recycled there,
//     judged on the path AND on its final path (junctions and links
//     resolved); the app's data (its session cookies, its WebView2 profile,
//     the working copies) is not even read;
//   - a write is a hidden temporary file (O_EXCL) renamed at the end, its
//     size exactly the one announced, marked as coming from the Internet
//     (Mark-of-the-Web), an editing working copy too;
//   - deletion only to the Recycle Bin, on fixed drives, never a drive root
//     nor a known folder (the page asks the operator first); an item the
//     Recycle Bin cannot take is never deleted for good without Windows'
//     own warning (localfs_windows.go recycleFlags);
//   - nothing here starts a process: editsession.go reachEditLaunch is the
//     one binding that does, on a working copy it owns.
//
// Errors reach the page as { code } (plus details for a few), never as free
// text: the page says them in the operator's language.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
)

const (
	localMaxFileBytes   = 100 << 20 // one file, both ways (the explorer's EXPLORER_MAX_BYTES)
	localReadChunk      = 786432    // 768 KiB: a multiple of 3, the chunks' base64 join
	localMaxWriteB64    = 1 << 20   // base64 characters per reachFsWrite (786432 bytes)
	localMaxReads       = 64
	localMaxWrites      = 16
	localHandleIdle     = time.Hour
	localMaxList        = 20000
	localWalkMaxEntries = 10000
	localWalkMaxDepth   = 32
	localWalkMaxBytes   = int64(10) << 30
	// A transfer streamed in chunks (a local SSH connection's SFTP,
	// localconn_explorer.go: never one message): one file, a batch, at most.
	localMaxStreamBytes = int64(1) << 40
	localMaxPending     = 256
	localWorkers        = 4
	localMaxPathUnits   = 1024
	localMaxNameUnits   = 200
	localMaxTrash       = 1000
	localDriveFixed     = 3 // GetDriveTypeW DRIVE_FIXED
)

// ── Errors ─────────────────────────────────────────────────────────────────

// fsErr: a refusal the page says in words (its code), with details for a
// few (exists: the file there and a free name; limit: how far it got).
type fsErr struct {
	Code  string
	Extra map[string]interface{}
}

func (e *fsErr) Error() string { return e.Code }

func fsE(code string) *fsErr { return &fsErr{Code: code} }

func fsEx(code string, extra map[string]interface{}) *fsErr { return &fsErr{Code: code, Extra: extra} }

var (
	errFsBadRequest = errors.New("invalid_request")
	errFsBusy       = errors.New("busy")
)

// Windows error numbers the page tells apart.
const (
	winErrSharingViolation = 32
	winErrLockViolation    = 33
	winErrDirNotEmpty      = 145
	winErrAlreadyExists    = 183
	winErrFileExists       = 80
)

// fsCodeOf: the code of any error (an OS error mapped).
func fsCodeOf(err error) string {
	var fe *fsErr
	if errors.As(err, &fe) {
		return fe.Code
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		switch en {
		case winErrSharingViolation, winErrLockViolation:
			return "in_use"
		case winErrAlreadyExists, winErrFileExists:
			return "exists"
		}
	}
	switch {
	case os.IsNotExist(err):
		return "not_found"
	case os.IsPermission(err):
		return "denied"
	case os.IsExist(err):
		return "exists"
	}
	return "io"
}

// fsErrJSON: what the page gets for a failure: {"code": ...[, details]}.
func fsErrJSON(err error) []byte {
	out := map[string]interface{}{}
	var fe *fsErr
	if errors.As(err, &fe) {
		for k, v := range fe.Extra {
			out[k] = v
		}
	}
	out["code"] = fsCodeOf(err)
	b, _ := json.Marshal(out)
	return b
}

// ── Paths and names ────────────────────────────────────────────────────────

var reservedDeviceNames = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
	for _, p := range []string{"COM", "LPT"} {
		for _, d := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00B9", "\u00B2", "\u00B3"} {
			m[p+d] = true
		}
	}
	return m
}()

// reservedDeviceName: a name Windows takes for a device (with or without an
// extension, trailing spaces before it ignored as Windows does).
func reservedDeviceName(name string) bool {
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	return reservedDeviceNames[strings.ToUpper(base)]
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func isDriveLetter(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

// (localPathOK, isDriveRoot, localExistingNameOK, pathUnder, samePath: the
// platform's path rules, localfs_path_windows.go / localfs_path_linux.go.)

// portableNameOK: one element Windows accepts, up to NTFS' 255 UTF-16 units
// (the 200 of sanitizeLocalName are for the names this app creates; a longer
// existing name is still sent): Windows' localExistingNameOK, and on every
// platform the names a remote side sends for files written here (scp.go).
func portableNameOK(n string) bool {
	if n == "" || n == "." || n == ".." || utf16Len(n) > 255 {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	return !strings.HasSuffix(n, ".") && !strings.HasSuffix(n, " ") && !reservedDeviceName(n)
}

// sanitizeLocalName: one path element Windows accepts, from any name (a
// remote file's, a folder's): separators, wildcards, ':' and control
// characters become '_', trailing dots and spaces go, a reserved device name
// gets a '_' in front, 200 UTF-16 units at most (its extension kept), never
// empty ("file").
func sanitizeLocalName(n string) string {
	var b strings.Builder
	for _, r := range n {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimRight(b.String(), ". ")
	if s == "" {
		return "file"
	}
	if reservedDeviceName(s) {
		s = "_" + s
	}
	if utf16Len(s) > localMaxNameUnits {
		ext := filepath.Ext(s)
		if ext == s || utf16Len(ext) > 16 {
			ext = ""
		}
		base := []rune(strings.TrimSuffix(s, ext))
		for len(base) > 0 && utf16Len(string(base))+utf16Len(ext) > localMaxNameUnits {
			base = base[:len(base)-1]
		}
		bs := strings.TrimRight(string(base), ". ")
		if bs == "" {
			bs = "file"
		}
		s = bs + ext
	}
	return s
}

// localNameOK: a name the page gives for a new element (a folder created, a
// rename): exactly one that sanitizing leaves as it is.
func localNameOK(n string) bool {
	return n != "" && n != "." && n != ".." && sanitizeLocalName(n) == n
}

// nameWithN: "report.pdf" -> "report (2).pdf" (a dot file keeps its name).
func nameWithN(name string, n int) string {
	ext := filepath.Ext(name)
	if ext == name {
		ext = ""
	}
	return strings.TrimSuffix(name, ext) + " (" + strconv.Itoa(n) + ")" + ext
}

func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if pathUnder(p, r) {
			return true
		}
	}
	return false
}

// The caps of a listing and of a batch (variables: the tests lower them).
var (
	localListCap = localMaxList
	localWalkCap = localWalkMaxEntries
)

var reqIDRe = regexp.MustCompile(`^[0-9a-f-]{8,64}$`)

func reqIDOK(id string) bool { return reqIDRe.MatchString(id) }

func newHandleID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func isoTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ── The operating system's side ────────────────────────────────────────────

// localEntry: one element of a folder as the disk has it (never following a
// link: Link is a symbolic link, a junction or another name surrogate).
type localEntry struct {
	Name     string
	IsDir    bool
	Size     int64
	Modified time.Time
	Hidden   bool
	System   bool
	ReadOnly bool
	Link     bool
}

type localPlace struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type localDrive struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}

type localRoots struct {
	Computer string       `json:"computer"`
	Home     string       `json:"home"`
	Places   []localPlace `json:"places"`
	Drives   []localDrive `json:"drives"`
}

// localSys: what the disk and the shell do (localfs_windows.go winSys; the
// tests replace a few).
type localSys interface {
	readDir(dir string) ([]localEntry, error)
	lstat(path string) (localEntry, error)
	finalPath(path string) (string, error)
	driveType(path string) uint32
	roots() localRoots
	setHidden(path string)
	markOfTheWeb(path string) error
	moveNoReplace(src, dst string) error
	moveReplace(src, dst string) error
	recycle(paths []string) error
	reveal(path string) error
}

// ── The local file system of the main window ───────────────────────────────

type readHandle struct {
	path string
	f    *os.File
	size int64
	mod  time.Time
	last time.Time
}

type writeHandle struct {
	mu                     sync.Mutex // its writes, in order; its end
	done                   bool
	dir, target, tmp, name string
	f                      *os.File
	size, written          int64
	mod                    time.Time
	conflict               string
	last                   time.Time
	edit                   *editSession // a working copy (editsession.go)
	sum                    hash.Hash
	head                   []byte // its first 8 KB (binary or text)
}

type localFS struct {
	sys       localSys
	protectW  []string // nothing written, created, renamed nor recycled under
	protectR  []string // not even read (the app's settings: its session cookies)
	keepRoots []string // never renamed nor recycled themselves (the known folders)
	editRoot  string   // the working copies (editsession.go)

	push        func(reqID string, ok bool, payload []byte)
	pickEditor  func(done func(path string, ok bool)) // the native dialog (the window's UI thread)
	settings    func() FilesSettings
	setSettings func(FilesSettings)
	now         func() time.Time

	sem     chan struct{}
	pending atomic.Int32

	mu     sync.Mutex
	reads  map[string]*readHandle
	writes map[string]*writeHandle

	edits *editManager
}

func newLocalFSWith(sys localSys, editRoot string) *localFS {
	l := &localFS{
		sys:         sys,
		editRoot:    editRoot,
		now:         time.Now,
		sem:         make(chan struct{}, localWorkers),
		reads:       map[string]*readHandle{},
		writes:      map[string]*writeHandle{},
		settings:    func() FilesSettings { return FilesSettings{}.normalized() },
		setSettings: func(FilesSettings) {},
		push:        func(string, bool, []byte) {},
	}
	l.edits = newEditManager(l, editRoot)
	return l
}

// run: an asynchronous call. At most localMaxPending wait, localWorkers run
// at once; the answer goes to the page (push) once done.
func (l *localFS) run(reqID string, fn func() (interface{}, error)) error {
	return l.runTo(reqID, fn, nil)
}

// runTo: run, its answer to the page of the window that asked (to; nil:
// push, the main window's). The main window and the session windows share
// this local file system (their bridges: localfs_bridge.go fsAsync).
func (l *localFS) runTo(reqID string, fn func() (interface{}, error), to func(reqID string, ok bool, payload []byte)) error {
	push := to
	if push == nil {
		push = l.push
	}
	if l.pending.Add(1) > localMaxPending {
		l.pending.Add(-1)
		return errFsBusy
	}
	go func() {
		l.sem <- struct{}{}
		res, err := func() (res interface{}, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fsE("io")
				}
			}()
			return fn()
		}()
		<-l.sem
		l.pending.Add(-1)
		if err != nil {
			push(reqID, false, fsErrJSON(err))
			return
		}
		b, e := json.Marshal(res)
		if e != nil {
			push(reqID, false, fsErrJSON(fsE("io")))
			return
		}
		push(reqID, true, b)
	}()
	return nil
}

// resolved: the path and, when it can be opened, its final path (every
// junction and link on its way followed).
func (l *localFS) resolved(p string) []string {
	out := []string{p}
	if f, err := l.sys.finalPath(p); err == nil && f != "" && !samePath(f, p) {
		out = append(out, f)
	}
	return out
}

func (l *localFS) writeProtected(p string) bool {
	for _, x := range l.resolved(p) {
		if underAny(x, l.protectW) || (l.editRoot != "" && pathUnder(x, l.editRoot)) {
			return true
		}
	}
	return false
}

func (l *localFS) readProtected(p string) bool {
	for _, x := range l.resolved(p) {
		if underAny(x, l.protectR) {
			return true
		}
	}
	return false
}

// keptRoot: a drive root or a known folder itself (never renamed nor recycled).
func (l *localFS) keptRoot(p string) bool {
	if isDriveRoot(p) {
		return true
	}
	for _, k := range l.keepRoots {
		if samePath(filepath.Clean(k), p) {
			return true
		}
	}
	return false
}

// ── Listing ────────────────────────────────────────────────────────────────

type fsListOpts struct {
	Hidden bool `json:"hidden"`
}

type fsListEntry struct {
	Name     string `json:"name"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Hidden   bool   `json:"hidden"`
	System   bool   `json:"system"`
	Link     bool   `json:"link"`
	ReadOnly bool   `json:"readonly"`
}

type fsListResult struct {
	Path      string        `json:"path"`
	Parent    string        `json:"parent"`
	Entries   []fsListEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

func (l *localFS) list(path string, o fsListOpts) (*fsListResult, error) {
	p, err := localPathOK(path)
	if err != nil {
		return nil, err
	}
	if l.readProtected(p) {
		return nil, fsE("denied")
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fsE("not_dir")
	}
	ents, err := l.sys.readDir(p)
	if err != nil {
		return nil, err
	}
	res := &fsListResult{Path: p, Entries: []fsListEntry{}}
	if !isDriveRoot(p) {
		res.Parent = filepath.Dir(p)
	}
	for _, e := range ents {
		if e.Hidden && !o.Hidden {
			continue
		}
		if len(res.Entries) >= localListCap {
			res.Truncated = true
			break
		}
		fe := fsListEntry{Name: e.Name, IsDir: e.IsDir, Modified: isoTime(e.Modified), Hidden: e.Hidden, System: e.System, Link: e.Link, ReadOnly: e.ReadOnly}
		if !e.IsDir {
			fe.Size = e.Size
		}
		res.Entries = append(res.Entries, fe)
	}
	return res, nil
}

// ── A selection, folders included (uploads) ────────────────────────────────

type fsWalkItem struct {
	Rel      string `json:"rel"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`
}

type fsWalkSkipped struct {
	Links         int      `json:"links"`
	Denied        int      `json:"denied"`
	TooLarge      int      `json:"tooLarge"`
	TooLargeNames []string `json:"tooLargeNames,omitempty"`
}

type fsWalkResult struct {
	Items      []fsWalkItem  `json:"items"`
	TotalBytes int64         `json:"totalBytes"`
	Skipped    fsWalkSkipped `json:"skipped"`
}

type walkState struct {
	l       *localFS
	res     *fsWalkResult
	entries int
	// The limits: one file, the batch (walk: the explorer's; walkLarge: a
	// streamed transfer's).
	maxFile, maxTotal int64
}

func (w *walkState) limit(reason string) error {
	return fsEx("limit", map[string]interface{}{"reason": reason, "entries": w.entries, "bytes": w.res.TotalBytes})
}

func (w *walkState) tooLarge(rel string) {
	w.res.Skipped.TooLarge++
	if len(w.res.Skipped.TooLargeNames) < 50 {
		w.res.Skipped.TooLargeNames = append(w.res.Skipped.TooLargeNames, rel)
	}
}

// add: one element found (the limits: entries, bytes).
func (w *walkState) add(it fsWalkItem) error {
	w.entries++
	if w.entries > localWalkCap {
		return w.limit("entries")
	}
	if !it.IsDir {
		w.res.TotalBytes += it.Size
		if w.res.TotalBytes > w.maxTotal {
			return w.limit("bytes")
		}
	}
	w.res.Items = append(w.res.Items, it)
	return nil
}

func (w *walkState) dir(abs, rel string, depth int) error {
	if depth > localWalkMaxDepth {
		return w.limit("depth")
	}
	if w.l.readProtected(abs) {
		w.res.Skipped.Denied++
		return nil
	}
	ents, err := w.l.sys.readDir(abs)
	if err != nil {
		w.res.Skipped.Denied++
		return nil
	}
	for _, e := range ents {
		r := rel + "/" + e.Name
		switch {
		case e.Link:
			w.res.Skipped.Links++
		case e.IsDir:
			if err := w.add(fsWalkItem{Rel: r, IsDir: true, Modified: isoTime(e.Modified)}); err != nil {
				return err
			}
			if err := w.dir(filepath.Join(abs, e.Name), r, depth+1); err != nil {
				return err
			}
		case e.Size > w.maxFile:
			w.tooLarge(r)
		default:
			if err := w.add(fsWalkItem{Rel: r, Size: e.Size, Modified: isoTime(e.Modified)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// walk: the elements names of dir, folders with everything in them (each
// folder before its contents), links never followed, the limits of a batch.
func (l *localFS) walk(dir string, names []string) (*fsWalkResult, error) {
	return l.walkMax(dir, names, localMaxFileBytes, localWalkMaxBytes)
}

// walkLarge: walk for a transfer streamed in chunks (a local SSH
// connection's): no 100 MB per file.
func (l *localFS) walkLarge(dir string, names []string) (*fsWalkResult, error) {
	return l.walkMax(dir, names, localMaxStreamBytes, localMaxStreamBytes)
}

func (l *localFS) walkMax(dir string, names []string, maxFile, maxTotal int64) (*fsWalkResult, error) {
	d, err := localPathOK(dir)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 || len(names) > localWalkMaxEntries {
		return nil, errFsBadRequest
	}
	if l.readProtected(d) {
		return nil, fsE("denied")
	}
	w := &walkState{l: l, res: &fsWalkResult{Items: []fsWalkItem{}}, maxFile: maxFile, maxTotal: maxTotal}
	for _, n := range names {
		if !localExistingNameOK(n) {
			return nil, fsE("invalid_name")
		}
		abs := filepath.Join(d, n)
		e, err := l.sys.lstat(abs)
		if err != nil {
			w.res.Skipped.Denied++
			continue
		}
		switch {
		case e.Link:
			w.res.Skipped.Links++
		case e.IsDir:
			if err := w.add(fsWalkItem{Rel: n, IsDir: true, Modified: isoTime(e.Modified)}); err != nil {
				return nil, err
			}
			if err := w.dir(abs, n, 1); err != nil {
				return nil, err
			}
		case e.Size > w.maxFile:
			w.tooLarge(n)
		default:
			if err := w.add(fsWalkItem{Rel: n, Size: e.Size, Modified: isoTime(e.Modified)}); err != nil {
				return nil, err
			}
		}
	}
	return w.res, nil
}

// ── Reading (uploads) ──────────────────────────────────────────────────────

type fsOpenResult struct {
	Handle   string `json:"handle"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

func (l *localFS) openRead(path string) (*fsOpenResult, error) {
	return l.openReadMax(path, localMaxFileBytes)
}

// openReadLarge: openRead for a transfer streamed in chunks (a local SSH
// connection's): no 100 MB per file.
func (l *localFS) openReadLarge(path string) (*fsOpenResult, error) {
	return l.openReadMax(path, localMaxStreamBytes)
}

func (l *localFS) openReadMax(path string, maxFile int64) (*fsOpenResult, error) {
	p, err := localPathOK(path)
	if err != nil {
		return nil, err
	}
	if l.readProtected(p) {
		return nil, fsE("denied")
	}
	e, err := l.sys.lstat(p)
	if err != nil {
		return nil, err
	}
	switch {
	case e.Link:
		return nil, fsE("is_link")
	case e.IsDir:
		return nil, fsE("is_dir")
	case e.Size > maxFile:
		return nil, fsE("too_large")
	}
	return l.openReadAtMax(p, maxFile)
}

// openReadAt: a read handle on a file already judged (a working copy's
// snapshot comes here directly).
func (l *localFS) openReadAt(p string) (*fsOpenResult, error) {
	return l.openReadAtMax(p, localMaxFileBytes)
}

func (l *localFS) openReadAtMax(p string, maxFile int64) (*fsOpenResult, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, fsE("is_dir")
	}
	if st.Size() > maxFile {
		f.Close()
		return nil, fsE("too_large")
	}
	now := l.now()
	id := newHandleID()
	l.mu.Lock()
	l.pruneLocked(now)
	for len(l.reads) >= localMaxReads {
		l.closeOldestReadLocked()
	}
	l.reads[id] = &readHandle{path: p, f: f, size: st.Size(), mod: st.ModTime(), last: now}
	l.mu.Unlock()
	return &fsOpenResult{Handle: id, Name: filepath.Base(p), Size: st.Size(), Modified: isoTime(st.ModTime())}, nil
}

func (l *localFS) closeOldestReadLocked() {
	var oldest string
	var t time.Time
	for k, h := range l.reads {
		if oldest == "" || h.last.Before(t) {
			oldest, t = k, h.last
		}
	}
	if h := l.reads[oldest]; h != nil {
		h.f.Close()
	}
	delete(l.reads, oldest)
}

type fsReadResult struct {
	Data string `json:"data"`
}

// read: length bytes at offset (a multiple of 3 unless it reaches the end:
// the page joins the chunks' base64). The file changed since it was opened:
// changed (what was read would not be one file).
func (l *localFS) read(handle string, offset int64, length int) (*fsReadResult, error) {
	l.mu.Lock()
	h := l.reads[handle]
	if h != nil {
		h.last = l.now()
	}
	l.mu.Unlock()
	if h == nil {
		return nil, fsE("stale_handle")
	}
	if offset < 0 || length <= 0 || length > localReadChunk || offset > h.size {
		return nil, errFsBadRequest
	}
	if length%3 != 0 && offset+int64(length) < h.size {
		return nil, errFsBadRequest
	}
	st, err := h.f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() != h.size || !st.ModTime().Equal(h.mod) {
		return nil, fsE("changed")
	}
	buf := make([]byte, length)
	n, err := h.f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &fsReadResult{Data: base64.StdEncoding.EncodeToString(buf[:n])}, nil
}

func (l *localFS) closeRead(handle string) {
	l.mu.Lock()
	h := l.reads[handle]
	delete(l.reads, handle)
	l.mu.Unlock()
	if h != nil {
		h.f.Close()
	}
}

// closeReadsUnder: the read handles on files of a folder (a working copy
// stopped).
func (l *localFS) closeReadsUnder(dir string) {
	l.mu.Lock()
	var gone []*readHandle
	for k, h := range l.reads {
		if pathUnder(h.path, dir) {
			gone = append(gone, h)
			delete(l.reads, k)
		}
	}
	l.mu.Unlock()
	for _, h := range gone {
		h.f.Close()
	}
}

// ── Writing (downloads, working copies) ────────────────────────────────────

type fsWriteOpts struct {
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Conflict string `json:"conflict"` // fail (default) | overwrite | rename
	// Large: a transfer streamed in chunks (a local SSH connection's SFTP):
	// no 100 MB per file.
	Large bool `json:"large"`
}

type fsExisting struct {
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	IsDir    bool   `json:"isDir,omitempty"`
}

type fsWriteBeginResult struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// freeName: the first "name (N).ext" free in dir (N 2..99), "" if none.
func (l *localFS) freeName(dir, name string) string {
	for i := 2; i <= 99; i++ {
		c := nameWithN(name, i)
		if _, err := l.sys.lstat(filepath.Join(dir, c)); err != nil {
			return c
		}
	}
	return ""
}

func (l *localFS) existsErr(dir, name string, e localEntry) error {
	return fsEx("exists", map[string]interface{}{
		"existing":  fsExisting{Size: e.Size, Modified: isoTime(e.Modified), IsDir: e.IsDir || e.Link},
		"suggested": l.freeName(dir, name),
	})
}

// writeBegin: a hidden temporary file in dir, for a file named name
// (sanitised) of size bytes. conflict: what an existing file there gets:
// fail (exists, with it and a free name), overwrite (a file, not read-only),
// rename (the next free "name (N).ext", decided at the end).
func (l *localFS) writeBegin(dir, name string, o fsWriteOpts) (*fsWriteBeginResult, error) {
	d, err := localPathOK(dir)
	if err != nil {
		return nil, err
	}
	if l.writeProtected(d) {
		return nil, fsE("protected")
	}
	st, err := os.Stat(d)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fsE("not_dir")
	}
	if o.Size < 0 {
		return nil, errFsBadRequest
	}
	if o.Size > localMaxFileBytes && !(o.Large && o.Size <= localMaxStreamBytes) {
		return nil, fsE("too_large")
	}
	switch o.Conflict {
	case "overwrite", "rename":
	default:
		o.Conflict = "fail"
	}
	final := sanitizeLocalName(name)
	target := filepath.Join(d, final)
	if e, err := l.sys.lstat(target); err == nil {
		switch {
		case e.IsDir || e.Link:
			if o.Conflict != "rename" {
				return nil, l.existsErr(d, final, e)
			}
		case o.Conflict == "fail":
			return nil, l.existsErr(d, final, e)
		case o.Conflict == "overwrite" && e.ReadOnly:
			return nil, fsE("denied")
		}
	}
	return l.beginTmp(d, final, target, o, nil)
}

func (l *localFS) beginTmp(dir, final, target string, o fsWriteOpts, edit *editSession) (*fsWriteBeginResult, error) {
	var mod time.Time
	if o.Modified != "" {
		if t, err := time.Parse(time.RFC3339, o.Modified); err == nil {
			mod = t
		}
	}
	now := l.now()
	l.mu.Lock()
	l.pruneLocked(now)
	full := len(l.writes) >= localMaxWrites
	l.mu.Unlock()
	if full {
		return nil, fsE("limit")
	}
	var f *os.File
	var tmp string
	for i := 0; i < 5; i++ {
		tmp = filepath.Join(dir, "."+final+".oblireach-part-"+randHex(3))
		var err error
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, err
		}
		f = nil
	}
	if f == nil {
		return nil, fsE("io")
	}
	l.sys.setHidden(tmp)
	h := &writeHandle{dir: dir, target: target, tmp: tmp, name: final, f: f, size: o.Size, mod: mod, conflict: o.Conflict, last: now, edit: edit}
	if edit != nil {
		h.sum = sha256.New()
	}
	id := newHandleID()
	l.mu.Lock()
	l.writes[id] = h
	l.mu.Unlock()
	return &fsWriteBeginResult{Handle: id, Name: final, Path: target}, nil
}

type fsWriteResult struct {
	Written int64 `json:"written"`
}

// write: the next bytes, in order (offset: what was written so far), never
// more than announced.
func (l *localFS) write(handle string, offset int64, b64 string) (*fsWriteResult, error) {
	if len(b64) > localMaxWriteB64 {
		return nil, errFsBadRequest
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errFsBadRequest
	}
	l.mu.Lock()
	h := l.writes[handle]
	l.mu.Unlock()
	if h == nil {
		return nil, fsE("stale_handle")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return nil, fsE("stale_handle")
	}
	if offset != h.written {
		return nil, errFsBadRequest
	}
	if h.written+int64(len(data)) > h.size {
		return nil, fsE("too_large")
	}
	if _, err := h.f.Write(data); err != nil {
		return nil, err
	}
	if h.sum != nil {
		h.sum.Write(data)
		if len(h.head) < 8192 {
			n := 8192 - len(h.head)
			if n > len(data) {
				n = len(data)
			}
			h.head = append(h.head, data[:n]...)
		}
	}
	h.written += int64(len(data))
	now := l.now()
	l.mu.Lock()
	h.last = now
	l.mu.Unlock()
	return &fsWriteResult{Written: h.written}, nil
}

// takeWrite: the handle leaves the list (its end, its abort); its writes
// on their way finish first.
func (l *localFS) takeWrite(handle string) *writeHandle {
	l.mu.Lock()
	h := l.writes[handle]
	delete(l.writes, handle)
	l.mu.Unlock()
	if h != nil {
		h.mu.Lock()
		h.done = true
		h.mu.Unlock()
	}
	return h
}

func (h *writeHandle) discard() {
	h.f.Close()
	os.Remove(h.tmp)
}

type fsWriteEndResult struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Binary bool   `json:"binary,omitempty"`
	// A working copy: the editor reachEditLaunch would start for it now
	// (notepad, system, custom; editsession_windows.go plannedEditor), so
	// the page warns of a binary file only when Notepad would open it.
	Editor string `json:"editor,omitempty"`
}

// writeEnd: the file is complete (exactly its size): flushed, dated as on
// the device, marked (Mark-of-the-Web, a working copy too: the default
// application of a document opens it protected), then renamed to its name as
// its conflict rule says.
func (l *localFS) writeEnd(handle string) (*fsWriteEndResult, error) {
	h := l.takeWrite(handle)
	if h == nil {
		return nil, fsE("stale_handle")
	}
	if h.written != h.size {
		h.discard()
		if h.edit != nil {
			l.edits.writeFailed(h.edit)
		}
		return nil, fsEx("io", map[string]interface{}{"reason": "size"})
	}
	if err := h.f.Sync(); err != nil {
		h.discard()
		if h.edit != nil {
			l.edits.writeFailed(h.edit)
		}
		return nil, err
	}
	h.f.Close()
	_ = l.sys.markOfTheWeb(h.tmp) // (a file system without streams: not marked)
	if !h.mod.IsZero() {          // (after the mark: writing its stream dates the file)
		_ = os.Chtimes(h.tmp, h.mod, h.mod)
	}
	if h.edit != nil {
		if err := l.sys.moveReplace(h.tmp, h.target); err != nil {
			os.Remove(h.tmp)
			l.edits.writeFailed(h.edit)
			return nil, err
		}
		var sum [32]byte
		copy(sum[:], h.sum.Sum(nil))
		binary := false
		for _, c := range h.head {
			if c == 0 {
				binary = true
				break
			}
		}
		l.edits.written(h.edit, sum)
		return &fsWriteEndResult{Path: h.target, Name: h.name, Binary: binary, Editor: plannedEditor(h.target, l.settings().normalized())}, nil
	}
	switch h.conflict {
	case "overwrite":
		if e, err := l.sys.lstat(h.target); err == nil && (e.IsDir || e.Link || e.ReadOnly) {
			os.Remove(h.tmp)
			return nil, fsE("denied")
		}
		if err := l.sys.moveReplace(h.tmp, h.target); err != nil {
			os.Remove(h.tmp)
			return nil, err
		}
		return &fsWriteEndResult{Path: h.target, Name: h.name}, nil
	case "rename":
		for i := 1; i <= 99; i++ {
			n := h.name
			if i > 1 {
				n = nameWithN(h.name, i)
			}
			dst := filepath.Join(h.dir, n)
			if _, err := l.sys.lstat(dst); err == nil {
				continue
			}
			if err := l.sys.moveNoReplace(h.tmp, dst); err == nil {
				return &fsWriteEndResult{Path: dst, Name: n}, nil
			} else if c := fsCodeOf(err); c != "exists" {
				os.Remove(h.tmp)
				return nil, err
			}
		}
		os.Remove(h.tmp)
		return nil, fsE("exists")
	default:
		if err := l.sys.moveNoReplace(h.tmp, h.target); err != nil {
			os.Remove(h.tmp)
			if fsCodeOf(err) == "exists" {
				if e, e2 := l.sys.lstat(h.target); e2 == nil {
					return nil, l.existsErr(h.dir, h.name, e)
				}
			}
			return nil, err
		}
		return &fsWriteEndResult{Path: h.target, Name: h.name}, nil
	}
}

func (l *localFS) writeAbort(handle string) {
	h := l.takeWrite(handle)
	if h == nil {
		return
	}
	h.discard()
	if h.edit != nil {
		l.edits.writeFailed(h.edit)
	}
}

// pruneLocked: handles idle for an hour go (a write's temporary file with it).
func (l *localFS) pruneLocked(now time.Time) {
	for k, h := range l.reads {
		if now.Sub(h.last) > localHandleIdle {
			h.f.Close()
			delete(l.reads, k)
		}
	}
	for k, h := range l.writes {
		if now.Sub(h.last) > localHandleIdle {
			delete(l.writes, k)
			h.mu.Lock()
			h.done = true
			h.mu.Unlock()
			h.discard()
			if h.edit != nil {
				go l.edits.writeFailed(h.edit)
			}
		}
	}
}

// closeAll: every handle closed, every temporary file deleted (the end).
func (l *localFS) closeAll() {
	l.mu.Lock()
	reads, writes := l.reads, l.writes
	l.reads, l.writes = map[string]*readHandle{}, map[string]*writeHandle{}
	l.mu.Unlock()
	for _, h := range reads {
		h.f.Close()
	}
	for _, h := range writes {
		h.mu.Lock()
		h.done = true
		h.mu.Unlock()
		h.discard()
	}
}

func (l *localFS) prune() {
	l.mu.Lock()
	l.pruneLocked(l.now())
	l.mu.Unlock()
}

// ── Folders, renames, the Recycle Bin, Explorer ────────────────────────────

type fsMkdirResult struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Existed bool   `json:"existed"`
}

// mkdir: a folder named name (sanitised) in parent; one already there is
// fine (existed).
func (l *localFS) mkdir(parent, name string) (*fsMkdirResult, error) {
	p, err := localPathOK(parent)
	if err != nil {
		return nil, err
	}
	if l.writeProtected(p) {
		return nil, fsE("protected")
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fsE("not_dir")
	}
	n := sanitizeLocalName(name)
	target := filepath.Join(p, n)
	if e, err := l.sys.lstat(target); err == nil {
		if e.IsDir && !e.Link {
			return &fsMkdirResult{Path: target, Name: n, Existed: true}, nil
		}
		return nil, l.existsErr(p, n, e)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return nil, err
	}
	return &fsMkdirResult{Path: target, Name: n}, nil
}

type fsPathResult struct {
	Path string `json:"path"`
}

// rename: within its folder only; the new name must be a valid one as it is.
func (l *localFS) rename(path, newName string) (*fsPathResult, error) {
	p, err := localPathOK(path)
	if err != nil {
		return nil, err
	}
	if !localNameOK(newName) {
		return nil, fsE("invalid_name")
	}
	if l.keptRoot(p) || l.writeProtected(p) {
		return nil, fsE("protected")
	}
	if _, err := l.sys.lstat(p); err != nil {
		return nil, err
	}
	dst := filepath.Join(filepath.Dir(p), newName)
	if dst == p {
		return &fsPathResult{Path: p}, nil
	}
	if e, err := l.sys.lstat(dst); err == nil && !samePath(dst, p) {
		return nil, l.existsErr(filepath.Dir(p), newName, e)
	}
	if err := l.sys.moveNoReplace(p, dst); err != nil {
		return nil, err
	}
	return &fsPathResult{Path: dst}, nil
}

type fsTrashResult struct {
	Trashed int `json:"trashed"`
}

// trash: to the Recycle Bin, nothing else (fixed drives only: elsewhere
// Windows would delete for good).
func (l *localFS) trash(paths []string) (*fsTrashResult, error) {
	if len(paths) == 0 || len(paths) > localMaxTrash {
		return nil, errFsBadRequest
	}
	var clean []string
	for _, raw := range paths {
		p, err := localPathOK(raw)
		if err != nil {
			return nil, err
		}
		if l.keptRoot(p) || l.writeProtected(p) {
			return nil, fsE("protected")
		}
		if _, err := l.sys.lstat(p); err != nil {
			return nil, err
		}
		if l.sys.driveType(p) != localDriveFixed {
			return nil, fsE("no_recycle")
		}
		for _, x := range l.resolved(p)[1:] {
			if l.sys.driveType(x) != localDriveFixed {
				return nil, fsE("no_recycle")
			}
		}
		clean = append(clean, p)
	}
	if err := l.sys.recycle(clean); err != nil {
		return nil, err
	}
	return &fsTrashResult{Trashed: len(clean)}, nil
}

// reveal: Windows Explorer on the element's folder, the element selected
// (never opened).
func (l *localFS) reveal(path string) error {
	p, err := localPathOK(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return err
	}
	return l.sys.reveal(p)
}

// sortedEntries: a folder's elements by name (the listing the page sorts
// itself; this keeps the cap deterministic).
func sortedEntries(es []localEntry) []localEntry {
	sort.Slice(es, func(i, j int) bool { return strings.ToLower(es[i].Name) < strings.ToLower(es[j].Name) })
	return es
}
