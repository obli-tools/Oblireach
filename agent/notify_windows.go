//go:build windows

package main

/*
#cgo LDFLAGS: -lwtsapi32
#include <windows.h>
#include <wtsapi32.h>

// getUILanguage returns the primary language ID of the user's default UI language.
static LANGID getUILanguage(void) {
	return PRIMARYLANGID(GetUserDefaultUILanguage());
}
*/
import "C"
import (
	"fmt"
	"log"
	"os"
)

// notifySession shows a toast notification in the given WTS session.
// It spawns a helper subprocess in the target session that displays a
// custom dark toast popup in the bottom-right corner (no ugly MessageBox).
func notifySession(sessionID int, username string, connected bool) {
	lang := uint16(C.getUILanguage())
	// LANG_FRENCH = 0x0C
	isFrench := lang == 0x0C

	var title, msg string
	if connected {
		if isFrench {
			title = "Oblireach — Connexion distante"
			msg = username + " ouvre une connexion Oblireach à votre machine."
		} else {
			title = "Oblireach — Remote Connection"
			msg = username + " is opening an Oblireach connection to your machine."
		}
	} else {
		if isFrench {
			title = "Oblireach — Connexion fermée"
			msg = username + " a fermé la connexion Oblireach à votre machine."
		} else {
			title = "Oblireach — Connection Closed"
			msg = username + " has closed the Oblireach connection to your machine."
		}
	}

	exe, err := os.Executable()
	if err != nil {
		log.Printf("notifySession: os.Executable: %v", err)
		return
	}

	cmdLine := fmt.Sprintf(`"%s" --notify-title "%s" --notify-msg "%s" --notify-timeout 8`,
		exe, title, msg)

	pid, rc := spawnInSessionGo(sessionID, cmdLine)
	if rc != 0 {
		log.Printf("notifySession: spawn in session %d failed: %d", sessionID, rc)
	} else {
		log.Printf("notifySession: toast PID %d in session %d", pid, sessionID)
	}
}
