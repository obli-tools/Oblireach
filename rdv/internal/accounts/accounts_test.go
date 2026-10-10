package accounts

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oblireach/rdv/internal/smtptest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func openStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sub", DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &clock{t: time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)}
	st.SetClock(c.now)
	return st, c
}

var ctx = context.Background()

func TestCreateAndTokens(t *testing.T) {
	st, clk := openStore(t)
	a, err := st.Create(ctx, "jane@example.com", "Jane", HashPassword("correct horse battery"), false)
	if err != nil || a.ID == 0 || a.Verified || a.Email != "jane@example.com" {
		t.Fatalf("create: %+v %v", a, err)
	}
	if _, err := st.Create(ctx, "jane@example.com", "Other", "x", false); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	b, hash, err := st.ByEmail(ctx, "jane@example.com")
	if err != nil || b.ID != a.ID || !CheckPassword(hash, "correct horse battery") || CheckPassword(hash, "wrong horse battery") {
		t.Fatalf("by email: %v", err)
	}

	tok, exp, err := st.IssueToken(ctx, a.ID)
	if err != nil || !strings.HasPrefix(tok, "rdv1.") || len(tok) < 40 || !exp.Equal(clk.now().Add(TokenTTL)) {
		t.Fatalf("issue: %q %v %v", tok, exp, err)
	}
	// Stored hashed only.
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE hash = ?`, tok).Scan(&n); err != nil || n != 0 {
		t.Fatal("token stored in clear")
	}
	got, e1, err := st.ResolveToken(ctx, tok)
	if err != nil || got.ID != a.ID || !got.LastLogin.Equal(clk.now()) || !e1.Equal(exp) {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	// Sliding: refreshed at most once a day.
	clk.add(23 * time.Hour)
	if _, e2, _ := st.ResolveToken(ctx, tok); !e2.Equal(exp) {
		t.Fatal("expiry slid within a day")
	}
	clk.add(2 * time.Hour)
	_, e3, err := st.ResolveToken(ctx, tok)
	if err != nil || !e3.Equal(clk.now().Add(TokenTTL)) {
		t.Fatalf("expiry not slid after a day: %v %v", e3, err)
	}
	// Expiry.
	clk.add(TokenTTL + time.Second)
	if _, _, err := st.ResolveToken(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	// Revocation and unknown tokens.
	tok2, _, _ := st.IssueToken(ctx, a.ID)
	if err := st.RevokeToken(ctx, tok2); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{tok2, "", "rdv1.unknown", strings.Repeat("x", 200)} {
		if _, _, err := st.ResolveToken(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token %q: %v", bad, err)
		}
	}
}

func TestDisableDelete(t *testing.T) {
	st, _ := openStore(t)
	a, _ := st.Create(ctx, "bob@example.com", "Bob", HashPassword("another long one"), true)
	tok, _, _ := st.IssueToken(ctx, a.ID)
	if err := st.SetDisabled(ctx, "bob@example.com", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ResolveToken(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatal("token of a disabled account still valid")
	}
	if err := st.SetDisabled(ctx, "bob@example.com", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ResolveToken(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabling must revoke the tokens for good")
	}
	if err := st.SetDisabled(ctx, "nobody@example.com", true); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_ = st.AddSession(ctx, a.ID)
	if err := st.Delete(ctx, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ByEmail(ctx, "bob@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatal("not deleted")
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM usage_day`).Scan(&n)
	if n != 0 {
		t.Fatal("counters survived the account")
	}
	if err := st.Delete(ctx, "bob@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPendingAndVerify(t *testing.T) {
	st, clk := openStore(t)
	var secrets []string
	for i := 0; i < 4; i++ {
		s, err := st.AddPending(ctx, "amy@example.com", "Amy", HashPassword("pending password "+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, s)
	}
	if st.PendingValid(ctx, secrets[0]) {
		t.Fatal("only the 3 most recent registrations are kept")
	}
	hashes, _ := st.PendingHashes(ctx, "amy@example.com")
	if len(hashes) != 3 || !st.PendingValid(ctx, secrets[3]) {
		t.Fatalf("pending %d", len(hashes))
	}
	a, created, err := st.Verify(ctx, secrets[2])
	if err != nil || !created || !a.Verified || a.Name != "Amy" {
		t.Fatalf("verify: %+v %v %v", a, created, err)
	}
	_, pw, _ := st.ByEmail(ctx, "amy@example.com")
	if !CheckPassword(pw, "pending password c") {
		t.Fatal("the verified registration's password must be the account's")
	}
	if st.PendingValid(ctx, secrets[3]) {
		t.Fatal("other registrations of the address must be dropped")
	}
	if _, _, err := st.Verify(ctx, secrets[2]); !errors.Is(err, ErrNotFound) {
		t.Fatal("a link works once")
	}
	// A verified account is never overwritten by a new registration.
	s, _ := st.AddPending(ctx, "amy@example.com", "Mallory", HashPassword("attacker password 1"))
	if _, created, err := st.Verify(ctx, s); err != nil || created {
		t.Fatalf("re-verify: %v %v", created, err)
	}
	if _, pw, _ := st.ByEmail(ctx, "amy@example.com"); !CheckPassword(pw, "pending password c") {
		t.Fatal("verified account overwritten")
	}
	// Expiry of the link.
	s, _ = st.AddPending(ctx, "zed@example.com", "Zed", HashPassword("zed password 123"))
	clk.add(VerifyTTL + time.Second)
	if st.PendingValid(ctx, s) {
		t.Fatal("expired link valid")
	}
	if _, _, err := st.Verify(ctx, s); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired link verified")
	}
	// An unverified account (created without SMTP) is taken over by the
	// mailbox owner, and its tokens end.
	u, _ := st.Create(ctx, "old@example.com", "Old", HashPassword("old password 123"), false)
	tok, _, _ := st.IssueToken(ctx, u.ID)
	s, _ = st.AddPending(ctx, "old@example.com", "New", HashPassword("new password 123"))
	if a, created, err := st.Verify(ctx, s); err != nil || !created || !a.Verified || a.Name != "New" || a.ID != u.ID {
		t.Fatalf("take over: %+v %v", a, err)
	}
	if _, _, err := st.ResolveToken(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatal("tokens of the unverified account survived")
	}
}

