//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"
)

func readMachineUUID() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		id := strings.TrimSpace(string(b))
		if len(id) == 32 {
			uuid := fmt.Sprintf("%s-%s-%s-%s-%s",
				id[0:8], id[8:12], id[12:16], id[16:20], id[20:32])
			if u := normaliseUUID(uuid); u != "" {
				return u
			}
		}
	}

	if b, err := os.ReadFile("/sys/class/dmi/id/product_uuid"); err == nil {
		if uuid := normaliseUUID(strings.TrimSpace(string(b))); uuid != "" {
			return uuid
		}
	}

	return ""
}
