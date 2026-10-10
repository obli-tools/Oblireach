package engine

// Local writes of the engine and where its diagnostics go.
//
// By default (the Oblireach agent) the engine keeps its historical
// diagnostic files: the encoder log (%TEMP%, C:\Users\Public or
// C:\ProgramData\OblireachAgent), the input log and the Magnification log
// (C:\Windows\Temp). A host that must leave nothing behind on the machine
// (the portable remote-help client) calls SetDiskWrites(false) before
// RunSession: no diagnostic file is created or opened any more, those lines
// go to the writer given to SetDiagnosticOutput (nil: dropped), and the
// engine does not change the SoftwareSASGeneration policy value for a
// Ctrl+Alt+Del request.
//
// What a viewer explicitly asks for is not covered by this switch: received
// files (Options.AllowFiles) and a display mode kept in the registry
// (Options.AllowDisplayModes).

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// diskWritesOff is the inverse of SetDiskWrites (zero value: writes on).
	diskWritesOff atomic.Bool

	diagMu  sync.Mutex
	diagOut io.Writer
)

// SetDiskWrites turns the engine's own diagnostic files (and the temporary
// SoftwareSASGeneration policy change) on or off for the whole process.
// Default: on. Call it before RunSession.
func SetDiskWrites(enabled bool) {
	diskWritesOff.Store(!enabled)
	setNativeDiskWrites(enabled)
}

// DiskWritesEnabled reports the SetDiskWrites setting.
func DiskWritesEnabled() bool { return !diskWritesOff.Load() }

// SetDiagnosticOutput sets where the diagnostics that would have gone to a
// file go while disk writes are off (nil: dropped). One line per call,
// prefixed with a timestamp and the source ("encoder", "input", "mag",
// "codec-dll"). Writes are serialised.
func SetDiagnosticOutput(w io.Writer) {
	diagMu.Lock()
	diagOut = w
	diagMu.Unlock()
}

// diagLine sends one diagnostic line to the SetDiagnosticOutput writer.
func diagLine(src, msg string) {
	diagMu.Lock()
	defer diagMu.Unlock()
	if diagOut == nil {
		return
	}
	for len(msg) > 0 && (msg[len(msg)-1] == '\n' || msg[len(msg)-1] == '\r') {
		msg = msg[:len(msg)-1]
	}
	fmt.Fprintf(diagOut, "%s [engine %s] %s\n", time.Now().Format("2006/01/02 15:04:05.000000"), src, msg)
}
