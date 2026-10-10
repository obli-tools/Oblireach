package main

// hwinfo.go — the Info panel's "More details" (agent_tools.go, infoMore*):
// the machine's maker and model as a person says them ("Dell Latitude 5440",
// "ASUS Zenbook Duo UX8406MA", "Lenovo ThinkPad X1 Carbon Gen 11", "Machine
// virtuelle Hyper-V"), and the maker's support page for its serial number.
//
// One table, two readers: the rules below are the reference (Go, tested by
// hwinfo_test.go on real reports) and the page reads the SAME rules, injected
// as JSON (hwRulesJS, at the head of toolsJS): the label it shows is the one
// tested here (testdata/hwlabel_cases.json is run by both sides). Patterns
// stay in the syntax RE2 and JavaScript share (no look-around, no
// back-reference), always case-insensitive.
//
// The support link never takes a URL from the page: the page names a vendor
// key and a serial number, POST /local/open-support; this side checks both
// against the table (an alphanumeric serial of the vendor's shape, never a
// placeholder, never a virtual machine) and builds the URL itself, then the
// system's default browser opens it (never the WebView). No request is ever
// made to these sites by the app (no warranty scraping).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// hwVendor: a maker, matched on the manufacturer string.
type hwVendor struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Match string `json:"match"`
	// Support: the support page; "{serial}" is replaced by the serial when
	// the page takes it (SerialInURL), else it is the maker's search page
	// (the serial is copied for the person to paste: HP's page takes no
	// serial in its address any more).
	Support     string `json:"support,omitempty"`
	SerialInURL bool   `json:"serialInUrl,omitempty"`
	// Serial: the shape of the maker's serial numbers (upper-cased first).
	Serial string `json:"serial,omitempty"`
	// ModelFromFamily: the model the person knows sits in the system's
	// version / family ("ThinkPad X1 Carbon Gen 11"), its model field holds
	// the machine type ("21HM004GFR").
	ModelFromFamily bool `json:"modelFromFamily,omitempty"`
}

// hwVM: a hypervisor, matched on every string of the machine at once
// (manufacturer, model, family, BIOS vendor, board), joined by " | ".
type hwVM struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Match string `json:"match"`
}

var hwVendors = []hwVendor{
	{Key: "dell", Name: "Dell", Match: `^dell\b`,
		Support: "https://www.dell.com/support/home/product-support/servicetag/{serial}/overview", SerialInURL: true, Serial: `^[A-Z0-9]{5,7}$`},
	{Key: "lenovo", Name: "Lenovo", Match: `^lenovo\b`, ModelFromFamily: true,
		Support: "https://pcsupport.lenovo.com/products/{serial}", SerialInURL: true, Serial: `^[A-Z0-9]{7,12}$`},
	{Key: "hpe", Name: "HPE", Match: `^(hewlett[- ]?packard enterprise|hpe)\b`},
	{Key: "hp", Name: "HP", Match: `^(hp|hewlett[- ]?packard)\b`,
		Support: "https://support.hp.com/checkwarranty", Serial: `^[A-Z0-9]{8,14}$`},
	{Key: "asus", Name: "ASUS", Match: `^asus`},
	{Key: "microsoft", Name: "Microsoft", Match: `^microsoft\b`},
	{Key: "apple", Name: "Apple", Match: `^apple\b`},
	{Key: "acer", Name: "Acer", Match: `^acer\b`},
	{Key: "msi", Name: "MSI", Match: `^(micro-star|msi\b)`},
	{Key: "gigabyte", Name: "Gigabyte", Match: `^gigabyte\b`},
	{Key: "fujitsu", Name: "Fujitsu", Match: `^fujitsu\b`},
	{Key: "dynabook", Name: "Dynabook", Match: `^dynabook\b`},
	{Key: "toshiba", Name: "Toshiba", Match: `^toshiba\b`},
	{Key: "samsung", Name: "Samsung", Match: `^samsung\b`},
	{Key: "intel", Name: "Intel", Match: `^intel\b`},
	{Key: "supermicro", Name: "Supermicro", Match: `^super ?micro\b`},
	{Key: "panasonic", Name: "Panasonic", Match: `^panasonic\b`},
	{Key: "getac", Name: "Getac", Match: `^getac\b`},
	{Key: "razer", Name: "Razer", Match: `^razer\b`},
	{Key: "framework", Name: "Framework", Match: `^framework\b`},
	{Key: "system76", Name: "System76", Match: `^system76\b`},
	{Key: "huawei", Name: "Huawei", Match: `^huawei\b`},
	{Key: "xiaomi", Name: "Xiaomi", Match: `^(xiaomi|timi)\b`},
	{Key: "medion", Name: "Medion", Match: `^medion\b`},
	{Key: "asrock", Name: "ASRock", Match: `^asrock\b`},
	{Key: "sony", Name: "Sony", Match: `^sony\b`},
	{Key: "vaio", Name: "VAIO", Match: `^vaio\b`},
	{Key: "lg", Name: "LG", Match: `^lg electronics\b`},
	{Key: "google", Name: "Google", Match: `^google\b`},
	{Key: "raspberrypi", Name: "Raspberry Pi", Match: `^raspberry pi\b`},
}

