//go:build linux

package main

// native_linux.go — the Linux side of the native bridge (native_bridge.go):
// the clipboard (platformClipboard, platform.go), not implemented yet.
// Phase 2: GdkClipboard on the UI thread (text, text/uri-list and the
// GNOME / KDE "copied files" types, image/png); under Wayland the clipboard
// is only readable by the focused window, so the watcher reads it again when
// a window of the app gets the focus.

type linuxClipboard struct{}

var sysClipboard platformClipboard = linuxClipboard{}

func (linuxClipboard) sequence() uint32 { return 0 }

func (linuxClipboard) read(uintptr) (clipboardInfo, error) {
	return clipboardInfo{}, notImplemented("clipboard")
}

func (linuxClipboard) writeText(uintptr, string) (uint32, error) {
	return 0, notImplemented("clipboard")
}

func (linuxClipboard) filePaths(uintptr) ([]string, error) {
	return nil, notImplemented("clipboard")
}
