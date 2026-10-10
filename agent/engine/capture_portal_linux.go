//go:build linux

package engine

// Portal capture backend: the screens the user shared in the desktop's
// dialog (portal_linux.go), read from PipeWire (pipewire_linux.go). Each
// shared screen is one entry of the viewer's monitor list; set_monitor
// switches between the streams already running (no new dialog). The
// mosaic (every screen in one frame) is not offered on the direct path.

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// pwFirstFrameWait: how long a new capture waits for the compositor's first
// frame (the encoder needs the size).
const pwFirstFrameWait = 5 * time.Second

type portalCapture struct {
	ps  *portalSession
	mu  sync.Mutex
	idx int
	fw  int // last frame size
	fh  int
}

func newPortalCapture(ps *portalSession) *portalCapture { return &portalCapture{ps: ps} }

func (c *portalCapture) name() string { return "portal" }

// pipewire returns the session's consumer, connecting it on first use.
func (c *portalCapture) pipewire() (*pwSession, error) {
	ps := c.ps
	ps.mu.Lock()
	pw := ps.pw
	ps.mu.Unlock()
	if pw != nil {
		return pw, nil
	}
	if err := ps.Err(); err != nil {
		return nil, err
	}
	fd := ps.takePWFD()
	if fd < 0 {
		return nil, errors.New("the screen sharing session has no PipeWire connection")
	}
	nodes := make([]uint32, len(ps.streams))
	for i, s := range ps.streams {
		nodes[i] = s.NodeID
	}
	pw, err := pwOpen(fd, nodes)
	if err != nil {
		return nil, err
	}
	ps.mu.Lock()
	ps.pw = pw
	ps.mu.Unlock()
	return pw, nil
}

func (c *portalCapture) initMonitor(idx int) error {
	if idx < 0 || idx >= len(c.ps.streams) {
		idx = 0 // a stale index (another layout): the first shared screen
	}
	pw, err := c.pipewire()
	if err != nil {
		return err
	}
	inf := pw.waitFirstFrame(idx, pwFirstFrameWait)
	if inf.State == pwStateError {
		return fmt.Errorf("PipeWire stream failed: %s", inf.Err)
	}
	w, h := inf.W, inf.H
	if w <= 0 || h <= 0 {
		// No format yet: the portal's size (logical; corrected by the first
		// frame, which then restarts the stream at its real size).
		w, h = c.ps.streams[idx].W, c.ps.streams[idx].H
	}
	if w <= 0 || h <= 0 {
		return errors.New("the compositor sent no picture of the shared screen")
	}
	c.mu.Lock()
	c.idx, c.fw, c.fh = idx, w, h
	c.mu.Unlock()
	log.Printf("capture: screen %d via the screen sharing portal (PipeWire node %d, %dx%d, frames so far shm=%d dmabuf=%d skipped=%d)",
		idx, c.ps.streams[idx].NodeID, w, h, inf.SHM, inf.DMABuf, inf.Skip)
	return nil
}

func (c *portalCapture) monitors() []MonitorInfo {
	c.ps.mu.Lock()
	pw := c.ps.pw
	c.ps.mu.Unlock()
	var size func(int) (int, int)
	if pw != nil {
		size = func(i int) (int, int) { inf := pw.info(i); return inf.W, inf.H }
	}
	return portalMonitors(c.ps.streams, size)
}

func (c *portalCapture) offset() (x, y int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.ps.streams[c.idx]
	return s.X, s.Y
}

func (c *portalCapture) size() (w, h int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fw, c.fh
}

func (c *portalCapture) frame(buf []byte) (int, int, error) {
	if err := c.ps.Err(); err != nil {
		// The user stopped sharing: end the stream (the caller ends on
		// errCaptureResized; RunSession reports the real reason).
		return 0, 0, fmt.Errorf("%w (%v)", errCaptureResized, err)
	}
	c.ps.mu.Lock()
	pw := c.ps.pw
	c.ps.mu.Unlock()
	if pw == nil {
		return 0, 0, errors.New("capture not initialised")
	}
	c.mu.Lock()
	idx := c.idx
	c.mu.Unlock()
	w, h, _, err := pw.read(idx, buf)
	if err != nil {
		return w, h, err
	}
	c.mu.Lock()
	c.fw, c.fh = w, h
	c.mu.Unlock()
	return w, h, nil
}

// close detaches from the stream; the streams keep running for the next
// initMonitor of the same session (they end with the portal session).
func (c *portalCapture) close() {}

// target is the geometry the pointer is mapped through: the stream's node,
// the frame size the viewer sees and the stream's logical size.
func (c *portalCapture) target() (node uint32, fw, fh, lw, lh int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx < 0 || c.idx >= len(c.ps.streams) || c.fw <= 0 || c.fh <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	s := c.ps.streams[c.idx]
	lw, lh = s.W, s.H
	if lw <= 0 || lh <= 0 {
		lw, lh = c.fw, c.fh
	}
	return s.NodeID, c.fw, c.fh, lw, lh, true
}
