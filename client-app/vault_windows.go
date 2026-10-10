//go:build windows

package main

// vault_windows.go — Windows-specific storage for the password vault:
// DPAPI (CryptProtectData) as the file's protector, CryptProtectMemory as
// the sealer that keeps decrypted secrets sealed in process memory between
// uses, and the vault file itself (private folder, atomic replace, a
// previous-version copy kept).
//
// No cgo: like the rest of this package, every Win32 call goes through
// syscall.NewLazyDLL/NewProc (see native_windows.go, localfs_windows.go).

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	modCrypt32 = syscall.NewLazyDLL("crypt32.dll")

	procCryptProtectData     = modCrypt32.NewProc("CryptProtectData")
	procCryptUnprotectData   = modCrypt32.NewProc("CryptUnprotectData")
	procCryptProtectMemory   = modCrypt32.NewProc("CryptProtectMemory")
	procCryptUnprotectMemory = modCrypt32.NewProc("CryptUnprotectMemory")
)

// dataBlob mirrors Win32's DATA_BLOB / CRYPTOAPI_BLOB.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	return unsafe.Slice(b.pbData, int(b.cbData))
}

const cryptprotectUIForbidden = 0x10

// vaultEntropy is fixed application entropy (not a secret on its own — DPAPI
// still binds to the Windows user account; this only stops another DPAPI
// consumer on the same account from decrypting the vault by accident).
var vaultEntropy = sha256.Sum256([]byte("Oblireach Desktop vault v1"))

// dpapiProtector implements protector via CryptProtectData/CryptUnprotectData,
// current-user scope (never CRYPTPROTECT_LOCAL_MACHINE).
type dpapiProtector struct{}

func (dpapiProtector) Protect(plain []byte) ([]byte, error) {
	in := newBlob(plain)
	entropy := newBlob(vaultEntropy[:])
	var out dataBlob
	r, _, e := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)),
		0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %v", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return append([]byte(nil), out.bytes()...), nil
}

func (dpapiProtector) Unprotect(blob []byte, use func(plain []byte) error) error {
	in := newBlob(blob)
	entropy := newBlob(vaultEntropy[:])
	var out dataBlob
	r, _, e := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)),
		0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return fmt.Errorf("CryptUnprotectData: %v", e)
	}
	plain := out.bytes()
	defer func() {
		wipeBytesLocal(plain)
		procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	}()
	return use(plain)
}

// cryptMemBlock is CRYPTPROTECTMEMORY_BLOCK_SIZE: CryptProtectMemory only
// accepts buffers that are a multiple of this size.
const cryptMemBlock = 16

// cryptMemSameProcess is CRYPTPROTECTMEMORY_SAME_PROCESS: the sealed bytes
// are only meaningful to this process, and not even across a restart.
const cryptMemSameProcess = 0

// dpapiMemSealer implements sealer via CryptProtectMemory/CryptUnprotectMemory.
// The sealed form is a 4-byte little-endian original length followed by the
// padded, encrypted block — Open trims back to the original length.
type dpapiMemSealer struct{}

func padTo16(plain []byte) []byte {
	n := len(plain)
	padded := n
	if r := padded % cryptMemBlock; r != 0 {
		padded += cryptMemBlock - r
	}
	if padded == 0 {
		padded = cryptMemBlock
	}
	out := make([]byte, padded)
	copy(out, plain)
	return out
}

func (dpapiMemSealer) Seal(p []byte) ([]byte, error) {
	buf := padTo16(p)
	r, _, e := procCryptProtectMemory.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), cryptMemSameProcess)
	if r == 0 {
		wipeBytesLocal(buf)
		return nil, fmt.Errorf("CryptProtectMemory: %v", e)
	}
	out := make([]byte, 4+len(buf))
	out[0] = byte(len(p))
	out[1] = byte(len(p) >> 8)
	out[2] = byte(len(p) >> 16)
	out[3] = byte(len(p) >> 24)
	copy(out[4:], buf)
	wipeBytesLocal(buf)
	return out, nil
}

