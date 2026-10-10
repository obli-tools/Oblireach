//go:build !windows && !darwin && !linux

package engine

import "fmt"

type MonitorInfo struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Primary  bool   `json:"primary,omitempty"`
	DPI      int    `json:"dpi,omitempty"`
	Scale    int    `json:"scale,omitempty"`
	Rotation int    `json:"rotation,omitempty"`
}

func enumerateMonitors() []MonitorInfo                                     { return nil }
func captureInitMonitor(idx int) error                                     { return fmt.Errorf("not supported") }
func captureMonitorOffset() (x, y int)                                     { return 0, 0 }
func captureInit() error                                                   { return fmt.Errorf("screen capture not supported on this platform") }
func captureClose()                                                        {}
func captureWidth() int                                                    { return 0 }
func captureHeight() int                                                   { return 0 }
func captureFrame(buf []byte) (width, height int, err error)               { return 0, 0, fmt.Errorf("not supported") }
