//go:build windows

package engine

/*
#cgo LDFLAGS: -luser32
#include <windows.h>
#include <wtsapi32.h>
#include "wts_dynamic.h" // WTSAPI32.DLL from System32 only, never a static import
#include <string.h>

// ── Vault type_text: the remote-keyboard-layout typing path ─────────────────
// The operator's own AZERTY/QWERTY never matters: each character is typed with
// the keys the REMOTE layout uses for it (Shift, AltGr, a dead key + Space),
// and KEYEVENTF_UNICODE only for what the layout cannot produce. This is the
// path already proven on the LogonUI secure desktop; VK_PACKET/Unicode there is
// the fallback. Nothing here logs a VK, a scan code or a character.

// A planned keystroke. kind: 0 = a key (with modifiers), 1 = a dead key then
// Space (types a bare accent like ^ ¨ ` ~), 2 = a Unicode code unit.
typedef struct {
    int kind;
    unsigned short vk;
    unsigned short scan;
    int mods;            // bit0 Shift, bit1 LCtrl, bit2 RMenu (AltGr = LCtrl+RMenu)
    unsigned short unit; // Unicode code unit (kind 2)
} TYPE_EVENT;

// obli_foreground_hkl: the layout of the foreground thread (LogonUI's on the
// secure desktop), so VkKeyScanExW / ToUnicodeEx resolve against the layout the
// remote machine actually shows — not the calling thread's.
static HKL obli_foreground_hkl(void) {
    HWND fg = GetForegroundWindow();
    if (fg) {
        DWORD tid = GetWindowThreadProcessId(fg, NULL);
        HKL h = GetKeyboardLayout(tid);
        if (h) return h;
    }
    return GetKeyboardLayout(0);
}

// obli_foreground_present: whether GetForegroundWindow() sees a window from
// THIS thread's attached desktop. Layout planned before the desktop
// attach: if the thread is not (yet) attached to the desktop that
// actually has a foreground window (e.g. Winlogon for a secure-only entry),
// this is 0 and obli_foreground_hkl silently falls back to the calling
// thread's own layout — wrong keyboard layout, garbled secret. Called by Go
// AFTER inputSwitchActiveDesktop() to refuse rather than guess.
static int obli_foreground_present(void) {
    return GetForegroundWindow() != NULL;
}

// obli_input_desktop_name copies the name of the desktop that currently
// receives input ("Default", "Winlogon", "Screen-saver"…). Empty when it
// cannot be opened (that also counts as "not the secure desktop").
static void obli_input_desktop_name(char *out, int outLen) {
    if (outLen > 0) out[0] = 0;
    HDESK d = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS);
    if (!d) return;
    DWORD needed = 0;
    GetUserObjectInformationA(d, UOI_NAME, out, outLen, &needed);
    if (outLen > 0) out[outLen - 1] = 0;
    CloseDesktop(d);
}

static int obli_input_desktop_is_secure(void) {
    char name[128] = {0};
    obli_input_desktop_name(name, sizeof(name));
    return _stricmp(name, "Winlogon") == 0;
}

// ── Before typing (typetext_prep.go): is the session ready for a password? ──

// obli_thread_on_input_desktop: this thread is attached to the desktop that
// receives input (else the caller re-attaches before probing the focus).
static int obli_thread_on_input_desktop(void) {
    char in[128] = {0}, th[128] = {0};
    DWORD needed = 0;
    obli_input_desktop_name(in, sizeof(in));
    HDESK t = GetThreadDesktop(GetCurrentThreadId());
    if (t) GetUserObjectInformationA(t, UOI_NAME, th, sizeof(th), &needed);
    th[sizeof(th) - 1] = 0;
    return in[0] != 0 && _stricmp(in, th) == 0;
}

// obli_focus_caret: whether the foreground thread (LogonUI on the secure
// desktop) has a text field with the caret — the password field shows.
// 1 yes, 0 no (the "Press Ctrl+Alt+Del" screen), -1 unknown (no foreground
// window on this thread's desktop, the query failed). Reads no content.
static int obli_focus_caret(void) {
    HWND fg = GetForegroundWindow();
    if (!fg) return -1;
    DWORD tid = GetWindowThreadProcessId(fg, NULL);
    if (!tid) return -1;
    GUITHREADINFO gi; memset(&gi, 0, sizeof(gi)); gi.cbSize = sizeof(gi);
    if (!GetGUIThreadInfo(tid, &gi)) return -1;
    return (gi.hwndCaret != NULL || (gi.flags & GUI_CARETBLINKING)) ? 1 : 0;
}

// obli_session_lock_flags: WTSINFOEX_LEVEL1_W.SessionFlags of this process's
// session (WTSSessionInfoEx = 25). Read at its fixed offset (Level DWORD,
// then the 8-aligned union: SessionId, SessionState, SessionFlags at 16) so
// it does not depend on the SDK headers declaring WTSINFOEXW. 1 = read.
static int obli_session_lock_flags(LONG *flags) {
    LPWSTR buf = NULL; DWORD n = 0;
    if (!WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, WTS_CURRENT_SESSION, (WTS_INFO_CLASS)25, &buf, &n) || !buf) return 0;
    int ok = 0;
    if (n >= 20) {
        DWORD level = 0;
        memcpy(&level, buf, sizeof(level));
        if (level == 1) { memcpy(flags, ((const char *)buf) + 16, sizeof(*flags)); ok = 1; }
    }
    WTSFreeMemory(buf);
    return ok;
}

// obli_session_has_user: whether a user is signed in to this process's
// session (WTSUserName not empty). 1 yes, 0 no (the sign-in screen nobody has
// passed), -1 unknown. Reads the name's length only.
static int obli_session_has_user(void) {
    LPWSTR buf = NULL; DWORD n = 0;
    if (!WTSQuerySessionInformationW(WTS_CURRENT_SERVER_HANDLE, WTS_CURRENT_SESSION, WTSUserName, &buf, &n) || !buf) return -1;
    int has = buf[0] != 0 ? 1 : 0;
    WTSFreeMemory(buf);
    return has;
}

// obli_os_win7_or_older: Windows 7 / Server 2008 R2 report the lock flags
// swapped (fixed in Windows 8). RtlGetVersion: not lied to by the manifest.
static int obli_os_win7_or_older(void) {
    typedef LONG (WINAPI *RtlGetVersionFn)(OSVERSIONINFOW *);
    HMODULE nt = GetModuleHandleW(L"ntdll.dll");
    if (!nt) return 0;
    RtlGetVersionFn f = (RtlGetVersionFn)GetProcAddress(nt, "RtlGetVersion");
    if (!f) return 0;
    OSVERSIONINFOW v; memset(&v, 0, sizeof(v)); v.dwOSVersionInfoSize = sizeof(v);
    if (f(&v) != 0) return 0;
    return v.dwMajorVersion < 6 || (v.dwMajorVersion == 6 && v.dwMinorVersion <= 1);
}

// obli_plan_rune resolves ONE UTF-16 code unit to a TYPE_EVENT using hkl.
//   returns 0 on success (ev filled), never fails (falls back to Unicode).
static int obli_plan_rune(unsigned short ch, HKL hkl, TYPE_EVENT *ev) {
    ev->kind = 2; ev->vk = 0; ev->scan = 0; ev->mods = 0; ev->unit = ch;

    SHORT sc = VkKeyScanExW((WCHAR)ch, hkl);
    if (sc == -1) return 0; // no key for it on this layout → Unicode

    int vk = sc & 0xFF;
    int shiftState = (sc >> 8) & 0xFF; // 1 Shift, 2 Ctrl, 4 Alt
    int wantShift = (shiftState & 1) != 0;
    int wantCtrl  = (shiftState & 2) != 0;
    int wantAlt   = (shiftState & 4) != 0;
    // Ctrl or Alt alone is a shortcut, not a character → Unicode. Only AltGr
    // (Ctrl+Alt together) is a real character modifier.
    if (wantCtrl != wantAlt) return 0;

    UINT scan = MapVirtualKeyExW((UINT)vk, MAPVK_VK_TO_VSC, hkl);

    // Build the modifier key-state ToUnicodeEx should see.
    BYTE ks[256]; memset(ks, 0, sizeof(ks));
    if (wantShift) { ks[VK_SHIFT] = 0x80; ks[VK_LSHIFT] = 0x80; }
    if (wantCtrl)  { ks[VK_CONTROL] = 0x80; ks[VK_LCONTROL] = 0x80; }
    if (wantAlt)   { ks[VK_MENU] = 0x80; ks[VK_RMENU] = 0x80; if (wantCtrl) ks[VK_LCONTROL] = 0x80; }

    WCHAR buf[8]; memset(buf, 0, sizeof(buf));
    // wFlags bit2 (0x4) = do not change the kernel keyboard state (Win10 1607+).
    int r = ToUnicodeEx((UINT)vk, scan, ks, buf, 8, 0x4, hkl);
    int mods = (wantShift ? 1 : 0) | (wantCtrl ? 2 : 0) | (wantAlt ? 4 : 0);
    if (r == 1 && buf[0] == (WCHAR)ch) {
        ev->kind = 0; ev->vk = (unsigned short)vk; ev->scan = (unsigned short)scan; ev->mods = mods;
        return 0;
    }
    if (r < 0) {
        // Dead key. Does dead-then-Space produce ch? Probe WITH state change,
        // then flush the pending dead state so we leave nothing behind.
        WCHAR d1[8]; memset(d1, 0, sizeof(d1));
        ToUnicodeEx((UINT)vk, scan, ks, d1, 8, 0, hkl); // arms the dead key
        BYTE ks2[256]; memset(ks2, 0, sizeof(ks2));
        UINT spScan = MapVirtualKeyExW(VK_SPACE, MAPVK_VK_TO_VSC, hkl);
        WCHAR sp[8]; memset(sp, 0, sizeof(sp));
        int rs = ToUnicodeEx(VK_SPACE, spScan, ks2, sp, 8, 0, hkl);
        if (rs == 1 && sp[0] == (WCHAR)ch) {
            ev->kind = 1; ev->vk = (unsigned short)vk; ev->scan = (unsigned short)scan; ev->mods = mods;
            return 0;
        }
        // Not what we wanted: make sure no dead state lingers (flush twice).
        ToUnicodeEx(VK_SPACE, spScan, ks2, sp, 8, 0, hkl);
        ToUnicodeEx(VK_SPACE, spScan, ks2, sp, 8, 0, hkl);
    }
    return 0; // Unicode fallback (ev already set to kind 2)
}

// ── The executor ────────────────────────────────────────────────────────────
static void obli_send_key(WORD vk, WORD scan, DWORD extraFlags) {
    INPUT in; memset(&in, 0, sizeof(in));
    in.type = INPUT_KEYBOARD;
    in.ki.wVk = vk;
    in.ki.wScan = scan;
    in.ki.dwFlags = extraFlags;
    SendInput(1, &in, sizeof(INPUT));
    SecureZeroMemory(&in, sizeof(in));
}
static int obli_send_key_ret(WORD vk, WORD scan, DWORD extraFlags) {
    INPUT in; memset(&in, 0, sizeof(in));
    in.type = INPUT_KEYBOARD;
    in.ki.wVk = vk;
    in.ki.wScan = scan;
    in.ki.dwFlags = extraFlags;
    UINT sent = SendInput(1, &in, sizeof(INPUT));
    SecureZeroMemory(&in, sizeof(in));
    return sent == 1 ? 1 : 0;
}

static void obli_mods_down(int mods) {
    if (mods & 1) obli_send_key(VK_LSHIFT, (WORD)MapVirtualKeyW(VK_LSHIFT, MAPVK_VK_TO_VSC), 0);
    if (mods & 2) obli_send_key(VK_LCONTROL, (WORD)MapVirtualKeyW(VK_LCONTROL, MAPVK_VK_TO_VSC), 0);
    if (mods & 4) obli_send_key(VK_RMENU, (WORD)MapVirtualKeyW(VK_RMENU, MAPVK_VK_TO_VSC), KEYEVENTF_EXTENDEDKEY);
}
static void obli_mods_up(int mods) {
    if (mods & 4) obli_send_key(VK_RMENU, (WORD)MapVirtualKeyW(VK_RMENU, MAPVK_VK_TO_VSC), KEYEVENTF_EXTENDEDKEY | KEYEVENTF_KEYUP);
    if (mods & 2) obli_send_key(VK_LCONTROL, (WORD)MapVirtualKeyW(VK_LCONTROL, MAPVK_VK_TO_VSC), KEYEVENTF_KEYUP);
    if (mods & 1) obli_send_key(VK_LSHIFT, (WORD)MapVirtualKeyW(VK_LSHIFT, MAPVK_VK_TO_VSC), KEYEVENTF_KEYUP);
}

// type_events types the plan, re-checking the desktop and foreground window
// before EVERY event. Returns 0 ok, -1 not_secure_desktop, -2 interrupted,
// -3 failed (SendInput refused). secureOnly requires the input desktop to be
// "Winlogon" throughout.
static int type_events(const TYPE_EVENT *ev, int n, int secureOnly, int delayMs) {
    // The caller (typeText) attaches this thread to the input desktop first.
    // Release any modifier left down (a lost key-up) and forget our tracking.
    obli_mods_up(1 | 2 | 4);

    char deskAtStart[128] = {0};
    obli_input_desktop_name(deskAtStart, sizeof(deskAtStart));
    if (secureOnly && _stricmp(deskAtStart, "Winlogon") != 0) return -1;
    HWND fgAtStart = GetForegroundWindow();

    // Turn Caps Lock off for the run, restore it after (KeePass auto-type).
    int capsWasOn = (GetKeyState(VK_CAPITAL) & 1) != 0;
    if (capsWasOn) {
        obli_send_key(VK_CAPITAL, (WORD)MapVirtualKeyW(VK_CAPITAL, MAPVK_VK_TO_VSC), 0);
        obli_send_key(VK_CAPITAL, (WORD)MapVirtualKeyW(VK_CAPITAL, MAPVK_VK_TO_VSC), KEYEVENTF_KEYUP);
    }

    int rc = 0;
    for (int i = 0; i < n; i++) {
        char deskNow[128] = {0};
        obli_input_desktop_name(deskNow, sizeof(deskNow));
        if (_stricmp(deskNow, deskAtStart) != 0) { rc = (secureOnly ? -1 : -2); break; }
        if (secureOnly && _stricmp(deskNow, "Winlogon") != 0) { rc = -1; break; }
        if (GetForegroundWindow() != fgAtStart) { rc = -2; break; }

        const TYPE_EVENT *e = &ev[i];
        int isTab = (e->kind == 0 && e->vk == VK_TAB);
        if (e->kind == 2) {
            if (!obli_send_key_ret(0, e->unit, KEYEVENTF_UNICODE)) { rc = -3; break; }
            if (!obli_send_key_ret(0, e->unit, KEYEVENTF_UNICODE | KEYEVENTF_KEYUP)) { rc = -3; break; }
        } else {
            obli_mods_down(e->mods);
            int ok = obli_send_key_ret(e->vk, (WORD)e->scan, 0)
                  && obli_send_key_ret(e->vk, (WORD)e->scan, KEYEVENTF_KEYUP);
            if (e->kind == 1 && ok) {
                // dead key armed: press Space to yield the bare accent.
                WORD sp = (WORD)MapVirtualKeyW(VK_SPACE, MAPVK_VK_TO_VSC);
                ok = obli_send_key_ret(VK_SPACE, sp, 0) && obli_send_key_ret(VK_SPACE, sp, KEYEVENTF_KEYUP);
            }
            obli_mods_up(e->mods);
            if (!ok) { rc = -3; break; }
        }
        Sleep(isTab ? (DWORD)(delayMs > 30 ? delayMs : 30) : (DWORD)delayMs);
    }

    obli_mods_up(1 | 2 | 4);
    if (capsWasOn) {
        obli_send_key(VK_CAPITAL, (WORD)MapVirtualKeyW(VK_CAPITAL, MAPVK_VK_TO_VSC), 0);
        obli_send_key(VK_CAPITAL, (WORD)MapVirtualKeyW(VK_CAPITAL, MAPVK_VK_TO_VSC), KEYEVENTF_KEYUP);
    }
    return rc;
}
*/
import "C"

