package engine

import (
	"strings"
	"testing"
)

// TestDispatchInputNeverLogsKeys proves no keystroke is logged: dispatching keyboard input never
// writes the key or the physical code (the typed content) to the diagnostic
// log. The log sink is hooked so nothing touches the real file.
//
// Password shape: a plain or
// Shift-only character key must produce NO log line at all — even a
// content-free "rx: type=..." line per keystroke leaks the password's
// length, which characters needed Shift, and (via the file's timestamps)
// inter-key timing. Only a system-modifier combo (Ctrl/Alt/Meta) still logs.
func TestDispatchInputNeverLogsKeys(t *testing.T) {
	var lines []string
	old := logInputEvent
	logInputEvent = func(msg string) { lines = append(lines, msg) }
	defer func() { logInputEvent = old }()

	// A password typed character by character — Shift included (the "S", "!").
	for _, k := range []string{"h", "S", "3", "c", "r", "3", "t", "!"} {
		dispatchInputJSON([]byte(`{"type":"key","action":"down","key":"`+k+`","code":"Key`+strings.ToUpper(k)+`","shift":true}`), 100, 100)
		dispatchInputJSON([]byte(`{"type":"key","action":"up","key":"`+k+`","code":"Key`+strings.ToUpper(k)+`","shift":true}`), 100, 100)
	}
	if len(lines) != 0 {
		t.Fatalf("plain/Shift character keys must produce NO log line at all, got %d: %v", len(lines), lines)
	}

	// A system-key combo (Ctrl+Alt+something not SAS) still logs — shape only,
	// never the key/code, and never the shift flag.
	dispatchInputJSON([]byte(`{"type":"key","action":"down","key":"x","code":"KeyX","ctrl":true,"alt":true,"shift":true}`), 100, 100)
	if len(lines) != 1 {
		t.Fatalf("expected exactly one shape-only line for the Ctrl+Alt combo, got %d: %v", len(lines), lines)
	}
	line := lines[0]
	for _, secret := range []string{"key=", "code=", `"x"`, "KeyX", "shift="} {
		if strings.Contains(line, secret) {
			t.Fatalf("log line leaked content: %q (matched %q)", line, secret)
		}
	}
	if !strings.HasPrefix(line, "rx: type=") {
		t.Fatalf("unexpected log line shape: %q", line)
	}
}

// TestDispatchDropsTypeText proves a type_text frame on the direct path is
// dropped (no reply channel there) and never logged.
func TestDispatchDropsTypeText(t *testing.T) {
	var lines []string
	old := logInputEvent
	logInputEvent = func(msg string) { lines = append(lines, msg) }
	defer func() { logInputEvent = old }()

	f := ttFrame("a1", true, []map[string]interface{}{b64part("s3cret")})
	dispatchInputJSON(f, 100, 100)
	for _, line := range lines {
		if strings.Contains(line, "type_text") || strings.Contains(line, "s3cret") {
			t.Fatalf("type_text leaked into the log: %q", line)
		}
	}
}

func TestSasStillLogged(t *testing.T) {
	var lines []string
	old := logInputEvent
	logInputEvent = func(msg string) { lines = append(lines, msg) }
	defer func() { logInputEvent = old }()
	oldHook := InputSASHook
	InputSASHook = func() {}
	defer func() { InputSASHook = oldHook }()

	dispatchInputJSON([]byte(`{"type":"key","action":"down","ctrl":true,"alt":true,"code":"Delete"}`), 100, 100)
	found := false
	for _, line := range lines {
		if line == "sas detected" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sas detected line missing; lines=%v", lines)
	}
}
