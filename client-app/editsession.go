package main

// editsession.go — the editing round trip of the Files page (edit_page.go):
// a remote file opened in a local editor, sent back over the original at
// every save, as WinSCP does.
//
// The page downloads the file (FILE_EXPLORER_CMD download_file, the explorer's
// gated path) into a WORKING COPY this file owns:
//   %LOCALAPPDATA%\OblireachClient\edit\<profile>_<deviceId>\<16 hex>\f\<name>
// (<name> sanitised, alone in its folder f: whatever its name, it never meets
// the session's bookkeeping, the marker .oblireach-edit.json (data only) and
// the snapshots .oblireach\ one level up; Mark-of-the-Web on it, as on any
// download: Office and PDF readers open it protected),
// then asks reachEditLaunch to open it in the editor of Settings > Files:
// Notepad (by its absolute path), the default application of the file type
// (never for a type Windows deems dangerous, nor a script type: Notepad
// then), or a program the operator picked in the native file dialog (never
// an interpreter or a launcher). That is the only binding that starts a
// process, on a copy it owns: a downloaded file is never run.
//
// A watcher (one goroutine per main window) looks at every working copy
// every 700 ms (the exact file only: an editor's lock and temporary files
// beside it are ignored). A change must hold for two looks and the file be
// readable; then its content is hashed and, when it differs from what the
// device has (the baseline) and from what was last reported, FROZEN in a
// snapshot (.oblireach\snap-<seq>) and reported to the page ('changed', its
// seq). The page checks the device's copy did not change meanwhile, sends
// the snapshot (upload_file overwrite: true), then says it is synced (the
// new baseline). Missing for 10 s: 'missing' (back: 'back'); over 100 MB:
// 'tooLarge'. Stopping deletes the working copy, refused ('unsynced') while
// it holds changes the device does not have, unless forced or kept. On a
// clean exit synced copies go; at start-up copies older than a day go.
//
// A working copy is tied to its server's ADDRESS (the profile's origin when
// it began), not only to the profile's id: a profile's address can be
// rewritten in place (the sign-in page of single-server mode). Once it no
// longer matches, the copy is never sent (its snapshots refused:
// server_changed) and it no longer comes back after a reload (stopped, its
// unsent changes kept on disk). The device's side of the copy (its size and
// date when last read, reachEditRemote) is kept here too, so a page that
// reloaded still checks the device's file did not change before sending.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	editWatchEvery   = 700 * time.Millisecond
	editStableLooks  = 2
	editMissingAfter = 10 * time.Second
	editMaxSessions  = 50
	editSweepAge     = 24 * time.Hour
	editSnapDir      = ".oblireach"
	editMarker       = ".oblireach-edit.json"
	editFileDir      = "f" // the working copy's own folder (its name never meets the bookkeeping)
	editRemoteModMax = 64
)

// ── Settings > Files (config.json) ─────────────────────────────────────────

// FilesSettings: the editor (notepad | system | custom, custom with the
// program picked in the native dialog: the page never sets a path), the size
// above which editing asks first, whether an edited file is sent back
// without asking, whether the local pane shows hidden files.
type FilesSettings struct {
	Editor        string `json:"editor,omitempty"`
	EditorPath    string `json:"editorPath,omitempty"`
	WarnMB        int    `json:"warnMB,omitempty"`
	ConfirmUpload bool   `json:"confirmUpload,omitempty"`
	ShowHidden    bool   `json:"showHidden,omitempty"`
}

var editWarnSizes = []int{1, 5, 10, 25, 50}

func (s FilesSettings) normalized() FilesSettings {
	switch s.Editor {
	case "system":
	case "custom":
		if s.EditorPath == "" {
			s.Editor = "notepad"
		}
	default:
		s.Editor = "notepad"
	}
	ok := false
	for _, w := range editWarnSizes {
		ok = ok || s.WarnMB == w
	}
	if !ok {
		s.WarnMB = 10
	}
	return s
}

type filesSettingsView struct {
	Editor        string `json:"editor"`
	EditorPath    string `json:"editorPath"`
	EditorName    string `json:"editorName"`
	WarnMB        int    `json:"warnMB"`
	ConfirmUpload bool   `json:"confirmUpload"`
	ShowHidden    bool   `json:"showHidden"`
}

