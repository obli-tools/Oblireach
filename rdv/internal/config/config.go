// Package config maps the RDV_* environment variables to the server
// configuration.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oblireach/rdv/internal/accounts"
	"github.com/oblireach/rdv/internal/server"
)

// TLS modes.
const (
	TLSAcme  = "acme"  // Let's Encrypt via TLS-ALPN-01 on the HTTPS port
	TLSFiles = "files" // certificate and key files, reloaded when they change
	TLSOff   = "off"   // plain HTTP: local tests, or behind a TLS reverse proxy
)

// Settings is the full runtime configuration.
type Settings struct {
	Server server.Config

	Mode        string
	Domain      string
	Listen      string // signalling HTTPS (or HTTP when TLS is off)
	AdminListen string // /metrics and /healthz
	TLSMode     string
	CertFile    string
	KeyFile     string
	ACMEEmail   string
	ACMEHTTP    string // optional HTTP-01 listener (e.g. ":80")
	DataDir     string
	LogLevel    slog.Level

	// ProxyProtocol expects a PROXY protocol header (v1 or v2) on every
	// connection to Listen: a TCP / SNI-passthrough proxy in front.
	ProxyProtocol bool

	// Accounts enables operator accounts (database DataDir/accounts.db).
	Accounts bool
	// SMTP is the mail server of the email verification (nil: accounts are
	// active at once, their address unverified).
	SMTP *accounts.SMTPConfig
}

// AccountsDB is the path of the account database.
func (st *Settings) AccountsDB() string { return filepath.Join(st.DataDir, accounts.DBFile) }

