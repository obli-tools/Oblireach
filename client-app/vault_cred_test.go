package main

// vault_cred_test.go — the vault's places (one tenant, a whole server,
// every server: the vault page's sections), the local base's reusable
// credentials (vault_conn.go, codec v4) and the decoding ceilings that let
// every field Save accepts be read back.

import (
	"errors"
	"strings"
	"testing"
)

// innerVersion: the inner format version of what the fake store holds.
func innerVersion(t *testing.T, s *fakeStore) byte {
	t.Helper()
	if len(s.data) < 15 {
		t.Fatalf("no vault file")
	}
	return s.data[14] ^ 0x5a // outer header (10) + "FAKE" (4), then the xor'd inner bytes
}

func unlockedVault(t *testing.T) (*Vault, *fakeStore) {
	t.Helper()
	v, s := newTestVault()
	if err := v.Create("secret1", 15); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return v, s
}

func relock(t *testing.T, v *Vault) {
	t.Helper()
	v.Lock()
	if err := v.Unlock("secret1"); err != nil {
		t.Fatalf("Unlock after a save: %v (state %s) — something Save accepted is not read back", err, v.State().State)
	}
}

// Regression: an SSH private key past 512 bytes, or a 500-rune note in a
// non-Latin script, made the whole vault unreadable at its next unlock.
func TestVaultEverySavedFieldIsReadBack(t *testing.T) {
	v, _ := unlockedVault(t)
	pem := fakePEMPrivateKey(60)
	id, err := v.Save(SaveInput{Label: "k", Kind: vaultKindSSH, Host: "h", Port: 22, Username: "root", AuthMethod: vaultAuthKey, Secret: pem})
	if err != nil {
		t.Fatalf("Save key: %v", err)
	}
	note := strings.Repeat("密", vaultMaxNoteRunes)
	if _, err := v.Save(SaveInput{Origin: "https://a", TenantID: 1, Label: "n", Secret: "x", Note: note}); err != nil {
		t.Fatalf("Save note: %v", err)
	}
	relock(t, v)
	if got, _ := v.Reveal(id); got != pem {
		t.Fatalf("the key does not round-trip through the file")
	}
}

