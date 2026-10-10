package main

// vault_codec.go — the vault's own length-prefixed binary encoding, both for
// the outer (unencrypted) envelope and the inner plaintext (only ever seen
// decrypted, in memory, briefly). Deliberately NOT encoding/json: its pooled
// encode buffers would keep copies of the secret around after use.
//
// Outer:  magic "OBRVAULT"(8) | u16 LE format | DPAPI blob (rest of file)
// Inner:  u8 v=1..5 | u32 revision | u32 flags | u16 autoLockMin
//         | salt[16] | u32 iterations | hash[32]
//         | u32 n | n × entry
// entry (v1): id[16] | i64 created | i64 updated | i64 lastUsed | u8 flags
//         | str origin | u32 tenantId | str tenantName
//         | str label | str username | str note | bytes secret
// entry (v2): v1 layout, plus at the end: u8 kind | id[16] parentId
//         | str host | u16 port | u8 authMethod
//         kind: 0=password (v1 shape, scoped by origin+tenantId) | 1=folder
//         (local connection tree, organizational only) | 2=ssh | 3=rdp (both
//         local connections, organized under parentId, NOT scoped by
//         origin/tenantId — origin/tenantId are left empty for these kinds).
//         authMethod (ssh only): 0=password | 1=private key (stored in the
//         same `secret` field as password entries — no new secret field, so
//         the "no field named secret/password on a page-facing shape" guard
//         in vault_bridge_windows_test.go stays meaningful unchanged).
//         A v1 file has no entry past `secret`: decode fills kind=password,
//         parentId=zero, host="", port=0, authMethod=0 for every entry, and
//         the vault re-persists as v2 on its next write (forward-only
//         migration, same pattern as the v1→v2 scenario JSON format on the
//         Obliance server).
// entry (v3): v2 layout, plus at the end: u16 n | n × device
//         device: str origin | u32 tenantId | str uuid | i64 id | str name
//         The machines a password entry is restricted to (vault.go
//         VaultDevice; n = 0: no restriction, the entry's origin + tenant
//         scope as before). A v1/v2 file decodes with n = 0 for every entry
//         (= no restriction, exactly what those entries meant).
//         Written as v3 only while at least one entry HAS a restriction,
//         else as v2 (byte for byte the v2 layout): a vault that uses no
//         restriction stays readable by an older (v2) Desktop. An older
//         Desktop refuses a v3 file as unreadable (its decoder only knows
//         v1/v2): it never silently drops a restriction.
// entry (v4): v3 layout, plus at the end: id[16] credId | bytes passphrase
//         credId: the reusable local credential (kind 4) an ssh/rdp
//         connection uses instead of its own username + secret (zero: its
//         own, as before). passphrase: an SSH private key's passphrase (a
//         key connection or a key credential; empty: none), a second secret
//         kept exactly like `secret`. Kind 4 = a reusable credential of the
//         local base (label, username, authMethod, secret, passphrase; no
//         origin / tenant / host / parent). Written as v4 only while a
//         credential, a reference or a passphrase exists (else v3 / v2 as
//         above): an older Desktop refuses such a file instead of listing a
//         credential as a strange connection or dialing without its key's
//         passphrase.
// entry (v5): v4 layout, plus at the end, for each device of the v3 tail
//         (same count, same order): str vmId | str vmName
//         vmId: the Hyper-V VM (GUID, lower case) the item is bound to
//         ("" = the device itself, as in v3/v4); vmName: its display name.
//         A v1..v4 file decodes with vmId "" for every device (exactly what
//         those items meant: the machine, never one of its VMs). Written as
//         v5 only while at least one device item IS bound to a VM (else
//         v4 / v3 / v2 as above, byte for byte): an older Desktop refuses
//         such a file instead of reading a VM-bound item as its host.
// str/bytes = u16 LE length + payload (bytes has no separate marker: only the
// secret field is raw bytes, everything else is UTF-8 text).
//
// Decoding is strict: every length is bounds-checked against what remains,
// trailing bytes after the last entry are refused, and every string is
// checked with utf8.Valid. FuzzVaultDecode (vault_codec_test.go) drives this
// with arbitrary input and only requires "no panic, ok or a clean error".

