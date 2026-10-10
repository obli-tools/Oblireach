package main

// sftp_bridge.go — WebView bindings for the local RDP/SSH
// connection tree's file explorer (on top of sftp_session.go and
// ssh_bridge.go). Main window only, same split as the
// vault's management bindings and connections_page.go's reachVaultByParent
// (a local server profile only ever shows in the main window's nav).
//
// The session is the backend of the SAME file explorer as an Obliance
// device's (explorer_viewer.go, its Files page tab, transfers, editing): the
// page connects (reachSftpConnect: the vault's secret never leaves Go, the
// host key decided like the terminal's), sends the explorer's own commands
// (reachSftpCmd: localconn_explorer.go maps them onto SFTP) and closes it
// with its last explorer (reachSftpClose).

import (
	"context"
	"encoding/json"
	"errors"
)

var errSFTPWrongKind = errors.New("sftp: entry is not an ssh connection")

// sftpConnectResult mirrors sshConnectResult's shape (sshHostKeyJSON is
// reused as-is — the same host, the same known_hosts store, the same
// operator decision either transport would show).
type sftpConnectResult struct {
	ConnID  string          `json:"connId,omitempty"`
	HostKey *sshHostKeyJSON `json:"hostKey,omitempty"`
	// Mode: "scp" when the server has no SFTP subsystem (scp.go: the same
	// explorer over SCP and its shell); Listing false: no shell either.
	Mode    string `json:"mode,omitempty"`
	Listing *bool  `json:"listing,omitempty"`
}

func (b *nativeBridge) sftpConnect(tok, reqID, idHex string, trust bool) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if m, err := v.Get(idHex); err != nil {
			return nil, err
		} else if m.Kind != vaultKindSSH {
			return nil, errSFTPWrongKind
		}
		// (its own secret, or its credential's: vault_conn.go ResolveConn)
		rc, err := v.ResolveConn(idHex)
		if err != nil {
			return nil, err
		}
		meta, secret := rc.Meta, rc.Secret
		store, err := newKnownHostsStore()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), sshDialTimeout)
		defer cancel()
		sess, err := sftpDial(ctx, sshDialParams{
			Host: meta.Host, Port: int(meta.Port), Username: meta.Username,
			AuthMethod: meta.AuthMethod, Secret: secret, Passphrase: rc.Passphrase,
		}, store, trust)
		if err != nil {
			var pend *sshHostKeyPending
			if errors.As(err, &pend) {
				return sftpConnectResult{HostKey: &sshHostKeyJSON{
					State: hostKeyStateString(pend.State), Fingerprint: pend.Fingerprint, Hostport: pend.Hostport,
				}}, nil
			}
			return nil, err
		}
		sess.vaultID = idHex
		connID := appSFTPRegistry.newID()
		appSFTPRegistry.put(connID, sess)
		res := sftpConnectResult{ConnID: connID, Mode: sess.mode()}
		if sess.scp != nil {
			l := sess.scp.shell
			res.Listing = &l
		}
		return res, nil
	})
}

func (b *nativeBridge) sftpClose(tok, connID string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	sess, ok := appSFTPRegistry.get(connID)
	if !ok {
		return true, nil
	}
	appSFTPRegistry.remove(connID)
	sess.Close()
	return true, nil
}

func sftpGetSession(connID string) (*sftpSession, error) {
	sess, ok := appSFTPRegistry.get(connID)
	if !ok || sess.isClosed() {
		return nil, errSFTPGone
	}
	return sess, nil
}

var errSFTPGone = errors.New("sftp: session closed")

// sftpCmd runs one file explorer command (explorer_viewer.go's vocabulary:
// localconn_explorer.go) on a live session. Always resolves (never the
// vault's error codes): {ok:true, result} or {ok:false, error[, gone]}, so
// the explorer quotes the SFTP server's own words ("already exists" asks
// to replace, like with an Obliance agent). gone: the session ended (the
// page reconnects once). No 30 s ceiling of its own: the page waits as long
// as the explorer waits for that command (EXPLORER_LONG_MS for a file).
func (b *nativeBridge) sftpCmd(tok, reqID, connID, typ, payloadJSON string) (bool, error) {
	if !b.allowed(tok) {
		return false, errNotAllowed
	}
	if !reqIDOK(reqID) {
		return false, errBadRequestID
	}
	if len(payloadJSON) > (explorerMaxBytes/3)*4+64*1024 {
		return false, errSFTPPathInvalid
	}
	go func() {
		out := func() (out map[string]interface{}) {
			defer func() {
				if r := recover(); r != nil {
					out = map[string]interface{}{"ok": false, "error": "internal error"}
				}
			}()
			sess, ok := appSFTPRegistry.get(connID)
			if !ok {
				return map[string]interface{}{"ok": false, "error": errSFTPGone.Error(), "gone": true}
			}
			return sftpRunExplorerCmd(sess, typ, []byte(payloadJSON))
		}()
		js, err := json.Marshal(out)
		if err != nil {
			js = []byte(`{"ok":false,"error":"internal error"}`)
		}
		b.w.Dispatch(func() { b.evalVault(reqID, true, string(js)) })
	}()
	return true, nil
}
