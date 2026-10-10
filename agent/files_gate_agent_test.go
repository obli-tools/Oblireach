package main

// Agent side of the file-transfer permission (files_gate.go): the
// supervisor passes --no-files to the capture helper it spawns.

import (
	"strings"
	"testing"
)

func TestCaptureHelperCmdLineNoFiles(t *testing.T) {
	n := newLegNonce()
	line, err := captureHelperCmdLine("x.exe", "127.0.0.1:5", n, 0, switchDXGIWaitMs, true)
	if err != nil || !strings.HasSuffix(line, " --dxgi-wait=5000 --no-files") {
		t.Fatalf("no-files command line: %q err=%v", line, err)
	}
	line, err = captureHelperCmdLine("x.exe", "127.0.0.1:5", n, 0, 0, false)
	if err != nil || strings.Contains(line, "--no-files") {
		t.Fatalf("files permitted: %q err=%v", line, err)
	}
}
