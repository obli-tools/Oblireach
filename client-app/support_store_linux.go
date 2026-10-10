//go:build linux

package main

// support_store_linux.go — where "Connect by code" keeps its support servers
// (support.go) on Linux: one file, support.dat, in the vault's private
// folder (0700, the file 0600), sealed by the Secret Service protector under
// its own key (purpose support-key: vault_linux.go ssProtector). The
// operator keys are never written anywhere else.

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	supportFileName = "support.dat"
	// supportFileMax: a sane bound for the sealed file (32 servers).
	supportFileMax = 1 << 20
)

type fileSupportStore struct {
	mu   sync.Mutex
	dir  string
	prot protector
}

func newSupportStore() supportBlobStore {
	return &fileSupportStore{dir: vaultBaseDir(), prot: ssProtector{purpose: ssPurposeSupport, keys: appKeyStore}}
}

func (s *fileSupportStore) path() string { return filepath.Join(s.dir, supportFileName) }

func (s *fileSupportStore) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return nil, errors.New("no local application data folder")
	}
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) > supportFileMax {
		return nil, errors.New("support settings file is invalid")
	}
	var out []byte
	err = s.prot.Unprotect(b, func(plain []byte) error {
		out = append([]byte(nil), plain...)
		return nil
	})
	return out, err
}

func (s *fileSupportStore) Save(plain []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return errors.New("no local application data folder")
	}
	if err := makePrivateDir(s.dir); err != nil {
		return err
	}
	blob, err := s.prot.Protect(plain)
	if err != nil {
		return err
	}
	return writePrivateFile(s.path(), blob)
}
