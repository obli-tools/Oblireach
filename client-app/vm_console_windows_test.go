//go:build windows

package main

// vm_console_windows_test.go — the Hyper-V VM consoles in the session
// windows (detached_windows.go: kind "vm-<guid>", the VM's name) and the
// vault typing into a VM console (vault_bridge_windows.go vaultTypeVm).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestVmConsoleKeys(t *testing.T) {
	kind := "vm-" + testVmGUID
	if toolKey("p", "7", kind) != "p:7~"+kind {
		t.Fatal("toolKey of a VM console")
	}
	for k, want := range map[string]bool{
		"p:7~" + kind: true, "7~" + kind: true,
		"p:7~vm-" + strings.ToUpper(testVmGUID): false, "p:7~vm-1234": false, "p:7~" + kind + "~cmd": false,
		"~" + kind: false, "p:~" + kind: false,
	} {
		if validDevKey(k) != want {
			t.Errorf("validDevKey(%q) = %v", k, !want)
		}
	}
	if keyProfile("p:7~"+kind) != "p" || keyProfile("7~"+kind) != "" {
		t.Fatal("keyProfile of a VM console's key")
	}
	if got := cleanTabIDs([]string{"A:7", "A:7~" + kind, "A:7~vm-bad", "A:7~cmd"}); strings.Join(got, ",") != "A:7,A:7~"+kind+",A:7~cmd" {
		t.Fatalf("cleanTabIDs: %v", got)
	}
	// The options: a VM console's kind, its VM's name only with it.
	if _, err := (detachOpts{Kind: kind, VmName: "DC01"}).check(); err != nil {
		t.Fatalf("a VM console's options refused: %v", err)
	}
	for _, o := range []detachOpts{
		{Kind: "vm-" + strings.ToUpper(testVmGUID)},
		{Kind: "vm-nope"},
		{Kind: "cmd", VmName: "DC01"},
		{VmName: "DC01"},
	} {
		if _, err := o.check(); err == nil || err.Error() != tr("detached.err.invalidKind") {
			t.Fatalf("accepted %+v (%v)", o, err)
		}
	}
}

// A VM console's window: its own key next to its host's ObliReach window,
// its VM's name in its URL; asked again: brought forward; handed over
// (tear-off onto the main window) with its kind and name.
func TestOpenWindowVmConsole(t *testing.T) {
	b, host, m, focused, _ := testMainBridge(t)
	var created []detachReq
	m.createHook = func(r detachReq) (*detachedWindow, error) {
		created = append(created, r)
		return &detachedWindow{webHost: webHost{hwnd: uintptr(0x600 + len(created))}, mgr: m, deviceID: r.deviceID}, nil
	}
	run := func() {
		q := host.dispatched
		host.dispatched = nil
		for _, f := range q {
			f()
		}
	}
	kind := "vm-" + testVmGUID
	if r, _ := b.openWindow(b.token, "7", "PC-7", detachOpts{Profile: "p"}); r != "opening" {
		t.Fatal(r)
	}
	if r, _ := b.openWindow(b.token, "7", "VM · DC01 (PC-7)", detachOpts{Profile: "p", Kind: kind, VmName: " DC01\n"}); r != "opening" {
		t.Fatalf("the VM console's window while the host's opens: %q", r)
	}
	if _, err := b.openWindow(b.token, "7", "VM", detachOpts{Profile: "p", Kind: "vm-zz"}); err == nil {
		t.Fatal("a malformed VM kind was accepted")
	}
	run()
	if len(created) != 2 || created[1].key != "p:7~"+kind || created[1].kind != kind || created[1].vmName != "DC01" || created[1].deviceID != "7" {
		t.Fatalf("created: %+v", created)
	}
	u := vmEntryURL(m.proxy.detachedToolEntryURL(created[1].deviceID, created[1].profile, created[1].session, created[1].viewOnly, created[1].kind, created[1].park, created[1].path), created[1].kind, created[1].vmName)
	if !strings.Contains(u, "m="+kind) || !strings.Contains(u, "vn=DC01") {
		t.Fatalf("entry URL: %s", u)
	}
	wv := m.byDevice["p:7~"+kind]
	if wv == nil || wv == m.byDevice["p:7"] {
		t.Fatal("the VM console has no window of its own")
	}
	if r, _ := b.openWindow(b.token, "7", "VM · DC01 (PC-7)", detachOpts{Profile: "p", Kind: kind}); r != "focused" || (*focused)[len(*focused)-1] != wv.hwnd {
		t.Fatalf("asked again: %q", r)
	}
	// Torn off a session window onto the main window: adopted with its kind and name.
	_, d1, _ := testSessionWindow(t, m, 0x100, "12")
	d1.windowTabs(d1.token, windowTabsInfo{IDs: []string{"A:12", "A:12~" + kind}})
	m.cursorHook = func() winPoint { return winPoint{10, 10} }
	m.dropHook = func(winPoint) (*detachedWindow, bool) { return nil, true }
	if r, err := d1.tearOff(d1.token, "12", "VM · DC01 (PC-12)", detachOpts{Profile: "A", Kind: kind, VmName: "DC01"}); err != nil || r != "moved" {
		t.Fatalf("tear-off of a VM console: %q %v", r, err)
	}
	if !evalHas(host, "__reachAdoptTab", `"kind":"`+kind+`"`) || !evalHas(host, "__reachAdoptTab", `"vmName":"DC01"`) || !m.mainTabs["A:12~"+kind] {
		t.Fatal("the VM console was not handed over with its kind and name")
	}
	var ai adoptInfo
	if err := json.Unmarshal([]byte(detachReq{deviceID: "12", kind: kind, vmName: "DC01", session: -1}.adoptJSON()), &ai); err != nil || ai.Kind != kind || ai.VmName != "DC01" {
		t.Fatalf("adoptJSON: %+v %v", ai, err)
	}
	if strings.Contains(detachReq{deviceID: "12", kind: "cmd"}.adoptJSON(), "vmName") {
		t.Fatal("a tool tab carries a VM name")
	}
}

