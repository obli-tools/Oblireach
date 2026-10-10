package main

// connimport_test.go — the connection tree's importers and export
// (connimport*.go, connexport.go, sqlite_read.go, vault_conn.go).

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"
)

// A 1Remote database made by SQLite itself (Python's sqlite3, page size
// 512 so that the Servers table has interior pages and one row overflows):
// the schema of 1Remote's DapperDataBase.cs, 43 rows (SSH with a 900-byte
// note, RDP with a tag, VNC, 40 SFTP in TreeNodes ["Bulk"]). gzip + base64.
const oneRemoteFixtureDB = "" +
	"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" +
	"S4eFL728RCR5iajzEpHnpX8AoenUmwAsAAA="

func oneRemoteFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(oneRemoteFixtureDB)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ── WinSCP ──────────────────────────────────────────────────────────────────

// A password as WinSCP stores it, made by an independent implementation of
// WinSCP's EncryptPassword (Security.cpp: flag, version, length, a shift of
// 5 random bytes, key+password, random padding to 50 bytes).
const winscpVector = "A35C4B594D221E4D222E3333282E332928392E72303D320F6F3F2E6F287D9FF5224D1E224D1E224D1E224D1E224D1E224D1E"

func TestWinSCPPasswordKnownVector(t *testing.T) {
	pw, ext, ok := winscpDecryptPassword(winscpVector, "root"+"router.lan")
	if !ok || ext || pw != "S3cr3t!é" {
		t.Fatalf("decrypt = %q ext=%v ok=%v", pw, ext, ok)
	}
	// The wrong key (another user / host): refused, never a wrong password.
	if pw, _, ok := winscpDecryptPassword(winscpVector, "admin"+"router.lan"); ok || pw != "" {
		t.Fatalf("wrong key decoded %q", pw)
	}
	// Round trip of the encoder used by the other tests.
	if pw, _, ok := winscpDecryptPassword(winscpEncryptPassword("p@ss w0rd", "uh"), "uh"); !ok || pw != "p@ss w0rd" {
		t.Fatalf("round trip = %q %v", pw, ok)
	}
	// A master-password ("external") value: flag 0xFF then version 0x01.
	if _, ext, ok := winscpDecryptPassword("A35D"+"41424344", "k"); ok || !ext {
		t.Fatalf("external: ext=%v ok=%v", ext, ok)
	}
	for _, junk := range []string{"", "ZZ", "A3", "A35C"} {
		if _, _, ok := winscpDecryptPassword(junk, "k"); ok {
			t.Fatalf("junk %q decoded", junk)
		}
	}
}

func TestWinSCPUnmunge(t *testing.T) {
	cases := map[string]string{
		"My%20Folder/srv":                      "My Folder/srv",
		"%EF%BB%BFS%C3%A9rver%20%C3%A9t%C3%A9": "Sérver été",
		"caf%E9":                               "café", // (ANSI: Windows-1252)
		"100%25":                               "100%",
		"plain":                                "plain",
	}
	for in, want := range cases {
		if got := winscpUnmunge(in); got != want {
			t.Errorf("unmunge(%q) = %q, want %q", in, got, want)
		}
	}
	if got := winscpUnmunge(winscpMunge("Dossier été/a*b?", false)); got != "Dossier été/a*b?" {
		t.Errorf("munge round trip = %q", got)
	}
}

