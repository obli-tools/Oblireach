//go:build linux

package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// FilePortalStore keeps the portal permission (Options.PortalPermission) in
// a file readable by the user only. The content is a restore token: it lets
// this app skip the desktop's dialog, nothing else (the desktop can revoke
// it); a host that wants it sealed implements PortalPermissionStore itself.
type FilePortalStore struct{ Path string }

// NewFilePortalStore returns a store at path ("" = $XDG_DATA_HOME/oblireach/portal-permission).
func NewFilePortalStore(path string) *FilePortalStore {
	if path == "" {
		if dir := DefaultOpenH264Dir(); dir != "" {
			path = filepath.Join(filepath.Dir(dir), "portal-permission")
		}
	}
	return &FilePortalStore{Path: path}
}

func (s *FilePortalStore) Load() (string, error) {
	if s.Path == "" {
		return "", errors.New("no path")
	}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// Save writes atomically; an empty blob removes the file (nothing kept).
func (s *FilePortalStore) Save(blob string) error {
	if s.Path == "" {
		return errors.New("no path")
	}
	if blob == "" {
		err := os.Remove(s.Path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".portal-permission-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(blob)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, s.Path)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}
