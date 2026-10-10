//go:build windows || linux

package main

// The production backend of "Receive help" (receive_help.go): supportlink
// for the rendezvous and the peer-to-peer link, the ObliReach engine
// (agent/engine) in this process for the session itself. Shared by Windows
// (receive_help_windows.go) and Linux (receive_help_linux.go).
//
// Engine settings (process-wide, set once before the first registration;
// nothing else in Desktop uses the engine):
//   - SetDiskWrites(false): the engine writes none of its diagnostic files
//     (encoder / input / Magnification logs) and does not touch the
//     SoftwareSASGeneration policy. Desktop keeps no log file of its own: the
//     engine's diagnostics are dropped (SetDiagnosticOutput(nil));
//   - SetBundledDLLs(false): Desktop's MSI ships no codec DLL (only
//     ObliReach.exe); the system encoders (Media Foundation H.264, then
//     JPEG) are used, and no DLL is ever loaded from the exe's folder
//     (Windows; Linux loads Cisco's OpenH264 only as a pinned file:
//     receive_help_linux.go);
//   - the engine's file-received notice re-runs the host executable with
//     the agent's --notify-* flags: Desktop does not implement them, so
//     OnFileReceived is always set (the page shows the file instead).

import (
	"context"
	"errors"
	"sync"

	"github.com/oblireach/agent/engine"
	"github.com/oblireach/supportlink"
	"github.com/oblireach/supportlink/wire"
)

var helpEngineOnce sync.Once

// helpConfigureEngine: the engine's process-wide settings for a host that
// must leave nothing behind (see above). Idempotent.
func helpConfigureEngine() {
	helpEngineOnce.Do(func() {
		engine.SetDiskWrites(false)
		engine.SetBundledDLLs(false)
		engine.SetDiagnosticOutput(nil)
	})
}

// helpPortalPermission: where the desktop's screen sharing permission is
// remembered (Linux under Wayland: receive_help_linux.go; nil: the desktop
// asks at every session, and always on Windows).
var helpPortalPermission func() engine.PortalPermissionStore

// helpEngineOptions: what a session may do (the user's choices at the
// acceptance) and how Desktop hears of it.
func helpEngineOptions(p helpPerms, ev helpEvents) engine.Options {
	o := engine.Options{
		AllowInput:     p.Control,
		AllowClipboard: p.Clipboard,
		AllowFiles:     p.Files,
		OnChat:         ev.OnChat,
		OnFileReceived: ev.FileReceived,
	}
	if helpPortalPermission != nil {
		o.PortalPermission = helpPortalPermission()
	}
	return o
}

type helpLinkBackend struct {
	version string
	ice     supportlink.ICEOptions
}

func (b *helpLinkBackend) Register(ctx context.Context, server string, hooks helpHooks) (helpHostLink, error) {
	helpConfigureEngine()
	h, err := supportlink.Register(ctx, supportlink.HostConfig{
		Server:        server,
		ClientVersion: b.version,
		SignalOptions: helpSignalOptions(b.version),
		ICE:           b.ice,
		Approve: func(ctx context.Context, r supportlink.ApprovalRequest) bool {
			return hooks.Approve(ctx, helpApprovalReq{SID: r.SID, Operator: r.OperatorName, Account: r.Account, SAS: r.SAS})
		},
		OnCredentials: func(c supportlink.CredentialsChange) { hooks.OnCredentials(c.ID, c.Code, c.Reason) },
		// A signalling drop: supportlink reconnects and asks for the same
		// ID back (the registration stays; only the status changes).
		OnState: func(s supportlink.HostState) {
			if hooks.OnState != nil {
				hooks.OnState(string(s))
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return helpHostAdapter{h}, nil
}

func (b *helpLinkBackend) NewSession(conn helpSessionLink, p helpPerms, ev helpEvents) helpRunner {
	helpConfigureEngine()
	return engine.NewSession(conn, helpEngineOptions(p, ev))
}

// Report: an abuse report to the support server the session came through
// (receive_help_report.go helpSendReport: the signalling's User-Agent,
// proxy and TLS settings).
func (b *helpLinkBackend) Report(ctx context.Context, server string, r helpReportReq) error {
	return helpSendReport(ctx, server, b.version, r)
}

func (b *helpLinkBackend) ErrKind(err error) string {
	var ue *supportlink.UpgradeError
	switch {
	case errors.As(err, &ue) && ue.Side == supportlink.UpgradeServer:
		return helpErrRefused // the server is too old: nothing to update here
	case errors.Is(err, supportlink.ErrUpgradeRequired):
		return helpErrUpgrade
	case errors.Is(err, supportlink.ErrBanned), errors.Is(err, supportlink.ErrRateLimited):
		return helpErrRefused
	}
	var se *supportlink.ServerError
	if errors.As(err, &se) {
		if se.Code == wire.ErrUnavailable { // (a temporary failure of the server)
			return helpErrUnreachable
		}
		return helpErrRefused
	}
	return helpErrUnreachable
}

type helpHostAdapter struct{ h *supportlink.Host }

func (a helpHostAdapter) ID() string            { return a.h.ID() }
func (a helpHostAdapter) Code() string          { return a.h.Code() }
func (a helpHostAdapter) Done() <-chan struct{} { return a.h.Done() }
func (a helpHostAdapter) Close() error          { return a.h.Close() }

// RotateCode: a new code for the same ID (receive_help.go helpCodeRotator);
// false while an operator exchange, an approval or a session runs.
func (a helpHostAdapter) RotateCode() bool { return a.h.RotateCode() != "" }

func (a helpHostAdapter) WaitForOperator(ctx context.Context) (helpSessionLink, error) {
	s, err := a.h.WaitForOperator(ctx)
	if err != nil || s == nil {
		if err == nil {
			err = errors.New("no session")
		}
		return nil, err
	}
	return helpSessionAdapter{s}, nil
}

type helpSessionAdapter struct{ *supportlink.Session }

func (s helpSessionAdapter) Path() (string, bool) {
	p := s.Session.Path()
	return string(p), p.Relayed()
}

// PeerInfo: the operator's typed name and server-verified account.
func (s helpSessionAdapter) PeerInfo() (string, string) {
	p := s.Session.Peer()
	return p.Name, p.Account
}
