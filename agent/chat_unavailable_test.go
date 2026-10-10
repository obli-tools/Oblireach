package main

import (
	"bytes"
	"encoding/json"
	"net"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Chat handshake with the service: a chat that never showed is reported as
// chat_unavailable (exact reason), never as "the user closed it".

type sentLog struct {
	mu   sync.Mutex
	msgs []map[string]interface{}
	ch   chan struct{}
}

func (s *sentLog) send(b []byte) error {
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	s.mu.Lock()
	s.msgs = append(s.msgs, m)
	s.mu.Unlock()
	select {
	case s.ch <- struct{}{}:
	default:
	}
	return nil
}

func (s *sentLog) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		p, _ := m["payload"].(map[string]interface{})
		a, _ := p["action"].(string)
		out = append(out, a)
	}
	return out
}

func (s *sentLog) payload(i int) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _ := s.msgs[i]["payload"].(map[string]interface{})
	return p
}

// waitFor waits until n messages were sent (or fails).
func (s *sentLog) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		s.mu.Lock()
		got := len(s.msgs)
		s.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-s.ch:
		case <-deadline:
			t.Fatalf("expected %d messages to the server, got %v", n, s.actions())
		}
	}
}

type chatFakes struct {
	sent       *sentLog
	notified   chan bool
	spawnRC    int
	helper     func(addr string) // fake helper process
	wv2OK      bool
	wv2Status  *webview2Status
	spawnCalls int
	sessUser   string // the session's user name the service reads

	nativeOK     bool     // the native chat window exists (chatNativeAvailable)
	preferNative bool     // Windows < 10 (chatPreferNative)
	cmdLines     []string // command lines of the spawned helpers
}

var addrRe = regexp.MustCompile(`--addr=(127\.0\.0\.1:\d+)`)

func installChatFakes(t *testing.T) *chatFakes {
	t.Helper()
	f := &chatFakes{sent: &sentLog{ch: make(chan struct{}, 64)}, notified: make(chan bool, 4), wv2OK: true}
	saved := []interface{}{chatSendToServer, chatWebView2Check, chatSpawnHelper, notifyChatUnavailable, chatAcceptTimeout, chatSessionUserName,
		chatNativeAvailable, chatPreferNative}
	t.Cleanup(func() {
		chatNativeAvailable = saved[6].(func() bool)
		chatPreferNative = saved[7].(func() bool)
		chatSendToServer = saved[0].(func([]byte) error)
		chatWebView2Check = saved[1].(func() (bool, *webview2Status))
		chatSpawnHelper = saved[2].(func(int, string) (uint32, int))
		notifyChatUnavailable = saved[3].(func(int, bool))
		chatAcceptTimeout = saved[4].(time.Duration)
		chatSessionUserName = saved[5].(func(int) string)
	})
	chatSendToServer = f.sent.send
	chatSessionUserName = func(int) string { return f.sessUser }
	chatWebView2Check = func() (bool, *webview2Status) { return f.wv2OK, f.wv2Status }
	// WebView2 only by default (the tests below this point predate the
	// native window); chat_native_test.go turns it on.
	chatNativeAvailable = func() bool { return f.nativeOK }
	chatPreferNative = func() bool { return f.preferNative }
	notifyChatUnavailable = func(_ int, installing bool) { f.notified <- installing }
	chatSpawnHelper = func(_ int, cmdLine string) (uint32, int) {
		f.spawnCalls++
		f.cmdLines = append(f.cmdLines, cmdLine)
		if f.spawnRC != 0 {
			return 0, f.spawnRC
		}
		m := addrRe.FindStringSubmatch(cmdLine)
		if m == nil {
			t.Fatalf("no --addr in %q", cmdLine)
		}
		if f.helper != nil {
			go f.helper(m[1])
		}
		return 1234, 0
	}
	return f
}

// dialHelper connects like the helper and reads the init message.
func dialHelper(t *testing.T, addr string) net.Conn {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Errorf("helper dial: %v", err)
		return nil
	}
	if typ, _, err := chatPipeRecv(c); err != nil || typ != chatPipeInit {
		t.Errorf("helper init: type=%d err=%v", typ, err)
	}
	return c
}

func helperEvent(c net.Conn, m map[string]string) {
	b, _ := json.Marshal(m)
	chatPipeSend(c, chatPipeEvent, b)
}

