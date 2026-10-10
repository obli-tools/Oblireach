package main

import (
	"fmt"
	"time"
)

// displayName shows "?" for an operator that gave no name.
func displayName(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// formatDuration renders 3:07 or 1:02:03.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
