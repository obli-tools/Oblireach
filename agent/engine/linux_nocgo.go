//go:build linux && !cgo

package engine

// Linux without cgo (CGO_ENABLED=0, the agent's "nocgo" build): the engine
// still compiles; X11, PipeWire and the native encoders are unavailable, so
// a session reports a coded capture error and the encoders fall back to
// JPEG. The portal handshake itself (pure Go) is kept.

import (
	"errors"
	"time"
)

var errNoCgo = errors.New("this build has no native screen capture (built without cgo)")

type x11NoCgo struct{}

func newX11Capture() linuxCapture               { return x11NoCgo{} }
func (x11NoCgo) name() string                   { return "x11" }
func (x11NoCgo) monitors() []MonitorInfo        { return nil }
func (x11NoCgo) initMonitor(int) error          { return errNoCgo }
func (x11NoCgo) offset() (int, int)             { return 0, 0 }
func (x11NoCgo) size() (int, int)               { return 0, 0 }
func (x11NoCgo) frame([]byte) (int, int, error) { return 0, 0, errNoCgo }
func (x11NoCgo) close()                         {}

type x11InputNoCgo struct{}

func newX11Input() linuxInput                                  { return x11InputNoCgo{} }
func (x11InputNoCgo) setOffset(x, y int)                       {}
func (x11InputNoCgo) mouseMove(x, y int)                       {}
func (x11InputNoCgo) mouseButton(int, bool, int, int)          {}
func (x11InputNoCgo) mouseScroll(int)                          {}
func (x11InputNoCgo) key(int, bool)                            {}
func (x11InputNoCgo) vkFromKey(string) (int, int)              { return 0, 0 }
func (x11InputNoCgo) keyEvent(macKeyIn) bool                   { return false }
func (x11InputNoCgo) releaseStale(bool, bool, bool, bool, int) {}
func (x11InputNoCgo) releaseAll()                              {}

type pwSession struct{}

func pwAvailable() error { return errNoCgo }
func pwOpen(fd int, nodes []uint32) (*pwSession, error) {
	closeFD(fd)
	return nil, errNoCgo
}
func (p *pwSession) info(int) pwStreamInfo {
	return pwStreamInfo{State: pwStateError, Err: errNoCgo.Error()}
}
func (p *pwSession) read(int, []byte) (int, int, uint64, error)         { return 0, 0, 0, errNoCgo }
func (p *pwSession) waitFirstFrame(i int, _ time.Duration) pwStreamInfo { return p.info(i) }
func (p *pwSession) close()                                             {}

func gstProbe(gstFamily) error            { return errNoCgo }
func newGstEncoder(gstFamily) h264Encoder { return noCgoEncoder{} }
func openH264LinuxProbe() error           { return errNoCgo }

type openH264Linux struct{ noCgoEncoder }

type noCgoEncoder struct{}

func (noCgoEncoder) init(int, int, int, int) error                  { return errNoCgo }
func (noCgoEncoder) encode([]byte, int, int, int64) ([]byte, error) { return nil, errNoCgo }
func (noCgoEncoder) setBitrate(int)                                 {}
func (noCgoEncoder) close()                                         {}
