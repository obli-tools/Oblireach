//go:build linux

package main

// clipboard_paste_linux.go — Ctrl+V of files and images in a file explorer
// on Linux (clipboardPasteBindings, platform.go): not implemented yet (the
// rules are platform-neutral: clipboard_paste.go). Phase 2: GdkClipboard's
// text/uri-list and "cut" markers (GNOME, KDE), image/png.

// clipboardPasteKinds: nothing on the clipboard can be pasted yet.
func (b *nativeBridge) clipboardPasteKinds(tok string) (pasteKinds, error) {
	if !b.allowed(tok) {
		return pasteKinds{}, errNotAllowed
	}
	return pasteKinds{}, nil
}

func (b *nativeBridge) clipboardPaste(tok, reqID string, large bool) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, notImplemented("clipboard paste")
}
