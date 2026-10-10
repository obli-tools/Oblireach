package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oblireach/rdv/internal/accounts"
)

func TestReportCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RDV_DATA_DIR", dir)
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := reportCmd(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, _, e := run("list"); code != 1 || !strings.Contains(e, "no account database") {
		t.Fatalf("missing db: %d %q", code, e)
	}
	st, err := accounts.Open(filepath.Join(dir, accounts.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, _ := st.Create(ctx, "jane@example.com", "Jane Doe", accounts.HashPassword("a long passphrase"), true)
	id, err := st.AddReport(ctx, accounts.ReportRecord{SID: "0123456789abcdef0123456789abcdef", AccountID: a.ID, HostTID: "482******",
		Reason: "scam", Text: "asked for my card"})
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := run("list")
	if code != 0 || !strings.Contains(out, "jane@example.com") || !strings.Contains(out, "01234567") || !strings.Contains(out, "scam") ||
		!strings.Contains(out, "asked for my card") || !strings.Contains(out, "1 report(s)") {
		t.Fatalf("list: %d\n%s", code, out)
	}
	if code, out, _ := run("close", "1", "handled"); code != 0 || !strings.Contains(out, "closed") {
		t.Fatalf("close: %d %s", code, out)
	}
	if r, _ := st.Report(ctx, id); r.Status != accounts.ReportHandled || r.ClosedBy != "cli" {
		t.Fatalf("closed %+v", r)
	}
	if _, out, _ := run("list"); !strings.Contains(out, "0 report(s)") {
		t.Fatal(out)
	}
	if _, out, _ := run("list", "closed"); !strings.Contains(out, "1 report(s)") {
		t.Fatal(out)
	}
	if code, _, e := run("close", "9", "handled"); code != 1 || !strings.Contains(e, "no report") {
		t.Fatalf("close missing: %d %q", code, e)
	}
	for _, bad := range [][]string{{}, {"list", "x"}, {"close", "1"}, {"close", "x", "handled"}, {"close", "1", "maybe"}, {"drop"}} {
		if code, _, _ := run(bad...); code != 2 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
}
