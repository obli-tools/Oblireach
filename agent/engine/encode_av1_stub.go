//go:build !windows

package engine

func av1Available() bool                                                    { return false }
func av1EncoderInit(width, height, fps, bitrateKbps int) error             { return nil }
func av1EncodeFrame(bgra []byte, width, height int) ([]byte, error)        { return nil, nil }
func av1EncoderClose()                                                      {}
