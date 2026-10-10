package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	sl "github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

// report sends an abuse report like the thin client does (supportlink),
// from the address xff when the server trusts proxy headers.
func (e *testEnv) report(t *testing.T, sid, token, reason, text string, end bool, xff string) error {
	t.Helper()
	o := e.sig
	o.Proxy = sl.ProxyDirect
	if xff != "" {
		o.Header = http.Header{"X-Forwarded-For": {xff}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return sl.SendReport(ctx, e.url, o, sl.Report{SID: sid, Token: token, Reason: reason, Text: text, EndSession: end})
}

// rawReport posts a JSON body to the report endpoint.
func (e *acctEnv) rawReport(t *testing.T, method, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, e.base+wire.ReportPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

// operatorSession signs in an operator account and connects it to a fresh
// host; it returns the host and operator sessions.
func (e *acctEnv) operatorSession(t *testing.T, email, pw string) (*sl.Session, *sl.Session) {
	t.Helper()
	_, r := e.login(t, email, pw)
	if r.Token == "" {
		t.Fatalf("desktop login of %s", email)
	}
	_, hs, os, err := connectPair(t, e.testEnv, tokenOp(e.testEnv, r.Token))
	if err != nil {
		t.Fatal(err)
	}
	return hs, os
}

func (e *acctEnv) reports(t *testing.T, f accounts.ReportFilter) []accounts.ReportRecord {
	t.Helper()
	list, _, err := e.store.Reports(context.Background(), f, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A report must carry the token the server gave the host of that very
// session: forged, swapped, operator-side and replayed reports are refused.
func TestReportBoundToSession(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()
	hs, os := e.operatorSession(t, opEmail, opPass)
	hs2, os2 := e.operatorSession(t, opEmail, opPass)
	tok, tok2 := hs.ReportToken(), hs2.ReportToken()
	if tok == "" || tok2 == "" || tok == tok2 || !strings.HasPrefix(tok, "rpt1.") {
		t.Fatalf("host report tokens %q %q", tok, tok2)
	}
	if os.ReportToken() != "" || os2.ReportToken() != "" {
		t.Fatal("the operator side got a report token")
	}
	if hs.SID() != os.SID() {
		t.Fatal("sid mismatch")
	}

	// Forgeries: an unknown session, a wrong token, the token of another
	// session, a token without its session.
	for _, c := range []struct{ sid, tok string }{
		{strings.Repeat("ab", 16), tok},
		{hs.SID(), "rpt1.forged-token"},
		{hs.SID(), tok2},
		{hs2.SID(), tok},
	} {
		if err := e.report(t, c.sid, c.tok, wire.ReportScam, "", false, ""); !errors.Is(err, sl.ErrReportInvalid) {
			t.Errorf("forged report %s/%s: %v", c.sid[:6], c.tok[:6], err)
		}
	}
	// Malformed requests.
	for _, body := range []string{
		`{`, `{"sid":"x","token":"t","reason":"scam"}`,
		fmt.Sprintf(`{"sid":%q,"token":"","reason":"scam"}`, hs.SID()),
		fmt.Sprintf(`{"sid":%q,"token":%q,"reason":"spam"}`, hs.SID(), tok),
	} {
		if code, m := e.rawReport(t, "POST", body); code != 400 || m["error"] != "bad_request" {
			t.Errorf("%s: %d %v", body, code, m)
		}
	}
	if code, _ := e.rawReport(t, "GET", ""); code != 405 {
		t.Errorf("GET: %d", code)
	}
	if n := len(e.reports(t, accounts.ReportFilter{})); n != 0 {
		t.Fatalf("%d reports stored by forgeries", n)
	}

	// The real report, during the session, ending it.
	text := "He asked for my bank card\r\nand my password\x07 " + strings.Repeat("x", 1200)
	if err := e.report(t, hs.SID(), tok, wire.ReportScam, text, true, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hs.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end after the report")
	}
	select {
	case <-os.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the operator's session did not end")
	}
	// Once per session.
	if err := e.report(t, hs.SID(), tok, wire.ReportOther, "", false, ""); !errors.Is(err, sl.ErrAlreadyReported) {
		t.Errorf("second report: %v", err)
	}
	list := e.reports(t, accounts.ReportFilter{})
	if len(list) != 1 {
		t.Fatalf("%d reports", len(list))
	}
	r := list[0]
	if r.SID != hs.SID() || r.AccountID != op.ID || r.Reason != wire.ReportScam || r.Status != accounts.ReportOpen ||
		!regexp.MustCompile(`^[1-9]\d\d\*{6}$`).MatchString(r.HostTID) || r.ReporterIP != "127.0.0.1" || r.ReporterKey == "" ||
		!strings.Contains(r.Operator, opEmail) || r.Accepted.IsZero() {
		t.Errorf("report %+v", r)
	}
	if !strings.HasPrefix(r.Text, "He asked for my bank card\nand my password ") || len([]rune(r.Text)) != wire.ReportMaxText {
		t.Errorf("text not cleaned / cut: %d runes %q", len([]rune(r.Text)), r.Text[:40])
	}
	// The session history row is linked (sid) and says why it ended; the
	// report gets the end of the session.
	waitFor(t, func() bool {
		h, err := e.store.SessionBySID(ctx, hs.SID())
		return err == nil && h.EndReason == "reported" && h.ReportHash != ""
	})
	waitFor(t, func() bool { rr, _ := e.store.Report(ctx, r.ID); return !rr.Ended.IsZero() })
	if s, _ := e.store.Sessions(ctx, op.ID, 10, 0); len(s) == 0 || s[0].ReportID != r.ID {
		t.Errorf("history not linked to the report: %+v", s)
	}
	if !strings.Contains(e.logs.String(), `"event":"report"`) || strings.Contains(e.logs.String(), "bank card") {
		t.Error("report log line missing, or the report text was logged")
	}
	if v := e.metric(t, `rdv_reports_total{result="ok_scam"}`); v != "1" {
		t.Errorf("metric ok_scam = %q", v)
	}
	if v := e.metric(t, `rdv_reports_total{result="invalid"}`); v != "4" {
		t.Errorf("metric invalid = %q", v)
	}
}

// After the end of a session the host may still report, for ReportWindow;
// after a restart (tickets lost) the history row carries the token hash.
func TestReportAfterEndAndWindow(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.ReportWindow = 2 * time.Second })
	e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()

	// Ended by the operator, reported after.
	hs, os := e.operatorSession(t, opEmail, opPass)
	os.Close()
	<-hs.Done()
	waitFor(t, func() bool { _, err := e.store.SessionBySID(ctx, hs.SID()); return err == nil })
	if err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportUnwanted, "", true, ""); err != nil {
		t.Fatalf("report after the end: %v", err)
	}
	r := e.reports(t, accounts.ReportFilter{})[0]
	if r.Ended.IsZero() || r.Reason != wire.ReportUnwanted {
		t.Errorf("report of an ended session %+v", r)
	}

	// Restart: the memory is gone, the history answers.
	hs2, os2 := e.operatorSession(t, opEmail, opPass)
	os2.Close()
	<-hs2.Done()
	waitFor(t, func() bool { _, err := e.store.SessionBySID(ctx, hs2.SID()); return err == nil })
	e.srv.hub.dropTickets()
	if err := e.report(t, hs2.SID(), "rpt1.wrong", wire.ReportOther, "", false, ""); !errors.Is(err, sl.ErrReportInvalid) {
		t.Errorf("wrong token through the history: %v", err)
	}
	if err := e.report(t, hs2.SID(), hs2.ReportToken(), wire.ReportOther, "", false, ""); err != nil {
		t.Fatalf("report through the history: %v", err)
	}

	// Window over: refused, in memory and through the history.
	hs3, os3 := e.operatorSession(t, opEmail, opPass)
	hs4, os4 := e.operatorSession(t, opEmail, opPass)
	os3.Close()
	os4.Close()
	<-hs3.Done()
	<-hs4.Done()
	waitFor(t, func() bool {
		_, err1 := e.store.SessionBySID(ctx, hs3.SID())
		_, err2 := e.store.SessionBySID(ctx, hs4.SID())
		return err1 == nil && err2 == nil
	})
	time.Sleep(2500 * time.Millisecond)
	if err := e.report(t, hs3.SID(), hs3.ReportToken(), wire.ReportOther, "", false, ""); !errors.Is(err, sl.ErrReportInvalid) {
		t.Errorf("after the window (memory): %v", err)
	}
	e.srv.hub.dropTickets()
	if err := e.report(t, hs4.SID(), hs4.ReportToken(), wire.ReportOther, "", false, ""); !errors.Is(err, sl.ErrReportInvalid) {
		t.Errorf("after the window (history): %v", err)
	}
	if n := len(e.reports(t, accounts.ReportFilter{})); n != 2 {
		t.Errorf("%d reports", n)
	}
	// The sweep forgets ended tickets after the window.
	e.srv.hub.sweep(time.Now())
	e.srv.hub.mu.Lock()
	n := len(e.srv.hub.tickets)
	e.srv.hub.mu.Unlock()
	if n != 0 {
		t.Errorf("%d tickets left after the window", n)
	}
}

func TestReportRateLimit(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.Limits.ReportPerHourIP, c.Limits.ReportBurstIP = 1, 2 })
	e.mkAccount(t, opEmail, "Op One", opPass, false)
	hs, _ := e.operatorSession(t, opEmail, opPass)
	for i := 0; i < 2; i++ {
		if err := e.report(t, hs.SID(), "rpt1.wrong", wire.ReportOther, "", false, ""); !errors.Is(err, sl.ErrReportInvalid) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportOther, "", false, "")
	var se *sl.ServerError
	if !errors.Is(err, sl.ErrRateLimited) || !errors.As(err, &se) || se.Retry <= 0 {
		t.Fatalf("third report from the same address: %v", err)
	}
	if n := len(e.reports(t, accounts.ReportFilter{})); n != 0 {
		t.Errorf("%d reports past the limit", n)
	}
}

