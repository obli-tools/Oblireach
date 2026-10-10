//go:build windows

package engine

/*
#include <windows.h>
#include <wtsapi32.h>
#include "wts_dynamic.h" // WTSAPI32.DLL from System32 only, never a static import
#include <versionhelpers.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	DWORD sessionId;
	int   state;
	char  username[256];
	char  domain[256];
	char  stationName[64];
} sessionDetail;

// enumWTSSessions fills a malloc'd array of sessionDetail.
// Caller must free(*out) with free().
static int enumWTSSessions(sessionDetail **out, int *count) {
	WTS_SESSION_INFOW *pSI = NULL;
	DWORD n = 0;
	if (!WTSEnumerateSessionsW(WTS_CURRENT_SERVER_HANDLE, 0, 1, &pSI, &n)) {
		return -(int)GetLastError();
	}
	sessionDetail *arr = (sessionDetail*)calloc(n, sizeof(sessionDetail));
	if (!arr) {
		WTSFreeMemory(pSI);
		return -1;
	}
	for (DWORD i = 0; i < n; i++) {
		arr[i].sessionId = pSI[i].SessionId;
		arr[i].state = (int)pSI[i].State;
		if (pSI[i].pWinStationName) {
			WideCharToMultiByte(CP_UTF8, 0, pSI[i].pWinStationName, -1,
				arr[i].stationName, sizeof(arr[i].stationName)-1, NULL, NULL);
		}
		LPWSTR pUser = NULL;
		DWORD  uLen  = 0;
		if (WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, pSI[i].SessionId,
				WTSUserName, &pUser, &uLen) && pUser && uLen > 2) {
			WideCharToMultiByte(CP_UTF8, 0, pUser, -1,
				arr[i].username, sizeof(arr[i].username)-1, NULL, NULL);
			WTSFreeMemory(pUser);
		}
		// The domain too: the session-follow same-user rules compare the
		// domain-qualified user, never the short name alone (the local
		// Administrator is not the domain Administrator).
		LPWSTR pDom = NULL;
		DWORD  dLen = 0;
		if (WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, pSI[i].SessionId,
				WTSDomainName, &pDom, &dLen) && pDom && dLen > 2) {
			WideCharToMultiByte(CP_UTF8, 0, pDom, -1,
				arr[i].domain, sizeof(arr[i].domain)-1, NULL, NULL);
		}
		if (pDom) WTSFreeMemory(pDom);
	}
	WTSFreeMemory(pSI);
	*out = arr;
	*count = (int)n;
	return 0;
}

static DWORD getConsoleSessionID(void) {
	return WTSGetActiveConsoleSessionId();
}

static DWORD getMySessionID(void) {
	DWORD id = 0;
	ProcessIdToSessionId(GetCurrentProcessId(), &id);
	return id;
}

// isWindowsServer returns 1 if this host is a Windows Server SKU
// (Server / Server Core / Server RDS), 0 for workstation/client SKUs.
// Uses VerifyVersionInfoW to avoid the manifest-gated GetVersionEx lies.
static int isWindowsServer(void) {
	OSVERSIONINFOEXW osvi;
	ZeroMemory(&osvi, sizeof(osvi));
	osvi.dwOSVersionInfoSize = sizeof(osvi);
	osvi.wProductType = VER_NT_WORKSTATION;
	DWORDLONG mask = VerSetConditionMask(0, VER_PRODUCT_TYPE, VER_EQUAL);
	// If VerifyVersionInfo returns FALSE, the OS is NOT a workstation, i.e.
	// it is a server (Windows Server / Domain Controller).
	return VerifyVersionInfoW(&osvi, VER_PRODUCT_TYPE, mask) ? 0 : 1;
}
*/
import "C"
import "unsafe"

// SessionInfo describes a Windows logon session.
//
// IsLoginPrompt is true when the session is sitting at Winlogon with no
// user logged in — picking it as a capture target shows the login screen.
// On RDS hosts this is the "connect to a fresh session" use case.
type SessionInfo struct {
	ID            int    `json:"id"`
	Username      string `json:"username"`
	// Domain is the session user's domain (WTSDomainName), kept agent-side for
	// the session-follow same-user rules (userIdent); not sent to the server.
	Domain        string `json:"-"`
	State         string `json:"state"`
	StationName   string `json:"stationName,omitempty"`
	IsConsole     bool   `json:"isConsole"`
	IsLoginPrompt bool   `json:"isLoginPrompt,omitempty"`
}

