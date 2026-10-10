package wire

import "testing"

func TestDigitsAndFormat(t *testing.T) {
	cases := map[string]string{
		"482 913 507": "482913507",
		"482-913-507": "482913507",
		" 48.29.13 ":  "482913",
		"abc":         "",
	}
	for in, want := range cases {
		if got := Digits(in); got != want {
			t.Errorf("Digits(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Group3("482913507"); got != "482 913 507" {
		t.Errorf("Group3 = %q", got)
	}
	if got := Group3("1234"); got != "1 234" {
		t.Errorf("Group3(1234) = %q", got)
	}
}

func TestValidity(t *testing.T) {
	if !ValidID("482913507") || ValidID("082913507") || ValidID("48291350") || ValidID("48291350x") {
		t.Error("ValidID")
	}
	if !ValidCode("012345678") || ValidCode("12345678") || ValidCode("1234567890") {
		t.Error("ValidCode")
	}
}

func TestRandomDigits(t *testing.T) {
	seen := map[string]bool{}
	var firstZero bool
	for i := 0; i < 2000; i++ {
		id := RandomDigits(IDLen, true)
		if !ValidID(id) {
			t.Fatalf("invalid ID %q", id)
		}
		code := RandomDigits(CodeLen, false)
		if !ValidCode(code) {
			t.Fatalf("invalid code %q", code)
		}
		if code[0] == '0' {
			firstZero = true
		}
		if seen[id] {
			t.Fatalf("duplicate ID after %d draws", i)
		}
		seen[id] = true
	}
	if !firstZero {
		t.Error("codes never start with 0: first digit is not uniform")
	}
	if TruncID("482913507") != "482******" {
		t.Error("TruncID")
	}
}
