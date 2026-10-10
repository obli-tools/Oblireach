package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ValidateResult is returned by the token validators.
type ValidateResult struct {
	Valid     bool
	Role      PeerRole
	SessionID string
	DeviceID  int
	TenantID  int
}

// validateViewerToken validates a short-lived viewer HMAC token that Obliance
// embeds in every session it creates.
//
// Format: "<sessionToken>.<expireUnix>.<hmac-sha256-hex>"
// The HMAC covers "<sessionToken>.<expireUnix>" signed with cfg.RelaySecret.
//
// Obliance must generate matching tokens using the same secret and format.
func validateViewerToken(rawToken string, cfg Config) (string, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid token format")
	}
	sessionToken, expireStr, sig := parts[0], parts[1], parts[2]

	// Verify signature
	mac := hmac.New(sha256.New, []byte(cfg.RelaySecret))
	mac.Write([]byte(sessionToken + "." + expireStr))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return "", fmt.Errorf("invalid token signature")
	}

	// Check expiry
	var expireTs int64
	if _, err := fmt.Sscanf(expireStr, "%d", &expireTs); err != nil {
		return "", fmt.Errorf("invalid expiry")
	}
	if time.Now().Unix() > expireTs {
		return "", fmt.Errorf("token expired")
	}

	return sessionToken, nil
}

// validateAgentSession calls the Obliance API to confirm that:
//   - the sessionToken corresponds to an active session,
//   - the device identified by apiKey is the one the session targets.
//
// Returns the session's raw token (= relay pairing key) on success.
func validateAgentSession(sessionToken, apiKey, oblianceURL string, httpClient *http.Client) (string, error) {
	url := oblianceURL + "/api/remote/relay/validate-agent"

	body, _ := json.Marshal(map[string]string{
		"sessionToken": sessionToken,
		"apiKey":       apiKey,
	})

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("obliance call failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("obliance rejected agent: %s", strings.TrimSpace(string(respBody)))
	}

	var result struct {
		Valid bool   `json:"valid"`
		Token string `json:"sessionToken"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil || !result.Valid {
		return "", fmt.Errorf("invalid response from obliance")
	}

	return result.Token, nil
}
