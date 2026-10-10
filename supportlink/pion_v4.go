//go:build !nt6

package supportlink

// Modern build line: pion stun v4 / transport v5. pion_v3_nt6.go is the same
// file for the nt6 line (stun v0.6 / transport v2, Go 1.20); keep both in
// step.

import (
	"encoding/binary"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

type transportNet = transport.Net

func newStdNet() (transportNet, error) { return stdnet.NewNet() }

// stunRequestRealmNonce returns the realm and nonce of an authenticated
// STUN request. Other traffic (responses, indications, ChannelData) is
// skipped on the header alone.
func stunRequestRealmNonce(p []byte) (realm, nonce []byte, ok bool) {
	if len(p) < 20 || !stun.IsMessage(p) || binary.BigEndian.Uint16(p[0:2])&0x0110 != 0 { // class bits: request = 00
		return nil, nil, false
	}
	m := &stun.Message{Raw: append([]byte(nil), p...)}
	if m.Decode() != nil {
		return nil, nil, false
	}
	realm, err1 := m.Get(stun.AttrRealm)
	nonce, err2 := m.Get(stun.AttrNonce)
	if err1 != nil || err2 != nil {
		return nil, nil, false
	}
	return realm, nonce, true
}

// stunRefreshZero encodes a TURN Refresh with LIFETIME 0 (deallocation).
func stunRefreshZero(user, pass string, realm, nonce []byte) ([]byte, error) {
	m, err := stun.Build(stun.TransactionID, stun.NewType(stun.MethodRefresh, stun.ClassRequest),
		stun.RawAttribute{Type: stun.AttrLifetime, Value: []byte{0, 0, 0, 0}},
		stun.RawAttribute{Type: stun.AttrUsername, Value: []byte(user)},
		stun.RawAttribute{Type: stun.AttrRealm, Value: realm},
		stun.RawAttribute{Type: stun.AttrNonce, Value: nonce},
		stun.NewLongTermIntegrity(user, string(realm), pass),
		stun.Fingerprint,
	)
	if err != nil {
		return nil, err
	}
	return m.Raw, nil
}

// stunURIHostPort parses a stun: / turn: / turns: URL (port 0 when absent).
func stunURIHostPort(u string) (string, int, error) {
	uri, err := stun.ParseURI(u)
	if err != nil {
		return "", 0, err
	}
	return uri.Host, uri.Port, nil
}