func TestWinSCPIniParse(t *testing.T) {
	ini := "\xEF\xBB\xBF[Configuration\\Interface]\r\nFoo=1\r\n" +
		"[Sessions\\Default%20Settings]\r\nHostName=ignored\r\n" +
		"[Sessions\\Prod%20Lab/Web/www1]\r\nHostName=10.0.0.1\r\nUserName=deploy\r\nPortNumber=2222\r\nFSProtocol=2\r\n" +
		"Password=" + winscpEncryptPassword("hunter2", "deploy10.0.0.1") + "\r\n" +
		"[Sessions\\%EF%BB%BFR%C3%A9seau/routeur]\r\nHostName=192.168.1.1\r\nUserName=root\r\n" +
		"[Sessions\\ftp-site]\r\nHostName=ftp.example.com\r\nFSProtocol=5\r\n" +
		"[Sessions\\scp-old]\r\nHostName=old.example.com\r\nFSProtocol=0\r\nPortNumber=22\r\nPassword=GARBAGE\r\n" +
		"[Sessions\\nohost]\r\nUserName=x\r\n"
	sessions, master := parseWinSCPIni([]byte(ini))
	if master {
		t.Fatal("no master password here")
	}
	set := winscpItems(sessions, master, nil)
	byName := map[string]connImportItem{}
	for _, it := range set.Items {
		byName[it.Name] = it
	}
	if _, ok := byName["Default Settings"]; ok {
		t.Fatal("Default Settings imported")
	}
	w := byName["www1"]
	if strings.Join(w.Folder, "/") != "Prod Lab/Web" || w.Host != "10.0.0.1" || w.Port != 2222 || w.Username != "deploy" ||
		w.Kind != vaultKindSSH || w.Secret != "hunter2" || w.Skip != "" || w.Proto != "SFTP" {
		t.Fatalf("www1 = %+v", w)
	}
	r := byName["routeur"]
	if strings.Join(r.Folder, "/") != "Réseau" || r.Port != 22 || r.Proto != "SFTP" || r.Secret != "" || r.SecretNote != "" {
		t.Fatalf("routeur = %+v (no FSProtocol: SFTP on 22)", r)
	}
	if f := byName["ftp-site"]; f.Skip != connSkipUnsupported || f.Proto != "FTP" {
		t.Fatalf("ftp = %+v", f)
	}
	if s := byName["scp-old"]; s.Kind != vaultKindSSH || s.Proto != "SCP" || s.SecretNote != connNoteUndecodable {
		t.Fatalf("scp = %+v", s)
	}
	if n := byName["nohost"]; n.Skip != connSkipNoHost {
		t.Fatalf("nohost = %+v", n)
	}

	// A master password: the passwords are not imported, the sessions are.
	ini2 := "[Configuration\\Security]\r\nUseMasterPassword=1\r\n[Sessions\\a]\r\nHostName=h\r\nUserName=u\r\nPassword=" +
		winscpEncryptPassword("x", "uh") + "\r\n"
	sessions, master = parseWinSCPIni([]byte(ini2))
	set = winscpItems(sessions, master, nil)
	if !master || len(set.Items) != 1 || set.Items[0].Secret != "" || set.Items[0].SecretNote != connNoteMaster || len(set.Warnings) == 0 {
		t.Fatalf("master: %v %+v", master, set)
	}
}

func TestWinSCPIniExport(t *testing.T) {
	nodes := []connExportNode{
		{ID: "f1", Type: "folder", Name: "Lab été"},
		{ID: "c1", Parent: "f1", Type: "ssh", Name: "box 1", Host: "10.0.0.1", Port: 22, Username: "root", secret: "never"},
		{ID: "c2", Type: "rdp", Name: "dc", Host: "10.0.0.2", Port: 3389},
	}
	ini, n, skipped := winscpIniExport(nodes)
	if n != 1 || skipped != 1 || bytes.Contains(ini, []byte("never")) || bytes.Contains(ini, []byte("Password")) {
		t.Fatalf("export: n=%d skipped=%d\n%s", n, skipped, ini)
	}
	sessions, _ := parseWinSCPIni(ini)
	set := winscpItems(sessions, false, nil)
	if len(set.Items) != 1 || strings.Join(set.Items[0].Folder, "/") != "Lab été" || set.Items[0].Name != "box 1" || set.Items[0].Username != "root" {
		t.Fatalf("re-read = %+v", set.Items)
	}
}

// ── SQLite / 1Remote ────────────────────────────────────────────────────────

