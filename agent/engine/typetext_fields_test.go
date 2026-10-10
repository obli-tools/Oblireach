package engine

import (
	"strings"
	"testing"
	"time"
)

// ── The sign-in screen read by UI Automation (typetext_prep.go) ─────────────

// Screens as UI Automation reports them on Windows 10/11 (XAML LogonUI: no
// Win32 caret, CredField 0 even with a field focused — the original bug).
var (
	scrPressCAD    = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldNone}
	scrKnownTile   = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldPassword, PwShown: true}
	scrOtherUser   = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldUser, PwShown: true, UserShown: true, UserEmpty: 1}
	scrOtherTyped  = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldUser, PwShown: true, UserShown: true, UserEmpty: 0}
	scrPwEmptyUsr  = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldPassword, PwShown: true, UserShown: true, UserEmpty: 1}
	scrPwUsrFull   = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldPassword, PwShown: true, UserShown: true, UserEmpty: 0}
	scrNoFocusPw   = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldNone, PwShown: true}
	scrNoFocusBoth = typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldNone, PwShown: true, UserShown: true, UserEmpty: 1}
	scrUACYesNo    = typeScreen{Desktop: "Winlogon", Locked: 0, Focus: fieldNone}
	scrDesktop     = typeScreen{Desktop: "Default", Locked: 0, CredField: 1}
)

func TestDecideTypePrepUIA(t *testing.T) {
	cases := []struct {
		s    typeScreen
		want typePrepAction
		why  string
	}{
		{scrPressCAD, typePrepSAS, "Press Ctrl+Alt+Del: SAS"},
		{scrKnownTile, typePrepNone, "the password box has the focus (XAML, no caret): no SAS"},
		{scrOtherUser, typePrepNone, "Other user, the username box focused: no SAS (captured screen)"},
		{scrNoFocusPw, typePrepNone, "fields shown, nothing focused: no SAS (the focus is moved instead)"},
		{scrUACYesNo, typePrepNone, "unlocked secure desktop: never a SAS"},
	}
	for _, c := range cases {
		if got := decideTypePrep(c.s, true); got != c.want {
			t.Errorf("%s: %+v -> %v, want %v", c.why, c.s, got, c.want)
		}
	}
}

// A captured sign-in sequence: SAS sent, then the "Other user" screen comes up with
// the focus in the username box. The caret probe saw 0 throughout (XAML), so
// the old wait gave up; UI Automation sees the field.
func TestPrepareTypingCapturedOtherUser(t *testing.T) {
	f := &fakePrep{clock: time.Unix(1000, 0), screens: []typeScreen{scrPressCAD, scrPressCAD, scrOtherUser}}
	prep, code, last := prepareTyping(true, f.deps())
	if prep != typePrepSASSent || code != "" || f.sas != 1 || last.Focus != fieldUser {
		t.Fatalf("prep=%q code=%q sas=%d last=%+v", prep, code, f.sas, last)
	}
}

func TestPrepareTypingSettleRecheck(t *testing.T) {
	// Ready, then gone during the settle (an animation), then back: waits on.
	f := &fakePrep{clock: time.Unix(1000, 0), screens: []typeScreen{scrPressCAD, scrKnownTile, scrPressCAD, scrPressCAD, scrKnownTile}}
	prep, code, last := prepareTyping(true, f.deps())
	if prep != typePrepSASSent || code != "" || last.Focus != fieldPassword || f.probes < 6 {
		t.Fatalf("prep=%q code=%q last=%+v probes=%d", prep, code, last, f.probes)
	}
}

func TestPrepareTypingResendsLostSASOnce(t *testing.T) {
	curtain := typeScreen{Desktop: "Default", Locked: 1, CredField: 0}
	f := &fakePrep{clock: time.Unix(1000, 0), screens: []typeScreen{curtain}}
	prep, code, _ := prepareTyping(true, f.deps())
	if prep != typePrepSASSent || code != typePrepNoCredScreen || f.sas != 2 {
		t.Fatalf("prep=%q code=%q sas=%d (want the SAS sent, then once more)", prep, code, f.sas)
	}
}

