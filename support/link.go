package main

// The production Backend: supportlink for the rendezvous and the
// peer-to-peer link, the ObliReach engine for the session itself.

import (
	"context"
	"errors"
	"fmt"

	"github.com/oblireach/agent/engine"
	"github.com/oblireach/supportlink"
)

type linkBackend struct {
	server  string
	version string
	ice     supportlink.ICEOptions
	signal  supportlink.SignalOptions
	logger  *slogLogger
}

func (b *linkBackend) Register(ctx context.Context, hooks HostHooks) (HostLink, error) {
	h, err := supportlink.Register(ctx, supportlink.HostConfig{
		Server:        b.server,
		SignalOptions: b.signal,
		ClientVersion: b.version,
		ICE:           b.ice,
		Logger:        b.logger,
		Approve: func(ctx context.Context, r supportlink.ApprovalRequest) bool {
			return hooks.Approve(ctx, Approval{SID: r.SID, OperatorName: r.OperatorName, Account: r.Account, SAS: r.SAS})
		},
		OnCredentials: func(c supportlink.CredentialsChange) {
			hooks.OnCredentials(c.ID, c.Code, c.Reason)
		},
	})
	if err != nil {
		return nil, err
	}
	return hostLink{h}, nil
}

func (b *linkBackend) RunSession(ctx context.Context, conn SessionLink, p Permissions, ev SessionEvents) error {
	s := engine.NewSession(conn, engine.Options{
		AllowInput:     p.Control,
		AllowClipboard: p.Clipboard,
		AllowFiles:     p.Files,
		// The engine's default notice re-runs the executable with the
		// agent's --notify-* flags: this client shows it in its banner.
		OnFileReceived: ev.FileReceived,
		// Chat is always offered: it carries text only, shown in this
		// client's own window.
		OnChat: ev.ChatReceived,
	})
	if ev.ChatReady != nil {
		ev.ChatReady(s.SendChat)
	}
	return s.Run(ctx)
}

// Report sends an abuse report to the rendezvous server (HTTPS, same proxy
// and TLS settings as the signalling).
func (b *linkBackend) Report(ctx context.Context, r ReportRequest) error {
	err := supportlink.SendReport(ctx, b.server, b.signal, supportlink.Report{
		SID: r.SID, Token: r.Token, Reason: r.Reason, Text: r.Text, EndSession: r.EndSession,
	})
	var se *supportlink.ServerError
	switch {
	case err == nil, errors.Is(err, supportlink.ErrAlreadyReported):
		return nil // a retry after a lost answer: the server has it
	case errors.Is(err, supportlink.ErrRateLimited):
		return fmt.Errorf("%w: %v", ErrReportRateLimited, err)
	case errors.Is(err, supportlink.ErrUnreachable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %v", ErrReportUnreachable, err)
	case errors.As(err, &se), errors.Is(err, supportlink.ErrReportInvalid), errors.Is(err, supportlink.ErrReportsDisabled):
		return fmt.Errorf("%w: %v", ErrReportRefused, err)
	}
	return fmt.Errorf("%w: %v", ErrReportUnreachable, err)
}

func (b *linkBackend) ErrKind(err error) string {
	var ue *supportlink.UpgradeError
	switch {
	case errors.As(err, &ue) && ue.Side == supportlink.UpgradeServer:
		// The server is too old for this client: nothing to update here,
		// retry until its administrator updates it.
		return ErrKindRefused
	case errors.Is(err, supportlink.ErrUpgradeRequired):
		return ErrKindUpgrade
	case errors.Is(err, supportlink.ErrBanned), errors.Is(err, supportlink.ErrRateLimited):
		return ErrKindRefused
	}
	var se *supportlink.ServerError
	if errors.As(err, &se) {
		return ErrKindRefused
	}
	return ErrKindUnreachable
}

type hostLink struct{ h *supportlink.Host }

func (l hostLink) ID() string            { return l.h.ID() }
func (l hostLink) Code() string          { return l.h.Code() }
func (l hostLink) Done() <-chan struct{} { return l.h.Done() }
func (l hostLink) Close() error          { return l.h.Close() }

func (l hostLink) WaitForOperator(ctx context.Context) (SessionLink, error) {
	s, err := l.h.WaitForOperator(ctx)
	if err != nil || s == nil {
		if err == nil {
			err = errors.New("no session")
		}
		return nil, err
	}
	return sessionLink{s}, nil
}

type sessionLink struct{ *supportlink.Session }

func (s sessionLink) Path() (string, bool) {
	p := s.Session.Path()
	return string(p), p.Relayed()
}
