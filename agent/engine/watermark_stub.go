//go:build !windows

package engine

func ShowWatermark(operatorName string)  {}
func HideWatermark()                     {}
func SetWatermarkRecording(recording bool) {}
