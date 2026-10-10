// Package server is the oblireach-rdv rendezvous server: WebSocket
// signalling between a host and an operator, and an embedded STUN/TURN
// server for the relay fallback. All state is in memory.
//
// It never sees content: the one-time code stays between the peers (CPace),
// SDP and candidates are sealed under the PAKE key, and media is DTLS end to
// end. TURN credentials are minted only once the host accepted a session,
// are bound to that session, and die with it.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oblireach/supportlink/wire"

	"github.com/oblireach/rdv/internal/accounts"
)

// Instance modes.
const (
	ModePrivate = "private"
	ModePublic  = "public"
)

// AccountQuotas bound what one operator account may use (0 = unlimited).
// Static operator keys are never subject to them.
type AccountQuotas struct {
	MaxConcurrent   int   // sessions at once (signalling included)
	SessionsPerDay  int   // sessions created per UTC day
	RelayBytesMonth int64 // relayed bytes per UTC month (both directions)
}

// OperatorKey is one instance operator key. Label names the account in logs
// and in the host's acceptance window; the secret never leaves the server.
type OperatorKey struct {
	Label  string
	Secret string
}

// Config is the server configuration (see internal/config for the
// environment mapping).
type Config struct {
	// PublicIP is the address advertised for relay allocations.
	PublicIP net.IP
	// ICEHost is the host name put in stun:/turn: URLs (domain or IP).
	ICEHost string
	// TURNPort is the STUN/TURN port, UDP and TCP (0 = pick a free one,
	// tests only).
	TURNPort int
	// TURNListenIP is the local address the TURN sockets bind to.
	TURNListenIP string
	// TURNTLSPort is the port advertised in the turns: URL (TURN over TLS,
	// sharing the signalling port). 0 = no TURN-TLS. It only works when the
	// signalling is served by ServeTLS, which demultiplexes HTTPS and TURN
	// after the TLS handshake.
	TURNTLSPort int
	// TURNTLSHost is the host name of the turns: URL. It must match the
	// certificate (default: ICEHost).
	TURNTLSHost string
	// RelayMinPort..RelayMaxPort is the UDP range of relay allocations.
	RelayMinPort, RelayMaxPort uint16
	// TURNSecret signs the TURN REST credentials.
	TURNSecret string
	// RelayMaxKbps caps each relayed session, per direction (0 = no cap).
	RelayMaxKbps int
	// RelayMaxDuration bounds a relayed session (credential lifetime).
	RelayMaxDuration time.Duration
	// RelayMaxConcurrent caps the sessions using the relay at once.
	RelayMaxConcurrent int
	// RelayIdleTimeout closes an allocation that relayed nothing for that
	// long (0 = 20 s, negative = never). ICE gathers relay candidates in
	// every session; the ones a session does not use are reclaimed instead
	// of holding a relay port for the whole session. A relay pair in use
	// always carries traffic (data, or ICE keepalives every 2 s).
	RelayIdleTimeout time.Duration
	// RelayAllowPrivatePeers lets TURN relay to private, loopback and
	// link-local peers. Off in production: an open relay towards the
	// server's own network is an SSRF.
	RelayAllowPrivatePeers bool

	// OperatorKeys are static operator keys (both modes).
	OperatorKeys []OperatorKey

	// Mode is ModePrivate (default) or ModePublic; Name and Version are
	// announced by GET /v1/info.
	Mode, Name, Version string
	// Accounts enables operator accounts (registration pages, login API,
	// tokens in connect). nil: static keys only.
	Accounts *accounts.Store
	// Mailer sends the verification links. nil: accounts are active at
	// once, their email unverified (and labelled so to the host).
	Mailer accounts.Mailer
	// PublicURL is the absolute base URL of the web pages
	// ("https://rdv.example.com"); empty: derived from each request.
	PublicURL string
	// Quotas of operator accounts.
	Quotas AccountQuotas
	// FormMinFill rejects forms submitted faster than this after they were
	// served (anti-bot; 0 = 3 s, negative = off).
	FormMinFill time.Duration
	// MinClientVersion refuses older clients with upgrade_required.
	MinClientVersion string
	// TrustProxyHeaders takes the client IP from X-Forwarded-For (only
	// behind a reverse proxy that sets it).
	TrustProxyHeaders bool

	Limits Limits

	// PakeTimeout: connect -> key confirmed (default 60 s).
	PakeTimeout time.Duration
	// DecisionTimeout: key confirmed -> accepted by the person (120 s).
	DecisionTimeout time.Duration
	// PingInterval and IdleTimeout of the signalling WebSocket (25 s / 60 s).
	PingInterval, IdleTimeout time.Duration
	// SweepInterval of the expiry and garbage-collection loop (1 s).
	SweepInterval time.Duration
	// ResumeGrace keeps the ID of a host whose signalling dropped for that
	// long: it reconnects with the ID and its resume secret and gets the
	// same ID back (0 = 10 min, negative = off).
	ResumeGrace time.Duration

	Logger    *slog.Logger
	LogIPMode string

	// AdminEmails are always admins once their address is verified
	// (RDV_ADMIN_EMAILS).
	AdminEmails []string
	// AdminTOTPOptional lets admins use the admin pages without TOTP
	// (RDV_ADMIN_REQUIRE_TOTP=false). Default: TOTP required.
	AdminTOTPOptional bool
	// HistoryRetention keeps the login and session history (0 = 90 days);
	// AuditRetention the admin audit log (0 = 365 days).
	HistoryRetention, AuditRetention time.Duration

	// Abuse reports (with Accounts only: they live in its database). The
	// host of an accepted session gets a report token; it may report the
	// operator during the session and for ReportWindow after its end
	// (0 = 1 h).
	ReportWindow time.Duration
	// ReportAutoDisable disables an operator account (pending an admin's
	// review) once that many distinct reporters reported it within
	// ReportAutoDisableWindow (0 = 7 days). 0 = off.
	ReportAutoDisable       int
	ReportAutoDisableWindow time.Duration

	// WebSecret derives the keys of the web side (form stamps, sign-in
	// tickets, TOTP secrets sealed at rest). Persist it (RDV_DATA_DIR/
	// web-secret): with a new one, enrolled TOTP secrets cannot be read.
	// nil: a per-process random secret (tests).
	WebSecret []byte

	// tamper rewrites routed messages: tests only, simulates a compromised
	// server.
	tamper func(*wire.Msg)
}