// filesSettingsIn: what the page may change (no path: only the dialog sets one).
type filesSettingsIn struct {
	Editor        string `json:"editor"`
	WarnMB        int    `json:"warnMB"`
	ConfirmUpload bool   `json:"confirmUpload"`
	ShowHidden    bool   `json:"showHidden"`
}

func (l *localFS) settingsView() filesSettingsView {
	s := l.settings().normalized()
	v := filesSettingsView{Editor: s.Editor, EditorPath: s.EditorPath, WarnMB: s.WarnMB, ConfirmUpload: s.ConfirmUpload, ShowHidden: s.ShowHidden}
	if s.EditorPath != "" {
		v.EditorName = editorDisplayName(s.EditorPath)
	}
	return v
}

func (l *localFS) applySettings(in filesSettingsIn) filesSettingsView {
	s := l.settings().normalized()
	switch in.Editor {
	case "notepad", "system":
		s.Editor = in.Editor
	case "custom":
		if s.EditorPath != "" {
			s.Editor = "custom"
		}
	}
	for _, w := range editWarnSizes {
		if in.WarnMB == w {
			s.WarnMB = w
		}
	}
	s.ConfirmUpload, s.ShowHidden = in.ConfirmUpload, in.ShowHidden
	l.setSettings(s.normalized())
	return l.settingsView()
}

type fsPickResult struct {
	Picked     bool   `json:"picked"`
	EditorPath string `json:"editorPath,omitempty"`
	EditorName string `json:"editorName,omitempty"`
}

// pickEditorAsync: the native file dialog (on the window's UI thread,
// pickEditor), then the program is judged; kept (Editor custom) when it may
// be an editor.
func (l *localFS) pickEditorAsync(reqID string) error {
	if l.pickEditor == nil {
		return errNotAllowed
	}
	if l.pending.Add(1) > localMaxPending {
		l.pending.Add(-1)
		return errFsBusy
	}
	l.pickEditor(func(path string, ok bool) {
		defer l.pending.Add(-1)
		if !ok {
			b, _ := json.Marshal(fsPickResult{})
			l.push(reqID, true, b)
			return
		}
		if err := editorPathOK(path); err != nil {
			l.push(reqID, false, fsErrJSON(err))
			return
		}
		s := l.settings().normalized()
		s.Editor, s.EditorPath = "custom", filepath.Clean(path)
		l.setSettings(s.normalized())
		b, _ := json.Marshal(fsPickResult{Picked: true, EditorPath: s.EditorPath, EditorName: editorDisplayName(s.EditorPath)})
		l.push(reqID, true, b)
	})
	return nil
}

// Programs that run what they are given (interpreters, launchers, shells):
// never an editor.
var editorDenied = map[string]bool{
	"cmd": true, "powershell": true, "pwsh": true, "powershell_ise": true, "wscript": true, "cscript": true, "mshta": true,
	"rundll32": true, "regsvr32": true, "msiexec": true, "conhost": true, "explorer": true, "wsl": true, "bash": true, "sh": true,
	"node": true, "java": true, "javaw": true, "hh": true, "control": true, "installutil": true, "msbuild": true, "forfiles": true,
	"schtasks": true, "sc": true, "reg": true, "regedit": true, "cmstp": true, "certutil": true, "bitsadmin": true,
	"py": true, "pyw": true, "wt": true, "windowsterminal": true, "runas": true, "mmc": true, "wmic": true, "perl": true, "ruby": true,
	"php": true, "dotnet": true, "csc": true, "vbc": true, "jsc": true, "cmdkey": true, "psexec": true, "psexec64": true,
}

var editorDeniedPrefix = []string{"python", "pwsh", "powershell"}