import (
	"encoding/binary"
	"errors"
	"time"
	"unicode/utf8"
)

var vaultMagic = []byte("OBRVAULT")

func buildOuter(format uint16, blob []byte) []byte {
	out := make([]byte, 0, 10+len(blob))
	out = append(out, vaultMagic...)
	out = binary.LittleEndian.AppendUint16(out, format)
	out = append(out, blob...)
	return out
}

// parseOuter splits the outer envelope. ok is false on anything that is not
// a well-formed OBRVAULT file (wrong magic, truncated header).
func parseOuter(outer []byte) (format uint16, blob []byte, ok bool) {
	if len(outer) < 10 || string(outer[:8]) != string(vaultMagic) {
		return 0, nil, false
	}
	format = binary.LittleEndian.Uint16(outer[8:10])
	return format, outer[10:], true
}

// Entry kinds (v2). See the format comment above.
const (
	vaultKindPassword byte = 0
	vaultKindFolder   byte = 1
	vaultKindSSH      byte = 2
	vaultKindRDP      byte = 3
	// vaultKindCredential (v4): a reusable username + password / private key
	// of the local base, referenced by connections (VaultEntry.CredID).
	vaultKindCredential byte = 4
)

// Auth methods for an ssh entry (v2).
const (
	vaultAuthPassword byte = 0
	vaultAuthKey      byte = 1
)

type vaultPlainEntry struct {
	id                                [16]byte
	created, updated, lastUsed        time.Time
	secureOnly                        bool
	origin                            string
	tenantID                          uint32
	tenantName, label, username, note string
	secret                            []byte

	// v2 fields — zero-valued (kind=vaultKindPassword) when decoded from a v1 file.
	kind       byte
	parentID   [16]byte
	host       string
	port       uint16
	authMethod byte

	// v3 — empty when decoded from a v1/v2 file (no restriction).
	devices []VaultDevice

	// v4 — zero / empty when decoded from a v1..v3 file.
	credID     [16]byte
	passphrase []byte
}

// vaultPlainVersion is the newest inner version (decode accepts 1..it).
const vaultPlainVersion = 5

// vaultPlainWriteVersion: 5 when a device item is bound to a VM; else 4
// with a credential, a reference to one or a passphrase; else 3 when an
// entry has a device restriction; else 2 (see the format comment: an older
// Desktop keeps reading such a vault).
func vaultPlainWriteVersion(pv *vaultPlain) byte {
	ver := byte(2)
	for _, e := range pv.entries {
		for _, d := range e.devices {
			if d.VMID != "" {
				return 5
			}
		}
		if e.kind == vaultKindCredential || e.credID != zeroVaultID || len(e.passphrase) > 0 {
			ver = 4
		}
		if len(e.devices) > 0 && ver < 3 {
			ver = 3
		}
	}
	return ver
}

type vaultPlain struct {
	revision    uint32
	autoLockMin uint16
	codeSalt    []byte
	codeIter    uint32
	codeHash    []byte
	entries     []vaultPlainEntry
}

// Decoding ceilings: never tighter than what vault.go lets Save write (a
// field Save accepts but the decoder refused made the whole vault
// unreadable at its next unlock: a 500-rune note in a non-Latin script, an
// SSH private key past 512 bytes). Both stay within the u16 length prefix.
const (
	vaultCodecMaxStr    = 4096  // a 500-rune note at 4 bytes a rune; real fields are bounded by vault.go's validation
	vaultCodecMaxSecret = 65535 // an SSH private key (vaultMaxKeyRunes, ASCII PEM); the u16 prefix's own limit
)

func putStr(dst []byte, s string) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(s)))
	return append(dst, s...)
}

func putBytes(dst []byte, b []byte) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(b)))
	return append(dst, b...)
}

func putTime(dst []byte, t time.Time) []byte {
	var unix int64
	if !t.IsZero() {
		unix = t.Unix()
	}
	return binary.LittleEndian.AppendUint64(dst, uint64(unix))
}

