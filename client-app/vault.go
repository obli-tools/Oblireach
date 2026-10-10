package main

// vault.go — the ObliReach Desktop local password vault ("Coffre"), portable
// part: on-disk codec, the lock/unlock state machine and the vault's public
// API. Platform storage (DPAPI, CryptProtectMemory, secbuf, the vault file
// itself) lives in vault_windows.go; this file only knows the protector and
// sealer interfaces, so it is fully unit-tested with fakes on any OS.
//
// Design: password vault. The vault code unlocks it; Windows Hello (fingerprint,
// face, Windows Hello PIN) may too, when the operator turned it on
// (UnlockWithPresence, hello_windows.go), the code staying the fallback.
//
// Windows Hello model. The vault file is sealed by DPAPI (current
// Windows user) in both cases; the code has never been a key, only a gate
// checked against the PBKDF2 hash INSIDE that sealed file. Hello is the same
// kind of gate, checked by Windows instead of by us:
//   - it is opt-in (SetPresence: only from an unlocked vault, i.e. after the
//     code or Hello itself, and the bridge asks Hello once more when turning
//     it on). The switch is a DPAPI-protected marker next to the vault file
//     (presenceFlag); creating or resetting the vault clears it;
//   - the file is only unsealed AFTER Windows answers "Verified"
//     (UnlockWithPresence: preconditions, then the prompt with the vault not
//     held, then the same decrypt as Unlock, skipping only the hash check);
//   - nothing else changes: auto-lock, Lock, Reveal (which still asks for
//     the code), the backoff of wrong codes (Hello has its own lockout).
// So it is not weaker than the code against what the code protects from (a
// person at an unlocked session who does not know the code: they would need
// this user's finger, face or Hello PIN, anti-hammered by Windows), and, like
// the code, it does nothing against code already running as this user (who
// can call DPAPI directly: outside this design's threat model, as before).
//
// Threat model, storage format and locking rules: as described above.
// Nothing here ever logs, and no exported function returns a secret except
// Reveal and BuildTypeText, both of which require a fresh unlock check.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Storage interfaces (fakes back these in tests; DPAPI/CryptProtectMemory
// implement them on Windows in vault_windows.go).
// ---------------------------------------------------------------------------

// protector seals/opens the whole vault file at rest (DPAPI on Windows).
type protector interface {
	Protect(plain []byte) ([]byte, error)
	// Unprotect decrypts blob and calls use with the plaintext; the
	// implementation wipes and frees the plaintext right after use returns.
	Unprotect(blob []byte, use func(plain []byte) error) error
}

// sealer keeps a secret sealed in process memory between uses
// (CryptProtectMemory on Windows).
type sealer interface {
	Seal(p []byte) (sealed []byte, err error)
	Open(s []byte, use func(p []byte) error) error
}

// vaultStore reads/writes the vault file on disk (atomic replace, exclusive
// lock, restricted DACL — vault_windows.go on Windows).
type vaultStore interface {
	// Load returns the raw outer bytes, or (nil, nil) if the file is absent.
	Load() ([]byte, error)
	// Save atomically replaces the file, keeping the previous version.
	Save(outer []byte) error
	// Reset removes the vault (delete) or renames it aside (setAside).
	Reset(setAside bool) error
	Path() string
}

// ---------------------------------------------------------------------------
// Entry / in-memory model
// ---------------------------------------------------------------------------

// VaultEntry is one saved credential. Secret is sealed (never plaintext) once
// loaded into memory; it is only opened for BuildTypeText or Reveal.
type VaultEntry struct {
	ID         [16]byte
	Created    time.Time
	Updated    time.Time
	LastUsed   time.Time // zero = never used
	SecureOnly bool
	Origin     string // server origin (scheme://host[:port])
	TenantID   uint32
	TenantName string
	Label      string
	Username   string
	Note       string
	secret     []byte // sealed bytes; nil while the vault is locked

	// v2 fields. Kind vaultKindPassword (zero value) keeps Origin/TenantID
	// scoping exactly as before; ParentID/Host/Port/AuthMethod are unused for
	// that kind. Kinds vaultKindFolder/SSH/RDP live in the local connection
	// tree (ParentID = "" hex means root) and ignore Origin/TenantID.
	Kind       byte
	ParentID   [16]byte
	Host       string
	Port       uint16
	AuthMethod byte

	// v3: optional device restriction (password entries only). Empty = the
	// whole (Origin, TenantID) scope, exactly as before v3. Non-empty = the
	// entry is offered (and typed) ONLY in a session on one of these devices
	// (each carries its own server origin and tenant: the picker may tick
	// machines of several servers / tenants).
	Devices []VaultDevice

	// v4. CredID: an ssh/rdp connection's reusable credential (kind
	// vaultKindCredential; zero = its own username + secret, as before).
	// passphrase: an SSH private key's passphrase (sealed like secret; nil
	// = none). hasSecret / hasPass: whether a non-empty one is stored (said
	// to the page as a yes/no, never the value).
	CredID     [16]byte
	passphrase []byte
	hasSecret  bool
	hasPass    bool
}

// VaultDevice identifies one machine a password entry is restricted to:
// its server (origin, as the proxy records a session's), its tenant, its
// stable identity (the device uuid, kept across a re-enrolment) and its
// numeric id on that server (what the server's session answer carries
// today), plus a display name (never used for matching).
//
// v5: VMID != "" binds the item to one Hyper-V VM of that host (its GUID,
// lower case, as the server's session answer carries it for a VM console:
// proxy.go captureSessionResponse); Origin / TenantID / UUID / ID are then
// its HOST's (what the session of a VM console is on). VMName: a display
// name only, like Name (the host's). Such an item matches a VM console of
// that VM only, never a session on the host itself; a plain item (VMID "")
// never matches a VM console (sessionMatch).
type VaultDevice struct {
	Origin   string
	TenantID uint32
	UUID     string
	ID       int64
	Name     string
	VMID     string
	VMName   string
}

// VaultSession is the server-resolved scope of a live session (reach_tunnels.go:
// captured from the server's own answer, never from the page).
type VaultSession struct {
	Origin     string
	TenantID   uint32
	DeviceID   int64
	DeviceUUID string // "" while the server does not send it
	// VM: a Hyper-V VM console (protocol 'vmconsole'; the device fields are
	// its HOST's). VMID: that VM's GUID (lower case) from the server's answer,
	// "" when it did not carry a valid one (then no device-restricted entry
	// matches: fail closed).
	VM   bool
	VMID string
}

