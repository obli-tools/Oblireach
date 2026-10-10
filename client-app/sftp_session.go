package main

// sftp_session.go — the local RDP/SSH connection tree's SFTP transport
// (on top of ssh_session.go's terminal): a
// *sftp.Client wrapped around its OWN dedicated *ssh.Client (not the live
// terminal's, if one happens to be open for the same entry — keeping the
// two independent avoids threading a reference to a private field out of
// sshConn, and a second dial costs one extra handshake against a host the
// operator already trusts, never a second credential prompt since the
// vault reveals the secret again the same way sshConnect does).
//
// Directory entries never carry a secret; downloads/uploads stream through
// a bounded buffer (never the whole file in memory), same rule the RDP-like
// paste feature (agent/filexfer.go) already follows for its own transfers.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const sftpXferChunk = 32 * 1024

// sftpSession is one live SFTP connection: its own ssh.Client (never
// shared with a terminal's), the sftp.Client on top, and a cancel flag a
// transfer in progress checks between chunks so Close() can interrupt it
// promptly instead of leaving a goroutine writing to a dead connection.
type sftpSession struct {
	id      string
	client  *ssh.Client
	sftp    *sftp.Client
	mu      sync.Mutex
	closed  bool
	cancels map[string]context.CancelFunc // xferId -> cancel, for an in-flight transfer
	streams *sftpStreams                  // transfers in chunks (sftp_stream.go), closed with the session
	// No SFTP subsystem on the server (sftp nil): the same commands over SCP
	// and the server's shell (scp.go), on this same connection.
	scp *scpFS
	// vaultID: the connection's vault entry (its Info panel finds a live
	// connection by it: localconn_info.go); never a secret.
	vaultID string
}

var errSFTPUnavailable = errors.New("sftp: this server has no SFTP subsystem")

// explorerFS: what the explorer's commands run on: SFTP, else SCP.
func (s *sftpSession) explorerFS() explorerFS {
	if s.sftp == nil && s.scp != nil {
		return s.scp
	}
	return sftpExplorerFS{s}
}

// mode: "sftp", or "scp" (the fallback).
func (s *sftpSession) mode() string {
	if s.sftp == nil && s.scp != nil {
		return "scp"
	}
	return "sftp"
}

// streamsOf: the session's transfers in chunks (made on first use).
func (s *sftpSession) streamsOf() *sftpStreams {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams == nil {
		s.streams = newSftpStreams()
	}
	return s.streams
}

func (s *sftpSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *sftpSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for _, cancel := range s.cancels {
		cancel()
	}
	st := s.streams
	s.mu.Unlock()
	if st != nil {
		st.closeAll(nil) // (the connection goes: nothing to remove with)
	}
	if s.sftp != nil {
		s.sftp.Close()
	}
	if s.client != nil {
		s.client.Close()
	}
	return nil
}

func (s *sftpSession) registerXfer(xferID string) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return ctx
	}
	if s.cancels == nil {
		s.cancels = map[string]context.CancelFunc{}
	}
	s.cancels[xferID] = cancel
	s.mu.Unlock()
	return ctx
}

func (s *sftpSession) endXfer(xferID string) {
	s.mu.Lock()
	if s.cancels != nil {
		if cancel, ok := s.cancels[xferID]; ok {
			cancel()
			delete(s.cancels, xferID)
		}
	}
	s.mu.Unlock()
}

// sftpDial connects, verifies the host key against the same store the
// terminal uses (a host trusted by one is trusted by the other, and a
// change is caught by either), and opens the SFTP subsystem.
func sftpDial(ctx context.Context, p sshDialParams, store *knownHostsStore, forceTrust bool) (*sftpSession, error) {
	client, err := sshDial(ctx, p, store, forceTrust)
	if err != nil {
		return nil, err // may be *sshHostKeyPending — caller (bridge) handles it the same way sshConnect does
	}
	c, err := sftp.NewClient(client)
	if err != nil {
		// No SFTP subsystem (refused, or its server missing): SCP and the
		// shell on the same connection (scp.go), the same explorer on top.
		return &sftpSession{client: client, scp: newScpFS(sshScpStarter{client})}, nil
	}
	return &sftpSession{client: client, sftp: c}, nil
}

// sftpEntry is one row of a directory listing, sent to the page.
type sftpEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"` // unix seconds
}

var errSFTPPathInvalid = errors.New("sftp: invalid path")

// sftpCleanDir rejects the empty-navigation cases a page could send by
// mistake (never trust the page's path arithmetic over the server's own
// idea of what "up" means) — "." lists the entry point, everything else
// must be an absolute POSIX path the server itself returned via List.
func sftpCleanDir(p string) (string, error) {
	if p == "" {
		p = "."
	}
	if strings.Contains(p, "\x00") {
		return "", errSFTPPathInvalid
	}
	return p, nil
}

