//go:build windows

package engine

/*
#cgo LDFLAGS: -lole32 -loleaut32 -luser32
#include <windows.h>
#include <ole2.h>
#include <oleauto.h>
#include <string.h>

// ── UI Automation of the sign-in screen (typetext_prep.go) ──────────────────
// Which element has the keyboard focus on the input desktop (a password box, a
// plain text box, something else) and which text boxes the foreground window
// shows. Narrator reads the same tree on the same screen. Reads no content
// except whether the plain text box (the username) is EMPTY — never its text,
// never a password box's value (UI Automation refuses it anyway).
//
// The interfaces are declared here (TDM-GCC's uiautomationclient.h has no
// IUIAutomation): only the vtable slots used are typed, in the Windows SDK's
// order (UIAutomationClient.h, 10.0.26100: IUIAutomation 6 ElementFromHandle,
// 8 GetFocusedElement, 21 CreateTrueCondition; IUIAutomationElement 3
// SetFocus, 6 FindAll, 10 GetCurrentPropertyValue; IUIAutomationElementArray
// 3 get_Length, 4 GetElement).

typedef struct OUiaEl OUiaEl;
typedef struct OUiaArr OUiaArr;
typedef struct OUia OUia;

typedef struct {
    HRESULT (STDMETHODCALLTYPE *QueryInterface)(OUiaEl *, REFIID, void **);
    ULONG (STDMETHODCALLTYPE *AddRef)(OUiaEl *);
    ULONG (STDMETHODCALLTYPE *Release)(OUiaEl *);
    HRESULT (STDMETHODCALLTYPE *SetFocus)(OUiaEl *);
    void *GetRuntimeId, *FindFirst;
    HRESULT (STDMETHODCALLTYPE *FindAll)(OUiaEl *, int scope, IUnknown *cond, OUiaArr **found);
    void *FindFirstBuildCache, *FindAllBuildCache, *BuildUpdatedCache;
    HRESULT (STDMETHODCALLTYPE *GetCurrentPropertyValue)(OUiaEl *, int propertyId, VARIANT *v);
} OUiaElVtbl;
struct OUiaEl { const OUiaElVtbl *lpVtbl; };

typedef struct {
    HRESULT (STDMETHODCALLTYPE *QueryInterface)(OUiaArr *, REFIID, void **);
    ULONG (STDMETHODCALLTYPE *AddRef)(OUiaArr *);
    ULONG (STDMETHODCALLTYPE *Release)(OUiaArr *);
    HRESULT (STDMETHODCALLTYPE *get_Length)(OUiaArr *, int *n);
    HRESULT (STDMETHODCALLTYPE *GetElement)(OUiaArr *, int i, OUiaEl **el);
} OUiaArrVtbl;
struct OUiaArr { const OUiaArrVtbl *lpVtbl; };

typedef struct {
    HRESULT (STDMETHODCALLTYPE *QueryInterface)(OUia *, REFIID, void **);
    ULONG (STDMETHODCALLTYPE *AddRef)(OUia *);
    ULONG (STDMETHODCALLTYPE *Release)(OUia *);
    void *CompareElements, *CompareRuntimeIds, *GetRootElement;           // 3..5
    HRESULT (STDMETHODCALLTYPE *ElementFromHandle)(OUia *, HWND, OUiaEl **); // 6
    void *ElementFromPoint;                                                // 7
    HRESULT (STDMETHODCALLTYPE *GetFocusedElement)(OUia *, OUiaEl **);     // 8
    void *s9, *s10, *s11, *s12, *s13, *s14, *s15, *s16, *s17, *s18, *s19, *s20;
    HRESULT (STDMETHODCALLTYPE *CreateTrueCondition)(OUia *, IUnknown **); // 21
} OUiaVtbl;
struct OUia { const OUiaVtbl *lpVtbl; };

static const CLSID obli_CLSID_CUIAutomation = {0xff48dba4, 0x60ef, 0x4201, {0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}};
static const IID obli_IID_IUIAutomation = {0x30cbe57d, 0xd9d0, 0x452a, {0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}};

#define OBLI_UIA_ControlType   30003
#define OBLI_UIA_IsEnabled     30010
#define OBLI_UIA_IsPassword    30019
#define OBLI_UIA_IsOffscreen   30022
#define OBLI_UIA_ValueValue    30045
#define OBLI_UIA_EditControl   50004
#define OBLI_UIA_TreeScope_Descendants 4
#define OBLI_UIA_MaxElements   3000

// What the probe saw. focus: 0 none, 1 password box, 2 plain text box.
typedef struct {
    int ok;          // UI Automation answered (the rest is meaningful)
    int focus;
    int pwShown;
    int userShown;
    int userEmpty;   // 1 empty, 0 holds text, -1 unknown
    int focusSet;    // the requested SetFocus succeeded
    int deskOk;      // the thread was attached to the input desktop
} OBLI_UIA_SCREEN;

static int obli_uia_int(OUiaEl *e, int prop, int dflt) {
    VARIANT v; VariantInit(&v);
    int r = dflt;
    if (SUCCEEDED(e->lpVtbl->GetCurrentPropertyValue(e, prop, &v))) {
        if (v.vt == VT_I4) r = v.lVal;
        else if (v.vt == VT_BOOL) r = (v.boolVal != VARIANT_FALSE) ? 1 : 0;
    }
    VariantClear(&v);
    return r;
}

// obli_uia_kind: 1 password box, 2 plain text box, 0 anything else.
static int obli_uia_kind(OUiaEl *e) {
    if (obli_uia_int(e, OBLI_UIA_ControlType, 0) != OBLI_UIA_EditControl) return 0;
    return obli_uia_int(e, OBLI_UIA_IsPassword, 0) ? 1 : 2;
}

// obli_uia_empty: 1 the text box is empty, 0 it holds text, -1 unknown. The
// value is wiped before it is freed and never leaves this function.
static int obli_uia_empty(OUiaEl *e) {
    VARIANT v; VariantInit(&v);
    int r = -1;
    if (SUCCEEDED(e->lpVtbl->GetCurrentPropertyValue(e, OBLI_UIA_ValueValue, &v)) && v.vt == VT_BSTR) {
        UINT n = v.bstrVal ? SysStringLen(v.bstrVal) : 0;
        r = n == 0 ? 1 : 0;
        if (v.bstrVal && n) SecureZeroMemory(v.bstrVal, n * sizeof(WCHAR));
    }
    VariantClear(&v);
    return r;
}

// obli_uia_probe runs on a fresh OS thread of its own (Go: a locked goroutine
// that exits with it): attach to the input desktop, read, optionally move the
// focus (focusTarget 1 password box, 2 plain text box), detach. Never types.
static void obli_uia_probe(int focusTarget, OBLI_UIA_SCREEN *out) {
    memset(out, 0, sizeof(*out));
    out->userEmpty = -1;
    HDESK orig = GetThreadDesktop(GetCurrentThreadId());
    HDESK d = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS | DESKTOP_WRITEOBJECTS | DESKTOP_ENUMERATE | DESKTOP_SWITCHDESKTOP);
    if (!d) return;
    if (!SetThreadDesktop(d)) { CloseDesktop(d); return; }
    out->deskOk = 1;

    if (SUCCEEDED(CoInitializeEx(NULL, COINIT_MULTITHREADED))) {
        OUia *uia = NULL;
        if (SUCCEEDED(CoCreateInstance(&obli_CLSID_CUIAutomation, NULL, CLSCTX_INPROC_SERVER, &obli_IID_IUIAutomation, (void **)&uia)) && uia) {
            OUiaEl *f = NULL;
            HRESULT hr = uia->lpVtbl->GetFocusedElement(uia, &f);
            if (SUCCEEDED(hr)) {
                out->ok = 1;
                if (f) out->focus = obli_uia_kind(f);
            }
            // The text boxes the foreground window shows (LogonUI / consent).
            OUiaEl *userEl = NULL, *pwEl = NULL;
            HWND fg = GetForegroundWindow();
            OUiaEl *root = NULL;
            IUnknown *all = NULL;
            if (out->ok && fg && SUCCEEDED(uia->lpVtbl->ElementFromHandle(uia, fg, &root)) && root &&
                SUCCEEDED(uia->lpVtbl->CreateTrueCondition(uia, &all)) && all) {
                OUiaArr *arr = NULL;
                if (SUCCEEDED(root->lpVtbl->FindAll(root, OBLI_UIA_TreeScope_Descendants, all, &arr)) && arr) {
                    int n = 0;
                    arr->lpVtbl->get_Length(arr, &n);
                    if (n > OBLI_UIA_MaxElements) n = OBLI_UIA_MaxElements;
                    for (int i = 0; i < n; i++) {
                        OUiaEl *e = NULL;
                        if (FAILED(arr->lpVtbl->GetElement(arr, i, &e)) || !e) continue;
                        int k = obli_uia_kind(e);
                        int keep = 0;
                        if (k && obli_uia_int(e, OBLI_UIA_IsEnabled, 1) && !obli_uia_int(e, OBLI_UIA_IsOffscreen, 0)) {
                            if (k == 1 && !pwEl) { out->pwShown = 1; pwEl = e; keep = 1; }
                            else if (k == 2 && !userEl) { out->userShown = 1; out->userEmpty = obli_uia_empty(e); userEl = e; keep = 1; }
                        }
                        if (!keep) e->lpVtbl->Release(e);
                    }
                    arr->lpVtbl->Release(arr);
                }
            }
            if (focusTarget == 1 && pwEl) out->focusSet = SUCCEEDED(pwEl->lpVtbl->SetFocus(pwEl));
            if (focusTarget == 2 && userEl) out->focusSet = SUCCEEDED(userEl->lpVtbl->SetFocus(userEl));
            if (all) all->lpVtbl->Release(all);
            if (root) root->lpVtbl->Release(root);
            if (pwEl) pwEl->lpVtbl->Release(pwEl);
            if (userEl) userEl->lpVtbl->Release(userEl);
            if (f) f->lpVtbl->Release(f);
            uia->lpVtbl->Release(uia);
        }
        CoUninitialize();
    }
    // Back to the thread's own desktop so the handle can be closed (else it
    // stays open with the thread, which exits right after: Go never reuses it).
    if (orig && SetThreadDesktop(orig)) CloseDesktop(d);
}
*/
import "C"

