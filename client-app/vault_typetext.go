package main

// vault_typetext.go — the agent's type_text wire frame for one saved secret
// (agent typetext.go), built in Go so the secret never crosses into page JS.
// Portable (no Windows API), so its exact shape is unit-tested everywhere
// (vault_typetext_test.go); the binding that injects it is
// vault_bridge.go's vaultType.
//
// What is typed, in order:
//   - the username then Tab, ONLY when the caller asked for it (withUser) AND
//     the entry has a username that is not blank. An empty (or blank)
//     username types the password alone: nothing at all before it — no Tab,
//     no space (on a machine outside a domain the sign-in
//     screen has no user field, so a username + Tab there lands in the
//     password field);
//   - the secret;
//   - Enter, when asked.
// autoSas asks the agent to send Ctrl+Alt+Del first when the session is
// locked and its password field is not showing (agent typetext_prep.go). It
// is written only when on: an older agent ignores the field anyway and types
// as before.
// user: the entry's username, sent apart from the parts when
// they do not carry it (the "password only" click). The agent types it ONLY
// when the sign-in screen's focused field is the username field ("Other
// user" on a domain machine): username, Tab, then the password once the
// focus is confirmed on a password box. Without a username there, the agent
// types nothing and says so (username_required). An older agent ignores it.

import (
	"encoding/json"
	"strings"
)

// typeTextOpts: what a vault click asks besides the entry itself.
type typeTextOpts struct {
	withUser bool // username + Tab before the password (when the entry has one)
	enter    bool // Enter after the password
	gen      int  // the view generation the page acted on (agent: session_changed otherwise)
	autoSas  bool // Ctrl+Alt+Del first when the session is locked (agent decides)
}

// typeTextHasUser: the username part is typed at all.
func typeTextHasUser(username string, withUser bool) bool {
	return withUser && strings.TrimSpace(username) != ""
}

// buildTypeTextFrame renders the type_text JSON for one saved secret. id is
// the page's request id (shortened to the agent's 32-char bound). The
// username (not a secret) is typed as stored; the secret goes straight from
// its []byte into the frame's base64 (no intermediate string copy).
func buildTypeTextFrame(id, username string, secret []byte, o typeTextOpts) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errEmptySecret
	}
	type part struct {
		B64 []byte `json:"b64,omitempty"`
		Key string `json:"key,omitempty"`
	}
	var parts []part
	if typeTextHasUser(username, o.withUser) {
		parts = append(parts, part{B64: []byte(username)}, part{Key: "Tab"})
	}
	parts = append(parts, part{B64: secret})
	if o.enter {
		parts = append(parts, part{Key: "Enter"})
	}
	var user []byte
	if !typeTextHasUser(username, o.withUser) && strings.TrimSpace(username) != "" {
		user = []byte(username)
	}
	msg := struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Gen     int    `json:"gen"`
		AutoSas bool   `json:"autoSas,omitempty"`
		User    []byte `json:"user,omitempty"`
		Parts   []part `json:"parts"`
	}{Type: "type_text", ID: shortRequestID(id), Gen: o.gen, AutoSas: o.autoSas, User: user, Parts: parts}
	return json.Marshal(msg)
}

// shortRequestID maps a (possibly long) page reqID onto the <=32
// [A-Za-z0-9_-] the agent's type_text id must be — reqIDOK already bounds
// the page's reqID more tightly than this, so this is a defensive truncate,
// never expected to actually cut anything in practice.
func shortRequestID(id string) string {
	if len(id) > 32 {
		return id[:32]
	}
	return id
}