func TestUsageAndPurge(t *testing.T) {
	st, clk := openStore(t)
	a, _ := st.Create(ctx, "u@example.com", "U", "h", true)
	for i := 0; i < 3; i++ {
		_ = st.AddSession(ctx, a.ID)
	}
	_ = st.AddRelay(ctx, a.ID, 1000)
	_ = st.AddRelay(ctx, a.ID, 500)
	_ = st.AddRelay(ctx, a.ID, 0)
	u, err := st.Usage(ctx, a.ID)
	if err != nil || u.SessionsToday != 3 || u.RelayBytesMonth != 1500 {
		t.Fatalf("usage %+v %v", u, err)
	}
	clk.add(24 * time.Hour) // 2026-04-01: new day, new month
	if u, _ = st.Usage(ctx, a.ID); u != (Usage{}) {
		t.Fatalf("counters not per day/month: %+v", u)
	}
	list, us, err := st.List(ctx)
	if err != nil || len(list) != 1 || len(us) != 1 {
		t.Fatal(err)
	}
	// Purge: the previous month is kept, older ones go; inactive accounts
	// are deleted after 24 months.
	if _, err := st.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM usage_month`).Scan(&n)
	if n != 1 {
		t.Fatalf("previous month purged too early (%d)", n)
	}
	clk.add(InactiveTTL)
	if d, err := st.Purge(ctx); err != nil || d != 1 {
		t.Fatalf("inactive account not purged: %d %v", d, err)
	}
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM usage_month`).Scan(&n)
	if n != 0 {
		t.Fatal("old counters kept")
	}
}

func TestReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFile)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.Create(ctx, "keep@example.com", "Keep", "h", true)
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.ByEmail(ctx, "keep@example.com"); err != nil {
		t.Fatal("data lost across reopen")
	}
}

func TestPasswords(t *testing.T) {
	h := HashPassword("a fine passphrase")
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") || !CheckPassword(h, "a fine passphrase") || CheckPassword(h, "a fine passphrasE") {
		t.Fatalf("hash %s", h)
	}
	if HashPassword("a fine passphrase") == h {
		t.Fatal("no salt")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=99999999,t=1,p=1$AA$AA", "plain"} {
		if CheckPassword(bad, "x") {
			t.Fatalf("accepted %q", bad)
		}
	}
	for pw, want := range map[string]error{
		"short":                        ErrPasswordShort,
		strings.Repeat("x", 129):       ErrPasswordLong,
		"Password123":                  ErrPasswordCommon,
		"QWERTYUIOP":                   ErrPasswordCommon,
		"aaaaaaaaaaaa":                 ErrPasswordCommon,
		"abababababab":                 ErrPasswordCommon,
		"abcdefghijklm":                ErrPasswordCommon,
		"9876543210":                   ErrPasswordCommon,
		"1122112211":                   ErrPasswordCommon,
		"jane.doe@example.com":         ErrPasswordCommon,
		"jane.doe2026":                 ErrPasswordCommon,
		"correct horse battery staple": nil,
		"Tr0ub4dor&3x":                 nil,
		"jane.doe likes long walks":    nil,
	} {
		if err := CheckPolicy(pw, "jane.doe@example.com"); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", pw, err, want)
		}
	}
}

