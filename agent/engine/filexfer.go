package engine

// File transfers over the ObliReach stream — the AGENT side of the
// contract the viewers speak (web: the Obliance client's
// reachFileTransfer.ts; desktop client-app: ReachTransfers in reach_viewer.go;
// the Obliance relay audits both directions: remote.file_upload / download).
//
// Upload (operator → the session user's Downloads folder):
//   file_begin{tid,name,size,mime,dest} → file_accept{tid,chunk,window,path} | file_error{tid,code}
//   0x20 frames [0x20][tid u32 LE][seq u32 LE][data]; each written chunk is
//   acknowledged with file_progress{tid,seq} (the viewer keeps at most
//   `window` frames unacknowledged)
//   file_end{tid,sha256} → file_done{tid,ok,path,name,size,sha256} (ok:false + code on a mismatch)
// Download (a file the session user copied, listed by clipboard_files):
//   file_get{tid,source:"clipboard",idx} → file_offer{tid,name,size,chunk,window} | file_error
//   0x21 frames, acknowledged by the viewer with file_progress{tid,seq}
//   file_done{tid,dir:"down",ok,sha256,size}
// Either side may send file_cancel{tid}. One upload and one download at a time.
//
// The file is written to "<name>.oblireach-part" and renamed at the end (never
// over an existing file: "name (1).ext"…), so the user never opens half a
// file. The platform layer (filexfer_windows.go) opens files AS the session
// user (impersonation): an upload lands with the user's rights, and a
// download can only read what the user can read. This file is pure Go (no
// build tag) so the state machine is unit-tested (filexfer_test.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

// Error codes the viewers translate (web ReachTransferTray, client-app REACH_FILE_ERRORS).
const (
	fxErrTooLarge  = "too_large"
	fxErrNoUser    = "no_user_session"
	fxErrBadName   = "bad_name"
	fxErrDenied    = "denied"
	fxErrDiskFull  = "disk_full"
	fxErrHash      = "hash_mismatch"
	fxErrBusy      = "busy"
	fxErrNotFound  = "not_found"
	fxErrUnsupport = "unsupported"
	fxErrTimeout   = "timeout"
	fxErrFailed    = "failed"
)

// fxError carries a viewer error code with the underlying cause.
type fxError struct {
	code string
	err  error
}

func (e *fxError) Error() string {
	if e.err != nil {
		return e.code + ": " + e.err.Error()
	}
	return e.code
}

func fxErr(code string, err error) error { return &fxError{code: code, err: err} }

// fxCode maps an error to a viewer code (platform errors → denied / disk_full…).
func fxCode(err error, def string) string {
	var fe *fxError
	if errors.As(err, &fe) {
		return fe.code
	}
	if errors.Is(err, os.ErrPermission) {
		return fxErrDenied
	}
	if errors.Is(err, os.ErrNotExist) {
		return fxErrNotFound
	}
	if isDiskFull(err) {
		return fxErrDiskFull
	}
	return def
}

const fxPartSuffix = ".oblireach-part"

// fileXferEnv is the platform layer.
type fileXferEnv struct {
	// sendJSON sends a control message to the viewer (PipeTypeControl).
	sendJSON func(msg map[string]interface{})
	// sendFrame sends a 0x21 download frame to the viewer (PipeTypeFileDown).
	sendFrame func(frame []byte) error
	// downloadsDir is the session user's Downloads folder (fxErrNoUser when
	// nobody is signed in).
	downloadsDir func() (string, error)
	// createFile creates path exclusively, as the session user.
	createFile func(path string) (*os.File, error)
	// openFile opens path for reading, as the session user.
	openFile func(path string) (*os.File, error)
	// rename moves src to dst, never replacing an existing dst.
	rename func(src, dst string) error
	// received tells the session user a file arrived (toast). Optional.
	received func(name, dir string)
	// markDownloaded best-effort tags a paste-as-RDP upload with the "file
	// came from the Internet" mark (Mark-of-the-Web), same as a real RDP
	// clipboard paste. Optional; errors are never surfaced to the viewer —
	// Windows itself gives no guarantee this mark survives (accepted:
	// the mark is a best-effort hint).
	markDownloaded func(path string)
	// stallTimeout: a download whose viewer stopped acknowledging is dropped.
	stallTimeout time.Duration
}

