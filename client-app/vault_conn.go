package main

// vault_conn.go — the connection tree's bulk operations (import / export:
// connimport.go, connexport.go), on the vault's own entries (vault.go). Kept
// apart from vault.go: additive, the single-entry paths (Save, Delete...)
// are unchanged.

import (
	"crypto/rand"
	"errors"
	"sort"
	"strings"
)

// ConnTree: every folder / connection of the local tree (never the
// passwords of the "password" kind, never a secret).
func (v *Vault) ConnTree() []VaultEntryMeta {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return nil
	}
	var out []VaultEntryMeta
	for _, e := range v.entries {
		if e.Kind == vaultKindFolder || e.Kind == vaultKindSSH || e.Kind == vaultKindRDP {
			out = append(out, e.meta())
		}
	}
	return out
}

// ExportConnections: the tree as export nodes; withSecrets adds each
// connection's secret (the caller encrypts them: connexport.go).
func (v *Vault) ExportConnections(withSecrets bool) ([]connExportNode, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return nil, errVaultLocked
	}
	var out []connExportNode
	for _, e := range v.entries {
		var typ string
		switch e.Kind {
		case vaultKindFolder:
			typ = "folder"
		case vaultKindSSH:
			typ = "ssh"
		case vaultKindRDP:
			typ = "rdp"
		default:
			continue
		}
		n := connExportNode{ID: idHex(e.ID), Parent: parentIDToHex(e.ParentID), Type: typ, Name: e.Label}
		if typ != "folder" {
			// (a connection using a credential: exported with the
			// credential's username / secret, a file on its own)
			src := v.connSourceLocked(e)
			n.Host, n.Port, n.Username = e.Host, int(e.Port), src.Username
			n.Auth = "password"
			if e.Kind == vaultKindSSH && src.AuthMethod == vaultAuthKey {
				n.Auth = "key"
			}
			if withSecrets && src.secret != nil {
				if err := v.seal.Open(src.secret, func(p []byte) error { n.secret = string(p); return nil }); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, n)
	}
	v.touch()
	return out, nil
}

