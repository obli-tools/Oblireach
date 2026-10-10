package supportlink

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"filippo.io/cpace"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/oblireach/supportlink/wire"
)

// pakeLabel versions every derivation: a future change of the binding gets
// a new label and old and new peers simply fail the key confirmation.
const pakeLabel = "oblireach-support/v1"

// pakeContext binds the exchange to the host ID and the server-assigned
// session id, so a PAKE message cannot be replayed into another session or
// against another host.
func pakeContext(hostID, sid string) *cpace.ContextInfo {
	return cpace.NewContextInfo("operator", "host:"+hostID, []byte(pakeLabel+"|"+sid))
}

// operatorPakeStart is the operator's first PAKE step.
func operatorPakeStart(code, hostID, sid string) ([]byte, *cpace.State, error) {
	return cpace.Start(code, pakeContext(hostID, sid))
}

// hostPakeExchange answers the operator's PAKE message and derives the key.
func hostPakeExchange(code, hostID, sid string, msgA []byte) (msgB, key []byte, err error) {
	return cpace.Exchange(code, pakeContext(hostID, sid), msgA)
}

// sessionKeys are the per-purpose keys expanded from the PAKE output. Each
// direction has its own keys so a message can never be reflected back to its
// sender.
type sessionKeys struct {
	confirmHost, confirmOp []byte // key confirmation MACs
	fpHost, fpOp           []byte // DTLS fingerprint MACs
	boxHost, boxOp         []byte // AEAD for SDP and candidates (sender's key)
	sas                    string // "418 207", read aloud if wanted
}

func deriveKeys(key []byte) sessionKeys {
	get := func(label string) []byte {
		out := make([]byte, 32)
		r := hkdf.New(sha256.New, key, nil, []byte(pakeLabel+" "+label))
		if _, err := r.Read(out); err != nil {
			panic(err) // 32 bytes from HKDF-SHA256 cannot fail
		}
		return out
	}
	s := get("sas")
	n := (uint32(s[0])<<16 | uint32(s[1])<<8 | uint32(s[2])) % 1000000
	return sessionKeys{
		confirmHost: get("confirm-host"), confirmOp: get("confirm-op"),
		fpHost: get("fp-host"), fpOp: get("fp-op"),
		boxHost: get("box-host"), boxOp: get("box-op"),
		sas: fmt.Sprintf("%03d %03d", n/1000, n%1000),
	}
}

func macB64(key []byte, data string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func checkMAC(key []byte, data, mac string) bool {
	want, err := base64.StdEncoding.DecodeString(mac)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return hmac.Equal(m.Sum(nil), want)
}

// Confirmation transcripts. The sid is inside so a confirmation from one
// session is worthless in another.
func hostConfirmData(sid string) string { return "host-confirm|" + sid }
func opConfirmData(sid string) string   { return "op-confirm|" + sid }

// seal encrypts a signalling payload with XChaCha20-Poly1305. The additional
// data binds the session and the message type, so the server cannot move a
// sealed candidate into an offer or into another session.
func seal(key []byte, sid, kind string, plain []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, plain, []byte(pakeLabel+"|"+sid+"|"+kind))
	return base64.StdEncoding.EncodeToString(out), nil
}

var errBox = errors.New("sealed payload not authenticated")

func open(key []byte, sid, kind, box string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(box)
	if err != nil {
		return nil, errBox
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, errBox
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(pakeLabel+"|"+sid+"|"+kind))
	if err != nil {
		return nil, errBox
	}
	return plain, nil
}

var fpRe = regexp.MustCompile(`(?m)^a=fingerprint:(\S+) (\S+)\s*$`)

// fingerprints extracts the a=fingerprint lines of an SDP, normalised and
// sorted, as the string that is MACed. An SDP without a fingerprint is an
// error: no DTLS means no end-to-end protection.
func fingerprints(sdp string) (string, error) {
	sdp = strings.ReplaceAll(sdp, "\r\n", "\n")
	ms := fpRe.FindAllStringSubmatch(sdp, -1)
	if len(ms) == 0 {
		return "", errors.New("no DTLS fingerprint in SDP")
	}
	set := map[string]bool{}
	for _, m := range ms {
		set[strings.ToLower(m[1])+" "+strings.ToUpper(m[2])] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ","), nil
}

// sealSDP prepares an offer/answer: the SDP sealed under the sender's box key
// and a MAC over its DTLS fingerprints under the sender's fingerprint key.
// The MAC is redundant with the AEAD today; it keeps the fingerprint binding
// explicit and independent of how the SDP is transported.
func sealSDP(boxKey, fpKey []byte, sid, kind, sdp string) (box, mac string, err error) {
	fps, err := fingerprints(sdp)
	if err != nil {
		return "", "", err
	}
	box, err = seal(boxKey, sid, kind, []byte(sdp))
	if err != nil {
		return "", "", err
	}
	return box, macB64(fpKey, sid+"|"+fps), nil
}

// openSDP reverses sealSDP; any failure is reported as ErrMITM.
func openSDP(boxKey, fpKey []byte, sid, kind, box, mac string) (string, error) {
	plain, err := open(boxKey, sid, kind, box)
	if err != nil {
		return "", fmt.Errorf("%w: %s %v", ErrMITM, kind, err)
	}
	return verifyFingerprints(fpKey, sid, string(plain), mac)
}

func verifyFingerprints(fpKey []byte, sid, sdp, mac string) (string, error) {
	fps, err := fingerprints(sdp)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrMITM, err)
	}
	if !checkMAC(fpKey, sid+"|"+fps, mac) {
		return "", fmt.Errorf("%w: DTLS fingerprint not bound to the code", ErrMITM)
	}
	return sdp, nil
}

// NewCode draws a fresh one-time code (9 digits) locally. It is never sent
// to the server.
func NewCode() string { return wire.RandomDigits(wire.CodeLen, false) }

// FormatDigits groups an ID or a code for display: "482 913 507".
func FormatDigits(s string) string { return wire.Group3(wire.Digits(s)) }
