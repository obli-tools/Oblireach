//go:build windows

package main

// rdp_bridge_windows.go — WebView bindings for the local RDP/SSH connection
// tree's RDP sessions (rdp_session.go, the vault's rdp entries), in every
// window: a local RDP connection is a TAB like the others (rdp_view.go:
// TOOL_IMPL.rdp), so its session window, "Detach by default", tear-off and
// merge work as for a terminal.
//
// The picture is mstsc.exe's own window embedded in a container HWND, a
// child of the window whose page shows the tab (created and destroyed on the
// UI thread, where every window of the app lives). The page tells where its
// tab's placeholder is (reachRdpShow: shown at that rect, or hidden while
// another tab, another page or a dialog is on screen). Moving the tab to
// another window: reachRdpPark (hidden, held by the main window, a one-time
// token: rdp_park.go) then reachRdpAttach in the new window's page (the
// container placed there, its events told there). Closing the tab:
// reachRdpClose. A window that closes ends the sessions it shows.

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"
)

// (errRdpWrongKind, errRdpNotHere: rdphost_relay.go, every platform)

// rdpParkHwnd: the main window, which holds a session's container while it
// moves (never shown there); rdpMainBridge: its bridge (the UI thread's
// queue when the window a session came from is gone).
var (
	rdpParkHwnd   atomic.Uintptr
	rdpMainBridge atomic.Pointer[nativeBridge]
)

// rdpLive: the first of these bridges whose window still lives, else the
// main window's (nil: the app is quitting).
func rdpLive(bs ...*nativeBridge) *nativeBridge {
	for _, b := range bs {
		if b != nil && !b.closed.Load() {
			return b
		}
	}
	if m := rdpMainBridge.Load(); m != nil && !m.closed.Load() {
		return m
	}
	return nil
}

// rdpConnectResult is reachRdpConnect's resolved payload.
type rdpConnectResult struct {
	ConnID string `json:"connId"`
}

// rdpOnUI runs f on the UI thread and waits for it (an async binding's
// goroutine only: never from the UI thread itself).
func (b *nativeBridge) rdpOnUI(f func() (uintptr, error)) (uintptr, error) {
	type res struct {
		h   uintptr
		err error
	}
	ch := make(chan res, 1)
	b.w.Dispatch(func() { h, err := f(); ch <- res{h, err} })
	select {
	case r := <-ch:
		return r.h, r.err
	case <-time.After(10 * time.Second):
		return 0, errors.New("rdp: the window did not answer")
	}
}

// rdpDestroyOnUI: a container destroyed on the UI thread (its creator),
// through a window that still lives.
func (b *nativeBridge) rdpDestroyOnUI(container uintptr) {
	if container == 0 {
		return
	}
	if d := rdpLive(b); d != nil {
		d.w.Dispatch(func() { rdpDestroyContainer(container) })
	}
}

// rdpConnect resolves the entry, reveals its secret (Go-internal only, same
// rule vaultType/sshConnect follow: the password never crosses into page
// JS), and launches+embeds mstsc.exe positioned at (x,y) sized (w,h) — the
// on-screen rect of the page's placeholder element (rdp_view.go).
func (b *nativeBridge) rdpConnect(tok, reqID, idHex string, x, y, w, h int32) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if m, err := v.Get(idHex); err != nil {
			return nil, err
		} else if m.Kind != vaultKindRDP {
			return nil, errRdpWrongKind
		}
		// (its own password, or its credential's: vault_conn.go ResolveConn)
		rc, err := v.ResolveConn(idHex)
		if err != nil {
			return nil, err
		}
		meta, secret := rc.Meta, rc.Secret
		container, err := b.rdpOnUI(func() (uintptr, error) { return rdpCreateContainer(b.hwnd, x, y, w, h) })
		if err != nil {
			return nil, err
		}
		connID := appRDPRegistry.newID()
		sess, err := rdpConnect(meta.Host, int(meta.Port), meta.Username, secret, container, 0, 0, w, h,
			func(closeErr error) {
				var owner *nativeBridge
				if e, ok := appRDPRegistry.take(connID); ok {
					owner = e.owner
				}
				appRdpParks.dropConn(connID)
				rdpLive(owner, b).rdpDestroyOnUI(container)
				// (told to the window showing it; parked or its window gone: nobody)
				if owner != nil && !owner.closed.Load() {
					owner.w.Dispatch(func() { owner.rdpEvalClosed(connID, closeErr) })
				}
			},
		)
		if err != nil {
			b.rdpDestroyOnUI(container)
			return nil, err
		}
		appRDPRegistry.put(connID, sess, container, b)
		return rdpConnectResult{ConnID: connID}, nil
	})
}