func encodeVaultPlain(pv *vaultPlain) []byte {
	out := make([]byte, 0, 256)
	ver := vaultPlainWriteVersion(pv)
	out = append(out, ver) // v2, v3 (a device restriction) or v4 (credentials) — decode accepts v1..v4
	out = binary.LittleEndian.AppendUint32(out, pv.revision)
	out = binary.LittleEndian.AppendUint32(out, 1) // flags: bit0 lockEnabled always set (v1: lock is mandatory)
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
		// v2 tail
		out = append(out, e.kind)
		out = append(out, e.parentID[:]...)
		out = putStr(out, e.host)
		out = binary.LittleEndian.AppendUint16(out, e.port)
		out = append(out, e.authMethod)
		if ver < 3 {
			continue
		}
		// v3 tail
		out = binary.LittleEndian.AppendUint16(out, uint16(len(e.devices)))
		for _, d := range e.devices {
			out = putStr(out, d.Origin)
			out = binary.LittleEndian.AppendUint32(out, d.TenantID)
			out = putStr(out, d.UUID)
			out = binary.LittleEndian.AppendUint64(out, uint64(d.ID))
			out = putStr(out, d.Name)
		}
		if ver < 4 {
			continue
		}
		// v4 tail
		out = append(out, e.credID[:]...)
		out = putBytes(out, e.passphrase)
		if ver < 5 {
			continue
		}
		// v5 tail: each device's VM binding, in the v3 tail's order
		for _, d := range e.devices {
			out = putStr(out, d.VMID)
			out = putStr(out, d.VMName)
		}
	}
	return out
}

type byteReader struct {
	b   []byte
	pos int
}

func (r *byteReader) remaining() int { return len(r.b) - r.pos }

func (r *byteReader) u8() (byte, bool) {
	if r.remaining() < 1 {
		return 0, false
	}
	v := r.b[r.pos]
	r.pos++
	return v, true
}

func (r *byteReader) u16() (uint16, bool) {
	if r.remaining() < 2 {
		return 0, false
	}
	v := binary.LittleEndian.Uint16(r.b[r.pos:])
	r.pos += 2
	return v, true
}

