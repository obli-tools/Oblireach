package engine

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestShapeDisplayModesUniqueSortedHighestRefresh(t *testing.T) {
	cur := rawDisplayMode{W: 1280, H: 720, Hz: 60, Bpp: 32}
	raw := []rawDisplayMode{
		{W: 640, H: 480, Hz: 60, Bpp: 32},
		{W: 1920, H: 1080, Hz: 60, Bpp: 32},
		{W: 1920, H: 1080, Hz: 144, Bpp: 32},
		{W: 1920, H: 1080, Hz: 240, Bpp: 16},                   // other depth: ignored
		{W: 1920, H: 1080, Hz: 165, Bpp: 32, Interlaced: true}, // interlaced: ignored
		{W: 1280, H: 720, Hz: 60, Bpp: 32},
		{W: 1280, H: 720, Hz: 120, Bpp: 32}, // current W×H keeps the current Hz
		{W: 1280, H: 1024, Hz: 75, Bpp: 32},
		{W: 2560, H: 1440, Hz: 60, Bpp: 32},
		{W: 320, H: 200, Hz: 70, Bpp: 32}, // too small
		{W: 1920, H: 1080, Hz: 120, Bpp: 32},
	}
	got := shapeDisplayModes(raw, cur)
	want := []displayMode{
		{W: 2560, H: 1440, Hz: 60},
		{W: 1920, H: 1080, Hz: 144},
		{W: 1280, H: 1024, Hz: 75},
		{W: 1280, H: 720, Hz: 60, Current: true},
		{W: 640, H: 480, Hz: 60},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestShapeDisplayModesAlwaysIncludesCurrent(t *testing.T) {
	// A current mode Windows does not enumerate (or an empty enumeration)
	// still appears, marked current.
	cur := rawDisplayMode{W: 1024, H: 768, Hz: 60, Bpp: 32}
	got := shapeDisplayModes(nil, cur)
	if len(got) != 1 || !got[0].Current || got[0].W != 1024 || got[0].H != 768 || got[0].Hz != 60 {
		t.Fatalf("got %+v", got)
	}
	got = shapeDisplayModes([]rawDisplayMode{{W: 1920, H: 1080, Hz: 60, Bpp: 32}}, cur)
	if len(got) != 2 || got[0].W != 1920 || got[0].Current || !got[1].Current {
		t.Fatalf("got %+v", got)
	}
}

func TestShapeDisplayModesPortrait(t *testing.T) {
	// Rotated a quarter: the list is expressed portrait like the current mode.
	cur := rawDisplayMode{W: 1080, H: 1920, Hz: 60, Bpp: 32, Orientation: 1}
	raw := []rawDisplayMode{
		{W: 1920, H: 1080, Hz: 60, Bpp: 32},
		{W: 1280, H: 720, Hz: 60, Bpp: 32},
	}
	got := shapeDisplayModes(raw, cur)
	want := []displayMode{{W: 1080, H: 1920, Hz: 60, Current: true}, {W: 720, H: 1280, Hz: 60}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	tg, ok := resolveDisplayTarget(raw, cur, displayModeSetReq{ID: "a", W: 720, H: 1280})
	if !ok || tg.W != 720 || tg.H != 1280 || tg.Orientation != 1 || tg.Bpp != 32 {
		t.Fatalf("target %+v ok=%v", tg, ok)
	}
}

func TestResolveDisplayTarget(t *testing.T) {
	cur := rawDisplayMode{W: 1280, H: 720, Hz: 60, Bpp: 32}
	raw := []rawDisplayMode{
		{W: 1920, H: 1080, Hz: 60, Bpp: 32},
		{W: 1920, H: 1080, Hz: 144, Bpp: 32},
		{W: 1920, H: 1080, Hz: 100, Bpp: 16},
		{W: 1280, H: 720, Hz: 60, Bpp: 32},
	}
	cases := []struct {
		req    displayModeSetReq
		ok     bool
		wantHz int
	}{
		{displayModeSetReq{W: 1920, H: 1080}, true, 144},          // listed Hz
		{displayModeSetReq{W: 1920, H: 1080, Hz: 60}, true, 60},   // offered Hz
		{displayModeSetReq{W: 1920, H: 1080, Hz: 100}, true, 144}, // other depth only: listed Hz
		{displayModeSetReq{W: 1920, H: 1080, Hz: 75}, true, 144},  // not offered: listed Hz
		{displayModeSetReq{W: 3840, H: 2160}, false, 0},           // not in the list
		{displayModeSetReq{W: 1280, H: 720, Hz: 0}, true, 60},
	}
	for i, c := range cases {
		tg, ok := resolveDisplayTarget(raw, cur, c.req)
		if ok != c.ok || (ok && (tg.Hz != c.wantHz || tg.Bpp != 32 || tg.W != c.req.W)) {
			t.Fatalf("case %d: got %+v ok=%v", i, tg, ok)
		}
	}
}

func TestParseDisplayModeSet(t *testing.T) {
	r, ok := parseDisplayModeSet([]byte(`{"type":"display_mode_set","id":"r1","w":1920,"h":1080,"hz":144,"keep":true}`))
	if !ok || r.ID != "r1" || r.W != 1920 || r.H != 1080 || r.Hz != 144 || !r.Keep {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
	for _, bad := range []string{
		`{"type":"display_mode_set","w":1920,"h":1080}`,            // no id
		`{"type":"display_mode_set","id":"a b","w":1920,"h":1080}`, // bad id
		`{"type":"display_mode_set","id":"a","w":0,"h":1080}`,
		`{"type":"display_mode_set","id":"a","w":1920,"h":-1}`,
		`{"type":"display_mode_set","id":"a","w":99999,"h":1080}`,
		`{"type":"display_mode_set","id":"a","w":1920,"h":1080,"hz":-5}`,
		`not json`,
	} {
		if _, ok := parseDisplayModeSet([]byte(bad)); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestIsDisplayModeMsg(t *testing.T) {
	yes := []string{`{"type":"display_modes_get","id":"x"}`, `{"type":"display_mode_set","id":"x","w":1,"h":1}`}
	no := []string{`{"type":"mouse","x":1}`, `{"type":"display_modes","id":"x"}`, `{"type":"key","code":"display_mode_set"}`, `display_mode`}
	for _, s := range yes {
		if !IsDisplayModeMsg([]byte(s)) {
			t.Fatalf("not detected: %s", s)
		}
	}
	for _, s := range no {
		if IsDisplayModeMsg([]byte(s)) {
			t.Fatalf("false positive: %s", s)
		}
	}
}

func TestDisplayAnswers(t *testing.T) {
	var m map[string]interface{}
	_ = json.Unmarshal(displayModesMsg("q1", displayModesInfo{Monitor: 0, Reason: "rdp"}), &m)
	if m["type"] != "display_modes" || m["changeable"] != false || m["reason"] != "rdp" || m["id"] != "q1" {
		t.Fatalf("list answer %v", m)
	}
	if modes, ok := m["modes"].([]interface{}); !ok || len(modes) != 0 {
		t.Fatalf("modes must be an empty array, got %v", m["modes"])
	}
	m = nil
	_ = json.Unmarshal(displayModeResult("q2", false, "badmode", -2), &m)
	if m["ok"] != false || m["code"] != "badmode" || m["cds"] != float64(-2) {
		t.Fatalf("result %v", m)
	}
	m = nil
	_ = json.Unmarshal(displayModeResult("q3", true, "", 0), &m)
	if m["ok"] != true || m["code"] != nil {
		t.Fatalf("ok result %v", m)
	}
	m = nil
	_ = json.Unmarshal(DisplayRefusal([]byte(`{"type":"display_mode_set","id":"z"}`), "session_changed"), &m)
	if m["type"] != "display_mode_result" || m["code"] != "session_changed" || m["id"] != "z" {
		t.Fatalf("refusal %v", m)
	}
	m = nil
	_ = json.Unmarshal(DisplayRefusal([]byte(`{"type":"display_modes_get","id":"z"}`), "session_changed"), &m)
	if m["type"] != "display_modes" || m["changeable"] != false || m["reason"] != "session_changed" {
		t.Fatalf("list refusal %v", m)
	}
	if displayModeCode(0) != "" || displayModeCode(1) != "restart" || displayModeCode(-2) != "badmode" || displayModeCode(-1) != "failed" {
		t.Fatal("codes")
	}
}

// ── Restore bookkeeping ─────────────────────────────────────────────────────

type fakeDisplayApply struct {
	dev     string
	m       rawDisplayMode
	persist bool
}

// fakeDisplay is a shared screen (several helpers = several keepers on the
// same fake), recording every apply.
type fakeDisplay struct {
	modes   map[string]rawDisplayMode
	fail    int // next apply returns this code when != 0
	applies []fakeDisplayApply
}

func (f *fakeDisplay) current(dev string) (rawDisplayMode, bool) {
	m, ok := f.modes[dev]
	return m, ok
}

func (f *fakeDisplay) apply(dev string, m rawDisplayMode, persist bool) int {
	if f.fail != 0 {
		rc := f.fail
		f.fail = 0
		return rc
	}
	f.applies = append(f.applies, fakeDisplayApply{dev, m, persist})
	f.modes[dev] = m
	return dispChangeSuccessful
}

var (
	modeOrig = rawDisplayMode{W: 1280, H: 720, Hz: 60, Bpp: 32}
	modeA    = rawDisplayMode{W: 1920, H: 1080, Hz: 60, Bpp: 32}
	modeB    = rawDisplayMode{W: 2560, H: 1440, Hz: 60, Bpp: 32}
)

func TestKeeperRestoresOriginalDynamically(t *testing.T) {
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{`\\.\DISPLAY1`: modeOrig}}
	var k displayKeeper
	if rc := k.set(fd, `\\.\DISPLAY1`, modeA, false); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	if rc := k.set(fd, `\\.\DISPLAY1`, modeB, false); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	got := k.restoreAll(fd)
	if len(got) != 1 || fd.modes[`\\.\DISPLAY1`] != modeOrig {
		t.Fatalf("restored %v, screen %+v", got, fd.modes)
	}
	last := fd.applies[len(fd.applies)-1]
	if last.persist || last.m != modeOrig {
		t.Fatalf("restore apply %+v (the first original, dynamic)", last)
	}
	if k.changed() {
		t.Fatal("bookkeeping not cleared")
	}
}

func TestKeeperKeepSkipsRestore(t *testing.T) {
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig}}
	var k displayKeeper
	k.set(fd, "D1", modeA, true)
	if !fd.applies[0].persist {
		t.Fatal("keep must write the registry")
	}
	if got := k.restoreAll(fd); len(got) != 0 || fd.modes["D1"] != modeA {
		t.Fatalf("kept mode was restored: %v %+v", got, fd.modes)
	}
}

func TestKeeperLastKeepWinsAndPersistedRestoreWritesRegistry(t *testing.T) {
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig}}
	var k displayKeeper
	k.set(fd, "D1", modeA, true)  // written to the registry
	k.set(fd, "D1", modeB, false) // then "do not keep"
	if got := k.restoreAll(fd); len(got) != 1 || fd.modes["D1"] != modeOrig {
		t.Fatalf("got %v %+v", got, fd.modes)
	}
	if last := fd.applies[len(fd.applies)-1]; !last.persist {
		t.Fatal("the registry was written: the restore must write it back too")
	}
}

func TestKeeperFailedSetRecordsNothing(t *testing.T) {
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig}, fail: dispChangeBadMode}
	var k displayKeeper
	if rc := k.set(fd, "D1", modeA, false); rc != dispChangeBadMode {
		t.Fatalf("rc %d", rc)
	}
	if k.changed() {
		t.Fatal("a refused change must not be remembered")
	}
	// The original is the mode before the FIRST successful change.
	k.set(fd, "D1", modeA, false)
	k.set(fd, "D1", modeB, false)
	k.restoreAll(fd)
	if fd.modes["D1"] != modeOrig {
		t.Fatalf("screen %+v", fd.modes)
	}
}

