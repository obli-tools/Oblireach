package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oblireach/supportlink"
)

// ── Fakes ───────────────────────────────────────────────────────────────────

type memSupportStore struct {
	mu   sync.Mutex
	data []byte
	fail bool
}

func (m *memSupportStore) Load() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return nil, errors.New("broken")
	}
	return append([]byte(nil), m.data...), nil
}

func (m *memSupportStore) Save(p []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("broken")
	}
	m.data = append([]byte(nil), p...)
	return nil
}

type supFrame struct {
	op byte
	p  []byte
}

// fakeSupSess: a session whose host side is the test.
type fakeSupSess struct {
	toPage   chan supFrame // what the host sends
	fromPage chan supFrame // what the page sent
	done     chan struct{}
	once     sync.Once
	err      error
	local    bool
	path     supportlink.Path
	sas      string
}

func newFakeSupSess() *fakeSupSess {
	return &fakeSupSess{toPage: make(chan supFrame, 64), fromPage: make(chan supFrame, 64), done: make(chan struct{}),
		path: supportlink.PathDirect, sas: "47 12"}
}

func (f *fakeSupSess) end(local bool, err error) {
	f.once.Do(func() {
		f.err, f.local = err, local
		close(f.done)
	})
}

func (f *fakeSupSess) ReadFrame() (byte, []byte, error) {
	select {
	case fr := <-f.toPage:
		return fr.op, fr.p, nil
	default:
	}
	select {
	case fr := <-f.toPage:
		return fr.op, fr.p, nil
	case <-f.done:
		return 0, nil, f.err
	}
}

func (f *fakeSupSess) WriteFrame(op byte, p []byte) error {
	select {
	case <-f.done:
		return supportlink.ErrClosed
	default:
	}
	f.fromPage <- supFrame{op, append([]byte(nil), p...)}
	return nil
}

func (f *fakeSupSess) Close() error           { f.end(true, io.EOF); return nil }
func (f *fakeSupSess) Path() supportlink.Path { return f.path }
func (f *fakeSupSess) SAS() string            { return f.sas }
func (f *fakeSupSess) closed() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}
func (f *fakeSupSess) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(3 * time.Second):
		t.Fatal("session not closed")
	}
}

const supOrigin = "http://127.0.0.1:5555"

func newTestSupportHub(dial supportDialFunc) (*supportHub, *fakeTimers, *httptest.Server) {
	h := newSupportHub(&memSupportStore{}, dial)
	ft := &fakeTimers{}
	h.after = ft.after
	h.userName = func() string { return "operator" }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveHTTP(w, r, supOrigin) }))
	return h, ft, srv
}

func supPost(t *testing.T, srv *httptest.Server, path string, body interface{}, origin string) (int, map[string]interface{}) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/local/support/"+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func supGet(t *testing.T, srv *httptest.Server, path string) (int, map[string]interface{}, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/local/support/" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]interface{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, string(raw)
}

// ── Settings ────────────────────────────────────────────────────────────────

func TestNormalizeSupportURL(t *testing.T) {
	for _, c := range []struct{ in, want, err string }{
		{"support.example.com", "https://support.example.com", ""},
		{"  HTTPS://Support.Example.com/ ", "https://support.example.com", ""},
		{"wss://rdv.example.com:8443/v1/ws", "wss://rdv.example.com:8443/v1/ws", ""},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", ""},
		{"ws://localhost:9000", "ws://localhost:9000", ""},
		{"http://support.example.com", "", "url_plain"},
		{"ws://203.0.113.9", "", "url_plain"},
		{"ftp://support.example.com", "", "url_invalid"},
		{"https://user:pw@support.example.com", "", "url_invalid"},
		{"https://support.example.com/?x=1", "", "url_invalid"},
		{"https://", "", "url_invalid"},
		{"", "", "url_invalid"},
		{"support example.com", "", "url_invalid"},
		{"https://" + strings.Repeat("a", 600) + ".com", "", "url_invalid"},
	} {
		got, err := normalizeSupportURL(c.in)
		code := ""
		var se *supportSettingsError
		if errors.As(err, &se) {
			code = se.Code
		}
		if got != c.want || code != c.err {
			t.Errorf("%q: got %q / %q, want %q / %q", c.in, got, code, c.want, c.err)
		}
	}
}

