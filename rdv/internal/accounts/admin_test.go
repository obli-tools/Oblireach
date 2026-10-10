package accounts

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// A database written by the first version (schema 1) is migrated in place,
// keeps its data, and opening it again changes nothing.
func TestMigrateFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFile)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	if err := migrate1(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1;
		INSERT INTO accounts(email, name, pw_hash, created_at, verified_at) VALUES('old@example.com', 'Old', 'h', 1000, 1000);
		INSERT INTO tokens(hash, account_id, expires_at, refreshed_at) VALUES('abc', 1, 99999999999, 5000);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for i := 0; i < 2; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var v int
		_ = st.db.QueryRow(`PRAGMA user_version`).Scan(&v)
		if v != SchemaVersion() {
			t.Fatalf("schema %d", v)
		}
		a, _, err := st.ByEmail(ctx, "old@example.com")
		if err != nil || a.Role != RoleOperator || a.TOTP || !a.Verified || a.Quota.MaxSessions != nil {
			t.Fatalf("migrated account %+v %v", a, err)
		}
		toks, err := st.Tokens(ctx, a.ID)
		if err != nil || len(toks) != 1 || toks[0].Created.Unix() != 5000 || toks[0].LastUsed.Unix() != 5000 {
			t.Fatalf("migrated tokens %+v %v", toks, err)
		}
		st.Close()
	}
	// A step interrupted half way (a column already added) runs again.
	db, _ = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	_, _ = db.Exec(`PRAGMA user_version = 1`)
	db.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatalf("re-run of migration 2: %v", err)
	}
	st.Close()
	// A newer schema is refused.
	db, _ = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	_, _ = db.Exec(`PRAGMA user_version = 99`)
	db.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("newer schema: %v", err)
	}
}

// RFC 6238 appendix B (SHA-1), truncated to 6 digits.
func TestTOTPVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, c := range []struct {
		t    int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		if got := TOTPCode(key, c.t/30); got != c.want {
			t.Errorf("T=%d: %s, want %s", c.t, got, c.want)
		}
	}
	now := time.Unix(1111111111, 0)
	if CheckTOTP(key, "050471", now) != 1111111111/30 {
		t.Fatal("current step")
	}
	if CheckTOTP(key, TOTPCode(key, now.Unix()/30-1), now) < 0 || CheckTOTP(key, TOTPCode(key, now.Unix()/30+1), now) < 0 {
		t.Fatal("one step of skew refused")
	}
	if CheckTOTP(key, TOTPCode(key, now.Unix()/30-2), now) >= 0 || CheckTOTP(key, "12345", now) >= 0 || CheckTOTP(key, "abcdef", now) >= 0 {
		t.Fatal("bad code accepted")
	}
	s := NewTOTPSecret()
	k, err := ParseTOTPSecret(strings.ToLower(s[:4]) + " " + s[4:])
	if err != nil || len(k) != 20 {
		t.Fatalf("secret round trip: %v", err)
	}
	uri := TOTPURI("Oblireach", "jane@example.com", s)
	if !strings.HasPrefix(uri, "otpauth://totp/Oblireach:jane@example.com?") || !strings.Contains(uri, "secret="+s) {
		t.Fatal(uri)
	}
}

func TestSealer(t *testing.T) {
	a, b := NewSealer([]byte("server secret one 0123456789abcdef"), "totp"), NewSealer([]byte("server secret two 0123456789abcdef"), "totp")
	c := a.Seal([]byte("hello"), []byte("acct:1"))
	if p, err := a.Open(c, []byte("acct:1")); err != nil || string(p) != "hello" {
		t.Fatal("round trip")
	}
	if _, err := a.Open(c, []byte("acct:2")); err == nil {
		t.Fatal("sealed value moved to another account")
	}
	if _, err := b.Open(c, []byte("acct:1")); err == nil {
		t.Fatal("opened with another server secret")
	}
	if _, err := NewSealer([]byte("server secret one 0123456789abcdef"), "other").Open(c, []byte("acct:1")); err == nil {
		t.Fatal("opened with another purpose")
	}
}

func TestRolesAndLastAdmin(t *testing.T) {
	st, _ := openStore(t)
	a, _ := st.Create(ctx, "a@example.com", "A", "h", true)
	b, _ := st.Create(ctx, "b@example.com", "B", "h", false)
	if n, err := st.EnsureAdmins(ctx, []string{"A@example.com", "b@example.com", "nobody@example.com"}); err != nil || n != 1 {
		t.Fatalf("ensure admins: %d %v", n, err) // b is unverified
	}
	if x, _ := st.ByID(ctx, b.ID); x.IsAdmin() {
		t.Fatal("unverified promoted")
	}
	for _, f := range []func() error{
		func() error { return st.SetRole(ctx, a.ID, RoleOperator) },
		func() error { return st.SetDisabledID(ctx, a.ID, true) },
		func() error { return st.DeleteID(ctx, a.ID) },
	} {
		if err := f(); !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("last admin: %v", err)
		}
	}
	if err := st.SetRole(ctx, b.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole(ctx, a.ID, RoleOperator); err != nil {
		t.Fatal("demote with another admin left")
	}
	if err := st.SetRole(ctx, a.ID, "root"); err == nil {
		t.Fatal("invalid role")
	}
}