// ImportConnections writes plan below the folder parentHex ("" = root), in
// one save: the folders of each item are found (same name, same parent; the
// case does not count) or created; a Replace item updates that connection in
// place (it stays in its folder; its name, host, port, user and auth follow
// the import; its secret too when the import has
// one, else it keeps its own); the others are new connections, with an
// empty secret when the import has none (the operator sets it later:
// Edit). Nothing is written when the save fails (the entries are restored).
func (v *Vault) ImportConnections(parentHex string, plan []connPlanItem) (connReport, error) {
	rep := connReport{Lines: []connReportLine{}}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return rep, errVaultLocked
	}
	parentID, ok := parentIDFromHex(parentHex)
	if !ok {
		return rep, errBadChars
	}
	if parentID != zeroVaultID {
		if p, _ := v.findLocked(parentHex); p == nil || p.Kind != vaultKindFolder {
			return rep, errNotFound
		}
	}
	// The state to come back to if the save fails.
	savedEntries := append([]*VaultEntry(nil), v.entries...)
	savedValues := map[*VaultEntry]VaultEntry{}

	newID := func() ([16]byte, error) {
		var id [16]byte
		_, err := rand.Read(id[:])
		return id, err
	}
	folderCache := map[string][16]byte{}
	folderFor := func(path []string) ([16]byte, error) {
		cur := parentID
		key := ""
		for _, name := range path {
			key += "\x00" + strings.ToLower(name)
			if id, ok := folderCache[key]; ok {
				cur = id
				continue
			}
			var found *VaultEntry
			for _, e := range v.entries {
				if e.Kind == vaultKindFolder && e.ParentID == cur && strings.EqualFold(e.Label, name) {
					found = e
					break
				}
			}
			if found == nil {
				if len(v.entries) >= vaultMaxEntries {
					return cur, errVaultFull
				}
				id, err := newID()
				if err != nil {
					return cur, err
				}
				found = &VaultEntry{ID: id, Created: v.now(), Updated: v.now(), Kind: vaultKindFolder, Label: name, ParentID: cur}
				v.entries = append(v.entries, found)
				rep.Folders++
			}
			folderCache[key] = found.ID
			cur = found.ID
		}
		return cur, nil
	}

	for _, p := range plan {
		it := p.Item
		line := connReportLine{Ref: p.Ref, Name: it.Name}
		auth := byte(vaultAuthPassword)
		if it.KeyAuth && it.Kind == vaultKindSSH {
			auth = vaultAuthKey
		}
		in := SaveInput{Label: it.Name, Username: it.Username, Kind: it.Kind, Host: it.Host, Port: it.Port, AuthMethod: auth, Secret: it.Secret}
		if err := validateSaveInput(&in); err != nil {
			line.Result, line.Reason = "failed", connReasonInvalid
			rep.Failed++
			rep.Lines = append(rep.Lines, line)
			continue
		}
		var sealed []byte
		if it.Secret != "" {
			s, err := v.seal.Seal([]byte(it.Secret))
			if err != nil {
				v.entries = savedEntries
				return connReport{}, err
			}
			sealed = s
		}
		if p.Replace != "" {
			e, _ := v.findLocked(p.Replace)
			if e == nil || e.Kind != it.Kind {
				line.Result, line.Reason = "failed", connReasonDupGone
				rep.Failed++
				rep.Lines = append(rep.Lines, line)
				continue
			}
			if _, kept := savedValues[e]; !kept {
				savedValues[e] = *e
			}
			e.Label, e.Host, e.Port, e.Username, e.AuthMethod = it.Name, it.Host, it.Port, it.Username, auth
			e.Updated = v.now()
			if sealed != nil {
				// (the previous sealed bytes are restored on failure: not
				// wiped here). Its own secret now, as an import has: no
				// credential, no passphrase of a former key.
				e.secret, e.hasSecret = sealed, true
				e.CredID, e.passphrase, e.hasPass = zeroVaultID, nil, false
				line.Secret = true
			} else if e.CredID != zeroVaultID && e.Kind == vaultKindRDP {
				e.AuthMethod = vaultAuthPassword
			}
			line.Result = "replaced"
			rep.Replaced++
			rep.Lines = append(rep.Lines, line)
			continue
		}
		folder, err := folderFor(it.Folder)
		if err == nil && len(v.entries) >= vaultMaxEntries {
			err = errVaultFull
		}
		if err != nil {
			if errors.Is(err, errVaultFull) {
				line.Result, line.Reason = "failed", connReasonFull
				rep.Failed++
				rep.Lines = append(rep.Lines, line)
				continue
			}
			v.entries = savedEntries
			return connReport{}, err
		}
		id, err := newID()
		if err != nil {
			v.entries = savedEntries
			return connReport{}, err
		}
		if sealed == nil {
			if sealed, err = v.seal.Seal(nil); err != nil { // (an empty secret, set later: Edit)
				v.entries = savedEntries
				return connReport{}, err
			}
		} else {
			line.Secret = true
		}
		v.entries = append(v.entries, &VaultEntry{ID: id, Created: v.now(), Updated: v.now(), Kind: it.Kind, ParentID: folder,
			Label: it.Name, Host: it.Host, Port: it.Port, Username: it.Username, AuthMethod: auth, secret: sealed, hasSecret: it.Secret != ""})
		line.Result = "imported"
		rep.Imported++
		rep.Lines = append(rep.Lines, line)
	}
	if rep.Imported+rep.Replaced+rep.Folders == 0 {
		return rep, nil
	}
	v.touch()
	if err := v.persistLocked(); err != nil {
		v.entries = savedEntries
		for e, val := range savedValues {
			*e = val
		}
		return connReport{}, err
	}
	return rep, nil
}

var errVaultFull = errors.New("vault full")

// ---------------------------------------------------------------------------
// v4: reusable credentials of the local base
// ---------------------------------------------------------------------------
//
// A credential (vaultKindCredential) is a named username + password or
// private key (+ its passphrase). A connection refers to it (CredID): not a
// copy, so changing the credential changes every connection using it, and a
// credential still used cannot be deleted (Delete: errCredInUse). The
// secrets never leave this file except through ResolveConn (Go dialers
// only) and Reveal / RevealPassphrase (the code re-checked by the caller).

// VaultCredUse is one connection using a credential (what the page shows
// when a deletion is refused, and its count).
type VaultCredUse struct {
	ID    string
	Label string
	Kind  byte
}

