package accounts

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mailer sends plain-text messages (verification links, notices).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// SMTP TLS modes.
const (
	SMTPStartTLS = "starttls" // plain connection upgraded with STARTTLS (required)
	SMTPTLS      = "tls"      // implicit TLS (port 465)
	SMTPNone     = "none"     // no TLS: local relays only
)

// SMTPConfig is the outgoing mail server.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string // "Oblireach <noreply@example.com>" or a bare address
	TLS      string // starttls | tls | none
	// RootCAs overrides the system roots (tests).
	RootCAs *x509.CertPool
	Timeout time.Duration
}

// SMTPMailer sends through an SMTP server, one connection per message.
type SMTPMailer struct {
	cfg  SMTPConfig
	from *mail.Address
}

// NewSMTPMailer validates the configuration.
func NewSMTPMailer(cfg SMTPConfig) (*SMTPMailer, error) {
	if cfg.Host == "" {
		return nil, errors.New("SMTP host is empty")
	}
	switch cfg.TLS {
	case "":
		cfg.TLS = SMTPStartTLS
	case SMTPStartTLS, SMTPTLS, SMTPNone:
	default:
		return nil, fmt.Errorf("SMTP TLS mode %q: expected starttls, tls or none", cfg.TLS)
	}
	if cfg.Port == 0 {
		switch cfg.TLS {
		case SMTPTLS:
			cfg.Port = 465
		case SMTPNone:
			cfg.Port = 25
		default:
			cfg.Port = 587
		}
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("SMTP sender %q: %w", cfg.From, err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &SMTPMailer{cfg: cfg, from: from}, nil
}

// Send delivers one message.
func (m *SMTPMailer) Send(ctx context.Context, to, subject, body string) error {
	if strings.ContainsAny(to, "\r\n") {
		return errors.New("invalid recipient")
	}
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	tlsCfg := &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}
	tlsCfg.RootCAs = m.cfg.RootCAs
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if m.cfg.TLS == SMTPTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return err
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if m.cfg.TLS == SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not offer STARTTLS (set RDV_SMTP_TLS=none only for a local relay)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if m.cfg.User != "" {
		// smtp.PlainAuth refuses to send the password without TLS, except
		// to localhost.
		if err := c.Auth(smtp.PlainAuth("", m.cfg.User, m.cfg.Password, m.cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(buildMessage(m.from, to, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func buildMessage(from *mail.Address, to, subject, body string) []byte {
	var b strings.Builder
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "localhost"
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 {
		domain = from.Address[i+1:]
	}
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "8bit")
	h("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	for _, l := range strings.Split(body, "\n") {
		b.WriteString(l + "\r\n") // net/smtp's DATA writer does the dot-stuffing
	}
	return []byte(b.String())
}
