package engine

import (
	"testing"
	"time"
)

func TestDecideTypePrep(t *testing.T) {
	cases := []struct {
		s       typeScreen
		autoSas bool
		want    typePrepAction
		why     string
	}{
		{typeScreen{Desktop: "Default", Locked: 0, CredField: 1}, true, typePrepNone, "unlocked, the user's desktop: type as before"},
		{typeScreen{Desktop: "Default", Locked: -1, CredField: -1}, true, typePrepNone, "lock state unknown: type as before"},
		{typeScreen{Desktop: "Winlogon", Locked: 0, CredField: 0}, true, typePrepNone, "a UAC prompt (unlocked, no field): never a Ctrl+Alt+Del"},
		{typeScreen{Desktop: "Winlogon", Locked: 1, CredField: 1}, true, typePrepNone, "locked, the password field shows: type"},
		{typeScreen{Desktop: "winlogon", Locked: 1, CredField: -1}, true, typePrepNone, "locked, field unknown: type (no unjustified Ctrl+Alt+Del)"},
		{typeScreen{Desktop: "Winlogon", Locked: 1, CredField: 0}, true, typePrepSAS, "locked, no field (Press Ctrl+Alt+Del): SAS first"},
		{typeScreen{Desktop: "Default", Locked: 1, CredField: 0}, true, typePrepSAS, "locked, still on the user's desktop (curtain): SAS first"},
		{typeScreen{Desktop: "Screen-saver", Locked: 1, CredField: -1}, true, typePrepSAS, "locked behind a screen saver: SAS first"},
		{typeScreen{Desktop: "", Locked: 1, CredField: -1}, true, typePrepSAS, "locked, desktop unreadable: SAS first"},
		{typeScreen{Desktop: "Winlogon", Locked: 1, CredField: 0}, false, typePrepNone, "option off: never"},
		{typeScreen{Desktop: "Default", Locked: 1, CredField: 0}, false, typePrepNone, "option off: never"},
	}
	for _, c := range cases {
		if got := decideTypePrep(c.s, c.autoSas); got != c.want {
			t.Errorf("%s: %+v autoSas=%v -> %v, want %v", c.why, c.s, c.autoSas, got, c.want)
		}
	}
}

// fakePrep drives prepareTyping: screens is what each probe returns in turn
// (the last one repeats), a fake clock advanced by sleep.
type fakePrep struct {
	screens []typeScreen
	probes  int
	sas     int
	clock   time.Time
	slept   time.Duration
}

func (f *fakePrep) deps() typePrepDeps {
	return typePrepDeps{
		probe: func() typeScreen {
			i := f.probes
			if i >= len(f.screens) {
				i = len(f.screens) - 1
			}
			f.probes++
			return f.screens[i]
		},
		sendSAS: func() { f.sas++ },
		sleep:   func(d time.Duration) { f.clock = f.clock.Add(d); f.slept += d },
		now:     func() time.Time { return f.clock },
	}
}

func TestPrepareTypingUnlockedTypesNow(t *testing.T) {
	f := &fakePrep{screens: []typeScreen{{Desktop: "Default", Locked: 0, CredField: 1}}}
	prep, code, _ := prepareTyping(true, f.deps())
	if prep != "" || code != "" || f.sas != 0 || f.slept != 0 {
		t.Fatalf("unlocked: prep=%q code=%q sas=%d slept=%v", prep, code, f.sas, f.slept)
	}
}

func TestPrepareTypingOptionOffNeverSAS(t *testing.T) {
	f := &fakePrep{screens: []typeScreen{{Desktop: "Winlogon", Locked: 1, CredField: 0}}}
	prep, code, last := prepareTyping(false, f.deps())
	if prep != "" || code != "" || f.sas != 0 || f.probes != 1 || !last.secure() {
		t.Fatalf("autoSas off: prep=%q code=%q sas=%d probes=%d (one probe, for the field choice)", prep, code, f.sas, f.probes)
	}
}

