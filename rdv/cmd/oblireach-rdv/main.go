// oblireach-rdv is the rendezvous server of Oblireach Support: signalling
// over HTTPS/WSS and an embedded STUN/TURN relay. It never sees the one-time
// codes nor any session content. Configuration is read from RDV_*
// environment variables.
//
// Usage:
//
//	oblireach-rdv                          run the server
//	oblireach-rdv healthcheck              exit 0 when the local instance is healthy
//	oblireach-rdv version
//	oblireach-rdv account list             operator accounts (RDV_DATA_DIR/accounts.db)
//	oblireach-rdv account disable <email>  block the account and end its logins
//	oblireach-rdv account enable <email>
//	oblireach-rdv account delete <email>   delete the account and its counters
//	oblireach-rdv account promote|demote <email>   admin role
//	oblireach-rdv account reset-2fa <email>        remove the TOTP of an account
//	oblireach-rdv admin create <email> [name]     verified admin + one-time password link (1 h)
//	oblireach-rdv report list [open|closed|all]   abuse reports (open by default)
//	oblireach-rdv report close <id> handled|not_abuse
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/oblireach/rdv/internal/accounts"
	"github.com/oblireach/rdv/internal/config"
	"github.com/oblireach/rdv/internal/server"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "version":
			fmt.Println(version)
			return
		case "account":
			os.Exit(accountCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "admin":
			os.Exit(adminCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "report":
			os.Exit(reportCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "setcap-self": // image build only (see Dockerfile)
			if err := setcapSelf(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintln(os.Stderr, "usage: oblireach-rdv [healthcheck|version|account list|disable|enable|delete|promote|demote|reset-2fa <email>|admin create <email> [name]|report list [open|closed|all]|report close <id> handled|not_abuse]")
			os.Exit(2)
		}
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err.Error())
		os.Exit(1)
	}
}

func run() error {
	st, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: st.LogLevel}))
	slog.SetDefault(log)
	st.Server.Logger = log
	st.Server.Version = version
	bindCap := raiseBindCap()

	if err := checkDataDir(st); err != nil {
		if st.Accounts || st.TLSMode == config.TLSAcme {
			return err
		}
		log.Warn("data directory not writable", "event", "config_warning", "detail", err.Error())
	}
	if st.Accounts {
		store, err := accounts.Open(st.AccountsDB())
		if err != nil {
			return err
		}
		defer store.Close()
		st.Server.Accounts = store
		if st.SMTP != nil {
			m, err := accounts.NewSMTPMailer(*st.SMTP)
			if err != nil {
				return err
			}
			st.Server.Mailer = m
		} else if st.Mode == server.ModePublic {
			log.Warn("accounts without email verification", "event", "config_warning",
				"detail", "RDV_SMTP_HOST is not set: new operator accounts are active at once and shown to hosts as unverified; set RDV_SMTP_* on a public instance")
		}
	}

	srv, err := server.New(st.Server)
	if err != nil {
		return err
	}
	if err := srv.StartTURN(); err != nil {
		return err
	}
	defer srv.Close()

	public := &http.Server{
		Addr:              st.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	admin := &http.Server{Addr: st.AdminListen, Handler: srv.AdminHandler(), ReadHeaderTimeout: 5 * time.Second}

	var acmeHTTP *http.Server
	switch st.TLSMode {
	case config.TLSAcme:
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(st.Domain),
			Cache:      autocert.DirCache(filepath.Join(st.DataDir, "acme")),
			Email:      st.ACMEEmail,
		}
		public.TLSConfig = m.TLSConfig() // TLS-ALPN-01 on the HTTPS port
		public.TLSConfig.MinVersion = tls.VersionTLS12
		if st.ACMEHTTP != "" {
			acmeHTTP = &http.Server{Addr: st.ACMEHTTP, Handler: m.HTTPHandler(nil), ReadHeaderTimeout: 5 * time.Second}
		}
	case config.TLSFiles:
		r := &certReloader{cert: st.CertFile, key: st.KeyFile}
		if _, err := r.get(nil); err != nil {
			return err
		}
		public.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.get}
	}

	ln, err := net.Listen("tcp", st.Listen)
	if err != nil {
		return bindHint(err, bindCap)
	}
	if st.ProxyProtocol {
		ln = server.ProxyProtocolListener(ln)
	}
	errc := make(chan error, 3)
	serve := func(name string, f func() error) {
		go func() {
			if err := f(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("%s: %w", name, bindHint(err, bindCap))
			}
		}()
	}
	serve("admin", admin.ListenAndServe)
	if acmeHTTP != nil {
		serve("acme-http", acmeHTTP.ListenAndServe)
	}
	if public.TLSConfig != nil {
		// TLS is terminated here so that TURN over TLS can share the port
		// (ServeTLS routes each connection after the handshake).
		serve("https", func() error { return srv.ServeTLS(public, ln, public.TLSConfig) })
	} else {
		serve("http", func() error { return public.Serve(ln) })
	}
	turns := "off"
	if st.Server.TURNTLSPort > 0 {
		turns = fmt.Sprintf("%s:%d", st.Server.TURNTLSHost, st.Server.TURNTLSPort)
	}
	log.Info("started", "event", "started", "version", version, "listen", st.Listen, "tls", st.TLSMode,
		"turn_port", srv.TURNPort(), "turns", turns, "relay_ports", fmt.Sprintf("%d-%d", st.Server.RelayMinPort, st.Server.RelayMaxPort),
		"public_ip", st.Server.PublicIP.String(), "operator_keys", len(st.Server.OperatorKeys),
		"mode", st.Mode, "accounts", st.Accounts, "email_verification", st.SMTP != nil,
		"proxy_protocol", st.ProxyProtocol, "trust_proxy", st.Server.TrustProxyHeaders)

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sigc:
		log.Info("stopping", "event", "stopping", "signal", s.String())
	case err := <-errc:
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Close() // drops WebSockets so Shutdown does not wait for them
	_ = public.Shutdown(ctx)
	_ = admin.Shutdown(ctx)
	if acmeHTTP != nil {
		_ = acmeHTTP.Shutdown(ctx)
	}
	return nil
}