func TestSessionLockState(t *testing.T) {
	cases := []struct{ wts, user, want int }{
		{1, 1, 1}, {0, 1, 0}, {-1, 1, -1}, {-1, -1, -1}, {0, -1, 0},
		{-1, 0, 1}, // nobody signed in: the sign-in screen counts as locked
		{0, 0, 1},  // "unlocked" with nobody signed in: still the sign-in screen
		{1, 0, 1},
	}
	for _, c := range cases {
		if got := sessionLockState(c.wts, c.user); got != c.want {
			t.Errorf("wts=%d user=%d -> %d, want %d", c.wts, c.user, got, c.want)
		}
	}
}

func TestCredShapeOf(t *testing.T) {
	u, pw := []byte("CONTOSO\\jsmith"), []byte("pw")
	cases := []struct {
		parts []typePart
		ok    bool
		user  bool
		enter bool
	}{
		{[]typePart{{Text: pw}}, true, false, false},
		{[]typePart{{Text: pw}, {Key: "Enter"}}, true, false, true},
		{[]typePart{{Text: u}, {Key: "Tab"}, {Text: pw}}, true, true, false},
		{[]typePart{{Text: u}, {Key: "Tab"}, {Text: pw}, {Key: "Enter"}}, true, true, true},
		{[]typePart{{Text: u}, {Text: pw}}, false, false, false},
		{[]typePart{{Key: "Tab"}, {Text: pw}}, false, false, false},
		{[]typePart{{Text: u}, {Key: "Enter"}, {Text: pw}}, false, false, false},
	}
	for i, c := range cases {
		got := credShapeOf(c.parts)
		if got.ok != c.ok || (got.user != nil) != c.user || got.enter != c.enter {
			t.Errorf("case %d: %+v", i, got)
		}
	}
}

func TestDecideTypeAction(t *testing.T) {
	cases := []struct {
		s        typeScreen
		haveUser bool
		want     typeAction
		why      string
	}{
		// (a) a known user's tile / (c) "locked by X": the password box.
		{scrKnownTile, false, actPwOnly, "known tile, password only"},
		{scrKnownTile, true, actPwOnly, "known tile, entry with a username: the tile names the user, password only"},
		// (b) "Other user": the username box focused.
		{scrOtherUser, true, actUserPw, "Other user + a username: username, Tab, password"},
		{scrOtherUser, false, actRefuseNoUser, "Other user, no username: nothing typed (never the password in clear)"},
		{scrOtherTyped, false, actFocusPw, "a username already typed by hand: the password field, the password"},
		{scrOtherTyped, true, actUserPw, "a username there, the entry's: replaced (Ctrl+A), then the password"},
		{typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldUser, UserShown: true, UserEmpty: -1, PwShown: true}, false, actRefuseNoUser, "username box, content unknown, no username: refused"},
		{typeScreen{Desktop: "Winlogon", Locked: 1, Focus: fieldUser, UserShown: true, UserEmpty: 0}, false, actRefuseNoUser, "username box filled but no password box shown: refused"},
		// the password box focused, the username box beside it empty.
		{scrPwEmptyUsr, true, actFocusUser, "empty username box beside: focus it, then username + password"},
		{scrPwEmptyUsr, false, actRefuseNoUser, "empty username box beside, no username: refused"},
		{scrPwUsrFull, false, actPwOnly, "username box filled: password only"},
		// nothing focused.
		{scrNoFocusPw, false, actFocusPw, "password box shown, unfocused: focus it"},
		{scrNoFocusBoth, true, actFocusUser, "both shown, username empty: focus the username box"},
		{scrNoFocusBoth, false, actRefuseNoUser, "both shown, username empty, none known: refused"},
		{scrUACYesNo, false, actAsIs, "UAC Yes/No: typed as before"},
		// not a sign-in screen UI Automation can read: as asked.
		{scrDesktop, true, actAsIs, "the user's desktop: as asked"},
		{typeScreen{Desktop: "Winlogon", Locked: 1, CredField: 1}, true, actAsIs, "UI Automation unknown: as before"},
	}
	for _, c := range cases {
		if got := decideTypeAction(c.s, true, c.haveUser); got != c.want {
			t.Errorf("%s: %+v user=%v -> %v, want %v", c.why, c.s, c.haveUser, got, c.want)
		}
	}
	if got := decideTypeAction(scrOtherUser, false, true); got != actAsIs {
		t.Errorf("an unknown parts shape is typed as asked, got %v", got)
	}
}

