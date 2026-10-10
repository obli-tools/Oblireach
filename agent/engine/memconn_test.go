package engine

import (
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"
)

// memFrame is one frame on a memConn.
type memFrame struct {
	op byte
	p  []byte
}

// memConn is an in-memory FrameConn: the test plays the viewer with send
// (viewer → host) and next / drain (host → viewer).
type memConn struct {
	in     chan memFrame
	out    chan memFrame
	closed chan struct{}
	once   sync.Once
}

func newMemConn() *memConn {
	return &memConn{in: make(chan memFrame, 64), out: make(chan memFrame, 4096), closed: make(chan struct{})}
}

func (c *memConn) ReadFrame() (byte, []byte, error) {
	select {
	case f := <-c.in:
		return f.op, f.p, nil
	case <-c.closed:
		return 0, nil, io.EOF
	}
}

func (c *memConn) WriteFrame(op byte, p []byte) error {
	b := append([]byte(nil), p...)
	select {
	case <-c.closed:
		return io.ErrClosedPipe
	default:
	}
	select {
	case c.out <- memFrame{op, b}:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	}
}

func (c *memConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// send is the viewer sending a frame.
func (c *memConn) send(op byte, p []byte) { c.in <- memFrame{op, p} }

func (c *memConn) sendJSON(t *testing.T, v interface{}) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	c.send(0x1, b)
}

// next returns the next host → viewer frame, failing after d.
func (c *memConn) next(t *testing.T, d time.Duration) memFrame {
	t.Helper()
	select {
	case f := <-c.out:
		return f
	case <-time.After(d):
		t.Fatalf("no frame within %v", d)
	}
	return memFrame{}
}

// pending returns the frames already written, without waiting.
func (c *memConn) pending() []memFrame {
	var out []memFrame
	for {
		select {
		case f := <-c.out:
			out = append(out, f)
		default:
			return out
		}
	}
}

// msgType reads a JSON frame's type.
func msgType(p []byte) string { return controlMsgType(p) }
