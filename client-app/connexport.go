package main

// connexport.go — Oblireach's own export of the local connection tree, and
// its import (the same importer as the other sources: connimport.go).
//
// File (JSON, UTF-8), formatVersion 1:
//   {
//     "format": "oblireach-connections",
//     "formatVersion": 1,
//     "exportedAt": "2026-10-02T12:00:00Z",
//     "nodes": [
//       {"id": "<hex>", "parent": "<hex or empty>", "type": "folder", "name": "Lab"},
//       {"id": "<hex>", "parent": "<hex>", "type": "ssh", "name": "router",
//        "host": "192.168.1.1", "port": 22, "username": "root", "auth": "password"},
//       {"id": "<hex>", "parent": "", "type": "rdp", "name": "dc01", "host": "...", "port": 3389, "username": "...", "auth": "password"}
//     ],
//     "secrets": null
//   }
// Without passwords by default ("secrets": null). With them, "secrets" is
// one encrypted block holding {"<node id>": "<secret>", ...}:
//   {"kdf": "scrypt", "n": 32768, "r": 8, "p": 1, "salt": "<b64 16 bytes>",
//    "cipher": "aes-256-gcm", "nonce": "<b64 12 bytes>", "data": "<b64>"}
// key = scrypt(passphrase, salt, N, r, p, 32); AES-256-GCM, additional data
// "oblireach-connections/1" (the block cannot be moved to another format
// version unnoticed). A wrong passphrase fails the GCM check: nothing is read.
//
// A newer formatVersion is refused (export_newer), never half read; an older
// one would be migrated here when one exists.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	connExportFormat        = "oblireach-connections"
	connExportFormatVersion = 1
	connExportAAD           = "oblireach-connections/1"
	connExportMinPassphrase = 8
)

// The scrypt cost (2^15, r 8, p 1: ~32 MB, a fraction of a second); the
// tests lower it.
var connExportScryptN = 1 << 15

