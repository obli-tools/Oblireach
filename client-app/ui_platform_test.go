package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Linux page carries its own blocks (the RDP stream mode, the POSIX
// local paths, "Receive help" on Linux), the Windows page none of them.
// OBLIREACH_DUMP_JS_LINUX=<dir>: the Linux page's scripts dumped there (the
// JS harness's Linux tests run them).
func TestBuildUIPlatformBlocks(t *testing.T) {
	old := uiTargetOS
	defer func() { uiTargetOS = old }()
	cfg := &Config{ServerURL: "https://obliance.example", Username: "admin", Language: "en"}

	uiTargetOS = "windows"
	win := buildUI(cfg)
	uiTargetOS = "linux"
	lin := buildUI(cfg)
	for _, want := range []string{"LOCAL_OS = 'unix';", "TOOL_IMPL.rdp = rdpStreamTabImpl;", "function rdpsCertDialog(", ".rdp-host .rdps-canvas{",
		"var LINUX_UPDATE = ", "renderSupportSection = (function (orig)"} {
		if strings.Contains(win, want) {
			t.Fatalf("the Windows page has %q", want)
		}
		if c := strings.Count(lin, want); c != 1 {
			t.Fatalf("the Linux page has %q %d times", want, c)
		}
	}
	// The page-wide rules hold for the Linux page too (whatever OS runs the test).
	t.Run("linux page rules", func(t *testing.T) {
		TestNoHardcodedColours(t)
		TestMacKeysEveryKeyMessageStamped(t)
	})
	if i := strings.Index(lin, "%!"); i >= 0 {
		t.Fatalf("fmt artifact near %q", lin[max(0, i-80):min(len(lin), i+40)])
	}
	if dir := os.Getenv("OBLIREACH_DUMP_JS_LINUX"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i, m := range scriptRe.FindAllStringSubmatch(lin, -1) {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("script-%d.js", i)), []byte(m[1]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte(lin), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
