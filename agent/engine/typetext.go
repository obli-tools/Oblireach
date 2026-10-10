package engine

// type_text — the vault's "type this saved secret on the remote machine"
// message (ObliReach password vault, Oblireach Desktop). The viewer never
// sends the operator's own keystrokes here; a type_text frame carries a small
// script of text parts and Tab/Enter keys, which the agent types with the
// REMOTE keyboard layout. The text parts are base64 so the agent decodes them
// straight into a []byte it can wipe, and the secret is NEVER logged (not its
// content, its length, or the keys it produced — no keystroke logging here either).
//
// This file is PURE Go: the parsing, validation, per-stream rate limiting and
// the result framing are unit-tested (typetext_test.go) without Windows. The
// keystroke planning and the actual typing live in typetext_windows.go.
//
// Wire shape (viewer → agent):
//
//	{"type":"type_text","id":"<=32 [A-Za-z0-9_-]>","secureOnly":true,"gen":<int>,
//	 "parts":[{"b64":"<base64 UTF-8>"},{"key":"Tab"},{"b64":"..."},{"key":"Enter"}]}
//
//	- parts: 1..6; a part is EXACTLY one of b64 or key; key is "Tab" or "Enter".
//	- decoded text: valid UTF-8, no control characters, <=128 runes per part,
//	  <=256 runes across all text parts.
//	- gen: MANDATORY, the init/session_state gen the viewer acted on when the
//	  operator clicked the vault entry. Rejected with session_changed when it
//	  no longer matches the agent's current view (a session/leg switch since).
//	- autoSas: optional (featTypeTextSAS). true: when the session is locked
//	  and its password field is not showing, Ctrl+Alt+Del is sent first and
//	  the credential screen awaited (typetext_prep.go). Absent / false: typed
//	  as before.
//	- user: optional, base64 UTF-8 (same bounds as a text part). The vault
//	  entry's username, sent apart from the parts when they do not already
//	  carry it: the agent types it ONLY when the sign-in screen's focused
//	  field is the username field (typetext_prep.go decideTypeAction).
//	  Ignored by an older agent.
//
// Result (agent → viewer):
//
//	{"type":"type_text_result","id":"...","ok":true[,"prep":"sas_sent"]}
//	{"type":"type_text_result","id":"...","ok":false,"code":"invalid|rate_limited|
//	  busy|not_secure_desktop|session_changed|interrupted|failed|
//	  no_credential_screen|username_required|field_focus_failed|
//	  password_field_missing"[,"prep":"sas_sent"]}
//
// prep says what was done before typing (Ctrl+Alt+Del). The result never
// carries a length or any content.

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	typeTextMaxParts      = 6
	typeTextMaxRunesPart  = 128
	typeTextMaxRunesTotal = 256
	typeTextMaxID         = 32
)

// featTypeText / featInputDesktop are advertised in init.features only where
// the typing path exists (Windows helper, typeTextSupported). A viewer offers
// the vault key button purely from featTypeText, and highlights it on the
// login / lock / UAC screen from featInputDesktop.
const (
	featTypeText     = "type_text"
	featInputDesktop = "input_desktop"
)

// typeTextMark is the byte pre-check that keeps the relay and dispatch paths
// from JSON-parsing every mouse move looking for a type_text frame.
var typeTextMark = []byte(`type_text`)

// IsTypeTextMsg reports whether a viewer text frame is a type_text message.
func IsTypeTextMsg(payload []byte) bool {
	if !bytes.Contains(payload, typeTextMark) {
		return false
	}
	var peek struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &peek) == nil && peek.Type == "type_text"
}

// typePart is one decoded step: exactly one of Text (a run of characters, held
// as raw bytes so it can be wiped) or Key ("Tab" / "Enter"; typeKeySelectAll
// is internal only: the agent's own Ctrl+A before a username, never accepted
// from the wire).
type typePart struct {
	Text []byte
	Key  string
}