import (
	"time"
	"unsafe"
)

// typeTextSupported: the vault typing path is Windows-only and exists on the
// capture helper (which owns the input desktop attach). Advertised in
// agentFeatures so viewers only show the vault key button on real Windows.
func typeTextSupported() bool { return true }

// typeEvent is the Go mirror of a planned keystroke (tests inspect these).
type typeEvent struct {
	kind int // 0 key, 1 deadKey+Space, 2 unicode
	vk   uint16
	scan uint16
	mods int
	unit uint16
}

const (
	typeEvKey      = 0
	typeEvDeadSpc  = 1
	typeEvUnicode  = 2
	typeKeyDelayMs = 12
)

// planTyping turns a validated type_text message into the keystroke plan for
// the current foreground layout. Tab and Enter parts become their own key
// events; each text rune is resolved by the remote layout (key / dead+Space /
// Unicode). Non-BMP runes emit two Unicode events (a UTF-16 surrogate pair).
func planTyping(msg *typeTextMsg) []typeEvent {
	hkl := C.obli_foreground_hkl()
	var out []typeEvent
	for _, p := range msg.Parts {
		if p.Key != "" {
			vk := uint16(0x09) // Tab
			mods := 0
			switch p.Key {
			case "Enter":
				vk = 0x0D
			case typeKeySelectAll:
				vk, mods = 0x41, 2 // Ctrl+A (VK_A on every layout)
			}
			scan := uint16(C.MapVirtualKeyExW(C.UINT(vk), C.MAPVK_VK_TO_VSC, hkl))
			out = append(out, typeEvent{kind: typeEvKey, vk: vk, scan: scan, mods: mods})
			continue
		}
		for _, r := range string(p.Text) {
			out = append(out, planRune(r, hkl)...)
		}
	}
	return out
}

