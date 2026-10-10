package engine

// typetext_prep.go — before typing a vault secret: is the remote session
// ready for a password, and WHICH field will receive what is typed? (Detect
// whether the session is already unlocked / ready for a password; otherwise
// send Ctrl+Alt+Del before typing. The domain
// "Other user" screen shows a username field AND a password field, the focus
// in the username one.) PURE Go: the decisions and the wait are unit-tested
// with fakes (typetext_prep_test.go); the probes (input desktop, WTS lock
// state, UI Automation of the sign-in screen, the caret fallback) and the SAS
// live in typetext_windows.go / typetext_uia_windows.go.
//
// Why UI Automation: the first version decided "the password field shows"
// from the Win32 caret of LogonUI's foreground thread (GetGUIThreadInfo
// hwndCaret). The Windows 10/11/Server 2016+ sign-in screen is XAML: its
// text boxes draw their own caret and create no Win32 one, so the probe
// read "no field" on a screen that had one — Ctrl+Alt+Del was sent, the
// wait never saw the field, "no_credential_screen", nothing typed (seen
// on a real sign-in screen). UI Automation (what Narrator reads on the same
// screen) tells the focused element's type and whether it is a password
// box. The caret stays as the fallback when UI Automation cannot answer.
//
// Ctrl+Alt+Del (decideTypePrep, asked per request: type_text "autoSas"):
//   - not locked, or lock state unknown → none (the user's desktop, a UAC
//     prompt: nothing to unlock). No user in the session (the sign-in screen
//     of a machine nobody is signed in to) counts as locked;
//   - locked, the input on the Winlogon desktop and a credential field there
//     (focused, or shown) → none;
//   - locked, Winlogon, no field ("Press Ctrl+Alt+Del to unlock") → SAS;
//   - locked, the input on another desktop (the lock curtain, a screen
//     saver) → SAS.
// After the SAS: wait (up to typePrepWait, a probe every typePrepPoll, the
// SAS sent once more if the desktop has not even switched after
// typePrepResendAfter) for a credential field, then a settle and a last
// probe; never there → "no_credential_screen", nothing typed.
//
// Which field (decideTypeAction, on the Winlogon desktop with a UI
// Automation answer; anywhere else: typed as the viewer asked):
//   - the password field has the focus (a known user's tile, "locked by X",
//     the second step) → the password alone (+ Enter when asked), even when
//     the viewer asked username + Tab + password: the tile already names the
//     user. But an EMPTY username field shown beside it → the focus is moved
//     there and username + Tab + password are typed (no username → refused);
//   - the username field has the focus ("Other user", a domain machine) →
//     with a username (the entry's): Ctrl+A, username, Tab, then — only once
//     the focus is confirmed on a password field — the password. Without a
//     username: the field already filled → the focus moved to the password
//     field, the password alone; empty → "username_required", nothing typed
//     (never a password into a field that shows it in clear);
//   - no field focused but fields shown → the focus is moved to the right one
//     first (as above); a focus that does not move → "field_focus_failed".

import (
	"strings"
	"time"
)

// featTypeTextSAS: this agent honours type_text "autoSas" (Windows helper).
const featTypeTextSAS = "type_text_sas"

// fieldKind: what UI Automation says has the keyboard focus.
type fieldKind int

const (
	fieldUnknown  fieldKind = iota // UI Automation could not tell (fallback: the caret)
	fieldNone                      // the focus is on no text field (a tile, a button, nothing)
	fieldPassword                  // a password box
	fieldUser                      // a plain text box (the sign-in screen's username)
)

// typeScreen is what the probes saw.
type typeScreen struct {
	Desktop   string // the input desktop's name ("Winlogon", "Default", "Screen-saver"; "" unknown)
	Locked    int    // 1 locked (or no user signed in), 0 unlocked, -1 unknown
	CredField int    // caret fallback: 1 a field has the caret, 0 none, -1 unknown
	// UI Automation (Winlogon desktop only; Focus fieldUnknown elsewhere or
	// when it cannot answer — the other fields then mean nothing).
	Focus     fieldKind
	PwShown   bool // a password box is shown (enabled, on screen)
	UserShown bool // a plain text box is shown
	UserEmpty int  // that text box: 1 empty, 0 holds text, -1 unknown
}

func (s typeScreen) secure() bool { return strings.EqualFold(s.Desktop, "Winlogon") }

// credShows: the sign-in screen offers a field to type into.
func (s typeScreen) credShows() bool {
	if s.Focus != fieldUnknown {
		return s.Focus == fieldPassword || s.Focus == fieldUser || s.PwShown || s.UserShown
	}
	return s.CredField != 0 // unknown caret counts as shown: never an unjustified Ctrl+Alt+Del
}

type typePrepAction int

