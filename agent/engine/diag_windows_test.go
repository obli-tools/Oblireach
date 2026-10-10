//go:build windows

package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fileState struct {
	exists bool
	size   int64
	mod    time.Time
}

func statFile(p string) fileState {
	fi, err := os.Stat(p)
	if err != nil {
		return fileState{}
	}
	return fileState{true, fi.Size(), fi.ModTime()}
}

// withDiskWritesOff turns disk writes off with a capturing writer for one test.
func withDiskWritesOff(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	SetDiskWrites(false)
	SetDiagnosticOutput(buf)
	t.Cleanup(func() {
		SetDiskWrites(true)
		SetDiagnosticOutput(nil)
	})
	return buf
}

// TestDiskWritesOffInputLog: the C input sink hands its lines to the host's
// writer and leaves C:\Windows\Temp\oblireach-input.log alone.
func TestDiskWritesOffInputLog(t *testing.T) {
	before := statFile(inputLogFiles()[0])
	buf := withDiskWritesOff(t)
	logInputEventImpl("diag-test: mouse_move shape only")
	if got := buf.String(); !strings.Contains(got, "[engine input] diag-test: mouse_move shape only") {
		t.Errorf("diagnostic writer got %q", got)
	}
	if after := statFile(inputLogFiles()[0]); after != before {
		t.Errorf("input log touched: %+v -> %+v", before, after)
	}
}

func TestDiskWritesOffEncoderDiag(t *testing.T) {
	saved := encodeDiagFile
	encodeDiagFile = nil
	defer func() { encodeDiagFile = saved }()
	buf := withDiskWritesOff(t)
	openEncoderDiag()
	if encodeDiagFile != nil {
		t.Fatal("encoder diagnostic file opened while disk writes are off")
	}
	diagf("encoderInit: %dx%d", 1920, 1080)
	if got := buf.String(); !strings.Contains(got, "[engine encoder] encoderInit: 1920x1080") {
		t.Errorf("diagnostic writer got %q", got)
	}
}

func TestDiskWritesOffNoPurgeStamp(t *testing.T) {
	withDiskWritesOff(t)
	dir := filepath.Join(t.TempDir(), "state")
	PurgeInputLogOnce(dir, "test")
	if _, err := os.Stat(dir); err == nil {
		t.Error("PurgeInputLogOnce wrote its stamp while disk writes are off")
	}
}

func TestDiskWritesOffSASPolicyUntouched(t *testing.T) {
	buf := withDiskWritesOff(t)
	orig, present, restore := ensureSASPolicy()
	restore()
	if orig != 0 || present {
		t.Errorf("policy read/changed: %d %v", orig, present)
	}
	if !strings.Contains(buf.String(), "SoftwareSASGeneration left unchanged") {
		t.Errorf("diagnostic writer got %q", buf.String())
	}
}

func TestDiskWritesDefaultOn(t *testing.T) {
	if !DiskWritesEnabled() {
		t.Fatal("disk writes must be on by default (agent behaviour)")
	}
	if !BundledDLLsEnabled() {
		t.Fatal("bundled DLLs must be allowed by default (agent behaviour)")
	}
}
