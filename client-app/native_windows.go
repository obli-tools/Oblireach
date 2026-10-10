//go:build windows

package main

// The Windows side of the native bridge (native_bridge.go): the clipboard
// (platformClipboard: CF_UNICODETEXT, CF_HDROP, the clipboard sequence
// number), on the UI thread that owns the window used as clipboard owner.

import (
	"errors"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modShell32  = syscall.NewLazyDLL("shell32.dll")

	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber = modUser32.NewProc("GetClipboardSequenceNumber")

	procGlobalAlloc  = modKernel32.NewProc("GlobalAlloc")
	procGlobalFree   = modKernel32.NewProc("GlobalFree")
	procGlobalLock   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32.NewProc("GlobalUnlock")
	procGlobalSize   = modKernel32.NewProc("GlobalSize")

	procDragQueryFileW = modShell32.NewProc("DragQueryFileW")
)

const (
	cfUnicodeText = 13
	cfHDrop       = 15
	gmemMoveable  = 0x0002
)

var (
	_ bridgeHost    = (*webHost)(nil)
	_ appWindow     = (*webHost)(nil)
	_ mainAppWindow = (*mainWindow)(nil)
	_ frameOps      = hostFrame{}
)

// ── platformClipboard ──────────────────────────────────────────────────────

// winClipboard: the Windows clipboard (the raw calls below).
type winClipboard struct{}

var sysClipboard platformClipboard = winClipboard{}

func (winClipboard) sequence() uint32 { return clipSequence() }

func (winClipboard) read(owner uintptr) (clipboardInfo, error) {
	var info clipboardInfo
	if err := clipOpen(owner); err != nil {
		return info, err
	}
	defer clipClose()
	info.Seq = clipSequence()
	info.HasFiles = clipFormatAvailable(cfHDrop)
	if clipFormatAvailable(cfUnicodeText) {
		text, ok := clipReadTextLocked()
		if ok {
			info.HasText = true
			if len(text) > maxClipboardText {
				info.TooLarge = true
			} else {
				info.Text = text
			}
		}
	}
	return info, nil
}

func (winClipboard) writeText(owner uintptr, text string) (uint32, error) {
	return clipWriteText(owner, text)
}

func (winClipboard) filePaths(owner uintptr) ([]string, error) {
	if err := clipOpen(owner); err != nil {
		return nil, err
	}
	var paths []string
	if clipFormatAvailable(cfHDrop) {
		paths = clipReadFilesLocked()
	}
	clipClose()
	return paths, nil
}

// ── Raw clipboard access ───────────────────────────────────────────────────

func clipSequence() uint32 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(r)
}

// clipOpen opens the clipboard, retrying while another process holds it
// (OpenClipboard fails instead of waiting): 10 tries, 25 ms apart.
func clipOpen(hwnd uintptr) error {
	var lastErr error
	for i := 0; i < 10; i++ {
		r, _, e := procOpenClipboard.Call(hwnd)
		if r != 0 {
			return nil
		}
		lastErr = e
		time.Sleep(25 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("OpenClipboard failed")
	}
	return errors.New("clipboard busy: " + lastErr.Error())
}

func clipClose() { procCloseClipboard.Call() }

func clipFormatAvailable(format uintptr) bool {
	r, _, _ := procIsClipboardFormatAvailable.Call(format)
	return r != 0
}

// clipReadTextLocked reads CF_UNICODETEXT. The clipboard must be open.
func clipReadTextLocked() (string, bool) {
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	size, _, _ := procGlobalSize.Call(h)
	n := int(size / 2)
	if n <= 0 {
		return "", true
	}
	u := unsafe.Slice((*uint16)(globalPtr(p)), n)
	end := n
	for i, c := range u {
		if c == 0 {
			end = i
			break
		}
	}
	// Copy out before GlobalUnlock (utf16.Decode allocates).
	return string(utf16.Decode(u[:end])), true
}

// clipWriteText replaces the clipboard with CF_UNICODETEXT. Returns the
// clipboard sequence number right after the write (used to recognise our
// own change in the watcher).
func clipWriteText(hwnd uintptr, text string) (uint32, error) {
	u := utf16.Encode([]rune(text))
	u = append(u, 0)
	hMem, _, e := procGlobalAlloc.Call(gmemMoveable, uintptr(len(u)*2))
	if hMem == 0 {
		return 0, errors.New("GlobalAlloc failed: " + e.Error())
	}
	p, _, _ := procGlobalLock.Call(hMem)
	if p == 0 {
		procGlobalFree.Call(hMem)
		return 0, errors.New("GlobalLock failed")
	}
	copy(unsafe.Slice((*uint16)(globalPtr(p)), len(u)), u)
	procGlobalUnlock.Call(hMem)

	if err := clipOpen(hwnd); err != nil {
		procGlobalFree.Call(hMem)
		return 0, err
	}
	procEmptyClipboard.Call()
	r, _, e := procSetClipboardData.Call(cfUnicodeText, hMem)
	if r == 0 {
		// Ownership was not transferred: free it ourselves.
		procGlobalFree.Call(hMem)
		clipClose()
		return 0, errors.New("SetClipboardData failed: " + e.Error())
	}
	clipClose()
	return clipSequence(), nil
}

// clipReadFilesLocked lists the CF_HDROP paths. The clipboard must be open.
func clipReadFilesLocked() []string {
	h, _, _ := procGetClipboardData.Call(cfHDrop)
	if h == 0 {
		return nil
	}
	count, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	if count == 0 {
		return nil
	}
	if count > maxClipboardFiles {
		count = maxClipboardFiles
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

// globalPtr turns a pointer returned by GlobalLock (memory owned by Windows,
// never by the Go heap) into an unsafe.Pointer without tripping go vet's
// uintptr->Pointer check.
func globalPtr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}