func TestSQLiteReaderAnd1RemoteDB(t *testing.T) {
	data := oneRemoteFixture(t)
	db, err := openSQLite(data)
	if err != nil {
		t.Fatal(err)
	}
	root, cols, err := db.sqliteTable("servers")
	if err != nil || strings.Join(cols, ",") != "Id,Protocol,ClassVersion,Json" {
		t.Fatalf("table: root=%d cols=%v err=%v", root, cols, err)
	}
	n := 0
	if err := db.tableRows(root, func([]interface{}) bool { n++; return true }); err != nil || n != 43 {
		t.Fatalf("rows = %d, %v", n, err)
	}
	set, err := parse1RemoteDB(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]connImportItem{}
	for _, it := range set.Items {
		by[it.Name] = it
	}
	a := by["AIRRAID"]
	if a.Kind != vaultKindSSH || a.Host != "192.168.1.1" || a.Port != 22 || a.Username != "root" || strings.Join(a.Folder, "/") != "Home/Network" ||
		a.Secret != "" || a.SecretNote != connNoteEncrypted {
		t.Fatalf("AIRRAID = %+v", a)
	}
	if d := by["dc01"]; d.Kind != vaultKindRDP || d.Port != 3389 || strings.Join(d.Folder, "/") != "Servers" {
		t.Fatalf("dc01 = %+v", d)
	}
	if v := by["kiosk"]; v.Skip != connSkipUnsupported || v.Proto != "VNC" {
		t.Fatalf("kiosk = %+v", v)
	}
	if f := by["files07"]; f.Kind != vaultKindSSH || f.Port != 2222 || f.Proto != "SFTP" {
		t.Fatalf("files07 = %+v", f)
	}
	// Corrupt / truncated data: an error, never a panic.
	for _, cut := range []int{0, 50, 100, 600, 3000, len(data) - 300} {
		_, _ = parse1RemoteDB(data[:cut], nil)
	}
	bad := append([]byte(nil), data...)
	for i := 512; i < len(bad); i += 97 {
		bad[i] ^= 0x5A
	}
	_, _ = parse1RemoteDB(bad, nil)
}