const (
	typePrepNone typePrepAction = iota // type now
	typePrepSAS                        // Ctrl+Alt+Del first, then wait for the field
)

func decideTypePrep(s typeScreen, autoSas bool) typePrepAction {
	if !autoSas || s.Locked != 1 {
		return typePrepNone
	}
	if s.secure() && s.credShows() {
		return typePrepNone
	}
	return typePrepSAS
}

// typeScreenReady: after Ctrl+Alt+Del, the credential screen is up.
func typeScreenReady(s typeScreen) bool { return s.secure() && s.credShows() }

const (
	typePrepWait        = 10 * time.Second
	typePrepPoll        = 150 * time.Millisecond
	typePrepSettle      = 400 * time.Millisecond
	typePrepResendAfter = 3 * time.Second // still not on the Winlogon desktop: the SAS was lost
	typeFieldSettle     = 200 * time.Millisecond
)

// typePrepDeps: the probes and the clock (fakes in tests).
type typePrepDeps struct {
	probe   func() typeScreen
	sendSAS func()
	sleep   func(time.Duration)
	now     func() time.Time
}

// Results: prep is what was done first ("" or "sas_sent", reported to the
// viewer with the result); code is "" (go on and type) or a failure code.
const (
	typePrepSASSent      = "sas_sent"
	typePrepNoCredScreen = "no_credential_screen"
	typeUsernameRequired = "username_required"      // the screen asks a username, the entry has none
	typeFieldFocusFailed = "field_focus_failed"     // the focus could not be put in the right field
	typePwFieldMissing   = "password_field_missing" // after username + Tab, no password field had the focus
)

// prepareTyping probes the screen and, when decideTypePrep says so, sends
// Ctrl+Alt+Del and waits for the credential screen. It never types. last is
// the screen to type into (the last probe), for decideTypeAction.
func prepareTyping(autoSas bool, d typePrepDeps) (prep, code string, last typeScreen) {
	s := d.probe()
	if decideTypePrep(s, autoSas) != typePrepSAS {
		return "", "", s
	}
	d.sendSAS()
	start := d.now()
	deadline := start.Add(typePrepWait)
	resent := false
	for {
		d.sleep(typePrepPoll)
		s = d.probe()
		if typeScreenReady(s) {
			d.sleep(typePrepSettle)
			if s2 := d.probe(); typeScreenReady(s2) { // the focus may have moved during the settle
				return typePrepSASSent, "", s2
			}
		}
		if !resent && !s.secure() && !d.now().Before(start.Add(typePrepResendAfter)) {
			resent = true
			d.sendSAS()
		}
		if !d.now().Before(deadline) {
			return typePrepSASSent, typePrepNoCredScreen, s
		}
	}
}

// ── Which field receives what ──────────────────────────────────────────────

// credShape: the parts the Desktop builds (client-app vault_typetext.go),
// recognised: [secret (Enter)] or [username Tab secret (Enter)]. ok false:
// another shape (then typed as asked, untouched).
type credShape struct {
	ok    bool
	user  []byte // the username part (nil: none) — slices of the message, not copies
	pw    []byte
	enter bool
}

func credShapeOf(parts []typePart) credShape {
	var c credShape
	p := parts
	if n := len(p); n > 0 && p[n-1].Key == "Enter" {
		c.enter = true
		p = p[:n-1]
	}
	switch {
	case len(p) == 1 && p[0].Text != nil:
		c.pw = p[0].Text
	case len(p) == 3 && p[0].Text != nil && p[1].Key == "Tab" && p[2].Text != nil:
		c.user, c.pw = p[0].Text, p[2].Text
	default:
		return credShape{}
	}
	c.ok = true
	return c
}

type typeAction int

const (
	actAsIs         typeAction = iota // the parts as the viewer sent them (not a sign-in screen we can read)
	actPwOnly                         // the password (+ Enter) into the focused password field
	actUserPw                         // Ctrl+A, username, Tab, (focus checked) password (+ Enter)
	actFocusUser                      // move the focus to the username field, then actUserPw
	actFocusPw                        // move the focus to the password field, then actPwOnly
	actRefuseNoUser                   // the screen needs a username, none known: nothing typed
)

// decideTypeAction: what to type where, from the screen and whether a
// username is known (the parts' own, or the frame's "user").
func decideTypeAction(s typeScreen, shapeOK, haveUser bool) typeAction {
	if !s.secure() || s.Focus == fieldUnknown || !shapeOK {
		return actAsIs
	}
	userEmpty := s.UserShown && s.UserEmpty == 1
	switch s.Focus {
	case fieldUser:
		if haveUser {
			return actUserPw
		}
		if s.UserEmpty == 0 && s.PwShown {
			return actFocusPw // a username is already there: only the password is missing
		}
		return actRefuseNoUser
	case fieldPassword:
		if userEmpty {
			if haveUser {
				return actFocusUser
			}
			return actRefuseNoUser
		}
		return actPwOnly
	}
	// fieldNone: nothing focused that takes text.
	switch {
	case userEmpty || (s.UserShown && !s.PwShown):
		if haveUser {
			return actFocusUser
		}
		return actRefuseNoUser
	case s.PwShown:
		return actFocusPw
	}
	return actAsIs // no field at all (a UAC Yes/No prompt): as before
}

