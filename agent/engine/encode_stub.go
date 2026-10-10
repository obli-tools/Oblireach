//go:build !windows && !darwin && !linux

package engine

import "fmt"

var encodeInputCount int
var encodeOutputCount int

func encoderInit(width, height, fps, bitrate int) (extradata []byte, err error) {
	return nil, fmt.Errorf("H.264 encoder not supported on this platform")
}
func encodeFrame(bgra []byte, width, height int, pts int64) ([]byte, error) {
	return nil, fmt.Errorf("H.264 encoder not supported on this platform")
}
func encoderClose() {}
