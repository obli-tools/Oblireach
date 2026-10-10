package main

// ssh_session_test.go verifies sshDial/openShellConn against a REAL
// in-process SSH server (golang.org/x/crypto/ssh's own server side), the
// same "exercise the real protocol" standard as the pipe-shell repo's
// OBLIANCE_TEST_PIPE_SHELL integration test: this is not a mock of the SSH
// wire protocol, it is a real client talking real SSH to a real (if
// minimal) server over a real loopback TCP connection.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// genEd25519Signer returns a deterministic (seed-keyed) ed25519 key pair as
// an ssh.Signer and its ssh.PublicKey — deterministic so a test can compare
// "the same key" across two calls without stashing bytes itself.
func genEd25519Signer(t *testing.T, seed byte) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	seedBytes := bytes.Repeat([]byte{seed}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(seedBytes)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	return signer, signer.PublicKey()
}

func genRSASigner(t *testing.T) ssh.Signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	return signer
}

// testSSHServer is a minimal real SSH server: one host key, password auth
// against a fixed user/pass, and a shell that echoes every line it reads
// back prefixed "echo: " (enough to prove data flows both ways through a
// real PTY session, without needing an actual OS shell in CI).
type testSSHServer struct {
	addr     string
	hostKey  ssh.Signer
	user     string
	pass     string
	listener net.Listener
}