var hwVMs = []hwVM{
	{Key: "hyperv", Name: "Hyper-V", Match: `hyper-v|microsoft corporation \| virtual machine`},
	{Key: "vmware", Name: "VMware", Match: `vmware`},
	{Key: "virtualbox", Name: "VirtualBox", Match: `virtualbox|innotek`},
	{Key: "parallels", Name: "Parallels", Match: `parallels`},
	{Key: "xen", Name: "Xen", Match: `\bxen\b|hvm domu`},
	{Key: "ec2", Name: "Amazon EC2", Match: `amazon ec2`},
	{Key: "gce", Name: "Google Compute Engine", Match: `google compute engine`},
	{Key: "kvm", Name: "KVM / QEMU", Match: `\bqemu\b|\bkvm\b|seabios|\bovmf\b|\bbochs\b|^standard pc \(|\| standard pc \(`},
}

// hwJunk: the placeholders firmware leaves in its fields ("To Be Filled By
// O.E.M.", "Default string", "System Serial Number"...): a row is hidden.
const hwJunk = `^(to be filled by o\.?e\.?m\.?|default string|default|system (product name|manufacturer|version|serial number|sku|name)|not (applicable|specified|available|defined)|none|n/?a|null|invalid|oem|o\.e\.m\.?|unknown|base ?board (serial number|product name)|chassis (serial number|manufacture)|type1productconfigid|type2 - board serial number|sku|serial ?number|serial|123456789|0123456789|1234567890|x\.x|to be filled|filled by o\.?e\.?m\.?|empty|undefined|chassis manufacture|product name|\*+)$`

// hwSerialChars: what a serial number may hold to be shown at all.
const hwSerialChars = `^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,63}$`

type hwRules struct {
	Vendors []hwVendor `json:"vendors"`
	VMs     []hwVM     `json:"vms"`
	Junk    string     `json:"junk"`
	Serial  string     `json:"serial"`
}

var hwCompiled struct {
	once    sync.Once
	vendors []*regexp.Regexp
	serials []*regexp.Regexp
	vms     []*regexp.Regexp
	junk    *regexp.Regexp
	serial  *regexp.Regexp
}

func hwRe() {
	hwCompiled.once.Do(func() {
		for _, v := range hwVendors {
			hwCompiled.vendors = append(hwCompiled.vendors, regexp.MustCompile(`(?i)`+v.Match))
			var s *regexp.Regexp
			if v.Serial != "" {
				s = regexp.MustCompile(v.Serial)
			}
			hwCompiled.serials = append(hwCompiled.serials, s)
		}
		for _, v := range hwVMs {
			hwCompiled.vms = append(hwCompiled.vms, regexp.MustCompile(`(?i)`+v.Match))
		}
		hwCompiled.junk = regexp.MustCompile(`(?i)` + hwJunk)
		hwCompiled.serial = regexp.MustCompile(hwSerialChars)
	})
}

