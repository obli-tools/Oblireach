package main

// Platform-neutral helpers of the end-user chat window (chat_windows.go):
// geometry, the JS <-> Go bridge and escaping. Kept free of Win32 calls so
// they are unit-tested everywhere (chat_layout_test.go).

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Geometry of the chat window, in the helper's logical pixels (the chat
// helper is DPI-unaware on purpose: Windows scales it, see main.go).
const (
	chatWinW         = 380
	chatWinH         = 520
	chatWinMargin    = 16 // gap to the work-area edges (so above the taskbar)
	chatWinRadius    = 18 // rounded corners (window region)
	chatBubbleSize   = 56 // minimized "bubble"
	chatBubbleMargin = 20
	chatWinAlpha     = 240 // 94 % opacity (layered window)

	// The host window is created hidden, already styled and placed, and is
	// shown when the page has loaded. chatRevealFallback shows it anyway if
	// that signal never comes: the window is never hidden forever.
	chatRevealFallback = 5 * time.Second
	// WebView2 still not created after this long: give up and exit (the
	// service then reports the chat as closed) instead of keeping an empty
	// window that the user cannot close.
	chatEmbedDeadline = 45 * time.Second
)

// chatRect is a screen rectangle (same layout as the Win32 RECT).
type chatRect struct{ Left, Top, Right, Bottom int32 }

func (r chatRect) width() int32  { return r.Right - r.Left }
func (r chatRect) height() int32 { return r.Bottom - r.Top }
func (r chatRect) usable() bool  { return r.width() > 0 && r.height() > 0 }

// chatDefaultWorkArea is used only when every Win32 query failed.
var chatDefaultWorkArea = chatRect{0, 0, 1024, 768}

// chatFirstUsable returns the first non-empty rectangle of cands (monitor
// work area, then the SPI work area, then the screen), else a 1024x768 default.
func chatFirstUsable(cands ...chatRect) chatRect {
	for _, c := range cands {
		if c.usable() {
			return c
		}
	}
	return chatDefaultWorkArea
}

// chatBottomRight returns the rectangle of a w x h window in the
// bottom-right corner of the work area, margin away from its right and bottom
// edges. The work area is that of one monitor (it may have negative
// coordinates, left of / above the primary one) and already excludes the
// taskbar. When the work area is too small, the window is pinned to its
// left / top edge so that its top-left part (header, close button) stays
// on screen.
func chatBottomRight(work chatRect, w, h, margin int32) chatRect {
	x := work.Right - w - margin
	if x < work.Left {
		x = work.Left
	}
	y := work.Bottom - h - margin
	if y < work.Top {
		y = work.Top
	}
	return chatRect{x, y, x + w, y + h}
}

// chatBridgeNames are the Go functions the chat page may call as
// window.<name>(...). Every call is posted as one JSON web message
// {method, params}; unknown methods are ignored.
var chatBridgeNames = []string{
	"goSendMessage", "goMinimizeChat", "goRestoreChat", "goCloseChat",
	"goAllowRemote", "goDenyRemote", "goTyping", "goStartDrag",
}

// chatBridgeScript defines the bridge functions in every document of the
// chat WebView (added with AddScriptToExecuteOnDocumentCreated).
func chatBridgeScript() string {
	names, _ := json.Marshal(chatBridgeNames)
	return `(function(){var n=` + string(names) + `;n.forEach(function(k){` +
		`window[k]=function(){var a=Array.prototype.slice.call(arguments);` +
		`try{window.chrome.webview.postMessage(JSON.stringify({method:k,params:a}));}catch(e){}};});})();`
}

type chatBridgeCall struct {
	Method string
	Args   []string // string parameters ("" for a non-string one)
}

// chatParseBridgeCall decodes a web message posted by chatBridgeScript.
func chatParseBridgeCall(raw string) (chatBridgeCall, bool) {
	var m struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return chatBridgeCall{}, false
	}
	known := false
	for _, n := range chatBridgeNames {
		if n == m.Method {
			known = true
			break
		}
	}
	if !known {
		return chatBridgeCall{}, false
	}
	c := chatBridgeCall{Method: m.Method}
	for _, p := range m.Params {
		var s string
		if json.Unmarshal(p, &s) != nil {
			s = ""
		}
		c.Args = append(c.Args, s)
	}
	return c, true
}

// arg returns the i-th string argument of a bridge call, or "".
func (c chatBridgeCall) arg(i int) string {
	if i < len(c.Args) {
		return c.Args[i]
	}
	return ""
}

// chatJSString returns s as a JavaScript string literal (quotes, backslashes,
// line breaks, U+2028/2029, NUL and "</script>" all escaped).
func chatJSString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// chatCleanText removes control characters (a NUL would make the WebView2
// string conversion panic) and trims spaces: for names shown in the window.
func chatCleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

// chatInitial is the upper-cased first letter of name, "?" when empty.
func chatInitial(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// chatInitials: first letters of the first and last words ("Jane Doe" ->
// "JD"), else the first letter; "?" when empty.
func chatInitials(name string) string {
	parts := strings.Fields(name)
	switch {
	case len(parts) >= 2:
		return chatInitial(parts[0]) + chatInitial(parts[len(parts)-1])
	case len(parts) == 1:
		return chatInitial(parts[0])
	}
	return "?"
}

// chatAvatarSrc accepts the operator avatar only as an image data URI made
// of URI-safe characters (it goes into an src="..." attribute); anything
// else is dropped and the initials are shown instead.
func chatAvatarSrc(avatar string) string {
	if !strings.HasPrefix(avatar, "data:image/") {
		return ""
	}
	for _, r := range avatar {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("+/=:;,.-_%", r):
		default:
			return ""
		}
	}
	return avatar
}
