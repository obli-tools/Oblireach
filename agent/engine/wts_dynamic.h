// WTSAPI32.DLL resolved at run time from System32 only.
//
// WTSAPI32.DLL is not a KnownDLL: a static import (-lwtsapi32) makes the
// Windows loader look for it in the executable's folder first, at process
// start, before any code of the program runs. For a portable executable run
// from Downloads, a planted WTSAPI32.DLL there would be loaded. The engine
// therefore never imports it statically: include this header after
// <wtsapi32.h> and the three WTS calls the engine makes go through function
// pointers resolved from System32 (LOAD_LIBRARY_SEARCH_SYSTEM32; Windows 7 /
// 2008 R2 without KB2533623: the full System32 path).
//
// Each translation unit (cgo file) gets its own static copy; the DLL is
// loaded once per process by Windows (reference counted).

#ifndef OBLI_WTS_DYNAMIC_H
#define OBLI_WTS_DYNAMIC_H

#include <windows.h>
#include <wtsapi32.h>
#include <wchar.h>

typedef BOOL (WINAPI *obli_PFN_WTSQuerySessionInformationW)(HANDLE, DWORD, WTS_INFO_CLASS, LPWSTR *, DWORD *);
typedef VOID (WINAPI *obli_PFN_WTSFreeMemory)(PVOID);
typedef BOOL (WINAPI *obli_PFN_WTSEnumerateSessionsW)(HANDLE, DWORD, DWORD, PWTS_SESSION_INFOW *, DWORD *);

static obli_PFN_WTSQuerySessionInformationW obli_p_WTSQuerySessionInformationW = NULL;
static obli_PFN_WTSFreeMemory               obli_p_WTSFreeMemory               = NULL;
static obli_PFN_WTSEnumerateSessionsW       obli_p_WTSEnumerateSessionsW       = NULL;
static volatile LONG                        obli_wts_state                     = 0; // 0 not tried, 1 ok, -1 unavailable

static __inline int obli_wts_load(void) {
    LONG st = InterlockedCompareExchange(&obli_wts_state, 0, 0);
    if (st != 0) return st > 0;
    HMODULE h = LoadLibraryExW(L"wtsapi32.dll", NULL, 0x00000800); // LOAD_LIBRARY_SEARCH_SYSTEM32
    if (!h) {
        wchar_t path[MAX_PATH + 32];
        UINT n = GetSystemDirectoryW(path, MAX_PATH);
        if (n > 0 && n < MAX_PATH) {
            path[n] = 0;
            wcscat(path, L"\\wtsapi32.dll");
            h = LoadLibraryExW(path, NULL, 0x00000008); // LOAD_WITH_ALTERED_SEARCH_PATH
        }
    }
    if (h) {
        obli_p_WTSQuerySessionInformationW = (obli_PFN_WTSQuerySessionInformationW)GetProcAddress(h, "WTSQuerySessionInformationW");
        obli_p_WTSFreeMemory               = (obli_PFN_WTSFreeMemory)GetProcAddress(h, "WTSFreeMemory");
        obli_p_WTSEnumerateSessionsW       = (obli_PFN_WTSEnumerateSessionsW)GetProcAddress(h, "WTSEnumerateSessionsW");
    }
    if (obli_p_WTSQuerySessionInformationW && obli_p_WTSFreeMemory && obli_p_WTSEnumerateSessionsW) {
        InterlockedExchange(&obli_wts_state, 1); // kept loaded for the process lifetime
        return 1;
    }
    InterlockedExchange(&obli_wts_state, -1);
    return 0;
}

static __inline BOOL obli_WTSQuerySessionInformationW(HANDLE srv, DWORD sid, WTS_INFO_CLASS cls, LPWSTR *buf, DWORD *n) {
    if (!obli_wts_load()) { SetLastError(ERROR_PROC_NOT_FOUND); return FALSE; }
    return obli_p_WTSQuerySessionInformationW(srv, sid, cls, buf, n);
}

static __inline VOID obli_WTSFreeMemory(PVOID p) {
    if (p && obli_wts_load()) obli_p_WTSFreeMemory(p);
}

static __inline BOOL obli_WTSEnumerateSessionsW(HANDLE srv, DWORD reserved, DWORD version, PWTS_SESSION_INFOW *info, DWORD *count) {
    if (!obli_wts_load()) { SetLastError(ERROR_PROC_NOT_FOUND); return FALSE; }
    return obli_p_WTSEnumerateSessionsW(srv, reserved, version, info, count);
}

#define WTSQuerySessionInformationW obli_WTSQuerySessionInformationW
#define WTSFreeMemory               obli_WTSFreeMemory
#define WTSEnumerateSessionsW       obli_WTSEnumerateSessionsW

#endif