func TestChatUnavailableWebView2Missing(t *testing.T) {
	f := installChatFakes(t)
	f.wv2OK = false
	f.wv2Status = &webview2Status{State: wv2StateInstalling, Installer: true}
	err := startChat(nil, "c0ffee01", "Op", "", 1)
	if err != errChatUnavailable {
		t.Fatalf("startChat: %v", err)
	}
	if f.spawnCalls != 0 {
		t.Fatal("no helper is spawned without the runtime")
	}
	if got := f.sent.actions(); len(got) != 2 || got[0] != "chat_unavailable" || got[1] != "user_closed" {
		t.Fatalf("messages: %v", got)
	}
	p := f.sent.payload(0)
	if p["reason"] != "webview2_missing" || p["installing"] != true || p["state"] != "installing" {
		t.Fatalf("chat_unavailable payload: %v", p)
	}
	if fb := f.sent.payload(1)["fallback"]; fb != "chat_unavailable" {
		t.Fatalf("legacy user_closed must be marked as a fallback: %v", f.sent.payload(1))
	}
	select {
	case inst := <-f.notified:
		if !inst {
			t.Fatal("toast must say the install is running")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the session's user gets a toast")
	}
}

func TestChatUnavailableSpawnFailed(t *testing.T) {
	f := installChatFakes(t)
	f.spawnRC = 5
	if err := startChat(nil, "c0ffee02", "Op", "", 1); err == nil {
		t.Fatal("spawn failure must fail startChat")
	}
	if got := f.sent.actions(); len(got) < 1 || got[0] != "chat_unavailable" || f.sent.payload(0)["reason"] != "helper_failed" {
		t.Fatalf("messages: %v", got)
	}
}

func TestChatHelperGoneBeforeReady(t *testing.T) {
	f := installChatFakes(t)
	f.helper = func(addr string) {
		if c := dialHelper(t, addr); c != nil {
			c.Close() // e.g. go-webview2 log.Fatalf, 45 s deadline
		}
	}
	if err := startChat(nil, "c0ffee03", "Op", "", 1); err != nil {
		t.Fatal(err)
	}
	f.sent.waitFor(t, 2)
	if got := f.sent.actions(); got[0] != "chat_unavailable" || f.sent.payload(0)["reason"] != "helper_failed" || got[1] != "user_closed" {
		t.Fatalf("messages: %v", got)
	}
}

func TestChatHelperReportsUnavailable(t *testing.T) {
	f := installChatFakes(t)
	release := make(chan struct{})
	f.helper = func(addr string) {
		c := dialHelper(t, addr)
		if c == nil {
			return
		}
		helperEvent(c, map[string]string{"action": "unavailable", "reason": "webview2_failed", "detail": "WebView2 154 is installed but did not start"})
		<-release // the toast stays on screen a while
		c.Close()
	}
	if err := startChat(nil, "c0ffee04", "Op", "", 1); err != nil {
		t.Fatal(err)
	}
	f.sent.waitFor(t, 2) // reported at once, before the helper exits
	p := f.sent.payload(0)
	if p["action"] != "chat_unavailable" || p["reason"] != "webview2_failed" || p["detail"] != "WebView2 154 is installed but did not start" {
		t.Fatalf("payload: %v", p)
	}
	close(release)
	time.Sleep(300 * time.Millisecond)
	if got := f.sent.actions(); len(got) != 2 {
		t.Fatalf("nothing more after the exit: %v", got)
	}
}

func TestChatHelperReadyThenClosed(t *testing.T) {
	f := installChatFakes(t)
	f.helper = func(addr string) {
		c := dialHelper(t, addr)
		if c == nil {
			return
		}
		helperEvent(c, map[string]string{"action": "ready"})
		helperEvent(c, map[string]string{"action": "typing"})
		time.Sleep(100 * time.Millisecond)
		c.Close() // window gone after it was shown
	}
	if err := startChat(nil, "c0ffee05", "Op", "", 1); err != nil {
		t.Fatal(err)
	}
	f.sent.waitFor(t, 2)
	time.Sleep(200 * time.Millisecond)
	got := f.sent.actions()
	if len(got) != 2 || got[0] != "typing" || got[1] != "user_closed" {
		t.Fatalf("a chat that was shown ends as user_closed (ready is not forwarded): %v", got)
	}
}

// The helper runs as the session's user: only the user's own events go on,
// rebuilt — a forged chat_unavailable (service-reserved), an unknown action
// or extra fields never reach the server.
func TestChatHelperEventsWhitelisted(t *testing.T) {
	f := installChatFakes(t)
	f.helper = func(addr string) {
		c := dialHelper(t, addr)
		if c == nil {
			return
		}
		helperEvent(c, map[string]string{"action": "ready"})
		helperEvent(c, map[string]string{"action": "chat_unavailable", "reason": "webview2_missing", "detail": "call +1 555 0100"})
		helperEvent(c, map[string]string{"action": "file_transfer", "fileName": "x.exe"})
		helperEvent(c, map[string]string{"action": "typing", "extra": "<b>"})
		b, _ := json.Marshal(map[string]interface{}{"action": "deny_remote", "allowed": true})
		chatPipeSend(c, chatPipeEvent, b)
		b, _ = json.Marshal(map[string]interface{}{"action": "allow_remote", "allowed": false})
		chatPipeSend(c, chatPipeEvent, b)
		helperEvent(c, map[string]string{"action": "user_closed", "fallback": "chat_unavailable"})
		time.Sleep(100 * time.Millisecond)
		c.Close()
	}
	if err := startChat(nil, "c0ffee0a", "Op", "", 1); err != nil {
		t.Fatal(err)
	}
	f.sent.waitFor(t, 4)
	time.Sleep(300 * time.Millisecond)
	if got := f.sent.actions(); !reflect.DeepEqual(got, []string{"typing", "deny_remote", "allow_remote", "user_closed"}) {
		t.Fatalf("only the user's own events are forwarded: %v", got)
	}
	want := []map[string]interface{}{
		{"action": "typing"},
		{"action": "deny_remote", "allowed": false},
		{"action": "allow_remote", "allowed": true},
		{"action": "user_closed"},
	}
	for i, w := range want {
		if p := f.sent.payload(i); !reflect.DeepEqual(p, w) {
			t.Fatalf("event %d rebuilt from its action: got %v, want %v", i, p, w)
		}
	}
	for _, a := range []string{"ready", "unavailable", "chat_unavailable", "file_transfer", "", "operator_message"} {
		if chatUserEvent(a) != nil {
			t.Fatalf("chatUserEvent(%q) must be nil", a)
		}
	}
}

// A user message: the sender's name is the session's user as the SERVICE reads
// it — the helper (running as that user) cannot claim another name; its own
// name only when the session has none (capped).
func TestChatMessageSenderFromService(t *testing.T) {
	for _, c := range []struct{ sess, claimed, want string }{
		{"jdoe", "Administrator", "jdoe"},
		{"", "alice", "alice"},
		{"", strings.Repeat("x", 500), strings.Repeat("x", 100) + "…"},
	} {
		f := installChatFakes(t)
		f.sessUser = c.sess
		claimed := c.claimed
		f.helper = func(addr string) {
			conn := dialHelper(t, addr)
			if conn == nil {
				return
			}
			helperEvent(conn, map[string]string{"action": "ready"})
			b, _ := json.Marshal(map[string]interface{}{"action": "user_message", "text": "hello", "from": claimed})
			chatPipeSend(conn, chatPipeMsg, b)
			b, _ = json.Marshal(map[string]interface{}{"action": "user_message", "text": 42, "from": claimed})
			chatPipeSend(conn, chatPipeMsg, b)
			time.Sleep(100 * time.Millisecond)
			conn.Close()
		}
		if err := startChat(nil, "c0ffee0b", "Op", "", 1); err != nil {
			t.Fatal(err)
		}
		f.sent.waitFor(t, 3)
		p := f.sent.payload(0)
		if p["from"] != c.want || p["text"] != "hello" {
			t.Fatalf("session user %q, helper claims %q: from=%v text=%v, want from %q", c.sess, c.claimed, p["from"], p["text"], c.want)
		}
		if p := f.sent.payload(1); p["text"] != "" {
			t.Fatalf("a non-string text goes on as empty text: %v", p["text"])
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestChatHelperUserClosedOnce(t *testing.T) {
	f := installChatFakes(t)
	f.helper = func(addr string) {
		c := dialHelper(t, addr)
		if c == nil {
			return
		}
		helperEvent(c, map[string]string{"action": "ready"})
		helperEvent(c, map[string]string{"action": "user_closed"})
		time.Sleep(100 * time.Millisecond)
		c.Close()
	}
	if err := startChat(nil, "c0ffee06", "Op", "", 1); err != nil {
		t.Fatal(err)
	}
	f.sent.waitFor(t, 1)
	time.Sleep(400 * time.Millisecond)
	if got := f.sent.actions(); len(got) != 1 || got[0] != "user_closed" {
		t.Fatalf("user_closed reported once: %v", got)
	}
}

func TestChatStoppedByOperatorBeforeReady(t *testing.T) {
	f := installChatFakes(t)
	connected := make(chan net.Conn, 1)
	f.helper = func(addr string) {
		if c := dialHelper(t, addr); c != nil {
			connected <- c
		}
	}
	if err := startChat(nil, "c0ffee07", "Op", "", 1); err != nil {
		t.Fatal(err)
	}
	c := <-connected
	defer c.Close()
	time.Sleep(100 * time.Millisecond)
	stopChat("c0ffee07")
	time.Sleep(400 * time.Millisecond)
	if got := f.sent.actions(); len(got) != 0 {
		t.Fatalf("an operator's close is not reported back: %v", got)
	}
}

func TestChatHelperNeverConnects(t *testing.T) {
	f := installChatFakes(t)
	chatAcceptTimeout = 200 * time.Millisecond
	if err := startChat(nil, "c0ffee08", "Op", "", 1); err != nil {
		t.Fatal(err)
	}
	f.sent.waitFor(t, 1)
	if got := f.sent.actions(); got[0] != "chat_unavailable" || f.sent.payload(0)["reason"] != "helper_failed" {
		t.Fatalf("messages: %v", got)
	}
}

func TestHelperUnavailableReason(t *testing.T) {
	for in, want := range map[string]string{
		"webview2_missing": "webview2_missing", "webview2_failed": "webview2_failed",
		"webview2_timeout": "webview2_timeout", "": "helper_failed", "bogus<script>": "helper_failed",
	} {
		if got := helperUnavailableReason(in); got != want {
			t.Fatalf("%q → %q", in, got)
		}
	}
}

// Nobody signed in to the session: the chat window (which runs as the
// session's user) cannot open — reported as no_user, no toast (nobody to see it).
func TestChatUnavailableNoUser(t *testing.T) {
	f := installChatFakes(t)
	f.spawnRC = chatSpawnNoUser
	if err := startChat(nil, "c0ffee0a", "Op", "", 1); err != errChatUnavailable {
		t.Fatalf("startChat: %v", err)
	}
	if got := f.sent.actions(); len(got) != 2 || got[0] != "chat_unavailable" || got[1] != "user_closed" {
		t.Fatalf("messages: %v", got)
	}
	if p := f.sent.payload(0); p["reason"] != chatUnavailNoUser || p["installing"] != false {
		t.Fatalf("chat_unavailable payload: %v", p)
	}
	select {
	case <-f.notified:
		t.Fatal("no toast without a signed-in user")
	case <-time.After(100 * time.Millisecond):
	}
}

// The chat window is started AS the session's user, never with the
// service's SYSTEM token (spawnInSessionGo, the capture helper's path).
func TestChatHelperSpawnsAsSessionUser(t *testing.T) {
	if reflect.ValueOf(chatSpawnHelper).Pointer() != reflect.ValueOf(spawnChatHelperAsUser).Pointer() {
		t.Fatal("chatSpawnHelper must be spawnChatHelperAsUser")
	}
	if reflect.ValueOf(chatSpawnHelper).Pointer() == reflect.ValueOf(spawnInSessionGo).Pointer() {
		t.Fatal("the chat window must never run with the SYSTEM token")
	}
}

// A message from the helper (which runs as the session's user) above the
// bound is refused before any allocation of its announced size.
func TestChatPipeRecvMaxRefusesHuge(t *testing.T) {
	var b bytes.Buffer
	hdr := []byte{0xff, 0xff, 0xff, 0x7f, chatPipeEvent} // announces ~2 GiB
	b.Write(hdr)
	if _, _, err := chatPipeRecvMax(&b, chatHelperMaxPayload); err != errChatPipeTooLarge {
		t.Fatalf("err=%v, want errChatPipeTooLarge", err)
	}
	b.Reset()
	chatPipeSend(&b, chatPipeEvent, []byte(`{"action":"typing"}`))
	if typ, p, err := chatPipeRecvMax(&b, chatHelperMaxPayload); err != nil || typ != chatPipeEvent || string(p) != `{"action":"typing"}` {
		t.Fatalf("normal message: %d %q %v", typ, p, err)
	}
}
