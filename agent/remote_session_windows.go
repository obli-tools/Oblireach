//go:build windows

package main

/*
#cgo LDFLAGS: -lwtsapi32 -ladvapi32 -luserenv
#include <windows.h>
#include <wtsapi32.h>
#include <userenv.h>
#include <tlhelp32.h>
#include <stdlib.h>
#include <wchar.h>

// enablePrivilege enables a named privilege in the given token.
// Returns TRUE on success; FALSE if the privilege is not held at all.
static BOOL enablePrivilege(HANDLE hToken, LPCWSTR privName) {
	TOKEN_PRIVILEGES tp;
	ZeroMemory(&tp, sizeof(tp));
	tp.PrivilegeCount = 1;
	tp.Privileges[0].Attributes = SE_PRIVILEGE_ENABLED;
	if (!LookupPrivilegeValueW(NULL, privName, &tp.Privileges[0].Luid))
		return FALSE;
	return AdjustTokenPrivileges(hToken, FALSE, &tp, 0, NULL, NULL);
}

// findProcPidInSession scans running processes for exeName (case-insensitive)
// running in the given WTS session and returns its PID (0 if not found).
// Used to locate winlogon.exe (SYSTEM, session-attached, Secure Desktop rights)
// in the target session so we can borrow its token — the same technique
// used by TeamViewer / RustDesk to capture UAC Secure Desktop and login
// screens from a service running in session 0.
static DWORD findProcPidInSession(DWORD dwSessionId, LPCWSTR exeName) {
	DWORD found = 0;
	HANDLE hSnap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
	if (hSnap == INVALID_HANDLE_VALUE) return 0;
	PROCESSENTRY32W pe;
	pe.dwSize = sizeof(pe);
	if (Process32FirstW(hSnap, &pe)) {
		do {
			if (_wcsicmp(pe.szExeFile, exeName) != 0) continue;
			DWORD sid = 0;
			if (ProcessIdToSessionId(pe.th32ProcessID, &sid) && sid == dwSessionId) {
				found = pe.th32ProcessID;
				break;
			}
		} while (Process32NextW(hSnap, &pe));
	}
	CloseHandle(hSnap);
	return found;
}

// tryBorrowToken opens the primary token of exeName in dwSessionId and
// returns it DIRECTLY — no DuplicateTokenEx. RustDesk's approach: calling
// DuplicateTokenEx creates a copy whose logon-session affinity is
// disconnected from the source token, which Windows treats as unprivileged
// for GPU / desktop-duplication access. Using the original token handle
// preserves that affinity and lets DXGI see the real hardware adapters
// instead of falling back to "Microsoft Basic Render Driver" (WARP).
static HANDLE tryBorrowToken(DWORD dwSessionId, LPCWSTR exeName, DWORD *outPid) {
	DWORD pid = findProcPidInSession(dwSessionId, exeName);
	if (outPid) *outPid = pid;
	if (pid == 0) return NULL;
	HANDLE hProc = OpenProcess(PROCESS_ALL_ACCESS, FALSE, pid);
	if (!hProc) return NULL;
	HANDLE hToken = NULL;
	if (!OpenProcessToken(hProc, TOKEN_ALL_ACCESS, &hToken)) {
		CloseHandle(hProc);
		return NULL;
	}
	CloseHandle(hProc);
	return hToken;
}

// openSessionSystemToken returns winlogon.exe's primary token from
// dwSessionId — borrowed directly, not duplicated. This is the exact
// technique RustDesk uses for no-user sessions (their LaunchProcessWin
// with as_user=FALSE). Any other process (LogonUI, dwm, csrss) gave
// identical E_ACCESSDENIED results in our testing, so we match RustDesk
// 1:1.
static HANDLE openSessionSystemToken(DWORD dwSessionId, DWORD *outDiag, DWORD *outErr, DWORD *outPid) {
	*outDiag = 0; *outErr = 0; *outPid = 0;
	HANDLE h = tryBorrowToken(dwSessionId, L"winlogon.exe", outPid);
	if (!h) {
		*outDiag = 1;
		*outErr = GetLastError();
	}
	return h;
}

// spawnInSession launches cmdLine inside the given Windows session.
//
// Token strategy (in order):
//  1. WTSQueryUserToken — the logged-in user's primary token. Required for
//     DXGI Desktop Duplication to see the session's outputs: DXGI is WinSta-
//     scoped and a process running on the Winlogon WinSta (which is what
//     the winlogon.exe token gives you) enumerates zero outputs for the
//     user's session. Works for any Active/Disconnected user session.
//  2. winlogon.exe's token — SYSTEM-level, attached to both Default and
//     Winlogon desktops. Only used when no user is logged in (e.g. console
//     at login screen). Grants Secure Desktop access for capturing the
//     sign-in UI and UAC (when relevant).
//  3. Crafted SYSTEM token with SetTokenSessionId. Last-resort fallback.
//
// outPID receives the PID of the spawned process on success.
// outStrategy receives: 0=WTSQueryUserToken, 1=winlogon token,
// 2=SetTokenSessionId fallback. outDiag/outErr/outWlPid expose why winlogon
// path was skipped when strategy=2. Returns 0 on success, negative
// GetLastError() on failure.
// desktopName parameter: if non-NULL, passed to STARTUPINFO.lpDesktop so
// the child process is created directly on that desktop (e.g.
// "winsta0\\winlogon" for capturing the sign-in UI). NULL falls back to
// "winsta0\\default". Starting the helper on winlogon desktop is the
// only way to make Magnification capture the login prompt — once a
// thread has any window on one desktop, SetThreadDesktop refuses to
// move it (ERROR_BUSY, 170).
// outProcess: if non-NULL, receives the child's process HANDLE (the caller
// owns it and must CloseHandle it) — the follow supervisor waits on / kills a
// helper through it, never by PID, so a recycled PID can never be hit. NULL
// closes it here (fire-and-forget spawns: toast, chat).
static int spawnInSessionOnDesktop(DWORD sessionId, wchar_t *cmdLine, wchar_t *desktopName,
		DWORD *outPID, DWORD *outStrategy, DWORD *outDiag, DWORD *outErr, DWORD *outWlPid,
		HANDLE *outProcess);

static int spawnInSession(DWORD sessionId, wchar_t *cmdLine, DWORD *outPID,
		DWORD *outStrategy, DWORD *outDiag, DWORD *outErr, DWORD *outWlPid) {
	return spawnInSessionOnDesktop(sessionId, cmdLine, NULL,
		outPID, outStrategy, outDiag, outErr, outWlPid, NULL);
}

static int spawnInSessionOnDesktop(DWORD sessionId, wchar_t *cmdLine, wchar_t *desktopName,
		DWORD *outPID, DWORD *outStrategy, DWORD *outDiag, DWORD *outErr, DWORD *outWlPid,
		HANDLE *outProcess) {
	HANDLE hToken = NULL;
	if (outProcess) *outProcess = NULL;
	*outStrategy = 255;
	*outDiag = 0; *outErr = 0; *outWlPid = 0;

	// No explicit privilege enabling — RustDesk's LaunchProcessWin doesn't
	// do it, they rely on the service running as LocalSystem which already
	// has all the needed privileges enabled by default. Any extra enabling
	// we do can subtly change token semantics and is unnecessary.

	// Strategy 0: build a SYSTEM primary token relocated to the target
	// session. SYSTEM integrity is above Medium which is what UIPI on
	// Windows Server 2025 requires for SendInput to reach Medium-IL user
	// apps — an explorer.exe-borrowed or WTSQueryUserToken-derived user
	// token runs at Medium IL and gets err=5 (ACCESS_DENIED) on every
	// SendInput call, even targeting the same user's own windows. SYSTEM
	// bypasses UIPI. Capture side still works because Magnification API
	// operates independently of token integrity.
	//
	// This is the historically working path that we briefly abandoned in
	// pursuit of RustDesk parity. RustDesk seems to tolerate whatever UIPI
	// does on their test OSes (older Server / Windows 10) but Server 2025
	// is stricter.
	{
		// Privileges must be enabled — LocalSystem holds them but they are
		// not always active in the current token.
		HANDLE hSelf = NULL;
		if (OpenProcessToken(GetCurrentProcess(),
				TOKEN_ADJUST_PRIVILEGES | TOKEN_QUERY, &hSelf)) {
			enablePrivilege(hSelf, L"SeTcbPrivilege");
			enablePrivilege(hSelf, L"SeAssignPrimaryTokenPrivilege");
			enablePrivilege(hSelf, L"SeIncreaseQuotaPrivilege");
			enablePrivilege(hSelf, L"SeDebugPrivilege");
			CloseHandle(hSelf);
		}
		HANDLE hSysToken = NULL;
		if (OpenProcessToken(GetCurrentProcess(), TOKEN_ALL_ACCESS, &hSysToken)) {
			HANDLE hPrimary = NULL;
			if (DuplicateTokenEx(hSysToken, TOKEN_ALL_ACCESS, NULL,
					SecurityImpersonation, TokenPrimary, &hPrimary)) {
				DWORD sid = sessionId;
				if (SetTokenInformation(hPrimary, TokenSessionId, &sid, sizeof(sid))) {
					hToken = hPrimary;
					*outStrategy = 0;
				} else {
					CloseHandle(hPrimary);
				}
			}
			CloseHandle(hSysToken);
		}
	}

	// Strategy 1: borrow a SYSTEM-context process token from the target
	// session (dwm.exe / csrss.exe / winlogon.exe, in that order).
	// winlogon's token is intentionally denied real GPU access by Windows
	// — DXGI returns only "Microsoft Basic Render Driver" (WARP) and
	// E_ACCESSDENIED on DuplicateOutput. DWM's token holds the GPU access
	// and gives us real adapters.
	if (!hToken) {
		hToken = openSessionSystemToken(sessionId, outDiag, outErr, outWlPid);
		if (hToken) *outStrategy = 1;
	}

	// Strategy 2: craft a SYSTEM token relocated to the target session.
	if (!hToken) {
		*outStrategy = 2;
		HANDLE hSysToken = NULL;
		if (!OpenProcessToken(GetCurrentProcess(), TOKEN_ALL_ACCESS, &hSysToken)) {
			return -(int)GetLastError();
		}
		if (!DuplicateTokenEx(hSysToken, TOKEN_ALL_ACCESS, NULL,
				SecurityImpersonation, TokenPrimary, &hToken)) {
			CloseHandle(hSysToken);
			return -(int)GetLastError();
		}
		CloseHandle(hSysToken);
		DWORD sid = sessionId;
		if (!SetTokenInformation(hToken, TokenSessionId, &sid, sizeof(sid))) {
			CloseHandle(hToken);
			return -(int)GetLastError();
		}
	}

	// Build the environment block from a real user token whenever possible
	// (TEMP/APPDATA paths, DISPLAY routing). Fall back to our own token
	// (SYSTEM-crafted) when no user is logged in. The old working path did
	// this unconditionally; trimming it introduced UIPI regressions that
	// showed up as SendInput err=5.
	LPVOID pEnv = NULL;
	{
		HANDLE hUserToken = NULL;
		if (WTSQueryUserToken(sessionId, &hUserToken)) {
			CreateEnvironmentBlock(&pEnv, hUserToken, FALSE);
			CloseHandle(hUserToken);
		}
		if (!pEnv) CreateEnvironmentBlock(&pEnv, hToken, FALSE);
	}

	STARTUPINFOW si;
	ZeroMemory(&si, sizeof(si));
	si.cb = sizeof(si);
	si.lpDesktop = desktopName ? desktopName : L"winsta0\\default";
	// Explicit desktop = put the child on the interactive desktop of the
	// target session. With lpDesktop=NULL Windows derives it from the
	// spawning token's logon-session defaults, which for a SYSTEM-crafted
	// token relocated via SetTokenInformation ends up on a service window
	// station. From there SendInput gets err=5 / ACCESS_DENIED even though
	// SYSTEM integrity should bypass UIPI — Windows also blocks service-
	// station processes from injecting into user desktops.

	PROCESS_INFORMATION pi;
	ZeroMemory(&pi, sizeof(pi));

	// CreateProcessAsUserW may modify cmdLine, so pass a writable copy.
	wchar_t *mutableCmd = _wcsdup(cmdLine);
	if (!mutableCmd) {
		if (pEnv) DestroyEnvironmentBlock(pEnv);
		CloseHandle(hToken);
		return -ERROR_NOT_ENOUGH_MEMORY;
	}

	DWORD flags = CREATE_UNICODE_ENVIRONMENT | CREATE_NO_WINDOW;
	BOOL ok = CreateProcessAsUserW(
		hToken, NULL, mutableCmd,
		NULL, NULL, FALSE,
		flags, pEnv, NULL, &si, &pi
	);
	free(mutableCmd);

	if (pEnv) DestroyEnvironmentBlock(pEnv);
	CloseHandle(hToken);

	if (!ok) {
		return -(int)GetLastError();
	}

	*outPID = pi.dwProcessId;
	if (outProcess) *outProcess = pi.hProcess;
	else CloseHandle(pi.hProcess);
	CloseHandle(pi.hThread);
	return 0;
}
*/
import "C"