// editorPathOK: a program the operator may pick as the editor: a local .exe
// that exists, not an interpreter nor a launcher.
func editorPathOK(p string) error {
	if p == "" {
		return fsE("no_editor")
	}
	clean, err := localPathOK(p)
	if err != nil {
		return fsE("dangerous_editor")
	}
	if !strings.EqualFold(filepath.Ext(clean), ".exe") {
		return fsE("dangerous_editor")
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean)))
	if editorDenied[base] {
		return fsE("dangerous_editor")
	}
	for _, pre := range editorDeniedPrefix {
		if strings.HasPrefix(base, pre) {
			return fsE("dangerous_editor")
		}
	}
	st, err := os.Lstat(clean)
	if err != nil {
		return fsE("no_editor")
	}
	if !st.Mode().IsRegular() {
		return fsE("dangerous_editor")
	}
	return nil
}

// ── Working copies ─────────────────────────────────────────────────────────

type editSnap struct {
	path string
	size int64
	sum  [32]byte
}

type editSession struct {
	ID, Profile, DeviceID, RemotePath, Name string
	dir, file                               string
	created                                 time.Time
	origin                                  string // its server's address when it began (profileOrigin)

	remote editRemote // the device's file when last read (its size, its date): the page's baseline

	baseline, reported [32]byte // what the device has; what was last reported
	seq, syncedSeq     int64
	snaps              map[int64]editSnap
	writing            bool // a download (or a reload) on its way
	stopped            bool

	// The watcher's view of the file.
	obsSize      int64
	obsMod       time.Time
	dirty        bool
	stable       int
	missingSince time.Time
	missing      bool
	tooLarge     bool

	lastSyncAt time.Time
	launched   bool
}

