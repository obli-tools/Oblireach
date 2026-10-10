package main

// connimport_bridge.go — the bindings of the connection tree's import /
// export (connimport.go, connexport.go): the WebView bindings (main window
// only, the page's token checked, like the vault's management:
// vault_bridge.go). The platform side (the Open / Save dialogs:
// platformDialogs, platform.go; the WinSCP registry) is
// connimport_windows.go.
//
// Bindings:
//   reachConnImportPick(tok, source)                  → preview (sync: the Open dialog, then the file read)
//   reachConnImportPassphrase(tok, reqID, id, pass)   → preview with the secrets (async: scrypt)
//   reachConnImportCommit(tok, reqID, id, optsJSON)   → report (async: one vault save)
//   reachConnImportDrop(tok, id)                      → forgets a preview not committed
//   reachConnExport(tok, reqID, format, passphrase, code) → {saved, canceled, count, skipped} (Save dialog, then async)
//     (with the passwords: the vault's code is checked again first, as for
//     showing one secret: vault_bridge.go vaultReveal)
// Secrets never cross to the page: a preview says only whether there is one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func connReadCapped(path string, max int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() || fi.Size() > max {
		return nil, errExportNotOurs
	}
	return os.ReadFile(path)
}

func connDirIfExists(p string) string {
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p
	}
	return ""
}

// ── Bindings ───────────────────────────────────────────────────────────────

func (b *nativeBridge) bindConnImport() {
	_ = b.w.Bind("reachConnImportPick", b.connImportPick)
	_ = b.w.Bind("reachConnImportPassphrase", b.connImportPassphrase)
	_ = b.w.Bind("reachConnImportCommit", b.connImportCommit)
	_ = b.w.Bind("reachConnImportDrop", b.connImportDrop)
	_ = b.w.Bind("reachConnExport", b.connExport)
}

// connPreviewFor keeps set (and enc: an export's sealed secrets) pending and
// answers its preview, the duplicates read from the vault now.
func connPreviewFor(v *Vault, set *connImportSet, enc *connExportFile, keepID string) connPreview {
	existing := v.ConnTree()
	if keepID != "" {
		connImportPending.drop(keepID)
	}
	e := &connPendingEntry{set: set, encrypted: enc}
	id := connImportPending.put(e)
	p := connBuildPreview(id, set, existing)
	e.dupOf = map[int]string{}
	for _, it := range p.Items {
		if it.Dup != "" {
			e.dupOf[it.Ref] = it.Dup
		}
	}
	p.NeedPassphrase = enc != nil && enc.Secrets != nil
	return p
}

func (b *nativeBridge) connImportPick(tok, source string) (connPreview, error) {
	if !b.allowed(tok) {
		return connPreview{}, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return connPreview{Error: "unreadable"}, nil
	}
	if v.State().State != "unlocked" {
		return connPreview{Error: "locked"}, nil
	}
	appData := os.Getenv("APPDATA")
	var set *connImportSet
	var enc *connExportFile
	fail := func(code string) (connPreview, error) { return connPreview{Source: source, Error: code}, nil }
	switch source {
	case "winscp-registry":
		sessions, master, found := winscpRegistrySessions()
		if !found {
			return fail("winscp_not_found")
		}
		set = winscpItems(sessions, master, connReadKeyFile)
	case "winscp-file":
		path, ok := platformDialogs.pickFile(b.hwnd, false, tr("connImport.pick.winscp"), "WinSCP.ini (*.ini)\x00*.ini\x00*.*\x00*.*\x00", appData, "", "")
		if !ok {
			return fail("canceled")
		}
		data, err := connReadCapped(path, 8<<20)
		if err != nil {
			return fail("read_failed")
		}
		sessions, master := parseWinSCPIni(data)
		if len(sessions) == 0 {
			return fail("nothing_found")
		}
		set = winscpItems(sessions, master, connReadKeyFile)
	case "1remote":
		dir := connDirIfExists(filepath.Join(appData, "1Remote"))
		if dir == "" {
			dir = connDirIfExists(filepath.Join(appData, "PRemoteM"))
		}
		path, ok := platformDialogs.pickFile(b.hwnd, false, tr("connImport.pick.oneRemote"), "1Remote (*.db, *.json)\x00*.db;*.json\x00*.*\x00*.*\x00", dir, "", "")
		if !ok {
			return fail("canceled")
		}
		data, err := connReadCapped(path, connImportMaxFileSize)
		if err != nil {
			return fail("read_failed")
		}
		if strings.HasPrefix(string(data[:min(16, len(data))]), "SQLite format 3") {
			set, err = parse1RemoteDB(data, connReadKeyFile)
			if err == nil {
				if _, werr := os.Stat(path + "-wal"); werr == nil {
					set.Warnings = append(set.Warnings, "wal_present")
				}
			}
		} else {
			set, err = parse1RemoteJSON(data, connReadKeyFile)
		}
		if err != nil {
			return fail("bad_format")
		}
	case "oblireach":
		home, _ := os.UserHomeDir()
		path, ok := platformDialogs.pickFile(b.hwnd, false, tr("connImport.pick.oblireach"), "Oblireach (*.json)\x00*.json\x00*.*\x00*.*\x00", connDirIfExists(filepath.Join(home, "Documents")), "", "")
		if !ok {
			return fail("canceled")
		}
		data, err := connReadCapped(path, connImportMaxFileSize)
		if err != nil {
			return fail("read_failed")
		}
		f, err := parseConnExport(data)
		if err == errExportNewer {
			return fail("export_newer")
		}
		if err != nil {
			return fail("bad_format")
		}
		set, enc = connExportItems(f, nil), f
	default:
		return connPreview{}, errNotAllowed
	}
	set.Source = source
	return connPreviewFor(v, set, enc, ""), nil
}

