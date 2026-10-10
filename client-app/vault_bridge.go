package main

// vault_bridge.go — the password vault's WebView bindings (MVP
// scope). Every window (main + each detached session window) gets
// the read/unlock/type bindings; management (list/create/save/delete/reveal)
// is main-window only, the same split localfs.go uses.
//
// Current simplifications (all listed
// here):
//   - Windows Hello: an opt-in second way to unlock, the code staying
//     the fallback (vault_hello.go; the model: vault.go). Reveal
//     still asks for the code.
//   - "Fresh re-authentication" for a widening edit (scope change, turning
//     off secureOnly) is not tracked separately: Save/Delete only require the
//     vault to already be unlocked. Reveal alone re-checks the code on every
//     call, since showing a secret on screen is the single most sensitive
//     action a session window's JS can trigger.
//   - No "Show" auto-hide countdown, no vault settings page (lock is
//     mandatory and non-configurable in this pass, so there is nothing to
//     toggle), no Settings > Vault autolock picker (fixed at 15 minutes).

import (
	"encoding/json"
	"errors"
)

// vaultEntryJSON is the wire shape of one entry sent to the page — exactly
// VaultEntryMeta's fields, spelled out here so the JSON keys are stable and
// documented in one place, and so a reflection test can confirm no secret
// field ever exists on it.
type vaultEntryJSON struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Username   string `json:"username"`
	SecureOnly bool   `json:"secureOnly"`
}

func toEntryJSON(m VaultEntryMeta) vaultEntryJSON {
	return vaultEntryJSON{ID: m.ID, Label: m.Label, Username: m.Username, SecureOnly: m.SecureOnly}
}

// vaultStateJSON answers reachVaultState.
type vaultStateJSON struct {
	State       string         `json:"state"`
	AutoLockMin int            `json:"autoLockMin"`
	Count       int            `json:"count"`
	Hello       vaultHelloJSON `json:"hello"` // Windows Hello: offered? on? (vault_hello.go)
}

func (b *nativeBridge) vaultState(tok string) (vaultStateJSON, error) {
	if !b.allowed(tok) {
		return vaultStateJSON{}, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return vaultStateJSON{State: "unreadable"}, nil
	}
	s := v.State()
	return vaultStateJSON{State: s.State, AutoLockMin: s.AutoLockMin, Count: s.Count, Hello: vaultHelloState(v, s.State)}, nil
}

// vaultForSessionJSON answers reachVaultForSession.
type vaultForSessionJSON struct {
	State   string           `json:"state"`
	Scope   vaultScopeJSON   `json:"scope"`
	Entries []vaultEntryJSON `json:"entries"`
}

type vaultScopeJSON struct {
	Known    bool  `json:"known"`
	TenantID int64 `json:"tenantId"`
	// The session's identity as the proxy recorded it from the server's
	// answer (never the page's): what "Save for this VM" pre-binds a new
	// entry to (vault_ui.go). VM: a Hyper-V VM console (the device fields
	// are its host's), VMID its VM ("" unknown).
	Origin     string `json:"origin"`
	DeviceID   int64  `json:"deviceId"`
	DeviceUUID string `json:"deviceUuid"`
	VM         bool   `json:"vm"`
	VMID       string `json:"vmId"`
}

// vaultForSession resolves scope itself from the proxy's tunnel registry —
// never from a value the page could have supplied — the same rule
// captureSessionResponse follows when it first learns the scope.
func (b *nativeBridge) vaultForSession(tok, sessionToken string) (vaultForSessionJSON, error) {
	if !b.allowed(tok) {
		return vaultForSessionJSON{}, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return vaultForSessionJSON{State: "unreadable"}, nil
	}
	state := v.State()
	out := vaultForSessionJSON{State: state.State, Entries: []vaultEntryJSON{}}
	if b.tunnels == nil {
		return out, nil
	}
	tun, _, ok := b.tunnels.lookup(sessionToken)
	if !ok || tun.tenant == 0 {
		return out, nil // scope.known stays false
	}
	out.Scope = vaultScopeJSON{Known: true, TenantID: tun.tenant, Origin: tun.origin, DeviceID: tun.deviceID, DeviceUUID: tun.deviceUUID,
		VM: tun.vm, VMID: tun.vmID}
	if state.State != "unlocked" {
		return out, nil
	}
	for _, m := range v.EntriesForSession(tun.vaultSession()) {
		out.Entries = append(out.Entries, toEntryJSON(m))
	}
	return out, nil
}

