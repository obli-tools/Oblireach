//go:build linux

package main

// secretservice_linux.go — the freedesktop Secret Service API
// (org.freedesktop.secrets) as the keeper of the app's sealing keys: the
// vault's (vault_linux.go), the support servers' (support_store_linux.go) and
// the biometric switch's marker. GNOME Keyring and KWallet 6 (ksecretd /
// kwalletd6 with its Secret Service interface) both serve it; KeePassXC can.
//
// What is stored: one item per key, a random 256-bit AES key, in the default
// collection ("Login", unlocked by PAM at sign-in on KDE and GNOME), with
// the attributes
//
//   xdg:schema = tools.obli.Oblireach.Key
//   app        = tools.obli.Oblireach
//   purpose    = vault-key | support-key | vault-presence-key
//   profile    = the app data folder's id (two installs, e.g. the Flatpak and
//                a development build, never share a key by accident)
//   key-id     = 32 hex digits, written in every blob the key seals
//
// Transfer: the "dh-ietf1024-sha256-aes128-cbc-pkcs7" session (the secret is
// AES-encrypted on the bus, as libsecret does), "plain" only when the
// service offers nothing else.
//
// A locked collection or item: Unlock, and the service's Prompt (its own
// password dialog) when it asks for one; a dismissed prompt is
// errKeyringLocked. No service at all (nothing owns the name, or the
// Flatpak's proxy hides it): errKeyringMissing. Both wrap
// errKeyringUnavailable: the vault stays locked (vault.go), it is never
// reported unreadable for a keyring that is just closed.
//
// Security notes (see also vault.go's header):
//   - the Secret Service has no per-application access control: every
//     program of the same user that can reach the session bus may search and
//     read these items (GNOME Keyring: no ACL at all; KWallet: its
//     per-application "allow" list applies to the KWallet API, not to the
//     Secret Service interface). Same scope as DPAPI (current user) on
//     Windows; in the Flatpak, other sandboxed apps without
//     --talk-name=org.freedesktop.secrets cannot;
//   - at rest the collection is encrypted with the user's keyring password
//     (the login password when PAM unlocks it). An empty keyring password
//     (automatic login without a password) leaves the key in clear on disk:
//     the vault is then only as safe as the disk encryption (LUKS).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/crypto/hkdf"
)

const (
	ssBusName      = "org.freedesktop.secrets"
	ssPath         = dbus.ObjectPath("/org/freedesktop/secrets")
	ssIface        = "org.freedesktop.Secret.Service"
	ssCollIface    = "org.freedesktop.Secret.Collection"
	ssItemIface    = "org.freedesktop.Secret.Item"
	ssPromptIface  = "org.freedesktop.Secret.Prompt"
	ssDefaultAlias = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	ssNoPath       = dbus.ObjectPath("/")

	ssAlgoDH    = "dh-ietf1024-sha256-aes128-cbc-pkcs7"
	ssAlgoPlain = "plain"

	ssAppID     = "tools.obli.Oblireach"
	ssSchema    = "tools.obli.Oblireach.Key"
	ssKeyLen    = 32
	ssKeyIDLen  = 16
	ssItemLabel = "Oblireach Desktop"

	// How long the service's own unlock dialog may stay up.
	ssPromptTimeout = 5 * time.Minute
)

// The key purposes (attribute "purpose").
const (
	ssPurposeVault    = "vault-key"
	ssPurposeSupport  = "support-key"
	ssPurposePresence = "vault-presence-key"
)

var (
	// (errKeyringUnavailable, vault.go: the keyring cannot give the key now.)
	errKeyringMissing = fmt.Errorf("%w: no Secret Service (org.freedesktop.secrets) on the session bus: install and start GNOME Keyring or enable KWallet's Secret Service interface (System Settings > KDE Wallet), and in the Flatpak grant --talk-name=org.freedesktop.secrets", errKeyringUnavailable)
	errKeyringLocked  = fmt.Errorf("%w: the keyring stays locked (its unlock dialog was dismissed)", errKeyringUnavailable)
	// errKeyringNoKey: the key a blob names is not in the keyring (another
	// user's or another computer's file, a key deleted by hand).
	errKeyringNoKey = errors.New("the key of this file is not in the keyring")
)

