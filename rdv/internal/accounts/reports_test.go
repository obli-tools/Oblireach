package accounts

import (
	"errors"
	"testing"
	"time"
)

func TestReportsStore(t *testing.T) {
	st, c := openStore(t)
	a, err := st.Create(ctx, "op@example.com", "Op", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	now := c.now()
	rec := ReportRecord{SID: "s1", AccountID: a.ID, Operator: "Op <op@example.com>", HostTID: "482******", Reason: "scam",
		Text: "card", ReporterIP: "203.0.113.0/24", ReporterKey: "k1", Started: now.Add(-time.Hour), Accepted: now.Add(-50 * time.Minute)}
	id, err := st.AddReport(ctx, rec)
	if err != nil || id == 0 {
		t.Fatalf("add: %d %v", id, err)
	}
	if _, err := st.AddReport(ctx, rec); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate sid: %v", err)
	}
	r, err := st.Report(ctx, id)
	if err != nil || r.Email != "op@example.com" || r.Status != ReportOpen || !r.Ended.IsZero() || r.Created.IsZero() {
		t.Fatalf("report %+v %v", r, err)
	}
	if err := st.SetReportSessionEnd(ctx, "s1", now, "direct"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReportNote(ctx, id, 1, "boss@example.com", "seen"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReportNote(ctx, 999, 1, "boss@example.com", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("note on a missing report: %v", err)
	}
	r, _ = st.Report(ctx, id)
	if r.Ended.IsZero() || r.Path != "direct" || len(r.Notes) != 1 || r.Notes[0].Text != "seen" {
		t.Fatalf("after end + note %+v", r)
	}
	if n, _ := st.OpenReportCount(ctx, a.ID); n != 1 {
		t.Fatalf("open count %d", n)
	}
	rows, _, _ := st.SearchAccounts(ctx, "", "id", false, 10, 0)
	if len(rows) != 1 || rows[0].OpenReports != 1 {
		t.Fatalf("account row %+v", rows)
	}

	// Distinct reporters: by key, closed-as-not-abuse ones excluded.
	rec2 := rec
	rec2.SID, rec2.ReporterKey = "s2", "k1"
	id2, _ := st.AddReport(ctx, rec2)
	rec3 := rec
	rec3.SID, rec3.ReporterKey = "s3", "k2"
	id3, _ := st.AddReport(ctx, rec3)
	if n, _ := st.DistinctReporters(ctx, a.ID, now.Add(-time.Hour)); n != 2 {
		t.Fatalf("distinct %d", n)
	}
	if err := st.SetReportStatus(ctx, id3, ReportNotAbuse, "boss"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.DistinctReporters(ctx, a.ID, now.Add(-time.Hour)); n != 1 {
		t.Fatalf("distinct after not_abuse %d", n)
	}
	if err := st.SetReportStatus(ctx, id3, "bogus", "boss"); err == nil {
		t.Fatal("bogus status accepted")
	}
	open, recent, _ := st.ReportCounts(ctx, now.Add(-time.Hour))
	if open != 2 || recent != 3 {
		t.Fatalf("counts %d %d", open, recent)
	}
	list, total, _ := st.Reports(ctx, ReportFilter{Status: "closed"}, 10, 0)
	if total != 1 || list[0].ID != id3 {
		t.Fatalf("closed list %d %+v", total, list)
	}
	_ = id2
}

// Closed reports follow the history retention from their closing; open
// ones stay, with the history row of their session.
func TestReportsRetention(t *testing.T) {
	st, c := openStore(t)
	st.SetRetention(30*24*time.Hour, 0)
	a, _ := st.Create(ctx, "op@example.com", "Op", "h", true)
	now := c.now()
	for _, sid := range []string{"open1", "closed1", "plain"} {
		if err := st.AddSessionRecord(ctx, SessionRecord{AccountID: a.ID, HostTID: "482******", Started: now, Accepted: now,
			Ended: now, Path: "direct", EndReason: "bye", SID: sid, ReportHash: "h-" + sid}); err != nil {
			t.Fatal(err)
		}
	}
	open, _ := st.AddReport(ctx, ReportRecord{SID: "open1", AccountID: a.ID, HostTID: "482******", Reason: "scam"})
	closed, _ := st.AddReport(ctx, ReportRecord{SID: "closed1", AccountID: a.ID, HostTID: "482******", Reason: "other"})
	if err := st.SetReportStatus(ctx, closed, ReportHandled, "boss"); err != nil {
		t.Fatal(err)
	}
	h, err := st.SessionBySID(ctx, "open1")
	if err != nil || h.ReportHash != "h-open1" || h.AccountID != a.ID {
		t.Fatalf("history by sid %+v %v", h, err)
	}

	c.add(29 * 24 * time.Hour)
	if _, err := st.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Report(ctx, closed); err != nil {
		t.Fatal("closed report purged before the retention")
	}
	c.add(2 * 24 * time.Hour)
	if _, err := st.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Report(ctx, closed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed report kept past the retention: %v", err)
	}
	if _, err := st.Report(ctx, open); err != nil {
		t.Fatal("open report purged")
	}
	if _, err := st.SessionBySID(ctx, "open1"); err != nil {
		t.Fatal("history row of an open report purged")
	}
	for _, sid := range []string{"closed1", "plain"} {
		if _, err := st.SessionBySID(ctx, sid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("history %s kept: %v", sid, err)
		}
	}
	// Closing it lets both go after the retention.
	if err := st.SetReportStatus(ctx, open, ReportNotAbuse, "boss"); err != nil {
		t.Fatal(err)
	}
	c.add(31 * 24 * time.Hour)
	if _, err := st.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Report(ctx, open); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed report kept")
	}
	if _, err := st.SessionBySID(ctx, "open1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("history kept after its report closed")
	}
}

func TestBanAndReview(t *testing.T) {
	st, c := openStore(t)
	a, _ := st.Create(ctx, "op@example.com", "Op", "h", true)
	if _, _, err := st.IssueTokenFor(ctx, a.ID, "desktop", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReview(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BanAccount(ctx, a.ID, "boss", "report 1"); err != nil {
		t.Fatal(err)
	}
	a, _ = st.ByID(ctx, a.ID)
	if !a.Disabled || !a.Banned || a.Review.IsZero() {
		t.Fatalf("banned %+v", a)
	}
	if toks, _ := st.Tokens(ctx, a.ID); len(toks) != 0 {
		t.Fatal("tokens survived the ban")
	}
	if banned, _ := st.EmailBanned(ctx, "OP@example.com"); !banned {
		t.Fatal("address not banned")
	}
	// Another account cannot take the address; it cannot register again.
	b, _ := st.Create(ctx, "other@example.com", "Other", "h", true)
	if err := st.SetEmail(ctx, b.ID, "op@example.com", true); !errors.Is(err, ErrExists) {
		t.Fatalf("email change to a banned address: %v", err)
	}
	if err := st.Delete(ctx, "op@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(ctx, "op@example.com", "Op", "h", true); !errors.Is(err, ErrBanned) {
		t.Fatalf("register a banned address: %v", err)
	}
	secret, _ := st.AddPending(ctx, "op@example.com", "Op", "h")
	if _, _, err := st.Verify(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("verify a banned address: %v", err)
	}
	// The ban outlives the account for InactiveTTL.
	c.add(InactiveTTL + time.Hour)
	if _, err := st.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if banned, _ := st.EmailBanned(ctx, "op@example.com"); banned {
		t.Fatal("ban kept after InactiveTTL")
	}

	// Enabling lifts a ban and a review.
	d, _ := st.Create(ctx, "d@example.com", "D", "h", true)
	_ = st.SetReview(ctx, d.ID, true)
	_ = st.BanAccount(ctx, d.ID, "boss", "admin")
	if err := st.SetDisabledID(ctx, d.ID, false); err != nil {
		t.Fatal(err)
	}
	d, _ = st.ByID(ctx, d.ID)
	if d.Disabled || d.Banned || !d.Review.IsZero() {
		t.Fatalf("after enable %+v", d)
	}
	if banned, _ := st.EmailBanned(ctx, "d@example.com"); banned {
		t.Fatal("ban not lifted")
	}
}
