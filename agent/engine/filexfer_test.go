package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fxProbe struct {
	mu     sync.Mutex
	msgs   []map[string]interface{}
	frames [][]byte
	got    chan struct{}
}

func (p *fxProbe) send(m map[string]interface{}) {
	b, _ := json.Marshal(m) // normalise like the wire (numbers → float64)
	var back map[string]interface{}
	_ = json.Unmarshal(b, &back)
	p.mu.Lock()
	p.msgs = append(p.msgs, back)
	p.mu.Unlock()
	select {
	case p.got <- struct{}{}:
	default:
	}
}

func (p *fxProbe) frame(f []byte) error {
	p.mu.Lock()
	p.frames = append(p.frames, append([]byte(nil), f...))
	p.mu.Unlock()
	select {
	case p.got <- struct{}{}:
	default:
	}
	return nil
}

func (p *fxProbe) last(typ string) map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.msgs) - 1; i >= 0; i-- {
		if p.msgs[i]["type"] == typ {
			return p.msgs[i]
		}
	}
	return nil
}

func (p *fxProbe) count(typ string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, m := range p.msgs {
		if m["type"] == typ {
			n++
		}
	}
	return n
}

func (p *fxProbe) nframes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.frames)
}

func (p *fxProbe) waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		select {
		case <-p.got:
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func newTestXfer(t *testing.T, dir string) (*fileXfer, *fxProbe, *[]string) {
	p := &fxProbe{got: make(chan struct{}, 1)}
	var received []string
	x := newFileXfer(fileXferEnv{
		sendJSON:     p.send,
		sendFrame:    p.frame,
		downloadsDir: func() (string, error) { return dir, nil },
		createFile: func(path string) (*os.File, error) {
			return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		},
		openFile: os.Open,
		rename: func(src, dst string) error {
			if _, err := os.Lstat(dst); err == nil {
				return &os.LinkError{Op: "rename", Old: src, New: dst, Err: os.ErrExist}
			}
			return os.Rename(src, dst)
		},
		received:     func(name, _ string) { received = append(received, name) },
		stallTimeout: 300 * time.Millisecond,
	})
	return x, p, &received
}

func ctl(kind string, fields map[string]interface{}) (string, []byte) {
	fields["type"] = kind
	b, _ := json.Marshal(fields)
	return kind, b
}

func uploadAll(t *testing.T, x *fileXfer, p *fxProbe, tid uint32, name string, data []byte, badHash bool) {
	t.Helper()
	uploadAllPaste(t, x, p, tid, name, data, badHash, false)
}

func uploadAllPaste(t *testing.T, x *fileXfer, p *fxProbe, tid uint32, name string, data []byte, badHash, paste bool) {
	t.Helper()
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": tid, "name": name, "size": len(data), "dest": "downloads", "paste": paste}))
	acc := p.last("file_accept")
	if acc == nil {
		t.Fatalf("no file_accept (last error %v)", p.last("file_error"))
	}
	chunk := int(acc["chunk"].(float64))
	var seq uint32
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		x.onData(buildFileFrame(FrameTypeFileUp, tid, seq, data[off:end]))
		seq++
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	if badHash {
		h = strings.Repeat("0", 64)
	}
	x.onControl(ctl("file_end", map[string]interface{}{"tid": tid, "sha256": h}))
}

func noPartFiles(t *testing.T, dir string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*"+fxPartSuffix))
	if len(m) != 0 {
		t.Fatalf("partial files left: %v", m)
	}
}

func TestFileUploadHappyPathAndUniqueName(t *testing.T) {
	dir := t.TempDir()
	x, p, received := newTestXfer(t, dir)
	data := bytes.Repeat([]byte("0123456789abcdef"), (fileChunkSize*2+100)/16+1) // 3 chunks
	uploadAll(t, x, p, 7, "rapport.pdf", data, false)
	done := p.last("file_done")
	if done == nil || done["ok"] != true {
		t.Fatalf("file_done = %v", done)
	}
	want := filepath.Join(dir, "rapport.pdf")
	if done["path"] != want {
		t.Fatalf("path %v, want %v", done["path"], want)
	}
	if got, _ := os.ReadFile(want); !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if p.count("file_progress") != 3 {
		t.Fatalf("progress acks %d, want 3 (one per chunk)", p.count("file_progress"))
	}
	if len(*received) != 1 || (*received)[0] != "rapport.pdf" {
		t.Fatalf("toast %v", *received)
	}
	// Same name again: never overwritten.
	uploadAll(t, x, p, 8, "rapport.pdf", []byte("second"), false)
	if d := p.last("file_done"); d["path"] != filepath.Join(dir, "rapport (1).pdf") || d["ok"] != true {
		t.Fatalf("second upload %v", d)
	}
	if got, _ := os.ReadFile(want); !bytes.Equal(got, data) {
		t.Fatal("the first file was overwritten")
	}
	noPartFiles(t, dir)
}

// TestFileUploadPasteNoSizeCap: a paste-as-RDP upload (file_begin.paste) is
// never rejected for its size alone, unlike a normal upload (by design:
// only the 20 GiB-per-paste total matters, enforced by the viewer before it
// starts transferring — the agent never buffers a whole file in memory
// either way, it streams chunk by chunk).
func TestFileUploadPasteNoSizeCap(t *testing.T) {
	dir := t.TempDir()
	x, p, _ := newTestXfer(t, dir)
	x.onControl(ctl("file_begin", map[string]interface{}{
		"tid": 1, "name": "gros.iso", "size": int64(fileMaxBytes) + 1, "paste": true,
	}))
	if e := p.last("file_error"); e != nil {
		t.Fatalf("a paste upload must not be size-capped: %v", e)
	}
	if acc := p.last("file_accept"); acc == nil {
		t.Fatalf("no file_accept for the oversized paste upload")
	}
	x.onControl(ctl("file_cancel", map[string]interface{}{"tid": 1}))
	// A non-paste upload of the same size is still refused (regression).
	x.onControl(ctl("file_begin", map[string]interface{}{
		"tid": 2, "name": "gros2.iso", "size": int64(fileMaxBytes) + 1,
	}))
	if e := p.last("file_error"); e == nil || e["tid"] != float64(2) || e["code"] != fxErrTooLarge {
		t.Fatalf("a non-paste upload must keep the size cap: %v", e)
	}
}

// TestFileUploadPasteMarksMOTW: env.markDownloaded is called with the final
// path only for a paste upload that completed successfully, never for a
// normal upload (Mark-of-the-Web only applies to the "as RDP" clipboard paste
// by design).
func TestFileUploadPasteMarksMOTW(t *testing.T) {
	dir := t.TempDir()
	x, p, _ := newTestXfer(t, dir)
	var marked []string
	x.env.markDownloaded = func(path string) { marked = append(marked, path) }

	uploadAllPaste(t, x, p, 1, "photo.jpg", []byte("hello"), false, true)
	if d := p.last("file_done"); d == nil || d["ok"] != true {
		t.Fatalf("paste upload: %v", d)
	}
	want := filepath.Join(dir, "photo.jpg")
	if len(marked) != 1 || marked[0] != want {
		t.Fatalf("markDownloaded = %v, want [%s]", marked, want)
	}

	uploadAllPaste(t, x, p, 2, "b.bin", []byte("hello"), false, false)
	if len(marked) != 1 {
		t.Fatalf("a non-paste upload must not be marked: %v", marked)
	}

	// A failed paste upload (bad hash) is never marked.
	uploadAllPaste(t, x, p, 3, "c.bin", []byte("hello"), true, true)
	if len(marked) != 1 {
		t.Fatalf("a failed paste upload must not be marked: %v", marked)
	}
}

func TestFileUploadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	x, p, _ := newTestXfer(t, dir)
	uploadAll(t, x, p, 1, "vide.txt", nil, false)
	if d := p.last("file_done"); d == nil || d["ok"] != true {
		t.Fatalf("empty file: %v", d)
	}
	if fi, err := os.Stat(filepath.Join(dir, "vide.txt")); err != nil || fi.Size() != 0 {
		t.Fatal("empty file not created")
	}
}

