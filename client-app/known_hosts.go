package main

// known_hosts.go — the local RDP/SSH connection tree's (connections_page.go,
// vault.go's kind=SSH entries) host key trust store: a plain-text,
// OpenSSH-compatible "known_hosts" file (host keys are public — nothing here
// is sealed like the vault's secrets), independent of any Obliance server.
//
// Trust-on-first-use, never silent: sshDial (ssh_session.go) always verifies
// against this store and never falls back to ssh.InsecureIgnoreHostKey. A
// host never seen before, or one whose key no longer matches what was
// trusted last time (a real MITM signal, or a reinstalled server), is
// reported back as *sshHostKeyPending — the caller must show the
// fingerprint and get an explicit operator decision before a second dial
// with forceTrust=true both records and uses the new key in one step.

import (
	"bufio"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

type hostKeyState int

const (
	hostKeyUnknown hostKeyState = iota
	hostKeyTrusted
	hostKeyMismatch
)

// testKnownHostsPathOverride: known_hosts_test.go points this at a temp file
// so tests never touch the real %LOCALAPPDATA%.
var testKnownHostsPathOverride string

func knownHostsPath() (string, error) {
	if testKnownHostsPathOverride != "" {
		return testKnownHostsPathOverride, nil
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("known_hosts: no home directory")
		}
		local = filepath.Join(home, "AppData", "Local")
	}
	dir := filepath.Join(local, "OblireachClient")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "known_hosts"), nil
}

// knownHostsStore is safe for concurrent use (one per running Desktop
// instance is enough; cross-instance exclusivity is not attempted, same
// stance as the vault's fileVaultStore).
type knownHostsStore struct {
	mu   sync.Mutex
	path string
}

func newKnownHostsStore() (*knownHostsStore, error) {
	p, err := knownHostsPath()
	if err != nil {
		return nil, err
	}
	return &knownHostsStore{path: p}, nil
}

// normalizeHostPort is the key a host is filed under: "[host]:port", so a
// non-standard port never collides with the default one for the same host.
func normalizeHostPort(host string, port int) string {
	return fmt.Sprintf("[%s]:%d", host, port)
}

func (s *knownHostsStore) readLines() ([]string, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		lines = append(lines, ln)
	}
	return lines, sc.Err()
}

// check reports whether hostport+key is already trusted, unseen, or seen
// before under a DIFFERENT key of the same algorithm (a mismatch — the one
// case that must never be silently accepted). A host trusted for one key
// algorithm and now offering another is "unknown" for that algorithm, same
// as OpenSSH: it can hold several trusted algorithms per host at once.
func (s *knownHostsStore) check(hostport string, key ssh.PublicKey) (hostKeyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines, err := s.readLines()
	if err != nil {
		return hostKeyUnknown, err
	}
	marshaled := key.Marshal()
	sameAlgo := false
	for _, ln := range lines {
		parts := strings.SplitN(ln, " ", 3)
		if len(parts) != 3 || parts[0] != hostport || parts[1] != key.Type() {
			continue
		}
		sameAlgo = true
		raw, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(raw, marshaled) == 1 {
			return hostKeyTrusted, nil
		}
	}
	if sameAlgo {
		return hostKeyMismatch, nil
	}
	return hostKeyUnknown, nil
}

// trust appends hostport+key as a newly trusted line. It never removes a
// prior (mismatched) entry for the same host+algorithm — both stay on file,
// same as OpenSSH leaving stale lines behind; only the newest exact match
// for a given host+algorithm is what check() reports as trusted going
// forward is NOT true here (check scans all lines and trusts on any exact
// match) — so a stale mismatched line for an old key remains "not trusted,
// not this one" harmlessly, never blocking the new key from working.
func (s *knownHostsStore) trust(hostport string, key ssh.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	line := hostport + " " + key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal()) + "\n"
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

func fingerprintSHA256(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }
