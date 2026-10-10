package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oblireach/agent/engine"
)

// ── Chat pipe message types ──────────────────────────────────────────────────

const (
	chatPipeMsg   = byte(0x10) // bidirectional: JSON chat message
	chatPipeStop  = byte(0x11) // service → helper: close chat
	chatPipeEvent = byte(0x12) // helper → service: user events
	chatPipeInit  = byte(0x13) // service → helper: init with avatar data
)

// ── ChatSession ──────────────────────────────────────────────────────────────

type ChatSession struct {
	chatID         string
	operatorName   string
	operatorAvatar string // data URI (base64)
	sessionID      int
	conn           net.Conn
	stopCh         chan struct{}
	once           sync.Once

	// Handshake with the helper (relayFromHelper's goroutine only): the
	// helper says "ready" once its page has loaded. A helper that goes away
	// before that never showed the chat: the server is told the chat could
	// not open (chat_unavailable), never that the user closed it.
	ready      bool
	userClosed bool   // the helper already reported user_closed
	senderName string // the session's user name, as the service reads it ("" until looked up)
	reported   bool   // chat_unavailable already sent
}

// Reasons of chat_unavailable (agent → server, chat_event payload.reason).
const (
	chatUnavailWebView2Missing = "webview2_missing" // runtime not installed (checked before spawning)
	chatUnavailWebView2Failed  = "webview2_failed"  // runtime found but WebView2 did not start in the helper
	chatUnavailWebView2Timeout = "webview2_timeout" // WebView2 still not up after chatEmbedDeadline
	chatUnavailHelperFailed    = "helper_failed"    // the helper could not be started / stopped before it was ready
	chatUnavailNoUser          = "no_user"          // nobody is signed in to the session (the chat runs as its user)
)

// chatSpawnNoUser: chatSpawnHelper's code when nobody is signed in to the
// session (-ERROR_NO_TOKEN from WTSQueryUserToken).
const chatSpawnNoUser = -1008

// chatHelperMaxPayload bounds one message from the helper (a user's line of
// text, an event): the helper runs as the session's user, who may send
// anything on its connection — never a multi-GB allocation in the service.
const chatHelperMaxPayload = 1 << 20

// errChatUnavailable: startChat refused because the chat window cannot
// open (already reported to the server).
var errChatUnavailable = fmt.Errorf("chat: the chat window cannot open on this machine")

// Hooks (replaced in tests).
var (
	chatSendToServer      = cmdWSSend                     // agent → server message
	chatWebView2Check     = webview2ChatCheck             // runtime present? (webview2_guard.go)
	chatSpawnHelper       = spawnChatHelperAsUser         // helper process in the user's session, AS that user (chat_spawn_windows.go)
	notifyChatUnavailable = platformNotifyChatUnavailable // toast to the session's user
	chatAcceptTimeout     = 15 * time.Second              // helper connection deadline
	chatSessionUserName   = platformChatSessionUserName   // the session's user name, as the service reads it
	chatNativeAvailable   = platformChatNativeAvailable   // the native Win32 chat window exists here (chat_native_windows.go)
	chatPreferNative      = platformChatPreferNative      // Windows < 10: native window rather than WebView2 109
)

// reportChatUnavailable tells the server the chat could not open (it ends
// the conversation with an exact reason instead of "the user closed it").
// A user_closed follows for servers that do not know chat_unavailable: they
// drop the new action and still end the chat; a current server ignores it
// (the chat already ended).
func reportChatUnavailable(chatID, reason string, installing bool, state, detail string) {
	if len(detail) > 300 {
		detail = capText(detail, 300)
	}
	msg, _ := json.Marshal(map[string]interface{}{
		"type":   "chat_event",
		"chatId": chatID,
		"payload": map[string]interface{}{
			"action":     "chat_unavailable",
			"reason":     reason,
			"installing": installing,
			"state":      state,
			"detail":     detail,
		},
	})
	_ = chatSendToServer(msg)
	legacy, _ := json.Marshal(map[string]interface{}{
		"type":    "chat_event",
		"chatId":  chatID,
		"payload": map[string]interface{}{"action": "user_closed", "fallback": "chat_unavailable"},
	})
	_ = chatSendToServer(legacy)
	log.Printf("Chat %s: cannot open on this machine (%s, installing=%v, state=%s) %s", chatID, reason, installing, state, detail)
}

// chatUserEvent: the chat_event payload forwarded for a helper event (the
// user closed the window, is typing, answered a remote-control request);
// nil for anything else. Rebuilt, never the helper's own map.
func chatUserEvent(action string) map[string]interface{} {
	switch action {
	case "user_closed", "typing":
		return map[string]interface{}{"action": action}
	case "allow_remote":
		return map[string]interface{}{"action": action, "allowed": true}
	case "deny_remote":
		return map[string]interface{}{"action": action, "allowed": false}
	}
	return nil
}