func (c *Config) defaults() error {
	if c.PublicIP == nil {
		return errors.New("public IP is required")
	}
	if c.ICEHost == "" {
		c.ICEHost = c.PublicIP.String()
	}
	if c.TURNTLSHost == "" {
		c.TURNTLSHost = c.ICEHost
	}
	if c.TURNTLSPort < 0 || c.TURNTLSPort > 65535 {
		return errors.New("invalid TURN-TLS port")
	}
	if c.RelayIdleTimeout == 0 {
		c.RelayIdleTimeout = 20 * time.Second
	}
	if c.TURNListenIP == "" {
		c.TURNListenIP = "0.0.0.0"
	}
	if c.RelayMinPort == 0 || c.RelayMaxPort < c.RelayMinPort {
		return errors.New("invalid relay port range")
	}
	if len(c.TURNSecret) < 16 {
		return errors.New("TURN secret too short (16 characters minimum)")
	}
	if len(c.OperatorKeys) == 0 && c.Accounts == nil {
		return errors.New("no operator key configured and accounts disabled: no operator could connect")
	}
	switch c.Mode {
	case "":
		c.Mode = ModePrivate
	case ModePrivate, ModePublic:
	default:
		return fmt.Errorf("invalid mode %q", c.Mode)
	}
	if c.Name == "" {
		c.Name = "Oblireach"
	}
	if c.Version == "" {
		c.Version = "dev"
	}
	if c.FormMinFill == 0 {
		c.FormMinFill = 3 * time.Second
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.Mailer != nil && c.Accounts != nil && c.PublicURL == "" {
		// Verification links are never built from the Host header of the
		// request (an attacker would get the link of someone else's mail).
		return errors.New("email verification needs the public URL of the server")
	}
	for _, k := range c.OperatorKeys {
		if len(k.Secret) < 16 {
			return fmt.Errorf("operator key %q too short (16 characters minimum)", k.Label)
		}
	}
	if c.RelayMaxDuration <= 0 {
		c.RelayMaxDuration = 4 * time.Hour
	}
	if c.RelayMaxConcurrent <= 0 {
		c.RelayMaxConcurrent = 300
	}
	if c.Limits == (Limits{}) {
		c.Limits = DefaultLimits()
	}
	c.Limits.webDefaults()
	if c.PakeTimeout <= 0 {
		c.PakeTimeout = 60 * time.Second
	}
	if c.DecisionTimeout <= 0 {
		c.DecisionTimeout = 120 * time.Second
	}
	if c.PingInterval <= 0 {
		c.PingInterval = 25 * time.Second
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 60 * time.Second
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = time.Second
	}
	if c.ResumeGrace == 0 {
		c.ResumeGrace = 10 * time.Minute
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.HistoryRetention <= 0 {
		c.HistoryRetention = accounts.DefaultHistoryTTL
	}
	if c.AuditRetention <= 0 {
		c.AuditRetention = accounts.DefaultAuditTTL
	}
	if c.ReportWindow <= 0 {
		c.ReportWindow = time.Hour
	}
	if c.ReportAutoDisableWindow <= 0 {
		c.ReportAutoDisableWindow = 7 * 24 * time.Hour
	}
	if c.ReportAutoDisable < 0 {
		c.ReportAutoDisable = 0
	}
	if len(c.WebSecret) == 0 {
		c.WebSecret = make([]byte, 32)
		if _, err := rand.Read(c.WebSecret); err != nil {
			return err
		}
	} else if len(c.WebSecret) < 16 {
		return errors.New("web secret too short (16 bytes minimum)")
	}
	for i, e := range c.AdminEmails {
		c.AdminEmails[i] = accounts.NormalizeEmail(e)
	}
	return nil
}

// Server is one rendezvous instance.
type Server struct {
	cfg  Config
	log  *eventLog
	m    *metrics
	hub  *hub
	turn *turnServer
	keys []hashedKey
	// turnsL receives the TURN-over-TLS connections split off the
	// signalling port by ServeTLS (nil without TURN-TLS).
	turnsL *connQueue
	web    *web
	// dbw serialises the asynchronous account-database writes (usage
	// counters) off the hub lock; nil once closed (guarded by hub.mu).
	dbw   chan func(context.Context)
	dbwWG sync.WaitGroup

	closeOnce sync.Once
	stop      chan struct{}
	started   time.Time
}

type hashedKey struct {
	label string
	sum   [32]byte
}

// New validates the configuration and prepares the server. Call StartTURN,
// then serve Handler (signalling) and AdminHandler (metrics, health).
func New(cfg Config) (*Server, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, m: newMetrics(), stop: make(chan struct{}), started: time.Now()}
	s.log = newEventLog(cfg.Logger, cfg.LogIPMode)
	if cfg.Accounts != nil {
		cfg.Accounts.SetRetention(cfg.HistoryRetention, cfg.AuditRetention)
	}
	for i, k := range cfg.OperatorKeys {
		label := k.Label
		if label == "" {
			label = fmt.Sprintf("key-%d", i+1)
		}
		s.keys = append(s.keys, hashedKey{label: label, sum: sha256.Sum256([]byte(k.Secret))})
	}
	s.hub = newHub(s)
	s.web = newWeb(s)
	if cfg.Accounts != nil {
		s.dbw = make(chan func(context.Context), 4096)
		s.dbwWG.Add(1)
		go s.dbWriter(s.dbw)
	}
	go s.web.janitor(s.stop)
	if cfg.TURNTLSPort > 0 {
		s.turnsL = newConnQueue(&net.TCPAddr{Port: cfg.TURNTLSPort}, nil)
	}
	return s, nil
}

// operatorAccount returns the account of a valid operator key. Every key is
// compared in constant time.
func (s *Server) operatorAccount(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(key))
	account, ok := "", false
	for _, k := range s.keys {
		if subtle.ConstantTimeCompare(sum[:], k.sum[:]) == 1 {
			account, ok = k.label, true
		}
	}
	return account, ok
}

