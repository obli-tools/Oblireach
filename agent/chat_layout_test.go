package main

import (
	"strings"
	"testing"
)

func TestChatBottomRightPlacement(t *testing.T) {
	cases := []struct {
		name string
		work chatRect
		want chatRect
	}{
		// 1920x1080 primary, taskbar at the bottom (40 px): above the taskbar.
		{"primary bottom taskbar", chatRect{0, 0, 1920, 1040}, chatRect{1524, 504, 1904, 1024}},
		// Taskbar on the right: the window stays left of it.
		{"right taskbar", chatRect{0, 0, 1856, 1080}, chatRect{1460, 544, 1840, 1064}},
		// Taskbar on top: the work area starts lower, bottom edge unchanged.
		{"top taskbar", chatRect{0, 48, 1920, 1080}, chatRect{1524, 544, 1904, 1064}},
		// Secondary monitor LEFT of the primary (negative coordinates).
		{"monitor left of primary", chatRect{-2560, 0, 0, 1400}, chatRect{-396, 864, -16, 1384}},
		// Secondary monitor ABOVE the primary, offset.
		{"monitor above primary", chatRect{-300, -1080, 1620, -40}, chatRect{1224, -576, 1604, -56}},
		// Work area too small (tiny VM console): pinned to the top-left so
		// the header and close button stay on screen.
		{"work area too small", chatRect{0, 0, 800, 500}, chatRect{404, 0, 784, 520}},
		{"narrower than the window", chatRect{100, 100, 400, 700}, chatRect{100, 164, 480, 684}},
	}
	for _, c := range cases {
		got := chatBottomRight(c.work, chatWinW, chatWinH, chatWinMargin)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.width() != chatWinW || got.height() != chatWinH {
			t.Errorf("%s: size %dx%d, want %dx%d", c.name, got.width(), got.height(), chatWinW, chatWinH)
		}
		if got.Left < c.work.Left || got.Top < c.work.Top {
			t.Errorf("%s: top-left %d,%d outside the work area %+v", c.name, got.Left, got.Top, c.work)
		}
	}
}

func TestChatBubblePlacement(t *testing.T) {
	got := chatBottomRight(chatRect{0, 0, 1920, 1040}, chatBubbleSize, chatBubbleSize, chatBubbleMargin)
	want := chatRect{1844, 964, 1900, 1020}
	if got != want {
		t.Fatalf("bubble: got %+v, want %+v", got, want)
	}
}

func TestChatFirstUsable(t *testing.T) {
	mon := chatRect{0, 0, 1920, 1040}
	spi := chatRect{0, 0, 1600, 860}
	if got := chatFirstUsable(mon, spi); got != mon {
		t.Errorf("monitor work area first: got %+v", got)
	}
	// A failed GetMonitorInfo leaves an empty rect: next candidate.
	if got := chatFirstUsable(chatRect{}, spi); got != spi {
		t.Errorf("empty monitor rect skipped: got %+v", got)
	}
	// Inverted / zero-sized rects are not usable either.
	if got := chatFirstUsable(chatRect{10, 10, 5, 500}, chatRect{0, 0, 0, 0}); got != chatDefaultWorkArea {
		t.Errorf("default expected, got %+v", got)
	}
	if got := chatFirstUsable(); got != chatDefaultWorkArea {
		t.Errorf("no candidate: got %+v", got)
	}
}

func TestChatBridgeParse(t *testing.T) {
	c, ok := chatParseBridgeCall(`{"method":"goSendMessage","params":["hello \"you\""]}`)
	if !ok || c.Method != "goSendMessage" || c.arg(0) != `hello "you"` {
		t.Fatalf("send: %+v %v", c, ok)
	}
	if c.arg(1) != "" {
		t.Errorf("missing arg must be empty")
	}
	c, ok = chatParseBridgeCall(`{"method":"goStartDrag","params":[]}`)
	if !ok || c.Method != "goStartDrag" || len(c.Args) != 0 {
		t.Fatalf("drag: %+v %v", c, ok)
	}
	// Non-string parameters become "".
	c, ok = chatParseBridgeCall(`{"method":"goSendMessage","params":[42]}`)
	if !ok || c.arg(0) != "" {
		t.Fatalf("number param: %+v %v", c, ok)
	}
	for _, raw := range []string{``, `not json`, `{"method":"eval","params":["x"]}`, `{"params":["x"]}`, `"goCloseChat"`} {
		if _, ok := chatParseBridgeCall(raw); ok {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestChatBridgeScriptDefinesEveryName(t *testing.T) {
	s := chatBridgeScript()
	for _, n := range chatBridgeNames {
		if !strings.Contains(s, `"`+n+`"`) {
			t.Errorf("bridge script misses %s", n)
		}
	}
	if !strings.Contains(s, "window.chrome.webview.postMessage") {
		t.Errorf("bridge script does not post web messages")
	}
}

func TestChatJSString(t *testing.T) {
	got := chatJSString("it's \"x\" \\ \n\r  \x00</script>")
	for _, bad := range []string{"\n", "\r", " ", " ", "\x00", "</script>"} {
		if strings.Contains(got, bad) {
			t.Errorf("unescaped %q in %s", bad, got)
		}
	}
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("not a string literal: %s", got)
	}
}

func TestChatNames(t *testing.T) {
	if got := chatCleanText("  Jane\x00 Doe\r\n "); got != "Jane Doe" {
		t.Errorf("clean: %q", got)
	}
	cases := map[string]string{"": "?", "jane": "J", "jane doe": "JD", "Jean Pierre Martin": "JM", "élodie": "É"}
	for in, want := range cases {
		if got := chatInitials(in); got != want {
			t.Errorf("initials(%q) = %q, want %q", in, got, want)
		}
	}
	if chatInitial("") != "?" {
		t.Errorf("empty initial")
	}
}

func TestChatAvatarSrc(t *testing.T) {
	ok := "data:image/png;base64,iVBORw0KGgo+/="
	if chatAvatarSrc(ok) != ok {
		t.Errorf("valid avatar rejected")
	}
	for _, bad := range []string{
		"", "https://example.com/a.png", "javascript:alert(1)",
		`data:image/png;base64,AA" onerror="alert(1)`, "data:image/svg+xml,<svg onload=x>",
		"data:text/html;base64,AAAA",
	} {
		if chatAvatarSrc(bad) != "" {
			t.Errorf("accepted %q", bad)
		}
	}
}
