// Package supportlink connects an Oblireach Support host (the person being
// helped) and an operator through an oblireach-rdv rendezvous server, then
// exposes the resulting WebRTC data channel as a FrameConn - the same frame
// contract as the agent's WebSocket (op 0x1 text, op 0x2 binary, one ordered
// stream across both kinds).
//
// Security model (see the wire package for the protocol):
//
//   - The host shows an ephemeral ID (assigned by the server) and a one-time
//     9-digit code drawn locally. The code never reaches the server.
//   - Host and operator run CPace (a balanced PAKE) with the code as password,
//     through the server. A server, or anyone else, gets one online guess per
//     code: the host burns the code after every failure and after every use.
//   - The PAKE key confirms both sides, derives a short verification code
//     (SAS) shown on both screens, seals SDP and ICE candidates (AEAD), and
//     authenticates each side's DTLS certificate fingerprint. A server that
//     substitutes its own DTLS endpoint (MITM) is detected before any frame
//     flows.
//   - Nothing happens without an explicit acceptance on the host side.
//
// Host side:
//
//	h, err := supportlink.Register(ctx, supportlink.HostConfig{Server: url, Approve: ask})
//	show(h.ID(), h.Code())
//	sess, err := h.WaitForOperator(ctx)
//	// sess is a FrameConn
//	// Report the operator (during the session or shortly after it), when
//	// the server gave a token (sess.ReportToken() != ""):
//	err = supportlink.SendReport(ctx, url, opts, supportlink.Report{SID: sess.SID(), Token: sess.ReportToken(), Reason: wire.ReportScam})
//
// Operator side:
//
//	sess, err := supportlink.Connect(ctx, supportlink.OperatorConfig{Server: url, Key: k, Name: n}, id, code)
package supportlink

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/oblireach/supportlink/wire"
)

// FrameConn is the transport contract of the ObliReach streaming engine: the
// same shape as the agent's wsConn. Op 0x1 carries text (JSON control), op
// 0x2 binary frames; like on a WebSocket, frames of both kinds are read in
// the order they were written. On a Session, op 0x8 closes, ops 0x9/0xA (ping/pong) are
// accepted and dropped (SCTP has its own liveness), and ReadFrame only ever
// returns 0x1 or 0x2, or an error (io.EOF after a clean close).
type FrameConn interface {
	WriteFrame(op byte, p []byte) error
	ReadFrame() (byte, []byte, error)
	Close() error
}

// Frame opcodes.
const (
	OpText   byte = 0x1
	OpBinary byte = 0x2
	OpClose  byte = 0x8
	OpPing   byte = 0x9
	OpPong   byte = 0xA
)

// Path is the network path a session ended up on.
type Path string

const (
	PathUnknown  Path = "unknown"
	PathDirect   Path = "direct"    // host/srflx/prflx pair, no relay
	PathRelayUDP Path = "relay_udp" // through the rdv TURN server, client->TURN over UDP
	PathRelayTCP Path = "relay_tcp" // through the rdv TURN server, client->TURN over TCP
	PathRelayTLS Path = "relay_tls" // through the rdv TURN server, client->TURN over TLS (443)
)

// Relayed reports whether the path goes through the TURN relay.
func (p Path) Relayed() bool { return p == PathRelayUDP || p == PathRelayTCP || p == PathRelayTLS }

