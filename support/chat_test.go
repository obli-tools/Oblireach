package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/oblireach/agent/engine"
)

func TestSanitizeChat(t *testing.T) {
	for in, want := range map[string]string{
		"hello":                          "hello",
		"  hello  ":                      "  hello",
		"a\r\nb":                         "a\nb",
		"a\n\n\n\n\nb":                   "a\n\nb",
		"\n\n  \nhi\n\n":                 "hi",
		"tab\there":                      "tab here",
		"bell\a esc\x1b[31m red":         "bell esc[31m red",
		"del\x7f c1\u0085x":              "del c1x",
		"pay\u202eoti\u202c.exe":         "payoti.exe", // RLO override
		"iso\u2066late\u2069 \u200emark": "isolate mark",
		"zero\u200bwidth\ufeff":          "zerowidth",
		"soft\u00adhyphen":               "softhyphen",
		"emoji 👩\u200d💻 ok":              "emoji 👩\u200d💻 ok",
		"日本語のテキスト":                       "日本語のテキスト",
		"مرحبا بك":                       "مرحبا بك",
		"bad\xffutf8":                    "bad\ufffdutf8",
		"private\ue000use":               "privateuse",
		"nbsp\u00a0sep\u2028line":        "nbsp sep line",
		"\x00\x01\x02":                   "",
		"   ":                            "",
	} {
		if got := sanitizeChat(in); got != want {
			t.Errorf("sanitizeChat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeChatLimits(t *testing.T) {
	long := strings.Repeat("é", engine.ChatMaxBytes) // 2 bytes each
	got := sanitizeChat(long)
	if len(got) > engine.ChatMaxBytes || !utf8.ValidString(got) {
		t.Errorf("long line: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	many := strings.Repeat("x\n", 100)
	if n := strings.Count(sanitizeChat(many), "\n") + 1; n != chatMaxLines {
		t.Errorf("100 lines kept as %d, want %d", n, chatMaxLines)
	}
	// The input field cap keeps any line within ChatMaxBytes.
	worst := strings.Repeat("\U0001F600", chatMaxInputUnits/2) + strings.Repeat("€", chatMaxInputUnits%2)
	if len(worst) > engine.ChatMaxBytes {
		t.Errorf("chatMaxInputUnits too high: %d bytes", len(worst))
	}
	if len(strings.Repeat("€", chatMaxInputUnits)) > engine.ChatMaxBytes {
		t.Errorf("chatMaxInputUnits too high for 3-byte characters")
	}
}

func TestChatDisplayBreaksLongWords(t *testing.T) {
	in := strings.Repeat("a", 40) + " short " + strings.Repeat("字", 20)
	got := chatDisplayText(in)
	parts := strings.Split(got, "\u200b")
	if len(parts) != 4 {
		t.Fatalf("break opportunities: %q", got)
	}
	for _, p := range parts {
		for _, w := range strings.Fields(p) {
			if n := utf8.RuneCountInString(w); n > chatWordBreakRunes {
				t.Errorf("run of %d characters left: %q", n, w)
			}
		}
	}
	if strings.ReplaceAll(got, "\u200b", "") != in {
		t.Errorf("display text changed the content: %q", got)
	}
	// What is sent and stored has no break opportunity; a received one is
	// removed.
	if s := sanitizeChat(got); s != in {
		t.Errorf("sanitize kept the break opportunities: %q", s)
	}
}

func TestChatLog(t *testing.T) {
	var l ChatLog
	now := time.Now()
	if _, ok := l.Add(ChatFromOperator, "\x00 \u202e", now); ok {
		t.Error("an empty line was added")
	}
	m1, _ := l.Add(ChatFromOperator, "hello", now)
	m2, _ := l.Add(ChatFromMe, "hi", now)
	m3, _ := l.Add(ChatFromOperator, "how can I help?", now)
	if m1.ID != 1 || m2.ID != 2 || m3.ID != 3 {
		t.Errorf("ids %d %d %d", m1.ID, m2.ID, m3.ID)
	}
	msgs := l.Messages()
	if chatUnread(msgs, 0) != 2 || chatUnread(msgs, 1) != 1 || chatUnread(msgs, 3) != 0 {
		t.Errorf("unread counts wrong")
	}
	if last, ok := lastOperatorLine(msgs); !ok || last.ID != 3 {
		t.Errorf("last operator line %+v", last)
	}
	l.MarkFailed(2)
	if !l.Messages()[1].Failed || msgs[1].Failed {
		t.Error("MarkFailed: the log must change, earlier copies must not")
	}
	for i := 0; i < chatMaxMessages+10; i++ {
		l.Add(ChatFromOperator, "x", now)
	}
	if n := len(l.Messages()); n != chatMaxMessages {
		t.Errorf("transcript holds %d lines, cap %d", n, chatMaxMessages)
	}
	if l.Messages()[0].ID != 14 {
		t.Errorf("oldest kept id %d, want 14", l.Messages()[0].ID)
	}
	l.Clear()
	if l.Messages() != nil {
		t.Error("Clear left lines")
	}
	if m, _ := l.Add(ChatFromMe, "again", now); m.ID != 1 {
		t.Errorf("ids restart after Clear: %d", m.ID)
	}
}

func TestChatPreview(t *testing.T) {
	if got := chatPreview("a\nb   c", 10); got != "a b c" {
		t.Errorf("preview %q", got)
	}
	if got := chatPreview(strings.Repeat("ab ", 20), 7); got != "ab ab a…" {
		t.Errorf("preview %q", got)
	}
}

// ── controller ───────────────────────────────────────────────────────────────

func TestControllerChat(t *testing.T) {
	h := start(t, nil)
	host := h.b.nextHost(t)
	sess, _ := host.operator(testContext(t), "s1", nil)
	<-h.b.runs
	h.v.waitPhase(t, PhaseSession, nil)

	if err := h.c.SendChat("too early?"); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	ev := h.b.lastEvents()
	var mu sync.Mutex
	var sent []string
	fail := false
	ev.ChatReady(func(text string) error {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return errors.New("link down")
		}
		sent = append(sent, text)
		return nil
	})
	ev.ChatReceived("Hello \u202eworld")
	st := h.v.waitPhase(t, PhaseSession, func(s State) bool { return len(s.Session.Chat) == 2 })
	if m := st.Session.Chat[1]; m.From != ChatFromOperator || m.Text != "Hello world" {
		t.Errorf("operator line %+v", m)
	}
	if err := h.c.SendChat("  \x00 "); !errors.Is(err, ErrChatEmpty) {
		t.Errorf("empty line: %v", err)
	}
	if err := h.c.SendChat("I need\x07 help\r\n"); err != nil {
		t.Fatal(err)
	}
	st = h.v.waitPhase(t, PhaseSession, func(s State) bool { return len(s.Session.Chat) == 3 })
	if m := st.Session.Chat[2]; m.From != ChatFromMe || m.Text != "I need help" || m.Failed {
		t.Errorf("own line %+v", m)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(sent) == 2 })
	mu.Lock()
	if sent[0] != "too early?" || sent[1] != "I need help" {
		t.Errorf("sent %q", sent)
	}
	fail = true
	mu.Unlock()
	if err := h.c.SendChat("lost"); err != nil {
		t.Fatal(err)
	}
	st = h.v.waitPhase(t, PhaseSession, func(s State) bool { return len(s.Session.Chat) == 4 && s.Session.Chat[3].Failed })
	if st.Session.Chat[0].Text != "too early?" {
		t.Errorf("first line %+v", st.Session.Chat[0])
	}

	// The transcript ends with the session, and a new session starts empty.
	sess.end(nil)
	end := h.v.waitPhase(t, PhaseEnded, nil)
	if end.Session.Chat != nil || h.c.State().Session.Chat != nil {
		t.Error("transcript kept after the session")
	}
	if err := h.c.SendChat("after"); !errors.Is(err, ErrChatNoSession) {
		t.Errorf("SendChat after the session: %v", err)
	}
	ev.ChatReceived("late line") // a stale engine callback: ignored
	if h.c.State().Session.Chat != nil {
		t.Error("a line after the session was kept")
	}
	h.c.Acknowledge()
	host2 := h.b.nextHost(t)
	host2.operator(testContext(t), "s2", nil)
	<-h.b.runs
	st = h.v.waitPhase(t, PhaseSession, nil)
	if len(st.Session.Chat) != 0 {
		t.Errorf("new session starts with %d lines", len(st.Session.Chat))
	}
}

func TestControllerChatQueueFull(t *testing.T) {
	h := start(t, nil)
	host := h.b.nextHost(t)
	host.operator(testContext(t), "s1", nil)
	<-h.b.runs
	h.v.waitPhase(t, PhaseSession, nil)
	block := make(chan struct{})
	defer close(block)
	h.b.lastEvents().ChatReady(func(string) error { <-block; return nil })
	var busy bool
	for i := 0; i < chatQueueLen+5; i++ {
		if err := h.c.SendChat("x"); errors.Is(err, ErrChatBusy) {
			busy = true
			break
		}
	}
	if !busy {
		t.Error("the queue never filled up")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func (b *fakeBackend) lastEvents() SessionEvents {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.events
}

// testContext is t.Context (Go 1.24) for the nt6 line's Go 1.20: cancelled
// when the test ends.
func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}
