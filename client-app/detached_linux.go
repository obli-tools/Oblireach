//go:build linux

package main

// detached_linux.go — the detached session windows on Linux
// (sessionWindowBindings, platform.go): not implemented yet. The model
// (device keys, options and their checks, tab reports) is platform-neutral:
// detached_model.go. Phase 2: one UI host window per session window; tabs
// moved by the menu ("Move to window") first, then by an internal GTK
// drag-and-drop between the app's own windows (Wayland gives no global
// pointer position nor window placement: a new window is placed by the
// compositor).

// sessionWindows: the main window's manager of the session windows.
type sessionWindows struct {
	// localfs: the main window's local file system, shared with every
	// session window for the local pane of its Files tabs.
	localfs *localFS
}

// detachedWindow: a session window.
type detachedWindow struct{}

func (b *nativeBridge) openWindow(tok, deviceID, title string, opts detachOpts) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	return "", notImplemented("session windows")
}

func (b *nativeBridge) focusWindow(tok, key string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, nil
}

func (b *nativeBridge) closeWindow(tok, key string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, nil
}

func (b *nativeBridge) closeWindows(tok string) (int, error) {
	if !b.allowed(tok) {
		return 0, errNotAllowed
	}
	return 0, nil
}

func (b *nativeBridge) closeProfileWindows(tok, profile string) (int, error) {
	if !b.allowed(tok) {
		return 0, errNotAllowed
	}
	return 0, nil
}

func (b *nativeBridge) listWindows(tok string) ([]string, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	return []string{}, nil
}

func (b *nativeBridge) windowTabs(tok string, info windowTabsInfo) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return true, nil
}

func (b *nativeBridge) windowTargets(tok string) ([]windowTarget, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	return []windowTarget{}, nil
}

func (b *nativeBridge) closeOwnWindow(tok string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return false, notImplemented("session windows")
}

func (b *nativeBridge) openTab(tok, key string) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	return "", notImplemented("session windows")
}

func (b *nativeBridge) tearOff(tok, deviceID, title string, opts detachOpts) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	return "", notImplemented("session windows")
}