// fakeField drives runTypeSteps: screens per probe (the last repeats), the
// focus moves, what was typed; afterTab is what the probes see once a Tab
// was typed.
type fakeField struct {
	screens  []typeScreen
	probes   int
	focusOK  bool
	focused  []fieldKind
	typed    [][]string
	afterTab *typeScreen
	tabbed   bool
}

func (f *fakeField) deps() typeFieldDeps {
	return typeFieldDeps{
		probe: func() typeScreen {
			f.probes++
			if f.tabbed && f.afterTab != nil {
				return *f.afterTab
			}
			i := f.probes - 1
			if i >= len(f.screens) {
				i = len(f.screens) - 1
			}
			return f.screens[i]
		},
		focus: func(k fieldKind) bool {
			f.focused = append(f.focused, k)
			return f.focusOK
		},
		typ: func(parts []typePart) string {
			var out []string
			for _, p := range parts {
				if p.Key != "" {
					out = append(out, "key:"+p.Key)
					if p.Key == "Tab" {
						f.tabbed = true
					}
				} else {
					out = append(out, "text:"+string(p.Text))
				}
			}
			f.typed = append(f.typed, out)
			return ""
		},
		sleep: func(time.Duration) {},
	}
}

func joinTyped(t [][]string) string {
	var s []string
	for _, step := range t {
		s = append(s, "["+strings.Join(step, " ")+"]")
	}
	return strings.Join(s, "")
}

func credMsg(user string, withUserPart, enter bool, frameUser string) *typeTextMsg {
	m := &typeTextMsg{ID: "a1", Gen: 1}
	if withUserPart {
		m.Parts = append(m.Parts, typePart{Text: []byte(user)}, typePart{Key: "Tab"})
	}
	m.Parts = append(m.Parts, typePart{Text: []byte("S3cr3t")})
	if enter {
		m.Parts = append(m.Parts, typePart{Key: "Enter"})
	}
	if frameUser != "" {
		m.User = []byte(frameUser)
	}
	return m
}

func TestRunTypeStepsOtherUserWithUsername(t *testing.T) {
	pwFocused := scrOtherUser
	pwFocused.Focus = fieldPassword
	f := &fakeField{screens: []typeScreen{scrOtherUser}, afterTab: &pwFocused}
	// The "password only" button; the entry has a username (frame "user").
	if code := runTypeSteps(scrOtherUser, credMsg("", false, true, "CONTOSO\\jsmith"), f.deps()); code != "" {
		t.Fatalf("code %q", code)
	}
	if got := joinTyped(f.typed); got != "[key:SelectAll text:CONTOSO\\jsmith key:Tab][text:S3cr3t key:Enter]" {
		t.Fatalf("typed %s", got)
	}
}

func TestRunTypeStepsOtherUserPasswordNeverInClear(t *testing.T) {
	// After username + Tab the focus is NOT on a password box: the password
	// is not typed.
	stay := scrOtherTyped
	f := &fakeField{screens: []typeScreen{scrOtherUser}, afterTab: &stay}
	code := runTypeSteps(scrOtherUser, credMsg("jsmith", true, true, ""), f.deps())
	if code != typePwFieldMissing {
		t.Fatalf("code %q", code)
	}
	if got := joinTyped(f.typed); strings.Contains(got, "S3cr3t") {
		t.Fatalf("the password was typed: %s", got)
	}
}

func TestRunTypeStepsOtherUserNoUsernameRefused(t *testing.T) {
	f := &fakeField{screens: []typeScreen{scrOtherUser}}
	if code := runTypeSteps(scrOtherUser, credMsg("", false, true, ""), f.deps()); code != typeUsernameRequired {
		t.Fatalf("code %q", code)
	}
	if len(f.typed) != 0 {
		t.Fatalf("typed %s", joinTyped(f.typed))
	}
}

func TestRunTypeStepsKnownTileDropsUsername(t *testing.T) {
	f := &fakeField{screens: []typeScreen{scrKnownTile}}
	if code := runTypeSteps(scrKnownTile, credMsg("jsmith", true, true, ""), f.deps()); code != "" {
		t.Fatalf("code %q", code)
	}
	if got := joinTyped(f.typed); got != "[text:S3cr3t key:Enter]" {
		t.Fatalf("typed %s", got)
	}
}