// rdpResize moves/resizes both the container and the embedded mstsc window
// together (the container's own position is relative to b.hwnd, same
// coordinate space as (x,y) at connect time; the embedded window inside the
// container is always kept at (0,0) filling it).
func (b *nativeBridge) rdpResize(tok, connID string, x, y, w, h int32) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	entry, ok := appRDPRegistry.get(connID)
	if !ok || entry.owner != b {
		return false, nil
	}
	rdpResizeContainer(entry.container, x, y, w, h)
	entry.sess.Resize(0, 0, w, h)
	return true, nil
}

// rdpShow: the picture shown at the tab's rect, or hidden (its tab not on screen).
func (b *nativeBridge) rdpShow(tok, connID string, show bool, x, y, w, h int32) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	entry, ok := appRDPRegistry.get(connID)
	if !ok || entry.owner != b {
		return false, nil
	}
	if show && (w <= 0 || h <= 0) {
		show = false
	}
	rdpShowContainer(entry.container, show, x, y, w, h)
	if show {
		entry.sess.Resize(0, 0, w, h)
	}
	return true, nil
}

// rdpPark: the session leaves this window (its tab moves): hidden, held by
// the main window; the token the other window's page takes it over with.
func (b *nativeBridge) rdpPark(tok, connID string) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	entry, ok := appRDPRegistry.get(connID)
	if !ok || entry.owner != b {
		return "", errRdpNotHere
	}
	park := rdpParkHwnd.Load()
	if park == 0 {
		return "", errRdpNotHere
	}
	t, err := appRdpParks.park(connID, func(id string) {
		// (never taken over: the session ends, its picture with it)
		if e, ok := appRDPRegistry.take(id); ok {
			e.sess.Close()
			if d := rdpLive(); d != nil {
				d.rdpDestroyOnUI(e.container)
			}
		}
	})
	if err != nil {
		return "", err
	}
	rdpShowContainer(entry.container, false, 0, 0, 0, 0)
	rdpReparentContainer(entry.container, park)
	appRDPRegistry.setOwner(connID, nil)
	return t, nil
}

// rdpAttach: a parked session taken over by this window's page, its picture
// at (x,y,w,h) here; its connection id.
func (b *nativeBridge) rdpAttach(tok, park string, x, y, w, h int32) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	if !reParkToken.MatchString(park) {
		return "", errRdpNotHere
	}
	connID := appRdpParks.take(park)
	if connID == "" {
		return "", errRdpNotHere
	}
	entry, ok := appRDPRegistry.get(connID)
	if !ok {
		return "", errRdpNotHere
	}
	appRDPRegistry.setOwner(connID, b)
	rdpReparentContainer(entry.container, b.hwnd)
	if w > 0 && h > 0 {
		rdpShowContainer(entry.container, true, x, y, w, h)
		entry.sess.Resize(0, 0, w, h)
	}
	return connID, nil
}

func (b *nativeBridge) rdpClose(tok, connID string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if o := appRDPRegistry.ownerOf(connID); o != nil && o != b {
		return false, errRdpNotHere
	}
	entry, ok := appRDPRegistry.take(connID)
	if !ok {
		return true, nil
	}
	appRdpParks.dropConn(connID)
	entry.sess.Close()
	rdpDestroyContainer(entry.container)
	return true, nil
}

// rdpCloseOwned: this window goes: the sessions it shows end (their
// containers, its children, go with it).
func (b *nativeBridge) rdpCloseOwned() {
	for _, id := range appRDPRegistry.ownedBy(b) {
		if e, ok := appRDPRegistry.take(id); ok {
			appRdpParks.dropConn(id)
			e.sess.Close()
		}
	}
}

// rdpTunnel: no picture tunnel on Windows (mstsc.exe draws its own window;
// the stream mode is Linux's: rdp_bridge_linux.go).
func (b *nativeBridge) rdpTunnel(tok, connID string) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	return "", errRdpNotStream
}

// rdpEvalClosed pushes to window.__reachRdpEvent, mirroring
// ssh_bridge.go's sshEvalClosed — the connection can end on its own
// (mstsc.exe exits) long after reachRdpConnect's own promise resolved.
func (b *nativeBridge) rdpEvalClosed(connID string, closeErr error) {
	if b.closed.Load() {
		return
	}
	origin, _ := json.Marshal(b.origin)
	id, _ := json.Marshal(connID)
	reason := ""
	if closeErr != nil {
		reason = closeErr.Error()
		if len(reason) > 300 {
			reason = reason[:300]
		}
	}
	reasonJS, _ := json.Marshal(reason)
	b.w.Eval("(function () { if (location.origin !== " + string(origin) + ") return; " +
		"var f = window.__reachRdpEvent; if (typeof f === 'function') f({\"type\":\"closed\",\"connId\":" + string(id) + ",\"reason\":" + string(reasonJS) + "}); })();")
}