func wtsStateName(n int) string {
	switch n {
	case 0:
		return "Active"
	case 1:
		return "Connected"
	case 2:
		return "ConnectQuery"
	case 3:
		return "Shadow"
	case 4:
		return "Disconnected"
	case 5:
		return "Idle"
	case 6:
		return "Listen"
	default:
		return "Unknown"
	}
}

// EnumerateSessions returns WTS sessions relevant to show the admin
// (Active, Connected, Disconnected). Session 0 and headless service sessions
// are filtered out.
func EnumerateSessions() []SessionInfo {
	var pArr *C.sessionDetail
	var count C.int
	if rc := C.enumWTSSessions(&pArr, &count); rc != 0 || count == 0 {
		return nil
	}
	defer C.free(unsafe.Pointer(pArr))

	n := int(count)
	arr := (*[1 << 16]C.sessionDetail)(unsafe.Pointer(pArr))[:n:n]
	conID := uint32(C.getConsoleSessionID())

	isServer := C.isWindowsServer() != 0

	var out []SessionInfo
	for i := 0; i < n; i++ {
		s := arr[i]
		state := int(s.state)
		// Skip: Listen (6), Reset (7), Down (8), Init (9)
		if state >= 6 {
			continue
		}
		username := C.GoString(&s.username[0])
		stationName := C.GoString(&s.stationName[0])
		// Always skip Session 0 — it is the SCM/services session and has
		// no user display; capturing it yields a black screen or WMF failure.
		if s.sessionId == 0 {
			continue
		}
		// Empty-username sessions are either pre-login (Winlogon showing the
		// sign-in prompt) or truly idle slots. Expose them only as a "login
		// prompt" target: the operator can pick one to drive a new login,
		// useful on RDS hosts and when a workstation is at the lock screen
		// with no cached user.
		loginPrompt := false
		if username == "" {
			// Only Connected/ConnectQuery make sense as login targets.
			// Disconnected without a username is usually a listener slot.
			if state != 1 && state != 2 {
				continue
			}
			// On workstations we typically have a single console session —
			// an empty-username Connected session there is almost always
			// the logon screen. On RDS any Connected-empty slot is fair game.
			if !isServer && uint32(s.sessionId) != conID {
				continue
			}
			loginPrompt = true
		}
		domain := ""
		if username != "" {
			domain = C.GoString(&s.domain[0])
		}
		out = append(out, SessionInfo{
			ID:            int(s.sessionId),
			Username:      username,
			Domain:        domain,
			State:         wtsStateName(state),
			StationName:   stationName,
			IsConsole:     uint32(s.sessionId) == conID,
			IsLoginPrompt: loginPrompt,
		})
	}
	return out
}

// IsServer returns true if this host is a Windows Server SKU.
// Obliance uses this to offer the RDS session picker (shadow an existing
// user session vs. drive a new login).
func IsServer() bool { return C.isWindowsServer() != 0 }

// ConsoleSessionID returns the physical console session ID.
func ConsoleSessionID() int { return int(C.getConsoleSessionID()) }

// CurrentSessionID returns this process's own session ID.
func CurrentSessionID() int { return int(C.getMySessionID()) }

// FindCaptureSession returns the best WTS session ID to use for screen capture.
//
// On a physical machine the console session (returned by WTSGetActiveConsoleSessionId)
// is what we want. On a headless VM or server that is only accessed via RDP,
// WTSGetActiveConsoleSessionId returns 0xFFFFFFFF ("no active console session").
// In that case we scan the WTS session list and return the first Active session
// that has a logged-in user — i.e. the user's RDP session. If nothing is found
// we fall back to the service's own session ID so the caller can decide what to do.
func FindCaptureSession() int {
	consoleID := ConsoleSessionID()
	// 0xFFFFFFFF (cast to int on 64-bit = 4294967295) means no active console session.
	if uint32(consoleID) != 0xFFFFFFFF {
		return consoleID
	}
	// No physical console — pick the first Active WTS session with a logged-in user.
	for _, s := range EnumerateSessions() {
		if s.State == "Active" && s.Username != "" {
			return s.ID
		}
	}
	// Fallback: our own session (service session 0 — direct capture in Session 0).
	return CurrentSessionID()
}