// typeTextMsg is a parsed, validated type_text frame.
type typeTextMsg struct {
	ID         string
	SecureOnly bool
	Gen        int
	AutoSAS    bool // Ctrl+Alt+Del first when the session is locked (typetext_prep.go)
	Parts      []typePart
	User       []byte // the entry's username for a sign-in screen that asks for one (optional)
}

// typeKeySelectAll: Ctrl+A, typed by the agent itself before a username so a
// field already holding text is replaced, not appended to.
const typeKeySelectAll = "SelectAll"

// wipe zeroes every decoded secret byte this message holds.
func (m *typeTextMsg) wipe() {
	for i := range m.Parts {
		WipeBytes(m.Parts[i].Text)
		m.Parts[i].Text = nil
	}
	WipeBytes(m.User)
	m.User = nil
}

// WipeBytes zeroes a byte slice in place (best effort; a plaintext scratch is
// never left in a buffer once we are done with it).
func WipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// parseTypeText decodes and validates a type_text frame. It returns the parsed
// message and an empty code, or a zero message and "invalid". Anything not
// exactly matching the contract is rejected (defence in depth: the secret came
// from the operator's own vault, but the wire is still validated strictly).
func parseTypeText(payload []byte) (typeTextMsg, string) {
	var raw struct {
		ID         string `json:"id"`
		SecureOnly bool   `json:"secureOnly"`
		Gen        *int   `json:"gen"`
		AutoSAS    bool   `json:"autoSas"`
		// Wipeable copy: B64 decodes straight into a
		// fresh []byte (encoding/json base64-decodes a JSON string into a []byte
		// field natively) instead of first landing in an immutable Go string
		// that base64.StdEncoding.DecodeString would then copy from — that
		// string could never be wiped and would sit in the SYSTEM helper's heap
		// for as long as the GC keeps it, recoverable from a crash dump.
		Parts []struct {
			B64 []byte  `json:"b64"`
			Key *string `json:"key"`
		} `json:"parts"`
		User []byte `json:"user"`
	}
	if json.Unmarshal(payload, &raw) != nil {
		return typeTextMsg{}, "invalid"
	}
	if !validTypeTextID(raw.ID) {
		return typeTextMsg{}, "invalid"
	}
	// Bound to the view generation: gen is mandatory, not
	// merely optional-and-checked-when-present — a frame with no gen at all
	// must never be typed blind. The value is checked against the current
	// view in handleTypeText (typetext_windows.go), the same guard ordinary
	// key/mouse events go through in dispatchInputJSON (stream.go).
	if raw.Gen == nil {
		return typeTextMsg{}, "invalid"
	}
	if len(raw.Parts) == 0 || len(raw.Parts) > typeTextMaxParts {
		return typeTextMsg{}, "invalid"
	}
	msg := typeTextMsg{ID: raw.ID, SecureOnly: raw.SecureOnly, Gen: *raw.Gen, AutoSAS: raw.AutoSAS}
	// user (optional): an empty one is no username; else the bounds of a
	// text part. It does not count toward the parts' total (typed instead of
	// nothing, never in addition to a username part: decideTypeAction).
	if len(raw.User) > 0 {
		u := raw.User
		if !utf8.Valid(u) || hasControlBytes(u) || utf8.RuneCount(u) > typeTextMaxRunesPart {
			WipeBytes(u)
			for _, p := range raw.Parts {
				WipeBytes(p.B64)
			}
			return typeTextMsg{}, "invalid"
		}
		msg.User = u
	}
	total := 0
	for _, p := range raw.Parts {
		switch {
		case p.B64 != nil && p.Key == nil:
			dec := p.B64
			if !utf8.Valid(dec) || hasControlBytes(dec) {
				WipeBytes(dec)
				msg.wipe()
				return typeTextMsg{}, "invalid"
			}
			n := utf8.RuneCount(dec)
			if n == 0 || n > typeTextMaxRunesPart {
				WipeBytes(dec)
				msg.wipe()
				return typeTextMsg{}, "invalid"
			}
			total += n
			if total > typeTextMaxRunesTotal {
				WipeBytes(dec)
				msg.wipe()
				return typeTextMsg{}, "invalid"
			}
			msg.Parts = append(msg.Parts, typePart{Text: dec})
		case p.Key != nil && p.B64 == nil:
			if *p.Key != "Tab" && *p.Key != "Enter" {
				msg.wipe()
				return typeTextMsg{}, "invalid"
			}
			msg.Parts = append(msg.Parts, typePart{Key: *p.Key})
		default:
			msg.wipe()
			return typeTextMsg{}, "invalid"
		}
	}
	return msg, ""
}

