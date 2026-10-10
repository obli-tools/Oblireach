//go:build windows

package engine

/*
#cgo LDFLAGS: -luser32

#include <windows.h>
#include <stdio.h>
#include <stdarg.h>

#define OBLI_INPUT_LOG "C:\\Windows\\Temp\\oblireach-input.log"
#define OBLI_INPUT_LOG_1 "C:\\Windows\\Temp\\oblireach-input.log.1"
// 5 MiB cap: past it the file is rotated to .1 (one generation kept).
// the log no longer holds typed keys, but it must still not grow without bound.
#define OBLI_INPUT_LOG_MAX (5 * 1024 * 1024)

// obli_input_log_rotate rolls the log over to .1 when it reaches the cap, so
// the diagnostic file can never grow unbounded.
static void obli_input_log_rotate(void) {
    WIN32_FILE_ATTRIBUTE_DATA fad;
    if (!GetFileAttributesExA(OBLI_INPUT_LOG, GetFileExInfoStandard, &fad)) return;
    if (fad.nFileSizeHigh == 0 && fad.nFileSizeLow < OBLI_INPUT_LOG_MAX) return;
    DeleteFileA(OBLI_INPUT_LOG_1);
    MoveFileA(OBLI_INPUT_LOG, OBLI_INPUT_LOG_1);
}

// Input diagnostic log — flushes per line so we can trace even when SendInput
// silently no-ops (most common on desktop mismatch). Rotated at OBLI_INPUT_LOG_MAX.
// Disk writes off (SetDiskWrites(false)): the line goes to the host's
// diagnostic writer (diag_windows.go) instead, and the file is never touched.
extern void obliEngineDiagC(char *src, char *line);
static volatile LONG g_input_disk_log = 1;
static void input_set_disk_log(int on) { InterlockedExchange(&g_input_disk_log, on ? 1 : 0); }

static void inputLog(const char *fmt, ...) {
    if (!InterlockedCompareExchange(&g_input_disk_log, 0, 0)) {
        char buf[1024];
        va_list hap; va_start(hap, fmt);
        vsnprintf(buf, sizeof(buf), fmt, hap);
        va_end(hap);
        buf[sizeof(buf) - 1] = 0;
        obliEngineDiagC((char *)"input", buf);
        return;
    }
    obli_input_log_rotate();
    FILE *f = fopen(OBLI_INPUT_LOG, "a");
    if (!f) return;
    SYSTEMTIME st; GetLocalTime(&st);
    fprintf(f, "[%04d-%02d-%02d %02d:%02d:%02d.%03d pid=%lu tid=%lu] ",
        st.wYear, st.wMonth, st.wDay, st.wHour, st.wMinute, st.wSecond, st.wMilliseconds,
        (unsigned long)GetCurrentProcessId(), (unsigned long)GetCurrentThreadId());
    va_list ap; va_start(ap, fmt);
    vfprintf(f, fmt, ap);
    va_end(ap);
    fputc('\n', f);
    fflush(f);
    fclose(f);
}

// inputLogGo: non-variadic wrapper callable from Go via CGo (Go can't call
// C varargs functions).
static void inputLogGo(const char *msg) {
    inputLog("%s", msg);
}

// Forward declaration
static void switch_to_active_desktop(void);

// g_use_virtual_desk: set to 1 for multi-monitor, 0 for single monitor.
// MOUSEEVENTF_VIRTUALDESK can be slow on RDP sessions — avoid when not needed.
static int g_use_virtual_desk = 0;

static void set_multi_monitor(int multiMon) {
    g_use_virtual_desk = multiMon;
}

// send_mouse_move: moves mouse to absolute coordinates (0..65535 range).
static void send_mouse_move(int screen_w, int screen_h, int x, int y) {
    switch_to_active_desktop();
    INPUT inp = {0};
    inp.type = INPUT_MOUSE;
    inp.mi.dwFlags = MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE;
    if (g_use_virtual_desk) inp.mi.dwFlags |= MOUSEEVENTF_VIRTUALDESK;
    inp.mi.dx = (LONG)(x * 65535 / (screen_w > 1 ? screen_w - 1 : 1));
    inp.mi.dy = (LONG)(y * 65535 / (screen_h > 1 ? screen_h - 1 : 1));
    UINT sent = SendInput(1, &inp, sizeof(INPUT));
    static int log_count = 0;
    if (log_count < 5 || log_count % 100 == 0) {
        inputLog("mouse_move: sent=%u err=%lu x=%d y=%d screen=%dx%d vdk=%d",
            sent, sent ? 0 : GetLastError(), x, y, screen_w, screen_h, g_use_virtual_desk);
    }
    log_count++;
}

// send_mouse_button: press/release a mouse button.
// button values match browser MouseEvent.button: 0=left, 1=middle, 2=right.
static void send_mouse_button(int button, int down, int screen_w, int screen_h, int x, int y) {
    switch_to_active_desktop();
    INPUT inp = {0};
    inp.type = INPUT_MOUSE;
    inp.mi.dwFlags = MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE;
    if (g_use_virtual_desk) inp.mi.dwFlags |= MOUSEEVENTF_VIRTUALDESK;
    inp.mi.dx = (LONG)(x * 65535 / (screen_w > 1 ? screen_w - 1 : 1));
    inp.mi.dy = (LONG)(y * 65535 / (screen_h > 1 ? screen_h - 1 : 1));

    switch (button) {
    case 0: inp.mi.dwFlags |= down ? MOUSEEVENTF_LEFTDOWN   : MOUSEEVENTF_LEFTUP;   break;
    case 1: inp.mi.dwFlags |= down ? MOUSEEVENTF_MIDDLEDOWN : MOUSEEVENTF_MIDDLEUP; break;
    case 2: inp.mi.dwFlags |= down ? MOUSEEVENTF_RIGHTDOWN  : MOUSEEVENTF_RIGHTUP;  break;
    }
    SendInput(1, &inp, sizeof(INPUT));
}

// nudge_input_for_display_wake: relative zero-delta mouse move. Produces no
// visible cursor motion but counts as user input to the display driver,
// which brings a sleeping Hyper-V Video framebuffer back online. Used
// alongside SetThreadExecutionState(ES_DISPLAY_REQUIRED) at helper start.
static void nudge_input_for_display_wake(void) {
    INPUT inp = {0};
    inp.type = INPUT_MOUSE;
    inp.mi.dwFlags = MOUSEEVENTF_MOVE; // relative, no absolute flag, dx=dy=0
    inp.mi.dx = 0;
    inp.mi.dy = 0;
    SendInput(1, &inp, sizeof(INPUT));
}

// send_mouse_scroll: wheel delta (positive = up, negative = down), scaled by WHEEL_DELTA.
static void send_mouse_scroll(int delta) {
    INPUT inp = {0};
    inp.type = INPUT_MOUSE;
    inp.mi.dwFlags = MOUSEEVENTF_WHEEL;
    // Each browser "tick" is 3 units; multiply to get WHEEL_DELTA (120)
    inp.mi.mouseData = (DWORD)(delta * WHEEL_DELTA);
    SendInput(1, &inp, sizeof(INPUT));
}

// release_stale_modifiers: the viewer sends, with every key event, whether
// Ctrl / Shift / Alt / Meta are held on the operator's side. A modifier that
// is down in this session while the viewer says it is up was left pressed by
// a lost key-up (focus change in the browser, clipboard permission prompt,
// Ctrl+Tab…): release it, otherwise every later letter becomes a shortcut.
// `all` = release every modifier regardless of the flags (stream end).
static void release_vk_if_down(int vk_generic, int vk_left, int vk_right) {
    if (!(GetAsyncKeyState(vk_generic) & 0x8000) &&
        !(GetAsyncKeyState(vk_left) & 0x8000) &&
        !(GetAsyncKeyState(vk_right) & 0x8000)) return;
    INPUT inp[2] = {0};
    inp[0].type = INPUT_KEYBOARD; inp[0].ki.wVk = (WORD)vk_left;  inp[0].ki.dwFlags = KEYEVENTF_KEYUP;
    inp[1].type = INPUT_KEYBOARD; inp[1].ki.wVk = (WORD)vk_right; inp[1].ki.dwFlags = KEYEVENTF_KEYUP;
    SendInput(2, inp, sizeof(INPUT));
    inputLog("release_stale_modifiers: released vk=0x%02x", vk_generic);
}

// protect (bit0 ctrl, 1 shift, 2 alt, 3 meta) marks modifiers this stream
// injected itself and has NOT released yet: they must never be reconciled away.
// Without it, a viewer that sends per-key flags (the desktop client-app's
// sendSystemKeys sends {Meta,meta:true} then {r,meta:false}) makes the agent
// release the Win it just pressed, so Win+R opens the Start menu and types "r".
static void release_stale_modifiers(int ctrl, int shift, int alt, int meta, int all, int protect) {
    switch_to_active_desktop();
    if ((all || !ctrl)  && !(protect & 1)) release_vk_if_down(VK_CONTROL, VK_LCONTROL, VK_RCONTROL);
    if ((all || !shift) && !(protect & 2)) release_vk_if_down(VK_SHIFT,   VK_LSHIFT,   VK_RSHIFT);
    if ((all || !alt)   && !(protect & 4)) release_vk_if_down(VK_MENU,    VK_LMENU,    VK_RMENU);
    if ((all || !meta)  && !(protect & 8)) release_vk_if_down(VK_LWIN,    VK_LWIN,     VK_RWIN);
}

// is_extended_vk: keys sent with the E0 "extended" prefix on a real keyboard.
// Most share their scan code with a numeric-keypad key: injected without
// KEYEVENTF_EXTENDEDKEY, Windows (and the console host in particular) takes
// them for their keypad twin, so Up/Down/Left/Right typed 8/2/4/6 with Num
// Lock on (no history recall in a remote CMD / PowerShell), Home/End 7/1, etc.
static int is_extended_vk(int vk) {
    switch (vk) {
    case VK_UP: case VK_DOWN: case VK_LEFT: case VK_RIGHT:
    case VK_INSERT: case VK_DELETE: case VK_HOME: case VK_END:
    case VK_PRIOR: case VK_NEXT:
    case VK_RCONTROL: case VK_RMENU: case VK_LWIN: case VK_RWIN: case VK_APPS:
    case VK_DIVIDE: case VK_NUMLOCK: case VK_SNAPSHOT: case VK_CANCEL:
        return 1;
    }
    return 0;
}

// send_key: press or release a virtual key code, with its real scan code and
// the extended flag where a physical keyboard would set it (is_extended_vk).
static void send_key(int vk, int down) {
    switch_to_active_desktop();
    INPUT inp = {0};
    inp.type = INPUT_KEYBOARD;
    inp.ki.wVk = (WORD)vk;
    inp.ki.wScan = (WORD)MapVirtualKeyW((UINT)vk, MAPVK_VK_TO_VSC);
    if (is_extended_vk(vk)) inp.ki.dwFlags |= KEYEVENTF_EXTENDEDKEY;
    if (!down) inp.ki.dwFlags |= KEYEVENTF_KEYUP;
    UINT sent = SendInput(1, &inp, sizeof(INPUT));
    // Never log the VK or the direction of a key — that is the typed
    // content. Only report a FAILED SendInput (sent==0), so the "SendInput
    // broken" regression is still visible while no keystroke is recorded.
    if (sent == 0) {
        inputLog("send_key: sent=0 err=%lu", GetLastError());
    }
}

// switch_to_active_desktop: ensures the current thread is attached to the
// input desktop (either "Default" for normal use or "Winlogon" for the
// login screen). This allows SendInput to work on the logon screen.
// Called periodically — the cache is THREAD-LOCAL because SetThreadDesktop
// is per-thread; a global cache would let one thread race another into
// seeing a "fresh" g_currentDesk while its own thread is still on an old
// desktop, breaking SendInput for that thread. This was the regression
// introduced when capture + input were split onto separate goroutines.
static __thread HDESK t_currentDesk = NULL;
static __thread DWORD t_deskCheckTime = 0;
// Desktop name of the last successfully-observed input desktop. OpenInputDesktop
// returns a NEW handle on each call, so the previous `t_currentDesk != inputDesk`
// check was always true and forced a SetThreadDesktop on every 500ms tick —
// which always fails with ERROR_BUSY (170) because Go's runtime has created
// windows on this thread. Result: 500ms-ly syscall storm + fopen/fwrite log
// spam + zero actual desktop change. Now we cache the NAME; if it matches the
// last attached desktop, we short-circuit.
static __thread char t_currentDeskName[128] = {0};

// Legacy global (still read by force_switch_active_desktop for backward
// compatibility), but no longer authoritative — the per-thread state is.
static HDESK g_currentDesk = NULL;

static void switch_to_active_desktop(void) {
    DWORD now = GetTickCount();
    if (now - t_deskCheckTime < 500) return;
    t_deskCheckTime = now;

    // Query what the THREAD is currently attached to. The helper process was
    // spawned with CreateProcess(lpDesktop=...) which gives this thread an
    // inherited HDESK with FULL access rights (whatever the token allows).
    // We must NOT blindly SetThreadDesktop to a freshly-opened handle from
    // OpenInputDesktop — that handle only has the rights we asked for in
    // the mask, and SendInput then returns ERROR_ACCESS_DENIED on the
    // otherwise-same desktop. This regression broke ALL input in 1.0.180
    // after the dispatch goroutine (a fresh thread without Go-runtime
    // windows that made SetThreadDesktop silently fail) started "succeeding"
    // at the switch — landing on a limited handle.
    HDESK threadDesk = GetThreadDesktop(GetCurrentThreadId());
    char threadName[128] = {0};
    DWORD needed = 0;
    if (threadDesk) {
        GetUserObjectInformationA(threadDesk, UOI_NAME, threadName, sizeof(threadName), &needed);
    }

    // What desktop is currently receiving user input?
    HDESK inputDesk = OpenInputDesktop(0, FALSE, DESKTOP_SWITCHDESKTOP);
    if (!inputDesk) {
        inputLog("switch_desk: OpenInputDesktop failed err=%lu", GetLastError());
        return;
    }
    char inputName[128] = {0};
    GetUserObjectInformationA(inputDesk, UOI_NAME, inputName, sizeof(inputName), &needed);

    // Already on the right desktop → no-op. Preserving the inherited handle
    // preserves its rights (essential for SendInput). Only switch when the
    // input desktop has actually transitioned (UAC prompt, screen lock,
    // Winlogon/Default flip).
    if (strcmp(inputName, threadName) == 0) {
        if (t_currentDeskName[0] == 0) {
            strncpy(t_currentDeskName, threadName, sizeof(t_currentDeskName) - 1);
            t_currentDeskName[sizeof(t_currentDeskName) - 1] = 0;
            inputLog("switch_desk: already on '%s' (no switch needed, inherited rights preserved)", threadName);
        }
        CloseDesktop(inputDesk);
        return;
    }

    // Desktops differ. Reopen the target with broad rights so SendInput works
    // post-attach. GENERIC_ALL first; fall back to the union of desktop-
    // specific rights that SendInput documentation implies are required
    // (HOOKCONTROL + JOURNAL*).
    CloseDesktop(inputDesk);
    inputDesk = OpenInputDesktop(0, FALSE, GENERIC_ALL);
    DWORD openErrGeneric = inputDesk ? 0 : GetLastError();
    if (!inputDesk) {
        inputDesk = OpenInputDesktop(0, FALSE,
            DESKTOP_READOBJECTS | DESKTOP_WRITEOBJECTS | DESKTOP_SWITCHDESKTOP |
            DESKTOP_CREATEWINDOW | DESKTOP_CREATEMENU | DESKTOP_HOOKCONTROL |
            DESKTOP_JOURNALRECORD | DESKTOP_JOURNALPLAYBACK | DESKTOP_ENUMERATE);
    }
    if (!inputDesk) {
        inputLog("switch_desk: reopen broad rights failed generic=%lu narrow=%lu",
            openErrGeneric, GetLastError());
        return;
    }

    BOOL ok = SetThreadDesktop(inputDesk);
    DWORD setErr = ok ? 0 : GetLastError();
    if (t_currentDesk) CloseDesktop(t_currentDesk);
    t_currentDesk = inputDesk;
    g_currentDesk = inputDesk;

    // Log only when the transition target changed vs. what we last attempted
    // (or the outcome differs). Otherwise a stuck Secure-Desktop loop would
    // emit one log per force_switch_active_desktop call from the capture
    // reinit path — unhelpful noise.
    static __thread char t_lastAttempt[128] = {0};
    static __thread BOOL t_lastOk = -1;
    if (strncmp(inputName, t_lastAttempt, sizeof(t_lastAttempt)) != 0 || t_lastOk != ok) {
        inputLog("switch_desk: transition '%s' → '%s' (SetThreadDesktop ok=%d err=%lu)",
            threadName, inputName, ok, setErr);
        strncpy(t_lastAttempt, inputName, sizeof(t_lastAttempt) - 1);
        t_lastAttempt[sizeof(t_lastAttempt) - 1] = 0;
        t_lastOk = ok;
    }
    strncpy(t_currentDeskName, inputName, sizeof(t_currentDeskName) - 1);
    t_currentDeskName[sizeof(t_currentDeskName) - 1] = 0;
}

// force_switch_active_desktop: bypasses the 500ms cache and re-attaches the
// CALLING thread to the current input desktop. Thread-local so callers
// from different goroutines can't stomp each other's cached view.
static void force_switch_active_desktop(void) {
    t_deskCheckTime = 0;
    t_currentDeskName[0] = 0; // force cache miss on name match too
    switch_to_active_desktop();
}

// attach_to_default_desktop forces the calling process to winsta0 and the
// current thread to the Default desktop. Required for the helper's capture
// thread when spawned with a token (e.g. winlogon.exe) whose primary
// WindowStation is not winsta0 — DXGI Desktop Duplication returns
// E_ACCESSDENIED from the Winlogon WinSta, but succeeds on WinSta0\Default
// even when no user is logged in. Fills outName with "WinSta\Desktop" for
// diagnostic logging.
static void attach_to_default_desktop(char *outName, int outLen) {
    char wsname[128] = {0};
    char deskname[128] = {0};

    HWINSTA hWinsta = OpenWindowStationW(L"winsta0", FALSE,
        WINSTA_ENUMDESKTOPS | WINSTA_READATTRIBUTES | WINSTA_ACCESSCLIPBOARD |
        WINSTA_CREATEDESKTOP | WINSTA_WRITEATTRIBUTES | WINSTA_ACCESSGLOBALATOMS |
        WINSTA_EXITWINDOWS | WINSTA_ENUMERATE | WINSTA_READSCREEN);
    if (!hWinsta) {
        hWinsta = OpenWindowStationW(L"winsta0", FALSE, WINSTA_READATTRIBUTES);
    }
    if (hWinsta) {
        SetProcessWindowStation(hWinsta);
        DWORD size = 0;
        GetUserObjectInformationA(hWinsta, UOI_NAME, wsname, sizeof(wsname), &size);
    }

    HDESK hDesk = OpenDesktopW(L"Default", 0, FALSE,
        DESKTOP_READOBJECTS | DESKTOP_WRITEOBJECTS | DESKTOP_SWITCHDESKTOP |
        DESKTOP_CREATEWINDOW | DESKTOP_CREATEMENU | DESKTOP_HOOKCONTROL |
        DESKTOP_JOURNALRECORD | DESKTOP_JOURNALPLAYBACK | DESKTOP_ENUMERATE);
    if (!hDesk) {
        hDesk = OpenDesktopW(L"Default", 0, FALSE, GENERIC_READ | GENERIC_WRITE);
    }
    if (hDesk) {
        SetThreadDesktop(hDesk);
        if (g_currentDesk) CloseDesktop(g_currentDesk);
        g_currentDesk = hDesk;
        DWORD size = 0;
        GetUserObjectInformationA(hDesk, UOI_NAME, deskname, sizeof(deskname), &size);
    }

    if (outName && outLen > 1) {
        snprintf(outName, outLen, "%s\\%s",
            wsname[0] ? wsname : "?",
            deskname[0] ? deskname : "?");
        outName[outLen-1] = 0;
    }
}

// get_virtual_screen_size: returns the full virtual desktop dimensions.
static void get_virtual_screen_size(int *w, int *h, int *ox, int *oy) {
    *w  = GetSystemMetrics(SM_CXVIRTUALSCREEN);
    *h  = GetSystemMetrics(SM_CYVIRTUALSCREEN);
    *ox = GetSystemMetrics(SM_XVIRTUALSCREEN);
    *oy = GetSystemMetrics(SM_YVIRTUALSCREEN);
}

// ── Local-input block via low-level hooks ───────────────────────────────────
// "Cut the mouse and keyboard of THIS machine" the TeamViewer / AnyDesk way:
// the person sitting in front of the remote screen can no longer move anything,
// but the remote operator keeps full control.
//
// The old implementation called BlockInput(TRUE). That also blocked the
// operator: BlockInput suppresses ALL synthetic input too, so SendInput from
// the agent stopped working — clicking the button "cut the mouse for everyone,
// me included". We now install WH_MOUSE_LL + WH_KEYBOARD_LL low-level hooks:
// every event reaches our hook proc with a flag saying whether it was INJECTED
// (our SendInput) or physical (the local user). We swallow the physical ones
// and let the injected ones through, so only the local person is frozen.
//
// Threading: low-level hook callbacks are delivered through the message queue
// of the thread that installed the hook, so that thread MUST run a message
// loop or the hook stalls the whole desktop's input. We install and pump the
// loop on one dedicated OS thread (Go side: a runtime.LockOSThread goroutine)
// and stop it by PostThreadMessage-ing WM_OBLI_STOP_HOOKS to it — the unhook
// then happens on that same thread, as SetWindowsHookEx requires.
//
// LIMITATION (unavoidable, documented for callers): low-level hooks do NOT run
// on the secure desktop. Ctrl+Alt+Del, the UAC elevation prompt and the lock
// screen are therefore never blocked — the local user can always escape with
// Ctrl+Alt+Del. This matches TeamViewer and RustDesk; it is a safety property,
// not a bug (a stuck machine can always be freed at the console).

#ifndef LLMHF_INJECTED
#define LLMHF_INJECTED 0x00000001
#endif
#ifndef LLMHF_LOWER_IL_INJECTED
#define LLMHF_LOWER_IL_INJECTED 0x00000002
#endif
#ifndef LLKHF_INJECTED
#define LLKHF_INJECTED 0x00000010
#endif
#ifndef LLKHF_LOWER_IL_INJECTED
#define LLKHF_LOWER_IL_INJECTED 0x00000002
#endif

#define WM_OBLI_STOP_HOOKS (WM_USER + 41)

static HHOOK g_mouseHook = NULL;
static HHOOK g_kbHook = NULL;
static DWORD g_hookThreadId = 0;
static volatile LONG g_hookActive = 0; // 1 while both hooks are installed

static volatile LONG g_autoReleased = 0; // 1 when the loop lifted the block by itself

// llMouseProc swallows physical mouse events, passes injected (operator) ones.
// Button RELEASES always pass: a button the local user held when the block
// went on must not stay logically pressed (a release alone does nothing).
static LRESULT CALLBACK llMouseProc(int nCode, WPARAM wParam, LPARAM lParam) {
    if (nCode == HC_ACTION) {
        MSLLHOOKSTRUCT *p = (MSLLHOOKSTRUCT *)lParam;
        int isUp = (wParam == WM_LBUTTONUP || wParam == WM_RBUTTONUP ||
                    wParam == WM_MBUTTONUP || wParam == WM_XBUTTONUP);
        if (p && !isUp && !(p->flags & (LLMHF_INJECTED | LLMHF_LOWER_IL_INJECTED))) {
            return 1; // eat the local user's event
        }
    }
    return CallNextHookEx(NULL, nCode, wParam, lParam);
}

// llKeyboardProc swallows physical key events, passes injected (operator) ones.
// Key RELEASES always pass, for the same reason as the mouse buttons.
static LRESULT CALLBACK llKeyboardProc(int nCode, WPARAM wParam, LPARAM lParam) {
    if (nCode == HC_ACTION) {
        KBDLLHOOKSTRUCT *p = (KBDLLHOOKSTRUCT *)lParam;
        int isUp = (wParam == WM_KEYUP || wParam == WM_SYSKEYUP);
        if (p && !isUp && !(p->flags & (LLKHF_INJECTED | LLKHF_LOWER_IL_INJECTED))) {
            return 1; // eat the local user's event
        }
    }
    return CallNextHookEx(NULL, nCode, wParam, lParam);
}

// desk_is_default reports whether desk is the user's "Default" desktop.
static int desk_is_default(HDESK desk) {
    WCHAR name[64];
    DWORD needed = 0;
    if (!desk) return 0;
    if (!GetUserObjectInformationW(desk, UOI_NAME, name, sizeof(name), &needed)) return 0;
    name[63] = 0;
    return _wcsicmp(name, L"Default") == 0;
}

// input_desktop_is_default reports whether the desktop receiving input right
// now is the user's normal desktop. It is not while Ctrl+Alt+Del, the lock
// screen or a UAC prompt holds the input (the "Winlogon" secure desktop; this
// process may not even be allowed to open it: that also counts as "not").
static int input_desktop_is_default(void) {
    HDESK d = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS);
    int ok;
    if (!d) return 0;
    ok = desk_is_default(d);
    CloseDesktop(d);
    return ok;
}

// install_local_input_hooks installs both low-level hooks. MUST be called on
// the dedicated hook thread (the one that will pump run_local_input_hook_loop).
// Returns 1 on success, 0 if either hook could not be installed. Refuses when
// this thread or the input is not on the user's Default desktop (no user
// signed in, lock screen, Ctrl+Alt+Del): there is no local user to freeze
// there, and the hooks would sit on the wrong desktop.
static int install_local_input_hooks(void) {
    HINSTANCE hInst = GetModuleHandleW(NULL);
    if (!desk_is_default(GetThreadDesktop(GetCurrentThreadId())) || !input_desktop_is_default()) {
        inputLog("input_block: not on the user's Default desktop, block refused");
        return 0;
    }
    InterlockedExchange(&g_autoReleased, 0);
    g_hookThreadId = GetCurrentThreadId();
    g_mouseHook = SetWindowsHookExW(WH_MOUSE_LL, llMouseProc, hInst, 0);
    g_kbHook = SetWindowsHookExW(WH_KEYBOARD_LL, llKeyboardProc, hInst, 0);
    if (!g_mouseHook || !g_kbHook) {
        DWORD e = GetLastError();
        if (g_mouseHook) { UnhookWindowsHookEx(g_mouseHook); g_mouseHook = NULL; }
        if (g_kbHook) { UnhookWindowsHookEx(g_kbHook); g_kbHook = NULL; }
        g_hookThreadId = 0;
        inputLog("input_block: SetWindowsHookEx failed err=%lu", e);
        return 0;
    }
    InterlockedExchange(&g_hookActive, 1);
    inputLog("input_block: low-level hooks installed (local input frozen, operator passes)");
    return 1;
}

// run_local_input_hook_loop pumps the message loop that delivers the hook
// callbacks, and unhooks (on this same thread) when asked to stop or when the
// local user's escape hatch fires. Blocks until stop_local_input_hooks posts
// WM_OBLI_STOP_HOOKS, or until the input desktop leaves the user's Default
// desktop (Ctrl+Alt+Del, lock, UAC prompt): the block then lifts by itself,
// as BlockInput did on Ctrl+Alt+Del, and g_autoReleased tells the Go side to
// drop every owner and tell the viewers. The desktop is checked at most every
// 500 ms, also while input keeps arriving.
static void run_local_input_hook_loop(void) {
    MSG msg;
    DWORD lastCheck = GetTickCount();
    int stop = 0;
    while (!stop) {
        DWORD r = MsgWaitForMultipleObjects(0, NULL, FALSE, 500, QS_ALLINPUT);
        if (r == WAIT_FAILED) break;
        while (PeekMessageW(&msg, NULL, 0, 0, PM_REMOVE)) {
            if (msg.message == WM_OBLI_STOP_HOOKS || msg.message == WM_QUIT) { stop = 1; break; }
            TranslateMessage(&msg);
            DispatchMessageW(&msg);
        }
        if (!stop && GetTickCount() - lastCheck >= 500) {
            lastCheck = GetTickCount();
            if (!input_desktop_is_default()) {
                InterlockedExchange(&g_autoReleased, 1);
                inputLog("input_block: input left the Default desktop (Ctrl+Alt+Del, lock or UAC), block lifted");
                break;
            }
        }
    }
    if (g_mouseHook) { UnhookWindowsHookEx(g_mouseHook); g_mouseHook = NULL; }
    if (g_kbHook) { UnhookWindowsHookEx(g_kbHook); g_kbHook = NULL; }
    InterlockedExchange(&g_hookActive, 0);
    g_hookThreadId = 0;
    inputLog("input_block: low-level hooks removed (local input restored)");
}

// stop_local_input_hooks asks the hook thread to tear down and exit. Safe to
// call from any thread; a no-op if nothing is installed. The post is retried
// briefly: it fails if the thread's message queue does not exist yet.
static void stop_local_input_hooks(void) {
    int i;
    for (i = 0; i < 100; i++) {
        DWORD tid = g_hookThreadId;
        if (!tid) return; // the loop already ended
        if (PostThreadMessageW(tid, WM_OBLI_STOP_HOOKS, 0, 0)) return;
        Sleep(20);
    }
    inputLog("input_block: could not signal the hook thread (err=%lu)", GetLastError());
}

static int local_input_hooks_active(void) {
    return InterlockedCompareExchange(&g_hookActive, 0, 0);
}

static int local_input_hooks_auto_released(void) {
    return InterlockedCompareExchange(&g_autoReleased, 0, 0);
}

// fg_hkl: the keyboard layout of the foreground window's thread — the layout
// the remote user actually types with. VkKeyScanW alone uses the CALLING
// thread's layout (this process: often en-US), so a character like "\" was
// mapped for the wrong layout (same helper as typetext_windows.go).
static HKL fg_hkl(void) {
    HWND fg = GetForegroundWindow();
    if (fg) {
        DWORD tid = GetWindowThreadProcessId(fg, NULL);
        HKL h = GetKeyboardLayout(tid);
        if (h) return h;
    }
    return GetKeyboardLayout(0);
}

// vk_from_char: the VK code for a Unicode character on the remote layout
// (the foreground window's). Returns the VK code (low byte), or 0 if unmapped.
static int vk_from_char(unsigned short ch) {
    SHORT res = VkKeyScanExW((WCHAR)ch, fg_hkl());
    if (res == -1) return 0;
    return (int)(res & 0xFF);
}

// vk_shift_from_char: returns modifier flags for the character.
// bit 0 = Shift, bit 1 = Ctrl, bit 2 = Alt
static int vk_mods_from_char(unsigned short ch) {
    SHORT res = VkKeyScanExW((WCHAR)ch, fg_hkl());
    if (res == -1) return 0;
    return (int)((res >> 8) & 0x07);
}

// tap_char_with_mods: types one character key (vk) with EXACTLY the modifiers
// the remote layout needs for it (bit0 Shift, bit1 Ctrl, bit2 Alt; Ctrl+Alt =
// AltGr, sent as LCtrl + RMenu), whatever modifiers are held at that moment
// (the operator's AltGr arrives as separate Ctrl / Alt key events), then puts
// the held modifiers back. So AltGr+8 on an AZERTY viewer gives "\" on the
// remote machine whatever its layout, and a "\" that needs AltGr there gets it.
static void tap_char_with_mods(int vk, int mods) {
    static const int held_vk[6] = { VK_LSHIFT, VK_RSHIFT, VK_LCONTROL, VK_RCONTROL, VK_LMENU, VK_RMENU };
    int was[6];
    int i;
    for (i = 0; i < 6; i++) {
        was[i] = (GetAsyncKeyState(held_vk[i]) & 0x8000) != 0;
        if (was[i]) send_key(held_vk[i], 0);
    }
    int ctrl = (mods & 2) != 0, alt = (mods & 4) != 0, shift = (mods & 1) != 0;
    if (ctrl) send_key(VK_LCONTROL, 1);
    if (alt) send_key(ctrl ? VK_RMENU : VK_LMENU, 1);
    if (shift) send_key(VK_LSHIFT, 1);
    send_key(vk, 1);
    send_key(vk, 0);
    if (shift) send_key(VK_LSHIFT, 0);
    if (alt) send_key(ctrl ? VK_RMENU : VK_LMENU, 0);
    if (ctrl) send_key(VK_LCONTROL, 0);
    for (i = 5; i >= 0; i--) if (was[i]) send_key(held_vk[i], 1);
}

// lock_on: whether a lock key (VK_CAPITAL...) is toggled on in the remote
// session, as the foreground window's thread sees it (GetKeyState is per
// thread: this helper's own queue is attached to it for the read).
static int lock_on(int vk) {
    HWND fg = GetForegroundWindow();
    DWORD me = GetCurrentThreadId();
    DWORD tid = fg ? GetWindowThreadProcessId(fg, NULL) : 0;
    BOOL att = (tid && tid != me) ? AttachThreadInput(me, tid, TRUE) : FALSE;
    int on = (GetKeyState(vk) & 1) != 0;
    if (att) AttachThreadInput(me, tid, FALSE);
    return on;
}

// clipboard_get_text: reads Unicode text from the clipboard.
// Returns a malloc'd UTF-8 string, or NULL if no text.
static char* clipboard_get_text(void) {
    if (!OpenClipboard(NULL)) return NULL;
    HANDLE hData = GetClipboardData(CF_UNICODETEXT);
    if (!hData) { CloseClipboard(); return NULL; }
    wchar_t *wstr = (wchar_t*)GlobalLock(hData);
    if (!wstr) { CloseClipboard(); return NULL; }
    int len = WideCharToMultiByte(CP_UTF8, 0, wstr, -1, NULL, 0, NULL, NULL);
    char *utf8 = (char*)malloc(len);
    WideCharToMultiByte(CP_UTF8, 0, wstr, -1, utf8, len, NULL, NULL);
    GlobalUnlock(hData);
    CloseClipboard();
    return utf8;
}

// clipboard_seq: the clipboard sequence number, bumped on every change. Used
// to detect a remote clipboard change for push-sync without a message window.
static unsigned long clipboard_seq(void) { return GetClipboardSequenceNumber(); }

// clipboard_set_text: writes UTF-8 text to the clipboard.
static int clipboard_set_text(const char *utf8) {
    int wlen = MultiByteToWideChar(CP_UTF8, 0, utf8, -1, NULL, 0);
    if (wlen <= 0) return -1;
    HGLOBAL hMem = GlobalAlloc(GMEM_MOVEABLE, wlen * sizeof(wchar_t));
    if (!hMem) return -2;
    wchar_t *wstr = (wchar_t*)GlobalLock(hMem);
    MultiByteToWideChar(CP_UTF8, 0, utf8, -1, wstr, wlen);
    GlobalUnlock(hMem);
    if (!OpenClipboard(NULL)) { GlobalFree(hMem); return -3; }
    EmptyClipboard();
    if (!SetClipboardData(CF_UNICODETEXT, hMem)) {
        // Ownership was not transferred: the memory is still ours.
        GlobalFree(hMem);
        CloseClipboard();
        return -4;
    }
    CloseClipboard();
    return 0;
}

// clipboard_has_text: text is on the clipboard (no OpenClipboard needed).
static int clipboard_has_text(void) {
    return IsClipboardFormatAvailable(CF_UNICODETEXT) ? 1 : 0;
}
*/
import "C"

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Cached virtual screen dimensions — refreshed once when monitor offset changes.
// Avoids calling GetSystemMetrics on every mouse move (slow on RDP servers).
var (
	g_monOffX, g_monOffY int
	g_vsW, g_vsH         int
	g_vsOX, g_vsOY       int
	g_vsInited           bool
)

