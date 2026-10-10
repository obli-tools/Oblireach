package main

import (
	"strings"
	"testing"
)

// The helper's command line carries no operator-chosen text (a quote in
// the operator name used to add helper arguments).
func TestChatHelperCmdLineHasNoOperatorText(t *testing.T) {
	got, err := chatHelperCmdLine(`C:\Program Files\Oblireach\oblireach-agent.exe`, "127.0.0.1:50123", "0123456789abcdef", "")
	if err != nil {
		t.Fatalf("valid chat id refused: %v", err)
	}
	want := `"C:\Program Files\Oblireach\oblireach-agent.exe" --chat-helper --addr=127.0.0.1:50123 --chat-id=0123456789abcdef`
	if got != want {
		t.Fatalf("command line\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "--operator") {
		t.Fatalf("operator name must not be on the command line: %s", got)
	}
}

func TestChatHelperCmdLineRefusesUnsafeChatID(t *testing.T) {
	bad := []string{
		"",
		`abc" --capture-helper`,
		"abc --url=http://x",
		"abc\x00",
		"abc\r\n",
		"a/b",
		`a\b`,
		"é",
		strings.Repeat("a", 65),
	}
	for _, id := range bad {
		if _, err := chatHelperCmdLine("x.exe", "127.0.0.1:1", id, ""); err == nil {
			t.Errorf("chat id %q accepted", id)
		}
		if validChatID(id) {
			t.Errorf("validChatID(%q) = true", id)
		}
	}
	for _, id := range []string{"0123456789abcdef", "A-b_9", strings.Repeat("f", 64)} {
		if !validChatID(id) {
			t.Errorf("validChatID(%q) = false", id)
		}
	}
}