// ICEOptions tune the WebRTC side. The zero value is the production default.
type ICEOptions struct {
	// ForceRelay sets iceTransportPolicy=relay (tests, or networks where the
	// direct path is known to be useless).
	ForceRelay bool
	// TURNTCPOnly keeps only the TURN servers over TCP and TLS
	// (turn:...?transport=tcp, turns:) and drops STUN: UDP-hostile networks.
	TURNTCPOnly bool
	// TURNTLSOnly keeps only the TURN-over-TLS servers (turns:, port 443 on
	// oblireach-rdv) and drops STUN: networks that only let HTTPS out. With
	// ForceRelay, the session goes through TURN-TLS or fails (tests).
	TURNTLSOnly bool
	// RelayReleaseAfter: once the selected candidate pair has been stable
	// for that long, the relay allocations it does not use are released
	// (all of them on a direct path). 0 = DefaultRelayReleaseAfter (3 s);
	// negative = keep them for the whole session (the previous behaviour).
	RelayReleaseAfter time.Duration
	// RelayAcceptanceMinWait is how long ICE waits for a direct pair before
	// nominating a relay pair. 0 = DefaultRelayAcceptanceMinWait; negative =
	// the WebRTC library default (2 s, which made relay setup about 2.2 s).
	RelayAcceptanceMinWait time.Duration
	// ConnectTimeout bounds ICE + DTLS + data channel opening after the
	// acceptance. 0 = 30 s.
	ConnectTimeout time.Duration
	// IncludeLoopback gathers loopback host candidates (local tests only).
	IncludeLoopback bool
	// IPFilter, when set, restricts the local addresses used for host
	// candidates.
	IPFilter func(net.IP) bool
}

// DefaultRelayAcceptanceMinWait replaces the WebRTC library's 2 s default:
// measured relay setup went from about 2.2 s to about 0.5 s, while a direct
// pair still wins whenever one exists within that window.
const DefaultRelayAcceptanceMinWait = 500 * time.Millisecond

// ServerError is an error code returned by the rendezvous server (or the
// peer's refusal). Compare with errors.Is against the Err* sentinels.
type ServerError struct {
	Code  string        // wire.Err* code
	Retry time.Duration // when the server gave one
	// Detail qualifies some codes: for ErrQuota, the quota reached
	// (wire.QuotaConcurrent, wire.QuotaDaily, wire.QuotaRelayMonth).
	Detail string
}

func (e *ServerError) Error() string {
	code := e.Code
	if e.Detail != "" {
		code += " " + e.Detail
	}
	if e.Retry > 0 {
		return fmt.Sprintf("rendezvous: %s (retry in %s)", code, e.Retry.Round(time.Second))
	}
	return "rendezvous: " + code
}

// Is matches the sentinel with the same code. An expired login
// (ErrAuthExpired) also matches ErrOperatorRequired: code written for
// operator keys keeps treating it as "not authorised".
func (e *ServerError) Is(target error) bool {
	t, ok := target.(*ServerError)
	if !ok {
		return false
	}
	return t.Code == e.Code || (e.Code == wire.ErrAuthExpired && t.Code == wire.ErrOperatorRequired)
}

// Sentinels for errors.Is.
var (
	ErrUnknownID        = &ServerError{Code: wire.ErrUnknownID}
	ErrRateLimited      = &ServerError{Code: wire.ErrRateLimited}
	ErrIDLocked         = &ServerError{Code: wire.ErrIDLocked}
	ErrWrongCode        = &ServerError{Code: wire.ErrBadCode}
	ErrExpired          = &ServerError{Code: wire.ErrExpired}
	ErrOperatorRequired = &ServerError{Code: wire.ErrOperatorRequired}
	ErrBanned           = &ServerError{Code: wire.ErrBanned}
	ErrUpgradeRequired  = &ServerError{Code: wire.ErrUpgradeRequired}
	ErrBusy             = &ServerError{Code: wire.ErrBusy}
	// ErrAuthExpired: the operator token (OperatorConfig.Token) is unknown,
	// expired, revoked or its account disabled: log in again. It also
	// matches ErrOperatorRequired.
	ErrAuthExpired = &ServerError{Code: wire.ErrAuthExpired}
	// ErrQuota: an operator account quota is reached; Detail of the
	// *ServerError names it (errors.As).
	ErrQuota    = &ServerError{Code: wire.ErrQuota}
	ErrRejected = &ServerError{Code: "rejected"}
	ErrPeerGone = &ServerError{Code: "peer_gone"}
)