func refreshVirtualScreenCache() {
	var vw, vh, vox, voy C.int
	C.get_virtual_screen_size(&vw, &vh, &vox, &voy)
	g_vsW = int(vw)
	g_vsH = int(vh)
	g_vsOX = int(vox)
	g_vsOY = int(voy)
	g_vsInited = true

	// Detect multi-monitor: if virtual screen origin is non-zero or
	// virtual screen is larger than primary, we have multiple monitors.
	primaryW := int(C.GetSystemMetrics(0))  // SM_CXSCREEN
	primaryH := int(C.GetSystemMetrics(1))  // SM_CYSCREEN
	multiMon := g_vsW > primaryW || g_vsH > primaryH || g_vsOX != 0 || g_vsOY != 0
	if multiMon {
		C.set_multi_monitor(1)
	} else {
		C.set_multi_monitor(0)
	}
}

func setInputMonitorOffset(x, y int) {
	g_monOffX = x
	g_monOffY = y
	refreshVirtualScreenCache()
}

// virtualScreenCacheStale reports whether the virtual screen moved or changed
// size since the cache was filled: a mode change on ANY monitor (not only the
// captured one) changes the space SendInput normalises absolute positions over.
// The capture helper's geometry watchdog refreshes the cache when it is.
func virtualScreenCacheStale() bool {
	if !g_vsInited {
		return true
	}
	var vw, vh, vox, voy C.int
	C.get_virtual_screen_size(&vw, &vh, &vox, &voy)
	return int(vw) != g_vsW || int(vh) != g_vsH || int(vox) != g_vsOX || int(voy) != g_vsOY
}

