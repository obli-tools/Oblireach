//go:build !windows && !linux

package main

import (
	"context"
	"errors"
)

// receive_help_other.go — off Windows, Desktop does not host a session
// (the engine's capture and input are only wired for the Windows build of
// Desktop): a registration always fails.

type helpNoBackend struct{}

func (helpNoBackend) Register(context.Context, string, helpHooks) (helpHostLink, error) {
	return nil, errors.New("receive help: not available on this platform")
}

func (helpNoBackend) NewSession(helpSessionLink, helpPerms, helpEvents) helpRunner { return nil }

func (helpNoBackend) ErrKind(error) string { return helpErrUnreachable }

func (helpNoBackend) Report(context.Context, string, helpReportReq) error {
	return errHelpReportUnavailable
}

func newAppHelpHost(settings *supportSettings) *helpHost {
	return newHelpHost(settings, helpNoBackend{})
}

func startIncomingSupport() {}

func stopIncomingSupport() {}
