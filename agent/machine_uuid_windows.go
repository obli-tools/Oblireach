//go:build windows

package main

import (
	"os/exec"
	"strings"
)

func readMachineUUID() string {
	out, err := exec.Command(
		"powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-CimInstance -ClassName Win32_ComputerSystemProduct).UUID",
	).Output()
	if err == nil {
		if uuid := normaliseUUID(strings.TrimSpace(string(out))); uuid != "" {
			return uuid
		}
	}

	out, err = exec.Command("wmic", "csproduct", "get", "UUID", "/value").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "UUID=") {
				if uuid := normaliseUUID(strings.TrimPrefix(line, "UUID=")); uuid != "" {
					return uuid
				}
			}
		}
	}

	return ""
}