// vaultAsyncResult is the {reqID, ok, ...} shape every async vault call
// pushes back via window.__reachVaultEvent (native_bridge.go's b.evalLocal
// pattern, reused here so both the main and every detached session window
// receive it — vault_windows.go's evalVault below).
func (b *nativeBridge) evalVault(reqID string, ok bool, payloadJSON string) {
	if b.closed.Load() {
		return
	}
	origin, _ := json.Marshal(b.origin)
	id, _ := json.Marshal(reqID)
	okJS := "false"
	if ok {
		okJS = "true"
	}
	b.w.Eval("(function () { if (location.origin !== " + string(origin) + ") return; " +
		"var f = window.__reachVaultEvent; if (typeof f === 'function') f({\"type\":\"done\",\"reqID\":" + string(id) + ",\"ok\":" + okJS + ",\"data\":" + payloadJSON + "}); })();")
}

// vaultAsync runs fn on its own goroutine and pushes the result; the
// binding itself returns immediately (true) once accepted, matching the
// fsAsync convention this app already uses for slow WebView-thread work.
func (b *nativeBridge) vaultAsync(tok, reqID string, fn func() (interface{}, error)) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if !reqIDOK(reqID) {
		return false, errBadRequestID
	}
	go func() {
		res, err := func() (res interface{}, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = errVaultInternal
				}
			}()
			return fn()
		}()
		if err != nil {
			errPayload := map[string]interface{}{"code": vaultErrCode(err)}
			if we, ok := err.(*vaultWaitError); ok {
				errPayload["seconds"] = we.Seconds
			}
			payload, _ := json.Marshal(errPayload)
			b.w.Dispatch(func() { b.evalVault(reqID, false, string(payload)) })
			return
		}
		payload, jerr := json.Marshal(res)
		if jerr != nil {
			b.w.Dispatch(func() { b.evalVault(reqID, false, `{"code":"internal"}`) })
			return
		}
		js := string(payload)
		b.w.Dispatch(func() { b.evalVault(reqID, true, js) })
	}()
	return true, nil
}

var (
	errBadRequestID  = errNotAllowed // reuse: an invalid reqID is as good a reason to refuse as a bad token
	errVaultInternal = errors.New("vault: internal error")
	// vaultType's own failure modes (session closed, scope not yet known, a
	// stale view generation, the injector momentarily busy or disabled):
	// sentinels so vaultErrCode can tell them apart from a generic internal
	// error instead of flattening all of them to "internal" (they used to be
	// ad-hoc errors.New(...) values vaultErrCode did not recognize).
	errVaultClosed         = errors.New("vault: session closed")
	errVaultScopeUnknown   = errors.New("vault: scope unknown")
	errVaultSessionChanged = errors.New("vault: session changed")
	errVaultBusy           = errors.New("vault: injector busy")
	errVaultUnavailable    = errors.New("vault: injector unavailable")
)

