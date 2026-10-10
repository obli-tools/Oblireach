//go:build !nt6

package supportlink

import "log/slog"

// slogLogger is log/slog's Logger. The nt6 line (Go 1.20, no log/slog) uses
// golang.org/x/exp/slog instead (slog_nt6.go).
type slogLogger = slog.Logger

func discardLogger() *slogLogger { return slog.New(slog.DiscardHandler) }
