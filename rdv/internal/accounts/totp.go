package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238): HMAC-SHA1, 6 digits, 30 s steps, one step of clock skew
// accepted each way. The shared secret is 160 random bits.
const (
	TOTPDigits = 6
	TOTPPeriod = 30
	TOTPSkew   = 1
	totpBytes  = 20
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random secret, base32 (what authenticator apps
// take).
func NewTOTPSecret() string {
	b := make([]byte, totpBytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return b32.EncodeToString(b)
}

// ParseTOTPSecret decodes a base32 secret.
func ParseTOTPSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	b, err := b32.DecodeString(strings.TrimRight(s, "="))
	if err != nil || len(b) < 10 {
		return nil, errors.New("accounts: invalid TOTP secret")
	}
	return b, nil
}

// TOTPCode computes the code of a time step.
func TOTPCode(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// TOTPStep is the time step of t.
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// CheckTOTP returns the matching time step of code around now (skew
// included), or -1. Comparisons are constant time.
func CheckTOTP(key []byte, code string, now time.Time) int64 {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return -1
	}
	cur := TOTPStep(now)
	found := int64(-1)
	for d := int64(-TOTPSkew); d <= TOTPSkew; d++ {
		if hmac.Equal([]byte(TOTPCode(key, cur+d)), []byte(code)) {
			found = cur + d
		}
	}
	return found
}

// TOTPURI is the otpauth:// URI of the enrolment QR code.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// Sealer encrypts small secrets at rest (AES-256-GCM) with a key derived
// from the server secret.
type Sealer struct{ aead cipher.AEAD }

// NewSealer derives the sealing key of a purpose from the server secret.
func NewSealer(serverSecret []byte, purpose string) *Sealer {
	m := hmac.New(sha256.New, serverSecret)
	m.Write([]byte("oblireach-rdv seal v1|" + purpose))
	block, err := aes.NewCipher(m.Sum(nil))
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &Sealer{aead: aead}
}

// Seal encrypts plain; ad binds the ciphertext to its context (the account).
func (s *Sealer) Seal(plain, ad []byte) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return s.aead.Seal(nonce, nonce, plain, ad)
}

// Open decrypts what Seal produced.
func (s *Sealer) Open(sealed, ad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n+s.aead.Overhead() {
		return nil, errors.New("accounts: sealed data too short")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], ad)
}
