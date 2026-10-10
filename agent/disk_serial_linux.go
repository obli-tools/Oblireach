//go:build linux

package main

// readSystemDiskSerial for device identity v2 (device_identity.go). Copied
// from the Obliance agent's machine_uuid_linux.go: the disk fingerprint must be
// identical in every Obli* agent on one machine.

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// readSystemDiskSerial returns the hardware serial of the physical block
// device hosting the root filesystem ("/"). Uses /proc/mounts to find the
// root device, then resolves its parent disk and reads the ID_SERIAL* udev
// attribute via `udevadm info`.
func readSystemDiskSerial() string {
	rootDev := findRootBlockDevice()
	if rootDev == "" {
		return ""
	}
	parent := parentBlockDevice(rootDev)
	if parent == "" {
		parent = rootDev
	}

	// Preferred: udevadm info exposes ID_SERIAL, ID_SERIAL_SHORT, ID_WWN.
	if out, err := exec.Command("udevadm", "info", "--query=property", "--name="+parent).Output(); err == nil {
		props := parseUdevProperties(string(out))
		for _, key := range []string{"ID_SERIAL_SHORT", "ID_SERIAL", "ID_WWN"} {
			if v := strings.TrimSpace(props[key]); v != "" {
				return v
			}
		}
	}

	// Fallback: /sys/block/<dev>/device/serial
	name := strings.TrimPrefix(parent, "/dev/")
	if b, err := os.ReadFile("/sys/block/" + name + "/device/serial"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}

	return ""
}

// findRootBlockDevice parses /proc/mounts and returns the device path of the
// filesystem mounted at "/". Returns "" if not found.
func findRootBlockDevice() string {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[1] == "/" {
			return fields[0]
		}
	}
	return ""
}

// parentBlockDevice resolves a partition like /dev/sda3 or /dev/nvme0n1p2 to
// its parent disk (/dev/sda, /dev/nvme0n1). Uses `lsblk` when available,
// otherwise strips trailing digits with a small heuristic.
func parentBlockDevice(dev string) string {
	if out, err := exec.Command("lsblk", "-no", "pkname", dev).Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return "/dev/" + name
		}
	}
	// Heuristic fallback: /dev/sda3 -> /dev/sda, /dev/nvme0n1p2 -> /dev/nvme0n1
	name := strings.TrimPrefix(dev, "/dev/")
	if strings.Contains(name, "nvme") || strings.Contains(name, "mmcblk") {
		if idx := strings.LastIndex(name, "p"); idx > 0 {
			return "/dev/" + name[:idx]
		}
		return dev
	}
	// strip trailing digits for /dev/sdXN style
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	if i > 0 && i < len(name) {
		return "/dev/" + name[:i]
	}
	return dev
}

// parseUdevProperties converts "KEY=value\n" output from `udevadm info
// --query=property` into a map.
func parseUdevProperties(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			out[line[:i]] = line[i+1:]
		}
	}
	return out
}
