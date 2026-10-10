//go:build linux

package main

// vault_linux.go — the password vault's platform side on Linux (its format,
// state machine and bindings are platform-neutral: vault.go, vault_codec.go,
// vault_bridge.go).
//
//   - protector (ssProtector): AES-256-GCM with a random 256-bit key kept in
//     the Secret Service (secretservice_linux.go). The code stays a check
//     inside the sealed content, exactly as with DPAPI on Windows: it is not
//     a key, so a short code is never something to brute-force offline.
//     The file keeps the Windows layout (vault_codec.go: "OBRVAULT" | u16
//     format 1 | protector blob); only the blob differs:
//
//       "OBRSS1"(6) | key-id(16) | nonce(12) | AES-256-GCM(key, inner)
//       additional data: "OBRSS1" | purpose | key-id
//
//     A Windows file cannot be opened here (its blob is DPAPI's), nor the
//     reverse: moving a vault between computers or systems goes through the
//     passphrase export (connexport.go: scrypt + AES-256-GCM), then the
//     import re-seals it locally;
//   - sealer (linuxMemSealer): AES-256-GCM with a per-process key held in an
//     mlock'ed page excluded from core dumps (MADV_DONTDUMP), no core file
//     (RLIMIT_CORE 0; see hardenProcess for why not PR_SET_DUMPABLE);
//   - vaultStore (fileVaultStore): $XDG_DATA_HOME/oblireach/vault (inside the
//     Flatpak: ~/.var/app/tools.obli.Oblireach/data/oblireach/vault), the
//     folder 0700, the files 0600, an atomic rename, vault.prev kept, and an
//     flock on vault.lock around every write (two processes never interleave);
//   - presenceFlag (filePresenceFlag): a marker sealed by the protector under
//     its own key (the fingerprint unlock switch, hello_linux.go).

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ── protector ──────────────────────────────────────────────────────────────

var ssBlobMagic = []byte("OBRSS1")

const ssNonceLen = 12

// ssKeys: what ssProtector needs of the key store (a fake in the tests).
type ssKeys interface {
	key(purpose string, keyID []byte, use func(key []byte) error) error
	current(purpose string, use func(keyID, key []byte) error) error
}

// ssProtector seals with the keyring key of its purpose.
type ssProtector struct {
	purpose string
	keys    ssKeys
}

func ssAAD(purpose string, keyID []byte) []byte {
	aad := append([]byte(nil), ssBlobMagic...)
	aad = append(aad, purpose...)
	aad = append(aad, 0)
	return append(aad, keyID...)
}

