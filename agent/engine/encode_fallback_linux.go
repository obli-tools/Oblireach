//go:build linux

package engine

// The second H.264 slot of stream.go (Media Foundation on Windows). Linux
// has every H.264 encoder behind the first slot (encode_linux.go), so this
// one only exists to reach stream.go's JPEG fallback: it outputs nothing and
// counts its input, and after jpegFallbackThreshold frames without output
// stream.go switches to JPEG and tells the viewer (codec_switch). An error
// here instead made stream.go skip every frame: a session without any
// H.264 encoder stayed black.

var encodeInputCount int
var encodeOutputCount int

func encoderInit(width, height, fps, bitrate int) (extradata []byte, err error) {
	encodeInputCount, encodeOutputCount = 0, 0
	return nil, nil
}

func encodeFrame(bgra []byte, width, height int, pts int64) ([]byte, error) {
	encodeInputCount++
	return nil, nil
}

func encoderClose() {}