func inputMouseMove(x, y int) {
	if !g_vsInited { refreshVirtualScreenCache() }
	absX := g_monOffX + x - g_vsOX
	absY := g_monOffY + y - g_vsOY
	C.send_mouse_move(C.int(g_vsW), C.int(g_vsH), C.int(absX), C.int(absY))
}

func inputMouseButton(button int, down bool, x, y int) {
	if !g_vsInited { refreshVirtualScreenCache() }
	absX := g_monOffX + x - g_vsOX
	absY := g_monOffY + y - g_vsOY
	d := C.int(0)
	if down { d = 1 }
	C.send_mouse_button(C.int(button), d, C.int(g_vsW), C.int(g_vsH), C.int(absX), C.int(absY))
}

// inputMoveDesktop / inputButtonDesktop take PHYSICAL virtual-desktop pixels
// (already resolved through the view's tile) and normalise them over the
// virtual screen for SendInput. Under Per-Monitor-V2 awareness the virtual
// screen and DXGI DesktopCoordinates are the same physical space, so a point
// on a mixed-DPI secondary monitor lands exactly (no mixed-DPI offset). Multi-monitor
// forces MOUSEEVENTF_VIRTUALDESK.
func inputMoveDesktop(vx, vy int) {
	if !g_vsInited {
		refreshVirtualScreenCache()
	}
	C.send_mouse_move(C.int(g_vsW), C.int(g_vsH), C.int(vx-g_vsOX), C.int(vy-g_vsOY))
}

