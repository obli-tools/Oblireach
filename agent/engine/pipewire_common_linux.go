//go:build linux

package engine

import "errors"

// pwStateError / pwStateStreaming mirror enum pw_stream_state.
const (
	pwStateError     = -1
	pwStateStreaming = 3
)

// pwStreamInfo is the state of one PipeWire stream.
type pwStreamInfo struct {
	W, H              int
	State             int
	Seq               uint64 // frames received
	SHM, DMABuf, Skip uint64 // frames copied from shared memory / a linear DMA-BUF, buffers skipped
	Err               string
}

var errPWNoFrame = errors.New("no frame from the compositor yet")
