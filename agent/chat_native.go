package main

// Platform-neutral part of the chat helper's two windows: the WebView2 page
// (chat_windows.go) and the NATIVE Win32 window (chat_native_windows.go),
// chosen when WebView2 cannot serve the chat: Windows 7 SP1 / 8.1 / Server
// 2008 R2 / 2012 / 2012 R2 (WebView2 Runtime frozen at 109, no longer
// patched), a runtime missing, or blocked by policy. Both windows speak the
// same pipe protocol with the service (chat.go): only the drawing differs.
// Kept free of Win32 calls so the decisions are unit-tested everywhere
// (chat_native_test.go).

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// chatUIMode is the window the helper opens (--chat-ui on its command line,
// a fixed value chosen by the service, never operator text).
const (
	chatUIWebView2 = "webview2" // default: the WebView2 page
	chatUINative   = "native"   // the native Win32 window
)

// chatUIFromFlag maps the --chat-ui value (anything unknown = WebView2,
// which falls back to the native window itself when it cannot start).
func chatUIFromFlag(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), chatUINative) {
		return chatUINative
	}
	return chatUIWebView2
}

// chatUIDecision: which window the service asks the helper for.
//   - preferNative: this Windows is older than 10 (the last WebView2 Runtime
//     that runs there, 109, gets no more security fixes): native at once;
//   - otherwise WebView2 when its runtime is usable, else native when the
//     native window exists on this platform;
//   - ok=false: no window can open (non-Windows build) — the caller reports
//     chat_unavailable as before.
func chatUIDecision(preferNative, webview2OK, nativeAvailable bool) (mode string, ok bool) {
	switch {
	case preferNative && nativeAvailable:
		return chatUINative, true
	case webview2OK:
		return chatUIWebView2, true
	case nativeAvailable:
		return chatUINative, true
	}
	return "", false
}

// chatHelperUI is what a chat window does for the service's messages.
type chatHelperUI interface {
	operatorMessage(text string)
	operatorTyping()
	remoteRequest(message string)
	fileReceived(name string)
	close() // the service ended the chat (stop, or the pipe broke)
}

// chatHelperDispatch applies one service -> helper pipe message to the
// window. Returns true when the chat is over (the window was asked to close).
func chatHelperDispatch(ui chatHelperUI, msgType byte, payload []byte) bool {
	switch msgType {
	case chatPipeMsg:
		var msg struct {
			Action   string `json:"action"`
			Text     string `json:"text"`
			Message  string `json:"message"`
			FileName string `json:"fileName"`
		}
		if json.Unmarshal(payload, &msg) != nil {
			return false
		}
		switch msg.Action {
		case "operator_message":
			ui.operatorMessage(msg.Text)
		case "request_remote":
			ui.remoteRequest(msg.Message)
		case "operator_typing":
			ui.operatorTyping()
		case "file_transfer":
			ui.fileReceived(msg.FileName)
		}
	case chatPipeStop:
		ui.close()
		return true
	}
	return false
}

// Bounds of the native window's input.
const (
	chatNativeMaxInput   = 4000            // characters typed in one message
	chatNativeTypingGap  = 2 * time.Second // at most one "typing" event per gap (as the page's emitTyping)
	chatNativeTypingShow = 3 * time.Second // the operator's typing indicator stays this long
)

// chatUserActions turns what the user does in the native window into the
// helper -> service events, exactly as the WebView2 page's bridge calls do
// (goSendMessage, goTyping, goAllowRemote / goDenyRemote, goCloseChat).
type chatUserActions struct {
	send func(action, text string) // goChatSend (replaced in tests)
	now  func() time.Time

	mu         sync.Mutex
	lastTyping time.Time
	closed     bool
	remoteOpen bool // a remote-control request waits for an answer
}

func newChatUserActions(send func(action, text string)) *chatUserActions {
	return &chatUserActions{send: send, now: time.Now}
}

// submit sends a typed line. Returns the text to show in the history (as
// sent) and false when there is nothing to send (blank) or the chat is over.
func (a *chatUserActions) submit(text string) (string, bool) {
	text = chatNativeCleanMessage(text)
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	if r := []rune(text); len(r) > chatNativeMaxInput {
		text = string(r[:chatNativeMaxInput])
	}
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return "", false
	}
	a.send("user_message", text)
	return text, true
}

// typed: the input changed. At most one "typing" event per chatNativeTypingGap.
func (a *chatUserActions) typed() bool {
	a.mu.Lock()
	now := a.now()
	// (A clock that went back never blocks the event.)
	if a.closed || (!a.lastTyping.IsZero() && !now.Before(a.lastTyping) && now.Sub(a.lastTyping) < chatNativeTypingGap) {
		a.mu.Unlock()
		return false
	}
	a.lastTyping = now
	a.mu.Unlock()
	a.send("typing", "")
	return true
}

// remoteRequested: the operator asked for remote control (the Allow / Deny
// buttons are shown).
func (a *chatUserActions) remoteRequested() {
	a.mu.Lock()
	a.remoteOpen = true
	a.mu.Unlock()
}

// answerRemote: Allow / Deny clicked. Sent once per request (a double click
// or a second button press is ignored); false when nothing was pending.
func (a *chatUserActions) answerRemote(allow bool) bool {
	a.mu.Lock()
	if !a.remoteOpen || a.closed {
		a.mu.Unlock()
		return false
	}
	a.remoteOpen = false
	a.mu.Unlock()
	if allow {
		a.send("allow_remote", "")
	} else {
		a.send("deny_remote", "")
	}
	return true
}

// userClosed: the user closed the window (sent once).
func (a *chatUserActions) userClosed() bool {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	a.closed = true
	a.mu.Unlock()
	a.send("user_closed", "")
	return true
}

// serviceClosed: the service ended the chat — nothing is sent any more (in
// particular no user_closed when the window then goes away).
func (a *chatUserActions) serviceClosed() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
}

// chatNativeCleanMessage: a message as shown / sent by the native window:
// line breaks normalised to CRLF (the Win32 edit controls), other control
// characters (NUL, escape sequences, bidi overrides are not controls and are
// kept as text) removed.
func chatNativeCleanMessage(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString("\r\n")
		case r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || (r >= 0x7f && r < 0xa0):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), "\r\n ")
}

// chatNativeStripEmoji removes the pictogram prefix some strings carry for
// the page (U+1F4CE PAPERCLIP): Windows 7's fonts show it as a box.
func chatNativeStripEmoji(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(s, "\U0001F4CE"))
}

// chatNativeHistoryLine: one history entry "[15:04] Name: text".
func chatNativeHistoryLine(at time.Time, who, text string) (head, body string) {
	return "[" + at.Format("15:04") + "] " + who + ": ", chatNativeCleanMessage(text)
}

// chatNativeNoRTF: a RichEdit control READS a string that starts with "{"
// followed by an RTF signature ("{\rtf", "{\urtf"...) as RTF when it is
// inserted with EM_REPLACESEL / WM_SETTEXT: RTF that could embed objects or
// links. Text from the operator (or anyone) must stay plain text: a leading
// "{" is preceded by an invisible U+200B ZERO WIDTH SPACE, so no inserted
// string can start an RTF stream.
func chatNativeNoRTF(s string) string {
	if strings.HasPrefix(s, "{") {
		return "\u200b" + s
	}
	return s
}
