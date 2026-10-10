package main

// ssh_bridge.go — WebView bindings for the local RDP/SSH connection
// tree's SSH terminals (ssh_session.go, known_hosts.go, ssh_relay.go).
//
// Every window gets these bindings: a local SSH terminal is a tab like an
// Obliance agent's (localconn_tools.go) and moves between windows like one.
// reachSshConnect dials (host key decided by the operator, the vault's
// secret used here, never handed to the page) and answers with a one-time
// TUNNEL token: the page then opens /local/ssh-tunnel/<token> (ssh_relay.go),
// the same protocol as an agent's terminal tunnel, so the shell lives in the
// proxy (term_park.go parks it while its tab moves to another window).

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

const sshDialTimeout = 20 * time.Second

// sshConnectResult is reachSshConnect's resolved payload: either a live
// connId, or a host key the operator must decide on (never both).
type sshConnectResult struct {
	Token   string          `json:"token,omitempty"` // its tunnel: /local/ssh-tunnel/<token>
	HostKey *sshHostKeyJSON `json:"hostKey,omitempty"`
}

type sshHostKeyJSON struct {
	State       string `json:"state"` // "unknown" | "mismatch"
	Fingerprint string `json:"fingerprint"`
	Hostport    string `json:"hostport"`
}

var errSSHWrongKind = errors.New("ssh: entry is not an SSH connection")

// sshConnect resolves the entry, reveals its secret (Go-internal only, the
// same "never crosses into page JS" rule vaultType follows for the secret
// itself), and dials. trust=false the first time; if the host key is
// unknown or changed, the promise resolves with hostKey set and no connId —
// the page shows the fingerprint and, on accept, calls again with
// trust=true, which both records and uses the key in the same dial (no gap
// between "shown" and "trusted").
func (b *nativeBridge) sshConnect(tok, reqID, idHex string, cols, rows int, trust bool) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if m, err := v.Get(idHex); err != nil {
			return nil, err
		} else if m.Kind != vaultKindSSH {
			return nil, errSSHWrongKind
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
		client, err := sshDial(ctx, sshDialParams{
			Host: meta.Host, Port: int(meta.Port), Username: meta.Username,
			AuthMethod: meta.AuthMethod, Secret: secret, Passphrase: rc.Passphrase,
		}, store, trust)
		if err != nil {
			var pend *sshHostKeyPending
			if errors.As(err, &pend) {
				return sshConnectResult{HostKey: &sshHostKeyJSON{
					State: hostKeyStateString(pend.State), Fingerprint: pend.Fingerprint, Hostport: pend.Hostport,
				}}, nil
			}
			return nil, err
		}
		tok, err := appLocalShells.add(realSSHClient{c: client, id: appSSHRegistry.newID()}, cols, rows, idHex)
		if err != nil {
			client.Close()
			return nil, err
		}
		return sshConnectResult{Token: tok}, nil
	})
}

func hostKeyStateString(s hostKeyState) string {
	if s == hostKeyMismatch {
		return "mismatch"
	}
	return "unknown"
}

// sshCwd: the folder of a live local shell, for files dropped on its
// terminal (ssh_relay.go localCwdScript): {path, sure}; path "" when it
// cannot be known (the page then asks, like for an agent's shell).
func (b *nativeBridge) sshCwd(tok, reqID, tunnel string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		if !reParkToken.MatchString(tunnel) {
			return localCwd{}, nil
		}
		return appLocalShells.cwd(tunnel), nil
	})
}

// localInfo: the Info panel's live view of a local SSH connection that is
// open (its terminal or its files, in any window): localconn_info.go;
// {open: false} when none is (nothing is dialed for it).
func (b *nativeBridge) localInfo(tok, reqID, idHex string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		v, err := getAppVault()
		if err != nil {
			return nil, errVaultInternal
		}
		if meta, err := v.Get(idHex); err != nil || meta.Kind != vaultKindSSH {
			return localInfo{}, nil // (an entry of the vault, an SSH one)
		}
		return localInfoOf(idHex, localInfoClient), nil
	})
}

// connProbe answers reachConnProbe: which local connections' host:port
// answer a TCP connection now (conn_probe.go), {id: bool}, for the Status
// column of the lists. idsJSON: their vault ids (never an address).
func (b *nativeBridge) connProbe(tok, reqID, idsJSON string) (bool, error) {
	return b.vaultAsync(tok, reqID, func() (interface{}, error) {
		var ids []string
		if len(idsJSON) > 64*1024 || json.Unmarshal([]byte(idsJSON), &ids) != nil {
			return map[string]bool{}, nil
		}
		v, err := getAppVault()
		if err != nil {
			return map[string]bool{}, nil
		}
		return probeConns(ids, func(id string) (string, int, bool) { return vaultConnAddr(v, id) }, func(addr string, d time.Duration) (net.Conn, error) { return net.DialTimeout("tcp", addr, d) }), nil
	})
}