import (
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/oblireach/agent/engine"
)

// spawnInSessionGo is a Go-friendly wrapper around the C spawnInSession function.
// Returns (pid, returnCode). returnCode 0 = success. Logs which token strategy
// was taken and, when the winlogon-token path is rejected, why.
func spawnInSessionGo(sessionID int, cmdLine string) (uint32, int) {
	cmdLineW, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, -1
	}
	var pid, strategy, diag, errCode, wlPid C.DWORD
	rc := int(C.spawnInSession(
		C.DWORD(sessionID),
		(*C.wchar_t)(unsafe.Pointer(cmdLineW)),
		&pid, &strategy, &diag, &errCode, &wlPid,
	))
	logSpawnStrategy(sessionID, uint32(strategy), uint32(diag), uint32(errCode), uint32(wlPid))
	return uint32(pid), rc
}

// logSpawnStrategy prints which token path the helper spawn used.
//
//	0 → the service's own SYSTEM token moved into the session
//	    (SetTokenSessionId): the helper runs AS SYSTEM on the user's
//	    desktop (SendInput past UIPI). The chat window never uses this path:
//	    it runs as the signed-in user (chat_spawn_windows.go).
//	1 → winlogon.exe's token (no user in session; Secure Desktop access).
//	2 → crafted SYSTEM + SetTokenSessionId fallback (last resort, no
//	    desktop or output visibility — rarely useful).
//
// For strategy=1 (winlogon), diag explains any earlier failure of the
// user-token path too if the code surfaces one.
func logSpawnStrategy(sessionID int, strategy, diag, errCode, wlPid uint32) {
	switch strategy {
	case 0:
		log.Printf("spawnInSession(%d): token strategy=system (service token moved into the session: runs as SYSTEM)", sessionID)
	case 1:
		log.Printf("spawnInSession(%d): token strategy=system-process (PID %d — no user in session)",
			sessionID, wlPid)
	case 2:
		reason := "unknown"
		switch diag {
		case 1:
			reason = "winlogon.exe not found in session"
		case 2:
			reason = fmt.Sprintf("OpenProcess(winlogon PID %d) failed (err=%d)", wlPid, errCode)
		case 3:
			reason = fmt.Sprintf("OpenProcessToken failed (err=%d)", errCode)
		case 4:
			reason = fmt.Sprintf("DuplicateTokenEx failed (err=%d)", errCode)
		}
		log.Printf("spawnInSession(%d): token strategy=fallback (%s)", sessionID, reason)
	}
}