// validTypeTextID: 1..32 bytes of [A-Za-z0-9_-].
func validTypeTextID(id string) bool {
	if len(id) == 0 || len(id) > typeTextMaxID {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// hasControlBytes reports any C0 (except none are allowed) or DEL/C1 control
// character in valid UTF-8 text. Tab and newline are control characters too and
// are refused in a text part — Tab/Enter travel as their own key parts.
func hasControlBytes(b []byte) bool {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
		i += size
	}
	return false
}

// PeekTypeTextID reads just the id so a result can name the request even when
// the rest is invalid. Returns "" for an absent or malformed id.
func PeekTypeTextID(payload []byte) string {
	var peek struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(payload, &peek)
	if validTypeTextID(peek.ID) {
		return peek.ID
	}
	return ""
}

// ── Result framing ──────────────────────────────────────────────────────────

// TypeTextResult builds a type_text_result frame. It carries the id and the
// outcome only — never a length, never any content.
func TypeTextResult(id string, ok bool, code string) []byte {
	return typeTextResultPrep(id, ok, code, "")
}

// typeTextResultPrep is TypeTextResult saying what was done before typing
// (prep: "sas_sent"; "" = nothing, the field is then absent).
func typeTextResultPrep(id string, ok bool, code, prep string) []byte {
	m := map[string]interface{}{"type": "type_text_result", "id": id, "ok": ok}
	if !ok && code != "" {
		m["code"] = code
	}
	if prep != "" {
		m["prep"] = prep
	}
	b, _ := json.Marshal(m)
	return b
}

// inputDesktopMsg builds an input_desktop frame (secure = the login / lock /
// UAC screen holds input).
func inputDesktopMsg(secure bool) []byte {
	b, _ := json.Marshal(map[string]interface{}{"type": "input_desktop", "secure": secure})
	return b
}

// ── Per-stream rate limit ────────────────────────────────────────────────────
// One type_text in flight at a time, at least 1 s between two, at most 20 in a
// rolling minute. One helper serves one stream, so a package-level limiter is
// the per-stream limiter.

const (
	typeMinInterval = 1 * time.Second
	typeMaxPerMin   = 20
)

// typeNow is the limiter's clock (tests override it).
var typeNow = time.Now

type typeLimiter struct {
	mu       sync.Mutex
	inFlight bool
	last     time.Time
	recent   []time.Time // sends within the last minute
}

var globalTypeLimiter typeLimiter

// begin reserves the single in-flight slot. It returns "" when the caller may
// proceed (and MUST call done()), or a code: "busy" (one already running) or
// "rate_limited" (too soon, or too many this minute).
func (l *typeLimiter) begin() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := typeNow()
	if l.inFlight {
		return "busy"
	}
	if !l.last.IsZero() && now.Sub(l.last) < typeMinInterval {
		return "rate_limited"
	}
	// Drop samples older than a minute, then count.
	cut := now.Add(-time.Minute)
	kept := l.recent[:0]
	for _, t := range l.recent {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.recent = kept
	if len(l.recent) >= typeMaxPerMin {
		return "rate_limited"
	}
	l.inFlight = true
	l.last = now
	l.recent = append(l.recent, now)
	return ""
}

func (l *typeLimiter) done() {
	l.mu.Lock()
	l.inFlight = false
	l.mu.Unlock()
}
