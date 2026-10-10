package main

// nt6 build line (Windows 7 SP1 / 8.1 / Server 2008 R2 / 2012 / 2012 R2):
// the version decisions, the build flavor, the heartbeat fields and the
// self-update refusals (sha2_unsupported, wrong_flavor). Platform-neutral:
// every decision is a pure function of a WinVersion.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/oblireach/agent/engine"
)

var (
	win7SP1   = engine.WinVersion{Major: 6, Minor: 1, Build: 7601, SPMajor: 1}
	win2008R2 = engine.WinVersion{Major: 6, Minor: 1, Build: 7601, SPMajor: 1, Server: true}
	win2012   = engine.WinVersion{Major: 6, Minor: 2, Build: 9200, Server: true}
	win81     = engine.WinVersion{Major: 6, Minor: 3, Build: 9600}
	win2012R2 = engine.WinVersion{Major: 6, Minor: 3, Build: 9600, Server: true}
	win10     = engine.WinVersion{Major: 10, Minor: 0, Build: 19045}
	win2025   = engine.WinVersion{Major: 10, Minor: 0, Build: 26100, Server: true}
	winVista  = engine.WinVersion{Major: 6, Minor: 0, Build: 6002}
	unknownOS = engine.WinVersion{}
)

func TestAgentFlavor(t *testing.T) {
	saved := agentFlavor
	t.Cleanup(func() { agentFlavor = saved })
	agentFlavor = ""
	if isNT6Build() || agentFlavorName() != "modern" {
		t.Fatalf("default flavor: %q", agentFlavorName())
	}
	agentFlavor = "nt6" // -ldflags "-X main.agentFlavor=nt6"
	if !isNT6Build() || agentFlavorName() != "nt6" {
		t.Fatalf("nt6 flavor: %q", agentFlavorName())
	}
}

func TestHeartbeatCarriesFlavorAndOSBuild(t *testing.T) {
	saved := agentFlavor
	t.Cleanup(func() { agentFlavor = saved })
	agentFlavor = "nt6"
	hb, _ := json.Marshal(cmdHeartbeatMsg{Type: "heartbeat", Flavor: agentFlavorName(), OSBuild: win7SP1.String()})
	push, _ := json.Marshal(pushPayload{Flavor: agentFlavorName(), OSBuild: win2012R2.String()})
	for name, b := range map[string]string{"heartbeat": string(hb), "push": string(push)} {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
		if m["flavor"] != "nt6" || m["osBuild"] == "" || m["osBuild"] == nil {
			t.Fatalf("%s: %s", name, b)
		}
	}
	// Off Windows (no OS build): the field is left out.
	b, _ := json.Marshal(cmdHeartbeatMsg{Type: "heartbeat", Flavor: "modern", OSBuild: unknownOS.String()})
	if strings.Contains(string(b), "osBuild") {
		t.Fatalf("empty osBuild must be omitted: %s", b)
	}
}

func TestSelfUpdateFlavorCheck(t *testing.T) {
	for _, c := range []struct {
		url      string
		needsNT6 bool
		want     string
	}{
		{"/api/agent/download/oblireach-agent.msi", false, ""},
		{"/api/agent/download/oblireach-agent-nt6.msi", true, ""},
		{"/api/agent/download/oblireach-agent.msi", true, "wrong_flavor"},
		{"/api/agent/download/OBLIREACH-AGENT.MSI?x=1", true, "wrong_flavor"},
		{"/api/agent/download/oblireach-agent-nt6.msi", false, "wrong_flavor"},
		{"/api/agent/download/oblireach-agent-nt6.msi#frag", false, "wrong_flavor"},
		{"/api/agent/download/oblireach-agent-darwin-arm64", true, ""},
		{"", true, ""},
	} {
		if got := selfUpdateFlavorCheck(c.url, c.needsNT6); got != c.want {
			t.Errorf("selfUpdateFlavorCheck(%q, nt6=%v) = %q, want %q", c.url, c.needsNT6, got, c.want)
		}
	}
}

// Windows 7 / 2008 R2 without KB4474419: a clear sha2_unsupported, never the
// package launched.
func TestSelfUpdateSHA2Unsupported(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	selfUpdateSHA2Missing = func() bool { return true }
	env.sigErr = errors.New("invalid Authenticode signature: 0x80096010 content does not match the signature")
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex(body))

	requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("auto_update_ws_sha2", "1.0.200"))

	if env.launches.Load() != 0 {
		t.Fatal("launched an unverified package")
	}
	st := env.state(t)
	if st.LastReason != "sha2_unsupported" || !strings.Contains(st.LastError, "sha2_unsupported") ||
		!strings.Contains(st.LastError, "KB4474419") || !strings.Contains(st.LastError, "0x80096010") {
		t.Fatalf("state: reason=%q error=%q", st.LastReason, st.LastError)
	}
	if s := selfUpdate.status(); s == nil || s.LastReason != "sha2_unsupported" {
		t.Fatalf("heartbeat status: %+v", s)
	}
}

// A real signature failure on a system that verifies SHA-2: no reason code.
func TestSelfUpdateSignatureFailureKeepsNoReason(t *testing.T) {
	res := selfUpdateSignatureFailure(errors.New("signed by \"X\""), false)
	if res.Reason != "" || res.Stage != "signature" || res.Outcome != outcomeFailed {
		t.Fatalf("%+v", res)
	}
}

// The nt6 machine is sent the modern package (old / misconfigured server):
// refused before anything is downloaded.
func TestSelfUpdateWrongFlavorNeverDownloads(t *testing.T) {
	env := newSelfUpdateEnv(t, "1.0.199")
	selfUpdateNeedsNT6 = func() bool { return true }
	body := fakeMSI(100 << 10)
	srv := newPkgServer(t, body, shaHex(body))

	requestSelfUpdate(&Config{ServerURL: srv.URL, APIKey: "k"}, updateCmd("auto_update_ws_flavor", "1.0.200"))

	if srv.hits.Load() != 0 || env.launches.Load() != 0 {
		t.Fatalf("wrong flavor: %d downloads, %d launches", srv.hits.Load(), env.launches.Load())
	}
	if st := env.state(t); st.LastReason != "wrong_flavor" || !strings.Contains(st.LastError, "oblireach-agent-nt6.msi") {
		t.Fatalf("state: reason=%q error=%q", st.LastReason, st.LastError)
	}
}

func TestMinMaxGo120Semantics(t *testing.T) {
	// Builtins (Go 1.21+) or minmax_go120.go (Go 1.20): same results.
	if min(3, 1, 2) != 1 || max(3, 1, 2) != 3 || min(5) != 5 || max(-1, 0) != 0 {
		t.Fatal("min / max")
	}
	if max(2-5, 0, 7-9) != 0 || min(1.5, -2.5) != -2.5 {
		t.Fatal("min / max mixed")
	}
}
