package main

import (
	"regexp"
	"strings"
	"testing"
)

// The page's own markup (before its scripts) never repeats an id, and the
// pieces the Scripts / Info / Chat slot and the Chat page rely on are there.
// (The Chat page itself is built by script: no id but its root, so none is
// shared with the chat panel; see test-viewer.mjs section 20.)
func TestPanelsAndChatPageMarkup(t *testing.T) {
	html := buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"})
	// The body tag carries the theme (<body data-theme="...">, theme.go).
	at := regexp.MustCompile(`\n<body[ >]`).FindStringIndex(html)
	if at == nil {
		t.Fatal("page has no <body> tag")
	}
	body := html[at[0]:]
	if i := strings.Index(body, "<script>"); i >= 0 {
		body = body[:i]
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		if seen[m[1]] {
			t.Errorf("id %q appears twice in the page markup", m[1])
		}
		seen[m[1]] = true
	}
	for _, id := range []string{"side-panel", "side-panel-body", "chat-panel", "chat-title", "chat-avatar", "chat-tabs", "chat-messages", "chat-typing", "chat-input", "chat-send"} {
		if !seen[id] {
			t.Errorf("page markup lacks #%s", id)
		}
	}
	if seen["chat-page"] {
		t.Error("#chat-page must be built by the script (once), not be in the markup")
	}
	for _, want := range []string{
		"{ route: 'chat',",
		"function renderChatPage(",
		"function rightSlotTake(",
		"body.rp-swap .side-panel,body.rp-swap .chat-panel{transition:none}",
		".nav-rail.compact .nav-item .nav-badge{display:block",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
