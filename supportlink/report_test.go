package supportlink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oblireach/supportlink/wire"
)

func TestReportURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"rdv.example.com", "https://rdv.example.com/v1/report"},
		{"wss://rdv.example.com:8443", "https://rdv.example.com:8443/v1/report"},
		{"wss://rdv.example.com/v1/ws", "https://rdv.example.com/v1/report"},
		{"wss://example.com/rdv/v1/ws?x=1", "https://example.com/rdv/v1/report"},
		{"ws://127.0.0.1:9000", "http://127.0.0.1:9000/v1/report"},
		{"https://rdv.example.com/custom", "https://rdv.example.com/v1/report"},
	} {
		got, err := reportURL(c.in, false)
		if err != nil || got != c.want {
			t.Errorf("reportURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := reportURL("ws://rdv.example.com", false); !errors.Is(err, ErrInvalidServer) {
		t.Errorf("plain ws to a remote host: %v", err)
	}
}

func TestCleanReportToken(t *testing.T) {
	if cleanReportToken("rpt1.AbC-_09") != "rpt1.AbC-_09" {
		t.Error("valid token dropped")
	}
	for _, bad := range []string{"", "a b", "tok\n", "é", strings.Repeat("a", 129)} {
		if cleanReportToken(bad) != "" {
			t.Errorf("%q kept", bad)
		}
	}
}

func TestCleanReportText(t *testing.T) {
	in := "  Hello\r\nworld\t!\x07‮\n\n\n\nend  "
	if got := wire.CleanReportText(in); got != "Hello\nworld !\n\nend" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("é", wire.ReportMaxText+50)
	if got := wire.CleanReportText(long); len([]rune(got)) != wire.ReportMaxText {
		t.Errorf("not cut: %d runes", len([]rune(got)))
	}
	if !wire.ValidReportReason(wire.ReportScam) || wire.ValidReportReason("spam") {
		t.Error("ValidReportReason")
	}
}

func TestSendReport(t *testing.T) {
	var got wire.ReportRequest
	var ua string
	status, answer := http.StatusOK, `{"ok":true}`
	retry := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wire.ReportPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		ua = r.Header.Get("User-Agent")
		_ = json.NewDecoder(r.Body).Decode(&got)
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	server := "ws://" + strings.TrimPrefix(srv.URL, "http://")
	o := SignalOptions{Proxy: ProxyDirect, Header: http.Header{"User-Agent": {"test/1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := Report{SID: "0123456789abcdef0123456789abcdef", Token: "rpt1.x", Reason: wire.ReportScam, Text: " he asked\x00 for my card ", EndSession: true}
	if err := SendReport(ctx, server, o, r); err != nil {
		t.Fatal(err)
	}
	if got.SID != r.SID || got.Token != r.Token || got.Reason != wire.ReportScam || got.Text != "he asked for my card" || !got.End || ua != "test/1" {
		t.Errorf("request %+v (UA %q)", got, ua)
	}

	for _, c := range []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusForbidden, `{"error":"invalid_report"}`, ErrReportInvalid},
		{http.StatusConflict, `{"error":"already_reported"}`, ErrAlreadyReported},
		{http.StatusTooManyRequests, `{"error":"rate_limited","retryAfter":30}`, ErrRateLimited},
		{http.StatusNotFound, `{"error":"reports_disabled"}`, ErrReportsDisabled},
		{http.StatusNotFound, `<html>not found</html>`, ErrReportsDisabled},
		{http.StatusBadGateway, `<html>bad gateway</html>`, ErrUnreachable},
		{http.StatusServiceUnavailable, `{"error":"unavailable"}`, ErrUnreachable},
	} {
		status, answer, retry = c.status, c.body, "30"
		err := SendReport(ctx, server, o, r)
		if !errors.Is(err, c.want) {
			t.Errorf("%d %s: %v, want %v", c.status, c.body, err, c.want)
		}
		var se *ServerError
		if c.status == http.StatusTooManyRequests && (!errors.As(err, &se) || se.Retry != 30*time.Second) {
			t.Errorf("rate limited: retry %v", err)
		}
	}

	// Refused locally: no token, bad reason.
	if err := SendReport(ctx, server, o, Report{SID: r.SID, Reason: wire.ReportScam}); !errors.Is(err, ErrReportInvalid) {
		t.Errorf("no token: %v", err)
	}
	if err := SendReport(ctx, server, o, Report{SID: r.SID, Token: "t", Reason: "spam"}); err == nil {
		t.Error("bad reason accepted")
	}
	// Server gone.
	srv.Close()
	if err := SendReport(ctx, server, o, r); !errors.Is(err, ErrUnreachable) {
		t.Errorf("closed server: %v", err)
	}
}