type fxUpload struct {
	tid     uint32
	final   string // planned final path (file_accept.path)
	part    string
	f       *os.File
	h       hash.Hash
	size    int64
	written int64
	nextSeq uint32
	// paste: this upload came from the operator's local clipboard files
	// pasted "as RDP" (file_begin.paste) — no per-file size cap (by
	// design: the file is streamed, never buffered in memory, and the
	// viewer already enforces the 20 GiB-per-paste total before it starts),
	// and Mark-of-the-Web is applied on success. An explicit drag-drop
	// upload or the file-explorer transfer keeps the fileMaxBytes cap.
	paste bool
}

type fxDownload struct {
	tid    uint32
	acked  atomic.Int64 // highest seq acknowledged (-1: none)
	wake   chan struct{}
	cancel chan struct{}
	once   sync.Once
}

func (d *fxDownload) stop() { d.once.Do(func() { close(d.cancel) }) }

// clipFile is one entry of the last clipboard_files list sent to the viewer.
type clipFile struct {
	Idx  int    `json:"idx"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir,omitempty"`
	path string
}

type fileXfer struct {
	env   fileXferEnv
	mu    sync.Mutex
	up    *fxUpload
	down  *fxDownload
	files []clipFile
}

func newFileXfer(env fileXferEnv) *fileXfer {
	if env.stallTimeout <= 0 {
		env.stallTimeout = 60 * time.Second
	}
	return &fileXfer{env: env}
}

func (x *fileXfer) fail(tid uint32, code, message string) {
	m := map[string]interface{}{"type": "file_error", "tid": tid, "code": code}
	if message != "" {
		m["message"] = message
	}
	x.env.sendJSON(m)
}

// onControl handles one file_* control message from the viewer.
func (x *fileXfer) onControl(kind string, payload []byte) {
	var m struct {
		Tid    *uint32 `json:"tid"`
		Name   string  `json:"name"`
		Size   *int64  `json:"size"`
		Sha256 string  `json:"sha256"`
		Seq    *int64  `json:"seq"`
		Source string  `json:"source"`
		Idx    *int    `json:"idx"`
		Paste  bool    `json:"paste"`
	}
	if json.Unmarshal(payload, &m) != nil || m.Tid == nil {
		return
	}
	tid := *m.Tid
	switch kind {
	case "file_begin":
		size := int64(-1)
		if m.Size != nil {
			size = *m.Size
		}
		x.beginUpload(tid, m.Name, size, m.Paste)
	case "file_end":
		x.endUpload(tid, strings.ToLower(strings.TrimSpace(m.Sha256)))
	case "file_cancel":
		x.cancel(tid)
	case "file_get":
		idx := -1
		if m.Idx != nil {
			idx = *m.Idx
		}
		x.startDownload(tid, m.Source, idx)
	case "file_progress":
		if m.Seq != nil {
			x.downloadAck(tid, *m.Seq)
		}
	}
}

// ── Upload ──────────────────────────────────────────────────────────────────

func (x *fileXfer) beginUpload(tid uint32, rawName string, size int64, paste bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.up != nil {
		x.fail(tid, fxErrBusy, "")
		return
	}
	if size < 0 || (!paste && size > fileMaxBytes) {
		x.fail(tid, fxErrTooLarge, "")
		return
	}
	name := sanitizeFileName(rawName)
	if name == "" {
		x.fail(tid, fxErrBadName, "")
		return
	}
	dir, err := x.env.downloadsDir()
	if err != nil {
		x.fail(tid, fxCode(err, fxErrDenied), err.Error())
		return
	}
	var f *os.File
	var final, part string
	for try := 0; try < 5; try++ {
		final = uniquePath(dir, name)
		part = final + fxPartSuffix
		f, err = x.env.createFile(part)
		if err == nil || !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		x.fail(tid, fxCode(err, fxErrDenied), err.Error())
		return
	}
	x.up = &fxUpload{tid: tid, final: final, part: part, f: f, h: sha256.New(), size: size, paste: paste}
	x.env.sendJSON(map[string]interface{}{
		"type": "file_accept", "tid": tid, "chunk": fileChunkSize, "window": fileWindow, "path": final,
	})
}

// dropUploadLocked closes and deletes the partial file (x.mu held).
func (x *fileXfer) dropUploadLocked() {
	u := x.up
	if u == nil {
		return
	}
	x.up = nil
	_ = u.f.Close()
	_ = os.Remove(u.part)
}