import (
	"runtime"
	"time"
)

// uiaProbeTimeout bounds one UI Automation probe: a hung LogonUI must not
// hold the vault request (UIA's own connection timeout is longer). On a
// timeout the answer is "unknown" and the thread finishes on its own.
const uiaProbeTimeout = 2500 * time.Millisecond

// uiaScreen probes the input desktop's focus and text boxes with UI
// Automation; focusTarget fieldPassword / fieldUser also moves the focus
// there first-found. ok false: no answer (the caller falls back to the caret).
func uiaScreen(focusTarget fieldKind) (s typeScreen, focusSet, ok bool) {
	target := C.int(0)
	switch focusTarget {
	case fieldPassword:
		target = 1
	case fieldUser:
		target = 2
	}
	done := make(chan C.OBLI_UIA_SCREEN, 1)
	go func() {
		// A fresh OS thread, never handed back to the Go scheduler: it is
		// attached to the Winlogon desktop and holds COM state; it exits with
		// this goroutine.
		runtime.LockOSThread()
		var r C.OBLI_UIA_SCREEN
		C.obli_uia_probe(target, &r)
		done <- r
	}()
	var r C.OBLI_UIA_SCREEN
	select {
	case r = <-done:
	case <-time.After(uiaProbeTimeout):
		return typeScreen{}, false, false
	}
	if r.ok == 0 {
		return typeScreen{}, false, false
	}
	s.Focus = fieldNone
	switch r.focus {
	case 1:
		s.Focus = fieldPassword
	case 2:
		s.Focus = fieldUser
	}
	s.PwShown = r.pwShown != 0
	s.UserShown = r.userShown != 0
	s.UserEmpty = int(r.userEmpty)
	return s, r.focusSet != 0, true
}