// ── TCP pipe framing ──────────────────────────────────────────────────────────
//
//	Framing: [4-byte uint32 LE payload length][1-byte type][payload]
//
// The message types are the capture loop's Link types (link.go).

func pipeSend(w io.Writer, msgType byte, payload []byte) error {
	hdr := make([]byte, 5)
	binary.LittleEndian.PutUint32(hdr[:4], uint32(len(payload)))
	hdr[4] = msgType
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// pipeSendLocked serialises pipeSend across concurrent writers. pipeSend does
// TWO Write() calls (header, then payload). Go's net.Conn.Write is atomic per
// call, but the runtime does NOT prevent inter-call interleaving — so two
// goroutines calling pipeSend concurrently can produce the sequence
// [A.hdr, B.hdr, A.payload, B.payload] which corrupts the framing. Observed
// in 1.0.178 after moving input dispatch (and its SAS pipeSend) onto its own
// goroutine: a PipeTypeSAS could slot between the header and payload of a
// large H.264 frame, and the service's pipeRecv would consume the frame's
// payload as if it were the SAS message's body.
func pipeSendLocked(mu *sync.Mutex, w io.Writer, msgType byte, payload []byte) error {
	mu.Lock()
	defer mu.Unlock()
	return pipeSend(w, msgType, payload)
}

func pipeRecv(r io.Reader) (msgType byte, payload []byte, err error) {
	hdr := make([]byte, 5)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return
	}
	msgType = hdr[4]
	length := binary.LittleEndian.Uint32(hdr[:4])
	if length > 8*1024*1024 { // 8 MB sanity cap
		err = fmt.Errorf("pipe: oversized message %d bytes", length)
		return
	}
	if length > 0 {
		payload = make([]byte, length)
		_, err = io.ReadFull(r, payload)
	}
	return
}

// ── Helper mode ───────────────────────────────────────────────────────────────

