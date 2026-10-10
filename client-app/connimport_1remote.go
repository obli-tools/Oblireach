package main

// connimport_1remote.go — 1Remote (formerly PRemoteM) connections.
//
// Format (1Remote's source, github.com/1Remote/1Remote, Ui/Service/DataSource/
// DAO/Dapper/TableServer.cs, DapperDataBase.cs, Ui/Model/Protocol/*):
//   - a SQLite database (default %AppData%\1Remote\1Remote.db, or next to the
//     program in portable mode), table "Servers" (Id, Protocol, ClassVersion,
//     Json): Json is the connection object serialized by Newtonsoft.Json;
//   - that object: Protocol ("SSH", "RDP", "SFTP", "VNC", "Telnet", "FTP",
//     "SCP", "APP"...), DisplayName, Address, Port (a STRING), UserName,
//     Password, PrivateKey (a key FILE path), UsePrivateKeyForConnect,
//     Tags (older versions: the grouping), TreeNodes (1Remote 2.x: the
//     folder path, root first);
//   - Password / PrivateKey are encrypted by 1Remote's own closed library
//     (_1Remote.Security.SimpleStringEncipher, a salt set at start-up) — not
//     readable here: passwords are never imported from 1Remote (reported:
//     encrypted_source). A private key PATH is read when it is a readable
//     OpenSSH/PEM key file in clear text (an encrypted value is no path).
// A JSON file is accepted too (assumption: an array of those same objects,
// or an object holding one under "Servers" / "servers" / "items"), e.g.
// rows copied out of the database by the operator.
//
// SFTP / SCP become SSH connections (the tree's SSH entries have the files
// explorer); VNC, Telnet, FTP, APP... are listed as unsupported.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// oneRemoteServer: the fields of a 1Remote connection object we use.
type oneRemoteServer struct {
	Protocol                string          `json:"Protocol"`
	DisplayName             string          `json:"DisplayName"`
	Address                 string          `json:"Address"`
	Port                    json.RawMessage `json:"Port"` // a string ("22"); a number accepted too
	UserName                string          `json:"UserName"`
	Password                string          `json:"Password"`
	PrivateKey              string          `json:"PrivateKey"`
	UsePrivateKeyForConnect *bool           `json:"UsePrivateKeyForConnect"`
	Tags                    []string        `json:"Tags"`
	TreeNodes               []string        `json:"TreeNodes"`
}

// parse1RemoteDB reads the "Servers" table of a 1Remote database.
func parse1RemoteDB(data []byte, readKey func(string) (string, string)) (*connImportSet, error) {
	db, err := openSQLite(data)
	if err != nil {
		return nil, err
	}
	root, cols, err := db.sqliteTable("Servers")
	if err != nil {
		return nil, err
	}
	ji, pi := -1, -1
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "json":
			ji = i
		case "protocol":
			pi = i
		}
	}
	if ji < 0 {
		return nil, errSQLiteFormat
	}
	var servers []oneRemoteServer
	err = db.tableRows(root, func(row []interface{}) bool {
		if ji >= len(row) {
			return true
		}
		js, _ := row[ji].(string)
		if b, ok := row[ji].([]byte); ok {
			js = string(b)
		}
		var s oneRemoteServer
		if json.Unmarshal([]byte(js), &s) != nil {
			return true
		}
		if s.Protocol == "" && pi >= 0 && pi < len(row) {
			s.Protocol, _ = row[pi].(string)
		}
		servers = append(servers, s)
		return len(servers) <= connImportMaxItems
	})
	if err != nil {
		return nil, err
	}
	return oneRemoteItems(servers, readKey), nil
}

// parse1RemoteJSON: the JSON form (see the file header).
func parse1RemoteJSON(data []byte, readKey func(string) (string, string)) (*connImportSet, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var servers []oneRemoteServer
	if err := json.Unmarshal(data, &servers); err != nil {
		var wrap map[string]json.RawMessage
		if err2 := json.Unmarshal(data, &wrap); err2 != nil {
			return nil, err
		}
		found := false
		for _, k := range []string{"Servers", "servers", "items", "Items"} {
			if raw, ok := wrap[k]; ok && json.Unmarshal(raw, &servers) == nil {
				found = true
				break
			}
		}
		if !found {
			var one oneRemoteServer
			if json.Unmarshal(data, &one) != nil || one.Protocol == "" {
				return nil, err
			}
			servers = []oneRemoteServer{one}
		}
	}
	return oneRemoteItems(servers, readKey), nil
}

func oneRemoteItems(servers []oneRemoteServer, readKey func(string) (string, string)) *connImportSet {
	set := &connImportSet{Source: "1remote"}
	for _, s := range servers {
		if len(set.Items) >= connImportMaxItems {
			set.Warnings = append(set.Warnings, "truncated")
			break
		}
		it := connImportItem{Name: s.DisplayName, Proto: s.Protocol, Host: s.Address, Username: s.UserName}
		it.Folder = append([]string{}, s.TreeNodes...)
		if len(it.Folder) == 0 && len(s.Tags) > 0 {
			it.Folder = []string{s.Tags[0]} // (older 1Remote: its first tag is the group)
		}
		defPort := 0
		switch strings.ToUpper(strings.TrimSpace(s.Protocol)) {
		case "SSH", "SFTP", "SCP":
			it.Kind, defPort = vaultKindSSH, 22
		case "RDP":
			it.Kind, defPort = vaultKindRDP, 3389
		}
		port := defPort
		if len(s.Port) > 0 {
			var ps string
			var pn float64
			if json.Unmarshal(s.Port, &ps) == nil {
				if n, err := strconv.Atoi(strings.TrimSpace(ps)); err == nil {
					port = n
				} else if strings.TrimSpace(ps) != "" {
					port = -1
				}
			} else if json.Unmarshal(s.Port, &pn) == nil {
				port = int(pn)
			}
		}
		if port > 0 && port <= 65535 {
			it.Port = uint16(port)
		}
		if it.Kind == vaultKindSSH && s.UsePrivateKeyForConnect != nil && *s.UsePrivateKeyForConnect {
			it.KeyAuth = true
			kp := strings.TrimSpace(s.PrivateKey)
			if kp != "" && readKey != nil && (strings.Contains(kp, `\`) || strings.Contains(kp, "/")) {
				it.Secret, it.SecretNote = readKey(kp)
			} else {
				it.SecretNote = connNoteEncrypted
			}
		} else if it.Kind != 0 && strings.TrimSpace(s.Password) != "" {
			it.SecretNote = connNoteEncrypted
		}
		connCheckItem(&it)
		set.Items = append(set.Items, it)
	}
	connSortItems(set.Items)
	return set
}
