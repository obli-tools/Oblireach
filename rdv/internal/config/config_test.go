package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oblireach/rdv/internal/server"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func base() map[string]string {
	return map[string]string{
		"RDV_TLS":           "off",
		"RDV_PUBLIC_IP":     "203.0.113.10",
		"RDV_OPERATOR_KEYS": "helpdesk:0123456789abcdef0123",
		"RDV_TURN_SECRET":   "0123456789abcdef0123",
	}
}

func TestLoadDefaults(t *testing.T) {
	st, err := Load(envOf(base()))
	if err != nil {
		t.Fatal(err)
	}
	c := st.Server
	if st.Listen != ":8080" || st.AdminListen != "127.0.0.1:9100" || st.Mode != "private" {
		t.Fatalf("listen %q admin %q mode %q", st.Listen, st.AdminListen, st.Mode)
	}
	if c.TURNPort != 3478 || c.RelayMinPort != 49160 || c.RelayMaxPort != 49999 {
		t.Fatalf("ports %d %d-%d", c.TURNPort, c.RelayMinPort, c.RelayMaxPort)
	}
	if c.RelayMaxKbps != 8000 || c.RelayMaxDuration != 240*time.Minute || c.RelayMaxConcurrent != 300 {
		t.Fatalf("relay caps %+v", c)
	}
	if c.ICEHost != "" || c.PublicIP.String() != "203.0.113.10" || c.LogIPMode != "full" {
		t.Fatalf("ice host %q ip %s", c.ICEHost, c.PublicIP)
	}
	if len(c.OperatorKeys) != 1 || c.OperatorKeys[0].Label != "helpdesk" {
		t.Fatalf("keys %+v", c.OperatorKeys)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, mut := range map[string]func(m map[string]string){
		"bad mode":      func(m map[string]string) { m["RDV_MODE"] = "open" },
		"acme no host":  func(m map[string]string) { m["RDV_TLS"] = "acme" },
		"bad tls":       func(m map[string]string) { m["RDV_TLS"] = "maybe" },
		"no keys":       func(m map[string]string) { delete(m, "RDV_OPERATOR_KEYS") },
		"short key":     func(m map[string]string) { m["RDV_OPERATOR_KEYS"] = "short" },
		"bad ports":     func(m map[string]string) { m["RDV_RELAY_PORTS"] = "5000-4000" },
		"bad ip":        func(m map[string]string) { m["RDV_PUBLIC_IP"] = "nope" },
		"no ip":         func(m map[string]string) { delete(m, "RDV_PUBLIC_IP") },
		"short secret":  func(m map[string]string) { m["RDV_TURN_SECRET"] = "x" },
		"bad ip mode":   func(m map[string]string) { m["RDV_LOG_IP_MODE"] = "partial" },
		"bad kbps":      func(m map[string]string) { m["RDV_RELAY_MAX_KBPS_SESSION"] = "-1" },
		"bad log level": func(m map[string]string) { m["RDV_LOG_LEVEL"] = "loud" },
	} {
		m := base()
		mut(m)
		if _, err := Load(envOf(m)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseOperatorKeys(t *testing.T) {
	keys, err := ParseOperatorKeys("# team\nalice:aaaaaaaaaaaaaaaa1, bbbbbbbbbbbbbbbb2\n\n team-b : cccccccccccccccc3 ;")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0].Label != "alice" || keys[1].Label != "key-2" || keys[2].Label != "team-b" ||
		keys[2].Secret != "cccccccccccccccc3" {
		t.Fatalf("%+v", keys)
	}
	if _, err := ParseOperatorKeys("a:aaaaaaaaaaaaaaaa1,b:aaaaaaaaaaaaaaaa1"); err == nil {
		t.Fatal("duplicate keys must be refused")
	}
	if _, err := ParseOperatorKeys("bad label:aaaaaaaaaaaaaaaa1"); err == nil || !strings.Contains(err.Error(), "label") {
		t.Fatalf("bad label: %v", err)
	}
}

func TestTurnSecretPersisted(t *testing.T) {
	dir := t.TempDir()
	a, err := turnSecret("", dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := turnSecret("", dir)
	if a != b || len(a) < 32 {
		t.Fatal("generated secret must be persisted and reused")
	}
}

func TestTURNTLSAndIdle(t *testing.T) {
	m := base()
	st, err := Load(envOf(m))
	if err != nil {
		t.Fatal(err)
	}
	if st.Server.TURNTLSPort != 0 || st.Server.RelayIdleTimeout != 20*time.Second {
		t.Fatalf("tls off: turns port %d idle %v", st.Server.TURNTLSPort, st.Server.RelayIdleTimeout)
	}
	m["RDV_TURNS"] = "on"
	if _, err := Load(envOf(m)); err == nil {
		t.Fatal("RDV_TURNS=on accepted with RDV_TLS=off")
	}

	m = base()
	m["RDV_TLS"], m["RDV_DOMAIN"] = "acme", "rdv.example.com"
	if st, err = Load(envOf(m)); err != nil {
		t.Fatal(err)
	}
	if st.Server.TURNTLSPort != 443 || st.Server.TURNTLSHost != "rdv.example.com" {
		t.Fatalf("acme: turns %s:%d", st.Server.TURNTLSHost, st.Server.TURNTLSPort)
	}
	m["RDV_LISTEN"], m["RDV_TURNS_PORT"], m["RDV_RELAY_IDLE_SECONDS"] = ":8443", "443", "0"
	if st, err = Load(envOf(m)); err != nil {
		t.Fatal(err)
	}
	if st.Server.TURNTLSPort != 443 || st.Server.RelayIdleTimeout >= 0 {
		t.Fatalf("mapped port: turns %d idle %v", st.Server.TURNTLSPort, st.Server.RelayIdleTimeout)
	}
	delete(m, "RDV_TURNS_PORT")
	if st, _ = Load(envOf(m)); st.Server.TURNTLSPort != 8443 {
		t.Fatalf("listen port: turns %d", st.Server.TURNTLSPort)
	}
	m["RDV_TURNS"] = "off"
	if st, _ = Load(envOf(m)); st.Server.TURNTLSPort != 0 {
		t.Fatal("RDV_TURNS=off ignored")
	}

	// files without a name: TURN-TLS silently off, unless asked for.
	m = base()
	m["RDV_TLS"] = "files"
	if st, err = Load(envOf(m)); err != nil || st.Server.TURNTLSPort != 0 {
		t.Fatalf("files without a name: %v %d", err, st.Server.TURNTLSPort)
	}
	m["RDV_TURNS"] = "on"
	if _, err := Load(envOf(m)); err == nil {
		t.Fatal("TURN-TLS without a host name accepted")
	}
	m["RDV_TURNS_HOST"] = "relay.example.com"
	if st, err = Load(envOf(m)); err != nil || st.Server.TURNTLSHost != "relay.example.com" || st.Server.TURNTLSPort != 443 {
		t.Fatalf("files with RDV_TURNS_HOST: %v %+v", err, st.Server)
	}
}

func TestModesAndAccounts(t *testing.T) {
	// Private: accounts off by default, keys required, no quota.
	st, err := Load(envOf(base()))
	if err != nil || st.Accounts || st.Server.Mode != "private" || st.Server.Quotas != (server.AccountQuotas{}) {
		t.Fatalf("private defaults: %v %+v", err, st)
	}

	// Public: accounts on, quotas on, keys optional.
	m := base()
	m["RDV_MODE"] = "public"
	delete(m, "RDV_OPERATOR_KEYS")
	m["RDV_DATA_DIR"] = t.TempDir()
	if st, err = Load(envOf(m)); err != nil {
		t.Fatal(err)
	}
	q := st.Server.Quotas
	if !st.Accounts || st.SMTP != nil || q.MaxConcurrent != 3 || q.SessionsPerDay != 50 || q.RelayBytesMonth != 20_000_000_000 {
		t.Fatalf("public defaults: %+v %+v", st, q)
	}
	if st.AccountsDB() != filepath.Join(m["RDV_DATA_DIR"], "accounts.db") {
		t.Fatalf("db path %s", st.AccountsDB())
	}
	m["RDV_ACCOUNT_MAX_SESSIONS"], m["RDV_ACCOUNT_SESSIONS_DAY"], m["RDV_ACCOUNT_RELAY_GB_MONTH"] = "5", "0", "1"
	if st, err = Load(envOf(m)); err != nil || st.Server.Quotas != (server.AccountQuotas{MaxConcurrent: 5, RelayBytesMonth: 1e9}) {
		t.Fatalf("quotas: %v %+v", err, st.Server.Quotas)
	}

	// Public without accounts and without keys: nobody could connect.
	m["RDV_ACCOUNTS"] = "false"
	if _, err := Load(envOf(m)); err == nil {
		t.Fatal("no keys and no accounts accepted")
	}

	// Private with accounts: keys optional.
	m = base()
	delete(m, "RDV_OPERATOR_KEYS")
	m["RDV_ACCOUNTS"], m["RDV_DATA_DIR"] = "true", t.TempDir()
	if st, err = Load(envOf(m)); err != nil || !st.Accounts || st.Server.Quotas != (server.AccountQuotas{}) {
		t.Fatalf("private with accounts: %v", err)
	}
}

func TestSMTPAndPublicURL(t *testing.T) {
	m := base()
	m["RDV_MODE"], m["RDV_DATA_DIR"] = "public", t.TempDir()
	m["RDV_SMTP_HOST"] = "smtp.example.com"
	if _, err := Load(envOf(m)); err == nil || !strings.Contains(err.Error(), "RDV_SMTP_FROM") {
		t.Fatalf("missing from: %v", err)
	}
	m["RDV_SMTP_FROM"] = "Oblireach <noreply@example.com>"
	if _, err := Load(envOf(m)); err == nil || !strings.Contains(err.Error(), "RDV_PUBLIC_URL") {
		t.Fatalf("SMTP without a public URL: %v", err)
	}
	m["RDV_DOMAIN"] = "rdv.example.com"
	st, err := Load(envOf(m))
	if err != nil {
		t.Fatal(err)
	}
	if st.SMTP == nil || st.SMTP.TLS != "starttls" || st.Server.PublicURL != "https://rdv.example.com" {
		t.Fatalf("smtp %+v url %q", st.SMTP, st.Server.PublicURL)
	}
	m["RDV_SMTP_TLS"] = "ssl"
	if _, err := Load(envOf(m)); err == nil {
		t.Fatal("bad SMTP TLS mode accepted")
	}
	m["RDV_SMTP_TLS"], m["RDV_PUBLIC_URL"] = "tls", "https://support.example.com/rdv/"
	if st, err = Load(envOf(m)); err != nil || st.Server.PublicURL != "https://support.example.com/rdv" {
		t.Fatalf("public url: %v %q", err, st.Server.PublicURL)
	}
	m["RDV_PUBLIC_URL"] = "ftp://x"
	if _, err := Load(envOf(m)); err == nil {
		t.Fatal("bad public URL accepted")
	}

	// A non-standard HTTPS port goes into the derived URL.
	m = base()
	m["RDV_TLS"], m["RDV_DOMAIN"], m["RDV_LISTEN"] = "acme", "rdv.example.com", ":8443"
	if st, err = Load(envOf(m)); err != nil || st.Server.PublicURL != "https://rdv.example.com:8443" {
		t.Fatalf("derived url: %v %q", err, st.Server.PublicURL)
	}
	// SNI passthrough in front: loopback listener or PROXY protocol.
	m["RDV_LISTEN"] = "127.0.0.1:8443"
	if st, _ = Load(envOf(m)); st.Server.PublicURL != "https://rdv.example.com" {
		t.Fatalf("loopback listener: %q", st.Server.PublicURL)
	}
	m["RDV_LISTEN"], m["RDV_PROXY_PROTOCOL"] = ":8443", "true"
	if st, _ = Load(envOf(m)); st.Server.PublicURL != "https://rdv.example.com" {
		t.Fatalf("proxy protocol: %q", st.Server.PublicURL)
	}
}

func TestProxySettings(t *testing.T) {
	m := base()
	st, _ := Load(envOf(m))
	if st.ProxyProtocol || st.Server.TrustProxyHeaders {
		t.Fatal("proxy settings on by default")
	}
	m["RDV_PROXY_PROTOCOL"], m["RDV_TRUST_PROXY"] = "true", "true"
	if st, _ = Load(envOf(m)); !st.ProxyProtocol || !st.Server.TrustProxyHeaders {
		t.Fatal("proxy settings ignored")
	}
}

func TestResumeGrace(t *testing.T) {
	m := base()
	if st, _ := Load(envOf(m)); st.Server.ResumeGrace != 10*time.Minute {
		t.Fatalf("default %v", st.Server.ResumeGrace)
	}
	m["RDV_RESUME_GRACE_SECONDS"] = "0"
	if st, _ := Load(envOf(m)); st.Server.ResumeGrace >= 0 {
		t.Fatal("0 must turn resume off")
	}
	m["RDV_RESUME_GRACE_SECONDS"] = "-5"
	if _, err := Load(envOf(m)); err == nil {
		t.Fatal("negative grace accepted")
	}
}

func TestAdminSettings(t *testing.T) {
	dir := t.TempDir()
	m := base()
	m["RDV_ACCOUNTS"], m["RDV_DATA_DIR"] = "true", dir
	st, err := Load(envOf(m))
	if err != nil {
		t.Fatal(err)
	}
	c := st.Server
	if c.AdminTOTPOptional || c.HistoryRetention != 90*24*time.Hour || c.AuditRetention != 365*24*time.Hour || len(c.AdminEmails) != 0 {
		t.Fatalf("defaults %+v", c)
	}
	// The web secret is generated once and kept.
	if len(c.WebSecret) < 32 {
		t.Fatal("no web secret")
	}
	st2, _ := Load(envOf(m))
	if string(st2.Server.WebSecret) != string(c.WebSecret) {
		t.Fatal("web secret not persisted")
	}
	m["RDV_ADMIN_EMAILS"] = " Boss@Example.com, second@example.com;third@example.com "
	m["RDV_ADMIN_REQUIRE_TOTP"] = "false"
	m["RDV_HISTORY_DAYS"] = "30"
	m["RDV_AUDIT_DAYS"] = "730"
	st, err = Load(envOf(m))
	if err != nil {
		t.Fatal(err)
	}
	c = st.Server
	if strings.Join(c.AdminEmails, ",") != "boss@example.com,second@example.com,third@example.com" || !c.AdminTOTPOptional ||
		c.HistoryRetention != 30*24*time.Hour || c.AuditRetention != 730*24*time.Hour {
		t.Fatalf("parsed %+v", c)
	}
	for k, v := range map[string]string{"RDV_ADMIN_EMAILS": "not-an-address", "RDV_HISTORY_DAYS": "0", "RDV_AUDIT_DAYS": "x", "RDV_WEB_SECRET": "short"} {
		mm := base()
		mm["RDV_ACCOUNTS"], mm["RDV_DATA_DIR"] = "true", t.TempDir()
		mm[k] = v
		if _, err := Load(envOf(mm)); err == nil || !strings.Contains(err.Error(), k) {
			t.Fatalf("%s=%q: %v", k, v, err)
		}
	}
	mm := base()
	mm["RDV_ACCOUNTS"], mm["RDV_DATA_DIR"] = "true", t.TempDir()
	mm["RDV_WEB_SECRET"] = strings.Repeat("s", 40)
	if st, err := Load(envOf(mm)); err != nil || string(st.Server.WebSecret) != strings.Repeat("s", 40) {
		t.Fatal("RDV_WEB_SECRET")
	}
	if _, err := filepath.Glob(filepath.Join(dir, "web-secret")); err != nil {
		t.Fatal(err)
	}
}

func TestReportSettings(t *testing.T) {
	m := base()
	m["RDV_ACCOUNTS"], m["RDV_DATA_DIR"] = "true", t.TempDir()
	st, err := Load(envOf(m))
	if err != nil {
		t.Fatal(err)
	}
	c := st.Server
	if c.ReportAutoDisable != 0 || c.ReportAutoDisableWindow != 7*24*time.Hour || c.ReportWindow != time.Hour {
		t.Fatalf("private defaults %d %s %s", c.ReportAutoDisable, c.ReportAutoDisableWindow, c.ReportWindow)
	}
	m["RDV_MODE"] = "public"
	if st, err = Load(envOf(m)); err != nil || st.Server.ReportAutoDisable != 3 {
		t.Fatalf("public default: %v %d", err, st.Server.ReportAutoDisable)
	}
	m["RDV_REPORTS_AUTODISABLE"], m["RDV_REPORTS_AUTODISABLE_DAYS"], m["RDV_REPORT_WINDOW_MINUTES"] = "0", "14", "30"
	if st, err = Load(envOf(m)); err != nil {
		t.Fatal(err)
	}
	c = st.Server
	if c.ReportAutoDisable != 0 || c.ReportAutoDisableWindow != 14*24*time.Hour || c.ReportWindow != 30*time.Minute {
		t.Fatalf("parsed %d %s %s", c.ReportAutoDisable, c.ReportAutoDisableWindow, c.ReportWindow)
	}
	for k, v := range map[string]string{"RDV_REPORTS_AUTODISABLE": "-1", "RDV_REPORTS_AUTODISABLE_DAYS": "0", "RDV_REPORT_WINDOW_MINUTES": "x"} {
		mm := base()
		mm["RDV_ACCOUNTS"], mm["RDV_DATA_DIR"] = "true", t.TempDir()
		mm[k] = v
		if _, err := Load(envOf(mm)); err == nil || !strings.Contains(err.Error(), k) {
			t.Fatalf("%s=%q: %v", k, v, err)
		}
	}
}