// VaultEntryMeta is what a caller outside this file is handed: everything
// except the secret. It is the ONLY shape the WebView bindings ever return
// for a list (vault_bridge_windows_test.go asserts this by reflection).
type VaultEntryMeta struct {
	ID         string
	Created    time.Time
	Updated    time.Time
	LastUsed   time.Time
	SecureOnly bool
	Origin     string
	TenantID   uint32
	TenantName string
	Label      string
	Username   string
	Note       string

	// v2 fields — see VaultEntry. Zero-valued (Kind=vaultKindPassword) for
	// every entry saved before this pass.
	Kind       byte
	ParentID   string // hex, "" = root
	Host       string
	Port       uint16
	AuthMethod byte

	// v3: the device restriction (a copy; empty = none).
	Devices []VaultDevice

	// v4: the credential a connection uses ("" = its own), whether a
	// secret / a key passphrase is stored (yes / no only).
	CredID       string
	Stored       bool
	KeyProtected bool
}

func (e *VaultEntry) meta() VaultEntryMeta {
	m := VaultEntryMeta{
		ID: idHex(e.ID), Created: e.Created, Updated: e.Updated, LastUsed: e.LastUsed,
		SecureOnly: e.SecureOnly, Origin: e.Origin, TenantID: e.TenantID, TenantName: e.TenantName,
		Label: e.Label, Username: e.Username, Note: e.Note,
		Kind: e.Kind, ParentID: parentIDToHex(e.ParentID), Host: e.Host, Port: e.Port, AuthMethod: e.AuthMethod,
		Devices: append([]VaultDevice(nil), e.Devices...),
		Stored:  e.hasSecret, KeyProtected: e.hasPass,
	}
	if e.CredID != zeroVaultID {
		m.CredID = idHex(e.CredID)
	}
	return m
}

var zeroVaultID [16]byte

// parentIDToHex: "" for the root (zero ID), else the usual 32-hex-char form.
func parentIDToHex(id [16]byte) string {
	if id == zeroVaultID {
		return ""
	}
	return idHex(id)
}