// planRune resolves one rune to one or two events.
func planRune(r rune, hkl C.HKL) []typeEvent {
	if r > 0xFFFF {
		r -= 0x10000
		hi := uint16(0xD800 + (r >> 10))
		lo := uint16(0xDC00 + (r & 0x3FF))
		return []typeEvent{{kind: typeEvUnicode, unit: hi}, {kind: typeEvUnicode, unit: lo}}
	}
	var ev C.TYPE_EVENT
	C.obli_plan_rune(C.ushort(uint16(r)), hkl, &ev)
	return []typeEvent{{
		kind: int(ev.kind), vk: uint16(ev.vk), scan: uint16(ev.scan),
		mods: int(ev.mods), unit: uint16(ev.unit),
	}}
}

// inputDesktopName returns the current input desktop's name ("" when it cannot
// be opened). secure = it is the Winlogon (login / lock / UAC) desktop.
func inputDesktopName() string {
	var buf [128]C.char
	C.obli_input_desktop_name(&buf[0], C.int(len(buf)))
	return C.GoString(&buf[0])
}

func inputDesktopSecure() bool { return C.obli_input_desktop_is_secure() != 0 }

// probeTypeScreen reads what decideTypePrep / decideTypeAction need: the
// input desktop, the session's lock state, and on the Winlogon desktop the
// focused field and the text boxes shown (UI Automation, typetext_uia_windows.go),
// with the caret as the fallback. It (re)attaches this thread to the input
// desktop first when it is not on it, so the foreground window it reads is
// the one shown. Reads no content, logs nothing.
func probeTypeScreen() typeScreen { return probeTypeScreenFocus(fieldUnknown, nil) }

