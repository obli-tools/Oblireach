package main

// sftp_stream.go — files of any size to and from a local SSH connection
// (localconn_explorer.go), in chunks: the explorer's single-message
// upload_file / download_file carry a whole file in base64 (the Obliance
// agent's protocol, 100 MB at most); a local connection's SFTP session is
// the app's own, so its transfers (transfers.go: the Files page's split
// view, files dropped on its terminal) stream instead:
//
//   upload_open  {path, overwrite}        -> {handle}   (its folders created)
//   upload_write {handle, offset, data}   -> {written}  (in order, base64)
//   upload_close {handle}                 -> {size}
//   upload_abort {handle}                 -> {}         (the partial file removed)
//   download_open  {path}                 -> {handle, size}
//   download_read  {handle, offset, length} -> {data, eof}
//   download_close {handle}               -> {}
//
// Handles belong to their SFTP session (closed with it), a few at a time, and
// one left idle is closed (an upload's partial file removed).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	sftpStreamMax      = 8
	sftpStreamIdle     = 10 * time.Minute
	sftpStreamMaxChunk = 4 << 20 // bytes per upload_write / download_read
)

var (
	errStreamHandle = errors.New("transfer handle not found")
	errStreamOrder  = errors.New("transfer out of order")
	errStreamFull   = errors.New("too many transfers at once")
)

type sftpStream struct {
	path    string
	up      bool
	w       io.WriteCloser
	r       io.ReadCloser
	size    int64 // download: the file's size; upload: bytes written
	pos     int64 // download: the next offset read
	last    time.Time
	writing sync.Mutex
}

type sftpStreams struct {
	mu      sync.Mutex
	handles map[string]*sftpStream
	now     func() time.Time
	saves   map[string]*saveJob // downloads the app saves itself (sftp_save.go)
}

func newSftpStreams() *sftpStreams {
	return &sftpStreams{handles: map[string]*sftpStream{}, now: time.Now}
}

func (s *sftpStreams) pruneLocked(fs explorerFS) {
	now := s.now()
	for id, h := range s.handles {
		if now.Sub(h.last) > sftpStreamIdle {
			delete(s.handles, id)
			go h.drop(fs)
		}
	}
}

// drop closes a handle (an upload left unfinished: its partial file removed).
func (h *sftpStream) drop(fs explorerFS) {
	if h.up {
		if h.w != nil {
			_ = h.w.Close()
		}
		if fs != nil {
			_ = fs.Remove(h.path)
		}
		return
	}
	if h.r != nil {
		_ = h.r.Close()
	}
}

// closeAll: the session ends (its handles with it).
func (s *sftpStreams) closeAll(fs explorerFS) {
	s.mu.Lock()
	hs := s.handles
	s.handles = map[string]*sftpStream{}
	sv := s.saves
	s.saves = nil
	s.mu.Unlock()
	for _, h := range hs {
		h.drop(fs)
	}
	for _, j := range sv {
		j.cancel() // (its partial file removed by its own goroutine)
	}
}

func (s *sftpStreams) add(fs explorerFS, h *sftpStream) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(fs)
	if len(s.handles) >= sftpStreamMax {
		return "", errStreamFull
	}
	id := randomToken()[:32]
	h.last = s.now()
	s.handles[id] = h
	return id, nil
}

func (s *sftpStreams) get(id string) (*sftpStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.handles[id]
	if h == nil {
		return nil, errStreamHandle
	}
	h.last = s.now()
	return h, nil
}

func (s *sftpStreams) take(id string) (*sftpStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.handles[id]
	if h == nil {
		return nil, errStreamHandle
	}
	delete(s.handles, id)
	return h, nil
}

func (s *sftpStreams) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.handles)
}

// explorerMkdirAll: the folders of p's parent, created when missing (an
// upload of a folder's files: the agent's upload creates them too).
func explorerMkdirAll(fs explorerFS, dir string, depth int) error {
	if dir == "" || dir == "/" || depth > 64 {
		return nil
	}
	if fi, err := fs.Lstat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s: not a folder", dir)
		}
		return nil
	}
	if err := explorerMkdirAll(fs, path.Dir(dir), depth+1); err != nil {
		return err
	}
	if err := fs.Mkdir(dir); err != nil {
		if fi, e2 := fs.Lstat(dir); e2 == nil && fi.IsDir() {
			return nil // (made meanwhile)
		}
		return err
	}
	return nil
}

