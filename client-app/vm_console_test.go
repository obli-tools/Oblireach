package main

// vm_console_test.go — the portable Go side of the Hyper-V VM consoles
// (vm_console_go.go): the "vm-<guid>" kind, the VM's name carried by a
// session window, the session registry of a 'vmconsole' answer, and the
// vault's type_text frame for a VM console.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testVmGUID = "0f8fad5b-d9cb-469f-a165-70867728950e"

func TestVmKind(t *testing.T) {
	for k, want := range map[string]bool{
		"vm-" + testVmGUID:                  true,
		"vm-" + strings.ToUpper(testVmGUID): false, // (the page lowers it)
		"vm-":                               false,
		"vm-" + testVmGUID + "x":            false,
		"vm-" + testVmGUID[:35]:             false,
		"vm" + testVmGUID:                   false,
		"VM-" + testVmGUID:                  false,
		"cmd":                               false,
		"":                                  false,
		"vm-../" + testVmGUID:               false,
	} {
		if vmKind(k) != want {
			t.Errorf("vmKind(%q) = %v", k, !want)
		}
	}
	for k, want := range map[string]bool{"cmd": true, "files": true, "rdp": true, "vm-" + testVmGUID: true, "vm-x": false, "": false, "reach": false} {
		if validWindowKind(k) != want {
			t.Errorf("validWindowKind(%q) = %v", k, !want)
		}
	}
}

