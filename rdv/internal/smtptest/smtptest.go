// Package smtptest is a minimal SMTP sink for tests: it accepts every
// message (optionally after STARTTLS and AUTH PLAIN) and hands it over on a
// channel. It is not a mail server.
package smtptest

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message is one received mail.
type Message struct {
	From string
	To   []string
	Data string // headers and body, CRLF line endings, dot-stuffing removed
	User string // AUTH PLAIN user ("" without AUTH)
	TLS  bool   // received over STARTTLS
}

// Sink is a running fake SMTP server.
type Sink struct {
	Addr string
	Msgs chan Message

	l   net.Listener
	tls *tls.Config
	wg  sync.WaitGroup
}

// Start listens on 127.0.0.1. With tlsCfg, STARTTLS is offered.
func Start(t *testing.T, tlsCfg *tls.Config) *Sink {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Sink{Addr: l.Addr().String(), Msgs: make(chan Message, 64), l: l, tls: tlsCfg}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		s.wg.Wait()
	})
	return s
}

// Port of the sink.
func (s *Sink) Port() int { return s.l.Addr().(*net.TCPAddr).Port }

// Wait returns the next message or fails the test after d.
func (s *Sink) Wait(t *testing.T, d time.Duration) Message {
	t.Helper()
	select {
	case m := <-s.Msgs:
		return m
	case <-time.After(d):
		t.Fatal("no mail received")
	}
	return Message{}
}

// None fails if a message arrives within d.
func (s *Sink) None(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-s.Msgs:
		t.Fatalf("unexpected mail to %v", m.To)
	case <-time.After(d):
	}
}

func (s *Sink) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	tp := textproto.NewConn(c)
	var msg Message
	_ = tp.PrintfLine("220 sink ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			ext := []string{"250-sink", "250-8BITMIME", "250-AUTH PLAIN"}
			if s.tls != nil && !msg.TLS {
				ext = append(ext, "250-STARTTLS")
			}
			ext = append(ext, "250 OK")
			for _, e := range ext {
				_ = tp.PrintfLine("%s", e)
			}
		case cmd == "STARTTLS" && s.tls != nil:
			_ = tp.PrintfLine("220 go ahead")
			tc := tls.Server(c, s.tls)
			if err := tc.Handshake(); err != nil {
				return
			}
			c = tc
			tp = textproto.NewConn(tc)
			msg.TLS = true
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			arg := strings.TrimSpace(line[len("AUTH PLAIN"):])
			if arg == "" {
				_ = tp.PrintfLine("334 ")
				if arg, err = tp.ReadLine(); err != nil {
					return
				}
			}
			b, _ := base64.StdEncoding.DecodeString(arg)
			parts := strings.Split(string(b), "\x00")
			if len(parts) == 3 {
				msg.User = parts[1]
			}
			_ = tp.PrintfLine("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			msg.From = addr(line[10:])
			_ = tp.PrintfLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			msg.To = append(msg.To, addr(line[8:]))
			_ = tp.PrintfLine("250 ok")
		case cmd == "DATA":
			_ = tp.PrintfLine("354 send")
			r := bufio.NewReader(tp.DotReader())
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				b.WriteString(strings.TrimSuffix(l, "\n") + strings.Repeat("\r\n", boolInt(strings.HasSuffix(l, "\n"))))
				if err != nil {
					break
				}
			}
			msg.Data = b.String()
			s.Msgs <- msg
			msg = Message{TLS: msg.TLS, User: msg.User}
			_ = tp.PrintfLine("250 queued")
		case cmd == "RSET", cmd == "NOOP":
			_ = tp.PrintfLine("250 ok")
		case cmd == "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("502 unknown")
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// addr extracts the address of "<a@b> PARAM=...".
func addr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '>'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "<")
}
