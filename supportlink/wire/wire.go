// Package wire is the signalling protocol shared by oblireach-rdv (the
// rendezvous server) and supportlink (host and operator clients).
//
// One JSON envelope (Msg) travels over a WebSocket at Path. The server routes
// opaque payloads between a host (the person being helped) and an operator;
// it never sees the one-time code (CPace PAKE between the peers) and, from
// protocol version 1, never sees SDP or ICE candidates in clear either: they
// travel in Box, sealed under a key derived from the PAKE.
//
// This package has no dependency besides the standard library so the server
// can import it without pulling WebRTC.
package wire

import (
	"crypto/rand"
	"math/big"
	"strings"
)

// ProtocolVersion is announced in hello. The server accepts only its own
// version, so both peers of a session always speak the same one; a refusal
// is an error upgrade_required carrying the server's version in Proto and
// its minimum client version in Reason (the client tells from Proto which
// side is too old).
//
// Version 2: text and binary frames share one ordered data channel (id 0,
// header byte = op | fin), instead of two channels (ctl id 0, video id 1)
// that did not keep the order between the two kinds.
const ProtocolVersion = 2

// Path is the signalling WebSocket endpoint.
const Path = "/v1/ws"

// Roles announced in hello.
const (
	RoleHost     = "host"
	RoleOperator = "operator"
)

// Message types.
const (
	THello      = "hello"      // C->S: proto, ver, role
	TRegister   = "register"   // H->S: ask for an ephemeral ID (again after id_locked); id + resume to get the same ID back after a drop
	TRegistered = "registered" // S->H: id, ttl (resume grace in seconds), resume (secret for the next resume)
	TConnect    = "connect"    // O->S: id, key (instance operator key) and/or token (operator account)
	TSession    = "session"    // S->O: sid
	TPake       = "pake"       // O->H: CPace message A
	TPakeReply  = "pake_reply" // H->O: CPace message B + host key confirmation
	TConfOp     = "conf_op"    // O->H: operator key confirmation + display name (server adds account)
	TPakeOK     = "pake_ok"    // H->S: operator confirmation verified (consumed by the server)
	TPakeFailed = "pake_failed"
	TAccept     = "accept" // H->S->O: explicit human acceptance (S adds ice)
	TReject     = "reject"
	TICE        = "ice"    // S->H: ICE servers + TURN credentials of the accepted session (+ report token)
	TOffer      = "offer"  // H->O: box(SDP) + MAC over the DTLS fingerprints
	TAnswer     = "answer" // O->H: same
	TCand       = "cand"   // both: box(candidate)
	TPath       = "path"   // C->S: selected path, for metrics/logs only
	TBye        = "bye"
	TError      = "error"
)

// Error codes carried in Msg.Err. The server answers unknown_id identically
// whether an ID is offline or never existed, so presence cannot be probed.
const (
	ErrUnknownID        = "unknown_id"
	ErrRateLimited      = "rate_limited"
	ErrIDLocked         = "id_locked"
	ErrBadCode          = "bad_code"
	ErrExpired          = "expired"
	ErrRelayQuota       = "relay_quota"
	ErrOperatorRequired = "operator_required"
	ErrBanned           = "banned"
	ErrUpgradeRequired  = "upgrade_required"
	ErrBusy             = "busy"
	ErrNoSession        = "no_session"
	ErrBadRequest       = "bad_request"
	// ErrAuthExpired: the operator account token is unknown, expired,
	// revoked or its account disabled. The operator must log in again.
	ErrAuthExpired = "auth_expired"
	// ErrQuota: an operator account quota is reached. Reason names it
	// (QuotaConcurrent, QuotaDaily, QuotaRelayMonth); Retry, when set, is
	// the time until it resets.
	ErrQuota = "quota_exceeded"
	// ErrUnavailable: a temporary server-side failure (retry later).
	ErrUnavailable = "unavailable"
)

// Quota names carried in Reason with ErrQuota.
const (
	QuotaConcurrent = "concurrent_sessions"
	QuotaDaily      = "sessions_per_day"
	QuotaRelayMonth = "relay_month"
)

// Msg is the single JSON envelope of the signalling WebSocket.
type Msg struct {
	T       string   `json:"t"`
	Proto   int      `json:"proto,omitempty"`   // hello: protocol version; error upgrade_required: the server's
	Ver     string   `json:"ver,omitempty"`     // hello: client version ("1.2.3")
	Role    string   `json:"role,omitempty"`    // hello
	ID      string   `json:"id,omitempty"`      // host ID (9 digits)
	SID     string   `json:"sid,omitempty"`     // session id assigned by the server
	Key     string   `json:"key,omitempty"`     // connect: operator key of the instance
	Token   string   `json:"token,omitempty"`   // connect: operator account token (login)
	Resume  string   `json:"resume,omitempty"`  // register / registered: resume secret of the ID (host memory only)
	Name    string   `json:"name,omitempty"`    // conf_op: operator display name (self-declared)
	Account string   `json:"account,omitempty"` // conf_op: operator account, set by the server only
	Pake    string   `json:"pake,omitempty"`    // base64 CPace message
	Conf    string   `json:"conf,omitempty"`    // base64 key-confirmation MAC
	Box     string   `json:"box,omitempty"`     // base64 AEAD-sealed SDP or candidate
	MAC     string   `json:"mac,omitempty"`     // base64 MAC over the DTLS fingerprints
	Opts    []string `json:"opts,omitempty"`    // accept: rights granted by the host
	ICE     *ICEInfo `json:"ice,omitempty"`     // accept / ice
	Path    string   `json:"path,omitempty"`    // path: direct | relay_udp | relay_tcp | relay_tls
	TTL     int      `json:"ttl,omitempty"`     // registered: seconds; ice: credential lifetime
	Err     string   `json:"err,omitempty"`     // error code
	Retry   int      `json:"retry,omitempty"`   // seconds before retrying
	Reason  string   `json:"reason,omitempty"`  // bye / error detail (never content)
	// Report is the abuse-report token of an accepted session, sent by the
	// server to the HOST only, in the ice message (host memory only). It
	// authenticates one report about that session at ReportPath, during the
	// session and for a while after it. Servers without reports omit it;
	// clients that do not know it ignore it (no protocol version change).
	Report string `json:"report,omitempty"`
}