// runHelperMode is called when this binary is launched with --capture-helper.
// It connects to addr (TCP), initialises DXGI capture + WMF encoder in its
// own session, then streams frames to the service process and handles input.
func runHelperMode(addr string) {
	// The helper runs as the interactive user — C:\ProgramData is not writable
	// by standard users, so setupLogging() falls back to stdout (discarded for
	// a no-window process).  Redirect to %TEMP% so crash messages are visible.
	// Dual logging: primary log in per-user TEMP (current behaviour) plus a
	// shared log under C:\Windows\Temp keyed by session+PID so the admin
	// can read it regardless of which user owns the helper.
	var logFiles []*os.File
	if tmpDir := os.TempDir(); tmpDir != "" {
		if f, err := os.OpenFile(
			filepath.Join(tmpDir, "oblireach-helper.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			logFiles = append(logFiles, f)
		}
	}
	if f, err := os.OpenFile(
		fmt.Sprintf(`C:\Windows\Temp\oblireach-helper-s%d-pid%d.log`,
			engine.CurrentSessionID(), os.Getpid()),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		logFiles = append(logFiles, f)
	}
	if len(logFiles) > 0 {
		writers := make([]io.Writer, len(logFiles))
		for i, f := range logFiles {
			writers[i] = f
		}
		log.SetOutput(io.MultiWriter(writers...))
		// Encoder diagnostic sink — use the first concrete file handle.
		engine.SetEncoderDiagFromLog(logFiles[0])
	}
	log.Printf("helper: connecting to service at %s", addr)

	var conn net.Conn
	var err error
	for i := 0; i < 20; i++ {
		conn, err = net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		log.Fatalf("helper: could not connect to service: %v", err)
	}
	defer conn.Close()

	// Mutex serialising all writes to conn. pipeSend does 2 Writes (header,
	// payload) and Go's net.Conn doesn't prevent inter-call interleaving
	// between goroutines. See pipeSendLocked doc comment for the corruption
	// pattern that broke CAD in 1.0.178.
	var helperPipeMu sync.Mutex
	pipeSendC := func(msgType byte, payload []byte) error {
		return pipeSendLocked(&helperPipeMu, conn, msgType, payload)
	}

	// One-time handshake (session follow): when the supervisor launched this
	// helper with --nonce, send the nonce as the very first frame so the
	// supervisor only trusts the process it spawned. The listener is a loopback
	// port open for ~15 s during which any local process could otherwise connect
	// and receive the operator's keystrokes (a password typed on the login
	// screen). This helper runs as SYSTEM, so a standard user cannot read the
	// nonce off its command line.
	if helperNonce != "" {
		if err := pipeSendC(engine.PipeTypeHello, []byte(helperNonce)); err != nil {
			log.Fatalf("helper: send hello failed: %v", err)
		}
	}

	// Ctrl+Alt+Del routing depends on whether this helper is running in the
	// physical console session or an RDP session. RustDesk's logic
	// (src/server/input_service.rs:2202-2212) is:
	//   - console session  → IPC to the SYSTEM service and let IT call
	//                        SendSAS(FALSE) from Session 0. Only this path
	//                        unlocks the "Press Ctrl+Alt+Del" secure-attention
	//                        screen of a locked console — the kernel routes
	//                        SAS from session 0 directly into Winlogon's
	//                        SAS dispatcher.
	//   - RDP session      → call SendSAS(FALSE) directly in THIS helper.
	//                        The helper runs with LocalSystem + SE_TCB in the
	//                        RDP session, so SendSAS posts SAS to the RDP
	//                        session's Winlogon.
	// In 1.0.184 I mistakenly unified both on the helper path, which broke
	// the console-unlock case (user stuck on "press CAD" screen). In
	// 1.0.175-1.0.183 I mistakenly unified both on the service path, which
	// broke RDP CAD (SAS from session 0 can't reach an RDP session's
	// Winlogon). Neither is right — RustDesk's split is.
	helperSessionID := engine.CurrentSessionID()
	cSessionID := engine.ConsoleSessionID()
	if helperSessionID == cSessionID {
		// Console. Pipe to service.
		engine.InputSASHook = func() {
			if err := pipeSendC(engine.PipeTypeSAS, nil); err != nil {
				log.Printf("helper: failed to pipe SAS to service: %v", err)
			}
		}
		log.Printf("helper: SAS route = service pipe (console session %d)", helperSessionID)
	} else {
		// RDP / non-console. Call SendSAS locally in this session.
		engine.InputSASHook = engine.InputSAS
		log.Printf("helper: SAS route = local inputSAS (non-console session %d, console=%d)",
			helperSessionID, cSessionID)
	}

	// Lock this goroutine to its OS thread for the entire helper lifetime.
	// COM/DXGI/WMF require all calls on the same thread where CoInitializeEx ran.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Keep the display awake for the whole helper lifetime. On Hyper-V VMs
	// with no active console viewer, the Hyper-V Video adapter enters an
	// idle power state where DXGI Desktop Duplication captures a blank
	// framebuffer — the agent streams "nothing" and the client sits on
	// "Waiting for agent to connect…". Opening Hyper-V Manager's console
	// window or launching RustDesk resolves it symptomatically because
	// both hold ES_DISPLAY_REQUIRED while active. We do the same here, so
	// the fix is intrinsic to Oblireach.
	engine.InputKeepDisplayAwake()

	// Log the helper's identity to help diagnose UIPI issues.
	// SYSTEM token = highest integrity, bypasses UIPI for admin windows.
	if u, err := os.UserHomeDir(); err == nil {
		log.Printf("helper: running as user home=%s", u)
	}

	// RustDesk doesn't call SetProcessWindowStation / SetThreadDesktop on
	// the capture thread — they let Windows route the process to the
	// correct WinSta/Desktop automatically from the spawning token. Doing
	// so explicitly adds token-ACL handshakes that can block DXGI access.

	// No-user (Winlogon / sign-in / lock screen) capture works because the
	// helper is spawned directly on winsta0\winlogon via STARTUPINFO.lpDesktop
	// in startCrossSessionStream when the target session has no user — DXGI
	// Desktop Duplication then runs natively against the physical display
	// adapter (Hyper-V Video / the console's real GPU output) that has the
	// Winlogon content already composed on it.
	//
	// We intentionally do NOT install or plug any Indirect Display Driver
	// here: neither TeamViewer nor RustDesk do for standard pre-login
	// capture (their IDDs are for privacy mode / headless-no-console use
	// cases that we haven't needed to support). The entire IDD saga was
	// chasing a distraction — the real fix was always the correct desktop
	// at spawn time.

	// Windows 7 / 2008 R2: desktop composition off for the session (GDI
	// capture, dwm_windows.go); restored when the helper ends (and by
	// Windows itself if it dies).
	restoreComposition := win7CompositionOffForSession()
	defer restoreComposition()

	// The capture / encode / input loop (capture_loop_windows.go), over the
	// service pipe.
	if err := engine.RunCaptureLoop(pipeLink{conn: conn, send: pipeSendC}); err != nil {
		log.Fatalf("helper: %v", err)
	}
}

// pipeLink is the capture helper's Link to the service: the TCP pipe
// framing (pipeSend / pipeRecv), writes serialised by the helper's mutex.
type pipeLink struct {
	conn net.Conn
	send func(msgType byte, payload []byte) error
}

func (l pipeLink) Send(msgType byte, payload []byte) error { return l.send(msgType, payload) }
func (l pipeLink) Recv() (byte, []byte, error)            { return pipeRecv(l.conn) }

// ── Cross-session stream: the follow supervisor and its legs ────────────────
//
// The supervisor owns the relay WebSocket (the same approved remote_sessions
// row, the same browser) for the whole stream. A "leg" is one capture-helper
// process serving the session currently in view. When the console session
// changes (logon, console re-attach, fast user switching, logoff), the watcher
// (session_watch_windows.go) tells the supervisor, which stops the old leg and
// spawns a new one in the right session WITHOUT dropping the relay: the viewers
// show a short "following the session…" notice (session_state) and keep their
// connection. On the first leg the behaviour is the same as before; the extra
// machinery only runs on a session change. The viewers already implement the
// contract (session_follow feature, session_state overlay) — no protocol change.

// legError carries a viewer error code for a leg that could not be brought up.
type legError struct {
	code string
	msg  string
}

func (e *legError) Error() string { return e.code + ": " + e.msg }

// legAbortedCode: the stream ended (viewer left, close_remote_tunnel) while a
// leg was being brought up — nothing to report.
const legAbortedCode = "aborted"

// leg is one capture-helper process and its loopback pipe.
type leg struct {
	conn        net.Conn
	pipeMu      sync.Mutex
	proc        syscall.Handle // the helper process, kept from CreateProcessAsUserW (released by stop)
	pid         uint32         // logging only — the process is waited on / killed by proc, never by PID
	sessionID   int
	hasUser     bool
	user        string
	ident       string
	gen         int // leg index: view-gen base and stale-signal filter
	initMsg     []byte
	expectClose atomic.Bool // set before we close on purpose: the reader stays quiet
}

func (l *leg) send(msgType byte, payload []byte) error {
	return pipeSendLocked(&l.pipeMu, l.conn, msgType, payload)
}

// stop ends the leg: a best-effort stop message, the pipe closed, then the
// helper is waited for (3 s) and terminated if still there — through its
// process handle, so a same-session respawn never fights the old helper over
// the single DXGI duplication, the input hooks or the watermark window, and a
// recycled PID can never be hit.
func (l *leg) stop() {
	l.expectClose.Store(true)
	if l.conn != nil {
		// Best-effort stop, bounded so a hung helper cannot block the switch.
		_ = l.conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		_ = l.send(engine.PipeTypeStop, nil)
		_ = l.conn.Close()
	}
	reapHelper(l.proc, 3*time.Second)
	l.proc = 0
}

// supervisor owns one follow stream (one relay WS, one leg at a time).
type supervisor struct {
	cfg      *Config
	token    string
	ws       *wsConn
	wsMu     sync.Mutex // serialises writes to ws (leg reader + control writes)
	operator string
	session  *engine.StreamSession
	// files: the operator may transfer files in this session (files_gate.go).
	// False: every leg gets --no-files, and the relay reader / leg reader
	// refuse file messages and frames in both directions as well.
	files bool

	ctrl  *followController
	cache *streamStateCache

	curLeg      atomic.Pointer[leg] // the current leg, read by the relay reader
	lastSession atomic.Int64        // the session in view or being switched to (close_remote_tunnel notifies it)
	legSeq      int                 // increments per spawned leg

	helperGoneCh chan int // gen of a leg whose helper died unexpectedly

	// noLegSince: since when no helper serves the stream (a switch in progress,
	// a failed switch being retried, a helper that died). Zero while a leg is up
	// and while reporting no_display (which has its own deadline).
	noLegSince        time.Time
	noDisplayDeadline time.Time // pinned session disconnected: end after this
	notifiedIdent     string    // the user last told "an operator is connected" (userIdent)

	// framesForwarded: video frames relayed to the viewer over the whole
	// stream (all legs). Zero = the viewer never had a picture: a stream that
	// then cannot follow its session ends for good (NoPictureEndCode) instead
	// of asking the viewers for a full reconnect that would loop.
	framesForwarded atomic.Int64

	// Lock the session when the connection ends (lock_on_end.go,
	// lock_on_end_windows.go): the session's lock state at connect and the
	// operator's choice. everLive: the first leg came up (run goroutine only).
	lockState lockOnEndState
	everLive  bool
}

// activeSupervisors maps a stream token to its supervisor (Windows follow
// path), so close_remote_tunnel can notify the session actually in view.
var activeSupervisors sync.Map // token → *supervisor

// streamCurrentSession returns the Windows session the stream is showing now
// (or switching to).
func streamCurrentSession(token string) (int, bool) {
	if v, ok := activeSupervisors.Load(token); ok {
		sup := v.(*supervisor)
		if l := sup.curLeg.Load(); l != nil {
			return l.sessionID, true
		}
		if sid := sup.lastSession.Load(); sid > 0 {
			return int(sid), true
		}
	}
	return 0, false
}

const (
	noDisplayWait = 600 * time.Second      // pinned session disconnected: wait this long
	switchSettle  = 500 * time.Millisecond // let a fresh logon session settle before spawning
	// switchDeadline bounds a session change: no helper up within this since
	// the leg went away → the stream ends with session_switch_timeout and the
	// viewers reconnect in full (design: "no init within 30 s").
	switchDeadline = 30 * time.Second
	// minSpawnBudget: the last spawn attempt of a switch still gets this long.
	minSpawnBudget = 10 * time.Second
	// firstLegBudget: the first leg keeps the historical timeouts (accept 15 s,
	// handshake, init 20 s).
	firstLegBudget  = 45 * time.Second
	helperAcceptMax = 15 * time.Second
	// helperHelloTimeout: a helper sends its hello right after connecting; a
	// local connection that says nothing is dropped after this.
	helperHelloTimeout = 3 * time.Second
	helperInitMax      = 20 * time.Second
	// endNoticeGrace: after reporting why the stream ends (tunnel_failed on
	// the command WS), keep the relay a moment so the server's coded error
	// normally reaches the viewer before the relay closes.
	endNoticeGrace = 300 * time.Millisecond
)

// startCrossSessionStream opens the relay and starts the follow supervisor.
// requestedSessionID decides the follow mode (<=0 = the console, see
// followRequestFor); resolved is the session validated by resolveTargetSession,
// where the first leg spawns; operator is who is connecting; files whether
// the operator may transfer files (files_gate.go).
func startCrossSessionStream(cfg *Config, token string, requestedSessionID, resolved int, operator string, files bool) error {
	ws, err := dialStreamWS(cfg, token)
	if err != nil {
		reportTunnelFailed(token, "internal", "could not reach the relay: "+err.Error())
		return fmt.Errorf("cross-session: WS connect failed: %w", err)
	}
	mode, pinned := openModeFor(requestedSessionID, engine.ConsoleSessionID())
	hasUser, user, ident := sessionUser(resolved)

	session := engine.NewStreamSession(token, ws, agentStreamHooks(token))
	activeStreams.Store(token, session)

	sup := &supervisor{
		cfg: cfg, token: token, ws: ws, operator: operator, session: session, files: files,
		ctrl:          newFollowController(mode, pinned, legInfo{session: resolved, hasUser: hasUser, user: user, ident: ident}),
		cache:         newStreamStateCache(),
		helperGoneCh:  make(chan int, 4),
		notifiedIdent: ident, // the open toast went to this session (startStream)
	}
	sup.lastSession.Store(int64(resolved))
	sup.lockState.reset(-1, "", false) // (about no session until run reads it)
	go sup.run(resolved, hasUser, user, ident)
	return nil
}

// wsSend serialises a control/keepalive write to the browser (the leg reader
// also writes frames; net serialises the byte stream, the mutex the framing).
func (s *supervisor) wsSend(op byte, payload []byte) error {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	return s.ws.WriteFrame(op, payload)
}

func (s *supervisor) sendSessionState(state, reason string, sid int, user string, waitSec int) {
	m := map[string]interface{}{"type": "session_state", "state": state}
	if reason != "" {
		m["reason"] = reason
	}
	if sid > 0 {
		m["sessionId"] = sid
	}
	if user != "" {
		m["user"] = user
	}
	if waitSec > 0 {
		m["waitSec"] = waitSec
	}
	b, _ := json.Marshal(m)
	_ = s.wsSend(0x1, b)
}

// endWith reports why the stream ends (the server fails the session and gives
// the viewer the coded error, retry per its RETRYABLE_AGENT_CODES) and keeps the
// relay a moment so that notice normally arrives before the relay closes.
func (s *supervisor) endWith(code, msg string) {
	reportTunnelFailed(s.token, code, msg)
	s.sleepOrStop(endNoticeGrace)
}

// endRetryable ends a stream whose session change could not be followed, so
// that the viewers reconnect in full (a fresh open, which resolves the session
// again). Both viewers refuse to auto-reconnect while a session change is
// pending (session_state switching / no_display), whatever the error's retry
// flag says; session_state live (reason switch_failed) clears that first, then
// the server's retry:true for session_switch_timeout drives the reconnect.
func (s *supervisor) endRetryable(code, msg string) {
	s.sendSessionState("live", "switch_failed", 0, "", 0)
	s.endWith(code, msg)
}

// sleepOrStop waits d, or less when the stream stops. Returns false when it
// stopped.
func (s *supervisor) sleepOrStop(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-s.session.Done():
		return false
	}
}

