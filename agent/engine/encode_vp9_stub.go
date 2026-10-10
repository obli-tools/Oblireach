//go:build !windows

package engine

func vp9Available() bool                                                    { return false }
func vp9EncoderInit(width, height, fps, bitrateKbps int) error             { return nil }
func vp9EncodeFrame(bgra []byte, width, height int) ([]byte, error)        { return nil, nil }
func vp9EncoderClose()                                                      {}
