package engine

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ttFrame builds a well-formed type_text frame with a fixed gen (1) — the
// vast majority of tests don't care about the value, only that one is
// present (gen is now mandatory: type_text is bound to the view generation).
// Use ttFrameGen directly to test a specific or mismatching gen.
func ttFrame(id string, secureOnly bool, parts []map[string]interface{}) []byte {
	return ttFrameGen(id, secureOnly, 1, parts)
}
func ttFrameGen(id string, secureOnly bool, gen int, parts []map[string]interface{}) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"type": "type_text", "id": id, "secureOnly": secureOnly, "gen": gen, "parts": parts,
	})
	return b
}
func b64part(s string) map[string]interface{} { return map[string]interface{}{"b64": base64.StdEncoding.EncodeToString([]byte(s))} }
func keyPart(k string) map[string]interface{} { return map[string]interface{}{"key": k} }

func TestIsTypeTextMsg(t *testing.T) {
	if !IsTypeTextMsg(ttFrame("a1", true, []map[string]interface{}{b64part("x")})) {
		t.Fatal("a real type_text frame not recognised")
	}
	for _, p := range [][]byte{
		[]byte(`{"type":"key","action":"down","key":"a"}`),
		[]byte(`{"type":"mouse","action":"move"}`),
		[]byte(`{"type":"keepalive"}`),
		[]byte(`not json`),
		[]byte(`{"type":"type_text_result","id":"a1","ok":true}`), // the AGENT's own reply is not a request
	} {
		if IsTypeTextMsg(p) {
			t.Fatalf("false positive on %s", p)
		}
	}
}

func TestParseTypeTextValid(t *testing.T) {
	f := ttFrame("Cafe_9-x", true, []map[string]interface{}{b64part("héllo €"), keyPart("Tab"), b64part("pw"), keyPart("Enter")})
	msg, code := parseTypeText(f)
	if code != "" {
		t.Fatalf("valid frame rejected: %s", code)
	}
	if msg.ID != "Cafe_9-x" || !msg.SecureOnly || msg.Gen != 1 || len(msg.Parts) != 4 {
		t.Fatalf("bad parse: %+v", msg)
	}
	if string(msg.Parts[0].Text) != "héllo €" || msg.Parts[1].Key != "Tab" ||
		string(msg.Parts[2].Text) != "pw" || msg.Parts[3].Key != "Enter" {
		t.Fatalf("parts wrong: %+v", msg.Parts)
	}
	msg.wipe()
	if msg.Parts[0].Text != nil {
		t.Fatal("wipe did not clear text")
	}
}

func TestParseTypeTextRejects(t *testing.T) {
	long := strings.Repeat("a", 129)
	tot := []map[string]interface{}{}
	for i := 0; i < 3; i++ {
		tot = append(tot, b64part(strings.Repeat("a", 100))) // 300 runes total > 256
	}
	cases := map[string][]byte{
		"empty id":          ttFrame("", true, []map[string]interface{}{b64part("x")}),
		"id too long":       ttFrame(strings.Repeat("a", 33), true, []map[string]interface{}{b64part("x")}),
		"id bad char":       ttFrame("a b", true, []map[string]interface{}{b64part("x")}),
		"no parts":          ttFrame("a1", true, []map[string]interface{}{}),
		"too many parts":    ttFrame("a1", true, []map[string]interface{}{b64part("a"), b64part("b"), b64part("c"), b64part("d"), b64part("e"), b64part("f"), b64part("g")}),
		"part too long":     ttFrame("a1", true, []map[string]interface{}{b64part(long)}),
		"total too long":    ttFrame("a1", true, tot),
		"empty text part":   ttFrame("a1", true, []map[string]interface{}{b64part("")}),
		"control char":      ttFrame("a1", true, []map[string]interface{}{b64part("a\tb")}),
		"newline":           ttFrame("a1", true, []map[string]interface{}{b64part("a\nb")}),
		"bad key":           ttFrame("a1", true, []map[string]interface{}{keyPart("F1")}),
		"both b64 and key":  {},
		"neither":           ttFrame("a1", true, []map[string]interface{}{{}}),
		"invalid base64":    ttFrame("a1", true, []map[string]interface{}{{"b64": "@@@notb64@@@"}}),
		"invalid utf8":      ttFrame("a1", true, []map[string]interface{}{{"b64": base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})}}),
		// Bound to the view generation: gen is mandatory,
		// never typed blind when absent (not merely checked when present).
		"missing gen": func() []byte {
			b, _ := json.Marshal(map[string]interface{}{
				"type": "type_text", "id": "a1", "secureOnly": true,
				"parts": []map[string]interface{}{b64part("x")},
			})
			return b
		}(),
	}
	cases["both b64 and key"] = ttFrame("a1", true, []map[string]interface{}{{"b64": base64.StdEncoding.EncodeToString([]byte("x")), "key": "Tab"}})
	for name, f := range cases {
		if _, code := parseTypeText(f); code != "invalid" {
			t.Fatalf("%s: expected invalid, got %q", name, code)
		}
	}
	// A secret with leading/trailing spaces is kept as-is.
	msg, code := parseTypeText(ttFrame("a1", false, []map[string]interface{}{b64part("  pw  ")}))
	if code != "" || string(msg.Parts[0].Text) != "  pw  " {
		t.Fatalf("spaces not preserved: %q %q", code, msg.Parts)
	}
}