// hwRulesJS: the table, for the page (var INFO_HW_RULES).
func hwRulesJS() string {
	b, err := json.Marshal(hwRules{Vendors: hwVendors, VMs: hwVMs, Junk: hwJunk, Serial: hwSerialChars})
	if err != nil {
		panic(err)
	}
	// (inside a <script>: no "</" can close it)
	return "var INFO_HW_RULES = " + strings.ReplaceAll(string(b), "</", `<\/`) + ";\n"
}

var hwSpaceRe = regexp.MustCompile(`\s+`)

// hwClean: a firmware string trimmed, its spaces collapsed, "" for a
// placeholder or a run of one character ("0000000", "........").
func hwClean(s string) string {
	hwRe()
	s = strings.TrimSpace(hwSpaceRe.ReplaceAllString(s, " "))
	if s == "" || hwCompiled.junk.MatchString(s) {
		return ""
	}
	if r := []rune(s); len(r) > 1 {
		same := true
		for _, c := range r[1:] {
			if c != r[0] {
				same = false
				break
			}
		}
		if same {
			return ""
		}
	} else if s == "0" || s == "." || s == "-" {
		return ""
	}
	return s
}

// hwVendorOf: the maker of a manufacturer string (index, -1 when unknown).
func hwVendorOf(man string) int {
	hwRe()
	man = hwClean(man)
	if man == "" {
		return -1
	}
	for i, re := range hwCompiled.vendors {
		if re.MatchString(man) {
			return i
		}
	}
	return -1
}

// hwVMOf: the hypervisor key of a machine ("" for a physical one).
func hwVMOf(parts ...string) string {
	hwRe()
	var keep []string
	for _, p := range parts {
		if c := hwClean(p); c != "" {
			keep = append(keep, c)
		}
	}
	all := strings.Join(keep, " | ")
	for i, re := range hwCompiled.vms {
		if re.MatchString(all) {
			return hwVMs[i].Key
		}
	}
	return ""
}

var hwSuffixRe = regexp.MustCompile(`(?i)[ ,]+(inc\.?|incorporated|corp\.?|corporation|co\.?,? ?ltd\.?|co\.?|ltd\.?|limited|llc|gmbh|s\.?a\.?|b\.?v\.?|ag|technology|technologies|computer|computers|international|electronics)$`)

// hwTidyMaker: an unknown maker, its legal suffixes dropped ("Quanta
// Computer Inc." -> "Quanta"), an all-capitals name in title case.
func hwTidyMaker(man string) string {
	s := hwClean(man)
	for i := 0; i < 4; i++ {
		t := strings.TrimSpace(hwSuffixRe.ReplaceAllString(s, ""))
		if t == s || t == "" {
			break
		}
		s = t
	}
	if s != "" && s == strings.ToUpper(s) && len(s) > 3 {
		w := strings.Fields(strings.ToLower(s))
		for i, x := range w {
			w[i] = strings.ToUpper(x[:1]) + x[1:]
		}
		s = strings.Join(w, " ")
	}
	return s
}

// hwDedup: ASUS writes its model twice ("UX8406MA_UX8406MA").
func hwDedup(m string) string {
	if i := strings.Index(m, "_"); i > 0 && i == len(m)-i-1 && m[:i] == m[i+1:] {
		return m[:i]
	}
	if i := strings.LastIndex(m, "_"); i > 0 {
		head, tail := m[:i], m[i+1:]
		if tail != "" && strings.HasSuffix(head, tail) {
			return head
		}
	}
	return m
}

var hwMachineTypeRe = regexp.MustCompile(`^[0-9A-Z]{4,12}$`)

