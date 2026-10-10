package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// logInputEvent is the single sink for the input diagnostic log. It is a
// package var so the no-keystroke-logging tests can replace it and prove that no keystroke
// content — no key, no code, no typed character, no VK — ever reaches the log.
// The real sink (logInputEventImpl) is platform-specific (input_windows.go and
// the stubs); on Windows it appends to the capped, rotated diagnostic file.
//
// No keystroke logging: the agent used to write every key an operator
// typed during a remote session (passwords included) to
// C:\Windows\Temp\oblireach-input.log in the clear, unbounded. That log now
// records only the shape of an event (its type, action and modifiers), never
// what was typed; the file is capped and rotated; and it is purged once on the
// first start of this version.
var logInputEvent = func(msg string) { logInputEventImpl(msg) }

// PurgeInputLogOnce empties the input diagnostic log(s) once per agent version
// (no keystroke logging). Earlier versions wrote every key typed during a
// session — passwords included — into these files in the clear, so the first
// start of a version that no longer does so wipes whatever the old versions
// left. The one-shot is tracked by a stamp in stateDir (the agent's config
// directory) carrying the version, so a normal restart never touches the file
// again. Best-effort throughout: a log that cannot be opened or truncated
// simply keeps its previous contents.
func PurgeInputLogOnce(stateDir, version string) {
	if !DiskWritesEnabled() {
		return
	}
	files := inputLogFiles()
	if len(files) == 0 {
		return
	}
	stamp := filepath.Join(stateDir, "input-log-purged")
	if b, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(b)) == version {
		return
	}
	for _, p := range files {
		if _, err := os.Stat(p); err != nil {
			continue // absent: nothing to wipe
		}
		_ = os.WriteFile(p, nil, 0644)
	}
	_ = os.MkdirAll(stateDir, 0755)
	_ = os.WriteFile(stamp, []byte(version), 0644)
}