func inputButtonDesktop(button int, down bool, vx, vy int) {
	if !g_vsInited {
		refreshVirtualScreenCache()
	}
	d := C.int(0)
	if down {
		d = 1
	}
	C.send_mouse_button(C.int(button), d, C.int(g_vsW), C.int(g_vsH), C.int(vx-g_vsOX), C.int(vy-g_vsOY))
}

// EnablePerMonitorDPIAwareness makes this process Per-Monitor-V2 DPI aware so
// GetSystemMetrics(SM_*VIRTUALSCREEN) reports the same PHYSICAL pixels as DXGI
// DesktopCoordinates. Without it a scaled secondary monitor gives the virtual
// screen in logical units while the capture offsets are physical, so the mouse
// is offset on that monitor. Must run before any window/DXGI use;
// harmless (and crisper) for the toast/chat/WebView2 windows. No-op on OSes
// that lack the API (falls back through the older awareness calls).
func EnablePerMonitorDPIAwareness() {
	u32 := newSystemLazyDLL("user32.dll")
	if setCtx := u32.NewProc("SetProcessDpiAwarenessContext"); setCtx.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (HANDLE)-4
		if r, _, _ := setCtx.Call(^uintptr(3)); r != 0 {
			return
		}
	}
	shcore := newSystemLazyDLL("shcore.dll")
	if setAw := shcore.NewProc("SetProcessDpiAwareness"); setAw.Find() == nil {
		// PROCESS_PER_MONITOR_DPI_AWARE = 2
		if r, _, _ := setAw.Call(2); r == 0 {
			return
		}
	}
	u32.NewProc("SetProcessDPIAware").Call()
}