// Without the account database there are no reports: no token, 404.
func TestReportsOffWithoutAccounts(t *testing.T) {
	e := newEnv(t, nil)
	_, hs, os, _, _ := pair(t, e, loopback, loopback)
	defer os.Close()
	if hs.ReportToken() != "" {
		t.Fatal("report token without an account database")
	}
	if err := e.report(t, hs.SID(), "rpt1.x", wire.ReportScam, "", false, ""); !errors.Is(err, sl.ErrReportsDisabled) {
		t.Fatalf("report: %v", err)
	}
}

// Static operator keys are reported by their label.
func TestReportStaticKey(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.OperatorKeys = []OperatorKey{{Label: testAccount, Secret: testKey}} })
	_, hs, os, err := connectPair(t, e.testEnv, opConfig(e.testEnv, loopback, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Close()
	if err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportHarassment, "rude", false, ""); err != nil {
		t.Fatal(err)
	}
	r := e.reports(t, accounts.ReportFilter{KeyLabel: testAccount})
	if len(r) != 1 || r[0].AccountID != 0 || r[0].KeyLabel != testAccount || r[0].Operator != testAccount {
		t.Fatalf("%+v", r)
	}
}

// Distinct reporters within the window disable the account pending review;
// the same reporter twice counts once; reports closed as not abuse do not
// count.
func TestReportAutoDisable(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.TrustProxyHeaders, c.ReportAutoDisable = true, 3 })
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()
	ob := newBrowser(t)
	if code, _, _ := e.webLogin(t, ob, opEmail, opPass, nil); code != 303 {
		t.Fatal("operator web login")
	}
	send := func(ip string) {
		t.Helper()
		hs, _ := e.operatorSession(t, opEmail, opPass)
		if err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportScam, "", false, ip); err != nil {
			t.Fatal(err)
		}
	}
	send("203.0.113.1")
	send("203.0.113.1") // same reporter
	send("2001:db8::1")
	if a, _ := e.store.ByID(ctx, op.ID); a.Disabled {
		t.Fatal("disabled with 2 distinct reporters")
	}
	// A report closed as not abuse does not count.
	first := e.reports(t, accounts.ReportFilter{AccountID: op.ID})
	if err := e.store.SetReportStatus(ctx, first[0].ID, accounts.ReportNotAbuse, "test"); err != nil { // 2001:db8::1
		t.Fatal(err)
	}
	send("2001:db8::2") // same /64 as the closed one: still the same reporter
	if a, _ := e.store.ByID(ctx, op.ID); a.Disabled {
		t.Fatal("an IPv6 address of the same /64 counted as a new reporter")
	}
	// The third distinct reporter: an open session ends with the account.
	hs, os := e.operatorSession(t, opEmail, opPass)
	if err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportUnwanted, "", false, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	a, _ := e.store.ByID(ctx, op.ID)
	if !a.Disabled || a.Review.IsZero() || a.Banned {
		t.Fatalf("account after 3 reporters: %+v", a)
	}
	select {
	case <-os.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the operator's session did not end")
	}
	entries, _, _ := e.store.AuditLog(ctx, 10, 0)
	if len(entries) == 0 || entries[0].Action != "auto_disable" || entries[0].Actor != "system" || entries[0].TargetID != op.ID ||
		!strings.Contains(entries[0].Detail, "reporters=3") {
		t.Fatalf("audit %+v", entries)
	}
	// Desktop and web sign-ins are refused; the web page tells the owner
	// the account is under review (the right password only).
	if c, _ := e.login(t, opEmail, opPass); c != 401 {
		t.Errorf("desktop login of an account under review: %d", c)
	}
	if code, _, _ := ob.do("GET", e.base+"/account", nil, nil); code != 303 {
		t.Error("web session survived the auto-disable")
	}
	b := newBrowser(t)
	if code, page, _ := e.webLogin(t, b, opEmail, opPass, nil); code != 403 || !strings.Contains(page, "being reviewed") || strings.Contains(page, "198.51") {
		t.Errorf("review notice: %d", code)
	}
	if code, page, _ := e.webLogin(t, b, opEmail, "wrong password here", nil); code != 401 || strings.Contains(page, "being reviewed") {
		t.Errorf("wrong password on an account under review: %d", code)
	}
	if v := e.metric(t, `rdv_reports_total{result="auto_disabled"}`); v != "1" {
		t.Errorf("metric auto_disabled = %q", v)
	}
	// Enabling lifts the review.
	if err := e.store.SetDisabledID(ctx, op.ID, false); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.store.ByID(ctx, op.ID); a.Disabled || !a.Review.IsZero() {
		t.Fatalf("after enabling: %+v", a)
	}
}

