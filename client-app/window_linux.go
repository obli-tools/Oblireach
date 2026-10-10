//go:build linux

package main

// window_linux.go — the per-window bindings of the Linux UI host, not
// implemented yet (windowBindings and inputBindings, platform.go):
//
//   - the page-drawn title bar (frame_windows.go on Windows): phase 2 uses an
//     undecorated GTK window, gdk_toplevel_begin_move / begin_resize (the
//     compositor moves it, quick tiling included) and notify::maximized;
//   - the taskbar attention (attention_windows.go): gtk_window_present
//     without an activation token (the window is marked as asking for
//     attention instead of being raised);
//   - the system keys of a session canvas (keyhook_windows.go): the
//     keyboard-shortcuts-inhibit protocol (gdk_toplevel_inhibit_system_shortcuts).

func (b *nativeBridge) windowDrag(tok, edge string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, notImplemented("window drag")
}

func (b *nativeBridge) windowControl(tok, action string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, notImplemented("window control")
}

// windowState: as a window without a native frame object (b.win nil) on
// Windows: active, not maximized.
func (b *nativeBridge) windowState(tok string) (windowStateInfo, error) {
	if !b.allowed(tok) {
		return windowStateInfo{}, errNotAllowed
	}
	return windowStateInfo{Active: true}, nil
}

func (b *nativeBridge) pushWindowState(windowStateInfo) {}

func (b *nativeBridge) attention(tok string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, nil
}

func (b *nativeBridge) keyCapture(tok string, on bool) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, notImplemented("system key capture")
}

// foregroundRoot: our window in the foreground (window_registry.go); none
// known yet (a Wayland client cannot read the global foreground window: the
// UI host will report its own focused window instead).
func foregroundRoot() uintptr { return 0 }