func inputMouseScroll(delta int) {
	C.send_mouse_scroll(C.int(delta))
}

// inputTapChar: one character key with exactly the modifiers mods (see
// tap_char_with_mods). Windows only: nil elsewhere (stream.go checks).
func init() {
	inputTapCharFn = func(vk, mods int) { C.tap_char_with_mods(C.int(vk), C.int(mods)) }
	inputLockOnFn = func(vk int) bool { return C.lock_on(C.int(vk)) != 0 }
}

func inputKey(vk int, down bool) {
	d := C.int(0)
	if down { d = 1 }
	C.send_key(C.int(vk), d)
}

func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// inputReleaseStaleModifiers releases every modifier the viewer reports as
// NOT held but that is down in the session (lost key-up → "Ctrl stuck"),
// EXCEPT the ones this stream injected itself and has not released (protect
// mask — see release_stale_modifiers).
func inputReleaseStaleModifiers(ctrl, shift, alt, meta bool, protect int) {
	C.release_stale_modifiers(cBool(ctrl), cBool(shift), cBool(alt), cBool(meta), 0, C.int(protect))
}

// inputReleaseAllModifiers releases Ctrl/Shift/Alt/Win (end of a stream).
func inputReleaseAllModifiers() {
	C.release_stale_modifiers(0, 0, 0, 0, 1, 0)
}

