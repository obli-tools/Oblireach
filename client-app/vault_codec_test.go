package main

import (
	"encoding/binary"
	"testing"
	"time"
)

func samplePlain() *vaultPlain {
	return &vaultPlain{
		revision: 3, autoLockMin: 15,
		codeSalt: []byte("0123456789abcdef"), codeIter: 600000, codeHash: []byte("0123456789abcdef0123456789abcdef"),
		entries: []vaultPlainEntry{
			{
				id: [16]byte{1, 2, 3}, created: time.Unix(1000, 0), updated: time.Unix(2000, 0), lastUsed: time.Time{},
				secureOnly: true, origin: "https://a.example:8443", tenantID: 5, tenantName: "Acme",
				label: "Domain admin", username: "adm", note: "note é€", secret: []byte("hunter2"),
			},
			{
				id: [16]byte{9}, secureOnly: false, origin: "https://b.example", tenantID: 1,
				label: "L2", username: "", note: "", secret: []byte(""),
			},
		},
	}
}

func TestVaultCodecRoundTrip(t *testing.T) {
	pv := samplePlain()
	enc := encodeVaultPlain(pv)
	dec, ok := decodeVaultPlain(enc)
	if !ok {
		t.Fatalf("decode failed")
	}
	if dec.revision != pv.revision || dec.autoLockMin != pv.autoLockMin {
		t.Fatalf("header mismatch: %+v", dec)
	}
	if len(dec.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(dec.entries))
	}
	e := dec.entries[0]
	if e.origin != "https://a.example:8443" || e.tenantName != "Acme" || e.label != "Domain admin" ||
		e.username != "adm" || e.note != "note é€" || string(e.secret) != "hunter2" || !e.secureOnly {
		t.Fatalf("entry 0 mismatch: %+v", e)
	}
	if !e.created.Equal(time.Unix(1000, 0)) || !e.updated.Equal(time.Unix(2000, 0)) || !e.lastUsed.IsZero() {
		t.Fatalf("entry 0 times mismatch: %+v", e)
	}
}

func TestVaultCodecRejectsTrailingBytes(t *testing.T) {
	enc := encodeVaultPlain(samplePlain())
	enc = append(enc, 0xff)
	if _, ok := decodeVaultPlain(enc); ok {
		t.Fatalf("expected trailing bytes to be refused")
	}
}

func TestVaultCodecRejectsBadVersion(t *testing.T) {
	// v2 (folders/ssh/rdp), v3 (device restriction), v4 (credentials) and
	// v5 (VM bindings) are real, valid formats — 6 is the genuinely-unknown
	// version for this test.
	enc := encodeVaultPlain(samplePlain())
	enc[0] = 6
	if _, ok := decodeVaultPlain(enc); ok {
		t.Fatalf("expected unknown version to be refused")
	}
}

// TestVaultCodecV1StillDecodes proves a vault file written before this pass
// (v1: no kind/parentId/host/port/authMethod on any entry) still loads, with
// every v1 entry defaulting to a password-kind, root-level connection — the
// forward-only migration the format comment at the top of this file promises.
func TestVaultCodecV1StillDecodes(t *testing.T) {
	pv := samplePlain()
	// A real v1 file has no per-entry tail (kind/parentId/host/port/authMethod)
	// that encodeVaultPlain (always v2 now) would write — rebuild it by hand
	// in the historical v1 layout.
	v1 := buildV1Bytes(pv)
	got, ok := decodeVaultPlain(v1)
	if !ok {
		t.Fatalf("v1 file must still decode")
	}
	if len(got.entries) != len(pv.entries) {
		t.Fatalf("entry count mismatch: got %d want %d", len(got.entries), len(pv.entries))
	}
	for i, e := range got.entries {
		if e.kind != vaultKindPassword {
			t.Fatalf("entry %d: v1 entry must default to vaultKindPassword, got %d", i, e.kind)
		}
		if e.parentID != ([16]byte{}) || e.host != "" || e.port != 0 || e.authMethod != 0 {
			t.Fatalf("entry %d: v1 entry must have zero-valued v2 fields, got %+v", i, e)
		}
	}
}

// buildV1Bytes hand-encodes pv in the exact pre-v2 layout (no per-entry tail),
// so the migration test above exercises the real historical byte shape
// instead of a same-process round trip.
func buildV1Bytes(pv *vaultPlain) []byte {
	out := make([]byte, 0, 256)
	out = append(out, 1)
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
	}
	return out
}

func TestVaultCodecRejectsTruncation(t *testing.T) {
	enc := encodeVaultPlain(samplePlain())
	for cut := 0; cut < len(enc); cut += 7 {
		if _, ok := decodeVaultPlain(enc[:cut]); ok {
			t.Fatalf("truncation at %d must not decode successfully", cut)
		}
	}
}

func TestVaultCodecRejectsInvalidUTF8(t *testing.T) {
	pv := samplePlain()
	pv.entries[0].label = string([]byte{0xff, 0xfe})
	enc := encodeVaultPlain(pv)
	if _, ok := decodeVaultPlain(enc); ok {
		t.Fatalf("expected invalid UTF-8 label to be refused")
	}
}

func TestOuterEnvelopeRoundTrip(t *testing.T) {
	blob := []byte("some dpapi blob bytes")
	outer := buildOuter(1, blob)
	format, got, ok := parseOuter(outer)
	if !ok || format != 1 || string(got) != string(blob) {
		t.Fatalf("outer round trip failed: format=%d ok=%v got=%q", format, ok, got)
	}
}

func TestParseOuterRejectsBadMagic(t *testing.T) {
	if _, _, ok := parseOuter([]byte("NOTAVAULTxxxxxx")); ok {
		t.Fatalf("expected bad magic to be refused")
	}
	if _, _, ok := parseOuter(nil); ok {
		t.Fatalf("expected empty input to be refused")
	}
}

func FuzzVaultDecode(f *testing.F) {
	f.Add(encodeVaultPlain(samplePlain()))
	f.Add([]byte{})
	f.Add([]byte{1})
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodeVaultPlain panicked on %x: %v", data, r)
			}
		}()
		decodeVaultPlain(data)
	})
}