func TestPrepareTypingSASThenField(t *testing.T) {
	f := &fakePrep{clock: time.Unix(1000, 0), screens: []typeScreen{
		{Desktop: "Winlogon", Locked: 1, CredField: 0}, // Press Ctrl+Alt+Del
		{Desktop: "Winlogon", Locked: 1, CredField: 0}, // still the curtain
		{Desktop: "Winlogon", Locked: 1, CredField: 0},
		{Desktop: "Winlogon", Locked: 1, CredField: 1}, // the password field
	}}
	prep, code, _ := prepareTyping(true, f.deps())
	if prep != typePrepSASSent || code != "" || f.sas != 1 {
		t.Fatalf("prep=%q code=%q sas=%d", prep, code, f.sas)
	}
	if f.slept != 3*typePrepPoll+typePrepSettle {
		t.Fatalf("waited %v, want 3 polls + settle", f.slept)
	}
	if f.probes != 5 {
		t.Fatalf("probes=%d, want 1 + 3 polls + 1 after the settle", f.probes)
	}
}

func TestPrepareTypingCurtainOnDefaultDesktop(t *testing.T) {
	f := &fakePrep{clock: time.Unix(1000, 0), screens: []typeScreen{
		{Desktop: "Default", Locked: 1, CredField: -1},
		{Desktop: "Default", Locked: 1, CredField: -1},
		{Desktop: "Winlogon", Locked: 1, CredField: -1}, // switched; caret unknown counts as ready
	}}
	prep, code, _ := prepareTyping(true, f.deps())
	if prep != typePrepSASSent || code != "" || f.sas != 1 {
		t.Fatalf("prep=%q code=%q sas=%d", prep, code, f.sas)
	}
}

func TestPrepareTypingNeverShowsFailsClearly(t *testing.T) {
	f := &fakePrep{clock: time.Unix(1000, 0), screens: []typeScreen{{Desktop: "Winlogon", Locked: 1, CredField: 0}}}
	prep, code, _ := prepareTyping(true, f.deps())
	if prep != typePrepSASSent || code != typePrepNoCredScreen || f.sas != 1 {
		t.Fatalf("prep=%q code=%q sas=%d", prep, code, f.sas)
	}
	if f.slept < typePrepWait || f.slept > typePrepWait+typePrepPoll {
		t.Fatalf("gave up after %v, want ~%v", f.slept, typePrepWait)
	}
}

func TestDomainInitValue(t *testing.T) {
	for st, want := range map[uint32]string{0: "", 1: "workgroup", 2: "workgroup", 3: "joined", 9: ""} {
		if got := domainInitValue(st); got != want {
			t.Errorf("status %d -> %q, want %q", st, got, want)
		}
	}
}

func TestWTSLockState(t *testing.T) {
	cases := []struct {
		flags int32
		win7  bool
		want  int
	}{
		{0, false, 1}, {1, false, 0}, {-1, false, -1},
		{0, true, 0}, {1, true, 1}, {-1, true, -1}, // Windows 7 / 2008 R2: swapped
	}
	for _, c := range cases {
		if got := wtsLockState(c.flags, c.win7); got != c.want {
			t.Errorf("flags=%d win7=%v -> %d, want %d", c.flags, c.win7, got, c.want)
		}
	}
}

func TestTypeTextAutoSasParsedAndResultPrep(t *testing.T) {
	f := ttFrame("a1", false, []map[string]interface{}{b64part("pw")})
	msg, code := parseTypeText(f)
	if code != "" || msg.AutoSAS {
		t.Fatalf("no autoSas field: off (an older Desktop): %+v %q", msg, code)
	}
	msg.wipe()
	raw := []byte(`{"type":"type_text","id":"a2","gen":1,"autoSas":true,"parts":[{"b64":"cHc="}]}`)
	msg, code = parseTypeText(raw)
	if code != "" || !msg.AutoSAS {
		t.Fatalf("autoSas:true not parsed: %+v %q", msg, code)
	}
	msg.wipe()
	if got := string(typeTextResultPrep("a2", true, "", typePrepSASSent)); got != `{"id":"a2","ok":true,"prep":"sas_sent","type":"type_text_result"}` {
		t.Fatalf("result with prep: %s", got)
	}
	if got := string(typeTextResultPrep("a2", false, typePrepNoCredScreen, typePrepSASSent)); got != `{"code":"no_credential_screen","id":"a2","ok":false,"prep":"sas_sent","type":"type_text_result"}` {
		t.Fatalf("failure with prep: %s", got)
	}
	if got := string(TypeTextResult("a2", true, "")); got != `{"id":"a2","ok":true,"type":"type_text_result"}` {
		t.Fatalf("plain result changed: %s", got)
	}
}