// vaultErrCode maps a Vault error to the short string code the page/i18n
// layer switches on (viewer.vault.err.* / vault.code.*). Never the raw Go
// error text: it could leak implementation detail, and never any secret.
func vaultErrCode(err error) string {
	switch {
	case errors.Is(err, errVaultLocked):
		return "locked"
	case errors.Is(err, errKeyringUnavailable):
		return "keyring_unavailable"
	case errors.Is(err, errWrongCode):
		return "wrong_code"
	case errors.Is(err, errNotFound):
		return "not_found"
	case errors.Is(err, errCredMissing):
		return "cred_missing"
	case errors.Is(err, errCredInUse):
		return "cred_in_use"
	case errors.Is(err, errCredKeyRDP):
		return "cred_rdp_key"
	case errors.Is(err, errScopeMismatch):
		return "scope"
	case errors.Is(err, errEmptyLabel), errors.Is(err, errEmptySecret), errors.Is(err, errBadChars), errors.Is(err, errTooLong):
		return "invalid"
	case errors.Is(err, errVaultClosed):
		return "closed"
	case errors.Is(err, errVaultScopeUnknown):
		return "scope_unknown"
	case errors.Is(err, errVaultSessionChanged):
		return "session_changed"
	case errors.Is(err, errVaultBusy):
		return "busy"
	case errors.Is(err, errVaultUnavailable):
		return "unavailable"
	case errors.Is(err, errRdpUnavailable):
		return "rdp_unavailable"
	case errors.Is(err, errRdpNoWindow):
		return "rdp_timeout"
	case errors.Is(err, errPresenceOff), errors.Is(err, errHelloOff):
		return "hello_off"
	case errors.Is(err, errHelloNotAvail):
		return "hello_unavailable"
	case errors.Is(err, errHelloUserCancel):
		return "hello_canceled"
	case errors.Is(err, errHelloRetries):
		return "hello_retries"
	case errors.Is(err, errHelloBusy):
		return "hello_busy"
	case errors.Is(err, errHelloNotVerified):
		return "hello_failed"
	case errors.Is(err, errHelloSkipped):
		return "hello_skipped"
	case errors.Is(err, errVaultHeld):
		return "help_session"
	}
	if we, ok := err.(*vaultWaitError); ok {
		_ = we
		return "wait"
	}
	return "internal"
}

// ---------------------------------------------------------------------------
// Every window: read + unlock + type.
// ---------------------------------------------------------------------------