func startTestSSHServer(t *testing.T, hostKeySeed byte, user, pass string) *testSSHServer {
	t.Helper()
	hostSigner, _ := genEd25519Signer(t, hostKeySeed)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(password) == pass {
				return nil, nil
			}
			return nil, errors.New("auth failed")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &testSSHServer{addr: ln.Addr().String(), hostKey: hostSigner, user: user, pass: pass, listener: ln}

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

func (s *testSSHServer) handleConn(nc net.Conn, cfg *ssh.ServerConfig) {
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

func (s *testSSHServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		switch req.Type {
		case "pty-req", "shell", "window-change":
			if req.WantReply {
				req.Reply(true, nil)
			}
			if req.Type == "shell" {
				go s.echoLoop(ch)
			}
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// echoLoop: read whatever arrives, write it straight back prefixed — proves
// stdin -> server -> stdout round-trips through the real ssh.Channel.
func (s *testSSHServer) echoLoop(ch ssh.Channel) {
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			ch.Write([]byte("echo: "))
			ch.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

func TestSSHDialUnknownHostKeyIsPending(t *testing.T) {
	srv := startTestSSHServer(t, 1, "alice", "s3cret")
	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, srv.addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := sshDial(ctx, sshDialParams{Host: host, Port: port, Username: "alice", AuthMethod: vaultAuthPassword, Secret: "s3cret"}, store, false)
	if err == nil {
		t.Fatal("expected a pending host-key error on first connect, got nil (must never silently trust)")
	}
	var pend *sshHostKeyPending
	if !errors.As(err, &pend) {
		t.Fatalf("error = %v (%T), want *sshHostKeyPending", err, err)
	}
	if pend.State != hostKeyUnknown {
		t.Fatalf("pend.State = %v, want unknown", pend.State)
	}
	if pend.Fingerprint == "" {
		t.Fatal("pend.Fingerprint is empty")
	}
}

func TestSSHDialForceTrustThenConnects(t *testing.T) {
	srv := startTestSSHServer(t, 1, "alice", "s3cret")
	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, srv.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := sshDial(ctx, sshDialParams{Host: host, Port: port, Username: "alice", AuthMethod: vaultAuthPassword, Secret: "s3cret"}, store, true)
	if err != nil {
		t.Fatalf("sshDial with forceTrust: %v", err)
	}
	defer client.Close()

	// A second, ordinary dial (forceTrust=false) must now succeed silently:
	// the key was recorded by the first call.
	client2, err := sshDial(ctx, sshDialParams{Host: host, Port: port, Username: "alice", AuthMethod: vaultAuthPassword, Secret: "s3cret"}, store, false)
	if err != nil {
		t.Fatalf("sshDial after trust: %v", err)
	}
	client2.Close()
}

func TestSSHDialWrongPasswordFails(t *testing.T) {
	srv := startTestSSHServer(t, 1, "alice", "s3cret")
	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, srv.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := sshDial(ctx, sshDialParams{Host: host, Port: port, Username: "alice", AuthMethod: vaultAuthPassword, Secret: "wrong"}, store, true)
	if err == nil {
		t.Fatal("expected an auth error with the wrong password")
	}
	var pend *sshHostKeyPending
	if errors.As(err, &pend) {
		t.Fatal("a wrong password must not surface as a host-key error")
	}
}

func TestSSHDialMismatchedHostKeyRefused(t *testing.T) {
	srv := startTestSSHServer(t, 1, "alice", "s3cret")
	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, srv.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A MITM presenting a key of its own for a host:port the real server
	// already occupies: trust an impostor key for this hostport directly
	// (standing in for "an attacker's key was trusted by mistake, or a
	// previous different server at this address"), then dial the REAL
	// server: its actual host key does not match the impostor's, so it
	// must be refused — check() must never treat "some key is trusted for
	// this host+algorithm" as "this key is trusted".
	impostor := testHostKey(t, 99)
	hp := normalizeHostPort(host, port)
	if err := store.trust(hp, impostor); err != nil {
		t.Fatalf("trust: %v", err)
	}
	_, err := sshDial(ctx, sshDialParams{Host: host, Port: port, Username: "alice", AuthMethod: vaultAuthPassword, Secret: "s3cret"}, store, false)
	if err == nil {
		t.Fatal("expected a pending mismatch error, got nil (a changed host key must never be silently accepted)")
	}
	var pend *sshHostKeyPending
	if !errors.As(err, &pend) {
		t.Fatalf("error = %v (%T), want *sshHostKeyPending", err, err)
	}
	if pend.State != hostKeyMismatch {
		t.Fatalf("pend.State = %v, want mismatch", pend.State)
	}
}

func TestSSHShellRoundTrip(t *testing.T) {
	srv := startTestSSHServer(t, 1, "alice", "s3cret")
	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, srv.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := sshDial(ctx, sshDialParams{Host: host, Port: port, Username: "alice", AuthMethod: vaultAuthPassword, Secret: "s3cret"}, store, true)
	if err != nil {
		t.Fatalf("sshDial: %v", err)
	}

	dataCh := make(chan []byte, 16)
	closeCh := make(chan error, 1)
	conn, err := openShellConn("t1", client, 80, 24, func(b []byte) { dataCh <- b }, func(err error) { closeCh <- err })
	if err != nil {
		t.Fatalf("openShellConn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var got bytes.Buffer
	deadline := time.After(3 * time.Second)
	for !bytes.Contains(got.Bytes(), []byte("echo: hello")) {
		select {
		case chunk := <-dataCh:
			got.Write(chunk)
		case <-deadline:
			t.Fatalf("timed out waiting for echo; got so far: %q", got.String())
		}
	}

	if err := conn.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-closeCh:
	case <-time.After(3 * time.Second):
		t.Fatal("onClose never fired after Close()")
	}
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Fatal("Write after Close must fail")
	}
}

func TestSSHConnRegistry(t *testing.T) {
	r := &sshConnRegistry{conns: map[string]*sshConn{}}
	id := r.newID()
	if id == "" {
		t.Fatal("newID returned empty string")
	}
	c := &sshConn{id: id}
	r.put(c)
	got, ok := r.get(id)
	if !ok || got != c {
		t.Fatalf("get(%q) = %v, %v, want %v, true", id, got, ok, c)
	}
	r.remove(id)
	if _, ok := r.get(id); ok {
		t.Fatal("entry still present after remove")
	}
}

// TestSSHPrivateKeyAuth proves key-based auth end-to-end with a real
// ed25519 client key the server explicitly authorizes — the auth path a
// vaultAuthKey entry exercises.
func TestSSHPrivateKeyAuth(t *testing.T) {
	_, clientPub := genEd25519Signer(t, 5)
	hostSigner, _ := genEd25519Signer(t, 1)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientPub.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unauthorized key")
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(reqs)
		for newCh := range chans {
			ch, requests, err := newCh.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer ch.Close()
				for req := range requests {
					if req.WantReply {
						req.Reply(true, nil)
					}
				}
			}()
		}
	}()

	store := withTestKnownHosts(t)
	host, port := splitHostPort(t, ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pemBytes := marshalEd25519PrivateKeyPEM(t, 5)
	client, err := sshDial(ctx, sshDialParams{Host: host, Port: port, Username: "bob", AuthMethod: vaultAuthKey, Secret: string(pemBytes)}, store, true)
	if err != nil {
		t.Fatalf("sshDial with key auth: %v", err)
	}
	client.Close()
}

// marshalEd25519PrivateKeyPEM renders an OpenSSH-format private key PEM the
// same shape a vault entry's Secret field would hold for authMethod=key —
// x/crypto/ssh can parse it back with ssh.ParsePrivateKey (sshAuthMethod).
// ssh.MarshalPrivateKey needs the original crypto.PrivateKey, not the
// ssh.Signer wrapper — genEd25519Signer built it from a deterministic seed,
// so this rebuilds the identical ed25519.PrivateKey directly rather than
// threading it through an extra return value everywhere else.
func marshalEd25519PrivateKeyPEM(t *testing.T, seed byte) []byte {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	return pem.EncodeToMemory(block)
}