// InputSAS sends a Secure Attention Sequence (Ctrl+Alt+Del).
// Must be called from the Session 0 service process: SendSAS from Session N
// (even with a SYSTEM token) returns TRUE but no secure-desktop transition
// occurs. The helper redirects here via PipeTypeSAS.
//
// Also defensively ensures HKLM\...\Policies\System!SoftwareSASGeneration is
// set to 1 (or 3) — if it's 0 or missing, SendSAS silently no-ops. We flip
// it for the call and restore after. Same belt-and-suspenders pattern as
// RustDesk's send_sas() in src/platform/windows.rs.
func InputSAS() {
	origValue, origPresent, restore := ensureSASPolicy()
	defer restore()
	_ = origValue
	_ = origPresent

	dll := newSystemLazyDLL("sas.dll")
	proc := dll.NewProc("SendSAS")
	findErr := proc.Find()
	if findErr != nil {
		logInputEvent(fmt.Sprintf("SAS: sas.dll SendSAS unavailable: %v", findErr))
		return
	}
	r1, _, callErr := proc.Call(0) // FALSE = called from service (not user)
	logInputEvent(fmt.Sprintf("SAS: SendSAS(FALSE) → r1=%d err=%v", r1, callErr))
}

// ensureSASPolicy reads HKLM\Software\Microsoft\Windows\CurrentVersion\Policies\System!SoftwareSASGeneration,
// promotes it to 1 if needed, and returns a restore func that writes back the
// original value (or deletes it if originally absent). Returns (origValue,
// origPresent, restore). origValue is meaningless when origPresent is false.
//
// Values: 0 = none, 1 = services only, 2 = ease-of-access only, 3 = both.
// SendSAS requires 1 or 3.
func ensureSASPolicy() (uint32, bool, func()) {
	const subKey = `Software\Microsoft\Windows\CurrentVersion\Policies\System`
	const valName = "SoftwareSASGeneration"

	if !DiskWritesEnabled() {
		// The host writes nothing on the machine: the policy is left as it is
		// (SendSAS still works where it already allows software SAS).
		logInputEvent("SAS: SoftwareSASGeneration left unchanged (local writes disabled by the host)")
		return 0, false, func() {}
	}

	k, _, err := registryCreateKey(subKey)
	if err != nil {
		logInputEvent(fmt.Sprintf("SAS: open policy key failed: %v", err))
		return 0, false, func() {}
	}
	defer registryCloseKey(k)

	orig, present, _ := registryGetDWORD(k, valName)
	if present && (orig == 1 || orig == 3) {
		return orig, true, func() {} // already allowed, nothing to do
	}
	if err := registrySetDWORD(k, valName, 1); err != nil {
		logInputEvent(fmt.Sprintf("SAS: set SoftwareSASGeneration=1 failed: %v", err))
		return orig, present, func() {}
	}
	logInputEvent(fmt.Sprintf("SAS: temporarily set SoftwareSASGeneration 0x%x→1 (was present=%v)", orig, present))
	return orig, present, func() {
		kk, _, err := registryOpenKey(subKey)
		if err != nil {
			return
		}
		defer registryCloseKey(kk)
		if present {
			_ = registrySetDWORD(kk, valName, orig)
		} else {
			_ = registryDeleteValue(kk, valName)
		}
	}
}

