//go:build !windows

package engine

import (
	"errors"
	"syscall"
)

// isDiskFull: no space left on the device (file transfers, filexfer.go).
func isDiskFull(err error) bool { return errors.Is(err, syscall.ENOSPC) }