// streamCmd runs one streaming command (handled: false when typ is not one).
func streamCmd(fs explorerFS, st *sftpStreams, typ string, payload []byte) (interface{}, bool, error) {
	if !strings.HasPrefix(typ, "upload_") && !strings.HasPrefix(typ, "download_") || typ == "upload_file" || typ == "download_file" {
		return nil, false, nil
	}
	var in struct {
		Path      string `json:"path"`
		Overwrite bool   `json:"overwrite"`
		Handle    string `json:"handle"`
		Offset    int64  `json:"offset"`
		Length    int    `json:"length"`
		Data      string `json:"data"`
		Size      *int64 `json:"size"` // upload_open: the file's size (SCP needs it first)
	}
	if len(payload) > 0 && json.Unmarshal(payload, &in) != nil {
		return nil, true, errExplorerBadPath
	}
	switch typ {
	case "upload_open":
		p, err := explorerCleanPath(in.Path, false)
		if err != nil || p == "/" {
			return nil, true, errExplorerBadPath
		}
		if fi, err := fs.Lstat(p); err == nil {
			if fi.IsDir() || !in.Overwrite {
				return nil, true, errExplorerExists
			}
		}
		if err := explorerMkdirAll(fs, path.Dir(p), 0); err != nil {
			return nil, true, err
		}
		size := int64(-1)
		if in.Size != nil && *in.Size >= 0 {
			size = *in.Size
		}
		w, err := explorerCreate(fs, p, size)
		if err != nil {
			return nil, true, err
		}
		id, err := st.add(fs, &sftpStream{path: p, up: true, w: w})
		if err != nil {
			_ = w.Close()
			_ = fs.Remove(p)
			return nil, true, err
		}
		return map[string]string{"handle": id}, true, nil
	case "upload_write":
		h, err := st.get(in.Handle)
		if err != nil || !h.up {
			return nil, true, errStreamHandle
		}
		data, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil || len(data) > sftpStreamMaxChunk {
			return nil, true, errExplorerBadPath
		}
		h.writing.Lock()
		defer h.writing.Unlock()
		if in.Offset != h.size {
			return nil, true, errStreamOrder
		}
		if _, err := h.w.Write(data); err != nil {
			return nil, true, err
		}
		h.size += int64(len(data))
		return map[string]int64{"written": h.size}, true, nil
	case "upload_close":
		h, err := st.take(in.Handle)
		if err != nil || !h.up {
			return nil, true, errStreamHandle
		}
		h.writing.Lock()
		defer h.writing.Unlock()
		if err := h.w.Close(); err != nil {
			_ = fs.Remove(h.path)
			return nil, true, err
		}
		return map[string]int64{"size": h.size}, true, nil
	case "upload_abort":
		if h, err := st.take(in.Handle); err == nil && h.up {
			h.writing.Lock()
			h.drop(fs)
			h.writing.Unlock()
		}
		return struct{}{}, true, nil
	case "download_open":
		p, err := explorerCleanPath(in.Path, false)
		if err != nil {
			return nil, true, err
		}
		fi, err := fs.Lstat(p)
		if err != nil {
			return nil, true, err
		}
		if fi.IsDir() {
			return nil, true, errExplorerBadPath
		}
		r, err := fs.Open(p)
		if err != nil {
			return nil, true, err
		}
		id, err := st.add(fs, &sftpStream{path: p, r: r, size: fi.Size()})
		if err != nil {
			_ = r.Close()
			return nil, true, err
		}
		return map[string]interface{}{"handle": id, "size": fi.Size()}, true, nil
	case "download_read":
		h, err := st.get(in.Handle)
		if err != nil || h.up {
			return nil, true, errStreamHandle
		}
		if in.Length <= 0 || in.Length > sftpStreamMaxChunk || in.Offset < 0 {
			return nil, true, errExplorerBadPath
		}
		h.writing.Lock()
		defer h.writing.Unlock()
		buf := make([]byte, in.Length)
		var n int
		if ra, ok := h.r.(io.ReaderAt); ok {
			n, err = ra.ReadAt(buf, in.Offset)
		} else {
			if in.Offset != h.pos {
				return nil, true, errStreamOrder
			}
			n, err = io.ReadFull(h.r, buf)
		}
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, true, err
		}
		h.pos = in.Offset + int64(n)
		eof := n < in.Length || h.pos >= h.size
		return map[string]interface{}{"data": base64.StdEncoding.EncodeToString(buf[:n]), "eof": eof}, true, nil
	case "download_close":
		if h, err := st.take(in.Handle); err == nil && !h.up {
			h.drop(fs)
		}
		return struct{}{}, true, nil
	}
	return nil, false, nil
}