func (dpapiMemSealer) Open(sealed []byte, use func(plain []byte) error) error {
	if len(sealed) < 4 {
		return errors.New("sealed: too short")
	}
	n := int(sealed[0]) | int(sealed[1])<<8 | int(sealed[2])<<16 | int(sealed[3])<<24
	buf := append([]byte(nil), sealed[4:]...)
	if len(buf) == 0 {
		// A sealed empty secret round-trips as n=0, buf empty: nothing to
		// unprotect, use gets an empty slice directly.
		return use(nil)
	}
	r, _, e := procCryptUnprotectMemory.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), cryptMemSameProcess)
	defer wipeBytesLocal(buf)
	if r == 0 {
		return fmt.Errorf("CryptUnprotectMemory: %v", e)
	}
	if n < 0 || n > len(buf) {
		return errors.New("sealed: bad length")
	}
	return use(buf[:n])
}

// ---------------------------------------------------------------------------
// The vault file itself
// ---------------------------------------------------------------------------

// vaultDirName / vaultFileName: %LOCALAPPDATA%\OblireachClient\vault\vault.dat.
// This sits under the same OblireachClient folder that localfs_windows.go
// already protects against both reading and writing from the Files page
// (protectR/protectW: "ownLocal"), and gets its own restrictive DACL besides
// (current user, SYSTEM, Administrators — makePrivateNamedDir).
const (
	vaultDirName  = "vault"
	vaultFileName = "vault.dat"
	vaultLockName = "vault.lock"
	vaultPrevName = "vault.prev"
	// The "unlock with Windows Hello" switch (vault.go presenceFlag): a
	// DPAPI-protected marker next to the vault file.
	vaultHelloName = "vault.hello"
)

func vaultBaseDir() string {
	local := platformPaths.localDataDir() // %LOCALAPPDATA%\OblireachClient (paths_windows.go)
	if local == "" {
		return ""
	}
	return filepath.Join(local, vaultDirName)
}

// makePrivateNamedDir creates dir (if absent) with a DACL granting only the
// current user, SYSTEM and Administrators full access, inherited by files
// created inside it — the same SDDL shape as updater_windows.go's
// makePrivateDir, but for a fixed, well-known path instead of a random temp
// name.
func makePrivateNamedDir(dir string) error {
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", dir)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	sddl, _ := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	var sd uintptr
	if r, _, e := procConvertStringSDToSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return fmt.Errorf("security descriptor: %v", e)
	}
	defer procLocalFree.Call(sd)
	sa := syscall.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	d16, _ := syscall.UTF16PtrFromString(dir)
	if err := syscall.CreateDirectory(d16, &sa); err != nil && err != syscall.ERROR_ALREADY_EXISTS {
		return err
	}
	return nil
}

// fileVaultStore implements vaultStore against the real filesystem, guarded
// by an in-process mutex (one Vault per running Desktop instance) — good
// enough for a single-user desktop app; cross-instance exclusivity is not
// attempted in this pass (two Desktop instances writing at once is already
// an edge case left for later).
type fileVaultStore struct {
	mu   sync.Mutex
	dir  string
	path string
}

func newFileVaultStore() (*fileVaultStore, error) {
	dir := vaultBaseDir()
	if dir == "" {
		return nil, errors.New("no LOCALAPPDATA")
	}
	if err := makePrivateNamedDir(dir); err != nil {
		return nil, err
	}
	return &fileVaultStore{dir: dir, path: filepath.Join(dir, vaultFileName)}, nil
}

func (s *fileVaultStore) Path() string { return s.path }

