package main

// sftp_session_test.go verifies sftp_session.go against a REAL in-process
// SFTP server (github.com/pkg/sftp's own server side, over a real SSH
// "sftp" subsystem channel from golang.org/x/crypto/ssh's server side) —
// the same "exercise the real protocol" standard ssh_session_test.go
// already sets for the terminal transport. genEd25519Signer/splitHostPort
// are reused from ssh_session_test.go (same package); a separate,
// subsystem-capable server is defined here rather than editing that file's
// shared testSSHServer, keeping the two test servers independent.

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type testSFTPServer struct {
	addr    string
	workDir string
}

func startTestSFTPServer(t *testing.T, user, pass string) *testSFTPServer {
	t.Helper()
	hostSigner, _ := genEd25519Signer(t, 9)
	workDir := t.TempDir()
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(password) == pass {
				return nil, nil
			}
			return nil, errAuthFailed
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &testSFTPServer{addr: ln.Addr().String(), workDir: workDir}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handleConn(nc, cfg)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return srv
}

var errAuthFailed = &testAuthError{}

type testAuthError struct{}

func (*testAuthError) Error() string { return "auth failed" }

func (s *testSFTPServer) handleConn(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, requests)
	}
}

func (s *testSFTPServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		if req.Type != "subsystem" {
			if req.WantReply {
				req.Reply(false, nil)
			}
			continue
		}
		// payload: uint32 length-prefixed subsystem name.
		name := string(req.Payload[4:])
		if name != "sftp" {
			req.Reply(false, nil)
			continue
		}
		req.Reply(true, nil)
		srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.workDir))
		if err != nil {
			return
		}
		srv.Serve()
		return
	}
}

