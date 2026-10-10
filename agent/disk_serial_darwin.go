//go:build darwin

package main

// readSystemDiskSerial for device identity v2 (device_identity.go). Copied
// from the Obliance agent's machine_uuid_darwin.go: the disk fingerprint must
// be identical in every Obli* agent on one machine.

import (
	"os/exec"
	"regexp"
	"strings"
)

var diskutilMediaSerialRe = regexp.MustCompile(`(?m)^\s*Device / Media Serial Number:\s*(.+)$`)

// readSystemDiskSerial returns the hardware serial of the physical disk
// hosting "/". Uses diskutil to resolve the boot volume to its physical
// backing disk, then reads the "Device / Media Serial Number" field.
func readSystemDiskSerial() string {
	// Resolve "/" to its device node via `df`.
	out, err := exec.Command("df", "/").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return ""
	}
	fields := strings.Fields(lines[1])
	if len(fields) == 0 {
		return ""
	}
	dev := fields[0] // e.g. /dev/disk3s1s1

	// Walk `diskutil info` until we find one with a media serial. If the
	// first device is an APFS snapshot / container, parent is the physical.
	for i := 0; i < 4 && dev != ""; i++ {
		info, err := exec.Command("diskutil", "info", dev).Output()
		if err != nil {
			return ""
		}
		s := string(info)
		if m := diskutilMediaSerialRe.FindStringSubmatch(s); m != nil {
			serial := strings.TrimSpace(m[1])
			if serial != "" {
				return serial
			}
		}
		// Move to the parent ("Part of Whole") if present.
		re := regexp.MustCompile(`(?m)^\s*Part [Oo]f Whole:\s*(\S+)`)
		pm := re.FindStringSubmatch(s)
		if pm == nil {
			break
		}
		dev = "/dev/" + strings.TrimSpace(pm[1])
	}

	return ""
}
