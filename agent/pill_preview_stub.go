//go:build !windows

package main

import (
	"fmt"
	"os"
)

// runPillPreview is Windows-only (the LIVE/REC watermark is a Win32 layered
// window). On other platforms the flag exists but does nothing.
func runPillPreview() {
	fmt.Fprintln(os.Stderr, "--pill-test is Windows-only.")
}