func (p ssProtector) Protect(plain []byte) ([]byte, error) {
	var out []byte
	err := p.keys.current(p.purpose, func(keyID, key []byte) error {
		block, err := aes.NewCipher(key)
		if err != nil {
			return err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		nonce := make([]byte, ssNonceLen)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		out = make([]byte, 0, len(ssBlobMagic)+len(keyID)+ssNonceLen+len(plain)+gcm.Overhead())
		out = append(out, ssBlobMagic...)
		out = append(out, keyID...)
		out = append(out, nonce...)
		out = gcm.Seal(out, nonce, plain, ssAAD(p.purpose, keyID))
		return nil
	})
	return out, err
}

// errSSBlob: not a blob of this protector (a Windows vault, a damaged file).
var errSSBlob = errors.New("not an Oblireach Secret Service blob")

func (p ssProtector) Unprotect(blob []byte, use func(plain []byte) error) error {
	hdr := len(ssBlobMagic) + ssKeyIDLen + ssNonceLen
	if len(blob) < hdr+16 || !bytes.Equal(blob[:len(ssBlobMagic)], ssBlobMagic) {
		return errSSBlob
	}
	keyID := blob[len(ssBlobMagic) : len(ssBlobMagic)+ssKeyIDLen]
	nonce := blob[len(ssBlobMagic)+ssKeyIDLen : hdr]
	return p.keys.key(p.purpose, keyID, func(key []byte) error {
		block, err := aes.NewCipher(key)
		if err != nil {
			return err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		plain, err := gcm.Open(nil, nonce, blob[hdr:], ssAAD(p.purpose, keyID))
		if err != nil {
			return errSSBlob
		}
		defer wipeBytesLocal(plain)
		return use(plain)
	})
}

// ── sealer ─────────────────────────────────────────────────────────────────

// linuxMemSealer: AES-256-GCM under the process key (memKey). Sealed form:
// nonce(12) | ciphertext+tag.
type linuxMemSealer struct{}

var (
	memKeyOnce sync.Once
	memKeyPage []byte // the mmap'ed page; the key is its first 32 bytes
	memKeyErr  error
	memAEAD    cipher.AEAD
)

// hardenProcess: no core file of this process (best effort). NOT
// prctl(PR_SET_DUMPABLE, 0): it makes /proc/<pid> unreadable by the user's
// other processes, and xdg-desktop-portal identifies its caller there
// (/proc/<pid>/root/.flatpak-info, the cgroup of a host app): every portal
// (file dialogs, OpenURI, notifications, Background) would then fail.
func hardenProcess() {
	_ = unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
}

func memKey() (cipher.AEAD, error) {
	memKeyOnce.Do(func() {
		hardenProcess()
		page, err := unix.Mmap(-1, 0, os.Getpagesize(), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
		if err != nil {
			memKeyErr = err
			return
		}
		_ = unix.Mlock(page)                         // (RLIMIT_MEMLOCK may refuse: still sealed)
		_ = unix.Madvise(page, unix.MADV_DONTDUMP)   // never in a core file
		_ = unix.Madvise(page, unix.MADV_WIPEONFORK) // never in a child
		if _, err := rand.Read(page[:32]); err != nil {
			memKeyErr = err
			return
		}
		block, err := aes.NewCipher(page[:32])
		if err != nil {
			memKeyErr = err
			return
		}
		memAEAD, memKeyErr = cipher.NewGCM(block)
		memKeyPage = page
	})
	return memAEAD, memKeyErr
}

func (linuxMemSealer) Seal(p []byte) ([]byte, error) {
	aead, err := memKey()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, p, nil), nil
}

func (linuxMemSealer) Open(sealed []byte, use func(p []byte) error) error {
	aead, err := memKey()
	if err != nil {
		return err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return errors.New("sealed: too short")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], nil)
	if err != nil {
		return errors.New("sealed: damaged")
	}
	defer wipeBytesLocal(plain)
	return use(plain)
}

var (
	_ protector = ssProtector{}
	_ sealer    = linuxMemSealer{}
)

// ── The vault file ─────────────────────────────────────────────────────────

const (
	vaultDirName  = "vault"
	vaultFileName = "vault.dat"
	vaultLockName = "vault.lock"
	vaultPrevName = "vault.prev"
	// The "unlock with a fingerprint" switch (vault.go presenceFlag).
	vaultHelloName = "vault.hello"
)

// vaultBaseDir: $XDG_DATA_HOME/oblireach/vault ("" when unknown).
func vaultBaseDir() string {
	local := platformPaths.localDataDir()
	if local == "" {
		return ""
	}
	return filepath.Join(local, vaultDirName)
}

// makePrivateDir: dir (and its parents) created 0700; an existing one is
// brought back to 0700 (it must be a real folder of this user, not a link).
func makePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if sys, ok := st.Sys().(*unix.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if st.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// writePrivateFile: data at path, 0600, through a temporary file renamed
// over it (fsync'ed first).
func writePrivateFile(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil { // the rename itself durable
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// fileVaultStore implements vaultStore on the real file system.
type fileVaultStore struct {
	mu   sync.Mutex
	dir  string
	path string
}

func newFileVaultStore() (*fileVaultStore, error) {
	dir := vaultBaseDir()
	if dir == "" {
		return nil, errors.New("no XDG data folder")
	}
	if err := makePrivateDir(dir); err != nil {
		return nil, err
	}
	return &fileVaultStore{dir: dir, path: filepath.Join(dir, vaultFileName)}, nil
}

func (s *fileVaultStore) Path() string { return s.path }

// locked runs f under an exclusive flock of vault.lock (another process of
// this user writing the vault waits).
func (s *fileVaultStore) locked(f func() error) error {
	lf, err := os.OpenFile(filepath.Join(s.dir, vaultLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lf.Fd()), unix.LOCK_UN)
	return f()
}

func (s *fileVaultStore) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// Save atomically replaces the vault file, the previous one kept as
// vault.prev (best effort).
func (s *fileVaultStore) Save(outer []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		if _, err := os.Stat(s.path); err == nil {
			prev := filepath.Join(s.dir, vaultPrevName)
			if old, err := os.ReadFile(s.path); err == nil {
				_ = writePrivateFile(prev, old)
			}
		}
		return writePrivateFile(s.path, outer)
	})
}

// Reset deletes the vault (setAside false) or renames it aside.
func (s *fileVaultStore) Reset(setAside bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		if _, err := os.Stat(s.path); err != nil {
			return nil
		}
		if !setAside {
			return os.Remove(s.path)
		}
		aside := filepath.Join(s.dir, fmt.Sprintf("vault-unreadable-%s.dat", time.Now().Format("20060102-150405")))
		return os.Rename(s.path, aside)
	})
}

// ── The fingerprint unlock switch ──────────────────────────────────────────

var vaultHelloMarker = []byte("oblireach-vault-hello-v1")

// filePresenceFlag: the marker sealed by the protector under the presence
// key: a file another user (or a copy from elsewhere) wrote is not a switch
// that is on.
type filePresenceFlag struct {
	path string
	prot protector
}

func (f filePresenceFlag) Enabled() bool {
	b, err := os.ReadFile(f.path)
	if err != nil || len(b) == 0 || len(b) > 4096 {
		return false
	}
	ok := false
	_ = f.prot.Unprotect(b, func(plain []byte) error {
		ok = subtle.ConstantTimeCompare(plain, vaultHelloMarker) == 1
		return nil
	})
	return ok
}

func (f filePresenceFlag) Set(on bool) error {
	if !on {
		if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	blob, err := f.prot.Protect(vaultHelloMarker)
	if err != nil {
		return err
	}
	return writePrivateFile(f.path, blob)
}

// ── App-wide vault instance ────────────────────────────────────────────────

var (
	appVault     *Vault
	appVaultInit sync.Once
	appVaultErr  error
	// testVaultOverride: a fresh Vault per test (never set outside tests).
	testVaultOverride *Vault
)

// getAppVault lazily creates the process-wide Vault.
func getAppVault() (*Vault, error) {
	if testVaultOverride != nil {
		return testVaultOverride, nil
	}
	appVaultInit.Do(func() {
		store, err := newFileVaultStore()
		if err != nil {
			appVaultErr = err
			return
		}
		appVault = NewVault(store, ssProtector{purpose: ssPurposeVault, keys: appKeyStore}, linuxMemSealer{})
		appVault.presence = filePresenceFlag{path: filepath.Join(store.dir, vaultHelloName),
			prot: ssProtector{purpose: ssPurposePresence, keys: appKeyStore}}
	})
	return appVault, appVaultErr
}