func (b *nativeBridge) connImportPassphrase(tok, reqID, importID, passphrase string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		e := connImportPending.get(importID)
		if e == nil || e.encrypted == nil {
			return connPreview{Error: "expired"}, nil
		}
		secrets, err := connOpenSecrets(e.encrypted.Secrets, passphrase)
		if err != nil {
			return connPreview{ImportID: importID, NeedPassphrase: true, Error: "bad_passphrase"}, nil
		}
		set := connExportItems(e.encrypted, secrets)
		set.Source = e.set.Source
		for k := range secrets {
			secrets[k] = ""
		}
		return connPreviewFor(v, set, nil, importID), nil
	})
}

func (b *nativeBridge) connImportCommit(tok, reqID, importID, optsJSON string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		var opts connCommitOptions
		if json.Unmarshal([]byte(optsJSON), &opts) != nil {
			return nil, errBadChars
		}
		e := connImportPending.take(importID)
		if e == nil {
			return map[string]string{"error": "expired"}, nil
		}
		defer func() {
			for i := range e.set.Items {
				e.set.Items[i].Secret = ""
			}
		}()
		plan, skipped := connPlan(e.set, opts, e.dupOf)
		rep, err := v.ImportConnections(opts.ParentID, plan)
		if err != nil {
			return nil, err
		}
		rep.Skipped = len(skipped)
		rep.Lines = append(rep.Lines, skipped...)
		return rep, nil
	})
}

func (b *nativeBridge) connImportDrop(tok, importID string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	connImportPending.drop(importID)
	return true, nil
}

// connExport: format "oblireach" (passphrase "" = without the passwords) or
// "winscp" (WinSCP.ini, never a password). code: the vault's code, required
// with the passwords (every secret leaves the vault: at least as guarded as
// Reveal, which re-checks it on every call).
func (b *nativeBridge) connExport(tok, reqID, format, passphrase, code string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if format != "oblireach" && format != "winscp" {
		return false, errNotAllowed
	}
	if format == "oblireach" && passphrase != "" && len([]rune(passphrase)) < connExportMinPassphrase {
		return b.vaultAsync(tok, reqID, func() (interface{}, error) { return map[string]interface{}{"error": "passphrase_short"}, nil })
	}
	home, _ := os.UserHomeDir()
	docs := connDirIfExists(filepath.Join(home, "Documents"))
	stamp := time.Now().Format("2006-01-02")
	var path string
	var ok bool
	if format == "winscp" {
		path, ok = platformDialogs.pickFile(b.hwnd, true, tr("connImport.exportSaveTitle"), "WinSCP.ini (*.ini)\x00*.ini\x00", docs, "WinSCP-oblireach-"+stamp+".ini", "ini")
	} else {
		path, ok = platformDialogs.pickFile(b.hwnd, true, tr("connImport.exportSaveTitle"), "Oblireach (*.json)\x00*.json\x00", docs, "oblireach-connections-"+stamp+".json", "json")
	}
	if !ok {
		return b.vaultAsync(tok, reqID, func() (interface{}, error) { return map[string]interface{}{"canceled": true}, nil })
	}
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		withSecrets := format == "oblireach" && passphrase != ""
		if withSecrets {
			ok, err := v.VerifyCode(code)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errWrongCode
			}
		}
		nodes, err := v.ExportConnections(withSecrets)
		if err != nil {
			return nil, err
		}
		defer func() {
			for i := range nodes {
				nodes[i].secret = ""
			}
		}()
		var data []byte
		count, skipped := 0, 0
		if format == "winscp" {
			data, count, skipped = winscpIniExport(nodes)
		} else {
			if data, err = buildConnExport(nodes, passphrase, time.Now()); err != nil {
				return nil, err
			}
			for _, n := range nodes {
				if n.Type != "folder" {
					count++
				}
			}
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return map[string]interface{}{"error": "write_failed"}, nil
		}
		return map[string]interface{}{"saved": true, "count": count, "skipped": skipped, "file": filepath.Base(path)}, nil
	})
}