func (s *supervisor) run(target int, hasUser bool, user, ident string) {
	defer s.session.Stop()
	defer s.killLeg()
	activeSupervisors.Store(s.token, s)
	defer activeSupervisors.Delete(s.token)
	// Lock the session when the connection ends: its state read now, before
	// the operator can unlock it; decided when run returns, whatever the cause
	// (runs first: the stream is still registered as showing the session).
	s.lockTake(target, hasUser, ident, "connect")
	defer s.lockAtEnd()

	l, lerr := s.spawnLeg(target, hasUser, user, ident, 0, time.Now().Add(firstLegBudget))
	if lerr != nil {
		if lerr.code != legAbortedCode {
			log.Printf("Stream %s: first leg failed: %v", s.token, lerr)
			s.endWith(lerr.code, lerr.msg)
		}
		return
	}
	s.setLeg(l)
	log.Printf("Stream %s (follow): first leg in session %d (mode=%d)", s.token, target, s.ctrl.mode)
	if err := s.wsSend(0x1, l.initMsg); err != nil {
		return
	}
	s.everLive = true
	s.sendLockState() // (session_lock_state: the viewers' padlock, lock_on_end)
	go s.legReader(l)
	go s.relayReader()
	go s.browserKeepalive()

	poll := time.NewTicker(sessionPollInterval)
	defer poll.Stop()

	for {
		select {
		case <-s.session.Done():
			return
		case gen := <-s.helperGoneCh:
			if cur := s.curLeg.Load(); cur == nil || cur.gen != gen {
				continue // a leg we already replaced
			}
			if !s.onHelperGone() {
				return
			}
		case <-sessionChangeCh:
			if !s.tick() {
				return
			}
		case <-poll.C:
			if !s.tick() {
				return
			}
		}
	}
}

// onHelperGone handles the current leg's helper dying. It reaps the dead leg,
// tells the viewer a session change is in progress, and leaves the respawn to
// the debounced samples (tick): a logoff / console re-attach breaks the pipe
// BEFORE WTS reports the new state, so deciding now would respawn into the
// session being torn down. The whole wait is bounded by switchDeadline.
func (s *supervisor) onHelperGone() bool {
	sid := s.ctrl.leg.session
	if l := s.curLeg.Load(); l != nil {
		// Why it went away, for the log (a crash code, a fatal init error,
		// the session logging off) — read before killLeg reaps the process.
		if code, exited := helperExitCode(l.proc, time.Second); exited {
			log.Printf("Stream %s: helper PID %d (session %d) exited: %s — its log: %s",
				s.token, l.pid, l.sessionID, engine.HelperExitText(code), engine.HelperLogHint(l.sessionID, l.pid))
		} else {
			log.Printf("Stream %s: helper PID %d (session %d) closed its pipe but is still running — its log: %s",
				s.token, l.pid, l.sessionID, engine.HelperLogHint(l.sessionID, l.pid))
		}
	}
	s.killLeg()
	s.cutTransfers()
	now := time.Now()
	if !s.ctrl.helperGone(now) {
		log.Printf("Stream %s: the capture helper stopped %d times within %s — ending (session %d: %s, pictures relayed: %d)",
			s.token, helperRestartMax, helperRestartWindow, sid, engine.SessionDisplay(sid), s.framesForwarded.Load())
		if code, retryable := engine.NoPictureEndCode(s.framesForwarded.Load()); !retryable {
			s.endWith(code, engine.CaptureUnavailableText(sid, engine.SessionDisplay(sid)))
			return false
		}
		s.endWith("internal", "the capture helper keeps stopping")
		return false
	}
	s.markNoLeg(now)
	log.Printf("Stream %s: helper pipe (session %d) broke — waiting for the session to settle", s.token, sid)
	s.sendSessionState("switching", "helper_restart", sid, s.ctrl.leg.user, 0)
	return true
}

func (s *supervisor) markNoLeg(now time.Time) {
	if s.noLegSince.IsZero() {
		s.noLegSince = now
	}
}

