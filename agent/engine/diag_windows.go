//go:build windows

package engine

// The C diagnostic sinks (encoder, input, Magnification) write their file
// while disk writes are on; while they are off they format the line and hand
// it to obliEngineDiagC, which forwards it to SetDiagnosticOutput.
//
// This file exports a Go function to C, so its preamble holds declarations
// only (cgo rule).

/*
#include <stdlib.h>
*/
import "C"

//export obliEngineDiagC
func obliEngineDiagC(src *C.char, line *C.char) {
	if src == nil || line == nil {
		return
	}
	diagLine(C.GoString(src), C.GoString(line))
}

// setNativeDiskWrites passes the switch to every C sink.
func setNativeDiskWrites(enabled bool) {
	encoderSetDiskLog(enabled)
	inputSetDiskLog(enabled)
	magSetDiskLog(enabled)
}