func TestVmWindowName(t *testing.T) {
	for in, want := range map[string]string{
		"  SRV-DC01  ":            "SRV-DC01",
		"a\x00b\x07c\nd\u2028e":   "abcde",
		"":                        "",
		"\t\n":                    "",
		"Serveur de fichiers (2)": "Serveur de fichiers (2)",
		strings.Repeat("é", 150):  strings.Repeat("é", 100),
	} {
		if got := vmWindowName(in); got != want {
			t.Errorf("vmWindowName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The entry of a VM console's window: its kind and its VM's name reach the
// page, validated; nothing else.
func TestEntryTargetVmConsole(t *testing.T) {
	p := newProxy(&Config{}, t.TempDir(), 4321)
	enter := func(raw string) string {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, raw, nil))
		return rec.Header().Get("Location")
	}
	kind := "vm-" + testVmGUID
	u := vmEntryURL(p.detachedToolEntryURL("12", "a1b2", -1, false, kind, "", ""), kind, "Serveur & co")
	pu, err := url.Parse(u)
	if err != nil || pu.Query().Get("m") != kind || pu.Query().Get("vn") != "Serveur & co" {
		t.Fatalf("entry URL: %s (%v)", u, err)
	}
	if loc := enter(pu.RequestURI()); loc != "/?detached=12&p=a1b2&m="+kind+"&vn=Serveur+%26+co" {
		t.Fatalf("landed on %q", loc)
	}
	// A name with control characters: cleaned; a name without a VM console: dropped.
	base := entryPath + "?k=" + url.QueryEscape(p.secret) + "&d=12"
	if loc := enter(base + "&m=" + kind + "&vn=" + url.QueryEscape("a\x07b\nc")); loc != "/?detached=12&m="+kind+"&vn=abc" {
		t.Fatalf("control characters: %q", loc)
	}
	if loc := enter(base + "&m=cmd&vn=DC01"); loc != "/?detached=12&m=cmd" {
		t.Fatalf("a VM name on a tool window: %q", loc)
	}
	if loc := enter(base + "&m=vm-nope&vn=DC01"); loc != "/?detached=12" {
		t.Fatalf("a malformed VM kind: %q", loc)
	}
	if loc := enter(base + "&m=" + kind + "&vn=%20%20"); loc != "/?detached=12&m="+kind {
		t.Fatalf("an empty VM name: %q", loc)
	}
	// Not a VM console: the URL is unchanged.
	if vmEntryURL("x", "cmd", "DC01") != "x" || vmEntryURL("x", kind, "\n") != "x" {
		t.Fatal("vmEntryURL added a name where none goes")
	}
}

// POST /api/remote/sessions answered for a VM console: registered for the
// vault, marked (its own frame), on its host's tenant and id.
func TestCaptureSessionResponseRegistersVmConsole(t *testing.T) {
	p := &Proxy{tunnels: newReachTunnels()}
	body := `{"data":{"id":"s1","protocol":"vmconsole","tenantId":1,"deviceId":42,"deviceTenantId":5,"sessionToken":"vm-tok_1","vmId":"` + testVmGUID + `"}}`
	resp := &http.Response{StatusCode: 201, Body: io.NopCloser(bytes.NewReader([]byte(body))), Header: http.Header{}}
	p.captureSessionResponse(resp, "https://server.example")
	tun, _, ok := p.tunnels.lookup("vm-tok_1")
	if !ok || !tun.vm || tun.tenant != 5 || tun.deviceID != 42 || tun.origin != "https://server.example" || tun.vmID != testVmGUID {
		t.Fatalf("VM console not registered as such: %+v ok=%v", tun, ok)
	}
	if s := tun.vaultSession(); !s.VM || s.VMID != testVmGUID || s.DeviceID != 42 {
		t.Fatalf("vault session of a VM console: %+v", s)
	}
	// The VM's GUID lowered; a malformed one dropped (still a VM console:
	// only the unrestricted entries are then offered).
	for in, want := range map[string]string{strings.ToUpper(testVmGUID): testVmGUID, "{" + testVmGUID + "}": "", "../x": "", "": ""} {
		vb, _ := json.Marshal(map[string]interface{}{"data": map[string]interface{}{"protocol": "vmconsole", "deviceId": 42, "deviceTenantId": 5, "sessionToken": "vm-tok_2", "vmId": in}})
		p.captureSessionResponse(&http.Response{StatusCode: 201, Body: io.NopCloser(bytes.NewReader(vb)), Header: http.Header{}}, "https://server.example")
		if tun, _, ok := p.tunnels.lookup("vm-tok_2"); !ok || !tun.vm || tun.vmID != want {
			t.Fatalf("vmId %q: %+v ok=%v, want %q", in, tun, ok, want)
		}
	}
	if got, _ := io.ReadAll(resp.Body); string(got) != body {
		t.Fatalf("body mutated: %q", got)
	}
	// An ObliReach session stays unmarked.
	p.captureSessionResponse(&http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"data":{"protocol":"oblireach","deviceId":1,"deviceTenantId":5,"sessionToken":"r-tok"}}`)), Header: http.Header{}}, "https://a")
	if tun, _, ok := p.tunnels.lookup("r-tok"); !ok || tun.vm || tun.vmID != "" || tun.vaultSession().VM {
		t.Fatalf("ObliReach session: %+v ok=%v", tun, ok)
	}
}

func decodeVmFrame(t *testing.T, b []byte) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %q (%v)", b, err)
	}
	return m
}

func TestBuildVmTypeTextFrame(t *testing.T) {
	// The password alone, Enter, a layout (upper-cased).
	b, err := buildVmTypeTextFrame("adm", []byte(`p"a\ss`), false, true, "0000040c", "")
	if err != nil {
		t.Fatal(err)
	}
	m := decodeVmFrame(t, b)
	if m["type"] != "type_text" || m["text"] != "p\"a\\ss\n" || m["layout"] != "0000040C" || len(m) != 3 {
		t.Fatalf("frame: %q", b)
	}
	// Username, Tab, password; no Enter; a malformed layout dropped.
	b, _ = buildVmTypeTextFrame("DOM\\adm", []byte("Pässwörd€"), true, false, "fr-FR", "")
	m = decodeVmFrame(t, b)
	if m["text"] != "DOM\\adm\tPässwörd€" || len(m) != 2 {
		t.Fatalf("frame with user: %q", b)
	}
	// The guest's system: windows / linux (any case) named, anything else left out.
	for in, want := range map[string]string{"linux": "linux", "Windows": "windows", " LINUX ": "linux", "": "", "macos": "", `li"nux`: ""} {
		b, _ = buildVmTypeTextFrame("", []byte("x"), false, false, "00000409", in)
		m = decodeVmFrame(t, b)
		if g, has := m["guest"]; g != want || has != (want != "") {
			t.Fatalf("guest %q: frame %q", in, b)
		}
	}
	// A blank username: the password alone, nothing before it.
	b, _ = buildVmTypeTextFrame("  ", []byte("x"), true, false, "", "")
	if decodeVmFrame(t, b)["text"] != "x" {
		t.Fatalf("blank user: %q", b)
	}
	// Control characters escaped, invalid UTF-8 replaced: always valid JSON.
	b, _ = buildVmTypeTextFrame("", []byte{'a', 0x01, 0x7f, 0xff, 0xc3, 'b', '\r'}, false, false, "", "")
	if got := decodeVmFrame(t, b)["text"]; got != "a\x01\x7f��b\r" {
		t.Fatalf("escaping: %q -> %q", b, got)
	}
	if bytes.ContainsAny(b, "\x01\x7f\r") {
		t.Fatalf("raw control characters in the frame: %q", b)
	}
	// Empty, too long.
	if _, err := buildVmTypeTextFrame("u", nil, false, false, "", ""); err != errEmptySecret {
		t.Fatalf("empty secret: %v", err)
	}
	if _, err := buildVmTypeTextFrame("", bytes.Repeat([]byte("é"), vmTypeTextMax+1), false, false, "", ""); err != errVmTypeTooLong {
		t.Fatalf("too long: %v", err)
	}
	if _, err := buildVmTypeTextFrame("", bytes.Repeat([]byte("é"), vmTypeTextMax), false, false, "", ""); err != nil {
		t.Fatalf("at the bound: %v", err)
	}
}

// The frame goes through the injector of the session like the ObliReach one.
func TestVmTypeTextFrameInjected(t *testing.T) {
	dst := &lockedBuffer{}
	inj := newWSInjector(dst)
	b, _ := buildVmTypeTextFrame("", []byte("s3cret"), false, true, "00000409", "linux")
	if err := inj.Inject(b); err != nil {
		t.Fatal(err)
	}
	_, op, payload := unmaskParse(t, bufio.NewReader(bytes.NewReader(dst.Bytes())))
	if op != 1 || decodeVmFrame(t, payload)["text"] != "s3cret\n" {
		t.Fatalf("injected: op %d %q", op, payload)
	}
}
