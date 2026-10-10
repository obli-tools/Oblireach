//go:build !nt6

package main

import (
	"io"
	"log/slog"
)

// slogLogger is log/slog's Logger, as supportlink takes it. The nt6 line
// (Go 1.20, no log/slog) uses golang.org/x/exp/slog (slog_nt6.go).
type slogLogger = slog.Logger

// newDebugLogger writes every level as text to w (-debug).
func newDebugLogger(w io.Writer) *slogLogger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
