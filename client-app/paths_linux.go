//go:build linux

package main

// paths_linux.go — where the app keeps its files on Linux (appPaths,
// platform.go): the XDG base directories ($XDG_CONFIG_HOME/oblireach,
// $XDG_DATA_HOME/oblireach). Inside the Flatpak they resolve to the app's own
// ~/.var/app/<app id>/config and .../data.

import (
	"os"
	"path/filepath"
)

type linuxPaths struct{}

var platformPaths appPaths = linuxPaths{}

const linuxAppDirName = "oblireach"

func (linuxPaths) configDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, linuxAppDirName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", linuxAppDirName)
}

func (linuxPaths) localDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, linuxAppDirName)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", linuxAppDirName)
}
