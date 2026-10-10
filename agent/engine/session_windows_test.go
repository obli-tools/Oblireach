//go:build windows

package engine

// End-to-end runs of the engine over an in-memory FrameConn with the real
// capture of the session the test runs in (view only: nothing is injected).
// Skipped where the screen cannot be captured (service session, no display).

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitFrame returns the next frame accepted by keep, failing after d.
func waitFrame(t *testing.T, c *memConn, d time.Duration, keep func(memFrame) bool) memFrame {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			t.Fatalf("no matching frame within %v", d)
		}
		f := c.next(t, left)
		if keep(f) {
			return f
		}
	}
}

// firstInit returns the init, or skips when the capture could not start.
func firstInit(t *testing.T, c *memConn) map[string]interface{} {
	t.Helper()
	f := waitFrame(t, c, 20*time.Second, func(f memFrame) bool { return f.op == 0x1 })
	var m map[string]interface{}
	if err := json.Unmarshal(f.p, &m); err != nil {
		t.Fatalf("first text frame: %s", f.p)
	}
	if m["type"] == "error" {
		t.Skipf("no screen to capture here: %v", m["message"])
	}
	if m["type"] != "init" {
		t.Fatalf("first text frame is %v, want init: %s", m["type"], f.p)
	}
	return m
}

func isVideo(f memFrame) bool {
	return f.op == 0x2 && len(f.p) > 1 && f.p[0] >= FrameTypeJPEG && f.p[0] <= FrameTypeAV1
}

func TestRunSessionWindowsCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("real capture")
	}
	c := newMemConn()
	var starts int32
	ended := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() {
		_ = RunSession(ctx, c, Options{
			OnStart: func() { atomic.AddInt32(&starts, 1) },
			OnEnd:   func(err error) { ended <- err },
		})
	}()
	m := firstInit(t, c)
	feats := ","
	for _, f := range m["features"].([]interface{}) {
		feats += f.(string) + ","
	}
	for _, must := range []string{"view_gen", "mosaic", "keepalive"} {
		if !strings.Contains(feats, ","+must+",") {
			t.Errorf("init features %s lack %s", feats, must)
		}
	}
	for _, never := range []string{"clipboard_text", "file_upload", "type_text", "input_desktop", "session_follow", "input_block_local", "blank_screen", "display_modes", "chat"} {
		if strings.Contains(feats, ","+never+",") {
			t.Errorf("view-only init advertises %s: %s", never, feats)
		}
	}
	if m["width"].(float64) <= 0 || m["height"].(float64) <= 0 {
		t.Fatalf("init size %v x %v", m["width"], m["height"])
	}
	if f := waitFrame(t, c, 15*time.Second, isVideo); f.p[0] != FrameTypeH264 && f.p[0] != FrameTypeJPEG {
		t.Fatalf("first video frame type %x", f.p[0])
	}
	if atomic.LoadInt32(&starts) != 1 {
		t.Fatalf("OnStart ran %d times", starts)
	}

	// Codec switch: the loop answers codec_switch, then JPEG frames follow.
	c.sendJSON(t, map[string]string{"type": "set_codec", "codec": "jpeg"})
	waitFrame(t, c, 10*time.Second, func(f memFrame) bool { return f.op == 0x1 && msgType(f.p) == "codec_switch" })
	j := waitFrame(t, c, 15*time.Second, func(f memFrame) bool { return isVideo(f) && f.p[0] == FrameTypeJPEG })
	if _, err := jpeg.Decode(bytes.NewReader(j.p[1:])); err != nil {
		t.Fatalf("JPEG frame does not decode: %v", err)
	}

	// Viewer input in a view-only session never reaches the loop: nothing to
	// observe here except that the session keeps running.
	c.sendJSON(t, map[string]interface{}{"type": "mouse", "action": "move", "x": 1, "y": 1})

	// The viewer leaves: the session ends cleanly.
	c.send(0x8, nil)
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("OnEnd(%v)", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("session did not end after the viewer closed")
	}
	if !sessionMu.TryLock() {
		t.Fatal("session lock not released")
	}
	sessionMu.Unlock()
}

func TestRunSessionCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("real capture")
	}
	c := newMemConn()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunSession(ctx, c, Options{}) }()
	firstInit(t, c)
	if err := RunSession(context.Background(), newMemConn(), Options{}); err != ErrSessionActive {
		t.Fatalf("second session: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("session did not end on cancel")
	}
}

// The agent's direct path (StreamSession) over a FrameConn: the init is the
// direct-path init (only the keepalive feature) and frames flow.
func TestStreamSessionDirectPath(t *testing.T) {
	if testing.Short() {
		t.Skip("real capture")
	}
	c := newMemConn()
	var failed atomic.Value
	s := NewStreamSession("test", c, StreamHooks{Failed: func(code, msg string) { failed.Store(code + ": " + msg) }})
	done := make(chan struct{})
	go func() { s.Run(); close(done) }()
	m := firstInit(t, c)
	if fs := m["features"].([]interface{}); len(fs) != 1 || fs[0] != "keepalive" {
		t.Fatalf("direct init features %v", fs)
	}
	waitFrame(t, c, 15*time.Second, isVideo)
	c.send(0x8, nil)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("direct stream did not stop")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed")
	}
	if v := failed.Load(); v != nil {
		t.Fatalf("failure reported: %v", v)
	}
}
