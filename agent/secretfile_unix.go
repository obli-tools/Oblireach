//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// config.json (API key + server URL): root only.
const secretFileMode os.FileMode = 0o600

// writeSecretFile writes a file only root can read (0600), atomically: the
// temp file of the same directory is created 0600 by os.CreateTemp (never
// world-readable, not even briefly), then renamed over path, which also drops
// whatever owner / mode / hard link / symlink the previous file had.
func writeSecretFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(e error) error {
		f.Close()
		_ = os.Remove(tmp)
		return e
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	// Chmod (not the create mode) so the umask cannot change it.
	if err := f.Chmod(secretFileMode); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// hardenSecretFile tightens an existing secret file in place to 0600 (used
// when it cannot be rewritten). Symlinks are refused, never followed.
func hardenSecretFile(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Mode().Perm() == secretFileMode {
		return nil
	}
	return os.Chmod(path, secretFileMode)
}

// Windows only (MSI registry copy of the key, service command line).
func loadConfigFromRegistry() (serverURL, apiKey string) { return "", "" }
func protectAgentRegistrySecrets() error                 { return nil }
func scrubServiceArguments() (bool, error)               { return false, nil }