func withEd25519HostKeyDial(t *testing.T, srv *testSFTPServer, user, pass string) *sftpSession {
	t.Helper()
	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, srv.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sftpDial(ctx, sshDialParams{Host: host, Port: port, Username: user, AuthMethod: vaultAuthPassword, Secret: pass}, store, true)
	if err != nil {
		t.Fatalf("sftpDial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestSFTPListEmptyDir(t *testing.T) {
	srv := startTestSFTPServer(t, "bob", "hunter2")
	sess := withEd25519HostKeyDial(t, srv, "bob", "hunter2")
	entries, err := sess.List(".")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("List(.) on an empty dir = %v, want none", entries)
	}
}

func TestSFTPMkdirListRenameDelete(t *testing.T) {
	srv := startTestSFTPServer(t, "bob", "hunter2")
	sess := withEd25519HostKeyDial(t, srv, "bob", "hunter2")

	if err := sess.Mkdir(".", "docs"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	entries, err := sess.List(".")
	if err != nil || len(entries) != 1 || entries[0].Name != "docs" || !entries[0].IsDir {
		t.Fatalf("List after Mkdir = %+v, %v", entries, err)
	}

	if err := sess.Rename(".", "docs", "documents"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	entries, err = sess.List(".")
	if err != nil || len(entries) != 1 || entries[0].Name != "documents" {
		t.Fatalf("List after Rename = %+v, %v", entries, err)
	}

	if err := sess.Remove(".", "documents"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	entries, err = sess.List(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("List after Remove = %+v, %v, want empty", entries, err)
	}
}

// TestSFTPMkdirRejectsPathSeparator proves a folder "name" cannot smuggle a
// path element into the remote server's directory (../, an absolute path,
// or a nested path) — Mkdir/Rename/a new upload's name must stay one
// element, the same discipline localfs.go's sanitizeLocalName holds the
// local side to.
func TestSFTPMkdirRejectsPathSeparator(t *testing.T) {
	srv := startTestSFTPServer(t, "bob", "hunter2")
	sess := withEd25519HostKeyDial(t, srv, "bob", "hunter2")
	for _, bad := range []string{"a/b", "a\\b", "../escape", ""} {
		if err := sess.Mkdir(".", bad); err == nil {
			t.Fatalf("Mkdir(%q) should have been rejected", bad)
		}
	}
}

func TestSFTPUploadThenDownloadRoundTrip(t *testing.T) {
	srv := startTestSFTPServer(t, "bob", "hunter2")
	sess := withEd25519HostKeyDial(t, srv, "bob", "hunter2")

	local := t.TempDir()
	srcPath := filepath.Join(local, "payload.bin")
	// Bigger than sftpXferChunk, so the round trip actually exercises more
	// than one chunk in each direction.
	payload := bytes.Repeat([]byte("obliance-sftp-chunk-test-"), 2000) // ~50KB
	if err := os.WriteFile(srcPath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	src, err := os.Open(srcPath)
	if err != nil {
		t.Fatalf("Open src: %v", err)
	}
	defer src.Close()

	var lastSent int64
	ctx := context.Background()
	sent, err := sess.Upload(ctx, ".", "payload.bin", src, int64(len(payload)), func(s, total int64) { lastSent = s })
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if sent != int64(len(payload)) || lastSent != sent {
		t.Fatalf("Upload sent=%d lastSent=%d, want %d", sent, lastSent, len(payload))
	}

	entries, err := sess.List(".")
	if err != nil || len(entries) != 1 || entries[0].Name != "payload.bin" || entries[0].Size != int64(len(payload)) {
		t.Fatalf("List after Upload = %+v, %v", entries, err)
	}

	full, err := sess.RealPath(".")
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	dstPath := filepath.Join(local, "downloaded.bin")
	dst, err := os.Create(dstPath)
	if err != nil {
		t.Fatalf("Create dst: %v", err)
	}
	got, err := sess.Download(ctx, sftpJoin(full, "payload.bin"), dst, nil)
	dst.Close()
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got != int64(len(payload)) {
		t.Fatalf("Download got=%d, want %d", got, len(payload))
	}
	roundTripped, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(roundTripped, payload) {
		t.Fatal("downloaded bytes differ from the uploaded payload")
	}
}

func TestSFTPDownloadCancelledContextStopsPromptly(t *testing.T) {
	srv := startTestSFTPServer(t, "bob", "hunter2")
	sess := withEd25519HostKeyDial(t, srv, "bob", "hunter2")

	local := t.TempDir()
	src, err := os.Create(filepath.Join(local, "big.bin"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	payload := bytes.Repeat([]byte("x"), 5*1024*1024) // 5MB, several chunks
	if _, err := src.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	src.Close()
	f, _ := os.Open(filepath.Join(local, "big.bin"))
	defer f.Close()
	if _, err := sess.Upload(context.Background(), ".", "big.bin", f, int64(len(payload)), nil); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	full, err := sess.RealPath(".")
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the first read
	dst, _ := os.Create(filepath.Join(local, "partial.bin"))
	defer dst.Close()
	_, err = sess.Download(ctx, sftpJoin(full, "big.bin"), dst, nil)
	if err == nil {
		t.Fatal("Download with a cancelled context should have returned an error")
	}
}

func TestSFTPRegistry(t *testing.T) {
	r := &sftpSessionRegistry{sess: map[string]*sftpSession{}}
	id := r.newID()
	if id == "" {
		t.Fatal("newID returned empty string")
	}
	s := &sftpSession{}
	r.put(id, s)
	got, ok := r.get(id)
	if !ok || got != s {
		t.Fatalf("get(%q) = %v, %v, want %v, true", id, got, ok, s)
	}
	r.remove(id)
	if _, ok := r.get(id); ok {
		t.Fatal("entry still present after remove")
	}
}

func TestSFTPCloseCancelsInFlightTransfer(t *testing.T) {
	srv := startTestSFTPServer(t, "bob", "hunter2")
	sess := withEd25519HostKeyDial(t, srv, "bob", "hunter2")
	ctx := sess.registerXfer("xfer1")
	if err := ctx.Err(); err != nil {
		t.Fatalf("context cancelled before Close(): %v", err)
	}
	sess.Close()
	if err := ctx.Err(); err == nil {
		t.Fatal("Close() should cancel every registered transfer")
	}
}