// hwMachine: the label of a machine and what it is. manufacturer, model,
// family (the system's version / family); extra: other strings that may name a
// hypervisor (BIOS vendor, board). label "" when the model is not known; vm
// the hypervisor key; vendor the maker key ("" unknown); maker its name.
type hwMachineInfo struct {
	Label  string `json:"label"`
	Maker  string `json:"maker"`
	Vendor string `json:"vendor"`
	VM     string `json:"vm"`
}

func hwMachine(manufacturer, model, family string, extra ...string) hwMachineInfo {
	out := hwMachineInfo{}
	if vm := hwVMOf(append([]string{manufacturer, model, family}, extra...)...); vm != "" {
		out.VM = vm
		return out
	}
	vi := hwVendorOf(manufacturer)
	if vi >= 0 {
		out.Vendor, out.Maker = hwVendors[vi].Key, hwVendors[vi].Name
	} else {
		out.Maker = hwTidyMaker(manufacturer)
	}
	m, fam := hwDedup(hwClean(model)), hwClean(family)
	if vi >= 0 && hwVendors[vi].ModelFromFamily && fam != "" && strings.Contains(fam, " ") && (m == "" || hwMachineTypeRe.MatchString(m)) {
		m = fam
	}
	if m == "" {
		return out
	}
	// (the maker's name already at the head of the model: not twice)
	low := strings.ToLower(m)
	for _, p := range []string{strings.ToLower(out.Maker), strings.ToLower(hwClean(manufacturer))} {
		if p != "" && strings.HasPrefix(low, p+" ") {
			m = strings.TrimSpace(m[len(p):])
			break
		}
	}
	if out.Vendor == "apple" && fam != "" && !strings.EqualFold(fam, m) {
		m += " (" + fam + ")"
	}
	if out.Maker == "" {
		out.Label = m
	} else {
		out.Label = out.Maker + " " + m
	}
	return out
}

// hwSerial: a serial number as shown ("" placeholder or odd characters).
func hwSerial(s string) string {
	hwRe()
	s = hwClean(s)
	if s == "" || !hwCompiled.serial.MatchString(s) {
		return ""
	}
	return s
}

// hwSupportURL: the support page of vendor for serial. inURL: the serial is
// in the address (else the page is the maker's search page). ok false: no
// page (unknown or unsupported maker, a placeholder or a serial of another
// shape).
func hwSupportURL(vendor, serial string) (u string, inURL bool, ok bool) {
	hwRe()
	s := strings.ToUpper(hwSerial(serial))
	if s == "" {
		return "", false, false
	}
	for i, v := range hwVendors {
		if v.Key != vendor || v.Support == "" {
			continue
		}
		if hwCompiled.serials[i] == nil || !hwCompiled.serials[i].MatchString(s) {
			return "", false, false
		}
		if v.SerialInURL {
			return strings.ReplaceAll(v.Support, "{serial}", s), true, true
		}
		return v.Support, false, true
	}
	return "", false, false
}

// hwBrowserOpen: the system's default browser (overridden by the tests).
var hwBrowserOpen = func(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

var errHwSupport = errors.New("no support page")

// handleOpenSupport: POST /local/open-support {vendor, serial} -> the maker's
// support page in the default browser. 400: no page for these (the page only
// offers the link when there is one).
func (p *Proxy) handleOpenSupport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprint(w, `{"error":"POST only"}`)
		return
	}
	var in struct {
		Vendor string `json:"vendor"`
		Serial string `json:"serial"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid body"}`)
		return
	}
	u, inURL, ok := hwSupportURL(in.Vendor, in.Serial)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":%q}`, errHwSupport.Error())
		return
	}
	if err := hwBrowserOpen(u); err != nil {
		log.Printf("open-support: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"launcher failed"}`)
		return
	}
	fmt.Fprintf(w, `{"ok":true,"serialInUrl":%t}`, inURL)
}
