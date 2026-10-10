//go:build windows

package engine

import (
	"context"
	"log"
)

// runSession (Windows): the agent's capture loop, in this process, over a
// frameLink. HelperNoFiles carries Options.AllowFiles into the loop (no file
// feature, every file_* refused there too); the gate applies the rest.
func runSession(ctx context.Context, g *gate, opts Options) error {
	HelperNoFiles = !opts.AllowFiles
	RunningUnderFollow = false
	setFileReceivedHook(opts.OnFileReceived)
	defer setFileReceivedHook(nil)
	l := newFrameLink(g)
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			l.close(ctx.Err())
			_ = g.Close() // unblocks the reader
		case <-stop:
		}
	}()
	err := RunCaptureLoop(l)
	close(stop)
	if err != nil {
		log.Printf("session: %v", err)
		_ = g.write(0x1, captureFailedMsg(err))
	}
	l.close(nil)
	return err
}
