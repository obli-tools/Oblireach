package main

// vault_devices_test.go — the v3 device restriction of a password entry
// (vault.go VaultDevice / sessionMatch) and the v2 → v3 format migration
// (vault_codec.go).

import (
	"encoding/binary"
	"errors"
	"testing"
)

// buildV2Bytes hand-encodes pv in the exact v2 layout (v1 entry + the v2
// tail, no device list), the shape every vault written before v3 has.
func buildV2Bytes(pv *vaultPlain) []byte {
	out := make([]byte, 0, 256)
	out = append(out, 2)
	out = binary.LittleEndian.AppendUint32(out, pv.revision)
	out = binary.LittleEndian.AppendUint32(out, 1)
	out = binary.LittleEndian.AppendUint16(out, pv.autoLockMin)
	out = putBytes(out, pv.codeSalt)
	out = binary.LittleEndian.AppendUint32(out, pv.codeIter)
	out = putBytes(out, pv.codeHash)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(pv.entries)))
	for _, e := range pv.entries {
		out = append(out, e.id[:]...)
		out = putTime(out, e.created)
		out = putTime(out, e.updated)
		out = putTime(out, e.lastUsed)
		var flags byte
		if e.secureOnly {
			flags |= 1
		}
		out = append(out, flags)
		out = putStr(out, e.origin)
		out = binary.LittleEndian.AppendUint32(out, e.tenantID)
		out = putStr(out, e.tenantName)
		out = putStr(out, e.label)
		out = putStr(out, e.username)
		out = putStr(out, e.note)
		out = putBytes(out, e.secret)
		out = append(out, e.kind)
		out = append(out, e.parentID[:]...)
		out = putStr(out, e.host)
		out = binary.LittleEndian.AppendUint16(out, e.port)
		out = append(out, e.authMethod)
	}
	return out
}

func TestVaultCodecV3RoundTripDevices(t *testing.T) {
	pv := samplePlain()
	pv.entries[0].devices = []VaultDevice{
		{Origin: "https://a.example:8443", TenantID: 5, UUID: "0f0e-uuid", ID: 61, Name: "SRV-01"},
		{Origin: "https://b.example", TenantID: 7, UUID: "", ID: 9, Name: "Poste é"},
	}
	enc := encodeVaultPlain(pv)
	if enc[0] != 3 {
		t.Fatalf("written as v%d, want v3", enc[0])
	}
	dec, ok := decodeVaultPlain(enc)
	if !ok {
		t.Fatal("v3 decode failed")
	}
	got := dec.entries[0].devices
	if len(got) != 2 || got[0] != pv.entries[0].devices[0] || got[1] != pv.entries[0].devices[1] {
		t.Fatalf("devices round trip: %+v", got)
	}
	if len(dec.entries[1].devices) != 0 {
		t.Fatalf("an entry without devices must stay unrestricted: %+v", dec.entries[1].devices)
	}
	// Truncated anywhere inside the device list: refused, never half-read.
	for cut := len(enc) - 1; cut > len(enc)-60; cut-- {
		if _, ok := decodeVaultPlain(enc[:cut]); ok {
			t.Fatalf("truncation at %d decoded", cut)
		}
	}
}

func TestVaultCodecV2StillDecodes(t *testing.T) {
	pv := samplePlain()
	pv.entries[1].kind = vaultKindRDP
	pv.entries[1].host = "rdp.example"
	pv.entries[1].port = 3389
	v2 := buildV2Bytes(pv)
	got, ok := decodeVaultPlain(v2)
	if !ok {
		t.Fatal("a v2 file must still decode")
	}
	for i, e := range got.entries {
		if len(e.devices) != 0 {
			t.Fatalf("entry %d: a v2 entry has no restriction, got %+v", i, e.devices)
		}
	}
	if got.entries[1].kind != vaultKindRDP || got.entries[1].host != "rdp.example" || got.entries[1].port != 3389 {
		t.Fatalf("v2 fields lost: %+v", got.entries[1])
	}
	// Written back without any restriction: still v2, byte for byte (an
	// older Desktop keeps reading it), and it reads the same.
	again := encodeVaultPlain(got)
	if again[0] != 2 || string(again) != string(v2) {
		t.Fatalf("re-persisted as v%d (same bytes: %v), want the v2 bytes", again[0], string(again) == string(v2))
	}
	if back, ok := decodeVaultPlain(again); !ok || len(back.entries) != 2 || back.entries[0].label != "Domain admin" {
		t.Fatalf("v2 -> v3 round trip failed: %v %+v", ok, back)
	}
}

func TestVaultCodecRejectsTooManyDevices(t *testing.T) {
	pv := samplePlain()
	for i := 0; i <= vaultMaxEntryDevices; i++ {
		pv.entries[0].devices = append(pv.entries[0].devices, VaultDevice{Origin: "https://a", TenantID: 1, ID: int64(i + 1)})
	}
	if _, ok := decodeVaultPlain(encodeVaultPlain(pv)); ok {
		t.Fatal("more devices than vaultMaxEntryDevices must be refused")
	}
}

