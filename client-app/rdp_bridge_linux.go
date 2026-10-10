//go:build linux

package main

// rdp_bridge_linux.go — the local RDP sessions of the connection tree on
// Linux (rdpBindings, platform.go): the stream mode. Wayland cannot embed
// another process's window (the Windows way: mstsc.exe reparented,
// rdp_session.go), so a FreeRDP 3 helper (rdphost, rdphost_proc.go) streams
// H.264 to the page's RDP tab through a local WebSocket
// (rdphost_relay.go); the tab draws it like an ObliReach session
// (rdp_stream_view.go). The tab stays TOOL_IMPL.rdp: its session window,
// "Detach by default", tear-off, merge and mosaic work as for a terminal.
//
// The bindings keep their Windows signatures (the page's contract):
//   - reachRdpConnect(reqId, id, x, y, w, h): w, h the tab's size in device
//     pixels; in stream mode x carries the page's scale (devicePixelRatio x
//     100), y is unused. Resolves {connId}.
//   - reachRdpTunnel(connId): a one-time token of /local/rdp-tunnel/<token>
//     (after a connect or an attach: the page then opens its WebSocket).
//   - reachRdpResize / reachRdpShow: nothing to place (the picture is in
//     the page); the size goes over the tunnel (rdp_resize).
//   - reachRdpPark(connId) / reachRdpAttach(park): the tab moves to another
//     window; the helper goes on (rdp_park.go's one-time token).
//   - reachRdpClose(connId): the session ends. A window that closes ends
//     the sessions it shows (rdpCloseOwned).

import (
	"errors"
	"sync/atomic"
)

// The errors the vault bindings report for an RDP launch (vault_bridge.go
// vaultErrCode).
var (
	errRdpUnavailable = errors.New("rdp: local RDP client not available")
	errRdpNoWindow    = errors.New("rdp: session window not found in time")
)

// rdpParkHwnd / rdpMainBridge: the main window and its bridge (kept for the
// platform-neutral code; the stream mode needs no native parking window).
var (
	rdpParkHwnd   atomic.Uintptr
	rdpMainBridge atomic.Pointer[nativeBridge]
)

// rdpConnectResult is reachRdpConnect's resolved payload.
type rdpConnectResult struct {
	ConnID string `json:"connId"`
	Mode   string `json:"mode"` // "stream"
}

// rdpStreams: the registry the bindings use (tests: their own).
var rdpStreams = appRdpStreams

func (b *nativeBridge) rdpConnect(tok, reqID, idHex string, x, y, w, h int32) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		raw, err := v.Get(idHex)
		if err != nil {
			return nil, err
		}
		if raw.Kind != vaultKindRDP {
			return nil, errRdpWrongKind
		}
		// (its own password, or its credential's: the secret stays in Go)
		rc, err := v.ResolveConn(idHex)
		if err != nil {
			return nil, err
		}
		user, dom := rdpSplitUser(rc.Meta.Username)
		port := int(rc.Meta.Port)
		if port == 0 {
			port = 3389
		}
		req := rdpConnectReq{Label: idHex, Host: rc.Meta.Host, Port: port, Username: user, Domain: dom, Password: rc.Secret,
			Width: int(w), Height: int(h), Scale: int(x), Layout: rdpLayoutOf(loadConfig().uiLang())}
		// (typed credentials replace the entry's own only: never a shared credential's)
		if raw.CredID == "" {
			req.SaveCreds = func(username, password string) { rdpSaveCreds(idHex, username, password) }
		}
		rc.Secret = ""
		s, err := rdpStreams.start(req, b, appRdpKnownHosts())
		req.Password = ""
		if err != nil {
			if errors.Is(err, errRdpHelperMissing) || errors.Is(err, errRdpHelperProtocol) {
				return nil, errRdpUnavailable
			}
			return nil, err
		}
		return rdpConnectResult{ConnID: s.id, Mode: "stream"}, nil
	})
}

// rdpSaveCreds: credentials typed in the session's prompt replace its
// entry's own (the vault unlocked; a failure leaves the entry as it was).
func rdpSaveCreds(idHex, username, password string) {
	v, err := getAppVault()
	if err != nil {
		return
	}
	m, err := v.Get(idHex)
	if err != nil || m.Kind != vaultKindRDP || m.CredID != "" {
		return
	}
	_, _ = v.Save(SaveInput{ID: m.ID, Origin: m.Origin, TenantID: m.TenantID, TenantName: m.TenantName, Label: m.Label,
		Username: username, Note: m.Note, Secret: password, SecureOnly: m.SecureOnly, Kind: m.Kind, ParentID: m.ParentID,
		Host: m.Host, Port: m.Port, AuthMethod: m.AuthMethod, Devices: m.Devices})
}

// rdpOwned: the session connID shown by this window (nil otherwise).
func (b *nativeBridge) rdpOwned(connID string) *rdpStream {
	s := rdpStreams.get(connID)
	if s == nil || !s.ownedBy(b) {
		return nil
	}
	return s
}

func (b *nativeBridge) rdpTunnel(tok, connID string) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	s := b.rdpOwned(connID)
	if s == nil {
		return "", errRdpNotHere
	}
	return rdpStreams.issueTunnel(s)
}

// rdpResize / rdpShow: the picture is drawn by the page (nothing native to
// place); a session of this window answers true.
func (b *nativeBridge) rdpResize(tok, connID string, x, y, w, h int32) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return b.rdpOwned(connID) != nil, nil
}

func (b *nativeBridge) rdpShow(tok, connID string, show bool, x, y, w, h int32) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	return b.rdpOwned(connID) != nil, nil
}

func (b *nativeBridge) rdpPark(tok, connID string) (string, error) {
	if !b.allowed(tok) {
		return "", errNotAllowed
	}
	s := b.rdpOwned(connID)
	if s == nil {
		return "", errRdpNotHere
	}
	return s.park()
}

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
	s := rdpStreams.get(connID)
	if s == nil {
		return "", errRdpNotHere
	}
	s.adopt(b)
	return connID, nil
}

func (b *nativeBridge) rdpClose(tok, connID string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	s := rdpStreams.get(connID)
	if s == nil {
		return true, nil
	}
	if !s.ownedBy(b) {
		return false, errRdpNotHere
	}
	appRdpParks.dropConn(connID)
	s.end()
	return true, nil
}

// rdpCloseOwned: this window goes: the sessions it shows end.
func (b *nativeBridge) rdpCloseOwned() {
	for _, s := range rdpStreams.ownedBy(b) {
		appRdpParks.dropConn(s.id)
		s.end()
	}
}