// parentIDFromHex: the inverse of parentIDToHex. ok is false for a malformed
// (non-empty, non-32-hex-char) value — the caller must treat that as a
// validation error, never silently fall back to root.
func parentIDFromHex(s string) (id [16]byte, ok bool) {
	if s == "" {
		return zeroVaultID, true
	}
	if len(s) != 32 {
		return id, false
	}
	for i := 0; i < 16; i++ {
		hi, ok1 := hexNibble(s[i*2])
		lo, ok2 := hexNibble(s[i*2+1])
		if !ok1 || !ok2 {
			return id, false
		}
		id[i] = hi<<4 | lo
	}
	return id, true
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func idHex(id [16]byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 32)
	for i, b := range id {
		out[i*2] = hexd[b>>4]
		out[i*2+1] = hexd[b&0xf]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

type vaultState int

const (
	vaultAbsent vaultState = iota
	vaultLocked
	vaultUnlocked
	vaultUnreadable
)

const (
	vaultMaxEntries       = 2000
	vaultMaxOrigin        = 200
	vaultMaxTenantName    = 120
	vaultMaxLabelRunes    = 80
	vaultMaxUsernameRunes = 128
	vaultMaxNoteRunes     = 500
	vaultMaxSecretRunes   = 128
	// vaultMaxKeyRunes: an SSH private key (kind=vaultKindSSH,
	// authMethod=vaultAuthKey) is not a typed-in password — a PEM-encoded
	// RSA 4096 key is already ~3,300 runes — so it needs a much larger
	// ceiling than vaultMaxSecretRunes, which stays tight for everything
	// else (passwords, RDP secrets).
	vaultMaxKeyRunes  = 16384
	vaultMaxHostRunes = 255
	vaultMinCodeLen   = 6

	// v3 device restriction bounds.
	vaultMaxEntryDevices   = 500
	vaultMaxDeviceUUID     = 64
	vaultMaxDeviceNameRune = 255
	// v5: a VM's display name (Hyper-V allows 100 characters).
	vaultMaxVMNameRunes = 255

	// v4: an SSH private key's passphrase.
	vaultMaxPassphraseRunes = 256

	// vaultPBKDF2Iterations is 600,000 in production; tests lower it via
	// withPBKDF2Iterations to keep the suite fast without changing the
	// algorithm under test.
	vaultPBKDF2Iterations = 600_000
)

// Vault is the single in-process instance guarding one vault.dat. All methods
// are safe for concurrent use.
type Vault struct {
	mu         sync.Mutex
	store      vaultStore
	prot       protector
	seal       sealer
	iterations int // PBKDF2 iterations; vaultPBKDF2Iterations unless a test overrides it

	state vaultState
	// held: kept locked while someone else drives this computer ("Receive
	// help", receive_help.go): no unlock, no new vault, no reset.
	held bool

	revision   uint32
	autoLockMi uint16
	codeSalt   []byte
	codeIter   uint32
	codeHash   []byte
	entries    []*VaultEntry

	failCount int
	failUntil time.Time
	nowFn     func() time.Time

	autoLockTimer *time.Timer
	autoLockDur   time.Duration
	onLock        func() // best-effort UI notification hook

	// presence: the "unlock with Windows Hello" switch (nil: not offered).
	presence presenceFlag
}

// presenceFlag: whether a user-presence check (Windows Hello) may unlock the
// vault instead of the code (vault_windows.go: a DPAPI-protected marker
// next to the vault file; a fake in tests).
type presenceFlag interface {
	Enabled() bool
	Set(on bool) error
}

// NewVault constructs a vault bound to store/prot/seal, loading its current
// state from disk (Load never blocks on a wrong code: only unlock does).
func NewVault(store vaultStore, prot protector, seal sealer) *Vault {
	v := &Vault{
		store: store, prot: prot, seal: seal,
		iterations: vaultPBKDF2Iterations,
		nowFn:      time.Now,
	}
	v.reload()
	return v
}

func (v *Vault) now() time.Time { return v.nowFn() }

// reload reads the on-disk state without decrypting the DPAPI layer (that
// only happens on Unlock). It just checks whether a file exists at all.
func (v *Vault) reload() {
	outer, err := v.store.Load()
	if err != nil {
		v.state = vaultUnreadable
		return
	}
	if outer == nil {
		v.state = vaultAbsent
		return
	}
	if _, _, ok := parseOuter(outer); !ok {
		v.state = vaultUnreadable
		return
	}
	v.state = vaultLocked
}

// State reports the current state and a few flags the UI needs without
// unlocking (count is -1 while locked/absent/unreadable).
type VaultStateInfo struct {
	State       string // "absent" | "locked" | "unlocked" | "unreadable"
	AutoLockMin int
	Count       int
}

func (v *Vault) State() VaultStateInfo {
	v.mu.Lock()
	defer v.mu.Unlock()
	info := VaultStateInfo{Count: -1}
	switch v.state {
	case vaultAbsent:
		info.State = "absent"
	case vaultLocked:
		info.State = "locked"
	case vaultUnreadable:
		info.State = "unreadable"
	case vaultUnlocked:
		info.State = "unlocked"
		info.AutoLockMin = int(v.autoLockMi)
		info.Count = len(v.entries)
	}
	return info
}

// Create makes a brand new vault with the given code (>= vaultMinCodeLen
// runes) and an empty entry list, then leaves it unlocked. Only valid from
// the "absent" state.
func (v *Vault) Create(code string, autoLockMin int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.held {
		return errVaultHeld
	}
	if v.state != vaultAbsent {
		return errors.New("vault already exists")
	}
	if utf8.RuneCountInString(code) < vaultMinCodeLen {
		return errors.New("code too short")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	v.clearPresenceLocked() // (a marker left by a vault deleted by hand)
	v.codeSalt = salt
	v.codeIter = uint32(v.iterations)
	v.codeHash = pbkdf2SHA256([]byte(code), salt, v.iterations, 32)
	v.autoLockMi = uint16(autoLockMin)
	v.entries = nil
	v.revision = 1
	v.state = vaultUnlocked
	v.resetAutoLockLocked()
	err := v.persistLocked()
	if errors.Is(err, errKeyringUnavailable) {
		// Nothing was written (no keyring to seal it): still no vault.
		v.state = vaultAbsent
		v.codeHash, v.codeSalt = nil, nil
		if v.autoLockTimer != nil {
			v.autoLockTimer.Stop()
		}
		return errKeyringUnavailable
	}
	return err
}

// Unlock checks code against the stored hash (constant time, PBKDF2-slowed)
// and, on success, decrypts the vault file into memory. Backoff of wrong
// codes: 30s / 5min / 15min after 5 / 10 / 15 failures.
func (v *Vault) Unlock(code string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.held {
		return errVaultHeld
	}
	if v.state == vaultUnlocked {
		return nil
	}
	if v.state != vaultLocked {
		return errVaultState(v.state)
	}
	if wait := v.waitLocked(); wait > 0 {
		return &vaultWaitError{Seconds: int(wait / time.Second)}
	}
	return v.openLocked(func(parsed *vaultPlain) error {
		hash := pbkdf2SHA256([]byte(code), parsed.codeSalt, int(parsed.codeIter), 32)
		if subtle.ConstantTimeCompare(hash, parsed.codeHash) != 1 {
			v.registerFailureLocked()
			return errWrongCode
		}
		v.failCount = 0
		return nil
	})
}

// openLocked unseals the vault file (DPAPI) into memory, once check (nil:
// none, the presence was verified by the caller) accepts it. v.mu held,
// state locked.
func (v *Vault) openLocked(check func(parsed *vaultPlain) error) error {
	outer, err := v.store.Load()
	if err != nil || outer == nil {
		v.state = vaultUnreadable
		return errors.New("unreadable")
	}
	format, blob, ok := parseOuter(outer)
	if !ok || format != 1 {
		v.state = vaultUnreadable
		return errors.New("unreadable")
	}

	var parsed *vaultPlain
	uerr := v.prot.Unprotect(blob, func(plain []byte) error {
		p, ok := decodeVaultPlain(plain)
		if !ok {
			return errors.New("corrupt")
		}
		parsed = p
		return nil
	})
	if errors.Is(uerr, errKeyringUnavailable) {
		return errKeyringUnavailable // (state unchanged: locked)
	}
	if uerr != nil || parsed == nil {
		v.state = vaultUnreadable
		return errors.New("unreadable")
	}

	if check != nil {
		if err := check(parsed); err != nil {
			for i := range parsed.entries {
				wipeBytesLocal(parsed.entries[i].secret)
				wipeBytesLocal(parsed.entries[i].passphrase)
			}
			return err
		}
	}

	v.revision = parsed.revision
	v.autoLockMi = parsed.autoLockMin
	v.codeSalt = parsed.codeSalt
	v.codeIter = parsed.codeIter
	v.codeHash = parsed.codeHash
	v.entries = make([]*VaultEntry, 0, len(parsed.entries))
	for _, pe := range parsed.entries {
		e := &VaultEntry{
			ID: pe.id, Created: pe.created, Updated: pe.updated, LastUsed: pe.lastUsed,
			SecureOnly: pe.secureOnly, Origin: pe.origin, TenantID: pe.tenantID, TenantName: pe.tenantName,
			Label: pe.label, Username: pe.username, Note: pe.note,
			Kind: pe.kind, ParentID: pe.parentID, Host: pe.host, Port: pe.port, AuthMethod: pe.authMethod,
			Devices: pe.devices, CredID: pe.credID,
			hasSecret: len(pe.secret) > 0, hasPass: len(pe.passphrase) > 0,
		}
		if sealed, err := v.seal.Seal(pe.secret); err == nil {
			e.secret = sealed
		}
		wipeBytesLocal(pe.secret)
		if len(pe.passphrase) > 0 {
			if sealed, err := v.seal.Seal(pe.passphrase); err == nil {
				e.passphrase = sealed
			}
			wipeBytesLocal(pe.passphrase)
		}
		v.entries = append(v.entries, e)
	}
	v.state = vaultUnlocked
	v.resetAutoLockLocked()
	return nil
}

// UnlockWithPresence unlocks the vault after a user-presence check (Windows
// Hello: verify shows its prompt and answers nil only for "Verified"),
// when the operator turned it on (presenceFlag). The vault is not held while
// the prompt is up (State, Lock... go on), and everything is checked again
// once it answered. The file is unsealed only after verify said yes.
func (v *Vault) UnlockWithPresence(verify func() error) error {
	v.mu.Lock()
	if err := v.presenceReadyLocked(); err != nil || v.state == vaultUnlocked {
		v.mu.Unlock()
		return err
	}
	v.mu.Unlock()
	if err := verify(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.presenceReadyLocked(); err != nil || v.state == vaultUnlocked {
		return err
	}
	return v.openLocked(nil)
}

// presenceReadyLocked: nil when a presence check may unlock now (or the vault
// is already unlocked).
func (v *Vault) presenceReadyLocked() error {
	if v.held {
		return errVaultHeld
	}
	if v.state == vaultUnlocked {
		return nil
	}
	if v.state != vaultLocked {
		return errVaultState(v.state)
	}
	if v.presence == nil || !v.presence.Enabled() {
		return errPresenceOff
	}
	return nil
}

// PresenceEnabled: Windows Hello may unlock this vault (it exists, and the
// operator turned it on).
func (v *Vault) PresenceEnabled() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.presence == nil || (v.state != vaultLocked && v.state != vaultUnlocked) {
		return false
	}
	return v.presence.Enabled()
}

// SetPresence turns "unlock with Windows Hello" on or off: from an unlocked
// vault only (the code, or Hello, was just proven). The bridge asks Hello
// once more before turning it on.
func (v *Vault) SetPresence(on bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return errVaultLocked
	}
	if v.presence == nil {
		return errPresenceOff
	}
	return v.presence.Set(on)
}

// clearPresenceLocked: a new or reset vault never inherits the switch.
func (v *Vault) clearPresenceLocked() {
	if v.presence != nil && v.presence.Enabled() {
		_ = v.presence.Set(false)
	}
}

// VerifyCode checks code against the unlocked vault's stored hash, without
// changing any state (no backoff bookkeeping — Unlock already owns that).
// Used by the binding layer to require a fresh code entry for an especially
// sensitive action (Reveal) beyond "the vault happens to be unlocked".
func (v *Vault) VerifyCode(code string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return false, errVaultLocked
	}
	hash := pbkdf2SHA256([]byte(code), v.codeSalt, int(v.codeIter), 32)
	return subtle.ConstantTimeCompare(hash, v.codeHash) == 1, nil
}

// SetHeld locks the vault and keeps it locked (on) until released (off):
// while held, Unlock, UnlockWithPresence, Create and Reset are refused
// (errVaultHeld). "Receive help" holds it while an operator drives this
// computer.
func (v *Vault) SetHeld(on bool) {
	v.mu.Lock()
	v.held = on
	v.mu.Unlock()
	if on {
		v.Lock()
	}
}

// Held reports SetHeld.
func (v *Vault) Held() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.held
}

