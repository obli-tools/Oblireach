package supportlink

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oblireach/supportlink/wire"
)

// Report is an abuse report about the operator of a session, sent by the
// person helped (the host side) with the session's ReportToken.
type Report struct {
	SID   string // Session.SID
	Token string // Session.ReportToken
	// Reason is one of wire.ReportReasons.
	Reason string
	// Text is optional; it is cleaned and cut to wire.ReportMaxText
	// characters (wire.CleanReportText).
	Text string
	// EndSession asks the server to end the session at once when it still
	// runs (the client should also end it locally).
	EndSession bool
}

// Report refusals (errors.Is). Rate limits match ErrRateLimited (Retry in
// the *ServerError).
var (
	// ErrReportInvalid: the server does not know this session and token,
	// or the report window is over.
	ErrReportInvalid = &ServerError{Code: wire.ErrInvalidReport}
	// ErrAlreadyReported: this session was already reported.
	ErrAlreadyReported = &ServerError{Code: wire.ErrAlreadyReported}
	// ErrReportsDisabled: the server does not take reports.
	ErrReportsDisabled = &ServerError{Code: wire.ErrReportsDisabled}
)

// reportTimeout bounds one report request (connection included).
const reportTimeout = 30 * time.Second

// SendReport sends an abuse report to the rendezvous server (HTTPS POST to
// wire.ReportPath on the signalling host, through the same proxy and TLS
// settings as the signalling). It may be called after the session ended:
// the server accepts reports for a while after the end of the session.
// Nothing is written to disk.
func SendReport(ctx context.Context, server string, o SignalOptions, r Report) error {
	r.Text = wire.CleanReportText(r.Text)
	if r.SID == "" || r.Token == "" || len(r.SID) > 64 || len(r.Token) > 128 {
		return fmt.Errorf("%w: no report token for this session", ErrReportInvalid)
	}
	if !wire.ValidReportReason(r.Reason) {
		return fmt.Errorf("supportlink: invalid report reason %q", r.Reason)
	}
	u, err := reportURL(server, o.AllowPlainWS)
	if err != nil {
		return err
	}
	proxy, err := resolveProxy(o.Proxy)
	if err != nil {
		return err
	}
	scheme := "https"
	if strings.HasPrefix(u, "http:") {
		scheme = "http"
	}
	var tc *tls.Config
	if o.TLSConfig != nil {
		tc = o.TLSConfig.Clone()
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialVia(ctx, proxy, network, proxyTarget(scheme, addr), addr)
		},
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableKeepAlives:     true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   reportTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	body, err := json.Marshal(wire.ReportRequest{SID: r.SID, Token: r.Token, Reason: r.Reason, Text: r.Text, End: r.EndSession})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range o.Header {
		req.Header[k] = append([]string(nil), v...)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &e)
	code := cleanDetail(e.Error)
	if code == "" {
		// Not an answer of the report endpoint: an older server, a proxy
		// error page.
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
			return ErrReportsDisabled
		case resp.StatusCode >= 500:
			return fmt.Errorf("%w: HTTP %d", ErrUnreachable, resp.StatusCode)
		}
		code = "http_" + strconv.Itoa(resp.StatusCode)
	}
	if code == wire.ErrUnavailable {
		return fmt.Errorf("%w: the server is temporarily unavailable", ErrUnreachable)
	}
	se := &ServerError{Code: code}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 86400 {
		se.Retry = time.Duration(s) * time.Second
	}
	return se
}

// reportURL is the report endpoint on the host of the signalling URL:
// "rdv.example.com" -> https://rdv.example.com/v1/report; a signalling path
// behind a prefix keeps the prefix.
func reportURL(server string, allowPlain bool) (string, error) {
	s, err := signalURL(server, allowPlain)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidServer, err)
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return "", errors.New("supportlink: unsupported scheme " + u.Scheme)
	}
	if strings.HasSuffix(u.Path, wire.Path) {
		u.Path = strings.TrimSuffix(u.Path, wire.Path) + wire.ReportPath
	} else {
		u.Path = wire.ReportPath
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// cleanReportToken keeps a report token received from the network when it
// looks like one (printable token characters, bounded), "" otherwise.
func cleanReportToken(t string) string {
	if len(t) == 0 || len(t) > 128 {
		return ""
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return ""
		}
	}
	return t
}