// ── Minimal registry helpers (syscall → advapi32) ───────────────────────────
// Used by ensureSASPolicy to flip SoftwareSASGeneration. Kept inline to avoid
// a go.mod bump for golang.org/x/sys/windows/registry.

const (
	_HKEY_LOCAL_MACHINE   = 0x80000002
	_KEY_READ             = 0x20019
	_KEY_SET_VALUE        = 0x0002
	_REG_DWORD            = 4
	_REG_OPTION_NON_VOLATILE = 0
)

var (
	advapi32                = newSystemLazyDLL("advapi32.dll")
	procRegCreateKeyExW     = advapi32.NewProc("RegCreateKeyExW")
	procRegOpenKeyExW       = advapi32.NewProc("RegOpenKeyExW")
	procRegCloseKey         = advapi32.NewProc("RegCloseKey")
	procRegQueryValueExW    = advapi32.NewProc("RegQueryValueExW")
	procRegSetValueExW      = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW     = advapi32.NewProc("RegDeleteValueW")
)

func registryCreateKey(subKey string) (uintptr, bool, error) {
	p, err := syscall.UTF16PtrFromString(subKey)
	if err != nil {
		return 0, false, err
	}
	var hKey uintptr
	var disp uint32
	r, _, callErr := procRegCreateKeyExW.Call(
		uintptr(_HKEY_LOCAL_MACHINE),
		uintptr(unsafe.Pointer(p)),
		0, 0,
		uintptr(_REG_OPTION_NON_VOLATILE),
		uintptr(_KEY_READ|_KEY_SET_VALUE),
		0,
		uintptr(unsafe.Pointer(&hKey)),
		uintptr(unsafe.Pointer(&disp)),
	)
	if r != 0 {
		return 0, false, fmt.Errorf("RegCreateKeyExW: %v (code=%d)", callErr, r)
	}
	return hKey, disp == 1, nil // disp=1 = REG_CREATED_NEW_KEY
}

func registryOpenKey(subKey string) (uintptr, bool, error) {
	p, err := syscall.UTF16PtrFromString(subKey)
	if err != nil {
		return 0, false, err
	}
	var hKey uintptr
	r, _, callErr := procRegOpenKeyExW.Call(
		uintptr(_HKEY_LOCAL_MACHINE),
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(_KEY_READ|_KEY_SET_VALUE),
		uintptr(unsafe.Pointer(&hKey)),
	)
	if r != 0 {
		return 0, false, fmt.Errorf("RegOpenKeyExW: %v (code=%d)", callErr, r)
	}
	return hKey, true, nil
}

func registryCloseKey(hKey uintptr) {
	procRegCloseKey.Call(hKey)
}

func registryGetDWORD(hKey uintptr, name string) (uint32, bool, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, false, err
	}
	var regType uint32
	var data uint32
	size := uint32(4)
	r, _, _ := procRegQueryValueExW.Call(
		hKey,
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(unsafe.Pointer(&regType)),
		uintptr(unsafe.Pointer(&data)),
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 {
		return 0, false, nil // missing value — not an error for our use
	}
	if regType != _REG_DWORD {
		return 0, false, fmt.Errorf("value is not REG_DWORD (type=%d)", regType)
	}
	return data, true, nil
}

func registrySetDWORD(hKey uintptr, name string, value uint32) error {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	v := value
	r, _, callErr := procRegSetValueExW.Call(
		hKey,
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(_REG_DWORD),
		uintptr(unsafe.Pointer(&v)),
		4,
	)
	if r != 0 {
		return fmt.Errorf("RegSetValueExW: %v (code=%d)", callErr, r)
	}
	return nil
}

func registryDeleteValue(hKey uintptr, name string) error {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	r, _, callErr := procRegDeleteValueW.Call(
		hKey,
		uintptr(unsafe.Pointer(p)),
	)
	if r != 0 {
		return fmt.Errorf("RegDeleteValueW: %v (code=%d)", callErr, r)
	}
	return nil
}

// logInputEventImpl writes a short line to the shared input log so we can trace
// what the helper is actually doing with operator input. It is reached only
// through the logInputEvent hook (input_log.go), so tests can prove no
// keystroke content ever reaches it.
func logInputEventImpl(msg string) {
	cstr := C.CString(msg)
	C.inputLogGo(cstr)
	C.free(unsafe.Pointer(cstr))
}

// inputSetDiskLog passes SetDiskWrites to the C sink (inputLog).
func inputSetDiskLog(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	C.input_set_disk_log(v)
}

