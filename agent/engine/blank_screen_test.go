package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The 18 languages of the Oblireach / Obliance UIs, each with its 4 texts.
func TestBlankScreenCatalogComplete(t *testing.T) {
	want := []string{"ar", "cs", "da", "de", "en", "es", "fr", "it", "ja", "ko", "nl", "pl", "pt-BR", "ru", "sv", "tr", "uk", "zh-CN"}
	if len(blankScreenCatalog) != len(want) {
		t.Fatalf("catalog has %d languages, want %d", len(blankScreenCatalog), len(want))
	}
	for _, l := range want {
		e, ok := blankScreenCatalog[l]
		if !ok {
			t.Fatalf("language %s missing", l)
		}
		for i, s := range e {
			if strings.TrimSpace(s) == "" {
				t.Fatalf("%s[%d] empty", l, i)
			}
		}
		if !strings.Contains(e[1], "{name}") {
			t.Fatalf("%s: the named body must contain {name}: %q", l, e[1])
		}
		if strings.Contains(e[2], "{name}") || strings.Contains(e[0], "{name}") || strings.Contains(e[3], "{name}") {
			t.Fatalf("%s: {name} only in the named body", l)
		}
		if !strings.Contains(e[0], "Oblireach") {
			t.Fatalf("%s: the title names Oblireach: %q", l, e[0])
		}
	}
	// Every PRIMARYLANGID maps into the catalog.
	for id, l := range blankLangByPrimaryID {
		if _, ok := blankScreenCatalog[l]; !ok {
			t.Fatalf("LANGID %#x maps to %q, not in the catalog", id, l)
		}
	}
	// French: formal address (no tutoiement).
	for _, s := range blankScreenCatalog["fr"] {
		for _, w := range []string{" tu ", " ton ", " ta ", " tes ", " te ", "t'"} {
			if strings.Contains(" "+strings.ToLower(s)+" ", w) {
				t.Fatalf("fr: tutoiement in %q", s)
			}
		}
	}
}

