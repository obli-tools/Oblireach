package engine

import "testing"

func TestMinMaxGo120Semantics(t *testing.T) {
	// Builtins (Go 1.21+) or minmax_go120.go (Go 1.20): same results.
	if min(3, 1, 2) != 1 || max(3, 1, 2) != 3 || min(5) != 5 || max(-1, 0) != 0 {
		t.Fatal("min / max")
	}
	if max(2-5, 0, 7-9) != 0 || min(1.5, -2.5) != -2.5 {
		t.Fatal("min / max mixed")
	}
}
