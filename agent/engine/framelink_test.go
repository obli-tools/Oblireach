package engine

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// The viewer frame of every capture-loop message: the agent relay's mapping
// (remote_session_windows.go forwardHelperFrame), byte for byte.
func TestViewerFrameMapping(t *testing.T) {
	p := []byte{0xAA, 0xBB}
	for _, c := range []struct {
		name  string
		typ   byte
		op    byte
		frame []byte
		video bool
		ok    bool
	}{
		{"control", PipeTypeControl, 0x1, p, false, true},
		{"init", PipeTypeInit, 0x1, p, false, true},
		{"audio v2", PipeTypeAudioV2, 0x2, p, false, true},
		{"file down", PipeTypeFileDown, 0x2, p, false, true},
		{"legacy audio", PipeTypeAudioData, 0x2, []byte{0x06, 0xAA, 0xBB}, false, true},
		{"h264", PipeTypeFrame, 0x2, []byte{0x02, 0xAA, 0xBB}, true, true},
		{"jpeg", PipeTypeJPEGFrame, 0x2, []byte{0x01, 0xAA, 0xBB}, true, true},
		{"vp9", PipeTypeVP9Frame, 0x2, []byte{0x03, 0xAA, 0xBB}, true, true},
		{"h265", PipeTypeH265Frame, 0x2, []byte{0x04, 0xAA, 0xBB}, true, true},
		{"av1", PipeTypeAV1Frame, 0x2, []byte{0x05, 0xAA, 0xBB}, true, true},
		{"hello", PipeTypeHello, 0, nil, false, false},
		{"input", PipeTypeInput, 0, nil, false, false},
		{"stop", PipeTypeStop, 0, nil, false, false},
		{"file data", PipeTypeFileData, 0, nil, false, false},
	} {
		op, frame, video, ok := viewerFrame(c.typ, p)
		if op != c.op || !bytes.Equal(frame, c.frame) || video != c.video || ok != c.ok {
			t.Errorf("%s: op=%x frame=%x video=%v ok=%v", c.name, op, frame, video, ok)
		}
	}
}

func TestFrameLinkRecv(t *testing.T) {
	c := newMemConn()
	l := newFrameLink(c)
	defer l.close(nil)
	up := buildFileFrame(FrameTypeFileUp, 3, 0, []byte("x"))
	c.send(0x1, []byte(`{"type":"keepalive"}`))
	c.send(0x1, []byte(`{"type":"mouse","action":"move"}`))
	c.send(0x2, []byte{0x55, 1, 2})
	c.send(0x2, up)
	c.send(0x9, []byte("p"))
	c.send(0x1, []byte(`{"type":"set_codec","codec":"jpeg"}`))
	for _, want := range []struct {
		typ byte
		p   []byte
	}{
		{PipeTypeInput, []byte(`{"type":"mouse","action":"move"}`)},
		{PipeTypeFileData, up},
		{PipeTypeInput, []byte(`{"type":"set_codec","codec":"jpeg"}`)},
	} {
		typ, p, err := l.Recv()
		if err != nil || typ != want.typ || !bytes.Equal(p, want.p) {
			t.Fatalf("Recv = %x %s %v, want %x %s", typ, p, err, want.typ, want.p)
		}
	}
	if f := c.next(t, time.Second); f.op != 0xA || string(f.p) != "p" {
		t.Fatalf("pong: %x %s", f.op, f.p)
	}
	c.send(0x8, nil)
	if _, _, err := l.Recv(); err != io.EOF {
		t.Fatalf("after close: %v", err)
	}
	if err := l.Send(PipeTypeControl, []byte(`{}`)); err != io.EOF {
		t.Fatalf("Send after close: %v", err)
	}
}

// slowConn delays every binary write past the 30 fps frame budget.
type slowConn struct {
	*memConn
	delay time.Duration
}

func (s slowConn) WriteFrame(op byte, p []byte) error {
	if op == 0x2 {
		time.Sleep(s.delay)
	}
	return s.memConn.WriteFrame(op, p)
}

// Congested video writes: the link lowers the bitrate like the relay does —
// set_bitrate back to the loop, bitrate to the viewer.
func TestFrameLinkAdaptiveBitrate(t *testing.T) {
	c := newMemConn()
	l := newFrameLink(slowConn{c, 40 * time.Millisecond})
	defer l.close(nil)
	for i := 0; i < 15; i++ {
		if err := l.Send(PipeTypeFrame, []byte{0, 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	typ, p, err := l.Recv()
	if err != nil || typ != PipeTypeInput {
		t.Fatalf("Recv = %x %s %v", typ, p, err)
	}
	var cmd struct {
		Type    string `json:"type"`
		Bitrate int    `json:"bitrate"`
	}
	if json.Unmarshal(p, &cmd) != nil || cmd.Type != "set_bitrate" || cmd.Bitrate != 10_000_000 {
		t.Fatalf("command %s", p)
	}
	var sawBitrate bool
	frames := 0
	for _, f := range c.pending() {
		if f.op == 0x2 {
			frames++
			if f.p[0] != FrameTypeH264 {
				t.Fatalf("frame type %x", f.p[0])
			}
		}
		if f.op == 0x1 && msgType(f.p) == "bitrate" {
			sawBitrate = true
		}
	}
	if frames != 15 || !sawBitrate {
		t.Fatalf("frames=%d bitrate message=%v", frames, sawBitrate)
	}
}
