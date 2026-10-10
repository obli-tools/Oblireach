//go:build !h265

package engine

func h265Available() bool                                             { return false }
func h265EncoderInit(width, height, fps, bitrateKbps int) error      { return nil }
func h265EncodeFrame(bgra []byte, width, height int) ([]byte, error) { return nil, nil }
func h265EncoderClose()                                              {}
