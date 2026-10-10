//go:build windows

package main

// Font family per language. Segoe UI covers the Latin, Cyrillic, Greek,
// Arabic and Hebrew scripts; for Japanese, Korean and Chinese the system UI
// font of that language is used so the glyphs follow the right regional
// forms (Han characters differ between Japanese and Chinese) instead of
// whatever GDI font linking picks. The first installed candidate wins;
// Segoe UI (with font linking) is the last resort.

import (
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var fontCandidates = map[string][]string{
	"ja":    {"Yu Gothic UI", "Meiryo UI", "MS UI Gothic"},
	"ko":    {"Malgun Gothic", "Gulim"},
	"zh-CN": {"Microsoft YaHei UI", "Microsoft YaHei", "SimSun"},
}

var (
	fontFaceMu    sync.Mutex
	fontFaceCache = map[string]string{}
)

// uiFontFace returns the font family for a language ("" = Segoe UI).
func uiFontFace(lang string) string {
	cands := fontCandidates[lang]
	if len(cands) == 0 {
		return ""
	}
	fontFaceMu.Lock()
	defer fontFaceMu.Unlock()
	if f, ok := fontFaceCache[lang]; ok {
		return f
	}
	face := ""
	for _, c := range cands {
		if fontInstalled(c) {
			face = c
			break
		}
	}
	fontFaceCache[lang] = face
	return face
}

// fontInstalled: GDI maps a missing family to another one; the face name
// it actually selected tells.
func fontInstalled(face string) bool {
	hdc, _, _ := pGetDC.Call(0)
	if hdc == 0 {
		return false
	}
	defer pReleaseDC.Call(0, hdc)
	f, _, _ := pCreateFontW.Call(uintptr(^uintptr(13)), 0, 0, 0, fwNormal, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16(face))))
	if f == 0 {
		return false
	}
	defer pDeleteObject.Call(f)
	old, _, _ := pSelectObject.Call(hdc, f)
	defer pSelectObject.Call(hdc, old)
	var buf [64]uint16
	n, _, _ := pGetTextFaceW.Call(hdc, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	if n == 0 {
		return false
	}
	return strings.EqualFold(syscall.UTF16ToString(buf[:]), face)
}
