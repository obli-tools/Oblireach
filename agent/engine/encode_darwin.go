//go:build darwin

package engine

// TODO: Implement using VideoToolbox H.264 hardware encoder.

import "fmt"

var encodeInputCount int
var encodeOutputCount int

func encoderInit(width, height, fps, bitrate int) (extradata []byte, err error) {
	return nil, fmt.Errorf("H.264 encoder not yet implemented on macOS (coming soon)")
}
func encodeFrame(bgra []byte, width, height int, pts int64) ([]byte, error) {
	return nil, fmt.Errorf("H.264 encoder not yet implemented on macOS")
}
func encoderClose() {}
