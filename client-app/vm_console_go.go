package main

// vm_console_go.go — the Go side of the Hyper-V VM consoles (the page's side
// is vm_console.go): an interactive console of a virtual machine hosted on a
// Hyper-V host, opened through Obliance (POST /api/remote/sessions with
// protocol 'vmconsole' and the VM's GUID). It is an ObliReach-like session
// tab of its own (a picture, the keyboard and the mouse), next to the host's
// ObliReach tab and tool tabs.
//
// A VM console tab is keyed like a tool tab: its host's key and "~vm-<guid>"
// (the GUID in lower case), so the host has its ObliReach tab, its tool tabs
// and one tab per VM console, each in one place (a window or a mosaic). A
// session window of a VM console carries the VM's name (only words for its
// title and tab: the server checks everything again) next to its kind.
//
// Portable (no Windows API): unit-tested everywhere (vm_console_test.go).

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// reVmKind: the kind of a VM console tab or window: "vm-" and the VM's GUID
// in lower case (the page lowers it).
var reVmKind = regexp.MustCompile(`^vm-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// vmKind: k is the kind of a VM console (never a tool kind).
func vmKind(k string) bool { return reVmKind.MatchString(k) }

// validWindowKind: what a session window, a tab key or an entry URL may hold
// besides an ObliReach session: a tool (toolKinds) or a VM console.
func validWindowKind(k string) bool { return toolKinds[k] || vmKind(k) }

// vmNameMax: the longest VM name carried (Hyper-V allows 100 characters).
const vmNameMax = 100

// vmWindowName: a VM's name as a session window carries it: no control
// characters, trimmed, at most vmNameMax characters ("" when nothing is left).
func vmWindowName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > vmNameMax {
		s = strings.TrimSpace(string(r[:vmNameMax]))
	}
	return s
}

// vmEntryURL: the entry URL of a session window (detachedToolEntryURL) with,
// for a VM console, the VM's name ("vn"; the page's tab title).
func vmEntryURL(u, kind, vmName string) string {
	if !vmKind(kind) {
		return u
	}
	if n := vmWindowName(vmName); n != "" {
		u += "&vn=" + url.QueryEscape(n)
	}
	return u
}

// ── The password vault in a VM console ──────────────────────────────────────
//
// The VM console's helper (Obliance agent vmconsole) types a text on the
// GUEST's keyboard layout: {"type":"type_text","text":...,"layout":KLID,
// "guest":"windows"|"linux"} (guest: a Linux guest reads AltGr and Unicode
// input differently, the helper adapts; an older helper ignores it).
// "\t" is Tab and "\n" Enter there. The vault types a saved secret into a VM
// console that way (vault_bridge.go vaultTypeVm): the username and
// Tab first only when asked (and the entry has one), the secret, Enter when
// asked. Built in Go from the secret's bytes: the secret never crosses into
// page JS, and is never turned into a Go string.

// reKlid: a Windows keyboard layout id (8 hex digits).
var reKlid = regexp.MustCompile(`^[0-9A-Fa-f]{8}$`)

var errVmTypeTooLong = errors.New("too_long")

// vmTypeTextMax: the helper types at most this many characters per request.
const vmTypeTextMax = 20000

// vmGuest: the guest system a type_text frame names ("windows" / "linux"),
// "" for anything else (the field is then left out: the helper's default,
// Windows, as with an older Desktop).
func vmGuest(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	}
	return ""
}

// buildVmTypeTextFrame renders the VM console's type_text JSON for one saved
// secret. layout: the guest's keyboard layout (KLID), dropped when malformed
// (the helper then uses its own default). guest: the guest's system
// ("windows" / "linux", vmGuest; an older agent ignores the field).
func buildVmTypeTextFrame(username string, secret []byte, withUser, enter bool, layout, guest string) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errEmptySecret
	}
	n := len(secret) + 64
	if typeTextHasUser(username, withUser) {
		n += len(username) * 2
	}
	out := make([]byte, 0, n)
	out = append(out, `{"type":"type_text","text":"`...)
	runes := 0
	if typeTextHasUser(username, withUser) {
		out, runes = appendJSONText(out, []byte(username), runes)
		out = append(out, `\t`...)
		runes++
	}
	out, runes = appendJSONText(out, secret, runes)
	if enter {
		out = append(out, `\n`...)
		runes++
	}
	out = append(out, '"')
	if reKlid.MatchString(layout) {
		out = append(out, `,"layout":"`...)
		out = append(out, strings.ToUpper(layout)...)
		out = append(out, '"')
	}
	if g := vmGuest(guest); g != "" {
		out = append(out, `,"guest":"`...)
		out = append(out, g...)
		out = append(out, '"')
	}
	out = append(out, '}')
	if runes > vmTypeTextMax {
		wipeBytesLocal(out)
		return nil, errVmTypeTooLong
	}
	return out, nil
}

// appendJSONText appends b to dst as the inside of a JSON string (quote,
// backslash and control characters escaped; a byte that is not valid UTF-8
// becomes U+FFFD), counting its characters into runes.
func appendJSONText(dst, b []byte, runes int) ([]byte, int) {
	const hexd = "0123456789abcdef"
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			switch {
			case c == '"':
				dst = append(dst, '\\', '"')
			case c == '\\':
				dst = append(dst, '\\', '\\')
			case c == '\n':
				dst = append(dst, '\\', 'n')
			case c == '\r':
				dst = append(dst, '\\', 'r')
			case c == '\t':
				dst = append(dst, '\\', 't')
			case c < 0x20 || c == 0x7f:
				dst = append(dst, '\\', 'u', '0', '0', hexd[c>>4], hexd[c&15])
			default:
				dst = append(dst, c)
			}
			i++
			runes++
			continue
		}
		size := utf8SeqLen(b[i:])
		if size == 0 {
			dst = append(dst, "�"...)
			i++
		} else {
			dst = append(dst, b[i:i+size]...)
			i += size
		}
		runes++
	}
	return dst, runes
}

// utf8SeqLen: the length of the valid UTF-8 sequence b starts with (0: not
// one). Byte-wise, so no string copy of a secret is ever made.
func utf8SeqLen(b []byte) int {
	c := b[0]
	var n int
	var min rune
	switch {
	case c&0xE0 == 0xC0:
		n, min = 2, 0x80
	case c&0xF0 == 0xE0:
		n, min = 3, 0x800
	case c&0xF8 == 0xF0:
		n, min = 4, 0x10000
	default:
		return 0
	}
	if len(b) < n {
		return 0
	}
	r := rune(c) & (0x7F >> n)
	for i := 1; i < n; i++ {
		if b[i]&0xC0 != 0x80 {
			return 0
		}
		r = r<<6 | rune(b[i]&0x3F)
	}
	if r < min || r > 0x10FFFF || (r >= 0xD800 && r <= 0xDFFF) {
		return 0
	}
	return n
}