// onData handles one 0x20 frame (upload data).
func (x *fileXfer) onData(frame []byte) {
	typ, tid, seq, data, ok := parseFileFrame(frame)
	if !ok || typ != FrameTypeFileUp {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	u := x.up
	if u == nil || u.tid != tid {
		return // cancelled / finished: late frames are ignored
	}
	if seq != u.nextSeq {
		x.dropUploadLocked()
		x.fail(tid, fxErrFailed, fmt.Sprintf("data out of order (got %d, want %d)", seq, u.nextSeq))
		return
	}
	if u.written+int64(len(data)) > u.size {
		x.dropUploadLocked()
		x.fail(tid, fxErrTooLarge, "more data than announced")
		return
	}
	if _, err := u.f.Write(data); err != nil {
		x.dropUploadLocked()
		x.fail(tid, fxCode(err, fxErrFailed), err.Error())
		return
	}
	u.h.Write(data)
	u.written += int64(len(data))
	u.nextSeq++
	x.env.sendJSON(map[string]interface{}{"type": "file_progress", "tid": tid, "seq": seq})
}

func (x *fileXfer) endUpload(tid uint32, wantHash string) {
	x.mu.Lock()
	u := x.up
	if u == nil || u.tid != tid {
		x.mu.Unlock()
		return
	}
	x.up = nil
	x.mu.Unlock()

	got := hex.EncodeToString(u.h.Sum(nil))
	done := func(ok bool, code, path string) {
		m := map[string]interface{}{"type": "file_done", "tid": tid, "ok": ok, "size": u.written, "sha256": got}
		if ok {
			m["path"] = path
			m["name"] = filepath.Base(path)
		} else {
			m["code"] = code
		}
		x.env.sendJSON(m)
	}
	if err := u.f.Close(); err != nil {
		_ = os.Remove(u.part)
		done(false, fxCode(err, fxErrFailed), "")
		return
	}
	if u.written != u.size || (wantHash != "" && wantHash != got) {
		_ = os.Remove(u.part)
		done(false, fxErrHash, "")
		return
	}
	// Rename to the final name, never over a file that appeared meanwhile.
	final := u.final
	var err error
	for try := 0; try < 5; try++ {
		if err = x.env.rename(u.part, final); err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			break
		}
		final = uniquePath(filepath.Dir(u.final), filepath.Base(u.final))
	}
	if err != nil {
		_ = os.Remove(u.part)
		done(false, fxCode(err, fxErrFailed), "")
		return
	}
	if u.paste && x.env.markDownloaded != nil {
		x.env.markDownloaded(final)
	}
	done(true, "", final)
	if x.env.received != nil {
		x.env.received(filepath.Base(final), filepath.Dir(final))
	}
}

// ── Download ────────────────────────────────────────────────────────────────

// setClipboardFiles records (and returns) the list shown to the viewer.
func (x *fileXfer) setClipboardFiles(paths []string, stat func(string) (os.FileInfo, error)) []clipFile {
	list := make([]clipFile, 0, len(paths))
	for _, p := range paths {
		if len(list) >= 100 {
			break
		}
		cf := clipFile{Idx: len(list), Name: filepath.Base(p), path: p}
		if fi, err := stat(p); err == nil {
			cf.Size = fi.Size()
			cf.Dir = fi.IsDir()
		}
		list = append(list, cf)
	}
	x.mu.Lock()
	x.files = list
	x.mu.Unlock()
	return list
}

func (x *fileXfer) startDownload(tid uint32, source string, idx int) {
	x.mu.Lock()
	if x.down != nil {
		x.mu.Unlock()
		x.fail(tid, fxErrBusy, "")
		return
	}
	if source != "clipboard" || idx < 0 || idx >= len(x.files) {
		x.mu.Unlock()
		x.fail(tid, fxErrNotFound, "")
		return
	}
	cf := x.files[idx]
	if cf.Dir {
		x.mu.Unlock()
		x.fail(tid, fxErrUnsupport, "folders cannot be downloaded")
		return
	}
	d := &fxDownload{tid: tid, wake: make(chan struct{}, 1), cancel: make(chan struct{})}
	d.acked.Store(-1)
	x.down = d
	x.mu.Unlock()

	f, err := x.env.openFile(cf.path)
	if err != nil {
		x.endDownload(d)
		x.fail(tid, fxCode(err, fxErrNotFound), err.Error())
		return
	}
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		_ = f.Close()
		x.endDownload(d)
		x.fail(tid, fxErrUnsupport, "")
		return
	}
	size := fi.Size()
	if size > fileMaxBytes {
		_ = f.Close()
		x.endDownload(d)
		x.fail(tid, fxErrTooLarge, "")
		return
	}
	x.env.sendJSON(map[string]interface{}{
		"type": "file_offer", "tid": tid, "name": filepath.Base(cf.path), "size": size,
		"chunk": fileChunkSize, "window": fileWindow,
	})
	go x.pumpDownload(d, f, size)
}

