package main

// Chat with the operator: the transcript model, independent of the UI.
//
// Lines travel as {"type":"chat","text":...} on the session's control
// channel (engine Options.OnChat / Session.SendChat), ChatMaxBytes at most
// each way. The transcript lives in memory only, belongs to one session and
// is dropped when the session ends; nothing is written to disk.
//
// Every text is sanitised before it is shown: control characters, bidi
// overrides and invisible format characters are removed (an operator must
// not be able to disguise or reorder what the person reads), line breaks
// are kept but runs of empty lines are collapsed. For display, very long
// words get break opportunities (chatDisplayText) so they wrap instead of
// overflowing.

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oblireach/agent/engine"
)

// ChatFrom says who wrote a chat line.
type ChatFrom int

const (
	ChatFromOperator ChatFrom = iota
	ChatFromMe
)

// ChatMessage is one line of the transcript.
type ChatMessage struct {
	ID     int // 1, 2, 3... within the session
	From   ChatFrom
	Text   string // sanitised
	At     time.Time
	Failed bool // ChatFromMe: could not be sent
}

const (
	// chatMaxMessages bounds the transcript (the oldest lines go first).
	chatMaxMessages = 500
	// chatMaxInputUnits caps the input field in UTF-16 code units so a
	// line can never exceed ChatMaxBytes once encoded in UTF-8 (a code unit
	// is at most 3 bytes, a surrogate pair 4 bytes for 2 units).
	chatMaxInputUnits = engine.ChatMaxBytes / 3
	// chatWordBreakRunes: a run of non-space characters longer than this
	// gets an invisible break opportunity when displayed.
	chatWordBreakRunes = 16
	// chatMaxLines: lines beyond this are joined into the last one.
	chatMaxLines = 40
)

// ChatLog is the transcript of one session. Not safe for concurrent use:
// the controller guards it.
type ChatLog struct {
	msgs   []ChatMessage
	nextID int
}

// Add sanitises text and appends it; ok is false when nothing is left to
// show (then nothing is appended).
func (l *ChatLog) Add(from ChatFrom, text string, at time.Time) (ChatMessage, bool) {
	text = sanitizeChat(text)
	if text == "" {
		return ChatMessage{}, false
	}
	l.nextID++
	m := ChatMessage{ID: l.nextID, From: from, Text: text, At: at}
	l.msgs = append(l.msgs, m)
	if n := len(l.msgs) - chatMaxMessages; n > 0 {
		l.msgs = append(l.msgs[:0:0], l.msgs[n:]...)
	}
	return m, true
}

// MarkFailed flags one of the person's lines as not sent.
func (l *ChatLog) MarkFailed(id int) {
	for i := range l.msgs {
		if l.msgs[i].ID == id {
			l.msgs[i].Failed = true
			return
		}
	}
}

// Messages returns a copy of the transcript.
func (l *ChatLog) Messages() []ChatMessage {
	if len(l.msgs) == 0 {
		return nil
	}
	return append([]ChatMessage(nil), l.msgs...)
}

// Clear drops the transcript.
func (l *ChatLog) Clear() {
	for i := range l.msgs { // wipe the texts (no clear builtin on Go 1.20, the nt6 line)
		l.msgs[i] = ChatMessage{}
	}
	l.msgs, l.nextID = nil, 0
}

// chatUnread counts the operator's lines with an ID above seen.
func chatUnread(msgs []ChatMessage, seen int) int {
	n := 0
	for _, m := range msgs {
		if m.From == ChatFromOperator && m.ID > seen {
			n++
		}
	}
	return n
}

// lastOperatorLine returns the operator's latest line, ok false if none.
func lastOperatorLine(msgs []ChatMessage) (ChatMessage, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].From == ChatFromOperator {
			return msgs[i], true
		}
	}
	return ChatMessage{}, false
}

// chatDisplayText is a sanitised line ready for word-wrapped display.
func chatDisplayText(text string) string { return breakLongWords(text, chatWordBreakRunes) }

// chatPreview turns a sanitised line into one short line (banner).
func chatPreview(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	r := []rune(text)
	return strings.TrimSpace(string(r[:maxRunes])) + "…"
}

// sanitizeChat makes a chat line safe to display (see the file comment) and
// cuts it to ChatMaxBytes.
func sanitizeChat(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune('\n')
		case r == '\t' || r == '\r' || r == '\u00a0' || r == '\u2028' || r == '\u2029':
			b.WriteRune(' ')
		case isChatInvisible(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	// Trim each line, collapse empty-line runs, cap the line count.
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	empty := 0
	for _, ln := range lines {
		ln = strings.TrimRightFunc(ln, unicode.IsSpace)
		if strings.TrimSpace(ln) == "" {
			empty++
			if empty > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		empty = 0
		out = append(out, ln)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) > chatMaxLines {
		out = append(out[:chatMaxLines-1], strings.Join(out[chatMaxLines-1:], " "))
	}
	return cutUTF8(strings.Join(out, "\n"), engine.ChatMaxBytes)
}

// isChatInvisible: characters never shown in a chat line: controls (C0, DEL,
// C1), bidi embeddings / overrides / isolates and marks, zero-width and
// other format characters (Cf, except the zero-width joiner that emoji
// sequences use), private use, noncharacters, and the replacement-free
// specials.
func isChatInvisible(r rune) bool {
	switch {
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return true
	case r == '\u200d': // ZWJ (emoji sequences)
		return false
	case unicode.Is(unicode.Cf, r): // bidi controls, ZWSP, BOM, soft hyphen...
		return true
	case unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
		return true
	case r >= 0xfdd0 && r <= 0xfdef, r&0xfffe == 0xfffe:
		return true
	case r >= 0xfff9 && r <= 0xfffc: // interlinear annotations, object replacement
		return true
	}
	return false
}

// breakLongWords inserts a zero-width space after every n characters of a
// run without spaces, so word-wrapping can break it.
func breakLongWords(s string, n int) string {
	run := 0
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			run = 0
		} else {
			if run >= n {
				b.WriteRune('\u200b')
				run = 0
			}
			run++
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cutUTF8 returns s cut to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