func TestReportAutoDisableOff(t *testing.T) {
	e := newAcctEnv(t, false, func(c *Config) { c.TrustProxyHeaders = true })
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	for i := 1; i <= 4; i++ {
		hs, _ := e.operatorSession(t, opEmail, opPass)
		if err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportScam, "", false, fmt.Sprintf("203.0.113.%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if a, _ := e.store.ByID(context.Background(), op.ID); a.Disabled {
		t.Fatal("disabled with the protection off")
	}
}

// The admin console: list, detail, actions with CSRF and confirmation,
// notes, ban from the report page, dashboard counter, account badges; the
// operator's own pages never show the report.
func TestReportAdminPages(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	ab, _, _ := e.adminBrowser(t)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()
	ob := newBrowser(t)
	e.webLogin(t, ob, opEmail, opPass, nil)

	hs, _ := e.operatorSession(t, opEmail, opPass)
	if err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportScam, "Asked <b>my</b> card number", true, ""); err != nil {
		t.Fatal(err)
	}
	<-hs.Done()
	rep := e.reports(t, accounts.ReportFilter{})[0]
	waitFor(t, func() bool { _, err := e.store.SessionBySID(ctx, hs.SID()); return err == nil })
	detail := fmt.Sprintf("/admin/reports/%d", rep.ID)

	// Access: admins only.
	if code, _, _ := ob.do("GET", e.base+"/admin/reports", nil, nil); code != 403 {
		t.Errorf("operator on the reports page: %d", code)
	}
	if code, _, _ := newBrowser(t).do("GET", e.base+detail, nil, nil); code != 303 {
		t.Errorf("anonymous on a report: %d", code)
	}
	// Dashboard counter, list, account badges.
	if _, page, _ := ab.do("GET", e.base+"/admin", nil, nil); !strings.Contains(page, "Open reports") || !strings.Contains(page, `<a href="/admin/reports">`) {
		t.Error("dashboard tile")
	}
	code, page, _ := ab.do("GET", e.base+"/admin/reports", nil, nil)
	if code != 200 || !strings.Contains(page, detail) || !strings.Contains(page, "scam or fraud") || !strings.Contains(page, "Asked &lt;b&gt;my&lt;/b&gt; card number") {
		t.Fatalf("list: %d", code)
	}
	if _, page, _ := ab.do("GET", e.base+"/admin/reports?status=closed", nil, nil); strings.Contains(page, detail) {
		t.Error("open report in the closed tab")
	}
	if _, page, _ := ab.do("GET", e.base+"/admin/accounts", nil, nil); !strings.Contains(page, "reported (1)") {
		t.Error("account row badge")
	}
	if _, page, _ := ab.do("GET", e.base+fmt.Sprintf("/admin/accounts/%d", op.ID), nil, nil); !strings.Contains(page, "Reports about this account") || !strings.Contains(page, detail) {
		t.Error("account detail reports")
	}
	if _, page, _ := ab.do("GET", e.base+"/admin/history", nil, nil); !strings.Contains(page, `class="pill red" href="`+detail+`"`) {
		t.Error("history row not linked to the report")
	}
	// Detail: the session, the reporter's address, the text escaped.
	code, page, _ = ab.do("GET", e.base+detail, nil, nil)
	if code != 200 || !strings.Contains(page, hs.SID()) || !strings.Contains(page, "127.0.0.1") || !strings.Contains(page, "ended after a report") ||
		!strings.Contains(page, "&lt;b&gt;my&lt;/b&gt;") || !strings.Contains(page, "Op One") {
		t.Fatalf("detail: %d", code)
	}

	// Without CSRF: refused.
	if code, _, _ := ab.do("POST", e.base+detail, mapForm("op", "handled", "confirmed", "1"), nil); code != 403 {
		t.Errorf("POST without CSRF: %d", code)
	}
	if code, _, _ := e.post(t, ob, "/account", detail, "op", "handled", "confirmed", "1"); code != 403 {
		t.Errorf("operator POST: %d", code)
	}
	// Close as handled: confirmation, then done + audit.
	code, page, _ = e.post(t, ab, detail, detail, "op", "handled")
	if code != 200 || !strings.Contains(page, `name="confirmed" value="1"`) {
		t.Fatalf("confirmation: %d", code)
	}
	if code, _, h := ab.do("POST", e.base+detail, fields(page), nil); code != 303 || loc(h) != detail+"?done=report_handled" {
		t.Fatalf("close: %d %q", code, loc(h))
	}
	if r, _ := e.store.Report(ctx, rep.ID); r.Status != accounts.ReportHandled || r.ClosedBy != adminEmail || r.Closed.IsZero() {
		t.Fatalf("closed report %+v", r)
	}
	// Note.
	if code, _, _ := e.post(t, ab, detail, detail, "op", "note", "note", "   "); code != 422 {
		t.Errorf("empty note: %d", code)
	}
	if code, _, h := e.post(t, ab, detail, detail, "op", "note", "note", "Called the person back."); code != 303 || loc(h) != detail+"?done=report_note" {
		t.Fatalf("note: %d", code)
	}
	if _, page, _ := ab.do("GET", e.base+detail+"?done=report_note", nil, nil); !strings.Contains(page, "Called the person back.") || !strings.Contains(page, "Note added.") {
		t.Error("note not shown")
	}
	// Reopen.
	_, page, _ = e.post(t, ab, detail, detail, "op", "reopen")
	if code, _, _ := ab.do("POST", e.base+detail, fields(page), nil); code != 303 {
		t.Fatal("reopen")
	}
	// Ban from the report page: through the account action, back here.
	_, page, _ = e.post(t, ab, detail, fmt.Sprintf("/admin/accounts/%d", op.ID), "op", "ban", "ret", detail)
	if !strings.Contains(page, `name="ret" value="`+detail+`"`) || !strings.Contains(page, "can no longer register") {
		t.Fatal("ban confirmation")
	}
	if code, _, h := ab.do("POST", e.base+fmt.Sprintf("/admin/accounts/%d", op.ID), fields(page), nil); code != 303 || loc(h) != detail+"?done=ban" {
		t.Fatalf("ban: %d %q", code, loc(h))
	}
	a, _ := e.store.ByID(ctx, op.ID)
	if !a.Disabled || !a.Banned {
		t.Fatalf("banned account %+v", a)
	}
	if banned, _ := e.store.EmailBanned(ctx, opEmail); !banned {
		t.Fatal("address not banned")
	}
	// The address cannot register again once the account is deleted.
	if err := e.store.DeleteID(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if code, page := e.register(t, newBrowser(t), "Again", opEmail, "another long passphrase"); code != 422 || !strings.Contains(page, "cannot be used") {
		t.Errorf("register with a banned address: %d", code)
	}
	// The report stays (operator label kept, account gone).
	if r, err := e.store.Report(ctx, rep.ID); err != nil || r.AccountID != 0 || !strings.Contains(r.Operator, opEmail) {
		t.Errorf("report after the account deletion: %+v %v", r, err)
	}
	entries, _, _ := e.store.AuditLog(ctx, 20, 0)
	var actions []string
	for _, en := range entries {
		actions = append(actions, en.Action)
	}
	for _, want := range []string{"report_handled", "report_note", "report_reopen", "ban"} {
		if !strings.Contains(strings.Join(actions, ","), want) {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
	if _, page, _ := ab.do("GET", e.base+"/admin/audit", nil, nil); !strings.Contains(page, "report closed (handled)") || !strings.Contains(page, "banned") {
		t.Error("audit page labels")
	}
}

// The operator's own pages do not reveal reports: the reported session
// reads "ended", nothing about the reporter.
func TestReportHiddenFromOperator(t *testing.T) {
	e := newAcctEnv(t, false, nil)
	op := e.mkAccount(t, opEmail, "Op One", opPass, false)
	ctx := context.Background()
	ob := newBrowser(t)
	e.webLogin(t, ob, opEmail, opPass, nil)
	hs, _ := e.operatorSession(t, opEmail, opPass)
	if err := e.report(t, hs.SID(), hs.ReportToken(), wire.ReportScam, "secret text", true, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s, _ := e.store.Sessions(ctx, op.ID, 5, 0); return len(s) == 1 })
	code, page, _ := ob.do("GET", e.base+"/account", nil, nil)
	if code != 200 || strings.Contains(page, "reported") || strings.Contains(page, "Report") || strings.Contains(page, "secret text") || strings.Contains(page, "127.0.0.1") {
		t.Fatalf("operator overview reveals the report: %d", code)
	}
	if !strings.Contains(page, ">ended<") {
		t.Error("the reported session should read as ended")
	}
}

func mapForm(kv ...string) map[string][]string {
	m := map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = []string{kv[i+1]}
	}
	return m
}