// tick evaluates one WTS snapshot and enforces the deadlines.
func (s *supervisor) tick() bool {
	now := time.Now()
	if s.ctrl.noDisplay && !s.noDisplayDeadline.IsZero() && now.After(s.noDisplayDeadline) {
		// The viewers keep their "no display" state (pick another session /
		// retry by hand): a blind reconnect would only be refused again.
		log.Printf("Stream %s: pinned session did not come back within %s — ending", s.token, noDisplayWait)
		s.endWith("session_switch_timeout", "the session did not come back in time")
		return false
	}
	if !s.ctrl.noDisplay && !s.noLegSince.IsZero() && now.Sub(s.noLegSince) > switchDeadline {
		code, retryable := engine.NoPictureEndCode(s.framesForwarded.Load())
		if !retryable {
			// The viewer never had a picture in this stream: a full reconnect
			// would start the same capture again (viewers that reset their
			// reconnect budget on each init looped on "Reconnecting" forever).
			sid := s.ctrl.leg.session
			log.Printf("Stream %s: no capture helper for %s and no picture was ever relayed — ending for good (%s, session %d: %s)",
				s.token, switchDeadline, code, sid, engine.SessionDisplay(sid))
			s.endWith(code, engine.CaptureUnavailableText(sid, engine.SessionDisplay(sid)))
			return false
		}
		log.Printf("Stream %s: no capture helper for %s — ending (the viewer reconnects)", s.token, switchDeadline)
		s.endRetryable(code, "could not follow the session change")
		return false
	}
	snap := snapshotSessions(s.ctrl.mode, s.ctrl.pinned)
	dec, ok := s.ctrl.evaluate(snap)
	if !ok {
		return true
	}
	return s.act(dec)
}

// act carries out a committed decision. Returns false when the stream must end.
func (s *supervisor) act(dec followDecision) bool {
	switch dec.action {
	case actionSwitch:
		return s.doSwitch(dec)
	case actionNoDisplay:
		s.killLeg()
		s.cutTransfers()
		s.ctrl.appliedNoDisplay()
		s.noLegSince = time.Time{} // no_display has its own deadline
		s.noDisplayDeadline = time.Now().Add(noDisplayWait)
		log.Printf("Stream %s: session %d disconnected — no display (waiting %s)", s.token, dec.session, noDisplayWait)
		s.sendSessionState("no_display", dec.reason, dec.session, dec.user, int(noDisplayWait/time.Second))
		return true
	case actionErrorEnd:
		log.Printf("Stream %s: ending (%s)", s.token, dec.errCode)
		s.endWith(dec.errCode, "the remote session is no longer available")
		return false
	default:
		return true
	}
}

// doSwitch stops the current leg and brings up a new one in dec.session, keeping
// the relay open. A spawn that fails is not fatal: the controller retries on the
// next debounced samples (re-reading where the session went) until
// switchDeadline. Returns false only when the stream must end.
func (s *supervisor) doSwitch(dec followDecision) bool {
	s.noDisplayDeadline = time.Time{}
	s.markNoLeg(time.Now())
	s.lastSession.Store(int64(dec.session))
	s.sendSessionState("switching", dec.reason, dec.session, dec.user, 0)
	s.killLeg()
	s.cutTransfers()
	if dec.reason == "logon" || dec.reason == "reconnected" {
		// Let a fresh logon / re-attach settle so the session is Active and its
		// desktop exists before we spawn into it.
		if !s.sleepOrStop(switchSettle) {
			return false
		}
	}
	deadline := s.noLegSince.Add(switchDeadline)
	if time.Until(deadline) < minSpawnBudget {
		deadline = time.Now().Add(minSpawnBudget)
	}
	l, lerr := s.spawnLeg(dec.session, dec.hasUser, dec.user, dec.ident, switchDXGIWaitMs, deadline)
	if lerr != nil {
		if lerr.code == legAbortedCode {
			return false
		}
		log.Printf("Stream %s: switch to session %d failed: %v — retrying while the session settles", s.token, dec.session, lerr)
		s.ctrl.switchFailed(dec, time.Now())
		return true
	}
	s.setLeg(l)
	s.ctrl.appliedSwitch(dec)
	s.noLegSince = time.Time{}
	log.Printf("Stream %s: switched to session %d (%s, user=%q)", s.token, dec.session, dec.reason, dec.user)
	if err := s.wsSend(0x1, l.initMsg); err != nil {
		return false
	}
	s.sendSessionState("live", dec.reason, dec.session, dec.user, 0)
	// Lock on end: another session or user now — its own state, read now
	// (the choice made for the previous one does not carry over).
	if !s.lockState.sameTarget(dec.session, dec.ident) {
		s.lockTake(dec.session, dec.hasUser, dec.ident, dec.reason)
	}
	s.sendLockState()
	go s.legReader(l)

	// Replay the state a fresh helper does not know (recording pill, codec,
	// view, local-input block per the same-user rule).
	msgs, blockDropped := s.cache.replayMessages(dec.ident)
	for _, m := range msgs {
		_ = l.send(engine.PipeTypeInput, m)
	}
	if blockDropped {
		b, _ := json.Marshal(map[string]interface{}{"type": "input_block_status", "blocked": false})
		_ = s.wsSend(0x1, b)
	}
	// The blank screen never follows into another session: the viewer is
	// told it is off (it may ask again for the new session).
	if s.cache.takeBlank() {
		_ = s.wsSend(0x1, engine.BlankScreenStatusJSON(false, "session_changed"))
	}
	// A different user is now watched: notify their session.
	if s.operator != "" && dec.ident != "" && !sameUser(dec.ident, s.notifiedIdent) {
		s.notifiedIdent = dec.ident
		go notifySession(dec.session, s.operator, true)
	}
	// Refresh the server's session list (the session in view changed).
	requestHeartbeatNow()
	return true
}

func (s *supervisor) setLeg(l *leg) {
	s.curLeg.Store(l)
	if l != nil {
		s.lastSession.Store(int64(l.sessionID))
	}
}

// killLeg stops the current leg (see leg.stop).
func (s *supervisor) killLeg() {
	if l := s.curLeg.Swap(nil); l != nil {
		l.stop()
	}
}

// sendFileError tells the viewer a transfer was cut by a session change (the
// helper that owned it is gone; its abort is silent).
func (s *supervisor) sendFileError(tid uint32) {
	b, _ := json.Marshal(map[string]interface{}{"type": "file_error", "tid": tid, "code": "session_changed"})
	_ = s.wsSend(0x1, b)
}

// cutTransfers warns the viewer about every transfer in flight when the leg
// goes away (switch, no_display, helper death — F7).
func (s *supervisor) cutTransfers() {
	for _, tid := range s.cache.takeTids() {
		s.sendFileError(tid)
	}
}

var (
	errAcceptAborted = errors.New("the stream ended")
	errAcceptTimeout = errors.New("the capture helper did not connect back in time")
)

// maxPendingHelperConns bounds the loopback connections being checked at once.
const maxPendingHelperConns = 16