// helperUnavailableReason: a reason the helper may report ("unavailable"
// pipe event); anything else counts as helper_failed.
func helperUnavailableReason(r string) string {
	switch r {
	case chatUnavailWebView2Missing, chatUnavailWebView2Failed, chatUnavailWebView2Timeout:
		return r
	}
	return chatUnavailHelperFailed
}

var activeChats sync.Map // chatID → *ChatSession

// chatLastParams stores the last known spawn parameters for each chat,
// so we can respawn the helper even after the user closed it.
type chatSpawnParams struct {
	operatorName   string
	operatorAvatar string
	sessionID      int
}

var chatLastParams sync.Map // chatID → *chatSpawnParams

func (cs *ChatSession) stop() {
	cs.once.Do(func() {
		close(cs.stopCh)
		if cs.conn != nil {
			cs.conn.Close()
		}
		activeChats.Delete(cs.chatID)
		log.Printf("Chat %s: stopped", cs.chatID)
	})
}

// validChatID: the server's chat ids are random hex. Anything else is refused
// before it reaches a command line.
func validChatID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// chatHelperCmdLine: the helper's command line. SECURITY: nothing chosen by an
// operator goes on it — the operator's display name (free text from the
// Obliance client) travels in the chatPipeInit message instead; a quote in it
// used to close the quoted --operator value and add helper arguments. addr is
// the local listener's "127.0.0.1:port", exe the service's own path.
func chatHelperCmdLine(exe, addr, chatID, ui string) (string, error) {
	if !validChatID(chatID) {
		return "", fmt.Errorf("chat: invalid chat id")
	}
	cmd := fmt.Sprintf(`"%s" --chat-helper --addr=%s --chat-id=%s`, exe, addr, chatID)
	if ui == chatUINative { // a fixed value, never operator text
		cmd += " --chat-ui=" + chatUINative
	}
	return cmd, nil
}

// startChat spawns a chat helper in the target session and sets up the pipe relay.
func startChat(cfg *Config, chatID, operatorName, operatorAvatar string, sessionID int) error {
	if !validChatID(chatID) {
		return fmt.Errorf("chat: invalid chat id")
	}
	if sessionID < 0 {
		sessionID = engine.FindCaptureSession()
	}

	// Which window: the WebView2 page, or the native Win32 window on
	// Windows < 10 (WebView2 Runtime 109 there, no longer patched) and
	// wherever the runtime is missing / blocked (chatUIDecision). Only when
	// neither can open is the chat reported unavailable (server: exact
	// reason; user: a toast).
	preferNative := chatPreferNative()
	wv2OK, st := true, (*webview2Status)(nil)
	if !preferNative {
		wv2OK, st = chatWebView2Check()
	}
	ui, uiOK := chatUIDecision(preferNative, wv2OK, chatNativeAvailable())
	if !uiOK {
		installing, state, detail := false, "", ""
		if st != nil {
			installing = st.State == wv2StateInstalling
			state = st.State
			detail = st.LastError
		}
		reportChatUnavailable(chatID, chatUnavailWebView2Missing, installing, state, detail)
		go notifyChatUnavailable(sessionID, installing)
		return errChatUnavailable
	}
	if ui == chatUINative {
		why := "Windows older than 10"
		if !preferNative {
			why = "WebView2 runtime not usable"
			if st != nil {
				why += " (" + st.State + ")"
			}
		}
		log.Printf("Chat %s: native chat window (%s)", chatID, why)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("chat: listen: %w", err)
	}
	addr := ln.Addr().String()
	log.Printf("Chat %s: listening on %s (session %d)", chatID, addr, sessionID)

	exe, err := executablePath()
	if err != nil {
		ln.Close()
		return fmt.Errorf("chat: executable: %w", err)
	}

	cmdLine, err := chatHelperCmdLine(exe, addr, chatID, ui)
	if err != nil {
		ln.Close()
		return err
	}

	pid, rc := chatSpawnHelper(sessionID, cmdLine)
	if rc == chatSpawnNoUser {
		ln.Close()
		reportChatUnavailable(chatID, chatUnavailNoUser, false, "",
			fmt.Sprintf("nobody is signed in to session %d", sessionID))
		return errChatUnavailable
	}
	if rc != 0 {
		ln.Close()
		reportChatUnavailable(chatID, chatUnavailHelperFailed, false, "",
			fmt.Sprintf("the chat window program could not be started in session %d (code %d)", sessionID, rc))
		return fmt.Errorf("chat: spawn in session %d failed (code %d)", sessionID, rc)
	}
	log.Printf("Chat %s: spawned helper PID %d in session %d", chatID, pid, sessionID)

	cs := &ChatSession{
		chatID:         chatID,
		operatorName:   operatorName,
		operatorAvatar: operatorAvatar,
		sessionID:      sessionID,
		stopCh:         make(chan struct{}),
	}
	activeChats.Store(chatID, cs)

	// Remember spawn params for respawn after user-close
	chatLastParams.Store(chatID, &chatSpawnParams{
		operatorName:   operatorName,
		operatorAvatar: operatorAvatar,
		sessionID:      sessionID,
	})

	// Accept the helper connection in background
	go func() {
		defer cs.stop()
		defer ln.Close()

		ln.(*net.TCPListener).SetDeadline(time.Now().Add(chatAcceptTimeout))
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Chat %s: accept failed: %v", chatID, err)
			select {
			case <-cs.stopCh: // closed by the operator meanwhile
			default:
				reportChatUnavailable(chatID, chatUnavailHelperFailed, false, "",
					"the chat window program did not connect back to the service")
			}
			return
		}
		cs.conn = conn
		log.Printf("Chat %s: helper connected", chatID)

		// Send init data (including avatar) to helper
		initData, _ := json.Marshal(map[string]string{
			"operatorName":   cs.operatorName,
			"operatorAvatar": cs.operatorAvatar,
		})
		chatPipeSend(conn, chatPipeInit, initData)

		// Read events from helper and forward to command WS
		cs.relayFromHelper()
	}()

	return nil
}