func TestVaultPlaces(t *testing.T) {
	v, _ := unlockedVault(t)
	save := func(in SaveInput) string {
		t.Helper()
		in.Secret = "pw"
		id, err := v.Save(in)
		if err != nil {
			t.Fatalf("Save %q: %v", in.Label, err)
		}
		return id
	}
	save(SaveInput{Label: "tenant", Origin: "https://a", TenantID: 1, TenantName: "Acme"})
	save(SaveInput{Label: "server", Origin: "https://a", TenantID: 0, TenantName: "ignored"})
	save(SaveInput{Label: "general"})
	if _, err := v.Save(SaveInput{Label: "bad", TenantID: 3, Secret: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("a tenant without its server: %v, want errBadChars", err)
	}
	labels := func(s VaultSession) string {
		var out []string
		for _, m := range v.EntriesForSession(s) {
			out = append(out, m.Label)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		s    VaultSession
		want string
	}{
		{VaultSession{Origin: "https://a", TenantID: 1}, "general,server,tenant"},
		{VaultSession{Origin: "https://a", TenantID: 2}, "general,server"},
		{VaultSession{Origin: "https://b", TenantID: 1}, "general"},
		{VaultSession{Origin: "", TenantID: 1}, ""},          // server unknown: nothing
		{VaultSession{Origin: "https://a", TenantID: 0}, ""}, // tenant unknown: nothing
	} {
		if got := labels(c.s); got != c.want {
			t.Errorf("session %+v: %q, want %q", c.s, got, c.want)
		}
	}
	for _, m := range v.List() {
		if m.Label == "server" && m.TenantName != "" {
			t.Errorf("a whole-server entry keeps no tenant name")
		}
	}
	// Typed only where offered: BuildTypeText follows the same rule.
	gen := v.List()
	var general string
	for _, m := range gen {
		if m.Label == "general" {
			general = m.ID
		}
	}
	build := func(u string, s []byte) ([]byte, error) { return append([]byte(nil), s...), nil }
	if _, err := v.BuildTypeText(general, VaultSession{Origin: "https://z", TenantID: 9}, false, build); err != nil {
		t.Fatalf("a general entry, any known session: %v", err)
	}
	if _, err := v.BuildTypeText(general, VaultSession{Origin: "", TenantID: 0}, false, build); !errors.Is(err, errScopeMismatch) {
		t.Fatalf("a general entry, an unknown session: %v, want errScopeMismatch", err)
	}
	relock(t, v)
	if got := labels(VaultSession{Origin: "https://a", TenantID: 2}); got != "general,server" {
		t.Fatalf("after the file: %q", got)
	}
}

func TestVaultRestrictedPlace(t *testing.T) {
	v, _ := unlockedVault(t)
	d1 := VaultDevice{Origin: "https://a", TenantID: 1, ID: 11, Name: "W1"}
	d2 := VaultDevice{Origin: "https://a", TenantID: 2, ID: 22, Name: "W2"}
	d3 := VaultDevice{Origin: "https://b", TenantID: 5, ID: 33, Name: "W3"}
	cases := []struct {
		in         SaveInput
		wantOrigin string
		wantTenant uint32
	}{
		// machines of two tenants of one server, the place "the whole server": kept
		{SaveInput{Label: "s", Origin: "https://a", TenantID: 0, Devices: []VaultDevice{d1, d2}}, "https://a", 0},
		// machines of two servers, the place "every server": kept
		{SaveInput{Label: "g", Devices: []VaultDevice{d1, d3}}, "", 0},
		// a place holding none of its machines: its first machine's (as before)
		{SaveInput{Label: "x", Origin: "https://b", TenantID: 0, Devices: []VaultDevice{d1}}, "https://a", 1},
	}
	for _, c := range cases {
		c.in.Secret = "pw"
		id, err := v.Save(c.in)
		if err != nil {
			t.Fatalf("Save %s: %v", c.in.Label, err)
		}
		for _, m := range v.List() {
			if m.ID == id && (m.Origin != c.wantOrigin || m.TenantID != c.wantTenant) {
				t.Errorf("%s: place %q/%d, want %q/%d", c.in.Label, m.Origin, m.TenantID, c.wantOrigin, c.wantTenant)
			}
		}
	}
	// A restriction still decides where it is typed, whatever its place.
	got := v.EntriesForSession(VaultSession{Origin: "https://b", TenantID: 5, DeviceID: 33})
	if len(got) != 1 || got[0].Label != "g" {
		t.Fatalf("W3's session: %+v, want the general entry restricted to it", got)
	}
	if got := v.EntriesForSession(VaultSession{Origin: "https://a", TenantID: 2, DeviceID: 99}); len(got) != 0 {
		t.Fatalf("another machine of the server: %+v, want none (restricted)", got)
	}
}

func TestVaultCredentials(t *testing.T) {
	v, store := unlockedVault(t)
	// No credential yet: the file keeps the older format.
	if _, err := v.Save(SaveInput{Label: "plain", Kind: vaultKindSSH, Host: "h0", Port: 22, Username: "u0", Secret: "own0"}); err != nil {
		t.Fatal(err)
	}
	if ver := innerVersion(t, store); ver != 2 {
		t.Fatalf("no credential: written as v%d, want v2 (an older Desktop keeps reading it)", ver)
	}
	if _, err := v.Save(SaveInput{Label: "nope", Kind: vaultKindCredential, Host: "h", Secret: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("a credential with a host: %v", err)
	}
	if _, err := v.Save(SaveInput{Label: "nope", Kind: vaultKindCredential}); !errors.Is(err, errEmptySecret) {
		t.Fatalf("a credential without its secret: %v", err)
	}
	cred, err := v.Save(SaveInput{Label: "root lab", Kind: vaultKindCredential, Username: "root", Secret: "labpw"})
	if err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	if ver := innerVersion(t, store); ver != 4 {
		t.Fatalf("a credential: written as v%d, want v4", ver)
	}
	ssh, err := v.Save(SaveInput{Label: "srv ssh", Kind: vaultKindSSH, Host: "10.0.0.1", Port: 22, Username: "ignored", Secret: "ignored", CredID: cred})
	if err != nil {
		t.Fatalf("an SSH connection using it: %v", err)
	}
	rdp, err := v.Save(SaveInput{Label: "srv rdp", Kind: vaultKindRDP, Host: "10.0.0.2", Port: 3389, CredID: cred})
	if err != nil {
		t.Fatalf("an RDP connection using it (no secret of its own): %v", err)
	}
	if _, err := v.Save(SaveInput{Label: "x", Kind: vaultKindSSH, Host: "h", Port: 22, CredID: strings.Repeat("ab", 16)}); !errors.Is(err, errCredMissing) {
		t.Fatalf("an unknown credential: %v", err)
	}
	if _, err := v.Save(SaveInput{Label: "x", Origin: "https://a", TenantID: 1, Secret: "p", CredID: cred}); !errors.Is(err, errBadChars) {
		t.Fatalf("a password entry with a credential: %v", err)
	}
	check := func(id, user, secret string) {
		t.Helper()
		rc, err := v.ResolveConn(id)
		if err != nil {
			t.Fatalf("ResolveConn: %v", err)
		}
		if rc.Meta.Username != user || rc.Secret != secret {
			t.Fatalf("resolved %q / %q, want %q / %q", rc.Meta.Username, rc.Secret, user, secret)
		}
	}
	check(ssh, "root", "labpw")
	check(rdp, "root", "labpw")
	// The tree shows the credential's username, never stores a copy; the
	// credential itself is in neither list.
	for _, m := range v.ByParent("") {
		if m.Kind == vaultKindCredential {
			t.Fatalf("a credential listed in the connection tree")
		}
		if m.ID == ssh && (m.Username != "root" || m.CredID != cred || m.Stored) {
			t.Fatalf("the SSH node: %+v", m)
		}
		if m.ID == "" {
			t.Fatal("no id")
		}
	}
	for _, m := range v.List() {
		if m.Kind == vaultKindCredential {
			t.Fatalf("a credential in the password list")
		}
	}
	// Changing the credential changes every connection using it.
	if _, err := v.Save(SaveInput{ID: cred, Label: "root lab", Kind: vaultKindCredential, Username: "admin", Secret: "newpw"}); err != nil {
		t.Fatal(err)
	}
	check(ssh, "admin", "newpw")
	check(rdp, "admin", "newpw")
	// Its uses, sorted.
	cs := v.Credentials()
	if len(cs) != 1 || len(cs[0].Uses) != 2 || cs[0].Uses[0].Label != "srv rdp" || cs[0].Uses[1].Label != "srv ssh" {
		t.Fatalf("Credentials: %+v", cs)
	}
	// A key credential: refused while an RDP connection uses it.
	if _, err := v.Save(SaveInput{ID: cred, Label: "root lab", Kind: vaultKindCredential, Username: "admin", AuthMethod: vaultAuthKey, Secret: fakePEMPrivateKey(3)}); !errors.Is(err, errCredKeyRDP) {
		t.Fatalf("a key for an RDP connection: %v", err)
	}
	// Deleting a credential in use: refused.
	if err := v.Delete(cred); !errors.Is(err, errCredInUse) {
		t.Fatalf("Delete in use: %v", err)
	}
	// A connection letting it go needs a secret of its own.
	if _, err := v.Save(SaveInput{ID: rdp, Label: "srv rdp", Kind: vaultKindRDP, Host: "10.0.0.2", Port: 3389}); !errors.Is(err, errEmptySecret) {
		t.Fatalf("its credential let go without a secret: %v", err)
	}
	if _, err := v.Save(SaveInput{ID: rdp, Label: "srv rdp", Kind: vaultKindRDP, Host: "10.0.0.2", Port: 3389, Username: "me", Secret: "mine"}); err != nil {
		t.Fatal(err)
	}
	check(rdp, "me", "mine")
	// Survives the file.
	relock(t, v)
	check(ssh, "admin", "newpw")
	if err := v.Delete(cred); !errors.Is(err, errCredInUse) {
		t.Fatalf("still in use after the file: %v", err)
	}
	if _, err := v.Save(SaveInput{ID: ssh, Label: "srv ssh", Kind: vaultKindSSH, Host: "10.0.0.1", Port: 22, Username: "r", Secret: "own"}); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(cred); err != nil {
		t.Fatalf("Delete unused: %v", err)
	}
	// A credential gone (only possible through an older file): said, never another secret.
	v.mu.Lock()
	for _, e := range v.entries {
		if idHex(e.ID) == ssh {
			e.CredID = [16]byte{1}
		}
	}
	v.mu.Unlock()
	if _, err := v.ResolveConn(ssh); !errors.Is(err, errCredMissing) {
		t.Fatalf("its credential gone: %v", err)
	}
}

func TestVaultKeyPassphrase(t *testing.T) {
	v, _ := unlockedVault(t)
	key := fakePEMPrivateKey(5)
	if _, err := v.Save(SaveInput{Label: "p", Kind: vaultKindSSH, Host: "h", Port: 22, Secret: "pw", Passphrase: "x"}); !errors.Is(err, errBadChars) {
		t.Fatalf("a passphrase on a password: %v", err)
	}
	cred, err := v.Save(SaveInput{Label: "nas", Kind: vaultKindCredential, Username: "admin", AuthMethod: vaultAuthKey, Secret: key, Passphrase: "open sesame"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := v.Save(SaveInput{Label: "nas ssh", Kind: vaultKindSSH, Host: "nas", Port: 22, CredID: cred})
	if err != nil {
		t.Fatal(err)
	}
	relock(t, v)
	rc, err := v.ResolveConn(conn)
	if err != nil || rc.Secret != key || rc.Passphrase != "open sesame" || rc.Meta.AuthMethod != vaultAuthKey {
		t.Fatalf("resolved: %v %q auth %d", err, rc.Passphrase, rc.Meta.AuthMethod)
	}
	if cs := v.Credentials(); len(cs) != 1 || !cs[0].KeyProtected {
		t.Fatalf("a protected key: said")
	}
	// An edit without a passphrase keeps it; clearPassphrase removes it.
	if _, err := v.Save(SaveInput{ID: cred, Label: "nas", Kind: vaultKindCredential, Username: "admin", AuthMethod: vaultAuthKey}); err != nil {
		t.Fatal(err)
	}
	if p, _ := v.RevealPassphrase(cred); p != "open sesame" {
		t.Fatalf("kept: %q", p)
	}
	if _, err := v.Save(SaveInput{ID: cred, Label: "nas", Kind: vaultKindCredential, Username: "admin", AuthMethod: vaultAuthKey, ClearPassphrase: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := v.RevealPassphrase(cred); p != "" {
		t.Fatalf("cleared: %q", p)
	}
}

func TestVaultExportUsesTheCredential(t *testing.T) {
	v, _ := unlockedVault(t)
	cred, _ := v.Save(SaveInput{Label: "c", Kind: vaultKindCredential, Username: "root", Secret: "credpw"})
	conn, err := v.Save(SaveInput{Label: "box", Kind: vaultKindSSH, Host: "h", Port: 22, CredID: cred})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := v.ExportConnections(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == cred {
			t.Fatalf("a credential exported as a node")
		}
		if n.ID == conn && (n.Username != "root" || n.secret != "credpw") {
			t.Fatalf("the connection exported with %q / %q", n.Username, n.secret)
		}
	}
}
