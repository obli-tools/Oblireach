package accounts

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP minimum: 19 MiB, 2 passes, 1 lane). Hashing
// runs at most hashParallel at a time so a login flood cannot exhaust the
// server's memory.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
	hashParallel = 4
)

// Password rules.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 128
)

var hashSem = make(chan struct{}, hashParallel)

// HashPassword returns a PHC-formatted argon2id hash.
func HashPassword(pw string) string {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	hashSem <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSem
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key))
}

// CheckPassword verifies pw against a hash produced by HashPassword (its own
// parameters are used, so they can be raised later).
func CheckPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 20 || p == 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	hashSem <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	<-hashSem
	return subtle.ConstantTimeCompare(got, want) == 1
}

var (
	dummyOnce sync.Once
	dummy     string
)

// BurnPasswordCheck spends the time of one password check (unknown email):
// answers take the same time whether the account exists or not.
func BurnPasswordCheck(pw string) {
	dummyOnce.Do(func() { dummy = HashPassword("dummy-password-for-timing") })
	CheckPassword(dummy, pw)
}

// Password policy failures.
var (
	ErrPasswordShort  = errors.New("password too short")
	ErrPasswordLong   = errors.New("password too long")
	ErrPasswordCommon = errors.New("password too common")
)

//go:embed common-passwords.list
var commonList string

var common = func() map[string]bool {
	m := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(commonList))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" && !strings.HasPrefix(l, "#") {
			m[strings.ToLower(l)] = true
		}
	}
	return m
}()

// CheckPolicy applies the password rules: length, the embedded list of
// common passwords, trivial patterns, and the email address itself.
func CheckPolicy(pw, email string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen {
		return ErrPasswordShort
	}
	if len(pw) > MaxPasswordLen {
		return ErrPasswordLong
	}
	low := strings.ToLower(pw)
	if common[low] || trivial(low) {
		return ErrPasswordCommon
	}
	if email != "" {
		local, _, _ := strings.Cut(strings.ToLower(email), "@")
		if low == strings.ToLower(email) || (len(local) >= 4 && strings.Contains(low, local) && n-utf8.RuneCountInString(local) < 6) {
			return ErrPasswordCommon
		}
	}
	return nil
}

// trivial: one repeated character, or a run of consecutive characters
// ("abcdefghij", "0987654321"), or a short pattern repeated ("abcabcabca").
func trivial(s string) bool {
	r := []rune(s)
	distinct := map[rune]bool{}
	for _, c := range r {
		distinct[c] = true
	}
	if len(distinct) <= 3 {
		return true
	}
	asc, desc := true, true
	for i := 1; i < len(r); i++ {
		if r[i] != r[i-1]+1 {
			asc = false
		}
		if r[i] != r[i-1]-1 {
			desc = false
		}
	}
	if asc || desc {
		return true
	}
	allDigits := true
	for _, c := range r {
		if !unicode.IsDigit(c) {
			allDigits = false
			break
		}
	}
	return allDigits && len(distinct) <= 5
}