// probeTypeScreenFocus is probeTypeScreen that first moves the focus to a
// field (fieldPassword / fieldUser) when asked; focusSet reports it.
func probeTypeScreenFocus(target fieldKind, focusSet *bool) typeScreen {
	if C.obli_thread_on_input_desktop() == 0 {
		inputSwitchActiveDesktop()
	}
	s := typeScreen{Desktop: inputDesktopName(), Locked: -1, CredField: int(C.obli_focus_caret())}
	wts := -1
	var flags C.LONG
	if C.obli_session_lock_flags(&flags) != 0 {
		wts = wtsLockState(int32(flags), C.obli_os_win7_or_older() != 0)
	}
	s.Locked = sessionLockState(wts, int(C.obli_session_has_user()))
	if s.secure() {
		if u, set, ok := uiaScreen(target); ok {
			s.Focus, s.PwShown, s.UserShown, s.UserEmpty = u.Focus, u.PwShown, u.UserShown, u.UserEmpty
			if focusSet != nil {
				*focusSet = set
			}
		}
	}
	return s
}

// winTypePrepDeps: the real probes. Ctrl+Alt+Del goes through InputSASHook,
// the very route of the viewer's Ctrl+Alt+Del: the service's SendSAS for the
// console session (Session 0 only reaches the console's Winlogon), SendSAS
// here for an RDP session (remote_session_windows.go).
func winTypePrepDeps() typePrepDeps {
	return typePrepDeps{
		probe: probeTypeScreen,
		sendSAS: func() {
			if InputSASHook != nil {
				InputSASHook()
			}
		},
		sleep: time.Sleep,
		now:   time.Now,
	}
}