func (r *byteReader) u32() (uint32, bool) {
	if r.remaining() < 4 {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v, true
}

func (r *byteReader) u64() (uint64, bool) {
	if r.remaining() < 8 {
		return 0, false
	}
	v := binary.LittleEndian.Uint64(r.b[r.pos:])
	r.pos += 8
	return v, true
}

func (r *byteReader) fixed(n int) ([]byte, bool) {
	if r.remaining() < n {
		return nil, false
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v, true
}

func (r *byteReader) bytesN(max int) ([]byte, bool) {
	n, ok := r.u16()
	if !ok || int(n) > max {
		return nil, false
	}
	return r.fixed(int(n))
}

func (r *byteReader) str(max int) (string, bool) {
	b, ok := r.bytesN(max)
	if !ok || !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

func (r *byteReader) timeVal() (time.Time, bool) {
	v, ok := r.u64()
	if !ok {
		return time.Time{}, false
	}
	if v == 0 {
		return time.Time{}, true
	}
	return time.Unix(int64(v), 0), true
}

// decodeVaultPlain strictly parses the inner plaintext. Trailing bytes after
// the last entry are refused (a truncation-extension attack on the DPAPI
// layer would otherwise be silently ignored).
func decodeVaultPlain(plain []byte) (*vaultPlain, bool) {
	r := &byteReader{b: plain}
	v, ok := r.u8()
	if !ok || v < 1 || v > vaultPlainVersion {
		return nil, false
	}
	pv := &vaultPlain{}
	if pv.revision, ok = r.u32(); !ok {
		return nil, false
	}
	if _, ok = r.u32(); !ok { // flags: reserved, ignored in v1
		return nil, false
	}
	if pv.autoLockMin, ok = r.u16(); !ok {
		return nil, false
	}
	if pv.codeSalt, ok = r.bytesN(64); !ok {
		return nil, false
	}
	pv.codeSalt = append([]byte(nil), pv.codeSalt...)
	if pv.codeIter, ok = r.u32(); !ok || pv.codeIter == 0 {
		return nil, false
	}
	if pv.codeHash, ok = r.bytesN(64); !ok {
		return nil, false
	}
	pv.codeHash = append([]byte(nil), pv.codeHash...)
	n, ok := r.u32()
	if !ok || n > vaultMaxEntries {
		return nil, false
	}
	pv.entries = make([]vaultPlainEntry, 0, n)
	for i := uint32(0); i < n; i++ {
		var e vaultPlainEntry
		idb, ok := r.fixed(16)
		if !ok {
			return nil, false
		}
		copy(e.id[:], idb)
		if e.created, ok = r.timeVal(); !ok {
			return nil, false
		}
		if e.updated, ok = r.timeVal(); !ok {
			return nil, false
		}
		if e.lastUsed, ok = r.timeVal(); !ok {
			return nil, false
		}
		flags, ok := r.u8()
		if !ok {
			return nil, false
		}
		e.secureOnly = flags&1 != 0
		if e.origin, ok = r.str(vaultCodecMaxStr); !ok {
			return nil, false
		}
		if e.tenantID, ok = r.u32(); !ok {
			return nil, false
		}
		if e.tenantName, ok = r.str(vaultCodecMaxStr); !ok {
			return nil, false
		}
		if e.label, ok = r.str(vaultCodecMaxStr); !ok {
			return nil, false
		}
		if e.username, ok = r.str(vaultCodecMaxStr); !ok {
			return nil, false
		}
		if e.note, ok = r.str(vaultCodecMaxStr); !ok {
			return nil, false
		}
		secret, ok := r.bytesN(vaultCodecMaxSecret)
		if !ok {
			return nil, false
		}
		e.secret = append([]byte(nil), secret...)
		if v >= 2 {
			if e.kind, ok = r.u8(); !ok {
				return nil, false
			}
			pid, ok2 := r.fixed(16)
			if !ok2 {
				return nil, false
			}
			copy(e.parentID[:], pid)
			if e.host, ok = r.str(vaultCodecMaxStr); !ok {
				return nil, false
			}
			if e.port, ok = r.u16(); !ok {
				return nil, false
			}
			if e.authMethod, ok = r.u8(); !ok {
				return nil, false
			}
		}
		if v >= 3 {
			nd, ok := r.u16()
			if !ok || int(nd) > vaultMaxEntryDevices {
				return nil, false
			}
			for j := 0; j < int(nd); j++ {
				var d VaultDevice
				var ok2 bool
				if d.Origin, ok2 = r.str(vaultCodecMaxStr); !ok2 {
					return nil, false
				}
				if d.TenantID, ok2 = r.u32(); !ok2 {
					return nil, false
				}
				if d.UUID, ok2 = r.str(vaultCodecMaxStr); !ok2 {
					return nil, false
				}
				id, ok3 := r.u64()
				if !ok3 {
					return nil, false
				}
				d.ID = int64(id)
				if d.Name, ok2 = r.str(vaultCodecMaxStr); !ok2 {
					return nil, false
				}
				e.devices = append(e.devices, d)
			}
		}
		if v >= 4 {
			cid, ok2 := r.fixed(16)
			if !ok2 {
				return nil, false
			}
			copy(e.credID[:], cid)
			pass, ok3 := r.bytesN(vaultCodecMaxSecret)
			if !ok3 {
				return nil, false
			}
			e.passphrase = append([]byte(nil), pass...)
		}
		if v >= 5 {
			for j := range e.devices {
				var ok2 bool
				if e.devices[j].VMID, ok2 = r.str(vaultCodecMaxStr); !ok2 {
					return nil, false
				}
				if e.devices[j].VMName, ok2 = r.str(vaultCodecMaxStr); !ok2 {
					return nil, false
				}
			}
		}
		// v1 entries: kind/parentID/host/port/authMethod stay zero-valued
		// (vaultKindPassword, zero parent, no host/port/key); v1/v2 entries
		// have no devices (no restriction) — see the format comment at the
		// top of this file.
		pv.entries = append(pv.entries, e)
	}
	if r.remaining() != 0 {
		return nil, false // trailing garbage
	}
	return pv, true
}

var errCodecTruncated = errors.New("truncated")
