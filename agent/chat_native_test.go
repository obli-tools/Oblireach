package main

// The native chat window's decisions (chat_native.go), with fakes: which
// window the service asks for, the service -> window message mapping, and
// the user's actions -> helper events (same protocol as the WebView2 page).

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatUIDecision(t *testing.T) {
	for _, c := range []struct {
		prefer, wv2, native bool
		mode                string
		ok                  bool
	}{
		{false, true, true, chatUIWebView2, true}, // Windows 10+, runtime OK
		{false, true, false, chatUIWebView2, true},
		{false, false, true, chatUINative, true}, // runtime missing / blocked
		{true, true, true, chatUINative, true},   // Windows < 10: native even with WebView2 109
		{true, false, true, chatUINative, true},
		{false, false, false, "", false}, // no window at all: chat_unavailable
		{true, false, false, "", false},
	} {
		mode, ok := chatUIDecision(c.prefer, c.wv2, c.native)
		if mode != c.mode || ok != c.ok {
			t.Errorf("chatUIDecision(prefer=%v, wv2=%v, native=%v) = %q,%v want %q,%v", c.prefer, c.wv2, c.native, mode, ok, c.mode, c.ok)
		}
	}
	if chatUIFromFlag("native") != chatUINative || chatUIFromFlag(" NATIVE ") != chatUINative ||
		chatUIFromFlag("") != chatUIWebView2 || chatUIFromFlag("evil --x") != chatUIWebView2 {
		t.Fatal("chatUIFromFlag")
	}
}

func TestChatHelperCmdLineNative(t *testing.T) {
	got, err := chatHelperCmdLine(`C:\x\oblireach-agent.exe`, "127.0.0.1:1", "abc", chatUINative)
	if err != nil || !strings.HasSuffix(got, " --chat-ui=native") {
		t.Fatalf("%q %v", got, err)
	}
	got, _ = chatHelperCmdLine(`C:\x\oblireach-agent.exe`, "127.0.0.1:1", "abc", "native\" --evil")
	if strings.Contains(got, "evil") || strings.Contains(got, "--chat-ui") {
		t.Fatalf("only the fixed value reaches the command line: %q", got)
	}
}

// fakeChatUI records what the window was asked to do.
type fakeChatUI struct {
	mu  sync.Mutex
	got []string
}

func (f *fakeChatUI) add(s string)                { f.mu.Lock(); f.got = append(f.got, s); f.mu.Unlock() }
func (f *fakeChatUI) operatorMessage(text string) { f.add("msg:" + text) }
func (f *fakeChatUI) operatorTyping()             { f.add("typing") }
func (f *fakeChatUI) remoteRequest(m string)      { f.add("remote:" + m) }
func (f *fakeChatUI) fileReceived(n string)       { f.add("file:" + n) }
func (f *fakeChatUI) close()                      { f.add("close") }

func TestChatHelperDispatch(t *testing.T) {
	ui := &fakeChatUI{}
	msg := func(m map[string]interface{}) []byte { b, _ := json.Marshal(m); return b }
	steps := []struct {
		typ  byte
		p    []byte
		stop bool
	}{
		{chatPipeMsg, msg(map[string]interface{}{"action": "operator_message", "text": "Bonjour <b>"}), false},
		{chatPipeMsg, msg(map[string]interface{}{"action": "operator_typing"}), false},
		{chatPipeMsg, msg(map[string]interface{}{"action": "request_remote", "message": "Puis-je ?"}), false},
		{chatPipeMsg, msg(map[string]interface{}{"action": "file_transfer", "fileName": "a.txt", "fileData": "QUJD"}), false},
		{chatPipeMsg, msg(map[string]interface{}{"action": "unknown_action"}), false},
		{chatPipeMsg, []byte("{not json"), false},
		{chatPipeInit, msg(map[string]interface{}{"operatorName": "x"}), false},
		{chatPipeStop, nil, true},
	}
	for i, s := range steps {
		if stop := chatHelperDispatch(ui, s.typ, s.p); stop != s.stop {
			t.Fatalf("step %d: stop=%v", i, stop)
		}
	}
	want := []string{"msg:Bonjour <b>", "typing", "remote:Puis-je ?", "file:a.txt", "close"}
	if strings.Join(ui.got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", ui.got, want)
	}
}

// sentEvents records goChatSend calls.
type sentEvents struct {
	mu  sync.Mutex
	got []string
}

func (s *sentEvents) send(action, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if text != "" {
		action += ":" + text
	}
	s.got = append(s.got, action)
}

func (s *sentEvents) list() string { s.mu.Lock(); defer s.mu.Unlock(); return strings.Join(s.got, "|") }