// connExportNode: one folder / connection of the file.
type connExportNode struct {
	ID       string `json:"id"`
	Parent   string `json:"parent"`
	Type     string `json:"type"` // folder, ssh, rdp
	Name     string `json:"name"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Auth     string `json:"auth,omitempty"` // password, key
	secret   string // (never marshalled: goes to the encrypted block)
}

type connExportSecrets struct {
	KDF    string `json:"kdf"`
	N      int    `json:"n"`
	R      int    `json:"r"`
	P      int    `json:"p"`
	Salt   string `json:"salt"`
	Cipher string `json:"cipher"`
	Nonce  string `json:"nonce"`
	Data   string `json:"data"`
}

type connExportFile struct {
	Format        string             `json:"format"`
	FormatVersion int                `json:"formatVersion"`
	ExportedAt    string             `json:"exportedAt"`
	Nodes         []connExportNode   `json:"nodes"`
	Secrets       *connExportSecrets `json:"secrets"`
}

var (
	errExportPassphrase = errors.New("export: passphrase too short")
	errExportBadPass    = errors.New("export: wrong passphrase or damaged file")
	errExportNotOurs    = errors.New("export: not an Oblireach connections file")
	errExportNewer      = errors.New("export: made by a newer version")
)

// buildConnExport: the file's bytes. passphrase "" = no secrets (they are
// dropped); otherwise every node's secret is encrypted with it.
func buildConnExport(nodes []connExportNode, passphrase string, now time.Time) ([]byte, error) {
	f := connExportFile{Format: connExportFormat, FormatVersion: connExportFormatVersion,
		ExportedAt: now.UTC().Format(time.RFC3339), Nodes: nodes}
	if f.Nodes == nil {
		f.Nodes = []connExportNode{}
	}
	if passphrase != "" {
		if len([]rune(passphrase)) < connExportMinPassphrase {
			return nil, errExportPassphrase
		}
		secrets := map[string]string{}
		for _, n := range nodes {
			if n.secret != "" {
				secrets[n.ID] = n.secret
			}
		}
		plain, err := json.Marshal(secrets)
		if err != nil {
			return nil, err
		}
		defer wipeBytesLocal(plain)
		block, err := connSeal(plain, passphrase)
		if err != nil {
			return nil, err
		}
		f.Secrets = block
	}
	return json.MarshalIndent(f, "", "  ")
}

func connDeriveKey(passphrase string, salt []byte, n, r, p int) ([]byte, error) {
	return scrypt.Key([]byte(passphrase), salt, n, r, p, 32)
}

func connSeal(plain []byte, passphrase string) (*connExportSecrets, error) {
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	s := &connExportSecrets{KDF: "scrypt", N: connExportScryptN, R: 8, P: 1, Cipher: "aes-256-gcm",
		Salt: base64.StdEncoding.EncodeToString(salt), Nonce: base64.StdEncoding.EncodeToString(nonce)}
	key, err := connDeriveKey(passphrase, salt, s.N, s.R, s.P)
	if err != nil {
		return nil, err
	}
	defer wipeBytesLocal(key)
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	s.Data = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, []byte(connExportAAD)))
	return s, nil
}

// connOpenSecrets: the {id: secret} map of the block, or errExportBadPass.
func connOpenSecrets(s *connExportSecrets, passphrase string) (map[string]string, error) {
	if s == nil {
		return map[string]string{}, nil
	}
	if s.KDF != "scrypt" || s.Cipher != "aes-256-gcm" || s.R < 1 || s.P < 1 || s.N < 2 || s.N > 1<<20 || s.N&(s.N-1) != 0 || s.R*s.P > 64 {
		return nil, errExportBadPass
	}
	salt, err1 := base64.StdEncoding.DecodeString(s.Salt)
	nonce, err2 := base64.StdEncoding.DecodeString(s.Nonce)
	data, err3 := base64.StdEncoding.DecodeString(s.Data)
	if err1 != nil || err2 != nil || err3 != nil || len(nonce) != 12 || len(salt) < 8 {
		return nil, errExportBadPass
	}
	key, err := connDeriveKey(passphrase, salt, s.N, s.R, s.P)
	if err != nil {
		return nil, errExportBadPass
	}
	defer wipeBytesLocal(key)
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, errExportBadPass
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, errExportBadPass
	}
	plain, err := gcm.Open(nil, nonce, data, []byte(connExportAAD))
	if err != nil {
		return nil, errExportBadPass
	}
	defer wipeBytesLocal(plain)
	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, errExportBadPass
	}
	return out, nil
}

// parseConnExport reads an Oblireach export (its secrets still sealed).
func parseConnExport(data []byte) (*connExportFile, error) {
	var f connExportFile
	if err := json.Unmarshal(data, &f); err != nil || f.Format != connExportFormat {
		return nil, errExportNotOurs
	}
	if f.FormatVersion > connExportFormatVersion {
		return nil, errExportNewer
	}
	if f.FormatVersion < 1 {
		return nil, errExportNotOurs
	}
	return &f, nil
}

// connExportItems: the file's connections as import items (folders from
// the parent chain), each with its secret from secrets (nil: none).
func connExportItems(f *connExportFile, secrets map[string]string) *connImportSet {
	set := &connImportSet{Source: "oblireach"}
	byID := map[string]connExportNode{}
	for _, n := range f.Nodes {
		if n.Type == "folder" {
			byID[n.ID] = n
		}
	}
	for _, n := range f.Nodes {
		if n.Type == "folder" {
			continue
		}
		if len(set.Items) >= connImportMaxItems {
			set.Warnings = append(set.Warnings, "truncated")
			break
		}
		var path []string
		seen := map[string]bool{}
		for p := n.Parent; p != "" && !seen[p] && len(path) < 64; {
			seen[p] = true
			fo, ok := byID[p]
			if !ok {
				break
			}
			path = append([]string{fo.Name}, path...)
			p = fo.Parent
		}
		it := connImportItem{Folder: path, Name: n.Name, Proto: strings.ToUpper(n.Type), Host: n.Host, Username: n.Username}
		switch n.Type {
		case "ssh":
			it.Kind = vaultKindSSH
			it.KeyAuth = n.Auth == "key"
		case "rdp":
			it.Kind = vaultKindRDP
		}
		if n.Port > 0 && n.Port <= 65535 {
			it.Port = uint16(n.Port)
		}
		if secrets != nil {
			it.Secret = secrets[n.ID]
		}
		connCheckItem(&it)
		set.Items = append(set.Items, it)
	}
	return set
}