func TestRunTypeStepsFocusMoved(t *testing.T) {
	// Nothing focused, the password box shown: focus it, check, type.
	f := &fakeField{screens: []typeScreen{scrKnownTile}, focusOK: true}
	if code := runTypeSteps(scrNoFocusPw, credMsg("", false, false, ""), f.deps()); code != "" {
		t.Fatalf("code %q", code)
	}
	if len(f.focused) != 1 || f.focused[0] != fieldPassword || joinTyped(f.typed) != "[text:S3cr3t]" {
		t.Fatalf("focused %v typed %s", f.focused, joinTyped(f.typed))
	}
	// SetFocus "worked" but the focus did not move: nothing typed.
	f = &fakeField{screens: []typeScreen{scrNoFocusPw}, focusOK: true}
	if code := runTypeSteps(scrNoFocusPw, credMsg("", false, false, ""), f.deps()); code != typeFieldFocusFailed || len(f.typed) != 0 {
		t.Fatalf("code %q typed %s", code, joinTyped(f.typed))
	}
	// SetFocus refused.
	f = &fakeField{screens: []typeScreen{scrKnownTile}, focusOK: false}
	if code := runTypeSteps(scrNoFocusPw, credMsg("", false, false, ""), f.deps()); code != typeFieldFocusFailed || len(f.typed) != 0 {
		t.Fatalf("SetFocus refused: code %q typed %s", code, joinTyped(f.typed))
	}
	// The empty username box beside a focused password box: focus it, type all.
	pwFocused := scrOtherUser
	pwFocused.Focus = fieldPassword
	f = &fakeField{screens: []typeScreen{scrOtherUser}, focusOK: true, afterTab: &pwFocused}
	if code := runTypeSteps(scrPwEmptyUsr, credMsg("jsmith", true, false, ""), f.deps()); code != "" {
		t.Fatalf("code %q", code)
	}
	if len(f.focused) != 1 || f.focused[0] != fieldUser || joinTyped(f.typed) != "[key:SelectAll text:jsmith key:Tab][text:S3cr3t]" {
		t.Fatalf("focused %v typed %s", f.focused, joinTyped(f.typed))
	}
}

func TestRunTypeStepsAsIsOffTheSignInScreen(t *testing.T) {
	f := &fakeField{screens: []typeScreen{scrDesktop}}
	if code := runTypeSteps(scrDesktop, credMsg("jsmith", true, true, "x"), f.deps()); code != "" {
		t.Fatalf("code %q", code)
	}
	if got := joinTyped(f.typed); got != "[text:jsmith key:Tab text:S3cr3t key:Enter]" || f.probes != 0 {
		t.Fatalf("typed %s probes %d", got, f.probes)
	}
}

func TestRunTypeStepsFocusLostBeforePassword(t *testing.T) {
	// The probe before the password-only step no longer sees a password box.
	f := &fakeField{screens: []typeScreen{scrPressCAD}}
	if code := runTypeSteps(scrKnownTile, credMsg("", false, true, ""), f.deps()); code != typePwFieldMissing || len(f.typed) != 0 {
		t.Fatalf("code %q typed %s", code, joinTyped(f.typed))
	}
}

func TestTypeTextUserFieldParsed(t *testing.T) {
	raw := []byte(`{"type":"type_text","id":"a3","gen":1,"user":"Q09OVE9TT1xqc21pdGg=","parts":[{"b64":"cHc="}]}`)
	msg, code := parseTypeText(raw)
	if code != "" || string(msg.User) != "CONTOSO\\jsmith" {
		t.Fatalf("user not parsed: %q %q", msg.User, code)
	}
	u := msg.User
	msg.wipe()
	if msg.User != nil || u[0] != 0 {
		t.Fatal("wipe did not clear the username")
	}
	for _, bad := range []string{
		`{"type":"type_text","id":"a3","gen":1,"user":"YQli","parts":[{"b64":"cHc="}]}`, // a\tb
		`{"type":"type_text","id":"a3","gen":1,"user":"/w==","parts":[{"b64":"cHc="}]}`, // invalid UTF-8
	} {
		if _, code := parseTypeText([]byte(bad)); code != "invalid" {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, code := parseTypeText([]byte(`{"type":"type_text","id":"a3","gen":1,"parts":[{"key":"SelectAll"}]}`)); code != "invalid" {
		t.Error("the internal SelectAll key must not be accepted from the wire")
	}
}