func TestBlankLangFromTag(t *testing.T) {
	cases := map[string]string{
		"fr-FR": "fr", "FR": "fr", "fr_CA": "fr", "pt-PT": "pt-BR", "pt-BR": "pt-BR", "zh-Hans-CN": "zh-CN",
		"zh-TW": "zh-CN", "en-GB": "en", "de-CH": "de", "uk-UA": "uk", "ar-SA": "ar", "": "", "xx-YY": "", "  ja-JP ": "ja",
	}
	for in, want := range cases {
		if got := blankLangFromTag(in); got != want {
			t.Errorf("blankLangFromTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlankScreenTextFor(t *testing.T) {
	fr := blankScreenTextFor("fr", "Alice Martin")
	if fr.Title != "Session Oblireach en cours" || fr.Body != "Alice Martin intervient sur votre ordinateur. Merci de patienter." || fr.RTL {
		t.Fatalf("fr text: %+v", fr)
	}
	anon := blankScreenTextFor("fr", "  ‮\u0007 ")
	if anon.Body != blankScreenCatalog["fr"][2] {
		t.Fatalf("no usable name → anonymous body, got %q", anon.Body)
	}
	if en := blankScreenTextFor("xx", "Bob"); en.Title != blankScreenCatalog["en"][0] || !strings.HasPrefix(en.Body, "Bob ") {
		t.Fatalf("unknown language → English: %+v", en)
	}
	if ar := blankScreenTextFor("ar", "Bob"); !ar.RTL || !strings.Contains(ar.Body, "Bob") {
		t.Fatalf("Arabic is right to left and names the operator: %+v", ar)
	}
}

func TestCleanOperatorName(t *testing.T) {
	if got := cleanOperatorName("  Alice\r\n\tMartin‮  "); got != "Alice Martin" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("é", 200)
	if got := cleanOperatorName(long); len([]rune(got)) != 64 {
		t.Fatalf("name not bounded to 64 runes: %d", len([]rune(got)))
	}
	if got := cleanOperatorName("a​b⁦c"); got != "a b c" {
		t.Fatalf("zero-width / isolates replaced: %q", got)
	}
}

func TestBlankScreenMessages(t *testing.T) {
	req, ok := parseBlankScreenRequest([]byte(`{"type":"set_blank_screen","on":true,"operator":"Mallory"}`))
	if !ok || !req.On || req.Operator != "Mallory" {
		t.Fatalf("parse: %+v %v", req, ok)
	}
	if _, ok := parseBlankScreenRequest([]byte(`{"type":"mouse","x":1,"note":"set_blank_screen"}`)); ok {
		t.Fatal("another type mentioning the word is not a request")
	}
	if IsBlankScreenMsg([]byte(`{"type":"keepalive"}`)) || !IsBlankScreenMsg([]byte(`{"type":"set_blank_screen","on":false}`)) {
		t.Fatal("isBlankScreenMsg")
	}
	// The service's name replaces whatever the viewer said.
	out := WithBlankOperator([]byte(`{"type":"set_blank_screen","on":true,"operator":"Mallory"}`), "Alice\n")
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil || m["operator"] != "Alice" || m["on"] != true || m["type"] != "set_blank_screen" {
		t.Fatalf("withBlankOperator: %s", out)
	}
	if s := string(BlankScreenStatusJSON(true, "ignored")); s != `{"active":true,"type":"blank_screen_status"}` {
		t.Fatalf("status on: %s", s)
	}
	if s := string(BlankScreenStatusJSON(false, "local_user")); s != `{"active":false,"reason":"local_user","type":"blank_screen_status"}` {
		t.Fatalf("status off: %s", s)
	}
}

// The feature and init.blankScreen agree with the platform's availability.
func TestBlankScreenAdvertised(t *testing.T) {
	ok, reason := blankScreenAvailable()
	has := false
	for _, f := range agentFeatures() {
		if f == featBlankScreen {
			has = true
		}
	}
	if has != ok {
		t.Fatalf("feature advertised=%v but available=%v (%s)", has, ok, reason)
	}
	info := blankScreenInitInfo()
	if info["available"] != ok || (!ok && info["reason"] != reason) {
		t.Fatalf("init.blankScreen %v vs available=%v reason=%q", info, ok, reason)
	}
	init := buildInitMessage(100, 100, 30, nil, nil, reachView{Mode: "monitor"}, 1, false, 48000, nil)
	if _, ok := init["blankScreen"]; !ok {
		t.Fatal("helper init carries blankScreen")
	}
	direct := buildDirectInitMessage(100, 100, 30, nil, nil)
	bs, _ := direct["blankScreen"].(map[string]interface{})
	if bs == nil || bs["available"] != false || bs["reason"] == "" {
		t.Fatalf("direct init: blankScreen unavailable with a reason, got %v", direct["blankScreen"])
	}
	for _, f := range directStreamFeatures() {
		if f == featBlankScreen {
			t.Fatal("the direct path never advertises the blank screen")
		}
	}
}

// Off is always answered "operator" and never touches the platform when the
// page is not up; unavailable platforms answer their reason.
func TestApplyBlankScreenOffAndUnavailable(t *testing.T) {
	blankActive.Store(false)
	var m map[string]interface{}
	_ = json.Unmarshal(applyBlankScreen(blankScreenRequest{On: false}), &m)
	if m["active"] != false || m["reason"] != "operator" {
		t.Fatalf("off: %v", m)
	}
	if ok, reason := blankScreenAvailable(); !ok {
		m = nil
		_ = json.Unmarshal(applyBlankScreen(blankScreenRequest{On: true, Operator: "x"}), &m)
		if m["active"] != false || m["reason"] != reason {
			t.Fatalf("unavailable: %v (want reason %q)", m, reason)
		}
		if blankActive.Load() {
			t.Fatal("never active where unavailable")
		}
	}
}

func TestBlankWatchdogBeat(t *testing.T) {
	blankLastBeat.Store(0)
	if !blankBeatStale(time.Now()) {
		t.Fatal("no beat yet: stale")
	}
	blankScreenBeat()
	if blankBeatStale(time.Now()) {
		t.Fatal("just beaten: fresh")
	}
	if !blankBeatStale(time.Now().Add(blankWatchdogMs*time.Millisecond + time.Second)) {
		t.Fatal("beyond the watchdog: stale")
	}
}

func TestNotifyBlankAutoOffNeverBlocks(t *testing.T) {
	for len(blankScreenAutoOff) > 0 {
		<-blankScreenAutoOff
	}
	notifyBlankAutoOff("watchdog")
	notifyBlankAutoOff("failed") // full: dropped, never blocks
	if r := <-blankScreenAutoOff; r != "watchdog" {
		t.Fatalf("got %q", r)
	}
}
