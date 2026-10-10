package main

// ssh_session.go — the local RDP/SSH connection tree's SSH transport: dial +
// mandatory host key verification (known_hosts.go, never
// ssh.InsecureIgnoreHostKey) + an interactive PTY shell, streamed to a
// caller-supplied onData callback. The bridge (ssh_bridge.go)
// pushes that data to the page's xterm.js via Eval, the same way the vault
// pushes its async results (vault_bridge.go's evalVault) — the
// rendering (xterm) is untouched; only the transport differs from an
// ObliReach agent tunnel.
//
// SFTP / the file explorer lives in sftp_session.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshHostKeyPending is returned (as the dial's error, unwrap with
// errors.As) when the host key needs the operator's decision. The caller
// shows Fingerprint/State and, on accept, dials again with forceTrust=true —
// which both records and uses the new key in the same call, so there is no
// window between "shown" and "trusted" where a different key could be
// substituted.
type sshHostKeyPending struct {
	State       hostKeyState
	Fingerprint string
	Hostport    string
}

func (e *sshHostKeyPending) Error() string {
	if e.State == hostKeyMismatch {
		return "ssh: host key changed"
	}
	return "ssh: host key unknown"
}

type sshDialParams struct {
	Host       string
	Port       int
	Username   string
	AuthMethod byte // vaultAuthPassword / vaultAuthKey (vault_codec.go)
	Secret     string
	Passphrase string // a private key's passphrase ("" = none)
}

// sshDial connects and verifies the host key, but does not open a shell.
// ctx bounds the TCP connect and the handshake together.
func sshDial(ctx context.Context, p sshDialParams, store *knownHostsStore, forceTrust bool) (*ssh.Client, error) {
	auth, err := sshAuthMethod(p)
	if err != nil {
		return nil, err
	}
	hostport := normalizeHostPort(p.Host, p.Port)
	var verifyErr error
	cfg := &ssh.ClientConfig{
		User: p.Username,
		Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			state, err := store.check(hostport, key)
			if err != nil {
				verifyErr = err
				return err
			}
			if state == hostKeyTrusted {
				return nil
			}
			if forceTrust {
				if err := store.trust(hostport, key); err != nil {
					verifyErr = err
					return err
				}
				return nil
			}
			verifyErr = &sshHostKeyPending{State: state, Fingerprint: fingerprintSHA256(key), Hostport: hostport}
			return verifyErr
		},
		Timeout: 15 * time.Second,
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, hostport, cfg)
	if err != nil {
		conn.Close()
		var pend *sshHostKeyPending
		if errors.As(verifyErr, &pend) {
			return nil, pend
		}
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

func sshAuthMethod(p sshDialParams) (ssh.AuthMethod, error) {
	switch p.AuthMethod {
	case vaultAuthPassword:
		return ssh.Password(p.Secret), nil
	case vaultAuthKey:
		var signer ssh.Signer
		var err error
		if p.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(p.Secret), []byte(p.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(p.Secret))
		}
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		return ssh.PublicKeys(signer), nil
	default:
		return nil, errors.New("ssh: unknown auth method")
	}
}

// sshConn is one live shell: its client and PTY session, plus the stdin
// writer the bridge's "send" binding uses.
type sshConn struct {
	id     string
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser

	mu     sync.Mutex
	closed bool // Write/Resize refuse once true; set before either close path fires onClose

	// onClose fires exactly once — whichever happens first, an explicit
	// Close() or the remote/network ending the session on its own — never
	// twice, never zero times.
	closeOnce sync.Once
	onClose   func(error)
}

func (c *sshConn) fireClose(err error) {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose(err)
		}
	})
}

// openShellConn opens a PTY shell on an already-dialed client. onData is
// called (from a background goroutine, possibly concurrently for stdout and
// stderr) with each chunk read; onClose once, when the shell session ends
// for any reason (remote exit, network drop, an explicit Close()).
func openShellConn(id string, client *ssh.Client, cols, rows int, onData func([]byte), onClose func(err error)) (*sshConn, error) {
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	c := &sshConn{id: id, client: client, sess: sess, stdin: stdin, onClose: onClose}
	go sshPump(stdout, onData)
	go sshPump(stderr, onData)
	go func() {
		waitErr := sess.Wait()
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		client.Close()
		c.fireClose(waitErr)
	}()
	return c, nil
}

func sshPump(r io.Reader, onData func([]byte)) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			onData(chunk)
		}
		if err != nil {
			return
		}
	}
}

func (c *sshConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, errors.New("ssh: session closed")
	}
	return c.stdin.Write(p)
}

func (c *sshConn) Resize(cols, rows int) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("ssh: session closed")
	}
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return c.sess.WindowChange(rows, cols)
}

func (c *sshConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	err := c.sess.Close()
	c.client.Close()
	c.fireClose(nil)
	return err
}

// ---------------------------------------------------------------------------
// Registry: every live local SSH shell, keyed by an opaque connection id the
// page holds (never a secret, never reused across dials).
// ---------------------------------------------------------------------------

type sshConnRegistry struct {
	mu    sync.Mutex
	conns map[string]*sshConn
	seq   uint64
}

var appSSHRegistry = &sshConnRegistry{conns: map[string]*sshConn{}}

func (r *sshConnRegistry) newID() string {
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("lssh_%d_%d", time.Now().UnixNano(), r.seq)
	r.mu.Unlock()
	return id
}

func (r *sshConnRegistry) put(c *sshConn) {
	r.mu.Lock()
	r.conns[c.id] = c
	r.mu.Unlock()
}

func (r *sshConnRegistry) get(id string) (*sshConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.conns[id]
	return c, ok
}

func (r *sshConnRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.conns, id)
	r.mu.Unlock()
}