func TestFileUploadRefusals(t *testing.T) {
	dir := t.TempDir()
	x, p, _ := newTestXfer(t, dir)

	// Hash mismatch: nothing kept.
	uploadAll(t, x, p, 1, "a.bin", []byte("hello"), true)
	if d := p.last("file_done"); d["ok"] != false || d["code"] != fxErrHash {
		t.Fatalf("hash mismatch: %v", d)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.bin")); err == nil {
		t.Fatal("a corrupted file must not be kept")
	}

	// Busy.
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 2, "name": "b.bin", "size": 10}))
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 3, "name": "c.bin", "size": 10}))
	if e := p.last("file_error"); e["tid"] != float64(3) || e["code"] != fxErrBusy {
		t.Fatalf("busy: %v", e)
	}
	// Out of order → error, partial removed.
	x.onData(buildFileFrame(FrameTypeFileUp, 2, 1, []byte("x")))
	if e := p.last("file_error"); e["tid"] != float64(2) {
		t.Fatalf("out of order: %v", e)
	}
	noPartFiles(t, dir)

	// More data than announced.
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 4, "name": "d.bin", "size": 2}))
	x.onData(buildFileFrame(FrameTypeFileUp, 4, 0, []byte("xyz")))
	if e := p.last("file_error"); e["tid"] != float64(4) || e["code"] != fxErrTooLarge {
		t.Fatalf("overrun: %v", e)
	}
	// Announced too large.
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 5, "name": "e.bin", "size": int64(fileMaxBytes) + 1}))
	if e := p.last("file_error"); e["tid"] != float64(5) || e["code"] != fxErrTooLarge {
		t.Fatalf("too large: %v", e)
	}
	// Unusable name.
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 6, "name": " .. ", "size": 1}))
	if e := p.last("file_error"); e["tid"] != float64(6) || e["code"] != fxErrBadName {
		t.Fatalf("bad name: %v", e)
	}
	// Cancel mid-way, then abort of another one: no partial file.
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 9, "name": "f.bin", "size": 10}))
	x.onData(buildFileFrame(FrameTypeFileUp, 9, 0, []byte("12345")))
	x.onControl(ctl("file_cancel", map[string]interface{}{"tid": 9}))
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 10, "name": "g.bin", "size": 10}))
	x.abort()
	noPartFiles(t, dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("nothing may be left in Downloads, got %d entries", len(entries))
	}
	// No user signed in.
	x.env.downloadsDir = func() (string, error) { return "", fxErr(fxErrNoUser, errors.New("no token")) }
	x.onControl(ctl("file_begin", map[string]interface{}{"tid": 11, "name": "h.bin", "size": 1}))
	if e := p.last("file_error"); e["tid"] != float64(11) || e["code"] != fxErrNoUser {
		t.Fatalf("no user: %v", e)
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"rapport.pdf":            "rapport.pdf",
		`C:\Users\x\evil.exe`:    "evil.exe",
		"../../Windows/win.ini":  "win.ini",
		"a:stream.txt":           "a_stream.txt",
		"CON":                    "_CON",
		"con.txt":                "_con.txt",
		"LPT1.log":               "_LPT1.log",
		"name. . ":               "name",
		" lead.txt":              "lead.txt",
		"tab\there?.txt":         "tab_here_.txt",
		"..":                     "",
		"   ":                    "",
		"x.oblireach-part":       "x.oblireach-part_",
		"été 2026 — résumé.docx": "été 2026 — résumé.docx",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 300) + ".txt"
	got := sanitizeFileName(long)
	if !strings.HasSuffix(got, ".txt") || len([]rune(got)) > 200 {
		t.Errorf("long name not capped with its extension: %d runes", len([]rune(got)))
	}
}