// A vault file written by a v2 Desktop: unlocks, its entries are offered
// exactly as before (no restriction), and the next write is v3.
func TestVaultMigrationV2FileToV3(t *testing.T) {
	salt := []byte("0123456789abcdef")
	pv := &vaultPlain{
		revision: 4, autoLockMin: 15, codeSalt: salt, codeIter: 2,
		codeHash: pbkdf2SHA256([]byte("secret1"), salt, 2, 32),
		entries: []vaultPlainEntry{{
			id: [16]byte{7}, origin: "https://a.example", tenantID: 3, label: "Old admin", username: "adm", secret: []byte("pw-old"),
		}},
	}
	prot := &fakeProtector{}
	blob, _ := prot.Protect(buildV2Bytes(pv))
	store := &fakeStore{data: buildOuter(1, blob)}
	v := NewVault(store, prot, fakeSealer{})
	v.iterations = 2
	if err := v.Unlock("secret1"); err != nil {
		t.Fatalf("Unlock v2 file: %v", err)
	}
	for _, s := range []VaultSession{
		{Origin: "https://a.example", TenantID: 3},
		{Origin: "https://a.example", TenantID: 3, DeviceID: 61},
		{Origin: "https://a.example", TenantID: 3, DeviceID: 62, DeviceUUID: "u62"},
	} {
		if got := v.EntriesForSession(s); len(got) != 1 || len(got[0].Devices) != 0 {
			t.Fatalf("an old entry must be offered on every device of its tenant (%+v): %+v", s, got)
		}
	}
	if got := v.EntriesForSession(VaultSession{Origin: "https://a.example", TenantID: 4, DeviceID: 61}); len(got) != 0 {
		t.Fatalf("still scoped to its tenant: %+v", got)
	}
	innerVer := func() byte {
		_, outerBlob, _ := parseOuter(store.data)
		var ver byte
		_ = prot.Unprotect(outerBlob, func(plain []byte) error { ver = plain[0]; return nil })
		return ver
	}
	if _, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 3, Label: "New", Secret: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if ver := innerVer(); ver != 2 {
		t.Fatalf("no restriction anywhere: written as v%d, want v2 (an older Desktop still reads it)", ver)
	}
	id, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 3, Label: "One machine", Secret: "x",
		Devices: []VaultDevice{{Origin: "https://a.example", TenantID: 3, UUID: "u61", ID: 61}}})
	if err != nil {
		t.Fatalf("Save restricted: %v", err)
	}
	if ver := innerVer(); ver != 3 {
		t.Fatalf("a restriction: written as v%d, want v3", ver)
	}
	if _, err := v.Save(SaveInput{ID: id, Origin: "https://a.example", TenantID: 3, Label: "One machine"}); err != nil {
		t.Fatal(err)
	}
	if ver := innerVer(); ver != 2 {
		t.Fatalf("the last restriction removed: written as v%d, want v2", ver)
	}
}