// Lock discards every decrypted secret from memory. Safe to call any time.
func (v *Vault) Lock() {
	v.mu.Lock()
	for _, e := range v.entries {
		wipeBytesLocal(e.secret)
		e.secret = nil
	}
	v.entries = nil
	if v.state == vaultUnlocked {
		v.state = vaultLocked
	}
	if v.autoLockTimer != nil {
		v.autoLockTimer.Stop()
	}
	hook := v.onLock
	v.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (v *Vault) resetAutoLockLocked() {
	if v.autoLockTimer != nil {
		v.autoLockTimer.Stop()
	}
	if v.autoLockMi == 0 {
		return
	}
	d := time.Duration(v.autoLockMi) * time.Minute
	v.autoLockTimer = time.AfterFunc(d, v.Lock)
}

// touch resets the auto-lock timer; called by every operation that proves
// the vault is in active use.
func (v *Vault) touch() {
	if v.state == vaultUnlocked {
		v.resetAutoLockLocked()
	}
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// scopeMatch reports whether a password entry belongs to (origin, tenantID).
// Local connections (folder/ssh/rdp) are never in scope for this check — they
// have no origin/tenant, only a place in the local ByParent tree.
//
// An entry's place: one tenant of one server (origin +
// tenant, as before); every tenant of one server (origin, tenant 0); every
// server (no origin, tenant 0) — the vault page's "General" section. Fail
// closed: a session whose server or tenant is not known matches nothing.
func (e *VaultEntry) scopeMatch(origin string, tenantID uint32) bool {
	if e.Kind != vaultKindPassword || origin == "" || tenantID == 0 {
		return false
	}
	if e.TenantID == 0 {
		return e.Origin == "" || e.Origin == origin
	}
	return e.Origin != "" && e.Origin == origin && e.TenantID == tenantID
}

// placeHolds: device d lies within the place (origin, tenantID) — see
// scopeMatch for the three kinds of place.
func placeHolds(origin string, tenantID uint32, d VaultDevice) bool {
	if origin == "" {
		return tenantID == 0
	}
	return d.Origin == origin && (tenantID == 0 || d.TenantID == tenantID)
}

// sessionMatch reports whether a password entry may be offered / typed in the
// live session s. No device restriction: the (origin, tenant) scope, as
// before v3 (a VM console included: its host's server and tenant). A
// restriction: s must be on one of its devices (same server origin, same
// tenant, same device) — the entry's own origin/tenant then play no part.
// A VM console (s.VM) matches only an item bound to that very VM (v5: same
// VM GUID, same server origin and tenant — whatever its host, so the item
// follows the VM through a live migration); a plain device item never matches a VM console
// of that device, and a VM-bound item never matches anything but its VM's
// console. Fail closed: a session whose device (or VM) is not known matches
// no restricted entry.
func (e *VaultEntry) sessionMatch(s VaultSession) bool {
	if e.Kind != vaultKindPassword {
		return false
	}
	if len(e.Devices) == 0 {
		return e.scopeMatch(s.Origin, s.TenantID)
	}
	if s.Origin == "" || s.TenantID == 0 {
		return false
	}
	if s.VM && s.VMID == "" {
		return false
	}
	for _, d := range e.Devices {
		if d.Origin != s.Origin || d.TenantID != s.TenantID || !vaultVMIs(d, s) {
			continue
		}
		// A VM keeps its GUID when it moves to another host: the host the
		// item was bound on plays no part.
		if s.VM || vaultDeviceIs(d, s) {
			return true
		}
	}
	return false
}

// vaultVMIs: the item d is for the kind of session s is — a plain device
// item for a session on the device itself, a VM-bound item for that VM's
// console only.
func vaultVMIs(d VaultDevice, s VaultSession) bool {
	if !s.VM {
		return d.VMID == ""
	}
	return d.VMID != "" && s.VMID != "" && d.VMID == s.VMID
}

// vaultDeviceIs: the restriction's device d is the session's device. By uuid
// when both sides know it (stable across a re-enrolment that changes the
// numeric id), else by the numeric id the server's session answer carries.
func vaultDeviceIs(d VaultDevice, s VaultSession) bool {
	if d.UUID != "" && s.DeviceUUID != "" {
		return strings.EqualFold(d.UUID, s.DeviceUUID)
	}
	return d.ID > 0 && d.ID == s.DeviceID
}

// EntriesForScope returns the metadata of the unrestricted entries visible
// for (origin, tenantID) — a session whose device is unknown.
// EntriesForSession is the session path.
func (v *Vault) EntriesForScope(origin string, tenantID uint32) []VaultEntryMeta {
	return v.EntriesForSession(VaultSession{Origin: origin, TenantID: tenantID})
}

// EntriesForSession returns the metadata of the entries offered in the live
// session s (sessionMatch), sorted by label. Empty and non-nil when locked or
// nothing matches.
func (v *Vault) EntriesForSession(s VaultSession) []VaultEntryMeta {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := []VaultEntryMeta{}
	if v.state != vaultUnlocked {
		return out
	}
	for _, e := range v.entries {
		if e.sessionMatch(s) {
			out = append(out, e.meta())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// List returns every password entry's metadata (main window only at the
// binding layer, existing password-manager UI); empty when locked. Local
// connections (folders/ssh/rdp) are not password-scoped and are listed
// separately by ByParent, so this shape — and every existing caller — is
// unchanged by the v2 format.
func (v *Vault) List() []VaultEntryMeta {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := []VaultEntryMeta{}
	if v.state != vaultUnlocked {
		return out
	}
	for _, e := range v.entries {
		if e.Kind == vaultKindPassword {
			out = append(out, e.meta())
		}
	}
	return out
}

// ByParent returns the direct children (folders and ssh/rdp connections) of
// the local connection tree node parentIDHex ("" = root), most recently
// updated first. Empty and non-nil when locked. This is the read side of the
// standalone RDP/SSH connection base (folders + connections), independent of
// any Obliance server/tenant scope.
func (v *Vault) ByParent(parentIDHex string) []VaultEntryMeta {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := []VaultEntryMeta{}
	if v.state != vaultUnlocked {
		return out
	}
	parentID, ok := parentIDFromHex(parentIDHex)
	if !ok {
		return out
	}
	for _, e := range v.entries {
		if (e.Kind == vaultKindFolder || e.Kind == vaultKindSSH || e.Kind == vaultKindRDP) && e.ParentID == parentID {
			out = append(out, v.connMetaLocked(e))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == vaultKindFolder // folders first
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func (v *Vault) findLocked(idHexStr string) (*VaultEntry, int) {
	for i, e := range v.entries {
		if idHex(e.ID) == idHexStr {
			return e, i
		}
	}
	return nil, -1
}

// Get returns one local connection tree node's metadata (never a secret —
// see Reveal for that) by id: the local SSH/RDP transport (ssh_session.go)
// needs host/port/username/authMethod before it can dial, the same fields
// ByParent already exposes per-row but with no single-id lookup until now.
func (v *Vault) Get(idHexStr string) (VaultEntryMeta, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return VaultEntryMeta{}, errVaultLocked
	}
	e, _ := v.findLocked(idHexStr)
	if e == nil {
		return VaultEntryMeta{}, errNotFound
	}
	return v.connMetaLocked(e), nil
}

// BuildTypeText opens entry id's secret (and its username, when withUser) and
// hands it to build, which must return a wiped []byte to caller (a caller in
// this codebase turns it into the type_text wire frame and wipes it again
// once sent). Bumps LastUsed on success. Refuses (scope_mismatch) when the
// entry is not in (origin, tenantID) — a caller must always pass the SERVER-
// RESOLVED scope, never the page's. A device-restricted entry is refused the
// same way outside its devices (sessionMatch).
func (v *Vault) BuildTypeText(idHexStr string, sess VaultSession, withUser bool,
	build func(username string, secret []byte) ([]byte, error)) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return nil, errVaultLocked
	}
	e, _ := v.findLocked(idHexStr)
	if e == nil {
		return nil, errNotFound
	}
	if !e.sessionMatch(sess) {
		return nil, errScopeMismatch
	}
	var out []byte
	var buildErr error
	err := v.seal.Open(e.secret, func(secret []byte) error {
		out, buildErr = build(e.Username, secret)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if buildErr != nil {
		return nil, buildErr
	}
	e.LastUsed = v.now()
	v.touch()
	_ = v.persistLocked() // best-effort: lastUsed is not worth failing the type over
	_ = withUser
	return out, nil
}

// Reveal opens entry id's secret as a UTF-8 string for the "Show" action. The
// caller (binding layer) must require a fresh code check immediately before
// calling this — Reveal itself does not re-check the code, so it can be
// reused for entries the caller has already gated.
func (v *Vault) Reveal(idHexStr string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return "", errVaultLocked
	}
	e, _ := v.findLocked(idHexStr)
	if e == nil {
		return "", errNotFound
	}
	var out string
	err := v.seal.Open(e.secret, func(secret []byte) error {
		out = string(secret)
		return nil
	})
	v.touch()
	return out, err
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// SaveInput is the validated shape of an add/edit form submission. Secret
// empty on an edit means "keep the existing one".
type SaveInput struct {
	ID         string // "" = create
	Origin     string
	TenantID   uint32
	TenantName string
	Label      string
	Username   string
	Note       string
	Secret     string // plaintext; wiped by Save once sealed
	SecureOnly bool

	// v2: local connection tree (folder/ssh/rdp). Kind "" or omitted defaults
	// to vaultKindPassword for backward compatibility with every existing
	// caller of Save built before this pass.
	Kind       byte
	ParentID   string // hex, "" = root; must name an existing folder entry otherwise
	Host       string
	Port       uint16
	AuthMethod byte

	// v3: the device restriction (password entries only; empty = none).
	Devices []VaultDevice

	// v4. CredID (an ssh / rdp connection): the reusable credential it uses
	// ("" = its own username + secret). Passphrase: an SSH private key's
	// (a key connection or a key credential; "" on an edit keeps the stored
	// one, ClearPassphrase removes it). Wiped by Save once sealed.
	CredID          string
	Passphrase      string
	ClearPassphrase bool
}

// validDeviceUUID: 0..64 of [A-Za-z0-9_-] (a uuid, or what a server uses as one).
func validDeviceUUID(s string) bool {
	if len(s) > vaultMaxDeviceUUID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// normVMID: a Hyper-V VM's GUID (8-4-4-4-12 hex digits) in lower case, or ok
// false. "" stays "" (no VM: a plain device item).
func normVMID(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	if len(s) != 36 {
		return "", false
	}
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return string(b), true
}

// validateDevices checks a device restriction and returns it deduplicated
// (the same server + tenant + identity [+ VM] once), in the order given.
func validateDevices(in []VaultDevice) ([]VaultDevice, error) {
	if len(in) > vaultMaxEntryDevices {
		return nil, errTooLong
	}
	var out []VaultDevice
	seen := map[string]bool{}
	for _, d := range in {
		if d.Origin == "" || len(d.Origin) > vaultMaxOrigin || hasControlRunes(d.Origin) {
			return nil, errBadChars
		}
		if d.TenantID == 0 || d.ID < 0 || !validDeviceUUID(d.UUID) || (d.UUID == "" && d.ID == 0) {
			return nil, errBadChars
		}
		if utf8.RuneCountInString(d.Name) > vaultMaxDeviceNameRune || hasControlRunes(d.Name) {
			return nil, errBadChars
		}
		vmID, ok := normVMID(d.VMID)
		if !ok {
			return nil, errBadChars
		}
		d.VMID = vmID
		if vmID == "" {
			d.VMName = ""
		} else if utf8.RuneCountInString(d.VMName) > vaultMaxVMNameRunes || hasControlRunes(d.VMName) {
			return nil, errBadChars
		}
		key := d.Origin + "|" + strconv.FormatUint(uint64(d.TenantID), 10) + "|" + strings.ToLower(d.UUID) + "|" + strconv.FormatInt(d.ID, 10) + "|" + vmID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out, nil
}

func validateSaveInput(in *SaveInput) error {
	if utf8.RuneCountInString(in.Label) == 0 {
		return errEmptyLabel
	}
	if utf8.RuneCountInString(in.Label) > vaultMaxLabelRunes {
		return errTooLong
	}
	if utf8.RuneCountInString(in.Username) > vaultMaxUsernameRunes {
		return errTooLong
	}
	if utf8.RuneCountInString(in.Note) > vaultMaxNoteRunes {
		return errTooLong
	}
	if len(in.Origin) > vaultMaxOrigin || len(in.TenantName) > vaultMaxTenantName {
		return errTooLong
	}
	// An SSH private key (PEM) gets a much larger ceiling than an ordinary
	// secret, and its internal newlines are not "control characters" the
	// way one would be in a hand-typed password or label — see
	// vaultMaxKeyRunes and hasControlRunesInSecret.
	isSSHKey := (in.Kind == vaultKindSSH || in.Kind == vaultKindCredential) && in.AuthMethod == vaultAuthKey
	maxSecretRunes := vaultMaxSecretRunes
	if isSSHKey {
		maxSecretRunes = vaultMaxKeyRunes
	}
	if utf8.RuneCountInString(in.Secret) > maxSecretRunes {
		return errTooLong
	}
	if len(in.Secret) > vaultCodecMaxSecret || len(in.Passphrase) > vaultCodecMaxSecret {
		return errTooLong // (the codec's u16 length prefix)
	}
	if hasControlRunes(in.Label) || hasControlRunes(in.Username) || hasControlRunesInSecret(in.Secret, isSSHKey) {
		return errBadChars
	}
	if in.Passphrase != "" || in.ClearPassphrase {
		if !isSSHKey {
			return errBadChars // a passphrase belongs to a private key only
		}
		if utf8.RuneCountInString(in.Passphrase) > vaultMaxPassphraseRunes {
			return errTooLong
		}
		if hasControlRunesInSecret(in.Passphrase, false) {
			return errBadChars
		}
	}
	if in.CredID != "" {
		if in.Kind != vaultKindSSH && in.Kind != vaultKindRDP {
			return errBadChars // only a connection uses a credential
		}
		if _, ok := parentIDFromHex(in.CredID); !ok {
			return errBadChars
		}
	}
	switch in.Kind {
	case vaultKindPassword:
		// Origin/TenantID carry the place: a tenant, a whole server (tenant
		// 0) or every server (no origin, tenant 0) — never a tenant alone.
		if in.Origin == "" && in.TenantID != 0 {
			return errBadChars
		}
	case vaultKindFolder:
		if in.Host != "" || in.Port != 0 || in.Secret != "" {
			return errBadChars // a folder is purely organizational
		}
	case vaultKindCredential:
		// a reusable username + password / private key of the local base:
		// no place (server, tenant, folder), no host.
		if in.Host != "" || in.Port != 0 || in.Origin != "" || in.TenantID != 0 || in.ParentID != "" {
			return errBadChars
		}
		if in.AuthMethod != vaultAuthPassword && in.AuthMethod != vaultAuthKey {
			return errBadChars
		}
	case vaultKindSSH, vaultKindRDP:
		if utf8.RuneCountInString(in.Host) == 0 || len(in.Host) > vaultMaxHostRunes {
			return errBadChars
		}
		if hasControlRunes(in.Host) {
			return errBadChars
		}
		if in.Port == 0 || in.Port > 65535 {
			return errBadChars
		}
		if in.Kind == vaultKindRDP && in.AuthMethod != vaultAuthPassword {
			return errBadChars // RDP only ever authenticates with a password in this pass
		}
	default:
		return errBadChars
	}
	if _, ok := parentIDFromHex(in.ParentID); !ok {
		return errBadChars
	}
	if len(in.Devices) > 0 {
		if in.Kind != vaultKindPassword {
			return errBadChars // only a password is offered per device
		}
		devs, err := validateDevices(in.Devices)
		if err != nil {
			return err
		}
		in.Devices = devs
	}
	return nil
}

func hasControlRunes(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || (r < 0x20) || r == 0x7f {
			return true
		}
	}
	return false
}

// hasControlRunesInSecret is hasControlRunes, except for an SSH private key
// (allowKeyNewlines) it lets '\n' and '\r' through — a PEM block is nothing
// but newline-separated lines, unlike every other secret this vault stores
// (a typed-in password never legitimately contains one). Every other
// control rune (tabs, NUL, other C0 codes, DEL) is still rejected either
// way: none of those appear in a real PEM file.
func hasControlRunesInSecret(s string, allowKeyNewlines bool) bool {
	for _, r := range s {
		if allowKeyNewlines && (r == '\n' || r == '\r') {
			continue
		}
		if r == '\n' || r == '\r' || r == '\t' || (r < 0x20) || r == 0x7f {
			return true
		}
	}
	return false
}

// Save creates or updates an entry. On create, id must be "" and Secret must
// not be empty. On edit, an empty Secret keeps the existing one.
func (v *Vault) Save(in SaveInput) (string, error) {
	if err := validateSaveInput(&in); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return "", errVaultLocked
	}

	parentID, ok := parentIDFromHex(in.ParentID)
	if !ok {
		return "", errBadChars // re-checked here: validateSaveInput ran before v.mu was held
	}
	if parentID != zeroVaultID {
		parent, _ := v.findLocked(in.ParentID)
		if parent == nil || parent.Kind != vaultKindFolder {
			return "", errNotFound
		}
	}

	// A connection's credential: an existing one (a password one for RDP,
	// which only ever signs in with a password).
	var credID [16]byte
	if in.CredID != "" {
		cid, _ := parentIDFromHex(in.CredID)
		cred, _ := v.findLocked(in.CredID)
		if cred == nil || cred.Kind != vaultKindCredential {
			return "", errCredMissing
		}
		if in.Kind == vaultKindRDP && cred.AuthMethod != vaultAuthPassword {
			return "", errCredKeyRDP
		}
		credID = cid
	}

	var e *VaultEntry
	if in.ID == "" {
		if in.Kind != vaultKindFolder && credID == zeroVaultID && utf8.RuneCountInString(in.Secret) == 0 {
			return "", errEmptySecret
		}
		if len(v.entries) >= vaultMaxEntries {
			return "", errors.New("vault full")
		}
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		e = &VaultEntry{ID: raw, Created: v.now(), Kind: in.Kind}
		v.entries = append(v.entries, e)
	} else {
		found, _ := v.findLocked(in.ID)
		if found == nil {
			return "", errNotFound
		}
		if found.Kind != in.Kind {
			return "", errBadChars // an entry never changes kind across an edit
		}
		if found.ID == parentID {
			return "", errBadChars // a folder cannot be its own parent
		}
		if found.Kind == vaultKindCredential {
			// A credential going from a password to a key (or back) needs
			// its new secret; an RDP connection using it keeps a password.
			if found.AuthMethod != in.AuthMethod && utf8.RuneCountInString(in.Secret) == 0 {
				return "", errEmptySecret
			}
			if in.AuthMethod != vaultAuthPassword {
				for _, c := range v.entries {
					if c.Kind == vaultKindRDP && c.CredID == found.ID {
						return "", errCredKeyRDP
					}
				}
			}
		}
		if (found.Kind == vaultKindSSH || found.Kind == vaultKindRDP) && credID == zeroVaultID &&
			!found.hasSecret && found.CredID != zeroVaultID && utf8.RuneCountInString(in.Secret) == 0 {
			return "", errEmptySecret // (its credential let go: a secret of its own now)
		}
		e = found
	}

	e.Updated = v.now()
	e.Label = in.Label
	e.Note = in.Note
	e.ParentID = parentID
	switch in.Kind {
	case vaultKindPassword:
		e.Origin = in.Origin
		e.TenantID = in.TenantID
		e.TenantName = in.TenantName
		if e.TenantID == 0 {
			e.TenantName = ""
		}
		e.Username = in.Username
		e.SecureOnly = in.SecureOnly
		e.Devices = append([]VaultDevice(nil), in.Devices...)
		if len(e.Devices) > 0 {
			// Restricted: the entry's own place holds one of its devices, so
			// the management list and an older reader agree on where it lives.
			inScope := false
			for _, d := range e.Devices {
				if placeHolds(e.Origin, e.TenantID, d) {
					inScope = true
					break
				}
			}
			if !inScope {
				e.Origin, e.TenantID, e.TenantName = e.Devices[0].Origin, e.Devices[0].TenantID, ""
			}
		}
	case vaultKindSSH, vaultKindRDP:
		e.Host = in.Host
		e.Port = in.Port
		e.SecureOnly = in.SecureOnly
		e.CredID = credID
		if credID != zeroVaultID {
			// Its credential's username and secret, not a copy: none of its own.
			e.Username = ""
			e.AuthMethod = vaultAuthPassword
			wipeBytesLocal(e.secret)
			wipeBytesLocal(e.passphrase)
			e.secret, e.passphrase, e.hasSecret, e.hasPass = nil, nil, false, false
			in.Secret, in.Passphrase = "", ""
		} else {
			e.Username = in.Username
			e.AuthMethod = in.AuthMethod
		}
	case vaultKindCredential:
		e.Username = in.Username
		e.AuthMethod = in.AuthMethod
	case vaultKindFolder:
		// name (Label) and ParentID only — everything else stays zero.
	}
	if utf8.RuneCountInString(in.Secret) > 0 {
		sealed, err := v.seal.Seal([]byte(in.Secret))
		if err != nil {
			return "", err
		}
		wipeBytesLocal(e.secret)
		e.secret = sealed
		e.hasSecret = true
	} else if e.secret == nil && e.Kind != vaultKindFolder {
		// (a connection using a credential: an empty sealed secret, as an
		// import without one leaves)
		if sealed, err := v.seal.Seal(nil); err == nil {
			e.secret = sealed
		}
	}
	in.Secret = ""
	keyEntry := (e.Kind == vaultKindSSH || e.Kind == vaultKindCredential) && e.AuthMethod == vaultAuthKey
	if !keyEntry || in.ClearPassphrase {
		wipeBytesLocal(e.passphrase)
		e.passphrase, e.hasPass = nil, false
	}
	if keyEntry && utf8.RuneCountInString(in.Passphrase) > 0 {
		sealed, err := v.seal.Seal([]byte(in.Passphrase))
		if err != nil {
			return "", err
		}
		wipeBytesLocal(e.passphrase)
		e.passphrase, e.hasPass = sealed, true
	}
	in.Passphrase = ""

	v.touch()
	if err := v.persistLocked(); err != nil {
		return "", err
	}
	return idHex(e.ID), nil
}

// Delete removes an entry (the caller confirms with the operator first).
func (v *Vault) Delete(idHexStr string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return errVaultLocked
	}
	e, i := v.findLocked(idHexStr)
	if e == nil {
		return errNotFound
	}
	if e.Kind == vaultKindFolder {
		for _, other := range v.entries {
			if other.ParentID == e.ID {
				return errFolderNotEmpty
			}
		}
	}
	if e.Kind == vaultKindCredential {
		for _, other := range v.entries {
			if other.CredID == e.ID {
				return errCredInUse // (the page lists them: Credentials' Uses)
			}
		}
	}
	wipeBytesLocal(e.secret)
	wipeBytesLocal(e.passphrase)
	v.entries = append(v.entries[:i], v.entries[i+1:]...)
	v.touch()
	return v.persistLocked()
}

// ChangeCode re-derives the code hash after checking oldCode, and re-encrypts
// on the next persist (the DPAPI layer is unaffected by the code — only the
// PBKDF2 hash used to gate unlocking changes).
func (v *Vault) ChangeCode(oldCode, newCode string) error {
	if utf8.RuneCountInString(newCode) < vaultMinCodeLen {
		return errors.New("code too short")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return errVaultLocked
	}
	hash := pbkdf2SHA256([]byte(oldCode), v.codeSalt, int(v.codeIter), 32)
	if subtle.ConstantTimeCompare(hash, v.codeHash) != 1 {
		return errWrongCode
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	v.codeSalt = salt
	v.codeIter = uint32(v.iterations)
	v.codeHash = pbkdf2SHA256([]byte(newCode), salt, v.iterations, 32)
	return v.persistLocked()
}

// Reset deletes the vault outright, or sets it aside (kept for an eventual
// profile restore) when the vault is unreadable. Only from unlocked (normal
// delete, code re-checked by the caller before this) or unreadable
// (set-aside, no code to check).
func (v *Vault) Reset(setAside bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.held {
		return errVaultHeld
	}
	if v.state != vaultUnlocked && v.state != vaultUnreadable {
		return errVaultState(v.state)
	}
	if err := v.store.Reset(setAside); err != nil {
		return err
	}
	v.clearPresenceLocked()
	for _, e := range v.entries {
		wipeBytesLocal(e.secret)
	}
	v.entries = nil
	v.codeSalt, v.codeHash = nil, nil
	v.state = vaultAbsent
	return nil
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// persistLocked serializes the current in-memory state and writes it
// through prot+store. Called with v.mu held. Entries' secrets are opened one
// at a time (sealer.Open) only for the instant they are copied into the
// plaintext codec buffer, which is wiped right after Protect returns.
func (v *Vault) persistLocked() error {
	v.revision++
	pv := &vaultPlain{
		revision: v.revision, autoLockMin: v.autoLockMi,
		codeSalt: v.codeSalt, codeIter: v.codeIter, codeHash: v.codeHash,
	}
	for _, e := range v.entries {
		pe := vaultPlainEntry{
			id: e.ID, created: e.Created, updated: e.Updated, lastUsed: e.LastUsed,
			secureOnly: e.SecureOnly, origin: e.Origin, tenantID: e.TenantID, tenantName: e.TenantName,
			label: e.Label, username: e.Username, note: e.Note,
			kind: e.Kind, parentID: e.ParentID, host: e.Host, port: e.Port, authMethod: e.AuthMethod,
			devices: e.Devices, credID: e.CredID,
		}
		_ = v.seal.Open(e.secret, func(secret []byte) error {
			pe.secret = append([]byte(nil), secret...)
			return nil
		})
		if e.passphrase != nil {
			_ = v.seal.Open(e.passphrase, func(p []byte) error {
				pe.passphrase = append([]byte(nil), p...)
				return nil
			})
		}
		pv.entries = append(pv.entries, pe)
	}
	plain := encodeVaultPlain(pv)
	defer wipeBytesLocal(plain)
	for i := range pv.entries {
		wipeBytesLocal(pv.entries[i].secret)
		wipeBytesLocal(pv.entries[i].passphrase)
	}
	blob, err := v.prot.Protect(plain)
	if err != nil {
		return err
	}
	outer := buildOuter(1, blob)
	return v.store.Save(outer)
}

// ---------------------------------------------------------------------------
// Backoff (brute-force throttling)
// ---------------------------------------------------------------------------

func (v *Vault) waitLocked() time.Duration {
	if v.failUntil.IsZero() {
		return 0
	}
	if d := v.failUntil.Sub(v.now()); d > 0 {
		return d
	}
	return 0
}

func (v *Vault) registerFailureLocked() {
	v.failCount++
	var wait time.Duration
	switch {
	case v.failCount >= 15:
		wait = 15 * time.Minute
	case v.failCount >= 10:
		wait = 5 * time.Minute
	case v.failCount >= 5:
		wait = 30 * time.Second
	}
	if wait > 0 {
		v.failUntil = v.now().Add(wait)
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	errVaultLocked    = errors.New("locked")
	errVaultHeld      = errors.New("help_session") // SetHeld: someone else drives this computer
	errWrongCode      = errors.New("wrong_code")
	errNotFound       = errors.New("not_found")
	errScopeMismatch  = errors.New("scope_mismatch")
	errEmptyLabel     = errors.New("empty_label")
	errEmptySecret    = errors.New("empty_secret")
	errTooLong        = errors.New("too_long")
	errBadChars       = errors.New("bad_chars")
	errFolderNotEmpty = errors.New("folder_not_empty")
	// v4: a connection's credential gone (deleted meanwhile), one still used
	// by connections (Delete refuses), a key credential for RDP.
	errCredMissing = errors.New("cred_missing")
	errCredInUse   = errors.New("cred_in_use")
	errCredKeyRDP  = errors.New("cred_rdp_key")
	// errPresenceOff: Windows Hello may not unlock this vault (turned off,
	// or not offered here).
	errPresenceOff = errors.New("presence_off")
	// errKeyringUnavailable: the protector cannot reach its key right now
	// (Linux: no Secret Service, or its keyring stays locked): the vault
	// stays locked, it is NOT unreadable (never offered for a reset). DPAPI
	// never answers it.
	errKeyringUnavailable = errors.New("keyring_unavailable")
)

type vaultWaitError struct{ Seconds int }

func (e *vaultWaitError) Error() string { return "wait" }

func errVaultState(s vaultState) error {
	switch s {
	case vaultAbsent:
		return errors.New("absent")
	case vaultUnreadable:
		return errors.New("unreadable")
	default:
		return errors.New("bad_state")
	}
}

// ---------------------------------------------------------------------------
// PBKDF2-HMAC-SHA256 (local implementation: go.mod targets go1.21, avoiding a
// dependency on golang.org/x/crypto for one function). Verified against the
// RFC 6070 test vectors in vault_test.go.
// ---------------------------------------------------------------------------

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, numBlocks*hashLen)
	buf := make([]byte, len(salt)+4)
	copy(buf, salt)
	for block := 1; block <= numBlocks; block++ {
		binary.BigEndian.PutUint32(buf[len(salt):], uint32(block))
		prf.Reset()
		prf.Write(buf)
		u := prf.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func wipeBytesLocal(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
