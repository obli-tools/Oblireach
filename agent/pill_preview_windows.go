//go:build windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/oblireach/agent/engine"
)

// runPillPreview shows the LIVE/REC watermark on screen and toggles the mode
// every 3 seconds so the user can eyeball both states without doing a full
// service deploy. Lasts until Ctrl+C or until the console closes.
func runPillPreview() {
	fmt.Fprintln(os.Stderr, "Oblireach pill preview — LIVE/REC capsule top-right.")
	fmt.Fprintln(os.Stderr, "Toggling LIVE↔REC every 3s. Ctrl+C to exit.")

	engine.ShowWatermark("preview")
	defer engine.HideWatermark()

	// Toggle LIVE↔REC on a ticker so both colours can be judged.
	done := make(chan struct{})
	go func() {
		rec := false
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				rec = !rec
				engine.SetWatermarkRecording(rec)
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	close(done)
}
