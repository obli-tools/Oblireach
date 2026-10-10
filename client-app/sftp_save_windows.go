//go:build windows

package main

// sftp_save_windows.go — Windows' side of sftp_save.go: the Downloads known
// folder (where a browser saves), and the mark of a downloaded file (its
// Zone.Identifier stream, as a browser writes it: SmartScreen and Office's
// protected view treat it as coming from elsewhere, like the page's former
// Blob download did).

import "os"

func init() {
	saveDownloadsDir = func() (string, error) {
		if p := knownFolder(&folderIDDownloads); p != "" {
			return p, nil
		}
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errSaveFolder
		}
		return home + `\Downloads`, nil
	}
	saveMarkFromElsewhere = func(p string) {
		_ = os.WriteFile(p+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
	}
}
