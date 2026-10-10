//go:build !windows

package engine

var audioInitDone bool

func audioInit() error                 { return nil }
func audioCapture() []byte             { return nil }
func audioCaptureV2() ([]byte, int)    { return nil, 0 }
func audioClose()                      {}
func audioSampleRate() int             { return 48000 }
