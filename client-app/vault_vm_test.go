package main

// vault_vm_test.go — v5: a password entry bound to Hyper-V VMs (vault.go
// VaultDevice.VMID / sessionMatch, vault_codec.go v5 tail): the matching
// rules of a VM console against plain and VM-bound items, the format
// (written as v5 only while a VM binding exists, older versions read
// unchanged) and the validation of a VM binding.

import (
	"errors"
	"strings"
	"testing"
)

const (
	vmA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	vmB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

func TestVaultCodecV5RoundTripVMs(t *testing.T) {
	pv := samplePlain()
	pv.entries[0].devices = []VaultDevice{
		{Origin: "https://a.example", TenantID: 5, UUID: "host-uuid", ID: 61, Name: "HV-01", VMID: vmA, VMName: "DC01"},
		{Origin: "https://a.example", TenantID: 5, UUID: "host-uuid", ID: 61, Name: "HV-01"}, // the host itself
		{Origin: "https://b.example", TenantID: 7, ID: 9, Name: "HV-B", VMID: vmB, VMName: "Débian é"},
	}
	enc := encodeVaultPlain(pv)
	if enc[0] != 5 {
		t.Fatalf("written as v%d, want v5", enc[0])
	}
	dec, ok := decodeVaultPlain(enc)
	if !ok {
		t.Fatal("v5 decode failed")
	}
	got := dec.entries[0].devices
	if len(got) != 3 {
		t.Fatalf("devices: %+v", got)
	}
	for i := range got {
		if got[i] != pv.entries[0].devices[i] {
			t.Fatalf("device %d round trip: %+v, want %+v", i, got[i], pv.entries[0].devices[i])
		}
	}
	if len(dec.entries[1].devices) != 0 {
		t.Fatalf("an entry without devices must stay unrestricted: %+v", dec.entries[1].devices)
	}
	// Truncated anywhere in the VM tail: refused, never half-read.
	for cut := len(enc) - 1; cut > len(enc)-40; cut-- {
		if _, ok := decodeVaultPlain(enc[:cut]); ok {
			t.Fatalf("truncation at %d decoded", cut)
		}
	}
	// Trailing bytes after the v5 tail: refused.
	if _, ok := decodeVaultPlain(append(append([]byte(nil), enc...), 0, 0)); ok {
		t.Fatal("trailing bytes after the v5 tail decoded")
	}
}

// Without any VM binding the encoder writes exactly what it wrote before v5
// (v2 / v3 / v4, byte for byte), and those versions read with no VM.
func TestVaultCodecV5OnlyWhenUsed(t *testing.T) {
	// v3: a plain device restriction.
	pv := samplePlain()
	pv.entries[0].devices = []VaultDevice{{Origin: "https://a.example", TenantID: 5, UUID: "u", ID: 61, Name: "SRV"}}
	enc := encodeVaultPlain(pv)
	if enc[0] != 3 {
		t.Fatalf("plain restriction written as v%d, want v3", enc[0])
	}
	dec, ok := decodeVaultPlain(enc)
	if !ok || dec.entries[0].devices[0].VMID != "" || dec.entries[0].devices[0].VMName != "" {
		t.Fatalf("v3: %v %+v", ok, dec)
	}
	// v4: a credential reference with a plain restriction: still v4.
	pv.entries[1].credID = [16]byte{9}
	enc = encodeVaultPlain(pv)
	if enc[0] != 4 {
		t.Fatalf("credential + plain restriction written as v%d, want v4", enc[0])
	}
	dec, ok = decodeVaultPlain(enc)
	if !ok || dec.entries[0].devices[0].VMID != "" || dec.entries[1].credID != pv.entries[1].credID {
		t.Fatalf("v4: %v %+v", ok, dec)
	}
	// A VM binding anywhere: v5, with the v4 tail kept.
	pv.entries[0].devices[0].VMID, pv.entries[0].devices[0].VMName = vmA, "DC01"
	enc = encodeVaultPlain(pv)
	if enc[0] != 5 {
		t.Fatalf("VM binding written as v%d, want v5", enc[0])
	}
	dec, ok = decodeVaultPlain(enc)
	if !ok || dec.entries[0].devices[0].VMID != vmA || dec.entries[1].credID != pv.entries[1].credID {
		t.Fatalf("v5 with a credential: %v %+v", ok, dec)
	}
	// The binding removed: back to v4, byte for byte what v4 wrote.
	pv.entries[0].devices[0].VMID, pv.entries[0].devices[0].VMName = "", ""
	if enc = encodeVaultPlain(pv); enc[0] != 4 {
		t.Fatalf("binding removed: written as v%d, want v4", enc[0])
	}
	// A v2 file (no device at all) still decodes and re-encodes as v2.
	v2 := buildV2Bytes(samplePlain())
	if got, ok := decodeVaultPlain(v2); !ok || string(encodeVaultPlain(got)) != string(v2) {
		t.Fatal("a v2 file must read and re-persist unchanged")
	}
}

func TestVaultVMBindingMatch(t *testing.T) {
	v, store := newTestVault()
	if err := v.Create("secret1", 15); err != nil {
		t.Fatal(err)
	}
	const o = "https://a.example"
	host := VaultDevice{Origin: o, TenantID: 1, UUID: "host-uuid", ID: 61, Name: "HV-01"}
	onVM := func(id, name string) VaultDevice { d := host; d.VMID, d.VMName = id, name; return d }
	save := func(label string, devs ...VaultDevice) string {
		t.Helper()
		id, err := v.Save(SaveInput{Origin: o, TenantID: 1, Label: label, Secret: "s-" + label, Devices: devs})
		if err != nil {
			t.Fatalf("save %s: %v", label, err)
		}
		return id
	}
	save("Tenant-wide")                                       // no restriction
	hostID := save("Host only", host)                         // the host itself
	vmAID := save("VM A", onVM(strings.ToUpper(vmA), "DC01")) // (GUID upper case: stored lower case)
	save("VM B", onVM(vmB, "WEB"))
	save("Host and VM B", host, onVM(vmB, "WEB"))
	// The same VM GUID bound under another host: the VM moved (live
	// migration), the item follows it.
	other := VaultDevice{Origin: o, TenantID: 1, UUID: "other-host", ID: 62, VMID: vmA}
	save("VM A on another host", other)

	labels := func(s VaultSession) string {
		var out []string
		for _, m := range v.EntriesForSession(s) {
			out = append(out, m.Label)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		s    VaultSession
		want string
		why  string
	}{
		{VaultSession{Origin: o, TenantID: 1, DeviceID: 61, DeviceUUID: "host-uuid"}, "Host and VM B,Host only,Tenant-wide", "the host's own session: never its VMs' entries"},
		{VaultSession{Origin: o, TenantID: 1, DeviceID: 61, DeviceUUID: "host-uuid", VM: true, VMID: vmA}, "Tenant-wide,VM A,VM A on another host", "VM A's console: its entries and the tenant-wide one, not the host's"},
		{VaultSession{Origin: o, TenantID: 1, DeviceID: 61, VM: true, VMID: vmB}, "Host and VM B,Tenant-wide,VM B", "VM B's console, host matched by id"},
		{VaultSession{Origin: o, TenantID: 1, DeviceID: 61, VM: true, VMID: "11111111-2222-3333-4444-555555555555"}, "Tenant-wide", "another VM of the host"},
		{VaultSession{Origin: o, TenantID: 1, DeviceID: 61, VM: true}, "Tenant-wide", "a VM console whose VM is unknown: unrestricted entries only"},
		{VaultSession{Origin: o, TenantID: 1, DeviceID: 62, DeviceUUID: "other-host", VM: true, VMID: vmA}, "Tenant-wide,VM A,VM A on another host", "VM A migrated to another host: its entries follow it"},
		{VaultSession{Origin: o, TenantID: 2, DeviceID: 61, VM: true, VMID: vmA}, "", "another tenant"},
		{VaultSession{Origin: "https://b.example", TenantID: 1, DeviceID: 61, VM: true, VMID: vmA}, "", "another server"},
	}
	for _, c := range cases {
		if got := labels(c.s); got != c.want {
			t.Errorf("%s: offered %q, want %q", c.why, got, c.want)
		}
	}
	build := func(sess VaultSession, entry string) error {
		_, err := v.BuildTypeText(entry, sess, false, func(_ string, s []byte) ([]byte, error) { return append([]byte(nil), s...), nil })
		return err
	}
	vmSess := VaultSession{Origin: o, TenantID: 1, DeviceID: 61, VM: true, VMID: vmA}
	if err := build(vmSess, vmAID); err != nil {
		t.Fatalf("typing into its VM: %v", err)
	}
	if err := build(vmSess, hostID); !errors.Is(err, errScopeMismatch) {
		t.Fatalf("a host entry typed into its VM = %v, want errScopeMismatch", err)
	}
	if err := build(VaultSession{Origin: o, TenantID: 1, DeviceID: 61}, vmAID); !errors.Is(err, errScopeMismatch) {
		t.Fatalf("a VM entry typed into its host = %v, want errScopeMismatch", err)
	}
	m, _ := v.Get(vmAID)
	if len(m.Devices) != 1 || m.Devices[0].VMID != vmA || m.Devices[0].VMName != "DC01" || m.Devices[0].Name != "HV-01" {
		t.Fatalf("stored VM binding: %+v", m.Devices)
	}

	// Survives a lock / reopen (v5 on disk).
	v.Lock()
	v2 := NewVault(store, &fakeProtector{}, fakeSealer{})
	v2.iterations = 2
	if err := v2.Unlock("secret1"); err != nil {
		t.Fatal(err)
	}
	if got := v2.EntriesForSession(vmSess); len(got) != 3 { // tenant-wide + VM A (both binds)
		t.Fatalf("after reopen: %+v", got)
	}
}

func TestVaultVMBindingValidation(t *testing.T) {
	v, _ := newTestVault()
	_ = v.Create("secret1", 15)
	host := VaultDevice{Origin: "o", TenantID: 1, UUID: "u", ID: 1}
	with := func(id, name string) []VaultDevice { d := host; d.VMID, d.VMName = id, name; return []VaultDevice{d} }
	for _, c := range []struct {
		devs []VaultDevice
		why  string
	}{
		{with("not-a-guid", ""), "malformed GUID"},
		{with("{"+vmA+"}", ""), "braced GUID"},
		{with(strings.Replace(vmA, "-", "x", 1), ""), "separator"},
		{with(vmA[:35]+"g", ""), "non-hex digit"},
		{with(vmA, "a\nb"), "control character in the VM name"},
		{with(vmA, strings.Repeat("x", vaultMaxVMNameRunes+1)), "VM name too long"},
	} {
		if _, err := v.Save(SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: c.devs}); err == nil {
			t.Errorf("%s: accepted", c.why)
		}
	}
	// The host and one of its VMs are two items; the same VM twice is one;
	// a name without a VM is dropped.
	d := host
	d.VMName = "ignored"
	id, err := v.Save(SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x",
		Devices: append(append(with(vmA, "A"), with(strings.ToUpper(vmA), "A again")...), d)})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := v.Get(id)
	if len(m.Devices) != 2 || m.Devices[0].VMID != vmA || m.Devices[1].VMID != "" || m.Devices[1].VMName != "" {
		t.Fatalf("deduplicated: %+v", m.Devices)
	}
}
