//go:build windows

package main

// support_store_windows.go — where "Connect by code" keeps its support
// servers (support.go): one file, support.dat, sealed by DPAPI for the
// current Windows user with its own entropy, in the vault's private folder
// (vault_windows.go makePrivateNamedDir: this user, SYSTEM, Administrators).
// The operator keys are never written anywhere else.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var supportEntropy = sha256.Sum256([]byte("Oblireach Desktop support servers v1"))

const (
	supportFileName = "support.dat"
	// supportFileMax: a sane bound for the sealed file (32 servers).
	supportFileMax = 1 << 20
)

type fileSupportStore struct {
	mu  sync.Mutex
	dir string
}

func newSupportStore() supportBlobStore { return &fileSupportStore{dir: vaultBaseDir()} }

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
	err = dpapiUnprotectWith(b, supportEntropy[:], func(plain []byte) error {
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
	if err := makePrivateNamedDir(s.dir); err != nil {
		return err
	}
	blob, err := dpapiProtectWith(plain, supportEntropy[:])
	if err != nil {
		return err
	}
	tmp := s.path() + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
