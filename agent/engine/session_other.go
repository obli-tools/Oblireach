//go:build !windows && !linux

package engine

import (
	"context"
	"errors"
)

// runSession (macOS / Linux): the direct stream over the gate.
func runSession(ctx context.Context, g *gate, opts Options) error {
	var failure error
	s := NewStreamSession("session", g, StreamHooks{
		Failed: func(code, message string) { failure = errors.New(code + ": " + message) },
	})
	go func() {
		select {
		case <-ctx.Done():
			s.Stop()
		case <-s.Done():
		}
	}()
	s.Run()
	return failure
}