// VaultCredential: a credential's description (never its secret) and the
// connections using it, sorted by label.
type VaultCredential struct {
	VaultEntryMeta
	Uses []VaultCredUse
}

// Credentials lists the credentials (sorted by label, case ignored); empty
// and non-nil while locked.
func (v *Vault) Credentials() []VaultCredential {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := []VaultCredential{}
	if v.state != vaultUnlocked {
		return out
	}
	for _, e := range v.entries {
		if e.Kind != vaultKindCredential {
			continue
		}
		c := VaultCredential{VaultEntryMeta: e.meta(), Uses: []VaultCredUse{}}
		for _, o := range v.entries {
			if (o.Kind == vaultKindSSH || o.Kind == vaultKindRDP) && o.CredID == e.ID {
				c.Uses = append(c.Uses, VaultCredUse{ID: idHex(o.ID), Label: o.Label, Kind: o.Kind})
			}
		}
		sort.SliceStable(c.Uses, func(i, j int) bool { return strings.ToLower(c.Uses[i].Label) < strings.ToLower(c.Uses[j].Label) })
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// connSourceLocked: where a connection's username / auth / secret come from
// — its credential when it uses one (and it still exists), else itself.
func (v *Vault) connSourceLocked(e *VaultEntry) *VaultEntry {
	if e.CredID == zeroVaultID {
		return e
	}
	for _, c := range v.entries {
		if c.ID == e.CredID && c.Kind == vaultKindCredential {
			return c
		}
	}
	return e
}

// connMetaLocked: a tree node's description as the lists show it — a
// connection using a credential shows the credential's username and auth.
func (v *Vault) connMetaLocked(e *VaultEntry) VaultEntryMeta {
	m := e.meta()
	if (e.Kind == vaultKindSSH || e.Kind == vaultKindRDP) && e.CredID != zeroVaultID {
		if src := v.connSourceLocked(e); src != e {
			m.Username, m.AuthMethod = src.Username, src.AuthMethod
			m.Stored, m.KeyProtected = false, src.hasPass
		}
	}
	return m
}

// ResolvedConn is what a dialer needs: the connection's description (with
// its credential's username / auth when it uses one), the secret, the key's
// passphrase. Go-internal only (ssh / sftp / rdp bridges), never the page.
type ResolvedConn struct {
	Meta       VaultEntryMeta
	Secret     string
	Passphrase string
}

// ResolveConn opens an ssh / rdp connection's secrets, through its
// credential when it uses one (errCredMissing: that credential is gone).
func (v *Vault) ResolveConn(idHexStr string) (ResolvedConn, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return ResolvedConn{}, errVaultLocked
	}
	e, _ := v.findLocked(idHexStr)
	if e == nil {
		return ResolvedConn{}, errNotFound
	}
	if e.Kind != vaultKindSSH && e.Kind != vaultKindRDP {
		return ResolvedConn{}, errBadChars
	}
	src := v.connSourceLocked(e)
	if e.CredID != zeroVaultID && src == e {
		return ResolvedConn{}, errCredMissing
	}
	out := ResolvedConn{Meta: v.connMetaLocked(e)}
	if err := v.seal.Open(src.secret, func(p []byte) error { out.Secret = string(p); return nil }); err != nil {
		return ResolvedConn{}, err
	}
	if src.passphrase != nil && src.AuthMethod == vaultAuthKey {
		if err := v.seal.Open(src.passphrase, func(p []byte) error { out.Passphrase = string(p); return nil }); err != nil {
			return ResolvedConn{}, err
		}
	}
	v.touch()
	return out, nil
}

// RevealPassphrase: an SSH private key's passphrase ("" = none), for the
// "Reveal" action — the caller re-checks the code first, as for Reveal.
func (v *Vault) RevealPassphrase(idHexStr string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state != vaultUnlocked {
		return "", errVaultLocked
	}
	e, _ := v.findLocked(idHexStr)
	if e == nil {
		return "", errNotFound
	}
	if e.passphrase == nil {
		return "", nil
	}
	var out string
	err := v.seal.Open(e.passphrase, func(p []byte) error { out = string(p); return nil })
	v.touch()
	return out, err
}