var (
	ttNetapi32              = newSystemLazyDLL("netapi32.dll")
	ttNetGetJoinInformation = ttNetapi32.NewProc("NetGetJoinInformation")
	ttNetApiBufferFree      = ttNetapi32.NewProc("NetApiBufferFree")
)

// domainJoinState is init.domain: "joined" (an Active Directory domain),
// "workgroup", or "" when it cannot be read. The vault types a username
// only where the sign-in screen can ask for one (client-app vault_ui.go).
func domainJoinState() string {
	if ttNetGetJoinInformation.Find() != nil {
		return ""
	}
	var name uintptr // (a buffer of netapi32's: only freed, never read)
	var status uint32
	r, _, _ := ttNetGetJoinInformation.Call(0, uintptr(unsafe.Pointer(&name)), uintptr(unsafe.Pointer(&status)))
	if name != 0 {
		ttNetApiBufferFree.Call(name)
	}
	if r != 0 {
		return ""
	}
	return domainInitValue(status)
}

// typeText executes a validated plan on the input desktop, honouring the
// secure-only rule. Returns "" on success or a viewer code (not_secure_desktop
// / interrupted / failed). Must run on the input dispatch thread (LockOSThread,
// attached to the input desktop) — the same thread that types the operator's
// own keys.
func typeText(msg *typeTextMsg) string {
	// Layout planned before the desktop attach: the plan resolves
	// the REMOTE keyboard layout via GetForegroundWindow (obli_foreground_hkl),
	// which only sees a window on THIS thread's currently attached desktop.
	// Planning before attaching risked reading the helper's own layout instead
	// of LogonUI's/consent.exe's — e.g. an AZERTY password typed with an
	// en-US plan (digits shift, punctuation moves). Attach first, exactly like
	// the ordinary key path does, before any layout is resolved.
	inputSwitchActiveDesktop()
	// For a secure-only entry, refuse rather than guess when there is still no
	// foreground window after the attach (the layout would silently fall back
	// to the helper's own) — a wrong-layout guess on a login/UAC prompt is a
	// failed logon that counts toward lockout, often on a privileged account.
	if msg.SecureOnly && C.obli_foreground_present() == 0 {
		return "not_secure_desktop"
	}
	plan := planTyping(msg)
	if len(plan) == 0 {
		return "invalid"
	}
	// Partial modifier release: a lost key-up on the operator's
	// side (Alt+Tab / Ctrl+Tab locally, a focus change) can leave a modifier
	// down in the session. The vault click is a mouse action on the toolbar,
	// so no key event runs the ordinary reconciliation first — and type_events'
	// own bookend (obli_mods_up) only covers 3 of the 8 modifier VKs
	// (LShift/LCtrl/RMenu): RShift, RCtrl, LAlt, LWin and RWin were never
	// released. Every character of the secret would then ride whichever
	// modifier is stuck (wrong password, or shortcuts firing on a normal
	// desktop). Run the full, state-checked release before typing, and reset
	// this stream's "recently injected" tracking so an unrelated ordinary key
	// right after the vault isn't wrongly protected from reconciliation.
	inputReleaseAllModifiers()
	inputResetModifierTracking()
	cev := make([]C.TYPE_EVENT, len(plan))
	for i, e := range plan {
		cev[i] = C.TYPE_EVENT{kind: C.int(e.kind), vk: C.ushort(e.vk), scan: C.ushort(e.scan), mods: C.int(e.mods), unit: C.ushort(e.unit)}
	}
	secure := C.int(0)
	if msg.SecureOnly {
		secure = 1
	}
	rc := int(C.type_events(&cev[0], C.int(len(cev)), secure, C.int(typeKeyDelayMs)))
	// Release again at the end — belt-and-suspenders in case an interrupted
	// run (desktop/foreground change mid-way) left a per-character modifier
	// held — and reset tracking again so it never leaks into the operator's
	// own next keystroke.
	inputReleaseAllModifiers()
	inputResetModifierTracking()
	// Wipe the plan (it carries no secret bytes, but the code units do describe
	// the typed characters): zero it defensively.
	for i := range cev {
		cev[i] = C.TYPE_EVENT{}
	}
	for i := range plan {
		plan[i] = typeEvent{}
	}
	switch rc {
	case 0:
		return ""
	case -1:
		return "not_secure_desktop"
	case -2:
		return "interrupted"
	default:
		return "failed"
	}
}