// ssSecret: org.freedesktop.Secret's Secret struct (oayays).
type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// ── The transfer session ───────────────────────────────────────────────────

// The 1024-bit MODP group of RFC 2409 (Oakley group 2), generator 2: what the
// Secret Service's dh-ietf1024 algorithm names.
var ssDHPrime, _ = new(big.Int).SetString(
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74"+
		"020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F1437"+
		"4FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED"+
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF", 16)

const ssDHBytes = 128

// ssDHKeyPair: a private exponent and its public value (2^x mod p, big
// endian).
func ssDHKeyPair(r io.Reader) (*big.Int, []byte, error) {
	buf := make([]byte, ssDHBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, nil, err
	}
	x := new(big.Int).SetBytes(buf)
	x.Mod(x, new(big.Int).Sub(ssDHPrime, big.NewInt(2)))
	x.Add(x, big.NewInt(2)) // 2 <= x <= p-1
	pub := new(big.Int).Exp(big.NewInt(2), x, ssDHPrime)
	return x, pub.Bytes(), nil
}

// ssDHAESKey: the AES-128 key of a session: HKDF-SHA256 (no salt, no info)
// of the shared secret, left-padded to the prime's 128 bytes (libsecret,
// gnome-keyring, python-secretstorage).
func ssDHAESKey(priv *big.Int, peer []byte) ([]byte, error) {
	y := new(big.Int).SetBytes(peer)
	one := big.NewInt(1)
	if y.Cmp(one) <= 0 || y.Cmp(new(big.Int).Sub(ssDHPrime, one)) >= 0 {
		return nil, errors.New("secret service: bad DH public value")
	}
	shared := new(big.Int).Exp(y, priv, ssDHPrime).Bytes()
	ikm := make([]byte, ssDHBytes)
	copy(ikm[ssDHBytes-len(shared):], shared)
	key := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, nil, nil), key); err != nil {
		return nil, err
	}
	wipeBytesLocal(ikm)
	return key, nil
}

// ssSession: an open transfer session (aes nil: plain).
type ssSession struct {
	path dbus.ObjectPath
	aes  []byte
}