func stopChat(chatID string) {
	if v, ok := activeChats.Load(chatID); ok {
		cs := v.(*ChatSession)
		// Send stop signal to helper
		if cs.conn != nil {
			chatPipeSend(cs.conn, chatPipeStop, nil)
		}
		cs.stop()
	}
}

func forwardChatMessage(chatID, operatorName, message string, timestamp int64) {
	msg, _ := json.Marshal(map[string]interface{}{
		"action":       "operator_message",
		"text":         message,
		"operatorName": operatorName,
		"timestamp":    timestamp,
	})

	v, ok := activeChats.Load(chatID)
	if ok {
		cs := v.(*ChatSession)
		// Try to send — if the pipe is dead (user closed), respawn the helper
		if cs.conn != nil && chatPipeSend(cs.conn, chatPipeMsg, msg) == nil {
			return // sent OK
		}
		log.Printf("Chat %s: helper disconnected, respawning for new message", chatID)
		cs.stop()
	} else {
		log.Printf("Chat %s: not active, respawning for incoming message", chatID)
	}

	// Resolve spawn params: use saved params, fall back to what we have
	spawnName := operatorName
	spawnAvatar := ""
	spawnSession := -1
	if p, ok := chatLastParams.Load(chatID); ok {
		sp := p.(*chatSpawnParams)
		if spawnName == "" {
			spawnName = sp.operatorName
		}
		spawnAvatar = sp.operatorAvatar
		spawnSession = sp.sessionID
	}

	go func() {
		if err := startChat(nil, chatID, spawnName, spawnAvatar, spawnSession); err != nil {
			log.Printf("Chat %s: respawn failed: %v", chatID, err)
			return
		}
		// Wait for helper to connect, then send the pending message
		time.Sleep(2 * time.Second)
		if v2, ok := activeChats.Load(chatID); ok {
			cs2 := v2.(*ChatSession)
			if cs2.conn != nil {
				chatPipeSend(cs2.conn, chatPipeMsg, msg)
			}
		}
	}()
}

func forwardChatTyping(chatID string) {
	v, ok := activeChats.Load(chatID)
	if !ok {
		return
	}
	cs := v.(*ChatSession)
	if cs.conn == nil {
		return
	}

	msg, _ := json.Marshal(map[string]string{"action": "operator_typing"})
	chatPipeSend(cs.conn, chatPipeMsg, msg)
}

func forwardChatFile(chatID string, payload map[string]interface{}) {
	v, ok := activeChats.Load(chatID)
	if !ok {
		return
	}
	cs := v.(*ChatSession)
	if cs.conn == nil {
		return
	}

	msg, _ := json.Marshal(map[string]interface{}{
		"action":   "file_transfer",
		"fileName": payload["fileName"],
		"fileSize": payload["fileSize"],
		"fileData": payload["fileData"],
	})
	chatPipeSend(cs.conn, chatPipeMsg, msg)
}

func forwardRemoteRequest(chatID, message string) {
	v, ok := activeChats.Load(chatID)
	if !ok {
		return
	}
	cs := v.(*ChatSession)
	if cs.conn == nil {
		return
	}

	msg, _ := json.Marshal(map[string]interface{}{
		"action":  "request_remote",
		"message": message,
	})
	chatPipeSend(cs.conn, chatPipeMsg, msg)
}