func (s *sftpSession) List(dir string) ([]sftpEntry, error) {
	if s.sftp == nil {
		return nil, errSFTPUnavailable
	}
	dir, err := sftpCleanDir(dir)
	if err != nil {
		return nil, err
	}
	infos, err := s.sftp.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]sftpEntry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, sftpEntry{Name: fi.Name(), IsDir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime().Unix()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// RealPath resolves "." and relative navigation server-side, so the page's
// breadcrumb is built from what the server actually considers the path to
// be, never from client-side string concatenation.
func (s *sftpSession) RealPath(p string) (string, error) {
	if s.sftp == nil {
		return "", errSFTPUnavailable
	}
	p, err := sftpCleanDir(p)
	if err != nil {
		return "", err
	}
	return s.sftp.RealPath(p)
}

func (s *sftpSession) Mkdir(dir, name string) error {
	if s.sftp == nil {
		return errSFTPUnavailable
	}
	if name == "" || strings.ContainsAny(name, "/\\\x00") {
		return errSFTPPathInvalid
	}
	dir, err := sftpCleanDir(dir)
	if err != nil {
		return err
	}
	full, err := s.RealPath(dir)
	if err != nil {
		return err
	}
	return s.sftp.Mkdir(sftpJoin(full, name))
}

func (s *sftpSession) Rename(dir, oldName, newName string) error {
	if s.sftp == nil {
		return errSFTPUnavailable
	}
	if newName == "" || strings.ContainsAny(newName, "/\\\x00") {
		return errSFTPPathInvalid
	}
	full, err := s.RealPath(dir)
	if err != nil {
		return err
	}
	return s.sftp.Rename(sftpJoin(full, oldName), sftpJoin(full, newName))
}

// Remove deletes one file, or one directory (which must already be empty —
// this pass does not walk and delete a whole subtree, the same
// conservative choice explorer_viewer.go's own "recursive" delete makes an
// explicit, confirmed operator action rather than something SFTP metadata
// calls do implicitly).
func (s *sftpSession) Remove(dir, name string) error {
	if s.sftp == nil {
		return errSFTPUnavailable
	}
	full, err := s.RealPath(dir)
	if err != nil {
		return err
	}
	target := sftpJoin(full, name)
	fi, err := s.sftp.Lstat(target)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return s.sftp.RemoveDirectory(target)
	}
	return s.sftp.Remove(target)
}

func sftpJoin(dir, name string) string {
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

// Download streams a remote file to an already-created local *os.File in
// sftpXferChunk-sized reads (never the whole file in memory), calling
// onProgress after each chunk. ctx cancelled -> stops promptly, the partial
// local file is the caller's to clean up (it knows whether to keep a
// partial download, this layer does not decide policy).
func (s *sftpSession) Download(ctx context.Context, remotePath string, dst *os.File, onProgress func(sent, total int64)) (int64, error) {
	if s.sftp == nil {
		return 0, errSFTPUnavailable
	}
	rf, err := s.sftp.Open(remotePath)
	if err != nil {
		return 0, err
	}
	defer rf.Close()
	total := int64(-1)
	if fi, err := rf.Stat(); err == nil {
		total = fi.Size()
	}
	buf := make([]byte, sftpXferChunk)
	var sent int64
	for {
		if err := ctx.Err(); err != nil {
			return sent, err
		}
		n, rerr := rf.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return sent, werr
			}
			sent += int64(n)
			if onProgress != nil {
				onProgress(sent, total)
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return sent, nil
			}
			return sent, rerr
		}
	}
}

// Upload is Download's mirror: a local *os.File streamed to a new remote
// file, sftpXferChunk at a time.
func (s *sftpSession) Upload(ctx context.Context, dir, name string, src *os.File, total int64, onProgress func(sent, total int64)) (int64, error) {
	if s.sftp == nil {
		return 0, errSFTPUnavailable
	}
	if name == "" || strings.ContainsAny(name, "/\\\x00") {
		return 0, errSFTPPathInvalid
	}
	full, err := s.RealPath(dir)
	if err != nil {
		return 0, err
	}
	rf, err := s.sftp.Create(sftpJoin(full, name))
	if err != nil {
		return 0, err
	}
	defer rf.Close()
	buf := make([]byte, sftpXferChunk)
	var sent int64
	for {
		if err := ctx.Err(); err != nil {
			return sent, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := rf.Write(buf[:n]); werr != nil {
				return sent, werr
			}
			sent += int64(n)
			if onProgress != nil {
				onProgress(sent, total)
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return sent, nil
			}
			return sent, rerr
		}
	}
}

// ---------------------------------------------------------------------------
// Registry: every live local SFTP session, keyed by an opaque id — same
// shape as appSSHRegistry (ssh_session.go), kept separate since a
// terminal's sshConn and a file explorer's sftpSession are independent
// connections that close independently.
// ---------------------------------------------------------------------------

type sftpSessionRegistry struct {
	mu   sync.Mutex
	sess map[string]*sftpSession
	seq  uint64
}

var appSFTPRegistry = &sftpSessionRegistry{sess: map[string]*sftpSession{}}

func (r *sftpSessionRegistry) newID() string {
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("lsftp_%d_%d", time.Now().UnixNano(), r.seq)
	r.mu.Unlock()
	return id
}

func (r *sftpSessionRegistry) put(id string, s *sftpSession) {
	s.id = id
	r.mu.Lock()
	r.sess[id] = s
	r.mu.Unlock()
}

func (r *sftpSessionRegistry) get(id string) (*sftpSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sess[id]
	return s, ok
}

func (r *sftpSessionRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.sess, id)
	r.mu.Unlock()
}
