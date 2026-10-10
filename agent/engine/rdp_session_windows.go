//go:build windows

package engine

/*
#cgo LDFLAGS: -luser32
#include <windows.h>
#include <wtsapi32.h>
#include "wts_dynamic.h" // WTSAPI32.DLL from System32 only, never a static import

#ifndef SM_REMOTESESSION
#define SM_REMOTESESSION 0x1000
#endif

// or_session_display: the WTS client protocol type (0 console, 1 ICA, 2 RDP)
// and connect state of a session; -1 when a value could not be read.
static void or_session_display(DWORD sid, int *proto, int *state) {
	*proto = -1; *state = -1;
	LPWSTR buf = NULL;
	DWORD n = 0;
	if (WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, sid, WTSClientProtocolType, &buf, &n) && buf) {
		if (n >= sizeof(USHORT)) *proto = (int)*(USHORT*)buf;
		WTSFreeMemory(buf);
	}
	buf = NULL; n = 0;
	if (WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, sid, WTSConnectState, &buf, &n) && buf) {
		if (n >= sizeof(INT)) *state = (int)*(INT*)buf;
		WTSFreeMemory(buf);
	}
}

// or_sm_remote: this process's session is shown by a remote client.
static int or_sm_remote(void) { return GetSystemMetrics(SM_REMOTESESSION) != 0; }
*/
import "C"

// SessionDisplay reads how a Windows session is displayed (WTS protocol type
// and connect state), from any process — the service uses it for its logs.
func SessionDisplay(sessionID int) sessionDisplayInfo {
	var proto, state C.int
	C.or_session_display(C.DWORD(sessionID), &proto, &state)
	d := sessionDisplayInfo{protocol: int(proto)}
	if state >= 0 {
		d.state = wtsStateName(int(state))
	}
	d.remote = isRemoteDisplay(false, d.protocol)
	return d
}

// ownSessionDisplay is SessionDisplay for this process's own session, with
// GetSystemMetrics(SM_REMOTESESSION) as well (the helper's view).
func ownSessionDisplay() sessionDisplayInfo {
	d := SessionDisplay(CurrentSessionID())
	d.remote = isRemoteDisplay(C.or_sm_remote() != 0, d.protocol)
	return d
}