func TestTypeTextResultNoContent(t *testing.T) {
	ok := string(TypeTextResult("req1", true, ""))
	if !strings.Contains(ok, `"ok":true`) || !strings.Contains(ok, `"id":"req1"`) || strings.Contains(ok, "code") {
		t.Fatalf("ok result wrong: %s", ok)
	}
	bad := string(TypeTextResult("req1", false, "not_secure_desktop"))
	if !strings.Contains(bad, `"ok":false`) || !strings.Contains(bad, `"code":"not_secure_desktop"`) {
		t.Fatalf("err result wrong: %s", bad)
	}
	// Neither a length nor any content ever appears (the "type_text_result"
	// type name aside — check the actual leak vectors).
	if strings.Contains(ok, `"len`) || strings.Contains(ok, "length") ||
		strings.Contains(bad, "b64") || strings.Contains(bad, `"parts"`) {
		t.Fatalf("result leaked content/length: ok=%s bad=%s", ok, bad)
	}
}

func TestTypeLimiter(t *testing.T) {
	base := time.Unix(1000, 0)
	now := base
	old := typeNow
	typeNow = func() time.Time { return now }
	defer func() { typeNow = old }()
	var l typeLimiter

	if c := l.begin(); c != "" {
		t.Fatalf("first begin should pass, got %q", c)
	}
	if c := l.begin(); c != "busy" {
		t.Fatalf("second begin while in flight should be busy, got %q", c)
	}
	l.done()
	// Too soon (< 1 s).
	now = base.Add(500 * time.Millisecond)
	if c := l.begin(); c != "rate_limited" {
		t.Fatalf("too-soon begin should be rate_limited, got %q", c)
	}
	// After 1 s it passes.
	now = base.Add(1100 * time.Millisecond)
	if c := l.begin(); c != "" {
		t.Fatalf("after interval should pass, got %q", c)
	}
	l.done()

	// 20 per rolling minute.
	var l2 typeLimiter
	start := time.Unix(2000, 0)
	for i := 0; i < 20; i++ {
		now = start.Add(time.Duration(i) * 2 * time.Second)
		if c := l2.begin(); c != "" {
			t.Fatalf("send %d within budget should pass, got %q", i, c)
		}
		l2.done()
	}
	now = start.Add(41 * time.Second) // still within the minute of the first sends
	if c := l2.begin(); c != "rate_limited" {
		t.Fatalf("21st in a minute should be rate_limited, got %q", c)
	}
	// A minute after the first send, the window has room again.
	now = start.Add(61 * time.Second)
	if c := l2.begin(); c != "" {
		t.Fatalf("after the minute rolled, should pass, got %q", c)
	}
}

func TestPeekTypeTextID(t *testing.T) {
	if PeekTypeTextID(ttFrame("Req-1", true, []map[string]interface{}{b64part("x")})) != "Req-1" {
		t.Fatal("id not peeked")
	}
	if PeekTypeTextID([]byte(`{"type":"type_text","id":"a b"}`)) != "" {
		t.Fatal("bad id should peek empty")
	}
}