var labelRe = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}$`)

// Load reads the configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (*Settings, error) {
	env := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	st := &Settings{
		Mode:        strings.ToLower(env("RDV_MODE", "private")),
		Domain:      env("RDV_DOMAIN", ""),
		TLSMode:     strings.ToLower(env("RDV_TLS", TLSAcme)),
		CertFile:    env("RDV_TLS_CERT", "/certs/fullchain.pem"),
		KeyFile:     env("RDV_TLS_KEY", "/certs/privkey.pem"),
		ACMEEmail:   env("RDV_ACME_EMAIL", ""),
		ACMEHTTP:    env("RDV_ACME_HTTP_LISTEN", ""),
		DataDir:     env("RDV_DATA_DIR", "/data"),
		AdminListen: env("RDV_ADMIN_LISTEN", "127.0.0.1:9100"),
	}
	public := false
	switch st.Mode {
	case server.ModePrivate:
	case server.ModePublic:
		public = true
	default:
		return nil, fmt.Errorf("RDV_MODE=%q: expected private or public", st.Mode)
	}
	st.Server.Mode = st.Mode
	st.Server.Name = env("RDV_NAME", "")
	if len(st.Server.Name) > 64 {
		return nil, errors.New("RDV_NAME: 64 characters at most")
	}
	def := "false"
	if public {
		def = "true"
	}
	st.Accounts = boolEnv(env("RDV_ACCOUNTS", def))
	switch st.TLSMode {
	case TLSAcme:
		if st.Domain == "" {
			return nil, errors.New("RDV_TLS=acme needs RDV_DOMAIN")
		}
		st.Listen = env("RDV_LISTEN", ":443")
	case TLSFiles:
		st.Listen = env("RDV_LISTEN", ":443")
	case TLSOff:
		st.Listen = env("RDV_LISTEN", ":8080")
	default:
		return nil, fmt.Errorf("RDV_TLS=%q: expected acme, files or off", st.TLSMode)
	}
	if err := st.LogLevel.UnmarshalText([]byte(env("RDV_LOG_LEVEL", "info"))); err != nil {
		return nil, fmt.Errorf("RDV_LOG_LEVEL: %w", err)
	}

	c := &st.Server
	var err error
	if c.PublicIP, err = publicIP(env("RDV_PUBLIC_IP", ""), st.Domain); err != nil {
		return nil, err
	}
	c.ICEHost = env("RDV_ICE_HOST", st.Domain)
	if c.TURNPort, err = intEnv(env, "RDV_TURN_PORT", 3478, 1, 65535); err != nil {
		return nil, err
	}
	c.TURNListenIP = env("RDV_TURN_LISTEN_IP", "0.0.0.0")
	if c.RelayMinPort, c.RelayMaxPort, err = portRange(env("RDV_RELAY_PORTS", "49160-49999")); err != nil {
		return nil, err
	}
	kbps, err := intEnv(env, "RDV_RELAY_MAX_KBPS_SESSION", 8000, 0, 10_000_000)
	if err != nil {
		return nil, err
	}
	c.RelayMaxKbps = kbps
	mins, err := intEnv(env, "RDV_RELAY_MAX_MIN_SESSION", 240, 1, 7*24*60)
	if err != nil {
		return nil, err
	}
	c.RelayMaxDuration = time.Duration(mins) * time.Minute
	if c.RelayMaxConcurrent, err = intEnv(env, "RDV_RELAY_MAX_CONCURRENT", 300, 1, 1_000_000); err != nil {
		return nil, err
	}
	idle, err := intEnv(env, "RDV_RELAY_IDLE_SECONDS", 20, 0, 3600)
	if err != nil {
		return nil, err
	}
	c.RelayIdleTimeout = time.Duration(idle) * time.Second
	if idle == 0 {
		c.RelayIdleTimeout = -1 // never
	}
	if c.TURNTLSPort, c.TURNTLSHost, err = turnTLS(env, st); err != nil {
		return nil, err
	}
	c.RelayAllowPrivatePeers = boolEnv(env("RDV_RELAY_ALLOW_PRIVATE", "false"))
	grace, err := intEnv(env, "RDV_RESUME_GRACE_SECONDS", 600, 0, 24*3600)
	if err != nil {
		return nil, err
	}
	c.ResumeGrace = time.Duration(grace) * time.Second
	if grace == 0 {
		c.ResumeGrace = -1 // off
	}
	c.TrustProxyHeaders = boolEnv(env("RDV_TRUST_PROXY", "false"))
	st.ProxyProtocol = boolEnv(env("RDV_PROXY_PROTOCOL", "false"))
	c.MinClientVersion = env("RDV_MIN_CLIENT_VERSION", "")
	c.LogIPMode = strings.ToLower(env("RDV_LOG_IP_MODE", server.IPModeFull))
	switch c.LogIPMode {
	case server.IPModeFull, server.IPModeTruncated, server.IPModeHMAC:
	default:
		return nil, fmt.Errorf("RDV_LOG_IP_MODE=%q: expected full, truncated or hmac", c.LogIPMode)
	}

	keys := env("RDV_OPERATOR_KEYS", "")
	if f := env("RDV_OPERATOR_KEYS_FILE", ""); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("RDV_OPERATOR_KEYS_FILE: %w", err)
		}
		keys += "\n" + string(b)
	}
	if c.OperatorKeys, err = ParseOperatorKeys(keys); err != nil {
		return nil, err
	}
	if len(c.OperatorKeys) == 0 && !st.Accounts {
		return nil, errors.New("RDV_OPERATOR_KEYS is empty and RDV_ACCOUNTS is off: no operator could connect")
	}
	if err := accountSettings(env, st, public); err != nil {
		return nil, err
	}

	if c.TURNSecret, err = turnSecret(env("RDV_TURN_SECRET", ""), st.DataDir); err != nil {
		return nil, err
	}
	if st.Accounts {
		ws, err := webSecret(env("RDV_WEB_SECRET", ""), st.DataDir)
		if err != nil {
			return nil, err
		}
		c.WebSecret = ws
	}
	return st, nil
}

// webSecret returns RDV_WEB_SECRET, else a secret persisted in the data
// directory (generated on first start). Unlike the TURN secret it must
// survive restarts: TOTP secrets are sealed with a key derived from it.
func webSecret(v, dataDir string) ([]byte, error) {
	if v != "" {
		if len(v) < 32 {
			return nil, errors.New("RDV_WEB_SECRET too short (32 characters minimum; e.g. openssl rand -hex 32)")
		}
		return []byte(v), nil
	}
	p := filepath.Join(dataDir, "web-secret")
	if b, err := os.ReadFile(p); err == nil {
		if s := strings.TrimSpace(string(b)); len(s) >= 32 {
			return []byte(s), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("web secret %s: %w", p, err)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	s := hex.EncodeToString(b)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("web secret: %w", err)
	}
	if err := os.WriteFile(p, []byte(s+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("web secret not persisted in %s (it must be: TOTP secrets are sealed with it): %w", dataDir, err)
	}
	return []byte(s), nil
}

// accountSettings reads the public URL, the SMTP server and the account
// quotas.
func accountSettings(env func(string, string) string, st *Settings, public bool) error {
	c := &st.Server
	c.PublicURL = strings.TrimRight(env("RDV_PUBLIC_URL", ""), "/")
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("RDV_PUBLIC_URL=%q: expected https://host[:port][/path]", c.PublicURL)
		}
	} else if st.Domain != "" {
		c.PublicURL = "https://" + st.Domain
		// The listen port is public only when nothing is in front: not
		// behind a TLS proxy (TLS off), a PROXY-protocol proxy, or on a
		// loopback address.
		if st.TLSMode != TLSOff && !st.ProxyProtocol {
			if h, p, err := net.SplitHostPort(st.Listen); err == nil && p != "443" && p != "" {
				if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
					c.PublicURL += ":" + p
				}
			}
		}
	}
	if !st.Accounts {
		return nil
	}
	if st.DataDir == "" {
		return errors.New("RDV_ACCOUNTS needs RDV_DATA_DIR (the account database lives there)")
	}

	// Admin console and history.
	for _, e := range strings.FieldsFunc(env("RDV_ADMIN_EMAILS", ""), func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		e = accounts.NormalizeEmail(e)
		if !accounts.ValidEmail(e) {
			return fmt.Errorf("RDV_ADMIN_EMAILS: %q is not a valid address", e)
		}
		c.AdminEmails = append(c.AdminEmails, e)
	}
	c.AdminTOTPOptional = !boolEnv(env("RDV_ADMIN_REQUIRE_TOTP", "true"))
	days, err := intEnv(env, "RDV_HISTORY_DAYS", 90, 1, 3650)
	if err != nil {
		return err
	}
	c.HistoryRetention = time.Duration(days) * 24 * time.Hour
	if days, err = intEnv(env, "RDV_AUDIT_DAYS", 365, 1, 3650); err != nil {
		return err
	}
	c.AuditRetention = time.Duration(days) * 24 * time.Hour

	// Abuse reports: auto-protection on by default on a public instance.
	defAuto := 0
	if public {
		defAuto = 3
	}
	if c.ReportAutoDisable, err = intEnv(env, "RDV_REPORTS_AUTODISABLE", defAuto, 0, 1000); err != nil {
		return err
	}
	if days, err = intEnv(env, "RDV_REPORTS_AUTODISABLE_DAYS", 7, 1, 365); err != nil {
		return err
	}
	c.ReportAutoDisableWindow = time.Duration(days) * 24 * time.Hour
	mins, err := intEnv(env, "RDV_REPORT_WINDOW_MINUTES", 60, 1, 7*24*60)
	if err != nil {
		return err
	}
	c.ReportWindow = time.Duration(mins) * time.Minute

	if host := env("RDV_SMTP_HOST", ""); host != "" {
		sc := &accounts.SMTPConfig{
			Host: host, User: env("RDV_SMTP_USER", ""), Password: env("RDV_SMTP_PASSWORD", ""),
			From: env("RDV_SMTP_FROM", ""), TLS: strings.ToLower(env("RDV_SMTP_TLS", accounts.SMTPStartTLS)),
		}
		var err error
		if sc.Port, err = intEnv(env, "RDV_SMTP_PORT", 0, 1, 65535); err != nil {
			return err
		}
		if sc.From == "" {
			return errors.New("RDV_SMTP_FROM is required with RDV_SMTP_HOST")
		}
		if _, err := accounts.NewSMTPMailer(*sc); err != nil {
			return fmt.Errorf("RDV_SMTP_*: %w", err)
		}
		if c.PublicURL == "" {
			return errors.New("email verification needs RDV_PUBLIC_URL or RDV_DOMAIN (the address put in the links)")
		}
		st.SMTP = sc
	}

	// Quotas: on by default on a public instance, unlimited on a private
	// one (0 = unlimited).
	defMax, defDay, defGB := 0, 0, 0
	if public {
		defMax, defDay, defGB = 3, 50, 20
	}
	if c.Quotas.MaxConcurrent, err = intEnv(env, "RDV_ACCOUNT_MAX_SESSIONS", defMax, 0, 10_000); err != nil {
		return err
	}
	if c.Quotas.SessionsPerDay, err = intEnv(env, "RDV_ACCOUNT_SESSIONS_DAY", defDay, 0, 1_000_000); err != nil {
		return err
	}
	gb, err := intEnv(env, "RDV_ACCOUNT_RELAY_GB_MONTH", defGB, 0, 1_000_000)
	if err != nil {
		return err
	}
	c.Quotas.RelayBytesMonth = int64(gb) * 1_000_000_000
	return nil
}

// ParseOperatorKeys reads "label:secret" or "secret" entries separated by
// commas, semicolons or new lines. Lines starting with # are comments.
func ParseOperatorKeys(s string) ([]server.OperatorKey, error) {
	var out []server.OperatorKey
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, e := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ';' }) {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			k := server.OperatorKey{Secret: e}
			if i := strings.IndexByte(e, ':'); i > 0 {
				k.Label, k.Secret = strings.TrimSpace(e[:i]), strings.TrimSpace(e[i+1:])
				if !labelRe.MatchString(k.Label) {
					return nil, fmt.Errorf("operator key label %q: use letters, digits and ._@+- (64 max)", k.Label)
				}
			}
			if len(k.Secret) < 16 {
				return nil, errors.New("operator key too short (16 characters minimum; e.g. openssl rand -hex 24)")
			}
			if seen[k.Secret] {
				return nil, errors.New("duplicate operator key")
			}
			seen[k.Secret] = true
			if k.Label == "" {
				k.Label = fmt.Sprintf("key-%d", len(out)+1)
			}
			out = append(out, k)
		}
	}
	return out, nil
}

// turnTLS returns the advertised port and host of TURN over TLS on the
// signalling port: on by default whenever this process terminates TLS
// (acme, files). With RDV_TLS=off a reverse proxy terminates TLS and cannot
// pass TURN (unless it forwards the TCP stream untouched, which is the same
// as not terminating it here).
func turnTLS(env func(string, string) string, st *Settings) (int, string, error) {
	if st.TLSMode == TLSOff {
		if boolEnv(env("RDV_TURNS", "")) {
			return 0, "", errors.New("RDV_TURNS needs RDV_TLS=acme or files: TURN-TLS shares the TLS port of the signalling")
		}
		return 0, "", nil
	}
	explicit := env("RDV_TURNS", "") != ""
	if v := env("RDV_TURNS", "on"); !boolEnv(v) {
		return 0, "", nil
	}
	_, lp, err := net.SplitHostPort(st.Listen)
	if err != nil {
		return 0, "", fmt.Errorf("RDV_LISTEN=%q: %w", st.Listen, err)
	}
	def, _ := strconv.Atoi(lp)
	if def == 0 {
		def = 443
	}
	port, err := intEnv(env, "RDV_TURNS_PORT", def, 1, 65535)
	if err != nil {
		return 0, "", err
	}
	host := env("RDV_TURNS_HOST", st.Domain)
	if host == "" && !explicit {
		return 0, "", nil // no name to verify the certificate against
	}
	if host == "" {
		return 0, "", errors.New("TURN-TLS needs a host name matching the certificate: set RDV_DOMAIN or RDV_TURNS_HOST (or RDV_TURNS=off)")
	}
	return port, host, nil
}

func publicIP(v, domain string) (net.IP, error) {
	if v != "" {
		ip := net.ParseIP(v)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("RDV_PUBLIC_IP=%q: expected an IPv4 address", v)
		}
		return ip.To4(), nil
	}
	if domain == "" {
		return nil, errors.New("RDV_PUBLIC_IP or RDV_DOMAIN is required")
	}
	// TODO(v1): discover the address with an external STUN query when the
	// domain does not resolve to this host.
	ips, err := net.LookupIP(domain)
	if err != nil {
		return nil, fmt.Errorf("RDV_PUBLIC_IP not set and %s does not resolve: %w", domain, err)
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4, nil
		}
	}
	return nil, fmt.Errorf("RDV_PUBLIC_IP not set and %s has no IPv4 address", domain)
}

func portRange(v string) (uint16, uint16, error) {
	a, b, ok := strings.Cut(v, "-")
	lo, err1 := strconv.Atoi(strings.TrimSpace(a))
	hi, err2 := strconv.Atoi(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || lo < 1024 || hi > 65535 || lo > hi {
		return 0, 0, fmt.Errorf("RDV_RELAY_PORTS=%q: expected MIN-MAX within 1024-65535", v)
	}
	return uint16(lo), uint16(hi), nil
}

func intEnv(env func(string, string) string, k string, def, min, max int) (int, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s=%q: expected an integer in %d..%d", k, v, min, max)
	}
	return n, nil
}

func boolEnv(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// turnSecret returns RDV_TURN_SECRET, else a secret persisted in the data
// directory (generated on first start), else a per-process random one.
// Credentials are bound to live sessions anyway; persistence only matters
// for several nodes sharing the secret (v2).
func turnSecret(v, dataDir string) (string, error) {
	if v != "" {
		if len(v) < 16 {
			return "", errors.New("RDV_TURN_SECRET too short (16 characters minimum)")
		}
		return v, nil
	}
	gen := func() string {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		return hex.EncodeToString(b)
	}
	if dataDir == "" {
		return gen(), nil
	}
	p := filepath.Join(dataDir, "turn-secret")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b)), nil
	}
	s := gen()
	if err := os.WriteFile(p, []byte(s+"\n"), 0o600); err != nil {
		slog.Warn("turn secret not persisted (data directory not writable); using a per-process secret", "err", err)
	}
	return s, nil
}