func TestVaultDeviceRestriction(t *testing.T) {
	v, store := newTestVault()
	if err := v.Create("secret1", 15); err != nil {
		t.Fatal(err)
	}
	devs := []VaultDevice{
		{Origin: "https://a.example", TenantID: 1, UUID: "uuid-61", ID: 61, Name: "SRV-61"},
		{Origin: "https://b.example", TenantID: 7, ID: 9, Name: "B-9"},                   // another server, no uuid known
		{Origin: "https://a.example", TenantID: 1, UUID: "UUID-61", ID: 61, Name: "dup"}, // deduplicated (uuid case)
	}
	id, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "Local admin SRV-61", Secret: "s3cret", Devices: devs})
	if err != nil {
		t.Fatalf("Save restricted: %v", err)
	}
	open, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, Label: "Whole tenant", Secret: "t"})
	if err != nil {
		t.Fatal(err)
	}
	labels := func(s VaultSession) []string {
		var out []string
		for _, m := range v.EntriesForSession(s) {
			out = append(out, m.Label)
		}
		return out
	}
	cases := []struct {
		s    VaultSession
		want int // entries offered
		why  string
	}{
		{VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 61}, 2, "its device, by id"},
		{VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 999, DeviceUUID: "uuid-61"}, 2, "its device re-enrolled (new id, same uuid)"},
		{VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 61, DeviceUUID: "other"}, 1, "same id, another uuid: not its device"},
		{VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 62}, 1, "another device of the tenant: only the open entry"},
		{VaultSession{Origin: "https://a.example", TenantID: 1}, 1, "device unknown: restricted entries never offered"},
		{VaultSession{Origin: "https://a.example", TenantID: 2, DeviceID: 61}, 0, "same id in another tenant"},
		{VaultSession{Origin: "https://b.example", TenantID: 7, DeviceID: 9}, 1, "its device on another server"},
		{VaultSession{Origin: "https://b.example", TenantID: 7, DeviceID: 9, DeviceUUID: "any"}, 1, "a device stored without uuid: matched by id"},
		{VaultSession{Origin: "https://c.example", TenantID: 7, DeviceID: 9}, 0, "same id on an unknown server"},
	}
	for _, c := range cases {
		if got := labels(c.s); len(got) != c.want {
			t.Errorf("%s: offered %v, want %d entries", c.why, got, c.want)
		}
	}
	build := func(sess VaultSession, entry string) error {
		_, err := v.BuildTypeText(entry, sess, false, func(_ string, s []byte) ([]byte, error) { return append([]byte(nil), s...), nil })
		return err
	}
	if err := build(VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 61}, id); err != nil {
		t.Fatalf("typing on its device: %v", err)
	}
	if err := build(VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 62}, id); !errors.Is(err, errScopeMismatch) {
		t.Fatalf("typing on another device = %v, want errScopeMismatch", err)
	}
	if err := build(VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 62}, open); err != nil {
		t.Fatalf("the open entry on any device of its tenant: %v", err)
	}
	m, _ := v.Get(id)
	if len(m.Devices) != 2 {
		t.Fatalf("stored restriction (deduplicated) = %+v", m.Devices)
	}
	m.Devices[0].ID = 12345 // a caller's copy never changes the vault
	if again, _ := v.Get(id); again.Devices[0].ID != 61 {
		t.Fatal("meta() must hand out a copy of the device list")
	}

	// Survives a lock / reopen (v3 on disk).
	v.Lock()
	v2 := NewVault(store, &fakeProtector{}, fakeSealer{})
	v2.iterations = 2
	if err := v2.Unlock("secret1"); err != nil {
		t.Fatal(err)
	}
	if got := v2.EntriesForSession(VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 62}); len(got) != 1 || got[0].Label != "Whole tenant" {
		t.Fatalf("after reopen: %+v", got)
	}
	// Edited back to "every machine": offered everywhere in its tenant again.
	if _, err := v2.Save(SaveInput{ID: id, Origin: "https://a.example", TenantID: 1, Label: "Local admin SRV-61"}); err != nil {
		t.Fatal(err)
	}
	if got := v2.EntriesForSession(VaultSession{Origin: "https://a.example", TenantID: 1, DeviceID: 62}); len(got) != 2 {
		t.Fatalf("restriction removed: %+v", got)
	}
}

func TestVaultDeviceRestrictionPlaceFollowsDevices(t *testing.T) {
	v, _ := newTestVault()
	_ = v.Create("secret1", 15)
	// The form's server/tenant is not one of the devices': the entry's place
	// becomes its first device's (what the management list shows).
	id, err := v.Save(SaveInput{Origin: "https://a.example", TenantID: 1, TenantName: "Acme", Label: "x", Secret: "y",
		Devices: []VaultDevice{{Origin: "https://b.example", TenantID: 7, ID: 9}}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := v.Get(id)
	if m.Origin != "https://b.example" || m.TenantID != 7 || m.TenantName != "" {
		t.Fatalf("place = %q/%d/%q", m.Origin, m.TenantID, m.TenantName)
	}
}

func TestVaultDeviceRestrictionValidation(t *testing.T) {
	v, _ := newTestVault()
	_ = v.Create("secret1", 15)
	good := VaultDevice{Origin: "https://a.example", TenantID: 1, UUID: "u-1", ID: 1}
	bad := []struct {
		in  SaveInput
		why string
	}{
		{SaveInput{Kind: vaultKindSSH, Label: "s", Host: "h", Port: 22, Secret: "x", Devices: []VaultDevice{good}}, "a local connection is not per device"},
		{SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: []VaultDevice{{Origin: "", TenantID: 1, ID: 1}}}, "no origin"},
		{SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: []VaultDevice{{Origin: "o", TenantID: 0, ID: 1}}}, "no tenant"},
		{SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: []VaultDevice{{Origin: "o", TenantID: 1}}}, "no identity"},
		{SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: []VaultDevice{{Origin: "o", TenantID: 1, ID: -2}}}, "negative id"},
		{SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: []VaultDevice{{Origin: "o", TenantID: 1, UUID: "a b", ID: 1}}}, "uuid with a space"},
		{SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: []VaultDevice{{Origin: "o", TenantID: 1, ID: 1, Name: "a\nb"}}}, "control char in the name"},
	}
	for _, c := range bad {
		if _, err := v.Save(c.in); err == nil {
			t.Errorf("%s: accepted", c.why)
		}
	}
	many := make([]VaultDevice, vaultMaxEntryDevices+1)
	for i := range many {
		many[i] = VaultDevice{Origin: "o", TenantID: 1, ID: int64(i + 1)}
	}
	if _, err := v.Save(SaveInput{Origin: "o", TenantID: 1, Label: "l", Secret: "x", Devices: many}); !errors.Is(err, errTooLong) {
		t.Errorf("too many devices = %v, want errTooLong", err)
	}
}