func Test1RemoteJSON(t *testing.T) {
	js := `[{"Protocol":"SSH","DisplayName":"a","Address":"h1","Port":"2200","UserName":"u","UsePrivateKeyForConnect":true,"PrivateKey":"ENCRYPTED"},
	        {"Protocol":"Telnet","DisplayName":"t","Address":"h2","Port":"23"},
	        {"Protocol":"RDP","DisplayName":"r","Address":"h3","Port":3390}]`
	set, err := parse1RemoteJSON([]byte(js), nil)
	if err != nil || len(set.Items) != 3 {
		t.Fatalf("%v %+v", err, set)
	}
	by := map[string]connImportItem{}
	for _, it := range set.Items {
		by[it.Name] = it
	}
	if a := by["a"]; a.Port != 2200 || !a.KeyAuth || a.SecretNote != connNoteEncrypted {
		t.Fatalf("a = %+v", a)
	}
	if r := by["r"]; r.Port != 3390 || r.Kind != vaultKindRDP {
		t.Fatalf("r = %+v", r)
	}
	if tl := by["t"]; tl.Skip != connSkipUnsupported {
		t.Fatalf("telnet = %+v", tl)
	}
	if _, err := parse1RemoteJSON([]byte(`{"Servers":[{"Protocol":"SSH","Address":"x"}]}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := parse1RemoteJSON([]byte(`not json`), nil); err == nil {
		t.Fatal("junk accepted")
	}
}

// ── Oblireach export ────────────────────────────────────────────────────────

func TestConnExportEncryptedRoundTrip(t *testing.T) {
	old := connExportScryptN
	connExportScryptN = 1 << 10
	defer func() { connExportScryptN = old }()
	nodes := []connExportNode{
		{ID: "f1", Type: "folder", Name: "Lab"},
		{ID: "f2", Parent: "f1", Type: "folder", Name: "Sub"},
		{ID: "c1", Parent: "f2", Type: "ssh", Name: "box", Host: "10.0.0.1", Port: 22, Username: "root", Auth: "password", secret: "hunter2"},
		{ID: "c2", Type: "ssh", Name: "keyed", Host: "10.0.0.2", Port: 22, Auth: "key", secret: "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n"},
		{ID: "c3", Type: "rdp", Name: "dc", Host: "10.0.0.3", Port: 3389, Username: "admin", Auth: "password", secret: "Pa$$"},
	}
	if _, err := buildConnExport(nodes, "short", time.Now()); err != errExportPassphrase {
		t.Fatalf("short passphrase: %v", err)
	}
	data, err := buildConnExport(nodes, "correct horse battery", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"hunter2", "Pa$$", "OPENSSH"} {
		if bytes.Contains(data, []byte(s)) {
			t.Fatalf("secret %q in clear in the file", s)
		}
	}
	f, err := parseConnExport(data)
	if err != nil || f.Secrets == nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := connOpenSecrets(f.Secrets, "wrong passphrase!"); err != errExportBadPass {
		t.Fatalf("wrong passphrase: %v", err)
	}
	sec, err := connOpenSecrets(f.Secrets, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	set := connExportItems(f, sec)
	if len(set.Items) != 3 {
		t.Fatalf("items = %+v", set.Items)
	}
	by := map[string]connImportItem{}
	for _, it := range set.Items {
		by[it.Name] = it
	}
	if b := by["box"]; strings.Join(b.Folder, "/") != "Lab/Sub" || b.Secret != "hunter2" || b.Kind != vaultKindSSH {
		t.Fatalf("box = %+v", b)
	}
	if k := by["keyed"]; !k.KeyAuth || !strings.HasPrefix(k.Secret, "-----BEGIN") {
		t.Fatalf("keyed = %+v", k)
	}
	// Without a passphrase: no secret at all in the file.
	plain, _ := buildConnExport(nodes, "", time.Now())
	if bytes.Contains(plain, []byte("hunter2")) || !bytes.Contains(plain, []byte(`"secrets": null`)) || !bytes.Contains(plain, []byte(`"formatVersion": 1`)) {
		t.Fatalf("plain export:\n%s", plain)
	}
	// A newer format: refused.
	if _, err := parseConnExport([]byte(`{"format":"oblireach-connections","formatVersion":99,"nodes":[]}`)); err != errExportNewer {
		t.Fatalf("newer: %v", err)
	}
	// Tampered ciphertext: the GCM check fails.
	f.Secrets.Data = base64.StdEncoding.EncodeToString([]byte("tampered ciphertext"))
	if _, err := connOpenSecrets(f.Secrets, "correct horse battery"); err != errExportBadPass {
		t.Fatalf("tampered: %v", err)
	}
}

// ── Duplicates, plan, the vault write ───────────────────────────────────────

func TestConnImportDuplicatesIntoVault(t *testing.T) {
	v, _ := newTestVault()
	if err := v.Create("secret1", 15); err != nil {
		t.Fatal(err)
	}
	labID, _ := v.Save(SaveInput{Label: "Lab", Kind: vaultKindFolder})
	oldID, err := v.Save(SaveInput{Label: "old box", Kind: vaultKindSSH, ParentID: labID, Host: "10.0.0.1", Port: 22, Username: "root", Secret: "oldpw"})
	if err != nil {
		t.Fatal(err)
	}
	set := &connImportSet{Items: []connImportItem{
		{Name: "box A", Kind: vaultKindSSH, Host: "10.0.0.1", Port: 22, Username: "root", Secret: "newpw", Folder: []string{"lab"}}, // dup of old box
		{Name: "box B", Kind: vaultKindSSH, Host: "10.0.0.1", Port: 22, Username: "root", Folder: []string{"Lab", "Deep"}},          // dup too
		{Name: "box C", Kind: vaultKindSSH, Host: "10.0.0.1", Port: 22, Username: "root"},                                           // dup, ignored
		{Name: "rdp", Kind: vaultKindRDP, Host: "DC01", Port: 3389, Username: "admin", Secret: "rdppw", Folder: []string{"Lab"}},
		{Name: "vnc", Skip: connSkipUnsupported},
		{Name: "unticked", Kind: vaultKindSSH, Host: "z", Port: 22},
	}}
	p := connBuildPreview("x", set, v.ConnTree())
	dupOf := map[int]string{}
	for _, it := range p.Items {
		if it.Dup != "" {
			dupOf[it.Ref] = it.Dup
		}
		if it.Ref < 3 && (it.Dup != oldID || it.DupName != "old box") {
			t.Fatalf("item %d dup = %q %q", it.Ref, it.Dup, it.DupName)
		}
		if it.Ref == 0 && !it.HasPassword {
			t.Fatal("preview: has password")
		}
	}
	// The host's case does not matter for a duplicate.
	if p2 := connBuildPreview("y", &connImportSet{Items: []connImportItem{{Name: "u", Kind: vaultKindSSH, Host: "10.0.0.1", Port: 22, Username: "root"}}}, v.ConnTree()); p2.Items[0].Dup != oldID {
		t.Fatal("dup not found")
	}
	opts := connCommitOptions{ImportSecrets: true, Decisions: []connDecision{
		{Ref: 0, Action: "replace"}, {Ref: 1, Action: "duplicate"}, {Ref: 2, Action: "skip"}, {Ref: 3, Action: "import"},
		{Ref: 4, Action: "import"}, {Ref: 99, Action: "import"},
	}}
	plan, skipped := connPlan(set, opts, dupOf)
	if len(plan) != 3 || len(skipped) != 3 {
		t.Fatalf("plan %d skipped %d", len(plan), len(skipped))
	}
	reasons := map[string]string{}
	for _, l := range skipped {
		reasons[l.Name] = l.Reason
	}
	if reasons["box C"] != connReasonDupIgnored || reasons["vnc"] != connSkipUnsupported || reasons["unticked"] != connReasonNotSelected {
		t.Fatalf("reasons = %v", reasons)
	}
	rep, err := v.ImportConnections("", plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Replaced != 1 || rep.Imported != 2 || rep.Folders != 1 || rep.Failed != 0 {
		t.Fatalf("report = %+v", rep)
	}
	// Replaced in place: same id, same folder, the new name and password.
	m, err := v.Get(oldID)
	if err != nil || m.Label != "box A" || m.ParentID != labID {
		t.Fatalf("replaced = %+v %v", m, err)
	}
	if s, _ := v.Reveal(oldID); s != "newpw" {
		t.Fatalf("replaced secret = %q", s)
	}
	// "Lab" reused (the case does not count), "Deep" created below it; box B without a password.
	var deep VaultEntryMeta
	for _, e := range v.ByParent(labID) {
		if e.Kind == vaultKindFolder && e.Label == "Deep" {
			deep = e
		}
	}
	if deep.ID == "" {
		t.Fatal("Deep folder not created under Lab")
	}
	kids := v.ByParent(deep.ID)
	if len(kids) != 1 || kids[0].Label != "box B" {
		t.Fatalf("Deep = %+v", kids)
	}
	if s, err := v.Reveal(kids[0].ID); err != nil || s != "" {
		t.Fatalf("box B secret = %q %v", s, err)
	}
	// Without "import the passwords": none stored.
	set2 := &connImportSet{Items: []connImportItem{{Name: "n", Kind: vaultKindSSH, Host: "q", Port: 22, Secret: "dropme"}}}
	plan2, _ := connPlan(set2, connCommitOptions{ImportSecrets: false, Decisions: []connDecision{{Ref: 0, Action: "import"}}}, nil)
	if len(plan2) != 1 || plan2[0].Item.Secret != "" {
		t.Fatalf("secrets kept: %+v", plan2)
	}
	// The vault locked: nothing written.
	v.Lock()
	if _, err := v.ImportConnections("", plan2); err == nil {
		t.Fatal("import into a locked vault")
	}
}

func TestConnPendingStore(t *testing.T) {
	now := time.Unix(1000, 0)
	s := &connPendingStore{m: map[string]*connPendingEntry{}, now: func() time.Time { return now }}
	set := &connImportSet{Items: []connImportItem{{Secret: "s"}}}
	id := s.put(&connPendingEntry{set: set})
	if s.get(id) == nil {
		t.Fatal("lost")
	}
	now = now.Add(connImportPendingTTL + time.Second)
	if s.get(id) != nil || set.Items[0].Secret != "" {
		t.Fatal("expired entry kept or its secret not cleared")
	}
	var ids []string
	for i := 0; i < connImportPendingMax+2; i++ {
		now = now.Add(time.Second)
		ids = append(ids, s.put(&connPendingEntry{set: &connImportSet{}}))
	}
	if len(s.m) != connImportPendingMax || s.get(ids[0]) != nil || s.get(ids[len(ids)-1]) == nil {
		t.Fatalf("cap: %d", len(s.m))
	}
	if s.take(ids[len(ids)-1]) == nil || s.get(ids[len(ids)-1]) != nil {
		t.Fatal("take")
	}
}

func TestConnCheckItemCleans(t *testing.T) {
	it := connImportItem{Name: "a\tb" + strings.Repeat("x", 200), Kind: vaultKindSSH, Host: " h ", Port: 22, Folder: []string{"", "f", " "}, Secret: "bad\npw"}
	connCheckItem(&it)
	if len([]rune(it.Name)) > vaultMaxLabelRunes || strings.ContainsAny(it.Name, "\t") || it.Host != "h" || len(it.Folder) != 1 ||
		it.Secret != "" || it.SecretNote != connNoteTooLong {
		t.Fatalf("%+v", it)
	}
	in := SaveInput{Label: it.Name, Kind: it.Kind, Host: it.Host, Port: it.Port}
	if err := validateSaveInput(&in); err != nil {
		t.Fatalf("cleaned item still invalid: %v", err)
	}
}