// acceptHelper accepts connections on a leg's loopback listener until one
// sends the right one-time hello (the nonce the supervisor put on the helper's
// command line), the deadline passes, or abort closes. Each connection is
// checked on its own goroutine with a short read deadline, so a local process
// that connects first — a port probe, or a user trying to block Reach (the
// listener is visible in netstat) — neither receives anything nor delays the
// real helper: it is simply dropped. The winner's read deadline is
// cleared; the caller closes the listener.
func acceptHelper(ln *net.TCPListener, nonce string, deadline time.Time, abort <-chan struct{}) (net.Conn, error) {
	_ = ln.SetDeadline(deadline)
	want := []byte(nonce)
	won := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	var decided atomic.Bool
	var pending atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			if decided.Load() || pending.Load() >= maxPendingHelperConns {
				_ = c.Close()
				continue
			}
			pending.Add(1)
			go func(c net.Conn) {
				defer pending.Add(-1)
				rd := time.Now().Add(helperHelloTimeout)
				if rd.After(deadline) {
					rd = deadline
				}
				_ = c.SetReadDeadline(rd)
				t, p, err := pipeRecv(c)
				if err == nil && t == engine.PipeTypeHello && subtle.ConstantTimeCompare(p, want) == 1 && decided.CompareAndSwap(false, true) {
					_ = c.SetReadDeadline(time.Time{})
					won <- c
					return
				}
				log.Printf("follow: a local connection to the helper port failed the handshake — dropped")
				_ = c.Close()
			}(c)
		}
	}()
	select {
	case c := <-won:
		return c, nil
	case <-abort:
		_ = ln.Close()
		if !decided.CompareAndSwap(false, true) {
			(<-won).Close() // a helper won at the same moment: not needed any more
		}
		return nil, errAcceptAborted
	case <-acceptErr:
		// Deadline (or the listener closed): a check still in flight may win.
		if decided.CompareAndSwap(false, true) {
			return nil, errAcceptTimeout
		}
		return <-won, nil
	}
}

// spawnLeg starts a supervised capture helper in sessionID, validates its
// one-time hello and reads its init, all before deadline. hasUser picks the
// Default vs Winlogon desktop; user / ident are the session's user (shown /
// same-user rules); dxgiWaitMs is passed to the helper (--dxgi-wait).
func (s *supervisor) spawnLeg(sessionID int, hasUser bool, user, ident string, dxgiWaitMs int, deadline time.Time) (*leg, *legError) {
	select {
	case <-s.session.Done():
		return nil, &legError{legAbortedCode, "the stream ended"}
	default:
	}
	lnAny, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, &legError{"internal", "could not open the local capture relay port"}
	}
	ln := lnAny.(*net.TCPListener)
	addr := ln.Addr().String()
	nonce := newLegNonce()
	s.legSeq++
	genBase := legGenBase(s.legSeq)

	exe, err := os.Executable()
	if err != nil {
		_ = ln.Close()
		return nil, &legError{"internal", "could not resolve the agent path"}
	}
	cmdLine, err := captureHelperCmdLine(exe, addr, nonce, genBase, dxgiWaitMs, !s.files)
	if err != nil {
		_ = ln.Close()
		return nil, &legError{"internal", err.Error()}
	}
	cmdLineW, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		_ = ln.Close()
		return nil, &legError{"internal", "bad command line"}
	}
	var desktopW *uint16
	if !hasUser {
		// No user in the session: spawn on the Winlogon desktop so the
		// Magnification fallback sees the sign-in / lock UI.
		desktopW, _ = syscall.UTF16PtrFromString(`winsta0\winlogon`)
	}

	var pid, strategy, diag, errCode, wlPid C.DWORD
	var hProc C.HANDLE
	rc := C.spawnInSessionOnDesktop(
		C.DWORD(sessionID),
		(*C.wchar_t)(unsafe.Pointer(cmdLineW)),
		(*C.wchar_t)(unsafe.Pointer(desktopW)),
		&pid, &strategy, &diag, &errCode, &wlPid, &hProc,
	)
	if rc != 0 {
		_ = ln.Close()
		return nil, &legError{"helper_spawn_failed", fmt.Sprintf("could not start the capture helper in session %d (code %d)", sessionID, -int(rc))}
	}
	proc := syscall.Handle(uintptr(unsafe.Pointer(hProc)))
	logSpawnStrategy(sessionID, uint32(strategy), uint32(diag), uint32(errCode), uint32(wlPid))
	disp := engine.SessionDisplay(sessionID)
	log.Printf("Stream %s: spawned helper PID %d in session %d (%s, user=%v, nonce leg %d) — its log: %s",
		s.token, uint32(pid), sessionID, disp, hasUser, s.legSeq, engine.HelperLogHint(sessionID, uint32(pid)))

	// A helper that dies before connecting (fatal capture init, crash) is
	// reported at once with its exit code, instead of after the whole
	// accept timeout — each viewer retry used to wait those 15 s.
	stopWatch := make(chan struct{})
	exited, watchDone := watchHelperExit(proc, stopWatch)
	endWatch := func() { close(stopWatch); <-watchDone }
	abort := make(chan struct{})
	go func() {
		select {
		case <-s.session.Done():
		case <-exited:
		case <-watchDone:
			return
		}
		close(abort)
	}()
	// exitedEarly: the helper is gone; its exit code for the log / viewer.
	exitedEarly := func(stage string) *legError {
		code, _ := helperExitCode(proc, time.Second)
		log.Printf("Stream %s: helper PID %d exited %s: %s (session %d: %s) — its log: %s",
			s.token, uint32(pid), stage, engine.HelperExitText(code), sessionID, disp, engine.HelperLogHint(sessionID, uint32(pid)))
		return &legError{"helper_timeout", fmt.Sprintf("the capture helper stopped %s (%s)", stage, engine.HelperExitText(code))}
	}

	acceptBy := time.Now().Add(helperAcceptMax)
	if deadline.Before(acceptBy) {
		acceptBy = deadline
	}
	conn, aerr := acceptHelper(ln, nonce, acceptBy, abort)
	_ = ln.Close() // one helper per leg: nothing else may connect from now on
	if aerr != nil {
		endWatch()
		select {
		case <-s.session.Done():
			reapHelper(proc, 0)
			return nil, &legError{legAbortedCode, "the stream ended"}
		default:
		}
		select {
		case <-exited:
			lerr := exitedEarly("before connecting")
			reapHelper(proc, 0)
			return nil, lerr
		default:
		}
		reapHelper(proc, 0)
		log.Printf("Stream %s: helper PID %d did not connect back within %s (session %d: %s) — its log: %s",
			s.token, uint32(pid), helperAcceptMax, sessionID, disp, engine.HelperLogHint(sessionID, uint32(pid)))
		return nil, &legError{"helper_timeout", "the capture helper did not connect back in time"}
	}
	endWatch()

	// Read the init (the helper retries DXGI up to dxgiWaitMs first). A stop
	// meanwhile closes the pipe so the read returns at once.
	initBy := time.Now().Add(helperInitMax)
	if deadline.Before(initBy) {
		initBy = deadline
	}
	_ = conn.SetReadDeadline(initBy)
	readDone := make(chan struct{})
	go func() {
		select {
		case <-s.session.Done():
			_ = conn.Close()
		case <-readDone:
		}
	}()
	mt, initMsg, err := pipeRecv(conn)
	close(readDone)
	if err != nil || mt != engine.PipeTypeInit {
		_ = conn.Close()
		select {
		case <-s.session.Done():
			reapHelper(proc, 0)
			return nil, &legError{legAbortedCode, "the stream ended"}
		default:
		}
		// A pipe that broke before the init: the helper died during its
		// capture / encoder start (it logs a fatal error first).
		if _, gone := helperExitCode(proc, time.Second); gone {
			lerr := exitedEarly("before sending its init")
			reapHelper(proc, 0)
			return nil, lerr
		}
		reapHelper(proc, 0)
		log.Printf("Stream %s: helper PID %d sent no init (%v, type 0x%02x) — session %d: %s — its log: %s",
			s.token, uint32(pid), err, mt, sessionID, disp, engine.HelperLogHint(sessionID, uint32(pid)))
		return nil, &legError{"helper_timeout", "the capture helper did not send its init"}
	}
	_ = conn.SetReadDeadline(time.Time{})

	return &leg{
		conn: conn, proc: proc, pid: uint32(pid),
		sessionID: sessionID, hasUser: hasUser, user: user, ident: ident,
		gen: s.legSeq, initMsg: initMsg,
	}, nil
}

