package main

import (
	"log"
	"regexp"
	"strings"
)

var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var zeroUUID = "00000000-0000-0000-0000-000000000000"

func normaliseUUID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == zeroUUID || !uuidRe.MatchString(s) {
		return ""
	}
	return s
}

func getMachineUUID() string {
	return readMachineUUID()
}

func resolveDeviceUUID(stored string) string {
	if hw := getMachineUUID(); hw != "" {
		if hw != stored {
			log.Printf("Device UUID: using machine UUID %s", hw)
		}
		return hw
	}
	if stored != "" {
		return stored
	}
	fresh := generateUUID()
	log.Printf("Device UUID: hardware UUID unavailable, generated %s", fresh)
	return fresh
}
