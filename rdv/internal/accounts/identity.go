package accounts

import (
	"net/mail"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of the identity fields. The label shown to the person being helped
// must fit the 64 characters every Oblireach client displays, email
// included: a truncated address could hide its real domain.
const (
	MaxEmailLen = 64
	MaxNameLen  = 40
	LabelMax    = 64
)

// ValidEmail checks a normalised address: a plain addr-spec (no display
// name, no comment), a dotted domain, at most MaxEmailLen bytes.
func ValidEmail(e string) bool {
	if e == "" || len(e) > MaxEmailLen || strings.ContainsAny(e, " \t\r\n<>()\"',;:\\[]") {
		return false
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || a.Name != "" {
		return false
	}
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || strings.Count(e, "@") != 1 {
		return false
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	for _, r := range e {
		if r > unicode.MaxASCII {
			return false // IDN addresses: punycode them (homograph risk in the label)
		}
	}
	return true
}

// CleanDisplayName validates a self-declared display name: 1..MaxNameLen
// characters, printable, without the characters that could forge an address
// or a status in the label ("<", ">", "@", brackets, quotes).
func CleanDisplayName(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	n := utf8.RuneCountInString(s)
	if n == 0 || n > MaxNameLen {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune("<>@()[]{}\"'`\\|", r) {
			return "", false
		}
	}
	return s, true
}

// Label is the operator identity shown to the person being helped. The
// server builds it; clients display it as authenticated. The verified part
// is the email address; the name is what the operator chose. An address
// that was never verified (instance without SMTP) is marked as such.
//
//	"Jane Doe <jane@example.com>"           verified
//	"Jane Doe <jane@example.com> (unverified)"
//
// When the full form does not fit LabelMax characters the name goes first,
// so the address is never truncated.
func Label(a Account) string {
	suffix := ""
	if !a.Verified {
		suffix = " (unverified)"
	}
	for _, s := range []string{
		a.Name + " <" + a.Email + ">" + suffix,
		"<" + a.Email + ">" + suffix,
		a.Email + suffix,
	} {
		if utf8.RuneCountInString(s) <= LabelMax {
			return s
		}
	}
	return "account #" + strconv.FormatInt(a.ID, 10) + suffix
}