// ReportPath is the HTTPS endpoint of abuse reports (POST, JSON
// ReportRequest). Answers: 200 {"ok":true}; 400 bad_request; 403
// invalid_report (unknown session, wrong token or window over: one answer
// for all); 404 reports_disabled; 409 already_reported; 429 rate_limited
// (Retry-After); 503 unavailable.
const ReportPath = "/v1/report"

// Report reasons (ReportRequest.Reason).
const (
	ReportScam       = "scam"
	ReportUnwanted   = "unwanted_access"
	ReportHarassment = "harassment"
	ReportOther      = "other"
)

// ReportReasons lists the valid reasons, in display order.
var ReportReasons = []string{ReportScam, ReportUnwanted, ReportHarassment, ReportOther}

// ValidReportReason reports whether r is one of ReportReasons.
func ValidReportReason(r string) bool {
	for _, v := range ReportReasons {
		if r == v {
			return true
		}
	}
	return false
}

// ReportMaxText bounds the free text of a report, in characters (runes).
const ReportMaxText = 1000

// ReportRequest is the body of a report. The person helped (the host) sends
// it; the server binds it to the session SID through Token, so a report can
// only be about a real session that this host accepted.
type ReportRequest struct {
	SID    string `json:"sid"`
	Token  string `json:"token"`
	Reason string `json:"reason"`
	Text   string `json:"text,omitempty"`
	// End asks the server to end the session at once when it still runs.
	End bool `json:"end,omitempty"`
}

// CleanReportText normalises the free text of a report: line breaks kept
// (CR LF and CR become LF, at most two in a row), tabs become spaces, other
// control and bidirectional-override characters are dropped, the ends are
// trimmed, and the text is cut to ReportMaxText characters. Clients and the
// server apply it alike.
func CleanReportText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	n, breaks := 0, 0
	for _, r := range s {
		switch {
		case r == '\n':
			breaks++
			if breaks > 2 {
				continue
			}
		case r == '\t':
			r = ' '
			breaks = 0
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			continue
		case r == '‎' || r == '‏' || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩'):
			continue
		default:
			breaks = 0
		}
		if n == ReportMaxText {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// Report error codes (JSON "error" of a refused report).
const (
	ErrInvalidReport   = "invalid_report"
	ErrAlreadyReported = "already_reported"
	ErrReportsDisabled = "reports_disabled"
)

// ICEInfo lists the ICE servers of an accepted session. URLs mixes stun: and
// turn: entries; Username/Credential apply to the turn: ones (TURN REST
// credentials bound to the session).
type ICEInfo struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
	TTL        int      `json:"ttl,omitempty"`
}

// IDLen and CodeLen: the ID and the one-time code are both 9 digits,
// displayed in groups of three ("482 913 507").
const (
	IDLen   = 9
	CodeLen = 9
)

// Digits keeps the decimal digits of s, dropping spaces, dashes, dots and any
// other separator users type when reading a number over the phone.
func Digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidID reports whether s (already normalised) is a well-formed host ID.
func ValidID(s string) bool { return len(s) == IDLen && allDigits(s) && s[0] != '0' }

// ValidCode reports whether s (already normalised) is a well-formed code.
func ValidCode(s string) bool { return len(s) == CodeLen && allDigits(s) }

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Group3 formats a digit string in groups of three: "482913507" -> "482 913 507".
func Group3(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// RandomDigits returns n uniformly random decimal digits from crypto/rand.
// With nonZeroFirst the first digit is 1-9 so the value never looks shorter
// than n digits.
func RandomDigits(n int, nonZeroFirst bool) string {
	b := make([]byte, n)
	for i := range b {
		max := int64(10)
		off := byte('0')
		if i == 0 && nonZeroFirst {
			max, off = 9, '1'
		}
		v, err := rand.Int(rand.Reader, big.NewInt(max))
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		b[i] = off + byte(v.Int64())
	}
	return string(b)
}

// TruncID hides all but the first three digits of an ID for logs.
func TruncID(id string) string {
	if len(id) <= 3 {
		return "***"
	}
	return id[:3] + strings.Repeat("*", len(id)-3)
}

// TruncSID shortens a session id for logs.
func TruncSID(sid string) string {
	if len(sid) <= 8 {
		return sid
	}
	return sid[:8]
}
