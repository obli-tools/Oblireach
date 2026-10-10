package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

type ttWire struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Gen     int     `json:"gen"`
	AutoSas *bool   `json:"autoSas"`
	User    *string `json:"user"`
	Parts   []struct {
		B64 *string `json:"b64"`
		Key *string `json:"key"`
	} `json:"parts"`
}

func decodeTT(t *testing.T, b []byte) ttWire {
	t.Helper()
	var w ttWire
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatalf("frame is not JSON: %v", err)
	}
	return w
}

// partsOf renders the parts as "text:<decoded>" / "key:<name>".
func partsOf(t *testing.T, w ttWire) []string {
	t.Helper()
	var out []string
	for _, p := range w.Parts {
		switch {
		case p.B64 != nil && p.Key == nil:
			raw, err := base64.StdEncoding.DecodeString(*p.B64)
			if err != nil {
				t.Fatalf("b64 part: %v", err)
			}
			out = append(out, "text:"+string(raw))
		case p.Key != nil && p.B64 == nil:
			out = append(out, "key:"+*p.Key)
		default:
			t.Fatalf("a part must be exactly one of b64 / key: %+v", p)
		}
	}
	return out
}

func eqParts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// An empty username types the password ONLY — nothing before
// it (no Tab, no space), whichever button was clicked.
func TestTypeTextFrameEmptyUsernameNothingBefore(t *testing.T) {
	for _, user := range []string{"", " ", "  ", " ", "\t"} {
		for _, withUser := range []bool{false, true} {
			f, err := buildTypeTextFrame("r1", user, []byte("p@ss w"), typeTextOpts{withUser: withUser, gen: 3})
			if err != nil {
				t.Fatal(err)
			}
			got := partsOf(t, decodeTT(t, f))
			if !eqParts(got, []string{"text:p@ss w"}) {
				t.Fatalf("username %q withUser=%v: parts %q, want the password alone", user, withUser, got)
			}
		}
	}
}

func TestTypeTextFrameShapes(t *testing.T) {
	cases := []struct {
		user string
		o    typeTextOpts
		want []string
	}{
		{"adm", typeTextOpts{withUser: true}, []string{"text:adm", "key:Tab", "text:pw"}},
		{"adm", typeTextOpts{withUser: false}, []string{"text:pw"}},
		{"adm", typeTextOpts{withUser: true, enter: true}, []string{"text:adm", "key:Tab", "text:pw", "key:Enter"}},
		{"", typeTextOpts{withUser: true, enter: true}, []string{"text:pw", "key:Enter"}},
		{"DOM\\adm", typeTextOpts{withUser: true}, []string{"text:DOM\\adm", "key:Tab", "text:pw"}},
	}
	for _, c := range cases {
		f, err := buildTypeTextFrame("r1", c.user, []byte("pw"), c.o)
		if err != nil {
			t.Fatal(err)
		}
		w := decodeTT(t, f)
		if got := partsOf(t, w); !eqParts(got, c.want) {
			t.Errorf("%q %+v: %q, want %q", c.user, c.o, got, c.want)
		}
		if w.Parts[0].Key != nil {
			t.Errorf("%q %+v: a frame never starts with a key", c.user, c.o)
		}
	}
}

func TestTypeTextFrameAutoSasAndHeader(t *testing.T) {
	f, _ := buildTypeTextFrame("abc", "", []byte("pw"), typeTextOpts{gen: 7, autoSas: true})
	w := decodeTT(t, f)
	if w.Type != "type_text" || w.ID != "abc" || w.Gen != 7 || w.AutoSas == nil || !*w.AutoSas {
		t.Fatalf("header: %+v", w)
	}
	// Off: the field is not written at all (an older agent's parser sees the
	// exact frame it always saw).
	f, _ = buildTypeTextFrame("abc", "", []byte("pw"), typeTextOpts{gen: 7})
	if w := decodeTT(t, f); w.AutoSas != nil {
		t.Fatalf("autoSas off must be omitted: %s", f)
	}
	long := "0123456789012345678901234567890123456789"
	f, _ = buildTypeTextFrame(long, "", []byte("pw"), typeTextOpts{})
	if w := decodeTT(t, f); len(w.ID) != 32 {
		t.Fatalf("id not bounded: %q", w.ID)
	}
	if _, err := buildTypeTextFrame("a", "u", nil, typeTextOpts{withUser: true}); err == nil {
		t.Fatal("an empty secret must never produce a frame (it would type the username + Tab alone)")
	}
}

// The entry's username travels apart ("user") when the parts do
// not carry it, so the agent can fill a sign-in screen's username field; it
// is never there twice, and never for a blank username.
func TestTypeTextFrameUserField(t *testing.T) {
	cases := []struct {
		user     string
		withUser bool
		want     string // "" = no user field
	}{
		{`CONTOSO\jsmith`, false, `CONTOSO\jsmith`},
		{`CONTOSO\jsmith`, true, ""}, // already in the parts
		{"", false, ""},
		{"  ", false, ""},
	}
	for _, c := range cases {
		f, err := buildTypeTextFrame("r1", c.user, []byte("pw"), typeTextOpts{withUser: c.withUser, autoSas: true})
		if err != nil {
			t.Fatal(err)
		}
		w := decodeTT(t, f)
		got := ""
		if w.User != nil {
			raw, err := base64.StdEncoding.DecodeString(*w.User)
			if err != nil {
				t.Fatal(err)
			}
			got = string(raw)
		}
		if got != c.want {
			t.Errorf("%q withUser=%v: user %q, want %q", c.user, c.withUser, got, c.want)
		}
		if !c.withUser && !eqParts(partsOf(t, w), []string{"text:pw"}) {
			t.Errorf("%q: the parts stay the password alone: %q", c.user, partsOf(t, w))
		}
	}
}