func (b *nativeBridge) vaultUnlock(tok, reqID, code string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if err := v.Unlock(code); err != nil {
			if we, ok := err.(*vaultWaitError); ok {
				return nil, we
			}
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

func (b *nativeBridge) vaultCreateWithCode(tok, reqID, code string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		const defaultAutoLockMin = 15
		if err := v.Create(code, defaultAutoLockMin); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

func (b *nativeBridge) vaultLock(tok string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if v, err := getAppVault(); err == nil {
		v.Lock()
	}
	return true, nil
}

// vaultType builds and injects a type_text frame for entry idHex into the
// live ObliReach session sessionToken, using the REMOTE tenant scope the
// proxy already resolved for that token (reach_tunnels.go) — never a value
// the page supplies. gen is the view generation the page last saw for this
// tab (reach_viewer.go's tab.gen); it travels through unchanged into the
// wire frame so the agent can refuse a stale click the same way it already
// refuses a stale ordinary keystroke. autoSas asks the agent to send
// Ctrl+Alt+Del first when the remote session is locked and its password
// field is not showing (agent typetext_prep.go; an older agent ignores it and
// types as before).
func (b *nativeBridge) vaultType(tok, reqID, sessionToken, idHex string, withUser, enter bool, gen *int, autoSas bool) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		if b.tunnels == nil {
			return nil, errVaultClosed
		}
		tun, inj, ok := b.tunnels.lookup(sessionToken)
		if !ok || inj == nil {
			return nil, errVaultClosed
		}
		if tun.vm { // (a VM console types its own way: vaultTypeVm)
			return nil, errVaultUnavailable
		}
		if tun.tenant == 0 {
			return nil, errVaultScopeUnknown
		}
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if gen == nil {
			return nil, errVaultSessionChanged
		}
		frame, err := v.BuildTypeText(idHex, tun.vaultSession(), withUser, func(username string, secret []byte) ([]byte, error) {
			return buildTypeTextFrame(reqID, username, secret, typeTextOpts{withUser: withUser, enter: enter, gen: *gen, autoSas: autoSas})
		})
		if err != nil {
			return nil, err
		}
		defer wipeBytesLocal(frame)
		if err := inj.Inject(frame); err != nil {
			switch err {
			case errInjectorBusy:
				return nil, errVaultBusy
			case errInjectorClosed:
				return nil, errVaultClosed
			case errInjectorDisabled:
				return nil, errVaultUnavailable
			}
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// vaultTypeVm: vaultType for a Hyper-V VM console (vm_console.go): the
// helper types the text on the guest's keyboard layout (layout: the KLID the
// operator chose for this VM, guest: its system, 'windows' or 'linux',
// vm_console_go.go buildVmTypeTextFrame). Same scope rules as vaultType: the
// session's (its host's) tenant and device, and its VM, as the proxy
// recorded them from the server's answer, never the page's.
func (b *nativeBridge) vaultTypeVm(tok, reqID, sessionToken, idHex string, withUser, enter bool, layout, guest string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		if b.tunnels == nil {
			return nil, errVaultClosed
		}
		tun, inj, ok := b.tunnels.lookup(sessionToken)
		if !ok || inj == nil {
			return nil, errVaultClosed
		}
		if !tun.vm {
			return nil, errVaultUnavailable
		}
		if tun.tenant == 0 {
			return nil, errVaultScopeUnknown
		}
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		frame, err := v.BuildTypeText(idHex, tun.vaultSession(), withUser, func(username string, secret []byte) ([]byte, error) {
			return buildVmTypeTextFrame(username, secret, withUser, enter, layout, guest)
		})
		if err != nil {
			if err == errVmTypeTooLong {
				return nil, errVaultUnavailable
			}
			return nil, err
		}
		defer wipeBytesLocal(frame)
		if err := inj.Inject(frame); err != nil {
			switch err {
			case errInjectorBusy:
				return nil, errVaultBusy
			case errInjectorClosed:
				return nil, errVaultClosed
			case errInjectorDisabled:
				return nil, errVaultUnavailable
			}
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------------------------------------------------------------------------
// Main window only: management.
// ---------------------------------------------------------------------------

type vaultEntryFullJSON struct {
	vaultEntryJSON
	Origin     string            `json:"origin"`
	TenantID   int64             `json:"tenantId"`
	TenantName string            `json:"tenantName"`
	Note       string            `json:"note"`
	Devices    []vaultDeviceJSON `json:"devices"` // the device restriction ([] = every device of its tenant)
}

// vaultDeviceJSON is one machine of an entry's device restriction (vault.go
// VaultDevice), as the management page shows and sends it.
// A VM-bound item (v5) carries its VM too: vmId (GUID), vmName; its
// origin / tenantId / uuid / id / name are then its host's.
type vaultDeviceJSON struct {
	Origin   string `json:"origin"`
	TenantID int64  `json:"tenantId"`
	UUID     string `json:"uuid"`
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	VMID     string `json:"vmId,omitempty"`
	VMName   string `json:"vmName,omitempty"`
}

func toDevicesJSON(ds []VaultDevice) []vaultDeviceJSON {
	out := []vaultDeviceJSON{}
	for _, d := range ds {
		out = append(out, vaultDeviceJSON{Origin: d.Origin, TenantID: int64(d.TenantID), UUID: d.UUID, ID: d.ID, Name: d.Name,
			VMID: d.VMID, VMName: d.VMName})
	}
	return out
}

// fromDevicesJSON: a tenant id out of uint32's range is refused (it would
// wrap onto another tenant), as a malformed entry.
func fromDevicesJSON(ds []vaultDeviceJSON) ([]VaultDevice, error) {
	var out []VaultDevice
	for _, d := range ds {
		if d.TenantID <= 0 || d.TenantID > 0xFFFFFFFF {
			return nil, errBadChars
		}
		out = append(out, VaultDevice{Origin: d.Origin, TenantID: uint32(d.TenantID), UUID: d.UUID, ID: d.ID, Name: d.Name,
			VMID: d.VMID, VMName: d.VMName})
	}
	return out, nil
}

func (b *nativeBridge) vaultList(tok string) ([]vaultEntryFullJSON, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return nil, nil
	}
	out := []vaultEntryFullJSON{}
	for _, m := range v.List() {
		out = append(out, vaultEntryFullJSON{
			vaultEntryJSON: toEntryJSON(m), Origin: m.Origin, TenantID: int64(m.TenantID), TenantName: m.TenantName, Note: m.Note,
			Devices: toDevicesJSON(m.Devices),
		})
	}
	return out, nil
}

// vaultNodeJSON is one folder or ssh/rdp connection in the local connection
// tree (servers.go's "local" server profile) — never a password entry, never
// a secret. Main window only, like vaultList.
type vaultNodeJSON struct {
	ID         string `json:"id"`
	Kind       byte   `json:"kind"` // vaultKindFolder / vaultKindSSH / vaultKindRDP
	ParentID   string `json:"parentId"`
	Label      string `json:"label"`
	Username   string `json:"username"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	AuthMethod byte   `json:"authMethod"`
	// v4: the reusable credential it uses ("" = its own username + secret;
	// Username / AuthMethod are then the credential's), whether a secret of
	// its own is stored, whether its key has a passphrase (yes / no only).
	CredID       string `json:"credId"`
	Stored       bool   `json:"stored"`
	KeyProtected bool   `json:"keyProtected"`
}

func toNodeJSON(m VaultEntryMeta) vaultNodeJSON {
	return vaultNodeJSON{
		ID: m.ID, Kind: m.Kind, ParentID: m.ParentID, Label: m.Label, Username: m.Username,
		Host: m.Host, Port: int(m.Port), AuthMethod: m.AuthMethod,
		CredID: m.CredID, Stored: m.Stored, KeyProtected: m.KeyProtected,
	}
}

// vaultCredentialJSON is one reusable credential of the local base (vault
// page, connection form): its description and the connections using it —
// never its secret or passphrase. Main window only, like vaultList.
type vaultCredentialJSON struct {
	ID           string             `json:"id"`
	Label        string             `json:"label"`
	Username     string             `json:"username"`
	AuthMethod   byte               `json:"authMethod"`
	Note         string             `json:"note"`
	KeyProtected bool               `json:"keyProtected"`
	Uses         []vaultCredUseJSON `json:"uses"`
}

type vaultCredUseJSON struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  byte   `json:"kind"`
}

func (b *nativeBridge) vaultCredentials(tok string) ([]vaultCredentialJSON, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return nil, nil
	}
	out := []vaultCredentialJSON{}
	for _, c := range v.Credentials() {
		j := vaultCredentialJSON{ID: c.ID, Label: c.Label, Username: c.Username, AuthMethod: c.AuthMethod, Note: c.Note,
			KeyProtected: c.KeyProtected, Uses: []vaultCredUseJSON{}}
		for _, u := range c.Uses {
			j.Uses = append(j.Uses, vaultCredUseJSON{ID: u.ID, Label: u.Label, Kind: u.Kind})
		}
		out = append(out, j)
	}
	return out, nil
}

// vaultByParent answers reachVaultByParent: the direct children (folders and
// ssh/rdp connections) of parentIDHex ("" = root) in the local connection
// tree. Every window (descriptions only, never a secret): a session window's
// "+" list and a tab moved there list them too (localconn_tools.go).
func (b *nativeBridge) vaultByParent(tok, parentIDHex string) ([]vaultNodeJSON, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return nil, nil
	}
	out := []vaultNodeJSON{}
	for _, m := range v.ByParent(parentIDHex) {
		out = append(out, toNodeJSON(m))
	}
	return out, nil
}

// vaultConnInfo answers reachVaultConnInfo: one ssh/rdp connection of the
// local tree (its description, never its secret), or null (none, locked, a
// folder or a password). A tab of a local connection moved to another
// window finds its connection again with it (reach_viewer.go reachAdoptFind).
func (b *nativeBridge) vaultConnInfo(tok, idHex string) (*vaultNodeJSON, error) {
	if !b.allowed(tok) {
		return nil, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return nil, nil
	}
	m, err := v.Get(idHex)
	if err != nil || (m.Kind != vaultKindSSH && m.Kind != vaultKindRDP) {
		return nil, nil
	}
	n := toNodeJSON(m)
	return &n, nil
}

// vaultSaveInput is the page's add/edit form (JSON, main window only — the
// secret is in page JS here, unlike vaultType, because the operator is the
// one typing it in, into this app's own form).
type vaultSaveInput struct {
	ID         string `json:"id"`
	Origin     string `json:"origin"`
	TenantID   int64  `json:"tenantId"`
	TenantName string `json:"tenantName"`
	Label      string `json:"label"`
	Username   string `json:"username"`
	Secret     string `json:"secret"`
	Note       string `json:"note"`
	SecureOnly bool   `json:"secureOnly"`

	// Local connection tree (servers.go's "local" server profile): a folder
	// or an ssh/rdp connection instead of a password. Kind 0 (omitted) is
	// vaultKindPassword, so every existing caller (the vault's own add/edit
	// form) is unaffected.
	Kind       byte   `json:"kind"`
	ParentID   string `json:"parentId"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	AuthMethod byte   `json:"authMethod"`

	// The device restriction of a password entry (absent / [] = none).
	Devices []vaultDeviceJSON `json:"devices"`

	// v4: a connection's credential ("" = its own), a key's passphrase (""
	// on an edit keeps it; clearPassphrase removes it).
	CredID          string `json:"credId"`
	Passphrase      string `json:"passphrase"`
	ClearPassphrase bool   `json:"clearPassphrase"`
}

func (b *nativeBridge) vaultSave(tok, reqID string, in vaultSaveInput) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		devs, err := fromDevicesJSON(in.Devices)
		if err != nil || in.TenantID < 0 || in.TenantID > 0xFFFFFFFF {
			in.Secret, in.Passphrase = "", ""
			if err == nil {
				err = errBadChars // (a tenant id out of uint32's range would wrap onto another one)
			}
			return nil, err
		}
		id, err := v.Save(SaveInput{
			ID: in.ID, Origin: in.Origin, TenantID: uint32(in.TenantID), TenantName: in.TenantName,
			Label: in.Label, Username: in.Username, Note: in.Note, Secret: in.Secret, SecureOnly: in.SecureOnly,
			Kind: in.Kind, ParentID: in.ParentID, Host: in.Host, Port: uint16(in.Port), AuthMethod: in.AuthMethod,
			Devices: devs, CredID: in.CredID, Passphrase: in.Passphrase, ClearPassphrase: in.ClearPassphrase,
		})
		in.Secret, in.Passphrase = "", ""
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})
}

func (b *nativeBridge) vaultDelete(tok, idHex string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	v, err := getAppVault()
	if err != nil {
		return false, errVaultInternal
	}
	if err := v.Delete(idHex); err != nil {
		return false, err
	}
	return true, nil
}

// vaultReveal re-checks the code on every call — see the file header on why
// this action alone does not simply trust "the vault happens to be
// unlocked" the way Save/Delete do in this pass.
func (b *nativeBridge) vaultReveal(tok, reqID, idHex, code string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		ok, err := v.VerifyCode(code)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errWrongCode
		}
		secret, err := v.Reveal(idHex)
		if err != nil {
			return nil, err
		}
		res := map[string]string{"secret": secret}
		// (a private key's passphrase, shown with it when there is one)
		if pass, err := v.RevealPassphrase(idHex); err == nil && pass != "" {
			res["passphrase"] = pass
		}
		return res, nil
	})
}