// The vault types into a VM console its own way (vaultTypeVm: the VM
// helper's frame, the guest's layout), never the ObliReach frame; and never
// into an ObliReach session that way.
func TestVaultTypeVm(t *testing.T) {
	h := &syncHost{}
	b, _ := newNativeBridge(h, "http://127.0.0.1:4321")
	b.windows = &sessionWindows{}
	b.tunnels = newReachTunnels()
	newTestVaultForBridge(t)
	if ok, err := b.vaultCreateWithCode(b.token, "aaaaaaaa", "secret1"); !ok || err != nil {
		t.Fatalf("create: %v %v", ok, err)
	}
	h.waitEvals(t, 1)
	in := vaultSaveInput{Origin: "https://a.example", TenantID: 5, Label: "VM admin", Username: "adm", Secret: "hunter2"}
	if ok, err := b.vaultSave(b.token, "bbbbbbbb", in); !ok || err != nil {
		t.Fatalf("save: %v %v", ok, err)
	}
	evals := h.waitEvals(t, 2)
	var saved struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := parsePushed(evals[1], &saved); err != nil || !saved.OK {
		t.Fatalf("save push: %v", evals[1])
	}
	var idOut struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(saved.Data, &idOut)

	// A VM console of host 99 (tenant 5).
	b.tunnels.registerSession("vm-tok", "https://a.example", 5, 99, "", true)
	dst := &lockedBuffer{}
	b.tunnels.attach("vm-tok", newWSInjector(dst))
	if ok, err := b.vaultTypeVm(b.token, "cccccccc", "vm-tok", idOut.ID, true, true, "0000040c", "linux"); !ok || err != nil {
		t.Fatalf("vaultTypeVm: %v %v", ok, err)
	}
	evals = h.waitEvals(t, 3)
	var res struct{ OK bool }
	if err := parsePushed(evals[2], &res); err != nil || !res.OK {
		t.Fatalf("type push: %v", evals[2])
	}
	_, _, payload := unmaskParse(t, bufio.NewReader(bytes.NewReader(dst.Bytes())))
	var f map[string]string
	if err := json.Unmarshal(payload, &f); err != nil || f["type"] != "type_text" || f["text"] != "adm\thunter2\n" || f["layout"] != "0000040C" || f["guest"] != "linux" {
		t.Fatalf("VM frame: %q (%v)", payload, err)
	}
	for i, e := range evals {
		if strings.Contains(e, "hunter2") {
			t.Fatalf("eval %d leaked the secret", i)
		}
	}
	// The ObliReach binding refuses a VM console, the VM one an ObliReach session.
	gen := 1
	b.vaultType(b.token, "dddddddd", "vm-tok", idOut.ID, false, false, &gen, false)
	b.tunnels.register("r-tok", "https://a.example", 5, 99)
	b.tunnels.attach("r-tok", newWSInjector(&lockedBuffer{}))
	b.vaultTypeVm(b.token, "eeeeeeee", "r-tok", idOut.ID, false, false, "", "")
	evals = h.waitEvals(t, 5)
	for _, e := range evals[3:] {
		var r struct{ OK bool }
		if err := parsePushed(e, &r); err != nil || r.OK {
			t.Fatalf("a mismatched binding typed: %v", e)
		}
	}
	// A wrong token never runs.
	if ok, err := b.vaultTypeVm("wrong", "ffffffff", "vm-tok", idOut.ID, false, false, "", ""); ok || err != errNotAllowed {
		t.Fatalf("wrong token: %v %v", ok, err)
	}
}
