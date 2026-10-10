package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oblireach/rdv/internal/accounts"
)

func TestAccountCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RDV_DATA_DIR", dir)
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := accountCmd(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, _, e := run("list"); code != 1 || !strings.Contains(e, "no account database") {
		t.Fatalf("missing db: %d %q", code, e)
	}
	st, err := accounts.Open(filepath.Join(dir, accounts.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := st.Create(ctx, "jane@example.com", "Jane Doe", accounts.HashPassword("a long passphrase"), true)
	tok, _, _ := st.IssueToken(ctx, a.ID)
	_ = st.AddRelay(ctx, a.ID, 3_500_000)

	code, out, _ := run("list")
	if code != 0 || !strings.Contains(out, "jane@example.com") || !strings.Contains(out, "Jane Doe") || !strings.Contains(out, "3.5 MB") || !strings.Contains(out, "1 account(s)") {
		t.Fatalf("list: %d\n%s", code, out)
	}
	if code, out, _ := run("disable", "Jane@Example.com"); code != 0 || !strings.Contains(out, "disabled") {
		t.Fatalf("disable: %d %s", code, out)
	}
	if _, _, err := st.ResolveToken(ctx, tok); err == nil {
		t.Fatal("disable did not revoke the token (server running concurrently)")
	}
	if _, out, _ := run("list"); strings.Count(out, "yes") != 2 { // verified and disabled
		t.Fatal(out)
	}
	if code, _, _ := run("enable", "jane@example.com"); code != 0 {
		t.Fatal("enable")
	}
	if b, _, _ := st.ByEmail(ctx, "jane@example.com"); b.Disabled {
		t.Fatal("still disabled")
	}
	if code, _, _ := run("delete", "jane@example.com"); code != 0 {
		t.Fatal("delete")
	}
	if code, _, e := run("delete", "jane@example.com"); code != 1 || !strings.Contains(e, "no account") {
		t.Fatalf("delete twice: %d %q", code, e)
	}
	for _, bad := range [][]string{{}, {"disable"}, {"list", "x"}, {"rename", "a"}} {
		if code, _, _ := run(bad...); code != 2 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
	st.Close()
}