func TestIdentity(t *testing.T) {
	for e, ok := range map[string]bool{
		"jane@example.com":                  true,
		"j.d+tag@sub.ex.fr":                 true,
		"jane@localhost":                    false,
		"Jane <j@ex.com>":                   false,
		"jane@@ex.com":                      false,
		"jane@ex..com":                      false,
		"jané@example.com":                  false,
		"a b@example.com":                   false,
		"":                                  false,
		strings.Repeat("a", 60) + "@ex.com": false,
	} {
		if ValidEmail(e) != ok {
			t.Errorf("ValidEmail(%q) != %v", e, ok)
		}
	}
	if n, ok := CleanDisplayName("  Jane   Doe "); !ok || n != "Jane Doe" {
		t.Fatalf("clean %q", n)
	}
	for _, bad := range []string{"", "Jane <boss@bank.com>", "a@b", "x‮evil", strings.Repeat("n", 41), "Verified (bank)"} {
		if _, ok := CleanDisplayName(bad); ok {
			t.Errorf("name %q accepted", bad)
		}
	}
	a := Account{ID: 7, Name: "Jane Doe", Email: "jane@example.com", Verified: true}
	if l := Label(a); l != "Jane Doe <jane@example.com>" {
		t.Fatal(l)
	}
	a.Verified = false
	if l := Label(a); l != "Jane Doe <jane@example.com> (unverified)" {
		t.Fatal(l)
	}
	a.Name, a.Email = strings.Repeat("N", 40), strings.Repeat("e", 30)+"@example.com"
	if l := Label(a); l != "<"+a.Email+"> (unverified)" {
		t.Fatal(l)
	}
	a.Email = strings.Repeat("e", 52) + "@example.com"
	if l := Label(a); l != "account #7 (unverified)" || len(l) > LabelMax {
		t.Fatal(l)
	}
	a.Verified = true
	if l := Label(a); l != a.Email {
		t.Fatal(l)
	}
}

func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func TestSMTPMailer(t *testing.T) {
	// Plain local relay.
	sink := smtptest.Start(t, nil)
	m, err := NewSMTPMailer(SMTPConfig{Host: "127.0.0.1", Port: sink.Port(), From: "Oblireach <noreply@example.com>", TLS: SMTPNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send(ctx, "jane@example.com", "Confirmez votre compte", "Line 1\n.dot line\nhttps://x/verify?token=abc\n"); err != nil {
		t.Fatal(err)
	}
	msg := sink.Wait(t, 5*time.Second)
	if msg.From != "noreply@example.com" || len(msg.To) != 1 || msg.To[0] != "jane@example.com" ||
		!strings.Contains(msg.Data, "Subject: =?utf-8?q?Confirmez_votre_compte?=") && !strings.Contains(msg.Data, "Subject: Confirmez votre compte") ||
		!strings.Contains(msg.Data, "\r\n.dot line\r\n") || !strings.Contains(msg.Data, "token=abc") {
		t.Fatalf("message %+v", msg)
	}
	if err := m.Send(ctx, "x@example.com\r\nRCPT TO:<y@example.com>", "s", "b"); err == nil {
		t.Fatal("header injection accepted")
	}

	// STARTTLS required: refused by a server that does not offer it.
	m, _ = NewSMTPMailer(SMTPConfig{Host: "127.0.0.1", Port: sink.Port(), From: "noreply@example.com"})
	if err := m.Send(ctx, "jane@example.com", "s", "b"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("no STARTTLS: %v", err)
	}

	// STARTTLS + AUTH.
	srvTLS, pool := selfSigned(t)
	tsink := smtptest.Start(t, srvTLS)
	m, _ = NewSMTPMailer(SMTPConfig{Host: "127.0.0.1", Port: tsink.Port(), From: "noreply@example.com", User: "mailer", Password: "pw", RootCAs: pool})
	if err := m.Send(ctx, "jane@example.com", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if msg := tsink.Wait(t, 5*time.Second); !msg.TLS || msg.User != "mailer" {
		t.Fatalf("starttls message %+v", msg)
	}

	for _, bad := range []SMTPConfig{{}, {Host: "h", From: "not an address"}, {Host: "h", From: "a@b.c", TLS: "ssl"}} {
		if _, err := NewSMTPMailer(bad); err == nil {
			t.Fatalf("config %+v accepted", bad)
		}
	}
}