// certReloader serves a certificate from files and reloads it when the
// files change (renewal by an external tool), checked at most once a minute.
type certReloader struct {
	cert, key string

	mu      sync.Mutex
	current *tls.Certificate
	mod     time.Time
	checked time.Time
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil && time.Since(r.checked) < time.Minute {
		return r.current, nil
	}
	r.checked = time.Now()
	fi, err := os.Stat(r.cert)
	if err != nil {
		if r.current != nil {
			return r.current, nil
		}
		return nil, err
	}
	if r.current != nil && !fi.ModTime().After(r.mod) {
		return r.current, nil
	}
	c, err := tls.LoadX509KeyPair(r.cert, r.key)
	if err != nil {
		if r.current != nil {
			slog.Warn("certificate reload failed; keeping the previous one", "err", err.Error())
			return r.current, nil
		}
		return nil, err
	}
	r.current, r.mod = &c, fi.ModTime()
	return r.current, nil
}

// bindHint explains a refused bind on a port below 1024.
func bindHint(err error, capOK bool) error {
	if !errors.Is(err, syscall.EACCES) || capOK {
		return err
	}
	return fmt.Errorf("%w (ports below 1024 need the NET_BIND_SERVICE capability: cap_add: [NET_BIND_SERVICE] in docker compose, "+
		"or listen on a high port behind a reverse proxy, e.g. RDV_LISTEN=127.0.0.1:8085 with RDV_TLS=off)", err)
}

// checkDataDir tells early, and clearly, that RDV_DATA_DIR is not writable
// (typically a bind mount owned by root while the image runs as 65532).
func checkDataDir(st *config.Settings) error {
	if st.DataDir == "" {
		return nil
	}
	if err := os.MkdirAll(st.DataDir, 0o700); err == nil {
		f, err := os.CreateTemp(st.DataDir, ".write-test-*")
		if err == nil {
			name := f.Name()
			f.Close()
			return os.Remove(name)
		}
	}
	return fmt.Errorf("RDV_DATA_DIR %s is not writable by uid %d (the account database, ACME certificates and the TURN secret live there): "+
		"with a bind mount, run on the host: chown -R 65532:65532 <host directory>", st.DataDir, os.Getuid())
}

// healthcheck queries the admin endpoint of the local instance (used by the
// container HEALTHCHECK: the distroless image has no shell nor curl).
func healthcheck() int {
	addr := os.Getenv("RDV_ADMIN_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:9100"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
