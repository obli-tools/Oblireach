package main

// Agent side of the vault typing (typetext.go): the session-follow state
// cache never remembers a type_text frame.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestObserveToHelperIgnoresTypeText: a type_text frame passing through the
// relay is never remembered in the stream-state cache, so it is never replayed
// into another session on a follow switch.
func TestObserveToHelperIgnoresTypeText(t *testing.T) {
	c := newStreamStateCache()
	// The same frame as the vault tests' ttFrame("a1", true, b64 "s3cret").
	frame, _ := json.Marshal(map[string]interface{}{
		"type": "type_text", "id": "a1", "secureOnly": true, "gen": 1,
		"parts": []map[string]interface{}{{"b64": base64.StdEncoding.EncodeToString([]byte("s3cret"))}},
	})
	c.observeToHelper(frame, "corp\\op")
	msgs, _ := c.replayMessages("corp\\op")
	for _, m := range msgs {
		if strings.Contains(string(m), "type_text") || strings.Contains(string(m), base64.StdEncoding.EncodeToString([]byte("s3cret"))) {
			t.Fatalf("type_text was cached and replayed: %s", m)
		}
	}
}
