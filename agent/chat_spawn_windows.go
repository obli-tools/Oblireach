//go:build windows

package main

// The chat window runs AS THE SIGNED-IN USER of its session — never with the
// service's SYSTEM token (spawnInSessionGo, which the capture helper needs to
// inject input past UIPI). The chat only shows a WebView2 page: as SYSTEM on
// the user's desktop, the browser engine's own UI (print preview → "Save as
// PDF" → a file dialog, a file dropped on the window, its profile folder in
// the user's own %APPDATA%…) would hand the user SYSTEM rights. As the user,
// it holds exactly the user's rights: nothing to gain.
//
// No user signed in to the session (logon screen): no chat window — the
// caller reports chat_unavailable "no_user" (chatSpawnNoUser).

import (
	"log"
	"syscall"
	"unsafe"
)

var (
	csWtsapi32                = syscall.NewLazyDLL("wtsapi32.dll")
	csWTSQueryUserToken       = csWtsapi32.NewProc("WTSQueryUserToken")
	csUserenv                 = syscall.NewLazyDLL("userenv.dll")
	csCreateEnvironmentBlock  = csUserenv.NewProc("CreateEnvironmentBlock")
	csDestroyEnvironmentBlock = csUserenv.NewProc("DestroyEnvironmentBlock")
	csAdvapi32                = syscall.NewLazyDLL("advapi32.dll")
	csLookupPrivilegeValueW   = csAdvapi32.NewProc("LookupPrivilegeValueW")
	csAdjustTokenPrivileges   = csAdvapi32.NewProc("AdjustTokenPrivileges")
)

const (
	csCreateUnicodeEnvironment = 0x00000400
	csCreateNoWindow           = 0x08000000
	csSePrivilegeEnabled       = 0x00000002
	csErrorNoToken             = 1008 // ERROR_NO_TOKEN: nobody signed in to the session
)

type csLUIDAndAttributes struct {
	LowPart    uint32
	HighPart   int32
	Attributes uint32
}

type csTokenPrivileges struct {
	Count      uint32
	Privileges [1]csLUIDAndAttributes
}

// csEnablePrivilege enables a privilege the service (LocalSystem) holds but
// may not have enabled (CreateProcessAsUser with another user's token needs
// SeAssignPrimaryToken / SeIncreaseQuota).
func csEnablePrivilege(name string) {
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return
	}
	var tok syscall.Token
	if err := syscall.OpenProcessToken(self, syscall.TOKEN_ADJUST_PRIVILEGES|syscall.TOKEN_QUERY, &tok); err != nil {
		return
	}
	defer tok.Close()
	n16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return
	}
	var tp csTokenPrivileges
	if r, _, _ := csLookupPrivilegeValueW.Call(0, uintptr(unsafe.Pointer(n16)), uintptr(unsafe.Pointer(&tp.Privileges[0]))); r == 0 {
		return
	}
	tp.Count = 1
	tp.Privileges[0].Attributes = csSePrivilegeEnabled
	csAdjustTokenPrivileges.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&tp)), 0, 0, 0)
}

// spawnChatHelperAsUser starts cmdLine in session sessionID with the token of
// the user signed in to it (WTSQueryUserToken: for an administrator, the
// filtered, non-elevated token), on winsta0\default, with that user's
// environment. Returns (pid, 0), or (0, -GetLastError()):
// -csErrorNoToken (= chatSpawnNoUser) when nobody is signed in.
func spawnChatHelperAsUser(sessionID int, cmdLine string) (uint32, int) {
	if sessionID < 0 {
		return 0, -87 // ERROR_INVALID_PARAMETER
	}
	csEnablePrivilege("SeTcbPrivilege")
	csEnablePrivilege("SeAssignPrimaryTokenPrivilege")
	csEnablePrivilege("SeIncreaseQuotaPrivilege")

	var tok syscall.Token
	if r, _, e := csWTSQueryUserToken.Call(uintptr(uint32(sessionID)), uintptr(unsafe.Pointer(&tok))); r == 0 {
		code := csErrorNoToken
		if errno, ok := e.(syscall.Errno); ok && errno != 0 {
			code = int(errno)
		}
		log.Printf("chat: no user token for session %d (err=%d): no chat window", sessionID, code)
		return 0, -code
	}
	defer tok.Close()

	// The user's own environment (never the service's: its %APPDATA% /
	// %LOCALAPPDATA% hold the WebView2 profile, %TEMP% the helper's log).
	var env *uint16
	if r, _, _ := csCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&env)), uintptr(tok), 0); r == 0 || env == nil {
		log.Printf("chat: CreateEnvironmentBlock failed for session %d: chat window not started", sessionID)
		return 0, -13 // ERROR_INVALID_DATA
	}
	defer csDestroyEnvironmentBlock.Call(uintptr(unsafe.Pointer(env)))

	cmd16, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, -87 // ERROR_INVALID_PARAMETER
	}
	desktop, _ := syscall.UTF16PtrFromString(`winsta0\default`)
	si := syscall.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi syscall.ProcessInformation
	if err := syscall.CreateProcessAsUser(tok, nil, cmd16, nil, nil, false,
		csCreateUnicodeEnvironment|csCreateNoWindow, env, nil, &si, &pi); err != nil {
		code := int(syscall.ERROR_ACCESS_DENIED)
		if errno, ok := err.(syscall.Errno); ok && errno != 0 {
			code = int(errno)
		}
		log.Printf("chat: CreateProcessAsUser in session %d failed: %v", sessionID, err)
		return 0, -code
	}
	syscall.CloseHandle(pi.Thread)
	syscall.CloseHandle(pi.Process)
	log.Printf("spawnInSession(%d): chat window as the signed-in user (WTSQueryUserToken), PID %d", sessionID, pi.ProcessId)
	return pi.ProcessId, 0
}

// platformChatSessionUserName: the user signed in to the session (WTS), the
// name shown to the operator for the chat window's messages.
func platformChatSessionUserName(sessionID int) string {
	_, user, _ := sessionUser(sessionID)
	return user
}