func (s *ssSession) encode(plain []byte, r io.Reader) (ssSecret, error) {
	out := ssSecret{Session: s.path, ContentType: "application/octet-stream"}
	if s.aes == nil {
		out.Value = append([]byte(nil), plain...)
		return out, nil
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(r, iv); err != nil {
		return out, err
	}
	block, err := aes.NewCipher(s.aes)
	if err != nil {
		return out, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := make([]byte, len(plain)+pad)
	copy(buf, plain)
	for i := len(plain); i < len(buf); i++ {
		buf[i] = byte(pad)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(buf, buf)
	out.Parameters, out.Value = iv, buf
	return out, nil
}

// decode: the secret's bytes (the caller wipes them).
func (s *ssSession) decode(sec ssSecret) ([]byte, error) {
	if s.aes == nil {
		return append([]byte(nil), sec.Value...), nil
	}
	if len(sec.Parameters) != aes.BlockSize || len(sec.Value) == 0 || len(sec.Value)%aes.BlockSize != 0 {
		return nil, errors.New("secret service: malformed secret")
	}
	block, err := aes.NewCipher(s.aes)
	if err != nil {
		return nil, err
	}
	buf := append([]byte(nil), sec.Value...)
	cipher.NewCBCDecrypter(block, sec.Parameters).CryptBlocks(buf, buf)
	pad := int(buf[len(buf)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(buf) {
		wipeBytesLocal(buf)
		return nil, errors.New("secret service: bad padding")
	}
	for _, b := range buf[len(buf)-pad:] {
		if int(b) != pad {
			wipeBytesLocal(buf)
			return nil, errors.New("secret service: bad padding")
		}
	}
	return buf[:len(buf)-pad], nil
}

// ── The client ─────────────────────────────────────────────────────────────

// ssClient: one conversation with the Secret Service (a session per use,
// closed after it: nothing stays open between two vault operations).
type ssClient struct {
	conn *dbus.Conn
	svc  dbus.BusObject
	sess *ssSession
}

// ssDial: the service, a transfer session open.
func ssDial() (*ssClient, error) {
	conn, err := sessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", errKeyringMissing, err)
	}
	c := &ssClient{conn: conn, svc: conn.Object(ssBusName, ssPath)}
	if err := c.openSession(); err != nil {
		if dbusNameMissing(err) {
			return nil, errKeyringMissing
		}
		return nil, err
	}
	return c, nil
}

func (c *ssClient) openSession() error {
	priv, pub, err := ssDHKeyPair(rand.Reader)
	if err != nil {
		return err
	}
	var out dbus.Variant
	var path dbus.ObjectPath
	err = c.svc.Call(ssIface+".OpenSession", 0, ssAlgoDH, dbus.MakeVariant(pub)).Store(&out, &path)
	if err == nil {
		peer, ok := out.Value().([]byte)
		if !ok {
			return errors.New("secret service: bad OpenSession answer")
		}
		key, err := ssDHAESKey(priv, peer)
		if err != nil {
			return err
		}
		c.sess = &ssSession{path: path, aes: key}
		return nil
	}
	if dbusNameMissing(err) {
		return err
	}
	// The service does not offer the DH session: plain (on the user's own
	// session bus; the Flatpak's proxy is the only party in between).
	if err := c.svc.Call(ssIface+".OpenSession", 0, ssAlgoPlain, dbus.MakeVariant("")).Store(&out, &path); err != nil {
		return err
	}
	c.sess = &ssSession{path: path}
	return nil
}

func (c *ssClient) close() {
	if c.sess != nil && c.sess.path != "" && c.sess.path != ssNoPath {
		_ = c.conn.Object(ssBusName, c.sess.path).Call("org.freedesktop.Secret.Session.Close", 0).Err
		wipeBytesLocal(c.sess.aes)
	}
}

// prompt runs a Prompt object (the service's own dialog) and waits for its
// Completed signal. dismissed: the user closed it.
func (c *ssClient) prompt(p dbus.ObjectPath) (dismissed bool, result dbus.Variant, err error) {
	if p == "" || p == ssNoPath {
		return false, dbus.Variant{}, nil
	}
	w, err := watchSignal(c.conn, p, ssPromptIface, "Completed")
	if err != nil {
		return false, dbus.Variant{}, err
	}
	defer w.stop()
	obj := c.conn.Object(ssBusName, p)
	if err := obj.Call(ssPromptIface+".Prompt", 0, portalParentWindow(0)).Err; err != nil {
		return false, dbus.Variant{}, err
	}
	body, err := w.wait(ssPromptTimeout)
	if err != nil {
		_ = obj.Call(ssPromptIface+".Dismiss", 0).Err
		return true, dbus.Variant{}, nil
	}
	if len(body) >= 1 {
		dismissed, _ = body[0].(bool)
	}
	if len(body) >= 2 {
		result, _ = body[1].(dbus.Variant)
	}
	return dismissed, result, nil
}

// unlock: these objects unlocked (their prompt if the service asks).
func (c *ssClient) unlock(objs []dbus.ObjectPath) error {
	if len(objs) == 0 {
		return nil
	}
	var unlocked []dbus.ObjectPath
	var p dbus.ObjectPath
	if err := c.svc.Call(ssIface+".Unlock", 0, objs).Store(&unlocked, &p); err != nil {
		return err
	}
	if p == ssNoPath || p == "" {
		return nil
	}
	dismissed, _, err := c.prompt(p)
	if err != nil {
		return err
	}
	if dismissed {
		return errKeyringLocked
	}
	return nil
}

// search: the items with these attributes, unlocked ones first, then the
// locked ones once unlocked; sorted (a stable choice among duplicates).
func (c *ssClient) search(attrs map[string]string) ([]dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	if err := c.svc.Call(ssIface+".SearchItems", 0, attrs).Store(&unlocked, &locked); err != nil {
		return nil, err
	}
	if len(locked) > 0 {
		if err := c.unlock(locked); err != nil {
			if len(unlocked) == 0 {
				return nil, err
			}
		} else {
			unlocked = append(unlocked, locked...)
		}
	}
	sort.Slice(unlocked, func(i, j int) bool { return unlocked[i] < unlocked[j] })
	return unlocked, nil
}

// secret: an item's secret bytes (the caller wipes them).
func (c *ssClient) secret(item dbus.ObjectPath) ([]byte, error) {
	var sec ssSecret
	if err := c.conn.Object(ssBusName, item).Call(ssItemIface+".GetSecret", 0, c.sess.path).Store(&sec); err != nil {
		if dbusErrName(err) == "org.freedesktop.Secret.Error.IsLocked" {
			if err := c.unlock([]dbus.ObjectPath{item}); err != nil {
				return nil, err
			}
			if err := c.conn.Object(ssBusName, item).Call(ssItemIface+".GetSecret", 0, c.sess.path).Store(&sec); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	defer wipeBytesLocal(sec.Value)
	return c.sess.decode(sec)
}

// attributes: an item's attributes.
func (c *ssClient) attributes(item dbus.ObjectPath) (map[string]string, error) {
	v, err := c.conn.Object(ssBusName, item).GetProperty(ssItemIface + ".Attributes")
	if err != nil {
		return nil, err
	}
	m, ok := v.Value().(map[string]string)
	if !ok {
		return nil, errors.New("secret service: bad attributes")
	}
	return m, nil
}

// defaultCollection: the "default" alias, created (the service may prompt
// for its password) when the user has none yet, unlocked.
func (c *ssClient) defaultCollection() (dbus.ObjectPath, error) {
	var coll dbus.ObjectPath
	err := c.svc.Call(ssIface+".ReadAlias", 0, "default").Store(&coll)
	if err != nil && !dbusNoMethod(err) {
		return "", err
	}
	if err != nil {
		coll = ssDefaultAlias // (a service without ReadAlias: the alias path itself)
	}
	if coll == ssNoPath || coll == "" {
		props := map[string]dbus.Variant{ssCollIface + ".Label": dbus.MakeVariant("Login")}
		var p dbus.ObjectPath
		if err := c.svc.Call(ssIface+".CreateCollection", 0, props, "default").Store(&coll, &p); err != nil {
			return "", err
		}
		if coll == ssNoPath || coll == "" {
			dismissed, res, err := c.prompt(p)
			if err != nil {
				return "", err
			}
			if dismissed {
				return "", errKeyringLocked
			}
			coll, _ = res.Value().(dbus.ObjectPath)
			if coll == "" || coll == ssNoPath {
				return "", errKeyringLocked
			}
		}
	}
	if v, err := c.conn.Object(ssBusName, coll).GetProperty(ssCollIface + ".Locked"); err == nil {
		if locked, _ := v.Value().(bool); locked {
			if err := c.unlock([]dbus.ObjectPath{coll}); err != nil {
				return "", err
			}
		}
	}
	return coll, nil
}

// create stores a new item (never replacing one: a fresh key-id each time).
func (c *ssClient) create(label string, attrs map[string]string, secret []byte) (dbus.ObjectPath, error) {
	coll, err := c.defaultCollection()
	if err != nil {
		return "", err
	}
	sec, err := c.sess.encode(secret, rand.Reader)
	if err != nil {
		return "", err
	}
	defer wipeBytesLocal(sec.Value)
	props := map[string]dbus.Variant{
		ssItemIface + ".Label":      dbus.MakeVariant(label),
		ssItemIface + ".Attributes": dbus.MakeVariant(attrs),
	}
	var item, p dbus.ObjectPath
	err = c.conn.Object(ssBusName, coll).Call(ssCollIface+".CreateItem", 0, props, sec, false).Store(&item, &p)
	if dbusErrName(err) == "org.freedesktop.Secret.Error.IsLocked" {
		if uerr := c.unlock([]dbus.ObjectPath{coll}); uerr != nil {
			return "", uerr
		}
		err = c.conn.Object(ssBusName, coll).Call(ssCollIface+".CreateItem", 0, props, sec, false).Store(&item, &p)
	}
	if err != nil {
		return "", err
	}
	if item == ssNoPath || item == "" {
		dismissed, res, err := c.prompt(p)
		if err != nil {
			return "", err
		}
		if dismissed {
			return "", errKeyringLocked
		}
		item, _ = res.Value().(dbus.ObjectPath)
	}
	return item, nil
}

// remove deletes an item (its prompt if the service asks).
func (c *ssClient) remove(item dbus.ObjectPath) error {
	var p dbus.ObjectPath
	if err := c.conn.Object(ssBusName, item).Call(ssItemIface+".Delete", 0).Store(&p); err != nil {
		return err
	}
	dismissed, _, err := c.prompt(p)
	if err == nil && dismissed {
		return errKeyringLocked
	}
	return err
}

// ── The key store ──────────────────────────────────────────────────────────

// ssKeyStore: the app's sealing keys in the Secret Service, by purpose. The
// key a blob names (its key-id) is looked up by that id alone (the profile
// may have moved); a new blob uses the profile's key of that purpose,
// created on first use.
type ssKeyStore struct {
	profile string
	mu      sync.Mutex // one creation at a time (no duplicate keys in-process)
}

// ssProfileID: the id of this install's data folder (attribute "profile").
func ssProfileID() string {
	sum := sha256.Sum256([]byte("oblireach-profile\x00" + platformPaths.localDataDir()))
	return hex.EncodeToString(sum[:8])
}

var appKeyStore = &ssKeyStore{}

func (k *ssKeyStore) profileID() string {
	if k.profile == "" {
		k.profile = ssProfileID()
	}
	return k.profile
}

// ssWrapErr: a Secret Service failure as the vault sees it.
func ssWrapErr(err error) error {
	if err == nil || errors.Is(err, errKeyringUnavailable) || errors.Is(err, errKeyringNoKey) {
		return err
	}
	if dbusNameMissing(err) {
		return errKeyringMissing
	}
	return fmt.Errorf("%w: %v", errKeyringUnavailable, err)
}

// key: the key named keyID, for use (wiped after it).
func (k *ssKeyStore) key(purpose string, keyID []byte, use func(key []byte) error) error {
	c, err := ssDial()
	if err != nil {
		return ssWrapErr(err)
	}
	defer c.close()
	items, err := c.search(map[string]string{"app": ssAppID, "purpose": purpose, "key-id": hex.EncodeToString(keyID)})
	if err != nil {
		return ssWrapErr(err)
	}
	if len(items) == 0 {
		return errKeyringNoKey
	}
	key, err := c.secret(items[0])
	if err != nil {
		return ssWrapErr(err)
	}
	defer wipeBytesLocal(key)
	if len(key) != ssKeyLen {
		return errKeyringNoKey
	}
	return use(key)
}

// current: the profile's key of this purpose, created when there is none.
func (k *ssKeyStore) current(purpose string, use func(keyID, key []byte) error) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	c, err := ssDial()
	if err != nil {
		return ssWrapErr(err)
	}
	defer c.close()
	attrs := map[string]string{"app": ssAppID, "purpose": purpose, "profile": k.profileID()}
	items, err := c.search(attrs)
	if err != nil {
		return ssWrapErr(err)
	}
	for _, it := range items {
		a, err := c.attributes(it)
		if err != nil {
			continue
		}
		id, err := hex.DecodeString(a["key-id"])
		if err != nil || len(id) != ssKeyIDLen {
			continue
		}
		key, err := c.secret(it)
		if err != nil {
			return ssWrapErr(err)
		}
		if len(key) != ssKeyLen {
			wipeBytesLocal(key)
			continue
		}
		err = use(id, key)
		wipeBytesLocal(key)
		return err
	}
	id := make([]byte, ssKeyIDLen)
	key := make([]byte, ssKeyLen)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	if _, err := rand.Read(key); err != nil {
		return err
	}
	defer wipeBytesLocal(key)
	attrs["xdg:schema"] = ssSchema
	attrs["key-id"] = hex.EncodeToString(id)
	if _, err := c.create(ssItemLabel+" ("+purpose+")", attrs, key); err != nil {
		return ssWrapErr(err)
	}
	return use(id, key)
}

// forget deletes every key of this purpose and profile (a vault reset may
// call it; nothing does today: an old key costs nothing and a file set
// aside may still need it).
func (k *ssKeyStore) forget(purpose string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	c, err := ssDial()
	if err != nil {
		return ssWrapErr(err)
	}
	defer c.close()
	items, err := c.search(map[string]string{"app": ssAppID, "purpose": purpose, "profile": k.profileID()})
	if err != nil {
		return ssWrapErr(err)
	}
	for _, it := range items {
		if err := c.remove(it); err != nil {
			return ssWrapErr(err)
		}
	}
	return nil
}
