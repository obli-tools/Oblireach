package main

// sftp_save.go — a local SSH connection's file saved on this computer by the
// app itself, for an explorer without the local pane (a session window's,
// or the main window's outside the split view): streamed from its SFTP (or
// SCP: scp.go) session straight to the Downloads folder, never through the
// page (it went through a Blob in the page's memory: the whole file, twice),
// so any size goes, like the split view's transfers (transfers.go). The page
// polls its progress.
//
//   save_open   {path, name} -> {job, size, name}
//   save_status {job}        -> {state: running|done|error|cancelled,
//                                received, size, name, error}
//   save_cancel {job}        -> {}
//
// The file is written under a temporary name (".oblireach-part"), then
// renamed to a free name ("report (2).pdf": an existing file is never
// replaced), marked as coming from another computer like a browser's
// download (Windows: its Zone.Identifier). Stopped (cancelled, failed, its
// session closed): the partial file goes. A job ends with its session.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

const (
	saveMaxRunning = 8
	saveChunk      = 256 * 1024
)

var errSaveFolder = errors.New("the Downloads folder is not available")

// saveDownloadsDir: where the files go (Windows: the Downloads known folder,
// sftp_save_windows.go; a test: its own folder).
var saveDownloadsDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errSaveFolder
	}
	return filepath.Join(home, "Downloads"), nil
}

// saveMarkFromElsewhere: the saved file marked as downloaded (Windows: its
// Zone.Identifier; elsewhere nothing).
var saveMarkFromElsewhere = func(string) {}

type saveJob struct {
	id       string
	mu       sync.Mutex
	state    string // running | done | error | cancelled
	received int64
	size     int64
	name     string
	errText  string
	stop     context.CancelFunc
}

func (j *saveJob) cancel() { j.stop() }

func (j *saveJob) snapshot() map[string]interface{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[string]interface{}{"state": j.state, "received": j.received, "size": j.size, "name": j.name}
	if j.errText != "" {
		out["error"] = j.errText
	}
	return out
}

func (j *saveJob) finish(state, name, errText string) {
	j.mu.Lock()
	j.state, j.errText = state, errText
	if name != "" {
		j.name = name
	}
	j.mu.Unlock()
}

// saveProgress counts what is written, and stops once cancelled.
type saveProgress struct {
	ctx context.Context
	w   io.Writer
	j   *saveJob
}

func (p *saveProgress) Write(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	p.j.mu.Lock()
	p.j.received += int64(n)
	p.j.mu.Unlock()
	return n, err
}

// saveCmd runs one save_* command (handled false when typ is not one).
func saveCmd(fs explorerFS, st *sftpStreams, typ string, payload []byte) (interface{}, bool, error) {
	if !strings.HasPrefix(typ, "save_") {
		return nil, false, nil
	}
	var in struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Job  string `json:"job"`
	}
	if len(payload) > 0 && json.Unmarshal(payload, &in) != nil {
		return nil, true, errExplorerBadPath
	}
	switch typ {
	case "save_open":
		p, err := explorerCleanPath(in.Path, false)
		if err != nil || p == "/" {
			return nil, true, errExplorerBadPath
		}
		fi, err := fs.Lstat(p)
		if err != nil {
			return nil, true, err
		}
		if fi.IsDir() {
			return nil, true, errExplorerBadPath
		}
		name := in.Name
		if name == "" {
			name = path.Base(p)
		}
		j, err := saveStart(fs, st, p, sanitizeLocalName(name), fi.Size())
		if err != nil {
			return nil, true, err
		}
		return map[string]interface{}{"job": j.id, "size": j.size, "name": j.name}, true, nil
	case "save_status":
		j := st.saveGet(in.Job)
		if j == nil {
			return nil, true, errStreamHandle
		}
		snap := j.snapshot()
		if snap["state"] != "running" {
			st.saveDrop(in.Job) // (its end told once)
		}
		return snap, true, nil
	case "save_cancel":
		if j := st.saveGet(in.Job); j != nil {
			j.cancel()
		}
		return struct{}{}, true, nil
	}
	return nil, true, errExplorerBadCmd
}

func (s *sftpStreams) saveGet(id string) *saveJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves[id]
}

func (s *sftpStreams) saveDrop(id string) {
	s.mu.Lock()
	delete(s.saves, id)
	s.mu.Unlock()
}

// saveStart: the temporary file made, the remote one opened, the copy on its way.
func saveStart(fs explorerFS, st *sftpStreams, remote, name string, size int64) (*saveJob, error) {
	st.mu.Lock()
	running := 0
	for _, j := range st.saves {
		j.mu.Lock()
		if j.state == "running" {
			running++
		}
		j.mu.Unlock()
	}
	st.mu.Unlock()
	if running >= saveMaxRunning {
		return nil, errStreamFull
	}
	dir, err := saveDownloadsDir()
	if err != nil {
		return nil, errSaveFolder
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, errSaveFolder
	}
	var tmp *os.File
	for i := 0; i < 8 && tmp == nil; i++ {
		tmp, err = os.OpenFile(filepath.Join(dir, name+"."+randHex(4)+".oblireach-part"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	if tmp == nil {
		return nil, errSaveFolder
	}
	rc, err := fs.Open(remote)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	j := &saveJob{id: randomToken()[:32], state: "running", size: size, name: name, stop: stop}
	st.mu.Lock()
	if st.saves == nil {
		st.saves = map[string]*saveJob{}
	}
	st.saves[j.id] = j
	st.mu.Unlock()
	go saveRun(ctx, j, rc, tmp, dir, name)
	return j, nil
}

func saveRun(ctx context.Context, j *saveJob, rc io.ReadCloser, tmp *os.File, dir, name string) {
	defer j.stop()
	tmpPath := tmp.Name()
	// (a cancel also closes the remote file: a read waiting on it ends)
	go func() { <-ctx.Done(); _ = rc.Close() }()
	_, cerr := io.CopyBuffer(&saveProgress{ctx: ctx, w: tmp, j: j}, struct{ io.Reader }{rc}, make([]byte, saveChunk))
	if e := tmp.Close(); cerr == nil {
		cerr = e
	}
	if ctx.Err() != nil && cerr == nil {
		cerr = ctx.Err()
	}
	if cerr != nil {
		os.Remove(tmpPath)
		if ctx.Err() != nil {
			j.finish("cancelled", "", "")
		} else {
			j.finish("error", "", explorerCmdErrText(cerr))
		}
		return
	}
	final, err := saveFinalName(dir, name)
	if err == nil {
		err = os.Rename(tmpPath, filepath.Join(dir, final))
		if err != nil {
			os.Remove(filepath.Join(dir, final))
		}
	}
	if err != nil {
		os.Remove(tmpPath)
		j.finish("error", "", explorerCmdErrText(err))
		return
	}
	saveMarkFromElsewhere(filepath.Join(dir, final))
	j.finish("done", final, "")
}

// saveFinalName: a free name in dir, reserved (an empty file made with
// O_EXCL: the rename replaces only that one, never a file of the operator).
func saveFinalName(dir, name string) (string, error) {
	for n := 1; n < 1000; n++ {
		cand := name
		if n > 1 {
			cand = nameWithN(name, n)
		}
		f, err := os.OpenFile(filepath.Join(dir, cand), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return cand, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errExplorerExists
}
