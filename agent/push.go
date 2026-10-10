package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/oblireach/agent/engine"
)

const pushInterval = 30 * time.Second

type pushPayload struct {
	DeviceUUID string        `json:"deviceUuid"`
	Hostname   string        `json:"hostname"`
	OS         string        `json:"os"`
	Arch       string        `json:"arch"`
	Version    string        `json:"version"`
	Flavor     string        `json:"flavor,omitempty"`  // "modern" | "nt6" (build_flavor.go)
	OSBuild    string        `json:"osBuild,omitempty"` // "6.1.7601" (Windows only)
	IsServer   bool          `json:"isServer"`
	Sessions   []engine.SessionInfo `json:"sessions,omitempty"`
}

type pushResponse struct {
	Status  string   `json:"status"`
	Command *command `json:"command,omitempty"`
}

type command struct {
	Type    string                 `json:"type"`
	ID      string                 `json:"id"`
	Payload map[string]interface{} `json:"payload"`
}

func runPushLoop(cfg *Config) {
	client := &http.Client{Timeout: 15 * time.Second}

	for {
		if err := doPush(cfg, client); err != nil {
			log.Printf("Push failed: %v", err)
		}
		time.Sleep(pushInterval)
	}
}

func doPush(cfg *Config, client *http.Client) error {
	hostname, _ := os.Hostname()

	payload := pushPayload{
		DeviceUUID: currentDeviceUUID(),
		Hostname:   hostname,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Version:    agentVersion,
		Flavor:     agentFlavorName(),
		OSBuild:    engine.WindowsVersion().String(),
		IsServer:   engine.IsServer(),
		Sessions:   engine.EnumerateSessions(),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal push payload: %w", err)
	}

	req, err := http.NewRequest("POST", cfg.ServerURL+"/api/oblireach/push", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", cfg.APIKey)
	// Identity v2: announce the pending switch to the shared device ID.
	applyIdentityHeaders(req.Header)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("push HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("push: unauthorized (check API key)")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("push: server returned %d", resp.StatusCode)
	}
	// Identity v2: the server acknowledged the switch (X-Obli-Device-Id-Ack):
	// the runtime ID changes, onSwitch persists it and redials the WS.
	handleIdentityAck(resp)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("push: read body: %w", err)
	}

	var pr pushResponse
	if err := json.Unmarshal(data, &pr); err != nil {
		return fmt.Errorf("push: unmarshal response: %w", err)
	}

	if pr.Command != nil {
		go handleCommand(cfg, pr.Command)
	}

	return nil
}

func handleCommand(cfg *Config, cmd *command) {
	log.Printf("Command received: type=%s id=%s", cmd.Type, cmd.ID)

	switch cmd.Type {
	case "open_remote_tunnel":
		token, _ := cmd.Payload["sessionToken"].(string)
		if token == "" {
			log.Printf("Command %s: missing sessionToken", cmd.ID)
			return
		}
		// Optional sessionId from payload; -1 = use console session default.
		sessionID := -1
		if raw, ok := cmd.Payload["sessionId"]; ok {
			if f, ok := raw.(float64); ok {
				sessionID = int(f)
			}
		}
		// The user of the session actually streamed is notified by startStream,
		// once the target session is resolved (never a vanished id).
		username, _ := cmd.Payload["username"].(string)
		// files: whether the operator may transfer files in this session
		// (capability 'files' + privacy unlock, decided by Obliance — see
		// files_gate.go). Absent (older server): allowed, as before.
		files := engine.FilesAllowedFromPayload(cmd.Payload)
		if !files {
			log.Printf("Command %s: file transfers not permitted in this session", cmd.ID)
		}
		if err := startStream(cfg, token, sessionID, username, files); err != nil {
			log.Printf("Command %s: startStream failed: %v", cmd.ID, err)
		}

	case "close_remote_tunnel":
		token, _ := cmd.Payload["sessionToken"].(string)
		if token != "" {
			// Notify the user in the target session before stopping.
			username, _ := cmd.Payload["username"].(string)
			if username != "" {
				// Notify the session the stream is actually showing now (the
				// follow supervisor may have moved it), falling back to the
				// console when the stream is not tracked here.
				notifyTarget, ok := streamCurrentSession(token)
				if !ok {
					notifyTarget = engine.FindCaptureSession()
				}
				go notifySession(notifyTarget, username, false)
			}
			stopStream(token)
		}

	case "update":
		// Payload {version, url[, sha256, size]}. One attempt at a time,
		// minimum gap and back-off: see update_guard.go / update_selfupdate.go.
		// handleCommand already runs in its own goroutine.
		requestSelfUpdate(cfg, cmd)

	case "webview2_install":
		// Payload {source:"microsoft", force?}: install the Microsoft Edge
		// WebView2 runtime of the chat window, per machine, with the
		// Microsoft-signed bootstrapper shipped next to the agent. One
		// attempt at a time + back-off (force: the device page's button):
		// see webview2_guard.go.
		requestWebView2Install(cmd)

	case "open_chat":
		chatID := payloadString(cmd.Payload, "chatId")
		operatorName := payloadString(cmd.Payload, "operatorName")
		sessionID := -1
		if raw, ok := cmd.Payload["sessionId"]; ok {
			if f, ok := raw.(float64); ok {
				sessionID = int(f)
			}
		}
		if chatID == "" {
			log.Printf("Command %s: missing chatId", cmd.ID)
			return
		}
		operatorAvatar := payloadString(cmd.Payload, "operatorAvatar")
		if err := startChat(cfg, chatID, operatorName, operatorAvatar, sessionID); err != nil {
			log.Printf("Command %s: startChat failed: %v", cmd.ID, err)
		}

	case "chat_message":
		chatID := payloadString(cmd.Payload, "chatId")
		message := payloadString(cmd.Payload, "message")
		operatorName := payloadString(cmd.Payload, "operatorName")
		ts := int64(0)
		if raw, ok := cmd.Payload["timestamp"]; ok {
			if f, ok := raw.(float64); ok {
				ts = int64(f)
			}
		}
		forwardChatMessage(chatID, operatorName, message, ts)

	case "close_chat":
		chatID := payloadString(cmd.Payload, "chatId")
		stopChat(chatID)

	case "request_remote":
		chatID := payloadString(cmd.Payload, "chatId")
		message := payloadString(cmd.Payload, "message")
		forwardRemoteRequest(chatID, message)

	case "chat_typing":
		chatID := payloadString(cmd.Payload, "chatId")
		forwardChatTyping(chatID)

	case "chat_file":
		chatID := payloadString(cmd.Payload, "chatId")
		forwardChatFile(chatID, cmd.Payload)

	default:
		log.Printf("Command %s: unknown type %q", cmd.ID, cmd.Type)
	}
}

func payloadString(payload map[string]interface{}, key string) string {
	v, _ := payload[key].(string)
	return v
}