func TestFileDownloadWindowedAndHashed(t *testing.T) {
	dir := t.TempDir()
	x, p, _ := newTestXfer(t, dir)
	data := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7}, (fileChunkSize*(fileWindow+3))/7) // > window chunks
	src := filepath.Join(dir, "remote.bin")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	list := x.setClipboardFiles([]string{src, dir}, os.Stat)
	if len(list) != 2 || list[0].Size != int64(len(data)) || !list[1].Dir {
		t.Fatalf("clipboard list %+v", list)
	}
	// A folder cannot be downloaded.
	x.onControl(ctl("file_get", map[string]interface{}{"tid": 1, "source": "clipboard", "idx": 1}))
	if e := p.last("file_error"); e["tid"] != float64(1) || e["code"] != fxErrUnsupport {
		t.Fatalf("folder: %v", e)
	}
	x.onControl(ctl("file_get", map[string]interface{}{"tid": 2, "source": "clipboard", "idx": 0}))
	if o := p.last("file_offer"); o == nil || o["size"] != float64(len(data)) || o["name"] != "remote.bin" {
		t.Fatalf("offer %v", o)
	}
	// Exactly `window` frames go out, then the agent waits for acks.
	p.waitFor(t, func() bool { return p.nframes() >= fileWindow })
	time.Sleep(50 * time.Millisecond)
	if n := p.nframes(); n != fileWindow {
		t.Fatalf("%d frames in flight, window is %d", n, fileWindow)
	}
	// A second download while this one runs: busy.
	x.onControl(ctl("file_get", map[string]interface{}{"tid": 3, "source": "clipboard", "idx": 0}))
	if e := p.last("file_error"); e["tid"] != float64(3) || e["code"] != fxErrBusy {
		t.Fatalf("busy download: %v", e)
	}
	// Ack everything as it arrives (like the viewers: one ack per frame).
	acked := 0
	p.waitFor(t, func() bool {
		for acked < p.nframes() {
			x.onControl(ctl("file_progress", map[string]interface{}{"tid": 2, "seq": acked}))
			acked++
		}
		return p.last("file_done") != nil
	})
	done := p.last("file_done")
	var got []byte
	p.mu.Lock()
	for i, f := range p.frames {
		typ, tid, seq, d, ok := parseFileFrame(f)
		if !ok || typ != frameTypeFileDown || tid != 2 || seq != uint32(i) {
			p.mu.Unlock()
			t.Fatalf("frame %d: type %x tid %d seq %d", i, typ, tid, seq)
		}
		got = append(got, d...)
	}
	p.mu.Unlock()
	sum := sha256.Sum256(data)
	if !bytes.Equal(got, data) || done["ok"] != true || done["sha256"] != hex.EncodeToString(sum[:]) || done["dir"] != "down" {
		t.Fatalf("download result ok=%v bytes=%d/%d done=%v", done["ok"], len(got), len(data), done)
	}
}

