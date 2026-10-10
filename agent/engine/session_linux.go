//go:build linux

package engine

import (
	"context"
	"errors"
	"log"
)

// runSession (Linux): the direct stream over the gate. Under Wayland the
// session first opens the screen sharing portal (the desktop's dialog: which
// screens, and remote control when Options.AllowInput) and holds it for its
// whole life, so a monitor switch never asks again; the stream ends when the
// user stops sharing from the desktop.
func runSession(ctx context.Context, g *gate, opts Options) error {
	var ps *portalSession
	if linuxCaptureBackend() == linuxBackendPortal {
		bus, err := dialPortalBus()
		if err == nil {
			ps, err = openPortalSession(ctx, bus, portalConfig{input: opts.AllowInput, store: opts.PortalPermission})
			if err != nil {
				_ = bus.Close()
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil // cancelled while the desktop's dialog was open
			}
			log.Printf("session: screen sharing portal: %v", err)
			_ = g.WriteFrame(0x1, captureFailedMsg(err))
			return err
		}
		attachPortalSession(ps)
		defer func() {
			detachPortalSession(ps)
			ps.close()
		}()
	}

	var failure error
	s := NewStreamSession("session", g, StreamHooks{
		Failed: func(code, message string) { failure = errors.New(code + ": " + message) },
	})
	var portalDone <-chan struct{}
	if ps != nil {
		portalDone = ps.Done()
	}
	go func() {
		select {
		case <-ctx.Done():
			s.Stop()
		case <-portalDone:
			s.Stop()
		case <-s.Done():
		}
	}()
	s.Run()
	if failure == nil && ps != nil {
		if err := ps.Err(); err != nil {
			failure = err // the user stopped sharing on the desktop
		}
	}
	return failure
}