// typeStep is one typeText call of an action: its parts, and the field the
// focus must be on before it runs (fieldUnknown: no check).
type typeStep struct {
	parts []typePart
	need  fieldKind
}

// planTypeSteps turns an action into the typeText calls, in order. user is
// the username to type (the parts' own, else the frame's). The parts are
// slices of the message's own buffers (msg.wipe clears them all).
func planTypeSteps(act typeAction, c credShape, user []byte, asIs []typePart) []typeStep {
	pw := []typePart{{Text: c.pw}}
	if c.enter {
		pw = append(pw, typePart{Key: "Enter"})
	}
	switch act {
	case actPwOnly, actFocusPw:
		return []typeStep{{parts: pw, need: fieldPassword}}
	case actUserPw, actFocusUser:
		return []typeStep{
			{parts: []typePart{{Key: typeKeySelectAll}, {Text: user}, {Key: "Tab"}}, need: fieldUser},
			{parts: pw, need: fieldPassword},
		}
	case actRefuseNoUser:
		return nil
	}
	return []typeStep{{parts: asIs, need: fieldUnknown}}
}

// typeFieldDeps: what runTypeSteps needs (fakes in tests).
type typeFieldDeps struct {
	probe func() typeScreen
	focus func(fieldKind) bool // move the focus to that field (UI Automation SetFocus)
	typ   func([]typePart) string
	sleep func(time.Duration)
}

// runTypeSteps decides and types. Returns "" (typed) or a code. The focus is
// checked by a fresh probe before each step bound to a field: a password is
// never typed unless a password box has the focus.
func runTypeSteps(s typeScreen, msg *typeTextMsg, d typeFieldDeps) string {
	c := credShapeOf(msg.Parts)
	user := c.user
	if len(user) == 0 {
		user = msg.User
	}
	act := decideTypeAction(s, c.ok, len(user) > 0)
	if act == actRefuseNoUser {
		return typeUsernameRequired
	}
	switch act {
	case actFocusUser, actFocusPw:
		want := fieldUser
		if act == actFocusPw {
			want = fieldPassword
		}
		if !d.focus(want) {
			return typeFieldFocusFailed
		}
		d.sleep(typeFieldSettle)
	}
	for i, st := range planTypeSteps(act, c, user, msg.Parts) {
		if st.need != fieldUnknown {
			if i > 0 {
				d.sleep(typeFieldSettle) // the Tab's focus move
			}
			if got := d.probe(); !got.secure() || got.Focus != st.need {
				switch {
				case i > 0:
					return typePwFieldMissing
				case act == actFocusUser || act == actFocusPw:
					return typeFieldFocusFailed
				case st.need == fieldPassword:
					return typePwFieldMissing
				}
				return typeFieldFocusFailed
			}
		}
		if rc := d.typ(st.parts); rc != "" {
			return rc
		}
	}
	return ""
}

// domainInitValue maps NetGetJoinInformation's NETSETUP_JOIN_STATUS onto
// init.domain: "joined" (NetSetupDomainName), "workgroup" (unjoined or a
// workgroup), "" unknown (the viewer then follows the vault entry).
func domainInitValue(status uint32) string {
	switch status {
	case 3:
		return "joined"
	case 1, 2:
		return "workgroup"
	}
	return ""
}

// sessionLockState: the WTS lock state, where a session with no signed-in
// user (the sign-in screen nobody has passed: "Other user", "Press
// Ctrl+Alt+Del to sign in") counts as locked. hasUser: 1 yes, 0 no, -1 unknown.
func sessionLockState(wts, hasUser int) int {
	if wts != 1 && hasUser == 0 {
		return 1
	}
	return wts
}

// wtsLockState maps WTSINFOEX_LEVEL1.SessionFlags onto 1 locked / 0
// unlocked / -1 unknown. Windows 7 / Server 2008 R2 report the two values
// swapped (a documented bug fixed in Windows 8): win7 says to swap them back.
func wtsLockState(flags int32, win7 bool) int {
	const lock, unlock = 0, 1 // WTS_SESSIONSTATE_LOCK / _UNLOCK
	switch flags {
	case lock:
		if win7 {
			return 0
		}
		return 1
	case unlock:
		if win7 {
			return 1
		}
		return 0
	}
	return -1
}