// browserKeepalive pings the browser so intermediate proxies keep the relay WS.
func (s *supervisor) browserKeepalive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.session.Done():
			return
		case <-ticker.C:
			if err := s.wsSend(0x9, nil); err != nil {
				s.session.Stop()
				return
			}
		}
	}
}

// relayReader reads browser → service frames and routes them to the current
// leg. While there is no leg (a switch in progress), input is dropped, a new
// file transfer is refused at once (file_error session_changed) and state is
// cached for replay. When the browser leaves, the stream stops.
func (s *supervisor) relayReader() {
	defer s.session.Stop()
	for {
		opcode, payload, err := s.ws.ReadFrame()
		if err != nil {
			return
		}
		switch opcode {
		case 0x8: // close
			return
		case 0x9: // ping
			s.wsMu.Lock()
			_ = s.ws.SendPong(payload)
			s.wsMu.Unlock()
		case 0x1: // JSON input / control
			if engine.IsKeepaliveMsg(payload) {
				continue // the follow path has no inactivity timeout
			}
			if enabled, valid, isMsg := parseSetLockOnEnd(payload); isMsg {
				// Lock on end (lock_on_end.go): kept by the supervisor, never
				// sent to the helper; answered with session_lock_state.
				s.onSetLockOnEnd(enabled, valid)
				continue
			}
			if !s.files && engine.IsViewerFileMessage(payload) {
				// Not permitted in this session (files_gate.go): never reaches
				// the helper; a request the viewer waits on is answered.
				if reply := engine.FileRefusal(payload); reply != nil {
					_ = s.wsSend(0x1, reply)
				}
				continue
			}
			if engine.IsTypeTextMsg(payload) {
				// The vault's typed-secret frame: forward to the current leg's
				// helper (it types and answers type_text_result), or, with no
				// leg (a session switch in progress), answer session_changed
				// now. It is never state-cached / replayed into another session.
				if l := s.curLeg.Load(); l != nil {
					_ = l.send(engine.PipeTypeInput, payload)
				} else {
					_ = s.wsSend(0x1, engine.TypeTextResult(engine.PeekTypeTextID(payload), false, "session_changed"))
				}
				engine.WipeBytes(payload)
				continue
			}
			if engine.IsBlankScreenMsg(payload) {
				// Blank screen: to the current leg's helper with the operator
				// name the server gave (never the viewer's), or answered now
				// with no leg. Not replayed into another session (cache).
				if l := s.curLeg.Load(); l != nil {
					s.cache.observeToHelper(payload, l.ident)
					_ = l.send(engine.PipeTypeInput, engine.WithBlankOperator(payload, s.operator))
				} else {
					_ = s.wsSend(0x1, engine.BlankScreenStatusJSON(false, "session_changed"))
				}
				continue
			}
			if engine.IsDisplayModeMsg(payload) {
				// Resolution selector: to the current leg's helper (it lives in
				// the streamed session), or answered now with no leg. Never
				// state-cached / replayed into another session.
				if l := s.curLeg.Load(); l != nil {
					_ = l.send(engine.PipeTypeInput, payload)
				} else {
					_ = s.wsSend(0x1, engine.DisplayRefusal(payload, "session_changed"))
				}
				continue
			}
			l := s.curLeg.Load()
			if l == nil {
				if tid, ok := transferStartTid(payload); ok {
					s.sendFileError(tid)
					continue
				}
				s.cache.observeToHelper(payload, "")
				continue
			}
			s.cache.observeToHelper(payload, l.ident)
			_ = l.send(engine.PipeTypeInput, payload)
		case 0x2: // binary: only file-upload data (0x20 frames) goes to the helper
			if !s.files {
				continue // file transfers not permitted in this session
			}
			if len(payload) >= engine.FileFrameHeaderLen && payload[0] == engine.FrameTypeFileUp {
				if l := s.curLeg.Load(); l != nil {
					_ = l.send(engine.PipeTypeFileData, payload)
				}
			}
		}
	}
}

// legReader reads one leg's helper → service frames and forwards them to the
// browser. On an unexpected pipe break it signals helperGoneCh (unless the
// supervisor closed the leg on purpose).
func (s *supervisor) legReader(l *leg) {
	ab := engine.NewAdaptiveBitrate(30)
	for {
		select {
		case <-s.session.Done():
			return
		default:
		}
		_ = l.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		msgType, payload, err := pipeRecv(l.conn)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if l.expectClose.Load() {
				return // we replaced this leg on purpose
			}
			log.Printf("Stream %s: helper (session %d) disconnected: %v", s.token, l.sessionID, err)
			select {
			case s.helperGoneCh <- l.gen:
			default:
			}
			return
		}
		if !s.forwardHelperFrame(l, ab, msgType, payload) {
			return
		}
	}
}

// forwardHelperFrame forwards one helper → service frame to the browser.
// Returns false when the relay WS write failed (the reader then exits).
func (s *supervisor) forwardHelperFrame(l *leg, ab *engine.AdaptiveBitrate, msgType byte, payload []byte) bool {
	if !s.files {
		// File transfers not permitted in this session: a helper that missed
		// --no-files still sends nothing file-related to the viewer.
		if msgType == engine.PipeTypeFileDown || (msgType == engine.PipeTypeControl && engine.IsAgentFileMessage(payload)) {
			return true
		}
	}
	switch msgType {
	case engine.PipeTypeControl:
		s.cache.observeFromHelper(payload) // finished transfers, a block the local user lifted
		if err := s.wsSend(0x1, payload); err != nil {
			return false
		}
	case engine.PipeTypeInit:
		// A mid-leg re-init (monitor / mosaic / resolution change) must reach the
		// viewer too, or its input maps with the old geometry.
		if err := s.wsSend(0x1, payload); err != nil {
			return false
		}
	case engine.PipeTypeAudioV2, engine.PipeTypeFileDown:
		if err := s.wsSend(0x2, payload); err != nil {
			return false
		}
	case engine.PipeTypeSAS:
		// SendSAS must fire from Session 0 (the service) to reach Winlogon.
		engine.InputSAS()
	case engine.PipeTypeAudioData:
		frame := make([]byte, 1+len(payload))
		frame[0] = engine.FrameTypeAudio
		copy(frame[1:], payload)
		_ = s.wsSend(0x2, frame)
	case engine.PipeTypeFrame, engine.PipeTypeJPEGFrame, engine.PipeTypeVP9Frame, engine.PipeTypeH265Frame, engine.PipeTypeAV1Frame:
		ft := engine.FrameTypeH264
		switch msgType {
		case engine.PipeTypeJPEGFrame:
			ft = engine.FrameTypeJPEG
		case engine.PipeTypeVP9Frame:
			ft = engine.FrameTypeVP9
		case engine.PipeTypeH265Frame:
			ft = engine.FrameTypeH265
		case engine.PipeTypeAV1Frame:
			ft = engine.FrameTypeAV1
		}
		frame := make([]byte, 1+len(payload))
		frame[0] = ft
		copy(frame[1:], payload)
		sendStart := time.Now()
		if err := s.wsSend(0x2, frame); err != nil {
			return false
		}
		if s.framesForwarded.Add(1) == 1 {
			log.Printf("Stream %s: first picture relayed to the viewer (session %d)", s.token, l.sessionID)
		}
		if newBr := ab.Report(time.Since(sendStart)); newBr > 0 {
			brCmd, _ := json.Marshal(map[string]interface{}{"type": "set_bitrate", "bitrate": newBr})
			_ = l.send(engine.PipeTypeInput, brCmd)
			brMsg, _ := json.Marshal(map[string]interface{}{"type": "bitrate", "bitrate": newBr})
			_ = s.wsSend(0x1, brMsg)
		}
	}
	return true
}