func TestKeeperChangedByOtherAfterIsLeftAlone(t *testing.T) {
	// Viewer A changes, then someone else (viewer B / the local user) changes
	// again: A leaving must not undo the newer choice.
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig}}
	var a displayKeeper
	a.set(fd, "D1", modeA, false)
	fd.modes["D1"] = modeB
	if got := a.restoreAll(fd); len(got) != 0 || fd.modes["D1"] != modeB {
		t.Fatalf("got %v screen %+v", got, fd.modes)
	}
}

func TestKeeperTwoViewersStack(t *testing.T) {
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig}}
	var a, b displayKeeper
	a.set(fd, "D1", modeA, false)
	b.set(fd, "D1", modeB, false)
	// A leaves first: B changed after it — nothing restored.
	if got := a.restoreAll(fd); len(got) != 0 || fd.modes["D1"] != modeB {
		t.Fatalf("A: %v %+v", got, fd.modes)
	}
	// B leaves: back to B's original (A's mode).
	if got := b.restoreAll(fd); len(got) != 1 || fd.modes["D1"] != modeA {
		t.Fatalf("B: %v %+v", got, fd.modes)
	}

	// Other order: B leaves first (back to A's mode), then A (back to the
	// true original).
	fd = &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig}}
	a, b = displayKeeper{}, displayKeeper{}
	a.set(fd, "D1", modeA, false)
	b.set(fd, "D1", modeB, false)
	b.restoreAll(fd)
	a.restoreAll(fd)
	if fd.modes["D1"] != modeOrig {
		t.Fatalf("screen %+v", fd.modes)
	}
}