// editRemote: the device's file as last listed (reachEditRemote), the page's
// baseline (a reload finds it again).
type editRemote struct {
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type editEvent struct {
	EditID string `json:"editId"`
	Type   string `json:"type"` // changed | missing | back | tooLarge
	Seq    int64  `json:"seq,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Sha256 string `json:"sha256,omitempty"`
}

type editManager struct {
	l    *localFS
	root string

	mu       sync.Mutex
	sessions map[string]*editSession

	push     func(editEvent)
	stat     func(string) (os.FileInfo, error)
	now      func() time.Time
	launcher func(e *editSession, s FilesSettings) (*editLaunchResult, error) // editsession_windows.go launchEditor
	origin   func(profile string) string                                      // a profile's server address now (Proxy.profileOrigin; nil: tests)
}

// originOf: the address of a profile's server now ("" without a Proxy).
func (m *editManager) originOf(profile string) string {
	if m.origin == nil {
		return ""
	}
	return m.origin(profile)
}

// moved: the session's server is no longer at the address it began with (the
// profile rewritten or gone): nothing of it is sent any more.
func (m *editManager) moved(e *editSession) bool {
	return m.origin != nil && m.originOf(e.Profile) != e.origin
}

// cleanRemoteMod: a date as the agent's listing gives it (text, short, no
// control character), else "".
func cleanRemoteMod(s string) string {
	if len(s) > editRemoteModMax {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

func newEditManager(l *localFS, root string) *editManager {
	return &editManager{l: l, root: root, sessions: map[string]*editSession{}, push: func(editEvent) {}, stat: os.Stat, now: time.Now,
		launcher: launchEditor}
}

var editIDPart = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

type editBeginOpts struct {
	Profile    string `json:"profile"`
	DeviceID   string `json:"deviceId"`
	RemotePath string `json:"remotePath"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Modified   string `json:"modified"`
}

type editBeginResult struct {
	EditID    string `json:"editId"`
	Handle    string `json:"handle"`
	LocalName string `json:"localName"`
}

func (m *editManager) byID(id string) *editSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// begin: a new working copy, its download's write handle (writeEnd then sets
// its baseline). The same remote file already edited: exists, with its id.
func (m *editManager) begin(o editBeginOpts) (*editBeginResult, error) {
	if m.root == "" {
		return nil, fsE("denied")
	}
	if !editIDPart.MatchString(o.Profile) || o.DeviceID == "" || !editIDPart.MatchString(o.DeviceID) {
		return nil, errFsBadRequest
	}
	if o.RemotePath == "" || len(o.RemotePath) > 4096 || strings.ContainsAny(o.RemotePath, "\x00\r\n") || o.Name == "" {
		return nil, errFsBadRequest
	}
	if o.Size < 0 {
		return nil, errFsBadRequest
	}
	if o.Size > localMaxFileBytes {
		return nil, fsE("too_large")
	}
	origin := m.originOf(o.Profile)
	if m.origin != nil && origin == "" {
		return nil, fsE("server_changed") // (a server this app does not know, or no longer)
	}
	m.mu.Lock()
	for _, e := range m.sessions {
		if e.Profile == o.Profile && e.DeviceID == o.DeviceID && e.RemotePath == o.RemotePath {
			m.mu.Unlock()
			return nil, fsEx("exists", map[string]interface{}{"editId": e.ID})
		}
	}
	if len(m.sessions) >= editMaxSessions {
		m.mu.Unlock()
		return nil, fsE("limit")
	}
	id := randHex(8)
	prof := o.Profile
	if prof == "" {
		prof = "local"
	}
	e := &editSession{ID: id, Profile: o.Profile, DeviceID: o.DeviceID, RemotePath: o.RemotePath, Name: o.Name,
		dir: filepath.Join(m.root, prof+"_"+o.DeviceID, id), created: m.now(), snaps: map[int64]editSnap{}, writing: true,
		origin: origin, remote: editRemote{Size: o.Size, Modified: cleanRemoteMod(o.Modified)}}
	e.file = filepath.Join(e.dir, editFileDir, sanitizeLocalName(o.Name))
	m.sessions[id] = e
	m.mu.Unlock()
	fail := func(err error) (*editBeginResult, error) {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		removeTreeUnder(m.root, e.dir)
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(e.file), 0o700); err != nil {
		return fail(err)
	}
	marker, _ := json.MarshalIndent(map[string]interface{}{"profile": o.Profile, "deviceId": o.DeviceID, "remotePath": o.RemotePath,
		"name": o.Name, "created": isoTime(e.created)}, "", "  ")
	_ = os.WriteFile(filepath.Join(e.dir, editMarker), marker, 0o600)
	m.l.sys.setHidden(filepath.Join(e.dir, editMarker))
	h, err := m.l.beginTmp(filepath.Dir(e.file), filepath.Base(e.file), e.file, fsWriteOpts{Size: o.Size, Modified: o.Modified, Conflict: "overwrite"}, e)
	if err != nil {
		return fail(err)
	}
	return &editBeginResult{EditID: id, Handle: h.Handle, LocalName: filepath.Base(e.file)}, nil
}

type editHandleResult struct {
	Handle string `json:"handle"`
}

// reload: the device's copy again over the working copy (the operator chose
// it after a change on the device).
func (m *editManager) reload(id string, o fsWriteOpts) (*editHandleResult, error) {
	m.mu.Lock()
	e := m.sessions[id]
	if e == nil || e.stopped {
		m.mu.Unlock()
		return nil, fsE("stale_handle")
	}
	if e.writing {
		m.mu.Unlock()
		return nil, errFsBusy
	}
	if o.Size < 0 || o.Size > localMaxFileBytes {
		m.mu.Unlock()
		return nil, fsE("too_large")
	}
	m.mu.Unlock()
	if m.moved(e) {
		return nil, fsE("server_changed")
	}
	m.mu.Lock()
	if e.writing || e.stopped {
		m.mu.Unlock()
		return nil, errFsBusy
	}
	e.writing = true
	m.mu.Unlock()
	h, err := m.l.beginTmp(filepath.Dir(e.file), filepath.Base(e.file), e.file, fsWriteOpts{Size: o.Size, Modified: o.Modified, Conflict: "overwrite"}, e)
	if err != nil {
		m.writeFailed(e)
		return nil, err
	}
	return &editHandleResult{Handle: h.Handle}, nil
}

// written: the device's content is in the working copy (a download, a
// reload): the new baseline, nothing to send, the snapshots gone.
func (m *editManager) written(e *editSession, sum [32]byte) {
	st, _ := m.stat(e.file)
	m.mu.Lock()
	defer m.mu.Unlock()
	e.baseline, e.reported = sum, sum
	e.writing, e.dirty, e.stable, e.missing, e.tooLarge = false, false, 0, false, false
	e.missingSince = time.Time{}
	if st != nil {
		e.obsSize, e.obsMod = st.Size(), st.ModTime()
	}
	for s, sn := range e.snaps {
		os.Remove(sn.path)
		delete(e.snaps, s)
	}
}

func (m *editManager) writeFailed(e *editSession) {
	m.mu.Lock()
	e.writing = false
	m.mu.Unlock()
}

type editLaunchResult struct {
	Editor     string `json:"editor"`
	EditorName string `json:"editorName,omitempty"`
	Fallback   string `json:"fallback,omitempty"`
}

func (m *editManager) launch(id string) (*editLaunchResult, error) {
	m.mu.Lock()
	e := m.sessions[id]
	ok := e != nil && !e.stopped && !e.writing
	m.mu.Unlock()
	if !ok {
		return nil, fsE("stale_handle")
	}
	if _, err := os.Stat(e.file); err != nil {
		return nil, fsE("not_found")
	}
	r, err := m.launcher(e, m.l.settings().normalized())
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	e.launched = true
	m.mu.Unlock()
	return r, nil
}

type editSnapResult struct {
	Handle string `json:"handle"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

// snapshot: a read handle on the frozen content of seq (the latest only:
// stale for an older one, a newer one is on its way). Its server no longer
// at the address it began with: server_changed (never sent elsewhere).
func (m *editManager) snapshot(id string, seq int64) (*editSnapResult, error) {
	if e := m.byID(id); e != nil && m.moved(e) {
		return nil, fsE("server_changed")
	}
	m.mu.Lock()
	e := m.sessions[id]
	var sn editSnap
	ok := e != nil && !e.stopped
	if ok {
		sn, ok = e.snaps[seq]
		ok = ok && seq == e.seq
	}
	m.mu.Unlock()
	if !ok {
		return nil, fsE("stale")
	}
	h, err := m.l.openReadAt(sn.path)
	if err != nil {
		return nil, err
	}
	return &editSnapResult{Handle: h.Handle, Size: sn.size, Sha256: hex.EncodeToString(sn.sum[:])}, nil
}

// synced: the device has seq now (its new baseline); older snapshots go.
func (m *editManager) synced(id string, seq int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.sessions[id]
	if e == nil {
		return false
	}
	sn, ok := e.snaps[seq]
	if !ok {
		return false
	}
	e.baseline = sn.sum
	if seq > e.syncedSeq {
		e.syncedSeq = seq
	}
	e.lastSyncAt = m.now()
	for s, x := range e.snaps {
		if s <= seq {
			os.Remove(x.path) // (still open for reading: removed at the end)
			delete(e.snaps, s)
		}
	}
	return true
}

// setRemote: the device's file as the page last listed it (after a download,
// an upload): kept for a page that reloads.
func (m *editManager) setRemote(id string, r editRemote) bool {
	if r.Size < 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.sessions[id]
	if e == nil || e.stopped {
		return false
	}
	e.remote = editRemote{Size: r.Size, Modified: cleanRemoteMod(r.Modified)}
	return true
}

type editStopOpts struct {
	Force bool `json:"force"` // changes the device does not have are dropped
	Keep  bool `json:"keep"`  // they are kept on disk (the sweep takes them after a day)
}

type editStopResult struct {
	Stopped bool `json:"stopped"`
	Kept    bool `json:"kept,omitempty"`
}

func fileSum(p string) ([32]byte, bool, error) {
	var sum [32]byte
	f, err := os.Open(p)
	if err != nil {
		return sum, false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, localMaxFileBytes+1)); err != nil {
		return sum, false, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, true, nil
}

// unsyncedLocked: the working copy holds what the device does not have.
func (m *editManager) unsynced(e *editSession) bool {
	sum, ok, err := fileSum(e.file)
	if err != nil || !ok {
		return false // (gone: nothing to lose)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return sum != e.baseline
}

// stop: the working copy ends (deleted). It holds changes the device does
// not have: unsynced, unless forced (dropped) or kept (left on disk).
func (m *editManager) stop(id string, o editStopOpts) (*editStopResult, error) {
	m.mu.Lock()
	e := m.sessions[id]
	if e == nil {
		m.mu.Unlock()
		return &editStopResult{Stopped: true}, nil
	}
	e.stopped = true
	m.mu.Unlock()
	if !o.Force && m.unsynced(e) {
		if !o.Keep {
			m.mu.Lock()
			e.stopped = false
			m.mu.Unlock()
			return nil, fsE("unsynced")
		}
		m.forget(e, false)
		return &editStopResult{Stopped: true, Kept: true}, nil
	}
	m.forget(e, true)
	return &editStopResult{Stopped: true}, nil
}

// forget: out of the list, its handles closed, its folder deleted (del).
func (m *editManager) forget(e *editSession, del bool) {
	m.mu.Lock()
	delete(m.sessions, e.ID)
	m.mu.Unlock()
	m.l.closeReadsUnder(e.dir)
	m.l.abortWritesOf(e)
	if del {
		removeTreeUnder(m.root, e.dir)
		_ = os.Remove(filepath.Dir(e.dir)) // (its device's folder, when empty)
	}
}

type editInfo struct {
	EditID     string     `json:"editId"`
	Profile    string     `json:"profile"`
	DeviceID   string     `json:"deviceId"`
	RemotePath string     `json:"remotePath"`
	Name       string     `json:"name"`
	State      string     `json:"state"` // writing | watching | missing | tooLarge
	Dirty      bool       `json:"dirty"`
	Seq        int64      `json:"seq"`
	LastSyncAt string     `json:"lastSyncAt,omitempty"`
	Remote     editRemote `json:"remote"` // the device's file when last read (the page's baseline)
}

// list: the working copies (the page reloaded: it takes them back). Those
// whose server is no longer at the address they began with stop instead
// (their unsent changes kept on disk, the sweep of a later start takes them).
func (m *editManager) list() []editInfo {
	m.mu.Lock()
	all := make([]*editSession, 0, len(m.sessions))
	for _, e := range m.sessions {
		all = append(all, e)
	}
	m.mu.Unlock()
	gone := map[*editSession]bool{}
	for _, e := range all {
		if m.moved(e) {
			gone[e] = true
		}
	}
	m.mu.Lock()
	var stop []*editSession
	for e := range gone {
		if !e.stopped {
			e.stopped = true
			stop = append(stop, e)
		}
	}
	out := []editInfo{}
	for _, e := range m.sessions {
		if e.stopped {
			continue
		}
		st := "watching"
		switch {
		case e.writing:
			st = "writing"
		case e.missing:
			st = "missing"
		case e.tooLarge:
			st = "tooLarge"
		}
		out = append(out, editInfo{EditID: e.ID, Profile: e.Profile, DeviceID: e.DeviceID, RemotePath: e.RemotePath, Name: e.Name,
			State: st, Dirty: e.reported != e.baseline, Seq: e.seq, LastSyncAt: isoTime(e.lastSyncAt), Remote: e.remote})
	}
	m.mu.Unlock()
	if len(stop) > 0 {
		go m.stopMoved(stop) // (hashing a copy: never on the UI thread)
	}
	return out
}

// stopMoved: sessions whose server moved end; a copy holding changes the
// device does not have stays on disk (the sweep of a later start takes it).
func (m *editManager) stopMoved(list []*editSession) {
	for _, e := range list {
		m.forget(e, !m.unsynced(e))
	}
}

// ── The watcher ────────────────────────────────────────────────────────────

func (m *editManager) watch(done <-chan struct{}) {
	t := time.NewTicker(editWatchEvery)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		m.tick()
		if n++; n%90 == 0 { // (about a minute)
			m.l.prune()
		}
	}
}

func (m *editManager) tick() {
	m.mu.Lock()
	list := make([]*editSession, 0, len(m.sessions))
	for _, e := range m.sessions {
		list = append(list, e)
	}
	m.mu.Unlock()
	for _, e := range list {
		for _, ev := range m.check(e) {
			m.push(ev)
		}
	}
}

// check: one look at a working copy; the events it gives.
func (m *editManager) check(e *editSession) []editEvent {
	m.mu.Lock()
	if e.writing || e.stopped {
		m.mu.Unlock()
		return nil
	}
	file := e.file
	m.mu.Unlock()
	st, err := m.stat(file)
	now := m.now()
	var evs []editEvent
	m.mu.Lock()
	if err != nil || st.IsDir() {
		if !e.missing {
			if e.missingSince.IsZero() {
				e.missingSince = now
			} else if now.Sub(e.missingSince) >= editMissingAfter {
				e.missing = true
				evs = append(evs, editEvent{EditID: e.ID, Type: "missing"})
			}
		}
		e.dirty, e.stable = false, 0
		m.mu.Unlock()
		return evs
	}
	e.missingSince = time.Time{}
	if e.missing {
		e.missing = false
		evs = append(evs, editEvent{EditID: e.ID, Type: "back"})
		e.dirty, e.stable = true, 0
		e.obsSize, e.obsMod = st.Size(), st.ModTime()
		m.mu.Unlock()
		return evs
	}
	if st.Size() != e.obsSize || !st.ModTime().Equal(e.obsMod) {
		e.obsSize, e.obsMod = st.Size(), st.ModTime()
		e.dirty, e.stable = true, 0
		m.mu.Unlock()
		return nil
	}
	if !e.dirty {
		m.mu.Unlock()
		return nil
	}
	if e.stable++; e.stable < editStableLooks {
		m.mu.Unlock()
		return nil
	}
	if st.Size() > localMaxFileBytes {
		e.dirty = false
		if !e.tooLarge {
			e.tooLarge = true
			evs = append(evs, editEvent{EditID: e.ID, Type: "tooLarge", Size: st.Size()})
		}
		m.mu.Unlock()
		return evs
	}
	e.tooLarge = false
	seq := e.seq + 1
	dir := e.dir
	m.mu.Unlock()

	snap, err := m.freeze(dir, file, seq, st)
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.stopped || e.writing {
		if err == nil {
			os.Remove(snap.path)
		}
		return nil
	}
	if err != nil {
		if errors.Is(err, errEditChanged) {
			e.stable = 0 // (changing still: looked at again)
		}
		return nil // (in use: tried again at the next look)
	}
	e.dirty = false
	if snap.sum == e.baseline || snap.sum == e.reported {
		os.Remove(snap.path) // (saved again as it was: nothing to send)
		return nil
	}
	e.seq = seq
	e.reported = snap.sum
	e.snaps[seq] = snap
	// Only the two latest are kept (the previous one may be on its way).
	for s, x := range e.snaps {
		if s < seq-1 && s > e.syncedSeq {
			os.Remove(x.path)
			delete(e.snaps, s)
		}
	}
	return []editEvent{{EditID: e.ID, Type: "changed", Seq: seq, Size: snap.size, Sha256: hex.EncodeToString(snap.sum[:])}}
}

var errEditChanged = errors.New("changed while read")

// freeze: the file copied into a snapshot while hashed; the file must not
// have changed meanwhile (its size and date as looked at before).
func (m *editManager) freeze(dir, file string, seq int64, st os.FileInfo) (editSnap, error) {
	var sn editSnap
	src, err := os.Open(file)
	if err != nil {
		return sn, err // (the editor holds it: another look)
	}
	defer src.Close()
	sd := filepath.Join(dir, editSnapDir)
	if err := os.MkdirAll(sd, 0o700); err != nil {
		return sn, err
	}
	m.l.sys.setHidden(sd)
	sn.path = filepath.Join(sd, "snap-"+itoa64(seq)+"-"+randHex(3))
	dst, err := os.OpenFile(sn.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return sn, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, localMaxFileBytes+1))
	cerr := dst.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(sn.path)
		return sn, err
	}
	after, err := m.stat(file)
	if err != nil || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) || n != st.Size() {
		os.Remove(sn.path)
		return sn, errEditChanged
	}
	sn.size = n
	copy(sn.sum[:], h.Sum(nil))
	return sn, nil
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// shutdown (a clean exit): working copies the device has go; the others stay
// (the sweep of a later start takes them after a day).
func (m *editManager) shutdown() {
	m.mu.Lock()
	list := make([]*editSession, 0, len(m.sessions))
	for _, e := range m.sessions {
		list = append(list, e)
	}
	m.mu.Unlock()
	for _, e := range list {
		m.mu.Lock()
		e.stopped = true
		writing := e.writing
		m.mu.Unlock()
		if writing || m.unsynced(e) {
			m.forget(e, writing) // (a download cut short: nothing of value)
			continue
		}
		m.forget(e, true)
	}
}

// abortWritesOf: the write handles of a working copy (stopped).
func (l *localFS) abortWritesOf(e *editSession) {
	l.mu.Lock()
	var gone []*writeHandle
	for k, h := range l.writes {
		if h.edit == e {
			gone = append(gone, h)
			delete(l.writes, k)
		}
	}
	l.mu.Unlock()
	for _, h := range gone {
		h.mu.Lock()
		h.done = true
		h.mu.Unlock()
		h.discard()
	}
}

// ── Cleaning up ────────────────────────────────────────────────────────────

// removeTreeUnder deletes dir, strictly inside root, without ever following
// a link or a junction (the link itself goes, never what it names).
func removeTreeUnder(root, dir string) {
	if root == "" || dir == "" || !pathUnder(dir, root) || strings.EqualFold(filepath.Clean(dir), filepath.Clean(root)) {
		return
	}
	removeTree(dir, 0)
}

func removeTree(p string, depth int) {
	st, err := os.Lstat(p)
	if err != nil {
		return
	}
	if st.Mode()&os.ModeSymlink != 0 || st.Mode()&os.ModeIrregular != 0 || !st.IsDir() || depth > 64 {
		if err := os.Remove(p); err != nil {
			_ = os.Chmod(p, 0o600) // (read-only)
			_ = os.Remove(p)
		}
		return
	}
	ents, _ := os.ReadDir(p)
	for _, e := range ents {
		removeTree(filepath.Join(p, e.Name()), depth+1)
	}
	_ = os.Remove(p)
}

// newestUnder: the latest date of a folder and what it holds (two levels:
// the session's bookkeeping, the working copy in its own folder), never
// through a link.
func newestUnder(dir string) time.Time {
	return newestIn(dir, 2)
}

func newestIn(dir string, depth int) time.Time {
	st, err := os.Lstat(dir)
	if err != nil {
		return time.Time{}
	}
	t := st.ModTime()
	if depth <= 0 {
		return t
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			continue // (a link's own date says nothing of the copy)
		}
		var et time.Time
		if e.IsDir() {
			et = newestIn(filepath.Join(dir, e.Name()), depth-1)
		} else if fi, err := e.Info(); err == nil {
			et = fi.ModTime()
		}
		if et.After(t) {
			t = et
		}
	}
	return t
}

// sweepEditRoot: working copies left by earlier runs, older than age, go
// (start-up). Only folders under root, never through a link.
func sweepEditRoot(root string, now time.Time, age time.Duration) {
	devs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range devs {
		dp := filepath.Join(root, d.Name())
		if !d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				removeTreeUnder(root, dp)
			}
			continue
		}
		sess, _ := os.ReadDir(dp)
		for _, s := range sess {
			sp := filepath.Join(dp, s.Name())
			if now.Sub(newestUnder(sp)) > age {
				removeTreeUnder(root, sp)
			}
		}
		_ = os.Remove(dp) // (when empty)
	}
}

// ── config.json (Proxy.mu guards cfg) ──────────────────────────────────────

func (p *Proxy) filesSettings() FilesSettings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.Files.normalized()
}

func (p *Proxy) setFilesSettings(s FilesSettings) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Files = s
	saveConfig(p.cfg)
}

// profileOrigin: the address (scheme://host, lowercased) of a server profile
// now; "" names the active server (single-server mode before any profile);
// an unknown profile: "". What a working copy is tied to.
func (p *Proxy) profileOrigin(id string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cfg == nil {
		return ""
	}
	u := p.cfg.ServerURL
	if id != "" {
		pr := p.cfg.profileByID(id)
		if pr == nil {
			return ""
		}
		u = pr.ServerURL
	}
	o, ok := serverOrigin(u)
	if !ok {
		return ""
	}
	return strings.ToLower(o)
}
