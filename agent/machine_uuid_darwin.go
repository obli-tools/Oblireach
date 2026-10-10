//go:build darwin

package main

import (
	"os/exec"
	"regexp"
	"strings"
)

var ioregUUIDRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f\-]+)"`)

func readMachineUUID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	if m := ioregUUIDRe.FindSubmatch(out); m != nil {
		return normaliseUUID(strings.TrimSpace(string(m[1])))
	}
	return ""
}