// inputVKFromKey converts a browser e.key string (single character) to
// the correct Windows VK code for the remote system's keyboard layout.
// Returns (vk, needsShift). If the character can't be mapped, returns (0, false).
func inputVKFromKey(key string) (vk int, mods int) {
	runes := []rune(key)
	if len(runes) != 1 {
		return 0, 0
	}
	ch := runes[0]
	vk = int(C.vk_from_char(C.ushort(ch)))
	mods = int(C.vk_mods_from_char(C.ushort(ch)))
	return vk, mods
}

func clipboardGet() string {
	cstr := C.clipboard_get_text()
	if cstr == nil {
		return ""
	}
	s := C.GoString(cstr)
	C.free(unsafe.Pointer(cstr))
	return s
}

func clipboardSet(text string) {
	clipboardSetText(text)
}

// clipboardSetText writes text to the clipboard and reports whether it
// worked. OpenClipboard fails while another program holds the clipboard
// open (clipboard managers, rdpclip, an app mid-copy): retried for ~200 ms
// instead of silently doing nothing.
func clipboardSetText(text string) bool {
	cstr := C.CString(text)
	defer C.free(unsafe.Pointer(cstr))
	for i := 0; i < 10; i++ {
		rc := int(C.clipboard_set_text(cstr))
		if rc == 0 {
			return true
		}
		if rc != -3 { // not "clipboard busy"
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// clipboardHasText reports text on the clipboard (a read that returned ""
// while this is true hit a busy clipboard and must be retried).
func clipboardHasText() bool { return C.clipboard_has_text() != 0 }

// clipboardSeq returns the OS clipboard sequence number (change counter).
func clipboardSeq() uint32 { return uint32(C.clipboard_seq()) }

// winHookState owns the single dedicated OS thread that installs the low-level
// hooks and pumps their message loop. Access is serialised by mu so concurrent
// set_input_block requests (or a request racing a stream teardown) can't leave
// two loops or a half-torn-down one.
var winHookState struct {
	mu      sync.Mutex
	running bool
	stopped chan struct{} // closed when the hook goroutine has fully exited
}

// platformInputBlockSupported reports that this build can freeze LOCAL input
// without freezing the operator (the low-level-hook path). Only Windows can;
// it is what gates the input_block_local feature advertised to viewers.
func platformInputBlockSupported() bool { return true }

// platformInputBlockApply installs (want=true) or removes (want=false) the
// local-input hooks and returns the state ACTUALLY in effect afterwards: true
// only when the hooks are installed and running. Idempotent — a want that
// matches the current state is a no-op. Called by the pure-Go block manager
// (input_block.go) under its own lock; never call it directly.
func platformInputBlockApply(want bool) bool {
	winHookState.mu.Lock()
	defer winHookState.mu.Unlock()

	if want && !winHookState.running {
		started := make(chan bool, 1)
		stopped := make(chan struct{})
		go func() {
			// The hooks, their message loop and the matching UnhookWindowsHookEx
			// must all live on ONE thread — lock it for the goroutine's life.
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ok := C.install_local_input_hooks() != 0
			started <- ok
			if !ok {
				close(stopped)
				return
			}
			C.run_local_input_hook_loop() // blocks until stop_local_input_hooks
			close(stopped)
			if C.local_input_hooks_auto_released() != 0 {
				// The local user's escape hatch fired (Ctrl+Alt+Del, lock, UAC):
				// drop every owner and tell the viewers. In a new goroutine: the
				// manager takes its lock, then winHookState.mu.
				if f := onPlatformAutoRelease; f != nil {
					go f()
				}
			}
		}()
		if <-started {
			winHookState.running = true
			winHookState.stopped = stopped
		} else {
			<-stopped // install failed: let the goroutine finish, stay unblocked
		}
	} else if !want && winHookState.running {
		C.stop_local_input_hooks()
		select {
		case <-winHookState.stopped:
			winHookState.running = false
			winHookState.stopped = nil
		case <-time.After(5 * time.Second):
			// The hook thread did not answer: report the truth (probably still
			// blocked); the next request retries, and the hooks go with the
			// process in any case.
			log.Printf("input_block: hook thread did not stop within 5 s")
		}
	}

	// Report the C-side truth (hooks actually installed), which the goroutine
	// set before signalling install completion — so a failed SetWindowsHookEx
	// surfaces as blocked:false to the viewer, not an optimistic "blocked".
	return winHookState.running && C.local_input_hooks_active() != 0
}

// InputKeepDisplayAwake tells Windows to keep the display powered on for
// the duration of this process. Required when capturing the console session
// of a Hyper-V VM (or any physical host whose monitor-timeout power policy
// has kicked in): DXGI Desktop Duplication against a "sleeping" display
// returns a blank/stale framebuffer, so the operator sees a never-
// transitioning "Waiting for agent to connect…" because init is sent but no
// frame ever decodes. Opening Hyper-V Manager's console window or launching
// RustDesk had the same wake effect — both hold ES_DISPLAY_REQUIRED.
//
// Must be called from a thread in the target session (the helper's thread
// after spawn is correct). The state persists until the process exits or
// another SetThreadExecutionState(ES_CONTINUOUS) resets it.
//
// Also injects a zero-delta mouse move: some display drivers (Hyper-V
// Video specifically) need an input event on the wire to bring the
// framebuffer out of its idle power state, not just a policy flag.
func InputKeepDisplayAwake() {
	k32 := newSystemLazyDLL("kernel32.dll")
	proc := k32.NewProc("SetThreadExecutionState")
	const (
		esContinuous      = 0x80000000
		esDisplayRequired = 0x00000002
		esSystemRequired  = 0x00000001
	)
	r1, _, callErr := proc.Call(uintptr(esContinuous | esDisplayRequired | esSystemRequired))
	logInputEvent(fmt.Sprintf("wake: SetThreadExecutionState → prev=0x%x err=%v", r1, callErr))

	// Zero-delta mouse nudge — brings the Hyper-V Video framebuffer out of
	// its idle power state when the ES_DISPLAY_REQUIRED flag alone isn't
	// enough to force rendering.
	C.nudge_input_for_display_wake()
}

// inputSwitchActiveDesktop re-attaches the current OS thread to whichever
// desktop is currently receiving user input (Default, Winlogon/Secure Desktop,
// or the screensaver). Must be called on a LockOSThread goroutine. Used by
// the capture layer before re-initialising DXGI after DXGI_ERROR_ACCESS_LOST.
func inputSwitchActiveDesktop() {
	C.force_switch_active_desktop()
}

// inputAttachToDefaultDesktop pins the current process to WinSta0 and the
// current OS thread to the Default desktop. Returns "WinSta\Desktop" for
// the logger. Used by the helper's capture thread at startup: DXGI Desktop
// Duplication returns E_ACCESSDENIED from the Winlogon WinSta (which is
// where the helper lands when spawned with winlogon.exe's token) but works
// from WinSta0\Default — even on a session with no user logged in.
func inputAttachToDefaultDesktop() string {
	var buf [320]C.char
	C.attach_to_default_desktop(&buf[0], C.int(len(buf)))
	return C.GoString(&buf[0])
}