func (s *fileVaultStore) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// Save atomically replaces the vault file: write to a fresh temp name in the
// same (already private) directory, flush, then rename over the target. The
// previous version is kept as vault.prev (best-effort).
func (s *fileVaultStore) Save(outer []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := filepath.Join(s.dir, fmt.Sprintf("%s.tmp-%d", vaultFileName, time.Now().UnixNano()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(outer); err != nil {
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
	prev := filepath.Join(s.dir, vaultPrevName)
	if _, err := os.Stat(s.path); err == nil {
		_ = os.Remove(prev)
		_ = os.Rename(s.path, prev) // best-effort: losing vault.prev is not fatal
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Reset deletes the vault (setAside=false) or renames it aside with a
// timestamp (setAside=true, for an unreadable vault that might belong to a
// profile that comes back).
func (s *fileVaultStore) Reset(setAside bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); err != nil {
		return nil // already absent
	}
	if !setAside {
		return os.Remove(s.path)
	}
	aside := filepath.Join(s.dir, fmt.Sprintf("vault-unreadable-%s.dat", time.Now().Format("20060102-150405")))
	return os.Rename(s.path, aside)
}

// ---------------------------------------------------------------------------
// "Unlock with Windows Hello" switch (vault.go presenceFlag)
// ---------------------------------------------------------------------------

// vaultHelloEntropy / vaultHelloMarker: the marker is this constant sealed by
// DPAPI for the current Windows user (with its own entropy): a file another
// account (or anything that is not this user's DPAPI) wrote is not a switch
// that is on. It lives in the vault's private folder (its DACL: this user,
// SYSTEM, Administrators).
var (
	vaultHelloEntropy = sha256.Sum256([]byte("Oblireach Desktop vault hello v1"))
	vaultHelloMarker  = []byte("oblireach-vault-hello-v1")
)

type filePresenceFlag struct{ path string }

func (f filePresenceFlag) Enabled() bool {
	b, err := os.ReadFile(f.path)
	if err != nil || len(b) == 0 || len(b) > 4096 {
		return false
	}
	ok := false
	_ = dpapiUnprotectWith(b, vaultHelloEntropy[:], func(plain []byte) error {
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
	blob, err := dpapiProtectWith(vaultHelloMarker, vaultHelloEntropy[:])
	if err != nil {
		return err
	}
	tmp := f.path + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// dpapiProtectWith / dpapiUnprotectWith: CryptProtectData for the current
// user with the given entropy (dpapiProtector's, with its own entropy).
func dpapiProtectWith(plain, entropyBytes []byte) ([]byte, error) {
	in := newBlob(plain)
	entropy := newBlob(entropyBytes)
	var out dataBlob
	r, _, e := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)),
		0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %v", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return append([]byte(nil), out.bytes()...), nil
}

func dpapiUnprotectWith(blob, entropyBytes []byte, use func(plain []byte) error) error {
	in := newBlob(blob)
	entropy := newBlob(entropyBytes)
	var out dataBlob
	r, _, e := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, uintptr(unsafe.Pointer(&entropy)),
		0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return fmt.Errorf("CryptUnprotectData: %v", e)
	}
	plain := out.bytes()
	defer func() {
		wipeBytesLocal(plain)
		procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	}()
	return use(plain)
}

// ---------------------------------------------------------------------------
// App-wide vault instance
// ---------------------------------------------------------------------------

var (
	appVault     *Vault
	appVaultInit sync.Once
	appVaultErr  error
	// testVaultOverride lets vault_bridge_windows_test.go inject a fresh,
	// isolated Vault per test instead of the process-wide singleton (whose
	// sync.Once would otherwise leak state across tests). Never set outside
	// tests.
	testVaultOverride *Vault
)

// getAppVault lazily creates the process-wide Vault against the real DPAPI
// store. Bindings call this instead of constructing a Vault themselves.
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
		appVault = NewVault(store, dpapiProtector{}, dpapiMemSealer{})
		appVault.presence = filePresenceFlag{path: filepath.Join(store.dir, vaultHelloName)}
	})
	return appVault, appVaultErr
}
