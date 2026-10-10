package main

// proxy_vault_test.go — captureSessionResponse and reachTunnelToken: the
// scope-resolution logic that feeds the vault's session registry. These are
// unit-tested directly against an *http.Response the way ModifyResponse
// builds one; the full WebSocket handshake path (handleWSProxy) reuses
// relayClientFrames / wsInjector, already covered end to end in
// reach_tunnels_test.go, and is not re-driven here through a live TCP
// handshake in this pass.

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func fakeSessionResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     http.Header{},
	}
}

func TestCaptureSessionResponseRegistersOblireach(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	body := `{"data":{"id":1,"protocol":"oblireach","tenantId":1,"deviceId":42,"deviceTenantId":5,"sessionToken":"tok-abc_123"}}`
	resp := fakeSessionResponse(t, 201, body)
	p.captureSessionResponse(resp, "https://server.example")

	tun, _, ok := p.tunnels.lookup("tok-abc_123")
	if !ok {
		t.Fatalf("expected the session to be registered")
	}
	if tun.tenant != 5 || tun.origin != "https://server.example" || tun.deviceID != 42 {
		t.Fatalf("unexpected tunnel: %+v", tun)
	}

	// The body must reach the client unchanged.
	got, _ := io.ReadAll(resp.Body)
	if string(got) != body {
		t.Fatalf("body mutated: got %q want %q", got, body)
	}
}

func TestCaptureSessionResponseGodViewWithoutDeviceTenantIsUnknown(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	// Master-tenant (1) session, NO deviceTenantId field at all — an older
	// server that has not shipped the addition yet.
	body := `{"data":{"protocol":"oblireach","tenantId":1,"deviceId":1,"sessionToken":"tokX"}}`
	p.captureSessionResponse(fakeSessionResponse(t, 201, body), "https://a")

	tun, _, ok := p.tunnels.lookup("tokX")
	if !ok || tun.tenant != 0 {
		t.Fatalf("expected tenant=0 (unknown) on a god-view session with no deviceTenantId, got %+v ok=%v", tun, ok)
	}
}

func TestCaptureSessionResponseNonMasterSessionTenantIsTrusted(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	body := `{"data":{"protocol":"oblireach","tenantId":7,"deviceId":1,"sessionToken":"tokY"}}`
	p.captureSessionResponse(fakeSessionResponse(t, 201, body), "https://a")
	tun, _, ok := p.tunnels.lookup("tokY")
	if !ok || tun.tenant != 7 {
		t.Fatalf("expected tenant=7 from a non-master session tenant, got %+v ok=%v", tun, ok)
	}
}

func TestCaptureSessionResponseIgnoresNonOblireach(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	body := `{"data":{"protocol":"ssh","tenantId":1,"deviceId":1,"deviceTenantId":5,"sessionToken":"tokZ"}}`
	p.captureSessionResponse(fakeSessionResponse(t, 201, body), "https://a")
	if _, _, ok := p.tunnels.lookup("tokZ"); ok {
		t.Fatalf("a non-oblireach protocol must never be registered")
	}
}

func TestCaptureSessionResponseRejectsBadToken(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	body := `{"data":{"protocol":"oblireach","tenantId":1,"deviceId":1,"deviceTenantId":5,"sessionToken":"has spaces/slash"}}`
	p.captureSessionResponse(fakeSessionResponse(t, 201, body), "https://a")
	if _, _, ok := p.tunnels.lookup("has spaces/slash"); ok {
		t.Fatalf("a malformed token must never be registered")
	}
}

func TestCaptureSessionResponseOversizedBodyPassedThroughIntact(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	big := bytes.Repeat([]byte("x"), 70*1024)
	resp := fakeSessionResponse(t, 201, string(big))
	p.captureSessionResponse(resp, "https://a")
	got, _ := io.ReadAll(resp.Body)
	if len(got) != len(big) || !bytes.Equal(got, big) {
		t.Fatalf("oversized body must reach the client byte-identical, got %d bytes want %d", len(got), len(big))
	}
}

func TestCaptureSessionResponseMalformedJSONPassedThrough(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	resp := fakeSessionResponse(t, 201, "not json")
	p.captureSessionResponse(resp, "https://a")
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "not json" {
		t.Fatalf("malformed body must still reach the client unchanged, got %q", got)
	}
}

func TestReachTunnelTokenExactPath(t *testing.T) {
	cases := []struct {
		path string
		tok  string
		ok   bool
	}{
		{"/api/remote/tunnel/abc123", "abc123", true},
		{"/api/remote/tunnel/", "", false},
		{"/api/remote/tunnel/a/b", "", false},
		{"/api/remote/sessions", "", false},
		{"/api/remote/tunnel", "", false},
	}
	for _, c := range cases {
		tok, ok := reachTunnelToken(c.path)
		if tok != c.tok || ok != c.ok {
			t.Fatalf("reachTunnelToken(%q) = (%q, %v), want (%q, %v)", c.path, tok, ok, c.tok, c.ok)
		}
	}
}

func TestValidSessionToken(t *testing.T) {
	if !validSessionToken("abc-DEF_123") {
		t.Fatalf("expected a normal token to be valid")
	}
	for _, bad := range []string{"", "has space", "has/slash", string(make([]byte, 600))} {
		if validSessionToken(bad) {
			t.Fatalf("expected %q to be invalid", bad)
		}
	}
}

// deviceUuid (a server that sends it) is recorded for the vault's device
// restriction; absent or malformed, the device is matched by id only.
func TestCaptureSessionResponseDeviceUUID(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	for tok, c := range map[string]struct{ uuid, want string }{
		"tokU1": {`,"deviceUuid":"3f1c-AB_9"`, "3f1c-AB_9"},
		"tokU2": {``, ""},
		"tokU3": {`,"deviceUuid":"bad uuid/../"`, ""},
	} {
		body := `{"data":{"protocol":"oblireach","tenantId":1,"deviceId":42,"deviceTenantId":5,"sessionToken":"` + tok + `"` + c.uuid + `}}`
		p.captureSessionResponse(fakeSessionResponse(t, 201, body), "https://a")
		tun, _, ok := p.tunnels.lookup(tok)
		if !ok || tun.deviceUUID != c.want || tun.deviceID != 42 {
			t.Errorf("%s: %+v ok=%v, want uuid %q", tok, tun, ok, c.want)
		}
		if s := tun.vaultSession(); s.Origin != "https://a" || s.TenantID != 5 || s.DeviceID != 42 || s.DeviceUUID != c.want {
			t.Errorf("%s: vaultSession %+v", tok, s)
		}
	}
}
