package engine

// Reach file-transfer permission. The
// Obliance server decides, per remote session, whether the operator may
// transfer files through Reach: the capability 'files' on the device (admins
// hold it) AND, when privacy mode is on, the privacy unlock of the 'files'
// feature — the same rule as the Obliance file explorer. The answer rides in
// open_remote_tunnel as `files` (boolean). When it is false:
//   - the service passes --no-files to the capture helper, which then
//     advertises none of clipboard_files / file_upload / file_download (init
//     features, clipboard.files, files.upload/download), lists no copied files
//     and answers every file_* request with file_error{code:"forbidden"}
//     without touching the disk;
//   - the service-side supervisor (remote_session_windows.go) ALSO refuses the
//     viewer's file_* messages and 0x20 frames before they reach the helper,
//     and drops the helper's 0x21 frames and file-only control messages
//     (defence in depth: a helper that missed the flag still transfers
//     nothing).
// The Obliance relay applies the same rule for agents older than this one.
// Covered: paste / drop uploads (file_begin + 0x20), downloads of files copied
// on the remote (clipboard_files list, file_get + 0x21). The direct capture
// path (stream.go) implements no file transfer at all.
//
// PURE Go (no cgo, no build tag): unit-tested in files_gate_test.go.

import (
	"bytes"
	"encoding/json"
	"strings"
)

// fxErrForbidden: file transfers are not permitted in this session (the
// operator lacks the 'files' permission, or privacy mode is on without the
// 'files' unlock). The viewers translate it.
const fxErrForbidden = "forbidden"

// HelperNoFiles is set from the capture helper's --no-files flag: this
// session may not transfer files (see the header).
var HelperNoFiles bool

// FilesAllowedFromPayload reads open_remote_tunnel's `files`. Absent: an
// Obliance server older than this rule, which never decides — the historical
// behaviour (allowed) is kept. Present but not a boolean: refused (fail closed).
func FilesAllowedFromPayload(payload map[string]interface{}) bool {
	raw, ok := payload["files"]
	if !ok {
		return true
	}
	b, isBool := raw.(bool)
	return isBool && b
}

// controlMsgType returns a JSON control message's type exactly as the capture
// helper's dispatch reads it (encoding/json into a `type` string field: the
// key matches case-insensitively and the last matching key wins), or "" when
// it has none or does not decode.
func controlMsgType(payload []byte) string {
	var p struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	return p.Type
}

// IsViewerFileMessage reports whether a viewer → agent control message belongs
// to a file transfer (file_begin, file_get, file_end, file_progress,
// file_cancel, and any later file_* message).
func IsViewerFileMessage(payload []byte) bool {
	return strings.HasPrefix(controlMsgType(payload), "file_")
}

var fileWord = []byte("file")

// IsAgentFileMessage reports whether a helper → viewer control message only
// exists for a file transfer: file_accept, file_offer, file_progress,
// file_done, clipboard_files (the list of files copied on the remote)…
// file_error is NOT one: it is how a refusal reaches the viewer.
func IsAgentFileMessage(payload []byte) bool {
	if !bytes.Contains(payload, fileWord) {
		return false // the helper's own JSON (json.Marshal never escapes letters)
	}
	t := controlMsgType(payload)
	if strings.HasPrefix(t, "clipboard_files") {
		return true
	}
	return strings.HasPrefix(t, "file_") && t != "file_error"
}

// FileRefusal is the answer to a refused viewer file message: file_error
// {tid, code:"forbidden"} for a message that expects one (it carries a tid and
// is not itself an acknowledgement, a cancel or an error), nil otherwise.
func FileRefusal(payload []byte) []byte {
	var m struct {
		Type string  `json:"type"`
		Tid  *uint32 `json:"tid"`
	}
	if json.Unmarshal(payload, &m) != nil || m.Tid == nil || !strings.HasPrefix(m.Type, "file_") {
		return nil
	}
	switch m.Type {
	case "file_progress", "file_cancel", "file_error":
		return nil
	}
	b, _ := json.Marshal(map[string]interface{}{
		"type": "file_error", "tid": *m.Tid, "code": fxErrForbidden,
		"message": "file transfer is not permitted in this session",
	})
	return b
}

// withoutFileFeatures removes the file-transfer features from a feature list.
func withoutFileFeatures(f []string) []string {
	out := f[:0:0]
	for _, x := range f {
		if x == featClipboardFiles || x == featFileUpload || x == featFileDownload {
			continue
		}
		out = append(out, x)
	}
	return out
}