func TestKeeperBackToOriginalNoExtraApply(t *testing.T) {
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig}}
	var k displayKeeper
	k.set(fd, "D1", modeA, false)
	k.set(fd, "D1", modeOrig, false)
	n := len(fd.applies)
	k.restoreAll(fd)
	if len(fd.applies) != n {
		t.Fatalf("an apply was made although the screen is already at its original mode")
	}
}

func TestKeeperPerMonitor(t *testing.T) {
	fd := &fakeDisplay{modes: map[string]rawDisplayMode{"D1": modeOrig, "D2": modeA}}
	var k displayKeeper
	k.set(fd, "D1", modeA, false)
	k.set(fd, "D2", modeB, true)
	got := k.restoreAll(fd)
	if !reflect.DeepEqual(got, []string{"D1"}) || fd.modes["D1"] != modeOrig || fd.modes["D2"] != modeB {
		t.Fatalf("got %v screen %+v", got, fd.modes)
	}
}

func TestAgentFeaturesDisplayModes(t *testing.T) {
	has := false
	for _, f := range agentFeatures() {
		if f == featDisplayModes {
			has = true
		}
	}
	if has != displayModesSupported() {
		t.Fatalf("display_modes advertised=%v supported=%v", has, displayModesSupported())
	}
	for _, f := range directStreamFeatures() {
		if f == featDisplayModes {
			t.Fatal("the direct stream path must not advertise display_modes")
		}
	}
}
