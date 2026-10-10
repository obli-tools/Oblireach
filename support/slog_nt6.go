//go:build nt6

package main

import (
	"io"

	"golang.org/x/exp/slog"
)

// slogLogger: Go 1.20 has no log/slog; supportlink's nt6 line takes
// golang.org/x/exp/slog's Logger (same API). See slog_std.go.
type slogLogger = slog.Logger

// newDebugLogger writes every level as text to w (-debug).
func newDebugLogger(w io.Writer) *slogLogger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