// StartTURN binds the STUN/TURN sockets and starts the expiry loop.
func (s *Server) StartTURN() error {
	t, err := startTURN(s)
	if err != nil {
		return err
	}
	s.turn = t
	go s.hub.sweepLoop(s.stop)
	return nil
}

// TURNPort is the bound STUN/TURN port.
func (s *Server) TURNPort() int {
	if s.turn == nil {
		return s.cfg.TURNPort
	}
	return s.turn.port
}

// Handler serves the signalling WebSocket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(wire.Path, s.hub.serveWS)
	s.web.routes(mux)
	return mux
}

// AdminHandler serves /metrics and /healthz. Keep it off the public
// interface.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", s.serveMetrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if s.turn == nil {
			http.Error(w, "turn not started", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// Close stops TURN and drops every signalling connection.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		s.hub.closeAll()
		if s.turn != nil {
			s.turn.close()
		}
		s.hub.mu.Lock()
		if s.dbw != nil {
			close(s.dbw)
			s.dbw = nil
		}
		s.hub.mu.Unlock()
		s.dbwWG.Wait()
	})
	return nil
}

// dbWriteLocked queues an account-database write (usage counters). The
// caller holds hub.mu. A full queue drops the write (logged): counters are
// best effort and the hub never waits for the disk.
func (s *Server) dbWriteLocked(f func(context.Context)) {
	if s.dbw == nil {
		return
	}
	select {
	case s.dbw <- f:
	default:
		s.log.warn("account_db_queue_full")
	}
}

func (s *Server) dbWriter(ch <-chan func(context.Context)) {
	defer s.dbwWG.Done()
	for f := range ch {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		f(ctx)
		cancel()
	}
}
