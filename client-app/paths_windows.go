//go:build windows

package main

// paths_windows.go — where the app keeps its files on Windows (appPaths,
// platform.go): the settings in %APPDATA%\OblireachClient (roaming), the
// machine-local data in %LOCALAPPDATA%\OblireachClient (the vault:
// vault_windows.go vaultBaseDir).

import (
	"os"
	"path/filepath"
)

type winPaths struct{}

var platformPaths appPaths = winPaths{}

// configDir: %APPDATA%\OblireachClient (moved from main.go's init).
func (winPaths) configDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(appData, "OblireachClient")
}

// localDataDir: %LOCALAPPDATA%\OblireachClient ("" when neither
// LOCALAPPDATA nor the home folder is known).
func (winPaths) localDataDir() string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		if home, err := os.UserHomeDir(); err == nil {
			local = filepath.Join(home, "AppData", "Local")
		}
	}
	if local == "" {
		return ""
	}
	return filepath.Join(local, "OblireachClient")
}
