//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLinuxCapture serves a fixed 64x48 picture.
type fakeLinuxCapture struct {
	mu     sync.Mutex
	inited []int
}

func (f *fakeLinuxCapture) name() string { return "fake" }
func (f *fakeLinuxCapture) monitors() []MonitorInfo {
	return []MonitorInfo{{Index: 0, Name: "Screen 1", Width: 64, Height: 48}, {Index: 1, Name: "Screen 2", X: 64, Width: 64, Height: 48}}
}
func (f *fakeLinuxCapture) initMonitor(idx int) error {
	f.mu.Lock()
	f.inited = append(f.inited, idx)
	f.mu.Unlock()
	return nil
}
func (f *fakeLinuxCapture) offset() (int, int) { return 0, 0 }
func (f *fakeLinuxCapture) size() (int, int)   { return 64, 48 }
func (f *fakeLinuxCapture) frame(buf []byte) (int, int, error) {
	for i := 0; i < 64*48*4; i++ {
		buf[i] = byte(i)
	}
	return 64, 48, nil
}
func (f *fakeLinuxCapture) close() {}

// runFakeLinuxSession runs RunSession on the fake capture; the returned
// stop ends it and waits.
func runFakeLinuxSession(t *testing.T, opts Options) (*memConn, *recInjector, *fakeLinuxCapture, func()) {
	t.Helper()
	t.Setenv("OBLIREACH_CAPTURE", "x11") // no portal: the override serves the frames
	capt := &fakeLinuxCapture{}
	rec := &recInjector{}
	in := newPortalInputWith(rec, func() (uint32, int, int, int, int, bool) { return 7, 64, 48, 32, 24, true })
	linuxCaptureOverride = func() (linuxCapture, linuxInput) { return capt, in }
	conn := newMemConn()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = RunSession(ctx, conn, opts)
		close(done)
	}()
	return conn, rec, capt, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the session did not end")
		}
		linuxCaptureOverride = nil
	}
}

// nextBinary waits for a video frame of type typ (skipping others).
func nextBinary(t *testing.T, c *memConn, typ byte, d time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		f := c.next(t, time.Until(deadline))
		if f.op == 0x2 && len(f.p) > 0 && f.p[0] == typ {
			return f.p
		}
	}
	t.Fatalf("no frame of type %#x within %v", typ, d)
	return nil
}

func TestLinuxDirectPathWithoutH264FallsBackToJPEG(t *testing.T) {
	t.Setenv("OBLIREACH_H264_ENCODER", "none")
	h264Probe.reset()
	conn, rec, _, stop := runFakeLinuxSession(t, Options{AllowInput: true, AllowBlankScreen: true})
	defer stop()

	init := conn.next(t, 5*time.Second)
	if msgType(init.p) != "init" {
		t.Fatalf("first frame = %s", init.p)
	}
	var m struct {
		Width, Height int
		Features      []string
		Monitors      []MonitorInfo
		BlankScreen   struct {
			Available bool
			Reason    string
		}
	}
	if err := json.Unmarshal(init.p, &m); err != nil {
		t.Fatal(err)
	}
	if m.Width != 64 || m.Height != 48 || len(m.Monitors) != 2 {
		t.Errorf("init = %s", init.p)
	}
	// Wayland has no blank screen: the viewers already show "not available
	// on this platform" for this reason.
	if m.BlankScreen.Available || m.BlankScreen.Reason != "platform" {
		t.Errorf("blankScreen = %+v", m.BlankScreen)
	}
	for _, f := range m.Features {
		if f == featBlankScreen || f == featInputBlockLocal || f == featClipboardText {
			t.Errorf("feature %s advertised on Linux", f)
		}
	}
	// No H.264 encoder: JPEG after the fallback threshold (it used to stay black).
	jpg := nextBinary(t, conn, FrameTypeJPEG, 5*time.Second)
	if len(jpg) < 100 || jpg[1] != 0xFF || jpg[2] != 0xD8 {
		t.Errorf("not a JPEG frame: % x", jpg[:8])
	}

	// Input reaches the Linux backend, mapped into the stream's logical size.
	conn.sendJSON(t, map[string]interface{}{"type": "mouse", "action": "move", "x": 32, "y": 24})
	conn.sendJSON(t, map[string]interface{}{"type": "key", "action": "down", "code": "KeyA", "key": "a"})
	conn.sendJSON(t, map[string]interface{}{"type": "key", "action": "up", "code": "KeyA", "key": "a"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(rec.snapshot(), ";"), "key 30 false") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := strings.Join(rec.snapshot(), "; "); got != "move 7 16.25 12.25; key 30 true; key 30 false" {
		t.Errorf("input = %s", got)
	}
}

func TestLinuxDirectPathH264(t *testing.T) {
	_ = withFakeBackends(t, &fakeEnc{name: "sw"})
	conn, _, capt, stop := runFakeLinuxSession(t, Options{AllowInput: true})
	defer stop()
	if msgType(conn.next(t, 5*time.Second).p) != "init" {
		t.Fatal("no init")
	}
	f := nextBinary(t, conn, FrameTypeH264, 3*time.Second)
	if string(f[1:]) != "sw" {
		t.Errorf("H.264 frame = %q", f)
	}
	// A monitor switch reuses the capture (no new portal dialog) and re-inits.
	conn.sendJSON(t, map[string]interface{}{"type": "set_monitor", "index": 1})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fr := conn.next(t, time.Until(deadline))
		if fr.op == 0x1 && msgType(fr.p) == "init" {
			capt.mu.Lock()
			got := capt.inited
			capt.mu.Unlock()
			if len(got) != 2 || got[1] != 1 {
				t.Errorf("monitor inits = %v", got)
			}
			return
		}
	}
	t.Fatal("no re-init after set_monitor")
}
