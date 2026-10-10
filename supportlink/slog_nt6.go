//go:build nt6

package supportlink

import (
	"context"

	"golang.org/x/exp/slog"
)

// slogLogger: Go 1.20 has no log/slog; golang.org/x/exp/slog is the same
// API (its predecessor). The modern line uses log/slog (slog_std.go).
type slogLogger = slog.Logger

func discardLogger() *slogLogger { return slog.New(discardHandler{}) }

// discardHandler: x/exp/slog has no DiscardHandler.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }
