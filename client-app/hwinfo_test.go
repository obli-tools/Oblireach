package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

type hwLabelCase struct {
	Name   string        `json:"name"`
	Man    string        `json:"man"`
	Model  string        `json:"model"`
	Family string        `json:"family"`
	Extra  []string      `json:"extra"`
	Want   hwMachineInfo `json:"want"`
}

// The makers and models of real reports (testdata/hwlabel_cases.json: the
// page's mirror runs the same file in the JS harness).
func TestHwMachineLabels(t *testing.T) {
	b, err := os.ReadFile("testdata/hwlabel_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []hwLabelCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 20 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		got := hwMachine(c.Man, c.Model, c.Family, c.Extra...)
		if got != c.Want {
			t.Errorf("%s: got %+v, want %+v", c.Name, got, c.Want)
		}
	}
}

func TestHwClean(t *testing.T) {
	for _, s := range []string{"", " ", "To Be Filled By O.E.M.", "to be filled by o.e.m.", "Default string", "System Serial Number",
		"System Product Name", "System manufacturer", "Not Specified", "Not Applicable", "None", "N/A", "INVALID", "0", "00000000",
		"XXXXXXXX", "........", "Chassis Serial Number", "Base Board Serial Number", "123456789", "OEM", "O.E.M.", "Unknown", "SerialNumber"} {
		if got := hwClean(s); got != "" {
			t.Errorf("hwClean(%q) = %q, want empty", s, got)
		}
	}
	for in, want := range map[string]string{"  Dell   Inc. ": "Dell Inc.", "ABC1234": "ABC1234", "A": "A", "Latitude\t5440": "Latitude 5440"} {
		if got := hwClean(in); got != want {
			t.Errorf("hwClean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHwSerial(t *testing.T) {
	ok := map[string]string{
		"ABC1234": "ABC1234", " PF2ABCDE ": "PF2ABCDE", "5CG1234XYZ": "5CG1234XYZ", "C02XK1JHJG5J": "C02XK1JHJG5J",
		// (a Hyper-V VM's BIOS serial: its identifier, shown as it is)
		"4012-1234-5678-9012-3456-7890-12": "4012-1234-5678-9012-3456-7890-12",
		"VMware-56 4d 2c 1a": "VMware-56 4d 2c 1a",
	}
	for in, want := range ok {
		if got := hwSerial(in); got != want {
			t.Errorf("hwSerial(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"To Be Filled By O.E.M.", "Default string", "System Serial Number", "0", "", "00000000", "<script>", "abc\"def", "-leading", strings.Repeat("A", 80)} {
		if got := hwSerial(in); got != "" {
			t.Errorf("hwSerial(%q) = %q, want empty", in, got)
		}
	}
}

func TestHwSupportURL(t *testing.T) {
	type want struct {
		u     string
		inURL bool
		ok    bool
	}
	cases := []struct {
		vendor, serial string
		w              want
	}{
		{"dell", "ABC1234", want{"https://www.dell.com/support/home/product-support/servicetag/ABC1234/overview", true, true}},
		{"dell", "abc1234", want{"https://www.dell.com/support/home/product-support/servicetag/ABC1234/overview", true, true}},
		{"lenovo", "PF2ABCDE", want{"https://pcsupport.lenovo.com/products/PF2ABCDE", true, true}},
		{"hp", "5CG1234XYZ", want{"https://support.hp.com/checkwarranty", false, true}},
		// refused: odd characters, placeholders, another shape, makers without a page, VMs
		{"dell", "ABC1234!", want{}}, {"dell", "ABC/123", want{}}, {"dell", "ABC 123", want{}}, {"dell", "ABC1234../x", want{}},
		{"dell", "To Be Filled By O.E.M.", want{}}, {"dell", "0", want{}}, {"dell", "", want{}}, {"dell", "ABCDEFGHIJ", want{}},
		{"lenovo", "System Serial Number", want{}}, {"lenovo", "PF2-ABCDE", want{}}, {"hp", "5CG1", want{}},
		{"asus", "N1NRKD00123456A", want{}}, {"microsoft", "012345678912", want{}}, {"apple", "C02XK1JHJG5J", want{}},
		{"hyperv", "4012-1234-5678", want{}}, {"", "ABC1234", want{}}, {"https://evil.example/", "ABC1234", want{}},
	}
	for _, c := range cases {
		u, in, ok := hwSupportURL(c.vendor, c.serial)
		if (want{u, in, ok}) != c.w {
			t.Errorf("hwSupportURL(%q, %q) = %q %v %v, want %+v", c.vendor, c.serial, u, in, ok, c.w)
		}
	}
	// A VM never gets a vendor key: no link (the page asks with m.vendor).
	if m := hwMachine("Microsoft Corporation", "Virtual Machine", ""); m.Vendor != "" || m.VM != "hyperv" {
		t.Errorf("Hyper-V: %+v", m)
	}
	if m := hwMachine("Dell Inc.", "VMware Virtual Platform", ""); m.Vendor != "" || m.VM != "vmware" {
		t.Errorf("a VM, even with a maker's name: %+v", m)
	}
}

// Every pattern compiles the same in RE2 and in JavaScript (no look-around,
// no back-reference, no named group), and the page gets the table.
func TestHwRulesJS(t *testing.T) {
	bad := regexp.MustCompile(`\(\?[=!<]|\\[1-9]|\(\?P?<`)
	all := []string{hwJunk, hwSerialChars}
	for _, v := range hwVendors {
		all = append(all, v.Match, v.Serial)
	}
	for _, v := range hwVMs {
		all = append(all, v.Match)
	}
	for _, p := range all {
		if bad.MatchString(p) {
			t.Errorf("pattern %q uses syntax JavaScript and RE2 do not share", p)
		}
	}
	js := hwRulesJS()
	if !strings.HasPrefix(js, "var INFO_HW_RULES = {") || strings.Contains(js, "</") || strings.Contains(js, "`") {
		t.Fatalf("hwRulesJS: %.80s", js)
	}
	if !strings.HasPrefix(toolsJS, js) {
		t.Fatal("toolsJS does not start with the rules")
	}
	var r hwRules
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(js, "var INFO_HW_RULES = "), ";\n")), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Vendors) != len(hwVendors) || len(r.VMs) != len(hwVMs) {
		t.Fatal("table incomplete")
	}
}

// POST /local/open-support: the app builds the address; GET, odd serials,
// unknown makers refused without opening anything.
func TestOpenSupportHandler(t *testing.T) {
	var opened []string
	prev := hwBrowserOpen
	hwBrowserOpen = func(u string) error { opened = append(opened, u); return nil }
	defer func() { hwBrowserOpen = prev }()
	p := &Proxy{}
	do := func(method, body string) int {
		rec := httptest.NewRecorder()
		p.handleOpenSupport(rec, httptest.NewRequest(method, "/local/open-support", strings.NewReader(body)))
		return rec.Code
	}
	if c := do(http.MethodPost, `{"vendor":"dell","serial":"ABC1234"}`); c != 200 {
		t.Fatalf("dell: %d", c)
	}
	if len(opened) != 1 || opened[0] != "https://www.dell.com/support/home/product-support/servicetag/ABC1234/overview" {
		t.Fatalf("opened %v", opened)
	}
	for _, c := range []struct{ m, b string }{
		{http.MethodGet, ``}, {http.MethodPost, `{"vendor":"dell","serial":"ABC1234\"&x"}`}, {http.MethodPost, `{"vendor":"asus","serial":"N1NRKD00123456A"}`},
		{http.MethodPost, `{"vendor":"hyperv","serial":"4012-1234"}`}, {http.MethodPost, `{"url":"https://evil.example"}`}, {http.MethodPost, `not json`},
	} {
		if code := do(c.m, c.b); code == 200 {
			t.Errorf("%s %s: accepted", c.m, c.b)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("refused requests opened %v", opened)
	}
	// The route is registered behind the proxy's guard (cookie + origin).
	px := newProxy(&Config{}, t.TempDir(), 0)
	rec := httptest.NewRecorder()
	px.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/local/open-support", strings.NewReader(`{"vendor":"dell","serial":"ABC1234"}`)))
	if rec.Code != http.StatusForbidden || len(opened) != 1 {
		t.Fatalf("without the app's cookie: %d, opened %v", rec.Code, opened)
	}
}