// handleTypeText parses, rate-limits, types and answers a type_text frame on
// the helper's input dispatch thread. reply sends the type_text_result control
// frame back to the service (PipeTypeControl). It wipes every secret byte.
func handleTypeText(payload []byte, reply func([]byte)) {
	id := PeekTypeTextID(payload)
	msg, code := parseTypeText(payload)
	WipeBytes(payload)
	if code != "" {
		reply(TypeTextResult(id, false, code))
		return
	}
	// Bound to the view generation: a secret must only be
	// typed into the view the operator was looking at when they clicked the
	// vault entry — never replayed into a session that has since switched
	// underneath it (fast user switching / an RDP reconnect to the console in
	// session-follow mode). Ordinary key/mouse events already enforce this in
	// dispatchInputJSON (stream.go); type_text bypasses that path entirely
	// (it never reaches dispatchInputJSON), so the same check runs here.
	if _, viewGen, haveView := getCurrentView(); !haveView || msg.Gen != viewGen {
		msg.wipe()
		reply(TypeTextResult(id, false, "session_changed"))
		return
	}
	if c := globalTypeLimiter.begin(); c != "" {
		msg.wipe()
		reply(TypeTextResult(msg.ID, false, c))
		return
	}
	defer globalTypeLimiter.done()
	// A locked session whose password field is not showing
	// gets Ctrl+Alt+Del first (when the viewer asked: autoSas), then the
	// credential screen is awaited; it never shows → nothing typed, said so.
	prep, pc, screen := prepareTyping(msg.AutoSAS, winTypePrepDeps())
	if pc != "" {
		msg.wipe()
		reply(typeTextResultPrep(id, false, pc, prep))
		return
	}
	// (No second view-generation check after Ctrl+Alt+Del: the switch to the
	// Winlogon desktop may re-publish the capture's view — a new gen — while
	// it is the very same Windows session: this helper serves one session
	// only, a session switch is another helper. The gen guard above already
	// bound the request to the view the operator clicked on.)
	// Which field gets what on the sign-in screen (the
	// password box, or the "Other user" username box first) — decided from
	// UI Automation, the focus re-checked before each step
	// (typetext_prep.go runTypeSteps). Elsewhere: typed as asked.
	rc := runTypeSteps(screen, &msg, typeFieldDeps{
		probe: probeTypeScreen,
		focus: func(k fieldKind) bool {
			var set bool
			probeTypeScreenFocus(k, &set)
			return set
		},
		typ: func(parts []typePart) string {
			step := typeTextMsg{ID: msg.ID, SecureOnly: msg.SecureOnly, Gen: msg.Gen, Parts: parts}
			return typeText(&step) // (the parts are msg's own buffers: msg.wipe clears them)
		},
		sleep: time.Sleep,
	})
	msg.wipe()
	reply(typeTextResultPrep(id, rc == "", rc, prep))
}

// ── Input-desktop watcher ────────────────────────────────────────────────────
// A 1 s ticker reports every change of the input desktop's secure state so the
// viewer highlights the vault key button on the login / lock / UAC screen. Runs
// on the helper; the service forwards its input_desktop frames to the viewer,
// and a fresh leg's init already carries the current value (currentInputDesktop).

// watchInputDesktop sends an input_desktop control frame on every change of the
// secure state until stop closes. The first tick reports the current state so a
// viewer that connected before any change still learns it.
func watchInputDesktop(stop <-chan struct{}, send func([]byte)) {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	last := -1
	for {
		cur := 0
		if inputDesktopSecure() {
			cur = 1
		}
		if cur != last {
			last = cur
			send(inputDesktopMsg(cur == 1))
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// currentInputDesktop is "secure" / "default" for init.inputDesktop (omitted
// when the desktop cannot be read).
func currentInputDesktop() string {
	name := inputDesktopName()
	if name == "" {
		return ""
	}
	if inputDesktopSecure() {
		return "secure"
	}
	return "default"
}
