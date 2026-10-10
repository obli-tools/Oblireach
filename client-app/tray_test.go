package main

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// The icon's menu: Open (default), the unread line only when there are
// some (99+ at most), Quit after a separator.
func TestTrayMenu(t *testing.T) {
	m := trayMenu(0)
	if len(m) != 3 || m[0].id != trayCmdOpen || !m[0].bold || !m[1].sep || m[2].id != trayCmdQuit {
		t.Fatalf("menu: %+v", m)
	}
	m = trayMenu(150)
	if len(m) != 4 || m[1].id != trayCmdChat || !strings.Contains(m[1].text, "99+") {
		t.Fatalf("menu with unread: %+v", m)
	}
	if trayTip(0) != "Oblireach" || !strings.Contains(trayTip(7), "7") {
		t.Fatalf("tips: %q %q", trayTip(0), trayTip(7))
	}
}

// A notification's texts: no control characters, bounded in UTF-16 units
// (never a split surrogate pair), a title and a text always.
func TestTrayNoteText(t *testing.T) {
	ti, tx := trayNoteText(" PC\x00-1\r\n", "a\tb\x1bc")
	if ti != "PC-1" || tx != "a bc" {
		t.Fatalf("clean: %q %q", ti, tx)
	}
	ti, tx = trayNoteText("", "")
	if ti != "Oblireach" || tx == "" {
		t.Fatalf("empty: %q %q", ti, tx)
	}
	long := strings.Repeat("\U0001F600", 200) // 400 UTF-16 units
	ti, tx = trayNoteText(long, long)
	for _, c := range []struct {
		s   string
		max int
	}{{ti, 63}, {tx, 255}} {
		u := utf16.Encode([]rune(c.s))
		if len(u) > c.max || !strings.HasSuffix(c.s, "…") {
			t.Fatalf("%d units (max %d)", len(u), c.max)
		}
		if strings.ContainsRune(c.s, '�') {
			t.Fatal("a surrogate pair split")
		}
	}
	if truncUTF16("abc", 3) != "abc" || truncUTF16("abcd", 3) != "ab…" {
		t.Fatal("truncUTF16")
	}
}
