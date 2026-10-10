//go:build !windows && !linux

package engine

func openH264Available() bool                                              { return false }
func openH264Init(width, height, fps, bitrate int) error                   { return nil }
func openH264EncodeFrame(bgra []byte, width, height int, ts int64) ([]byte, error) { return nil, nil }
func openH264SetBitrate(bitrate int)                                       {}
func openH264Close()                                                       {}