func TestFileDownloadStallAndCancel(t *testing.T) {
	dir := t.TempDir()
	x, p, _ := newTestXfer(t, dir)
	src := filepath.Join(dir, "big.bin")
	_ = os.WriteFile(src, make([]byte, fileChunkSize*(fileWindow+2)), 0o644)
	x.setClipboardFiles([]string{src}, os.Stat)
	// Nobody acknowledges: dropped after the stall timeout, with an error.
	x.onControl(ctl("file_get", map[string]interface{}{"tid": 5, "source": "clipboard", "idx": 0}))
	p.waitFor(t, func() bool { e := p.last("file_error"); return e != nil && e["tid"] == float64(5) })
	if e := p.last("file_error"); e["code"] != fxErrTimeout {
		t.Fatalf("stall: %v", e)
	}
	// A new one can start; a cancel stops it without file_done.
	p.waitFor(t, func() bool { x.mu.Lock(); defer x.mu.Unlock(); return x.down == nil })
	x.onControl(ctl("file_get", map[string]interface{}{"tid": 6, "source": "clipboard", "idx": 0}))
	p.waitFor(t, func() bool { return p.last("file_offer") != nil && p.last("file_offer")["tid"] == float64(6) })
	x.onControl(ctl("file_cancel", map[string]interface{}{"tid": 6}))
	time.Sleep(100 * time.Millisecond)
	if d := p.last("file_done"); d != nil && d["tid"] == float64(6) {
		t.Fatal("a cancelled download must not report file_done")
	}
	// Stale index.
	x.onControl(ctl("file_get", map[string]interface{}{"tid": 7, "source": "clipboard", "idx": 4}))
	if e := p.last("file_error"); e["tid"] != float64(7) || e["code"] != fxErrNotFound {
		t.Fatalf("stale idx: %v", e)
	}
}