func (x *fileXfer) endDownload(d *fxDownload) {
	d.stop()
	x.mu.Lock()
	if x.down == d {
		x.down = nil
	}
	x.mu.Unlock()
}

func (x *fileXfer) downloadAck(tid uint32, seq int64) {
	x.mu.Lock()
	d := x.down
	x.mu.Unlock()
	if d == nil || d.tid != tid {
		return
	}
	for {
		cur := d.acked.Load()
		if seq <= cur || d.acked.CompareAndSwap(cur, seq) {
			break
		}
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// pumpDownload streams the file in 0x21 frames, `fileWindow` unacknowledged
// at most, then file_done with the SHA-256. Reads stop at the size announced
// in file_offer (a file growing meanwhile never overruns the viewer).
func (x *fileXfer) pumpDownload(d *fxDownload, f *os.File, size int64) {
	defer f.Close()
	defer x.endDownload(d)
	h := sha256.New()
	r := io.LimitReader(f, size)
	buf := make([]byte, fileChunkSize)
	var seq uint32
	var sent int64
	for {
		// Window: wait for acknowledgements.
		for int64(seq)-(d.acked.Load()+1) >= fileWindow {
			select {
			case <-d.cancel:
				return
			case <-d.wake:
			case <-time.After(x.env.stallTimeout):
				x.fail(d.tid, fxErrTimeout, "the viewer stopped acknowledging")
				return
			}
		}
		select {
		case <-d.cancel:
			return
		default:
		}
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			h.Write(buf[:n])
			if serr := x.env.sendFrame(buildFileFrame(frameTypeFileDown, d.tid, seq, buf[:n])); serr != nil {
				return // stream gone
			}
			seq++
			sent += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			x.env.sendJSON(map[string]interface{}{"type": "file_done", "tid": d.tid, "dir": "down", "ok": false, "code": fxCode(err, fxErrFailed)})
			return
		}
	}
	select {
	case <-d.cancel:
		return
	default:
	}
	m := map[string]interface{}{
		"type": "file_done", "tid": d.tid, "dir": "down", "ok": sent == size,
		"size": sent, "sha256": hex.EncodeToString(h.Sum(nil)),
	}
	if sent != size {
		m["code"] = fxErrFailed
		m["message"] = "the file changed on the remote while it was being sent"
	}
	x.env.sendJSON(m)
}

// ── Common ──────────────────────────────────────────────────────────────────

func (x *fileXfer) cancel(tid uint32) {
	x.mu.Lock()
	if x.up != nil && x.up.tid == tid {
		x.dropUploadLocked()
	}
	d := x.down
	x.mu.Unlock()
	if d != nil && d.tid == tid {
		x.endDownload(d)
	}
}

// abort drops everything in flight (the stream ends).
func (x *fileXfer) abort() {
	x.mu.Lock()
	x.dropUploadLocked()
	d := x.down
	x.mu.Unlock()
	if d != nil {
		x.endDownload(d)
	}
}

// ── Names ───────────────────────────────────────────────────────────────────

var reservedWinNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeFileName turns a viewer-supplied name into a safe single Windows
// file name: last path element only (no "..\" escape), no control / reserved
// characters (no ":" → no alternate data stream), no trailing dots / spaces,
// no device names, at most 200 UTF-16 units (extension kept). "" = unusable.
func sanitizeFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"|?*`, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	name = strings.TrimRight(strings.TrimLeft(b.String(), " "), " .")
	if name == "" || strings.Trim(name, ".") == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(name), fxPartSuffix) {
		name += "_"
	}
	stem := strings.ToUpper(name)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if reservedWinNames[strings.TrimRight(stem, " ")] {
		name = "_" + name
	}
	const maxUnits = 200
	if len(utf16.Encode([]rune(name))) > maxUnits {
		ext := filepath.Ext(name)
		if len(utf16.Encode([]rune(ext))) > 20 {
			ext = ""
		}
		rs := []rune(strings.TrimSuffix(name, ext))
		for len(rs) > 0 && len(utf16.Encode(rs))+len(utf16.Encode([]rune(ext))) > maxUnits {
			rs = rs[:len(rs)-1]
		}
		name = strings.TrimRight(string(rs), " .") + ext
	}
	return name
}

// uniquePath is dir\name, or dir\"stem (n).ext" when that name (or its part
// file) is already taken.
func uniquePath(dir, name string) string {
	taken := func(p string) bool {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
		_, err := os.Lstat(p + fxPartSuffix)
		return err == nil
	}
	p := filepath.Join(dir, name)
	if !taken(p) {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 1; n < 10000; n++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
		if !taken(p) {
			return p
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, time.Now().UnixNano(), ext))
}
