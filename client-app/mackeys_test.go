package main

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Mac keyboard mapping (reach_viewer.go reachKeyMsg, agent keymap_mac.go).

// Every key message the viewer sends goes through reachKeyMsg (which adds
// macMap for a macOS agent that maps keys): a send site that bypasses it would
// leave that key unmapped on a Mac.
func TestMacKeysEveryKeyMessageStamped(t *testing.T) {
	html := buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"})
	for _, want := range []string{
		"KEY_MAP_MAC: 'key_map_mac'",
		"function reachKeyMsg(tab, k)",
		"function reachMacMapMode(policy, ctrlCmd, agentMaps, operatorIsMac)",
		"renderMacKeysSetting(pb);",
		"REACH_MACKEYS_KEY = 'oblireach_mac_keys'",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// A key message literal: "{ type: 'key'". Each must be the argument of
	// reachKeyMsg( on the same line.
	for i, line := range strings.Split(html, "\n") {
		if !strings.Contains(line, "{ type: 'key'") {
			continue
		}
		if !strings.Contains(line, "reachKeyMsg(tab, { type: 'key'") {
			t.Errorf("line %d sends a key message without reachKeyMsg: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// The decision table, run in node when it is installed (the page's own
// function, extracted from the built page).
func TestMacKeysDecision(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	html := buildUI(&Config{ServerURL: "https://obliance.example", Username: "admin"})
	fn := regexp.MustCompile(`(?s)function reachMacMapMode\(.*?\n}\n`).FindString(strings.ReplaceAll(html, "\r\n", "\n"))
	if fn == "" {
		t.Fatal("reachMacMapMode not found")
	}
	script := fn + `
var cases = [
  ['auto', true, true, false, 2],   // Windows operator, macOS agent, Ctrl -> Cmd
  ['auto', false, true, false, 1],  // without Ctrl -> Cmd
  ['off', true, true, false, 0],    // setting Off
  ['auto', true, false, false, 0],  // Windows / Linux agent, or an old macOS agent
  ['auto', true, true, true, 0],    // the operator is on a Mac
  ['weird', true, true, false, 2],  // unknown policy = automatic
];
var bad = cases.filter(function (c) { return reachMacMapMode(c[0], c[1], c[2], c[3]) !== c[4]; });
if (bad.length) { console.log(JSON.stringify(bad)); process.exit(1); }
`
	cmd := exec.Command(node, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("decision table: %v %s", err, out)
	}
}
