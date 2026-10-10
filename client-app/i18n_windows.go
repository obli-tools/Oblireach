//go:build windows

package main

// The Windows side of the translations (i18n.go): the display language of
// this Windows account (the automatic language's last resort before
// English).

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	procGetUserDefaultUILanguage = modKernel32.NewProc("GetUserDefaultUILanguage")
	procLCIDToLocaleName         = modKernel32.NewProc("LCIDToLocaleName")
)

var (
	winUILangOnce sync.Once
	winUILang     string
)

// windowsUILanguage: the Windows display language of this account ("fr-FR"),
// read once ("" when Windows does not say).
func windowsUILanguage() string {
	winUILangOnce.Do(func() {
		if procGetUserDefaultUILanguage.Find() != nil || procLCIDToLocaleName.Find() != nil {
			return
		}
		id, _, _ := procGetUserDefaultUILanguage.Call()
		if id == 0 {
			return
		}
		var buf [85]uint16 // LOCALE_NAME_MAX_LENGTH
		n, _, _ := procLCIDToLocaleName.Call(id&0xFFFF, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			return
		}
		winUILang = syscall.UTF16ToString(buf[:])
	})
	return winUILang
}

func init() { systemUILanguage = windowsUILanguage }

// (emitLang, the language switch told to a window's page: native_events.go.)
