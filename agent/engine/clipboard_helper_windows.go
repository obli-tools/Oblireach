//go:build windows

package engine

// Helper-side clipboard. ObliReach on Windows always runs the capture
// helper cross-session (the service is in session 0), and the helper's pipe
// reader never handled clipboard messages — so clipboard_set / clipboard_get /
// clipboard_sync did nothing on every real install. This wires them in the
// user session where the clipboard actually lives:
//   - clipboard_set{text,id,paste}  → set the clipboard (+ optional Ctrl+V), ack
//   - clipboard_get{id}             → read it, reply clipboard_content
//   - clipboard_sync{push}          → poll the change counter, push remote copies
// Replies go back over PipeTypeControl, which the service forwards to the viewer
// as WS text.

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

type helperClipState struct {
	mu      sync.Mutex
	stop    chan struct{}
	lastSeq uint32
}

var helperClip helperClipState

// handleHelperClipboard dispatches one clipboard control message. `send` writes
// a pipe frame back to the service; `paste` injects Ctrl+V through the input
// dispatch thread (which is attached to the input desktop — calling SendInput
// straight from the reader goroutine would not be).
func handleHelperClipboard(kind string, payload []byte, send func(byte, []byte) error, paste func()) {
	switch kind {
	case "clipboard_set":
		var m struct {
			Text  string `json:"text"`
			ID    string `json:"id"`
			Paste bool   `json:"paste"`
		}
		_ = json.Unmarshal(payload, &m)
		if len(m.Text) > clipMaxText {
			clipAck(send, m.ID, false, "too_large")
			return
		}
		// Browsers hand over "\n" line ends; Windows programs expect CRLF in
		// CF_UNICODETEXT (older Notepad / consoles show a pasted LF-only text
		// on one line).
		text := toCRLF(m.Text)
		// Set + note the new sequence number under the watcher's lock, so the
		// watcher never sees our own change as a remote copy (echo).
		helperClip.mu.Lock()
		ok := clipboardSetText(text)
		helperClip.lastSeq = clipboardSeq()
		helperClip.mu.Unlock()
		if !ok {
			// Never paste after a failed set: Ctrl+V would paste whatever was
			// on the remote clipboard before (possibly a password).
			clipAck(send, m.ID, false, "busy")
			return
		}
		clipAck(send, m.ID, true, "")
		if m.Paste && paste != nil {
			paste()
		}
	case "clipboard_get":
		var m struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(payload, &m)
		clipSendContent(send, clipboardReadText(), m.ID, "request")
	case "clipboard_sync":
		var m struct {
			Push bool `json:"push"`
		}
		_ = json.Unmarshal(payload, &m)
		if m.Push {
			startClipWatch(send)
		} else {
			stopClipWatch()
		}
	}
}

func clipAck(send func(byte, []byte) error, id string, ok bool, code string) {
	msg := map[string]interface{}{"type": "clipboard_ack", "ok": ok}
	if id != "" {
		msg["id"] = id
	}
	if code != "" {
		msg["code"] = code
	}
	b, _ := json.Marshal(msg)
	_ = send(PipeTypeControl, b)
}

func clipSendContent(send func(byte, []byte) error, text, id, origin string) {
	truncated := false
	if len(text) > clipMaxText {
		text = safeTruncateUTF8(text, clipMaxText)
		truncated = true
	}
	msg := map[string]interface{}{"type": "clipboard_content", "text": text, "origin": origin, "truncated": truncated}
	if id != "" {
		msg["id"] = id
	}
	b, _ := json.Marshal(msg)
	_ = send(PipeTypeControl, b)
}

// clipboardReadText reads the clipboard text, retrying for ~200 ms while
// another program holds the clipboard open (the read then returns "" although
// text is there).
func clipboardReadText() string {
	for i := 0; ; i++ {
		t := clipboardGet()
		if t != "" || i >= 10 || !clipboardHasText() {
			return t
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// toCRLF turns every line end (\n, \r\n, lone \r) into \r\n.
func toCRLF(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// safeTruncateUTF8 caps a string at n bytes without splitting a rune.
func safeTruncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func startClipWatch(send func(byte, []byte) error) {
	helperClip.mu.Lock()
	if helperClip.stop != nil {
		helperClip.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	helperClip.stop = stop
	if helperClip.lastSeq == 0 {
		helperClip.lastSeq = clipboardSeq()
	}
	helperClip.mu.Unlock()

	go func() {
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()
		filesShown := false
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				seq := clipboardSeq()
				helperClip.mu.Lock()
				changed := seq != helperClip.lastSeq
				helperClip.lastSeq = seq
				helperClip.mu.Unlock()
				if !changed {
					continue
				}
				// Files copied in Explorer (CF_HDROP): listed for download
				// (file_get idx refers to this list). Another copy clears it.
				if x := helperClipXfer; x != nil {
					if paths, isFiles := clipboardFileList(); isFiles {
						sendClipFiles(send, x.setClipboardFiles(paths, statAsSessionUser))
						filesShown = true
						continue
					}
					if filesShown {
						sendClipFiles(send, x.setClipboardFiles(nil, statAsSessionUser))
						filesShown = false
					}
				}
				if text := clipboardReadText(); text != "" {
					clipSendContent(send, text, "", "remote_change")
				}
			}
		}
	}()
}

// helperClipXfer is the capture helper's file-transfer state: the clipboard
// watcher lists copied files into it (nil: no file transfer).
var helperClipXfer *fileXfer

func sendClipFiles(send func(byte, []byte) error, list []clipFile) {
	if list == nil {
		list = []clipFile{}
	}
	b, _ := json.Marshal(map[string]interface{}{"type": "clipboard_files", "files": list})
	_ = send(PipeTypeControl, b)
}

func stopClipWatch() {
	helperClip.mu.Lock()
	if helperClip.stop != nil {
		close(helperClip.stop)
		helperClip.stop = nil
	}
	helperClip.mu.Unlock()
}
