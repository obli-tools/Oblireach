package main

// rdp_known_hosts.go — the trust store of the local RDP connections'
// server certificates (the stream backend: rdphost_relay.go), the RDP
// counterpart of known_hosts.go: trust on first use, never silent.
//
// The helper (rdphost) is given the fingerprints already trusted for its
// target (knownFingerprints of the connect line) and accepts them without a
// question; any other certificate is asked to the operator in the page
// (rdp_cert: its fingerprint, "another name", "changed" in red), the helper
// waiting. "Remember" files it here; a fingerprint that REPLACES another
// one for the same server is only filed after the operator confirmed the
// change explicitly (the page's second step). Accept without "remember":
// this session only (FreeRDP never writes its own store: its config folder
// is a cache of ours).
//
// One line per trusted certificate: "<host>:<port> <sha256 hex>", host in
// lower case (a name or an address as typed in the connection), the
// fingerprint as lower-case hex without separators. The file is not secret
// (public certificates) but private to the user (0600), in the app's local
// data folder (platformPaths.localDataDir()).

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	rdpKnownHostsFile = "rdp_known_hosts"
	rdpKnownHostsMax  = 4096 // lines kept at most (the oldest go first)
)

// rdpKnownHosts is safe for concurrent use.
type rdpKnownHosts struct {
	mu   sync.Mutex
	path string
}

var (
	rdpKnownOnce sync.Once
	rdpKnownApp  *rdpKnownHosts
)

// appRdpKnownHosts: the app's store (nil without a local data folder: every
// certificate is then asked, nothing remembered).
func appRdpKnownHosts() *rdpKnownHosts {
	rdpKnownOnce.Do(func() {
		if dir := platformPaths.localDataDir(); dir != "" {
			rdpKnownApp = &rdpKnownHosts{path: filepath.Join(dir, rdpKnownHostsFile)}
		}
	})
	return rdpKnownApp
}

// rdpHostKey: the key a server is filed under ("" when invalid).
func rdpHostKey(host string, port int) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || len(host) > 255 || strings.ContainsAny(host, " \t\r\n\x00") || port <= 0 || port > 65535 {
		return ""
	}
	return host + ":" + strconv.Itoa(port)
}

// normRdpFingerprint: "sha256 AB:cd:01", "AB:CD:01" and "abcd01" are the
// same fingerprint ("abcd01"); "" when it holds no hex digit.
func normRdpFingerprint(fp string) string {
	if i := strings.LastIndexByte(fp, ' '); i >= 0 {
		fp = fp[i+1:]
	}
	var b strings.Builder
	for _, c := range fp {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			b.WriteRune(c)
		case c >= 'A' && c <= 'F':
			b.WriteRune(c + ('a' - 'A'))
		}
	}
	if b.Len() > 128 {
		return ""
	}
	return b.String()
}

func (s *rdpKnownHosts) readLocked() ([][2]string, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][2]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		parts := strings.Fields(ln)
		if len(parts) != 2 {
			continue
		}
		if fp := normRdpFingerprint(parts[1]); fp != "" {
			out = append(out, [2]string{parts[0], fp})
		}
	}
	return out, sc.Err()
}

// known: the fingerprints trusted for key (several: a farm behind one name).
func (s *rdpKnownHosts) known(key string) []string {
	if s == nil || key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lines, _ := s.readLocked()
	var out []string
	for _, l := range lines {
		if l[0] == key {
			out = append(out, l[1])
		}
	}
	sort.Strings(out)
	return out
}

// trust files fp for key (idempotent). replace: the other fingerprints of
// key go (the operator confirmed a changed certificate).
func (s *rdpKnownHosts) trust(key, fp string, replace bool) error {
	if s == nil {
		return errors.New("rdp: no trust store")
	}
	fp = normRdpFingerprint(fp)
	if key == "" || fp == "" {
		return errors.New("rdp: invalid certificate")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lines, err := s.readLocked()
	if err != nil {
		return err
	}
	kept := lines[:0]
	for _, l := range lines {
		if l[0] == key && (replace || l[1] == fp) {
			continue
		}
		kept = append(kept, l)
	}
	kept = append(kept, [2]string{key, fp})
	if len(kept) > rdpKnownHostsMax {
		kept = kept[len(kept)-rdpKnownHostsMax:]
	}
	var b strings.Builder
	b.WriteString("# Oblireach: trusted RDP server certificates (host:port sha256)\n")
	for _, l := range kept {
		b.WriteString(l[0] + " " + l[1] + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".rdp_known_hosts-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(b.String())
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, s.path)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}
