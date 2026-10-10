//go:build windows

package main

// clipboard_paste_windows.go — the Windows clipboard read for a paste in a
// file explorer (clipboard_paste.go): CF_HDROP (and "Preferred DropEffect",
// to know a cut), the "PNG" format, CF_DIBV5, CF_DIB. Bound on every window
// (the main one and the session windows): their explorers all paste.
//
//   reachClipboardPasteKinds(tok)          -> {files, image, text} (no open)
//   reachClipboardPaste(tok, reqId, large) -> true, then the answer through
//       window.__reachLocalFs(reqId, ok, pasteResult) (local_pane.go localFs)
//
// The clipboard is opened on the UI thread (the window's: the bindings run
// there), read and closed at once; walking the folders and encoding an image
// happen afterwards on a goroutine (the window never waits for the disk).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	cfDIB        = 8
	cfDIBV5      = 17
	dropEffMove  = 2
	pasteMaxPath = 10000 // copied paths read, at most
)

var (
	procRegisterClipboardFormatW = modUser32.NewProc("RegisterClipboardFormatW")

	clipFmtOnce       sync.Once
	cfPreferredEffect uintptr
	cfPNG             uintptr
)

func clipFormats() {
	clipFmtOnce.Do(func() {
		reg := func(name string) uintptr {
			p, err := syscall.UTF16PtrFromString(name)
			if err != nil {
				return 0
			}
			r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(p)))
			return r
		}
		cfPreferredEffect = reg("Preferred DropEffect")
		cfPNG = reg("PNG")
	})
}

// clipPasteKindsNow: what the clipboard holds (IsClipboardFormatAvailable
// needs no open clipboard). An image counts alone only: with text (cells of
// a spreadsheet, a page...) Ctrl+V is not a screenshot.
func clipPasteKindsNow() pasteKinds {
	clipFormats()
	k := pasteKinds{Files: clipFormatAvailable(cfHDrop), Text: clipFormatAvailable(cfUnicodeText)}
	img := clipFormatAvailable(cfDIBV5) || clipFormatAvailable(cfDIB) || (cfPNG != 0 && clipFormatAvailable(cfPNG))
	k.Image = img && !k.Text && !k.Files
	return k
}

// clipReadRawLocked: a global memory format's bytes (copied). The clipboard
// must be open.
func clipReadRawLocked(format uintptr, max int) ([]byte, bool) {
	if format == 0 {
		return nil, false
	}
	h, _, _ := procGetClipboardData.Call(format)
	if h == 0 {
		return nil, false
	}
	size, _, _ := procGlobalSize.Call(h)
	if size == 0 || int(size) > max {
		return nil, false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil, false
	}
	defer procGlobalUnlock.Call(h)
	out := make([]byte, int(size))
	copy(out, unsafe.Slice((*byte)(globalPtr(p)), int(size)))
	return out, true
}

// clipPasteRaw: what was read from the clipboard (open, read, closed).
type clipPasteRaw struct {
	kind  string // "files", "png", "dib", "none"
	paths []string
	cut   bool
	data  []byte
}

// clipPasteReadLocked: the clipboard's paste. The clipboard must be open.
func clipPasteReadLocked() clipPasteRaw {
	clipFormats()
	if clipFormatAvailable(cfHDrop) {
		r := clipPasteRaw{kind: "files", paths: clipReadPathsLocked(pasteMaxPath)}
		if b, ok := clipReadRawLocked(cfPreferredEffect, 64); ok && len(b) >= 4 {
			r.cut = b[0]&dropEffMove != 0
		}
		return r
	}
	if clipFormatAvailable(cfUnicodeText) {
		return clipPasteRaw{kind: "none"}
	}
	if b, ok := clipReadRawLocked(cfPNG, pasteMaxDIB); ok && pngChecked(b) {
		return clipPasteRaw{kind: "png", data: b}
	}
	for _, f := range []uintptr{cfDIBV5, cfDIB} {
		if clipFormatAvailable(f) {
			if b, ok := clipReadRawLocked(f, pasteMaxDIB); ok {
				return clipPasteRaw{kind: "dib", data: b}
			}
		}
	}
	return clipPasteRaw{kind: "none"}
}

// clipReadPathsLocked: the CF_HDROP paths (max of them). The clipboard must
// be open.
func clipReadPathsLocked(max int) []string {
	h, _, _ := procGetClipboardData.Call(cfHDrop)
	if h == 0 {
		return nil
	}
	count, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	if int(count) > max {
		count = uintptr(max)
	}
	out := make([]string, 0, count)
	for i := uintptr(0); i < count; i++ {
		n, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		if n == 0 || n > 32767 {
			continue
		}
		buf := make([]uint16, n+1)
		got, _, _ := procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), n+1)
		if got == 0 {
			continue
		}
		out = append(out, syscall.UTF16ToString(buf[:got]))
	}
	return out
}

// pasteProtectR: the app's own data, never read (localfs_windows.go
// newLocalFS: config.json's folder, its %LOCALAPPDATA% folder, its WebView2
// profile).
func pasteProtectR() []string {
	var out []string
	add := func(p string) {
		if p != "" && filepath.IsAbs(p) {
			out = append(out, filepath.Clean(p))
		}
	}
	add(configDir)
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		add(filepath.Join(local, "OblireachClient"))
	}
	if appData, exe := os.Getenv("APPDATA"), exeName(); appData != "" && exe != "" {
		add(filepath.Join(appData, exe))
	}
	return out
}

func exeName() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Base(exe)
}

// pasteBuild: the page's answer from what the clipboard held.
func pasteBuild(raw clipPasteRaw, large bool, now time.Time) (*pasteResult, error) {
	switch raw.kind {
	case "files":
		res, err := pasteFiles(winSys{}, pasteProtectR(), raw.paths, large)
		if err != nil {
			return nil, err
		}
		res.Cut = raw.cut
		return res, nil
	case "png":
		return pasteImage(raw.data, now)
	case "dib":
		b, err := dibToPNG(raw.data)
		if err != nil {
			return &pasteResult{Kind: "none", Items: []pasteItem{}}, nil
		}
		return pasteImage(b, now)
	}
	return &pasteResult{Kind: "none", Items: []pasteItem{}}, nil
}

func (b *nativeBridge) clipboardPasteKinds(tok string) (pasteKinds, error) {
	if !b.allowed(tok) {
		return pasteKinds{}, errNotAllowed
	}
	return clipPasteKindsNow(), nil
}

var errPasteBusy = errors.New("busy")

func (b *nativeBridge) clipboardPaste(tok, reqID string, large bool) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if !reqIDOK(reqID) {
		return false, errFsBadRequest
	}
	if err := clipOpen(b.hwnd); err != nil {
		return false, errPasteBusy
	}
	raw := clipPasteReadLocked()
	clipClose()
	go func() {
		ok, payload := true, []byte(nil)
		res, err := func() (res *pasteResult, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = errors.New("io")
				}
			}()
			return pasteBuild(raw, large, time.Now())
		}()
		if err == nil {
			payload, err = json.Marshal(res)
		}
		if err != nil {
			ok, payload = false, fsErrJSON(fsE("io"))
		}
		js := string(payload)
		b.w.Dispatch(func() { b.evalLocalFs(reqID, ok, js) })
	}()
	return true, nil
}