// supportUserServers: the servers the user added (not the built-in one).
func supportUserServers(list []supportServer) []supportServer {
	var out []supportServer
	for _, s := range list {
		if s.ID != supportPublicID {
			out = append(out, s)
		}
	}
	return out
}

func supportBuiltinOf(t *testing.T, list []supportServer) supportServer {
	t.Helper()
	for _, s := range list {
		if s.ID == supportPublicID {
			return s
		}
	}
	t.Fatalf("no built-in server in %+v", list)
	return supportServer{}
}

func TestSupportSettings(t *testing.T) {
	store := &memSupportStore{}
	s := &supportSettings{store: store}
	// Nothing saved: the built-in public server alone, the default, no key.
	list, err := s.list()
	if err != nil || len(list) != 1 || list[0].ID != supportPublicID || list[0].URL != supportPublicURL || !list[0].Default || list[0].Key != "" || !list[0].view(time.Now()).Builtin {
		t.Fatalf("empty settings: %+v %v", list, err)
	}
	// A server without a key: kept (enough to receive help, receive_help.go),
	// not usable to connect by code.
	if list, err := s.save(supportServerInput{URL: "nokey.example.com"}); err != nil || len(supportUserServers(list)) != 1 || supportUserServers(list)[0].Key != "" || supportUserServers(list)[0].view(time.Now()).HasKey {
		t.Fatalf("a new server without a key: %v %+v", err, list)
	} else if _, err := s.remove(supportUserServers(list)[0].ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.save(supportServerInput{URL: "support.example.com", Key: "  secret-key-1  "})
	if err != nil || len(supportUserServers(list)) != 1 {
		t.Fatal(list, err)
	}
	a := supportUserServers(list)[0]
	if a.Label != "support.example.com" || a.URL != "https://support.example.com" || a.Key != "secret-key-1" || !a.Default || !reSupportID.MatchString(a.ID) {
		t.Fatalf("first server %+v (the first one added is the default, not the built-in one)", a)
	}
	if b := supportBuiltinOf(t, list); b.Default {
		t.Fatalf("built-in still the default: %+v", b)
	}
	list, err = s.save(supportServerInput{Label: "Lab\x07 two", URL: "wss://lab.example.net", Key: "k2", Default: true})
	if u := supportUserServers(list); err != nil || len(u) != 2 || u[0].Default || !u[1].Default || u[1].Label != "Lab two" {
		t.Fatalf("second server, default moved: %+v %v", list, err)
	}
	// An edit with an empty key keeps the saved one.
	list, err = s.save(supportServerInput{ID: a.ID, Label: "Main", URL: "support.example.com"})
	if u := supportUserServers(list); err != nil || u[0].Key != "secret-key-1" || u[0].Label != "Main" {
		t.Fatalf("edit keeps the key: %+v %v", list, err)
	}
	// The built-in one: its key and default can be set, not its name nor
	// its address; it cannot be removed.
	list, err = s.save(supportServerInput{ID: supportPublicID, Label: "Mine", URL: "evil.example.com", Key: "pub-key", Default: true})
	if b := supportBuiltinOf(t, list); err != nil || b.Key != "pub-key" || b.Label != supportPublicLabel || b.URL != supportPublicURL || !b.Default {
		t.Fatalf("built-in edit: %+v %v", b, err)
	}
	if _, err := s.remove(supportPublicID); err == nil || err.(*supportSettingsError).Code != "builtin" {
		t.Fatalf("built-in removed: %v", err)
	}
	if list, _ = s.list(); !supportBuiltinOf(t, list).Default || supportBuiltinOf(t, list).Key != "pub-key" {
		t.Fatalf("built-in choice not kept: %+v", list)
	}
	// Another one chosen: the built-in one is no longer the default.
	list, err = s.save(supportServerInput{ID: a.ID, URL: "support.example.com", Default: true})
	if err != nil || supportBuiltinOf(t, list).Default || !supportUserServers(list)[0].Default {
		t.Fatalf("default back to a user server: %+v %v", list, err)
	}
	list = supportUserServers(list)
	if _, err := s.save(supportServerInput{ID: "0123456789abcdef", URL: "x.example"}); err == nil || err.(*supportSettingsError).Code != "not_found" {
		t.Fatalf("edit of an unknown id: %v", err)
	}
	if _, err := s.save(supportServerInput{URL: "x.example", Key: "a\nb"}); err == nil {
		t.Fatal("a key with a control character")
	}
	// Removing the default one: the next added is the default.
	list, err = s.remove(list[0].ID)
	if u := supportUserServers(list); err != nil || len(u) != 1 || !u[0].Default || supportBuiltinOf(t, list).Default {
		t.Fatalf("remove: %+v %v", list, err)
	}
	if _, err := s.remove("nope"); err == nil {
		t.Fatal("remove unknown")
	}
	// Bounded list.
	for i := 0; i < supportMaxServers-1; i++ {
		if _, err := s.save(supportServerInput{URL: fmt.Sprintf("s%d.example.com", i), Key: "k"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.save(supportServerInput{URL: "one-more.example.com", Key: "k"}); err == nil || err.(*supportSettingsError).Code != "too_many" {
		t.Fatalf("too many: %v", err)
	}
	// What the store holds is what the settings read back.
	got, _, _ := (&supportSettings{store: store}).get(supportUserServers(list)[0].ID)
	if got.Key != "k2" || got.Label != "Lab two" {
		t.Fatalf("reloaded %+v", got)
	}
	store.fail = true
	if _, err := s.list(); err == nil || err.(*supportSettingsError).Code != "storage" {
		t.Fatalf("broken store: %v", err)
	}
}

// The page gets hasKey, never a key; posts need the local Origin; a peer
// that is not loopback gets nothing.
func TestSupportSettingsHTTP(t *testing.T) {
	_, _, srv := newTestSupportHub(nil)
	defer srv.Close()
	if code, _ := supPost(t, srv, "servers/save", map[string]interface{}{"url": "support.example.com", "key": "TOPSECRETKEY"}, ""); code != http.StatusForbidden {
		t.Fatalf("no Origin: %d", code)
	}
	if code, _ := supPost(t, srv, "servers/save", map[string]interface{}{"url": "support.example.com", "key": "TOPSECRETKEY"}, "http://evil.example"); code != http.StatusForbidden {
		t.Fatalf("other Origin: %d", code)
	}
	code, out := supPost(t, srv, "servers/save", map[string]interface{}{"label": "Help desk", "url": "support.example.com", "key": "TOPSECRETKEY"}, supOrigin)
	if code != 200 {
		t.Fatal(code, out)
	}
	code, out = supPost(t, srv, "servers/save", map[string]interface{}{"url": "http://support.example.com", "key": "k"}, supOrigin)
	if code != 400 || out["error"] != "url_plain" {
		t.Fatal(code, out)
	}
	code, out, raw := supGet(t, srv, "servers")
	if code != 200 || strings.Contains(raw, "TOPSECRETKEY") || strings.Contains(raw, `"key"`) {
		t.Fatalf("the key reached the page: %s", raw)
	}
	servers := out["servers"].([]interface{})
	first := servers[0].(map[string]interface{})
	pub := servers[1].(map[string]interface{})
	if len(servers) != 2 || first["hasKey"] != true || first["label"] != "Help desk" || first["default"] != true || first["builtin"] != nil || out["defaultName"] != "operator" {
		t.Fatalf("list %s", raw)
	}
	if pub["id"] != supportPublicID || pub["builtin"] != true || pub["hasKey"] != false || pub["url"] != supportPublicURL || pub["default"] != false {
		t.Fatalf("built-in entry %s", raw)
	}
	if code, out := supPost(t, srv, "servers/remove", map[string]string{"id": supportPublicID}, supOrigin); code != 400 || out["error"] != "builtin" {
		t.Fatalf("built-in removed: %d %v", code, out)
	}
	code, out = supPost(t, srv, "servers/remove", map[string]string{"id": first["id"].(string)}, supOrigin)
	if code != 200 || len(out["servers"].([]interface{})) != 1 {
		t.Fatal(code, out)
	}

	h := newSupportHub(&memSupportStore{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/local/support/servers", nil)
	req.RemoteAddr = "203.0.113.5:4000"
	h.serveHTTP(rec, req, supOrigin)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote peer: %d", rec.Code)
	}
}

// ── Error mapping ───────────────────────────────────────────────────────────

func TestSupportErrCode(t *testing.T) {
	for _, c := range []struct {
		err   error
		code  string
		retry int
	}{
		{supportlink.ErrWrongCode, "wrong_code", 0},
		{fmt.Errorf("wrapped: %w", supportlink.ErrMITM), "mitm", 0},
		{fmt.Errorf("%w: candidate x", supportlink.ErrMITM), "mitm", 0},
		{&supportlink.ServerError{Code: "id_locked", Retry: 900 * time.Second}, "id_locked", 900},
		{&supportlink.ServerError{Code: "unknown_id"}, "unknown_id", 0},
		{supportlink.ErrRejected, "rejected", 0},
		{&supportlink.ServerError{Code: "expired"}, "expired", 0},
		{&supportlink.ServerError{Code: "busy"}, "busy", 0},
		{&supportlink.ServerError{Code: "rate_limited", Retry: 1500 * time.Millisecond}, "rate_limited", 2},
		{&supportlink.ServerError{Code: "operator_required"}, "key_refused", 0},
		{&supportlink.ServerError{Code: "banned", Retry: time.Hour}, "banned", 3600},
		{&supportlink.ServerError{Code: "upgrade_required"}, "upgrade_required", 0},
		{&supportlink.UpgradeError{Side: supportlink.UpgradeClient, MinVersion: "1.2.0", ServerProto: 2}, "app_too_old", 0},
		{fmt.Errorf("x: %w", &supportlink.UpgradeError{Side: supportlink.UpgradeServer, ServerProto: 1}), "server_too_old", 0},
		{supportlink.ErrPeerGone, "peer_gone", 0},
		{&supportlink.ServerError{Code: "relay_quota"}, "relay_quota", 0},
		{&supportlink.ServerError{Code: "something_new"}, "server_error", 0},
		{supportlink.ErrInvalidID, "invalid_id", 0},
		{supportlink.ErrInvalidCode, "invalid_code", 0},
		{context.DeadlineExceeded, "timeout", 0},
		{context.Canceled, "cancelled", 0},
		{fmt.Errorf("%w: %w", supportlink.ErrUnreachable, errors.New("dial tcp: connection refused")), "unreachable", 0},
		{fmt.Errorf("%w: signalling connection lost", supportlink.ErrUnreachable), "unreachable", 0},
		{fmt.Errorf("%w: refusing plain ws:// to a remote server (use wss://)", supportlink.ErrInvalidServer), "url_invalid", 0},
		{errors.New("supportlink: signalling closed"), "network", 0}, // (untyped: no string matching)
		{errors.New("supportlink: peer connection timed out"), "network", 0},
	} {
		code, retry := supportErrCode(c.err)
		if code != c.code || retry != c.retry {
			t.Errorf("%v: got %s/%d, want %s/%d", c.err, code, retry, c.code, c.retry)
		}
	}
}

// ── Setup flow ──────────────────────────────────────────────────────────────

func addTestServer(t *testing.T, h *supportHub) string {
	t.Helper()
	list, err := h.settings.save(supportServerInput{Label: "Lab", URL: "support.example.com", Key: "op-key"})
	if err != nil {
		t.Fatal(err)
	}
	return supportUserServers(list)[0].ID
}

func waitSupState(t *testing.T, srv *httptest.Server, conn, want string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, out, _ := supGet(t, srv, "connect/"+conn)
		if out["state"] == want {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %v, want %s", out, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSupportConnectFlow(t *testing.T) {
	sess := newFakeSupSess()
	sess.path = supportlink.PathRelayUDP
	release := make(chan struct{})
	var got supportlink.OperatorConfig
	var gotID, gotCode string
	h, _, srv := newTestSupportHub(func(ctx context.Context, cfg supportlink.OperatorConfig, id, code string) (supportSess, error) {
		got, gotID, gotCode = cfg, id, code
		cfg.OnAwaitingApproval("47 12")
		select {
		case <-release:
			return sess, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	defer srv.Close()
	sid := addTestServer(t, h)

	// Refused before anything starts.
	for _, c := range []struct {
		body map[string]string
		want string
	}{
		{map[string]string{"server": sid, "id": "12345678", "code": "123456789"}, "invalid_id"},
		{map[string]string{"server": sid, "id": "123456789", "code": "12345"}, "invalid_code"},
		{map[string]string{"server": "0000000000000000", "id": "123456789", "code": "123456789"}, "no_server"},
	} {
		if code, out := supPost(t, srv, "connect", c.body, supOrigin); code != 400 || out["error"] != c.want {
			t.Errorf("%v: %d %v", c.body, code, out)
		}
	}
	if code, _ := supPost(t, srv, "connect", map[string]string{"server": sid, "id": "482913507", "code": "123456789"}, ""); code != 403 {
		t.Fatalf("connect without Origin: %d", code)
	}

	code, out := supPost(t, srv, "connect", map[string]string{"server": sid, "id": "482 913 507", "code": "123-456-789", "name": "Op\x01 Name"}, supOrigin)
	if code != 200 {
		t.Fatal(code, out)
	}
	conn := out["conn"].(string)
	st := waitSupState(t, srv, conn, "awaiting")
	if st["sas"] != "47 12" || st["token"] != nil {
		t.Fatalf("awaiting %v", st)
	}
	if gotID != "482913507" || gotCode != "123456789" || got.Key != "op-key" || got.Name != "Op Name" || got.Server != "https://support.example.com" {
		t.Fatalf("dial got %q %q %+v", gotID, gotCode, got)
	}
	close(release)
	st = waitSupState(t, srv, conn, "ready")
	if st["path"] != "relay_udp" || !reParkToken.MatchString(st["token"].(string)) {
		t.Fatalf("ready %v", st)
	}
	if _, waiting, _ := h.counts(); waiting != 1 {
		t.Fatalf("tunnel not registered: %d", waiting)
	}
	// Cancel once ready but before the tunnel is opened: the session closes.
	supPost(t, srv, "cancel", map[string]string{"conn": conn}, supOrigin)
	sess.waitClosed(t)
	if _, waiting, _ := h.counts(); waiting != 0 {
		t.Fatal("tunnel kept after cancel")
	}
}

func TestSupportConnectFailAndCancel(t *testing.T) {
	var mode string
	h, _, srv := newTestSupportHub(func(ctx context.Context, cfg supportlink.OperatorConfig, id, code string) (supportSess, error) {
		if mode == "wrong" {
			return nil, supportlink.ErrWrongCode
		}
		cfg.OnAwaitingApproval("11 22")
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer srv.Close()
	sid := addTestServer(t, h)
	mode = "wrong"
	_, out := supPost(t, srv, "connect", map[string]string{"server": sid, "id": "482913507", "code": "000000000"}, supOrigin)
	st := waitSupState(t, srv, out["conn"].(string), "failed")
	if st["error"] != "wrong_code" {
		t.Fatalf("failed %v", st)
	}
	mode = "wait"
	_, out = supPost(t, srv, "connect", map[string]string{"server": sid, "id": "482913507", "code": "000000000"}, supOrigin)
	conn := out["conn"].(string)
	waitSupState(t, srv, conn, "awaiting")
	supPost(t, srv, "cancel", map[string]string{"conn": conn}, supOrigin)
	waitSupState(t, srv, conn, "cancelled")
	// A server without key never dials.
	h2 := newSupportHub(&memSupportStore{}, nil)
	if _, err := h2.start(supportServer{URL: "https://x.example"}, "482913507", "000000000", "n"); err == nil || err.(*supportStartError).Code != "login_required" {
		t.Fatalf("no key: %v", err)
	}
}

// ── The tunnel ──────────────────────────────────────────────────────────────

// registerTunnel puts an established session behind a token, as finish does.
func registerTunnel(h *supportHub, sess supportSess) string {
	c := &supportConn{id: randomToken(), state: supStateConnecting, cancel: func() {}}
	h.mu.Lock()
	h.conns[c.id] = c
	h.mu.Unlock()
	h.finish(c, sess, nil)
	v, _ := h.state(c.id)
	return v.Token
}

func readCloseFrame(t *testing.T, c net.Conn, rd *bufio.Reader) (uint16, string) {
	t.Helper()
	for {
		f, err := readFrame(t, c, rd)
		if err != nil {
			t.Fatalf("waiting for the close frame: %v", err)
		}
		if f.op == 0x8 {
			if len(f.payload) < 2 {
				return 0, ""
			}
			return binary.BigEndian.Uint16(f.payload), string(f.payload[2:])
		}
	}
}

func TestSupportTunnelGates(t *testing.T) {
	h, _, srv := newTestSupportHub(nil)
	defer srv.Close()
	sess := newFakeSupSess()
	tok := registerTunnel(h, sess)
	if st, _, _ := wsDial(t, srv, "/local/support/tunnel/"+tok, ""); st != http.StatusForbidden {
		t.Fatalf("no Origin: %d", st)
	}
	if st, _, _ := wsDial(t, srv, "/local/support/tunnel/"+tok, "http://evil.example"); st != http.StatusForbidden {
		t.Fatalf("foreign Origin: %d", st)
	}
	if st, _, _ := wsDial(t, srv, "/local/support/tunnel/../x", supOrigin); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Fatalf("bad token: %d", st)
	}
	if st, _, _ := wsDial(t, srv, "/local/support/tunnel/"+strings.Repeat("ab", 32), supOrigin); st != http.StatusGone {
		t.Fatalf("unknown token: %d", st)
	}
	if sess.closed() {
		t.Fatal("refused attempts must not touch the session")
	}
	st, c, rd := wsDial(t, srv, "/local/support/tunnel/"+tok, supOrigin)
	if st != http.StatusSwitchingProtocols {
		t.Fatalf("open: %d", st)
	}
	if st2, _, _ := wsDial(t, srv, "/local/support/tunnel/"+tok, supOrigin); st2 != http.StatusGone {
		t.Fatalf("token used twice: %d", st2)
	}
	_ = c
	_ = rd

	// Remote peers are refused even with the right Origin and token.
	tok2 := registerTunnel(h, newFakeSupSess())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/local/support/tunnel/"+tok2, nil)
	req.RemoteAddr = "192.0.2.7:5000"
	req.Header.Set("Origin", supOrigin)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	h.serveHTTP(rec, req, supOrigin)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote peer: %d", rec.Code)
	}
}

func TestSupportTunnelExpiry(t *testing.T) {
	h, ft, srv := newTestSupportHub(nil)
	defer srv.Close()
	sess := newFakeSupSess()
	tok := registerTunnel(h, sess)
	ft.fire(0)
	sess.waitClosed(t)
	if st, _, _ := wsDial(t, srv, "/local/support/tunnel/"+tok, supOrigin); st != http.StatusGone {
		t.Fatalf("expired token: %d", st)
	}
}

// Frames both ways, ping, fragmentation, large frames, and the three ends.
func TestSupportBridgeFrames(t *testing.T) {
	h, _, srv := newTestSupportHub(nil)
	defer srv.Close()
	sess := newFakeSupSess()
	st, c, rd := wsDial(t, srv, "/local/support/tunnel/"+registerTunnel(h, sess), supOrigin)
	if st != http.StatusSwitchingProtocols {
		t.Fatal(st)
	}
	// Host -> page: the init (text), then video (binary, one large).
	sess.toPage <- supFrame{0x1, []byte(`{"type":"init","width":1920,"height":1080}`)}
	big := bytes.Repeat([]byte{0x01, 0xAB}, 300000)
	sess.toPage <- supFrame{0x2, []byte{0x01, 0, 0, 0, 1}}
	sess.toPage <- supFrame{0x2, big}
	mustFrame(t, c, rd, 0x1, `{"type":"init","width":1920,"height":1080}`)
	mustFrame(t, c, rd, 0x2, string([]byte{0x01, 0, 0, 0, 1}))
	f, err := readFrame(t, c, rd)
	if err != nil || f.op != 0x2 || !bytes.Equal(f.payload, big) {
		t.Fatalf("large frame: %v %d", err, len(f.payload))
	}
	// Page -> host: input JSON, a binary file chunk, a fragmented text.
	c.Write(masked(t, 0x1, `{"type":"mouse","x":10,"y":20}`))
	c.Write(masked(t, 0x2, "\x20chunk"))
	frag1, _ := buildMaskedFrame(0x1, []byte(`{"type":"key",`))
	frag1[0] &^= 0x80 // not the last fragment
	ping, _ := buildMaskedFrame(0x9, []byte("hb"))
	frag2, _ := buildMaskedFrame(0x0, []byte(`"code":"KeyA"}`))
	c.Write(frag1)
	c.Write(ping)
	c.Write(frag2)
	mustFrame(t, c, rd, 0xA, "hb")
	for _, want := range []supFrame{{0x1, []byte(`{"type":"mouse","x":10,"y":20}`)}, {0x2, []byte("\x20chunk")}, {0x1, []byte(`{"type":"key","code":"KeyA"}`)}} {
		select {
		case got := <-sess.fromPage:
			if got.op != want.op || !bytes.Equal(got.p, want.p) {
				t.Fatalf("host got %d %q, want %d %q", got.op, got.p, want.op, want.p)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("host never got %q", want.p)
		}
	}
	// The host ends the session: the page reads why.
	sess.end(false, io.EOF)
	code, reason := readCloseFrame(t, c, rd)
	if code != supportCloseHostEnded || reason != "host_ended" {
		t.Fatalf("close %d %q", code, reason)
	}
	waitLive(t, h, 0)
}

func waitLive(t *testing.T, h *supportHub, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, live := h.counts(); live == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("live tunnels never %d", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSupportBridgeEnds(t *testing.T) {
	h, _, srv := newTestSupportHub(nil)
	defer srv.Close()

	// The link is lost: 4000 "lost".
	sess := newFakeSupSess()
	_, c, rd := wsDial(t, srv, "/local/support/tunnel/"+registerTunnel(h, sess), supOrigin)
	sess.end(false, errors.New("supportlink: ICE failed"))
	if code, reason := readCloseFrame(t, c, rd); code != supportCloseLost || reason != "lost" {
		t.Fatalf("lost: %d %q", code, reason)
	}

	// The tab closes (close frame): the session is closed by us.
	sess2 := newFakeSupSess()
	_, c2, _ := wsDial(t, srv, "/local/support/tunnel/"+registerTunnel(h, sess2), supOrigin)
	c2.Write(masked(t, 0x8, "\x03\xe8"))
	sess2.waitClosed(t)
	if !sess2.local {
		t.Fatal("not closed by the operator side")
	}

	// The page's connection drops without a close frame: same.
	sess3 := newFakeSupSess()
	_, c3, _ := wsDial(t, srv, "/local/support/tunnel/"+registerTunnel(h, sess3), supOrigin)
	c3.Close()
	sess3.waitClosed(t)

	// An unmasked frame from the page is a protocol error: closed.
	sess4 := newFakeSupSess()
	_, c4, _ := wsDial(t, srv, "/local/support/tunnel/"+registerTunnel(h, sess4), supOrigin)
	c4.Write(buildServerFrame(0x1, []byte("{}")))
	sess4.waitClosed(t)
	waitLive(t, h, 0)
}
