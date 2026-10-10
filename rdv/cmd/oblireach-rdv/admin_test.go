package main

import (
	"bytes"
	"context"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/oblireach/rdv/internal/accounts"
)

func TestAdminCreateAndRoles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RDV_DATA_DIR", dir)
	t.Setenv("RDV_PUBLIC_URL", "https://rdv.example.com/")
	t.Setenv("RDV_ADMIN_EMAILS", "")
	run := func(f func([]string, *bytes.Buffer, *bytes.Buffer) int, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := f(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	admin := func(a []string, o, e *bytes.Buffer) int { return adminCmd(a, o, e) }
	account := func(a []string, o, e *bytes.Buffer) int { return accountCmd(a, o, e) }

	// First set-up: no database yet, no SMTP.
	code, out, _ := run(admin, "create", "Owner@Example.com", "The", "Owner")
	if code != 0 || !strings.Contains(out, "created (verified)") {
		t.Fatalf("admin create: %d %s", code, out)
	}
	link := regexp.MustCompile(`https://rdv\.example\.com/reset\?token=\S+`).FindString(out)
	if link == "" {
		t.Fatalf("no link: %s", out)
	}
	st, err := accounts.Open(filepath.Join(dir, accounts.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, _, err := st.ByEmail(ctx, "owner@example.com")
	if err != nil || !a.IsAdmin() || !a.Verified || a.Name != "The Owner" {
		t.Fatalf("created %+v %v", a, err)
	}
	u, _ := url.Parse(link)
	if id, _, err := st.ActionToken(ctx, u.Query().Get("token"), accounts.ActionReset); err != nil || id != a.ID {
		t.Fatal("set-password link not stored")
	}
	// Run again on the existing account: a new link, the old one revoked.
	code, out, _ = run(admin, "create", "owner@example.com")
	if code != 0 || !strings.Contains(out, "already had an account") {
		t.Fatalf("again: %s", out)
	}
	if _, _, err := st.ActionToken(ctx, u.Query().Get("token"), accounts.ActionReset); err == nil {
		t.Fatal("previous link still valid")
	}
	if code, _, _ := run(admin, "create", "not an address"); code != 1 {
		t.Fatal("bad address")
	}
	if code, _, _ := run(admin, "delete", "x@example.com"); code != 2 {
		t.Fatal("bad subcommand")
	}

	// promote / demote / last admin / reset-2fa.
	op, _ := st.Create(ctx, "op@example.com", "Op", "h", true)
	if code, out, _ := run(account, "promote", "OP@example.com"); code != 0 || !strings.Contains(out, "promoted") {
		t.Fatalf("promote: %s", out)
	}
	if x, _ := st.ByID(ctx, op.ID); !x.IsAdmin() {
		t.Fatal("not promoted")
	}
	if code, _, _ := run(account, "demote", "op@example.com"); code != 0 {
		t.Fatal("demote")
	}
	if code, _, e := run(account, "demote", "owner@example.com"); code != 1 || !strings.Contains(e, "last enabled admin") {
		t.Fatalf("last admin: %d %s", code, e)
	}
	t.Setenv("RDV_ADMIN_EMAILS", "op@example.com")
	_ = st.SetRole(ctx, op.ID, accounts.RoleAdmin)
	if _, out, _ := run(account, "demote", "op@example.com"); !strings.Contains(out, "RDV_ADMIN_EMAILS") {
		t.Fatal("no note about RDV_ADMIN_EMAILS")
	}
	_ = st.SetTOTP(ctx, op.ID, []byte("sealed-secret-bytes-0123456789"))
	if code, out, _ := run(account, "reset-2fa", "op@example.com"); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("reset-2fa: %s", out)
	}
	if x, _ := st.ByID(ctx, op.ID); x.TOTP {
		t.Fatal("TOTP kept")
	}
	if code, _, _ := run(account, "promote", "ghost@example.com"); code != 1 {
		t.Fatal("unknown account")
	}
	// Every CLI change is in the audit log.
	entries, total, _ := st.AuditLog(ctx, 20, 0)
	if total != 6 || entries[0].Action != "cli_reset_2fa" || entries[0].Actor != "cli" {
		t.Fatalf("audit %d %+v", total, entries)
	}
	code, out, _ = run(account, "list")
	if code != 0 || !strings.Contains(out, "ROLE") || !strings.Contains(out, "admin") {
		t.Fatalf("list: %s", out)
	}
}