// relayFromHelper reads pipe messages from the chat helper and forwards to the server.
func (cs *ChatSession) relayFromHelper() {
	for {
		select {
		case <-cs.stopCh:
			return
		default:
		}

		cs.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		msgType, payload, err := chatPipeRecvMax(cs.conn, chatHelperMaxPayload)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("Chat %s: helper disconnected: %v", cs.chatID, err)
			select {
			case <-cs.stopCh:
				return // closed on our side (operator, respawn): nothing to report
			default:
			}
			if cs.reported || cs.userClosed {
				return // the server already knows
			}
			if !cs.ready {
				// Gone before its page loaded (go-webview2's fatal errors, the
				// WebView2 start deadline, a crash): the chat never showed.
				reportChatUnavailable(cs.chatID, chatUnavailHelperFailed, false, "",
					"the chat window program stopped before the chat was shown")
				return
			}
			// Notify server that user closed
			agentMsg, _ := json.Marshal(map[string]interface{}{
				"type":   "chat_event",
				"chatId": cs.chatID,
				"payload": map[string]interface{}{
					"action": "user_closed",
				},
			})
			chatSendToServer(agentMsg)
			return
		}

		if msgType == chatPipeEvent {
			// Forward user event to server
			var event map[string]interface{}
			if json.Unmarshal(payload, &event) == nil {
				action, _ := event["action"].(string)
				switch action {
				case "ready":
					// Handshake only (not forwarded): the chat page is up.
					cs.ready = true
					log.Printf("Chat %s: helper ready (chat window shown)", cs.chatID)
					continue
				case "unavailable":
					// The helper cannot show the chat (WebView2): reported at
					// once — it may keep a toast on screen a while before exiting.
					if !cs.reported && !cs.ready {
						cs.reported = true
						reason, _ := event["reason"].(string)
						detail, _ := event["detail"].(string)
						reportChatUnavailable(cs.chatID, helperUnavailableReason(reason), false, "", detail)
					}
					continue
				case "user_closed":
					cs.userClosed = true
				}
				// The helper runs as the session's user (chat_spawn_windows.go):
				// only the user's own events go on, rebuilt from their action —
				// never a service-reserved one (chat_unavailable) nor extra fields.
				fwd := chatUserEvent(action)
				if fwd == nil {
					log.Printf("Chat %s: helper event %q not forwarded", cs.chatID, capText(action, 40))
					continue
				}
				agentMsg, _ := json.Marshal(map[string]interface{}{
					"type":    "chat_event",
					"chatId":  cs.chatID,
					"payload": fwd,
				})
				chatSendToServer(agentMsg)
			}
		} else if msgType == chatPipeMsg {
			// User message → forward to server
			var msg map[string]interface{}
			if json.Unmarshal(payload, &msg) == nil {
				// The sender's name: the session's user as the SERVICE reads it
				// (the helper runs as that user and could claim any name); the
				// helper's own only when the session has none.
				if cs.senderName == "" {
					cs.senderName = capText(strings.TrimSpace(chatSessionUserName(cs.sessionID)), 100)
				}
				from := cs.senderName
				if from == "" {
					s, _ := msg["from"].(string)
					from = capText(s, 100)
				}
				text, _ := msg["text"].(string)
				agentMsg, _ := json.Marshal(map[string]interface{}{
					"type":   "chat_message",
					"chatId": cs.chatID,
					"payload": map[string]interface{}{
						"text":      text,
						"from":      from,
						"timestamp": time.Now().UnixMilli(),
					},
				})
				chatSendToServer(agentMsg)
			}
		}
	}
}

// executablePath returns the path of the current executable.
func executablePath() (string, error) {
	return os.Executable()
}

// ── Chat pipe framing (same format as capture pipe) ─────────────────────────

func chatPipeSend(w io.Writer, msgType byte, payload []byte) error {
	hdr := make([]byte, 5)
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(payload)))
	hdr[4] = msgType
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

func chatPipeRecv(r io.Reader) (msgType byte, payload []byte, err error) {
	return chatPipeRecvMax(r, 0)
}

// errChatPipeTooLarge: a message above the reader's bound (the connection is
// then dropped: its framing cannot be trusted any more).
var errChatPipeTooLarge = fmt.Errorf("chat pipe: message too large")

// chatPipeRecvMax reads one message; max > 0 refuses a payload above max bytes.
func chatPipeRecvMax(r io.Reader, max uint32) (msgType byte, payload []byte, err error) {
	hdr := make([]byte, 5)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	length := binary.LittleEndian.Uint32(hdr[0:4])
	msgType = hdr[4]
	if max > 0 && length > max {
		return 0, nil, errChatPipeTooLarge
	}
	if length > 0 {
		payload = make([]byte, length)
		if _, err = io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return msgType, payload, nil
}