func TestSearchAndStats(t *testing.T) {
	st, clk := openStore(t)
	var ids []int64
	for _, e := range []string{"zed@example.com", "amy@example.com", "bob_x@example.com"} {
		a, _ := st.Create(ctx, e, strings.ToUpper(e[:1])+e[1:3], "h", true)
		ids = append(ids, a.ID)
		clk.add(time.Hour)
	}
	rows, total, err := st.SearchAccounts(ctx, "", "email", false, 10, 0)
	if err != nil || total != 3 || rows[0].Email != "amy@example.com" || rows[2].Email != "zed@example.com" {
		t.Fatalf("sort: %v %d", err, total)
	}
	// LIKE wildcards are literal.
	if rows, total, _ := st.SearchAccounts(ctx, "b_x", "email", false, 10, 0); total != 1 || rows[0].Email != "bob_x@example.com" {
		t.Fatal("search b_x")
	}
	if _, total, _ := st.SearchAccounts(ctx, "%", "", false, 10, 0); total != 0 {
		t.Fatal("% matched")
	}
	if rows, _, _ := st.SearchAccounts(ctx, "", "name; DROP TABLE accounts", false, 10, 0); len(rows) != 3 {
		t.Fatal("unknown sort key")
	}
	now := clk.now()
	add := func(id int64, path string, accepted bool, relayed int64, ago time.Duration) {
		r := SessionRecord{AccountID: id, HostTID: "482******", Started: now.Add(-ago - time.Minute), Ended: now.Add(-ago), Path: path, Relayed: relayed, EndReason: "bye"}
		if accepted {
			r.Accepted = r.Started.Add(10 * time.Second)
		}
		if err := st.AddSessionRecord(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	add(ids[0], "direct", true, 0, time.Hour)
	add(ids[0], "relay_udp", true, 5000, 2*24*time.Hour)
	add(ids[0], "unknown", false, 0, time.Hour)
	add(ids[1], "relay_tcp", true, 7000, 40*24*time.Hour)
	add(0, "direct", true, 0, time.Hour)
	_ = st.AddSessionRecord(ctx, SessionRecord{AccountID: 999, HostTID: "x", Started: now, Ended: now, Path: "direct", EndReason: "bye"}) // deleted account: dropped
	s, err := st.SessionStatsSince(ctx, ids[0], now.Add(-30*24*time.Hour))
	if err != nil || s.Sessions != 2 || s.Direct != 1 || s.Relay != 1 || s.Failed != 1 || s.Relayed != 5000 || s.Seconds != 100 {
		t.Fatalf("stats %+v %v", s, err)
	}
	all, _ := st.SessionStatsSince(ctx, 0, now.Add(-30*24*time.Hour))
	if all.Sessions != 3 {
		t.Fatalf("all stats %+v", all)
	}
	days, _ := st.DailyStats(ctx, 0, 30)
	if len(days) != 30 || days[29].Day != now.Format("2006-01-02") || days[29].Direct != 2 || days[27].Relay != 1 || days[27].Relayed != 5000 {
		t.Fatalf("daily %+v", days[27:])
	}
	rows, _, _ = st.SearchAccounts(ctx, "", "sessions", true, 10, 0)
	if rows[0].ID != ids[0] || rows[0].Sessions30 != 2 {
		t.Fatalf("sessions sort %+v", rows[0])
	}
	recs, _ := st.Sessions(ctx, 0, 10, 0)
	if len(recs) != 5 || recs[len(recs)-1].Email != "amy@example.com" {
		t.Fatalf("sessions %d", len(recs))
	}
	c, _ := st.AccountCounts(ctx)
	if c.Total != 3 || c.Verified != 3 || c.NewWeek != 3 {
		t.Fatalf("counts %+v", c)
	}
}

func TestActionTokensAndWebSessions(t *testing.T) {
	st, clk := openStore(t)
	a, _ := st.Create(ctx, "a@example.com", "A", "h", true)
	s1, _ := st.NewActionToken(ctx, a.ID, ActionReset, "", time.Hour)
	s2, _ := st.NewActionToken(ctx, a.ID, ActionReset, "", time.Hour)
	if _, _, err := st.ActionToken(ctx, s1, ActionReset); err == nil {
		t.Fatal("an older link of the same kind still works")
	}
	if _, _, err := st.ActionToken(ctx, s2, ActionEmail); err == nil {
		t.Fatal("link used for another kind")
	}
	if id, _, err := st.ConsumeActionToken(ctx, s2, ActionReset); err != nil || id != a.ID {
		t.Fatal("consume")
	}
	if _, _, err := st.ConsumeActionToken(ctx, s2, ActionReset); err == nil {
		t.Fatal("consumed twice")
	}
	w, _ := st.NewWebSession(ctx, a.ID, "1.2.3.0/24", false)
	ws, acc, err := st.ResolveWebSession(ctx, w)
	if err != nil || acc.ID != a.ID || ws.MFA || ws.IP != "1.2.3.0/24" {
		t.Fatal("resolve")
	}
	_ = st.SetWebSessionMFA(ctx, ws.Hash)
	// Sliding idle limit, absolute limit.
	for i := 0; i < 15; i++ { // 165 h of use, never 12 h idle
		clk.add(11 * time.Hour)
		if _, _, err := st.ResolveWebSession(ctx, w); err != nil {
			t.Fatalf("idle refresh %d: %v", i, err)
		}
	}
	clk.add(4 * time.Hour) // 169 h > 7 days
	if _, _, err := st.ResolveWebSession(ctx, w); err == nil {
		t.Fatal("past the absolute lifetime")
	}
	if ok, _ := st.UseTOTPStep(ctx, a.ID, 10); !ok {
		t.Fatal("step")
	}
	if ok, _ := st.UseTOTPStep(ctx, a.ID, 10); ok {
		t.Fatal("step replayed")
	}
	if ok, _ := st.UseTOTPStep(ctx, a.ID, 9); ok {
		t.Fatal("older step")
	}
}