// ErrMITM means a signalling payload or a DTLS fingerprint was not
// authenticated by the code: the rendezvous server (or the network in front
// of it) tampered with the session. The session is aborted before any frame
// flows.
var ErrMITM = errors.New("supportlink: signalling not authenticated by the code (possible MITM) - aborted")

// ErrInvalidCode / ErrInvalidID: malformed input, rejected locally before
// spending an attempt on the server.
var (
	ErrInvalidCode = errors.New("supportlink: the code must be 9 digits")
	ErrInvalidID   = errors.New("supportlink: the ID must be 9 digits")
)

// ErrUnreachable: the rendezvous server could not be reached (DNS, TCP,
// TLS, proxy, WebSocket handshake) or the signalling connection was lost
// before the session was set up. The underlying error is wrapped too.
var ErrUnreachable = errors.New("supportlink: rendezvous server unreachable")

// ErrInvalidServer: the server setting is not a usable address (empty,
// unparsable, unsupported scheme, or plain ws:// to a remote host).
var ErrInvalidServer = errors.New("supportlink: invalid rendezvous server address")

// UpgradeSide tells which side of an incompatibility must be updated.
type UpgradeSide string

const (
	UpgradeClient UpgradeSide = "client" // this application is too old for the server
	UpgradeServer UpgradeSide = "server" // the rendezvous server is too old for this application
)

// UpgradeError is the server's upgrade_required answer, with what it said.
// errors.Is(err, ErrUpgradeRequired) matches it.
type UpgradeError struct {
	// Side is the side to update.
	Side UpgradeSide
	// MinVersion is the minimum client version the server accepts ("" when
	// it has none, or did not say).
	MinVersion string
	// ServerProto is the signalling protocol version the server speaks (0
	// when it did not say: a server older than protocol 2).
	ServerProto int
}

func (e *UpgradeError) Error() string {
	if e.Side == UpgradeServer {
		return fmt.Sprintf("rendezvous: upgrade_required (the server is too old: protocol %d, this client speaks %d)", e.ServerProto, wire.ProtocolVersion)
	}
	if e.MinVersion != "" {
		return "rendezvous: upgrade_required (this client is too old: the server requires version " + e.MinVersion + " or later)"
	}
	return "rendezvous: upgrade_required (this client is too old for the server)"
}

// Is matches ErrUpgradeRequired.
func (e *UpgradeError) Is(target error) bool {
	t, ok := target.(*ServerError)
	return ok && t.Code == wire.ErrUpgradeRequired
}

func upgradeError(m wire.Msg) *UpgradeError {
	e := &UpgradeError{Side: UpgradeClient, MinVersion: cleanVersion(m.Reason), ServerProto: m.Proto}
	// A server that does not announce its protocol predates protocol 2;
	// one that speaks an older protocol than this client must be updated.
	if m.Proto < wire.ProtocolVersion {
		e.Side = UpgradeServer
	}
	return e
}

// cleanVersion keeps a short dotted version string received from the
// network, or nothing.
func cleanVersion(v string) string {
	if len(v) > 32 {
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= '0' && c <= '9' || c == '.' || c == 'v') {
			return ""
		}
	}
	return v
}

func serverError(m wire.Msg) error {
	switch m.Err {
	case "":
		return &ServerError{Code: "unknown_error"}
	case wire.ErrUpgradeRequired:
		return upgradeError(m)
	}
	e := &ServerError{Code: m.Err, Retry: time.Duration(m.Retry) * time.Second}
	if m.Err == wire.ErrQuota {
		e.Detail = cleanDetail(m.Reason)
	}
	return e
}

// cleanDetail keeps a short lowercase identifier received from the network.
func cleanDetail(v string) string {
	if len(v) > 32 {
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= 'a' && c <= 'z' || c == '_') {
			return ""
		}
	}
	return v
}

func logger(l *slogLogger) *slogLogger {
	if l != nil {
		return l
	}
	return discardLogger()
}