func TestChatUserActions(t *testing.T) {
	ev := &sentEvents{}
	a := newChatUserActions(ev.send)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }

	if _, ok := a.submit("   \r\n  "); ok {
		t.Fatal("blank line sent")
	}
	if shown, ok := a.submit("  salut\x00 \x1b[31mrouge\r\nligne 2\r\n"); !ok || shown != "  salut [31mrouge\r\nligne 2" {
		t.Fatalf("submit: %q %v", shown, ok)
	}
	long := strings.Repeat("é", chatNativeMaxInput+50)
	if shown, _ := a.submit(long); len([]rune(shown)) != chatNativeMaxInput {
		t.Fatalf("submit not capped: %d runes", len([]rune(shown)))
	}

	// Typing: one event per 2 s.
	if !a.typed() || a.typed() {
		t.Fatal("typing debounce")
	}
	now = now.Add(chatNativeTypingGap)
	if !a.typed() {
		t.Fatal("typing after the gap")
	}

	// Allow / Deny: only for a pending request, once.
	if a.answerRemote(true) {
		t.Fatal("answer without a request")
	}
	a.remoteRequested()
	if !a.answerRemote(false) || a.answerRemote(true) {
		t.Fatal("one answer per request")
	}
	a.remoteRequested()
	if !a.answerRemote(true) {
		t.Fatal("second request")
	}

	// Closed by the user: user_closed once, nothing after.
	if !a.userClosed() || a.userClosed() {
		t.Fatal("user_closed once")
	}
	if _, ok := a.submit("late"); ok || a.typed() {
		t.Fatal("nothing is sent after close")
	}
	want := strings.Join([]string{
		"user_message:  salut [31mrouge\r\nligne 2",
		"user_message:" + strings.Repeat("é", chatNativeMaxInput),
		"typing", "typing",
		"deny_remote", "allow_remote",
		"user_closed",
	}, "|")
	if ev.list() != want {
		t.Fatalf("events:\n got %q\nwant %q", ev.list(), want)
	}
}

// Closed by the service: the window goes away without a user_closed.
func TestChatUserActionsServiceClosed(t *testing.T) {
	ev := &sentEvents{}
	a := newChatUserActions(ev.send)
	a.remoteRequested()
	a.serviceClosed()
	if a.userClosed() || a.answerRemote(true) {
		t.Fatal("events after the service closed the chat")
	}
	if ev.list() != "" {
		t.Fatalf("sent %q", ev.list())
	}
}

func TestChatNativeText(t *testing.T) {
	if got := chatNativeCleanMessage("a\nb\rc\r\n\td\u202e\x7f\u0085e  \n\n"); got != "a\r\nb\r\nc\r\n d\u202ee" {
		t.Fatalf("clean: %q", got)
	}
	if chatNativeStripEmoji("\U0001F4CE Fichier reçu :") != "Fichier reçu :" {
		t.Fatal("emoji prefix")
	}
	if chatNativeNoRTF(`{\rtf1 x}`) != "\u200b"+`{\rtf1 x}` || chatNativeNoRTF("a{") != "a{" || chatNativeNoRTF("") != "" {
		t.Fatalf("no RTF: %q", chatNativeNoRTF(`{\rtf1 x}`))
	}
	head, body := chatNativeHistoryLine(time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC), "Op", "x\ny")
	if head != "[15:04] Op: " || body != "x\r\ny" {
		t.Fatalf("%q %q", head, body)
	}
}

// Service side: WebView2 missing but the native window exists -> the helper
// is spawned with --chat-ui=native, nothing reported unavailable.
func TestStartChatFallsBackToNativeWindow(t *testing.T) {
	f := installChatFakes(t)
	f.wv2OK = false
	f.wv2Status = &webview2Status{State: wv2StateBlocked}
	f.nativeOK = true
	ready := make(chan struct{})
	f.helper = func(addr string) {
		c := dialHelper(t, addr)
		if c == nil {
			return
		}
		helperEvent(c, map[string]string{"action": "ready"})
		close(ready)
		time.Sleep(200 * time.Millisecond)
		c.Close()
	}
	if err := startChat(nil, "c0ffee77", "Op", "", 1); err != nil {
		t.Fatalf("startChat: %v", err)
	}
	<-ready
	if len(f.cmdLines) != 1 || !strings.HasSuffix(f.cmdLines[0], "--chat-ui=native") {
		t.Fatalf("helper command line: %q", f.cmdLines)
	}
	select {
	case <-f.notified:
		t.Fatal("no 'chat cannot open' toast when the native window serves the chat")
	case <-time.After(300 * time.Millisecond):
	}
	for _, a := range f.sent.actions() {
		if a == "chat_unavailable" {
			t.Fatalf("reported unavailable: %v", f.sent.actions())
		}
	}
}

// Windows < 10: native at once, WebView2 not even checked.
func TestStartChatPrefersNativeOnOldWindows(t *testing.T) {
	f := installChatFakes(t)
	f.nativeOK, f.preferNative = true, true
	checked := false
	chatWebView2Check = func() (bool, *webview2Status) { checked = true; return true, nil }
	f.helper = func(addr string) {
		if c := dialHelper(t, addr); c != nil {
			c.Close()
		}
	}
	if err := startChat(nil, "c0ffee78", "Op", "", 1); err != nil {
		t.Fatalf("startChat: %v", err)
	}
	if checked {
		t.Fatal("WebView2 checked on Windows < 10")
	}
	if len(f.cmdLines) != 1 || !strings.Contains(f.cmdLines[0], "--chat-ui=native") {
		t.Fatalf("helper command line: %q", f.cmdLines)
	}
	stopChat("c0ffee78")
}

// Windows 10+ with a working runtime: the WebView2 page, no flag.
func TestStartChatKeepsWebView2(t *testing.T) {
	f := installChatFakes(t)
	f.nativeOK = true
	f.helper = func(addr string) {
		if c := dialHelper(t, addr); c != nil {
			c.Close()
		}
	}
	if err := startChat(nil, "c0ffee79", "Op", "", 1); err != nil {
		t.Fatalf("startChat: %v", err)
	}
	if len(f.cmdLines) != 1 || strings.Contains(f.cmdLines[0], "--chat-ui") {
		t.Fatalf("helper command line: %q", f.cmdLines)
	}
	stopChat("c0ffee79")
}
